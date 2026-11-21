package compatibility

import (
	"fmt"
	"sync"
)

// ErrReadUnsupported marks a rejected read capability: the target version
// does not claim to read the source version. Both versions are named.
type ErrReadUnsupported struct{ From, To VersionID }

func (e *ErrReadUnsupported) Error() string {
	return fmt.Sprintf("compatibility: version %q cannot read records of version %q", e.To, e.From)
}

// ErrReplayUnsupported marks a rejected replay capability: the target
// version cannot replay pending records of the source version, so the
// pending obligations keep their original version and wait for a build
// that can continue them.
type ErrReplayUnsupported struct{ From, To VersionID }

func (e *ErrReplayUnsupported) Error() string {
	return fmt.Sprintf("compatibility: version %q cannot replay records of version %q; pending keeps its original version", e.To, e.From)
}

// ErrWriteTargetNotCurrent marks a new-write attempt that does not target
// the current version. New writes always use the declared current
// version; they never quietly extend an older one.
type ErrWriteTargetNotCurrent struct{ Target, Current VersionID }

func (e *ErrWriteTargetNotCurrent) Error() string {
	return fmt.Sprintf("compatibility: new writes must target the current version %q, not %q", e.Current, e.Target)
}

// ErrVersionNotWritable marks a version that is registered for reading
// but retired for new writes.
type ErrVersionNotWritable struct{ ID VersionID }

func (e *ErrVersionNotWritable) Error() string {
	return fmt.Sprintf("compatibility: version %q is registered for reading but not for new writes", e.ID)
}

// ErrStoreTransitionRejected marks the never-a-silent-migration rule of
// the persistent store format family (ClaimStoreFormat): the candidate
// new-write version cannot read the version already carried by the
// durable store, so switching the store to it would abandon or rewrite
// remaining metadata. The transition is rejected before any write,
// regardless of drain state (DELIVERY_STATE_MACHINE: an incompatible
// persistent store format change is not permitted by a drain, and
// UPGRADE-01 requires the refusal to hold even after a completed drain
// with empty payload).
type ErrStoreTransitionRejected struct{ From, To VersionID }

func (e *ErrStoreTransitionRejected) Error() string {
	return fmt.Sprintf("compatibility: transition from store format %q to %q is rejected: the new version cannot read the remaining metadata, and a silent migration is never performed", e.From, e.To)
}

// Matrix is the read/replay/new-write capability matrix over the
// registered encoding versions. Registration is serialized; capability
// questions take a read lock, so a codec wiring step may register
// versions while decisions run.
//
// A Matrix is safe for concurrent use.
type Matrix struct {
	mu       sync.RWMutex
	current  VersionID
	versions map[VersionID]EncodingVersion
}

