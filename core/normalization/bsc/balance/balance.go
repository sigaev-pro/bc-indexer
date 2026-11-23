// Package balance is the balance-actions area of the BSC normalization
// module (E04-03.balance_actions).
//
// It turns the observed native monetary operations of one BSC transaction
// into the typed causal actions of protocol/balance: gas purchase, gas
// return, fee recipient credit, frame value transfers, SELFDESTRUCT
// projections and BSC system-execution operations, each with stable
// tx-local identities and orders. Net-zero and compensating actions never
// disappear, and the receipt fee summary stays an aggregate that never
// adds a second movement.
//
// BSC fee model anchors:
//
//   - the fee accumulator, the native system address 0xff..fe
//     (consensus.SystemAddress of the pinned client), receives the fee
//     credit of ordinary transactions in the retained corpus path; Parlia
//     later sweeps the accumulated balance to the block validator outside
//     every transaction, which is why a sweep group is rejected here and
//     routed to the optional outside-tx scope
//   - .project/docs/CURRENT_SEMANTICS.md, D62 (action table, fee summary)
//     and D74 (order, scopes, protocol operation_index)
//   - protocol/balance fixture_test.go: the BSC corpus rows where the
//     accumulator is the tip recipient and no burn exists on the Istanbul
//     path
package balance

