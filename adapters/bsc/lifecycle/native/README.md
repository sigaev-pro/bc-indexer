# BSC lifecycle seams (E01-02.lifecycle_seams, BSC block)

This directory is BSC-only. It joins the shared lifecycle gate
([`../README.md`](../README.md), block 0) to the accepted BSC tree (BSC
`54afaff4da31ac9e49ba646a4f65104edac5a2f3`), on top of the BSC admission seam
([`../../admission/native/README.md`](../../admission/native/README.md)) and
the BSC decision bridge
([`../../decision/native/README.md`](../../decision/native/README.md)). It
routes every BSC row of `callers.json` through the gate and proves
AC-CAP-01-01, AC-LIF-06-04 and AC-LIF-06-05 for the private local BSC slot
only; every public cell stays NOT_RUN (D96, D98, D99). Two rows are deferred
under D104 (see "Deferred rows").

| Path | Installed as | Role |
|---|---|---|
| `patches/08-core-lifecycle-seams.diff` | applied after the admission patches 01 to 06 and the decision patch 07 | The core seam (`core/extractor_lifecycle.go`), the gate passes and choke points in `core/blockchain.go`, the decision step changes, the Parlia attestation read, the node wiring, the mining refusal and the manual database guard |
| `go/*.go` | `extractor/bsc/lifecycle` (package `bsclifecycle`) | The lifecycle hook, the decisions without a new block, the recovery driver, the startup reconciliation, `GoLive`, the attestation read, the node config and profile check; `unit_test.go` (pure parts) and `probe_test.go` (the measured probe) |

The shared package `extractor/lifecycle` is installed byte-identical from
`adapters/bsc/lifecycle`. The driver, its portable tests, its mutants and a
development sample are in
[`test/harness/fixtures/lifecycle_bsc`](../../../../test/harness/fixtures/lifecycle_bsc/README.md).

## Node wiring (D100)

- `eth.New` on a capture node (the config file named by
  `EXTRACTOR_BSC_CONFIG`: store ID, producer and run, native commit, genesis
  file): before the store is opened, `CheckProfile` refuses what the capture
  profile forbids. It runs the state profile check of E01-02.pins (hash
  scheme, full garbage collection, no block-data pruning), then refuses any
  sync mode but full (`bsc.snap-sync`), voting and the MEV builder role
  (`bsc.sealing`).
- Parlia is built with the system-call API (`bsc.NewSystemCallAPI`) instead
  of the RPC backend, before `CreateConsensusEngine`
  (`bsc.system-call-api`). After `NewBlockChain`, `Attach` binds it to the
  chain and the seam. It then starts the admission seam with the input
  cleanup of D100 and the decision bridge (`bscdecision.Start`), installs the
  lifecycle as the import hook and the lifecycle seam of the chain, opens the
  gate (`lifecycle.Open`, client `bsc`) and runs the recovery driver. The
  node registers the live service (`Lifecycle.Live(eth.Synced)`) after the
  backend, so a node stop stops the service before the chain and the store
  close.
- `Ethereum.StartMining` is refused through the gate (`bsc.sealing`). The
  native commit stays the config value: D100 gives the binary identity to the
  Geth block only.
- Phases. A first start is BOOTSTRAP: imports run natively and uncaptured.
  `Begin` returns no observation in a pass that does not observe, and the
  execution check of patch 01 lets it run. The live service calls `GoLive`
  once `eth.Synced()` reports the end of the initial sync. A restart is
  RECOVERY: the recovery driver runs, then `GoLive` keeps the stored H.
- `GoLive` takes the guard, reads the native head, flushes its state root
  through the hash trie database and syncs, marks the root synced in its own
  `Roots`, and writes the lifecycle view of H. Then the gate writes and syncs
  the boundary record before `decision.Bridge.GoLive`.

## Gate use

The lock order is guard, then `chainmu` (NATIVE_CAPTURE_PROTOCOL). Every
guarded caller takes the observation guard first (`hook.Acquire`), then
opens its pass (`Enter`), then takes `chainmu`; a nested native call opens
its pass with the same token. While the recovery driver runs,
`Lifecycle.Acquire` nests each guarded call in a pass of `bsc.recovery`, so
the gate admits it in RECOVERY.

