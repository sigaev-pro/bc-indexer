package balance

import (
	"errors"
	"math"
	"testing"
)

// Fixture-local native reason codes. The wire reason enum is pinned per
// profile (E02-10.closed_profiles); this package treats codes as opaque, so
// the tests use stable placeholders that stand for the native reasons.
const (
	reasonGasPurchase  uint32 = 1
	reasonGasReturn    uint32 = 2
	reasonFeeCredit    uint32 = 3
	reasonTransfer     uint32 = 4
	reasonSelfDestruct uint32 = 5
	reasonBurn         uint32 = 6
)

var (
	addrPayer       = MustParseAddress("7e5f4552091a69125d5dfcb7b8c2659029395bdf")
	addrRecipient   = MustParseAddress("4000000000000000000000000000000000000001")
	addrCoinbase    = MustParseAddress("2000000000000000000000000000000000000000")
	addrContract    = MustParseAddress("3000000000000000000000000000000000000001")
	addrChild       = MustParseAddress("3000000000000000000000000000000000000002")
	addrBeneficiary = MustParseAddress("5000000000000000000000000000000000000001")
)

func addrPtr(a Address20) *Address20 { return &a }

func u(s string) UInt256 { return MustParseUInt256(s) }

// validNested is a full valid record: fees, a committed root transfer, a
// rolled back child call attempt, a gas return and a fee credit. Execution
// orders follow the D74 synthetic example: 1 purchase, 2 root frame entry,
// 3 root transfer, 4 child frame entry, 5 child transfer, 6 gas return,
// 7 fee credit.
func validNested() TxBalances {
	payerAfterPurchase := u("70")
	payerAfterTransfer := u("65")
	payerAfterReturn := u("68")
	return TxBalances{
		Scopes: []RollbackScope{
			TxBoundaryScope(),
			{ScopeID: 1, ParentScopeID: scopePtr(0), OwnerFrameID: scopePtr(0)},
			{ScopeID: 2, ParentScopeID: scopePtr(1), OwnerFrameID: scopePtr(1), RolledBack: true},
		},
		Frames: []FrameRef{
			{FrameID: 0, ExecutionOrder: 2},
			{FrameID: 1, ExecutionOrder: 4},
		},
		Actions: []BalanceAction{
			{
				ActionIndex: 0, ExecutionOrder: 1,
				Cause: ProtocolCause(OpGasPurchase, 0), Engine: EngineEVM, Kind: KindFee,
				From: addrPtr(addrPayer), Amount: u("30"), Asset: AssetNative,
				EffectsCommitted: true, RollbackScope: scopePtr(0),
				Effects: []BalanceEffect{
					{EffectIndex: 0, Address: addrPayer, Before: u("100"), After: payerAfterPurchase, NativeReasonCode: reasonGasPurchase},
				},
			},
			{
				ActionIndex: 1, ExecutionOrder: 3,
				Cause: FrameCause(0), Engine: EngineEVM, Kind: KindTransfer,
				From: addrPtr(addrPayer), To: addrPtr(addrRecipient), Amount: u("5"), Asset: AssetNative,
				EffectsCommitted: true, RollbackScope: scopePtr(1),
				Effects: []BalanceEffect{
					{EffectIndex: 0, Address: addrPayer, Before: payerAfterPurchase, After: payerAfterTransfer, NativeReasonCode: reasonTransfer},
					{EffectIndex: 1, Address: addrRecipient, Before: u("0"), After: u("5"), NativeReasonCode: reasonTransfer},
				},
			},
			{
				ActionIndex: 2, ExecutionOrder: 5,
				Cause: FrameCause(1), Engine: EngineEVM, Kind: KindTransfer,
				From: addrPtr(addrRecipient), To: addrPtr(addrChild), Amount: u("5"), Asset: AssetNative,
				EffectsCommitted: false, RollbackScope: scopePtr(2),
				Effects: []BalanceEffect{
					{EffectIndex: 0, Address: addrRecipient, Before: u("5"), After: u("0"), NativeReasonCode: reasonTransfer},
					{EffectIndex: 1, Address: addrChild, Before: u("0"), After: u("5"), NativeReasonCode: reasonTransfer},
				},
			},
			{
				ActionIndex: 3, ExecutionOrder: 6,
				Cause: ProtocolCause(OpGasReturn, 1), Engine: EngineEVM, Kind: KindRefund,
				To: addrPtr(addrPayer), Amount: u("3"), Asset: AssetNative,
				EffectsCommitted: true, RollbackScope: scopePtr(0),
				Effects: []BalanceEffect{
					{EffectIndex: 0, Address: addrPayer, Before: payerAfterTransfer, After: payerAfterReturn, NativeReasonCode: reasonGasReturn},
				},
			},
			{
				ActionIndex: 4, ExecutionOrder: 7,
				Cause: ProtocolCause(OpFeeRecipientCredit, 2), Engine: EngineEVM, Kind: KindFee,
				To: addrPtr(addrCoinbase), Amount: u("2"), Asset: AssetNative,
				EffectsCommitted: true, RollbackScope: scopePtr(0),
				Effects: []BalanceEffect{
					{EffectIndex: 0, Address: addrCoinbase, Before: u("0"), After: u("2"), NativeReasonCode: reasonFeeCredit},
				},
			},
		},
		FeeSummary: &FeeSummary{
			Payer:            addrPtr(addrPayer),
			GasPurchaseDebit: u("30"),
			GasReturnCredit:  u("3"),
			NetCharge:        u("27"),
			RecipientCredits: []FeeCredit{{Address: addrCoinbase, Amount: u("2")}},
		},
	}
}

