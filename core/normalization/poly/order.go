package poly

import (
	"fmt"
	"sort"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// The orderer implements the shared D74 execution_order counter of one
// transaction: CallFrame entries, execution actions and balance actions are
// numbered by their observed native synchronous boundary, starting at 1,
// with no gaps, and an equal number is legal only for an execution action
// and the single monetary projection caused by it.

// txOrders is the allocated order of every captured object of one
// transaction, by capture position.
type txOrders struct {
	frames        []uint64
	selfDestructs []uint64
	ops           []uint64
	// protocolIndex is the tx-local TransactionProtocol operation index of
	// each op with a protocol cause, assigned by observed order.
	protocolIndex []uint64
	// opSequence is the observed sequence of each op (the projection pairs
	// share the opcode sequence).
	opSequence []uint64
	// maxOrder is the highest allocated number; all numbers 1..maxOrder are
	// in use.
	maxOrder uint64
}

// allocateOrders merges the observed sequences of all captured objects into
// the shared counter and returns the allocation.
func allocateOrders(tx *TxCapture) (*txOrders, error) {
	o := &txOrders{
		frames:        make([]uint64, len(tx.Frames)),
		selfDestructs: make([]uint64, len(tx.SelfDestructs)),
		ops:           make([]uint64, len(tx.Operations)),
		protocolIndex: make([]uint64, len(tx.Operations)),
		opSequence:    make([]uint64, len(tx.Operations)),
	}

	// Frame entries are strictly ordered native boundaries: each frame entry
	// is its own operation, so two frames never share a sequence and a
	// balance operation never shares a frame's sequence (the value transfer
	// at call entry is a separate later operation).
	for i := 1; i < len(tx.Frames); i++ {
		if tx.Frames[i].Entry <= tx.Frames[i-1].Entry {
			return nil, fmt.Errorf("frame %d entry %d after entry %d: %w",
				i, tx.Frames[i].Entry, tx.Frames[i-1].Entry, ErrOrder)
		}
	}

	// Every distinct observed sequence becomes exactly one order number. A
	// SELFDESTRUCT and its monetary projection share one sequence, so their
	// group shares one number; any other sharing is illegal.
	sdSeq := make(map[uint64]int, len(tx.SelfDestructs)) // sequence -> selfdestruct index
	for i, sd := range tx.SelfDestructs {
		if other, seen := sdSeq[sd.Sequence]; seen {
			return nil, fmt.Errorf("selfdestructs %d and %d share sequence %d: %w", other, i, sd.Sequence, ErrOrder)
		}
		sdSeq[sd.Sequence] = i
	}
	projectionOf := make(map[int]int, len(tx.Operations)) // selfdestruct index -> op index
	for i, op := range tx.Operations {
		switch op.Cause {
		case OpCauseSelfDestruct:
			if op.SelfDestruct < 0 || op.SelfDestruct >= len(tx.SelfDestructs) {
				return nil, fmt.Errorf("op %d references unknown selfdestruct %d: %w", i, op.SelfDestruct, ErrCapture)
			}
			if tx.Operations[i].Sequence != tx.SelfDestructs[op.SelfDestruct].Sequence {
				return nil, fmt.Errorf("op %d sequence %d differs from its selfdestruct sequence %d: %w",
					i, op.Sequence, tx.SelfDestructs[op.SelfDestruct].Sequence, ErrOrder)
			}
			if first, seen := projectionOf[op.SelfDestruct]; seen {
				return nil, fmt.Errorf("selfdestruct %d projected by ops %d and %d: %w",
					op.SelfDestruct, first, i, ErrOrder)
			}
			projectionOf[op.SelfDestruct] = i
		}
	}

	sequences := make([]uint64, 0, len(tx.Frames)+len(tx.SelfDestructs)+len(tx.Operations))
	for i := range tx.Frames {
		if _, clash := sdSeq[tx.Frames[i].Entry]; clash {
			return nil, fmt.Errorf("frame %d entry %d equals a selfdestruct sequence: %w", i, tx.Frames[i].Entry, ErrOrder)
		}
		sequences = append(sequences, tx.Frames[i].Entry)
	}
	for i := range tx.SelfDestructs {
		sequences = append(sequences, tx.SelfDestructs[i].Sequence)
	}
	for i := range tx.Operations {
		if tx.Operations[i].Cause != OpCauseSelfDestruct {
			sequences = append(sequences, tx.Operations[i].Sequence)
		}
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	for i := 1; i < len(sequences); i++ {
		if sequences[i] == sequences[i-1] {
			return nil, fmt.Errorf("sequence %d used by unrelated operations: %w", sequences[i], ErrOrder)
		}
	}
	rank := make(map[uint64]uint64, len(sequences))
	for i, seq := range sequences {
		rank[seq] = uint64(i) + 1
	}
	o.maxOrder = uint64(len(sequences))

	for i := range tx.Frames {
		o.frames[i] = rank[tx.Frames[i].Entry]
	}
	for i := range tx.SelfDestructs {
		o.selfDestructs[i] = rank[tx.SelfDestructs[i].Sequence]
	}
	for i := range tx.Operations {
		o.opSequence[i] = tx.Operations[i].Sequence
		o.ops[i] = rank[tx.Operations[i].Sequence]
	}

	// Protocol operation indexes: one tx-local counter over every observed
	// protocol invocation, in observed order; several fee credits are
	// separate invocations with separate indexes.
	type indexed struct{ op, seq uint64 }
	var protos []indexed
	for i := range tx.Operations {
		if tx.Operations[i].Cause == OpCauseProtocol {
			protos = append(protos, indexed{uint64(i), tx.Operations[i].Sequence})
		}
	}
	sort.Slice(protos, func(a, b int) bool { return protos[a].seq < protos[b].seq })
	for idx, p := range protos {
		o.protocolIndex[p.op] = uint64(idx)
	}
	return o, nil
}

// buildScopes normalizes the observed snapshot rows into the rollback scope
// list of D74: scope 0 is the included-transaction boundary; rows become
// scope ids 1..N in creation order, with parents mapped onto the normalized
// tree. The native snapshot id is provenance, never the published scope id.
func buildScopes(tx *TxCapture) ([]balance.RollbackScope, []uint64, error) {
	scopes := make([]balance.RollbackScope, 0, len(tx.Snapshots)+1)
	scopes = append(scopes, balance.TxBoundaryScope())
	scopeOfRow := make([]uint64, len(tx.Snapshots))
	for i, row := range tx.Snapshots {
		id := uint64(i) + 1
		scopeOfRow[i] = id
		scope := balance.RollbackScope{ScopeID: id, RolledBack: row.RolledBack}
		// Every non-boundary scope carries an explicit parent: the nearest
		// enclosing scope, or 0 for direct children of the transaction
		// boundary (D74 rule 2; absence is only legal on scope 0).
		parent := uint64(0)
		if row.Parent >= 0 {
			if row.Parent >= i {
				return nil, nil, fmt.Errorf("scope row %d parent %d is not an earlier row: %w", i, row.Parent, ErrScope)
			}
			parent = scopeOfRow[row.Parent]
		}
		scope.ParentScopeID = &parent
		if row.OwnerFrame >= 0 {
			if row.OwnerFrame >= len(tx.Frames) {
				return nil, nil, fmt.Errorf("scope row %d owner frame %d does not exist: %w", i, row.OwnerFrame, ErrScope)
			}
			owner := uint64(row.OwnerFrame)
			scope.OwnerFrameID = &owner
		}
		scopes = append(scopes, scope)
	}
	return scopes, scopeOfRow, nil
}

// scopeID maps an operation's snapshot row reference onto the published
// scope id: -1 is the boundary scope 0.
func scopeID(row int, scopeOfRow []uint64) (uint64, error) {
	if row < 0 {
		return 0, nil
	}
	if row >= len(scopeOfRow) {
		return 0, fmt.Errorf("unknown snapshot row %d: %w", row, ErrScope)
	}
	return scopeOfRow[row], nil
}
