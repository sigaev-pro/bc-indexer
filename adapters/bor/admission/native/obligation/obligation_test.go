package obligation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/extractor/admission"
)

// memStore is a synced in-memory store; durability is the admission core's
// own test subject, not this package's.
type memStore struct {
	data map[string][]byte
	// failSuffix makes a Put of a key with this suffix fail.
	failSuffix string
}

func (m *memStore) Has(key []byte) (bool, error) { _, ok := m.data[string(key)]; return ok, nil }
func (m *memStore) Get(key []byte) ([]byte, error) {
	value, ok := m.data[string(key)]
	if !ok {
		return nil, errors.New("not found")
	}
	return bytes.Clone(value), nil
}
func (m *memStore) Put(key, value []byte) error {
	if m.failSuffix != "" && strings.HasSuffix(string(key), m.failSuffix) {
		return errors.New("store write failed")
	}
	m.data[string(key)] = bytes.Clone(value)
	return nil
}
func (m *memStore) SyncKeyValue() error { return nil }

var errUnknownPin = errors.New("unknown pin")

// world is the fake native side: a pin ledger, capsules and logs.
type world struct {
	calls    []string
	pins     map[string]string
	capsules map[string]string
	closed   map[string]admission.Consumed
	logs     map[string]*fakeLog
	pinErr   error
	sealErr  error
	openErr  error
	verify   func(key string) error
	failure  error
	windows  int
	router   *Router
	readable error
	plainLog bool
}

func newWorld() *world {
	return &world{pins: map[string]string{}, capsules: map[string]string{}, closed: map[string]admission.Consumed{},
		logs: map[string]*fakeLog{}}
}

func (w *world) record(id string) []byte { return []byte("ledger:" + id + ":" + w.pins[id]) }

// Ledger.
func (w *world) Record(id string) ([]byte, string, bool, error) {
	root, ok := w.pins[id]
	if !ok {
		return nil, "", false, nil
	}
	return w.record(id), root, true, nil
}
func (w *world) Readable(string) error { return w.readable }
func (w *world) Release(id string) error {
	w.calls = append(w.calls, "release "+id)
	if _, ok := w.pins[id]; !ok {
		return errUnknownPin
	}
	delete(w.pins, id)
	return nil
}

// admission.Inputs.
func (w *world) CapsuleDigest(key string) (string, error) {
	digest, ok := w.capsules[key]
	if !ok {
		return "", errors.New("no capsule")
	}
	return digest, nil
}
func (w *world) Consumed(key string) (admission.Consumed, error) {
	consumed, ok := w.closed[key]
	if !ok {
		return admission.Consumed{}, errors.New("not closed")
	}
	return consumed, nil
}

// Native.
func (w *world) Pin(key string, c admission.Candidate) error {
	w.calls = append(w.calls, "pin "+key)
	if w.pinErr != nil {
		return w.pinErr
	}
	w.pins[key] = c.ParentRoot
	return nil
}
func (w *world) Seal(key string, c admission.Candidate) error {
	w.calls = append(w.calls, "seal "+key)
	if w.sealErr != nil {
		return w.sealErr
	}
	w.capsules[key] = hexOf("capsule " + key)
	return nil
}
func (w *world) OpenLog(key string) (Log, error) {
	w.calls = append(w.calls, "open "+key)
	if w.openErr != nil {
		return nil, w.openErr
	}
	log := w.logs[key]
	if log == nil {
		log = &fakeLog{world: w, key: key, saved: map[string][]byte{}}
		w.logs[key] = log
	}
	log.cursor = 0
	if w.plainLog {
		return struct{ Log }{log}, nil
	}
	return log, nil
}
func (w *world) OpenWindow(admission.Candidate) {
	open := w.router != nil && w.router.open
	w.windows++
	w.calls = append(w.calls, fmt.Sprintf("purge window=%t", open))
}
func (w *world) CloseWindow() {
	w.windows--
	w.calls = append(w.calls, "close window")
}
func (w *world) Verify(key string) (error, error) {
	w.calls = append(w.calls, "verify "+key)
	if w.failure != nil {
		return nil, w.failure
	}
	if w.verify != nil {
		return w.verify(key), nil
	}
	return nil, nil
}

// fakeLog replays saved responses in order and appends new ones.
type fakeLog struct {
	world    *world
	key      string
	order    []string
	saved    map[string][]byte
	cursor   int
	refuse   error
	finish   error
	fetched  int
	finished bool
}

func (l *fakeLog) Next(request []byte, fetch func() ([]byte, error)) ([]byte, error) {
	if l.refuse != nil {
		return nil, l.refuse
	}
	if l.cursor < len(l.order) {
		if l.order[l.cursor] != string(request) {
			return nil, errors.New("prefix conflict")
		}
		l.cursor++
		return l.saved[string(request)], nil
	}
	if l.finished {
		return nil, errors.New("log closed")
	}
	body, err := fetch()
	if err != nil {
		return nil, err
	}
	l.fetched++
	l.order = append(l.order, string(request))
	l.saved[string(request)] = bytes.Clone(body)
	l.cursor++
	return body, nil
}

