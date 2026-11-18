# Coherent private current-fork BSC import

Status: implementation in progress; native import NOT_RUN.

This continuation starts from accepted PR188. It must use the accepted BSC
capture patches and an actual Parlia chain, with no fake engine or modified
consensus rules. It does not claim public/mainnet replay equivalence.

The planned private chain uses chain ID714 and an explicit compressed fork
schedule. Block forks through Hertzfix are active at genesis; Shanghai and
Kepler are active initially. The remaining applicable timestamp forks through
Pasteur activate together after the initial block. Jenner and later forks
remain inactive. Native CheckConfigForkOrder must accept the complete config.
Target type-path blocks are strictly after all applicable current forks.

Explicit genesis code comes from the same native mainnet upgrade constants
and code digests established by the accepted initialization prerequisite.
Private genesis hashes have no default modern upgrade map. Native block1
runs the legacy initializers; the Feynman transition runs its own native
initializers. No already-initialized validator is silently placed at genesis.

An explicitly funded public-key operator registers the owned consensus/BLS
validator through real StakeHub.createValidator and native BLS precompile
verification. A short native chain crosses UTC midnight, triggering actual
parent-state election calls and updateValidatorSetV2. The target current-fork
blocks then execute successful/reverting transactions of types0–4. The fixture
must verify the active validator mapping before claiming active income.

Generation executes the pinned native pre/post protocol calls, required modern
header fields, native transaction application, Parlia assembly/sealing and
InsertChain. Fresh capture import and reopen/reimport retain observations,
durable admission/decision records and live transaction state snapshots.
Each system transaction needs independently expected nonce, balance and
relevant storage transitions. Roots and agreement with the generator alone
are insufficient acceptance evidence.

All attempts use frozen committed sources, fresh owned outputs, bounded
commands and the coordinator's exclusive runtime slot. Preserve failures.
Bootstrap-only results remain explicitly separate if later phases fail.
Public profiles and any unexecuted activation/unknown-type controls remain
NOT_RUN. Issue #21 remains open until its full accepted criteria are met.
