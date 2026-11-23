package poly

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// TestAuthorizationClosedEmpty: the profile is closed and empty; any source
// tuple, including an invalid or duplicate one, is an explicit error.
func TestAuthorizationClosedEmpty(t *testing.T) {
	if AuthorizationProfileNotApplicable.Applicable() {
		t.Fatalf("profile must not be applicable")
	}
	if AuthorizationProfileNotApplicable.Name() != "POLY_AUTHORIZATION_EMPTY_V1" {
		t.Fatalf("profile name differs")
	}

	tx := legacyTxForCalls(t)
	tx.AuthorizationTuples = []AuthorizationTupleCapture{{
		ChainID: balance.UInt256FromUint64(15001),
		Address: fixtureSender,
		Nonce:   1,
	}}
	if _, err := NormalizeTransaction(&tx); !errors.Is(err, ErrAuthorization) {
		t.Fatalf("source tuple accepted: %v", err)
	}

	// Duplicate tuples are preserved source data and also rejected.
	tx.AuthorizationTuples = append(tx.AuthorizationTuples, tx.AuthorizationTuples[0])
	if _, err := NormalizeTransaction(&tx); !errors.Is(err, ErrAuthorization) {
		t.Fatalf("duplicate tuples accepted: %v", err)
	}

	// The StateSync transaction rejects tuples through the same profile.
	sync := crossingStateSyncTx(t)
	sync.AuthorizationTuples = tx.AuthorizationTuples
	if _, err := NormalizeTransaction(&sync); !errors.Is(err, ErrAuthorization) {
		t.Fatalf("state sync tuple accepted: %v", err)
	}
}

// TestSelfDestructActionRules: the opcode action is kept even at zero
// balance, eligibility and deletion are separate facts, and the burn
// finalization is its own operation with related-action links.
func TestSelfDestructActionRules(t *testing.T) {
	tx := legacyTxForCalls(t)
	beneficiary := mustAddress("0x6000000000000000000000000000000000000001")
	contract := mustAddress("0x6000000000000000000000000000000000000002")

	tx.SelfDestructs = []SelfDestructCapture{{
		Sequence: 6, Frame: 0, ContractAddress: contract, Beneficiary: beneficiary,
		BalanceAtEntry: balance.UInt256FromUint64(0), // zero-balance opcode stays
		CreatedSameTx:  false,                        // not eligible post-6780
		Deleted:        false, Committed: true,
	}}
	record, err := NormalizeTransaction(&tx)
	if err != nil {
		t.Fatalf("zero-balance selfdestruct: %v", err)
	}
	action := record.ExecutionActions[0]
	if action.Payload.BalanceAtEntry != (balance.UInt256{}) {
		t.Fatalf("balance at entry differs")
	}
	if action.Payload.DeletionEligibleByRules || action.Payload.DeletionCommitted {
		t.Fatalf("an ineligible destruction must not commit deletion")
	}
	if action.Payload.EffectsCommitted != true {
		t.Fatalf("committed flag differs")
	}
	// The opcode without a monetary projection is legal: no action pairs
	// with it and no invented transfer appears.
	for _, a := range record.Actions {
		if a.Cause.Kind == balance.CauseExecutionAction {
			t.Fatalf("zero-balance opcode produced a monetary projection")
		}
	}

	// The finalization burn: its own operation with related links, at its
	// own later order.
	burner := tx
	burner.SelfDestructs[0].BalanceAtEntry = balance.UInt256FromUint64(3)
	burner.SelfDestructs[0].BurnedAtFinalization = true
	burner.Operations = append(burner.Operations, OpCapture{
		Sequence: 7, Cause: OpCauseProtocol, Operation: balance.OpSelfDestructFinalizationBurn,
		From: addrPtr(contract), Amount: balance.UInt256FromUint64(3),
		Effects: []EffectCapture{{
			Address: contract, Before: balance.UInt256FromUint64(3), After: balance.UInt256FromUint64(0),
		}},
		Committed: true, Snapshot: -1, Related: []int{0},
	})
	burner.Accounts = append(burner.Accounts, AccountBoundaryCapture{
		Address: contract, ExistedBefore: true, ExistsAfter: true,
		BalanceBefore: balance.UInt256FromUint64(3), BalanceAfter: balance.UInt256FromUint64(0),
	})
	burnRecord, err := NormalizeTransaction(&burner)
	if err != nil {
		t.Fatalf("burn finalization: %v", err)
	}
	var burn *balance.BalanceAction
	for i := range burnRecord.Actions {
		if burnRecord.Actions[i].Cause.Operation == balance.OpSelfDestructFinalizationBurn {
			burn = &burnRecord.Actions[i]
		}
	}
	if burn == nil {
		t.Fatalf("burn action missing")
	}
	if burn.Kind != balance.KindBurn || burn.ExecutionOrder == burnRecord.ExecutionActions[0].ExecutionOrder {
		t.Fatalf("the burn must be its own later operation")
	}
	if len(burn.Cause.RelatedExecutionActions) != 1 || burn.Cause.RelatedExecutionActions[0] != 1 {
		t.Fatalf("the burn must link its execution action")
	}

	// A burn without related actions is rejected.
	linked := burner
	linked.Operations[len(linked.Operations)-1].Related = nil
	if _, err := NormalizeTransaction(&linked); err == nil {
		t.Fatalf("unlinked burn accepted")
	}
}

