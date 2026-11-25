# RocksDB journal storage

`core/journal/storage/rocksdb` is the durable implementation of the journal
storage seam (`core/journal/storage`). It runs the producer journal
(`core/journal/producer`) and the Gateway journal (`gateway/journal`) on the
engine that decision D33 accepted for the local WAL and the Gateway journal:
RocksDB 10.4.2 through the Go binding `github.com/linxGnu/grocksdb` v1.10.2.
COMPONENT_STACK and research/ROCKSDB_STACK in `.project/docs` hold the pins.

## Why it is a separate module

The binding needs CGO and a native `librocksdb`. Keeping that here leaves
`core/journal/storage`, `core/journal/producer` and `gateway/journal` free of
it, so they build and test on a machine without the engine. The producer
journal runs inside the patched node process, so a capture node build needs
CGO and this library at build time.

The documents name the runtime directories but not the place of this source
code, so it sits next to the seam it implements.

## Where the store lives

`Open` does not choose the directory. LIVE_CONFIG_SPEC names one per role:
`storage.live_dir` for the producer journal (deployment default `/data/wal`)
and `storage.gateway_dir` for the Gateway journal (default `/data/journal`).
The native node data directory, `live_dir` and `gateway_dir` must not be the
same directory or nested in one another, and each network has its own.

`Open` uses the path as given. Resolving a relative path against the
configuration file is the caller's job (LIVE_CONFIG_SPEC, D78). A symbolic
link to the store directory opens like the directory itself.

## API

```go
import (
    "github.com/sigaev-pro/bc-indexer/core/journal/storage/rocksdb"
)

st, err := rocksdb.Open(dir, rocksdb.Options{}) // storage.Storage
if err != nil {
    // ErrForeign, ErrIncompatible, ErrLocked or ErrOpen. Nothing was
    // reinitialized; see "What Open refuses".
}
defer rocksdb.Close(st)
```

- `Open(dir string, opts Options) (storage.Storage, error)` opens the store in
  `dir`, or creates it when `dir` is missing or empty.
- `Options` has one field, `Sync`. Its zero value is `SyncAlways`.
- `Close(st storage.Storage) error` closes a store. The seam interface has no
  `Close`, so callers that hold the interface use this function.

## Guarantees

- **The write-ahead log is always on.** No option turns it off.
- **Writes are synced.** With `SyncAlways`, `Put`, `Delete` and `Apply`
  return only after the engine reports the write synced. `SyncNever` skips
  the sync: a returned write survives a process crash but not a power loss.
  Use it for measurements and throwaway data only.
- **A batch is all or nothing.** `Apply` is one engine `WriteBatch`. After a
  restart every operation of a batch is durable or none is. A batch over
  `storage.MaxBatchBytes` is refused before the engine is touched.
- **One serialized writer.** The store serializes its writers, and the
  engine's directory lock makes the open exclusive: a second writer gets an
  error that wraps `ErrLocked`.
- **Every error is returned.** No write, sync or iterator error is logged and
  ignored.
- **No repair that drops records.** The engine's repair command is never
  used. A store the engine cannot open stays an explicit error.
- **Ordered iteration.** `Iterate` walks the half-open range `[lo, hi)` in
  unsigned byte order, exactly like the memory store, over the snapshot the
  engine takes when the walk starts. The key and value given to the visit
  function are copies.
- **A torn tail is dropped, earlier records stay.** The engine's default log
  recovery (`kTolerateCorruptedTailRecords`) stops at a record that a crash
  tore at the end of the log and keeps every record before it. A record is
  never half there.

## What `Open` refuses

`Open` never reinitializes a directory: it never asks the engine to create a
database in a directory that holds one, and it never starts the engine on a
directory without a valid marker.