func (l *fakeLog) Finish() error {
	l.world.calls = append(l.world.calls, "finish "+l.key)
	if l.finish != nil {
		return l.finish
	}
	capsule := l.world.capsules[l.key]
	chain := hexOf(fmt.Sprintf("chain %s %d", l.key, len(l.order)))
	digest, err := admission.ConsumedDigest(capsule, uint64(len(l.order)), chain)
	if err != nil {
		return err
	}
	l.world.closed[l.key] = admission.Consumed{Observation: l.key, CapsuleDigest: capsule, Count: uint64(len(l.order)),
		Chain: chain, Digest: digest}
	l.finished = true
	return nil
}

func (l *fakeLog) Cursor() uint64   { return uint64(l.cursor) }
func (l *fakeLog) Frontier() uint64 { return uint64(len(l.order)) }

func hexOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// heimdall answers fixed bodies by path; status overrides the status code.
type heimdall struct {
	bodies map[string]string
	status int
	calls  []string
}

func (h *heimdall) RoundTrip(req *http.Request) (*http.Response, error) {
	h.calls = append(h.calls, req.URL.RequestURI())
	status := http.StatusOK
	if h.status != 0 {
		status = h.status
	}
	body := h.bodies[req.URL.Path]
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

type fixture struct {
	t      *testing.T
	store  *memStore
	world  *world
	guard  *admission.Guard
	adm    *admission.Admission
	driver *Driver
	stub   *heimdall
}

const host = "heimdall.test"

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, store: &memStore{data: map[string][]byte{}}, world: newWorld(), guard: admission.NewGuard(),
		stub: &heimdall{bodies: map[string]string{"/bor/spans/1": `{"span":1}`, "/status": `{"ok":true}`,
			"/milestones/latest": `{"m":1}`}}}
	f.reopen(true)
	return f
}

// reopen opens the admission again on the same store, as a restart does.
func (f *fixture) reopen(live bool) {
	f.t.Helper()
	token := f.guard.Acquire()
	defer token.Release()
	adm, err := admission.Open(token, f.store, admission.Producer{ProducerID: "bor", RunID: "run-1"},
		PinAdapter{Ledger: f.world, Unknown: errUnknownPin}, f.world)
	if err != nil {
		f.t.Fatal(err)
	}
	if live {
		if err = adm.GoLive(token); err != nil {
			f.t.Fatal(err)
		}
	}
	router := &Router{Inner: f.stub, Host: host}
	f.world.router = router
	f.adm = adm
	f.driver = &Driver{Admission: adm, Guard: f.guard, Native: f.world, Router: router}
}

func candidate(n uint64) admission.Candidate {
	return admission.Candidate{Number: n, Hash: hexOf(fmt.Sprint("block ", n)), ParentHash: hexOf(fmt.Sprint("block ", n-1)),
		ParentRoot: hexOf(fmt.Sprint("root ", n-1))}
}

func (f *fixture) states() map[string]admission.State {
	f.t.Helper()
	token := f.guard.Acquire()
	defer token.Release()
	list, err := f.adm.Observations(token)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]admission.State{}
	for _, obs := range list {
		out[obs.Key] = obs.State
	}
	return out
}

func (f *fixture) mode() admission.Mode {
	f.t.Helper()
	token := f.guard.Acquire()
	defer token.Release()
	mode, err := f.adm.Mode(token)
	if err != nil {
		f.t.Fatal(err)
	}
	return mode
}

func get(t *testing.T, path string) (string, error) {
	t.Helper()
	req := &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "http", Host: host, Path: path}, Header: http.Header{}}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err == nil && resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("status %d", resp.StatusCode)
	}
	return string(body), err
}

// useRouter installs the router as the process transport for one test.
func useRouter(t *testing.T, r *Router) {
	previous := http.DefaultTransport
	http.DefaultTransport = r
	t.Cleanup(func() { http.DefaultTransport = previous })
}

const key0 = "bor.run-1.0000000000000000"

