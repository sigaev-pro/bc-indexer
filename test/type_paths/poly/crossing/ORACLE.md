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

The wrapper reuses the pinned accepted driver's exclusive output ownership,
bounded command runner, signal handling and cleanup. Every rendered helper and
its original pinned source are retained in preparation/run evidence. No shared
adapter or upstream production behavior changes.
