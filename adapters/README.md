# Native adapters

## State provider and pins (E01-02.pins)

`adapters/<client>/state_provider` is the state-provider and pin seam for
Geth (`geth`), BSC (`bsc`) and Bor (`bor`). It keeps a pinned source root and
its required inputs available in the native store until an allowed release,
and it rejects an unsupported state backend or native profile before capture.
It implements the admission and source-pin rules of the native capture
protocol (`.project/docs/NATIVE_CAPTURE_PROTOCOL.md`).

Each package compiles inside its own accepted native module graph, so native
internal types are never merged into one graph. `provider.go` and
`provider_test.go` are shared and byte-identical in the three packages; the
driver refuses to prepare if they differ. `family.go` and `family_test.go`
hold the client differences. Bor also adds one file to its native
`internal/cli/server` package (`native_overlay/`), so the preflight reads the
same effective configuration as `bor server`; it never replaces a native file.

### Interface

- `Bind(db, storeID)` records the producer binding (store ID, chain ID, genesis
  hash, state scheme and the digest of the state profile) once and syncs it.
  A different binding is a foreign store, including one made under another
  profile (D108). A record without a profile was written before D108
  and is the profile of the hash scheme.
- `New(db, triedb, identity)` opens the provider only on a bound store of the
  hash or the path scheme, with the trie database of that scheme.
- `Pin(pin)` follows the protocol order: `triedb.Commit(parentRoot, false)`,
  `SyncKeyValue()`, a check that the root, every header and every code are
  readable, then the ledger record and a second `SyncKeyValue()`. A trie-cache
  reference alone is never a pin. A failed pin writes no record; repeating the
  same pin re-verifies it, and a different record under the same ID is refused.
  The caller holds the native observation guard, so the import cannot drop the
  parent root from the trie cache before `Pin` commits it (protocol step 2).
- `Release(id)` durably removes a live pin. The old state is then no longer
  promised; physical deletion can happen later.
- `Pins`, `Verify` and `BoundIdentity` read the ledger and fail closed on a
  malformed or misfiled record.
- `Preflight(instanceDir, expected)` checks a stopped native home read-only:
  the hash or the path scheme, genesis, chain, binding, every live pin, the
  retention floor N, and no interrupted offline prune. An empty expected store
  ID admits only an unbound store without pins. A rejected home is never wiped
  or reinitialized. The report names the oldest block the store still holds,
  its head and N (`Coverage`).
- `CheckProfile` enforces the fast-node capture profile of D108 on
  the effective native config; the rules are in the next section. Geth and
  BSC read the effective config from the native `dumpconfig` output
  (`DecodeEffectiveConfig`), after native config file, environment and flag
  precedence; Bor uses `EffectiveConfig(args)`, the native `bor server`
  precedence. Other values stay as the operator set them.
- `CheckStoreProfile(cfg, scheme)` completes `CheckProfile` with the scheme of
  the opened store. The flags of the fast node pass no state scheme, and the
  client then keeps the scheme of its store; the rules that depend on the
  scheme (the state history floor under path) are applied here. A config that
  names another scheme than the store is refused.

The ledger lives in the native key-value store under
`extractor-state-provider-store-v1` and `extractor-state-provider-pin-v1-<id>`.
Neither key has the 32-byte length or the code prefix that the native offline
pruner deletes. Consensus inputs that live outside the native store are copied
into the input capsule by E01-02.inputs; a pin retains native references only.

### Fast-node capture profile (D108)

For the live-verification stage every capture node is a fast node that keeps
the last N blocks. N is the immutability threshold of the client, which is
also its default state history: 600,000 blocks for BSC and 90,000 for Geth
and Bor. A pin is one block back, far inside N.

