# block

Owner task: E02-08.block_metadata (#24): block metadata and lifecycle schema
incl. empty blocks; native counts are not completeness assertions. Inputs:
E01-05 profile fields, MSG/SEM.
See .project/docs/TASK_DECOMPOSITION.md and
.project/evidence/E02-parallel-plan-20261121.md (stream C).

Stdlib-only Go module, no I/O, no native runs. Sources of meaning:
MESSAGE_CONTRACT (sections 2, 4, 5, 7), CURRENT_SEMANTICS (block, header,
timestamp, finality evidence references), CONTROL_WIRE_CONTRACT (activation,
batch and observation identities), D25 (no BlockComplete/CoverageUpdated),
D79/F02 (one decision, one delivery batch).

## Model

- `BlockMetadata` — block identity (number, hash, parent hash), the source
  precision `Timestamp`, the observed `SourceTransactionCount`, the typed
  `Header` (common fields plus presence-explicit optional fork fields and
  the mandatory `source_rlp`), and the closed `FamilyDetails` oneof.
- `EthBlockDetails` — Geth-only optional `block_access_list_hash` and
  `slot_number`. The beacon root and the post-Prague requests hash are
  common optional header fields.
- `BscBlockDetails` — the observed Parlia system transaction count and the
  merged Parlia attested anchors (`justified`, `finalized`) observed with
  the import, as references.
- `BorBlockDetails` — sprint length and sprint-end observation, the
  side-chain import observation, the StateSync count with its mode
  (`END_OF_BLOCK_COMMIT` pre-Madhugiri, `BODY_TRANSACTION` after), and
  Heimdall milestone/checkpoint references.
- `BlockRecord` — one canonical period of one block identity: activation
  reference (network, block hash, producer, run, activation id with block
  ordinal 0), the admitted observation reference, metadata and the initial
  lifecycle snapshot (revision 0).
- `ActivationState` / `LifecycleEvent` / `StartApplied` / `StartPending` /
  `Apply` — the MESSAGE_CONTRACT section 5 table: ACTIVE, PENDING_APPLIED,
  REMOVED (terminal); revision monotonicity, same-revision conflict, stale
  rejection, idempotent retries.
- `NewCanonicalPeriod` — the same block returning after a reorg gets a NEW
  activation record from the new native decision; the old record and state
  stay immutable and terminal.
- `FinalityMarkers` — the per-family finality references a snapshot cites
  (ETH safe/finalized with basis; BSC attested snapshot, effective
  finalized with basis; Polygon milestone/checkpoint/native finalized/L1
  checkpoint). References, not derived truth; status derivation from
  ancestry is E02-03.lifecycle_validator.
- `DeliveryBatchRef` — batch id (producer 16, run 16, batch counter from 1),
  the block activations the batch carries events for, and its own delivery
  frontier (`WAL_SAVED` then `NATS_ACKED`). Nothing else: D25 excludes
  BlockComplete and CoverageUpdated, and `ParseBlockEventKind` rejects
  those names with `ErrBlockCompleteForbidden`.

## Invariants (tested)

- Counts are observations: no count has a minimum, zero is a valid observed
  value, and mutating any count never affects record validity or lifecycle
  validation (`TestCountsAreObservationsNotLifecycleInputs`).
- Empty blocks are first-class records; the BSC and Bor fixtures include
  real observed empty blocks with count 0 and gas used 0.
- Absence is explicit (`Optional[T]`, null in the local encoding); present
  zero values (BSC pre-London base fee 0, present zero beacon root, ETH
  post-merge difficulty 0) stay present.
- The closed family oneofs have exactly one variant and no ANY fallback;
  unknown families, unknown enum names and unknown JSON fields are
  rejected.
- Timestamps preserve source precision; SECONDS carries nanos 0 and
  MILLISECONDS carries whole-millisecond nanos (BSC derivation included).

## Local encoding

`encode.go` provides a JSON local record encoding used by the portable
tests. It assigns no wire field numbers, is not the D73 fragment codec and
makes no canonical-bytes claim; E02-02.schemas and E02-02.codec own the
wire registry. Unknown fields are rejected so the closed schema cannot be
bypassed through JSON.

## Fixtures

`fixtures_data_test.go` carries values extracted offline from the retained
E01-05 run records (ETH run-02 `6f46e18`, BSC run-09 `54d048c`, Bor run-04
`aae2fac`; exact paths and SHA-256 in the file header). They pin presence
and observed values only; they are not native run evidence.

## Open questions

1. BSC vote evidence boundary: the full D62 payloads (early vote evidence
   with validator/quorum/matching counts, GenesisFallback reasons, the
   effective basis transitions) belong to the BSC evidence schema consumed
   by E02-03.lifecycle_validator. This package carries only the merged
   attested anchors and the effective basis slot. Confirm the split when
   E02-02/E02-03 land.
2. Polygon unavailability reasons: CURRENT_SEMANTICS defines
   `Evidence<T>=Known|NotObserved|Unavailable{reason}` for the Polygon
   evidence fields. The marker skeleton here carries the same reason enum
   so a snapshot can record an unavailable optional lookup. If the
   lifecycle validator wants Unavailable states only in the full evidence
   payloads, the marker can be narrowed later without a wire change.
3. Heimdall references: `HeimdallCheckpointRef` carries kind, end anchor
   and an optional numeric checkpoint id. The Milestone record also has a
   string `milestone_id` and the full evidence has SourceObservationRef
   with the D80 digest. Those stay with the Polygon evidence schema; a
   block record only cites the covering range. Confirm whether the string
   milestone id must also be citable here.
4. Bor sprint fields: `SprintLength` and `SprintEnd` are recorded as
   observed fork context (the retained fixture used a four-block sprint
   with StateSync committed at the boundary). E04-01.block_record may
   prefer to derive them from the pinned fork configuration; if so, the
   fields here become the observation the derivation is checked against.
5. Counter start values: CONTROL_WIRE_CONTRACT says durable counters start
   at 1; the retained E01 traces show decision and observation sequences
   starting at 0. This package requires the batch counter to be at least 1
   and leaves decision/observation sequences unrestricted. Confirm the
   intended floor for decision and observation counters.
6. The local JSON encoding is for tests and in-process diagnostics only.
   If any later stage wants to persist it, that use needs its own review
   against the D73/D76 storage rules; it must never become the wire form.
