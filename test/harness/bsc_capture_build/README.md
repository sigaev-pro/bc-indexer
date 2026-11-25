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
6. It runs `version` and `dumpconfig` in the new image, in owned offline
   containers, and refuses a version or an effective config that differs
   from the pins and the capture profile.

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
the smoke.

```sh
python3 -I test/harness/bsc_capture_build/bsc_capture_build.py smoke \
  --image bc-indexer/bsc-capture:1.8.2-capture --output "$SMOKE" --seconds 20
```

Portable tests of the procedure run without Docker:

```sh
python3 -B -m unittest discover -s test/harness/bsc_capture_build
```

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
docker run --rm bc-indexer/bsc-capture:1.8.2-capture --chapel dumpgenesis > "$CONFIG/genesis.json"
```

```json
{
  "store_id": "0123456789abcdef0123456789abcdef",
  "producer_id": "bsc-chapel",
  "run_id": "run-1",
  "native_commit": "54afaff4da31ac9e49ba646a4f65104edac5a2f3",
  "genesis_file": "/config/bootstrap/bsc/genesis.json"
}
```

Save the JSON as `$CONFIG/extractor-bsc.json`. Init once, with the explicit
hash scheme: an empty BSC store selects the path scheme, which the capture
profile refuses.

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
The profile requires full sync: snap sync is refused. `CheckProfile` in
`adapters/bsc/lifecycle/native/go/node.go` returns an error for every sync
mode but full (`bsc.snap-sync`), and `adapters/bsc/lifecycle/native/README.md`
lists `bsc.snap-sync` as a refused caller. BSC selects snap sync by default,
so `--syncmode full` is mandatory on a capture node; without it the node
stops with `Failed to register the Ethereum service`.

A first start runs BOOTSTRAP: it imports natively and uncaptured until the
initial sync ends, then logs `Extractor lifecycle is live` and captures from
that boundary. Keep `store_id`, `producer_id`, `run_id` and the genesis with
the home: another binding is a foreign store, and the store keeps its
identity.
