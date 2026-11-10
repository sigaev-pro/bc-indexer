package stateprovider

// BSC hooks for the shared tests. Blocks of the accepted private fixture are
// imported through the native BlockChain with the native Parlia engine,
// which verifies their real seals.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/parlia"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/state/pruner"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/ethereum/go-ethereum/triedb/pathdb"
	"github.com/holiman/uint256"
)

func inputPath(t testing.TB, name string) string {
	t.Helper()
	root := os.Getenv("EXTRACTOR_PINS_INPUTS")
	if root == "" {
		t.Fatal("EXTRACTOR_PINS_INPUTS is required")
	}
	return filepath.Join(root, name)
}

func familyGenesis(t testing.TB) *core.Genesis {
	t.Helper()
	data, err := os.ReadFile(inputPath(t, "genesis.json"))
	if err != nil {
		t.Fatal(err)
	}
	genesis := new(core.Genesis)
	if err := json.Unmarshal(data, genesis); err != nil {
		t.Fatal(err)
	}
	return genesis
}

func familyBlocks(t testing.TB) types.Blocks {
	t.Helper()
	data, err := os.ReadFile(inputPath(t, "blocks.rlp"))
	if err != nil {
		t.Fatal(err)
	}
	stream := rlp.NewStream(bytes.NewReader(data), 0)
	var blocks types.Blocks
	for {
		block := new(types.Block)
		if err := stream.Decode(block); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		blocks = append(blocks, block)
	}
	if len(blocks) != 2 || blocks[0].NumberU64() != 1 || blocks[1].NumberU64() != 2 {
		t.Fatalf("fixture blocks %d", len(blocks))
	}
	return blocks
}

func newMemoryStore(t testing.TB) (ethdb.Database, *triedb.Database, *types.Header) {
	t.Helper()
	db := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(db, triedb.HashDefaults)
	block, err := familyGenesis(t).Commit(db, tdb, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tdb.Close(); db.Close() })
	return db, tdb, block.Header()
}

func newPathTrieDB(_ ethdb.Database) *triedb.Database {
	return triedb.NewDatabase(rawdb.NewMemoryDatabase(), &triedb.Config{PathDB: pathdb.Defaults})
}

// commitSideState writes a side-branch child of parent into the trie cache
// only, with one account that has a balance, code and storage, and writes an
// unsealed side header that names its root. It is test scaffolding for a
// known side branch, not a consensus-valid block.
func commitSideState(t testing.TB, db ethdb.Database, tdb *triedb.Database, parent *types.Header, seed byte) (*types.Header, common.Hash) {
	t.Helper()
	statedb, err := state.New(parent.Root, state.NewDatabase(tdb, state.NewCodeDB(db)))
	if err != nil {
		t.Fatal(err)
	}
	account := common.BytesToAddress([]byte{0xee, seed})
	code := []byte{0x60, seed, 0x60, 0x00, 0x55, 0x00}
	statedb.SetBalance(account, uint256.NewInt(uint64(seed)+1), tracing.BalanceChangeUnspecified)
	statedb.SetCode(account, code, tracing.CodeChangeUnspecified)
	statedb.SetState(account, common.Hash{0x01}, common.Hash{seed})
	number := new(big.Int).Add(parent.Number, common.Big1)
	root, err := statedb.Commit(number.Uint64(), true, false)
	if err != nil {
		t.Fatal(err)
	}
	header := &types.Header{ParentHash: parent.Hash(), Number: number, Root: root, GasLimit: parent.GasLimit,
		Time: parent.Time + 1, Difficulty: big.NewInt(1), Extra: []byte{'s', 'i', 'd', 'e', seed}}
	rawdb.WriteHeader(db, header)
	return header, crypto.Keccak256Hash(code)
}

type testNode struct {
	database ethdb.Database
	chain    *core.BlockChain
}

