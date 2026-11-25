// Named errors, the typed error answer of the gateway, the retry rules and
// the backoff of the client and the driver.

package ingestclient

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sigaev-pro/bc-indexer/adapters/ingestclient/ingest"
	"github.com/sigaev-pro/bc-indexer/config"
)

// Errors of the client and the driver. Each one ends the run; the class
// tells the node what to do, and the driver reports it through
// config.Reporter.Failure before it returns.
var (
	// ErrConfig rejects a configuration NewDriver cannot run with.
	ErrConfig = errors.New("ingestclient: invalid configuration")
	// ErrConfigBlocked is the answer to a retry action FIX_CONFIG: the
	// driver stopped, every byte is kept, the configuration must be fixed
	// and checked again.
	ErrConfigBlocked = errors.New("ingestclient: CONFIG_BLOCKED")
	// ErrBlocked is the answer to STOP_LOCAL_OWNER, DRAIN_OLD_BUILD and
	// DIAGNOSE_IMMUTABLE and to a conflict the driver must not retry away:
	// the driver stopped and every byte is kept.
	ErrBlocked = errors.New("ingestclient: BLOCKED")
	// ErrLimitIncompatible reports a frame the transport budget cannot
	// carry: the batch stays READY and nothing was sealed.
	ErrLimitIncompatible = errors.New("ingestclient: LIMIT_INCOMPATIBLE")
	// ErrInvalidSeed rejects a reservation seed that is not the binary v2
	// layout with the pinned versions.
	ErrInvalidSeed = errors.New("ingestclient: invalid reservation seed")
	// ErrProtocol rejects a gateway answer that is not the one the contract
	// defines: a malformed field, a success answer of another shape.
	ErrProtocol = errors.New("ingestclient: malformed gateway answer")
	// ErrJournalData rejects producer journal bytes that do not match the
	// markers of their batch: a manifest, attempt data or plan the driver
	// cannot send as they are. The owner stops on it (STORE_FATAL class).
	ErrJournalData = errors.New("ingestclient: producer journal data does not match its markers")
	// ErrIngestAckMismatch rejects an IngestAck that differs from the
	// producer's records in any of its six fields.
	ErrIngestAckMismatch = errors.New("ingestclient: IngestAck does not match the batch")
	// ErrDriverStopped rejects an announcement after Run ended.
	ErrDriverStopped = errors.New("ingestclient: driver is not running")
	// ErrDriverRunning rejects a second Run on one driver.
	ErrDriverRunning = errors.New("ingestclient: driver is already running")
	// ErrQueueFull rejects an announcement when the queue holds its bound.
	ErrQueueFull = errors.New("ingestclient: driver queue is full")
	// ErrCallTimeout reports a call that got no answer within callTimeout.
	ErrCallTimeout = errors.New("ingestclient: gateway did not answer in time")
)

// WireError is the typed error answer of one RPC
// ({code, request_id, owner_state, field_path, retry_action, detail}). The
// gRPC status is transport only and never carries it.
type WireError struct {
	// Step names the refused call.
	Step string
	// Code and Action are the registry values of the answer.
	Code   ingest.ErrorCode
	Action ingest.RetryAction
	// Detail is the redacted detail of the gateway.
	Detail string
	// NextOffset is the cursor an OFFSET_MISMATCH answer carries: the
	// offset the gateway expects next.
	NextOffset uint64
}

func (e *WireError) Error() string {
	return fmt.Sprintf("ingestclient: %s: %s %s: %s", e.Step, e.Code, e.Action, e.Detail)
}

// Unwrap maps the answer onto the class the node acts on. RECONNECT and
// RESUME_AT_OFFSET are handled inside the driver and unwrap to nothing. So
// is RETRY_BACKOFF with the code TRANSIENT_UNAVAILABLE, the one transient
// answer of the registry; RETRY_BACKOFF with any other code is a conflict
// the registry pairs with that action, and a conflict is never retried
// away, so it is BLOCKED.
func (e *WireError) Unwrap() error {
	switch e.Action {
	case ingest.RetryAction_FIX_CONFIG:
		return ErrConfigBlocked
	case ingest.RetryAction_STOP_LOCAL_OWNER, ingest.RetryAction_DRAIN_OLD_BUILD,
		ingest.RetryAction_DIAGNOSE_IMMUTABLE:
		return ErrBlocked
	case ingest.RetryAction_RETRY_UNSPECIFIED:
		// The registry sends no action with ALREADY_RETIRED, MODE_CONFLICT
		// and VERSION_UNSUPPORTED.
		return ErrBlocked
	case ingest.RetryAction_RETRY_BACKOFF:
		if e.Code != ingest.ErrorCode_TRANSIENT_UNAVAILABLE {
			return ErrBlocked
		}
	}
	return nil
}

// action is what the driver does with a failed call.
type action uint8

const (
	// actionRetry backs off and repeats the same call.
	actionRetry action = iota + 1
	// actionReconnect opens a new session and repeats the same call.
	actionReconnect
	// actionResume continues the stream at the cursor the answer carries.
	actionResume
	// actionStop ends the run with the error.
	actionStop
)

