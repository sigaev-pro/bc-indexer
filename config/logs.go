package config

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sigaev-pro/bc-indexer/protocol/compatibility"
)

// The E06-12 control-log surface. Control states are reported through
// the state names the journals already persist: the native admission
// states of E01-02 (INPUT_PREPARING, ADMITTED, EXEC_READY, ABANDONED,
// NATIVE_REJECTED, DECIDED), the producer delivery states of E02-04
// (INTENT, STAGING, READY, SEALED, ACKED_CLEANUP, OUTBOX) and the
// gateway batch states of E03 (PREPARING, PREPARED, RECEIVING, CLOSED,
// NATS_ACKED, RELEASING), with RETIRED reported through the retired
// prefix of either owner. There is no second parallel journal, no
// simplified RECEIVED/WAL_SAVED vocabulary, and no deduplication or
// completeness verifier: the reporter is a stateless formatter over
// read-only journal facts.
//
// The required control events are visible with debug logging off; only
// diagnostic detail uses the debug level. Secrets never enter the
// output: the reporter receives identities and states only, and every
// rendered string passes a redaction pass as defense in depth
// (AC-OPS-09-01). A SIGKILL is never reported as a graceful STOPPED
// (AC-OPS-09-02); the following startup reports the recovery instead.

// LogLevel is the runtime.log_level enum with its info default.
type LogLevel uint8

// The log levels.
const (
	LevelDebug LogLevel = iota + 1
	LevelInfo
	LevelWarn
	LevelError
)

// ParseLogLevel converts a level name. Unknown names are rejected, not
// defaulted.
func ParseLogLevel(s string) (LogLevel, error) {
	switch s {
	case "debug":
		return LevelDebug, nil
	case "info":
		return LevelInfo, nil
	case "warn":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %s", quote(s))
}

// String returns the level name.
func (l LogLevel) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	}
	return "level(" + strconv.Itoa(int(l)) + ")"
}

// The single control-state vocabulary, mirroring the journals. These
// constants are the only accepted state names in reporter input; an
// unknown name is an error, never a silent extra state.
const (
	StateNativeInputPreparing = "INPUT_PREPARING"
	StateNativeAdmitted       = "ADMITTED"
	StateNativeExecReady      = "EXEC_READY"
	StateNativeAbandoned      = "ABANDONED"
	StateNativeRejected       = "NATIVE_REJECTED"
	StateNativeDecided        = "DECIDED"

	StateDeliveryIntent       = "INTENT"
	StateDeliveryStaging      = "STAGING"
	StateDeliveryReady        = "READY"
	StateDeliverySealed       = "SEALED"
	StateDeliveryAckedCleanup = "ACKED_CLEANUP"
	StateDeliveryOutbox       = "OUTBOX"

	StateGatewayPreparing  = "PREPARING"
	StateGatewayPrepared   = "PREPARED"
	StateGatewayReceiving  = "RECEIVING"
	StateGatewayClosed     = "CLOSED"
	StateGatewayNATSACKed  = "NATS_ACKED"
	StateGatewayReleasing  = "RELEASING"
	StateRetired           = "RETIRED"
	StateProducerModeLive  = "LIVE"
	StateProducerModeDrain = "DRAIN"
)

// nativeStates are the native admission states of E01-02.
var nativeStates = map[string]bool{
	StateNativeInputPreparing: true,
	StateNativeAdmitted:       true,
	StateNativeExecReady:      true,
	StateNativeAbandoned:      true,
	StateNativeRejected:       true,
	StateNativeDecided:        true,
}

// deliveryStates are the producer delivery states of E02-04.
var deliveryStates = map[string]bool{
	StateDeliveryIntent:       true,
	StateDeliveryStaging:      true,
	StateDeliveryReady:        true,
	StateDeliverySealed:       true,
	StateDeliveryAckedCleanup: true,
	StateDeliveryOutbox:       true,
}

// gatewayStates are the gateway batch states of E03 plus RETIRED, which
// the retired prefix reports for either owner.
var gatewayStates = map[string]bool{
	StateGatewayPreparing: true,
	StateGatewayPrepared:  true,
	StateGatewayReceiving: true,
	StateGatewayClosed:    true,
	StateGatewayNATSACKed: true,
	StateGatewayReleasing: true,
	StateRetired:          true,
}

// Field is one rendered event field.
type Field struct {
	Key   string
	Value string
}

// Event is one control event. Kind names the event; the fields carry
// identities (profile, observation key, batch sequence, state, digests),
// never payloads or credentials.
type Event struct {
	Level  LogLevel
	Kind   string
	Fields []Field
}

