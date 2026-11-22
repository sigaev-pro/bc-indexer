# Gateway ingest RPC

`gateway/ingest` is the E03-02.rpc_streaming output: the gRPC control wire
of the gateway ingest boundary, on top of the [gateway
journal](../journal/README.md) (E03-01.gateway_store) and the [session
fencing](../session/README.md) boundary (E03-02.fencing).

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

## Rules held by the server

- **Fenced mutating boundary.** Every mutating RPC validates the session
  header (protocol version 2, session token, connection and admission
  generation, binding digest, producer/run identity) and then runs the
  journal mutation inside `session.Gate.Mutate` — the token check happens
  together with the commit under the gate's serialized ordering. After
  `FenceAdmission` (the SealGeneration/Resume barrier) a stale RPC answers
  `SESSION_STALE`/`RECONNECT` and creates no state.
- **Typed errors.** Answers carry the registry's `{code, request_id,
  owner_state, retry_action, detail}`; the gRPC status is only a transport
  categorization. Journal errors map one-to-one (OFFSET_MISMATCH with the
  expected cursor, IMMUTABLE_CONFLICT, ALREADY_RETIRED, MODE_CONFLICT,
  LIMIT_INCOMPATIBLE, ...); unknown errors become STORE_FATAL, never a
  silent success.
- **Bounds.** One RPC including its wrapper is at most 256 KiB
  (`GRPCServerOptions` sets the gRPC receive bound; larger messages are
  refused with ResourceExhausted at the transport level); one data chunk is
  at most 64 KiB (`LIMIT_INCOMPATIBLE`).
- **IMPORT_SEALED** imports the original seed (issuer epoch preserved, no
  sequence allocated) and seals only through `SealImport`;
  `FinalizePublish`/`SealImport` cannot be mixed.
- **Before CLOSED** the manifest digest, the declared layout digest and the
  completeness of every frame are validated by the journal.

## Versions

Go 1.27.2, gRPC-Go 1.84.0, protobuf-go 1.36.12 (COMPONENT_STACK). Regenerate
the bindings with protoc + protoc-gen-go/protoc-gen-go-grpc at those pinned
versions:

```
protoc -I . --go_out=paths=source_relative:. --go-grpc_out=paths=source_relative:. ingest.proto
```

Out of scope for this block: mTLS/UDS transport authentication, the
NATS-facing publisher, GetBatchState pagination and the Seal/Switch/Resume
maintenance RPCs (E03-03 and later).
