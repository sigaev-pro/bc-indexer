// Test-only probe of the E01-02.admission Bor block. It builds solely in the
// accepted Bor native module graph after the preparation has installed the
// Bor block (test/harness/bor_admission_tree.py): the harness copies it to
// cmd/extractor-bor-admission-probe and the Heimdall stub of the E01-02.inputs
// Bor probe to its heimdallstub subdirectory.
//
// generate: seal a private Bor chain (pre-Rio, past the first 1024-block
// checkpoint, then Rio) against the Heimdall stub.
// attempt: start a native Bor node on a persistent instance directory with
// the startup identity, run the recovery driver, and import a block range
// through the product import path with per-block observations; optional crash
// points lose the process with SIGKILL at one admission step.
// scenario: run a plan of attempts as child processes on one instance
// directory and record the store between them.
//
// An attempt can also send a bor RPC call through the native JSON-RPC server
// while the window of one observed block is open (it must wait at the window
// gate and stay out of the dependency log), replay every saved EXEC_READY
// observation from its saved inputs, and send the same RPC call with cold
// caches outside every window (it then reads Heimdall through the native
// client).
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/cmd/extractor-bor-admission-probe/heimdallstub"
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
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/admission/native/obligation"
	"github.com/ethereum/go-ethereum/extractor/borimport"
	_ "github.com/ethereum/go-ethereum/extractor/borwire"
	"github.com/ethereum/go-ethereum/extractor/retention"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
)

const (
	signerAddress = "0x71562b71999873db5b286df957af199ec94617f7"
	producerID    = "bor"
	runID         = "run-1"
	storeID       = "0123456789abcdef0123456789abcdef"
	// The normalization profile names the test-only probe profile, as in E01-02.inputs.
	normalizationProfile = "test-only-probe-v1"
	instanceName         = "bor"
)

// Key prefixes that the store dump lists.
var dumpPrefixes = []string{"extractor-admission-v1/", "extractor-source-inputs-v1/", "extractor-state-provider-",
	"extractor-admission-run-v1/"}

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

// inputs are the canonical genesis and chain-config items. The driver
// derives both from the accepted test genesis; the node runs with exactly
// their decoding.
type inputs struct {
	genesisBytes []byte
	configBytes  []byte
	genesis      *core.Genesis
}

func loadInputs(genesisPath, configPath string) (inputs, error) {
	genesisBytes, err := readBounded(genesisPath, 4*1024*1024)
	if err != nil {
		return inputs{}, err
	}
	configBytes, err := readBounded(configPath, 1024*1024)
	if err != nil {
		return inputs{}, err
	}
	var genesis core.Genesis
	if err = json.Unmarshal(genesisBytes, &genesis); err != nil {
		return inputs{}, err
	}
	var config params.ChainConfig
	if err = json.Unmarshal(configBytes, &config); err != nil {
		return inputs{}, err
	}
	if config.Bor == nil || config.ChainID.Cmp(big.NewInt(15001)) != 0 || config.Bor.RioBlock == nil {
		return inputs{}, errors.New("expected the private Bor genesis 15001 with a Rio block")
	}
	genesis.Config = &config
	return inputs{genesisBytes: genesisBytes, configBytes: configBytes, genesis: &genesis}, nil
}

// stubTransport answers every Heimdall request from the stub files. With
// rpc set, the first request in the window of rpc.block starts the RPC call
// and answers only once the call waits at the window gate.
type stubTransport struct {
	stub heimdallstub.Stub
	rpc  *rpcInWindow
}

func (s stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body.Close()
	}
	if req.URL.Host != heimdallstub.Host {
		return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
	}
	s.rpc.trigger(req)
	body, status := s.stub.Serve(req.URL)
	return &http.Response{Status: http.StatusText(status), StatusCode: status, Proto: "HTTP/1.1", ProtoMajor: 1,
		ProtoMinor: 1, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)), Request: req}, nil
}

func newStack(dataDir string) (*node.Node, error) {
	return node.New(&node.Config{Name: instanceName, DataDir: dataDir,
		P2P: p2p.Config{NoDiscovery: true, MaxPeers: 0, ListenAddr: "127.0.0.1:0"}})
}

// backend starts the native Bor backend: sequential capture profile (hash
// scheme archive, full sync, no parallel, pipelined, stateless or preconf
// paths), Heimdall behind the process transport.
func backend(stack *node.Node, genesis *core.Genesis) (*eth.Ethereum, error) {
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
		return nil, fmt.Errorf("create native Bor backend: %w", err)
	}
	if _, ok := backend.Engine().(*bor.Bor); !ok {
		return nil, errors.New("native engine is not Bor")
	}
	return backend, nil
}

// shutdown stops the engine and the chain before the node closes the databases.
func shutdown(stack *node.Node, backend *eth.Ethereum) {
	if backend != nil {
		backend.Engine().Close()
		backend.TxPool().Close()
		backend.BlockChain().Stop()
	}
	stack.Close()
}

