// The closed registries the schema layer composes: the event family and kind
// registry v1 of the READY descriptor, the transport form, the pinned
// versions of the v2 contract, and the mapping onto the phase-1 models
// (semantic identities, profiles payload variants). Unknown values are
// errors; there is no OTHER.
package proto

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// Family is the event family of registry v1
// (tx=1, block=2, balance.system=3, bor.legacy_receipt=4; the last two are
// optional families gated by the network profile).
type Family uint16

// Event families of registry v1.
const (
	FamilyTx              Family = 1
	FamilyBlock           Family = 2
	FamilyBalanceSystem   Family = 3
	FamilyBorLegacy       Family = 4
	familyRegistryHighest        = FamilyBorLegacy
)

// Valid reports whether f is a registered family value.
func (f Family) Valid() bool {
	return f >= FamilyTx && f <= familyRegistryHighest
}

// String returns the registry name of f.
func (f Family) String() string {
	switch f {
	case FamilyTx:
		return "tx"
	case FamilyBlock:
		return "block"
	case FamilyBalanceSystem:
		return "balance.system"
	case FamilyBorLegacy:
		return "bor.legacy_receipt"
	}
	return fmt.Sprintf("Family(%d)", uint16(f))
}

// Kind is the lifecycle event kind of registry v1
// (applied=1, removed=2, status=3).
type Kind uint16

// Lifecycle event kinds of registry v1.
const (
	KindApplied Kind = 1
	KindRemoved Kind = 2
	KindStatus  Kind = 3
	kindHighest      = KindStatus
)

// Valid reports whether k is a registered kind value.
func (k Kind) Valid() bool {
	return k >= KindApplied && k <= kindHighest
}

// String returns the registry name of k.
func (k Kind) String() string {
	switch k {
	case KindApplied:
		return "applied"
	case KindRemoved:
		return "removed"
	case KindStatus:
		return "status"
	}
	return fmt.Sprintf("Kind(%d)", uint16(k))
}

// Form is the transport form of one RecordFragment: a complete fragment or
// one part of it. The Nats-Msg-Id tuple encodes COMPLETE as 0 and PART as 1.
type Form byte

// Transport forms.
const (
	FormComplete Form = 0
	FormPart     Form = 1
)

// Valid reports whether f is a registered form.
func (f Form) Valid() bool { return f == FormComplete || f == FormPart }

// String returns the registry name of f.
func (f Form) String() string {
	switch f {
	case FormComplete:
		return "COMPLETE"
	case FormPart:
		return "PART"
	}
	return fmt.Sprintf("Form(%d)", byte(f))
}

// Version pins of the v2 wire contract (D73: logical schema_version=2;
// record_format_version=1; transport/layout/header/control/manifest/
// reservation=2). The reservation seed carries these values; decoders reject
// any other value instead of guessing.
const (
	// TransportVersion is the TransportFrame wrapper version.
	TransportVersion uint32 = 2
	// RecordFormatVersion is the RecordFragment format version.
	RecordFormatVersion uint32 = 1
	// LayoutVersion is the transport layout version.
	LayoutVersion uint32 = 2
	// HeaderVersion is the immutable header contract version.
	HeaderVersion uint32 = 2
	// ControlVersion is the control wire protocol version.
	ControlVersion uint32 = 2
	// ManifestVersion is the READY manifest version.
	ManifestVersion uint32 = 2
)

// FragmentMaxBytes bounds one serialized RecordFragment, including its
// wrapper: 1..65536 bytes (D73, F11). The bound applies to complete
// fragments, to fragment_total_bytes and to every part data field.
const FragmentMaxBytes = 65536

// familyFromSemantic maps the phase-1 semantic family onto the wire registry
// value. The two registries carry the same closed set; an unmapped value is
// a closed-registry error, never a passthrough.
func familyFromSemantic(f semantic.Family) (Family, error) {
	switch f {
	case semantic.FamilyTx:
		return FamilyTx, nil
	case semantic.FamilyBlock:
		return FamilyBlock, nil
	case semantic.FamilyBalanceSystem:
		return FamilyBalanceSystem, nil
	case semantic.FamilyBorLegacy:
		return FamilyBorLegacy, nil
	}
	return 0, fmt.Errorf("%w: semantic family %s is not in registry v1", ErrClosedRegistry, f)
}

// kindFromSemantic maps the phase-1 semantic kind onto the wire registry
// value.
func kindFromSemantic(k semantic.Kind) (Kind, error) {
	switch k {
	case semantic.KindApplied:
		return KindApplied, nil
	case semantic.KindRemoved:
		return KindRemoved, nil
	case semantic.KindStatus:
		return KindStatus, nil
	}
	return 0, fmt.Errorf("%w: semantic kind %s is not in registry v1", ErrClosedRegistry, k)
}

// variantFor returns the closed payload variant of a family/kind pair: the
// registry v1 table of MESSAGE_CONTRACT section 7 mirrored by the phase-1
// profiles package (tx applied/removed/status = 1/2/3, block = 4/5/6,
// balance.system = 7/8/9, bor.legacy_receipt = 10/11/12).
func variantFor(f Family, k Kind) (profiles.PayloadVariant, error) {
	if !f.Valid() {
		return 0, fmt.Errorf("%w: family %d is not in registry v1", ErrClosedRegistry, uint16(f))
	}
	if !k.Valid() {
		return 0, fmt.Errorf("%w: kind %d is not in registry v1", ErrClosedRegistry, uint16(k))
	}
	base := uint8(f-1) * 3 // families group three kinds in registry order
	return profiles.PayloadVariant(base + uint8(k)), nil
}
