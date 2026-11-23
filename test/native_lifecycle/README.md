# Native lifecycle integration (E05-02.reorg)

This module collects the lifecycle-event evidence of a canonical
reorganization on the real capture packages: the admission observations,
the decision bridge, the lifecycle gate and the finality owner of the
assembled extractor module, driven over an in-memory native store with
injected power loss, with every native decision mirrored into the real
lifecycle registry of E05-01.registry and the GC anchor observed on the
real recovery GC engine of E05-01.activation_gc. The published event set
folds through the E02-03 lifecycle validator oracle. One applicability
slot per network: `eth` and `bsc` are implemented; the POLY slot is
NOT_RUN. It owns the native half of REQ-CAP-08 (AC-CAP-08-02) and
REQ-LIF-02 (AC-LIF-02-01):

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

The full native-plus-Gateway crash matrix stays with E05-03; the durable
registry itself is E05-01.registry; the GC decision over the anchor is
E05-01.activation_gc.

## Shared shape of a slot

Each slot is its own Go package under this directory; the harness installs
it into the assembled extractor module, where it imports the real packages
at their native paths:

- one observation per block through the real `Begin`/`Admit`/`ExecReady`
  with a pin and input capsule, so decision members are real EXEC_READY
  records with durable child roots;
- one FINALITY/IMPORT/SIDE/CANONICAL intent per native step through the
  gate pass of the mapped caller, the native marker in the final batch,
  and NATIVE_DECIDED through the bridge;
- every native decision is mirrored into `adapters/lifecycle_registry`
  before native settles it: the intent, the corpus capture of each member
  block, the settled marker, and the application plan — the Applied,
  Removed and StatusChanged descriptors are committed as registry
  application plans, so the published event set (`Registry.Events`) is
  the evidence, and the registry rules (revision frontier, terminal
  activations, speculative corpus, applied-decision frontier) are the
  real ones, not a test-local model;
- the bridge's applied-decision frontier is answered from the registry
  (the mirror seam), so the native marker cleanup of an applied decision
  runs for real;
- the GC anchor is observed on `adapters/recovery_gc`: the finality
  owner's `Accept` drives the engine, the accepted anchor is the shared
  `recoverygc.AcceptedGcAnchor` record, and the registry's activation
  records map onto GC entities;
- the published events fold through `test/lifecycle_validator` (the
  E02-03 oracle) in several delivery orders to one unambiguous record
  set.

## ETH block (`eth`)

`python3 test/harness/native_lifecycle.py assemble DIR` builds the
runnable module: the finality assembly (source_inputs, admission,
decision, lifecycle, finality under `extractor/`) plus the ETH model at
`extractor/native_lifecycle_eth`, bound to the repository-local
`adapters/lifecycle_registry` and `test/lifecycle_validator` (the finality
assembly already binds `adapters/recovery_gc` and `protocol/semantic`)
through local replaces. `test/harness/test_native_lifecycle.py` is the
portable control. The tests are portable: memory storage, no native node,
no network.

### Scenario rules

- **Import** (`geth.import`): one IMPORT decision per new canonical block;
  its manifest allocates exactly one batch with the Applied descriptors of
  the block entity (ordinal 0) and its body transactions (ordinal body
  index+1) — the entity-ordinal contract of CONTROL_WIRE_CONTRACT gives
  one block entity to a decision.
- **Side block** (`geth.insert-without-head`): one SIDE decision retains
  the corpus and the observation for a later known-branch switch; it has
  no public events, allocates no batch and closes as a checkpoint in the
  registry.
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
  replay; a branch block without one (a reapply) rebuilds its corpus by a
  native targeted replay of the retained inputs before the switch
  decision, and the registry reuses the corpus record of the first
  application, so the new activation is speculative.
- **Reapply**: the same branch returning canonical gets new activations
  (a new decision sequence, never the first activation of the branch); the
  previous activations stay terminal.
- **Finality** (`geth.set-finalized`): the finality owner of
  E05-01.eth_finality observes the finalized setter; its FINALITY decision
  carries public events and its StatusChanged descriptors are committed
  through the registry under the same decision, one revision from the
  same activation counter per covered activation.
- **Manifest ledger**: the batch allocation commits first and is rebuilt
  from the durable allocation records at reopen (the producer-store model
  of D79), then one chunk per descriptor group, then the READY header
  last; the registry application of the decision follows the header, so a
  partial manifest applied nothing. A manifest without its header is not
  READY, and a rebuild from the same native marker restores the same
  batch and the same full ordered set.
- **Delivery** is a pure publication tracker of the model
  (UNPUBLISHED / ONE_PART_PUBLISHED / PUB_ACKED); no lifecycle step reads
  it. Removed is created for a READY Applied in every state, and the
  Applied delivery is never guessed from it.

### Tests

