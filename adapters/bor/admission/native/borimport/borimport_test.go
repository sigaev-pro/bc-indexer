package borimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/admission/native/obligation"
	"github.com/ethereum/go-ethereum/extractor/borwire"
	"github.com/ethereum/go-ethereum/extractor/capture"
	"github.com/ethereum/go-ethereum/extractor/retention"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
)

const testStore = "0123456789abcdef0123456789abcdef"

// boundStore is a hash-scheme store with a committed genesis, bound to testStore.
func boundStore(t *testing.T) (ethdb.Database, *triedb.Database, *types.Block, stateprovider.StoreIdentity) {
	t.Helper()
	db := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(db, triedb.HashDefaults)
	genesis := &core.Genesis{Config: params.AllEthashProtocolChanges, BaseFee: big.NewInt(params.InitialBaseFee),
		Alloc: types.GenesisAlloc{common.HexToAddress("0x01"): {Balance: big.NewInt(1)}}}
	block := genesis.MustCommit(db, tdb)
	identity, err := stateprovider.Bind(db, testStore)
	if err != nil {
		t.Fatal(err)
	}
	return db, tdb, block, identity
}

func testPin(id string, identity stateprovider.StoreIdentity, block *types.Block, headers ...stateprovider.HeaderRef) stateprovider.Pin {
	if len(headers) == 0 {
		headers = []stateprovider.HeaderRef{{Hash: block.Hash(), Number: block.NumberU64()}}
	}
	return stateprovider.Pin{ID: id, Store: identity, Profile: ProfileDigest(), ParentRoot: block.Root(), Headers: headers}
}

func TestRetentionHooksAreInstalledByTheWiring(t *testing.T) {
	if !retention.Installed() {
		t.Fatal("borwire did not install every retention hook")
	}
}

