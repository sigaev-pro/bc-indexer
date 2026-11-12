# Independent expected results of the smoke corpus

The qa role wrote this oracle before any corpus block was generated or
imported by a native client. `corpus_oracle.py` computes the expected files
in `expected/` from the transactions and genesis accounts below,
the gas rules in [corpus_evm.py](corpus_evm.py), and the fee rules of each family.
It never reads a native output. Generated block hashes, timestamps and state
roots bind the prepared input; they are never expected values.

`python3 test/native_corpus/corpus_oracle.py check` proves that the committed files
are what `corpus_oracle.py` computes. `test_native_corpus.py` checks the reference
executor against the published EIP-2200 and EIP-3529 gas tables, and checks
the totals below against hand arithmetic.

## Fixtures

Each family uses its accepted private fixture with only these accounts added
to the genesis allocation:

| Address | Code | Role |
|---|---|---|
| `7e5f4552091a69125d5dfcb7b8c2659029395bdf` | none | sender, public test key 1, 10 native coins; already present in every accepted fixture; never funded on a public network |
| `3000000000000000000000000000000000000001` | parent, below | nested-revert parent |
| `3000000000000000000000000000000000000002` | child, below | nested-revert child |
| `3000000000000000000000000000000000000003` | receiver, below | Bor StateSync receiver (POLY only) |

The transfer recipient `4000000000000000000000000000000000000001` starts
absent. None of these addresses is in an accepted fixture allocation. The
fixtures are: ETH chain 31337 (`test/harness/fixtures/genesis.json`, London
at genesis, ethash, unreachable terminal total difficulty); BSC chain 714
(`test/harness/fixtures/bsc_admission/genesis.json`, Berlin and London at
block 8, sole validator `03735c2ed70a56cd221e0024eb4bf90243c9d6e9`); Bor chain
15001 (`tests/bor/testdata/genesis.json` of Bor `3a018e38`, London at block 1,
Shanghai, Cancun, Prague, Delhi and Bhilai at block 3, sprint 4, signer
`71562b71999873db5b286df957af199ec94617f7` with 1000 coins).

## Contracts

Hand-assembled bytecode (no compiler). `corpus_evm.disassemble` prints it.

Child, `600160005560c160006000a160ee60005260206000fd`: store 1 in slot 0, emit
`LOG1` with topic `0xc1` and no data, then `REVERT` with the 32-byte word
`0xee`.

Parent: store 1 in slot 0; `CALL` the child with the parent's call value,
30,000 gas, no input and a 32-byte output area at memory 0; store `ISZERO` of
the result in slot 1, `RETURNDATASIZE` in slot 2 and the returned word in
slot 3; store the call value at memory 0 and emit `LOG1` with topic `0xa1`
over that word; stop. Forwarding all gas with `GAS` would leave the parent
1/64 of its gas after the call (EIP-150), so the child gets a fixed 30,000.

Receiver: for `onStateReceive(uint256 id, bytes data)` it stores `id` in
slot 0, `CALLER` in slot 1, `len(data)` (calldata word at 0x44) in slot 2 and
the first data word (0x64) in slot 3, then emits `LOG1` with topic `0x5e`
over `id`.

## Gas rules

ETH and Bor block 1 use London rules: EIP-2929 cold account access 2,600 and
warm 100, cold slot surcharge 2,100, SSTORE set 20,000, EIP-3529 refunds. The
transaction sender, the recipient and the precompiles are warm; the coinbase
is not (EIP-3651 is a Shanghai rule). BSC blocks before block 8 use Istanbul
rules: CALL 700, SSTORE set 20,000 with the EIP-2200 sentry. In both, a value
CALL costs 9,000 more, the callee gets a 2,300 stipend, unused callee gas
returns to the caller, and a `REVERT` keeps the remaining gas. No corpus
transaction earns a refund. Intrinsic gas is 21,000 (no calldata).

Child execution: London 6 + 22,100 + 9 + 750 + 6 + 6 + 6 = 22,883;
Istanbul 6 + 20,000 + 9 + 750 + 6 + 6 + 6 = 20,783.

Parent execution, London: slot 0, 22,106; pushes for the call, 20; the call,
100 + 2,500 (cold child) + 3 (one memory word) + 9,000 + 30,000 forwarded,
less 32,300 - 22,883 = 9,417 returned, so 32,186; slots 1, 2 and 3, 22,106,
22,105 and 22,109; `CALLVALUE`/`MSTORE`, 8; the log, 9 + 375 + 375 + 256 =
1,015. Total 121,655, so the receipt uses 142,655. Istanbul: 20,006 + 20 +
(700 + 3 + 9,000 + 30,000 - 11,517) + 20,006 + 20,005 + 20,009 + 8 + 1,015 =
109,255, so the receipt uses 130,255.