func generate(args []string) error {
	flags := flag.NewFlagSet("generate", flag.ContinueOnError)
	genesisPath := flags.String("genesis", "", "canonical genesis JSON")
	configPath := flags.String("config", "", "canonical chain-config JSON")
	keySource := flags.String("keysource", "", "tests/bor/helper.go of the accepted source")
	bodies := flags.String("bodies", "", "stub response directory")
	count := flags.Uint64("blocks", 0, "number of blocks")
	output := flags.String("out", "", "new output directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	in, err := loadInputs(*genesisPath, *configPath)
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
	http.DefaultTransport = stubTransport{stub: heimdallstub.Stub{Bodies: *bodies, Variant: "a"}}
	stack, err := newStack(filepath.Join(*output, "generation-data"))
	if err != nil {
		return err
	}
	eth, err := backend(stack, in.genesis)
	if err != nil {
		stack.Close()
		return err
	}
	defer shutdown(stack, eth)
	engine := eth.Engine().(*bor.Bor)
	chain := eth.BlockChain()
	author := crypto.PubkeyToAddress(key.PublicKey)
	engine.Authorize(author, func(account accounts.Account, mime string, payload []byte) ([]byte, error) {
		if account.Address != author {
			return nil, errors.New("wrong signing account")
		}
		return crypto.Sign(crypto.Keccak256(payload), key)
	})
	config := in.genesis.Config
	var encoded bytes.Buffer
	prepared := make([]map[string]any, 0, *count)
	ctx := context.Background()
	for number := uint64(1); number <= *count; number++ {
		parent := chain.CurrentBlock()
		header := &types.Header{ParentHash: parent.Hash(), Number: new(big.Int).SetUint64(number),
			GasLimit: parent.GasLimit, Time: parent.Time + 1, Difficulty: big.NewInt(1)}
		if config.IsLondon(header.Number) {
			header.BaseFee = eip1559.CalcBaseFee(config, parent)
			if !config.IsLondon(parent.Number) {
				limit := parent.GasLimit * config.ElasticityMultiplier()
				header.GasLimit = core.CalcGasLimit(limit, limit)
			}
		}
		if err = engine.Prepare(chain, header, false); err != nil {
			return fmt.Errorf("prepare block %d: %w", number, err)
		}
		// Prepare moves a past header time to the wall clock, and Seal then
		// waits for it. The earliest time that native verification accepts
		// keeps every header in the past, so sealing never waits and the
		// chain is deterministic.
		header.Time = parent.Time + bor.CalcProducerDelay(number, 0, config.Bor)
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
			"state_root": block.Root().Hex(), "receipt_root": block.ReceiptHash().Hex(), "timestamp": block.Time()})
	}
	if err = writeNew(filepath.Join(*output, "blocks.rlp"), encoded.Bytes()); err != nil {
		return err
	}
	genesisHeader, err := rlp.EncodeToBytes(chain.GetHeaderByNumber(0))
	if err != nil {
		return err
	}
	if err = writeNew(filepath.Join(*output, "genesis-header.rlp"), genesisHeader); err != nil {
		return err
	}
	return saveJSON(filepath.Join(*output, "generated.json"), map[string]any{
		"genesis_hash": chain.GetBlockByNumber(0).Hash().Hex(), "blocks": prepared,
		"rio_block": config.Bor.RioBlock.Uint64(), "checkpoint_interval": bor.ExtractorCheckpointInterval})
}

func readBlocks(path string) ([]*types.Block, error) {
	data, err := readBounded(path, 256*1024*1024)
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

// crashPoint loses the process with SIGKILL at one admission step of the
// observation of one block, right after the step's sync returned. A process
// kill keeps every write the process made; the synced frontier is what a
// power loss keeps, so the retry sees the durable state of that point.
type crashPoint struct {
	step   string // begin, pin, seal, admitted, log-N, finish, exec-ready
	block  uint64
	output string
	result func() any
}

func parseCrash(value string) (*crashPoint, error) {
	if value == "" {
		return nil, nil
	}
	step, number, ok := strings.Cut(value, "@")
	block, err := strconv.ParseUint(number, 10, 64)
	if !ok || err != nil || !regexp.MustCompile(`^(begin|pin|seal|admitted|log-[0-9]+|finish|exec-ready)$`).MatchString(step) {
		return nil, fmt.Errorf("bad crash point %q", value)
	}
	return &crashPoint{step: step, block: block}, nil
}

var observing struct {
	sync.Mutex
	block uint64
}

func setObserving(number uint64) {
	observing.Lock()
	observing.block = number
	observing.Unlock()
}

func observingBlock() uint64 {
	observing.Lock()
	defer observing.Unlock()
	return observing.block
}

func (c *crashPoint) fire(step string) {
	if c == nil || c.step != step {
		return
	}
	observing.Lock()
	block := observing.block
	observing.Unlock()
	if block != c.block {
		return
	}
	_ = saveJSON(c.output, c.result())
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		panic(fmt.Sprintf("crash point: kill failed: %v", err))
	}
	select {}
}

// crashDB is the admission's view of the native store. It fires the crash
// point after the sync that made the step's commit record durable.
type crashDB struct {
	ethdb.Database
	crash   *crashPoint
	mu      sync.Mutex
	pending []string
}

