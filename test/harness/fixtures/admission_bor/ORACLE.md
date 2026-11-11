# Oracle of the E01-02.admission Bor probe (qa)

Profile: private local Bor fixture chain 15001 (POLY, private local). Every
public profile cell (Polygon Mainnet, Amoy) stays NOT_RUN (D96, D98, D99).
The judge is `compare` in `test/harness/bor_admission_probe.py`; its expected
values come from this oracle (`expected.json`), the stub files, the sealed
blocks and the README formulas, never from the probe output.

## Fixture

- Genesis: the accepted test genesis `tests/bor/testdata/genesis.json` of the
  Bor source, with `timestamp` = `genesis_time`, `bor.stateSyncConfirmationDelay`
  = `{"0": state_sync_delay}` and `bor.rioBlock` = `rio_block`, in the shared
  canonical JSON form (`admission.canonical`). The chain-config item is its
  `config` object in the same form. The driver derives both again from the
  retained source archive and requires the prepared files to be equal.
- Chain: `chain_blocks` blocks sealed by the tracked span-0 test key at the
  earliest header times that native verification accepts, against the stub.
  Blocks 1 to 1031 are before Rio (sprint 4 up to block 31, then 2); blocks
  1032 to 1040 are Rio blocks. The chain passes the first checkpoint, 1024.
- Stub: the state sync and span bodies of the E01-02.inputs Bor stub, with
  the latest span set to span 1 (blocks 256 to 6655), so a Rio block resolves
  to span 1. The record times of the state sync events are before the block 4
  `to_time`, so block 4 commits both events.

The judge decodes every sealed block with its own RLP reader, hashes every
header with its own Keccak-256 (tested in `test_bor_source_inputs_probe.py`)
and checks the chain links; the generation record must agree.

## Counted Heimdall calls per observation

Rules read from the pinned Bor source (`consensus/bor/bor.go` `Finalize`,
`checkAndCommitSpan`, `CommitStates`; `span_store.go` and
`span_store_lookup.go`; `getVeBlopSnapshot`). Each window starts with purged
caches, so every read below reaches the router.

| Block | Calls, in order |
|---|---|
| Before Rio, not a sprint start | none |
| Block 4 (first sprint start) | `GET /bor/spans/1` (first span commit), `GET /status`, state sync page `from_id=1`, then `from_id=3` (variant `b`: `from_id=4`, three events) |
| Later sprint starts before Rio | `GET /status`, state sync page `from_id=3` |
| Rio block | `GET /status`, `GET /bor/spans/latest`, `GET /bor/spans/1` (the header check from the capsule) |
| Rio sprint start | the Rio calls, then state sync page `from_id=3` (the status is cached by then) |

The state sync request is `GET /clerk/time?from_id=<id>&to_time=<time>&pagination.limit=50`
with `to_time` = header time minus the delay, in UTC. The response bodies are
the stub files named in `expected.json`.

## Expected records

For every durable observation of block N with sequence S:

- INPUT_PREPARING: the candidate is (N, Keccak-256 of the header, the parent
  hash, the parent state root).
- Capsule: the eleven items of `capsule_items` in that order, each computed
  here (see the native README): the block RLP sliced from `blocks.rlp`, the
  parent header RLP, its hash and root, the ancestor headers by the ancestor
  rule (checkpoint at or below the parent up to the parent before Rio; the
  parent alone from Rio on), the canonical genesis and chain config, the
  fork ID (`zlib.crc32` over the genesis hash and the block forks), the
  commit and the profile name. The checkpoint snapshot is native Bor data:
  it must name the first ancestor (number and hash) and hold exactly the
  fixture signer before Rio, and be empty from Rio on. The capsule digest is
  recomputed with `source_inputs.capsule_digest`.
- ADMITTED: the candidate, the capsule digest, the pin digest equal to the
  SHA-256 of the pin ledger record in the store, and the admitted digest
  recomputed with `admission.admitted_digest`.
- EXEC_READY: the consumed digest recomputed from the capsule digest and the
  counted calls above with `source_inputs` (AC-CAP-04-03, the real binding of
  D98 item 7), the native result digest recomputed from the sealed header,
  the EXEC_READY digest recomputed with `admission.exec_ready_digest`, the
  closed manifest equal to the consumed digest, and the pin still live.
- NATIVE_REJECTED: the evidence digest recomputed from the observation key,
  the block hash and the native error text that the probe reports; no
  EXEC_READY and no live pin.

## Scenarios

Each scenario runs its attempts on one instance directory; a crash point is
a SIGKILL of the attempt right after the sync of one step of the block's
observation.

