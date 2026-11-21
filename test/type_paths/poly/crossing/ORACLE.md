# Private ordinary-type activation crossings

Partial E01-05 follow-on, dependent on the frozen preactivation producer
`e4a559069426e69f13478623ae69387b252c1323`. Native execution and independent
acceptance are pending. This directory does not change that producer.

The first three private profiles isolate Ethereum signer gates: Berlin, London
or Prague at block 2,
with earlier Ethereum forks at 0 and later forks at 8. Bor prefixes match the
preactivation fixtures; all other native configuration remains source-derived.
The pinned native generator applies a signed legacy nonce-0 transfer at block
1 and the selected type 1, 2 or 4 nonce-1 transaction at block 2. Its existing
London gas-limit doubling, base-fee calculation, pre-transaction system calls,
native execution, finalization and seal are preserved.

Two additional private profiles schedule London with Jaipur at 2, and Prague
with Bhilai at 2, following the pinned source's paired mainnet schedules while
retaining private allocations. Jaipur changes native SealHash; the generator
and candidate signer use the corresponding full configuration. Bhilai's fee
denominator reads the parent height, so block 2 still uses Delhi's denominator
16; this two-block fixture does not claim the block-3 denominator64 transition.
Neither the isolated gates nor these paired private schedules establish public
fork bundles, contract upgrades or complete historical coverage.

A fresh importer first attempts the selected type with nonce 0 at block 1,
using the valid generated block-1 header with only transaction root and seal
changed. Require the exact native unsupported-type error and unchanged genesis
head. This is a refusal fixture, not a claim that its execution commitments
would be valid if the type gate were bypassed. The same importer then imports
the unmodified native-generated legacy block 1 and typed block 2. Retain raw
blocks, receipt and account proofs, actual decisions, admission frontiers and
observer identities at each step. Snapshots must not alias later mutable maps.

Expected final decision frontier is 2 and admission frontier is 3: one rejected
attempt followed by two successful imports. Strict proof of these observations
requires a separate judge and negative controls; the producer returns diagnostic
results only. Type 127, public profiles and parent-gate acceptance are excluded.

## Madhugiri body-StateSync carrier crossings

Two further profiles extend the same matrix to the reserved body StateSync
transaction. Both activate every Ethereum signer gate from genesis (prefix 5)
and move the sprint to `{"0": 2}` through a new checked seam, so block 2 is
the first sprint start and the Heimdall stub's two-event page (id 1 to the
TARGET contract, id 2 to the reverting recipient, both carrying
`extractor-corpus-state-sync-1`, L1 hashes `0x..01`/`0x..02`) must commit at
block 2. Block 1 stays the plain legacy nonce-0 transfer and is not a sprint
start; the cutoff passes with a one-second margin (block-2 time 1558348307
after record_time 1558348306).

`PreMadhugiri` keeps `madhugiriBlock` at 8, so block 2's body is only the
legacy nonce-1 transfer and the two events commit off-body: a separate Bor
receipt (the pre-Madhugiri `writeBorStateSyncLogs` path) plus header-chain
state-sync records, with the receiver slot reaching 2 either way. It uses Bor
prefix 6, not the 7 of the design's case tuple: the design's own parenthetical
("jaipur..rio at 0; madhugiri..uburu at 8") requires six pre-Madhugiri forks,
and prefix 7 would activate Madhugiri at genesis, destroying the carrier
control. This row carries no pre-gate candidate and no post-gate controls.