func openDisk(t testing.TB, instanceDir string) ethdb.Database {
	t.Helper()
	chaindata := filepath.Join(instanceDir, "chaindata")
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

// openNode opens the native chain on disk with the capture profile: hash
// state scheme and full garbage collection. Native snapshots are optional.
func openNode(t testing.TB, instanceDir string, snapshots bool) *testNode {
	t.Helper()
	db := openDisk(t, instanceDir)
	config := core.DefaultConfig()
	config.StateScheme, config.ArchiveMode, config.SnapshotWait = rawdb.HashScheme, false, true
	if !snapshots {
		config.SnapshotLimit = 0
	}
	genesis := familyGenesis(t)
	chain, err := core.NewBlockChain(db, genesis, parlia.New(genesis.Config, db, nil, genesis.ToBlock().Hash()), config)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return &testNode{database: db, chain: chain}
}

func (n *testNode) db() ethdb.Database          { return n.database }
func (n *testNode) trieDB() *triedb.Database    { return n.chain.TrieDB() }
func (n *testNode) head() *types.Header         { return n.chain.CurrentBlock() }
func (n *testNode) setHead(number uint64) error { return n.chain.SetHead(number) }
func (n *testNode) stop()                       { n.chain.Stop(); n.database.Close() }
func (n *testNode) insert(blocks types.Blocks) error {
	_, err := n.chain.InsertChain(blocks)
	return err
}

func openStore(t testing.TB, instanceDir string) (ethdb.Database, func()) {
	t.Helper()
	db := openDisk(t, instanceDir)
	return db, func() { db.Close() }
}

func initStore(t testing.TB, instanceDir string, genesis *core.Genesis, path bool) {
	t.Helper()
	db := openDisk(t, instanceDir)
	defer db.Close()
	config := triedb.HashDefaults
	if path {
		config = &triedb.Config{PathDB: pathdb.Defaults}
	}
	tdb := triedb.NewDatabase(db, config)
	if _, err := genesis.Commit(db, tdb, nil); err != nil {
		t.Fatal(err)
	}
	if err := tdb.Close(); err != nil {
		t.Fatal(err)
	}
}

func nativePrune(db ethdb.Database, instanceDir string, root common.Hash) error {
	native, err := pruner.NewPruner(db, pruner.Config{Datadir: instanceDir, BloomSize: 256}, nativeTriesInMemory)
	if err != nil {
		return err
	}
	return native.Prune(root)
}

// runDump runs the accepted native binary's dumpconfig with the given
// arguments, extra environment and optional config file body.
func runDump(t *testing.T, name string, args []string, env []string, config string) ([]byte, string, error) {
	t.Helper()
	binary := os.Getenv("EXTRACTOR_NATIVE_BINARY")
	if binary == "" {
		t.Fatal("EXTRACTOR_NATIVE_BINARY is required")
	}
	dir := workDir(t, name)
	full := append([]string{"dumpconfig", "--datadir", filepath.Join(dir, "datadir")}, args...)
	if config != "" {
		path := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		full = append(full, "--config", path)
	}
	command := exec.Command(binary, full...)
	command.Env = append([]string{"HOME=" + dir, "PATH=/usr/bin:/bin"}, env...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	tail := stderr.String()
	if len(tail) > 600 {
		tail = tail[len(tail)-600:]
	}
	return stdout.Bytes(), tail, err
}

// dumpCase is accepted when reason is empty; otherwise the rejection text
// (native stderr or the profile error) must contain reason.
func dumpCase(name, reason string, args, env []string, config string, reflect func(*ethconfigView) bool) profileCase {
	return profileCase{name: name, reason: reason, run: func(t *testing.T) (bool, any) {
		out, stderr, err := runDump(t, name, args, env, config)
		if err != nil {
			return false, map[string]any{"layer": "native", "error": err.Error(), "stderr": stderr}
		}
		cfg, err := DecodeEffectiveConfig(out)
		if err != nil {
			t.Fatalf("%s: decode native dump: %v", name, err)
		}
		profile, err := CheckProfile(cfg)
		view := viewOf(cfg)
		if err != nil {
			return false, map[string]any{"layer": "profile", "error": err.Error(), "profile": profile, "effective": view}
		}
		if reflect != nil && !reflect(&view) {
			return false, map[string]any{"layer": "reflection", "effective": view}
		}
		return true, map[string]any{"profile": profile, "effective": view}
	}}
}

const baseConfig = "[Eth]\nStateScheme = \"hash\"\nNoPruning = false\n"

var baseArgs = []string{"--state.scheme=hash", "--gcmode=full"}

func profileCases(t testing.TB) []profileCase {
	history := func(value uint64) func(*ethconfigView) bool {
		return func(view *ethconfigView) bool { return view.TransactionHistory == value }
	}
	return []profileCase{
		dumpCase("baseline_flags", "", baseArgs, nil, "", nil),
		dumpCase("allowed_native_args", "", append(baseArgs, "--history.transactions=12345"), nil, "", history(12345)),
		dumpCase("allowed_environment", "", baseArgs, []string{"GETH_HISTORY_TRANSACTIONS=23456"}, "", history(23456)),
		dumpCase("allowed_config", "", nil, nil, baseConfig+"TransactionHistory = 34567\n", history(34567)),
		dumpCase("wrong_hash_profile_default", `state scheme ""`, nil, nil, "", nil),
		dumpCase("wrong_hash_profile_flag", `state scheme "path"`, []string{"--state.scheme=path"}, nil, "", nil),
		dumpCase("wrong_hash_profile_environment", `state scheme "path"`, nil, []string{"GETH_STATE_SCHEME=path"}, "", nil),
		dumpCase("wrong_hash_profile_config", `state scheme "path"`, nil, nil, "[Eth]\nStateScheme = \"path\"\n", nil),
		dumpCase("environment_over_config_cannot_bypass", "incompatible state scheme", nil, []string{"GETH_STATE_SCHEME=path"}, baseConfig, nil),
		dumpCase("flag_over_config_cannot_bypass", "gcmode archive", []string{"--gcmode=archive"}, nil, baseConfig, nil),
		dumpCase("wrong_gc_profile_archive_flag", "gcmode archive", []string{"--state.scheme=hash", "--gcmode=archive"}, nil, "", nil),
		dumpCase("wrong_gc_profile_archive_environment", "gcmode archive", []string{"--state.scheme=hash"}, []string{"GETH_GCMODE=archive"}, "", nil),
		dumpCase("wrong_gc_profile_archive_config", "gcmode archive", nil, nil, "[Eth]\nStateScheme = \"hash\"\nNoPruning = true\n", nil),
		dumpCase("forbidden_pruning_ancient_flag", "pruneancient deletes block data", append(baseArgs, "--pruneancient"), nil, "", nil),
		dumpCase("forbidden_pruning_block_history_flag", "history.blocks", append(baseArgs, "--history.blocks=90000"), nil, "", nil),
		dumpCase("forbidden_pruning_block_history_environment", "history.blocks", baseArgs, []string{"GETH_HISTORY_BLOCKS=90000"}, "", nil),
		dumpCase("forbidden_pruning_block_history_config", "history.blocks", nil, nil, baseConfig+"BlockHistory = 90000\n", nil),
	}
}

// ethconfigView is the reported subset of the effective native config.
type ethconfigView struct {
	StateScheme        string `json:"state_scheme"`
	NoPruning          bool   `json:"no_pruning"`
	TransactionHistory uint64 `json:"transaction_history"`
}

func viewOf(cfg interface{}) ethconfigView {
	data, _ := json.Marshal(cfg)
	var view struct {
		StateScheme        string
		NoPruning          bool
		TransactionHistory uint64
	}
	_ = json.Unmarshal(data, &view)
	return ethconfigView{StateScheme: view.StateScheme, NoPruning: view.NoPruning, TransactionHistory: view.TransactionHistory}
}

func TestUnitCheckProfileFields(t *testing.T) {
	base := `[Eth]
StateScheme = "hash"
NoPruning = false
`
	cfg, err := DecodeEffectiveConfig([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CheckProfile(cfg); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		strings.Replace(base, `"hash"`, `"path"`, 1),
		strings.Replace(base, `"hash"`, `""`, 1),
		strings.Replace(base, "false", "true", 1),
		base + "PruneAncientData = true\n",
		base + "BlockHistory = 1\n",
	} {
		cfg, err := DecodeEffectiveConfig([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CheckProfile(cfg); !errors.Is(err, ErrProfile) {
			t.Fatalf("%q accepted: %v", body, err)
		}
	}
	if _, err := DecodeEffectiveConfig([]byte("[Eth]\nUnknownField = 1\n")); err == nil {
		t.Fatal("unknown native field accepted")
	}
	if _, err := CheckProfile(nil); err == nil {
		t.Fatal("missing config accepted")
	}
}

// TestNativeLegacyPruneOffset covers the BSC prune-block marker: native Open
// would rewrite the freezer metadata, so preflight rejects the home first and
// leaves it unchanged. The marker is written by the test, not by a real
// prune-block run.
func TestNativeLegacyPruneOffset(t *testing.T) {
	genesis := familyGenesis(t)
	dir := workDir(t, "legacy-offset")
	initStore(t, dir, genesis, false)
	db, closeDB := openStore(t, dir)
	if err := db.Put([]byte("offSetOfCurrentAncientFreezer"), big.NewInt(5).Bytes()); err != nil {
		t.Fatal(err)
	}
	closeDB()
	unbound := StoreIdentity{ChainID: genesis.Config.ChainID.String(), GenesisHash: genesis.ToBlock().Hash(),
		StateScheme: rawdb.HashScheme}
	before := inventory(t, dir)
	_, err := Preflight(dir, unbound)
	if !sameInventory(before, inventory(t, dir)) {
		t.Fatal("preflight changed the home")
	}
	mustErr(t, err, ErrForbiddenPruning)
	record(t, "legacy_offset", err.Error())
}
