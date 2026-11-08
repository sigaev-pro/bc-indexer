# Independent expected results

These literal expected values are fixed before preparation and native import.
The generator does not read or write `expected.json`. The comparator does not
calculate its expected balances from native receipts, dumps, or generated roots.

This private chain activates London and its prerequisite forks at genesis. Its
terminal total difficulty is deliberately unreachable in this two-block fixture;
it tests historical proof-of-work import rules, not public consensus or current
Ethereum head behavior. The sender uses private key 1, a public test key that must
never hold public-network funds. Its address is
`7e5f4552091a69125d5dfcb7b8c2659029395bdf`. Recipient `1000...0000` and coinbase
`2000...0000` have no keys or initial balances in this test.

The sender starts with 10 ETH. Block 1 transfers 1 ETH using a legacy transaction,
nonce 0, gas limit 21,000, and gas price 2,000,000,000 wei. Its recipient is an
empty account, so intrinsic gas is exactly 21,000. Sender balance is
10,000,000,000,000,000,000 - 1,000,000,000,000,000,000 - 42,000,000,000,000 =
8,999,958,000,000,000,000 wei, with nonce 1. Recipient balance is 1 ETH, nonce 0.

Genesis base fee is 1,000,000,000 and gas limit is 10,000,000. The empty genesis
lowers block 1 base fee by one eighth to 875,000,000. Coinbase receives the
Constantinople-and-later 2 ETH reward plus
(2,000,000,000 - 875,000,000) * 21,000 = 23,625,000,000,000 wei of tips.
The base-fee burn is 18,375,000,000,000 wei. No refund or uncle is present.

Block 2 is empty. Its base fee is
875,000,000 - floor(875,000,000 * (5,000,000 - 21,000) / 5,000,000 / 8) =
766,084,375. Sender and recipient are unchanged; coinbase receives another 2 ETH.
Both blocks must be present, with the expected gas and transaction counts.

Generated genesis/block hashes and state roots bind the prepared input. They are
not independent oracle values. A full native export must match that input byte
for byte, while both native account dumps must match the literal values above.
