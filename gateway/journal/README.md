# Gateway delivery journal

`gateway/journal` is the E03-01.gateway_store output: the gateway state
machine PREPARING -> PREPARED (reservation) -> RECEIVING -> CLOSED ->
NATS_ACKED -> RELEASING -> RETIRED of
DELIVERY_STATE_MACHINE, on
the shared storage seam.

## State machine

| Call | Guard | One atomic commit |
|---|---|---|
| `BeginPrepare(id, digest, eventCount, total, mode, importSeed)` | seq above the retired prefix; digest conflict checks | prepare_meta (+ imported reservation in IMPORT_SEALED mode) + PREPARING |
| `PrepareChunk(id, digest, offset, sha, data)` | bound digest, contiguous offsets, data <= 64 KiB | one manifest chunk |
| `FinalizePrepare(id, digest)` | complete stream, rehash equals the digest | reservation seed + sequence range + PREPARED (normal); state switch only (import) |
| `BeginPublish(id, seed, planDigest, layoutDigest, entryCount)` | seed equals the stored reservation, planDigest = SHA-256(seed) | publish record + RECEIVING |
| `PublishChunk(id, planDigest, addr, meta, offset, sha, data)` | plan identity, first chunk declares the frame, offsets contiguous, data <= 64 KiB | declaration + one frame chunk |
| `FinalizePublish` / `SealImport(id, plan, manifest, layout)` | complete frames of every declared entry, recomputed layout digest matches | CLOSED marker + CLOSED |
| `RecordPubAck(id, plan, addr)` | CLOSED batch, declared frame | ackset key |
| `MarkNATSACKed(id, plan, manifest)` | every frame of every fragment acknowledged | NATS_ACKED marker + state |
| `BeginRelease(id, plan)` | NATS_ACKED | RELEASING marker + cleanup cursor |
| `ReleaseCleanup(id, maxKeys)` | RELEASING | bounded payload deletion |
| `CompleteRelease(id)` | cleanup done, seq exactly retired_prefix+1 | retired_prefix advance + evidence deletion + RETIRED |

Rules held by the journal:

- **Reservation retry never allocates a new sequence.** The reservation is
  committed once; `BeginPrepare`/`FinalizePrepare` replays return the saved
  seed byte-for-byte (the acceptance case of E03-01.gateway_store), a
  different digest after the reservation is `ErrImmutableConflict`, and the
  next batch's `sequence_base` continues exactly after the reserved range.
- **IMPORT_SEALED** (`PrepareImportSealed`) re-imports a SEALED producer
  batch after gateway store loss: the caller supplies the original seed, no
  sequence or reservation id is allocated, and the original
  `issuer_store_epoch` is preserved. `SealImport` (not `FinalizePublish`)
  closes an imported batch; the modes cannot be mixed.
- **Seed layout** is the exact `extractor.reservation.v2` binary of
  CONTROL_WIRE_CONTRACT; `plan_digest = SHA256(seed)`. The layout manifest
  digest is recomputed over the declared entries in
  `extractor.layout.v2` form before CLOSED.
- **Chunks are idempotent**: identical retry returns the same next offset, a
  hole returns `ErrOffsetMismatch` with the expected cursor, a conflicting
  rewrite is `ErrImmutableConflict`, data chunks are at most 64 KiB, and a
  stream closes only after every contiguous chunk is verified.
- **CLOSED is the only publishable state**; PubAck accounting runs only on
  CLOSED batches, undeclared frames are never created by a late callback,
  duplicates are tolerated, and NATS_ACKED needs every frame acknowledged.
- **Ordered release**: retired_prefix advances one batch at a time per
  producer/run; `BeginPrepare`/`PublishChunk`/finalize for a retired seq
  answer `ErrAlreadyRetired` without creating keys; the RELEASING marker
  survives the bounded cleanup until the final commit.
- **One serialized writer** and checked counters (no overflow, no reuse).

## Key layout

See `keys.go` for the physical encoding fixed by this module (the logical
prefixes are DELIVERY_STATE_MACHINE's); records are versioned binary
layouts in `records.go`. Scope notes: the publisher NATS client, the
account/stream metadata checks of D77 and the Seal/Switch/Resume cutover
RPCs belong to E03-03 and later blocks; this journal provides their state
basis.
