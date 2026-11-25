package producer

// The read side of the producer journal (E03-01.producer_store, readers):
// the list of batches still in flight and the stored chunk streams, for the
// delivery driver that moves a READY batch to RETIRED through the gateway
// RPCs. The readers change nothing and invent nothing: they answer what the
// durable markers say, a hole in a chunk stream is an error, and a chunk is
// handed to the visitor as the store holds it.

import (
	"errors"
	"fmt"
)

// ChunkBytes is the largest chunk the producer writes or reads: one RPC data
// chunk (CONTROL_WIRE_CONTRACT, "Sizes, headers, memory").
const ChunkBytes = 64 << 10

// ChunkVisitor receives one stored chunk. chunk belongs to the store and is
// valid only during the call; a visitor that keeps it must copy it. The
// visitor runs while the store mutex is held, so it must not call back into
// the store. A visitor may stop the stream early with storage.ErrStopIterate.
type ChunkVisitor func(offset uint64, chunk []byte) error

// errNoVisitor rejects a stream read without a visitor.
var errNoVisitor = errors.New("producer journal: nil chunk visitor")

// Pending lists every batch that is not RETIRED, in batch sequence order. The
// list holds the batches of the collector too (INTENT and STAGING); a driver
// picks the states it owns.
func (s *Store) Pending() ([]BatchInfo, error) {
	s.mu.Lock()
	var seqs []uint64
	lo, hi := keyRange([]byte(keyBatchPrefix))
	err := s.st.Iterate(lo, hi, func(k, v []byte) error {
		// Only the state markers are of interest: batch/<seq>/state.
		if len(k) != len(keyBatchPrefix)+8+len(stateSuffix) ||
			string(k[len(k)-len(stateSuffix):]) != stateSuffix {
			return nil
		}
		state, err := stateOf(v)
		if err != nil {
			return err
		}
		if state != StateRetired {
			seqs = append(seqs, beU64(k[len(keyBatchPrefix):len(keyBatchPrefix)+8]))
		}
		return nil
	})
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// Keys carry the sequence in big endian, so the walk is already in
	// batch sequence order. Each batch is then read whole; one that retired
	// in between is left out.
	out := make([]BatchInfo, 0, len(seqs))
	for _, seq := range seqs {
		info, err := s.Batch(seq)
		if err != nil {
			return nil, err
		}
		if info.State != StateRetired {
			out = append(out, info)
		}
	}
	return out, nil
}

// AttemptData streams the RecordFragment bytes of the current attempt, in
// offset order: one call per stored chunk, offsets contiguous from 0.
func (s *Store) AttemptData(seq, generation uint64, visit ChunkVisitor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireAttemptGeneration(seq, generation); err != nil {
		return err
	}
	return s.streamChunks(attemptKey(seq, generation, "data"), visit)
}

// AttemptManifest streams the manifest bytes of the current attempt, in
// offset order: one call per stored chunk, offsets contiguous from 0.
func (s *Store) AttemptManifest(seq, generation uint64, visit ChunkVisitor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireAttemptGeneration(seq, generation); err != nil {
		return err
	}
	return s.streamChunks(attemptKey(seq, generation, "manifest"), visit)
}

// Plan returns the current plan generation of a READY or later batch.
// header is the opaque header bytes BeginPlan stored. A batch that holds no
// plan answers ErrNotFound; a batch before READY answers ErrStateConflict.
func (s *Store) Plan(seq uint64) (generation uint64, seed []byte, layoutDigest [32]byte, header []byte, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requirePlanState(seq); err != nil {
		return 0, nil, [32]byte{}, nil, err
	}
	cur, err := s.readPlanCurrent(seq)
	if err != nil {
		return 0, nil, [32]byte{}, nil, err
	}
	hdr, err := s.readPlanHeader(seq, cur.Generation)
	if err != nil {
		return 0, nil, [32]byte{}, nil, err
	}
	return cur.Generation, append([]byte(nil), hdr.Seed...), hdr.LayoutDigest,
		append([]byte(nil), hdr.Header...), nil
}

// PlanChunks streams the plan payload of one plan generation, in offset
// order: one call per stored chunk, offsets contiguous from 0. The
// generation must have been opened by BeginPlan; its chunks may be absent
// after the payload cleanup, which then streams nothing.
func (s *Store) PlanChunks(seq, generation uint64, visit ChunkVisitor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requirePlanState(seq); err != nil {
		return err
	}
	if _, err := s.readPlanHeader(seq, generation); err != nil {
		return err
	}
	return s.streamChunks(planGenKey(seq, generation, "chunks/"), visit)
}

// stateSuffix ends every state marker key.
const stateSuffix = "/state"

// stateOf decodes one state marker value with the rule of readState: an
// unreadable marker is corruption, never a default.
func stateOf(raw []byte) (State, error) {
	if len(raw) != 1 || raw[0] < uint8(StateIntent) || raw[0] > uint8(StateRetired) {
		return 0, ErrCorrupt
	}
	return State(raw[0]), nil
}

// requireAttemptGeneration checks that generation is the attempt the batch
// currently holds: the staged attempt while STAGING, the READY attempt from
// READY on. An INTENT batch holds no attempt. The chunks of a replaced
// attempt survive until the cleanup and must not be read as the current one.
func (s *Store) requireAttemptGeneration(seq, generation uint64) error {
	state, err := s.readState(seq)
	if err != nil {
		return err
	}
	if state == StateIntent {
		return ErrStateConflict
	}
	intent, err := s.readIntent(seq)
	if err != nil {
		return err
	}
	want := intent.AttemptGeneration
	if state != StateStaging {
		ready, err := s.readReady(seq)
		if err != nil {
			return err
		}
		want = ready.Attempt
	}
	if want != generation {
		return ErrStateConflict
	}
	return nil
}

// requirePlanState admits the batch states that may hold a plan.
func (s *Store) requirePlanState(seq uint64) error {
	state, err := s.readState(seq)
	if err != nil {
		return err
	}
	if state == StateIntent || state == StateStaging {
		return ErrStateConflict
	}
	return nil
}

// streamChunks walks one offset-addressed chunk stream in key order, which
// is offset order, and hands every chunk to the visitor. The stream must be
// contiguous from offset 0: a hole or a misplaced chunk is ErrOffsetGap, a
// key of the wrong shape is corruption. The visitor's error ends the walk
// and is returned as is; storage.ErrStopIterate ends it without an error.
func (s *Store) streamChunks(prefix []byte, visit ChunkVisitor) error {
	if visit == nil {
		return errNoVisitor
	}
	lo, hi := keyRange(prefix)
	var cover uint64
	return s.st.Iterate(lo, hi, func(k, v []byte) error {
		if len(k) != len(prefix)+8 {
			return fmt.Errorf("%w: chunk key %x under %q", errCorrupt, k, prefix)
		}
		offset := beU64(k[len(prefix):])
		if offset != cover {
			return ErrOffsetGap
		}
		if err := visit(offset, v); err != nil {
			return err
		}
		cover += uint64(len(v))
		return nil
	})
}
