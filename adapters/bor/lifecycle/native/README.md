# Bor block of E01-02.lifecycle_seams

This directory is the Bor-only part of `adapters/bor/lifecycle`. The shared
gate is described in [../README.md](../README.md). This block joins that gate
to every Bor caller of `callers.json`, wires the capture node into
`bor server`, and proves the result with a measured run on the private POLY
fixture.

The plan rows it implements are in PROJECT_STATUS ("E01-02.lifecycle_seams",
plan for the per-client blocks, every row marked Bor or each client block).

## Layout

| Path | Installed in the Bor tree at | Content |
|---|---|---|
| `borlifecycle/` | `extractor/borlifecycle` | Glue: the gate inside the decision recovery driver, the lent guard token, the DECISION intents and markers, the finality hook, the startup reconcile, the outside refusals |
| `borlifecycle/profile/` | `extractor/borlifecycle/profile` | The `bor server` capture profile check, each refusal named by its REFUSE caller |
| `borlifecycle/wire/` | `extractor/borlifecycle/wire` | `--extractor.capture` wiring of `bor server` and the manual-command refusal; imported by the shipped main |
| `tree/` | the same relative path | New native files (hooks of `core`, `core/rawdb`, `eth`, the whitelist, `internal/cli`, the server, and the decision bridge) |
| `patches.json` | (applied after the admission and decision patch sets) | 47 anchored edits |
| `lifecycle_tree.py` | | Installer: `bor_admission_tree.install` (unchanged), then the shared `lifecycle` package, the glue, the new files and the edits |
| `probe/main.go` | `cmd/extractor-bor-lifecycle-probe` | Measured probe (test only) |
| `lifecycle_bor.py` | | Driver: `prepare`, `run`, `observe`, `vectors`, `derive` |
| `test_lifecycle_bor.py`, `lifecycle_bor.mutants.json`, `sample/` | | 28 portable controls on development outputs (`sample/`, not measured) and 71 single-check mutants |

The installer adds a third patch set after the admission and decision patch
sets, so it does not change their files. An anchor may sit in text that an
earlier set inserted (`core/zz_extractor_decision.go`), or in the installed
`extractor/bordecision/bordecision.go`. Every anchor must occur exactly once,
and an edit name must be unique across the three sets.

The shared installer `test/harness/bor_admission_tree.py` stays unchanged. A
product build that wants the lifecycle seams calls `lifecycle_tree.install`.

## Guard and lock order

The admission's `Driver.Observe` and the decision bridge's live decider take
the observation guard themselves, and the guard is not reentrant. So no pass
may hold the guard across a nested import. The block uses three rules:

- **Lent token.** A pass that a chain-lock holder opens (`Enter(id, chain)`
  with `chain` true) stores its token as `held`. `held` is set and read only
  with `chainmu` held, so only that goroutine can see it. Nested calls of that
  goroutine reuse it: the import pass, the live decider (edit
  `lifecycle-live-acquire`), and the recovery imports.
- **Import pass.** The decision path of one executed block
  (`extractorWriteDecided`) is one `bor.import` pass. It covers the child-root
  barrier, the intent, the native write with the marker, and the resolve.
  The observation window before it (Observe to Done) takes the guard itself
  and writes nothing natively.
- **Via label.** `recoverAncestors` and `insertSideChain` hold no pass. They
  label their nested imports, which enter as `bor.recover-ancestors` or
  `bor.side-chain`.

Lock order, as the shared README states for Bor:

- **chainmu, then the guard, then the whitelist finality lock** on every
  import and every chain-lock caller. `SetHead`, `SetCanonical`,
  `writeKnownBlock` and the milestone reorganization take the guard right
  after `chainmu`.
- **Whitelist callers** take the guard before the whitelist finality lock and
  never under `chainmu`.
- **`SetCanonical`** takes the guard only after its re-execution of a pruned
  branch, whose imports take the guard themselves.

