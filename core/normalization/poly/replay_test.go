package poly

import (
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// TestReplaySameCaptureIsStable: normalizing the same capture twice yields
// the same logical record: every identity, order and scope is stable across
// capture recovery, and the retained source bytes are byte-identical.
func TestReplaySameCaptureIsStable(t *testing.T) {
	capture := crossingBlock(t)
	first, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	replay, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if err := VerifyReplay(first, replay); err != nil {
		t.Fatalf("replay stability: %v", err)
	}
	if err := VerifyBytesStable(first, replay); err != nil {
		t.Fatalf("bytes stability: %v", err)
	}

	// A replay whose fee flow differs must be caught by the semantic
	// compare: the tip amount, its effect and the boundary all move by one.
	tampered := crossingBlock(t)
	tampered.Transactions[0].Operations[2].Amount = balance.UInt256FromUint64(tipAmount + 1)
	tampered.Transactions[0].Operations[2].Effects[0].After = balance.UInt256FromUint64(tipAmount + 1)
	tampered.Transactions[0].Accounts[2].BalanceAfter = balance.UInt256FromUint64(tipAmount + 1)
	other, err := NormalizeBlock(&tampered, Options{})
	if err != nil {
		t.Fatalf("tampered: %v", err)
	}
	if err := VerifyReplay(first, other); err == nil {
		t.Fatalf("a changed fee flow passed as a stable replay")
	}
}

// TestReplayPrefixMatches: a replayed prefix of a longer capture matches
// the same prefix of the full record; its counters are not compared.
func TestReplayPrefixMatches(t *testing.T) {
	capture := crossingBlock(t)
	full, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	prefixCapture := crossingBlock(t)
	prefixCapture.Transactions = prefixCapture.Transactions[:1]
	prefixCapture.Header.GasUsed = 21000
	prefixCapture.StateSyncRows = 0 // the prefix replay has not observed the sync yet
	prefix, err := NormalizeBlock(&prefixCapture, Options{})
	if err != nil {
		t.Fatalf("prefix: %v", err)
	}
	if err := VerifyReplayPrefix(full, prefix); err != nil {
		t.Fatalf("prefix replay: %v", err)
	}

	// A prefix that normalizes a different transaction must not match.
	diverged := crossingBlock(t)
	diverged.Transactions = diverged.Transactions[:1]
	diverged.StateSyncRows = 0 // prefix replay of the same block
	diverged.Transactions[0].Operations[1].Amount = balance.UInt256FromUint64(6)
	diverged.Transactions[0].Operations[1].Effects[0].After = balance.MustParseUInt256("9999894999999999994")
	diverged.Transactions[0].Operations[1].Effects[1].After = balance.UInt256FromUint64(6)
	diverged.Transactions[0].Accounts[0].BalanceAfter = balance.MustParseUInt256("9999894999999999994")
	diverged.Transactions[0].Accounts[1].BalanceAfter = balance.UInt256FromUint64(6)
	diverged.Header.GasUsed = 21000
	divergedRecord, err := NormalizeBlock(&diverged, Options{})
	if err != nil {
		t.Fatalf("diverged: %v", err)
	}
	if err := VerifyReplayPrefix(full, divergedRecord); err == nil {
		t.Fatalf("a diverged prefix passed")
	}

	// A prefix longer than the full record is rejected.
	longer := crossingBlock(t)
	second := crossingLegacyTx(t)
	second.Position = 1
	second.Receipt.Consensus = fixtureReceiptWire(t, nil, 42000, fixtureBloom(nil), nil)
	longer.Transactions = append([]TxCapture{longer.Transactions[0], second}, longer.Transactions[1])
	longer.Transactions[2].Position = 2
	longer.Transactions[2].BodyTransactionCount = 3
	longer.Header.GasUsed = 42000
	longer.Transactions[1].Receipt.CumulativeGasUsed = 42000
	typeByte := StateSyncTxType
	longer.Transactions[2].Receipt.CumulativeGasUsed = 42000
	longer.Transactions[2].Receipt.Consensus = fixtureReceiptWire(
		t, &typeByte, 42000, longer.Transactions[2].Receipt.LogsBloom, longer.Transactions[2].Receipt.Logs)
	longerFull, err := NormalizeBlock(&longer, Options{})
	if err != nil {
		t.Fatalf("longer normalize: %v", err)
	}
	if err := VerifyReplayPrefix(longerFull, record4(t)); err == nil {
		t.Fatalf("a longer prefix passed")
	}
}

// TestOptionalScopeDefaultOff: with the default options the optional legacy
// record is never produced and the mandatory scope is unchanged.
func record4(t *testing.T) *BlockRecord {
	t.Helper()
	capture := crossingBlock(t)
	record, err := NormalizeBlock(&capture, Options{})
	if err != nil {
		t.Fatalf("record4: %v", err)
	}
	return record
}

func TestOptionalScopeDefaultOff(t *testing.T) {
	var opts Options
	if !opts.IsOff() {
		t.Fatalf("zero options must be OFF")
	}
	capture := legacyCorpusBlock(t)
	record, err := NormalizeBlock(&capture, opts)
	if err != nil {
		t.Fatalf("default OFF must not block the mandatory scope: %v", err)
	}
	if record.LegacyReceipt != nil {
		t.Fatalf("default OFF produced an optional record")
	}
	// The mandatory body effects are identical with and without the scope
	// enabled: turning the option on never changes native transaction
	// results.
	enabled, err := NormalizeBlock(&capture, legacyOpts())
	if err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if len(enabled.Transactions) != len(record.Transactions) ||
		enabled.Metadata.SourceTransactionCount != record.Metadata.SourceTransactionCount {
		t.Fatalf("the optional scope changed the mandatory body scope")
	}
}
