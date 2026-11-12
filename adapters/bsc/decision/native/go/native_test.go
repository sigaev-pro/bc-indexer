package bscdecision

// The native probe of the BSC decision block on the accepted private BSC
// Parlia fixture (genesis and blocks 1 and 2). TestNativeBSCDecision runs
// the live import, the crash points of AC-CAP-07-01, 07-02, 07-03 and 07-06
// in a child process that kills itself, the restart with the recovery
// driver, and the re-import of blocks already in the chain. It writes
// report.json and one trace per scenario for the driver
// (test/harness/bsc_decision.py), which checks them with its own rules.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/parlia"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/bsc"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	envInputs     = "EXTRACTOR_DECISION_INPUTS"
	envCommit     = "EXTRACTOR_DECISION_NATIVE_COMMIT"
	envWork       = "EXTRACTOR_DECISION_WORK"
	envChildMode  = "EXTRACTOR_DECISION_CHILD_MODE"
	envChildHome  = "EXTRACTOR_DECISION_CHILD_HOME"
	envChildTrace = "EXTRACTOR_DECISION_CHILD_TRACE"
	probeStoreID  = "0123456789abcdef0123456789abcdef"
	// mirrorPrefix holds the applied decisions of the probe mirror.
	mirrorPrefix = "extractor-probe-mirror-v1/"
)

var probeProducer = admission.Producer{ProducerID: "bsc-probe", RunID: "run-1"}

// Crash points of the child, during the import of block 1, whose state
// differs from the genesis state (blocks 1 and 2 have the same state root).
// The suffix names the store model of the extractor records: "-drop" loses
// every unsynced write (power loss); the others keep what pebble wrote and
// synced. A SIGKILL can lose native writes that pebble has not synced, so
// at the native write points the child syncs the native store first: the
// outcome in which the native writes before the point reached the disk.
var crashModes = []string{
	"first-handoff",            // AC-CAP-07-01: ADMITTED, one durable dependency response
	"exec-ready-unsynced-drop", // AC-CAP-07-01: EXEC_READY written, not synced, lost
	"exec-ready-synced",        // AC-CAP-07-02: EXEC_READY durable, child state volatile
	"barrier-synced",           // AC-CAP-07-02: child root flushed and synced, no intent
	"intent-synced",            // AC-CAP-07-03: durable intent, no native write
	"block-written",            // AC-CAP-07-03: block batch written, head and marker not
	"head-written",             // AC-CAP-07-03: head batch with the marker written, NATIVE_DECIDED not
	"decided-unsynced-drop",    // marker durable, NATIVE_DECIDED written and lost
	"native-rejection",         // AC-CAP-07-06: durable intent, native bad-block record
}

func probeEnv(t testing.TB, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}

type fixture struct {
	genesisFile []byte
	genesis     *core.Genesis
	blocks      types.Blocks
	commit      string
}

func loadFixture(t testing.TB) fixture {
	t.Helper()
	inputs := probeEnv(t, envInputs)
	fx := fixture{commit: probeEnv(t, envCommit)}
	var err error
	if fx.genesisFile, err = os.ReadFile(filepath.Join(inputs, "genesis.json")); err != nil {
		t.Fatal(err)
	}
	fx.genesis = new(core.Genesis)
	if err := json.Unmarshal(fx.genesisFile, fx.genesis); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(inputs, "blocks.rlp"))
	if err != nil {
		t.Fatal(err)
	}
	stream := rlp.NewStream(bytes.NewReader(data), 0)
	for {
		block := new(types.Block)
		if err := stream.Decode(block); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		fx.blocks = append(fx.blocks, block)
	}
	if len(fx.blocks) != 2 || fx.blocks[0].NumberU64() != 1 || fx.blocks[1].NumberU64() != 2 {
		t.Fatalf("fixture blocks %d", len(fx.blocks))
	}
	return fx
}

// ---- trace ----

// tracer appends one JSON line per event with a direct write, so the lines
// survive a SIGKILL of the writing process. on, when set, runs after the
// line is written.
type tracer struct {
	mu   sync.Mutex
	file *os.File
	on   func(bsc.Event)
}