func nestedBoundary() (map[Address20]UInt256, map[Address20]UInt256) {
	return map[Address20]UInt256{
		addrPayer:     u("100"),
		addrRecipient: u("0"),
		addrChild:     u("0"),
		addrCoinbase:  u("0"),
	}, map[Address20]UInt256{
		addrPayer:     u("68"),
		addrRecipient: u("5"),
		addrChild:     u("0"),
		addrCoinbase:  u("2"),
	}
}

// validSimple is one plain committed transfer: purchase, frame entry,
// transfer, fee credit.
func validSimple() TxBalances {
	tx := validNested()
	tx.Scopes = tx.Scopes[:2]
	tx.Frames = tx.Frames[:1]
	tx.Actions = []BalanceAction{tx.Actions[0], tx.Actions[1], tx.Actions[4]}
	tx.Actions[2].ExecutionOrder = 4
	tx.FeeSummary = &FeeSummary{
		Payer:            addrPtr(addrPayer),
		GasPurchaseDebit: u("30"),
		GasReturnCredit:  u("0"),
		NetCharge:        u("30"),
		RecipientCredits: []FeeCredit{{Address: addrCoinbase, Amount: u("2")}},
	}
	return tx
}

// validPairing exercises the only allowed shared execution_order: the
// SELFDESTRUCT execution action and its monetary projection joined by the
// exact ExecutionAction cause.
func validPairing() TxBalances {
	return TxBalances{
		Scopes: []RollbackScope{
			TxBoundaryScope(),
			{ScopeID: 1, ParentScopeID: scopePtr(0), OwnerFrameID: scopePtr(0)},
		},
		Frames:           []FrameRef{{FrameID: 0, ExecutionOrder: 2}},
		ExecutionActions: []ExecutionActionRef{{ExecutionActionID: 9, ExecutionOrder: 3}},
		Actions: []BalanceAction{
			{
				ActionIndex: 0, ExecutionOrder: 1,
				Cause: ProtocolCause(OpGasPurchase, 0), Engine: EngineEVM, Kind: KindFee,
				From: addrPtr(addrPayer), Amount: u("10"), Asset: AssetNative,
				EffectsCommitted: true, RollbackScope: scopePtr(0),
				Effects: []BalanceEffect{
					{EffectIndex: 0, Address: addrPayer, Before: u("100"), After: u("90"), NativeReasonCode: reasonGasPurchase},
				},
			},
			{
				ActionIndex: 1, ExecutionOrder: 3,
				Cause: ExecutionActionCause(9), Engine: EngineEVM, Kind: KindSelfDestructTransfer,
				From: addrPtr(addrContract), To: addrPtr(addrBeneficiary), Amount: u("6"), Asset: AssetNative,
				EffectsCommitted: true, RollbackScope: scopePtr(1),
				Effects: []BalanceEffect{
					{EffectIndex: 0, Address: addrContract, Before: u("10"), After: u("4"), NativeReasonCode: reasonSelfDestruct},
					{EffectIndex: 1, Address: addrBeneficiary, Before: u("0"), After: u("6"), NativeReasonCode: reasonSelfDestruct},
				},
			},
		},
		FeeSummary: &FeeSummary{
			Payer:            addrPtr(addrPayer),
			GasPurchaseDebit: u("10"),
			NetCharge:        u("10"),
		},
	}
}

