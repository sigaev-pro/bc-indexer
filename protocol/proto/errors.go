// Named errors of the schema layer. Every rejection carries one of these
// sentinels and a specific reason naming the offending field or record, so
// callers classify failures without matching free-form text.
package proto

import "errors"

var (
	// ErrMalformed rejects structurally broken wire bytes: truncation,
	// non-minimal varints, wrong wire types or leftover bytes.
	ErrMalformed = errors.New("proto: malformed canonical encoding")

	// ErrUnknownField rejects a field number outside the pinned v2 table.
	ErrUnknownField = errors.New("proto: unknown field number")

	// ErrDuplicateField rejects a field that appears more than once.
	ErrDuplicateField = errors.New("proto: duplicate field")

	// ErrFieldOrder rejects fields that are not in ascending tag order.
	ErrFieldOrder = errors.New("proto: fields not in ascending tag order")

	// ErrMissingField rejects an encoding that lacks a named scalar,
	// including required explicit zeros such as fragment_index 0.
	ErrMissingField = errors.New("proto: missing required field")

	// ErrInvalidValue rejects a structurally valid encoding whose value is
	// outside its bounds (lengths, oneof exclusivity, zero identities).
	ErrInvalidValue = errors.New("proto: invalid value")

	// ErrVersionUnsupported rejects an unknown transport, record-format or
	// layout version. There is no fallback success for unknown versions.
	ErrVersionUnsupported = errors.New("proto: unsupported version")

	// ErrClosedRegistry rejects an unknown family, kind, variant or
	// profile value. The registries are closed; there is no OTHER.
	ErrClosedRegistry = errors.New("proto: closed registry violation")

	// ErrOverflow rejects checked arithmetic that would wrap. Counters,
	// lengths, sums and offsets never wrap or truncate.
	ErrOverflow = errors.New("proto: checked arithmetic overflow")

	// ErrLimitIncompatible rejects a frame budget that cannot carry even a
	// one-byte part (LAYOUT-01: capacity C below 1).
	ErrLimitIncompatible = errors.New("proto: frame budget too small for any part")

	// ErrFragments rejects a fragment list that breaks the READY manifest
	// rules: non-continuous indices, out-of-bounds lengths or a length sum
	// that differs from record_length.
	ErrFragments = errors.New("proto: fragment descriptors inconsistent")

	// ErrIdentityMismatch rejects an envelope whose parts name different
	// identities (event id vs semantic activation, block record vs
	// descriptor).
	ErrIdentityMismatch = errors.New("proto: identity mismatch between envelope parts")

	// ErrConflict rejects a payload part that belongs to another family or
	// kind than the semantic descriptor declares.
	ErrConflict = errors.New("proto: payload conflicts with the semantic descriptor")

	// ErrVariantMismatch rejects a payload variant that is not the closed
	// family/kind variant of the descriptor.
	ErrVariantMismatch = errors.New("proto: payload variant does not match family and kind")

	// ErrSemantic wraps a semantic.SemanticDescriptor rejection.
	ErrSemantic = errors.New("proto: semantic descriptor invalid")

	// ErrPayload wraps a rejection of a typed phase-1 payload view.
	ErrPayload = errors.New("proto: envelope payload invalid")
)
