# recovery_gc (E05-01.activation_gc)

The recovery garbage-collection decision core, per network (ETH/BSC/POLY
through the shared native vocabulary). It accepts exactly one durable
irreversible GC anchor per network from the network-specific finality
evidence of E05-01 (ETH finalized ancestry, BSC header-attested Source,
Polygon L1-verified checkpoint), evaluates retained entities against that
anchor, proven ancestry and the absence of pending work, and releases
entities and pins through a restartable two-phase commit. It is the
client-independent core under `adapters/<client>`; the E03-01 cleanup
supplies the delivery half of the pending oracle. The package is a
standalone Go module over protocol/semantic only, with memory-backed
storage, so its tests are portable and offline.

Task: E05-01.activation_gc (issue #54). REQ: REQ-LIF-08. AC:
AC-LIF-08-01…AC-LIF-08-05.

## Model

### Anchor acceptance

`ObserveFinality` records one evidence observation with its native decision,
evidence digest and profile. An observation that provably contains the
accepted anchor becomes the in-memory candidate; an observation that does
not cover it is stale and changes nothing — a delayed stale observation
below the anchor is a refresh, never a fatal regression and never a
demotion of the anchor (AC-LIF-08-03). An observation without evidence is
refused, so a source outage cannot invent an anchor. `CommitAnchor` is the
anchor's own sync decision: the candidate must provably contain the accepted
anchor (or, for the first anchor, sit on the canonical chain the engine
already accepted decisions for), then the AcceptedGcAnchor record is
written and synced. A power loss before the commit keeps the previous
anchor, and the unaccepted candidate authorizes nothing (AC-LIF-08-04).

### Authoritative decisions and the fatal regression

`Authoritative` records one new authoritative canonical decision. A decision
whose head is below the accepted anchor height, or whose proven canonical
ancestry carries a different hash at the anchor height, is a fatal
regression: the error is returned, the engine halts, the decision never
reaches the durable frontier and no GC runs by it (AC-LIF-08-05). Completed
releases and delivery state are not rolled back, and already-sent bytes are
not recalled: a previously correct CLOSED batch keeps its normal Gateway
delivery, which this engine never touches. A restart of a halted store
refuses to open (D57). Unavailable ancestry evidence holds the decision
without stopping the engine; only proven regression is fatal.

### Evaluation

`Evaluate` decides one entity at a time, in a fixed order of conditions:

1. no accepted anchor for the network: retain (AC-LIF-08-01);
2. pending capture, transition, replay or delivery: retain — an
   acknowledgement, a block height or a TTL is not pending work and has no
   representation in the engine;
3. canonical branch: release only when the entity block is a proven
   ancestor-or-self of the anchor; the height alone never substitutes for
   proven ancestry;
4. noncanonical branch: an applied entity waits for its removal transition;
   a removed or speculative entity releases only on proven divergence from
   the pinned canonical ancestry.

### Release

`Release` re-evaluates every entity under the current anchor and pending
state — a stale plan authorizes nothing — and the evaluation and the commit
hold one engine lock section, so an anchor acceptance, an observation or an
authoritative decision that lands concurrently cannot change what the
evaluated plan releases. The release then commits in phases: the durable
release intent (its own sync decision), the entity drops through
the Releaser seam with a compact tombstone left behind, the pin releases
for pins no retained entity shares (the caller's retained list protects
shared pins), and the completion marker. Every step is idempotent, so a
power loss at any step is completed by the next Open or Reconcile without
applying anything twice, while shared pins and the compact
anchor/ancestry/decision-frontier records survive (AC-LIF-08-02).

## Checks

`go test ./...` (also with `-race -cover`) is portable and offline: no
anchor and pending retention across all four pending kinds, ancestry-not-
height coverage, stale and unavailable observations without false fatals,
candidate power loss, every crash point of one release with every subset of
unsynced writes kept, fatal below-anchor and divergent-head regressions with
the halt and the refused reopen, unknown-ancestry hold, anchor monotonicity
and first-anchor chain check, record corruption, concurrent evaluation, and
a composition test where adapters/lifecycle_registry drives the entity
states over one shared store. `gofmt -l` and `go vet` are clean.
[recovery_gc.mutants.json](recovery_gc.mutants.json) holds single-check
mutants of the anchor, evaluation, halt and release rules, run with
`test/mutate.py`.