Choke points (`Require`):
- EXECUTION: the state commit of every native block write
  (`writeBlockWithState`) and of the decision step (before the child-root
  barrier).
- CANONICAL: the canonical-hash rewrite (`reorg`), the head writer
  (`writeHeadBlock`), a requested rewind (`setHeadBeyondRoot`, not the startup
  repair) and the shutdown head rewrite.
- FINALITY: the finalized setter (`SetFinalized`).

A head write that fails its check stops the node (`log.Crit`). A refused
`SetFinalized` writes nothing and logs the error (the native setter returns
none). The other choke points return the error. Because the guard
serializes the patched callers, a write with no pass open, or under a pass
of another effect, stops the gate (`ErrBypass`).

Decisions without a new block:

- SetHead rewind (`bsc.set-head`): `SetHead` and `SetHeadWithTimestamp` take
  the guard and enter. The CANONICAL intent names the head that the native
  `rewindHead` of the target keeps. After the native rewind, the head must be
  that head; its marker and the view are a synced batch of their own, then
  the decision is resolved. A finalized clear
  (`SetFinalized(nil)`) is its own FINALITY decision (`ClearEvidence`), with
  the reference and its marker in one synced batch. Then `loadLastState`
  runs.
- Shutdown (`bsc.shutdown`): with the hash scheme, `Stop` commits recent
  states and rewrites the head block hash. With a seam, that rewrite is one
  CANONICAL decision of the head to itself, with the marker and view in the
  batch of the head hash. It takes the guard after the chain mutex is closed,
  so no import holds it. A rewrite to a lower block (the commit of the head
  state failed) is skipped; the next start repairs and reconciles the head.
- Startup reconciliation (`bsc.startup-reconcile`): see below.
- A native error after an intent and before its batch, with the head
  unchanged, rejects the intent (`NotAppliedEvidence`). The probe shows this
  with a SetHead that the pin ledger refuses. Otherwise the intent stays
  unresolved, and the next start settles it. After the native rewind,
  `loadLastState` runs on every exit, so an error never leaves the in-memory
  markers half-reloaded.

Evidence digests: SHA-256 of the tag, a zero byte, then the fields; strings
as `u64(len) || bytes`, heads as `u64(number) || hash`.

| Digest | Tag | Fields |
|---|---|---|
| CANONICAL | `extractor-bsc-lifecycle-canonical-v1` | caller, old head, new head |
| finalized clear | `extractor-bsc-lifecycle-clear-v1` | field, caller, old reference |
| not applied | `extractor-bsc-lifecycle-not-applied-v1` | intent digest (as a string), native head hash |

The lifecycle view record (`extractor-lifecycle-bsc-v1/<producer>.<run>/view`,
JSON `{head, finalized}`) holds the decided head and finalized block. It
joins the head batch of every decided import, of the decision bridge
(`writeHeadBlock`, with the finalized block of the decision evidence), and
the batch of every lifecycle decision marker, and it is written at H.

## Import decision: attestation read and events

- The decision step of an observed block first reads the Parlia snapshot
  attestation of the block through `lifecycle.ReadAttestation`. It uses the
  evidence reader of the decision bridge (the capsule window, the block and
  the genesis) and a new Parlia method, `ExtractorAttestation`, that returns
  the snapshot lookup error instead of turning it into genesis. A correct
  snapshot without an attestation is GENESIS_FALLBACK with the real genesis
  hash (AC-LIF-06-04). A lookup error is `ErrSource` before any write: the
  observation stays EXEC_READY without a decision, and a restart replays it
  (AC-LIF-06-05). The decision evidence of the bridge then reads the native
  `GetJustifiedNumberAndHash` and `GetFinalizedHeader`, unchanged; its digest
  binds the same justified and finalized blocks.
- D101: the decided canonical write now also sends the verified-block feed
  (`highestVerifiedBlockFeed`), which native `writeBlockAndSetHead` sends for
  a canonical block. With it, every decided import sends the same events as
  a native import of the same block (chain, chain head, block, verified
  block, logs, finalized header). A decided SIDE write would send what native
  sends for a side block (only the block feed of `insertChain`). It cannot
  run on BSC, because the seam observes only blocks on top of the head, so
  the fork choice always makes them the head.

