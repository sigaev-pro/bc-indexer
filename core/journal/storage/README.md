# Journal storage seam

`core/journal/storage` is the shared persistence seam of the E03 Block 1
stores (`core/journal/producer`, `gateway/journal`). Both state machines of
DELIVERY_STATE_MACHINE run on
RocksDB (10.4.2, grocksdb v1.10.2 per COMPONENT_STACK), but neither store may
import a concrete database: this module fixes the seam so the stores compile
and test without a native RocksDB build. The RocksDB adapter is a thin wrapper
(Get/Put/Delete map to sync writes, Batch maps to one sync `WriteBatch`,
Iterate maps to a bounded iterator) added when the demo environment runs.

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

The module is a separate Go module with a `go 1.25` directive, so the accepted
Geth, BSC and Bor client build graphs can compile it unchanged (same rule as
`core/capture`). It has no dependencies.