| Rule | BSC | Geth | Bor |
|---|---|---|---|
| Refused | a state scheme other than hash, path or none; `gcmode archive`; tries disabled (`--tries-verify-mode none`); block history set and below N; state history set and below N under path | a state scheme other than hash, path or none; `gcmode archive`; state history set and below N under path | a state scheme other than hash or path; `gcmode archive`; state history set and below N under path |
| Allowed and recorded | `--pruneancient` (deprecated: the node turns it into a block history of N and full sync); `--history.blocks=0`; `--history.state=0`; the transaction and log indexes and their search switches; `--history.chain`; `--history.trienode` | `--history.state=0`; the transaction and log indexes | `--history.state=0`; the transaction and log indexes |
| Unchanged | full sync only; no snap sync, voting or builder role (`bsclifecycle.CheckProfile`) | `--gcmode full` | `--gcmode full`, full sync, and no pipelined state-root import, parallel stateless import, witness sync, preconfirmations, sequencer stream or parallel EVM (D60) |

A value of 0 means the entire chain and is allowed. The search indexes and
the chain history mode do not feed the capture path. The profile is bound to
the store: `StoreIdentity.Profile` is the SHA-256 of the state profile string
of the scheme (`stateProfile` in `family.go`), and a store bound under another
string is refused.

A store that keeps fewer than N blocks behind its head is refused by the
preflight with `ErrForbiddenPruning`. Its oldest block is the greater of the
offset that the prune-block command of an older client left in the key-value
store and the tail of the ancient store. Native `Open` rewrites a legacy
offset even read-only, so a store that holds one is opened without its
ancient part; a pin whose headers moved to the ancient store is then refused
as unavailable.

The node wiring decides which scheme it opens. The BSC lifecycle opens hash
stores only until the path-scheme block of stream S0 lands; the Geth and Bor
wiring names the hash scheme in its identity. In all three, a pin on a path
store is refused by `Pin` with `ErrUnsupportedBackend` until the separate
path recovery proof that the native capture protocol requires exists.

### Native deletion paths

These native paths delete committed hash-scheme state or pinned inputs, from
a source review of the three accepted clients. Each has a ledger guard:

| Native path | Deletes | Guard |
|---|---|---|
| Offline `snapshot prune-state` (`pruner.Prune`) | Trie nodes and code outside the target | `PruneState` refuses while any pin is live |
| Startup resume of an interrupted prune (`pruner.RecoverPruning`) | Same | `RecoverPruning` refuses while any pin is live; `Preflight` rejects a home that holds a `statebloom.*` file |
| Head rewind (`BlockChain.SetHead`, crash repair) | Every header above the new head | `RequireRewindAllowed(db, newHead)` refuses a rewind below a pinned header |
| Chain freezer (`chain_freezer.go` side-chain wipe) | Side-chain blocks at every frozen number and their dangling descendants | `FreezeLimit(db, limit)` keeps freezing at or below the fork point of every pinned side-branch header; frozen canonical headers stay readable |
| BSC `--history.blocks`, `--history.state` under path; Bor `--history.state` under path | Block data or state history older than N | `CheckProfile` rejects a value set below N; 0 (the entire chain) and N or more pass |
| BSC `--history.blocks` at run time (the native freezer truncates the ancient store), Bor `snapshot prune-block` (offline), or a home pruned by an older BSC `prune-block` (the accepted BSC commit has no such command) | Old block data, headers included | `Preflight` refuses a store whose oldest block is above `head - N` with `ErrForbiddenPruning` |

Geth has no online setting at this commit that deletes pinned state or
headers; for Ethereum, `forbidden_pruning` is the offline prune-state path,
covered by the `statebloom` preflight check and the guarded pruner.

The trie cache's own garbage collection (`Dereference`) only drops memory,
which is why `Pin` commits and syncs the root. Under the path scheme a pin is
the persistent layer of the store; it is defined and proven together with the
path recovery proof of D108, and until then `Pin` applies to the hash scheme
only.

The prune, recovery and rewind guards are tested in-process against the
native code; the freezer guard is unit-tested only, because a real freeze
needs 90,000 blocks. Calling the guards from the native command, startup and
freezer paths of a shipped binary needs a patched native build, so
AC-CAP-03-01 `pinned_state_pruning_attempt` is proven in-process, not for the
shipped binaries; see the open decision in `.project/PROJECT_STATUS.md`.

### Native tests

The Go tests need the accepted private fixture of the family
(`EXTRACTOR_PINS_INPUTS` with `genesis.json` and `blocks.rlp`), a writable
`EXTRACTOR_PINS_WORK`, a report path `EXTRACTOR_PINS_REPORT`, and for Geth and
BSC the accepted native binary in `EXTRACTOR_NATIVE_BINARY`.
`test/harness/pins.py` supplies them in owned offline
containers:

