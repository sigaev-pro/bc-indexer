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
| `BatchState(id, plan, after, limit)` | read-only; from PREPARED on the plan digest must equal the stored plan | one bounded page of frame cursors; the IngestAck only while NATS_ACKED is durable |
| `Release(id, manifest, plan, maxKeys)` | NATS_ACKED or RELEASING, seq exactly retired_prefix+1, both digests equal the NATS_ACKED marker | the begin, bounded cleanup and complete steps up to RETIRED |
| `RecoveryRelease(id, ack, assertion)` | exactly retired_prefix+1, no local record, the ack names the active binding | recovered assertion + RETIRED + retired_prefix, one commit |
| `SealGeneration(run, intent)` | active binding generation, retired_prefix equals the highwater, nothing unfinished at or below it | Seal record (the resume record of a resumed generation is retired) |
| `PrepareSwitch(run, oldSeal, desired, switchID)` | sealed and not resumed, the old seal equals the durable one, same destination line and network, generation exactly next | Switch record, PREPARED |
| `CommitSwitch(switchID, prepared, digest, generation)` | the exact prepared receipt and a matching binding commit assertion | Switch record COMMITTED + the active binding |
| `Resume(run, intent)` | sealed, the old seal equals the durable one, active binding, admission generation exactly next | Resume record |
| `CheckPrepareSwitch` / `CheckCommitSwitch` | the same rules as the two rows above, read-only | nothing: a receipt to a retry, an error to a refusal |

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
- **Batch state answers only what is durable.** A batch at or below the
  retired prefix is RETIRED with no ack: its evidence left with the retire
  commit. A batch the store never saw is State 0. The IngestAck is returned
  only while the NATS_ACKED marker exists (NATS_ACKED and RELEASING), and
  the cursor page is bounded by the caller's limit with a continuation flag.
- **Release binds the whole identity.** A retry for an already retired
  sequence answers the durable prefix again; a sequence above
  retired_prefix+1, a batch that is not NATS_ACKED, or another manifest or
  plan digest creates nothing. The cleanup runs in bounded steps and a
  crash resumes from the remaining keys.
- **RecoveryRelease is for a batch the store lost.** It needs exactly the
  next sequence, no local record (an existing one is an immutable conflict
  and is kept), and an assertion that names the same plan and binding as
  the saved IngestAck. The accepted assertion is durable, so a retry with a
  different ack is refused.
- **Seal, switch and resume bind the whole intent.** A retry returns the
  durable receipt; the same identity with another intent, or a new identity
  over an unfinished one, is `ErrImmutableConflict`. A seal needs
  `retired_prefix == highwater` (zero is the empty frontier); only the
  admission generation a Resume opened can be sealed again. A switch needs
  a seal that was not resumed, a descriptor of the same destination line and
  network at exactly the next generation, and the active binding identity
  (the `Options.Binding` option on a store that never committed a switch,
  otherwise the durable `meta/destination_binding` record); without it the
  continuity checks cannot run and the switch is `ErrBindingMismatch`.
  `CommitSwitch` makes the new binding active in the same commit, and
  reopening the store keeps it: a stale option never reverts a binding.
  A committed switch of an earlier seal is history, so every later drain
  cycle can switch again. `CheckPrepareSwitch` and `CheckCommitSwitch` run
  the same rules without writing.

## Key layout

See `keys.go` for the physical encoding fixed by this module (the logical
prefixes are DELIVERY_STATE_MACHINE's); records are versioned binary
layouts in `records.go`, `state.go` and `cutover.go`. Scope notes: the
publisher NATS client, the account/stream metadata checks of D77 and the
process wiring of the Seal/Switch/Resume RPCs belong to the ingest boundary
and later blocks; this journal provides their state basis.
