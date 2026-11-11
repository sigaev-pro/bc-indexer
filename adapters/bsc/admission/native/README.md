# Admission: BSC block (E01-02.admission, part A)

This directory holds the BSC-only part of E01-02.admission: the native patch
set for the accepted BSC source (commit `54afaff4da31ac9e49ba646a4f65104edac5a2f3`)
and the Go package that wires the shared admission core
([README](../README.md)) and the source inputs core
([README](../../source_inputs/README.md)) into the BSC import path. It is
not compared with the Geth and Bor copies (`native/` is excluded from the
shared-file check).

Part A covers the import path, the per-block observations, `Reject`, the
retention and VM safe-point patches, the pin adapter, the recovery driver,
startup identity, the canonical JSON move and AC-CAP-02-01 and AC-CAP-02-02
on the private fixture. The BSC Parlia input rules (reads below the window,
the parent-state check, system-contract calls, concurrent reads and the
window and checkpoint rule) are part B (see PROJECT_STATUS).

## Layout

| Path | Installed at | Content |
|---|---|---|
| `patches/01-core-import-seam.diff` | `core/extractor_hook.go`, `core/blockchain.go` | Import seam, `Reject` point, EXEC_READY point, rewind guard |
| `patches/02-rawdb-freezer.diff` | `core/rawdb/chain_freezer.go` | Freezer limit |
| `patches/03-eth-recover-pruning.diff` | `core/state/pruner/extractor.go`, `eth/backend.go` | `RecoverPruning` through the pin ledger |
| `patches/04-geth-prune-state.diff` | `cmd/geth/snapshot.go`, `cmd/geth/main.go` | `PruneState` through the pin ledger; guard registration |
| `patches/05-vm-safe-point.diff` | `core/vm/extractor_safepoint.go`, `core/vm/interpreter.go` | VM safe point |
| `patches/06-parlia-reset.diff` | `consensus/parlia/extractor.go` | Snapshot cache reset |
| `go/*.go` | `extractor/bsc` | Seam, Parlia handle, static items, adapters, tests |
| `go/retention/retention.go` | `extractor/bsc/retention` | Guard registration |

The driver `test/harness/bsc_admission.py` extracts the accepted source,
applies the patches with exact context and no fuzz, installs
`adapters/bsc/source_inputs/inputs.go`, `adapters/bsc/admission/{admission,canonical}.go`
and `adapters/bsc/state_provider/{provider,family}.go` under `extractor/`
(module path `github.com/ethereum/go-ethereum`), installs this package, and
builds the probe test binary and the patched `geth`.

## Import seam

`core.ExtractorHook` (patch 01) is the native seam. `Seam` implements it.
While a hook is installed, `BlockChain.InsertChain` takes the observation
guard (`Acquire`) before the chain mutex, so the lock order is guard, then
native locks. It then imports one block per `insertChain` call. For each
block it calls `Begin` before any header verification:

1. `Begin` refuses a block outside the supported profile: the parent must be
   the current head and its state must be present (`ErrImportProfile`). A
   block that is already in the chain with its state needs no observation,
   and the native path skips it.
2. An ADMITTED observation of the same candidate is resumed. An ADMITTED
   observation of another candidate refuses the block (`ErrUnresolved`), so
   a new H never passes a saved obligation. An INPUT_PREPARING observation is
   abandoned: it is not a capture.
3. Otherwise: `admission.Begin` (INPUT_PREPARING), the parent pin
   (`state_provider.Pin`: trie commit, sync, ledger record, sync), the static
   capsule (`SealCapsule`), and `admission.Admit` with its closing sync.
4. The observation is attached: the capsule is read back and checked
   (`ReadStatic`), its declared snapshot keys and its dependency log are
   attached to the Parlia handle, and the Parlia in-memory snapshots are
   dropped (patch 06), so the observation's reads do not depend on earlier
   imports.

`Begin` returns only after the closing ADMITTED sync, so header
verification, `Process`, the Parlia system transactions in
`Parlia.applyTransaction` and every capture hook run after it.

The pin of an observation holds the parent root and the headers of the
parent and of the capsule's ancestor window. Its profile field is the
SHA-256 of the state-profile item.

### Exits

- **EXEC_READY.** `ProcessBlock` calls `Validated` after `ValidateState` and
  before the state, the receipts or the block are written. `Validated`
  detaches the Parlia handle and refuses a wiring error, closes the log with
  `Log.Finish` and calls `admission.ExecReady`. An error stops the import
  before any write, and the obligation stays ADMITTED.
- **NATIVE_REJECTED.** `reportBadBlock` is where the native client records
  an authoritative rejection: a failed header verification, body
  validation, `Process` or `ValidateState`. Patch 01 calls `Rejected` there,
  after the "payment lane state unavailable" early return, which leaves a
  block unjudged. `Rejected` calls `admission.Reject` with the evidence
  digest. If the Parlia handle reports a wiring error, the rejection is not
  trusted, because Parlia hides a failed read behind a regenerated snapshot;
  the observation stays ADMITTED.
- **Unresolved.** If `insertChain` returns without either exit (for
  example a stop during import), `End` returns `ErrUnresolved`. The
  obligation keeps its pin and blocks new admissions until a retry or the
  recovery driver resolves it.
- **Unobserved execution.** With a hook installed, `ProcessBlock` refuses
  any block that is not the block of the current observation
  (`ErrExtractorUnobserved`). This covers other callers of `ProcessBlock`
  and ancestor re-execution.

Side-chain import, reorgs and known-branch activation do not go through
this path. They belong to E01-02.decision_bridge and E01-02.lifecycle_seams,
as do the caller map, the live boundary H and the node flags that install
the seam.

### Digests

