# Coherent private current-fork BSC import

Status: the judged private import and both importer refusal controls
PASSed (run09 and run15 at `54d048c`/`92afc8c`, 2026-11-20). Issue #21
stays open: the selected-state scope, the Hertz/Haber/Pascal
applicable-crossings question, public profiles and the merge remain.

The judge checks ordered transaction and receipt trie roots, signed envelope
fields and recovered senders against retained native vectors, and binds the
native receipt JSON (legacy receipts omit `type`; an access-list entry
without storage keys encodes as null). Source-derived selected initializer
and StakeHub/credit registration storage checks are provisional; their
complete provenance manifest and exhaustive controls remain pending. The
`diagnose` command remains the execution-only attempt (`ac_verdict=null`);
a diagnostic PASS is never acceptance.

Retained failure history of the resumed completion: diagnostic04 election
INVALID at block 11 (empty validator-extra array; repaired by the two
bootstrap transactions below); diagnostic05/07 at `8e08e63`/`c2d364c`
(missing blob sidecars at insert 12; target block not blob-eligible under
BEP-657); diagnostic08 at `c15cac0` (native PASS, judge FAIL on the receipt
and access-list encodings, aligned at `54d048c`); judged run09 PASS;
preparation10/11 vet failures of the control draft (method value as field;
blob hash out of scope); run12 parlia finalize panic (the control engine
lacked the generator's Authorize signer); run13 refused by the data
availability check after parlia.prepare clamped the historical time to now;
run14 judged refusal binding too strict for the native blob-body error —
then judged run15 PASS with both controls at `92afc8c`.

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

Bootstrap diagnostic continuation: `ValidatorSet.init()` creates 21 legacy
validators but leaves the later `validatorExtraSet` empty. Diagnostic04 reached
real registration and ten imported generation blocks, then failed with INVALID
at the first breathe block. The source-backed repair uses two genuine operator
transactions before election: undelegate one BNB of shares, then delegate one
BNB. StakeHub's self-delegation check calls `felony` through its normal contract
identity, materializing the extra array before the unmapped-validator return.
The sole eligible validator remains unjailed. The second call restores pooled
stake/supply to 2001 BNB, while the credit proxy holds 2002 BNB because one BNB
remains pending unbond. This route is diagnostic until native execution and the
independent pending-request, governance-token and absolute-balance oracle pass.
