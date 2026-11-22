// Package errors is E03-04.pressure_errors: the error classification, the
// retry policy decisions, the capped backoff with jitter and the bounded
// queues of the delivery path
// (DELIVERY_STATE_MACHINE, "Errors, ENOSPC, совместимость";
// DELIVERY_RECOVERY, scenario table).
//
// The classes follow the contract table: transient failures retry with a
// capped exponential backoff and unbounded attempts while pending lives on
// disk; configuration failures stop publication with a named reason and
// keep every byte; a poison batch holds its frontier and drain with a
// visible error instead of an infinite fast retry; a local fatal disk or
// journal error stops its own owner, while a lost remote link is an
// outage, never evidence of remote corruption; and ENOSPC stops capture
// without any TTL or quota dropping pending data.
package errors

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
)

// Class is the error class of the delivery error table.
type Class uint8

// The error classes.
const (
	// ClassUnknown marks an unclassified error. The policy stops and
	// diagnoses: no silent retry, no drop, no false acknowledgment.
	ClassUnknown Class = iota

	// ClassTransient marks disconnects, timeouts and an offline server:
	// capped exponential backoff with jitter, no attempt limit, bounded
	// in-flight, pending on disk.
	ClassTransient

	// ClassConfigBlocked marks a wrong route, TLS, permission or limit:
	// publication stops, data and a named reason are kept, and the target
	// is re-checked after the fix.
	ClassConfigBlocked

	// ClassPoison marks an immutable READY/SEALED conflict or a batch the
	// pipeline cannot process: the bytes under the id never change, the
	// batch holds its frontier and drain, and the error stays visible.
	// This is an honest stop, not an infinite fast retry.
	ClassPoison

	// ClassLocalFatal marks a local fatal error of an owner's own disk or
	// journal (adapter WAL, gateway journal): that owner stops. A
	// successful ERROR write on an already broken disk is not required.
	ClassLocalFatal

	// ClassRemoteOutage marks a lost link to the remote owner: the remote
	// owner keeps running its own policy, and the local owner continues
	// its safe local WAL. A lost link is never proof of remote corruption.
	ClassRemoteOutage

	// ClassNoSpace marks ENOSPC: capture stops, space is added or safely
	// freed outside the stores, and no manual SST/WAL/pending deletion or
	// repair-with-drop happens. Pending has no TTL and no quota.
	ClassNoSpace
)

// String returns the class name.
func (c Class) String() string {
	switch c {
	case ClassUnknown:
		return "UNKNOWN"
	case ClassTransient:
		return "TRANSIENT"
	case ClassConfigBlocked:
		return "CONFIG_BLOCKED"
	case ClassPoison:
		return "POISON"
	case ClassLocalFatal:
		return "LOCAL_FATAL"
	case ClassRemoteOutage:
		return "REMOTE_OUTAGE"
	case ClassNoSpace:
		return "ENOSPC"
	}
	return fmt.Sprintf("Class(%d)", uint8(c))
}

// Action is the owner action a class prescribes.
type Action uint8

// The owner actions of the error table.
const (
	// ActionDiagnose stops the step and surfaces the error: the default
	// for unclassified failures.
	ActionDiagnose Action = iota

	// ActionRetryBackoff retries the same bytes and route after a backoff
	// delay, with no attempt limit.
	ActionRetryBackoff

	// ActionStopPublication stops publishing and keeps the data until a
	// fixed configuration is re-checked.
	ActionStopPublication

	// ActionHoldFrontier holds the frontier and drain on the failing
	// batch and keeps the error visible.
	ActionHoldFrontier

	// ActionStopLocalOwner stops this owner's process (native adapter on
	// a broken WAL, gateway on a broken journal).
	ActionStopLocalOwner

	// ActionContinueLocal keeps collecting into the safe local WAL while
	// the remote side is unreachable.
	ActionContinueLocal

	// ActionStopCapture stops new capture until space exists again.
	ActionStopCapture
)

// String returns the action name.
func (a Action) String() string {
	switch a {
	case ActionDiagnose:
		return "DIAGNOSE"
	case ActionRetryBackoff:
		return "RETRY_BACKOFF"
	case ActionStopPublication:
		return "STOP_PUBLICATION"
	case ActionHoldFrontier:
		return "HOLD_FRONTIER"
	case ActionStopLocalOwner:
		return "STOP_LOCAL_OWNER"
	case ActionContinueLocal:
		return "CONTINUE_LOCAL"
	case ActionStopCapture:
		return "STOP_CAPTURE"
	}
	return fmt.Sprintf("Action(%d)", uint8(a))
}

