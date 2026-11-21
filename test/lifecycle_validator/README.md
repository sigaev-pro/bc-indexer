# test/lifecycle_validator

Owner task: E02-03.lifecycle_validator (#28, phase 2): test lifecycle automaton over the E02 schemas.

This module is the **schema-independent half** of E02-03: a deterministic
automaton that folds reordered lifecycle events into one unambiguous result
per activation. It operates on `protocol/semantic` `SemanticDescriptor`
values only (family; kind Applied/Removed/Status with the revision rules;
`ActivationRef` identity). A production consumer is **not** created; this is
a test oracle for the lifecycle rules, later reused by E03-11.block_lifecycle
and E05-01.registry as the semantic oracle.

**Scope boundary:** schema-level message validation (typed payloads,
`RecordFragment` trees, evidence profiles) is the parallel E02-02.schemas
stream's scope. That validator wires in **in front of** this automaton after
its merge: an event reaches `Apply` only after the E02-02 schema half has
accepted its message content. This module creates no protocol/proto files and
validates no message content beyond what `SemanticDescriptor.Validate`
enforces.

## Layout

- `lifecycle.go` — the automaton: `Automaton`, `Record`, `Result`, the
  outcome `Class` vocabulary, the `State` names, `ActivationKey`.
- `lifecycle_test.go` — the lifecycle table rows, reorder permutations,
  conflict/stale/duplicate/unknown outcomes, reinclusion coexistence,
  terminal immutability, value semantics.
- Go module `github.com/sigaev-pro/bc-indexer/test/lifecycle_validator`,
  standard library plus `protocol/semantic` (local `replace`), go 1.25,
  single-threaded, no goroutines.

## Design

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
- `Evidence`: revision → digest, at most one event per revision (slot 0 is
  the Applied event); `MaxRevision` is the frontier.

### Outcome vocabulary

Every applied event gets exactly one class:

| Class | Meaning |
|---|---|
| `Accepted` | The event advanced the record: a lifecycle state transition and/or a new evidence frontier revision. |
| `Duplicate` | Idempotent retry: the same descriptor digest is already recorded at its revision. The record is unchanged. |
| `Conflict` | Content clash: the revision slot or the entity signature is already recorded with a different digest. The record is unchanged; nothing is overwritten. |
| `Stale` | The event cannot advance the record: an older revision than the evidence frontier, or any non-duplicate event on a terminal record. A stale event with a free revision slot is merged as evidence only; it never replaces newer evidence and never reactivates. |
| `Unknown` | The event references an activation with no record: the reference is recorded (pending evidence or a tombstone), and the activation itself is not invented. |

A descriptor that fails `SemanticDescriptor.Validate` is a caller error (an
`error` return), not an outcome.

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
  Status/Removed rev>0), `Digest()` as the content discriminator: same digest
  = same event (Duplicate), same revision with a different digest = Conflict.

## Checks

`gofmt` clean, `go vet` clean, `go test -race` clean; coverage 94.2% of
statements (the remainder is defensive error branches on already-validated
data). Standard library and `protocol/semantic` only; no Docker, no native
runs.

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
   revision.** A writer must not emit it. The automaton records it as stale
   evidence and never advances the lifecycle state; whether a stricter
   validator should flag it as a distinct error class is a vocabulary
   decision for the schema-level half (E02-02) or a later contract change.
4. **Digest granularity.** "Same revision, different digest" is judged over
   the full `SemanticDescriptor` digest (identity + revision + source). The
   record-level `record_digest` over fragment bytes is the E02-02/E02-07
   layer; if that layer defines a finer content digest, it can replace the
   discriminator inside `Apply` without touching the state rules.
5. **Evidence map bounds.** The map grows with distinct revisions per
   activation. For the test automaton this is unbounded by design; a
   production registry (E05-01) needs its own retention rule.
