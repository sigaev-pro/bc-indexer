# Bor block of E01-02.decision_bridge

This directory is the Bor-only part of `adapters/bor/decision` (the shared
core is described in [../README.md](../README.md)). It joins the decision
core to the Bor product import path of the admission block
([../../admission/native/README.md](../../admission/native/README.md)): the
native marker patch, the child-root barrier, the import-path calls, the
switch of the admission to `OpenWithCleanup`, and the recovery driver that
completes the native import of every EXEC_READY observation in
RECOVERY_ONLY before `decision.Bridge.GoLive`. The plan rows it implements
are in PROJECT_STATUS ("E01-02.decision_bridge", plan for the per-client
blocks, every row that names Bor or each client block).

## Layout

| Path | Installed in the Bor tree at | Content |
|---|---|---|
| `decider/` | `extractor/decision/native/decider` | Stdlib-only decision rules: the intent of an executed block, the native rejection, the recovery driver |
| `bordecision/` | `extractor/bordecision` | Native glue: the child-root check over the hash trie store, the live and recovery deciders, the native-only replay before a completed import, `Start` |
| `tree/` | the same relative path | New native files of the patch set (`core/zz_extractor_decision.go`) |
| `patches.json` | (applied after the admission patch set) | Anchored edits of `core/blockchain.go` |

`test/harness/bor_admission_tree.py` installs the shared `decision` package at
`extractor/decision`, the two packages above, the new files and the edits,
after the admission block and its patch set; an edit name must be unique
across both patch sets and every anchor must occur exactly once. The
admission block switches to `admission.OpenWithCleanup` with a
`source_inputs.Purge` adapter (`borimport`), so closing an observation deletes
its capsule, dependency log and admission records (D100). A store written
before this switch must be discarded, not reused.

## Import path (product)

One decision per executed block, after its EXEC_READY, on the product import
path (`insertChain`, normal write path, edit `decision-import-write`). The
native chain lock (`chainmu`) is held throughout; the decider then takes the
observation guard, so the lock order of the admission block holds (chain
lock, then guard). In protocol order:

1. **Child-root barrier.** The StateDB of the block is committed
   (`CommitWithUpdate`), its root is flushed through the hash trie database
   (`triedb.Commit(root)`) and the key-value store is synced. The root must
   be the header root. Native code commits the StateDB once more inside
   `writeBlockWithState`; edit `decision-commit-once` makes that call return
   the barrier's commit, so the state is committed once. In the hash scheme
   the trie database commits children before their parent, so a durable
   root node means a durable trie below it; `ExtractorRootDurable` (the
   `Roots` of the bridge) syncs and checks the root node.
2. **Intent.** The native fork choice predicts the write before it happens,
   with the total difficulty the write would store (`extractorTransition`):
   `IMPORT` onto a child of the head, `CANONICAL` onto another branch,
   `SIDE` without a head change (`setHead` false, or a block that does not
   outweigh the head). The decider binds the authoritative EXEC_READY
   observation of the block (open, lowest sequence; a block observed twice
   gets one decision) and its state root, and records the intent
   (`decision.Bridge.Intend`). For `CANONICAL`, the stored blocks of the new
   branch between the common ancestor and the block (known side blocks that
   become canonical with it) are members too, ascending, before the block:
   each is named by its authoritative EXEC_READY observation, which its
   `SIDE` decision retained, so the decision closes it. A branch block
   without such an observation has no corpus binding, and the intent is
   refused (the import stops). The evidence digest binds the fork choice
   inputs (see Digests). `IMPORT` and `CANONICAL` have public events; `SIDE`
   has none.
3. **Native check.** The native milestone and checkpoint check
   (`ValidateReorg`, as in native Bor). A refusal is a native rejection
   after EXEC_READY: nothing of the block was written, and the decider
   records it (`RejectIntent` with the digest of the native error). A block
   that is stored natively cannot be rejected (source invariant error).
