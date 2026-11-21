package profiles

import "testing"

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

func TestSourcesBindingDigestIsSHA256OfFileBytes(t *testing.T) {
	file := []byte(`{"schema_version":1,"l1":{"deployment_id":"01"}}`)
	a := ComputeSourcesBindingDigest(file)
	b := ComputeSourcesBindingDigest(file)
	if a != b {
		t.Fatal("same file bytes must yield the same digest")
	}
	ws := append(append([]byte{}, file...), ' ')
	if a == ComputeSourcesBindingDigest(ws) {
		t.Fatal("whitespace change must change the digest (D80)")
	}
	if a == (SourcesBindingDigest{}) {
		t.Fatal("digest must never be all-zero")
	}
}

func TestSourceObservationRefMandatoryDigest(t *testing.T) {
	file := []byte("sources")
	ref := SourceObservationRef{DeploymentID: "d1", Digest: ComputeSourcesBindingDigest(file)}
	if err := ref.Validate(file); err != nil {
		t.Fatalf("matching digest must validate: %v", err)
	}
	if err := ref.Validate([]byte("other")); err == nil {
		t.Fatal("mismatched file bytes must be ErrDigestMismatch")
	}
	if err := ref.Validate(nil); err == nil {
		t.Fatal("missing file bytes must be ErrDigestMissing")
	}
	var zero SourceObservationRef
	if err := zero.Validate(file); err == nil {
		t.Fatal("zero digest must not validate against real bytes")
	}
}

func TestL1CheckpointMandatoryDigest(t *testing.T) {
	file := []byte("sources")
	cp := L1Checkpoint{Number: 4, Time: 5, Digest: ComputeSourcesBindingDigest(file)}
	if err := cp.Validate(file); err != nil {
		t.Fatalf("matching digest must validate: %v", err)
	}
	if err := cp.Validate([]byte("x")); err == nil {
		t.Fatal("mismatch must be rejected")
	}
	if err := (L1Checkpoint{}).Validate(nil); err == nil {
		t.Fatal("missing file must be rejected")
	}
}

func TestKind2RequiresNativeType(t *testing.T) {
	eth, _ := Default(FamilyEth)
	if err := eth.ValidateReceiptEncoding(ReceiptEncoding{Kind: KindTypedConsensusEnvelope}, true); err == nil {
		t.Fatal("kind 2 with missing native_type must be rejected (AC-DAT-02-03)")
	}
}

func TestBodyKindsForbiddenInAuxiliarySlot(t *testing.T) {
	bor, _ := Default(FamilyBor)
	zero := uint32(0)
	one := uint32(1)
	if err := bor.ValidateReceiptEncoding(ReceiptEncoding{Kind: KindLegacyConsensusRLP, NativeType: &zero}, false); err == nil {
		t.Fatal("kind 1 must be forbidden in the auxiliary slot")
	}
	if err := bor.ValidateReceiptEncoding(ReceiptEncoding{Kind: KindTypedConsensusEnvelope, NativeType: &one}, false); err == nil {
		t.Fatal("kind 2 must be forbidden in the auxiliary slot")
	}
	if err := bor.ValidateReceiptEncoding(OptionalBorLegacy(), false); err != nil {
		t.Fatalf("kind 3 in the auxiliary slot must validate: %v", err)
	}
}

func TestOriginEnumClosed(t *testing.T) {
	if OriginInvalid != 0 || OriginBodyMandatory != 1 || OriginBorLegacySynthetic != 2 {
		t.Fatal("origin enum values must stay closed v1")
	}
}