| Scenario | AC | Expected |
|---|---|---|
| `ac-cap-02-01-begin`, `-pin`, `-seal` | AC-CAP-02-01 | Lost after the INPUT_PREPARING commit, after the pin, or after the seal: the observation is INPUT_PREPARING with no pin / a pin / a pin and a capsule, no log entry and no EXEC_READY. The restart reopens the saved run, refuses `GoLive` (pending) and `Begin` (RECOVERY_ONLY), abandons it (pin released), and imports the block again as a new observation that reaches EXEC_READY |
| `ac-cap-02-02-admitted`, `-log`, `-finish` | AC-CAP-02-02, AC-CAP-04-03 | Lost after the closing ADMITTED sync, after three saved calls, or after the closed manifest: the restart lists the same ADMITTED obligation (unchanged record) and refuses `GoLive` and `Begin`; recovery replays the saved prefix (never the changed variant `b` bytes) and fetches the tail, then reaches EXEC_READY with the independent consumed digest. With variant `b`, the live re-import of the block is a new observation that native validation rejects (NATIVE_REJECTED with its evidence digest); a third attempt with variant `a` completes the import |
| `ac-cap-02-02-exec-ready` | AC-CAP-02-02 | Lost after the EXEC_READY sync: recovery keeps it for the decision bridge, and the block is imported again as a new observation |
| `native-rejection` | AC-CAP-02-02 | Variant `b` without a saved prefix: native validation fails (`invalid merkle root`), the observation is NATIVE_REJECTED with its evidence digest, its pin is released, its log is not closed, and no EXEC_READY exists; the next attempt completes |
| `checkpoint-rio` | AC-CAP-02-02, AC-CAP-04-03 | Blocks 1 to 1020 imported without observations, then 1021 to 1040 observed. Lost after two saved calls of block 1033 (Rio): recovery verifies the header from the capsule and the saved status and latest span, fetches span 1, and reaches EXEC_READY. The native walk check of block 1028 passes with the capsule list and fails without the checkpoint header and the next one. Native `SetHead` below a pinned header is refused. A different store identity, a leftover state bloom filter with live pins (node start), and the patched `bor snapshot prune-state` are refused without a store change; the accepted `bor` binary does not refuse, so the refusal comes from the patch |
| `rpc-window` | AC-CAP-02-02, AC-CAP-04-03 (D100 item 1) | Blocks 1 to 1030 imported without observations, then 1031 to 1040 observed. The first Heimdall read in the window of block 1035 (Rio, `GET /status`) starts a `bor_getSnapshot` call for block 1034 through the native JSON-RPC server, and the stub answers only once the call waits at the window gate. The call must still be waiting when the observation closes and then return the native snapshot of block 1034 (number and hash from the sealed chain). Before the import of block 1037, the probe purges the caches, starts the same call for block 1036 and holds the next Heimdall request for one second; the import of block 1037 starts while that counted read (a Rio path) is in flight, so the window must wait for the call before it routes reads to the log; the call returns the native snapshot of block 1036. Every observed block reaches EXEC_READY bound to the independent consumed digest of the import's own calls, so neither call's reads are in any log. Then every EXEC_READY replays from its saved inputs alone: no error, and its counted calls are exactly the oracle calls of its block, each served from the log (`replay`). Finally the same call for block 1040 with purged caches outside every window reads `/status`, `/bor/spans/latest` and `/bor/spans/1`, each at least once, passed to the native client (the native block producer of the probe node may read the same paths in that interval), so the call is a counted Heimdall reader whenever its caches are cold, as they are when a window opens |
| `prune-block-live-pins` | AC-CAP-02-02 (D100 item 1) | Blocks 1 to 6 observed (six EXEC_READY pins). The patched `bor snapshot prune-block` exits 1 with `guarded block pruning: live pins forbid` before any native step (its output lacks the native `freezer is empty, abort`), and the store records and the ancient directory are unchanged. The accepted `bor` binary does not refuse: it reaches the native pruner and stops at `freezer is empty, abort` (exit 1), so the refusal comes from the patch |
| `prune-block-no-pins` | AC-CAP-02-02 (D100 item 1) | Blocks 1 to 6 imported without observations (no pin). The patched `bor snapshot prune-block` does not refuse: it reaches the native pruner and stops at `freezer is empty, abort` (exit 1) |

The empty-freezer stop comes from `core/state/pruner` `backupOldDb`: the
fixture chain (1,040 blocks) is shorter than the freezer threshold (90,000
blocks), so the ancient store holds no block. The native pruner opens no
backup before that check.

Every attempt that is not lost or refused must exit 0, and every observed
block of a completed range must reach EXEC_READY with its counted calls
fetched live.
