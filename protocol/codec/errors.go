// Named errors of the fragment codec. Every rejection wraps one of these
// sentinels and names the offending field, node, fragment or frame, so
// callers classify failures without matching free-form text.
package codec

import "errors"

var (
	// ErrMalformed rejects structurally broken fragment bytes: truncation,
	// unknown or out-of-order fields, duplicates of singular fields,
	// non-minimal varints, wrong wire types or leftover bytes.
	ErrMalformed = errors.New("codec: malformed canonical encoding")

	// ErrMissingField rejects an encoding that lacks a named scalar of the
	// pinned D73 tables, including required explicit zeros.
	ErrMissingField = errors.New("codec: missing required field")

	// ErrInvalidValue rejects a structurally valid encoding whose value is
	// outside its bounds: lengths outside 1..65536, zero part data, zero
	// counters, unknown registry values, wrong versions of nested
	// structures.
	ErrInvalidValue = errors.New("codec: invalid value")

	// ErrVersionUnsupported rejects an unknown record format or logical
	// schema version. There is no fallback success for unknown versions.
	ErrVersionUnsupported = errors.New("codec: unsupported version")

	// ErrClosedRegistry rejects an unknown family, kind or form value. The
	// registries are closed; there is no OTHER.
	ErrClosedRegistry = errors.New("codec: closed registry violation")

	// ErrOverflow rejects checked arithmetic on lengths, counts, sums or
	// offsets that would wrap. Counters never wrap or truncate.
	ErrOverflow = errors.New("codec: checked arithmetic overflow")

	// ErrTruncated rejects an input that ends before the expected bytes:
	// a short fragment payload, a missing part, a missing seal or an
	// unfinished tree.
	ErrTruncated = errors.New("codec: input truncated")

	// ErrDuplicate rejects a conflicting repeat: the same fragment or part
	// index delivered again with different bytes or a different digest.
	ErrDuplicate = errors.New("codec: conflicting duplicate frame")

	// ErrReordered rejects a gap or regression in fragment or part indices:
	// frames must arrive in publication order after deduplication.
	ErrReordered = errors.New("codec: frames out of order")

	// ErrDigestMismatch rejects bytes whose fragment, blob or record digest
	// differs from the expected digest.
	ErrDigestMismatch = errors.New("codec: digest mismatch")

	// ErrIdentityMismatch rejects frames or fragments of one record that
	// name different event ids, record digests, epochs or chain sequences.
	ErrIdentityMismatch = errors.New("codec: frame identities disagree")

	// ErrConflict rejects a payload that contradicts the stream position:
	// a form change of one fragment, a fragment count that differs between
	// frames, or a layout entry that does not belong to the saved plan.
	ErrConflict = errors.New("codec: frame conflicts with its stream")

	// ErrFragments rejects a fragment sequence that breaks the record
	// shape: a missing header or seal, items after blobs fragments, empty
	// items or blobs fragments, non-continuous indices, or counts and
	// length sums that disagree.
	ErrFragments = errors.New("codec: fragment sequence inconsistent")

	// ErrTree rejects a typed tree that breaks the D73 preorder rules:
	// non-continuous node ids, a parent at or after its child, a root with
	// a parent or placement fields, field tags out of ascending order,
	// element index gaps, child counts that disagree with the actual
	// children, or depth and breadth over the codec limits.
	ErrTree = errors.New("codec: typed tree inconsistent")

	// ErrBlobs rejects a blob chunk stream that breaks the chunk rules:
	// unknown blob ids, offsets not continuous from zero, chunk sizes other
	// than min(chunk data bound, remaining), extra bytes or chunks, or
	// lengths and digests that differ from the BlobRef.
	ErrBlobs = errors.New("codec: blob chunk stream inconsistent")

	// ErrUTF8 rejects a UTF-8 node whose assembled bytes are not valid
	// UTF-8. Validation is incremental; a chunk boundary may split one
	// multi-byte rune.
	ErrUTF8 = errors.New("codec: invalid UTF-8 in typed node")

	// ErrLimitIncompatible rejects a frame that cannot fit the transport
	// budget even though its layout entry is well formed — the codec-level
	// analog of the pre-SEALED frame size check (LAYOUT-01: nothing is
	// replanned or truncated here).
	ErrLimitIncompatible = errors.New("codec: frame budget too small")
)
