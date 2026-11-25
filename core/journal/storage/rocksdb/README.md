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

`Open` takes the path as given. Resolving a relative path against the
configuration file is the caller's job (LIVE_CONFIG_SPEC, D78). The rule that
`live_dir` and `gateway_dir` are not the same directory and not nested in one
another, with symbolic links resolved, is enforced by the configuration check
(`config/validate.go`, `checkStorageSeparation`). The native node's data
directory is compared the same way where the state is opened. `Open` compares
nothing: it is handed one directory and opens it.

## API

```go
import (
    "github.com/sigaev-pro/bc-indexer/core/journal/storage/rocksdb"
)

st, err := rocksdb.Open(dir, rocksdb.Options{}) // storage.Storage
if err != nil {
    // See "What Open refuses".
}
defer rocksdb.Close(st)
```

- `Open(dir string, opts Options) (storage.Storage, error)` opens the store in
  `dir`, or creates it when `dir` is missing or empty.
- `Options` has no exported field. A caller can write only `Options{}`, and
  that value syncs every write. The unsynced mode exists for the tests of
  this package and cannot be reached from another package: the seam contract
  promises a durable write, and a caller must not be able to build a store
  that breaks it.
- `Close(st storage.Storage) error` closes a store. The seam interface has no
  `Close`, so callers that hold the interface use this function.

Errors of `Open`: `ErrForeign`, `ErrIncompatible`, `ErrLocked` and `ErrOpen`
(the table below says which is which). `Open` also returns a plain error with
no sentinel of this package for an empty path, a path that cannot be resolved
or read, and a directory, lock file or marker that cannot be created.
`ErrClosed` is returned by every operation after `Close`.

`Close` always returns nil. The binding does not report the engine's close
status, so a failure inside the engine at close cannot be told here.

## Guarantees

- **Every write is synced.** `Put`, `Delete` and `Apply` return only after the
  engine reports the write synced. The write-ahead log is always on.
- **An applied record is never rolled back.** Open sets the engine's log
  recovery mode to `kTolerateCorruptedTailRecords`. The engine's default,
  point-in-time recovery, stops at the first damage in the log and opens the
  store without the records after it, and without an error. The rule, as the
  engine implements it (db_impl_open.cc, the status handling of
  `RecoverLogFiles`):
  - a record that a crash left incomplete at the end of the log is not
    reported as damage. It is dropped, and every record before it stays;
  - any reported damage, in the middle of the log or at its end, fails the
    open with `ErrOpen`. Nothing is dropped and nothing is repaired.

  This is REC-T11 (corruption is an explicit error, never a silent skip) and
  research/ROCKSDB_STACK (automatic repair that drops records is not
  recovery).
- **A batch is all or nothing.** `Apply` is one engine `WriteBatch`. After a
  restart every operation of a batch is durable or none is. A batch over
  `storage.MaxBatchBytes` is refused before the engine is touched.
- **One writer.** Writes are serialized inside the store. The open is
  exclusive through a lock this package takes before the engine starts: a
  second writer, in this process or another and under any spelling of the
  path, gets `ErrLocked`.
- **Every error is returned.** No write, sync or iterator error is logged and
  ignored. A walk over a damaged table file returns an error; it never ends
  quietly in the middle.
- **No repair that drops records.** The engine's repair command is never
  used. A store the engine cannot open stays an explicit error.
- **Ordered iteration.** `Iterate` walks the half-open range `[lo, hi)` in
  unsigned byte order, exactly like the memory store, over the snapshot the
  engine takes when the walk starts. The key and value given to the visit
  function are copies.
- **LZ4 everywhere.** Compression and bottommost compression are both LZ4, as
  research/ROCKSDB_STACK fixes. The engine stores the codec with each block,
  so the first durable byte fixes it.

## What `Open` refuses

`Open` never reinitializes a directory. It never asks the engine to create a
database in a directory that holds one, and it never starts the engine on a
directory without a valid marker.

