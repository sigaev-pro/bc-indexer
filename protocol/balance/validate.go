package balance

import (
	"errors"
	"fmt"
)

// Sentinel errors of the validator. Every rejection maps to one of them, so
// callers can distinguish violation classes without matching message text.
// The vectors they reject correspond to acceptance criterion AC-DAT-05-03.
var (
	// Scope list errors.
	ErrNoScopes              = errors.New("balance: rollback scope list is empty")
	ErrNoFrames              = errors.New("balance: an executed transaction always has at least the root frame")
	ErrScopeZeroMissing      = errors.New("balance: scope 0, the included-transaction boundary, is missing")
	ErrScopeZeroParent       = errors.New("balance: scope 0 must not have a parent scope")
	ErrScopeZeroOwner        = errors.New("balance: scope 0 must not have an owner frame")
	ErrScopeZeroRolledBack   = errors.New("balance: scope 0 is never rolled back for an included transaction")
	ErrScopeIDDuplicate      = errors.New("balance: duplicate rollback scope id")
	ErrScopeIDGap            = errors.New("balance: gap in rollback scope ids")
	ErrScopeParentAbsent     = errors.New("balance: non-boundary scope without a parent scope")
	ErrScopeParentMissing    = errors.New("balance: parent scope reference does not exist")
	ErrScopeParentOrder      = errors.New("balance: parent scope id must be smaller than the scope id")
	ErrScopeCycle            = errors.New("balance: rollback scope parent chain contains a cycle")
	ErrOwnerFrameUnknown     = errors.New("balance: owner frame reference does not exist")
	ErrRollbackNotPropagated = errors.New("balance: a descendant of a rolled back scope must itself be rolled back")

	// Ordering errors, shared by the single execution_order counter.
	ErrOrderInvalid       = errors.New("balance: execution_order must start at 1")
	ErrOrderDuplicate     = errors.New("balance: duplicate execution_order")
	ErrOrderGap           = errors.New("balance: execution_order values must form the continuous range 1..N")
	ErrFalseOrderEquality = errors.New("balance: equal execution_order is allowed only for an execution action and its exact monetary projection")
	ErrCauseOrderMismatch = errors.New("balance: an action with an execution action cause must share that execution action's execution_order")

	// Action list errors.
	ErrListUnsorted                   = errors.New("balance: list is not stored in ascending order")
	ErrActionIndexDuplicate           = errors.New("balance: duplicate action_index")
	ErrCauseKindUnknown               = errors.New("balance: cause kind is unspecified")
	ErrCauseOperationUnknown          = errors.New("balance: cause operation is unspecified")
	ErrCauseKindMismatch              = errors.New("balance: action kind does not match its cause")
	ErrCauseRefUnknown                = errors.New("balance: cause reference does not exist in this transaction")
	ErrEngineMismatch                 = errors.New("balance: engine does not match the cause")
	ErrAssetNotNative                 = errors.New("balance: asset must be NATIVE in the closed v1 registry")
	ErrRollbackScopeAbsentWithEffects = errors.New("balance: an action with effects must carry an explicit rollback scope; absence is not an unknown scope")
	ErrRollbackScopeUnknown           = errors.New("balance: rollback scope reference does not exist")
	ErrCommittedInRolledBackScope     = errors.New("balance: effects_committed is forbidden inside a rolled back scope")
	ErrCommittedWithoutEffects        = errors.New("balance: effects_committed requires effects")
	ErrEffectIndex                    = errors.New("balance: effect_index must be 0..len-1 without gaps")
	ErrEffectChain                    = errors.New("balance: effects on one address inside one action must chain")
	ErrEffectShape                    = errors.New("balance: effects do not match the action kind shape")
	ErrFeeScope                       = errors.New("balance: fee actions live at the non-rolled-back transaction boundary scope 0")
	ErrFeeNotCommitted                = errors.New("balance: fee actions are actual movements and must be committed")
	ErrRelatedActionsMissing          = errors.New("balance: this operation must reference its related execution actions")

	// Fee summary errors.
	ErrSummaryMismatch = errors.New("balance: fee summary does not equal the aggregate of the fee actions")

	// Boundary reconciliation errors (ReconcileBoundary).
	ErrBoundaryBaselineMissing = errors.New("balance: committed effect address has no before point")
	ErrBoundaryAfterMissing    = errors.New("balance: reconciled address has no after point")
	ErrBoundaryUnexplained     = errors.New("balance: boundary change without committed effects")
	ErrBoundaryMismatch        = errors.New("balance: committed effects do not reconcile with the boundary points")
)

