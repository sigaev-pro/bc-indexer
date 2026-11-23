# Runbook — bc-indexer acceptance environment operating procedures

E07-03.runbook (#74). The verifiable operating procedures of the product on
the acceptance topology: start, stop, drain, crash recovery and resume,
with the exact state transitions, preconditions, refusals and evidence each
step produces. Every procedure is executable offline against the composed
acceptance stack of `test/acceptance` (the in-memory NATS double, the
memory stores and the merged E01–E06 modules); no hidden environment and
no secrets are involved. The Go drill `runbook_test.go` executes each
procedure below against the real drain controller (`adapters/maintenance`)
and the real maintenance engine (`core/delivery/maintenance`), and
cross-checks the startup decision table embedded in this document.

## Scope and components

- One applicability slot per product family: `geth` (ETH), `bsc` (BSC),
  `bor` (POLY). A PASS of one slot never closes the others.
- Adapter process = the native client process pair of one network: the
  producer WAL (`core/delivery` producer store) and the gateway journal.
- NATS is shared infrastructure; the journals and stores are never shared.
- The durable producer mode is the single source of operational truth:
  `LIVE`, `BLOCKED`, `DRAIN`, `DRAINED`.

## Durable modes

| Mode | Meaning | New live H+1 capture | New polls | Saved obligations |
|---|---|---|---|---|
| LIVE | ordinary capture | admitted | run | run |
| BLOCKED | fatal local condition held the store | admitted on restart | run | run |
| DRAIN | RECOVERY_ONLY drain in progress | refused | refused | run to completion |
| DRAINED | drain sealed | refused (until Resume) | refused (until Resume) | none pending |

## Startup decision table

The table below is normative: the Go drill parses it and verifies every
row against `adapters/maintenance.Startup`. `REJECT` rows are refusals,
not silent defaults.

```table
flag=live mode=LIVE -> LIVE
flag=live mode=BLOCKED -> LIVE
flag=live mode=DRAIN -> REJECT
flag=live mode=DRAINED -> RESUME
flag=drain mode=LIVE -> DRAIN
flag=drain mode=BLOCKED -> DRAIN
flag=drain mode=DRAIN -> DRAIN
flag=drain mode=DRAINED -> DRAIN
```

## START (live)

Preconditions: the producer store opens in `LIVE` (fresh or restarted);
the gateway journal opens its session; the destination check passes.

1. Start with `--extractor-mode=live`.
2. The startup decision answers `LIVE`.
3. New-block and finality callbacks are admitted; polls run.
4. Evidence: producer mode `LIVE`, batches advance INTENT → READY →
   SEALED → NATS_ACKED → retired.

## STOP (graceful: drain to DRAINED)

Preconditions: the store is in `LIVE` or `BLOCKED`; the operator wants a
closed frontier.

1. Start the same compatible build with `--extractor-mode=drain`.
2. The startup decision answers `DRAIN`; the store durably enters `DRAIN`
   before the recovery sync.
3. The mode barrier holds while the drain runs: new live callbacks and new
   polls are refused with the admission-closed error; only saved
   obligations run.
4. Finish every batch-capable saved obligation. An obligation whose native
   source is unavailable stays batch-capable, so the drain stays in
   `DRAIN`.
5. With the frontier exhausted, seal: the drain point and batch highwater
   are fixed, the admission generation is sealed, and `DRAINED` is saved
   with the seal ack.
6. Evidence: producer mode `DRAINED`, the saved `SealAck` (drain id,
   highwater, generations), the controller snapshot shows no remaining
   batch-capable obligations; retained future-GC pins may remain and do
   not block the completed drain.
7. Refusals on this path: sealing before the frontier is exhausted
   (seal-blocked); re-entering drain over a completed drain
   (transition-rejected); a repeated seal with the same drain id returns
   the saved ack; a different drain id is an immutable conflict.

## STOP (hard) and crash recovery

A crash is the memory cut point: both adapter processes restart on their
surviving stores; only RAM overlays are lost.

1. Whatever durable mode survived, restart with `--extractor-mode=drain`
   to finish or re-check the drain: an unfinished `DRAIN` resumes in
   `DRAIN`; a finished one finds its saved seal and stays `DRAINED`.
2. An explicit `--extractor-mode=live` over an unfinished `DRAIN` is
   rejected: the saved drain resumes first.
3. After the crash-recovered drain completes, the resume path below is the
   only way back to capture.

## RESUME (explicit return to live)

Preconditions: the store is `DRAINED`.

1. Start with `--extractor-mode=live`; the startup decision answers
   `RESUME` — never an automatic return to capture.
2. The Resume commits the next admission generation over a MAINTENANCE
   session; the producer commits `LIVE` with the resume ack.
3. Capture opens at that commit and not before: the admission gate stays
   closed while the store is `DRAINED`.
4. A lost ResumeAck recovers through the saved resume intent: the exact
   intent returns its durable receipt — the same ack, not a new
   generation.
5. Evidence: producer mode `LIVE`, the saved `ResumeAck`, new blocks
   admitted and delivered again.

## Diagnostics

- Controller snapshot (`Report`): durable mode, remaining batch-capable
  obligations, retained pins, drain point and seal state — committed state
  only, visible with debug logging disabled.
- Every refusal names its sentinel: admission-closed, transition-rejected,
  seal-blocked, immutable conflict, fenced session.
- Evidence paths on the acceptance topology: the producer store (mode,
  drain point, seal ack, resume intent), the gateway journal (session
  fence, sealed generation), the NATS stream contents (the reader of
  `reader.go`), and the acceptance suite output. No secrets are involved
  anywhere on this topology.
