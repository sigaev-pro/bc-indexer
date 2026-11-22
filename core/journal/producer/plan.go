package producer

import (
	"crypto/sha256"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// BeginPlan records the transport plan handoff for a READY batch after the
// gateway returned its reservation: the exact seed bytes (whose SHA-256 is
// the plan digest), the layout manifest digest and the producer header
// bytes. Each call opens a new plan generation; an active unpublished
// generation must be abandoned first, which is the only pre-SEALED recovery
// allowed after gateway loss. Retrying the handoff with identical bytes
// returns the same generation.
func (s *Store) BeginPlan(seq uint64, seed []byte, layoutDigest [32]byte, header []byte) (uint64, [32]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.readState(seq)
	if err != nil {
		return 0, [32]byte{}, err
	}
	planDigest := sha256.Sum256(seed)

	if state == StateSealed || state == StateAckedCleanup || state == StateRetired {
		sealed, err := s.readSealed(seq)
		if err != nil {
			return 0, [32]byte{}, err
		}
		if sealed.PlanDigest != planDigest || sealed.LayoutDigest != layoutDigest {
			return 0, [32]byte{}, ErrImmutableConflict
		}
		return sealed.Generation, planDigest, nil
	}
	if state != StateReady {
		return 0, [32]byte{}, ErrStateConflict
	}

	if raw, err := s.st.Get(planGenCurrentKey(seq)); err == nil {
		cur, err := decodePlanCurrent(raw)
		if err != nil {
			return 0, [32]byte{}, err
		}
		hdr, err := s.readPlanHeader(seq, cur.Generation)
		if err == nil && !cur.Abandoned && hdr.Seed != nil && string(hdr.Seed) == string(seed) && hdr.LayoutDigest == layoutDigest {
			return cur.Generation, planDigest, nil
		}
		if !cur.Abandoned {
			return 0, [32]byte{}, ErrStateConflict
		}
		if cur.Generation == ^uint64(0) {
			return 0, [32]byte{}, ErrCounterOverflow
		}
		return s.openPlan(seq, cur.Generation+1, seed, layoutDigest, header, planDigest)
	} else if err != storage.ErrNotFound {
		return 0, [32]byte{}, err
	}
	return s.openPlan(seq, 1, seed, layoutDigest, header, planDigest)
}

// AbandonPlan durably refuses the current unpublished plan generation. It is
// allowed only while READY (pre-SEALED recovery after gateway loss); the
// READY manifest bytes, event IDs and digests are untouched.
func (s *Store) AbandonPlan(seq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.readState(seq)
	if err != nil {
		return err
	}
	if state != StateReady {
		return ErrStateConflict
	}
	cur, err := s.readPlanCurrent(seq)
	if err != nil {
		return err
	}
	if cur.Abandoned {
		return nil
	}
	cur.Abandoned = true
	return s.st.Put(planGenCurrentKey(seq), encodePlanCurrent(cur))
}

// WritePlanChunk appends one bounded plan payload chunk (frame bytes and
// headers spooled for the publisher) of the given plan generation.
func (s *Store) WritePlanChunk(seq, generation, offset uint64, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readState(seq)
	if err != nil {
		return err
	}
	cur, err := s.readPlanCurrent(seq)
	if err != nil {
		return err
	}
	if state != StateReady || cur.Generation != generation || cur.Abandoned {
		return ErrStateConflict
	}
	return writeChunk(s.st, planGenKey(seq, generation, "chunks/"), offset, data)
}

// Seal is the closing SEALED commit: it verifies the plan chunk stream is
// complete, then atomically binds the plan digest, layout digest, generation
// and total size with the state switch. After SEALED the reservation,
// layout, headers and frame bytes are immutable: resealing with the same
// digests is a no-op, with different digests an immutable conflict, and no
// new plan generation is accepted.
func (s *Store) Seal(seq, generation, totalBytes uint64) (Sealed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.readState(seq)
	if err != nil {
		return Sealed{}, err
	}
	cur, err := s.readPlanCurrent(seq)
	if err != nil {
		return Sealed{}, err
	}
	hdr, err := s.readPlanHeader(seq, generation)
	if err != nil {
		return Sealed{}, err
	}

	switch state {
	case StateSealed, StateAckedCleanup, StateRetired:
		sealed, err := s.readSealed(seq)
		if err != nil {
			return Sealed{}, err
		}
		if sealed.Generation != generation || sealed.TotalBytes != totalBytes {
			return Sealed{}, ErrImmutableConflict
		}
		return sealed, nil
	case StateReady:
		if cur.Generation != generation || cur.Abandoned {
			return Sealed{}, ErrStateConflict
		}
	default:
		return Sealed{}, ErrStateConflict
	}

	cover, _, err := chunkCover(s.st, planGenKey(seq, generation, "chunks/"))
	if err != nil {
		return Sealed{}, err
	}
	if cover != totalBytes {
		return Sealed{}, ErrOffsetGap
	}
	planDigest := sha256.Sum256(hdr.Seed)
	sealed := Sealed{
		PlanDigest:   planDigest,
		LayoutDigest: hdr.LayoutDigest,
		Generation:   generation,
		TotalBytes:   totalBytes,
	}
	b := storage.NewBatch()
	b.Put([]byte(keyPlanSealed(seq)), encodeSealed(sealed))
	s.putState(b, seq, StateSealed)
	if err := s.st.Apply(b); err != nil {
		return Sealed{}, err
	}
	return sealed, nil
}

func (s *Store) openPlan(seq, generation uint64, seed []byte, layoutDigest [32]byte, header []byte, planDigest [32]byte) (uint64, [32]byte, error) {
	b := storage.NewBatch()
	b.Put(planGenCurrentKey(seq), encodePlanCurrent(PlanCurrent{Generation: generation}))
	b.Put(planGenKey(seq, generation, "header"), encodePlanHeader(PlanHeader{Seed: seed, LayoutDigest: layoutDigest, Header: header}))
	if err := s.st.Apply(b); err != nil {
		return 0, [32]byte{}, err
	}
	return generation, planDigest, nil
}

func planGenCurrentKey(seq uint64) []byte {
	k := append([]byte(keyPlanPrefix), u64be(seq)...)
	return append(k, "/current"...)
}

func keyPlanSealed(seq uint64) []byte {
	k := append([]byte(keyPlanPrefix), u64be(seq)...)
	return append(k, "/sealed"...)
}

func (s *Store) readPlanCurrent(seq uint64) (PlanCurrent, error) {
	raw, err := s.st.Get(planGenCurrentKey(seq))
	if err == storage.ErrNotFound {
		return PlanCurrent{}, ErrNotFound
	}
	if err != nil {
		return PlanCurrent{}, err
	}
	return decodePlanCurrent(raw)
}

func (s *Store) readPlanHeader(seq, generation uint64) (PlanHeader, error) {
	raw, err := s.st.Get(planGenKey(seq, generation, "header"))
	if err == storage.ErrNotFound {
		return PlanHeader{}, ErrNotFound
	}
	if err != nil {
		return PlanHeader{}, err
	}
	return decodePlanHeader(raw)
}

func (s *Store) readSealed(seq uint64) (Sealed, error) {
	raw, err := s.st.Get(keyPlanSealed(seq))
	if err == storage.ErrNotFound {
		return Sealed{}, ErrNotFound
	}
	if err != nil {
		return Sealed{}, err
	}
	return decodeSealed(raw)
}
