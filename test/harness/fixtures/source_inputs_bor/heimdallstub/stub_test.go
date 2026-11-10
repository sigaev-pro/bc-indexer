package heimdallstub

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The test bodies are short distinct strings; the real stub files live in ../stub.
var testBodies = map[string]string{
	"status.json": "status", "span-latest.json": "latest", "span-0.json": "span0", "span-1.json": "span1",
	"clerk-a-from-1.json": "clerk-a", "clerk-b-from-1.json": "clerk-b", "clerk-empty.json": "empty",
}

func bodies(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range testBodies {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func get(t *testing.T, transport http.RoundTripper, target string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return transport.RoundTrip(req)
}

func read(t *testing.T, resp *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// fakeLog replays saved responses below its frontier and appends beyond it,
// like the shared package, without a store.
type fakeLog struct {
	requests  []string
	responses [][]byte
	cursor    int
	err       error
	fetches   int
}

func (l *fakeLog) Next(request []byte, fetch func() ([]byte, error)) ([]byte, error) {
	if l.err != nil {
		return nil, l.err
	}
	if l.cursor < len(l.requests) {
		if l.requests[l.cursor] != string(request) {
			return nil, errors.New("dependency log prefix conflict")
		}
		l.cursor++
		return l.responses[l.cursor-1], nil
	}
	l.fetches++
	body, err := fetch()
	if err != nil {
		return nil, err
	}
	l.requests = append(l.requests, string(request))
	l.responses = append(l.responses, body)
	l.cursor++
	return body, nil
}

func TestCounted(t *testing.T) {
	cases := map[string]bool{
		"/clerk/time": true, "/bor/spans/0": true, "/bor/spans/12": true,
		"/bor/spans/latest": false, "/status": false, "/milestones/latest": false, "/bor/spans/": false,
		"/bor/spans/1/x": false, "/x/bor/spans/1": false, "/clerk/time/x": false,
	}
	for path, want := range cases {
		if got := Counted(&url.URL{Path: path}); got != want {
			t.Errorf("Counted(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestStubServe(t *testing.T) {
	dir := bodies(t)
	cases := []struct {
		variant, target, body string
		status                int
	}{
		{"a", "/status", "status", 200},
		{"a", "/bor/spans/latest", "latest", 200},
		{"a", "/bor/spans/0", "span0", 200},
		{"a", "/bor/spans/1", "span1", 200},
		{"a", "/bor/spans/2", "not found\n", 404},
		{"a", "/clerk/time?from_id=1&to_time=x", "clerk-a", 200},
		{"b", "/clerk/time?from_id=1&to_time=x", "clerk-b", 200},
		{"b", "/clerk/time?from_id=3&to_time=x", "empty", 200},
		{"a", "/clerk/time?from_id=10", "empty", 200},
		{"a", "/milestones/latest", "not found\n", 404},
	}
	for _, c := range cases {
		u, err := url.Parse(c.target)
		if err != nil {
			t.Fatal(err)
		}
		body, status := Stub{Bodies: dir, Variant: c.variant}.Serve(u)
		if string(body) != c.body || status != c.status {
			t.Errorf("%s %s: got %q %d, want %q %d", c.variant, c.target, body, status, c.body, c.status)
		}
	}
	if _, status := (Stub{Bodies: filepath.Join(dir, "missing"), Variant: "a"}).Serve(&url.URL{Path: "/status"}); status != 500 {
		t.Errorf("unreadable body: status %d, want 500", status)
	}
}

func TestRecorderRoutesCountedCallsThroughTheLog(t *testing.T) {
	log := &fakeLog{}
	rec := NewRecorder(Stub{Bodies: bodies(t), Variant: "a"}, log, -1, nil)
	targets := []string{URL + "/status", URL + "/bor/spans/0", URL + "/bor/spans/latest",
		URL + "/clerk/time?from_id=1&to_time=T&pagination.limit=50", URL + "/status"}
	want := []string{"status", "span0", "latest", "clerk-a", "status"}
	for i, target := range targets {
		resp, err := get(t, rec, target)
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		if resp.StatusCode != 200 || read(t, resp) != want[i] {
			t.Fatalf("%s: wrong response", target)
		}
	}
	rows, uncounted := rec.Snapshot()
	if !reflect.DeepEqual(log.requests, []string{"GET /bor/spans/0", "GET /clerk/time?from_id=1&to_time=T&pagination.limit=50"}) {
		t.Fatalf("logged requests %q", log.requests)
	}
	if len(rows) != 2 || rows[0].Sequence != 0 || rows[1].Sequence != 1 || rows[1].Request != log.requests[1] ||
		rows[1].RequestSHA256 != HexSum([]byte(log.requests[1])) || rows[1].ResponseSHA256 != HexSum([]byte("clerk-a")) ||
		rows[1].ResponseLength != len("clerk-a") || rows[0].Origin != "fetch" || rows[1].Origin != "fetch" {
		t.Fatalf("rows %+v", rows)
	}
	if !reflect.DeepEqual(uncounted, map[string]int{"/status": 2, "/bor/spans/latest": 1}) {
		t.Fatalf("uncounted %v", uncounted)
	}
}

func TestRecorderReplaysWithoutFetching(t *testing.T) {
	log := &fakeLog{requests: []string{"GET /bor/spans/0"}, responses: [][]byte{[]byte("saved")}}
	rec := NewRecorder(Stub{Bodies: bodies(t), Variant: "b"}, log, -1, nil)
	resp, err := get(t, rec, URL+"/bor/spans/0")
	if err != nil || read(t, resp) != "saved" {
		t.Fatalf("replay: %v", err)
	}
	rows, _ := rec.Snapshot()
	if log.fetches != 0 || rows[0].Origin != "replay" || rows[0].ResponseSHA256 != HexSum([]byte("saved")) {
		t.Fatalf("replay fetched or recorded wrongly: %+v", rows)
	}
}

func TestRecorderErrors(t *testing.T) {
	dir := bodies(t)
	rec := NewRecorder(Stub{Bodies: dir, Variant: "a"}, &fakeLog{}, -1, nil)
	if _, err := get(t, rec, "http://other.invalid/status"); err == nil || !strings.Contains(err.Error(), "unexpected host") {
		t.Fatalf("foreign host: %v", err)
	}
	// A counted call the stub cannot answer is a fetch error, never a saved response.
	if _, err := get(t, rec, URL+"/bor/spans/7"); err == nil || !strings.Contains(err.Error(), "stub status 404") {
		t.Fatalf("missing span: %v", err)
	}
	rows, _ := rec.Snapshot()
	if len(rows) != 1 || rows[0].Error == "" || rows[0].ResponseSHA256 != "" {
		t.Fatalf("failed row %+v", rows)
	}
	failing := &fakeLog{err: errors.New("dependency log prefix conflict: x")}
	rec = NewRecorder(Stub{Bodies: dir, Variant: "a"}, failing, -1, nil)
	resp, err := get(t, rec, URL+"/bor/spans/0")
	if err == nil || resp != nil || !strings.Contains(err.Error(), "prefix conflict") {
		t.Fatalf("log error must reach Bor as a transport error: %v", err)
	}
	rows, _ = rec.Snapshot()
	if len(rows) != 1 || !strings.Contains(rows[0].Error, "prefix conflict") {
		t.Fatalf("log error row %+v", rows)
	}
}

func TestRecorderWithoutLogFetches(t *testing.T) {
	rec := NewRecorder(Stub{Bodies: bodies(t), Variant: "b"}, nil, -1, nil)
	resp, err := get(t, rec, URL+"/clerk/time?from_id=1")
	if err != nil || read(t, resp) != "clerk-b" {
		t.Fatalf("generation fetch: %v", err)
	}
	rows, _ := rec.Snapshot()
	if len(rows) != 1 || rows[0].Origin != "fetch" {
		t.Fatalf("rows %+v", rows)
	}
}

type lost struct{}

func TestCrashPointAfterSaveBeforeHandOff(t *testing.T) {
	log := &fakeLog{}
	var crashed []Row
	rec := NewRecorder(Stub{Bodies: bodies(t), Variant: "a"}, log, 1, func(rows []Row) { crashed = rows })
	rec.kill = func() { panic(lost{}) }
	if _, err := get(t, rec, URL+"/bor/spans/0"); err != nil {
		t.Fatal(err)
	}
	var handed *http.Response
	func() {
		defer func() {
			if value := recover(); value != (lost{}) {
				t.Fatalf("expected the crash point, got %v", value)
			}
		}()
		handed, _ = get(t, rec, URL+"/bor/spans/1")
	}()
	if handed != nil {
		t.Fatal("the response reached Bor after the crash point")
	}
	if len(log.requests) != 2 || len(crashed) != 2 || crashed[1].Sequence != 1 ||
		crashed[1].ResponseSHA256 != HexSum([]byte("span1")) {
		t.Fatalf("the crash point ran before the response was saved: log %q rows %+v", log.requests, crashed)
	}
}
