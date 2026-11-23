package eth

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

func TestAssembleTxCorpusTransfer(t *testing.T) {
	record, err := AssembleTx(corpusTransferTx())
	if err != nil {
		t.Fatal(err)
	}
	// The shared counter: purchase 1, root frame entry 2, transfer 3,
	// fee credit 4 (no return: all gas used).
	if record.Calls[0].ExecutionOrder != 2 {
		t.Errorf("root frame order = %d, want 2", record.Calls[0].ExecutionOrder)
	}
	orders := map[uint64]balance.ActionKind{}
	for _, a := range record.Actions {
		orders[a.ExecutionOrder] = a.Kind
	}
	if orders[1] != balance.KindFee || orders[3] != balance.KindTransfer || orders[4] != balance.KindFee {
		t.Fatalf("orders = %v", orders)
	}
	if len(record.RollbackScope) != 2 || record.RollbackScope[1].ScopeID != 1 {
		t.Fatalf("scopes = %+v", record.RollbackScope)
	}
	summary := record.Receipt.FeeSummary
	if summary.NetCharge.Decimal() != "42000000000000" || len(summary.RecipientCredits) != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	// The boundary state: sender nonce 0 -> 1, recipient created with
	// 1 ETH, coinbase credited the tip.
	change, ok := record.StateChanges.Find(corpusSender)
	if !ok || change.NonceBefore != 0 || change.NonceAfter != 1 {
		t.Fatalf("sender change = %+v", change)
	}
	if change, ok = record.StateChanges.Find(corpusRecipient); !ok || change.ExistedBefore || !change.ExistsAfter {
		t.Fatalf("recipient change = %+v", change)
	}
	if change.BalanceAfter.Decimal() != "1000000000000000000" {
		t.Fatalf("recipient balance after = %s", change.BalanceAfter.Decimal())
	}
	// The retained source encoding verifies against the typed receipt.
	if err := VerifySourceEncoding(sourceOf(record.Receipt), record.Receipt); err != nil {
		t.Fatal(err)
	}
	if err := record.Reconcile(); err != nil {
		t.Fatalf("the committed effects must reconcile with the boundary: %v", err)
	}
}

func TestAssembleTxCorpusNestedRevert(t *testing.T) {
	record, err := AssembleTx(corpusNestedRevertTx())
	if err != nil {
		t.Fatal(err)
	}
	// Scope 2 (the child call) is rolled back; the root scope survives.
	if !RolledBack(record.RollbackScope, 2) || RolledBack(record.RollbackScope, 1) || RolledBack(record.RollbackScope, 0) {
		t.Fatalf("scopes = %+v", record.RollbackScope)
	}
	// The child frame keeps its local REVERT outcome and stays
	// uncommitted; the root stays committed.
	if record.Calls[1].Outcome != ResultRevert || record.Calls[1].EffectsCommitted {
		t.Fatalf("child frame = %+v", record.Calls[1])
	}
	if !record.Calls[0].EffectsCommitted {
		t.Fatalf("root frame = %+v", record.Calls[0])
	}
	// The child's 5 wei transfer keeps its points uncommitted; the root
	// transfer stays committed.
	var rootTransfer, childTransfer *balance.BalanceAction
	for i := range record.Actions {
		a := &record.Actions[i]
		if a.Cause.Kind == balance.CauseFrame && a.Cause.FrameID == 0 {
			rootTransfer = a
		}
		if a.Cause.Kind == balance.CauseFrame && a.Cause.FrameID == 1 {
			childTransfer = a
		}
	}
	if rootTransfer == nil || !rootTransfer.EffectsCommitted {
		t.Fatalf("root transfer = %+v", rootTransfer)
	}
	if childTransfer == nil || childTransfer.EffectsCommitted {
		t.Fatalf("child transfer = %+v", childTransfer)
	}
	if len(childTransfer.Effects) != 2 {
		t.Fatal("the cancelled child transfer keeps its original effects")
	}
	// The reverted log attempt stays out of the committed receipt logs.
	if len(record.Receipt.Logs) != 1 || record.Receipt.Logs[0].Address != corpusParent {
		t.Fatalf("receipt logs = %+v", record.Receipt.Logs)
	}
	// The child account is absent from the boundary diff: its transient
	// storage write and value were rolled back.
	if _, ok := record.StateChanges.Find(corpusChild); ok {
		t.Fatal("the reverted child account must not appear in the boundary diff")
	}
	if err := record.Reconcile(); err != nil {
		t.Fatalf("the committed effects must reconcile with the boundary: %v", err)
	}
	// Fees stay committed at scope 0 although the root execution caught a
	// child revert (and would stay committed on a root revert too).
	for _, a := range record.Actions {
		if a.Cause.Kind == balance.CauseTransactionProtocol {
			if !a.EffectsCommitted || *a.RollbackScope != 0 {
				t.Fatalf("fee action = %+v", a)
			}
		}
	}
}