`Madhugiri` schedules `madhugiriBlock` at 2 (`madhugiriProBlock` stays 8; the
fork order stays valid) and keeps the ordinary legacy nonce-1 transfer as the
only authored body transaction: native `FinalizeAndAssemble` appends the
events-derived 0x7f itself, so it occupies the final position among multiple
transactions and the 127 receipt joins the body receipt trie with
transactionIndex 1. Its `before_activation` is the observation-only candidate
of the accepted preactivation proof: a hand-built block 1 whose only body
transaction is the events-derived 0x7f. Block 1 is not a sprint start, so
native Finalize returns before any body requirement and the processor skips
the reserved envelope; the candidate header is rebuilt with the pinned
skip-execution commitments (no gas, no bloom, the empty receipt root and the
unchanged parent state root — the Prague parent-hash write is a no-op in this
genesis, as the accepted activation run showed). It is recorded with
classification `native-preMadhugiri-observation-no-refusal-credit` and
`accepted: true`; no refusal credit is claimed, and the type-refusal
requirement is guarded off for kind 127 as in `activationCase`. The generated
block 1 then lands beside the accepted candidate and block 2 reorgs the
canonical head onto the generated chain.

Views of both new cases extend the pinned activation view with four
StateSync-specific observations: the live `OnTxStart`/`OnTxEnd` rows and
`OnEnter`/`OnExit` call frames (reset per block; post-gate block 2 shows the
synthetic system-address frame wrapping the two receiver commits),
`bor_receipt` presence and logs through `rawdb.ReadBorReceipt` (present with
the single TARGET log before the gate, absent after it), and the
`HeaderChain().GetStateSync()` rows, which hold both events in either case
because `SetStateSync` runs before the gate branch.

Post-gate rejection controls stay in two separate result sections because
their refusals live at different layers. `components.unknown_block_decode`
sweeps envelope tag bytes 5..126 over the crossing block's 0x7f transaction
(122 rows; the preceding legacy transaction is kept) and every one fails
native block decoding with heads unchanged — type 127 itself decodes, so it
must not appear there. `import_refusals` holds the three body controls a
fresh importer seeded with block 1 must refuse with `ErrStateSyncMismatch`
while its head stays pinned at the block-1 hash: `missing` (0x7f dropped),
`non-final` (0x7f first, legacy last) and `mismatched-l1-hash` (rebuilt
carrier with `items[0].TxHash` changed to `0x..99`). The mismatch surfaces
during Finalize after admission, so decisions stay at one while the admission
frontier advances to 2, mirroring the accepted state-sync refusal pattern.

These two rows are diagnostic until the paired judge extension and the
planned mutation audit cover them; the native run must still confirm the
sprint-2 timing, the skip-execution commitments of the observation candidate
and the record counts around the accepted-candidate reorg.

The wrapper reuses the pinned accepted driver's exclusive output ownership,
bounded command runner, signal handling and cleanup. Every rendered helper and
its original pinned source are retained in preparation/run evidence. No shared
adapter or upstream production behavior changes.

The separate judge starts from source-derived initial watched accounts, then
requires the legacy transfer's absolute state and the typed transfer's exact
state delta. Initial contract code hashes come from pinned Bor
`tests/bor/testdata/genesis.json` (SHA-256
`aff8d66c9e67bfdacad48372c4f26d1bcb58207fa6fcae09dc28bc5c1c83e483`)
and the accepted corpus additions in `test/native_corpus/expected/poly.json`.
All four watched storage slots begin at zero. The authority begins absent;
type 4 creates its delegation code and increments its nonce to 1. Its intrinsic
gas is 21000 + 25000 with no existing-account authorization refund. Source
`core/state_transition.go` and `core/bor_fee_log.go` define gas purchase,
effective price, refund, value-transfer and deprecated fee-transfer log
balances. The fee event describes tip movement, not final sender balance.

The helper provenance file identifies narrow source copies from preactivation
judge checkpoint `6400125`; that checkpoint is a dependency, not an acceptance
claim. The accepted durable-record checker is adapted only to distinguish
successful decision sequences 0/1 from admission observation sequences 1/2
after the first refused admission. Native replay and controls remain pending.

## D108 offline continuation

The retained three-case native observation at producer `b3b51d8` is embedded
as a digest-bound test vector. This proves only the isolated private Ethereum
gate cases. Paired London/Jaipur and Prague/Bhilai producer `37e59cb` has a
successful retained preparation; its runtime remains pending. Neither result
establishes a full historical or public fork profile. Type 127 remains excluded
from this ordinary-type judge.

