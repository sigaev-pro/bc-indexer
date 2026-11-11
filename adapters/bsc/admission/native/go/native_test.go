package bsc

// Native probe of the BSC admission block on the accepted private BSC Parlia
// fixture (two blocks). The driver test/harness/bsc_admission.py runs
// TestNativeBSCAdmission; it re-runs the test binary as
// TestNativeBSCAdmissionChild for each crash point.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/parlia"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/bsc/retention"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	envInputs     = "EXTRACTOR_ADMISSION_INPUTS"
	envCommit     = "EXTRACTOR_ADMISSION_NATIVE_COMMIT"
	envWork       = "EXTRACTOR_ADMISSION_WORK"
	envChildMode  = "EXTRACTOR_ADMISSION_CHILD_MODE"
	envChildHome  = "EXTRACTOR_ADMISSION_CHILD_HOME"
	envChildTrace = "EXTRACTOR_ADMISSION_CHILD_TRACE"
	probeStoreID  = "0123456789abcdef0123456789abcdef"
)

var probeProducer = admission.Producer{ProducerID: "bsc-probe", RunID: "run-1"}

// Crash points of the child. Each name says where the child kills itself
// (SIGKILL) during the observation of block 2, and whether unsynced records
// are dropped (power-loss model) or kept (process crash on pebble).
var crashModes = []string{
	"before-admitted-put-drop", // AC-CAP-02-01: before the ADMITTED record is written
	"admitted-unsynced-drop",   // AC-CAP-02-01: ADMITTED written, not synced, lost
	"admitted-unsynced-keep",   // AC-CAP-02-01: ADMITTED written, not synced, kept
	"admitted-synced",          // AC-CAP-02-02: right after the closing ADMITTED sync
	"first-handoff",            // AC-CAP-02-02: after the first durable dependency response
	"exec-ready-unsynced-drop", // EXEC_READY written, not synced, lost
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

// altBlock is block 2 with one extra-data byte changed: another candidate at
// the same height whose seal no longer verifies.
func (fx fixture) altBlock() *types.Block {
	header := types.CopyHeader(fx.blocks[1].Header())
	header.Extra = bytes.Clone(header.Extra)
	header.Extra[0] ^= 0x01
	return types.NewBlockWithHeader(header).WithBody(*fx.blocks[1].Body())
}

// ---- trace ----

// tracer appends one JSON line per event with a direct write, so the lines
// survive a SIGKILL of the writing process.
type tracer struct {
	mu   sync.Mutex
	file *os.File
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

func (tr *tracer) emit(event Event) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	line, err := json.Marshal(event)
	if err == nil {
		_, err = tr.file.Write(append(line, '\n'))
	}
	if err != nil {
		panic(fmt.Sprintf("trace write: %v", err))
	}
}

// ---- power-loss store model ----

// RecordKind names the record that a key of the admission or source inputs
// store holds, and the observation it belongs to.
func RecordKind(key []byte) (string, string) {
	k := string(key)
	switch {
	case k == RunRecordKey:
		return "", "run"
	case strings.HasPrefix(k, "extractor-admission-v1/"):
		parts := strings.Split(strings.TrimPrefix(k, "extractor-admission-v1/"), "/")
		if len(parts) == 2 {
			return "", parts[1]
		}
		if len(parts) == 3 {
			return parts[0] + "." + parts[1], parts[2]
		}
	case strings.HasPrefix(k, "extractor-source-inputs-v1/"):
		parts := strings.Split(strings.TrimPrefix(k, "extractor-source-inputs-v1/"), "/")
		kind := "inputs-other"
		switch {
		case len(parts) == 2 && parts[1] == "capsule":
			kind = "capsule"
		case len(parts) == 4 && parts[1] == "capsule":
			kind = "capsule-chunk"
		case len(parts) == 3 && parts[1] == "log":
			kind = "entry"
		case len(parts) == 4 && parts[1] == "log":
			kind = "entry-chunk"
		case len(parts) == 2 && parts[1] == "frontier":
			kind = "frontier"
		case len(parts) == 2 && parts[1] == "closed":
			kind = "closed"
		}
		return parts[0], kind
	}
	return "", "other"
}

// Store models for the admission and source inputs records.
const (
	// modelNative passes writes to pebble; a SIGKILL may lose writes that
	// pebble still buffers in the process.
	modelNative = "native"
	// modelDrop keeps a Put in memory until SyncKeyValue, so a SIGKILL loses
	// every unsynced write (the worst case of a power loss).
	modelDrop = "drop"
	// modelKeep writes and syncs every Put at once, so a SIGKILL keeps every
	// unsynced write (the other extreme).
	modelKeep = "keep"
)

func storeModel(mode string) string {
	switch {
	case strings.HasSuffix(mode, "-drop"):
		return modelDrop
	case strings.HasSuffix(mode, "-keep"):
		return modelKeep
	}
	return modelNative
}

// probeStore holds the admission and source inputs records under a store
// model.
type probeStore struct {
	db      ethdb.Database
	model   string
	emit    func(Event)
	mu      sync.Mutex
	pending map[string][]byte
	order   []string
	lastPut string
	// beforePut and afterPut run with the observation and kind of each Put;
	// afterSync runs with the kind of the last Put before the sync.
	beforePut, afterPut func(observation, kind string)
	afterSync           func(lastKind, lastObservation string)
	lastObs             string
}

func (s *probeStore) Has(key []byte) (bool, error) {
	s.mu.Lock()
	_, ok := s.pending[string(key)]
	s.mu.Unlock()
	if ok {
		return true, nil
	}
	return s.db.Has(key)
}

func (s *probeStore) Get(key []byte) ([]byte, error) {
	s.mu.Lock()
	value, ok := s.pending[string(key)]
	s.mu.Unlock()
	if ok {
		return bytes.Clone(value), nil
	}
	return s.db.Get(key)
}

func (s *probeStore) Put(key, value []byte) error {
	observation, kind := RecordKind(key)
	if s.beforePut != nil {
		s.beforePut(observation, kind)
	}
	s.emit(Event{"event": "put", "observation": observation, "record": kind})
	s.mu.Lock()
	s.lastPut, s.lastObs = kind, observation
	if s.model == modelDrop {
		if _, ok := s.pending[string(key)]; !ok {
			s.order = append(s.order, string(key))
		}
		s.pending[string(key)] = bytes.Clone(value)
		s.mu.Unlock()
	} else {
		s.mu.Unlock()
		if err := s.db.Put(key, value); err != nil {
			return err
		}
		if s.model == modelKeep {
			if err := s.db.SyncKeyValue(); err != nil {
				return err
			}
			s.emit(Event{"event": "sync", "model": modelKeep})
		}
	}
	if s.afterPut != nil {
		s.afterPut(observation, kind)
	}
	return nil
}

func (s *probeStore) SyncKeyValue() error {
	s.mu.Lock()
	if len(s.order) != 0 {
		batch := s.db.NewBatch()
		for _, key := range s.order {
			batch.Put([]byte(key), s.pending[key])
		}
		if err := batch.Write(); err != nil {
			s.mu.Unlock()
			return err
		}
		s.pending, s.order = map[string][]byte{}, nil
	}
	last, lastObs := s.lastPut, s.lastObs
	s.lastPut = ""
	s.mu.Unlock()
	if err := s.db.SyncKeyValue(); err != nil {
		return err
	}
	s.emit(Event{"event": "sync"})
	if s.afterSync != nil && last != "" {
		s.afterSync(last, lastObs)
	}
	return nil
}

// ---- node ----

type node struct {
	t       testing.TB
	home    string
	db      ethdb.Database
	chain   *core.BlockChain
	backend *SystemCallBackend
	seam    *Seam
	store   *probeStore
	system  map[string]int
	outside int
	mu      sync.Mutex
	emit    func(Event)
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

// startNode opens the home with the capture profile of E01-02.pins, the
// Parlia engine with the system-call ethAPI and a tracer, installs the seam and
// returns before recovery. producer may be empty to open the saved run.
func startNode(t testing.TB, fx fixture, home string, producer admission.Producer, model string, emit func(Event)) (*node, error) {
	n := &node{t: t, home: home, system: map[string]int{}, emit: emit}
	n.db = openDisk(t, home)
	n.store = &probeStore{db: n.db, model: model, emit: emit, pending: map[string][]byte{}}
	hooks := &tracing.Hooks{OnSystemTxStart: func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		if n.seam != nil {
			if o := n.seam.active.Load(); o != nil {
				n.system[o.key]++
				emit(Event{"event": "system-tx", "observation": o.key})
				return
			}
		}
		n.outside++
	}}
	config := core.DefaultConfig()
	config.StateScheme, config.ArchiveMode, config.SnapshotLimit = rawdb.HashScheme, false, 0
	config.VmConfig.Tracer = hooks
	api, backend := NewSystemCallAPI()
	engine := parlia.New(fx.genesis.Config, n.db, api, fx.genesis.ToBlock().Hash())
	chain, err := core.NewBlockChain(n.db, fx.genesis, engine, config)
	if err != nil {
		n.db.Close()
		return nil, err
	}
	n.chain, n.backend = chain, backend
	backend.Bind(chain, nil)
	identity := stateprovider.StoreIdentity{StoreID: probeStoreID, ChainID: fx.genesis.Config.ChainID.String(),
		GenesisHash: chain.Genesis().Hash(), StateScheme: rawdb.HashScheme}
	seam, err := Start(admission.NewGuard(), n.db, chain, Config{Producer: producer, Identity: identity,
		GenesisFile: fx.genesisFile, NativeCommit: fx.commit, Store: n.store, Emit: emit})
	if err != nil {
		n.close()
		return nil, err
	}
	n.mu.Lock()
	n.seam = seam
	n.mu.Unlock()
	backend.Bind(chain, seam)
	chain.SetExtractorHook(seam)
	return n, nil
}

func (n *node) close() {
	if n.chain != nil {
		n.chain.Stop()
	}
	n.db.Close()
}

func (n *node) mode() admission.Mode {
	release := n.seam.Acquire()
	defer release()
	mode, err := n.seam.adm.Mode(n.seam.token)
	if err != nil {
		n.t.Fatal(err)
	}
	return mode
}

func (n *node) goLive() error {
	release := n.seam.Acquire()
	defer release()
	return n.seam.adm.GoLive(n.seam.token)
}

// observationRow is one observation in the report.
type observationRow struct {
	Key       string              `json:"key"`
	State     string              `json:"state"`
	Candidate admission.Candidate `json:"candidate"`
}

func (n *node) observations() []observationRow {
	observations, err := n.seam.Observations()
	if err != nil {
		n.t.Fatal(err)
	}
	rows := []observationRow{}
	for _, obs := range observations {
		rows = append(rows, observationRow{Key: obs.Key, State: string(obs.State), Candidate: obs.Candidate})
	}
	return rows
}

// records returns every admission record of the producer run, by key.
func (n *node) records() map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	prefix := []byte(fmt.Sprintf("extractor-admission-v1/%s.%s/", n.seam.producer.ProducerID, n.seam.producer.RunID))
	it := n.db.NewIterator(prefix, nil)
	defer it.Release()
	for it.Next() {
		out[strings.TrimPrefix(string(it.Key()), string(prefix))] = json.RawMessage(bytes.Clone(it.Value()))
	}
	return out
}