func TestAssembleTxIsDeterministicForReplay(t *testing.T) {
	first, err := AssembleTx(corpusNestedRevertTx())
	if err != nil {
		t.Fatal(err)
	}
	second, err := AssembleTx(corpusNestedRevertTx())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("the same observation must assemble into the identical record: capture recovery replays, it does not continue counters")
	}
}

func TestAssembleTxRejectsBrokenObservations(t *testing.T) {
	// A frame without an entry op.
	obs := corpusTransferTx()
	obs.Ops = []TxOp{{Kind: OpGasPurchase}, {Kind: OpTransfer, TransferIndex: 0}, {Kind: OpGasReturn}, {Kind: OpFeeCredit}}
	if _, err := AssembleTx(obs); !errors.Is(err, ErrAssembleShape) {
		t.Fatalf("missing frame entry error = %v", err)
	}
	// A double entry.
	obs = corpusTransferTx()
	obs.Ops = append(obs.Ops, TxOp{Kind: OpFrameEntry})
	if _, err := AssembleTx(obs); !errors.Is(err, ErrAssembleShape) {
		t.Fatalf("double frame entry error = %v", err)
	}
	// A transfer referenced twice.
	obs = corpusTransferTx()
	obs.Ops = []TxOp{{Kind: OpGasPurchase}, {Kind: OpFrameEntry}, {Kind: OpTransfer}, {Kind: OpTransfer}, {Kind: OpFeeCredit}}
	if _, err := AssembleTx(obs); !errors.Is(err, ErrAssembleShape) {
		t.Fatalf("double transfer error = %v", err)
	}
	// An unknown scope mark.
	obs = corpusTransferTx()
	obs.ScopeOps = append(obs.ScopeOps, ScopeOp{Kind: ScopeMarkRollback, Scope: 9})
	if _, err := AssembleTx(obs); !errors.Is(err, ErrScopeUnknown) {
		t.Fatalf("unknown scope mark error = %v", err)
	}
}

func TestRecordOrderContinuityGapRejected(t *testing.T) {
	record, err := AssembleTx(corpusTransferTx())
	if err != nil {
		t.Fatal(err)
	}
	// Drop the middle action: orders 1, 3, 4 no longer form the continuous
	// range 1..N, and the D74 validator must reject the stored record.
	gapped := *record
	gapped.Actions = []balance.BalanceAction{record.Actions[0], record.Actions[2]}
	if err := gapped.Validate(); err == nil {
		t.Fatal("a gap in the shared execution_order must fail validation")
	}
}

func TestTransactionRecordTxDescriptor(t *testing.T) {
	record, err := AssembleTx(corpusTransferTx())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := record.TxDescriptor(semantic.KindApplied,
		semantic.Network{ChainID: semantic.UInt256(u256("31337")), GenesisHash: semantic.Hash32(patternHash(0x0a))},
		semantic.ID16(patternID16(2)), semantic.ID16(patternID16(3)), 7, patternHash(1), 0,
		semantic.SourceRef{NodeInstanceID: semantic.ID16(patternID16(1)), WriterEpoch: semantic.ID16(patternID16(4)), SourceSequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Family != semantic.FamilyTx {
		t.Fatalf("family = %s", descriptor.Family)
	}
	// The entity ordinal is the body index plus one: block entity 0,
	// transaction 0 is entity 1.
	if descriptor.Activation.ActivationID.EntityOrdinal != 1 {
		t.Fatalf("entity ordinal = %d, want 1", descriptor.Activation.ActivationID.EntityOrdinal)
	}
	if ref := descriptor.Activation.Transaction; ref == nil || ref.NativePosition != 0 {
		t.Fatalf("inclusion binding = %+v", descriptor.Activation.Transaction)
	}
}

func TestTransactionRecordValidateRejectsMissingPieces(t *testing.T) {
	record, err := AssembleTx(corpusTransferTx())
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	noReceipt := *record
	noReceipt.Receipt = nil
	if err := noReceipt.Validate(); err == nil {
		t.Fatal("a record without a receipt must fail")
	}
	noRaw := *record
	noRaw.RawEncodings = nil
	if err := noRaw.Validate(); err == nil {
		t.Fatal("a record without raw encodings must fail")
	}
	unknownStatus := *record
	unknownStatus.CaptureStatus = CaptureUnknown
	if err := unknownStatus.Validate(); err == nil {
		t.Fatal("an unknown capture status must fail")
	}
}
