# Private BSC Parlia expectations

This oracle is fixed before fixture generation. It applies only to the private
chain derived from BSC commit `54afaff4da31ac9e49ba646a4f65104edac5a2f3` and its
`tests/truffle/storage/genesis.json`. Preserve the original 24 allocations and
11 system-contract bytecodes, adding only the public test-key-1 sender with
10 BNB. The private chain ID is 714. It is not BSC mainnet or Chapel identity.

The sole validator is `0x03735c2ed70a56cd221e0024eb4bf90243c9d6e9`, selected by
the source genesis and matching public test keystore. Native key decryption must
verify that public identity without logging private material. The native Parlia
engine must prepare, authorize, assemble, seal and validate both blocks. Captured
native Prepare timestamps are prepared inputs, not deterministic expectations.

At heights 1 and 2, London (height 8) is inactive. Kepler also requires London,
even though the source configuration has `keplerTime: 0`. There is no base fee,
burn or Ethereum mining reward. The private genesis selects the Default system
contract network; the source's Mirror/Bruno/Euler upgrades only map public
networks and do not replace this private fixture's contracts.

The first user transaction transfers 1 BNB with gas price 5 gwei and gas 21000.
The exact fee is 105000000000000 wei. Sender balance becomes
8999895000000000000 with nonce 1; recipient balance becomes
1000000000000000000 with nonce 0. Both remain unchanged at block 2.

`consensus/parlia/parlia.go` initializes seven contracts in this order at block 1:
1000, 1001, 1003, 1006, 1004, 1005, 2000. Each call has zero value and `init()`
data `0xe1c7392a`. The source ABI and `signer/fourbyte/4byte.json` independently
identify this selector. Native fee distribution then sends fee/16 =
6562500000000 wei to 1002, and 98437500000000 wei to 1000 through
`deposit(address)` (`0xf340fa01`, followed by the padded validator address).
These nine system transactions use validator nonces 0 through 8, gas price 0,
and gas limit 9223372036854775807. They follow the user transaction. All ten
receipts must succeed. Only the user receipt has independently fixed gas 21000;
system calls execute native EVM gas and do not have ordinary intrinsic gas.

The validator nonce is 9 after block 1. Its final balance and contract-internal
transfers are not inferred from native fee routing. Block 2 has zero transactions,
receipts, withdrawals and gas. With an in-turn validator, no active later system
branches, no private contract upgrade, and the explicitly checked zero balance
of the fee accumulator `0xfffffffffffffffffffffffffffffffffffffffe` after block 1,
the second block must preserve the entire state root. This checks an invariant;
generated root literals never become independent expected roots.

The comparison joins every transaction and receipt by exact order, hash, block
identity and count. CLI header observations and the complete imported/exported
chain must bind the supplied identities before calling this boundary. Raw native
state and receipt observations remain evidence. Exact system-contract storage,
log semantics, system gas and validator/contract balances are unchecked. Public
profiles, capture and NATS are also unchecked. This two-block slice cannot pass
the full quick suite or close issue 12.

The imported-DB observer will be a separately pinned preparation tool. A scenario
fixture cannot nominate an executable. The trusted caller must supply the tool
manifest digest separately from scenario inputs; that manifest must bind the
accepted build, isolated BSC source/module closure, compiler, commands and binary.
The observation comparator alone does not authenticate an execution claim.
