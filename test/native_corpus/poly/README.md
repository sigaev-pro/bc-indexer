# POLY (Bor) block of the native smoke corpus

This directory is the POLY client block of E01-03.smoke_corpus (#19). It
generates the POLY corpus blocks with native Bor on the private fixture,
imports them through the accepted E01-02.decision_bridge import path,
exports the native output, and compares it with the independent expected
results of block 0 (`../expected/poly.json`, [../ORACLE.md](../ORACLE.md)). The
comparator and the observation format are block 0's
([../README.md](../README.md)); this block changes no block 0 file and no
shared `admission`, `decision` or `source_inputs` file.

## Files

| File | Content |
|---|---|
| `main.go` | The corpus probe. It builds only in the accepted Bor module graph after the admission and decision patch sets are installed |
| `poly_corpus.py` | The driver: `prepare`, `run`, `observe` |
| `stub/` | The Heimdall stub bodies: spans and status as in the accepted admission fixture, and the one state sync event of the oracle |
| `sample/` | Probe outputs of a development run (not a measured record), used by the portable controls |
| `test_poly_corpus.py` | Portable controls of the driver |
| `poly_corpus.mutants.json` | Single-check mutants of the driver, run by `test/mutate.py` |

## Fixture

The genesis is `tests/bor/testdata/genesis.json` of the accepted Bor source
(`3a018e38`) with the corpus accounts of `expected/poly.json`
(`genesis_additions`) added to the allocation, as ORACLE.md "Fixtures"
states. One chain-config value is added too: `bor.stateSyncConfirmationDelay`
`{"0": 0}`. The test genesis activates Indore at block 3 and sets no delay,
and native Bor then panics at the first state sync after Indore
(`CalculateStateSyncDelay` reads an empty map, `params/config.go`
`borKeyValueConfigHelper`). The accepted admission fixture uses the same
value (`state_sync_delay` 0 in `test/harness/fixtures/admission_bor/expected.json`,
added by `derived_inputs` in `test/harness/bor_admission_probe.py`). With
delay 0 the state sync query of block 4 ends at its time, nine seconds after
the genesis, and the stub's event (one second after the genesis) lands at
block 4 as the oracle states; any delay of at most seven seconds would give
the same result. ORACLE.md "Fixtures" lists only the accounts; this value is
the one fixture input it does not name, and the evidence records it. The genesis timestamp, the gas limit and every
fork block stay as in the test genesis.

The Heimdall stub is the in-process stub of the E01-02.inputs Bor probe (D98
item 6), with this directory's bodies. It serves exactly one state sync
event: id 1, contract `3000000000000000000000000000000000000003`, data
`extractor-corpus-state-sync-1`, recorded one second after the genesis time
(before block 4).

## What the probe does

`run` executes three probe commands, one container each, in the pinned
runtime image:

1. **generate:** a native Bor node seals blocks 1 to 4 with the tracked span-0
   test signer, as the admission probe does. Block 1 holds the four corpus
   transactions signed with public test key 1. The probe applies them with
   native `core.ApplyTransaction` (the signer as the execution beneficiary),
   then native `FinalizeAndAssemble` and `Seal`; Bor's miner is not used, and
   the probe sets the header fields (the time, the base fee and, at the
   London block, the gas limit of 20,000,000) that native verification then
   accepts on import. Blocks 2 and 3
   are empty, and block 4, the first sprint start, commits the state sync
   event that the stub serves.
2. **import:** a new native node on a new instance starts with the admission
   (`borimport.Start`, with the startup identity) and the decision bridge
   (`bordecision.Start`: the recovery driver, then `GoLive`). It imports each
   block through the product import path (`InsertChain`) with the admission
   observer. It records:
   - the observation key of each block;
   - the state sync data that native Bor's `Finalize` records for the block
     (`HeaderChain.GetStateSync`);
   - the bridge's decisions and delivery obligations.
3. **reimport:** the node starts again on the same instance, with recovery,
   then live. It imports the four blocks again and counts the observations
   that open and the decisions that are new. Then it exports the stored
   chain:
   - header fields and receipts from the native database;
   - call frames from the native `callTracer` through `tracers.API.TraceTransaction`;
   - state before and after each transaction, from a native re-execution of
     the block prefix on the parent state (the processor's pre-transaction
     system calls, then `core.ApplyTransaction`, author from the engine, the
     recovered signer). Each re-executed receipt must match the stored one in
     status, gas used, cumulative gas and every log (address, topics and
     data); the effective gas price is derived, not executed, and is read
     from the stored receipt;
   - block-end state at the block root;
   - the state sync receipt (`rawdb.ReadBorReceipt`) and `lastStateId()` of
     `0x1001` through the native genesis contracts client.

The driver turns these outputs into the observation (`observation.json`) and
runs `corpus_compare.compare`:
- `decisions` are the `NATIVE_DECIDED` decisions of the import, each
  member's block hash mapped to its number;
- `reimport` counts the re-import's observations and the decision sequences
  that are new after the restart.

The run also fails if a bridge fact that the comparator does not see is
wrong:
- each block has exactly one observation;
- the decision members are those observations;
- the obligations are the `IMPORT` decisions;
- neither the restart nor the re-import changed the decisions or the
  obligations;
- the re-import moved no head and reported no error.

A bridge failure also marks every case FAIL in the run record.

The expected file gives the driver only the corpus accounts to add and the
account list to read, never a value.

## Commands

From the repository root (`M` is the accepted BuildManifest of D95):

```sh
python3 test/native_corpus/poly/poly_corpus.py prepare --build-manifest M --sha256 SHA --output PREP
python3 test/native_corpus/poly/poly_corpus.py run PREP/preparation.json --sha256 SHA_OF_PREP --output RUN
python3 test/native_corpus/poly/poly_corpus.py observe RUN --output NEW
python3 test/native_corpus/poly/test_poly_corpus.py
python3 test/mutate.py test/native_corpus/poly/poly_corpus.mutants.json --output DIR
```

`prepare` and `run` hold the host lock `/tmp/extractor-harness-runner.lock`.
Preparation (install, `go mod verify`, `go vet` and build of the probe) is
not part of the measured run; generation, import, re-import, export and the
comparison are.

## Limits

- Private local POLY only (chain 15001, one validator, stub Heimdall). Polygon
  Mainnet and Amoy stay NOT_RUN (D96).
- The fixture runs the state sync before Madhugiri: a separate state sync
  receipt, not a body transaction (ORACLE.md, "Older fork paths").
- The state before and after each transaction comes from a native
  re-execution of the block prefix, not from the import itself; the
  re-execution must reproduce every stored receipt.
- `observe` compares a retained run again with the expected file that run
  used and writes only into a new directory; a retained record is never
  rewritten.
- A bridge failure fails the run and every case (a whole-observation
  difference, as block 0's comparator treats one).
- The state sync event comes from the engine's `Finalize` record. The
  `BlockChain` state sync feed is not used, because this native import path
  never sets the data it sends.
- The re-import proves that native Bor skips a stored block before
  observation. No positive control in the same process shows that the
  observer would open an observation for a new block; the import process
  shows that it does.
- Native Bor's own unit tests and the admission and decision checks are not
  repeated here; they belong to the accepted E01-02 records.