func (d *crashDB) Put(key, value []byte) error {
	d.mu.Lock()
	d.pending = append(d.pending, string(key))
	d.mu.Unlock()
	return d.Database.Put(key, value)
}

func (d *crashDB) SyncKeyValue() error {
	if err := d.Database.SyncKeyValue(); err != nil {
		return err
	}
	d.mu.Lock()
	written := d.pending
	d.pending = nil
	d.mu.Unlock()
	for _, key := range written {
		switch {
		case strings.HasPrefix(key, "extractor-admission-v1/") && strings.HasSuffix(key, "/counter"):
			d.crash.fire("begin")
		case strings.HasPrefix(key, "extractor-state-provider-pin-v1-"):
			d.crash.fire("pin")
		case strings.HasPrefix(key, "extractor-source-inputs-v1/") && strings.HasSuffix(key, "/capsule"):
			d.crash.fire("seal")
		case strings.HasSuffix(key, "/admitted"):
			d.crash.fire("admitted")
		case strings.HasPrefix(key, "extractor-source-inputs-v1/") && strings.HasSuffix(key, "/closed"):
			d.crash.fire("finish")
		case strings.HasSuffix(key, "/exec-ready"):
			d.crash.fire("exec-ready")
		}
	}
	return nil
}

// headerChain serves only the given headers, as the capsule does.
type headerChain struct {
	config  *params.ChainConfig
	headers []*types.Header
}

func (h headerChain) Config() *params.ChainConfig  { return h.config }
func (h headerChain) CurrentHeader() *types.Header { return h.headers[len(h.headers)-1] }
func (h headerChain) GetHeader(hash common.Hash, number uint64) *types.Header {
	for _, header := range h.headers {
		if header.Hash() == hash && header.Number.Uint64() == number {
			return header
		}
	}
	return nil
}
func (h headerChain) GetHeaderByNumber(number uint64) *types.Header {
	for _, header := range h.headers {
		if header.Number.Uint64() == number {
			return header
		}
	}
	return nil
}
func (h headerChain) GetHeaderByHash(hash common.Hash) *types.Header {
	for _, header := range h.headers {
		if header.Hash() == hash {
			return header
		}
	}
	return nil
}
func (h headerChain) GetTd(common.Hash, uint64) *big.Int { return nil }

// Observed is one observation of the attempt.
type Observed struct {
	Block   uint64           `json:"block"`
	Key     string           `json:"key"`
	Rows    []obligation.Row `json:"counted_calls"`
	Error   string           `json:"error,omitempty"`
	Capsule *CapsuleSummary  `json:"capsule,omitempty"`
}

// CapsuleSummary describes the decoded capsule of an observation.
type CapsuleSummary struct {
	AncestorFirst  uint64 `json:"ancestor_first"`
	AncestorCount  int    `json:"ancestor_count"`
	CheckpointSize int    `json:"checkpoint_snapshot_bytes"`
}

type walkCheck struct {
	Block     uint64 `json:"block"`
	Complete  string `json:"complete_list_error"`
	Truncated string `json:"truncated_list_error"`
	Ancestors int    `json:"ancestors"`
}

type attemptResult struct {
	Args           []string           `json:"args"`
	Error          string             `json:"error,omitempty"`
	Run            string             `json:"run"`
	Pending        []string           `json:"pending_at_open"`
	GoLiveBefore   string             `json:"golive_before_recovery,omitempty"`
	BeginBefore    string             `json:"begin_before_recovery,omitempty"`
	Recovery       *obligation.Report `json:"recovery,omitempty"`
	RecoveryRows   []obligation.Row   `json:"recovery_counted_calls"`
	BaseInserted   int                `json:"base_inserted"`
	Observations   []Observed         `json:"observations"`
	Inserted       int                `json:"inserted"`
	ImportError    string             `json:"import_error,omitempty"`
	HeadNumber     uint64             `json:"head_number"`
	HeadHash       string             `json:"head_hash"`
	SetHead        string             `json:"set_head_error,omitempty"`
	Walk           *walkCheck         `json:"walk_check,omitempty"`
	RetentionHooks bool               `json:"retention_hooks_installed"`
	Passed         map[string]int     `json:"passed_heimdall_calls"`
	Crashed        bool               `json:"crashed"`
	RPC            *RPCCheck          `json:"rpc_in_window,omitempty"`
	Replays        []ReplayRow        `json:"replays,omitempty"`
	RPCControl     *RPCControl        `json:"rpc_control,omitempty"`
}

