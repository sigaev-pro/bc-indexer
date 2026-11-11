package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// vectorFile holds the independent expected values. `python3
// test/harness/admission.py vectors` computes them from the README formulas;
// test/harness/test_admission.py checks that the file matches.
type vectorFile struct {
	Producer        string            `json:"producer"`
	Run             string            `json:"run"`
	Sequence        uint64            `json:"sequence"`
	Key             string            `json:"key"`
	Candidate       Candidate         `json:"candidate"`
	PinDigest       string            `json:"pin_digest"`
	CapsuleDigest   string            `json:"capsule_digest"`
	ConsumedCount   uint64            `json:"consumed_count"`
	ConsumedChain   string            `json:"consumed_chain"`
	ExecDigest      string            `json:"exec_digest"`
	AdmittedDigest  string            `json:"admitted_digest"`
	ConsumedDigest  string            `json:"consumed_digest"`
	ExecReadyDigest string            `json:"exec_ready_digest"`
	EvidenceDigest  string            `json:"evidence_digest"`
	RejectedDigest  string            `json:"rejected_digest"`
	Binding         map[string]string `json:"binding"`
	Canonical       []struct {
		Input string  `json:"input"`
		Want  *string `json:"want"`
	} `json:"canonical"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	data, err := os.ReadFile("vectors.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v vectorFile
	if err := strictDecode(data, &v); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	return v
}

var (
	errCrash     = errors.New("injected power loss")
	errDisk      = errors.New("injected disk error")
	errNoPin     = errors.New("no such pin")
	errNoCapsule = errors.New("capsule is not sealed")
	errNotClosed = errors.New("log is not closed")
)

type write struct {
	key   string
	value []byte
}

// memStore models a native key-value store. Put stays pending until
// SyncKeyValue makes it durable. powerLoss keeps a chosen subset of pending
// writes and drops the rest. crashAt makes the mutating operation with that
// index (Put or SyncKeyValue, counted from 1) fail without effect.
type memStore struct {
	durable   map[string][]byte
	pending   []write
	ops       int
	crashAt   int
	putErr    error
	syncErr   error
	getErr    error
	hasErr    error
	getErrFor string
	writes    []string
}

func newStore() *memStore { return &memStore{durable: map[string][]byte{}} }

func (s *memStore) view(key string) ([]byte, bool) {
	for i := len(s.pending) - 1; i >= 0; i-- {
		if s.pending[i].key == key {
			return s.pending[i].value, true
		}
	}
	value, ok := s.durable[key]
	return value, ok
}

func (s *memStore) Has(key []byte) (bool, error) {
	if s.hasErr != nil {
		return false, s.hasErr
	}
	_, ok := s.view(string(key))
	return ok, nil
}

func (s *memStore) Get(key []byte) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.getErrFor != "" && strings.HasSuffix(string(key), s.getErrFor) {
		return nil, errDisk
	}
	value, ok := s.view(string(key))
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return bytes.Clone(value), nil
}

func (s *memStore) step() error {
	s.ops++
	if s.crashAt != 0 && s.ops >= s.crashAt {
		return errCrash
	}
	return nil
}

func (s *memStore) Put(key, value []byte) error {
	if s.putErr != nil {
		return s.putErr
	}
	if err := s.step(); err != nil {
		return err
	}
	s.pending = append(s.pending, write{string(key), bytes.Clone(value)})
	s.writes = append(s.writes, string(key))
	return nil
}

func (s *memStore) SyncKeyValue() error {
	if s.syncErr != nil {
		return s.syncErr
	}
	if err := s.step(); err != nil {
		return err
	}
	for _, w := range s.pending {
		s.durable[w.key] = w.value
	}
	s.pending = nil
	return nil
}

// powerLoss keeps pending write i when bit i of keep is set and returns the
// reopened store.
func (s *memStore) powerLoss(keep uint) *memStore {
	out := &memStore{durable: maps.Clone(s.durable)}
	for i, w := range s.pending {
		if keep&(1<<i) != 0 {
			out.durable[w.key] = w.value
		}
	}
	return out
}

func (s *memStore) snapshot() map[string]string {
	out := map[string]string{}
	for key, value := range s.durable {
		out[key] = string(value)
	}
	return out
}

// fakePins is a durable pin ledger: a pin exists from Pin until Release.
type fakePins struct {
	roots      map[string]string
	digests    map[string]string
	released   []string
	verifyErr  error
	releaseErr error
}

func newPins() *fakePins { return &fakePins{roots: map[string]string{}, digests: map[string]string{}} }

func (p *fakePins) pin(id, root, digest string) { p.roots[id], p.digests[id] = root, digest }

func (p *fakePins) Verify(id, root string) (string, error) {
	if p.verifyErr != nil {
		return "", p.verifyErr
	}
	got, ok := p.roots[id]
	if !ok {
		return "", errNoPin
	}
	if got != root {
		return "", fmt.Errorf("pin %s holds root %s", id, got)
	}
	return p.digests[id], nil
}

func (p *fakePins) Release(id string) error {
	if p.releaseErr != nil {
		return p.releaseErr
	}
	delete(p.roots, id)
	delete(p.digests, id)
	p.released = append(p.released, id)
	return nil
}

// fakeInputs holds sealed capsule digests and closed consumed manifests.
type fakeInputs struct {
	capsules map[string]string
	consumed map[string]Consumed
}

func newInputs() *fakeInputs {
	return &fakeInputs{capsules: map[string]string{}, consumed: map[string]Consumed{}}
}

func (f *fakeInputs) CapsuleDigest(id string) (string, error) {
	digest, ok := f.capsules[id]
	if !ok {
		return "", errNoCapsule
	}
	return digest, nil
}

func (f *fakeInputs) Consumed(id string) (Consumed, error) {
	manifest, ok := f.consumed[id]
	if !ok {
		return Consumed{}, errNotClosed
	}
	return manifest, nil
}

// fixture is one producer with its store, pins, inputs and guard token.
type fixture struct {
	t      *testing.T
	v      vectorFile
	db     *memStore
	pins   *fakePins
	inputs *fakeInputs
	guard  *Guard
	token  *Token
	a      *Admission
}

func (f *fixture) producer() Producer { return Producer{ProducerID: f.v.Producer, RunID: f.v.Run} }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, v: loadVectors(t), db: newStore(), pins: newPins(), inputs: newInputs(), guard: NewGuard()}
	f.token = f.guard.Acquire()
	f.reopen(f.db)
	return f
}

func (f *fixture) reopen(db *memStore) {
	f.t.Helper()
	f.db = db
	a, err := Open(f.token, db, f.producer(), f.pins, f.inputs)
	if err != nil {
		f.t.Fatalf("open: %v", err)
	}
	f.a = a
}

func (f *fixture) live() {
	f.t.Helper()
	if err := f.a.GoLive(f.token); err != nil {
		f.t.Fatalf("go live: %v", err)
	}
}

func (f *fixture) begin() Observation {
	f.t.Helper()
	obs, err := f.a.Begin(f.token, f.v.Candidate)
	if err != nil {
		f.t.Fatalf("begin: %v", err)
	}
	return obs
}

// prepare pins and seals the inputs of key with the vector digests.
func (f *fixture) prepare(key string) {
	f.pins.pin(key, f.v.Candidate.ParentRoot, f.v.PinDigest)
	f.inputs.capsules[key] = f.v.CapsuleDigest
}

func (f *fixture) closeLog(key string) {
	f.inputs.consumed[key] = Consumed{Observation: key, CapsuleDigest: f.v.CapsuleDigest,
		Count: f.v.ConsumedCount, Chain: f.v.ConsumedChain, Digest: f.v.ConsumedDigest}
}

func (f *fixture) admit(key string) AdmittedRecord {
	f.t.Helper()
	record, err := f.a.Admit(f.token, key)
	if err != nil {
		f.t.Fatalf("admit: %v", err)
	}
	return record
}

func (f *fixture) execReady(key string) ExecReadyRecord {
	f.t.Helper()
	record, err := f.a.ExecReady(f.token, key, f.v.ExecDigest)
	if err != nil {
		f.t.Fatalf("exec ready: %v", err)
	}
	return record
}

func (f *fixture) states() []State {
	f.t.Helper()
	list, err := f.a.Observations(f.token)
	if err != nil {
		f.t.Fatalf("observations: %v", err)
	}
	out := []State{}
	for _, obs := range list {
		out = append(out, obs.State)
	}
	return out
}

func TestDigestVectors(t *testing.T) {
	v := loadVectors(t)
	key := ObservationKey(Producer{ProducerID: v.Producer, RunID: v.Run}, v.Sequence)
	if key != v.Key {
		t.Fatalf("observation key %s, want %s", key, v.Key)
	}
	if got, err := AdmittedDigest(key, v.Sequence, v.Candidate, v.PinDigest, v.CapsuleDigest); err != nil || got != v.AdmittedDigest {
		t.Fatalf("admitted digest %s %v, want %s", got, err, v.AdmittedDigest)
	}
	if got, err := ConsumedDigest(v.CapsuleDigest, v.ConsumedCount, v.ConsumedChain); err != nil || got != v.ConsumedDigest {
		t.Fatalf("consumed digest %s %v, want %s", got, err, v.ConsumedDigest)
	}
	if got, err := ExecReadyDigest(v.AdmittedDigest, v.ConsumedDigest, v.ExecDigest); err != nil || got != v.ExecReadyDigest {
		t.Fatalf("exec ready digest %s %v, want %s", got, err, v.ExecReadyDigest)
	}
}

// TestFullFlow runs one observation through every step and checks each
// record against the independent vectors.
func TestFullFlow(t *testing.T) {
	f := newFixture(t)
	if f.a.Mode() != RecoveryOnly || len(f.states()) != 0 {
		t.Fatalf("fresh admission: mode %s, states %v", f.a.Mode(), f.states())
	}
	f.live()
	obs := f.begin()
	if obs.Key != f.v.Key || obs.Sequence != 0 || obs.State != InputPreparing || obs.Candidate != f.v.Candidate {
		t.Fatalf("begin: %+v", obs)
	}
	f.prepare(obs.Key)
	admitted := f.admit(obs.Key)
	want := AdmittedRecord{Observation: f.v.Key, Sequence: 0, Candidate: f.v.Candidate, PinDigest: f.v.PinDigest,
		CapsuleDigest: f.v.CapsuleDigest, Digest: f.v.AdmittedDigest}
	if admitted != want {
		t.Fatalf("admitted %+v, want %+v", admitted, want)
	}
	f.closeLog(obs.Key)
	ready := f.execReady(obs.Key)
	wantReady := ExecReadyRecord{Observation: f.v.Key, Sequence: 0, AdmittedDigest: f.v.AdmittedDigest,
		ConsumedDigest: f.v.ConsumedDigest, ExecDigest: f.v.ExecDigest, Digest: f.v.ExecReadyDigest}
	if ready != wantReady {
		t.Fatalf("exec ready %+v, want %+v", ready, wantReady)
	}
	list, err := f.a.Observations(f.token)
	if err != nil || len(list) != 1 || list[0].State != ExecReady || *list[0].Admitted != want || *list[0].ExecReady != wantReady {
		t.Fatalf("observations %+v %v", list, err)
	}
	f.reopen(f.db)
	list, err = f.a.Observations(f.token)
	if err != nil || len(list) != 1 || list[0].State != ExecReady || *list[0].ExecReady != wantReady {
		t.Fatalf("reopened observations %+v %v", list, err)
	}
	f.live()
	next := f.begin()
	if next.Sequence != 1 || next.Key != ObservationKey(f.producer(), 1) {
		t.Fatalf("second observation %+v", next)
	}
	for _, key := range f.db.writes {
		if len(key) <= 33 || !strings.HasPrefix(key, keyPrefix+f.v.Producer+"."+f.v.Run+"/") {
			t.Fatalf("key %q is short or outside the producer prefix", key)
		}
	}
	if len(f.pins.released) != 0 {
		t.Fatalf("pins released: %v", f.pins.released)
	}
}

func TestRepeatedStepsReturnTheSameRecord(t *testing.T) {
	f := newFixture(t)
	f.live()
	key := f.begin().Key
	f.prepare(key)
	first := f.admit(key)
	writes := len(f.db.writes)
	if again := f.admit(key); again != first || len(f.db.writes) != writes {
		t.Fatalf("repeated admit %+v wrote %d keys", again, len(f.db.writes)-writes)
	}
	f.pins.pin(key, f.v.Candidate.ParentRoot, strings.Repeat("1", 64))
	if _, err := f.a.Admit(f.token, key); !errors.Is(err, ErrConflict) {
		t.Fatalf("admit with another pin record: %v", err)
	}
	f.prepare(key)
	f.closeLog(key)
	ready := f.execReady(key)
	writes = len(f.db.writes)
	if again := f.execReady(key); again != ready || len(f.db.writes) != writes {
		t.Fatalf("repeated exec ready %+v", again)
	}
	if _, err := f.a.ExecReady(f.token, key, strings.Repeat("2", 64)); !errors.Is(err, ErrConflict) {
		t.Fatalf("exec ready with another corpus: %v", err)
	}
	if _, err := f.a.Admit(f.token, key); !errors.Is(err, ErrState) {
		t.Fatalf("admit after exec ready: %v", err)
	}
	if err := f.a.Abandon(f.token, key); !errors.Is(err, ErrState) {
		t.Fatalf("abandon after exec ready: %v", err)
	}
}

func TestModesAndPending(t *testing.T) {
	f := newFixture(t)
	if _, err := f.a.Begin(f.token, f.v.Candidate); !errors.Is(err, ErrMode) {
		t.Fatalf("begin in recovery: %v", err)
	}
	f.live()
	if f.a.Mode() != Live {
		t.Fatalf("mode %s", f.a.Mode())
	}
	key := f.begin().Key
	if _, err := f.a.Begin(f.token, f.v.Candidate); !errors.Is(err, ErrPending) {
		t.Fatalf("begin while INPUT_PREPARING: %v", err)
	}
	f.prepare(key)
	f.admit(key)
	if _, err := f.a.Begin(f.token, f.v.Candidate); !errors.Is(err, ErrPending) {
		t.Fatalf("begin while ADMITTED: %v", err)
	}
	f.reopen(f.db)
	if f.a.Mode() != RecoveryOnly {
		t.Fatalf("reopened mode %s", f.a.Mode())
	}
	if err := f.a.GoLive(f.token); !errors.Is(err, ErrPending) {
		t.Fatalf("go live while ADMITTED: %v", err)
	}
	if _, err := f.a.Admit(f.token, key); !errors.Is(err, ErrMode) {
		t.Fatalf("admit in recovery: %v", err)
	}
	f.closeLog(key)
	f.execReady(key)
	f.live()
	f.begin()
}

func TestAdmitNeedsPinAndCapsule(t *testing.T) {
	f := newFixture(t)
	f.live()
	key := f.begin().Key
	before := f.db.snapshot()
	if _, err := f.a.Admit(f.token, key); !errors.Is(err, ErrBinding) || !errors.Is(err, errNoPin) {
		t.Fatalf("admit without pin: %v", err)
	}
	f.pins.pin(key, strings.Repeat("3", 64), f.v.PinDigest)
	if _, err := f.a.Admit(f.token, key); !errors.Is(err, ErrBinding) {
		t.Fatalf("admit with a pin of another root: %v", err)
	}
	f.pins.pin(key, f.v.Candidate.ParentRoot, f.v.PinDigest)
	if _, err := f.a.Admit(f.token, key); !errors.Is(err, ErrBinding) || !errors.Is(err, errNoCapsule) {
		t.Fatalf("admit without capsule: %v", err)
	}
	f.inputs.capsules[key] = "not-a-digest"
	if _, err := f.a.Admit(f.token, key); !errors.Is(err, ErrBinding) {
		t.Fatalf("admit with a malformed capsule digest: %v", err)
	}
	if !maps.Equal(before, f.db.snapshot()) || len(f.db.pending) != 0 {
		t.Fatalf("a refused admission wrote to the store")
	}
	if _, err := f.a.Admit(f.token, ObservationKey(f.producer(), 9)); !errors.Is(err, ErrUnknown) {
		t.Fatalf("admit of an unknown key: %v", err)
	}
	if got := f.states(); !slices.Equal(got, []State{InputPreparing}) {
		t.Fatalf("states %v", got)
	}
}

func TestExecReadyBinding(t *testing.T) {
	f := newFixture(t)
	f.live()
	key := f.begin().Key
	if _, err := f.a.ExecReady(f.token, key, f.v.ExecDigest); !errors.Is(err, ErrState) {
		t.Fatalf("exec ready before admission: %v", err)
	}
	f.prepare(key)
	f.admit(key)
	good := Consumed{Observation: key, CapsuleDigest: f.v.CapsuleDigest, Count: f.v.ConsumedCount,
		Chain: f.v.ConsumedChain, Digest: f.v.ConsumedDigest}
	other := strings.Repeat("4", 64)
	cases := map[string]func(){
		"log not closed":        func() {},
		"other observation":     func() { c := good; c.Observation = "x"; f.inputs.consumed[key] = c },
		"other capsule":         func() { c := good; c.CapsuleDigest = other; f.inputs.consumed[key] = c },
		"digest not recomputed": func() { c := good; c.Digest = other; f.inputs.consumed[key] = c },
		"other count":           func() { c := good; c.Count++; f.inputs.consumed[key] = c },
		"other chain":           func() { c := good; c.Chain = other; f.inputs.consumed[key] = c },
		"malformed chain":       func() { c := good; c.Chain = "x"; f.inputs.consumed[key] = c },
		"capsule changed":       func() { f.closeLog(key); f.inputs.capsules[key] = other },
		"capsule missing":       func() { f.closeLog(key); delete(f.inputs.capsules, key) },
		"pin changed":           func() { f.closeLog(key); f.pins.pin(key, f.v.Candidate.ParentRoot, other) },
		"pin released":          func() { f.closeLog(key); f.pins.Release(key) },
	}
	before := f.db.snapshot()
	for name, setup := range cases {
		f.prepare(key)
		delete(f.inputs.consumed, key)
		setup()
		if _, err := f.a.ExecReady(f.token, key, f.v.ExecDigest); !errors.Is(err, ErrBinding) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, bad := range []string{"", "x", strings.ToUpper(f.v.ExecDigest), f.v.ExecDigest + "0"} {
		if _, err := f.a.ExecReady(f.token, key, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("exec digest %q: %v", bad, err)
		}
	}
	if !maps.Equal(before, f.db.snapshot()) || len(f.db.pending) != 0 {
		t.Fatalf("a refused EXEC_READY wrote to the store")
	}
	f.prepare(key)
	f.pins.released = nil
	f.closeLog(key)
	f.execReady(key)
}

func TestBeginPowerLoss(t *testing.T) {
	for crash := 1; crash <= 5; crash++ {
		for keep := uint(0); keep < 4; keep++ {
			f := newFixture(t)
			f.live()
			f.db.crashAt = f.db.ops + crash
			obs, err := f.a.Begin(f.token, f.v.Candidate)
			completed := err == nil
			if !completed && !errors.Is(err, errCrash) {
				t.Fatalf("crash %d: %v", crash, err)
			}
			if !completed && !errors.Is(f.a.GoLive(f.token), ErrFailed) {
				t.Fatalf("crash %d: admission did not stop", crash)
			}
			f.reopen(f.db.powerLoss(keep))
			states := f.states()
			switch {
			case completed && !slices.Equal(states, []State{InputPreparing}):
				t.Fatalf("crash %d keep %b: completed Begin left %v", crash, keep, states)
			case !slices.Equal(states, []State{}) && !slices.Equal(states, []State{InputPreparing}):
				t.Fatalf("crash %d keep %b: states %v", crash, keep, states)
			}
			if len(states) == 1 {
				list, _ := f.a.Observations(f.token)
				if list[0].Key != f.v.Key || list[0].Candidate != f.v.Candidate {
					t.Fatalf("crash %d: recovered %+v", crash, list[0])
				}
				if completed && obs.Key != list[0].Key {
					t.Fatalf("crash %d: key changed", crash)
				}
				if err := f.a.Abandon(f.token, f.v.Key); err != nil {
					t.Fatalf("crash %d: abandon: %v", crash, err)
				}
			}
			f.live()
			next := f.begin()
			if want := uint64(len(states)); next.Sequence != want {
				t.Fatalf("crash %d keep %b: next sequence %d, want %d", crash, keep, next.Sequence, want)
			}
		}
	}
}

// TestAdmitPowerLoss is the unit form of AC-CAP-02-01 and AC-CAP-02-02.
// Before the closing sync returns, a power loss leaves INPUT_PREPARING or a
// complete ADMITTED record, never EXEC_READY. After it, the same obligation
// is recovered from the same inputs, keeps its pin and blocks a new H until
// it reaches EXEC_READY.
func TestAdmitPowerLoss(t *testing.T) {
	want := AdmittedRecord{Observation: "", Sequence: 0}
	for crash := 0; crash <= 3; crash++ {
		for keep := uint(0); keep < 2; keep++ {
			f := newFixture(t)
			f.live()
			key := f.begin().Key
			f.prepare(key)
			want = AdmittedRecord{Observation: key, Sequence: 0, Candidate: f.v.Candidate, PinDigest: f.v.PinDigest,
				CapsuleDigest: f.v.CapsuleDigest, Digest: f.v.AdmittedDigest}
			if crash > 0 {
				f.db.crashAt = f.db.ops + crash
			}
			record, err := f.a.Admit(f.token, key)
			synced := err == nil
			if synced && record != want {
				t.Fatalf("admit %+v", record)
			}
			if !synced && !errors.Is(err, errCrash) {
				t.Fatalf("crash %d: %v", crash, err)
			}
			f.reopen(f.db.powerLoss(keep))
			list, err := f.a.Observations(f.token)
			if err != nil || len(list) != 1 {
				t.Fatalf("crash %d keep %b: %+v %v", crash, keep, list, err)
			}
			obs := list[0]
			if synced && obs.State != Admitted {
				t.Fatalf("crash %d: the closing sync returned but the reopened state is %s", crash, obs.State)
			}
			if obs.State != InputPreparing && obs.State != Admitted {
				t.Fatalf("crash %d keep %b: state %s", crash, keep, obs.State)
			}
			if err := f.a.GoLive(f.token); !errors.Is(err, ErrPending) {
				t.Fatalf("crash %d keep %b: go live with %s: %v", crash, keep, obs.State, err)
			}
			if obs.State == InputPreparing {
				if obs.Admitted != nil {
					t.Fatalf("INPUT_PREPARING with an ADMITTED record")
				}
				if err := f.a.Abandon(f.token, key); err != nil {
					t.Fatalf("abandon: %v", err)
				}
				if !slices.Equal(f.pins.released, []string{key}) {
					t.Fatalf("abandon released %v", f.pins.released)
				}
				f.live()
				continue
			}
			if *obs.Admitted != want {
				t.Fatalf("recovered %+v, want %+v", *obs.Admitted, want)
			}
			if err := f.a.Abandon(f.token, key); !errors.Is(err, ErrState) {
				t.Fatalf("abandon of ADMITTED: %v", err)
			}
			if len(f.pins.released) != 0 {
				t.Fatalf("ADMITTED pin released: %v", f.pins.released)
			}
			f.closeLog(key)
			ready := f.execReady(key)
			if ready.AdmittedDigest != f.v.AdmittedDigest || ready.Digest != f.v.ExecReadyDigest {
				t.Fatalf("recovered obligation produced %+v", ready)
			}
			f.live()
			if next := f.begin(); next.Sequence != 1 {
				t.Fatalf("next sequence %d", next.Sequence)
			}
		}
	}
}

func TestExecReadyPowerLoss(t *testing.T) {
	for crash := 0; crash <= 3; crash++ {
		for keep := uint(0); keep < 2; keep++ {
			f := newFixture(t)
			f.live()
			key := f.begin().Key
			f.prepare(key)
			f.admit(key)
			f.closeLog(key)
			if crash > 0 {
				f.db.crashAt = f.db.ops + crash
			}
			_, err := f.a.ExecReady(f.token, key, f.v.ExecDigest)
			synced := err == nil
			if !synced && !errors.Is(err, errCrash) {
				t.Fatalf("crash %d: %v", crash, err)
			}
			f.reopen(f.db.powerLoss(keep))
			got := f.states()
			if synced && !slices.Equal(got, []State{ExecReady}) {
				t.Fatalf("crash %d: synced EXEC_READY reopened as %v", crash, got)
			}
			if !slices.Equal(got, []State{ExecReady}) && !slices.Equal(got, []State{Admitted}) {
				t.Fatalf("crash %d keep %b: %v", crash, keep, got)
			}
			if ready := f.execReady(key); ready.Digest != f.v.ExecReadyDigest {
				t.Fatalf("crash %d keep %b: retry %+v", crash, keep, ready)
			}
			f.live()
		}
	}
}

func TestAbandonAndLowWater(t *testing.T) {
	f := newFixture(t)
	f.live()
	first := f.begin().Key
	if err := f.a.Abandon(f.token, first); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if err := f.a.Abandon(f.token, first); !errors.Is(err, ErrUnknown) {
		t.Fatalf("abandon twice: %v", err)
	}
	second := f.begin().Key
	f.prepare(second)
	f.admit(second)
	f.closeLog(second)
	f.execReady(second)
	third := f.begin().Key
	if err := f.a.Abandon(f.token, third); err != nil {
		t.Fatalf("abandon third: %v", err)
	}
	var low lowwaterRecord
	if ok, err := readRecord(f.db, f.a.key("lowwater"), &low); !ok || err != nil || low.Sequence != 1 {
		t.Fatalf("low-water %+v %v %v, want 1", low, ok, err)
	}
	// Records below the low-water mark are never read again.
	f.db.durable[string(f.a.recordKey(0, "preparing"))] = []byte("damaged")
	f.reopen(f.db)
	list, err := f.a.Observations(f.token)
	if err != nil || len(list) != 1 || list[0].Key != second || list[0].State != ExecReady {
		t.Fatalf("observations after abandon %+v %v", list, err)
	}
	if !slices.Equal(f.pins.released, []string{first, third}) {
		t.Fatalf("released %v", f.pins.released)
	}
}

func TestAbandonPowerLoss(t *testing.T) {
	for crash := 0; crash <= 4; crash++ {
		for keep := uint(0); keep < 2; keep++ {
			f := newFixture(t)
			f.live()
			key := f.begin().Key
			f.prepare(key)
			if crash > 0 {
				f.db.crashAt = f.db.ops + crash
			}
			err := f.a.Abandon(f.token, key)
			if err != nil && !errors.Is(err, errCrash) {
				t.Fatalf("crash %d: %v", crash, err)
			}
			f.reopen(f.db.powerLoss(keep))
			got := f.states()
			if err == nil && len(got) != 0 {
				t.Fatalf("crash %d: completed abandon reopened as %v", crash, got)
			}
			if len(got) == 1 {
				if got[0] != InputPreparing {
					t.Fatalf("crash %d keep %b: %v", crash, keep, got)
				}
				if err := f.a.Abandon(f.token, key); err != nil {
					t.Fatalf("crash %d: retry abandon: %v", crash, err)
				}
			}
			f.live()
			if next := f.begin(); next.Sequence != 1 {
				t.Fatalf("crash %d keep %b: next sequence %d", crash, keep, next.Sequence)
			}
		}
	}
}

func TestOpenRejectsCorruptRecords(t *testing.T) {
	base := newFixture(t)
	base.live()
	key := base.begin().Key
	base.prepare(key)
	base.admit(key)
	base.closeLog(key)
	base.execReady(key)
	prep, admitted, ready := string(base.a.recordKey(0, "preparing")), string(base.a.recordKey(0, "admitted")),
		string(base.a.recordKey(0, "exec-ready"))
	abandoned, counter, low := string(base.a.recordKey(0, "abandoned")), string(base.a.key("counter")),
		string(base.a.key("lowwater"))
	edit := func(record string, change func(map[string]any)) func(map[string][]byte) {
		return func(db map[string][]byte) {
			var value map[string]any
			if err := json.Unmarshal(db[record], &value); err != nil {
				t.Fatalf("decode %s: %v", record, err)
			}
			change(value)
			db[record], _ = json.Marshal(value)
		}
	}
	other := strings.Repeat("5", 64)
	cases := map[string]func(map[string][]byte){
		"missing preparing":      func(db map[string][]byte) { delete(db, prep) },
		"preparing key":          edit(prep, func(v map[string]any) { v["observation"] = "x" }),
		"preparing sequence":     edit(prep, func(v map[string]any) { v["sequence"] = 1 }),
		"preparing hash":         edit(prep, func(v map[string]any) { v["candidate"].(map[string]any)["hash"] = "x" }),
		"preparing parent hash":  edit(prep, func(v map[string]any) { v["candidate"].(map[string]any)["parent_hash"] = "x" }),
		"preparing parent root":  edit(prep, func(v map[string]any) { v["candidate"].(map[string]any)["parent_root"] = "x" }),
		"unknown field":          edit(prep, func(v map[string]any) { v["extra"] = 1 }),
		"malformed preparing":    func(db map[string][]byte) { db[prep] = []byte("{") },
		"trailing data":          func(db map[string][]byte) { db[prep] = append(db[prep], []byte(" {}")...) },
		"admitted digest":        edit(admitted, func(v map[string]any) { v["digest"] = other }),
		"admitted pin digest":    edit(admitted, func(v map[string]any) { v["pin_digest"] = other }),
		"admitted capsule":       edit(admitted, func(v map[string]any) { v["capsule_digest"] = "x" }),
		"admitted key":           edit(admitted, func(v map[string]any) { v["observation"] = "x" }),
		"admitted sequence":      edit(admitted, func(v map[string]any) { v["sequence"] = 1 }),
		"admitted candidate":     edit(admitted, func(v map[string]any) { v["candidate"].(map[string]any)["number"] = 8 }),
		"exec ready alone":       func(db map[string][]byte) { delete(db, admitted) },
		"exec ready digest":      edit(ready, func(v map[string]any) { v["digest"] = other }),
		"exec ready consumed":    edit(ready, func(v map[string]any) { v["consumed_digest"] = other }),
		"exec ready admitted":    edit(ready, func(v map[string]any) { v["admitted_digest"] = other }),
		"exec ready key":         edit(ready, func(v map[string]any) { v["observation"] = "x" }),
		"exec ready sequence":    edit(ready, func(v map[string]any) { v["sequence"] = 1 }),
		"exec ready malformed":   edit(ready, func(v map[string]any) { v["exec_digest"] = "x" }),
		"abandoned and admitted": func(db map[string][]byte) { db[abandoned] = []byte(`{"observation":"` + key + `","sequence":0}`) },
		"abandoned content": func(db map[string][]byte) {
			delete(db, admitted)
			delete(db, ready)
			db[abandoned] = []byte(`{"observation":"x","sequence":0}`)
		},
		"abandoned and admitted only": func(db map[string][]byte) {
			delete(db, ready)
			db[abandoned] = []byte(`{"observation":"` + key + `","sequence":0}`)
		},
		"abandoned and ready": func(db map[string][]byte) {
			delete(db, admitted)
			db[abandoned] = []byte(`{"observation":"` + key + `","sequence":0}`)
		},
		"low-water above counter": func(db map[string][]byte) { db[low] = []byte(`{"sequence":2}`) },
		"malformed counter":       func(db map[string][]byte) { db[counter] = []byte(`{"next":-1}`) },
		"malformed low-water":     func(db map[string][]byte) { db[low] = []byte(`[]`) },
	}
	for name, damage := range cases {
		db := &memStore{durable: maps.Clone(base.db.durable)}
		damage(db.durable)
		if _, err := Open(base.token, db, base.producer(), base.pins, base.inputs); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A rejected observation: the record needs ADMITTED, no EXEC_READY and a
	// digest that recomputes.
	rejectedKey := string(base.a.recordKey(0, "rejected"))
	rejectedBase := maps.Clone(base.db.durable)
	delete(rejectedBase, ready)
	rejectedDigest, _ := RejectedDigest(base.v.AdmittedDigest, base.v.EvidenceDigest)
	rejectedBase[rejectedKey], _ = json.Marshal(RejectedRecord{Observation: key, Sequence: 0,
		AdmittedDigest: base.v.AdmittedDigest, EvidenceDigest: base.v.EvidenceDigest, Digest: rejectedDigest})
	if a, err := Open(base.token, &memStore{durable: maps.Clone(rejectedBase)}, base.producer(), base.pins, base.inputs); err != nil {
		t.Fatalf("clean rejected copy: %v", err)
	} else if list, _ := a.Observations(base.token); len(list) != 0 {
		t.Fatalf("a rejected observation is still open: %+v", list)
	}
	rejectedCases := map[string]func(map[string][]byte){
		"rejected without admitted": func(db map[string][]byte) { delete(db, admitted) },
		"rejected and exec ready":   func(db map[string][]byte) { db[ready] = base.db.durable[ready] },
		"rejected and abandoned": func(db map[string][]byte) {
			db[abandoned] = []byte(`{"observation":"` + key + `","sequence":0}`)
		},
		"rejected digest":    edit(rejectedKey, func(v map[string]any) { v["digest"] = other }),
		"rejected evidence":  edit(rejectedKey, func(v map[string]any) { v["evidence_digest"] = other }),
		"rejected admitted":  edit(rejectedKey, func(v map[string]any) { v["admitted_digest"] = other }),
		"rejected key":       edit(rejectedKey, func(v map[string]any) { v["observation"] = "x" }),
		"rejected sequence":  edit(rejectedKey, func(v map[string]any) { v["sequence"] = 1 }),
		"rejected malformed": edit(rejectedKey, func(v map[string]any) { v["evidence_digest"] = "x" }),
	}
	for name, damage := range rejectedCases {
		db := &memStore{durable: maps.Clone(rejectedBase)}
		damage(db.durable)
		if _, err := Open(base.token, db, base.producer(), base.pins, base.inputs); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A damaged candidate of an observation that is still INPUT_PREPARING.
	for _, field := range []string{"hash", "parent_hash", "parent_root"} {
		db := &memStore{durable: maps.Clone(base.db.durable)}
		delete(db.durable, admitted)
		delete(db.durable, ready)
		edit(prep, func(v map[string]any) { v["candidate"].(map[string]any)[field] = "x" })(db.durable)
		if _, err := Open(base.token, db, base.producer(), base.pins, base.inputs); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("INPUT_PREPARING %s: %v", field, err)
		}
	}
	// A clean copy opens.
	if _, err := Open(base.token, &memStore{durable: maps.Clone(base.db.durable)}, base.producer(), base.pins, base.inputs); err != nil {
		t.Fatalf("clean copy: %v", err)
	}
}

func TestBeginRefusesRecordBeyondCounter(t *testing.T) {
	for _, kind := range []string{"admitted", "exec-ready", "abandoned", "rejected"} {
		f := newFixture(t)
		f.live()
		f.db.durable[string(f.a.recordKey(0, kind))] = []byte("{}")
		if _, err := f.a.Begin(f.token, f.v.Candidate); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("%s beyond the counter: %v", kind, err)
		}
		if _, err := f.a.Observations(f.token); !errors.Is(err, ErrFailed) {
			t.Fatalf("%s: admission did not stop: %v", kind, err)
		}
	}
	f := newFixture(t)
	f.live()
	f.db.durable[string(f.a.recordKey(0, "preparing"))] = []byte("orphan")
	if obs := f.begin(); obs.Sequence != 0 {
		t.Fatalf("orphan INPUT_PREPARING was not overwritten")
	}
}

func TestStoreErrorsStopTheAdmission(t *testing.T) {
	type step func(f *fixture) error
	begin := func(f *fixture) error { _, err := f.a.Begin(f.token, f.v.Candidate); return err }
	admit := func(f *fixture) error { _, err := f.a.Admit(f.token, f.v.Key); return err }
	ready := func(f *fixture) error { _, err := f.a.ExecReady(f.token, f.v.Key, f.v.ExecDigest); return err }
	abandon := func(f *fixture) error { return f.a.Abandon(f.token, f.v.Key) }
	reject := func(f *fixture) error { _, err := f.a.Reject(f.token, f.v.Key, f.v.EvidenceDigest); return err }
	setups := map[string]func(f *fixture){
		"begin":   func(f *fixture) {},
		"admit":   func(f *fixture) { f.begin(); f.prepare(f.v.Key) },
		"ready":   func(f *fixture) { f.begin(); f.prepare(f.v.Key); f.admit(f.v.Key); f.closeLog(f.v.Key) },
		"abandon": func(f *fixture) { f.begin() },
		"reject":  func(f *fixture) { f.begin(); f.prepare(f.v.Key); f.admit(f.v.Key) },
	}
	steps := map[string]step{"begin": begin, "admit": admit, "ready": ready, "abandon": abandon, "reject": reject}
	for name, setup := range setups {
		for _, fault := range []string{"put", "sync", "has"} {
			if fault == "has" && name != "begin" {
				continue
			}
			f := newFixture(t)
			f.live()
			setup(f)
			switch fault {
			case "put":
				f.db.putErr = errDisk
			case "sync":
				f.db.syncErr = errDisk
			case "has":
				f.db.hasErr = errDisk
			}
			if err := steps[name](f); !errors.Is(err, errDisk) {
				t.Fatalf("%s %s: %v", name, fault, err)
			}
			f.db.putErr, f.db.syncErr, f.db.hasErr = nil, nil, nil
			if err := steps[name](f); !errors.Is(err, ErrFailed) {
				t.Fatalf("%s %s: retry on a stopped admission: %v", name, fault, err)
			}
			for other, run := range steps {
				if err := run(f); !errors.Is(err, ErrFailed) {
					t.Fatalf("%s %s: %s after the stop: %v", name, fault, other, err)
				}
			}
		}
	}
	f := newFixture(t)
	f.live()
	f.begin()
	f.pins.releaseErr = errDisk
	if err := f.a.Abandon(f.token, f.v.Key); !errors.Is(err, errDisk) {
		t.Fatalf("release error: %v", err)
	}
	if err := f.a.GoLive(f.token); !errors.Is(err, ErrFailed) {
		t.Fatalf("release error did not stop the admission: %v", err)
	}
	// Abandon writes the low-water mark in a second step.
	f = newFixture(t)
	f.live()
	f.begin()
	f.db.crashAt = f.db.ops + 3
	if err := f.a.Abandon(f.token, f.v.Key); !errors.Is(err, errCrash) {
		t.Fatalf("low-water put: %v", err)
	}
	f = newFixture(t)
	f.live()
	f.begin()
	f.db.crashAt = f.db.ops + 4
	if err := f.a.Abandon(f.token, f.v.Key); !errors.Is(err, errCrash) {
		t.Fatalf("low-water sync: %v", err)
	}
}

func TestOpenChecksArgumentsAndStoreErrors(t *testing.T) {
	v := loadVectors(t)
	guard := NewGuard()
	token := guard.Acquire()
	p := Producer{ProducerID: v.Producer, RunID: v.Run}
	pins, inputs := newPins(), newInputs()
	for _, bad := range []Producer{{"", "r"}, {"p", ""}, {"P", "r"}, {"p", "r.x"}, {"-p", "r"}, {"p", "r/x"},
		{strings.Repeat("p", 41), "r"}, {"p", strings.Repeat("r", 41)}} {
		if _, err := Open(token, newStore(), bad, pins, inputs); !errors.Is(err, ErrInvalid) {
			t.Fatalf("producer %+v: %v", bad, err)
		}
	}
	if _, err := Open(token, newStore(), Producer{strings.Repeat("p", 40), strings.Repeat("r", 40)}, pins, inputs); err != nil {
		t.Fatalf("longest producer: %v", err)
	}
	if _, err := Open(token, nil, p, pins, inputs); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil store: %v", err)
	}
	if _, err := Open(token, newStore(), p, nil, inputs); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil pins: %v", err)
	}
	if _, err := Open(token, newStore(), p, pins, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil inputs: %v", err)
	}
	for name, db := range map[string]*memStore{
		"sync": {durable: map[string][]byte{}, syncErr: errDisk},
		"has":  {durable: map[string][]byte{}, hasErr: errDisk},
	} {
		if _, err := Open(token, db, p, pins, inputs); !errors.Is(err, errDisk) {
			t.Fatalf("%s error: %v", name, err)
		}
	}
	f := newFixture(t)
	f.live()
	f.begin()
	for _, suffix := range []string{"counter", "lowwater", "preparing", "admitted", "exec-ready", "abandoned", "rejected"} {
		f.db.durable[string(f.a.key("lowwater"))] = []byte(`{"sequence":0}`)
		f.db.durable[string(f.a.recordKey(0, "admitted"))] = []byte("{}")
		f.db.durable[string(f.a.recordKey(0, "exec-ready"))] = []byte("{}")
		f.db.durable[string(f.a.recordKey(0, "abandoned"))] = []byte("{}")
		f.db.durable[string(f.a.recordKey(0, "rejected"))] = []byte("{}")
		db := &memStore{durable: maps.Clone(f.db.durable), getErrFor: suffix}
		if _, err := Open(f.token, db, f.producer(), f.pins, f.inputs); !errors.Is(err, errDisk) {
			t.Fatalf("get error on %s: %v", suffix, err)
		}
	}
	if _, err := Open(nil, newStore(), p, pins, inputs); !errors.Is(err, ErrGuard) {
		t.Fatalf("nil token: %v", err)
	}
	if err := token.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := Open(token, newStore(), p, pins, inputs); !errors.Is(err, ErrGuard) {
		t.Fatalf("released token: %v", err)
	}
}

func TestBeginRejectsMalformedCandidate(t *testing.T) {
	f := newFixture(t)
	f.live()
	for _, field := range []string{"hash", "parent_hash", "parent_root"} {
		for _, bad := range []string{"", "x", strings.ToUpper(f.v.Candidate.Hash), f.v.Candidate.Hash + "0"} {
			c := f.v.Candidate
			switch field {
			case "hash":
				c.Hash = bad
			case "parent_hash":
				c.ParentHash = bad
			default:
				c.ParentRoot = bad
			}
			if _, err := f.a.Begin(f.token, c); !errors.Is(err, ErrInvalid) {
				t.Fatalf("%s %q: %v", field, bad, err)
			}
		}
	}
	if len(f.db.writes) != 0 {
		t.Fatalf("a refused Begin wrote %v", f.db.writes)
	}
}

func TestDigestsRejectMalformedHex(t *testing.T) {
	v := loadVectors(t)
	good := v.CapsuleDigest
	for _, bad := range []string{"", good[:62], good + "00", strings.ToUpper(good), "zz" + good[2:]} {
		c := v.Candidate
		c.ParentRoot = bad
		if _, err := AdmittedDigest(v.Key, 0, c, good, good); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("admitted candidate %q: %v", bad, err)
		}
		if _, err := AdmittedDigest(v.Key, 0, v.Candidate, bad, good); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("admitted pin %q: %v", bad, err)
		}
		if _, err := AdmittedDigest(v.Key, 0, v.Candidate, good, bad); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("admitted capsule %q: %v", bad, err)
		}
		if _, err := ConsumedDigest(bad, 0, good); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("consumed capsule %q: %v", bad, err)
		}
		if _, err := ConsumedDigest(good, 0, bad); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("consumed chain %q: %v", bad, err)
		}
		for i := range 3 {
			args := []string{good, good, good}
			args[i] = bad
			if _, err := ExecReadyDigest(args[0], args[1], args[2]); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("exec ready argument %d %q: %v", i, bad, err)
			}
		}
	}
}

func TestGuard(t *testing.T) {
	f := newFixture(t)
	other := NewGuard().Acquire()
	calls := map[string]func(*Token) error{
		"observations": func(tok *Token) error { _, err := f.a.Observations(tok); return err },
		"go live":      func(tok *Token) error { return f.a.GoLive(tok) },
		"begin":        func(tok *Token) error { _, err := f.a.Begin(tok, f.v.Candidate); return err },
		"admit":        func(tok *Token) error { _, err := f.a.Admit(tok, f.v.Key); return err },
		"exec ready":   func(tok *Token) error { _, err := f.a.ExecReady(tok, f.v.Key, f.v.ExecDigest); return err },
		"abandon":      func(tok *Token) error { return f.a.Abandon(tok, f.v.Key) },
		"reject":       func(tok *Token) error { _, err := f.a.Reject(tok, f.v.Key, f.v.EvidenceDigest); return err },
	}
	for name, call := range calls {
		for label, tok := range map[string]*Token{"nil": nil, "other guard": other, "empty": {}} {
			if err := call(tok); !errors.Is(err, ErrGuard) {
				t.Fatalf("%s with %s token: %v", name, label, err)
			}
		}
	}
	if err := (&Token{}).Release(); !errors.Is(err, ErrGuard) {
		t.Fatalf("release of an empty token: %v", err)
	}
	var nilToken *Token
	if err := nilToken.Release(); !errors.Is(err, ErrGuard) {
		t.Fatalf("release of a nil token: %v", err)
	}
	if err := f.token.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := f.token.Release(); !errors.Is(err, ErrGuard) {
		t.Fatalf("second release: %v", err)
	}
	for name, call := range calls {
		if err := call(f.token); !errors.Is(err, ErrGuard) {
			t.Fatalf("%s with a released token: %v", name, err)
		}
	}
	// A nil token is refused also while the guard is free.
	for name, call := range calls {
		if err := call(nil); !errors.Is(err, ErrGuard) {
			t.Fatalf("%s with a nil token on a free guard: %v", name, err)
		}
	}
	// A new holder of the same guard may use the admission.
	holder := f.guard.Acquire()
	if err := f.a.GoLive(holder); err != nil {
		t.Fatalf("new holder: %v", err)
	}
	acquired := make(chan *Token)
	go func() { acquired <- f.guard.Acquire() }()
	select {
	case <-acquired:
		t.Fatalf("Acquire returned while the guard was held")
	case <-time.After(20 * time.Millisecond):
	}
	if err := f.a.GoLive(holder); err != nil {
		t.Fatalf("holder lost the guard to a waiter: %v", err)
	}
	if err := holder.Release(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	waiter := <-acquired
	if err := holder.Release(); !errors.Is(err, ErrGuard) {
		t.Fatalf("stale token released the guard of a new holder: %v", err)
	}
	if err := f.a.GoLive(waiter); err != nil {
		t.Fatalf("waiter: %v", err)
	}
	if err := f.a.GoLive(holder); !errors.Is(err, ErrGuard) {
		t.Fatalf("stale token used the admission: %v", err)
	}
	if err := waiter.Release(); err != nil {
		t.Fatalf("release waiter: %v", err)
	}
}

func TestObservationsReturnCopies(t *testing.T) {
	f := newFixture(t)
	f.live()
	key := f.begin().Key
	f.prepare(key)
	f.admit(key)
	f.closeLog(key)
	f.execReady(key)
	list, _ := f.a.Observations(f.token)
	list[0].Admitted.Digest = "changed"
	list[0].ExecReady.Digest = "changed"
	list[0].State = Abandoned
	again, _ := f.a.Observations(f.token)
	if again[0].Admitted.Digest != f.v.AdmittedDigest || again[0].ExecReady.Digest != f.v.ExecReadyDigest ||
		again[0].State != ExecReady {
		t.Fatalf("a caller changed the stored observation: %+v", again[0])
	}
}

// TestRejectClosesAnAdmittedObservation: native validation refused an
// ADMITTED observation. Reject records NATIVE_REJECTED, releases the pin and
// unblocks new admissions, also in RECOVERY_ONLY after a restart.
func TestRejectClosesAnAdmittedObservation(t *testing.T) {
	f := newFixture(t)
	f.live()
	key := f.begin().Key
	if _, err := f.a.Reject(f.token, key, f.v.EvidenceDigest); !errors.Is(err, ErrState) {
		t.Fatalf("reject of INPUT_PREPARING: %v", err)
	}
	f.prepare(key)
	f.admit(key)
	f.reopen(f.db)
	for _, bad := range []string{"", "x", strings.ToUpper(f.v.EvidenceDigest), f.v.EvidenceDigest + "0"} {
		if _, err := f.a.Reject(f.token, key, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("evidence digest %q: %v", bad, err)
		}
	}
	if len(f.pins.released) != 0 {
		t.Fatalf("a refused Reject released %v", f.pins.released)
	}
	record, err := f.a.Reject(f.token, key, f.v.EvidenceDigest)
	want := RejectedRecord{Observation: key, Sequence: 0, AdmittedDigest: f.v.AdmittedDigest,
		EvidenceDigest: f.v.EvidenceDigest, Digest: f.v.RejectedDigest}
	if err != nil || record != want {
		t.Fatalf("reject %+v %v, want %+v", record, err, want)
	}
	if got, err := RejectedDigest(f.v.AdmittedDigest, f.v.EvidenceDigest); err != nil || got != f.v.RejectedDigest {
		t.Fatalf("rejected digest %s %v", got, err)
	}
	if !slices.Equal(f.pins.released, []string{key}) || len(f.states()) != 0 {
		t.Fatalf("released %v, states %v", f.pins.released, f.states())
	}
	if _, err := f.a.Reject(f.token, key, f.v.EvidenceDigest); !errors.Is(err, ErrUnknown) {
		t.Fatalf("reject twice: %v", err)
	}
	var low lowwaterRecord
	if ok, err := readRecord(f.db, f.a.key("lowwater"), &low); !ok || err != nil || low.Sequence != 1 {
		t.Fatalf("low-water %+v %v %v, want 1", low, ok, err)
	}
	f.reopen(f.db)
	if len(f.states()) != 0 {
		t.Fatalf("reopened states %v", f.states())
	}
	f.live()
	next := f.begin()
	f.prepare(next.Key)
	f.admit(next.Key)
	f.closeLog(next.Key)
	f.execReady(next.Key)
	if _, err := f.a.Reject(f.token, next.Key, f.v.EvidenceDigest); !errors.Is(err, ErrState) {
		t.Fatalf("reject of EXEC_READY: %v", err)
	}
}

func TestRejectPowerLoss(t *testing.T) {
	for crash := 0; crash <= 4; crash++ {
		for keep := uint(0); keep < 2; keep++ {
			f := newFixture(t)
			f.live()
			key := f.begin().Key
			f.prepare(key)
			f.admit(key)
			if crash > 0 {
				f.db.crashAt = f.db.ops + crash
			}
			_, err := f.a.Reject(f.token, key, f.v.EvidenceDigest)
			if err != nil && !errors.Is(err, errCrash) {
				t.Fatalf("crash %d: %v", crash, err)
			}
			f.reopen(f.db.powerLoss(keep))
			got := f.states()
			if err == nil && len(got) != 0 {
				t.Fatalf("crash %d: completed reject reopened as %v", crash, got)
			}
			if len(got) == 1 {
				// The pin was released before the record: EXEC_READY is refused
				// and the repeated Reject completes.
				if got[0] != Admitted {
					t.Fatalf("crash %d keep %b: %v", crash, keep, got)
				}
				f.closeLog(key)
				if _, err := f.a.ExecReady(f.token, key, f.v.ExecDigest); !errors.Is(err, ErrBinding) {
					t.Fatalf("crash %d: EXEC_READY without the pin: %v", crash, err)
				}
				if _, err := f.a.Reject(f.token, key, f.v.EvidenceDigest); err != nil {
					t.Fatalf("crash %d: retry reject: %v", crash, err)
				}
			}
			f.live()
			if next := f.begin(); next.Sequence != 1 {
				t.Fatalf("crash %d keep %b: next sequence %d", crash, keep, next.Sequence)
			}
		}
	}
}
