package balance

import (
	"errors"
	"testing"
)

// Reference effects fixtures for the three families, transcribed from the
// retained E01-03 smoke corpus (read-only in the main checkout):
//
//	eth.json  sha256 4c6ded51914d675da6fe71c90232f893bf42a0d1620d888e891132cb00332e29
//	bsc.json  sha256 939ee929b410ab0b637a2e8dc6fedff2db9ef53ebbfa0cc027de65cc02c3ba58
//	poly.json sha256 6ec2a6325ab26441e13a8433ac4378d4322e07d5864e19533e4cf3c3f64ac53b
//
// Every literal below is the corpus value of the named block and transaction
// index; the builder derives the intermediate balance points (gas purchase,
// transfer, gas return) with exact arithmetic from those literals, and the
// driver checks the result against the corpus state_before/state_after
// points and fee decomposition. The corpus cannot be read at test time: the
// tests must stay portable across checkouts.
//
// Fee model verified here for every fixture transaction:
//
//	gas purchase debit = gas_limit * effective_gas_price
//	gas return credit  = (gas_limit - gas_used) * effective_gas_price
//	net charge         = purchase - return = corpus fee.total
//
// On Polygon the base-fee burn is an actual credit to the zero address
// (corpus "burn_recipient"); on ETH the burn is implicit (no address effect,
// verified separately by fee rules); on BSC (istanbul) there is no burn.

var (
	corpusPayer          = MustParseAddress("7e5f4552091a69125d5dfcb7b8c2659029395bdf")
	corpusEOA            = MustParseAddress("4000000000000000000000000000000000000001")
	corpusCaller         = MustParseAddress("3000000000000000000000000000000000000001")
	corpusReverter       = MustParseAddress("3000000000000000000000000000000000000002")
	corpusEthCoinbase    = MustParseAddress("2000000000000000000000000000000000000000")
	corpusBscAccumulator = MustParseAddress("fffffffffffffffffffffffffffffffffffffffe")
	corpusPolyBurn       = MustParseAddress("0000000000000000000000000000000000000000")
	corpusPolyValidator  = MustParseAddress("71562b71999873db5b286df957af199ec94617f7")
)

type txShape int

const (
	// shapeTransfer is a plain committed value transfer.
	shapeTransfer txShape = iota
	// shapeCaughtRevert is a successful root call whose nested child call
	// reverted and was caught by the parent.
	shapeCaughtRevert
	// shapeRootRevert is a root-level revert: the root execution scope is
	// rolled back while the fee actions at scope 0 survive.
	shapeRootRevert
)

type corpusTxSpec struct {
	name  string
	shape txShape

	payer                   Address20
	payerBefore, payerAfter string

	gasLimit, gasUsed uint64
	gasPrice          string

	value                     string
	rootTo                    Address20
	rootToBefore, rootToAfter string

	childTo                     Address20
	childValue                  string
	childToBefore, childToAfter string

	tipRecipient                   Address20
	tipBefore, tipAfter, tipAmount string

	// burnRecipient is nil when the family burns implicitly (ETH) or not at
	// all (BSC istanbul); on Polygon it is the zero address and the credit
	// is an actual modeled action.
	burnRecipient *Address20
	burnAmount    string
	// burnBefore and burnAfter are the corpus balance points of the burn
	// recipient, used only when burnRecipient is set.
	burnBefore, burnAfter string
	// burnt is the corpus fee.burnt decomposition value; it is asserted
	// against the net charge regardless of whether it is modeled as an
	// action.
	burnt string

	feeTotal string
}

func polyBurnRecipient() *Address20 {
	burn := corpusPolyBurn
	return &burn
}

