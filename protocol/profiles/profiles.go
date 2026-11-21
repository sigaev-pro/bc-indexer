// Package profiles implements the closed registry of payload variants,
// receipt encodings and network detail profiles for E02-10.closed_profiles
// (#25). The registry is normative v1: adding a variant is a versioned
// registry change, never a silent extension, and an unsupported version
// never falls back to Any.
package profiles

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// RegistryVersion identifies the closed registry revision. Unknown
// versions are rejected, not defaulted.
type RegistryVersion uint16

const V1 RegistryVersion = 1

// Family is one of the three E01-proven execution families.
type Family uint8

const (
	FamilyEth Family = 1
	FamilyBsc Family = 2
	FamilyBor Family = 3
)

func (f Family) String() string {
	switch f {
	case FamilyEth:
		return "ETH"
	case FamilyBsc:
		return "BSC"
	case FamilyBor:
		return "BOR"
	default:
		return fmt.Sprintf("Family(%d)", uint8(f))
	}
}

// ReceiptEncodingKind is the closed D75 receipt encoding enum. Zero is
// reserved INVALID and never a default success.
type ReceiptEncodingKind uint8

const (
	KindInvalid                ReceiptEncodingKind = 0
	KindLegacyConsensusRLP     ReceiptEncodingKind = 1
	KindTypedConsensusEnvelope ReceiptEncodingKind = 2
	KindBorLegacyStorageRLP    ReceiptEncodingKind = 3
)

// NativeTypeMax bounds the typed envelope native_type per D75 (1..127).
const NativeTypeMax uint32 = 127

// NetworkDetails is the closed D75 receipt network-details oneof: a
// profile must select exactly one variant; unknown extras are errors.
type NetworkDetails uint8

const (
	DetailsInvalid NetworkDetails = 0
	DetailsEth     NetworkDetails = 1
	DetailsBsc     NetworkDetails = 2
	DetailsBor     NetworkDetails = 3
)

// PayloadVariant names a closed event payload type from MESSAGE_CONTRACT
// section 4. BlockComplete and CoverageUpdated are excluded by D25 and
// have no variant here.
type PayloadVariant uint8

const (
	VariantInvalid                       PayloadVariant = 0
	VariantTransactionApplied            PayloadVariant = 1
	VariantTransactionRemoved            PayloadVariant = 2
	VariantTransactionStatusChanged      PayloadVariant = 3
	VariantBlockApplied                  PayloadVariant = 4
	VariantBlockRemoved                  PayloadVariant = 5
	VariantBlockStatusChanged            PayloadVariant = 6
	VariantSystemBalanceApplied          PayloadVariant = 7
	VariantSystemBalanceRemoved          PayloadVariant = 8
	VariantSystemBalanceStatusChanged    PayloadVariant = 9
	VariantBorLegacyReceiptApplied       PayloadVariant = 10
	VariantBorLegacyReceiptRemoved       PayloadVariant = 11
	VariantBorLegacyReceiptStatusChanged PayloadVariant = 12
)

// ReceiptEncoding is the D75 SourceReceiptEncoding row: kind, optional
// native_type and exact byte contract. Presence of native_type is fixed
// by the table, not by the caller.
type ReceiptEncoding struct {
	Kind       ReceiptEncodingKind
	NativeType *uint32 // present iff the table requires it; nil otherwise
}

// Profile is one closed entry: the payload variants a family may emit,
// which receipt encodings apply and which network-details variant its
// body receipts carry.
type Profile struct {
	Registry RegistryVersion
	Family   Family
	Variants []PayloadVariant
	Receipts []ReceiptEncoding
	Details  NetworkDetails
}

// ErrClosedRegistry marks any attempt to use an unknown version, family,
// kind, variant or an applicability violation. The registry never
// silently extends and never falls back to Any.
type ErrClosedRegistry struct{ Reason string }

func (e *ErrClosedRegistry) Error() string { return "profiles: closed registry: " + e.Reason }

func closed(reason string) error { return &ErrClosedRegistry{Reason: reason} }

// Default returns the closed v1 profile for a family. The returned
// receipts are the table rows applicable to the family's mandatory body
// transactions; the optional Bor legacy storage encoding (kind 3) is only
// reachable through OptionalBorLegacy because it never applies to body
// receipts.
func Default(f Family) (Profile, error) {
	body := []ReceiptEncoding{
		{Kind: KindLegacyConsensusRLP, NativeType: uint32p(0)},
		{Kind: KindTypedConsensusEnvelope, NativeType: nil},
	}
	variantsFor := func(f Family) []PayloadVariant {
		base := []PayloadVariant{
			VariantTransactionApplied, VariantTransactionRemoved,
			VariantTransactionStatusChanged, VariantBlockApplied,
			VariantBlockRemoved, VariantBlockStatusChanged,
		}
		if f == FamilyBor {
			return append(base,
				VariantSystemBalanceApplied, VariantSystemBalanceRemoved,
				VariantSystemBalanceStatusChanged,
				VariantBorLegacyReceiptApplied, VariantBorLegacyReceiptRemoved,
				VariantBorLegacyReceiptStatusChanged)
		}
		if f == FamilyBsc {
			return append(base,
				VariantSystemBalanceApplied, VariantSystemBalanceRemoved,
				VariantSystemBalanceStatusChanged)
		}
		return base
	}
	switch f {
	case FamilyEth:
		return Profile{Registry: V1, Family: f, Variants: variantsFor(f), Receipts: body, Details: DetailsEth}, nil
	case FamilyBsc:
		return Profile{Registry: V1, Family: f, Variants: variantsFor(f), Receipts: body, Details: DetailsBsc}, nil
	case FamilyBor:
		return Profile{Registry: V1, Family: f, Variants: variantsFor(f), Receipts: bodyReceiptsBor(), Details: DetailsBor}, nil
	}
	return Profile{}, closed(fmt.Sprintf("unknown family %d", uint8(f)))
}

