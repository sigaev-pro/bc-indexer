package eth

import (
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// FrameKind is the closed kind registry of an execution frame
// (MESSAGE_CONTRACT section 3): CALL, CALLCODE, DELEGATECALL, STATICCALL,
// CREATE, CREATE2 and the registered native protocol boundary. There is no
// OTHER fallback.
type FrameKind uint8

// Frame kinds.
const (
	KindCall FrameKind = iota + 1
	KindCallCode
	KindDelegateCall
	KindStaticCall
	KindCreate
	KindCreate2
	KindNativeProtocol
)

// String returns the contract name of the kind.
func (k FrameKind) String() string {
	switch k {
	case KindCall:
		return "CALL"
	case KindCallCode:
		return "CALLCODE"
	case KindDelegateCall:
		return "DELEGATECALL"
	case KindStaticCall:
		return "STATICCALL"
	case KindCreate:
		return "CREATE"
	case KindCreate2:
		return "CREATE2"
	case KindNativeProtocol:
		return "NATIVE_PROTOCOL"
	}
	return fmt.Sprintf("FRAME_KIND_INVALID(%d)", uint8(k))
}

// Valid reports whether k is a registered kind.
func (k FrameKind) Valid() bool { return k >= KindCall && k <= KindNativeProtocol }

// Creates reports whether the kind is a contract-creation kind whose native
// entry determines an attempted create address.
func (k FrameKind) Creates() bool { return k == KindCreate || k == KindCreate2 }

// Result is the native execution outcome vocabulary shared by call frames
// and receipts: SUCCESS, REVERT, FAILURE.
type Result uint8

// Results.
const (
	ResultSuccess Result = iota + 1
	ResultRevert
	ResultFailure
)

// String returns the contract name of the result.
func (r Result) String() string {
	switch r {
	case ResultSuccess:
		return "SUCCESS"
	case ResultRevert:
		return "REVERT"
	case ResultFailure:
	}
	return "FAILURE"
}

// Valid reports whether r is a registered result.
func (r Result) Valid() bool { return r >= ResultSuccess && r <= ResultFailure }

// CallFrame is one ordered execution frame of the call tree, including
// failed and reverted calls (MESSAGE_CONTRACT section 3):
//
//	CallFrame={frame_id,parent_id?,execution_order,depth,engine,kind,
//	  caller_address?,context_address?,code_address?,msg_sender?,target?,
//	  value?,input,output,gas?,gas_used?,outcome,effects_committed,
//	  error_code?,revert_data?,attempted_create_address?,created_address?}
//
// Address roles are kept separate and are never substituted for one another
// under DELEGATECALL or delegated code: caller_address is the native from,
// context_address the frame's own context, code_address the executed code
// holder, msg_sender the message sender the code observes and target the
// native target. Applicable input/output/gas/revert data is never truncated.
type CallFrame struct {
	// FrameID is the tx-local frame identity. The root frame is 0.
	FrameID uint64
	// ParentID is absent on the root frame and present on every other
	// frame; it always references an existing frame of this transaction.
	ParentID Optional[uint64]
	// ExecutionOrder is the position of the native frame entry in the
	// shared tx-local counter (D74). Returning from a call does not create
	// a new frame, and a parent completion never moves its entry after a
	// child.
	ExecutionOrder uint64
	// Depth is the native call depth; the root frame is 0.
	Depth uint64
	// Engine mirrors the balance model engine of the frame.
	Engine balance.Engine
	// Kind is the closed frame kind.
	Kind FrameKind
	// CallerAddress is the native from of the message.
	CallerAddress *Address20
	// ContextAddress is the frame's execution context account.
	ContextAddress *Address20
	// CodeAddress is the account whose code executes (differs from the
	// context under DELEGATECALL and EIP-7702 delegation).
	CodeAddress *Address20
	// MsgSender is the sender the executed code observes (differs from the
	// caller under DELEGATECALL).
	MsgSender *Address20
	// Target is the native target of the message.
	Target *Address20
	// Value is the message value. It proves nothing about a committed
	// transfer by itself; the balance actions carry the movements.
	Value Optional[UInt256]
	// Input and Output are the applicable message input and result bytes.
	Input  []byte
	Output []byte
	// Gas is the gas limit of the frame and GasUsed the consumed gas, with
	// presence following the native observation.
	Gas     Optional[uint64]
	GasUsed Optional[uint64]
	// Outcome is the local frame outcome before rollback.
	Outcome Result
	// EffectsCommitted is the final applicability of the frame's effects
	// after rollback: local success cancelled by an ancestor scope rollback
	// becomes false. The local outcome itself is never rewritten.
	EffectsCommitted bool
	// ErrorCode is the native diagnostic string in the profile namespace,
	// present when the frame failed. It promises no cross-network enum.
	ErrorCode Optional[string]
	// RevertData is the revert payload, kept separate from the error code.
	RevertData []byte
	// AttemptedCreateAddress is the address of the creation attempt as the
	// native computation determined it (D81): it is present whenever the
	// native entry or computation fixed the address, including when the
	// attempt then failed. It is never derived by checking a returned
	// address for a non-zero value, and an error after constructor
	// execution does not demote it.
	AttemptedCreateAddress *Address20
	// CreatedAddress is present only on a locally successful creation.
	// Final existence is read from AccountChange, never from this field.
	CreatedAddress *Address20
}

// FrameInput is one observed native frame entry in native order. The first
// input is the root frame. ParentIndex is the index of the parent input
// (not the frame id) and is ignored for the root.
type FrameInput struct {
	ParentIndex  int
	Kind         FrameKind
	Engine       balance.Engine
	Caller       *Address20
	Context      *Address20
	Code         *Address20
	MsgSender    *Address20
	Target       *Address20
	Value        Optional[UInt256]
	Input        []byte
	Output       []byte
	Gas          Optional[uint64]
	GasUsed      Optional[uint64]
	LocalOutcome Result
	ErrorCode    Optional[string]
	RevertData   []byte
	// AttemptedCreate is the D81 attempted address observation. Leave nil
	// only when native never determined an address for this entry.
	AttemptedCreate *Address20
	// CreatedCreate is the locally successful creation address. Setting it
	// requires LocalOutcome SUCCESS and a creation kind; the builder
	// enforces this and never copies one field from the other.
	CreatedCreate *Address20
}

// Call errors.
var (
	// ErrCallShape reports a structurally invalid frame input list.
	ErrCallShape = errors.New("eth: invalid call tree")
	// ErrCreateAddress reports a violation of the D81 create-address rules:
	// a created address without local success, or a created address on a
	// non-creation kind.
	ErrCreateAddress = errors.New("eth: created_address requires a locally successful creation kind")
)

// BuildCalls normalizes the observed frame entries into the stored call
// list, allocating each entry its execution_order from the shared counter
// in input order. This form fits an observation whose only ordered
// operations are the frame entries; when other operations interleave with
// the entries (gas purchase before the root, transfers after entries, fee
// settlement after execution), use BuildCallsWithOrders and allocate each
// entry's order at its actual position.
func BuildCalls(order *Order, inputs []FrameInput) ([]CallFrame, error) {
	orders := make([]uint64, len(inputs))
	for i := range inputs {
		entry, err := order.Allocate()
		if err != nil {
			return nil, err
		}
		orders[i] = entry
	}
	return BuildCallsWithOrders(inputs, orders)
}

// BuildCallsWithOrders is the interleaved form: every frame entry receives
// the execution_order already allocated at its actual position in the
// shared counter. Orders must be positive and strictly increasing in
// native entry order: a parent entry always precedes its child, and a
// returning call never reorders anything.
func BuildCallsWithOrders(inputs []FrameInput, orders []uint64) ([]CallFrame, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("%w: empty input list", ErrCallShape)
	}
	if len(orders) != len(inputs) {
		return nil, fmt.Errorf("%w: %d orders for %d frames", ErrCallShape, len(orders), len(inputs))
	}
	frames := make([]CallFrame, 0, len(inputs))
	depths := make([]uint64, len(inputs))
	for i, in := range inputs {
		if !in.Kind.Valid() {
			return nil, fmt.Errorf("%w: frame %d kind %s", ErrCallShape, i, in.Kind)
		}
		if !in.LocalOutcome.Valid() {
			return nil, fmt.Errorf("%w: frame %d outcome", ErrCallShape, i)
		}
		f := CallFrame{
			FrameID:                uint64(i),
			ExecutionOrder:         orders[i],
			Engine:                 in.Engine,
			Kind:                   in.Kind,
			CallerAddress:          in.Caller,
			ContextAddress:         in.Context,
			CodeAddress:            in.Code,
			MsgSender:              in.MsgSender,
			Target:                 in.Target,
			Value:                  in.Value,
			Input:                  in.Input,
			Output:                 in.Output,
			Gas:                    in.Gas,
			GasUsed:                in.GasUsed,
			Outcome:                in.LocalOutcome,
			ErrorCode:              in.ErrorCode,
			RevertData:             in.RevertData,
			AttemptedCreateAddress: in.AttemptedCreate,
			CreatedAddress:         in.CreatedCreate,
		}
		if i == 0 {
			if in.ParentIndex != 0 {
				return nil, fmt.Errorf("%w: the root frame has no parent", ErrCallShape)
			}
			depths[0] = 0
		} else {
			if in.ParentIndex < 0 || in.ParentIndex >= i {
				return nil, fmt.Errorf("%w: frame %d parent index %d is not an earlier frame",
					ErrCallShape, i, in.ParentIndex)
			}
			parent := uint64(in.ParentIndex)
			f.ParentID = Some(parent)
			depths[i] = depths[in.ParentIndex] + 1
		}
		f.Depth = depths[i]
		// D81: created_address exists only on a locally successful creation.
		if f.CreatedAddress != nil {
			if !in.Kind.Creates() || in.LocalOutcome != ResultSuccess {
				return nil, fmt.Errorf("%w: frame %d kind %s outcome %s",
					ErrCreateAddress, i, in.Kind, in.LocalOutcome)
			}
		}
		// A successful creation implies the attempt address; the reverse
		// derivation is forbidden (D81).
		if f.AttemptedCreateAddress == nil && f.CreatedAddress != nil {
			f.AttemptedCreateAddress = f.CreatedAddress
		}
		frames = append(frames, f)
	}
	if err := ValidateCalls(frames); err != nil {
		return nil, err
	}
	return frames, nil
}

