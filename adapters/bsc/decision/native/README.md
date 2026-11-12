# Decision bridge: BSC block (E01-02.decision_bridge)

This directory holds the BSC-only part of E01-02.decision_bridge: the native
patch that orders the write of an observed block, and the Go package that
wires the shared decision core ([README](../README.md)) into the BSC import
path on top of the BSC admission seam
([README](../../admission/native/README.md)). It is not compared with the
Geth and Bor copies (`native/` is excluded from the shared-file check).

## Layout

| Path | Installed at | Content |
|---|---|---|
| `patches/07-core-decision-step.diff` | `core/extractor_decision.go`, `core/blockchain.go` | Decision step of an observed block, marker in the head batch, replay of a known block |
| `go/*.go` | `extractor/bsc/decision` (package `bscdecision`) | Hook, decision step, child-root barrier record, decision evidence, recovery driver, tests |

The driver `test/harness/bsc_decision.py` extracts the accepted BSC source
(commit `54afaff4da31ac9e49ba646a4f65104edac5a2f3`), applies the six
admission patches and then patch 07 with exact context and no fuzz, installs
the shared packages `source_inputs`, `admission`, `decision` and
`state_provider`, the BSC admission package (`extractor/bsc`) and this
package, and builds the probe test binary.

The BSC admission package gains, for this block:
- `Config.Cleanup`: when set, `Start` opens the admission with
  `admission.OpenWithCleanup` (D100);
- `Seam.Replay`: the native-only replay of an EXEC_READY observation;
- `Seam.RecoverObligations`: the admission part of `Recover`, without
  `GoLive`;
- accessors for the admission, the store, the capsule and the genesis.

## Decision step (patch 07)

While an observation is attached, `writeBlockAndSetHead` hands the write of
the block to `extractorWriteBlockAndSetHead` right after the native fork
choice (`ReorgNeededWithFastFinality`). The step runs, with the guard and the
chain mutex held:

1. The native fork choice: `canonical` (the block becomes the head, `IMPORT`)
   or not (a side block, `SIDE`). A block that needs a reorganization is
   refused: the admission profile imports only on top of the head.
2. The child-root barrier: the state commit, `triedb.Commit(root)` and
   `SyncKeyValue`. The committed root must be the block's root. The barrier
   needs the hash scheme.
