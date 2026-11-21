package balance

import "fmt"

// Engine names the execution engine that performed the native operation. It
// mirrors the CallFrame engine values from MESSAGE_CONTRACT section 3.
type Engine uint8

const (
	// EngineUnspecified is the invalid zero value.
	EngineUnspecified Engine = iota
	// EngineEVM is the EVM execution engine of the ETH, BSC and Polygon
	// profiles.
	EngineEVM
	// EngineNativeProtocol is a registered native protocol execution boundary
	// (BSC system execution, Bor state-sync execution).
	EngineNativeProtocol
)

// String returns the registry name of the engine.
func (e Engine) String() string {
	switch e {
	case EngineEVM:
		return "EVM"
	case EngineNativeProtocol:
		return "NATIVE_PROTOCOL"
	default:
		return fmt.Sprintf("ENGINE_INVALID(%d)", uint8(e))
	}
}

// ActionKind is the closed registry of monetary action kinds (D62).
type ActionKind uint8

const (
	// KindUnspecified is the invalid zero value.
	KindUnspecified ActionKind = iota
	// KindTransfer is a native value transfer: one action groups the debit
	// and the credit of one native transfer invocation.
	KindTransfer
	// KindMint is a native mint.
	KindMint
	// KindBurn is a native burn, including the EIP-6780 finalization burn.
	KindBurn
	// KindFee is a fee-motivated movement: gas purchase or a fee credit.
	KindFee
	// KindRefund is a monetary return of unused gas.
	KindRefund
	// KindSelfDestructTransfer is the monetary projection of a SELFDESTRUCT
	// execution action.
	KindSelfDestructTransfer
	// KindNativeProtocolChange keeps the actual effects of a known native
	// system operation without inventing an economic cause.
	KindNativeProtocolChange
)

// String returns the registry name of the kind.
func (k ActionKind) String() string {
	switch k {
	case KindTransfer:
		return "TRANSFER"
	case KindMint:
		return "MINT"
	case KindBurn:
		return "BURN"
	case KindFee:
		return "FEE"
	case KindRefund:
		return "REFUND"
	case KindSelfDestructTransfer:
		return "SELFDESTRUCT_TRANSFER"
	case KindNativeProtocolChange:
		return "NATIVE_PROTOCOL_CHANGE"
	default:
		return fmt.Sprintf("KIND_INVALID(%d)", uint8(k))
	}
}

// BalanceAction is one causal native monetary action of a transaction
// (CURRENT_SEMANTICS D62):
//
//	BalanceAction={action_index, execution_order, cause, engine, kind,
//	  from?, to?, amount, asset=NATIVE, effects_committed, rollback_scope?,
//	  balance_effects[]}
//
// A nil From/To is an absent field; an explicit zero amount is present data.
// Actions whose effects sum to zero are kept: +10 and -10 in one successful
// transaction remain two actions even when the final diff is zero. A repeated
// or cancelled attempt keeps its order, scope and original before/after
// points with effects_committed=false.
type BalanceAction struct {
	// ActionIndex is the tx-local action identity. It is its own identity,
	// not a chronology shared with frames or execution actions.
	ActionIndex uint64
	// ExecutionOrder is the position of the native monetary operation in the
	// single tx-local execution_order counter shared by calls, execution
	// actions and balance actions (D74). It starts at 1.
	ExecutionOrder uint64
	// Cause identifies the causing operation boundary.
	Cause Cause
	// Engine is the engine that performed the operation.
	Engine Engine
	// Kind is the closed monetary kind.
	Kind ActionKind
	// From is the debited address, when the native operation has one.
	From *Address20
	// To is the credited address, when the native operation has one.
	To *Address20
	// Amount is the exact amount in minimal units. Its meaning follows the
	// kind; for KindNativeProtocolChange it carries the operation's own
	// declared amount and the effects list stays authoritative.
	Amount UInt256
	// Asset is always NATIVE in the current closed registry.
	Asset Asset
	// EffectsCommitted is the final applicability of this action's effects
	// after rollback: true only when the effects survived the transaction.
	EffectsCommitted bool
	// RollbackScope references a scope_id from the same transaction's
	// rollback_scopes list, never a native snapshot ID. Nil is absent and is
	// legal only for actions without effects whose native scope is unproven;
	// absence is never an unknown scope in place of an explicit 0 (D74
	// rule 5). The schema keeps absent and scope 0 distinct.
	RollbackScope *uint64
	// Effects lists the address effects of this action in native order.
	Effects []BalanceEffect
}

// BalanceEffect is one captured balance mutation of one action
// (CURRENT_SEMANTICS D62):
//
//	BalanceEffect={effect_index, address, before, after, native_reason_code}
//
// Before/after are the points of this action alone; AccountChange is the
// whole-transaction boundary view and is never summed with these.
type BalanceEffect struct {
	// EffectIndex orders the effects inside one action from 0 without gaps.
	// Effects never allocate their own execution_order.
	EffectIndex uint64
	// Address is the mutated account.
	Address Address20
	// Before is the balance immediately before this mutation.
	Before UInt256
	// After is the balance immediately after this mutation.
	After UInt256
	// NativeReasonCode is the source-native reason code. It is interpreted
	// only together with a pinned ProfileRef; this package treats it as
	// opaque and never derives meaning from it.
	NativeReasonCode uint32
}

// Delta returns the signed difference after-before as sign (-1, 0, 1) and
// magnitude.
func (e BalanceEffect) Delta() (int, UInt256) {
	switch e.Before.Cmp(e.After) {
	case -1:
		d, err := e.After.SubChecked(e.Before)
		if err != nil {
			// Unreachable: After >= Before in this branch.
			return 0, UInt256{}
		}
		return 1, d
	case 1:
		d, err := e.Before.SubChecked(e.After)
		if err != nil {
			return 0, UInt256{}
		}
		return -1, d
	default:
		return 0, UInt256{}
	}
}