The direct call of the child (the second nested-revert transaction) fails
with status 0: London 43,883, Istanbul 41,783. Its value is not moved, its
storage write and log are discarded, and the full fee is charged.

## Ethereum (Geth)

Coinbase `2000000000000000000000000000000000000000`. Block gas limit
10,000,000 (input). Block 1 base fee 875,000,000.

| Index | Case | Transaction | Gas used | Price | Tip to coinbase |
|---|---|---|---|---|---|
| 0 | transfer | legacy, 1 ETH to the recipient, gas 21,000, 2 gwei | 21,000 | 2 gwei | 23,625,000,000,000 |
| 1 | fee | dynamic fee, 1 gwei to the recipient, gas 21,000, max fee 2.2 gwei, max priority 1.5 gwei | 21,000 | min(2.2, 0.875 + 1.5) = 2.2 gwei | 1.325 gwei × 21,000 = 27,825,000,000,000 |
| 2 | nested-revert | legacy, 5 wei to the parent, gas 200,000, 2 gwei | 142,655 | 2 gwei | 160,486,875,000,000 |
| 3 | nested-revert | legacy, 7 wei to the child, gas 100,000, 2 gwei | 43,883 | 2 gwei | 49,368,375,000,000 |

The base fee times the gas used is burnt. Block 1 uses 228,538 gas. The
sender ends at 10 ETH - 1 ETH - 1 gwei - 5 wei - 461,276,000,000,000 wei of
fees = 8,999,538,722,999,999,995 wei with nonce 4. The parent holds 5 wei.
The coinbase receives the tips and, outside any transaction, the 2 ETH block
reward of each block, which E01-01.harness already asserts
(`test/harness/fixtures/ORACLE.md`). Block 2 is empty with base fee
875,000,000 - floor(875,000,000 × (5,000,000 - 228,538) / 5,000,000 / 8).

System-action is NOT_APPLICABLE for ETH. DEVELOPMENT_PLAN E01-03 (line 89)
asks for native system transactions, and a block body of this London ethash
fixture has none. Two kinds of effects without a transaction exist on
Ethereum, and neither is a system transaction:
- post-merge protocol system calls (EIP-4788 beacon roots and EIP-2935
  history storage from Cancun and Prague, and the EIP-7002 and EIP-7251
  request contracts). No TASK_DECOMPOSITION card names them; the proposed
  owners, still to be confirmed by the coordinator, are E04-02.optional_scope
  for their effects outside a transaction and E01-05.type_paths for the
  current-fork system paths. This pre-merge fixture runs none of them;
- withdrawals and block rewards, which are credits outside a transaction.
  Line 89 checks them only when that extension is implemented; their owner is
  E04-02.optional_scope (outside-transaction records, off by default, enabled
  only with declared typed fixtures; BALANCE_MODEL, withdrawals and rewards).
  The block-end state here still binds the block reward.

## BSC (Parlia)

The user fee (gas used × gas price, no base fee before London) goes to the
fee accumulator `fffffffffffffffffffffffffffffffffffffffe`
(`core/state_transition.go` line 702). At block 1 Parlia appends, after the
user transactions, the seven `init()` calls (`0xe1c7392a`) to 1000, 1001,
1003, 1006, 1004, 1005 and 2000, then `distributeIncoming`
(`consensus/parlia/parlia.go` lines 2028-2058): `fees >> 4` to the system
reward contract 1002 with no data, and the rest to the validator contract
1000 through `deposit(address)` (`0xf340fa01` and the padded validator). The
nine system transactions come from the validator with nonces 0 to 8, gas
price 0 and gas 9,223,372,036,854,775,807. The fee accumulator ends at 0 and
the validator nonce at 9. Every system transaction succeeds (status 1), as
the accepted E01-01 BSC oracle states for the same fixture
(`test/harness/fixtures/bsc/ORACLE.md`, "All ten receipts must succeed").

| Index | Case | Transaction | Gas used | Fee at 5 gwei |
|---|---|---|---|---|
| 0 | transfer | 1 BNB to the recipient, gas 21,000 | 21,000 | 105,000,000,000,000 |
| 1 | nested-revert | 5 wei to the parent, gas 200,000 | 130,255 | 651,275,000,000,000 |
| 2 | nested-revert | 7 wei to the child, gas 100,000 | 41,783 | 208,915,000,000,000 |

Fees are 965,190,000,000,000 wei: 60,324,375,000,000 to 1002 and
904,865,625,000,000 to 1000. The sender ends at 8,999,034,809,999,999,995 wei
with nonce 3. Block 1 has 12 body transactions. Its block gas is not
asserted, because the system transactions run native system-contract code
that this oracle does not model. System-contract storage, logs and gas, and
the validator and system-contract balances are not asserted. Block 2 is
empty and keeps the whole state root (E01-01 BSC oracle).

## Polygon PoS (Bor)

