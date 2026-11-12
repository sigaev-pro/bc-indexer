package lifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
)

// vectorFile holds the independent expected values. `python3
// test/harness/lifecycle.py vectors` computes them from the README formulas;
// test/harness/test_lifecycle.py checks that the file matches.
type vectorFile struct {
	StoreID         string        `json:"store_id"`
	Producer        string        `json:"producer"`
	Run             string        `json:"run"`
	Head            decision.Head `json:"head"`
	Root            string        `json:"root"`
	BoundaryKey     string        `json:"boundary_key"`
	BoundaryDigest  string        `json:"boundary_digest"`
	FCUCaller       string        `json:"fcu_caller"`
	OldFinalized    decision.Head `json:"old_finalized"`
	NewFinalized    decision.Head `json:"new_finalized"`
	FinalizedDigest string        `json:"finalized_digest"`
	FirstSafeDigest string        `json:"first_safe_digest"`
	Genesis         string        `json:"genesis"`
	Source          decision.Head `json:"source"`
	Target          decision.Head `json:"target"`
	AttestedDigest  string        `json:"attested_digest"`
	FallbackDigest  string        `json:"fallback_digest"`
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
)

type write struct {
	key     string
	value   []byte
	deleted bool
}

// memStore models the native key-value store. Put and Delete stay pending
// until SyncKeyValue makes them durable; powerLoss drops every pending
// write. crashAt makes the mutating operation with that index (counted from
// 1) fail without effect.
type memStore struct {
	durable map[string][]byte
	pending []write
	ops     int
	crashAt int
	hasErr  error
	getErr  error
	syncErr error
	syncs   int
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
	if err := s.step(); err != nil {
		return err
	}
	s.pending = append(s.pending, write{key: string(key), value: bytes.Clone(value)})
	return nil
}

func (s *memStore) Delete(key []byte) error {
	if err := s.step(); err != nil {
		return err
	}
	s.pending = append(s.pending, write{key: string(key), deleted: true})
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
		if w.deleted {
			delete(s.durable, w.key)
		} else {
			s.durable[w.key] = w.value
		}
	}
	s.pending = nil
	s.syncs++
	return nil
}

// powerLoss drops every pending write and returns the reopened store.
func (s *memStore) powerLoss() *memStore { return &memStore{durable: maps.Clone(s.durable)} }

// noPins and noInputs fail every call: the tests here admit no observation.
type noPins struct{}

func (noPins) Verify(string, string) (string, error) { return "", errors.New("no pins") }
func (noPins) Release(string) error                  { return errors.New("no pins") }

type noInputs struct{}

func (noInputs) CapsuleDigest(string) (string, error) { return "", errors.New("no inputs") }
func (noInputs) Consumed(string) (admission.Consumed, error) {
	return admission.Consumed{}, errors.New("no inputs")
}

type allRoots struct{}

func (allRoots) Durable(string) (bool, error) { return true, nil }

// someRoots reports the roots it holds as durable, or fails with err.
type someRoots struct {
	durable map[string]bool
	err     error
}

func (r someRoots) Durable(root string) (bool, error) { return r.durable[root], r.err }

type noMirror struct{}

func (noMirror) Applied(uint64, string) (bool, error) { return false, nil }

func caller(client, name string, route Route, trigger Trigger, effects []Effect, kinds []decision.Kind) Caller {
	c := Caller{ID: client + "." + name, Client: client, Source: "core/blockchain.go:" + strings.ReplaceAll(name, "-", ""),
		Entry: []string{"Entry"}, Effects: effects, Trigger: trigger, Route: route, Kinds: kinds,
		Status: Planned, Owner: "E01-02.lifecycle_seams"}
	if route == Refuse {
		c.Refusal = "refused by the profile check"
	}
	return c
}

// testMap is a valid map with the same callers for every client.
func testMap() Map {
	m := Map{SchemaVersion: 1, Sources: map[string]string{}}
	for _, client := range Clients {
		m.Sources[client] = strings.Repeat("a", 40)
		imp := caller(client, "import", Observation, NewBlock, []Effect{Execution, System, Canonical},
			[]decision.Kind{decision.Import, decision.Side, decision.Canonical})
		imp.Status, imp.Patch, imp.Owner = Guarded, "admission-import", "E01-02.admission"
		m.Callers = append(m.Callers, imp,
			caller(client, "set-canonical", Decision, NoNewBlock, []Effect{Canonical},
				[]decision.Kind{decision.Canonical}),
			caller(client, "engine-forkchoice", Decision, RPC, []Effect{Canonical, Finality},
				[]decision.Kind{decision.Canonical, decision.Finality}),
			caller(client, "recovery", Observation, Startup, []Effect{Execution}, []decision.Kind{decision.Import}),
			caller(client, "miner", Refuse, Sealing, []Effect{Execution, Canonical}, nil),
			caller(client, "eth-call", ReadOnly, RPC, nil, nil))
	}
	return m
}

type fixture struct {
	guard *admission.Guard
	token *admission.Token
	db    *memStore
	adm   *admission.Admission
}

func newFixture(t *testing.T, db *memStore) *fixture {
	t.Helper()
	guard := admission.NewGuard()
	token := guard.Acquire()
	adm, err := admission.Open(token, db, admission.Producer{ProducerID: "producer-1", RunID: "run-1"}, noPins{},
		noInputs{})
	if err != nil {
		t.Fatalf("admission.Open: %v", err)
	}
	return &fixture{guard: guard, token: token, db: db, adm: adm}
}

func (f *fixture) open(t *testing.T, client string, drain bool) *Gate {
	t.Helper()
	g, err := OpenMap(f.token, f.db, f.adm, "native-store-1", client, drain, testMap())
	if err != nil {
		t.Fatalf("OpenMap: %v", err)
	}
	return g
}

func (f *fixture) bridge(t *testing.T) *decision.Bridge {
	t.Helper()
	b, err := decision.Open(f.token, f.db, f.adm, "native-store-1", allRoots{}, noMirror{})
	if err != nil {
		t.Fatalf("decision.Open: %v", err)
	}
	return b
}

func hash(seed string) string {
	var out strings.Builder
	for _, b := range []byte(seed) {
		fmt.Fprintf(&out, "%02x", b)
	}
	return (out.String() + strings.Repeat("0", 64))[:63] + "1"
}

var (
	head7 = decision.Head{Number: 7, Hash: hash("head-7")}
	root7 = hash("root-7")
)

func mustErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error %v, want %v", err, want)
	}
}

