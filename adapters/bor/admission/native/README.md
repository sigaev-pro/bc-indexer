# Bor block of E01-02.admission

This directory is the Bor-only part of `adapters/bor/admission` (the shared
core is described in [../README.md](../README.md)). It wires the admission
core, the source inputs of E01-02.inputs, the pin ledger of E01-02.pins and
the owned capture seam of E01-02.owned_capture into the accepted Bor import
path, and adds the Bor native patch set of D98 items 2 and 4. The plan rows
it implements are in PROJECT_STATUS ("E01-02.admission", plan for the
per-client blocks, every row that names Bor).

## Layout

| Path | Installed in the Bor tree at | Content |
|---|---|---|
| `obligation/` | `extractor/admission/native/obligation` | Stdlib-only decision logic: observation driver, pin adapter, recovery driver, Heimdall router |
| `borimport/` | `extractor/borimport` | Native glue: per-block capsule, pin, header check from the capsule, re-execution, startup identity, VM safe point |
| `tree/` | the same relative path | New native files of the patch set |
| `patches.json` | (applied) | Anchored edits of native files |

`test/harness/bor_admission_tree.py` installs everything into an extracted
Bor source tree: the shared packages (`admission`, `source_inputs`,
`state_provider`, `core/capture`) under `extractor/`, the files above, and the
edits. Every anchor must occur exactly once and no new file may replace a
native one, so a changed native tree is refused, not patched in the wrong
place. The record lists every file's digest before and after. The module path
is `github.com/ethereum/go-ethereum`, as in the accepted tree.

## Import path and per-block observations

One observation per block on the Bor product import path,
`processBlockForImport` to `ProcessBlock` (NATIVE_CAPTURE_PROTOCOL, "Bor"):

1. The edit `import-path-observation` makes `processBlockForImport` call
   `extractorProcess` while an observer is installed. Preconfirmed results
   (`claimPreconf`) are then never used: the block is always executed by
   `ProcessBlock` inside its observation.
2. `Observe` (borimport) runs `obligation.Driver.Observe` under the
   observation guard: `Begin`, the pin of the parent root, the sealed
   capsule and `Admit`. A failure before `Admit` returns abandons the
   observation (no capture exists) and fails the import.
3. After the closing ADMITTED sync, the driver opens the observation's
   dependency log, installs it in the Heimdall router (the window), and drops
   the Heimdall and snapshot caches (`ExtractorPurgeInputs`: the span store
   caches, which also stops its poll loop and range warmup, and the snapshot
   cache). Every Heimdall read of the block now goes through the log.
