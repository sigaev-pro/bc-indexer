// Test-only probe for the E01-02.inputs Bor block. It builds solely in the
// accepted Bor native module graph: the harness copies it to
// cmd/extractor-bor-inputs-probe and the shared source_inputs package to its
// sourceinputs subdirectory.
//
// generate: seal a private Bor chain whose sprint starts read state sync events
// and spans from the Heimdall stub.
// attempt: import that chain with native InsertChain while every counted
// Heimdall response passes through the dependency log first.
// scenario: run a plan of attempts as child processes and record the store
// between them.
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/cmd/extractor-bor-inputs-probe/heimdallstub"
	"github.com/ethereum/go-ethereum/cmd/extractor-bor-inputs-probe/sourceinputs"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

// The shared package writes through the native key-value store.
var _ sourceinputs.Store = ethdb.KeyValueStore(nil)

const (
	signerAddress = "0x71562b71999873db5b286df957af199ec94617f7"
	chainBlocks   = 9
	// The stand-in marker is test-only; the real EXEC_READY binding belongs to E01-02.admission (D98).
	markerPrefix = "extractor-test-only-stand-in-exec-ready/"
	logPrefix    = "extractor-source-inputs-v1/"
	// The normalization profile item names the test-only probe profile, as in the Geth block.
	normalizationProfile = "test-only-probe-v1"
	// Bor stores a vote snapshot every checkpointInterval blocks (consensus/bor/bor.go).
	checkpointInterval = 1024
)

func readBounded(path string, maximum int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, fmt.Errorf("%s is not a bounded regular file", path)
	}
	return io.ReadAll(io.LimitReader(f, maximum))
}

func saveJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func testKey(source string) (*ecdsa.PrivateKey, error) {
	data, err := readBounded(source, 1024*1024)
	if err != nil {
		return nil, err
	}
	matches := regexp.MustCompile(`(?m)^\s*privKey\s*=\s*"([0-9a-fA-F]{64})"\s*$`).FindAllSubmatch(data, -1)
	if len(matches) != 1 {
		return nil, errors.New("missing unique tracked span-0 test key")
	}
	key, err := crypto.HexToECDSA(string(matches[0][1]))
	if err != nil || crypto.PubkeyToAddress(key.PublicKey) != common.HexToAddress(signerAddress) {
		return nil, errors.New("tracked test signer identity mismatch")
	}
	return key, nil
}

