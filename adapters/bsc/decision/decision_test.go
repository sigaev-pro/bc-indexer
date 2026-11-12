package decision

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/extractor/admission"
)

// vectorFile holds the independent expected values. `python3
// test/harness/decision.py vectors` computes them from the README formulas;
// test/harness/test_decision.py checks that the file matches.
type vectorFile struct {
	StoreID               string              `json:"store_id"`
	Producer              string              `json:"producer"`
	Run                   string              `json:"run"`
	Candidate             admission.Candidate `json:"candidate"`
	PinDigest             string              `json:"pin_digest"`
	CapsuleDigest         string              `json:"capsule_digest"`
	ConsumedCount         uint64              `json:"consumed_count"`
	ConsumedChain         string              `json:"consumed_chain"`
	ConsumedDigest        string              `json:"consumed_digest"`
	ExecDigest            string              `json:"exec_digest"`
	ExecReadyDigest       string              `json:"exec_ready_digest"`
	OldHead               Head                `json:"old_head"`
	NewHead               Head                `json:"new_head"`
	ChildRoot             string              `json:"child_root"`
	DecisionEvidence      string              `json:"decision_evidence"`
	RejectionEvidence     string              `json:"rejection_evidence"`
	ImportIntent          IntentRecord        `json:"import_intent"`
	ImportMarkerKey       string              `json:"import_marker_key"`
	ImportMarker          string              `json:"import_marker"`
	ImportDecidedDigest   string              `json:"import_decided_digest"`
	ImportRejectedDigest  string              `json:"import_rejected_digest"`
	MemberDecidedDigest   string              `json:"member_decided_digest"`
	MemberRejectedDigest  string              `json:"member_rejected_digest"`
	FinalityIntent        IntentRecord        `json:"finality_intent"`
	FinalityMarker        string              `json:"finality_marker"`
	FinalityDecidedDigest string              `json:"finality_decided_digest"`
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
	errCrash = errors.New("injected power loss")
	errDisk  = errors.New("injected disk error")
	errNoPin = errors.New("no such pin")
)

type write struct {
	key     string
	value   []byte
	deleted bool
}

// memStore models the native key-value store. Put and Delete stay pending
// until SyncKeyValue makes them durable. powerLoss keeps a chosen subset of
// pending writes and drops the rest. crashAt makes the mutating operation
// with that index (Put, Delete or SyncKeyValue, counted from 1) fail without
// effect.
type memStore struct {
	durable   map[string][]byte
	pending   []write
	ops       int
	crashAt   int
	putErr    error
	syncErr   error
	deleteErr error
	hasErrFor string
	getErrFor string
	writes    int
	// maxPending is the largest number of unsynced writes seen.
	maxPending int
}

func newStore() *memStore { return &memStore{durable: map[string][]byte{}} }

func (s *memStore) view(key string) ([]byte, bool) {
	for i := len(s.pending) - 1; i >= 0; i-- {
		if s.pending[i].key == key {
			return s.pending[i].value, !s.pending[i].deleted
		}
	}
	value, ok := s.durable[key]
	return value, ok
}

func (s *memStore) Has(key []byte) (bool, error) {
	if s.hasErrFor != "" && strings.HasSuffix(string(key), s.hasErrFor) {
		return false, errDisk
	}
	_, ok := s.view(string(key))
	return ok, nil
}

func (s *memStore) Get(key []byte) ([]byte, error) {
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
	s.pending = append(s.pending, write{key: string(key), value: bytes.Clone(value)})
	s.writes++
	s.maxPending = max(s.maxPending, len(s.pending))
	return nil
}

func (s *memStore) Delete(key []byte) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	if err := s.step(); err != nil {
		return err
	}
	s.pending = append(s.pending, write{key: string(key), deleted: true})
	s.writes++
	s.maxPending = max(s.maxPending, len(s.pending))
	return nil
}

func apply(durable map[string][]byte, w write) {
	if w.deleted {
		delete(durable, w.key)
	} else {
		durable[w.key] = w.value
	}
}

func (s *memStore) SyncKeyValue() error {
	if s.syncErr != nil {
		return s.syncErr
	}
	if err := s.step(); err != nil {
		return err
	}
	for _, w := range s.pending {
		apply(s.durable, w)
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
			apply(out.durable, w)
		}
	}
	return out
}

// fakePins is a durable pin ledger.
type fakePins struct {
	roots    map[string]string
	digests  map[string]string
	released []string
}

func (p *fakePins) Verify(id, root string) (string, error) {
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
	delete(p.roots, id)
	delete(p.digests, id)
	p.released = append(p.released, id)
	return nil
}

// fakeInputs holds the sealed capsules and closed consumed manifests.
type fakeInputs struct {
	capsules map[string]string
	consumed map[string]admission.Consumed
}

func (f *fakeInputs) CapsuleDigest(id string) (string, error) {
	digest, ok := f.capsules[id]
	if !ok {
		return "", errors.New("capsule is not sealed")
	}
	return digest, nil
}

func (f *fakeInputs) Consumed(id string) (admission.Consumed, error) {
	manifest, ok := f.consumed[id]
	if !ok {
		return admission.Consumed{}, errors.New("log is not closed")
	}
	return manifest, nil
}

// fakeRoots holds the durable native state roots.
type fakeRoots struct {
	durable map[string]bool
	err     error
}

func (r *fakeRoots) Durable(root string) (bool, error) { return r.durable[root], r.err }

// fakeMirror is the applied-decision frontier of the mirror.
type fakeMirror struct {
	applied map[uint64]string
	// applies counts how often each decision was applied.
	applies map[uint64]int
	calls   int
	err     error
}

func (m *fakeMirror) Applied(seq uint64, digest string) (bool, error) {
	m.calls++
	if m.err != nil {
		return false, m.err
	}
	got, ok := m.applied[seq]
	return ok && got == digest, nil
}

// apply applies every open obligation, as the mirror and READY of E05 do.
func (m *fakeMirror) apply(obligations []Obligation) {
	for _, o := range obligations {
		m.applied[o.Decided.Sequence] = o.Decided.Digest
		m.applies[o.Decided.Sequence]++
	}
}

// fixture is one producer run with its store, admission, bridge and token.
// pins, inputs, roots and mirror model durable native state and survive a
// power loss of the store.
type fixture struct {
	t      *testing.T
	v      vectorFile
	db     *memStore
	pins   *fakePins
	inputs *fakeInputs
	roots  *fakeRoots
	mirror *fakeMirror
	token  *admission.Token
	adm    *admission.Admission
	b      *Bridge
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, v: loadVectors(t), db: newStore(),
		pins:   &fakePins{roots: map[string]string{}, digests: map[string]string{}},
		inputs: &fakeInputs{capsules: map[string]string{}, consumed: map[string]admission.Consumed{}},
		roots:  &fakeRoots{durable: map[string]bool{}},
		mirror: &fakeMirror{applied: map[uint64]string{}, applies: map[uint64]int{}}}
	f.token = admission.NewGuard().Acquire()
	f.reopen(f.db)
	return f
}

func (f *fixture) producer() admission.Producer {
	return admission.Producer{ProducerID: f.v.Producer, RunID: f.v.Run}
}

func (f *fixture) reopen(db *memStore) {
	f.t.Helper()
	if err := f.tryReopen(db); err != nil {
		f.t.Fatalf("reopen: %v", err)
	}
}

func (f *fixture) tryReopen(db *memStore) error {
	f.db = db
	adm, err := admission.Open(f.token, db, f.producer(), f.pins, f.inputs)
	if err != nil {
		return err
	}
	b, err := Open(f.token, db, adm, f.v.StoreID, f.roots, f.mirror)
	if err != nil {
		return err
	}
	f.adm, f.b = adm, b
	return nil
}

func (f *fixture) live() {
	f.t.Helper()
	if err := f.b.GoLive(f.token); err != nil {
		f.t.Fatalf("go live: %v", err)
	}
}

// execReady runs one observation of candidate c to EXEC_READY.
func (f *fixture) execReady(c admission.Candidate) admission.ExecReadyRecord {
	f.t.Helper()
	obs, err := f.adm.Begin(f.token, c)
	if err != nil {
		f.t.Fatalf("begin: %v", err)
	}
	f.pins.roots[obs.Key], f.pins.digests[obs.Key] = c.ParentRoot, f.v.PinDigest
	f.inputs.capsules[obs.Key] = f.v.CapsuleDigest
	if _, err := f.adm.Admit(f.token, obs.Key); err != nil {
		f.t.Fatalf("admit: %v", err)
	}
	f.inputs.consumed[obs.Key] = admission.Consumed{Observation: obs.Key, CapsuleDigest: f.v.CapsuleDigest,
		Count: f.v.ConsumedCount, Chain: f.v.ConsumedChain, Digest: f.v.ConsumedDigest}
	ready, err := f.adm.ExecReady(f.token, obs.Key, f.v.ExecDigest)
	if err != nil {
		f.t.Fatalf("exec ready: %v", err)
	}
	return ready
}