// validFinalizationBurn extends validPairing with the EIP-6780 finalization
// burn: a separate operation at its own position, linked to the SELFDESTRUCT
// execution action through related_execution_actions.
func validFinalizationBurn() TxBalances {
	tx := validPairing()
	tx.Actions[1].Effects[0].After = u("4")
	burn := BalanceAction{
		ActionIndex: 2, ExecutionOrder: 4,
		Cause: ProtocolCause(OpSelfDestructFinalizationBurn, 1, 9), Engine: EngineEVM, Kind: KindBurn,
		From: addrPtr(addrContract), Amount: u("4"), Asset: AssetNative,
		EffectsCommitted: true, RollbackScope: scopePtr(0),
		Effects: []BalanceEffect{
			{EffectIndex: 0, Address: addrContract, Before: u("4"), After: u("0"), NativeReasonCode: reasonBurn},
		},
	}
	tx.Actions = append(tx.Actions, burn)
	return tx
}

// validZeroFee covers a zero-fee system transaction: applicable explicit
// zeros in the summary and no fee actions at all.
func validZeroFee() TxBalances {
	return TxBalances{
		Scopes: []RollbackScope{
			TxBoundaryScope(),
			{ScopeID: 1, ParentScopeID: scopePtr(0), OwnerFrameID: scopePtr(0)},
		},
		Frames:     []FrameRef{{FrameID: 0, ExecutionOrder: 1}},
		FeeSummary: &FeeSummary{},
	}
}

// validSystemExecution covers a native protocol execution action (here a
// Bor state-sync delivery): the actual effects of a known native system
// operation are kept as-is, without an invented economic transfer cause.
func validSystemExecution() TxBalances {
	return TxBalances{
		Scopes: []RollbackScope{
			TxBoundaryScope(),
			{ScopeID: 1, ParentScopeID: scopePtr(0), OwnerFrameID: scopePtr(0)},
		},
		Frames: []FrameRef{{FrameID: 0, ExecutionOrder: 1}},
		Actions: []BalanceAction{
			{
				ActionIndex: 0, ExecutionOrder: 2,
				Cause: ProtocolCause(OpBorStateSyncExecution, 0), Engine: EngineNativeProtocol, Kind: KindNativeProtocolChange,
				To: addrPtr(addrRecipient), Amount: u("7"), Asset: AssetNative,
				EffectsCommitted: true, RollbackScope: scopePtr(1),
				Effects: []BalanceEffect{
					{EffectIndex: 0, Address: addrRecipient, Before: u("0"), After: u("7"), NativeReasonCode: reasonTransfer},
				},
			},
		},
		FeeSummary: &FeeSummary{},
	}
}

func TestValidatorAcceptsValidRecords(t *testing.T) {
	records := map[string]func() TxBalances{
		"nested":          validNested,
		"simple":          validSimple,
		"pairing":         validPairing,
		"burn":            validFinalizationBurn,
		"zeroFee":         validZeroFee,
		"systemExecution": validSystemExecution,
	}
	for name, build := range records {
		if err := Validate(build()); err != nil {
			t.Fatalf("%s: valid record rejected: %v", name, err)
		}
	}
}

func TestReconcileAcceptsValidRecords(t *testing.T) {
	before, after := nestedBoundary()
	if err := ReconcileBoundary(validNested().Scopes, validNested().Actions, before, after); err != nil {
		t.Fatalf("nested: %v", err)
	}
	beforePairing := map[Address20]UInt256{addrPayer: u("100"), addrContract: u("10"), addrBeneficiary: u("0")}
	afterPairing := map[Address20]UInt256{addrPayer: u("90"), addrContract: u("4"), addrBeneficiary: u("6")}
	if err := ReconcileBoundary(validPairing().Scopes, validPairing().Actions, beforePairing, afterPairing); err != nil {
		t.Fatalf("pairing: %v", err)
	}
	afterBurn := map[Address20]UInt256{addrPayer: u("90"), addrContract: u("0"), addrBeneficiary: u("6")}
	if err := ReconcileBoundary(validFinalizationBurn().Scopes, validFinalizationBurn().Actions, beforePairing, afterBurn); err != nil {
		t.Fatalf("burn: %v", err)
	}
}