The measured `deadlock-check` step runs the import path (chainmu, guard, then
the whitelist read lock in `ValidateReorg`) concurrently with whitelist
callers (guard, then the whitelist write lock). A cycle would stop both, and
the step fails at its deadline.

## Callers

| Caller | Route | How the block routes it |
|---|---|---|
| `bor.import` | OBSERVATION | The import pass (above) |
| `bor.recover-ancestors` | OBSERVATION | Via label; its blocks are decided `SIDE` |
| `bor.side-chain` | OBSERVATION | Via label; the re-import of the stateless blocks is decided by the import path |
| `bor.recovery` | OBSERVATION | The gate opens inside `bordecision.Start`, with the guard and `chainmu` held (edit `lifecycle-recovery-open`). The `bor.recovery` pass covers the decision recovery; the recovery imports nest in it |
| `bor.known-block` | DECISION | `writeKnownBlock` is one CANONICAL decision. Members are the blocks of the new branch above the common ancestor, each bound to its retained EXEC_READY observation. A block without one refuses the intent: the known-branch rule ends in a source error, never a silent activation, and no replay is made |
| `bor.set-canonical` | DECISION | One CANONICAL decision after the pruned-branch re-execution |
| `bor.set-head` | DECISION | One CANONICAL decision for the predicted head. Before any intent, a rewind is refused when the target is below H or has no state (native code would rewind further than the intent). The canonical part is decided before `setHeadBeyondRoot` clears a finality reference, so a cleared finalized hash on a standalone set-head is a FINALITY decision of its own; inside the milestone reorganization the clear fails closed (see there) |
| `bor.milestone-reorg` | DECISION | See "Milestone reorganization" |
| `bor.milestone`, `bor.checkpoint`, `bor.milestone-lock` | DECISION | Each patched whitelist method opens its pass before the finality lock (the vote keeps it from `LockMutex` to `UnlockMutex`). Each native finality write is a FINALITY decision of its own (see "Write choke points") |
| `bor.startup-reconcile` | DECISION | See "Startup reconcile" |
| `bor.seal-write`, `bor.engine-api`, `bor.snap-sync`, `bor.stateless` | REFUSE | `WriteBlockAndSetHead`, `WriteBlockAndSetHeadPipelined`, `InsertBlockWithoutSetHead`, `InsertReceiptChain` and `InsertChainStateless` enter the gate, which refuses and counts them. The profile check refuses their roles too |
| `bor.parallel`, `bor.pipelined`, `bor.preconf` | REFUSE | The profile check refuses them. Their native entries refuse without the guard (`RefuseOutside`), because an observation may already hold it there |
| `bor.manual-commands` | REFUSE | `removedb`, `purge-whitelisted-entries` and `backfill-statesync-txs` (which rewrites StateSync receipts and lookups) are refused on a store with a live pin or any decision record (no mirror until E05-01.registry, so every decision record keeps an obligation or a retained member) |
| `bor.prune-commands` | REFUSE (GUARDED) | The admission's retention guard refuses; the probe counts it |
| READ_ONLY rows | READ_ONLY | No pass; unchanged |

**Known-block reachability.** On a capture node the native entries do not
reach `writeKnownBlock`, for two reasons:

- the admission inserts one block per batch;
- the native left-trim of `insertChain` replaces the known-block error with
  the fork-choice result.

So a stored side block that needs a reorganization is executed again under a
new observation and decided by the import path. That path is captured. The
probe exercises the patched `writeKnownBlock` through
`core.ExtractorWriteKnownBlock`, which holds `chainmu` as `insertChain` does.

## Write choke points

| Write | Hook | Check |
|---|---|---|
| `WriteCanonicalHash`, `WriteHeadHeaderHash`, `WriteHeadBlockHash`, `WriteHeadFastBlockHash` | `rawdb.ExtractorRequire` | `Require(CANONICAL)` |
| `WriteFinalizedBlockHash`, `WriteLastFinality`, `DeleteLastFinality`, `WriteLockField`, `WriteFutureMilestoneList` | `rawdb.ExtractorFinality` | One FINALITY intent in the innermost pass before the write, with the evidence of the key and its old and new value. `Require(FINALITY)` is then checked. After the write: the value check, the marker, the sync and the resolve |
| The state commit of the decision path | `extractorRequire("EXECUTION")` | `Require(EXECUTION)` |