// The event kinds.
const (
	EventStartup  = "STARTUP"
	EventNative   = "NATIVE"
	EventDelivery = "DELIVERY"
	EventGateway  = "GATEWAY"
	EventMode     = "MODE"
	EventStopped  = "STOPPED"
	EventError    = "ERROR"
	EventDetail   = "DETAIL" // debug-only diagnostic detail
)

// Reporter renders control events to a writer under a fixed level. It
// keeps no per-key or per-batch history: identical facts render twice,
// because deduplication and completeness checks are not this layer's
// job.
type Reporter struct {
	level LogLevel
	out   io.Writer
	now   func() time.Time
	mu    sync.Mutex
}

// NewReporter builds a reporter. A nil writer discards debug detail
// while the required events still return their rendered form.
func NewReporter(level LogLevel, out io.Writer) *Reporter {
	return &Reporter{level: level, out: out, now: time.Now}
}

// Enabled reports whether the level renders.
func (r *Reporter) Enabled(level LogLevel) bool { return level >= r.level }

// Render formats one event. Values pass the redaction pass; the bool
// result reports whether the level is enabled.
func (r *Reporter) Render(ev Event) (string, bool) {
	if !r.Enabled(ev.Level) {
		return "", false
	}
	var b strings.Builder
	b.WriteString("level=")
	b.WriteString(ev.Level.String())
	b.WriteString(" event=")
	b.WriteString(ev.Kind)
	for _, f := range ev.Fields {
		b.WriteByte(' ')
		b.WriteString(f.Key)
		b.WriteByte('=')
		b.WriteString(redact(f.Value))
	}
	return b.String(), true
}

// emit renders and writes one event with a timestamp.
func (r *Reporter) emit(ev Event) (string, bool) {
	line, ok := r.Render(ev)
	if !ok || r.out == nil {
		return line, ok
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.out, "%s %s\n", r.now().UTC().Format(time.RFC3339), line)
	return line, true
}

// RecoveredObligation is one startup recovery fact: the journal owner,
// the persisted state name and how many obligations the restart found
// in it.
type RecoveredObligation struct {
	Owner string // "native", "producer" or "gateway"
	State string
	Count int
}

// Startup reports process startup with the recovery view of the
// persisted journals (AC-OPS-09-02: after SIGKILL the startup shows the
// recovery of the relevant identities, not an invented graceful stop).
func (r *Reporter) Startup(role Role, profile string, mode compatibility.Mode, recovered []RecoveredObligation) (string, bool) {
	ev := Event{Level: LevelInfo, Kind: EventStartup, Fields: []Field{
		{Key: "role", Value: string(role)},
		{Key: "profile", Value: profile},
		{Key: "mode", Value: mode.String()},
	}}
	for _, ro := range recovered {
		if err := validOwnerState(ro.Owner, ro.State); err != nil {
			continue // unknown states never render; callers validate first
		}
		ev.Fields = append(ev.Fields, Field{
			Key:   "recovered_" + ro.Owner + "_" + ro.State,
			Value: strconv.Itoa(ro.Count),
		})
	}
	return r.emit(ev)
}

// NativeObservation reports one native admission state of an
// observation key. Unknown state names are rejected: the reporter never
// invents states such as the retired RECEIVED/WAL_SAVED names.
func (r *Reporter) NativeObservation(key, state string) (string, bool, error) {
	if !nativeStates[state] {
		return "", false, fmt.Errorf("unknown native observation state %s", quote(state))
	}
	line, ok := r.emit(Event{Level: LevelInfo, Kind: EventNative, Fields: []Field{
		{Key: "key", Value: key},
		{Key: "state", Value: state},
	}})
	return line, ok, nil
}

// DeliveryBatch reports one producer delivery state of a batch
// sequence.
func (r *Reporter) DeliveryBatch(seq uint64, state string) (string, bool, error) {
	if !deliveryStates[state] {
		return "", false, fmt.Errorf("unknown producer delivery state %s", quote(state))
	}
	line, ok := r.emit(Event{Level: LevelInfo, Kind: EventDelivery, Fields: []Field{
		{Key: "batch", Value: strconv.FormatUint(seq, 10)},
		{Key: "state", Value: state},
	}})
	return line, ok, nil
}

// GatewayBatch reports one gateway batch state of a batch sequence.
func (r *Reporter) GatewayBatch(seq uint64, state string) (string, bool, error) {
	if !gatewayStates[state] {
		return "", false, fmt.Errorf("unknown gateway batch state %s", quote(state))
	}
	line, ok := r.emit(Event{Level: LevelInfo, Kind: EventGateway, Fields: []Field{
		{Key: "batch", Value: strconv.FormatUint(seq, 10)},
		{Key: "state", Value: state},
	}})
	return line, ok, nil
}

