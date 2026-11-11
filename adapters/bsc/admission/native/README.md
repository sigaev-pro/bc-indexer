# Admission: BSC block (E01-02.admission)

This directory holds the BSC-only part of E01-02.admission: the native patch
set for the accepted BSC source (commit `54afaff4da31ac9e49ba646a4f65104edac5a2f3`)
and the Go package that wires the shared admission core
([README](../README.md)) and the source inputs core
([README](../../source_inputs/README.md)) into the BSC import path. It is
not compared with the Geth and Bor copies (`native/` is excluded from the
shared-file check).

Part A (PR #148) covers the import path, the per-block observations,
`Reject`, the retention and VM safe-point patches, the pin adapter, the
recovery driver, startup identity, the canonical JSON move and AC-CAP-02-01
and AC-CAP-02-02 on the private fixture. Part B covers the BSC Parlia
inputs (D99 items 2 and 5):
- the observation reader, which serves only the capsule headers;
- Parlia snapshots as logged dependency responses;
- the system-contract calls;
- the parent-state exit;
- the window and checkpoint rule, proven on a long private chain.

## Layout

| Path | Installed at | Content |
|---|---|---|
| `patches/01-core-import-seam.diff` | `core/extractor_hook.go`, `core/blockchain.go`, `core/state_processor.go` | Import seam, observation reader, `Reject` point, parent-state mark, EXEC_READY point, rewind guard |
| `patches/02-rawdb-freezer.diff` | `core/rawdb/extractor.go`, `core/rawdb/chain_freezer.go` | Freezer limit |
| `patches/03-eth-recover-pruning.diff` | `core/state/pruner/extractor.go`, `eth/backend.go` | `RecoverPruning` through the pin ledger |
| `patches/04-geth-prune-state.diff` | `cmd/geth/snapshot.go`, `cmd/geth/main.go` | `PruneState` through the pin ledger; guard registration |
| `patches/05-vm-safe-point.diff` | `core/vm/extractor_safepoint.go`, `core/vm/interpreter.go` | VM safe point |
| `patches/06-parlia-snapshots.diff` | `consensus/parlia/extractor.go`, `parlia.go`, `lubanFork.go`, `bohrFork.go`, `feynmanfork.go` | Logged snapshot lookup; failed system-contract calls marked |
| `go/*.go` | `extractor/bsc` | Seam, observation reader, system-call backend, static items, adapters, tests |
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
block it first runs the native data-availability check (`IsDataAvailable`)
and then calls `Begin`, before any header verification:

0. A block that fails the data-availability check is refused with no
   observation. `insertChain` refuses such a block without `reportBadBlock`,
   so after `Begin` the failure would leave an obligation that no exit
   resolves. The check also settles the block's blob sidecars (an old block's
   sidecars are dropped), and `Begin` saves the checked sidecars in the
   capsule.
1. `Begin` refuses a block outside the supported profile: the parent must be
   the current head and its state must be present (`ErrImportProfile`). A
   block that is already in the chain with its state needs no observation,
   and the native path skips it.
2. An ADMITTED observation of the same candidate is resumed. An ADMITTED
   observation of another candidate refuses the block (`ErrUnresolved`), so
   a new H never passes a saved obligation. An INPUT_PREPARING observation
   is left only by a restart (`Begin` abandons its own on error);
   `admission.Begin` refuses a new block while it exists, and `Recover`
   abandons it, because it is not a capture.
3. Otherwise: `admission.Begin` (INPUT_PREPARING), the parent pin
   (`state_provider.Pin`: trie commit, sync, ledger record, sync), the static
   capsule (`SealCapsule`), and `admission.Admit` with its closing sync.
4. The observation is attached: the capsule is read back and checked
   (`ReadStatic`), and the observation's reader is built over its headers
   and its dependency log (see "Observation reader").

`Begin` returns only after the closing ADMITTED sync, so header
verification, `Process`, the Parlia system transactions in
`Parlia.applyTransaction` and every capture hook run after it.

The pin of an observation holds the parent root and the headers of the
parent and of the capsule's ancestor window. Its profile field is the
SHA-256 of the state-profile item.

### Exits

