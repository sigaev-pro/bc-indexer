# Native smoke corpus (E01-03.smoke_corpus)

A minimal corpus of native execution cases for ETH (Geth), BSC (Parlia) and
POLY (Bor), each with independent expected results:

| Case | ETH | BSC | POLY |
|---|---|---|---|
| transfer | yes | yes | yes |
| nested-revert | yes | yes | yes |
| fee | dynamic-fee burn and tip | fee accumulator and the Parlia distribution | burn to the burnt contract, tip, Bor fee log |
| system-action | NOT_APPLICABLE (no native system transaction) | Parlia `init()` system transactions | Bor StateSync |
| empty-block | yes | yes | yes |

This corpus is small. It is not full production semantic coverage. TC02, the
BOOT-08 catalog and the native type matrix of E01-05.type_paths stay with
their owners. Public profiles stay NOT_RUN (D96).

## Files

- [ORACLE.md](ORACLE.md): the fixtures, contracts, gas and fee rules and the
  hand arithmetic, fixed before any native run.
- [corpus_evm.py](corpus_evm.py): a small reference executor for the opcodes the
  corpus contracts use, with Istanbul and London gas rules. It refuses every
  other opcode.
- [corpus_oracle.py](corpus_oracle.py): computes `expected/eth.json`,
  `expected/bsc.json` and `expected/poly.json`.
- [corpus_compare.py](corpus_compare.py): compares one observation with one expected file.
- [test_native_corpus.py](test_native_corpus.py): portable controls (the
  EIP-2200 and EIP-3529 gas tables, the hand arithmetic, a perfect synthetic
  observation per family and single-field mismatches, format errors).
- [native_corpus.mutants.json](native_corpus.mutants.json): one mutant per
  checked condition, run by `test/mutate.py`.

Commands, from the repository root:

```sh
python3 test/native_corpus/corpus_oracle.py check
python3 test/native_corpus/test_native_corpus.py
python3 test/native_corpus/corpus_compare.py test/native_corpus/expected/eth.json OBSERVATION.json --report REPORT.json
python3 test/mutate.py test/native_corpus/native_corpus.mutants.json --output RUN_DIR
```

## Independence

The expected files come only from `corpus_oracle.py`, `corpus_evm.py` and the rules in
ORACLE.md. Neither file reads a native output, and `corpus_compare.py` never changes
an expected value from an observation. A client block that finds a
difference records the FAIL under D83. It changes the oracle only with a
reviewed reason taken from the protocol rules or the native source, never to
match an observed value.

## Observation format

A client block writes one JSON observation per family from native output.
Addresses are 40 lowercase hex digits and byte strings are lowercase hex,
both without `0x`; amounts are decimal strings; counters are integers.

```text
{
  "schema_version": 1, "kind": "native-corpus-observation",
  "family": "eth" | "bsc" | "poly", "chain_id": <int>,
  "source": {<native build, tool and record digests>},
  "blocks": [{
    "number", "hash", "state_root", "base_fee" (decimal or null), "gas_limit", "gas_used",
    "body_tx_count": <transactions in the block body, system transactions included>,
    "transactions": [{                      # user transactions, in body order
      "index", "hash", "type", "from", "nonce", "to", "value", "gas",
      "gas_price" | "max_fee_per_gas" and "max_priority_fee_per_gas", "data",
      "receipt": {"status", "gas_used", "cumulative_gas_used", "effective_gas_price",
                  "logs": [{"address", "topics", "data"}]},
      "frame": {"type", "from", "to", "value", "input", "output", "error", "calls": [...]},
      "state_before": {<address>: {"balance", "nonce", "code", "storage": {"<slot>": <64 hex>}}},
      "state_after": {...}
    }],
    "system_transactions": [{"index", "from", "nonce", "to", "value", "data", "gas", "gas_price", "status"}],
    "system_nonce_after": {<address>: <nonce>},
    "state_sync": [{"id", "logs", "state_receiver_last_state_id"}],
    "state_after_block": {<address>: {...}}
  }],
  "decisions": [{"block", "kind"}],          # NATIVE_DECIDED records of the live import
  "reimport": {"new_observations", "new_decisions"}   # after a restart and a re-import
}
```

How a client block fills it:
- blocks, receipts and logs: the native chain database after the import
  through the accepted E01-02.decision_bridge path;
- `frame`: the client's native call tracer (`callTracer`) for the
  transaction; `error` is the tracer's error text or null;
- `state_before` and `state_after`: native state reads at the parent root and
  after each transaction (a native re-execution of the block prefix on the
  parent state is allowed and must be named in `source`); `state_after_block`:
  native reads at the block root, for every account the expected file lists;
- `system_transactions` (BSC): the body transactions sent by the validator,
  with their receipt status; `system_nonce_after`: the native nonce;
- `state_sync` (POLY): the state sync receipt logs and the native result of
  `lastStateId()` on `0x1001`;
- `decisions` and `reimport`: the decision bridge records of the run.

The comparator joins blocks by number and transactions by index, reports
each difference with its place (block and transaction index or system kind),
and gives each case PASS, FAIL or NOT_APPLICABLE. A whole-observation or
block-level difference fails every case in that block.
