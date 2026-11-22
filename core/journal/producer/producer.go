// Package producer is the producer delivery journal of E03-01.producer_store:
// the state machine INTENT -> STAGING -> READY -> SEALED -> ACKED_CLEANUP ->
// release outbox -> RETIRED of DELIVERY_STATE_MACHINE, on the shared storage
// seam.
//
// Rules this store holds (references are DELIVERY_STATE_MACHINE unless noted):
//
//   - batch_id is (producer_id, run_id, batch_seq). batch_seq is allocated
//     atomically with the durable delivery intent; without an intent there is
//     no allocated batch, and a replayed native decision marker finds its
//     original batch instead of allocating a second one ("Identity, режимы и
//     atomicity", "Таблица переходов").
//   - A STAGING retry keeps the batch_id and semantic identity, increments
//     attempt_generation and reserves a fresh event_sequence range; the
//     previous attempt's recovery inputs survive until the new attempt's
//     closing commit replaces them.
//   - Every recoverability transition is one atomic storage batch (a closing
//     commit): READY binds manifest digest + checkpoint + state, SEALED binds
//     plan digests + state, ACKED_CLEANUP binds the full IngestAck +
//     release_outbox + state, RETIRED binds the retired_prefix advance +
//     outbox deletion. A crash between transitions reopens at the last
//     committed state; an unclosed marker never permits publication.
//   - After READY the event identity and manifest digest are immutable; a
//     different digest under the same batch is ErrImmutableConflict, never a
//     silent rewrite. After SEALED the plan can only be replayed, not
//     rebuilt; a new unpublished plan generation is allowed only after a
//     durable AbandonPlan while still READY.
//   - ACKED_CLEANUP keeps the full IngestAck and release_outbox until a
//     verified ReleaseAck; payload cleanup never deletes them
//     ("Release и потеря Gateway store").
//   - Release retires strictly in batch_seq order: a late acknowledgement of
//     an earlier unfinished batch is rejected, an already-retired prefix is
//     answered idempotently.
//   - One serialized writer: every mutating call runs under the store mutex.
//   - Only LIVE admits new capture; DRAIN completes saved obligations,
//     DRAINED survives restart without auto-returning to LIVE, and a return
//     to LIVE requires ResumeToLive, which allocates the next monotonic
//     admission generation.
//
// The store performs no network I/O and imports no database driver; it is
// deterministic over its keys and safe to test with the memory storage
// adapter.
package producer

import (
	"crypto/rand"
	"sync"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// Mode is the durable producer mode.
type Mode uint8

const (
	// ModeLIVE admits new capture after the native barrier.
	ModeLIVE Mode = iota + 1
	// ModeDRAIN completes only already saved obligations.
	ModeDRAIN
	// ModeDRAINED is the durable end of a drain; a plain restart does not
	// return it to LIVE.
	ModeDRAINED
	// ModeBLOCKED stops unsafe work and keeps the reason out of band.
	ModeBLOCKED
)

func (m Mode) String() string {
	switch m {
	case ModeLIVE:
		return "LIVE"
	case ModeDRAIN:
		return "DRAIN"
	case ModeDRAINED:
		return "DRAINED"
	case ModeBLOCKED:
		return "BLOCKED"
	default:
		return "UNKNOWN"
	}
}

// State is a batch's durable state.
type State uint8

const (
	// StateIntent: batch_seq allocated with a durable delivery intent.
	StateIntent State = iota + 1
	// StateStaging: an attempt is assembling; no Publish is allowed.
	StateStaging
	// StateReady: manifest + checkpoint committed by one closing commit.
	StateReady
	// StateSealed: transport plan closed; Publish is allowed.
	StateSealed
	// StateAckedCleanup: verified IngestAck and outbox saved; payload
	// cleanup may run.
	StateAckedCleanup
	// StateRetired: outbox deleted and retired_prefix advanced.
	StateRetired
)

func (s State) String() string {
	switch s {
	case StateIntent:
		return "INTENT"
	case StateStaging:
		return "STAGING"
	case StateReady:
		return "READY"
	case StateSealed:
		return "SEALED"
	case StateAckedCleanup:
		return "ACKED_CLEANUP"
	case StateRetired:
		return "RETIRED"
	default:
		return "UNKNOWN"
	}
}

// Store is one producer journal over a storage seam. All mutating calls are
// serialized; reads take the same mutex so a read never sees a half-applied
// transition.
type Store struct {
	mu  sync.Mutex
	st  storage.Storage
	id  Identity
	md  ModeState
	ct  Counters
	bnd [32]byte
	ret uint64
}

// Open loads or initializes the journal. On an empty storage it creates the
// producer/run identity from cryptographic randomness, starts counters at 1
// and mode LIVE with admission generation 1 (CONTROL_WIRE_CONTRACT: new
// independent identities are generated with crypto RNG and saved before use).
func Open(st storage.Storage) (*Store, error) {
	s := &Store{st: st}
	if raw, err := st.Get([]byte(keyIdentity)); err == nil {
		if s.id, err = decodeIdentity(raw); err != nil {
			return nil, err
		}
	} else if err == storage.ErrNotFound {
		var id Identity
		if _, err := rand.Read(id.ProducerID[:]); err != nil {
			return nil, err
		}
		if _, err := rand.Read(id.RunID[:]); err != nil {
			return nil, err
		}
		if err := st.Put([]byte(keyIdentity), encodeIdentity(id)); err != nil {
			return nil, err
		}
		s.id = id
	} else {
		return nil, err
	}

	if raw, err := st.Get([]byte(keyMode)); err == nil {
		if s.md, err = decodeMode(raw); err != nil {
			return nil, err
		}
	} else if err == storage.ErrNotFound {
		s.md = ModeState{Mode: ModeLIVE, AdmissionGeneration: 1}
		if err := st.Put([]byte(keyMode), encodeMode(s.md)); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}

	if raw, err := st.Get([]byte(keyCounters)); err == nil {
		if s.ct, err = decodeCounters(raw); err != nil {
			return nil, err
		}
	} else if err == storage.ErrNotFound {
		s.ct = Counters{BatchSeqNext: 1, EventSequenceNext: 1}
		if err := st.Put([]byte(keyCounters), encodeCounters(s.ct)); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}

	if raw, err := st.Get([]byte(keyBinding)); err == nil {
		copy(s.bnd[:], raw)
	} else if err != storage.ErrNotFound {
		return nil, err
	}
	if raw, err := st.Get([]byte(keyRetired)); err == nil {
		s.ret = beU64(raw)
	} else if err != storage.ErrNotFound {
		return nil, err
	}
	return s, nil
}

// Identity returns the persistent producer/run identity.
func (s *Store) Identity() Identity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

// ModeInfo returns the durable mode and admission generation.
func (s *Store) ModeInfo() ModeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.md
}

