package sourceinputs

// Tests of the BSC wiring. TestUnitBSC* use the native in-memory database.
// TestNativeBSC* import the accepted private BSC fixture through the native
// BlockChain and the native Parlia engine; they need the environment that
// test/harness/bsc_source_inputs.py sets and fail without it.

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
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/parlia"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/rlp"
)

// ---- unit tests on the native in-memory database ----

type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func (l *eventLog) emit(event Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) names() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var names []string
	for _, event := range l.events {
		name := event["event"].(string)
		if record, ok := event["record"]; ok {
			name += ":" + record.(string)
		}
		names = append(names, name)
	}
	return names
}

func bscUnitLog(t *testing.T, db ethdb.Database, events *eventLog, id string) *Log {
	t.Helper()
	store := TracedStore{DB: db, Emit: events.emit}
	if _, err := SealCapsule(store, id, []Item{{Name: "header", Data: []byte(id)}}); err != nil {
		t.Fatal(err)
	}
	log, err := OpenLog(store, id)
	if err != nil {
		t.Fatal(err)
	}
	return log
}

func TestUnitBSCGetIsDurableBeforeHandoff(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	events := new(eventLog)
	log := bscUnitLog(t, db, events, "obs-a")
	if err := db.Put([]byte("parlia-k"), []byte("snapshot")); err != nil {
		t.Fatal(err)
	}
	wrapped := NewParliaDB(db, false, events.emit)
	var handed [][]byte
	wrapped.Handoff = func(id string, sequence uint64, response []byte) {
		if id != "obs-a" || sequence != uint64(len(handed)) {
			t.Fatalf("handoff %s %d", id, sequence)
		}
		handed = append(handed, response)
	}
	if err := wrapped.Attach("obs-a", log); err != nil {
		t.Fatal(err)
	}
	value, err := wrapped.Get([]byte("parlia-k"))
	if err != nil || string(value) != "snapshot" {
		t.Fatalf("Get = %q, %v", value, err)
	}
	if len(handed) != 1 || !bytes.Equal(handed[0], []byte("\x01snapshot")) || wrapped.Fetches() != 1 {
		t.Fatalf("handed %q, fetches %d", handed, wrapped.Fetches())
	}
	want := []string{"put:capsule-chunk", "sync", "put:capsule", "sync", "sync", "fetch",
		"put:entry-chunk", "put:entry", "sync", "put:frontier", "sync", "handoff"}
	if got := events.names(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events %v, want %v", got, want)
	}
	if err := wrapped.Detach(); err != nil {
		t.Fatal(err)
	}
}

func TestUnitBSCMissingKeyAndHas(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	events := new(eventLog)
	log := bscUnitLog(t, db, events, "obs-b")
	if err := db.Put([]byte("present"), []byte{}); err != nil {
		t.Fatal(err)
	}
	wrapped := NewParliaDB(db, false, events.emit)
	var handed []string
	wrapped.Handoff = func(_ string, _ uint64, response []byte) { handed = append(handed, hex.EncodeToString(response)) }
	if err := wrapped.Attach("obs-b", log); err != nil {
		t.Fatal(err)
	}
	if value, err := wrapped.Get([]byte("missing")); !errors.Is(err, ErrDependencyNotFound) || value != nil {
		t.Fatalf("missing Get = %q, %v", value, err)
	}
	if value, err := wrapped.Get([]byte("present")); err != nil || value == nil || len(value) != 0 {
		t.Fatalf("empty value Get = %q, %v", value, err)
	}
	if present, err := wrapped.Has([]byte("present")); err != nil || !present {
		t.Fatalf("Has(present) = %v, %v", present, err)
	}
	if present, err := wrapped.Has([]byte("missing")); err != nil || present {
		t.Fatalf("Has(missing) = %v, %v", present, err)
	}
	if strings.Join(handed, ",") != "00,01,01,00" {
		t.Fatalf("handed %v", handed)
	}
	if err := wrapped.Detach(); err != nil {
		t.Fatal(err)
	}
	manifest, err := log.Finish()
	if err != nil || manifest.Count != 4 {
		t.Fatalf("Finish = %+v, %v", manifest, err)
	}
}

