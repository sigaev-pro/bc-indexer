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
| `process_block_begin` | `core/blockchain.go` | `ProcessBlock` calls `Begin` before `processor.Process` and closes the observation when it returns |
| `process_block_exec_ready` | `core/blockchain.go` | `ExecReady` after `ValidateState` and the stateless self-check, before the block is written |
| `freezer_limit` | `core/rawdb/chain_freezer.go` | The chain freezer stays at or below `FreezeLimit` (D98 item 2) |
| `backend_import`, `backend_preflight`, `backend_recover_pruning`, `backend_attach` | `eth/backend.go` | Startup: `Preflight` before the store opens, `RecoverPruning` through the guard, `Attach` after `NewBlockChain` |
| `snapshot_import`, `snapshot_prune_state` | `cmd/geth/snapshot.go` | `geth snapshot prune-state` calls `PruneState` (D98 item 2) |

The guards of D98 item 2 are active in every binary that links
`extractor/admission/native`: its `init` sets `core.ExtractorRewindAllowed`
and `rawdb.ExtractorFreezeLimit`, and `eth` imports it. They do not depend on
an admission config, because pins of an earlier run stay live.

## Per-block observation on the import path

All Geth imports that execute a block go through `ProcessBlock`. With the
guard and the chain mutex held, the patched `ProcessBlock` calls
`Admission.Begin` before `processor.Process`. In LIVE:

1. `admission.Begin` records INPUT_PREPARING (counter sync).
2. `stateprovider.Pin` pins the parent root under the observation key: trie
   commit, sync, ledger record, sync. The pin names the parent header.
3. `source_inputs.SealCapsule` seals the static capsule (see below).
4. `admission.Admit` verifies the pin and the capsule and syncs ADMITTED.
   This is the closing ADMITTED sync; nothing of the block runs before it.
5. `source_inputs.OpenLog` opens the dependency log.

If a step before `Admit` fails, the observation is abandoned at once (its pin
is released) and the block is refused. The observation then installs into
the execution config the BLOCKHASH answers of the capsule and the VM safe
point, and turns off the parallel processor, which reads neither.

When `processor.Process`, `ValidateState` or the stateless self-check fails,
the deferred `Close` writes NATIVE_REJECTED with the digest of the native
rejection evidence (`RejectionDigest`: block hash and native error text),
never EXEC_READY; the pin is released. When validation passes, `ExecReady`
closes the log (`Log.Finish`) and writes EXEC_READY with the execution digest
before the block is written. A BLOCKHASH lookup that the capsule cannot
answer, or an error of `ExecReady` itself, is not a native verdict: the
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
   to `InsertChain`. In RECOVERY_ONLY, `Begin` accepts only a block whose
   candidate equals a saved obligation and whose encodings equal the saved
   items; it reuses that observation (no new sequence), its pin, its capsule
   answers and its log. Native validation then writes EXEC_READY or
   NATIVE_REJECTED as in LIVE.
3. `GoLive` switches to LIVE. An obligation that is still open stops startup.

Any other block, a new H, is refused with `admission.ErrMode` while an
obligation is open, and `GoLive` is refused with `admission.ErrPending`.

## Startup identity

The shipped binary reads the admission config from the file named by
`EXTRACTOR_ADMISSION_CONFIG` (strict JSON: `store_id`, `producer_id`,
`chain_id`, `genesis_hash`, `native_commit`, `normalization_profile`).
Without it the node runs without admission; the retention guards stay
active. With it:

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
fails closed. The recovery driver holds the guard only between its import
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
- `geth removedb` and manual database tools are not guarded.
- Every EXEC_READY observation keeps its pin until the decision bridge
  closes it (E01-02.decision_bridge); with block-by-block import, pins and
  committed parent roots accumulate until then.
