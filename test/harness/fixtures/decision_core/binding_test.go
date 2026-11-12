package decisionbinding

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/extractor/admission"
	admissionbinding "github.com/ethereum/go-ethereum/extractor/admission_binding"
	"github.com/ethereum/go-ethereum/extractor/decision"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
)

// The vector inputs of source_inputs (test/harness/source_inputs.py); the
// decision inputs come from extractor/decision/vectors.json.
var (
	items     = []sourceinputs.Item{{Name: "header", Data: []byte("header-bytes")}, {Name: "body", Data: []byte{}}}
	request0  = []byte("GET /clerk/event-record/list?from-id=1&to-time=1700000000&limit=50")
	response0 = []byte(`{"result":[]}`)
	request1  = []byte("GET /bor/span/1")
	response1 = bytes.Repeat([]byte{7}, 70000)
	errCrash  = errors.New("injected power loss")
)

type vectors struct {
	StoreID          string              `json:"store_id"`
	Producer         string              `json:"producer"`
	Run              string              `json:"run"`
	Candidate        admission.Candidate `json:"candidate"`
	ExecDigest       string              `json:"exec_digest"`
	OldHead          decision.Head       `json:"old_head"`
	NewHead          decision.Head       `json:"new_head"`
	ChildRoot        string              `json:"child_root"`
	DecisionEvidence string              `json:"decision_evidence"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	data, err := os.ReadFile("../decision/vectors.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	return v
}

type write struct {
	key     string
	value   []byte
	deleted bool
}

// memStore is the power-loss store model of all three packages: a Put or
// Delete is durable after SyncKeyValue; powerLoss keeps a chosen subset of
// pending writes; crashAt fails the mutating operation with that index
// without effect.
type memStore struct {
	durable map[string][]byte
	pending []write
	ops     int
	crashAt int
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

func apply(durable map[string][]byte, w write) {
	if w.deleted {
		delete(durable, w.key)
	} else {
		durable[w.key] = w.value
	}
}

func (s *memStore) SyncKeyValue() error {
	if err := s.step(); err != nil {
		return err
	}
	for _, w := range s.pending {
		apply(s.durable, w)
	}
	s.pending = nil
	return nil
}

// powerLoss keeps pending write i when bit i of keep is set.
func (s *memStore) powerLoss(keep uint64) *memStore {
	out := &memStore{durable: maps.Clone(s.durable)}
	for i, w := range s.pending {
		if i < 64 && keep&(1<<i) != 0 {
			apply(out.durable, w)
		}
	}
	return out
}

func (s *memStore) keys(prefix string) []string {
	out := []string{}
	for key := range s.durable {
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}

// storePins is a pin ledger in the same store; a released pin has no key.
type storePins struct{ db *memStore }

func pinKey(id string) []byte { return []byte("extractor-test-pin-v1/" + id) }

func (p *storePins) Verify(id, root string) (string, error) {
	value, ok := p.db.view(string(pinKey(id)))
	if !ok {
		return "", fmt.Errorf("no pin %s", id)
	}
	if string(value) != root {
		return "", fmt.Errorf("pin %s holds another root", id)
	}
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:]), nil
}

func (p *storePins) Release(id string) error {
	if err := p.db.Delete(pinKey(id)); err != nil {
		return err
	}
	return p.db.SyncKeyValue()
}

// roots reports the child roots that the native side made durable.
type roots map[string]bool

func (r roots) Durable(root string) (bool, error) { return r[root], nil }

// mirror is the applied-decision frontier.
type mirror map[uint64]string

func (m mirror) Applied(seq uint64, digest string) (bool, error) { return m[seq] == digest, nil }

type node struct {
	v      vectors
	db     *memStore
	token  *admission.Token
	adm    *admission.Admission
	b      *decision.Bridge
	roots  roots
	mirror mirror
}

func (n *node) open(db *memStore) error {
	n.db = db
	adm, err := admission.OpenWithCleanup(n.token, db, admission.Producer{ProducerID: n.v.Producer, RunID: n.v.Run},
		&storePins{db: db}, admissionbinding.Inputs{DB: db}, Purger{DB: db})
	if err != nil {
		return err
	}
	b, err := decision.Open(n.token, db, adm, n.v.StoreID, n.roots, n.mirror)
	if err != nil {
		return err
	}
	n.adm, n.b = adm, b
	return nil
}

// observe runs one observation through the real source_inputs log to
// EXEC_READY and makes its child root durable.
func (n *node) observe() (admission.ExecReadyRecord, error) {
	obs, err := n.adm.Begin(n.token, n.v.Candidate)
	if err != nil {
		return admission.ExecReadyRecord{}, err
	}
	if err := n.db.Put(pinKey(obs.Key), []byte(n.v.Candidate.ParentRoot)); err != nil {
		return admission.ExecReadyRecord{}, err
	}
	if err := n.db.SyncKeyValue(); err != nil {
		return admission.ExecReadyRecord{}, err
	}
	if _, err := sourceinputs.SealCapsule(n.db, obs.Key, items); err != nil {
		return admission.ExecReadyRecord{}, err
	}
	if _, err := n.adm.Admit(n.token, obs.Key); err != nil {
		return admission.ExecReadyRecord{}, err
	}
	log, err := sourceinputs.OpenLog(n.db, obs.Key)
	if err != nil {
		return admission.ExecReadyRecord{}, err
	}
	for _, step := range []struct{ request, response []byte }{{request0, response0}, {request1, response1}} {
		if _, err := log.Next(step.request, func() ([]byte, error) { return step.response, nil }); err != nil {
			return admission.ExecReadyRecord{}, err
		}
	}
	if _, err := log.Finish(); err != nil {
		return admission.ExecReadyRecord{}, err
	}
	n.roots[n.v.ChildRoot] = true
	return n.adm.ExecReady(n.token, obs.Key, n.v.ExecDigest)
}

// decide runs the decision of an EXEC_READY observation: intent, the native
// batch with the marker, NATIVE_DECIDED, the mirror and the marker cleanup.
func (n *node) decide(ready admission.ExecReadyRecord) error {
	intent, err := n.b.Intend(n.token, decision.Intent{Kind: decision.Import, OldHead: n.v.OldHead,
		NewHead: n.v.NewHead, EvidenceDigest: n.v.DecisionEvidence, Events: true,
		Members: []decision.Member{{Observation: ready.Observation, ExecReadyDigest: ready.Digest,
			ChildRoot: n.v.ChildRoot}}})
	if err != nil {
		return err
	}
	return n.finish(intent)
}

func (n *node) finish(intent decision.IntentRecord) error {
	value, err := decision.MarkerValue(intent)
	if err != nil {
		return err
	}
	if err := n.db.Put(decision.MarkerKey(n.adm.Producer(), intent.Sequence), value); err != nil {
		return err
	}
	if err := n.db.SyncKeyValue(); err != nil {
		return err
	}
	if _, err := n.b.Resolve(n.token, intent.Sequence); err != nil {
		return err
	}
	obligations, err := n.b.Obligations(n.token)
	if err != nil {
		return err
	}
	for _, o := range obligations {
		n.mirror[o.Decided.Sequence] = o.Decided.Digest
	}
	return n.b.Cleanup(n.token)
}

// recover completes what a power loss interrupted, as a client recovery
// driver does: an unresolved intent is completed by the native side, an
// undecided EXEC_READY observation gets its decision.
func (n *node) recover() error {
	decisions, err := n.b.Decisions(n.token)
	if err != nil {
		return err
	}
	for _, d := range decisions {
		if d.State == decision.Intended || d.State == decision.Decided {
			if err := n.finish(d.Intent); err != nil {
				return err
			}
		}
	}
	undecided, err := n.b.Undecided(n.token)
	if err != nil {
		return err
	}
	for _, obs := range undecided {
		if err := n.decide(*obs.ExecReady); err != nil {
			return err
		}
	}
	return n.b.GoLive(n.token)
}

func newNode(t *testing.T) *node {
	t.Helper()
	n := &node{v: loadVectors(t), token: admission.NewGuard().Acquire(), roots: roots{}, mirror: mirror{}}
	if err := n.open(newStore()); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := n.b.GoLive(n.token); err != nil {
		t.Fatalf("go live: %v", err)
	}
	return n
}

// checkClean asserts the end state of a decided observation: its inputs,
// pin, admission records and decision records are gone; only the counters,
// low-water and purged marks remain.
func checkClean(t *testing.T, n *node, label string) {
	t.Helper()
	if left := n.db.keys("extractor-source-inputs-v1/"); len(left) != 0 {
		t.Fatalf("%s: source_inputs records left: %v", label, left)
	}
	if left := n.db.keys("extractor-test-pin-v1/"); len(left) != 0 {
		t.Fatalf("%s: pins left: %v", label, left)
	}
	if left := n.db.keys("extractor-decision-marker-v1/"); len(left) != 0 {
		t.Fatalf("%s: markers left: %v", label, left)
	}
	for _, prefix := range []string{"extractor-admission-v1/", "extractor-decision-v1/"} {
		for _, key := range n.db.keys(prefix) {
			if !strings.HasSuffix(key, "/counter") && !strings.HasSuffix(key, "/lowwater") &&
				!strings.HasSuffix(key, "/purged") {
				t.Fatalf("%s: record left: %s", label, key)
			}
		}
	}
	if len(n.mirror) != 1 {
		t.Fatalf("%s: mirror applied %v", label, n.mirror)
	}
}

// TestRealCleanup decides an observation whose inputs live in the real
// source_inputs log; closing it deletes the capsule and log (D100) and every
// record below the low-water marks.
func TestRealCleanup(t *testing.T) {
	n := newNode(t)
	ready, err := n.observe()
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	// Capsule, its one non-empty item chunk, two entries, response chunks,
	// frontier and closed manifest.
	if got := len(n.db.keys("extractor-source-inputs-v1/")); got != 9 {
		t.Fatalf("inputs before the decision: %d keys", got)
	}
	if err := n.decide(ready); err != nil {
		t.Fatalf("decide: %v", err)
	}
	checkClean(t, n, "clean run")
	if err := n.open(n.db); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := n.b.GoLive(n.token); err != nil {
		t.Fatalf("go live after reopen: %v", err)
	}
}

// TestPowerLossFromDecisionToCleanup: a power loss at every store operation
// from Intend to the last cleanup sync, with four subsets of unsynced writes
// kept. Recovery always ends in the clean state with one applied decision.
func TestPowerLossFromDecisionToCleanup(t *testing.T) {
	probe := newNode(t)
	ready, err := probe.observe()
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	start := probe.db.ops
	if err := probe.decide(ready); err != nil {
		t.Fatalf("clean run: %v", err)
	}
	total := probe.db.ops - start
	for crash := 1; crash <= total; crash++ {
		for _, keep := range []uint64{0, 1, 0b0110, ^uint64(0)} {
			n := newNode(t)
			ready, err := n.observe()
			if err != nil {
				t.Fatalf("observe: %v", err)
			}
			n.db.crashAt = n.db.ops + crash
			if err := n.decide(ready); !errors.Is(err, errCrash) {
				t.Fatalf("crash %d: %v", crash, err)
			}
			if err := n.open(n.db.powerLoss(keep)); err != nil {
				t.Fatalf("crash %d keep %b: reopen: %v", crash, keep, err)
			}
			if err := n.recover(); err != nil {
				t.Fatalf("crash %d keep %b: recover: %v", crash, keep, err)
			}
			checkClean(t, n, fmt.Sprintf("crash %d keep %b", crash, keep))
		}
	}
}
