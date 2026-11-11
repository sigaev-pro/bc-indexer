package native

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

const testStore = "0123456789abcdef0123456789abcdef"

// reader stores BLOCKHASH(NUMBER-k) for k = 1, 256, 257 in slots 0, 1, 2.
var reader = common.HexToAddress("0x00000000000000000000000000000000000000c1")

func readerCode() []byte {
	code := []byte{}
	for slot, back := range []uint16{1, 256, 257} {
		code = append(code, 0x61, byte(back>>8), byte(back), 0x43, 0x03, 0x40, 0x60, byte(slot), 0x55)
	}
	return append(code, 0x00)
}

type fixture struct {
	genesis *core.Genesis
	blocks  []*types.Block
	bad     *types.Block
}

// newFixture generates blocks 1..n; block n calls the reader. bad is block n
// with a wrong state root, which native validation rejects after execution.
func newFixture(t *testing.T, n int) fixture {
	t.Helper()
	key, _ := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	sender := crypto.PubkeyToAddress(key.PublicKey)
	config := *params.AllEthashProtocolChanges
	config.ChainID = big.NewInt(1337)
	genesis := &core.Genesis{Config: &config, GasLimit: 30_000_000, BaseFee: big.NewInt(params.InitialBaseFee),
		Difficulty: big.NewInt(1), Alloc: types.GenesisAlloc{
			sender: {Balance: new(big.Int).Lsh(big.NewInt(1), 100)},
			reader: {Balance: new(big.Int), Code: readerCode()},
		}}
	signer := types.LatestSigner(genesis.Config)
	sign := func(nonce uint64, to common.Address) *types.Transaction {
		tx, err := types.SignTx(types.NewTransaction(nonce, to, big.NewInt(1), 200_000, big.NewInt(2_000_000_000), nil), signer, key)
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	db, blocks, _ := core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), n-1, func(index int, block *core.BlockGen) {
		block.AddTx(sign(uint64(index), common.HexToAddress("0x1000000000000000000000000000000000000000")))
	})
	// The reader block answers BLOCKHASH from a separate generator chain.
	generator, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), genesis, ethash.NewFaker(), core.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generator.InsertChain(blocks); err != nil {
		t.Fatal(err)
	}
	last, _ := core.GenerateChain(genesis.Config, blocks[n-2], ethash.NewFaker(), db, 1, func(index int, block *core.BlockGen) {
		block.AddTxWithChain(generator, sign(uint64(n-1), reader))
	})
	generator.Stop()
	blocks = append(blocks, last...)
	header := blocks[n-1].Header()
	header.Root = common.Hash{1}
	bad := types.NewBlockWithHeader(header).WithBody(*blocks[n-1].Body())
	return fixture{genesis: genesis, blocks: blocks, bad: bad}
}

func (f fixture) config() Config {
	return Config{StoreID: testStore, ProducerID: "geth-test", ChainID: "1337", GenesisHash: f.genesis.ToBlock().Hash(),
		NativeCommit: strings.Repeat("ab", 20), Profile: "test-only-probe-v1"}
}

