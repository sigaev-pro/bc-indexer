# Oracle of the E01-02.admission and E01-02.decision_bridge Geth probe

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
| `clean` | `clean-1` imports 300 and 301; `clean-2-restart` restarts; the shipped `geth` runs five times on this store; `clean-3-rewind` tries `SetHead(298)` and runs `ProcessBlock` of 301 without writing state, as `debug_executionWitness` does | Two EXEC_READY observations with pins, full capsules and windows; the restart opens the saved run `r1` and changes nothing; the rewind is refused with `live pins forbid this native deletion` and the head stays 301; the witness execution runs natively without an error and records nothing |
| shipped `geth` | `snapshot prune-state`; start with an interrupted-prune marker; start with the config of another store ID; start with the config of another producer; start without a config; `geth import` of block 301 into the `admit_before_sync` store | Each exits non-zero before the timeout with, in order, the live-pins refusal (twice), `native store identity mismatch: expected binding` (the Preflight refusal), `store belongs to another producer run`, `store is bound to capture producer store`, and the live-pins refusal of the manual database guard |
| `begin_before_counter_sync` | Power loss in the counter sync of Begin, then restart and import 300 | No observation survives; the retry admits sequence 0 |
| `begin_after_counter_sync` | Power loss right after the counter sync, before the pin; the shipped `geth removedb --remove.state --remove.chain` (`removedb-open-obligation`); restart | removedb exits non-zero with `open admission obligations forbid this native database write` and every chaindata file is unchanged; the INPUT_PREPARING observation without a pin is abandoned; sequence 1 reaches EXEC_READY |
| `admit_before_sync` | Power loss in the closing ADMITTED sync, after the pin and the seal (AC-CAP-02-01) | Nothing was admitted; the restart abandons sequence 0 and releases its pin; sequence 1 reaches EXEC_READY |
| `removedb_allowed` | Power loss in the counter sync of Begin on a store of its own; the shipped `geth import` of block 300 (`import-bound`); the shipped `geth removedb --remove.state --remove.chain` | The attempt is killed at the crash point; with no pin and no open obligation the manual database guard passes, `geth import` stops with `store is bound to capture producer store`, and removedb exits 0 with no file left directly in chaindata (the key-value store is deleted natively) |
| `admit_after_sync` | Power loss right after the closing ADMITTED sync (AC-CAP-02-02); fourteen shipped `geth` commands that delete or rewrite chaindata (`admitted-*`: `removedb`, `init`, `import`, `import-history`, `import-preimages`, `prune-history`, `db compact`, `db delete` and `db put` of the ADMITTED record key, `db import`, `db import-badblocks`, `bintrie convert`, `snapshot prune-state`, `snapshot generate-trie`); a held restart; a restart with another normalization profile; a recovering restart that then imports 301 | Each command exits non-zero with the live-pins refusal and every chaindata file (path, size and SHA-256) is unchanged after it; the held restart is in RECOVERY_ONLY with sequence 0 ADMITTED and pinned, refuses the competing block `bad` (`admission mode forbids this step`) and GoLive (`an observation is still pending`), and executes nothing; the restart with another profile stops with `admitted obligation was not resolved` and changes no record or pin; recovery completes the same ADMITTED record to EXEC_READY, then 301 is admitted as sequence 1 |
| `exec_ready_before_sync` | Power loss in the EXEC_READY sync; restart | Recovery re-executes and writes EXEC_READY with the same execution digest as `clean-1`; it stores block 300 with its state and keeps the head at 299 |
| `without_set_head` | Import 300 with `InsertBlockWithoutSetHead` (engine `newPayload`), then `SetCanonical(300)` | One EXEC_READY observation; `SetCanonical` makes 300 the head without a second execution |
| `reject_live` | Import `bad`, then 300 | `bad` ends NATIVE_REJECTED without a pin and without EXEC_READY; 300 is admitted as sequence 1 |
| `reject_after_admit` | Power loss right after the ADMITTED sync of `bad`; restart, then import 300 | Recovery ends the saved obligation as NATIVE_REJECTED; 300 is admitted as sequence 1 |
| `freezer` | A pinned synthetic side-branch header at block 101 forking from 100, the finalized marker at 250, reopen | The native chain freezer freezes 101 blocks (0 to 100), and the side header and the canonical fork block stay readable |

A crash attempt must end killed at its named crash point with no result.
Before a crash inside Begin no trace event is written; a crash in Admit comes
after `begin`, `pinned` and `sealed`, and no `admitted` or `exec_ready` event
appears before it.

## Decision bridge scenarios (E01-02.decision_bridge)

These attempts attach the decision bridge (`AttachDecisions`, the shipped
configuration): the admission opens with `OpenWithCleanup`, the probe mirror
answers every query with `mirror_applied` (false unless stated) and logs it,
a native crash point kills the process at a named point of a decided write,
and the native outcome fixture refuses a named block after its intent. The
scenarios above keep the admission alone, so their expectations are those of
the admission block.

