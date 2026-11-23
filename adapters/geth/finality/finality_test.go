package finality

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

	recoverygc "github.com/sigaev-pro/bc-indexer/adapters/recovery_gc"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"

	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
)

// vectorFile holds the independent expected values. `python3
// test/harness/finality.py vectors` computes them from the README formulas;
// test/harness/test_finality.py checks that the file matches.
type vectorFile struct {
	StoreID                string        `json:"store_id"`
	Producer               string        `json:"producer"`
	Run                    string        `json:"run"`
	Head                   decision.Head `json:"head"`
	SetFinalizedCaller     string        `json:"set_finalized_caller"`
	SetSafeCaller          string        `json:"set_safe_caller"`
	StartupCaller          string        `json:"startup_caller"`
	Block3                 decision.Head `json:"block_3"`
	Block5                 decision.Head `json:"block_5"`
	Block7                 decision.Head `json:"block_7"`
	LiveSafeDigest         string        `json:"live_safe_digest"`
	LiveFinalizedDigest    string        `json:"live_finalized_digest"`
	RestartSafeDigest      string        `json:"restart_safe_digest"`
	RestartFinalizedDigest string        `json:"restart_finalized_digest"`
	UnsetSafeDigest        string        `json:"unset_safe_digest"`
	RetainedSafeDigest     string        `json:"retained_safe_digest"`
	RestartEvidenceDigest  string        `json:"restart_evidence_digest"`
	ClearEvidenceDigest    string        `json:"clear_evidence_digest"`
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
	errChain = errors.New("injected ancestry lookup failure")
)

type write struct {
	key     string
	value   []byte
	deleted bool
}

// memStore models the native key-value store. Put and Delete stay pending
// until SyncKeyValue makes them durable; powerLoss keeps a chosen subset of
// the pending writes. crashAt makes the mutating operation with that index
// (counted from 1) fail without effect.
type memStore struct {
	durable map[string][]byte
	pending []write
	ops     int
	crashAt int
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
	_, ok := s.view(string(key))
	return ok, nil
}

