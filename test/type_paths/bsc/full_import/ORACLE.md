# Private BSC oracle boundaries

The native producer and the independent Python judgement have separate source
identities. A diagnostic result with `ac_verdict=null` is not acceptance. The
producer imports real Parlia blocks; its intermediate roots alone do not define
the expected balances or storage. Public profiles remain NOT_RUN.

`oracle_sources.json` pins exact official contract contents, Git blob identities
and SHA-256 digests. Runtime bytecode correspondence uses the accepted native
upgrade constants in `../modern_init/provenance.json`. This establishes source
provenance; it does not claim an independent Solidity recompilation.

Both Pascal and Pasteur dependency locks select OpenZeppelin 4.9.6. Its
Initializable stores uint8 `_initialized` and bool `_initializing` at offsets
0 and 1 in slot 0. Protectable then packs `_paused` and `_protector` at offsets
2 and 3, with blacklist slot 1 and its reserved slots 2–51. Consequently
StakeHub and Governor initialize slot 0 to `protector << 24 | 1`.
TokenRecoverPortal inherits ReentrancyGuard first: initialized flag remains
slot 0, status occupies slot 1, its gap occupies 2–50, and Protectable's
protector occupies slot 51 at offset 1. These layouts are not interchangeable.

StakeHub then places receive status at 52; transfer gas, minimum self stake,
minimum change and maximum elected count at 53–56; validator-set values and
indexes at 63–64; validator records at 65; moniker mapping at 66; consensus
mapping at 67. Validator records contain consensus, operator, credit proxy,
creation time and vote bytes at offsets 0–4 from the mapping entry.

StakeCredit inherits Initializable, ReentrancyGuard and ERC20. The associated
Context gap occupies 51–100; ERC20 balances mapping is 101, total supply 103,
and its reserved gap ends at 150. StakeCredit operator is 151 and pooled BNB
152. Registration forwards all 2001 BNB to the credit proxy, minting 2000
shares to the operator and 1 share to the dead address. The lock is shares,
not a native transfer to the dead address. CREATE uses the explicitly zero
genesis nonce of StakeHub; the proxy's EIP-1967 implementation is 0x2003 and
admin is the dead address. The native regression retains the rejected old
moniker and verifies that its isolated state copy changes no canonical state.

The absolute balance/nonce ledger starts from the explicit private genesis.
It checks every observed account before and after every transaction, derives
fees from receipt gas and the fixture's fee rules, and never initializes the
next expected state from observed after-state. It models Parlia's pre-deposit
SystemAddress-to-coinbase sweep, reward/burn fractions, deprecated and active
deposit balances, election dusk transfer, proxy funding, and authorization
nonce persistence on revert. Selected storage assertions are separate.

Signed envelopes bind row fields and recovered senders to ordered raw block
transactions. Ordered transaction and receipt tries bind those wires to the
native header. Invalid EIP-7702 authorization signatures decode with no
recovered authority; they do not imply rejection of the containing native
transaction. This fixture independently requires its intended valid
authorizations. Native-vector and mutation controls cover the test-only
codec; pending diagnostic vectors must cover embedded trie children and
shared paths before acceptance.

The genuine bootstrap calls are modeled separately: undelegating one self-share
BNB creates the pending request through StakeCredit, and the StakeHub call into
ValidatorSet materializes the legacy extra array. Redelegating one BNB restores
2001 BNB pooled stake and 2000 operator shares. The proxy's native balance is
2002 BNB because the pending request remains funded. Request shares/amount,
seven-day unlock, queue packing/hash, sequence and governance-token supply and
operator balance have separate before/after expectations. The pending unlock is
also carried across subsequent rows; correlated substitutions cannot reset it.

D108 offline follow-up binds the selected Validator tail labels to separate
raw-word observations at offsets 10 (jailed), 11 (jailUntil), 12 (updateTime),
and 13 (agent), relative to the operator entry in mapping slot 65. Registration
expects zero jailed/jailUntil/agent and the block timestamp for updateTime.
Bootstrap requires zero jailed/jailUntil/agent independently of label/word
agreement. Correlated changes to both representations must still fail the
expected-value checks. The producer adds those four words in a separate
literal-offset loop at `0b0a5a9`; this oracle branch does not modify native code.
These are selected state observations, not Merkle-authenticated storage proofs.

Receipt controls distinguish an empty RLP topic list from an empty byte string,
even when both observed receipt wires and reported topics are changed together.
The explicit four-field receipt arity guard has a verdict-equivalent deletion:
the immediately following four-target tuple unpack still rejects every other
cardinality with ValueError. This equivalence depends on retaining that unpack;
it does not justify removing both checks. Earlier failed mutation records stay
retained. Offline controls do not establish native bootstrap or import success.
