package bsclifecycle_test

// The native probe of the BSC block of E01-02.lifecycle_seams on the private
// Parlia fixture (chain 714). The driver
// (adapters/bsc/lifecycle/native/bsc_lifecycle.py) runs TestBSCLifecycleProbe
// in the pinned compiler image and judges report.json with its own rules.
//
// Blocks: A1..A7 extend genesis, S3 forks from A2 and S4 extends S3; every
// block holds one transfer of public test key 1, so every state root
// differs. Native Parlia seals them with the public test validator key.
//
// Paths:
//   - native caller: the shipped node (eth.New with the capture config of
//     EXTRACTOR_BSC_CONFIG) and its RPC APIs (admin_importChain,
//     debug_setHead, debug_executionWitness), StartMining, node close and
//     restart;
//   - harness: a direct BlockChain with bsclifecycle.Attach and direct calls
//     of the same native methods.
//
// Both run the same scenario; the report holds the decisions and native
// results of each step. Further scenarios: the attestation lookup error,
// the deadlock smoke, the crash points of a SetHead decision (child
// processes), the native event feeds against a reference chain without the
// extractor, and the refusals outside the gate.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/parlia"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/bsc"
	bscdecision "github.com/ethereum/go-ethereum/extractor/bsc/decision"
	bsclifecycle "github.com/ethereum/go-ethereum/extractor/bsc/lifecycle"
	"github.com/ethereum/go-ethereum/extractor/bsc/retention"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
)

const (
	envInputs    = "EXTRACTOR_LIFECYCLE_INPUTS"
	envWork      = "EXTRACTOR_LIFECYCLE_WORK"
	envCommit    = "EXTRACTOR_LIFECYCLE_NATIVE_COMMIT"
	envChildMode = "EXTRACTOR_LIFECYCLE_CHILD_MODE"
	envChildHome = "EXTRACTOR_LIFECYCLE_CHILD_HOME"
	probeStoreID = "0123456789abcdef0123456789abcdef"
	validator    = "0x03735c2ed70a56cd221e0024eb4bf90243c9d6e9"
	recipient    = "0x000000000000000000000000000000000000beef"
	childExit    = 3
)

var probeProducer = admission.Producer{ProducerID: "bsc-lifecycle", RunID: "run-1"}

// blockOrder is the order of the blocks in blocks.rlp.
var blockOrder = []string{"A1", "A2", "A3", "A4", "A5", "A6", "A7", "S3", "S4"}

func probeEnv(t testing.TB, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}

