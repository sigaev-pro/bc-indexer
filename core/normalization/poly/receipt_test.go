package poly

import (
	"testing"
)

// TestReceiptEncodingKindSelection checks the closed D75 table: kind 1 with
// native_type 0 for a legacy receipt, kind 2 with native_type 127 for the
// StateSync receipt.
func TestReceiptEncodingKindSelection(t *testing.T) {
	legacy := legacyTxForCalls(t)
	record, err := NormalizeTransaction(&legacy)
	if err != nil {
		t.Fatalf("legacy: %v", err)
	}
	if record.Receipt.SourceEncoding.Kind != 1 || record.Receipt.SourceEncoding.NativeType == nil ||
		*record.Receipt.SourceEncoding.NativeType != 0 {
		t.Fatalf("legacy receipt encoding differs")
	}

	sync := crossingStateSyncTx(t)
	syncRecord, err := NormalizeTransaction(&sync)
	if err != nil {
		t.Fatalf("state sync: %v", err)
	}
	if syncRecord.Receipt.SourceEncoding.Kind != 2 || syncRecord.Receipt.SourceEncoding.NativeType == nil ||
		*syncRecord.Receipt.SourceEncoding.NativeType != 127 {
		t.Fatalf("state sync receipt encoding differs")
	}
}

// TestReceiptConsensusCrossCheck checks that the consensus bytes and the
// typed receipt are verified against each other: a mismatch in any common
// field is an explicit error.
func TestReceiptConsensusCrossCheck(t *testing.T) {
	tx := legacyTxForCalls(t)

	// A typed cumulative value that differs from the consensus field.
	mismatch := tx
	mismatch.Receipt.CumulativeGasUsed = 21001
	if _, err := NormalizeTransaction(&mismatch); err == nil {
		t.Fatalf("cumulative gas mismatch accepted")
	}

	// A typed bloom that differs from the consensus bloom.
	bloom := tx
	var other Bloom
	other[0] = 1
	bloom.Receipt.LogsBloom = other
	if _, err := NormalizeTransaction(&bloom); err == nil {
		t.Fatalf("bloom mismatch accepted")
	}

	// Trailing bytes after the consensus list.
	trailing := tx
	trailing.Receipt.Consensus = append(append([]byte(nil), tx.Receipt.Consensus...), 0)
	if _, err := NormalizeTransaction(&trailing); err == nil {
		t.Fatalf("trailing consensus bytes accepted")
	}

	// A typed log the consensus bytes do not carry.
	extra := tx
	log := fixtureLog(fixtureTarget, 1)
	extra.Receipt.Logs = []LogCapture{log}
	if _, err := NormalizeTransaction(&extra); err == nil {
		t.Fatalf("typed log without consensus log accepted")
	}
}

// TestReceiptPreStatusRoot checks that a pre-status source root stays
// source data: the root is kept and never converted into a status.
func TestReceiptPreStatusRoot(t *testing.T) {
	tx := legacyTxForCalls(t)
	root := mustHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	emptyBloom := fixtureBloom(nil)
	tx.Receipt.Status = nil
	tx.Receipt.PostStateRoot = hashPtr(root)
	receipt := rlpEncodeList(
		rlpEncodeBytes(root[:]),
		rlpEncodeUint(21000),
		rlpEncodeBytes(emptyBloom[:]),
		rlpEncodeList(),
	)
	tx.Receipt.Consensus = receipt
	record, err := NormalizeTransaction(&tx)
	if err != nil {
		t.Fatalf("pre-status root: %v", err)
	}
	if record.Receipt.Status != nil || record.Receipt.PostStateRoot == nil {
		t.Fatalf("pre-status root was converted")
	}

	// A root that disagrees with the consensus field is rejected.
	tx.Receipt.PostStateRoot = hashPtr(mustHash("0x4444444444444444444444444444444444444444444444444444444444444444"))
	if _, err := NormalizeTransaction(&tx); err == nil {
		t.Fatalf("root mismatch accepted")
	}
}
