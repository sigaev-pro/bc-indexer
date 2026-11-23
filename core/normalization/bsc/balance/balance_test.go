package balance

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

var (
	payer       = balance.Address20{0x7e}
	recipient   = balance.Address20{0x40}
	validator   = balance.Address20{0x68}
	rewardAddr  = balance.Address20{0x10, 0x02}
	burnAddr    = balance.MustParseAddress("000000000000000000000000000000000000dead")
	valContract = balance.Address20{0x10, 0x00}
)

func u(v uint64) balance.UInt256 { return balance.UInt256FromUint64(v) }

func mutation(addr balance.Address20, before, after balance.UInt256) Mutation {
	return Mutation{Address: addr, Before: before, After: after, NativeReasonCode: 42}
}

// The retained BSC corpus fee flow: the payer purchases gas, the
// accumulator 0xff..fe receives the full fee as the recipient credit, and
// the purchase/return/credit all live at the boundary scope 0.
func TestCorpusFeeFlowBuildsAndValidates(t *testing.T) {
	b := NewBuilder()
	scope0 := uint64(0)
	if _, err := b.Add(Group{
		OpID: 1, Role: RoleGasPurchase, ExecutionOrder: 1, NativeSnapshot: 0,
		Mutations: []Mutation{mutation(payer, u(1000), u(790))},
	}, &scope0, true); err != nil {
		t.Fatalf("gas purchase: %v", err)
	}
	frame := uint64(0)
	if _, err := b.Add(Group{
		OpID: 2, Role: RoleTransfer, ExecutionOrder: 3, FramePresent: true, FrameID: frame, NativeSnapshot: 1,
		Mutations: []Mutation{
			mutation(payer, u(790), u(780)),
			mutation(recipient, u(0), u(10)),
		},
	}, &scope0, true); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	// A child frame entered at order 4; its SELFDESTRUCT executed at
	// order 5 and its monetary projection shares that order through the
	// exact execution-action cause.
	child := uint64(1)
	if _, err := b.Add(Group{
		OpID: 5, Role: RoleSelfDestructProjection, ExecutionOrder: 5, ExecutionActionID: 1, NativeSnapshot: 2,
		Mutations: []Mutation{
			mutation(recipient, u(10), u(0)),
			mutation(burnAddr, u(0), u(10)),
		},
	}, &scope0, true); err != nil {
		t.Fatalf("selfdestruct projection: %v", err)
	}
	if _, err := b.Add(Group{
		OpID: 3, Role: RoleGasReturn, ExecutionOrder: 6, NativeSnapshot: 0,
		Mutations: []Mutation{mutation(payer, u(780), u(785))},
	}, &scope0, true); err != nil {
		t.Fatalf("gas return: %v", err)
	}
	if _, err := b.Add(Group{
		OpID: 4, Role: RoleFeeRecipientCredit, ExecutionOrder: 7, NativeSnapshot: 0,
		Mutations: []Mutation{mutation(FeeRecipient, u(0), u(210))},
	}, &scope0, true); err != nil {
		t.Fatalf("fee credit: %v", err)
	}

	actions := b.Actions()
	if len(actions) != 5 {
		t.Fatalf("want 5 actions, got %d", len(actions))
	}
	if actions[0].ActionIndex != 1 || actions[4].ActionIndex != 5 {
		t.Fatalf("action indices not assigned in order: %+v", actions)
	}
	if actions[4].Cause.Operation != balance.OpFeeRecipientCredit || *actions[4].To != FeeRecipient {
		t.Fatalf("fee credit must credit the BSC accumulator, got %+v", actions[4])
	}
	if actions[0].Cause.OperationIndex != 0 || actions[3].Cause.OperationIndex != 1 || actions[4].Cause.OperationIndex != 2 {
		t.Fatalf("protocol operation indices must be 0,1,2 in invocation order")
	}
	// The SELFDESTRUCT projection shares order 5 with its execution action.
	if actions[2].ExecutionOrder != 5 || actions[2].Kind != balance.KindSelfDestructTransfer {
		t.Fatalf("selfdestruct projection drifted: %+v", actions[2])
	}

	summary, err := FeeSummary(actions)
	if err != nil {
		t.Fatalf("fee summary: %v", err)
	}
	if summary.Payer == nil || *summary.Payer != payer {
		t.Fatal("summary payer must be the purchase payer")
	}
	if summary.GasPurchaseDebit != u(210) || summary.GasReturnCredit != u(5) || summary.NetCharge != u(205) {
		t.Fatalf("summary amounts drifted: %+v", summary)
	}
	if len(summary.RecipientCredits) != 1 || summary.RecipientCredits[0].Address != FeeRecipient || summary.RecipientCredits[0].Amount != u(210) {
		t.Fatalf("summary credits drifted: %+v", summary.RecipientCredits)
	}

	view := balance.TxBalances{
		Scopes: []balance.RollbackScope{
			balance.TxBoundaryScope(),
			{ScopeID: 1, ParentScopeID: &scope0, OwnerFrameID: &frame},
			{ScopeID: 2, ParentScopeID: &scope0, OwnerFrameID: &child},
		},
		Actions:          actions,
		Frames:           []balance.FrameRef{{FrameID: 0, ExecutionOrder: 2}, {FrameID: 1, ExecutionOrder: 4}},
		ExecutionActions: []balance.ExecutionActionRef{{ExecutionActionID: 1, ExecutionOrder: 5}},
		FeeSummary:       &summary,
	}
	if err := balance.Validate(view); err != nil {
		t.Fatalf("shared validator rejected the built view: %v", err)
	}
}

