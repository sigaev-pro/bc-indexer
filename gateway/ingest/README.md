# Gateway ingest RPC

`gateway/ingest` is the E03-02.rpc_streaming output: the gRPC control wire
of the gateway ingest boundary. It runs on top of the gateway journal
(E03-01.gateway_store, module `gateway/journal`) and the session fencing
boundary (E03-02.fencing, module `gateway/session`). The destination
descriptor of a switch is validated by the `gateway/destination` module.

## Service

`ingest.proto` (generated bindings are committed) defines, per
CONTROL_WIRE_CONTRACT's RPC table:

| RPC | Purpose |
|---|---|
| `HelloChallenge` | per-boot `boot_nonce` + one-shot `challenge` |
| `OpenSession` | binds a `session_token` to admission/connection generations; returns store epoch, binding, retired prefix |
| `BeginPrepare` | opens or resumes the manifest descriptor stream; `PREPARE_IMPORT_SEALED` carries the original seed |
| `PrepareChunk` | one bounded manifest chunk; idempotent, cursor-based |
| `FinalizePrepare` | verifies the full manifest, returns the exact reservation seed and plan digest |
| `BeginPublish` | accepts seed/plan/layout identity, returns durable resume cursors |
| `PublishChunk` | bidirectional stream of bounded transport frame chunks; one response per request, errors do not close the stream |
| `FinalizePublish` | closes a normal batch: `CLOSED` after completeness validation |
| `SealImport` | closes an IMPORT_SEALED batch with the same validation |
| `GetBatchState` | durable state of one batch, the `IngestAck` only while it is durable, and one bounded page of frame cursors |
| `Release` | ordered release of one `NATS_ACKED` batch up to its final commit; answers `ReleaseAck(batch_id, retired_prefix)` only after it |
| `RecoveryRelease` | completes a batch the gateway store lost from the producer's saved `IngestAck` and ACKED/outbox assertion; answers `ReleaseAck` |
| `SealGeneration` | seals an exhausted admission generation; answers `SealAck` and fences the sessions of that generation |
| `PrepareSwitch` | checks the desired destination online, then durably prepares the switch; answers `SwitchPrepared` while the old binding stays active |
| `CommitSwitch` | re-checks the target, then durably activates the prepared binding; answers `SwitchCommitted` with the same identity |
| `Resume` | opens exactly the next admission generation of a sealed run; answers `ResumeAck` |

With these RPCs a gateway acknowledgement reaches the producer:
`GetBatchState` is the RPC that returns the `IngestAck` once the batch is
`NATS_ACKED`, and `Release` or `RecoveryRelease` is the one that answers the
`ReleaseAck` the producer waits for.

## Rules held by the server

- **Fenced mutating boundary.** Every RPC with a session header, the
  read-only `GetBatchState` included, validates the header first (protocol
  version 2, session token, connection and admission generation, binding
  digest, producer/run identity) and then runs the journal step inside
  `session.Gate.Mutate`: the token check happens together with the commit
  under the gate's serialized ordering, not only when the RPC is read. After
  `FenceAdmission` (the SealGeneration/Resume barrier) a stale RPC answers
  `SESSION_STALE`/`RECONNECT` and creates no state.