func newTracer(t testing.TB, path string) *tracer {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return &tracer{file: file}
}

func (tr *tracer) emit(event bsc.Event) {
	tr.mu.Lock()
	line, err := json.Marshal(event)
	if err == nil {
		_, err = tr.file.Write(append(line, '\n'))
	}
	on := tr.on
	tr.mu.Unlock()
	if err != nil {
		panic(fmt.Sprintf("trace write: %v", err))
	}
	if on != nil {
		on(event)
	}
}

// ---- power-loss store model ----

// recordKind names the extractor record of a key: its scope (admission,
// decision, inputs or run), the observation or decision it belongs to, and
// its kind.
func recordKind(key []byte) (scope, owner, kind string) {
	k := string(key)
	for _, prefix := range []struct{ name, text string }{{"admission", "extractor-admission-v1/"},
		{"decision", "extractor-decision-v1/"}, {"inputs", "extractor-source-inputs-v1/"}} {
		if !strings.HasPrefix(k, prefix.text) {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(k, prefix.text), "/")
		switch {
		case len(parts) == 2:
			return prefix.name, parts[0], parts[1]
		case len(parts) >= 3:
			return prefix.name, parts[0] + "/" + parts[1], parts[len(parts)-1]
		}
		return prefix.name, "", k
	}
	return "other", "", k
}

const (
	modelNative = "native"
	modelDrop   = "drop"
)

func storeModel(mode string) string {
	if strings.HasSuffix(mode, "-drop") {
		return modelDrop
	}
	return modelNative
}

// probeStore holds the extractor records under a store model. In the drop
// model a Put or Delete stays in memory until SyncKeyValue.
type probeStore struct {
	db      ethdb.Database
	model   string
	emit    func(bsc.Event)
	mu      sync.Mutex
	pending map[string][]byte // nil value: a pending delete
	order   []string
	last    [3]string
	// afterPut runs after each Put; afterSync after a sync whose last Put
	// had this scope, owner and kind.
	afterPut, afterSync func(scope, owner, kind string)
}

func (s *probeStore) Has(key []byte) (bool, error) {
	s.mu.Lock()
	value, ok := s.pending[string(key)]
	s.mu.Unlock()
	if ok {
		return value != nil, nil
	}
	return s.db.Has(key)
}

func (s *probeStore) Get(key []byte) ([]byte, error) {
	s.mu.Lock()
	value, ok := s.pending[string(key)]
	s.mu.Unlock()
	if ok {
		if value == nil {
			return nil, errors.New("not found")
		}
		return bytes.Clone(value), nil
	}
	return s.db.Get(key)
}

func (s *probeStore) write(key, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.model != modelDrop {
		if value == nil {
			return s.db.Delete(key)
		}
		return s.db.Put(key, value)
	}
	if _, ok := s.pending[string(key)]; !ok {
		s.order = append(s.order, string(key))
	}
	s.pending[string(key)] = value
	return nil
}

func (s *probeStore) Put(key, value []byte) error {
	scope, owner, kind := recordKind(key)
	s.emit(bsc.Event{"event": "put", "scope": scope, "owner": owner, "record": kind})
	if err := s.write(key, bytes.Clone(value)); err != nil {
		return err
	}
	s.mu.Lock()
	s.last = [3]string{scope, owner, kind}
	s.mu.Unlock()
	if s.afterPut != nil {
		s.afterPut(scope, owner, kind)
	}
	return nil
}

func (s *probeStore) Delete(key []byte) error {
	return s.write(key, nil)
}

func (s *probeStore) SyncKeyValue() error {
	s.mu.Lock()
	if len(s.order) != 0 {
		batch := s.db.NewBatch()
		for _, key := range s.order {
			if value := s.pending[key]; value == nil {
				batch.Delete([]byte(key))
			} else {
				batch.Put([]byte(key), value)
			}
		}
		if err := batch.Write(); err != nil {
			s.mu.Unlock()
			return err
		}
		s.pending, s.order = map[string][]byte{}, nil
	}
	last := s.last
	s.last = [3]string{}
	s.mu.Unlock()
	if err := s.db.SyncKeyValue(); err != nil {
		return err
	}
	s.emit(bsc.Event{"event": "sync"})
	if s.afterSync != nil && last[0] != "" {
		s.afterSync(last[0], last[1], last[2])
	}
	return nil
}

