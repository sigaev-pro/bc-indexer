# Private Polygon preactivation checks

This follow-on to the accepted current-fork proof is partial E01-05 evidence.
It does not establish public profiles or E06/E07 coverage. Native diagnostic
execution completed; the strict judge and controls require independent review.

Pinned Bor is 3a018e38467594508ca31ebeaf804837fb92834a. Four historical private
profiles retain source genesis/contracts and chain ID15001. A chronological
prefix of scheduled forks is active at0, the remainder at8; Hampi stays nil,
Osaka absent, and no BlobSchedule is added. Only blocks1–2 are measured.

Each profile must first generate and import a valid signed legacy transfer at1
and an empty block at2. A separate same-genesis import seeds block1 and tries a
correctly signed, funded type1/2/4 transaction before its native signer gate
(Berlin/London/Prague). Require the actual native type refusal, unchanged head,
one durable import decision and two closed admission attempts.
Candidates retain the empty control's execution commitments to reach the
earlier native signer gate. They are not claimed execution-valid after the
fork, and these are preactivation refusals, not a paired boundary crossing.

Body127 is a diagnostic, not a presumed refusal: pinned state_processor.go
skips StateSync unconditionally, while Bor Finalize and receipt-count equality
apply body-StateSync requirements only after Madhugiri. A coherent preMadhugiri
candidate may therefore be accepted without an ordinary receipt. Preserve the
actual result, raw headers/body/receipts/state and durable frontiers. Do not
change native validation or turn observed acceptance into refusal credit.

The wrapper pins and reuses accepted ownership/deadlines/cleanup. Generated Go
support changes only entrypoint/transaction-helper names and corpusBlocks=2;
the new main supplies legacy control transactions and historical inputs.

## Source-derived historical details

The profiles are preBerlin (Ethereum prefix0/Bor prefix0), preLondon (1/0),
prePrague (4/4, through Cancun/Ahmedabad), and preMadhugiri (5/6, through
Prague/Rio). Future forks remain at8. Native `MakeSigner` and
`TransactionToMessage` produce the exact candidate-hash-bound error:
`could not apply tx 0 [<transaction hash>]: transaction type not supported`.
The capture observer retains that same error; it is not a successful capture.

The valid legacy control spends21000 gas at5gwei and transfers7wei from public
fixture key1 (initial10ETH) to the empty recipient. Bor emits two native logs:
the value transfer after upfront gas purchase, followed by the deprecated
fee-transfer event. `core/bor_fee_log.go`, `core/evm.go` and
`core/state_transition.go` define the signatures, topic order and five amount
words. The fee event describes the tip against initial sender/span-signer
balances (10ETH/1000ETH); it does not describe final post-transaction balances.
The judge derives these amounts and bloom independently, including basefee
burn. `params/protocol_params.go` and `consensus/misc/eip1559/eip1559.go` select
denominator16 after Delhi or64 after Bhilai; Dandeli/Lisovo are still future.

Pinned `core/genesis.go` constructs a Shanghai/Cancun genesis with20 header
fields, empty withdrawals and zero blob/beacon fields. Ordinary measured Bor
headers have15 fields before London or16 after London. These are distinct
native formats; no blob schedule or blob execution support is inferred.

Native producer `e4a559069426e69f13478623ae69387b252c1323` observed three ordinary
type refusals. PreMadhugiri body127 was accepted with no receipts or gas and the
same authenticated state root as the empty control. Its body transaction root
is distinct. This result is an explicit absence of preactivation-refusal
credit for127, not an activation guarantee or a paired boundary crossing.