4. **Native write with the marker.** The final authoritative batch of the
   decision carries the marker (`MarkerValue` under `MarkerKey`): the head
   batch of the block (`writeHeadBlockWithPreconf` called by
   `emitPostWriteEvents`, edit `decision-marker-head-batch`) for `IMPORT`
   and `CANONICAL`, after the block batch and, for a reorganization, after
   every reorg batch; the block batch (edit `decision-marker-block-batch`)
   for `SIDE`. The marker is armed only for this block and this write. The
   write must carry it and end with the predicted status, then the store is
   synced and the decider resolves the intent (`Resolve`, then `Cleanup`).

Any other failure after the intent (a write error, a missing marker) leaves
the intent unresolved: the bridge refuses a new intent, so imports stop until
the recovery driver settles it at the next start.

The import of a block that is already stored with its state does not reach
this path: native `insertChain` skips a known block before execution, so it
opens no observation and no decision (measured after a restart,
`known-block-restart`). Canonical and finality changes without a new
execution (known branch, `SetHead`, the finality setters, the whitelist)
belong to E01-02.lifecycle_seams.

## Recovery driver

`bordecision.Start` runs at node start, after `borimport.Start`, with the
native chain lock held (`ExtractorRecover`) and then the observation guard:

1. The admission recovery (`obligation.Driver.RecoverThen`): every
   INPUT_PREPARING observation is abandoned; every ADMITTED obligation is
   re-executed from its pin and capsule with its log replayed, then
   EXEC_READY or rejected.
2. `decision.Open`, which checks every open decision against its records and
   marker and finishes interrupted closes.
3. `decider.Recover`:
   - The saved unresolved intent, if any: with its marker, `Resolve`.
     Without, native recovery completes exactly the saved transition: the
     last member (the executed block; earlier members are branch blocks
     already stored) is replayed natively from its capsule and closed log
     (`ReplayHeld`: every counted read from the log, every entry consumed, the
     bound result digest), and the native import of the block runs through
     the decision path of step 2 to 4 above, with the transition predicted
     from the saved old head (the native head must be that head or on the
     saved branch). The recovery decider returns
     the saved intent only if it is exactly the predicted transition (kind,
     heads, member, child root, evidence); anything else is a source
     invariant error. A native refusal records the rejection; an intent
     that neither completes nor is refused stops the start.
   - Every EXEC_READY observation without a decision, in sequence order, the
     same way: native-only replay, then the native import with a new
     intent. This completes, in RECOVERY_ONLY, the native import of an
     obligation that the admission recovery re-executed or that a loss left
     after EXEC_READY with a volatile child root (AC-CAP-07-02). A block
     that is already stored without a decision is a source invariant error.
   - `decision.Bridge.GoLive`.
4. Only then are the live decider and the observer installed.

The admission's own re-execution in step 1 writes no block; the native import
in step 3 does. A loss at any step of a decision is therefore settled before
the node imports again:

| Loss after | Store at the restart | Recovery |
|---|---|---|
| EXEC_READY (AC-CAP-07-02) | EXEC_READY, child state volatile | Native-only replay, then the native import with a new intent |
| The child-root barrier | EXEC_READY, child state durable | The same |
| The intent (AC-CAP-07-03) | Unresolved intent, no marker, block not stored | Native recovery completes the saved transition |
| The block batch (AC-CAP-07-03) | Unresolved intent, no marker, block stored if the block batch reached the disk; for `CANONICAL`, the head may already be on the branch (native reorg writes the head of each branch block before the final head batch) | The same, from the saved old head; the write repeats the block batch and the final head batch |
| The head batch with the marker | Unresolved intent with marker | `Resolve` |
| The decided sync | NATIVE_DECIDED, observation open | `decision.Open` closes it |
| A native refusal, before `RejectIntent` (AC-CAP-07-06) | Unresolved intent, no marker, block not stored | The native check refuses again (the whitelisted milestone is stored natively); `RejectIntent` |

## Digests

SHA-256, the tag followed by one zero byte, each field as an 8-byte
big-endian length and its bytes:

