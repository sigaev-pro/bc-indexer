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

Commands (repository root):

```sh
python3 test/type_paths/eth/test_type_paths_eth.py
python3 test/mutate.py test/type_paths/eth/eth_type_paths.mutants.json --output RUN_DIR
python3 test/type_paths/eth/eth_type_paths.py prepare --accepted-preparation PREPARATION.json --sha256 SHA --output PREP_DIR
python3 test/type_paths/eth/eth_type_paths.py run PREP_DIR/preparation.json --sha256 SHA --output RUN_DIR
```

Preparation uses the accepted decision-bridge preparation, applies its
unchanged native patch sets, verifies their installed bytes, and compiles
the new probe offline with its pinned compiler. Runtime executes the pinned
binary in the retained runtime image, then runs the retained independent
judge. Both phases use the host single-writer lock and retained command,
failure, and cleanup records. Native work needs the coordinator's runtime
slot; portable controls do not run a node.

The accepted admission `observation.Configure` sets only
ExtractorGetHash, ExtractorSafePoint and serial execution; it preserves
the import tracer slot used by this probe. A portable source guard checks
that composition assumption. Passing observation/decision records remain
mandatory; tracing alone does not prove capture.

Status: implementation in progress. Native preparation and runtime are
NOT_RUN until a retained record establishes their result.