import (
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// Sentinel errors of the builder.
var (
	ErrUnknownRole    = errors.New("bsc/balance: a mutation group without a resolved native role is never published")
	ErrSweepOutsideTx = errors.New("bsc/balance: the Parlia fee sweep is an outside-tx operation, not a transaction action")
	ErrEffectIndex    = errors.New("bsc/balance: effect_index must be 0..len-1 in native order")
	ErrSidesMissing   = errors.New("bsc/balance: the observed mutations do not carry the sides this role requires")
	ErrOrderReuse     = errors.New("bsc/balance: an execution order is allocated exactly once")
	ErrOrderZero      = errors.New("bsc/balance: execution_order starts at 1")
	ErrSeveralPayers  = errors.New("bsc/balance: a transaction has one gas purchase payer")
	ErrCreditNoTo     = errors.New("bsc/balance: a fee recipient credit names its recipient")
)

// FeeRecipient is the native BSC fee accumulator: the system address
// 0xfffffffffffffffffffffffffffffffffffffffe of the pinned client. Fee
// recipient credits of ordinary transactions land here on the retained
// corpus path.
var FeeRecipient = balance.Address20{
	0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe,
}

// Role names what native operation a mutation group belongs to. The role
// comes from the pinned native reason provenance of the capture; it is
// never guessed from amounts or addresses.
type Role uint8

// Roles of observed mutation groups.
const (
	// RoleInvalid is the unset role; a group with it is an error.
	RoleInvalid Role = iota
	// RoleGasPurchase is the upfront gas purchase of the fee payer.
	RoleGasPurchase
	// RoleGasReturn is the return of unused gas to the payer.
	RoleGasReturn
	// RoleFeeRecipientCredit is the fee credit to the native recipient.
	RoleFeeRecipientCredit
	// RoleTransfer is a value transfer at the entry of a call frame.
	RoleTransfer
	// RoleSelfDestructProjection is the monetary movement of a
	// SELFDESTRUCT execution action.
	RoleSelfDestructProjection
	// RoleSystemExecution is a BSC system-execution operation inside a
	// Parlia system transaction.
	RoleSystemExecution
	// RoleParliaSweep is the Parlia SystemAddress-to-validator fee sweep
	// that runs immediately before a deposit system transaction, outside
	// every transaction boundary.
	RoleParliaSweep
	// RoleFinalizationBurn is the post-execution burn of a SELFDESTRUCT
	// finalization (EIP-6780 family rules): a separate operation at its
	// own position that references the execution actions it finalizes.
	RoleFinalizationBurn
)

var roleNames = [...]string{
	"INVALID", "GAS_PURCHASE", "GAS_RETURN", "FEE_RECIPIENT_CREDIT",
	"TRANSFER", "SELFDESTRUCT_PROJECTION", "SYSTEM_EXECUTION", "PARLIA_SWEEP",
	"FINALIZATION_BURN",
}

// String returns the registry name of r.
func (r Role) String() string {
	if r == 0 || int(r) >= len(roleNames) {
		return fmt.Sprintf("ROLE_INVALID(%d)", uint8(r))
	}
	return roleNames[r]
}

// Group is one observed native monetary operation: the balance mutations
// that share one native integration operation identity. The capture layer
// groups by that identity; adjacency or equal sums never substitute for
// it.
type Group struct {
	// OpID is the native integration operation identity shared by the
	// effects of this group.
	OpID uint64
	// Role is the resolved native role of the operation.
	Role Role
	// ExecutionOrder is the position the normalizer allocated to this
	// operation in the single tx-local counter of D74.
	ExecutionOrder uint64
	// FramePresent marks a proven frame link of a transfer group; the
	// frame is the causing call frame.
	FramePresent bool
	FrameID      uint64
	// ExecutionActionID is the causing execution action of a
	// SELFDESTRUCT projection group.
	ExecutionActionID uint64
	// NativeSnapshot is the native snapshot scope active at the operation,
	// or 0 outside every nested snapshot.
	NativeSnapshot uint64
	// RelatedExecutionActions links a finalization burn to the execution
	// actions it finalizes; required for RoleFinalizationBurn.
	RelatedExecutionActions []uint64
	// Mutations are the captured balance mutations in native order.
	Mutations []Mutation
}

// Mutation is one captured balance mutation of a group.
type Mutation struct {
	Address balance.Address20
	Before  balance.UInt256
	After   balance.UInt256
	// NativeReasonCode is the opaque source reason code, interpreted only
	// together with the pinned profile.
	NativeReasonCode uint32
}

// Action is one built balance action: the typed action plus the tx-local
// protocol operation index, when the cause is a protocol operation.
type Action struct {
	BalanceAction balance.BalanceAction
	// OperationIndex is the tx-local index of the protocol invocation for
	// protocol causes, allocated from 0 without gaps in invocation order.
	// It is neither an execution_order value nor a native body position.
	OperationIndex uint64
	// HasOperationIndex reports whether the cause is a protocol operation.
	HasOperationIndex bool
}

// Builder assembles the typed actions of one transaction from its observed
// groups and allocates the tx-local protocol operation indices.
type Builder struct {
	actions     []Action
	usedOrders  map[uint64]bool
	protocolOps uint64
}

// NewBuilder returns a builder for one transaction.
func NewBuilder() *Builder {
	return &Builder{usedOrders: make(map[uint64]bool)}
}

// Add turns one group into its typed action. Effects keep their native
// order and receive effect indices from 0. The caller supplies the
// rollback-resolved committed fact and the normalized scope reference; a
// group with effects must carry an explicit scope.
func (b *Builder) Add(group Group, scope *uint64, committed bool) (Action, error) {
	if group.Role == RoleInvalid {
		return Action{}, fmt.Errorf("op %d: %w", group.OpID, ErrUnknownRole)
	}
	if group.Role == RoleParliaSweep {
		return Action{}, fmt.Errorf("op %d: %w", group.OpID, ErrSweepOutsideTx)
	}
	if group.ExecutionOrder == 0 {
		return Action{}, fmt.Errorf("op %d: %w", group.OpID, ErrOrderZero)
	}
	if b.usedOrders[group.ExecutionOrder] {
		return Action{}, fmt.Errorf("op %d order %d: %w", group.OpID, group.ExecutionOrder, ErrOrderReuse)
	}
	b.usedOrders[group.ExecutionOrder] = true

	effects := make([]balance.BalanceEffect, len(group.Mutations))
	for i, m := range group.Mutations {
		effects[i] = balance.BalanceEffect{
			EffectIndex: uint64(i), Address: m.Address, Before: m.Before, After: m.After,
			NativeReasonCode: m.NativeReasonCode,
		}
	}
	debit, credit := debitSide(group), creditSide(group)

	action := Action{BalanceAction: balance.BalanceAction{
		ExecutionOrder:   group.ExecutionOrder,
		Asset:            balance.AssetNative,
		EffectsCommitted: committed,
		RollbackScope:    scope,
		Effects:          effects,
	}}

	switch group.Role {
	case RoleGasPurchase:
		from := debit
		if from == nil {
			from = firstAddress(group)
		}
		if from == nil {
			return Action{}, fmt.Errorf("op %d gas purchase: %w", group.OpID, ErrSidesMissing)
		}
		action.BalanceAction.Cause = b.protocolCause(balance.OpGasPurchase)
		action.BalanceAction.Engine = balance.EngineEVM
		action.BalanceAction.Kind = balance.KindFee
		action.BalanceAction.From = from
		action.BalanceAction.Amount = debitAmount(group)
	case RoleGasReturn:
		to := credit
		if to == nil {
			to = firstAddress(group)
		}
		if to == nil {
			return Action{}, fmt.Errorf("op %d gas return: %w", group.OpID, ErrSidesMissing)
		}
		action.BalanceAction.Cause = b.protocolCause(balance.OpGasReturn)
		action.BalanceAction.Engine = balance.EngineEVM
		action.BalanceAction.Kind = balance.KindRefund
		action.BalanceAction.To = to
		action.BalanceAction.Amount = creditAmount(group)
	case RoleFeeRecipientCredit:
		to := credit
		if to == nil {
			to = firstAddress(group)
		}
		if to == nil {
			return Action{}, fmt.Errorf("op %d fee credit: %w", group.OpID, ErrSidesMissing)
		}
		action.BalanceAction.Cause = b.protocolCause(balance.OpFeeRecipientCredit)
		action.BalanceAction.Engine = balance.EngineEVM
		action.BalanceAction.Kind = balance.KindFee
		action.BalanceAction.To = to
		action.BalanceAction.Amount = creditAmount(group)
	case RoleTransfer:
		if !group.FramePresent {
			return Action{}, fmt.Errorf("op %d transfer: %w", group.OpID, ErrSidesMissing)
		}
		from, to := transferSides(group)
		if from == nil || to == nil {
			return Action{}, fmt.Errorf("op %d transfer: %w", group.OpID, ErrSidesMissing)
		}
		action.BalanceAction.Cause = balance.FrameCause(group.FrameID)
		action.BalanceAction.Engine = balance.EngineEVM
		action.BalanceAction.Kind = balance.KindTransfer
		action.BalanceAction.From, action.BalanceAction.To = from, to
		action.BalanceAction.Amount = transferAmount(group)
	case RoleSelfDestructProjection:
		from, to := transferSides(group)
		if from == nil || to == nil {
			return Action{}, fmt.Errorf("op %d selfdestruct: %w", group.OpID, ErrSidesMissing)
		}
		action.BalanceAction.Cause = balance.ExecutionActionCause(group.ExecutionActionID)
		action.BalanceAction.Engine = balance.EngineEVM
		action.BalanceAction.Kind = balance.KindSelfDestructTransfer
		action.BalanceAction.From, action.BalanceAction.To = from, to
		action.BalanceAction.Amount = transferAmount(group)
	case RoleSystemExecution:
		action.BalanceAction.Cause = b.protocolCause(balance.OpBSCSystemExecution)
		action.BalanceAction.Engine = balance.EngineNativeProtocol
		action.BalanceAction.Kind = balance.KindNativeProtocolChange
		// From/To follow the operation and the effects list stays
		// authoritative; no economic transfer shape is invented for a
		// known native system operation.
		action.BalanceAction.From, action.BalanceAction.To = debit, credit
		action.BalanceAction.Amount = debitAmount(group)
	case RoleFinalizationBurn:
		from := debit
		if from == nil {
			from = firstAddress(group)
		}
		if from == nil || len(group.RelatedExecutionActions) == 0 {
			return Action{}, fmt.Errorf("op %d finalization burn: %w", group.OpID, ErrSidesMissing)
		}
		action.BalanceAction.Cause = balance.ProtocolCause(balance.OpSelfDestructFinalizationBurn, b.protocolOps, group.RelatedExecutionActions...)
		b.protocolOps++
		action.BalanceAction.Engine = balance.EngineEVM
		action.BalanceAction.Kind = balance.KindBurn
		action.BalanceAction.From = from
		action.BalanceAction.Amount = debitAmount(group)
	default:
		return Action{}, fmt.Errorf("op %d: %w", group.OpID, ErrUnknownRole)
	}
	action.OperationIndex = action.BalanceAction.Cause.OperationIndex
	action.HasOperationIndex = action.BalanceAction.Cause.Kind == balance.CauseTransactionProtocol
	b.actions = append(b.actions, action)
	return action, nil
}

// AddSelfDestructProjection adds the monetary projection of one
// SELFDESTRUCT execution action. It is the single D74 exception to the
// one-order rule: the projection shares the execution action's
// execution_order through its exact cause, so the order-reuse guard is
// satisfied by the caller passing the action's order and id.
func (b *Builder) AddSelfDestructProjection(group Group, executionActionID, executionOrder uint64, scope *uint64, committed bool) (Action, error) {
	group.Role = RoleSelfDestructProjection
	group.ExecutionActionID = executionActionID
	group.ExecutionOrder = executionOrder
	return b.Add(group, scope, committed)
}

// protocolCause allocates the next tx-local protocol operation index in
// invocation order, from 0 without gaps.
func (b *Builder) protocolCause(op balance.ProtocolOperation) balance.Cause {
	cause := balance.ProtocolCause(op, b.protocolOps)
	b.protocolOps++
	return cause
}

// Actions returns the built actions with action indices assigned in
// execution order, sorted by execution_order.
func (b *Builder) Actions() []balance.BalanceAction {
	out := make([]balance.BalanceAction, 0, len(b.actions))
	for _, a := range b.actions {
		out = append(out, a.BalanceAction)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ExecutionOrder < out[j-1].ExecutionOrder; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	for i := range out {
		out[i].ActionIndex = uint64(i + 1)
	}
	return out
}

// FeeSummary aggregates the fee actions of the transaction in action
// order: the payer of the purchases, the purchase debit, the return
// credit, the net charge and the recipient credits. It is an aggregate
// view: the caller never adds it to the action effects. A zero-fee Parlia
// system transaction has no fee actions and aggregates to explicit zeros
// with an absent payer.
func FeeSummary(actions []balance.BalanceAction) (balance.FeeSummary, error) {
	summary := balance.FeeSummary{}
	var purchase, ret balance.UInt256
	var credits []balance.FeeCredit
	var payer *balance.Address20
	for i := range actions {
		a := &actions[i]
		if a.Cause.Kind != balance.CauseTransactionProtocol {
			continue
		}
		switch a.Cause.Operation {
		case balance.OpGasPurchase:
			sum, err := purchase.AddChecked(a.Amount)
			if err != nil {
				return balance.FeeSummary{}, err
			}
			purchase = sum
			if payer != nil && (a.From == nil || *payer != *a.From) {
				return balance.FeeSummary{}, ErrSeveralPayers
			}
			if a.From != nil {
				payer = a.From
			}
		case balance.OpGasReturn:
			sum, err := ret.AddChecked(a.Amount)
			if err != nil {
				return balance.FeeSummary{}, err
			}
			ret = sum
		case balance.OpFeeRecipientCredit:
			if a.To == nil {
				return balance.FeeSummary{}, ErrCreditNoTo
			}
			credits = append(credits, balance.FeeCredit{Address: *a.To, Amount: a.Amount})
		}
	}
	net, err := purchase.SubChecked(ret)
	if err != nil {
		return balance.FeeSummary{}, err
	}
	summary.Payer = payer
	summary.GasPurchaseDebit = purchase
	summary.GasReturnCredit = ret
	summary.NetCharge = net
	summary.RecipientCredits = credits
	return summary, nil
}

// debitSide returns the address of the first mutation that decreases the
// balance, or nil when the group has no debit side.
func debitSide(group Group) *balance.Address20 {
	for _, m := range group.Mutations {
		if m.Before.Cmp(m.After) > 0 {
			addr := m.Address
			return &addr
		}
	}
	return nil
}

// creditSide returns the address of the first mutation that increases the
// balance, or nil when the group has no credit side.
func creditSide(group Group) *balance.Address20 {
	for _, m := range group.Mutations {
		if m.After.Cmp(m.Before) > 0 {
			addr := m.Address
			return &addr
		}
	}
	return nil
}

// transferSides returns both sides of a transfer group. A self-address
// transfer resolves to the same address on both sides, keeping the action
// present with a net-zero effect.
func transferSides(group Group) (*balance.Address20, *balance.Address20) {
	from, to := debitSide(group), creditSide(group)
	if from == nil && to == nil {
		if addr := firstAddress(group); addr != nil {
			return addr, addr
		}
		return nil, nil
	}
	if from == nil {
		from = firstAddress(group)
	}
	if to == nil {
		to = firstAddress(group)
	}
	return from, to
}

func firstAddress(group Group) *balance.Address20 {
	if len(group.Mutations) == 0 {
		return nil
	}
	addr := group.Mutations[0].Address
	return &addr
}

func debitAmount(group Group) balance.UInt256 {
	for _, m := range group.Mutations {
		if m.Before.Cmp(m.After) > 0 {
			d, _ := m.Before.SubChecked(m.After)
			return d
		}
	}
	return balance.UInt256{}
}

func creditAmount(group Group) balance.UInt256 {
	for _, m := range group.Mutations {
		if m.After.Cmp(m.Before) > 0 {
			d, _ := m.After.SubChecked(m.Before)
			return d
		}
	}
	return balance.UInt256{}
}

func transferAmount(group Group) balance.UInt256 {
	return debitAmount(group)
}