// pins returns the exact ledger records by pin ID.
func (n *node) pins() map[string]string {
	out := map[string]string{}
	it := n.db.NewIterator([]byte(PinRecordPrefix), nil)
	defer it.Release()
	for it.Next() {
		out[strings.TrimPrefix(string(it.Key()), PinRecordPrefix)] = hex.EncodeToString(it.Value())
	}
	return out
}

// capsules returns the capsule record of each observation.
func (n *node) capsules(keys []string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, key := range keys {
		if data, err := n.db.Get([]byte("extractor-source-inputs-v1/" + key + "/capsule")); err == nil {
			out[key] = json.RawMessage(data)
		}
	}
	return out
}

type scenario struct {
	Name         string                     `json:"name"`
	Before       []observationRow           `json:"observations_after_restart,omitempty"`
	ModeAtStart  string                     `json:"mode_at_start,omitempty"`
	GoLiveError  string                     `json:"go_live_error,omitempty"`
	AltError     string                     `json:"alt_block_error,omitempty"`
	ImportErrors []string                   `json:"import_errors"`
	After        []observationRow           `json:"observations"`
	Records      map[string]json.RawMessage `json:"records"`
	Capsules     map[string]json.RawMessage `json:"capsules"`
	Pins         map[string]string          `json:"pins"`
	SystemTxs    map[string]int             `json:"system_txs"`
	OutsideTxs   int                        `json:"system_txs_outside"`
	OutsideSafe  int64                      `json:"safe_points_outside"`
	Fetches      int                        `json:"fetches"`
	HeadNumber   uint64                     `json:"head_number"`
	HeadHash     string                     `json:"head_hash"`
	HeadRoot     string                     `json:"head_root"`
	Extra        map[string]string          `json:"extra,omitempty"`
}

