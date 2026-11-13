# ETH (Geth) client block of the native smoke corpus

This directory is the ETH client block of E01-03.smoke_corpus (#19). It
produces the ETH observation in the format of
[../README.md](../README.md#observation-format) from native Geth output and
compares it with the block 0 oracle, `expected/eth.json`. It changes no
oracle, admission, decision or source_inputs file.

| File | Role |
|---|---|
| `probe.go` | The Go probe, installed as `cmd/extractor-corpus-eth` in the accepted patched Geth tree |
| `corpus_eth.py` | `prepare` builds the probe from the accepted Geth decision-bridge preparation; `run` executes it, adds `source`, checks the native self-checks and runs `corpus_compare.py` |
| `test_corpus_eth.py` | Portable controls of the driver, and the probe inputs against ORACLE.md |
| `corpus_eth.mutants.json` | Single-check mutants of the driver's native self-checks and source rule |

## Accepted build

`prepare` takes the Geth decision-bridge preparation of E01-02.decision_bridge
that the accepted records 03 at `ca88680` used (pinned by the SHA-256 of its
`preparation.json`). It extracts the same Geth `1df30b18` source archive,
applies the admission and decision-bridge patch sets from the copies that
preparation retains, and installs the same extractor packages. It refuses
unless every retained copy matches its record, every installed patch and
file equals the accepted install record, and the repository's Geth adapter
code is byte-equal to those copies. Only the probe is added. The probe is
compiled offline with the accepted compiler image and the accepted module
cache.

## What the probe does

1. Inputs. It reads the fixture genesis (`test/harness/fixtures/genesis.json`,
   chain 31337, London at genesis, ethash, gas limit 10,000,000, base fee
   1 gwei). The sender, public test key 1 with 10 ETH, is already there. The
   probe adds the parent and child contracts of ORACLE.md and refuses if any
   corpus address other than the sender is already allocated. It generates
   blocks 1 and 2 with `core.GenerateChainWithGenesis` and the ethash faker,
   with coinbase `0x2000…0000`. Block 1 holds the four corpus transactions,
   signed with key 1: the transfer, the dynamic-fee transfer, the call of
   the parent and the direct call of the child. Block 2 is empty.
2. Live import. It opens a new node store with the shipped attach calls
   (`Preflight` on the instance directory, `core.NewBlockChain`, then
   `AttachDecisions` with `Options{}` and `DecisionOptions{}`, the arguments
   of the `backend_attach_decisions` patch). The blockchain config is
   `core.DefaultConfig()` (hash state scheme), not the node's flags, and the
   engine is the ethash faker. It then imports both blocks with
   `InsertChain`, so each block goes through the patched `ProcessBlock`:
   admission, EXEC_READY, intent, native write and NATIVE_DECIDED. The chain is stopped and the store closed.
3. Restart and re-import. It opens the same store the same way, which runs
   the recovery driver, and imports the same two blocks again. Both blocks
   are canonical with their state, so Geth skips them natively as known
   blocks before `ProcessBlock`; the check is that the restart opens no
   observation and no decision and leaves the decision records unchanged
   (the decision-bridge README, "One decision per block").
4. Export, on the restarted chain:
   - blocks, receipts and logs, read with `rawdb.ReadBlock` and
     `rawdb.ReadReceipts`;
   - `frame` and `state_before`/`state_after`, from one native
     re-execution of each block on its parent state with
     `core.ApplyTransactionWithEVM`, traced by the native `callTracer`
     (`tracers.DefaultDirectory.New("callTracer")`). The probe reads the five
     corpus accounts (balance, nonce, code, and slots 0 to 3 of the
     contracts) before and after each transaction. The re-execution then
     applies the ethash `Finalize` (the block reward). Each re-executed
     receipt must equal the imported one in status, gas used, cumulative
     gas, logs and effective gas price (computed from the transaction and
     the base fee, since `ApplyTransactionWithEVM` leaves it unset), and the
     final root must equal the header root. Bloom and type are compared
     too, but both sides derive them from the logs and the transaction, so
     they add no independent check;
   - `state_after_block`, native reads at the block root;
   - `decisions`, one row per decision record with an intent and a
     NATIVE_DECIDED record from the live import, naming the chain number of
     its single member (an intent with another member count stops the
     probe; an intent without a NATIVE_DECIDED record is listed in
     `native.undecided_intents`);
   - `reimport`, the growth of the admission and decision counters during
     the restart import.

The probe writes no expected value. The `native` section of the
observation keeps the import results, the full admission and decision
records before and after each import, and the re-execution checks.

The driver refuses a PASS (`native_failures`, `verdict`) unless both imports
attached and inserted without error up to block 2; the store held no
extractor record before the live import; the admission and decision
counters each count one sequence per block; the restart saw exactly the
records the live import left and did not change the decision records; no
intent is undecided; the re-execution checks one receipt per body
transaction of every block, each equal, with equal gas and roots; and the
comparison is PASS with exit 0.

## Commands

From the repository root, in a clean checkout, under the host lock (both
commands take `/tmp/extractor-harness-runner.lock` and refuse to start when
another runner holds it):

```sh
python3 test/native_corpus/eth/corpus_eth.py prepare --accepted-preparation PREPARATION.json --sha256 SHA --output OUT
python3 test/native_corpus/eth/corpus_eth.py run OUT/preparation.json --sha256 SHA --output RUN
python3 test/native_corpus/eth/test_corpus_eth.py
python3 test/mutate.py test/native_corpus/eth/corpus_eth.mutants.json --output RUN_DIR
```

`run` writes `observation.json`, `comparison.json` (the `corpus_compare.py`
report) and `run-record.json`. The run is PASS only when the comparison is
PASS and every native self-check holds.

## Limits

- The blocks are sealed by the ethash faker, as in the accepted Geth probes:
  the import does not verify proof of work.
- `state_before` and `state_after` come from a native re-execution on the
  parent state, which the observation format allows and `source.tool`
  names. The re-execution must reproduce the imported receipts and root.
- The probe runs the live import and the re-import in one process and calls
  `AttachDecisions` directly with `core.DefaultConfig()`; it does not start
  the `geth` command.
- The re-import is skipped natively as known blocks, so it shows that the
  restart adds nothing, not that `ProcessBlock` refuses a second decision;
  the decision-bridge probe covers the known-block refusal.
- System-action is NOT_APPLICABLE for ETH (ORACLE.md). The fixture is before
  the merge. Every public cell (`ethereum-*`) stays NOT_RUN (D96).
