# test/lifecycle_validator

Owner task: E02-03.lifecycle_validator (#28, phase 2, completed by the
schema half): test lifecycle automaton over the E02 schemas.

This module has two halves of one test oracle. The **semantic half**
(`lifecycle.go`) is a deterministic automaton that folds reordered lifecycle
events into one unambiguous result per activation; it operates on
`protocol/semantic` `SemanticDescriptor` values (family; kind
Applied/Removed/Status with the revision rules; `ActivationRef` identity).
The **schema half** (`schema.go`, added when E02-02.schemas merged) wires
`protocol/proto` in front of it: lifecycle events arrive as schema messages
(a proto `Envelope`, its READY `EventDescriptor` v2 and the canonical
`TransportFrame` bytes of the record's fragments), are validated through
the proto seam, and only the verified `SemanticDescriptor` reaches the
state machine. A production consumer is **not** created; this is a test
oracle for the lifecycle rules, later reused by E03-11.block_lifecycle and
E05-01.registry as the semantic oracle.

## Layout

- `lifecycle.go` — the semantic half: `Automaton`, `Record`, `Result`, the
  outcome `Class` vocabulary, the `State` names, `ActivationKey`, the
  caller-supplied content `Digest` discriminator.
- `schema.go` — the schema half: `SchemaEvent` (envelope + event descriptor
  + canonical frame bytes) with its end-to-end `Validate`, `NewSchemaEvent`
  (the writer-side assembly), and `SchemaAutomaton` composing the semantic
  half with the stream rules (ordinal continuity, writer validity).
- `lifecycle_test.go` — the semantic half's tests: the lifecycle table
  rows, reorder permutations, conflict/stale/duplicate/unknown outcomes,
  reinclusion coexistence, terminal immutability, value semantics.
- `schema_test.go` — the schema half's tests: schema-level round-trips over
  the contract table and every registry family, exhaustive and shuffled
  reorder convergence at the schema seam, ordinal continuity rejection, the
  writer-invalid post-terminal error class, the record-digest
  discriminator, and corrupted-message rejection per named error.
- Go module `github.com/sigaev-pro/bc-indexer/test/lifecycle_validator`,
  standard library plus `protocol/semantic` and `protocol/proto` (local
  `replace`s), go 1.25, single-threaded, no goroutines.

## Design

### Schema half (E02-03 completion)

The schema half is the composition the module README of the first half
promised: the proto validator sits **in front of** the state machine, and
the automaton's content discriminator is now the record-level
`record_digest`.

**`SchemaEvent`.** One lifecycle event at the schema seam: the
`proto.Envelope` (public event id, network profile, `SemanticDescriptor`,
payload fragments), the READY `EventDescriptor` v2 of the record, and the
canonical `TransportFrame` encodings of its fragments — one COMPLETE frame
per fragment in index order (PART frames and their assembly are the
codec/layout layers' territory). `NewSchemaEvent` is the writer-side
assembly: it runs the proto envelope validation at the batch ordinal,
encodes the frames, and pins the shared record identity on every frame —
event id, record digest, fragment count and digests from the READY
descriptors, and the one chain sequence `sequence_base + ordinal`
(counters start at 1).

**`Validate` composes, in order:**

1. `proto.ValidateEnvelope` — the phase-1 semantic identity, the event id
   agreement, the closed family/kind/payload-variant triangle admitted by
   the network profile, the typed payload views, and the READY descriptor
   build over the payload fragments (`record_length`, `record_digest`, the
   fragment digests, `fragment_manifest_digest`, with fragment index
   continuity and the length sum of `ValidateFragments`). Proto rejections
   pass through with their named sentinels.
2. The claimed `EventDescriptor` must equal the recomputed one; a lying
   manifest descriptor is `ErrDescriptorMismatch` with the offending field
   named (in the descriptor encoding order), never a silent re-labeling.
3. Every frame decodes from its canonical bytes (the pinned v2 wrapper
   table rejects unknown fields, duplicates, out-of-order tags, non-minimal
   varints, wrong versions, a doubled or missing oneof) and agrees with the
   verified event: COMPLETE form, fragment index continuity `0..N-1`, the
   shared event id, record digest, fragment count, network and chain
   sequence (one sequence per logical event, `base >= 1` so it stays above
   the ordinal), and each frame's fragment bytes are exactly the envelope's
   payload fragment and hash to the manifest digest pinned for them.
   Disagreements are `ErrFrameMismatch`.

On success `Validate` returns the envelope's `SemanticDescriptor` as a
detached copy; the same event always validates to the same descriptor.

**`SchemaAutomaton`.** Composes the semantic-half `Automaton`. `Apply` runs,
in a fixed order: schema validation, writer validity, ordinal continuity,
then the fold `Apply(descriptor, record_digest)`. A rejection is an error,
never an outcome, and changes nothing — not the records and not the ordinal
ledger.

**Ordinal continuity.** Ordinals are batch-scoped: the READY manifest fixes
them `0..N-1` per public batch, so they restart in every batch and global
cross-batch continuity is not well-defined for a lifecycle stream. The rule
this seam enforces is the per-activation one: the ordinals of one
activation's events must arrive at consecutive steps — the first event of
an activation fixes its base, every later event of the same activation
carries exactly the previous ordinal plus one. A gap, a duplicate or a
regression is `ErrOrdinalContinuity`; the rejected event consumes no
ordinal, so the corrected delivery still applies. Interleaved activations
keep independent ledgers (`TestSchemaOrdinalContinuity`). Reorder
convergence is preserved: every permutation of a writer-valid event set,
with ordinals assigned at delivery positions the way a writer emitting that
order would number them, converges to the identical final record
(`TestSchemaReorderPermutations`, `TestSchemaMixedActivationShuffles`).

**Writer validity (open question 3, resolved).** MESSAGE_CONTRACT section 5
fixes that the writer creates no new Status events for a terminal
activation. The semantic half cannot tell a post-terminal StatusChanged
from a pre-terminal one that merely arrived late, so it stale-records both.
The schema half can: the revisions of one activation come from one
monotonic counter, and a StatusChanged whose revision is **above** the
recorded Removed revision (kept in `Record.RemovedRevision` once the record
terminalizes) must have been created after the removal. The
`SchemaAutomaton` rejects it as a distinct error class, `ErrWriterInvalid`,
before the fold — it is never stale-recorded, and the rejection consumes no
ordinal. Everything the writer may still emit keeps its semantic-half
outcome: a StatusChanged at or below the removed revision is late evidence
(`Stale`) or, at exactly the removed revision, the ordinary revision-clash
`Conflict`; a late Applied merges as the tombstone origin (`Stale`); a
second Removed adds late evidence (`Stale`) — the contract names only new
Status events as forbidden (`TestSchemaWriterInvalidPostTerminalStatus`).

**Digest discriminator (open question 4, resolved).** The discriminator
inside `Apply` is now caller-supplied: `Apply(d, content Digest)` folds
with the digest the layer in front pins for the event bytes, and the state
rules are untouched. The schema half supplies the record-level
`record_digest` from the proto `EventDescriptor` — SHA-256 over the
record's fragment bytes — replacing the descriptor digest. This aligns
`Duplicate` with the contract's own words ("retry Applied with the same
bytes is the same fact"): identical record bytes at one revision are
`Duplicate` **even when the descriptors differ** (different source
provenance), and differing bytes at an occupied revision are still
`Conflict` (`TestSchemaRecordDigestDiscriminator`). A semantic-only caller
passes the descriptor digest; an unset zero digest is a caller error
(`ErrUnsetDigest`).

### Activation identity

### Activation identity

An activation record is keyed by `ActivationKey{Network, ProducerID, RunID,
ActivationID{DecisionSequence, EntityOrdinal}}` — the decision entity that
one producer run observed on one network. The event family, block hash and
inclusion binding are **content**, not key parts: the first accepted event of
a record fixes an `EntitySignature`, and a later event with a different
signature is a Conflict (identity clash), never a silent overwrite.

Revision monotonicity holds inside this domain only, matching the contract
note that revisions are monotonic within one producer/run's lifecycle object,
not across independent runs or networks. Independent runs and networks
therefore keep separate records (`TestIndependentObservationsDoNotMix`).

### Record

A `Record` is a value type; every transition stores a fresh copy and every
returned record is a detached deep copy:

- `State`: `PENDING_APPLIED` → `ACTIVE` → `REMOVED`, never backwards.
- `Origin`: the Applied descriptor once observed; `nil` means not observed —
  it is never invented for evidence-only or tombstone-only activations.
- `Evidence`: revision → content digest, at most one event per revision
  (slot 0 is the Applied event); `MaxRevision` is the frontier.
- `RemovedRevision`: the revision that terminalized the record, meaningful
  only in `REMOVED` and never changed by later evidence; the schema half's
  writer-validity check reads it.

### Outcome vocabulary

Every applied event gets exactly one class:

| Class | Meaning |
|---|---|
| `Accepted` | The event advanced the record: a lifecycle state transition and/or a new evidence frontier revision. |
| `Duplicate` | Idempotent retry: the same content digest (record bytes at the schema seam) is already recorded at its revision. The record is unchanged. |
| `Conflict` | Content clash: the revision slot or the entity signature is already recorded with a different digest. The record is unchanged; nothing is overwritten. |
| `Stale` | The event cannot advance the record: an older revision than the evidence frontier, or any non-duplicate event on a terminal record. A stale event with a free revision slot is merged as evidence only; it never replaces newer evidence and never reactivates. |
| `Unknown` | The event references an activation with no record: the reference is recorded (pending evidence or a tombstone), and the activation itself is not invented. |

A descriptor that fails `SemanticDescriptor.Validate` — or, at the schema
seam, any message rejection — is a caller error (an `error` return), not an
outcome.

### Determinism and reorder independence

`Apply` is a pure transition over value records: no goroutines, no
wall-clock, no map-order inputs. For any **clash-free** event multiset of one
activation (at most one event per revision, at most one Applied), every
delivery order produces the identical final record, because the record is a
function of the recorded set:

- the state is `REMOVED` if any Removed is recorded, else `ACTIVE` once the
  Applied origin is recorded, else `PENDING_APPLIED`;
- each revision slot holds the one event offered at it; the frontier is the
  maximum revision.

Two mechanisms make this hold under reordering:

1. **Removed terminalizes regardless of delivery order.** A Removed accepted
   into a non-terminal record moves it to `REMOVED` at any revision
   (CURRENT_SEMANTICS: the Removed revision comes from the same activation
   counter as StatusChanged, and its terminal meaning does not depend on
   delivery order).
2. **Stale events merge into free slots.** An older StatusChanged arriving
   after a newer one (or any late event arriving after Removed) is recorded
   in its free slot without moving the frontier or the state, so both orders
   of `{Status(3), Removed(5)}` or `{Applied, Status(2), Status(5),
   Removed(7)}` converge (`TestReorderExhaustivePermutations`, all
   permutations via Heap's algorithm; `TestReorderMixedActivations`, 500
   seeded LCG shuffles of interleaved activations).

Clashing multisets (two different digests at one revision, or two entity
signatures under one key) are exactly the Conflict scenarios. There the
**first accepted record wins** and the clash is reported on the later event;
the final record of such an invalid set necessarily depends on which content
arrived first, which is why the contract forbids such streams rather than
defining a winner. The automaton's promise for them is: one deterministic
outcome per event, no silent overwrite, state unchanged on rejection.

### Reinclusion

After Removed, the record is terminal: no event reactivates it. A returned
previously-removed entity arrives as a **new activation** — a new decision
sequence (and block) — so the automaton keys it as a separate record and both
coexist (`TestReinclusionCreatesNewActivation`). The old record stays
immutable terminal: its state, entity, origin and recorded slots never
change; only never-before-seen revision slots may be added by late evidence.

### Terminal immutability, precisely

After `REMOVED`: the state, the entity signature, the origin (once merged)
and every already-recorded revision slot never change. A late Applied that
was never observed is merged as the tombstone's origin with outcome `Stale`
(the tombstone "keeps its cancel meaning until the late Applied" — it
cancels that applied record instead of reactivating); everything else is
Duplicate, Conflict, or Stale-with-slot-merge (`TestTerminalImmutabilityAfterRemoved`).

## Normative anchors

- `.project/docs/MESSAGE_CONTRACT.md` section 5 — the lifecycle state table
  (ABSENT/PENDING_APPLIED/ACTIVE/REMOVED rows, retry Applied = same fact,
  revision clash = conflict not last-write-wins, late events never reactivate,
  return of the same block = new Applied(B), B≠A) and the paragraph on
  revision monotonicity per producer/run and no invented records.
- `.project/CONTEXT.md` — «Каноническая активация» (canonical activation: a
  returned previously-removed entity creates a new activation),
  «Включение» (inclusion: re-inclusion in another block is a different
  inclusion), «Результат сценария» (CaseResult: verdicts need actual run
  evidence — the tests here are that evidence for this module).
- `.project/docs/CURRENT_SEMANTICS.md` — Applied revision=0; StatusChanged
  carries a full replacement snapshot with revision>0; Removed carries
  revision>0 from the same activation counter and is terminal regardless of
  delivery order; a repeated native decision does not create a new revision.
- `protocol/semantic` — `SemanticDescriptor`, `Validate()` (Applied rev=0,
  Status/Removed rev>0), `Digest()` as the semantic-only content
  discriminator.
- `protocol/proto` (E02-02.schemas, merged) — `Envelope` and
  `ValidateEnvelope` (family/kind/variant triangle, typed views, READY
  descriptor build), `EventDescriptor` with the record-level
  `record_digest`, `TransportFrame` with its canonical v2 encoding, and the
  family/kind registry v1 (tx=1, block=2; applied=1, removed=2, status=3)
  of `.project/docs/CONTROL_WIRE_CONTRACT.md` ("READY manifest and
  reservation", "TransportFrame layout v2").

## Checks

`gofmt` clean, `go vet` clean, `go test -race` clean; 25 test functions
(120 subtests) pass with 94.9% statement coverage. The remainder is
defensive error branches: already-validated data in the record dumps and
comparisons, a descriptor-diff field that cannot disagree through
`Validate` (the ordinal is the recomputation input), a frame-bytes
equality branch behind a SHA-256 collision, and the ordinal-successor
overflow guards. Standard library, `protocol/semantic` and
`protocol/proto` only; no Docker, no native runs.

## Open questions (recorded, not guessed)

1. **Outcome label for post-terminal events.** The contract fixes the result
   ("does not reactivate A") but not a label. This automaton classifies a
   non-duplicate, non-clashing event on a terminal record as `Stale` while
   still merging its free revision slot for order independence. If a later
   task wants terminal records fully frozen (no slot growth), the reorder
   guarantee needs the evidence map kept per set instead — a design change to
   propose, not to guess here.
2. **Late Applied on a tombstone: merge vs reject.** This automaton merges
   the origin (outcome `Stale`) so that `{Applied, Removed}` converges to the
   same final record in both orders. The alternative (reject without merge)
   would make the final record order-dependent; rejected for that reason,
   but the contract text does not name the stored shape explicitly.
3. **Post-terminal StatusChanged with a revision above the Removed
   revision — resolved by the schema half.** The schema half now rejects it
   as the distinct error class `ErrWriterInvalid` (revision above
   `Record.RemovedRevision` implies it was created after the removal);
   see "Writer validity (open question 3, resolved)" above. The semantic
   half alone still stale-records it — that contrast is pinned by
   `TestSchemaWriterInvalidPostTerminalStatus`. Whether a post-terminal
   second Removed should join the same error class stays open: the contract
   names only new Status events as forbidden, so it keeps the `Stale`
   evidence outcome.
4. **Digest granularity — resolved by the schema half.** The discriminator
   inside `Apply` is caller-supplied; the schema half passes the proto
   record-level `record_digest` over the record's fragment bytes, replacing
   the descriptor digest. See "Digest discriminator (open question 4,
   resolved)" above.
5. **Evidence map bounds.** The map grows with distinct revisions per
   activation. For the test automaton this is unbounded by design; a
   production registry (E05-01) needs its own retention rule.
6. **Ordinal continuity scope.** Ordinals are batch-scoped (0..N-1 per
   public batch), so the schema half enforces continuity per activation in
   delivery order (consecutive steps, no gaps, no duplicates), not across
   batches. If a later delivery-layer task defines a cross-batch stream
   cursor, this ledger rule should be re-examined against it.
