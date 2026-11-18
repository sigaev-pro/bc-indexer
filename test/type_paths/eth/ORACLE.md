# Independent ETH type-path expectations

This oracle is written before the native probe runs. Native output may
provide actual gas consumption and effective prices, but cannot choose
expected receipt status, source fields, coverage, value transfer, account
nonce changes, delegation, or a pass verdict.

Private chain 31337 uses public test key 1 as the sender
`7e5f4552091a69125d5dfcb7b8c2659029395bdf` and key 2 as the authorization
authority `2b5ad5c4795c026514f8317c7a215e218dccd6cf`. The sender starts with
100 ETH. Success recipient is `4000000000000000000000000000000000000001`;
revert recipient is `3000000000000000000000000000000000000002`. Its code
`600160005560006000fd` stores 1 in slot 0 and then reverts, so that slot
remains zero before and after each included failed transaction.

The ten transactions are, in order, types 0,1,2,3,4 in block 1 and again in
block 2; sender nonces are 0 through 9. Each sends one wei with empty data
and gas limit 150,000. Block 1 succeeds, moving five wei total to the success
recipient; block 2 reverts, moving no value to its target. Every included
transaction consumes a sender nonce. Sender balance falls by gasUsed times
effectiveGasPrice, plus blobGasUsed times blobGasPrice for type 3, plus one
wei only on success. This accounting relation is checked from actual native
state/receipts; it is not a full independent gas schedule implementation.

Types 1 through 4 include the same ordered access list: target with slot 0
twice, then target with no storage keys. Duplicates are intentional. Types
0 and 1 specify gas price 2 gwei; types 2 through 4 specify fee cap 2 gwei
and priority cap 1 gwei. The blob transaction has one version-1 hash
`01` followed by 31 zero bytes, a 1 gwei blob fee cap, and no sidecar.

Type 4 authorizes delegation to `3000000000000000000000000000000000000004`
(STOP code). Authority nonces are 0 and 1 in the respective authorization
tuples. The authority nonce becomes 1 and 2 respectively, and its code is
`ef0100` followed by the delegate address after either transaction, even
when the second transaction's EVM execution reverts. The authority does
not pay the transaction fee. Outer signed source bytes and all source list
entries must be preserved in the imported block and observed transaction.

Each block must contain exactly the four protocol system-call targets:
beacon roots `000f3df6d732807ef1319fb7b8bb8522d0beac02`, history storage
`0000f90827f1c53a10cb7a02335b175320002935`, withdrawal queue
`00000961ef480eb55e80d19ad83579a64c007002`, and consolidation queue
`0000bbddc7ce488642fb579f8b00f3a590007251`, all from
`fffffffffffffffffffffffffffffffffffffffe`. These are observations of call
presence, not acceptance of outside-transaction effect semantics.

Each fork control accepts the selected type in block 2 at its activation
and rejects it in block 1 before activation without advancing the head.
Berlin and London controls use premerge headers with nonzero difficulty
and the native fake seal engine; Cancun and Prague controls use PoS headers.
These miniature activation configurations are private fixtures, not public
historical-chain evidence. Attempt 01 incorrectly combined pre-London rules
with PoS headers and failed header validation before reaching the type gate;
the expected type-gate outcomes remain unchanged.
The native signer must separately return ErrTxTypeNotSupported before the
fork. For type 3 the import rejection is the earlier native body check
`data blobs present in block body`; the other types reach unsupported-type
rejection. Unknown envelope `7ec0` must be rejected by the native transaction
decoder as ErrTxTypeNotSupported. Decoder rejection is not reported as an
executed import. All public profile cells remain NOT_RUN.