func errText(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

func hexHash(h common.Hash) string { return hex.EncodeToString(h.Bytes()) }

// ---- fixture and blocks ----

type fixture struct {
	genesisPath string
	genesisFile []byte
	genesis     *core.Genesis
	key         *keystore.Key
	commit      string
	blocks      map[string]*types.Block
	names       map[common.Hash]string
}

func loadFixture(t testing.TB) *fixture {
	t.Helper()
	inputs := probeEnv(t, envInputs)
	fx := &fixture{genesisPath: filepath.Join(inputs, "genesis.json"), commit: probeEnv(t, envCommit),
		blocks: map[string]*types.Block{}, names: map[common.Hash]string{}}
	var err error
	if fx.genesisFile, err = os.ReadFile(fx.genesisPath); err != nil {
		t.Fatal(err)
	}
	fx.genesis = new(core.Genesis)
	if err := json.Unmarshal(fx.genesisFile, fx.genesis); err != nil {
		t.Fatal(err)
	}
	if fx.genesis.Config.ChainID.Cmp(big.NewInt(714)) != 0 || fx.genesis.Config.Parlia == nil {
		t.Fatal("not the private Parlia chain 714")
	}
	data, err := os.ReadFile(filepath.Join(inputs, "validator-keystore.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fx.key, err = keystore.DecryptKey(data, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(fx.key.Address.Hex(), validator) {
		t.Fatal("public validator identity mismatch")
	}
	fx.generate(t)
	return fx
}

// generate seals the probe blocks with native Parlia on an in-memory
// archive chain. Timestamps are the parent's plus the period (S3 one second
// later than A3), so the blocks are the same in every run.
func (fx *fixture) generate(t testing.TB) {
	t.Helper()
	db := rawdb.NewMemoryDatabase()
	api, backend := bsc.NewSystemCallAPI()
	engine := parlia.New(fx.genesis.Config, db, api, fx.genesis.ToBlock().Hash())
	defer engine.Close()
	engine.Authorize(fx.key.Address,
		func(account accounts.Account, mime string, data []byte) ([]byte, error) {
			return crypto.Sign(crypto.Keccak256(data), fx.key.PrivateKey)
		},
		func(account accounts.Account, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error) {
			return types.SignTx(tx, types.LatestSignerForChainID(chainID), fx.key.PrivateKey)
		})
	config := core.DefaultConfig()
	config.StateScheme, config.ArchiveMode, config.SnapshotLimit = rawdb.HashScheme, true, 0
	chain, err := core.NewBlockChain(db, fx.genesis, engine, config)
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()
	backend.Bind(chain, nil)
	sender, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	fx.names[chain.Genesis().Hash()] = "G"
	seal := func(name string, parent *types.Header, offset uint64, value int64) {
		header := &types.Header{ParentHash: parent.Hash(), Number: new(big.Int).Add(parent.Number, big.NewInt(1)),
			GasLimit: parent.GasLimit, Time: parent.Time + 3, Difficulty: big.NewInt(1)}
		if err := engine.Prepare(chain, header); err != nil {
			t.Fatal(err)
		}
		header.Time = parent.Time + offset
		statedb, err := chain.StateAt(parent)
		if err != nil {
			t.Fatal(err)
		}
		signer := types.MakeSigner(fx.genesis.Config, header.Number, header.Time)
		nonce := statedb.GetNonce(crypto.PubkeyToAddress(sender.PublicKey))
		tx, err := types.SignTx(types.NewTransaction(nonce, common.HexToAddress(recipient), big.NewInt(value), 21_000,
			big.NewInt(5_000_000_000), nil), signer, sender)
		if err != nil {
			t.Fatal(err)
		}
		pool := core.NewGasPool(header.GasLimit)
		evm := vm.NewEVM(core.NewEVMBlockContext(header, chain, &header.Coinbase), statedb, fx.genesis.Config,
			vm.Config{})
		statedb.SetTxContext(tx.Hash(), 0)
		receipt, err := core.ApplyTransaction(evm, pool, statedb, header, tx, core.NewReceiptBloomGenerator())
		if err != nil {
			t.Fatal(err)
		}
		header.GasUsed = pool.Used()
		block, _, err := core.AssembleBlock(engine, chain, header, statedb, &types.Body{Transactions: types.Transactions{tx}},
			[]*types.Receipt{receipt})
		if err != nil {
			t.Fatal(err)
		}
		result, stop := make(chan *types.Block, 1), make(chan struct{})
		if err := engine.Seal(chain, block, result, stop); err != nil {
			t.Fatal(err)
		}
		select {
		case block = <-result:
		case <-time.After(30 * time.Second):
			t.Fatal("seal deadline")
		}
		close(stop)
		if _, err := chain.InsertChain(types.Blocks{block}); err != nil {
			t.Fatalf("generated block %s: %v", name, err)
		}
		fx.blocks[name] = block
		fx.names[block.Hash()] = name
	}
	parent := chain.Genesis().Header()
	for i := 1; i <= 7; i++ {
		seal(fmt.Sprintf("A%d", i), parent, 3, int64(i))
		parent = fx.blocks[fmt.Sprintf("A%d", i)].Header()
	}
	seal("S3", fx.blocks["A2"].Header(), 4, 1003)
	seal("S4", fx.blocks["S3"].Header(), 3, 1004)
	if chain.CurrentBlock().Hash() != fx.blocks["A7"].Hash() {
		t.Fatal("the generator head is not A7")
	}
}

func (fx *fixture) chainOf(names ...string) types.Blocks {
	var out types.Blocks
	for _, name := range names {
		out = append(out, fx.blocks[name])
	}
	return out
}

func (fx *fixture) name(hash string) string {
	raw, err := hex.DecodeString(hash)
	if err != nil {
		return hash
	}
	if name, ok := fx.names[common.BytesToHash(raw)]; ok {
		return name
	}
	if hash == lifecycle.ZeroHash {
		return "zero"
	}
	return hash
}

// ---- stores ----

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

// store is one capture store: the shipped node or the harness chain.
type store interface {
	chain() *core.BlockChain
	store() ethdb.Database
	life() *bsclifecycle.Lifecycle
	importNew(names ...string) error
	importKnown(names ...string) error
	setHead(number uint64) error
	close()
}

// nodeStore is the shipped node (eth.New with the capture config).
type nodeStore struct {
	fx    *fixture
	work  string
	stack *node.Node
	eth   *eth.Ethereum
}

func writeConfig(t testing.TB, fx *fixture, path string) {
	t.Helper()
	data, err := json.Marshal(bsclifecycle.FileConfig{StoreID: probeStoreID, ProducerID: probeProducer.ProducerID,
		RunID: probeProducer.RunID, NativeCommit: fx.commit, GenesisFile: fx.genesisPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func openNode(fx *fixture, home string, mutate func(*ethconfig.Config)) (*nodeStore, error) {
	stack, err := node.New(&node.Config{Name: "geth", DataDir: home, DBEngine: "pebble",
		P2P: p2p.Config{MaxPeers: 0, NoDiscovery: true, ListenAddr: ""}})
	if err != nil {
		return nil, err
	}
	cfg := ethconfig.Defaults
	cfg.Genesis = fx.genesis
	cfg.NetworkId = fx.genesis.Config.ChainID.Uint64()
	cfg.SyncMode = ethconfig.FullSync
	cfg.StateScheme = rawdb.HashScheme
	cfg.SnapshotCache = 0
	cfg.LogNoHistory = true
	if mutate != nil {
		mutate(&cfg)
	}
	backend, err := eth.New(stack, &cfg)
	if err != nil {
		stack.Close()
		return nil, err
	}
	if err := stack.Start(); err != nil {
		stack.Close()
		return nil, err
	}
	return &nodeStore{fx: fx, work: home, stack: stack, eth: backend}, nil
}

func (s *nodeStore) chain() *core.BlockChain       { return s.eth.BlockChain() }
func (s *nodeStore) store() ethdb.Database         { return s.eth.ChainDb() }
func (s *nodeStore) life() *bsclifecycle.Lifecycle { return bsclifecycle.Of(s.chain()) }
func (s *nodeStore) close()                        { s.stack.Close() }

// importNew is admin_importChain of an RLP file of the blocks.
func (s *nodeStore) importNew(names ...string) error {
	var buffer bytes.Buffer
	for _, block := range s.fx.chainOf(names...) {
		if err := rlp.Encode(&buffer, block); err != nil {
			return err
		}
	}
	path := filepath.Join(s.work, "import-"+strings.Join(names, "-")+".rlp")
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		return err
	}
	_, err := eth.NewAdminAPI(s.eth).ImportChain(path)
	return err
}

// importKnown is InsertChain, the method that the block fetcher and the
// downloader call; admin_importChain skips a batch of stored blocks.
func (s *nodeStore) importKnown(names ...string) error {
	_, err := s.chain().InsertChain(s.fx.chainOf(names...))
	return err
}

// setHead is debug_setHead.
func (s *nodeStore) setHead(number uint64) error {
	return ethapi.NewDebugAPI(s.eth.APIBackend).SetHead(hexutil.Uint64(number))
}

// harnessStore is a direct BlockChain with the lifecycle attached.
type harnessStore struct {
	fx *fixture
	db ethdb.Database
	ch *core.BlockChain
	l  *bsclifecycle.Lifecycle
}

func openHarness(fx *fixture, home string, opts bsclifecycle.Options, emit func(bsc.Event)) (*harnessStore, error) {
	db := openDiskNoFatal(home)
	if db == nil {
		return nil, errors.New("cannot open the harness store")
	}
	config := core.DefaultConfig()
	config.StateScheme, config.ArchiveMode, config.SnapshotLimit = rawdb.HashScheme, false, 0
	api, backend := bsc.NewSystemCallAPI()
	engine := parlia.New(fx.genesis.Config, db, api, fx.genesis.ToBlock().Hash())
	chain, err := core.NewBlockChain(db, fx.genesis, engine, config)
	if err != nil {
		db.Close()
		return nil, err
	}
	l, err := bsclifecycle.Attach(chain, db, backend, bsclifecycle.Config{Producer: probeProducer,
		StoreID: probeStoreID, NativeCommit: fx.commit, GenesisFile: fx.genesisFile, Emit: emit}, opts)
	if err != nil {
		chain.Stop()
		db.Close()
		return nil, err
	}
	return &harnessStore{fx: fx, db: db, ch: chain, l: l}, nil
}

func openDiskNoFatal(home string) ethdb.Database {
	chaindata := filepath.Join(home, "geth", "chaindata")
	kv, err := pebble.New(chaindata, 64, 64, "", false)
	if err != nil {
		return nil
	}
	db, err := rawdb.Open(kv, rawdb.OpenOptions{Ancient: filepath.Join(chaindata, "ancient")})
	if err != nil {
		kv.Close()
		return nil
	}
	return db
}

func (s *harnessStore) chain() *core.BlockChain       { return s.ch }
func (s *harnessStore) store() ethdb.Database         { return s.db }
func (s *harnessStore) life() *bsclifecycle.Lifecycle { return s.l }
func (s *harnessStore) close() {
	s.ch.Stop()
	s.db.Close()
}

// importNew imports one block per InsertChain call.
func (s *harnessStore) importNew(names ...string) error {
	for _, block := range s.fx.chainOf(names...) {
		if _, err := s.ch.InsertChain(types.Blocks{block}); err != nil {
			return err
		}
	}
	return nil
}

func (s *harnessStore) importKnown(names ...string) error {
	_, err := s.ch.InsertChain(s.fx.chainOf(names...))
	return err
}

func (s *harnessStore) setHead(number uint64) error { return s.ch.SetHead(number) }

// ---- records ----

type decisionRow struct {
	Sequence uint64   `json:"sequence"`
	State    string   `json:"state"`
	Kind     string   `json:"kind"`
	Old      string   `json:"old"`
	New      string   `json:"new"`
	Members  []string `json:"members"`
	Marker   bool     `json:"marker"`
	Evidence string   `json:"evidence_digest"`
	Intent   string   `json:"intent_digest"`
	OldHead  any      `json:"old_head"`
	NewHead  any      `json:"new_head"`
}

func guarded(l *bsclifecycle.Lifecycle, f func(adm *admission.Admission, token *admission.Token) error) error {
	release := l.Hook().Seam().Acquire()
	defer release()
	adm, token := l.Hook().Seam().Admission()
	return f(adm, token)
}

func decisions(fx *fixture, l *bsclifecycle.Lifecycle) []decisionRow {
	rows := []decisionRow{}
	bridge := l.Hook().Bridge()
	if bridge == nil {
		return rows
	}
	_ = guarded(l, func(_ *admission.Admission, token *admission.Token) error {
		ds, err := bridge.Decisions(token)
		if err != nil {
			return err
		}
		for _, d := range ds {
			row := decisionRow{Sequence: d.Sequence, State: string(d.State), Kind: string(d.Intent.Kind),
				Old: fx.name(d.Intent.OldHead.Hash), New: fx.name(d.Intent.NewHead.Hash), Members: []string{},
				Marker: d.Marker, Evidence: d.Intent.EvidenceDigest, Intent: d.Intent.Digest,
				OldHead: d.Intent.OldHead, NewHead: d.Intent.NewHead}
			for _, m := range d.Intent.Members {
				row.Members = append(row.Members, fx.name(m.Block))
			}
			rows = append(rows, row)
		}
		return nil
	})
	return rows
}

type observationRow struct {
	Key   string `json:"key"`
	State string `json:"state"`
	Block string `json:"block"`
}

func observations(fx *fixture, l *bsclifecycle.Lifecycle) []observationRow {
	rows := []observationRow{}
	_ = guarded(l, func(adm *admission.Admission, token *admission.Token) error {
		list, err := adm.Observations(token)
		for _, obs := range list {
			rows = append(rows, observationRow{Key: obs.Key, State: string(obs.State), Block: fx.name(obs.Candidate.Hash)})
		}
		return err
	})
	return rows
}

// errorClass names the sentinel of an error. An RPC method (for example
// admin_importChain) flattens the error into its text, so the text of the
// sentinel also counts.
func errorClass(err error) string {
	if err == nil {
		return ""
	}
	classes := []struct {
		name     string
		sentinel error
	}{{"ErrCorpus", bsclifecycle.ErrCorpus}, {"ErrRefused", lifecycle.ErrRefused}, {"ErrSource", lifecycle.ErrSource},
		{"ErrImportProfile", bsc.ErrImportProfile}, {"ErrExtractorWitness", core.ErrExtractorWitness},
		{"ErrBypass", lifecycle.ErrBypass}, {"ErrPhase", lifecycle.ErrPhase}, {"ErrLivePins", stateprovider.ErrLivePins},
		{"ErrProfile", stateprovider.ErrProfile}, {"ErrEngine", bsc.ErrEngine}}
	for _, c := range classes {
		if errors.Is(err, c.sentinel) || strings.Contains(err.Error(), c.sentinel.Error()) {
			return c.name
		}
	}
	return "other"
}

type step struct {
	Name       string            `json:"name"`
	Error      any               `json:"error"`
	Class      string            `json:"class"`
	Phase      string            `json:"phase"`
	Head       string            `json:"head"`
	HeadNumber uint64            `json:"head_number"`
	Finalized  string            `json:"finalized"`
	Canonical  []string          `json:"canonical"`
	New        []decisionRow     `json:"new_decisions"`
	Open       []observationRow  `json:"open_observations"`
	View       map[string]any    `json:"view"`
	Extra      map[string]any    `json:"extra,omitempty"`
	Coverage   map[string][3]int `json:"-"`
}

// recorder records the steps of one path.
type recorder struct {
	fx    *fixture
	s     store
	steps []step
	seen  int
}

func (r *recorder) record(name string, err error, extra map[string]any) step {
	chain := r.s.chain()
	l := r.s.life()
	head := chain.CurrentBlock()
	st := step{Name: name, Error: errText(err), Class: errorClass(err), Phase: string(l.Gate().Phase()),
		Head: r.fx.name(hexHash(head.Hash())), HeadNumber: head.Number.Uint64(),
		Finalized: r.fx.name(hexHash(rawdb.ReadFinalizedBlockHash(r.s.store()))), Extra: extra}
	for n := uint64(0); n <= head.Number.Uint64(); n++ {
		st.Canonical = append(st.Canonical, r.fx.name(hexHash(chain.GetCanonicalHash(n))))
	}
	all := decisions(r.fx, l)
	if len(all) < r.seen {
		r.seen = 0
	}
	st.New = all[r.seen:]
	r.seen = len(all)
	st.Open = observations(r.fx, l)
	if viewHead, viewFinal, ok, verr := l.ReadView(); verr == nil && ok {
		st.View = map[string]any{"head": r.fx.name(viewHead.Hash)}
		if viewFinal != nil {
			st.View["finalized"] = r.fx.name(viewFinal.Hash)
		}
	}
	r.steps = append(r.steps, st)
	return st
}

// coverage adds the gate coverage of a store to total.
func addCoverage(total map[string]lifecycle.Reach, l *bsclifecycle.Lifecycle) {
	for id, reach := range l.Gate().Coverage() {
		t := total[id]
		t.Passes += reach.Passes
		t.Intents += reach.Intents
		t.Refusals += reach.Refusals
		total[id] = t
	}
}

// ---- the shared scenario ----

type driver struct {
	t        *testing.T
	fx       *fixture
	r        *recorder
	reopen   func() (store, error)
	total    map[string]lifecycle.Reach
	beforeUp func(home string)
}

func (d *driver) restart(name string, fault func()) {
	exits := d.r.s.life().ExitErrors()
	d.r.s.close()
	// The shutdown decision runs during close, so coverage is read after it.
	addCoverage(d.total, d.r.s.life())
	if fault != nil {
		fault()
	}
	s, err := d.reopen()
	if err != nil {
		d.t.Fatalf("%s: %v", name, err)
	}
	d.r.s = s
	d.r.record(name, nil, map[string]any{"exit_errors_before": exits})
}

// shared runs the scenario that both paths run. It returns the index of
// the first step.
func (d *driver) shared(home string) {
	fx, r := d.fx, d.r
	s := func() store { return r.s }
	r.record("start (BOOTSTRAP)", nil, nil)
	r.record("import A1 A2 A3 (BOOTSTRAP)", s().importNew("A1", "A2", "A3"), nil)
	r.record("import S3 (BOOTSTRAP, side block)", s().importNew("S3"), nil)
	r.record("setHead 2 (BOOTSTRAP)", s().setHead(2), nil)
	_, err := s().life().GoLive()
	r.record("GoLive (H)", err, nil)
	r.record("fixture: S3 stored above the head, as a snap-sync rollback leaves it", rollbackRemnant(fx, s(), "S3"),
		map[string]any{"s3_has_state": s().chain().HasState(fx.blocks["S3"].Root()),
			"s3_known_with_state": s().chain().HasBlockAndState(fx.blocks["S3"].Hash(), 3)})
	r.record("InsertChain S3 (known block)", s().importKnown("S3"), nil)
	r.record("import A3 A4 A5", s().importNew("A3", "A4", "A5"), nil)
	r.record("setHead 4", s().setHead(4), nil)
	r.record("import A5 again", s().importNew("A5"), nil)
	d.restart("restart 1 (shutdown, RECOVERY, live at H)", nil)
	r.record("import S4 (side branch, pruned parent S3)", s().importNew("S4"), map[string]any{
		"s3_stored": s().chain().HasBlock(fx.blocks["S3"].Hash(), 3), "s3_has_state": s().chain().HasState(
			fx.blocks["S3"].Root())})
	r.record("import A6 A7", s().importNew("A6", "A7"), nil)
	d.restart("restart 2 (A7 state root deleted: startup repair and reconciliation)", func() {
		db := openDisk(d.t, home)
		rawdb.DeleteLegacyTrieNode(db, fx.blocks["A7"].Root())
		if err := db.SyncKeyValue(); err != nil {
			d.t.Fatal(err)
		}
		db.Close()
	})
	r.record("InsertChain A7 (stored, its state deleted)", s().importKnown("A7"), nil)
}

// rollbackRemnant writes the block data of a block that the native rewind
// deleted (header, body, total difficulty), as the snap-sync rollback that
// the native known-block loop names leaves it: a stored block with its state
// above the head. The state is the native state that the rewind kept in the
// trie database; the fixture writes no state.
func rollbackRemnant(fx *fixture, s store, name string) error {
	block := fx.blocks[name]
	parent := s.chain().GetTd(block.ParentHash(), block.NumberU64()-1)
	if parent == nil {
		return errors.New("the parent total difficulty is missing")
	}
	db := s.store()
	rawdb.WriteTd(db, block.Hash(), block.NumberU64(), new(big.Int).Add(parent, block.Difficulty()))
	rawdb.WriteBlock(db, block)
	return db.SyncKeyValue()
}

// summary is the comparison part of a path: per step, the error class, the
// head and the decisions (without sequence-independent digests that bind
// the store).
type summaryRow struct {
	Name      string   `json:"name"`
	Class     string   `json:"class"`
	Phase     string   `json:"phase"`
	Head      string   `json:"head"`
	Finalized string   `json:"finalized"`
	Canonical []string `json:"canonical"`
	Decisions []string `json:"decisions"`
	Open      []string `json:"open"`
}

func summarize(steps []step) []summaryRow {
	out := []summaryRow{}
	for _, st := range steps {
		row := summaryRow{Name: st.Name, Class: st.Class, Phase: st.Phase, Head: st.Head, Finalized: st.Finalized,
			Canonical: st.Canonical, Decisions: []string{}, Open: []string{}}
		for _, d := range st.New {
			row.Decisions = append(row.Decisions, fmt.Sprintf("%d %s %s %s->%s %v %s", d.Sequence, d.State, d.Kind,
				d.Old, d.New, d.Members, d.Evidence))
		}
		for _, o := range st.Open {
			row.Open = append(row.Open, o.State+" "+o.Block)
		}
		out = append(out, row)
	}
	return out
}

// ---- events (D101) ----

type eventLog struct {
	mu     sync.Mutex
	events []string
	subs   []interface{ Unsubscribe() }
	quit   chan struct{}
	wg     sync.WaitGroup
}

func watch[T any](log *eventLog, subscribe func(chan<- T) interface{ Unsubscribe() }, label func(T) string) {
	ch := make(chan T, 256)
	log.subs = append(log.subs, subscribe(ch))
	log.wg.Add(1)
	go func() {
		defer log.wg.Done()
		for {
			select {
			case ev := <-ch:
				log.mu.Lock()
				log.events = append(log.events, label(ev))
				log.mu.Unlock()
			case <-log.quit:
				for {
					select {
					case ev := <-ch:
						log.mu.Lock()
						log.events = append(log.events, label(ev))
						log.mu.Unlock()
					default:
						return
					}
				}
			}
		}
	}()
}

func newEventLog(fx *fixture, chain *core.BlockChain) *eventLog {
	log := &eventLog{quit: make(chan struct{})}
	name := func(h *types.Header) string { return fx.name(hexHash(h.Hash())) }
	watch(log, func(ch chan<- core.ChainEvent) interface{ Unsubscribe() } { return chain.SubscribeChainEvent(ch) },
		func(ev core.ChainEvent) string { return "chain " + name(ev.Header) })
	watch(log, func(ch chan<- core.ChainHeadEvent) interface{ Unsubscribe() } {
		return chain.SubscribeChainHeadEvent(ch)
	}, func(ev core.ChainHeadEvent) string { return "head " + name(ev.Header) })
	watch(log, func(ch chan<- core.ChainHeadEvent) interface{ Unsubscribe() } {
		return chain.SubscribeChainBlockEvent(ch)
	}, func(ev core.ChainHeadEvent) string { return "block " + name(ev.Header) })
	watch(log, func(ch chan<- core.HighestVerifiedBlockEvent) interface{ Unsubscribe() } {
		return chain.SubscribeHighestVerifiedHeaderEvent(ch)
	}, func(ev core.HighestVerifiedBlockEvent) string { return "verified " + name(ev.Header) })
	watch(log, func(ch chan<- []*types.Log) interface{ Unsubscribe() } { return chain.SubscribeLogsEvent(ch) },
		func(ev []*types.Log) string { return fmt.Sprintf("logs %d", len(ev)) })
	watch(log, func(ch chan<- core.FinalizedHeaderEvent) interface{ Unsubscribe() } {
		return chain.SubscribeFinalizedHeaderEvent(ch)
	}, func(ev core.FinalizedHeaderEvent) string { return "finalized " + name(ev.Header) })
	return log
}

// take stops the log and returns its events, sorted (the feeds run on
// separate goroutines).
func (log *eventLog) take() []string {
	time.Sleep(200 * time.Millisecond)
	close(log.quit)
	log.wg.Wait()
	for _, sub := range log.subs {
		sub.Unsubscribe()
	}
	out := append([]string{}, log.events...)
	sort.Strings(out)
	return out
}

// eventsOf imports each block with its own InsertChain call and returns the
// events of each import.
func eventsOf(fx *fixture, chain *core.BlockChain, names ...string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, name := range names {
		log := newEventLog(fx, chain)
		_, err := chain.InsertChain(fx.chainOf(name))
		out[name] = log.take()
		if err != nil {
			return out, fmt.Errorf("%s: %w", name, err)
		}
	}
	return out, nil
}

// ---- the probe ----

func writeJSON(t testing.TB, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
}

// tracer writes the trace events of the harness stores, one JSON line each.
type tracer struct {
	mu   sync.Mutex
	file *os.File
	kept []map[string]any
}

// events returns the kept events of scope with this event name.
func (tr *tracer) events(scope, name string) []map[string]any {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	out := []map[string]any{}
	for _, line := range tr.kept {
		if line["scope"] == scope && line["event"] == name {
			out = append(out, line)
		}
	}
	return out
}

func newTracer(t testing.TB, path string) *tracer {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	return &tracer{file: file}
}

func (tr *tracer) emit(scope string) func(bsc.Event) {
	return func(event bsc.Event) {
		line := map[string]any{"scope": scope}
		for k, v := range event {
			line[k] = v
		}
		data, err := json.Marshal(line)
		if err != nil {
			data = []byte(fmt.Sprintf(`{"scope":%q,"marshal_error":%q}`, scope, err.Error()))
		}
		tr.mu.Lock()
		defer tr.mu.Unlock()
		tr.kept = append(tr.kept, line)
		tr.file.Write(append(data, '\n'))
	}
}

func TestBSCLifecycleProbe(t *testing.T) {
	retention.Install()
	started := time.Now()
	work := probeEnv(t, envWork)
	fx := loadFixture(t)
	tr := newTracer(t, filepath.Join(work, "trace.jsonl"))
	report := map[string]any{"schema_version": 1, "kind": "bsc-lifecycle-probe", "genesis": hexHash(fx.genesis.ToBlock().Hash())}
	blocks := map[string]any{}
	for name, block := range fx.blocks {
		blocks[name] = map[string]any{"number": block.NumberU64(), "hash": hexHash(block.Hash()),
			"parent": fx.name(hexHash(block.ParentHash())), "root": hexHash(block.Root()), "time": block.Time()}
	}
	report["blocks"] = blocks
	// The blocks in a fixed order, for the driver's own header checks.
	var encoded bytes.Buffer
	for _, name := range blockOrder {
		if err := rlp.Encode(&encoded, fx.blocks[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "blocks.rlp"), encoded.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	report["block_order"] = blockOrder
	total := map[string]lifecycle.Reach{}

	// Profile refusals outside the gate.
	profile := map[string]any{}
	check := func(mutate func(*ethconfig.Config)) error {
		cfg := ethconfig.Defaults
		cfg.StateScheme, cfg.SyncMode = rawdb.HashScheme, ethconfig.FullSync
		mutate(&cfg)
		return bsclifecycle.CheckProfile(&cfg)
	}
	profile["full_sync"] = errText(check(func(*ethconfig.Config) {}))
	profile["snap_sync"] = errText(check(func(c *ethconfig.Config) { c.SyncMode = ethconfig.SnapSync }))
	profile["vote_enable"] = errText(check(func(c *ethconfig.Config) { c.Miner.VoteEnable = true }))
	enabled := true
	profile["mev_builder"] = errText(check(func(c *ethconfig.Config) { c.Miner.Mev.Enabled = &enabled }))
	profile["path_scheme"] = errText(check(func(c *ethconfig.Config) { c.StateScheme = rawdb.PathScheme }))
	configPath := filepath.Join(work, "capture-config.json")
	writeConfig(t, fx, configPath)
	if err := os.Setenv(bsclifecycle.ConfigEnv, configPath); err != nil {
		t.Fatal(err)
	}
	_, snapErr := openNode(fx, filepath.Join(work, "snap"), func(c *ethconfig.Config) { c.SyncMode = ethconfig.SnapSync })
	profile["eth_new_snap_sync"] = errText(snapErr)
	// geth import: Start refuses a Parlia engine without the system-call
	// API (bsc.manual-commands).
	{
		db := rawdb.NewMemoryDatabase()
		engine := parlia.New(fx.genesis.Config, db, nil, fx.genesis.ToBlock().Hash())
		config := core.DefaultConfig()
		config.StateScheme, config.SnapshotLimit = rawdb.HashScheme, 0
		chain, err := core.NewBlockChain(db, fx.genesis, engine, config)
		if err != nil {
			t.Fatal(err)
		}
		_, startErr := bscdecision.Start(admission.NewGuard(), db, chain, bscdecision.Config{Seam: bsc.Config{
			Producer: probeProducer, Identity: stateprovider.StoreIdentity{StoreID: probeStoreID,
				ChainID: "714", GenesisHash: chain.Genesis().Hash(), StateScheme: rawdb.HashScheme},
			GenesisFile: fx.genesisFile, NativeCommit: fx.commit}, Mirror: noMirror{}})
		profile["start_without_eth_api"] = errText(startErr)
		chain.Stop()
		db.Close()
	}
	report["profile"] = profile

	// Native caller path.
	nativeHome := filepath.Join(work, "native")
	ns, err := openNode(fx, nativeHome, nil)
	if err != nil {
		t.Fatal(err)
	}
	rn := &recorder{fx: fx, s: ns}
	dn := &driver{t: t, fx: fx, r: rn, total: total, reopen: func() (store, error) { return openNode(fx, nativeHome, nil) }}
	dn.shared(nativeHome)
	nativeShared := len(rn.steps)
	// The rest of the caller map on the shipped node.
	n := rn.s.(*nodeStore)
	chain := n.chain()
	_, err = chain.InsertBlockWithoutSetHead(context.Background(), fx.blocks["A7"], false)
	rn.record("InsertBlockWithoutSetHead (Engine API)", err, nil)
	_, err = chain.SetCanonical(fx.blocks["A6"])
	rn.record("SetCanonical (Engine API)", err, nil)
	_, err = chain.InsertReceiptChain(types.Blocks{}, nil, 0)
	rn.record("InsertReceiptChain (snap sync)", err, nil)
	_, err = chain.InsertHeaderChain([]*types.Header{fx.blocks["S4"].Header()})
	rn.record("InsertHeaderChain (header sync)", err, nil)
	_, err = chain.WriteBlockAndSetHead(fx.blocks["S4"], nil, nil, nil, nil)
	rn.record("WriteBlockAndSetHead (miner)", err, nil)
	rn.record("StartMining", n.eth.StartMining(), nil)
	rn.record("ResetWithGenesisBlock", chain.ResetWithGenesisBlock(chain.Genesis()), nil)
	_, err = eth.NewDebugAPI(n.eth).ExecutionWitness(rpc.BlockNumberOrHashWithNumber(6))
	rn.record("debug_executionWitness", err, nil)
	nativeExits := n.life().ExitErrors()
	n.close()
	addCoverage(total, n.life())
	report["native_caller"] = map[string]any{"steps": rn.steps, "shared_steps": nativeShared,
		"exit_errors": nativeExits}

	// Harness path: the same scenario through direct calls.
	harnessHome := filepath.Join(work, "harness")
	hs, err := openHarness(fx, harnessHome, bsclifecycle.Options{}, tr.emit("harness"))
	if err != nil {
		t.Fatal(err)
	}
	rh := &recorder{fx: fx, s: hs}
	dh := &driver{t: t, fx: fx, r: rh, total: total, reopen: func() (store, error) {
		return openHarness(fx, harnessHome, bsclifecycle.Options{}, tr.emit("harness"))
	}}
	dh.shared(harnessHome)
	harnessShared := len(rh.steps)
	// Deadlock smoke: guarded callers concurrently.
	h := rh.s.(*harnessStore)
	report["deadlock"] = deadlockSmoke(fx, h)
	rh.record("after deadlock smoke", nil, nil)
	harnessExits := h.life().ExitErrors()
	h.close()
	addCoverage(total, h.life())
	report["harness"] = map[string]any{"steps": rh.steps, "shared_steps": harnessShared, "exit_errors": harnessExits}
	report["ac_cap_01_01"] = map[string]any{"native": summarize(rn.steps[:nativeShared]),
		"harness": summarize(rh.steps[:harnessShared])}

	// Attestation lookup error on the import path (AC-LIF-06-05), then
	// recovery without the fault.
	report["attestation_fault"] = attestationFault(t, fx, filepath.Join(work, "attestation"), tr, total)

	// Native event feeds of the decided imports against a reference chain
	// without the extractor (D101).
	report["events"] = eventFeeds(t, fx, filepath.Join(work, "events"), tr)

	// Crash points of a SetHead decision.
	report["crashes"] = crashes(t, fx, work, tr, total)

	// Refusals outside the gate.
	outside := map[string]any{"bsc.snap-sync": profile["eth_new_snap_sync"], "bsc.sealing": profile["vote_enable"],
		"bsc.manual-commands":   profile["start_without_eth_api"],
		"bsc.database-commands": report["attestation_fault"].(map[string]any)["database_guard"]}
	for id, refusal := range outside {
		if refusal != nil {
			t := total[id]
			t.Refusals++
			total[id] = t
		}
	}
	report["outside_gate_refusals"] = outside
	m, err := lifecycle.Callers()
	if err != nil {
		t.Fatal(err)
	}
	cov := map[string]any{}
	routes := map[string]string{}
	for _, c := range m.Callers {
		if c.Client != "bsc" {
			continue
		}
		routes[c.ID] = string(c.Route)
		reach := total[c.ID]
		cov[c.ID] = map[string]uint64{"passes": reach.Passes, "intents": reach.Intents, "refusals": reach.Refusals}
	}
	report["coverage"] = cov
	report["routes"] = routes
	report["gaps"] = lifecycle.Gaps(m, "bsc", total)
	report["seconds"] = time.Since(started).Seconds()
	writeJSON(t, filepath.Join(work, "report.json"), report)
}

type noMirror struct{}

func (noMirror) Applied(uint64, string) (bool, error) { return false, nil }

func deadlockSmoke(fx *fixture, h *harnessStore) map[string]any {
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	calls := map[string]func() error{
		"InsertChain known A6 A7": func() error { return h.importKnown("A6", "A7") },
		"SetHead 7 (to the head)": func() error { return h.ch.SetHead(7) },
		"InsertReceiptChain (refused)": func() error {
			_, err := h.ch.InsertReceiptChain(types.Blocks{}, nil, 0)
			if errors.Is(err, lifecycle.ErrRefused) {
				return nil
			}
			return err
		},
		"ExitErrors": func() error {
			if exits := h.l.ExitErrors(); len(exits) != 0 {
				return fmt.Errorf("exit errors %v", exits)
			}
			return nil
		},
		"GoLive (live already)": func() error { _, err := h.l.GoLive(); return err },
	}
	done := make(chan struct{})
	for name, call := range calls {
		wg.Add(1)
		go func(name string, call func() error) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if err := call(); err != nil {
					errs <- name + ": " + err.Error()
				}
			}
		}(name, call)
	}
	go func() { wg.Wait(); close(done) }()
	result := map[string]any{"callers": len(calls), "calls_each": 10}
	select {
	case <-done:
		result["finished"] = true
	case <-time.After(120 * time.Second):
		result["finished"] = false
	}
	close(errs)
	failures := []string{}
	for e := range errs {
		failures = append(failures, e)
	}
	sort.Strings(failures)
	result["errors"] = failures
	result["head"] = fx.name(hexHash(h.ch.CurrentBlock().Hash()))
	return result
}

// hidingReader makes the native snapshot lookup of the attestation read
// fail: the snapshot of the block is computed on a native reader without
// the block's header, as when a required header is unavailable.
type hidingReader struct {
	consensus.ChainHeaderReader
	native *core.BlockChain
	block  common.Hash
}

func (r hidingReader) ExtractorSnapshot(number uint64, hash common.Hash,
	compute func(native consensus.ChainHeaderReader) ([]byte, error)) ([]byte, error) {
	return compute(hidden{ChainHeaderReader: r.native, block: r.block})
}

type hidden struct {
	consensus.ChainHeaderReader
	block common.Hash
}

func (h hidden) GetHeader(hash common.Hash, number uint64) *types.Header {
	if hash == h.block {
		return nil
	}
	return h.ChainHeaderReader.GetHeader(hash, number)
}

func (h hidden) GetHeaderByHash(hash common.Hash) *types.Header {
	if hash == h.block {
		return nil
	}
	return h.ChainHeaderReader.GetHeaderByHash(hash)
}

func attestationFault(t *testing.T, fx *fixture, home string, tr *tracer, total map[string]lifecycle.Reach) map[string]any {
	out := map[string]any{}
	var faulty bool
	var chainRef *core.BlockChain
	opts := bsclifecycle.Options{AttestationReader: func(evidence consensus.ChainHeaderReader,
		block *types.Header) consensus.ChainHeaderReader {
		if !faulty {
			return evidence
		}
		return hidingReader{ChainHeaderReader: evidence, native: chainRef, block: block.Hash()}
	}}
	h, err := openHarness(fx, home, opts, tr.emit("attestation"))
	if err != nil {
		t.Fatal(err)
	}
	chainRef = h.ch
	r := &recorder{fx: fx, s: h}
	r.record("start (BOOTSTRAP)", nil, nil)
	r.record("import A1 A2 (BOOTSTRAP)", h.importNew("A1", "A2"), nil)
	_, err = h.l.GoLive()
	r.record("GoLive", err, nil)
	faulty = true
	r.record("import A3 with the snapshot lookup error", h.importNew("A3"), nil)
	faulty = false
	guardErr := bsclifecycle.RequireNoOpenCapture(h.db)
	out["database_guard"] = errText(guardErr)
	r.record("database guard (removedb, db put) while A3 is EXEC_READY", guardErr, nil)
	h.close()
	addCoverage(total, h.l)
	h, err = openHarness(fx, home, opts, tr.emit("attestation"))
	if err != nil {
		t.Fatal(err)
	}
	chainRef = h.ch
	r.s = h
	r.record("restart: recovery replays A3 without the fault", nil, nil)
	r.record("database guard after the decision", bsclifecycle.RequireNoOpenCapture(h.db), nil)
	h.close()
	addCoverage(total, h.l)
	out["steps"] = r.steps
	return out
}

func eventFeeds(t *testing.T, fx *fixture, home string, tr *tracer) map[string]any {
	out := map[string]any{}
	// Reference: a plain chain without the extractor.
	db := rawdb.NewMemoryDatabase()
	api, backend := bsc.NewSystemCallAPI()
	engine := parlia.New(fx.genesis.Config, db, api, fx.genesis.ToBlock().Hash())
	config := core.DefaultConfig()
	config.StateScheme, config.SnapshotLimit = rawdb.HashScheme, 0
	ref, err := core.NewBlockChain(db, fx.genesis, engine, config)
	if err != nil {
		t.Fatal(err)
	}
	backend.Bind(ref, nil)
	refEvents, err := eventsOf(fx, ref, "A1", "A2", "A3", "A4", "A5", "A6", "A7")
	out["reference"] = refEvents
	out["reference_error"] = errText(err)
	ref.Stop()
	db.Close()
	// Capture: A1 A2 in BOOTSTRAP (native, uncaptured), then A3..A7 decided.
	h, err := openHarness(fx, home, bsclifecycle.Options{}, tr.emit("events"))
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := eventsOf(fx, h.ch, "A1", "A2")
	out["bootstrap"] = bootstrap
	out["bootstrap_error"] = errText(err)
	_, err = h.l.GoLive()
	out["golive_error"] = errText(err)
	decided, err := eventsOf(fx, h.ch, "A3", "A4", "A5", "A6", "A7")
	out["decided"] = decided
	out["decided_error"] = errText(err)
	out["decisions"] = decisions(fx, h.l)
	h.close()
	return out
}

// ---- crash points ----

var crashModes = []string{"set-head-intended", "set-head-rewound"}

// TestBSCLifecycleChild builds a live store, then runs SetHead 4 from A5 and
// exits at the crash point of its mode, after a sync of the native store.
func TestBSCLifecycleChild(t *testing.T) {
	mode := os.Getenv(envChildMode)
	if mode == "" {
		t.Skip("child only")
	}
	retention.Install()
	fx := loadFixture(t)
	home := probeEnv(t, envChildHome)
	var h *harnessStore
	point := strings.TrimPrefix(mode, "set-head-")
	opts := bsclifecycle.Options{Point: func(name string) {
		if name == point && h != nil && h.ch.CurrentBlock().Number.Uint64() >= 4 {
			if err := h.db.SyncKeyValue(); err != nil {
				t.Fatal(err)
			}
			os.Exit(childExit)
		}
	}}
	var err error
	if h, err = openHarness(fx, home, opts, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.importNew("A1", "A2", "A3"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.l.GoLive(); err != nil {
		t.Fatal(err)
	}
	if err := h.importNew("A4", "A5"); err != nil {
		t.Fatal(err)
	}
	err = h.ch.SetHead(4)
	t.Fatalf("SetHead returned without reaching %s: %v", point, err)
}

func crashes(t *testing.T, fx *fixture, work string, tr *tracer, total map[string]lifecycle.Reach) []map[string]any {
	out := []map[string]any{}
	for _, mode := range crashModes {
		home := filepath.Join(work, "crash-"+mode)
		cmd := exec.Command(os.Args[0], "-test.run", "^TestBSCLifecycleChild$", "-test.count=1")
		cmd.Env = append(os.Environ(), envChildMode+"="+mode, envChildHome+"="+home)
		output, err := cmd.CombinedOutput()
		code := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else if err == nil {
			code = 0
		}
		row := map[string]any{"mode": mode, "child_exit": code}
		if code != childExit {
			row["child_output"] = string(output)
		}
		db := openDisk(t, home)
		row["head_after_crash"] = fx.name(hexHash(rawdb.ReadHeadBlockHash(db)))
		db.Close()
		h, err := openHarness(fx, home, bsclifecycle.Options{}, tr.emit("crash-"+mode))
		row["restart_error"] = errText(err)
		if err == nil {
			row["phase"] = string(h.l.Gate().Phase())
			row["head"] = fx.name(hexHash(h.ch.CurrentBlock().Hash()))
			row["decisions"] = decisions(fx, h.l)
			if viewHead, _, ok, verr := h.l.ReadView(); verr == nil && ok {
				row["view_head"] = fx.name(viewHead.Hash)
			}
			h.close()
			addCoverage(total, h.l)
		}
		row["settle"] = tr.events("crash-"+mode, "lifecycle-settle")
		out = append(out, row)
	}
	return out
}

var _ = decision.Canonical
