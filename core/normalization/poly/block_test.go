package poly

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// TestBlockCarrierSplitNeverMixes: the legacy record and the body carrier
// never appear together, and a 0x7f before the gate is refused.
func TestBlockCarrierSplitNeverMixes(t *testing.T) {
	capture := crossingBlock(t)
	opts := Options{LegacyReceipt: true, DeclaredFixtures: []string{"POLY-L01"}}
	legacy := legacyCapture(t)
	capture.LegacyReceipt = legacy
	if _, err := NormalizeBlock(&capture, opts); err == nil {
		t.Fatalf("legacy record alongside the body carrier accepted")
	}

	// A body 0x7f before the Madhugiri gate of its own block.
	preGate := legacyCorpusBlock(t)
	preGate.Forks.MadhugiriBlock = 8
	preGate.Transactions = []TxCapture{crossingStateSyncTx(t)}
	preGate.Transactions[0].BlockNumber = 4
	preGate.Transactions[0].Forks = preGate.Forks
	preGate.Transactions[0].Position = 0
	preGate.Transactions[0].BodyTransactionCount = 1
	if _, err := NormalizeBlock(&preGate, opts); !errors.Is(err, ErrTypeNotApplicable) {
		t.Fatalf("pre-gate body carrier accepted: %v", err)
	}

	// Rows after the gate without the final body transaction.
	missing := crossingBlock(t)
	missing.StateSyncRows = 2
	missing.Transactions = missing.Transactions[:1]
	missing.Header.GasUsed = 21000
	if _, err := NormalizeBlock(&missing, Options{}); !errors.Is(err, ErrStateSyncMismatch) {
		t.Fatalf("missing body carrier accepted: %v", err)
	}
}

// TestBlockStateSyncFinalPosition: at most one 0x7f, at the final body
// position, with the observed row count.
func TestBlockStateSyncFinalPosition(t *testing.T) {
	// Non-final position: the crossing design's non-final refusal.
	nonFinal := crossingBlock(t)
	nonFinal.Transactions = []TxCapture{crossingStateSyncTx(t), crossingLegacyTx(t)}
	nonFinal.Transactions[0].Position = 0
	nonFinal.Transactions[1].Position = 1
	if _, err := NormalizeBlock(&nonFinal, Options{}); !errors.Is(err, ErrStateSyncMismatch) {
		t.Fatalf("non-final body carrier accepted: %v", err)
	}

	// Two carriers.
	double := crossingBlock(t)
	double.Transactions = append(double.Transactions, crossingStateSyncTx(t))
	double.Transactions[2].Position = 2
	double.Transactions[2].BodyTransactionCount = 3
	double.StateSyncRows = 4
	if _, err := NormalizeBlock(&double, Options{}); err == nil {
		t.Fatalf("double body carrier accepted")
	}

	// The observed rows must equal the body records.
	counted := crossingBlock(t)
	counted.StateSyncRows = 3
	if _, err := NormalizeBlock(&counted, Options{}); !errors.Is(err, ErrStateSyncMismatch) {
		t.Fatalf("row count mismatch accepted: %v", err)
	}
}

// TestBlockSprintBoundary: the system work commits at the sprint boundary
// of the pinned rule.
func TestBlockSprintBoundary(t *testing.T) {
	offBoundary := crossingBlock(t)
	offBoundary.Number = 3
	offBoundary.Header.Number = 3
	offBoundary.Forks = ForkState{MadhugiriBlock: 2, MadhugiriActive: true, ChainID: "15001"}
	for i := range offBoundary.Transactions {
		offBoundary.Transactions[i].BlockNumber = 3
	}
	if _, err := NormalizeBlock(&offBoundary, Options{}); err == nil {
		t.Fatalf("state sync off the sprint boundary accepted")
	}

	// The pre-gate corpus block sits on its own boundary (4 % 4 == 0).
	onBoundary := legacyCorpusBlock(t)
	if _, err := NormalizeBlock(&onBoundary, legacyOpts()); err != nil {
		t.Fatalf("boundary corpus block refused: %v", err)
	}
}