func (s *memStore) Get(key []byte) ([]byte, error) {
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

// Range visits every visible key that starts with prefix, in ascending
// byte order, with the value visible through unsynced writes; a pending
// delete hides the key.
func (s *memStore) Range(prefix []byte, visit func(key, value []byte) error) error {
	view := map[string][]byte{}
	for key, value := range s.durable {
		if strings.HasPrefix(key, string(prefix)) {
			view[key] = value
		}
	}
	for _, w := range s.pending {
		if !strings.HasPrefix(w.key, string(prefix)) {
			continue
		}
		if w.deleted {
			delete(view, w.key)
		} else {
			view[w.key] = w.value
		}
	}
	for _, key := range slices.Sorted(maps.Keys(view)) {
		if err := visit([]byte(key), view[key]); err != nil {
			return err
		}
	}
	return nil
}

func applyWrite(durable map[string][]byte, w write) {
	if w.deleted {
		delete(durable, w.key)
	} else {
		durable[w.key] = w.value
	}
}

// powerLoss keeps pending write i when bit i of keep is set and returns the
// reopened store.
func (s *memStore) powerLoss(keep uint) *memStore {
	out := &memStore{durable: maps.Clone(s.durable)}
	for i, w := range s.pending {
		if keep&(1<<i) != 0 {
			applyWrite(out.durable, w)
		}
	}
	return out
}

// noPins and noInputs fail every call: the tests here admit no block
// observation, so no pins or inputs are ever needed.
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

type noMirror struct{}

func (noMirror) Applied(uint64, string) (bool, error) { return false, nil }

// appliedMirror reports the recorded decisions as applied, as the lifecycle
// registry mirror does once a decision published its events: the bridge
// cleanup then deletes the native markers of those decisions.
type appliedMirror struct {
	applied map[uint64]string
}

func (m appliedMirror) Applied(sequence uint64, digest string) (bool, error) {
	return m.applied[sequence] == digest, nil
}

// The GC anchor owner seams of the fixture: the recovery GC engine over
// the same store, with the fixture chain as its ancestry oracle and
// nothing pending, released or pinned.
type gcOracle struct{ chain *chain }

func (o gcOracle) Contains(ancestor, descendant recoverygc.AnchorRef) recoverygc.Verdict {
	ok, err := o.chain.isAncestorOrSelf(Ref{Height: ancestor.Height, Hash: ancestor.Hash.String()},
		Ref{Height: descendant.Height, Hash: descendant.Hash.String()})
	if err != nil {
		return recoverygc.VerdictUnknown
	}
	if ok {
		return recoverygc.VerdictContains
	}
	return recoverygc.VerdictDivergent
}

type gcNothingPending struct{}

func (gcNothingPending) Pending(recoverygc.EntityID) (recoverygc.PendingReport, error) {
	return recoverygc.PendingReport{}, nil
}

type gcReleaser struct{}

func (gcReleaser) ReleaseEntity(recoverygc.EntityID) error { return nil }

type gcPins struct{}

func (gcPins) Release(string) error { return nil }

// The semantic identity of the fixture run: the network and the ID16
// identities of the GC anchor contract.
var (
	gcNetwork  = semantic.Network{ChainID: semantic.UInt256{1}, GenesisHash: seedHash32("gc-genesis")}
	gcNode     = semantic.ID16{0x11}
	gcProducer = semantic.ID16{0x22}
	gcRun      = semantic.ID16{0x33}
)

// seedHash32 derives a fixed Hash32 of a seed: the seed bytes in a 32-byte
// value.
func seedHash32(seed string) semantic.Hash32 {
	var out semantic.Hash32
	copy(out[:], []byte(seed))
	return out
}

// openEngine opens the recovery GC engine of the fixture over db.
func openEngine(t *testing.T, db *memStore, chain *chain) *recoverygc.Engine {
	t.Helper()
	engine, _, err := recoverygc.Open(db, gcOracle{chain: chain}, gcNothingPending{}, gcReleaser{}, gcPins{})
	if err != nil {
		t.Fatalf("recoverygc.Open: %v", err)
	}
	return engine
}

// chain is a block tree: the parent of every block. It answers the
// ancestry oracle of the tracker.
type chain struct {
	parent  map[string]string
	errOn   string
	failAll bool
}

func newChain() *chain { return &chain{parent: map[string]string{}} }

func (c *chain) add(hash, parent string) *chain {
	c.parent[hash] = parent
	return c
}

func (c *chain) isAncestorOrSelf(ancestor, block Ref) (bool, error) {
	if c.failAll || (c.errOn != "" && (ancestor.Hash == c.errOn || block.Hash == c.errOn)) {
		return false, errChain
	}
	for {
		if block.Hash == ancestor.Hash {
			return true, nil
		}
		parent, ok := c.parent[block.Hash]
		if !ok {
			return false, nil
		}
		block = Ref{Hash: parent}
	}
}

func hash(seed string) string {
	var out strings.Builder
	for _, b := range []byte(seed) {
		fmt.Fprintf(&out, "%02x", b)
	}
	return (out.String() + strings.Repeat("0", 64))[:63] + "1"
}

func headOf(height uint64, seed string) decision.Head {
	return decision.Head{Number: height, Hash: hash(seed)}
}

func refOf(h decision.Head) Ref { return Ref{Height: h.Number, Hash: h.Hash} }

// none is the "none yet" native reference: the zero hash, never an empty
// string.
var none = decision.Head{Hash: lifecycle.ZeroHash}

// fixed ad-hoc blocks of the fixture chain: a higher live safe block, an
// advanced finalized block and a height-5 fork.
var (
	safeBlock = headOf(7, "block-7-safe")
	tallBlock = headOf(10, "block-10")
	forkBlock = headOf(5, "fork-5")
)

func testCaller(client, name string, route lifecycle.Route, trigger lifecycle.Trigger, effects []lifecycle.Effect,
	kinds []decision.Kind) lifecycle.Caller {
	c := lifecycle.Caller{ID: client + "." + name, Client: client,
		Source: "core/blockchain.go:" + strings.ReplaceAll(name, "-", ""),
		Entry:  []string{"Entry"}, Effects: effects, Trigger: trigger, Route: route, Kinds: kinds,
		Status: lifecycle.Planned, Owner: "E05-01.eth_finality"}
	if route == lifecycle.Refuse {
		c.Refusal = "refused by the profile check"
	}
	return c
}

// testMap is a valid caller map with the same shape for every client; the
// geth rows are the ones the tests enter.
func testMap() lifecycle.Map {
	m := lifecycle.Map{SchemaVersion: 1, Sources: map[string]string{}}
	for _, client := range lifecycle.Clients {
		m.Sources[client] = strings.Repeat("a", 40)
		m.Callers = append(m.Callers,
			testCaller(client, "import", lifecycle.Observation, lifecycle.NewBlock,
				[]lifecycle.Effect{lifecycle.Execution, lifecycle.System, lifecycle.Canonical},
				[]decision.Kind{decision.Import, decision.Side, decision.Canonical}),
			testCaller(client, "set-canonical", lifecycle.Decision, lifecycle.NoNewBlock,
				[]lifecycle.Effect{lifecycle.Canonical}, []decision.Kind{decision.Canonical}),
			testCaller(client, "set-safe", lifecycle.Decision, lifecycle.NoNewBlock,
				[]lifecycle.Effect{lifecycle.Finality}, []decision.Kind{decision.Finality}),
			testCaller(client, "set-finalized", lifecycle.Decision, lifecycle.NoNewBlock,
				[]lifecycle.Effect{lifecycle.Finality}, []decision.Kind{decision.Finality}),
			testCaller(client, "startup-reconcile", lifecycle.Decision, lifecycle.Startup,
				[]lifecycle.Effect{lifecycle.Canonical, lifecycle.Finality},
				[]decision.Kind{decision.Canonical, decision.Finality}),
			testCaller(client, "recovery", lifecycle.Observation, lifecycle.Startup,
				[]lifecycle.Effect{lifecycle.Execution}, []decision.Kind{decision.Import}),
			testCaller(client, "miner", lifecycle.Refuse, lifecycle.Sealing,
				[]lifecycle.Effect{lifecycle.Execution, lifecycle.Canonical}, nil),
			testCaller(client, "eth-call", lifecycle.ReadOnly, lifecycle.RPC, nil, nil))
	}
	return m
}

// fixture is one opened producer run: the admission, the decision bridge,
// the lifecycle gate, the recovery GC engine and the finality tracker on
// one store.
type fixture struct {
	t      *testing.T
	v      vectorFile
	guard  *admission.Guard
	token  *admission.Token
	db     *memStore
	chain  *chain
	mirror decision.Mirror
	adm    *admission.Admission
	bridge *decision.Bridge
	gate   *lifecycle.Gate
	gc     *recoverygc.Engine
	tr     *Tracker
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, v: loadVectors(t), guard: admission.NewGuard(), db: newStore(),
		chain: newChain(), mirror: noMirror{}}
	f.token = f.guard.Acquire()
	f.buildChain()
	f.open(f.db)
	return f
}

// restart opens a second fixture over the reopened store of f.
func (f *fixture) restart(t *testing.T, keep uint) *fixture {
	t.Helper()
	g := &fixture{t: t, v: f.v, guard: f.guard, token: f.token, chain: newChain(), mirror: f.mirror}
	g.buildChain()
	g.open(f.db.powerLoss(keep))
	return g
}

// buildChain links the vector blocks and the ad-hoc blocks: genesis g, a
// canonical branch 3-5-7-10, a height-5 fork and a safe block at 7.
func (f *fixture) buildChain() {
	genesis := headOf(0, "genesis")
	f.chain.add(f.v.Block3.Hash, genesis.Hash)
	f.chain.add(f.v.Block5.Hash, f.v.Block3.Hash)
	f.chain.add(f.v.Head.Hash, f.v.Block5.Hash)
	f.chain.add(safeBlock.Hash, f.v.Block5.Hash)
	f.chain.add(tallBlock.Hash, f.v.Head.Hash)
	f.chain.add(forkBlock.Hash, f.v.Block3.Hash)
}

func (f *fixture) open(db *memStore) {
	f.t.Helper()
	f.db = db
	adm, err := admission.Open(f.token, db, admission.Producer{ProducerID: f.v.Producer, RunID: f.v.Run},
		noPins{}, noInputs{})
	if err != nil {
		f.t.Fatalf("admission.Open: %v", err)
	}
	bridge, err := decision.Open(f.token, db, adm, f.v.StoreID, allRoots{}, f.mirror)
	if err != nil {
		f.t.Fatalf("decision.Open: %v", err)
	}
	gate, err := lifecycle.OpenMap(f.token, db, adm, f.v.StoreID, "geth", false, testMap())
	if err != nil {
		f.t.Fatalf("lifecycle.OpenMap: %v", err)
	}
	gc := openEngine(f.t, db, f.chain)
	tr, err := Open(f.token, db, adm, f.v.StoreID,
		RunIdentity{Network: gcNetwork, Node: gcNode, Producer: gcProducer, Run: gcRun},
		f.chain.isAncestorOrSelf, gc)
	if err != nil {
		f.t.Fatalf("finality.Open: %v", err)
	}
	f.adm, f.bridge, f.gate, f.gc, f.tr = adm, bridge, gate, gc, tr
}

