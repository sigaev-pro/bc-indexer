# BSC (Parlia) normalization module

`core/normalization/bsc` is the E04 BSC-family module: it normalizes the
captured observations of included BSC body transactions and blocks into the
typed record views of the shared phase-1 models, with the full BSC
transaction semantics — the nine E04 areas plus the Parlia-specific ones —
in one module. It is a pure normalization layer: it consumes typed observed
facts, performs no I/O, never re-executes anything, and never rebuilds
consensus bytes from normalized fields.

The module is one Go module with one package per area:

| Package | Area | What it owns |
|---|---|---|
| `.` (root) | classification, system semantics, record assembly | The `Ordinary \| ParliaSystem` classification over the actual wire type, the Parlia system-call registry with the independently expected transitions, the observation input model, the normalizer, the record and its validation and digest |
| `calls` | E04-01.calls | The full call tree: frames including failed and reverted calls, the shared `execution_order`, the D81 attempted/created address distinction |
| `receipt` | E04-01.receipts | The typed receipt, the D75 source encoding selection (kinds 1/2, BSC types 0..4), the fee summary aggregate, and the explicitly present empty `BscReceiptDetails` variant |
| `authorization` | E04-01.authorization | The closed empty EIP-7702 profile: the sub-package exists, the current BSC profile accepts no tuples |
| `block` | E04-01.block_record | The BSC block projection: millisecond timestamps (`Time*1000 + low_u64(MixDigest)`), per-fork optional presence, source RLP, the Parlia system-transaction count observation |
| `state` | E04-03.state_diff | The committed transaction-boundary diff: accounts by raw address, storage by raw key with one entry per key, `storage_reset`, existence flags, code bytes exactly when code changed |
| `selfdestruct` | E04-01.selfdestruct | The SELFDESTRUCT execution action: opcode, monetary movement and deletion as separate facts |
| `balance` | E04-03.balance_actions | The action builder over observed mutation groups: gas purchase, gas return, fee recipient credit (the `0xff..fe` accumulator), transfers, SELFDESTRUCT projections, BSC system execution, finalization burns |
| `importpath` | E04-04.import_pressure | The streaming full-import path with a bounded in-flight window, slow-worker backpressure, and capture-recovery prefix verification by record digest |
| `optionalscope` | E04-02.optional_scope | The outside-tx scope declaration: default OFF, the Parlia fee sweep as the one declared BSC outside-tx operation |

## Design

### Input model

`Observation` (root package) is the seam between the E01 capture spool and
normalization: the envelope facts (typed effective message with
provenance, raw wire type and bytes), the ordered native event stream
(snapshot lifecycle, frame entries and exits, mutation groups,
SELFDESTRUCTs, finalization burns), the receipt facts (including the exact
spooled consensus bytes and the native receipt type), and the raw account
boundary rows. Every value is an owned copy; the normalizer shares no
memory with the native side. Mutation groups arrive grouped by their
native integration operation identity with the pinned role already
resolved — the grouping is never guessed from amounts or adjacency.

### Normalization pipeline

`Normalizer.Normalize` folds one observation in one deterministic pass:

1. The closed authorization profile accepts the tuple list (empty for the
   current BSC profile; any tuple is an explicit error).
2. The classifier applies the pinned criterion: system recipient AND zero
   effective gas price AND recovered sender equal to the block coinbase.
   Sender recovery errors propagate. The wire type, signature and payload
   are preserved; the classification assigns no new type id.
3. The event walk allocates the single tx-local `execution_order` (D74)
   over the order-bearing operations and builds the rollback-scope tree:
   scope 0 is the transaction boundary, scopes 1..N are allocated in the
   creation order of the observed native snapshots, a repeated native id
   after a revert or end creates a new scope, and an ancestor rollback
   marks every descendant. Frame exits and snapshot lifecycle steps are
   bookkeeping and allocate no order.
4. After the walk, the final rolled-back facts resolve the commit flags:
   fee actions live committed at scope 0; a frame's effects commit only
   when it succeeded locally and its exit scope survived; a SELFDESTRUCT
   and its monetary projection share one order only through
   `cause=ExecutionAction`.
5. The receipt keeps the spooled consensus bytes under the D75 kind
   selected by the actual native type (checked against the included wire
   type after the BSC actualTx substitution), aggregates the fee summary
   from the actions, and carries the mandatory, explicitly empty
   `BscReceiptDetails`.
6. The boundary rows normalize into the committed diff (raw-byte ordering,
   one storage entry per key with the tx-initial/final value, reset
   semantics).
7. The record validates: call tree, scope tree, the shared
   `protocol/balance` structural validator, receipt, diff, the
   classification consistency of the root frame (NATIVE_PROTOCOL for
   ParliaSystem, EVM for Ordinary), and — for ParliaSystem calls of a
   registered kind — the independent transition expectations from the
   committed boundary points.

