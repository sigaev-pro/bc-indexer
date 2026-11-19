# Private Polygon preactivation checks

This follow-on to the accepted current-fork proof is partial E01-05 evidence.
It does not establish public profiles or E06/E07 coverage. Native execution
and independent acceptance remain pending.

Pinned Bor is 3a018e38467594508ca31ebeaf804837fb92834a. Four historical private
profiles retain source genesis/contracts and chain ID15001. A chronological
prefix of scheduled forks is active at0, the remainder at8; Hampi stays nil,
Osaka absent, and no BlobSchedule is added. Only blocks1–2 are measured.

Each profile must first generate and import a valid signed legacy transfer at1
and an empty block at2. A separate same-genesis import seeds block1 and tries a
correctly signed, funded type1/2/4 transaction before its native signer gate
(Berlin/London/Prague). Require the actual native type refusal, unchanged head,
one durable import decision and two closed admission attempts.

Body127 is a diagnostic, not a presumed refusal: pinned state_processor.go
skips StateSync unconditionally, while Bor Finalize and receipt-count equality
apply body-StateSync requirements only after Madhugiri. A coherent preMadhugiri
candidate may therefore be accepted without an ordinary receipt. Preserve the
actual result, raw headers/body/receipts/state and durable frontiers. Do not
change native validation or turn observed acceptance into refusal credit.

The wrapper pins and reuses accepted ownership/deadlines/cleanup. Generated Go
support changes only entrypoint/transaction-helper names and corpusBlocks=2;
the new main supplies legacy control transactions and historical inputs.