// goLive records the live boundary and switches the gate and the admission
// to live.
func (f *fixture) goLive() {
	f.t.Helper()
	if _, err := f.gate.GoLive(f.token, f.v.Head, hash("root-7"), allRoots{}, f.bridge); err != nil {
		f.t.Fatalf("go live: %v", err)
	}
}

// trySet drives one live setter call: the pass of the mapped caller, the
// planned observation through Set, the durable batch and the resolved
// decision. It reports the failure instead of stopping the test.
func (f *fixture) trySet(callerID string, field lifecycle.Field, oldBlock, newBlock decision.Head) (Planned,
	error) {
	f.t.Helper()
	pass, err := f.gate.Enter(f.token, callerID)
	if err != nil {
		return Planned{}, err
	}
	defer func() {
		if err := pass.Exit(); err != nil {
			f.t.Fatalf("exit %s: %v", callerID, err)
		}
	}()
	change := lifecycle.FinalityChange{Field: field, Caller: callerID, Old: oldBlock, New: newBlock}
	plan, err := f.tr.Set(f.token, pass, f.bridge, f.v.Head, change)
	if err != nil {
		return Planned{}, err
	}
	if plan.Noop {
		return plan, nil
	}
	if err := f.tr.Commit(f.token, plan); err != nil {
		return Planned{}, err
	}
	if _, err := f.tr.Resolve(f.token, f.bridge, plan.Intent.Sequence); err != nil {
		return Planned{}, err
	}
	return plan, nil
}

func (f *fixture) set(callerID string, field lifecycle.Field, oldBlock, newBlock decision.Head) Planned {
	f.t.Helper()
	plan, err := f.trySet(callerID, field, oldBlock, newBlock)
	if err != nil {
		f.t.Fatalf("set %s: %v", field, err)
	}
	return plan
}

// tryClear drives one rewind clear of a field.
func (f *fixture) tryClear(field lifecycle.Field) error {
	f.t.Helper()
	callerID := f.v.SetSafeCaller
	if field == lifecycle.Finalized {
		callerID = f.v.SetFinalizedCaller
	}
	pass, err := f.gate.Enter(f.token, callerID)
	if err != nil {
		return err
	}
	defer func() {
		if err := pass.Exit(); err != nil {
			f.t.Fatalf("exit %s: %v", callerID, err)
		}
	}()
	plan, err := f.tr.Clear(f.token, pass, f.bridge, f.v.Head, field, f.v.ClearEvidenceDigest)
	if err != nil {
		return err
	}
	if err := f.tr.Commit(f.token, plan); err != nil {
		return err
	}
	_, err = f.tr.Resolve(f.token, f.bridge, plan.Intent.Sequence)
	return err
}

// startup drives the startup refresh to completion inside the startup
// reconcile pass: one planned change per call, committed and resolved
// before the next.
func (f *fixture) startup(finalized decision.Head, known bool) []Planned {
	f.t.Helper()
	var plans []Planned
	for round := 0; ; round++ {
		if round > 8 {
			f.t.Fatal("the startup refresh does not converge")
		}
		pass, err := f.gate.Enter(f.token, f.v.StartupCaller)
		if err != nil {
			f.t.Fatalf("enter startup: %v", err)
		}
		plan, more, err := f.tr.Startup(f.token, pass, f.bridge, f.v.Head,
			func() (decision.Head, bool, error) {
				return finalized, known, nil
			})
		if err != nil {
			f.t.Fatalf("startup: %v", err)
		}
		if err := pass.Exit(); err != nil {
			f.t.Fatalf("exit startup: %v", err)
		}
		if !more {
			return plans
		}
		if err := f.tr.Commit(f.token, plan); err != nil {
			f.t.Fatalf("commit startup: %v", err)
		}
		if _, err := f.tr.Resolve(f.token, f.bridge, plan.Intent.Sequence); err != nil {
			f.t.Fatalf("resolve startup: %v", err)
		}
		plans = append(plans, plan)
	}
}

// recoverDecisions finishes the open decisions of a restarted fixture: an
// intent with its marker completes, one without is rejected by native
// recovery.
func (f *fixture) recoverDecisions() {
	f.t.Helper()
	decisions, err := f.bridge.Decisions(f.token)
	if err != nil {
		f.t.Fatalf("decisions: %v", err)
	}
	for _, d := range decisions {
		if d.State != decision.Intended {
			continue
		}
		if d.Marker {
			if _, err := f.tr.Resolve(f.token, f.bridge, d.Sequence); err != nil {
				f.t.Fatalf("resolve %d: %v", d.Sequence, err)
			}
			continue
		}
		if _, err := f.bridge.RejectIntent(f.token, d.Sequence, f.v.ClearEvidenceDigest); err != nil {
			f.t.Fatalf("reject %d: %v", d.Sequence, err)
		}
	}
}

func mustErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error %v, want %v", err, want)
	}
}

func mustStatus(t *testing.T, got, want Status) {
	t.Helper()
	if got != want {
		t.Fatalf("status %s, want %s", got, want)
	}
}

func mustObservation(t *testing.T, name string, got, want Observation) {
	t.Helper()
	if got != want {
		t.Fatalf("%s observation %+v, want %+v", name, got, want)
	}
}

