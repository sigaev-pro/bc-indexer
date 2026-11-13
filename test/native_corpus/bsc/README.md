# BSC (Parlia) block of the native smoke corpus

This directory is the BSC client block of E01-03.smoke_corpus (#19). It
generates the BSC corpus blocks with native BSC on the private Parlia
fixture, imports them through the accepted E01-02.decision_bridge import
path, exports the native output, and compares it with the independent
expected results of block 0 (`../expected/bsc.json`, [../ORACLE.md](../ORACLE.md)).
The comparator and the observation format are block 0's
([../README.md](../README.md)). This block changes no block 0 file and no
shared `admission`, `decision` or `source_inputs` file.

## Files

| File | Content |
|---|---|
| `corpus_test.go` | The corpus probe, `TestBSCCorpus`. The driver installs it in the accepted BSC decision package (`extractor/bsc/decision`), where it reuses the node, store and mirror helpers of the accepted native probe (`adapters/bsc/decision/native/go/native_test.go`) |
| `bsc_corpus.py` | The driver: `prepare`, `run`, `observe` |
| `sample/` | Probe outputs of a development run (not a measured record), used by the portable controls |
| `test_bsc_corpus.py` | Portable controls of the driver |
| `bsc_corpus.mutants.json` | Single-check mutants of the driver, run by `test/mutate.py` |

## Fixture

The genesis is the accepted private BSC genesis
(`test/harness/fixtures/bsc_admission/genesis.json`: the 24 allocations and 11
system-contract bytecodes of BSC `54afaff4` `tests/truffle/storage/genesis.json`
plus the public test-key-1 sender), with the corpus contracts of
`expected/bsc.json` (`genesis_additions`) added to the allocation, as
ORACLE.md "Fixtures" states. The sender is already in that genesis with the
same 10 BNB; the driver checks that and adds only the parent and the child.
No other fixture input is needed: the chain config, the fork blocks, the
validator and the genesis timestamp stay as in the accepted fixture. The
validator key is the public test keystore of the BSC source
(`bsc_admission.KEYSTORE`, password empty).

## What the probe does

`run` executes the probe binary three times, one container each, in the
pinned compiler image. Each step is a separate process:

1. **generate:** native Parlia seals blocks 1 and 2 on an in-memory chain with
   the validator key, as the accepted E01-01 BSC generator does. Block 1
   holds the three corpus transactions of ORACLE.md (transfer, nested call,
   direct revert), signed with public test key 1 and applied with native
   `core.ApplyTransaction`. Native `AssembleBlock` (Parlia
   `FinalizeAndAssemble`) appends the Parlia system transactions: the seven
   `init()` calls and the fee distribution. Block 2 is empty. The step writes
   `blocks.rlp` and `generated.json`.
2. **import:** a new node opens a new home with the accepted admission and
   decision bridge (`startNode` of the native probe: the capture profile, the
   Parlia system-call API, `Start`, the probe store and mirror). It runs the
   recovery driver (`Hook.Recover`, ending in `GoLive`) and imports both
   blocks through `InsertChain`. It records the admission and decision
   records, the delivery obligations, the open observations, the counters,
   the mode and the head.
3. **reimport:** the node starts again on the same home, recovers, records
   the same bridge state, imports both blocks again and records it once more.
   Then it exports the stored chain:
   - header fields, the body and the receipts from the native database;
   - the Parlia system transactions (`Parlia.IsSystemTransaction`) with their
     sender, nonce, recipient, value, data, gas, gas price and receipt status;
   - the state before and after each user transaction, from a native
     re-execution of the block prefix on the parent state
     (`TryUpdateBuildInSystemContract` as in `StateProcessor.Process`, then
     `core.ApplyTransaction` with the engine's author). Each re-executed
     receipt must match the stored one in status, gas used, cumulative gas
     and every log;
   - call frames from the native `callTracer` attached to that re-execution;
   - the validator nonce and the listed accounts at the block root.

The driver turns these outputs into the observation (`observation.json`) and
runs `corpus_compare.compare`:
- `decisions` are the decisions with a `NATIVE_DECIDED` record in the
  retained decision records, each member's block hash mapped to its number;
- `reimport` is the difference of the admission and decision counters
  before and after the re-import.

The run also fails if a bridge fact that the comparator does not see is
wrong:
- the import ends LIVE at block 2, with exactly one observation and one
  decision per block and no open observation;
- each decision is an IMPORT with one member, the members are the two
  corpus blocks with distinct observations, and each NATIVE_DECIDED record
  binds its intent;
- the obligations are those decisions and bind their intents;
- neither the restart nor the re-import changed the decision records, the
  obligations or the counters, and the re-import moved no head and reported
  no error.

A bridge failure marks every case FAIL in the run record.

The expected file gives the driver only the corpus accounts to add and the
account list to read, never a value.

## Commands

From the repository root (`M` is the accepted BuildManifest of D95):

```sh
python3 test/native_corpus/bsc/bsc_corpus.py prepare --build-manifest M --sha256 SHA --output PREP
python3 test/native_corpus/bsc/bsc_corpus.py run PREP/tool.json --sha256 SHA_OF_TOOL --output RUN
python3 test/native_corpus/bsc/bsc_corpus.py observe RUN/bsc --output NEW
python3 test/native_corpus/bsc/test_bsc_corpus.py
python3 test/mutate.py test/native_corpus/bsc/bsc_corpus.mutants.json --output DIR
```

`prepare` and `run` hold the host lock `/tmp/extractor-harness-runner.lock`
(`pins.owned_attempt`). Preparation (patches, install, vet and build of the
test binary) is not part of the measured run; generation, import, re-import,
export and the comparison are.

## Limits

- Private local BSC only (chain 714, one validator, blocks before Berlin and
  Plato). `bsc-mainnet` and `bsc-testnet` stay NOT_RUN (D96).
- The fee case is the distribution before Kepler under Istanbul gas rules
  (ORACLE.md, "Older fork paths").
- The state before and after each user transaction comes from a native
  re-execution of the block prefix, not from the import itself; the
  re-execution must reproduce every stored receipt. System transactions are
  not re-executed, so no state is read between them; the block-end state
  binds their combined result for the listed accounts.
- The mirror is the probe stand-in of the accepted BSC decision block
  (E05-01.registry owns the real one).
- `observe` compares a retained run again with the repository's expected
  file and writes only into a new directory; a retained record is never
  rewritten.
- Native BSC's own tests and the admission and decision checks are not
  repeated here; they belong to the accepted E01-02 records.
