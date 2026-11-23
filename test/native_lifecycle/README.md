# Native lifecycle (E05-02.reorg)

`test/native_lifecycle` owns
[E05-02.reorg](../.project/docs/TASK_DECOMPOSITION.md): REQ-CAP-08
(AC-CAP-08-02) and REQ-LIF-02 (AC-LIF-02-01) — evidence for reorgs,
reapplication, known branches and reordered events, with Removed created
for a READY Applied activation regardless of whether PubAck has arrived.

This directory holds one applicability slot per network. The BSC slot is
implemented; the ETH and POLY slots are NOT_RUN.

| Path | Role |
|---|---|
| `bsc/reorg_test.go` | The BSC slot's Go tests. `python3 test/harness/bsc_finality.py assemble DIR` installs them into the assembled binding module (as `extractor/bsc/finality`, together with `adapters/bsc/finality`), where `go test ./extractor/bsc/finality/` runs them against the real admission, decision, lifecycle and finality packages on an in-memory store model with injected power loss. |
| `test/harness/test_bsc_finality.py` | The portable controls of the assembly and the digest vectors (run by `bootstrap/ci`). |

## BSC slot

The BSC reorg behavior under the accepted E01 seams: the capture path
follows no side branch (D104), so a reorganization is one CANONICAL decision
without a new execution (the `bsc.set-head` rewind path of the lifecycle
map) followed by fresh imports of the new branch — every reapplied block is
a new activation. The tests drive the real decision bridge
(`extractor/decision`) over the real admission path on a memory store, fold
the resulting lifecycle descriptors over the real `protocol/semantic`
identity model, and use the BSC finality observatory
(`adapters/bsc/finality`) for the finality side of the reorg:

- `TestReorgCreatesRemovedForReadyApplied` (AC-LIF-02-01): after A1..A5 are
  imported and decided, one canonical rewind A5→A2 creates a Removed for
  each of the three abandoned activations. The Removed derives from the
  durable decision records alone — the delivery obligations of the removed
  activations are still open, the mirror applied nothing and no PubAck
  exists; the canonical decision alone fabricates no Applied for the new
  branch (no executed and decided blocks, no activation).
- `TestReapplyIsANewActivation` (AC-CAP-08-02): the reorg to B3..B5 and the
  reapply of A5 on top of B5 give A5 a new decision sequence, activation
  identity and batch (one batch per decision, idempotent per decision); the
  removed first A5 activation stays terminal, its reapply is a separate
  live activation, and the reordered event stream folds to the same result
  per activation in every order.
- `TestReorgDecisionCrashAndMarkerRepeat` (AC-CAP-08-02): a power loss
  between the canonical reorg intent and its marker, and the repeated
  native marker of the same decision after the restart: the one former
  decision is restored and resolved (a second decision is refused while the
  intent is unresolved), the former batch bindings are adopted, not
  reallocated, and the complete ordered Removed set derives from the
  restored decision.
- `TestSideBranchFinalityKeepsCanonicalActivations` (AC-LIF-06-03): the
  header-attested anchor is accepted on the canonical branch; after the
  canonical change to the side branch, a delayed side-branch finality
  observation is refused, and the observation of the new head — which no
  longer covers the accepted anchor — is the proven regression that stops
  the observatory; the abandoned activations project to
  `CANONICAL_UNFINALIZED`.

The registry behavior these tests need (Applied revision 0, Removed
revision from the same activation counter, no speculative corpus) is driven
through a test-native mini-registry over the real `protocol/semantic`
descriptors; the product registry is E05-01.registry and is not created
here.

## What this slot does not do

This is unit-level evidence on an in-memory store model, not a running
BSC node: the native SetHead/known-block/side-chain paths, the D104
targeted native replay, the marker inside a real native batch and the
PubAck-side delivery interplay are the E05-03.full_crash_matrix and native
BSC blocks; the retained-ancestry/pin release side of the reorg is
E05-01.activation_gc; every public AC cell stays NOT_RUN. The ETH and POLY
slots of this task are not started.