func TestVectors(t *testing.T) {
	v := loadVectors(t)
	producer := DecisionRef{Producer: v.Producer + "." + v.Run}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"live safe", mustDigest(t, observationRecord{Field: lifecycle.Safe, Known: true,
			Block: refOf(v.Block5), Origin: OriginLive, Basis: ConsensusSetter,
			Decision: &DecisionRef{Producer: producer.Producer, Sequence: 0}}), v.LiveSafeDigest},
		{"live finalized", mustDigest(t, observationRecord{Field: lifecycle.Finalized, Known: true,
			Block: refOf(v.Block5), Origin: OriginLive, Basis: UnsetBasis,
			Decision: &DecisionRef{Producer: producer.Producer, Sequence: 0}}), v.LiveFinalizedDigest},
		{"restart safe", mustDigest(t, observationRecord{Field: lifecycle.Safe, Known: true,
			Block: refOf(v.Block5), Origin: OriginRestart, Basis: StartupFinalizedFallback,
			Decision: &DecisionRef{Producer: producer.Producer, Sequence: 1}}), v.RestartSafeDigest},
		{"restart finalized", mustDigest(t, observationRecord{Field: lifecycle.Finalized, Known: true,
			Block: refOf(v.Block5), Origin: OriginRestart, Basis: UnsetBasis,
			Decision: &DecisionRef{Producer: producer.Producer, Sequence: 0}}), v.RestartFinalizedDigest},
		{"unset safe", mustDigest(t, observationRecord{Field: lifecycle.Safe, Known: false,
			Reason: ReferenceUnset, Basis: UnsetBasis,
			Decision: &DecisionRef{Producer: producer.Producer, Sequence: 0}}), v.UnsetSafeDigest},
		{"retained safe", mustDigest(t, observationRecord{Field: lifecycle.Safe, Known: true,
			Block: refOf(v.Block5), Origin: OriginRetained, Basis: ConsensusSetter,
			Decision: &DecisionRef{Producer: producer.Producer, Sequence: 0}}), v.RetainedSafeDigest},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s digest %s, want %s", c.name, c.got, c.want)
		}
	}
	restart, err := RestartDigest(lifecycle.Finalized, v.Block5, v.Head)
	if err != nil || restart != v.RestartEvidenceDigest {
		t.Fatalf("restart evidence digest %s %v, want %s", restart, err, v.RestartEvidenceDigest)
	}
}

func mustDigest(t *testing.T, r observationRecord) string {
	t.Helper()
	digest, err := ObservationDigest(r)
	if err != nil {
		t.Fatalf("observation digest of %s: %v", r.Field, err)
	}
	return digest
}

func TestOpenFreshTracker(t *testing.T) {
	f := newFixture(t)
	if _, err := f.tr.Evidence(); !errors.Is(err, ErrState) {
		t.Fatalf("evidence before any observation: %v", err)
	}
	if _, ok := f.tr.Anchor(); ok {
		t.Fatal("fresh tracker holds an anchor")
	}
	if _, ok := f.tr.Retained(lifecycle.Safe); ok {
		t.Fatal("fresh tracker holds retained evidence")
	}
	if stopped, _ := f.tr.Stopped(); stopped {
		t.Fatal("fresh tracker is stopped")
	}
}

func TestLiveSetterObservations(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	if plan := f.set(f.v.SetSafeCaller, lifecycle.Safe, none, f.v.Block5); plan.Noop {
		t.Fatal("the first safe set is not a no-op")
	}
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	ev, err := f.tr.Evidence()
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if ev.Head != refOf(f.v.Head) {
		t.Fatalf("observed head %v, want %v", ev.Head, f.v.Head)
	}
	mustObservation(t, "safe", ev.Safe, KnownObservation(refOf(f.v.Block5), OriginLive))
	mustObservation(t, "finalized", ev.Finalized, KnownObservation(refOf(f.v.Block5), OriginLive))
	if ev.Basis != ConsensusSetter {
		t.Fatalf("safe basis %s, want %s", ev.Basis, ConsensusSetter)
	}
	// A finality-only update without a new block: the same head, a new safe.
	f.set(f.v.SetSafeCaller, lifecycle.Safe, f.v.Block5, safeBlock)
	ev, err = f.tr.Evidence()
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if ev.Safe.Block != refOf(safeBlock) || ev.Head != refOf(f.v.Head) {
		t.Fatalf("the finality-only update moved nothing: %+v", ev)
	}
	// The GC anchor follows the finalized reference only.
	anchor, err := f.tr.Accept(f.token)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if refOfAnchor(anchor.Anchor) != refOf(f.v.Block5) {
		t.Fatalf("anchor %v, want the finalized block", anchor.Anchor)
	}
}

func TestSetNoopKeepsReference(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	ev, _ := f.tr.Evidence()
	open := len(f.openDecisions())
	zero := f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, f.v.Block5,
		decision.Head{Number: 5, Hash: lifecycle.ZeroHash})
	if !zero.Noop {
		t.Fatal("a zero hash is a no-op")
	}
	unchanged := f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, f.v.Block5, f.v.Block5)
	if !unchanged.Noop {
		t.Fatal("an unchanged reference is a no-op")
	}
	after, _ := f.tr.Evidence()
	if after.Finalized != ev.Finalized {
		t.Fatalf("a no-op changed the finalized reference: %+v", after.Finalized)
	}
	if got := len(f.openDecisions()); got != open {
		t.Fatalf("a no-op made a decision: %d open, want %d", got, open)
	}
}

func (f *fixture) openDecisions() []decision.Decision {
	f.t.Helper()
	decisions, err := f.bridge.Decisions(f.token)
	if err != nil {
		f.t.Fatalf("decisions: %v", err)
	}
	return decisions
}

// TestSetterStaysDecidedAfterLaterFailure is FIN-ETH-03: the finalized
// setter ran and decided; the later safe validation of the same forkchoice
// request failed and the request returned an error. The evidence keeps the
// actual native mutation, and the failed later step decides nothing.
func TestSetterStaysDecidedAfterLaterFailure(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	// The later safe validation failed: the safe setter never ran.
	ev, err := f.tr.Evidence()
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	mustObservation(t, "finalized", ev.Finalized, KnownObservation(refOf(f.v.Block5), OriginLive))
	mustObservation(t, "safe", ev.Safe, UnknownObservation(NotObserved))
	// The decided finalized setter keeps its marker and its anchor.
	if _, err := f.db.Get(decision.MarkerKey(admission.Producer{ProducerID: f.v.Producer, RunID: f.v.Run}, 0)); err != nil {
		t.Fatalf("finalized marker: %v", err)
	}
	if _, err := f.tr.Accept(f.token); err != nil {
		t.Fatalf("accept: %v", err)
	}
}

