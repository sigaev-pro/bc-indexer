# Bor block of E01-02.admission

This directory is the Bor-only part of `adapters/bor/admission` (the shared
core is described in [../README.md](../README.md)). It wires the admission
core, the source inputs of E01-02.inputs, the pin ledger of E01-02.pins and
the owned capture seam of E01-02.owned_capture into the accepted Bor import
path, and adds the Bor native patch set of D98 items 2 and 4 and D100 item 1
(the prune-block guard and the window gate for other Heimdall readers).
The plan rows
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
   dependency log, installs it in the Heimdall router, and opens the window
   (`ExtractorOpenWindow`): it waits for every gated call in progress to
   return, holds back every other caller of the gated entry points (header
   verification, the bor RPC API, block production and every other path to a
   counted Heimdall read; see "Window gate"), and drops the Heimdall and
   snapshot caches (the span store caches, which also stops its poll loop and
   range warmup, and the snapshot cache). Every counted Heimdall read of the
   window now belongs to this block's import and goes through the log.
4. The header seal is verified again from capsule inputs only (see "Header
   check from the capsule"). A `verifySeal` failure is a native validation
   failure; a capsule that cannot be read or decoded, or a snapshot that
   cannot be built from it, is not.
5. `ProcessBlock` executes and validates the block (`ValidateState`).
6. `Done`: with no error, the log is closed (`Log.Finish`) and `ExecReady`
   binds the closed consumed manifest and the native result digest. A native
   validation failure (header check or `ProcessBlock`) calls `Reject` with
   the digest of the rejection evidence and never `ExecReady`.

Failures that say nothing about the block keep the obligation ADMITTED with
its pin: a refusal by the dependency log (prefix conflict, store failure,
which also stops the window), a Heimdall failure before a native failure, a
capsule read or decode failure, and local state that `ProcessBlock` could
not read (`ExtractorLocalStateUnavailable`, the native `isLocalStateUnavailable`:
`Fail` on the live path, the same rule in recovery). A Heimdall failure is
passed to Bor as an explicit source error and the log stays usable, so Bor's
own retry goes through the log; if the retry succeeds, the block completes
normally. A Heimdall response with a status other than 200 reaches Bor
unchanged and is not saved, so Bor's own status handling applies (a 503 ends
its retry loop at once, as in native Bor). The next `Observe`, or
`Recover` at startup, resolves an ADMITTED obligation from its pin and
capsule before any new `Begin`, so no restart is needed.

The native steps after `ProcessBlock` (the milestone whitelist check
`ValidateReorg` and the block write) are the native decision, which belongs
to E01-02.decision_bridge.

While an observer is installed, `insertChainWithWitnesses`, which every
batch import passes (the side-chain re-import too), imports one block at a
time (edit `import-path-one-block-batches`), so the header verification of a
later block of the batch runs after the window of the earlier one closed.
Header verification of other components (the block fetcher, a later batch)
waits at the window gate.

## Window gate (D98, D100 item 1)

Only the import's own Heimdall reads may enter an observation's log; other
readers use the native client. `consensus/bor/zz_extractor_gate.go` is the
gate. The goroutine that opens a window owns it; while it is open, every
other goroutine that enters a gated engine entry point waits until the
window closes, and the window opens only after every gated call in progress
has returned. A held-back reader therefore reads Heimdall after the window,
through the router outside every window, which passes it to the native
client unlogged; a reader that started before the window finished before it,
and the window's cache purge comes after it, so nothing it cached reaches
the import.

The gated entry points (edits `window-gate-*`, each first statement
`defer c.extractorGate()()`) are every exported `Bor` method that can reach
a counted read (span by ID, latest span, status, state sync page):
`VerifyHeader`, `VerifyHeaders` (per header), `VerifySeal`, `Prepare`,
`Finalize`, `FinalizeAndAssemble`, `FinalizeAndAssembleForSimulation`,
`FinalizeForPipeline`, `IsAuthorizedSigner`, `SealWithStopHook` (and so
`Seal`), `CalcDifficulty`, `CommitStates` and `FetchAndCommitSpan`; and every
method of the `bor` RPC API (`GetSnapshot`, `GetSnapshotAtHash`,
`GetSigners`, `GetCurrentProposer`, `GetCurrentValidators`, ...). The native
test `TestExtractorGateCoversEveryHeimdallEntryPoint` parses `bor.go` and
`api.go`: every exported `Bor` method must be classified as gated or not, and
every gated method and every API method must pass the gate first, so a new
native entry point fails the preparation until it is classified. The
ungated ones read no counted endpoint: `Author`, `Seal` (delegates),
`AssembleBlock`, `GetCurrentValidators` of `Bor` (the validator contract),
`EarliestAnnounceTime` and the accessors. Bor reads Heimdall elsewhere only
through uncounted endpoints (checkpoints and milestones in `eth`), which the
router never logs.

