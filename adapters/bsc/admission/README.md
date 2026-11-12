# Admission core (E01-02.admission, block 0)

`adapters/<client>/admission` is the client-independent guard/pin/ADMITTED
seam of REQ-CAP-02 (AC-CAP-02-01, AC-CAP-02-02) for Geth (`geth`), BSC (`bsc`)
and Bor (`bor`). It records each native observation from INPUT_PREPARING to
ADMITTED and EXEC_READY, and it binds the EXEC_READY record to the final
consumed-input manifest of E01-02.inputs (D98 item 7). It implements the
"Admission и source pin" and "Durable состояния" rules of
[NATIVE_CAPTURE_PROTOCOL](../../../.project/docs/NATIVE_CAPTURE_PROTOCOL.md).
The package also holds the shared canonical JSON rule of D99 item 3.

This directory is shared. Every file outside `native/` is byte-identical for
Geth, BSC and Bor; `python3 test/harness/admission.py check` and the portable
test `test/harness/test_admission.py` (run by `bootstrap/ci`) refuse any
difference. `native/` is reserved for the per-client blocks (native patches
and import-path wiring) and is not compared. The package imports only the Go
standard library and has no `go.mod`, so the same bytes compile inside each
accepted native module graph and with a plain `go 1.25` module.

## What this block does not do

Native wiring, the retention and VM safe-point patches, the per-block
observations on the product import path, and every client-specific input
rule are later blocks; the plan is in PROJECT_STATUS ("E01-02.admission").
The native decision marker, NATIVE_DECIDED and the delivery states belong to
E01-02.decision_bridge (`adapters/<client>/decision`). This package adds
`Reject`, the NATIVE_REJECTED exit for an ADMITTED observation that native
validation refused, and, for the decision bridge, `Decide`, the close of an
EXEC_READY observation, with the deletion of the records of closed
observations (D100). The live
boundary H and the caller map belong to E01-02.lifecycle_seams. This block
runs no native client and gives no AC verdict.

## Design

### Seams it joins

The package does not import the pin or input packages. It reaches them
through two small interfaces, so the same bytes compile in every client:

- `Pins` (E01-02.pins, `adapters/<client>/state_provider`):
  `Verify(id, parentRoot)` checks that a durable pin exists, holds this parent
  root and is readable, and returns the SHA-256 of its ledger record;
  `Release(id)` durably removes it and returns nil for an absent pin. The pin
  ID of an observation is its observation key, so a pin can belong to only
  one observation. The native `state_provider` differs on purpose: its
  `Release` returns `ErrUnknownPin` for an absent pin and its `Verify` returns
  no digest, so each client block's pin adapter maps `ErrUnknownPin` to nil,
  returns the ledger-record digest, and tests an `Abandon` without a pin.
- `Inputs` (E01-02.inputs, `adapters/<client>/source_inputs`):
  `CapsuleDigest(key)` returns the digest of the sealed static capsule;
  `Consumed(key)` returns the durable closed consumed-input manifest
  (`Log.Closed` after `OpenLog`), or an error if the log is not closed.

The test-only glue `test/harness/fixtures/admission_core/binding.go` adapts
the real `source_inputs` package to `Inputs`, and its test runs both packages
together (see Checks). Each client block wires the native pin provider.

### Store and durability model

The same model as `source_inputs`: the store is `Has`, `Get`, `Put`,
`SyncKeyValue` of the native `ethdb.Database`; a `Put` is durable only after
`SyncKeyValue` returns nil; a power loss may keep any subset of unsynced
writes; after a process crash, unsynced writes may still be visible. `Open`
syncs before it reads, so nothing it trusts can be lost later. Every step
writes its data, syncs, and only then writes one commit record and syncs.

A producer is `(producer_id, run_id)`; each matches
`^[0-9a-z][0-9a-z-]{0,39}$`. The observation key is
`<producer_id>.<run_id>.<sequence>`, with the sequence as 16 lowercase hex
digits; it matches the ID pattern of `source_inputs` and of the pin ledger.
Keys start with `extractor-admission-v1/<producer_id>.<run_id>/`; every key is
longer than 33 bytes and starts with `e`, so the native offline pruner never
deletes one.

