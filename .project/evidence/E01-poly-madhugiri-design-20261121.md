# Madhugiri/MadhugiriPro body-StateSync crossing fixture — design (2026-11-21)

Read-only design produced for the coordinator by a background analyst
under the owner's continue-and-parallelize direction. Nothing was
modified. The design implements the Madhugiri obligation of the
applicability matrix (E01-applicability-matrix-20261120.md) as a
follow-up beyond draft PR #194; sequencing: paired 5-case judge
extension first, then cases 6-7 of this design.

---

I have everything needed. Here is the design.

---

# Madhugiri/MadhugiriPro body-StateSync crossing fixture — design (E01, PR #194 follow-up)

Prerequisites and anchors: producer worktree `.worktrees/type-paths-poly-crossing` (clean at `37e59cb`), judge worktree `.worktrees/type-paths-poly-crossing-judge` (at `694a22c`), retained prep `d107-poly-crossing-preparation-02-37e59cb` (Bor source under `bor/source`, commit `3a018e38`), and the accepted post-Madhugiri proof `aae2fac` (judge: `test/type_paths/poly/poly_judge.py`). Sequencing per the readiness plan: audit-06 and the paired 5-case judge extension land first; this fixture adds cases 6–7 to that extended matrix.

## 1. Fixture case matrix

Two new rows in `crossingMain`'s table (`.worktrees/type-paths-poly-crossing/test/type_paths/poly/crossing/probe.go`), both with `ethPrefix=5` (berlin..prague at 0), `borPrefix=7` (jaipur..rio at 0; madhugiri..uburu at 8), and a new sprint override to `{"0": 2}`:

| Case | `gate`/`borGate` | `kind` | Block 1 (pre-gate) | Block 2 (crossing, sprint start) |
|---|---|---|---|---|
| `PreMadhugiri` | none (`madhugiriBlock`=8) | 0/127-obs | legacy nonce-0 transfer; not a sprint start (1%2≠0), no sync | body = **[legacy nonce-1 transfer only]**; two events committed off-body; **separate Bor receipt**; `GetStateSync` records both events; receiver slot0 = 2 |
| `Madhugiri` | `madhugiriBlock`=2 (`madhugiriProBlock` stays 8; fork order valid) | 127 | same block 1 | body = **[legacy nonce-1, 0x7f StateSync appended natively by `FinalizeAndAssemble`]** — the 0x7f occupies the **final** position; 127 receipt in the body receipt trie; no separate Bor receipt |

Why sprint 2 is required and sufficient: `IsSprintStart(n, s) = n % s == 0` (bor.go:2405); the prepared genesis pins `sprint {"0": 4}` (verified in prep-02 `bor/inputs/genesis.json`), which never reaches a sprint start in the 2-block chain (`corpusBlocks = 2` via the activation seam). The single seam edit `engine["sprint"] = {"0": 2}` mutates both `genesis.json` and `chain-config.json` marshals (they share the `config` map). Event cutoff passes with 1 s margin: delay 0 → `to` = block-2 time 1558348307 > record_time 1558348306 (strictly `Before`). `madhugiri=2 < madhugiriPro=8` passes `CheckConfigForkOrder`; Rio is active so `checkAndCommitSpan` is skipped at the sprint start; the genesis has no `bor.blockAlloc`, so no contract upgrade lands inside the crossing block.

Negative (pre-gate) control, two layers:
- **Carrier control (required):** the `PreMadhugiri` case's native-generated block 2 proves pre-gate StateSync surfaces as separate receipts, not a body transaction: body has no 0x7f, `rawdb.ReadBorReceipt` returns a non-nil receipt (status 1, the item-1 TARGET log), written by the pre-Madhugiri `blockLogs`-beyond-tx-logs path (blockchain.go:5130–5155); the 127 receipt is absent from the trie.
- **Observation-only pre-gate candidate (mirror of the accepted preactivation proof):** in the `Madhugiri` case, `before_activation` inserts a hand-built block-1 candidate whose only body tx is the events-derived 127. Pre-gate `Finalize` returns before any body requirement (bor.go:1318–1321) and the processor skips it, so it is accepted without execution — record `classification: "native-preMadhugiri-observation-no-refusal-credit"`, `accepted: true`, state root unchanged, exactly as `activation_judge.py:185–196` judged it. The existing hard requirement `errors.Is(importErr, core.ErrTxTypeNotSupported)` in `crossingCase` must be guarded off for `kind == 127` (same guard shape as `activationCase`).

## 2. Constructing the type-127 body transaction from the stub

Input (already in place, no producer change): prep-02 `bor/bodies/clerk-corpus-from-1.json` carries the two-event page rewritten by the accepted E01-05 prepare (`poly_native.py:116–122`) — id 1 → contract `0x3000…0003` (TARGET, success), id 2 → contract `0x3000…0002` (REVERT, the reverting recipient), both data `extractor-corpus-state-sync-1`, sequential IDs from `lastStateId` 0, L1 hashes `0x…01`/`0x…02`, `log_index` 1/2, `bor_chain_id` "15001", `record_time` 1558348306. The stub serves `/clerk/time?from_id=1` from this file (`test/harness/fixtures/source_inputs_bor/heimdallstub/stub.go`).