func TestUnitBSCReplayIgnoresTheLiveValue(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	events := new(eventLog)
	log := bscUnitLog(t, db, events, "obs-c")
	if err := db.Put([]byte("parlia-k"), []byte("first")); err != nil {
		t.Fatal(err)
	}
	wrapped := NewParliaDB(db, false, events.emit)
	if err := wrapped.Attach("obs-c", log); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Get([]byte("parlia-k")); err != nil {
		t.Fatal(err)
	}
	if present, err := wrapped.Has([]byte("parlia-k")); err != nil || !present {
		t.Fatalf("Has = %v, %v", present, err)
	}
	if err := wrapped.Detach(); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete([]byte("parlia-k")); err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("parlia-k"), []byte("changed")); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenLog(db, "obs-c")
	if err != nil {
		t.Fatal(err)
	}
	replay := NewParliaDB(db, false, events.emit)
	if err := replay.Attach("obs-c", reopened); err != nil {
		t.Fatal(err)
	}
	if value, err := replay.Get([]byte("parlia-k")); err != nil || string(value) != "first" {
		t.Fatalf("replayed Get = %q, %v", value, err)
	}
	if err := db.Delete([]byte("parlia-k")); err != nil {
		t.Fatal(err)
	}
	if present, err := replay.Has([]byte("parlia-k")); err != nil || !present {
		t.Fatalf("replayed Has = %v, %v", present, err)
	}
	if replay.Fetches() != 0 {
		t.Fatalf("replay fetched %d times", replay.Fetches())
	}
	// A closed log refuses a read beyond the saved prefix.
	if _, err := replay.Get([]byte("parlia-other")); !errors.Is(err, ErrClosed) {
		t.Fatalf("read beyond the closed prefix = %v", err)
	}
	if err := replay.Detach(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Detach = %v", err)
	}
}

func TestUnitBSCReadsOutsideAnObservationStop(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	if err := db.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	wrapped := NewParliaDB(db, false, nil)
	if _, err := wrapped.Get([]byte("k")); !errors.Is(err, ErrNoObservation) {
		t.Fatalf("Get without a log = %v", err)
	}
	if _, err := wrapped.Has([]byte("k")); !errors.Is(err, ErrNoObservation) {
		t.Fatalf("Has after a stop = %v", err)
	}
	if wrapped.Fetches() != 0 || !errors.Is(wrapped.Err(), ErrNoObservation) {
		t.Fatalf("fetches %d, err %v", wrapped.Fetches(), wrapped.Err())
	}
	// The error stays after a later attach.
	events := new(eventLog)
	if err := wrapped.Attach("obs-d", bscUnitLog(t, db, events, "obs-d")); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Get([]byte("k")); !errors.Is(err, ErrNoObservation) {
		t.Fatalf("Get after the stop = %v", err)
	}
	if err := wrapped.Detach(); !errors.Is(err, ErrNoObservation) {
		t.Fatalf("Detach = %v", err)
	}
}

func TestUnitBSCLiveModeReadsTheNativeDatabase(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	if err := db.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	wrapped := NewParliaDB(db, true, nil)
	if value, err := wrapped.Get([]byte("k")); err != nil || string(value) != "v" {
		t.Fatalf("live Get = %q, %v", value, err)
	}
	if _, err := wrapped.Get([]byte("missing")); !errors.Is(err, ErrDependencyNotFound) {
		t.Fatalf("live missing Get = %v", err)
	}
	if wrapped.Fetches() != 2 || wrapped.Err() != nil {
		t.Fatalf("fetches %d, err %v", wrapped.Fetches(), wrapped.Err())
	}
}

