package stateprovider

// BSC hooks for the shared tests. Blocks of the accepted private fixture are
// imported through the native BlockChain with the native Parlia engine,
// which verifies their real seals.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/parlia"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/state/pruner"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/params"
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

// newPathStore is newMemoryStore on a path store: the fixture genesis
// committed through the path trie database. The native path store writes the
// genesis state to the key-value store in the background, so it waits until
// the store carries it; only then does the native reader name the scheme.
func newPathStore(t testing.TB) (ethdb.Database, *triedb.Database, *types.Header) {
	t.Helper()
	db := rawdb.NewMemoryDatabase()
	// A capture node disallows the asynchronous flush of the path store
	// (D108), so a commit returns when its state is persistent.
	config := pathdb.Defaults
	config.NoAsyncFlush = true
	tdb := triedb.NewDatabase(db, &triedb.Config{PathDB: config})
	block, err := familyGenesis(t).Commit(db, tdb, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tdb.Close(); db.Close() })
	for deadline := time.Now().Add(10 * time.Second); rawdb.ReadPersistentStateID(db) == 0 && rawdb.ReadStateScheme(db) != rawdb.PathScheme; {
		if time.Now().After(deadline) {
			t.Fatal("the path store did not write its genesis state")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return db, tdb, block.Header()
}

// tailDB is a native database with a synthetic ancient tail.
type tailDB struct {
	ethdb.Database
	tail uint64
	err  error
}

func (db tailDB) Tail() (uint64, error) { return db.tail, db.err }

// withTail returns db with the given tail of its ancient store, or the error
// that reading the tail returns.
func withTail(db ethdb.Database, tail uint64, err error) ethdb.Database {
	return tailDB{Database: db, tail: tail, err: err}
}

// legacyOffsetKey is the key-value key that holds the offset of the prune-block
// and pruneancient options (the native schema keeps it unexported). A client
// without such an option has none.
func legacyOffsetKey() []byte { return []byte("offSetOfCurrentAncientFreezer") }

// openChainData opens only the key-value part of a home, without the native
// freezer: nothing moves to the ancient store while it is open.
func openChainData(t testing.TB, instanceDir string) ethdb.Database {
	t.Helper()
	kv, err := pebble.New(filepath.Join(instanceDir, "chaindata"), 64, 64, "", false)
	if err != nil {
		t.Fatal(err)
	}
	return rawdb.NewDatabase(kv)
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

// openPathNode opens the native chain on disk with the path state scheme and
// the synchronous flush of a capture node (D108). Native snapshots are off.
func openPathNode(t testing.TB, instanceDir string) *testNode {
	t.Helper()
	db := openDisk(t, instanceDir)
	config := core.DefaultConfig()
	config.StateScheme, config.ArchiveMode, config.SnapshotWait = rawdb.PathScheme, false, true
	config.SnapshotLimit = 0
	config = config.WithNoAsyncFlush(true)
	genesis := familyGenesis(t)
	chain, err := core.NewBlockChain(db, genesis, parlia.New(genesis.Config, db, nil, genesis.ToBlock().Hash()), config)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return &testNode{database: db, chain: chain}
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

const (
	baseConfig   = "[Eth]\nStateScheme = \"hash\"\nNoPruning = false\n"
	pathConfig   = "[Eth]\nStateScheme = \"path\"\nNoPruning = false\n"
	floorText    = "keeps fewer than 600000 blocks"
	snapshotArgs = "--syncmode=full --history.transactions=600000 --history.blocks=600000 " +
		"--history.logs=600000 --tries-verify-mode=local"
)

var baseArgs = []string{"--state.scheme=hash", "--gcmode=full"}

func profileCases(t testing.TB) []profileCase {
	history := func(value uint64) func(*ethconfigView) bool {
		return func(view *ethconfigView) bool { return view.TransactionHistory == value }
	}
	scheme := func(want string) func(*ethconfigView) bool {
		return func(view *ethconfigView) bool { return view.StateScheme == want }
	}
	blocks := func(want uint64) func(*ethconfigView) bool {
		return func(view *ethconfigView) bool { return view.BlockHistory == want }
	}
	states := func(want uint64) func(*ethconfigView) bool {
		return func(view *ethconfigView) bool { return view.StateHistory == want }
	}
	return []profileCase{
		// The hash profile of E01 and the values that stay as the operator
		// set them.
		dumpCase("baseline_flags", "", baseArgs, nil, "", nil),
		dumpCase("allowed_native_args", "", append(baseArgs, "--history.transactions=12345"), nil, "", history(12345)),
		dumpCase("allowed_environment", "", baseArgs, []string{"GETH_HISTORY_TRANSACTIONS=23456"}, "", history(23456)),
		dumpCase("allowed_config", "", nil, nil, baseConfig+"TransactionHistory = 34567\n", history(34567)),
		// The fast-node profile of D108: the flags of the official pruned
		// snapshot pass no state scheme, and the store keeps its own.
		dumpCase("fastnode_snapshot_flags", "", []string{snapshotArgs}, nil, "", fastnodeSnapshot),
		dumpCase("fastnode_default_scheme", "", []string{"--syncmode=full"}, nil, "", scheme("")),
		dumpCase("path_profile_flag", "", []string{"--state.scheme=path", "--gcmode=full"}, nil, "", scheme("path")),
		dumpCase("path_profile_environment", "", []string{"--gcmode=full"}, []string{"GETH_STATE_SCHEME=path"}, "",
			scheme("path")),
		dumpCase("path_profile_config", "", nil, nil, pathConfig, scheme("path")),
		// Block data of the last N blocks is the fast-node retention. The
		// flag parser raises a shorter history to N, and the node turns the
		// deprecated pruneancient option into N.
		dumpCase("allowed_block_history_flag", "", append(baseArgs, "--history.blocks=90000"), nil, "", blocks(600000)),
		dumpCase("allowed_block_history_environment", "", baseArgs, []string{"GETH_HISTORY_BLOCKS=90000"}, "",
			blocks(600000)),
		dumpCase("allowed_pruneancient_flag", "", append(baseArgs, "--pruneancient"), nil, "", pruneAncient),
		// The state history matters under the path scheme only.
		dumpCase("allowed_state_history_flag", "", []string{"--state.scheme=path", "--gcmode=full", "--history.state=600000"},
			nil, "", states(600000)),
		dumpCase("allowed_state_history_entire_chain", "", []string{"--state.scheme=path", "--gcmode=full", "--history.state=0"},
			nil, "", states(0)),
		dumpCase("allowed_state_history_under_hash", "", []string{"--state.scheme=hash", "--gcmode=full", "--history.state=599999"},
			nil, "", states(599999)),
		// A source cannot override another.
		dumpCase("environment_over_config_cannot_bypass", "incompatible state scheme", nil, []string{"GETH_STATE_SCHEME=path"},
			baseConfig, nil),
		dumpCase("flag_over_config_cannot_bypass", "gcmode archive", []string{"--gcmode=archive"}, nil, baseConfig, nil),
		// The refusals of the profile.
		dumpCase("wrong_gc_profile_archive_flag", "gcmode archive", []string{"--state.scheme=hash", "--gcmode=archive"}, nil,
			"", nil),
		dumpCase("wrong_gc_profile_archive_environment", "gcmode archive", []string{"--state.scheme=hash"},
			[]string{"GETH_GCMODE=archive"}, "", nil),
		dumpCase("wrong_gc_profile_archive_config", "gcmode archive", nil, nil,
			"[Eth]\nStateScheme = \"hash\"\nNoPruning = true\n", nil),
		// A config file bypasses the clamp of the flag parser.
		dumpCase("forbidden_block_history_config", "capture constraints: history.blocks 1000", nil, nil,
			baseConfig+"BlockHistory = 1000\n", nil),
		dumpCase("forbidden_tries_none_flag", "capture constraints: tries-verify-mode", []string{"--tries-verify-mode=none"},
			nil, "", nil),
		dumpCase("forbidden_tries_none_config", "capture constraints: tries-verify-mode", nil, nil,
			baseConfig+"TriesVerifyMode = \"none\"\n", nil),
		dumpCase("forbidden_state_history_flag", "capture constraints: history.state 599999",
			[]string{"--state.scheme=path", "--gcmode=full", "--history.state=599999"}, nil, "", nil),
		dumpCase("forbidden_state_history_config", "capture constraints: history.state 599999", nil, nil,
			pathConfig+"StateHistory = 599999\n", nil),
		dumpCase("forbidden_scheme_config", "capture constraints: state scheme", nil, nil,
			"[Eth]\nStateScheme = \"bin\"\nNoPruning = false\n", nil),
	}
}

// fastnodeSnapshot is the effective config of the flags of the official
// pruned snapshot: no state scheme, N blocks of block and state history, and
// tries verified locally.
func fastnodeSnapshot(view *ethconfigView) bool {
	return view.StateScheme == "" && !view.NoPruning && view.BlockHistory == retentionFloor &&
		view.StateHistory == retentionFloor && view.TriesVerifyMode == "local" && !view.PruneAncientData &&
		view.TransactionHistory == 600000 && view.LogHistory == 600000
}

// pruneAncient is the effective config of the deprecated pruneancient option:
// the node turns it into a block history of N.
func pruneAncient(view *ethconfigView) bool {
	return view.PruneAncientData && view.BlockHistory == retentionFloor
}

// ethconfigView is the reported subset of the effective native config.
type ethconfigView struct {
	StateScheme        string `json:"state_scheme"`
	NoPruning          bool   `json:"no_pruning"`
	PruneAncientData   bool   `json:"prune_ancient_data"`
	BlockHistory       uint64 `json:"block_history"`
	StateHistory       uint64 `json:"state_history"`
	TriesVerifyMode    string `json:"tries_verify_mode"`
	TransactionHistory uint64 `json:"transaction_history"`
	LogHistory         uint64 `json:"log_history"`
}

func viewOf(cfg *ethconfig.Config) ethconfigView {
	return ethconfigView{StateScheme: cfg.StateScheme, NoPruning: cfg.NoPruning,
		PruneAncientData: cfg.PruneAncientData, BlockHistory: cfg.BlockHistory, StateHistory: cfg.StateHistory,
		TriesVerifyMode: cfg.TriesVerifyMode.String(), TransactionHistory: cfg.TransactionHistory,
		LogHistory: cfg.LogHistory}
}

func TestUnitCheckProfileFields(t *testing.T) {
	decode := func(body string) *ethconfig.Config {
		cfg, err := DecodeEffectiveConfig([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	base := "[Eth]\nStateScheme = \"hash\"\nNoPruning = false\n"
	for name, body := range map[string]string{
		"hash":                                         base,
		"no scheme (the flags of the snapshot)":        "[Eth]\nNoPruning = false\n",
		"path":                                         pathConfig,
		"block history N":                              base + "BlockHistory = 600000\n",
		"block history of the entire chain":            base + "BlockHistory = 0\n",
		"pruneancient with a block history N":          base + "PruneAncientData = true\nBlockHistory = 600000\n",
		"state history below N under hash":             base + "StateHistory = 599999\n",
		"state history N under path":                   pathConfig + "StateHistory = 600000\n",
		"state history of the entire chain under path": pathConfig + "StateHistory = 0\n",
		"tries local":                                  base + "TriesVerifyMode = \"local\"\n",
		"search indexes and chain history off or short": base +
			"TransactionHistory = 1\nLogHistory = 1\nLogNoHistory = true\nTrienodeHistory = 0\n" +
			"HistoryMode = \"postmerge\"\n",
	} {
		profile, err := CheckProfile(decode(body))
		if err != nil || profile.GCMode != "full" {
			t.Fatalf("%s: %v (profile %v)", name, err, profile)
		}
	}
	for name, c := range map[string]struct{ body, reason string }{
		"archive":               {"[Eth]\nStateScheme = \"hash\"\nNoPruning = true\n", "gcmode archive"},
		"block history below N": {base + "BlockHistory = 599999\n", "history.blocks 599999 " + floorText},
		// The deprecated option does not excuse a shorter history.
		"pruneancient with a block history below N": {base + "PruneAncientData = true\nBlockHistory = 1000\n",
			"history.blocks 1000 " + floorText},
		"state history below N under path": {pathConfig + "StateHistory = 599999\n", "history.state 599999 " + floorText},
		"tries none":                       {base + "TriesVerifyMode = \"none\"\n", "tries-verify-mode \"none\""},
		"unknown scheme":                   {"[Eth]\nStateScheme = \"bin\"\nNoPruning = false\n", `state scheme "bin"`},
	} {
		_, err := CheckProfile(decode(c.body))
		if !errors.Is(err, ErrProfile) || !strings.Contains(err.Error(), c.reason) {
			t.Fatalf("%s: %v, want %q", name, err, c.reason)
		}
	}
	if _, err := DecodeEffectiveConfig([]byte("[Eth]\nUnknownField = 1\n")); err == nil {
		t.Fatal("unknown native field accepted")
	}
	if _, err := CheckProfile(nil); err == nil {
		t.Fatal("missing config accepted")
	}
	// The profile records the values that the rules read.
	profile, err := CheckProfile(decode(base + "PruneAncientData = true\nBlockHistory = 600000\nStateHistory = 599999\n" +
		"TriesVerifyMode = \"local\"\nTransactionHistory = 1\nLogHistory = 2\nHistoryMode = \"postmerge\"\n"))
	want := EffectiveProfile{StateScheme: rawdb.HashScheme, GCMode: "full", PruneAncientData: true, BlockHistory: 600000,
		StateHistory: 599999, TriesVerifyMode: "local", HistoryMode: "postmerge", TransactionHistory: 1, LogHistory: 2}
	if err != nil || profile != want {
		t.Fatalf("profile %v %v, want %v", profile, err, want)
	}
}

// TestUnitStoreProfile covers the rules that need the scheme of the opened
// store: the flags of the official pruned snapshot name no scheme, and the
// state history floor holds under the scheme the store has.
func TestUnitStoreProfile(t *testing.T) {
	decode := func(body string) *ethconfig.Config {
		cfg, err := DecodeEffectiveConfig([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	short := decode("[Eth]\nNoPruning = false\nStateHistory = 599999\n")
	profile, err := CheckProfile(short)
	if err != nil || profile.StateScheme != "" {
		t.Fatalf("config without a scheme %v %v", profile, err)
	}
	if profile, err = CheckStoreProfile(short, rawdb.HashScheme); err != nil || profile.StateScheme != rawdb.HashScheme ||
		profile.StateHistory != 599999 {
		t.Fatalf("hash store %v %v", profile, err)
	}
	if _, err = CheckStoreProfile(short, rawdb.PathScheme); !errors.Is(err, ErrProfile) ||
		!strings.Contains(err.Error(), "history.state 599999") {
		t.Fatalf("path store with a short state history %v", err)
	}
	full := decode("[Eth]\nNoPruning = false\nStateHistory = 600000\n")
	if profile, err = CheckStoreProfile(full, rawdb.PathScheme); err != nil || profile.StateScheme != rawdb.PathScheme {
		t.Fatalf("path store %v %v", profile, err)
	}
	for _, scheme := range []string{"bin", ""} {
		if _, err = CheckStoreProfile(full, scheme); !errors.Is(err, ErrProfile) ||
			!strings.Contains(err.Error(), "state scheme") {
			t.Fatalf("store of scheme %q %v", scheme, err)
		}
	}
	named := decode(pathConfig + "StateHistory = 600000\n")
	if _, err = CheckStoreProfile(named, rawdb.HashScheme); !errors.Is(err, ErrProfile) ||
		!strings.Contains(err.Error(), "is not the") {
		t.Fatalf("hash store under a path config %v", err)
	}
	if _, err = CheckStoreProfile(nil, rawdb.HashScheme); err == nil {
		t.Fatal("missing config accepted")
	}
}

// TestUnitFloorIsNative pins N to the values of the pinned client: the floor
// is the immutability threshold and the default state history.
func TestUnitFloorIsNative(t *testing.T) {
	if retentionFloor != params.FullImmutabilityThreshold || retentionFloor != pathdb.Defaults.StateHistory ||
		retentionFloor != ethconfig.Defaults.StateHistory {
		t.Fatalf("retention floor %d is not the native immutability threshold %d or the default state history %d",
			retentionFloor, params.FullImmutabilityThreshold, ethconfig.Defaults.StateHistory)
	}
}

// TestNativeLegacyPruneOffset covers the offset that the prune-block and
// pruneancient options of an older BSC client leave behind. Native Open would
// rewrite it, so preflight opens the store without its ancient part, measures
// the offset as the oldest block and refuses a home that keeps fewer than N
// blocks behind its head; the home is left unchanged. The offset is written by
// the test, not by a real prune-block run.
func TestNativeLegacyPruneOffset(t *testing.T) {
	genesis := familyGenesis(t)
	dir := workDir(t, "legacy-offset")
	initStore(t, dir, genesis, false)
	unbound := StoreIdentity{ChainID: genesis.Config.ChainID.String(), GenesisHash: genesis.ToBlock().Hash(),
		StateScheme: rawdb.HashScheme}
	results := map[string]any{}
	for _, item := range []struct {
		name   string
		offset uint64
		head   uint64
		refuse bool
	}{
		{"offset on a genesis-only home", 5, 0, true},
		{"offset N blocks behind the head", 3, retentionFloor + 3, false},
		{"offset N-1 blocks behind the head", 3, retentionFloor + 2, true},
	} {
		db := openChainData(t, dir)
		first := rawdb.ReadHeader(db, genesis.ToBlock().Hash(), 0)
		if item.head == 0 {
			rawdb.WriteHeadHeaderHash(db, genesis.ToBlock().Hash())
		} else {
			setHead(t, db, first, item.head)
		}
		if err := db.Put(legacyOffsetKey(), new(big.Int).SetUint64(item.offset).Bytes()); err != nil {
			t.Fatal(err)
		}
		db.Close()
		before := inventory(t, dir)
		report, err := Preflight(dir, unbound)
		if !sameInventory(before, inventory(t, dir)) {
			t.Fatalf("%s: preflight changed the home", item.name)
		}
		if item.refuse {
			if !errors.Is(err, ErrForbiddenPruning) {
				t.Fatalf("%s: error %v, want the retention floor", item.name, err)
			}
			results[item.name] = err.Error()
			continue
		}
		if err != nil || report.Coverage != (Coverage{Oldest: item.offset, Head: item.head, Floor: retentionFloor}) {
			t.Fatalf("%s: %v %v", item.name, report.Coverage, err)
		}
		results[item.name] = "<nil>"
	}
	if refused, ok := results["offset on a genesis-only home"].(string); !ok ||
		!strings.HasPrefix(refused, "forbidden pruning profile") {
		t.Fatalf("the offset on a genesis-only home was not refused: %v", results)
	}
	record(t, "legacy_offset", results["offset on a genesis-only home"])
	record(t, "legacy_offset_at_floor", results)
}

// TestNativeRetentionFloor covers the floor N of D108 on a home whose ancient
// store a `--history.blocks` run pruned: the native freezer moved its tail. A
// home that keeps N blocks behind its head passes; one that keeps fewer is
// refused and left byte for byte unchanged. The head above the stored headers
// is written by the test; it is not a consensus-valid chain.
func TestNativeRetentionFloor(t *testing.T) {
	genesis := familyGenesis(t)
	dir := workDir(t, "pruned-home")
	initStore(t, dir, genesis, false)
	unbound := StoreIdentity{ChainID: genesis.Config.ChainID.String(), GenesisHash: genesis.ToBlock().Hash(),
		StateScheme: rawdb.HashScheme}
	const stored = 4
	// The ancient store held headers 0 to 3 and was pruned from its start;
	// the key-value store continues at 4. The head is still the genesis, so
	// the native freezer of the open store does not move anything.
	func() {
		db, closeDB := openStore(t, dir)
		defer closeDB()
		first := rawdb.ReadHeader(db, genesis.ToBlock().Hash(), 0)
		headers := []*types.Header{first}
		for number := uint64(1); number <= stored; number++ {
			header := types.CopyHeader(first)
			header.Number = new(big.Int).SetUint64(number)
			headers = append(headers, header)
			if number == stored {
				rawdb.WriteHeader(db, header)
				rawdb.WriteCanonicalHash(db, header.Hash(), number)
			}
		}
		if _, err := rawdb.WriteAncientHeaderChain(db, headers[:stored], big.NewInt(0)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.TruncateTail(stored); err != nil {
			t.Fatalf("prune the ancient store: %v", err)
		}
	}()
	// The head stands N blocks above the tail of the ancient store, or one
	// block less.
	results := map[string]any{}
	for _, item := range []struct {
		name   string
		head   uint64 // above N
		refuse bool
	}{
		{"ancient tail N blocks behind the head", stored, false},
		{"ancient tail N-1 blocks behind the head", stored - 1, true},
	} {
		kv := openChainData(t, dir)
		setHead(t, kv, rawdb.ReadHeader(kv, genesis.ToBlock().Hash(), 0), retentionFloor+item.head)
		kv.Close()
		before := inventory(t, dir)
		report, err := Preflight(dir, unbound)
		if !sameInventory(before, inventory(t, dir)) {
			t.Fatalf("%s: preflight changed the home", item.name)
		}
		if item.refuse {
			if !errors.Is(err, ErrForbiddenPruning) {
				t.Fatalf("%s: error %v, want the retention floor", item.name, err)
			}
			results[item.name] = err.Error()
			continue
		}
		if err != nil || report.Coverage != (Coverage{Oldest: stored, Head: retentionFloor + item.head,
			Floor: retentionFloor}) {
			t.Fatalf("%s: %v %v", item.name, report.Coverage, err)
		}
		results[item.name] = report.Coverage
	}
	record(t, "ancient_tail", results)
}

// TestUnitStateProfiles pins the state profile strings of this client. A
// string binds the profile to every store of its scheme (D108): a changed
// string refuses every bound store, so it changes only with a decision.
func TestUnitStateProfiles(t *testing.T) {
	want := map[string]string{rawdb.HashScheme: "state.scheme=hash;gcmode=full;snapshots=off", rawdb.PathScheme: "state.scheme=path;gcmode=full;history.blocks>=600000;history.state>=600000;tries=local"}
	for scheme, profile := range want {
		if stateProfile(scheme) != profile || profileOf(scheme) != fmt.Sprintf("%x", sha256.Sum256([]byte(profile))) {
			t.Fatalf("state profile of the %s scheme is %q, want %q", scheme, stateProfile(scheme), profile)
		}
	}
	if stateProfile("") != "" || stateProfile("bin") != "" {
		t.Fatal("a scheme without a state profile has one")
	}
}

// pathChildEnv names the home of TestNativePathPinChild.
const pathChildEnv = "EXTRACTOR_PINS_PATH_CHILD"

// persistentIs reports whether the store recorded the state id of root as
// its persistent state id.
func persistentIs(db ethdb.Database, root common.Hash) bool {
	return PersistentState(db, root)
}

// TestNativePathPinChild is the subprocess half of TestNativePathPinSurvivesKill
// on a path store: it imports the fixture, optionally pins block 1 and kills
// itself, so the in-memory layers of the path store are lost without a flush.
func TestNativePathPinChild(t *testing.T) {
	dir := os.Getenv(pathChildEnv)
	if dir == "" {
		return
	}
	node := openPathNode(t, dir)
	blocks := familyBlocks(t)
	if err := node.insert(blocks); err != nil {
		t.Fatal(err)
	}
	identity, err := Bind(node.db(), testStore)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := New(node.db(), node.trieDB(), identity)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"head": node.head().Number.Uint64(),
		"block1_persistent_before": persistentIs(node.db(), blocks[0].Root())}
	if os.Getenv("EXTRACTOR_PINS_CHILD_PIN") == "1" {
		if err := provider.Pin(pinFor("obs-volatile", identity, blocks[0].Header())); err != nil {
			t.Fatal(err)
		}
	}
	state["block1_persistent_after"] = persistentIs(node.db(), blocks[0].Root())
	state["persistent_state_id"] = rawdb.ReadPersistentStateID(node.db())
	data, err := json.Marshal(state)
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "child.json"), data, 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {}
}

func runPathChild(t *testing.T, dir string, pin bool) map[string]any {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestNativePathPinChild$", "-test.count=1")
	command.Env = append(os.Environ(), pathChildEnv+"="+dir, fmt.Sprintf("EXTRACTOR_PINS_CHILD_PIN=%d", map[bool]int{false: 0, true: 1}[pin]))
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("child was not killed: %v\n%s", err, output)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child did not end by SIGKILL: %v\n%s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(dir, "child.json"))
	if err != nil {
		t.Fatalf("child state: %v\n%s", err, output)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// TestNativePathPinSurvivesKill covers PIN-01 in path mode (D108, P3): a
// SIGKILL after the pin loses the in-memory layers of the path store, the
// pinned parent state survives as the persistent layer and native recovery
// resumes from it; the unpinned control loses the state and rewinds.
func TestNativePathPinSurvivesKill(t *testing.T) {
	blocks := familyBlocks(t)
	genesis := familyGenesis(t)
	expected := StoreIdentity{StoreID: testStore, ChainID: genesis.Config.ChainID.String(),
		GenesisHash: genesis.ToBlock().Hash(), StateScheme: rawdb.PathScheme}
	for _, pinned := range []bool{true, false} {
		name := map[bool]string{true: "pinned", false: "control"}[pinned]
		dir := workDir(t, "path-"+name)
		child := runPathChild(t, dir, pinned)
		if child["block1_persistent_before"] != false || child["head"] != float64(2) {
			t.Fatalf("block 1 state was persistent before the pin: %v", child)
		}
		before := inventory(t, dir)
		report, preflightErr := Preflight(dir, expected)
		if !sameInventory(before, inventory(t, dir)) {
			t.Fatal("preflight changed the store")
		}
		db, closeDB := openStore(t, dir)
		persistent := persistentIs(db, blocks[0].Root())
		closeDB()
		node := openPathNode(t, dir)
		head := node.head()
		resumed := node.chain.HasState(blocks[0].Root())
		recovered := map[string]any{"pinned": pinned, "child": child, "preflight_pins": report.Pins,
			"block1_persistent_at_restart": persistent, "recovered_head": head.Number.Uint64(),
			"recovered_root": head.Root.Hex(), "block1_state_resumed": resumed}
		if pinned {
			if child["block1_persistent_after"] != true || preflightErr != nil || len(report.Pins) != 1 ||
				!persistent || !resumed || head.Root != blocks[0].Root() {
				t.Fatalf("pinned recovery failed: %v %v", recovered, preflightErr)
			}
			want := uint64(1)
			if blocks[1].Root() == blocks[0].Root() {
				want = 2
			}
			if head.Number.Uint64() != want {
				t.Fatalf("native recovery head %d, want %d", head.Number.Uint64(), want)
			}
			provider, err := New(node.db(), node.trieDB(), expected)
			if err != nil {
				t.Fatal(err)
			}
			if err := provider.Release("obs-volatile"); err != nil {
				t.Fatal(err)
			}
		} else if child["block1_persistent_after"] != false || persistent || resumed || head.Number.Uint64() != 0 ||
			preflightErr != nil || len(report.Pins) != 0 {
			t.Fatalf("unpinned control did not lose block 1 state: %v %v", recovered, preflightErr)
		}
		node.stop()
		record(t, name, recovered)
	}
}

// TestUnitStoreScheme covers the scheme reader of the read-only preflight
// (D108): the stopped home names the scheme its store records.
func TestUnitStoreScheme(t *testing.T) {
	genesis := familyGenesis(t)
	for _, item := range []struct {
		name string
		path bool
		want string
	}{{"hash", false, rawdb.HashScheme}, {"path", true, rawdb.PathScheme}} {
		dir := workDir(t, "scheme-"+item.name)
		initStore(t, dir, genesis, item.path)
		before := inventory(t, dir)
		scheme, err := StoreScheme(dir)
		if err != nil || scheme != item.want {
			t.Fatalf("%s store: %q %v, want %q", item.name, scheme, err, item.want)
		}
		if !sameInventory(before, inventory(t, dir)) {
			t.Fatalf("%s: the scheme reader changed the home", item.name)
		}
	}
	if _, err := StoreScheme(workDir(t, "scheme-empty")); err == nil {
		t.Fatal("a home without a store has a scheme")
	}
}
