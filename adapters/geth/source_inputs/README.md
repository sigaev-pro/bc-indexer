# Source inputs core (E01-02.inputs, block 0)

`adapters/<client>/source_inputs` keeps the inputs of one native observation
for REQ-CAP-04: a sealed static input capsule and an append-only log of
consumed dependency responses. It implements the input-capsule rules of
[NATIVE_CAPTURE_PROTOCOL](../../../.project/docs/NATIVE_CAPTURE_PROTOCOL.md)
("Admission и source pin").

This directory is shared. `inputs.go`, `inputs_test.go`, this README and the
mutant table are byte-identical for Geth (`geth`), BSC (`bsc`) and Bor
(`bor`); `python3 test/harness/source_inputs.py check` and the portable test
`test/harness/test_source_inputs.py` (run by `bootstrap/ci`) refuse any
difference. The package imports only the Go standard library and has no
`go.mod`, so the same bytes compile inside each accepted native module graph
and with a plain `go 1.25` module. Native wiring, the Bor Heimdall resolver and
the real EXEC_READY binding are later blocks.

## Design

### Store and durability model

The package writes through `Store` (`Has`, `Get`, `Put`, `SyncKeyValue`), a
subset of the native `ethdb.Database` of each client. A `Put` is durable only
after `SyncKeyValue` returns nil; before that, a power loss may keep any subset
of the unsynced writes. Every multi-write step therefore writes its data,
syncs, and only then writes one small commit record and syncs again. Data left
by an interrupted step is never read, because no commit record points to it,
and it is overwritten by the next attempt.

Keys are `extractor-source-inputs-v1/<observation>/...`. An observation ID and
an item name match `^[0-9a-z][0-9a-z._-]{0,127}$` and cannot contain `/`, so the
keys of two observations never collide. No key is 32 or 33 bytes long, so the
native offline pruner (trie node and code keys) never deletes one.

| Key suffix | Value |
|---|---|
| `/capsule` | Capsule manifest (JSON): observation, items, digest. Commit point of the seal |
| `/capsule/<item>/<chunk>` | Item data chunk |
| `/log/<sequence>` | Entry (JSON): sequence, request SHA-256 and length, response SHA-256 and length |
| `/log/<sequence>/<chunk>` | Response data chunk |
| `/frontier` | Durable frontier (JSON): entry count and chain value. Commit point of a response |
| `/closed` | Final consumed manifest (JSON). Written by `Finish` |

`<item>`, `<sequence>` and `<chunk>` are 16 lowercase hex digits. Data is
stored in chunks of at most `ChunkBytes` (65,536); every chunk except the last
is full. A read checks the whole SHA-256, which also covers every chunk
size; a damaged or missing chunk is `ErrCorrupt`.

### Digests

All digests are SHA-256, written as 64 lowercase hex digits. `u64` is 8 bytes
big-endian; `tag` is the ASCII tag followed by one zero byte; `H(x)` is the
32-byte SHA-256 of `x`.

- Capsule digest: `H(tag("extractor-source-inputs-capsule-v1") || u64(n) ||`
  for each item in order `u64(len(name)) || name || u64(len(data)) || H(data))`.
- Entry digest: `H(tag("extractor-source-inputs-entry-v1") || u64(sequence) ||
  H(request) || u64(len(request)) || H(response) || u64(len(response)))`.
- Chain: `chain_0 = H(tag("extractor-source-inputs-chain-v1") || capsule)`,
  `chain_{i+1} = H(chain_i || entry_i)`.
- Consumed-manifest digest: `H(tag("extractor-source-inputs-consumed-v1") ||
  capsule || u64(count) || chain_count)`.

The chain binds the log to its capsule and to the exact order of requests and
responses without holding the whole log in memory. The request identity is
the SHA-256 and length of the exact request bytes. The response bytes are the
raw bytes the caller passes in; for Bor these are the raw Heimdall response
body at the transport boundary, saved before decoding (D98).

### Static capsule

`SealCapsule(db, id, items)` writes the named static inputs (for example the
header and body encodings, parent root and hash, chain config and fork digest,
native commit, normalization profile, known ancestor headers, BSC snapshot
dependencies) in the given order: all chunks, sync, then the manifest, sync.
Sealing the same items again verifies the stored chunks and returns the same
manifest; different items, or the same items in another order, are
`ErrCapsuleConflict`. `ReadCapsule` and `ReadItem` check the manifest digest and
the data. Which items a client stores is decided by its own block.

### Dependency log

`OpenLog(db, id)` needs a sealed capsule. It recomputes the chain over every
durable entry and refuses a frontier, entry or closed manifest that does not
match (`ErrCorrupt`). Each attempt starts at the first logical request of the
observation with its cursor at 0. `Next(request, fetch)`:

1. **Replay.** Below the durable frontier, the request digest must equal the
   saved one. Then `Next` returns the saved response bytes after checking them
   and never calls `fetch`, so a retry never sees a fresh response.
2. **Prefix conflict.** A different request at a durable offset is
   `ErrPrefixConflict`. Nothing is written, the saved entry stays, and the log
   stops; a reopened log still replays the saved prefix.
3. **Tail append.** At the frontier, `Next` calls `fetch` once, then writes the
   response chunks and the entry, syncs, writes the advanced frontier, syncs,
   and only then returns the bytes (a copy that the caller's buffer cannot
   change). A `fetch` error writes nothing and leaves the log usable; this is
   the explicit source error of the protocol, with no skipped request.
4. **Failure.** Any store error stops the log (`ErrFailed` afterwards); the
   caller reopens it.

Crash points of one response:

| Power loss | Durable state after reopen | Retry |
|---|---|---|
| Before the first sync | Frontier unchanged; partial chunks or entry may exist | Fetches again; the orphan data is overwritten |
| After the first sync, before the frontier sync | Complete orphan entry beyond the frontier (or, if the frontier write survives, the new frontier) | Fetches again, or replays the saved response |
| After the frontier sync, before the hand-off | Frontier includes the response | Replays the saved bytes; a changed external response is never used (AC-CAP-04-01) |

So the frontier is the durable consumed frontier: nothing beyond it was handed
to native code, and everything at or below it is replayed from saved bytes.

### Final consumed manifest

`Finish()` runs after a completed execution. The attempt must have consumed
every durable entry (`ErrUnconsumed` otherwise: an unconsumed durable response
means the retry diverged from an earlier attempt). It writes the `/closed`
record with the consumed-manifest digest and syncs. After that, `Next` still
replays the saved prefix (for native-only replay) but refuses a new response
(`ErrClosed`), and `Finish` returns the same manifest. The EXEC_READY marker
references this digest; in this block a test-only stand-in marker binds it
(AC-CAP-04-03, D98), and the real binding belongs to E01-02.admission.

### Bounds and concurrency

A log is not safe for concurrent use: the caller holds the native observation
guard, so requests arrive in native order. Opening a log reads every entry
record (not the response data). One response or item is held in memory once,
as the caller already holds it; the store sees values of at most `ChunkBytes`.

## Checks

`go vet` and `go test -race -cover` in a copy with a generated `go 1.25`
module; `test/mutate.py source_inputs.mutants.json` for the mutants. The
expected digests in `inputs_test.go` come from
`python3 test/harness/source_inputs.py vectors`, an independent Python
implementation of the formulas above. These are unit tests on an in-memory
power-loss store model; native evidence for each client is a later block.
