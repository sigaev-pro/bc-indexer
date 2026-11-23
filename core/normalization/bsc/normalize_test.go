package bsc

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/authorization"
	bscbalance "github.com/sigaev-pro/bc-indexer/core/normalization/bsc/balance"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/calls"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/receipt"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/state"
	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

var (
	fixturePayer    = balance.Address20{0x7e}
	fixtureTo       = balance.Address20{0x40}
	fixtureChild    = balance.Address20{0x30, 0x02}
	fixtureCoinbase = balance.Address20{0x68}
	fixtureReward   = balance.MustParseAddress("0x0000000000000000000000000000000000001002")
	fixtureBurn     = balance.MustParseAddress("000000000000000000000000000000000000dead")
	fixtureValSet   = balance.MustParseAddress("0x0000000000000000000000000000000000001000")
)

func u(v uint64) balance.UInt256 { return balance.UInt256FromUint64(v) }

func mut(addr balance.Address20, before, after uint64) bscbalance.Mutation {
	return bscbalance.Mutation{Address: addr, Before: u(before), After: u(after)}
}

func transferEvent(opID, frame uint64, snapshot uint64, sides ...bscbalance.Mutation) Event {
	return Event{Group: &bscbalance.Group{
		OpID: opID, Role: bscbalance.RoleTransfer, FramePresent: true, FrameID: frame,
		NativeSnapshot: snapshot, Mutations: sides,
	}}
}

func groupEvent(opID uint64, role bscbalance.Role, snapshot uint64, sides ...bscbalance.Mutation) Event {
	return Event{Group: &bscbalance.Group{
		OpID: opID, Role: role, NativeSnapshot: snapshot, Mutations: sides,
	}}
}

func snapshotCreate(nativeID uint64, owner *uint64) Event {
	return Event{Snapshot: &SnapshotEvent{NativeID: nativeID, Kind: SnapshotCreate, OwnerFrameIndex: owner}}
}

func evmRootFrame(caller, target balance.Address20, outcome calls.Outcome) Event {
	targetCopy := target
	return Event{FrameEntry: &calls.Frame{
		Depth: 0, Engine: balance.EngineEVM, Kind: calls.KindCall,
		CallerAddress: &caller, Target: &targetCopy, Outcome: outcome,
	}}
}

func accountRow(addr balance.Address20, before, after uint64, nonceBefore, nonceAfter uint64) state.AccountChange {
	return state.AccountChange{Address: addr, ExistedBefore: before > 0 || nonceBefore > 0,
		ExistsAfter: after > 0 || nonceAfter > 0, BalanceBefore: u(before), BalanceAfter: u(after),
		NonceBefore: nonceBefore, NonceAfter: nonceAfter}
}

func ordinaryEnvelope(position uint8) EnvelopeFacts {
	target := fixtureTo
	return EnvelopeFacts{
		Position: uint64(position), NativeType: 0, Wire: []byte{0xd0, 0x01},
		Sender: fixturePayer, SenderProvenance: ProvenanceNativeDerived,
		Target: &target, TargetProvenance: ProvenanceNativeDerived,
		Value: u(5), Input: []byte{0x00}, Nonce: uint64(position), GasLimit: 21000,
		EffectiveGasPrice: u(2),
	}
}

func successReceipt() ReceiptFacts {
	status := true
	gasUsed := uint64(20000)
	return ReceiptFacts{
		Status: &status, Result: receipt.ResultSuccess,
		CumulativeGasUsed: 20000, GasUsed: &gasUsed,
		EffectiveGasPrice: u(2), NativeReceiptType: 0, ConsensusBytes: []byte{0xf9, 0x02},
	}
}

func revertReceiptOf(t *testing.T) ReceiptFacts {
	t.Helper()
	r := successReceipt()
	status := false
	r.Status = &status
	r.Result = receipt.ResultRevert
	return r
}

