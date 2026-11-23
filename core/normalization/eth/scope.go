package eth

import (
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// RollbackScope is the normalized rollback scope of the shared balance
// model; the rules below are the D74 contract this builder enforces.
type RollbackScope = balance.RollbackScope

// Scope errors.
var (
	// ErrScopeUnknown reports a reference to a scope that was never created.
	ErrScopeUnknown = errors.New("eth: unknown rollback scope")
	// ErrScopeParentOrder reports a parent scope that is not smaller than
	// the new scope. The parent link is the nearest enclosing scope.
	ErrScopeParentOrder = errors.New("eth: parent scope must be smaller than the scope")
	// ErrScopeParentAbsent reports a non-boundary scope without a parent.
	ErrScopeParentAbsent = errors.New("eth: non-boundary scope without a parent")
	// ErrScopeBoundary reports an attempt to give scope 0 a parent, an
	// owner or a rollback mark. Scope 0 is the included-transaction
	// boundary and is never rolled back for a published included tx.
	ErrScopeBoundary = errors.New("eth: scope 0 is the transaction boundary")
)

// ScopeBuilder allocates the rollback scopes of one transaction in the
// creation order of the observed native snapshot scopes (D74):
//
//   - Scope 0 exists from the start: the included-transaction boundary, no
//     parent, no owner, never rolled back. The root EVM execution gets its
//     own nested child scope; a root revert marks that child, never scope 0.
//   - Scope IDs 1..N are allocated in creation order and never reused inside
//     the transaction. A repeated native snapshot ID after a rollback or
//     discard is a new creation and gets a new scope_id.
//   - Each non-boundary scope names its nearest enclosing scope (or 0) and,
//     only when the link is proven, the owning frame. One frame may own
//     several scopes; the owner may be 0 for the root frame.
//   - rolled_back is the final cancellation fact, direct or through an
//     ancestor. Finalize propagates an ancestor rollback to every
//     descendant, including previously completed scopes.
//
// The builder never invents a snapshot for a failed frame without effects
// and never drops an observed scope: the caller records what native showed.
type ScopeBuilder struct {
	scopes []RollbackScope
	marked []bool
}

// NewScopeBuilder starts the scope list with the transaction boundary
// scope 0.
func NewScopeBuilder() *ScopeBuilder {
	return &ScopeBuilder{
		scopes: []RollbackScope{balance.TxBoundaryScope()},
		marked: make([]bool, 1),
	}
}

// Enter allocates the next scope. parent is the nearest enclosing scope
// (0 for a direct child of the transaction boundary). ownerFrame is the
// proven owning frame; nil keeps the link absent. The pointer value 0 is a
// valid owner (the root frame).
func (b *ScopeBuilder) Enter(parent uint64, ownerFrame *uint64) (uint64, error) {
	if parent >= uint64(len(b.scopes)) {
		return 0, fmt.Errorf("%w: parent %d of new scope %d", ErrScopeUnknown, parent, uint64(len(b.scopes)))
	}
	if parent != 0 && b.scopes[parent].ParentScopeID == nil {
		return 0, fmt.Errorf("%w: scope %d has no parent itself", ErrScopeParentAbsent, parent)
	}
	id := uint64(len(b.scopes))
	s := RollbackScope{ScopeID: id, ParentScopeID: &parent}
	if ownerFrame != nil {
		owner := *ownerFrame
		s.OwnerFrameID = &owner
	}
	b.scopes = append(b.scopes, s)
	b.marked = append(b.marked, false)
	return id, nil
}

// MarkRolledBack records the final direct rollback of one scope. Ancestor
// propagation happens in Finalize.
func (b *ScopeBuilder) MarkRolledBack(id uint64) error {
	if id >= uint64(len(b.scopes)) {
		return fmt.Errorf("%w: %d", ErrScopeUnknown, id)
	}
	if id == 0 {
		return fmt.Errorf("%w: cannot mark the boundary scope rolled back", ErrScopeBoundary)
	}
	b.marked[id] = true
	return nil
}

// Finalize returns the scope list ordered by scope_id with the transitive
// rolled_back fact computed: a rolled-back ancestor marks every descendant,
// including previously successfully completed scopes, while a caught child
// revert never marks the parent or independent later scopes.
func (b *ScopeBuilder) Finalize() ([]RollbackScope, error) {
	out := make([]RollbackScope, len(b.scopes))
	copy(out, b.scopes)
	for i := range out {
		if b.marked[out[i].ScopeID] {
			out[i].RolledBack = true
		}
	}
	// Propagate down: a scope is rolled back when any strict ancestor is.
	// Scopes are ordered by id and every parent is smaller, so one pass in
	// increasing id order has already visited every ancestor.
	for i := 1; i < len(out); i++ {
		if out[i].RolledBack {
			continue
		}
		if parent := *out[i].ParentScopeID; out[parent].RolledBack {
			out[i].RolledBack = true
		}
	}
	if err := balance.Validate(balance.TxBalances{Scopes: out}); err != nil {
		// ErrNoFrames is expected here: the frame list is assembled
		// elsewhere. Everything else is a real scope violation.
		if !errors.Is(err, balance.ErrNoFrames) {
			return nil, err
		}
	}
	return out, nil
}

// RolledBack reports the final rolled_back fact of one scope of a finished
// list.
func RolledBack(scopes []RollbackScope, id uint64) bool {
	for i := range scopes {
		if scopes[i].ScopeID == id {
			return scopes[i].RolledBack
		}
	}
	return false
}
