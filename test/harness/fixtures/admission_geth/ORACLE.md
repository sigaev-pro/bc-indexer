# Oracle of the E01-02.admission Geth probe

The probe (`probe.go`) imports blocks through the patched Geth
`InsertChain` and reports what happened. The oracle (`compare` in
`admission_geth.py`, with `expected.json`) is written separately from the Go
code: it recomputes every digest from the formulas of the shared README
(`test/harness/admission.py`, `test/harness/source_inputs.py`) and checks the
saved bytes against the generated chain with its own Keccak-256 and RLP
decoder (`test/harness/source_inputs_geth.py`).

## Fixture

Private local chain from `genesis.json` (chain ID 31337, London rules,
Ethash faker, hash state scheme). Blocks 1 to 299 carry one transfer each and
are imported natively without admission before every scenario. Blocks 300
(`target`) and 301 (`next`) call the reader contract, which stores
BLOCKHASH(N-1), BLOCKHASH(N-256), BLOCKHASH(N-257) and BLOCKHASH(N-128) and
then calls the identity precompile with the 32-byte word of N. `bad` is block
300 with a wrong state root: Ethash faker accepts its header, and native
validation rejects it after execution with `invalid merkle root`.

## Checks on every observation

- Chain: every generated header hashes to its block hash and names its
  parent; the genesis hash equals `expected.json`.
- INPUT_PREPARING: observation key, sequence and candidate (number, block
  hash, parent hash, parent state root read from the generated parent
  header).
- The set of records equals the state: EXEC_READY has preparing, admitted
  and exec-ready; NATIVE_REJECTED has preparing, admitted and rejected;
  ADMITTED has preparing and admitted; ABANDONED has preparing and
  abandoned.
- ADMITTED: the capsule item names follow the layout (six head items, every
  window ancestor from `max(0, N-256)` to `N-2`, four tail items), the
  capsule digest recomputes from the item references, and the admitted digest
  recomputes from the key, sequence, candidate, pin digest and capsule
  digest.
- Pin: an EXEC_READY or ADMITTED observation has a live pin whose record
  hashes to the admitted pin digest, holds the parent root and names exactly
  the parent header and the store; an ABANDONED or NATIVE_REJECTED
  observation has no pin.
- EXEC_READY: the consumed digest is the digest of an empty log of this
  capsule, and the record digest binds the admitted digest, the consumed
  digest and the execution digest.
- NATIVE_REJECTED: the record digest binds the admitted digest and the
  evidence digest.
- Sequences and the counter, and the exact set of live pins, per attempt.
- Admission steps: the ordered trace events (`opened`, `abandoned`,
  `recovering`, `resumed`, `live`, `begin`, `pinned`, `sealed`, `admitted`,
  `exec_ready`, `rejected`, `recovered`).
- BLOCKHASH: for every executed reader block, the lookups are N-1, N-256 and
  N-128 in order (N-257 is outside the window and never asked), each found
  and answered with the generated hash of that block; the reader slots at the
  head hold those hashes and zero for N-257.
- Safe point: the captured CALL inputs are exactly the 32-byte words of the
  executed reader blocks, one per execution, so no prefetcher or other
  execution reached the safe point.
- Closing sync: every ADMITTED record is written after the seal and synced
  before any lookup.

In `clean-1`, every saved item of both capsules is checked byte for byte:
`header-rlp` and `parent-header` equal the generated headers, each
`ancestor-header-<n>` equals the generated header of block n, `parent-hash`
and `parent-root` match the parent, `encodings` equals `expected.json`,
`chain-config` is canonical (D99 item 3), decodes to the expected config and
keeps the literal of `terminalTotalDifficulty`, `fork-id` is the CRC-32 of the
genesis hash with next 0, and the native commit and profile match.

## Scenarios

| Scenario | Attempts | Expected |
|---|---|---|
| `clean` | `clean-1` imports 300 and 301; `clean-2-restart` restarts; the shipped `geth` runs five times; `clean-3-rewind` tries `SetHead(298)` and runs `ProcessBlock` of 301 without writing state, as `debug_executionWitness` does | Two EXEC_READY observations with pins, full capsules and windows; the restart opens the saved run `r1` and changes nothing; the rewind is refused with `live pins forbid this native deletion` and the head stays 301; the witness execution runs natively without an error and records nothing |
| shipped `geth` | `snapshot prune-state`; start with an interrupted-prune marker; start with the config of another store ID; start with the config of another producer; start without a config | Each exits non-zero before the timeout with, in order, the live-pins refusal (twice), `native store identity mismatch: expected binding` (the Preflight refusal), `store belongs to another producer run` and `store is bound to capture producer store` |
| `begin_before_counter_sync` | Power loss in the counter sync of Begin, then restart and import 300 | No observation survives; the retry admits sequence 0 |
| `begin_after_counter_sync` | Power loss right after the counter sync, before the pin; restart | The INPUT_PREPARING observation without a pin is abandoned; sequence 1 reaches EXEC_READY |
| `admit_before_sync` | Power loss in the closing ADMITTED sync, after the pin and the seal (AC-CAP-02-01) | Nothing was admitted; the restart abandons sequence 0 and releases its pin; sequence 1 reaches EXEC_READY |
| `admit_after_sync` | Power loss right after the closing ADMITTED sync (AC-CAP-02-02); a held restart; a restart with another normalization profile; a recovering restart that then imports 301 | The held restart is in RECOVERY_ONLY with sequence 0 ADMITTED and pinned, refuses the competing block `bad` (`admission mode forbids this step`) and GoLive (`an observation is still pending`), and executes nothing; the restart with another profile stops with `admitted obligation was not resolved` and changes no record or pin; recovery completes the same ADMITTED record to EXEC_READY, then 301 is admitted as sequence 1 |
| `exec_ready_before_sync` | Power loss in the EXEC_READY sync; restart | Recovery re-executes and writes EXEC_READY with the same execution digest as `clean-1`; it stores block 300 with its state and keeps the head at 299 |
| `without_set_head` | Import 300 with `InsertBlockWithoutSetHead` (engine `newPayload`), then `SetCanonical(300)` | One EXEC_READY observation; `SetCanonical` makes 300 the head without a second execution |
| `reject_live` | Import `bad`, then 300 | `bad` ends NATIVE_REJECTED without a pin and without EXEC_READY; 300 is admitted as sequence 1 |
| `reject_after_admit` | Power loss right after the ADMITTED sync of `bad`; restart, then import 300 | Recovery ends the saved obligation as NATIVE_REJECTED; 300 is admitted as sequence 1 |
| `freezer` | A pinned synthetic side-branch header at block 101 forking from 100, the finalized marker at 250, reopen | The native chain freezer freezes 101 blocks (0 to 100), and the side header and the canonical fork block stay readable |

A crash attempt must end killed at its named crash point with no result.
Before a crash inside Begin no trace event is written; a crash in Admit comes
after `begin`, `pinned` and `sealed`, and no `admitted` or `exec_ready` event
appears before it.

## Mutants

`native_mutants.json` lists native mutants of the patch set and of
`geth.go`. Each must compile and must fail this oracle in the measured run.
