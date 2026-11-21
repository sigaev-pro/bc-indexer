package compatibility

import "fmt"

// DeliveryState is the delivery state of a pending obligation as fixed by
// DELIVERY_STATE_MACHINE: INTENT, STAGING, READY, SEALED, ACKED_CLEANUP
// and the release outbox. StateNone means no pending obligation reports a
// state; it is valid only together with a zero pending count.
type DeliveryState uint8

const (
	// StateNone reports that no pending obligation carries a state. It is
	// not a delivery state and never a default success.
	StateNone DeliveryState = iota
	StateIntent
	StateStaging
	StateReady
	StateSealed
	StateAckedCleanup
	StateOutbox
)

// SealedOrLater reports whether the state is SEALED or one of the later
// post-publication states. An obligation at SEALED or later must replay
// byte-identically, which is why a format change is out of the question
// while such obligations remain.
func (s DeliveryState) SealedOrLater() bool {
	return s >= StateSealed && s <= StateOutbox
}

func (s DeliveryState) String() string {
	switch s {
	case StateNone:
		return "NONE"
	case StateIntent:
		return "INTENT"
	case StateStaging:
		return "STAGING"
	case StateReady:
		return "READY"
	case StateSealed:
		return "SEALED"
	case StateAckedCleanup:
		return "ACKED_CLEANUP"
	case StateOutbox:
		return "OUTBOX"
	default:
		return fmt.Sprintf("DeliveryState(%d)", uint8(s))
	}
}

// valid reports whether s is one of the defined states.
func (s DeliveryState) valid() bool {
	return s >= StateNone && s <= StateOutbox
}

// pendingValid reports whether s is a state a pending obligation can be
// in, that is, any state except StateNone.
func (s DeliveryState) pendingValid() bool {
	return s >= StateIntent && s <= StateOutbox
}

// Mode is the producer mode of DELIVERY_STATE_MACHINE: LIVE, DRAIN,
// DRAINED or BLOCKED. ModeUnset means the mode was not reported and is
// never a default for a decision.
type Mode uint8

const (
	ModeUnset Mode = iota
	ModeLive
	ModeDrain
	ModeDrained
	ModeBlocked
)

func (m Mode) String() string {
	switch m {
	case ModeUnset:
		return "UNSET"
	case ModeLive:
		return "LIVE"
	case ModeDrain:
		return "DRAIN"
	case ModeDrained:
		return "DRAINED"
	case ModeBlocked:
		return "BLOCKED"
	default:
		return fmt.Sprintf("Mode(%d)", uint8(m))
	}
}

// valid reports whether m is one of the defined modes.
func (m Mode) valid() bool {
	return m >= ModeUnset && m <= ModeBlocked
}

// DrainSnapshot is the durable drain input of a format-transition
// decision: the producer mode, the count of pending (not yet retired)
// obligations and the highest delivery state any of them has reached.
type DrainSnapshot struct {
	Mode        Mode
	Pending     int
	MaxDelivery DeliveryState
}

// ErrInvalidSnapshot marks contradictory drain input: an unset mode, a
// negative pending count, a delivery state without pending obligations,
// or pending obligations without a delivery state. Such input is never
// rounded to a permissive default.
type ErrInvalidSnapshot struct{ Reason string }

func (e *ErrInvalidSnapshot) Error() string {
	return "compatibility: invalid drain snapshot: " + e.Reason
}

func (s DrainSnapshot) validate() error {
	if !s.Mode.valid() || s.Mode == ModeUnset {
		return &ErrInvalidSnapshot{Reason: "producer mode is unknown or not reported"}
	}
	if s.Pending < 0 {
		return &ErrInvalidSnapshot{Reason: "pending count is negative"}
	}
	if !s.MaxDelivery.valid() {
		return &ErrInvalidSnapshot{Reason: "highest delivery state is unknown"}
	}
	if s.Pending == 0 && s.MaxDelivery != StateNone {
		return &ErrInvalidSnapshot{Reason: "delivery state reported without pending obligations"}
	}
	if s.Pending > 0 && !s.MaxDelivery.pendingValid() {
		return &ErrInvalidSnapshot{Reason: "pending obligations without a delivery state"}
	}
	return nil
}

// ErrTransitionBlocked is the drain-guard rejection: a format transition
// may not start while a drain is in progress or while any pending
// obligation remains. The rejection is independent of the capability
// matrix: during drain the remaining metadata must stay in a format the
// running build can finish; a completed drain closes obligations but does
// not by itself permit changing them.
type ErrTransitionBlocked struct {
	Reason   string
	Snapshot DrainSnapshot
}

func (e *ErrTransitionBlocked) Error() string {
	return fmt.Sprintf("compatibility: format transition blocked: %s (mode %s, %d pending, highest state %s)",
		e.Reason, e.Snapshot.Mode, e.Snapshot.Pending, e.Snapshot.MaxDelivery)
}

// DrainGuard decides whether a store-format transition may start, given
// the durable drain facts. The decision is deliberately independent of
// the capability matrix:
//
//   - Zero pending obligations and no drain in progress: the transition
//     timing is allowed (the matrix still applies on its own).
//   - Any pending obligation: rejected. This includes obligations at
//     SEALED or later, which must replay byte-identically.
//   - Drain in progress (mode DRAIN): rejected even with zero pending,
//     because the drain has not yet fixed drain_id/highwater and sealed;
//     the transition waits for the completed, sealed drain.
//
// Reading and replaying pending obligations during a drain are not
// guarded here: finishing the saved obligations is exactly what a drain
// does, so those decisions belong to the matrix alone.
type DrainGuard struct{}

// AllowFormatTransition returns nil when the transition timing is allowed
// and an error explaining the veto otherwise.
func (DrainGuard) AllowFormatTransition(s DrainSnapshot) error {
	if err := s.validate(); err != nil {
		return err
	}
	if s.Mode == ModeDrain {
		return &ErrTransitionBlocked{Reason: "drain is in progress", Snapshot: s}
	}
	if s.Pending > 0 {
		reason := "pending obligations remain"
		if s.MaxDelivery.SealedOrLater() {
			reason = "pending obligations at SEALED or later remain and must replay their original bytes"
		}
		return &ErrTransitionBlocked{Reason: reason, Snapshot: s}
	}
	return nil
}
