package poly

import (
	"encoding/hex"
	"errors"
	"testing"
)

// The receiver-call payload vectors come from the retained crossing judge
// (test/type_paths/poly/crossing of the type-paths worktree), re-derived
// with its independent codec.
const (
	wantOuterOne = "19494a17000000000000000000000000000000000000000000000000000000005ce282120000000000000000000000000000000000000000000000000000000000000040000000000000000000000000000000000000000000000000000000000000005ef85c019430000000000000000000000000000000000000039d657874726163746f722d636f727075732d73746174652d73796e632d31a00000000000000000000000000000000000000000000000000000000000000001018531353030310000"
	wantInnerOne = "26c53bea00000000000000000000000000000000000000000000000000000000000000010000000000000000000000000000000000000000000000000000000000000040000000000000000000000000000000000000000000000000000000000000001d657874726163746f722d636f727075732d73746174652d73796e632d31000000"
)

func TestReceiverCallPayloadsMatchCrossingVectors(t *testing.T) {
	l1 := mustHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	event := ClerkEventCapture{
		ID: 1, Contract: fixtureTarget, Data: fixtureData, TxHash: l1,
		LogIndex: 1, ChainID: "15001",
	}
	if got := hex.EncodeToString(commitCallInput(1558348306, event)); got != wantOuterOne {
		t.Fatalf("commit payload differs:\n got %s\nwant %s", got, wantOuterOne)
	}
	if got := hex.EncodeToString(onStateReceiveInput(1, fixtureData)); got != wantInnerOne {
		t.Fatalf("onStateReceive payload differs:\n got %s\nwant %s", got, wantInnerOne)
	}
}

func TestStateSyncWireAndRecordsMustMatch(t *testing.T) {
	tx := crossingStateSyncTx(t)
	// The wrong L1 hash in the body: the native import refuses the same
	// condition with its hash mismatch; so does the normalizer.
	bad := tx
	bad.Wire = mustHex(t, twoItemStateSyncWire)
	bad.Wire[167] ^= 0xff // corrupt the second record's L1 hash
	bad.ObservedTxHash = Keccak256(bad.Wire)
	_, err := NormalizeTransaction(&bad)
	if !errors.Is(err, ErrStateSyncMismatch) {
		t.Fatalf("L1 hash mismatch: got %v", err)
	}

	// A capture that disagrees with the body counts.
	worse := tx
	worse.StateSync.Records = worse.StateSync.Records[:1]
	if _, err := NormalizeTransaction(&worse); !errors.Is(err, ErrStateSyncMismatch) {
		t.Fatalf("record count mismatch: got %v", err)
	}

	// Record ids must continue the observed last id.
	gap := tx
	gap.StateSync.LastStateIDBefore = 5
	if _, err := NormalizeTransaction(&gap); !errors.Is(err, ErrStateSyncMismatch) {
		t.Fatalf("id discontinuity accepted: %v", err)
	}
}

func TestStateSyncRequiresFinalPositionAndGate(t *testing.T) {
	tx := crossingStateSyncTx(t)

	// Non-final body position: the crossing design's non-final refusal.
	nonFinal := tx
	nonFinal.Position = 0
	if _, err := NormalizeTransaction(&nonFinal); !errors.Is(err, ErrStateSyncMismatch) {
		t.Fatalf("non-final position accepted: %v", err)
	}

	// Before the Madhugiri gate the body type is not applicable.
	preGate := tx
	preGate.Forks = ForkState{MadhugiriBlock: 8, MadhugiriActive: true, ChainID: "15001"}
	preGate.BlockNumber = 2
	if _, err := NormalizeTransaction(&preGate); !errors.Is(err, ErrTypeNotApplicable) {
		t.Fatalf("pre-gate body 0x7f accepted: %v", err)
	}

	// A fabricated sender is rejected: the protocol defines none.
	sender := tx
	sender.Sender = addrPtr(fixtureSender)
	if _, err := NormalizeTransaction(&sender); err == nil {
		t.Fatalf("fabricated sender accepted")
	}

	// An EVM message projection on a protocol transaction is rejected.
	message := tx
	message.Message = &EvmMessageCapture{SenderProvenance: ProvenanceEncoded}
	if _, err := NormalizeTransaction(&message); err == nil {
		t.Fatalf("fabricated message projection accepted")
	}
}

func TestStateSyncFrameStructure(t *testing.T) {
	tx := crossingStateSyncTx(t)

	// Dropping the caught-revert target call breaks the structure.
	missing := tx
	missing.Frames = missing.Frames[:4]
	if _, err := NormalizeTransaction(&missing); err == nil {
		t.Fatalf("missing nested target call accepted")
	}

	// A tampered clerk event must not verify against the commit payload.
	tampered := tx
	event := *tampered.StateSync.Records[0].FullEvent
	event.ChainID = "137"
	tampered.StateSync.Records[0].FullEvent = &event
	if _, err := NormalizeTransaction(&tampered); err == nil {
		t.Fatalf("tampered clerk event accepted")
	}

	// A non-system root caller is not the native boundary.
	wrongCaller := tx
	wrongCaller.Frames[0].CallerAddress = addrPtr(fixtureSender)
	if _, err := NormalizeTransaction(&wrongCaller); err == nil {
		t.Fatalf("wrong root caller accepted")
	}
}

func TestStateSyncReceiptRules(t *testing.T) {
	tx := crossingStateSyncTx(t)

	gas := tx
	gas.Receipt.GasUsed = u64Ptr(1)
	if _, err := NormalizeTransaction(&gas); err == nil {
		t.Fatalf("gas-using state-sync receipt accepted")
	}

	price := tx
	five := fiveGwei
	price.Receipt.EffectiveGasPrice = &five
	if _, err := NormalizeTransaction(&price); err == nil {
		t.Fatalf("priced state-sync receipt accepted")
	}

	failed := tx
	failed.Receipt.Status = boolPtr(false)
	if _, err := NormalizeTransaction(&failed); err == nil {
		t.Fatalf("failed state-sync status accepted")
	}
}
