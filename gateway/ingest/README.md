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
| `SealGeneration` | seals an exhausted admission generation; answers `SealAck`, fences the sessions of that generation and closes data admission of the run |
| `PrepareSwitch` | checks the desired destination online, then durably prepares the switch; answers `SwitchPrepared`; the old binding stays active and data admission stays closed |
| `CommitSwitch` | re-checks the target, then durably activates the prepared binding; answers `SwitchCommitted` with the same identity |
| `Resume` | opens the next admission generation of a sealed run, the only one that admits data from then on; answers `ResumeAck` |

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
- **Data waits for the Resume.** The boundary above also checks the data
  RPCs, the Prepare and Publish steps: a data RPC runs only in a generation
  that is open for data. A run without a seal is open in every generation. A
  sealed run is closed in every generation until a Resume opens exactly the
  next one, and open in that one alone. While a destination switch is
  prepared and not committed, no run of the store is open. A refused data
  RPC answers `DRAIN_REQUIRED`/`RECONNECT` and creates no state. The journal
  holds the rule (`CheckDataAdmission`), so it holds across a gateway
  restart. The control RPCs of a drain (`GetBatchState`, `Release`,
  `RecoveryRelease`, `PrepareSwitch`) are not data and keep answering.
  Without this rule a batch created between a seal and its Resume could
  never be acknowledged or released: the publisher refuses a batch of a
  binding that is no longer active, and an unreleasable batch would hold the
  next seal.
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
- **A repeated call returns the durable receipt and writes nothing.** A
  retry of a Release, a SealGeneration, a PrepareSwitch, a CommitSwitch or a
  Resume with the same intent answers what the first call committed and
  never commits twice. The whole intent is compared, the entire old seal of
  a Resume included. The same identity with another intent, or a new
  identity over an unfinished one, is `IMMUTABLE_CONFLICT`.
- **GetBatchState answers only what is durable.** The `IngestAck` is
  returned only while the `NATS_ACKED` marker exists (`NATS_ACKED` and
  `RELEASING`). A page holds at most 1024 frame cursors in (ordinal,
  fragment, frame) order; when more follow, `next_cursor` is the last cursor
  of the page and the next request continues after it. A read is never a
  permission to delete producer payload.
- **Release is ordered and binds the whole identity.** Only the next batch
  of the run releases, it must be `NATS_ACKED`, and both the manifest and
  the plan digest must match. The order is the one of the contract's release
  row: the publisher barrier goes up first, then the `RELEASING` marker is
  committed, then the bounded cleanup runs and the final commit advances
  the retired prefix. The journal decides whether a release may start before
  the barrier is raised, so a refused release never fences a batch that
  still publishes. A crash resumes from the remaining keys, and a retry for
  an already retired sequence answers the durable prefix again.
- **RecoveryRelease is for a batch the store lost.** It needs exactly the
  next sequence, no local record, and an assertion that names the same
  batch, plan and binding as the saved `IngestAck` of the active
  destination. The accepted assertion is durable.
- **SealGeneration and Resume fence in the same step as their commit**
  (`Gate.MutateRaise`): no RPC of the sealed generation can commit between
  the two. The request comes from the session of the sealed generation, or
  on a retry from a session of the next one. A session of any other
  generation is refused a retry, although the durable receipt would answer
  it.
- **A sealed admission generation opens no session.** `OpenSession` answers
  `GENERATION_CONFLICT` for it. The admission barrier lives in the gateway
  process, so after a restart the durable seal is what refuses it; the
  retry of the seal and its Resume come from the next generation.