func (f *fixture) importIntent(ready admission.ExecReadyRecord) Intent {
	f.roots.durable[f.v.ChildRoot] = true
	return Intent{Kind: Import, OldHead: f.v.OldHead, NewHead: f.v.NewHead, EvidenceDigest: f.v.DecisionEvidence,
		Events: true, Members: []Member{{Observation: ready.Observation, ExecReadyDigest: ready.Digest,
			ChildRoot: f.v.ChildRoot}}}
}

func (f *fixture) finalityIntent() Intent {
	return Intent{Kind: Finality, OldHead: f.v.NewHead, NewHead: f.v.NewHead, EvidenceDigest: f.v.DecisionEvidence}
}

func (f *fixture) intend(in Intent) IntentRecord {
	f.t.Helper()
	record, err := f.b.Intend(f.token, in)
	if err != nil {
		f.t.Fatalf("intend: %v", err)
	}
	return record
}

// native models the native patch: it writes the marker in its final batch
// and syncs.
func (f *fixture) native(record IntentRecord) {
	f.t.Helper()
	value, err := MarkerValue(record)
	if err != nil {
		f.t.Fatalf("marker value: %v", err)
	}
	if err := f.db.Put(MarkerKey(f.producer(), record.Sequence), value); err != nil {
		f.t.Fatalf("native batch: %v", err)
	}
	if err := f.db.SyncKeyValue(); err != nil {
		f.t.Fatalf("native sync: %v", err)
	}
}

func (f *fixture) resolve(seq uint64) DecidedRecord {
	f.t.Helper()
	record, err := f.b.Resolve(f.token, seq)
	if err != nil {
		f.t.Fatalf("resolve: %v", err)
	}
	return record
}

func (f *fixture) obligations() []Obligation {
	f.t.Helper()
	list, err := f.b.Obligations(f.token)
	if err != nil {
		f.t.Fatalf("obligations: %v", err)
	}
	return list
}

func (f *fixture) decisions() []Decision {
	f.t.Helper()
	list, err := f.b.Decisions(f.token)
	if err != nil {
		f.t.Fatalf("decisions: %v", err)
	}
	return list
}

func (f *fixture) cleanup() {
	f.t.Helper()
	if err := f.b.Cleanup(f.token); err != nil {
		f.t.Fatalf("cleanup: %v", err)
	}
}

func (f *fixture) openObservations() []admission.Observation {
	f.t.Helper()
	list, err := f.adm.Observations(f.token)
	if err != nil {
		f.t.Fatalf("observations: %v", err)
	}
	return list
}

func (f *fixture) hasMarker(seq uint64) bool {
	_, ok := f.db.durable[string(MarkerKey(f.producer(), seq))]
	return ok
}

// admissionDecided returns the DECIDED record digest that admission wrote
// for an observation sequence.
func (f *fixture) admissionDecided(seq uint64) string {
	key := fmt.Sprintf("extractor-admission-v1/%s.%s/%016x/decided", f.v.Producer, f.v.Run, seq)
	var record admission.DecidedRecord
	if err := strictDecode(f.db.durable[key], &record); err != nil {
		f.t.Fatalf("admission DECIDED record %d: %v", seq, err)
	}
	return record.DecisionDigest
}

func otherCandidate(f *fixture) admission.Candidate {
	c := f.v.Candidate
	c.Number, c.Hash, c.ParentHash = 8, strings.Repeat("ab", 32), c.Hash
	return c
}

func TestDigestVectors(t *testing.T) {
	v := loadVectors(t)
	for _, record := range []IntentRecord{v.ImportIntent, v.FinalityIntent} {
		if got, err := IntentDigest(record); err != nil || got != record.Digest {
			t.Fatalf("intent %d digest %s %v, want %s", record.Sequence, got, err, record.Digest)
		}
	}
	for record, want := range map[*IntentRecord]string{&v.ImportIntent: v.ImportMarker,
		&v.FinalityIntent: v.FinalityMarker} {
		if got, err := MarkerValue(*record); err != nil || string(got) != want {
			t.Fatalf("marker %s %v, want %s", got, err, want)
		}
	}
	p := admission.Producer{ProducerID: v.Producer, RunID: v.Run}
	if got := string(MarkerKey(p, 0)); got != v.ImportMarkerKey || len(got) <= 33 || got[0] != 'e' {
		t.Fatalf("marker key %s, want %s", got, v.ImportMarkerKey)
	}
	if got, _ := DecidedDigest(v.ImportIntent.Digest, Batch); got != v.ImportDecidedDigest {
		t.Fatalf("decided digest %s", got)
	}
	if got, _ := DecidedDigest(v.FinalityIntent.Digest, Checkpoint); got != v.FinalityDecidedDigest {
		t.Fatalf("checkpoint digest %s", got)
	}
	if got, _ := RejectedDigest(v.ImportIntent.Digest, v.RejectionEvidence); got != v.ImportRejectedDigest {
		t.Fatalf("rejected digest %s", got)
	}
	if got, _ := admission.DecidedDigest(v.ExecReadyDigest, v.ImportDecidedDigest); got != v.MemberDecidedDigest {
		t.Fatalf("member decided digest %s", got)
	}
	if _, err := DecidedDigest("AB", Batch); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed intent digest: %v", err)
	}
	if _, err := RejectedDigest("AB", v.RejectionEvidence); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed rejected intent digest: %v", err)
	}
	if _, err := RejectedDigest(v.ImportIntent.Digest, "AB"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed evidence digest: %v", err)
	}
	for _, damage := range []func(r *IntentRecord){
		func(r *IntentRecord) { r.OldHead.Hash = "x" },
		func(r *IntentRecord) { r.NewHead.Hash = "x" },
		func(r *IntentRecord) { r.Members[0].Block = "x" },
		func(r *IntentRecord) { r.Members[0].ExecReadyDigest = "x" },
		func(r *IntentRecord) { r.Members[0].ChildRoot = "x" },
		func(r *IntentRecord) { r.EvidenceDigest = "x" },
		func(r *IntentRecord) { r.EvidenceDigest = "0" + r.EvidenceDigest },
	} {
		record := v.ImportIntent
		record.Members = slices.Clone(record.Members)
		damage(&record)
		if _, err := IntentDigest(record); !errors.Is(err, ErrInvalid) {
			t.Fatalf("damaged intent accepted: %v", err)
		}
	}
	// Every field enters the digest.
	reference := v.ImportIntent
	for i, change := range []func(r *IntentRecord){
		func(r *IntentRecord) { r.NativeStoreID = "other" },
		func(r *IntentRecord) { r.Producer = "other" },
		func(r *IntentRecord) { r.Sequence = 9 },
		func(r *IntentRecord) { r.Kind = Canonical },
		func(r *IntentRecord) { r.OldHead.Number = 1 },
		func(r *IntentRecord) { r.NewHead.Number = 1 },
		func(r *IntentRecord) { r.Members = nil },
		func(r *IntentRecord) { r.Members[0].Observation = "other" },
		func(r *IntentRecord) { r.Members[0].ChildRoot = v.ExecDigest },
		func(r *IntentRecord) { r.EvidenceDigest = v.ExecDigest },
		func(r *IntentRecord) { r.Events = false },
	} {
		record := reference
		record.Members = slices.Clone(reference.Members)
		change(&record)
		if got, err := IntentDigest(record); err != nil || got == reference.Digest {
			t.Fatalf("change %d left the digest unchanged: %v", i, err)
		}
	}
}

