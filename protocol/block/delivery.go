package block

import (
	"errors"
	"fmt"
)

// ErrBlockCompleteForbidden reports an attempt to use the excluded
// BlockComplete or CoverageUpdated marker. D25 removed collector-side
// completeness proof: a delivery batch tracks its own transmission only and
// never states that a block's data is complete.
var ErrBlockCompleteForbidden = errors.New("block: BlockComplete and CoverageUpdated are excluded by D25")

// BatchID identifies one delivery batch. The durable binary form is
// producer_id16 || run_id16 || batch_sequence_u64be (40 bytes); the batch
// counter starts at 1.
type BatchID struct {
	ProducerID Id16   `json:"producer_id"`
	RunID      Id16   `json:"run_id"`
	Sequence   uint64 `json:"sequence"`
}

// Validate requires assigned identities and a first-or-later batch counter.
func (b BatchID) Validate() error {
	if b.ProducerID.IsZero() {
		return fmt.Errorf("%w: batch producer id is zero", ErrInvalidValue)
	}
	if b.RunID.IsZero() {
		return fmt.Errorf("%w: batch run id is zero", ErrInvalidValue)
	}
	if b.Sequence == 0 {
		return fmt.Errorf("%w: batch sequence is zero", ErrInvalidValue)
	}
	return nil
}

// DeliveryFrontier is the local delivery progress of one batch (D79/F02).
// WAL_SAVED is reached after the complete batch with all its messages is
// durably saved; NATS_ACKED after every transport frame of every event of
// the batch received its PubAck. A partial event or block ACK never
// completes the batch.
//
// This is a local accounting of the transmission of one decision's public
// events. It is never a statement that the source emitted all blockchain
// data, and it never carries a block completeness marker.
type DeliveryFrontier uint8

// Delivery frontiers. Zero is not a valid frontier.
const (
	FrontierWalSaved DeliveryFrontier = iota + 1
	FrontierNatsAcked
)

var deliveryFrontierNames = []string{"WAL_SAVED", "NATS_ACKED"}

// String returns the contract name of f.
func (f DeliveryFrontier) String() string {
	if f == 0 || int(f) > len(deliveryFrontierNames) {
		return "INVALID"
	}
	return deliveryFrontierNames[f-1]
}

// Validate reports whether f is a known frontier.
func (f DeliveryFrontier) Validate() error {
	if f == 0 || int(f) > len(deliveryFrontierNames) {
		return fmt.Errorf("%w: delivery frontier %d", ErrInvalidValue, uint8(f))
	}
	return nil
}

// MarshalJSON encodes the frontier as its contract name.
func (f DeliveryFrontier) MarshalJSON() ([]byte, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return jsonMarshal(f.String())
}

// UnmarshalJSON decodes the frontier name strictly.
func (f *DeliveryFrontier) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	for i, n := range deliveryFrontierNames {
		if n == name {
			*f = DeliveryFrontier(i + 1)
			return nil
		}
	}
	return fmt.Errorf("%w: unknown delivery frontier %q", ErrInvalidValue, name)
}

// AdvanceFrontier moves the delivery frontier forward. The only legal
// transition is WAL_SAVED to NATS_ACKED: the frontier never moves back and
// never skips durable storage.
func AdvanceFrontier(current, next DeliveryFrontier) (DeliveryFrontier, error) {
	if err := current.Validate(); err != nil {
		return 0, err
	}
	if err := next.Validate(); err != nil {
		return 0, err
	}
	if current == FrontierWalSaved && next == FrontierNatsAcked {
		return FrontierNatsAcked, nil
	}
	if current == next {
		return current, nil
	}
	return 0, fmt.Errorf("%w: delivery frontier %s cannot advance to %s", ErrInvalidValue, current, next)
}

// BlockEventKind is the closed set of public block-family event kinds
// (MESSAGE_CONTRACT section 7: family=block, kind=applied|removed|status).
// BlockComplete and CoverageUpdated are excluded by D25: the schema has no
// such kind, and ParseBlockEventKind rejects the names explicitly.
type BlockEventKind uint8

// Block event kinds. Zero is not a valid kind.
const (
	BlockEventApplied BlockEventKind = iota + 1
	BlockEventRemoved
	BlockEventStatusChanged
)

var blockEventKindNames = map[string]BlockEventKind{
	"applied": BlockEventApplied,
	"removed": BlockEventRemoved,
	"status":  BlockEventStatusChanged,
}

