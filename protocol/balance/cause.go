package balance

import (
	"fmt"
	"slices"
)

// CauseKind selects the branch of the closed cause union (D62): a monetary
// action is caused by an execution frame, by an execution action such as
// SELFDESTRUCT, or by an observed transaction-protocol operation.
type CauseKind uint8

const (
	// CauseUnspecified is the invalid zero value.
	CauseUnspecified CauseKind = iota
	// CauseFrame points at the CallFrame whose entry or execution performed
	// the native transfer invocation.
	CauseFrame
	// CauseExecutionAction points at the exact execution action (currently
	// SELFDESTRUCT) whose monetary projection this action is.
	CauseExecutionAction
	// CauseTransactionProtocol points at an observed protocol operation from
	// the closed v1 registry below.
	CauseTransactionProtocol
)

// String returns the registry name of the cause branch.
func (k CauseKind) String() string {
	switch k {
	case CauseFrame:
		return "Frame"
	case CauseExecutionAction:
		return "ExecutionAction"
	case CauseTransactionProtocol:
		return "TransactionProtocol"
	default:
		return fmt.Sprintf("CAUSE_INVALID(%d)", uint8(k))
	}
}

// ProtocolOperation is the closed v1 registry of observed transaction
// protocol operations that can cause a balance action (D62). The zero value
// is invalid; unknown native paths must not be published as an OTHER-style
// fallback.
type ProtocolOperation uint8

const (
	// OpUnspecified is the invalid zero value.
	OpUnspecified ProtocolOperation = iota
	// OpGasPurchase is the upfront gas purchase: the payer is debited for the
	// full gas limit at the effective gas price.
	OpGasPurchase
	// OpGasReturn is the return of unused gas to the payer after execution.
	OpGasReturn
	// OpFeeRecipientCredit is a fee credit to a native recipient (tip, or a
	// family-defined burn recipient such as the Polygon zero address).
	OpFeeRecipientCredit
	// OpBSCSystemExecution is a BSC native system execution path.
	OpBSCSystemExecution
	// OpBorStateSyncExecution is a Polygon Bor state-sync execution path.
	OpBorStateSyncExecution
	// OpSelfDestructFinalizationBurn is the EIP-6780 finalization burn of a
	// self-destructed contract; it runs at its own position, not at the
	// SELFDESTRUCT opcode position.
	OpSelfDestructFinalizationBurn
)

// String returns the registry name of the operation.
func (o ProtocolOperation) String() string {
	switch o {
	case OpGasPurchase:
		return "GAS_PURCHASE"
	case OpGasReturn:
		return "GAS_RETURN"
	case OpFeeRecipientCredit:
		return "FEE_RECIPIENT_CREDIT"
	case OpBSCSystemExecution:
		return "BSC_SYSTEM_EXECUTION"
	case OpBorStateSyncExecution:
		return "BOR_STATE_SYNC_EXECUTION"
	case OpSelfDestructFinalizationBurn:
		return "SELFDESTRUCT_FINALIZATION_BURN"
	default:
		return fmt.Sprintf("OPERATION_INVALID(%d)", uint8(o))
	}
}

// IsFeeOperation reports whether the operation belongs to the tx-level fee
// flow (purchase, return, recipient credit). Fee actions always live at the
// non-rolled-back transaction boundary scope 0 (D74 rule 7).
func (o ProtocolOperation) IsFeeOperation() bool {
	switch o {
	case OpGasPurchase, OpGasReturn, OpFeeRecipientCredit:
		return true
	default:
		return false
	}
}

// Cause is the closed cause union of a balance action (D62). Exactly one
// branch is meaningful, selected by Kind; the other fields are ignored and
// must be left zero. Equal defines cause identity: two actions with the same
// cause refer to the same native operation boundary.
type Cause struct {
	// Kind selects the union branch.
	Kind CauseKind
	// FrameID is the CallFrame reference for CauseFrame.
	FrameID uint64
	// ExecutionActionID is the ExecutionAction reference for
	// CauseExecutionAction.
	ExecutionActionID uint64
	// Operation is the registry operation for CauseTransactionProtocol.
	Operation ProtocolOperation
	// OperationIndex is the tx-local index of the observed protocol
	// invocation for CauseTransactionProtocol. It is a separate counter: it
	// is neither execution_order nor the native body position.
	OperationIndex uint64
	// RelatedExecutionActions links a protocol operation to the execution
	// actions it finalizes; currently used by
	// OpSelfDestructFinalizationBurn.
	RelatedExecutionActions []uint64
}

// FrameCause builds a frame cause.
func FrameCause(frameID uint64) Cause {
	return Cause{Kind: CauseFrame, FrameID: frameID}
}

// ExecutionActionCause builds an execution-action cause. This is the only
// cause that permits a shared execution_order between an execution action and
// its monetary projection (D74).
func ExecutionActionCause(executionActionID uint64) Cause {
	return Cause{Kind: CauseExecutionAction, ExecutionActionID: executionActionID}
}

// ProtocolCause builds a transaction-protocol cause.
func ProtocolCause(op ProtocolOperation, operationIndex uint64, relatedExecutionActions ...uint64) Cause {
	var related []uint64
	if len(relatedExecutionActions) > 0 {
		related = append(related, relatedExecutionActions...)
	}
	return Cause{
		Kind:                    CauseTransactionProtocol,
		Operation:               op,
		OperationIndex:          operationIndex,
		RelatedExecutionActions: related,
	}
}

// Equal reports cause identity. A nil and an empty related-action list are
// the same absence of links.
func (c Cause) Equal(o Cause) bool {
	if c.Kind != o.Kind {
		return false
	}
	switch c.Kind {
	case CauseFrame:
		return c.FrameID == o.FrameID
	case CauseExecutionAction:
		return c.ExecutionActionID == o.ExecutionActionID
	case CauseTransactionProtocol:
		return c.Operation == o.Operation &&
			c.OperationIndex == o.OperationIndex &&
			slices.Equal(c.RelatedExecutionActions, o.RelatedExecutionActions)
	default:
		return false
	}
}

// String renders the cause for diagnostics, using registry names.
func (c Cause) String() string {
	switch c.Kind {
	case CauseFrame:
		return fmt.Sprintf("Frame(%d)", c.FrameID)
	case CauseExecutionAction:
		return fmt.Sprintf("ExecutionAction(%d)", c.ExecutionActionID)
	case CauseTransactionProtocol:
		return fmt.Sprintf("TransactionProtocol{%s, %d, %v}", c.Operation, c.OperationIndex, c.RelatedExecutionActions)
	default:
		return c.Kind.String()
	}
}