func TestCommitCrashAndRecovery(t *testing.T) {
	for name, keep := range map[string]uint{"power loss": 0, "crash": ^uint(0)} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.goLive()
			if _, err := f.trySet(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5); err != nil {
				t.Fatalf("first set: %v", err)
			}
			// The safe change crashes inside its commit: after the
			// observation record and the marker write, before their sync.
			f.db.crashAt = f.db.ops + 7
			err := func() error {
				defer func() { f.db.crashAt = 0 }()
				_, err := f.trySet(f.v.SetSafeCaller, lifecycle.Safe, none, f.v.Block5)
				return err
			}()
			if !errors.Is(err, errCrash) {
				t.Fatalf("commit error %v, want the injected crash", err)
			}
			reopened := f.db.powerLoss(keep)
			safeKey := keyPrefix + f.v.Producer + "." + f.v.Run + "/" + fieldKey(lifecycle.Safe)
			_, safeDurable := reopened.durable[safeKey]
			if safeDurable != (name == "crash") {
				t.Fatalf("safe record durable=%v after a %s", safeDurable, name)
			}
			g := &fixture{t: t, v: f.v, guard: f.guard, token: f.token, chain: newChain(),
				mirror: noMirror{}}
			g.buildChain()
			g.open(reopened)
			// Native recovery finishes the interrupted transition: the
			// crash kept its marker, the power loss left none.
			g.recoverDecisions()
			if _, err := g.tr.Evidence(); !errors.Is(err, ErrState) {
				t.Fatalf("evidence without a refreshed head: %v", err)
			}
			// The startup refresh re-establishes the evidence without a new
			// head event.
			g.startup(f.v.Block5, true)
			ev, err := g.tr.Evidence()
			if err != nil {
				t.Fatalf("evidence: %v", err)
			}
			mustObservation(t, "finalized", ev.Finalized, KnownObservation(refOf(f.v.Block5), OriginLive))
			mustObservation(t, "safe", ev.Safe, KnownObservation(refOf(f.v.Block5), OriginRestart))
			if ev.Basis != StartupFinalizedFallback {
				t.Fatalf("safe basis %s, want the startup fallback", ev.Basis)
			}
		})
	}
}

// TestStartupFallbackLowerSafe is AC-LIF-05-01: live safe above finalized,
// both observed before; restart restores safe from finalized. The new safe
// has startup provenance, the lower safe is not fatal and not a GC basis,
// and the previous live safe survives as retained evidence.
func TestStartupFallbackLowerSafe(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	f.set(f.v.SetSafeCaller, lifecycle.Safe, none, safeBlock)
	anchor, err := f.tr.Accept(f.token)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	g := f.restart(t, ^uint(0))
	if plans := g.startup(f.v.Block5, true); len(plans) == 0 {
		t.Fatal("the startup fallback of a lower safe plans a change")
	}
	ev, err := g.tr.Evidence()
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	mustObservation(t, "safe", ev.Safe, KnownObservation(refOf(f.v.Block5), OriginRestart))
	if ev.Basis != StartupFinalizedFallback {
		t.Fatalf("safe basis %s, want the startup fallback", ev.Basis)
	}
	retained, ok := g.tr.Retained(lifecycle.Safe)
	if !ok {
		t.Fatal("the previous live safe was destroyed")
	}
	mustObservation(t, "retained safe", retained, KnownObservation(refOf(safeBlock), OriginRetained))
	// The lower safe is not fatal and no basis for GC.
	if stopped, err := g.tr.Stopped(); stopped {
		t.Fatalf("the tracker stopped on the lower safe: %v", err)
	}
	status, err := g.tr.StatusOf(refOf(f.v.Head))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	mustStatus(t, status, StatusUnfinalized)
	again, err := g.tr.Accept(g.token)
	if err != nil {
		t.Fatalf("accept after restart: %v", err)
	}
	if again.Anchor != anchor.Anchor {
		t.Fatalf("the anchor moved to %v, want %v", again.Anchor, anchor.Anchor)
	}
	// A higher live safe replaces the retained evidence.
	g.goLive()
	g.set(g.v.SetSafeCaller, lifecycle.Safe, f.v.Block5, safeBlock)
	if _, ok := g.tr.Retained(lifecycle.Safe); ok {
		t.Fatal("retained evidence outlived a higher safe")
	}
}

// TestStartupIdempotentReplay: repeating the startup refresh with the same
// finalized reference plans nothing and creates no new decision.
func TestStartupIdempotentReplay(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	f.set(f.v.SetSafeCaller, lifecycle.Safe, none, f.v.Block5)
	g := f.restart(t, ^uint(0))
	if plans := g.startup(f.v.Block5, true); len(plans) != 1 {
		t.Fatalf("the first startup planned %d changes, want the safe fallback", len(plans))
	}
	open := len(g.openDecisions())
	if plans := g.startup(f.v.Block5, true); len(plans) != 0 {
		t.Fatalf("the repeated startup planned %d changes", len(plans))
	}
	if got := len(g.openDecisions()); got != open {
		t.Fatalf("the repeated startup changed the open decisions: %d, want %d", got, open)
	}
}

// TestStartupFinalityAdvanced: finality moved during the downtime; the
// startup refresh observes it without a new head event and the projection
// of the saved activation follows.
func TestStartupFinalityAdvanced(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	status, err := f.tr.StatusOf(refOf(f.v.Head))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	mustStatus(t, status, StatusUnfinalized)
	g := f.restart(t, ^uint(0))
	plans := g.startup(tallBlock, true)
	if len(plans) != 2 {
		t.Fatalf("the advanced startup planned %d changes, want finalized and safe", len(plans))
	}
	ev, err := g.tr.Evidence()
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	mustObservation(t, "finalized", ev.Finalized, KnownObservation(refOf(tallBlock), OriginRestart))
	mustObservation(t, "safe", ev.Safe, KnownObservation(refOf(tallBlock), OriginRestart))
	status, err = g.tr.StatusOf(refOf(f.v.Head))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	mustStatus(t, status, StatusFinalized)
}

func TestStartupReaderErrorIsSourceError(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	g := f.restart(t, ^uint(0))
	pass, err := g.gate.Enter(g.token, g.v.StartupCaller)
	if err != nil {
		g.t.Fatalf("enter: %v", err)
	}
	_, _, err = g.tr.Startup(g.token, pass, g.bridge, g.v.Head, func() (decision.Head, bool, error) {
		return decision.Head{}, false, errDisk
	})
	if err := pass.Exit(); err != nil {
		g.t.Fatalf("exit: %v", err)
	}
	mustErr(t, err, ErrSource)
	if stopped, _ := g.tr.Stopped(); stopped {
		t.Fatal("a reader error stopped the tracker")
	}
	// The refresh succeeds once the source recovers.
	g.startup(f.v.Block5, true)
	if _, err := g.tr.Evidence(); err != nil {
		t.Fatalf("evidence after recovery: %v", err)
	}
}

