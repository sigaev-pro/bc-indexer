package poly

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

func mutateItems(t *testing.T, wire string) TxCapture {
	t.Helper()
	tx := crossingStateSyncTx(t)
	tx.Wire = mustHex(t, wire)
	tx.ObservedTxHash = Keccak256(tx.Wire)
	return tx
}

func TestStateSyncWireFramingNegatives(t *testing.T) {
	// A two-item wire with the first item's contract truncated by two
	// bytes; every field-width violation is a mismatch error.
	cases := map[string]string{
		"item not a list":   "7f81ff",
		"one field only":    "7fc301000194300000000000000000000000000000000000000039d657874726163746f722d636f727075732d73746174652d73796e632d31a00000000000000000000000000000000000000000000000000000000000000001",
		"short l1 hash":     "7ff852019430000000000000000000000000000000000000039d657874726163746f722d636f727075732d73746174652d73796e632d3180000000000000000000000000000000000000000000000000000000000000001",
		"short contract":    "7ff8530182300000000000000000000000000000000000039d657874726163746f722d636f727075732d73746174652d73796e632d31a0000000000000000000000000000000000000000000000000000000000000001",
		"missing envelope":  "f8aef855019430000000000000000000000000000000000000039d657874726163746f722d636f727075732d73746174652d73796e632d31a0000000000000000000000000000000000000000000000000000000000000001",
		"truncated body":    "7ff8ae",
		"nonminimal id":     "7ff8569400820101943000000000000000000000000000000000000039d657874726163746f722d636f727075732d73746174652d73796e632d31a0000000000000000000000000000000000000000000000000000000000000001",
		"list inside data":  "7ff85701c0943000000000000000000000000000000000000039d657874726163746f722d636f727075732d73746174652d73796e632d31a0000000000000000000000000000000000000000000000000000000000000001",
		"list inside item":  "7ff856f5c401943000000000000000000000000000000000000039d657874726163746f722d636f727075732d73746174652d73796e632d31a0000000000000000000000000000000000000000000000000000000000000001",
		"short string item": "7f82abff",
	}
	for name, wire := range cases {
		tx := mutateItems(t, wire)
		if _, err := NormalizeTransaction(&tx); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestStateSyncTypeEnvelopeMismatch(t *testing.T) {
	tx := crossingStateSyncTx(t)
	tx.NativeType = 2 // typed envelope type with 0x7f bytes
	if _, err := NormalizeTransaction(&tx); err == nil {
		t.Fatalf("0x7f bytes with a typed-envelope type accepted")
	}

	legacy := legacyTxForCalls(t)
	legacy.NativeType = StateSyncTxType
	if _, err := NormalizeTransaction(&legacy); err == nil {
		t.Fatalf("state-sync type with a legacy envelope accepted")
	}

	empty := legacy
	empty.Wire = nil
	if _, err := NormalizeTransaction(&empty); err == nil {
		t.Fatalf("empty wire accepted")
	}
}

func TestReceiptPresenceNegatives(t *testing.T) {
	tx := legacyTxForCalls(t)

	both := tx
	root := mustHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	both.Receipt.PostStateRoot = hashPtr(root)
	if _, err := NormalizeTransaction(&both); err == nil {
		t.Fatalf("status and root together accepted")
	}

	neither := tx
	neither.Receipt.Status = nil
	if _, err := NormalizeTransaction(&neither); err == nil {
		t.Fatalf("receipt without outcome accepted")
	}

	result := tx
	result.Receipt.Result = ResultUnspecified
	if _, err := NormalizeTransaction(&result); err == nil {
		t.Fatalf("unspecified result accepted")
	}
}

func TestFrameAndSelfDestructNegatives(t *testing.T) {
	tx := legacyTxForCalls(t)

	kindless := tx
	kindless.Frames[0].Kind = FrameUnspecified
	if _, err := NormalizeTransaction(&kindless); err == nil {
		t.Fatalf("unspecified frame kind accepted")
	}

	outcomeless := tx
	outcomeless.Frames[0].Outcome = OutcomeUnspecified
	if _, err := NormalizeTransaction(&outcomeless); err == nil {
		t.Fatalf("unspecified frame outcome accepted")
	}

	createWithTarget := tx
	created := mustAddress("0x5000000000000000000000000000000000000002")
	createWithTarget.Frames = append(createWithTarget.Frames, FrameCapture{
		Entry: 6, Parent: 0, Depth: 1, Engine: balance.EngineEVM, Kind: FrameCreate,
		CallerAddress: addrPtr(fixtureSender), Target: addrPtr(fixtureSuccess),
		AttemptedCreateAddress: addrPtr(created), CreatedAddress: addrPtr(created),
		Outcome: OutcomeSuccess, Committed: true,
	})
	if _, err := NormalizeTransaction(&createWithTarget); err == nil {
		t.Fatalf("create frame with a target accepted")
	}

	callWithCreate := tx
	callWithCreate.Frames = append(callWithCreate.Frames, FrameCapture{
		Entry: 6, Parent: 0, Depth: 1, Engine: balance.EngineEVM, Kind: FrameStaticCall,
		CallerAddress: addrPtr(fixtureSender), Target: addrPtr(fixtureSuccess),
		AttemptedCreateAddress: addrPtr(created), Outcome: OutcomeSuccess, Committed: true,
	})
	if _, err := NormalizeTransaction(&callWithCreate); err == nil {
		t.Fatalf("call frame with create addresses accepted")
	}

	badFrame := tx
	badFrame.SelfDestructs = []SelfDestructCapture{{Sequence: 6, Frame: 9}}
	if _, err := NormalizeTransaction(&badFrame); err == nil {
		t.Fatalf("selfdestruct with an unknown frame accepted")
	}

	uncommittedDeletion := tx
	uncommittedDeletion.SelfDestructs = []SelfDestructCapture{{Sequence: 6, Frame: 0, Deleted: true}}
	if _, err := NormalizeTransaction(&uncommittedDeletion); err == nil {
		t.Fatalf("uncommitted deletion accepted")
	}
}

func TestOrderAndScopeNegatives(t *testing.T) {
	tx := legacyTxForCalls(t)

	repeat := tx
	repeat.Frames = append(repeat.Frames, FrameCapture{
		Entry: 2, Parent: 0, Depth: 1, Engine: balance.EngineEVM, Kind: FrameCall,
		CallerAddress: addrPtr(fixtureSender), Outcome: OutcomeSuccess, Committed: true,
	})
	if _, err := NormalizeTransaction(&repeat); !errors.Is(err, ErrOrder) {
		t.Fatalf("repeated frame entry accepted: %v", err)
	}

	twinSelfDestructs := tx
	twinSelfDestructs.SelfDestructs = []SelfDestructCapture{
		{Sequence: 7, Frame: 0}, {Sequence: 7, Frame: 0},
	}
	if _, err := NormalizeTransaction(&twinSelfDestructs); !errors.Is(err, ErrOrder) {
		t.Fatalf("twin selfdestruct sequences accepted: %v", err)
	}

	badSnapshotRow := tx
	badSnapshotRow.Operations[1].Snapshot = 4
	if _, err := NormalizeTransaction(&badSnapshotRow); !errors.Is(err, ErrScope) {
		t.Fatalf("unknown snapshot row accepted: %v", err)
	}

	laterParent := tx
	laterParent.Snapshots = []SnapshotCapture{
		{NativeID: 1, Parent: 1, OwnerFrame: 0},
	}
	if _, err := NormalizeTransaction(&laterParent); !errors.Is(err, ErrScope) {
		t.Fatalf("self-referencing scope parent accepted: %v", err)
	}

	unknownOwner := tx
	unknownOwner.Snapshots = []SnapshotCapture{{NativeID: 1, Parent: -1, OwnerFrame: 7}}
	if _, err := NormalizeTransaction(&unknownOwner); !errors.Is(err, ErrScope) {
		t.Fatalf("unknown owner frame accepted: %v", err)
	}

	causeless := tx
	causeless.Operations[1].Cause = OpCauseUnspecified
	if _, err := NormalizeTransaction(&causeless); err == nil {
		t.Fatalf("unspecified operation cause accepted")
	}

	frameless := tx
	frameless.Operations[1].Frame = 3
	if _, err := NormalizeTransaction(&frameless); err == nil {
		t.Fatalf("operation with an unknown frame accepted")
	}

	sdless := tx
	sdless.Operations[1].Cause = OpCauseSelfDestruct
	sdless.Operations[1].SelfDestruct = 0
	if _, err := NormalizeTransaction(&sdless); err == nil {
		t.Fatalf("selfdestruct cause without the opcode accepted")
	}
}

func TestLegacyExtraNegatives(t *testing.T) {
	capture := legacyCapture(t)

	wrongFields := *capture
	wrongFields.StorageRLP = rlpEncodeList(
		rlpEncodeBytes([]byte{1}),
		rlpEncodeUint(0),
	)
	if _, err := NormalizeLegacyReceipt(&wrongFields, legacyOpts()); !errors.Is(err, ErrLegacySource) {
		t.Fatalf("two-field storage accepted: %v", err)
	}

	wrongLogs := *capture
	wrongLogs.Logs = append(wrongLogs.Logs, fixtureLog(fixtureTarget, 2))
	if _, err := NormalizeLegacyReceipt(&wrongLogs, legacyOpts()); !errors.Is(err, ErrLegacySource) {
		t.Fatalf("log count mismatch accepted: %v", err)
	}

	badLogAddress := *capture
	badLogAddress.StorageRLP = rlpEncodeList(
		rlpEncodeBytes([]byte{1}),
		rlpEncodeUint(0),
		rlpEncodeList(rlpEncodeList(
			rlpEncodeBytes(fixtureRevert[:]),
			rlpEncodeList(rlpEncodeBytes(StateSyncEventLogTopic[:])),
			rlpEncodeBytes(oneData[:]),
		)),
	)
	if _, err := NormalizeLegacyReceipt(&badLogAddress, legacyOpts()); !errors.Is(err, ErrLegacySource) {
		t.Fatalf("log address mismatch accepted: %v", err)
	}
}

func TestReplayGuardNegatives(t *testing.T) {
	capture := crossingBlock(t)
	record, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if err := EqualSemantics(nil, record); err == nil {
		t.Fatalf("nil record accepted")
	}
	if err := EqualSemantics(record, nil); err == nil {
		t.Fatalf("nil replay accepted")
	}

	other, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	other.Metadata.SourceTransactionCount = 9
	if err := EqualSemantics(record, other); err == nil {
		t.Fatalf("metadata difference accepted")
	}
	other2, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	other2.Finality.HeimdallCheckpoints = nil
	if err := EqualSemantics(record, other2); err == nil {
		t.Fatalf("finality difference accepted")
	}
	other3, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	other3.Transactions[1].Receipt.SourceEncoding.Bytes[0] ^= 0xff
	if err := VerifyBytesStable(record, other3); err == nil {
		t.Fatalf("consensus bytes difference accepted")
	}
	shorter, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	shorter.Transactions = shorter.Transactions[:1]
	if err := VerifyBytesStable(record, shorter); err == nil {
		t.Fatalf("length difference accepted")
	}
}

func TestEnumNames(t *testing.T) {
	names := []struct {
		got  string
		want string
	}{
		{FrameCall.String(), "CALL"},
		{FrameCallCode.String(), "CALLCODE"},
		{FrameDelegateCall.String(), "DELEGATECALL"},
		{FrameStaticCall.String(), "STATICCALL"},
		{FrameCreate.String(), "CREATE"},
		{FrameCreate2.String(), "CREATE2"},
		{FrameNativeProtocol.String(), "NATIVE_PROTOCOL"},
		{FrameUnspecified.String(), "FRAME_INVALID"},
		{OutcomeSuccess.String(), "SUCCESS"},
		{OutcomeRevert.String(), "REVERT"},
		{OutcomeFailure.String(), "FAILURE"},
		{OutcomeUnspecified.String(), "OUTCOME_INVALID"},
		{ResultSuccess.String(), "SUCCESS"},
		{ResultRevert.String(), "REVERT"},
		{ResultFailure.String(), "FAILURE"},
		{ResultUnspecified.String(), "RESULT_INVALID"},
		{LogScopeSystemPre.String(), "SYSTEM_PRE"},
		{LogScopeTXStateSync.String(), "TX_STATE_SYNC"},
		{LogScopeSystemPost.String(), "SYSTEM_POST"},
		{LogScopeInvalid.String(), "LOG_SCOPE_INVALID"},
		{NativeOpBorStateSync.String(), "BOR_STATE_SYNC"},
		{NativeOpUnspecified.String(), "NATIVE_OPERATION_INVALID"},
		{RawEncodingEvmLegacyRLP.String(), "EVM_LEGACY_RLP"},
		{RawEncodingEip2718Envelope.String(), "EIP2718_ENVELOPE"},
		{RawEncodingBorStateSyncEnvelope.String(), "BOR_STATE_SYNC_ENVELOPE"},
		{RawEncodingUnspecified.String(), "RAW_ENCODING_INVALID"},
	}
	for _, c := range names {
		if c.got != c.want {
			t.Errorf("enum name %s, want %s", c.got, c.want)
		}
	}
}

func TestForkStateGate(t *testing.T) {
	gate := ForkState{MadhugiriBlock: 100, MadhugiriActive: true}
	if gate.StateSyncAt(99) || !gate.StateSyncAt(100) || !gate.StateSyncAt(101) {
		t.Fatalf("gate boundary differs")
	}
	unscheduled := ForkState{MadhugiriBlock: 100, MadhugiriActive: false}
	if unscheduled.StateSyncAt(200) {
		t.Fatalf("unscheduled gate opened")
	}
}
