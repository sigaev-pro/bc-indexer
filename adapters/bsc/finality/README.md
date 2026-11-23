# BSC finality (E05-01.bsc_finality)

`adapters/bsc/finality` is the typed BSC finality evidence seam of
[E05-01.bsc_finality](../../../.project/docs/TASK_DECOMPOSITION.md): REQ-LIF-06
(AC-LIF-06-01 to 06-03). BSC has two different native finality surfaces —
the early vote-only result that Parlia's `GetFinalizedHeader` derives from
the in-memory VotePool quorum (observed at `NotifyFinalized`, the caller row
`bsc.notify-finalized` of the lifecycle map), and the header-derived
snapshot attestation (the merged `VoteData` Source/Target of the native
transition logic). This package keeps them separate and gives the GC its
only BSC evidence source:

- every observation is typed with its own provenance (`Origin`:
  `NATIVE_LIVE`, `NATIVE_RESTART`, `RETAINED_DURABLE`) and its own digest;
- the projection (`Project`) reports a **header status**
  (`FINALIZED_HEADER_ATTESTED`, `JUSTIFIED`, `NO_ATTESTED_FINALITY`,
  `CANONICAL_UNFINALIZED`) and an **early status**
  (`VOTE_FINALIZED_OBSERVED`, `NOT_COVERED`, `NOT_OBSERVED`) per activation;
  the two are never collapsed into one unconditional finalized flag;
- the GC anchor (`Observatory.ObserveHeader`) accepts **only** the
  attested snapshot source — the contract-established header `Source` — on
  the canonical execution ancestry. A vote-derived effective result
  (`VOTE_POOL_QUORUM` basis), `CurrentFinalBlock` or a persisted
  `LastFinalized` hash alone never becomes an anchor.

The pinned native facts and the logical contract are in
[ETH_BSC_FINALITY_CONTRACT](../../../.project/docs/research/ETH_BSC_FINALITY_CONTRACT.md);
the typed fields follow the BSC evidence section of
[CURRENT_SEMANTICS](../../../.project/docs/CURRENT_SEMANTICS.md) (D62).

## What this package does not do

No native client runs here. The error-aware native snapshot accessor (the
Parlia projection that returns the header-derived Source/Target with the
fallback reason instead of collapsing a lookup error), the instrumented
effective-finality branch, the vote-envelope spool of the native bridge and
the AC verdicts on a running node are the native BSC block, on the accepted
tree of the E01 blocks; every AC-LIF-06-01 to 06-03 profile cell stays
NOT_RUN. The product activation/revision registry, the decision mirror and
the Removed/Applied/Status publication are E05-01.registry; the release of
pins, the terminal/pending conditions and the actual cleanup are
E05-01.activation_gc, which consumes `Observatory.Anchor` as its BSC
evidence. The public getter (`CurrentFinalBlock`) cannot distinguish a
missing reference from a snapshot error, so nothing here reads it.

## Model

All block references are `decision.Head` values (number, 64-digit lowercase
hash; the zero hash never encodes a known block).

| Type | Meaning |
|---|---|
| `HeaderFinality` | oneof: `AttestedSnapshot{SnapshotHead, Justified, Finalized}` (the target is justified, the source is finalized) or `GenesisFallback{SnapshotHead, Genesis, Reason: NO_ATTESTATION \| PRE_PLATO}` |
| `Reference` | oneof: `Known{Head, Origin}` or `Unknown{Reason: NOT_OBSERVED \| NATIVE_REFERENCE_UNSET}` — the native effective finalized observation |
| `EffectiveBasis` | `HEADER_SNAPSHOT \| VOTE_POOL_QUORUM \| GENESIS_FALLBACK \| UNSET` — the basis of the native effective result |
| `EarlyVoteEvidence` | `{Origin, EvaluationHead, SourceJustified, VoteTarget, FinalizedCandidate, ValidatorSnapshotParent, ValidatorCount, QuorumCount, MatchingVoteCount, EvidenceDigest}` |
| `Evidence` | `{ObservedExecutionHead, ObservedNativeHeaderHead, HeaderFinality, NativeEffectiveFinalized, EffectiveBasis, Early?}` |

Shape rules (`Evidence.valid` and friends): an attested snapshot carries no
fallback fields and satisfies `finalized ≤ justified ≤ snapshot` by number;
a fallback carries the real genesis hash (number 0); a known reference
carries a valid origin and no unknown reason; the basis agrees with the
rest — `HEADER_SNAPSHOT` only with the attested snapshot source as the
effective result, `GENESIS_FALLBACK` only with the fallback genesis,
`VOTE_POOL_QUORUM` only with the early evidence whose `FinalizedCandidate`
is the effective result, `UNSET` only with an unknown reference. A fresh
early observation has origin `NATIVE_LIVE` or `NATIVE_RESTART`, a
non-empty validator set, `QuorumCount == ceil(2N/3)` (at least two),
`QuorumCount ≤ MatchingVoteCount ≤ ValidatorCount`, and
`candidate ≤ source ≤ target ≤ evaluation head` by number.

## Projection

