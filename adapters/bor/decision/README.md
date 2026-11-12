# Decision core (E01-02.decision_bridge, block 0)

`adapters/<client>/decision` is the client-independent child-root,
native-marker and mirror seam of REQ-CAP-07 (AC-CAP-07-01, 07-02, 07-03 and
07-06) for Geth (`geth`), BSC (`bsc`) and Bor (`bor`). It records the
transition intent of each native decision before the native mutation,
recognizes the native decision marker, records the authoritative
NATIVE_DECIDED result with its single delivery obligation, and closes the
EXEC_READY observations of E01-02.admission that the decision resolved. It
implements the "Durable состояния и native decision bridge" and "Startup"
rules of [NATIVE_CAPTURE_PROTOCOL](../../../.project/docs/NATIVE_CAPTURE_PROTOCOL.md)
with the D100 items handed to this task.

This directory is shared. Every file outside `native/` is byte-identical for
Geth, BSC and Bor; `python3 test/harness/decision.py check` and the portable
test `test/harness/test_decision.py` (run by `bootstrap/ci`) refuse any
difference. `native/` is reserved for the per-client blocks. The package
imports the Go standard library and the shared admission package at
`github.com/ethereum/go-ethereum/extractor/admission`, the path at which the
client blocks install it in each native tree (module path
`github.com/ethereum/go-ethereum` in all three), so it compiles only in that
layout; `python3 test/harness/decision.py assemble DIR` builds it.

## What this block does not do

The native marker patch, the child-root barrier over the native trie
database, the recovery driver and the import-path calls are client blocks.
The mirror itself (activation refs, revisions, Removed/Applied/Status
descriptors, the registry generation, READY, the applied-decision frontier;
AC-CAP-07-04 and 07-05) belongs to E05-01.registry, and the decision to
batch ID allocation and semantic descriptors to E02-01.semantic_descriptors.
The caller map of canonical and finality paths without a new block belongs
to E01-02.lifecycle_seams. The plan is in PROJECT_STATUS
("E01-02.decision_bridge"). This block runs no native client and gives no AC
verdict.

## Design

### Seams it joins

- `admission.Admission` (shared, E01-02.admission). The bridge shares its
  observation guard and token (`Admission.Check`), reads the open
  observations, and closes each resolved EXEC_READY observation with the
  admission step `Admission.Decide` (D100: never by writing admission keys).
  `Decide` writes the DECIDED record, releases the pin, and, for an
  admission opened with `OpenWithCleanup`, deletes the capsule and log
  records (`source_inputs.Purge`) and the admission records below its
  low-water mark.
- `Roots` (client block): `Durable(root)` reports whether a state root is
  durable in the native store, after the native StateDB commit, the flush
  through the hash provider and `SyncKeyValue` (the child-root barrier).
- `Mirror` (E05-01.registry): `Applied(sequence, decidedDigest)` reports
  whether the mirror durably applied the decision, so that its
  applied-decision frontier covers it. The bridge asks it only about
  NATIVE_DECIDED `BATCH` decisions. Decision sequences are not contiguous
  for the mirror: a `CHECKPOINT` decision or a rejected intent never reaches
  it, so its frontier is per decision sequence and digest, never a
  contiguous prefix.
- The native decision marker. The native patch of each client writes
  `MarkerValue(intent)` under `MarkerKey(producer, sequence)` in the final
  authoritative native batch of the decision (with the head, canonical or
  finality mutation) and syncs it. The bridge reads it from the same store;
  only `Cleanup` deletes it.

### Store and durability model

The store is the native `ethdb.Database`: `Has`, `Get`, `Put`, `Delete`,
`SyncKeyValue` (`admission.DeleteStore`). A write or delete is durable only
after `SyncKeyValue` returns nil; a power loss may keep any subset of
unsynced writes; after a process crash, unsynced writes may still be visible.
`Open` syncs before it reads. Every step writes its record and syncs before
anything depends on it.