// TestFatalRegressionBelowAnchor is AC-LIF-05-04: with a durable
// irreversible anchor F, a new authoritative SetFinalized below F stops the
// native owner before any READY, publication or GC of the conflicting
// decision, and is not dismissed as a stale callback.
func TestFatalRegressionBelowAnchor(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	if _, err := f.tr.Accept(f.token); err != nil {
		t.Fatalf("accept: %v", err)
	}
	_, err := f.trySet(f.v.SetFinalizedCaller, lifecycle.Finalized, f.v.Block5, f.v.Block3)
	mustErr(t, err, ErrFatal)
	if errors.Is(err, ErrSource) || errors.Is(err, lifecycle.ErrNoop) {
		t.Fatalf("the regression was dismissed as %v", err)
	}
	assertStoppedByRegression(t, f, f.v.Block5)
}

// TestFatalRegressionSameHeightIncompatibleHash is the same-head variant of
// AC-LIF-05-04: a new authoritative SetFinalized at the anchor height with
// an incompatible hash, with the former head unchanged.
func TestFatalRegressionSameHeightIncompatibleHash(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	if _, err := f.tr.Accept(f.token); err != nil {
		t.Fatalf("accept: %v", err)
	}
	_, err := f.trySet(f.v.SetFinalizedCaller, lifecycle.Finalized, f.v.Block5, forkBlock)
	mustErr(t, err, ErrFatal)
	assertStoppedByRegression(t, f, f.v.Block5)
}

func assertStoppedByRegression(t *testing.T, f *fixture, want decision.Head) {
	t.Helper()
	stopped, err := f.tr.Stopped()
	if !stopped || !errors.Is(err, ErrFatal) {
		t.Fatalf("tracker stopped=%v with %v, want a fatal stop", stopped, err)
	}
	// No evidence, marker or GC of the conflicting decision exists.
	ev, readErr := f.tr.Evidence()
	if readErr != nil {
		t.Fatalf("evidence after the fatal stop: %v", readErr)
	}
	if !ev.Finalized.Known || ev.Finalized.Block != refOf(want) {
		t.Fatalf("the conflicting decision became evidence: %+v", ev.Finalized)
	}
	if _, err := f.tr.Accept(f.token); !errors.Is(err, ErrFailed) {
		t.Fatalf("accept after the fatal stop: %v, want the stopped tracker", err)
	}
	if _, err := f.tr.Set(f.token, nil, nil, f.v.Head, lifecycle.FinalityChange{}); !errors.Is(err, ErrFailed) {
		t.Fatalf("set after the fatal stop: %v, want the stopped tracker", err)
	}
	// The accepted anchor record is untouched: already accepted
	// irreversibility is not revoked by the stop.
	anchor, ok := f.tr.Anchor()
	if !ok || refOfAnchor(anchor.Anchor) != refOf(want) {
		t.Fatalf("anchor after the fatal stop: %v %v", anchor.Anchor, ok)
	}
	g := f.restart(t, ^uint(0))
	g.startup(want, true)
	if _, err := g.tr.Accept(g.token); err != nil {
		t.Fatalf("accept after restart: %v", err)
	}
	anchor, ok = g.tr.Anchor()
	if !ok || refOfAnchor(anchor.Anchor) != refOf(want) {
		t.Fatalf("anchor after restart: %v %v", anchor.Anchor, ok)
	}
}

// TestAncestryLookupErrorIsNotFatal: an ancestry lookup failure during the
// regression check is a source error; it stops nothing and blocks the
// anchor, it does not declare a conflict.
func TestAncestryLookupErrorIsNotFatal(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	if _, err := f.tr.Accept(f.token); err != nil {
		t.Fatalf("accept: %v", err)
	}
	f.chain.failAll = true
	_, err := f.trySet(f.v.SetFinalizedCaller, lifecycle.Finalized, f.v.Block5, f.v.Block3)
	mustErr(t, err, ErrSource)
	if stopped, _ := f.tr.Stopped(); stopped {
		t.Fatal("an ancestry lookup error stopped the tracker")
	}
	// Once the ancestry source recovers, the compatible change runs and the
	// anchor follows; while it is down, a new acceptance is blocked.
	f.chain.failAll = false
	forward := f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, f.v.Block5, f.v.Head)
	f.chain.failAll = true
	if _, err := f.tr.Accept(f.token); !errors.Is(err, ErrSource) {
		t.Fatalf("accept during the ancestry failure: %v, want the source error", err)
	}
	f.chain.failAll = false
	moved, err := f.tr.Accept(f.token)
	if err != nil {
		t.Fatalf("accept forward: %v", err)
	}
	if refOfAnchor(moved.Anchor) != refOf(f.v.Head) || moved.EvidenceDigest != forward.Next.Digest {
		t.Fatalf("forward anchor %+v", moved)
	}
}

func TestSafeNeverFatalAndNeverAnAnchor(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	f.set(f.v.SetSafeCaller, lifecycle.Safe, none, f.v.Head)
	if _, err := f.tr.Accept(f.token); err != nil {
		t.Fatalf("accept: %v", err)
	}
	// A safe below the anchor is a normal observation, not fatal.
	lower := f.set(f.v.SetSafeCaller, lifecycle.Safe, f.v.Head, f.v.Block3)
	if lower.Noop {
		t.Fatal("a lower safe is a real change")
	}
	ev, err := f.tr.Evidence()
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if ev.Safe.Block != refOf(f.v.Block3) {
		t.Fatalf("safe %v, want the lower block", ev.Safe.Block)
	}
	retained, ok := f.tr.Retained(lifecycle.Safe)
	if !ok || retained.Block != refOf(f.v.Head) {
		t.Fatalf("retained safe %v %v, want the higher previous safe", retained, ok)
	}
	// Clearing safe is a real native write, not a zero-hash no-op.
	if err := f.tryClear(lifecycle.Safe); err != nil {
		t.Fatalf("clear safe: %v", err)
	}
	ev, err = f.tr.Evidence()
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	mustObservation(t, "cleared safe", ev.Safe, UnknownObservation(ReferenceUnset))
	anchor, _ := f.tr.Anchor()
	if refOfAnchor(anchor.Anchor) != refOf(f.v.Block5) {
		t.Fatalf("anchor %v, want the finalized block", anchor.Anchor)
	}
}

func TestClearFinalizedBelowAnchorIsFatal(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	if _, err := f.tr.Accept(f.token); err != nil {
		t.Fatalf("accept: %v", err)
	}
	err := f.tryClear(lifecycle.Finalized)
	mustErr(t, err, ErrFatal)
	assertStoppedByRegression(t, f, f.v.Block5)
}