- **EXEC_READY.** `ProcessBlock` calls `Validated` after `ValidateState` and
  before the state, the receipts or the block are written. `Validated`
  detaches the reader and refuses a reader error or a parent state read
  error (`statedb.Error()`), closes the log with `Log.Finish` and calls
  `admission.ExecReady`. An error stops the import
  before any write, and the obligation stays ADMITTED.
- **NATIVE_REJECTED.** `reportBadBlock` is where the native client records
  an authoritative rejection: a failed header verification, body
  validation, `Process` or `ValidateState`. Patch 01 calls `Rejected` there,
  after the "payment lane state unavailable" early return, which leaves a
  block unjudged. `Rejected` calls `admission.Reject` with the evidence
  digest. A rejection that may come from an input failure is not trusted,
  and the observation stays ADMITTED. That covers three cases: the reader
  refused a read; a parent state read failed (patch 01 marks the cause with
  `ErrExtractorParentState`); or a system-contract call failed (patch 06
  marks it with `ErrExtractorSystemCall`).
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

## Observation reader

While an observation is attached, header verification (`VerifyHeaders`),
`Process`, `Finalize` and BLOCKHASH read headers through the observation's
`Reader`. Patch 01 passes it as the engine's `chain` argument, and
through `Process`'s context to `StateProcessor`. The reader serves:
- the parent and the ancestor window of the capsule;
- the parent as the current header (`Begin` requires the parent to be the
  head);
- the genesis header, which is a static input.

Any other header read, total difficulty, or a verified-block query stops
the observation (`ErrUndeclaredHeader`, `ErrUndeclaredRead`). The
observation stays ADMITTED: the import fails closed and is never recorded
as NATIVE_REJECTED. Readers on other goroutines (RPC, the vote pool) keep
the native chain and are never logged into the observation.

`GetFinalizedHeader` and the fork choice run after EXEC_READY. They do not
feed the execution result. The finality decision belongs to
E01-02.decision_bridge.

## Parlia snapshots

When Parlia looks up a snapshot through the reader (patch 06), the lookup
is a logged dependency response:
- The request is the tag `bsc-parlia-snapshot-v1`, a zero byte, the block
  number (8 bytes, big-endian) and the block hash.
- The response is the snapshot's JSON, the encoding of a stored snapshot.
- The first attempt runs the native lookup on the native chain (memory
  cache, stored checkpoint snapshot, header walk), and the response is
  durable before Parlia receives it. A retry receives the saved bytes.

The lookup's own reads are native, so they are not observation reads.
These include `FindAncientHeader` at a validator-set switch, the quorum
walk of a vote attestation, and the finality-reward walk. The response does
not depend on what the engine holds in memory, so there is no engine reset.

Only the snapshot of a capsule header may be requested. Parlia looks up the
same snapshot several times per block. The first lookup is logged, and the
later ones receive the same bytes without a new entry. The order is the
native order: one block per `insertChain`, verification finishes before
execution, and the reader serializes the requests.

## System-contract calls

On the import path, Parlia calls the validator, stake and turn-length
contracts in `Finalize`, through its `ethAPI`. The calls happen on epoch
blocks, Bohr turn-length checks and Feynman breathe blocks, each on the
parent state, addressed by the parent hash. `Start` refuses an engine
without `ethAPI`, because the first epoch block would otherwise fail.

`SystemCallBackend` is the backend to give that `ethAPI`:
- It serves a state only by block hash.
- While an observation is attached, it refuses any block other than the
  observed parent. The parent pin keeps that state.
- It runs the call without the import tracer: a system call is not block
  execution, so it is neither captured nor a VM safe point.

The node wiring (E01-02.lifecycle_seams) must pass this backend, not the
node's RPC backend.

## Parent state

`CheckParentState` of the E01-02.inputs probe walked the whole parent
state. It is replaced by a per-block check at the point of read:
- `Begin` requires the parent root, and the pin keeps the state.
- A state read that fails during execution sets `statedb.Error()`. Native
  execution then continues on a missing value.
- Patch 01 marks the native failure (`ErrExtractorParentState`), and
  `Validated` refuses a result read with an error. Either way the
  observation stays ADMITTED, and a retry with the state present resolves
  it.