// The retained corpus fee flow of an ordinary BSC transfer, with the D74
// example order: purchase, frame entry, value transfer, gas return, fee
// credit; the whole order range is continuous 1..5.
func TestNormalizeOrdinaryTransfer(t *testing.T) {
	owner := uint64(0)
	obs := &Observation{
		Envelope: ordinaryEnvelope(0), Coinbase: fixtureCoinbase,
		Events: []Event{
			groupEvent(1, bscbalance.RoleGasPurchase, 0, mut(fixturePayer, 50000, 8000)),
			evmRootFrame(fixturePayer, fixtureTo, calls.OutcomeSuccess),
			snapshotCreate(1, &owner),
			transferEvent(2, 0, 1, mut(fixturePayer, 8000, 7995), mut(fixtureTo, 0, 5)),
			{Snapshot: &SnapshotEvent{NativeID: 1, Kind: SnapshotEnd}},
			groupEvent(3, bscbalance.RoleGasReturn, 0, mut(fixturePayer, 7995, 9995)),
			groupEvent(4, bscbalance.RoleFeeRecipientCredit, 0, mut(bscbalance.FeeRecipient, 0, 40000)),
		},
		Receipt: successReceipt(),
		Boundary: []state.AccountChange{
			accountRow(fixturePayer, 50000, 9995, 0, 1),
			accountRow(fixtureTo, 0, 5, 0, 0),
			accountRow(bscbalance.FeeRecipient, 0, 40000, 0, 0),
		},
	}
	record, err := New().Normalize(obs)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if record.Classification != ClassificationOrdinary {
		t.Fatalf("classification = %s", record.Classification)
	}
	if len(record.Calls.Frames) != 1 || !record.Calls.Frames[0].EffectsCommitted {
		t.Fatal("the root frame must be present and committed")
	}
	if len(record.Scopes) != 2 || record.Scopes[1].ScopeID != 1 || record.Scopes[1].RolledBack {
		t.Fatalf("scopes drifted: %+v", record.Scopes)
	}
	if len(record.Actions) != 4 {
		t.Fatalf("want 4 actions, got %d", len(record.Actions))
	}
	orders := map[uint64]bool{}
	for _, a := range record.Actions {
		orders[a.ExecutionOrder] = true
	}
	orders[record.Calls.Frames[0].ExecutionOrder] = true
	for want := uint64(1); want <= 5; want++ {
		if !orders[want] {
			t.Fatalf("execution order %d missing from the continuous range", want)
		}
	}
	summary := record.Receipt.FeeSummary
	if summary.GasPurchaseDebit != u(42000) || summary.GasReturnCredit != u(2000) || summary.NetCharge != u(40000) {
		t.Fatalf("fee summary drifted: %+v", summary)
	}
	if len(summary.RecipientCredits) != 1 || summary.RecipientCredits[0].Address != bscbalance.FeeRecipient {
		t.Fatalf("the BSC fee credit must land on the accumulator: %+v", summary.RecipientCredits)
	}
	if record.Receipt.Details == nil {
		t.Fatal("BscReceiptDetails must be present")
	}
	if err := record.ReconcileBoundary(); err != nil {
		t.Fatalf("boundary reconciliation: %v", err)
	}
}

