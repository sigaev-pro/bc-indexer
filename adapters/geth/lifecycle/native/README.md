# Geth lifecycle seams (E01-02.lifecycle_seams, Geth block)

This directory is Geth-only. It joins the shared lifecycle gate
([`../README.md`](../README.md), block 0) to the accepted Geth tree (Geth
`1df30b18d9ed35ee9d28ffd4648442c2204554ad`), on top of the Geth admission
wiring and the Geth decision bridge
([`../../decision/native/README.md`](../../decision/native/README.md)). It
routes every Geth row of `callers.json` through the gate and proves
AC-CAP-01-01, AC-LIF-05-02 and AC-LIF-05-03 for the private local ETH slot
only; every public cell stays NOT_RUN (D96, D98, D99).

| File | Installed as | Role |
|---|---|---|
| `patches.json` | applied after the admission and decision-bridge patch sets | 34 anchored text patches; each original must occur exactly once in the tree those patch sets produce |
| `core_lifecycle.go.txt` | `core/extractor_lifecycle.go` | The core seam (`core.ExtractorLifecycle`), the finality setters behind their choke point, the SetHead rewind flow |
| `lifecycle.go` | `extractor/admission/native/lifecycle.go` | `AttachLifecycle`, the gate passes, the CANONICAL and FINALITY decisions without a new block, their recovery, the startup reconciliation, `GoLive`, the profile check and the native-commit identity |
| `lifecycle_test.go` | `extractor/admission/native/lifecycle_test.go` | Unit tests of the pure parts (profile check, identity, evidence digests) |

The shared package `extractor/lifecycle` is installed byte-identical from
`adapters/geth/lifecycle`. The measured probe and its driver are in
[`test/harness/fixtures/lifecycle_geth`](../../../../test/harness/fixtures/lifecycle_geth).

## Node wiring (D100)

- `eth.New` on a capture node (`EXTRACTOR_ADMISSION_CONFIG` set): after
  `Preflight`, `CheckSyncMode` refuses any sync mode but full before the store
  is opened; after `NewBlockChain`, `AttachLifecycle` replaces
  `AttachDecisions` and the node registers the live service
  (`Lifecycle.Live(eth.Synced)`).
- `AttachLifecycle` checks the native-commit identity, attaches the admission
  (with input cleanup) and the decision seam, installs itself as the
  admission seam of the chain (so every guarded call passes through it) and
  as the lifecycle seam, opens the gate (`lifecycle.Open`, client `geth`) and
  runs the recovery driver.
- Phases. A first start is BOOTSTRAP: the initial native sync runs natively
  and uncaptured (`ProcessBlock` begins an observation only in an observing
  pass). The live service calls `GoLive` once `eth.Synced()` reports the end
  of the initial sync (the Engine API sets it at the first valid forkchoice).
  A restart is RECOVERY: the recovery driver runs, then `GoLive` keeps the
  stored H.
- `GoLive` takes the guard, reads the native head, flushes its state root
  through the hash trie database (`TrieDB().Commit`) and syncs, writes the
  lifecycle view of H, then lets the gate write and sync the boundary record
  before `decision.Bridge.GoLive`.
- `geth` (`cmd/geth/config.go`, `makeFullNode`): on a capture node,
  `CheckFlags` refuses `--dev`, `--synctarget` and a `--syncmode` other than
  full before the node is made. An unset `--syncmode` passes this stage;
  `eth.New` then checks the final sync mode, which a `--config` file may
  set. Geth has no mining, voting or builder role in the
  accepted tree; the only self-sealing path is the developer beacon.
- Native-commit identity (D100): `NativeIdentity` is the extractor build
  stamp `BinaryCommit` (`-ldflags -X
  github.com/ethereum/go-ethereum/extractor/admission/native.BinaryCommit=<commit>`)
  or, without it, the clean VCS revision that the Go toolchain recorded
  (`internal/version`). `AttachLifecycle` refuses a config whose
  `native_commit` differs, and a binary with neither (`ErrIdentity`); the
  capsule commit is therefore the binary's. A plain build of the patched
  tree has no clean VCS revision, so the release build of the shipped
  `geth` must pass the stamp; that build belongs to E01-01.builds and E06
  (D100). In this block only the probe binary is stamped.

## Gate use

Every guarded caller takes the observation guard first, then opens its pass
(`Enter`), then takes `chainmu`: the Geth lock order is guard, then
`chainmu` (NATIVE_CAPTURE_PROTOCOL). A nested native call opens its pass
with the same token. While the recovery driver runs, `Lifecycle.Lock` nests
each guarded call in a pass of `geth.recovery`, so the gate admits it in
RECOVERY; the decision recovery helper `ExtractorCompleteHead` takes the
guard too (patch `complete_head_guard`).

Choke points (`Require`): the state commit of every executed block write
(`writeBlockWithState`, EXECUTION); the canonical-hash rewrite (`reorg`), the
head writer (`writeHeadBlock`) and a requested rewind (`setHeadBeyondRoot`),
CANONICAL; the finalized and safe setters, FINALITY. A head write that fails
the check stops the node (`log.Crit`); the other choke points return the
error. Snap-sync writers and the chain reset are refused before they write.

