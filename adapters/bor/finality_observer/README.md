# Polygon finality observer (E05-01.polygon_observer)

`adapters/bor/finality_observer` is the POLY owner block of
E05-01.polygon_observer (#53): the durable typed observation stream of the
native Polygon milestone/checkpoint statuses and the separately verified
L1-finalized checkpoint evidence for GC, per CURRENT_SEMANTICS "Polygon
evidence и L1 GC" (D63) and "Provisioning supporting identities" (D80). It
is the `PolygonCheckpointObserver` inside the Bor adapter: no new process,
no Ethereum transaction extractor, network reads only through the injected
own-Heimdall and own-L1 seams, outside hooks and native locks.

It is a separate Go module (`go 1.25`, the oldest directive among the
accepted Geth, BSC and Bor graphs) with two dependencies:
[protocol/block](../../../protocol/block/README.md) (anchors, references,
origins, unavailable reasons, Polygon markers, native decision refs) and
[protocol/profiles](../../../protocol/profiles/README.md) (the normative
D80 `ComputeSourcesBindingDigest`), both wired with repository-local
`replace` directives. It performs no I/O and imports no client type; tests
use the memory store of this package. Every assigned AC
(AC-LIF-07-01…06, 08, 09; the structural AC-LIF-07-07 belongs to
E02-10.closed_profiles) has one test; native runtime evidence stays with
the E01/E05 harness.

## Files

| File | Content |
|---|---|
| `sources.go` | The strict `finality.polygon.sources_file` parser: closed schema v1, the two role names, strict UTF-8 JSON without unknown or duplicate fields, comments, substitutions or trailing data; the D80 digest of the exact file bytes |
| `evidence.go` | The typed D63 evidence: `SourceObservationRef` with the mandatory D80 digest and the closed operation/selector union, `Milestone`, `Checkpoint`, `Whitelist` (with the narrower retained form), `NativeFinalized`, `L1Checkpoint` with `FinalizedContractState`, the `Evidence[T]` Known/NotObserved/Unavailable trichotomy, `PolygonEvidence`, with strict JSON |
| `digests.go` | The canonical digests: exact bytes (response, calldata, result), the RootChain params digest, the whitelist ancestry digest, the Bor range digest |
| `l1.go` | The checked `checkpoint_id * child_chain_block_interval` header block id, the closed `headerBlocks` calldata (selector `0x41539d4a`), the strict static decode of the five-word result |
| `observer.go` | The observer: `ObserveNative`, `ObserveL1` (the five D63 steps), `Markers`, `Projection`, `Resend`, `ReplaceSources`; the source error taxonomy |
| `store.go` | The durable observation stream seam (`Store`), the memory store with explicit `Sync`/`Crash` power-loss points and the canonical, deterministic `EvidenceBytes` encoding |

## The five D63 steps of `ObserveL1`

1. The native stored checkpoint and the chain/checkpoint parameters from
   the own Heimdall, with the exact responses and source identity; a
   checkpoint whose callbacks carry no id gets nothing invented — the
   lookup is `Unavailable{CHECKPOINT_PENDING}` until a full query has it.
2. The finalized L1 anchor F from the own Polygon L1 node; without F the
   pins stay and the lookup retries later; latest/safe and confirmation
   counts never substitute F.
3. The checked `UInt256` product and the exact hash-pinned
   `RootChain.headerBlocks` read at `{blockHash: F.hash, requireCanonical:
   true}`.
4. The strict five-word decode and the cross-checks of root, range and
   proposer against the checkpoint, of the parameters before and after the
   read, and of the L1/Heimdall identities against the pinned binding. A
   mismatch is an explicit `ErrIdentity`/`ErrMalformed` error, never an
   `Unavailable`-success and never an accepted anchor (AC-LIF-07-04); a
   mismatching new candidate does not raise the anchor.
5. The native Bor verifier seam recomputes the checkpoint root over the
   pinned header inputs of the continuous canonical range and re-checks
   the end hash ancestry; the verified frontier only advances — a
   conflicting candidate below or at the verified anchor is
   `ErrInvariant` (a fatal source invariant per D63), and a newer
   finalized F2 produces a new append-only observation bound to F2 while
   the earlier observation keeps its F (AC-LIF-07-05).

## D80 provenance immutability

Every observation freezes its `ObservationBinding`: the D80
`sources_binding_digest` (SHA-256 of the exact pinned file bytes, derived
by `protocol/profiles`), the two supporting deployment IDs and the
verified identities. `ReplaceSources` — including a whitespace-only
change — binds only new observations (AC-LIF-07-08). Recovery and late
serialization (`Resend`, `EvidenceBytes`) take the saved digest and the
saved canonical bytes from the saved observation, never from the current
file, so READY fragments and SEALED frames stay byte-identical across a
config replacement (AC-LIF-07-09). The narrower retained whitelist without
a saved response carries no `SourceObservationRef` and no invented digest.

## Availability, errors and publication

`UnavailableError` is one concrete transient lookup state
(SOURCE_OFFLINE, SOURCE_SYNCING, STATE_UNAVAILABLE, CHECKPOINT_PENDING):
GC waits with the explicit availability, the accepted anchor is never
erased and publication continues (AC-LIF-07-03). `ObserveNative` performs
no L1 read and `Markers`/`Projection` read only the durable stream, so
tx/status snapshots never wait for the GC source. A native milestone purge
drops the milestone whitelist and falls back to the checkpoint whitelist
without touching the retained L1 anchor (AC-LIF-07-06). A persisted
whitelist without its saved full response publishes as the narrower Known
form with absent `source_evidence`, and a checkpoint root is never used as
a block hash (AC-LIF-07-01). The milestone/checkpoint lifecycle markers
are the accepted whitelist anchors — the finality-bearing references —
and the native projection keeps `NATIVE_MILESTONE_COVERED`,
`NATIVE_CHECKPOINT_COVERED`, `CANONICAL_NO_NATIVE_FINALITY` and
`L1_CHECKPOINT_COVERED`, `L1_NOT_VERIFIED` separate, assigned only on the
exact-hash ancestry match of the activation.

## Boundaries

The observer does not commit the GC anchor and never releases capture pins
itself: E05-01.activation_gc owns the terminal/pending/shared-pin
conditions, the anchor commit and the restartable release, consuming the
accepted durable evidence and ancestry of this stream. The whitelist
canonical check and the range verification are seams (`Ancestry`); the
native Bor verifier itself, the REST/RPC endpoints and the E01-01.harness
own-Polygon-L1 fixtures are the client blocks. Every AC verdict of the
native profile stays NOT_RUN until the harness runs them.