// FrameRef is the minimal presence view of one CallFrame of the same
// transaction: its tx-local identity and its entry execution_order. The full
// CallFrame type belongs to the calls model, not to this package.
type FrameRef struct {
	FrameID        uint64
	ExecutionOrder uint64
}

// ExecutionActionRef is the minimal presence view of one ExecutionAction of
// the same transaction: its tx-local identity and its execution_order.
type ExecutionActionRef struct {
	ExecutionActionID uint64
	ExecutionOrder    uint64
}

// TxBalances is one transaction's balance view: the rollback scopes, the
// causal monetary actions, the presence views of the frames and execution
// actions they may reference, and the optional receipt fee summary.
//
// The lists must be stored sorted: scopes by scope_id, actions by
// execution_order, frames and execution actions by their identities.
type TxBalances struct {
	Scopes           []RollbackScope
	Actions          []BalanceAction
	Frames           []FrameRef
	ExecutionActions []ExecutionActionRef
	FeeSummary       *FeeSummary
}

type referenceSets struct {
	scopes     map[uint64]RollbackScope
	frames     map[uint64]FrameRef
	execAction map[uint64]ExecutionActionRef
}

// Validate checks the structural invariants of one transaction's balance
// view before READY (CURRENT_SEMANTICS D74): order continuity, uniqueness
// and the single shared-order rule; the legality of every cause; all
// tx-local references; the scope tree with parent<child, proven owner frames
// and rollback transitivity; the prohibition of committed effects inside a
// rolled-back scope; the fee-scope rule; and the aggregate equality of the
// fee summary.
//
// It returns nil when the record is valid, and otherwise one error joined
// from every violation found. A valid record must contain at least scope 0
// and at least one frame: an executed transaction always has its root frame.
func Validate(tx TxBalances) error {
	var errs []error

	refs := referenceSets{
		scopes:     make(map[uint64]RollbackScope, len(tx.Scopes)),
		frames:     make(map[uint64]FrameRef, len(tx.Frames)),
		execAction: make(map[uint64]ExecutionActionRef, len(tx.ExecutionActions)),
	}
	for _, f := range tx.Frames {
		refs.frames[f.FrameID] = f
	}
	for _, ea := range tx.ExecutionActions {
		refs.execAction[ea.ExecutionActionID] = ea
	}

	errs = append(errs, validateScopeList(tx.Scopes, refs.frames, refs.scopes)...)
	errs = append(errs, validateActionList(tx.Actions, refs)...)
	errs = append(errs, validateExecutionOrders(tx)...)
	if tx.FeeSummary != nil {
		errs = append(errs, validateFeeSummary(tx)...)
	}
	if len(tx.Frames) == 0 {
		errs = append(errs, ErrNoFrames)
	}
	return errors.Join(errs...)
}