Decisions without a new block:

- CANONICAL (`set-canonical`, `known-block`): the intent names, for every
  block of the branch of the new head above its common ancestor with the
  current head, the SIDE member that retains it (the known-branch corpus
  rule); a branch block without one is `ErrCorpus`, a source error before
  any mutation. The branch is found from the headers, not from the
  canonical-hash index, which can outlive a head rewind. The marker
  and the lifecycle view join the head batch of the new head, then the
  decision is resolved. Evidence: `CanonicalEvidence(caller, old, new,
  members)`.
- SetHead rewind (`set-head`): the guard and the pass are taken in `SetHead`
  and `SetHeadWithTimestamp`. The intent names the head that the native
  `rewindHead` of the target keeps; after the native rewind the head must be
  that head, and the marker and view are a batch of their own. Then, in the
  native order, the safe clear, the finalized clear and the safe reload of
  `loadLastState` are each one FINALITY decision (`ClearEvidence` for a
  clear, `FinalityDigest` otherwise), then `loadLastState` runs.
- FINALITY (`set-finalized`, `set-safe`): one decision per setter call, the
  old reference read under the guard from the field the setter writes. The
  finalized reference and its marker and view are one synced batch; safe
  lives in memory, so its marker and view are a batch of their own. In both
  cases the batch is synced before the in-memory reference moves, and a
  failed batch write is fatal, as the native finalized write is. An unchanged reference is rewritten without an intent or marker; a
  zero hash calls no setter (the Engine API skips it), so nothing is
  declared final (AC-LIF-05-02). A setter that ran stays decided when the
  forkchoice then fails (AC-LIF-05-03).
- A native error after the intent and before its batch, with the head
  unchanged, rejects the intent (`NotAppliedEvidence`); otherwise the intent
  stays unresolved and the next start settles it.

The lifecycle view record (`extractor-lifecycle-geth-v1/<producer>.<run>/view`,
JSON `{head, finalized, safe}`) is written in the batch of every decision
marker that moves the head or a finality reference, and at H. It holds the
decided references: while the recovery driver runs, every view write keeps
the stored finality references and changes only the head or the field being
decided, because a restart resets native safe to finalized.

## Recovery and startup reconciliation

The recovery driver is `Decisions.Recover` with three changes: an
unresolved lifecycle intent without a marker is settled first; after the
native-only replay the startup reconciliation runs; the gate goes live at
the stored H instead of `decision.Bridge.GoLive`.

- A lifecycle intent (any FINALITY intent, or a CANONICAL intent whose
  evidence is not the write evidence of its first member): a FINALITY intent
  is rejected, because its marker is in the same synced batch as the
  finalized write and safe lives in memory; a CANONICAL intent is completed
  (marker and view written, resolved) when the native head is its new head,
  rejected when the native head is its old head, completed through
  `ExtractorCompleteHead` from its old head when a crash inside the
  reorganization left a block strictly on the new branch (below the new
  head, above the fork point with the old head) as the head, and otherwise
  stops startup (`ErrLifecycle`): a head that native startup repair lowered
  to the shared chain is never moved forward here.
- On a restart the lifecycle view must be readable before the driver
  writes anything (`ErrCorrupt` otherwise).
- Startup reconciliation (`geth.startup-reconcile`): native startup repair
  runs before the gate opens, so the driver compares the native head and
  finality references with the view. A head that differs and is an ancestor
  of the view head (a native startup rewind) becomes one CANONICAL decision;
  a head below H, or one that is not an ancestor, is a source invariant
  error. Each changed finality reference becomes one FINALITY decision; a
  restart sets safe to finalized, so this runs on most restarts.

## Caller routes

Every Geth row of `callers.json`, checked against the accepted tree.