// The corpus caught-revert shape: a successful root call whose child call
// reverted and was caught. The child's scope and transfer roll back; the
// root transfer, the fees and the root frame stay committed.
func TestNormalizeCaughtChildRevert(t *testing.T) {
	owner := uint64(0)
	rootExit := Event{FrameExit: &FrameExitFacts{FrameIndex: 0, ExitNativeSnapshot: 0}}
	childExit := Event{FrameExit: &FrameExitFacts{FrameIndex: 1, ExitNativeSnapshot: 2}}
	obs := &Observation{
		Envelope: ordinaryEnvelope(1), Coinbase: fixtureCoinbase,
		Events: []Event{
			groupEvent(1, bscbalance.RoleGasPurchase, 0, mut(fixturePayer, 50000, 8000)),
			evmRootFrame(fixturePayer, fixtureTo, calls.OutcomeSuccess),
			snapshotCreate(1, &owner),
			transferEvent(2, 0, 1, mut(fixturePayer, 8000, 7995), mut(fixtureTo, 0, 5)),
			{FrameEntry: &calls.Frame{Depth: 1, ParentID: &owner, Engine: balance.EngineEVM, Kind: calls.KindCall, CallerAddress: &fixtureTo, Target: &fixtureChild, Outcome: calls.OutcomeRevert}},
			snapshotCreate(2, &owner),
			transferEvent(3, 1, 2, mut(fixtureTo, 5, 0), mut(fixtureChild, 0, 5)),
			{Snapshot: &SnapshotEvent{NativeID: 2, Kind: SnapshotRevert}},
			childExit,
			{Snapshot: &SnapshotEvent{NativeID: 1, Kind: SnapshotEnd}},
			rootExit,
			groupEvent(4, bscbalance.RoleGasReturn, 0, mut(fixturePayer, 7995, 9995)),
			groupEvent(5, bscbalance.RoleFeeRecipientCredit, 0, mut(bscbalance.FeeRecipient, 0, 40000)),
		},
		Receipt: successReceipt(),
		Boundary: []state.AccountChange{
			accountRow(fixturePayer, 50000, 9995, 0, 1),
			accountRow(fixtureTo, 0, 5, 0, 0),
			accountRow(bscbalance.FeeRecipient, 0, 40000, 0, 0),
		},
	}
	record, err := New().Normalize(obs)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(record.Scopes) != 3 {
		t.Fatalf("want scopes 0,1,2, got %+v", record.Scopes)
	}
	if !record.Scopes[2].RolledBack || record.Scopes[1].RolledBack {
		t.Fatalf("only the child scope rolls back: %+v", record.Scopes)
	}
	var childTransfer *balance.BalanceAction
	for i := range record.Actions {
		a := &record.Actions[i]
		if a.Cause.Kind == balance.CauseFrame && a.Cause.FrameID == 1 {
			childTransfer = a
		}
	}
	if childTransfer == nil {
		t.Fatal("the child transfer action is missing: rolled-back attempts stay present")
	}
	if childTransfer.EffectsCommitted {
		t.Fatal("the child transfer must not be committed after the child revert")
	}
	if !record.Calls.Frames[0].EffectsCommitted || record.Calls.Frames[1].EffectsCommitted {
		t.Fatal("root stays committed, the reverted child does not")
	}
	if err := record.ReconcileBoundary(); err != nil {
		t.Fatalf("boundary reconciliation: %v", err)
	}
}

// The corpus root-revert shape: the root scope rolls back, the value
// transfer and the root frame effects with it, while the fee actions live
// at the never-rolled-back boundary scope 0.
func TestNormalizeRootRevert(t *testing.T) {
	owner := uint64(0)
	revertReceipt := successReceipt()
	status := false
	revertReceipt.Status = &status
	revertReceipt.Result = receipt.ResultRevert
	obs := &Observation{
		Envelope: ordinaryEnvelope(2), Coinbase: fixtureCoinbase,
		Events: []Event{
			groupEvent(1, bscbalance.RoleGasPurchase, 0, mut(fixturePayer, 50000, 8000)),
			evmRootFrame(fixturePayer, fixtureTo, calls.OutcomeRevert),
			snapshotCreate(1, &owner),
			transferEvent(2, 0, 1, mut(fixturePayer, 8000, 7995), mut(fixtureTo, 0, 5)),
			{Snapshot: &SnapshotEvent{NativeID: 1, Kind: SnapshotRevert}},
			{FrameExit: &FrameExitFacts{FrameIndex: 0, ExitNativeSnapshot: 1}},
			groupEvent(3, bscbalance.RoleGasReturn, 0, mut(fixturePayer, 7995, 9995)),
			groupEvent(4, bscbalance.RoleFeeRecipientCredit, 0, mut(bscbalance.FeeRecipient, 0, 40000)),
		},
		Receipt: revertReceipt,
		Boundary: []state.AccountChange{
			accountRow(fixturePayer, 50000, 10000, 0, 1),
			accountRow(bscbalance.FeeRecipient, 0, 40000, 0, 0),
		},
	}
	record, err := New().Normalize(obs)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !record.Scopes[1].RolledBack {
		t.Fatal("the root scope must be rolled back")
	}
	if record.Calls.Frames[0].EffectsCommitted {
		t.Fatal("a reverted root frame never keeps committed effects")
	}
	for _, a := range record.Actions {
		if a.Cause.Kind == balance.CauseFrame && a.EffectsCommitted {
			t.Fatal("the rolled-back value transfer must not be committed")
		}
		if a.Cause.Kind == balance.CauseTransactionProtocol {
			if !a.EffectsCommitted || a.RollbackScope == nil || *a.RollbackScope != 0 {
				t.Fatal("fee actions live committed at scope 0")
			}
		}
	}
	if err := record.ReconcileBoundary(); err != nil {
		t.Fatalf("boundary reconciliation: %v", err)
	}
}

