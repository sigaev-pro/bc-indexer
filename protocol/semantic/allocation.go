// Decision-to-batch allocation of D79: one native decision with public
// events creates exactly one delivery batch with the full ordered set of
// semantic descriptors of that decision. The decision-to-batch ID binding
// and the batch counter advance in one commit; a repeated decision marker
// restores the previous binding, and a decision without public events
// allocates no batch at all.
package semantic

import (
	"encoding/binary"
	"fmt"
)

// BatchID is the 40-byte batch identity: producer_id16 || run_id16 ||
// batch_sequence_u64be. The batch sequence is a separate durable counter
// that starts at 1.
type BatchID [40]byte

// NewBatchID returns the batch identity of one producer run with the given
// batch sequence. The sequence starts at 1; zero is only the empty
// frontier.
func NewBatchID(producer, run ID16, sequence uint64) (BatchID, error) {
	if producer.IsZero() || run.IsZero() {
		return BatchID{}, fmt.Errorf("%w: unset producer or run of a batch", ErrInvalid)
	}
	if sequence == 0 {
		return BatchID{}, fmt.Errorf("%w: batch sequence 0", ErrInvalid)
	}
	var out BatchID
	copy(out[:16], producer[:])
	copy(out[16:32], run[:])
	copy(out[32:], u64(sequence))
	return out, nil
}

// Decode returns the producer, run and batch sequence of the identity.
func (b BatchID) Decode() (producer, run ID16, sequence uint64) {
	copy(producer[:], b[:16])
	copy(run[:], b[16:32])
	sequence = binaryBe64(b[32:])
	return producer, run, sequence
}

// String returns the 80 lowercase hex characters of the identity.
func (b BatchID) String() string { return hexString(b[:]) }

func binaryBe64(b []byte) uint64 { return binary.BigEndian.Uint64(b) }

// DecisionID identifies one native decision: the producer run that made it
// and the native decision sequence, which starts at 0.
type DecisionID struct {
	ProducerID ID16
	RunID      ID16
	Sequence   uint64
}

// BatchAllocation is the decision-to-batch binding: one decision, the batch
// that carries its public events, allocated exactly once.
type BatchAllocation struct {
	Decision DecisionID
	Batch    BatchID
}

// Validate checks the allocation: the batch must belong to the producer run
// of the decision, and the batch sequence must start at 1.
func (a BatchAllocation) Validate() error {
	if a.Decision.ProducerID.IsZero() || a.Decision.RunID.IsZero() {
		return fmt.Errorf("%w: unset producer or run of a decision", ErrInvalid)
	}
	producer, run, sequence := a.Batch.Decode()
	if producer != a.Decision.ProducerID || run != a.Decision.RunID {
		return fmt.Errorf("%w: batch of %s.%s on decision of %s.%s", ErrIdentity, producer, run,
			a.Decision.ProducerID, a.Decision.RunID)
	}
	if sequence == 0 {
		return fmt.Errorf("%w: batch sequence 0", ErrInvalid)
	}
	return nil
}

// Digest returns the deterministic digest of the allocation: SHA-256 over
//
//	UTF8("extractor.allocation.v1") || 0x00 ||
//	producer16 || run16 || u64be(decision_sequence) || batch40
func (a BatchAllocation) Digest() (Hash32, error) {
	if err := a.Validate(); err != nil {
		return Hash32{}, err
	}
	return digestOf([]byte(tagAllocation), []byte{0}, a.Decision.ProducerID[:], a.Decision.RunID[:],
		u64(a.Decision.Sequence), a.Batch[:]), nil
}

// Allocator hands each native decision of one producer run exactly one
// delivery batch. It is the in-memory model of the durable binding: the
// producer store (E03-01) commits the binding and the batch counter in one
// atomic step and rebuilds this state at startup. It is not safe for
// concurrent use.
type Allocator struct {
	producer ID16
	run      ID16
	// next is the next unused batch sequence; it starts at 1 and only
	// moves forward, so no batch sequence is reused.
	next uint64
	// batches holds the one allocation of each decision that has one,
	// keyed by the native decision sequence.
	batches map[uint64]BatchID
}

// NewAllocator returns the allocator of one producer run. The batch counter
// starts at 1.
func NewAllocator(producer, run ID16) (*Allocator, error) {
	if producer.IsZero() || run.IsZero() {
		return nil, fmt.Errorf("%w: unset producer or run of an allocator", ErrInvalid)
	}
	return &Allocator{producer: producer, run: run, next: 1, batches: map[uint64]BatchID{}}, nil
}

// Producer returns the producer identity of the allocator.
func (a *Allocator) Producer() ID16 { return a.producer }

// Run returns the run identity of the allocator.
func (a *Allocator) Run() ID16 { return a.run }

// EnsureBatch returns the batch of the decision, allocating the next batch
// sequence on the first call. It is idempotent per decision: a repeated
// call for the same decision returns the same batch and does not advance
// the counter, so a retry after a marker repeat or a restart restores the
// previous binding instead of allocating a second batch. Only a decision
// with public events may call it; a decision without public events records
// a decision checkpoint and allocates nothing (D79). The second result
// reports whether this call allocated the batch.
func (a *Allocator) EnsureBatch(decision uint64) (BatchID, bool, error) {
	if batch, ok := a.batches[decision]; ok {
		return batch, false, nil
	}
	// Check the counter before writing anything, so an overflow leaves the
	// allocator unchanged instead of reusing the last sequence.
	next, err := addUint64(a.next, 1)
	if err != nil {
		return BatchID{}, false, err
	}
	batch, err := NewBatchID(a.producer, a.run, a.next)
	if err != nil {
		return BatchID{}, false, err
	}
	a.batches[decision] = batch
	a.next = next
	return batch, true, nil
}

// AdoptBatch restores the allocation of a decision from the durable record
// of a previous run of the allocator, for example after a restart or a
// repeated decision marker. Adopting the same batch again is idempotent;
// adopting a different batch for a decision that already has one is
// ErrReallocation, because a decision keeps the batch first allocated to
// it. The batch must belong to the producer run of the allocator, and the
// batch counter moves past it so no sequence is reused.
func (a *Allocator) AdoptBatch(decision uint64, batch BatchID) error {
	producer, run, sequence := batch.Decode()
	if producer != a.producer || run != a.run {
		return fmt.Errorf("%w: batch of %s.%s on allocator of %s.%s", ErrIdentity, producer, run,
			a.producer, a.run)
	}
	if sequence == 0 {
		return fmt.Errorf("%w: batch sequence 0", ErrInvalid)
	}
	if held, ok := a.batches[decision]; ok {
		if held != batch {
			return fmt.Errorf("%w: decision %d holds %s, not %s", ErrReallocation, decision, held,
				batch)
		}
		return nil
	}
	// Compute the counter move before writing anything, so an overflow
	// leaves the allocator unchanged.
	var next uint64
	if sequence >= a.next {
		moved, err := addUint64(sequence, 1)
		if err != nil {
			return err
		}
		next = moved
	}
	a.batches[decision] = batch
	if sequence >= a.next {
		a.next = next
	}
	return nil
}

// Allocation returns the allocation of a decision that has one. A decision
// without an allocation is ErrUnallocated.
func (a *Allocator) Allocation(decision uint64) (BatchAllocation, error) {
	batch, ok := a.batches[decision]
	if !ok {
		return BatchAllocation{}, fmt.Errorf("%w: decision %d", ErrUnallocated, decision)
	}
	return BatchAllocation{Decision: DecisionID{ProducerID: a.producer, RunID: a.run, Sequence: decision},
		Batch: batch}, nil
}
