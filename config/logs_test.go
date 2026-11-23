package config

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/compatibility"
)

// Reporter tests (E06-12, AC-OPS-09-01/02): the required control states
// are visible with debug logging off, the vocabulary is exactly the
// existing journal states, no second journal dedups events, secrets are
// redacted, and a SIGKILL never renders a graceful STOPPED.

func TestParseLogLevel(t *testing.T) {
	for _, ok := range []string{"debug", "info", "warn", "error"} {
		if _, err := ParseLogLevel(ok); err != nil {
			t.Fatalf("level %s rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "INFO", "trace", "fatal"} {
		if _, err := ParseLogLevel(bad); err == nil {
			t.Fatalf("level %q accepted", bad)
		}
	}
}

func TestReporterShowsRequiredStatesWithDebugOff(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(LevelInfo, &buf)

	if line, ok, err := r.NativeObservation("obs-7", StateNativeAdmitted); err != nil || !ok {
		t.Fatalf("ADMITTED not reported: %v %v", err, err)
	} else if !strings.Contains(line, "state=ADMITTED") || !strings.Contains(line, "key=obs-7") {
		t.Fatalf("ADMITTED line = %s", line)
	}
	for _, step := range []struct {
		state string
	}{
		{StateNativeExecReady}, {StateNativeDecided},
	} {
		if _, _, err := r.NativeObservation("obs-7", step.state); err != nil {
			t.Fatalf("native state %s rejected: %v", step.state, err)
		}
	}
	if _, _, err := r.DeliveryBatch(3, StateDeliveryReady); err != nil {
		t.Fatalf("READY rejected: %v", err)
	}
	if _, _, err := r.DeliveryBatch(3, StateDeliverySealed); err != nil {
		t.Fatalf("SEALED rejected: %v", err)
	}
	if _, _, err := r.GatewayBatch(3, StateGatewayNATSACKed); err != nil {
		t.Fatalf("NATS_ACKED rejected: %v", err)
	}
	if line, ok := r.RetiredPrefix("producer", 3); !ok || !strings.Contains(line, "state=RETIRED") {
		t.Fatalf("RETIRED not reported: %s", line)
	}
	if line, ok := r.Mode(compatibility.ModeDrain, "drain requested"); !ok || !strings.Contains(line, "mode=DRAIN") {
		t.Fatalf("DRAIN mode not reported: %s", line)
	}
	if line, ok, err := r.Stopped("SIGTERM"); err != nil || !ok || !strings.Contains(line, "event=STOPPED") || !strings.Contains(line, "exit_hint=143") {
		t.Fatalf("graceful STOPPED not reported: %s (%v)", line, err)
	}

	out := buf.String()
	for _, want := range []string{"event=NATIVE", "event=DELIVERY", "event=GATEWAY", "event=MODE", "event=STOPPED"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output misses %s:\n%s", want, out)
		}
	}
}

func TestReporterStartupShowsRecovery(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(LevelInfo, &buf)
	line, ok := r.Startup(RoleLive, "ethereum-mainnet", compatibility.ModeLive, []RecoveredObligation{
		{Owner: "native", State: StateNativeExecReady, Count: 2},
		{Owner: "producer", State: StateDeliverySealed, Count: 1},
		{Owner: "gateway", State: StateGatewayNATSACKed, Count: 1},
	})
	if !ok {
		t.Fatalf("startup not reported")
	}
	for _, want := range []string{"event=STARTUP", "role=live", "profile=ethereum-mainnet",
		"recovered_native_EXEC_READY=2", "recovered_producer_SEALED=1", "recovered_gateway_NATS_ACKED=1"} {
		if !strings.Contains(line, want) {
			t.Fatalf("startup line misses %s: %s", want, line)
		}
	}
	if !strings.Contains(buf.String(), "event=STARTUP") {
		t.Fatalf("startup not written")
	}
}

func TestReporterDebugDetailHiddenAtInfo(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(LevelInfo, &buf)
	if _, ok := r.Detail("paths", "live_dir=/x/y"); ok {
		t.Fatalf("detail rendered at info level")
	}
	if buf.Len() != 0 {
		t.Fatalf("detail wrote output at info level")
	}
	debug := NewReporter(LevelDebug, &buf)
	if _, ok := debug.Detail("paths", "live_dir=/x/y"); !ok {
		t.Fatalf("detail hidden at debug level")
	}
}

func TestReporterRejectsStatesOutsideTheJournals(t *testing.T) {
	r := NewReporter(LevelInfo, nil)
	for _, bad := range []string{"RECEIVED", "WAL_SAVED", "PUBLISHED", "acked", ""} {
		if _, _, err := r.NativeObservation("k", bad); err == nil {
			t.Fatalf("native state %q accepted", bad)
		}
		if _, _, err := r.DeliveryBatch(1, bad); err == nil {
			t.Fatalf("delivery state %q accepted", bad)
		}
		if _, _, err := r.GatewayBatch(1, bad); err == nil {
			t.Fatalf("gateway state %q accepted", bad)
		}
	}
	// A startup recovery fact with an unknown state renders without it.
	line, _ := r.Startup(RoleGateway, "bsc-mainnet", compatibility.ModeLive, []RecoveredObligation{
		{Owner: "native", State: "RECEIVED", Count: 1},
	})
	if strings.Contains(line, "RECEIVED") {
		t.Fatalf("unknown recovery state rendered: %s", line)
	}
}

func TestReporterStateVocabularyMatchesJournals(t *testing.T) {
	// The delivery names must stay the compatibility policy's names.
	pairs := map[string]compatibility.DeliveryState{
		StateDeliveryIntent:       compatibility.StateIntent,
		StateDeliveryStaging:      compatibility.StateStaging,
		StateDeliveryReady:        compatibility.StateReady,
		StateDeliverySealed:       compatibility.StateSealed,
		StateDeliveryAckedCleanup: compatibility.StateAckedCleanup,
		StateDeliveryOutbox:       compatibility.StateOutbox,
	}
	for name, state := range pairs {
		if name != state.String() {
			t.Fatalf("config name %s drifted from compatibility %s", name, state.String())
		}
	}
	modes := map[string]compatibility.Mode{
		StateProducerModeLive:  compatibility.ModeLive,
		StateProducerModeDrain: compatibility.ModeDrain,
	}
	for name, mode := range modes {
		if name != mode.String() {
			t.Fatalf("mode name %s drifted from compatibility %s", name, mode.String())
		}
	}
	// The retired simplified names must not exist anywhere in this
	// package's vocabulary.
	for _, legacy := range []string{"RECEIVED", "WAL_SAVED"} {
		if nativeStates[legacy] || deliveryStates[legacy] || gatewayStates[legacy] {
			t.Fatalf("legacy state %s exists in the vocabulary", legacy)
		}
	}
}

func TestReporterDoesNotDeduplicate(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(LevelInfo, &buf)
	for i := 0; i < 2; i++ {
		if _, _, err := r.DeliveryBatch(5, StateDeliverySealed); err != nil {
			t.Fatalf("sealed rejected: %v", err)
		}
	}
	if got := strings.Count(buf.String(), "state=SEALED"); got != 2 {
		t.Fatalf("identical facts rendered %d times, want 2 (no dedup verifier)", got)
	}
}

func TestReporterSIGKILLIsNotAGracefulStop(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(LevelInfo, &buf)
	for _, cause := range []string{"SIGKILL", "SIGSEGV", "unknown"} {
		if _, _, err := r.Stopped(cause); !errors.Is(err, ErrNotGraceful) {
			t.Fatalf("cause %s reported as graceful", cause)
		}
	}
	if buf.Len() != 0 {
		t.Fatalf("non-graceful termination wrote a STOPPED event:\n%s", buf.String())
	}
	// The restart reports the recovery instead.
	line, ok := r.Startup(RoleLive, "bsc-mainnet", compatibility.ModeDrain, []RecoveredObligation{
		{Owner: "producer", State: StateDeliverySealed, Count: 4},
	})
	if !ok || !strings.Contains(line, "recovered_producer_SEALED=4") {
		t.Fatalf("recovery after SIGKILL not reported: %s", line)
	}
}

func TestReporterStopsOnGracefulSignalsOnly(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(LevelInfo, &buf)
	if _, _, err := r.Stopped("SIGINT"); err != nil {
		t.Fatalf("SIGINT rejected: %v", err)
	}
	if !strings.Contains(buf.String(), "signal=SIGINT exit_hint=130") {
		t.Fatalf("SIGINT line wrong:\n%s", buf.String())
	}
}

func TestReporterRedactsSecrets(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(LevelInfo, &buf)
	_, _, err := r.NativeObservation("nats://user:supersecret@nats.internal:4222", StateNativeAdmitted)
	if err != nil {
		t.Fatalf("observation rejected: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "supersecret") {
		t.Fatalf("URL password leaked:\n%s", out)
	}
	if !strings.Contains(out, "nats://***@nats.internal:4222") {
		t.Fatalf("URL not redacted:\n%s", out)
	}

	line, _ := r.Render(Event{Level: LevelError, Kind: EventError, Fields: []Field{
		{Key: "reason", Value: "credentials_file rejected: jwt=eyJhbGciOi.abc and password=hunter2"},
	}})
	for _, secret := range []string{"eyJhbGciOi.abc", "hunter2"} {
		if strings.Contains(line, secret) {
			t.Fatalf("secret %s leaked: %s", secret, line)
		}
	}
	if !strings.Contains(line, "jwt=***") || !strings.Contains(line, "password=***") {
		t.Fatalf("values not masked: %s", line)
	}

	// Ordinary values stay readable.
	line, _ = r.Render(Event{Level: LevelInfo, Kind: EventDetail, Fields: []Field{
		{Key: "path", Value: "/data/wal/eth"},
	}})
	if !strings.Contains(line, "/data/wal/eth") {
		t.Fatalf("ordinary value masked: %s", line)
	}
}

func TestReporterFailureClasses(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(LevelInfo, &buf)
	cfgErr := &Error{Field: "gateway.nats.stream", Code: ExitConfig, Reason: "mismatch"}
	if line, ok := r.Failure(cfgErr); !ok ||
		!strings.Contains(line, "class=config") || !strings.Contains(line, "code=2") ||
		!strings.Contains(line, "field=gateway.nats.stream") {
		t.Fatalf("config failure line = %s", line)
	}
	if line, ok := r.Failure(errors.New("disk full")); !ok ||
		!strings.Contains(line, "class=runtime") || !strings.Contains(line, "code=5") {
		t.Fatalf("runtime failure line = %s", line)
	}
	if line, ok := r.Failure(nil); !ok {
		t.Fatalf("nil failure dropped")
	} else if !strings.Contains(line, "class=runtime") {
		t.Fatalf("nil failure line = %s", line)
	}
}

func TestReporterConcurrentUse(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(LevelInfo, &buf)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, _ = r.DeliveryBatch(uint64(i), StateDeliveryReady)
			_, _ = r.RetiredPrefix("producer", uint64(i))
			_, _ = r.Failure(errors.New("boom"))
		}(i)
	}
	wg.Wait()
}