Keys start with `extractor-decision-v1/<producer_id>.<run_id>/`; markers
with `extractor-decision-marker-v1/<producer_id>.<run_id>/<seq>`. Every key
is longer than 33 bytes and starts with `e`, so the native offline pruner
never deletes one. The sequence is the native decision sequence
(`native_decision_sequence`, 16 lowercase hex digits), a counter separate
from observation, event, batch and source sequences.

| Key suffix | Value (JSON, strict decode) |
|---|---|
| `/counter` | `{"next": n}`: decisions below `n` exist. Commit point of `Intend` |
| `/lowwater` | `{"sequence": n}`: every decision below `n` is done and is not read |
| `/purged` | `{"sequence": n}`: the records of every decision below `n` are deleted |
| `/<seq>/intent` | Transition intent; also the bytes of the native marker |
| `/<seq>/decided` | NATIVE_DECIDED: intent digest, delivery (`BATCH` or `CHECKPOINT`), digest. Commit point of the decision and its obligation |
| `/<seq>/rejected` | Native rejection of the intent: intent digest, evidence digest, digest |

The intent record is `{native_store_id, producer, sequence, kind, old_head,
new_head, members, evidence_digest, events, digest}`. `kind` is `IMPORT`
(the head moves over newly executed blocks), `SIDE` (executed blocks stored
without a head change; no events), `CANONICAL` (a canonical change without a
new execution, or a reorganization that includes executed blocks) or
`FINALITY` (no members). A member is `{observation, block, exec_ready_digest,
child_root}`: an EXEC_READY observation, its candidate block hash, its
EXEC_READY digest and its durable child state root. The marker fields of
NATIVE_CAPTURE_PROTOCOL map onto it: `native_store_id`, `decision_sequence`
(`sequence`), `observation_id` (members), `old_head`, `new_head`,
`decision_kind` (`kind`), `evidence` (`evidence_digest`), and `input_digest`
and `exec_digest`, which the EXEC_READY digest of each member binds.
`events` says whether the decision has public events.

### Steps

The caller holds the observation guard. One decision, in protocol order:

1. Child-root barrier (client): the native StateDB commit, the trie flush
   and `SyncKeyValue` make the child root of every executed member durable.
2. `Intend(intent)`: every member must be an open EXEC_READY observation
   with its EXEC_READY digest, the authoritative observation of its block,
   and `Roots.Durable(child_root)` must be true. It writes `/<n>/intent`,
   syncs, writes `/counter` = n+1 and syncs. It is refused while another
   intent is unresolved.
3. Native mutation (client): the native side applies the transition; its
   final authoritative batch carries the marker and is synced.
4. `Resolve(n)`: the marker must be present with exactly the intent bytes.
   It writes `/<n>/decided` with `BATCH` if the intent has public events and
   `CHECKPOINT` otherwise, and syncs. This sync is the commit point of the
   authoritative decision and its single delivery obligation. Then, except
   for `SIDE`, it closes the resolved observations with `Admission.Decide`,
   which releases their parent pins: the child root and the decision are
   durable now. A `SIDE` decision retains its members (see "Side blocks").
5. Delivery (E02-01, E05-01, E03): `Obligations` lists each NATIVE_DECIDED
   `BATCH` decision that the mirror has not applied. The mirror allocates
   the batch for it atomically, builds the descriptors and closes READY.
6. `Cleanup`: for each NATIVE_DECIDED decision that is a `CHECKPOINT` or
   that the mirror applied, it deletes the marker, syncs once, then advances
   `/lowwater` and deletes the records below it (`/purged`).

`RejectIntent(n, evidence)` records the native rejection of an unresolved
intent when no marker exists: the native side never applied the transition
and kept none of its member blocks (for example a native validation or
commit failure after EXEC_READY). It closes the members with
`Admission.Decide` using the rejection digest. No NATIVE_DECIDED record,
obligation or Applied follows. A valid block that loses the fork choice is
stored natively as a side block, so its intent is a `SIDE` decision, never
a rejection.

