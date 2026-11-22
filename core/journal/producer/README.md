# Producer delivery journal

`core/journal/producer` is the E03-01.producer_store output: the producer
state machine INTENT -> STAGING -> READY -> SEALED -> ACKED_CLEANUP ->
release outbox -> RETIRED of
[DELIVERY_STATE_MACHINE](../../../.project/docs/DELIVERY_STATE_MACHINE.md),
on the shared [storage seam](../storage).

## State machine

| Call | Guard | One atomic commit |
|---|---|---|
| `SetDestinationBinding` | before the first capture, once | binding digest |
| `AllocateBatch(decision, eventCount)` | mode LIVE, binding saved | batch_seq + intent + decision index + counter |
| `StartAttempt(seq, eventCount)` | INTENT or STAGING | attempt_generation++ + fresh event sequence range + counter |
| `WriteAttemptData` / `WriteAttemptManifest` | current attempt, contiguous offsets | one chunk |
| `CommitReady(seq, gen, len, checkpoint)` | STAGING, complete manifest stream | ready marker (digest, length, count) + checkpoint + READY + prune of replaced attempts |
| `BeginPlan(seq, seed, layoutDigest, header)` | READY (or idempotent replay; new generation only after `AbandonPlan`) | plan generation + header (seed, layout digest) |
| `AbandonPlan(seq)` | READY, plan not sealed | durable refusal of the unpublished plan |
| `WritePlanChunk` | current plan generation | one chunk |
| `Seal(seq, gen, total)` | READY, complete plan stream | SEALED marker (plan digest = SHA-256 of the seed, layout digest, generation, size) |
| `OnIngestAck(seq, ack)` | SEALED, ack matches plan/manifest/binding | full IngestAck + release_outbox + cleanup cursor + ACKED_CLEANUP |
| `Cleanup(seq, maxKeys)` | ACKED_CLEANUP | bounded payload deletion (attempt chunks, plan payload); the SEALED marker, outbox, ack, READY marker and checkpoint survive |
| `OnReleaseAck(seq)` | cleanup done, seq is exactly retired_prefix+1 | retired_prefix advance + outbox/cleanup/SEALED deletion + RETIRED |

Rules held by the store:

- **Identity.** `batch_id = (producer_id, run_id, batch_seq)`; the store
  identity is generated with crypto RNG on first open. A replayed native
  decision returns its original batch (`decision/<id>` index is written in
  the same commit as the intent), so a retry never allocates a second batch.
- **Attempts do not change semantic identity.** `StartAttempt` keeps the
  batch and decision, increments the attempt generation and reserves a fresh
  event sequence range; the replaced attempt's inputs are pruned only by the
  next winning closing commit.
- **Closing commits are atomic.** READY, SEALED, ACKED_CLEANUP and RETIRED
  are each a single storage batch (see `core/journal/storage`); a crash
  between transitions reopens at the last committed state.
- **Immutability.** After READY the manifest digest and event identity are
  fixed (`ErrImmutableConflict` on any different bytes under the same ID);
  after SEALED only replay works, and a new plan generation requires a
  durable `AbandonPlan` while still READY (pre-SEALED gateway-loss recovery).
- **Ordered release.** `OnReleaseAck` accepts exactly the next batch in
  sequence; replays and already-retired prefixes are idempotent successes,
  jumps are rejected. The outbox holds every acked batch until its own turn.
- **Modes.** Only LIVE admits new capture; DRAIN finishes saved obligations;
  DRAINED survives restart without auto-returning to LIVE; `ResumeToLive`
  allocates the next monotonic admission generation in the same commit as
  the mode switch.
- **One serialized writer.** Every call runs under the store mutex; counter
  overflow is checked before commit and values are never reused.

## Key layout

See `keys.go` for the physical encoding fixed by this module (the logical
prefixes are DELIVERY_STATE_MACHINE's). Records are versioned binary layouts
(`records.go`); an unreadable record is a fatal error, never a default.

## Scope

No network, no RocksDB import; the module compiles into the native client
build graphs (`go 1.25`, same rule as `core/capture`). The transport-plan
bytes are opaque to this store: computing the layout manifest digest and
frame bytes is the E02 codec's job, feeding this journal.
