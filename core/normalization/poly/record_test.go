package poly

import (
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// TestCrossingBlockNormalizes is the end-to-end pipeline of the retained
// Madhugiri crossing evidence: a sprint-boundary block whose body is one
// legacy transfer and the final 0x7f StateSync transaction with a successful
// target call and a caught revert.
func TestCrossingBlockNormalizes(t *testing.T) {
	capture := crossingBlock(t)
	record, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("normalize crossing block: %v", err)
	}
	if len(record.Transactions) != 2 {
		t.Fatalf("got %d transactions, want 2", len(record.Transactions))
	}
	if record.Metadata.SourceTransactionCount != 2 {
		t.Fatalf("source count %d, want 2 (the 0x7f is a body transaction)", record.Metadata.SourceTransactionCount)
	}
	details, ok := record.Metadata.Details.Bor, true
	if !ok || details == nil {
		t.Fatalf("family details are not Bor")
	}
	if got := details.StateSyncMode.MustGet(); got != block.StateSyncBodyTransaction {
		t.Fatalf("state sync mode %s, want BODY_TRANSACTION", got)
	}
	if got := details.StateSyncCount.MustGet(); got != 2 {
		t.Fatalf("state sync count %d, want 2", got)
	}
	if got := details.SprintLength.MustGet(); got != 2 {
		t.Fatalf("sprint length %d, want 2", got)
	}
	if !details.SprintEnd.MustGet() {
		t.Fatalf("block 2 is the sprint boundary of sprint 2")
	}
	if len(record.Finality.HeimdallCheckpoints) != 1 {
		t.Fatalf("finality marker references %d, want 1", len(record.Finality.HeimdallCheckpoints))
	}

	legacy := record.Transactions[0]
	if legacy.NativeType != 0 || legacy.RawEncoding.Kind != RawEncodingEvmLegacyRLP {
		t.Fatalf("legacy raw encoding kind %s", legacy.RawEncoding.Kind)
	}
	if legacy.Receipt == nil || legacy.Receipt.FeeSummary == nil {
		t.Fatalf("legacy receipt missing")
	}
	summary := legacy.Receipt.FeeSummary
	if summary.NetCharge.Decimal() != "105000000000000" {
		t.Fatalf("net charge %s", summary.NetCharge.Decimal())
	}
	if len(summary.RecipientCredits) != 2 {
		t.Fatalf("recipient credits %d, want tip and burn", len(summary.RecipientCredits))
	}
	if summary.RecipientCredits[0].Address != fixtureCoinbase {
		t.Fatalf("first credit is not the tip")
	}
	if summary.RecipientCredits[1].Address != BurnAddress {
		t.Fatalf("second credit is not the base-fee burn")
	}
	// The D74 order: purchase(1), root frame(2), transfer(3), tip(4),
	// burn(5).
	if legacy.Calls[0].ExecutionOrder != 2 {
		t.Fatalf("root frame order %d, want 2", legacy.Calls[0].ExecutionOrder)
	}
	for i, want := range []uint64{1, 3, 4, 5} {
		if legacy.Actions[i].ExecutionOrder != want {
			t.Fatalf("action %d order %d, want %d", i, legacy.Actions[i].ExecutionOrder, want)
		}
	}
	if err := legacy.ReconcileBoundary(); err != nil {
		t.Fatalf("legacy boundary reconcile: %v", err)
	}
	if legacy.Authorization.Applicable() {
		t.Fatalf("authorization must be a closed empty profile")
	}

	sync := record.Transactions[1]
	if sync.NativeType != StateSyncTxType || sync.RawEncoding.Kind != RawEncodingBorStateSyncEnvelope {
		t.Fatalf("state sync raw encoding kind %s", sync.RawEncoding.Kind)
	}
	if sync.Projection.Evm != nil || sync.Projection.NativeProtocol == nil {
		t.Fatalf("state sync projection is not native protocol execution")
	}
	if got := sync.Projection.NativeProtocol.RecordCount; got != 2 {
		t.Fatalf("record count %d, want 2", got)
	}
	if sync.StateSync == nil || len(sync.StateSync.Items) != 2 {
		t.Fatalf("state sync details missing")
	}
	// The five frames of the native observation: synthetic root, commit,
	// target, commit, target(revert).
	if len(sync.Calls) != 5 {
		t.Fatalf("got %d calls, want 5", len(sync.Calls))
	}
	wantDepths := []uint64{0, 1, 2, 1, 2}
	for i, want := range wantDepths {
		if sync.Calls[i].Depth != want {
			t.Fatalf("call %d depth %d, want %d", i, sync.Calls[i].Depth, want)
		}
	}
	if sync.Calls[0].Kind != FrameNativeProtocol || *sync.Calls[0].CallerAddress != SystemCallerAddress {
		t.Fatalf("synthetic root frame shape differs")
	}
	if sync.Calls[4].Outcome != OutcomeRevert || sync.Calls[4].EffectsCommitted {
		t.Fatalf("the caught revert must stay uncommitted source data")
	}
	if sync.Calls[3].Outcome != OutcomeSuccess || !sync.Calls[3].EffectsCommitted {
		t.Fatalf("the second commit succeeds even though its target reverted")
	}
	// Zero-fee system transaction: applicable zeros, absent payer.
	if sync.Receipt.FeeSummary.Payer != nil || !sync.Receipt.FeeSummary.NetCharge.IsZero() {
		t.Fatalf("state sync fee summary must be explicit zeros without a payer")
	}
	if len(sync.Receipt.NetworkDetails.SourceLogScopes) != 1 ||
		sync.Receipt.NetworkDetails.SourceLogScopes[0].Scope != LogScopeTXStateSync {
		t.Fatalf("source log scopes differ")
	}
	if err := sync.ReconcileBoundary(); err != nil {
		t.Fatalf("state sync boundary reconcile: %v", err)
	}
}