// TestImportAndFinality: the full flow of an IMPORT decision with public
// events and a FINALITY decision without them, with the independent values.
func TestImportAndFinality(t *testing.T) {
	f := newFixture(t)
	f.live()
	ready := f.execReady(f.v.Candidate)
	if ready.Digest != f.v.ExecReadyDigest {
		t.Fatalf("EXEC_READY digest %s, want the admission vector", ready.Digest)
	}
	intent := f.intend(f.importIntent(ready))
	if !reflect.DeepEqual(intent, f.v.ImportIntent) {
		t.Fatalf("intent %+v, want %+v", intent, f.v.ImportIntent)
	}
	// No marker yet: the intent is not a decision and writes nothing.
	writes := f.db.writes
	if _, err := f.b.Resolve(f.token, 0); !errors.Is(err, ErrNoMarker) {
		t.Fatalf("resolve without marker: %v", err)
	}
	if f.db.writes != writes || len(f.obligations()) != 0 {
		t.Fatalf("an intent without marker wrote or listed an obligation")
	}
	f.native(intent)
	decided := f.resolve(0)
	if decided != (DecidedRecord{Sequence: 0, IntentDigest: intent.Digest, Delivery: Batch,
		Digest: f.v.ImportDecidedDigest}) {
		t.Fatalf("decided %+v", decided)
	}
	if got := f.admissionDecided(0); got != f.v.ImportDecidedDigest {
		t.Fatalf("observation closed with %s", got)
	}
	if !slices.Equal(f.pins.released, []string{ready.Observation}) || len(f.openObservations()) != 0 {
		t.Fatalf("pin or observation not closed: %v", f.pins.released)
	}
	writes = f.db.writes
	if again := f.resolve(0); again != decided || f.db.writes != writes {
		t.Fatalf("repeated resolve %+v wrote %d", again, f.db.writes-writes)
	}
	got := f.obligations()
	if len(got) != 1 || got[0].Decided != decided || !reflect.DeepEqual(got[0].Intent, intent) {
		t.Fatalf("obligations %+v", got)
	}
	// The marker stays until the mirror applied the decision.
	f.cleanup()
	if !f.hasMarker(0) || len(f.decisions()) != 1 {
		t.Fatalf("cleanup removed an unapplied marker")
	}
	f.mirror.apply(got)
	if len(f.obligations()) != 0 {
		t.Fatalf("an applied decision is still an obligation")
	}
	f.cleanup()
	if f.hasMarker(0) || len(f.decisions()) != 0 {
		t.Fatalf("cleanup kept the marker of an applied decision")
	}
	if _, ok := f.db.durable[string(f.b.recordKey(0, "intent"))]; ok {
		t.Fatalf("records below the low-water mark were not deleted")
	}

	// A FINALITY decision without public events: a checkpoint, no
	// obligation, and its marker goes at once.
	final := f.intend(f.finalityIntent())
	if !reflect.DeepEqual(final, f.v.FinalityIntent) {
		t.Fatalf("finality intent %+v, want %+v", final, f.v.FinalityIntent)
	}
	f.native(final)
	if checkpoint := f.resolve(1); checkpoint.Delivery != Checkpoint || checkpoint.Digest != f.v.FinalityDecidedDigest {
		t.Fatalf("checkpoint %+v", checkpoint)
	}
	if len(f.obligations()) != 0 {
		t.Fatalf("a decision without public events has an obligation")
	}
	calls := f.mirror.calls
	f.cleanup()
	if f.hasMarker(1) || len(f.decisions()) != 0 || f.mirror.calls != calls {
		t.Fatalf("checkpoint cleanup: marker %v, mirror calls %d", f.hasMarker(1), f.mirror.calls-calls)
	}
	f.reopen(f.db)
	if len(f.decisions()) != 0 || string(f.db.durable[string(f.b.key("lowwater"))]) != `{"sequence":2}` ||
		string(f.db.durable[string(f.b.key("purged"))]) != `{"sequence":2}` {
		t.Fatalf("after reopen: %v", f.decisions())
	}
	f.cleanup()
}

func TestIntendRefusals(t *testing.T) {
	f := newFixture(t)
	f.live()
	ready := f.execReady(f.v.Candidate)
	good := f.importIntent(ready)
	cases := map[string]struct {
		change func(in *Intent)
		want   error
	}{
		"kind":                {func(in *Intent) { in.Kind = "OTHER" }, ErrInvalid},
		"old head hash":       {func(in *Intent) { in.OldHead.Hash = "AB" }, ErrInvalid},
		"new head hash":       {func(in *Intent) { in.NewHead.Hash = "" }, ErrInvalid},
		"evidence digest":     {func(in *Intent) { in.EvidenceDigest = "zz" }, ErrInvalid},
		"import no members":   {func(in *Intent) { in.Members = nil }, ErrInvalid},
		"side with events":    {func(in *Intent) { in.Kind, in.OldHead = Side, in.NewHead }, ErrInvalid},
		"side moves the head": {func(in *Intent) { in.Kind, in.Events = Side, false }, ErrInvalid},
		"side no members": {func(in *Intent) {
			in.Kind, in.Events, in.OldHead, in.Members = Side, false, in.NewHead, nil
		}, ErrInvalid},
		"finality members":   {func(in *Intent) { in.Kind = Finality }, ErrInvalid},
		"member digest":      {func(in *Intent) { in.Members[0].ExecReadyDigest = "x" }, ErrInvalid},
		"member root":        {func(in *Intent) { in.Members[0].ChildRoot = "x" }, ErrInvalid},
		"unknown member":     {func(in *Intent) { in.Members[0].Observation = "producer-1.run-1.0000000000000009" }, ErrBinding},
		"other EXEC_READY":   {func(in *Intent) { in.Members[0].ExecReadyDigest = f.v.ExecDigest }, ErrBinding},
		"volatile root":      {func(in *Intent) { in.Members[0].ChildRoot = f.v.ExecDigest }, ErrChildRoot},
		"member named twice": {func(in *Intent) { in.Members = append(in.Members, in.Members[0]) }, ErrDuplicate},
	}
	for name, tc := range cases {
		in := good
		in.Members = slices.Clone(good.Members)
		tc.change(&in)
		writes := f.db.writes
		_, err := f.b.Intend(f.token, in)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v, want %v", name, err, tc.want)
		}
		// The refusal names its cause.
		if strings.HasSuffix(name, "hash") && !strings.Contains(err.Error(), "head hash") ||
			name == "evidence digest" && !strings.Contains(err.Error(), "evidence digest") {
			t.Fatalf("%s: refusal does not name the cause: %v", name, err)
		}
		if f.db.writes != writes {
			t.Fatalf("%s: a refused intent wrote", name)
		}
	}
	f.roots.err = errDisk
	if _, err := f.b.Intend(f.token, good); !errors.Is(err, ErrChildRoot) || !errors.Is(err, errDisk) {
		t.Fatalf("root check error: %v", err)
	}
	f.roots.err = nil
	// SIDE and CANONICAL forms are accepted.
	side := good
	side.Kind, side.Events, side.OldHead = Side, false, good.NewHead
	record := f.intend(side)
	if _, err := f.b.Intend(f.token, f.finalityIntent()); !errors.Is(err, ErrPending) {
		t.Fatalf("second intent while one is unresolved: %v", err)
	}
	f.native(record)
	if decided := f.resolve(record.Sequence); decided.Delivery != Checkpoint {
		t.Fatalf("SIDE delivery %s", decided.Delivery)
	}
	f.intend(Intent{Kind: Canonical, OldHead: f.v.OldHead, NewHead: f.v.NewHead, EvidenceDigest: f.v.DecisionEvidence,
		Events: true})
}

// TestUnadmittedObservationHasNoDecision is the unit form of AC-CAP-07-01:
// before the EXEC_READY closing sync the observation is ADMITTED, and no
// decision or obligation can name it; recovery keeps the same obligation.
func TestUnadmittedObservationHasNoDecision(t *testing.T) {
	f := newFixture(t)
	f.live()
	obs, err := f.adm.Begin(f.token, f.v.Candidate)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	f.pins.roots[obs.Key], f.pins.digests[obs.Key] = f.v.Candidate.ParentRoot, f.v.PinDigest
	f.inputs.capsules[obs.Key] = f.v.CapsuleDigest
	if _, err := f.adm.Admit(f.token, obs.Key); err != nil {
		t.Fatalf("admit: %v", err)
	}
	f.inputs.consumed[obs.Key] = admission.Consumed{Observation: obs.Key, CapsuleDigest: f.v.CapsuleDigest,
		Count: f.v.ConsumedCount, Chain: f.v.ConsumedChain, Digest: f.v.ConsumedDigest}
	f.db.crashAt = f.db.ops + 2
	if _, err := f.adm.ExecReady(f.token, obs.Key, f.v.ExecDigest); !errors.Is(err, errCrash) {
		t.Fatalf("interrupted exec ready: %v", err)
	}
	f.reopen(f.db.powerLoss(0))
	list := f.openObservations()
	if len(list) != 1 || list[0].State != admission.Admitted {
		t.Fatalf("after power loss %v", list)
	}
	if undecided, err := f.b.Undecided(f.token); err != nil || len(undecided) != 0 {
		t.Fatalf("an ADMITTED observation is listed as undecided: %v %v", undecided, err)
	}
	in := f.importIntent(admission.ExecReadyRecord{Observation: obs.Key, Digest: f.v.ExecReadyDigest})
	if _, err := f.b.Intend(f.token, in); !errors.Is(err, ErrBinding) {
		t.Fatalf("intent over an ADMITTED observation: %v", err)
	}
	if len(f.decisions()) != 0 || len(f.obligations()) != 0 {
		t.Fatalf("an unclosed corpus produced a decision")
	}
	if ready, err := f.adm.ExecReady(f.token, obs.Key, f.v.ExecDigest); err != nil || ready.Digest != f.v.ExecReadyDigest {
		t.Fatalf("recovery of the same obligation: %+v %v", ready, err)
	}
}