// loadGenesis reads the fixture genesis and builds its effective chain config:
// the genesis "config" object with stateSyncConfirmationDelay set to {"0": delay}.
// Bor reads this map at every sprint start after Indore and panics when it is
// empty; the test genesis omits it. The config bytes are canonical JSON (sorted
// keys, no spaces, numbers as written in the file), and the returned genesis
// runs with exactly their decoding, so the capsule item is the config native
// Bor uses.
func loadGenesis(path string, delay uint64) (*core.Genesis, []byte, []byte, error) {
	data, err := readBounded(path, 1024*1024)
	if err != nil {
		return nil, nil, nil, err
	}
	var genesis core.Genesis
	if err = json.Unmarshal(data, &genesis); err != nil {
		return nil, nil, nil, err
	}
	if genesis.Config == nil || genesis.Config.Bor == nil || genesis.Config.ChainID.Cmp(big.NewInt(15001)) != 0 {
		return nil, nil, nil, errors.New("expected private Bor genesis 15001")
	}
	if len(genesis.Config.Bor.StateSyncConfirmationDelay) != 0 {
		return nil, nil, nil, errors.New("the fixture genesis already sets a state sync confirmation delay")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var raw map[string]any
	if err = decoder.Decode(&raw); err != nil {
		return nil, nil, nil, err
	}
	object, ok := raw["config"].(map[string]any)
	if !ok {
		return nil, nil, nil, errors.New("the genesis config is not an object")
	}
	borObject, ok := object["bor"].(map[string]any)
	if !ok {
		return nil, nil, nil, errors.New("the genesis Bor config is not an object")
	}
	borObject["stateSyncConfirmationDelay"] = map[string]any{"0": json.Number(fmt.Sprint(delay))}
	config, err := json.Marshal(object)
	if err != nil {
		return nil, nil, nil, err
	}
	var effective params.ChainConfig
	if err = json.Unmarshal(config, &effective); err != nil {
		return nil, nil, nil, err
	}
	genesis.Config = &effective
	return &genesis, data, config, nil
}

// capsuleItems builds the static input manifest of one observation: the import
// of the sealed blocks on top of the genesis. Its parent is the genesis block,
// and Bor's snapshot walk from that parent reads only the genesis header.
func capsuleItems(genesis *core.Genesis, genesisBytes, config []byte, blocks []*types.Block, blocksBytes []byte,
	commit string) ([]sourceinputs.Item, error) {
	if len(blocks) == 0 || blocks[0].NumberU64() != 1 {
		return nil, errors.New("the probe imports a chain that starts at block 1")
	}
	base := genesis.ToBlock()
	parent, err := rlp.EncodeToBytes(base.Header())
	if err != nil {
		return nil, err
	}
	ancestors, err := rlp.EncodeToBytes([]*types.Header{base.Header()})
	if err != nil {
		return nil, err
	}
	fork, err := forkItem(genesis.Config, base, blocks[len(blocks)-1])
	if err != nil {
		return nil, err
	}
	return []sourceinputs.Item{
		{Name: "genesis", Data: genesisBytes},
		{Name: "blocks", Data: blocksBytes},
		{Name: "parent-header", Data: parent},
		{Name: "parent-hash", Data: base.Hash().Bytes()},
		{Name: "parent-root", Data: base.Root().Bytes()},
		{Name: "ancestor-headers", Data: ancestors},
		{Name: "chain-config", Data: config},
		{Name: "fork-id", Data: fork},
		{Name: "native-commit", Data: []byte(commit)},
		{Name: "normalization-profile", Data: []byte(normalizationProfile)},
	}, nil
}

// forkItem is the native EIP-2124 fork ID at the last block of the observation.
func forkItem(config *params.ChainConfig, genesis *types.Block, last *types.Block) ([]byte, error) {
	id := forkid.NewID(config, genesis, last.NumberU64(), last.Time())
	return json.Marshal(map[string]string{"hash": hex.EncodeToString(id.Hash[:]), "next": fmt.Sprint(id.Next)})
}

// tamperItem changes the last byte of one item. It is the negative control of
// that item: the cross-check below must refuse it before anything is sealed.
func tamperItem(items []sourceinputs.Item, name string) error {
	for index := range items {
		if items[index].Name == name && len(items[index].Data) > 0 {
			data := bytes.Clone(items[index].Data)
			data[len(data)-1] ^= 1
			items[index].Data = data
			return nil
		}
	}
	return fmt.Errorf("unknown tamper item %q", name)
}

// checkItems cross-checks the capsule items against each other, the sealed
// blocks and the genesis item before the capsule is sealed.
func checkItems(items []sourceinputs.Item, genesis *core.Genesis, blocks []*types.Block) error {
	data := map[string][]byte{}
	for _, item := range items {
		data[item.Name] = item.Data
	}
	disagree := func(name, other string) error {
		return fmt.Errorf("capsule item %s disagrees with %s", name, other)
	}
	var parent types.Header
	if rlp.DecodeBytes(data["parent-header"], &parent) != nil || parent.Number == nil ||
		parent.Hash() != blocks[0].ParentHash() || parent.Number.Uint64()+1 != blocks[0].NumberU64() {
		return disagree("parent-header", "the first sealed block")
	}
	if !bytes.Equal(data["parent-hash"], parent.Hash().Bytes()) {
		return disagree("parent-hash", "the parent header")
	}
	if !bytes.Equal(data["parent-root"], parent.Root.Bytes()) {
		return disagree("parent-root", "the parent header")
	}
	var ancestors []*types.Header
	if rlp.DecodeBytes(data["ancestor-headers"], &ancestors) != nil || len(ancestors) == 0 {
		return disagree("ancestor-headers", "the Bor snapshot walk")
	}
	// Bor's snapshot walk goes back from the parent to the genesis or to the
	// nearest checkpoint header; the list holds every header it may read.
	for index, header := range ancestors {
		if header.Number == nil || (index > 0 && (header.ParentHash != ancestors[index-1].Hash() ||
			header.Number.Uint64() != ancestors[index-1].Number.Uint64()+1 ||
			header.Number.Uint64()%checkpointInterval == 0)) {
			return disagree("ancestor-headers", "the Bor snapshot walk")
		}
	}
	first := ancestors[0].Number.Uint64()
	if ancestors[len(ancestors)-1].Hash() != parent.Hash() || first%checkpointInterval != 0 {
		return disagree("ancestor-headers", "the Bor snapshot walk")
	}
	base := genesis.ToBlock()
	if first == 0 && ancestors[0].Hash() != base.Hash() {
		return disagree("ancestor-headers", "the genesis item")
	}
	fork, err := forkItem(genesis.Config, base, blocks[len(blocks)-1])
	if err != nil || !bytes.Equal(data["fork-id"], fork) {
		return disagree("fork-id", "the chain config")
	}
	if string(data["normalization-profile"]) != normalizationProfile {
		return disagree("normalization-profile", "the probe profile")
	}
	return nil
}

// backend starts a native Bor backend in its own data directory. Heimdall is
// the stub behind the process transport.
func backend(genesis *core.Genesis, dataDir string) (*node.Node, *eth.Ethereum, error) {
	stack, err := node.New(&node.Config{DataDir: dataDir,
		P2P: p2p.Config{NoDiscovery: true, MaxPeers: 0, ListenAddr: "127.0.0.1:0"}})
	if err != nil {
		return nil, nil, err
	}
	cfg := ethconfig.Defaults
	cfg.Genesis, cfg.NetworkId, cfg.SyncMode = genesis, 15001, downloader.FullSync
	cfg.NoPruning, cfg.StateScheme = true, "hash"
	cfg.DatabaseCache, cfg.DatabaseHandles = 64, 64
	cfg.TrieCleanCache, cfg.TrieDirtyCache, cfg.SnapshotCache = 32, 32, 0
	cfg.WithoutHeimdall, cfg.DevFakeAuthor = false, false
	cfg.HeimdallURL, cfg.HeimdallgRPCAddress, cfg.HeimdallWSAddress = heimdallstub.URL, "", ""
	cfg.HeimdallTimeout = 5 * time.Second
	cfg.ParallelEVM.Enable, cfg.EnablePipelinedImportSRC = false, false
	cfg.EnableParallelStatelessImport, cfg.EnablePreconfs, cfg.AcceptPreconfTx = false, false, false
	backend, err := eth.New(stack, &cfg)
	if err != nil {
		stack.Close()
		return nil, nil, fmt.Errorf("create native Bor backend: %w", err)
	}
	if _, ok := backend.Engine().(*bor.Bor); !ok {
		stack.Close()
		return nil, nil, errors.New("native engine is not Bor")
	}
	return stack, backend, nil
}

// shutdown stops the chain and engine before the node closes the databases;
// the node was never started, so Close alone would not stop them.
func shutdown(stack *node.Node, backend *eth.Ethereum) {
	// Close the engine first: it stops the span store polls and the milestone
	// fetcher, so no Heimdall call is in flight while the chain stops.
	backend.Engine().Close()
	backend.TxPool().Close()
	backend.BlockChain().Stop()
	stack.Close()
}

func generate(args []string) error {
	flags := flag.NewFlagSet("generate", flag.ContinueOnError)
	genesisPath := flags.String("genesis", "", "genesis JSON")
	keySource := flags.String("keysource", "", "tests/bor/helper.go of the accepted source")
	bodies := flags.String("bodies", "", "stub response directory")
	output := flags.String("out", "", "new output directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	genesis, _, _, err := loadGenesis(*genesisPath, 0)
	if err != nil {
		return err
	}
	key, err := testKey(*keySource)
	if err != nil {
		return err
	}
	if err = os.Mkdir(*output, 0o700); err != nil {
		return err
	}
	transport := heimdallstub.NewRecorder(heimdallstub.Stub{Bodies: *bodies, Variant: "a"}, nil, -1, nil)
	http.DefaultTransport = transport
	stack, backend, err := backend(genesis, filepath.Join(*output, "generation-data"))
	if err != nil {
		return err
	}
	defer shutdown(stack, backend)
	engine := backend.Engine().(*bor.Bor)
	chain := backend.BlockChain()
	author := crypto.PubkeyToAddress(key.PublicKey)
	engine.Authorize(author, func(account accounts.Account, mime string, payload []byte) ([]byte, error) {
		if account.Address != author {
			return nil, errors.New("wrong signing account")
		}
		return crypto.Sign(crypto.Keccak256(payload), key)
	})
	var encoded bytes.Buffer
	prepared := make([]map[string]any, 0, chainBlocks)
	ctx := context.Background()
	for number := uint64(1); number <= chainBlocks; number++ {
		parent := chain.CurrentBlock()
		header := &types.Header{ParentHash: parent.Hash(), Number: new(big.Int).SetUint64(number),
			GasLimit: parent.GasLimit, Time: parent.Time + 1, Difficulty: big.NewInt(1)}
		if genesis.Config.IsLondon(header.Number) {
			header.BaseFee = eip1559.CalcBaseFee(genesis.Config, parent)
			if !genesis.Config.IsLondon(parent.Number) {
				limit := parent.GasLimit * genesis.Config.ElasticityMultiplier()
				header.GasLimit = core.CalcGasLimit(limit, limit)
			}
		}
		if err = engine.Prepare(chain, header, false); err != nil {
			return fmt.Errorf("prepare block %d: %w", number, err)
		}
		state, err := chain.StateAt(parent.Root)
		if err != nil {
			return err
		}
		block, _, _, err := engine.FinalizeAndAssemble(chain, header, state, &types.Body{}, nil)
		if err != nil {
			return fmt.Errorf("finalize block %d: %w", number, err)
		}
		sealed := make(chan *consensus.NewSealedBlockEvent, 1)
		if err = engine.Seal(chain, block, nil, sealed, ctx.Done()); err != nil {
			return fmt.Errorf("seal block %d: %w", number, err)
		}
		event := <-sealed
		if event == nil || event.Block == nil {
			return errors.New("missing sealed block")
		}
		block = event.Block
		if _, err = chain.InsertChain([]*types.Block{block}, false); err != nil {
			return fmt.Errorf("import generated block %d: %w", number, err)
		}
		if chain.CurrentBlock().Hash() != block.Hash() {
			return errors.New("generation import did not advance the head")
		}
		if err = rlp.Encode(&encoded, block); err != nil {
			return err
		}
		prepared = append(prepared, map[string]any{"number": number, "hash": block.Hash().Hex(),
			"state_root": block.Root().Hex(), "timestamp": block.Time()})
	}
	if err = writeNew(filepath.Join(*output, "blocks.rlp"), encoded.Bytes()); err != nil {
		return err
	}
	// The parent of block 1 is the genesis header; the driver checks it against the sealed blocks.
	parentHeader, err := rlp.EncodeToBytes(chain.GetHeaderByNumber(0))
	if err != nil {
		return err
	}
	if err = writeNew(filepath.Join(*output, "parent-header.rlp"), parentHeader); err != nil {
		return err
	}
	rows, uncounted := transport.Snapshot()
	return saveJSON(filepath.Join(*output, "generated.json"), map[string]any{
		"genesis_hash": chain.GetBlockByNumber(0).Hash().Hex(), "blocks": prepared,
		"counted_calls": rows, "uncounted_calls": uncounted})
}

func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func readBlocks(path string) ([]*types.Block, error) {
	data, err := readBounded(path, 64*1024*1024)
	if err != nil {
		return nil, err
	}
	stream := rlp.NewStream(bytes.NewReader(data), uint64(len(data)))
	var blocks []*types.Block
	for {
		block := new(types.Block)
		if err := stream.Decode(block); errors.Is(err, io.EOF) {
			return blocks, nil
		} else if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
}

// Dump lists every source-input and marker key of the store with a value digest.
type Dump []map[string]any

func dumpStore(db ethdb.KeyValueStore) (Dump, error) {
	dump := Dump{}
	for _, prefix := range []string{logPrefix, markerPrefix} {
		it := db.NewIterator([]byte(prefix), nil)
		for it.Next() {
			dump = append(dump, map[string]any{"key": string(it.Key()), "value_sha256": heimdallstub.HexSum(it.Value()),
				"value_length": len(it.Value())})
		}
		err := it.Error()
		it.Release()
		if err != nil {
			return nil, err
		}
	}
	return dump, nil
}

func openStore(path string) (*pebble.Database, error) {
	return pebble.New(path, 16, 16, "", false)
}

type attemptResult struct {
	Observation    string                 `json:"observation"`
	Blocks         string                 `json:"blocks"`
	Variant        string                 `json:"variant"`
	UsesLog        bool                   `json:"uses_log"`
	StateSyncDelay uint64                 `json:"state_sync_delay"`
	FaultDelay     int                    `json:"fault_state_sync_delay"`
	Tamper         string                 `json:"tamper"`
	CrashAfter     int                    `json:"crash_after"`
	Error          string                 `json:"error,omitempty"`
	Capsule        *sourceinputs.Capsule  `json:"capsule,omitempty"`
	ConfigMatches  *bool                  `json:"native_config_matches_capsule,omitempty"`
	FrontierAtOpen uint64                 `json:"frontier_at_open"`
	Rows           []heimdallstub.Row     `json:"counted_calls"`
	Uncounted      map[string]int         `json:"uncounted_calls"`
	Inserted       int                    `json:"inserted"`
	ImportError    string                 `json:"import_error,omitempty"`
	PrefixConflict bool                   `json:"prefix_conflict"`
	HeadNumber     uint64                 `json:"head_number"`
	HeadHash       string                 `json:"head_hash"`
	HeadRoot       string                 `json:"head_root"`
	Consumed       *sourceinputs.Consumed `json:"consumed,omitempty"`
	FinishError    string                 `json:"finish_error,omitempty"`
	Marker         map[string]string      `json:"stand_in_marker,omitempty"`
	Crashed        bool                   `json:"crashed"`
}

func attempt(args []string) (returned error) {
	flags := flag.NewFlagSet("attempt", flag.ContinueOnError)
	genesisPath := flags.String("genesis", "", "genesis JSON")
	blocksPath := flags.String("blocks", "", "blocks.rlp to import")
	bodies := flags.String("bodies", "", "stub response directory")
	storePath := flags.String("store", "", "persistent source-input store")
	dataDir := flags.String("datadir", "", "new chain data directory")
	observation := flags.String("observation", "", "observation ID")
	commit := flags.String("commit", "", "Bor source commit")
	variant := flags.String("variant", "a", "stub variant")
	useLog := flags.Bool("log", true, "pass counted calls through the dependency log")
	delay := flags.Uint64("state-sync-delay", 0, "state sync confirmation delay of the effective chain config")
	fault := flags.Int("fault-state-sync-delay", -1, "test fault: run with this delay after sealing the capsule")
	tamper := flags.String("tamper", "", "negative control: change this capsule item before the cross-check")
	crashAfter := flags.Int("crash-after", -1, "lose the process after this saved counted call")
	output := flags.String("out", "", "result JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	result := attemptResult{Observation: *observation, Blocks: filepath.Base(filepath.Dir(*blocksPath)),
		Variant: *variant, UsesLog: *useLog, StateSyncDelay: *delay, FaultDelay: *fault, Tamper: *tamper,
		CrashAfter: *crashAfter}
	defer func() {
		if returned != nil {
			result.Error = returned.Error()
		}
		if err := saveJSON(*output, result); returned == nil {
			returned = err
		}
	}()
	genesis, genesisBytes, config, err := loadGenesis(*genesisPath, *delay)
	if err != nil {
		return err
	}
	blocks, err := readBlocks(*blocksPath)
	if err != nil {
		return err
	}
	blocksBytes, err := readBounded(*blocksPath, 64*1024*1024)
	if err != nil {
		return err
	}
	items, err := capsuleItems(genesis, genesisBytes, config, blocks, blocksBytes, *commit)
	if err != nil {
		return err
	}
	if *tamper != "" {
		if err = tamperItem(items, *tamper); err != nil {
			return err
		}
	}
	// A refused item is never sealed: the cross-check runs before the store opens.
	if err = checkItems(items, genesis, blocks); err != nil {
		return err
	}
	var store *pebble.Database
	var dependencies *sourceinputs.Log
	if *useLog {
		if store, err = openStore(*storePath); err != nil {
			return err
		}
		capsule, err := sourceinputs.SealCapsule(store, *observation, items)
		if err != nil {
			store.Close()
			return err
		}
		result.Capsule = &capsule
		if dependencies, err = sourceinputs.OpenLog(store, *observation); err != nil {
			store.Close()
			return err
		}
		result.FrontierAtOpen = dependencies.Frontier()
	}
	onCrash := func(rows []heimdallstub.Row) {
		result.Rows, result.Crashed = rows, true
		_ = saveJSON(*output+".crash.json", result)
	}
	// A nil *Log inside the interface would not be nil, so convert only a real log.
	var dependencyLog heimdallstub.Log
	if dependencies != nil {
		dependencyLog = dependencies
	}
	sealed, err := json.Marshal(genesis.Config)
	if err != nil {
		if store != nil {
			store.Close()
		}
		return err
	}
	if *fault >= 0 {
		// The AC-CAP-04-02 fault: native Bor runs with a delay that differs from the
		// sealed chain config, so it builds a different state sync request than the
		// lost attempt did. The capsule stays the same; only the log can refuse it.
		faulty, borConfig := *genesis.Config, *genesis.Config.Bor
		borConfig.StateSyncConfirmationDelay = map[string]uint64{"0": uint64(*fault)}
		faulty.Bor = &borConfig
		genesis.Config = &faulty
	}
	transport := heimdallstub.NewRecorder(heimdallstub.Stub{Bodies: *bodies, Variant: *variant}, dependencyLog, *crashAfter, onCrash)
	http.DefaultTransport = transport
	stack, backend, err := backend(genesis, *dataDir)
	if err != nil {
		if store != nil {
			store.Close()
		}
		return err
	}
	chain := backend.BlockChain()
	running, err := json.Marshal(chain.Config())
	matches := err == nil && bytes.Equal(running, sealed)
	result.ConfigMatches = &matches
	if chain.Genesis().Hash() != blocks[0].ParentHash() || (!matches && *fault < 0) {
		shutdown(stack, backend)
		if store != nil {
			store.Close()
		}
		return errors.New("native Bor does not run with the capsule chain config and parent")
	}
	result.Inserted, err = chain.InsertChain(blocks, false)
	if err != nil {
		result.ImportError = err.Error()
		result.PrefixConflict = errors.Is(err, sourceinputs.ErrPrefixConflict)
	}
	head := chain.CurrentBlock()
	result.HeadNumber, result.HeadHash, result.HeadRoot = head.Number.Uint64(), head.Hash().Hex(), head.Root.Hex()
	shutdown(stack, backend)
	result.Rows, result.Uncounted = transport.Snapshot()
	for _, row := range result.Rows {
		if row.Error != "" && strings.Contains(row.Error, sourceinputs.ErrPrefixConflict.Error()) {
			result.PrefixConflict = true
		}
	}
	if store == nil {
		return nil
	}
	defer store.Close()
	if err == nil && head.Hash() == blocks[len(blocks)-1].Hash() {
		consumed, err := dependencies.Finish()
		if err != nil {
			result.FinishError = err.Error()
			return nil
		}
		result.Consumed = &consumed
		marker := map[string]string{"kind": "test-only-stand-in-exec-ready", "observation": consumed.Observation,
			"consumed_manifest_digest": consumed.Digest}
		data, err := json.Marshal(marker)
		if err != nil {
			return err
		}
		if err = store.Put([]byte(markerPrefix+*observation), data); err != nil {
			return err
		}
		if err = store.SyncKeyValue(); err != nil {
			return err
		}
		stored, err := store.Get([]byte(markerPrefix + *observation))
		if err != nil {
			return err
		}
		var read map[string]string
		if err = json.Unmarshal(stored, &read); err != nil {
			return err
		}
		result.Marker = read
	}
	return nil
}

// Plan is the attempt list of one scenario, fixed in the oracle.
type Plan struct {
	Name        string `json:"name"`
	Observation string `json:"observation"`
	Attempts    []struct {
		Blocks         string `json:"blocks"`
		Variant        string `json:"variant"`
		Log            bool   `json:"log"`
		StateSyncDelay uint64 `json:"state_sync_delay"`
		FaultDelay     int    `json:"fault_state_sync_delay"`
		Tamper         string `json:"tamper"`
		CrashAfter     int    `json:"crash_after"`
	} `json:"attempts"`
}

func storeDump(path string) (Dump, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return Dump{}, nil
	}
	db, err := openStore(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return dumpStore(db)
}

func scenario(args []string) error {
	flags := flag.NewFlagSet("scenario", flag.ContinueOnError)
	planPath := flags.String("plan", "", "scenario plan JSON")
	genesisPath := flags.String("genesis", "", "genesis JSON")
	chains := flags.String("chains", "", "directory with one subdirectory per generated chain")
	bodies := flags.String("bodies", "", "stub response directory")
	commit := flags.String("commit", "", "Bor source commit")
	output := flags.String("out", "", "new output directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	data, err := readBounded(*planPath, 1024*1024)
	if err != nil {
		return err
	}
	var plan Plan
	// The oracle plan also carries its expected values; the probe reads only the attempts.
	if err = json.Unmarshal(data, &plan); err != nil {
		return err
	}
	if err = os.Mkdir(*output, 0o700); err != nil {
		return err
	}
	storePath := filepath.Join(*output, "store")
	rows := make([]map[string]any, 0, len(plan.Attempts))
	for index, step := range plan.Attempts {
		before, err := storeDump(storePath)
		if err != nil {
			return err
		}
		out := filepath.Join(*output, fmt.Sprintf("attempt-%d.json", index))
		argv := []string{"attempt", "-genesis", *genesisPath, "-blocks", filepath.Join(*chains, step.Blocks, "blocks.rlp"),
			"-bodies", *bodies, "-store", storePath, "-datadir", filepath.Join(*output, fmt.Sprintf("chain-%d", index)),
			"-observation", plan.Observation, "-commit", *commit, "-variant", step.Variant,
			fmt.Sprintf("-log=%t", step.Log), "-state-sync-delay", fmt.Sprint(step.StateSyncDelay),
			"-fault-state-sync-delay", fmt.Sprint(step.FaultDelay), "-tamper=" + step.Tamper,
			"-crash-after", fmt.Sprint(step.CrashAfter), "-out", out}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		child := exec.CommandContext(ctx, os.Args[0], argv...)
		logFile, err := os.Create(filepath.Join(*output, fmt.Sprintf("attempt-%d.log", index)))
		if err != nil {
			cancel()
			return err
		}
		child.Stdout, child.Stderr = logFile, logFile
		runErr := child.Run()
		cancel()
		logFile.Close()
		status := map[string]any{"exit_code": child.ProcessState.ExitCode(), "signal": ""}
		if ws, ok := child.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			status["signal"] = ws.Signal().String()
		}
		if runErr != nil {
			status["error"] = runErr.Error()
		}
		after, err := storeDump(storePath)
		if err != nil {
			return err
		}
		rows = append(rows, map[string]any{"argv": argv, "status": status, "store_before": before, "store_after": after})
	}
	return saveJSON(filepath.Join(*output, "scenario.json"), map[string]any{"plan": plan, "attempts": rows})
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe generate|attempt|scenario [flags]")
		os.Exit(2)
	}
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelWarn, false)))
	var err error
	switch os.Args[1] {
	case "generate":
		err = generate(os.Args[2:])
	case "attempt":
		err = attempt(os.Args[2:])
	case "scenario":
		err = scenario(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