A refused write stops the process (`log.Crit`), so the native write fails
closed.

The finality hook records its intent in the innermost pass, which belongs to
the guard holder. Source review of the pinned tree found every native
finality writer on that goroutine:

- the whitelist writers (`WriteLastFinality`, `DeleteLastFinality`,
  `WriteLockField`, `WriteFutureMilestoneList`) are called only from the
  patched milestone and checkpoint methods, inside their own pass;
- `SetFinalized` (`WriteFinalizedBlockHash`) is called only by the set-head
  clear, inside the set-head pass, and by the Engine API, which `bor server`
  does not register (`bor.engine-api` is refused);
- `NewService` writes the lock record at node start, before the hooks are
  installed.

A writer on another goroutine while a pass is open would record its intent in
the wrong pass; none exists in the pinned tree. The rawdb hooks are process-wide. One capture chain per process is
supported, as the admission's Heimdall transport already requires.

Bor's `SetSafe` writes no native state (memory only), so a cleared safe block
is no decision.

## Lifecycle decisions and their markers

A DECISION pass proceeds in this order:

1. It syncs an evidence record under
   `extractor-lifecycle-bor-v1/<producer>.<run>/evidence/<digest>`.
2. It records the intent through `Pass.Intend`. CANONICAL intents carry
   public events; FINALITY intents do not. A finality write in a pass whose
   CANONICAL intent is still open is refused (`ErrOrder`).
3. The native mutation runs.
4. It checks the native result against the intent: the head, or the
   finality value.
5. It writes the marker in a batch of its own and syncs, then `Resolve`s,
   deletes the evidence record and `Cleanup`s. When a crash falls between
   the synced marker and the resolve, the decision recovery driver resolves
   the intent and the repair step deletes its evidence record.

The marker is not in the final native batch. A native rewind spans many
batches, and the whitelist writes single keys. Instead, the startup
reconcile settles an unresolved lifecycle intent without a marker from
native evidence. An intent with its marker is resolved by the decision
recovery driver, as on the import path.

## Startup reconcile

`bor.startup-reconcile` has two parts, both inside the recovery pass:

1. **Settle**, before the decision recovery driver. An unresolved intent
   without a marker whose evidence record exists is a lifecycle intent:
   - native state equal to its new value: the marker is written and the
     intent is resolved;
   - native state equal to its old value: `RejectIntent`;
   - a CANONICAL intent whose native head lies on the path the mutation
     walks (native rewinds and reorganizations move the head in steps, one
     batch per block): the head is on the old chain above the new head, or
     on the new branch below it. The mutation is completed natively inside
     the recovery pass (`ExtractorCompleteCanonical`: the rewind, or
     `SetCanonical` of the new head), with the saved intent covering the
     nested callers, and the intent is then decided;
   - anything else: a source invariant error.

   An intent with a marker, or without such a record, is left to the
   decision recovery driver, which resolves a marked intent and completes an
   import-path intent.
2. **Repair**, after the decision recovery driver, when no intent is
   unresolved (otherwise a source invariant error). It first deletes every
   evidence record left (the decision recovery driver resolved those intents
   from their markers). A native head that
   differs from the last decided head is native startup repair made before
   the gate opened. It becomes a CANONICAL decision from the last decided
   head to the native head. A native head below H is a source invariant
   error.

The reconcile compares the head only. Bor's native finalized hash is set
only by the refused Engine API, and the whitelist `NewService` rewrites the
lock record at startup only when it is corrupt; neither startup finality
write is decided here. This is a limit of this block (see "Limits").

## Node wiring and H

`bor server --extractor.capture FILE` installs the seams:

- After `buildEth` and before `eth.New`, `profile.Check` runs, then the store
  preflight. A refusal stops the server before the store opens.