| Key suffix | Value (JSON, strict decode) |
|---|---|
| `/counter` | `{"next": n}`: sequences below `n` exist. Commit point of `Begin` |
| `/lowwater` | `{"sequence": n}`: every sequence below `n` is terminal and is not read |
| `/<seq>/preparing` | INPUT_PREPARING: observation, sequence, candidate block |
| `/<seq>/admitted` | ADMITTED: candidate, pin digest, capsule digest, admitted digest. Commit point of `Admit` |
| `/<seq>/exec-ready` | EXEC_READY: admitted digest, consumed digest, exec digest, record digest. Commit point of `ExecReady` |
| `/<seq>/abandoned` | Terminal cleanup of an INPUT_PREPARING observation |
| `/<seq>/rejected` | NATIVE_REJECTED: admitted digest, evidence digest, record digest. Terminal |
| `/<seq>/decided` | DECIDED: EXEC_READY digest, decision digest, record digest. Terminal |
| `/purged` | `{"sequence": n}`: the records of every sequence below `n` are deleted (with cleanup only) |

The candidate block is `{number, hash, parent_hash, parent_root}`; hashes
are 64 lowercase hex digits.

### States and steps

The caller holds the observation guard for every call (see Guard). Steps of
one observation, in protocol order:

1. `Begin(candidate)` (LIVE only): writes `/<n>/preparing`, syncs, writes
   `/counter` = n+1, syncs. The observation now exists in INPUT_PREPARING; it
   is not an accepted capture.
2. The caller pins the parent root under the observation key
   (`state_provider.Pin`: trie commit, sync, ledger record, sync) and seals
   the static capsule (`source_inputs.SealCapsule`). Both are durable when
   they return.
3. `Admit(key)` (LIVE only): `Pins.Verify(key, parent_root)` and
   `Inputs.CapsuleDigest(key)` must succeed; then it writes `/<n>/admitted`
   with both digests and syncs.
   This sync is the closing ADMITTED sync: the obligation is discoverable from
   here on, and no capture hook or `Process` may run before it returns.
4. Native execution consumes dependencies through the `source_inputs` log and
   closes it with `Log.Finish`.
5. `ExecReady(key, execDigest)` (LIVE or RECOVERY_ONLY): the pin must still
   verify with the admitted pin digest, the capsule digest must be unchanged,
   and `Inputs.Consumed(key)` must be closed for this key and this capsule,
   with a digest that the package recomputes from the capsule digest, count
   and chain. Only then it writes `/<n>/exec-ready` and syncs. `execDigest`
   is the digest of the immutable record corpus, which the capture side
   computes. The decision bridge references the EXEC_READY record digest.

Two steps close an observation before EXEC_READY; both work in either mode:

- `Abandon(key)` cleans an INPUT_PREPARING observation, which is not a
  capture.
- `Reject(key, evidenceDigest)` closes an ADMITTED observation that native
  validation refused (NATIVE_REJECTED, for example a failed state check after
  `Process`). `evidenceDigest` is the digest of the native rejection
  evidence. No EXEC_READY, no Applied and no public result follows.

One step closes an EXEC_READY observation, in either mode:

- `Decide(key, decisionDigest)` is called only by the decision bridge, after
  its own record that resolved the observation is durable (a NATIVE_DECIDED
  record, or the rejection of its transition intent). `decisionDigest` is
  the digest of that record. The child root and the decision are durable
  then, so the parent pin is no longer needed.

All three write the terminal record and sync, then call `Pins.Release(key)`,
then advance `/lowwater` past terminal sequences. The record comes first, so
an ADMITTED or EXEC_READY obligation keeps its pin until its closing record
is durable. After a power loss before that sync, the observation reopens in
its earlier state with its pin, and repeating the step completes it. After a
power loss or a `Release` error between the sync and the release, `Open`
releases the pin of every closed observation above `/lowwater` again
(`Release` of an absent pin returns nil) and only then advances `/lowwater`.
No other step releases a pin.

Records of closed observations (D100). An admission opened with
`OpenWithCleanup(t, db, producer, pins, inputs, cleanup)` also deletes
them: after the release, `cleanup.Purge(key)` deletes the capsule and the
dependency log (`source_inputs.Purge`; nil when nothing is left), and after
`/lowwater` passes a sequence, its records under this package are deleted in
batches of 256 sequences, each batch syncing its deletions before it advances
`/purged`. `Open` repeats the purge of every closed observation above
`/lowwater` and the deletion of the records in `[purged, lowwater)`, so a
power loss at any point only repeats work. An admission from `Open` deletes
nothing, as before; the client blocks switch to `OpenWithCleanup` before the
first product run.