// A Parlia system execution keeps the actual effects of the known native
// operation under the closed cause, without inventing a transfer shape.
func TestSystemExecutionGroup(t *testing.T) {
	b := NewBuilder()
	value := u(10000)
	group := Group{
		OpID: 9, Role: RoleSystemExecution, ExecutionOrder: 2, NativeSnapshot: 0,
		Mutations: []Mutation{
			mutation(validator, u(10000), u(0)),
			mutation(rewardAddr, u(0), u(625)),
			mutation(burnAddr, u(0), u(1000)),
			mutation(valContract, u(0), u(8375)),
		},
	}
	_ = value
	if _, err := b.Add(group, new(uint64), true); err != nil {
		t.Fatalf("system execution: %v", err)
	}
	actions := b.Actions()
	if len(actions) != 1 {
		t.Fatalf("want 1 action, got %d", len(actions))
	}
	a := actions[0]
	if a.Cause.Operation != balance.OpBSCSystemExecution {
		t.Fatalf("cause operation = %s", a.Cause.Operation)
	}
	if a.Engine != balance.EngineNativeProtocol || a.Kind != balance.KindNativeProtocolChange {
		t.Fatalf("unexpected shape %s/%s", a.Engine, a.Kind)
	}
	if len(a.Effects) != 4 {
		t.Fatalf("effects must keep all four native mutations, got %d", len(a.Effects))
	}
	if a.Amount != u(10000) {
		t.Fatalf("amount drifted: %s", a.Amount)
	}

	view := balance.TxBalances{
		Scopes:  []balance.RollbackScope{balance.TxBoundaryScope()},
		Actions: actions,
		Frames:  []balance.FrameRef{{FrameID: 0, ExecutionOrder: 1}},
	}
	if err := balance.Validate(view); err != nil {
		t.Fatalf("shared validator rejected the system view: %v", err)
	}
}

// The Parlia sweep runs outside every transaction; forcing it into a tx
// record is an error, not a silent transfer.
func TestParliaSweepRejected(t *testing.T) {
	b := NewBuilder()
	group := Group{
		OpID: 1, Role: RoleParliaSweep, ExecutionOrder: 1,
		Mutations: []Mutation{mutation(FeeRecipient, u(210), u(0)), mutation(validator, u(0), u(210))},
	}
	if _, err := b.Add(group, new(uint64), true); !errors.Is(err, ErrSweepOutsideTx) {
		t.Fatalf("sweep error = %v", err)
	}
}

func TestUnknownRoleRejected(t *testing.T) {
	b := NewBuilder()
	if _, err := b.Add(Group{OpID: 1, Role: RoleInvalid, ExecutionOrder: 1}, new(uint64), true); !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("unknown role error = %v", err)
	}
}

func TestOrderRules(t *testing.T) {
	b := NewBuilder()
	if _, err := b.Add(Group{OpID: 1, Role: RoleGasPurchase, ExecutionOrder: 0, Mutations: []Mutation{mutation(payer, u(1), u(0))}}, new(uint64), true); !errors.Is(err, ErrOrderZero) {
		t.Fatalf("zero order error = %v", err)
	}
	b2 := NewBuilder()
	scope := uint64(0)
	if _, err := b2.Add(Group{OpID: 1, Role: RoleGasPurchase, ExecutionOrder: 1, Mutations: []Mutation{mutation(payer, u(1), u(0))}}, &scope, true); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := b2.Add(Group{OpID: 2, Role: RoleGasReturn, ExecutionOrder: 1, Mutations: []Mutation{mutation(payer, u(0), u(1))}}, &scope, true); !errors.Is(err, ErrOrderReuse) {
		t.Fatalf("order reuse error = %v", err)
	}
}

// A self-address transfer keeps both sides on one address with a net-zero
// effect; the action never disappears.
func TestSelfAddressTransferStaysPresent(t *testing.T) {
	b := NewBuilder()
	frame := uint64(0)
	_, err := b.Add(Group{
		OpID: 1, Role: RoleTransfer, ExecutionOrder: 3, FramePresent: true, FrameID: frame,
		Mutations: []Mutation{mutation(payer, u(10), u(10))},
	}, new(uint64), true)
	if err != nil {
		t.Fatalf("self transfer: %v", err)
	}
	actions := b.Actions()
	if *actions[0].From != payer || *actions[0].To != payer {
		t.Fatal("self transfer sides drifted")
	}
	if len(actions[0].Effects) != 1 || actions[0].Effects[0].Before != actions[0].Effects[0].After {
		t.Fatalf("net-zero effect lost: %+v", actions[0].Effects)
	}
}

// A zero-fee transaction aggregates to explicit zeros with an absent
// payer: no fee actions exist, so the summary adds nothing.
func TestZeroFeeSummary(t *testing.T) {
	summary, err := FeeSummary(nil)
	if err != nil {
		t.Fatalf("zero fee summary: %v", err)
	}
	if summary.Payer != nil || !summary.NetCharge.IsZero() || len(summary.RecipientCredits) != 0 {
		t.Fatalf("zero fee summary drifted: %+v", summary)
	}
}

func TestSeveralPayersRejected(t *testing.T) {
	other := balance.Address20{0x99}
	actions := []balance.BalanceAction{
		{ActionIndex: 1, ExecutionOrder: 1, Cause: balance.ProtocolCause(balance.OpGasPurchase, 0), From: &payer, Amount: u(1)},
		{ActionIndex: 2, ExecutionOrder: 2, Cause: balance.ProtocolCause(balance.OpGasPurchase, 1), From: &other, Amount: u(1)},
	}
	if _, err := FeeSummary(actions); !errors.Is(err, ErrSeveralPayers) {
		t.Fatalf("several payers error = %v", err)
	}
}
