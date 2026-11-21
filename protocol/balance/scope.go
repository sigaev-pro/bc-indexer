package balance

// RollbackScope is a normalized native rollback scope of one transaction
// (CURRENT_SEMANTICS D74):
//
//	RollbackScope={scope_id, parent_scope_id?, owner_frame_id?, rolled_back}
//
// The rules encoded by the validator:
//
//   - Scope 0 is the included-transaction boundary: no parent, no owner and
//     never rolled_back for a published included transaction. The root
//     execution has its own nested child scope, so a root revert never marks
//     scope 0.
//   - Scope IDs 1..N are allocated in creation order of the observed native
//     snapshot scopes and are never reused inside the transaction. Each has
//     an existing parent_scope_id < scope_id: the nearest enclosing scope,
//     or 0. owner_frame_id is present only with a proven frame link and may
//     be 0 for the root frame; one frame may own several scopes.
//   - rolled_back is the final cancellation fact, direct or through an
//     ancestor: an ancestor rollback propagates to every descendant,
//     including previously completed ones, while a caught child revert never
//     marks the parent or independent later scopes.
//   - Fees and nonce changes that survive a root revert are never attributed
//     to a rolled-back scope: fee actions live at scope 0, nonce is part of
//     AccountChange and gets no BalanceAction.
type RollbackScope struct {
	// ScopeID is the tx-local scope identity. Scope 0 is the
	// included-transaction boundary.
	ScopeID uint64
	// ParentScopeID is the nearest enclosing scope, or 0 for direct children
	// of the transaction boundary. Nil is absent and is legal only on
	// scope 0.
	ParentScopeID *uint64
	// OwnerFrameID is the proven owning CallFrame, when the link is proven.
	OwnerFrameID *uint64
	// RolledBack is the final rollback fact for this scope, including
	// propagation from ancestors.
	RolledBack bool
}

// TxBoundaryScope returns scope 0, the included-transaction boundary scope:
// no parent, no owner, not rolled back.
func TxBoundaryScope() RollbackScope {
	return RollbackScope{ScopeID: 0}
}

func scopePtr(id uint64) *uint64 { return &id }
