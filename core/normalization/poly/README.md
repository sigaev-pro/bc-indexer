# Polygon transaction normalization (E04, POLY family)

`core/normalization/poly` is the Polygon (Bor) family transaction
normalization of E04 (#41..#49): it turns captured native observations of
one Bor block and its body transactions into the typed records of the
message contract and validates them against the closed protocol models of
E02. It is a separate Go module (`go 1.25`, the oldest directive among the
accepted Geth, BSC and Bor graphs) with three dependencies:
[protocol/balance](../../../protocol/balance/README.md),
[protocol/block](../../../protocol/block/README.md) and
[protocol/profiles](../../../protocol/profiles/README.md), all wired with
repository-local `replace` directives. It performs no I/O, imports no
client type, and every exported function is deterministic in its inputs.

The ETH and BSC siblings of this module (their own worktrees) own the
family-neutral behavior; this module owns the Polygon-specific semantics and
the family view of the nine E04 areas. Nothing here executes or reimplements
native Bor: wrong capture data becomes an explicit error, never a repaired
record.

## Design

The seam has two sides. The **capture side** (`types.go`) is what the Bor
adapter's hooks and readers observed: live transaction rows, `OnEnter`/
`OnExit` frames, snapshot rows, monetary operations with their effects,
receipts with consensus bytes, per-transaction account boundaries, and the
StateSync records the native path committed. Every field is source data.
The **record side** is the normalized `TransactionRecord`, `BlockRecord`
and the optional `BorLegacyReceiptRecord`, with all identities, orders and
scopes derived by the normalizer and every published byte defensively
copied so the record never aliases capture memory.

Pipeline (`record.go`, `block.go`):

1. `NormalizeTransaction` checks the closed body type registry
   (0/1/2/4 applicable, 127 the StateSync type, 3 refused without a native
   applicability proof), verifies the observed transaction hash against the
   wire, applies the closed empty authorization profile, allocates the
   shared D74 order, builds the scope tree, normalizes calls, execution
   actions, balance actions, the receipt (with the D75 encoding table and
   the consensus cross-check), the state diff and the fee summary, and
   finishes with the full `protocol/balance.Validate` of the balance view.
2. `NormalizeBlock` builds `block.BlockMetadata` with the Bor family
   details, normalizes every body transaction, enforces the block-level
   coherence rules, and normalizes the optional legacy record when the
   optional scope is enabled.

### The nine ETH areas, Polygon side

| Area | File | Key rules |
|---|---|---|
| calls | `calls.go` | Frame tree with the shared order; the five address roles never substitute for each other; D81 `attempted_create_address` from observed computation, `created_address` only on local success; a successful child under a rolled-back ancestor is not committed |
| receipts | `receipt.go` | D75 kinds 1/2 (native_type 127 for StateSync) through `protocol/profiles`; strict RLP framing; consensus bytes cross-checked against the typed status/root, cumulative gas, bloom and logs; pre-status roots stay source data |
| authorization | `authorization.go` | Closed empty profile (EIP-7702 not active here): any source tuple, including invalid or duplicate ones, is an explicit error, never a dropped field |
| block record | `block.go` | `BorBlockDetails` sprint/side-chain context, StateSync count and mode, Heimdall checkpoint references in `FinalityMarker`; seconds timestamp precision; empty blocks are first-class with count 0 |
| state diff | `statediff.go` | Transaction-boundary `AccountChange` sorted by raw address bytes, storage by raw key bytes, one row per key, existence in flags, code observations mandatory when the code hash changes; `ReconcileBoundary` as the fixture oracle over `protocol/balance` |
| selfdestruct | `selfdestruct.go` | Opcode action kept at zero balance; fork eligibility and committed deletion separate; the monetary projection shares the opcode order only through `cause=ExecutionAction`; the EIP-6780 finalization burn is its own later operation with related-action links |
| balance actions | `balance.go` | D62 registry: gas purchase, gas return, the two Polygon fee credits (signer tip and base-fee burn to the zero address), frame transfers, `BOR_STATE_SYNC_EXECUTION` as `NATIVE_PROTOCOL_CHANGE`; unknown paths and the BSC operation are explicit errors |
| import pressure | `replay.go` | `EqualSemantics`/`VerifyReplay`/`VerifyReplayPrefix`/`VerifyBytesStable`: the same capture always reproduces the same IDs, orders, scopes and bytes; a prefix replay matches the same prefix and never extends counters |
| optional scope | `optional.go` | Default OFF is the mandatory verified case: no optional records, mandatory body effects unchanged; enabling requires declared typed fixtures and never mixes the legacy record into the body inventory |

### StateSync body transactions (post-Madhugiri, type 0x7f)

`statesync.go` implements the retained E01 semantics: the wire is
`0x7f || RLP(items)` with four-field items `{ID, Contract, Data, TxHash}`;
the decoded body must equal the committed records and the record IDs must
continue the observed last id (the native import refuses the same
conditions); the type is applicable only from the configured Madhugiri
height and only at the final body position; the transaction has no sender
and an `NativeProtocolExecution{BOR_STATE_SYNC, count}` projection — no
zero-filled fabrications. The call structure is the pinned one: a
`NATIVE_PROTOCOL` synthetic root frame from the system caller
`0xff..fe` to the state receiver, then per record a depth-1 commit call
(`commitState(uint256,bytes)`, selector `0x19494a17`) and the nested
`onStateReceive(uint256,bytes)` target call (selector `0x26c53bea`); the
commit payload is verified against the full six-field clerk event whenever
the capture kept it, and a caught target revert stays uncommitted source
data that never fails the outer receipt. The receipt is the native one:
status success, zero gas, absent price, cumulative copied from the last
ordinary receipt.

`BorReceiptDetails.source_log_scopes` (`scopes.go`) carries the D75
provenance: an ordinary body transaction has the present empty list; a
StateSync receipt describes every source log exactly once, by
`transaction_log_index`, in source order, with the closed
`SYSTEM_PRE | TX_STATE_SYNC | SYSTEM_POST` scope set. The provenance never
filters, reorders or rewrites logs, and a `SYSTEM_PRE`/`SYSTEM_POST` log
never becomes an action of the StateSync transaction.

### Legacy synthetic receipt (pre-Madhugiri, optional)

`legacy.go` implements the E00-15 auxiliary record: the pre-Madhugiri
system work produced a separate native storage receipt, never a body
transaction. With the optional scope enabled and fixtured, the record
carries the origin `BOR_LEGACY_SYNTHETIC_RECEIPT`, the derived identity
`keccak256("matic-bor-receipt-" || u64be(number) || block_hash)` verified
against the observed hash, the synthetic index equal to the ordinary
receipt count, the raw storage RLP as D75 kind 3 (no `native_type`),
strictly decoded and cross-checked, and the coverage `SOURCE_RECEIPT_ONLY`.
The source states stay distinct: absent, corrupt (an explicit error, never
empty success) and present-without-logs (the native writer never wrote
that). Its per-log phase attribution is an auxiliary field whose semantics
deliberately differ from the body `source_log_scopes`.

### Block-level rules

`NormalizeBlock` enforces the carrier split: before Madhugiri the system
work is the end-of-block commit (the optional legacy record, never counted
in the body inventory); from the gate on it is exactly one final body
`0x7f` whose record count equals the observed StateSync rows. The two
carriers never mix and never double the same events (POLY-L04). System work
commits at the sprint boundary (`number % sprint == 0`, the pinned
`IsSprintStart` rule the block schema records as the sprint-end
observation), the header number/parent/time must agree with the block
identity, and the header gas must equal the last receipt rollup.

### Keccak and RLP

The Go standard library ships FIPS-202 SHA3 only, so `keccak.go` carries a
compact reference Keccak-256 (legacy `0x01` padding) for the one native
derivation the contract requires (the legacy receipt identity) plus the
transaction-hash verification; `rlp.go` is a minimal strict reader/writer
for the exact native structures used here (StateSync envelope, clerk event,
receipt framing). Both are pinned by vectors from the independent E01
fixture codec (`test/type_paths/poly/poly_codec.py`), which this module
never reads.

## Anchors

- MESSAGE_CONTRACT sections 3, 3.1 (D75/D76), 3.2 and 4.
- CURRENT_SEMANTICS: D62 (balance actions and fees), D63 (Polygon evidence;
  the block record carries references only), D74 (execution order and
  rollback scopes), the Polygon evidence section.
- `.project/docs/research/POLYGON_LEGACY_CAPTURE.md` (E00-15, D17/D75):
  legacy carrier semantics, writer behavior, hash formula, POLY-L01..07.
- E01 evidence: `.project/evidence/E01-poly-madhugiri-design-20261121.md`
  (crossing case: sprint 2, two records, caught revert, receipt oracle),
  `.project/evidence/E01-05.type_paths.poly.md` and
  `E01-05.type_paths.poly_activation.md` (type applicability, importer
  refusals), `test/native_corpus/poly` (pre-Madhugiri corpus block 4).
- E02 models: `protocol/balance` (actions, causes, scopes, validator),
  `protocol/block` (metadata, `BorBlockDetails`, `HeimdallCheckpointRef`),
  `protocol/profiles` (closed receipt encodings and details variants).

## Checks

`go test -race -cover .` runs the suite offline (60 tests, 88% statement
coverage at the time of writing). The fixtures are the retained E01 cases:
the Madhugiri crossing block and the pre-Madhugiri corpus block, plus
negative controls for every closed rule (wire framing, record mismatch,
non-final position, pre-gate type, fabricated sender, scope gaps and
duplicates, consensus mismatches, optional-scope gating, replay
divergence). `gofmt -l` and `go vet` are clean. Native runtime evidence is
E01/E04-04's job; this module proves normalization rules only.

## Open questions

1. **Type 3 (blob) applicability.** The E01-05 fixture proved decode and
   pool refusal and the nil-blob-field body refusal (`data blobs present in
   block body`). The closed registry here refuses type 3 captures; a future
   profile with a native BlobSchedule applicability proof would be a
   versioned registry change.
2. **Prague, EIP-7702 and EIP-6780 together.** The pinned Bor profile
   schedules Prague, and the mission's closed empty authorization profile
   (EIP-7702 not active in the current Polygon profile, like BSC) drives
   `authorization.go`; the E01-05 type-4 imports show the envelope decodes.
   If a later profile revision declares active authorization tuples, the
   closed empty profile becomes a versioned change. The selfdestruct rules
   assume the post-Cancun EIP-6780 semantics the pinned source implements.
3. **`SprintEnd` naming.** `protocol/block` defines `SprintEnd` as the
   boundary where Bor commits span and StateSync work; the native condition
   is `IsSprintStart(n, s) = n % s == 0`. This module follows the schema
   field and the pinned native condition; the naming tension belongs to the
   schema owners.
4. **Fee-credit order.** The summary lists recipient credits in observed
   native order (tip then burn in the fixtures). The validator checks the
   aggregate, not the order; if a profile pins a canonical order, it should
   be stated in the schema.
5. **SYSTEM_PRE/SYSTEM_POST observation.** The retained fixtures only ever
   produced `TX_STATE_SYNC` logs. The PRE/POST scope values exist and are
   validated, but no retained native observation attributes a log to span
   updates or code upgrades around the commit yet; POLY-L05 would be the
   fixture to add.
