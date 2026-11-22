package journal

import (
	"crypto/sha256"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// FrameAddr identifies one transport frame: event ordinal, fragment index
// and frame index (0 for COMPLETE, part index for PART).
type FrameAddr struct {
	Ordinal       uint64
	FragmentIndex uint64
	FrameIndex    uint64
}

// FrameMeta is the declaration carried by the first chunk of every frame.
// Form is 0 for COMPLETE and 1 for PART. For COMPLETE, Capacity equals
// FragmentTotal and PartCount is 1; for PART, Capacity is the part data size
// C and PartCount the number of parts (CONTROL_WIRE_CONTRACT "TransportFrame
// layout v2").
type FrameMeta struct {
	Form          byte
	Capacity      uint64
	PartCount     uint64
	FragmentTotal uint64
}

// layoutEntry derives the layout manifest entry of a frame declaration.
func (m FrameMeta) entry(addr FrameAddr) LayoutEntry {
	return LayoutEntry{
		Ordinal:       addr.Ordinal,
		FragmentIndex: addr.FragmentIndex,
		Form:          m.Form,
		Capacity:      m.Capacity,
		PartCount:     m.PartCount,
		FragmentTotal: m.FragmentTotal,
	}
}

// FrameTotal is the declared byte size of one frame.
func (m FrameMeta) frameTotal(frame uint64) uint64 { return m.entry(FrameAddr{}).FrameTotal(frame) }

// PublishResume reports the durable resume cursors after BeginPublish.
type PublishResume struct {
	// Frames lists every declared frame with its next expected offset.
	Frames []FrameCursor
}

// FrameCursor is one frame's resume point.
type FrameCursor struct {
	Addr      FrameAddr
	NextBytes uint64
}

// BeginPublish accepts the transport plan of a PREPARED batch: the exact
// reservation seed (compared byte-for-byte with the stored reservation, and
// its SHA-256 must equal planDigest) plus the layout manifest digest and
// entry count. The call switches the batch to RECEIVING in one atomic
// commit. A retry with the same plan returns the durable resume cursors.
func (j *Journal) BeginPublish(id BatchID, seed []byte, planDigest, layoutDigest [32]byte, entryCount uint64) (PublishResume, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if id.Seq <= j.readRetired(id) {
		return PublishResume{}, ErrAlreadyRetired
	}
	if entryCount == 0 {
		return PublishResume{}, ErrIncomplete
	}
	res, err := j.readReservation(id)
	if err != nil {
		return PublishResume{}, err
	}
	if string(res.Seed) != string(seed) {
		return PublishResume{}, ErrIdentityMismatch
	}
	if PlanDigest(seed) != planDigest {
		return PublishResume{}, ErrIdentityMismatch
	}

	state, err := j.readState(id)
	if err != nil {
		return PublishResume{}, err
	}
	if state == StateReceiving || state == StateClosed || state == StateNatsAcked || state == StateReleasing || state == StateRetired {
		pub, err := j.readPublish(id)
		if err != nil {
			return PublishResume{}, err
		}
		if pub.PlanDigest != planDigest || pub.LayoutDigest != layoutDigest || pub.EntryCount != entryCount {
			return PublishResume{}, ErrImmutableConflict
		}
		return j.frameCursors(id)
	}
	if state != StatePrepared {
		return PublishResume{}, ErrStateConflict
	}

	b := storage.NewBatch()
	b.Put(batchKey(id, "/publish"), encodePublish(Publish{PlanDigest: planDigest, LayoutDigest: layoutDigest, EntryCount: entryCount}))
	j.putState(b, id, StateReceiving)
	if err := j.st.Apply(b); err != nil {
		return PublishResume{}, err
	}
	return PublishResume{}, nil
}

// PublishChunk stores one bounded chunk of one transport frame. The first
// chunk of a frame (offset 0) must carry the frame declaration, which also
// declares the layout entry; later chunks repeat it identically or omit it.
// The same bytes at the same offset are idempotent; a hole returns
// ErrOffsetMismatch with the expected cursor; a conflicting rewrite or a
// changed declaration is an immutable conflict. A frame may not extend
// beyond its declared total, and the stream cannot grow after CLOSED.
func (j *Journal) PublishChunk(id BatchID, planDigest [32]byte, addr FrameAddr, meta *FrameMeta, offset uint64, chunkSHA [32]byte, data []byte) (nextOffset uint64, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if id.Seq <= j.readRetired(id) {
		return 0, ErrAlreadyRetired
	}
	state, err := j.readState(id)
	if err != nil {
		return 0, err
	}
	if state != StateReceiving && state != StateClosed && state != StateNatsAcked && state != StateReleasing {
		return 0, ErrStateConflict
	}
	pub, err := j.readPublish(id)
	if err != nil {
		return 0, err
	}
	if pub.PlanDigest != planDigest {
		return 0, ErrIdentityMismatch
	}
	if len(data) > MaxDataChunkBytes {
		return 0, ErrChunkTooLarge
	}
	if sha256.Sum256(data) != chunkSHA {
		return 0, ErrPayloadDigest
	}

	var total uint64
	if meta != nil {
		if err := j.declareFrame(id, addr, *meta); err != nil {
			return 0, err
		}
		total = meta.frameTotal(addr.FrameIndex)
	}
	head, err := j.frameTotalOf(id, addr)
	if err != nil {
		return 0, err
	}
	if meta != nil && head != total {
		return 0, ErrImmutableConflict
	}
	total = head

	prefix := frameKeyPrefix(id, addr.Ordinal, addr.FragmentIndex, addr.FrameIndex)
	cover, _, err := chunkCover(j.st, prefix)
	if err != nil {
		return 0, err
	}
	key := append(append([]byte(nil), prefix...), u64be(offset)...)
	switch {
	case offset == cover:
		if state != StateReceiving {
			return cover, ErrStateConflict // the frame stream is closed
		}
		if cover+uint64(len(data)) > total {
			return cover, ErrStateConflict // beyond the declared frame size
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

// declareFrame records the frame declaration (layout entry plus per-frame
// total) and rejects any change to an existing declaration.
func (j *Journal) declareFrame(id BatchID, addr FrameAddr, meta FrameMeta) error {
	if meta.Form > 1 {
		return ErrStateConflict
	}
	entry := meta.entry(addr)
	if entry.Form == 0 {
		if entry.PartCount != 1 || entry.Capacity != entry.FragmentTotal {
			return ErrStateConflict
		}
		if addr.FrameIndex != 0 {
			return ErrStateConflict
		}
	} else {
		if entry.PartCount == 0 || entry.Capacity == 0 {
			return ErrStateConflict
		}
		if addr.FrameIndex >= entry.PartCount {
			return ErrStateConflict
		}
		if entry.Capacity*entry.PartCount < entry.FragmentTotal {
			return ErrStateConflict // parts cannot cover the fragment
		}
	}

	lkey := layoutKey(id, addr.Ordinal, addr.FragmentIndex)
	if raw, err := j.st.Get(lkey); err == nil {
		old, derr := decodeLayoutEntry(raw)
		if derr != nil {
			return derr
		}
		if old != entry {
			return ErrImmutableConflict
		}
	} else if err != storage.ErrNotFound {
		return err
	} else if err := j.st.Put(lkey, encodeLayoutEntry(entry)); err != nil {
		return err
	}

	hkey := frameHeadKey(id, addr.Ordinal, addr.FragmentIndex, addr.FrameIndex)
	total := entry.FrameTotal(addr.FrameIndex)
	if raw, err := j.st.Get(hkey); err == nil {
		if beU64(raw) != total {
			return ErrImmutableConflict
		}
		return nil
	} else if err != storage.ErrNotFound {
		return err
	}
	return j.st.Put(hkey, u64be(total))
}

func (j *Journal) frameTotalOf(id BatchID, addr FrameAddr) (uint64, error) {
	raw, err := j.st.Get(frameHeadKey(id, addr.Ordinal, addr.FragmentIndex, addr.FrameIndex))
	if err == storage.ErrNotFound {
		return 0, ErrBatchNotFound // undeclared frame
	}
	if err != nil {
		return 0, err
	}
	if len(raw) != 8 {
		return 0, errCorrupt
	}
	return beU64(raw), nil
}

// layoutDigest recomputes the layout manifest digest over the stored
// entries in (ordinal, fragment index) order:
// SHA256("extractor.layout.v2" || 0x00 || plan_digest || entry_count || entries).
func (j *Journal) layoutDigest(id BatchID, planDigest [32]byte, entryCount uint64) ([32]byte, []LayoutEntry, error) {
	lo, hi := keyRange(batchKey(id, "layout/"))
	var entries []LayoutEntry
	err := j.st.Iterate(lo, hi, func(k, v []byte) error {
		e, derr := decodeLayoutEntry(v)
		if derr != nil {
			return derr
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return [32]byte{}, nil, err
	}
	if uint64(len(entries)) != entryCount {
		return [32]byte{}, nil, ErrIncomplete
	}
	h := sha256.New()
	h.Write([]byte("extractor.layout.v2"))
	h.Write([]byte{0})
	h.Write(planDigest[:])
	h.Write(u64be(entryCount))
	for _, e := range entries {
		h.Write(u64be(e.Ordinal))
		h.Write(u64be(e.FragmentIndex))
		h.Write([]byte{e.Form})
		h.Write(u64be(e.Capacity))
		h.Write(u64be(e.PartCount))
		h.Write(u64be(e.FragmentTotal))
	}
	var d [32]byte
	copy(d[:], h.Sum(nil))
	return d, entries, nil
}

// completeFrames verifies every declared frame received its full bytes.
func (j *Journal) completeFrames(id BatchID, entries []LayoutEntry) error {
	for _, e := range entries {
		for f := uint64(0); f < e.FrameCount(); f++ {
			prefix := frameKeyPrefix(id, e.Ordinal, e.FragmentIndex, f)
			cover, _, err := chunkCover(j.st, prefix)
			if err != nil {
				return err
			}
			if cover != e.FrameTotal(f) {
				return ErrIncomplete
			}
		}
	}
	return nil
}

// FinalizePublish closes a normal batch: after verifying the plan identity,
// the recomputed layout manifest digest and the completeness of every frame,
// one atomic commit switches the batch to CLOSED, the only publishable
// state. Retrying with the same digests is idempotent; different digests are
// an immutable conflict.
func (j *Journal) FinalizePublish(id BatchID, planDigest, manifestDigest, layoutDigest [32]byte) error {
	return j.finalize(id, planDigest, manifestDigest, layoutDigest, false)
}

// SealImport closes an IMPORT_SEALED batch after its recovered manifest and
// original frames validated by the same completeness rules.
func (j *Journal) SealImport(id BatchID, planDigest, manifestDigest, layoutDigest [32]byte) error {
	return j.finalize(id, planDigest, manifestDigest, layoutDigest, true)
}

func (j *Journal) finalize(id BatchID, planDigest, manifestDigest, layoutDigest [32]byte, importMode bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if id.Seq <= j.readRetired(id) {
		return ErrAlreadyRetired
	}
	state, err := j.readState(id)
	if err != nil {
		return err
	}
	if state == StateClosed || state == StateNatsAcked || state == StateReleasing || state == StateRetired {
		if state == StateRetired {
			return nil // evidence deleted with the retire commit
		}
		res, err := j.readReservation(id)
		if err != nil {
			return err
		}
		// A replay of the wrong closing mode is a mode conflict even on an
		// already closed batch.
		if res.Import != importMode {
			return ErrModeConflict
		}
		closed, err := j.readClosed(id)
		if err != nil {
			return err
		}
		if closed.PlanDigest != planDigest || closed.ManifestDigest != manifestDigest || closed.LayoutDigest != layoutDigest {
			return ErrImmutableConflict
		}
		return nil
	}
	if state != StateReceiving {
		return ErrStateConflict
	}
	res, err := j.readReservation(id)
	if err != nil {
		return err
	}
	if res.Import != importMode {
		return ErrModeConflict
	}
	pub, err := j.readPublish(id)
	if err != nil {
		return err
	}
	if pub.PlanDigest != planDigest || pub.LayoutDigest != layoutDigest {
		return ErrIdentityMismatch
	}
	if res.ManifestDigest != manifestDigest {
		return ErrIdentityMismatch
	}
	digest, entries, err := j.layoutDigest(id, planDigest, pub.EntryCount)
	if err != nil {
		return err
	}
	if digest != layoutDigest {
		return ErrLayoutMismatch
	}
	if err := j.completeFrames(id, entries); err != nil {
		return err
	}

	b := storage.NewBatch()
	b.Put(batchKey(id, "/closed"), encodeClosed(Closed{
		PlanDigest:     planDigest,
		ManifestDigest: manifestDigest,
		LayoutDigest:   layoutDigest,
		BindingDigest:  j.bnd,
	}))
	j.putState(b, id, StateClosed)
	return j.st.Apply(b)
}

// frameCursors lists every declared frame with its durable next offset, for
// resuming an interrupted publish stream.
func (j *Journal) frameCursors(id BatchID) (PublishResume, error) {
	lo, hi := keyRange(batchKey(id, "layout/"))
	var entries []LayoutEntry
	err := j.st.Iterate(lo, hi, func(k, v []byte) error {
		e, derr := decodeLayoutEntry(v)
		if derr != nil {
			return derr
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return PublishResume{}, err
	}
	res := PublishResume{}
	for _, e := range entries {
		for f := uint64(0); f < e.FrameCount(); f++ {
			addr := FrameAddr{Ordinal: e.Ordinal, FragmentIndex: e.FragmentIndex, FrameIndex: f}
			prefix := frameKeyPrefix(id, addr.Ordinal, addr.FragmentIndex, addr.FrameIndex)
			cover, _, err := chunkCover(j.st, prefix)
			if err != nil {
				return PublishResume{}, err
			}
			res.Frames = append(res.Frames, FrameCursor{Addr: addr, NextBytes: cover})
		}
	}
	return res, nil
}

func (j *Journal) readReservation(id BatchID) (Reservation, error) {
	raw, err := j.st.Get(batchKey(id, "/reservation"))
	if err == storage.ErrNotFound {
		return Reservation{}, ErrBatchNotFound
	}
	if err != nil {
		return Reservation{}, err
	}
	return decodeReservation(raw)
}

func (j *Journal) readPublish(id BatchID) (Publish, error) {
	raw, err := j.st.Get(batchKey(id, "/publish"))
	if err == storage.ErrNotFound {
		return Publish{}, ErrBatchNotFound
	}
	if err != nil {
		return Publish{}, err
	}
	return decodePublish(raw)
}

func (j *Journal) readClosed(id BatchID) (Closed, error) {
	raw, err := j.st.Get(batchKey(id, "/closed"))
	if err == storage.ErrNotFound {
		return Closed{}, ErrBatchNotFound
	}
	if err != nil {
		return Closed{}, err
	}
	return decodeClosed(raw)
}
