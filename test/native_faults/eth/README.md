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
   - the decision intent or decided record.

   It then panics at the next `SyncKeyValue`: before syncing for a
   "before" point, after syncing for an "after" point. For
   `dec.in-native-mutation` it panics at the first native write after the
   synced intent. The panic unwinds the native caller, so no later code of
   the step runs.
4. **Abandon and reopen.** The probe abandons the node (no
   `BlockChain.Stop`, no store close). It reopens on
   `CrashClone(UnsyncedDataPercent: 0)` for a power loss and
   `CrashClone(UnsyncedDataPercent: 100, RNG)` for a crash.
   `unsynced_dropped` is the fault store's count of writes since the last
   completed sync, an upper bound.
5. **States after the restart.** They are read from the raw records before
   any attach: the boundary, the native canonical header at its number, and
   the sequence 0 records of admission, pins and decisions. The opening mode
   follows the gate rule (RECOVERY with a boundary, BOOTSTRAP without). The
   shipped attach then finishes recovery and goes live itself; its phase is
   kept as `attach_phase`.
6. **Recovery and import.** `GoLive` runs, then blocks 1 and 2 import, then
   the probe reads the final boundary, the native header at H, the mode,
   the head and the decided members. Last come the corpus observation (the
   native export of `test/native_corpus/eth/probe.go`) and a re-import of
   both blocks, which must add no observation or decision.

`faults_geth.py judge` turns the probe report into the block 0 faults
report. It compares each case's observation with
`test/native_corpus/expected/eth.json` by `corpus_compare.py`, without the
decision rows (see the block 0 README, "Report format"). It adds the probe's
own step errors (restart, `GoLive`, import, undecided intents, re-import,
native re-execution), and any of them fails the result.

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
not run.