func TestObserveAdmitsBeforeExecutionAndBindsExecReady(t *testing.T) {
	f := newFixture(t)
	useRouter(t, f.driver.Router)
	obs, err := f.driver.Observe(candidate(1))
	if err != nil {
		t.Fatal(err)
	}
	if obs.Key() != key0 {
		t.Fatalf("key %s", obs.Key())
	}
	want := []string{"pin " + key0, "seal " + key0, "open " + key0, "purge window=true", "verify " + key0}
	if !slices.Equal(f.world.calls, want) {
		t.Fatalf("calls %v, want %v", f.world.calls, want)
	}
	// The observation holds the guard until Done; read the store directly.
	if ok, _ := f.store.Has([]byte("extractor-admission-v1/bor.run-1/0000000000000000/admitted")); !ok {
		t.Fatalf("no durable ADMITTED record before execution")
	}
	if _, err := f.store.Get([]byte("extractor-admission-v1/bor.run-1/0000000000000000/exec-ready")); err == nil {
		t.Fatalf("EXEC_READY before Done")
	}
	if body, err := get(t, "/bor/spans/1"); err != nil || body != `{"span":1}` {
		t.Fatalf("span read %q %v", body, err)
	}
	result := ResultDigest("h", "s", "r", "1")
	if err := obs.Done(result, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.states()[key0]; got != admission.ExecReady {
		t.Fatalf("state after Done %s", got)
	}
	token := f.guard.Acquire()
	list, _ := f.adm.Observations(token)
	token.Release()
	ready := list[0].ExecReady
	if ready == nil || ready.ExecDigest != result || ready.ConsumedDigest != f.world.closed[key0].Digest {
		t.Fatalf("EXEC_READY %+v does not bind the result and the consumed manifest", ready)
	}
	if f.world.closed[key0].Count != 1 || f.world.logs[key0].fetched != 1 {
		t.Fatalf("the counted read did not go through the log")
	}
	if _, ok := f.world.pins[key0]; !ok {
		t.Fatalf("EXEC_READY released the pin")
	}
	if err := obs.Done(result, nil); !errors.Is(err, ErrWindow) || errors.Is(err, admission.ErrGuard) {
		t.Fatalf("second Done: %v", err)
	}
}

func TestObserveAbandonsBeforeAdmit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*world)
		pins  int
	}{
		{"pin", func(w *world) { w.pinErr = errors.New("pin failed") }, 0},
		{"seal", func(w *world) { w.sealErr = errors.New("seal failed") }, 0},
		{"admit", func(w *world) { w.readable = errors.New("trie node missing") }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.setup(f.world)
			if _, err := f.driver.Observe(candidate(1)); err == nil {
				t.Fatal("expected an error")
			}
			acquired := make(chan *admission.Token, 1)
			go func() { acquired <- f.guard.Acquire() }()
			select {
			case token := <-acquired:
				token.Release()
			case <-time.After(5 * time.Second):
				t.Fatal("a failed Observe kept the observation guard")
			}
			if len(f.states()) != 0 {
				t.Fatalf("observation left open: %v", f.states())
			}
			if len(f.world.pins) != tc.pins {
				t.Fatalf("pins left: %v", f.world.pins)
			}
			if !slices.Contains(f.world.calls, "release "+key0) {
				t.Fatalf("Abandon did not release the pin: %v", f.world.calls)
			}
			if slices.Contains(f.world.calls, "open "+key0) {
				t.Fatalf("a log window opened before ADMITTED")
			}
			// The guard is free and a new observation can begin.
			f.world.pinErr, f.world.sealErr, f.world.readable = nil, nil, nil
			obs, err := f.driver.Observe(candidate(1))
			if err != nil {
				t.Fatal(err)
			}
			if err := obs.Done(ResultDigest("a", "b", "c", "0"), nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestObserveRejectsAFailedHeaderCheck(t *testing.T) {
	f := newFixture(t)
	bad := errors.New("unauthorized signer")
	f.world.verify = func(string) error { return bad }
	if _, err := f.driver.Observe(candidate(1)); !errors.Is(err, bad) {
		t.Fatalf("Observe: %v", err)
	}
	if len(f.states()) != 0 {
		t.Fatalf("rejected observation still open: %v", f.states())
	}
	record, err := f.store.Get([]byte("extractor-admission-v1/bor.run-1/0000000000000000/rejected"))
	if err != nil || !strings.Contains(string(record), RejectionDigest(key0, candidate(1).Hash, bad.Error())) {
		t.Fatalf("rejection record %s %v", record, err)
	}
	if _, ok := f.world.pins[key0]; ok {
		t.Fatalf("Reject kept the pin")
	}
	if _, err := f.driver.Observe(candidate(1)); err == nil || f.driver.Router.open {
		t.Fatalf("guard or window not released after a rejected check")
	}
}

func TestDoneRejectsANativeValidationFailure(t *testing.T) {
	f := newFixture(t)
	obs, err := f.driver.Observe(candidate(1))
	if err != nil {
		t.Fatal(err)
	}
	bad := errors.New("invalid merkle root")
	if err := obs.Done("", bad); !errors.Is(err, bad) {
		t.Fatalf("Done: %v", err)
	}
	if len(f.states()) != 0 || len(f.world.pins) != 0 {
		t.Fatalf("rejected observation kept state %v or pin %v", f.states(), f.world.pins)
	}
	if slices.Contains(f.world.calls, "finish "+key0) {
		t.Fatalf("a rejected observation closed its log")
	}
}

func TestSourceFailureBeforeANativeFailureKeepsTheObligation(t *testing.T) {
	f := newFixture(t)
	useRouter(t, f.driver.Router)
	obs, err := f.driver.Observe(candidate(1))
	if err != nil {
		t.Fatal(err)
	}
	f.stub.status = http.StatusServiceUnavailable
	if _, err := get(t, "/bor/spans/1"); err == nil || err.Error() != "status 503" {
		t.Fatalf("first read: %v", err)
	}
	err = obs.Done("", errors.New("error while committing span"))
	if !errors.Is(err, ErrInput) {
		t.Fatalf("Done: %v", err)
	}
	if f.states()[key0] != admission.Admitted || f.world.pins[key0] == "" {
		t.Fatalf("a source failure closed the obligation: %v", f.states())
	}
	if f.world.windows != 0 {
		t.Fatalf("the window stayed open")
	}
	if _, err := f.driver.Observe(candidate(2)); !errors.Is(err, admission.ErrPending) {
		t.Fatalf("a new observation passed over the ADMITTED one: %v", err)
	}
}

func TestErrorStatusReachesBorUnlogged(t *testing.T) {
	f := newFixture(t)
	useRouter(t, f.driver.Router)
	obs, err := f.driver.Observe(candidate(1))
	if err != nil {
		t.Fatal(err)
	}
	f.stub.status = http.StatusServiceUnavailable
	f.stub.bodies["/bor/spans/1"] = "unavailable"
	req := &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "http", Host: host, Path: "/bor/spans/1"}}
	resp, err := f.driver.Router.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Bor did not receive the native status: %v %v", resp, err)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "unavailable" || len(f.world.logs[key0].order) != 0 {
		t.Fatalf("body %q, saved %v", body, f.world.logs[key0].order)
	}
	if err := obs.Done("", errors.New("span 1 not available")); !errors.Is(err, ErrInput) {
		t.Fatalf("Done: %v", err)
	}
}