// The D74 SELFDESTRUCT shape: the opcode at its own order, its monetary
// projection sharing that exact order through cause=ExecutionAction, and
// an ancestor rollback cancelling both while the record keeps them.
func TestNormalizeSelfDestructWithRollback(t *testing.T) {
	owner := uint64(0)
	beneficiary := fixtureBurn
	obs := &Observation{
		Envelope: ordinaryEnvelope(3), Coinbase: fixtureCoinbase,
		Events: []Event{
			groupEvent(1, bscbalance.RoleGasPurchase, 0, mut(fixturePayer, 50000, 8000)),
			evmRootFrame(fixturePayer, fixtureTo, calls.OutcomeRevert),
			snapshotCreate(1, &owner),
			transferEvent(2, 0, 1, mut(fixturePayer, 8000, 7995), mut(fixtureTo, 0, 5)),
			snapshotCreate(2, &owner),
			{SelfDestruct: &SelfDestructFacts{
				FrameIndex: 0, Contract: fixtureTo, Beneficiary: beneficiary,
				BalanceAtEntry: u(5), DeletionEligible: true,
				Projection: &bscbalance.Group{
					OpID: 10, Role: bscbalance.RoleSelfDestructProjection, NativeSnapshot: 2,
					Mutations: []bscbalance.Mutation{mut(fixtureTo, 5, 0), mut(beneficiary, 0, 5)},
				},
			}},
			{Snapshot: &SnapshotEvent{NativeID: 2, Kind: SnapshotRevert}},
			{Snapshot: &SnapshotEvent{NativeID: 1, Kind: SnapshotRevert}},
			{FrameExit: &FrameExitFacts{FrameIndex: 0, ExitNativeSnapshot: 1}},
			groupEvent(3, bscbalance.RoleGasReturn, 0, mut(fixturePayer, 7995, 9995)),
			groupEvent(4, bscbalance.RoleFeeRecipientCredit, 0, mut(bscbalance.FeeRecipient, 0, 40000)),
		},
		Receipt: revertReceiptOf(t),
		Boundary: []state.AccountChange{
			accountRow(fixturePayer, 50000, 10000, 0, 1),
			accountRow(bscbalance.FeeRecipient, 0, 40000, 0, 0),
		},
	}
	record, err := New().Normalize(obs)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(record.ExecutionActions) != 1 {
		t.Fatalf("want one execution action, got %d", len(record.ExecutionActions))
	}
	destruct := record.ExecutionActions[0]
	if destruct.EffectsCommitted || destruct.DeletionCommitted {
		t.Fatal("a rolled-back SELFDESTRUCT keeps neither committed effects nor a committed deletion")
	}
	if !destruct.DeletionEligibleByRules || destruct.BalanceAtEntry != u(5) {
		t.Fatalf("the opcode facts drifted: %+v", destruct)
	}
	var projection *balance.BalanceAction
	for i := range record.Actions {
		if record.Actions[i].Cause.Kind == balance.CauseExecutionAction {
			projection = &record.Actions[i]
		}
	}
	if projection == nil {
		t.Fatal("the SELFDESTRUCT monetary projection is missing")
	}
	if projection.ExecutionOrder != destruct.ExecutionOrder {
		t.Fatalf("projection order %d must share the execution action order %d",
			projection.ExecutionOrder, destruct.ExecutionOrder)
	}
	if projection.EffectsCommitted {
		t.Fatal("the projection must not be committed after the rollback")
	}
	if err := record.ReconcileBoundary(); err != nil {
		t.Fatalf("boundary reconciliation: %v", err)
	}
}