func validateScopeList(scopes []RollbackScope, frames map[uint64]FrameRef, byID map[uint64]RollbackScope) []error {
	if len(scopes) == 0 {
		return []error{ErrNoScopes}
	}
	var errs []error
	prev := int64(-1)
	for i, s := range scopes {
		id := int64(s.ScopeID)
		if id <= prev {
			errs = append(errs, fmt.Errorf("scope %d: %w", s.ScopeID, ErrScopeIDDuplicate))
		} else if id != prev+1 {
			errs = append(errs, fmt.Errorf("scope %d at position %d: %w", s.ScopeID, i, ErrScopeIDGap))
		}
		prev = id
		byID[s.ScopeID] = s
	}
	if scopes[0].ScopeID != 0 {
		errs = append(errs, ErrScopeZeroMissing)
	} else {
		s0 := scopes[0]
		if s0.ParentScopeID != nil {
			errs = append(errs, fmt.Errorf("scope 0 parent %d: %w", *s0.ParentScopeID, ErrScopeZeroParent))
		}
		if s0.OwnerFrameID != nil {
			errs = append(errs, fmt.Errorf("scope 0 owner frame %d: %w", *s0.OwnerFrameID, ErrScopeZeroOwner))
		}
		if s0.RolledBack {
			errs = append(errs, ErrScopeZeroRolledBack)
		}
	}

	for _, s := range scopes {
		if s.ScopeID == 0 {
			continue
		}
		if s.ParentScopeID == nil {
			errs = append(errs, fmt.Errorf("scope %d: %w", s.ScopeID, ErrScopeParentAbsent))
			continue
		}
		if _, ok := byID[*s.ParentScopeID]; !ok {
			errs = append(errs, fmt.Errorf("scope %d parent %d: %w", s.ScopeID, *s.ParentScopeID, ErrScopeParentMissing))
			continue
		}
		if hasScopeCycle(s.ScopeID, byID) {
			errs = append(errs, fmt.Errorf("scope %d: %w", s.ScopeID, ErrScopeCycle))
			continue
		}
		if *s.ParentScopeID >= s.ScopeID {
			errs = append(errs, fmt.Errorf("scope %d parent %d: %w", s.ScopeID, *s.ParentScopeID, ErrScopeParentOrder))
		}
		if s.OwnerFrameID != nil {
			if _, ok := frames[*s.OwnerFrameID]; !ok {
				errs = append(errs, fmt.Errorf("scope %d owner frame %d: %w", s.ScopeID, *s.OwnerFrameID, ErrOwnerFrameUnknown))
			}
		}
	}

	// Rollback propagation: the final rolled_back of a scope includes its
	// ancestors. A descendant that is not marked rolled back under a rolled
	// back ancestor is invalid.
	for _, s := range scopes {
		if s.ScopeID == 0 || s.RolledBack {
			continue
		}
		if scopeHasRolledBackAncestor(s.ScopeID, byID) {
			errs = append(errs, fmt.Errorf("scope %d: %w", s.ScopeID, ErrRollbackNotPropagated))
		}
	}
	return errs
}

func hasScopeCycle(start uint64, byID map[uint64]RollbackScope) bool {
	seen := make(map[uint64]bool)
	cur := start
	for steps := 0; steps <= len(byID); steps++ {
		if cur == 0 || seen[cur] {
			return seen[cur]
		}
		seen[cur] = true
		s, ok := byID[cur]
		if !ok || s.ParentScopeID == nil {
			return false
		}
		cur = *s.ParentScopeID
	}
	return true
}

func scopeHasRolledBackAncestor(start uint64, byID map[uint64]RollbackScope) bool {
	cur := start
	for steps := 0; steps <= len(byID); steps++ {
		s, ok := byID[cur]
		if !ok || s.ParentScopeID == nil {
			return false
		}
		cur = *s.ParentScopeID
		parent, ok := byID[cur]
		if !ok {
			return false
		}
		if parent.RolledBack {
			return true
		}
	}
	return false
}

func validateActionList(actions []BalanceAction, refs referenceSets) []error {
	var errs []error
	seenIndex := make(map[uint64]bool, len(actions))
	for i := range actions {
		a := &actions[i]
		if i > 0 {
			switch {
			case a.ExecutionOrder < actions[i-1].ExecutionOrder:
				errs = append(errs, fmt.Errorf("action %d: %w", a.ActionIndex, ErrListUnsorted))
			case a.ExecutionOrder == actions[i-1].ExecutionOrder:
				errs = append(errs, fmt.Errorf("action %d order %d: %w", a.ActionIndex, a.ExecutionOrder, ErrOrderDuplicate))
			}
		}
		if seenIndex[a.ActionIndex] {
			errs = append(errs, fmt.Errorf("action index %d: %w", a.ActionIndex, ErrActionIndexDuplicate))
		}
		seenIndex[a.ActionIndex] = true
		errs = append(errs, validateAction(a, refs)...)
	}
	return errs
}

