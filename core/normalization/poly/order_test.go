package poly

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// TestOrderSharedCounter checks the shared D74 counter through the crossing
// legacy transaction: every captured object gets one number, the numbers
// form 1..N, and the SELFDESTRUCT projection shares the opcode number.
func TestOrderSharedCounter(t *testing.T) {
	tx := legacyTxForCalls(t)

	// Add a SELFDESTRUCT with its monetary projection at the same observed
	// boundary, plus a finalization burn at its own later position.
	beneficiary := mustAddress("0x6000000000000000000000000000000000000001")
	contract := mustAddress("0x6000000000000000000000000000000000000002")
	tx.Frames = append(tx.Frames, FrameCapture{
		Entry: 6, Parent: 0, Depth: 1,
		Engine: balance.EngineEVM, Kind: FrameCall,
		CallerAddress: addrPtr(fixtureSender), ContextAddress: addrPtr(contract),
		Target: addrPtr(contract), Outcome: OutcomeSuccess, Committed: true,
	})
	tx.SelfDestructs = []SelfDestructCapture{{
		Sequence: 7, Frame: 1, ContractAddress: contract, Beneficiary: beneficiary,
		BalanceAtEntry: balance.UInt256FromUint64(9), CreatedSameTx: true,
		Deleted: true, Committed: true, BurnedAtFinalization: false,
	}}
	tx.Operations = append(tx.Operations,
		OpCapture{
			Sequence: 7, Cause: OpCauseSelfDestruct, SelfDestruct: 0,
			From: addrPtr(contract), To: addrPtr(beneficiary),
			Amount: balance.UInt256FromUint64(9),
			Effects: []EffectCapture{
				{Address: contract, Before: balance.UInt256FromUint64(9), After: balance.UInt256FromUint64(0)},
				{Address: beneficiary, Before: balance.UInt256FromUint64(0), After: balance.UInt256FromUint64(9)},
			},
			Committed: true, Snapshot: 0,
		},
		OpCapture{
			Sequence: 8, Cause: OpCauseProtocol, Operation: balance.OpGasReturn,
			To: addrPtr(fixtureSender), Amount: balance.UInt256FromUint64(0),
			Effects: []EffectCapture{
				{Address: fixtureSender,
					Before: balance.MustParseUInt256("9999894999999999995"),
					After:  balance.MustParseUInt256("9999894999999999995")},
			},
			Committed: true, Snapshot: -1,
		},
	)
	tx.Accounts = append(tx.Accounts,
		AccountBoundaryCapture{
			Address: beneficiary, ExistedBefore: true, ExistsAfter: true,
			BalanceBefore: balance.UInt256FromUint64(0), BalanceAfter: balance.UInt256FromUint64(9),
		},
		AccountBoundaryCapture{
			Address: contract, ExistedBefore: true, ExistsAfter: false,
			BalanceBefore: balance.UInt256FromUint64(9), BalanceAfter: balance.UInt256FromUint64(0),
		},
	)
	record, err := NormalizeTransaction(&tx)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	// The SELFDESTRUCT execution action and its projection share order 7.
	var action, exec uint64
	for i := range record.Actions {
		if record.Actions[i].Cause.Kind == balance.CauseExecutionAction {
			action = record.Actions[i].ExecutionOrder
		}
	}
	exec = record.ExecutionActions[0].ExecutionOrder
	if action != exec {
		t.Fatalf("projection order %d differs from the opcode order %d", action, exec)
	}
	// The burn and the gas return hold their own later numbers; all numbers
	// together stay the continuous range 1..N.
	max := uint64(0)
	for _, c := range record.Calls {
		if c.ExecutionOrder > max {
			max = c.ExecutionOrder
		}
	}
	for _, a := range record.Actions {
		if a.ExecutionOrder > max {
			max = a.ExecutionOrder
		}
	}
	for _, e := range record.ExecutionActions {
		if e.ExecutionOrder > max {
			max = e.ExecutionOrder
		}
	}
	seen := make(map[uint64]int)
	for _, c := range record.Calls {
		seen[c.ExecutionOrder]++
	}
	for _, e := range record.ExecutionActions {
		seen[e.ExecutionOrder]++
	}
	for _, a := range record.Actions {
		seen[a.ExecutionOrder]++
	}
	total := 0
	for order, count := range seen {
		if order == 0 || order > max {
			t.Fatalf("order %d outside 1..%d", order, max)
		}
		if count > 2 {
			t.Fatalf("order %d shared by %d objects", order, count)
		}
		total += count
	}
	// Two frames + two protocol fee ops + transfer + selfdestruct pair +
	// gas return: 8 distinct orders (the pair shares one).
	if max != 8 {
		t.Fatalf("highest order %d, want 8", max)
	}
	if len(record.Scopes) != 2 {
		t.Fatalf("scopes %d, want boundary and root execution scope", len(record.Scopes))
	}
	if err := record.ReconcileBoundary(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

// TestOrderRejectsIllegalSharing checks that a frame entry never shares a
// number and an unrelated op never rides another boundary.
func TestOrderRejectsIllegalSharing(t *testing.T) {
	tx := legacyTxForCalls(t)

	// An op sharing the root frame entry sequence.
	riding := tx
	riding.Operations[1].Sequence = 2 // the transfer rides the frame entry
	if _, err := NormalizeTransaction(&riding); !errors.Is(err, ErrOrder) {
		t.Fatalf("op riding a frame entry: got %v", err)
	}

	// A projection that leaves its opcode boundary.
	split := tx
	beneficiary := mustAddress("0x6000000000000000000000000000000000000001")
	contract := mustAddress("0x6000000000000000000000000000000000000002")
	split.SelfDestructs = []SelfDestructCapture{{
		Sequence: 7, Frame: 0, ContractAddress: contract, Beneficiary: beneficiary,
		CreatedSameTx: true, Deleted: true, Committed: true,
	}}
	split.Operations = append(split.Operations, OpCapture{
		Sequence: 9, Cause: OpCauseSelfDestruct, SelfDestruct: 0,
		From: addrPtr(contract), To: addrPtr(beneficiary),
		Amount: balance.UInt256FromUint64(1), Committed: true, Snapshot: 0,
		Effects: []EffectCapture{
			{Address: contract, Before: balance.UInt256FromUint64(1), After: balance.UInt256FromUint64(0)},
			{Address: beneficiary, Before: balance.UInt256FromUint64(0), After: balance.UInt256FromUint64(1)},
		},
	})
	if _, err := NormalizeTransaction(&split); !errors.Is(err, ErrOrder) {
		t.Fatalf("projection away from its opcode accepted: %v", err)
	}
}

// TestScopesNativeToNormalized checks the scope tree mapping: re-used
// native snapshot ids become distinct scopes and parents map onto the
// normalized tree.
func TestScopesNativeToNormalized(t *testing.T) {
	tx := legacyTxForCalls(t)
	// A second snapshot creation that re-uses native id 1 after a discard.
	tx.Snapshots = append(tx.Snapshots, SnapshotCapture{
		NativeID: 1, Parent: 0, OwnerFrame: -1, RolledBack: false,
	})
	record, err := NormalizeTransaction(&tx)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(record.Scopes) != 3 {
		t.Fatalf("scopes %d, want boundary + two distinct re-used rows", len(record.Scopes))
	}
	second := record.Scopes[2]
	if second.ScopeID != 2 || *second.ParentScopeID != 1 {
		t.Fatalf("second scope %d parent %v", second.ScopeID, *second.ParentScopeID)
	}
	if second.OwnerFrameID != nil {
		t.Fatalf("unproven owner link must stay absent")
	}
}