```sh
python3 -I test/harness/pins.py prepare --build-manifest /abs/manifest.json \
  --sha256 ACCEPTED_BUILD_SHA256 --output /abs/new-preparation
python3 -I test/harness/pins.py run /abs/new-preparation/tool.json --sha256 TOOL_PIN \
  --profile-manifest /abs/eth.json PIN --profile-manifest /abs/bsc.json PIN \
  --profile-manifest /abs/bor.json PIN --output /abs/new-run
```

The mutant check of these packages runs their single-check mutants, each in
the native module of its family:

```sh
python3 -I test/harness/pins_mutants.py mutants /abs/new-preparation/tool.json --sha256 TOOL_PIN \
  --profile-manifest /abs/eth.json PIN --profile-manifest /abs/bsc.json PIN \
  --profile-manifest /abs/bor.json PIN --output /abs/new-mutants
```

Both commands record the git commit of the checkout and refuse when the
tracked adapter or harness files differ from it, so evidence names the exact
executed source. Preparation compiles each package with the accepted compiler
and offline module cache (`go vet`, then `go test -c`) within 3,600 seconds. The run checks
each test-only ProfileManifest, copies the fixture inputs, runs the compiled
tests under one 1,800-second budget, and independently checks the retained
report against the fixture's prepared block roots. Both commands hold the
host-wide single-writer lock, refuse while Docker holds another run's harness
container, refuse an existing output without writing into it, and always write
their record into an output they created, with stop signals blocked from the
start of that step until the lock is released. A retry links the earlier FAIL
by its SHA-256 pin and a correction (`--previous-attempt PROOF
--previous-sha256 PIN --correction TEXT` for preparation, `--previous-run`
for a run).

| Test | Covers |
|---|---|
| `TestNativeVolatileTrieLost` | AC-CAP-03-01 `volatile_trie_lost`: a child process imports blocks 1–2, pins block 1 and is killed with SIGKILL; the pinned root and full state survive and native recovery resumes from it. The unpinned control loses block 1's state and rewinds to genesis. |
| `TestNativePinnedStatePruning` | AC-CAP-03-01 `pinned_state_pruning_attempt` and the missing-provider half of AC-CAP-03-02: the guarded native pruner refuses while pins are live; the same native pruner without the guard deletes a pinned side-branch state and its code, and preflight then rejects the home; after release the guarded prune deletes it. |
| `TestNativeRewindDeletesPinnedHeader` | Native `SetHead` deletes a pinned header unless the rewind guard refuses it. |
| `TestNativeStorePreflight` | AC-CAP-03-02 `wrong_datadir`, `wrong_backend`, `forbidden_pruning`, and the retention below N of D108; AC-OPS-03-02 `foreign_identity`, `foreign_genesis`. A path-initialized store is accepted with the profile of the path scheme; a home pruned N blocks behind its head passes and one pruned closer is refused; each home is byte-for-byte unchanged. |
| `TestNativeEffectiveProfile` | AC-OPS-01-02 and AC-OPS-01-03 with the native parser: allowed arguments, environment and config are reflected; forbidden values are rejected whichever source sets them, and each rejection must name its expected reason. Bor's `bor server` parser reads no configuration environment at this commit, so for Polygon `allowed_environment` is covered by `environment_is_not_a_config_source`. |
| `TestNativeLegacyPruneOffset` (BSC, Bor) | The offset of the offline prune-block commands is the oldest block of the store: N blocks behind the head it passes, closer or on a home without a head it is refused. |
| `TestNativeRetentionFloor` (BSC, Bor) | A home whose ancient store a block-pruning run truncated: the native tail N blocks behind the head passes, one block closer is refused; the home is byte-for-byte unchanged. |
| `TestUnit*` | Ledger, binding and its profile, release, prune, recovery and freezer guards, the retention rule, and the profile rules of each client on a native memory store. |

The side-branch state in the pruning test has an unsealed header written by
the test; it stands for a known side branch and is not a consensus-valid block.
These tests use private local fixtures and give no public-profile credit.
