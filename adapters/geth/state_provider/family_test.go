package stateprovider

// Geth hooks for the shared tests. Blocks of the accepted private fixture are
// imported through the native BlockChain with the ethash faker, which is the
// engine that generated them.

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
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
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

// tailDB is a native database with a synthetic ancient tail. The chain tables
// of Geth share no pruning group, so their tail is the one of the empty group.
type tailDB struct {
	ethdb.Database
	tail uint64
	err  error
}

func (db tailDB) Tail(group string) (uint64, error) { return db.tail, db.err }

// withTail returns db with the given tail of its ancient store, or the error
// that reading the tail returns.
func withTail(db ethdb.Database, tail uint64, err error) ethdb.Database {
	return tailDB{Database: db, tail: tail, err: err}
}

// legacyOffsetKey is the key-value key that holds the offset of a prune-block
// option. Geth at this commit has no such option, so it has none.
func legacyOffsetKey() []byte { return nil }

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
	config := familyGenesis(t).Config
	root, err := statedb.Commit(config.Rules(number, false, parent.Time+1), number.Uint64())
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
	chain, err := core.NewBlockChain(db, familyGenesis(t), ethash.NewFaker(), config)
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
	native, err := pruner.NewPruner(db, pruner.Config{Datadir: instanceDir, BloomSize: 256})
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
	baseConfig = "[Eth]\nStateScheme = \"hash\"\nNoPruning = false\n"
	pathConfig = "[Eth]\nStateScheme = \"path\"\nNoPruning = false\n"
)

var baseArgs = []string{"--state.scheme=hash", "--gcmode=full"}

func profileCases(t testing.TB) []profileCase {
	history := func(value uint64) func(*ethconfigView) bool {
		return func(view *ethconfigView) bool { return view.TransactionHistory == value }
	}
	scheme := func(want string) func(*ethconfigView) bool {
		return func(view *ethconfigView) bool { return view.StateScheme == want }
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
		// The fast-node profile of D108: the flags of a fast node pass no
		// state scheme, and the store keeps its own.
		dumpCase("fastnode_default_scheme", "", []string{"--syncmode=full"}, nil, "", scheme("")),
		dumpCase("path_profile_flag", "", []string{"--state.scheme=path", "--gcmode=full"}, nil, "", scheme("path")),
		dumpCase("path_profile_environment", "", []string{"--gcmode=full"}, []string{"GETH_STATE_SCHEME=path"}, "",
			scheme("path")),
		dumpCase("path_profile_config", "", nil, nil, pathConfig, scheme("path")),
		// The state history matters under the path scheme only.
		dumpCase("allowed_state_history_flag", "", []string{"--state.scheme=path", "--gcmode=full", "--history.state=90000"},
			nil, "", states(90000)),
		dumpCase("allowed_state_history_under_hash", "", []string{"--state.scheme=hash", "--gcmode=full", "--history.state=89999"},
			nil, "", states(89999)),
		// A source cannot override another.
		dumpCase("environment_over_config_cannot_bypass", "gcmode archive", nil, []string{"GETH_GCMODE=archive"},
			baseConfig, nil),
		dumpCase("flag_over_config_cannot_bypass", "gcmode archive", []string{"--gcmode=archive"}, nil, baseConfig, nil),
		// The refusals of the profile.
		dumpCase("wrong_gc_profile_archive_flag", "gcmode archive", []string{"--state.scheme=hash", "--gcmode=archive"}, nil,
			"", nil),
		dumpCase("wrong_gc_profile_archive_environment", "gcmode archive", []string{"--state.scheme=hash"},
			[]string{"GETH_GCMODE=archive"}, "", nil),
		dumpCase("wrong_gc_profile_archive_config", "gcmode archive", nil, nil,
			"[Eth]\nStateScheme = \"hash\"\nNoPruning = true\n", nil),
		dumpCase("forbidden_state_history_flag", "capture constraints: history.state 89999",
			[]string{"--state.scheme=path", "--gcmode=full", "--history.state=89999"}, nil, "", nil),
		dumpCase("forbidden_state_history_config", "capture constraints: history.state 89999", nil, nil,
			pathConfig+"StateHistory = 89999\n", nil),
	}
}

// ethconfigView is the reported subset of the effective native config.
type ethconfigView struct {
	StateScheme        string `json:"state_scheme"`
	NoPruning          bool   `json:"no_pruning"`
	StateHistory       uint64 `json:"state_history"`
	TransactionHistory uint64 `json:"transaction_history"`
}