Every intent is checked against an intent record built here from the
generated chain: the native store ID, the producer run, the sequence, the
kind, the old and new heads, the members (observation key, block hash,
EXEC_READY digest from the trace or the saved record, child root from the
generated header), the Geth evidence digest
`H(tag("extractor-geth-intent-evidence-v1") || u64(len(kind)) || kind ||
block hash || u64(number) || child root)` and the events flag. The marker in
the trace must equal `decision.marker_value` of that record byte for byte,
and its digest `decision.intent_digest`. A NATIVE_DECIDED trace must carry
`decided_digest(intent, delivery)` with `BATCH` for `IMPORT` and `CANONICAL`
and `CHECKPOINT` for `SIDE`; a rejection must carry `rejected_digest(intent,
H(tag("extractor-geth-native-refusal-v1") || block hash))`. The end state
of each attempt: the decision counter, the admission counter, the markers
left, the live pins, and no source inputs of closed observations.

| Scenario | Attempts | Expected |
|---|---|---|
| `decision_clean` | `decision-clean-1` imports 300 and 301; `decision-clean-2-restart` restarts with a mirror that applied both and imports 300 and 301 again | Two `IMPORT` decisions (`BATCH`), markers kept, no pin, no inputs, head 301; after the restart the imports are skipped natively with no observation, decision or execution, and `Cleanup` leaves only the counter, low-water mark and purge mark at 2 |
| `decision_exec_ready` (AC-CAP-07-01) | Power loss before the EXEC_READY sync; recover and import 301 | Nothing is decided before EXEC_READY; recovery re-executes the same ADMITTED obligation with the execution digest of `decision-clean-1`, defers the write, replays the block natively and decides it as `SIDE` (`CHECKPOINT`); 301 is then one `CANONICAL` decision that names 301 and the retained 300 and closes both |
| `decision_child_root` (AC-CAP-07-02) | Power loss right after the EXEC_READY sync; recover and import 301 | The child root of 300 is not on disk at the restart; the durable EXEC_READY record found there recomputes; native-only replay (no capture, no BLOCKHASH answer from the capsule) decides 300 as `SIDE` with exactly that EXEC_READY digest; then the `CANONICAL` decision as above |
| `decision_intent` (AC-CAP-07-03) | Power loss right after the decision counter sync of `Intend`; recover and import 301 | The child root of 300 is on disk; recovery settles intent 0 by re-import (`reimport`) with the intent bytes of `decision-clean-1`, `IMPORT`, `BATCH`; 301 is decision 1 |
| `decision_mutation` (AC-CAP-07-03) | Native crash after the block batch, before the head batch with the marker; recover, import 300 and 301 | Recovery completes the head from the stored block (`stored_block`) with the same intent; the import of 300 is refused with `block is already in the chain with its state` and opens no observation; 301 is decision 1 |
| `decision_marker` | Power loss before the NATIVE_DECIDED sync, after the marker; recover and import 301 | Recovery resolves intent 0 from its marker (`marker`) |
| `decision_canonical_intent`, `decision_canonical_mutated`, `decision_canonical_intermediate` (AC-CAP-07-03, CANONICAL) | 300 is written without the head (`SIDE`), then 301 crashes inside its `CANONICAL` decision: after the decision counter sync, after the block batch of 301, or after the intermediate head batch of 300 that `reorg` writes; recover | The `SIDE` and `CANONICAL` intents equal the independent records (members 301 and the retained 300); the `CANONICAL` decision is not recorded before the crash and no marker exists then; recovery settles intent 1 by re-import, from the stored block, or from the stored block with the head already on 300, and decides it once as `BATCH`; head 301, no pin, marker 1 kept |
| `decision_rejected` (AC-CAP-07-06) | The outcome fixture refuses 300 after its intent; power loss before the rejection sync; recover | Recovery settles intent 0 from the native bad-block record (`bad_block`) with the rejection digest of `decision_reject_live`; no NATIVE_DECIDED, no marker, the mirror is never asked, 300 is not stored, head 299, counters 1 and 1 |
| `decision_reject_live` | The outcome fixture refuses 300 | The import fails with the fixture error; the intent is rejected at once with the native refusal evidence; the same end state |

The shipped run `decision-legacy-store` starts the patched `geth` with the
capture config on the `clean` store, whose blocks were written by the
admission alone: startup stops with `native data does not settle the
decision`.

## Mutants

`native_mutants.json` lists native mutants of both patch sets, `geth.go`,
`decision.go` and `core/extractor_decision.go`. Each must compile and must
fail this oracle in the measured run.

The removedb block (D100 item 3) retired the mutant `prune_state_skipped` of
the Geth block (PR #147). The manual database guard now refuses
`geth snapshot prune-state` before it reaches `PruneState`, with the same
live-pins refusal, so a build that skips `PruneState` passes this oracle:
the mutant is equivalent on the shipped binary. The patch
`snapshot_prune_state` still applies exactly once, and the E01-02.pins unit
tests (`adapters/geth/state_provider/provider_test.go`) cover the refusal of
`PruneState` itself. Measured run 03 of the Geth block killed the mutant on
the earlier patch set; on the current patch set the call is covered only by
the patch check and those unit tests.
