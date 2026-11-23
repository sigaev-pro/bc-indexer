// Package selfdestruct is the SELFDESTRUCT area of the BSC normalization
// module (E04-01.selfdestruct).
//
// The opcode, its monetary movement and the account deletion are three
// separate facts: the ExecutionAction keeps the opcode even at zero
// balance, the monetary projection is a balance action caused by the exact
// execution action (sharing its execution_order, the only shared order the
// D74 counter allows), and the final existence of the account is observed
// in the transaction-boundary account changes, never derived from the
// opcode alone.
//
// Normative anchors:
//
//   - .project/docs/MESSAGE_CONTRACT.md, section 3, ExecutionAction and
//     SelfDestructAction rows
//   - .project/docs/CURRENT_SEMANTICS.md, D74 (SELFDESTRUCT and its
//     monetary projection share the order only through
//     cause=ExecutionAction; the finalization burn is a separate operation
//     at its own position) and the D62 action table (SELFDESTRUCT keeps
//     the opcode at balance 0; no second invented CALL)
//   - EIP-6780 activation follows the pinned BSC fork rules; the package
//     records the observed eligibility, it does not re-derive the fork
package selfdestruct

import (
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// Sentinel errors of the action validator.
var (
	ErrOrderZero            = errors.New("bsc/selfdestruct: execution_order starts at 1")
	ErrDeletionNotEligible  = errors.New("bsc/selfdestruct: a committed deletion requires fork eligibility")
	ErrDeletionNotCommitted = errors.New("bsc/selfdestruct: uncommitted effects cannot carry a committed deletion")
	ErrZeroActionID         = errors.New("bsc/selfdestruct: execution_action_id starts at 1")
)

// Action is the typed SELFDESTRUCT execution action of one transaction:
// the opcode event itself, independent of its monetary projection.
type Action struct {
	// ExecutionActionID is the tx-local execution-action identity.
	ExecutionActionID uint64
	// ExecutionOrder is the position of the opcode execution in the single
	// tx-local counter of D74. Its monetary projection balance action
	// shares this order exactly through cause=ExecutionAction.
	ExecutionOrder uint64
	// FrameID references the frame that executed the opcode, never an
	// artificial CALL frame.
	FrameID uint64
	// Contract is the destroyed contract address.
	Contract balance.Address20
	// Beneficiary is the designated beneficiary. A self-beneficiary and a
	// possible burn follow the native fork rules, not a universal
	// transfer rule.
	Beneficiary balance.Address20
	// BalanceAtEntry is the contract balance at opcode entry, kept even
	// when it is zero.
	BalanceAtEntry balance.UInt256
	// EffectsCommitted is the final applicability of this action's effects
	// after rollback.
	EffectsCommitted bool
	// DeletionEligibleByRules records whether the fork rules make the
	// account deletion eligible (for example the same-transaction creation
	// case of EIP-6780).
	DeletionEligibleByRules bool
	// DeletionCommitted records whether the deletion actually survived.
	// It is false whenever the effects did not commit.
	DeletionCommitted bool
}

// Ref returns the presence view for the shared balance validator.
func (a Action) Ref() balance.ExecutionActionRef {
	return balance.ExecutionActionRef{ExecutionActionID: a.ExecutionActionID, ExecutionOrder: a.ExecutionOrder}
}

// Validate checks the action's own rules: positive identities and order,
// and the deletion flag lattice.
func (a Action) Validate() error {
	var errs []error
	if a.ExecutionActionID == 0 {
		errs = append(errs, ErrZeroActionID)
	}
	if a.ExecutionOrder == 0 {
		errs = append(errs, ErrOrderZero)
	}
	if a.DeletionCommitted {
		if !a.DeletionEligibleByRules {
			errs = append(errs, fmt.Errorf("action %d: %w", a.ExecutionActionID, ErrDeletionNotEligible))
		}
		if !a.EffectsCommitted {
			errs = append(errs, fmt.Errorf("action %d: %w", a.ExecutionActionID, ErrDeletionNotCommitted))
		}
	}
	return errors.Join(errs...)
}

// MonetaryProjection builds the balance action of the monetary movement
// this SELFDESTRUCT caused: one action grouping the debit of the contract
// and the credit of the beneficiary, including the self-beneficiary
// net-zero case. It shares the execution order of the execution action
// through its exact cause; the two representations are never summed as
// two movements. A rollback scope of nil means the native scope was not
// proven, which is legal only because a zero-balance destruction produces
// no effects.
func (a Action) MonetaryProjection(effects []balance.BalanceEffect, committed bool, rollbackScope *uint64) (balance.BalanceAction, error) {
	if err := a.Validate(); err != nil {
		return balance.BalanceAction{}, err
	}
	amount := a.BalanceAtEntry
	from, to := a.Contract, a.Beneficiary
	action := balance.BalanceAction{
		ActionIndex:      0, // assigned by the caller's action indexer
		ExecutionOrder:   a.ExecutionOrder,
		Cause:            balance.ExecutionActionCause(a.ExecutionActionID),
		Engine:           balance.EngineEVM,
		Kind:             balance.KindSelfDestructTransfer,
		From:             &from,
		To:               &to,
		Amount:           amount,
		Asset:            balance.AssetNative,
		EffectsCommitted: committed,
		RollbackScope:    rollbackScope,
		Effects:          effects,
	}
	return action, nil
}
