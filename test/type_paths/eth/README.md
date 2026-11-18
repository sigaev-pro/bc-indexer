# ETH native type-path probe (E01-05)

This private fixture exercises the five body transaction types in the pinned
Geth client through native block import with the accepted admission and
decision bridge. It is separate from the older-fork smoke corpus and from
the early-fault probes. No public profile receives credit.

The fixture uses chain 31337, the native merged test configuration through
Osaka (Bogota and UBT disabled), two generated PoS blocks, deterministic
public test keys, and fixed success/revert recipients. Each block includes
one transaction of each native type 0 through 4. Block 1 succeeds; block 2
calls a reverting contract. Blob transactions carry a versioned hash and
no sidecar. SetCode transactions authorize a separate authority, whose
authorization nonce and delegation survive the transaction's EVM revert.

Actual import tracing records transaction bytes and before/after accounts,
native receipt outcomes, and protocol system-call targets. Re-execution
on each imported parent state must reproduce imported receipts and roots.
The fixture installs the native beacon-root, history-storage, withdrawal
queue and consolidation queue contracts. Their call presence is observed;
outside-transaction effect semantics remain owned by E04-02.

Separate private chains place Berlin/London/Cancun/Prague at block/time 2.
Each imports an empty block before activation and its supported transaction
at activation. A substituted pre-activation body must be rejected without
advancing the head. The blob control may be refused by native body validation
before the signer; those boundaries are reported distinctly. An unknown
typed envelope must be refused by the native decoder, before import.

The Python judge owns expected outcomes independently of native output.
It checks coverage, source field preservation, successful and reverted
value transfer, sender nonce/fee arithmetic, SetCode authority state,
receipts/root equality, actual capture decisions and activation controls.
No expected values are learned from the generated receipts.

Status: implementation in progress. Native preparation and runtime are
NOT_RUN until a retained record establishes their result. Commands and
limitations will be recorded with the first prepared candidate.