// ---- probe mirror ----

// probeMirror is a stand-in for the decision mirror of E05-01.registry: it
// saves the applied decisions in the native store, synced.
type probeMirror struct{ db ethdb.Database }

func (m probeMirror) key(sequence uint64) []byte {
	return []byte(fmt.Sprintf("%s%016x", mirrorPrefix, sequence))
}

func (m probeMirror) Applied(sequence uint64, digest string) (bool, error) {
	data, err := m.db.Get(m.key(sequence))
	if err != nil {
		return false, nil
	}
	return string(data) == digest, nil
}

func (m probeMirror) apply(sequence uint64, digest string) error {
	if err := m.db.Put(m.key(sequence), []byte(digest)); err != nil {
		return err
	}
	return m.db.SyncKeyValue()
}

// ---- node ----

type node struct {
	t      testing.TB
	home   string
	db     ethdb.Database
	chain  *core.BlockChain
	hook   *Hook
	store  *probeStore
	mirror probeMirror
}

func openDisk(t testing.TB, home string) ethdb.Database {
	t.Helper()
	chaindata := filepath.Join(home, "geth", "chaindata")
	kv, err := pebble.New(chaindata, 64, 64, "", false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := rawdb.Open(kv, rawdb.OpenOptions{Ancient: filepath.Join(chaindata, "ancient")})
	if err != nil {
		kv.Close()
		t.Fatal(err)
	}
	return db
}

// startNode opens the home with the capture profile of E01-02.pins and the
// Parlia engine with the system-call ethAPI, starts the decision hook and
// installs it; it returns before recovery.
func startNode(t testing.TB, fx fixture, home, model string, emit func(bsc.Event)) (*node, error) {
	n := &node{t: t, home: home}
	n.db = openDisk(t, home)
	n.store = &probeStore{db: n.db, model: model, emit: emit, pending: map[string][]byte{}}
	n.mirror = probeMirror{db: n.db}
	config := core.DefaultConfig()
	config.StateScheme, config.ArchiveMode, config.SnapshotLimit = rawdb.HashScheme, false, 0
	api, backend := bsc.NewSystemCallAPI()
	engine := parlia.New(fx.genesis.Config, n.db, api, fx.genesis.ToBlock().Hash())
	chain, err := core.NewBlockChain(n.db, fx.genesis, engine, config)
	if err != nil {
		n.db.Close()
		return nil, err
	}
	n.chain = chain
	backend.Bind(chain, nil)
	identity := stateprovider.StoreIdentity{StoreID: probeStoreID, ChainID: fx.genesis.Config.ChainID.String(),
		GenesisHash: chain.Genesis().Hash(), StateScheme: rawdb.HashScheme}
	hook, err := Start(admission.NewGuard(), n.db, chain, Config{Seam: bsc.Config{Producer: probeProducer,
		Identity: identity, GenesisFile: fx.genesisFile, NativeCommit: fx.commit, Store: n.store, Emit: emit},
		Mirror: n.mirror})
	if err != nil {
		n.close()
		return nil, err
	}
	n.hook = hook
	backend.Bind(chain, hook.Seam())
	chain.SetExtractorHook(hook)
	return n, nil
}

func (n *node) close() {
	if n.chain != nil {
		n.chain.Stop()
	}
	n.db.Close()
}

func (n *node) guarded(f func(adm *admission.Admission, token *admission.Token) error) {
	release := n.hook.Acquire()
	defer release()
	adm, token := n.hook.Seam().Admission()
	if err := f(adm, token); err != nil {
		n.t.Fatal(err)
	}
}

func (n *node) mode() string {
	var mode admission.Mode
	n.guarded(func(adm *admission.Admission, token *admission.Token) (err error) {
		mode, err = adm.Mode(token)
		return err
	})
	return string(mode)
}

// prefixed returns every record under prefix, by the rest of its key.
func (n *node) prefixed(prefix string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	it := n.db.NewIterator([]byte(prefix), nil)
	defer it.Release()
	for it.Next() {
		value := bytes.Clone(it.Value())
		if !json.Valid(value) {
			value, _ = json.Marshal(hex.EncodeToString(value))
		}
		out[strings.TrimPrefix(string(it.Key()), prefix)] = json.RawMessage(value)
	}
	return out
}

type observationRow struct {
	Key       string              `json:"key"`
	State     string              `json:"state"`
	Candidate admission.Candidate `json:"candidate"`
}

type obligationRow struct {
	Sequence uint64 `json:"sequence"`
	Intent   string `json:"intent_digest"`
	Decided  string `json:"decided_digest"`
}

// scenario is one scenario of the report.
type scenario struct {
	Name           string                     `json:"name"`
	RecoverError   string                     `json:"recover_error,omitempty"`
	ModeAtStart    string                     `json:"mode_at_start,omitempty"`
	BeforeRecovery []observationRow           `json:"observations_after_restart,omitempty"`
	RootAtRestart  *bool                      `json:"block1_root_on_disk_at_restart,omitempty"`
	HeadAtRestart  *uint64                    `json:"head_at_restart,omitempty"`
	ImportErrors   []string                   `json:"import_errors"`
	Mode           string                     `json:"mode"`
	Observations   []observationRow           `json:"observations"`
	Admission      map[string]json.RawMessage `json:"admission_records"`
	Decisions      map[string]json.RawMessage `json:"decision_records"`
	Markers        map[string]json.RawMessage `json:"markers"`
	Obligations    []obligationRow            `json:"obligations"`
	Capsules       []string                   `json:"capsules"`
	Pins           []string                   `json:"pins"`
	BadBlocks      []string                   `json:"bad_blocks"`
	HeadNumber     uint64                     `json:"head_number"`
	HeadHash       string                     `json:"head_hash"`
	Finalized      uint64                     `json:"finalized_number"`
	Reimport       map[string]any             `json:"reimport,omitempty"`
	Extra          map[string]string          `json:"extra,omitempty"`
}

func (n *node) finish(sc *scenario) {
	sc.Mode = n.mode()
	n.guarded(func(adm *admission.Admission, token *admission.Token) error {
		observations, err := adm.Observations(token)
		if err != nil {
			return err
		}
		sc.Observations = []observationRow{}
		for _, obs := range observations {
			sc.Observations = append(sc.Observations, observationRow{Key: obs.Key, State: string(obs.State),
				Candidate: obs.Candidate})
		}
		obligations, err := n.hook.Bridge().Obligations(token)
		sc.Obligations = []obligationRow{}
		for _, o := range obligations {
			sc.Obligations = append(sc.Obligations, obligationRow{Sequence: o.Decided.Sequence,
				Intent: o.Intent.Digest, Decided: o.Decided.Digest})
		}
		return err
	})
	run := probeProducer.ProducerID + "." + probeProducer.RunID + "/"
	sc.Admission = n.prefixed("extractor-admission-v1/" + run)
	sc.Decisions = n.prefixed("extractor-decision-v1/" + run)
	sc.Markers = n.prefixed("extractor-decision-marker-v1/" + run)
	sc.Capsules = []string{}
	for key := range n.prefixed("extractor-source-inputs-v1/") {
		if strings.HasSuffix(key, "/capsule") {
			sc.Capsules = append(sc.Capsules, strings.TrimSuffix(key, "/capsule"))
		}
	}
	sort.Strings(sc.Capsules)
	sc.Pins = []string{}
	for key := range n.prefixed(bsc.PinRecordPrefix) {
		sc.Pins = append(sc.Pins, key)
	}
	sort.Strings(sc.Pins)
	sc.BadBlocks = []string{}
	for _, block := range rawdb.ReadAllBadBlocks(n.db) {
		sc.BadBlocks = append(sc.BadBlocks, hex.EncodeToString(block.Hash().Bytes()))
	}
	head := n.chain.CurrentBlock()
	sc.HeadNumber, sc.HeadHash = head.Number.Uint64(), hex.EncodeToString(head.Hash().Bytes())
	if final := n.chain.CurrentFinalBlock(); final != nil {
		sc.Finalized = final.Number.Uint64()
	}
}

func counters(n *node) (uint64, uint64) {
	read := func(key string) uint64 {
		var counter struct {
			Next uint64 `json:"next"`
		}
		if data, err := n.db.Get([]byte(key)); err == nil {
			json.Unmarshal(data, &counter)
		}
		return counter.Next
	}
	run := probeProducer.ProducerID + "." + probeProducer.RunID
	return read("extractor-admission-v1/" + run + "/counter"), read("extractor-decision-v1/" + run + "/counter")
}

// reimport imports both fixture blocks again after a restart: blocks that
// are in the chain with their state open no observation and no decision.
func reimport(t *testing.T, fx fixture, home string, tr *tracer) map[string]any {
	t.Helper()
	tr.emit(bsc.Event{"event": "restart"})
	n, err := startNode(t, fx, home, modelNative, tr.emit)
	if err != nil {
		t.Fatal(err)
	}
	defer n.close()
	if err := n.hook.Recover(n.chain); err != nil {
		t.Fatal(err)
	}
	observationsBefore, decisionsBefore := counters(n)
	head := n.chain.CurrentBlock().Number.Uint64()
	_, err = n.chain.InsertChain(fx.blocks[:head])
	observationsAfter, decisionsAfter := counters(n)
	out := map[string]any{"error": errText(err), "blocks": head,
		"observation_counter": []uint64{observationsBefore, observationsAfter},
		"decision_counter":    []uint64{decisionsBefore, decisionsAfter},
		"head":                n.chain.CurrentBlock().Number.Uint64()}
	if err != nil || observationsAfter != observationsBefore || decisionsAfter != decisionsBefore {
		t.Fatalf("re-import of blocks in the chain: %v", out)
	}
	return out
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---- child ----

func TestNativeBSCDecisionChild(t *testing.T) {
	mode := os.Getenv(envChildMode)
	if mode == "" {
		t.Skip("runs only as a crash child of TestNativeBSCDecision")
	}
	fx := loadFixture(t)
	home := probeEnv(t, envChildHome)
	tr := newTracer(t, probeEnv(t, envChildTrace))
	n, err := startNode(t, fx, home, storeModel(mode), tr.emit)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.hook.Recover(n.chain); err != nil {
		t.Fatal(err)
	}
	block1 := admission.ObservationKey(probeProducer, 0)
	run := probeProducer.ProducerID + "." + probeProducer.RunID
	crash := func(point string) {
		tr.emit(bsc.Event{"event": "crash", "observation": block1, "point": point})
		syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	}
	onEvent := func(name string, f func()) {
		tr.on = func(event bsc.Event) {
			if event["event"] == name && (event["observation"] == block1 || event["number"] == uint64(1)) {
				f()
			}
		}
	}
	// syncedCrash syncs the native store, then crashes.
	syncedCrash := func(point string) func() {
		return func() {
			if err := n.db.SyncKeyValue(); err != nil {
				t.Fatal(err)
			}
			crash(point)
		}
	}
	switch mode {
	case "first-handoff":
		n.hook.Seam().Handoff = func(obs string, seq uint64, _ []byte) {
			if obs == block1 && seq == 0 {
				crash(mode)
			}
		}
	case "exec-ready-unsynced-drop":
		n.store.afterPut = func(scope, owner, kind string) {
			if scope == "admission" && owner == run+"/0000000000000000" && kind == "exec-ready" {
				crash(mode)
			}
		}
	case "exec-ready-synced":
		n.store.afterSync = func(scope, owner, kind string) {
			if scope == "admission" && owner == run+"/0000000000000000" && kind == "exec-ready" {
				crash(mode)
			}
		}
	case "barrier-synced":
		onEvent("barrier", func() { crash(mode) })
	case "intent-synced":
		n.store.afterSync = func(scope, owner, kind string) {
			if scope == "decision" && owner == run && kind == "counter" {
				crash(mode)
			}
		}
	case "block-written":
		onEvent("native-block", syncedCrash(mode))
	case "head-written":
		onEvent("native-head", syncedCrash(mode))
	case "decided-unsynced-drop":
		n.store.afterPut = func(scope, owner, kind string) {
			if scope == "decision" && owner == run+"/0000000000000000" && kind == "decided" {
				crash(mode)
			}
		}
	case "native-rejection":
		// The native outcome fixture of AC-CAP-07-06: after the durable
		// intent, the native store records block 1 as a bad block (the
		// record of reportBadBlock), then the process ends before the
		// adapter reconciles.
		onEvent("intent", func() {
			rawdb.WriteBadBlock(n.db, fx.blocks[0])
			if err := n.db.SyncKeyValue(); err != nil {
				t.Fatal(err)
			}
			tr.emit(bsc.Event{"event": "native-bad-block", "observation": block1})
			crash(mode)
		})
	default:
		t.Fatalf("unknown mode %s", mode)
	}
	_, err = n.chain.InsertChain(fx.blocks[:1])
	t.Fatalf("block 1 ended without the crash: %v", err)
}

func runChild(t *testing.T, work, mode, home, trace string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeBSCDecisionChild$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), envChildMode+"="+mode, envChildHome+"="+home, envChildTrace+"="+trace)
	output, err := cmd.CombinedOutput()
	os.WriteFile(filepath.Join(work, "child-"+mode+".out"), output, 0o644)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("child %s did not end with SIGKILL: %v\n%s", mode, err, output)
	}
}

