package producer

import (
	"crypto/sha256"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// Attempt describes one assembly attempt of a batch: the same batch_id and
// semantic identity, a new attempt generation and a fresh event sequence
// range.
type Attempt struct {
	BatchSeq          uint64
	Generation        uint64
	EventSequenceBase uint64
	EventCount        uint64
}

// AllocateBatch allocates batch_seq atomically with the durable delivery
// intent for a native decision. A replayed decision returns its original
// batch with replayed=true and does not consume a new batch_seq; speculative
// or rejected native input never reaches this call. Only LIVE admits new
// capture, and the destination binding must already be saved.
func (s *Store) AllocateBatch(decision uint64, eventCount uint64) (Attempt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if raw, err := s.st.Get(decisionKey(decision)); err == nil {
		seq := beU64(raw)
		intent, err := s.readIntent(seq)
		if err != nil {
			return Attempt{}, false, err
		}
		// A replayed marker keeps the original event count; a different
		// count for the same decision is a semantic conflict, not a new
		// batch.
		if intent.EventCount != eventCount {
			return Attempt{}, true, ErrImmutableConflict
		}
		return Attempt{
			BatchSeq:          seq,
			Generation:        intent.AttemptGeneration,
			EventSequenceBase: intent.EventSequenceBase,
			EventCount:        intent.EventCount,
		}, true, nil
	} else if err != storage.ErrNotFound {
		return Attempt{}, false, err
	}

	if s.md.Mode != ModeLIVE {
		return Attempt{}, false, ErrModeConflict
	}
	if eventCount == 0 {
		return Attempt{}, false, ErrEmptyBatch
	}
	if s.bnd == ([32]byte{}) {
		return Attempt{}, false, ErrBindingRequired
	}
	if s.ct.BatchSeqNext == ^uint64(0) {
		return Attempt{}, false, ErrCounterOverflow
	}

	seq := s.ct.BatchSeqNext
	s.ct.BatchSeqNext++
	intent := Intent{
		Decision:            decision,
		AttemptGeneration:   0, // no assembly attempt yet (zero is the empty frontier)
		EventSequenceBase:   0,
		EventCount:          eventCount,
		AdmissionGeneration: s.md.AdmissionGeneration,
	}
	b := storage.NewBatch()
	b.Put(decisionKey(decision), u64be(seq))
	b.Put(batchKey(seq, "/intent"), encodeIntent(intent))
	s.putState(b, seq, StateIntent)
	b.Put([]byte(keyCounters), encodeCounters(s.ct))
	if err := s.st.Apply(b); err != nil {
		return Attempt{}, false, err
	}
	return Attempt{BatchSeq: seq, Generation: 0, EventCount: eventCount}, false, nil
}

// StartAttempt begins a STAGING (re)assembly: it increments the attempt
// generation and reserves a fresh event_sequence range atomically. The
// semantic identity (batch_id, decision, manifest semantics) is unchanged;
// the previous attempt's recovery inputs stay until a later CommitReady
// replaces them. Only INTENT and STAGING batches accept a new attempt.
func (s *Store) StartAttempt(seq uint64, eventCount uint64) (Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.readState(seq)
	if err != nil {
		return Attempt{}, err
	}
	if state != StateIntent && state != StateStaging {
		return Attempt{}, ErrStateConflict
	}
	intent, err := s.readIntent(seq)
	if err != nil {
		return Attempt{}, err
	}
	if intent.EventCount != eventCount {
		return Attempt{}, ErrImmutableConflict
	}
	if s.md.Mode == ModeBLOCKED {
		return Attempt{}, ErrModeConflict
	}
	if s.ct.EventSequenceNext == ^uint64(0) || s.ct.EventSequenceNext+eventCount < s.ct.EventSequenceNext {
		return Attempt{}, ErrCounterOverflow
	}

	base := s.ct.EventSequenceNext
	s.ct.EventSequenceNext += eventCount
	intent.AttemptGeneration++
	intent.EventSequenceBase = base
	b := storage.NewBatch()
	b.Put(batchKey(seq, "/intent"), encodeIntent(intent))
	s.putState(b, seq, StateStaging)
	b.Put([]byte(keyCounters), encodeCounters(s.ct))
	if err := s.st.Apply(b); err != nil {
		return Attempt{}, err
	}
	return Attempt{
		BatchSeq:          seq,
		Generation:        intent.AttemptGeneration,
		EventSequenceBase: base,
		EventCount:        eventCount,
	}, nil
}

// WriteAttemptData appends one bounded payload chunk of the current attempt
// (immutable RecordFragment bytes on disk). Offsets are contiguous; a
// retried identical chunk is a no-op, different bytes under a written range
// are an immutable conflict.
func (s *Store) WriteAttemptData(seq, generation, offset uint64, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireAttempt(seq, generation); err != nil {
		return err
	}
	return writeChunk(s.st, attemptKey(seq, generation, "data"), offset, data)
}

// WriteAttemptManifest appends one bounded chunk of the manifest byte stream
// ("extractor.manifest.v2" bytes, CONTROL_WIRE_CONTRACT "READY manifest and
// reservation") of the current attempt.
func (s *Store) WriteAttemptManifest(seq, generation, offset uint64, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireAttempt(seq, generation); err != nil {
		return err
	}
	return writeChunk(s.st, attemptKey(seq, generation, "manifest"), offset, data)
}

// CommitReady is the closing READY commit: with one atomic batch it verifies
// the manifest stream, records the manifest digest and event count, saves the
// capture/lifecycle checkpoint, switches the state to READY and prunes the
// replaced earlier attempt generations. After READY the manifest digest and
// event identity are immutable. Retrying the same commit is a no-op; a
// different manifest under the same batch is ErrImmutableConflict.
func (s *Store) CommitReady(seq, generation uint64, manifestLength uint64, checkpoint []byte) (Ready, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.readState(seq)
	if err != nil {
		return Ready{}, err
	}
	intent, err := s.readIntent(seq)
	if err != nil {
		return Ready{}, err
	}

	cover, digest, err := manifestDigest(s.st, attemptKey(seq, generation, "manifest"))
	if err != nil {
		return Ready{}, err
	}
	ready := Ready{
		ManifestDigest: digest,
		ManifestLength: manifestLength,
		EventCount:     intent.EventCount,
		Attempt:        generation,
	}

	switch state {
	case StateReady, StateSealed, StateAckedCleanup, StateRetired:
		old, err := s.readReady(seq)
		if err != nil {
			return Ready{}, err
		}
		if old.Attempt != generation {
			return Ready{}, ErrStateConflict
		}
		// The manifest bytes are still verifiable while they exist (before
		// payload cleanup). Post-cleanup the recorded marker is the answer.
		if cover == manifestLength && old.ManifestDigest != digest {
			return Ready{}, ErrImmutableConflict
		}
		if cover != manifestLength && (old.ManifestLength != manifestLength || cover != 0) {
			return Ready{}, ErrImmutableConflict
		}
		return old, nil
	case StateStaging:
		if intent.AttemptGeneration != generation {
			return Ready{}, ErrStateConflict
		}
	default:
		return Ready{}, ErrStateConflict
	}
	if cover != manifestLength {
		return Ready{}, ErrOffsetGap
	}

	b := storage.NewBatch()
	b.Put(batchKey(seq, "/ready"), encodeReady(ready))
	b.Put(batchKey(seq, "/checkpoint"), append([]byte(nil), checkpoint...))
	s.putState(b, seq, StateReady)
	// The winning attempt replaces the recovery inputs of all earlier
	// generations in the same closing commit.
	lo, hi := keyRange(attemptRoot(seq))
	var doomed [][]byte
	if err := s.st.Iterate(lo, hi, func(k, _ []byte) error {
		if genOfAttemptKey(k, seq) < generation {
			doomed = append(doomed, append([]byte(nil), k...))
		}
		return nil
	}); err != nil {
		return Ready{}, err
	}
	for _, k := range doomed {
		b.Delete(k)
	}
	if err := s.st.Apply(b); err != nil {
		return Ready{}, err
	}
	return ready, nil
}

func (s *Store) requireAttempt(seq, generation uint64) error {
	state, err := s.readState(seq)
	if err != nil {
		return err
	}
	if state != StateIntent && state != StateStaging {
		return ErrStateConflict
	}
	intent, err := s.readIntent(seq)
	if err != nil {
		return err
	}
	if intent.AttemptGeneration != generation {
		return ErrStateConflict
	}
	return nil
}

func (s *Store) readIntent(seq uint64) (Intent, error) {
	raw, err := s.st.Get(batchKey(seq, "/intent"))
	if err == storage.ErrNotFound {
		return Intent{}, ErrNotFound
	}
	if err != nil {
		return Intent{}, err
	}
	return decodeIntent(raw)
}

func (s *Store) readReady(seq uint64) (Ready, error) {
	raw, err := s.st.Get(batchKey(seq, "/ready"))
	if err == storage.ErrNotFound {
		return Ready{}, ErrNotFound
	}
	if err != nil {
		return Ready{}, err
	}
	return decodeReady(raw)
}

// attemptRoot is the parent of all attempt keys of a batch.
func attemptRoot(seq uint64) []byte {
	return batchKey(seq, "attempt/")
}

// genOfAttemptKey extracts the generation from
// batch/<seq>/attempt/<gen>/...; it returns 0 for malformed keys.
func genOfAttemptKey(k []byte, seq uint64) uint64 {
	prefix := attemptRoot(seq)
	if len(k) <= len(prefix)+8 || string(k[:len(prefix)]) != string(prefix) {
		return 0
	}
	return beU64(k[len(prefix) : len(prefix)+8])
}

// writeChunk writes one offset-addressed chunk. A chunk identical to the
// stored bytes at that offset is a no-op (idempotent retry); a hole or a
// conflicting rewrite is an error, never a partial write.
func writeChunk(st storage.Storage, prefix []byte, offset uint64, data []byte) error {
	cover, _, err := chunkCover(st, prefix)
	if err != nil {
		return err
	}
	key := append(append([]byte(nil), prefix...), u64be(offset)...)
	switch {
	case offset == cover:
		return st.Put(key, append([]byte(nil), data...))
	case offset > cover:
		return ErrOffsetGap
	default:
		old, err := st.Get(key)
		if err == storage.ErrNotFound {
			return ErrOffsetGap
		}
		if err != nil {
			return err
		}
		if string(old) == string(data) {
			return nil
		}
		return ErrImmutableConflict
	}
}

// chunkCover returns the contiguous covered length from offset 0 and the
// number of stored chunks. Any gap is an error.
func chunkCover(st storage.Storage, prefix []byte) (uint64, int, error) {
	lo, hi := keyRange(prefix)
	var (
		cover uint64
		n     int
	)
	err := st.Iterate(lo, hi, func(k, v []byte) error {
		off := beU64(k[len(prefix):])
		if off != cover {
			return ErrOffsetGap
		}
		cover += uint64(len(v))
		n++
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return cover, n, nil
}

// manifestDigest verifies the manifest chunk stream is gap-free and returns
// its covered length and SHA-256 digest.
func manifestDigest(st storage.Storage, prefix []byte) (uint64, [32]byte, error) {
	lo, hi := keyRange(prefix)
	h := sha256.New()
	var cover uint64
	err := st.Iterate(lo, hi, func(k, v []byte) error {
		off := beU64(k[len(prefix):])
		if off != cover {
			return ErrOffsetGap
		}
		h.Write(v)
		cover += uint64(len(v))
		return nil
	})
	if err != nil {
		return 0, [32]byte{}, err
	}
	var d [32]byte
	copy(d[:], h.Sum(nil))
	return cover, d, nil
}
