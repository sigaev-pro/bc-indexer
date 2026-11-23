# Ethereum finality owner (E05-01.eth_finality)

`adapters/geth/finality` is the Ethereum finality owner of E05-01: the
typed safe and finalized observations of the pinned Geth, the setter,
restart and no-new-block semantics, and the observation of the GC anchor for
the recovery GC engine. It owns REQ-LIF-05 (AC-LIF-05-01, AC-LIF-05-04):

> AC-LIF-05-01 — ETH: live safe above finalized, both previously observed;
> restart with native safe = the restored finalized. The new safe has
> startup provenance; a lower safe is not fatal and not a GC basis.
>
> AC-LIF-05-04 — ETH: a durable irreversible anchor F and the former head;
> a new authoritative SetFinalized below F or with an incompatible hash at
> the same head. Fatal stop of the native owner; no READY, publication or
> GC of the conflicting decision; the event is not dismissed as a stale
> callback. The former correct CLOSED pending follows the usual delivery
> protocol.

The source facts are pinned in
[ETH_BSC_FINALITY_CONTRACT](../../../.project/docs/research/ETH_BSC_FINALITY_CONTRACT.md):
`SetFinalized` writes the native store and `SetSafe` only RAM; startup
restores **both** pointers from the persisted finalized header, so an
observed safe below the previous live safe after a restart is expected; the
finalized setter runs before the safe validation of one forkchoice request,
so one successful RPC status or a ChainHeadEvent proves nothing; a rewind
clears the pointers with nil when the new head is below them. The package
builds on the shared
[lifecycle core](../lifecycle/README.md) (the caller map, the gate, one
FINALITY intent per setter call, the zero-hash no-op) and the
[decision bridge](../../geth/decision/README.md) (the intent, the native
marker, NATIVE_DECIDED). It is ETH-only; the BSC and Polygon finality
owners are E05-01.bsc_finality and E05-01.polygon_observer.

The package is not the native patch. The native block instruments the
actual `SetSafe`/`SetFinalized` callers and the rewind, and calls this
package inside the pass of the mapped caller; it never derives evidence
from an RPC status or a chain event alone. It compiles only in the
assembled layout; `python3 test/harness/finality.py assemble DIR` builds
it.

## Typed evidence

```
Origin       = NATIVE_LIVE | NATIVE_RESTART | RETAINED_DURABLE
Reason       = NOT_OBSERVED | NATIVE_REFERENCE_UNSET
Basis        = CONSENSUS_SETTER | STARTUP_FINALIZED_FALLBACK | UNSET
Ref          = {height, hash}
Observation  = Known{block, origin} | Unknown{reason}
Evidence     = {head, safe, finalized, safe_basis}
Status       = ETH_FINALIZED | ETH_SAFE | ETH_CANONICAL_UNFINALIZED | ETH_FINALITY_UNKNOWN
```

`StatusOf(activation, evidence, ancestry)` projects one activation block:
known finalized covering the activation by ancestry (`isAncestorOrSelf(
activation.block, finalized)`) is ETH_FINALIZED; else known safe covering
it is ETH_SAFE; known references without coverage are
ETH_CANONICAL_UNFINALIZED; no known reference is ETH_FINALITY_UNKNOWN. An
unknown safe never cancels a known finalized. The ancestry oracle is
error-aware: a lookup failure is returned, never turned into false or
Unknown.

## Tracker

`Open(token, store, admission, storeID, identity, ancestry, anchorer)`
loads the durable observation records; until a refresh re-establishes them,
the loaded records are retained evidence. The identity addresses the
network and the native decision references of the run on the
protocol/semantic model; the anchorer is the recovery GC engine that owns
the durable GC anchor of the network. Every call needs the held guard
token. The setter paths follow the decision protocol in explicit steps, so
every boundary survives a crash:

- `Set(token, pass, bridge, head, change)` plans one live native setter
  call: one FINALITY intent with public events through `pass.Finality`
  before the native mutation — the finality coverage change it plans is the
  StatusChanged evidence of the covered activations — with the unchanged
  head (a finality-only update needs no new block), and the next typed
  observation — `NATIVE_LIVE`, safe with the `CONSENSUS_SETTER` basis. A
  zero new hash or an unchanged reference is a no-op (`Noop` in the plan):
  no decision, nothing declared final, the old native reference stays, and
  the pass may rewrite an unchanged reference without a marker.
- `Clear(token, pass, bridge, head, field, evidenceDigest)` plans one
  rewind clear (`SetSafe(nil)`/`SetFinalized(nil)`): a real native write
  recorded with the caller's clear-evidence digest, after which the field
  is `Unknown{NATIVE_REFERENCE_UNSET}` — not the zero-hash no-op.
- `Startup(token, pass, bridge, head, finalized)` refreshes the evidence
  after a restart inside the startup reconcile pass, without a new head
  event. The finalized reader is error-aware: `known=false` is the native
  store holding none, an error is `ErrSource` and never Unknown evidence —
  the stale Known records of the previous run are cleared as
  `Unknown{NATIVE_REFERENCE_UNSET}`, one planned change per field in the
  same fixed order, and their evidence survives as retained, because a
  refreshed Known record would present saved evidence as a fresh native
  read. A changed finalized reference makes one new FINALITY decision with
  public events and `NATIVE_RESTART` origin; safe is restored from the
  finalized header with the `STARTUP_FINALIZED_FALLBACK` basis even when it
  is lower than the previous live safe — not fatal, never a GC basis — and
  the previous live safe survives as retained evidence. A reference
  unchanged by the refresh replays the saved decision: no new intent, no
  new revision. One call plans at most the next change (finalized, then
  safe), because the bridge refuses a second intent while one is
  unresolved; the recovery driver commits, resolves and calls again until
  it plans nothing.