The import passes. `ExtractorOpenWindow` takes the hash of the observed
block (`OpenWindow` gets the candidate); the goroutine that opened the
window passes its own gated calls, and `Finalize` of the observed block
passes on that goroutine or on a goroutine it started (edit
`window-gate-finalize` calls `extractorBlockGate(header)`): native
`ProcessBlock` runs its serial (and parallel) processor on goroutines it
starts. Calls nested in it (`CommitStates`, `FetchAndCommitSpan`) pass too.
`Finalize` of the observed block on any other goroutine is not the import
and waits: state regeneration for tracing (`eth/state_accessor.go`, through
`Processor().Process`) can finalize a stored block, including the observed
block of a replay or of a re-imported side-chain block. A goroutine already
inside a gated call passes nested ones (`GetCurrentProposer` calls
`GetSnapshot`), so the gate never waits for itself. The goroutine identity
is the first line of `runtime.Stack`, and its creator the `created by ... in
goroutine <id>` line at its end. `ExtractorGateWaiting` reports the callers
waiting at the gate.

Order of a window start (`ExtractorOpenWindow`, then `Router.Enter`): the
gate waits for every gated call in progress, the purge stops the span
store poll loop and range warmup, and then the window waits for the
detached span range fetches still in flight (`fetchSpanRange` runs on the
span store lifetime, not on its caller's context, and retries until it
succeeds). Edit `window-gate-detached-fetch` counts each lookup in
`spanRangeById` before its singleflight `DoChan`, so a fetch is counted
before it can start, and releases it when the result is delivered: to the
caller, or, when the caller left on its context (as `waitForNewSpan` does
after its timeout), to a goroutine that waits for the result. A window can
therefore never open while a started fetch is uncounted. Only then does the
driver route counted reads to the log, so no read that another reader
started before the window can land in it.

Lock order: the observation guard is taken inside `processBlockForImport`,
with the native chain lock (`chainmu`) already held. NATIVE_CAPTURE_PROTOCOL
puts the guard first; here no caller takes the guard and then `chainmu`.
E01-02.lifecycle_seams and E01-02.decision_bridge must keep it that way (or
take `chainmu` before the guard) when they add callers.

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
status. Inside a window, where only the import reads (see "Window gate"), each goes
through `Log.Next` with the request identity
`GET <path>?<query>`, so the raw response body is durable before Bor receives
it, and a retry replays the saved bytes. The latest span bounds the span IDs
that Bor reads and the status decides whether Bor waits, so both are logged
rather than bounded. Requests outside every window (header verification
ahead of the observation, the poll loop before the first window) and
uncounted ones (milestones) pass through and are counted by path only. The
first counted failure stops the window: later counted requests of the window
fail with the same error.

## Recovery driver and startup identity

`Start` first requires the single native HTTP Heimdall client at the
configured host with the process transport (`ExtractorCheckHeimdall`): a
gRPC client, a failover client, a client with its own transport or a
WebSocket client would let Heimdall reads bypass the router. The effective
`bor server` profile check (`state_provider.CheckProfile`: hash scheme,
archive, no parallel, pipelined, stateless or preconf paths) runs where the
server configuration exists, in the caller that E01-02.lifecycle_seams adds;
the probe starts native Bor with that profile.

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
candidate.

`Replay` (`obligation.Driver.Replay`, `borimport.Node.Replay`) checks a saved
EXEC_READY observation without changing it: with its closed log in the
router, it opens the window, verifies the header from the capsule and
re-executes the block on the pinned parent state. Every counted read must be
served from the log (a read past the closed log stops the window), every
saved entry must be consumed, and the native result digest must equal the
bound one. A foreign read that had entered the log would fail it as a prefix
conflict or an unconsumed entry. Which caller starts the node with `Start`, and the live boundary
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
| `bor snapshot prune-block` (`internal/cli/snapshot.go`, D100 item 1) | `retention-prune-block` | `extractorRequirePruneBlock` (`internal/cli/zz_extractor_prune_block.go`): `state_provider.RequireNoLivePins` on the store opened read-only, before every path of the command (a new prune, the resumption of an interrupted one, the replacement of the ancient directory); an unreadable ledger refuses too |
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
native execution. It runs after the gas charge and memory expansion of the
opcode, before the opcode hook and the opcode itself. `borimport.SafePoint`
maps it to `core/capture`: it reserves
callback credit at the safe point (waiting for the worker when the budget is
full) and the opcode hook notes the opcode from that credit inside the
callback, where nothing may wait. A capture error stops the capture and is
reported by `Stats`. Installing a capture producer (tracer hooks with the
safe point) on the chain, together with the raw spool encoder, belongs to
E04-04.import_pressure (D98 item 4); this block adds the patch and the
adapter, and the native tests drive them with a real EVM run.