// RetiredPrefix returns the compact retired frontier. The producer keeps
// only this prefix forever, never a payload or digest archive.
func (s *Store) RetiredPrefix() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ret
}

// BindingDigest returns the saved destination binding digest; it is the zero
// value until SetDestinationBinding succeeds.
func (s *Store) BindingDigest() [32]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bnd
}

// SetDestinationBinding saves the immutable destination binding digest. It
// must happen before the first capture commitment; a second call with the
// same digest is idempotent, a different digest is a conflict.
func (s *Store) SetDestinationBinding(digest [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bnd == digest {
		return nil
	}
	if s.bnd != ([32]byte{}) {
		return ErrImmutableConflict
	}
	if err := s.st.Put([]byte(keyBinding), digest[:]); err != nil {
		return err
	}
	s.bnd = digest
	return nil
}

// SetDrain durably stops new capture (LIVE -> DRAIN). Saved obligations keep
// flowing; identity, counters and lifecycle references survive.
func (s *Store) SetDrain() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.md.Mode != ModeLIVE {
		return ErrStateConflict
	}
	s.md.Mode = ModeDRAIN
	return s.st.Put([]byte(keyMode), encodeMode(s.md))
}

// SetDrained records the durable end of a drain (DRAIN -> DRAINED). Only
// ResumeToLive leaves DRAINED.
func (s *Store) SetDrained() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.md.Mode != ModeDRAIN {
		return ErrStateConflict
	}
	s.md.Mode = ModeDRAINED
	return s.st.Put([]byte(keyMode), encodeMode(s.md))
}

// SetBlocked stops unsafe work from any mode and keeps the reason out of
// band; the lack of space must not prevent stopping.
func (s *Store) SetBlocked() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.md.Mode == ModeBLOCKED {
		return nil
	}
	s.md.Mode = ModeBLOCKED
	return s.st.Put([]byte(keyMode), encodeMode(s.md))
}

// ResumeToLive returns a DRAINED store to LIVE with the next monotonic
// admission generation (Resume, D54/D57/D59). The generation bump is one
// durable commit with the mode switch, so a crash either leaves DRAINED with
// the old generation or LIVE with the new one.
func (s *Store) ResumeToLive() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.md.Mode != ModeDRAINED {
		return 0, ErrStateConflict
	}
	if s.md.AdmissionGeneration == ^uint64(0) {
		return 0, ErrCounterOverflow
	}
	s.md.AdmissionGeneration++
	s.md.Mode = ModeLIVE
	if err := s.st.Put([]byte(keyMode), encodeMode(s.md)); err != nil {
		return 0, err
	}
	return s.md.AdmissionGeneration, nil
}

// EventID returns the event_id bytes for one event of an attempt:
// producer_id16 || run_id16 || event_sequence_u64be (CONTROL_WIRE_CONTRACT
// "Базовые кодирования и идентичности").
func (s *Store) EventID(eventSequence uint64) [40]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id [40]byte
	copy(id[:16], s.id.ProducerID[:])
	copy(id[16:32], s.id.RunID[:])
	copy(id[32:], u64be(eventSequence))
	return id
}

func (s *Store) putState(b *storage.Batch, seq uint64, state State) {
	b.Put(batchKey(seq, "/state"), []byte{uint8(state)})
}

func (s *Store) readState(seq uint64) (State, error) {
	raw, err := s.st.Get(batchKey(seq, "/state"))
	if err == storage.ErrNotFound {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if len(raw) != 1 || raw[0] < uint8(StateIntent) || raw[0] > uint8(StateRetired) {
		return 0, ErrCorrupt
	}
	return State(raw[0]), nil
}
