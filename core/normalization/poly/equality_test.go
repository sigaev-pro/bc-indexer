package poly

import (
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// The deep-equality helpers back the replay checks; every branch must
// distinguish real differences, including pointer presence and explicit
// zeros.

func twoRecords(t *testing.T) (*BlockRecord, *BlockRecord) {
	t.Helper()
	capture := crossingBlock(t)
	first, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	replay, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	return first, replay
}

func TestDeepEqualBranches(t *testing.T) {
	first, replay := twoRecords(t)
	tx := replay.Transactions[1]

	// Scope pointer presence and value (the legacy transaction owns the
	// root execution scope).
	legacy := replay.Transactions[0]
	scope := legacy.Scopes[1]
	if !scopesEqual(&scope, &first.Transactions[0].Scopes[1]) {
		t.Fatalf("equal scopes differ")
	}
	bare := balance.RollbackScope{ScopeID: 1}
	if scopesEqual(&scope, &bare) {
		t.Fatalf("scope without parent presence passed")
	}
	bare2 := scope
	bare2.OwnerFrameID = nil // the real scope owns the root frame
	if scopesEqual(&scope, &bare2) {
		t.Fatalf("scope owner presence passed")
	}

	// Receipt presence arms.
	if err := receiptsEqual(tx.Receipt, first.Transactions[1].Receipt); err != nil {
		t.Fatalf("equal receipts differ: %v", err)
	}
	variation := *tx.Receipt
	variation.Status = nil
	root := mustHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	variation.PostStateRoot = &root
	if err := receiptsEqual(tx.Receipt, &variation); err == nil {
		t.Fatalf("status/root presence passed")
	}
	variation = *tx.Receipt
	variation.Result = ResultFailure
	if err := receiptsEqual(tx.Receipt, &variation); err == nil {
		t.Fatalf("result difference passed")
	}
	variation = *tx.Receipt
	gas := uint64(3)
	variation.GasUsed = &gas
	if err := receiptsEqual(tx.Receipt, &variation); err == nil {
		t.Fatalf("gas presence passed")
	}
	variation = *tx.Receipt
	price := fiveGwei
	variation.EffectiveGasPrice = &price
	if err := receiptsEqual(tx.Receipt, &variation); err == nil {
		t.Fatalf("price presence passed")
	}
	variation = *tx.Receipt
	variation.BlobGasUsed = &gas
	if err := receiptsEqual(tx.Receipt, &variation); err == nil {
		t.Fatalf("blob presence passed")
	}
	variation = *tx.Receipt
	variation.BlobGasPrice = &gas
	if err := receiptsEqual(tx.Receipt, &variation); err == nil {
		t.Fatalf("blob price presence passed")
	}
	variation = *tx.Receipt
	variation.Logs = variation.Logs[:0]
	if err := receiptsEqual(tx.Receipt, &variation); err == nil {
		t.Fatalf("log count difference passed")
	}
	variation = *tx.Receipt
	variation.SourceEncoding.Kind = 1
	if err := receiptsEqual(tx.Receipt, &variation); err == nil {
		t.Fatalf("encoding kind difference passed")
	}
	variation = *tx.Receipt
	variation.SourceEncoding.NativeType = nil
	if err := receiptsEqual(tx.Receipt, &variation); err == nil {
		t.Fatalf("native type presence passed")
	}
	variation = *tx.Receipt
	scopes := append([]SourceLogScope(nil), tx.Receipt.NetworkDetails.SourceLogScopes...)
	scopes[0].Scope = LogScopeSystemPost
	variation.NetworkDetails.SourceLogScopes = scopes
	if err := receiptsEqual(tx.Receipt, &variation); err == nil {
		t.Fatalf("network details difference passed")
	}
	if err := receiptsEqual(tx.Receipt, nil); err == nil {
		t.Fatalf("nil receipt passed")
	}

	// Action arms.
	action := first.Transactions[0].Actions[1]
	if !actionsEqual(&action, &replay.Transactions[0].Actions[1]) {
		t.Fatalf("equal actions differ")
	}
	clone := action
	clone.Amount = balance.UInt256FromUint64(1)
	if actionsEqual(&action, &clone) {
		t.Fatalf("amount difference passed")
	}
	clone = action
	clone.From = nil
	if actionsEqual(&action, &clone) {
		t.Fatalf("from presence passed")
	}
	clone = action
	clone.To = nil
	if actionsEqual(&action, &clone) {
		t.Fatalf("to presence passed")
	}
	clone = action
	clone.RollbackScope = nil
	if actionsEqual(&action, &clone) {
		t.Fatalf("scope presence passed")
	}
	clone = action
	clone.Effects = clone.Effects[:1]
	if actionsEqual(&action, &clone) {
		t.Fatalf("effect count passed")
	}

	// Transaction identity and state-sync arms.
	moved := *replay.Transactions[1]
	moved.Position = 9
	if err := txDeepEqual(replay.Transactions[1], &moved); err == nil {
		t.Fatalf("position difference passed")
	}
	moved = *replay.Transactions[1]
	moved.TransactionHash[0] ^= 0xff
	if err := txDeepEqual(replay.Transactions[1], &moved); err == nil {
		t.Fatalf("hash difference passed")
	}
	moved = *replay.Transactions[1]
	moved.StateSync = nil
	if err := txDeepEqual(replay.Transactions[1], &moved); err == nil {
		t.Fatalf("state-sync presence passed")
	}
	moved = *replay.Transactions[1]
	details := *moved.StateSync
	details.RecordTime = 1
	moved.StateSync = &details
	if err := txDeepEqual(replay.Transactions[1], &moved); err == nil {
		t.Fatalf("state-sync detail difference passed")
	}
	moved = *replay.Transactions[1]
	moved.Calls = moved.Calls[:2]
	if err := txDeepEqual(replay.Transactions[1], &moved); err == nil {
		t.Fatalf("list size difference passed")
	}
	if err := txDeepEqual(replay.Transactions[0], replay.Transactions[1]); err == nil {
		t.Fatalf("different transactions passed")
	}
}

func TestVerifyReplayPrefixGuards(t *testing.T) {
	first, replay := twoRecords(t)

	bound := *replay
	bound.Metadata.Hash = mustHash("0x8888888888888888888888888888888888888888888888888888888888888888")
	if err := VerifyReplayPrefix(first, &bound); err == nil {
		t.Fatalf("foreign block prefix passed")
	}

	// A missing receipt trips the bytes check.
	noReceipt := *replay
	noReceipt.Transactions[0].Receipt = nil
	if err := VerifyBytesStable(first, &noReceipt); err == nil {
		t.Fatalf("missing receipt passed")
	}

	// Legacy presence and content feed the record comparison.
	corpus := legacyCorpusBlock(t)
	legacyOn, err := NormalizeBlock(&corpus, legacyOpts())
	if err != nil {
		t.Fatalf("legacy on: %v", err)
	}
	legacyTwin, err := NormalizeBlock(&corpus, legacyOpts())
	if err != nil {
		t.Fatalf("legacy twin: %v", err)
	}
	if err := EqualSemantics(legacyOn, legacyTwin); err != nil {
		t.Fatalf("equal legacy records differ: %v", err)
	}
	legacyTwin.LegacyReceipt = nil
	if err := EqualSemantics(legacyOn, legacyTwin); err == nil {
		t.Fatalf("legacy presence difference passed")
	}
}

func TestBuildHeaderOptionalFields(t *testing.T) {
	capture := crossingBlock(t)
	root := mustHash("0x3100000000000000000000000000000000000000000000000000000000000031")
	requests := mustHash("0x3200000000000000000000000000000000000000000000000000000000000032")
	gas := uint64(5)
	excess := uint64(6)
	capture.Header.WithdrawalsRoot = &root
	capture.Header.BlobGasUsed = &gas
	capture.Header.ExcessBlobGas = &excess
	capture.Header.ParentBeaconBlockRoot = &root
	capture.Header.RequestsHash = &requests
	record, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	header := record.Metadata.Header
	if !header.WithdrawalsRoot.Present() || header.WithdrawalsRoot.MustGet() != root ||
		!header.BlobGasUsed.Present() || header.BlobGasUsed.MustGet() != 5 ||
		!header.ExcessBlobGas.Present() || header.ExcessBlobGas.MustGet() != 6 ||
		!header.ParentBeaconBlockRoot.Present() || !header.RequestsHash.Present() {
		t.Fatalf("optional header fields lost")
	}
	if !header.BaseFee.Present() {
		t.Fatalf("base fee lost")
	}
}
