// Package compatibility is the version-independent policy skeleton of
// E02-04.compatibility (#29): the read/replay/new-write capability matrix,
// the immutable-pending rule and the drain guard.
//
// The rules follow DELIVERY_STATE_MACHINE (preflight versions are checked
// before any writable mutation; a compatible reader republishes the old
// bytes; an incompatible persistent store format change is rejected before
// a write even with empty payload; there are no automatic migrations),
// CONTROL_WIRE_CONTRACT (an unknown version is a protocol incompatibility
// with no fallback success) and CONTEXT ("saved-state compatibility": a new
// version may continue old obligations, and completed delivery does not
// make an incompatible store compatible).
//
// This package defines no concrete encoding versions and no version
// constants. Concrete versions (the transport_version, record_format_version
// and sibling identifiers) are owned by E02-02.codec (#27) and register
// here through the EncodingVersion interface; nothing in this package
// imports the codec.
//
// The package imports only the Go standard library. It performs no I/O.
package compatibility

import "fmt"

// VersionID is the opaque identifier of one encoding version. The policy
// never parses, orders or assigns meaning to it: it compares identifiers
// for equality and prints them in errors. Concrete identifiers are defined
// by the codec module when E02-02.codec (#27) lands.
type VersionID string

// EncodingVersion is what a versioned encoding must expose so this policy
// can judge it. Implementations live in the codec module and register
// through Matrix.Register or NewMatrix.
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
