package profiles

import (
	"bytes"
	"testing"
)

func TestDefaultProfilesAreClosed(t *testing.T) {
	for _, family := range []Family{FamilyEth, FamilyBsc, FamilyBor} {
		p, err := Default(family)
		if err != nil {
			t.Fatalf("%s: %v", family, err)
		}
		if p.Registry != V1 {
			t.Fatalf("%s: registry must be v1, got %d", family, p.Registry)
		}
		if err := p.ValidateDetails(p.Details); err != nil {
			t.Fatalf("%s: own details must validate: %v", family, err)
		}
		for _, v := range p.Variants {
			if err := p.ValidateVariant(v); err != nil {
				t.Fatalf("%s: own variant %d must validate: %v", family, v, err)
			}
		}
	}
	if _, err := Default(Family(9)); err == nil {
		t.Fatal("unknown family must be a closed-registry error")
	}
}

func TestInvalidEnumIsNeverSuccess(t *testing.T) {
	p, _ := Default(FamilyEth)
	if err := p.ValidateVariant(VariantInvalid); err == nil {
		t.Fatal("variant 0 must be rejected")
	}
	enc := ReceiptEncoding{Kind: KindInvalid}
	if err := p.ValidateReceiptEncoding(enc, true); err == nil {
		t.Fatal("kind 0 must be rejected, not defaulted")
	}
	if err := p.ValidateDetails(DetailsInvalid); err == nil {
		t.Fatal("details 0 must be rejected")
	}
}

func TestCrossFamilyVariantIsClosed(t *testing.T) {
	eth, _ := Default(FamilyEth)
	if err := eth.ValidateVariant(VariantBorLegacyReceiptApplied); err == nil {
		t.Fatal("Bor-only variant must not be in the ETH closed set")
	}
	bsc, _ := Default(FamilyBsc)
	if err := bsc.ValidateVariant(VariantBorLegacyReceiptApplied); err == nil {
		t.Fatal("Bor-only variant must not be in the BSC closed set")
	}
	bor, _ := Default(FamilyBor)
	if err := bor.ValidateVariant(VariantBorLegacyReceiptApplied); err != nil {
		t.Fatalf("Bor variant must validate on Bor: %v", err)
	}
}

func TestReceiptEncodingTable(t *testing.T) {
	bor, _ := Default(FamilyBor)
	zero := uint32(0)
	if err := bor.ValidateReceiptEncoding(ReceiptEncoding{Kind: KindLegacyConsensusRLP, NativeType: &zero}, true); err != nil {
		t.Fatalf("kind 1 with native_type 0 must validate: %v", err)
	}
	if err := bor.ValidateReceiptEncoding(ReceiptEncoding{Kind: KindLegacyConsensusRLP}, true); err == nil {
		t.Fatal("kind 1 without native_type must be rejected")
	}
	oneTwentySeven := uint32(127)
	if err := bor.ValidateReceiptEncoding(ReceiptEncoding{Kind: KindTypedConsensusEnvelope, NativeType: &oneTwentySeven}, true); err != nil {
		t.Fatalf("kind 2 with native_type 127 (Bor StateSync) must validate: %v", err)
	}
	if err := bor.ValidateReceiptEncoding(ReceiptEncoding{Kind: KindTypedConsensusEnvelope, NativeType: &zero}, true); err == nil {
		t.Fatal("kind 2 with native_type 0 must be rejected")
	}
	eth, _ := Default(FamilyEth)
	oneTwentyEight := uint32(128)
	if err := eth.ValidateReceiptEncoding(ReceiptEncoding{Kind: KindTypedConsensusEnvelope, NativeType: &oneTwentyEight}, true); err == nil {
		t.Fatal("kind 2 with native_type 128 must be rejected (1..127)")
	}
	if err := eth.ValidateReceiptEncoding(ReceiptEncoding{Kind: KindBorLegacyStorageRLP}, true); err == nil {
		t.Fatal("kind 3 must be forbidden in body receipts")
	}
	if err := bor.ValidateReceiptEncoding(OptionalBorLegacy(), false); err != nil {
		t.Fatalf("kind 3 as optional Bor legacy must validate: %v", err)
	}
	if err := bor.ValidateReceiptEncoding(ReceiptEncoding{Kind: KindBorLegacyStorageRLP, NativeType: &zero}, false); err == nil {
		t.Fatal("kind 3 with native_type must be rejected")
	}
}

func TestNetworkDetailsOneOf(t *testing.T) {
	eth, _ := Default(FamilyEth)
	if err := eth.ValidateDetails(DetailsBsc); err == nil {
		t.Fatal("BSC details on an ETH profile must be rejected")
	}
	if err := eth.ValidateDetails(DetailsEth); err != nil {
		t.Fatalf("ETH details must validate: %v", err)
	}
}

func TestUnsupportedVersionNoFallback(t *testing.T) {
	p, _ := Default(FamilyEth)
	p.Registry = RegistryVersion(2)
	if err := p.ValidateDetails(DetailsEth); err == nil {
		t.Fatal("unsupported registry version must never fall back to success")
	}
	if err := p.ValidateVariant(VariantBlockApplied); err == nil {
		t.Fatal("unsupported registry version must reject variants too")
	}
	if err := p.ValidateReceiptEncoding(ReceiptEncoding{Kind: KindLegacyConsensusRLP, NativeType: new(uint32)}, true); err == nil {
		t.Fatal("unsupported registry version must reject encodings too")
	}
}

func TestSourcesBindingDigestDeterministic(t *testing.T) {
	keys := [][]byte{[]byte("k1"), []byte("k2")}
	a := ComputeSourcesBinding(V1, FamilyBor, "3a018e38", keys)
	b := ComputeSourcesBinding(V1, FamilyBor, "3a018e38", keys)
	if a != b {
		t.Fatal("same inputs must yield the same digest")
	}
	c := ComputeSourcesBinding(V1, FamilyBor, "3a018e39", keys)
	if a == c {
		t.Fatal("different source commit must change the digest")
	}
	d := ComputeSourcesBinding(V1, FamilyBor, "3a018e38", [][]byte{[]byte("k1"), []byte("k3")})
	if a == d {
		t.Fatal("different observation keys must change the digest")
	}
	e := ComputeSourcesBinding(V1, FamilyEth, "3a018e38", keys)
	if a == e {
		t.Fatal("different family must change the digest")
	}
	if bytes.Equal(a[:], make([]byte, 32)) {
		t.Fatal("digest must never be all-zero")
	}
}