// TestVolatileChildRootNeedsReplay is the unit form of AC-CAP-07-02: with
// EXEC_READY durable and the child root lost, no intent is written; after
// native-only replay makes the same root durable, the decision binds the
// unchanged EXEC_READY record.
func TestVolatileChildRootNeedsReplay(t *testing.T) {
	f := newFixture(t)
	f.live()
	ready := f.execReady(f.v.Candidate)
	in := f.importIntent(ready)
	f.reopen(f.db.powerLoss(0))
	readyKey := fmt.Sprintf("extractor-admission-v1/%s.%s/%016x/exec-ready", f.v.Producer, f.v.Run, 0)
	before := bytes.Clone(f.db.durable[readyKey])
	delete(f.roots.durable, f.v.ChildRoot)
	if _, err := f.b.Intend(f.token, in); !errors.Is(err, ErrChildRoot) {
		t.Fatalf("intent over a volatile child root: %v", err)
	}
	if undecided, _ := f.b.Undecided(f.token); len(undecided) != 1 || undecided[0].Key != ready.Observation {
		t.Fatalf("undecided %v", undecided)
	}
	if err := f.b.GoLive(f.token); !errors.Is(err, ErrPending) {
		t.Fatalf("go live with an undecided EXEC_READY: %v", err)
	}
	f.roots.durable[f.v.ChildRoot] = true
	intent := f.intend(in)
	if !reflect.DeepEqual(intent, f.v.ImportIntent) || !bytes.Equal(f.db.durable[readyKey], before) {
		t.Fatalf("replay changed the corpus binding or the decision identity")
	}
	f.native(intent)
	f.resolve(0)
	f.live()
}

// TestMissingMarkerIsNotADecision is the unit form of AC-CAP-07-03: a power
// loss inside the native mutation, before the marker, leaves the intent
// unresolved. Recovery completes exactly the saved transition.
func TestMissingMarkerIsNotADecision(t *testing.T) {
	f := newFixture(t)
	f.live()
	intent := f.intend(f.importIntent(f.execReady(f.v.Candidate)))
	// The native mutation writes canonical mappings in several steps; the
	// power loss keeps some of them but not the final batch with the marker.
	for i := range 3 {
		if err := f.db.Put([]byte(fmt.Sprintf("native-canonical-%d", i)), []byte("x")); err != nil {
			t.Fatalf("native write: %v", err)
		}
	}
	f.reopen(f.db.powerLoss(0b101))
	list := f.decisions()
	if len(list) != 1 || list[0].State != Intended || list[0].Marker {
		t.Fatalf("after power loss %+v", list)
	}
	if _, err := f.b.Resolve(f.token, 0); !errors.Is(err, ErrNoMarker) {
		t.Fatalf("resolve without marker: %v", err)
	}
	if len(f.obligations()) != 0 {
		t.Fatalf("an intent without marker produced an obligation")
	}
	if err := f.b.GoLive(f.token); !errors.Is(err, ErrPending) {
		t.Fatalf("go live with an unresolved intent: %v", err)
	}
	if _, err := f.b.Intend(f.token, f.finalityIntent()); !errors.Is(err, ErrPending) {
		t.Fatalf("new intent over an unresolved one: %v", err)
	}
	// Native recovery completes the saved transition and writes its marker.
	f.native(list[0].Intent)
	if decided := f.resolve(0); decided.Digest != f.v.ImportDecidedDigest {
		t.Fatalf("recovered decision %+v", decided)
	}
	if got := f.obligations(); len(got) != 1 || !reflect.DeepEqual(got[0].Intent, intent) {
		t.Fatalf("obligations %+v", got)
	}
	f.live()
}

// TestNativeRejectionIsExplicit is the unit form of AC-CAP-07-06: a native
// rejection of a durable intent, then a crash before reconciliation; the
// restart neither applies nor accepts the intent, and the rejection is
// recorded explicitly.
func TestNativeRejectionIsExplicit(t *testing.T) {
	f := newFixture(t)
	f.live()
	ready := f.execReady(f.v.Candidate)
	intent := f.intend(f.importIntent(ready))
	f.reopen(f.db.powerLoss(0))
	if list := f.decisions(); len(list) != 1 || list[0].State != Intended {
		t.Fatalf("after crash %+v", list)
	}
	if _, err := f.b.RejectIntent(f.token, 0, "x"); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "evidence digest") {
		t.Fatalf("malformed evidence: %v", err)
	}
	if _, err := f.b.RejectIntent(f.token, 5, f.v.RejectionEvidence); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown decision: %v", err)
	}
	// A power loss inside RejectIntent: the observation is closed only if
	// the rejection is durable, and a repeat completes it.
	probe := newFixture(t)
	probe.live()
	probe.intend(probe.importIntent(probe.execReady(probe.v.Candidate)))
	start := probe.db.ops
	if _, err := probe.b.RejectIntent(probe.token, 0, probe.v.RejectionEvidence); err != nil {
		t.Fatalf("probe reject: %v", err)
	}
	for crash := 1; crash <= probe.db.ops-start; crash++ {
		for _, keep := range []uint{0, 0b10, 0b110, ^uint(0)} {
			g := newFixture(t)
			g.live()
			g.intend(g.importIntent(g.execReady(g.v.Candidate)))
			g.db.crashAt = g.db.ops + crash
			if _, err := g.b.RejectIntent(g.token, 0, g.v.RejectionEvidence); !errors.Is(err, errCrash) {
				t.Fatalf("crash %d: %v", crash, err)
			}
			g.reopen(g.db.powerLoss(keep))
			list := g.decisions()
			if len(g.openObservations()) == 0 && len(list) != 0 {
				t.Fatalf("crash %d keep %b: observation closed but the intent is %+v", crash, keep, list)
			}
			if len(list) == 1 {
				if _, err := g.b.RejectIntent(g.token, 0, g.v.RejectionEvidence); err != nil {
					t.Fatalf("crash %d keep %b: repeated reject: %v", crash, keep, err)
				}
			}
			if len(g.openObservations()) != 0 || len(g.decisions()) != 0 || len(g.obligations()) != 0 {
				t.Fatalf("crash %d keep %b: rejection not completed", crash, keep)
			}
		}
	}
	rejected, err := f.b.RejectIntent(f.token, 0, f.v.RejectionEvidence)
	if err != nil || rejected.Digest != f.v.ImportRejectedDigest || rejected.IntentDigest != intent.Digest {
		t.Fatalf("reject intent %+v %v", rejected, err)
	}
	if got := f.admissionDecided(0); got != f.v.ImportRejectedDigest {
		t.Fatalf("observation closed with %s", got)
	}
	if len(f.obligations()) != 0 || len(f.decisions()) != 0 || f.hasMarker(0) || len(f.mirror.applied) != 0 {
		t.Fatalf("a rejected intent left an obligation, decision or marker")
	}
	if !slices.Equal(f.pins.released, []string{ready.Observation}) {
		t.Fatalf("released %v", f.pins.released)
	}
	if _, err := f.b.RejectIntent(f.token, 0, f.v.RejectionEvidence); !errors.Is(err, ErrUnknown) {
		t.Fatalf("repeated reject: %v", err)
	}
	f.reopen(f.db)
	f.live()

	// A marker means the native side accepted the transition.
	g := newFixture(t)
	g.live()
	accepted := g.intend(g.importIntent(g.execReady(g.v.Candidate)))
	g.native(accepted)
	if _, err := g.b.RejectIntent(g.token, 0, g.v.RejectionEvidence); !errors.Is(err, ErrInvariant) {
		t.Fatalf("reject with a marker: %v", err)
	}
	g.resolve(0)
	if _, err := g.b.RejectIntent(g.token, 0, g.v.RejectionEvidence); !errors.Is(err, ErrState) {
		t.Fatalf("reject of a decided decision: %v", err)
	}
}

// recoverAll is the recovery driver of the tests: it completes every saved
// transition (the native side writes the marker if it is absent), decides
// every undecided EXEC_READY observation, lets the mirror apply the
// obligations and removes the markers.
func (f *fixture) recoverAll() {
	f.t.Helper()
	for _, d := range f.decisions() {
		if d.State == Intended {
			if !d.Marker {
				f.native(d.Intent)
			}
			f.resolve(d.Sequence)
		}
	}
	undecided, err := f.b.Undecided(f.token)
	if err != nil {
		f.t.Fatalf("undecided: %v", err)
	}
	for _, obs := range undecided {
		record := f.intend(f.importIntent(*obs.ExecReady))
		f.native(record)
		f.resolve(record.Sequence)
	}
	f.mirror.apply(f.obligations())
	f.cleanup()
}