### Side blocks

A known side block can become canonical later without a new execution
(NATIVE_CAPTURE_PROTOCOL, "known branch"); its corpus binding, pin and
inputs must still exist then. So a `SIDE` decision does not close its
members: they stay EXEC_READY with their pins, capsules and logs, the
admission low-water mark stays below them, and the decision stays listed
in `Decisions` (NATIVE_DECIDED, its marker removed by `Cleanup`) while it
retains a member. `Undecided` and `GoLive` treat retained members as
decided. A later `CANONICAL` or `IMPORT` decision that names a retained
member closes it, and the `SIDE` decision is then done. The release of side
blocks that never become canonical belongs to E05-01.activation_gc, which
adds its step to this package (never by writing these keys) and releases
only on the irreversible anchor of the native profile. Until then the
retained state grows with the number of stored side blocks, as the GC rule
requires.

### Delivery obligation

A NATIVE_DECIDED decision with public events has exactly one obligation,
identified by its decision sequence and NATIVE_DECIDED digest. Recovery
lists the same obligation from the same records; repeating `Resolve`
returns the same record and never a second one. A decision without public
events (`CHECKPOINT`, including every `SIDE` decision) has no obligation:
its NATIVE_DECIDED record is the durable decision checkpoint. A rejected
intent has none. Neither creates a public batch. After the mirror applies an
obligation, the mirror's applied-decision frontier keeps it out of
`Obligations` even if a power loss leaves its marker; `Cleanup` then removes
the marker.

### One decision per block

After a recovery the same block can be observed twice: an EXEC_READY
observation from before the crash and a new one of the same candidate. The
authoritative observation of a block is its open EXEC_READY observation with
the lowest sequence. `Intend` refuses a member that is not authoritative, or
two members of one block (`ErrDuplicate`). When a decision is resolved or
rejected, every open EXEC_READY observation of a member block is closed with
the same digest, so the block gets one decision. A later new application of
the same branch is a new decision with a new decision sequence, as
CONTROL_WIRE_CONTRACT requires.

### Recovery and modes

`Open` syncs, reads `/counter`, `/lowwater` and `/purged`, and checks every
decision in `[lowwater, counter)` with its marker:

| Found | Result |
|---|---|
| Missing intent, damaged record, digest that does not recompute, intent of another native store or producer | `ErrCorrupt` |
| An unresolved intent that is not the last sequence | `ErrCorrupt` |
| A marker whose bytes differ from its intent | `ErrInvariant` |
| A rejected intent with a marker | `ErrInvariant` |
| NATIVE_DECIDED `BATCH` without marker that the mirror has not applied | `ErrInvariant` (the marker was removed before the mirror) |
| Unresolved intent, with or without marker | Listed in `Decisions` |
| NATIVE_DECIDED with marker | Listed; `Obligations` lists it until the mirror applies it |
| NATIVE_DECIDED without marker, or rejected | Done |

`Open` then closes, again, every open observation of a resolved decision
other than `SIDE` (finishing a close that a power loss interrupted),
advances `/lowwater` and deletes the records below it. The client recovery
driver opens the admission, finishes its ADMITTED obligations (`ExecReady`
or `Reject`) and abandons INPUT_PREPARING observations before it opens the
bridge: an observation of a decided block that is not EXEC_READY stops
`Open` with `ErrInvariant`.

An absent marker never proves a decision. The client recovery driver first
runs the native DB recovery, then, for an unresolved intent, either
completes exactly the saved transition (which writes the marker) and calls
`Resolve`, or records the native rejection with `RejectIntent`; if native
data cannot settle the outcome it stops with a source invariant error. For
an EXEC_READY observation without a decision (`Undecided`) whose child root
is not durable, it runs native-only replay from the parent pin and the
capsule, which must produce the same root; the EXEC_READY record and the
decision identity are not replaced (AC-CAP-07-02). `GoLive` switches the
admission to LIVE only when no intent is unresolved and no EXEC_READY
observation is undecided, and otherwise follows `admission.GoLive`.