- Decision evidence (the intent's `evidence_digest`): tag
  `extractor-bor-decision-evidence-v1`; kind, block number (decimal), block
  hash, child root, old head number and hash, new head number and hash, the
  total difficulty of the old head and of the block (decimal). Hashes are
  64 lowercase hex digits.
- Decision rejection evidence (`RejectIntent`): tag
  `extractor-bor-decision-rejection-v1`; the intent digest and the native
  error text.

## Mirror

Until E05-01.registry exists, `Start` uses `NoMirror`: no decision is
applied, so every NATIVE_DECIDED `BATCH` decision keeps its single delivery
obligation and its marker, and the decision low-water mark stays at the
first of them (every start reads them all). `SIDE` decisions have no
obligation; `Cleanup` removes their marker at once.

## Checks

- `decider`: unit tests on the real admission and decision cores with an
  in-memory store and a fake native side, and `decider.mutants.json`
  (`test/mutate.py` in the module from `python3 test/harness/bor_decision.py
  assemble DIR`).
- `core/zz_extractor_decision_test.go`, `bordecision`: native Go tests in the
  Bor module graph during the preparation (`test/harness/bor_admission_probe.py`):
  the protocol order on a real chain (the root durable and nothing of the
  block written at `Intend`, the marker in the head batch or the block
  batch), the three kinds, a native refusal after the intent, an intent
  failure that writes nothing, the native path without a decider, the
  recovery import under the chain lock, and the commit-once and marker
  helpers.
- The probe (`test/harness/fixtures/admission_bor`) runs native Bor with the
  patched import path on a private fixture chain and loses the process at
  each decision step; the qa oracle is
  [ORACLE.md](../../../../test/harness/fixtures/admission_bor/ORACLE.md).

## Limits

- Private local evidence only (D96, D98, D99): test genesis, one validator,
  stub Heimdall. Public profiles stay with E06 and E07.
- The probe chain has one branch: `SIDE` and `CANONICAL` decisions on the
  import path are covered by the native tests, not by the probe.
- The native rejection fixture is a whitelisted milestone that conflicts
  with the block and arrives after its EXEC_READY; it is stored natively, so
  the restart refuses the same transition again.
- Process loss in the probe is a real SIGKILL at a decision step. Pebble may
  lose writes it has not synced when the process dies, so the `block-batch`
  step syncs the store first (the product write path does not sync there);
  every other step fires after a sync. Loss of unsynced writes is covered only by
  the unit tests of the shared core.
- Without E05-01.registry no obligation is applied and no `BATCH` marker is
  removed (see Mirror).
- A pin stays live only while the decision of its block is pending (and while
  a `SIDE` decision retains its block, until E05-01.activation_gc), so the
  retention guards of the admission block act on undecided blocks.
- The decision path is on the normal write path of `insertChain`. The
  pipelined, parallel and stateless import paths are outside the capture
  profile (`state_provider.CheckProfile`); a block observed on one of them
  would reach EXEC_READY without a decision, and the next start would stop
  (the block is stored without a decision, a source invariant error).
- The native glue (`bordecision`, `core/zz_extractor_decision.go`) has no
  mutant table: each mutant would need a full Bor build and probe run. The
  native tests and the probe cover it; the probe chain has one branch, so
  the `SIDE`/`CANONICAL` status check of the write and the partial-reorg
  completion are covered by the native tests only.
- Recovery completes an import with `setHead` true. A block imported with
  `setHead` false (`InsertBlockWithoutSetHead`, not used by the Bor import of
  the capture profile) would be predicted again with a head change; such a
  saved intent stops the start (source invariant error).
- A completed interrupted reorganization repeats the final head write only;
  stale canonical indexes above the new head that the interrupted native
  reorg had not yet deleted stay as native Bor leaves them after such a loss.
- Blocks that the node seals itself reach the chain through
  `WriteBlockAndSetHead` without an observation or a decision; the capture
  profile imports blocks, it does not produce them. Canonical and finality
  changes without a new execution belong to E01-02.lifecycle_seams.
