# lifecycle_registry (E05-01.registry)

The durable activation and revision registry with the native decision
mirror, per network (ETH/BSC/POLY through the shared native vocabulary). It
is the client-independent decision core under `adapters/<client>`: the
per-network finality evidence adapters of E05-01 feed it decisions, and the
native caller patches of E01-02 call it through the observation guard. The
package is a standalone Go module over protocol/semantic only, with a
memory-backed `Store` seam, so its tests are portable and offline.

Task: E05-01.registry (issue #50). REQ: REQ-CAP-07, REQ-LIF-01. AC:
AC-CAP-07-04, AC-CAP-07-05, AC-LIF-01-01, AC-LIF-01-02.

## Model

One `Registry` mirrors the native decisions of one native store and keeps
the activation records their applications create. Identities are the public
`Id16`/`Network`/`SemanticDescriptor` values of protocol/semantic; a
`NativeDecisionRef` is the NativeDecisionRef of CURRENT_SEMANTICS over them.

### Decision mirror

`Intend` records the transition intent (INTENDED); `SettleDecided` and
`SettleRejected` mirror the settled native markers. NATIVE_REJECTED and the
applied-decision frontier (APPLIED) are terminal. Intents are idempotent per
decision sequence; a different intent, a sequence out of order or a second
network of one producer run is refused. A SIDE decision carries no public
events: settling it closes its checkpoint (the D79 decision checkpoint
without a batch), so it never blocks later decisions of the run.

### Application

`Apply(ref, plan)` applies one settled decision: the plan is the full
ordered event set (semantic descriptors with their content digests). The
kind rules are fixed: IMPORT applies Applied events, CANONICAL applies
Applied and Removed, FINALITY applies Status events only, SIDE applies
nothing. Applications commit in decision order: an earlier settled decision
that the registry has not closed blocks a later one.

The commit is one roll-forward sequence:

1. the full plan is stored in the decision record and synced — the durable
   application intent;
2. every activation record is derived from it and synced;
3. the decision passes the applied-decision frontier and syncs.

A power loss may keep any subset of the unsynced writes. `Open` verifies
every record digest and then reconciles: each decided decision with a stored
plan is completed to exactly the records the plan derives, in decision
order, idempotently. A partially applied decision is never visible:
`Events` returns only the events of decisions past the frontier
(AC-CAP-07-04), and the applied-decision frontier suppresses a second
application — a repeated marker replays `Intend`, `SettleDecided` and
`Apply` as no-ops, and a different plan of an applied decision is
`ErrAlreadyApplied` (AC-CAP-07-05).

### Speculative corpus and targeted replay

`CaptureCorpus` records the corpus of one captured block execution under a
decision, before native settles the branch. The corpus alone never creates
an Applied event: a SIDE decision is refused (`ErrNotCanonical`) and a
NATIVE_REJECTED decision is refused (`ErrRejected`); execution success does
not substitute a canonical decision (AC-LIF-01-01). The corpus stays
retained, and a later canonical decision of the same branch reuses it — the
activation is recorded as speculative.

When a known branch becomes canonical without a new execution callback and
the corpus is absent, `Apply` defers with `*CorpusReplayError` naming the
exact blocks. Only `ReplayCorpus` can create the missing corpus, and only
for a block the stored application of that decision needs: there is no API
that scans a range, so the unknown downtime range is never scanned
(AC-LIF-01-02). The same application then completes.

### Revisions and cancellability

Revisions of StatusChanged and Removed events come from one counter per
activation: an event must carry exactly `NextRevision`, so an older or
skipped revision is refused. Removed terminalizes the activation and keeps
its cancel meaning: no later event reactivates it, the writer creates no new
Status for it, and the same block returns only as a new activation under a
new decision sequence. `MarkReady` mirrors the READY publication commit and
`MarkAcked` the delivery acknowledgement, but a removal never waits for
either: a READY Applied activation stays cancellable (MESSAGE_CONTRACT
section 5).

## Checks

`go test ./...` (also with `-race -cover`) is portable and offline: mirror
transitions, kind rules, speculative and rejected corpora, deferred
application with targeted replay, every crash point of one application with
every subset of unsynced writes kept (converging to the golden state or to
nothing), marker repeat after restart, cancellability before and after
READY, revision frontier rules, record corruption, concurrent producer
runs, and a convergence check of the published events through the E02-03
test oracle (test/lifecycle_validator) in 100 shuffled delivery orders.
`gofmt -l` and `go vet` are clean.