Crash points of one decision (unit form):

| Power loss | State after `Open` | Result |
|---|---|---|
| Before EXEC_READY is durable (AC-CAP-07-01) | ADMITTED | `Intend` refuses it; admission recovers the same obligation |
| After EXEC_READY, child root volatile (AC-CAP-07-02) | EXEC_READY, undecided | `Intend` refuses (`ErrChildRoot`) until native-only replay makes the same root durable |
| In `Intend`, before the counter sync | No decision | An orphan intent is overwritten |
| Process crash in `Intend` after the counter write, before its sync | Unresolved intent without marker (`Open` syncs the visible counter) | Recovery completes or rejects it, as after any unresolved intent |
| After `Intend`, inside the native mutation (AC-CAP-07-03) | Unresolved intent, no marker | `Resolve` is `ErrNoMarker`; native recovery completes the saved transition |
| After a native rejection, before `RejectIntent` (AC-CAP-07-06) | Unresolved intent, no marker | `RejectIntent` records the rejection; no obligation |
| After the marker, before the decided sync | Unresolved intent with marker | `Resolve` completes it |
| After the decided sync, before the closes | NATIVE_DECIDED | `Open` closes the observations; one obligation |
| After the mirror applied, before the cleanup sync | NATIVE_DECIDED with marker | No obligation; `Cleanup` repeats |

Repeating a completed `Resolve` returns the stored record. A resolved or
rejected decision that is done is no longer listed, so repeating
`RejectIntent` is `ErrUnknown`. A store error stops the bridge (`ErrFailed`
afterwards); the caller reopens it. `Intend` re-reads the counter: a second
bridge of the same producer run stops with `ErrConflict` before it writes.

### Digests

SHA-256 as 64 lowercase hex digits; `u64` is 8 bytes big-endian; `tag(t)` is
the ASCII tag and one zero byte; `str(s)` is `u64(len(s)) || s`; a hex digest
inside a digest input is its 32 raw bytes; `head = u64(number) || hash`.

- Intent: `H(tag("extractor-decision-intent-v1") || str(native_store_id) ||
  str(producer) || u64(sequence) || str(kind) || head(old_head) ||
  head(new_head) || u64(len(members)) || for each member: str(observation)
  || block || exec_ready_digest || child_root || evidence_digest ||
  events)`, with `events` one byte, 1 or 0.
- NATIVE_DECIDED: `H(tag("extractor-decision-decided-v1") || intent_digest
  || str(delivery))`.
- Rejected intent: `H(tag("extractor-decision-rejected-v1") || intent_digest
  || evidence_digest)`.
- Marker value: the JSON encoding of the intent record, fields in the order
  above, no whitespace.
- Admission DECIDED (in the admission package):
  `H(tag("extractor-admission-decided-v1") || exec_ready_digest ||
  decision_digest)`, where `decision_digest` is the NATIVE_DECIDED or
  rejected-intent digest.

### Bounds and concurrency

The guard serializes all calls. One intent is unresolved at a time, so
`Open` reads the decisions whose markers are not yet removed, which the
mirror bounds, and the `SIDE` decisions that still retain a member, which
E05-01.activation_gc bounds. Records of done decisions are deleted in batches of 256
sequences per sync; a power loss repeats a batch.

## Checks

`go vet` and `go test -race -cover` in the module from `python3
test/harness/decision.py assemble DIR`; `test/mutate.py decision.mutants.json`
for the mutants. The expected digests and marker bytes come from `python3
test/harness/decision.py vectors`, an independent Python implementation.
The glue test `test/harness/fixtures/decision_core` runs the real
`source_inputs`, admission and decision packages together. These are unit
tests on an in-memory power-loss store model; native evidence for each
client is a later block.
