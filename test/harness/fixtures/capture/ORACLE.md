# Owned capture probe oracle

This oracle was written by hand, before any probe run, from the contract layout
below. A separate Python computation produced the digests in
[expected.json](expected.json); `test_capture_probe.py` recomputes them from the
same written formulas, not from the Go probe.

## Input bytes

Every pattern byte is `(i*131 + seed*17 + (i >> 8)) mod 256` for byte index `i`.

| Pair | Caller | Callee | Size | Input seed (P) | Output seed (Q) |
|---|---|---|---|---|---|
| small | `0x…a1` | `0x…b1` | 4,096 | 1 | 2 |
| large | `0x…a2` | `0x…b2` | 262,144 | 3 | 4 |

The caller copies P to memory `[0, size)`, then calls the callee with input
`[0, size)` and output `[0, size)`. The two regions overlap completely. The
callee returns Q, so the call overwrites P in caller memory. The caller then
emits `LOG0` over `[0, size)` and stores the first memory word in slot 0.

Blocks: block 1 calls the small pair, block 2 calls the large pair, and block 3
is a 1-wei value transfer. Every scenario must import all three blocks.

## Expected observations

- Each captured CALL input equals P (tag 1, its length and SHA-256), although
  native memory holds Q by the time the worker reads it. The receipt logs equal
  Q, and slot 0 of each caller equals the first 32 bytes of Q. This shows that
  native memory changed and the captured bytes did not (AC-CAP-05-01).
- `overlap_delayed_worker`: the worker starts only after the whole import. No
  wait is needed, the budget is never exceeded, and two callback notes exist.
- `vm_safe_backpressure`: a 32 KiB budget and 4 KiB chunks. The 256 KiB input
  must stop the producer at the VM safe point while block 2 executes (head 1),
  with no callback open, no new frame and no import return during a hold of at
  least 300 ms. After the worker starts, the import finishes (AC-CAP-06-02,
  `vm_safe_backpressure`).
- `import_backpressure`: the budget fits exactly the frames of block 1. The
  producer must stop at the import boundary before block 2 is imported
  (head 1), with the same hold rules, then finish. Three block fields exist
  (AC-CAP-06-02, `import_backpressure`).
- In both call scenarios, a waiting call made inside the real opcode callback
  must fail at once with `ErrInCallback`.
- Every scenario head equals the generated head and the baseline import head
  without capture, so capture and the pauses did not change the native result.
  Native import also checks each block's state root, receipts and gas.
- Peak held bytes never exceed the configured budget, and no probe error exists.

Block hashes and block RLP digests are native outputs that the oracle compares
for equality across runs; it does not claim them as independent values.