- **Typed errors.** Answers carry the registry's `{code, request_id,
  owner_state, retry_action, detail}`; the gRPC status is only a transport
  categorization. Journal errors map one-to-one; unknown errors become
  STORE_FATAL, never a silent success.
- **Bounds.** One RPC including its wrapper is at most 256 KiB
  (`GRPCServerOptions` sets the gRPC receive bound; larger messages are
  refused with ResourceExhausted at the transport level); one data chunk is
  at most 64 KiB (`LIMIT_INCOMPATIBLE`).
- **IMPORT_SEALED** imports the original seed (issuer epoch preserved, no
  sequence allocated) and seals only through `SealImport`;
  `FinalizePublish`/`SealImport` cannot be mixed.
- **Before CLOSED** the manifest digest, the declared layout digest and the
  completeness of every frame are validated by the journal.
- **A repeated call returns the durable receipt.** A retry of a Release, a
  SealGeneration, a PrepareSwitch, a CommitSwitch or a Resume with the same
  intent answers what the first call committed and never commits twice. The
  same identity with another intent, or a new identity over an unfinished
  one, is `IMMUTABLE_CONFLICT`.
- **GetBatchState answers only what is durable.** The `IngestAck` is
  returned only while the `NATS_ACKED` marker exists (`NATS_ACKED` and
  `RELEASING`). A page holds at most 1024 frame cursors in (ordinal,
  fragment, frame) order; when more follow, `next_cursor` is the last cursor
  of the page and the next request continues after it. A read is never a
  permission to delete producer payload.
- **Release is ordered and binds the whole identity.** Only the next batch
  of the run releases, it must be `NATS_ACKED`, and both the manifest and
  the plan digest must match. The cleanup runs in bounded steps, a crash
  resumes from the remaining keys, and a retry for an already retired
  sequence answers the durable prefix again.
- **RecoveryRelease is for a batch the store lost.** It needs exactly the
  next sequence, no local record, and an assertion that names the same
  batch, plan and binding as the saved `IngestAck` of the active
  destination. The accepted assertion is durable.
- **SealGeneration and Resume fence in the same step as their commit**
  (`Gate.MutateRaise`): no RPC of the sealed generation can commit between
  the two. The request comes from the session of the sealed generation, or
  on a retry from a session of the next one.
- **A sealed admission generation opens no session.** `OpenSession` answers
  `GENERATION_CONFLICT` for it. The admission barrier lives in the gateway
  process, so after a restart the durable seal is what keeps the sealed
  generation closed; the retry of the seal and its Resume come from the next
  generation.
- **The switch checks the target online.** `PrepareSwitch` verifies the
  desired destination before anything durable, and the first `CommitSwitch`
  verifies it again before the activation. A failed or unreachable check
  never produces `SwitchPrepared` or a commit. An already committed switch
  answers its receipt without the target. The verifier is bound with
  `SetTargetVerifier`; without one both steps answer `TRANSIENT_UNAVAILABLE`,
  because the check is mandatory and is never skipped.
- **Durable refusals come first.** Every rule that depends only on the
  durable state (no seal, another old seal, an illegal successor descriptor,
  another receipt, another switch over an unfinished one) is answered before
  the online check, so a refusal never depends on NATS being available.
- **A descriptor is a legal successor only when** it differs from the active
  binding, keeps the same destination line and network, and is exactly the
  next binding generation. The journal must know the active binding identity
  for that (`Options.Binding` of the journal on a store that never committed
  a switch); otherwise the switch is `BINDING_MISMATCH`. Every later drain
  cycle can switch again: a committed switch of an earlier seal is history.

### Refusals of the control RPCs

| Refusal | Code | Retry action |
|---|---|---|
| Missing or invalid header; zero or wrong-width identity; unknown batch; an assertion that names another batch, plan or binding | `IDENTITY_MISMATCH` | `RETRY_BACKOFF` |
| Stale, unknown or short session token | `SESSION_STALE` | `RECONNECT` |
| Header generations differ from the session; a session of a sealed admission generation; not the next admission generation; another binding generation than the active one at a seal | `GENERATION_CONFLICT` | `RECONNECT` |
| Descriptor generation is not exactly the next one | `GENERATION_CONFLICT` | `FIX_CONFIG` |
| Binding digest differs from the store; an acknowledgement of another destination; a descriptor of another destination line or network; a Resume that names another binding; a failed target check | `BINDING_MISMATCH` | `FIX_CONFIG` |
| Release out of order or of a batch that is not `NATS_ACKED`; RecoveryRelease that is not the next sequence; no prepared switch to commit; descriptor equals the active binding | `MODE_CONFLICT` | `RETRY_BACKOFF` |
| Switch or Resume without a sealed generation, or over a seal that was already resumed | `DRAIN_REQUIRED` | `DRAIN_OLD_BUILD` |
| Same identity with another intent; new identity over an unfinished one; RecoveryRelease over a batch the store still holds | `IMMUTABLE_CONFLICT` | `DIAGNOSE_IMMUTABLE` |
| Malformed desired descriptor | `INVALID_DESCRIPTOR` | `FIX_CONFIG` |
| Descriptor over 16 KiB | `LIMIT_INCOMPATIBLE` | `FIX_CONFIG` |
| Protocol version is not 2; assertion schema version is not 1 | `VERSION_UNSUPPORTED` | `DRAIN_OLD_BUILD` |
| Target unreachable, or no verifier bound | `TRANSIENT_UNAVAILABLE` | `RETRY_BACKOFF` |

### Contract points this module resolves as follows

- `NOT_FOUND` is a value of `BatchState`, not an error: the error registry is
  closed and the contract lists NOT_FOUND as a successful answer.
- A `PREPARING` batch is reported as it is, without cursors or
  acknowledgement: it exists and has no plan yet.
- A `RETIRED` batch answers without comparing the plan digest and without an
  acknowledgement: its evidence left with the retire commit, and only the
  retired prefix remembers it.
- The contract names no code for an out-of-order or unacknowledged Release.
  It stays `MODE_CONFLICT`, the existing mapping of a refused transition.
- A seal at highwater 0 is accepted for a run that never created a batch:
  the contract fixes zero as the empty frontier.
- `OutboxAssertion` and `BindingCommitAssertion` are fixed versioned
  messages (`schema_version` 1). The gateway checks that they agree with
  the rest of the request; it cannot inspect the producer's disk.
- The session that prepared a switch stays bound to the old binding, so
  `CommitSwitch` admits that old digest for the exact open switch and the
  retry after the commit reaches the durable receipt.
- A switch record is an open transition while it is only prepared or while
  its seal is still the current one. The in-memory reference engine keeps
  the committed phase for the lifetime of its store, which would allow one
  switch only; the journal treats a committed switch of an earlier seal as
  history, so a second destination change is possible.

## Versions

Go 1.27.2, gRPC-Go 1.84.0, protobuf-go 1.36.12 (COMPONENT_STACK). The
committed bindings were generated with exactly these tools; do not generate
with other versions and do not bump them here:

| Tool | Version |
|---|---|
| protoc | v5.29.3 (libprotoc 29.3) |
| protoc-gen-go | v1.36.12 |
| protoc-gen-go-grpc | v1.6.2 |

Regenerate from this directory after a change to `ingest.proto`:

```
cd gateway/ingest
PATH="$(go env GOPATH)/bin:$PATH" protoc -I . --go_out=paths=source_relative:. --go-grpc_out=paths=source_relative:. ingest.proto
```

The two plugins are installed at the pinned versions with:

```
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
```

A regeneration of an unchanged `ingest.proto` must leave both `.pb.go`
files byte for byte identical; confirm that with `git status` before
committing a change.

## Out of scope

mTLS/UDS transport authentication; the NATS publisher worker and the
process wiring that binds the target verifier and the publisher gate; the
`MAINTENANCE` session and the OpenSession evidence import (a lost
CommitSwitch or Resume reply is recovered through the durable receipt from
an ordinary session).

One limit matters for recovery: `OpenSession` does not apply the
`retired_prefix` of its request yet. `RecoveryRelease` needs the gateway's
retired prefix to be exactly one below the recovered sequence, so after a
real store loss it works for sequence 1 on its own; a higher sequence needs
the producer's prefix adopted as the baseline of the recovered store first.

A second limit comes from the session gate: its admission barrier is one
store-wide number, while seals and resumes are kept per producer and run.
A gateway that serves several runs at different admission generations fences
the sessions of every run when one of them seals or resumes. One run per
gateway is the model these RPCs were checked against.
