package eth

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// corpusSelfDestructTx extends the transfer observation with one observed
// SELFDESTRUCT of the recipient contract (7 wei to the coinbase, deleted).
func corpusSelfDestructTx() TxObservation {
	obs := corpusTransferTx()
	obs.Inclusion = Inclusion{NativePosition: 1, TransactionHash: patternHash(0x33)}
	obs.Transfers = nil
	obs.Ops = []TxOp{
		{Kind: OpGasPurchase},
		{Kind: OpFrameEntry, FrameIndex: 0},
		{Kind: OpSelfDestruct, SelfDestructIdx: 0},
		{Kind: OpGasReturn},
		{Kind: OpFeeCredit},
	}
	obs.SelfDestruct = []SelfDestructObservation{{
		FrameID:          0,
		Contract:         corpusParent,
		Beneficiary:      corpusRecipient,
		BalanceAtEntry:   u256("7"),
		DeletionEligible: true,
		Scope:            1,
		AccountDeleted:   true,
		MovementPosition: MovementAtOpcode,
		MovementEffects: []balance.BalanceEffect{
			{EffectIndex: 0, Address: corpusParent, Before: u256("7"), After: u256("0")},
			{EffectIndex: 1, Address: corpusRecipient, Before: u256("0"), After: u256("7")},
		},
	}}
	obs.State = []AccountObservation{
		accountObservation(corpusParent, true, "7", 0, corpusParentCode, false, "0", 0, ""),
		accountObservation(corpusRecipient, false, "0", 0, "", true, "7", 0, ""),
		accountObservation(corpusSender, true, "10000000000000000000", 0, "", true, "9999958000000000000", 1, ""),
		accountObservation(corpusCoinbase, false, "0", 0, "", true, "23625000000000", 0, ""),
	}
	// The parent is deleted: its storage reset marker.
	obs.State[0].StorageReset = true
	obs.State[0].Before.Storage = map[Hash32]Hash32{slotHash(0): valueHash(1)}
	return obs
}

func TestAssembleTxSelfDestructThroughWire(t *testing.T) {
	direct, err := AssembleTx(corpusSelfDestructTx())
	if err != nil {
		t.Fatal(err)
	}
	// The execution action and its projection share the order through the
	// cause link; the deletion is committed.
	if len(direct.ExecActions) != 1 || !direct.ExecActions[0].Payload.SelfDestruct.DeletionCommitted {
		t.Fatalf("execution actions = %+v", direct.ExecActions)
	}
	found := false
	for _, a := range direct.Actions {
		if a.Cause.Kind == balance.CauseExecutionAction {
			found = true
			if a.Kind != balance.KindSelfDestructTransfer || a.Amount.Decimal() != "7" {
				t.Fatalf("projection = %+v", a)
			}
		}
	}
	if !found {
		t.Fatal("the selfdestruct monetary projection is missing")
	}
	// The deleted parent keeps its storage reset without per-key rows.
	change, _ := direct.StateChanges.Find(corpusParent)
	if !change.StorageReset || len(change.StorageChanges) != 0 {
		t.Fatalf("deleted account change = %+v", change)
	}
	// The wire round trip preserves the record exactly.
	capturer := newCapturer()
	transport := NewTransport(capturer)
	store := newMemStore()
	sink := &funcSink{}
	collector, err := NewCollector(capturer, store, sink, DefaultScopeDeclaration())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := transport.EmitBlock(ctx, corpusBlockObservation(1), nil); err != nil {
		t.Fatal(err)
	}
	if err := transport.EmitTx(ctx, corpusSelfDestructTx(), nil); err != nil {
		t.Fatal(err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if err := collector.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*sink.blocks[0].Records[0], *direct) {
		t.Fatal("the selfdestruct record through the import path must equal the direct assembly")
	}
}

func TestStringHelpers(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{CaptureCaptured.String(), "CAPTURED"},
		{CaptureNotApplicable.String(), "NOT_APPLICABLE"},
		{CaptureUnknown.String(), "CAPTURE_UNKNOWN"},
		{RawLegacyRLP.String(), "EVM_LEGACY_RLP"},
		{RawEIP2718Envelope.String(), "EIP2718_ENVELOPE"},
		{RawEncodingKind(9).String(), "RAW_ENCODING_INVALID"},
		{ProvenanceEncoded.String(), "ENCODED"},
		{ProvenanceNativeDerived.String(), "NATIVE_DERIVED"},
		{ProvenanceNotApplicable.String(), "NOT_APPLICABLE"},
		{NetworkDetails{Eth: &EthReceiptDetails{}}.Family(), "ETH"},
		{NetworkDetails{}.Family(), ""},
		{KindCall.String(), "CALL"},
		{KindCreate2.String(), "CREATE2"},
		{KindNativeProtocol.String(), "NATIVE_PROTOCOL"},
		{FrameKind(88).String(), "FRAME_KIND_INVALID(88)"},
		{ResultSuccess.String(), "SUCCESS"},
		{ResultRevert.String(), "REVERT"},
		{ResultFailure.String(), "FAILURE"},
		{OptionalBlockReward.String(), "balance.system.block-reward"},
		{OptionalNone.String(), "OPTIONAL_INVALID"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("String() = %q, want %q", c.got, c.want)
		}
	}
}

func TestFrameByOrder(t *testing.T) {
	frames, err := BuildCalls(NewOrder(), []FrameInput{
		{Kind: KindCall, Engine: balance.EngineEVM, LocalOutcome: ResultSuccess},
		{ParentIndex: 0, Kind: KindStaticCall, Engine: balance.EngineEVM, LocalOutcome: ResultSuccess},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := FrameByOrder(frames, 2); !ok || f.FrameID != 1 {
		t.Fatalf("FrameByOrder(2) = %+v, %v", f, ok)
	}
	if _, ok := FrameByOrder(frames, 9); ok {
		t.Fatal("an unknown order must not resolve")
	}
}

func TestTxObservationTxBalancesView(t *testing.T) {
	record, err := AssembleTx(corpusTransferTx())
	if err != nil {
		t.Fatal(err)
	}
	view := record.TxBalances()
	if len(view.Frames) != 1 || len(view.Scopes) != 2 || view.FeeSummary.NetCharge.Decimal() != "42000000000000" {
		t.Fatalf("view = %+v", view.FeeSummary)
	}
}

func TestReceiptSourcesDifferFromTypedFields(t *testing.T) {
	// The consensus bytes carry no gas_used, prices or hashes: those stay
	// typed-only (D75). A receipt differing only in those fields still
	// verifies against the same bytes.
	r := corpusReceiptNoLogs()
	enc, err := SourceEncoding(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	other := *r
	other.GasUsed = Some(uint64(1))
	other.EffectiveGasPrice = Some(gwei(9))
	if err := VerifySourceEncoding(enc, &other); err != nil {
		t.Fatalf("consensus-only fields must not affect verification: %v", err)
	}
	if !bytes.Equal(enc.Bytes, mustReencode(t, &other)) {
		t.Fatal("the consensus bytes must not change with typed-only fields")
	}
}

func mustReencode(t *testing.T, r *Receipt) []byte {
	t.Helper()
	enc, err := SourceEncoding(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	return enc.Bytes
}
