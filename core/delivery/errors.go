// Named errors of the delivery seam layer. Every rejection carries one of
// these sentinels with a specific reason, so the release, maintenance and
// error engines classify failures without matching free-form text. The
// sentinels mirror the protocol error registry of CONTROL_WIRE_CONTRACT
// where a protocol code exists for the same rule.
package delivery

import "errors"

var (
	// ErrInvalidValue rejects a structurally invalid identity or record:
	// a zero id, a zero counter or a missing digest.
	ErrInvalidValue = errors.New("delivery: invalid value")

	// ErrCounterOverflow rejects checked counter arithmetic that would
	// wrap. Counters never wrap, truncate or reuse values.
	ErrCounterOverflow = errors.New("delivery: checked counter overflow")

	// ErrNotFound marks a read of a record the store does not hold.
	ErrNotFound = errors.New("delivery: record not found")

	// ErrState rejects a transition the current durable state does not
	// allow. The state is unchanged; the caller keeps its data.
	ErrState = errors.New("delivery: state does not allow the transition")

	// ErrImmutableConflict rejects different bytes or a different identity
	// under an id that already carries durable content. The old content
	// stands; nothing is rewritten or re-identified.
	ErrImmutableConflict = errors.New("delivery: immutable conflict")

	// ErrAlreadyRetired answers a Prepare/Publish whose sequence is at or
	// below the retired prefix. It creates no keys and by itself confirms
	// nothing about an unacknowledged batch.
	ErrAlreadyRetired = errors.New("delivery: batch already retired")

	// ErrConflictPending rejects an operation that conflicts with an
	// existing local record that the owner has not confirmed: a baseline
	// adoption over unconfirmed batches, or a recovery release over a
	// locally recorded pending batch. Pending is never deleted to resolve
	// the conflict.
	ErrConflictPending = errors.New("delivery: conflicts with unconfirmed local record")

	// ErrFenced rejects a step whose session fence is stale: an old token,
	// an old connection or admission generation, or a wrong session kind.
	// A fenced step creates no state.
	ErrFenced = errors.New("delivery: session fence rejected the step")

	// ErrPressured marks a bounded buffer refusing more work: backpressure
	// to the caller, not data loss.
	ErrPressured = errors.New("delivery: bounded buffer is full")
)