// TestBlockHeaderCoherence: header number/parent/time must agree with the
// block identity, and the header gas must equal the last receipt rollup.
func TestBlockHeaderCoherence(t *testing.T) {
	number := crossingBlock(t)
	number.Header.Number = 7
	if _, err := NormalizeBlock(&number, Options{}); !errors.Is(err, ErrBlock) {
		t.Fatalf("header number mismatch accepted: %v", err)
	}

	gas := crossingBlock(t)
	gas.Header.GasUsed = 30000
	if _, err := NormalizeBlock(&gas, Options{}); err == nil {
		t.Fatalf("header gas mismatch accepted")
	}

	parent := crossingBlock(t)
	parent.Header.ParentHash = mustHash("0x8888888888888888888888888888888888888888888888888888888888888888")
	if _, err := NormalizeBlock(&parent, Options{}); !errors.Is(err, ErrBlock) {
		t.Fatalf("header parent mismatch accepted: %v", err)
	}
}

// TestBlockTimestampSeconds: the Bor timestamp keeps the source precision
// of whole seconds.
func TestBlockTimestampSeconds(t *testing.T) {
	capture := crossingBlock(t)
	record, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if record.Metadata.Timestamp.Seconds != 1558348307 || record.Metadata.Timestamp.Nanos != 0 {
		t.Fatalf("timestamp %v", record.Metadata.Timestamp)
	}
}

// TestBlockFamilyDetailsAndFinality: the Bor details and the finality
// marker carry the same observed references, one per row.
func TestBlockFamilyDetailsAndFinality(t *testing.T) {
	capture := crossingBlock(t)
	id := uint64(42)
	capture.HeimdallCheckpoints = append(capture.HeimdallCheckpoints, HeimdallRefCapture{
		Kind: HeimdallCheckpoint, EndHeight: 2, EndHash: fixtureBlockHash, CheckpointID: &id,
	})
	record, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(record.Finality.HeimdallCheckpoints) != 2 {
		t.Fatalf("finality references %d", len(record.Finality.HeimdallCheckpoints))
	}
	second := record.Finality.HeimdallCheckpoints[1]
	if second.Kind != block.CheckpointArea || !second.CheckpointID.Present() || second.CheckpointID.MustGet() != 42 {
		t.Fatalf("checkpoint reference differs")
	}
	details := record.Metadata.Details.Bor
	if len(details.HeimdallCheckpoints) != 2 ||
		details.HeimdallCheckpoints[0].End.Hash != record.Finality.HeimdallCheckpoints[0].End.Hash {
		t.Fatalf("block details and finality marker disagree")
	}
	if record.Metadata.Details.Family() != block.FamilyPoly {
		t.Fatalf("family differs")
	}
}

// TestLegacyBlockBinding: the legacy record binds to its own block, index
// and cumulative gas.
func TestLegacyBlockBinding(t *testing.T) {
	capture := legacyCorpusBlock(t)
	wrongBlock := capture
	wrongBlock.LegacyReceipt = &LegacyReceiptCapture{}
	*wrongBlock.LegacyReceipt = *capture.LegacyReceipt
	wrongBlock.LegacyReceipt.BlockHash = mustHash("0x7777777777777777777777777777777777777777777777777777777777777777")
	if _, err := NormalizeBlock(&wrongBlock, legacyOpts()); err == nil {
		t.Fatalf("legacy record of another block accepted")
	}

	wrongIndex := capture
	wrongIndex.LegacyReceipt = &LegacyReceiptCapture{}
	*wrongIndex.LegacyReceipt = *capture.LegacyReceipt
	wrongIndex.LegacyReceipt.OrdinaryReceiptCount = 2
	if _, err := NormalizeBlock(&wrongIndex, legacyOpts()); err == nil {
		t.Fatalf("wrong synthetic index accepted")
	}
}