func corpusSpecs() []corpusTxSpec {
	return []corpusTxSpec{
		// --- ETH, london; coinbase 0x2000...00 receives the tip; burn is implicit ---
		{
			name:  "eth block 1 tx 0 (transfer)",
			shape: shapeTransfer,
			payer: corpusPayer, payerBefore: "10000000000000000000", payerAfter: "8999958000000000000",
			gasLimit: 21000, gasUsed: 21000, gasPrice: "2000000000",
			value:  "1000000000000000000",
			rootTo: corpusEOA, rootToBefore: "0", rootToAfter: "1000000000000000000",
			tipRecipient: corpusEthCoinbase, tipBefore: "0", tipAfter: "23625000000000", tipAmount: "23625000000000",
			burnt:    "18375000000000",
			feeTotal: "42000000000000",
		},
		{
			name:  "eth block 1 tx 1 (fee, eip-1559)",
			shape: shapeTransfer,
			payer: corpusPayer, payerBefore: "8999958000000000000", payerAfter: "8999911799000000000",
			gasLimit: 21000, gasUsed: 21000, gasPrice: "2200000000",
			value:  "1000000000",
			rootTo: corpusEOA, rootToBefore: "1000000000000000000", rootToAfter: "1000000001000000000",
			tipRecipient: corpusEthCoinbase, tipBefore: "23625000000000", tipAfter: "51450000000000", tipAmount: "27825000000000",
			burnt:    "18375000000000",
			feeTotal: "46200000000000",
		},
		{
			name:  "eth block 1 tx 2 (nested-revert, child caught)",
			shape: shapeCaughtRevert,
			payer: corpusPayer, payerBefore: "8999911799000000000", payerAfter: "8999626488999999995",
			gasLimit: 200000, gasUsed: 142655, gasPrice: "2000000000",
			value:  "5",
			rootTo: corpusCaller, rootToBefore: "0", rootToAfter: "5",
			childTo: corpusReverter, childValue: "5", childToBefore: "0", childToAfter: "0",
			tipRecipient: corpusEthCoinbase, tipBefore: "51450000000000", tipAfter: "211936875000000", tipAmount: "160486875000000",
			burnt:    "124823125000000",
			feeTotal: "285310000000000",
		},
		{
			name:  "eth block 1 tx 3 (nested-revert, root failed)",
			shape: shapeRootRevert,
			payer: corpusPayer, payerBefore: "8999626488999999995", payerAfter: "8999538722999999995",
			gasLimit: 100000, gasUsed: 43883, gasPrice: "2000000000",
			value:  "7",
			rootTo: corpusReverter, rootToBefore: "0", rootToAfter: "0",
			tipRecipient: corpusEthCoinbase, tipBefore: "211936875000000", tipAfter: "261305250000000", tipAmount: "49368375000000",
			burnt:    "38397625000000",
			feeTotal: "87766000000000",
		},

		// --- BSC, istanbul; the fee accumulator 0xff...fe receives the full fee as tip; no burn ---
		{
			name:  "bsc block 1 tx 0 (transfer)",
			shape: shapeTransfer,
			payer: corpusPayer, payerBefore: "10000000000000000000", payerAfter: "8999895000000000000",
			gasLimit: 21000, gasUsed: 21000, gasPrice: "5000000000",
			value:  "1000000000000000000",
			rootTo: corpusEOA, rootToBefore: "0", rootToAfter: "1000000000000000000",
			tipRecipient: corpusBscAccumulator, tipBefore: "0", tipAfter: "105000000000000", tipAmount: "105000000000000",
			burnt:    "0",
			feeTotal: "105000000000000",
		},
		{
			name:  "bsc block 1 tx 1 (nested-revert, child caught)",
			shape: shapeCaughtRevert,
			payer: corpusPayer, payerBefore: "8999895000000000000", payerAfter: "8999243724999999995",
			gasLimit: 200000, gasUsed: 130255, gasPrice: "5000000000",
			value:  "5",
			rootTo: corpusCaller, rootToBefore: "0", rootToAfter: "5",
			childTo: corpusReverter, childValue: "5", childToBefore: "0", childToAfter: "0",
			tipRecipient: corpusBscAccumulator, tipBefore: "105000000000000", tipAfter: "756275000000000", tipAmount: "651275000000000",
			burnt:    "0",
			feeTotal: "651275000000000",
		},
		{
			name:  "bsc block 1 tx 2 (nested-revert, root failed)",
			shape: shapeRootRevert,
			payer: corpusPayer, payerBefore: "8999243724999999995", payerAfter: "8999034809999999995",
			gasLimit: 100000, gasUsed: 41783, gasPrice: "5000000000",
			value:  "7",
			rootTo: corpusReverter, rootToBefore: "0", rootToAfter: "0",
			tipRecipient: corpusBscAccumulator, tipBefore: "756275000000000", tipAfter: "965190000000000", tipAmount: "208915000000000",
			burnt:    "0",
			feeTotal: "208915000000000",
		},

		// --- Polygon, london; the burn is an actual credit to the zero address; the validator receives the tip ---
		{
			name:  "poly block 1 tx 0 (transfer)",
			shape: shapeTransfer,
			payer: corpusPayer, payerBefore: "10000000000000000000", payerAfter: "8999895000000000000",
			gasLimit: 21000, gasUsed: 21000, gasPrice: "5000000000",
			value:  "1000000000000000000",
			rootTo: corpusEOA, rootToBefore: "0", rootToAfter: "1000000000000000000",
			tipRecipient: corpusPolyValidator, tipBefore: "1000000000000000000000", tipAfter: "1000000084000000000000", tipAmount: "84000000000000",
			burnRecipient: polyBurnRecipient(), burnAmount: "21000000000000",
			burnBefore: "0", burnAfter: "21000000000000",
			burnt:    "21000000000000",
			feeTotal: "105000000000000",
		},
		{
			name:  "poly block 1 tx 1 (fee, eip-1559)",
			shape: shapeTransfer,
			payer: corpusPayer, payerBefore: "8999895000000000000", payerAfter: "8999831999000000000",
			gasLimit: 21000, gasUsed: 21000, gasPrice: "3000000000",
			value:  "1000000000",
			rootTo: corpusEOA, rootToBefore: "1000000000000000000", rootToAfter: "1000000001000000000",
			tipRecipient: corpusPolyValidator, tipBefore: "1000000084000000000000", tipAfter: "1000000126000000000000", tipAmount: "42000000000000",
			burnRecipient: polyBurnRecipient(), burnAmount: "21000000000000",
			burnBefore: "21000000000000", burnAfter: "42000000000000",
			burnt:    "21000000000000",
			feeTotal: "63000000000000",
		},
		{
			name:  "poly block 1 tx 2 (nested-revert, child caught)",
			shape: shapeCaughtRevert,
			payer: corpusPayer, payerBefore: "8999831999000000000", payerAfter: "8999118723999999995",
			gasLimit: 200000, gasUsed: 142655, gasPrice: "5000000000",
			value:  "5",
			rootTo: corpusCaller, rootToBefore: "0", rootToAfter: "5",
			childTo: corpusReverter, childValue: "5", childToBefore: "0", childToAfter: "0",
			tipRecipient: corpusPolyValidator, tipBefore: "1000000126000000000000", tipAfter: "1000000696620000000000", tipAmount: "570620000000000",
			burnRecipient: polyBurnRecipient(), burnAmount: "142655000000000",
			burnBefore: "42000000000000", burnAfter: "184655000000000",
			burnt:    "142655000000000",
			feeTotal: "713275000000000",
		},
		{
			name:  "poly block 1 tx 3 (nested-revert, root failed)",
			shape: shapeRootRevert,
			payer: corpusPayer, payerBefore: "8999118723999999995", payerAfter: "8998899308999999995",
			gasLimit: 100000, gasUsed: 43883, gasPrice: "5000000000",
			value:  "7",
			rootTo: corpusReverter, rootToBefore: "0", rootToAfter: "0",
			tipRecipient: corpusPolyValidator, tipBefore: "1000000696620000000000", tipAfter: "1000000872152000000000", tipAmount: "175532000000000",
			burnRecipient: polyBurnRecipient(), burnAmount: "43883000000000",
			burnBefore: "184655000000000", burnAfter: "228538000000000",
			burnt:    "43883000000000",
			feeTotal: "219415000000000",
		},
	}
}