### Parlia system transactions

A Parlia system transaction is a full body transaction: it keeps its
actual wire type (legacy in the retained evidence, never an invented
system type), a NATIVE_PROTOCOL root frame at a registered source
execution boundary, and its monetary effects under the closed
`BSC_SYSTEM_EXECUTION` protocol cause with kind `NATIVE_PROTOCOL_CHANGE`
(no invented economic shape). Its fee summary stores applicable explicit
zeros with an absent payer.

The registered system-call kinds and their independently expected
transitions (from the retained private-import oracle):

- Every system transaction: the sender nonce rises by exactly one and the
  sender balance falls by exactly the transaction value.
- `e1c7392a` legacy `init()` and `8129fc1c` Feynman `initializeContracts`:
  storage transitions only, no balance moves.
- `f340fa01` deposit: value split by the observed contract fractions
  (deployed default 625/10000 reward, 1000/10000 burn) with the rest to
  the validator set contract.
- `1e4c1524` validator-set update (election): the validator set contract
  balance empties into the reward contract (the dusk transfer).
- Unregistered selectors keep their real effects; only the generic sender
  rules apply and no economic expectation is invented.

The Parlia fee sweep — the accumulator `0xff..fe` to validator move that
runs immediately before the deposit transaction's boundary — is outside
every transaction. It is never forced into a transaction record; the
`balance` builder rejects a sweep group and the `optionalscope`
declaration governs its publication (default OFF drops it without an
error and without touching any transaction record).

### Import pressure and recovery

`importpath.Runner` streams a block import through the normalizer with a
bounded in-flight window: each observation is normalized alone and its
record emitted before the next slot frees, so collector memory stays
bounded regardless of block size, and the normalized output is identical
for every window size. A slow sink back-pressures the whole import. The
retained fixture order puts user transactions before the Parlia system
transactions of a block; an ordinary record after the system boundary is
rejected as scope mixing. Capture recovery re-normalizes the same corpus
from scratch and compares the record digests prefix-wise — nothing
continues counters after already repeated operations.

### Optional scope

Default OFF, no declared fixtures. Turning the outside-tx scope on
requires a declared typed fixture; the Parlia fee sweep is the one
registered BSC feature. Outside-tx records keep their own order space and
never borrow a transaction identity.

## Anchors

Normative documents (main checkout):

- `.project/docs/MESSAGE_CONTRACT.md` — sections 3 (TransactionRecord,
  CallFrame, ExecutionAction, AccountChange, D74 order rules), 3.1
  (D75-D76 source receipt encoding), 3.2 (receipt network details:
  BscReceiptDetails), 4 (BlockApplied), 7 (optional families)
- `.project/docs/CURRENT_SEMANTICS.md` — D62 (balance actions and fees),
  D74 (operation order and rollback scopes), BSC timestamp rule
- `.project/docs/TASK_DECOMPOSITION.md` — E04-01..E04-04 rows
- `.project/docs/TRANSACTION_TYPE_REGISTRY.md` — BSC/Parlia section:
  classification criterion, native Finalize/applyTransaction capture path

Retained E01 evidence (read-only inputs):

- `test/native_corpus/bsc/` — the smoke corpus: the fee accumulator as
  tip recipient, no burn on the Istanbul path, nested-revert and
  root-revert shapes, empty block, Parlia system transactions appended by
  `AssembleBlock`
- `test/type_paths/bsc/full_import/` — the coherent private import:
  `ledger.py` (absolute balance/nonce model, the sweep, the distribution
  fractions), `judge.py` (`check_system`: selector registry, expected
  nonce/balance/storage transitions), `ORACLE.md`, `native_test.go`
  (system contract set, fixture order)
- `adapters/bsc/type_mapping/paths.json` — the classification wording
  and the pinned source commit
- `protocol/balance`, `protocol/block`, `protocol/profiles` — the shared
  phase-1 models this module produces and validates against

## Open questions

- Post-Kepler fee split: the retained corpus path credits the full fee to
  the accumulator; the burn fractions of later forks need retained native
  vectors before the fee model gains a second credit shape.
- Unregistered system selectors (slash paths, direct StakeHub
  interactions) have no independent distribution expectation yet; each
  needs its own retained oracle before it moves out of
  `SystemCallUnregistered`.
- The SELFDESTRUCT finalization burn is built and validated structurally;
  an independent expectation (which accounts may burn at finalization
  under the pinned forks) needs retained evidence.
- The mapping from the E01 capture spool frames onto `Observation` is the
  adapter convergence task of E05; the seam types here are the stable
  contract for it.
- The per-fork system-contract set and the receipt consensus-byte
  streaming writer (D76 bounded output) are profile/codec-layer inputs;
  this module consumes both as given.

## Commands

```sh
cd core/normalization/bsc
gofmt -l .
go vet ./...
go test -race ./...
```
