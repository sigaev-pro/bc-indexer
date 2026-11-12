# Geth decision bridge (E01-02.decision_bridge, Geth block)

This directory is Geth-only. It joins the shared decision core
([`../README.md`](../README.md), block 0) to the accepted Geth import path
(Geth `1df30b18d9ed35ee9d28ffd4648442c2204554ad`), on top of the Geth
admission wiring ([`../../admission/native/README.md`](../../admission/native/README.md)).
It proves AC-CAP-07-01, 07-02, 07-03 and 07-06 for the private local ETH slot
only; every public cell stays NOT_RUN (D96, D98, D99).

| File | Installed as | Role |
|---|---|---|
| `patches.json` | applied after `adapters/geth/admission/native/patches.json` | Six anchored text patches; each original must occur exactly once in the tree that the admission patch set produces |
| `core_decision.go.txt` | `core/extractor_decision.go` | The write-path seam (`core.ExtractorDecision`), the decided `writeBlockWithState`, and the two native completion helpers of the recovery driver |
| `decision.go` | `extractor/admission/native/decision.go` | `AttachDecisions`, the `Roots` adapter, the import-path calls and the recovery driver; it shares the package of the Geth admission wiring |
| `decision_test.go` | `extractor/admission/native/decision_test.go` | Unit tests on an in-memory Geth chain |

The shared packages `extractor/admission`, `extractor/source_inputs` and
`extractor/decision` are installed byte-identical. The Geth admission wiring
(`adapters/geth/admission/native/geth.go`, also Geth-only) gains five
extension points that this block uses: an attach with input cleanup, the
admission part of recovery without the switch to LIVE
(`RecoverObligations`), the native-only replay of an EXEC_READY observation
in RECOVERY_ONLY, the observation key, and the known-block refusal of
`Begin`.

## Patches

| Patch | File | Change |
|---|---|---|
| `blockchain_decision_field` | `core/blockchain.go` | `BlockChain` holds the decision seam and the decision of the write in progress |
| `process_block_decision` | `core/blockchain.go` `ProcessBlock` | After `ExecReady`, an observed state write starts a decision (`Write`); before the bridge is open (the admission part of recovery) the block is not written |
| `write_block_decision` | `core/blockchain.go` `writeBlockWithState` | A decided write runs `extractorWriteBlockWithState`: this is the side-chain write of `InsertBlockWithoutSetHead` and the block batch of a head change |
| `head_batch_marker` | `core/blockchain.go` `writeHeadBlock` | Only the head batch of the decided block carries the native decision marker, then `SyncKeyValue`; the head batches that `reorg` writes for lower blocks of the new branch carry none |
| `process_block_resolve` | `core/blockchain.go` `ProcessBlock` | `Resolve` after the batch with the marker is durable, then `Cleanup` |
| `backend_attach_decisions` | `eth/backend.go` | The shipped node calls `AttachDecisions` instead of `Attach` |

## One decided block write

The caller holds the observation guard and the chain mutex; the admission
observation is EXEC_READY.

1. `statedb.Commit` (the native StateDB commit of `writeBlockWithState`).
2. Child-root barrier: `triedb.Commit(root)` flushes the child state from
   the hash trie database to the key-value store, then `SyncKeyValue`. The
   root is not referenced for in-memory garbage collection afterwards: it is
   on disk. Only the hash state scheme is supported; a decided write in
   another scheme is refused.
3. `Intend` with the intent of the write:
   - `SIDE` without a head change (`InsertBlockWithoutSetHead`, the engine
     `newPayload` path, and every write of the recovery driver that does not
     complete a saved head change): old and new head are the current head,
     no events, delivery `CHECKPOINT`;
   - `IMPORT` when the block extends the head (`InsertChain`): events,
     delivery `BATCH`;
   - `CANONICAL` when the block moves the head to another branch
     (`writeBlockAndSetHead` with a reorganization): events, delivery
     `BATCH`; its members are the new block and every block of the new branch
     that a `SIDE` decision retains, so that one decision closes them.
   The first member is always the written block, with its EXEC_READY digest
   and its durable child root. The evidence digest is
   `H(tag("extractor-geth-intent-evidence-v1") || u64(len(kind)) || kind ||
   block hash || u64(number) || child root)`.
4. The native mutation: the block batch (block, receipts, preimages; for
   `SIDE` also the marker) is written and synced; for a head change,
   `writeBlockAndSetHead` reorganizes if needed and `writeHeadBlock` writes
   the head batch with the marker, then syncs. `reorg` first writes a head
   batch for every lower block of the new branch; the marker is bound to the
   hash of the decided block, so only its own head batch, the final one,
   carries it. The marker is
   `decision.MarkerValue(intent)` under `decision.MarkerKey`.
5. `Resolve` writes NATIVE_DECIDED and closes the members with
   `Admission.Decide` (except `SIDE`), which releases their pins and, with
   `OpenWithCleanup`, purges their capsules, logs and admission records.
   `Cleanup` then removes the markers of decisions that the mirror applied.

