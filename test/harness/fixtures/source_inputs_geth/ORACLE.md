# Oracle for the E01-02.inputs Geth probe

The probe (`probe.go`) runs inside the accepted Geth tree and only reports
observations. The oracle is `compare()` in `test/harness/source_inputs_geth.py`
with the fixed values in `expected.json`. It never trusts a value the probe
computes about itself when an independent value exists:

- Expected BLOCKHASH answers come from the generated chain, which the probe
  builds with `core.GenerateChain` and a separate in-memory chain, not from the
  import under test.
- Capsule, entry, chain and consumed-manifest digests are recomputed with the
  independent Python formulas of `test/harness/source_inputs.py`.
- The native commit is the accepted Geth source commit from the preparation.

## Chain

Genesis is `genesis.json` (chain 31337, London rules, ethash faker) plus the
reader contract at `0x...c1`. Its code stores `BLOCKHASH(NUMBER-k)` in slot
`i` for `k` = 1, 3, 1. Blocks 1 to 3 carry plain transfers; block 4 calls the
reader. The observation is block 4, so the dependency log must hold exactly
three requests, for blocks 3, 1 and 3, in that order:
`geth-blockhash-v1 ref=<block 4 hash> number=<n>`. Each response is the
32-byte hash of block `n`.

The routed dependency is the BLOCKHASH ancestor lookup of the serial state
processor (`core.GetHashFn`). It is a test-only stand-in: Geth calls no
external service during import, and BLOCKHASH is a lookup inside native
execution whose requests depend on execution, so it exercises the log there.
This probe does not decide the product classification of Geth ancestor
hashes (static capsule or dependency log); E01-02.admission owns that. A
test-only hook (`hook.go.txt`, one inserted call after `defer evm.Release()`
in `StateProcessor.Process`) routes it through the log. The state prefetcher and the parallel processor never call the hook; the
fixture has no Amsterdam access list, so the serial path runs.

## Scenarios

Every attempt runs in a child process on an on-disk Pebble store with the
native freezer. The parent imports blocks 1 to 3 and stops the chain first.

| Scenario | Attempts | Pass condition |
|---|---|---|
| `clean_then_closed_replay` | normal source; then rewind to block 3 and replay with every source answer poisoned | Both import block 4 with the expected slots; the first fetches 0, 1, 2 and closes the manifest; the second fetches nothing and returns the same manifest |
| `crash_after_seal` | SIGKILL right after the seal; retry | The retry finds the capsule from the killed process, opens frontier 0, imports block 4 |
| `crash_before_handoff` | SIGKILL after the response for sequence 1 is persisted and synced, before the hand-off; retry with answers below frontier 2 poisoned | The retry opens frontier 2, replays 0 and 1 without a fetch, fetches 2, imports block 4 |
| `poisoned_log_replay` | every source answer poisoned on a new log; then replay with the native source | Both attempts are rejected by native validation with `invalid merkle root` and the head stays at block 3; the first fetches 0, 1, 2 (poisoned) and persists them; the second fetches nothing and hands native code the saved poisoned answers, not the native ones |
| `control_direct_poison` | hook returns poisoned answers without the log | Native validation rejects block 4 with `invalid merkle root` and the head stays at block 3 |

Order checks, from the event log of the recording store:

- A new seal writes exactly one chunk per capsule item (every item is
  nonempty and at most one chunk long), syncs, writes `/capsule` once and
  syncs, before `sealed`. A reseal writes nothing and syncs. `OpenLog` syncs
  between `sealed` and `opened`, and `process_start` comes after both.
- For a fetched sequence, the events between its request and its hand-off are
  exactly: fetch, response chunk, entry, sync, frontier, sync. A replayed
  sequence has no event between them. No event lies between a hand-off and
  the next request.
- The killed attempt ends with the same persist sequence for sequence 1
  followed by the crash, with no hand-off of sequence 1.

In `poisoned_log_replay` the saved bytes differ from native truth, so its
replay is rejected only if native code received the saved bytes; native
mutant `native_lookup_bypass` (hand native code a fresh native lookup) is
killed there. In the other replays the source answers for durable offsets
are poisoned; native mutant `bypass_saved_bytes` (hand native code the source
answer) is killed there. The poisoned answer for block `n` is SHA-256 of
`changed external response <n>`.

## Limits

A SIGKILL is a process crash, not a power loss: the operating system keeps
unsynced writes. The power-loss model is covered by the block 0 unit tests.
These are private local runs with the ethash faker; no AC verdict and no
public-profile credit follow from them.
