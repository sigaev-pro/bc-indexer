# Private Bor transfer oracle

This oracle is fixed before fixture generation. It describes a two-block private
chain associated with the accepted Bor build; it is not Polygon Mainnet or Amoy
identity or public-profile evidence. Expected values come from source rules and
arithmetic, not from generated blocks, state roots or observed receipts.

## Exact source and fixture

Use Bor source commit `3a018e38467594508ca31ebeaf804837fb92834a` and its
`tests/bor/testdata/genesis.json`. Preserve its real validator/state-receiver/token
contract code, original balances and fork schedule. Add the public test-key-1
sender with 10 native coins. The distinct recipient starts absent/zero. The
fixture chain ID is 15001; London activates at block 1, Jaipur at 2, later
configured forks at 3, and the first sprint boundary is 4. Neither tested block
executes a sprint transition. Native preparation must verify the span-0 validator
from the actual genesis contract; static code presence is insufficient.

The span-0 signer is `0x71562b71999873db5b286df957af199ec94617f7`, identified
with its tracked test credential in `tests/bor/helper.go`. Preparation may use
that test credential but must log only its public identity. The existing signer
balance is 1000 native coins. Without-Heimdall mode is explicit: real genesis
validators and genuine Bor Prepare/Authorize/Seal/import are required, while
Heimdall, state-sync, checkpoint and L1 behavior remain untested. DevFakeAuthor
must remain false.

## Independent monetary and log values

The first block contains one legacy transfer: 1 native coin, 21,000 gas, gas
price 5 gwei, nonce 0, and empty calldata. Its London gas limit is 20,000,000:
the native transition doubles the genesis limit of 10,000,000. The first base fee
is 1 gwei. The second block keeps the same gas limit and has no transactions.
Its base fee is `1000000000 - floor(floor(1000000000 * 9979000 / 10000000) / 8)
= 875262500`. Jaipur changes the seal payload, not this denominator.

The sender pays 105,000,000,000,000 wei and transfers 1 native coin. Its final
balance is 8,999,895,000,000,000,000 wei with nonce 1. The recipient receives
1,000,000,000,000,000,000 wei with nonce 0. Bor credits the configured burnt
contract, address zero, with 21,000,000,000,000 wei, and the recovered signer
with an 84,000,000,000,000 wei tip. The signer ends at
1,000,000,084,000,000,000,000 wei with nonce 0. There is no block subsidy.
The header coinbase remains zero; the execution beneficiary is the recovered
author. These two identities must not be confused.

The ordinary transfer produces two logs at address `0x1010`, in this order:
the Bor transfer log and the Bor fee log. The exact signatures, four topics and
five ABI words are fixed in [expected.json](expected.json). Transfer data is
`[1000000000000000000, 9999895000000000000, 0, 8999895000000000000,
1000000000000000000]`. Fee data is `[84000000000000, 10000000000000000000,
1000000000000000000000, 9999916000000000000, 1000000084000000000000]`.
The fee log's fourth word is the initial sender balance minus the tip; it is
deliberately not the final sender balance. The native source explicitly retains
this deprecated EIP-1559 log behavior. Do not normalize it into a different log.

Relevant source rules are `core/state_transition.go:473-490,627-670`,
`core/evm.go:52-65,264-274`, `core/bor_fee_log.go:11-13,91-118`,
`core/chain_makers.go:622-631`, `consensus/misc/eip1559/eip1559.go:CalcBaseFee`,
`params/protocol_params.go:302-325`, and `consensus/bor/bor.go:223-227,1433-1447`.
The empty second block has no transaction, receipt, log, withdrawal or reward;
its full state root and selected account balances/nonces must remain unchanged.

## Evidence and limits

Generated hashes, timestamps and roots bind prepared input bytes; they are not
independent expected outcomes. Actual server readiness must prove fresh genesis
and head 0. Imported head 2, full raw blocks, author, body receipts, joined logs
and state dumps must come from the accepted Bor server over local IPC. Any Bor
pseudo receipt must be kept distinct from body receipts. Raw RPC evidence and
the separately pinned helper source/compiler/module/command/binary authority
must remain available. Mutable scenario input cannot nominate an executable.

The portable comparator tests synthetic observations only. Native preparation,
server startup, effective execution flags, IPC import and real controls remain
NOT_RUN at this first checkpoint. Contract-internal storage, further forks,
sprint/span transitions, public networks, capture and NATS are outside this
partial oracle. All seven public profiles and the complete quick suite remain
NOT_RUN; issue 12 stays open.
