# Journal storage seam

`core/journal/storage` is the shared persistence seam of the E03 Block 1
stores (`core/journal/producer`, `gateway/journal`). Both state machines of
DELIVERY_STATE_MACHINE run on
RocksDB (10.4.2, grocksdb v1.10.2 per COMPONENT_STACK), but neither store may
import a concrete database: this module fixes the seam so the stores compile
and test without a native RocksDB build.

The durable implementation is the separate module
`core/journal/storage/rocksdb`. It is a thin wrapper over this interface
(Get/Put/Delete map to synced writes, Batch maps to one synced `WriteBatch`,
Iterate maps to a bounded iterator) and needs CGO and the native engine.
This module and the stores build without it.

## Interface

```go
type Storage interface {
    Get(key []byte) ([]byte, error)          // ErrNotFound when absent
    Put(key, value []byte) error
    Delete(key []byte) error
    Apply(b *Batch) error                    // one atomic, durable commit
    Iterate(lo, hi []byte, visit func(key, value []byte) error) error
}
```

Rules every implementation must hold:

- Keys are opaque bytes in unsigned byte order; iteration covers the half-open
  range `[lo, hi)`, an empty `hi` means "to the end".
- `Apply` is atomic and durable: all operations or none, sync commit before
  return. A closing commit that binds a manifest, identity and checkpoint into
  one marker is exactly one `Apply`; that is what makes each state transition
  crash-safe.
- `Apply` rejects a batch over `MaxBatchBytes` (1 MiB including per-operation
  overhead, per CONTROL_WIRE_CONTRACT) before touching state.
- `Get` and iteration return copies; callers cannot alias store memory.
- Iteration walks a stable snapshot of the range.
- `Batch.Visit` is how an implementation outside this package reads the
  buffered operations. `Apply` of one batch is one atomic commit; no
  implementation may split one batch.

## Implementations and tests

- `MemoryStorage` (`memory.go`) holds the state in memory. Tests and tools
  use it where no disk is involved.
- `core/journal/storage/rocksdb` holds the durable implementation on the
  engine that decision D33 accepted.
- `core/journal/storage/storagetest` is the conformance suite. Every
  implementation runs the same cases, so the durable store is checked by the
  same evidence as the memory store. `TestMemoryConformance` in this module
  runs it for `MemoryStorage`. Besides the seam rules above, the cases pin
  what the journals rely on: a batch deletes keys that are there and applies
  its operations in the order given, `Put` replaces a value, and a walk
  returns the value stored under each key.

The module is a separate Go module with a `go 1.25` directive, so the accepted
Geth, BSC and Bor client build graphs can compile it unchanged (same rule as
`core/capture`). It has no dependencies.