Native encoding path (all pinned source `3a018e38`; nothing is reimplemented):
1. Sprint start at block 2 → `Bor.Finalize`/`finalizeAndAssemble` calls `CommitStates` (bor.go:2032): `IsIndore` branch reads `GenesisContractsClient.LastStateId(number-1, parentHash)` = 0, sets `to = header.Time − CalculateStateSyncDelay(2)` = 1558348307, fetches the page.
2. `commitStateSyncPage` (bor.go:2195) per record: `validateEventRecord` (sequential `lastStateID+1 == ID`, chain ID `"15001"`, `Time.Before(to)`), `admitStateSync` (Valencia/Austin budgets inactive — events are tiny), appends `&types.StateSyncData{ID, Contract, Data, TxHash}` (first four `clerk.EventRecord` fields), invokes the receiver via `GenesisContractsClient.CommitState`, advances `lastStateID`.
3. Body tx: `types.NewTx(&types.StateSyncTx{StateSyncData})` (`core/types/tx_state_sync.go`, `StateSyncTxType = 0x7f`). Generation appends it at bor.go:1532; import rebuilds it at bor.go:1325 and requires hash equality with the final body tx (`core.ErrStateSyncMismatch` otherwise), then appends the 127 receipt (`insertStateSyncTransactionAndCalculateReceipt`: type 127, status 1, gasUsed 0, cumulative = last ordinary receipt, logs = state-sync logs, txIndex = len(body)−1).
4. Receiver progress observable: RECEIVER `0x…1001` storage slot 0 goes 0 → 1 → 2 — visible in the per-call traced snapshots (`outer_before[RECEIVER].storage[0] == i−1`, `outer_after == i`, stable during the inner call), in the authenticated storage proofs, and optionally via `engine.GenesisContractsClient.LastStateId` per block (corpus precedent). Keep the ordinary legacy tx in block 2 before the 0x7f: it proves "final position among multiple transactions" and gives the 127 receipt `transactionIndex` 1 and non-empty cumulative gas.

## 3. Judge obligations (crossing_judge.py case structure)

Add `("PreMadhugiri", "", 0, 5, 7)` and `("Madhugiri", "madhugiriBlock", 127, 5, 7)` to the extended CASES; new function `madhugiri_case(row)` dispatched on `row["type"] == 127`. Producer case dict (fields the judge consumes):

```
{name, type: 127, activation_block: 2, bor_activation: "madhugiriBlock" | "",
 config, genesis_raw,
 before_activation: {head_before, accepted: true, classification, block: view(stored=false),
                     head_hash, admission, decisions, observed, keys, observer_errors},
 crossing: {blocks: [view1, view2], head_hash, admission, decisions, observed, keys, observer_errors},
 import_refusals: [{case, raw_block, number, hash, root, parent, transaction_proofs,
                    error, head_before, head_after, decisions, admission} × 3],
 components: {unknown_block_decode: [{type, error} × 122], head_before, head_after}}
```

Each `view` = existing `activationView` fields plus four new ones: `transactions` (live `OnTxStart/OnTxEnd` rows, reset per block), `calls` (live `OnEnter/OnExit` frames), `bor_receipt` (`rawdb.ReadBorReceipt` presence + logs), `state_sync` (`HeaderChain().GetStateSync()` rows). `crossing_helpers.historical_config` needs a `sprint` parameter (currently hardcodes `{"0": 4}` in the equality check) — a checked seam, so its tests change too.

