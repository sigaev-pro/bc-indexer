package poly

import (
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

func legacyTxForCalls(t *testing.T) TxCapture {
	t.Helper()
	tx := crossingLegacyTx(t)
	tx.Receipt.Consensus = fixtureReceiptWire(t, nil, 21000, fixtureBloom(nil), nil)
	return tx
}

// TestCallsD81AttemptedVsCreated checks the D81 rule through the crossing
// fixture: a CREATE attempt that fails keeps its attempted address and has
// no created address; a created address requires local success.
func TestCallsD81AttemptedVsCreated(t *testing.T) {
	tx := legacyTxForCalls(t)
	attempted := mustAddress("0x5000000000000000000000000000000000000001")
	created := mustAddress("0x5000000000000000000000000000000000000002")

	// A failed CREATE keeps the attempt address.
	failed := tx
	failed.Frames = append(failed.Frames, FrameCapture{
		Entry: 6, Parent: 0, Depth: 1,
		Engine: balance.EngineEVM, Kind: FrameCreate,
		CallerAddress: addrPtr(fixtureSender), ContextAddress: addrPtr(fixtureSender),
		AttemptedCreateAddress: addrPtr(attempted),
		Outcome:                OutcomeFailure, ErrorCode: strPtr("gas uint64 overflow"),
	})
	record, err := NormalizeTransaction(&failed)
	if err != nil {
		t.Fatalf("failed create: %v", err)
	}
	call := record.Calls[len(record.Calls)-1]
	if call.AttemptedCreateAddress == nil || *call.AttemptedCreateAddress != attempted {
		t.Fatalf("attempted address missing on a failed create")
	}
	if call.CreatedAddress != nil {
		t.Fatalf("failed create must not carry a created address")
	}

	// A created address without local success is rejected.
	forged := failed
	forged.Frames[len(forged.Frames)-1].CreatedAddress = addrPtr(created)
	if _, err := NormalizeTransaction(&forged); err == nil {
		t.Fatalf("created address on a failed create accepted")
	}

	// A created address without an attempt address is rejected.
	noAttempt := failed
	noAttempt.Frames[len(noAttempt.Frames)-1].Outcome = OutcomeSuccess
	noAttempt.Frames[len(noAttempt.Frames)-1].Committed = true
	noAttempt.Frames[len(noAttempt.Frames)-1].CreatedAddress = addrPtr(created)
	noAttempt.Frames[len(noAttempt.Frames)-1].AttemptedCreateAddress = nil
	if _, err := NormalizeTransaction(&noAttempt); err == nil {
		t.Fatalf("created address without an attempt accepted")
	}

	// A successful child inside a reverted parent is not committed.
	rolled := tx
	rolled.Snapshots = []SnapshotCapture{{NativeID: 1, Parent: -1, OwnerFrame: 0, RolledBack: true}}
	rolled.Frames = append(rolled.Frames, FrameCapture{
		Entry: 6, Parent: 0, Depth: 1,
		Engine: balance.EngineEVM, Kind: FrameCall,
		CallerAddress: addrPtr(fixtureSender), ContextAddress: addrPtr(fixtureSuccess),
		Target: addrPtr(fixtureSuccess), Outcome: OutcomeSuccess,
	})
	if _, err := NormalizeTransaction(&rolled); err == nil {
		t.Fatalf("committed child inside an uncommitted parent accepted")
	}
}

// TestCallsShapeRules checks the frame tree rules: parent before child,
// depth continuity, engine/kind pairs and the root frame rules.
func TestCallsShapeRules(t *testing.T) {
	tx := legacyTxForCalls(t)

	orphan := tx
	orphan.Frames = append(orphan.Frames, FrameCapture{
		Entry: 6, Parent: 9, Depth: 1, Engine: balance.EngineEVM, Kind: FrameCall,
	})
	if _, err := NormalizeTransaction(&orphan); err == nil {
		t.Fatalf("frame with a later parent accepted")
	}

	skip := tx
	skip.Frames = append(skip.Frames, FrameCapture{
		Entry: 6, Parent: 0, Depth: 3, Engine: balance.EngineEVM, Kind: FrameCall,
	})
	if _, err := NormalizeTransaction(&skip); err == nil {
		t.Fatalf("depth discontinuity accepted")
	}

	mixed := tx
	mixed.Frames = append(mixed.Frames, FrameCapture{
		Entry: 6, Parent: 0, Depth: 1, Engine: balance.EngineNativeProtocol, Kind: FrameCall,
		CallerAddress: addrPtr(fixtureSender), Target: addrPtr(fixtureSuccess),
		Outcome: OutcomeSuccess, Committed: true,
	})
	if _, err := NormalizeTransaction(&mixed); err == nil {
		t.Fatalf("EVM kind with native protocol engine accepted")
	}

	committed := tx
	committed.Frames[0].Outcome = OutcomeRevert
	if _, err := NormalizeTransaction(&committed); err == nil {
		t.Fatalf("committed reverted root accepted")
	}
}

func strPtr(s string) *string { return &s }
