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

func loadGenesis(path string, delay uint64) (*core.Genesis, []byte, error) {
	data, err := readBounded(path, 1024*1024)
	if err != nil {
		return nil, nil, err
	}
	var genesis core.Genesis
	if err = json.Unmarshal(data, &genesis); err != nil {
		return nil, nil, err
	}
	if genesis.Config == nil || genesis.Config.Bor == nil || genesis.Config.ChainID.Cmp(big.NewInt(15001)) != 0 {
		return nil, nil, errors.New("expected private Bor genesis 15001")
	}
	if len(genesis.Config.Bor.StateSyncConfirmationDelay) != 0 {
		return nil, nil, errors.New("the fixture genesis already sets a state sync confirmation delay")
	}
	// Bor reads this map at every sprint start after Indore and panics when it is
	// empty; the test genesis omits it, so the probe sets it in memory. A nonzero
	// delay is the fault injection of AC-CAP-04-02: Bor itself then builds a
	// different state sync request.
	genesis.Config.Bor.StateSyncConfirmationDelay = map[string]uint64{"0": delay}
	return &genesis, data, nil
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
	genesis, _, err := loadGenesis(*genesisPath, 0)
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
	f, err := os.OpenFile(filepath.Join(*output, "blocks.rlp"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(encoded.Bytes()); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	rows, uncounted := transport.Snapshot()
	return saveJSON(filepath.Join(*output, "generated.json"), map[string]any{
		"genesis_hash": chain.GetBlockByNumber(0).Hash().Hex(), "blocks": prepared,
		"counted_calls": rows, "uncounted_calls": uncounted})
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
	CrashAfter     int                    `json:"crash_after"`
	Capsule        *sourceinputs.Capsule  `json:"capsule,omitempty"`
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
	delay := flags.Uint64("state-sync-delay", 0, "state sync confirmation delay override")
	crashAfter := flags.Int("crash-after", -1, "lose the process after this saved counted call")
	output := flags.String("out", "", "result JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	result := attemptResult{Observation: *observation, Blocks: filepath.Base(filepath.Dir(*blocksPath)),
		Variant: *variant, UsesLog: *useLog, StateSyncDelay: *delay, CrashAfter: *crashAfter}
	defer func() {
		if err := saveJSON(*output, result); returned == nil {
			returned = err
		}
	}()
	genesis, genesisBytes, err := loadGenesis(*genesisPath, *delay)
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
	var store *pebble.Database
	var dependencies *sourceinputs.Log
	if *useLog {
		if store, err = openStore(*storePath); err != nil {
			return err
		}
		capsule, err := sourceinputs.SealCapsule(store, *observation, []sourceinputs.Item{
			{Name: "genesis", Data: genesisBytes}, {Name: "blocks", Data: blocksBytes},
			{Name: "bor-source-commit", Data: []byte(*commit)}})
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
	transport := heimdallstub.NewRecorder(heimdallstub.Stub{Bodies: *bodies, Variant: *variant}, dependencyLog, *crashAfter, onCrash)
	http.DefaultTransport = transport
	stack, backend, err := backend(genesis, *dataDir)
	if err != nil {
		return err
	}
	chain := backend.BlockChain()
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
		if row.Error != "" && regexp.MustCompile(regexp.QuoteMeta(sourceinputs.ErrPrefixConflict.Error())).MatchString(row.Error) {
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