func TestVectors(t *testing.T) {
	v := loadVectors(t)
	producer := v.Producer + "." + v.Run
	b := Boundary{NativeStoreID: v.StoreID, Producer: producer, Head: v.Head, Root: v.Root}
	if got, err := BoundaryDigest(b); err != nil || got != v.BoundaryDigest {
		t.Fatalf("boundary digest %s %v, want %s", got, err, v.BoundaryDigest)
	}
	finalized := FinalityChange{Field: Finalized, Caller: v.FCUCaller, Old: v.OldFinalized, New: v.NewFinalized}
	if got, err := FinalityDigest(finalized); err != nil || got != v.FinalizedDigest {
		t.Fatalf("finalized digest %s %v, want %s", got, err, v.FinalizedDigest)
	}
	safe := FinalityChange{Field: Safe, Caller: v.FCUCaller, Old: decision.Head{Hash: ZeroHash}, New: v.NewFinalized}
	if got, err := FinalityDigest(safe); err != nil || got != v.FirstSafeDigest {
		t.Fatalf("first safe digest %s %v, want %s", got, err, v.FirstSafeDigest)
	}
	att := &Attestation{SourceNumber: v.Source.Number, SourceHash: v.Source.Hash, TargetNumber: v.Target.Number,
		TargetHash: v.Target.Hash}
	e, err := ReadAttestation(func() (*Attestation, error) { return att, nil },
		func() (string, error) { return v.Genesis, nil })
	if err != nil || e.Digest != v.AttestedDigest || e.Status != Attested || e.Justified != v.Target ||
		e.Finalized != v.Source {
		t.Fatalf("attested evidence %+v %v, want digest %s", e, err, v.AttestedDigest)
	}
	e, err = ReadAttestation(func() (*Attestation, error) { return nil, nil },
		func() (string, error) { return v.Genesis, nil })
	genesis := decision.Head{Number: 0, Hash: v.Genesis}
	if err != nil || e.Digest != v.FallbackDigest || e.Status != GenesisFallback || e.Justified != genesis ||
		e.Finalized != genesis {
		t.Fatalf("fallback evidence %+v %v, want digest %s", e, err, v.FallbackDigest)
	}
	db := newStore()
	f := newFixture(t, db)
	g, err := OpenMap(f.token, db, &namedProducer{f.adm, v.Producer, v.Run}, v.StoreID, "geth", false, testMap())
	if err != nil {
		t.Fatal(err)
	}
	if string(g.key) != v.BoundaryKey {
		t.Fatalf("boundary key %s, want %s", g.key, v.BoundaryKey)
	}
}

// namedProducer reports another producer for the vector key.
type namedProducer struct {
	*admission.Admission
	producer, run string
}

func (n *namedProducer) Producer() admission.Producer {
	return admission.Producer{ProducerID: n.producer, RunID: n.run}
}

func TestShippedMapIsValid(t *testing.T) {
	m, err := Callers()
	if err != nil {
		t.Fatalf("Callers: %v", err)
	}
	for _, client := range Clients {
		n := 0
		for _, c := range m.Callers {
			if c.Client == client {
				n++
			}
		}
		if n == 0 {
			t.Fatalf("no callers of %s", client)
		}
	}
}

func TestParseMapStrict(t *testing.T) {
	for name, data := range map[string]string{
		"unknown field": `{"schema_version":1,"sources":{},"callers":[],"extra":1}`,
		"trailing":      `{"schema_version":1,"sources":{},"callers":[]} {}`,
		"not json":      `[`,
	} {
		if _, err := ParseMap([]byte(data)); !errors.Is(err, ErrMap) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	valid, err := json.Marshal(testMap())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseMap(valid); err != nil {
		t.Fatalf("valid map: %v", err)
	}
	if _, err := ParseMap(append(valid, []byte(" {}")...)); !errors.Is(err, ErrMap) {
		t.Fatalf("valid map with trailing data: %v", err)
	}
	var raw map[string]any
	_ = json.Unmarshal(valid, &raw)
	delete(raw["callers"].([]any)[3].(map[string]any), "note")
	missing, _ := json.Marshal(raw)
	if _, err := ParseMap(missing); !errors.Is(err, ErrMap) {
		t.Fatalf("a caller without a note: %v", err)
	}
	for _, field := range []string{"schema_version", "sources", "callers"} {
		var top map[string]any
		_ = json.Unmarshal(valid, &top)
		delete(top, field)
		data, _ := json.Marshal(top)
		if _, err := ParseMap(data); !errors.Is(err, ErrMap) {
			t.Fatalf("a map without %s: %v", field, err)
		}
	}
	data := `{"schema_version":1,"sources":{"geth":"` + strings.Repeat("a", 40) + `"},"callers":[]}`
	if _, err := ParseMap([]byte(data)); !errors.Is(err, ErrMap) {
		t.Fatalf("one source: %v", err)
	}
}

func TestValidateRules(t *testing.T) {
	if err := Validate(testMap()); err != nil {
		t.Fatalf("valid map: %v", err)
	}
	edit := func(i int, change func(*Caller)) Map {
		m := testMap()
		m.Callers = slices.Clone(m.Callers)
		change(&m.Callers[i])
		return m
	}
	// Caller indexes in testMap: 0 import, 1 set-canonical, 2 engine-forkchoice,
	// 3 recovery, 4 miner, 5 eth-call.
	cases := map[string]Map{
		"id of another client":    edit(0, func(c *Caller) { c.ID = "bsc.only-here" }),
		"source suffix":           edit(0, func(c *Caller) { c.Source = "core/blockchain.go:insertChain()" }),
		"source prefix":           edit(0, func(c *Caller) { c.Source = " core/blockchain.go:insertChain" }),
		"owner suffix":            edit(1, func(c *Caller) { c.Owner = "E01-02.lifecycle_seams!" }),
		"owner prefix":            edit(1, func(c *Caller) { c.Owner = "xE01-02.lifecycle_seams" }),
		"id pattern":              edit(0, func(c *Caller) { c.ID = "geth.Import" }),
		"unknown client":          edit(0, func(c *Caller) { c.Client = "erigon"; c.ID = "erigon.import" }),
		"long client name":        edit(0, func(c *Caller) { c.Client = "a-very-long-client-name" }),
		"source pattern":          edit(0, func(c *Caller) { c.Source = "core/blockchain.go" }),
		"no entry":                edit(0, func(c *Caller) { c.Entry = nil }),
		"empty entry":             edit(0, func(c *Caller) { c.Entry = []string{""} }),
		"trigger":                 edit(0, func(c *Caller) { c.Trigger = "SOMETIMES" }),
		"repeated effect":         edit(0, func(c *Caller) { c.Effects = []Effect{Execution, Execution} }),
		"repeated kind":           edit(0, func(c *Caller) { c.Kinds = []decision.Kind{decision.Import, decision.Import} }),
		"unknown effect":          edit(0, func(c *Caller) { c.Effects = append(c.Effects, "PRUNE") }),
		"refusal off REFUSE":      edit(0, func(c *Caller) { c.Refusal = "x" }),
		"read-only with effect":   edit(5, func(c *Caller) { c.Effects = []Effect{Canonical} }),
		"read-only with kind":     edit(5, func(c *Caller) { c.Kinds = []decision.Kind{decision.Canonical} }),
		"observation no exec":     edit(3, func(c *Caller) { c.Effects = []Effect{Canonical} }),
		"observation no kind":     edit(3, func(c *Caller) { c.Kinds = nil }),
		"observation finality":    edit(3, func(c *Caller) { c.Kinds = []decision.Kind{decision.Finality} }),
		"decision no effect":      edit(1, func(c *Caller) { c.Effects = nil; c.Kinds = nil }),
		"decision extra kind":     edit(1, func(c *Caller) { c.Kinds = append(c.Kinds, decision.Finality) }),
		"decision wrong kind":     edit(1, func(c *Caller) { c.Kinds = []decision.Kind{decision.Import} }),
		"decision execution":      edit(1, func(c *Caller) { c.Effects = []Effect{Execution}; c.Kinds = []decision.Kind{decision.Import} }),
		"refuse no refusal":       edit(4, func(c *Caller) { c.Refusal = "" }),
		"refuse no effect":        edit(4, func(c *Caller) { c.Effects = nil }),
		"refuse kind":             edit(4, func(c *Caller) { c.Kinds = []decision.Kind{decision.Import} }),
		"unknown route":           edit(4, func(c *Caller) { c.Route = "BYPASS"; c.Refusal = "" }),
		"guarded without patch":   edit(0, func(c *Caller) { c.Patch = "" }),
		"planned with patch":      edit(1, func(c *Caller) { c.Patch = "x" }),
		"unknown status":          edit(1, func(c *Caller) { c.Status = "DONE" }),
		"owner pattern":           edit(1, func(c *Caller) { c.Owner = "lifecycle" }),
		"duplicate id":            edit(1, func(c *Caller) { c.ID = "geth.import" }),
		"finality only as refuse": edit(2, func(c *Caller) { c.Route = Refuse; c.Kinds = nil; c.Refusal = "x" }),
	}
	for name, m := range cases {
		if err := Validate(m); !errors.Is(err, ErrMap) {
			t.Errorf("%s: %v", name, err)
		}
	}
	m := testMap()
	m.SchemaVersion = 2
	mustErr(t, Validate(m), ErrMap)
	m = testMap()
	m.Sources["bor"] = "abc"
	mustErr(t, Validate(m), ErrMap)
	m = testMap()
	m.Sources["bor"] = strings.Repeat("a", 41)
	mustErr(t, Validate(m), ErrMap)
	m = testMap()
	m.Sources["bor"] = "x" + strings.Repeat("a", 40)
	mustErr(t, Validate(m), ErrMap)
	m = testMap()
	m.Sources["erigon"] = strings.Repeat("a", 40)
	mustErr(t, Validate(m), ErrMap)
	m = testMap()
	delete(m.Sources, "bor")
	m.Sources["erigon"] = strings.Repeat("a", 40)
	mustErr(t, Validate(m), ErrMap)
	m = testMap()
	m.Callers = slices.DeleteFunc(m.Callers, func(c Caller) bool { return c.ID == "bor.import" })
	// Without bor.import, no routed bor caller has the SYSTEM effect.
	mustErr(t, Validate(m), ErrMap)
}

func TestOpenPhases(t *testing.T) {
	f := newFixture(t, newStore())
	g := f.open(t, "geth", false)
	if g.Phase() != Bootstrap {
		t.Fatalf("new run phase %s", g.Phase())
	}
	if _, ok := g.Boundary(); ok {
		t.Fatal("boundary before GoLive")
	}
	if g := f.open(t, "geth", true); g.Phase() != Drain {
		t.Fatalf("drain phase %s", g.Phase())
	}
	if _, err := g.GoLive(f.token, head7, root7, allRoots{}, f.bridge(t)); err != nil {
		t.Fatal(err)
	}
	if g := f.open(t, "geth", false); g.Phase() != Recovery {
		t.Fatalf("restart phase %s", g.Phase())
	}
	if g := f.open(t, "geth", true); g.Phase() != Drain {
		t.Fatalf("drain restart phase %s", g.Phase())
	}
}

func TestOpenArguments(t *testing.T) {
	f := newFixture(t, newStore())
	m := testMap()
	if _, err := OpenMap(f.token, f.db, nil, "native-store-1", "geth", false, m); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil admission: %v", err)
	}
	if _, err := OpenMap(f.token, nil, f.adm, "native-store-1", "geth", false, m); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil store: %v", err)
	}
	if _, err := OpenMap(nil, f.db, f.adm, "native-store-1", "geth", false, m); !errors.Is(err, admission.ErrGuard) {
		t.Fatalf("no token: %v", err)
	}
	if _, err := OpenMap(f.token, f.db, f.adm, "bad store", "geth", false, m); !errors.Is(err, ErrInvalid) {
		t.Fatalf("store id: %v", err)
	}
	if _, err := OpenMap(f.token, f.db, f.adm, "native-store-1", "erigon", false, m); !errors.Is(err, ErrInvalid) {
		t.Fatalf("client: %v", err)
	}
	m.SchemaVersion = 0
	if _, err := OpenMap(f.token, f.db, f.adm, "native-store-1", "geth", false, m); !errors.Is(err, ErrMap) {
		t.Fatalf("map: %v", err)
	}
	if _, err := Open(f.token, f.db, f.adm, "native-store-1", "bor", false); err != nil {
		t.Fatalf("Open with the shipped map: %v", err)
	}
	f.db.syncErr = errDisk
	if _, err := OpenMap(f.token, f.db, f.adm, "native-store-1", "geth", false, testMap()); !errors.Is(err, errDisk) {
		t.Fatalf("sync error: %v", err)
	}
	f.db.syncErr, f.db.hasErr = nil, errDisk
	if _, err := OpenMap(f.token, f.db, f.adm, "native-store-1", "geth", false, testMap()); !errors.Is(err, errDisk) {
		t.Fatalf("has error: %v", err)
	}
}