func validateAction(a *BalanceAction, refs referenceSets) []error {
	var errs []error
	if a.ExecutionOrder == 0 {
		errs = append(errs, fmt.Errorf("action %d: %w", a.ActionIndex, ErrOrderInvalid))
	}
	if a.Asset != AssetNative {
		errs = append(errs, fmt.Errorf("action %d asset %s: %w", a.ActionIndex, a.Asset, ErrAssetNotNative))
	}
	errs = append(errs, validateCauseShape(a, refs)...)

	if len(a.Effects) > 0 && a.RollbackScope == nil {
		errs = append(errs, fmt.Errorf("action %d: %w", a.ActionIndex, ErrRollbackScopeAbsentWithEffects))
	}
	if a.RollbackScope != nil {
		scope, ok := refs.scopes[*a.RollbackScope]
		if !ok {
			errs = append(errs, fmt.Errorf("action %d scope %d: %w", a.ActionIndex, *a.RollbackScope, ErrRollbackScopeUnknown))
		} else if scope.RolledBack && a.EffectsCommitted {
			errs = append(errs, fmt.Errorf("action %d scope %d: %w", a.ActionIndex, *a.RollbackScope, ErrCommittedInRolledBackScope))
		}
	}
	if a.EffectsCommitted && len(a.Effects) == 0 {
		errs = append(errs, fmt.Errorf("action %d: %w", a.ActionIndex, ErrCommittedWithoutEffects))
	}

	lastAfter := make(map[Address20]UInt256)
	for i, e := range a.Effects {
		if e.EffectIndex != uint64(i) {
			errs = append(errs, fmt.Errorf("action %d effect %d index %d: %w", a.ActionIndex, i, e.EffectIndex, ErrEffectIndex))
		}
		if prev, ok := lastAfter[e.Address]; ok && e.Before != prev {
			errs = append(errs, fmt.Errorf("action %d effect %d address %x: %w", a.ActionIndex, i, e.Address, ErrEffectChain))
		}
		lastAfter[e.Address] = e.After
	}
	return errs
}

