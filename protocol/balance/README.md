# balance

Owner task: E02-05.actions_model (#23): the typed model of native monetary
actions for ETH, BSC and Polygon, the common execution_order, cause equality
and the RollbackScope tree with its validator. Inputs: the retained E01-03
smoke corpus effects, MESSAGE_CONTRACT section 3 and CURRENT_SEMANTICS
sections D74 (order and scopes) and D62 (balance actions and fees); see
.project/docs/TASK_DECOMPOSITION.md and
.project/evidence/E02-parallel-plan-20261121.md. CONTEXT.md fixes the terms
"execution_order" and "RollbackScope".

## Package contents

| File | Contents |
|---|---|
| types.go | Address20, UInt256 (exact big-endian arithmetic, no floats), Asset |
| cause.go | Closed cause union Frame / ExecutionAction / TransactionProtocol, protocol operation registry v1, cause equality |
| action.go | BalanceAction, BalanceEffect, Engine, ActionKind |
| scope.go | RollbackScope, scope 0 as the included-transaction boundary |
| summary.go | FeeSummary as the aggregate receipt view |
| validate.go | Structural validator over one transaction's balance view (TxBalances) |
| reconcile.go | Fixture oracle: committed effects vs transaction-boundary points |

The package is record-side only. Capture, wire field numbers, proto bindings
and the RecordFragment codec belong to E04 and E02-02; the full CallFrame and
ExecutionAction models belong to E04-01. This package sees them through the
minimal presence views FrameRef and ExecutionActionRef.

## Model summary

- One tx-local `execution_order` counter for calls, execution actions and
  balance actions, starting at 1, continuous 1..N. Equal orders across lists
  are legal only for an execution action and the balance action whose cause
  names that exact execution action (D74). Frame-caused value transfers get
  their own order after the frame entry: cause=Frame is causality, not order
  equality.
- `RollbackScope={scope_id, parent_scope_id?, owner_frame_id?, rolled_back}`:
  scope 0 is the included-transaction boundary (never rolled back for a
  published included transaction); the root execution lives in its own nested
  child scope; ids 1..N are allocated in creation order with parent < child;
  an ancestor rollback propagates to every descendant; a caught child revert
  never marks the parent.
- `BalanceAction` with a closed cause, closed kind, optional from/to, exact
  UInt256 amount, asset NATIVE, explicit rollback scope reference (absent and
  0 stay distinct), final effects_committed and the address effects list with
  per-effect before/after points.
- Fee actions (GAS_PURCHASE, GAS_RETURN, FEE_RECIPIENT_CREDIT) always live at
  scope 0 and stay committed: fees and nonce that survive a root revert are
  never attributed to a rolled-back scope.
- The receipt `FeeSummary` is an aggregate view: every field must equal the
  exact aggregate of the fee actions, so a summary can never add a second
  movement and actions whose effects cancel never disappear.

## Validator

`Validate(TxBalances)` enforces, with one sentinel error per violation class:
scope list ordering and continuity; scope 0 shape; parent existence,
parent < child, cycles, proven owner frames; rollback propagation; action
list storage order and action_index uniqueness; order validity, duplicates,
gaps and false equality across lists; cause references and the closed
kind/operation/engine pairing; effect indexes, per-address chaining and the
kind-specific shapes (two-sided transfers, single payer effect for the gas
purchase, single credit effects); the fee scope rule; and the aggregate
equality of the fee summary. The violation classes map to acceptance
criterion AC-DAT-05-03 (duplicate/gap/overflow order, false equality, foreign
cause/owner/scope refs, scope cycle, absent instead of 0, committed effect
in a reverted scope).

`ReconcileBoundary` is the fixture oracle of D62: the sum of committed
address effects must equal the boundary change of every touched account.
It is a test oracle over retained fixtures, not a production completeness
verifier.

## Fixtures

The tests carry reference effects fixtures transcribed from the retained
E01-03 smoke corpus (read-only, main checkout): all four ETH user
transactions, all three BSC user transactions and all four Polygon user
transactions of block 1, covering fees (legacy and EIP-1559), plain value
transfers, a caught nested revert with its three-node scope tree and a root
revert whose fees survive at scope 0. Corpus provenance:

```
test/native_corpus/expected/eth.json  sha256 4c6ded51914d675da6fe71c90232f893bf42a0d1620d888e891132cb00332e29
test/native_corpus/expected/bsc.json  sha256 939ee929b410ab0b637a2e8dc6fedff2db9ef53ebbfa0cc027de65cc02c3ba58
test/native_corpus/expected/poly.json sha256 6ec2a6325ab26441e13a8433ac4378d4322e07d5864e19533e4cf3c3f64ac53b
```

Every fixture literal is a corpus value; the builder derives intermediate
points with exact arithmetic and the driver checks the result against the
corpus state_before/state_after and the corpus fee decomposition
(purchase = gas_limit x effective_gas_price, return = (gas_limit -
gas_used) x effective_gas_price, net = purchase - return = fee.total =
tip + burnt).

## Interpretations (within the accepted contract)

- Polygon base-fee burn: the corpus gives an explicit burn_recipient (the
  zero address) whose balance grows by exactly fee.burnt, so the burn is
  modeled as a committed FEE_RECIPIENT_CREDIT action of kind FEE to that
  address at scope 0. On ETH the burn is implicit (no address effect, no
  action; verified separately by fee rules); on BSC istanbul there is no
  burn.
- Engine mapping: frame-caused transfers, the SELFDESTRUCT projection, gas
  purchase/return, fee credits and the finalization burn run under EngineEVM;
  BSC_SYSTEM_EXECUTION and BOR_STATE_SYNC_EXECUTION run under
  EngineNativeProtocol, following the CallFrame engine rules.
- Frame-caused actions are exactly kind TRANSFER; the SELFDESTRUCT monetary
  projection is kind SELFDESTRUCT_TRANSFER with the contract as from and the
  beneficiary as to. A self-address action keeps either the chained
  debit+credit effect pair or the single collapsed net-zero point; both
  preserve the net-zero action.
- Native reason codes stay opaque here: they are interpreted only against a
  pinned profile registry (E02-10/E02-02). The fixtures use documented
  placeholder codes.
- The validator requires stored list order (scopes by scope_id, actions by
  execution_order, frames and execution actions by their ids), per the
  storage rules of MESSAGE_CONTRACT section 3.

## Open questions

Where the normative documents do not fix a detail, this package records the
question instead of guessing:

1. Zero-amount gas return. When gas_limit == gas_used there is no balance
   mutation to return. CURRENT_SEMANTICS keeps "actual" purchase, return and
   credit operations, but does not say whether a zero-credit return
   invocation is published as an action with amount 0 or omitted. The corpus
   cannot distinguish the two. This package omits the action and keeps an
   explicit zero in the fee summary; the native capture answer belongs to
   E04-03.balance_actions.
2. Relative order of the two Polygon fee credits. The corpus proves both the
   burn-recipient credit and the validator tip credit happen at the
   transaction level, but its state is order-independent, so the corpus
   cannot bind their relative execution_order and operation_index. The
   fixtures list the burn credit first and the tip second; the true native
   order must be fixed by source binding in E04-03 before the Polygon
   profile is accepted.
