# Session fencing

`gateway/session` is the E03-02.fencing output: session, admission and
publisher fencing for the gateway
([DELIVERY_STATE_MACHINE, "Fencing"](../../.project/docs/DELIVERY_STATE_MACHINE.md)).

## Model

- `Token{Epoch, SessionID}` — one authenticated session bound to the epoch
  (admission or connection generation) that admitted it. Session ids are
  crypto-random and never logged.
- `Barrier{epoch}` — invalidates every token of an older epoch
  (`Invalidates`).
- `Gate` — the serialized ordering point shared by session switches and the
  mutations they guard:
  - `Open(epoch)` admits a session (below the current barrier:
    `ErrGenerationConflict`); `Revoke` closes one session.
  - `Raise(epoch)` puts a monotonic barrier (SealGeneration, Resume,
    Release). Tokens of older epochs become `ErrSessionStale`.
  - `Check(token)` validates; `Mutate(token, fn)` validates **and runs fn
    under the same mutex**, so the token check happens together with the
    commit, not only when the RPC is read. A barrier raised after `Mutate`
    returned cannot undo `fn`; a barrier raised before it starts prevents
    `fn` from running at all.
- `PublisherGate` — fences the internal publisher worker by
  `publisher_generation`: PubAck callbacks carry
  `{batch_id, plan_digest, generation}`. `NewGeneration()` and
  `Release(batch)` put barriers; a stale callback (`ErrCallbackStale`) or a
  callback for a released batch (`ErrBatchReleased`) never creates state.
  Duplicates of still-valid callbacks stay allowed: already-sent network
  bytes cannot be recalled, and the journal answers them idempotently.

The package keeps no durable state: `boot_nonce` and session lifetimes are
per process by contract, so a restart fences every old token implicitly.
mTLS/UDS authentication of the handshake is the transport layer's job
(E03-03); this package is the ordering and invalidation core. The ingest RPC
boundary (`gateway/ingest`) wraps every mutating journal call in
`Gate.Mutate`.