- After `eth.New` and before `node.Start`, `wire.Start` runs: `borimport.Start`,
  `borlifecycle.Enable`, `bordecision.Start` (gate, recovery, GoLive), then the
  live observer.

The probe starts its capture node with the same `wire.Start`.

A plain `bor server` (no `--extractor.capture`) is refused on a store with a
live pin or a decision record, by the same check as the manual commands, so
a capture store cannot run without the gate.

**H.** `GoLive` runs in the same guard hold as the bridge's GoLive, after the
repair and the end of the recovery pass, with the native head read under
the guard and its state root durable. A restart keeps the stored H.

On Bor there is no BOOTSTRAP window. The decision block's recovery driver
(`bordecision.Start`) switches the admission and the bridge to LIVE at the
first start, and the store preflight refuses a store that a native node
filled without the capture binding. So the first capture start is on a new
store, H is its genesis, and the initial sync is captured block by block.
D100 reads H "after the initial native sync"; a Bor BOOTSTRAP window is a
scope question raised with the coordinator (see the evidence). The rewind
refusal below H is therefore a guard that the measured run cannot reach.

## Milestone reorganization

Native `reorgToFinalized` rewinds, then inserts and activates the canonical
chain, each step taking `chainmu` itself. The native rewind deletes every
block above `rewindTo`, side blocks too, so the canonical chain is executed
again.

On a capture chain, `extractorReorgToFinalized` runs the whole sequence under
one `chainmu` hold, so no other caller runs between the steps:

1. The rewind is one CANONICAL decision of `bor.milestone-reorg`. The nested
   set-head is covered by it.
2. The guard is released.
3. The blocks are imported through the import path, each with its own
   observation and decision.
4. `SetCanonical` of the last block is a `bor.set-canonical` decision.

The re-execution cannot sit inside one decision, because the observations
take the guard. As natively, the call reports a moved head once the decided
rewind ran, even if the insert after it fails, so the verifier still purges
the stale milestones; the insert passes the node's witness setting. A
finalized reference above `rewindTo` would be cleared inside the covered
set-head while the rewind intent is open; that write fails closed. It cannot
happen on `bor server`, whose finalized hash stays zero.

## Fixture

The fixture is the accepted Bor test genesis (chain 15001) with three
changes:

- test key 1 is funded, for the transfers that give D4 and E4 their own state
  roots;
- `stateSyncConfirmationDelay` is `{"0": 0}`, as in the admission and corpus
  fixtures;
- the Heimdall stub of D98 item 6 serves Heimdall.

The generator seals 17 blocks with native Prepare, FinalizeAndAssemble and
Seal. At an equal total difficulty the native fork choice keeps the head by
hash order, so the generator raises a block's time offset until C4 and E4 hash
below the sibling they must not replace.

## Measured run

Each step is one probe process in the pinned runtime image, under the host
lock:

