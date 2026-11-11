package admissionbinding

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/extractor/admission"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
)

// The vector inputs of source_inputs (test/harness/source_inputs.py) and of
// admission (test/harness/admission.py). The expected digests come from
// extractor/admission/vectors.json ("binding"), computed in Python.
var (
	items     = []sourceinputs.Item{{Name: "header", Data: []byte("header-bytes")}, {Name: "body", Data: []byte{}}}
	request0  = []byte("GET /clerk/event-record/list?from-id=1&to-time=1700000000&limit=50")
	response0 = []byte(`{"result":[]}`)
	request1  = []byte("GET /bor/span/1")
	response1 = bytes.Repeat(func() []byte {
		out := make([]byte, 256)
		for i := range out {
			out[i] = byte(i)
		}
		return out
	}(), 300)
	errCrash = errors.New("injected power loss")
)

type vectors struct {
	Producer   string              `json:"producer"`
	Run        string              `json:"run"`
	Key        string              `json:"key"`
	Candidate  admission.Candidate `json:"candidate"`
	PinDigest  string              `json:"pin_digest"`
	ExecDigest string              `json:"exec_digest"`
	Binding    map[string]string   `json:"binding"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	data, err := os.ReadFile("../admission/vectors.json")
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
	key   string
	value []byte
}

// memStore is the power-loss store model of both packages: a Put is durable
// after SyncKeyValue; powerLoss keeps a chosen subset of pending writes;
// crashAt fails the mutating operation with that index without effect.
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
			return s.pending[i].value, true
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
	s.pending = append(s.pending, write{string(key), bytes.Clone(value)})
	return nil
}

func (s *memStore) SyncKeyValue() error {
	if err := s.step(); err != nil {
		return err
	}
	for _, w := range s.pending {
		s.durable[w.key] = w.value
	}
	s.pending = nil
	return nil
}

// powerLoss keeps pending write i when bit i of keep is set.
func (s *memStore) powerLoss(keep uint64) *memStore {
	out := &memStore{durable: maps.Clone(s.durable)}
	for i, w := range s.pending {
		if i < 64 && keep&(1<<i) != 0 {
			out.durable[w.key] = w.value
		}
	}
	return out
}

// storePins is a pin ledger in the same store: a pin record is the parent
// root, synced; a released pin is an empty record. The native ledger is
// adapters/<client>/state_provider; its durability is proven in E01-02.pins.
type storePins struct{ db *memStore }

func pinKey(id string) []byte { return []byte("extractor-test-pin-v1/" + id) }

func (p *storePins) pin(id, root string) error {
	if err := p.db.Put(pinKey(id), []byte(root)); err != nil {
		return err
	}
	return p.db.SyncKeyValue()
}

func (p *storePins) Verify(id, root string) (string, error) {
	value, ok := p.db.view(string(pinKey(id)))
	if !ok || len(value) == 0 {
		return "", fmt.Errorf("no pin %s", id)
	}
	if string(value) != root {
		return "", fmt.Errorf("pin %s holds another root", id)
	}
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:]), nil
}

func (p *storePins) Release(id string) error {
	if err := p.db.Put(pinKey(id), nil); err != nil {
		return err
	}
	return p.db.SyncKeyValue()
}

type node struct {
	t     *testing.T
	v     vectors
	db    *memStore
	pins  *storePins
	token *admission.Token
	a     *admission.Admission
}

func open(t *testing.T, v vectors, db *memStore, token *admission.Token) (*node, error) {
	pins := &storePins{db: db}
	a, err := admission.Open(token, db, admission.Producer{ProducerID: v.Producer, RunID: v.Run}, pins, Inputs{DB: db})
	return &node{t: t, v: v, db: db, pins: pins, token: token, a: a}, err
}

// observe runs one observation from Begin to ExecReady and returns the
// EXEC_READY record. fetches counts calls of the fetch function.
func (n *node) observe(fetches *int) (admission.ExecReadyRecord, error) {
	if err := n.a.GoLive(n.token); err != nil {
		return admission.ExecReadyRecord{}, err
	}
	obs, err := n.a.Begin(n.token, n.v.Candidate)
	if err != nil {
		return admission.ExecReadyRecord{}, err
	}
	if err := n.pins.pin(obs.Key, n.v.Candidate.ParentRoot); err != nil {
		return admission.ExecReadyRecord{}, err
	}
	if _, err := sourceinputs.SealCapsule(n.db, obs.Key, items); err != nil {
		return admission.ExecReadyRecord{}, err
	}
	if _, err := n.a.Admit(n.token, obs.Key); err != nil {
		return admission.ExecReadyRecord{}, err
	}
	return n.execute(obs.Key, fetches)
}

// execute consumes both dependencies through the log, closes it and writes
// EXEC_READY. A replayed request never calls fetch.
func (n *node) execute(key string, fetches *int) (admission.ExecReadyRecord, error) {
	log, err := sourceinputs.OpenLog(n.db, key)
	if err != nil {
		return admission.ExecReadyRecord{}, err
	}
	for _, step := range []struct{ request, response []byte }{{request0, response0}, {request1, response1}} {
		got, err := log.Next(step.request, func() ([]byte, error) { *fetches++; return step.response, nil })
		if err != nil {
			return admission.ExecReadyRecord{}, err
		}
		if !bytes.Equal(got, step.response) {
			return admission.ExecReadyRecord{}, fmt.Errorf("log returned other bytes")
		}
	}
	if _, err := log.Finish(); err != nil {
		return admission.ExecReadyRecord{}, err
	}
	return n.a.ExecReady(n.token, key, n.v.ExecDigest)
}

// TestRealBinding binds the EXEC_READY record to the closed consumed
// manifest of the real source_inputs log (D98 item 7).
func TestRealBinding(t *testing.T) {
	v := loadVectors(t)
	token := admission.NewGuard().Acquire()
	n, err := open(t, v, newStore(), token)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := n.a.GoLive(token); err != nil {
		t.Fatal(err)
	}
	obs, err := n.a.Begin(token, v.Candidate)
	if err != nil || obs.Key != v.Key {
		t.Fatalf("begin %+v %v", obs, err)
	}
	if _, err := n.a.Admit(token, obs.Key); !errors.Is(err, admission.ErrBinding) {
		t.Fatalf("admit without pin or capsule: %v", err)
	}
	if err := n.pins.pin(obs.Key, v.Candidate.ParentRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := n.a.Admit(token, obs.Key); !errors.Is(err, admission.ErrBinding) {
		t.Fatalf("admit without capsule: %v", err)
	}
	if _, err := sourceinputs.SealCapsule(n.db, obs.Key, items); err != nil {
		t.Fatal(err)
	}
	admitted, err := n.a.Admit(token, obs.Key)
	if err != nil || admitted.CapsuleDigest != v.Binding["capsule_digest"] || admitted.PinDigest != v.Binding["pin_digest"] {
		t.Fatalf("admit %+v %v, want capsule %s", admitted, err, v.Binding["capsule_digest"])
	}
	log, err := sourceinputs.OpenLog(n.db, obs.Key)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ request, response []byte }{{request0, response0}, {request1, response1}} {
		if _, err := log.Next(step.request, func() ([]byte, error) { return step.response, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := n.a.ExecReady(token, obs.Key, v.ExecDigest); !errors.Is(err, admission.ErrBinding) ||
		!errors.Is(err, ErrNotClosed) {
		t.Fatalf("EXEC_READY before the log is closed: %v", err)
	}
	if _, err := (Inputs{DB: n.db}).Consumed(obs.Key); !errors.Is(err, ErrNotClosed) {
		t.Fatalf("consumed of an open log: %v", err)
	}
	manifest, err := log.Finish()
	if err != nil || manifest.Digest != v.Binding["consumed_digest"] {
		t.Fatalf("finish %+v %v", manifest, err)
	}
	ready, err := n.a.ExecReady(token, obs.Key, v.ExecDigest)
	if err != nil {
		t.Fatalf("exec ready: %v", err)
	}
	if admitted.Digest != v.Binding["admitted_digest"] || ready.ConsumedDigest != v.Binding["consumed_digest"] ||
		ready.Digest != v.Binding["exec_ready_digest"] {
		t.Fatalf("digests: admitted %s, consumed %s, exec ready %s; want %v", admitted.Digest, ready.ConsumedDigest,
			ready.Digest, v.Binding)
	}
	if _, err := (Inputs{DB: n.db}).CapsuleDigest("other"); !errors.Is(err, sourceinputs.ErrNoCapsule) {
		t.Fatalf("capsule of an unknown observation: %v", err)
	}
	if _, err := (Inputs{DB: n.db}).Consumed("other"); !errors.Is(err, sourceinputs.ErrNoCapsule) {
		t.Fatalf("consumed of an unknown observation: %v", err)
	}
}

// TestPowerLossAtEveryStep injects a power loss at every store operation of
// one observation, from Begin to EXEC_READY, with several subsets of the
// unsynced writes kept. EXEC_READY can survive only a power loss in its own
// sync, as the complete record. Recovery then follows the protocol: an
// INPUT_PREPARING observation is abandoned and a new one starts; an ADMITTED
// observation is completed in RECOVERY_ONLY from its saved inputs, without
// fetching a durable response again, and its EXEC_READY record equals the
// clean run (AC-CAP-02-01 and AC-CAP-02-02, unit form with the real log).
func TestPowerLossAtEveryStep(t *testing.T) {
	v := loadVectors(t)
	token := admission.NewGuard().Acquire()
	clean, err := open(t, v, newStore(), token)
	if err != nil {
		t.Fatal(err)
	}
	cleanFetches := 0
	want, err := clean.observe(&cleanFetches)
	if err != nil || want.Digest != v.Binding["exec_ready_digest"] || cleanFetches != 2 {
		t.Fatalf("clean run %+v %v, %d fetches", want, err, cleanFetches)
	}
	total := clean.db.ops
	admittedSeen, preparingSeen, durableSeen := 0, 0, 0
	for crash := 2; crash <= total; crash++ {
		for _, keep := range []uint64{0, 1, 0x5555555555555555, ^uint64(0)} {
			db := newStore()
			first, err := open(t, v, db, token)
			if err != nil {
				t.Fatal(err)
			}
			db.crashAt = crash
			fetches := 0
			if _, err := first.observe(&fetches); !errors.Is(err, errCrash) {
				t.Fatalf("crash %d: %v", crash, err)
			}
			reopened := db.powerLoss(keep)
			n, err := open(t, v, reopened, token)
			if err != nil {
				t.Fatalf("crash %d keep %x: reopen: %v", crash, keep, err)
			}
			list, err := n.a.Observations(token)
			if err != nil || len(list) > 1 {
				t.Fatalf("crash %d keep %x: %+v %v", crash, keep, list, err)
			}
			if len(list) == 0 || list[0].State == admission.InputPreparing {
				preparingSeen++
				if len(list) == 1 {
					if err := n.a.Abandon(token, list[0].Key); err != nil {
						t.Fatalf("crash %d: abandon: %v", crash, err)
					}
				}
				fetches = 0
				ready, err := n.observe(&fetches)
				if err != nil || fetches != 2 || ready.Sequence != uint64(len(list)) {
					t.Fatalf("crash %d keep %x: fresh observation %+v %v", crash, keep, ready, err)
				}
				continue
			}
			admittedSeen++
			obs := list[0]
			if obs.State == admission.ExecReady {
				// Only the last operation, the EXEC_READY sync, can leave a
				// surviving EXEC_READY write; it is then the complete record.
				if crash != total || *obs.ExecReady != want {
					t.Fatalf("crash %d keep %x: EXEC_READY %+v", crash, keep, *obs.ExecReady)
				}
				continue
			}
			if err := n.a.GoLive(token); !errors.Is(err, admission.ErrPending) {
				t.Fatalf("crash %d: go live with an ADMITTED obligation: %v", crash, err)
			}
			if *obs.Admitted != (admission.AdmittedRecord{Observation: v.Key, Sequence: 0, Candidate: v.Candidate,
				PinDigest: v.Binding["pin_digest"], CapsuleDigest: v.Binding["capsule_digest"],
				Digest: v.Binding["admitted_digest"]}) {
				t.Fatalf("crash %d: recovered %+v", crash, *obs.Admitted)
			}
			saved, err := sourceinputs.OpenLog(reopened, obs.Key)
			if err != nil {
				t.Fatalf("crash %d keep %x: open log: %v", crash, keep, err)
			}
			durable := int(saved.Frontier())
			fetches = 0
			ready, err := n.execute(obs.Key, &fetches)
			if err != nil {
				t.Fatalf("crash %d keep %x: recovery: %v", crash, keep, err)
			}
			if fetches != 2-durable {
				t.Fatalf("crash %d keep %x: recovery fetched %d responses with %d durable", crash, keep, fetches, durable)
			}
			if durable > 0 {
				durableSeen++
			}
			if ready != want {
				t.Fatalf("crash %d keep %x: recovered EXEC_READY %+v, want %+v", crash, keep, ready, want)
			}
			if err := n.a.GoLive(token); err != nil {
				t.Fatalf("crash %d: go live after recovery: %v", crash, err)
			}
		}
	}
	if admittedSeen == 0 || preparingSeen == 0 || durableSeen == 0 {
		t.Fatalf("crash points reached ADMITTED %d times (%d with durable responses) and INPUT_PREPARING %d times",
			admittedSeen, durableSeen, preparingSeen)
	}
}
