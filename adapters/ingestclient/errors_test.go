package ingestclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sigaev-pro/bc-indexer/adapters/ingestclient/ingest"
	"github.com/sigaev-pro/bc-indexer/config"
	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
)

func TestClassifyActsOnTheRetryRule(t *testing.T) {
	wire := func(action ingest.RetryAction, code ingest.ErrorCode) error {
		return &WireError{Step: "step", Code: code, Action: action}
	}
	cases := []struct {
		name string
		err  error
		want action
	}{
		{"transport unavailable", status.Error(codes.Unavailable, "connection refused"), actionRetry},
		{"transport deadline", status.Error(codes.DeadlineExceeded, "no answer"), actionRetry},
		{"call timeout", ErrCallTimeout, actionRetry},
		{"transient answer", wire(ingest.RetryAction_RETRY_BACKOFF, ingest.ErrorCode_TRANSIENT_UNAVAILABLE), actionRetry},
		{"reconnect", wire(ingest.RetryAction_RECONNECT, ingest.ErrorCode_SESSION_STALE), actionReconnect},
		{"resume at offset", wire(ingest.RetryAction_RESUME_AT_OFFSET, ingest.ErrorCode_OFFSET_MISMATCH), actionResume},
		{"fix config", wire(ingest.RetryAction_FIX_CONFIG, ingest.ErrorCode_BINDING_MISMATCH), actionStop},
		{"stop local owner", wire(ingest.RetryAction_STOP_LOCAL_OWNER, ingest.ErrorCode_STORE_FATAL), actionStop},
		{"drain old build", wire(ingest.RetryAction_DRAIN_OLD_BUILD, ingest.ErrorCode_VERSION_UNSUPPORTED), actionStop},
		{"diagnose immutable", wire(ingest.RetryAction_DIAGNOSE_IMMUTABLE, ingest.ErrorCode_IMMUTABLE_CONFLICT), actionStop},
		{"no action", wire(ingest.RetryAction_RETRY_UNSPECIFIED, ingest.ErrorCode_ALREADY_RETIRED), actionStop},
		// A conflict the registry pairs with RETRY_BACKOFF is not transient.
		{"identity mismatch", wire(ingest.RetryAction_RETRY_BACKOFF, ingest.ErrorCode_IDENTITY_MISMATCH), actionStop},
		{"mode conflict", wire(ingest.RetryAction_RETRY_BACKOFF, ingest.ErrorCode_MODE_CONFLICT), actionStop},
		{"method not implemented", status.Error(codes.Unimplemented, "unknown method"), actionStop},
		{"journal error", producer.ErrCorrupt, actionStop},
		{"wrapped journal error", fmt.Errorf("batch 1: %w", producer.ErrOffsetGap), actionStop},
	}
	for _, tc := range cases {
		if got := classify(tc.err); got != tc.want {
			t.Fatalf("%s: classify = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestWireErrorUnwrapsToTheClass(t *testing.T) {
	cases := []struct {
		action ingest.RetryAction
		code   ingest.ErrorCode
		want   error
	}{
		{ingest.RetryAction_FIX_CONFIG, ingest.ErrorCode_BINDING_MISMATCH, ErrConfigBlocked},
		{ingest.RetryAction_STOP_LOCAL_OWNER, ingest.ErrorCode_STORE_FATAL, ErrBlocked},
		{ingest.RetryAction_DRAIN_OLD_BUILD, ingest.ErrorCode_DRAIN_REQUIRED, ErrBlocked},
		{ingest.RetryAction_DIAGNOSE_IMMUTABLE, ingest.ErrorCode_IMMUTABLE_CONFLICT, ErrBlocked},
		{ingest.RetryAction_RETRY_UNSPECIFIED, ingest.ErrorCode_ALREADY_RETIRED, ErrBlocked},
		{ingest.RetryAction_RETRY_BACKOFF, ingest.ErrorCode_IDENTITY_MISMATCH, ErrBlocked},
		{ingest.RetryAction_RETRY_BACKOFF, ingest.ErrorCode_TRANSIENT_UNAVAILABLE, nil},
		{ingest.RetryAction_RECONNECT, ingest.ErrorCode_SESSION_STALE, nil},
		{ingest.RetryAction_RESUME_AT_OFFSET, ingest.ErrorCode_OFFSET_MISMATCH, nil},
	}
	for _, tc := range cases {
		err := error(&WireError{Step: "PrepareChunk", Code: tc.code, Action: tc.action, Detail: "detail"})
		if tc.want == nil {
			if errors.Unwrap(err) != nil {
				t.Fatalf("%s %s unwraps to %v, want no class", tc.code, tc.action, errors.Unwrap(err))
			}
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s %s is not %v", tc.code, tc.action, tc.want)
		}
		if tc.want == ErrBlocked && errors.Is(err, ErrConfigBlocked) {
			t.Fatalf("%s %s is also CONFIG_BLOCKED", tc.code, tc.action)
		}
	}
	wire := &WireError{Step: "Release", Code: ingest.ErrorCode_MODE_CONFLICT, Action: ingest.RetryAction_RETRY_UNSPECIFIED}
	if wire.Error() == "" || wire.Step != "Release" {
		t.Fatalf("the typed error does not render its step: %q", wire.Error())
	}
}

func TestBackoffDoublesToItsCapWithJitter(t *testing.T) {
	originalBase, originalMax := backoffBaseDelay, backoffMaxDelay
	backoffBaseDelay, backoffMaxDelay = 200*time.Millisecond, 1600*time.Millisecond
	defer func() { backoffBaseDelay, backoffMaxDelay = originalBase, originalMax }()

	base := retryBackoff{random: func() uint64 { return 0 }}
	for attempt, half := range []time.Duration{100, 200, 400, 800, 800, 800} {
		if got := base.next(); got != half*time.Millisecond {
			t.Fatalf("attempt %d without spread: %s, want %d ms", attempt, got, half)
		}
	}
	base.reset()
	if got := base.next(); got != 100*time.Millisecond {
		t.Fatalf("after a reset the sequence starts over: %s", got)
	}
	// The random draw spreads each delay over the upper half of its interval.
	spread := retryBackoff{random: func() uint64 { return 7 }}
	if got := spread.next(); got != 100*time.Millisecond+7 {
		t.Fatalf("first delay with a draw of 7: %s", got)
	}
	// Every delay lies in the upper half of its own interval, and no delay
	// exceeds the cap.
	var b retryBackoff
	interval := backoffBaseDelay
	for i := 0; i < 80; i++ {
		if got := b.next(); got < interval/2 || got > interval || got > backoffMaxDelay {
			t.Fatalf("delay %s is outside [%s, %s]", got, interval/2, interval)
		}
		interval = min(interval*2, backoffMaxDelay)
	}
	// The sequence ends the run when ctx is over and waits otherwise.
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if !b.sleep(ended) {
		t.Fatal("sleep with a ended context must report that the run is over")
	}
	short := retryBackoff{attempt: 63}
	if short.sleep(context.Background()) {
		t.Fatal("sleep with a live context must end")
	}
}

func TestFailureCarriesClassAndExitCode(t *testing.T) {
	wrapped := fmt.Errorf("batch 3: %w", producer.ErrCorrupt)
	cases := []struct {
		err  error
		want config.ExitCode
	}{
		{ErrConfig, config.ExitConfig},
		{fmt.Errorf("%w: %v", ErrConfigBlocked, "BINDING_MISMATCH"), config.ExitConfig},
		{fmt.Errorf("%w: %v", ErrLimitIncompatible, "budget"), config.ExitConfig},
		{fmt.Errorf("%w: %v", ErrBlocked, "ALREADY_RETIRED"), config.ExitState},
		{ErrProtocol, config.ExitUnsupported},
		{ErrInvalidSeed, config.ExitUnsupported},
		{ErrIngestAckMismatch, config.ExitUnsupported},
		{wrapped, config.ExitFatal},
		{errors.New("disk full"), config.ExitFatal},
	}
	for _, tc := range cases {
		failure := newFailure(tc.err)
		var located *config.Error
		if !errors.As(failure, &located) || located.Code != tc.want || located.Field != "delivery" {
			t.Fatalf("%v: located %+v, want code %d on field delivery", tc.err, located, tc.want)
		}
		if config.Exit(failure) != tc.want {
			t.Fatalf("%v: config.Exit = %d, want %d", tc.err, config.Exit(failure), tc.want)
		}
		if !errors.Is(failure, tc.err) {
			t.Fatalf("%v: the cause is not reachable through %v", tc.err, failure)
		}
		if failure.Error() != "config: delivery: "+tc.err.Error() {
			t.Fatalf("the reason changed: %q", failure.Error())
		}
	}
	if !errors.Is(newFailure(wrapped), producer.ErrCorrupt) {
		t.Fatal("the journal error inside the cause must stay reachable")
	}
	// The reporter renders the located error with its exit-code class.
	var out strings.Builder
	config.NewReporter(config.LevelInfo, &out).Failure(newFailure(fmt.Errorf("%w: %v", ErrBlocked, "ALREADY_RETIRED")))
	if !strings.Contains(out.String(), "event=ERROR class=config code=3 field=delivery reason=ingestclient: BLOCKED: ALREADY_RETIRED") {
		t.Fatalf("failure line: %q", out.String())
	}
}

func TestCheckRetiredPrefix(t *testing.T) {
	for _, tc := range []struct {
		gateway, producer uint64
		ok                bool
	}{
		{0, 0, true}, {1, 0, true}, {5, 5, true}, {6, 5, true}, {4, 5, false}, {7, 5, false}, {0, 1, false},
	} {
		err := checkRetiredPrefix(tc.gateway, tc.producer)
		if tc.ok && err != nil {
			t.Fatalf("gateway %d, producer %d: %v, want no error", tc.gateway, tc.producer, err)
		}
		if !tc.ok && !errors.Is(err, ErrBlocked) {
			t.Fatalf("gateway %d, producer %d: %v, want ErrBlocked", tc.gateway, tc.producer, err)
		}
	}
}

func TestRequestIDIsSixteenRandomBytes(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := newRequestID()
		if err != nil || len(id) != 16 {
			t.Fatalf("request id of %d bytes: %v", len(id), err)
		}
		if seen[string(id)] {
			t.Fatal("a request id repeated")
		}
		seen[string(id)] = true
	}
}
