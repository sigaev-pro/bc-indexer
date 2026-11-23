package poly

import (
	"fmt"
)

// SelfDestructAction is the typed payload of the SELFDESTRUCT execution
// action (MESSAGE_CONTRACT section 3): the opcode itself, kept even at a
// zero balance, with the fork eligibility and the final deletion result as
// separate facts.
type SelfDestructAction struct {
	ContractAddress Address20
	Beneficiary     Address20
	BalanceAtEntry  UInt256
	// EffectsCommitted is the final applicability of this action's effects
	// after rollback.
	EffectsCommitted bool
	// DeletionEligibleByRules reports the fork eligibility fact (EIP-6780:
	// same-transaction creation keeps full deletion semantics).
	DeletionEligibleByRules bool
	// DeletionCommitted is the stored deletion result of this action after
	// rollback and finalization; it is false whenever EffectsCommitted is
	// false.
	DeletionCommitted bool
}

// ExecutionAction is one typed execution action of the closed v1 registry.
// The monetary projection of a SELFDESTRUCT is a separate BalanceAction
// with cause ExecutionAction; the two representations are never summed.
type ExecutionAction struct {
	ExecutionActionID uint64
	ExecutionOrder    uint64
	// FrameID references the frame that executed the opcode, never an
	// artificial CALL.
	FrameID uint64
	Payload SelfDestructAction
}

// normalizeSelfDestructs builds the execution actions of the captured
// SELFDESTRUCT opcodes. Repeated opcodes keep their own identities and
// orders; a cancelled attempt keeps its points with committed=false.
func normalizeSelfDestructs(tx *TxCapture, orders *txOrders) ([]ExecutionAction, error) {
	out := make([]ExecutionAction, len(tx.SelfDestructs))
	for i, sd := range tx.SelfDestructs {
		if sd.Frame < 0 || sd.Frame >= len(tx.Frames) {
			return nil, fmt.Errorf("selfdestruct %d references unknown frame %d: %w", i, sd.Frame, ErrCapture)
		}
		if !sd.Committed && sd.Deleted {
			return nil, fmt.Errorf("selfdestruct %d is deleted but not committed: %w", i, ErrCapture)
		}
		out[i] = ExecutionAction{
			ExecutionActionID: uint64(i) + 1,
			ExecutionOrder:    orders.selfDestructs[i],
			FrameID:           uint64(sd.Frame),
			Payload: SelfDestructAction{
				ContractAddress:         sd.ContractAddress,
				Beneficiary:             sd.Beneficiary,
				BalanceAtEntry:          sd.BalanceAtEntry,
				EffectsCommitted:        sd.Committed,
				DeletionEligibleByRules: sd.CreatedSameTx,
				DeletionCommitted:       sd.Deleted && sd.Committed,
			},
		}
	}
	return out, nil
}
