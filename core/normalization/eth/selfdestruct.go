package eth

import (
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// SelfDestruct errors.
var (
	// ErrSelfDestructShape reports a structurally invalid selfdestruct
	// observation.
	ErrSelfDestructShape = errors.New("eth: invalid selfdestruct observation")
	// ErrSelfDestructRegistry reports a native movement for which the
	// closed v1 operation registry has no representation. The builder
	// fails closed instead of inventing an OTHER-style fallback; extending
	// the registry is a versioned schema change.
	ErrSelfDestructRegistry = errors.New("eth: selfdestruct movement without a v1 registry operation")
)

// SelfDestructAction is the typed execution-action payload of the
// SELFDESTRUCT opcode (MESSAGE_CONTRACT section 3). The opcode itself, its
// monetary movement and the deletion are three separate facts:
//
//   - this action keeps the opcode execution even when the contract
//     balance is zero;
//   - the monetary movement is the separate BalanceAction whose cause
//     names this execution action and whose effects are the observed
//     native mutations, never a second invented CALL;
//   - the deletion facts: DeletionEligibleByRules describes the
//     fork/creation eligibility (EIP-6780: pre-Cancun every selfdestruct
//     may delete; post-Cancun only a contract created in the same
//     transaction), DeletionCommitted is the surviving result of this
//     action after rollback and finalization, and the final existence of
//     the account is read from AccountChange, never derived from the
//     opcode alone.
type SelfDestructAction struct {
	ContractAddress Address20
	Beneficiary     Address20
	BalanceAtEntry  UInt256
	// EffectsCommitted is the final applicability of this action's effects
	// after rollback.
	EffectsCommitted bool
	// DeletionEligibleByRules is the fork/creation eligibility input.
	DeletionEligibleByRules bool
	// DeletionCommitted is false whenever EffectsCommitted is false.
	DeletionCommitted bool
}

// ExecutionActionPayload is the closed payload union of execution actions.
// The only mandatory v1 variant is SELFDESTRUCT; native pseudo enter/exit
// callbacks normalize into it and never create a second frame or transfer.
type ExecutionActionPayload struct {
	SelfDestruct *SelfDestructAction
}

// ExecutionAction is one typed execution action that is not reducible to a
// balance transfer (MESSAGE_CONTRACT section 3):
//
//	ExecutionAction={execution_action_id,execution_order,frame_id,payload}
//
// The frame references the frame that executed the opcode, never an
// invented CALL. Repeated or cancelled selfdestructs keep their own
// identity and order, even when their amounts are zero.
type ExecutionAction struct {
	ExecutionActionID uint64
	ExecutionOrder    uint64
	FrameID           uint64
	Payload           ExecutionActionPayload
}

// MovementPosition is where native performed the monetary movement of a
// selfdestruct: at the opcode position, or at the transaction finalization
// position (the EIP-6780 end-of-transaction processing).
type MovementPosition uint8

// Movement positions.
const (
	// MovementAtOpcode is the immediate transfer of pre-Cancun rules and of
	// the post-Cancun same-transaction-creation case.
	MovementAtOpcode MovementPosition = iota + 1
	// MovementAtFinalization is the end-of-transaction processing position.
	MovementAtFinalization
)

// SelfDestructObservation is one observed native SELFDESTRUCT execution.
type SelfDestructObservation struct {
	// FrameID is the frame that executed the opcode.
	FrameID uint64
	// Contract and Beneficiary are the opcode addresses.
	Contract    Address20
	Beneficiary Address20
	// BalanceAtEntry is the contract balance at the opcode.
	BalanceAtEntry UInt256
	// DeletionEligible is the fork/creation eligibility already resolved
	// by the adapter from the pinned fork rules and the same-tx creation
	// fact (EIP-6780).
	DeletionEligible bool
	// Scope is the rollback scope the opcode ran in.
	Scope uint64
	// RolledBack is the final rollback fact of that scope.
	RolledBack bool
	// AccountDeleted is the observed final existence fact from the
	// transaction-boundary account change.
	AccountDeleted bool
	// MovementPosition is where native moved the funds.
	MovementPosition MovementPosition
	// MovementEffects are the observed native balance mutations of the
	// movement in native order. Zero balance means an empty list: the
	// action still exists, with no invented movement.
	MovementEffects []balance.BalanceEffect
}

// BuildSelfDestruct normalizes one observed selfdestruct into the
// execution action and the monetary projection of an opcode-position
// movement. The two returned objects share one execution_order only
// through the cause=ExecutionAction link, which is the one equality the
// shared counter allows (D74). Finalization-position movements are built
// separately with BuildFinalizationBurn at their own actual position.
//
// The ids come from the caller's tx-local counters: execActionID is the
// next execution action id (1-based) and the action index counter advances
// inside; the two are separate identities, not one counter. The order
// allocator must be the same single counter the frames used.
func BuildSelfDestruct(order *Order, actions *ActionIndex, execActionID uint64, obs SelfDestructObservation) (ExecutionAction, *balance.BalanceAction, error) {
	entry, err := order.Allocate()
	if err != nil {
		return ExecutionAction{}, nil, err
	}
	actionIndex := actions.Next()
	action := SelfDestructAction{
		ContractAddress:         obs.Contract,
		Beneficiary:             obs.Beneficiary,
		BalanceAtEntry:          obs.BalanceAtEntry,
		EffectsCommitted:        !obs.RolledBack,
		DeletionEligibleByRules: obs.DeletionEligible,
	}
	// deletion_committed is the surviving result: a cancelled action never
	// commits its deletion, and eligibility plus the observed final
	// existence decide the rest.
	action.DeletionCommitted = action.EffectsCommitted && action.DeletionEligibleByRules && obs.AccountDeleted
	exec := ExecutionAction{
		ExecutionActionID: execActionID,
		ExecutionOrder:    entry,
		FrameID:           obs.FrameID,
		Payload:           ExecutionActionPayload{SelfDestruct: &action},
	}

	// The monetary projection: real observed effects only, never a second
	// invented transfer. Its shape depends on where native moved the funds.
	if len(obs.MovementEffects) == 0 {
		if obs.BalanceAtEntry.IsZero() || obs.Beneficiary == obs.Contract {
			// Zero balance, or funds left in place because the beneficiary
			// is the contract itself and the fork rules moved nothing (the
			// post-Cancun not-created-in-tx case): the opcode action stays
			// and the projection keeps the cause link with amount zero and
			// no effects. A movement of nothing is not a movement, so the
			// projection never claims committed effects (the shared
			// validator forbids committed effects without an effect list).
			proj := zeroMovementProjection(entry, execActionID, actionIndex, obs)
			return exec, &proj, nil
		}
		// A nonzero balance that native never moved anywhere is a source
		// inconsistency for the v1 registry: pre-Cancun always moves at
		// the opcode, post-Cancun either moves at finalization or leaves
		// the funds in place only when the beneficiary is the contract
		// itself.
		return ExecutionAction{}, nil, fmt.Errorf("%w: balance %s moved nowhere for beneficiary %x",
			ErrSelfDestructShape, obs.BalanceAtEntry.Decimal(), obs.Beneficiary)
	}

	amount, err := movementAmount(obs.MovementEffects)
	if err != nil {
		return ExecutionAction{}, nil, err
	}
	switch obs.MovementPosition {
	case MovementAtOpcode:
		// One action groups the debit of the contract and the credit of
		// the beneficiary of the same native transfer invocation,
		// including the self-beneficiary case where both sides land on one
		// address.
		proj := transferProjection(entry, execActionID, actionIndex, obs, amount)
		return exec, &proj, nil
	default:
		// Finalization-position movements are separate operations with
		// their own order at their actual position; see
		// BuildFinalizationBurn. Anything else is a source inconsistency.
		return ExecutionAction{}, nil, fmt.Errorf("%w: movement position %d on an opcode-position builder",
			ErrSelfDestructShape, uint8(obs.MovementPosition))
	}
}

// BuildFinalizationBurn normalizes the EIP-6780 finalization burn of a
// selfdestructed contract: the destroyed remainder at its actual
// finalization position, with a new execution_order of its own and the
// related execution action reference. The fact that a recipient is absent
// does not by itself prove a burn; the observation must show the burn
// effects. A finalization credit to a surviving beneficiary has no v1
// registry operation; the builder fails closed rather than inventing one
// (see README open questions).
func BuildFinalizationBurn(order *Order, actions *ActionIndex, protocols *ProtocolIndex, execActionID uint64, obs SelfDestructObservation) (*balance.BalanceAction, error) {
	if !movementIsBurn(obs) {
		return nil, fmt.Errorf("%w: finalization credit of a surviving beneficiary has no v1 operation",
			ErrSelfDestructRegistry)
	}
	if len(obs.MovementEffects) == 0 {
		return nil, nil
	}
	amount, err := movementAmount(obs.MovementEffects)
	if err != nil {
		return nil, err
	}
	burnOrder, err := order.Allocate()
	if err != nil {
		return nil, err
	}
	scope := uint64(0)
	from := obs.Contract
	return &balance.BalanceAction{
		ActionIndex:      actions.Next(),
		ExecutionOrder:   burnOrder,
		Cause:            balance.ProtocolCause(balance.OpSelfDestructFinalizationBurn, protocols.Next(), execActionID),
		Engine:           balance.EngineEVM,
		Kind:             balance.KindBurn,
		From:             &from,
		Amount:           amount,
		Asset:            balance.AssetNative,
		EffectsCommitted: !obs.RolledBack,
		RollbackScope:    &scope,
		Effects:          obs.MovementEffects,
	}, nil
}

// zeroMovementProjection is the kept-but-empty projection of a selfdestruct
// whose native movement was empty.
func zeroMovementProjection(entry, execActionID, actionIndex uint64, obs SelfDestructObservation) balance.BalanceAction {
	from := obs.Contract
	to := obs.Beneficiary
	scope := obs.Scope
	return balance.BalanceAction{
		ActionIndex:      actionIndex,
		ExecutionOrder:   entry,
		Cause:            balance.ExecutionActionCause(execActionID),
		Engine:           balance.EngineEVM,
		Kind:             balance.KindSelfDestructTransfer,
		From:             &from,
		To:               &to,
		Amount:           balance.UInt256{},
		Asset:            balance.AssetNative,
		EffectsCommitted: false,
		RollbackScope:    &scope,
	}
}

// transferProjection is the opcode-position movement.
func transferProjection(entry, execActionID, actionIndex uint64, obs SelfDestructObservation, amount UInt256) balance.BalanceAction {
	from := obs.Contract
	to := obs.Beneficiary
	scope := obs.Scope
	return balance.BalanceAction{
		ActionIndex:      actionIndex,
		ExecutionOrder:   entry,
		Cause:            balance.ExecutionActionCause(execActionID),
		Engine:           balance.EngineEVM,
		Kind:             balance.KindSelfDestructTransfer,
		From:             &from,
		To:               &to,
		Amount:           amount,
		Asset:            balance.AssetNative,
		EffectsCommitted: !obs.RolledBack,
		RollbackScope:    &scope,
		Effects:          obs.MovementEffects,
	}
}

// movementAmount derives the moved amount from the observed effects: the
// debit of the contract address.
func movementAmount(effects []balance.BalanceEffect) (UInt256, error) {
	for i := range effects {
		sign, mag := effects[i].Delta()
		if sign < 0 {
			return mag, nil
		}
	}
	return UInt256{}, fmt.Errorf("%w: movement effects carry no debit", ErrSelfDestructShape)
}

// movementIsBurn reports whether the finalization movement destroys the
// funds instead of crediting a surviving beneficiary: the account was
// deleted (its credit cannot exist) or the beneficiary is the deleted
// contract itself.
func movementIsBurn(obs SelfDestructObservation) bool {
	return obs.AccountDeleted || obs.Beneficiary == obs.Contract
}
