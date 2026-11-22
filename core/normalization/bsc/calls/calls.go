// Package calls is the execution-frame area of the BSC normalization
// module (E04-01.calls).
//
// It owns the full call tree of one BSC transaction: every executed frame
// including failed and reverted calls, the single tx-local execution_order
// shared with execution actions and balance actions (D74), and the D81
// distinction between an attempted contract address and a created contract
// address. It validates the tree shape and produces the frame presence
// view that the shared balance validator consumes.
//
// Normative anchors:
//
//   - .project/docs/CURRENT_SEMANTICS.md, "Block, calls and account
//     changes" (CallFrame fields, D81 attempted/created distinction) and
//     section D74 (operation order and rollback scopes)
//   - .project/docs/MESSAGE_CONTRACT.md, section 3 (CallFrame row of the
//     TransactionRecord table)
//   - adapters/bsc/type_mapping/paths.json: every body type reaches
//     StateProcessor.Process -> ApplyTransactionWithEVM, or the Parlia
//     system path; a Parlia system transaction is a NATIVE_PROTOCOL frame
//     at a registered source execution boundary, never a fake EVM call
package calls

import (
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// Sentinel errors of the call tree validator. Each names one rule of the
// contract; none is silently tolerated.
var (
	ErrNoRootFrame           = errors.New("bsc/calls: an executed transaction always has its root frame")
	ErrFrameIDDuplicate      = errors.New("bsc/calls: duplicate frame id")
	ErrRootParent            = errors.New("bsc/calls: the root frame has no parent")
	ErrParentUnknown         = errors.New("bsc/calls: parent frame reference does not exist")
	ErrParentOrder           = errors.New("bsc/calls: parent entry must precede child entry")
	ErrRootDepth             = errors.New("bsc/calls: the root frame has depth 0")
	ErrDepthMismatch         = errors.New("bsc/calls: depth must be parent depth plus one")
	ErrListUnsorted          = errors.New("bsc/calls: frames must be stored in ascending execution_order")
	ErrOrderZero             = errors.New("bsc/calls: execution_order starts at 1")
	ErrOrderDuplicate        = errors.New("bsc/calls: duplicate frame entry execution_order")
	ErrCommittedNonSuccess   = errors.New("bsc/calls: only a locally successful frame may have committed effects")
	ErrCreatedNotSuccess     = errors.New("bsc/calls: created_address requires a locally successful creation")
	ErrCreatedWithoutAttempt = errors.New("bsc/calls: created_address requires attempted_create_address")
	ErrNativeProtocolKind    = errors.New("bsc/calls: a NATIVE_PROTOCOL frame uses the NATIVE_PROTOCOL kind")
	ErrEvmKind               = errors.New("bsc/calls: an EVM frame uses an EVM call kind")
	ErrCreateTarget          = errors.New("bsc/calls: a creation frame has no target address")
	ErrCallTargetMissing     = errors.New("bsc/calls: a call frame carries a target address")
	ErrCallerMissing         = errors.New("bsc/calls: caller_address is mandatory on a frame")
)

// Kind is the closed frame kind of MESSAGE_CONTRACT section 3.
type Kind uint8

// Frame kinds. Zero is not a valid kind.
const (
	KindInvalid Kind = iota
	KindCall
	KindCallCode
	KindDelegateCall
	KindStaticCall
	KindCreate
	KindCreate2
	KindNativeProtocol
)

var kindNames = [...]string{"INVALID", "CALL", "CALLCODE", "DELEGATECALL", "STATICCALL", "CREATE", "CREATE2", "NATIVE_PROTOCOL"}

// String returns the contract name of k.
func (k Kind) String() string {
	if k == 0 || int(k) >= len(kindNames) {
		return fmt.Sprintf("KIND_INVALID(%d)", uint8(k))
	}
	return kindNames[k]
}

// IsCreation reports whether k is a contract-creation kind.
func (k Kind) IsCreation() bool { return k == KindCreate || k == KindCreate2 }

// Outcome is the local execution outcome of one frame.
type Outcome uint8

// Outcomes. Zero is not a valid outcome.
const (
	OutcomeInvalid Outcome = iota
	OutcomeSuccess
	OutcomeRevert
	OutcomeFailure
)

var outcomeNames = [...]string{"INVALID", "SUCCESS", "REVERT", "FAILURE"}

// String returns the contract name of o.
func (o Outcome) String() string {
	if o == 0 || int(o) >= len(outcomeNames) {
		return fmt.Sprintf("OUTCOME_INVALID(%d)", uint8(o))
	}
	return outcomeNames[o]
}

// Frame is one execution frame of the call tree. It keeps caller_address
// (native from), context_address, code_address, msg_sender and target as
// separate fields: one must never substitute for another under
// DELEGATECALL or delegated code. A nil pointer is an absent field; the
// schema never replaces absence with a zero address.
type Frame struct {
	// FrameID is the tx-local frame identity. The root frame has id 0.
	FrameID uint64
	// ParentID is the parent frame; absent on the root frame.
	ParentID *uint64
	// ExecutionOrder is the entry position of the frame in the single
	// tx-local counter of D74, shared with execution actions and balance
	// actions. A frame entry marks the entry, not the whole execution
	// interval; returning from a call creates no new frame.
	ExecutionOrder uint64
	// Depth is the call depth; the root frame has depth 0.
	Depth uint64
	// Engine is the execution engine of the frame: EVM, or NATIVE_PROTOCOL
	// for a registered native protocol boundary such as a Parlia system
	// transaction execution.
	Engine balance.Engine
	// Kind is the closed frame kind.
	Kind Kind
	// CallerAddress is the native sender of the message.
	CallerAddress *balance.Address20
	// ContextAddress is the execution context account.
	ContextAddress *balance.Address20
	// CodeAddress is the account whose code executes.
	CodeAddress *balance.Address20
	// MsgSender is the msg.sender seen by the executing code.
	MsgSender *balance.Address20
	// Target is the message target for call kinds; creation kinds carry no
	// target and keep their attempt in the creation fields.
	Target *balance.Address20
	// Value is the message value; nil when the kind carries none.
	Value *balance.UInt256
	// Input and Output are the untruncated message input and output.
	Input  []byte
	Output []byte
	// Gas and GasUsed are the gas limit and gas consumed, when applicable.
	Gas     *uint64
	GasUsed *uint64
	// Outcome is the local execution outcome.
	Outcome Outcome
	// EffectsCommitted is the final applicability of the frame's effects
	// after rollback: an ancestor rollback cancels a locally successful
	// frame, and a successful child never survives a reverted ancestor.
	EffectsCommitted bool
	// ErrorCode is the UTF-8 native diagnostic in the profile namespace,
	// when the native source produced one.
	ErrorCode *string
	// RevertData is the revert payload, kept separately from output.
	RevertData []byte
	// AttemptedCreateAddress is the address the native computation
	// designated for a creation attempt. Per D81 it comes from the observed
	// native computation or entry: an early native refusal may return a
	// zero address after the attempt address was already known, and an
	// address returned together with an error after constructor execution
	// does not prove local success. It stays present for failed attempts.
	AttemptedCreateAddress *balance.Address20
	// CreatedAddress is present only when the creation succeeded locally.
	// Final existence is a separate fact observed in the account changes.
	CreatedAddress *balance.Address20
}

// Tree is the ordered call tree of one transaction, stored in ascending
// execution_order.
type Tree struct {
	Frames []Frame
}

// Refs returns the frame presence view for the shared balance validator.
func (t Tree) Refs() []balance.FrameRef {
	refs := make([]balance.FrameRef, 0, len(t.Frames))
	for _, f := range t.Frames {
		refs = append(refs, balance.FrameRef{FrameID: f.FrameID, ExecutionOrder: f.ExecutionOrder})
	}
	return refs
}

// Root returns the root frame of the tree.
func (t Tree) Root() *Frame {
	for i := range t.Frames {
		if t.Frames[i].FrameID == 0 {
			return &t.Frames[i]
		}
	}
	return nil
}

// Validate checks the tree shape against the contract: exactly one root
// frame with id 0, depth 0 and no parent; every parent reference exists,
// entered before the child, one depth below it; frames stored in ascending
// unique execution_order starting at 1; committed effects only on a
// locally successful frame; created_address only on a locally successful
// creation that also carries its attempt; engine/kind pairing; and the
// target presence rules of creation and call kinds.
func (t Tree) Validate() error {
	if t.Root() == nil {
		return ErrNoRootFrame
	}
	var errs []error
	byID := make(map[uint64]*Frame, len(t.Frames))
	for i := range t.Frames {
		f := &t.Frames[i]
		if other, ok := byID[f.FrameID]; ok {
			errs = append(errs, fmt.Errorf("frames %d and %d: %w", other.FrameID, f.FrameID, ErrFrameIDDuplicate))
			continue
		}
		byID[f.FrameID] = f
	}
	prevOrder := uint64(0)
	seenOrders := make(map[uint64]bool, len(t.Frames))
	for i := range t.Frames {
		f := &t.Frames[i]
		if f.ExecutionOrder == 0 {
			errs = append(errs, fmt.Errorf("frame %d: %w", f.FrameID, ErrOrderZero))
		} else if seenOrders[f.ExecutionOrder] {
			errs = append(errs, fmt.Errorf("frame %d order %d: %w", f.FrameID, f.ExecutionOrder, ErrOrderDuplicate))
		}
		seenOrders[f.ExecutionOrder] = true
		if i > 0 && f.ExecutionOrder < prevOrder {
			errs = append(errs, fmt.Errorf("frame %d order %d: %w", f.FrameID, f.ExecutionOrder, ErrListUnsorted))
		}
		prevOrder = f.ExecutionOrder
	}
	root := t.Root()
	if root.ParentID != nil {
		errs = append(errs, ErrRootParent)
	}
	if root.Depth != 0 {
		errs = append(errs, fmt.Errorf("root depth %d: %w", root.Depth, ErrRootDepth))
	}
	for i := range t.Frames {
		f := &t.Frames[i]
		if f.CallerAddress == nil {
			errs = append(errs, fmt.Errorf("frame %d: %w", f.FrameID, ErrCallerMissing))
		}
		if f.ParentID == nil {
			if f.FrameID != 0 {
				errs = append(errs, fmt.Errorf("frame %d: %w", f.FrameID, ErrRootParent))
			}
			continue
		}
		parent, ok := byID[*f.ParentID]
		if !ok {
			errs = append(errs, fmt.Errorf("frame %d parent %d: %w", f.FrameID, *f.ParentID, ErrParentUnknown))
			continue
		}
		if parent.ExecutionOrder >= f.ExecutionOrder {
			errs = append(errs, fmt.Errorf("frame %d parent %d: %w", f.FrameID, *f.ParentID, ErrParentOrder))
		}
		if f.Depth != parent.Depth+1 {
			errs = append(errs, fmt.Errorf("frame %d depth %d: %w", f.FrameID, f.Depth, ErrDepthMismatch))
		}
	}
	for i := range t.Frames {
		f := &t.Frames[i]
		if f.EffectsCommitted && f.Outcome != OutcomeSuccess {
			errs = append(errs, fmt.Errorf("frame %d outcome %s: %w", f.FrameID, f.Outcome, ErrCommittedNonSuccess))
		}
		if f.CreatedAddress != nil {
			if f.Outcome != OutcomeSuccess {
				errs = append(errs, fmt.Errorf("frame %d outcome %s: %w", f.FrameID, f.Outcome, ErrCreatedNotSuccess))
			}
			if f.AttemptedCreateAddress == nil {
				errs = append(errs, fmt.Errorf("frame %d: %w", f.FrameID, ErrCreatedWithoutAttempt))
			}
		}
		switch f.Engine {
		case balance.EngineNativeProtocol:
			if f.Kind != KindNativeProtocol {
				errs = append(errs, fmt.Errorf("frame %d kind %s: %w", f.FrameID, f.Kind, ErrNativeProtocolKind))
			}
		case balance.EngineEVM:
			if f.Kind == KindInvalid || f.Kind == KindNativeProtocol {
				errs = append(errs, fmt.Errorf("frame %d kind %s: %w", f.FrameID, f.Kind, ErrEvmKind))
			}
		default:
			errs = append(errs, fmt.Errorf("frame %d engine %s: %w", f.FrameID, f.Engine, ErrEvmKind))
		}
		if f.Kind.IsCreation() {
			if f.Target != nil {
				errs = append(errs, fmt.Errorf("frame %d kind %s: %w", f.FrameID, f.Kind, ErrCreateTarget))
			}
		} else if f.Target == nil {
			errs = append(errs, fmt.Errorf("frame %d kind %s: %w", f.FrameID, f.Kind, ErrCallTargetMissing))
		}
	}
	return errors.Join(errs...)
}
