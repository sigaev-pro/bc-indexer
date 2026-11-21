// Package compatibility is the version policy of E02-04.compatibility
// (#29): the read/replay/new-write capability matrix, the wire/plan
// versus persistent-store transition rules, the immutable-pending rule
// and the drain guard, plus the concrete encoding versions pinned by the
// proto registry.
//
// The rules follow DELIVERY_STATE_MACHINE (preflight versions are checked
// before any writable mutation; a compatible reader republishes the old
// bytes; an incompatible wire/plan upgrade requires DRAINED by the old
// build; an incompatible persistent store format change is rejected
// before a write even with empty payload; there are no automatic
// migrations), CONTROL_WIRE_CONTRACT (an unknown version is a protocol
// incompatibility with no fallback success) and CONTEXT ("saved-state
// compatibility": a new version may continue old obligations, and
// completed delivery does not make an incompatible store compatible).
//
// The concrete ProtocolVersion values mirror the version constants
// pinned in protocol/proto/registry.go (E02-02.schemas #26) and are
// registered per kind in DefaultRegistry; this module stays stdlib-only
// and does not import the proto module. Further versions — including
// the encodings owned by E02-02.codec (#27) — register through the
// EncodingVersion interface; nothing in this package imports the codec.
//
// The package imports only the Go standard library. It performs no I/O.
package compatibility

import "fmt"

// VersionID is the opaque identifier of one encoding version. The policy
// never parses, orders or assigns meaning to it: it compares identifiers
// for equality and prints them in errors. Concrete identifiers are
// defined by this package (the pinned proto versions) and by the codec
// module for its own encodings.
type VersionID string

// EncodingVersion is what a versioned encoding must expose so this policy
// can judge it. Implementations register through Matrix.Register or
// NewMatrix; this package's ProtocolVersion is the concrete
// implementation of the pinned proto versions.
type EncodingVersion interface {
	// ID returns the opaque, stable identifier of this encoding version.
	// Two versions are the same version iff their IDs compare equal.
	ID() VersionID

	// Reads reports whether a process running this version can decode
	// the records and metadata encoded by the version identified by from.
	// This predicate is the compatibility claim under test: the policy
	// never assumes that a newer version reads an older one, and claims
	// are per pair, never transitive.
	Reads(from VersionID) bool

	// WritesNew reports whether new records may be created in this
	// version. A version can be registered as readable so pending
	// obligations survive, while remaining retired for new writes.
	WritesNew() bool
}

// ErrInvalidVersion marks a structurally invalid registration: a nil
// implementation or an empty identifier. Registration never repairs or
// defaults such input.
type ErrInvalidVersion struct{ Reason string }

func (e *ErrInvalidVersion) Error() string {
	return fmt.Sprintf("compatibility: invalid version registration: %s", e.Reason)
}

// ErrDuplicateVersion marks an attempt to register a second version under
// an identifier that is already registered. The first registration stands.
type ErrDuplicateVersion struct{ ID VersionID }

func (e *ErrDuplicateVersion) Error() string {
	return fmt.Sprintf("compatibility: version %q is already registered", e.ID)
}

// ErrUnknownVersion marks any decision over a version that is not
// registered. An unknown version is a protocol incompatibility that
// preserves the data; there is no fallback success.
type ErrUnknownVersion struct{ ID VersionID }

func (e *ErrUnknownVersion) Error() string {
	return fmt.Sprintf("compatibility: version %q is not registered (unknown versions are protocol incompatibilities, not fallbacks)", e.ID)
}
