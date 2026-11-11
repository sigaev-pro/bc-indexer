package bsc

// The long private Parlia chain (D99 items 2 and 5) and the parent-state
// exit. The chain is generated here by the native engine (Prepare,
// AssembleBlock, Seal with the public test validator key) and validated by
// the native import; timestamps are the parent's plus the period, so the
// chain is deterministic. Blocks 1 to 989 are imported natively, then 990
// to 1100 through the seam: they cover the epoch block 1000 (system-contract
// calls), the stored checkpoint snapshot at 1024, and capsule windows above
// that checkpoint.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/consensus/parlia"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
)

const (
	longChainLength       = 1100
	longChainObservedFrom = 990
	longChainEpochBlock   = 1000
	longChainCheckpoint   = 1024
	// fixtureValidator is the sole validator of the private fixture genesis.
	fixtureValidator = "0x03735c2ed70a56cd221e0024eb4bf90243c9d6e9"
	// systemContract is the validator set contract that block 1 calls.
	systemContract = "0x0000000000000000000000000000000000001000"
)

func validatorKey(t *testing.T) *keystore.Key {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(probeEnv(t, envInputs), "validator-keystore.json"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := keystore.DecryptKey(data, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(key.Address.Hex(), fixtureValidator) {
		t.Fatalf("validator %s", key.Address.Hex())
	}
	return key
}

func generateLongChain(t *testing.T, fx fixture, key *keystore.Key, count int,
	after ...func(*core.BlockChain, *parlia.Parlia, *SystemCallBackend)) []*types.Block {
	t.Helper()
	db := rawdb.NewMemoryDatabase()
	api, backend := NewSystemCallAPI()
	engine := parlia.New(fx.genesis.Config, db, api, fx.genesis.ToBlock().Hash())
	defer engine.Close()
	engine.Authorize(key.Address,
		func(account accounts.Account, mime string, data []byte) ([]byte, error) {
			return crypto.Sign(crypto.Keccak256(data), key.PrivateKey)
		},
		func(account accounts.Account, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error) {
			return types.SignTx(tx, types.LatestSignerForChainID(chainID), key.PrivateKey)
		})
	config := core.DefaultConfig()
	config.StateScheme, config.ArchiveMode, config.SnapshotLimit = rawdb.HashScheme, true, 0
	chain, err := core.NewBlockChain(db, fx.genesis, engine, config)
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()
	backend.Bind(chain, nil)
	var blocks []*types.Block
	for number := 1; number <= count; number++ {
		parent := chain.CurrentBlock()
		header := &types.Header{ParentHash: parent.Hash(), Number: big.NewInt(int64(number)),
			GasLimit: parent.GasLimit, Time: parent.Time + 3, Difficulty: big.NewInt(1)}
		if err := engine.Prepare(chain, header); err != nil {
			t.Fatal(err)
		}
		header.Time = parent.Time + 3
		// The base fee that the miner sets from London on; this BSC version
		// has no withdrawals before Cancun, which the fixture does not reach.
		body := &types.Body{}
		if fx.genesis.Config.IsLondon(header.Number) {
			header.BaseFee = eip1559.CalcBaseFee(fx.genesis.Config, parent)
		}
		statedb, err := chain.StateAt(parent)
		if err != nil {
			t.Fatal(err)
		}
		block, _, err := core.AssembleBlock(engine, chain, header, statedb, body, nil)
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
			t.Fatalf("generated block %d: %v", number, err)
		}
		blocks = append(blocks, block)
	}
	for _, f := range after {
		f(chain, engine, backend)
	}
	return blocks
}

// longRun is one import of the long chain.
type longRun struct {
	Exec      map[uint64]string   `json:"exec_digests"`
	Snapshots map[uint64][]string `json:"snapshot_response_sha256"`
	Calls     map[uint64]int      `json:"system_calls_on_parent"`
	States    map[string]int      `json:"states"`
	Window    []uint64            `json:"window_of_last_block"`
	Stored    uint64              `json:"stored_checkpoint_number"`
	Restarts  int                 `json:"restarts"`
}

// importLongChain imports blocks 1..observedFrom-1 natively, then the rest
// through the seam. With cold set, the node restarts before every observed
// block, so each snapshot lookup starts from an empty engine.
func importLongChain(t *testing.T, fx fixture, blocks []*types.Block, home string, cold bool, tr *tracer) longRun {
	t.Helper()
	chain, db := plainChain(t, fx, home)
	if _, err := chain.InsertChain(blocks[:longChainObservedFrom-1]); err != nil {
		t.Fatal(err)
	}
	chain.Stop()
	db.Close()
	run := longRun{Exec: map[uint64]string{}, Snapshots: map[uint64][]string{}, Calls: map[uint64]int{}, States: map[string]int{}}
	keys := map[string]uint64{}
	var mu sync.Mutex
	emit := func(e Event) {
		tr.emit(e)
		mu.Lock()
		defer mu.Unlock()
		switch e["event"] {
		case "begin":
			keys[e["observation"].(string)] = e["number"].(uint64)
		case "handoff":
			number := keys[e["observation"].(string)]
			run.Snapshots[number] = append(run.Snapshots[number], e["response_sha256"].(string))
		case "exec-ready":
			run.Exec[keys[e["observation"].(string)]] = e["exec_digest"].(string)
		}
	}
	var n *node
	open := func(producer admission.Producer) {
		var err error
		if n, err = startNode(t, fx, home, producer, modelNative, emit); err != nil {
			t.Fatal(err)
		}
		if err := n.seam.Recover(n.chain); err != nil {
			t.Fatal(err)
		}
	}
	open(probeProducer)
	for _, block := range blocks[longChainObservedFrom-1:] {
		if cold && block.NumberU64() > longChainObservedFrom {
			n.close()
			open(admission.Producer{})
			run.Restarts++
		}
		if _, err := n.chain.InsertChain(types.Blocks{block}); err != nil {
			t.Fatalf("block %d: %v", block.NumberU64(), err)
		}
		if calls := n.backend.Calls()[block.ParentHash()]; calls > 0 {
			run.Calls[block.NumberU64()] += calls
		}
	}
	for _, row := range n.observations() {
		run.States[row.State]++
		if row.Candidate.Number == longChainLength {
			static, err := ReadStatic(n.seam.store, row.Key, n.seam.genesis, fx.commit)
			if err != nil {
				t.Fatal(err)
			}
			run.Window = []uint64{static.Parent.Number.Uint64(), static.Ancestors[len(static.Ancestors)-1].Number.Uint64()}
		}
	}
	checkpoint := blocks[longChainCheckpoint-1]
	blob, err := n.db.Get(append([]byte("parlia-"), checkpoint.Hash().Bytes()...))
	if err != nil {
		t.Fatalf("stored checkpoint snapshot: %v", err)
	}
	var stored struct {
		Number uint64      `json:"number"`
		Hash   common.Hash `json:"hash"`
	}
	if err := json.Unmarshal(blob, &stored); err != nil || stored.Hash != checkpoint.Hash() {
		t.Fatalf("stored checkpoint snapshot: %v", err)
	}
	run.Stored = stored.Number
	n.close()
	return run
}

func TestNativeBSCLongChain(t *testing.T) {
	fx := loadFixture(t)
	work := probeEnv(t, envWork)
	blocks := generateLongChain(t, fx, validatorKey(t), longChainLength)
	var encoded bytes.Buffer
	for _, block := range blocks {
		if err := rlp.Encode(&encoded, block); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "long-blocks.rlp"), encoded.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	warm := importLongChain(t, fx, blocks, filepath.Join(work, "home-long-warm"), false, newTracer(t, filepath.Join(work, "trace-long-warm.jsonl")))
	cold := importLongChain(t, fx, blocks, filepath.Join(work, "home-long-cold"), true, newTracer(t, filepath.Join(work, "trace-long-cold.jsonl")))
	observed := longChainLength - longChainObservedFrom + 1
	for name, run := range map[string]longRun{"warm": warm, "cold": cold} {
		if run.States[string(admission.ExecReady)] != observed || len(run.States) != 1 || len(run.Exec) != observed {
			t.Fatalf("%s: states %v", name, run.States)
		}
		if run.Calls[longChainEpochBlock] == 0 || len(run.Calls) != 1 {
			t.Fatalf("%s: system calls %v", name, run.Calls)
		}
		if run.Stored != longChainCheckpoint || run.Window[0] != longChainLength-1 || run.Window[1] != longChainLength-BlockHashWindow {
			t.Fatalf("%s: checkpoint %d window %v", name, run.Stored, run.Window)
		}
		for number := uint64(longChainObservedFrom); number <= longChainLength; number++ {
			if len(run.Snapshots[number]) == 0 {
				t.Fatalf("%s: block %d logged no snapshot", name, number)
			}
		}
	}
	// The logged snapshots and the native results do not depend on what
	// the engine holds in memory.
	for number := uint64(longChainObservedFrom); number <= longChainLength; number++ {
		if strings.Join(warm.Snapshots[number], ",") != strings.Join(cold.Snapshots[number], ",") || warm.Exec[number] != cold.Exec[number] {
			t.Fatalf("block %d: warm and cold differ", number)
		}
	}
	if cold.Restarts != observed-1 {
		t.Fatalf("restarts %d", cold.Restarts)
	}
	report := map[string]any{"length": longChainLength, "observed_from": longChainObservedFrom,
		"hashes": map[string]string{"1000": blocks[999].Hash().Hex(), "1024": blocks[1023].Hash().Hex(), "1100": blocks[1099].Hash().Hex()},
		"warm": warm, "cold": cold}
	data, err := json.MarshalIndent(report, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "long-report.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// deleteAccountLeaf deletes the leaf node of addr in the state trie under
// root and returns its hash and blob; the root node stays.
func deleteAccountLeaf(t *testing.T, home string, root common.Hash, addr common.Address) (common.Hash, []byte) {
	t.Helper()
	db := openDisk(t, home)
	defer db.Close()
	nodes := triedb.NewDatabase(db, triedb.HashDefaults)
	tr, err := trie.New(trie.StateTrieID(root), nodes)
	if err != nil {
		t.Fatal(err)
	}
	proof := memorydb.New()
	if err := tr.Prove(crypto.Keccak256(addr.Bytes()), proof); err != nil {
		t.Fatal(err)
	}
	it := proof.NewIterator(nil, nil)
	defer it.Release()
	var leaf common.Hash
	var blob []byte
	for it.Next() {
		hash := common.BytesToHash(it.Key())
		if hash != root {
			leaf, blob = hash, common.CopyBytes(it.Value())
		}
	}
	if blob == nil {
		t.Fatal("no node below the root")
	}
	rawdb.DeleteLegacyTrieNode(db, leaf)
	return leaf, blob
}

func TestNativeBSCParentStateIncomplete(t *testing.T) {
	fx := loadFixture(t)
	home := filepath.Join(probeEnv(t, envWork), "home-parent-state")
	tr := newTracer(t, filepath.Join(probeEnv(t, envWork), "trace-parent-state.jsonl"))
	n, err := startNode(t, fx, home, probeProducer, modelNative, tr.emit)
	if err != nil {
		t.Fatal(err)
	}
	root := n.chain.Genesis().Root()
	n.close()
	leaf, blob := deleteAccountLeaf(t, home, root, common.HexToAddress(systemContract))
	if n, err = startNode(t, fx, home, admission.Producer{}, modelNative, tr.emit); err != nil {
		t.Fatal(err)
	}
	if err := n.seam.Recover(n.chain); err != nil {
		t.Fatal(err)
	}
	_, err = n.chain.InsertChain(fx.blocks[:1])
	rows := n.observations()
	if !errors.Is(err, core.ErrExtractorParentState) || len(rows) != 1 || rows[0].State != string(admission.Admitted) {
		t.Fatalf("missing parent state: %v %v", err, rows)
	}
	n.close()
	// With the state back, the retry resolves the same obligation.
	db := openDisk(t, home)
	rawdb.WriteLegacyTrieNode(db, leaf, blob)
	db.Close()
	if n, err = startNode(t, fx, home, admission.Producer{}, modelNative, tr.emit); err != nil {
		t.Fatal(err)
	}
	defer n.close()
	if err := n.seam.Recover(n.chain); err != nil {
		t.Fatal(err)
	}
	rows = n.observations()
	if len(rows) != 1 || rows[0].State != string(admission.ExecReady) || rows[0].Key != ObservationKey(0) {
		t.Fatalf("after the state came back: %v", rows)
	}
}

func TestNativeBSCSystemCallGuard(t *testing.T) {
	fx := loadFixture(t)
	chain, db := plainChain(t, fx, filepath.Join(probeEnv(t, envWork), "home-system-call"))
	defer db.Close()
	defer chain.Stop()
	if _, err := chain.InsertChain(fx.blocks); err != nil {
		t.Fatal(err)
	}
	parent := fx.blocks[0].Header()
	reader := newReader("obs-1", Static{Parent: parent}, chain.Genesis().Header(), nil, func(Event) {}, func() {}, nil)
	seam := &Seam{}
	seam.active.Store(&observation{seam: seam, key: "obs-1", reader: reader})
	_, backend := NewSystemCallAPI()
	backend.Bind(chain, seam)
	if _, header, err := backend.StateAndHeaderByNumberOrHash(context.Background(), rpc.BlockNumberOrHashWithHash(parent.Hash(), false)); err != nil || header.Hash() != parent.Hash() {
		t.Fatalf("call on the parent: %v", err)
	}
	other := fx.blocks[1].Hash()
	if _, _, err := backend.StateAndHeaderByNumberOrHash(context.Background(), rpc.BlockNumberOrHashWithHash(other, false)); !errors.Is(err, ErrSystemCallBlock) ||
		!errors.Is(reader.Err(), ErrSystemCallBlock) {
		t.Fatalf("call on another block: %v", err)
	}
	if _, _, err := backend.StateAndHeaderByNumberOrHash(context.Background(), rpc.BlockNumberOrHashWithNumber(1)); !errors.Is(err, ErrSystemCallBlock) {
		t.Fatalf("call by number: %v", err)
	}
	if calls := backend.Calls(); calls[parent.Hash()] != 1 || len(calls) != 1 {
		t.Fatalf("calls %v", calls)
	}
	// Start refuses an engine that cannot make system-contract calls.
	home := filepath.Join(probeEnv(t, envWork), "home-no-ethapi")
	nodb := openDisk(t, home)
	defer nodb.Close()
	config := core.DefaultConfig()
	config.StateScheme, config.ArchiveMode, config.SnapshotLimit = rawdb.HashScheme, false, 0
	bare, err := core.NewBlockChain(nodb, fx.genesis, parlia.New(fx.genesis.Config, nodb, nil, fx.genesis.ToBlock().Hash()), config)
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Stop()
	if _, err := Start(admission.NewGuard(), nodb, bare, Config{Producer: probeProducer, GenesisFile: fx.genesisFile,
		NativeCommit: fx.commit}); !errors.Is(err, ErrEngine) {
		t.Fatalf("start without ethAPI: %v", err)
	}
}

func TestNativeBSCSystemCallFailureIsMarked(t *testing.T) {
	fx := loadFixture(t)
	other, otherDB := plainChain(t, fx, filepath.Join(probeEnv(t, envWork), "home-system-call-failure"))
	defer otherDB.Close()
	defer other.Stop()
	generateLongChain(t, fx, validatorKey(t), longChainEpochBlock/5-1, func(chain *core.BlockChain, engine *parlia.Parlia,
		backend *SystemCallBackend) {
		parent := chain.CurrentBlock()
		header := &types.Header{ParentHash: parent.Hash(), Number: big.NewInt(int64(longChainEpochBlock / 5)),
			GasLimit: parent.GasLimit, Time: parent.Time + 3, Difficulty: big.NewInt(1)}
		// The epoch block calls the validator contract on the parent; the
		// backend's chain does not know the parent, so the call fails.
		backend.Bind(other, nil)
		if err := engine.Prepare(chain, header); !errors.Is(err, parlia.ErrExtractorSystemCall) {
			t.Fatalf("failed system call: %v", err)
		}
	})
}