// buildCorpusTx materializes the typed balance view of one corpus
// transaction. Execution orders follow the D74 example: 1 gas purchase,
// 2 root frame entry, 3 root value transfer, 4 child frame entry, 5 child
// value transfer, 6 gas return, then the fee credits (Polygon: burn credit
// first, then the validator tip; the relative order of the two credits is an
// open question recorded in README.md).
func buildCorpusTx(spec corpusTxSpec) (tx TxBalances, before, after map[Address20]UInt256) {
	var order, actionIndex, protocolOp uint64
	nextOrder := func() uint64 { order++; return order }
	nextAction := func() uint64 { actionIndex++; return actionIndex }
	nextProtocolOp := func() uint64 { protocolOp++; return protocolOp - 1 }

	payerBefore := u(spec.payerBefore)
	value := u(spec.value)
	price := u(spec.gasPrice)

	purchase, ok := price.MulUint64(spec.gasLimit)
	if !ok {
		panic("gas purchase overflow in fixture " + spec.name)
	}
	refundGas, ok := price.MulUint64(spec.gasLimit - spec.gasUsed)
	if !ok {
		panic("gas refund overflow in fixture " + spec.name)
	}

	tx.Scopes = []RollbackScope{TxBoundaryScope()}
	tx.Frames = append(tx.Frames, FrameRef{FrameID: 0, ExecutionOrder: nextOrder()})
	scope1 := RollbackScope{ScopeID: 1, ParentScopeID: scopePtr(0), OwnerFrameID: scopePtr(0), RolledBack: spec.shape == shapeRootRevert}
	tx.Scopes = append(tx.Scopes, scope1)

	// Order 1: gas purchase (scope 0, committed).
	payerAfterPurchase, err := payerBefore.SubChecked(purchase)
	if err != nil {
		panic("gas purchase underflow in fixture " + spec.name)
	}
	tx.Actions = append(tx.Actions, BalanceAction{
		ActionIndex: nextAction(), ExecutionOrder: nextOrder(),
		Cause: ProtocolCause(OpGasPurchase, nextProtocolOp()), Engine: EngineEVM, Kind: KindFee,
		From: addrPtr(spec.payer), Amount: purchase, Asset: AssetNative,
		EffectsCommitted: true, RollbackScope: scopePtr(0),
		Effects: []BalanceEffect{
			{Address: spec.payer, Before: payerBefore, After: payerAfterPurchase, NativeReasonCode: reasonGasPurchase},
		},
	})

	// Root frame entry got order 2 above; its value transfer follows.
	rootCommitted := spec.shape != shapeRootRevert
	rootToBefore, rootToAfter := u(spec.rootToBefore), u(spec.rootToAfter)
	payerAfterTransfer, err := payerAfterPurchase.SubChecked(value)
	if err != nil {
		panic("root transfer underflow in fixture " + spec.name)
	}
	rootToAfterAttempt, err := rootToBefore.AddChecked(value)
	if err != nil {
		panic("root transfer overflow in fixture " + spec.name)
	}
	tx.Actions = append(tx.Actions, BalanceAction{
		ActionIndex: nextAction(), ExecutionOrder: nextOrder(),
		Cause: FrameCause(0), Engine: EngineEVM, Kind: KindTransfer,
		From: addrPtr(spec.payer), To: addrPtr(spec.rootTo), Amount: value, Asset: AssetNative,
		EffectsCommitted: rootCommitted, RollbackScope: scopePtr(1),
		Effects: []BalanceEffect{
			{Address: spec.payer, Before: payerAfterPurchase, After: payerAfterTransfer, NativeReasonCode: reasonTransfer},
			{Address: spec.rootTo, Before: rootToBefore, After: rootToAfterAttempt, NativeReasonCode: reasonTransfer},
		},
	})

	// The caught-revert case adds the child frame, its rolled back scope and
	// the kept attempt.
	if spec.shape == shapeCaughtRevert {
		tx.Frames = append(tx.Frames, FrameRef{FrameID: 1, ExecutionOrder: nextOrder()})
		tx.Scopes = append(tx.Scopes, RollbackScope{ScopeID: 2, ParentScopeID: scopePtr(1), OwnerFrameID: scopePtr(1), RolledBack: true})
		childValue := u(spec.childValue)
		childToBefore := u(spec.childToBefore)
		rootToAfterChild, err := rootToAfterAttempt.SubChecked(childValue)
		if err != nil {
			panic("child transfer underflow in fixture " + spec.name)
		}
		childToAfterAttempt, err := childToBefore.AddChecked(childValue)
		if err != nil {
			panic("child transfer overflow in fixture " + spec.name)
		}
		tx.Actions = append(tx.Actions, BalanceAction{
			ActionIndex: nextAction(), ExecutionOrder: nextOrder(),
			Cause: FrameCause(1), Engine: EngineEVM, Kind: KindTransfer,
			From: addrPtr(spec.rootTo), To: addrPtr(spec.childTo), Amount: childValue, Asset: AssetNative,
			EffectsCommitted: false, RollbackScope: scopePtr(2),
			Effects: []BalanceEffect{
				{Address: spec.rootTo, Before: rootToAfterAttempt, After: rootToAfterChild, NativeReasonCode: reasonTransfer},
				{Address: spec.childTo, Before: childToBefore, After: childToAfterAttempt, NativeReasonCode: reasonTransfer},
			},
		})
	}

	// Gas return: only an actual non-zero credit becomes an action; the
	// summary keeps an explicit zero otherwise (see README.md). An omitted
	// invocation consumes neither an execution_order nor a protocol
	// operation index.
	if !refundGas.IsZero() {
		payerCommitted, err := payerAfterPurchase.SubChecked(value)
		if err != nil {
			panic("committed balance underflow in fixture " + spec.name)
		}
		payerAfterReturn, err := payerCommitted.AddChecked(refundGas)
		if err != nil {
			panic("gas return overflow in fixture " + spec.name)
		}
		tx.Actions = append(tx.Actions, BalanceAction{
			ActionIndex: nextAction(), ExecutionOrder: nextOrder(),
			Cause: ProtocolCause(OpGasReturn, nextProtocolOp()), Engine: EngineEVM, Kind: KindRefund,
			To: addrPtr(spec.payer), Amount: refundGas, Asset: AssetNative,
			EffectsCommitted: true, RollbackScope: scopePtr(0),
			Effects: []BalanceEffect{
				{Address: spec.payer, Before: payerCommitted, After: payerAfterReturn, NativeReasonCode: reasonGasReturn},
			},
		})
	}

	// Fee credits at scope 0: the Polygon burn recipient first, then the
	// tip recipient. Both survive a root revert.
	addCredit := func(to Address20, amount, recipientBefore UInt256, reason uint32) {
		recipientAfter, err := recipientBefore.AddChecked(amount)
		if err != nil {
			panic("fee credit overflow in fixture " + spec.name)
		}
		tx.Actions = append(tx.Actions, BalanceAction{
			ActionIndex: nextAction(), ExecutionOrder: nextOrder(),
			Cause: ProtocolCause(OpFeeRecipientCredit, nextProtocolOp()), Engine: EngineEVM, Kind: KindFee,
			To: addrPtr(to), Amount: amount, Asset: AssetNative,
			EffectsCommitted: true, RollbackScope: scopePtr(0),
			Effects: []BalanceEffect{
				{Address: to, Before: recipientBefore, After: recipientAfter, NativeReasonCode: reason},
			},
		})
	}
	if spec.burnRecipient != nil {
		addCredit(*spec.burnRecipient, u(spec.burnAmount), u(spec.burnBefore), reasonFeeCredit)
	}
	addCredit(spec.tipRecipient, u(spec.tipAmount), u(spec.tipBefore), reasonFeeCredit)

	// The fee summary is the aggregate of the actions above, never a second
	// movement.
	net, err := purchase.SubChecked(refundGas)
	if err != nil {
		panic("net charge underflow in fixture " + spec.name)
	}
	summary := &FeeSummary{
		Payer:            addrPtr(spec.payer),
		GasPurchaseDebit: purchase,
		GasReturnCredit:  refundGas,
		NetCharge:        net,
	}
	if spec.burnRecipient != nil {
		summary.RecipientCredits = append(summary.RecipientCredits, FeeCredit{Address: *spec.burnRecipient, Amount: u(spec.burnAmount)})
	}
	summary.RecipientCredits = append(summary.RecipientCredits, FeeCredit{Address: spec.tipRecipient, Amount: u(spec.tipAmount)})
	tx.FeeSummary = summary

	// The builder is the author of the record, so it assigns the effect
	// order inside each action.
	for i := range tx.Actions {
		for j := range tx.Actions[i].Effects {
			tx.Actions[i].Effects[j].EffectIndex = uint64(j)
		}
	}

	// Boundary points from the corpus: every touched account, including the
	// accounts a rolled back attempt would have moved.
	before = map[Address20]UInt256{
		spec.payer:        payerBefore,
		spec.rootTo:       rootToBefore,
		spec.tipRecipient: u(spec.tipBefore),
	}
	after = map[Address20]UInt256{
		spec.payer:        u(spec.payerAfter),
		spec.rootTo:       rootToAfter,
		spec.tipRecipient: u(spec.tipAfter),
	}
	if spec.burnRecipient != nil {
		before[*spec.burnRecipient] = u(spec.burnBefore)
		after[*spec.burnRecipient] = u(spec.burnAfter)
	}
	if spec.shape == shapeCaughtRevert {
		before[spec.childTo] = u(spec.childToBefore)
		after[spec.childTo] = u(spec.childToAfter)
	}
	return tx, before, after
}

