# protocol/compatibility — E02-04.compatibility (#29)

Owner task: E02-04.compatibility (#29, phase 2): read/replay/new-write
capability matrix. See .project/docs/TASK_DECOMPOSITION.md and
.project/evidence/E02-parallel-plan-20261121.md.

Version-independent policy skeleton for saved-state compatibility: the
read/replay/new-write capability matrix, the immutable-pending rule and
the drain guard. The package defines no concrete encoding versions, no
version constants and no wire formats; it fixes only the decision rules.
Concrete encoding versions (the `transport_version=2`,
`record_format_version=1` and sibling identifiers) are owned by
E02-02.codec (#27, a parallel stream at the time of writing) and wire in
through the `EncodingVersion` interface once that module merges. Nothing
here imports the codec, and no `protocol/proto` or `protocol/codec` files
are created by this task.

The package uses only the Go standard library and performs no I/O. The
tests are portable: no fixtures, no native runs.

## Normative anchors

| Rule family | Anchor |
|---|---|
| Capability matrix | CONTROL_WIRE_CONTRACT (version fields; an unknown code/version is a protocol incompatibility with the data preserved and no fallback success; an unknown layout version is not accepted). DELIVERY_STATE_MACHINE "Errors, ENOSPC, совместимость": preflight versions (`store_format,plan_format,wire_schema,validator_profile,native_profile`) are checked before any writable mutation; a compatible reader republishes the old bytes; new writes use the declared new schema only after preflight. |
| Never a silent migration | DELIVERY_STATE_MACHINE: an incompatible persistent store format change is rejected before a write even with empty payload; there are no automatic migrations; UPGRADE-01 requires the refusal to hold even after a drain. CONTEXT "Совместимость сохранённого состояния": completing delivery does not make an incompatible store compatible. |
| Immutable pending | DELIVERY_STATE_MACHINE: bytes under an existing ID are never changed; CONFIG_BLOCKED/invalid pending does not mutate or delete the batch; after SEALED the plan, layout, headers and frame bytes are immutable. CONTROL_WIRE_CONTRACT: `IMMUTABLE_CONFLICT` for a conflicting recorded range; a different manifest for the same batch is a conflict, not a rewrite. |
| Drain guard | DELIVERY_STATE_MACHINE "Drain и cutover": DRAIN only recovers and finishes saved obligations; only after native obligations are exhausted does the producer fix drain_id/highwater and seal; an incompatible wire/plan upgrade requires DRAINED by the old build. DELIVERY_RECOVERY: a drain completes obligations; it is not a format change. CONTEXT "Drain"/"Resume": a completed drain preserves identities and still-needed recovery state; Resume continues the saved producer/run. |
| Delivery states and modes | DELIVERY_STATE_MACHINE transition table: producer INTENT/STAGING/READY/SEALED/ACKED_CLEANUP plus the release outbox; producer modes LIVE/DRAIN/DRAINED/BLOCKED. |

## Design

Three rule families, each pure and independently testable, composed by
`Policy` into the preflight performed before any writable mutation.

### 1. Capability matrix (`matrix.go`, `version.go`)

`EncodingVersion` is what a versioned encoding must expose: an opaque
`VersionID`, a readability predicate `Reads(from)` and a writability
predicate `WritesNew()`. The policy never parses or orders identifiers
and never assumes that a newer version reads an older one: read/replay of
an older version by a newer one is allowed exactly when the newer version
registered that claim, per pair and never transitively.

`Matrix` answers, over the registered versions:

- `CanRead(from, to)` — `to` decodes `from`'s records. Identity always
  holds; otherwise the claim of `to` decides; unknown versions on either
  side are `ErrUnknownVersion` (no fallback).
- `CanReplay(from, to)` — `to` republishes `from`'s pending bytes. Replay
  retransmits the exact durable bytes and never reserializes, so the
  claim is the read claim of the same pair; the rejection type
  (`ErrReplayUnsupported`) is separate because the operational stop
  differs.
- `CanWriteNew(from, to)` — new records in `to` while the store carries
  `from`. Ordered rules: both versions registered; `to` must be the
  current version (`ErrWriteTargetNotCurrent` — new writes always target
  the current version, never quietly extend an older one); `to` must be
  writable (`ErrVersionNotWritable`); and when `from != to`, `to` must
  read `from` (`ErrStoreTransitionRejected`) — a transition to a format
  that cannot read the remaining metadata is rejected before any write
  and is never migrated silently, regardless of drain state.

### 2. Immutable pending (`pending.go`)

A pending record's version is fixed once durable. `RewritePending`
rejects any rewrite under a different version with `ErrPendingImmutable`,
which names the record and both versions; the durable record keeps its
original version. A rewrite under the original version is the ordinary
chunk-retry path and not a compatibility decision. The rule is
version-agnostic: it holds even for versions the running build never
registered.

### 3. Drain guard (`drain.go`)

`DrainGuard.AllowFormatTransition` decides the timing of a store-format
transition from a `DrainSnapshot` (producer mode, pending count, highest
delivery state among pending obligations):

- Zero pending and no drain in progress (LIVE, DRAINED or BLOCKED with
  zero pending) — allowed; the matrix still applies on its own.
- Any pending obligation — rejected (`ErrTransitionBlocked`), with the
  reason naming mid-drain delivery when pending sits at SEALED or later
  (those obligations must replay byte-identically).
- Mode DRAIN — rejected even with zero pending: the drain has not yet
  fixed drain_id/highwater and sealed, so the transition waits for the
  completed, sealed drain.

Contradictory snapshots (unset or unknown mode, negative pending, a
delivery state without pending obligations, pending without a state) are
`ErrInvalidSnapshot`; input is never rounded to a permissive default.
Reading and replaying pending during a drain are not guarded: finishing
saved obligations is exactly what a drain does, so those decisions belong
to the matrix alone.

### Composition (`policy.go`)

`Policy.PreflightTransition(durable, target)` runs the families in a
fixed order: the matrix decides capability first (an incompatible pair is
rejected regardless of drain state), then the drain guard vetoes the
timing of any real transition (`durable.Version != target`) regardless of
what the matrix allowed. Writing in the store's current version is not a
transition: the guard does not apply, so a live build keeps capturing
while earlier batches are pending. `Policy.ContinuePending(durable)`
answers whether the running build may continue old obligations: the
current version must replay the durable version, otherwise the pending
bytes and their version stay untouched and wait for a compatible build.

## Wiring after E02-02.codec (#27)

The codec module will implement `EncodingVersion` for its concrete
versions (transport, record format, layout, header, manifest) and
register them (`NewMatrix`/`Matrix.Register`), declaring one current
version and explicit `Reads` claims for the older ones. This package
needs no change for that wiring; the tests' `fakeVersion` double mirrors
the expected shape. The matrix is safe for concurrent use so codec
registration can happen while decisions run.

## Open questions (recorded, not guessed)

1. Whether replay needs its own registered predicate once codec lands —
   a version could be readable for diagnostics yet forbidden to replay.
   The skeleton derives replay from the read claim (replay retransmits
   exact bytes), which is the stricter-safe reading of the current
   contract.
2. Whether mode BLOCKED with zero pending should allow a format
   transition. The card names only pending obligations and an in-progress
   drain as blockers, so BLOCKED is currently treated as "not mid-drain".
3. Whether the matrix should enforce a direction (older to newer) on
   claims instead of trusting registrations to be well-formed;
   identifiers are opaque here, so no ordering exists to check against.
4. Whether `WritesNew() == false` can occur for the current version in
   practice, or whether writability only matters for candidate versions
   during a future codec negotiation; the matrix keeps the check so a
   read-only current version rejects new writes instead of defaulting.
5. How the codec will classify version pairs that may upgrade after a
   completed DRAINED versus store formats that can never upgrade within
   the same store. Both map to the same matrix rejection here; UPGRADE-01
   requires the prewrite refusal to hold for the persistent format case,
   which the matrix already guarantees without consulting the guard.
