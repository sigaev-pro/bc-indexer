# Capture-enabled BSC client image

`bsc_capture_build.py` builds the first runnable BSC client that carries the
extractor patches: upstream BSC v1.8.2 (commit
`54afaff4da31ac9e49ba646a4f65104edac5a2f3`) plus patches 01 to 08 of this
repository, compiled in the pinned Go 1.27.3 compiler image and packaged on
the pinned Debian 13.7 runtime image as `bc-indexer/bsc-capture:1.8.2-capture`
for linux/amd64.

This is a harness tool, not the accepted output of E01-01.builds. The
candidate lock (`.project/bootstrap/components/candidate-lock.json`) keeps
`patched-native-images` at PENDING_IMPLEMENTATION, and the image gives no
public-profile or AC credit. The seams it carries were proven on private
local fixtures only.

## What the build does

1. It reads the accepted BuildManifest and binds it to the candidate lock:
   the BSC commit, the Go version and both image references must agree.
2. It extracts the accepted BSC source archive and applies patches 01 to 08
   through the existing install path of the lifecycle block (`install` in
   `test/harness/fixtures/lifecycle_bsc/bsc_lifecycle.py`): the admission
   patches 01 to 06, the decision patch 07, the lifecycle patch 08, then the
   `extractor/` packages. The strict diff applier in
   `test/harness/bsc_admission.py` is the only patch code; this tool has
   none of its own.
3. In the pinned compiler image, offline and with the read-only module cache
   of the accepted build, it runs `gofmt -l`, checks the compiler version
   and runs `go build -trimpath -buildvcs=false ./cmd/geth`.
4. It builds the image from `Dockerfile` next to this file: the pinned
   runtime image and the binary in one layer, with `SOURCE_DATE_EPOCH` set
   to the upstream commit time.
5. It writes `provenance.json` into the output directory and the same facts
   into the image labels.
6. It runs `version`, `dumpconfig` and `ldd` in the new image, in owned
   offline containers. It refuses a version or an effective config that
   differs from the pins and the capture profile, and a binary with a shared
   library that the runtime image does not hold.

## Build

Requirements: Python 3.12 or newer; Docker with the pinned `golang` and
`debian` images already present; the accepted BuildManifest with its source
archive and module cache. All paths must be absolute, with no symlink
ancestors. `OUTPUT` must be a new directory outside the accepted build
storage, for example under the ignored `.project/runtime/` directory of a
checkout.

```sh
python3 -I test/harness/bsc_capture_build/bsc_capture_build.py build \
  --build-manifest "$BUILD_ARTIFACTS/manifest.json" \
  --sha256 "$ACCEPTED_BUILD_MANIFEST_SHA256" \
  --output "$OUTPUT"
```

The command holds the host-wide harness lock, pulls nothing, and uses two
CPUs. Exit 0 with `"result": "PASS"` means the image is built and checked.
Every run writes `OUTPUT/build.json` with its commands and logs; a failed
check removes the image tag, so a failed record never leaves its image.

## Verify and smoke

`dumpconfig` needs a writable data directory, so give it one:

```sh
docker run --rm bc-indexer/bsc-capture:1.8.2-capture version
docker run --rm --mount type=tmpfs,destination=/work \
  bc-indexer/bsc-capture:1.8.2-capture --chapel --datadir /work/dumpconfig \
  --state.scheme hash --gcmode full --syncmode full dumpconfig
```

The `[Eth]` section then shows `StateScheme = "hash"`, `SyncMode = "full"`,
`NoPruning = false` and `PruneAncientData = false`.