| Directory | Error |
|---|---|
| Missing or empty | None: the store is created |
| Not empty, no `STORE_FORMAT` file (another program's data, an engine database that is not ours) | `ErrForeign` |
| A marker of another store format, another store, or unreadable | `ErrIncompatible` |
| Database files without the engine's `CURRENT` file | `ErrIncompatible` |
| Open in another process or store object | `ErrLocked` |
| A store of this format that the engine cannot open | `ErrOpen` |

The first four refusals write nothing at all. The last two come from the
engine: the locked one writes nothing, and for a damaged store the engine may
append to its own informational log (`LOG`) while it fails to open. The log
holds no data.

`ErrOpen` is kept apart from `ErrIncompatible` on purpose. The store is ours
and of this format, so a caller maps it to a disk error and not to an
incompatible state. LIVE_CONFIG_SPEC gives the two different exit codes
(5 for an unrecoverable runtime or disk error, 3 for an occupied or
incompatible state).

There is no automatic migration. A build refuses a store of another format
before any write, as DELIVERY_STATE_MACHINE requires.

An interrupted first creation needs no repair. `Open` writes the marker
first, then lets the engine create the database. If the process dies in
between, the next `Open` finishes the creation, because no record was ever
written to that directory.

## Store directory

`Open` writes one file of its own, `STORE_FORMAT`:

```
bc-indexer journal store
format_version=1
engine=rocksdb
engine_version=10.4.2
```

`format_version` is the layout version of the store directory. A different
engine version is information for operators, not a refusal: engine file
formats are compatible across engine versions. Every other file belongs to
the engine (`CURRENT`, `IDENTITY`, `LOCK`, `LOG`, `MANIFEST-*`, `OPTIONS-*`,
the write-ahead logs and the table files). Do not edit or delete them by
hand.

## Building the engine library

`build-librocksdb.sh` builds the pinned static library from the upstream
source. It installs nothing system-wide. It needs `cmake`, `make`, a C++
compiler, `curl` and `tar`, and the compression libraries `snappy`, `lz4`,
`zlib` and `zstd` (for example `brew install snappy lz4 zstd` on macOS, or
`apt-get install libsnappy-dev zlib1g-dev liblz4-dev libzstd-dev` on Debian
or Ubuntu).

From the repository root:

```
core/journal/storage/rocksdb/build-librocksdb.sh --jobs 5
```

The script downloads the source archive of one commit, checks its SHA-256
against the value stored in the script, builds, and installs into
`.build/rocksdb-10.4.2/prefix` at the repository root (a directory that
`.gitignore` already ignores):

- `lib/librocksdb.a`, the static library;
- `include/rocksdb`, the headers;
- `librocksdb.env`, the CGO flags to load before `go build` or `go test`.

`--prefix DIR` installs somewhere else (give an absolute path) and
`--jobs N` sets the number of compile jobs (half the CPUs by default). Set
`LIBROCKSDB_DEPS_PREFIX` when the compression libraries were built into a
local prefix. The script refuses to start when its work directories are not
empty, so run it once in a row and delete `.build/rocksdb-10.4.2/src` and
`.build/rocksdb-10.4.2/build` before another run.

The library carries no processor-specific code (`PORTABLE=1`), so it also
runs on another x86-64 or ARM64 host than the one that built it.

On one development machine (Intel macOS) the build took 11 minutes 7 seconds
with 5 jobs on a loaded machine, and 3 minutes 33 seconds with 6 jobs on an
idle one.

The SHA-256 of the library differs from build to build: the engine records
its compile time in one object file. In two builds from the pinned archive
the other 346 of 347 object files were byte-identical. The pin is the
SHA-256 of the source archive.

## Building and testing the adapter

The adapter builds only with the library present. From the repository root:

```
. .build/rocksdb-10.4.2/prefix/librocksdb.env
cd core/journal/storage/rocksdb
go build ./...
go vet ./...
go test -count=1 -race ./...
```

The two lines of `librocksdb.env` are the whole requirement:

```
export CGO_CFLAGS="-I<prefix>/include"
export CGO_LDFLAGS="-L<prefix>/lib"
```

The binding names the libraries itself, so no other flag is needed.

## Tests

- `TestConformance` runs the shared suite of `core/journal/storage/storagetest`
  against the real engine. The memory store runs the same suite.
- `TestOpenCreatesDirectoryAndReopens`, `TestReopenRestoresWritesBatchesAndDeletes`
  and `TestBatchIsWholeAfterRestart` cover the restart: what was written
  comes back byte for byte, a batch is whole or absent, and a refused batch
  left nothing.
- `TestReopenAfterFlushToTableFiles` does the same for a store past the
  engine's 64 MiB write buffer, so part of the data is in table files and
  part in the write-ahead log. The other restart tests are small enough to
  be recovered from the log alone.
- `TestSurvivesProcessKill` kills a helper process with SIGKILL while it
  commits batches. Every batch that had returned is whole afterwards, no
  batch is partly there, and the directory lock is released by the kill. It
  runs for both sync modes.
- `TestExclusiveOpenRefusesSecondWriter` covers the exclusive open.
- `TestForeignDirectoryIsRefused`, `TestIncompatibleStoreIsRefused`,
  `TestDamagedDatabaseIsRefusedNotReinitialized` and
  `TestInterruptedFirstCreation` check that a refused directory is left
  exactly as it was.
- `TestProducerJournalRestartKeepsIdenticalState` drives the real producer
  journal to SEALED, STAGING and INTENT, closes the store, opens it again,
  and compares the state and the stored bytes. It then continues the
  protocol from that state. `core/journal/producer` is a test-only
  dependency of this module.
- `TestConcurrentReadersAndWriters`, `TestCloseWaitsForAnIterationThatWrites`
  and `TestCloseCalledTwiceConcurrentlyReturns` run under the race detector.
- `TestOpenAcceptsSymlinkedDirectory` covers a store directory reached
  through a symbolic link.

## Limits

- A power loss is not simulated. `SyncAlways` relies on the engine's synced
  writes; the tests prove durability across a process crash.
- The tests ran on Intel macOS only. Linux is the deployment target, and the
  adapter uses only the binding's portable calls, but the same tests have not
  run there.
- The locked state is known by the text of the engine's error, because the
  binding returns every engine status as a plain error.
- Engine tuning (memory, compaction, compression) is left at the engine
  defaults. research/ROCKSDB_STACK leaves it to a later benchmark.