// validateCauseShape checks the cause branch, the engine, the closed
// kind/operation pairing and the effect shapes of the D62 table.
func validateCauseShape(a *BalanceAction, refs referenceSets) []error {
	var errs []error
	fail := func(err error) {
		errs = append(errs, fmt.Errorf("action %d cause %s: %w", a.ActionIndex, a.Cause, err))
	}

	switch a.Cause.Kind {
	case CauseFrame:
		if _, ok := refs.frames[a.Cause.FrameID]; !ok {
			fail(ErrCauseRefUnknown)
		}
		if a.Engine != EngineEVM {
			fail(ErrEngineMismatch)
		}
		if a.Kind != KindTransfer {
			fail(ErrCauseKindMismatch)
		}
		errs = append(errs, checkTransferShape(a)...)

	case CauseExecutionAction:
		ea, ok := refs.execAction[a.Cause.ExecutionActionID]
		if !ok {
			fail(ErrCauseRefUnknown)
		} else if ea.ExecutionOrder != a.ExecutionOrder {
			fail(ErrCauseOrderMismatch)
		}
		if a.Engine != EngineEVM {
			fail(ErrEngineMismatch)
		}
		if a.Kind != KindSelfDestructTransfer {
			fail(ErrCauseKindMismatch)
		}
		errs = append(errs, checkTransferShape(a)...)

	case CauseTransactionProtocol:
		if a.Cause.Operation == OpUnspecified {
			fail(ErrCauseOperationUnknown)
			return errs
		}
		switch a.Cause.Operation {
		case OpGasPurchase:
			if a.Engine != EngineEVM || a.Kind != KindFee || !addrPresent(a.From) || addrPresent(a.To) {
				fail(ErrCauseKindMismatch)
			}
			errs = append(errs, checkSingleDebit(a)...)
		case OpGasReturn:
			if a.Engine != EngineEVM || a.Kind != KindRefund || addrPresent(a.From) || !addrPresent(a.To) {
				fail(ErrCauseKindMismatch)
			}
			errs = append(errs, checkSingleCredit(a)...)
		case OpFeeRecipientCredit:
			if a.Engine != EngineEVM || a.Kind != KindFee || addrPresent(a.From) || !addrPresent(a.To) {
				fail(ErrCauseKindMismatch)
			}
			errs = append(errs, checkSingleCredit(a)...)
		case OpSelfDestructFinalizationBurn:
			if a.Engine != EngineEVM || a.Kind != KindBurn || !addrPresent(a.From) || addrPresent(a.To) {
				fail(ErrCauseKindMismatch)
			}
			if len(a.Cause.RelatedExecutionActions) == 0 {
				fail(ErrRelatedActionsMissing)
			}
			for _, id := range a.Cause.RelatedExecutionActions {
				if _, ok := refs.execAction[id]; !ok {
					errs = append(errs, fmt.Errorf("action %d related execution action %d: %w", a.ActionIndex, id, ErrCauseRefUnknown))
				}
			}
			errs = append(errs, checkSingleDebit(a)...)
		case OpBSCSystemExecution, OpBorStateSyncExecution:
			// The actual effects of a known native system operation are kept
			// as-is; the From/To fields follow the operation and no economic
			// transfer shape is invented.
			if a.Engine != EngineNativeProtocol || a.Kind != KindNativeProtocolChange {
				fail(ErrCauseKindMismatch)
			}
		}
		if a.Cause.Operation.IsFeeOperation() {
			if a.RollbackScope == nil || *a.RollbackScope != 0 {
				fail(ErrFeeScope)
			}
			if !a.EffectsCommitted {
				fail(ErrFeeNotCommitted)
			}
		}

	default:
		fail(ErrCauseKindUnknown)
	}
	return errs
}

// checkTransferShape verifies the two-sided shape of a native transfer
// invocation: one action groups the debit and the credit, including the
// self-address case, where both sides land on one address and the effects
// stay net-zero.
func checkTransferShape(a *BalanceAction) []error {
	var errs []error
	fail := func(reason string) {
		errs = append(errs, fmt.Errorf("action %d: %s: %w", a.ActionIndex, reason, ErrEffectShape))
	}
	if !addrPresent(a.From) || !addrPresent(a.To) {
		fail("transfer requires both from and to")
		return errs
	}
	if len(a.Effects) == 0 {
		return errs
	}
	if *a.From == *a.To {
		// Both sides are kept even when they land on one address: either as
		// the chained pair of native mutations, debit then credit, or as the
		// single collapsed no-op point. Either way the action stays present
		// with a net-zero effect.
		switch len(a.Effects) {
		case 1:
			e := a.Effects[0]
			if e.Address != *a.From || e.Before != e.After {
				fail("a single self-address effect must be the net-zero point")
			}
		case 2:
			first, second := a.Effects[0], a.Effects[1]
			if first.Address != *a.From || second.Address != *a.From {
				fail("self-address effects must stay on the single address")
				return errs
			}
			if !afterIsDifference(first.Before, first.After, a.Amount) {
				fail("self-address debit side after != before - amount")
			}
			if !afterIsSum(second.Before, second.After, a.Amount) {
				fail("self-address credit side after != before + amount")
			}
		default:
			fail("a self-address action keeps one or two effects, not more")
		}
		return errs
	}
	if len(a.Effects) != 2 {
		fail("a transfer keeps both sides, debit and credit")
		return errs
	}
	debit, credit := &a.Effects[0], &a.Effects[1]
	if debit.Address != *a.From {
		debit, credit = credit, debit
	}
	if debit.Address != *a.From || credit.Address != *a.To {
		fail("transfer effects must touch exactly from and to")
		return errs
	}
	if !afterIsDifference(debit.Before, debit.After, a.Amount) {
		fail("debit side after != before - amount")
	}
	if !afterIsSum(credit.Before, credit.After, a.Amount) {
		fail("credit side after != before + amount")
	}
	return errs
}