func TestCorpusFixturesPassValidationAndReconcile(t *testing.T) {
	for _, spec := range corpusSpecs() {
		tx, before, after := buildCorpusTx(spec)
		if err := Validate(tx); err != nil {
			t.Errorf("%s: invalid record: %v", spec.name, err)
			continue
		}
		if err := ReconcileBoundary(tx.Scopes, tx.Actions, before, after); err != nil {
			t.Errorf("%s: does not reconcile with the corpus boundary points: %v", spec.name, err)
		}

		s := tx.FeeSummary
		if s.NetCharge.Decimal() != spec.feeTotal {
			t.Errorf("%s: net charge %s != corpus fee total %s", spec.name, s.NetCharge.Decimal(), spec.feeTotal)
		}
		tipPlusBurn := u(spec.tipAmount)
		if spec.burnt != "" {
			sum, err := tipPlusBurn.AddChecked(u(spec.burnt))
			if err != nil {
				t.Fatalf("%s: fee decomposition overflow", spec.name)
			}
			tipPlusBurn = sum
		}
		if s.NetCharge != tipPlusBurn {
			t.Errorf("%s: net charge %s != tip %s + burnt %s", spec.name, s.NetCharge.Decimal(), spec.tipAmount, spec.burnt)
		}
		wantCredits := 1
		if spec.burnRecipient != nil {
			wantCredits = 2
		}
		if len(s.RecipientCredits) != wantCredits {
			t.Errorf("%s: %d summary credits, want %d", spec.name, len(s.RecipientCredits), wantCredits)
		}

		// Execution orders strictly increase within the transaction and the
		// actions keep both sides of every transfer.
		previous := uint64(0)
		for _, a := range tx.Actions {
			if a.ExecutionOrder <= previous {
				t.Errorf("%s: action orders do not strictly increase", spec.name)
			}
			previous = a.ExecutionOrder
			if a.Cause.Kind == CauseFrame && len(a.Effects) != 2 {
				t.Errorf("%s: transfer action %d does not keep both sides", spec.name, a.ActionIndex)
			}
		}
	}
}