// TestPowerLossAtEveryStep: a power loss at every store operation from
// Intend through Resolve, the mirror and Cleanup, with four subsets of
// unsynced writes kept. Recovery always ends with one decision for the
// block, with the clean-run digest, applied once; no marker, pin or open
// observation is left.
func TestPowerLossAtEveryStep(t *testing.T) {
	run := func(f *fixture, ready admission.ExecReadyRecord) error {
		record, err := f.b.Intend(f.token, f.importIntent(ready))
		if err != nil {
			return err
		}
		value, _ := MarkerValue(record)
		if err := f.db.Put(MarkerKey(f.producer(), 0), value); err != nil {
			return err
		}
		if err := f.db.SyncKeyValue(); err != nil {
			return err
		}
		if _, err := f.b.Resolve(f.token, 0); err != nil {
			return err
		}
		obligations, err := f.b.Obligations(f.token)
		if err != nil {
			return err
		}
		f.mirror.apply(obligations)
		return f.b.Cleanup(f.token)
	}
	probe := newFixture(t)
	probe.live()
	ready := probe.execReady(probe.v.Candidate)
	start := probe.db.ops
	if err := run(probe, ready); err != nil {
		t.Fatalf("clean run: %v", err)
	}
	total := probe.db.ops - start
	for crash := 1; crash <= total; crash++ {
		for _, keep := range []uint{0, 1, 0b0110, ^uint(0)} {
			f := newFixture(t)
			f.live()
			ready := f.execReady(f.v.Candidate)
			f.db.crashAt = f.db.ops + crash
			if err := run(f, ready); !errors.Is(err, errCrash) {
				t.Fatalf("crash %d: %v", crash, err)
			}
			f.reopen(f.db.powerLoss(keep))
			f.recoverAll()
			if len(f.mirror.applied) != 1 || f.mirror.applied[0] != f.v.ImportDecidedDigest || f.mirror.applies[0] != 1 {
				t.Fatalf("crash %d keep %b: applied %v", crash, keep, f.mirror.applied)
			}
			if len(f.decisions()) != 0 || len(f.openObservations()) != 0 || f.hasMarker(0) || f.hasMarker(1) ||
				len(f.pins.roots) != 0 {
				t.Fatalf("crash %d keep %b: left decisions %v, observations %v, pins %v", crash, keep,
					f.decisions(), f.openObservations(), f.pins.roots)
			}
			f.reopen(f.db)
			f.live()
		}
	}
}

// TestOneObligationAcrossRecovery: after the NATIVE_DECIDED sync, a crash
// before the observation is closed, and again before the marker cleanup
// sync, still yields the one obligation; once applied, it never returns.
func TestOneObligationAcrossRecovery(t *testing.T) {
	f := newFixture(t)
	f.live()
	ready := f.execReady(f.v.Candidate)
	intent := f.intend(f.importIntent(ready))
	f.native(intent)
	f.db.crashAt = f.db.ops + 3
	if _, err := f.b.Resolve(f.token, 0); !errors.Is(err, errCrash) {
		t.Fatalf("crash in the close: %v", err)
	}
	f.reopen(f.db.powerLoss(0))
	if len(f.openObservations()) != 0 {
		t.Fatalf("Open did not finish the close")
	}
	first := f.obligations()
	f.reopen(f.db)
	second := f.obligations()
	if len(first) != 1 || !reflect.DeepEqual(first, second) || first[0].Decided.Digest != f.v.ImportDecidedDigest {
		t.Fatalf("obligations %+v then %+v", first, second)
	}
	f.mirror.apply(first)
	f.db.crashAt = f.db.ops + 2
	if err := f.b.Cleanup(f.token); !errors.Is(err, errCrash) {
		t.Fatalf("crash in the cleanup: %v", err)
	}
	f.reopen(f.db.powerLoss(0))
	if !f.hasMarker(0) || len(f.obligations()) != 0 || len(f.decisions()) != 1 {
		t.Fatalf("after the cleanup crash: marker %v, obligations %v", f.hasMarker(0), f.obligations())
	}
	f.cleanup()
	if f.hasMarker(0) || len(f.decisions()) != 0 {
		t.Fatalf("repeated cleanup left the marker")
	}
}

// TestBlockObservedTwice: two EXEC_READY observations of the same block get
// one decision; the earlier observation is authoritative, and resolving it
// closes both.
func TestBlockObservedTwice(t *testing.T) {
	f := newFixture(t)
	f.live()
	first := f.execReady(f.v.Candidate)
	second := f.execReady(f.v.Candidate)
	other := f.execReady(otherCandidate(f))
	if err := f.b.GoLive(f.token); !errors.Is(err, ErrPending) {
		t.Fatalf("go live with undecided observations: %v", err)
	}
	in := f.importIntent(second)
	if _, err := f.b.Intend(f.token, in); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("intent over the later observation: %v", err)
	}
	both := f.importIntent(first)
	both.Members = append(both.Members, f.importIntent(second).Members...)
	if _, err := f.b.Intend(f.token, both); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("intent naming both observations: %v", err)
	}
	intent := f.intend(f.importIntent(first))
	if undecided, _ := f.b.Undecided(f.token); len(undecided) != 2 {
		t.Fatalf("undecided while the intent is open: %v", undecided)
	}
	f.native(intent)
	f.resolve(0)
	list := f.openObservations()
	if len(list) != 1 || list[0].Key != other.Observation {
		t.Fatalf("open observations %v", list)
	}
	if !slices.Equal(f.pins.released, []string{first.Observation, second.Observation}) {
		t.Fatalf("released %v", f.pins.released)
	}
	if f.admissionDecided(1) != f.v.ImportDecidedDigest {
		t.Fatalf("the duplicate was not closed by the same decision")
	}
	if len(f.obligations()) != 1 {
		t.Fatalf("obligations %v", f.obligations())
	}
}