- `exec_digest` (`ExecDigest`): SHA-256 of the tag `extractor-bsc-exec-result-v1`
  and a zero byte, then the block hash, state root and receipt root, then
  `u64(count)`, then each receipt's consensus encoding (`MarshalBinary`) as
  `str`. It stands for the native result until the capture side (E04)
  supplies the digest of the record corpus. The EXEC_READY record binds it,
  together with the admitted digest and the closed consumed-input manifest
  (D98 item 7).
- Rejection evidence (`RejectionEvidence`): SHA-256 of the tag
  `extractor-bsc-rejection-v1` and a zero byte, then the block hash and
  `str(native error text)`.

## Parlia handle

`ParliaDB` wraps the database handle that `parlia.New` receives. It started
as the E01-02.inputs BSC probe wrapper. Inside an observation, every `Get`
and `Has` goes through `Log.Next`. A read of a key that the capsule does not
declare, `NewIterator`, a native read error, a log error or a malformed saved
response stops the observation. `Detach` reports the stop. Outside an
observation, reads pass to the native database: they serve RPC, metrics and
the fork choice after EXEC_READY, not execution. A wiring error clears when
the next observation attaches.

## Static items

Same item list as the E01-02.inputs BSC block. Two changes:

- `genesis-json` is now `admission.CanonicalJSON` of the genesis file (D99
  item 3). `LoadGenesis` refuses a genesis that does not build the home's
  genesis block.
- `DecodeStatic` now checks every value item in Go: the canonical genesis,
  the fork id (recomputed from the decoded config with the native `forkid`
  package), the native commit and both profile items.

The driver recomputes the fork id independently from a ChainConfig fork
table. It reads that table from the pinned `params/config.go` the way
`forkid.gatherForks` reads the struct: a field whose Go name ends in `Block`
or `Time` is a block fork for `*big.Int` and a time fork for `*uint64`. A
genesis config key that ChainConfig does not know is refused. The
key-suffix recompute of the E01-02.inputs probe is not used by this block.

## Retention and safe-point patches

- `PruneState` (patch 04): `geth snapshot prune-state` calls
  `stateprovider.PruneState`, which refuses while a pin is live. It runs the
  native pruner with 128, the native default of `--triesInMemory`; another
  value is refused.
- `RecoverPruning` (patch 03): at startup the eth backend asks the native
  pruner whether an interrupted prune is pending
  (`ExtractorPruningPending`, the same bloom-filter search that native
  `RecoverPruning` does). Only then does it call `stateprovider.RecoverPruning`,
  which refuses while a pin is live. A refusal or any other error stops the
  node. Native BSC only logs a recovery error and starts anyway.
- `RequireRewindAllowed` (patch 01): `setHeadBeyondRoot`, which backs
  `SetHead`, `SetHeadWithTimestamp`, `debug_setHead` and the startup repair,
  calls `core.ExtractorRewindGuard` first. A rewind by timestamp passes 0, so
  any pinned header refuses it.
- `FreezeLimit` (patch 02): the chain freezer caps each batch at
  `rawdb.ExtractorFreezeLimit` (exclusive) and holds while the limit is at
  or below the frozen count.

The core packages cannot import `state_provider` (it imports
`eth/ethconfig`, which imports `core`). So the rewind and freezer guards are
function variables, set by the `init` function of `extractor/bsc/retention`,
which the patched `cmd/geth` imports. The variables are therefore set before
any store opens.

- VM safe point (patch 05): the interpreter calls the installed safe point
  with its EVM's tracer before the opcode hook and before the opcode changes
  memory or state, only in traced execution. The seam installs it at `Start`
  and acts only for the import tracer (`BlockChain`'s `VmConfig.Tracer`)
  inside an admitted observation. There it forwards to `Config.SafePoint`,
  where the owned-capture seam (`core/capture`, E01-02.owned_capture) waits.
  Safe points of the import tracer outside an observation are counted. Other
  tracers, such as RPC traces, are ignored.

## Startup and recovery

`Start` (before any import) does the following, in order:

1. It checks that the engine has the Parlia patch and that the handle wraps
   the native database.
2. It binds the store identity on first use, then opens `state_provider.New`,
   which refuses a store of another identity. The read-only
   `stateprovider.Preflight` runs before the node opens the store.
3. It opens the saved producer run (`extractor-bsc-admission-run-v1`), or
   saves the configured run on first start, and refuses a configured run
   that differs or a record that names another store.
4. It opens the admission in RECOVERY_ONLY and installs the safe point.

`Recover` then resolves every saved obligation. It abandons INPUT_PREPARING
observations. It re-executes each ADMITTED obligation from its capsule
block through the native import; the seam resumes it, and the log replays
the saved responses, so the obligation reaches EXEC_READY or NATIVE_REJECTED.
Then it calls `GoLive`. While the admission is RECOVERY_ONLY, a live import
of any other block is refused (`ErrMode`).

## Checks

`go/unit_test.go` covers the Parlia handle, the static checks, the run
record, the pin and input adapters (including `Abandon` without a pin), the
digests, the guard registration, and the real chain freezer with a pinned
side branch (control: without the limit, the freezer deletes it).

`go/native_test.go` is the native probe on the accepted private fixture:

- live import;
- NATIVE_REJECTED of a block whose seal fails;
- six crash points of AC-CAP-02-01 and AC-CAP-02-02 under three store
  models, each followed by restart and recovery;
- identity refusals, the rewind guard and the unobserved-execution refusal.

The driver's `run` also starts the patched `geth` on a probe home that holds
live pins, and re-checks every digest and the trace order with its own
rules. `bsc_admission.mutants.json` lists the Go mutants. The Python mutants
and portable controls are in `test/harness/fixtures/bsc_admission`.