// String returns the wire kind name of k.
func (k BlockEventKind) String() string {
	switch k {
	case BlockEventApplied:
		return "applied"
	case BlockEventRemoved:
		return "removed"
	case BlockEventStatusChanged:
		return "status"
	default:
		return "INVALID"
	}
}

// Validate reports whether k is a known kind.
func (k BlockEventKind) Validate() error {
	if k == 0 || k > BlockEventStatusChanged {
		return fmt.Errorf("%w: block event kind %d", ErrInvalidValue, uint8(k))
	}
	return nil
}

// forbiddenKindNames are the excluded completeness markers. Any use of them
// in the block family is an explicit error, not an unknown value.
var forbiddenKindNames = map[string]error{
	"BLOCK_COMPLETE":   ErrBlockCompleteForbidden,
	"block_complete":   ErrBlockCompleteForbidden,
	"COVERAGE_UPDATED": ErrBlockCompleteForbidden,
	"coverage_updated": ErrBlockCompleteForbidden,
}

// ParseBlockEventKind parses the closed wire kind names of the block event
// family. The D25-excluded completeness markers are rejected with
// ErrBlockCompleteForbidden; any other unknown name with ErrInvalidValue.
func ParseBlockEventKind(name string) (BlockEventKind, error) {
	if err, forbidden := forbiddenKindNames[name]; forbidden {
		return 0, fmt.Errorf("%w: kind %q", err, name)
	}
	if k, ok := blockEventKindNames[name]; ok {
		return k, nil
	}
	return 0, fmt.Errorf("%w: unknown block event kind %q", ErrInvalidValue, name)
}

// MarshalJSON encodes the kind as its wire name.
func (k BlockEventKind) MarshalJSON() ([]byte, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	return jsonMarshal(k.String())
}

// UnmarshalJSON decodes the wire kind name strictly and forbids the D25
// markers by name.
func (k *BlockEventKind) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	parsed, err := ParseBlockEventKind(name)
	if err != nil {
		return err
	}
	*k = parsed
	return nil
}

// DeliveryBatchRef is the local link from the prepared messages of one
// native decision to their delivery progress (D79/F02): one native decision
// with public events creates exactly one delivery batch carrying the full
// ordered set of that decision's events.
//
// The reference carries the block record activations whose events the batch
// transports and the batch's own delivery frontier. It carries nothing
// else: no BlockComplete or CoverageUpdated marker (D25), no coverage
// claim, no source inventory statement, and no promise that any particular
// block is complete. A reorg decision can reference several activations; a
// finality-only decision references the activation whose status it updates.
type DeliveryBatchRef struct {
	Batch BatchID `json:"batch"`
	// Activations are the block activations this batch carries events
	// for. Every batch has at least one public event; a decision without
	// public events records a checkpoint without a batch.
	Activations []BlockActivationRef `json:"activations"`
	Frontier    DeliveryFrontier     `json:"frontier"`
}

// Validate checks the batch identity, the non-empty, duplicate-free
// activation references and the frontier. It deliberately inspects nothing
// beyond delivery identity: block metadata, counts and lifecycle evidence
// are out of scope for the delivery link and can never influence it.
func (r *DeliveryBatchRef) Validate() error {
	if err := r.Batch.Validate(); err != nil {
		return err
	}
	if err := r.Frontier.Validate(); err != nil {
		return err
	}
	if len(r.Activations) == 0 {
		return fmt.Errorf("%w: delivery batch references no activation", ErrInvalidValue)
	}
	seen := make(map[BlockActivationRef]struct{}, len(r.Activations))
	for i, ref := range r.Activations {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: batch activation %d: %v", ErrInvalidValue, i, err)
		}
		if _, dup := seen[ref]; dup {
			return fmt.Errorf("%w: batch activation %d is a duplicate", ErrInvalidValue, i)
		}
		seen[ref] = struct{}{}
	}
	return nil
}

// Ack advances the batch frontier to NATS_ACKED when the complete batch was
// durably saved. Partial ACKs never reach this method: the caller advances
// the frontier only after every frame of every event of the batch.
func (r *DeliveryBatchRef) Ack() error {
	next, err := AdvanceFrontier(r.Frontier, FrontierNatsAcked)
	if err != nil {
		return err
	}
	r.Frontier = next
	return nil
}