func TestSourceFailureRetriedThroughTheLog(t *testing.T) {
	f := newFixture(t)
	useRouter(t, f.driver.Router)
	obs, err := f.driver.Observe(candidate(1))
	if err != nil {
		t.Fatal(err)
	}
	f.stub.status = http.StatusServiceUnavailable
	if _, err := get(t, "/bor/spans/1"); err == nil || err.Error() != "status 503" {
		t.Fatalf("first read: %v", err)
	}
	f.stub.status = 0
	if body, err := get(t, "/bor/spans/1"); err != nil || body != `{"span":1}` {
		t.Fatalf("retry: %q %v", body, err)
	}
	if _, err := get(t, "/status"); err != nil {
		t.Fatal(err)
	}
	rows := f.driver.Router.Rows()
	if len(rows) != 3 || rows[0].Error == "" || rows[1].Sequence != 0 || rows[1].Origin != "fetch" || rows[2].Sequence != 1 {
		t.Fatalf("rows %+v", rows)
	}
	if err := obs.Done(ResultDigest("a", "b", "c", "0"), nil); err != nil {
		t.Fatalf("Done after a successful retry: %v", err)
	}
	if f.states()[key0] != admission.ExecReady || f.world.closed[key0].Count != 2 {
		t.Fatalf("state %v, consumed %+v", f.states(), f.world.closed[key0])
	}
}