func TestUnitBSCIteratorStopsTheObservation(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	wrapped := NewParliaDB(db, true, nil)
	iterator := wrapped.NewIterator([]byte("parlia-"), nil)
	iterator.Release()
	if !errors.Is(wrapped.Err(), ErrUnloggedRead) {
		t.Fatalf("Err = %v", wrapped.Err())
	}
	if _, err := wrapped.Get([]byte("k")); !errors.Is(err, ErrUnloggedRead) {
		t.Fatalf("Get after an iterator = %v", err)
	}
	// The first error is kept: a later iterator does not replace it.
	first := NewParliaDB(db, false, nil)
	if _, err := first.Get([]byte("k")); !errors.Is(err, ErrNoObservation) {
		t.Fatalf("Get without a log = %v", err)
	}
	first.NewIterator(nil, nil).Release()
	if !errors.Is(first.Err(), ErrNoObservation) || errors.Is(first.Err(), ErrUnloggedRead) {
		t.Fatalf("first error replaced: %v", first.Err())
	}
}

func TestUnitBSCAttachTwiceIsRefused(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	events := new(eventLog)
	log := bscUnitLog(t, db, events, "obs-e")
	wrapped := NewParliaDB(db, false, nil)
	if err := wrapped.Attach("obs-e", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Attach(nil) = %v", err)
	}
	if err := wrapped.Attach("obs-e", log); err != nil {
		t.Fatal(err)
	}
	if err := wrapped.Attach("obs-e", log); !errors.Is(err, ErrInvalid) {
		t.Fatalf("second Attach = %v", err)
	}
}

// failingGet reports a read error for a key that exists.
type failingGet struct {
	ethdb.Database
	hasErr error
}

func (f failingGet) Get([]byte) ([]byte, error) { return nil, errors.New("disk read failed") }

func (f failingGet) Has(key []byte) (bool, error) {
	if f.hasErr != nil {
		return false, f.hasErr
	}
	return f.Database.Has(key)
}

func TestUnitBSCNativeReadErrorsStopTheObservation(t *testing.T) {
	for _, hasErr := range []error{nil, errors.New("disk has failed")} {
		db := rawdb.NewMemoryDatabase()
		if err := db.Put([]byte("k"), []byte("v")); err != nil {
			t.Fatal(err)
		}
		events := new(eventLog)
		log := bscUnitLog(t, db, events, "obs-f")
		wrapped := NewParliaDB(failingGet{Database: db, hasErr: hasErr}, false, events.emit)
		if err := wrapped.Attach("obs-f", log); err != nil {
			t.Fatal(err)
		}
		_, err := wrapped.Get([]byte("k"))
		if err == nil || errors.Is(err, ErrDependencyNotFound) || wrapped.Err() == nil {
			t.Fatalf("hasErr %v: Get = %v, Err %v", hasErr, err, wrapped.Err())
		}
		if log.Frontier() != 0 {
			t.Fatalf("a failed read was logged")
		}
	}
	db := rawdb.NewMemoryDatabase()
	events := new(eventLog)
	log := bscUnitLog(t, db, events, "obs-g")
	wrapped := NewParliaDB(failingGet{Database: db, hasErr: errors.New("disk has failed")}, false, events.emit)
	if err := wrapped.Attach("obs-g", log); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Has([]byte("k")); err == nil || wrapped.Err() == nil {
		t.Fatalf("Has error = %v", err)
	}
}

func TestUnitBSCMalformedSavedResponseStops(t *testing.T) {
	for _, tc := range []struct {
		tag      string
		response []byte
	}{{ParliaGetTag, []byte{}}, {ParliaGetTag, []byte{2, 1}}, {ParliaGetTag, []byte{0, 0}},
		{ParliaHasTag, []byte{}}, {ParliaHasTag, []byte{2}}, {ParliaHasTag, []byte{1, 0}}} {
		db := rawdb.NewMemoryDatabase()
		events := new(eventLog)
		log := bscUnitLog(t, db, events, "obs-h")
		if _, err := log.Next(ParliaRequest(tc.tag, []byte("k")), func() ([]byte, error) { return tc.response, nil }); err != nil {
			t.Fatal(err)
		}
		reopened, err := OpenLog(db, "obs-h")
		if err != nil {
			t.Fatal(err)
		}
		wrapped := NewParliaDB(db, false, nil)
		if err := wrapped.Attach("obs-h", reopened); err != nil {
			t.Fatal(err)
		}
		if tc.tag == ParliaGetTag {
			_, err = wrapped.Get([]byte("k"))
		} else {
			_, err = wrapped.Has([]byte("k"))
		}
		if !errors.Is(err, ErrCorrupt) || !errors.Is(wrapped.Err(), ErrCorrupt) {
			t.Fatalf("%s %x: err %v, Err %v", tc.tag, tc.response, err, wrapped.Err())
		}
	}
}