func (n *node) finish(sc *scenario) {
	sc.After = n.observations()
	sc.Records = n.records()
	keys := []string{}
	for key := range sc.Records {
		if strings.HasSuffix(key, "/preparing") {
			var prep admission.Preparing
			json.Unmarshal(sc.Records[key], &prep)
			keys = append(keys, prep.Observation)
		}
	}
	sort.Strings(keys)
	sc.Capsules = n.capsules(keys)
	sc.Pins = n.pins()
	n.mu.Lock()
	sc.SystemTxs, sc.OutsideTxs = n.system, n.outside
	n.mu.Unlock()
	sc.OutsideSafe = n.seam.SafePointsOutside()
	sc.Fetches = int(n.seam.Fetches())
	head := n.chain.CurrentBlock()
	sc.HeadNumber, sc.HeadHash, sc.HeadRoot = head.Number.Uint64(), head.Hash().Hex(), head.Root.Hex()
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---- child ----

func TestNativeBSCAdmissionChild(t *testing.T) {
	mode := os.Getenv(envChildMode)
	if mode == "" {
		t.Skip("runs only as a crash child of TestNativeBSCAdmission")
	}
	fx := loadFixture(t)
	home := probeEnv(t, envChildHome)
	tr := newTracer(t, probeEnv(t, envChildTrace))
	n, err := startNode(t, fx, home, probeProducer, storeModel(mode), tr.emit)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.seam.Recover(n.chain); err != nil {
		t.Fatal(err)
	}
	if _, err := n.chain.InsertChain(fx.blocks[:1]); err != nil {
		t.Fatal(err)
	}
	block2 := ObservationKey(1)
	crash := func(point string) {
		tr.emit(Event{"event": "crash", "observation": block2, "point": point})
		syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	}
	switch mode {
	case "before-admitted-put-drop":
		n.store.beforePut = func(obs, kind string) {
			if obs == block2 && kind == "admitted" {
				crash(mode)
			}
		}
	case "admitted-unsynced-drop", "admitted-unsynced-keep":
		n.store.afterPut = func(obs, kind string) {
			if obs == block2 && kind == "admitted" {
				crash(mode)
			}
		}
	case "admitted-synced":
		n.store.afterSync = func(kind, obs string) {
			if obs == block2 && kind == "admitted" {
				crash(mode)
			}
		}
	case "first-handoff":
		n.seam.Handoff = func(obs string, seq uint64, _ []byte) {
			if obs == block2 && seq == 0 {
				crash(mode)
			}
		}
	case "exec-ready-unsynced-drop":
		n.store.afterPut = func(obs, kind string) {
			if obs == block2 && kind == "exec-ready" {
				crash(mode)
			}
		}
	default:
		t.Fatalf("unknown mode %s", mode)
	}
	_, err = n.chain.InsertChain(fx.blocks[1:])
	t.Fatalf("block 2 ended without the crash: %v", err)
}

// ObservationKey returns the probe producer's key of a sequence.
func ObservationKey(sequence uint64) string { return admission.ObservationKey(probeProducer, sequence) }

// ---- parent ----

func runChild(t *testing.T, work, mode, home, trace string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeBSCAdmissionChild$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), envChildMode+"="+mode, envChildHome+"="+home, envChildTrace+"="+trace)
	output, err := cmd.CombinedOutput()
	os.WriteFile(filepath.Join(work, "child-"+mode+".out"), output, 0o644)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("child %s did not end with SIGKILL: %v\n%s", mode, err, output)
	}
	return string(output)
}