func TestLogRefusalStopsTheWindow(t *testing.T) {
	f := newFixture(t)
	useRouter(t, f.driver.Router)
	obs, err := f.driver.Observe(candidate(1))
	if err != nil {
		t.Fatal(err)
	}
	f.world.logs[key0].refuse = errors.New("dependency log prefix conflict")
	if _, err := get(t, "/bor/spans/1"); !errors.Is(err, ErrInput) {
		t.Fatalf("first read: %v", err)
	}
	f.world.logs[key0].refuse = nil
	if _, err := get(t, "/status"); !errors.Is(err, ErrInput) {
		t.Fatalf("a read after a log refusal was not refused: %v", err)
	}
	if err := obs.Done(ResultDigest("a", "b", "c", "0"), nil); !errors.Is(err, ErrInput) {
		t.Fatalf("Done: %v", err)
	}
	if f.states()[key0] != admission.Admitted {
		t.Fatalf("state %v", f.states())
	}
	rows := f.driver.Router.Rows()
	if len(rows) != 2 || rows[0].Error == "" || rows[1].Error == "" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestInfrastructureFailuresKeepTheObligation(t *testing.T) {
	f := newFixture(t)
	f.world.failure = errors.New("capsule read failed")
	if _, err := f.driver.Observe(candidate(1)); !errors.Is(err, f.world.failure) {
		t.Fatalf("Observe: %v", err)
	}
	if f.states()[key0] != admission.Admitted || f.world.windows != 0 {
		t.Fatalf("state %v, windows %d", f.states(), f.world.windows)
	}
	f.world.failure = nil
	f.driver.Reexecutor = &reexec{result: ResultDigest("h", "s", "r", "1")}
	obs, err := f.driver.Observe(candidate(2))
	if err != nil {
		t.Fatalf("the saved obligation was not resolved before Begin: %v", err)
	}
	missing := errors.New("parent state unavailable")
	if err := obs.Fail(missing); !errors.Is(err, missing) {
		t.Fatalf("Fail: %v", err)
	}
	if f.states()[key0] != admission.ExecReady {
		t.Fatalf("the saved obligation was not resolved: %v", f.states())
	}
	if f.states()["bor.run-1.0000000000000001"] != admission.Admitted || f.world.windows != 0 {
		t.Fatalf("Fail closed the obligation: %v", f.states())
	}
	if err := obs.Fail(nil); !errors.Is(err, ErrWindow) {
		t.Fatalf("second Fail: %v", err)
	}
}

func TestResolveFailureStopsTheNextObservation(t *testing.T) {
	f := newFixture(t)
	f.world.failure = errors.New("capsule read failed")
	if _, err := f.driver.Observe(candidate(1)); err == nil {
		t.Fatal("expected an error")
	}
	f.driver.Reexecutor = &reexec{err: errors.New("state unavailable")}
	f.world.failure = nil
	if _, err := f.driver.Observe(candidate(2)); err == nil || errors.Is(err, admission.ErrPending) {
		t.Fatalf("Observe: %v", err)
	}
	if len(f.states()) != 1 || f.world.windows != 0 {
		t.Fatalf("states %v windows %d", f.states(), f.world.windows)
	}
}

func TestFinishFailureKeepsTheObligation(t *testing.T) {
	f := newFixture(t)
	obs, err := f.driver.Observe(candidate(1))
	if err != nil {
		t.Fatal(err)
	}
	unconsumed := errors.New("durable responses were not consumed")
	f.world.logs[key0].finish = unconsumed
	if err := obs.Done(ResultDigest("a", "b", "c", "0"), nil); !errors.Is(err, unconsumed) {
		t.Fatalf("Done: %v", err)
	}
	if f.states()[key0] != admission.Admitted {
		t.Fatalf("state %v", f.states())
	}
}

type reexec struct {
	result string
	native error
	err    error
	keys   []string
	before func()
}

func (r *reexec) Reexecute(key string, c admission.Candidate) (string, error, error) {
	r.keys = append(r.keys, key)
	if r.before != nil {
		r.before()
	}
	return r.result, r.native, r.err
}

// leaveAdmitted leaves one obligation ADMITTED with one saved counted read,
// as a crash after the closing ADMITTED sync does.
func leaveAdmitted(t *testing.T, f *fixture) {
	t.Helper()
	useRouter(t, f.driver.Router)
	if _, err := f.driver.Observe(candidate(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, "/bor/spans/1"); err != nil {
		t.Fatal(err)
	}
	// Lost before Done: the guard and window die with the process.
	f.guard = admission.NewGuard()
	f.world.windows = 0
	f.reopen(false)
	useRouter(t, f.driver.Router)
}

func TestRecoverExecReadyFromSavedInputs(t *testing.T) {
	f := newFixture(t)
	leaveAdmitted(t, f)
	f.world.calls = nil
	f.stub.bodies["/bor/spans/1"] = `{"span":"changed"}`
	var seen string
	r := &reexec{result: ResultDigest("h", "s", "r", "2"), before: func() {
		seen, _ = get(t, "/bor/spans/1")
	}}
	report, err := f.driver.Recover(r)
	if err != nil {
		t.Fatal(err)
	}
	if seen != `{"span":1}` {
		t.Fatalf("recovery read %q, not the saved response", seen)
	}
	if !slices.Equal(report.ExecReady, []string{key0}) || f.states()[key0] != admission.ExecReady {
		t.Fatalf("report %+v states %v", report, f.states())
	}
	if f.mode() != admission.Live {
		t.Fatalf("recovery did not go live")
	}
	if !slices.Contains(f.world.calls, "verify "+key0) || !slices.Equal(r.keys, []string{key0}) {
		t.Fatalf("recovery skipped the header check or execution: %v", f.world.calls)
	}
}

func TestRecoverRejectsAndAbandons(t *testing.T) {
	f := newFixture(t)
	leaveAdmitted(t, f)
	bad := errors.New("invalid gas used")
	report, err := f.driver.Recover(&reexec{native: bad})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(report.Rejected, []string{key0}) || len(f.states()) != 0 {
		t.Fatalf("report %+v states %v", report, f.states())
	}
	// An INPUT_PREPARING observation (lost before Admit) is abandoned.
	f.world.sealErr = errors.New("lost")
	token := f.guard.Acquire()
	begun, err := f.adm.Begin(token, candidate(2))
	token.Release()
	if err != nil {
		t.Fatal(err)
	}
	f.reopen(false)
	report, err = f.driver.Recover(&reexec{})
	if err != nil || !slices.Equal(report.Abandoned, []string{begun.Key}) || f.mode() != admission.Live {
		t.Fatalf("report %+v err %v", report, err)
	}
}

func TestRecoverRejectsAFailedHeaderCheck(t *testing.T) {
	f := newFixture(t)
	leaveAdmitted(t, f)
	bad := errors.New("unauthorized signer")
	f.world.verify = func(string) error { return bad }
	r := &reexec{result: ResultDigest("h", "s", "r", "2")}
	report, err := f.driver.Recover(r)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(report.Rejected, []string{key0}) || len(r.keys) != 0 || f.mode() != admission.Live {
		t.Fatalf("report %+v, re-executed %v, mode %s", report, r.keys, f.mode())
	}
}

func TestRecoverRejectWriteFailureIsNotARejection(t *testing.T) {
	f := newFixture(t)
	leaveAdmitted(t, f)
	f.store.failSuffix = "/rejected"
	report, err := f.driver.Recover(&reexec{native: errors.New("invalid gas used")})
	if err == nil || len(report.Rejected) != 0 {
		t.Fatalf("report %+v err %v", report, err)
	}
}

func TestRecoverFailureStaysInRecovery(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    *reexec
		prep func(*fixture)
	}{
		{"reexecution error", &reexec{err: errors.New("state unavailable")}, func(*fixture) {}},
		{"open error", &reexec{}, func(f *fixture) { f.world.openErr = errors.New("corrupt log") }},
		{"input failure", &reexec{}, func(f *fixture) { f.world.logs[key0].refuse = errors.New("prefix conflict") }},
		{"input failure before a native failure", &reexec{native: errors.New("span commit failed")},
			func(f *fixture) { f.world.logs[key0].refuse = errors.New("prefix conflict") }},
		{"source failure before a native failure", &reexec{native: errors.New("state sync failed")},
			func(f *fixture) { f.stub.status = http.StatusServiceUnavailable; f.world.logs[key0].order = nil }},
		{"capsule failure", &reexec{}, func(f *fixture) { f.world.failure = errors.New("capsule read failed") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			leaveAdmitted(t, f)
			tc.prep(f)
			if strings.Contains(tc.name, "failure before") || tc.name == "input failure" {
				tc.r.before = func() { get(t, "/bor/spans/1") }
			}
			report, err := f.driver.Recover(tc.r)
			if err == nil || len(report.Rejected) != 0 || len(report.ExecReady) != 0 {
				t.Fatalf("report %+v err %v", report, err)
			}
			if f.mode() != admission.RecoveryOnly || f.states()[key0] != admission.Admitted {
				t.Fatalf("mode %s states %v", f.mode(), f.states())
			}
			if f.driver.Router.open || f.world.windows != 0 {
				t.Fatalf("window left open")
			}
		})
	}
}

func TestRecoverKeepsExecReady(t *testing.T) {
	f := newFixture(t)
	obs, err := f.driver.Observe(candidate(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := obs.Done(ResultDigest("a", "b", "c", "0"), nil); err != nil {
		t.Fatal(err)
	}
	f.reopen(false)
	report, err := f.driver.Recover(&reexec{})
	if err != nil || !slices.Equal(report.Kept, []string{key0}) || len(report.ExecReady) != 0 {
		t.Fatalf("report %+v err %v", report, err)
	}
}

func TestPinAdapter(t *testing.T) {
	w := newWorld()
	p := PinAdapter{Ledger: w, Unknown: errUnknownPin}
	w.pins["a"] = "root"
	digest, err := p.Verify("a", "root")
	if err != nil || digest != hexOf("ledger:a:root") {
		t.Fatalf("digest %s %v", digest, err)
	}
	if _, err := p.Verify("b", "root"); !errors.Is(err, ErrPinMissing) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := p.Verify("a", "other"); !errors.Is(err, ErrPinRoot) {
		t.Fatalf("root: %v", err)
	}
	w.readable = errors.New("missing trie node")
	if _, err := p.Verify("a", "root"); !errors.Is(err, w.readable) {
		t.Fatalf("readable: %v", err)
	}
	if err := p.Release("a"); err != nil {
		t.Fatal(err)
	}
	if err := p.Release("a"); err != nil {
		t.Fatalf("absent pin: %v", err)
	}
	failing := PinAdapter{Ledger: failingLedger{}, Unknown: errUnknownPin}
	if err := failing.Release("a"); err == nil || errors.Is(err, errUnknownPin) {
		t.Fatalf("store error swallowed: %v", err)
	}
	if _, err := failing.Verify("a", "root"); err == nil {
		t.Fatalf("record error swallowed")
	}
}

type failingLedger struct{}

func (failingLedger) Record(string) ([]byte, string, bool, error) {
	return nil, "", false, errors.New("io")
}
func (failingLedger) Readable(string) error { return nil }
func (failingLedger) Release(string) error  { return errors.New("io") }

func TestAbandonWithoutPin(t *testing.T) {
	f := newFixture(t)
	f.world.pinErr = errors.New("trie commit failed")
	if _, err := f.driver.Observe(candidate(1)); err == nil {
		t.Fatal("expected an error")
	}
	if len(f.states()) != 0 {
		t.Fatalf("Abandon without a pin failed: %v", f.states())
	}
}

func TestDigests(t *testing.T) {
	frame := func(tag string, values ...string) string {
		var buf bytes.Buffer
		buf.WriteString(tag)
		buf.WriteByte(0)
		for _, v := range values {
			buf.Write([]byte{0, 0, 0, 0, 0, 0, byte(len(v) >> 8), byte(len(v))})
			buf.WriteString(v)
		}
		sum := sha256.Sum256(buf.Bytes())
		return hex.EncodeToString(sum[:])
	}
	if got := RejectionDigest("k", "h", "e"); got != frame("extractor-bor-native-rejection-v1", "k", "h", "e") {
		t.Fatalf("rejection digest %s", got)
	}
	long := strings.Repeat("x", 300)
	if got := ResultDigest(long, "s", "r", "21000"); got != frame("extractor-bor-native-result-v1", long, "s", "r", "21000") {
		t.Fatalf("result digest %s", got)
	}
	if ResultDigest("a", "b", "c", "d") == ResultDigest("ab", "", "c", "d") {
		t.Fatalf("fields are not length-prefixed")
	}
}

func TestCounted(t *testing.T) {
	for path, want := range map[string]bool{
		"/clerk/time": true, "/bor/spans/0": true, "/bor/spans/12": true, "/bor/spans/latest": true, "/status": true,
		"/bor/spans/": false, "/bor/spans/1x": false, "/milestones/latest": false, "/checkpoints/latest": false,
	} {
		if got := Counted(&url.URL{Path: path}); got != want {
			t.Errorf("Counted(%s) = %t", path, got)
		}
	}
}

func TestRouter(t *testing.T) {
	stub := &heimdall{bodies: map[string]string{"/bor/spans/1": "one", "/milestones/latest": "m", "/big": ""}}
	r := &Router{Inner: stub, Host: host}
	useRouter(t, r)
	if body, err := get(t, "/bor/spans/1"); err != nil || body != "one" {
		t.Fatalf("outside: %q %v", body, err)
	}
	if err, _ := r.Leave(); !errors.Is(err, ErrWindow) {
		t.Fatalf("Leave without Enter: %v", err)
	}
	log := &fakeLog{world: newWorld(), saved: map[string][]byte{}}
	if err := r.Enter(nil); !errors.Is(err, ErrWindow) {
		t.Fatalf("nil log: %v", err)
	}
	var after []Row
	r.AfterCounted = func(row Row) { after = append(after, row) }
	if err := r.Enter(log); err != nil {
		t.Fatal(err)
	}
	if err := r.Enter(log); !errors.Is(err, ErrWindow) {
		t.Fatalf("second Enter: %v", err)
	}
	if body, err := get(t, "/bor/spans/1"); err != nil || body != "one" {
		t.Fatalf("inside: %q %v", body, err)
	}
	if body, err := get(t, "/milestones/latest"); err != nil || body != "m" {
		t.Fatalf("uncounted inside: %q %v", body, err)
	}
	if err, _ := r.Leave(); err != nil {
		t.Fatal(err)
	}
	// A second window on the same log replays the saved bytes.
	stub.bodies["/bor/spans/1"] = "changed"
	log.cursor = 0
	if err := r.Enter(log); err != nil {
		t.Fatal(err)
	}
	if body, err := get(t, "/bor/spans/1"); err != nil || body != "one" {
		t.Fatalf("replay: %q %v", body, err)
	}
	rows := r.Rows()
	if len(rows) != 1 || rows[0].Origin != "replay" || rows[0].Request != "GET /bor/spans/1" ||
		rows[0].RequestSHA256 != hexOf("GET /bor/spans/1") || rows[0].ResponseSHA256 != hexOf("one") || rows[0].ResponseLength != 3 {
		t.Fatalf("rows %+v", rows)
	}
	if len(after) != 2 || after[0].Origin != "fetch" || after[1].Origin != "replay" {
		t.Fatalf("AfterCounted rows %+v", after)
	}
	if log.fetched != 1 {
		t.Fatalf("fetched %d", log.fetched)
	}
	if err, _ := r.Leave(); err != nil {
		t.Fatal(err)
	}
	passed := r.Passed()
	if passed["/bor/spans/1"] != 1 || passed["/milestones/latest"] != 1 || len(passed) != 2 {
		t.Fatalf("passed %v", passed)
	}
	// Another host never reaches the log.
	other := &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "http", Host: "other", Path: "/status"}}
	if _, err := r.RoundTrip(other); err != nil || r.Passed()["/status"] != 0 {
		t.Fatalf("other host: %v %v", err, r.Passed())
	}
}

func TestRouterRefusesOversizeResponses(t *testing.T) {
	stub := &heimdall{bodies: map[string]string{"/status": strings.Repeat("x", MaxResponseBytes+1)}}
	r := &Router{Inner: stub, Host: host}
	useRouter(t, r)
	log := &fakeLog{world: newWorld(), saved: map[string][]byte{}}
	if err := r.Enter(log); err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, "/status"); !errors.Is(err, ErrInput) {
		t.Fatalf("oversize: %v", err)
	}
	if fault, source := r.Leave(); fault != nil || !errors.Is(source, ErrInput) {
		t.Fatalf("Leave: %v %v", fault, source)
	}
	stub.bodies["/status"] = strings.Repeat("x", MaxResponseBytes)
	if err := r.Enter(log); err != nil {
		t.Fatal(err)
	}
	if body, err := get(t, "/status"); err != nil || len(body) != MaxResponseBytes {
		t.Fatalf("largest allowed response: %d %v", len(body), err)
	}
	if err, _ := r.Leave(); err != nil {
		t.Fatal(err)
	}
}