`smoke` starts the image offline on a throwaway home. It takes the Chapel
genesis from the image itself, runs the init, starts the node with a minimal
capture config for `--seconds` after its start, stops it with SIGINT, and
runs two controls on the same home. The node log names the Chapel network,
the chain configuration, full sync and the clean stop. The controls show
the extractor hooks in the start path: the default sync mode (snap) is
refused with `bsc.snap-sync`, and another `store_id` is refused with
`native store identity mismatch`. A first start stays in BOOTSTRAP, so the
log line `Extractor lifecycle is live` needs a real sync and is not part of
the smoke. The image entry point is `geth`, so the start and the controls
run their shell scripts through the `entrypoint` option of the owned command
boundary (`native.py`).

```sh
python3 -I test/harness/bsc_capture_build/bsc_capture_build.py smoke \
  --image bc-indexer/bsc-capture:1.8.2-capture --output "$SMOKE" --seconds 20
```

Portable tests of the procedure run without Docker:

```sh
python3 -B -m unittest discover -s test/harness/bsc_capture_build
```

## Later variants

The base build takes no variant input. Two later variants are already
parameters of `build`; their content is not in this repository yet.

An overlay on top of patches 01 to 08, for the in-node collector:

- `--extra-package SOURCE=TARGET` installs a file or a directory at a new
  path of the source tree. It never replaces a native or an installed file.
- `--extra-patch PATH` applies a unified diff after patches 01 to 08 with the
  same strict applier. An overlay patch can also change what the packages
  installed.

A C library that the binary links, for RocksDB 10.4.2:

- `--cgo-include DIR` and `--cgo-library DIR` are absolute directories with
  headers and with libraries. The build mounts them read-only into the
  compiler container and passes them as `CGO_CFLAGS=-I...` and
  `CGO_LDFLAGS=-L...`.
- `--cgo-link FLAG` adds a linker flag, such as `-lrocksdb`.
- `--runtime-library FILE` is a shared library the image needs in
  `/usr/local/lib`. A statically linked library needs none.

Both need `--variant NAME`. The tag becomes
`bc-indexer/bsc-capture:1.8.2-capture-NAME`, and provenance.json and the
labels `io.extractor.variant`, `io.extractor.overlay-*`, `io.extractor.cgo-*`
and `io.extractor.runtime-libraries` record the inputs by digest. `ldd`
then proves that the runtime image holds every shared library of the
binary.

The parameters were exercised once with a throwaway overlay that is not in
this repository: a patch, a package with one cgo file, a header directory,
a shared library and its link flag. The build linked the library,
`ldd` found it in the runtime image, and `geth version` and
`geth dumpconfig` ran there. The same variant without
`--runtime-library` was refused and its tag removed.

What each variant still needs: the packages and patches of the collector;
and for RocksDB, an installation prefix built from its source with the build
dependencies of the candidate lock, the overlay that uses it, and a choice
between static and dynamic linking. The build compiles with the accepted
`go.mod` copy and the offline module cache (`-mod=readonly`), so a variant
that adds a Go module needs an extended module input first.

## Run on Chapel for capture

Capture is switched on by the environment variable `EXTRACTOR_BSC_CONFIG`,
which names a JSON file with `store_id` (32 lowercase hex characters),
`producer_id`, `run_id`, `native_commit` (the upstream BSC commit) and
`genesis_file` (the genesis JSON that built the home; see
`adapters/bsc/lifecycle/native/go/node.go`). Without the variable the binary
is the plain node. `$DATA` is the native home and `$CONFIG` holds the config
and the genesis file; both are bind-mounted at the paths of
`.project/docs/DEPLOYMENT_BOOTSTRAP_CONTRACT.md` (`/data/node/client` and
`/config`).

```sh
mkdir -p "$CONFIG/bootstrap/bsc"
docker run --rm bc-indexer/bsc-capture:1.8.2-capture --chapel dumpgenesis \
  > "$CONFIG/bootstrap/bsc/genesis.json"
```

Save this as `$CONFIG/extractor-bsc.json`:

```json
{
  "store_id": "0123456789abcdef0123456789abcdef",
  "producer_id": "bsc-chapel",
  "run_id": "run-1",
  "native_commit": "54afaff4da31ac9e49ba646a4f65104edac5a2f3",
  "genesis_file": "/config/bootstrap/bsc/genesis.json"
}
```

