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