func TestNativeBSCAdmission(t *testing.T) {
	fx := loadFixture(t)
	work := probeEnv(t, envWork)
	report := map[string]any{"genesis_canonical_sha256": "", "scenarios": []*scenario{}}
	var scenarios []*scenario
	trace := func(name string) *tracer { return newTracer(t, filepath.Join(work, "trace-"+name+".jsonl")) }

	// 1. Live import of both blocks in one InsertChain call.
	{
		tr := trace("live")
		home := filepath.Join(work, "home-live")
		n, err := startNode(t, fx, home, probeProducer, modelNative, tr.emit)
		if err != nil {
			t.Fatal(err)
		}
		sc := &scenario{Name: "live", ModeAtStart: string(n.mode()), Extra: map[string]string{}}
		// A seam call without the observation guard is refused.
		if _, err := n.seam.Begin(n.chain, fx.blocks[0]); !errors.Is(err, ErrGuard) {
			t.Fatalf("begin without the guard: %v", err)
		}
		// A live import before recovery is refused.
		_, err = n.chain.InsertChain(fx.blocks[:1])
		sc.Extra["import_before_recovery"] = errText(err)
		if !errors.Is(err, admission.ErrMode) {
			t.Fatalf("import before recovery: %v", err)
		}
		if err := n.seam.Recover(n.chain); err != nil {
			t.Fatal(err)
		}
		// A block that is not on top of the head with its parent state is
		// refused before anything is admitted.
		_, err = n.chain.InsertChain(fx.blocks[1:])
		sc.Extra["not_on_head"] = errText(err)
		if !errors.Is(err, ErrImportProfile) || len(n.observations()) != 0 {
			t.Fatalf("block 2 on the genesis: %v", err)
		}
		_, err = n.chain.InsertChain(fx.blocks)
		sc.ImportErrors = []string{errText(err)}
		if err != nil {
			t.Fatal(err)
		}
		// A known block with its state needs no observation.
		_, err = n.chain.InsertChain(fx.blocks[:1])
		sc.Extra["known_block"] = fmt.Sprintf("%s;%d", errText(err), len(n.observations()))
		if err != nil || len(n.observations()) != 2 {
			t.Fatalf("known block: %v", err)
		}
		// An execution outside an observation is refused.
		parent := n.chain.GetHeaderByNumber(1)
		_, err = n.chain.ProcessBlock(context.Background(), parent.Root, fx.blocks[1], core.ExecuteConfig{})
		sc.Extra["unobserved_execution"] = errText(err)
		if !errors.Is(err, core.ErrExtractorUnobserved) {
			t.Fatalf("unobserved execution: %v", err)
		}
		// A rewind below a pinned header is refused by the patched SetHead.
		retention.Install()
		err = n.chain.SetHead(0)
		sc.Extra["rewind_below_pin"] = errText(err)
		if !errors.Is(err, stateprovider.ErrLivePins) || n.chain.CurrentBlock().Number.Uint64() != 2 {
			t.Fatalf("rewind below a pin: %v", err)
		}
		receipts := []string{}
		for _, block := range fx.blocks {
			for _, receipt := range n.chain.GetReceiptsByHash(block.Hash()) {
				encoded, _ := receipt.MarshalBinary()
				receipts = append(receipts, fmt.Sprintf("%d:%x", block.NumberU64(), encoded))
			}
		}
		sc.Extra["receipts"] = strings.Join(receipts, ",")
		sc.Extra["genesis_canonical_hex"] = hex.EncodeToString(n.seam.genesis.Canonical)
		genesisRLP, _ := rlp.EncodeToBytes(n.chain.Genesis().Header())
		sc.Extra["genesis_header_rlp"] = hex.EncodeToString(genesisRLP)
		n.finish(sc)
		n.close()
		// Restart: the saved run opens with an empty producer, a foreign
		// producer is refused, and the EXEC_READY observations are listed.
		other := admission.Producer{ProducerID: "bsc-probe", RunID: "run-2"}
		if _, err := startNode(t, fx, home, other, modelNative, tr.emit); !errors.Is(err, ErrRunIdentity) {
			t.Fatalf("foreign run: %v", err)
		} else {
			sc.Extra["foreign_run"] = err.Error()
		}
		n, err = startNode(t, fx, home, admission.Producer{}, modelNative, tr.emit)
		if err != nil {
			t.Fatal(err)
		}
		sc.Extra["restart_observations"] = fmt.Sprint(len(n.observations()))
		n.close()
		// The read-only preflight refuses another store identity.
		identity := stateprovider.StoreIdentity{StoreID: probeStoreID, ChainID: fx.genesis.Config.ChainID.String(),
			GenesisHash: fx.genesis.ToBlock().Hash(), StateScheme: rawdb.HashScheme}
		if _, err := stateprovider.Preflight(filepath.Join(home, "geth"), identity); err != nil {
			t.Fatalf("preflight: %v", err)
		}
		identity.StoreID = strings.Repeat("f", 32)
		_, err = stateprovider.Preflight(filepath.Join(home, "geth"), identity)
		sc.Extra["preflight_other_store"] = errText(err)
		if !errors.Is(err, stateprovider.ErrWrongStore) {
			t.Fatalf("preflight of another store: %v", err)
		}
		scenarios = append(scenarios, sc)
	}

	// 2. Native validation failure after ADMITTED: NATIVE_REJECTED, then the
	// real block imports.
	{
		tr := trace("reject")
		n, err := startNode(t, fx, filepath.Join(work, "home-reject"), probeProducer, modelNative, tr.emit)
		if err != nil {
			t.Fatal(err)
		}
		sc := &scenario{Name: "reject", Extra: map[string]string{}}
		if err := n.seam.Recover(n.chain); err != nil {
			t.Fatal(err)
		}
		_, err1 := n.chain.InsertChain(fx.blocks[:1])
		_, err2 := n.chain.InsertChain(types.Blocks{fx.altBlock()})
		_, err3 := n.chain.InsertChain(fx.blocks[1:])
		sc.ImportErrors = []string{errText(err1), errText(err2), errText(err3)}
		if err1 != nil || err2 == nil || errors.Is(err2, ErrUnresolved) || err3 != nil {
			t.Fatalf("reject imports: %v", sc.ImportErrors)
		}
		sc.Extra["alt_hash"] = fx.altBlock().Hash().Hex()
		n.finish(sc)
		n.close()
		scenarios = append(scenarios, sc)
	}

	// 3. Crash points of AC-CAP-02-01 and AC-CAP-02-02, then restart and
	// recovery.
	for _, mode := range crashModes {
		home := filepath.Join(work, "home-"+mode)
		path := filepath.Join(work, "trace-"+mode+".jsonl")
		runChild(t, work, mode, home, path)
		tr := newTracer(t, path)
		tr.emit(Event{"event": "restart"})
		n, err := startNode(t, fx, home, admission.Producer{}, storeModel(mode), tr.emit)
		if err != nil {
			t.Fatalf("%s restart: %v", mode, err)
		}
		sc := &scenario{Name: mode, ModeAtStart: string(n.mode()), Before: n.observations(), Extra: map[string]string{}}
		sc.GoLiveError = errText(n.goLive())
		_, err = n.chain.InsertChain(types.Blocks{fx.altBlock()})
		sc.AltError = errText(err)
		if err == nil {
			t.Fatalf("%s: a new candidate imported before recovery", mode)
		}
		if err := n.seam.Recover(n.chain); err != nil {
			t.Fatalf("%s recover: %v", mode, err)
		}
		sc.Extra["head_after_recovery"] = fmt.Sprint(n.chain.CurrentBlock().Number.Uint64())
		// Block 2 imports if recovery did not already import it.
		_, err = n.chain.InsertChain(fx.blocks[1:])
		sc.ImportErrors = []string{errText(err)}
		if err != nil {
			t.Fatalf("%s import: %v", mode, err)
		}
		n.finish(sc)
		n.close()
		scenarios = append(scenarios, sc)
	}

	report["scenarios"] = scenarios
	data, err := json.MarshalIndent(report, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "report.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	checkReport(t, fx, scenarios)
}

// checkReport is the probe's own check; the driver checks the report and the
// traces again with independent rules.
func checkReport(t *testing.T, fx fixture, scenarios []*scenario) {
	byName := map[string]*scenario{}
	for _, sc := range scenarios {
		byName[sc.Name] = sc
	}
	execReady := func(sc *scenario) map[uint64]admission.ExecReadyRecord {
		out := map[uint64]admission.ExecReadyRecord{}
		for key, raw := range sc.Records {
			if strings.HasSuffix(key, "/exec-ready") {
				var record admission.ExecReadyRecord
				json.Unmarshal(raw, &record)
				var prep admission.Preparing
				json.Unmarshal(sc.Records[strings.TrimSuffix(key, "exec-ready")+"preparing"], &prep)
				out[prep.Candidate.Number] = record
			}
		}
		return out
	}
	live := byName["live"]
	reference := execReady(live)
	if len(reference) != 2 || len(live.Pins) != 2 || live.HeadNumber != 2 || live.OutsideSafe != 0 || live.OutsideTxs != 0 {
		t.Fatalf("live: %d exec-ready, %d pins, head %d, outside %d/%d", len(reference), len(live.Pins), live.HeadNumber,
			live.OutsideSafe, live.OutsideTxs)
	}
	if live.SystemTxs[ObservationKey(0)] != 9 || live.SystemTxs[ObservationKey(1)] != 0 {
		t.Fatalf("system transactions by observation: %v", live.SystemTxs)
	}
	reject := byName["reject"]
	if _, ok := reject.Records[fmt.Sprintf("%016x/rejected", 1)]; !ok || len(reject.Pins) != 2 || reject.HeadNumber != 2 {
		t.Fatalf("reject: %v pins %d head %d", keysOf(reject.Records), len(reject.Pins), reject.HeadNumber)
	}
	for _, mode := range crashModes {
		sc := byName[mode]
		got := execReady(sc)
		if got[2].ConsumedDigest != reference[2].ConsumedDigest || got[2].ExecDigest != reference[2].ExecDigest ||
			sc.HeadNumber != 2 || sc.HeadHash != live.HeadHash || sc.ModeAtStart != string(admission.RecoveryOnly) {
			t.Fatalf("%s: block 2 exec-ready %+v, reference %+v, head %d", mode, got[2], reference[2], sc.HeadNumber)
		}
		admitted := false
		for _, row := range sc.Before {
			if row.Candidate.Number == 2 && row.State == string(admission.Admitted) {
				admitted = true
			}
			if row.State == string(admission.ExecReady) && row.Candidate.Number == 2 {
				t.Fatalf("%s: EXEC_READY of block 2 survived a crash before its sync", mode)
			}
		}
		wantAdmitted := map[string]bool{"before-admitted-put-drop": false, "admitted-unsynced-drop": false,
			"admitted-unsynced-keep": true, "admitted-synced": true, "first-handoff": true, "exec-ready-unsynced-drop": true}[mode]
		if admitted != wantAdmitted || admitted && sc.GoLiveError == "" {
			t.Fatalf("%s: admitted after restart %v (want %v), GoLive %q", mode, admitted, wantAdmitted, sc.GoLiveError)
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	keys := []string{}
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ---- the core seam contract, with a fake hook ----

type fakeHook struct {
	obs    *fakeObservation
	begins int
}

func (h *fakeHook) Acquire() func() { return func() {} }

func (h *fakeHook) Begin(*core.BlockChain, *types.Block) (core.ExtractorObservation, error) {
	h.begins++
	return h.obs, nil
}

type fakeObservation struct {
	hash         common.Hash
	validated    int
	validatedErr error
	endErr       error
}

func (o *fakeObservation) Hash() [32]byte { return o.hash }

func (o *fakeObservation) Reader(native core.ExtractorReader) core.ExtractorReader { return native }

func (o *fakeObservation) Validated(*types.Block, *state.StateDB, *core.ProcessResult) error {
	o.validated++
	return o.validatedErr
}

func (o *fakeObservation) Rejected(*types.Block, *core.ProcessResult, error) {}

func (o *fakeObservation) End(err error) error {
	if o.endErr != nil {
		return o.endErr
	}
	return err
}

func plainChain(t *testing.T, fx fixture, home string) (*core.BlockChain, ethdb.Database) {
	t.Helper()
	db := openDisk(t, home)
	config := core.DefaultConfig()
	config.StateScheme, config.ArchiveMode, config.SnapshotLimit = rawdb.HashScheme, false, 0
	api, backend := NewSystemCallAPI()
	engine := parlia.New(fx.genesis.Config, db, api, fx.genesis.ToBlock().Hash())
	chain, err := core.NewBlockChain(db, fx.genesis, engine, config)
	if err != nil {
		t.Fatal(err)
	}
	backend.Bind(chain, nil)
	return chain, db
}

func TestNativeBSCHookContract(t *testing.T) {
	fx := loadFixture(t)
	work := t.TempDir()
	cases := []struct {
		name string
		obs  *fakeObservation
		head uint64
		err  error
	}{
		{"another-block", &fakeObservation{hash: fx.blocks[1].Hash()}, 0, core.ErrExtractorUnobserved},
		{"validated-error", &fakeObservation{hash: fx.blocks[0].Hash(), validatedErr: errors.New("exec-ready failed")}, 0, nil},
		{"end-error", &fakeObservation{hash: fx.blocks[0].Hash(), endErr: errors.New("unresolved")}, 1, nil},
		{"unavailable-data", &fakeObservation{hash: fx.blocks[0].Hash()}, 0, nil},
	}
	for _, c := range cases {
		chain, db := plainChain(t, fx, filepath.Join(work, c.name))
		hook := &fakeHook{obs: c.obs}
		chain.SetExtractorHook(hook)
		blocks := fx.blocks[:1]
		if c.name == "unavailable-data" {
			// The fixture is before Cancun, so a sidecar fails the native
			// data-availability check.
			blocks = types.Blocks{fx.blocks[0].WithSidecars(types.BlobSidecars{{BlockNumber: big.NewInt(1),
				BlockHash: fx.blocks[0].Hash()}})}
		}
		_, err := chain.InsertChain(blocks)
		head := chain.CurrentBlock().Number.Uint64()
		stored := chain.HasBlock(fx.blocks[0].Hash(), 1)
		chain.Stop()
		db.Close()
		switch c.name {
		case "another-block":
			if !errors.Is(err, c.err) || c.obs.validated != 0 {
				t.Errorf("%s: %v, validated %d", c.name, err, c.obs.validated)
			}
		case "validated-error":
			if err == nil || !strings.Contains(err.Error(), "exec-ready failed") || stored {
				t.Errorf("%s: %v, block stored %v", c.name, err, stored)
			}
		case "end-error":
			if err == nil || err.Error() != "unresolved" || c.obs.validated != 1 {
				t.Errorf("%s: %v, validated %d", c.name, err, c.obs.validated)
			}
		case "unavailable-data":
			if err == nil || !strings.Contains(err.Error(), "sidecars present in block body before cancun") ||
				hook.begins != 0 || stored {
				t.Errorf("%s: %v, begins %d, stored %v", c.name, err, hook.begins, stored)
			}
		}
		if head != c.head {
			t.Errorf("%s: head %d, want %d", c.name, head, c.head)
		}
	}
}

// An import that stops before execution (the chain is stopping) leaves the
// obligation ADMITTED and reports it; a restart recovers it.
func TestNativeBSCStoppedImport(t *testing.T) {
	fx := loadFixture(t)
	home := filepath.Join(t.TempDir(), "home")
	n, err := startNode(t, fx, home, probeProducer, modelNative, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.seam.Recover(n.chain); err != nil {
		t.Fatal(err)
	}
	if _, err := n.chain.InsertChain(fx.blocks[:1]); err != nil {
		t.Fatal(err)
	}
	n.chain.InterruptInsert(true)
	_, err = n.chain.InsertChain(fx.blocks[1:])
	rows := n.observations()
	n.close()
	if !errors.Is(err, ErrUnresolved) || len(rows) != 2 || rows[1].State != string(admission.Admitted) {
		t.Fatalf("stopped import: %v, %+v", err, rows)
	}
	n, err = startNode(t, fx, home, admission.Producer{}, modelNative, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	defer n.close()
	if err := n.seam.Recover(n.chain); err != nil {
		t.Fatal(err)
	}
	rows = n.observations()
	if n.chain.CurrentBlock().Number.Uint64() != 2 || len(rows) != 2 || rows[1].State != string(admission.ExecReady) ||
		rows[1].Key != ObservationKey(1) {
		t.Fatalf("after recovery: %+v", rows)
	}
}