| Caller | Route | How it passes the gate here | Exercised by the probe |
|---|---|---|---|
| `geth.import` | OBSERVATION | `InsertChain` takes the guard and enters; `insertChain` begins observations in an observing pass | bootstrap, side-chain re-import, every restart |
| `geth.insert-without-head` | OBSERVATION | `InsertBlockWithoutSetHead` enters | Engine API `newPayload` |
| `geth.recover-ancestors` | OBSERVATION | `recoverAncestors` enters, nested; each re-execution is a SIDE write (D100) | `InsertBlockWithoutSetHead` on the pruned BOOTSTRAP side block S3 |
| `geth.recovery` | OBSERVATION | `Lifecycle.Lock` nests every guarded call of the recovery driver in its pass | each restart |
| `geth.side-chain` | OBSERVATION | `insertSideChain` enters, nested; its stateless `writeBlockWithoutState` reaches no choke point; the re-import of the pruned ancestors (the internal `insertChain`) runs nested in this pass, whose map effects include EXECUTION and CANONICAL | `InsertChain` of T4 on the pruned T3 |
| `geth.known-block` | DECISION (CANONICAL) | `writeKnownBlock` enters and makes the CANONICAL decision | `InsertChain` of the stored side blocks C3 C4 |
| `geth.set-canonical` | DECISION (CANONICAL) | `SetCanonical` enters before `recoverAncestors`; CANONICAL decision before the reorg | Engine API forkchoice |
| `geth.set-finalized` | DECISION (FINALITY) | `SetFinalized` takes the guard, enters, one decision per call | Engine API forkchoice |
| `geth.set-safe` | DECISION (FINALITY) | `SetSafe` likewise | Engine API forkchoice |
| `geth.set-head` | DECISION (CANONICAL, FINALITY) | `SetHead` and `SetHeadWithTimestamp` take the guard and enter | `SetHead(3)` |
| `geth.startup-reconcile` | DECISION (CANONICAL, FINALITY) | the reconciliation of the recovery driver | the safe reset of the restart |
| `geth.dev-sealing` | REFUSE | `CheckFlags` refuses `--dev`; `NewSimulatedBeacon` is refused through the gate | both |
| `geth.sync-target` | REFUSE | `CheckFlags` refuses `--synctarget`; `Syncer.Sync` (the Sync API) is refused through the gate | both |
| `geth.snap-sync` | REFUSE | `CheckFlags` and `eth.New` refuse snap sync; `InsertReceiptChain`, `SnapSyncStart`, `SnapSyncComplete`, `InsertHeaderChain` and `InsertHeadersBeforeCutoff` are refused through the gate | `eth.New` with snap sync; `InsertReceiptChain` |
| `geth.parallel` | REFUSE | `process_block_begin` disables the parallel processor for an observed import; the refusal is recorded there | every observed import |
| `geth.manual-commands` | REFUSE | the merged writable-database and removedb guards (own process, no gate) | `RequireNoOpenCapture` on the closed store |
| `geth.freezer` | READ_ONLY | moves canonical data only; `freezer_limit` keeps pinned forks. It freezes up to the finalized block, so a branch below finality cannot become canonical | no pass needed |
| `geth.stateless-self-check` | READ_ONLY | a second in-memory execution, never writes | no pass needed |
| `geth.prefetch` | READ_ONLY | throwaway StateDB in `ProcessBlock`, no choke point | runs in every import |
| `geth.eth-call` | READ_ONLY | `applyMessageWithEVM` on a state copy | source review |
| `geth.execution-witness` | READ_ONLY | `ProcessBlock` with `WriteState` false: no observation, no state commit | source review |
| `geth.payload-build` | READ_ONLY | `generateWork` on a state copy; a built block reaches the chain only through `newPayload` | source review |
| `geth.replay-bad-block` | READ_ONLY | re-executes without writing | source review |
| `geth.simulate` | READ_ONLY | `eth_simulateV1` on a state copy | source review |
| `geth.stateless-payload` | READ_ONLY | executes against a witness, no chain write | source review |
| `geth.trace-state` | READ_ONLY | ephemeral trie database | source review |

A READ_ONLY caller that ever reached a choke point would fail it with no
open pass (`ErrBypass`), so a missed write cannot pass silently.

## Limits

- Only the hash state scheme is supported (as in the decision bridge).
- A head change to a branch whose block has no retained corpus is a source
  error (`ErrCorpus`); the targeted native replay is not implemented. For a
  block stored during BOOTSTRAP there are no inputs to replay from. For a
  block whose decision closed its observation, the close purged its capsule
  and log and released its pin (decision bridge with input cleanup, D100), so
  the retained inputs that the protocol's replay needs are gone; the
  protocol then allows the source error. The effect on a live node is that a
  reorganization back to a branch it left (A, then B, then A again) stops
  the node from following that branch: it fails closed and keeps running.
  The targeted replay belongs to E05-02.reorg: D104 (PR from the #18 BSC
  block), which the coordinator applied to the Geth path on 2026-11-13;
  keeping the inputs of decided canonical blocks until the irreversible
  anchor may be part of that fix.
- A SetHead rewind interrupted between its intent and its marker with the
  native head between the old and the new head stops the next start
  (`ErrLifecycle`); completing a partial rewind belongs to
  E01-04.early_faults (coordinator technical decision, 2026-11-13). The same
  holds for a crash inside a lifecycle CANONICAL reorganization after which
  native startup repair lowers the head to or below the fork point: the next
  start stops (`ErrLifecycle`, crash scenario `repaired`); E01-04.early_faults
  as well.
- A refused finality setter call writes nothing and logs the error; the
  Engine API response still reports the forkchoice as valid, because the
  native setters return no error. The gate is stopped after a bypass, so the
  node imports nothing more.
- If `GoLive` fails on a first start, the live service stops the node
  (`log.Crit`) rather than let it keep importing uncaptured.
- `SetCanonical` of the current head rewrites the head batch natively and is
  decided as a CANONICAL decision from the head to itself.
- A runtime chain reset (`ResetWithGenesisBlock`) is refused on a capture
  store.
- Typed finality observations, the GC anchor and the regression rule
  (AC-LIF-05-01, 05-04) are E05-01.eth_finality; the mirror and the release
  of retained side blocks are E05-01.registry and E05-01.activation_gc.