## Checks

- `obligation`: unit tests on the real admission core with an in-memory store
  and fakes for the native side (`go test` in a `go 1.25` module and in the
  Bor graph), and `obligation.mutants.json` (`test/mutate.py`).
- `borimport`, the rawdb freezer test and the window gate tests
  (`consensus/bor`, `TestExtractorGate*`): native Go tests in the Bor module
  graph during the preparation (`test/harness/bor_admission_probe.py`):
  retention hooks on the real ledger, the safe point with a real EVM run and
  a slow worker, the pin adapter on the native ledger, the run record, the
  config check, the gate (owner, waiting callers, nested calls, a window that
  waits for calls in progress) and its coverage of every entry point.
- The probe (`test/harness/fixtures/admission_bor`) runs native Bor with the
  patched import path on a private fixture chain; the qa oracle is
  [ORACLE.md](../../../../test/harness/fixtures/admission_bor/ORACLE.md).

## Limits

- Private local evidence only (D96, D98, D99): test genesis, one validator,
  stub Heimdall. Public profiles stay with E06 and E07.
- The window gate identifies the import by the goroutine that opened the
  window and by `Finalize` of the observed block. An import path that read
  Heimdall from another gated entry point on another goroutine would wait
  for its own window: a stopped import, never a foreign read in the log. The
  capture profile excludes the pipelined and stateless paths.
- Held-back callers (an RPC call, the miner) wait for up to one window, the
  execution of one block. A window waits for the detached span range fetches
  in flight; while Heimdall fails, such a fetch keeps retrying, so the window
  and the import wait until Heimdall answers (a stalled import, never a
  foreign read in the log).
- The creator rule admits `Finalize` of the observed block only on the
  window owner or a goroutine it started directly. A native version that
  finalized on a grandchild goroutine would stall the import, never log a
  foreign read.
- The gate state (`zz_extractor_gate_core.go`, standard library only) has a
  mutant table (`gate.mutants.json`) that compiles each mutant in a small
  module. The Bor bindings of the gate (`zz_extractor_gate.go`, the patched
  span store and entry points) have no mutant table: each mutant would need
  a full Bor build and probe run; the native tests (including an AST check
  that every entry point and the fetch count are in place) and the probe
  scenarios cover them.
- The race of a detached span range fetch is closed by counting before
  `DoChan`. The deterministic proof is the native AST check
  (`TestExtractorSpanRangeFetchCountPrecedesDoChan`);
  `TestExtractorSpanRangeFetchIsCountedFromTheStart` and the probe step
  `expired_fetch` show an expired-context lookup counted, drained and kept
  out of the log, but they cannot reproduce the old scheduling window, and
  the probe's held request passes the router before the window opens, so it
  would stay out of the log without the drain too. The probe holds the next
  counted request after it arms, so a held request could also be another
  reader's read of the same path, and `held_at_observe` is sampled before the
  import starts, not when the window opens.
- If an interrupted block prune already removed the old ancient directory,
  the guard's read-only open fails, so the native `AncientDbReplacer`
  resumption is refused too (fail closed); the store then needs the native
  repair without the guard.
- In the accepted Bor source, `node.OpenDatabaseWithFreezer` drops its
  ancient argument, so native `bor snapshot prune-block` cannot open a store
  whose freezer holds blocks (it stops with "ancient chain segments already
  extracted"); on such a store the native pruner deletes nothing. The guard
  opens the store with `OpenDatabaseWithOptions`, which resolves the ancient
  directory, so it refuses for the live pins before that native step, and it
  stays in front of the pruner if a later source fixes the open.
- `bor snapshot prune-block` refuses while any pin is live, EXEC_READY pins
  that the decision bridge still holds included; pruning ancient blocks
  needs every obligation resolved first. The probe chain (1,040 blocks) is
  shorter than the freezer threshold (90,000), so `prune-block-frozen` marks
  block 30 finalized and the native freezer freezes blocks 0 to 30 with their
  pinned headers; the refusal is measured over that non-empty freezer as
  well as over an empty one. No run exercises a native deletion: the
  accepted pruner cannot open a store with frozen blocks (see above) and
  stops at an empty freezer.
- In the probe, `SetHead` and the startup prune recovery are refused inside
  the probe process, which imports `borwire` like the shipped `bor`; the
  shipped binary itself runs `snapshot prune-state`, and the preparation
  records that `./cmd/cli` depends on `borwire`.
- Process loss in the probe is a real SIGKILL right after a sync, not a power
  cut; a process kill keeps unsynced writes, so loss of unsynced writes is
  covered only by the unit tests of the shared core.