| Test | Evidence |
|---|---|
| `TestRemovedForReadyAppliedRegardlessOfPubAck` | AC-LIF-02-01: the Removed set of the reorg decision references the former activations in all three publication states |
| `TestReorgIsOneDecisionWithOneBatch` | AC-CAP-08-02: one reorg decision, complete Removed set plus its Applied entities under one batch; the SIDE decisions allocate none; the registry committed the same ordered set |
| `TestKnownBranchBecomesCanonicalWithoutNewExecution` | the retained corpus decides the switch; no replay, no new admission; the applied activation reuses the speculative corpus of the SIDE decision |
| `TestReapplyCreatesNewActivations` | the reapplied branch gets new activations from targeted replay; the old activations stay terminal in the registry |
| `TestReorderedEventsFoldIdentically` | the registry's published event history folds to identical records in four delivery orders through the E02-03 oracle; the SIDE decisions publish nothing |
| `TestGcAnchorAndMarkerCleanup` | the bridge cleanup deletes the markers of the applied decisions; the GC anchor acceptance survives it, the abandoned activations are terminal, and the engine releases them against the accepted anchor |
| `TestManifestCrashAndMarkerRepeatRestoresOneBatch` | AC-CAP-08-02: a crash at every manifest fault point, both power-loss extremes, and the marker repeat restoring the one former batch and the full ordered set; a partial manifest is not READY and applied nothing |

### Model limits

- The manifest ledger is a model of the producer store of E03-01
  (allocation record, chunks, READY header) on the same memory store; the
  real store and the WAL protocol are not exercised here.
- The reordered-events proof folds semantic descriptors through the E02-03
  oracle; the proto schema half and the transport are out of scope.
- One body transaction per block keeps the entity ordinals readable; the
  optional families are not exercised.

## BSC block (`bsc`)

`python3 test/harness/bsc_finality.py assemble DIR` builds the runnable
module: the lifecycle binding assembly (source_inputs, admission,
decision, lifecycle under `extractor/`), the BSC finality package at
`extractor/bsc/finality` and the BSC model at
`extractor/native_lifecycle_bsc`, bound to the repository-local
`adapters/lifecycle_registry`, `adapters/recovery_gc`,
`protocol/semantic` and `test/lifecycle_validator` through local
replaces. `test/harness/test_bsc_finality.py` is the portable control.

The BSC reorg behavior under the accepted E01 seams: the capture path
follows no side branch (D104), so a reorganization is one CANONICAL
decision without a new execution (the `bsc.set-head` rewind path of the
lifecycle map) followed by fresh imports of the new branch — every
reapplied block is a new activation. The tests drive the real decision
bridge over the real admission path on a memory store, mirror the
decisions into the real lifecycle registry, fold the published events
through the E02-03 oracle, and use the BSC finality observatory
(`adapters/bsc/finality`) for the finality side of the reorg:

- `TestReorgCreatesRemovedForReadyApplied` (AC-LIF-02-01): after A1..A5
  are imported and decided, one canonical rewind A5→A2 creates a Removed
  for each of the three abandoned activations. The Removed derives from
  the durable decision records alone — the delivery obligations of the
  removed activations are still open and no PubAck exists; the canonical
  decision alone fabricates no Applied for the new branch.
- `TestReapplyIsANewActivation` (AC-CAP-08-02): the reorg to B3..B5 and
  the reapply of A5 on top of B5 give A5 a new decision sequence,
  activation identity and batch (one batch per decision, idempotent per
  decision); the removed first A5 activation stays terminal in the
  registry, its reapply is a separate live activation, and the reordered
  registry event stream folds to the same result per activation in every
  order through the E02-03 oracle.
- `TestReorgDecisionCrashAndMarkerRepeat` (AC-CAP-08-02): a power loss
  between the canonical reorg intent and its marker, and the repeated
  native marker of the same decision after the restart: the one former
  decision is restored and resolved (a second decision is refused while
  the intent is unresolved), the former batch bindings are adopted, not
  reallocated, and the complete ordered Removed set derives from the
  restored decision.
- `TestSideBranchFinalityKeepsCanonicalActivations` (AC-LIF-06-03): the
  header-attested anchor is accepted on the canonical branch through the
  recovery GC engine; after the canonical change to the side branch, a
  delayed side-branch finality observation is refused, and the
  observation of the new head — which no longer covers the accepted
  anchor — is the proven regression that stops the observatory; the
  abandoned activations project to `CANONICAL_UNFINALIZED`.

## What these slots do not do

This is unit-level evidence on in-memory store models, not running
nodes: wiring the native setters, rewind and startup drivers is the
native Geth and BSC blocks; the native SetHead/known-block/side-chain
paths, the D104 targeted native replay, the marker inside a real native
batch and the PubAck-side delivery interplay are E05-03.full_crash_matrix;
the registry and GC packages themselves are tested in their own modules;
the POLY slot of this task is not started; every public AC cell stays
NOT_RUN until the native blocks measure them.
