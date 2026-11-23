# core/normalization/eth — full Ethereum transaction semantics (E04)

Owner tasks: E04-01.calls, E04-01.receipts, E04-01.authorization,
E04-01.block_record, E04-03.state_diff, E04-01.selfdestruct,
E04-03.balance_actions, E04-04.import_pressure and E04-02.optional_scope
(#41-#49, ETH family). See
../../.project/docs/TASK_DECOMPOSITION.md
(the E04 rows) and
../../.project/docs/DEVELOPMENT_PLAN.md.

Stdlib-only Go module (Go 1.25) composing the repository-local E02
packages: protocol/balance,
protocol/block,
protocol/profiles,
protocol/semantic and
core/capture. No native runs, no I/O outside the
interfaces the caller provides; every fixture value cites its E01 evidence
file.

## Why this package exists (deep module)

One native Ethereum block decision must become normalized records with one
shared execution_order counter, one rollback-scope tree, byte-exact receipt
consensus encodings and causal monetary actions — before READY. The seam is
`AssembleTx(TxObservation) (*TransactionRecord, error)` plus the collector
pair `Transport`/`Collector`. Callers — the native adapter and the tests —
learn those two interfaces. Behind the seam this package hides:

- the D74 order/scope/cause rules across the call tree, execution actions
  and balance actions, including the one allowed shared order;
- the D75 receipt encoding registry and the D76 bounded byte-equivalent
  consensus writer and streaming verifier;
- the D81 attempted-versus-created address distinction;
- the EIP-7702 source-authorization preservation and the envelope check
  that structurally cannot filter tuples;
- the ETH block-record projection with source timestamp precision and
  fork-field presence;
- the transaction-boundary state diff with storage-reset semantics;
- SELFDESTRUCT with opcode, monetary movement and deletion kept separate
  under EIP-6780 fork rules;
- the fee flow (purchase, return, recipient credit) as causal actions plus
  the aggregate-only fee summary;
- the D17 optional outside-tx scope, default OFF without any loss of
  native transaction effects.

The deletion test: without this package, every consumer of native
observations would re-derive these rules and they would drift.

## Package contents

| File | Contents |
|---|---|
| doc.go | Package documentation and the shared type aliases (`Address20`, `UInt256`, `Hash32`, `Optional`) |
| order.go | `Order`: the single tx-local execution_order counter (D74), 1-based, checked overflow |
| scope.go | `ScopeBuilder`: scope 0 as the included-tx boundary, ordered scope creation, transitive rollback propagation |
| calls.go | `CallFrame`, `BuildCalls`/`BuildCallsWithOrders`, D81 rules, `ApplyCommitView`, frame validation |
| receipt.go | `Receipt`, `Log`, the ETH network-details variant, `SourceEncoding` (D75 kinds 1/2 via protocol/profiles), `VerifySourceEncoding` |
| rlp.go | Canonical RLP primitives: the bounded two-pass writer and the view-based reader with width rules |
| authorization.go | `AuthorizationTuple` preservation, the isolation-checked external envelope, designation-code cross-check |
| blockrecord.go | `BuildBlockRecord`: the native BlockApplied projection, header mapping, ETH seconds timestamp, BlockApplied descriptor |
| statediff.go | `AccountChange`/`StorageChange`, `BuildStateDiff`, storage-reset rules, `CodeOf`, boundary balances |
| selfdestruct.go | `SelfDestructAction` execution action, opcode-position projection, `BuildFinalizationBurn` |
| actions.go | Fee observation and the three fee builders, transfer actions, `FeeSummaryOf` |
| assemble.go | `TxObservation`/`AssembleTx`: the native-order walk over all builders into one validated record |
| record.go | `TransactionRecord`, whole-record validation, execution projection, raw encodings, tx descriptor |
| optionalscope.go | `ScopeDeclaration` (D17, default OFF), `SystemBalanceRecord`, optional-entity descriptor |
| wire.go, wire_convert.go | The import wire (JSON events plus keyed blobs) and both conversion directions |
| transport.go | `Transport`: the producer side over core/capture, streaming large fields chunk by chunk |
| collector.go | `Collector`: the bounded worker — one field in flight, streaming blob spool, digest verification |

## Design decisions

1. **One counter, walked in native order.** `AssembleTx` walks the
   observed operation list (`TxOp`) and allocates the shared
   `execution_order` at each native boundary: gas purchase, every frame
   entry, every transfer invocation, every SELFDESTRUCT (whose monetary
   projection reuses the number through `cause=ExecutionAction`), the gas
   return, the fee credit and each finalization burn. `BuildCalls`
   (sequential) exists for frame-only observations; the interleaved form
   passes pre-allocated orders. The corpus test pins the concrete counter
   of transaction (1,0): purchase 1, root frame 2, transfer 3, credit 4.
2. **Rollback scopes own the commit view.** Scope 0 is the
   included-transaction boundary and is never rolled back for a published
   included tx; the root EVM execution lives in a child scope. `Finalize`
   propagates ancestor rollback to every descendant (a successful child
   does not survive an ancestor rollback) while a caught child revert
   never marks the parent or independent later scopes. `ApplyCommitView`
   and the transfer/selfdestruct builders derive their committed flags
   from the finished scope list; local outcomes are never rewritten.
3. **D81 in the types.** `attempted_create_address` is an observation
   input kept even when the attempt failed (including a zero address the
   native computation produced); `created_address` exists only on a
   locally successful creation kind, and the builder derives attempted
   from created only in that direction. Final existence is read from
   `AccountChange`.
4. **D76 shape: size pass, then stream.** The consensus writer computes
   the exact RLP payload size without building bytes, then streams header
   and fields to the sink (one small header slice per item). The verifier
   walks views of the retained bytes with canonical width rules —
   self-encoded small scalars, `0x80` for zero, no leading zeros, no
   trailing bytes — and cross-checks every consensus field against the
   typed receipt. The corpus no-log receipt is asserted byte-for-byte
   against a hand-computed `f9 0108 01 825208 b90100 … c0`.
5. **Authorization preservation is structural.** `NormalizeAuthorizationList`
   returns the source list (defensive copy, order and duplicates intact);
   `ValidateExternalEnvelope` receives an envelope view with no
   authorization field at all, so envelope validation cannot filter
   tuples. Per-tuple execution outcomes are cross-checked against the
   boundary account: an applied tuple's authority carries
   `ef0100 || delegate` (the type-path oracle), and its nonce advances —
   even when the transaction's EVM execution reverts.
6. **The block record is exact for empty and nonempty blocks alike.** The
   count is the observed value (0 is not an absence), the timestamp keeps
   the ETH source precision (whole seconds, nanos 0), fork fields carry
   presence from the native header (a present zero stays present), and
   `header.source_rlp` is mandatory.
7. **The state diff is final state only.** Boundary points decide rows:
   the reverted child account of the corpus nested-revert transaction
   leaves no row (its transient slot-0 write was inside the reverted
   scope), fees and the final nonce survive a root revert, a 100 → 110 →
   100 tour leaves no row (the movements stay in the actions), and a
   storage reset covers key removal without enumeration while post-reset
   writes stay explicit.
8. **SELFDESTRUCT splits three facts.** The execution action keeps the
   opcode even at balance zero; the monetary projection carries only the
   observed native mutations (zero balance or a post-Cancun
   self-beneficiary no-op yields a kept, zero, effect-less projection);
   deletion commits only when the action's effects survived, the fork
   rules made it eligible and the boundary change observed the account
   gone. Finalization burns run at their own order as
   `SELFDESTRUCT_FINALIZATION_BURN` with the related execution action.
9. **Fees are actions; the summary is an aggregate.** The three fee
   builders emit GAS_PURCHASE, GAS_RETURN (only for a nonzero leftover)
   and FEE_RECIPIENT_CREDIT at scope 0, committed even on root revert;
   `FeeSummaryOf` aggregates them, and the corpus numbers (42000000000000
   purchase, 23625000000000 tip, 285310000000000 net of (1,2)) are pinned
   in tests. The base-fee burn is a fee-rule fact verified separately; no
   extra payer debit is invented.
10. **The import path is bounded by the capture seam.** `Transport`
    streams large values as keyed `TagBytes` capture fields; `Collector`
    keeps exactly one field in flight, streams blob chunks into the
    `ByteStore`, verifies the whole-field digest and the content key of
    every reference, and releases each frame as soon as its payload is
    consumed — so the capture budget bounds the in-flight memory while the
    assembled record retains what the contract requires it to retain.
    Records assembled through the collector are deep-equal to the direct
    `AssembleTx` results (native results unchanged), a slow sink
    backpressures the producer through the budget, and a mid-run failure
    followed by a replay reproduces the identical logical identities
    (the crash-recovery property of test/native_faults/eth).
11. **D17 is a declaration, not a flag.** The default declaration is OFF:
    outside-tx observations (the corpus 2 ETH block reward) produce no
    records and the transaction records are unaffected. Enabling requires
    named typed kinds; an observation of an undeclared kind is an explicit
    error, never a silent widening. The published unit is the
    `SystemBalanceRecord` with its optional-entity descriptor (body count
    plus one plus native order); no v1 `TransactionProtocol` operation is
    invented for ETH outside-tx movements.

## Evidence anchors (fixtures)

Every fixture literal cites its source; nothing was run natively:

- test/native_corpus/expected/eth.json — blocks 1 (4 txs) and 2 (empty),
  the fee rows, the boundary states, the storage slots, the block reward;
- test/native_corpus/eth/probe.go — the corpus contracts, addresses,
  chain id 31337;
- test/type_paths/eth/ORACLE.md and probe.go — types 0-4, the EIP-7702
  authority/delegate, `ef0100` designation, nonces;
- test/native_faults/eth — the crash-recovery replay property.

`TestCorpusEvidenceCrossCheck` additionally compares the embedded anchors
against the retained oracle file when the checkout carries it.

## Reproducibility

`AssembleTx` is a pure function of the observation: two replays of the
same observation produce deep-equal records (frame ids, action indexes,
execution orders, scope ids, cause references). The wire round trip is
checked in both directions, and the RLP writer output is verified by the
reader on every assembled receipt.

## Checks

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean;
- `go test ./...` and `go test -race ./...` — 64 tests, all passing;
- `go test -cover` — 78.9% of statements (the remainder is defensive
  error branching of the wire decoders);
- no Docker, no native runs (D96 boundaries intact).

## Open questions

1. **Post-Cancun finalization credit to a surviving beneficiary.** EIP-6780
   moves a surviving selfdestructed contract's funds to the beneficiary at
   the end of the transaction. The closed v1 `TransactionProtocol` registry
   has no operation for that position (the burn operation is debit-only).
   `BuildFinalizationBurn` fails closed with `ErrSelfDestructRegistry`
   instead of inventing a fallback; representing it needs a versioned
   registry decision.
2. **Family-shared model location.** TASK_DECOMPOSITION names
   family-shared paths (`core/normalization/calls`, `…/state`, and so on)
   while the three families are implemented in parallel worktrees. The
   `CallFrame`, `ExecutionAction`, `StateDiff` and receipt types defined
   here mirror the contract; whether they lift into a shared module or
   stay per-family with a later unification is a merge-time decision.
3. **Durable import checkpoints.** The collector is deterministic across
   replays, but durable checkpointing of a partially consumed stream
   (resume without re-emitting) belongs to the E03/E05 safe-point seam;
   the current contract only requires the replay property.
4. **Wire versioning.** The import wire (tags, JSON shapes, blob layout)
   is v1 and closed by `jsonUnmarshalStrict`; a second consumer besides
   the ETH adapter would justify extracting it next to protocol/codec.
5. **Native code hashes.** Fixture code hashes use a stand-in hash; the
   native adapter owns the real pinned-client code hash values.