## Recovery and startup reconciliation

The recovery driver, inside the gate:

1. An unresolved lifecycle intent (CANONICAL or FINALITY; BSC block writes
   make only IMPORT and SIDE intents) without a marker is settled first from
   native evidence. The decision recovery driver would refuse it. The
   lifecycle opens the bridge once for this; the driver opens it again.
   - A FINALITY intent is rejected: its marker is in the same synced batch as
     the finalized write.
   - A CANONICAL intent is completed (marker and view written, then
     resolved) when the native head is its new head.
   - It is rejected when the native head is its old head.
   - Otherwise startup stops (`ErrLifecycle`).
2. The decision recovery driver (`bscdecision.Hook.Recover`). Its re-imports
   run nested in `bsc.recovery` passes.
3. On a restart, the startup reconciliation. Native startup repair runs
   before the gate opens, so the driver compares the native head and
   finalized block with the view.
   - A head that differs and is an ancestor of the view head (a native
     startup rewind) becomes one CANONICAL decision.
   - A head below H, or one that is not an ancestor, is a source invariant
     error.
   - A changed finalized block becomes one FINALITY decision.
4. `GoLive` at the stored H.

## Caller routes

Every BSC row of `callers.json`, checked against the patched tree.

| Caller | Route | How it passes the gate here | Probe |
|---|---|---|---|
| `bsc.import` | OBSERVATION | `InsertChain` takes the guard and enters; `Begin` observes in an observing pass | every import, both paths |
| `bsc.recovery` | OBSERVATION | `Lifecycle.Acquire` nests every guarded call of the recovery driver | every restart |
| `bsc.side-chain` | OBSERVATION | `insertSideChain` enters, and its re-import of pruned ancestors runs in a nested import pass (source review: no probe step reaches it). In LIVE, `Begin` refuses every block whose parent is not the head with its state, first (D104) | `import S4` on the pruned S3: `ErrImportProfile` |
| `bsc.known-block` | DECISION (CANONICAL) | where the native fork choice activates a stored block (the known-block loop of `insertChain`, and `writeKnownBlock`), the pass enters and stops with `ErrCorpus` (D104) | `InsertChain S3`: `ErrCorpus`, head unchanged |
| `bsc.set-head` | DECISION (CANONICAL, FINALITY) | `SetHead` and `SetHeadWithTimestamp` take the guard and enter | `setHead 4`, BOOTSTRAP `setHead 2`, deadlock smoke, crash points |
| `bsc.shutdown` | DECISION (CANONICAL) | `Stop` enters for the head rewrite | every node close |
| `bsc.startup-reconcile` | DECISION (CANONICAL, FINALITY) | the reconciliation of the recovery driver | restart 2 (A7 state root deleted) |
| `bsc.engine-api` | REFUSE | `InsertBlockWithoutSetHead` and `SetCanonical` are refused through the gate; patch 01 refuses unobserved execution | both refused |
| `bsc.snap-sync` | REFUSE | `CheckProfile` refuses snap sync; `InsertReceiptChain`, `InsertHeaderChain`, `InsertHeadersBeforeCutoff`, `SnapSyncStart` and `SnapSyncComplete` are refused through the gate; `geth import-history` opens the store for writing, so the manual database guard refuses it on a capture store | `eth.New` with snap sync; `InsertReceiptChain`, `InsertHeaderChain` |
| `bsc.sealing` | REFUSE | `CheckProfile` refuses voting and the builder role; `StartMining` and `WriteBlockAndSetHead` are refused through the gate | all three |
| `bsc.manual-commands` | REFUSE | `Start` refuses a Parlia engine without the system-call API (`geth import`); the prune-state guard refuses live pins (admission patch 04) | `Start` without the API |
| `bsc.database-commands` | REFUSE | `MakeChainDatabase` (every writable open of an existing store) and `geth removedb` run `ExtractorGuardChaindata`. It refuses while a pin is live (every ADMITTED or EXEC_READY observation keeps its parent pin) or an obligation is open (a producer run whose decision low-water mark is below its counter: an unresolved intent, or a decision the mirror has not applied) | `ExtractorGuardChaindata` on a store with A3 EXEC_READY (pin), after A3's decision (obligation), and on a store without decisions (passes) |
| `bsc.attestation-read` | READ_ONLY | read through `ReadAttestation` in every import decision | every decided import |
| `bsc.execution-witness` | READ_ONLY | an execution without a state write is refused on a capture node (`ErrExtractorWitness`), so it never runs outside the guard or through the current observation | `debug_executionWitness` |
| `bsc.system-call-api` | READ_ONLY | the system-call API of the node wiring | the shipped node |
| `bsc.freezer` | READ_ONLY | admission patch 02 | source review |
| `bsc.eth-call`, `bsc.prefetch`, `bsc.trace-state` | READ_ONLY | a state copy, a throwaway state or an ephemeral trie database; no choke point | source review |
| `bsc.notify-finalized` | READ_ONLY | writes no native state | E05-01.bsc_finality |