func TestOpenRejectsDamage(t *testing.T) {
	type damage struct {
		name   string
		change func(f *fixture)
		want   error
	}
	decided := func(f *fixture) {
		f.native(f.intend(f.importIntent(f.execReady(f.v.Candidate))))
		f.resolve(0)
	}
	put := func(f *fixture, key []byte, value string) { f.db.durable[string(key)] = []byte(value) }
	cases := []damage{
		{"missing intent", func(f *fixture) { put(f, f.b.key("counter"), `{"next":1}`) }, ErrCorrupt},
		{"intent format", func(f *fixture) {
			put(f, f.b.key("counter"), `{"next":1}`)
			put(f, f.b.recordKey(0, "intent"), `{"x":1}`)
		}, ErrCorrupt},
		{"intent with an extra field", func(f *fixture) {
			f.intend(f.importIntent(f.execReady(f.v.Candidate)))
			value := strings.Replace(string(f.db.durable[string(f.b.recordKey(0, "intent"))]), "{", `{"extra":1,`, 1)
			put(f, f.b.recordKey(0, "intent"), value)
		}, ErrCorrupt},
		{"intent with trailing data", func(f *fixture) {
			f.intend(f.importIntent(f.execReady(f.v.Candidate)))
			put(f, f.b.recordKey(0, "intent"), string(f.db.durable[string(f.b.recordKey(0, "intent"))])+" {}")
		}, ErrCorrupt},
		{"decided digest field", func(f *fixture) {
			decided(f)
			put(f, f.b.recordKey(0, "decided"), strings.Replace(
				string(f.db.durable[string(f.b.recordKey(0, "decided"))]), f.v.ImportDecidedDigest,
				f.v.FinalityDecidedDigest, 1))
		}, ErrCorrupt},
		{"intent with a malformed head", func(f *fixture) {
			f.intend(f.importIntent(f.execReady(f.v.Candidate)))
			put(f, f.b.recordKey(0, "intent"), strings.Replace(string(f.db.durable[string(f.b.recordKey(0, "intent"))]),
				f.v.OldHead.Hash, "AB", 1))
		}, ErrCorrupt},
		{"intent of another producer", func(f *fixture) {
			f.intend(f.importIntent(f.execReady(f.v.Candidate)))
			var record IntentRecord
			_ = strictDecode(f.db.durable[string(f.b.recordKey(0, "intent"))], &record)
			record.Producer = "other.run"
			record.Digest, _ = IntentDigest(record)
			value, _ := MarkerValue(record)
			put(f, f.b.recordKey(0, "intent"), string(value))
		}, ErrCorrupt},
		{"intent digest", func(f *fixture) {
			f.intend(f.importIntent(f.execReady(f.v.Candidate)))
			value := strings.Replace(string(f.db.durable[string(f.b.recordKey(0, "intent"))]), `"events":true`,
				`"events":false`, 1)
			put(f, f.b.recordKey(0, "intent"), value)
		}, ErrCorrupt},
		{"low-water above counter", func(f *fixture) { put(f, f.b.key("lowwater"), `{"sequence":1}`) }, ErrCorrupt},
		{"purged above low-water", func(f *fixture) { put(f, f.b.key("purged"), `{"sequence":1}`) }, ErrCorrupt},
		{"unresolved intent below the counter", func(f *fixture) {
			f.intend(f.importIntent(f.execReady(f.v.Candidate)))
			intent := strings.Replace(string(f.db.durable[string(f.b.recordKey(0, "intent"))]), `"sequence":0`,
				`"sequence":1`, 1)
			record := IntentRecord{}
			_ = strictDecode([]byte(intent), &record)
			record.Digest, _ = IntentDigest(record)
			value, _ := MarkerValue(record)
			put(f, f.b.recordKey(1, "intent"), string(value))
			put(f, f.b.key("counter"), `{"next":2}`)
		}, ErrCorrupt},
		{"marker bytes", func(f *fixture) {
			f.intend(f.importIntent(f.execReady(f.v.Candidate)))
			put(f, MarkerKey(f.producer(), 0), `{}`)
		}, ErrInvariant},
		{"decided format", func(f *fixture) {
			decided(f)
			put(f, f.b.recordKey(0, "decided"), `{"x":1}`)
		}, ErrCorrupt},
		{"decided digest", func(f *fixture) {
			decided(f)
			put(f, f.b.recordKey(0, "decided"), strings.Replace(
				string(f.db.durable[string(f.b.recordKey(0, "decided"))]), "BATCH", "CHECKPOINT", 1))
		}, ErrCorrupt},
		{"decided and rejected", func(f *fixture) {
			decided(f)
			delete(f.db.durable, string(MarkerKey(f.producer(), 0)))
			f.mirror.applied[0] = f.v.ImportDecidedDigest
			digest, _ := RejectedDigest(f.v.ImportIntent.Digest, f.v.RejectionEvidence)
			put(f, f.b.recordKey(0, "rejected"), fmt.Sprintf(
				`{"sequence":0,"intent_digest":%q,"evidence_digest":%q,"digest":%q}`,
				f.v.ImportIntent.Digest, f.v.RejectionEvidence, digest))
		}, ErrCorrupt},
		{"rejected digest", func(f *fixture) {
			f.intend(f.importIntent(f.execReady(f.v.Candidate)))
			put(f, f.b.recordKey(0, "rejected"), fmt.Sprintf(
				`{"sequence":0,"intent_digest":%q,"evidence_digest":%q,"digest":%q}`,
				f.v.ImportIntent.Digest, f.v.RejectionEvidence, f.v.ImportIntent.Digest))
		}, ErrCorrupt},
		{"rejected with marker", func(f *fixture) {
			intent := f.intend(f.importIntent(f.execReady(f.v.Candidate)))
			if _, err := f.b.RejectIntent(f.token, 0, f.v.RejectionEvidence); err != nil {
				t.Fatalf("reject: %v", err)
			}
			put(f, f.b.key("lowwater"), `{"sequence":0}`)
			put(f, f.b.key("purged"), `{"sequence":0}`)
			put(f, f.b.recordKey(0, "intent"), string(f.v.ImportMarker))
			put(f, f.b.recordKey(0, "rejected"), fmt.Sprintf(
				`{"sequence":0,"intent_digest":%q,"evidence_digest":%q,"digest":%q}`,
				intent.Digest, f.v.RejectionEvidence, f.v.ImportRejectedDigest))
			put(f, MarkerKey(f.producer(), 0), f.v.ImportMarker)
		}, ErrInvariant},
		{"marker removed before the mirror applied it", func(f *fixture) {
			decided(f)
			delete(f.db.durable, string(MarkerKey(f.producer(), 0)))
		}, ErrInvariant},
		{"other native store", func(f *fixture) {
			f.intend(f.importIntent(f.execReady(f.v.Candidate)))
			f.v.StoreID = "other-store"
		}, ErrCorrupt},
	}
	for _, tc := range cases {
		f := newFixture(t)
		f.live()
		tc.change(f)
		if err := f.tryReopen(f.db); !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	// A malformed intent names the cause.
	fm := newFixture(t)
	fm.live()
	fm.intend(fm.importIntent(fm.execReady(fm.v.Candidate)))
	put(fm, fm.b.recordKey(0, "intent"), strings.Replace(string(fm.db.durable[string(fm.b.recordKey(0, "intent"))]),
		fm.v.OldHead.Hash, "AB", 1))
	if err := fm.tryReopen(fm.db); err == nil || !strings.Contains(err.Error(), `digest "AB"`) {
		t.Fatalf("malformed intent: %v", err)
	}
	// A missing intent is named as such.
	f0 := newFixture(t)
	put(f0, f0.b.key("counter"), `{"next":1}`)
	if err := f0.tryReopen(f0.db); err == nil || !strings.Contains(err.Error(), "missing intent 0") {
		t.Fatalf("missing intent: %v", err)
	}
	// Mirror and store read errors pass through Open.
	f := newFixture(t)
	f.live()
	decided(f)
	delete(f.db.durable, string(MarkerKey(f.producer(), 0)))
	f.mirror.err = errDisk
	if err := f.tryReopen(f.db); !errors.Is(err, errDisk) {
		t.Fatalf("mirror error: %v", err)
	}
	for _, suffix := range []string{"counter", "lowwater", "purged", "/intent", "/decided", "/rejected",
		"/0000000000000000"} {
		f := newFixture(t)
		f.live()
		f.intend(f.importIntent(f.execReady(f.v.Candidate)))
		put(f, MarkerKey(f.producer(), 0), f.v.ImportMarker)
		put(f, f.b.recordKey(0, "decided"), "{}")
		put(f, f.b.recordKey(0, "rejected"), "{}")
		put(f, f.b.key("purged"), `{"sequence":0}`)
		put(f, f.b.key("lowwater"), `{"sequence":0}`)
		f.db.getErrFor = suffix
		if err := f.tryReopen(f.db); !errors.Is(err, errDisk) {
			t.Fatalf("read error of %s: %v", suffix, err)
		}
	}
}

func TestIntendRefusesRecordsBeyondTheCounter(t *testing.T) {
	for _, key := range []func(f *fixture) []byte{
		func(f *fixture) []byte { return f.b.recordKey(0, "decided") },
		func(f *fixture) []byte { return f.b.recordKey(0, "rejected") },
		func(f *fixture) []byte { return MarkerKey(f.producer(), 0) },
	} {
		f := newFixture(t)
		f.live()
		in := f.importIntent(f.execReady(f.v.Candidate))
		f.db.durable[string(key(f))] = []byte("{}")
		if _, err := f.b.Intend(f.token, in); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("%s beyond the counter: %v", key(f), err)
		}
		if _, err := f.b.Decisions(f.token); !errors.Is(err, ErrFailed) {
			t.Fatalf("bridge did not stop: %v", err)
		}
	}
	// An orphan intent of an interrupted Intend is overwritten.
	f := newFixture(t)
	f.live()
	in := f.importIntent(f.execReady(f.v.Candidate))
	f.db.durable[string(f.b.recordKey(0, "intent"))] = []byte("orphan")
	if record := f.intend(in); record.Sequence != 0 {
		t.Fatalf("orphan intent was not overwritten")
	}
}

func TestOneBridgePerProducer(t *testing.T) {
	f := newFixture(t)
	f.live()
	stale, err := Open(f.token, f.db, f.adm, f.v.StoreID, f.roots, f.mirror)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	f.intend(f.finalityIntent())
	writes := f.db.writes
	if _, err := stale.Intend(f.token, f.finalityIntent()); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale bridge: %v", err)
	}
	if f.db.writes != writes {
		t.Fatalf("stale bridge wrote")
	}
	if _, err := stale.Obligations(f.token); !errors.Is(err, ErrFailed) {
		t.Fatalf("stale bridge did not stop: %v", err)
	}
}

