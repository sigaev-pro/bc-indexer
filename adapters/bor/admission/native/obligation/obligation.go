// Package obligation drives the Bor native observations of E01-02.admission
// through the shared admission core: Begin, the parent-root pin, the sealed
// capsule and the closing ADMITTED sync, then native verification and
// execution with every counted Heimdall read passing through the obligation's
// dependency log, then ExecReady or Reject. It also holds the pin adapter, the
// recovery driver and the Heimdall router.
//
// The package imports only the Go standard library and the shared admission
// package, so test/mutate.py compiles it outside the Bor module graph.
// extractor/borimport binds it to native Bor.
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
	"regexp"
	"sync"

	"github.com/ethereum/go-ethereum/extractor/admission"
)

var (
	// ErrPinMissing reports an absent pin ledger record.
	ErrPinMissing = errors.New("pin is not in the ledger")
	// ErrPinRoot reports a pin that holds another parent root.
	ErrPinRoot = errors.New("pin holds another parent root")
	// ErrInput reports a counted Heimdall read that the dependency log or the
	// source refused. The obligation stays ADMITTED: an input failure is not a
	// native validation failure.
	ErrInput = errors.New("counted Heimdall input failed")
	// ErrWindow reports a window opened twice or closed without being open.
	ErrWindow = errors.New("observation window misuse")
)

// MaxResponseBytes bounds one Heimdall response body.
const MaxResponseBytes = 16 << 20

// Domain tags of the digests of this package. Each is followed by one zero byte.
const (
	tagRejection = "extractor-bor-native-rejection-v1"
	tagExec      = "extractor-bor-native-result-v1"
)

func tagged(tag string) []byte { return append([]byte(tag), 0) }

func field(value string) []byte {
	out := make([]byte, 8, 8+len(value))
	length := uint64(len(value))
	for i := 7; i >= 0; i-- {
		out[i] = byte(length)
		length >>= 8
	}
	return append(out, value...)
}