A chain reset (`ResetWithGenesisBlock`) is refused on a capture store
(`ErrExtractorReset`). A READ_ONLY caller that ever reached a choke point
would fail it with no open pass (`ErrBypass`).

## Deferred rows (D104)

The owner decided on 2026-11-13 to fail closed now and fix later (D104). The
targeted native replay for these two rows belongs to E05-02.reorg.

- `bsc.known-block`: a stored block becomes the head without an execution
  only through the known-block path. The known-branch corpus rule needs a
  retained EXEC_READY observation of each block of the branch, and the BSC
  import path retains none. The seam observes only blocks on top of the
  head, and a decision closes its observation and purges its inputs (D100).
  So the activation stops with `ErrCorpus`, a `lifecycle.ErrInvariant`,
  before any write.
  - In this BSC version the known-block loop of `insertChain` re-executes a
    stored block that the fork choice prefers. With a capture hook, `Begin`
    returns no observation for a stored block with state. So the gate's
    known-block pass enters right after the fork choice, before the
    re-execution.
  - `writeKnownBlock` (the mid-batch path) has the same pass. The capture
    import inserts one block per call, so that path does not run.
- `bsc.side-chain`: `Begin` refuses every block whose parent is not the
  head with its state (`ErrImportProfile`), before `insertSideChain` can run.
  That covers a pruned parent and an ordinary side branch or reorganization
  alike, so the BSC capture path follows no side branch until E05-02.reorg.
  The D101 plan row on the events of the decided SIDE path is therefore
  unreachable on BSC (see "Import decision").

## Limits

- Only the hash state scheme (as in the decision bridge).
- The known-block step of the probe uses a fixture. Native `SetHead` deletes
  every fork above its target, so the probe rewrites the block data of S3
  (header, body, total difficulty) after the rewind. That is the stored block
  above the head that the native known-block loop describes for a snap-sync
  rollback. The state of S3 is the native state that the rewind kept; the
  fixture writes no state. Snap sync itself is refused on a capture store.
- The finalized clear of a rewind is implemented but not exercised: the
  fixture has no vote attestation, so the finalized block stays genesis and
  no rewind goes below it.
- The FINALITY decision of the startup reconciliation is implemented but not
  exercised, for the same reason.
- The passes of `insertSideChain` (and its nested import pass) are never
  entered by the probe: a LIVE side branch is refused at `Begin`, and the
  BOOTSTRAP imports of the probe never meet a pruned ancestor. They rest on
  source review.
- The node wiring is proven through `eth.New` and the RPC APIs in-process
  (`admin_importChain`, `debug_setHead`, `debug_executionWitness`); the
  shipped `geth` command line and its release build are E01-01.builds and E06
  (D100).
- The probe calls `GoLive` where the live service would after the initial
  sync: a node without peers never reports `Synced`.
- With no mirror (E05-01.registry), every decision stays an obligation, so the
  manual database guard refuses every capture store that has made a
  decision. The guard reads the decision records only; lifecycle records (H
  and the view) are not obligations.
- A refused finalized setter call is logged, not returned: native
  `SetFinalized` returns no error. The gate is stopped after a bypass, so
  the node imports nothing more.
- If `GoLive` fails on a first start, the live service stops the node
  (`log.Crit`).