// TestLegacyCorpusBlockNormalizes is the pre-Madhugiri carrier: an empty
// body whose state-sync work is the separate Bor receipt, enabled through
// the optional scope with declared fixtures.
func TestLegacyCorpusBlockNormalizes(t *testing.T) {
	capture := legacyCorpusBlock(t)
	opts := Options{LegacyReceipt: true, DeclaredFixtures: []string{"POLY-L01"}}
	record, err := NormalizeBlock(&capture, opts)
	if err != nil {
		t.Fatalf("normalize corpus block: %v", err)
	}
	if record.Metadata.SourceTransactionCount != 0 {
		t.Fatalf("empty body must stay count 0, got %d", record.Metadata.SourceTransactionCount)
	}
	details := record.Metadata.Details.Bor
	if got := details.StateSyncMode.MustGet(); got != block.StateSyncEndOfBlockCommit {
		t.Fatalf("state sync mode %s, want END_OF_BLOCK_COMMIT", got)
	}
	if record.LegacyReceipt == nil {
		t.Fatalf("legacy record missing")
	}
	if record.LegacyReceipt.SyntheticReceiptIndex != 0 {
		t.Fatalf("synthetic index %d, want the ordinary receipt count 0", record.LegacyReceipt.SyntheticReceiptIndex)
	}
	if record.LegacyReceipt.SourceReceiptHash != DeriveBorReceiptHash(4, fixtureBlockHash) {
		t.Fatalf("legacy identity differs from the derived formula")
	}
	if record.LegacyReceipt.Coverage != LegacyCoverage {
		t.Fatalf("coverage %s", record.LegacyReceipt.Coverage)
	}
}

// TestEmptyBlockIsFirstClass checks the empty-block case: count 0, no
// transactions, valid metadata.
func TestEmptyBlockIsFirstClass(t *testing.T) {
	capture := crossingBlock(t)
	capture.Transactions = nil
	capture.StateSyncRows = 0
	capture.Header.GasUsed = 0
	record, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("normalize empty block: %v", err)
	}
	if record.Metadata.SourceTransactionCount != 0 || len(record.Transactions) != 0 {
		t.Fatalf("empty block must have zero transactions")
	}
	if got := record.Metadata.Details.Bor.StateSyncCount.MustGet(); got != 0 {
		t.Fatalf("state sync count must be the observed zero, got %d", got)
	}
}

// TestNormalizeTransactionRejectsUnknownHash checks the identity binding:
// an observed hash that does not match the wire is an error, not a re-hash.
func TestNormalizeTransactionRejectsUnknownHash(t *testing.T) {
	tx := crossingStateSyncTx(t)
	tx.ObservedTxHash = Hash32{}
	if _, err := NormalizeTransaction(&tx); err == nil {
		t.Fatalf("mismatched observed hash accepted")
	}
}

// TestNormalizeTransactionRejectsUnknownType checks the closed type
// registry: type 3 has no native applicability proof in this profile.
func TestNormalizeTransactionRejectsUnknownType(t *testing.T) {
	tx := crossingLegacyTx(t)
	tx.Receipt.Consensus = fixtureReceiptWire(t, nil, 21000, fixtureBloom(nil), nil)
	tx.NativeType = 3
	tx.Wire = []byte{0x03, 0x01}
	tx.ObservedTxHash = Keccak256(tx.Wire)
	if _, err := NormalizeTransaction(&tx); err == nil {
		t.Fatalf("type 3 accepted without an applicability proof")
	}
}

// TestFeeActionShapes checks the D62 shapes of the fee actions of the
// crossing legacy transaction through the closed protocol validator.
func TestFeeActionShapes(t *testing.T) {
	tx := crossingLegacyTx(t)
	tx.Receipt.Consensus = fixtureReceiptWire(t, nil, 21000, fixtureBloom(nil), nil)
	record, err := NormalizeTransaction(&tx)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	var kinds []balance.ActionKind
	var ops []balance.ProtocolOperation
	var indexes []uint64
	for _, a := range record.Actions {
		kinds = append(kinds, a.Kind)
		if a.Cause.Kind == balance.CauseTransactionProtocol {
			ops = append(ops, a.Cause.Operation)
			indexes = append(indexes, a.Cause.OperationIndex)
		}
	}
	wantKinds := []balance.ActionKind{balance.KindFee, balance.KindTransfer, balance.KindFee, balance.KindFee}
	for i := range wantKinds {
		if kinds[i] != wantKinds[i] {
			t.Fatalf("action %d kind %s, want %s", i, kinds[i], wantKinds[i])
		}
	}
	wantOps := []balance.ProtocolOperation{
		balance.OpGasPurchase, balance.OpFeeRecipientCredit, balance.OpFeeRecipientCredit,
	}
	for i := range wantOps {
		if ops[i] != wantOps[i] {
			t.Fatalf("protocol op %d is %s, want %s", i, ops[i], wantOps[i])
		}
		if indexes[i] != uint64(i) {
			t.Fatalf("operation index %d is %d, want %d", i, indexes[i], i)
		}
	}
	// The value transfer lives inside the root execution scope, the fees at
	// the boundary scope 0.
	if *record.Actions[0].RollbackScope != 0 {
		t.Fatalf("gas purchase must live at the boundary scope")
	}
	if *record.Actions[1].RollbackScope != 1 {
		t.Fatalf("the transfer must live inside the root execution scope")
	}
}
