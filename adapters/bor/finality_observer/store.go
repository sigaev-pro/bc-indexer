package finalityobserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// Store errors.
var (
	// ErrConflict reports the same observation identity with different
	// bytes. The durable stream is append-only; nothing is rewritten.
	ErrConflict = errors.New("finality_observer: durable observation conflict")
	// ErrUnknownObservation reports a lookup of an observation identity
	// that the durable stream does not hold.
	ErrUnknownObservation = errors.New("finality_observer: observation not found")
)

// ObservationBinding is the source provenance frozen into one observation
// when it is made: the D80 sources_binding_digest of the exact pinned file
// bytes, the two supporting deployment IDs and the verified identities. A
// later replacement of the sources_file changes none of it.
type ObservationBinding struct {
	SourcesBinding block.Hash32
	L1NodeID       block.Id16
	HeimdallNodeID block.Id16
	Heimdall       HeimdallIdentity
	L1Network      block.NetworkIdentity
}

// Observation is one durable typed observation of the observer stream: the
// native decision that produced it, the frozen source binding and the full
// Polygon evidence. The stream is append-only: a newer finalized L1 block
// creates a new observation and never rewrites an earlier one.
type Observation struct {
	ID       string
	Decision block.NativeDecisionRef
	Binding  ObservationBinding
	Evidence PolygonEvidence
}

// observationJSON is the canonical encoding shape of an observation. The
// encoding is deterministic (fixed field order, no maps), which is what
// makes the evidence bytes of a replayed observation byte-identical after
// a sources_file replacement.
type observationJSON struct {
	ID       string                  `json:"observation_id"`
	Decision block.NativeDecisionRef `json:"native_decision"`
	Binding  ObservationBinding      `json:"binding"`
	Evidence PolygonEvidence         `json:"evidence"`
}

// MarshalJSON encodes the observation canonically.
func (o Observation) MarshalJSON() ([]byte, error) {
	if err := o.Evidence.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(observationJSON(o))
}

// UnmarshalJSON decodes the observation strictly.
func (o *Observation) UnmarshalJSON(data []byte) error {
	var raw observationJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*o = Observation(raw)
	if o.ID == "" {
		return fmt.Errorf("%w: observation id is empty", ErrInvalidValue)
	}
	if o.Binding.SourcesBinding.IsZero() {
		return fmt.Errorf("%w: observation without the D80 sources digest", ErrInvalidValue)
	}
	return o.Evidence.Validate()
}

// EvidenceBytes returns the canonical evidence encoding of the observation.
// Recovery and late serialization take these bytes from the saved
// observation, never from the current sources_file, so a replay after a
// config replacement reproduces the former bytes byte for byte (D80).
func (o Observation) EvidenceBytes() ([]byte, error) {
	return json.Marshal(o)
}

// Equal reports byte equality of the canonical encodings of two
// observations.
func (o Observation) Equal(other Observation) bool {
	a, err := o.EvidenceBytes()
	if err != nil {
		return false
	}
	b, err := other.EvidenceBytes()
	if err != nil {
		return false
	}
	return string(a) == string(b)
}

// Store is the durable typed observation stream. The observer appends
// observations and the verified L1 evidence frontier; the GC consumer
// reads them. Implementations keep every write durable together with its
// inputs and evidence; the memory store models power loss with its
// unsynced-write discard.
type Store interface {
	// Append adds one observation to the durable stream. Appending the
	// same observation (same ID, same bytes) is idempotent; the same ID
	// with different bytes is ErrConflict.
	Append(obs Observation) error
	// Observations returns the durable observations in stream order.
	Observations() ([]Observation, error)
	// Observation returns one durable observation by its identity.
	Observation(id string) (Observation, error)
	// SaveFrontier records the verified L1 evidence frontier. The frontier
	// only advances: a lower or conflicting candidate is refused by the
	// observer before this is called.
	SaveFrontier(ev L1Checkpoint) error
	// Frontier returns the verified L1 evidence frontier.
	Frontier() (L1Checkpoint, bool, error)
}

// MemoryStore is the in-memory Store with explicit sync points: a write is
// durable only after Sync; Crash discards the unsynced writes, the model
// of power loss before the durable commit. It is safe for concurrent use.
type MemoryStore struct {
	mu                 sync.Mutex
	durable            []Observation
	pending            []Observation
	durableFrontier    L1Checkpoint
	frontierSet        bool
	pendingFrontier    L1Checkpoint
	pendingFrontierSet bool
}

// NewMemoryStore returns an empty memory store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

// Append buffers one observation; it becomes durable at the next Sync.
func (s *MemoryStore) Append(obs Observation) error {
	if _, err := obs.EvidenceBytes(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, have := range append(append([]Observation(nil), s.durable...), s.pending...) {
		if have.ID != obs.ID {
			continue
		}
		if have.Equal(obs) {
			return nil
		}
		return fmt.Errorf("%w: observation %s", ErrConflict, obs.ID)
	}
	s.pending = append(s.pending, obs)
	return nil
}

// Observations returns the durable observations in stream order.
func (s *MemoryStore) Observations() ([]Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Observation, len(s.durable))
	copy(out, s.durable)
	return out, nil
}

// Observation returns one durable observation by its identity.
func (s *MemoryStore) Observation(id string) (Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, obs := range s.durable {
		if obs.ID == id {
			return obs, nil
		}
	}
	return Observation{}, ErrUnknownObservation
}

// SaveFrontier buffers the verified L1 evidence frontier.
func (s *MemoryStore) SaveFrontier(ev L1Checkpoint) error {
	if err := ev.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingFrontier = ev
	s.pendingFrontierSet = true
	return nil
}

// Frontier returns the durable verified L1 evidence frontier.
func (s *MemoryStore) Frontier() (L1Checkpoint, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.durableFrontier, s.frontierSet, nil
}

// Sync makes every buffered write durable in one durable commit.
func (s *MemoryStore) Sync() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.durable = append(s.durable, s.pending...)
	s.pending = nil
	if s.pendingFrontierSet {
		s.durableFrontier = s.pendingFrontier
		s.frontierSet = true
		s.pendingFrontier = L1Checkpoint{}
		s.pendingFrontierSet = false
	}
}

// Crash discards the unsynced writes: power loss before the durable
// commit keeps the last synced state and nothing else.
func (s *MemoryStore) Crash() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = nil
	s.pendingFrontier = L1Checkpoint{}
	s.pendingFrontierSet = false
}