| Directory | Error | What the refusal changes |
|---|---|---|
| Missing or empty | None: the store is created | |
| Not empty, no `STORE_FORMAT` file (another program's data, an engine database that is not ours) | `ErrForeign` | Nothing |
| A marker of another store format, another store, or unreadable | `ErrIncompatible` | Nothing |
| Database files without the engine's `CURRENT` file | `ErrIncompatible` | Nothing |
| Open in another process or store object | `ErrLocked` | Nothing |
| A store of this format that the engine cannot open: a damaged log that is not an incomplete last record, a damaged database | `ErrOpen` | The engine may append to its own informational log |

The first four refusals are decided by this package before the engine
starts, and the lock is taken before the engine starts too. So only the last
row reaches the engine, and `LOG` is the only file it can write. `LOG` holds
no data.

`ErrOpen` is kept apart from `ErrIncompatible` on purpose. The store is ours
and of this format, so a caller maps it to a disk error and not to an
incompatible state. LIVE_CONFIG_SPEC gives the two different exit codes (5
for an unrecoverable runtime or disk error, 3 for an occupied or
incompatible state).

There is no automatic migration. A build refuses a store of another format
before any write, as DELIVERY_STATE_MACHINE requires.

### An interrupted first creation

`Open` creates a store in steps: the lock file, the marker, then the engine's
database. A crash can stop it between any two. The next `Open` finishes the
creation from each of these states:

- the lock file alone, or with the marker still being written
  (`STORE_FORMAT.tmp`);
- the marker with the engine's own files that hold no data: `LOCK`,
  `IDENTITY`, `LOG`, `LOG.old.*`, `OPTIONS-*`, and `archive/` as long as it
  holds nothing but `LOG.old.*` files. The engine moves an old `LOG` into
  `archive/` before it takes its lock, so a creation that was refused or
  interrupted can leave one there.

When writing the marker fails, `Open` removes the temporary file itself.

Two states are refused with `ErrIncompatible`, and `Open` does not choose
between their causes:

- database files without `CURRENT` (a manifest, a log, a table file). Either
  the engine was interrupted between creating its first manifest and writing
  `CURRENT`, or somebody removed `CURRENT` from a real store. In the first
  case no write was ever accepted, because no write is accepted before
  `CURRENT` exists;
- an `archive/` that holds anything but `LOG.old.*` files.

Remove such a directory by hand only after checking what it holds.

## Store directory

`Open` writes two files of its own. `STORE_FORMAT` is the marker:

```
bc-indexer journal store
format_version=1
engine=rocksdb
```

`STORE_LOCK` carries the exclusive lock of the writer while the store is
open. It is not the engine's `LOCK` file: the engine's lock is released for
the whole process when any descriptor of that file closes, so a refused open
would release the lock of the writer that holds it.

`format_version` is the layout version of the store directory. A build
refuses a store of another version. The marker does not repeat the engine's
version. The binding has no call that reports the linked one, and the engine
writes it into every store itself: the first line of `LOG` and
`rocksdb_version` in the `OPTIONS-*` file.

Every other file belongs to the engine. Do not edit or delete them by hand.

## Building the engine library

`build-librocksdb.sh` builds the pinned static library from the upstream
source. It installs nothing system-wide. It needs `cmake`, `make`, a C++
compiler, `curl` and `tar`, and the compression libraries `snappy`, `lz4`,
`zlib` and `zstd` (for example `brew install snappy lz4 zstd` on macOS, or
`apt-get install libsnappy-dev zlib1g-dev liblz4-dev libzstd-dev` on Debian
or Ubuntu). The script refuses to build when one of their headers is
missing.

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
- `librocksdb.lock`, the resolved compression libraries;
- `librocksdb.env`, the CGO flags to load before `go build` or `go test`.

`--prefix DIR` installs somewhere else (an absolute path) and `--jobs N` sets
the number of compile jobs (half the CPUs by default). The script refuses to
start when its work directories are not empty; delete
`.build/rocksdb-10.4.2/src` and `.build/rocksdb-10.4.2/build` before another
run. Set `LIBROCKSDB_DEPS_PREFIX` (prefix paths separated by semicolons, each
with `include/` and `lib/`) when the compression libraries were built into a
local prefix.

The library carries no processor-specific code (`PORTABLE=1`), so it also
runs on another x86-64 or ARM64 host than the one that built it.

### Dependency lock

The script does not build the compression libraries. It uses the ones on the
build machine and records what it used, because research/ROCKSDB_STACK
requires resolved dependency locks. `librocksdb.lock` holds, for each
library, the version its header declares and the SHA-256 of the library file
the linker resolves. The script prints the same lines.

Where the libraries are shared, as on macOS with Homebrew, the test binary
and any binary that links the adapter depend on those files at run time. The
lock records; it does not force the same files on the next build. Compare the
lock files to see that two builds used the same libraries.

The lock of the toolchain this branch was tested with (Intel macOS, Homebrew;
the paths and digests belong to that machine):

```
snappy version=1.2.2 library=/usr/local/lib/libsnappy.dylib sha256=327962f26cd42999e2a3ba9871acc58b8081bdffca9aa9fdbda0a046b47cef6e
lz4 version=1.11.1 library=/usr/local/lib/liblz4.dylib sha256=93cb3a6785be795179a2f129c2bb1140a746b92c855d75c6f71d7dbd717d16f7
zlib version=1.2.12 library=/Library/Developer/CommandLineTools/SDKs/MacOSX.sdk/usr/lib/libz.tbd sha256=134edaa6559929b481e2fb125dff204c28162ebd647c6997a5be8b00984a311f
zstd version=1.5.7 library=/usr/local/lib/libzstd.dylib sha256=fb01d38cf980e9423cdf69d04affb2d6b7985e029e4f9d8883833df732dfe3da
```

On macOS zlib resolves to the stub file of the SDK, not to a Homebrew
library. The binding's own build script compiles lz4 1.10.0; this build
linked 1.11.1, whose header marks one function the engine still calls as
deprecated.

On that machine the compile took 3 minutes 33 seconds with 6 jobs on an
idle host, 11 minutes 7 seconds with 5 jobs on a loaded one, and 22 minutes
with 6 jobs while the host ran other work at a load average near 150.

The SHA-256 of the library differs from build to build: the engine records
its compile time in one object file. In three builds from the pinned
archive the other 346 of 347 object files were byte-identical. The pin is
the SHA-256 of the source archive.

## Building and testing the adapter

The adapter builds only with the library present. From the repository root:

```
. .build/rocksdb-10.4.2/prefix/librocksdb.env
cd core/journal/storage/rocksdb
go build ./...
go vet ./...
go test -count=1 -race ./...
```

`librocksdb.env` holds the include path of the library and its library path,
followed by the library path of every `LIBROCKSDB_DEPS_PREFIX` entry. The
binding names the libraries itself (`-lrocksdb -lsnappy -llz4 -lzstd -lz`),
but the linker finds them only where they are in its default search path.
When they are not, the flags file carries the path; so always source it, and
always build with the same `LIBROCKSDB_DEPS_PREFIX` as the library.

## Tests

- `TestConformance` runs the shared suite of `core/journal/storage/storagetest`
  against the real engine. The memory store runs the same suite.
- `TestOpenCreatesDirectoryAndReopens`, `TestReopenRestoresWritesBatchesAndDeletes`,
  `TestBatchIsWholeAfterRestart` and `TestReopenAfterFlushToTableFiles` cover
  the restart: what was written comes back byte for byte, also from table
  files; a batch is whole or absent; a refused batch left nothing.
- `TestDamagedLogIsRefusedAndLeftAlone`, `TestIncompleteLastLogRecordIsDropped`
  and `TestDamagedLastLogRecordIsRefused` are the log recovery rule above.
- `TestSurvivesKillWhileCommittingBatches` kills a helper process with
  SIGKILL at a random point of a batch, in both sync modes. Every batch that
  had returned is whole after the kill, and no batch is partly there. Each
  kill is a subtest. The helper pauses inside each batch through a hook of
  `Apply`, so the kill also lands between two operations of one batch. A
  store that wrote a batch one operation at a time is caught by nearly
  every kill: 20 of 21 kills in three runs.
- `TestSurvivesKillDuringFlushAndCompaction` does the same while the engine
  flushes a full write buffer to a table file, and while it merges table
  files. The helper asks the engine which jobs are running, and kills itself
  while one is. Its keys are spread at random, because files of sequential
  keys are only moved to the next level, not merged.
- `TestExclusiveOpenInOneProcessUnderEverySpelling`,
  `TestExclusiveOpenAcrossProcesses` and `TestConcurrentOpenAllowsExactlyOne`
  cover the exclusive open.
- `TestRefusedOpenLeavesDirectoryAsItWas` takes a snapshot of the whole
  directory around every refusal and compares. `TestInterruptedFirstCreation`
  covers each state an interrupted creation can leave.
- `TestDamagedDatabaseIsRefusedNotReinitialized` and
  `TestNeverRecreatesOverAnExistingDatabase` cover a damaged database: the
  open fails and the data files keep their bytes. The second one stands in
  for `CURRENT` going missing between the look at the directory and the
  engine's open, the case where create-if-missing would build a fresh empty
  database over the store.
- `TestWriteErrorsAreReturned` makes the directory read-only and fills the
  write buffer. The engine cannot create its next log file; `Put`, `Delete`
  and `Apply` return that error, every write that returned is in the store
  after a restart, and the refused one is not.
- `TestIterateReportsADamagedTableBlock` damages one block of a table file:
  a walk returns an error instead of ending in the middle, and a read of a
  key in that block is an error.
- `TestEngineSettingsMatchTheBaseline` reads the `OPTIONS-*` file the engine
  wrote: the engine version, the log recovery mode, LZ4 for both
  compressions, and the default write buffer.
- `TestProducerJournalRestartKeepsIdenticalState` drives the real producer
  journal to SEALED, STAGING and INTENT, closes the store, opens it again,
  and compares the state and the stored bytes. It then continues the
  protocol from that state. `core/journal/producer` is a test-only
  dependency of this module.
- `TestConcurrentReadersAndWriters`, `TestCloseWaitsForAnIterationThatWrites`
  and `TestCloseCalledTwiceConcurrentlyReturns` run under the race detector.

Two hooks exist for these tests and are nil or zero outside them:
`betweenOps` lets a test pause inside a batch, and `testWriteBufferBytes`
shrinks the engine's write buffer, so that a flush, and after four flushes a
merge, start after a few MiB instead of 64 MiB.

## Limits

- **A power loss is not simulated.** The synced write relies on the engine's
  sync. The tests prove durability across a process crash.
- **Interruption points not covered by a kill.** The marker write and the
  engine's first creation are covered through the states they leave
  (`TestInterruptedFirstCreation`), not by killing a process at that moment.
  An interrupted `Close` is not a separate case: the engine flushes at
  shutdown only data that was written without the log, and this adapter
  never writes that way, so a kill during `Close` leaves what any kill
  leaves.
- **A synced write costs one sync.** On the development Mac one synced write
  took about 20 ms for a small value and 25 to 28 ms for 64 KiB, because
  macOS uses `F_FULLFSYNC`. Throughput is bytes per commit times about 40
  commits a second there: 2.6 MB/s when every 64 KiB chunk is one `Put`, and
  35 to 39 MB/s at batches near 1 MiB. A caller that writes many chunks
  should pack them into one `Apply`. Linux is expected to be far cheaper;
  it is not measured.
- **Linux is untested, and nothing tests it by itself.** The deployment
  target is Linux, and `.github/workflows/bootstrap.yml` runs no Go build or
  test at all. This needs a Go job that builds the library and runs these
  tests on Linux before the adapter is used there. It is a follow-up that
  still has to be put into the task catalog.
- **The locked state is known by the lock of this package.** Only when a
  writer does not take that lock does the engine's lock answer, and its
  error is plain text that this package reads (the two texts of
  `env/fs_posix.cc`).
- **Engine tuning** (memory, compaction, the sizes of caches) is left at the
  engine defaults. research/ROCKSDB_STACK leaves the sizes to a later
  benchmark.
- **Platforms without `flock`** refuse to open a store rather than open it
  without the exclusive lock.