func TestExecutionOrderMonotonicWithinTx(t *testing.T) {
	tx := validNested()
	previous := uint64(0)
	for _, a := range tx.Actions {
		if a.ExecutionOrder <= previous {
			t.Fatalf("action orders must strictly increase inside one tx: %d after %d", a.ExecutionOrder, previous)
		}
		previous = a.ExecutionOrder
	}
	if err := Validate(tx); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
}

// TestValidatorMutationTable is the AC-DAT-05-03 vector table: every
// violation class is rejected through its own sentinel error.
func TestValidatorMutationTable(t *testing.T) {
	type mutation struct {
		name   string
		base   func() TxBalances
		mutate func(*TxBalances)
		want   error
	}
	table := []mutation{
		// --- execution_order of the single shared counter ---
		{"duplicate order in actions", validNested,
			func(tx *TxBalances) { tx.Actions[4].ExecutionOrder = 6 }, ErrOrderDuplicate},
		{"gap in order", validNested,
			func(tx *TxBalances) { tx.Actions[4].ExecutionOrder = 8 }, ErrOrderGap},
		{"zero order", validNested,
			func(tx *TxBalances) { tx.Actions[0].ExecutionOrder = 0 }, ErrOrderInvalid},
		{"order jump to counter ceiling", validNested,
			func(tx *TxBalances) { tx.Actions[4].ExecutionOrder = math.MaxUint64 }, ErrOrderGap},
		{"action order equals frame entry order", validSimple,
			func(tx *TxBalances) { tx.Actions[1].ExecutionOrder = 2 }, ErrFalseOrderEquality},
		{"execution action order equals frame order", validPairing,
			func(tx *TxBalances) { tx.ExecutionActions[0].ExecutionOrder = 2 }, ErrFalseOrderEquality},
		{"shared order without the exact execution action cause", validPairing,
			func(tx *TxBalances) { tx.Actions[1].Cause = FrameCause(0) }, ErrFalseOrderEquality},
		{"projection order differs from its execution action", validPairing,
			func(tx *TxBalances) { tx.Actions[1].ExecutionOrder = 4 }, ErrCauseOrderMismatch},
		{"two projections of one execution action", validPairing,
			func(tx *TxBalances) {
				second := tx.Actions[1]
				second.ActionIndex = 2
				tx.Actions = append(tx.Actions, second)
			}, ErrFalseOrderEquality},
		{"unsorted action list", validNested,
			func(tx *TxBalances) { tx.Actions[0], tx.Actions[1] = tx.Actions[1], tx.Actions[0] }, ErrListUnsorted},
		{"duplicate action_index", validNested,
			func(tx *TxBalances) { tx.Actions[4].ActionIndex = 3 }, ErrActionIndexDuplicate},

		// --- rollback scope tree ---
		{"frame list empty", validNested,
			func(tx *TxBalances) { tx.Frames = nil }, ErrNoFrames},
		{"scope parent cycle", validNested,
			func(tx *TxBalances) {
				tx.Scopes[1].ParentScopeID = scopePtr(2)
				tx.Scopes[2].ParentScopeID = scopePtr(1)
			}, ErrScopeCycle},
		{"scope self parent cycle", validNested,
			func(tx *TxBalances) { tx.Scopes[2].ParentScopeID = scopePtr(2) }, ErrScopeCycle},
		{"scope orphan parent", validNested,
			func(tx *TxBalances) { tx.Scopes[1].ParentScopeID = scopePtr(42) }, ErrScopeParentMissing},
		{"scope parent absent", validNested,
			func(tx *TxBalances) { tx.Scopes[2].ParentScopeID = nil }, ErrScopeParentAbsent},
		{"scope id gap", validNested,
			func(tx *TxBalances) {
				tx.Scopes[2].ScopeID = 3
				tx.Actions[2].RollbackScope = scopePtr(3)
			}, ErrScopeIDGap},
		{"scope id duplicate", validNested,
			func(tx *TxBalances) { tx.Scopes = append(tx.Scopes, tx.Scopes[2]) }, ErrScopeIDDuplicate},
		{"scope 0 missing", validNested,
			func(tx *TxBalances) { tx.Scopes = tx.Scopes[1:] }, ErrScopeZeroMissing},
		{"scope 0 rolled back", validNested,
			func(tx *TxBalances) { tx.Scopes[0].RolledBack = true }, ErrScopeZeroRolledBack},
		{"scope 0 with parent", validNested,
			func(tx *TxBalances) { tx.Scopes[0].ParentScopeID = scopePtr(0) }, ErrScopeZeroParent},
		{"scope 0 with owner frame", validNested,
			func(tx *TxBalances) { tx.Scopes[0].OwnerFrameID = scopePtr(0) }, ErrScopeZeroOwner},
		{"owner frame unknown", validNested,
			func(tx *TxBalances) { tx.Scopes[2].OwnerFrameID = scopePtr(99) }, ErrOwnerFrameUnknown},
		{"rollback not propagated to descendant", validNested,
			func(tx *TxBalances) {
				tx.Scopes[1].RolledBack = true
				tx.Scopes[2].RolledBack = false
				tx.Actions[1].EffectsCommitted = false
			}, ErrRollbackNotPropagated},

		// --- references and closed unions ---
		{"unknown frame cause", validNested,
			func(tx *TxBalances) { tx.Actions[1].Cause = FrameCause(99) }, ErrCauseRefUnknown},
		{"unknown execution action cause", validPairing,
			func(tx *TxBalances) { tx.Actions[1].Cause = ExecutionActionCause(99) }, ErrCauseRefUnknown},
		{"unspecified cause kind", validNested,
			func(tx *TxBalances) { tx.Actions[1].Cause = Cause{} }, ErrCauseKindUnknown},
		{"unspecified protocol operation", validNested,
			func(tx *TxBalances) { tx.Actions[0].Cause = ProtocolCause(OpUnspecified, 0) }, ErrCauseOperationUnknown},
		{"kind mismatch for frame cause", validNested,
			func(tx *TxBalances) { tx.Actions[1].Kind = KindFee }, ErrCauseKindMismatch},
		{"engine mismatch", validNested,
			func(tx *TxBalances) { tx.Actions[1].Engine = EngineNativeProtocol }, ErrEngineMismatch},
		{"asset not native", validNested,
			func(tx *TxBalances) { tx.Actions[1].Asset = AssetUnspecified }, ErrAssetNotNative},
		{"burn without related execution actions", validFinalizationBurn,
			func(tx *TxBalances) { tx.Actions[2].Cause = ProtocolCause(OpSelfDestructFinalizationBurn, 1) }, ErrRelatedActionsMissing},
		{"burn related execution action unknown", validFinalizationBurn,
			func(tx *TxBalances) { tx.Actions[2].Cause = ProtocolCause(OpSelfDestructFinalizationBurn, 1, 99) }, ErrCauseRefUnknown},

		// --- action-to-scope attribution ---
		{"absent rollback scope with effects", validNested,
			func(tx *TxBalances) { tx.Actions[1].RollbackScope = nil }, ErrRollbackScopeAbsentWithEffects},
		{"unknown rollback scope", validNested,
			func(tx *TxBalances) { tx.Actions[1].RollbackScope = scopePtr(99) }, ErrRollbackScopeUnknown},
		{"committed effect inside rolled back scope", validNested,
			func(tx *TxBalances) { tx.Actions[2].EffectsCommitted = true }, ErrCommittedInRolledBackScope},
		{"fee action outside scope 0", validNested,
			func(tx *TxBalances) { tx.Actions[0].RollbackScope = scopePtr(1) }, ErrFeeScope},
		{"fee action not committed", validNested,
			func(tx *TxBalances) { tx.Actions[0].EffectsCommitted = false }, ErrFeeNotCommitted},
		{"committed without effects", validNested,
			func(tx *TxBalances) { tx.Actions[0].Effects = nil }, ErrCommittedWithoutEffects},

		// --- effect structure ---
		{"effect index gap", validNested,
			func(tx *TxBalances) { tx.Actions[1].Effects[1].EffectIndex = 7 }, ErrEffectIndex},
		{"transfer missing credit side", validNested,
			func(tx *TxBalances) { tx.Actions[1].Effects = tx.Actions[1].Effects[:1] }, ErrEffectShape},
		{"transfer effects on wrong addresses", validNested,
			func(tx *TxBalances) { tx.Actions[1].Effects[1].Address = addrChild }, ErrEffectShape},
		{"self-address transfer effect off the address", validNested,
			func(tx *TxBalances) { tx.Actions[1].To = addrPtr(addrPayer) }, ErrEffectShape},
		{"self-address single effect not net zero", validNested,
			func(tx *TxBalances) {
				tx.Actions[1].To = addrPtr(addrPayer)
				tx.Actions[1].Effects = tx.Actions[1].Effects[:1]
			}, ErrEffectShape},
		{"self-address action with three effects", validNested,
			func(tx *TxBalances) {
				tx.Actions[1].To = addrPtr(addrPayer)
				after := tx.Actions[1].Effects[0].After
				tx.Actions[1].Effects = []BalanceEffect{
					{EffectIndex: 0, Address: addrPayer, Before: u("70"), After: u("65"), NativeReasonCode: reasonTransfer},
					{EffectIndex: 1, Address: addrPayer, Before: u("65"), After: after, NativeReasonCode: reasonTransfer},
					{EffectIndex: 2, Address: addrPayer, Before: after, After: after, NativeReasonCode: reasonTransfer},
				}
			}, ErrEffectShape},
		{"purchase debit shape broken", validNested,
			func(tx *TxBalances) { tx.Actions[0].Effects[0].After = u("39") }, ErrEffectShape},
		{"purchase with two effects", validNested,
			func(tx *TxBalances) {
				after := tx.Actions[0].Effects[0].After
				tx.Actions[0].Effects = append(tx.Actions[0].Effects,
					BalanceEffect{EffectIndex: 1, Address: addrPayer, Before: after, After: after, NativeReasonCode: reasonGasPurchase})
			}, ErrEffectShape},
		{"gas return charged instead of credited", validNested,
			func(tx *TxBalances) { tx.Actions[3].Effects[0].After = u("62") }, ErrEffectShape},
		{"fee credit on the wrong address", validNested,
			func(tx *TxBalances) { tx.Actions[4].Effects[0].Address = addrPayer }, ErrEffectShape},
		{"burn amount shape broken", validFinalizationBurn,
			func(tx *TxBalances) { tx.Actions[2].Amount = u("5") }, ErrEffectShape},
		{"system execution with an evm engine", validSystemExecution,
			func(tx *TxBalances) { tx.Actions[0].Engine = EngineEVM }, ErrCauseKindMismatch},
		{"system execution dressed as a transfer", validSystemExecution,
			func(tx *TxBalances) { tx.Actions[0].Kind = KindTransfer }, ErrCauseKindMismatch},

		// --- fee summary stays an aggregate, never a second movement ---
		{"summary debit inflated", validNested,
			func(tx *TxBalances) { tx.FeeSummary.GasPurchaseDebit = u("31") }, ErrSummaryMismatch},
		{"summary net broken", validNested,
			func(tx *TxBalances) { tx.FeeSummary.NetCharge = u("26") }, ErrSummaryMismatch},
		{"summary credit dropped", validNested,
			func(tx *TxBalances) { tx.FeeSummary.RecipientCredits = nil }, ErrSummaryMismatch},
		{"summary payer dropped", validNested,
			func(tx *TxBalances) { tx.FeeSummary.Payer = nil }, ErrSummaryMismatch},
		{"summary return credit underflows", validNested,
			func(tx *TxBalances) { tx.FeeSummary.GasReturnCredit = u("31") }, ErrSummaryMismatch},
		{"extra credit action beyond the summary", validNested,
			func(tx *TxBalances) {
				credit := tx.Actions[4]
				credit.ActionIndex = 5
				credit.ExecutionOrder = 8
				credit.Effects[0].After = u("4")
				tx.Actions = append(tx.Actions, credit)
			}, ErrSummaryMismatch},
	}

	for _, m := range table {
		tx := m.base()
		m.mutate(&tx)
		err := Validate(tx)
		if err == nil {
			t.Fatalf("%s: violation accepted", m.name)
		}
		if !errors.Is(err, m.want) {
			t.Fatalf("%s: got %v, want %v", m.name, err, m.want)
		}
	}
}