func TestNormalizeRejectsAuthorizationTuples(t *testing.T) {
	env := ordinaryEnvelope(4)
	env.Authorizations = []authorization.Tuple{{Nonce: 1}}
	obs := &Observation{Envelope: env, Coinbase: fixtureCoinbase, Receipt: successReceipt()}
	if _, err := New().Normalize(obs); !errors.Is(err, ErrAuthorization) {
		t.Fatalf("authorization error = %v", err)
	}
}

func TestNormalizeRejectsReceiptTypeDrift(t *testing.T) {
	obs := &Observation{
		Envelope: ordinaryEnvelope(5), Coinbase: fixtureCoinbase,
		Events:  []Event{evmRootFrame(fixturePayer, fixtureTo, calls.OutcomeSuccess)},
		Receipt: successReceipt(),
	}
	obs.Receipt.NativeReceiptType = 2
	if _, err := New().Normalize(obs); !errors.Is(err, ErrReceiptTypeDrift) {
		t.Fatalf("receipt drift error = %v", err)
	}
}

// The same observation always normalizes to the same logical digest; a
// changed logical fact changes it.
func TestNormalizeDeterminism(t *testing.T) {
	owner := uint64(0)
	build := func() *Observation {
		return &Observation{
			Envelope: ordinaryEnvelope(6), Coinbase: fixtureCoinbase,
			Events: []Event{
				groupEvent(1, bscbalance.RoleGasPurchase, 0, mut(fixturePayer, 50000, 8000)),
				evmRootFrame(fixturePayer, fixtureTo, calls.OutcomeSuccess),
				snapshotCreate(1, &owner),
				transferEvent(2, 0, 1, mut(fixturePayer, 8000, 7995), mut(fixtureTo, 0, 5)),
				{Snapshot: &SnapshotEvent{NativeID: 1, Kind: SnapshotEnd}},
				groupEvent(3, bscbalance.RoleGasReturn, 0, mut(fixturePayer, 7995, 9995)),
				groupEvent(4, bscbalance.RoleFeeRecipientCredit, 0, mut(bscbalance.FeeRecipient, 0, 40000)),
			},
			Receipt:  successReceipt(),
			Boundary: []state.AccountChange{accountRow(fixturePayer, 50000, 9995, 0, 1)},
		}
	}
	first, err := New().Normalize(build())
	if err != nil {
		t.Fatalf("first normalize: %v", err)
	}
	second, err := New().Normalize(build())
	if err != nil {
		t.Fatalf("second normalize: %v", err)
	}
	if first.Digest() != second.Digest() {
		t.Fatal("the same observation must normalize to the same digest")
	}
	changed := build()
	changed.Events[3] = transferEvent(9, 0, 1, mut(fixturePayer, 8000, 7990), mut(fixtureTo, 0, 10))
	third, err := New().Normalize(changed)
	if err != nil {
		t.Fatalf("changed normalize: %v", err)
	}
	if third.Digest() == first.Digest() {
		t.Fatal("a changed logical fact must change the digest")
	}
}

func TestNormalizeEventShape(t *testing.T) {
	obs := &Observation{
		Envelope: ordinaryEnvelope(7), Coinbase: fixtureCoinbase, Receipt: successReceipt(),
		Events: []Event{{}},
	}
	if _, err := New().Normalize(obs); !errors.Is(err, ErrEventShape) {
		t.Fatalf("empty event error = %v", err)
	}
}
