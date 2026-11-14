# BSC fault probe (E01-04.early_faults, BSC block)

| File | Role |
|---|---|
| `faults_bsc.py` | `prepare` builds the probe test binary from the accepted BSC build manifest (the accepted BSC lifecycle install plus the faults patch set); `run` executes it under the host lock on the accepted smoke corpus BSC blocks and judges the report; `judge` judges a saved probe report |
| `faults_test.go` | The probe, `TestBSCFaults`, installed as `extractor/bsc/faults/faults_test.go` (package `bscfaults_test`) |
| `test_faults_bsc.py` | Portable controls of the driver (run by `bootstrap/ci`) |
| `faults_bsc.mutants.json` | Single-check mutants of the driver judgement |
| `sample_report.json` | A development run of the probe, compacted (one shared block export, each case's own decisions and re-import counts); used by the portable tests, not measured evidence |

The patch set is in
[adapters/bsc/faults/native](../../../adapters/bsc/faults/native/README.md).
The fault model, catalog and judge are block 0:
[test/native_faults/README.md](../README.md). The design follows the Geth
probe ([test/native_faults/eth](../eth/README.md)); this file names the BSC
differences.

## What the probe does

Inputs: the smoke corpus BSC blocks 1 and 2 of the accepted E01-03 BSC run
(`blocks.rlp`, native Parlia blocks with the corpus transactions and the
system transactions) and its genesis (the private Parlia chain 714 with the
corpus contracts), both pinned by the SHA-256 of that run's `run.json`.

For each catalog point and each variant (30 cases), on a fresh store:

1. **Open.** The chaindata opens on `vfs.NewStrictMem` (pebble v1, through
   `pebble.ExtractorFS`), with the created directories synced. A fault store
   wraps the key-value store. A harness chain (Parlia with the system-call
   API, hash scheme, as the BSC lifecycle probe's harness store) gets the
   shipped `bsclifecycle.Attach`. The freezer stays on the OS file system in
   a new directory per open.
2. **Scenario and fault.** As on Geth. H points: block 1 imports in
   BOOTSTRAP and the fault comes inside `GoLive`. Other points: `GoLive` at
   the genesis and the fault inside the import of block 1. The fault store
   (with its batch, the arming table and the catalog order) is the Geth
   probe's; a portable test compares every declaration.
3. **Abandon and copy.** The probe abandons the node: no `BlockChain.Stop`
   and no store close. Closing a pebble v1 store under a live chain panics
   on the chain's open iterators, and a close would flush Pebble's in-process
   WAL buffer, which a process crash does not do. Pebble v1's `MemFS` has no
   crash clone, so the probe copies the chaindata files into a new strict
   file system: as they are for a crash (after waiting for the WAL files to
   stop growing, no sync), and after `SetIgnoreSyncs(true)` and
   `ResetToSyncedState` for a power loss.
4. **States after the restart** are read as on Geth (below the durable
   counter). The opening mode is the gate's phase from the `gate-opened`
   event of `bsclifecycle.Attach`.
5. **Recovery, import, export.** `GoLive`, the import of blocks 1 and 2, the
   final boundary, header at H, mode, head and decided members, then the
   native export of each block (`exportBlock`, copied from
   `test/native_corpus/bsc/corpus_test.go`; a portable test keeps it equal)
   and a re-import that must add no observation or decision.

A decided intent can have no member: a BSC import also records FINALITY
decisions. As in `bsc_corpus.observation_of`, the decision rows have one row
per member, so such a decision gives no row.

`faults_bsc.py judge` builds each case's observation as
`bsc_corpus.observation_of` does and compares it with
`test/native_corpus/expected/bsc.json` by `corpus_compare.py`, without only
the per-block `decisions` rows. Any of these probe errors fails the result:

- the storage model;
- a malformed case;
- restart, `GoLive` or import errors;
- undecided intents (the list must be empty);
- re-import errors;
- an export without one row per block. The export's native re-execution
  stops the test on a receipt that differs from the stored one.

## Commands

From a clean checkout outside the repository root's records (both take
`/tmp/extractor-harness-runner.lock`):

```sh
python3 test/native_faults/bsc/faults_bsc.py prepare BUILD_MANIFEST --sha256 SHA --output OUT
python3 test/native_faults/bsc/faults_bsc.py run OUT/tool.json --sha256 SHA --corpus-run CORPUS_RUN.json --corpus-sha256 SHA --output RUN
python3 test/native_faults/bsc/test_faults_bsc.py
python3 test/mutate.py test/native_faults/bsc/faults_bsc.mutants.json --output RUN_DIR
```

`run` writes `run.json` with the verdict, `bsc/work/report.json` and
`bsc/work/faults-report.json`. The attempt records PASS when the probe ran
and the verdict is PASS or INCOMPLETE; a FAIL verdict fails the attempt.
`prepare` and `run` exit 0 only on a PASS attempt, 1 on a failed attempt and
2 on a refusal before any record exists; `judge` exits 0 only on a PASS
verdict.