- `Commit(token, plans...)` writes the final authoritative batch of the
  changes: the typed observation records, the retained records they keep,
  and the native decision markers, in one store sync. A crash or power
  loss before the sync changes nothing; recovery replays the plan, and the
  interrupted decision is resolved (its marker survived) or rejected (it
  did not).
- `Resolve(token, bridge, sequence)` records NATIVE_DECIDED through the
  bridge, which itself refuses a change whose marker is not durable.

A setter that ran stays decided even when a later step of the same native
request fails (FIN-ETH-03): each setter call is its own decision.

## GC anchor

The anchor is accepted only from the current durable **finalized**
observation — safe is never an anchor source — and `Accept(token)` requires
the finalized block on the canonical execution ancestry of the observed
head. The recovery GC engine (E05-01.activation_gc) owns the durable
anchor: `Accept` observes the finalized observation on it and commits the
candidate, so the acceptance stays the engine's own sync decision and the
returned `recovery_gc.AcceptedGcAnchor` is the shared anchor contract.
Monotonicity is by ancestry: the candidate must be the accepted anchor or
its descendant; anything else is refused (`ErrRegression`) and the accepted
anchor stays. Accepting the same anchor is idempotent. The durable
finalized observation record is the authoritative evidence — its verified
digest binds the FINALITY decision it names — so acceptance never depends
on the native decision marker, which the bridge cleanup deletes once a
decision without public events, or one the mirror applied, needs it no
longer. Acceptance and the later release of refs and pins are separate sync
decisions; a payload ACK is not GC. The full GC decision (terminal
lifecycle snapshots, no pending transitions, pin ownership) is
E05-01.activation_gc; the registry mirror is E05-01.registry.

## Fatal regression

With an accepted anchor, three native decisions are a proven fatal
regression (`ErrFatal`; the tracker stops, every later call is `ErrFailed`)
— before any evidence record, marker, READY or GC of the conflicting
decision, so it is never dismissed as a stale callback:

1. a new authoritative SetFinalized below the anchor or with an
   incompatible hash (neither the anchor block nor its descendant), with
   the former head unchanged — AC-LIF-05-04;
2. a rewind that clears the anchored finalized reference (the rewind
   deleted the anchored finalized ancestry);
3. a startup refresh observing a finalized reference in conflict with the
   anchor (an operator rewind or a damaged native store below the
   irreversible anchor), or finding none while an anchor is accepted (the
   rewind deleted the anchored finalized ancestry).

A lower safe after restart is not fatal (AC-LIF-05-01). An ancestry lookup
failure is `ErrSource`, stops nothing and blocks a new acceptance; it does
not declare a conflict. The stop does not revoke the accepted anchor or
any decided obligation: by D57 this is not a global stop-publish, and a
former correct CLOSED pending follows the usual delivery protocol.

## Records

Key prefix `extractor-finality-v1/<producer>.<run>/` (every key longer
than 33 bytes and starting with "e", so the native offline pruner keeps
them): `safe`, `finalized`, `safe/retained`, `finalized/retained`. Every
record carries its store identity and producer and a SHA-256 digest over
its content; a record of another store or producer is `ErrInvariant`, a
damaged one `ErrCorrupt`. The accepted anchor is the engine's own
`extractor-recovery-gc-v1/` record of the network.

| Record | Tag | Fields |
|---|---|---|
| observation | `extractor-finality-observation-v1` | field; known branch (block, origin, basis — UNSET for finalized) or unknown reason; decision ref |
| restart evidence | `extractor-finality-restart-v1` | field, startup-observed block (all-zero hash: the unset refresh), unchanged head |

A string is its 8-byte big-endian length and its bytes, a number 8 bytes
big-endian, a hash its 32 raw bytes, a head its number then hash, a
decision ref a presence byte then producer and sequence. A retained record
keeps the block and decision binding of the previous observation with the
`RETAINED_DURABLE` origin and the digest of that retained form.
`python3 test/harness/finality.py vectors` computes the expected values of
`vectors.json` from these formulas, independently of the Go code.

## Tests

`finality_test.go` runs on an in-memory store model with injected power
loss and with the real admission, decision and lifecycle packages: the
vectors, the live setter observations and the finality-only update, the
zero-hash and unchanged no-ops, the setter that stays decided after a
later failed step, the commit crash and both recovery paths, the startup
fallback of a lower safe with retained evidence (AC-LIF-05-01), the
idempotent replay, the finality that advanced over the downtime, the
reader and ancestry source errors, both fatal regression variants and the
anchored clear (AC-LIF-05-04), the safe that is never fatal and never an
anchor, the anchor acceptance rules and the acceptance that survives the
bridge marker cleanup, the startup that clears the stale Known records of
an unset native reference, the status projection matrix, the damaged
records, and the pruner-safe keys. `finality.mutants.json` holds
single-check mutants that run in the assembled module
(`test/harness/test_finality.py` is the portable control).

## What this block does not do

No native client runs here: wiring the mapped Geth setters, the rewind and
the startup driver to this tracker is the Geth native block. The registry
mirror and the StatusChanged publication are E05-01.registry; the BSC and
Polygon owners are their own tasks; the GC decision over the anchor is
E05-01.activation_gc; the reorg/reapply/known-branch/reordered-event
evidence is E05-02.reorg (`test/native_lifecycle`). The AC-LIF-05-01 and
AC-LIF-05-04 profile cells stay NOT_RUN until the native block measures
them.
