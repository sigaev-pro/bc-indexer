package poly

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// CallFrame is the normalized execution frame of MESSAGE_CONTRACT section 3.
// The five address roles stay separate fields: substituting one for another
// under DELEGATECALL or delegated code is a normalization error, and the
// model never rewrites a source role.
type CallFrame struct {
	FrameID        uint64
	ParentID       *uint64
	ExecutionOrder uint64
	Depth          uint64
	Engine         balance.Engine
	Kind           FrameKind
	CallerAddress  *Address20
	ContextAddress *Address20
	CodeAddress    *Address20
	MsgSender      *Address20
	Target         *Address20
	Value          *UInt256
	Input          []byte
	Output         []byte
	Gas            *uint64
	GasUsed        *uint64
	Outcome        Outcome
	// EffectsCommitted is the final applicability of this frame's effects:
	// a locally successful child inside a rolled back ancestor is not
	// committed, and local success alone never proves final existence.
	EffectsCommitted bool
	// ErrorCode is the native diagnostic string in the profile namespace,
	// present only when the native source produced one.
	ErrorCode *string
	// RevertData is the REVERT payload, separate from Output.
	RevertData []byte
	// AttemptedCreateAddress is the CREATE/CREATE2 attempt address as the
	// native computation determined it (D81): it is present even when the
	// attempt failed, and it is never derived by checking a returned
	// address for zero.
	AttemptedCreateAddress *Address20
	// CreatedAddress is present only on a locally successful creation; the
	// final existence of the account is read from AccountChange.
	CreatedAddress *Address20
}

// normalizeCalls validates the captured frames and builds the normalized
// call list in native entry order. FrameID and the shared execution order
// are assigned here; the root frame is id 0 at depth 0 with no parent.
func normalizeCalls(tx *TxCapture, orders *txOrders) ([]CallFrame, error) {
	if len(tx.Frames) == 0 {
		return nil, fmt.Errorf("an executed transaction always has its root frame: %w", ErrCapture)
	}
	out := make([]CallFrame, len(tx.Frames))
	for i, f := range tx.Frames {
		if f.Parent >= i {
			return nil, fmt.Errorf("frame %d parent %d is not an earlier frame: %w", i, f.Parent, ErrCapture)
		}
		if f.Parent >= 0 {
			parent := &tx.Frames[f.Parent]
			if f.Depth != parent.Depth+1 {
				return nil, fmt.Errorf("frame %d depth %d does not follow parent depth %d: %w",
					i, f.Depth, parent.Depth, ErrCapture)
			}
		} else if f.Depth != 0 {
			return nil, fmt.Errorf("frame %d has no parent but depth %d: %w", i, f.Depth, ErrCapture)
		}
		if i == 0 && f.Parent != -1 {
			return nil, fmt.Errorf("root frame has parent %d: %w", f.Parent, ErrCapture)
		}
		if i > 0 && f.Parent == -1 {
			return nil, fmt.Errorf("frame %d has no parent: %w", i, ErrCapture)
		}
		if f.Outcome == OutcomeUnspecified {
			return nil, fmt.Errorf("frame %d outcome is unspecified: %w", i, ErrCapture)
		}
		// Engine and kind form the closed pairs of the registry.
		isNative := f.Kind == FrameNativeProtocol
		if isNative != (f.Engine == balance.EngineNativeProtocol) {
			return nil, fmt.Errorf("frame %d kind %s with engine mismatch: %w", i, f.Kind, ErrCapture)
		}
		switch f.Kind {
		case FrameCreate, FrameCreate2:
			if f.CreatedAddress != nil && f.Outcome != OutcomeSuccess {
				return nil, fmt.Errorf("frame %d created an address with outcome %s: %w", i, f.Outcome, ErrCapture)
			}
			if f.CreatedAddress != nil && f.AttemptedCreateAddress == nil {
				return nil, fmt.Errorf("frame %d created an address without an attempt address: %w", i, ErrCapture)
			}
			if f.Target != nil {
				return nil, fmt.Errorf("create frame %d carries a target: %w", i, ErrCapture)
			}
		case FrameDelegateCall, FrameCallCode, FrameStaticCall, FrameCall:
			if f.AttemptedCreateAddress != nil || f.CreatedAddress != nil {
				return nil, fmt.Errorf("call frame %d carries create addresses: %w", i, ErrCapture)
			}
		case FrameNativeProtocol:
			if f.AttemptedCreateAddress != nil || f.CreatedAddress != nil {
				return nil, fmt.Errorf("native protocol frame %d carries create addresses: %w", i, ErrCapture)
			}
		default:
			return nil, fmt.Errorf("frame %d kind is unspecified: %w", i, ErrCapture)
		}
		// Local outcome rules for the final committed flag.
		if f.Outcome != OutcomeSuccess && f.Committed {
			return nil, fmt.Errorf("frame %d outcome %s is committed: %w", i, f.Outcome, ErrCapture)
		}
		if f.Parent >= 0 && tx.Frames[f.Parent].Outcome == OutcomeSuccess && !tx.Frames[f.Parent].Committed && f.Committed {
			return nil, fmt.Errorf("frame %d is committed inside an uncommitted parent: %w", i, ErrCapture)
		}

		cf := CallFrame{
			FrameID:                uint64(i),
			ExecutionOrder:         orders.frames[i],
			Depth:                  f.Depth,
			Engine:                 f.Engine,
			Kind:                   f.Kind,
			CallerAddress:          f.CallerAddress,
			ContextAddress:         f.ContextAddress,
			CodeAddress:            f.CodeAddress,
			MsgSender:              f.MsgSender,
			Target:                 f.Target,
			Value:                  f.Value,
			Input:                  f.Input,
			Output:                 f.Output,
			Gas:                    f.Gas,
			GasUsed:                f.GasUsed,
			Outcome:                f.Outcome,
			EffectsCommitted:       f.Committed,
			ErrorCode:              f.ErrorCode,
			RevertData:             f.RevertData,
			AttemptedCreateAddress: f.AttemptedCreateAddress,
			CreatedAddress:         f.CreatedAddress,
		}
		if f.Parent >= 0 {
			parentID := uint64(f.Parent)
			cf.ParentID = &parentID
		}
		out[i] = cf
	}
	// The root frame of an included ordinary transaction: a locally
	// successful root is committed (a root revert is represented by a child
	// scope, never by scope 0).
	root := &tx.Frames[0]
	if root.Parent != -1 || root.Depth != 0 {
		return nil, fmt.Errorf("root frame shape: %w", ErrCapture)
	}
	if root.Outcome == OutcomeSuccess && !root.Committed {
		return nil, fmt.Errorf("successful root frame is not committed: %w", ErrCapture)
	}
	return out, nil
}