func attempt(args []string) (returned error) {
	flags := flag.NewFlagSet("attempt", flag.ContinueOnError)
	genesisPath := flags.String("genesis", "", "canonical genesis JSON")
	configPath := flags.String("config", "", "canonical chain-config JSON")
	blocksPath := flags.String("blocks", "", "blocks.rlp to import")
	bodies := flags.String("bodies", "", "stub response directory")
	dataDir := flags.String("datadir", "", "persistent native data directory")
	commit := flags.String("commit", "", "Bor source commit")
	variant := flags.String("variant", "a", "stub variant")
	base := flags.Uint64("base", 0, "import blocks 1..base without observations first")
	from := flags.Uint64("from", 1, "first observed block")
	to := flags.Uint64("to", 0, "last observed block")
	crashValue := flags.String("crash", "", "crash point STEP@BLOCK")
	setHead := flags.Int64("set-head", -1, "after the import, ask native SetHead for this head (must be refused)")
	walk := flags.Uint64("walk-check", 0, "check the native snapshot walk of this observed block from its capsule")
	batch := flags.Bool("batch", false, "import the observed range with one native InsertChain call")
	store := flags.String("store-id", storeID, "producer binding of the native store")
	unobserved := flags.Bool("unobserved", false, "import the range without observations")
	rpcDuring := flags.Uint64("rpc-during", 0, "send bor_getSnapshot of the parent while this block's window is open")
	replay := flags.Bool("replay", false, "after the import, replay every saved EXEC_READY observation")
	control := flags.Uint64("rpc-control", 0, "finally send bor_getSnapshot of this block with cold caches outside every window")
	output := flags.String("out", "", "result JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	result := attemptResult{Args: args, Observations: []Observed{}, RecoveryRows: []obligation.Row{}, Pending: []string{}}
	crash, err := parseCrash(*crashValue)
	if err != nil {
		return err
	}
	rec := &recorder{result: &result}
	if crash != nil {
		crash.output = *output + ".crash.json"
		crash.result = func() any {
			rec.mu.Lock()
			defer rec.mu.Unlock()
			result.Crashed = true
			if n := len(result.Observations); n > 0 {
				result.Observations[n-1].Rows = append([]obligation.Row{}, rec.window...)
			}
			return result
		}
	}
	defer func() {
		if returned != nil {
			result.Error = returned.Error()
		}
		if err := saveJSON(*output, result); returned == nil {
			returned = err
		}
	}()
	result.RetentionHooks = retention.Installed()
	in, err := loadInputs(*genesisPath, *configPath)
	if err != nil {
		return err
	}
	blocks, err := readBlocks(*blocksPath)
	if err != nil {
		return err
	}
	if *to == 0 || *to > uint64(len(blocks)) || *from < 1 || *from > *to || *base >= *from {
		return errors.New("bad block range")
	}
	stack, err := newStack(*dataDir)
	if err != nil {
		return err
	}
	instance := stack.ResolvePath("")
	cfg := borimport.Config{InstanceDir: instance, StoreID: *store, Producer: producerID, Run: runID,
		Genesis: in.genesisBytes, ChainConfig: in.configBytes, Commit: *commit, Normalization: normalizationProfile,
		HeimdallHost: heimdallstub.Host}
	// Startup identity: a stopped store of another identity is refused before the node opens it.
	if err = borimport.Preflight(cfg, in.genesis.Config.ChainID.String(), in.genesis.ToBlock().Hash()); err != nil {
		stack.Close()
		return err
	}
	var inWindow *rpcInWindow
	if *rpcDuring > 0 {
		result.RPC = &RPCCheck{Block: *rpcDuring, Target: *rpcDuring - 1}
		inWindow = &rpcInWindow{check: result.RPC, done: make(chan struct{})}
	}
	http.DefaultTransport = stubTransport{stub: heimdallstub.Stub{Bodies: *bodies, Variant: *variant}, rpc: inWindow}
	eth, err := backend(stack, in.genesis)
	if err != nil {
		stack.Close()
		return err
	}
	chain := eth.BlockChain()
	engine := eth.Engine().(*bor.Bor)
	if inWindow != nil {
		inWindow.engine, inWindow.chain = engine, chain
		rec.rpc = inWindow
	}
	db := &crashDB{Database: eth.ChainDb(), crash: crash}
	node, err := borimport.Start(cfg, db, chain)
	if err != nil {
		shutdown(stack, eth)
		return err
	}
	defer func() {
		result.Passed = node.Router.Passed()
		node.Detach()
		shutdown(stack, eth)
	}()
	router := node.Router
	rec.node = node
	result.Run = node.Run
	router.AfterCounted = func(row obligation.Row) {
		rec.mu.Lock()
		rec.window = append(rec.window, row)
		rec.mu.Unlock()
		crash.fire(fmt.Sprintf("log-%d", row.Sequence))
	}
	// RECOVERY_ONLY: with a saved obligation, GoLive and Begin are refused.
	token := node.Driver.Guard.Acquire()
	saved, err := node.Driver.Admission.Observations(token)
	if err == nil {
		for _, obs := range saved {
			result.Pending = append(result.Pending, fmt.Sprintf("%s %s", obs.Key, obs.State))
		}
		pending := false
		for _, obs := range saved {
			pending = pending || obs.State == admission.Admitted || obs.State == admission.InputPreparing
		}
		if pending {
			result.GoLiveBefore = fmt.Sprint(node.Driver.Admission.GoLive(token))
			_, beginErr := node.Driver.Admission.Begin(token, admission.Candidate{})
			result.BeginBefore = fmt.Sprint(beginErr)
		}
	}
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		return err
	}
	report, err := node.Recover()
	result.Recovery = &report
	result.RecoveryRows = router.Rows()
	if err != nil {
		return fmt.Errorf("recovery: %w", err)
	}
	if *base > 0 {
		// Blocks below the observed range: a chain the node already had.
		node.Detach()
		result.BaseInserted, err = chain.InsertChain(blocks[:*base], false)
		http.DefaultTransport = router
		if err != nil {
			return fmt.Errorf("base import: %w", err)
		}
	}
	if *unobserved {
		node.Detach()
		http.DefaultTransport = router
	} else {
		chain.SetExtractorObserver(rec)
	}
	if *batch {
		// One native batch: the patched import splits it into one block per
		// observation window.
		var pending []*types.Block
		for number := *from; number <= *to; number++ {
			if block := blocks[number-1]; !chain.HasBlockAndState(block.Hash(), number) {
				pending = append(pending, block)
			}
		}
		if len(pending) > 0 {
			result.Inserted, err = chain.InsertChain(pending, false)
			if err != nil {
				result.ImportError = err.Error()
			}
		}
	} else {
		for number := *from; number <= *to; number++ {
			block := blocks[number-1]
			if chain.HasBlockAndState(block.Hash(), number) {
				continue
			}
			if _, err := chain.InsertChain([]*types.Block{block}, false); err != nil {
				result.ImportError = err.Error()
				break
			}
			result.Inserted++
		}
	}
	setObserving(0)
	if inWindow != nil {
		if !inWindow.started.Load() {
			return fmt.Errorf("no Heimdall request in the window of block %d started the RPC call", *rpcDuring)
		}
		select {
		case <-inWindow.done:
		case <-time.After(2 * time.Minute):
			return errors.New("the RPC call did not return after the window closed")
		}
	}
	head := chain.CurrentBlock()
	result.HeadNumber, result.HeadHash = head.Number.Uint64(), head.Hash().Hex()
	if err = describe(node, db, &result); err != nil {
		return err
	}
	if *setHead >= 0 {
		result.SetHead = fmt.Sprint(chain.SetHead(uint64(*setHead)))
	}
	if *walk > 0 {
		result.Walk, err = checkWalk(eth, db, *walk)
		if err != nil {
			return err
		}
	}
	if *replay {
		if result.Replays, err = replayAll(node); err != nil {
			return err
		}
	}
	if *control > 0 {
		result.RPCControl = rpcControl(engine, chain, router, *control)
	}
	return nil
}