The execution beneficiary is the recovered signer; the header coinbase stays
zero. The burnt contract at both blocks is address zero. Block 1 is the first
London block: gas limit 20,000,000 and base fee 1 gwei. For each transaction
Bor burns gas used × base fee to address zero, pays gas used × (price - base
fee) to the signer (`core/state_transition.go` lines 605-670), and logs at
`0x1010`:
- a transfer log for each value transfer, inside the transfer's snapshot, so a
  reverted call loses it (`core/evm.go` `Transfer`, `core/bor_fee_log.go`);
  data: amount, sender and recipient balances before, then after;
- after execution, a fee log when the tip is above zero; data: tip, the
  sender balance before the gas purchase, the signer balance before, the
  first minus the tip, the second plus the tip.

| Index | Case | Transaction | Gas used | Price | Logs |
|---|---|---|---|---|---|
| 0 | transfer | legacy, 1 coin, gas 21,000, 5 gwei | 21,000 | 5 gwei | transfer, fee (the E01-01 Bor values) |
| 1 | fee | dynamic fee, 1 gwei, gas 21,000, max fee 5 gwei, max priority 2 gwei | 21,000 | 3 gwei; tip 42,000,000,000,000, burn 21,000,000,000,000 | transfer, fee |
| 2 | nested-revert | legacy, 5 wei to the parent, gas 200,000, 5 gwei | 142,655 | 5 gwei | transfer, parent `0xa1`, fee |
| 3 | nested-revert | legacy, 7 wei to the child, gas 100,000, 5 gwei | 43,883 | 5 gwei | fee |

Block 1 uses 228,538 gas. Address zero ends at 228,538 gwei; the signer at
1000 coins plus 872,152,000,000,000 wei of tips; the sender at
8,998,899,308,999,999,995 wei with nonce 4. Blocks 2 and 3 are empty. Base
fees: block 2 = 1 gwei - floor(1 gwei × (10,000,000 - 228,538) / 10,000,000
/ 8); block 3 uses denominator 8 (parent 2 is before Delhi); block 4 uses 64
(parent 3 is Bhilai). Block 2 keeps the whole state root. Block 3 activates
Shanghai, Cancun and Prague; actions outside a transaction there are not
asserted.

Block 4 is the first sprint start. The in-process Heimdall stub (D98 item 6)
serves exactly one state sync event: id 1, contract the receiver, data the
29 bytes `extractor-corpus-state-sync-1`. The system address calls
`commitState` of `0x1001`, the genesis StateReceiver (solc 0.5.12 bytecode:
selectors `19494a17` `commitState`, `5407ca67` `lastStateId`; it requires
sequential ids and calls `onStateReceive`, `0x26c53bea`, with 5,000,000 gas
when the receiver has code; it emits no event). The reference executor runs
the receiver with that call: slots 0 to 3 become 1, `0x1001`, 29 and the
data padded to 32 bytes, and its one log (topic `0x5e`, data 1) is the only
log of the block's state sync receipt. `lastStateId()` returns 1. State sync
calls are outside the block gas (`consensus/bor/statefull/processor.go`
calls the EVM directly), so block 4 uses 0 gas and has no body transaction.
The span commit to `0x1000` at block 4 is not asserted.

## What the comparison asserts

For every user transaction: its fields, receipt status, gas used,
cumulative gas, logs in order, effective gas price, the call frames (type,
from, to, value, input, output, error), and the balance, nonce, code and
slots 0 to 3 of every listed account before and after it. For every block:
the body transaction count, base fee, gas limit and gas used where given,
BSC system transactions in order, the state sync result, the listed
accounts at the block end, an unchanged state root where stated, exactly one
IMPORT decision and no decision of any other block, and no new observation or decision after a restart and
re-import.

Not asserted: roots, hashes and timestamps as values; unlisted accounts;
system-contract internals; outside-transaction actions except the ETH block
reward already asserted by E01-01. This corpus is small. It is not full
production semantic coverage: TC02, the BOOT-08 catalog and the type matrix
of E01-05.type_paths stay with their owners.

## Older fork paths

The private fixtures run older paths than the production networks. These are
limits of this corpus, handed to E01-05.type_paths (the current-fork native
type and system paths) and to E06 and E07 (public profiles):
- POLY system-action is the StateSync before Madhugiri: the fixture has no
  Madhugiri block, so state sync is a separate receipt and not a body
  transaction. Polygon Mainnet and Amoy have Madhugiri, where state sync is a
  body transaction of type `StateSyncTxType`.
- The BSC fee case is the path before Kepler (`fees >> 4` to the system reward
  contract) under Istanbul gas rules. Production BSC is past Kepler, where
  that distribution is off.
- The ETH fixture is before the merge (ethash, London).

## Changing the oracle

A client block that finds a difference keeps the FAIL record (D83). The
oracle changes only in a separate change that the qa role makes and an
independent reviewer accepts, with a reason taken from the protocol rules or
the native source, never to match an observed value.