// ActionForClass returns the owner action the class prescribes.
func ActionForClass(c Class) Action {
	switch c {
	case ClassTransient:
		return ActionRetryBackoff
	case ClassConfigBlocked:
		return ActionStopPublication
	case ClassPoison:
		return ActionHoldFrontier
	case ClassLocalFatal:
		return ActionStopLocalOwner
	case ClassRemoteOutage:
		return ActionContinueLocal
	case ClassNoSpace:
		return ActionStopCapture
	default:
		return ActionDiagnose
	}
}

// The sentinel errors owners register. Engines of other modules (the
// publisher's NATS errors, the destination check's unavailable error, the
// stores' local fatal errors) register their own sentinels against these
// classes through Classifier.Register.
var (
	// ErrLocalDiskFatal marks a write failure of the owner's own disk:
	// the local owner stops; no durable cursor advances.
	ErrLocalDiskFatal = errors.New("delivery/errors: local disk write failed")

	// ErrJournalFatal marks a fatal error of the owner's own journal.
	ErrJournalFatal = errors.New("delivery/errors: local journal fatal")

	// ErrLinkLost marks a lost link to the remote owner.
	ErrLinkLost = errors.New("delivery/errors: remote link lost")

	// ErrNoSpaceLeft marks ENOSPC on a local write.
	ErrNoSpaceLeft = errors.New("delivery/errors: no space left on device")

	// ErrConfiguration marks a wrong route, permission or limit.
	ErrConfiguration = errors.New("delivery/errors: configuration rejected the step")

	// ErrPoisonBatch marks a batch the pipeline cannot process without
	// changing immutable bytes.
	ErrPoisonBatch = errors.New("delivery/errors: poison batch holds the frontier")
)

// Classifier maps errors to classes: registered sentinels first, then the
// builtin structural rules (timeouts, deadline exceeded, ENOSPC), and
// ClassUnknown for everything else. A Classifier is safe for concurrent
// use. Wiring registers the engine sentinels once at startup.
type Classifier struct {
	mu    sync.RWMutex
	byErr map[error]Class
}

// NewClassifier returns a classifier with the package sentinels
// pre-registered.
func NewClassifier() *Classifier {
	return &Classifier{byErr: map[error]Class{
		ErrLocalDiskFatal: ClassLocalFatal,
		ErrJournalFatal:   ClassLocalFatal,
		ErrLinkLost:       ClassRemoteOutage,
		ErrNoSpaceLeft:    ClassNoSpace,
		ErrConfiguration:  ClassConfigBlocked,
		ErrPoisonBatch:    ClassPoison,
	}}
}

// Register maps one sentinel error of an engine onto a class. Registering
// a nil sentinel is rejected; re-registering the same sentinel with a
// different class is rejected so a wiring mistake cannot silently
// reclassify.
func (c *Classifier) Register(sentinel error, class Class) error {
	if sentinel == nil {
		return fmt.Errorf("delivery/errors: nil sentinel")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.byErr[sentinel]; ok && existing != class {
		return fmt.Errorf("delivery/errors: sentinel already registered as %s", existing)
	}
	c.byErr[sentinel] = class
	return nil
}

// Classify returns the class of err. nil classifies as ClassUnknown with
// a false known flag; wrapped errors unwrap through the chain.
func (c *Classifier) Classify(err error) (class Class, known bool) {
	if err == nil {
		return ClassUnknown, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for e := err; e != nil; e = errors.Unwrap(e) {
		if class, ok := c.byErr[e]; ok {
			return class, true
		}
	}
	// Builtin structural rules.
	if errors.Is(err, context.DeadlineExceeded) {
		return ClassTransient, true
	}
	if errors.Is(err, syscall.ENOSPC) {
		return ClassNoSpace, true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ClassTransient, true
	}
	return ClassUnknown, false
}

// MustClassify classifies and returns the action of the class, so a
// runtime loop decides one branch. An unknown error prescribes diagnose.
func (c *Classifier) MustClassify(err error) Action {
	class, _ := c.Classify(err)
	return ActionForClass(class)
}