func TestUnitBSCRecordKind(t *testing.T) {
	cases := map[string][2]string{
		keyPrefix + "o/capsule":                                   {"o", "capsule"},
		keyPrefix + "o/capsule/0000000000000000/0000000000000000": {"o", "capsule-chunk"},
		keyPrefix + "o/log/0000000000000000":                      {"o", "entry"},
		keyPrefix + "o/log/0000000000000000/0000000000000001":     {"o", "entry-chunk"},
		keyPrefix + "o/frontier":                                  {"o", "frontier"},
		keyPrefix + "o/closed":                                    {"o", "closed"},
		keyPrefix + "o/unknown":                                   {"o", "other"},
		keyPrefix + "o/log":                                       {"o", "other"},
		"parlia-o/frontier":                                       {"", "other"},
	}
	for key, want := range cases {
		observation, kind := RecordKind([]byte(key))
		if observation != want[0] || kind != want[1] {
			t.Errorf("RecordKind(%q) = %q, %q; want %q, %q", key, observation, kind, want[0], want[1])
		}
	}
	// The keys the core writes have the kinds the probe expects.
	if _, kind := RecordKind(capsuleKey("o")); kind != "capsule" {
		t.Error(kind)
	}
	if _, kind := RecordKind(itemChunkKey("o", 1, 2)); kind != "capsule-chunk" {
		t.Error(kind)
	}
	if _, kind := RecordKind(entryKey("o", 3)); kind != "entry" {
		t.Error(kind)
	}
	if _, kind := RecordKind(responseChunkKey("o", 3, 4)); kind != "entry-chunk" {
		t.Error(kind)
	}
	if _, kind := RecordKind(frontierKey("o")); kind != "frontier" {
		t.Error(kind)
	}
	if _, kind := RecordKind(closedKey("o")); kind != "closed" {
		t.Error(kind)
	}
}

func TestUnitBSCBlockItems(t *testing.T) {
	items := BlockItems([]byte{0xc0}, common.Hash{1}, []byte("{}"), "abc")
	var names []string
	for _, item := range items {
		names = append(names, item.Name+"="+hex.EncodeToString(item.Data))
	}
	want := []string{"block-rlp=c0", "parent-hash=01" + strings.Repeat("00", 31), "genesis-json=7b7d",
		"native-commit=616263", "state-profile=" + hex.EncodeToString([]byte(StateProfile))}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("items %v", names)
	}
	if string(ParliaRequest(ParliaGetTag, []byte("k"))) != "bsc-parlia-db-get-v1\x00k" ||
		string(ParliaRequest(ParliaHasTag, []byte("k"))) != "bsc-parlia-db-has-v1\x00k" {
		t.Fatal("request encoding")
	}
}

// ---- native probe on the accepted private BSC fixture ----

const childHomeEnv = "EXTRACTOR_INPUTS_CHILD_HOME"

func probeEnv(t testing.TB, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}

type probeFixture struct {
	genesis     *core.Genesis
	genesisJSON []byte
	genesisHash common.Hash
	blocks      types.Blocks
	raw         [][]byte
	commit      string
}