// TestNetZeroPreservation proves that actions whose effects cancel out stay
// in the record: a self-transfer keeps one net-zero effect, and two opposing
// transfers remain two actions with a zero per-address sum.
func TestNetZeroPreservation(t *testing.T) {
	self := TxBalances{
		Scopes: []RollbackScope{
			TxBoundaryScope(),
			{ScopeID: 1, ParentScopeID: scopePtr(0), OwnerFrameID: scopePtr(0)},
		},
		Frames: []FrameRef{{FrameID: 0, ExecutionOrder: 2}},
		Actions: []BalanceAction{
			{
				ActionIndex: 0, ExecutionOrder: 1,
				Cause: ProtocolCause(OpGasPurchase, 0), Engine: EngineEVM, Kind: KindFee,
				From: addrPtr(addrPayer), Amount: u("1"), Asset: AssetNative,
				EffectsCommitted: true, RollbackScope: scopePtr(0),
				Effects: []BalanceEffect{
					{EffectIndex: 0, Address: addrPayer, Before: u("100"), After: u("99"), NativeReasonCode: reasonGasPurchase},
				},
			},
			{
				ActionIndex: 1, ExecutionOrder: 3,
				Cause: FrameCause(0), Engine: EngineEVM, Kind: KindTransfer,
				From: addrPtr(addrPayer), To: addrPtr(addrPayer), Amount: u("5"), Asset: AssetNative,
				EffectsCommitted: true, RollbackScope: scopePtr(1),
				Effects: []BalanceEffect{
					{EffectIndex: 0, Address: addrPayer, Before: u("99"), After: u("99"), NativeReasonCode: reasonTransfer},
				},
			},
		},
		FeeSummary: &FeeSummary{
			Payer:            addrPtr(addrPayer),
			GasPurchaseDebit: u("1"),
			NetCharge:        u("1"),
		},
	}
	if err := Validate(self); err != nil {
		t.Fatalf("net-zero self-transfer rejected: %v", err)
	}
	if len(self.Actions) != 2 || len(self.Actions[1].Effects) != 1 {
		t.Fatalf("the net-zero action or its effect disappeared: %d actions", len(self.Actions))
	}
	if err := ReconcileBoundary(self.Scopes, self.Actions,
		map[Address20]UInt256{addrPayer: u("100")},
		map[Address20]UInt256{addrPayer: u("99")}); err != nil {
		t.Fatalf("net-zero self-transfer does not reconcile: %v", err)
	}

	roundTrip := TxBalances{
		Scopes: []RollbackScope{
			TxBoundaryScope(),
			{ScopeID: 1, ParentScopeID: scopePtr(0), OwnerFrameID: scopePtr(0)},
			{ScopeID: 2, ParentScopeID: scopePtr(1), OwnerFrameID: scopePtr(1)},
		},
		Frames: []FrameRef{{FrameID: 0, ExecutionOrder: 2}, {FrameID: 1, ExecutionOrder: 4}},
		Actions: []BalanceAction{
			{
				ActionIndex: 0, ExecutionOrder: 1,
				Cause: ProtocolCause(OpGasPurchase, 0), Engine: EngineEVM, Kind: KindFee,
				From: addrPtr(addrPayer), Amount: u("1"), Asset: AssetNative,
				EffectsCommitted: true, RollbackScope: scopePtr(0),
				Effects: []BalanceEffect{
					{EffectIndex: 0, Address: addrPayer, Before: u("100"), After: u("99"), NativeReasonCode: reasonGasPurchase},
				},
			},
			{
				ActionIndex: 1, ExecutionOrder: 3,
				Cause: FrameCause(0), Engine: EngineEVM, Kind: KindTransfer,
				From: addrPtr(addrContract), To: addrPtr(addrBeneficiary), Amount: u("10"), Asset: AssetNative,
				EffectsCommitted: true, RollbackScope: scopePtr(1),
				Effects: []BalanceEffect{
					{EffectIndex: 0, Address: addrContract, Before: u("10"), After: u("0"), NativeReasonCode: reasonTransfer},
					{EffectIndex: 1, Address: addrBeneficiary, Before: u("0"), After: u("10"), NativeReasonCode: reasonTransfer},
				},
			},
			{
				ActionIndex: 2, ExecutionOrder: 5,
				Cause: FrameCause(1), Engine: EngineEVM, Kind: KindTransfer,
				From: addrPtr(addrBeneficiary), To: addrPtr(addrContract), Amount: u("10"), Asset: AssetNative,
				EffectsCommitted: true, RollbackScope: scopePtr(2),
				Effects: []BalanceEffect{
					{EffectIndex: 0, Address: addrBeneficiary, Before: u("10"), After: u("0"), NativeReasonCode: reasonTransfer},
					{EffectIndex: 1, Address: addrContract, Before: u("0"), After: u("10"), NativeReasonCode: reasonTransfer},
				},
			},
		},
		FeeSummary: &FeeSummary{
			Payer:            addrPtr(addrPayer),
			GasPurchaseDebit: u("1"),
			NetCharge:        u("1"),
		},
	}
	if err := Validate(roundTrip); err != nil {
		t.Fatalf("compensating transfers rejected: %v", err)
	}
	if len(roundTrip.Actions) != 3 {
		t.Fatalf("+10/-10 must stay two transfer actions, got %d actions", len(roundTrip.Actions)-1)
	}
	if err := ReconcileBoundary(roundTrip.Scopes, roundTrip.Actions,
		map[Address20]UInt256{addrPayer: u("100"), addrContract: u("10"), addrBeneficiary: u("0")},
		map[Address20]UInt256{addrPayer: u("99"), addrContract: u("10"), addrBeneficiary: u("0")}); err != nil {
		t.Fatalf("compensating transfers do not reconcile: %v", err)
	}
}

