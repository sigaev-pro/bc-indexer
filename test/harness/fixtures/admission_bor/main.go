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
	"runtime"
	"slices"
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
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/admission/native/obligation"
	"github.com/ethereum/go-ethereum/extractor/bordecision"
	"github.com/ethereum/go-ethereum/extractor/borimport"
	_ "github.com/ethereum/go-ethereum/extractor/borwire"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/decision/native/decider"
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
	"extractor-admission-run-v1/", "extractor-decision-v1/", "extractor-decision-marker-v1/"}

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
	stub  heimdallstub.Stub
	rpc   *rpcInWindow
	delay *delayOnce
}

func (s stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body.Close()
	}
	if req.URL.Host != heimdallstub.Host {
		return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
	}
	s.rpc.trigger(req)
	s.delay.hold(req)
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
	// begin, pin, seal, admitted, log-N, finish, exec-ready; and the decision
	// steps child-root, intent, native-rejected, block-batch, marker, decided.
	step   string
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
	if !ok || err != nil || !regexp.MustCompile(`^(begin|pin|seal|admitted|log-[0-9]+|finish|exec-ready|child-root|intent|native-rejected|block-batch|marker|decided)$`).MatchString(step) {
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
	crash *crashPoint
	// synced, if set, runs for every key whose write a sync made durable,
	// before the crash point fires.
	synced  func(key string)
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
		if d.synced != nil {
			d.synced(key)
		}
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
		case strings.HasPrefix(key, "extractor-decision-v1/") && strings.HasSuffix(key, "/counter"):
			d.crash.fire("intent")
		case strings.HasPrefix(key, "extractor-decision-v1/") && strings.HasSuffix(key, "/decided"):
			d.crash.fire("decided")
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

// Snapshot is the store rows of one observation (admission records, capsule,
// log, pin) right after its EXEC_READY, NATIVE_REJECTED or ABANDONED sync,
// before a decision or the admission cleanup deletes them, with its decoded
// capsule summary and checkpoint-snapshot item when it has a capsule.
type Snapshot struct {
	Rows           Dump            `json:"rows"`
	Capsule        *CapsuleSummary `json:"capsule,omitempty"`
	Block          uint64          `json:"block,omitempty"`
	CheckpointItem string          `json:"checkpoint_item,omitempty"`
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
	RPCBefore      *RPCBefore         `json:"rpc_before_window,omitempty"`
	ExpiredFetch   *ExpiredFetch      `json:"expired_fetch,omitempty"`
	FrozenPins     *FrozenPins        `json:"pins_after_freeze,omitempty"`
	Replays        []ReplayRow        `json:"replays,omitempty"`
	RPCControl     *RPCControl        `json:"rpc_control,omitempty"`
	// Decision bridge (E01-02.decision_bridge).
	DecisionRecovery *decider.Report      `json:"decision_recovery,omitempty"`
	Snapshots        map[string]*Snapshot `json:"snapshots"`
	ChildRootsAtOpen map[string]bool      `json:"child_roots_at_open"`
	// HeadAtOpen and StoredAtOpen are the native head and the stored blocks
	// of the range when the node started, before the recovery.
	HeadAtOpen    uint64        `json:"head_number_at_open"`
	StoredAtOpen  []uint64      `json:"stored_at_open"`
	Decisions     []DecisionRow `json:"decisions"`
	Obligations   []uint64      `json:"obligations"`
	Reimported    int           `json:"reimported"`
	ReimportError string        `json:"reimport_error,omitempty"`
}

// DecisionRow is one decision that the bridge lists after the import.
type DecisionRow struct {
	Sequence uint64   `json:"sequence"`
	State    string   `json:"state"`
	Kind     string   `json:"kind"`
	Members  []string `json:"members"`
	Marker   bool     `json:"marker"`
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
	setHead := flags.Int64("set-head", -1, "before the recovery, ask native SetHead for this head (must be refused)")
	walk := flags.Uint64("walk-check", 0, "at EXEC_READY of this block, check the native snapshot walk from its capsule")
	batch := flags.Bool("batch", false, "import the observed range with one native InsertChain call")
	store := flags.String("store-id", storeID, "producer binding of the native store")
	unobserved := flags.Bool("unobserved", false, "import the range without observations")
	rpcDuring := flags.Uint64("rpc-during", 0, "send bor_getSnapshot of the parent while this block's window is open")
	rpcBeforeBlock := flags.Uint64("rpc-before", 0, "have bor_getSnapshot of the parent inside a Heimdall read when this block's window starts")
	expiredBlock := flags.Uint64("expired-fetch", 0, "start a span range lookup with an expired context just before this block's window")
	finalize := flags.Uint64("finalize", 0, "after the import, mark this block finalized (the native freezer freezes up to it)")
	awaitFrozen := flags.Uint64("await-frozen", 0, "after the start, wait until the native freezer froze this block, then record the pins")
	verifyPins := flags.Bool("verify-pins", false, "record every live pin and its native Verify result")
	replay := flags.Bool("replay", false, "after the import, replay every saved EXEC_READY observation")
	control := flags.Uint64("rpc-control", 0, "finally send bor_getSnapshot of this block with cold caches outside every window")
	conflict := flags.Uint64("milestone-conflict", 0, "after EXEC_READY of this block, whitelist a conflicting milestone at its number")
	reimport := flags.Bool("reimport", false, "after the start, import the range again without skipping stored blocks")
	output := flags.String("out", "", "result JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	result := attemptResult{Args: args, Observations: []Observed{}, RecoveryRows: []obligation.Row{}, Pending: []string{},
		ChildRootsAtOpen: map[string]bool{}, StoredAtOpen: []uint64{}, Decisions: []DecisionRow{}, Obligations: []uint64{},
		Snapshots: map[string]*Snapshot{}}
	crash, err := parseCrash(*crashValue)
	if err != nil {
		return err
	}
	rec := &recorder{result: &result, replay: *replay, walk: *walk}
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
	delay := &delayOnce{}
	var before *rpcBefore
	if *rpcBeforeBlock > 0 {
		result.RPCBefore = &RPCBefore{Block: *rpcBeforeBlock, Target: *rpcBeforeBlock - 1}
		before = &rpcBefore{check: result.RPCBefore, delay: delay, done: make(chan struct{})}
	}
	var expired *expiredFetch
	if *expiredBlock > 0 {
		result.ExpiredFetch = &ExpiredFetch{Block: *expiredBlock}
		expired = &expiredFetch{check: result.ExpiredFetch, delay: delay}
	}
	transport := stubTransport{stub: heimdallstub.Stub{Bodies: *bodies, Variant: *variant}, rpc: inWindow, delay: delay}
	http.DefaultTransport = transport
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
	if before != nil {
		before.engine, before.chain = engine, chain
		rec.before = before
	}
	if expired != nil {
		expired.engine = engine
		rec.expired = expired
	}
	db := &crashDB{Database: eth.ChainDb(), crash: crash}
	rec.db, rec.eth = db, eth
	db.synced = func(key string) {
		// Admission terminal records: keep the observation's rows before a
		// decision or the cleanup deletes them.
		prefix := "extractor-admission-v1/" + producerID + "." + runID + "/"
		parts := strings.Split(strings.TrimPrefix(key, prefix), "/")
		if strings.HasPrefix(key, prefix) && len(parts) == 2 &&
			(parts[1] == "exec-ready" || parts[1] == "rejected" || parts[1] == "abandoned") {
			if err := rec.snapshot(producerID + "." + runID + "." + parts[0]); err != nil {
				panic(fmt.Sprintf("snapshot: %v", err))
			}
		}
	}
	if *conflict > 0 {
		rec.conflict = *conflict
		rec.milestone = eth.Downloader().ChainValidator.ProcessMilestone
	}
	core.ExtractorDecisionStep = func(step string, _ common.Hash) { crash.fire(step) }
	node, err := borimport.Start(cfg, db, chain)
	if err != nil {
		shutdown(stack, eth)
		return err
	}
	var decided *bordecision.Node
	defer func() {
		result.Passed = node.Router.Passed()
		if decided != nil {
			decided.Detach()
		} else {
			node.Detach()
		}
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
	result.HeadAtOpen = chain.CurrentBlock().Number.Uint64()
	for number := *from; number <= *to; number++ {
		if chain.HasBlock(blocks[number-1].Hash(), number) {
			result.StoredAtOpen = append(result.StoredAtOpen, number)
		}
	}
	// RECOVERY_ONLY: with a saved obligation, GoLive and Begin are refused.
	token := node.Driver.Guard.Acquire()
	saved, err := node.Driver.Admission.Observations(token)
	if err == nil {
		for _, obs := range saved {
			result.Pending = append(result.Pending, fmt.Sprintf("%s %s", obs.Key, obs.State))
			if obs.State == admission.ExecReady && obs.Candidate.Number >= 1 && obs.Candidate.Number <= uint64(len(blocks)) {
				// AC-CAP-07-02: is the child state of the saved EXEC_READY block durable?
				root := blocks[obs.Candidate.Number-1].Root()
				result.ChildRootsAtOpen[obs.Key] = rawdb.HasLegacyTrieNode(eth.ChainDb(), root)
			}
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
	if *awaitFrozen > 0 {
		deadline := time.Now().Add(5 * time.Minute)
		for {
			frozen, err := eth.ChainDb().Ancients()
			if err != nil {
				return err
			}
			if frozen > *awaitFrozen {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("the native freezer froze %d blocks, not block %d", frozen, *awaitFrozen)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if *awaitFrozen > 0 || *verifyPins {
		if result.FrozenPins, err = pinStatus(eth.ChainDb()); err != nil {
			return err
		}
	}
	if *setHead >= 0 {
		// Before the recovery, while the saved obligations keep their pins.
		result.SetHead = fmt.Sprint(chain.SetHead(uint64(*setHead)))
	}
	decided, report, err := bordecision.Start(node, nil)
	result.Recovery = &report
	// Every counted call of the recovery windows, in order: the admission
	// recovery, then the replay before each completed native import.
	rec.mu.Lock()
	result.RecoveryRows = append([]obligation.Row{}, rec.window...)
	rec.window = nil
	rec.mu.Unlock()
	if decided != nil {
		result.DecisionRecovery = &decided.Report
	}
	if err != nil {
		return fmt.Errorf("recovery: %w", err)
	}
	if *base > 0 {
		// Blocks below the observed range: a chain the node already had.
		decided.Detach()
		result.BaseInserted, err = chain.InsertChain(blocks[:*base], false)
		http.DefaultTransport = router
		if err != nil {
			return fmt.Errorf("base import: %w", err)
		}
	}
	if *unobserved {
		decided.Detach()
		http.DefaultTransport = router
	} else {
		decided.Attach(rec)
	}
	if *reimport {
		// Blocks already in the chain with their state: the native import
		// skips them, so none opens an observation or a decision.
		result.Reimported, err = chain.InsertChain(blocks[*from-1:*to], false)
		if err != nil {
			result.ReimportError = err.Error()
		}
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
	if before != nil {
		if result.RPCBefore.HeldRequest == "" {
			return fmt.Errorf("block %d was not imported with an RPC call in flight", *rpcBeforeBlock)
		}
		select {
		case <-before.done:
		case <-time.After(2 * time.Minute):
			return errors.New("the RPC call started before the window did not return")
		}
	}
	if *finalize > 0 {
		rawdb.WriteFinalizedBlockHash(eth.ChainDb(), blocks[*finalize-1].Hash())
	}
	head := chain.CurrentBlock()
	result.HeadNumber, result.HeadHash = head.Number.Uint64(), head.Hash().Hex()
	if err = describe(node, db, &result); err != nil {
		return err
	}
	if err = describeDecisions(decided, &result); err != nil {
		return err
	}
	if *walk > 0 && result.Walk == nil {
		return fmt.Errorf("block %d was not observed for the walk check", *walk)
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
	// Waiting is true once a goroutine that runs the bor API GetSnapshot was
	// seen waiting in the window gate (its stack trace), WaitingGoroutine is
	// its stack header.
	Waiting          bool   `json:"waiting_at_gate"`
	WaitingGoroutine string `json:"waiting_goroutine,omitempty"`
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
		if header := gateWaiter("consensus/bor.(*API).GetSnapshot("); header != "" {
			r.check.Waiting, r.check.WaitingGoroutine = true, header
			return
		}
		select {
		case <-r.done:
			return
		case <-time.After(time.Millisecond):
		}
	}
}

// gateWaiter returns the stack header of a goroutine that runs function and
// waits in the window gate (sync.Cond.Wait inside extractorGateState.enter),
// or "" if there is none.
func gateWaiter(function string) string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	for _, trace := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(trace, function) && strings.Contains(trace, "sync.(*Cond).Wait(") &&
			strings.Contains(trace, "consensus/bor.(*extractorGateState).enter(") {
			header, _, _ := strings.Cut(trace, "\n")
			return header
		}
	}
	return ""
}

func (r *rpcInWindow) returned() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// RPCBefore records the bor RPC call that is inside a Heimdall read when
// the window of Block starts: the probe purges the caches, starts the call,
// holds the first counted Heimdall request that follows for a second, and
// begins the import of Block while that request is in flight.
type RPCBefore struct {
	Block          uint64 `json:"block"`
	Target         uint64 `json:"target"`
	HeldRequest    string `json:"held_request"`
	HeldAtObserve  bool   `json:"held_at_observe"`
	Number         uint64 `json:"snapshot_number"`
	Hash           string `json:"snapshot_hash"`
	Error          string `json:"error,omitempty"`
	ReturnedByDone bool   `json:"returned_by_done"`
}

// delayOnce holds the first counted Heimdall request after arm for a second.
type delayOnce struct {
	mu       sync.Mutex
	armed    bool
	request  string
	held     chan struct{}
	inFlight atomic.Bool
}

func (d *delayOnce) arm() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.armed, d.held = true, make(chan struct{})
}

func (d *delayOnce) hold(req *http.Request) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if !d.armed || !obligation.Counted(req.URL) {
		d.mu.Unlock()
		return
	}
	d.armed, d.request = false, req.Method+" "+req.URL.RequestURI()
	held := d.held
	d.inFlight.Store(true)
	d.mu.Unlock()
	close(held)
	time.Sleep(time.Second)
	d.inFlight.Store(false)
}

// rpcBefore starts the RPC call so that it is inside a Heimdall read when
// the window of its block starts.
type rpcBefore struct {
	engine *bor.Bor
	chain  *core.BlockChain
	delay  *delayOnce
	check  *RPCBefore
	done   chan struct{}
}

func (r *rpcBefore) start() error {
	r.engine.ExtractorPurgeInputs()
	r.delay.arm()
	go func() {
		defer close(r.done)
		number, hash, err := rpcCall(r.engine, r.chain, r.check.Target)
		r.check.Number, r.check.Hash = number, hash
		if err != nil {
			r.check.Error = err.Error()
		}
	}()
	select {
	case <-r.delay.held:
	case <-time.After(30 * time.Second):
		return errors.New("no Heimdall request followed the RPC call")
	}
	r.delay.mu.Lock()
	r.check.HeldRequest = r.delay.request
	r.delay.mu.Unlock()
	r.check.HeldAtObserve = r.delay.inFlight.Load()
	return nil
}

// ExpiredFetch records a native span range lookup with an expired context,
// made through the window gate just before the window of Block starts, as
// waitForNewSpan does after its timeout: the lookup returns at once while its
// detached fetch of span 1 is held in flight for a second.
type ExpiredFetch struct {
	Block         uint64 `json:"block"`
	LookupError   string `json:"lookup_error"`
	HeldRequest   string `json:"held_request"`
	HeldAtObserve bool   `json:"held_at_observe"`
}

type expiredFetch struct {
	engine *bor.Bor
	delay  *delayOnce
	check  *ExpiredFetch
}

func (e *expiredFetch) start() error {
	e.engine.ExtractorPurgeInputs()
	e.delay.arm()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.check.LookupError = fmt.Sprint(e.engine.ExtractorFetchSpanRange(ctx, 1))
	select {
	case <-e.delay.held:
	case <-time.After(30 * time.Second):
		return errors.New("the detached span fetch reached no Heimdall request")
	}
	e.delay.mu.Lock()
	e.check.HeldRequest = e.delay.request
	e.delay.mu.Unlock()
	e.check.HeldAtObserve = e.delay.inFlight.Load()
	return nil
}

// FrozenPins records the pins after the native freezer ran: the frozen block
// count and, per pin, its header numbers and the native Verify result.
type FrozenPins struct {
	Ancients uint64      `json:"ancients"`
	Pins     []PinStatus `json:"pins"`
}

// PinStatus is one live pin and whether its native data is readable.
type PinStatus struct {
	ID      string   `json:"id"`
	Headers []uint64 `json:"headers"`
	Error   string   `json:"verify_error,omitempty"`
}

func pinStatus(db ethdb.Database) (*FrozenPins, error) {
	ancients, err := db.Ancients()
	if err != nil {
		return nil, err
	}
	pins, err := stateprovider.Pins(db)
	if err != nil {
		return nil, err
	}
	out := &FrozenPins{Ancients: ancients, Pins: []PinStatus{}}
	for _, pin := range pins {
		row := PinStatus{ID: pin.ID, Headers: []uint64{}}
		for _, ref := range pin.Headers {
			row.Headers = append(row.Headers, ref.Number)
		}
		if err := stateprovider.Verify(db, pin); err != nil {
			row.Error = err.Error()
		}
		out.Pins = append(out.Pins, row)
	}
	return out, nil
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
	node    *borimport.Node
	db      ethdb.Database
	rpc     *rpcInWindow
	before  *rpcBefore
	expired *expiredFetch
	// replay replays each observation from its saved inputs right after
	// EXEC_READY, before its decision closes it.
	replay bool
	// conflict is the block whose EXEC_READY is followed by a conflicting
	// whitelisted milestone (the native rejection fixture of AC-CAP-07-06).
	conflict  uint64
	milestone func(uint64, common.Hash)
	// walk is the block whose native snapshot walk is checked from its
	// capsule at EXEC_READY.
	walk   uint64
	eth    *eth.Ethereum
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
	if r.before != nil && r.before.check.Block == block.NumberU64() {
		if err := r.before.start(); err != nil {
			r.finish(err)
			return nil, err
		}
	}
	if r.expired != nil && r.expired.check.Block == block.NumberU64() {
		if err := r.expired.start(); err != nil {
			r.finish(err)
			return nil, err
		}
	}
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
	if b := o.recorder.before; b != nil && b.check.Block == block.NumberU64() {
		select {
		case <-b.done:
			b.check.ReturnedByDone = true
		default:
		}
	}
	err = o.inner.Done(block, receipts, usedGas, err)
	o.recorder.finish(err)
	key := o.inner.(interface{ Key() string }).Key()
	o.recorder.keyed(key)
	if err != nil {
		return err
	}
	// EXEC_READY is durable and the guard is free; the decision follows.
	if o.recorder.walk == block.NumberU64() {
		walk, walkErr := checkWalk(o.recorder.eth, o.recorder.db, block.NumberU64())
		if walkErr != nil {
			return walkErr
		}
		o.recorder.mu.Lock()
		o.recorder.result.Walk = walk
		o.recorder.mu.Unlock()
	}
	if o.recorder.replay {
		row := ReplayRow{Key: key}
		if replayErr := o.recorder.node.Replay(key); replayErr != nil {
			row.Error = replayErr.Error()
		}
		row.Rows = o.recorder.node.Router.Rows()
		o.recorder.mu.Lock()
		o.recorder.result.Replays = append(o.recorder.result.Replays, row)
		o.recorder.mu.Unlock()
	}
	if o.recorder.conflict == block.NumberU64() {
		o.recorder.milestone(block.NumberU64(), common.Hash{0xee})
	}
	return nil
}

func (o recorded) Fail(err error) error {
	err = o.inner.Fail(err)
	o.recorder.finish(err)
	o.recorder.keyed(o.inner.(interface{ Key() string }).Key())
	return err
}

// keyed sets the observation key of the last observation.
func (r *recorder) keyed(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result.Observations[len(r.result.Observations)-1].Key = key
}

// snapshot records the store rows of observation key and, if it has a
// sealed capsule, its capsule summary and checkpoint-snapshot item.
func (r *recorder) snapshot(key string) error {
	seq := key[strings.LastIndex(key, ".")+1:]
	snap := &Snapshot{Rows: Dump{}}
	for _, prefix := range []string{"extractor-admission-v1/" + producerID + "." + runID + "/" + seq + "/",
		"extractor-source-inputs-v1/" + key + "/", "extractor-state-provider-pin-v1-" + key} {
		rows, err := dumpPrefix(r.db, prefix)
		if err != nil {
			return err
		}
		snap.Rows = append(snap.Rows, rows...)
	}
	if sealed, err := r.db.Has([]byte("extractor-source-inputs-v1/" + key + "/capsule")); err != nil {
		return err
	} else if sealed {
		c, err := borimport.ReadCapsule(r.db, key)
		if err != nil {
			return err
		}
		item, err := sourceinputs.ReadItem(r.db, key, 6)
		if err != nil {
			return err
		}
		snap.Block, snap.CheckpointItem = c.Block.NumberU64(), hex.EncodeToString(item)
		snap.Capsule = &CapsuleSummary{AncestorFirst: c.Ancestors[0].Number.Uint64(), AncestorCount: len(c.Ancestors),
			CheckpointSize: len(c.Checkpoint)}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result.Snapshots[key] = snap
	return nil
}

// describeDecisions lists the decisions and delivery obligations of the bridge.
func describeDecisions(n *bordecision.Node, result *attemptResult) error {
	if n == nil {
		return nil
	}
	token := n.Import.Driver.Guard.Acquire()
	decisions, err := n.Bridge.Decisions(token)
	var obligations []decision.Obligation
	if err == nil {
		obligations, err = n.Bridge.Obligations(token)
	}
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		return err
	}
	for _, d := range decisions {
		row := DecisionRow{Sequence: d.Sequence, State: string(d.State), Kind: string(d.Intent.Kind), Members: []string{},
			Marker: d.Marker}
		for _, m := range d.Intent.Members {
			row.Members = append(row.Members, m.Observation)
		}
		result.Decisions = append(result.Decisions, row)
	}
	for _, o := range obligations {
		result.Obligations = append(result.Obligations, o.Decided.Sequence)
	}
	return nil
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
		if observed.Key == "" {
			observed.Key = list[len(list)-1]
		}
		if observed.Capsule != nil || !slices.Contains(list, observed.Key) {
			continue
		}
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
	for _, prefix := range dumpPrefixes {
		rows, err := dumpPrefix(db, prefix)
		if err != nil {
			return nil, err
		}
		dump = append(dump, rows...)
	}
	return dump, nil
}

// recordKeys are the keys whose values a dump lists: records and the decision keys.
var recordKeys = regexp.MustCompile(`/(counter|lowwater|purged|preparing|admitted|exec-ready|abandoned|rejected|decided|intent|capsule|frontier|closed)$|^extractor-state-provider-|^extractor-admission-run-v1/|^extractor-decision-`)

func dumpPrefix(db ethdb.Iteratee, prefix string) (Dump, error) {
	dump := Dump{}
	it := db.NewIterator([]byte(prefix), nil)
	defer it.Release()
	for it.Next() {
		row := map[string]any{"key": string(it.Key()), "value_sha256": heimdallstub.HexSum(it.Value()),
			"value_length": len(it.Value())}
		if recordKeys.MatchString(string(it.Key())) {
			row["value"] = string(it.Value())
		}
		dump = append(dump, row)
	}
	return dump, it.Error()
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
	RPCBefore  uint64 `json:"rpc_before"`
	Expired    uint64 `json:"expired_fetch"`
	Finalize   uint64 `json:"finalize"`
	Frozen     uint64 `json:"await_frozen"`
	VerifyPins bool   `json:"verify_pins"`
	// ToolArgs are extra arguments of the tool command, separated by spaces.
	ToolArgs   string `json:"tool_args"`
	Replay     bool   `json:"replay"`
	RPCControl uint64 `json:"rpc_control"`
	// MilestoneConflict and Reimport are the decision bridge fixtures.
	MilestoneConflict uint64 `json:"milestone_conflict"`
	Reimport          bool   `json:"reimport"`
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
			row["prune_state"], err = runTool(filepath.Join(*tools, step.PruneTool), step.Command, strings.Fields(step.ToolArgs),
				dataDir, *output, index)
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
				"-rpc-before", fmt.Sprint(step.RPCBefore), "-expired-fetch", fmt.Sprint(step.Expired),
				"-finalize", fmt.Sprint(step.Finalize), "-await-frozen", fmt.Sprint(step.Frozen),
				fmt.Sprintf("-verify-pins=%t", step.VerifyPins),
				fmt.Sprintf("-replay=%t", step.Replay), "-rpc-control", fmt.Sprint(step.RPCControl),
				"-milestone-conflict", fmt.Sprint(step.MilestoneConflict), fmt.Sprintf("-reimport=%t", step.Reimport), "-out", out}
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
func runTool(binary, command string, args []string, dataDir, output string, index int) (map[string]any, error) {
	if command == "" {
		command = "prune-state"
	}
	if command != "prune-state" && command != "prune-block" {
		return nil, fmt.Errorf("unknown tool command %q", command)
	}
	logPath := filepath.Join(output, fmt.Sprintf("attempt-%d.log", index))
	status, err := runChild(binary, append([]string{"snapshot", command, "--datadir", dataDir}, args...), logPath)
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