- **The switch runs between the seal and the Resume.** `PrepareSwitch`
  verifies the desired destination online before anything durable, and the
  first `CommitSwitch` verifies it again before the activation. A failed or
  unreachable check never produces `SwitchPrepared` or a commit. An already
  committed switch answers its receipt without the target. The verifier is
  bound with `SetTargetVerifier`; without one both steps answer
  `TRANSIENT_UNAVAILABLE`, because the check is mandatory and is never
  skipped. The Resume of the seal waits for the commit: under a prepared
  switch it is `MODE_CONFLICT`.
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
| Missing or invalid header; zero or wrong-width identity; unknown batch; an assertion that names another batch, plan, binding or switch | `IDENTITY_MISMATCH` | `RETRY_BACKOFF` |
| Stale, unknown or short session token | `SESSION_STALE` | `RECONNECT` |
| Header generations differ from the session; a session of a sealed admission generation; a seal or resume retry from a session of another generation; not the next admission generation | `GENERATION_CONFLICT` | `RECONNECT` |
| Descriptor generation is not exactly the next one; another binding generation than the active one at a seal | `GENERATION_CONFLICT` | `FIX_CONFIG` |
| Binding digest differs from the store; an acknowledgement of another destination; a descriptor of another destination line or network; a Resume that names another binding; a failed target check | `BINDING_MISMATCH` | `FIX_CONFIG` |
| Release out of order or of a batch that is not `NATS_ACKED`; RecoveryRelease that is not the next sequence; no prepared switch to commit; descriptor equals the active binding; Resume under a prepared switch | `MODE_CONFLICT` | `RETRY_BACKOFF` |
| Switch or Resume without a sealed generation; a switch over a seal that was already resumed | `DRAIN_REQUIRED` | `DRAIN_OLD_BUILD` |
| A data RPC of a run that is sealed and not resumed into the session's generation, or that arrives while a switch is prepared | `DRAIN_REQUIRED` | `RECONNECT` |
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
- A data RPC refused during a drain answers `DRAIN_REQUIRED` with
  `RECONNECT`: the producer comes back in the generation the Resume opened.
  Within one gateway process the admission barrier refuses the same RPC with
  `SESSION_STALE`; the seal alone cannot answer after a restart, so the
  durable rule names the cause.
- A seal at highwater 0 is accepted for a run that never created a batch:
  the contract fixes zero as the empty frontier. This diverges from the
  reference engine in `core/delivery/maintenance`, which rejects a zero
  highwater. A run that never produced a batch must still be able to drain
  and resume, so the journal follows the contract.
- `OutboxAssertion` and `BindingCommitAssertion` are fixed versioned
  messages (`schema_version` 1). The gateway checks that they agree with
  the rest of the request; it cannot inspect the producer's disk.
- The session that prepared a switch stays bound to the old binding, so
  `CommitSwitch` admits that old digest for the exact open switch and the
  retry after the commit reaches the durable receipt.
- A switch record is an open transition while its seal is the current one.
  The reference engine in `core/delivery/maintenance` keeps the committed
  phase for the lifetime of its store, which allows one switch only; the
  journal treats a committed switch of an earlier seal as history, so a
  second destination change is possible.
- The contract orders the cutover as seal, PrepareSwitch, CommitSwitch, then
  Resume. A prepared switch always belongs to the current seal, because the
  Resume, and with it every later seal, is refused while a switch is
  prepared.

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
OpenSession evidence import.

## Known limits

- **One live run per gateway.** The admission barrier of the session gate
  is one store-wide number, while seals and resumes are kept per producer
  and run. When one run seals or resumes, the sessions of every other run
  go stale, so a gateway can serve one live run at a time. The journal
  holds the seal and resume records per run; the gate does not.
- **MAINTENANCE sessions are refused on the real wire.** `OpenSession`
  answers `MODE_CONFLICT` for the MAINTENANCE mode, so acceptance cases
  AC-DLV-11-03, AC-DLV-11-06 and AC-DLV-11-07 are not met on the wire. A
  lost `CommitSwitch` or `Resume` reply is recovered today through the
  durable receipt from an ordinary session of the right generation, not
  through a MAINTENANCE session. Follow-up: the wire half of the backlog
  tasks E03-03.maintenance (AC-DLV-11-06, AC-DLV-11-07) and E05-03.drain
  (AC-DLV-11-03), which owns the acceptance cases and whose modules stop at
  the gateway boundary.
- **The producer's retired prefix is not adopted.** `OpenSession` does not
  apply the `retired_prefix` of its request. `RecoveryRelease` needs the
  gateway's retired prefix to be exactly one below the recovered sequence,
  so after a real store loss it works for sequence 1 on its own; a higher
  sequence needs the producer's prefix adopted as the baseline of the
  recovered store first.
- **The switch needs a bound target verifier.** Without one both switch
  steps answer `TRANSIENT_UNAVAILABLE`; no production verifier is wired in
  this module.
