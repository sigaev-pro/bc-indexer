package poly

import (
	"bytes"
	"fmt"
	"reflect"
)

// The import-pressure area of the normalizer: normalization is a pure
// function of the capture, and capture recovery replays the same capture
// through the same normalizer. These checks prove the two properties the
// contract requires: the replayed record keeps every logical identity,
// order and scope of the first normalization (it never continues counters
// past already repeated operations), and a replayed prefix of a longer
// capture matches the same prefix of the full record.

// EqualSemantics checks that two normalized block records are the same
// logical record: metadata, family details, finality references, every
// transaction with all IDs, execution orders, scopes, actions and receipt
// bytes, and the optional legacy record. Byte slices compare by content,
// pointers by pointee, so a fresh normalization of the same capture passes.
func EqualSemantics(a, b *BlockRecord) error {
	if a == nil || b == nil {
		return fmt.Errorf("nil record")
	}
	if !reflect.DeepEqual(a.Metadata, b.Metadata) {
		return fmt.Errorf("block metadata differs")
	}
	if len(a.Finality.HeimdallCheckpoints) != len(b.Finality.HeimdallCheckpoints) {
		return fmt.Errorf("finality references differ")
	}
	for i := range a.Finality.HeimdallCheckpoints {
		if !reflect.DeepEqual(a.Finality.HeimdallCheckpoints[i], b.Finality.HeimdallCheckpoints[i]) {
			return fmt.Errorf("finality reference %d differs", i)
		}
	}
	if len(a.Transactions) != len(b.Transactions) {
		return fmt.Errorf("transaction counts differ: %d vs %d", len(a.Transactions), len(b.Transactions))
	}
	for i := range a.Transactions {
		if err := txDeepEqual(a.Transactions[i], b.Transactions[i]); err != nil {
			return err
		}
	}
	if (a.LegacyReceipt == nil) != (b.LegacyReceipt == nil) {
		return fmt.Errorf("legacy receipt presence differs")
	}
	if a.LegacyReceipt != nil && !reflect.DeepEqual(a.LegacyReceipt, b.LegacyReceipt) {
		return fmt.Errorf("legacy receipt differs")
	}
	return nil
}

// VerifyReplay checks a recovered capture of the same block against the
// first normalization: the replay must reproduce the same record exactly.
// This is the capture-recovery stability rule of E04-04: the same corpus
// keeps the same logical IDs, orders and scopes, and repeated operations
// never extend the counters of the first pass.
func VerifyReplay(first, replay *BlockRecord) error {
	if err := EqualSemantics(first, replay); err != nil {
		return fmt.Errorf("replay differs: %w", err)
	}
	return nil
}

// VerifyReplayPrefix checks that a replayed prefix of a longer capture
// matches the corresponding prefix of the full record. A prefix replay that
// agrees on every already repeated operation is consistent; the counters of
// the remaining operations are not compared, because the replay has not
// observed them yet.
func VerifyReplayPrefix(full, prefix *BlockRecord) error {
	if len(prefix.Transactions) > len(full.Transactions) {
		return fmt.Errorf("prefix replay has %d transactions, more than the full %d",
			len(prefix.Transactions), len(full.Transactions))
	}
	if prefix.Metadata.Number != full.Metadata.Number || prefix.Metadata.Hash != full.Metadata.Hash {
		return fmt.Errorf("prefix replay is bound to another block")
	}
	for i := range prefix.Transactions {
		if err := txDeepEqual(prefix.Transactions[i], full.Transactions[i]); err != nil {
			return fmt.Errorf("prefix replay: %w", err)
		}
	}
	return nil
}

// VerifyBytesStable checks that the retained source bytes of two records
// are byte-identical: after READY the saved bytes are the record, and a
// later regeneration never substitutes for them (D76).
func VerifyBytesStable(a, b *BlockRecord) error {
	if len(a.Transactions) != len(b.Transactions) {
		return fmt.Errorf("transaction counts differ")
	}
	for i := range a.Transactions {
		x, y := a.Transactions[i], b.Transactions[i]
		if !bytes.Equal(x.RawEncoding.Bytes, y.RawEncoding.Bytes) {
			return fmt.Errorf("raw encoding bytes differ at position %d", i)
		}
		if x.Receipt == nil || y.Receipt == nil {
			return fmt.Errorf("missing receipt at position %d", i)
		}
		if !bytes.Equal(x.Receipt.SourceEncoding.Bytes, y.Receipt.SourceEncoding.Bytes) {
			return fmt.Errorf("receipt consensus bytes differ at position %d", i)
		}
	}
	return nil
}
