package stateprovider

// Bor hooks for the shared tests. Blocks of the accepted private fixture are
// imported through a native Bor node (eth backend with the real Bor engine,
// without Heimdall), the same way the accepted preparation generated them.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/state/pruner"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/internal/cli/server"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
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
	block, err := familyGenesis(t).Commit(db, tdb)
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
	statedb, err := state.New(parent.Root, state.NewDatabase(tdb, nil))
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
	t         testing.TB
	stack     *node.Node
	backend   *eth.Ethereum
	snapshots bool
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

// openNode starts a native Bor node in instanceDir with the capture profile:
// hash state scheme, full garbage collection, full sync and the ordinary
// sequential import path. Native snapshots are optional.
func openNode(t testing.TB, instanceDir string, snapshots bool) *testNode {
	t.Helper()
	stack, err := node.New(&node.Config{DataDir: filepath.Dir(instanceDir), Name: filepath.Base(instanceDir),
		P2P: p2p.Config{NoDiscovery: true, MaxPeers: 0, ListenAddr: "127.0.0.1:0"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := ethconfig.Defaults
	cfg.Genesis, cfg.NetworkId, cfg.SyncMode = familyGenesis(t), 15001, downloader.FullSync
	cfg.NoPruning, cfg.StateScheme = false, rawdb.HashScheme
	cfg.DatabaseCache, cfg.DatabaseHandles, cfg.TrieCleanCache, cfg.TrieDirtyCache = 64, 64, 64, 64
	cfg.SnapshotCache = 0
	if snapshots {
		cfg.SnapshotCache = 64
	}
	cfg.WithoutHeimdall, cfg.DevFakeAuthor = true, false
	cfg.ParallelEVM.Enable, cfg.ParallelEVM.Enforce, cfg.EnablePipelinedImportSRC = false, false, false
	cfg.EnableParallelStatelessImport, cfg.EnablePreconfs, cfg.AcceptPreconfTx = false, false, false
	backend, err := eth.New(stack, &cfg)
	if err != nil {
		stack.Close()
		t.Fatal(err)
	}
	if err := stack.Start(); err != nil {
		stack.Close()
		t.Fatal(err)
	}
	return &testNode{t: t, stack: stack, backend: backend, snapshots: snapshots}
}

func (n *testNode) db() ethdb.Database          { return n.backend.ChainDb() }
func (n *testNode) trieDB() *triedb.Database    { return n.backend.BlockChain().TrieDB() }
func (n *testNode) head() *types.Header         { return n.backend.BlockChain().CurrentBlock() }
func (n *testNode) setHead(number uint64) error { return n.backend.BlockChain().SetHead(number) }
func (n *testNode) insert(blocks types.Blocks) error {
	_, err := n.backend.BlockChain().InsertChain(blocks, false)
	return err
}

// stop closes the node. The Bor backend builds snapshots in the background,
// so a snapshot node first waits until the head snapshot verifies.
func (n *testNode) stop() {
	n.t.Helper()
	if n.snapshots {
		deadline := time.Now().Add(60 * time.Second)
		for err := n.backend.BlockChain().Snapshots().Verify(n.head().Root); err != nil; err = n.backend.BlockChain().Snapshots().Verify(n.head().Root) {
			if time.Now().After(deadline) {
				n.t.Fatalf("native snapshot did not complete: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if err := n.stack.Close(); err != nil {
		n.t.Fatal(err)
	}
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
	if _, err := genesis.Commit(db, tdb); err != nil {
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

const baseConfig = `"state.scheme" = "hash"
gcmode = "full"
syncmode = "full"

[parallelevm]
enable = false
enforce = false

[pipeline]
enable-import-src = false

[witness]
syncwithwitnesses = false
parallel-stateless-import = false

[relay]
enable-preconfs = false

[jsonrpc]
accept-preconf-tx = false

[sequencer]
enabled = false
`

// effectiveCase checks one `bor server` argument set. It is accepted when
// reason is empty; otherwise the rejection text must contain reason. The
// config body, when present, is written to a file and passed with --config
// like a deployment.
func effectiveCase(name, reason string, config string, args []string, env map[string]string,
	reflect func(*server.Config) bool) profileCase {
	return profileCase{name: name, reason: reason, run: func(t *testing.T) (bool, any) {
		for key, value := range env {
			t.Setenv(key, value)
		}
		full := append([]string{}, args...)
		if config != "" {
			path := filepath.Join(workDir(t, name), "config.toml")
			if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			full = append([]string{"--config", path}, full...)
		}
		cfg, err := EffectiveConfig(full)
		if err != nil {
			return false, map[string]any{"layer": "native", "error": err.Error()}
		}
		profile, err := CheckProfile(cfg)
		view := map[string]any{"profile": profile, "cache": cfg.Cache.Cache}
		if err != nil {
			view["layer"], view["error"] = "profile", err.Error()
			return false, view
		}
		if reflect != nil && !reflect(cfg) {
			view["layer"] = "reflection"
			return false, view
		}
		return true, view
	}}
}

func profileCases(t testing.TB) []profileCase {
	cache := func(value uint64) func(*server.Config) bool {
		return func(cfg *server.Config) bool { return cfg.Cache.Cache == value }
	}
	on := func(flag string) []string { return []string{"--" + flag + "=true"} }
	return []profileCase{
		effectiveCase("baseline_config", "", baseConfig, nil, nil, nil),
		effectiveCase("allowed_config", "", baseConfig+"\n[cache]\ncache = 1234\n", nil, nil, cache(1234)),
		effectiveCase("allowed_native_args", "", baseConfig+"\n[cache]\ncache = 1234\n", []string{"--cache=2345"}, nil, cache(2345)),
		// The `bor server` parser reads no configuration environment
		// variables at this commit (only BOR_GRPC_TOKEN), so environment
		// cannot select a forbidden value.
		effectiveCase("environment_is_not_a_config_source", "", baseConfig, nil,
			map[string]string{"BOR_STATE_SCHEME": "path", "BOR_GCMODE": "archive", "BOR_PARALLELEVM_ENABLE": "true"}, nil),
		effectiveCase("wrong_hash_profile_default", `state.scheme "path"`, strings.Replace(baseConfig, `"state.scheme" = "hash"`+"\n", "", 1), nil, nil, nil),
		effectiveCase("wrong_hash_profile_config", `state.scheme "path"`, strings.Replace(baseConfig, `= "hash"`, `= "path"`, 1), nil, nil, nil),
		effectiveCase("wrong_hash_profile_flag", `state.scheme "path"`, baseConfig, []string{"--state.scheme=path"}, nil, nil),
		effectiveCase("wrong_gc_profile_archive_flag", `gcmode "archive"`, baseConfig, []string{"--gcmode=archive"}, nil, nil),
		effectiveCase("wrong_gc_profile_archive_config", `gcmode "archive"`, strings.Replace(baseConfig, `gcmode = "full"`, `gcmode = "archive"`, 1), nil, nil, nil),
		effectiveCase("wrong_syncmode_flag", `syncmode "snap"`, baseConfig, []string{"--syncmode=snap"}, nil, nil),
		effectiveCase("bor_pipelined_import", "pipeline.enable-import-src", baseConfig, on("pipeline.enable-import-src"), nil, nil),
		effectiveCase("bor_parallel_stateless", "witness.parallelstatelessimport", baseConfig, on("witness.parallelstatelessimport"), nil, nil),
		effectiveCase("bor_witness_sync", "witness.syncwithwitnesses", baseConfig, on("witness.syncwithwitnesses"), nil, nil),
		effectiveCase("bor_preconfirmation_relay", "relay.enable-preconfs", baseConfig, on("relay.enable-preconfs"), nil, nil),
		effectiveCase("bor_preconfirmation_rpc", "accept-preconf-tx", baseConfig, on("accept-preconf-tx"), nil, nil),
		effectiveCase("bor_blockstm_enable", "parallelevm.enable", baseConfig, on("parallelevm.enable"), nil, nil),
		effectiveCase("bor_blockstm_enforce", "parallelevm.enforce", baseConfig, on("parallelevm.enforce"), nil, nil),
		effectiveCase("bor_blockstm_config", "parallelevm.enable", strings.Replace(baseConfig, "enable = false\nenforce", "enable = true\nenforce", 1), nil, nil, nil),
		effectiveCase("bor_sequencer", "sequencer.enabled", baseConfig, on("sequencer.enabled"), nil, nil),
	}
}

func compliantConfig() *server.Config {
	cfg := server.DefaultConfig()
	cfg.StateScheme, cfg.GcMode, cfg.SyncMode = rawdb.HashScheme, "full", "full"
	cfg.ParallelEVM.Enable, cfg.ParallelEVM.Enforce = false, false
	cfg.Pipeline.EnableImportSRC = false
	cfg.Witness.EnableParallelStatelessImport, cfg.Witness.SyncWithWitnesses = false, false
	cfg.Relay.EnablePreconfs, cfg.JsonRPC.AcceptPreconfTx, cfg.Sequencer.Enabled = false, false, false
	return cfg
}

func TestUnitCheckProfileFields(t *testing.T) {
	if _, err := CheckProfile(compliantConfig()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*server.Config){
		"scheme":     func(c *server.Config) { c.StateScheme = rawdb.PathScheme },
		"gcmode":     func(c *server.Config) { c.GcMode = "archive" },
		"syncmode":   func(c *server.Config) { c.SyncMode = "snap" },
		"enable":     func(c *server.Config) { c.ParallelEVM.Enable = true },
		"enforce":    func(c *server.Config) { c.ParallelEVM.Enforce = true },
		"pipeline":   func(c *server.Config) { c.Pipeline.EnableImportSRC = true },
		"stateless":  func(c *server.Config) { c.Witness.EnableParallelStatelessImport = true },
		"witness":    func(c *server.Config) { c.Witness.SyncWithWitnesses = true },
		"relay":      func(c *server.Config) { c.Relay.EnablePreconfs = true },
		"rpc":        func(c *server.Config) { c.JsonRPC.AcceptPreconfTx = true },
		"sequencer":  func(c *server.Config) { c.Sequencer.Enabled = true },
		"no-evm":     func(c *server.Config) { c.ParallelEVM = nil },
		"no-pipe":    func(c *server.Config) { c.Pipeline = nil },
		"no-witness": func(c *server.Config) { c.Witness = nil },
		"no-relay":   func(c *server.Config) { c.Relay = nil },
		"no-rpc":     func(c *server.Config) { c.JsonRPC = nil },
		"no-seq":     func(c *server.Config) { c.Sequencer = nil },
	} {
		cfg := compliantConfig()
		change(cfg)
		if _, err := CheckProfile(cfg); !errors.Is(err, ErrProfile) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	if _, err := CheckProfile(nil); err == nil {
		t.Fatal("missing config accepted")
	}
}

// TestNativeLegacyPruneOffset covers the marker that the native
// `bor snapshot prune-block` command leaves after deleting old block data:
// preflight rejects the home and leaves it unchanged. The marker is written
// by the test, not by a real prune-block run.
func TestNativeLegacyPruneOffset(t *testing.T) {
	genesis := familyGenesis(t)
	dir := workDir(t, "legacy-offset")
	initStore(t, dir, genesis, false)
	db, closeDB := openStore(t, dir)
	if err := db.Put([]byte("offsetOfCurrentAncientFreezer"), big.NewInt(5).Bytes()); err != nil {
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