// execReady leaves key0 EXEC_READY after the counted reads of paths.
func execReady(t *testing.T, f *fixture, result string, paths ...string) {
	t.Helper()
	useRouter(t, f.driver.Router)
	obs, err := f.driver.Observe(candidate(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if _, err := get(t, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := obs.Done(result, nil); err != nil {
		t.Fatal(err)
	}
}

func requireGuardFree(t *testing.T, f *fixture) {
	t.Helper()
	acquired := make(chan *admission.Token, 1)
	go func() { acquired <- f.guard.Acquire() }()
	select {
	case token := <-acquired:
		token.Release()
	case <-time.After(5 * time.Second):
		t.Fatal("Replay kept the observation guard")
	}
}

func TestReplayExecReadyFromItsSavedInputs(t *testing.T) {
	f := newFixture(t)
	result := ResultDigest("h", "s", "r", "2")
	execReady(t, f, result, "/bor/spans/1", "/status")
	f.stub.bodies["/bor/spans/1"] = `{"span":"changed"}`
	stubCalls := len(f.stub.calls)
	f.world.calls = nil
	var seen []string
	r := &reexec{result: result, before: func() {
		for _, path := range []string{"/bor/spans/1", "/status", "/milestones/latest"} {
			body, err := get(t, path)
			if err != nil {
				t.Fatal(err)
			}
			seen = append(seen, body)
		}
	}}
	err := f.driver.Replay(key0, r)
	requireGuardFree(t, f)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seen, []string{`{"span":1}`, `{"ok":true}`, `{"m":1}`}) {
		t.Fatalf("replay read %q, not the saved responses", seen)
	}
	// Only the uncounted milestone read reached Heimdall.
	if !slices.Equal(f.stub.calls[stubCalls:], []string{"/milestones/latest"}) {
		t.Fatalf("replay reached Heimdall: %v", f.stub.calls[stubCalls:])
	}
	if !slices.Equal(f.world.calls, []string{"open " + key0, "purge window=true", "verify " + key0, "close window"}) ||
		f.world.windows != 0 || !slices.Equal(r.keys, []string{key0}) {
		t.Fatalf("calls %v windows %d keys %v", f.world.calls, f.world.windows, r.keys)
	}
	if f.states()[key0] != admission.ExecReady || f.world.pins[key0] == "" {
		t.Fatalf("replay changed the observation: %v pins %v", f.states(), f.world.pins)
	}
	if f.driver.Router.open {
		t.Fatal("replay left the router window open")
	}
}

func TestReplayDetectsADifferentInputHistory(t *testing.T) {
	result := ResultDigest("h", "s", "r", "2")
	for _, tc := range []struct {
		name  string
		saved []string
		reads []string
		want  string
	}{
		// A read of another component entered the log during the window.
		{"unconsumed entry", []string{"/bor/spans/1", "/status"}, []string{"/bor/spans/1"}, "consumed 1 of 2"},
		{"prefix conflict", []string{"/status", "/bor/spans/1"}, []string{"/bor/spans/1", "/status"}, "prefix conflict"},
		{"read past the closed log", []string{"/bor/spans/1"}, []string{"/bor/spans/1", "/status"}, "log closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			execReady(t, f, result, tc.saved...)
			stubCalls := len(f.stub.calls)
			r := &reexec{result: result, before: func() {
				for _, path := range tc.reads {
					_, _ = get(t, path)
				}
			}}
			err := f.driver.Replay(key0, r)
			requireGuardFree(t, f)
			if !errors.Is(err, ErrReplay) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
			if len(f.stub.calls) != stubCalls {
				t.Fatalf("replay fetched %v", f.stub.calls[stubCalls:])
			}
			if f.states()[key0] != admission.ExecReady || f.world.windows != 0 {
				t.Fatalf("states %v windows %d", f.states(), f.world.windows)
			}
		})
	}
}

func TestReplayDetectsAnotherResult(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fixture, *reexec)
		want  string
		exec  bool
	}{
		{"result digest", func(f *fixture, r *reexec) { r.result = ResultDigest("x", "s", "r", "2") }, "result", true},
		{"native failure", func(f *fixture, r *reexec) { r.native = errors.New("invalid gas used") }, "invalid gas used", true},
		{"other failure", func(f *fixture, r *reexec) { r.err = errors.New("missing trie node") }, "missing trie node", true},
		{"header check", func(f *fixture, r *reexec) {
			f.world.verify = func(string) error { return errors.New("unauthorized signer") }
		}, "unauthorized signer", false},
		{"header check failure", func(f *fixture, r *reexec) { f.world.failure = errors.New("capsule unreadable") },
			"capsule unreadable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			result := ResultDigest("h", "s", "r", "2")
			execReady(t, f, result, "/bor/spans/1")
			r := &reexec{result: result, before: func() { _, _ = get(t, "/bor/spans/1") }}
			tc.setup(f, r)
			err := f.driver.Replay(key0, r)
			requireGuardFree(t, f)
			if !errors.Is(err, ErrReplay) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
			if (len(r.keys) == 1) != tc.exec {
				t.Fatalf("re-executed %v", r.keys)
			}
			if f.states()[key0] != admission.ExecReady || f.world.windows != 0 || f.driver.Router.open {
				t.Fatalf("states %v windows %d", f.states(), f.world.windows)
			}
		})
	}
}

