# protocol/compatibility — E02-04.compatibility (#29)

Owner task: E02-04.compatibility (#29, phase 2): read/replay/new-write
capability matrix. See .project/docs/TASK_DECOMPOSITION.md and
.project/evidence/E02-parallel-plan-20261121.md.

Policy for saved-state compatibility — the read/replay/new-write
capability matrix, the wire/plan versus persistent-store transition
rules, the immutable-pending rule and the drain guard — plus the concrete
encoding versions pinned by the proto registry (`TransportV2`,
`RecordFormatV1`, `LayoutV2`, `HeaderV2`, `ControlV2`, `ManifestV2`)
registered per kind in a default registry. The pinned identifiers mirror
`protocol/proto/registry.go` (E02-02.schemas #26: transport=2,
record_format=1, layout=2, header=2, control=2, manifest=2); this module
stays stdlib-only and does not import the proto module. Further versions
— including the encodings owned by E02-02.codec (#27) — wire in through
the `EncodingVersion` interface; nothing here imports the codec.

The package uses only the Go standard library and performs no I/O. The
tests are portable: no fixtures, no native runs.

## Normative anchors

| Rule family | Anchor |
|---|---|
| Capability matrix | CONTROL_WIRE_CONTRACT (version fields; an unknown code/version is a protocol incompatibility with the data preserved and no fallback success; an unknown layout version is not accepted). DELIVERY_STATE_MACHINE "Errors, ENOSPC, совместимость": preflight versions (`store_format,plan_format,wire_schema,validator_profile,native_profile`) are checked before any writable mutation; a compatible reader republishes the old bytes; new writes use the declared new schema only after preflight. |
| Never a silent migration | DELIVERY_STATE_MACHINE: an incompatible persistent store format change is rejected before a write even with empty payload; there are no automatic migrations; UPGRADE-01 requires the refusal to hold even after a drain. CONTEXT "Совместимость сохранённого состояния": completing delivery does not make an incompatible store compatible. |
| Wire/plan versus store upgrade | DELIVERY_STATE_MACHINE "Errors, ENOSPC, совместимость": an incompatible wire/plan upgrade requires DRAINED by the old build; an incompatible persistent store format change is not permitted by a drain. CONTROL_WIRE_CONTRACT "TransportFrame layout v2": the wrapper carries the original immutable fragment bytes and independent reserialization is forbidden, so replay is the read claim. |
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
  claim is the read claim of the same pair (see resolved question 1); the
  rejection type (`ErrReplayUnsupported`) is separate because the
  operational stop differs.
- `CanWriteNew(from, to, claim)` — new records in `to` while the store
  carries `from`, under the transition policy of the `claim` family.
  Ordered rules: the family must be stated (`ErrInvalidClaim` — never
  defaulted); both versions registered; `to` must be the current version
  (`ErrWriteTargetNotCurrent` — new writes always target the current
  version, never quietly extend an older one); `to` must be writable
  (`ErrVersionNotWritable`); and when `from != to` and `to` cannot read
  `from`, the family decides: `ClaimStoreFormat` rejects before any write
  (`ErrStoreTransitionRejected`) regardless of drain state, while
  `ClaimWirePlan` treats the pair as a timing question and leaves the
  DRAINED requirement to the drain guard (see below).

### 2. Immutable pending (`pending.go`)

A pending record's version is fixed once durable. `RewritePending`
rejects any rewrite under a different version with `ErrPendingImmutable`,
which names the record and both versions; the durable record keeps its
original version. A rewrite under the original version is the ordinary
chunk-retry path and not a compatibility decision. The rule is
version-agnostic: it holds even for versions the running build never
registered.

### 3. Drain guard (`drain.go`)

`DrainGuard.AllowFormatTransition` decides the timing of a format
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

`DrainGuard.AllowIncompatibleWireUpgrade` decides the stricter timing of
an incompatible wire/plan upgrade: every ordinary rule above applies, and
beyond them mode DRAINED is required explicitly. A quiet LIVE or BLOCKED
store is not enough — the saved obligations must be finished and sealed
by a build that still understands the old format before a build that
cannot read it starts writing (DELIVERY_STATE_MACHINE: an incompatible
wire/plan upgrade requires DRAINED by the old build). The persistent
store format family never reaches this rule: an incompatible store pair
is rejected by the matrix regardless of drain state, so no drain state
can be spent on permitting it.

Contradictory snapshots (unset or unknown mode, negative pending, a
delivery state without pending obligations, pending without a state) are
`ErrInvalidSnapshot`; input is never rounded to a permissive default.
Reading and replaying pending during a drain are not guarded: finishing
saved obligations is exactly what a drain does, so those decisions belong
to the matrix alone.

### Composition (`policy.go`)

`Policy.PreflightTransition(durable, target, claim)` runs the families in
a fixed order: the matrix decides capability first (an incompatible
store-format pair is rejected here regardless of drain state), then, for
a real transition (`durable.Version != target`), the drain guard vetoes
the timing regardless of what the matrix allowed — a compatible pair
follows the ordinary timing rules, an incompatible wire/plan pair
requires mode DRAINED. Writing in the store's current version is not a
transition: the guard does not apply, so a live build keeps capturing
while earlier batches are pending. `Policy.ContinuePending(durable)`
answers whether the running build may continue old obligations: the
current version must replay the durable version, otherwise the pending
bytes and their version stay untouched and wait for a compatible build.

### Concrete versions and the default registry (`versions.go`, `registry.go`)

`ProtocolVersion` is the concrete `EncodingVersion` implementation: an
identifier, an explicit list of readable earlier versions and a
writability flag. Six pinned instances mirror the version constants of
`protocol/proto/registry.go`, one per kind, named after the constants:

| Version | Mirrors | Identifier |
|---|---|---|
| `TransportV2` | `TransportVersion = 2` (TransportFrame wrapper) | `transport/2` |
| `RecordFormatV1` | `RecordFormatVersion = 1` (RecordFragment format) | `record_format/1` |
| `LayoutV2` | `LayoutVersion = 2` (transport layout) | `layout/2` |
| `HeaderV2` | `HeaderVersion = 2` (immutable header contract) | `header/2` |
| `ControlV2` | `ControlVersion = 2` (control wire protocol) | `control/2` |
| `ManifestV2` | `ManifestVersion = 2` (READY manifest) | `manifest/2` |

Every pinned version is the first of its kind, so each read set is empty
and identity is the only readability. The digest-domain strings of the
contract (`extractor.network.v1`, `extractor.record.v1`,
`extractor.fragments.v1`, `extractor.layout.v2`, `extractor.manifest.v2`,
`extractor.reservation.v2`, `extractor.binding.v1`) are fixed parts of
their encodings' bytes, not preflight version fields, and are not
registered. `DefaultRegistry` wires the six into one matrix per kind
(kinds version independently and a matrix tracks one current new-write
version, so the kinds cannot share a single matrix); a cross-kind
question is `ErrUnknownVersion` on any one matrix — the safe rejection.

### Claim types: wire/plan versus persistent store format (`claim.go`)

`ClaimType` states which preflight version family of
DELIVERY_STATE_MACHINE a transition decision belongs to; the two families
answer the same version pair differently, so the family is a required
parameter (`ErrInvalidClaim`), never defaulted:

- `ClaimWirePlan` — the transport wrapper, record format, layout,
  headers, control protocol and manifest versions. An incompatible
  upgrade in this family is a timing question: a completed drain closes
  every saved obligation, so after DRAINED by the old build the new
  format's inability to read the old one stops mattering for new writes.
  The matrix passes the pair and the drain guard enforces DRAINED.
- `ClaimStoreFormat` — the persistent `store_format` preflight version of
  the durable store itself (no concrete version registered here; the
  store format is owned outside the wire contract). An incompatible
  change is a capability rejection before any write, regardless of drain
  state: identity and lifecycle metadata remain for the store's whole
  life, there are no automatic migrations, and even a completed drain
  with empty payload does not permit the change (UPGRADE-01).

This is the resolved form of the skeleton's conservative never-rule: the
skeleton rejected every incompatible pair regardless of drain state,
which was safe but also blocked the wire/plan upgrades DELIVERY_STATE_MACHINE
allows after DRAINED. The two-rule policy keeps the never-rule where it
is normative (persistent store format) and opens the DRAINED-gated path
where it is normative (wire/plan).

## Wiring after E02-02.codec (#27)

The codec module will implement `EncodingVersion` for its concrete
encodings and register them (`NewMatrix`/`Matrix.Register`), declaring one
current version per kind and explicit `Reads` claims for the older ones;
`ProtocolVersion` is the worked example of that shape, and the tests'
`fakeVersion` double mirrors it for hypothetical histories (a future
transport v3 claiming transport/2, a store format, and so on). The matrix
is safe for concurrent use so codec registration can happen while
decisions run. Transition decisions state their claim family
(`CanWriteNew`, `PreflightTransition`); read and replay decisions stay
family-free.

## Resolved questions

1. (Was: whether replay needs its own registered predicate once codec
   lands.) Resolved: no separate predicate — read implies replay for
   these encodings. Replay retransmits the exact durable bytes and never
   reserializes: the transport wrapper carries the original immutable
   fragment bytes and their independent reserialization is forbidden
   (CONTROL_WIRE_CONTRACT "TransportFrame layout v2"), and a compatible
   reader republishes the old bytes while a SEALED plan always repeats
   its original layout, headers and bytes (DELIVERY_STATE_MACHINE). A
   version that can decode the records can therefore republish them; a
   version readable "for diagnostics yet forbidden to replay" cannot be
   expressed by these encodings, and `CanReplay` remains the read claim
   of the same pair with its own rejection type.
5. (Was: how the codec will classify version pairs that may upgrade
   after a completed DRAINED versus store formats that can never upgrade
   within the same store.) Resolved by the `ClaimType` parameter:
   DELIVERY_STATE_MACHINE "Errors, ENOSPC, совместимость" draws exactly
   two rules — an incompatible wire/plan upgrade requires DRAINED by the
   old build and is allowed then (a drain closes every obligation), while
   an incompatible persistent store format change is never permitted
   within the same store, whatever the drain state (no automatic
   migrations; UPGRADE-01 keeps the prewrite refusal even after a drain
   with empty payload). The skeleton's single never-rule was the safe
   intersection of both; the two-rule policy is the correct shape, and
   the prewrite rejection for incompatible store pairs still holds
   without consulting the guard.

## Open questions (recorded, not guessed)

2. Whether mode BLOCKED with zero pending should allow a format
   transition. The card names only pending obligations and an in-progress
   drain as blockers, so BLOCKED is currently treated as "not mid-drain".
   (For an incompatible wire/plan upgrade BLOCKED is not enough: the
   DRAINED requirement is explicit.)
3. Whether the matrix should enforce a direction (older to newer) on
   claims instead of trusting registrations to be well-formed;
   identifiers are opaque here, so no ordering exists to check against.
4. Whether `WritesNew() == false` can occur for the current version in
   practice, or whether writability only matters for candidate versions
   during a future codec negotiation; the matrix keeps the check so a
   read-only current version rejects new writes instead of defaulting.
