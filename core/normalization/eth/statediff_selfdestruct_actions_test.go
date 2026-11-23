package eth

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

func TestBuildStateDiffCorpusStorageChanges(t *testing.T) {
	// The parent contract of transaction (1,2): slots 0, 1, 2 and 3 change
	// from zero to 1, 1, 0x20 and 0xee (expected/eth.json).
	empty := map[Hash32]Hash32{slotHash(0): {}, slotHash(1): {}, slotHash(2): {}, slotHash(3): {}}
	after := map[Hash32]Hash32{slotHash(0): valueHash(1), slotHash(1): valueHash(1), slotHash(2): valueHash(0x20), slotHash(3): valueHash(0xee)}
	codeHash := codeHashOf(corpusParentCode)
	diff, err := BuildStateDiff([]AccountObservation{{
		Address: corpusParent,
		Before:  AccountPoint{Exists: true, CodeHash: codeHash, Storage: empty},
		After:   AccountPoint{Exists: true, Balance: u256("5"), CodeHash: codeHash, Storage: after},
	}})
	if err != nil {
		t.Fatal(err)
	}
	change, ok := diff.Find(corpusParent)
	if !ok {
		t.Fatal("the parent account must appear in the diff")
	}
	if len(change.StorageChanges) != 4 {
		t.Fatalf("storage changes = %d, want 4", len(change.StorageChanges))
	}
	// Sorted by raw key bytes: slots 0..3 in order.
	wantValues := []Hash32{valueHash(1), valueHash(1), valueHash(0x20), valueHash(0xee)}
	for i, sc := range change.StorageChanges {
		if sc.Key != slotHash(byte(i)) || sc.After != wantValues[i] || sc.Before != (Hash32{}) {
			t.Errorf("storage change %d = %+v", i, sc)
		}
	}
	if change.CodeBefore != nil || change.CodeAfter != nil {
		t.Error("an unchanged code is not repeated as bytes")
	}
}

