// Package storage is the journal persistence seam shared by the producer and
// gateway stores (E03-01.producer_store, E03-01.gateway_store).
//
// Both state machines of DELIVERY_STATE_MACHINE run on RocksDB (10.4.2 with
// grocksdb v1.10.2 per COMPONENT_STACK), but neither store may import a
// concrete database: the seam is this small ordered-key interface so the
// stores compile and test without a native RocksDB build. The RocksDB adapter
// is a thin wrapper over the same interface added when the demo environment
// runs; tests use the memory-backed adapter in this package.
//
// Contract of every implementation:
//
//   - Keys and values are opaque byte strings ordered by unsigned byte
//     comparison. Iteration is over the half-open range [lo, hi); an empty hi
//     means "to the end of the key space".
//   - Apply is atomic and durable: either every operation of the batch is
//     visible after the call returns (sync commit, WAL enabled) or none is.
//     A closing commit that binds several records into one marker is exactly
//     one Apply, which is what makes every state-machine transition
//     crash-safe. No implementation may split one batch.
//   - Apply rejects a batch larger than MaxBatchBytes before touching state
//     (CONTROL_WIRE_CONTRACT: a bounded WriteBatch is at most 1 MiB including
//     metadata and marker bytes).
//   - Get returns a copy; callers cannot reach into the store's memory.
package storage

import "errors"

// ErrNotFound is returned by Get for a missing key. Stores translate it into
// their "absent" answers and never into a corruption error.
var ErrNotFound = errors.New("storage: key not found")

// ErrBatchTooLarge is returned by Apply when the batch exceeds
// MaxBatchBytes. The batch is not applied, so the caller can retry with
// smaller bounded chunks.
var ErrBatchTooLarge = errors.New("storage: batch exceeds 1 MiB bound")

// ErrStopIterate is a sentinel a visit function may return to stop iteration
// early without reporting an error.
var ErrStopIterate = errors.New("storage: stop iteration")

// MaxBatchBytes is the byte bound of one atomic batch: the sum of key and
// value bytes plus BatchOpOverhead per operation (CONTROL_WIRE_CONTRACT,
// "Sizes, headers, memory": a bounded WriteBatch is at most 1 MiB including
// metadata and the marker).
const MaxBatchBytes = 1 << 20

// BatchOpOverhead accounts for the per-operation metadata a physical
// WriteBatch carries alongside the key and value bytes.
const BatchOpOverhead = 16

// Storage is the ordered-key persistence seam. One serialized writer per
// store is the caller's rule (DELIVERY_STATE_MACHINE); implementations must
// still be safe for concurrent use, and Apply of one batch is the unit of
// atomicity.
type Storage interface {
	// Get returns a copy of the value for key, or ErrNotFound.
	Get(key []byte) ([]byte, error)
	// Put stores value under key durably.
	Put(key, value []byte) error
	// Delete removes key. Deleting a missing key is not an error.
	Delete(key []byte) error
	// Apply applies every buffered operation of b atomically and durably.
	Apply(b *Batch) error
	// Iterate visits every key/value pair with lo <= key < hi in ascending
	// byte order. An empty hi iterates to the end. Pairs are visited from a
	// stable view: an implementation either copies the range first or
	// otherwise guarantees the visit is not disturbed by writes. Returning
	// ErrStopIterate from visit stops the walk; any other error aborts it and
	// is returned.
	Iterate(lo, hi []byte, visit func(key, value []byte) error) error
}

// Batch buffers ordered operations for one atomic commit. A Batch is created
// with NewBatch, filled with Put and Delete, and handed to Storage.Apply. A
// Batch is not safe for concurrent use; the single serialized writer owns it.
type Batch struct {
	ops  []batchOp
	size int
}

type batchOp struct {
	del   bool
	key   []byte
	value []byte
}

// NewBatch returns an empty batch.
func NewBatch() *Batch { return &Batch{} }

// Put buffers a key/value write.
func (b *Batch) Put(key, value []byte) {
	b.ops = append(b.ops, batchOp{key: append([]byte(nil), key...), value: append([]byte(nil), value...)})
	b.size += len(key) + len(value) + BatchOpOverhead
}

// Delete buffers a key deletion.
func (b *Batch) Delete(key []byte) {
	b.ops = append(b.ops, batchOp{del: true, key: append([]byte(nil), key...)})
	b.size += len(key) + BatchOpOverhead
}

// Len returns the number of buffered operations.
func (b *Batch) Len() int { return len(b.ops) }

// Size returns the accounted byte size of the batch: key and value bytes plus
// BatchOpOverhead per operation.
func (b *Batch) Size() int { return b.size }

// Reset empties the batch so its buffer can be reused.
func (b *Batch) Reset() {
	b.ops = b.ops[:0]
	b.size = 0
}
