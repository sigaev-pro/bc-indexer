package selfdestruct

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

var (
	contract    = balance.Address20{0x01}
	beneficiary = balance.Address20{0x02}
)

func validAction() Action {
	return Action{
		ExecutionActionID: 1, ExecutionOrder: 5, FrameID: 0,
		Contract: contract, Beneficiary: beneficiary,
		BalanceAtEntry: balance.UInt256FromUint64(10),
	}
}

func TestValidActionPasses(t *testing.T) {
	if err := validAction().Validate(); err != nil {
		t.Fatalf("valid action rejected: %v", err)
	}
	ref := validAction().Ref()
	if ref.ExecutionActionID != 1 || ref.ExecutionOrder != 5 {
		t.Fatalf("unexpected ref: %+v", ref)
	}
}

// The opcode stays an action even at zero balance, and a non-eligible or
// rolled-back destruction keeps its record with uncommitted effects.
func TestZeroBalanceAndUncommittedStayPresent(t *testing.T) {
	zero := validAction()
	zero.BalanceAtEntry = balance.UInt256{}
	if err := zero.Validate(); err != nil {
		t.Fatalf("zero-balance destruction rejected: %v", err)
	}

	rolledBack := validAction()
	rolledBack.DeletionEligibleByRules = true
	rolledBack.EffectsCommitted = false
	rolledBack.DeletionCommitted = false
	if err := rolledBack.Validate(); err != nil {
		t.Fatalf("rolled-back destruction rejected: %v", err)
	}
}

func TestDeletionFlagLattice(t *testing.T) {
	ineligible := validAction()
	ineligible.DeletionCommitted = true
	if !errors.Is(ineligible.Validate(), ErrDeletionNotEligible) {
		t.Fatalf("ineligible deletion error = %v", ineligible.Validate())
	}

	uncommitted := validAction()
	uncommitted.DeletionEligibleByRules = true
	uncommitted.DeletionCommitted = true
	if !errors.Is(uncommitted.Validate(), ErrDeletionNotCommitted) {
		t.Fatalf("uncommitted deletion error = %v", uncommitted.Validate())
	}

	committed := uncommitted
	committed.EffectsCommitted = true
	if err := committed.Validate(); err != nil {
		t.Fatalf("committed eligible deletion rejected: %v", err)
	}
}

func TestIdentitiesPositive(t *testing.T) {
	zeroID := validAction()
	zeroID.ExecutionActionID = 0
	if !errors.Is(zeroID.Validate(), ErrZeroActionID) {
		t.Fatalf("zero id error = %v", zeroID.Validate())
	}

	zeroOrder := validAction()
	zeroOrder.ExecutionOrder = 0
	if !errors.Is(zeroOrder.Validate(), ErrOrderZero) {
		t.Fatalf("zero order error = %v", zeroOrder.Validate())
	}
}

// The monetary projection shares the execution order only through its
// exact cause, groups both sides of the movement, and keeps the
// self-beneficiary net-zero case present.
func TestMonetaryProjection(t *testing.T) {
	action := validAction()
	scope := uint64(2)
	projection, err := action.MonetaryProjection([]balance.BalanceEffect{
		{EffectIndex: 0, Address: contract, Before: balance.UInt256FromUint64(10), After: balance.UInt256{}},
		{EffectIndex: 1, Address: beneficiary, Before: balance.UInt256{}, After: balance.UInt256FromUint64(10)},
	}, true, &scope)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if projection.ExecutionOrder != action.ExecutionOrder {
		t.Fatalf("projection order %d drifted from action order %d", projection.ExecutionOrder, action.ExecutionOrder)
	}
	if projection.Cause.Kind != balance.CauseExecutionAction || projection.Cause.ExecutionActionID != action.ExecutionActionID {
		t.Fatalf("projection cause is not the exact execution action: %+v", projection.Cause)
	}
	if projection.Kind != balance.KindSelfDestructTransfer || projection.Engine != balance.EngineEVM {
		t.Fatalf("unexpected projection shape: %s/%s", projection.Kind, projection.Engine)
	}
	if *projection.From != contract || *projection.To != beneficiary {
		t.Fatal("projection sides drifted")
	}

	selfBeneficiary := validAction()
	selfBeneficiary.Beneficiary = contract
	netZero, err := selfBeneficiary.MonetaryProjection([]balance.BalanceEffect{
		{EffectIndex: 0, Address: contract, Before: balance.UInt256FromUint64(10), After: balance.UInt256FromUint64(10)},
	}, true, &scope)
	if err != nil {
		t.Fatalf("self-beneficiary projection: %v", err)
	}
	if len(netZero.Effects) != 1 {
		t.Fatalf("self-beneficiary keeps the net-zero point, got %d effects", len(netZero.Effects))
	}

	invalid := validAction()
	invalid.ExecutionActionID = 0
	if _, err := invalid.MonetaryProjection(nil, false, nil); err == nil {
		t.Fatal("projection of an invalid action must fail")
	}
}
