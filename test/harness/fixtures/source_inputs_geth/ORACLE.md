# Oracle for the E01-02.inputs Geth probe

The probe (`probe.go`) runs inside the accepted Geth tree and only reports
observations. The oracle is `compare()` in `test/harness/source_inputs_geth.py`
with the fixed values in `expected.json`. It never trusts a value the probe
computes about itself when an independent value exists:

- The genesis hash is pinned in `expected.json` (genesis.json plus the
  reader contract; the value was first observed in measured run 03 and is
  fixed since then, so a changed genesis fails). Expected block and
  transaction hashes come from the generated chain, which the probe builds with `core.GenerateChain` and a separate in-memory chain,
  not from the import under test. `generated_hashes[n]` is the hash of block
  `n`; index 0 is the genesis.
- The probe reports the saved bytes of every capsule item, read back from the
  store after the seal. The oracle checks them against the manifest and then
  decodes them itself: a Keccak-256 and an RLP decoder in the driver,
  written from the specifications. Two multi-block Keccak vectors come from
  `crypto.Keccak256` of the accepted Geth tree.
- Capsule and consumed-manifest digests are recomputed with the independent
  Python formulas of `test/harness/source_inputs.py`.
- The native commit is the accepted Geth source commit from the preparation.

## Chain

Genesis is `genesis.json` (chain 31337, London rules, ethash faker) plus the
reader contract at `0x...c1`. Its code stores `BLOCKHASH(NUMBER-k)` in slot
`i` for `k` = 1, 3, 1. Blocks 1 to 3 carry plain transfers; block 4 calls the
reader. The observation is block 4, so native code must look up blocks 3, 1
and 3, in that order.

## Static capsule

NATIVE_CAPTURE_PROTOCOL lists the static manifest: the original header, body
and encodings, the parent root and hash, the chain config and fork digest,
the native commit and normalization profile, and the ancestor headers known
in advance. For block `N`, the BLOCKHASH window is blocks `max(0, N-256)` to
`N-1`; those headers are known before execution, so they are capsule items,
not dependency log entries. The capsule of block 4 holds, in order:

| Item | Saved bytes | Oracle check |
|---|---|---|
| `header-rlp` | RLP of the block 4 header | Keccak-256 equals the generated block 4 hash |
| `body-rlp` | RLP of the block 4 body | The list `[transactions, uncles]`; no uncles; the Keccak-256 of each transaction encoding equals the generated transaction hashes |
| `encodings` | JSON naming the format of each encoded item | Equals `expected.json` |
| `parent-header` | RLP of the block 3 header | Number 3, parent hash of block 2, Keccak-256 equals the block 3 hash |
| `parent-hash` | 32 bytes | The generated block 3 hash |
| `parent-root` | 32 bytes | The state root field of the saved parent header |
| `ancestor-header-0` to `-2` | RLP of the headers of blocks 0 to 2 | Number, parent hash and Keccak-256 against the generated chain |
| `chain-config` | JSON of the native chain config | Equals `chain_config` in `expected.json`: the `genesis.json` config plus the zero `depositContractAddress` that Geth writes |
| `fork-id` | JSON `{hash, next}` | `hash` is the big-endian CRC-32 of the genesis hash (every fork of this config is at block 0), `next` is `"0"` |
| `native-commit` | The accepted Geth source commit | Equal |
| `normalization-profile` | `test-only-probe-v1` | Equal; a stand-in until the normalizer defines a product profile |

Every item is nonempty and at most one chunk long. The probe imports the
block decoded from the saved `header-rlp` and `body-rlp` bytes, and the
test-only hook (`hook.go.txt`, one inserted call after `defer evm.Release()`
in `StateProcessor.Process`) answers each BLOCKHASH lookup with the
Keccak-256 of the saved header of that block. Each lookup event names the
item and the answer; the oracle recomputes both. The state prefetcher and
the parallel processor never call the hook; the fixture has no Amsterdam
access list, so the serial path runs.

The Geth import of this fixture has no response that is unknown in advance,
so the dependency log stays empty. Each imported attempt still opens the
log, closes it with count 0 and the consumed-manifest digest of the capsule,
and then must refuse a new response.

## Scenarios

Every attempt runs in a child process on an on-disk Pebble store with the
native freezer. The parent imports blocks 1 to 3 and stops the chain first.
A poisoned header differs from the native one only in its extra data,
`changed external response <n>`, so its hash differs. A poisoned body is the
body of block 3.

| Scenario | Attempts | Pass condition |
|---|---|---|
| `clean_then_closed_replay` | native source; then rewind to block 3 and retry with a source that poisons the body, the parent header and ancestor 1 | Both import block 4 with the expected slots; the retry does not ask the source, reseals the saved capsule and returns the same manifest |
| `crash_after_seal` | SIGKILL right after the seal; retry with the same poisoning source | The retry finds the capsule of the killed process, does not ask the source, imports block 4 |
| `poisoned_parent_header` | the source poisons `parent-header`; then retry with the native source | Both rejected with `invalid merkle root`; the head stays at block 3; the retry hands native code the hash of the saved poisoned header |
| `poisoned_ancestor_header` | the same for `ancestor-header-1` | The same |
| `poisoned_body` | the source poisons `body-rlp`; then retry with the native source | Both rejected with `transaction root hash mismatch` before execution starts |
| `control_direct_poison` | the hook returns poisoned hashes without a capsule | Rejected with `invalid merkle root`; the head stays at block 3 |

Order checks, from the event log of the recording store:

- A new seal asks the source first and once, writes exactly one chunk per
  capsule item, syncs, writes `/capsule` once and syncs, before `sealed`. A
  reseal asks nothing, writes nothing and syncs. `OpenLog` syncs between
  `sealed` and `opened`, and `process_start` comes after both.
- After `process_start` come only the lookups and then the close of the
  empty log: `closed` and a sync on the first close, then `finished`.

The poisoned scenarios are the negative controls for the items that native
code reads: the parent header, an ancestor header and the body. Their
replays are rejected only if native code received the saved bytes. Native
mutant `native_lookup_bypass` (a fresh native lookup) passes the clean
scenarios and dies in the poisoned header scenarios; `hook_never_installed`
leaves no lookup event; `block_not_from_capsule` (import the generated block) dies in
`poisoned_body`; `retry_asks_source` dies because the poisoning source
conflicts with the sealed capsule; `header_never_poisoned` and
`body_never_poisoned` show that the poisoning works. The other items have
negative controls in the oracle tests: each item replaced by wrong but
well-formed bytes, with a consistent manifest, is rejected. Native mutants
`parent_header_wrong_block` and `ancestor_window_short` check the parent
and the window natively.

The driver tests (`test_source_inputs_geth_driver.py`) run preparation and
the measured run with Docker faked at the Native boundary: refusals create no
output, failures and stop signals still write the record, and a surviving
native mutant fails the run.

## Limits

A SIGKILL is a process crash, not a power loss: the operating system keeps
unsynced writes. The power-loss model is covered by the block 0 unit tests.
The window is checked at block 4, where it starts at the genesis; a window
of 256 headers is not run. These are private local runs with the ethash
faker; no AC verdict and no public-profile credit follow from them.