func TestClearFinalizedWithoutAnchor(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	if err := f.tryClear(lifecycle.Finalized); err != nil {
		t.Fatalf("clear finalized: %v", err)
	}
	ev, err := f.tr.Evidence()
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	mustObservation(t, "cleared finalized", ev.Finalized, UnknownObservation(ReferenceUnset))
	// Without a known finalized reference there is no anchor source.
	if _, err := f.tr.Accept(f.token); !errors.Is(err, ErrState) {
		t.Fatalf("accept without finalized: %v", err)
	}
}

func TestAnchorAcceptanceRules(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	// No finalized observation yet: safe alone is never an anchor.
	f.set(f.v.SetSafeCaller, lifecycle.Safe, none, f.v.Block5)
	if _, err := f.tr.Accept(f.token); !errors.Is(err, ErrState) {
		t.Fatalf("accept from safe only: %v", err)
	}
	// A finalized reference off the canonical execution ancestry of the
	// observed head is not an anchor.
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, forkBlock)
	if _, err := f.tr.Accept(f.token); !errors.Is(err, ErrState) {
		t.Fatalf("accept off the canonical ancestry: %v", err)
	}
	// The canonical finalized reference is accepted, idempotently.
	canonical := f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, forkBlock, f.v.Block5)
	anchor, err := f.tr.Accept(f.token)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if anchor.EvidenceDigest != canonical.Next.Digest {
		t.Fatalf("anchor evidence %s, want the finalized observation digest %s", anchor.EvidenceDigest,
			canonical.Next.Digest)
	}
	again, err := f.tr.Accept(f.token)
	if err != nil || again != anchor {
		t.Fatalf("repeat accept %v %v, want the same anchor", again, err)
	}
	// The anchor follows finalized forward, never backward.
	higher := f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, f.v.Block5, f.v.Head)
	moved, err := f.tr.Accept(f.token)
	if err != nil {
		t.Fatalf("accept forward: %v", err)
	}
	if refOfAnchor(moved.Anchor) != refOf(f.v.Head) || moved.EvidenceDigest != higher.Next.Digest {
		t.Fatalf("forward anchor %+v", moved)
	}
}

// TestAnchorRefusesRegression: a durable finalized record below the
// accepted anchor is refused by Accept and keeps the accepted anchor. The
// record pair is written directly, because the tracked flows stop at the
// fatal regression before it can arise.
func TestAnchorRefusesRegression(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	higher := f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Head)
	if _, err := f.tr.Accept(f.token); err != nil {
		t.Fatalf("accept: %v", err)
	}
	// Hand-write an older durable finalized record, as a damaged store
	// could hold one below the accepted anchor.
	lower := observationRecord{NativeStoreID: f.v.StoreID,
		Producer: f.v.Producer + "." + f.v.Run, Field: lifecycle.Finalized, Known: true,
		Block: refOf(f.v.Block5), Origin: OriginRestart, Basis: UnsetBasis,
		Decision: &DecisionRef{Producer: f.v.Producer + "." + f.v.Run, Sequence: higher.Intent.Sequence}}
	digest, err := ObservationDigest(lower)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	lower.Digest = digest
	data, err := json.Marshal(lower)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := f.db.Put(f.tr.key(fieldKey(lifecycle.Finalized)), data); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := f.db.SyncKeyValue(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	g := f.restart(t, ^uint(0))
	if _, err := g.tr.Accept(g.token); !errors.Is(err, ErrRegression) {
		t.Fatalf("accept a candidate below the anchor: %v", err)
	}
	anchor, ok := g.tr.Anchor()
	if !ok || refOfAnchor(anchor.Anchor) != refOf(f.v.Head) {
		t.Fatalf("anchor %v %v, want the accepted one", anchor.Anchor, ok)
	}
}

// TestAcceptSurvivesMarkerCleanup is the Accept/CHECKPOINT rule: a FINALITY
// decision carries public StatusChanged events, so its native marker lives
// until the mirror applied it — and the bridge cleanup then deletes the
// marker. Acceptance is driven by the durable finalized observation record,
// never by the marker, so it survives the cleanup.
func TestAcceptSurvivesMarkerCleanup(t *testing.T) {
	f := &fixture{t: t, v: loadVectors(t), guard: admission.NewGuard(), db: newStore(), chain: newChain(),
		mirror: appliedMirror{applied: map[uint64]string{}}}
	f.token = f.guard.Acquire()
	f.buildChain()
	f.open(f.db)
	f.goLive()
	plan := f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	if !plan.Intent.Events {
		t.Fatal("the FINALITY decision carries no public StatusChanged events")
	}
	decided, err := f.bridge.Resolve(f.token, plan.Intent.Sequence)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	f.mirror.(appliedMirror).applied[plan.Intent.Sequence] = decided.Digest
	if err := f.bridge.Cleanup(f.token); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	marker, err := f.db.Has(decision.MarkerKey(admission.Producer{ProducerID: f.v.Producer, RunID: f.v.Run},
		plan.Intent.Sequence))
	if err != nil {
		t.Fatalf("marker read: %v", err)
	}
	if marker {
		t.Fatal("the cleanup kept the marker of the applied decision")
	}
	anchor, err := f.tr.Accept(f.token)
	if err != nil {
		t.Fatalf("accept after the cleanup deleted the marker: %v", err)
	}
	if refOfAnchor(anchor.Anchor) != refOf(f.v.Block5) || anchor.EvidenceDigest != plan.Next.Digest {
		t.Fatalf("anchor %+v, want the finalized observation of %s", anchor, f.v.Block5.Hash)
	}
}

