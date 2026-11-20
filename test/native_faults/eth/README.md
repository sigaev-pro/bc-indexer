# Geth fault probe (E01-04.early_faults, Geth block)

| File | Role |
|---|---|
| `faults_geth.py` | `prepare` builds the probe from the accepted Geth decision-bridge preparation plus the lifecycle and faults patch sets; `run` executes it under the host lock and judges the report; `judge` judges a saved probe report |
| `probe.go` | The probe, installed as `cmd/extractor-faults-geth` |
| `test_faults_geth.py` | Portable controls of the driver (run by `bootstrap/ci`) |
| `faults_geth.mutants.json` | Single-check mutants of the driver judgement |
| `sample_report.json` | A development run of the probe, compacted (one shared corpus observation, each case's own decisions); used by the portable tests, not measured evidence |

The patch set is in
[adapters/geth/faults/native](../../../adapters/geth/faults/native/README.md).
The fault model, catalog and judge are block 0:
[test/native_faults/README.md](../README.md).

## What the probe does

Inputs: the smoke corpus ETH blocks 1 and 2 (E01-03.smoke_corpus), made
with Geth's own chain generator on the private fixture
(`test/harness/fixtures/genesis.json` plus the corpus contracts), as in
`test/native_corpus/eth/probe.go`.

For each catalog point and each variant (30 cases), on a fresh store:

1. **Open.** The chaindata opens on `vfs.NewCrashableMem` (pebble v2, through
   `pebble.ExtractorFS`). The probe syncs the created directories, so they
   survive a power loss. A fault store wraps the key-value store, and a
   direct `core.BlockChain` gets the shipped `AttachLifecycle`. The freezer
   stays on the OS file system in a new directory per open; the short chain
   never reaches it.
2. **Scenario.**
   - H points: block 1 imports in BOOTSTRAP, the initial sync. The fault
     comes inside `GoLive`, which sets H at block 1.
     `h.after-golive-before-first-import` fires once `GoLive` returns.
   - All other points: `GoLive` sets H at the genesis. The fault comes
     inside the import of block 1, the first live block.
3. **Fault.** The fault store matches the key the step writes:
   - the boundary record;
   - the admission counter, ADMITTED or EXEC_READY record;
   - a pin write or delete;
   - the decision counter (`dec.before-intent-sync`: `Intend` syncs the
     intent, then writes the counter), the intent or the decided record.

   It then panics at the next `SyncKeyValue`: before syncing for a
   "before" point, after syncing for an "after" point. For
   `dec.in-native-mutation` it panics at the first native write after the
   synced intent. The panic unwinds the native caller, so no later code of
   the step runs.
4. **Abandon and reopen.** The probe abandons the node (no
   `BlockChain.Stop`, no store close). It reopens on
   `CrashClone(UnsyncedDataPercent: 0)` for a power loss and
   `CrashClone(UnsyncedDataPercent: 100, RNG)` for a crash. Before the crash
   copy it waits until the WAL files stop growing (`wal_settle_ms`, at most
   two seconds; no sync). Pebble v2 keeps a small NoSync WAL record in
   process memory until a sync or a full WAL block, so a process crash loses
   the pending capture write just as a power loss does: on ETH the crash
   variant reaches the same states as the power loss at the pending points.
   `unsynced_dropped` is the fault store's count of writes since the last
   completed sync, an upper bound.
5. **States after the restart.** They are read from the records before any
   attach: the boundary, the native canonical header at its number, and the
   sequence 0 records of admission, pins and decisions, counted only below
   the durable counter of the run, as the accepted packages read them. The
   opening mode is the gate's phase from its `gate_opened` trace event,
   before the shipped attach finishes recovery and goes live; the phase
   after the attach is kept as `attach_phase`.
6. **Recovery and import.** `GoLive` runs, then blocks 1 and 2 import, then
   the probe reads the final boundary, the native header at H, the mode,
   the head and the decided members. Last come the corpus observation (the
   native export of `test/native_corpus/eth/probe.go`) and a re-import of
   both blocks, which must add no observation or decision.

`faults_geth.py judge` turns the probe report into the block 0 faults
report. It compares each case's observation with
`test/native_corpus/expected/eth.json` by `corpus_compare.py`, without only
the per-block `decisions` rows (see the block 0 README, "Report format").
It adds the probe's own errors, and any of them fails the result:

- the storage model;
- a malformed case;
- restart, `GoLive` or import errors;
- undecided intents (the list must be empty);
- re-import errors;
- the native re-execution checks of `test/native_corpus/eth/corpus_eth.py`:
  one row per block, the root, the gas and one equal receipt per
  transaction.

The export helpers of `probe.go` are copied from
`test/native_corpus/eth/probe.go`. A portable test keeps them identical.
The one difference is in `decisions`: a rejected intent is not undecided.

## Handoffs

Each handoff is one native scenario of `handoffs.go`; the driver derives its
verdict from the recorded facts and never accepts a producer verdict.

- `geth.pass-at-golive`: a canonical import held open across `GoLive` must be
  refused, finish before H is set, and leave no observation or decision.
- `geth.sethead-interrupted`: a `SetHead` rewind stopped between its two head
  batches must complete from the durable intermediate head, and the recovery
  must settle the saved intent inside the rewind.
- `geth.mid-reorg-long-branch`: a reorganization stopped before its final head
  batch, with more than one block on the old branch, must complete from the
  stored transition and settle the intent inside the reorg.
- `geth.repaired-judge`: the same stop as the reorg, with the head lowered to
  a shared ancestor below the fork point, as a native startup repair would
  leave it. The restart must refuse to complete the reorganization, name the
  one unresolved CANONICAL intent, and settle nothing; the verdict binds the
  refusal, the intent heads, the two member blocks and the lowered head.

## Commands

From the repository root, in a clean checkout (both take
`/tmp/extractor-harness-runner.lock`):

```sh
python3 test/native_faults/eth/faults_geth.py prepare --accepted-preparation PREPARATION.json --sha256 SHA --output OUT
python3 test/native_faults/eth/faults_geth.py run OUT/preparation.json --sha256 SHA --output RUN
python3 test/native_faults/eth/test_faults_geth.py
python3 test/mutate.py test/native_faults/eth/faults_geth.mutants.json --output RUN_DIR
```

`run` writes `probe/report.json`, `probe/faults-report.json` and
`run-record.json` with the verdict. The result is PASS only when every case
and every Geth handoff passes. INCOMPLETE means some cases or handoffs are
not run. `run` exits 0 on PASS and INCOMPLETE (the record is written);
`judge` exits 0 only on PASS.