## Window and checkpoint rule

The capsule holds the parent and the 256 BLOCKHASH ancestors (from the
genesis when the block is lower). It also declares the snapshot rule
(`parlia-snapshots`).

Parlia reads headers directly only for these:
- the parent;
- vote-attestation targets up to three blocks down;
- finality-reward targets about 200 blocks down.

All of these are inside the window. Checkpoints do not widen it: a stored
checkpoint snapshot is read by the native lookup, whose result is logged.
The trusted-checkpoint path of `snapshot` runs only after a walk of 90,000
headers without a stored checkpoint. It also runs inside the native lookup.

## Static items

Same item list as the E01-02.inputs BSC block, with five changes:

- A new item, `blob-sidecars`, follows `block-rlp`. It holds the RLP list of
  the block's blob sidecars (an empty list when there are none), because the
  block encoding drops them while the native import checks and writes them.
  `DecodeStatic` decodes the list, checks that each sidecar names the block
  (`SanityCheck`) and attaches it to the decoded block (`WithSidecars`), so
  recovery imports the block with the sidecars it was admitted with. The
  private fixture is before Cancun, so its sidecar items are empty lists; a
  unit test covers a block that carries a sidecar.

- The ancestor window is the 256 BLOCKHASH ancestors (part B); it no longer
  reaches down to the checkpoint below the parent.
- `parlia-snapshot-keys` is replaced by `parlia-snapshots`, the snapshot
  declaration `bsc-parlia-snapshot-v1;capsule-headers` (part B).
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
block, with the capsule's blob sidecars, through the native import; the seam resumes it, and the log replays
the saved responses, so the obligation reaches EXEC_READY or NATIVE_REJECTED.
Then it calls `GoLive`. While the admission is RECOVERY_ONLY, a live import
of any other block is refused (`ErrMode`).

## Checks

`go/unit_test.go` covers the observation reader, the snapshot log, the
non-authoritative input failures, the static checks and window, the run
record, the pin and input adapters (including `Abandon` without a pin), the
digests, the guard registration, and the real chain freezer with a pinned
side branch (control: without the limit, the freezer deletes it).

`go/native_test.go` is the native probe on the accepted private fixture:

- live import;
- NATIVE_REJECTED of a block whose seal fails;
- six crash points of AC-CAP-02-01 and AC-CAP-02-02 under three store
  models, each followed by restart and recovery;
- identity refusals, the rewind guard and the unobserved-execution refusal;
- the hook contract with a fake hook (`TestNativeBSCHookContract`) and an
  import stopped during execution, then recovered
  (`TestNativeBSCStoppedImport`);
- a missing parent state node: the obligation stays ADMITTED, and recovery
  resolves it once the node is back (`TestNativeBSCParentStateIncomplete`);
- the system-call guard (`TestNativeBSCSystemCallGuard`).

`go/longchain_test.go` (`TestNativeBSCLongChain`) proves the window and
checkpoint rule and the system-contract calls on the real import path. The
chain has 1,030 blocks:
- It is generated by the native engine (Prepare, AssembleBlock, Seal, with
  the public test validator key from the BSC source tree) and validated by
  the native import.
- Timestamps are the parent's plus the period, so the chain is
  deterministic.
- Blocks 1 to 994 are imported natively, then 995 to 1030 through the seam,
  twice. The first import keeps one engine (warm). The second restarts the
  node before every block (cold).

The test checks:
- every observed block reaches EXEC_READY;
- epoch block 1000 makes its system calls on the parent;
- the stored checkpoint snapshot at 1024 exists;
- block 1030's window runs from 1029 down to 774, below that checkpoint;
- the warm and cold imports log the same snapshot bytes and reach the same
  exec digests.

The fixture has one validator and no vote attestations, so the quorum and
finality-reward walks and a validator-set switch with a lookback do not
occur in it.

The driver's `run` also starts the patched `geth` on a probe home that holds
live pins, and re-checks every digest and the trace order with its own
rules. `bsc_admission.mutants.json` lists the Go mutants. The Python mutants
and portable controls are in `test/harness/fixtures/bsc_admission`.