func viewOf(cfg *ethconfig.Config) ethconfigView {
	return ethconfigView{StateScheme: cfg.StateScheme, NoPruning: cfg.NoPruning, StateHistory: cfg.StateHistory,
		TransactionHistory: cfg.TransactionHistory}
}

func TestUnitCheckProfileFields(t *testing.T) {
	decode := func(body string) *ethconfig.Config {
		cfg, err := DecodeEffectiveConfig([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	for name, body := range map[string]string{
		"hash":                                 baseConfig,
		"no scheme (the flags of a fast node)": "[Eth]\nNoPruning = false\n",
		"path":                                 pathConfig,
		"state history below N under hash":     baseConfig + "StateHistory = 89999\n",
		"state history N under path":           pathConfig + "StateHistory = 90000\n",
		"state history of the entire chain under path": pathConfig + "StateHistory = 0\n",
	} {
		profile, err := CheckProfile(decode(body))
		if err != nil || profile.GCMode != "full" {
			t.Fatalf("%s: %v (profile %v)", name, err, profile)
		}
	}
	for name, c := range map[string]struct{ body, reason string }{
		"archive":                          {"[Eth]\nStateScheme = \"hash\"\nNoPruning = true\n", "gcmode archive"},
		"state history below N under path": {pathConfig + "StateHistory = 89999\n", "history.state 89999 keeps fewer than 90000 blocks"},
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
	profile, err := CheckProfile(decode(pathConfig + "StateHistory = 90000\n"))
	if want := (EffectiveProfile{StateScheme: rawdb.PathScheme, GCMode: "full", StateHistory: 90000}); err != nil ||
		profile != want {
		t.Fatalf("profile %v %v, want %v", profile, err, want)
	}
}

// TestUnitStoreProfile covers the rules that need the scheme of the opened
// store: the flags of a fast node name no scheme, and the state history floor
// holds under the scheme the store has.
func TestUnitStoreProfile(t *testing.T) {
	decode := func(body string) *ethconfig.Config {
		cfg, err := DecodeEffectiveConfig([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	short := decode("[Eth]\nNoPruning = false\nStateHistory = 89999\n")
	profile, err := CheckProfile(short)
	if err != nil || profile.StateScheme != "" {
		t.Fatalf("config without a scheme %v %v", profile, err)
	}
	if profile, err = CheckStoreProfile(short, rawdb.HashScheme); err != nil || profile.StateScheme != rawdb.HashScheme ||
		profile.StateHistory != 89999 {
		t.Fatalf("hash store %v %v", profile, err)
	}
	if _, err = CheckStoreProfile(short, rawdb.PathScheme); !errors.Is(err, ErrProfile) ||
		!strings.Contains(err.Error(), "history.state 89999") {
		t.Fatalf("path store with a short state history %v", err)
	}
	full := decode("[Eth]\nNoPruning = false\nStateHistory = 90000\n")
	if profile, err = CheckStoreProfile(full, rawdb.PathScheme); err != nil || profile.StateScheme != rawdb.PathScheme {
		t.Fatalf("path store %v %v", profile, err)
	}
	for _, scheme := range []string{"bin", ""} {
		if _, err = CheckStoreProfile(full, scheme); !errors.Is(err, ErrProfile) ||
			!strings.Contains(err.Error(), "state scheme") {
			t.Fatalf("store of scheme %q %v", scheme, err)
		}
	}
	named := decode("[Eth]\nStateScheme = \"hash\"\nNoPruning = false\n")
	if _, err = CheckStoreProfile(named, rawdb.PathScheme); !errors.Is(err, ErrProfile) ||
		!strings.Contains(err.Error(), "is not the") {
		t.Fatalf("path store under a hash config %v", err)
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

// TestUnitStateProfiles pins the state profile strings of this client. A
// string binds the profile to every store of its scheme (D108): a changed
// string refuses every bound store, so it changes only with a decision.
func TestUnitStateProfiles(t *testing.T) {
	want := map[string]string{rawdb.HashScheme: "state.scheme=hash;gcmode=full", rawdb.PathScheme: "state.scheme=path;gcmode=full;history.state>=90000"}
	for scheme, profile := range want {
		if stateProfile(scheme) != profile || profileOf(scheme) != fmt.Sprintf("%x", sha256.Sum256([]byte(profile))) {
			t.Fatalf("state profile of the %s scheme is %q, want %q", scheme, stateProfile(scheme), profile)
		}
	}
	if stateProfile("") != "" || stateProfile("bin") != "" {
		t.Fatal("a scheme without a state profile has one")
	}
}
