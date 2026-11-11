package bsc

// The long private Parlia chain (D99 items 2 and 5) and the parent-state
// exit. The chain is generated here by the native engine (Prepare,
// AssembleBlock, Seal with the public test validator key) and validated by
// the native import; timestamps are the parent's plus the period, so the
// chain is deterministic. Blocks 1 to 994 are imported natively, then 995
// to 1030 through the seam: they cover the epoch block 1000 (system-contract
// calls), the stored checkpoint snapshot at 1024, and capsule windows above
// that checkpoint.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/consensus/parlia"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
)

const (
	longChainLength       = 1030
	longChainObservedFrom = 995
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

// generator seals and imports blocks of the private chain one by one.
type generator struct {
	fx      fixture
	chain   *core.BlockChain
	engine  *parlia.Parlia
	backend *SystemCallBackend
}

func newGenerator(t *testing.T, fx fixture, key *keystore.Key, db ethdb.Database) *generator {
	t.Helper()
	api, backend := NewSystemCallAPI()
	engine := parlia.New(fx.genesis.Config, db, api, fx.genesis.ToBlock().Hash())
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
	backend.Bind(chain, nil)
	return &generator{fx: fx, chain: chain, engine: engine, backend: backend}
}

func (g *generator) close() {
	g.chain.Stop()
	g.engine.Close()
}

// next seals the child of the current head and imports it.
func (g *generator) next(t *testing.T) *types.Block {
	t.Helper()
	parent := g.chain.CurrentBlock()
	header := &types.Header{ParentHash: parent.Hash(), Number: new(big.Int).Add(parent.Number, big.NewInt(1)),
		GasLimit: parent.GasLimit, Time: parent.Time + 3, Difficulty: big.NewInt(1)}
	if err := g.engine.Prepare(g.chain, header); err != nil {
		t.Fatal(err)
	}
	header.Time = parent.Time + 3
	// The base fee that the miner sets from London on; this BSC version has
	// no withdrawals before Cancun, which the fixture does not reach.
	if g.fx.genesis.Config.IsLondon(header.Number) {
		header.BaseFee = eip1559.CalcBaseFee(g.fx.genesis.Config, parent)
	}
	statedb, err := g.chain.StateAt(parent)
	if err != nil {
		t.Fatal(err)
	}
	block, _, err := core.AssembleBlock(g.engine, g.chain, header, statedb, &types.Body{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, stop := make(chan *types.Block, 1), make(chan struct{})
	if err := g.engine.Seal(g.chain, block, result, stop); err != nil {
		t.Fatal(err)
	}
	select {
	case block = <-result:
	case <-time.After(30 * time.Second):
		t.Fatal("seal deadline")
	}
	close(stop)
	if _, err := g.chain.InsertChain(types.Blocks{block}); err != nil {
		t.Fatalf("generated block %d: %v", block.NumberU64(), err)
	}
	return block
}

func generateLongChain(t *testing.T, fx fixture, key *keystore.Key, count int,
	after ...func(*core.BlockChain, *parlia.Parlia, *SystemCallBackend)) []*types.Block {
	t.Helper()
	g := newGenerator(t, fx, key, rawdb.NewMemoryDatabase())
	defer g.close()
	var blocks []*types.Block
	for len(blocks) < count {
		blocks = append(blocks, g.next(t))
	}
	for _, f := range after {
		f(g.chain, g.engine, g.backend)
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
	hashes := map[string]string{}
	for _, number := range []int{longChainEpochBlock, longChainCheckpoint, longChainLength} {
		hashes[fmt.Sprint(number)] = blocks[number-1].Hash().Hex()
	}
	report := map[string]any{
		"length":        longChainLength,
		"observed_from": longChainObservedFrom,
		"hashes":        hashes,
		"warm":          warm,
		"cold":          cold,
	}
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

// The trusted-checkpoint record (D99 item 5). Both tests are off unless
// their variable is set; the driver runs them in their own records.
const (
	// envGenerate names the persistent directory of the generated chain.
	// TestNativeBSCGenerateTrustedChain resumes from what it holds.
	envGenerate = "EXTRACTOR_ADMISSION_TRUSTED_GENERATE"
	// envTrusted names the generated chain file that
	// TestNativeBSCTrustedCheckpoint imports.
	envTrusted = "EXTRACTOR_ADMISSION_TRUSTED"
	// trustedCheckpoint is where a cold snapshot lookup of the observed
	// block's parent takes Parlia's trusted-checkpoint path: the walk down
	// from the parent has collected more than params.FullImmutabilityThreshold
	// headers there, no checkpoint is stored, and 1200 % 1000 == 200.
	trustedCheckpoint = 1200
)

// trustedParent is the parent of the observed block of the trusted record.
func trustedParent() int { return int(params.FullImmutabilityThreshold) + trustedCheckpoint + 50 }

// blockOffsets returns the end offset of each complete block in an RLP
// block file; a truncated tail is ignored.
func blockOffsets(t *testing.T, path string) []int64 {
	t.Helper()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	stream := rlp.NewStream(bufio.NewReaderSize(file, 1<<20), 0)
	var offsets []int64
	var end int64
	for {
		kind, size, err := stream.Kind()
		if err != nil || kind != rlp.List {
			return offsets
		}
		if _, err := stream.Raw(); err != nil {
			return offsets
		}
		end += int64(rlp.ListSize(size))
		offsets = append(offsets, end)
	}
}

func TestNativeBSCGenerateTrustedChain(t *testing.T) {
	dir := os.Getenv(envGenerate)
	if dir == "" {
		t.Skip("set " + envGenerate + " to run")
	}
	started := time.Now()
	fx := loadFixture(t)
	length := trustedParent() + 1
	db := openDisk(t, filepath.Join(dir, "home"))
	defer db.Close()
	g := newGenerator(t, fx, validatorKey(t), db)
	defer g.close()
	// The file and the generator home may disagree after a cut: keep the
	// common prefix, then rewrite the blocks that only the home holds.
	path := filepath.Join(dir, "blocks.rlp")
	offsets := blockOffsets(t, path)
	head := int(g.chain.CurrentBlock().Number.Uint64())
	keep := min(len(offsets), head)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var size int64
	if keep > 0 {
		size = offsets[keep-1]
	}
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(size, 0); err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(file, 1<<20)
	flush := func() {
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	for number := keep + 1; number <= head; number++ {
		if err := rlp.Encode(writer, g.chain.GetBlockByNumber(uint64(number))); err != nil {
			t.Fatal(err)
		}
	}
	for number := head + 1; number <= length; number++ {
		if err := rlp.Encode(writer, g.next(t)); err != nil {
			t.Fatal(err)
		}
		if number%10000 == 0 {
			flush()
		}
	}
	flush()
	digest := sha256.New()
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatal(err)
	}
	last := g.chain.CurrentBlock()
	if int(last.Number.Uint64()) != length || len(blockOffsets(t, path)) != length {
		t.Fatalf("generated %d blocks, want %d", last.Number.Uint64(), length)
	}
	record := map[string]any{"count": length, "sha256": fmt.Sprintf("%x", digest.Sum(nil)), "last_hash": last.Hash().Hex(),
		"genesis_hash": fx.genesis.ToBlock().Hash().Hex(), "resumed_from": head, "kept_in_file": keep,
		"session_seconds": time.Since(started).Seconds()}
	data, err := json.MarshalIndent(record, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chain.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNativeBSCTrustedCheckpoint(t *testing.T) {
	path := os.Getenv(envTrusted)
	if path == "" {
		t.Skip("set " + envTrusted + " to run")
	}
	fx := loadFixture(t)
	work := probeEnv(t, envWork)
	parent := trustedParent()
	home := filepath.Join(work, "home-trusted")
	chain, db := plainChain(t, fx, home)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	stream := rlp.NewStream(bufio.NewReaderSize(file, 1<<20), 0)
	read := func() *types.Block {
		block := new(types.Block)
		if err := stream.Decode(block); err != nil {
			t.Fatal(err)
		}
		return block
	}
	// Native import of the parent chain, keeping the hashes of the blocks
	// where the trusted-checkpoint path can start.
	candidates := map[uint64]common.Hash{}
	started := time.Now()
	var batch types.Blocks
	for number := 1; number <= parent; number++ {
		block := read()
		if block.NumberU64() != uint64(number) {
			t.Fatalf("block %d at position %d", block.NumberU64(), number)
		}
		if number%1000 == 200 {
			candidates[block.NumberU64()] = block.Hash()
		}
		if batch = append(batch, block); len(batch) == 2048 || number == parent {
			if _, err := chain.InsertChain(batch); err != nil {
				t.Fatal(err)
			}
			batch = nil
		}
	}
	importSeconds := time.Since(started).Seconds()
	observed := read()
	if err := stream.Decode(new(types.Block)); !errors.Is(err, io.EOF) {
		t.Fatalf("chain file longer than %d blocks: %v", parent+1, err)
	}
	chain.Stop()
	// Drop every stored Parlia snapshot, so the cold lookup must walk.
	var stored [][]byte
	it := db.NewIterator([]byte("parlia-"), nil)
	for it.Next() {
		if len(it.Key()) == len("parlia-")+common.HashLength {
			stored = append(stored, common.CopyBytes(it.Key()))
		}
	}
	it.Release()
	for _, key := range stored {
		if err := db.Delete(key); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	tr := newTracer(t, filepath.Join(work, "trace-trusted.jsonl"))
	n, err := startNode(t, fx, home, probeProducer, modelNative, tr.emit)
	if err != nil {
		t.Fatal(err)
	}
	defer n.close()
	if err := n.seam.Recover(n.chain); err != nil {
		t.Fatal(err)
	}
	observedStarted := time.Now()
	if _, err := n.chain.InsertChain(types.Blocks{observed}); err != nil {
		t.Fatalf("observed block %d: %v", observed.NumberU64(), err)
	}
	observedSeconds := time.Since(observedStarted).Seconds()
	rows := n.observations()
	if len(rows) != 1 || rows[0].State != string(admission.ExecReady) {
		t.Fatalf("observations %v", rows)
	}
	// The trusted-checkpoint path stores the snapshot it builds; it must be
	// the only candidate block with a stored snapshot.
	var entered []uint64
	var blob []byte
	for number, hash := range candidates {
		if data, err := n.db.Get(append([]byte("parlia-"), hash.Bytes()...)); err == nil {
			entered, blob = append(entered, number), data
		}
	}
	if len(entered) != 1 || entered[0] != trustedCheckpoint {
		t.Fatalf("trusted checkpoint entered at %v, want [%d]", entered, trustedCheckpoint)
	}
	var snap struct {
		Number  uint64            `json:"number"`
		Hash    common.Hash       `json:"hash"`
		Recents map[string]string `json:"recents"`
	}
	if err := json.Unmarshal(blob, &snap); err != nil || snap.Number != trustedCheckpoint || snap.Hash != candidates[trustedCheckpoint] {
		t.Fatalf("trusted snapshot: %v", err)
	}
	report := map[string]any{
		"parent":                   parent,
		"threshold":                params.FullImmutabilityThreshold,
		"entered_at":               entered[0],
		"checkpoint_hash":          candidates[trustedCheckpoint].Hex(),
		"observed_number":          observed.NumberU64(),
		"observed_hash":            observed.Hash().Hex(),
		"deleted_snapshots":        len(stored),
		"trusted_snapshot_sha256":  fmt.Sprintf("%x", sha256.Sum256(blob)),
		"trusted_snapshot_recents": len(snap.Recents),
		"import_seconds":           importSeconds,
		"observed_seconds":         observedSeconds,
	}
	data, err := json.MarshalIndent(report, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "trusted-report.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNativeBSCSystemCallFailureStaysAdmitted(t *testing.T) {
	fx := loadFixture(t)
	work := probeEnv(t, envWork)
	blocks := generateLongChain(t, fx, validatorKey(t), longChainEpochBlock/5)
	home := filepath.Join(work, "home-system-call-admitted")
	chain, db := plainChain(t, fx, home)
	if _, err := chain.InsertChain(blocks[:len(blocks)-1]); err != nil {
		t.Fatal(err)
	}
	chain.Stop()
	db.Close()
	other, otherDB := plainChain(t, fx, filepath.Join(work, "home-system-call-other"))
	defer otherDB.Close()
	defer other.Stop()
	tr := newTracer(t, filepath.Join(work, "trace-system-call.jsonl"))
	n, err := startNode(t, fx, home, probeProducer, modelNative, tr.emit)
	if err != nil {
		t.Fatal(err)
	}
	defer n.close()
	if err := n.seam.Recover(n.chain); err != nil {
		t.Fatal(err)
	}
	// The epoch block's validator call reaches the backend for the observed
	// parent, but the backend's chain does not know it: the call fails
	// without a reader refusal, and the obligation stays ADMITTED.
	n.backend.Bind(other, n.seam)
	epoch := blocks[len(blocks)-1:]
	if _, err := n.chain.InsertChain(epoch); !errors.Is(err, parlia.ErrExtractorSystemCall) {
		t.Fatalf("failed system call: %v", err)
	}
	if rows := n.observations(); len(rows) != 1 || rows[0].State != string(admission.Admitted) {
		t.Fatalf("after the failed call: %v", rows)
	}
	n.backend.Bind(n.chain, n.seam)
	if _, err := n.chain.InsertChain(epoch); err != nil {
		t.Fatal(err)
	}
	if rows := n.observations(); len(rows) != 1 || rows[0].State != string(admission.ExecReady) || rows[0].Key != ObservationKey(0) {
		t.Fatalf("after the retry: %v", rows)
	}
}

// wrongSnapshotChain answers every snapshot request with the snapshot JSON
// of another block.
type wrongSnapshotChain struct{ core.ExtractorReader }

func (c wrongSnapshotChain) ExtractorSnapshot(number uint64, hash common.Hash,
	compute func(consensus.ChainHeaderReader) ([]byte, error)) ([]byte, error) {
	return []byte(`{"number":` + fmt.Sprint(number+1) + `,"hash":"` + hash.Hex() + `"}`), nil
}

func TestNativeBSCSnapshotResponseIsMarked(t *testing.T) {
	fx := loadFixture(t)
	chain, db := plainChain(t, fx, filepath.Join(probeEnv(t, envWork), "home-snapshot-response"))
	defer db.Close()
	defer chain.Stop()
	engine := chain.Engine().(*parlia.Parlia)
	if err := engine.VerifyHeader(wrongSnapshotChain{chain}, fx.blocks[0].Header()); !errors.Is(err, parlia.ErrExtractorSnapshotResponse) {
		t.Fatalf("snapshot response of another block: %v", err)
	}
}