func TestBuildStateDiffDropsUnchangedAccounts(t *testing.T) {
	// The child contract of transaction (1,2): its 5 wei transfer and its
	// slot-0 store were both inside the reverted call; the boundary points
	// are identical, so the diff has no row.
	codeHash := codeHashOf(corpusChildCode)
	diff, err := BuildStateDiff([]AccountObservation{{
		Address: corpusChild,
		Before:  AccountPoint{Exists: true, CodeHash: codeHash, Storage: map[Hash32]Hash32{slotHash(0): {}}},
		After:   AccountPoint{Exists: true, CodeHash: codeHash, Storage: map[Hash32]Hash32{slotHash(0): {}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Accounts) != 0 {
		t.Fatalf("accounts = %d, want 0 for identical boundary points", len(diff.Accounts))
	}
	// The 100 -> 110 -> 100 balance tour leaves no row either.
	diff, err = BuildStateDiff([]AccountObservation{{
		Address: corpusRecipient,
		Before:  AccountPoint{Exists: true, Balance: u256("100")},
		After:   AccountPoint{Exists: true, Balance: u256("100")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Accounts) != 0 {
		t.Fatal("a net-zero balance tour leaves no diff row; the movements stay in the actions")
	}
}

func TestBuildStateDiffExistenceCodeAndReset(t *testing.T) {
	// Creation: an absent account gains code.
	diff, err := BuildStateDiff([]AccountObservation{{
		Address: corpusChild,
		Before:  AccountPoint{Exists: false, CodeHash: fixtureCodeHash(nil), Code: []byte{}},
		After:   AccountPoint{Exists: true, CodeHash: codeHashOf(corpusChildCode), Code: decodeHexOrEmpty(corpusChildCode)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	change := diff.Accounts[0]
	if change.ExistedBefore || !change.ExistsAfter {
		t.Fatalf("existence flags = %v -> %v", change.ExistedBefore, change.ExistsAfter)
	}
	if string(change.CodeBefore) != "" || string(change.CodeAfter) != string(decodeHexOrEmpty(corpusChildCode)) {
		t.Fatalf("code bytes must be present exactly on a code change")
	}
	// Deletion: storage reset without enumeration, account gone after.
	diff, err = BuildStateDiff([]AccountObservation{{
		Address: corpusParent,
		Before: AccountPoint{Exists: true, Balance: u256("5"), CodeHash: codeHashOf(corpusParentCode),
			Code:    decodeHexOrEmpty(corpusParentCode),
			Storage: map[Hash32]Hash32{slotHash(0): valueHash(1)}},
		After:        AccountPoint{Exists: false, CodeHash: fixtureCodeHash(nil), Code: []byte{}},
		StorageReset: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	change = diff.Accounts[0]
	if !change.StorageReset || change.ExistsAfter {
		t.Fatalf("reset = %v exists after = %v", change.StorageReset, change.ExistsAfter)
	}
	if len(change.StorageChanges) != 0 {
		t.Fatal("a deleted account carries no per-key storage rows")
	}
	// Reset with a subsequent explicit write.
	diff, err = BuildStateDiff([]AccountObservation{{
		Address: corpusParent,
		Before: AccountPoint{Exists: true, CodeHash: codeHashOf(corpusParentCode),
			Storage: map[Hash32]Hash32{slotHash(0): valueHash(1), slotHash(5): valueHash(9)}},
		After:        AccountPoint{Exists: true, CodeHash: codeHashOf(corpusParentCode), Storage: map[Hash32]Hash32{slotHash(7): valueHash(2)}},
		StorageReset: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	change = diff.Accounts[0]
	if !change.StorageReset {
		t.Fatal("the reset marker must survive")
	}
	// Only the surviving write is enumerated; the reset covers the rest.
	if len(change.StorageChanges) != 1 || change.StorageChanges[0].Key != slotHash(7) {
		t.Fatalf("storage changes = %+v, want only the write after the reset", change.StorageChanges)
	}
}

func TestBuildStateDiffValidation(t *testing.T) {
	if _, err := BuildStateDiff([]AccountObservation{
		{Address: corpusRecipient, After: AccountPoint{Exists: true, Balance: u256("1")}},
		{Address: corpusRecipient, After: AccountPoint{Exists: true, Balance: u256("2")}},
	}); !errors.Is(err, ErrStateDiffShape) {
		t.Fatalf("duplicate address error = %v", err)
	}
	// Code bytes without a code change are rejected.
	diff := StateDiff{Accounts: []AccountChange{{
		Address: corpusRecipient, ExistsAfter: true, CodeBefore: []byte{1},
	}}}
	if err := diff.Validate(); !errors.Is(err, ErrStateDiffShape) {
		t.Fatalf("code presence error = %v", err)
	}
	// Unsorted storage keys are rejected.
	diff = StateDiff{Accounts: []AccountChange{{
		Address: corpusRecipient, ExistsAfter: true,
		StorageChanges: []StorageChange{
			{Key: slotHash(3), Before: Hash32{}, After: valueHash(1)},
			{Key: slotHash(1), Before: Hash32{}, After: valueHash(1)},
		},
	}}}
	if err := diff.Validate(); !errors.Is(err, ErrStateDiffShape) {
		t.Fatalf("unsorted keys error = %v", err)
	}
}

func TestBoundaryBalancesAndCodeOf(t *testing.T) {
	diff, err := BuildStateDiff([]AccountObservation{
		accountObservation(corpusRecipient, false, "0", 0, "", true, "1000000000000000000", 0, ""),
		accountObservation(corpusSender, true, "10", 0, "", true, "9", 1, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	before, after := diff.BoundaryBalances()
	if before[corpusSender].Decimal() != "10" || after[corpusSender].Decimal() != "9" {
		t.Fatalf("boundary balances wrong: %s -> %s", before[corpusSender].Decimal(), after[corpusSender].Decimal())
	}
	if _, ok := diff.CodeOf(corpusRecipient); !ok {
		t.Fatal("an existing account without a code change has no code view")
	}
}

func selfDestructObservation(contract, beneficiary Address20, balance string, effects []balance.BalanceEffect) SelfDestructObservation {
	return SelfDestructObservation{
		FrameID:          0,
		Contract:         contract,
		Beneficiary:      beneficiary,
		BalanceAtEntry:   u256(balance),
		DeletionEligible: true,
		Scope:            1,
		AccountDeleted:   true,
		MovementPosition: MovementAtOpcode,
		MovementEffects:  effects,
	}
}

func TestSelfDestructKeepsZeroBalanceAction(t *testing.T) {
	order := NewOrder()
	// Frame entry first.
	if _, err := order.Allocate(); err != nil {
		t.Fatal(err)
	}
	obs := selfDestructObservation(corpusParent, corpusRecipient, "0", nil)
	exec, proj, err := BuildSelfDestruct(order, &ActionIndex{}, 1, obs)
	if err != nil {
		t.Fatal(err)
	}
	if exec.Payload.SelfDestruct == nil {
		t.Fatal("the opcode action must exist even at balance zero")
	}
	if proj == nil {
		t.Fatal("the monetary projection must stay linked")
	}
	if !proj.Amount.IsZero() || len(proj.Effects) != 0 {
		t.Fatalf("projection = %s with %d effects, want zero without effects", proj.Amount.Decimal(), len(proj.Effects))
	}
	if proj.EffectsCommitted {
		t.Fatal("a projection without effects never claims committed effects")
	}
	if !exec.Payload.SelfDestruct.EffectsCommitted {
		t.Error("a surviving selfdestruct in a live scope keeps its opcode committed")
	}
}

func TestSelfDestructOpcodePositionMovementSharesOrderViaCause(t *testing.T) {
	order := NewOrder()
	_, _ = order.Allocate() // frame entry
	effects := []balance.BalanceEffect{
		{EffectIndex: 0, Address: corpusParent, Before: u256("7"), After: u256("0")},
		{EffectIndex: 1, Address: corpusRecipient, Before: u256("1"), After: u256("8")},
	}
	obs := selfDestructObservation(corpusParent, corpusRecipient, "7", effects)
	exec, proj, err := BuildSelfDestruct(order, &ActionIndex{}, 1, obs)
	if err != nil {
		t.Fatal(err)
	}
	if exec.ExecutionOrder != proj.ExecutionOrder {
		t.Fatalf("orders %d and %d differ: the execution action and its projection share the number",
			exec.ExecutionOrder, proj.ExecutionOrder)
	}
	if proj.Cause.Kind != balance.CauseExecutionAction || proj.Cause.ExecutionActionID != exec.ExecutionActionID {
		t.Fatalf("cause = %s, want ExecutionAction(%d)", proj.Cause, exec.ExecutionActionID)
	}
	if proj.Amount.Decimal() != "7" || proj.Kind != balance.KindSelfDestructTransfer {
		t.Fatalf("projection = %s of kind %s", proj.Amount.Decimal(), proj.Kind)
	}
	if !exec.Payload.SelfDestruct.DeletionCommitted {
		t.Error("an eligible surviving deletion with a deleted account commits")
	}
}

func TestSelfDestructRevertCancelsEffectsAndDeletion(t *testing.T) {
	order := NewOrder()
	effects := []balance.BalanceEffect{
		{EffectIndex: 0, Address: corpusParent, Before: u256("7"), After: u256("0")},
		{EffectIndex: 1, Address: corpusRecipient, Before: u256("0"), After: u256("7")},
	}
	obs := selfDestructObservation(corpusParent, corpusRecipient, "7", effects)
	obs.RolledBack = true
	obs.AccountDeleted = false
	exec, proj, err := BuildSelfDestruct(order, &ActionIndex{}, 1, obs)
	if err != nil {
		t.Fatal(err)
	}
	if exec.Payload.SelfDestruct.EffectsCommitted || exec.Payload.SelfDestruct.DeletionCommitted {
		t.Fatal("a cancelled selfdestruct commits neither effects nor deletion")
	}
	if proj.EffectsCommitted {
		t.Fatal("the rolled back projection keeps its points uncommitted")
	}
	if len(proj.Effects) != 2 {
		t.Fatal("the cancelled attempt keeps its original effects")
	}
}

func TestSelfDestructForkRulesSeparateEligibilityFromOutcome(t *testing.T) {
	order := NewOrder()
	effects := []balance.BalanceEffect{
		{EffectIndex: 0, Address: corpusParent, Before: u256("4"), After: u256("0")},
		{EffectIndex: 1, Address: corpusRecipient, Before: u256("0"), After: u256("4")},
	}
	// Post-Cancun without the same-tx creation: not eligible, no
	// deletion, but the funds still move at the opcode position in the
	// pre-Cancun shape (EIP-6780 keeps transfers; only deletion is
	// fork-gated).
	obs := selfDestructObservation(corpusParent, corpusRecipient, "4", effects)
	obs.DeletionEligible = false
	obs.AccountDeleted = false
	exec, _, err := BuildSelfDestruct(order, &ActionIndex{}, 1, obs)
	if err != nil {
		t.Fatal(err)
	}
	if exec.Payload.SelfDestruct.DeletionEligibleByRules {
		t.Error("eligibility follows the fork rules input")
	}
	if exec.Payload.SelfDestruct.DeletionCommitted {
		t.Error("an ineligible selfdestruct never commits deletion")
	}
	// Eligible but the account survived (final existence is an
	// AccountChange fact, not an opcode fact).
	obs.DeletionEligible = true
	obs.AccountDeleted = false
	exec, _, err = BuildSelfDestruct(order, &ActionIndex{}, 1, obs)
	if err != nil {
		t.Fatal(err)
	}
	if exec.Payload.SelfDestruct.DeletionCommitted {
		t.Error("deletion commits only with the observed final deletion")
	}
}

func TestSelfDestructSelfBeneficiaryNoMovementPostCancun(t *testing.T) {
	order := NewOrder()
	obs := selfDestructObservation(corpusParent, corpusParent, "9", nil)
	obs.DeletionEligible = false
	obs.AccountDeleted = false
	exec, proj, err := BuildSelfDestruct(order, &ActionIndex{}, 1, obs)
	if err != nil {
		t.Fatal(err)
	}
	if exec.Payload.SelfDestruct == nil {
		t.Fatal("the opcode action stays")
	}
	if proj == nil || !proj.Amount.IsZero() {
		t.Fatal("the self-beneficiary no-op keeps a zero projection, not an invented transfer")
	}
	if exec.Payload.SelfDestruct.DeletionCommitted {
		t.Error("no deletion without eligibility")
	}
}

func TestSelfDestructInconsistentMovementRejected(t *testing.T) {
	order := NewOrder()
	obs := selfDestructObservation(corpusParent, corpusRecipient, "7", nil)
	if _, _, err := BuildSelfDestruct(order, &ActionIndex{}, 1, obs); !errors.Is(err, ErrSelfDestructShape) {
		t.Fatalf("nonzero balance moved nowhere error = %v", err)
	}
	credit := []balance.BalanceEffect{{EffectIndex: 0, Address: corpusRecipient, Before: u256("0"), After: u256("7")}}
	obs = selfDestructObservation(corpusParent, corpusRecipient, "7", credit)
	if _, _, err := BuildSelfDestruct(order, &ActionIndex{}, 1, obs); !errors.Is(err, ErrSelfDestructShape) {
		t.Fatalf("movement without a debit error = %v", err)
	}
}

func TestFinalizationBurnSeparateOrderAndCause(t *testing.T) {
	order := NewOrder()
	frameEntry, _ := order.Allocate()
	opcodeOrder, _ := order.Allocate()
	if frameEntry == opcodeOrder {
		t.Fatal("setup")
	}
	burnEffects := []balance.BalanceEffect{
		{EffectIndex: 0, Address: corpusParent, Before: u256("3"), After: u256("0")},
	}
	obs := selfDestructObservation(corpusParent, corpusParent, "3", burnEffects)
	obs.MovementPosition = MovementAtFinalization
	obs.AccountDeleted = true
	// The opcode-position builder refuses a finalization movement.
	if _, _, err := BuildSelfDestruct(order, &ActionIndex{}, 1, obs); !errors.Is(err, ErrSelfDestructShape) {
		t.Fatalf("finalization position on the opcode builder error = %v", err)
	}
	burn, err := BuildFinalizationBurn(order, &ActionIndex{}, &ProtocolIndex{}, 1, obs)
	if err != nil {
		t.Fatal(err)
	}
	if burn.ExecutionOrder == opcodeOrder {
		t.Fatal("the finalization burn has its own order at its actual position, not the opcode's")
	}
	if burn.Cause.Operation != balance.OpSelfDestructFinalizationBurn || burn.Cause.RelatedExecutionActions[0] != 1 {
		t.Fatalf("cause = %s, want the burn operation linked to execution action 1", burn.Cause)
	}
	if burn.Kind != balance.KindBurn {
		t.Fatalf("kind = %s, want BURN", burn.Kind)
	}
	// A finalization credit to a surviving beneficiary has no v1
	// operation: fail closed.
	credit := []balance.BalanceEffect{
		{EffectIndex: 0, Address: corpusParent, Before: u256("3"), After: u256("0")},
		{EffectIndex: 1, Address: corpusRecipient, Before: u256("0"), After: u256("3")},
	}
	obs.MovementEffects = credit
	obs.AccountDeleted = false
	obs.Beneficiary = corpusRecipient
	if _, err := BuildFinalizationBurn(order, &ActionIndex{}, &ProtocolIndex{}, 1, obs); !errors.Is(err, ErrSelfDestructRegistry) {
		t.Fatalf("surviving beneficiary credit error = %v", err)
	}
}

func TestFeeBuildersCorpusTransfer(t *testing.T) {
	order := NewOrder()
	actions := &ActionIndex{}
	protocols := &ProtocolIndex{}
	fee := FeeObservation{
		Payer: corpusSender, GasLimit: 21000, GasUsed: 21000, EffectiveGasPrice: gwei(2),
		Recipient: corpusCoinbase, BaseFee: u256("875000000"),
		PayerBalanceBefore:     u256("10000000000000000000"),
		RecipientBalanceBefore: u256("0"),
	}
	purchase, err := BuildGasPurchaseAction(order, actions, protocols, fee)
	if err != nil {
		t.Fatal(err)
	}
	if purchase.Amount.Decimal() != "42000000000000" {
		t.Fatalf("purchase = %s, want 42000000000000 (expected/eth.json fee.total)", purchase.Amount.Decimal())
	}
	if purchase.Effects[0].After.Decimal() != "9999958000000000000" {
		t.Fatalf("purchase after point = %s", purchase.Effects[0].After.Decimal())
	}
	// All gas used: no return action.
	ret, err := BuildGasReturnAction(order, actions, protocols, fee)
	if err != nil {
		t.Fatal(err)
	}
	if ret != nil {
		t.Fatal("a zero leftover produces no gas return action")
	}
	credit, err := BuildFeeCreditAction(order, actions, protocols, fee)
	if err != nil {
		t.Fatal(err)
	}
	if credit.Amount.Decimal() != "23625000000000" {
		t.Fatalf("tip = %s, want 23625000000000 (expected/eth.json fee.tip)", credit.Amount.Decimal())
	}
	if credit.Effects[0].After.Decimal() != "23625000000000" {
		t.Fatalf("coinbase after = %s, want 23625000000000", credit.Effects[0].After.Decimal())
	}
	summary, err := FeeSummaryOf([]balance.BalanceAction{purchase, credit})
	if err != nil {
		t.Fatal(err)
	}
	if summary.GasPurchaseDebit.Decimal() != "42000000000000" || summary.GasReturnCredit.Decimal() != "0" {
		t.Fatalf("summary purchase/return = %s/%s", summary.GasPurchaseDebit.Decimal(), summary.GasReturnCredit.Decimal())
	}
	if summary.NetCharge.Decimal() != "42000000000000" {
		t.Fatalf("net charge = %s, want 42000000000000", summary.NetCharge.Decimal())
	}
	if len(summary.RecipientCredits) != 1 || summary.RecipientCredits[0].Amount.Decimal() != "23625000000000" {
		t.Fatalf("recipient credits = %+v", summary.RecipientCredits)
	}
	if summary.Payer == nil || *summary.Payer != corpusSender {
		t.Fatal("the summary payer is the purchase payer")
	}
}

func TestFeeBuildersCorpusRevertWithReturn(t *testing.T) {
	order := NewOrder()
	actions := &ActionIndex{}
	protocols := &ProtocolIndex{}
	fee := FeeObservation{
		Payer: corpusSender, GasLimit: 200000, GasUsed: 142655, EffectiveGasPrice: gwei(2),
		Recipient: corpusCoinbase, BaseFee: u256("875000000"),
		PayerBalanceBefore:     u256("8999911799000000000"),
		PayerBalanceAtReturn:   Some(u256("8999511798999999995")),
		RecipientBalanceBefore: u256("51450000000000"),
	}
	purchase, err := BuildGasPurchaseAction(order, actions, protocols, fee)
	if err != nil {
		t.Fatal(err)
	}
	if purchase.Amount.Decimal() != "400000000000000" {
		t.Fatalf("purchase = %s, want 400000000000000", purchase.Amount.Decimal())
	}
	ret, err := BuildGasReturnAction(order, actions, protocols, fee)
	if err != nil {
		t.Fatal(err)
	}
	if ret == nil || ret.Amount.Decimal() != "114690000000000" {
		t.Fatalf("return = %v, want 114690000000000", ret)
	}
	credit, err := BuildFeeCreditAction(order, actions, protocols, fee)
	if err != nil {
		t.Fatal(err)
	}
	if credit.Amount.Decimal() != "160486875000000" {
		t.Fatalf("tip = %s, want 160486875000000 (expected/eth.json fee.tip of (1,2))", credit.Amount.Decimal())
	}
	summary, err := FeeSummaryOf([]balance.BalanceAction{purchase, *ret, credit})
	if err != nil {
		t.Fatal(err)
	}
	// The fee total of the oracle (285310000000000) is purchase minus
	// return: the summary is the same aggregate, not an extra event.
	if summary.NetCharge.Decimal() != "285310000000000" {
		t.Fatalf("net charge = %s, want 285310000000000", summary.NetCharge.Decimal())
	}
	// A nonzero return without the observed point is rejected.
	fee.PayerBalanceAtReturn = None[UInt256]()
	if _, err := BuildGasReturnAction(NewOrder(), &ActionIndex{}, &ProtocolIndex{}, fee); !errors.Is(err, ErrActionShape) {
		t.Fatalf("missing return point error = %v", err)
	}
}

func TestFeeBuildersBlobChargeIncludedInPurchase(t *testing.T) {
	order := NewOrder()
	fee := FeeObservation{
		Payer: corpusSender, GasLimit: 150000, GasUsed: 100000, EffectiveGasPrice: gwei(2),
		Recipient: corpusCoinbase, BaseFee: gwei(1),
		BlobGasUsed:            Some(uint64(131072)),
		BlobGasPrice:           Some(gwei(1)),
		PayerBalanceBefore:     u256("100000000000000000000"),
		RecipientBalanceBefore: u256("0"),
	}
	purchase, err := BuildGasPurchaseAction(order, &ActionIndex{}, &ProtocolIndex{}, fee)
	if err != nil {
		t.Fatal(err)
	}
	// gas 150000 * 2 gwei + blob 131072 * 1 gwei.
	if purchase.Amount.Decimal() != "431072000000000" {
		t.Fatalf("purchase = %s, want 431072000000000 (gas plus blob charge)", purchase.Amount.Decimal())
	}
	// Blob fields must pair up.
	fee.BlobGasPrice = None[UInt256]()
	if _, err := BuildGasPurchaseAction(NewOrder(), &ActionIndex{}, &ProtocolIndex{}, fee); !errors.Is(err, ErrActionShape) {
		t.Fatalf("blob gas without price error = %v", err)
	}
}

func TestNetZeroTransfersDoNotDisappear(t *testing.T) {
	order := NewOrder()
	if _, err := order.Allocate(); err != nil {
		t.Fatal(err) // frame entry order 1
	}
	actions := &ActionIndex{}
	// The +10/-10 self-transfer: both effects on one address, net zero.
	obs := TransferObservation{
		FrameID: 0, From: corpusSender, To: corpusSender, Amount: u256("10"),
		Scope: 1, Committed: true,
		Effects: []balance.BalanceEffect{
			{EffectIndex: 0, Address: corpusSender, Before: u256("100"), After: u256("90")},
			{EffectIndex: 1, Address: corpusSender, Before: u256("90"), After: u256("100")},
		},
	}
	a, err := BuildTransferAction(order, actions, obs)
	if err != nil {
		t.Fatal(err)
	}
	if a.Amount.Decimal() != "10" {
		t.Fatalf("amount = %s", a.Amount.Decimal())
	}
	if err := balance.Validate(balance.TxBalances{
		Scopes:  []balance.RollbackScope{balance.TxBoundaryScope(), {ScopeID: 1, ParentScopeID: uint64Ptr(0)}},
		Actions: []balance.BalanceAction{a},
		Frames:  []balance.FrameRef{{FrameID: 0, ExecutionOrder: 1}},
	}); err != nil {
		t.Fatalf("the net-zero self-transfer must validate: %v", err)
	}
}