Only this package writes keys under its prefix, and `/lowwater` is computed
from its own view of open observations. The decision bridge therefore closes
EXEC_READY observations through `Decide`, never by writing these keys
directly.

Repeating a completed `Admit` or `ExecReady` with the same arguments returns
the stored record; different arguments are `ErrConflict`. A closed
observation is no longer listed, so repeating `Abandon`, `Reject` or
`Decide` is `ErrUnknown`. A pin, capsule or consumed manifest
that is missing or does not match is `ErrBinding`, which also wraps the error
of `Pins` or `Inputs`; nothing is written. A store error, or an error of
`Pins.Release`, stops the admission (`ErrFailed` afterwards); the caller
reopens it.

### Modes and recovery

`Open` starts in RECOVERY_ONLY. It syncs, reads `/counter` and `/lowwater`,
and checks every record in `[lowwater, counter)`: a missing INPUT_PREPARING
record, an EXEC_READY or NATIVE_REJECTED without ADMITTED, a NATIVE_REJECTED
together with EXEC_READY, a DECIDED without EXEC_READY or with
NATIVE_REJECTED, an ABANDONED together with any later record, a `/purged`
mark above `/lowwater`, or a digest that does not recompute is `ErrCorrupt`.
Then it releases the pins of the closed observations in that range again
(with cleanup, it also purges their inputs again), advances `/lowwater`, and
with cleanup deletes the records below it.
`Observations` lists every non-terminal observation with its state.

In RECOVERY_ONLY, `ExecReady` and `Reject` resolve saved obligations and
`Abandon` cleans INPUT_PREPARING; `Begin` and `Admit` are refused (`ErrMode`).
`GoLive` switches to LIVE only when no observation is in INPUT_PREPARING or
ADMITTED (`ErrPending`). In LIVE, `Begin` is also refused while one is. So an
ADMITTED obligation can never be passed over by a new H: it stays in the
store, keeps its pin, and blocks new admissions until it reaches EXEC_READY
or NATIVE_REJECTED. EXEC_READY observations stay listed until the decision
bridge closes them with `Decide`; the bridge's own `GoLive` also refuses
while an EXEC_READY observation has no decision.

`Open` sees only its own `(producer_id, run_id)` prefix. Which run a restart
opens, and the refusal of a native store of another identity, belong to the
startup preflight (E01-02.pins `Preflight`) and E01-02.lifecycle_seams; see
the plan.

Crash points of one observation (AC-CAP-02-01 and AC-CAP-02-02, unit form):

| Power loss | State after `Open` | Result |
|---|---|---|
| In `Begin`, before the counter sync | No observation, or INPUT_PREPARING | Nothing accepted; an orphan `/preparing` beyond the counter is overwritten |
| After `Begin`, before the closing ADMITTED sync | INPUT_PREPARING, or ADMITTED if the record write survived | Never EXEC_READY. INPUT_PREPARING is not a capture: `Abandon` releases its pin. A surviving ADMITTED record is complete, because the pin and capsule were durable before it was written |
| After the ADMITTED sync | ADMITTED with the same candidate, pin digest and capsule digest | The same obligation is recovered; `GoLive` and `Begin` are refused until `ExecReady` or `Reject` |
| In `Reject` or `Abandon`, before the record sync | The earlier state, with its pin | Repeating the step completes it |
| In `Reject` or `Abandon`, after the record sync | Closed; `Open` releases the pin again and advances `/lowwater` | Nothing to repeat |
| In `ExecReady`, before its sync | ADMITTED (or EXEC_READY if the write survived) | Retry replays the closed log and writes the same EXEC_READY |
| After the EXEC_READY sync | EXEC_READY | Listed for the decision bridge |
| In `Decide`, before the record sync | EXEC_READY, with its pin | The bridge repeats the close from its own record |
| In `Decide`, after the record sync | Closed; `Open` releases the pin and, with cleanup, purges the inputs again | Nothing to repeat |