func checkSingleDebit(a *BalanceAction) []error {
	var errs []error
	if len(a.Effects) == 0 {
		return errs
	}
	if len(a.Effects) != 1 || a.From == nil || a.Effects[0].Address != *a.From ||
		!afterIsDifference(a.Effects[0].Before, a.Effects[0].After, a.Amount) {
		errs = append(errs, fmt.Errorf("action %d: %w", a.ActionIndex, ErrEffectShape))
	}
	return errs
}

func checkSingleCredit(a *BalanceAction) []error {
	var errs []error
	if len(a.Effects) == 0 {
		return errs
	}
	if len(a.Effects) != 1 || a.To == nil || a.Effects[0].Address != *a.To ||
		!afterIsSum(a.Effects[0].Before, a.Effects[0].After, a.Amount) {
		errs = append(errs, fmt.Errorf("action %d: %w", a.ActionIndex, ErrEffectShape))
	}
	return errs
}

func addrPresent(p *Address20) bool { return p != nil }

func afterIsSum(before, after, delta UInt256) bool {
	got := before.Big()
	got.Add(got, delta.Big())
	return got.Cmp(after.Big()) == 0
}

func afterIsDifference(before, after, delta UInt256) bool {
	got := before.Big()
	got.Sub(got, delta.Big())
	return got.Sign() >= 0 && got.Cmp(after.Big()) == 0
}

// validateExecutionOrders enforces the single tx-local counter rules of D74:
// values start at 1, are unique inside each list, may be equal across lists
// only for the pair of an execution action and the balance action whose
// cause names that exact execution action, and together form the continuous
// range 1..N.
func validateExecutionOrders(tx TxBalances) []error {
	var errs []error
	frameOrders := make(map[uint64]uint64, len(tx.Frames))
	for _, f := range tx.Frames {
		if f.ExecutionOrder == 0 {
			errs = append(errs, fmt.Errorf("frame %d: %w", f.FrameID, ErrOrderInvalid))
		}
		if other, ok := frameOrders[f.ExecutionOrder]; ok {
			errs = append(errs, fmt.Errorf("frames %d and %d order %d: %w", other, f.FrameID, f.ExecutionOrder, ErrOrderDuplicate))
		}
		frameOrders[f.ExecutionOrder] = f.FrameID
	}
	execOrders := make(map[uint64]ExecutionActionRef, len(tx.ExecutionActions))
	for _, ea := range tx.ExecutionActions {
		if ea.ExecutionOrder == 0 {
			errs = append(errs, fmt.Errorf("execution action %d: %w", ea.ExecutionActionID, ErrOrderInvalid))
		}
		if _, ok := execOrders[ea.ExecutionOrder]; ok {
			errs = append(errs, fmt.Errorf("execution actions order %d: %w", ea.ExecutionOrder, ErrOrderDuplicate))
		}
		execOrders[ea.ExecutionOrder] = ea
	}

	paired := make(map[uint64]int, len(tx.Actions))
	orders := make(map[uint64]bool, len(tx.Frames)+len(tx.ExecutionActions)+len(tx.Actions))
	for o := range frameOrders {
		orders[o] = true
	}
	for o := range execOrders {
		if _, ok := frameOrders[o]; ok {
			errs = append(errs, fmt.Errorf("order %d: %w", o, ErrFalseOrderEquality))
		}
		orders[o] = true
	}
	for i := range tx.Actions {
		a := &tx.Actions[i]
		o := a.ExecutionOrder
		if o == 0 {
			continue
		}
		if _, ok := frameOrders[o]; ok {
			errs = append(errs, fmt.Errorf("action %d order %d equals frame entry: %w", a.ActionIndex, o, ErrFalseOrderEquality))
		}
		if ea, ok := execOrders[o]; ok {
			exact := a.Cause.Kind == CauseExecutionAction && a.Cause.ExecutionActionID == ea.ExecutionActionID
			if !exact {
				errs = append(errs, fmt.Errorf("action %d order %d equals execution action %d: %w", a.ActionIndex, o, ea.ExecutionActionID, ErrFalseOrderEquality))
			}
			paired[ea.ExecutionActionID]++
			if paired[ea.ExecutionActionID] > 1 {
				errs = append(errs, fmt.Errorf("execution action %d paired by several actions: %w", ea.ExecutionActionID, ErrFalseOrderEquality))
			}
		}
		orders[o] = true
	}

	if len(orders) > 0 {
		maxOrder := uint64(0)
		for o := range orders {
			if o > maxOrder {
				maxOrder = o
			}
		}
		if uint64(len(orders)) != maxOrder {
			errs = append(errs, fmt.Errorf("orders span 1..%d with %d distinct values: %w", maxOrder, len(orders), ErrOrderGap))
		}
	}
	return errs
}

