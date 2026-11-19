# Private ordinary-type activation crossings

Partial E01-05 follow-on, dependent on the frozen preactivation producer
`e4a559069426e69f13478623ae69387b252c1323`. Native execution and independent
acceptance are pending. This directory does not change that producer.

Each separate private profile schedules Berlin, London or Prague at block 2,
with earlier Ethereum forks at 0 and later forks at 8. Bor prefixes match the
preactivation fixtures; all other native configuration remains source-derived.
The pinned native generator applies a signed legacy nonce-0 transfer at block
1 and the selected type 1, 2 or 4 nonce-1 transaction at block 2. Its existing
London gas-limit doubling, base-fee calculation, pre-transaction system calls,
native execution, finalization and seal are preserved.

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