// ValidateCalls checks the stored list shape: ascending execution_order,
// root frame 0 with depth 0 and no parent, existing parents one depth
// lower, and presence rules of the stored optional fields.
func ValidateCalls(frames []CallFrame) error {
	if len(frames) == 0 {
		return fmt.Errorf("%w: empty call list", ErrCallShape)
	}
	for i := range frames {
		f := &frames[i]
		if f.FrameID != uint64(i) {
			return fmt.Errorf("%w: frame at position %d has id %d", ErrCallShape, i, f.FrameID)
		}
		if i > 0 && f.ExecutionOrder <= frames[i-1].ExecutionOrder {
			return fmt.Errorf("%w: frame %d order %d not ascending", ErrCallShape, i, f.ExecutionOrder)
		}
		if f.ExecutionOrder == 0 {
			return fmt.Errorf("%w: frame %d order 0", ErrCallShape, i)
		}
		if i == 0 {
			if f.ParentID.Present() || f.Depth != 0 {
				return fmt.Errorf("%w: the root frame carries a parent or a nonzero depth", ErrCallShape)
			}
		} else {
			parent, ok := f.ParentID.Get()
			if !ok || parent >= uint64(i) {
				return fmt.Errorf("%w: frame %d parent reference", ErrCallShape, i)
			}
			if f.Depth != frames[parent].Depth+1 {
				return fmt.Errorf("%w: frame %d depth %d does not follow its parent",
					ErrCallShape, i, f.Depth)
			}
		}
		if !f.Kind.Valid() || !f.Outcome.Valid() {
			return fmt.Errorf("%w: frame %d closed enums", ErrCallShape, i)
		}
		if f.CreatedAddress != nil && (f.Outcome != ResultSuccess || !f.Kind.Creates()) {
			return fmt.Errorf("%w: frame %d created address without local success", ErrCreateAddress, i)
		}
		if f.Outcome == ResultSuccess {
			if code, ok := f.ErrorCode.Get(); ok && code != "" {
				return fmt.Errorf("%w: frame %d carries an error code on SUCCESS", ErrCallShape, i)
			}
		}
	}
	return nil
}