// RetiredPrefix reports the retired-prefix advance of one owner: the
// RETIRED reporting path for both the producer and the gateway.
func (r *Reporter) RetiredPrefix(owner string, seq uint64) (string, bool) {
	line, ok := r.emit(Event{Level: LevelInfo, Kind: EventDelivery, Fields: []Field{
		{Key: "owner", Value: owner},
		{Key: "state", Value: StateRetired},
		{Key: "prefix", Value: strconv.FormatUint(seq, 10)},
	}})
	return line, ok
}

// Mode reports a durable producer-mode fact or transition.
func (r *Reporter) Mode(mode compatibility.Mode, detail string) (string, bool) {
	fields := []Field{{Key: "mode", Value: mode.String()}}
	if detail != "" {
		fields = append(fields, Field{Key: "detail", Value: detail})
	}
	return r.emit(Event{Level: LevelInfo, Kind: EventMode, Fields: fields})
}

// gracefulSignals are the signals that end in a graceful STOPPED
// report; SIGINT maps to exit hint 130 and SIGTERM to 143
// (LIVE_CONFIG_SPEC, "Валидация и остановка").
var gracefulSignals = map[string]int{
	"SIGINT":  130,
	"SIGTERM": 143,
}

// ErrNotGraceful rejects a termination cause that must not render a
// STOPPED event: SIGKILL, an unknown signal or a hard exit.
var ErrNotGraceful = fmt.Errorf("termination cause is not a graceful stop")

// Stopped reports a graceful termination. Any other cause returns
// ErrNotGraceful and emits nothing: a SIGKILL is never reported as a
// graceful STOPPED state.
func (r *Reporter) Stopped(cause string) (string, bool, error) {
	hint, ok := gracefulSignals[cause]
	if !ok {
		return "", false, ErrNotGraceful
	}
	line, ok := r.emit(Event{Level: LevelInfo, Kind: EventStopped, Fields: []Field{
		{Key: "signal", Value: cause},
		{Key: "exit_hint", Value: strconv.Itoa(hint)},
	}})
	return line, ok, nil
}

// Failure reports one error with its exit-code class. Located
// configuration errors carry their field; wrapped diagnostics pass the
// redaction pass.
func (r *Reporter) Failure(err error) (string, bool) {
	fields := []Field{{Key: "class", Value: "runtime"}, {Key: "code", Value: strconv.Itoa(int(ExitFatal))}}
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			fields = []Field{
				{Key: "class", Value: "config"},
				{Key: "code", Value: strconv.Itoa(int(e.Code))},
				{Key: "field", Value: e.Field},
				{Key: "reason", Value: e.Reason},
			}
		} else {
			fields = append(fields, Field{Key: "reason", Value: err.Error()})
		}
	}
	return r.emit(Event{Level: LevelError, Kind: EventError, Fields: fields})
}

// Detail emits debug-level diagnostic detail. It is invisible with
// debug logging off, which is why it must never carry a required
// control state.
func (r *Reporter) Detail(kind, text string) (string, bool) {
	return r.emit(Event{Level: LevelDebug, Kind: EventDetail, Fields: []Field{
		{Key: "about", Value: kind},
		{Key: "text", Value: text},
	}})
}

// validOwnerState checks one recovery fact against the vocabulary.
func validOwnerState(owner, state string) error {
	switch owner {
	case "native":
		if !nativeStates[state] {
			return fmt.Errorf("unknown native state %s", quote(state))
		}
	case "producer":
		if !deliveryStates[state] {
			return fmt.Errorf("unknown producer state %s", quote(state))
		}
	case "gateway":
		if !gatewayStates[state] {
			return fmt.Errorf("unknown gateway state %s", quote(state))
		}
	default:
		return fmt.Errorf("unknown owner %s", quote(owner))
	}
	return nil
}

// urlUserinfo matches a userinfo credentials part of a URL.
var urlUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/@\s:]+):([^@\s]*)@`)

// secretKeyedValue matches a key=value pair whose key names secret
// material.
var secretKeyedValue = regexp.MustCompile(`(?i)\b((?:password|passwd|secret|token|jwt|nkey|credentials?|api[_-]?key|private[_-]?key)[a-z0-9_-]*)=([^\s]*)`)

// redactedValue replaces the secret material it captures.
const redactedValue = "$1=***"

// redact masks credential material in a rendered value: URL userinfo
// and the values of secret-looking keys. File contents never enter the
// reporter in the first place; this pass bounds the damage of a
// mistaken reason string.
func redact(s string) string {
	if s == "" {
		return s
	}
	s = urlUserinfo.ReplaceAllString(s, "${1}***@")
	return secretKeyedValue.ReplaceAllString(s, redactedValue)
}