func TestRequireRewindAndFreezeHooksCallTheLedger(t *testing.T) {
	db, tdb, block, identity := boundStore(t)
	provider, err := stateprovider.New(db, tdb, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := retention.RequireRewindAllowed(db, 0); err != nil {
		t.Fatalf("rewind without pins: %v", err)
	}
	if limit, err := retention.FreezeLimit(db, 100); err != nil || limit != 100 {
		t.Fatalf("freeze limit without pins: %d %v", limit, err)
	}
	pin := testPin("bor.run-1.0000000000000007", identity, block, stateprovider.HeaderRef{Hash: block.Hash(), Number: 0},
		stateprovider.HeaderRef{Hash: common.HexToHash("0x07"), Number: 7})
	if err := provider.Pin(pin); err == nil {
		t.Fatal("a pin of an unreadable header was recorded")
	}
	pin = testPin("bor.run-1.0000000000000000", identity, block)
	if err := provider.Pin(pin); err != nil {
		t.Fatal(err)
	}
	// The pinned genesis header is canonical: no freeze limit, and only a
	// rewind below it would be refused (none exists below 0).
	if err := retention.RequireRewindAllowed(db, 0); err != nil {
		t.Fatalf("rewind to the pinned header: %v", err)
	}
	if limit, err := retention.FreezeLimit(db, 100); err != nil || limit != 100 {
		t.Fatalf("canonical pin limited the freezer: %d %v", limit, err)
	}
	// A side-branch header at number 1 limits the freezer to the fork point.
	side := &types.Header{ParentHash: block.Hash(), Number: big.NewInt(1), Root: block.Root(), Difficulty: big.NewInt(9)}
	rawdb.WriteHeader(db, side)
	sidePin := testPin("bor.run-1.0000000000000001", identity, block, stateprovider.HeaderRef{Hash: side.Hash(), Number: 1})
	if err := provider.Pin(sidePin); err != nil {
		t.Fatal(err)
	}
	if limit, err := retention.FreezeLimit(db, 100); err != nil || limit != 1 {
		t.Fatalf("side-branch pin: limit %d %v, want 1", limit, err)
	}
	if err := retention.RequireRewindAllowed(db, 0); !errors.Is(err, stateprovider.ErrLivePins) {
		t.Fatalf("rewind below a pinned header: %v", err)
	}
}

func TestGuardedRecoverPruning(t *testing.T) {
	db, tdb, block, identity := boundStore(t)
	provider, err := stateprovider.New(db, tdb, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Pin(testPin("bor.run-1.0000000000000000", identity, block)); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := retention.RecoverPruning(dir, db); err != nil {
		t.Fatalf("no pending prune, live pins: %v", err)
	}
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "statebloom.0x01.bf.gz.tmp"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pending, err := borwire.PendingPrune(dir); err != nil || pending {
		t.Fatalf("a temporary bloom file counted as a pending prune: %t %v", pending, err)
	}
	if err := os.WriteFile(filepath.Join(nested, "statebloom.0x01.bf.gz"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := retention.RecoverPruning(dir, db); !errors.Is(err, stateprovider.ErrLivePins) {
		t.Fatalf("pending prune with live pins: %v", err)
	}
	if err := provider.Release("bor.run-1.0000000000000000"); err != nil {
		t.Fatal(err)
	}
	// Without pins the native recovery runs; the fake bloom file makes it fail natively.
	if err := retention.RecoverPruning(dir, db); err == nil || errors.Is(err, stateprovider.ErrLivePins) {
		t.Fatalf("native recovery after the guard: %v", err)
	}
	if _, err := borwire.PendingPrune(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("an unreadable directory passed")
	}
}

func TestPinLedgerAdapterOnTheNativeLedger(t *testing.T) {
	db, tdb, block, identity := boundStore(t)
	provider, err := stateprovider.New(db, tdb, identity)
	if err != nil {
		t.Fatal(err)
	}
	pins := obligation.PinAdapter{Ledger: ledger{db: db, provider: provider}, Unknown: stateprovider.ErrUnknownPin}
	const id = "bor.run-1.0000000000000000"
	root := hexOf(block.Root())
	if _, err := pins.Verify(id, root); !errors.Is(err, obligation.ErrPinMissing) {
		t.Fatalf("absent pin: %v", err)
	}
	if err := pins.Release(id); err != nil {
		t.Fatalf("release of an absent pin: %v", err)
	}
	if err := provider.Pin(testPin(id, identity, block)); err != nil {
		t.Fatal(err)
	}
	listed, err := stateprovider.Pins(db)
	if err != nil || len(listed) != 1 || listed[0].ID != id {
		t.Fatalf("ledger listing %v %v", listed, err)
	}
	record, err := db.Get([]byte(pinPrefix + id))
	if err != nil {
		t.Fatalf("pinPrefix does not match the ledger: %v", err)
	}
	want := sha256.Sum256(record)
	digest, err := pins.Verify(id, root)
	if err != nil || digest != hex.EncodeToString(want[:]) {
		t.Fatalf("digest %s %v", digest, err)
	}
	if _, err := pins.Verify(id, hexOf(common.HexToHash("0x02"))); !errors.Is(err, obligation.ErrPinRoot) {
		t.Fatalf("wrong root: %v", err)
	}
	if err := pins.Release(id); err != nil {
		t.Fatal(err)
	}
	if err := pins.Release(id); err != nil {
		t.Fatalf("second release: %v", err)
	}
	if left, _ := stateprovider.Pins(db); len(left) != 0 {
		t.Fatalf("pin left after release: %v", left)
	}
	if err := (ledger{db: db, provider: provider}).Readable(id); !errors.Is(err, stateprovider.ErrUnknownPin) {
		t.Fatalf("readable of an absent pin: %v", err)
	}
	if err := db.Put([]byte(pinPrefix+id), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := (ledger{db: db, provider: provider}).Record(id); !errors.Is(err, stateprovider.ErrInvalidPin) {
		t.Fatalf("malformed record: %v", err)
	}
}

func TestStartupRun(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	run, err := StartupRun(db, "bor", "run-1")
	if err != nil || run != "run-1" {
		t.Fatalf("first start: %s %v", run, err)
	}
	if run, err = StartupRun(db, "bor", "run-2"); err != nil || run != "run-1" {
		t.Fatalf("restart did not reopen the saved run: %s %v", run, err)
	}
	if run, err = StartupRun(db, "other", "run-3"); err != nil || run != "run-3" {
		t.Fatalf("another producer: %s %v", run, err)
	}
	if err := db.Put([]byte(runPrefix+"bad"), []byte(`{"run_id":"r","x":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := StartupRun(db, "bad", "run-1"); !errors.Is(err, ErrConfig) {
		t.Fatalf("malformed run record: %v", err)
	}
	if len(runPrefix+"b") <= 33 || runPrefix[0] != 'e' {
		t.Fatal("run keys could be deleted by the native offline pruner")
	}
}

func TestCheckConfig(t *testing.T) {
	config := params.AllEthashProtocolChanges
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := admission.CanonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Genesis: []byte(`{"a":1}`), ChainConfig: canonical}
	if err := checkConfig(cfg, config); err != nil {
		t.Fatalf("matching config: %v", err)
	}
	other := *config
	other.ChainID = big.NewInt(99)
	if err := checkConfig(cfg, &other); !errors.Is(err, ErrConfig) {
		t.Fatalf("another running config: %v", err)
	}
	cfg.Genesis = []byte(`{ "a": 1 }`)
	if err := checkConfig(cfg, config); !errors.Is(err, ErrConfig) {
		t.Fatalf("non-canonical genesis: %v", err)
	}
}

// loop runs PUSH1 40, then JUMPDEST, PUSH1 1, SWAP1, SUB, DUP1, PUSH1 2, JUMPI: 40 rounds.
var loop = common.FromHex("6028" + "5b" + "6001" + "90" + "03" + "80" + "6002" + "57" + "00")

func runLoop(t *testing.T, tracer *tracing.Hooks) []byte {
	t.Helper()
	out, _, err := runtime.Execute(loop, nil, &runtime.Config{GasLimit: 1_000_000, EVMConfig: vm.Config{Tracer: tracer}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSafePointWaitsForTheWorkerOutsideCallbacks(t *testing.T) {
	c, err := capture.New(capture.Config{ChunkBytes: 64, BufferBytes: 3 * (capture.FrameOverhead + 1)})
	if err != nil {
		t.Fatal(err)
	}
	sp := &SafePoint{Capturer: c, Ctx: context.Background(), Credit: capture.FrameOverhead + 1}
	var ops []byte
	done := make(chan error, 1)
	go func() {
		for {
			frame, err := c.Next(context.Background())
			if err != nil {
				done <- err
				return
			}
			ops = append(ops, frame.Data()...)
			time.Sleep(time.Millisecond)
			if err := c.Release(frame); err != nil {
				done <- err
				return
			}
		}
	}()
	traced := runLoop(t, sp.Hooks())
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("worker: %v", err)
	}
	calls, notes, err := sp.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if calls == 0 || notes != calls || len(ops) != calls {
		t.Fatalf("safe point calls %d, notes %d, worker ops %d", calls, notes, len(ops))
	}
	if c.Stats().Waits == 0 {
		t.Fatal("the producer never waited for the worker at a safe point")
	}
	if !bytes.Equal(ops[:2], []byte{byte(vm.PUSH1), byte(vm.JUMPDEST)}) || ops[len(ops)-1] != byte(vm.STOP) {
		t.Fatalf("opcode order %x", ops)
	}
	// Prefetchers clear the tracer: no safe point runs without it, and the
	// result is the same.
	if untraced := runLoop(t, nil); !bytes.Equal(untraced, traced) {
		t.Fatal("the safe point changed the execution result")
	}
}

func TestSafePointCaptureFailureLeavesExecutionUnchanged(t *testing.T) {
	c, err := capture.New(capture.Config{ChunkBytes: 64, BufferBytes: 4 * (capture.FrameOverhead + 1)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// No worker and a canceled context: the first full budget fails the wait.
	sp := &SafePoint{Capturer: c, Ctx: ctx, Credit: capture.FrameOverhead + 1}
	traced := runLoop(t, sp.Hooks())
	calls, _, err := sp.Stats()
	if err == nil || calls < 5 {
		t.Fatalf("calls %d err %v", calls, err)
	}
	if untraced := runLoop(t, nil); !bytes.Equal(untraced, traced) {
		t.Fatal("a capture failure changed the execution result")
	}
}

func TestLocalStateErrorsAreNotNativeVerdicts(t *testing.T) {
	if !core.ExtractorLocalStateUnavailable(&trie.MissingNodeError{}) {
		t.Fatal("a missing trie node counted as a native verdict")
	}
	if core.ExtractorLocalStateUnavailable(errors.New("invalid merkle root")) {
		t.Fatal("a validation failure counted as missing local state")
	}
}

func TestCapsuleNamesAndDigests(t *testing.T) {
	if len(ItemNames) != 11 || ItemNames[0] != ItemGenesis || ItemNames[10] != ItemNormalizationProfile {
		t.Fatalf("item names %v", ItemNames)
	}
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(5), Root: common.HexToHash("0x0a"),
		ReceiptHash: common.HexToHash("0x0b")})
	want := obligation.ResultDigest(hexOf(block.Hash()), hexOf(common.HexToHash("0x0a")), hexOf(common.HexToHash("0x0b")), "21000")
	if got := ResultDigest(block, 21000); got != want {
		t.Fatalf("result digest %s, want %s", got, want)
	}
	sum := sha256.Sum256([]byte(`{"gcmode":"archive","state_scheme":"hash","syncmode":"full"}`))
	if ProfileDigest() != hex.EncodeToString(sum[:]) {
		t.Fatal("profile digest")
	}
}