func TestReplayRefusesWhatItCannotCheck(t *testing.T) {
	f := newFixture(t)
	err := f.driver.Replay(key0, &reexec{})
	requireGuardFree(t, f)
	if !errors.Is(err, ErrReplay) {
		t.Fatalf("unknown observation: %v", err)
	}
	leaveAdmitted(t, f)
	f.world.calls = nil
	if err := f.driver.Replay(key0, &reexec{}); !errors.Is(err, ErrReplay) || len(f.world.calls) != 0 {
		t.Fatalf("ADMITTED observation: %v %v", err, f.world.calls)
	}
	requireGuardFree(t, f)

	f = newFixture(t)
	execReady(t, f, ResultDigest("h", "s", "r", "2"))
	f.world.plainLog = true
	f.world.calls = nil
	if err := f.driver.Replay(key0, &reexec{}); !errors.Is(err, ErrReplay) || slices.Contains(f.world.calls, "purge window=true") {
		t.Fatalf("log without progress: %v %v", err, f.world.calls)
	}
	f.world.plainLog = false
	f.world.openErr = errors.New("store closed")
	if err := f.driver.Replay(key0, &reexec{}); !errors.Is(err, f.world.openErr) {
		t.Fatalf("open failure: %v", err)
	}
	f.world.openErr = nil
	f.driver.Router.open = true
	if err := f.driver.Replay(key0, &reexec{}); !errors.Is(err, ErrWindow) || f.world.windows != 0 {
		t.Fatalf("router misuse: %v windows %d", err, f.world.windows)
	}
	f.driver.Router.open = false
	requireGuardFree(t, f)
}