func TestCorpusFixtureScopeTrees(t *testing.T) {
	for _, spec := range corpusSpecs() {
		tx, _, _ := buildCorpusTx(spec)
		scopes := tx.Scopes

		if len(scopes) < 2 || scopes[0].ScopeID != 0 || scopes[0].RolledBack {
			t.Fatalf("%s: scope 0 missing or rolled back", spec.name)
		}
		root := scopes[1]
		if root.ScopeID != 1 || root.ParentScopeID == nil || *root.ParentScopeID != 0 {
			t.Fatalf("%s: root execution scope malformed: %+v", spec.name, root)
		}

		switch spec.shape {
		case shapeTransfer:
			if root.RolledBack {
				t.Errorf("%s: successful transfer must not roll back the root scope", spec.name)
			}
			for _, a := range tx.Actions {
				if !a.EffectsCommitted {
					t.Errorf("%s: every action of a successful transfer must be committed", spec.name)
				}
			}
		case shapeCaughtRevert:
			if root.RolledBack {
				t.Errorf("%s: a caught child revert must not roll back the parent scope", spec.name)
			}
			if len(scopes) != 3 || !scopes[2].RolledBack || *scopes[2].ParentScopeID != 1 {
				t.Fatalf("%s: child scope tree malformed: %+v", spec.name, scopes)
			}
			var attempts int
			for _, a := range tx.Actions {
				if !a.EffectsCommitted {
					attempts++
					if a.RollbackScope == nil || *a.RollbackScope != 2 {
						t.Errorf("%s: the reverted attempt must keep its scope", spec.name)
					}
					if len(a.Effects) == 0 {
						t.Errorf("%s: the reverted attempt must keep its original points", spec.name)
					}
				}
			}
			if attempts != 1 {
				t.Errorf("%s: exactly one reverted attempt expected, got %d", spec.name, attempts)
			}
		case shapeRootRevert:
			if !root.RolledBack {
				t.Errorf("%s: the root execution scope must be rolled back", spec.name)
			}
			for _, a := range tx.Actions {
				if a.RollbackScope == nil {
					t.Fatalf("%s: action without a scope", spec.name)
				}
				switch *a.RollbackScope {
				case 0:
					// Fees and credits survive at the transaction boundary.
					if !a.EffectsCommitted {
						t.Errorf("%s: fee action at scope 0 must stay committed after a root revert", spec.name)
					}
				case 1:
					if a.EffectsCommitted {
						t.Errorf("%s: the reverted value transfer must not be committed", spec.name)
					}
				default:
					t.Errorf("%s: unexpected scope %d", spec.name, *a.RollbackScope)
				}
			}
		}
		if err := Validate(tx); err != nil {
			t.Errorf("%s: invalid record: %v", spec.name, err)
		}
	}
}