Checks (port from `poly_judge.system_state`/`refusal_claims`, adapted to the crossing block 2):
- Config/identity: `configuration_numbers`; `bor.madhugiriBlock == 2|8`; `bor.sprint == {"0": 2}`; full equality vs `historical_config(5, 7, sprint=2)`; genesis width 20 (prague at 0), block headers width 16; base-fee pair pinned by formula (Delhi denominator 16; london active from genesis — derive from the native run before hardcoding, as the activation judge did).
- Array encoding: `txs[1][0] == 0x7f` and `plain_rlp(rlp_list(txs[1][1:])) == [[b"\x01", TARGET, DATA, 0x..01], [b"\x02", REVERT, DATA, 0x..02]]` — judge-side re-encoding of `[]*StateSyncData` (four fields each, sequential IDs, distinct contracts, exact L1 hashes). `h.signed_input(txs[0], 0, 1)` for the preceding legacy tx.
- Receipts: two receipts; receipt[0] = existing `receipt_oracle(0, base, …)`; receipt[1]: type 127, status 1, gasUsed 0, `cumulativeGasUsed == header gas == 21000`, effectiveGasPrice 0, logs `[[TARGET, [topic 0x…5e], 0x..01]]` (only the successful item; the revert emits nothing), bloom recomputed, wire `b"\x7f" + rlp([status, cumulative, bloom, logs])`, transactionIndex 1, complete receipt root == header[5] + inclusion proofs.
- Per-item state and call identity (post-gate block 2): one live tx row `from == SYSTEM (0xff…fe)` with `before == block-1 end`, `after == block-2 end`; five calls at depths [0,1,2,1,2]; `outer_i` input `== abi(19494a17, 1558348306, rlp([id, contract, DATA, l1hash, logidx, b"15001"]))`, `inner_i` input `== abi(26c53bea, id, DATA)`; receiver slot0 i−1→i at outer, stable at inner; inner1 sets TARGET storage `(1, RECEIVER, len(DATA), padded DATA)`; inner2 reverts with `before == after`; `outer_after == inner_after`; final outer `after` equals the authenticated end state. Absolute final state: SENDER nonce 2, RECEIVER slot0 = 2, unrelated accounts unchanged, `h1[3] != h2[3]`.
- Carrier difference: post-gate `bor_receipt.present == false`; pre-gate case block 2: `len(txs) == 1`, one receipt, `bor_receipt.present == true` with the single TARGET log, receiver/TARGET effects identical (optional sharp check: both cases' block-2 state roots are equal — same state transition, different carrier).
- `state_sync` rows equal the two events in both cases (`SetStateSync` happens regardless of gate); durable records via existing `records()`/`check_records` (Madhugiri positive: frontier 3 like the other crossings; PreMadhugiri: 2).
- Verdict: on PASS flip `"reserved127"` from `NOT_EVALUATED` to e.g. `{"madhugiri_crossing": true, "pre_madhugiri_separate_receipt": true}` with `public_profile_credit: false`.

## 4. Rejection controls

Three post-gate importer refusals (fresh importer seeded with block 1; candidate rebuilt from the generated block-2 header, `TxHash`/`GasUsed` recomputed, resealed with the tracked test signer — port of `rejectedImports` in `test/type_paths/poly/probe.go:322`, minus blob/reordered/duplicate):
- `missing`: body `[legacy]` (0x7f dropped) → `state-sync mismatch: block body missing state-sync transaction, heimdall reported 2 event(s)`.
- `non-final`: body `[0x7f, legacy]` → last tx not 127 → same missing-final mismatch.
- `mismatched-l1-hash`: body `[legacy, rebuilt-127 with items[0].TxHash = 0x..99]` → `state-sync mismatch: hash mismatch, got X want Y`; the judge re-derives both hashes independently.

Each row: heads pinned at the block-1 hash, `check_records(decisions, admission, [block1], frontier=2)` — decisions stay at 1, admission advances to 2 (mismatch is discovered in `Finalize` after admission, mirroring E01-05's frontier pattern).

Separate decode refusal (mirrors the accepted pattern): `components.unknown_block_decode` — sweep envelope tag bytes 5..126 over the crossing block's 0x7f transaction; every one fails `rlp` block decoding with "transaction type not supported", heads unchanged. Type 127 itself decodes (it is a known type) — its refusal exists only at the import layer, which is exactly why the two reports must stay in separate result sections (`components` vs `import_refusals`), as in E01-05's `component_claims`/`refusal_claims` split. The pre-gate acceptance observation (`before_activation`) carries the explicit `classification` label so no refusal credit is claimed there — the preactivation proof's convention.

## 5. Effort estimate

- **Producer edits (4 files, ~12–15 focused edits):** `probe.go` — case table +2 rows, `crossingKind` 127-bypass, `crossingTransactions` legacy-only block 2 for kind 127, `crossingInputs` sprint seam, `crossingCase` 127 branch (no type-refusal requirement, events-derived pre-gate candidate, view extensions: live rows/calls, `bor_receipt`, `state_sync`, `LastStateId`), new 3-case rejection harness + decode sweep; `crossing_support.py` — 2–3 new seams + pin update; `test_crossing.py` — ~3 rendering-test updates; `ORACLE.md` — one section.
- **Judge edits (~8 files):** `crossing_helpers.py` (sprint parameter + 4 ported event/ABI helpers), `crossing_judge.py` (`madhugiri_case`, ~150–200 lines, verdict flip), 5 test files, `crossing.mutants.json`, `ORACLE.md`, plus a digest-bound native vector embed after the run.
- **New offline tests:** ~10–15 methods (encoding vectors for the event RLP/ABI selectors, report fixtures for all new row kinds, refusal/decode-sweep fakes).
- **New mutant rows:** ~40–60 (single-check convention; current file has 140 rows for a comparable check population).
- **Native iterations:** one new preparation (install/vet/build ≈ 15–20 min, serialized slot) + 2–3 run cycles — expect one repair iteration (likely suspects: sprint-2 timing/base-fee pinning, receipt/bloom bytes). The 5-case paired run already pending can carry these cases in the same binary only if the producer lands before it; otherwise one extra run.
- **Audits:** one additional mutation audit after this judge extension (beyond the already-planned audit-06 and paired-judge audit), with 1–2 strengthen-and-rerun cycles. Total: roughly one producer iteration + 3–4 judge/audit iterations.