func TestEnterRoutes(t *testing.T) {
	f := newFixture(t, newStore())
	g := f.open(t, "geth", false)
	if _, err := g.GoLive(f.token, head7, root7, allRoots{}, f.bridge(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Enter(f.token, "bsc.import"); !errors.Is(err, ErrUnmapped) {
		t.Fatalf("other client: %v", err)
	}
	if _, err := g.Enter(f.token, "geth.unknown"); !errors.Is(err, ErrUnmapped) {
		t.Fatalf("unknown: %v", err)
	}
	for range 2 {
		if _, err := g.Enter(f.token, "geth.miner"); !errors.Is(err, ErrRefused) {
			t.Fatalf("refused caller: %v", err)
		}
	}
	if _, err := g.Enter(f.token, "geth.eth-call"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("read-only caller: %v", err)
	}
	other := admission.NewGuard().Acquire()
	if _, err := g.Enter(other, "geth.import"); !errors.Is(err, admission.ErrGuard) {
		t.Fatalf("foreign token: %v", err)
	}
	p, err := g.Enter(f.token, "geth.import")
	if err != nil || !p.Observe() || p.Caller().ID != "geth.import" {
		t.Fatalf("live import pass %v %v", p, err)
	}
	if err := p.Exit(); err != nil {
		t.Fatal(err)
	}
	if err := p.Exit(); !errors.Is(err, ErrOrder) {
		t.Fatalf("second exit: %v", err)
	}
	cov := g.Coverage()
	if cov["geth.miner"] != (Reach{Refusals: 2}) || cov["geth.import"] != (Reach{Passes: 1}) {
		t.Fatalf("coverage %v", cov)
	}
	if _, ok := cov["geth.eth-call"]; ok {
		t.Fatal("read-only caller counted")
	}
	cov["geth.import"] = Reach{}
	if g.Coverage()["geth.import"].Passes != 1 {
		t.Fatal("Coverage shares its map")
	}
}

func TestBootstrapDoesNotObserve(t *testing.T) {
	f := newFixture(t, newStore())
	g := f.open(t, "geth", false)
	p, err := g.Enter(f.token, "geth.set-canonical")
	if err != nil || p.Observe() {
		t.Fatalf("bootstrap pass %v %v", p, err)
	}
	if err := g.Require(Canonical); err != nil {
		t.Fatalf("bootstrap mutation under a pass: %v", err)
	}
	_, err = p.Intend(f.bridge(t), decision.Intent{Kind: decision.Canonical})
	mustErr(t, err, ErrPhase)
	if err := p.Exit(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryAdmitsOnlyStartup(t *testing.T) {
	f := newFixture(t, newStore())
	g := f.open(t, "geth", false)
	if _, err := g.GoLive(f.token, head7, root7, allRoots{}, f.bridge(t)); err != nil {
		t.Fatal(err)
	}
	for _, drain := range []bool{false, true} {
		g := f.open(t, "geth", drain)
		if _, err := g.Enter(f.token, "geth.import"); !errors.Is(err, ErrPhase) {
			t.Fatalf("import in %s: %v", g.Phase(), err)
		}
		if _, err := g.Enter(f.token, "geth.engine-forkchoice"); !errors.Is(err, ErrPhase) {
			t.Fatalf("forkchoice in %s: %v", g.Phase(), err)
		}
		if g.Coverage()["geth.import"].Passes != 0 {
			t.Fatal("refused phase entry counted as a pass")
		}
		outer, err := g.Enter(f.token, "geth.recovery")
		if err != nil || !outer.Observe() {
			t.Fatalf("startup pass in %s: %v", g.Phase(), err)
		}
		inner, err := g.Enter(f.token, "geth.import")
		if err != nil {
			t.Fatalf("nested import in %s: %v", g.Phase(), err)
		}
		if err := outer.Exit(); !errors.Is(err, ErrOrder) {
			t.Fatalf("outer exit first: %v", err)
		}
		if _, err := outer.Intend(f.bridge(t), decision.Intent{Kind: decision.Import}); !errors.Is(err, ErrOrder) {
			t.Fatalf("outer intend under an inner pass: %v", err)
		}
		if err := inner.Exit(); err != nil {
			t.Fatal(err)
		}
		if err := outer.Exit(); err != nil {
			t.Fatal(err)
		}
		if _, err := g.Enter(f.token, "geth.import"); !errors.Is(err, ErrPhase) {
			t.Fatalf("import after the startup pass: %v", err)
		}
	}
}

func TestGoLiveRefusesOpenPass(t *testing.T) {
	db := newStore()
	f := newFixture(t, db)
	g := f.open(t, "geth", false)
	b := f.bridge(t)
	outer, err := g.Enter(f.token, "geth.import")
	if err != nil {
		t.Fatal(err)
	}
	syncs := db.syncs
	// A bootstrap pass must not carry uncaptured mutations past H.
	if _, err := g.GoLive(f.token, head7, root7, allRoots{}, b); !errors.Is(err, ErrOrder) {
		t.Fatalf("GoLive under an open pass: %v", err)
	}
	if _, ok := g.Boundary(); ok || db.syncs != syncs || g.Phase() != Bootstrap {
		t.Fatalf("a refused GoLive wrote a boundary or changed the phase %s", g.Phase())
	}
	if err := outer.Exit(); err != nil {
		t.Fatal(err)
	}
	if _, err := g.GoLive(f.token, head7, root7, allRoots{}, b); err != nil {
		t.Fatalf("GoLive after the pass closed: %v", err)
	}
}

func TestGoLiveNeedsDurableRoot(t *testing.T) {
	db := newStore()
	f := newFixture(t, db)
	g := f.open(t, "geth", false)
	b := f.bridge(t)
	cases := map[string]struct {
		root  string
		roots decision.Roots
		want  error
	}{
		"nil roots":       {root7, nil, ErrInvalid},
		"malformed root":  {"x", allRoots{}, ErrInvalid},
		"volatile root":   {root7, someRoots{}, ErrRoot},
		"roots error":     {root7, someRoots{err: errDisk}, errDisk},
		"other root kept": {root7, someRoots{durable: map[string]bool{hash("other"): true}}, ErrRoot},
	}
	for name, tc := range cases {
		if _, err := g.GoLive(f.token, head7, tc.root, tc.roots, b); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, ok := g.Boundary(); ok || len(db.pending) != 0 {
		t.Fatal("a boundary was written without a durable root")
	}
	got, err := g.GoLive(f.token, head7, root7, someRoots{durable: map[string]bool{root7: true}}, b)
	if err != nil || got.Head != head7 || got.Root != root7 {
		t.Fatalf("GoLive with a durable root: %+v %v", got, err)
	}
	// A restart keeps H and does not check a root again.
	f2 := newFixture(t, db.powerLoss())
	g2 := f2.open(t, "geth", false)
	if _, err := g2.GoLive(f2.token, head7, root7, someRoots{}, f2.bridge(t)); err != nil {
		t.Fatalf("restart GoLive: %v", err)
	}
}

func TestDecisionPassNeedsIntent(t *testing.T) {
	f, g, _ := liveGeth(t)
	if _, err := g.Enter(f.token, "geth.engine-forkchoice"); err != nil {
		t.Fatal(err)
	}
	mustErr(t, g.Require(Canonical), ErrBypass)
	f, g, b := liveGeth(t)
	p, _ := g.Enter(f.token, "geth.engine-forkchoice")
	canonical := decision.Intent{Kind: decision.Canonical, OldHead: head7,
		NewHead: decision.Head{Number: 8, Hash: hash("head-8")}, EvidenceDigest: hash("evidence"), Events: true}
	if _, err := p.Intend(b, canonical); err != nil {
		t.Fatal(err)
	}
	// A CANONICAL intent covers every write of its reorganization.
	for range 3 {
		if err := g.Require(Canonical); err != nil {
			t.Fatalf("canonical write after its intent: %v", err)
		}
	}
	// The CANONICAL intent does not allow a finality write.
	mustErr(t, g.Require(Finality), ErrBypass)
	if g.Coverage()["geth.engine-forkchoice"] != (Reach{Passes: 1, Intents: 1}) {
		t.Fatalf("coverage %v", g.Coverage())
	}
}

func TestFinalityIntentAllowsOneWrite(t *testing.T) {
	v := loadVectors(t)
	f, g, b := liveGeth(t)
	p, err := g.Enter(f.token, "geth.engine-forkchoice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Finality(b, head7, FinalityChange{Field: Finalized, Caller: "geth.engine-forkchoice",
		Old: v.OldFinalized, New: v.NewFinalized}, true); err != nil {
		t.Fatal(err)
	}
	if err := g.Require(Finality); err != nil {
		t.Fatalf("finalized write after its intent: %v", err)
	}
	// The safe setter of the same forkchoice update needs its own intent.
	mustErr(t, g.Require(Finality), ErrBypass)
}

func TestFailedIntentGrantsNothing(t *testing.T) {
	f, g, b := liveGeth(t)
	p, err := g.Enter(f.token, "geth.set-canonical")
	if err != nil {
		t.Fatal(err)
	}
	// The bridge refuses an intent with a malformed head.
	_, err = p.Intend(b, decision.Intent{Kind: decision.Canonical, OldHead: head7, NewHead: decision.Head{Hash: "x"},
		EvidenceDigest: hash("evidence")})
	mustErr(t, err, decision.ErrInvalid)
	if g.Coverage()["geth.set-canonical"].Intents != 0 {
		t.Fatal("a refused intent was counted")
	}
	mustErr(t, g.Require(Canonical), ErrBypass)
}

func TestUnchangedFinalityAllowsRewrite(t *testing.T) {
	v := loadVectors(t)
	f, g, b := liveGeth(t)
	p, err := g.Enter(f.token, "geth.engine-forkchoice")
	if err != nil {
		t.Fatal(err)
	}
	zero := FinalityChange{Field: Finalized, Caller: "geth.engine-forkchoice", Old: v.OldFinalized,
		New: decision.Head{Hash: ZeroHash}}
	_, err = p.Finality(b, head7, zero, true)
	mustErr(t, err, ErrNoop)
	// No setter runs for a zero hash, so no write is allowed.
	mustErr(t, g.Require(Finality), ErrBypass)
	f, g, b = liveGeth(t)
	p, _ = g.Enter(f.token, "geth.engine-forkchoice")
	same := FinalityChange{Field: Finalized, Caller: "geth.engine-forkchoice", Old: v.OldFinalized,
		New: v.OldFinalized}
	_, err = p.Finality(b, head7, same, true)
	mustErr(t, err, ErrNoop)
	if err := g.Require(Finality); err != nil {
		t.Fatalf("rewrite of an unchanged reference: %v", err)
	}
	if ds, _ := b.Decisions(f.token); len(ds) != 0 {
		t.Fatalf("an unchanged reference made a decision: %+v", ds)
	}
	// The no-op allows one write only: a later setter that changes the
	// reference without its intent is a bypass.
	mustErr(t, g.Require(Finality), ErrBypass)
	// A caller that may not make FINALITY gets no rewrite either.
	f, g, b = liveGeth(t)
	q, _ := g.Enter(f.token, "geth.set-canonical")
	same.Caller = "geth.set-canonical"
	_, err = q.Finality(b, head7, same, true)
	mustErr(t, err, ErrKind)
	_ = q.Exit()
	_, err = q.Finality(b, head7, same, true)
	mustErr(t, err, ErrOrder)
	f, g, b = liveGeth(t)
	r, _ := g.Enter(f.token, "geth.engine-forkchoice")
	same.Caller = "geth.engine-forkchoice"
	other := admission.NewGuard().Acquire()
	r2 := &Pass{gate: g, caller: r.caller, token: other, granted: map[Effect]bool{}}
	_, err = r2.Finality(b, head7, same, true)
	mustErr(t, err, admission.ErrGuard)
}

func TestRequire(t *testing.T) {
	f := newFixture(t, newStore())
	g := f.open(t, "geth", false)
	mustErr(t, g.Require("PRUNE"), ErrInvalid)
	p, err := g.Enter(f.token, "geth.import")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Effect{Execution, System, Canonical} {
		if err := g.Require(e); err != nil {
			t.Fatalf("Require(%s) under import: %v", e, err)
		}
	}
	inner, err := g.Enter(f.token, "geth.set-canonical")
	if err != nil {
		t.Fatal(err)
	}
	// The innermost pass decides: set-canonical may not execute.
	mustErr(t, g.Require(Execution), ErrBypass)
	mustErr(t, g.Require(Canonical), ErrFailed)
	if _, err := g.Enter(f.token, "geth.import"); !errors.Is(err, ErrFailed) {
		t.Fatalf("enter after a bypass: %v", err)
	}
	_, err = inner.Intend(nil, decision.Intent{Kind: decision.Canonical})
	mustErr(t, err, ErrFailed)
	if err := inner.Exit(); err != nil {
		t.Fatal(err)
	}
	if err := p.Exit(); err != nil {
		t.Fatal(err)
	}
}

func TestRequireWithoutPass(t *testing.T) {
	f := newFixture(t, newStore())
	g := f.open(t, "bsc", false)
	mustErr(t, g.Require(Finality), ErrBypass)
	if _, err := g.GoLive(f.token, head7, root7, allRoots{}, f.bridge(t)); !errors.Is(err, ErrFailed) {
		t.Fatalf("GoLive after a bypass: %v", err)
	}
}

func TestIntendKinds(t *testing.T) {
	f := newFixture(t, newStore())
	g := f.open(t, "bor", false)
	b := f.bridge(t)
	if _, err := g.GoLive(f.token, head7, root7, allRoots{}, b); err != nil {
		t.Fatal(err)
	}
	p, err := g.Enter(f.token, "bor.set-canonical")
	if err != nil {
		t.Fatal(err)
	}
	finality := decision.Intent{Kind: decision.Finality, OldHead: head7, NewHead: head7,
		EvidenceDigest: hash("evidence")}
	_, err = p.Intend(b, finality)
	mustErr(t, err, ErrKind)
	_, err = p.Intend(nil, decision.Intent{Kind: decision.Canonical})
	mustErr(t, err, ErrInvalid)
	canonical := decision.Intent{Kind: decision.Canonical, OldHead: head7,
		NewHead: decision.Head{Number: 6, Hash: hash("head-6")}, EvidenceDigest: hash("evidence"), Events: true}
	rec, err := p.Intend(b, canonical)
	if err != nil || rec.Kind != decision.Canonical || rec.Sequence != 0 {
		t.Fatalf("canonical intent %+v %v", rec, err)
	}
	if err := p.Exit(); err != nil {
		t.Fatal(err)
	}
	_, err = p.Intend(b, canonical)
	mustErr(t, err, ErrOrder)
	other := admission.NewGuard().Acquire()
	if err := (&Pass{gate: g, caller: p.caller, token: other}).Exit(); !errors.Is(err, admission.ErrGuard) {
		t.Fatalf("exit with a foreign token: %v", err)
	}
	if _, err := (&Pass{gate: g, caller: p.caller, token: other}).Intend(b, canonical); !errors.Is(err, admission.ErrGuard) {
		t.Fatalf("intend with a foreign token: %v", err)
	}
}

func TestGoLiveBoundary(t *testing.T) {
	db := newStore()
	f := newFixture(t, db)
	g := f.open(t, "geth", false)
	b := f.bridge(t)
	if _, err := g.GoLive(f.token, head7, root7, allRoots{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil bridge: %v", err)
	}
	if _, err := g.GoLive(f.token, decision.Head{Number: 7, Hash: "x"}, root7, allRoots{}, b); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad head: %v", err)
	}
	if _, err := g.GoLive(nil, head7, root7, allRoots{}, b); !errors.Is(err, admission.ErrGuard) {
		t.Fatalf("no token: %v", err)
	}
	syncs := db.syncs
	got, err := g.GoLive(f.token, head7, root7, allRoots{}, b)
	if err != nil || got.Head != head7 || got.NativeStoreID != "native-store-1" || got.Producer != "producer-1.run-1" {
		t.Fatalf("GoLive %+v %v", got, err)
	}
	if db.syncs != syncs+1 || len(db.pending) != 0 {
		t.Fatalf("boundary not synced: %d syncs, %d pending", db.syncs-syncs, len(db.pending))
	}
	if mode, _ := f.adm.Mode(f.token); mode != admission.Live || g.Phase() != Live {
		t.Fatalf("mode %s phase %s", mode, g.Phase())
	}
	again, err := g.GoLive(f.token, decision.Head{Number: 9, Hash: hash("head-9")}, root7, allRoots{}, b)
	if err != nil || again != got {
		t.Fatalf("second GoLive %+v %v", again, err)
	}
	// A restart keeps H, even with a higher native head.
	f2 := newFixture(t, db.powerLoss())
	g2 := f2.open(t, "geth", false)
	if stored, ok := g2.Boundary(); !ok || stored != got {
		t.Fatalf("stored boundary %+v %v", stored, ok)
	}
	later, err := g2.GoLive(f2.token, decision.Head{Number: 20, Hash: hash("head-20")}, root7, allRoots{}, f2.bridge(t))
	if err != nil || later != got || g2.Phase() != Live {
		t.Fatalf("restart GoLive %+v %v %s", later, err, g2.Phase())
	}
	// Drain never goes live.
	f3 := newFixture(t, db.powerLoss())
	g3 := f3.open(t, "geth", true)
	if _, err := g3.GoLive(f3.token, head7, root7, allRoots{}, f3.bridge(t)); !errors.Is(err, ErrPhase) {
		t.Fatalf("drain GoLive: %v", err)
	}
}

// failingLife refuses GoLive, as the bridge does with an undecided EXEC_READY
// observation.
type failingLife struct{}

func (failingLife) GoLive(*admission.Token) error { return decision.ErrPending }

func TestGoLiveCrashPoints(t *testing.T) {
	// Power loss before the boundary sync: no boundary, the run is new again.
	db := newStore()
	f := newFixture(t, db)
	g := f.open(t, "geth", false)
	b := f.bridge(t)
	db.crashAt = db.ops + 2
	if _, err := g.GoLive(f.token, head7, root7, allRoots{}, b); !errors.Is(err, errCrash) {
		t.Fatalf("crash at sync: %v", err)
	}
	if _, err := g.Enter(f.token, "geth.import"); !errors.Is(err, ErrFailed) {
		t.Fatalf("enter after a failed boundary write: %v", err)
	}
	f2 := newFixture(t, db.powerLoss())
	if g2 := f2.open(t, "geth", false); g2.Phase() != Bootstrap {
		t.Fatalf("phase after a lost boundary %s", g2.Phase())
	}
	// Put error.
	db = newStore()
	f = newFixture(t, db)
	g = f.open(t, "geth", false)
	b = f.bridge(t)
	db.crashAt = db.ops + 1
	if _, err := g.GoLive(f.token, head7, root7, allRoots{}, b); !errors.Is(err, errCrash) {
		t.Fatalf("crash at put: %v", err)
	}
	if _, err := g.Enter(f.token, "geth.import"); !errors.Is(err, ErrFailed) {
		t.Fatalf("enter after a failed boundary put: %v", err)
	}
	// The boundary is synced but the bridge refuses GoLive: the run keeps H
	// and restarts in Recovery.
	db = newStore()
	f = newFixture(t, db)
	g = f.open(t, "geth", false)
	if _, err := g.GoLive(f.token, head7, root7, allRoots{}, failingLife{}); !errors.Is(err, decision.ErrPending) {
		t.Fatalf("bridge refusal: %v", err)
	}
	if g.Phase() != Recovery {
		t.Fatalf("phase after a refused GoLive %s", g.Phase())
	}
	if _, err := g.Enter(f.token, "geth.import"); !errors.Is(err, ErrPhase) {
		t.Fatalf("import before live: %v", err)
	}
	f2 = newFixture(t, db.powerLoss())
	g2 := f2.open(t, "geth", false)
	got, err := g2.GoLive(f2.token, decision.Head{Number: 8, Hash: hash("head-8")}, root7, allRoots{}, f2.bridge(t))
	if err != nil || got.Head != head7 {
		t.Fatalf("restart keeps H: %+v %v", got, err)
	}
}

func TestBoundaryRecordChecks(t *testing.T) {
	db := newStore()
	f := newFixture(t, db)
	g := f.open(t, "geth", false)
	if _, err := g.GoLive(f.token, head7, root7, allRoots{}, f.bridge(t)); err != nil {
		t.Fatal(err)
	}
	key := string(g.key)
	good := db.durable[key]
	if _, err := OpenMap(f.token, db, f.adm, "native-store-2", "geth", false, testMap()); !errors.Is(err, ErrInvariant) {
		t.Fatalf("other store: %v", err)
	}
	var b Boundary
	_ = strictDecode(good, &b)
	b.Producer = "producer-2.run-1"
	b.Digest, _ = BoundaryDigest(b)
	db.durable[key] = []byte(fmt.Sprintf(`{"native_store_id":%q,"producer":%q,"head":{"number":7,"hash":%q},"root":%q,"digest":%q}`,
		b.NativeStoreID, b.Producer, b.Head.Hash, b.Root, b.Digest))
	if _, err := OpenMap(f.token, db, f.adm, "native-store-1", "geth", false, testMap()); !errors.Is(err, ErrInvariant) {
		t.Fatalf("other producer: %v", err)
	}
	for name, value := range map[string][]byte{
		"digest":   bytes.Replace(good, []byte(`"number":7`), []byte(`"number":8`), 1),
		"field":    bytes.Replace(good, []byte(`{`), []byte(`{"x":1,`), 1),
		"garbage":  []byte("{"),
		"trailing": append(bytes.Clone(good), []byte(" {}")...),
		"hash":     bytes.Replace(good, []byte(head7.Hash), []byte("zz"), 1),
	} {
		db.durable[key] = value
		if _, err := OpenMap(f.token, db, f.adm, "native-store-1", "geth", false, testMap()); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	db.durable[key] = good
	db.getErr = errDisk
	if _, err := OpenMap(f.token, db, f.adm, "native-store-1", "geth", false, testMap()); !errors.Is(err, errDisk) {
		t.Fatalf("get error: %v", err)
	}
}

func TestGaps(t *testing.T) {
	m := testMap()
	all := []string{"geth.engine-forkchoice", "geth.import", "geth.miner", "geth.recovery", "geth.set-canonical"}
	if got := Gaps(m, "geth", nil); !slices.Equal(got, all) {
		t.Fatalf("gaps without coverage %v", got)
	}
	cov := map[string]Reach{"geth.import": {Passes: 1}, "geth.recovery": {Passes: 2},
		"geth.set-canonical": {Passes: 1, Intents: 1}, "geth.engine-forkchoice": {Passes: 1},
		"geth.miner": {Passes: 1}}
	if got := Gaps(m, "geth", cov); !slices.Equal(got, []string{"geth.engine-forkchoice", "geth.miner"}) {
		t.Fatalf("a decision caller needs an intent, a refused caller a refusal: %v", got)
	}
	cov["geth.engine-forkchoice"] = Reach{Passes: 1, Intents: 1}
	cov["geth.miner"] = Reach{Refusals: 1}
	cov["geth.import"] = Reach{Refusals: 1}
	if got := Gaps(m, "geth", cov); !slices.Equal(got, []string{"geth.import"}) {
		t.Fatalf("a routed caller needs a pass: %v", got)
	}
	cov["geth.import"] = Reach{Passes: 1}
	if got := Gaps(m, "geth", cov); len(got) != 0 {
		t.Fatalf("full coverage gaps %v", got)
	}
	// End to end: a gate run that reaches every caller has no gaps.
	f := newFixture(t, newStore())
	g := f.open(t, "bsc", false)
	b := f.bridge(t)
	if _, err := g.GoLive(f.token, head7, root7, allRoots{}, b); err != nil {
		t.Fatal(err)
	}
	p, err := g.Enter(f.token, "bsc.import")
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Exit()
	for i, id := range []string{"bsc.set-canonical", "bsc.engine-forkchoice"} {
		p, err := g.Enter(f.token, id)
		if err != nil {
			t.Fatal(err)
		}
		next := decision.Head{Number: uint64(8 + i), Hash: hash(fmt.Sprintf("head-%d", 8+i))}
		rec, err := p.Intend(b, decision.Intent{Kind: decision.Canonical, OldHead: head7, NewHead: next,
			EvidenceDigest: hash("evidence"), Events: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = p.Exit()
		// No native write follows, so the intent is rejected before the next.
		if _, err := b.RejectIntent(f.token, rec.Sequence, hash("no-write")); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = g.Enter(f.token, "bsc.miner")
	if got := Gaps(m, "bsc", g.Coverage()); !slices.Equal(got, []string{"bsc.recovery"}) {
		t.Fatalf("gate run gaps %v", got)
	}
}

func TestConcurrentRequire(t *testing.T) {
	f := newFixture(t, newStore())
	g := f.open(t, "geth", false)
	p, err := g.Enter(f.token, "geth.import")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.Require(Execution); err != nil {
				t.Error(err)
			}
			_ = g.Phase()
			_, _ = g.Boundary()
			_ = g.Coverage()
		}()
	}
	wg.Wait()
	if err := p.Exit(); err != nil {
		t.Fatal(err)
	}
}

func TestFinalityDigestRules(t *testing.T) {
	v := loadVectors(t)
	base := FinalityChange{Field: Finalized, Caller: v.FCUCaller, Old: v.OldFinalized, New: v.NewFinalized}
	cases := map[string]struct {
		change func(*FinalityChange)
		want   error
	}{
		"field":         {func(c *FinalityChange) { c.Field = "JUSTIFIED" }, ErrInvalid},
		"caller":        {func(c *FinalityChange) { c.Caller = "engine" }, ErrInvalid},
		"old hash":      {func(c *FinalityChange) { c.Old.Hash = "x" }, ErrInvalid},
		"new hash":      {func(c *FinalityChange) { c.New.Hash = "x" }, ErrInvalid},
		"zero new hash": {func(c *FinalityChange) { c.New = decision.Head{Number: 9, Hash: ZeroHash} }, ErrNoop},
		"unchanged":     {func(c *FinalityChange) { c.New = c.Old }, ErrNoop},
		"same hash, other number": {func(c *FinalityChange) {
			c.New = decision.Head{Number: c.Old.Number + 1,
				Hash: c.Old.Hash}
		}, ErrInvalid},
	}
	for name, tc := range cases {
		c := base
		tc.change(&c)
		if _, err := FinalityDigest(c); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The digest binds every field.
	seen := map[string]string{}
	for name, c := range map[string]FinalityChange{
		"base":   base,
		"safe":   {Field: Safe, Caller: base.Caller, Old: base.Old, New: base.New},
		"caller": {Field: Finalized, Caller: "geth.set-finalized", Old: base.Old, New: base.New},
		"old":    {Field: Finalized, Caller: base.Caller, Old: decision.Head{Number: 2, Hash: base.Old.Hash}, New: base.New},
		"new":    {Field: Finalized, Caller: base.Caller, Old: base.Old, New: decision.Head{Number: 6, Hash: base.New.Hash}},
	} {
		d, err := FinalityDigest(c)
		if err != nil {
			t.Fatal(err)
		}
		if prev, ok := seen[d]; ok {
			t.Fatalf("%s and %s share a digest", name, prev)
		}
		seen[d] = name
	}
}

// fcu is the unit form of the Geth forkchoiceUpdated finality steps: the
// finalized setter, then the safe validation, then the safe setter. A zero
// hash skips its setter. Each setter that runs records its own FINALITY
// decision before the native write, and the native write puts the marker in
// its batch. The returned error is the RPC error.
func fcu(t *testing.T, g *Gate, f *fixture, b *decision.Bridge, refs map[Field]decision.Head, finalized,
	safe decision.Head, safeValid bool) error {
	t.Helper()
	p, err := g.Enter(f.token, "geth.engine-forkchoice")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Exit(); err != nil {
			t.Fatal(err)
		}
	}()
	set := func(field Field, next decision.Head) error {
		rec, err := p.Finality(b, head7, FinalityChange{Field: field, Caller: "geth.engine-forkchoice",
			Old: refs[field], New: next}, true)
		if errors.Is(err, ErrNoop) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := g.Require(Finality); err != nil {
			return err
		}
		value, err := decision.MarkerValue(rec)
		if err != nil {
			return err
		}
		// The native setter batch: the new reference and the marker, synced.
		if err := f.db.Put([]byte("native/"+string(field)), []byte(next.Hash)); err != nil {
			return err
		}
		if err := f.db.Put(decision.MarkerKey(f.adm.Producer(), rec.Sequence), value); err != nil {
			return err
		}
		if err := f.db.SyncKeyValue(); err != nil {
			return err
		}
		refs[field] = next
		_, err = b.Resolve(f.token, rec.Sequence)
		return err
	}
	if finalized.Hash != ZeroHash {
		if err := set(Finalized, finalized); err != nil {
			return err
		}
	}
	if safe.Hash != ZeroHash && !safeValid {
		return errors.New("invalid safe block hash")
	}
	if safe.Hash != ZeroHash {
		return set(Safe, safe)
	}
	return nil
}

func liveGeth(t *testing.T) (*fixture, *Gate, *decision.Bridge) {
	t.Helper()
	f := newFixture(t, newStore())
	g := f.open(t, "geth", false)
	b := f.bridge(t)
	if _, err := g.GoLive(f.token, head7, root7, allRoots{}, b); err != nil {
		t.Fatal(err)
	}
	return f, g, b
}

// TestForkchoiceZeroHashKeepsReference is the unit form of AC-LIF-05-02: a
// zero safe or finalized hash keeps the previous native reference and makes
// no decision.
func TestForkchoiceZeroHashKeepsReference(t *testing.T) {
	v := loadVectors(t)
	f, g, b := liveGeth(t)
	refs := map[Field]decision.Head{Safe: v.OldFinalized, Finalized: v.OldFinalized}
	zero := decision.Head{Hash: ZeroHash}
	if err := fcu(t, g, f, b, refs, zero, zero, true); err != nil {
		t.Fatal(err)
	}
	if refs[Safe] != v.OldFinalized || refs[Finalized] != v.OldFinalized {
		t.Fatalf("zero hash changed a reference: %v", refs)
	}
	if ds, _ := b.Decisions(f.token); len(ds) != 0 {
		t.Fatalf("zero hash made decisions: %+v", ds)
	}
	if err := fcu(t, g, f, b, refs, v.OldFinalized, zero, true); err != nil {
		t.Fatal(err)
	}
	if ds, _ := b.Decisions(f.token); len(ds) != 0 {
		t.Fatalf("an unchanged reference made a decision: %+v", ds)
	}
}

// TestForkchoiceErrorAfterFinalizedSetter is the unit form of AC-LIF-05-03:
// the finalized setter runs, the safe validation fails and the RPC returns
// an error. The finalized decision stays decided with its evidence, and no
// safe decision exists.
func TestForkchoiceErrorAfterFinalizedSetter(t *testing.T) {
	v := loadVectors(t)
	f, g, b := liveGeth(t)
	refs := map[Field]decision.Head{Safe: v.OldFinalized, Finalized: v.OldFinalized}
	err := fcu(t, g, f, b, refs, v.NewFinalized, decision.Head{Number: 6, Hash: hash("bad-safe")}, false)
	if err == nil {
		t.Fatal("the invalid safe hash did not fail the call")
	}
	ds, err := b.Decisions(f.token)
	if err != nil || len(ds) != 1 {
		t.Fatalf("decisions %+v %v", ds, err)
	}
	d := ds[0]
	want, _ := FinalityDigest(FinalityChange{Field: Finalized, Caller: "geth.engine-forkchoice",
		Old: v.OldFinalized, New: v.NewFinalized})
	if d.State != decision.Decided || d.Intent.Kind != decision.Finality || d.Intent.EvidenceDigest != want ||
		d.Decided.Delivery != decision.Batch || d.Intent.OldHead != head7 || d.Intent.NewHead != head7 {
		t.Fatalf("finalized decision %+v", d)
	}
	if refs[Safe] != v.OldFinalized || refs[Finalized] != v.NewFinalized {
		t.Fatalf("native references %v", refs)
	}
	// The RPC error does not undo the setter: the decided record cannot be
	// rejected.
	if _, err := b.RejectIntent(f.token, d.Sequence, hash("rpc-error")); !errors.Is(err, decision.ErrState) {
		t.Fatalf("reject a decided finality: %v", err)
	}
	// A valid repeat sets safe only.
	if err := fcu(t, g, f, b, refs, v.NewFinalized, v.NewFinalized, true); err != nil {
		t.Fatal(err)
	}
	ds, _ = b.Decisions(f.token)
	if len(ds) != 2 || ds[1].Intent.Kind != decision.Finality {
		t.Fatalf("decisions after the repeat %+v", ds)
	}
}

// TestPowerLossBeforeFinalitySetter: the intent is synced, the setter batch
// with its marker is lost. The marker is absent, so the intent is rejected
// from native evidence, never decided.
func TestPowerLossBeforeFinalitySetter(t *testing.T) {
	v := loadVectors(t)
	f, g, b := liveGeth(t)
	p, err := g.Enter(f.token, "geth.engine-forkchoice")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := p.Finality(b, head7, FinalityChange{Field: Finalized, Caller: "geth.engine-forkchoice",
		Old: v.OldFinalized, New: v.NewFinalized}, true)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := decision.MarkerValue(rec)
	_ = f.db.Put(decision.MarkerKey(f.adm.Producer(), rec.Sequence), value)
	f2 := newFixture(t, f.db.powerLoss())
	b2 := f2.bridge(t)
	if _, err := b2.Resolve(f2.token, rec.Sequence); !errors.Is(err, decision.ErrNoMarker) {
		t.Fatalf("resolve without the marker: %v", err)
	}
	if _, err := b2.RejectIntent(f2.token, rec.Sequence, hash("setter-lost")); err != nil {
		t.Fatalf("reject: %v", err)
	}
	g2 := f2.open(t, "geth", false)
	if g2.Phase() != Recovery {
		t.Fatalf("phase %s", g2.Phase())
	}
}

func TestFinalityPassChecks(t *testing.T) {
	v := loadVectors(t)
	f, g, b := liveGeth(t)
	p, err := g.Enter(f.token, "geth.set-canonical")
	if err != nil {
		t.Fatal(err)
	}
	c := FinalityChange{Field: Finalized, Caller: "geth.set-canonical", Old: v.OldFinalized, New: v.NewFinalized}
	_, err = p.Finality(b, head7, c, true)
	mustErr(t, err, ErrKind)
	c.Caller = "geth.engine-forkchoice"
	_, err = p.Finality(b, head7, c, true)
	mustErr(t, err, ErrInvalid)
	_ = p.Exit()
	p, _ = g.Enter(f.token, "geth.engine-forkchoice")
	c.New = decision.Head{Hash: ZeroHash}
	_, err = p.Finality(b, head7, c, true)
	mustErr(t, err, ErrNoop)
	_ = p.Exit()
}

func attestation(v vectorFile) *Attestation {
	return &Attestation{SourceNumber: v.Source.Number, SourceHash: v.Source.Hash, TargetNumber: v.Target.Number,
		TargetHash: v.Target.Hash}
}

// TestAttestationGenesisFallback is the unit form of AC-LIF-06-04: a correct
// snapshot without an attestation gives GenesisFallback with the real
// genesis hash, not an error and not an attestation.
func TestAttestationGenesisFallback(t *testing.T) {
	v := loadVectors(t)
	e, err := ReadAttestation(func() (*Attestation, error) { return nil, nil },
		func() (string, error) { return v.Genesis, nil })
	if err != nil || e.Status != GenesisFallback || e.Justified.Hash != v.Genesis || e.Finalized.Number != 0 {
		t.Fatalf("fallback %+v %v", e, err)
	}
	_, err = ReadAttestation(func() (*Attestation, error) { return nil, nil },
		func() (string, error) { return "", errDisk })
	if !errors.Is(err, ErrSource) || !errors.Is(err, errDisk) {
		t.Fatalf("genesis lookup error lost: %v", err)
	}
	for name, genesis := range map[string]func() (string, error){
		"zero genesis":      func() (string, error) { return ZeroHash, nil },
		"malformed genesis": func() (string, error) { return "0x00", nil },
	} {
		if _, err := ReadAttestation(func() (*Attestation, error) { return nil, nil }, genesis); !errors.Is(err, ErrSource) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestAttestationLookupError is the unit form of AC-LIF-06-05: a snapshot
// or header lookup error is a source error, never genesis, Unknown or empty
// evidence.
func TestAttestationLookupError(t *testing.T) {
	v := loadVectors(t)
	genesisCalled := false
	e, err := ReadAttestation(func() (*Attestation, error) { return nil, errDisk },
		func() (string, error) { genesisCalled = true; return v.Genesis, nil })
	if !errors.Is(err, ErrSource) || !errors.Is(err, errDisk) || genesisCalled || e != (AttestationEvidence{}) {
		t.Fatalf("lookup error %+v %v (genesis read %v)", e, err, genesisCalled)
	}
	cases := map[string]func(*Attestation){
		"zero target":     func(a *Attestation) { a.TargetHash = ZeroHash },
		"zero source":     func(a *Attestation) { a.SourceHash = ZeroHash },
		"bad target":      func(a *Attestation) { a.TargetHash = "x" },
		"bad source":      func(a *Attestation) { a.SourceHash = "x" },
		"source > target": func(a *Attestation) { a.SourceNumber = a.TargetNumber + 1 },
	}
	for name, change := range cases {
		a := attestation(v)
		change(a)
		if _, err := ReadAttestation(func() (*Attestation, error) { return a, nil },
			func() (string, error) { return v.Genesis, nil }); !errors.Is(err, ErrSource) {
			t.Errorf("%s: %v", name, err)
		}
	}
	a := attestation(v)
	a.SourceNumber = a.TargetNumber
	if _, err := ReadAttestation(func() (*Attestation, error) { return a, nil },
		func() (string, error) { return "", errDisk }); err != nil {
		t.Fatalf("attested evidence must not read genesis: %v", err)
	}
	if _, err := ReadAttestation(nil, func() (string, error) { return v.Genesis, nil }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil snapshot reader: %v", err)
	}
	if _, err := ReadAttestation(func() (*Attestation, error) { return nil, nil }, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil genesis reader: %v", err)
	}
}