// validateFeeSummary enforces the aggregate view rule: every summary field
// must equal the exact aggregate of the fee actions present. A summary that
// adds movements on top of the actions, or that misses fee actions, is
// rejected; this is what keeps the receipt summary from double-counting.
func validateFeeSummary(tx TxBalances) []error {
	s := tx.FeeSummary
	var errs []error
	fail := func(reason string) {
		errs = append(errs, fmt.Errorf("fee summary: %s: %w", reason, ErrSummaryMismatch))
	}

	var purchase, ret UInt256
	var payer *Address20
	var credits []BalanceAction
	for i := range tx.Actions {
		a := &tx.Actions[i]
		if a.Cause.Kind != CauseTransactionProtocol {
			continue
		}
		switch a.Cause.Operation {
		case OpGasPurchase:
			sum, err := purchase.AddChecked(a.Amount)
			if err != nil {
				fail("gas purchase aggregate overflow")
			} else {
				purchase = sum
			}
			if payer != nil && (a.From == nil || *payer != *a.From) {
				fail("several gas purchase payers")
			}
			if a.From != nil {
				payer = a.From
			}
		case OpGasReturn:
			sum, err := ret.AddChecked(a.Amount)
			if err != nil {
				fail("gas return aggregate overflow")
			} else {
				ret = sum
			}
		case OpFeeRecipientCredit:
			credits = append(credits, *a)
		}
	}

	if purchase != s.GasPurchaseDebit {
		fail(fmt.Sprintf("gas_purchase_debit %s does not aggregate the purchase actions %s", s.GasPurchaseDebit.Decimal(), purchase.Decimal()))
	}
	if ret != s.GasReturnCredit {
		fail(fmt.Sprintf("gas_return_credit %s does not aggregate the return actions %s", s.GasReturnCredit.Decimal(), ret.Decimal()))
	}
	net, err := purchase.SubChecked(ret)
	if err != nil {
		fail("net charge underflow")
	} else if net != s.NetCharge {
		fail(fmt.Sprintf("net_charge %s != purchase %s - return %s", s.NetCharge.Decimal(), purchase.Decimal(), ret.Decimal()))
	}
	if len(credits) != len(s.RecipientCredits) {
		fail(fmt.Sprintf("%d credit actions but %d summary credits", len(credits), len(s.RecipientCredits)))
	} else {
		for i, c := range credits {
			if c.To == nil || *c.To != s.RecipientCredits[i].Address || c.Amount != s.RecipientCredits[i].Amount {
				fail(fmt.Sprintf("credit %d does not match its credit action", i))
			}
		}
	}
	if payer != nil {
		if s.Payer == nil {
			fail("purchase actions exist but the summary payer is absent")
		} else if *s.Payer != *payer {
			fail("summary payer does not match the purchase payer")
		}
	}
	return errs
}