The type-4 no-refund premise now requires an authenticated absent authority
account at the first block, not merely zero nonce/balance and empty code. A
valid proof of an existing empty account is a separate rejection control.
Transition-state checks cover every watched account's nonce, balance, code
and storage. Receipt controls include a valid inclusion proof in a larger
receipt trie, which must fail the separate complete-root check. Correlated
decision controls recompute digests so semantic identity checks are exercised.

All failed mutation attempts remain retained. The table explains nine
candidate equivalent guard deletions, including their mandatory downstream
checks and cryptographic assumptions where relevant. Independent review and a
fresh complete mutation audit are required before those explanations receive
acceptance. Portable extracted Go controls pass all 14 mutants; their explicit
native type doubles do not provide signing, execution or import evidence.

## Paired Bor-schedule matrix

The judge now accepts both retained observation shapes. The frozen unpaired
three-case vector above remains valid unchanged, and the paired producer at
`37e59cb` is judged as a five-case matrix: its retained unpaired three plus
London with Jaipur at 2 and Prague with Bhilai at 2. Dispatch is by case count;
a report of any other length fails against whichever matrix is closer. Every
paired-report row carries `bor_activation`: an empty string for the retained
unpaired rows, `jaipurBlock` or `bhilaiBlock` for the paired rows. Unpaired
rows must not carry the field, so the frozen vector cannot be relabeled as a
paired observation.

The paired Bor gate sits inside the nested `bor` configuration and activates
at the crossing block itself, ahead of its historical prefix position;
`historical_config` now expects that gate at 2 for the paired rows while the
unpaired expectations (London Jaipur 8, Prague Bhilai 8) stay binding for the
original three. Gate values, presence, count, order and both Ethereum gates
are rejected when changed. Bhilai's fee denominator reads the parent height,
so block 2 still uses Delhi's denominator 16; the unchanged base-fee oracle
keeps that expectation for PragueBhilai, and this fixture still does not claim
the block-3 denominator-64 transition. Jaipur changes the native SealHash and
the producer signs with the full configuration, but the offline judge does not
authenticate block seals, so the pairing is bound through the configuration
gate rather than the seal bytes.

The paired producer's runtime is pending, so the offline paired controls pair
the retained unpaired payloads with paired identity and configuration only;
its retained preparation does not make those blocks paired evidence. A genuine
paired native observation must still be judged as a whole when retained. Nine
new single-check mutants cover the paired dispatch, matrix, `bor_activation`
and gate bindings; all existing checks and mutants are retained unchanged,
and the complete audit remains required for acceptance.

## Madhugiri body-StateSync carrier crossings

The judge accepts the paired five-case matrix extended to seven rows
(producer `e7634d1`): `PreMadhugiri` (kind 0, Bor prefix 6 — six forks precede
Madhugiri, so `madhugiriBlock` stays scheduled at 8) and `Madhugiri`
(`madhugiriBlock` gated to 2 over Bor prefix 7, where the gate override
replaces the prefix position 0). Both rows activate every Ethereum signer
gate from genesis (prefix 5) and move the sprint to `{"0": 2}` through the
checked seam, so `historical_config` now takes a sprint parameter that the
frozen rows still default to 4. Dispatch stays by case count, with the
madhugiri rows routed to `madhugiri_case` before the ordinary paired path.

The gated row is judged end to end. The block-2 body must be the signed
legacy nonce-1 transfer followed by the native `0x7f` envelope in the final
position, and the judge re-encodes `[]*StateSyncData` independently:
sequential ids, the TARGET/REVERT contracts in event order, the corpus data
and the exact L1 hashes `0x..01`/`0x..02`. The second receipt must be the
appended type-127 receipt (status 1, gasUsed 0, cumulative equal to the
header gas, effectiveGasPrice 0, only the successful item's TARGET log,
recomputed bloom, typed wire `b"\x7f" + rlp(...)` at transactionIndex 1,
complete receipt root). The live rows must be the legacy row then the
system-sender row (`0xff..fe`) bracketing the state-sync effects, and the
five call frames must reproduce the synthetic root wrapper and both receiver
commits: the outer `19494a17` input wraps the six-field clerk event record
with the stub's record time, the inner `26c53bea` input carries the event id
and data, the receiver slot advances only at the outer calls, the reverting
item retains its before/after state, and the successful item sets the TARGET
storage quadruple.