A native refusal after the intent (the native outcome fixture of the probe;
the shipped binary has none) writes the block as a Geth bad block
(`rawdb.WriteBadBlock`, the durable native evidence), syncs, then
`RejectIntent` with `H(tag("extractor-geth-native-refusal-v1") || block
hash)`. The block is not stored; no NATIVE_DECIDED record, marker or
obligation follows.

## Recovery driver (`Decisions.Recover`)

`AttachDecisions` runs it after `NewBlockChain` (the native DB recovery),
before any import:

1. Admission part (`RecoverObligations`): abandon INPUT_PREPARING
   observations and re-execute each ADMITTED obligation through
   `InsertBlockWithoutSetHead` to EXEC_READY or NATIVE_REJECTED. The bridge
   is not open yet, so the block is not written (`deferred`).
2. `decision.Open` with the `Roots` adapter and the mirror (none in the
   shipped binary: no decision is applied before E05-01.registry).
3. The unresolved intent, if any, is settled from native evidence:
   - its marker is present: `Resolve`;
   - the written block is a Geth bad block and is not stored: the native
     side refused it, `RejectIntent` with the same evidence digest as the
     live path (AC-CAP-07-06);
   - the written block is stored with its state (the block batch is durable,
     the final batch is not): `ExtractorCompleteHead` reorganizes if needed
     and writes the head batch with the saved marker (`SIDE`:
     `ExtractorWriteMarker`), then `Resolve` (AC-CAP-07-03). The head must be
     the saved old head or, for `CANONICAL`, a lower block of the new branch
     that an interrupted `reorg` already made the head; `reorg` runs again
     from the saved old head, so its clean-up of the old branch indexes
     (canonical hashes above the new branch, transaction lookups) is done;
   - nothing of the block is stored: the saved block (from its capsule) is
     re-imported natively, through `InsertChain` for a head change and
     `InsertBlockWithoutSetHead` for `SIDE`, with native-only replay of its
     EXEC_READY observation; the write must intend exactly the saved
     transition (same kind, heads, members and evidence) and reuses its
     record (AC-CAP-07-03). A native refusal during the re-import records the
     rejection, which also settles the intent;
   - anything else (a bad block that is also stored, a head that is not the
     saved old head, a write that would intend another transition) stops
     startup with `ErrDecisionRecovery`, a source invariant error.
4. Every EXEC_READY observation without a decision is replayed natively from
   its parent state (`InsertBlockWithoutSetHead`, no capture hook, no
   admission step) and decided as `SIDE`; the replay must pass native
   validation, which proves the same child root, and the barrier makes that
   root durable. The EXEC_READY record and the observation stay
   (AC-CAP-07-02). A block that is already stored without a decision stops
   startup: such a store was written before the switch to the bridge.
5. `Cleanup`, then `decision.Bridge.GoLive`.

The native-only replay is a `replayed` observation in `resume`: it installs
neither the saved BLOCKHASH answers nor the safe point, writes no admission
record, and stops recovery if native execution or validation fails.

## One decision per block

A block that the native store already holds with its state never opens a
new observation: `Begin` in LIVE refuses it with `ErrKnownBlock`. Geth skips
such a block natively (`ErrKnownBlock`, `skipBlock`) unless its snapshot
layer is missing, in which case it would re-execute it; that re-execution is
refused. The probe shows both: after a clean restart the imports of the
known blocks are skipped natively; after a crash between the block batch and
the head batch the completed head has no snapshot layer, and its import is
refused without an observation.

## Limits

- Hash state scheme only. The barrier flushes the child state of every
  decided block to disk, so a capture node keeps the state of every decided
  block (like an archive node) until E05-01.activation_gc releases it.
- Without a mirror (the shipped binary until E05-01.registry), every
  NATIVE_DECIDED `IMPORT` and `CANONICAL` decision keeps its delivery
  obligation and its marker, and the decision low-water mark does not move:
  `decision.Open` at each start and the `Cleanup` after each decided block
  visit every decision since the first one, so their cost grows with the
  blocks imported. A long-running or product run needs E05-01.registry, as
  it needs E05-01.activation_gc for retained side blocks (block 0).
- A `SIDE` decision retains its member, pin and inputs until a `CANONICAL`
  decision names it or E05-01.activation_gc releases it (block 0). Head
  changes without a new block (`SetCanonical`, a known block above the head
  through `writeKnownBlock`, finality) do not pass the bridge yet: they
  belong to E01-02.lifecycle_seams. So a retained side block that becomes
  canonical through `SetCanonical` keeps its `SIDE` retention.
- The recovery driver re-executes an ADMITTED obligation twice (once for
  EXEC_READY before the bridge opens, once as the native-only replay that
  writes it with its decision).
- A known block that Geth would re-execute only to rebuild a missing
  snapshot layer is refused (`ErrKnownBlock`) instead of re-executed.
- Stores written by the admission alone before this block are refused at
  startup (`ErrDecisionRecovery`); they are discarded, as block 0 requires.
- The native outcome fixture is the only native refusal after an intent that
  the probe exercises; the shipped binary has none, and a native write
  failure in `writeBlockWithState` or `reorg` after the intent leaves the
  intent unresolved: the next import is refused (`ErrPending`) until a
  restart settles it from native evidence.