### Guard

`Guard` is the one observation guard of a node. `Acquire` blocks until the
guard is free and returns a `Token`; `Token.Release` frees it, and a second
release is `ErrGuard`. Every admission call takes the token and refuses a nil
token, a released token, or a token of another guard (`ErrGuard`). The lock
order is guard, then native locks. A nested native call passes the token it
received; it never calls `Acquire` again, which would deadlock. An admission
is bound to the guard of the token passed to `Open`. `Mode` also needs the
token. Only one `Admission` may own a producer run at a time: `Begin` re-reads
the durable counter and stops with `ErrConflict` if another instance advanced
it, before it writes anything.

### Digests

All digests are SHA-256, written as 64 lowercase hex digits. `u64` is 8 bytes
big-endian; `tag` is the ASCII tag followed by one zero byte; `H(x)` is the
32-byte SHA-256 of `x`; `str(s)` is `u64(len(s)) || s`; a hex digest inside a
digest input is its 32 raw bytes.

- Candidate: `cand = u64(number) || hash || parent_hash || parent_root`.
- Admitted digest: `H(tag("extractor-admission-admitted-v1") || str(key) ||
  u64(sequence) || cand || pin_digest || capsule_digest)`.
- Consumed digest (recomputed as a check; the formula of `source_inputs`):
  `H(tag("extractor-source-inputs-consumed-v1") || capsule_digest ||
  u64(count) || chain)`.
- EXEC_READY digest: `H(tag("extractor-admission-exec-ready-v1") ||
  admitted_digest || consumed_digest || exec_digest)`.
- NATIVE_REJECTED digest: `H(tag("extractor-admission-rejected-v1") ||
  admitted_digest || evidence_digest)`.
- DECIDED digest: `H(tag("extractor-admission-decided-v1") ||
  exec_ready_digest || decision_digest)`.

The EXEC_READY digest therefore covers the candidate, the pin record, the
static capsule, every consumed request and response in order (through the
chain), and the record corpus.

### Canonical JSON (D99 item 3)

`CanonicalJSON(data)` is the one rule for JSON capsule items such as the
genesis and the chain config. Its output is a function of the parsed value
only, and Go and Python produce the same bytes:

- Input is UTF-8 JSON (RFC 8259) with at most 64 nested arrays and objects,
  and nothing after the value except JSON whitespace. A byte order mark,
  invalid UTF-8, a raw control character in a string, or a lone surrogate
  escape is refused.
- Numbers must be integers `-?(0|[1-9][0-9]*)` of any size and are written as
  read; `-0`, a fraction or an exponent is refused. Chain configs and genesis
  files hold only integers, and a float would have no exact shared form.
- Objects are written with keys sorted by Unicode code point (equal to UTF-8
  byte order); a duplicate key is refused.
- Strings escape only `"` as `\"`, `\` as `\\`, and characters below U+0020
  as `\u00xx` with lowercase hex. All other characters, including `<`, `>`,
  `&`, U+2028 and U+2029, are written as raw UTF-8.
- `true`, `false` and `null` are kept. No whitespace is written.

Errors wrap `ErrCanonical`. Each client block moves its genesis and config
capsule items to this rule; the BSC block also replaces its fork-id recompute
from JSON keys that end in `Block` or `Time`.

### Bounds and concurrency

The guard serializes all calls. `Open` reads the records of every
non-terminal observation; with sequential import and the decision bridge
closing EXEC_READY observations, that is a few records. With cleanup, the
records of closed observations are deleted, at most 256 sequences per sync.
`CanonicalJSON` holds the parsed value once; its recursion is
bounded by the depth limit.

## Checks

`go vet` and `go test -race -cover` in a copy with a generated `go 1.25`
module; `test/mutate.py admission.mutants.json` for the mutants. The expected
digests and canonical outputs in the Go tests come from
`python3 test/harness/admission.py vectors`, an independent Python
implementation. `python3 test/harness/admission.py assemble DIR` lays out
`source_inputs`, this package and the glue under `extractor/` in a module
named `github.com/ethereum/go-ethereum`, the path at which the drivers install
them in the native trees; `go test ./extractor/...` there runs the binding
test with the real `source_inputs` log. These are unit tests on an in-memory
power-loss store model; native evidence for each client is a later block.
