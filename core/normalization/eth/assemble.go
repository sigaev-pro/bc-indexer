package eth

import (
	"errors"
	"fmt"
	"sort"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// Assembly errors.
var (
	// ErrAssembleShape reports a structurally invalid transaction
	// observation.
	ErrAssembleShape = errors.New("eth: invalid transaction observation")
)

// ScopeOp is one observed rollback-scope operation in native order: a
// scope creation (with the nearest enclosing parent and the proven owner
// frame) or a final direct rollback mark.
type ScopeOp struct {
	Kind   ScopeOpKind
	Parent uint64
	// OwnerFrame is the proven owner of an enter op; nil keeps the link
	// absent. The value 0 names the root frame.
	OwnerFrame *uint64
	// Scope is the scope a mark op rolls back.
	Scope uint64
}

// ScopeOpKind selects the scope operation.
type ScopeOpKind uint8

// Scope operations.
const (
	// ScopeEnter creates the next observed native scope.
	ScopeEnter ScopeOpKind = iota + 1
	// ScopeMarkRollback records the final direct rollback of one scope.
	ScopeMarkRollback
)

// TxOpKind selects one observed native operation of the shared
// execution_order counter, in the order native performed them.
type TxOpKind uint8

// Transaction operations of the observation walk.
const (
	// OpGasPurchase is the upfront gas purchase, always before the root
	// frame entry.
	OpGasPurchase TxOpKind = iota + 1
	// OpFrameEntry is the entry of frame FrameIndex.
	OpFrameEntry
	// OpTransfer is the value transfer invocation TransferIndex, observed
	// after the frame entry that caused it.
	OpTransfer
	// OpSelfDestruct is the SELFDESTRUCT opcode SelfDestructIndex.
	OpSelfDestruct
	// OpGasReturn is the monetary return of unused gas after execution.
	OpGasReturn
	// OpFeeCredit is the fee recipient credit.
	OpFeeCredit
	// OpFinalizationBurn is the EIP-6780 finalization burn of
	// SelfDestructIndex, at its actual finalization position.
	OpFinalizationBurn
)

// TxOp is one observed operation position of the transaction.
type TxOp struct {
	Kind            TxOpKind
	FrameIndex      uint64
	TransferIndex   uint64
	SelfDestructIdx uint64
}

// TxObservation is one fully observed transaction: everything the
// normalizer needs, in native order, with the observed before/after
// points. It is the typed form of what the import path carries; a native
// adapter produces it from capture frames.
type TxObservation struct {
	Inclusion      Inclusion
	NativeType     uint32
	RawEncodings   []RawEncoding
	Projection     EvmMessage
	Receipt        Receipt
	ReplaceSource  bool
	SourceEncoding SourceReceiptEncoding

	ScopeOps []ScopeOp

	Frames       []FrameInput
	Transfers    []TransferObservation
	SelfDestruct []SelfDestructObservation
	Fee          FeeObservation

	Ops []TxOp

	State          []AccountObservation
	Authorizations []AuthorizationTuple
	AuthOutcomes   []AuthorizationTupleOutcome
}

// AssembleTx normalizes one observed transaction into the stored record.
// It walks the observed operations in native order and allocates the one
// shared execution_order counter at each boundary: the gas purchase, every
// frame entry, every transfer invocation, every SELFDESTRUCT (whose
// monetary projection shares the number through its cause), the gas
// return, the fee credit and each finalization burn. It then finalizes the
// scope tree, applies the commit view to the frames, builds the state diff
// and the receipt, aggregates the fee summary and validates the whole
// record.
//
// The same observation always assembles into the same record: the frame
// ids, action indexes, execution orders, scope ids and cause references
// are functions of the observation alone, which is what capture recovery
// relies on (test/native_faults/eth: a replayed observation must restore
// the same logical identities, not continue the counters).
func AssembleTx(obs TxObservation) (*TransactionRecord, error) {
	order := NewOrder()
	actions := &ActionIndex{}
	protocols := &ProtocolIndex{}
	feeActions := make([]balance.BalanceAction, 0, 8)
	var allActions []balance.BalanceAction
	execActions := make([]ExecutionAction, 0)
	frameOrders := make([]uint64, len(obs.Frames))
	usedFrame := make([]bool, len(obs.Frames))
	usedTransfer := make([]bool, len(obs.Transfers))
	usedSelfDestruct := make([]bool, len(obs.SelfDestruct))

	scopeBuilder := NewScopeBuilder()
	for _, op := range obs.ScopeOps {
		switch op.Kind {
		case ScopeEnter:
			owner := op.OwnerFrame
			if _, err := scopeBuilder.Enter(op.Parent, owner); err != nil {
				return nil, err
			}
		case ScopeMarkRollback:
			if err := scopeBuilder.MarkRolledBack(op.Scope); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: scope op %d", ErrAssembleShape, uint8(op.Kind))
		}
	}
	scopes, err := scopeBuilder.Finalize()
	if err != nil {
		return nil, err
	}

	for _, op := range obs.Ops {
		switch op.Kind {
		case OpGasPurchase:
			a, err := BuildGasPurchaseAction(order, actions, protocols, obs.Fee)
			if err != nil {
				return nil, err
			}
			feeActions = append(feeActions, a)
			allActions = append(allActions, a)
		case OpFrameEntry:
			if op.FrameIndex >= uint64(len(obs.Frames)) {
				return nil, fmt.Errorf("%w: frame entry %d of %d", ErrAssembleShape, op.FrameIndex, len(obs.Frames))
			}
			if usedFrame[op.FrameIndex] {
				return nil, fmt.Errorf("%w: frame %d entered twice", ErrAssembleShape, op.FrameIndex)
			}
			entry, err := order.Allocate()
			if err != nil {
				return nil, err
			}
			usedFrame[op.FrameIndex] = true
			frameOrders[op.FrameIndex] = entry
		case OpTransfer:
			if op.TransferIndex >= uint64(len(obs.Transfers)) {
				return nil, fmt.Errorf("%w: transfer %d of %d", ErrAssembleShape, op.TransferIndex, len(obs.Transfers))
			}
			if usedTransfer[op.TransferIndex] {
				return nil, fmt.Errorf("%w: transfer %d twice", ErrAssembleShape, op.TransferIndex)
			}
			usedTransfer[op.TransferIndex] = true
			transfer := &obs.Transfers[op.TransferIndex]
			// The commit view of a transfer: a scope rollback cancels it
			// even when the observation kept the local flag.
			if RolledBack(scopes, transfer.Scope) {
				transfer.Committed = false
			}
			a, err := BuildTransferAction(order, actions, *transfer)
			if err != nil {
				return nil, err
			}
			allActions = append(allActions, a)
		case OpSelfDestruct:
			if op.SelfDestructIdx >= uint64(len(obs.SelfDestruct)) {
				return nil, fmt.Errorf("%w: selfdestruct %d of %d", ErrAssembleShape, op.SelfDestructIdx, len(obs.SelfDestruct))
			}
			if usedSelfDestruct[op.SelfDestructIdx] {
				return nil, fmt.Errorf("%w: selfdestruct %d twice", ErrAssembleShape, op.SelfDestructIdx)
			}
			usedSelfDestruct[op.SelfDestructIdx] = true
			selfDestruct := &obs.SelfDestruct[op.SelfDestructIdx]
			if RolledBack(scopes, selfDestruct.Scope) {
				selfDestruct.RolledBack = true
			}
			exec, proj, err := BuildSelfDestruct(order, actions, uint64(len(execActions))+1, *selfDestruct)
			if err != nil {
				return nil, err
			}
			execActions = append(execActions, exec)
			if proj != nil {
				allActions = append(allActions, *proj)
			}
		case OpGasReturn:
			a, err := BuildGasReturnAction(order, actions, protocols, obs.Fee)
			if err != nil {
				return nil, err
			}
			if a != nil {
				feeActions = append(feeActions, *a)
				allActions = append(allActions, *a)
			}
		case OpFeeCredit:
			a, err := BuildFeeCreditAction(order, actions, protocols, obs.Fee)
			if err != nil {
				return nil, err
			}
			feeActions = append(feeActions, a)
			allActions = append(allActions, a)
		case OpFinalizationBurn:
			if op.SelfDestructIdx >= uint64(len(obs.SelfDestruct)) {
				return nil, fmt.Errorf("%w: finalization burn of selfdestruct %d", ErrAssembleShape, op.SelfDestructIdx)
			}
			a, err := BuildFinalizationBurn(order, actions, protocols, uint64(op.SelfDestructIdx)+1, obs.SelfDestruct[op.SelfDestructIdx])
			if err != nil {
				return nil, err
			}
			if a != nil {
				allActions = append(allActions, *a)
			}
		default:
			return nil, fmt.Errorf("%w: tx op %d", ErrAssembleShape, uint8(op.Kind))
		}
	}
	for i, used := range usedFrame {
		if !used {
			return nil, fmt.Errorf("%w: frame %d has no entry op", ErrAssembleShape, i)
		}
	}

	frames, err := BuildCallsWithOrders(obs.Frames, frameOrders)
	if err != nil {
		return nil, err
	}
	frames, err = ApplyCommitView(frames, scopes)
	if err != nil {
		return nil, err
	}

	stateDiff, err := BuildStateDiff(obs.State)
	if err != nil {
		return nil, err
	}

	summary, err := FeeSummaryOf(feeActions)
	if err != nil {
		return nil, err
	}
	receipt := obs.Receipt
	receipt.FeeSummary = summary
	receipt.NetworkDetails = NetworkDetails{Eth: &EthReceiptDetails{}}
	if obs.ReplaceSource {
		receipt.Source = Some(obs.SourceEncoding)
	} else {
		source, err := SourceEncoding(&receipt, obs.NativeType)
		if err != nil {
			return nil, err
		}
		receipt.Source = Some(source)
	}
	if err := VerifySourceEncoding(sourceOf(&receipt), &receipt); err != nil {
		return nil, err
	}

	authorizations, err := NormalizeAuthorizationList(obs.Authorizations)
	if err != nil {
		return nil, err
	}
	sortActionsByOrder(allActions)
	record := &TransactionRecord{
		Inclusion:             obs.Inclusion,
		NativeType:            obs.NativeType,
		RawEncodings:          obs.RawEncodings,
		Projection:            obs.Projection,
		Receipt:               &receipt,
		Calls:                 frames,
		ExecActions:           execActions,
		Actions:               allActions,
		RollbackScope:         scopes,
		StateChanges:          stateDiff,
		Authorizations:        authorizations,
		AuthorizationOutcomes: obs.AuthOutcomes,
		CaptureStatus:         CaptureCaptured,
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	return record, nil
}

// sourceOf returns the retained source encoding of a receipt that carries
// one.
func sourceOf(r *Receipt) SourceReceiptEncoding {
	s, _ := r.Source.Get()
	return s
}

// sortActionsByOrder stores the action list in ascending execution_order,
// the storage order of D74.
func sortActionsByOrder(actions []balance.BalanceAction) {
	sort.Slice(actions, func(i, j int) bool {
		return actions[i].ExecutionOrder < actions[j].ExecutionOrder
	})
}