Init once, with the explicit hash scheme: an empty BSC store selects the
path scheme, which the capture profile refuses, and a later
`--state.scheme hash` does not convert it (`incompatible state scheme`).

```sh
docker run --rm \
  --mount type=bind,source="$DATA",target=/data/node/client \
  --mount type=bind,source="$CONFIG",target=/config,readonly \
  bc-indexer/bsc-capture:1.8.2-capture \
  --chapel --datadir /data/node/client --state.scheme hash init /config/bootstrap/bsc/genesis.json
```

Run with the capture profile:

```sh
docker run -d --name bsc-capture-chapel \
  --mount type=bind,source="$DATA",target=/data/node/client \
  --mount type=bind,source="$CONFIG",target=/config,readonly \
  --env EXTRACTOR_BSC_CONFIG=/config/extractor-bsc.json \
  bc-indexer/bsc-capture:1.8.2-capture \
  --chapel --datadir /data/node/client --state.scheme hash --gcmode full --syncmode full
```

The profile is `--state.scheme hash --gcmode full` and, for BSC, no
`--pruneancient` and no nonzero `--history.blocks` (`adapters/README.md`).
The node needs its peers to sync: allow or publish the P2P port of
NETWORK_PORTS (30311 for `bsc-node`) where the host rules permit it; the
command above publishes none. A first start runs BOOTSTRAP: it imports
natively and uncaptured until the initial sync ends, then logs
`Extractor lifecycle is live` and captures from that boundary. Keep
`store_id`, `producer_id`, `run_id` and the genesis with the home: another
binding is a foreign store.

## Full sync, and a data directory of a vanilla node

`.project/docs/DEPLOYMENT_BOOTSTRAP_CONTRACT.md` lists
`--state.scheme hash --gcmode full` for Geth and BSC; `syncmode="full"`
stands there for Bor. The BSC capture node is stricter than that line:
BSC selects snap sync by default, and the capture node refuses it.

- `CheckProfile` in `adapters/bsc/lifecycle/native/go/node.go` returns
  `sync mode snap (bsc.snap-sync)` for every sync mode but full, before the
  store is opened. Without `--syncmode full` a capture node stops with
  `Failed to register the Ethereum service`.
- The reason is patch 08: it refuses the writers a snap sync needs
  (`SnapSyncStart`, `SnapSyncComplete`, `InsertReceiptChain`,
  `InsertHeaderChain`, `InsertHeadersBeforeCutoff` in `core/blockchain.go`)
  as the caller `bsc.snap-sync`, in every phase of the gate
  (`Gate.Enter` in `adapters/bsc/lifecycle/lifecycle.go` returns `ErrRefused`
  for a refused caller without looking at the phase).

The rule is about the start configuration, not about the history of the data
directory. Nothing in the capture path needs a locally executed history:

- A first start is BOOTSTRAP. Its passes do not observe (`Pass.Observe`), so
  imports run natively and uncaptured.
- The recovery drivers (`recover` in
  `adapters/bsc/lifecycle/native/go/recover.go` and `Hook.Recover` in
  `adapters/bsc/decision/native/go/recover.go`) read extractor records only.
  A directory of a vanilla node has none.
- H is the native head when `eth.Synced()` turns true; `GoLive` flushes that
  head's state root and writes the view.

So the capture node can open a directory that a vanilla node snap-synced,
with `--syncmode full`, when that directory holds the same genesis, the hash
state scheme and no pruned block data. The node runs a full sync of the
remaining gap uncaptured and goes live at the tip. Blocks before H are
never captured, whichever way the directory was synced.

This follows from the code. The capture node was not run on a directory
that a snap sync produced, and no directory of another node was opened.
Reuse a directory with one writer only: stop the vanilla node first.
