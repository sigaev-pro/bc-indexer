# Native adapters

## State provider and pins (E01-02.pins)

`adapters/<client>/state_provider` is the hash-provider and pin seam for Geth
(`geth`), BSC (`bsc`) and Bor (`bor`). It keeps a pinned source root and its
required inputs available in the native store until an allowed release, and it
rejects an unsupported state backend or native profile before capture. It
implements the admission and source-pin rules of
[NATIVE_CAPTURE_PROTOCOL](../.project/docs/NATIVE_CAPTURE_PROTOCOL.md).

Each package compiles inside its own accepted native module graph, so native
internal types are never merged into one graph. `provider.go` and
`provider_test.go` are shared and byte-identical in the three packages; the
driver refuses to prepare if they differ. `family.go` and `family_test.go`
hold the client differences. Bor also adds one file to its native
`internal/cli/server` package (`native_overlay/`), so the preflight reads the
same effective configuration as `bor server`; it never replaces a native file.

### Interface

- `Bind(db, storeID)` records the producer binding (store ID, chain ID, genesis
  hash, state scheme) once and syncs it. A different binding is a foreign store.
- `New(db, triedb, identity)` opens the provider only on a bound hash-scheme
  store with the hash trie database.
- `Pin(pin)` follows the protocol order: `triedb.Commit(parentRoot, false)`,
  `SyncKeyValue()`, a check that the root, every header and every code are
  readable, then the ledger record and a second `SyncKeyValue()`. A trie-cache
  reference alone is never a pin. A failed pin writes no record; repeating the
  same pin is idempotent and a different record under the same ID is refused.
- `Release(id)` durably removes a live pin. The old state is then no longer
  promised; physical deletion can happen later.
- `Pins`, `Verify` and `BoundIdentity` read the ledger and fail closed on a
  malformed or misfiled record.
- `Preflight(instanceDir, expected)` checks a stopped native home read-only:
  hash scheme, genesis, chain, binding, every live pin, and no interrupted
  offline prune. An empty expected store ID admits only an unbound store
  without pins. A rejected home is never wiped or reinitialized.
- `CheckProfile` enforces the capture profile on the effective native config:
  `--state.scheme hash` and `--gcmode full` for all three; for BSC also no
  `--pruneancient` and no nonzero `--history.blocks`; for Bor also full sync and
  no pipelined state-root import, parallel stateless import, witness sync,
  preconfirmations, sequencer stream or parallel EVM (D60). Geth and BSC read
  the effective config from the native `dumpconfig` output
  (`DecodeEffectiveConfig`), after native config file, environment and flag
  precedence; Bor uses `EffectiveConfig(args)`, the native `bor server`
  precedence. Other values stay as the operator set them.

The ledger lives in the native key-value store under
`extractor-state-provider-store-v1` and `extractor-state-provider-pin-v1-<id>`.
Neither key has the 32-byte length or the code prefix that the native offline
pruner deletes. Consensus inputs that live outside the native store are copied
into the input capsule by E01-02.inputs; a pin retains native references only.

### Native deletion paths

In the hash scheme the native store loses committed state and inputs only
through these paths. Each has a ledger guard:

| Native path | Deletes | Guard |
|---|---|---|
| Offline `snapshot prune-state` (`pruner.Prune`) | Trie nodes and code outside the target | `PruneState` refuses while any pin is live |
| Startup resume of an interrupted prune (`pruner.RecoverPruning`) | Same | `RecoverPruning` refuses while any pin is live; `Preflight` rejects a home that holds a `statebloom.*` file |
| Head rewind (`BlockChain.SetHead`, crash repair) | Every header above the new head | `RequireRewindAllowed(db, newHead)` refuses a rewind below a pinned header |
| BSC `--pruneancient`, `--history.blocks` | Old block data | Rejected by `CheckProfile` |

The trie cache's own garbage collection (`Dereference`) only drops memory,
which is why `Pin` commits and syncs the root. Path-scheme stores are rejected
by `New` and `Preflight` until a separate recovery proof exists.

The guards are tested in-process against the native code. Calling them from
the native command and startup paths of a shipped binary needs a patched native
build, which no accepted task produces yet; see the open decision in
[PROJECT_STATUS](../.project/PROJECT_STATUS.md).

### Native tests

The Go tests need the accepted private fixture of the family
(`EXTRACTOR_PINS_INPUTS` with `genesis.json` and `blocks.rlp`), a writable
`EXTRACTOR_PINS_WORK`, a report path `EXTRACTOR_PINS_REPORT`, and for Geth and
BSC the accepted native binary in `EXTRACTOR_NATIVE_BINARY`.
[test/harness/pins.py](../test/harness/pins.py) supplies them in owned offline
containers:

```sh
python3 -I test/harness/pins.py prepare --build-manifest /abs/manifest.json \
  --sha256 ACCEPTED_BUILD_SHA256 --output /abs/new-preparation
python3 -I test/harness/pins.py run /abs/new-preparation/tool.json --sha256 TOOL_PIN \
  --profile-manifest /abs/eth.json PIN --profile-manifest /abs/bsc.json PIN \
  --profile-manifest /abs/bor.json PIN --output /abs/new-run
```

Preparation compiles each package with the accepted compiler and offline
module cache (`go vet`, then `go test -c`) within 3,600 seconds. The run checks
each test-only ProfileManifest, copies the fixture inputs, runs the compiled
tests under one 1,800-second budget, and independently checks the retained
report against the fixture's prepared block roots. Both commands hold the
host-wide single-writer lock, refuse while Docker holds another run's harness
container, refuse an existing output without writing into it, and always write
their record into an output they created, with stop signals blocked during that
write. A retry links the earlier FAIL with its pin and a correction.

| Test | Covers |
|---|---|
| `TestNativeVolatileTrieLost` | AC-CAP-03-01 `volatile_trie_lost`: a child process imports blocks 1–2, pins block 1 and is killed with SIGKILL; the pinned root and full state survive and native recovery resumes from it. The unpinned control loses block 1's state and rewinds to genesis. |
| `TestNativePinnedStatePruning` | AC-CAP-03-01 `pinned_state_pruning_attempt` and the missing-provider half of AC-CAP-03-02: the guarded native pruner refuses while pins are live; the same native pruner without the guard deletes a pinned side-branch state and its code, and preflight then rejects the home; after release the guarded prune deletes it. |
| `TestNativeRewindDeletesPinnedHeader` | Native `SetHead` deletes a pinned header unless the rewind guard refuses it. |
| `TestNativeStorePreflight` | AC-CAP-03-02 `wrong_datadir`, `wrong_backend`, `forbidden_pruning`; AC-OPS-03-02 `foreign_identity`, `foreign_genesis` and a path-initialized store; each home is byte-for-byte unchanged. |
| `TestNativeEffectiveProfile` | AC-OPS-01-02 and AC-OPS-01-03 with the native parser: allowed arguments, environment and config are reflected; forbidden values are rejected whichever source sets them. |
| `TestUnit*` | Ledger, binding, release, guard and profile rules on a native memory store. |

The side-branch state in the pruning test has an unsealed header written by
the test; it stands for a known side branch and is not a consensus-valid block.
These tests use private local fixtures and give no public-profile credit.
