package bsc

import (
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/authorization"
	bscbalance "github.com/sigaev-pro/bc-indexer/core/normalization/bsc/balance"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/calls"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/receipt"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/selfdestruct"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/state"
	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// Sentinel errors of the normalizer.
var (
	ErrEventShape        = errors.New("bsc: exactly one event field must be set")
	ErrAuthorization     = errors.New("bsc: the envelope carries authorization tuples the profile does not accept")
	ErrUnknownSnapshot   = errors.New("bsc: an event references a native snapshot that is not active")
	ErrSnapshotReused    = errors.New("bsc: a native snapshot id is created while still active")
	ErrParentFrameOrder  = errors.New("bsc: a frame entry references a parent frame that does not exist yet")
	ErrExitWithoutEntry  = errors.New("bsc: a frame exit without its entry")
	ErrReceiptTypeDrift  = errors.New("bsc: the native receipt type differs from the included wire type")
	ErrBoundaryNormalize = errors.New("bsc: the boundary rows do not normalize")
)

// Normalizer folds one captured transaction observation into the full
// record view. It is deterministic: the same observation always normalizes
// to the same logical IDs, orders and scopes, which is what capture
// recovery verifies by re-normalizing and comparing the prefix.
type Normalizer struct {
	// Classifier performs the Parlia classification.
	Classifier Classifier
	// Authorization is the closed authorization profile; the zero value
	// is the current empty BSC profile.
	Authorization authorization.Profile
}

// New returns a normalizer with the default BSC profile pieces.
func New() *Normalizer {
	return &Normalizer{
		Classifier:    Classifier{SystemContracts: DefaultSystemContracts()},
		Authorization: authorization.Current(),
	}
}

// scopeState tracks one allocated normalized scope.
type scopeState struct {
	scope    balance.RollbackScope
	nativeID uint64
}

// pendingGroup is one mutation group waiting for the final rolled-back
// facts.
type pendingGroup struct {
	group    bscbalance.Group
	scope    uint64
	pairedID uint64 // non-zero: shares the SELFDESTRUCT execution action's order
}

// pendingDestruct is one SELFDESTRUCT execution action with its resolved
// projection scope.
type pendingDestruct struct {
	action          selfdestruct.Action
	frameExitScope  uint64 // native snapshot at the executing frame's exit
	projectionScope uint64
	hasProjection   bool
}

// Normalize folds one observation into the record view and validates it.
// Every allocation — frame ids, execution orders, scope ids, action
// indices, protocol operation indices — is tx-local and deterministic in
// observation order.
func (n *Normalizer) Normalize(obs *Observation) (*Record, error) {
	if _, err := n.Authorization.Accept(obs.Envelope.Authorizations); err != nil {
		return nil, fmt.Errorf("position %d: %w", obs.Envelope.Position, ErrAuthorization)
	}
	classification, err := n.Classifier.Classify(obs.Envelope, obs.Coinbase)
	if err != nil {
		return nil, err
	}

	builder := bscbalance.NewBuilder()
	var (
		frames     []calls.Frame
		frameExits = make(map[uint64]uint64) // frame id -> resolved exit scope
		scopes     []scopeState
		active     []int // stack of indices into scopes
		nativeOpen = make(map[uint64]int)
		order      uint64
		frameID    uint64
		destructs  []pendingDestruct
		groups     []pendingGroup
	)

	resolveActive := func(nativeID uint64) (uint64, error) {
		if nativeID == 0 {
			return 0, nil
		}
		idx, ok := nativeOpen[nativeID]
		if !ok {
			return 0, fmt.Errorf("%w: native snapshot %d", ErrUnknownSnapshot, nativeID)
		}
		return scopes[idx].scope.ScopeID, nil
	}
	innermost := func() uint64 {
		if len(active) == 0 {
			return 0
		}
		return scopes[active[len(active)-1]].scope.ScopeID
	}
	popThrough := func(idx int) {
		for len(active) > 0 && active[len(active)-1] != idx {
			delete(nativeOpen, scopes[active[len(active)-1]].nativeID)
			active = active[:len(active)-1]
		}
		if len(active) > 0 {
			delete(nativeOpen, scopes[active[len(active)-1]].nativeID)
			active = active[:len(active)-1]
		}
	}

	for i := range obs.Events {
		ev := &obs.Events[i]
		set := 0
		for _, p := range []bool{ev.Snapshot != nil, ev.FrameEntry != nil, ev.FrameExit != nil,
			ev.Group != nil, ev.SelfDestruct != nil, ev.FinalizationBurn != nil} {
			if p {
				set++
			}
		}
		if set != 1 {
			return nil, fmt.Errorf("event %d: %w", i, ErrEventShape)
		}
		switch {
		case ev.Snapshot != nil:
			se := ev.Snapshot
			switch se.Kind {
			case SnapshotCreate:
				if _, open := nativeOpen[se.NativeID]; open {
					return nil, fmt.Errorf("%w: native snapshot %d", ErrSnapshotReused, se.NativeID)
				}
				id := uint64(len(scopes) + 1)
				parent := innermost()
				var owner *uint64
				if se.OwnerFrameIndex != nil {
					owner = se.OwnerFrameIndex
				}
				scopes = append(scopes, scopeState{
					scope:    balance.RollbackScope{ScopeID: id, ParentScopeID: &parent, OwnerFrameID: owner},
					nativeID: se.NativeID,
				})
				nativeOpen[se.NativeID] = len(scopes) - 1
				active = append(active, len(scopes)-1)
			case SnapshotRevert:
				idx, ok := nativeOpen[se.NativeID]
				if !ok {
					return nil, fmt.Errorf("%w: native snapshot %d", ErrUnknownSnapshot, se.NativeID)
				}
				markRolledBack(scopes, scopes[idx].scope.ScopeID)
				popThrough(idx)
			case SnapshotEnd:
				idx, ok := nativeOpen[se.NativeID]
				if !ok {
					return nil, fmt.Errorf("%w: native snapshot %d", ErrUnknownSnapshot, se.NativeID)
				}
				popThrough(idx)
			default:
				return nil, fmt.Errorf("event %d: %w", i, ErrEventShape)
			}

		case ev.FrameEntry != nil:
			frame := *ev.FrameEntry
			if frame.ParentID != nil && *frame.ParentID >= frameID {
				return nil, fmt.Errorf("event %d: %w: parent %d of %d", i, ErrParentFrameOrder, *frame.ParentID, frameID)
			}
			frame.FrameID = frameID
			order++
			frame.ExecutionOrder = order
			frames = append(frames, frame)
			frameID++

		case ev.FrameExit != nil:
			if ev.FrameExit.FrameIndex >= frameID {
				return nil, fmt.Errorf("event %d: %w: frame %d", i, ErrExitWithoutEntry, ev.FrameExit.FrameIndex)
			}
			exitScope, err := resolveExitScope(scopes, nativeOpen, ev.FrameExit.ExitNativeSnapshot)
			if err != nil {
				return nil, fmt.Errorf("event %d frame %d exit: %w", i, ev.FrameExit.FrameIndex, err)
			}
			frameExits[ev.FrameExit.FrameIndex] = exitScope

		case ev.Group != nil:
			order++
			g := *ev.Group
			g.ExecutionOrder = order
			scopeID, err := resolveActive(g.NativeSnapshot)
			if err != nil {
				return nil, fmt.Errorf("event %d op %d: %w", i, g.OpID, err)
			}
			groups = append(groups, pendingGroup{group: g, scope: scopeID})

		case ev.SelfDestruct != nil:
			order++
			pd := pendingDestruct{action: selfdestruct.Action{
				ExecutionActionID:       uint64(len(destructs) + 1),
				ExecutionOrder:          order,
				FrameID:                 ev.SelfDestruct.FrameIndex,
				Contract:                ev.SelfDestruct.Contract,
				Beneficiary:             ev.SelfDestruct.Beneficiary,
				BalanceAtEntry:          ev.SelfDestruct.BalanceAtEntry,
				DeletionEligibleByRules: ev.SelfDestruct.DeletionEligible,
			}}
			if ev.SelfDestruct.Projection != nil {
				proj := *ev.SelfDestruct.Projection
				proj.ExecutionOrder = order
				scopeID, err := resolveActive(proj.NativeSnapshot)
				if err != nil {
					return nil, fmt.Errorf("event %d selfdestruct %d: %w", i, pd.action.ExecutionActionID, err)
				}
				pd.projectionScope = scopeID
				pd.hasProjection = true
				groups = append(groups, pendingGroup{
					group: proj, scope: scopeID, pairedID: pd.action.ExecutionActionID,
				})
			}
			destructs = append(destructs, pd)

		case ev.FinalizationBurn != nil:
			order++
			g := ev.FinalizationBurn.Group
			g.ExecutionOrder = order
			g.Role = bscbalance.RoleFinalizationBurn
			g.RelatedExecutionActions = ev.FinalizationBurn.RelatedExecutionActions
			scopeID, err := resolveActive(g.NativeSnapshot)
			if err != nil {
				return nil, fmt.Errorf("event %d burn: %w", i, err)
			}
			groups = append(groups, pendingGroup{group: g, scope: scopeID})
		}
	}

	// Final rolled-back facts, then the typed actions.
	rolledBack := make(map[uint64]bool, len(scopes))
	for i := range scopes {
		if scopes[i].scope.RolledBack {
			rolledBack[scopes[i].scope.ScopeID] = true
		}
	}
	scopeByID := make(map[uint64]balance.RollbackScope, len(scopes)+1)
	scopeByID[0] = balance.TxBoundaryScope()
	for i := range scopes {
		scopeByID[scopes[i].scope.ScopeID] = scopes[i].scope
	}

	for _, pg := range groups {
		scopePtr := pg.scope
		committed := !rolledBackTransitively(scopes, pg.scope)
		var err error
		if pg.pairedID != 0 {
			_, err = builder.AddSelfDestructProjection(pg.group, pg.pairedID, pg.group.ExecutionOrder, &scopePtr, committed)
		} else {
			_, err = builder.Add(pg.group, &scopePtr, committed)
		}
		if err != nil {
			return nil, err
		}
	}
	actions := builder.Actions()

	// Frame commit resolution: a frame's effects committed only when it
	// succeeded locally and the scope at its exit survived every
	// rollback. A frame without an observed exit resolved at the
	// transaction end: scope 0.
	for i := range frames {
		frames[i].EffectsCommitted = frames[i].Outcome == calls.OutcomeSuccess &&
			!rolledBackTransitively(scopes, frameExits[frames[i].FrameID])
	}

	// Receipt: the native receipt type must match the included wire type
	// after the BSC actualTx substitution; the encoding is selected from
	// the actual type and the source consensus bytes are kept as spooled.
	if obs.Receipt.NativeReceiptType != uint32(obs.Envelope.NativeType) {
		return nil, fmt.Errorf("position %d: %w: receipt %d wire %d",
			obs.Envelope.Position, ErrReceiptTypeDrift, obs.Receipt.NativeReceiptType, obs.Envelope.NativeType)
	}
	encoding, err := receipt.NewEncoding(obs.Receipt.NativeReceiptType, obs.Receipt.ConsensusBytes)
	if err != nil {
		return nil, err
	}
	summary, err := bscbalance.FeeSummary(actions)
	if err != nil {
		return nil, err
	}
	if classification == ClassificationParliaSystem && summary.Payer != nil {
		// A zero-fee system transaction stores applicable zeros with an
		// absent payer; a purchase action contradicts the zero gas price
		// the classification proved.
		return nil, fmt.Errorf("position %d: %w: system transaction with a fee payer",
			obs.Envelope.Position, ErrSystemSender)
	}
	typed := &receipt.Receipt{
		Status:             obs.Receipt.Status,
		PreStatusStateRoot: obs.Receipt.PreStatusStateRoot,
		Result:             obs.Receipt.Result,
		CumulativeGasUsed:  obs.Receipt.CumulativeGasUsed,
		GasUsed:            obs.Receipt.GasUsed,
		EffectiveGasPrice:  obs.Receipt.EffectiveGasPrice,
		BlobGasUsed:        obs.Receipt.BlobGasUsed,
		BlobGasPrice:       obs.Receipt.BlobGasPrice,
		Logs:               obs.Receipt.Logs,
		LogsBloom:          obs.Receipt.LogsBloom,
		Encoding:           encoding,
		FeeSummary:         summary,
		Details:            &receipt.BscReceiptDetails{},
	}

	diff, err := state.Normalize(obs.Boundary)
	if err != nil {
		return nil, fmt.Errorf("position %d: %w: %v", obs.Envelope.Position, ErrBoundaryNormalize, err)
	}

	execActions := make([]selfdestruct.Action, 0, len(destructs))
	frameCommitted := func(id uint64) bool {
		for i := range frames {
			if frames[i].FrameID == id {
				return frames[i].EffectsCommitted
			}
		}
		return false
	}
	for _, pd := range destructs {
		pd.frameExitScope = frameExits[pd.action.FrameID]
		committed := frameCommitted(pd.action.FrameID) && !rolledBackTransitively(scopes, pd.commitScope())
		pd.action.EffectsCommitted = committed
		// The deletion survives exactly when the effects commit and the
		// fork rules made the account deletion eligible.
		pd.action.DeletionCommitted = committed && pd.action.DeletionEligibleByRules
		execActions = append(execActions, pd.action)
	}

	scopeList := make([]balance.RollbackScope, 0, len(scopes)+1)
	scopeList = append(scopeList, balance.TxBoundaryScope())
	for i := range scopes {
		scopeList = append(scopeList, scopes[i].scope)
	}

	record := &Record{
		Classification:   classification,
		Position:         obs.Envelope.Position,
		TransactionHash:  obs.Envelope.TransactionHash,
		Envelope:         obs.Envelope,
		Calls:            calls.Tree{Frames: frames},
		Scopes:           scopeList,
		Actions:          actions,
		ExecutionActions: execActions,
		Receipt:          *typed,
		StateDiff:        diff,
		SystemCallKind:   SystemCallInvalid,
		Coinbase:         obs.Coinbase,
	}
	if classification == ClassificationParliaSystem {
		record.SystemCallKind = ClassifySystemCall(obs.Envelope.Input)
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if err := record.checkSystemTransition(obs); err != nil {
		return nil, fmt.Errorf("position %d: %w", obs.Envelope.Position, err)
	}
	return record, nil
}

// commitScope returns the scope the SELFDESTRUCT effects are resolved
// against: the projection group's scope when a monetary movement exists,
// else the executing frame's exit scope.
func (p pendingDestruct) commitScope() uint64 {
	if p.hasProjection {
		return p.projectionScope
	}
	return p.frameExitScope
}

// markRolledBack marks the scope and every scope whose parent chain
// reaches it: an ancestor rollback cancels every descendant, including
// previously completed ones.
func markRolledBack(scopes []scopeState, id uint64) {
	for i := range scopes {
		cur := scopes[i].scope.ScopeID
		for cur != 0 {
			if cur == id {
				scopes[i].scope.RolledBack = true
				break
			}
			cur = parentOf(scopes, cur)
		}
	}
}

func parentOf(scopes []scopeState, id uint64) uint64 {
	for i := range scopes {
		if scopes[i].scope.ScopeID == id {
			if scopes[i].scope.ParentScopeID == nil {
				return 0
			}
			return *scopes[i].scope.ParentScopeID
		}
	}
	return 0
}

// rolledBackTransitively reports whether the scope, or any ancestor, was
// marked rolled back.
func rolledBackTransitively(scopes []scopeState, id uint64) bool {
	cur := id
	for steps := 0; steps <= len(scopes); steps++ {
		for i := range scopes {
			if scopes[i].scope.ScopeID != cur {
				continue
			}
			if scopes[i].scope.RolledBack {
				return true
			}
			if scopes[i].scope.ParentScopeID == nil {
				return false
			}
			cur = *scopes[i].scope.ParentScopeID
			break
		}
	}
	return false
}

// resolveExitScope resolves the native snapshot a frame exited into. The
// scope may already have reverted and left the active stack: the exit
// still refers to it, because that is what makes the frame's effects
// rolled back. An active scope wins; otherwise the most recently created
// scope of that native id is the one that just ended.
func resolveExitScope(scopes []scopeState, nativeOpen map[uint64]int, nativeID uint64) (uint64, error) {
	if nativeID == 0 {
		return 0, nil
	}
	if idx, ok := nativeOpen[nativeID]; ok {
		return scopes[idx].scope.ScopeID, nil
	}
	for i := len(scopes) - 1; i >= 0; i-- {
		if scopes[i].nativeID == nativeID {
			return scopes[i].scope.ScopeID, nil
		}
	}
	return 0, fmt.Errorf("%w: native snapshot %d", ErrUnknownSnapshot, nativeID)
}