// TestStartupWithoutFinalizedReferenceClearsStaleRecords: known=false of
// the startup reader means the native store holds no finalized reference,
// so the stale Known records of the previous run are cleared as unset, one
// planned change per field in the fixed order; the saved evidence survives
// as retained and is never presented as a fresh native read.
func TestStartupWithoutFinalizedReferenceClearsStaleRecords(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	f.set(f.v.SetSafeCaller, lifecycle.Safe, none, safeBlock)
	g := f.restart(t, ^uint(0))
	plans := g.startup(decision.Head{}, false)
	if len(plans) != 2 {
		t.Fatalf("the unset startup planned %d changes, want finalized then safe", len(plans))
	}
	if plans[0].Next.Field != lifecycle.Finalized || plans[1].Next.Field != lifecycle.Safe {
		t.Fatalf("the unset startup planned %s then %s", plans[0].Next.Field, plans[1].Next.Field)
	}
	ev, err := g.tr.Evidence()
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	mustObservation(t, "cleared finalized", ev.Finalized, UnknownObservation(ReferenceUnset))
	mustObservation(t, "cleared safe", ev.Safe, UnknownObservation(ReferenceUnset))
	retained, ok := g.tr.Retained(lifecycle.Finalized)
	if !ok || retained.Block != refOf(f.v.Block5) || retained.Origin != OriginRetained {
		t.Fatalf("retained finalized %v %v, want the saved block as retained", retained, ok)
	}
	retainedSafe, ok := g.tr.Retained(lifecycle.Safe)
	if !ok || retainedSafe.Block != refOf(safeBlock) || retainedSafe.Origin != OriginRetained {
		t.Fatalf("retained safe %v %v, want the saved block as retained", retainedSafe, ok)
	}
	if plans := g.startup(decision.Head{}, false); len(plans) != 0 {
		t.Fatalf("the repeated unset startup planned %d changes", len(plans))
	}
}

// TestStartupWithoutFinalizedUnderAnchorIsFatal: a startup that finds no
// finalized reference while an anchor is accepted lost the anchored
// finalized ancestry — the same fatal rewind as a live clear.
func TestStartupWithoutFinalizedUnderAnchorIsFatal(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	if _, err := f.tr.Accept(f.token); err != nil {
		t.Fatalf("accept: %v", err)
	}
	g := f.restart(t, ^uint(0))
	pass, err := g.gate.Enter(g.token, g.v.StartupCaller)
	if err != nil {
		g.t.Fatalf("enter: %v", err)
	}
	_, _, err = g.tr.Startup(g.token, pass, g.bridge, g.v.Head, func() (decision.Head, bool, error) {
		return decision.Head{}, false, nil
	})
	if err := pass.Exit(); err != nil {
		g.t.Fatalf("exit: %v", err)
	}
	mustErr(t, err, ErrFatal)
	stopped, stopErr := g.tr.Stopped()
	if !stopped || !errors.Is(stopErr, ErrFatal) {
		t.Fatalf("stopped=%v with %v, want the fatal stop", stopped, stopErr)
	}
	anchor, ok := g.tr.Anchor()
	if !ok || refOfAnchor(anchor.Anchor) != refOf(f.v.Block5) {
		t.Fatalf("anchor %v %v, want the accepted one", anchor.Anchor, ok)
	}
}

func TestStatusOfMatrix(t *testing.T) {
	f := newFixture(t)
	finalized := KnownObservation(refOf(f.v.Block5), OriginLive)
	unknown := UnknownObservation(NotObserved)
	cases := []struct {
		name       string
		ev         Evidence
		activation Ref
		want       Status
	}{
		{"finalized covers", Evidence{Safe: unknown, Finalized: finalized}, refOf(f.v.Block3), StatusFinalized},
		{"unknown safe keeps finalized", Evidence{Safe: unknown, Finalized: finalized}, refOf(f.v.Block3),
			StatusFinalized},
		{"safe covers", Evidence{Safe: KnownObservation(refOf(f.v.Block5), OriginLive), Finalized: unknown},
			refOf(f.v.Block3), StatusSafe},
		{"known without coverage", Evidence{Safe: KnownObservation(refOf(f.v.Block5), OriginLive),
			Finalized: finalized}, refOf(f.v.Head), StatusUnfinalized},
		{"no known reference", Evidence{Safe: unknown, Finalized: unknown}, refOf(f.v.Block3), StatusUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := StatusOf(c.activation, c.ev, f.chain.isAncestorOrSelf)
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			mustStatus(t, got, c.want)
		})
	}
	// An ancestry lookup error propagates and is never a negative status.
	f.chain.errOn = f.v.Block3.Hash
	if _, err := StatusOf(refOf(f.v.Block3), Evidence{Finalized: finalized}, f.chain.isAncestorOrSelf); !errors.Is(err,
		errChain) {
		t.Fatalf("ancestry error %v, want the lookup failure", err)
	}
}

func TestOpenRejectsDamagedRecords(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	recordKey := string(f.tr.key(fieldKey(lifecycle.Finalized)))
	for name, want := range map[string]error{"foreign store": ErrInvariant, "wrong digest": ErrCorrupt,
		"corrupt bytes": ErrCorrupt} {
		t.Run(name, func(t *testing.T) {
			reopened := f.db.powerLoss(^uint(0))
			switch name {
			case "foreign store":
				var record observationRecord
				if err := strictDecode(reopened.durable[recordKey], &record); err != nil {
					t.Fatalf("decode: %v", err)
				}
				record.NativeStoreID = "another-store"
				reopened.durable[recordKey] = marshalRecord(t, record)
			case "wrong digest":
				var record observationRecord
				if err := strictDecode(reopened.durable[recordKey], &record); err != nil {
					t.Fatalf("decode: %v", err)
				}
				record.Digest = hash("wrong")
				reopened.durable[recordKey] = marshalRecord(t, record)
			case "corrupt bytes":
				reopened.durable[recordKey] = []byte("{")
			}
			adm, err := admission.Open(f.token, reopened, admission.Producer{ProducerID: f.v.Producer,
				RunID: f.v.Run}, noPins{}, noInputs{})
			if err != nil {
				t.Fatalf("admission.Open: %v", err)
			}
			g := &fixture{t: t, v: f.v, guard: f.guard, token: f.token, chain: newChain(), mirror: noMirror{}}
			g.buildChain()
			gc := openEngine(t, reopened, g.chain)
			if _, err := Open(f.token, reopened, adm, f.v.StoreID,
				RunIdentity{Network: gcNetwork, Node: gcNode, Producer: gcProducer, Run: gcRun},
				g.chain.isAncestorOrSelf, gc); !errors.Is(err, want) {
				t.Fatalf("open damaged store: %v, want %v", err, want)
			}
		})
	}
}

func marshalRecord(t *testing.T, record observationRecord) []byte {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func TestKeysArePrunerSafe(t *testing.T) {
	f := newFixture(t)
	f.goLive()
	f.set(f.v.SetFinalizedCaller, lifecycle.Finalized, none, f.v.Block5)
	if _, err := f.tr.Accept(f.token); err != nil {
		t.Fatalf("accept: %v", err)
	}
	for key := range f.db.durable {
		if !strings.HasPrefix(key, keyPrefix) && !strings.HasPrefix(key, "extractor-") ||
			len(key) <= 33 || key[0] != 'e' {
			t.Fatalf("key %q is not pruner-safe", key)
		}
	}
}
