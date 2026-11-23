package poly

import (
	"errors"
	"testing"
)

func legacyOpts() Options {
	return Options{LegacyReceipt: true, DeclaredFixtures: []string{"POLY-L01"}}
}

func legacyCapture(t *testing.T) *LegacyReceiptCapture {
	t.Helper()
	capture := legacyCorpusBlock(t)
	return capture.LegacyReceipt
}

// TestLegacyDisabledByDefault: the optional scope is OFF by default and the
// record is refused; the default is the mandatory verified case.
func TestLegacyDisabledByDefault(t *testing.T) {
	capture := legacyCapture(t)
	if _, err := NormalizeLegacyReceipt(capture, Options{}); !errors.Is(err, ErrLegacyDisabled) {
		t.Fatalf("default OFF: got %v", err)
	}
	// Enabling without declared fixtures is an error, not a silent enable.
	if _, err := NormalizeLegacyReceipt(capture, Options{LegacyReceipt: true}); !errors.Is(err, ErrLegacyNotFixtured) {
		t.Fatalf("enable without fixtures: got %v", err)
	}
}

// TestLegacySourceStatesStayDistinct: absent, corrupt and present are three
// different source states; corrupt is never converted into empty success.
func TestLegacySourceStatesStayDistinct(t *testing.T) {
	if got, err := NormalizeLegacyReceipt(nil, legacyOpts()); err != nil || got != nil {
		t.Fatalf("absent source: %v %v", got, err)
	}
	capture := legacyCapture(t)
	absent := *capture
	absent.Present = false
	if got, err := NormalizeLegacyReceipt(&absent, legacyOpts()); err != nil || got != nil {
		t.Fatalf("absent record: %v %v", got, err)
	}

	corrupt := *capture
	corrupt.DecodeErr = errors.New("rlp: expected input list for types.ReceiptStorageRLP")
	if _, err := NormalizeLegacyReceipt(&corrupt, legacyOpts()); !errors.Is(err, ErrLegacySource) {
		t.Fatalf("corrupt record: got %v", err)
	}

	// A present record without logs contradicts the native writer.
	empty := *capture
	empty.Logs = nil
	if _, err := NormalizeLegacyReceipt(&empty, legacyOpts()); !errors.Is(err, ErrLegacySource) {
		t.Fatalf("log-free present record accepted: %v", err)
	}
}

// TestLegacyIdentityFormula: the derived identity follows the pinned native
// formula; an observed hash that does not match is rejected, not adopted.
func TestLegacyIdentityFormula(t *testing.T) {
	capture := legacyCapture(t)
	record, err := NormalizeLegacyReceipt(capture, legacyOpts())
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if record.SourceReceiptHash != DeriveBorReceiptHash(4, fixtureBlockHash) {
		t.Fatalf("identity differs from the formula")
	}
	if record.RawEncoding.Kind != 3 {
		t.Fatalf("raw encoding kind %d, want the storage kind 3", record.RawEncoding.Kind)
	}
	if record.RawEncoding.NativeType != nil {
		t.Fatalf("storage encoding must not carry a native type")
	}

	wrong := *capture
	wrong.ObservedHash = mustHash("0x9999999999999999999999999999999999999999999999999999999999999999")
	if _, err := NormalizeLegacyReceipt(&wrong, legacyOpts()); !errors.Is(err, ErrLegacySource) {
		t.Fatalf("wrong observed hash accepted: %v", err)
	}
}

// TestLegacyStorageEncoding: the storage RLP is decoded strictly as the
// storage consensus subset and cross-checked against the observed values.
func TestLegacyStorageEncoding(t *testing.T) {
	capture := legacyCapture(t)

	status := *capture
	status.Status = 0
	status.StorageRLP = rlpEncodeList(
		rlpEncodeBytes([]byte{0}),
		rlpEncodeUint(0),
		capture.StorageRLP[len(rlpEncodeList(rlpEncodeBytes([]byte{1}), rlpEncodeUint(0))):],
	)
	if _, err := NormalizeLegacyReceipt(&status, legacyOpts()); !errors.Is(err, ErrLegacySource) {
		t.Fatalf("non-success stored status accepted: %v", err)
	}

	cumulative := *capture
	cumulative.CumulativeGasUsed = 7
	if _, err := NormalizeLegacyReceipt(&cumulative, legacyOpts()); !errors.Is(err, ErrLegacySource) {
		t.Fatalf("cumulative mismatch accepted: %v", err)
	}

	truncated := *capture
	truncated.StorageRLP = capture.StorageRLP[:len(capture.StorageRLP)-2]
	if _, err := NormalizeLegacyReceipt(&truncated, legacyOpts()); !errors.Is(err, ErrLegacySource) {
		t.Fatalf("truncated storage accepted: %v", err)
	}
}

// TestLegacyLogPhasesDifferFromBodyScopes: the auxiliary per-log phases
// keep their own semantics; a malformed row is rejected but the phases
// never feed the D75 body provenance.
func TestLegacyLogPhasesDifferFromBodyScopes(t *testing.T) {
	capture := legacyCapture(t)
	record, err := NormalizeLegacyReceipt(capture, legacyOpts())
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(record.NativeLogPhases) != 1 || record.NativeLogPhases[0].Scope != LogScopeTXStateSync {
		t.Fatalf("auxiliary phases differ")
	}

	bad := *capture
	bad.LogPhases = []LogPhaseCapture{{TransactionLogIndex: 3, Phase: LogScopeSystemPre}}
	if _, err := NormalizeLegacyReceipt(&bad, legacyOpts()); !errors.Is(err, ErrLegacySource) {
		t.Fatalf("malformed phase accepted: %v", err)
	}
}