| Step | Process | Proves |
|---|---|---|
| 1 | `generate` | The fixture blocks |
| 2 | `scenario` | First start (BOOTSTRAP, then LIVE with H at the genesis), then every caller through its native entry, except known-block and the milestone reorganization, which run through the test-only hooks (see "Known-block reachability" and "Milestone reorganization"): imports A1 to A5; side blocks; `SetCanonical(A2)`; the refused activation of A3, whose observation was closed (known-branch corpus rule); known blocks B3 and B4; `SetCanonical(C4)`; `SetCanonical` of the stateless D4 (recover-ancestors); E5 on the stateless E4 (side chain); the milestone reorganization to F4; the whitelist calls; the four refused entries; the deadlock check; `SetHead(7)` |
| 3 | `restart` | RECOVERY, then LIVE with H kept and nothing to settle |
| 4, 5 | `crash`, `restart` (settle) | A child runs `SetHead(6)` and exits after the native rewind, before the marker; the restart settles the intent as decided from native evidence |
| 6, 7 | `crash`, `restart` (resolved) | A child runs `SetHead(5)` and exits after the synced marker, before `Resolve`; the decision recovery driver resolves it |
| 8, 9 | `crash`, `restart` (imported) | A child imports H6 and exits after the synced head batch with the marker, before `Resolve`; the decision recovery driver resolves it |
| 10 | `restart` (advance) | Imports H7 and H8 again |
| 11, 12 | `crash`, `restart` (completed) | A child runs `SetHead(5)` from H8 and exits at the fourth canonical write, after the first rewind step's batch reached the disk; the restart finds the head between H8 and H5, completes the rewind and decides it |
| 13, 14 | `fault`, `restart` (repair) | The state root node of the head is deleted offline; native startup repair rewinds and the startup reconcile decides it |
| 15 | `manual` | `removedb`, `snapshot prune-state`, `purge-whitelisted-entries` and `backfill-statesync-txs`, each in its own process (the native commands leave their node open), are refused by the capture-store guard or the retention guard, by message; the store stays |
| 16 | `profile` | Each forbidden `bor server` setting is refused with its caller. `NewServer` with `--extractor.capture --mine` stops before `eth.New`; `NewServer` without the flag is refused on the stopped capture store |
| 17 | `native-caller` | A second store imports A1 to A5 through `admin_importChain` |
| 18 to 20 | `restart` (stateless), `fault`, `restart` (refuse) | A third store imports A1, A2, B3, D4 and D5; D4's state root is deleted offline; `SetHead(4)` is refused before any intent and the head and decisions stay |
| 21 | `gaps` | `lifecycle.Gaps` over the summed coverage |

The driver then judges every output (`judge`) and writes the AC matrix of
the POLY slot:

- **AC-CAP-01-01:** PASS only when every check holds and both the Go and the
  Python gaps are empty.
- **AC-LIF-05-02, 05-03, 06-04 and 06-05:** NOT_APPLICABLE on POLY (ETH and
  BSC cases).
- **Polygon Mainnet and Amoy:** NOT_RUN (D96).

`observe DIR --output NEW` judges a retained run again without Docker.

## Digests

The evidence digest is SHA-256 of the tag `extractor-bor-lifecycle-evidence-v1`,
one zero byte, then each field as its 8-byte big-endian length and its bytes.
The fields, in order:

- caller, kind, operation;
- old head number (decimal) and hash, new head number and hash;
- for a finality write: the native key (hex), and the SHA-256 of the old and
  new value (empty for none).

A rejection evidence uses the tag `extractor-bor-lifecycle-rejection-v1` over
the intent digest and the native value. `python3 lifecycle_bor.py vectors`
computes the values of the Go test vectors independently.

## Limits

- **Scope.** Private local POLY only, on the private chain with one validator.
  Polygon Mainnet and Amoy are NOT_RUN.
- **Not run.** The parallel, pipelined and preconf entries are refused by the
  profile check. Their in-code refusals are not run, because the capture
  profile never builds those paths.
- **Fault model.** The startup-repair fault deletes the head state root
  offline, a process crash model. It is not a power-loss test (that is
  E01-04.early_faults).
- **Startup finality.** The startup reconcile compares the head only (see
  "Startup reconcile"); a native finality write made at startup before the
  gate opens is not decided. Owner: E05-01.polygon_observer with the typed
  finality evidence.
- **Last decided head.** The repair step takes the last decided head from
  the decisions the bridge still holds. With no mirror (until
  E05-01.registry) every head-moving decision keeps its marker and stays
  there. Once a mirror lets `Cleanup` drop applied decisions, the repair
  step needs a persisted last decided head. Owner: E05-01.registry.
- **H on Bor.** H is the genesis of a new capture store; the below-H refusal
  is not reached by the measured run.
- **Test-only seams.** `core.ExtractorWriteKnownBlock`,
  `eth.ExtractorReorgToFinalized` and `Node.Step` exist for the probe; no
  product path calls them.
- **Not this block.** Typed finality observations are E05-01. The retained
  observations of side blocks that a rewind deleted are released by
  E05-01.activation_gc.