// ApplyCommitView sets the final EffectsCommitted of every frame from the
// finished rollback-scope list: a frame's effects survive only when the
// frame succeeded locally and no scope owned by the frame was rolled back.
// Because a scope rollback propagates to all descendant scopes (D74 rule 4),
// checking the frame's own scopes covers ancestor rollback; a caught child
// revert never un-commits the parent. Frames without a proven owning scope
// keep their local outcome.
func ApplyCommitView(frames []CallFrame, scopes []RollbackScope) ([]CallFrame, error) {
	if err := ValidateCalls(frames); err != nil {
		return nil, err
	}
	rolled := make(map[uint64]bool, len(scopes))
	for _, s := range scopes {
		rolled[s.ScopeID] = s.RolledBack
	}
	out := make([]CallFrame, len(frames))
	copy(out, frames)
	for i := range out {
		f := &out[i]
		committed := f.Outcome == ResultSuccess
		for _, s := range scopes {
			if s.OwnerFrameID == nil || *s.OwnerFrameID != f.FrameID {
				continue
			}
			if rolled[s.ScopeID] {
				committed = false
			}
		}
		f.EffectsCommitted = committed
	}
	return out, nil
}

// FrameRefs returns the minimal presence view of the frames for the shared
// balance validator.
func FrameRefs(frames []CallFrame) []balance.FrameRef {
	refs := make([]balance.FrameRef, len(frames))
	for i := range frames {
		refs[i] = balance.FrameRef{FrameID: frames[i].FrameID, ExecutionOrder: frames[i].ExecutionOrder}
	}
	return refs
}

// FrameByOrder finds the frame whose entry holds the given execution_order.
func FrameByOrder(frames []CallFrame, order uint64) (*CallFrame, bool) {
	for i := range frames {
		if frames[i].ExecutionOrder == order {
			return &frames[i], true
		}
	}
	return nil, false
}