func loadProbeFixture(t testing.TB) probeFixture {
	t.Helper()
	inputs := probeEnv(t, "EXTRACTOR_INPUTS_INPUTS")
	fx := probeFixture{commit: probeEnv(t, "EXTRACTOR_INPUTS_NATIVE_COMMIT")}
	var err error
	if fx.genesisJSON, err = os.ReadFile(filepath.Join(inputs, "genesis.json")); err != nil {
		t.Fatal(err)
	}
	fx.genesis = new(core.Genesis)
	if err := json.Unmarshal(fx.genesisJSON, fx.genesis); err != nil {
		t.Fatal(err)
	}
	fx.genesisHash = fx.genesis.ToBlock().Hash()
	data, err := os.ReadFile(filepath.Join(inputs, "blocks.rlp"))
	if err != nil {
		t.Fatal(err)
	}
	stream := rlp.NewStream(bytes.NewReader(data), 0)
	for {
		raw, err := stream.Raw()
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		block := new(types.Block)
		if err := rlp.DecodeBytes(raw, block); err != nil {
			t.Fatal(err)
		}
		if encoded, err := rlp.EncodeToBytes(block); err != nil || !bytes.Equal(encoded, raw) {
			t.Fatalf("block %d does not re-encode to its fixture bytes", block.NumberU64())
		}
		fx.blocks, fx.raw = append(fx.blocks, block), append(fx.raw, bytes.Clone(raw))
	}
	if len(fx.blocks) != 2 || fx.blocks[0].NumberU64() != 1 || fx.blocks[1].NumberU64() != 2 {
		t.Fatalf("fixture blocks %d", len(fx.blocks))
	}
	return fx
}

// tracer appends one JSON line per event with a direct write, so the lines
// survive a SIGKILL of the writing process.
type tracer struct {
	mu   sync.Mutex
	file *os.File
	t    testing.TB
}

