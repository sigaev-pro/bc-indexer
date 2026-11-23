# Native lifecycle integration (E05-02.reorg)

This module collects the lifecycle-event evidence of a canonical
reorganization on the real capture packages: the admission observations,
the decision bridge, the lifecycle gate and the finality owner of the
assembled extractor module, driven over an in-memory native store with
injected power loss. It covers ETH first (`eth`); the BSC and POLY blocks
follow in their own directories. It owns the native half of REQ-CAP-08
(AC-CAP-08-02) and REQ-LIF-02 (AC-LIF-02-01):

> AC-CAP-08-02 — ALL: one native reorg decision replaces several blocks;
> the manifest is built in chunks. Crash inside the construction and a
> repeat of the same native marker: one former batch_id and the full
> ordered set of Removed/Applied/status descriptors are restored; a
> partial manifest is not READY and the events are not split into block
> batches.
>
> AC-LIF-02-01 — ALL: an Applied manifest is READY; variants before
> Publish and after one part. A canonical reorg: the durable Removed
> references the former activation regardless of the PubAck; the Applied
> delivery is not guessed.

The full native-plus-Gateway crash matrix stays with E05-03; the registry
mirror and the durable StatusChanged publication are E05-01.registry; the
GC decision over the anchor is E05-01.activation_gc.

## ETH block (`eth`)

`test/harness/native_lifecycle.py assemble DIR` builds the runnable
module: the finality assembly (source_inputs, admission, decision,
lifecycle, finality under `extractor/`) plus the ETH model at
`extractor/native_lifecycle_eth`, bound to the repository-local
`protocol/semantic` (descriptors, batch allocation of D79) and
`test/lifecycle_validator` (the E02-03 oracle) through local replaces. The
tests are portable: memory storage, no native node, no network.

The model drives the real packages — no reimplementation of the decision
protocol:

- one observation per block through the real `Begin`/`Admit`/`ExecReady`
  with a pin and input capsule, so decision members are real EXEC_READY
  records with durable child roots;
- one FINALITY/IMPORT/SIDE/CANONICAL intent per native step through the
  gate pass of the mapped caller, the native marker in the final batch,
  and NATIVE_DECIDED through the bridge;
- the finality owner of E05-01.eth_finality observes the finalized setter,
  and the status descriptors are derived from its typed evidence.

### Scenario rules

- **Import** (`geth.import`): one IMPORT decision per new canonical block;
  its manifest allocates exactly one batch with the Applied descriptors of
  the block entity (ordinal 0) and its body transactions (ordinal body
  index+1) — the entity-ordinal contract of CONTROL_WIRE_CONTRACT gives
  one block entity to a decision.
- **Side block** (`geth.insert-without-head`): one SIDE decision retains
  the corpus and the observation for a later known-branch switch; it has
  no public events and allocates no batch.
- **Canonical switch** (`geth.set-canonical`): the branch is applied
  bottom-up, one CANONICAL decision per block; the first decision that
  leaves the old canonical path is the **reorg decision**: one native
  decision whose manifest replaces several blocks — the complete Removed
  set of the replaced activations (revision>0 from the same activation
  counter, referencing the former `activation_ref` by decision sequence)
  together with its own Applied entities, in one batch built in chunks.
  The Removed set is never split per replaced block.
- **Known branch without execution**: a branch block with an open retained
  observation is decided from that observation — no new admission, no
  replay; a branch block without one (a reapply) rebuilds its corpus by
  targeted replay of the retained inputs before the switch decision.
- **Reapply**: the same branch returning canonical gets new activations
  (a new decision sequence, never the first activation of the branch); the
  previous activations stay terminal.
- **Manifest ledger**: the batch allocation commits first and is rebuilt
  from the durable allocation records at reopen (the producer-store model
  of D79), then one chunk per descriptor group, then the READY header
  last; a manifest without its header is not READY, and a rebuild from the
  same native marker restores the same batch and the same full ordered
  set.
- **Delivery** is a pure publication tracker of the model
  (UNPUBLISHED / ONE_PART_PUBLISHED / PUB_ACKED); no lifecycle step reads
  it. Removed is created for a READY Applied in every state, and the
  Applied delivery is never guessed from it.

### Tests

| Test | Evidence |
|---|---|
| `TestRemovedForReadyAppliedRegardlessOfPubAck` | AC-LIF-02-01: the Removed set of the reorg decision references the former activations in all three publication states |
| `TestReorgIsOneDecisionWithOneBatch` | AC-CAP-08-02: one reorg decision, complete Removed set plus its Applied entities under one batch; the SIDE decisions allocate none |
| `TestKnownBranchBecomesCanonicalWithoutNewExecution` | the retained corpus decides the switch; no replay, no new admission |
| `TestReapplyCreatesNewActivations` | the reapplied branch gets new activations from targeted replay; the old activations stay terminal |
| `TestReorderedEventsFoldIdentically` | the whole event history (Applied, Removed, StatusChanged from the finality evidence) folds to identical records in four delivery orders through the E02-03 oracle |
| `TestManifestCrashAndMarkerRepeatRestoresOneBatch` | AC-CAP-08-02: a crash at every manifest fault point, both power-loss extremes, and the marker repeat restoring the one former batch and the full ordered set; a partial manifest is not READY |

### Model limits

- The activation registry and the revision counters are in-memory model
  state with idempotent per-decision allocation; the durable registry is
  E05-01.registry.
- The manifest ledger is a model of the producer store of E03-01
  (allocation record, chunks, READY header) on the same memory store; the
  real store and the WAL protocol are not exercised here.
- The reordered-events proof folds semantic descriptors through the E02-03
  oracle; the proto schema half and the transport are out of scope.
- One body transaction per block keeps the entity ordinals readable; the
  optional families are not exercised.

## BSC and POLY blocks

Not in this change. The BSC block will add the early-vote and
header-attested finality paths of E05-01.bsc_finality to the same
scenarios; the POLY block the milestone/checkpoint projections of
E05-01.polygon_observer.