// classify maps a failed call onto the action of its retry rule. A typed
// answer is acted on by its retry action. Any other error is a transport
// failure or a local one: the transport is TRANSIENT (backoff, no attempt
// limit), except a method the gateway does not implement, which is a build
// the driver cannot talk to. A local error (journal, validation, a
// malformed answer) is never retried.
//
// DECISION (contract reference: DELIVERY_STATE_MACHINE, "Errors, ENOSPC,
// совместимость": TRANSIENT is a backoff without an attempt limit, and an
// immutable conflict is BLOCKED): the registry pairs RETRY_BACKOFF with
// codes that are conflicts (IDENTITY_MISMATCH, MODE_CONFLICT,
// INVALID_DESCRIPTOR, INVALID_PAYLOAD). Repeating the same request after
// such an answer can never change it, so only TRANSIENT_UNAVAILABLE is
// repeated. The retry actions RECONNECT and RESUME_AT_OFFSET are acted on
// as the registry says.
func classify(err error) action {
	if err == nil {
		return actionStop
	}
	var wire *WireError
	if errors.As(err, &wire) {
		switch wire.Action {
		case ingest.RetryAction_RETRY_BACKOFF:
			if wire.Code == ingest.ErrorCode_TRANSIENT_UNAVAILABLE {
				return actionRetry
			}
		case ingest.RetryAction_RECONNECT:
			return actionReconnect
		case ingest.RetryAction_RESUME_AT_OFFSET:
			return actionResume
		}
		return actionStop
	}
	if st, ok := status.FromError(err); ok {
		if st.Code() == codes.Unimplemented {
			return actionStop
		}
		return actionRetry
	}
	if errors.Is(err, ErrCallTimeout) {
		return actionRetry
	}
	return actionStop
}

// wireError builds the typed error of one refused call.
func wireError(step string, e *ingest.Error, nextOffset uint64) *WireError {
	return &WireError{Step: step, Code: e.GetCode(), Action: e.GetRetryAction(), Detail: e.GetDetail(), NextOffset: nextOffset}
}

// newRequestID returns one Id16 for a durable operation. The id is kept
// while the same operation is repeated.
func newRequestID() ([]byte, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, fmt.Errorf("ingestclient: request id: %w", err)
	}
	return id, nil
}

// Time bounds of the driver and its client. They are package variables so
// the tests of this package can shorten them; production code sets none.
var (
	// callTimeout bounds one call, a stream receive included.
	callTimeout = 20 * time.Second
	// backoffBaseDelay is the first RETRY_BACKOFF delay; each further one
	// doubles up to backoffMaxDelay, and jitter spreads it over the upper
	// half of the interval.
	backoffBaseDelay = 200 * time.Millisecond
	backoffMaxDelay  = 10 * time.Second
)

// retryBackoff is the capped exponential backoff with jitter of TRANSIENT
// failures: no attempt limit, bounded in-flight (one call at a time).
type retryBackoff struct {
	attempt int
	random  func() uint64
}

// reset returns the sequence to its first delay after a call succeeded.
func (b *retryBackoff) reset() { b.attempt = 0 }

// next returns the next delay: the base doubled per attempt, capped, and
// spread by jitter over the upper half of the interval so that retried
// drivers do not stay in step.
func (b *retryBackoff) next() time.Duration {
	delay := backoffBaseDelay
	for i := 0; i < b.attempt && delay < backoffMaxDelay; i++ {
		delay *= 2
		if delay <= 0 { // the shift overflowed: the cap applies
			delay = backoffMaxDelay
		}
	}
	if delay > backoffMaxDelay {
		delay = backoffMaxDelay
	}
	if b.attempt < 63 {
		b.attempt++
	}
	random := b.random
	if random == nil {
		random = mathrand.Uint64
	}
	half := delay / 2
	return half + time.Duration(random()%uint64(half))
}

// sleep waits for one backoff delay or until ctx ends; it reports whether
// the run is over.
func (b *retryBackoff) sleep(ctx context.Context) bool {
	delay := b.next()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-timer.C:
		return false
	}
}

// fmtError wraps a class sentinel with located detail.
func fmtError(class error, format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{class}, args...)...)
}

// Failure is how a stopped driver reports its cause. It carries the class
// of the stop (one of the errors above, reachable with errors.Is) and the
// located error of the live configuration (config.Error, reachable with
// errors.As), whose exit-code class is the one LIVE_CONFIG_SPEC assigns:
// 2 for a configuration that must be fixed, 3 for a blocked state, 4 for
// an incompatible protocol and 5 for a fatal store or runtime error.
type Failure struct {
	// located is the exit-code class of the stop.
	located *config.Error
	// cause is the error the driver stopped with.
	cause error
}

// Error returns the reason of the stop.
func (f *Failure) Error() string { return f.located.Error() }

// Unwrap returns both the located error and the cause, so errors.As finds
// the first and errors.Is the class of the second.
func (f *Failure) Unwrap() []error { return []error{f.located, f.cause} }

// newFailure maps the error a run ended with onto its exit-code class of
// LIVE_CONFIG_SPEC ("Валидация и остановка").
func newFailure(err error) *Failure {
	code := config.ExitFatal
	field := "delivery"
	switch {
	case errors.Is(err, ErrConfig), errors.Is(err, ErrConfigBlocked), errors.Is(err, ErrLimitIncompatible):
		code = config.ExitConfig
	case errors.Is(err, ErrBlocked):
		code = config.ExitState
	case errors.Is(err, ErrProtocol), errors.Is(err, ErrInvalidSeed), errors.Is(err, ErrIngestAckMismatch):
		code = config.ExitUnsupported
	}
	return &Failure{located: &config.Error{Field: field, Code: code, Reason: err.Error()}, cause: err}
}