The carrier difference is judged across both rows: post-gate
`bor_receipt.present` must be false, while the pre-gate block 2 carries a
single legacy transaction, one body receipt and a separate Bor receipt
holding the single TARGET log; both rows' `state_sync` records must equal the
two events (SetStateSync runs before the gate branch), both final states must
be identical (same transition, different carrier) and the state root must
advance. The pre-gate row must claim no gated controls: no
`before_activation`, no `import_refusals`, no `components`.

The gated row's `before_activation` is the observation-only candidate of the
accepted preactivation proof: a hand-built block 1 whose only body
transaction is the events-derived `0x7f`, accepted with the classification
label and no refusal credit. Its skip-execution commitments are bound
directly (unchanged genesis state root, zero gas, empty bloom, empty receipt
root); its executed-state fields are not required, mirroring the coordinator
note that the never-executed candidate cannot carry durable state claims.
The durable reorg records follow the pinned decider source: the candidate
imports as canonical block 1 (IMPORT), the generated block 1 lands beside it
as a no-event SIDE write, and block 2 reorgs through a CANONICAL transition
whose member list carries both branch blocks, with three observations
(blocks 1, 1, 2) and admission frontier 3. The pre-gate row's plain records
are two parent-derived IMPORT decisions at admission frontier 2.

The post-gate rejection controls are bound in their two separate sections.
`import_refusals` holds the three bodies a fresh importer seeded with block 1
must refuse with the native StateSync mismatch: `missing` (the dropped
carrier), `non-final` (the carrier first, the legacy transfer last) and
`mismatched-l1-hash` (the carrier rebuilt with the first item's L1 hash
changed to `0x..99`); the exact native error strings are pinned from
bor.go:1321/1330, both hash-mismatch digests are re-derived by the judge, the
heads stay pinned at the block-1 hash, and each refusal keeps one decision
with admission frontier 2 (the mismatch surfaces in Finalize after
admission). `components.unknown_block_decode` sweeps envelope tags 5..126
over the crossing block's `0x7f` transaction (122 rows, the preceding legacy
transaction kept) and requires every row to fail native block decoding with
heads unchanged at the crossing head; type 127 itself decodes, which is why
the two refusals stay in separate sections.

The verdict flips `reserved127` from `NOT_EVALUATED` to
`{"madhugiri_crossing": true, "pre_madhugiri_separate_receipt": true}` with
`public_profile_credit` false. Offline controls build a synthetic
seven-case report over the retained Prague body with a test-only hexary trie
builder (account and storage proofs, including absence proofs) and pass it
plus every negative control; the five-case paired matrix and the frozen
unpaired three keep passing unchanged, and the existing 149 mutant rows are
retained untouched; 183 new single-check rows cover the carrier bindings,
with only subsumed conjuncts (implied by a retained equality or a stronger
neighbor check) left out of the table.

### Pending the native run

The block-1/2 base-fee pair of the sprint-2 Delhi-denominator profiles is
not guessed: the native base-fee pair is pinned (984375000, 969058741), so the transition is judged
only for internal consistency (every receipt is re-derived from the actual
header values) until the coordinator derives the two integers from the
native run. Two further bindings wait on the same run: the pre-gate block-2
header bloom (the separate Bor receipt log may or may not fold into it) and
the reorg record counts above, which follow the pinned decider source but
must be confirmed around the accepted-candidate reorg. The synthetic
fixtures pin the shapes the producer at `e7634d1` renders; any divergence
the native run shows is a judge repair, not a producer change.