// TestCorpusFixtureFeesNeverAttributedToRolledBackScopes is the fixture form
// of AC-DAT-05-06's fee rule: after a root revert the surviving fee actions
// stay at scope 0 and nothing fee-like is attributed to the cancelled scope.
func TestCorpusFixtureFeesNeverAttributedToRolledBackScopes(t *testing.T) {
	for _, spec := range corpusSpecs() {
		if spec.shape != shapeRootRevert {
			continue
		}
		tx, before, after := buildCorpusTx(spec)
		if err := Validate(tx); err != nil {
			t.Fatalf("%s: %v", spec.name, err)
		}
		for _, a := range tx.Actions {
			if a.Cause.Kind == CauseTransactionProtocol && a.Cause.Operation.IsFeeOperation() {
				if a.RollbackScope == nil || *a.RollbackScope != 0 {
					t.Errorf("%s: fee action outside scope 0", spec.name)
				}
				if !a.EffectsCommitted {
					t.Errorf("%s: fee action not committed", spec.name)
				}
			}
		}
		// The tip recipient still gained the tip even though the root scope
		// was cancelled: the corpus after points prove it.
		if err := ReconcileBoundary(tx.Scopes, tx.Actions, before, after); err != nil {
			t.Errorf("%s: %v", spec.name, err)
		}
	}
}

