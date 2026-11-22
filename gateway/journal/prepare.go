package journal

import (
	"crypto/sha256"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// PrepareMode selects the prepare path.
type PrepareMode uint8

const (
	// PrepareNormal allocates a fresh reservation and sequence range.
	PrepareNormal PrepareMode = iota
	// PrepareImportSealed recovers a SEALED producer batch after gateway
	// store loss: the original seed is supplied, no new sequence is
	// allocated, and the original issuer epoch is preserved
	// (CONTROL_WIRE_CONTRACT, IMPORT_SEALED).
	PrepareImportSealed
)

// PrepareResume is the BeginPrepare answer: exactly one of ResumeOffset or
// Reservation is set.
type PrepareResume struct {
	// ResumeOffset is the next expected manifest chunk offset when the
	// stream is still open.
	ResumeOffset uint64
	// Reservation is the committed reservation when the batch is already
	// prepared.
	Reservation *Reservation
}

// BeginPrepare opens or resumes the PREPARING phase of one batch. Rules:
//
//   - seq at or below the retired prefix answers ErrAlreadyRetired without
//     creating any key.
//   - A batch already prepared with the same manifest digest returns its
//     saved reservation byte-for-byte; a different digest is an immutable
//     conflict (the reservation retry never allocates a new sequence).
//   - A still-open stream with a different bound digest is an immutable
//     conflict; with the same digest it resumes at the durable offset.
//
// In IMPORT_SEALED mode the exact original seed bytes must accompany the
// call; they are validated against the batch identity, manifest digest,
// event count and binding, and stored unchanged.
func (j *Journal) BeginPrepare(id BatchID, manifestDigest [32]byte, eventCount, descriptorBytesTotal uint64, mode PrepareMode, importSeed []byte) (PrepareResume, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if id.Seq <= j.readRetired(id) {
		return PrepareResume{}, ErrAlreadyRetired
	}
	if eventCount == 0 {
		return PrepareResume{}, ErrIncomplete
	}

	if raw, err := j.st.Get(batchKey(id, "/reservation")); err == nil {
		res, derr := decodeReservation(raw)
		if derr != nil {
			return PrepareResume{}, derr
		}
		if res.ManifestDigest != manifestDigest {
			return PrepareResume{}, ErrImmutableConflict
		}
		if mode == PrepareImportSealed != res.Import {
			return PrepareResume{}, ErrModeConflict
		}
		return PrepareResume{Reservation: &res}, nil
	} else if err != storage.ErrNotFound {
		return PrepareResume{}, err
	}

	if raw, err := j.st.Get(batchKey(id, "/prepare_meta")); err == nil {
		meta, derr := decodePrepareMeta(raw)
		if derr != nil {
			return PrepareResume{}, derr
		}
		if meta.ManifestDigest != manifestDigest {
			return PrepareResume{}, ErrImmutableConflict
		}
		cover, _, cerr := chunkCover(j.st, batchKey(id, "prepare/"))
		if cerr != nil {
			return PrepareResume{}, cerr
		}
		return PrepareResume{ResumeOffset: cover}, nil
	} else if err != storage.ErrNotFound {
		return PrepareResume{}, err
	}

	var res Reservation
	if mode == PrepareImportSealed {
		l, err := ParseSeed(importSeed)
		if err != nil {
			return PrepareResume{}, err
		}
		if l.Batch != id || l.ManifestDigest != manifestDigest || l.EventCount != eventCount ||
			l.BindingDigest != j.bnd {
			return PrepareResume{}, ErrInvalidSeed
		}
		res = Reservation{
			Seed:           append([]byte(nil), importSeed...),
			ReservationID:  l.ReservationID,
			IssuerEpoch:    l.IssuerEpoch,
			ManifestDigest: l.ManifestDigest,
			EventCount:     l.EventCount,
			SequenceBase:   l.SequenceBase,
			BindingDigest:  l.BindingDigest,
			FrameLimit:     l.FrameLimit,
			Import:         true,
		}
	}

	meta := PrepareMeta{ManifestDigest: manifestDigest, EventCount: eventCount, Total: descriptorBytesTotal, Import: mode == PrepareImportSealed}
	b := storage.NewBatch()
	b.Put(batchKey(id, "/prepare_meta"), encodePrepareMeta(meta))
	if mode == PrepareImportSealed {
		// The imported seed is durable from the first chunk on: even a crash
		// before FinalizePrepare never re-allocates anything.
		b.Put(batchKey(id, "/reservation"), encodeReservation(res))
	}
	j.putState(b, id, StatePreparing)
	return PrepareResume{}, j.st.Apply(b)
}

// PrepareChunk stores one bounded manifest descriptor chunk. The same bytes
// at the same offset are idempotent and return the same next offset; a hole
// returns ErrOffsetMismatch with the expected cursor as nextOffset; a
// conflicting rewrite is an immutable conflict. After the reservation exists
// the stream is closed: retries of stored chunks stay no-ops, extensions
// are rejected.
func (j *Journal) PrepareChunk(id BatchID, manifestDigest [32]byte, offset uint64, chunkSHA [32]byte, data []byte) (nextOffset uint64, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	state, err := j.readState(id)
	if err != nil {
		return 0, err
	}
	meta, err := j.readPrepareMeta(id)
	if err != nil {
		return 0, err
	}
	if meta.ManifestDigest != manifestDigest {
		return 0, ErrImmutableConflict
	}
	if len(data) > MaxDataChunkBytes {
		return 0, ErrChunkTooLarge
	}
	if sha256.Sum256(data) != chunkSHA {
		return 0, ErrPayloadDigest
	}

	prefix := batchKey(id, "prepare/")
	cover, _, err := chunkCover(j.st, prefix)
	if err != nil {
		return 0, err
	}
	key := append(append([]byte(nil), prefix...), u64be(offset)...)
	switch {
	case offset == cover:
		if state != StatePreparing {
			return cover, ErrStateConflict // the stream is closed
		}
		if err := j.st.Put(key, append([]byte(nil), data...)); err != nil {
			return 0, err
		}
		return cover + uint64(len(data)), nil
	case offset > cover:
		return cover, ErrOffsetMismatch
	default:
		old, err := j.st.Get(key)
		if err == storage.ErrNotFound {
			return cover, ErrOffsetMismatch
		}
		if err != nil {
			return 0, err
		}
		if string(old) != string(data) {
			return cover, ErrImmutableConflict
		}
		return cover, nil
	}
}

// FinalizePrepare closes the manifest stream and commits the reservation.
// Normal mode allocates the sequence range and reservation id atomically
// with the PREPARED state; a retry with the same batch and digest returns
// the saved seed byte-for-byte and consumes nothing. The full contiguous
// stream must rehash to the declared manifest digest.
func (j *Journal) FinalizePrepare(id BatchID, manifestDigest [32]byte) (Reservation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if raw, err := j.st.Get(batchKey(id, "/reservation")); err == nil {
		res, derr := decodeReservation(raw)
		if derr != nil {
			return Reservation{}, derr
		}
		if res.ManifestDigest != manifestDigest {
			return Reservation{}, ErrImmutableConflict
		}
		// An IMPORT_SEALED reservation is durable from BeginPrepare while
		// the batch is still PREPARING: a retry here must still verify the
		// manifest and switch to PREPARED, not short-circuit.
		state, err := j.readState(id)
		if err != nil {
			return Reservation{}, err
		}
		if state != StatePreparing {
			return res, nil
		}
		if !res.Import {
			return Reservation{}, errCorrupt
		}
	} else if err != storage.ErrNotFound {
		return Reservation{}, err
	}

	state, err := j.readState(id)
	if err != nil {
		return Reservation{}, err
	}
	if state != StatePreparing {
		return Reservation{}, ErrStateConflict
	}
	meta, err := j.readPrepareMeta(id)
	if err != nil {
		return Reservation{}, err
	}
	if meta.ManifestDigest != manifestDigest {
		return Reservation{}, ErrImmutableConflict
	}

	prefix := batchKey(id, "prepare/")
	cover, _, err := chunkCover(j.st, prefix)
	if err != nil {
		return Reservation{}, err
	}
	if cover != meta.Total {
		return Reservation{}, ErrIncomplete
	}
	h := sha256.New()
	if err := hashChunks(j.st, prefix, func(p []byte) { h.Write(p) }); err != nil {
		return Reservation{}, err
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	if digest != manifestDigest {
		return Reservation{}, ErrManifestMismatch
	}

	var res Reservation
	if meta.Import {
		// The seed became durable in BeginPrepare; it only needs the state
		// switch now.
		raw, err := j.st.Get(batchKey(id, "/reservation"))
		if err != nil {
			return Reservation{}, err
		}
		if res, err = decodeReservation(raw); err != nil {
			return Reservation{}, err
		}
	} else {
		if j.ct.SequenceNext == ^uint64(0) || j.ct.SequenceNext+meta.EventCount < j.ct.SequenceNext {
			return Reservation{}, ErrCounterOverflow
		}
		if j.ct.ReservationNext == ^uint64(0) {
			return Reservation{}, ErrCounterOverflow
		}
		rid, err := newReservationID()
		if err != nil {
			return Reservation{}, err
		}
		res = Reservation{
			ReservationID:  rid,
			IssuerEpoch:    j.epoch,
			ManifestDigest: manifestDigest,
			EventCount:     meta.EventCount,
			SequenceBase:   j.ct.SequenceNext,
			BindingDigest:  j.bnd,
			FrameLimit:     j.frames,
		}
		res.Seed = BuildSeed(SeedLayout{
			ReservationID:  res.ReservationID,
			IssuerEpoch:    res.IssuerEpoch,
			Batch:          id,
			ManifestDigest: res.ManifestDigest,
			EventCount:     res.EventCount,
			SequenceBase:   res.SequenceBase,
			BindingDigest:  res.BindingDigest,
			FrameLimit:     res.FrameLimit,
		})
	}

	b := storage.NewBatch()
	if !meta.Import {
		j.ct.SequenceNext += res.EventCount
		j.ct.ReservationNext++
		b.Put([]byte(keyCounters), encodeCounters(j.ct))
	}
	b.Put(batchKey(id, "/reservation"), encodeReservation(res))
	j.putState(b, id, StatePrepared)
	if err := j.st.Apply(b); err != nil {
		return Reservation{}, err
	}
	return res, nil
}

func (j *Journal) readPrepareMeta(id BatchID) (PrepareMeta, error) {
	raw, err := j.st.Get(batchKey(id, "/prepare_meta"))
	if err == storage.ErrNotFound {
		return PrepareMeta{}, ErrBatchNotFound
	}
	if err != nil {
		return PrepareMeta{}, err
	}
	return decodePrepareMeta(raw)
}