// TestStateDiffDeterministicOrder: accounts sort by raw address bytes,
// storage by raw key bytes, one row per key, and existence lives in the
// flags.
func TestStateDiffDeterministicOrder(t *testing.T) {
	tx := crossingStateSyncTx(t)
	keyOne := mustHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	keyTwo := mustHash("0x0000000000000000000000000000000000000000000000000000000000000002")
	tx.Accounts = []AccountBoundaryCapture{
		{
			Address: fixtureTarget, ExistedBefore: true, ExistsAfter: true,
			StorageReset: true,
			Storage: []StorageBoundaryCapture{
				{Key: keyTwo, Before: Hash32{}, After: keyTwo},
				{Key: keyOne, Before: Hash32{}, After: keyOne},
			},
		},
		{
			Address: fixtureReceiver, ExistedBefore: true, ExistsAfter: true,
			Storage: []StorageBoundaryCapture{{Key: Hash32{}, Before: Hash32{}, After: keyOne}},
		},
	}
	record, err := NormalizeTransaction(&tx)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	changes := record.StateChanges
	if len(changes) != 2 {
		t.Fatalf("accounts %d, want 2", len(changes))
	}
	if !lessAddress(changes[0].Address, changes[1].Address) {
		t.Fatalf("accounts are not sorted by raw address bytes")
	}
	target := changes[1]
	if !target.StorageReset {
		t.Fatalf("storage reset flag lost")
	}
	if target.StorageChanges[0].Key != keyOne || target.StorageChanges[1].Key != keyTwo {
		t.Fatalf("storage rows are not sorted by raw key bytes")
	}

	// A repeated key is rejected.
	dup := tx
	dup.Accounts = []AccountBoundaryCapture{{
		Address: fixtureTarget, ExistedBefore: true, ExistsAfter: true,
		Storage: []StorageBoundaryCapture{
			{Key: keyOne, Before: Hash32{}, After: keyOne},
			{Key: keyOne, Before: keyOne, After: keyTwo},
		},
	}}
	if _, err := NormalizeTransaction(&dup); err == nil {
		t.Fatalf("repeated storage key accepted")
	}

	// A code change without both code observations is rejected.
	code := tx
	codeHash := mustHash("0x5500000000000000000000000000000000000000000000000000000000000005")
	code.Accounts = []AccountBoundaryCapture{{
		Address: fixtureTarget, ExistedBefore: true, ExistsAfter: true,
		CodeHashBefore: repeatHash("c"), CodeHashAfter: codeHash,
		CodeAfter: []byte{0x60},
	}}
	if _, err := NormalizeTransaction(&code); err == nil {
		t.Fatalf("code change without both observations accepted")
	}

	// A new account without observed code is rejected.
	fresh := tx
	fresh.Accounts = []AccountBoundaryCapture{{
		Address:       mustAddress("0x7000000000000000000000000000000000000001"),
		ExistedBefore: false, ExistsAfter: true,
		CodeHashAfter: repeatHash("d"),
	}}
	if _, err := NormalizeTransaction(&fresh); err == nil {
		t.Fatalf("creation without code observation accepted")
	}
}

// TestBalanceActionRegistryClosed: an unknown native path is an explicit
// error, never an OTHER fallback; the BSC operation is closed to this
// family.
func TestBalanceActionRegistryClosed(t *testing.T) {
	tx := legacyTxForCalls(t)
	tx.Operations = append(tx.Operations, OpCapture{
		Sequence: 6, Cause: OpCauseProtocol, Operation: balance.OpBSCSystemExecution,
		Amount: balance.UInt256FromUint64(1), Committed: true, Snapshot: -1,
	})
	if _, err := NormalizeTransaction(&tx); err == nil {
		t.Fatalf("BSC system execution accepted in the Polygon family")
	}
}

// TestBalanceStateSyncProtocolChange: the BOR_STATE_SYNC_EXECUTION
// operation keeps the actual effects of the native system path without an
// invented transfer shape.
func TestBalanceStateSyncProtocolChange(t *testing.T) {
	tx := crossingStateSyncTx(t)
	// A receiver contract that moves native value during the state sync
	// execution: kept as NATIVE_PROTOCOL_CHANGE with the registered cause.
	tx.Operations = []OpCapture{{
		Sequence: 6, Cause: OpCauseProtocol, Operation: balance.OpBorStateSyncExecution,
		From: addrPtr(fixtureReceiver), To: addrPtr(fixtureTarget),
		Amount: balance.UInt256FromUint64(4),
		Effects: []EffectCapture{
			{Address: fixtureReceiver, Before: balance.UInt256FromUint64(4), After: balance.UInt256FromUint64(0)},
			{Address: fixtureTarget, Before: balance.UInt256FromUint64(0), After: balance.UInt256FromUint64(4)},
		},
		Committed: true, Snapshot: -1,
	}}
	tx.Accounts[0].BalanceBefore = balance.UInt256FromUint64(4)
	tx.Accounts[0].BalanceAfter = balance.UInt256FromUint64(0)
	tx.Accounts[1].BalanceBefore = balance.UInt256FromUint64(0)
	tx.Accounts[1].BalanceAfter = balance.UInt256FromUint64(4)
	record, err := NormalizeTransaction(&tx)
	if err != nil {
		t.Fatalf("state sync protocol change: %v", err)
	}
	action := record.Actions[0]
	if action.Cause.Operation != balance.OpBorStateSyncExecution || action.Kind != balance.KindNativeProtocolChange ||
		action.Engine != balance.EngineNativeProtocol {
		t.Fatalf("state sync protocol action shape differs")
	}
	if action.Cause.OperationIndex != 0 {
		t.Fatalf("operation index %d, want the first observed invocation", action.Cause.OperationIndex)
	}
	if err := record.ReconcileBoundary(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}
