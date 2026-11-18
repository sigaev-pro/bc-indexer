# Polygon native type paths: fixture contract

Status: design and implementation in progress; no native acceptance evidence.

The source is Bor `3a018e38467594508ca31ebeaf804837fb92834a`, archive
SHA-256 `a313bc56a136b054d44ef0a319bcbc0fcf0eddba046035ed2b89b83c83847e38`.
The accepted E01-03 corpus provides the native genesis, deterministic signer,
Heimdall input shape and admission/decision bridge, not current-fork coverage.

## Fork profile

`params/config.go: BorMainnetChainConfig` schedules Berlin, London, Shanghai,
Cancun and Prague. Its Bor fork sequence is Jaipur, Delhi, Indore, Ahmedabad,
Bhilai, Rio, Madhugiri, MadhugiriPro, Dandeli, Lisovo, LisovoPro, Giugliano,
Chicago, Valencia, Austin and Uburu. Hampi is explicitly nil (unscheduled);
Osaka is not scheduled. `MergedTestChainConfig` is therefore unsuitable.

The private fixture retains chain ID 15001 and the source test genesis contracts.
It activates only the above scheduled rules at genesis, retains an explicit
four-block sprint, and sets StateSync confirmation delay to zero. Mainnet
activation heights are provenance, not claims about the live network. Private
activation is not a public-profile test. Native configuration validation and
native header/import checks must accept the resulting profile before credit.
Contract upgrades must not occur inside the measured StateSync block; the
source genesis receiver's compatibility with active rules remains to be proved.

## Bounded proof obligations

1. Native import of successful and reverting ordinary transactions of applicable
   types, with independently specified state/log/status expectations. Types 0,
   1, 2 and 4 are candidates. Type 3 requires actual native applicability proof:
   generic decoding and the Cancun flag alone are insufficient.
2. Two independent StateSync items at a sprint boundary, including a reverting
   recipient where the native receiver permits it. Check each item's actual
   before/after target state and call identity, receiver progress, body type
   `0x7f`, receipt and final state root. Do not equate a caught child revert with
   a failed outer receipt.
3. Native rejection controls for missing, non-final and mismatched StateSync
   body transactions; activation controls where the native path supplies a
   gate. Unknown-type decode refusal is reported separately from import refusal.
4. Authenticate raw RLP block headers, canonical links and native final head;
   bind receipts, state snapshots and actual adapter observation/decision
   records to those identities. Compose observation hooks with accepted capture
   hooks instead of replacing them.
5. Preserve portable corruption controls, single-check mutants, native errors,
   exact executed source, bounded runtime and owned cleanup records.

## Source-derived StateSync behavior

`core/state_processor.go` skips body StateSync in the ordinary transaction loop
and brackets native finalization with transaction hooks. `consensus/bor/bor.go`
`Finalize` requires the final body transaction to match the StateSync transaction
constructed from Heimdall events after Madhugiri. `FinalizeAndAssemble` appends
that body transaction and its receipt. These are native operations, not fixture
reimplementations.

`commitStateSyncPage` requires sequential event IDs, the configured chain ID and
event time before the selected cutoff. It appends each event's ID, contract,
data and L1 transaction hash, invokes the receiver and advances the last ID.
Valencia and Austin impose byte/gas budgets; keep fixture events well below
both. Receiver logs are required for receipt construction. The item L1 hash
must not be substituted for the Polygon inclusion transaction hash.

E01-03 established block-end system state only (D103). This block must not claim
per-action state coverage until real native observations substantiate it.
Full E04 call semantics, E05 feed reconciliation and public-profile coverage
remain outside this bounded proof.