// rpcCall sends bor_getSnapshot for block number through the native bor RPC
// service on an in-process JSON-RPC server, created as the node creates its
// in-process endpoint (node.New).
func rpcCall(engine *bor.Bor, chain *core.BlockChain, number uint64) (uint64, string, error) {
	server := rpc.NewServer("", 0, 0)
	defer server.Stop()
	for _, api := range engine.APIs(chain) {
		if err := server.RegisterName(api.Namespace, api.Service); err != nil {
			return 0, "", err
		}
	}
	client := rpc.DialInProc(server)
	defer client.Close()
	var snap struct {
		Number uint64      `json:"number"`
		Hash   common.Hash `json:"hash"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := client.CallContext(ctx, &snap, "bor_getSnapshot", rpc.BlockNumber(number)); err != nil {
		return 0, "", err
	}
	return snap.Number, snap.Hash.Hex(), nil
}

// RPCCheck records the bor RPC call sent inside the window of Block.
type RPCCheck struct {
	Block uint64 `json:"block"`
	// Target is the block whose snapshot the call asks for.
	Target uint64 `json:"target"`
	// TriggerRequest is the Heimdall request of the window that started the call.
	TriggerRequest string `json:"trigger_request"`
	// Waiting is true once the call was seen waiting at the window gate.
	Waiting bool `json:"waiting_at_gate"`
	// ReturnedInWindow is true if the call had returned when the window's
	// observation closed.
	ReturnedInWindow bool   `json:"returned_in_window"`
	Number           uint64 `json:"snapshot_number"`
	Hash             string `json:"snapshot_hash"`
	Error            string `json:"error,omitempty"`
}

// rpcInWindow starts the RPC call from the first Heimdall request of the
// window of its block.
type rpcInWindow struct {
	engine  *bor.Bor
	chain   *core.BlockChain
	check   *RPCCheck
	started atomic.Bool
	done    chan struct{}
}

func (r *rpcInWindow) trigger(req *http.Request) {
	if r == nil || observingBlock() != r.check.Block || !r.started.CompareAndSwap(false, true) {
		return
	}
	r.check.TriggerRequest = req.Method + " " + req.URL.RequestURI()
	go func() {
		defer close(r.done)
		number, hash, err := rpcCall(r.engine, r.chain, r.check.Target)
		r.check.Number, r.check.Hash = number, hash
		if err != nil {
			r.check.Error = err.Error()
		}
	}()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if bor.ExtractorGateWaiting() > 0 {
			r.check.Waiting = true
			return
		}
		select {
		case <-r.done:
			return
		case <-time.After(time.Millisecond):
		}
	}
}

func (r *rpcInWindow) returned() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// ReplayRow is the replay of one saved EXEC_READY observation.
type ReplayRow struct {
	Key   string           `json:"key"`
	Rows  []obligation.Row `json:"counted_calls"`
	Error string           `json:"error,omitempty"`
}

// RPCControl is the same RPC call with cold caches outside every window.
type RPCControl struct {
	Target uint64         `json:"target"`
	Number uint64         `json:"snapshot_number"`
	Hash   string         `json:"snapshot_hash"`
	Error  string         `json:"error,omitempty"`
	Passed map[string]int `json:"passed_heimdall_calls"`
}

// replayAll replays every saved EXEC_READY observation of the admission.
func replayAll(node *borimport.Node) ([]ReplayRow, error) {
	token := node.Driver.Guard.Acquire()
	saved, err := node.Driver.Admission.Observations(token)
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		return nil, err
	}
	rows := []ReplayRow{}
	for _, obs := range saved {
		if obs.State != admission.ExecReady {
			continue
		}
		row := ReplayRow{Key: obs.Key}
		if err := node.Replay(obs.Key); err != nil {
			row.Error = err.Error()
		}
		row.Rows = node.Router.Rows()
		rows = append(rows, row)
	}
	return rows, nil
}

// rpcControl purges the Heimdall caches and sends the RPC call outside every
// window; its Heimdall reads pass through the router to the native client.
func rpcControl(engine *bor.Bor, chain *core.BlockChain, router *obligation.Router, target uint64) *RPCControl {
	before := router.Passed()
	engine.ExtractorPurgeInputs()
	control := &RPCControl{Target: target, Passed: map[string]int{}}
	number, hash, err := rpcCall(engine, chain, target)
	control.Number, control.Hash = number, hash
	if err != nil {
		control.Error = err.Error()
	}
	for path, count := range router.Passed() {
		if count != before[path] {
			control.Passed[path] = count - before[path]
		}
	}
	return control
}

// recorder wraps the admission observer and keeps the counted calls and the
// outcome of every observation, so a batch import is judged block by block.
type recorder struct {
	node   *borimport.Node
	rpc    *rpcInWindow
	mu     sync.Mutex
	result *attemptResult
	window []obligation.Row // counted calls of the open window, for crash points
}

type recorded struct {
	recorder *recorder
	inner    core.ExtractorObservation
}

func (r *recorder) Observe(block *types.Block, parent *types.Header) (core.ExtractorObservation, error) {
	setObserving(block.NumberU64())
	r.mu.Lock()
	r.result.Observations = append(r.result.Observations, Observed{Block: block.NumberU64()})
	r.window = nil
	r.mu.Unlock()
	inner, err := r.node.Observe(block, parent)
	if err != nil {
		r.finish(err)
		return nil, err
	}
	return recorded{recorder: r, inner: inner}, nil
}

func (r *recorder) finish(err error) {
	rows := r.node.Router.Rows()
	r.mu.Lock()
	defer r.mu.Unlock()
	last := &r.result.Observations[len(r.result.Observations)-1]
	last.Rows = rows
	if err != nil {
		last.Error = err.Error()
	}
}

func (o recorded) Done(block *types.Block, receipts types.Receipts, usedGas uint64, err error) error {
	if r := o.recorder.rpc; r != nil && r.check.Block == block.NumberU64() && r.started.Load() {
		// The window is still open here.
		r.check.ReturnedInWindow = r.returned()
	}
	err = o.inner.Done(block, receipts, usedGas, err)
	o.recorder.finish(err)
	return err
}

func (o recorded) Fail(err error) error {
	err = o.inner.Fail(err)
	o.recorder.finish(err)
	return err
}

// describe fills the observation keys and capsule summaries from the store.
func describe(node *borimport.Node, db ethdb.Database, result *attemptResult) error {
	keys, err := capsuleKeys(db)
	if err != nil {
		return err
	}
	byBlock := map[uint64][]string{}
	for _, key := range keys {
		c, err := borimport.ReadCapsule(db, key)
		if err != nil {
			return err
		}
		byBlock[c.Block.NumberU64()] = append(byBlock[c.Block.NumberU64()], key)
	}
	for i := range result.Observations {
		observed := &result.Observations[i]
		list := byBlock[observed.Block]
		if len(list) == 0 {
			continue
		}
		observed.Key = list[len(list)-1]
		c, err := borimport.ReadCapsule(db, observed.Key)
		if err != nil {
			return err
		}
		observed.Capsule = &CapsuleSummary{AncestorFirst: c.Ancestors[0].Number.Uint64(), AncestorCount: len(c.Ancestors),
			CheckpointSize: len(c.Checkpoint)}
	}
	return nil
}

func capsuleKeys(db ethdb.KeyValueStore) ([]string, error) {
	it := db.NewIterator([]byte("extractor-source-inputs-v1/"), nil)
	defer it.Release()
	var keys []string
	for it.Next() {
		key := string(it.Key())
		if strings.HasSuffix(key, "/capsule") {
			keys = append(keys, strings.TrimSuffix(strings.TrimPrefix(key, "extractor-source-inputs-v1/"), "/capsule"))
		}
	}
	sort.Strings(keys)
	return keys, it.Error()
}

// checkWalk runs the native Bor snapshot walk (VerifySeal, no capsule
// snapshot) with cold caches over only the capsule ancestor headers of the
// observed block. With the complete list (the checkpoint header up to the
// parent) it must pass: the walk reads no header below the checkpoint and
// stops at its stored snapshot. Without the checkpoint header and the one
// after it, it must fail: the walk needs every header above the checkpoint.
func checkWalk(eth *eth.Ethereum, db ethdb.Database, number uint64) (*walkCheck, error) {
	keys, err := capsuleKeys(db)
	if err != nil {
		return nil, err
	}
	var c borimport.Capsule
	found := false
	for _, key := range keys {
		capsule, err := borimport.ReadCapsule(db, key)
		if err != nil {
			return nil, err
		}
		if capsule.Block.NumberU64() == number {
			c, found = capsule, true
		}
	}
	if !found {
		return nil, fmt.Errorf("no observation of block %d", number)
	}
	engine := eth.Engine().(*bor.Bor)
	if engine.ExtractorIsRio(number) {
		return nil, errors.New("the walk check applies before Rio")
	}
	config := eth.BlockChain().Config()
	check := &walkCheck{Block: number, Ancestors: len(c.Ancestors)}
	engine.ExtractorPurgeInputs()
	check.Complete = fmt.Sprint(engine.VerifySeal(headerChain{config: config, headers: c.Ancestors}, c.Block.Header()))
	if len(c.Ancestors) < 3 {
		return nil, errors.New("the walk check needs two headers above the checkpoint")
	}
	engine.ExtractorPurgeInputs()
	check.Truncated = fmt.Sprint(engine.VerifySeal(headerChain{config: config, headers: c.Ancestors[2:]}, c.Block.Header()))
	return check, nil
}

// Dump lists every extractor key of the store with its value digest, and
// the value itself for the small records the judge reads.
type Dump []map[string]any

func openStore(path string) (*pebble.Database, error) {
	return pebble.New(path, 16, 16, "", true)
}

func dumpStore(path string) (Dump, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return Dump{}, nil
	}
	db, err := openStore(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	dump := Dump{}
	record := regexp.MustCompile(`/(counter|lowwater|preparing|admitted|exec-ready|abandoned|rejected|capsule|frontier|closed)$|^extractor-state-provider-|^extractor-admission-run-v1/`)
	for _, prefix := range dumpPrefixes {
		it := db.NewIterator([]byte(prefix), nil)
		for it.Next() {
			row := map[string]any{"key": string(it.Key()), "value_sha256": heimdallstub.HexSum(it.Value()),
				"value_length": len(it.Value())}
			if record.MatchString(string(it.Key())) {
				row["value"] = string(it.Value())
			}
			dump = append(dump, row)
		}
		err := it.Error()
		it.Release()
		if err != nil {
			return nil, err
		}
	}
	return dump, nil
}

// Step is one attempt of a scenario plan.
type Step struct {
	Base      uint64 `json:"base"`
	From      uint64 `json:"from"`
	To        uint64 `json:"to"`
	Variant   string `json:"variant"`
	Crash     string `json:"crash"`
	SetHead   int64  `json:"set_head"`
	Walk      uint64 `json:"walk_check"`
	Batch     bool   `json:"batch"`
	PruneDir  string `json:"stale_prune_dir"`
	PruneTool string `json:"prune_state"`
	// Command is the `bor snapshot` command that PruneTool runs: prune-state
	// (the default) or prune-block.
	Command    string `json:"tool_command"`
	StoreID    string `json:"store_id"`
	Unobserved bool   `json:"unobserved"`
	RPCDuring  uint64 `json:"rpc_during"`
	Replay     bool   `json:"replay"`
	RPCControl uint64 `json:"rpc_control"`
}

// Plan is the attempt list of one scenario, fixed in the oracle.
type Plan struct {
	Name     string `json:"name"`
	Attempts []Step `json:"attempts"`
}

func scenario(args []string) error {
	flags := flag.NewFlagSet("scenario", flag.ContinueOnError)
	planPath := flags.String("plan", "", "scenario plan JSON")
	genesisPath := flags.String("genesis", "", "canonical genesis JSON")
	configPath := flags.String("config", "", "canonical chain-config JSON")
	blocks := flags.String("blocks", "", "blocks.rlp")
	bodies := flags.String("bodies", "", "stub response directory")
	commit := flags.String("commit", "", "Bor source commit")
	tools := flags.String("tools", "", "directory with the patched bor binary (bor) and the accepted one (bor-accepted)")
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
	dataDir := filepath.Join(*output, "data")
	store := filepath.Join(dataDir, instanceName, "chaindata")
	ancient := filepath.Join(store, "ancient")
	rows := make([]map[string]any, 0, len(plan.Attempts))
	for index, step := range plan.Attempts {
		before, err := dumpStore(store)
		if err != nil {
			return err
		}
		row := map[string]any{"store_before": before}
		if row["ancient_before"], err = dumpAncient(ancient); err != nil {
			return err
		}
		if step.PruneDir != "" {
			// A leftover state bloom filter in a subdirectory: the node-level
			// check sees only the instance directory, the native pruner walks it all.
			dir := filepath.Join(dataDir, instanceName, step.PruneDir)
			if err = os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			if err = writeNew(filepath.Join(dir, "statebloom.0x"+strings.Repeat("ab", 32)+".bf.gz"), []byte("stale")); err != nil {
				return err
			}
		}
		if step.PruneTool != "" {
			row["prune_state"], err = runTool(filepath.Join(*tools, step.PruneTool), step.Command, dataDir, *output, index)
			if err != nil {
				return err
			}
		} else {
			out := filepath.Join(*output, fmt.Sprintf("attempt-%d.json", index))
			argv := []string{"attempt", "-genesis", *genesisPath, "-config", *configPath, "-blocks", *blocks,
				"-bodies", *bodies, "-datadir", dataDir, "-commit", *commit, "-variant", step.Variant,
				"-base", fmt.Sprint(step.Base), "-from", fmt.Sprint(step.From), "-to", fmt.Sprint(step.To),
				"-crash=" + step.Crash, "-store-id", storeIDOf(step), "-set-head", fmt.Sprint(step.SetHead), "-walk-check", fmt.Sprint(step.Walk), fmt.Sprintf("-batch=%t", step.Batch),
				fmt.Sprintf("-unobserved=%t", step.Unobserved), "-rpc-during", fmt.Sprint(step.RPCDuring),
				fmt.Sprintf("-replay=%t", step.Replay), "-rpc-control", fmt.Sprint(step.RPCControl), "-out", out}
			row["argv"] = argv
			row["status"], err = runChild(os.Args[0], argv, filepath.Join(*output, fmt.Sprintf("attempt-%d.log", index)))
			if err != nil {
				return err
			}
		}
		if row["store_after"], err = dumpStore(store); err != nil {
			return err
		}
		if row["ancient_after"], err = dumpAncient(ancient); err != nil {
			return err
		}
		rows = append(rows, row)
	}
	if err = saveCheckpoints(store, filepath.Join(*output, "checkpoints.json")); err != nil {
		return err
	}
	return saveJSON(filepath.Join(*output, "scenario.json"), map[string]any{"plan": plan, "attempts": rows})
}

// saveCheckpoints writes the checkpoint-snapshot item of every sealed capsule
// (native Bor data that the judge checks against the ancestor rule).
func saveCheckpoints(path, output string) error {
	db, err := openStore(path)
	if err != nil {
		return err
	}
	defer db.Close()
	keys, err := capsuleKeys(db)
	if err != nil {
		return err
	}
	items := []map[string]any{}
	for _, key := range keys {
		c, err := borimport.ReadCapsule(db, key)
		if err != nil {
			return err
		}
		data, err := sourceinputs.ReadItem(db, key, 6)
		if err != nil {
			return err
		}
		items = append(items, map[string]any{"key": key, "block": c.Block.NumberU64(), "data": hex.EncodeToString(data)})
	}
	return saveJSON(output, map[string]any{"checkpoint_items": items})
}

func storeIDOf(step Step) string {
	if step.StoreID != "" {
		return step.StoreID
	}
	return storeID
}

func runChild(binary string, argv []string, logPath string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	child := exec.CommandContext(ctx, binary, argv...)
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()
	child.Stdout, child.Stderr = logFile, logFile
	started := time.Now()
	runErr := child.Run()
	status := map[string]any{"exit_code": child.ProcessState.ExitCode(), "signal": "",
		"seconds": time.Since(started).Seconds()}
	if ws, ok := child.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		status["signal"] = ws.Signal().String()
	}
	if runErr != nil {
		status["error"] = runErr.Error()
	}
	return status, nil
}

// runTool runs `bor snapshot <command>` (prune-state when command is empty)
// of one binary on the scenario data directory and keeps its output. Each
// binary file is named bor, because the native node derives the instance
// directory from the program name.
func runTool(binary, command, dataDir, output string, index int) (map[string]any, error) {
	if command == "" {
		command = "prune-state"
	}
	if command != "prune-state" && command != "prune-block" {
		return nil, fmt.Errorf("unknown tool command %q", command)
	}
	logPath := filepath.Join(output, fmt.Sprintf("attempt-%d.log", index))
	status, err := runChild(binary, []string{"snapshot", command, "--datadir", dataDir}, logPath)
	if err != nil {
		return nil, err
	}
	text, err := readBounded(logPath, 16*1024*1024)
	if err != nil {
		return nil, err
	}
	status["binary"] = binary
	status["command"] = command
	status["refused_live_pins"] = bytes.Contains(text, []byte(stateprovider.ErrLivePins.Error()))
	status["output_sha256"] = heimdallstub.HexSum(text)
	if len(text) > 8192 {
		text = text[len(text)-8192:]
	}
	status["output_tail"] = string(text)
	return status, nil
}

// dumpAncient lists every file under the ancient directory with its digest.
func dumpAncient(dir string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == dir {
			return filepath.SkipDir
		}
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := readBounded(path, 1<<30)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[rel] = heimdallstub.HexSum(data)
		return nil
	})
	return files, err
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
