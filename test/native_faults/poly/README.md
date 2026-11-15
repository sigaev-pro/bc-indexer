# Bor fault probe (E01-04.early_faults, Bor block)

| File | Role |
|---|---|
| `faults_poly.py` | `prepare` builds the probe from the accepted Bor build manifest (the accepted Bor lifecycle install plus the faults patch set); `run` executes it under the host lock on the accepted smoke corpus POLY blocks and judges the report; `judge` judges a saved probe report |
| `main.go` | The probe, installed as `cmd/extractor-faults-bor` with the Heimdall stub in its `heimdallstub` subdirectory |
| `test_faults_poly.py` | Portable controls of the driver and of the copied probe code (run by `bootstrap/ci`) |
| `faults_poly.mutants.json` | Single-check mutants of the driver judgement |
| `sample_report.json` | A development run of the probe, compacted (one shared block export and state sync list, each case's own decisions and re-import counts); used by the portable tests, not measured evidence |

The patch set is in
[adapters/bor/faults/native](../../../adapters/bor/faults/native/README.md).
The fault model, catalog and judge are block 0:
[test/native_faults/README.md](../README.md). The design follows the Geth
and BSC probes ([eth](../eth/README.md), [bsc](../bsc/README.md)); this file
names the Bor differences.

## What the probe does

Inputs, all from the accepted E01-03 POLY run (its run record is pinned by
SHA-256, and its preparation by the run record):

- the corpus blocks 1 to 4 (`blocks.rlp`): block 1 holds the corpus
  transactions, blocks 2 and 3 are empty, and block 4 is the first sprint
  start, with a state sync event;
- the genesis and chain config of the private Bor chain 15001, with the
  corpus accounts;
- the Heimdall stub bodies.

For each catalog point and each variant (30 cases), in a child process of
its own:

1. **Start.** A capture node starts as the Bor lifecycle probe starts one:
   - the node stack;
   - the store identity preflight (`borimport.Preflight`); a no-op here,
     because the new OS data directory has no store (evidence, Limits);
   - the native backend in the sequential capture profile, with Heimdall
     answered by the in-process stub;
   - `wire.Start`: the admission, the decision bridge with the lifecycle
     gate, the live observer.

   The chaindata opens on `vfs.NewStrictMem` (pebble v1, through
   `pebble.ExtractorFS`), with the created directories synced. The fault
   store wraps the key-value store through `node.ExtractorWrap`. The freezer
   stays on the OS file system. A new store starts in BOOTSTRAP (D105).
2. **Scenario and fault.** As on Geth and BSC:
   - H points: block 1 imports in BOOTSTRAP, and the fault comes inside
     `wire.Run.GoLive`.
   - Other points: `GoLive` at the genesis, and the fault comes inside the
     import of block 1.

   The fault store, its batch, the arming table and the catalog order are
   the Geth probe's. The Bor tree uses the same record prefixes.
3. **Abandon and copy.** The probe abandons the node: no stop and no store
   close. It copies the chaindata files into a new strict file system, as on
   BSC:
   - for a crash, as they are, after waiting for the WAL files to stop
     growing (no sync);
   - for a power loss, after `SetIgnoreSyncs(true)` and
     `ResetToSyncedState`.

   The abandoned node holds the lock of its OS data directory. So the new
   node starts on a new OS data directory, and the copy puts the files at
   that node's chaindata path.
4. **States after the restart** are read as on Geth (below the durable
   counter), before the new node starts.
5. **Restart.** The process-global lifecycle hooks are cleared first, as a
   new process starts without them: `rawdb.ExtractorRequire`,
   `rawdb.ExtractorFinality` and `whitelist.ExtractorEnter`. The abandoned
   node set them. Then a new node starts on the copy.
6. **Opening mode.** `wire.Start` recovers a store that has a boundary and
   goes live at once. So the opening mode is BOOTSTRAP when the new node
   waits in BOOTSTRAP. Otherwise it is the phase that the startup reconcile
   reports from the opened gate (`borlifecycle.Node.Report().Phase`).
7. **Recovery, import, export.**
   - `GoLive`, only when the node waits in BOOTSTRAP.
   - The import of blocks 1 to 4, one at a time, with the state sync data
     the native engine records for each block.
   - The final boundary (read from the store), header at H, mode, head and
     decided members.
   - A re-import that must add no observation or decision.
   - The native export of each block (`exportChain`, copied from
     `test/native_corpus/poly/main.go`).

A portable test keeps each copied declaration equal to its source:

- the inputs, the node start helpers and the export, from the POLY corpus
  probe;
- the fault store and the state reading, from the Geth probe;
- the decision rows, from the BSC probe: one row per member.

The storage helpers take the chaindata directory, because each Bor node has
its own path. A portable test pins `settleWAL`, `freshFS` and `copyFS` as
the BSC probe's with only the directory changed. `syncDirs` syncs the
directory and every parent up to `/` (BSC syncs its fixed path); a portable
test pins its text, and others pin the fail-closed steps: a failed child's
row, the fault store guard and the opening mode.

**Why child processes.** An abandoned node is never freed. Sixty Bor nodes,
each with its caches, would not fit in one process in the runtime memory
limit. A child that fails is recorded with the end of its error output
(`child_error`).

`faults_poly.py judge` builds each case's observation as
`poly_corpus.observation_of` does, with the state sync events of the
import. It compares the observation with
`test/native_corpus/expected/poly.json` by `corpus_compare.py`, without only
the per-block `decisions` rows. Any of
these probe errors fails the result:

- the storage model;
- a malformed case;
- a child, restart, `GoLive` or import error;
- undecided intents (the list must be empty);
- re-import or export errors;
- an export without one row per block. The export's native re-execution
  stops on a receipt that differs from the stored one.

## Commands

From a clean checkout outside the repository root's records (both take
`/tmp/extractor-harness-runner.lock`):

```sh
python3 test/native_faults/poly/faults_poly.py prepare BUILD_MANIFEST --sha256 SHA --output OUT
python3 test/native_faults/poly/faults_poly.py run OUT/tool.json --sha256 SHA --corpus-run CORPUS_RUN_RECORD.json --corpus-sha256 SHA --output RUN
python3 test/native_faults/poly/test_faults_poly.py
python3 test/mutate.py test/native_faults/poly/faults_poly.mutants.json --output RUN_DIR
```

`run` writes `run.json` with the verdict, `bor/work/report.json` and
`bor/work/faults-report.json`. The attempt records PASS when the probe ran
and the verdict is PASS or INCOMPLETE; a FAIL verdict fails the attempt.
`prepare` and `run` exit 0 only on a PASS attempt, 1 on a failed attempt and
2 on a refusal before any record exists; `judge` exits 0 only on a PASS
verdict.