func TestArgumentsGuardAndStoreErrors(t *testing.T) {
	f := newFixture(t)
	if _, err := Open(f.token, f.db, nil, f.v.StoreID, f.roots, f.mirror); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil admission: %v", err)
	}
	if _, err := Open(nil, f.db, f.adm, f.v.StoreID, f.roots, f.mirror); !errors.Is(err, admission.ErrGuard) {
		t.Fatalf("nil token: %v", err)
	}
	for name, open := range map[string]func() error{
		"store":    func() error { _, err := Open(f.token, nil, f.adm, f.v.StoreID, f.roots, f.mirror); return err },
		"roots":    func() error { _, err := Open(f.token, f.db, f.adm, f.v.StoreID, nil, f.mirror); return err },
		"mirror":   func() error { _, err := Open(f.token, f.db, f.adm, f.v.StoreID, f.roots, nil); return err },
		"store id": func() error { _, err := Open(f.token, f.db, f.adm, "bad id", f.roots, f.mirror); return err },
	} {
		if err := open(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	f.db.syncErr = errDisk
	if _, err := Open(f.token, f.db, f.adm, f.v.StoreID, f.roots, f.mirror); !errors.Is(err, errDisk) {
		t.Fatalf("sync error: %v", err)
	}
	// The guard is checked before the store is touched.
	if _, err := Open(admission.NewGuard().Acquire(), f.db, f.adm, f.v.StoreID, f.roots, f.mirror); !errors.Is(err,
		admission.ErrGuard) {
		t.Fatalf("foreign token: %v", err)
	}
	f.db.syncErr = nil
	other := admission.NewGuard().Acquire()
	for name, call := range map[string]func() error{
		"decisions":   func() error { _, err := f.b.Decisions(other); return err },
		"undecided":   func() error { _, err := f.b.Undecided(other); return err },
		"go live":     func() error { return f.b.GoLive(other) },
		"intend":      func() error { _, err := f.b.Intend(other, f.finalityIntent()); return err },
		"resolve":     func() error { _, err := f.b.Resolve(other, 0); return err },
		"reject":      func() error { _, err := f.b.RejectIntent(other, 0, f.v.RejectionEvidence); return err },
		"obligations": func() error { _, err := f.b.Obligations(other); return err },
		"cleanup":     func() error { return f.b.Cleanup(other) },
	} {
		if err := call(); !errors.Is(err, admission.ErrGuard) {
			t.Fatalf("%s with a foreign token: %v", name, err)
		}
	}
	if _, err := f.b.Resolve(f.token, 3); !errors.Is(err, ErrUnknown) {
		t.Fatalf("resolve unknown: %v", err)
	}

	// A store error in the first write or the first sync of each writing
	// step stops the bridge.
	steps := map[string]struct {
		setup func(f *fixture)
		run   func(f *fixture) error
	}{
		"intend": {func(f *fixture) {}, func(f *fixture) error {
			_, err := f.b.Intend(f.token, f.finalityIntent())
			return err
		}},
		"resolve": {func(f *fixture) { f.native(f.intend(f.finalityIntent())) }, func(f *fixture) error {
			_, err := f.b.Resolve(f.token, 0)
			return err
		}},
		"reject": {func(f *fixture) { f.intend(f.finalityIntent()) }, func(f *fixture) error {
			_, err := f.b.RejectIntent(f.token, 0, f.v.RejectionEvidence)
			return err
		}},
		"cleanup": {func(f *fixture) { f.native(f.intend(f.finalityIntent())); f.resolve(0) },
			func(f *fixture) error { return f.b.Cleanup(f.token) }},
	}
	for name, step := range steps {
		for crash := 1; crash <= 2; crash++ {
			g := newFixture(t)
			g.live()
			step.setup(g)
			g.db.crashAt = g.db.ops + crash
			if err := step.run(g); !errors.Is(err, errCrash) {
				t.Fatalf("%s crash %d: %v", name, crash, err)
			}
			if _, err := g.b.Decisions(g.token); !errors.Is(err, ErrFailed) {
				t.Fatalf("%s crash %d: bridge did not stop: %v", name, crash, err)
			}
		}
	}
	// Mirror errors pass through Obligations and Cleanup.
	g := newFixture(t)
	g.live()
	g.native(g.intend(g.importIntent(g.execReady(g.v.Candidate))))
	g.resolve(0)
	g.mirror.err = errDisk
	if _, err := g.b.Obligations(g.token); !errors.Is(err, errDisk) {
		t.Fatalf("obligations mirror error: %v", err)
	}
	if err := g.b.Cleanup(g.token); !errors.Is(err, errDisk) {
		t.Fatalf("cleanup mirror error: %v", err)
	}
	// A marker read error stops Resolve and RejectIntent.
	for _, reject := range []bool{false, true} {
		h := newFixture(t)
		h.live()
		h.intend(h.finalityIntent())
		h.db.hasErrFor = "/0000000000000000"
		var err error
		if reject {
			_, err = h.b.RejectIntent(h.token, 0, h.v.RejectionEvidence)
		} else {
			_, err = h.b.Resolve(h.token, 0)
		}
		if !errors.Is(err, errDisk) {
			t.Fatalf("marker read error (reject %v): %v", reject, err)
		}
	}
}

func TestDecisionsReturnCopies(t *testing.T) {
	f := newFixture(t)
	f.live()
	intent := f.intend(f.importIntent(f.execReady(f.v.Candidate)))
	f.native(intent)
	f.resolve(0)
	list := f.decisions()
	list[0].Intent.Members[0].Observation = "changed"
	list[0].Decided.Digest = "changed"
	obligations := f.obligations()
	obligations[0].Intent.Members[0].Observation = "changed"
	intent.Members[0].Observation = "changed"
	again := f.decisions()
	if again[0].Intent.Members[0].Observation != f.v.ImportIntent.Members[0].Observation ||
		again[0].Decided.Digest != f.v.ImportDecidedDigest {
		t.Fatalf("Intend, Decisions or Obligations returned shared state")
	}
}

// TestPruneInBatches: many finished decisions are deleted in batches, and a
// power loss inside a batch only repeats it.
func TestPruneInBatches(t *testing.T) {
	f := newFixture(t)
	f.live()
	total := pruneBatch + 5
	for seq := range total {
		f.native(f.intend(f.finalityIntent()))
		f.resolve(uint64(seq))
	}
	// One Cleanup removes every marker and prunes in two batches.
	base := f.db.powerLoss(0)
	clean := *f
	clean.reopen(base.powerLoss(0))
	clean.cleanup()
	if clean.db.maxPending > max(total, 3*pruneBatch+1) {
		t.Fatalf("a prune batch held %d unsynced writes", clean.db.maxPending)
	}
	recordsLeft := func(db *memStore) int {
		count := 0
		for key := range db.durable {
			if strings.HasPrefix(key, keyPrefix) && (strings.HasSuffix(key, "/intent") ||
				strings.HasSuffix(key, "/decided")) {
				count++
			}
		}
		return count
	}
	for _, crash := range []int{total + 2, total + 3 + 3*pruneBatch, total + 3 + 3*pruneBatch + 1} {
		g := *f
		g.reopen(base.powerLoss(0))
		g.db.crashAt = g.db.ops + crash
		if err := g.b.Cleanup(g.token); !errors.Is(err, errCrash) {
			t.Fatalf("crash %d: %v", crash, err)
		}
		g.reopen(g.db.powerLoss(0))
		g.cleanup()
		if left := recordsLeft(g.db); left != 0 {
			t.Fatalf("crash %d: %d records left", crash, left)
		}
		if got := string(g.db.durable[string(g.b.key("purged"))]); got != fmt.Sprintf(`{"sequence":%d}`, total) {
			t.Fatalf("crash %d: purged mark %s", crash, got)
		}
	}
}

// TestCleanupFinishedAfterItsSync: once the marker deletion is synced, a
// power loss before the low-water mark moves leaves a done decision, which
// Open does not list again.
func TestCleanupFinishedAfterItsSync(t *testing.T) {
	f := newFixture(t)
	f.live()
	f.native(f.intend(f.importIntent(f.execReady(f.v.Candidate))))
	f.resolve(0)
	f.mirror.apply(f.obligations())
	f.db.crashAt = f.db.ops + 3
	if err := f.b.Cleanup(f.token); !errors.Is(err, errCrash) {
		t.Fatalf("crash after the marker sync: %v", err)
	}
	f.reopen(f.db.powerLoss(0))
	if len(f.decisions()) != 0 || f.hasMarker(0) ||
		string(f.db.durable[string(f.b.key("lowwater"))]) != `{"sequence":1}` {
		t.Fatalf("done decision listed again: %+v", f.decisions())
	}
}

// TestUnfinishedObservationOfTheBlock: a member block with an open
// observation that is not EXEC_READY gets no intent; one that appears
// after the intent stops Resolve with a source invariant error.
func TestUnfinishedObservationOfTheBlock(t *testing.T) {
	f := newFixture(t)
	f.live()
	ready := f.execReady(f.v.Candidate)
	if _, err := f.adm.Begin(f.token, f.v.Candidate); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := f.b.Intend(f.token, f.importIntent(ready)); !errors.Is(err, ErrPending) {
		t.Fatalf("intent with an unfinished observation of the block: %v", err)
	}

	g := newFixture(t)
	g.live()
	ready = g.execReady(g.v.Candidate)
	intent := g.intend(g.importIntent(ready))
	if _, err := g.adm.Begin(g.token, g.v.Candidate); err != nil {
		t.Fatalf("begin: %v", err)
	}
	g.native(intent)
	if _, err := g.b.Resolve(g.token, 0); !errors.Is(err, ErrInvariant) {
		t.Fatalf("resolve with an unfinished observation of the block: %v", err)
	}
}

// TestPruneKeepsItsOrder: a power loss in the prune keeps any subset of its
// unsynced writes; the purged mark never survives without the deletions it
// covers.
func TestPruneKeepsItsOrder(t *testing.T) {
	setup := func() *fixture {
		f := newFixture(t)
		f.live()
		for seq := range 2 {
			f.native(f.intend(f.finalityIntent()))
			f.resolve(uint64(seq))
		}
		return f
	}
	probe := setup()
	start := probe.db.ops
	probe.cleanup()
	total := probe.db.ops - start
	for crash := 1; crash <= total; crash++ {
		for keep := uint(0); keep < 1<<7; keep += 0b11 {
			f := setup()
			f.db.crashAt = f.db.ops + crash
			if err := f.b.Cleanup(f.token); !errors.Is(err, errCrash) {
				t.Fatalf("crash %d: %v", crash, err)
			}
			db := f.db.powerLoss(keep)
			var purged purgedRecord
			if _, err := readRecord(db, f.b.key("purged"), &purged); err != nil {
				t.Fatalf("crash %d keep %b: %v", crash, keep, err)
			}
			for seq := range purged.Sequence {
				for _, kind := range recordKinds {
					if _, ok := db.durable[string(f.b.recordKey(seq, kind))]; ok {
						t.Fatalf("crash %d keep %b: purged mark %d with record %d/%s", crash, keep,
							purged.Sequence, seq, kind)
					}
				}
			}
		}
	}
}

// TestSideRetainsItsMembers: a SIDE decision keeps its observations
// EXEC_READY with their pins, so the known side block can become canonical
// later without a new execution; the later decision closes them.
func TestSideRetainsItsMembers(t *testing.T) {
	f := newFixture(t)
	f.live()
	ready := f.execReady(f.v.Candidate)
	side := f.importIntent(ready)
	side.Kind, side.Events, side.OldHead = Side, false, side.NewHead
	record := f.intend(side)
	f.native(record)
	if decided := f.resolve(0); decided.Delivery != Checkpoint {
		t.Fatalf("SIDE delivery %s", decided.Delivery)
	}
	f.cleanup()
	writes := f.db.writes
	f.cleanup()
	if f.db.writes != writes {
		t.Fatalf("a repeated cleanup of a retained SIDE decision wrote %d times", f.db.writes-writes)
	}
	list := f.openObservations()
	if len(list) != 1 || list[0].Key != ready.Observation || len(f.pins.released) != 0 {
		t.Fatalf("SIDE closed its member: %v, released %v", list, f.pins.released)
	}
	if f.hasMarker(0) || len(f.obligations()) != 0 {
		t.Fatalf("SIDE kept its marker or has an obligation")
	}
	for _, reopen := range []bool{false, true} {
		if reopen {
			f.reopen(f.db)
		}
		decisions := f.decisions()
		if len(decisions) != 1 || decisions[0].State != Decided || decisions[0].Marker {
			t.Fatalf("reopen %v: retained SIDE decision %+v", reopen, decisions)
		}
		if undecided, _ := f.b.Undecided(f.token); len(undecided) != 0 {
			t.Fatalf("reopen %v: a retained member is undecided", reopen)
		}
		f.live()
	}
	// The side block becomes canonical without a new execution.
	canonical := f.importIntent(ready)
	canonical.Kind = Canonical
	record = f.intend(canonical)
	f.native(record)
	closing := f.resolve(1)
	if len(f.openObservations()) != 0 || !slices.Equal(f.pins.released, []string{ready.Observation}) {
		t.Fatalf("the canonical decision did not close the member")
	}
	if got := f.admissionDecided(0); got != closing.Digest {
		t.Fatalf("member closed with %s, want %s", got, closing.Digest)
	}
	f.mirror.apply(f.obligations())
	f.cleanup()
	if len(f.decisions()) != 0 || string(f.db.durable[string(f.b.key("lowwater"))]) != `{"sequence":2}` {
		t.Fatalf("decisions left %+v", f.decisions())
	}
}

// TestRejectedIntentKeepsRetainedSideBlocks: a canonical switch to a
// retained side block that the native side refuses leaves the block
// retained by its SIDE decision.
func TestRejectedIntentKeepsRetainedSideBlocks(t *testing.T) {
	f := newFixture(t)
	f.live()
	ready := f.execReady(f.v.Candidate)
	side := f.importIntent(ready)
	side.Kind, side.Events, side.OldHead = Side, false, side.NewHead
	f.native(f.intend(side))
	f.resolve(0)
	f.cleanup()
	canonical := f.importIntent(ready)
	canonical.Kind = Canonical
	f.intend(canonical)
	if _, err := f.b.RejectIntent(f.token, 1, f.v.RejectionEvidence); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if list := f.openObservations(); len(list) != 1 || len(f.pins.released) != 0 {
		t.Fatalf("the rejection closed a retained side block: %v, released %v", list, f.pins.released)
	}
	f.reopen(f.db)
	if decisions := f.decisions(); len(decisions) != 1 || decisions[0].Intent.Kind != Side {
		t.Fatalf("the SIDE decision no longer retains its member: %+v", decisions)
	}
	if list := f.openObservations(); len(list) != 1 || len(f.pins.released) != 0 {
		t.Fatalf("the reopen closed a retained side block: %v, released %v", list, f.pins.released)
	}
	f.live()
}

// TestSidePowerLoss: a power loss at every store operation of a SIDE
// decision, from Intend through Resolve and Cleanup, with four subsets of
// unsynced writes kept; after recovery the member is EXEC_READY with its
// pin and a SIDE decision retains it.
func TestSidePowerLoss(t *testing.T) {
	sideIntent := func(f *fixture, ready admission.ExecReadyRecord) Intent {
		in := f.importIntent(ready)
		in.Kind, in.Events, in.OldHead = Side, false, in.NewHead
		return in
	}
	run := func(f *fixture, ready admission.ExecReadyRecord) error {
		record, err := f.b.Intend(f.token, sideIntent(f, ready))
		if err != nil {
			return err
		}
		value, _ := MarkerValue(record)
		if err := f.db.Put(MarkerKey(f.producer(), 0), value); err != nil {
			return err
		}
		if err := f.db.SyncKeyValue(); err != nil {
			return err
		}
		if _, err := f.b.Resolve(f.token, 0); err != nil {
			return err
		}
		return f.b.Cleanup(f.token)
	}
	probe := newFixture(t)
	probe.live()
	ready := probe.execReady(probe.v.Candidate)
	start := probe.db.ops
	if err := run(probe, ready); err != nil {
		t.Fatalf("clean run: %v", err)
	}
	total := probe.db.ops - start
	for crash := 1; crash <= total; crash++ {
		for _, keep := range []uint{0, 1, 0b0110, ^uint(0)} {
			f := newFixture(t)
			f.live()
			ready := f.execReady(f.v.Candidate)
			f.db.crashAt = f.db.ops + crash
			if err := run(f, ready); !errors.Is(err, errCrash) {
				t.Fatalf("crash %d: %v", crash, err)
			}
			f.reopen(f.db.powerLoss(keep))
			for _, d := range f.decisions() {
				if d.State == Intended {
					if !d.Marker {
						f.native(d.Intent)
					}
					f.resolve(d.Sequence)
				}
			}
			if undecided, _ := f.b.Undecided(f.token); len(undecided) != 0 {
				f.native(f.intend(sideIntent(f, ready)))
				f.resolve(0)
			}
			f.cleanup()
			f.reopen(f.db)
			list := f.decisions()
			if len(list) != 1 || list[0].Intent.Kind != Side || list[0].Marker || len(f.openObservations()) != 1 ||
				len(f.pins.released) != 0 {
				t.Fatalf("crash %d keep %b: decisions %+v, observations %v, released %v", crash, keep, list,
					f.openObservations(), f.pins.released)
			}
			f.live()
		}
	}
}

// TestRejectedIntentClosesBlocksOfDecidedImports: only SIDE decisions retain
// blocks. A new observation of a block whose IMPORT decision still has its
// marker is closed when its own intent is rejected.
func TestRejectedIntentClosesBlocksOfDecidedImports(t *testing.T) {
	f := newFixture(t)
	f.live()
	f.native(f.intend(f.importIntent(f.execReady(f.v.Candidate))))
	f.resolve(0)
	again := f.execReady(f.v.Candidate)
	canonical := f.importIntent(again)
	canonical.Kind, canonical.Events = Canonical, false
	f.intend(canonical)
	if _, err := f.b.RejectIntent(f.token, 1, f.v.RejectionEvidence); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if list := f.openObservations(); len(list) != 0 {
		t.Fatalf("the rejected observation stayed open: %v", list)
	}
}
