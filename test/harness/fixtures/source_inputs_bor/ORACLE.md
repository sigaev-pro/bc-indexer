# Bor source-inputs oracle (E01-02.inputs, Bor block)

The qa role fixed this oracle from the Bor source rules and the stub files,
not from probe output. It covers AC-CAP-04-01 to AC-CAP-04-03 on a private
local fixture chain. It is not Polygon Mainnet or Amoy evidence: every public
profile cell stays NOT_RUN (D96, D98).

## Source and fixture

Bor source commit `3a018e38467594508ca31ebeaf804837fb92834a`, its
`tests/bor/testdata/genesis.json` (chain 15001, sprint 4, Indore at block 3,
no Rio) and the span-0 test signer `0x71562b71999873db5b286df957af199ec94617f7`
from `tests/bor/helper.go`. Preparation seals blocks 1 to 9 with real Bor
Prepare, FinalizeAndAssemble and Seal against the stub, variant `a`. Block 4
and block 8 are sprint starts, so `Finalize` runs the span check and
`CommitStates`.

The genesis omits `stateSyncConfirmationDelay`, and Bor indexes that map at
every sprint start after Indore (`params/config.go`
`CalculateStateSyncDelay`), so the probe sets it in memory to `{"0": 0}`. The
capsule keeps the unchanged genesis bytes.

## Heimdall stub (D98)

The deterministic in-process stub answers the native `heimdall.HeimdallClient`
through the process HTTP transport; no network is used. Its bodies are the
fixed files in `stub/`, written by hand in the heimdall-v2 JSON format:

| Request path | Body |
|---|---|
| `/bor/spans/0`, `/bor/spans/1` | `span-0.json`, `span-1.json`: the test signer, chain `15001` |
| `/clerk/time` with `from_id=1` | `clerk-<variant>-from-1.json` |
| `/clerk/time`, any other `from_id` | `clerk-empty.json` |
| `/bor/spans/latest`, `/status` | `span-latest.json`, `status.json` (not counted) |
| anything else | 404 (not counted) |

Variant `a` returns state sync events 1 and 2. Variant `b` is the changed
external response: events 1 to 3 with other data. If Bor received variant `b`,
the state receiver's last state ID would be 3, not 2, so the block 4 state
root would differ from the sealed chain, and the next request would ask for
`from_id=4`, not `from_id=3`.

Counted calls are the calls through which Bor receives execution inputs:
state sync event pages (`/clerk/time`) and spans by ID (`/bor/spans/<id>`).
The latest-span and status calls (from the span store poll loop,
`getLatestSpan` and `waitUntilHeimdallIsSynced`) are answered by the stub
without the log. Their bytes do not enter execution directly, but the latest
span chooses which span IDs Bor reads. A changed latest span would therefore
change the counted span requests, and the log would refuse that change as a
prefix conflict instead of silently replaying it. The saved bytes
are the raw response body at the transport boundary, before Bor decodes it;
the request identity is `GET <path>?<query>` exactly as Bor builds it.

## Expected counted request sequence

From `consensus/bor` at the pinned commit, a fresh import of blocks 1 to 9
makes these counted calls, in this order:

| Sequence | Request | Body | Source rule |
|---|---|---|---|
| 0 | `GET /bor/spans/0` | `span-0.json` | Header verification of block 1 builds the genesis snapshot: `snapshot` at number 0 calls `spanStore.spanByBlockNumber(1)` (span 0) |
| 1 | `GET /bor/spans/1` | `span-1.json` | Block 4 `Finalize`, `checkAndCommitSpan`: the genesis contract span has end block 0, so `needToCommitSpan` is true and `FetchAndCommitSpan` reads span 1 |
| 2 | `GET /clerk/time?from_id=1&to_time=T4&pagination.limit=50` | `clerk-a-from-1.json` | Block 4 `CommitStates`: last state ID 0, so `from_id` 1; `to` is the block 4 time minus the delay |
| 3 | `GET /clerk/time?from_id=3&to_time=T4&pagination.limit=50` | `clerk-empty.json` | The page added records, so `CommitStates` asks for the next page |
| 4 | `GET /clerk/time?from_id=3&to_time=T8&pagination.limit=50` | `clerk-empty.json` | Block 8 `CommitStates`: last state ID 2 |

`T4` and `T8` are the block 4 and block 8 header times from the sealed chain,
formatted as RFC 3339 UTC seconds (`stateSyncURL`). Span 1 is cached after
sequence 1, so no later span call is counted. With a delay `d`, `to_time` is
the header time minus `d`; nothing else changes.

## Expected digests

The driver computes the capsule digest, every entry digest, the chain and the
consumed-manifest digest with the README formulas in
`test/harness/source_inputs.py`, from the capsule items (`genesis`: the genesis
file bytes; `blocks`: the sealed `blocks.rlp`; `bor-source-commit`: the commit
string) and the expected requests and stub bodies above. It never reads these
values from the probe.

The header times (for `to_time`) and the state roots are decoded by the driver
from `blocks.rlp` with its own RLP reader. Only the block hashes come from the
generation record `generated.json` (Python's `hashlib` has no Keccak), and the
driver refuses the run if that record's numbers, times or state roots differ
from the decoded headers.

## Scenarios

Each scenario uses a new store and runs its attempts as separate processes. A
crash point is a real `SIGKILL` of the attempt process right after `Next`
returned the durable response and before the transport hands it to Bor.

| Scenario | Attempts | Expected |
|---|---|---|
| `ac-cap-04-01` | Variant `a`, lost after sequence 2; then variant `b` | Lost attempt: sequences 0 to 2 fetched and durable. Retry: opens at frontier 3, replays sequences 0 to 2 with the variant `a` bytes and never fetches them, fetches 3 and 4, presents the expected request sequence, never returns the variant `b` bytes, imports the sealed head, and closes with the independent consumed digest |
| `ac-cap-04-02` | Variant `a`, lost after sequence 2; then delay 1; then delay 0 | Second attempt: replays 0 and 1, then Bor sends the delayed request at offset 2, which is `ErrPrefixConflict`; import fails, nothing is closed and no store key changes. Third attempt: opens at frontier 3 and completes from the saved prefix |
| `ac-cap-04-03` | Variant `a`, lost after sequence 1; then variant `a` | Retry: replays 0 and 1, appends 2 to 4 beyond the frontier, keeps every saved prefix key byte-identical, imports the sealed head, and the test-only stand-in marker in the store binds the consumed digest, which equals the independent value |
| `control-changed-response` | Variant `b` without the log | Native import fails at block 4 (3 blocks inserted): the changed response matters |
| `control-delay` | Delay 1 without the log | Bor builds the delayed request sequence and imports the sealed head: the AC-CAP-04-02 conflict comes from the log, not from Bor |

The stand-in marker is test-only (D98 item 7); the real EXEC_READY binding is
proven in E01-02.admission.