func digest(tag string, values ...string) string {
	h := sha256.New()
	h.Write(tagged(tag))
	for _, value := range values {
		h.Write(field(value))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RejectionDigest is the digest of the native rejection evidence: the
// observation key, the candidate block hash and the native error text.
func RejectionDigest(key, blockHash, nativeError string) string {
	return digest(tagRejection, key, blockHash, nativeError)
}

// ResultDigest is the digest of the native execution result that EXEC_READY
// binds until the record corpus of E04-01 exists: block hash, state root,
// receipt root and gas used (decimal), each as written by native Bor.
func ResultDigest(blockHash, stateRoot, receiptRoot, gasUsed string) string {
	return digest(tagExec, blockHash, stateRoot, receiptRoot, gasUsed)
}

// Ledger is the native pin ledger of E01-02.pins for one store.
type Ledger interface {
	// Record returns the raw ledger record of pin id and its parent root,
	// with found false for an absent pin.
	Record(id string) (record []byte, parentRoot string, found bool, err error)
	// Readable checks that every reference of pin id is readable.
	Readable(id string) error
	// Release durably removes pin id. An absent pin returns the native
	// unknown-pin error.
	Release(id string) error
}

// PinAdapter adapts the native ledger to admission.Pins. Unknown is the
// native unknown-pin error (state_provider ErrUnknownPin).
type PinAdapter struct {
	Ledger  Ledger
	Unknown error
}

// Verify checks that pin id exists, holds parentRoot and is readable, and
// returns the SHA-256 of its raw ledger record.
func (p PinAdapter) Verify(id, parentRoot string) (string, error) {
	record, root, found, err := p.Ledger.Record(id)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("%w: %s", ErrPinMissing, id)
	}
	if root != parentRoot {
		return "", fmt.Errorf("%w: %s holds %s, not %s", ErrPinRoot, id, root, parentRoot)
	}
	if err := p.Ledger.Readable(id); err != nil {
		return "", err
	}
	sum := sha256.Sum256(record)
	return hex.EncodeToString(sum[:]), nil
}

// Release removes pin id; an absent pin is not an error.
func (p PinAdapter) Release(id string) error {
	if err := p.Ledger.Release(id); err != nil && !errors.Is(err, p.Unknown) {
		return err
	}
	return nil
}

// Log is the dependency log of one observation (source_inputs.Log).
type Log interface {
	Next(request []byte, fetch func() ([]byte, error)) ([]byte, error)
	Finish() error
}

// Native is the Bor side of one observation. Every method runs under the
// observation guard.
type Native interface {
	// Pin durably pins the parent root of c under key.
	Pin(key string, c admission.Candidate) error
	// Seal seals the capsule of key for candidate c.
	Seal(key string, c admission.Candidate) error
	// OpenLog opens the dependency log of key.
	OpenLog(key string) (Log, error)
	// Purge drops the native Heimdall and snapshot caches, so every
	// Heimdall read of the window reaches the router.
	Purge()
	// Verify verifies the header seal of key again from the capsule's
	// ancestor headers and the logged Heimdall reads.
	Verify(key string) error
}

// Reexecutor re-executes a saved ADMITTED obligation from its pin and capsule.
type Reexecutor interface {
	// Reexecute runs native execution and validation of key and returns the
	// result digest, or the native validation error in nativeErr. err is any
	// other failure; the obligation then stays ADMITTED.
	Reexecute(key string, c admission.Candidate) (resultDigest string, nativeErr error, err error)
}

// Driver runs observations of one admission.
type Driver struct {
	Admission *admission.Admission
	Guard     *admission.Guard
	Native    Native
	Router    *Router
}

// Observation is one open observation. Done closes it.
type Observation struct {
	driver *Driver
	token  *admission.Token
	key    string
	cand   admission.Candidate
	log    Log
	closed bool
}

// Key returns the observation key.
func (o *Observation) Key() string { return o.key }

// Observe opens the observation of candidate c: Begin, pin, seal and Admit,
// then the log window, the cache purge and the header check from the
// capsule. On a failure before Admit returns, the observation is abandoned
// and the error returned; no capture exists. A failed header check after
// Admit is a native validation failure: the observation is rejected and the
// check error returned.
func (d *Driver) Observe(c admission.Candidate) (*Observation, error) {
	token := d.Guard.Acquire()
	obs, err := d.observe(token, c)
	if err != nil {
		if releaseErr := token.Release(); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
		return nil, err
	}
	return obs, nil
}

func (d *Driver) observe(token *admission.Token, c admission.Candidate) (*Observation, error) {
	begun, err := d.Admission.Begin(token, c)
	if err != nil {
		return nil, err
	}
	key := begun.Key
	abandon := func(cause error) error {
		if err := d.Admission.Abandon(token, key); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	if err := d.Native.Pin(key, c); err != nil {
		return nil, abandon(err)
	}
	if err := d.Native.Seal(key, c); err != nil {
		return nil, abandon(err)
	}
	if _, err := d.Admission.Admit(token, key); err != nil {
		return nil, abandon(err)
	}
	// ADMITTED is durable: from here on the obligation keeps its pin until
	// ExecReady or Reject.
	obs := &Observation{driver: d, token: token, key: key, cand: c}
	if err := obs.open(); err != nil {
		return nil, err
	}
	if err := d.Native.Verify(key); err != nil {
		obs.closed = true
		return nil, obs.finish("", err)
	}
	return obs, nil
}

func (o *Observation) open() error {
	log, err := o.driver.Native.OpenLog(o.key)
	if err != nil {
		return err
	}
	o.log = log
	if err := o.driver.Router.Enter(log); err != nil {
		return err
	}
	o.driver.Native.Purge()
	return nil
}

// Done closes the observation after native execution and validation. With
// nativeErr nil, it closes the log and writes EXEC_READY bound to
// resultDigest. A native validation failure writes NATIVE_REJECTED and
// returns nativeErr. A counted input failure in the window, or a failure to
// close, leaves the obligation ADMITTED and returns the error; recovery
// resolves it.
func (o *Observation) Done(resultDigest string, nativeErr error) error {
	if o.closed {
		return ErrWindow
	}
	o.closed = true
	err := o.finish(resultDigest, nativeErr)
	if releaseErr := o.token.Release(); releaseErr != nil {
		err = errors.Join(err, releaseErr)
	}
	return err
}

func (o *Observation) finish(resultDigest string, nativeErr error) error {
	d := o.driver
	fault := d.Router.Leave()
	if fault != nil {
		return errors.Join(fault, nativeErr)
	}
	if nativeErr != nil {
		evidence := RejectionDigest(o.key, o.cand.Hash, nativeErr.Error())
		if _, err := d.Admission.Reject(o.token, o.key, evidence); err != nil {
			return errors.Join(nativeErr, err)
		}
		return nativeErr
	}
	if err := o.log.Finish(); err != nil {
		return err
	}
	_, err := d.Admission.ExecReady(o.token, o.key, resultDigest)
	return err
}

// Report lists what the recovery driver did, by observation key.
type Report struct {
	Abandoned []string `json:"abandoned"`
	ExecReady []string `json:"exec_ready"`
	Rejected  []string `json:"rejected"`
	Kept      []string `json:"kept"`
}

// Recover resolves every saved obligation of a RECOVERY_ONLY admission: an
// INPUT_PREPARING observation is abandoned; an ADMITTED one is verified and
// re-executed from its pin and capsule with the log replayed, then closed
// with ExecReady or Reject. EXEC_READY observations stay for the decision
// bridge. Only then does it switch to LIVE. Any other failure stops it and
// leaves the admission in RECOVERY_ONLY.
func (d *Driver) Recover(r Reexecutor) (Report, error) {
	report := Report{Abandoned: []string{}, ExecReady: []string{}, Rejected: []string{}, Kept: []string{}}
	token := d.Guard.Acquire()
	err := d.recover(token, r, &report)
	if releaseErr := token.Release(); releaseErr != nil {
		err = errors.Join(err, releaseErr)
	}
	return report, err
}

func (d *Driver) recover(token *admission.Token, r Reexecutor, report *Report) error {
	saved, err := d.Admission.Observations(token)
	if err != nil {
		return err
	}
	for _, obs := range saved {
		switch obs.State {
		case admission.InputPreparing:
			if err := d.Admission.Abandon(token, obs.Key); err != nil {
				return err
			}
			report.Abandoned = append(report.Abandoned, obs.Key)
		case admission.Admitted:
			rejected, err := d.resolve(token, r, obs)
			if err != nil {
				return err
			}
			if rejected {
				report.Rejected = append(report.Rejected, obs.Key)
			} else {
				report.ExecReady = append(report.ExecReady, obs.Key)
			}
		default:
			report.Kept = append(report.Kept, obs.Key)
		}
	}
	return d.Admission.GoLive(token)
}

func (d *Driver) resolve(token *admission.Token, r Reexecutor, saved admission.Observation) (bool, error) {
	obs := &Observation{driver: d, token: token, key: saved.Key, cand: saved.Candidate}
	if err := obs.open(); err != nil {
		return false, err
	}
	var result string
	nativeErr := d.Native.Verify(saved.Key)
	if nativeErr == nil {
		var err error
		result, nativeErr, err = r.Reexecute(saved.Key, saved.Candidate)
		if err != nil {
			return false, errors.Join(err, d.Router.Leave())
		}
	}
	if err := obs.finish(result, nativeErr); err != nil && !errors.Is(err, nativeErr) {
		return false, err
	}
	return nativeErr != nil, nil
}

// Row is one counted Heimdall call of a window.
type Row struct {
	Sequence       int    `json:"sequence"`
	Request        string `json:"request"`
	RequestSHA256  string `json:"request_sha256"`
	ResponseSHA256 string `json:"response_sha256,omitempty"`
	ResponseLength int    `json:"response_length"`
	Origin         string `json:"origin"`
	Error          string `json:"error,omitempty"`
}

var spanByID = regexp.MustCompile(`^/bor/spans/[0-9]+$`)

// Counted reports whether a Heimdall request is an input that a window logs:
// state sync event pages and spans by ID (D98), and the latest span and the
// status (D99 item 1). The latest span bounds which span IDs Bor reads; the
// status decides whether Bor waits.
func Counted(u *url.URL) bool {
	switch {
	case u.Path == "/clerk/time", u.Path == "/bor/spans/latest", u.Path == "/status":
		return true
	default:
		return spanByID.MatchString(u.Path)
	}
}

// Router is the process HTTP transport of the Heimdall client. Inside a
// window, every counted request goes through the window's dependency log, so
// its raw response body is durable before Bor receives it. Outside a window,
// requests pass to Inner and are only counted by path.
type Router struct {
	Inner http.RoundTripper
	// Host is the Heimdall host; other hosts pass to Inner unchanged.
	Host string
	// AfterCounted, if set, runs after a counted response was obtained and
	// before Bor receives it (the probe's crash points).
	AfterCounted func(Row)

	mu     sync.Mutex
	log    Log
	open   bool
	fault  error
	rows   []Row
	passed map[string]int
}

// Enter opens a window that routes counted requests through log.
func (r *Router) Enter(log Log) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.open || log == nil {
		return ErrWindow
	}
	r.log, r.open, r.fault, r.rows = log, true, nil, nil
	return nil
}

// Leave closes the window and returns its first input failure.
func (r *Router) Leave() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.open {
		return ErrWindow
	}
	r.log, r.open = nil, false
	return r.fault
}