// ---- parent ----

func TestNativeBSCDecision(t *testing.T) {
	fx := loadFixture(t)
	work := probeEnv(t, envWork)
	scenarios := []*scenario{}
	trace := func(name string) *tracer { return newTracer(t, filepath.Join(work, "trace-"+name+".jsonl")) }

	// 1. Live import: each block gets one IMPORT decision with one
	// obligation; the mirror applies block 1, and Cleanup removes its marker.
	{
		tr := trace("live")
		home := filepath.Join(work, "home-live")
		n, err := startNode(t, fx, home, modelNative, tr.emit)
		if err != nil {
			t.Fatal(err)
		}
		sc := &scenario{Name: "live", ModeAtStart: n.mode(), Extra: map[string]string{}}
		// A block before recovery is refused: the bridge is not open and
		// admission is RECOVERY_ONLY.
		_, err = n.chain.InsertChain(fx.blocks[:1])
		sc.Extra["import_before_recovery"] = errText(err)
		if !errors.Is(err, admission.ErrMode) {
			t.Fatalf("import before recovery: %v", err)
		}
		if err := n.hook.Recover(n.chain); err != nil {
			t.Fatal(err)
		}
		_, err = n.chain.InsertChain(fx.blocks)
		sc.ImportErrors = []string{errText(err)}
		if err != nil {
			t.Fatal(err)
		}
		var decided string
		n.guarded(func(adm *admission.Admission, token *admission.Token) error {
			obligations, err := n.hook.Bridge().Obligations(token)
			if err != nil || len(obligations) != 2 {
				return fmt.Errorf("obligations %d: %v", len(obligations), err)
			}
			decided = obligations[0].Decided.Digest
			if err := n.mirror.apply(0, decided); err != nil {
				return err
			}
			return n.hook.Bridge().Cleanup(token)
		})
		sc.Extra["applied_decision_0"] = decided
		n.finish(sc)
		n.close()
		sc.Reimport = reimport(t, fx, home, tr)
		scenarios = append(scenarios, sc)
	}

	// 2. Crash points of block 1, each followed by restart, recovery and the
	// live import of both blocks.
	for _, mode := range crashModes {
		tr := trace(mode)
		home := filepath.Join(work, "home-"+mode)
		runChild(t, work, mode, home, filepath.Join(work, "trace-"+mode+".jsonl"))
		tr.emit(bsc.Event{"event": "restart"})
		n, err := startNode(t, fx, home, modelNative, tr.emit)
		if err != nil {
			t.Fatalf("%s: restart: %v", mode, err)
		}
		sc := &scenario{Name: mode, ModeAtStart: n.mode(), Extra: map[string]string{}}
		n.guarded(func(adm *admission.Admission, token *admission.Token) error {
			observations, err := adm.Observations(token)
			for _, obs := range observations {
				sc.BeforeRecovery = append(sc.BeforeRecovery, observationRow{Key: obs.Key, State: string(obs.State),
					Candidate: obs.Candidate})
			}
			return err
		})
		onDisk := rawdb.HasLegacyTrieNode(n.db, fx.blocks[0].Root())
		sc.RootAtRestart = &onDisk
		headAtRestart := n.chain.CurrentBlock().Number.Uint64()
		sc.HeadAtRestart = &headAtRestart
		sc.RecoverError = errText(n.hook.Recover(n.chain))
		if sc.RecoverError != "" {
			t.Fatalf("%s: recovery: %s", mode, sc.RecoverError)
		}
		sc.ImportErrors = []string{}
		if mode != "native-rejection" {
			// Live import continues: block 1 is in the chain, block 2 is new.
			_, err = n.chain.InsertChain(fx.blocks)
			sc.ImportErrors = append(sc.ImportErrors, errText(err))
		}
		n.finish(sc)
		n.close()
		sc.Reimport = reimport(t, fx, home, tr)
		scenarios = append(scenarios, sc)
	}
	checkReport(t, fx, scenarios)
	data, err := json.MarshalIndent(map[string]any{"scenarios": scenarios}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "report.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// checkReport checks the outcome of every scenario; the driver checks the
// same report again with its own rules and digests.
func checkReport(t *testing.T, fx fixture, scenarios []*scenario) {
	t.Helper()
	hash := func(i int) string { return hex.EncodeToString(fx.blocks[i].Hash().Bytes()) }
	for _, sc := range scenarios {
		head, obligations, markers := 2, []uint64{0, 1}, []string{"0000000000000000", "0000000000000001"}
		decided := []string{"0000000000000000/decided", "0000000000000001/decided"}
		switch sc.Name {
		case "live":
			// The mirror applied decision 0; Cleanup removed its marker and
			// pruned its records.
			obligations, markers, decided = []uint64{1}, markers[1:], decided[1:]
		case "native-rejection":
			// RejectIntent is done at once: its records are pruned, and no
			// marker, obligation or NATIVE_DECIDED record exists.
			head, obligations, markers, decided = 0, nil, nil, nil
		}
		if sc.Mode != string(admission.Live) || int(sc.HeadNumber) != head {
			t.Errorf("%s: mode %s head %d", sc.Name, sc.Mode, sc.HeadNumber)
		}
		if head > 0 && sc.HeadHash != hash(head-1) {
			t.Errorf("%s: head hash %s", sc.Name, sc.HeadHash)
		}
		if len(sc.Observations) != 0 || len(sc.Capsules) != 0 || len(sc.Pins) != 0 {
			t.Errorf("%s: open %d, capsules %v, pins %v", sc.Name, len(sc.Observations), sc.Capsules, sc.Pins)
		}
		got := []uint64{}
		for _, o := range sc.Obligations {
			got = append(got, o.Sequence)
		}
		if fmt.Sprint(got) != fmt.Sprint(append([]uint64{}, obligations...)) {
			t.Errorf("%s: obligations %v", sc.Name, got)
		}
		gotMarkers := keysOf(sc.Markers)
		if fmt.Sprint(gotMarkers) != fmt.Sprint(append([]string{}, markers...)) {
			t.Errorf("%s: markers %v", sc.Name, gotMarkers)
		}
		gotDecided := []string{}
		for _, key := range keysOf(sc.Decisions) {
			if strings.HasSuffix(key, "/decided") || strings.HasSuffix(key, "/rejected") {
				gotDecided = append(gotDecided, key)
			}
		}
		if fmt.Sprint(gotDecided) != fmt.Sprint(append([]string{}, decided...)) {
			t.Errorf("%s: decision records %v", sc.Name, gotDecided)
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := []string{}
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