// TestSelfBeneficiarySelfDestructKeepsNetZeroEffect covers a self-destruct
// whose beneficiary is the contract itself: two chained effects on one
// address that sum to zero, both kept.
func TestSelfBeneficiarySelfDestructKeepsNetZeroEffect(t *testing.T) {
	tx := validPairing()
	tx.Actions[1].To = addrPtr(addrContract)
	tx.Actions[1].Effects = []BalanceEffect{
		{EffectIndex: 0, Address: addrContract, Before: u("10"), After: u("4"), NativeReasonCode: reasonSelfDestruct},
		{EffectIndex: 1, Address: addrContract, Before: u("4"), After: u("10"), NativeReasonCode: reasonSelfDestruct},
	}
	if err := Validate(tx); err != nil {
		t.Fatalf("self-beneficiary self-destruct rejected: %v", err)
	}
	before := map[Address20]UInt256{addrPayer: u("100"), addrContract: u("10")}
	after := map[Address20]UInt256{addrPayer: u("90"), addrContract: u("10")}
	if err := ReconcileBoundary(tx.Scopes, tx.Actions, before, after); err != nil {
		t.Fatalf("self-beneficiary self-destruct does not reconcile: %v", err)
	}
	// Breaking the chain between the two effects on one address is caught.
	tx.Actions[1].Effects[1].Before = u("7")
	if err := Validate(tx); !errors.Is(err, ErrEffectChain) {
		t.Fatalf("broken effect chain accepted: %v", err)
	}
}

