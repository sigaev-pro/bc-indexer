package receipt

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

func statusReceipt(t *testing.T, nativeType uint32) *Receipt {
	t.Helper()
	status := true
	gasUsed := uint64(21000)
	return &Receipt{
		Status:            &status,
		Result:            ResultSuccess,
		CumulativeGasUsed: 21000,
		GasUsed:           &gasUsed,
		Encoding:          mustEncoding(t, nativeType),
		Details:           &BscReceiptDetails{},
	}
}

func mustEncoding(t *testing.T, nativeType uint32) Encoding {
	t.Helper()
	enc, err := NewEncoding(nativeType, []byte{0xf6, 0x01})
	if err != nil {
		t.Fatalf("NewEncoding(%d): %v", nativeType, err)
	}
	return enc
}

func TestValidReceiptPasses(t *testing.T) {
	r := statusReceipt(t, 0)
	r.FeeSummary = ZeroFeeSummary()
	if err := r.Validate(); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	if r.Details.Validate() != nil {
		t.Fatal("empty BscReceiptDetails must validate")
	}
}

// D75: the empty variant is mandatory and explicit. A missing variant is
// an error, never a default success.
func TestDetailsVariantIsMandatory(t *testing.T) {
	r := statusReceipt(t, 0)
	r.Details = nil
	if !errors.Is(r.Validate(), ErrNoDetails) {
		t.Fatalf("missing details error = %v", r.Validate())
	}
}

func TestEncodingKindFollowsNativeType(t *testing.T) {
	for _, nativeType := range []uint32{0, 1, 2, 3, 4} {
		enc, err := NewEncoding(nativeType, []byte{0x01})
		if err != nil {
			t.Fatalf("type %d rejected: %v", nativeType, err)
		}
		if nativeType == 0 && enc.Kind != 1 {
			t.Fatalf("type 0 must use kind 1, got %d", uint8(enc.Kind))
		}
		if nativeType != 0 && enc.Kind != 2 {
			t.Fatalf("type %d must use kind 2, got %d", nativeType, uint8(enc.Kind))
		}
	}
	for _, nativeType := range []uint32{5, 42, 127, 128, 0xff} {
		if _, err := NewEncoding(nativeType, []byte{0x01}); !errors.Is(err, ErrNativeTypeRange) {
			t.Fatalf("type %d error = %v, want ErrNativeTypeRange", nativeType, err)
		}
	}
	if _, err := NewEncoding(0, nil); !errors.Is(err, ErrEmptyEncoding) {
		t.Fatalf("empty bytes error = %v, want ErrEmptyEncoding", err)
	}
}

// A Parlia system receipt keeps the wire type of its actual transaction:
// type 0 legacy in the retained fixtures, encoded with kind 1.
func TestSystemReceiptKeepsWireType(t *testing.T) {
	enc, err := NewEncoding(0, []byte{0xf6, 0x00})
	if err != nil {
		t.Fatalf("system receipt encoding rejected: %v", err)
	}
	if enc.NativeType != 0 {
		t.Fatalf("system receipt invented type %d", enc.NativeType)
	}
}

func TestOutcomeIsExactlyOne(t *testing.T) {
	r := statusReceipt(t, 1)
	root := block.Hash32{0xaa}
	r.PreStatusStateRoot = &root
	if !errors.Is(r.Validate(), ErrNoOutcome) {
		t.Fatalf("double outcome error = %v", r.Validate())
	}

	r = statusReceipt(t, 1)
	r.Status = nil
	r.PreStatusStateRoot = &root
	if err := r.Validate(); err != nil {
		t.Fatalf("pre-status outcome rejected: %v", err)
	}

	r = statusReceipt(t, 1)
	r.Status = nil
	if !errors.Is(r.Validate(), ErrNoOutcome) {
		t.Fatalf("missing outcome error = %v", r.Validate())
	}
}

func TestEncodingMustBePresentAndConsistent(t *testing.T) {
	r := statusReceipt(t, 2)
	r.Encoding.Bytes = nil
	if !errors.Is(r.Validate(), ErrNoEncoding) {
		t.Fatalf("missing encoding error = %v", r.Validate())
	}

	r = statusReceipt(t, 2)
	r.Encoding.Kind = 1 // wrong kind for typed receipt
	if !errors.Is(r.Validate(), ErrEncodingMismatch) {
		t.Fatalf("kind mismatch error = %v", r.Validate())
	}

	r = statusReceipt(t, 2)
	r.Encoding.NativeType = 9
	if !errors.Is(r.Validate(), ErrNativeTypeRange) {
		t.Fatalf("out-of-registry type error = %v", r.Validate())
	}
}

func TestZeroFeeSummaryIsExplicitZeros(t *testing.T) {
	s := ZeroFeeSummary()
	if s.Payer != nil {
		t.Fatal("a zero-fee system transaction has no applicable payer")
	}
	if !s.GasPurchaseDebit.IsZero() || !s.GasReturnCredit.IsZero() || !s.NetCharge.IsZero() || len(s.RecipientCredits) != 0 {
		t.Fatal("a zero-fee system transaction stores applicable explicit zeros")
	}
}

func TestGasUsedRange(t *testing.T) {
	r := statusReceipt(t, 0)
	over := uint64(999999)
	r.GasUsed = &over
	if !errors.Is(r.Validate(), ErrGasUsedRange) {
		t.Fatalf("gas range error = %v", r.Validate())
	}
}

func TestLogsKeepNativeShape(t *testing.T) {
	r := statusReceipt(t, 0)
	r.Logs = []Log{{TxLogIndex: 0, BlockLogIndex: 3, Topics: nil}}
	if err := r.Validate(); err != nil {
		t.Fatalf("committed log rejected: %v", err)
	}
}
