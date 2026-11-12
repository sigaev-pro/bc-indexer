# Lifecycle core (E01-02.lifecycle_seams, block 0)

`adapters/<client>/lifecycle` is the client-independent part of
E01-02.lifecycle_seams for Geth (`geth`), BSC (`bsc`) and Bor (`bor`):
REQ-CAP-01 (AC-CAP-01-01), REQ-LIF-05 (AC-LIF-05-02, 05-03) and REQ-LIF-06
(AC-LIF-06-04, 06-05). It holds:

- the caller map (`callers.json`): every native caller of the pinned source
  commits that executes blocks or system calls, changes the canonical chain,
  or changes finality, including events without a new block and known-branch
  paths, each with its route through the observation guard and its owner;
- the gate that the native patches use: a mapped caller enters it with the
  held guard token, a native write choke point requires an open pass of a
  caller with that effect, and any other write fails closed;
- the live boundary H with the gate phases of the NATIVE_CAPTURE_PROTOCOL
  startup rules;
- the client-independent evidence rules of the finality callers: one
  FINALITY decision per native setter call, a zero hash as a no-op, and the
  error-aware BSC attestation read.

It implements the observation-guard rule ("Один observation guard", one
observation guard), the startup and known-branch rules ("Startup, known branch
и lifecycle GC") and the decision-marker rules of
[NATIVE_CAPTURE_PROTOCOL](../../../.project/docs/NATIVE_CAPTURE_PROTOCOL.md),
with the items that D100 and D101 hand to this task.

This directory is shared. Every file outside `native/` is byte-identical for
Geth, BSC and Bor; `python3 test/harness/lifecycle.py check` and the portable
test `test/harness/test_lifecycle.py` (run by `bootstrap/ci`) refuse any
difference. `native/` is reserved for the per-client blocks. The package
imports the Go standard library and the shared admission and decision
packages at `github.com/ethereum/go-ethereum/extractor/...`, the path at which
the client blocks install them, so it compiles only in that layout; `python3
test/harness/lifecycle.py assemble DIR` builds it.

## What this block does not do

No native client runs here. The native patches that call the gate (the
caller entries, the write choke points, the FINALITY and CANONICAL intents
without a new block, the node flag, the caller of `Start`, the place where H
is read), the per-client items of D100 and D101 and the AC verdicts are the
client blocks; the plan is in PROJECT_STATUS ("E01-02.lifecycle_seams").
Every AC-CAP-01-01, AC-LIF-05-02, 05-03, AC-LIF-06-04 and 06-05 profile cell
stays NOT_RUN. Typed finality observations, the GC anchor, VotePool evidence
and Polygon checkpoints belong to E05-01.eth_finality, E05-01.bsc_finality
and E05-01.polygon_observer; the mirror to E05-01.registry; release of
retained side blocks to E05-01.activation_gc.

## Caller map

`callers.json` pins each client to the source commit of its accepted native
tree (`sources`): the candidate-lock commits that every merged admission and
decision patch applies to (Geth `1df30b18`, BSC `54afaff4`, Bor `3a018e38`).
The commits in the NATIVE_CAPTURE_PROTOCOL table are earlier candidates; the
portable test checks `sources` against
`.project/bootstrap/components/candidate-lock.json`. It lists callers. Each caller has:

| Field | Meaning |
|---|---|
| `id` | `<client>.<name>`, unique, lowercase |
| `client` | `geth`, `bsc` or `bor` |
| `source` | `<file>.go:<function>` at the pinned commit |
| `entry` | the native entry points that reach it (RPC methods, commands, internal callers) |
| `effects` | the mutations it may cause: `EXECUTION`, `SYSTEM`, `CANONICAL`, `FINALITY` |
| `trigger` | `NEW_BLOCK`, `NO_NEW_BLOCK`, `KNOWN_BRANCH`, `SIDE_CHAIN`, `REORG`, `STARTUP`, `SYNC_MODE`, `SEALING`, `RPC`, `MANUAL_COMMAND` |
| `route` | how it passes the guard (below) |
| `kinds` | the decision kinds it may make |
| `status` | `GUARDED` (a merged patch routes it; `patch` names the edit) or `PLANNED` (the owner routes it) |
| `owner` | the task that guards it, or that must still route or exercise it (a GUARDED row may name the task that exercises a merged patch) |
| `refusal` | the fail-closed mechanism of a `REFUSE` caller |
| `note` | why the route is right, and what is not this task's |

Routes. There is no bypass route: a caller that mutates chain state is
`OBSERVATION`, `DECISION` or `REFUSE`.

- `OBSERVATION`: executes new blocks under an admission observation, and the
  decision that follows closes it. It needs `EXECUTION`, and its kinds are
  among `IMPORT`, `SIDE` and `CANONICAL`.
- `DECISION`: changes the canonical chain or finality without a new
  execution, through a decision intent before the native mutation and a
  marker in its final native batch. Its effects are `CANONICAL` and/or
  `FINALITY`, and its kinds are exactly the matching `CANONICAL` and
  `FINALITY`. Exception: the `*.startup-reconcile` rows. Their native
  mutation (startup repair) runs before the gate opens, so no choke point
  sees it; the recovery driver decides it afterwards from native evidence,
  inside its startup pass, or stops with a source invariant error.
- `REFUSE`: the capture profile forbids it (miner, snap sync, manual
  rewrites of chaindata, parallel or pipelined import). It has effects, no
  kinds and a `refusal` that names the mechanism (the gate, the profile
  check, a native guard).
- `READ_ONLY`: re-executes or reads without changing chain state (eth_call,
  tracing, payload building, prefetch). No effects, no kinds, no pass; it is
  never a capture point.

Map rules (`ParseMap` and `Validate` in Go, `check_map` in Python,
independently): schema version the integer 1; exactly the three clients,
each pinned to a 40-digit commit; every field present and unknown fields
refused; each caller valid as above, with a `patch` exactly
when `GUARDED` and a task-ID owner; unique IDs; and for every client the
`OBSERVATION` and `DECISION` callers together cover all four effects. The
map rows are source review of the pinned commits; each client block checks
its rows against its native tree and exercises them.

## Gate

`Open(token, store, admission, storeID, client, drain)` loads the map rows
of the client and the stored boundary. Every call that changes gate state,
except `Require`, needs the held guard token; `Phase`, `Boundary`,
`Coverage` and `Gaps` only read.

Lock order. NATIVE_CAPTURE_PROTOCOL puts the observation guard before native
locks, and Geth and BSC keep that order (guard, then `chainmu`). Bor takes
the guard inside `processBlockForImport` with `chainmu` already held, so in
Bor the order is `chainmu`, then guard, then the whitelist finality lock;
the Bor callers that take `chainmu` with `TryLock` themselves (`SetHead`,
`SetCanonical`, `writeKnownBlock`) take the guard right after it. No caller
may take the guard while it holds a whitelist lock, or hold the guard across
a call that takes `chainmu` itself (`borVerify`, `reorgToFinalized`). Each
client block checks its order with a deadlock test.

- `Enter(token, id)` opens a pass for a mapped caller. An unmapped ID is
  `ErrUnmapped`; a `REFUSE` caller is `ErrRefused` and its refusal is
  counted; a `READ_ONLY` caller needs no pass. A nested native call enters
  again with the same token; passes close in reverse order (`Exit`).
- `Require(effect)` is called by the native write choke points (the state
  commit, the head and canonical-hash writers, the finality setters) before
  the mutation. The innermost open pass must belong to a caller with that
  effect. Otherwise the mutation bypassed the gate: `ErrBypass`, the native
  write fails closed, and the gate stops (every later call is `ErrFailed`).
  Because the guard serializes the patched callers, the check catches any
  write that happens with no pass open or under a pass of another effect; a
  write from another goroutine while a pass is open is not caught here and
  stays a source-review item of each client block.
- `Pass.Intend(bridge, intent)` records the decision intent of the innermost
  pass, if its caller may make that kind, and counts it. `Pass.Finality`
  builds it for one finality setter call. An observing pass of a `DECISION`
  caller must record an intent before it writes: `Require(CANONICAL)` needs a
  `CANONICAL` intent of that pass and `Require(FINALITY)` a `FINALITY`
  intent; otherwise it is `ErrBypass`. A `CANONICAL` intent covers every
  write of its reorganization. A `FINALITY` intent, or a no-op rewrite of an
  unchanged reference (below), allows exactly one finality write, so each
  setter call of one request needs its own intent, made right before its
  own write.
- `Coverage()` counts passes, intents and refusals per caller, and
  `Gaps(map, client, coverage)` lists every `OBSERVATION` caller without a
  pass, every `DECISION` caller without an intent, and every `REFUSE` caller
  without a refusal: the AC-CAP-01-01 check that no required caller was
  missed. An empty list is required for a PASS. Refusals that happen outside
  the gate (the profile check, the node flag check, a refused command in its
  own process) do not reach `Enter`; the client block adds their evidence to
  the coverage map before it calls `Gaps`.

### Phases and the live boundary H

| Phase | When | Who may enter |
|---|---|---|
| `BOOTSTRAP` | no boundary record yet (first start, initial native sync) | every routed caller; passes do not observe (`Observe` false), so the native mutation runs without capture and `Intend` is refused |
| `RECOVERY` | a boundary exists, before `GoLive` (every restart) | only a `STARTUP` caller and the callers nested in its pass; every other entry is `ErrPhase`, so new imports and head or finality changes wait until live |
| `LIVE` | after `GoLive` | every routed caller, observing |
| `DRAIN` | opened with `drain` | as `RECOVERY`; `GoLive` is refused |

`GoLive(token, head, root, roots, bridge)` runs under the guard with no
pass open (`ErrOrder` otherwise, so no bootstrap pass carries uncaptured
mutations past H), with the native head and its state root read under the
guard. Without a boundary it requires the root to be durable in the native
store (`roots.Durable`: flushed through the hash provider and synced;
`ErrRoot` otherwise), so native startup repair cannot rewind below H; then it
writes the boundary record with the head and its root and syncs it, then
calls `decision.Bridge.GoLive`. The client block passes the state root of
the H header itself, read under the guard. With a boundary (a restart, or a
crash after the boundary sync) it keeps the stored H and never sets a new
one. If the bridge
refuses, the gate stays in `RECOVERY`. A failed boundary write stops the
gate; a power loss before its sync leaves no boundary, so the next start is
`BOOTSTRAP` again and nothing was captured.

Record: key `extractor-lifecycle-v1/<producer>.<run>/boundary` (longer than
33 bytes and starting with "e", so the native offline pruner keeps it),
JSON `{"native_store_id","producer","head":{"number","hash"},"root","digest"}`. A
record of another store or producer is `ErrInvariant`; a malformed record or
a wrong digest is `ErrCorrupt`.

## Finality evidence

`FinalityDigest(change)` is the evidence digest of one native setter call
(`SAFE` or `FINALIZED`, the mapped caller, the old and new blocks). A zero
new hash is `ErrNoop`: the Engine API uses it for "no change", so the old
native reference stays and nothing is declared final (AC-LIF-05-02). An
unchanged reference is `ErrNoop` too. An old zero hash means "none yet".

A new hash equal to the old one at another number is `ErrInvalid` (one block
cannot have two numbers).

`Pass.Finality` makes one FINALITY intent per setter call, before the
setter runs, with the unchanged head. The native setter then writes its
reference and the marker in one batch and syncs, and the caller resolves the
decision. For an unchanged reference it makes no intent but lets the pass
rewrite the same reference once, because Geth calls `SetFinalized` on every
forkchoice update with a non-zero hash; that write carries no marker. The
client block reads the old reference under the guard from the same native
field the setter writes, so a real change is never taken for an unchanged
one. A zero hash allows no write, because no setter runs. A setter that ran
stays decided even if a later step of the same request fails and the request
returns an error; a setter that did not run makes no decision (AC-LIF-05-03).
A decided record cannot be rejected afterwards.

A rewind that clears safe or finalized (`SetSafe(nil)`, `SetFinalized(nil)`
in the `*.set-head` callers) is a real native write, not the Engine API
"no change" of a zero hash. `Pass.Finality` does not cover it, because
`FinalityDigest` treats a zero new hash as a no-op. The client block records
one FINALITY intent per cleared field through `Pass.Intend`, with its own
clear-evidence digest. Until it does, the clear fails closed at the choke
point (`ErrBypass`).

`ReadAttestation(snapshot, genesis)` is the error-aware read of the BSC
Parlia vote attestation: the target is the justified block, the source the
finalized block. A correct snapshot without an attestation is
`GENESIS_FALLBACK` with the real genesis hash (AC-LIF-06-04). A snapshot or
genesis lookup error, a malformed or zero hash, or a source above the target
is `ErrSource`, never genesis, Unknown or empty evidence (AC-LIF-06-05).

## Digests

SHA-256 of the tag, one zero byte, then the fields; a string is its 8-byte
big-endian length and its bytes, a number is 8 bytes big-endian, a hash is
its 32 raw bytes, a head is its number then its hash.

| Digest | Tag | Fields |
|---|---|---|
| boundary | `extractor-lifecycle-boundary-v1` | native store ID, producer (`<producer>.<run>`), head, state root of the head |
| finality | `extractor-lifecycle-finality-v1` | field, caller ID, old head, new head |
| attestation | `extractor-lifecycle-attestation-v1` | status, justified head, finalized head |

`python3 test/harness/lifecycle.py vectors` computes the expected values of
`vectors.json` from these formulas, independently of the Go code.

## Tests

`lifecycle_test.go` runs on an in-memory store model with injected power
loss and with the real admission and decision packages: the map rules, the
gate routes and phases, nesting, the choke-point check, the boundary record
and its crash points, coverage gaps, and the unit forms of AC-LIF-05-02,
05-03, 06-04 and 06-05. `lifecycle.mutants.json` holds single-check mutants
that run in the assembled module.