func newChain(t *testing.T, db ethdb.Database, f fixture) *core.BlockChain {
	t.Helper()
	chain, err := core.NewBlockChain(db, f.genesis, ethash.NewFaker(), core.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	return chain
}

// failingStore fails the first Put whose key ends with suffix.
type failingStore struct {
	ethdb.Database
	suffix string
	failed bool
}

func (s *failingStore) Put(key, value []byte) error {
	if !s.failed && strings.HasSuffix(string(key), s.suffix) {
		s.failed = true
		return errors.New("injected store failure")
	}
	return s.Database.Put(key, value)
}

func observations(t *testing.T, a *Admission) []admission.Observation {
	t.Helper()
	c, token := a.Core()
	defer token.Release()
	list, err := c.Observations(token)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func livePins(t *testing.T, db ethdb.Database) []string {
	t.Helper()
	pins, err := stateprovider.Pins(db)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, pin := range pins {
		ids = append(ids, pin.ID)
	}
	return ids
}

func TestUnitConfigIsStrict(t *testing.T) {
	f := newFixture(t, 2)
	dir := t.TempDir()
	good, _ := json.Marshal(f.config())
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := LoadConfig(path); err != nil || cfg != f.config() {
		t.Fatalf("valid config: %v %+v", err, cfg)
	}
	t.Setenv(ConfigEnv, "")
	if cfg, err := ConfigFromEnvironment(); cfg != nil || err != nil {
		t.Fatalf("unset environment: %v %v", cfg, err)
	}
	t.Setenv(ConfigEnv, path)
	if cfg, err := ConfigFromEnvironment(); err != nil || cfg == nil || *cfg != f.config() {
		t.Fatalf("environment config: %v %v", cfg, err)
	}
	bad := map[string]func(*Config){
		"store":    func(c *Config) { c.StoreID = "short" },
		"producer": func(c *Config) { c.ProducerID = "Upper" },
		"commit":   func(c *Config) { c.NativeCommit = "abc" },
		"profile":  func(c *Config) { c.Profile = "" },
		"genesis":  func(c *Config) { c.GenesisHash = common.Hash{} },
		"chain":    func(c *Config) { c.ChainID = "01" },
		"chain-0":  func(c *Config) { c.ChainID = "" },
	}
	for name, change := range bad {
		cfg := f.config()
		change(&cfg)
		if err := cfg.validate(); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	unknown := strings.Replace(string(good), "{", `{"extra":1,`, 1)
	if err := os.WriteFile(path, []byte(unknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); !errors.Is(err, ErrConfig) {
		t.Fatalf("unknown field: %v", err)
	}
	if err := os.WriteFile(path, append(good, []byte(" {}")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); !errors.Is(err, ErrConfig) {
		t.Fatalf("trailing data: %v", err)
	}
}

func TestUnitOpenRunKeepsTheSavedRun(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	run, err := OpenRun(db, testStore, "geth-test", "")
	if err != nil || !partPattern.MatchString(run) || run[0] != 'r' {
		t.Fatalf("first run: %q %v", run, err)
	}
	if again, err := OpenRun(db, testStore, "geth-test", "other-run"); err != nil || again != run {
		t.Fatalf("restart must open the saved run %q: %q %v", run, again, err)
	}
	if _, err := OpenRun(db, testStore, "geth-other", ""); !errors.Is(err, ErrRun) {
		t.Fatalf("another producer: %v", err)
	}
	if _, err := OpenRun(db, strings.Repeat("f", 32), "geth-test", ""); !errors.Is(err, ErrRun) {
		t.Fatalf("another store: %v", err)
	}
	if err := db.Put([]byte(runKey), []byte(`{"store_id":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRun(db, testStore, "geth-test", ""); !errors.Is(err, ErrRun) {
		t.Fatalf("malformed record: %v", err)
	}
	if _, err := OpenRun(rawdb.NewMemoryDatabase(), testStore, "geth-test", "Bad"); !errors.Is(err, ErrConfig) {
		t.Fatalf("invalid run ID: %v", err)
	}
	if len(runKey) == 32 || len(runKey) == 33 || runKey[0] == 'c' {
		t.Fatal("the run key could be deleted by the offline pruner")
	}
}

func TestUnitPinAdapter(t *testing.T) {
	f := newFixture(t, 3)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	defer chain.Stop()
	if _, err := chain.InsertChain(f.blocks[:2]); err != nil {
		t.Fatal(err)
	}
	identity, err := stateprovider.Bind(db, testStore)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := stateprovider.New(db, chain.TrieDB(), identity)
	if err != nil {
		t.Fatal(err)
	}
	pins := Pins{DB: db, Provider: provider, Identity: identity}
	head := chain.CurrentBlock()
	root := hex.EncodeToString(head.Root[:])
	if _, err := pins.Verify("geth-test.r1.0000000000000000", root); !errors.Is(err, stateprovider.ErrUnknownPin) {
		t.Fatalf("absent pin: %v", err)
	}
	if err := pins.Release("geth-test.r1.0000000000000000"); err != nil {
		t.Fatalf("an absent pin must release as nil: %v", err)
	}
	pin := stateprovider.Pin{ID: "geth-test.r1.0000000000000000", Store: identity, Profile: strings.Repeat("0", 64),
		ParentRoot: head.Root, Headers: []stateprovider.HeaderRef{{Hash: head.Hash(), Number: head.Number.Uint64()}}}
	if err := provider.Pin(pin); err != nil {
		t.Fatal(err)
	}
	listed, err := stateprovider.Pins(db)
	if err != nil || len(listed) != 1 {
		t.Fatalf("ledger: %v %v", listed, err)
	}
	raw, err := db.Get([]byte(pinPrefix + pin.ID))
	if err != nil {
		t.Fatalf("pin prefix differs from the ledger: %v", err)
	}
	want := sha256.Sum256(raw)
	if digest, err := pins.Verify(pin.ID, root); err != nil || digest != hex.EncodeToString(want[:]) {
		t.Fatalf("verify digest: %q %v", digest, err)
	}
	if _, err := pins.Verify(pin.ID, strings.Repeat("1", 64)); !errors.Is(err, stateprovider.ErrInvalidPin) {
		t.Fatalf("wrong root: %v", err)
	}
	other := pins
	other.Identity.StoreID = strings.Repeat("f", 32)
	if _, err := other.Verify(pin.ID, root); !errors.Is(err, stateprovider.ErrWrongStore) {
		t.Fatalf("other store: %v", err)
	}
	if err := db.Put([]byte(pinPrefix+"geth-test.r1.0000000000000001"), []byte(`{"id":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := pins.Verify("geth-test.r1.0000000000000001", root); !errors.Is(err, stateprovider.ErrInvalidPin) {
		t.Fatalf("malformed record: %v", err)
	}
	if err := db.Delete([]byte(pinPrefix + "geth-test.r1.0000000000000001")); err != nil {
		t.Fatal(err)
	}
	if err := pins.Release(pin.ID); err != nil {
		t.Fatal(err)
	}
	if ids := livePins(t, db); len(ids) != 0 {
		t.Fatalf("pin not released: %v", ids)
	}
	if err := pins.Release("Bad ID"); !errors.Is(err, stateprovider.ErrInvalidPin) {
		t.Fatalf("invalid ID must not be mapped to nil: %v", err)
	}
}

// TestUnitAbandonWithoutPin closes an INPUT_PREPARING observation whose pin
// was never written, through the real pin adapter.
func TestUnitAbandonWithoutPin(t *testing.T) {
	f := newFixture(t, 2)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	defer chain.Stop()
	identity, _ := stateprovider.Bind(db, testStore)
	provider, err := stateprovider.New(db, chain.TrieDB(), identity)
	if err != nil {
		t.Fatal(err)
	}
	guard := admission.NewGuard()
	token := guard.Acquire()
	defer token.Release()
	core, err := admission.Open(token, db, admission.Producer{ProducerID: "geth-test", RunID: "r1"},
		Pins{DB: db, Provider: provider, Identity: identity}, Inputs{Store: db})
	if err != nil {
		t.Fatal(err)
	}
	if err := core.GoLive(token); err != nil {
		t.Fatal(err)
	}
	genesis := chain.Genesis()
	obs, err := core.Begin(token, admission.Candidate{Number: 1, Hash: hexHash(f.blocks[0].Hash()),
		ParentHash: hexHash(genesis.Hash()), ParentRoot: hexHash(genesis.Root())})
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Abandon(token, obs.Key); err != nil {
		t.Fatalf("abandon without a pin: %v", err)
	}
}

func TestUnitInputsAdapter(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	inputs := Inputs{Store: db}
	if _, err := inputs.CapsuleDigest("geth-test.r1.0000000000000000"); !errors.Is(err, sourceinputs.ErrNoCapsule) {
		t.Fatalf("missing capsule: %v", err)
	}
	capsule, err := sourceinputs.SealCapsule(db, "geth-test.r1.0000000000000000", []sourceinputs.Item{{Name: "a", Data: []byte{1}}})
	if err != nil {
		t.Fatal(err)
	}
	if digest, err := inputs.CapsuleDigest(capsule.Observation); err != nil || digest != capsule.Digest {
		t.Fatalf("capsule digest: %q %v", digest, err)
	}
	if _, err := inputs.Consumed(capsule.Observation); !errors.Is(err, admission.ErrBinding) {
		t.Fatalf("open log: %v", err)
	}
	log, err := sourceinputs.OpenLog(db, capsule.Observation)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := log.Finish()
	if err != nil {
		t.Fatal(err)
	}
	got, err := inputs.Consumed(capsule.Observation)
	if err != nil || got.Digest != closed.Digest || got.CapsuleDigest != capsule.Digest || got.Count != 0 {
		t.Fatalf("consumed: %+v %v", got, err)
	}
	if _, err := inputs.Consumed("geth-test.r1.0000000000000001"); err == nil {
		t.Fatal("a log without a capsule must fail")
	}
}

func TestUnitCapsuleHoldsTheFullWindow(t *testing.T) {
	f := newFixture(t, 300)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	defer chain.Stop()
	if _, err := chain.InsertChain(f.blocks[:299]); err != nil {
		t.Fatal(err)
	}
	block := f.blocks[299]
	parent := chain.GetHeader(block.ParentHash(), 299)
	items, err := CapsuleItems(chain, block, parent, strings.Repeat("ab", 20), "test-only-probe-v1")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, item := range items {
		names = append(names, item.Name)
	}
	if len(items) != 6+255+4 || names[6] != "ancestor-header-44" || names[260] != "ancestor-header-298" ||
		names[3] != itemParent || names[len(names)-1] != itemProfile {
		t.Fatalf("capsule layout: %d items, %v ... %v", len(items), names[:8], names[len(names)-5:])
	}
	answers, err := blockHashAnswers(items, 300)
	if err != nil || len(answers) != 256 {
		t.Fatalf("answers: %d %v", len(answers), err)
	}
	for number := uint64(44); number < 300; number++ {
		if answers[number] != chain.GetHeaderByNumber(number).Hash() {
			t.Fatalf("answer %d differs from the chain", number)
		}
	}
	if _, ok := answers[43]; ok {
		t.Fatal("block 43 is outside the window")
	}
	for _, item := range items {
		if item.Name == itemConfig {
			canonical, err := admission.CanonicalJSON(item.Data)
			if err != nil || string(canonical) != string(item.Data) {
				t.Fatalf("chain config is not canonical: %v", err)
			}
		}
	}
	// A short, reordered or misnamed window is refused.
	broken := append([]sourceinputs.Item{}, items[:6]...)
	broken = append(broken, items[7:]...)
	if _, err := blockHashAnswers(broken, 300); !errors.Is(err, ErrCapsule) {
		t.Fatalf("short window: %v", err)
	}
	swapped := append([]sourceinputs.Item{}, items...)
	swapped[6], swapped[7] = swapped[7], swapped[6]
	if _, err := blockHashAnswers(swapped, 300); !errors.Is(err, ErrCapsule) {
		t.Fatalf("reordered window: %v", err)
	}
	renamed := append([]sourceinputs.Item{}, items...)
	renamed[6] = sourceinputs.Item{Name: "ancestor-header-45", Data: items[6].Data}
	if _, err := blockHashAnswers(renamed, 300); !errors.Is(err, ErrCapsule) {
		t.Fatalf("misnamed window: %v", err)
	}
	// Below the full window, the capsule starts at the genesis.
	early, err := CapsuleItems(chain, f.blocks[4], chain.GetHeaderByNumber(4), strings.Repeat("ab", 20), "p")
	if err != nil {
		t.Fatal(err)
	}
	if answers, err := blockHashAnswers(early, 5); err != nil || len(answers) != 5 || answers[0] != chain.Genesis().Hash() {
		t.Fatalf("early window: %v", err)
	}
}

func TestUnitImportPathAndRecovery(t *testing.T) {
	f := newFixture(t, 300)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	if _, err := chain.InsertChain(f.blocks[:299]); err != nil {
		t.Fatal(err)
	}
	events := []string{}
	trace := func(event string, fields map[string]any) { events = append(events, event) }
	a, err := Attach(chain, db, f.config(), Options{RunID: "r1", Trace: trace})
	if err != nil {
		t.Fatal(err)
	}
	// A natively invalid block is rejected after execution, never EXEC_READY.
	if _, err := chain.InsertChain(types.Blocks{f.bad}); err == nil {
		t.Fatal("the bad block was imported")
	}
	if list := observations(t, a); len(list) != 0 {
		t.Fatalf("a rejected observation stays listed: %+v", list)
	}
	if ids := livePins(t, db); len(ids) != 0 {
		t.Fatalf("a rejected observation keeps its pin: %v", ids)
	}
	if _, err := chain.InsertChain(f.blocks[299:]); err != nil {
		t.Fatal(err)
	}
	list := observations(t, a)
	if len(list) != 1 || list[0].State != admission.ExecReady || list[0].Key != "geth-test.r1.0000000000000001" {
		t.Fatalf("observations: %+v", list)
	}
	want := strings.Join([]string{"opened", "live", "begin", "pinned", "sealed", "admitted", "lookup", "lookup",
		"rejected", "begin", "pinned", "sealed", "admitted", "lookup", "lookup", "exec_ready"}, ",")
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	state, err := chain.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.GetState(reader, common.Hash{}) != f.blocks[298].Hash() ||
		state.GetState(reader, common.BigToHash(big.NewInt(1))) != f.blocks[43].Hash() ||
		state.GetState(reader, common.BigToHash(big.NewInt(2))) != (common.Hash{}) {
		t.Fatal("BLOCKHASH answers differ from the chain")
	}
	// The retention guard of the linked binary refuses a rewind below the pin.
	if err := chain.SetHead(200); !errors.Is(err, stateprovider.ErrLivePins) {
		t.Fatalf("rewind below a pin: %v", err)
	}
	chain.Stop()
}

func TestUnitRecoveryCompletesTheSavedObligation(t *testing.T) {
	f := newFixture(t, 5)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	if _, err := chain.InsertChain(f.blocks[:4]); err != nil {
		t.Fatal(err)
	}
	// The EXEC_READY write fails after the closing ADMITTED sync, so the
	// obligation stays ADMITTED, as after a power loss right after admission.
	store := &failingStore{Database: db, suffix: "/exec-ready"}
	a, err := Attach(chain, db, f.config(), Options{Store: store, RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chain.InsertChain(f.blocks[4:]); err == nil {
		t.Fatal("the import passed a failed EXEC_READY write")
	}
	chain.Stop()
	if _, err := a.core.Observations(a.guard.Acquire()); !errors.Is(err, admission.ErrFailed) {
		t.Fatalf("the admission must stop after a store error: %v", err)
	}

	chain = newChain(t, db, f)
	defer chain.Stop()
	held, err := Attach(chain, db, f.config(), Options{RunID: "r9", SkipRecovery: true})
	if err != nil {
		t.Fatal(err)
	}
	if held.Producer().RunID != "r1" {
		t.Fatalf("restart opened run %q, not the saved run", held.Producer().RunID)
	}
	// RECOVERY_ONLY: another block (a new H) is refused, GoLive is refused.
	if _, err := chain.InsertChain(types.Blocks{f.bad}); !errors.Is(err, admission.ErrMode) {
		t.Fatalf("new H in RECOVERY_ONLY: %v", err)
	}
	c, token := held.Core()
	if err := c.GoLive(token); !errors.Is(err, admission.ErrPending) {
		t.Fatalf("GoLive with an open obligation: %v", err)
	}
	pending, err := c.Observations(token)
	token.Release()
	if err != nil || len(pending) != 1 || pending[0].State != admission.Admitted {
		t.Fatalf("after restart: %+v %v", pending, err)
	}
	admitted := *pending[0].Admitted
	if err := held.Recover(); err != nil {
		t.Fatal(err)
	}
	list := observations(t, held)
	if len(list) != 1 || list[0].State != admission.ExecReady || list[0].Key != admitted.Observation ||
		list[0].Admitted.Digest != admitted.Digest {
		t.Fatalf("recovered: %+v", list)
	}
	if chain.CurrentBlock().Hash() != f.blocks[4].Hash() {
		t.Fatal("recovery did not import the saved block")
	}
}

func TestUnitRecoveryRejectsASavedInvalidBlock(t *testing.T) {
	f := newFixture(t, 5)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	if _, err := chain.InsertChain(f.blocks[:4]); err != nil {
		t.Fatal(err)
	}
	store := &failingStore{Database: db, suffix: "/rejected"}
	if _, err := Attach(chain, db, f.config(), Options{Store: store, RunID: "r1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := chain.InsertChain(types.Blocks{f.bad}); err == nil {
		t.Fatal("the bad block was imported")
	}
	chain.Stop()
	chain = newChain(t, db, f)
	defer chain.Stop()
	a, err := Attach(chain, db, f.config(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if list := observations(t, a); len(list) != 0 {
		t.Fatalf("recovery left %+v", list)
	}
	if ids := livePins(t, db); len(ids) != 0 {
		t.Fatalf("a rejected obligation keeps its pin: %v", ids)
	}
	if chain.CurrentBlock().Number.Uint64() != 4 {
		t.Fatal("the bad block was imported in recovery")
	}
	if _, err := chain.InsertChain(f.blocks[4:]); err != nil {
		t.Fatalf("LIVE after recovery: %v", err)
	}
}

func TestUnitRecoveryAbandonsPreparing(t *testing.T) {
	f := newFixture(t, 5)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	if _, err := chain.InsertChain(f.blocks[:4]); err != nil {
		t.Fatal(err)
	}
	// The ADMITTED write fails, so the observation stays INPUT_PREPARING with
	// a pin and a capsule; the admission stops.
	store := &failingStore{Database: db, suffix: "/admitted"}
	if _, err := Attach(chain, db, f.config(), Options{Store: store, RunID: "r1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := chain.InsertChain(f.blocks[4:]); err == nil {
		t.Fatal("the import passed a failed ADMITTED write")
	}
	if ids := livePins(t, db); len(ids) != 1 {
		t.Fatalf("pins before restart: %v", ids)
	}
	chain.Stop()
	chain = newChain(t, db, f)
	defer chain.Stop()
	a, err := Attach(chain, db, f.config(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if ids := livePins(t, db); len(ids) != 0 {
		t.Fatalf("an abandoned observation keeps its pin: %v", ids)
	}
	if _, err := chain.InsertChain(f.blocks[4:]); err != nil {
		t.Fatal(err)
	}
	list := observations(t, a)
	if len(list) != 1 || list[0].State != admission.ExecReady || list[0].Sequence != 1 {
		t.Fatalf("after restart: %+v", list)
	}
}

func TestUnitImportWithoutGuardIsRefused(t *testing.T) {
	f := newFixture(t, 2)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	defer chain.Stop()
	a, err := Attach(chain, db, f.config(), Options{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Begin(f.blocks[0], chain.Genesis().Root()); !errors.Is(err, ErrGuardMissing) {
		t.Fatalf("Begin without the guard: %v", err)
	}
}

func TestUnitAttachRefusesAnotherIdentity(t *testing.T) {
	f := newFixture(t, 2)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	defer chain.Stop()
	cfg := f.config()
	cfg.ChainID = "1"
	if _, err := Attach(chain, db, cfg, Options{}); !errors.Is(err, stateprovider.ErrWrongStore) {
		t.Fatalf("another chain: %v", err)
	}
	cfg = f.config()
	if _, err := Attach(chain, db, cfg, Options{}); err != nil {
		t.Fatal(err)
	}
	cfg.StoreID = strings.Repeat("f", 32)
	if _, err := Attach(chain, db, cfg, Options{}); !errors.Is(err, stateprovider.ErrWrongStore) {
		t.Fatalf("another store ID: %v", err)
	}
	cfg = f.config()
	cfg.ProducerID = "geth-other"
	if _, err := Attach(chain, db, cfg, Options{}); !errors.Is(err, ErrRun) {
		t.Fatalf("another producer: %v", err)
	}
}

func TestUnitDigests(t *testing.T) {
	block := common.HexToHash("0x01")
	data := append([]byte(tagRejection), 0)
	data = append(data, block.Bytes()...)
	data = append(data, 0, 0, 0, 0, 0, 0, 0, 3)
	want := sha256.Sum256(append(data, "bad"...))
	if RejectionDigest(block, errors.New("bad")) != hex.EncodeToString(want[:]) {
		t.Fatal("rejection digest differs from its formula")
	}
	b := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})
	receipts := types.Receipts{{Status: 1, CumulativeGasUsed: 21000, Logs: []*types.Log{}}}
	encoded, _ := rlp.EncodeToBytes(receipts)
	got, err := ExecResultDigest(b, &core.ProcessResult{Receipts: receipts, GasUsed: 21000})
	data = append([]byte(tagExecResult), 0)
	data = append(data, b.Hash().Bytes()...)
	data = append(data, 0, 0, 0, 0, 0, 0, 0x52, 0x08)
	data = binary.BigEndian.AppendUint64(data, uint64(len(encoded)))
	want = sha256.Sum256(append(data, encoded...))
	if err != nil || got != hex.EncodeToString(want[:]) {
		t.Fatalf("exec digest: %v", err)
	}
}

func TestUnitRetentionGuardsAreInstalled(t *testing.T) {
	if core.ExtractorRewindAllowed == nil || rawdb.ExtractorFreezeLimit == nil {
		t.Fatal("linking this package must install the retention guards")
	}
	dir := t.TempDir()
	db := rawdb.NewMemoryDatabase()
	if err := RecoverPruning(db, dir); err != nil {
		t.Fatalf("no interrupted prune: %v", err)
	}
}
