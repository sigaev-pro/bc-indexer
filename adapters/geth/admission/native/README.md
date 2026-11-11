# Geth admission wiring (E01-02.admission, Geth block)

This directory holds the Geth-only part of E01-02.admission (#16): the
product native patch set and the package that joins the shared admission
core to the accepted Geth import path. The shared files one level up stay
byte-identical for Geth, BSC and Bor; this directory is not compared.

The accepted tree is Geth `1df30b18d9ed35ee9d28ffd4648442c2204554ad`
(E01-01.builds). The preparation of
[test/harness/fixtures/admission_geth](../../../../test/harness/fixtures/admission_geth/admission_geth.py)
applies [patches.json](patches.json) and installs these packages under
`extractor/` in that tree (module `github.com/ethereum/go-ethereum`):

| Tree path | Source |
|---|---|
| `extractor/admission` | the shared admission core (`adapters/geth/admission`) |
| `extractor/source_inputs` | the shared capsule and log (`adapters/geth/source_inputs`) |
| `extractor/state_provider` | the pin ledger (`adapters/geth/state_provider`, `provider.go` and `family.go`) |
| `extractor/admission/native` | [geth.go](geth.go) and its tests |
| `core/extractor_admission.go` | [core_hook.go.txt](core_hook.go.txt) |
| `core/rawdb/extractor_retention.go` | [rawdb_hook.go.txt](rawdb_hook.go.txt) |

## Native patch set

Every patch in `patches.json` replaces one text that occurs exactly once in
the accepted tree; the preparation refuses anything else and records the file
digests.

| Patch | File | What it does |
|---|---|---|
| `vm_config_fields` | `core/vm/interpreter.go` | Adds `ExtractorGetHash` and `ExtractorSafePoint` to `vm.Config` |
| `vm_safe_point` | `core/vm/interpreter.go` | The table loop calls the safe point between VM steps, before the opcode hook and before the opcode changes memory or state (D98 item 4) |
| `vm_safe_point_loop` | `core/vm/interpreter.go` | An execution with a safe point runs the table loop; the generated loop has none. The native differential test keeps the two loops equal |
| `state_processor_get_hash` | `core/state_processor.go` | The serial processor answers BLOCKHASH with `cfg.ExtractorGetHash` |
| `blockchain_field` | `core/blockchain.go` | Holds the admission seam |
| `insert_chain_guard`, `insert_without_head_guard`, `set_canonical_guard` | `core/blockchain.go` | The three import entry points that reach `ProcessBlock` take the observation guard before the chain mutex |
| `rewind_guard` | `core/blockchain.go` | A requested rewind (`SetHead`, `SetHeadWithTimestamp`, `debug_setHead`) calls `RequireRewindAllowed` under the chain mutex (D98 item 2) |
| `process_block_begin` | `core/blockchain.go` | For an import that writes state, `ProcessBlock` calls `Begin` before `setupExecutionState` (so before the prefetcher and `processor.Process`), turns off the parallel processor, and closes the observation when it returns |
| `process_block_configure` | `core/blockchain.go` | Installs the saved BLOCKHASH answers and the safe point into the config of `processor.Process` only |
| `stateless_self_check` | `core/blockchain.go` | The stateless self-check (`--vm.statelessselfvalidation`) runs with a config without the BLOCKHASH answers and the safe point, so nothing is captured twice |
| `process_block_exec_ready` | `core/blockchain.go` | `ExecReady` after `ValidateState` and the stateless self-check, before the block is written |
| `freezer_limit` | `core/rawdb/chain_freezer.go` | The chain freezer stays at or below `FreezeLimit` (D98 item 2) |
| `backend_import`, `backend_preflight`, `backend_recover_pruning`, `backend_attach` | `eth/backend.go` | Startup: `Preflight` before the store opens, `RecoverPruning` through the guard, `Attach` after `NewBlockChain`, and `RequireNoCapture` without a config |
| `snapshot_import`, `snapshot_prune_state` | `cmd/geth/snapshot.go` | `geth snapshot prune-state` calls `PruneState` (D98 item 2) |
| `writable_database_guard` | `cmd/utils/flags.go` | `MakeChainDatabase` (and so `MakeChain`) runs the manual database guard before every writable open of an existing store (D100 item 3) |
| `removedb_guard` | `cmd/geth/dbcmd.go` | `geth removedb` runs the manual database guard before any prompt or deletion (D100 item 3) |
| `generate_trie_guard` | `cmd/geth/snapshot.go` | `geth snapshot generate-trie` runs the manual database guard before it opens the source store and the source chain freezer for writing (D100 item 3) |

The preparation also installs `utils_hook.go.txt` as
`cmd/utils/extractor_guard.go` (`ExtractorGuardChaindata`), next to
`core_hook.go.txt` and `rawdb_hook.go.txt`.

The guards of D98 item 2 and the capture-store refusal are active in every
binary that links `extractor/admission/native`: its `init` sets
`core.ExtractorRewindAllowed`, `core.ExtractorRequireNoCapture` and
`rawdb.ExtractorFreezeLimit`, and `eth` imports it. They do not depend on
an admission config, because pins of an earlier run stay live.

## Per-block observation on the import path

All Geth imports that execute a block go through `ProcessBlock`, reached
only through `InsertChain`, `InsertBlockWithoutSetHead` and `SetCanonical`,
each with `WriteState` set. `debug_executionWitness` also calls
`ProcessBlock`, directly and without writing state; such an execution is not
observed and runs natively. For a state-writing import, with the guard and
the chain mutex held, the patched `ProcessBlock` calls `Admission.Begin`
before `setupExecutionState`, so before the speculative prefetcher, the
tracer's block start and `processor.Process`. In LIVE:

1. `admission.Begin` records INPUT_PREPARING (counter sync).
2. `stateprovider.Pin` pins the parent root under the observation key: trie
   commit, sync, ledger record, sync. The pin names the parent header.
3. `source_inputs.SealCapsule` seals the static capsule (see below).
4. `admission.Admit` verifies the pin and the capsule and syncs ADMITTED.
   This is the closing ADMITTED sync; nothing of the block runs before it.
5. `source_inputs.OpenLog` opens the dependency log.

If a step before `Admit` fails, the observation is abandoned at once (its pin
is released) and the block is refused. The parallel processor is turned off
before `setupExecutionState` picks the state reader, because it reads
neither the saved answers nor the safe point. Right before
`processor.Process`, the observation installs the BLOCKHASH answers of the
capsule and the VM safe point into that execution's config only; the
prefetcher keeps its own copy without them, and the stateless self-check
gets a copy without them.

When `processor.Process`, `ValidateState` or the stateless self-check fails,
the deferred `Close` writes NATIVE_REJECTED with the digest of the native
rejection evidence (`RejectionDigest`: block hash and native error text),
never EXEC_READY; the pin is released. When validation passes, `ExecReady`
closes the log (`Log.Finish`) and writes EXEC_READY with the execution digest
before the block is written. A failure before the execution is configured (for
example missing parent state), a BLOCKHASH lookup that the capsule cannot
answer, or an error of `ExecReady` itself is not a native verdict: the
observation stays ADMITTED, the block is refused, and recovery resolves it.

Geth has no external dependency on this profile: under the static manifest
decision of #14, the BLOCKHASH window headers are static capsule items, so
the log closes with count 0. The EXEC_READY record still binds its closed
consumed manifest (D98 item 7).

`ExecResultDigest` (block hash, gas used and the consensus encoding of every
receipt) is the execution digest until the capture side computes the digest
of the record corpus; `Options.ExecDigest` is where it plugs in
(E01-02.decision_bridge and E04-01.block_record).

### Capsule of a Geth block

Items in order: `header-rlp`, `body-rlp`, `encodings`, `parent-header`,
`parent-hash`, `parent-root`, `ancestor-header-<n>` for every block of the
BLOCKHASH window below the parent (from `max(0, N-256)` to `N-2`),
`chain-config`, `fork-id`, `native-commit`, `normalization-profile`. The
window headers are found through parent links, so a side branch gets its own
ancestors. `chain-config` and `fork-id` use the shared canonical JSON rule of
D99 item 3 (`admission.CanonicalJSON`); integers such as
`terminalTotalDifficulty` keep their literal. `normalization-profile` is the
configured profile ID; E04-01.block_record defines the product profile
(D99 item 4).

`blockHashAnswers` checks that the window items are exactly the headers of
`max(0, N-256)` to `N-1`, each the parent of the next, and maps every number
to the Keccak-256 hash of the saved header bytes. The EVM asks `GetHash` only
inside that window, so a block of height 257 or more reads all 256 saved
headers and never its own chain.

## Recovery driver (AC-CAP-02-02)

`Attach` opens the admission in RECOVERY_ONLY and runs `Recover` before any
import:

1. Every INPUT_PREPARING observation is abandoned; it is not a capture, and
   its pin, if any, is released (an absent pin releases as nil).
2. Every ADMITTED obligation is re-executed through the patched import path:
   the block is decoded from the saved `header-rlp` and `body-rlp` and passed
   to `InsertBlockWithoutSetHead`, which writes the block but does not move
   the head, because the original import may not have set it (engine
   `newPayload`); the next import or the consensus client decides the head as
   natively. In RECOVERY_ONLY, `Begin` accepts only a block whose candidate
   equals a saved obligation, and only when every static item the node would
   seal now (block encodings, window headers, chain config, fork ID, native
   commit, profile) equals the saved capsule byte for byte; otherwise the
   obligation stays ADMITTED and startup stops, so EXEC_READY never binds a
   capsule that differs from the execution. It reuses that observation (no
   new sequence), its pin, its capsule answers and its log. Native validation
   then writes EXEC_READY or NATIVE_REJECTED as in LIVE.
3. `GoLive` switches to LIVE. An obligation that is still open stops startup.

Any other block, a new H, is refused with `admission.ErrMode` while an
obligation is open, and `GoLive` is refused with `admission.ErrPending`.

## Startup identity

The shipped binary reads the admission config from the file named by
`EXTRACTOR_ADMISSION_CONFIG` (strict JSON: `store_id`, `producer_id`,
`chain_id`, `genesis_hash`, `native_commit`, `normalization_profile`).
Without it, the node starts only on a store that does not belong to a
capture producer: `RequireNoCapture` refuses a bound store, a live pin or a
run record. The same check runs in `ProcessBlock` for every state-writing
import without an admission seam, so commands that open the chain without
`eth.New` (for example `geth import`) cannot import into such a store
either. The retention guards stay active in both cases. The capture
profile needs the hash state scheme (`--state.scheme hash`, E01-02.pins);
`Attach` refuses a store of another scheme. With the config:

- Before native code opens the store, `Preflight` runs the read-only
  `stateprovider.Preflight` of E01-02.pins: a store of another identity, an
  interrupted prune or an unreadable pin is refused and nothing is written. A
  store that is still unbound and has no pins passes, and so does a new node
  without chaindata.
- After `NewBlockChain`, `Attach` binds the store (`stateprovider.Bind`),
  checks the bound chain and genesis against the config, and opens the saved
  producer run from the run record `extractor-admission-geth-run-v1`
  (`store_id`, `producer_id`, `run_id`). The first start records a run; every
  restart opens the same run, so `admission.Open` sees its obligations. A run
  record of another store or producer is refused (`ErrRun`).

Which producer and caller map own the live boundary H stay with
E01-02.lifecycle_seams.

## Manual database guard (D100 item 3)

The pin ledger and the admission records live in chaindata. A manual `geth`
command that deletes or rewrites chaindata would lose them silently: after
`geth removedb`, the node starts as a new node, `Preflight` lets it pass, and
the ADMITTED obligations are gone. `RequireNoOpenCapture` refuses while a pin
is live (`stateprovider.RequireNoLivePins`, the ledger check of the other
retention guards) or an observation of any producer run in the store is
INPUT_PREPARING or ADMITTED (`OpenObligations`). `OpenObligations` reads the
records as `admission.Open` does, between the low-water mark and the counter
of each run, so an INPUT_PREPARING record beyond the counter (an interrupted
`Begin`) is not an observation; a key under `extractor-admission-v1/` that
does not fit the layout fails closed. An EXEC_READY observation is not open,
but it keeps its pin until the decision bridge closes it, so its store stays
refused.

`ExtractorGuardChaindata` opens an existing store read-only with the
database flags of the command, runs the check and closes it. It writes no
key; as at any read-only open, the native freezer creates its directory and
lock file if they are missing at the flag path. A directory without a native
store passes. Every caller already
holds the instance lock of the native node (`makeConfigNode`), so no geth
process can add a pin between the check and the writable open. A refused
command exits with status 1 before it writes.

Commands of the shipped `geth` (source `1df30b18`) that open chaindata for
writing or delete it:

| Command | Writes | Guard |
|---|---|---|
| `removedb` | Deletes the key-value store, the state freezers and the chain freezer | `removedb_guard`, before any prompt |
| `init` | Writes the genesis block and the chain config | `MakeChainDatabase` |
| `import` | Imports blocks | `MakeChain`; on a bound store without pins or open obligations, `RequireNoCapture` in `ProcessBlock` still refuses before any block is written |
| `import-history` | Writes ancient bodies and receipts | `MakeChain` |
| `import-preimages` | Writes preimages | `MakeChainDatabase` |
| `prune-history` | Deletes history below the prune point | `MakeChain` |
| `db compact` | Rewrites the key-value files | `MakeChainDatabase` |
| `db delete`, `db put` | Delete or overwrite one key | `MakeChainDatabase` |
| `db import` | Writes keys from a dump | `MakeChainDatabase` |
| `db import-badblocks` | Writes the bad-block store | `MakeChainDatabase` |
| `db inspect-trie` | Only reads, but opens the store writable natively | `MakeChainDatabase`; refused on a capture store as well |
| `bintrie convert` | Writes a binary trie, and deletes the source state with `--delete-source` | `MakeChainDatabase` |
| `snapshot prune-state` | Deletes state outside the target root | `MakeChainDatabase`, then `PruneState` (D98 item 2) |
| `download-era` | Writes downloaded era1 files into `chaindata/ancient/chain/era` (or the `--era.dir` directory) without opening the store | Not guarded: it checks each file against the era1 checksums of a public network and changes no key, no freezer table and no admission or pin record |
| `db pebble-upgrade` | Raises the format of a pebble v1 store | Not guarded: `pebble.Upgrade` returns without a write when the store already has a pebble v2 format, and a store with pins or admission records was written by this pebble v2 binary |
| `snapshot generate-trie` | Opens the source pebble store writable to take a hard-linked checkpoint outside chaindata, then opens the checkpoint with the source chain freezer writable, so the native freezer may append blocks to the source freezer | `generate_trie_guard`, before the checkpoint |

Every other command opens chaindata read-only or does not touch it; the node
itself (`geth`, `geth console`) starts through `eth.New` and its startup
guards. Since `snapshot prune-state` now meets the manual database guard
first, `PruneState` refuses there only as a second check; its own refusal is
covered by the E01-02.pins unit tests.

## Pin and inputs adapters

`Pins.Verify(id, parentRoot)` reads the ledger record of the observation,
checks its store identity and parent root, runs `stateprovider.Verify` and
returns the SHA-256 of the record bytes. `Pins.Release` maps
`stateprovider.ErrUnknownPin` to nil, so the Abandon of an observation whose
pin was never written succeeds. `Inputs` is the same adapter as the block 0
binding glue, over the admission store.

## Guard and lock order

The entry points take the guard before the chain mutex; `Begin`,
`ExecReady` and `Close` use the token of the current import and refuse
without it (`ErrGuardMissing`), so an entry point without the guard patch
fails closed. Only state-writing imports, which run under the guard and the
chain mutex, reach `Begin`. The recovery driver holds the guard only between its import
calls. Pins are written only under the chain mutex, and the rewind guard
runs under it too, so no pin appears between the check and a rewind.

## Limits

- The safe point is installed for the serial execution of admitted blocks
  only; such executions run the table loop, which is slower than the
  generated loop. The capture side (E04-04.import_pressure) installs the
  `core/capture` function; the probe installs one that captures every CALL
  input.
- The rewind guard covers requested rewinds. Startup repair rewinds
  (`repair` true) keep native behavior: they stop at the newest block with
  state, and every pinned parent root is committed to disk. The native
  chain-config compatibility rewind ignores the error of `SetHead`, so a
  refused rewind there leaves the head in place.
- The manual database guard opens the store read-only with the
  `--datadir.ancient` flag of the command, as `MakeChainDatabase` does, while
  `geth removedb` deletes the freezer that the TOML config may also name.
  The pins and the admission records are in the key-value store, so the
  check does not depend on the freezer; if the read-only open fails, the
  command is refused.
- `SetCanonical` re-executes blocks only when the new head has no state
  (`recoverAncestors`); that path takes the guard and admits each block as a
  new observation, but no scenario exercises it.
- `native-commit` comes from the config file, not from the binary.
- Every EXEC_READY observation keeps its pin until the decision bridge
  closes it (E01-02.decision_bridge); with block-by-block import, pins and
  committed parent roots accumulate until then.