4. The header seal is verified again from capsule inputs only (see "Header
   check from the capsule"). A failure is a native validation failure.
5. `ProcessBlock` executes and validates the block (`ValidateState`).
6. `Done`: with no error, the log is closed (`Log.Finish`) and `ExecReady`
   binds the closed consumed manifest and the native result digest. A native
   validation failure (header check or `ProcessBlock`) calls `Reject` with
   the digest of the rejection evidence and never `ExecReady`. A counted
   input failure (the log refused a request, or Heimdall failed) is not a
   native validation failure: the obligation stays ADMITTED with its pin, the
   import stops, and recovery resolves it.

The native steps after `ProcessBlock` (the milestone whitelist check
`ValidateReorg` and the block write) are the native decision, which belongs
to E01-02.decision_bridge.

While an observer is installed, `InsertChainWithWitnesses` imports a batch
one block at a time (edit `import-path-one-block-batches`), so no header
verification of a later block, which may read Heimdall, runs inside the
window of an earlier one.

Digests (SHA-256, tag followed by one zero byte, each field as an 8-byte
big-endian length and its bytes):

- Native result (the EXEC_READY `exec_digest` until the E04-01 record corpus
  exists): tag `extractor-bor-native-result-v1`; block hash, state root,
  receipt root (64 lowercase hex digits each) and the gas used in decimal.
  `ValidateState` has checked all of them against the header.
- Rejection evidence: tag `extractor-bor-native-rejection-v1`; observation
  key, block hash, native error text.

## Capsule items

| Item | Bytes |
|---|---|
| `genesis` | The genesis file in the shared canonical JSON form |
| `block` | RLP of the block |
| `parent-header` | RLP of the parent header |
| `parent-hash` | Keccak-256 of `parent-header` |
| `parent-root` | State root of the parent |
| `ancestor-headers` | RLP list of the headers of the ancestor rule |
| `checkpoint-snapshot` | Before Rio, Bor's stored snapshot of the first ancestor (`bor-<hash>`); from Rio on, empty |
| `chain-config` | The effective chain config in the shared canonical JSON form; `Start` refuses a node that does not run with its decoding |
| `fork-id` | EIP-2124 fork ID at the block (`core/forkid` `NewID`) |
| `native-commit` | Bor source commit |
| `normalization-profile` | The normalization profile name (test-only `test-only-probe-v1` until E04-01.block_record, D99 item 4) |

The genesis and chain-config items follow the shared canonical JSON rule of
D99 item 3 (`admission.CanonicalJSON`): `Start` refuses items that are not
already canonical.

### Ancestor rule (D99 items 1 and 5)

- Before Rio, Bor's snapshot walk (`snapshot`) goes back from the parent
  until it finds a snapshot: in memory, on disk at a multiple of 1024
  (`checkpointInterval`), or at genesis. With cold caches it stops at the
  nearest checkpoint at or below the parent, whose snapshot Bor stored when
  it applied that header. The rule is therefore: the checkpoint header up to
  the parent, plus the stored snapshot of that checkpoint.
- From Rio on (`getVeBlopSnapshot`), no ancestor is walked: the producers come
  from the span of the block, read through `spanByBlockNumber` (status,
  latest span and the span ranges, all through the log), and
  `performSpanCheck` reads only the parent header. The rule is the parent
  header alone and an empty checkpoint snapshot.

The probe proves the checkpoint interval natively (`-walk-check`): the native
`VerifySeal` walk with cold caches over only the capsule headers passes, so
it reads no header below the checkpoint; without the checkpoint header and
the next one it fails with an unknown ancestor, so it needs every header
above the checkpoint.

### Header check from the capsule

`ExtractorVerifySeal` (new file in `consensus/bor`) builds the snapshot from
capsule inputs only: before Rio, Bor's own `loadSnapshot` over the
checkpoint-snapshot item and `Snapshot.apply` over the ancestor headers; from
Rio on, `getVeBlopSnapshot` without the span-change wait. It then runs the
native `verifySeal` over a header reader that serves only the capsule headers.
The edit `ancestor-rule-snapshot-override` makes `snapshot` return that
snapshot for exactly that header. The check runs in every window and again in
recovery, so a block reaches EXEC_READY only if the capsule and the logged
Heimdall reads suffice to verify its seal.

## Heimdall inputs (D98 item 6, D99 item 1)

The Heimdall HTTP client uses the process transport; `Start` installs the
`obligation.Router` as that transport. Counted requests are state sync event
pages (`/clerk/time`), spans by ID (`/bor/spans/<id>`), the latest span and the
status. Inside a window each goes through `Log.Next` with the request identity
`GET <path>?<query>`, so the raw response body is durable before Bor receives
it, and a retry replays the saved bytes. The latest span bounds the span IDs
that Bor reads and the status decides whether Bor waits, so both are logged
rather than bounded. Requests outside every window (header verification
ahead of the observation, the poll loop before the first window) and
uncounted ones (milestones) pass through and are counted by path only. The
first counted failure stops the window: later counted requests of the window
fail with the same error.

## Recovery driver and startup identity

`borimport.Preflight` refuses a stopped native store of another identity
(`state_provider.Preflight`) before the node opens it. `Start` binds the store
identity, reopens the saved producer run (`extractor-admission-run-v1/producer/<id>`),
installs the router and opens the admission in RECOVERY_ONLY. `Recover`
abandons each INPUT_PREPARING observation, and for each ADMITTED obligation
opens its log window, purges the caches, verifies the header from the capsule
and re-executes the capsule block with `ProcessBlock` on the pinned parent
state; then `ExecReady` or `Reject`. Only then does it switch to LIVE and
install the observer. Any other failure leaves the admission in
RECOVERY_ONLY. EXEC_READY observations stay for the decision bridge.
Re-execution in recovery writes no block: a later native import of the same
block opens a new observation, and the decision bridge decides once per
candidate. Which caller starts the node with `Start`, and the live boundary
H, belong to E01-02.lifecycle_seams.

## Pin adapter

`obligation.PinAdapter` maps the native ledger to `admission.Pins`: `Verify`
returns the SHA-256 of the raw ledger record after checking that the pin
exists, holds the parent root and is readable (`state_provider.Verify`);
`Release` maps `ErrUnknownPin` to nil. The pin of an observation holds the
parent root and the parent header, with the profile digest of the Bor capture
profile (SHA-256 of `{"gcmode":"archive","state_scheme":"hash","syncmode":"full"}`).

## Retention patch (D98 item 2)

Native packages cannot import `extractor/state_provider` without an import
cycle, so `extractor/retention` holds hooks and `extractor/borwire` installs
them; the shipped `bor` main (`cmd/cli/main.go`) imports `borwire`. A nil hook
keeps the native behavior, which only binaries without the wiring see.

| Native path | Edit | Guard |
|---|---|---|
| `bor snapshot prune-state` (`internal/cli/snapshot.go`) | `retention-prune-state` | `state_provider.PruneState`: refuses while a pin is live |
| Startup prune recovery (`eth/backend.go`) | `retention-recover-pruning` | With a pending state bloom filter, `state_provider.RecoverPruning` (refuses while a pin is live); the error stops the node instead of being logged |
| `SetHead`, `SetHeadWithTimestamp` (`debug_setHead`) | `retention-set-head*` | `RequireRewindAllowed` for the new head |
| Chain freezer threshold (`core/rawdb/chain_freezer.go`) | `retention-freeze-threshold` | `min(threshold, FreezeLimit(threshold+1)-1)`, read through the key-value store and the freezer |

The startup recovery checks the pins only when the native pruner would act
(a `statebloom.<root>.bf.gz` file anywhere in the instance directory, found
the way the native pruner finds it), because EXEC_READY observations keep
their pins and must not stop a restart. `cmd/geth` is not a shipped Bor
binary and is not patched.

## VM safe point (D98 item 4)

The edit `safe-point-hook-field` adds `OnExtractorSafePoint` to
`tracing.Hooks`; `safe-point-interpreter-call` makes the interpreter call it
on the native goroutine between VM steps, before the opcode hook and before
the opcode changes memory or state. Only traced execution calls it: the
prefetchers clear the tracer. The hook returns nothing, so it cannot change
native execution. `borimport.SafePoint` maps it to `core/capture`: it reserves
callback credit at the safe point (waiting for the worker when the budget is
full) and the opcode hook notes the opcode from that credit inside the
callback, where nothing may wait. A capture error stops the capture and is
reported by `Stats`. The raw spool encoder belongs to E04-04.import_pressure.

## Checks

- `obligation`: unit tests on the real admission core with an in-memory store
  and fakes for the native side (`go test` in a `go 1.25` module and in the
  Bor graph), and `obligation.mutants.json` (`test/mutate.py`).
- `borimport` and the rawdb freezer test: native Go tests in the Bor module
  graph during the preparation (`test/harness/bor_admission_probe.py`):
  retention hooks on the real ledger, the safe point with a real EVM run and
  a slow worker, the pin adapter on the native ledger, the run record and the
  config check.
- The probe (`test/harness/fixtures/admission_bor`) runs native Bor with the
  patched import path on a private fixture chain; the qa oracle is
  [ORACLE.md](../../../../test/harness/fixtures/admission_bor/ORACLE.md).

## Limits

- Private local evidence only (D96, D98, D99): test genesis, one validator,
  stub Heimdall. Public profiles stay with E06 and E07.
- A Heimdall read by another component during a window (for example an RPC
  span query) would enter the log; recovery then fails closed with a prefix
  conflict or unconsumed entries instead of capturing different inputs.
- Process loss in the probe is a real SIGKILL right after a sync, not a power
  cut; a process kill keeps unsynced writes, so loss of unsynced writes is
  covered only by the unit tests of the shared core.