// Rows returns the counted calls of the current or last window.
func (r *Router) Rows() []Row {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Row(nil), r.rows...)
}

// Passed returns the Heimdall requests that did not go through a log (every
// request outside a window, and uncounted ones inside), by path.
func (r *Router) Passed() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int, len(r.passed))
	for path, count := range r.passed {
		out[path] = count
	}
	return out
}

func (r *Router) fetch(req *http.Request) ([]byte, error) {
	resp, err := r.Inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxResponseBytes {
		return nil, fmt.Errorf("Heimdall response larger than %d bytes", MaxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Heimdall status %d", resp.StatusCode)
	}
	return body, nil
}

func response(req *http.Request, body []byte) *http.Response {
	return &http.Response{Status: "200 OK", StatusCode: http.StatusOK, Proto: "HTTP/1.1", ProtoMajor: 1,
		ProtoMinor: 1, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: req}
}

// RoundTrip implements http.RoundTripper. A counted request in a window
// holds the router lock until its response is durable, so counted requests
// reach the log one at a time and in native order.
func (r *Router) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != r.Host {
		return r.Inner.RoundTrip(req)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.open || !Counted(req.URL) {
		if r.passed == nil {
			r.passed = map[string]int{}
		}
		r.passed[req.URL.Path]++
		return r.Inner.RoundTrip(req)
	}
	request := []byte(req.Method + " " + req.URL.RequestURI())
	sum := sha256.Sum256(request)
	row := Row{Sequence: len(r.rows), Request: string(request), RequestSHA256: hex.EncodeToString(sum[:]), Origin: "replay"}
	if r.fault != nil {
		row.Error = r.fault.Error()
		r.rows = append(r.rows, row)
		return nil, r.fault
	}
	body, err := r.log.Next(request, func() ([]byte, error) {
		row.Origin = "fetch"
		return r.fetch(req)
	})
	if err != nil {
		r.fault = fmt.Errorf("%w: %s: %w", ErrInput, request, err)
		row.Error = err.Error()
		r.rows = append(r.rows, row)
		return nil, r.fault
	}
	got := sha256.Sum256(body)
	row.ResponseSHA256, row.ResponseLength = hex.EncodeToString(got[:]), len(body)
	r.rows = append(r.rows, row)
	if r.AfterCounted != nil {
		r.AfterCounted(row)
	}
	return response(req, body), nil
}
