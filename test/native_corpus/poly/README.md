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
`borKeyValueConfigHelper`). The accepted admission fixture derives the same
value (`test/harness/bor_admission_probe.py` `derived_inputs`, D99 item 3).
With delay 0 the state sync query ends at the block time, so the value
changes no expected result. The genesis timestamp, the gas limit and every
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
   transactions signed with public test key 1; the native producer path
   applies them with the signer as the execution beneficiary. Blocks 2 and 3
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
     the block prefix on the parent state (`core.ApplyTransaction`, author
     from the engine, the recovered signer). Each re-executed receipt must
     equal the stored receipt;
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
- the restart did not change the decisions;
- the re-import moved no head and reported no error.

The expected file gives the driver only the corpus accounts to add and the
account list to read, never a value.

## Commands

From the repository root (`M` is the accepted BuildManifest of D95):

```sh
python3 test/native_corpus/poly/poly_corpus.py prepare --build-manifest M --sha256 SHA --output PREP
python3 test/native_corpus/poly/poly_corpus.py run PREP/preparation.json --sha256 SHA_OF_PREP --output RUN
python3 test/native_corpus/poly/poly_corpus.py observe RUN
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
- The state sync event comes from the engine's `Finalize` record. The
  `BlockChain` state sync feed is not used, because this native import path
  never sets the data it sends.
- The re-import proves that native Bor skips a stored block before
  observation. No positive control in the same process shows that the
  observer would open an observation for a new block; the import process
  shows that it does.
- Native Bor's own unit tests and the admission and decision checks are not
  repeated here; they belong to the accepted E01-02 records.