`Project(evidence, activation, ancestry)` computes the two statuses of one
activation. Coverage is exact ancestry, both directions: an evidence block
`e` covers the activation `a` only when `a` is an ancestor of or equals `e`
**and** `e` itself is an ancestor of or equals the observed execution head —
so a side-branch observation covers nothing even when the activation sits
below the fork point (AC-LIF-06-03). The genesis fallback covers only the
genesis activation itself; everything above it is `NO_ATTESTED_FINALITY`,
never an error and never a number of confirmations. An ancestry lookup error
is `ErrSource`, never a status.

## Observatory

`Observatory` is the durable evidence store of one producer run. Records
live under `extractor-bsc-finality-v1/<producer>.<run>/{early,evidence,anchor}`
(every key is longer than 33 bytes and starts with "e", so the native
offline pruner keeps it). Every record carries its store ID and producer and
a digest; a record of another store or producer is `ErrInvariant`, a damaged
one `ErrCorrupt`.

- `ObserveEarly(token, evidence, votes)` stores one early vote observation
  of this process together with the digest of the owned snapshot of the
  verified vote envelopes and validator context it was computed from; the
  declared `EvidenceDigest` must equal `EarlyDigest(evidence, votes)`, so the
  record binds its spool (`ErrConflict` otherwise). A power loss before the
  sync drops the record: only transient early evidence is lost, and a lost
  early observation never lowers a durable anchor.
- `Early(token)` serves the stored early evidence **only** with
  `RETAINED_DURABLE` provenance after a restart (AC-LIF-06-02): retained
  evidence, never a fresh VotePool result. A fresh observation with a
  retained origin is refused; a startup refresh observation
  (`NATIVE_RESTART`) may replace the record.
- `ObserveHeader(token, evidence, ancestry, decisionSequence)` records one
  full evidence and applies the GC anchor gate. `decisionSequence` is the
  sequence of the native decision that observed the snapshot (0 for a
  startup refresh observation).

### The anchor gate

In order:

1. **Heads reconcile first.** The native header head must be on the
   canonical execution ancestry of the observed execution head (either side
   may be briefly ahead, never on another branch). A snapshot from a side
   branch — a delayed side-branch or same-height different-hash notification
   after the canonical branch settled — is `ErrBranch`: it changes nothing,
   finalizes nothing and is not a regression (AC-LIF-06-03).
2. **The accepted anchor is irreversible.** The execution head must still
   cover the accepted anchor by ancestry. A head below it or beside it — the
   operator rewind or a canonical change that abandoned a header-finalized
   block — is a proven regression: `ErrRegression` and the observatory
   stops; the caller stops the native owner before READY or events of the
   conflicting decision.
3. **Only the attested snapshot source is a candidate.** A genesis fallback
   gives no candidate (`Refused`). A candidate that is not on the canonical
   execution ancestry gives no candidate either (`Refused`, no error).
4. **Monotonic by ancestry.** With no anchor the candidate is `Accepted`;
   an equal candidate is `Unchanged`; a descendant is `Advanced`; a lower
   candidate on the accepted chain is `Stale` — the anchor stays and
   nothing is written, because a lower getter snapshot or a vanished
   VotePool is never a proven regression. A candidate that neither contains
   the accepted anchor nor lies on its chain — a different hash at the
   anchor height or an incompatible branch, observed by the authoritative
   header source — is `ErrRegression` (fatal, the observatory stops; every
   later call is `ErrFailed`).

A stale callback, a lower safe, a vanished VotePool or an unavailable header
blocks GC and is never fatal; only a proven regression below the accepted
anchor is.

## Digests

SHA-256 of the tag, one zero byte, then the fields; a string is its 8-byte
big-endian length and its bytes, a number is 8 bytes big-endian, a head is
its number then its 32 raw hash bytes.

| Digest | Tag | Fields |
|---|---|---|
| header snapshot | `extractor-bsc-finality-header-v1` | `ATTESTED`: snapshot head, justified, finalized; `GENESIS_FALLBACK`: snapshot head, reason, genesis, genesis |
| early evidence | `extractor-bsc-finality-early-v1` | origin, evaluation head, source, target, candidate, validator parent, validator count, quorum, matching, SHA-256 of the vote snapshot |
| anchor record | `extractor-bsc-finality-anchor-v1` | native store ID, producer (`<producer>.<run>`), anchor head, header evidence digest, decision sequence |

`python3 test/harness/bsc_finality.py vectors` computes the expected values
of `vectors.json` from these formulas, independently of the Go code;
`test/harness/test_bsc_finality.py` checks that they match.

## Tests

The package compiles only in the assembled module
(`python3 test/harness/bsc_finality.py assemble DIR`, which extends the
lifecycle binding module with this package, the BSC reorg tests of
`test/native_lifecycle/bsc` and a `protocol/semantic` replace).
`finality_test.go` runs there on an in-memory store model with injected
power loss: the digest vectors, the shape rules, the separate-status
projection (including the side-branch and genesis-fallback cases), the
anchor gate (accept, advance, unchanged, stale, the two fatal regressions,
the refused side branches), the retained-early restart semantics, the early
record integrity and a concurrent-observation check. The reorg, reapply,
side-branch and reordered-event behavior is in
[`test/native_lifecycle`](../../../test/native_lifecycle/README.md).