3. `Intend` (the observation's `core.ExtractorDecider`): it records the
   transition intent and returns the decision. An error stops the import
   before any write of the block.
4. The block batch: the writes of native `writeBlockWithState` (total
   difficulty, block, receipts, blob sidecars, preimages). For a `SIDE`
   decision this is the final batch and carries the marker.
5. For `IMPORT`, the head batch of native `writeHeadBlock`, with the marker
   `decision.MarkerValue(intent)` under `decision.MarkerKey`.
6. `SyncKeyValue`, then `Resolve`: NATIVE_DECIDED, which closes the member
   observation through `admission.Decide`.
7. The native feeds, and `SetFinalized` with the finalized header of the
   decision evidence.

An observation without a decision step is refused at the write
(`ErrExtractorDecision`), and so is a direct `writeBlockWithState` of an
observed block. `skipBlock` never skips an observed block: only the replay of
an EXEC_READY observation observes a block that is already stored, and the
native known-block path would move the head without a decision.

The trie garbage collection of `writeBlockWithState` does not run for an
observed block: the barrier has flushed its whole state to disk.

## Hook

`Hook` implements `core.ExtractorHook` over the admission seam. `Begin`:
- refuses any block other than the member of an unresolved intent
  (`decision.ErrPending`);
- replays a block with an open EXEC_READY observation (`Seam.Replay`)
  instead of observing it again. Before the bridge opens it refuses that
  block (`ErrDeferred`): recovery replays it later.
- hands any other block to `Seam.Begin`, which also skips a block that is
  already in the chain with its state.

Every observation it returns implements `core.ExtractorDecider`. `Intend`:
- records the root of the barrier (see "Child-root barrier");
- computes the decision evidence (see "Decision evidence");
- builds the intent: kind `IMPORT` with public events, or `SIDE` without
  them, the old head, the new head, and one member (the EXEC_READY
  observation, its digest and the block root);
- records it with `decision.Bridge.Intend`. If an intent is unresolved, it
  must be exactly this transition (`sameTransition`), and the step reuses it
  instead (`ErrTransition` otherwise).

`Resolve` calls `decision.Bridge.Resolve`, then `Cleanup`.

## Child-root barrier

`Roots.Durable(root)` is true only for a root whose barrier synced in this
process and whose node is in the store. After a restart, a root node on disk
does not prove that its subtrie is complete, because the trie flush writes
several batches. So recovery replays every EXEC_READY observation without a
decision, and the replay's own barrier records the root.

## Decision evidence

`DecisionEvidence` records the native fork choice and the Parlia finality
result after EXEC_READY (D100, D99 item 2). It reads them through an
`EvidenceReader`, which serves:
- the parent and the ancestor window of the capsule;
- the block itself;
- the genesis.

Any other header read, total difficulty or verified-block query is refused
and fails closed: the step returns `ErrFinality`, the block is not written
and its observation stays EXEC_READY without a decision. A missing finalized
header also fails closed, where native BSC would read finality 0.

Parlia snapshots of a served header are computed by the native lookup, which
also sees the block, because the block is not written yet. These lookups
come after EXEC_READY and are not logged; the evidence digest binds their
result.

The evidence digest is SHA-256 of the tag `extractor-bsc-decision-evidence-v1`
and a zero byte, then `str(kind)`, then the old head, the new head, the
justified block (`GetJustifiedNumberAndHash`) and the finalized block
(`GetFinalizedHeader`), each as `u64(number) || hash`. The intent's
`evidence_digest` is this digest.

The native finality paths stay with E05-01.bsc_finality: the safe and
finalized setters, the VotePool, and early finality from votes in memory.
With a VotePool, `GetFinalizedHeader` reads in-memory votes; their result is
recorded in the evidence, but not their source.

## Recovery driver

`Hook.Recover` runs after the native DB recovery (`NewBlockChain`):

1. `Seam.RecoverObligations` abandons INPUT_PREPARING observations and
   re-executes each ADMITTED obligation. The bridge is not open, so `Intend`
   refuses with `ErrDeferred` before any write, and the block stays
   EXEC_READY.
2. `decision.Open`.
3. The unresolved intent, if any, is settled from native evidence:
   - its marker is present: `Resolve`;
   - its member block is a native bad block (the `reportBadBlock` record) and
     is not stored: the native side rejected it, so `RejectIntent` with
     `BadBlockEvidence`;
   - its member block is both a bad block and stored: the native data cannot
     settle it (`decision.ErrInvariant`);
   - otherwise the import completes exactly the saved transition: it imports
     the block again, the hook replays it, and `Intend` reuses the saved
     intent.
4. Each EXEC_READY observation without a decision is imported again. The
   hook replays it, and the decision step decides it. The replay checks the
   EXEC_READY record (its consumed and exec digests) and does not replace it
   (AC-CAP-07-02).
5. `decision.Bridge.GoLive`. An error keeps the node in RECOVERY_ONLY.

`BadBlockEvidence` is SHA-256 of the tag `extractor-bsc-bad-block-v1` and a
zero byte, then the block hash and the SHA-256 of the block's RLP encoding.

On the BSC import path, native code refuses nothing after the fork choice:
the total difficulty is read before it, and a failed write ends the native
process. So the AC-CAP-07-06 case uses a native outcome fixture: the native
bad-block record of the member block, written after the durable intent.

## One decision per block

A block that is already in the chain with its state opens no new
observation (`Seam.Begin`), so it gets no second decision. An EXEC_READY
observation from before a restart is replayed, never observed again. So the
BSC import path never observes the same block twice.

## Checks

`go/unit_test.go` covers:
- the barrier record (`Roots`);
- the evidence reader: the window, the refused reads, and a snapshot that
  sees the block;
- `Evaluate`: the IMPORT and SIDE evidence, the digest formula, and the
  fail-closed cases (a justified or finalized block below the window, a
  refused read with a result, no finalized block, a justified error);
- `sameTransition`, `BadBlockEvidence`, `Purger`, the `Start` refusals, and
  `Intend` before the bridge opens.

`go/native_test.go` (`TestNativeBSCDecision`) is the native probe on the
accepted private fixture (blocks 1 and 2; the state root of block 1 differs
from the genesis, and block 2 has the same root as block 1):

- **live**: both blocks are decided (`IMPORT`, one obligation each). The probe
  mirror applies decision 0, and `Cleanup` removes its marker and records.
  An import before recovery is refused, and so is a hook call without the
  guard.
- **crash points of block 1**, each in a child process that kills itself
  (SIGKILL), then a restart, `Recover` and the live import of both blocks.
  At the native write points the child syncs the native store first: the
  outcome in which those writes reached the disk.

  | Crash point | AC | State at the restart | Recovery |
  |---|---|---|---|
  | `first-handoff` | 07-01 | ADMITTED, one durable response, root not on disk | Re-execution to EXEC_READY, replay, decision |
  | `exec-ready-unsynced-drop` | 07-01 | ADMITTED (EXEC_READY lost), root not on disk | The same |
  | `exec-ready-synced` | 07-02 | EXEC_READY, root not on disk | Native-only replay, the same EXEC_READY record, decision |
  | `barrier-synced` | 07-02 | EXEC_READY, root on disk, no intent | Replay (the root is not trusted), decision |
  | `intent-synced` | 07-03 | Durable intent, nothing written | Completes the saved intent |
  | `block-written` | 07-03 | Intent, block stored, head not moved | Completes it with the stored block |
  | `head-written` | 07-03 | Head and marker written, no NATIVE_DECIDED | `Resolve` from the marker |
  | `decided-unsynced-drop` | 07-03 | Marker durable, NATIVE_DECIDED lost | `Resolve` from the marker |
  | `native-rejection` | 07-06 | Intent, native bad-block record | `RejectIntent`; no marker, obligation or NATIVE_DECIDED |

  At the `intent-synced` restart the probe also checks two refusals: before
  the bridge opens, the EXEC_READY block is refused (`ErrDeferred`); and with
  the bridge open, block 2 is refused while the intent of block 1 is
  unresolved (`decision.ErrPending`).
- **re-import**: after every scenario, the node restarts and imports the
  blocks of the chain again. No observation and no decision is opened.

The driver checks the report and every trace again with its own rules, and
recomputes each digest from the README formulas: intent, NATIVE_DECIDED,
rejected intent, marker bytes, admission DECIDED, the evidence, and the
bad-block evidence.

`bsc_decision.mutants.json` lists the Go mutants. The Python mutants and the
portable controls are in `test/harness/fixtures/bsc_decision`.

## Limits

- The fixture has two empty blocks before Plato and one validator. So the
  justified and finalized blocks are the genesis, and no attestation,
  VotePool or validator-set path runs. The fail-closed reads below the window
  are proven by the unit tests, with a stand-in for Parlia.
- No `SIDE` decision occurs: the admission profile imports only on top of the
  head, where the fork choice always makes the block the head. The `SIDE`
  marker in the block batch is covered by source review only.
- The mismatch of a saved intent (`ErrTransition`), a bad block that is also
  stored, and a replay whose digests differ (`ErrReplay`) do not occur on the
  fixture. The first is covered by `TestUnitSameTransition`, the others by
  source review.
- The `skipBlock` patch is not exercised: in the probe, native `skipBlock`
  already executes the stored block again, because the chain has a snapshot
  tree without a layer for that block. The patch keeps that behavior for a
  chain where `skipBlock` would skip the block; it is covered by source
  review.
- The probe installs only the decision hook, so the refusal of an observed
  block without a decision step (`ErrExtractorDecision`, for example under
  the plain admission seam) is covered by source review.
- No store or native write error is injected. A failed `Resolve`, `Cleanup`,
  native batch write or sync after the intent leaves the intent unresolved,
  and recovery settles it; these error paths are covered by source review.
- Native chain data has process-crash semantics (D100): a SIGKILL may lose
  writes that pebble has not synced. The probe proves the outcomes in which
  the native writes before a crash point reached the disk, and in which they
  were lost (`intent-synced` is the outcome of a lost block batch).
- The mirror is a probe stand-in. E05-01.registry implements `Mirror`. The
  node wiring of the hook, its mirror and its recovery belongs to
  E01-02.lifecycle_seams.