// TestCorpusFixtureMutationStillRejected proves the corpus-derived fixtures
// are not vacuously green: a single corrupted reference or flag inside a
// real corpus shape is rejected by the same sentinels as the synthetic
// vectors.
func TestCorpusFixtureMutationStillRejected(t *testing.T) {
	spec := corpusSpecs()[2] // eth block 1 tx 2, the caught nested revert

	tx, before, after := buildCorpusTx(spec)
	tx.Scopes[1].RolledBack = true
	tx.Scopes[2].RolledBack = false
	if err := Validate(tx); !errors.Is(err, ErrRollbackNotPropagated) {
		t.Fatalf("unpropagated rollback accepted: %v", err)
	}

	tx, _, _ = buildCorpusTx(spec)
	tx.Actions[2].EffectsCommitted = true
	if err := Validate(tx); !errors.Is(err, ErrCommittedInRolledBackScope) {
		t.Fatalf("committed attempt inside the rolled back child scope accepted: %v", err)
	}
	if err := ReconcileBoundary(tx.Scopes, tx.Actions, before, after); !errors.Is(err, ErrCommittedInRolledBackScope) {
		t.Fatalf("reconciliation missed the committed attempt: %v", err)
	}

	tx, _, _ = buildCorpusTx(spec)
	tx.Actions[2].RollbackScope = nil
	if err := Validate(tx); !errors.Is(err, ErrRollbackScopeAbsentWithEffects) {
		t.Fatalf("absent scope accepted in place of the explicit child scope: %v", err)
	}

	tx, _, _ = buildCorpusTx(spec)
	inflated, err := tx.FeeSummary.GasPurchaseDebit.AddChecked(u("1"))
	if err != nil {
		t.Fatal(err)
	}
	tx.FeeSummary.GasPurchaseDebit = inflated
	if err := Validate(tx); !errors.Is(err, ErrSummaryMismatch) {
		t.Fatalf("inflated summary accepted: %v", err)
	}
}