func TestReconcileBoundaryRejects(t *testing.T) {
	build := func() (TxBalances, map[Address20]UInt256, map[Address20]UInt256) {
		before, after := nestedBoundary()
		return validNested(), before, after
	}

	tx, before, after := build()
	after[addrPayer] = u("67")
	if err := ReconcileBoundary(tx.Scopes, tx.Actions, before, after); !errors.Is(err, ErrBoundaryMismatch) {
		t.Fatalf("wrong after point accepted: %v", err)
	}

	tx, before, after = build()
	delete(before, addrCoinbase)
	if err := ReconcileBoundary(tx.Scopes, tx.Actions, before, after); !errors.Is(err, ErrBoundaryBaselineMissing) {
		t.Fatalf("missing baseline accepted: %v", err)
	}

	tx, before, after = build()
	delete(after, addrCoinbase)
	if err := ReconcileBoundary(tx.Scopes, tx.Actions, before, after); !errors.Is(err, ErrBoundaryAfterMissing) {
		t.Fatalf("missing after point accepted: %v", err)
	}

	tx, before, after = build()
	after[addrChild] = u("3")
	if err := ReconcileBoundary(tx.Scopes, tx.Actions, before, after); !errors.Is(err, ErrBoundaryUnexplained) {
		t.Fatalf("unexplained boundary change accepted: %v", err)
	}

	// A committed action inside a rolled back scope never reconciles.
	tx, before, after = build()
	tx.Actions[2].EffectsCommitted = true
	if err := ReconcileBoundary(tx.Scopes, tx.Actions, before, after); !errors.Is(err, ErrCommittedInRolledBackScope) {
		t.Fatalf("committed attempt inside a rolled back scope reconciled: %v", err)
	}
}