func bodyReceiptsBor() []ReceiptEncoding {
	// Bor body receipts are kinds 1/2 by actual type; StateSync 0x7f uses
	// kind 2 with native_type 127 per D75.
	oneTwentySeven := uint32(127)
	return []ReceiptEncoding{
		{Kind: KindLegacyConsensusRLP, NativeType: uint32p(0)},
		{Kind: KindTypedConsensusEnvelope, NativeType: nil},
		{Kind: KindTypedConsensusEnvelope, NativeType: &oneTwentySeven},
	}
}

// OptionalBorLegacy returns the optional Bor legacy storage encoding
// (kind 3, no native_type). It applies only to the
// BorLegacyReceiptRecord auxiliary origin, never to body receipts.
func OptionalBorLegacy() ReceiptEncoding {
	return ReceiptEncoding{Kind: KindBorLegacyStorageRLP, NativeType: nil}
}

// SourcesBindingDigest implements the D80 mandatory Hash32: the digest
// binding a source observation reference (or an L1 checkpoint) to its
// exact source set. It is mandatory in SourceObservationRef and
// L1Checkpoint and is computed over the domain-separated canonical
// encoding of the reference's inputs.
type SourcesBindingDigest [32]byte

// ComputeSourcesBinding derives the digest for a source observation: the
// registry version, family, the source commit identity and the ordered
// observation keys. The same inputs always yield the same digest; any
// input change changes the digest.
func ComputeSourcesBinding(registry RegistryVersion, family Family, sourceCommit string, observationKeys [][]byte) SourcesBindingDigest {
	h := sha256.New()
	h.Write([]byte("extractor.sources_binding.v1"))
	var header [8]byte
	binary.BigEndian.PutUint16(header[0:2], uint16(registry))
	header[2] = byte(family)
	h.Write(header[:3])
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(sourceCommit)))
	h.Write(lenBuf[:])
	h.Write([]byte(sourceCommit))
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(observationKeys)))
	h.Write(lenBuf[:])
	for _, key := range observationKeys {
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(key)))
		h.Write(lenBuf[:])
		h.Write(key)
	}
	var out SourcesBindingDigest
	copy(out[:], h.Sum(nil))
	return out
}

// ValidateReceiptEncoding checks one encoding against the closed table:
// kind applicability, native_type presence and range.
func (p Profile) ValidateReceiptEncoding(enc ReceiptEncoding, bodyReceipt bool) error {
	if p.Registry != V1 {
		return closed(fmt.Sprintf("unsupported registry version %d", uint16(p.Registry)))
	}
	switch enc.Kind {
	case KindInvalid:
		return closed("kind 0 is INVALID, not a default success")
	case KindLegacyConsensusRLP:
		if enc.NativeType == nil || *enc.NativeType != 0 {
			return closed("kind 1 requires native_type present and equal to 0")
		}
	case KindTypedConsensusEnvelope:
		if enc.NativeType == nil {
			// The profile table row without a pinned native_type accepts
			// any registry-allowed 1..127 value; presence on the wire is
			// checked by the schema validator, here nil means "table row".
			return nil
		}
		if *enc.NativeType == 0 || *enc.NativeType > NativeTypeMax {
			return closed(fmt.Sprintf("kind 2 native_type %d outside 1..127", *enc.NativeType))
		}
		if p.Family == FamilyBor && *enc.NativeType == 127 {
			return nil // Bor StateSync body encoding per D75
		}
	case KindBorLegacyStorageRLP:
		if enc.NativeType != nil {
			return closed("kind 3 must not carry native_type")
		}
		if bodyReceipt {
			return closed("kind 3 is forbidden in body receipts")
		}
		if p.Family != FamilyBor {
			return closed("kind 3 applies only to the Bor family")
		}
		return nil
	default:
		return closed(fmt.Sprintf("unknown kind %d", uint8(enc.Kind)))
	}
	return nil
}

// ValidateVariant checks that a payload variant belongs to the family's
// closed set.
func (p Profile) ValidateVariant(v PayloadVariant) error {
	if p.Registry != V1 {
		return closed(fmt.Sprintf("unsupported registry version %d", uint16(p.Registry)))
	}
	if v == VariantInvalid {
		return closed("variant 0 is INVALID")
	}
	for _, allowed := range p.Variants {
		if v == allowed {
			return nil
		}
	}
	return closed(fmt.Sprintf("variant %d not in the closed %s set", uint8(v), p.Family))
}

// ValidateDetails checks the network-details oneof: exactly the family's
// variant is valid; every other selection (including "empty") is closed.
func (p Profile) ValidateDetails(d NetworkDetails) error {
	if p.Registry != V1 {
		return closed(fmt.Sprintf("unsupported registry version %d", uint16(p.Registry)))
	}
	switch p.Family {
	case FamilyEth:
		if d == DetailsEth {
			return nil
		}
	case FamilyBsc:
		if d == DetailsBsc {
			return nil
		}
	case FamilyBor:
		if d == DetailsBor {
			return nil
		}
	default:
		return closed(fmt.Sprintf("unknown family %d", uint8(p.Family)))
	}
	return closed(fmt.Sprintf("details variant %d is not the closed %s variant", uint8(d), p.Family))
}

func uint32p(v uint32) *uint32 { return &v }
