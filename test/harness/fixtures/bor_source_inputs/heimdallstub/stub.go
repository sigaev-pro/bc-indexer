// Package heimdallstub is the test-only Heimdall stub and HTTP transport
// recorder of the E01-02.inputs Bor probe. It imports only the standard
// library, so its unit tests and mutants run without the Bor module graph.
package heimdallstub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"syscall"
)

// Host is the Heimdall host given to Bor. No network is used: the process
// transport answers every request in memory.
const Host = "heimdall.stub.invalid"

// URL is the Heimdall URL given to Bor.
const URL = "http://" + Host

var spanPath = regexp.MustCompile(`^/bor/spans/[0-9]+$`)

// Counted reports whether Bor receives execution inputs through this call
// (D98): state sync event pages and spans by ID.
func Counted(u *url.URL) bool {
	return u.Path == "/clerk/time" || spanPath.MatchString(u.Path)
}

// Stub is the deterministic in-process Heimdall stub. Its response bodies are
// fixed files; Variant selects the first state sync page.
type Stub struct {
	Bodies  string
	Variant string
}

// Serve returns the raw response body and status for one request.
func (s Stub) Serve(u *url.URL) ([]byte, int) {
	name := ""
	switch {
	case u.Path == "/status":
		name = "status.json"
	case u.Path == "/bor/spans/latest":
		name = "span-latest.json"
	case u.Path == "/bor/spans/0":
		name = "span-0.json"
	case u.Path == "/bor/spans/1":
		name = "span-1.json"
	case u.Path == "/clerk/time":
		if u.Query().Get("from_id") == "1" {
			name = "clerk-" + s.Variant + "-from-1.json"
		} else {
			name = "clerk-empty.json"
		}
	}
	if name == "" {
		return []byte("not found\n"), http.StatusNotFound
	}
	data, err := os.ReadFile(filepath.Join(s.Bodies, name))
	if err != nil {
		return []byte(err.Error()), http.StatusInternalServerError
	}
	return data, http.StatusOK
}

// HexSum is the lowercase hex SHA-256 of data.
func HexSum(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// Log is the dependency log the recorder writes through: the Next method of
// the shared source_inputs package.
type Log interface {
	Next(request []byte, fetch func() ([]byte, error)) ([]byte, error)
}

// Row is one counted call as the recorder saw it.
type Row struct {
	Sequence       int    `json:"sequence"`
	Request        string `json:"request"`
	RequestSHA256  string `json:"request_sha256"`
	ResponseSHA256 string `json:"response_sha256,omitempty"`
	ResponseLength int    `json:"response_length"`
	Origin         string `json:"origin"`
	Error          string `json:"error,omitempty"`
}

// Recorder is the process HTTP transport. Counted calls go through the
// dependency log, so Bor only receives bytes that are durable first; other
// calls (status and latest-span polls) are answered by the stub directly.
type Recorder struct {
	mu         sync.Mutex
	stub       Stub
	log        Log // nil: generation, no log
	crashAfter int
	onCrash    func([]Row)
	kill       func()
	rows       []Row
	uncounted  map[string]int
}

// NewRecorder returns a recorder. With crashAfter >= 0, the process is lost
// with SIGKILL right after the counted call with that sequence was saved and
// before its bytes are handed to Bor; onCrash runs first with the rows so far.
func NewRecorder(stub Stub, log Log, crashAfter int, onCrash func([]Row)) *Recorder {
	return &Recorder{stub: stub, log: log, crashAfter: crashAfter, onCrash: onCrash, kill: killSelf,
		uncounted: map[string]int{}}
}

func killSelf() {
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		panic(fmt.Sprintf("crash point: kill failed: %v", err))
	}
	select {}
}

func response(req *http.Request, status int, body []byte) *http.Response {
	return &http.Response{Status: http.StatusText(status), StatusCode: status, Proto: "HTTP/1.1", ProtoMajor: 1,
		ProtoMinor: 1, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)), Request: req}
}

// RoundTrip implements http.RoundTripper.
func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body.Close()
	}
	if req.URL.Host != Host {
		return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !Counted(req.URL) {
		body, status := r.stub.Serve(req.URL)
		r.uncounted[req.URL.Path]++
		return response(req, status, body), nil
	}
	request := []byte(req.Method + " " + req.URL.RequestURI())
	row := Row{Sequence: len(r.rows), Request: string(request), RequestSHA256: HexSum(request), Origin: "replay"}
	fetch := func() ([]byte, error) {
		row.Origin = "fetch"
		body, status := r.stub.Serve(req.URL)
		if status != http.StatusOK {
			return nil, fmt.Errorf("stub status %d", status)
		}
		return body, nil
	}
	var body []byte
	var err error
	if r.log == nil {
		body, err = fetch()
	} else {
		body, err = r.log.Next(request, fetch)
	}
	if err != nil {
		row.Error = err.Error()
		r.rows = append(r.rows, row)
		return nil, err
	}
	row.ResponseSHA256, row.ResponseLength = HexSum(body), len(body)
	r.rows = append(r.rows, row)
	if row.Sequence == r.crashAfter {
		// The response is durable and Bor has not received it: lose the process here.
		r.onCrash(append([]Row(nil), r.rows...))
		r.kill()
	}
	return response(req, http.StatusOK, body), nil
}

// Snapshot returns the counted rows and the uncounted call counts by path.
func (r *Recorder) Snapshot() ([]Row, map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]string, 0, len(r.uncounted))
	for key := range r.uncounted {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	uncounted := make(map[string]int, len(keys))
	for _, key := range keys {
		uncounted[key] = r.uncounted[key]
	}
	return append([]Row(nil), r.rows...), uncounted
}
