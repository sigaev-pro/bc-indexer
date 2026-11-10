# Owned capture seam

`core/capture` is the E01-02.owned_capture output: the ownership boundary
between native execution and the capture worker (REQ-CAP-05, REQ-CAP-06). It is
a separate Go module with no dependencies and a `go 1.25` directive, so the
accepted Geth and BSC (`go 1.25.0`) and Bor (`go 1.26.10`) module graphs can
compile it unchanged. It imports no client type and performs no I/O.

## Contract

One native goroutine (the producer) and one worker share a `Capturer`.

| Call | Who and where | Waits? |
|---|---|---|
| `CaptureField`, `CaptureBytes` | Producer, at a safe point outside any callback, while the native view is stable | Yes, before each frame, until the budget fits it |
| `Reserve` | Producer, at a safe point before a callback | Yes, until the credit fits |
| `EnterCallback`, `Note`, `ExitCallback` | Producer, inside a native callback | Never; `Note` spends reserved credit |
| `Next`, `Release` | Worker, in frame order | `Next` waits for a frame |
| `Close`, `Abort` | Producer or owner | No |

- A field becomes `BEGIN_FIELD`, then `CHUNK` frames of at most `ChunkBytes`
  (at most 16,384, the BlobChunk data limit), then `END_FIELD`. Every frame has
  a gap-free sequence. Each chunk carries its offset, length and SHA-256; the
  end frame carries the whole-field length and SHA-256. No call holds a whole
  field: `CaptureField` reads through a `ReadFunc` into one reused chunk buffer.
- A frame holds only value fields and a Go string. Strings are immutable, so a
  worker cannot reach or change native memory through a frame. `Data` returns a
  new copy. A test checks the whole worker-visible type graph for slices, maps,
  pointers, channels, functions and interfaces.
- Inside a callback, every call that can wait (`CaptureField`, `CaptureBytes`,
  `Reserve`, `Close`) fails at once with `ErrInCallback`. A callback records
  data only with `Note` (at most 256 bytes), paid from credit that `Reserve`
  took at the preceding safe point. Missing credit is `ErrCredit`; the call
  never waits for the worker.
- Any producer error stops the capture (fail closed): the worker receives the
  same error, never a partial field, and all budget is returned. A canceled
  worker wait does not stop the capture.
- A reservation that only already-reserved credit blocks fails with
  `ErrTooLarge` instead of waiting forever.

## Memory bound

The budget `BufferBytes` covers every queued frame, every delivered frame the
worker has not released, and all unused credit. Each frame costs its payload
plus `FrameOverhead` (128 bytes, at least the size of a `Frame` value). The
frame ring is preallocated with `BufferBytes / FrameOverhead` slots, so it never
grows. The additional collector memory is therefore at most `BufferBytes` of
frame payload and overhead, the ring itself (no more than `BufferBytes`), and
one `ChunkBytes` scratch buffer, plus allocator rounding. Native heap and state
caches are the node's own memory and are not part of this bound.

## Checks

`go test -race -cover .` runs the unit tests (100% statement coverage). They
include a 64 MiB field through a 64 KiB budget with a slow worker, the
overlapping-buffer case of AC-CAP-05-01, and the safe-point wait of AC-CAP-06-02.
The native probe in [test/harness](../../test/harness/README.md#owned-capture-probe-e01-02owned_capture)
runs this module inside the accepted Geth, BSC and Bor import paths.

The product adapter patches that call the seam belong to the adapter and
E04-04 tasks; the probe's VM safe point is test-only.