// NewMatrix builds a matrix whose current (new-write) version is current
// and registers the additional versions. Every registration rule of
// Matrix.Register applies.
func NewMatrix(current EncodingVersion, others ...EncodingVersion) (*Matrix, error) {
	m := &Matrix{versions: make(map[VersionID]EncodingVersion)}
	if err := m.Register(current); err != nil {
		return nil, err
	}
	// The matrix is not shared yet, so the plain write needs no lock.
	m.current = current.ID()
	for _, v := range others {
		if err := m.Register(v); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Register adds one encoding version. A nil implementation, an empty
// identifier, or an identifier that is already registered is rejected; the
// matrix is unchanged.
func (m *Matrix) Register(v EncodingVersion) error {
	if v == nil {
		return &ErrInvalidVersion{Reason: "version implementation is nil"}
	}
	id := v.ID()
	if id == "" {
		return &ErrInvalidVersion{Reason: "version identifier is empty"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.versions[id]; dup {
		return &ErrDuplicateVersion{ID: id}
	}
	m.versions[id] = v
	return nil
}

// Current returns the identifier of the current version, which is the
// only legal target of new writes.
func (m *Matrix) Current() VersionID {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

func (m *Matrix) lookupLocked(id VersionID) (EncodingVersion, error) {
	v, ok := m.versions[id]
	if !ok {
		return nil, &ErrUnknownVersion{ID: id}
	}
	return v, nil
}

// capability runs the shared part of the read and replay questions: both
// versions must be registered, identity always holds, and otherwise the
// readability predicate of to decides. ok reports the claim's outcome.
func (m *Matrix) capability(from, to VersionID) (ok bool, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tv, err := m.lookupLocked(to)
	if err != nil {
		return false, err
	}
	if _, err := m.lookupLocked(from); err != nil {
		return false, err
	}
	if from == to {
		// A version always reads its own records; the predicate is not
		// consulted for identity.
		return true, nil
	}
	return tv.Reads(from), nil
}

// CanRead reports whether a process running version to may decode the
// records and metadata of version from. Reading an older version by a
// newer one is allowed exactly when the newer version registered that
// claim; nothing is assumed and no fallback exists. Rejections name both
// versions.
func (m *Matrix) CanRead(from, to VersionID) error {
	ok, err := m.capability(from, to)
	if err != nil {
		return err
	}
	if !ok {
		return &ErrReadUnsupported{From: from, To: to}
	}
	return nil
}

// CanReplay reports whether a process running version to may republish
// the pending records of version from. Replay retransmits the exact
// durable bytes and never reserializes them — the transport wrapper
// carries the original immutable fragment bytes and their independent
// reserialization is forbidden (CONTROL_WIRE_CONTRACT "TransportFrame
// layout v2"), and a compatible reader republishes the old bytes
// (DELIVERY_STATE_MACHINE) — so decode capability is replay capability:
// the replay claim is the read claim of the same pair, with no separate
// registered predicate. The rejection type is separate so callers and
// tests can tell a replay stop from a decode stop.
func (m *Matrix) CanReplay(from, to VersionID) error {
	ok, err := m.capability(from, to)
	if err != nil {
		return err
	}
	if !ok {
		return &ErrReplayUnsupported{From: from, To: to}
	}
	return nil
}

// CanWriteNew reports whether the capability rules permit new records to
// be created in version to while the durable store still carries version
// from, under the transition policy of the claim family. The rules, in
// order:
//
//  1. claim must be a stated family (wire/plan or store format); the
//     family is never defaulted.
//
//  2. from and to must both be registered (unknown versions are protocol
//     incompatibilities).
//
//  3. to must be the current version; new writes always target it.
//
//  4. to must be writable (not registered as read-only).
//
//  5. if from and to differ and to cannot read from, the claim family
//     decides (DELIVERY_STATE_MACHINE "Errors, ENOSPC, совместимость"):
//
//     ClaimStoreFormat — rejected here with ErrStoreTransitionRejected,
//     before any write and regardless of drain state. The store keeps its
//     identity and lifecycle metadata for its whole life, there are no
//     automatic migrations, and even a completed drain with empty payload
//     does not permit the change (UPGRADE-01).
//
//     ClaimWirePlan — not a capability failure. A completed drain closes
//     every saved obligation, so the old format stops mattering for new
//     writes once the old build is DRAINED. This check returns nil and
//     the timing requirement (DRAINED by the old build) is enforced by
//     the drain guard; the matrix alone never authorizes the switch
//     "now", it only declines to reject the pair. A compatible wire/plan
//     transition (to reads from) is subject to the ordinary transition
//     timing, like a compatible store transition.
func (m *Matrix) CanWriteNew(from, to VersionID, claim ClaimType) error {
	if !claim.valid() {
		return &ErrInvalidClaim{Claim: claim}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	tv, err := m.lookupLocked(to)
	if err != nil {
		return err
	}
	if _, err := m.lookupLocked(from); err != nil {
		return err
	}
	if to != m.current {
		return &ErrWriteTargetNotCurrent{Target: to, Current: m.current}
	}
	if !tv.WritesNew() {
		return &ErrVersionNotWritable{ID: to}
	}
	if from != to && !tv.Reads(from) && claim == ClaimStoreFormat {
		return &ErrStoreTransitionRejected{From: from, To: to}
	}
	return nil
}