func newTracer(t testing.TB, path string) *tracer {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return &tracer{file: file, t: t}
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

func openProbeDisk(t testing.TB, home string) ethdb.Database {
	t.Helper()
	chaindata := filepath.Join(home, "chaindata")
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

// openChain opens the native chain with the capture profile of E01-02.pins
// (hash scheme, full garbage collection, native snapshots off). Only the
// Parlia engine receives the wrapped handle.
func openChain(db ethdb.Database, engineDB ethdb.Database, genesis *core.Genesis) (*core.BlockChain, error) {
	config := core.DefaultConfig()
	config.StateScheme, config.ArchiveMode, config.SnapshotLimit = rawdb.HashScheme, false, 0
	return core.NewBlockChain(db, genesis, parlia.New(genesis.Config, engineDB, nil, genesis.ToBlock().Hash()), config)
}

type observationResult struct {
	Observation string    `json:"observation"`
	Capsule     Capsule   `json:"capsule"`
	Consumed    *Consumed `json:"consumed"`
	Frontier    uint64    `json:"frontier_at_start"`
	Fetches     int       `json:"fetches"`
	HeadNumber  uint64    `json:"head_number"`
	HeadHash    string    `json:"head_hash"`
	HeadRoot    string    `json:"head_root"`
	Error       string    `json:"error"`
}

func observationID(index int) string { return fmt.Sprintf("bsc-block-%d", index+1) }

// observe runs one observation: seal and sync the capsule, open the log,
// then open the native chain with a fresh Parlia engine and import one
// block. Every read of the engine during the observation, including its
// startup reads, goes through the log. storeDB holds the source inputs; nil
// means the node's own database.
func observe(t testing.TB, fx probeFixture, home string, storeDB ethdb.Database, index int, emit func(Event),
	handoff func(string, uint64, []byte)) observationResult {
	t.Helper()
	db := openProbeDisk(t, home)
	defer db.Close()
	if storeDB == nil {
		storeDB = db
	}
	id := observationID(index)
	result := observationResult{Observation: id}
	fail := func(err error) observationResult {
		result.Error = err.Error()
		emit(Event{"event": "failed", "observation": id, "error": result.Error})
		return result
	}
	parent := fx.genesisHash
	if index > 0 {
		parent = fx.blocks[index-1].Hash()
	}
	store := TracedStore{DB: storeDB, Emit: emit}
	var err error
	if result.Capsule, err = SealCapsule(store, id, BlockItems(fx.raw[index], parent, fx.genesisJSON, fx.commit)); err != nil {
		return fail(err)
	}
	log, err := OpenLog(store, id)
	if err != nil {
		return fail(err)
	}
	result.Frontier = log.Frontier()
	engineDB := NewParliaDB(db, false, emit)
	engineDB.Handoff = handoff
	if err := engineDB.Attach(id, log); err != nil {
		return fail(err)
	}
	emit(Event{"event": "execute", "observation": id, "frontier": log.Frontier()})
	chain, err := openChain(db, engineDB, fx.genesis)
	if err != nil {
		return fail(err)
	}
	_, insertErr := chain.InsertChain(types.Blocks{fx.blocks[index]})
	head := chain.CurrentBlock()
	chain.Stop()
	wiringErr := engineDB.Detach()
	result.Fetches = engineDB.Fetches()
	result.HeadNumber, result.HeadHash, result.HeadRoot = head.Number.Uint64(), head.Hash().Hex(), head.Root.Hex()
	if insertErr != nil {
		return fail(insertErr)
	}
	if wiringErr != nil {
		return fail(wiringErr)
	}
	consumed, err := log.Finish()
	if err != nil {
		return fail(err)
	}
	result.Consumed = &consumed
	emit(Event{"event": "finished", "observation": id, "consumed": consumed})
	return result
}

// TestNativeBSCInputsChild is the capture process. It completes block 1,
// then kills itself with SIGKILL at the first hand-off of block 2: after the
// response is durable and before Parlia receives it.
func TestNativeBSCInputsChild(t *testing.T) {
	home := os.Getenv(childHomeEnv)
	if home == "" {
		t.Skip("runs only as the capture child of TestNativeBSCInputs")
	}
	fx := loadProbeFixture(t)
	tr := newTracer(t, probeEnv(t, "EXTRACTOR_INPUTS_TRACE"))
	if result := observe(t, fx, home, nil, 0, tr.emit, nil); result.Error != "" || result.Consumed == nil {
		t.Fatalf("block 1: %+v", result)
	}
	kill := func(id string, sequence uint64, _ []byte) {
		if id == observationID(1) && sequence == 0 {
			tr.emit(Event{"event": "crash", "observation": id, "sequence": sequence})
			syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {}
		}
	}
	result := observe(t, fx, home, nil, 1, tr.emit, kill)
	t.Fatalf("block 2 ended without the crash: %+v", result)
}

type traceEvent struct {
	Event       string `json:"event"`
	Observation string `json:"observation"`
	Sequence    uint64 `json:"sequence"`
	RequestHex  string `json:"request_hex"`
}

func readTrace(t testing.TB, path string) []traceEvent {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []traceEvent
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var event traceEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("trace line %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

// tamperSnapshot moves every validator of a Parlia snapshot to another
// address, so a block sealed by the real validator no longer verifies.
func tamperSnapshot(blob []byte) ([]byte, error) {
	snap := new(parlia.Snapshot)
	if err := json.Unmarshal(blob, snap); err != nil {
		return nil, err
	}
	if len(snap.Validators) == 0 {
		return nil, errors.New("snapshot has no validators")
	}
	moved := make(map[common.Address]*parlia.ValidatorInfo, len(snap.Validators))
	for address, info := range snap.Validators {
		address[0] ^= 0xff
		moved[address] = info
	}
	snap.Validators = moved
	return json.Marshal(snap)
}

func sumHex(data []byte) string {
	digest := sum(data)
	return hex.EncodeToString(digest[:])
}

type probeReport struct {
	Genesis      string              `json:"genesis_hash"`
	Blocks       []string            `json:"block_hashes"`
	Child        map[string]any      `json:"child"`
	AfterCrash   map[string]any      `json:"after_crash"`
	ReplayTamper map[string]any      `json:"replay_tamper"`
	Resume       observationResult   `json:"resume"`
	Control      map[string]any      `json:"control"`
	Replay       []observationResult `json:"replay"`
	PublicCredit bool                `json:"public_profile_credit"`
}

func TestNativeBSCInputs(t *testing.T) {
	fx := loadProbeFixture(t)
	work := probeEnv(t, "EXTRACTOR_INPUTS_WORK")
	capture, replayHome, control := filepath.Join(work, "capture"), filepath.Join(work, "replay"), filepath.Join(work, "control")
	report := probeReport{Genesis: fx.genesisHash.Hex(), Blocks: []string{fx.blocks[0].Hash().Hex(), fx.blocks[1].Hash().Hex()}}

	// 1. Capture child: block 1 completes, block 2 is killed at its first hand-off.
	childTrace := filepath.Join(work, "trace-capture.jsonl")
	childOutput, err := os.Create(filepath.Join(work, "child-output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestNativeBSCInputsChild$", "-test.v", "-test.count=1")
	child.Env = append(os.Environ(), childHomeEnv+"="+capture, "EXTRACTOR_INPUTS_TRACE="+childTrace)
	child.Stdout, child.Stderr = childOutput, childOutput
	runErr := child.Run()
	childOutput.Close()
	var exitErr *exec.ExitError
	killed := errors.As(runErr, &exitErr) && exitErr.Sys().(syscall.WaitStatus).Signaled() &&
		exitErr.Sys().(syscall.WaitStatus).Signal() == syscall.SIGKILL
	report.Child = map[string]any{"error": fmt.Sprint(runErr), "killed_by_sigkill": killed}
	if !killed {
		t.Fatalf("capture child was not killed at the hand-off: %v", runErr)
	}
	var crashRequest []byte
	for _, event := range readTrace(t, childTrace) {
		if event.Event == "handoff" && event.Observation == observationID(1) && event.Sequence == 0 {
			if crashRequest, err = hex.DecodeString(event.RequestHex); err != nil {
				t.Fatal(err)
			}
		}
	}
	key, ok := bytes.CutPrefix(crashRequest, ParliaRequest(ParliaGetTag, nil))
	if !ok || !bytes.HasPrefix(key, []byte("parlia-")) {
		t.Fatalf("first block 2 request %q is not a Parlia snapshot read", crashRequest)
	}

	// 2. After the crash: both capsules and the block 2 response are on disk.
	db := openProbeDisk(t, capture)
	after := map[string]any{"snapshot_key_hex": hex.EncodeToString(key)}
	for index := range 2 {
		capsule, err := ReadCapsule(db, observationID(index))
		if err != nil {
			t.Fatalf("capsule %d after the crash: %v", index+1, err)
		}
		after[observationID(index)+"_capsule"] = capsule
		log, err := OpenLog(db, observationID(index))
		if err != nil {
			t.Fatal(err)
		}
		after[observationID(index)+"_frontier"] = log.Frontier()
	}
	inspect, err := OpenLog(db, observationID(1))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := inspect.Next(crashRequest, func() ([]byte, error) { return nil, errors.New("inspection must not fetch") })
	if err != nil {
		t.Fatalf("saved block 2 response: %v", err)
	}
	live, err := db.Get(key)
	if err != nil {
		t.Fatalf("live snapshot after the crash: %v", err)
	}
	db.Close()
	after["saved_response_sha256"] = sumHex(saved)
	after["saved_equals_live"] = bytes.Equal(saved, append([]byte{statusPresent}, live...))
	report.AfterCrash = after
	if !after["saved_equals_live"].(bool) {
		t.Fatal("the saved response differs from the snapshot Parlia would have read")
	}

	// 3. Resume block 2 on the crashed home: the saved prefix is replayed
	// without a native read; a read beyond it is fetched and logged.
	resumeTrace := newTracer(t, filepath.Join(work, "trace-resume.jsonl"))
	report.Resume = observe(t, fx, capture, nil, 1, resumeTrace.emit, nil)
	// Only responses beyond the saved frontier may be fetched.
	if report.Resume.Error != "" || report.Resume.Frontier != 1 || report.Resume.Consumed == nil ||
		uint64(report.Resume.Fetches) != report.Resume.Consumed.Count-report.Resume.Frontier ||
		report.Resume.HeadHash != fx.blocks[1].Hash().Hex() {
		t.Errorf("resume: %+v", report.Resume)
	}

	// 4. Control: the same tampered snapshot read live makes block 2 fail.
	controlTrace := newTracer(t, filepath.Join(work, "trace-control.jsonl"))
	report.Control = runControl(t, fx, control, controlTrace.emit)

	// 5. Native-only replay on a fresh home from the saved inputs of the
	// crashed home: no native read, same consumed manifests. Before block 2,
	// the live snapshot of the replay home is tampered as in the control; the
	// replay still hands Parlia the saved bytes, so block 2 still verifies.
	store := openProbeDisk(t, capture)
	replayTrace := newTracer(t, filepath.Join(work, "trace-replay.jsonl"))
	report.Replay = append(report.Replay, observe(t, fx, replayHome, store, 0, replayTrace.emit, nil))
	report.ReplayTamper = tamperHome(t, replayHome, key)
	report.Replay = append(report.Replay, observe(t, fx, replayHome, store, 1, replayTrace.emit, nil))
	store.Close()
	for index, result := range report.Replay {
		if result.Error != "" || result.Fetches != 0 || result.HeadHash != fx.blocks[index].Hash().Hex() {
			t.Errorf("replay %d: %+v", index+1, result)
		}
	}
	if report.Replay[1].Consumed == nil || report.Resume.Consumed == nil || *report.Replay[1].Consumed != *report.Resume.Consumed {
		t.Error("replayed block 2 manifest differs from the resumed one")
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(probeEnv(t, "EXTRACTOR_INPUTS_REPORT"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tamperHome replaces the stored Parlia snapshot of a home with a tampered
// copy and reports both digests.
func tamperHome(t *testing.T, home string, key []byte) map[string]any {
	t.Helper()
	db := openProbeDisk(t, home)
	defer db.Close()
	live, err := db.Get(key)
	if err != nil {
		t.Fatalf("live snapshot of %s: %v", home, err)
	}
	tampered, err := tamperSnapshot(live)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put(key, tampered); err != nil {
		t.Fatal(err)
	}
	if err := db.SyncKeyValue(); err != nil {
		t.Fatal(err)
	}
	return map[string]any{"live_before_tamper_sha256": sumHex(live), "live_after_tamper_sha256": sumHex(tampered),
		"live_after_tamper_response_sha256": sumHex(append([]byte{statusPresent}, tampered...))}
}

// runControl imports block 1 live, applies the same tampering to the stored
// snapshot, then imports block 2 live with a fresh engine: the live read of
// the tampered snapshot makes block 2 fail.
func runControl(t *testing.T, fx probeFixture, home string, emit func(Event)) map[string]any {
	t.Helper()
	db := openProbeDisk(t, home)
	chain, err := openChain(db, NewParliaDB(db, true, emit), fx.genesis)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chain.InsertChain(types.Blocks{fx.blocks[0]}); err != nil {
		t.Fatal(err)
	}
	chain.Stop()
	db.Close()
	result := tamperHome(t, home, append([]byte("parlia-"), fx.genesisHash.Bytes()...))
	db = openProbeDisk(t, home)
	defer db.Close()
	engineDB := NewParliaDB(db, true, emit)
	chain, err = openChain(db, engineDB, fx.genesis)
	if err != nil {
		result["stage"], result["error"] = "open", err.Error()
		return result
	}
	_, err = chain.InsertChain(types.Blocks{fx.blocks[1]})
	result["head_number"] = chain.CurrentBlock().Number.Uint64()
	chain.Stop()
	result["stage"], result["error"], result["fetches"] = "insert", fmt.Sprint(err), engineDB.Fetches()
	if err == nil || !strings.Contains(err.Error(), "unauthorized validator") {
		t.Errorf("control import of block 2 = %v", err)
	}
	return result
}
