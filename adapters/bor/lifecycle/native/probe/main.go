// Command extractor-bor-lifecycle-probe is the measured probe of the Bor block
// of E01-02.lifecycle_seams. It runs native Bor on the private POLY fixture
// (chain 15001, the in-process Heimdall stub of D98 item 6) as a capture node
// started through the capture wiring of `bor server` (wire.Start) and drives
// every mapped Bor caller through its native entry: imports, side branches,
// SetHead, known blocks, SetCanonical with a pruned ancestor, the side-chain
// import, the milestone reorganization, the whitelist, the refused paths, the
// restart, a crash before a lifecycle marker, a native startup repair, the
// manual commands, the `bor server` profile check and the admin_importChain
// caller. Each command writes one JSON result with the gate coverage. See
// adapters/bor/lifecycle/native/README.md.
//
// Extractor fixture (E01-02.lifecycle_seams, Bor block); test-only.
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
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/cmd/extractor-bor-lifecycle-probe/heimdallstub"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/borimport"
	"github.com/ethereum/go-ethereum/extractor/borlifecycle"
	"github.com/ethereum/go-ethereum/extractor/borlifecycle/profile"
	"github.com/ethereum/go-ethereum/extractor/borlifecycle/wire"
	_ "github.com/ethereum/go-ethereum/extractor/borwire"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
	"github.com/ethereum/go-ethereum/internal/cli"
	"github.com/ethereum/go-ethereum/internal/cli/server"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	signerAddress        = "0x71562b71999873db5b286df957af199ec94617f7"
	senderAddress        = "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf"
	producerID           = "bor"
	runID                = "run-1"
	storeID              = "0123456789abcdef0123456789abcdef"
	normalizationProfile = "test-only-probe-v1"
	instanceName         = "bor"
	chainID              = 15001
)

// plan is the generated fixture: name, parent ("" for genesis), the extra
// seconds over the earliest header time, which makes siblings differ, whether
// the block carries a transfer (so its state root differs from its siblings'),
// and a sibling whose hash it must stay below: the native fork choice breaks a
// total-difficulty tie at one height by hash, so the generator raises the
// offset until the block stays a side block when it is imported after it.
var plan = []struct {
	name, parent string
	offset       uint64
	transfer     bool
	below        string
}{
	{"A1", "", 0, false, ""}, {"A2", "A1", 0, false, ""}, {"A3", "A2", 0, false, ""}, {"A4", "A3", 0, false, ""},
	{"A5", "A4", 0, false, ""}, {"B3", "A2", 1, false, ""}, {"B4", "B3", 1, false, ""}, {"C4", "B3", 2, false, "B4"},
	{"D4", "B3", 3, true, ""}, {"E4", "B3", 4, true, "D4"}, {"E5", "E4", 4, false, ""}, {"D5", "D4", 0, true, ""}, {"F4", "B3", 5, false, ""},
	{"G4", "B3", 6, false, ""}, {"H5", "F4", 0, false, ""}, {"H6", "H5", 0, false, ""}, {"H7", "H6", 0, false, ""},
	{"H8", "H7", 0, false, ""}, {"A6", "A5", 0, false, ""},
}

// senderKey is public test key 1; the driver funds it in the genesis.
func senderKey() (*ecdsa.PrivateKey, error) {
	key, err := crypto.HexToECDSA(strings.Repeat("0", 63) + "1")
	if err != nil || crypto.PubkeyToAddress(key.PublicKey) != common.HexToAddress(senderAddress) {
		return nil, errors.New("test key 1 identity mismatch")
	}
	return key, nil
}

func readBounded(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maximum)
	}
	return data, nil
}

func writeNew(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func saveJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeNew(path, append(data, '\n'))
}

func signerKey(source string) (*ecdsa.PrivateKey, error) {
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

type inputs struct {
	genesisPath, configPath string
	genesis                 *core.Genesis
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
	if config.Bor == nil || config.ChainID.Cmp(big.NewInt(chainID)) != 0 {
		return inputs{}, errors.New("expected the private Bor genesis 15001")
	}
	genesis.Config = &config
	return inputs{genesisPath: genesisPath, configPath: configPath, genesis: &genesis}, nil
}

// stubTransport answers every Heimdall request from the stub files.
type stubTransport struct{ stub heimdallstub.Stub }

func (s stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body.Close()
	}
	if req.URL.Host != heimdallstub.Host {
		return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
	}
	body, status := s.stub.Serve(req.URL)
	return &http.Response{Status: http.StatusText(status), StatusCode: status, Proto: "HTTP/1.1", ProtoMajor: 1,
		ProtoMinor: 1, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)), Request: req}, nil
}

func newStack(dataDir string) (*node.Node, error) {
	return node.New(&node.Config{Name: instanceName, DataDir: dataDir,
		P2P: p2p.Config{NoDiscovery: true, MaxPeers: 0, ListenAddr: "127.0.0.1:0"}})
}

// backend starts native Bor in the sequential capture profile (hash scheme,
// full sync, no parallel, pipelined, stateless or preconf paths), Heimdall
// behind the process transport.
func backend(stack *node.Node, genesis *core.Genesis) (*eth.Ethereum, error) {
	cfg := ethconfig.Defaults
	cfg.Genesis, cfg.NetworkId, cfg.SyncMode = genesis, chainID, downloader.FullSync
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

func shutdown(stack *node.Node, backend *eth.Ethereum) {
	if backend != nil {
		backend.Engine().Close()
		backend.TxPool().Close()
		backend.BlockChain().Stop()
	}
	stack.Close()
}

// ---------------------------------------------------------------------------
// Generation (not captured).

// Generated is one block of the fixture.
type Generated struct {
	Name   string `json:"name"`
	Number uint64 `json:"number"`
	Hash   string `json:"hash"`
	Parent string `json:"parent"`
	Root   string `json:"state_root"`
	Time   uint64 `json:"timestamp"`
	// Transactions is the number of body transactions.
	Transactions int `json:"transactions"`
}

func generate(args []string) error {
	flags := flag.NewFlagSet("generate", flag.ContinueOnError)
	genesisPath := flags.String("genesis", "", "canonical genesis JSON")
	configPath := flags.String("config", "", "canonical chain-config JSON")
	keySource := flags.String("keysource", "", "tests/bor/helper.go of the accepted source")
	bodies := flags.String("bodies", "", "stub response directory")
	output := flags.String("out", "", "new output directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	in, err := loadInputs(*genesisPath, *configPath)
	if err != nil {
		return err
	}
	key, err := signerKey(*keySource)
	if err != nil {
		return err
	}
	if err = os.Mkdir(*output, 0o700); err != nil {
		return err
	}
	http.DefaultTransport = stubTransport{stub: heimdallstub.Stub{Bodies: *bodies, Variant: "corpus"}}
	stack, err := newStack(filepath.Join(*output, "generation-data"))
	if err != nil {
		return err
	}
	backend, err := backend(stack, in.genesis)
	if err != nil {
		stack.Close()
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
	config := in.genesis.Config
	sender, err := senderKey()
	if err != nil {
		return err
	}
	byName := map[string]*types.Block{"": chain.GetBlockByNumber(0)}
	var encoded bytes.Buffer
	rows := []Generated{}
	for _, p := range plan {
		var block *types.Block
		for offset := p.offset; ; offset++ {
			if offset > p.offset+64 {
				return fmt.Errorf("%s: no offset keeps it below %s", p.name, p.below)
			}
			if block, err = buildBlock(engine, chain, config, author, sender, byName[p.parent], offset, p.transfer,
				p.name); err != nil {
				return err
			}
			if p.below == "" || bytes.Compare(block.Hash().Bytes(), byName[p.below].Hash().Bytes()) < 0 {
				break
			}
		}
		if _, err = chain.InsertChain([]*types.Block{block}, false); err != nil {
			return fmt.Errorf("import generated %s: %w", p.name, err)
		}
		if !chain.HasBlockAndState(block.Hash(), block.NumberU64()) {
			return fmt.Errorf("generated %s is not stored with its state", p.name)
		}
		byName[p.name] = block
		if err = rlp.Encode(&encoded, block); err != nil {
			return err
		}
		rows = append(rows, Generated{Name: p.name, Number: block.NumberU64(), Hash: hexOf(block.Hash()), Parent: p.parent,
			Root: hexOf(block.Root()), Time: block.Time(), Transactions: len(block.Transactions())})
	}
	if err = writeNew(filepath.Join(*output, "blocks.rlp"), encoded.Bytes()); err != nil {
		return err
	}
	return saveJSON(filepath.Join(*output, "generated.json"), map[string]any{
		"genesis_hash": hexOf(chain.GetBlockByNumber(0).Hash()), "blocks": rows})
}

// buildBlock seals one block on parentBlock with native Prepare,
// FinalizeAndAssemble and Seal (Bor's miner is not used). A transfer block
// applies one transfer of the sender with native core.ApplyTransaction.
func buildBlock(engine *bor.Bor, chain *core.BlockChain, config *params.ChainConfig, author common.Address,
	sender *ecdsa.PrivateKey, parentBlock *types.Block, offset uint64, transfer bool, name string) (*types.Block, error) {
	number := parentBlock.NumberU64() + 1
	header := &types.Header{ParentHash: parentBlock.Hash(), Number: new(big.Int).SetUint64(number),
		GasLimit: parentBlock.GasLimit(), Time: parentBlock.Time() + 1, Difficulty: big.NewInt(1)}
	if config.IsLondon(header.Number) {
		header.BaseFee = eip1559.CalcBaseFee(config, parentBlock.Header())
		if !config.IsLondon(parentBlock.Number()) {
			limit := parentBlock.GasLimit() * config.ElasticityMultiplier()
			header.GasLimit = core.CalcGasLimit(limit, limit)
		}
	}
	if err := engine.Prepare(chain, header, false); err != nil {
		return nil, fmt.Errorf("prepare %s: %w", name, err)
	}
	// The earliest time native verification accepts, plus the offset, keeps
	// every header in the past, so the chain is deterministic.
	header.Time = parentBlock.Time() + bor.CalcProducerDelay(number, 0, config.Bor) + offset
	statedb, err := chain.StateAt(parentBlock.Root())
	if err != nil {
		return nil, err
	}
	evm := vm.NewEVM(core.NewEVMBlockContext(header, chain, &author), statedb, config, vm.Config{})
	if config.IsPrague(header.Number) || config.IsVerkle(header.Number) {
		core.ProcessParentBlockHash(header.ParentHash, evm)
	}
	body := &types.Body{}
	var receipts []*types.Receipt
	if transfer {
		to := common.BytesToAddress(crypto.Keccak256([]byte(name))[:20])
		tx, err := types.SignNewTx(sender, types.LatestSigner(config), &types.DynamicFeeTx{ChainID: big.NewInt(chainID),
			Nonce: statedb.GetNonce(common.HexToAddress(senderAddress)), GasTipCap: big.NewInt(1_000_000_000),
			GasFeeCap: big.NewInt(100_000_000_000), Gas: 21_000, To: &to, Value: big.NewInt(1)})
		if err != nil {
			return nil, err
		}
		pool := new(core.GasPool).AddGas(header.GasLimit)
		used := uint64(0)
		statedb.SetTxContext(tx.Hash(), 0)
		receipt, err := core.ApplyTransaction(evm, pool, statedb, header, tx, &used)
		if err != nil {
			return nil, fmt.Errorf("apply the transfer of %s: %w", name, err)
		}
		header.GasUsed, body.Transactions, receipts = used, types.Transactions{tx}, []*types.Receipt{receipt}
	}
	block, _, _, err := engine.FinalizeAndAssemble(chain, header, statedb, body, receipts)
	if err != nil {
		return nil, fmt.Errorf("finalize %s: %w", name, err)
	}
	sealed := make(chan *consensus.NewSealedBlockEvent, 1)
	if err = engine.Seal(chain, block, nil, sealed, context.Background().Done()); err != nil {
		return nil, fmt.Errorf("seal %s: %w", name, err)
	}
	event := <-sealed
	if event == nil || event.Block == nil {
		return nil, errors.New("missing sealed block")
	}
	return event.Block, nil
}

func hexOf(hash common.Hash) string { return hex.EncodeToString(hash[:]) }

// readBlocks returns the generated blocks by name, in plan order.
func readBlocks(path string) (map[string]*types.Block, error) {
	data, err := readBounded(path, 64*1024*1024)
	if err != nil {
		return nil, err
	}
	stream := rlp.NewStream(bytes.NewReader(data), uint64(len(data)))
	out := map[string]*types.Block{}
	for _, p := range plan {
		block := new(types.Block)
		if err := stream.Decode(block); err != nil {
			return nil, fmt.Errorf("block %s: %w", p.name, err)
		}
		out[p.name] = block
	}
	if err := stream.Decode(new(types.Block)); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected trailing block")
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// The capture node.

type capture struct {
	stack    *node.Node
	eth      *eth.Ethereum
	chain    *core.BlockChain
	run      *wire.Run
	blocks   map[string]*types.Block
	result   map[string]any
	steps    []Step
	stopped  bool
	stepHook func(string, string)
}

// Step is one scenario step: what ran, the native head after it, and the
// error, if any.
type Step struct {
	Name     string `json:"name"`
	Head     string `json:"head"`
	HeadName string `json:"head_name"`
	Number   uint64 `json:"head_number"`
	Error    string `json:"error"`
}

type common_ struct {
	genesis, config, bodies, blocks, datadir, commit, out *string
}

func commonFlags(flags *flag.FlagSet) common_ {
	return common_{
		genesis: flags.String("genesis", "", "canonical genesis JSON"),
		config:  flags.String("config", "", "canonical chain-config JSON"),
		bodies:  flags.String("bodies", "", "stub response directory"),
		blocks:  flags.String("blocks", "", "generated blocks.rlp"),
		datadir: flags.String("datadir", "", "native data directory"),
		commit:  flags.String("commit", "", "native source commit"),
		out:     flags.String("out", "", "new result file"),
	}
}

func (c common_) capture() wire.Capture {
	return wire.Capture{StoreID: storeID, Producer: producerID, Run: runID, Genesis: *c.genesis,
		ChainConfig: *c.config, Commit: *c.commit, Normalization: normalizationProfile, HeimdallHost: heimdallstub.Host}
}

// start opens the capture node through the capture wiring of `bor server`:
// the profile-checked native backend, then wire.Start (admission, lifecycle
// gate in the decision recovery driver, live observer).
func start(c common_, step func(string, string), stay bool) (*capture, error) {
	in, err := loadInputs(*c.genesis, *c.config)
	if err != nil {
		return nil, err
	}
	var blocks map[string]*types.Block
	if *c.blocks != "" {
		if blocks, err = readBlocks(*c.blocks); err != nil {
			return nil, err
		}
	}
	stack, err := newStack(*c.datadir)
	if err != nil {
		return nil, err
	}
	cfg, err := c.capture().Config(stack.ResolvePath(""))
	if err != nil {
		stack.Close()
		return nil, err
	}
	if err = borimport.Preflight(cfg, in.genesis.Config.ChainID.String(), in.genesis.ToBlock().Hash()); err != nil {
		stack.Close()
		return nil, err
	}
	http.DefaultTransport = stubTransport{stub: heimdallstub.Stub{Bodies: *c.bodies, Variant: "corpus"}}
	backend, err := backend(stack, in.genesis)
	if err != nil {
		stack.Close()
		return nil, err
	}
	result := map[string]any{"native_head_at_start": headRow(backend.BlockChain().CurrentBlock(), blocks)}
	run, err := wire.Start(stack, backend, c.capture())
	if err != nil {
		shutdown(stack, backend)
		return nil, fmt.Errorf("capture start: %w", err)
	}
	result["bootstrap_at_start"] = run.Bootstrapping()
	node := &capture{stack: stack, eth: backend, chain: backend.BlockChain(), run: run, blocks: blocks, result: result,
		stepHook: step}
	if run.Bootstrapping() {
		result["phase_after_start"] = "BOOTSTRAP"
		if !stay {
			if err := node.goLive(); err != nil {
				node.close()
				return nil, err
			}
		}
		return node, nil
	}
	node.live()
	return node, nil
}

// goLive ends BOOTSTRAP (wire.Run.GoLive, the call of the bor server
// watcher) and records the head it set as H.
func (c *capture) goLive() error {
	head := headRow(c.chain.CurrentBlock(), c.blocks)
	if err := c.run.GoLive(); err != nil {
		return err
	}
	c.result["head_at_golive"] = head
	c.live()
	return nil
}

func (c *capture) live() {
	c.run.Lifecycle.Step = c.stepHook
	c.result["reconcile"] = c.run.Lifecycle.Report()
	c.result["decision_recovery"] = c.run.Decided.Report
	c.result["phase_after_start"] = string(c.run.Lifecycle.Phase())
}

func headRow(h *types.Header, blocks map[string]*types.Block) map[string]any {
	return map[string]any{"number": h.Number.Uint64(), "hash": hexOf(h.Hash()), "name": nameOf(h.Hash(), blocks)}
}

func nameOf(hash common.Hash, blocks map[string]*types.Block) string {
	for name, b := range blocks {
		if b.Hash() == hash {
			return name
		}
	}
	if hash == (common.Hash{}) {
		return ""
	}
	return "?"
}

func (c *capture) step(name string, err error) {
	h := c.chain.CurrentBlock()
	row := Step{Name: name, Head: hexOf(h.Hash()), HeadName: nameOf(h.Hash(), c.blocks), Number: h.Number.Uint64()}
	if err != nil {
		row.Error = err.Error()
	}
	c.steps = append(c.steps, row)
}

// DecisionRow is one decision the bridge lists.
type DecisionRow struct {
	Sequence uint64   `json:"sequence"`
	State    string   `json:"state"`
	Kind     string   `json:"kind"`
	OldHead  string   `json:"old_head"`
	NewHead  string   `json:"new_head"`
	Members  []string `json:"members"`
	Marker   bool     `json:"marker"`
}

func (c *capture) decisions() ([]DecisionRow, error) {
	if c.run.Decided.Bridge == nil {
		// BOOTSTRAP: the bridge is not open; nothing is decided.
		return []DecisionRow{}, nil
	}
	token := c.run.Import.Driver.Guard.Acquire()
	defer token.Release()
	return decisionRows(c.run.Decided.Bridge, token, c.blocks)
}

func decisionRows(b *decision.Bridge, token *admission.Token, blocks map[string]*types.Block) ([]DecisionRow, error) {
	list, err := b.Decisions(token)
	if err != nil {
		return nil, err
	}
	rows := []DecisionRow{}
	for _, d := range list {
		row := DecisionRow{Sequence: d.Sequence, State: string(d.State), Kind: string(d.Intent.Kind),
			OldHead: nameOf(common.HexToHash(d.Intent.OldHead.Hash), blocks),
			NewHead: nameOf(common.HexToHash(d.Intent.NewHead.Hash), blocks), Members: []string{}, Marker: d.Marker}
		for _, m := range d.Intent.Members {
			row.Members = append(row.Members, nameOf(common.HexToHash(m.Block), blocks))
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// finish writes the result with the gate coverage, then stops the node.
func (c *capture) finish(out string, failure error) error {
	rows, derr := c.decisions()
	c.result["steps"] = c.steps
	c.result["decisions"] = rows
	c.result["coverage"] = map[string]lifecycle.Reach{}
	if c.run.Lifecycle != nil {
		c.result["coverage"] = c.run.Lifecycle.Coverage()
		c.result["boundary"], _ = gateBoundary(c.run.Lifecycle)
	}
	c.result["native_head"] = headRow(c.chain.CurrentBlock(), c.blocks)
	c.result["evidence_records"] = c.evidenceRecords()
	if failure != nil {
		c.result["error"] = failure.Error()
	}
	if derr != nil {
		c.result["decisions_error"] = derr.Error()
	}
	c.close()
	return errors.Join(saveJSON(out, c.result), failure)
}

// evidenceRecords counts the lifecycle evidence records left in the store.
func (c *capture) evidenceRecords() int {
	it := c.eth.ChainDb().NewIterator([]byte("extractor-lifecycle-bor-v1/"), nil)
	defer it.Release()
	count := 0
	for it.Next() {
		count++
	}
	return count
}

func gateBoundary(n *borlifecycle.Node) (any, bool) {
	r := n.Report()
	if r.Boundary == nil {
		return nil, false
	}
	return r.Boundary, true
}

func (c *capture) close() {
	if c.stopped {
		return
	}
	c.stopped = true
	c.run.Stop()
	shutdown(c.stack, c.eth)
}

func (c *capture) insert(name string, names ...string) {
	chain := make([]*types.Block, 0, len(names))
	for _, n := range names {
		chain = append(chain, c.blocks[n])
	}
	_, err := c.chain.InsertChain(chain, false)
	c.step(name, err)
}

// writeStateless stores a generated block with its total difficulty but
// without receipts or state, as a block body downloaded without execution.
func (c *capture) writeStateless(name string) error {
	block := c.blocks[name]
	ptd := c.chain.GetTd(block.ParentHash(), block.NumberU64()-1)
	if ptd == nil {
		return fmt.Errorf("%s: parent total difficulty missing", name)
	}
	db := c.eth.ChainDb()
	batch := db.NewBatch()
	rawdb.WriteTd(batch, block.Hash(), block.NumberU64(), new(big.Int).Add(ptd, block.Difficulty()))
	rawdb.WriteBlock(batch, block)
	if err := batch.Write(); err != nil {
		return err
	}
	if c.chain.HasState(block.Root()) {
		return fmt.Errorf("%s has state", name)
	}
	return db.SyncKeyValue()
}

// ---------------------------------------------------------------------------
// Commands on the capture node.

// scenario is the first start: BOOTSTRAP, GoLive at the genesis, then every
// live caller through its native entry.
func scenario(args []string) error {
	flags := flag.NewFlagSet("scenario", flag.ContinueOnError)
	c := commonFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	node, err := start(c, nil, false)
	if err != nil {
		return err
	}
	b := node.blocks
	node.insert("import-A1-A5", "A1", "A2", "A3", "A4", "A5")
	rows, err := node.decisions()
	node.result["after_import"] = map[string]any{"decisions": rows, "head": headRow(node.chain.CurrentBlock(), b),
		"error": errString(err)}
	node.insert("side-B3", "B3")
	node.insert("side-B4", "B4")
	_, err = node.chain.SetCanonical(b["A2"])
	node.step("set-canonical-A2", err)
	// A3 is stored with its state, but its IMPORT observation was closed:
	// the known-branch corpus rule refuses its activation before any intent.
	err = node.chain.ExtractorWriteKnownBlock(b["A3"])
	if errors.Is(err, borlifecycle.ErrCorpus) {
		err = nil
	} else {
		err = fmt.Errorf("not refused by the corpus rule: %v", err)
	}
	node.step("known-A3-refused", err)
	node.step("known-B3", node.chain.ExtractorWriteKnownBlock(b["B3"]))
	node.step("known-B4", node.chain.ExtractorWriteKnownBlock(b["B4"]))
	node.insert("side-C4", "C4")
	_, err = node.chain.SetCanonical(b["C4"])
	node.step("set-canonical-C4", err)
	err = node.writeStateless("D4")
	if err == nil {
		_, err = node.chain.SetCanonical(b["D4"])
	}
	node.step("set-canonical-stateless-D4", err)
	node.step("stateless-E4", node.writeStateless("E4"))
	node.insert("side-chain-E5", "E5")
	node.insert("side-F4", "F4")
	node.insert("side-G4", "G4")
	moved := eth.ExtractorReorgToFinalized(node.eth, node.chain.CurrentBlock().Number.Uint64(), 3,
		[]*types.Block{b["F4"]})
	node.step("milestone-reorg-F4", boolErr(moved, "the milestone reorganization moved nothing"))
	node.whitelist(b["F4"])
	node.refusals(b["H5"])
	node.deadlock(b)
	node.step("set-head-7", node.chain.SetHead(7))
	return node.finish(*c.out, nil)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func boolErr(ok bool, message string) error {
	if ok {
		return nil
	}
	return errors.New(message)
}

// whitelist drives the whitelist callers through the downloader's whitelist
// service, as the milestone and checkpoint handlers and the vote API do.
func (c *capture) whitelist(final *types.Block) {
	d := c.eth.Downloader()
	d.ProcessCheckpoint(final.NumberU64(), final.Hash())
	c.step("checkpoint-F4", nil)
	d.ProcessMilestone(final.NumberU64(), final.Hash())
	c.step("milestone-F4", nil)
	d.ProcessFutureMilestone(16, common.HexToHash(strings.Repeat("16", 32)))
	c.step("future-milestone-16", nil)
	locked := d.LockMutex(12)
	d.UnlockMutex(locked, "lifecycle-vote-1", 12, common.HexToHash(strings.Repeat("12", 32)))
	c.step("milestone-vote-12", boolErr(locked, "the vote lock was refused"))
	d.RemoveMilestoneID("lifecycle-vote-1")
	c.step("milestone-remove-vote", nil)
	d.PurgeMilestonesAfter(3)
	c.step("milestone-purge-after-3", nil)
}

// refusals calls the refused native entries; each must return the refusal.
func (c *capture) refusals(block *types.Block) {
	refused := func(name string, err error) {
		if errors.Is(err, core.ErrExtractorRefused) {
			c.step(name, nil)
			return
		}
		c.step(name, fmt.Errorf("not refused: %v", err))
	}
	_, err := c.chain.InsertBlockWithoutSetHead(block, false)
	refused("refuse-engine-api", err)
	_, err = c.chain.InsertReceiptChain(types.Blocks{block}, nil, 0)
	refused("refuse-snap-sync", err)
	statedb, serr := c.chain.StateAt(c.chain.CurrentBlock().Root)
	if serr != nil {
		refused("refuse-seal-write", serr)
	} else {
		_, err = c.chain.WriteBlockAndSetHead(block, nil, nil, statedb, false)
		refused("refuse-seal-write", err)
	}
	_, err = c.chain.InsertChainStateless(types.Blocks{block}, nil)
	refused("refuse-stateless", err)
}

// deadlock runs the import path (chain lock, guard, whitelist read lock) and
// the whitelist callers (guard, whitelist write lock) concurrently; a lock
// order cycle would stop both and the step fails at its deadline.
func (c *capture) deadlock(b map[string]*types.Block) {
	d := c.eth.Downloader()
	done := make(chan error, 2)
	go func() {
		var err error
		for _, name := range []string{"H5", "H6", "H7", "H8"} {
			if _, ierr := c.chain.InsertChain([]*types.Block{b[name]}, false); ierr != nil {
				err = errors.Join(err, fmt.Errorf("%s: %w", name, ierr))
			}
		}
		done <- err
	}()
	go func() {
		for i := 0; i < 8; i++ {
			d.ProcessMilestone(b["F4"].NumberU64(), b["F4"].Hash())
			locked := d.LockMutex(20)
			d.UnlockMutex(false, "", 20, common.Hash{})
			_ = locked
		}
		done <- nil
	}()
	var err error
	deadline := time.After(120 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case e := <-done:
			err = errors.Join(err, e)
		case <-deadline:
			c.step("deadlock-check", errors.New("deadline: the import and whitelist callers did not finish"))
			c.result["deadlock"] = "TIMEOUT"
			saveJSON(*flagOut, c.result) //nolint:errcheck
			os.Exit(4)
		}
	}
	c.step("deadlock-check", err)
}

var flagOut = new(string)

// crashStore is the store of the restarted node; a "write" crash point syncs
// it before the process exits.
var crashStore interface{ SyncKeyValue() error }

// restart reopens the node: RECOVERY, the startup reconcile, GoLive with the
// stored H.
func restart(args []string) error {
	flags := flag.NewFlagSet("restart", flag.ContinueOnError)
	c := commonFlags(flags)
	crash := flags.String("crash", "", "lifecycle caller whose crash step exits the process (crash point)")
	crashStep := flags.String("crash-step", "marker", "lifecycle step of the crash point: write, marker or resolve")
	crashCount := flags.Int("crash-count", 1, "exit at this occurrence of the crash step")
	crashImport := flags.String("crash-import", "", "block whose import exits after its synced marker (crash point)")
	imports := flags.String("import", "", "comma-separated blocks to import after the start")
	setHead := flags.Uint64("sethead", 0, "SetHead after the start (0: none)")
	refuse := flags.String("refuse-sethead", "", "comma-separated SetHead targets that must be refused")
	bootstrap := flags.Bool("bootstrap", false, "on a store in BOOTSTRAP, import natively before GoLive")
	crashBootstrap := flags.Bool("crash-bootstrap", false, "exit after the BOOTSTRAP imports (crash point)")
	goLive := flags.Bool("golive", false, "end BOOTSTRAP (GoLive) after the imports")
	importLive := flags.String("import-live", "", "comma-separated blocks to import after GoLive")
	whitelistDuring := flags.String("whitelist-during-golive", "", "block whose milestone the whitelist writes while GoLive runs")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var step func(string, string)
	if *crash != "" {
		seen := 0
		step = func(name, caller string) {
			if name == *crashStep && caller == *crash {
				if seen++; seen < *crashCount {
					return
				}
				if name == "write" && crashStore != nil {
					// The native batches committed before this write reached
					// the disk; the write and everything after it did not.
					if err := crashStore.SyncKeyValue(); err != nil {
						fmt.Fprintln(os.Stderr, "crash point: sync failed:", err)
					}
				}
				fmt.Fprintf(os.Stderr, "crash point: %s of %s\n", name, caller)
				os.Exit(3)
			}
		}
	}
	node, err := start(c, step, *bootstrap)
	if err != nil {
		return err
	}
	crashStore = node.eth.ChainDb()
	if *crashImport != "" {
		target := node.blocks[*crashImport].Hash()
		core.ExtractorDecisionStep = func(name string, block common.Hash) {
			if name == "marker" && block == target {
				// The head batch with the marker is synced; Resolve is not.
				fmt.Fprintf(os.Stderr, "crash point: import marker of %s\n", *crashImport)
				os.Exit(3)
			}
		}
	}
	if *imports != "" {
		for _, name := range strings.Split(*imports, ",") {
			node.insert("import-"+name, name)
		}
	}
	if *crashBootstrap {
		// The native imports of BOOTSTRAP are written; nothing was captured.
		fmt.Fprintln(os.Stderr, "crash point: during BOOTSTRAP at", headRow(node.chain.CurrentBlock(), node.blocks)["name"])
		os.Exit(3)
	}
	if *goLive {
		if *whitelistDuring != "" {
			// The whitelist callers in BOOTSTRAP enter the open gate with
			// non-observing passes and take the guard: one milestone call
			// first, then a vote that holds the guard (LockMutex) while
			// GoLive starts. GoLive must wait for UnlockMutex.
			block := node.blocks[*whitelistDuring]
			d := node.eth.Downloader()
			d.ProcessMilestone(block.NumberU64(), block.Hash())
			node.result["coverage_before_golive"] = borlifecycle.Of(node.chain).Coverage()
			d.LockMutex(block.NumberU64() + 10)
			done := make(chan error, 1)
			go func() { done <- node.goLive() }()
			time.Sleep(2 * time.Second)
			select {
			case err := <-done:
				node.result["golive_waited_for_vote"] = false
				d.UnlockMutex(false, "", block.NumberU64()+10, common.Hash{})
				node.step("golive", err)
			default:
				node.result["golive_waited_for_vote"] = true
				d.UnlockMutex(false, "", block.NumberU64()+10, common.Hash{})
				node.step("golive", <-done)
			}
		} else {
			node.step("golive", node.goLive())
		}
	}
	if *importLive != "" {
		for _, name := range strings.Split(*importLive, ",") {
			node.insert("import-"+name, name)
		}
	}
	if *setHead != 0 {
		node.step(fmt.Sprintf("set-head-%d", *setHead), node.chain.SetHead(*setHead))
	}
	if *refuse != "" {
		for _, value := range strings.Split(*refuse, ",") {
			var target uint64
			fmt.Sscan(value, &target)
			err := node.chain.SetHead(target)
			if errors.Is(err, core.ErrExtractorRefused) {
				err = nil
			} else {
				err = fmt.Errorf("not refused: %v", err)
			}
			node.step("refuse-set-head-"+value, err)
		}
	}
	return node.finish(*c.out, nil)
}

// fault deletes the state root node of the native head of a stopped store,
// as a power loss that kept the head block but lost its unsynced state.
func fault(args []string) error {
	flags := flag.NewFlagSet("fault", flag.ContinueOnError)
	datadir := flags.String("datadir", "", "native data directory")
	out := flags.String("out", "", "new result file")
	blocksPath := flags.String("blocks", "", "generated blocks.rlp (with -block)")
	name := flags.String("block", "", "generated block whose state root to delete (default: the native head)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	kv, err := pebble.New(filepath.Join(*datadir, instanceName, "chaindata"), 16, 16, "", false)
	if err != nil {
		return err
	}
	db := rawdb.NewDatabase(kv)
	defer db.Close()
	hash := rawdb.ReadHeadBlockHash(db)
	if *name != "" {
		blocks, err := readBlocks(*blocksPath)
		if err != nil {
			return err
		}
		hash = blocks[*name].Hash()
	}
	number, ok := rawdb.ReadHeaderNumber(db, hash)
	if !ok {
		return errors.New("no native head")
	}
	header := rawdb.ReadHeader(db, hash, number)
	if header == nil || !rawdb.HasLegacyTrieNode(db, header.Root) {
		return errors.New("the native head has no state root node")
	}
	rawdb.DeleteLegacyTrieNode(db, header.Root)
	if err := db.SyncKeyValue(); err != nil {
		return err
	}
	return saveJSON(*out, map[string]any{"head": hexOf(hash), "number": number, "deleted_root": hexOf(header.Root),
		"block": *name})
}

// nativeCaller imports A1..A5 into a new capture instance through the native
// admin_importChain caller (eth.AdminAPI.ImportChain), the path of a node
// operator, for AC-CAP-01-01.
func nativeCaller(args []string) error {
	flags := flag.NewFlagSet("native-caller", flag.ContinueOnError)
	c := commonFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	node, err := start(c, nil, false)
	if err != nil {
		return err
	}
	var encoded bytes.Buffer
	for _, name := range []string{"A1", "A2", "A3", "A4", "A5"} {
		if err := rlp.Encode(&encoded, node.blocks[name]); err != nil {
			return errors.Join(err, node.finish(*c.out, err))
		}
	}
	file := filepath.Join(filepath.Dir(*c.out), "native-caller-A1-A5.rlp")
	if err := writeNew(file, encoded.Bytes()); err != nil {
		return errors.Join(err, node.finish(*c.out, err))
	}
	ok, err := eth.NewAdminAPI(node.eth).ImportChain(file)
	if err == nil && !ok {
		err = errors.New("admin_importChain returned false")
	}
	node.step("admin-import-A1-A5", err)
	rows, derr := node.decisions()
	node.result["after_import"] = map[string]any{"decisions": rows, "head": headRow(node.chain.CurrentBlock(), node.blocks),
		"error": errString(derr)}
	return node.finish(*c.out, nil)
}

// ---------------------------------------------------------------------------
// Refusals outside a running node.

// manualCommands are the native manual commands run against a stopped
// capture store, each with the REFUSE caller it exercises.
var manualCommands = []struct {
	caller string
	args   []string
}{
	{"bor.manual-commands", []string{"removedb"}},
	{"bor.prune-commands", []string{"snapshot", "prune-state"}},
	{"bor.manual-commands", []string{"purge-whitelisted-entries"}},
	{"bor.manual-commands", []string{"backfill-statesync-txs"}},
}

// manual runs each native manual command against a stopped capture store in a
// process of its own (the native commands leave their node, and its datadir
// lock, open), and records the exit code and the end of its output.
func manual(args []string) error {
	flags := flag.NewFlagSet("manual", flag.ContinueOnError)
	datadir := flags.String("datadir", "", "native data directory")
	out := flags.String("out", "", "new result file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	rows := []map[string]any{}
	for i, cmd := range manualCommands {
		child := exec.Command(os.Args[0], "manual-one", "-datadir", *datadir, "-index", fmt.Sprint(i))
		output, err := child.CombinedOutput()
		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			return err
		}
		if len(output) > 2048 {
			output = output[len(output)-2048:]
		}
		rows = append(rows, map[string]any{"caller": cmd.caller, "command": strings.Join(cmd.args, " "), "exit": code,
			"refused": code != 0, "output": string(output)})
	}
	chaindata := filepath.Join(*datadir, instanceName, "chaindata")
	_, statErr := os.Stat(chaindata)
	return saveJSON(*out, map[string]any{"commands": rows, "chaindata_kept": statErr == nil})
}

// manualOne runs one native manual command through the native CLI and exits
// with its code.
func manualOne(args []string) error {
	flags := flag.NewFlagSet("manual-one", flag.ContinueOnError)
	datadir := flags.String("datadir", "", "native data directory")
	index := flags.Int("index", -1, "index of the manual command")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *index < 0 || *index >= len(manualCommands) {
		return errors.New("unknown manual command")
	}
	// The native commands name the instance directory after the program.
	os.Args[0] = instanceName
	os.Exit(cli.Run(append(append([]string{}, manualCommands[*index].args...), "--datadir", *datadir)))
	return nil
}

// profileCheck runs the capture profile check of `bor server` on argument
// sets with the native flag precedence, and starts NewServer with a refused
// flag and --extractor.capture: it must stop after buildEth, before eth.New
// opens the store.
func profileCheck(args []string) error {
	flags := flag.NewFlagSet("profile", flag.ContinueOnError)
	datadir := flags.String("datadir", "", "new directory for the server attempt")
	captureFile := flags.String("capture", "", "capture configuration file")
	captureStore := flags.String("capture-store", "", "stopped capture store for the plain bor server check")
	out := flags.String("out", "", "new result file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	base := []string{"--syncmode", "full", "--gcmode", "full", "--state.scheme", "hash", "--parallelevm.enable=false",
		"--parallelevm.enforce=false"}
	sets := []struct {
		name string
		args []string
	}{
		{"capture-profile", nil},
		{"snap-sync", []string{"--syncmode", "snap"}},
		{"parallel-evm", []string{"--parallelevm.enable=true"}},
		{"pipelined-import", []string{"--pipeline.enable-import-src"}},
		{"stateless-import", []string{"--witness.parallelstatelessimport"}},
		{"witness-sync", []string{"--witness.syncwithwitnesses"}},
		{"preconfs", []string{"--relay.enable-preconfs"}},
		{"accept-preconf-tx", []string{"--accept-preconf-tx"}},
		{"sequencer", []string{"--sequencer.enabled"}},
		{"mining", []string{"--mine"}},
		{"dev", []string{"--dev"}},
		{"dev-fake-author", []string{"--bor.devfakeauthor"}},
	}
	rows := []map[string]any{}
	for _, s := range sets {
		row := map[string]any{"name": s.name, "args": s.args}
		cfg, err := server.ExtractorEffectiveConfig(append(append([]string{}, base...), s.args...))
		if err != nil {
			row["config_error"] = err.Error()
		} else {
			refusals, err := profile.Check(cfg)
			row["refusals"], row["error"] = refusals, errString(err)
		}
		rows = append(rows, row)
	}
	result := map[string]any{"sets": rows}
	cfg, err := server.ExtractorEffectiveConfig(append(append([]string{"--datadir", *datadir, "--extractor.capture",
		*captureFile}, base...), "--mine"))
	if err == nil {
		_, err = server.NewServer(cfg)
	}
	_, statErr := os.Stat(filepath.Join(*datadir, instanceName, "chaindata"))
	result["server"] = map[string]any{"error": errString(err), "refused": errors.Is(err, profile.ErrProfile),
		"chaindata_created": statErr == nil}
	if *captureStore != "" {
		// A plain bor server (no --extractor.capture) on the stopped capture store.
		cfg, err := server.ExtractorEffectiveConfig(append([]string{"--datadir", *captureStore}, base...))
		if err == nil {
			_, err = server.NewServer(cfg)
		}
		result["plain_server"] = map[string]any{"error": errString(err), "refused": errors.Is(err, wire.ErrPlainStore)}
	}
	return saveJSON(*out, result)
}

// gaps sums the coverage of the result files and lists the callers that the
// run did not reach as required (lifecycle.Gaps).
func gaps(args []string) error {
	flags := flag.NewFlagSet("gaps", flag.ContinueOnError)
	coverage := flags.String("coverage", "", "summed coverage JSON")
	out := flags.String("out", "", "new result file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	data, err := readBounded(*coverage, 1024*1024)
	if err != nil {
		return err
	}
	var sum map[string]lifecycle.Reach
	if err := json.Unmarshal(data, &sum); err != nil {
		return err
	}
	m, err := lifecycle.Callers()
	if err != nil {
		return err
	}
	ids := []string{}
	for _, caller := range m.Callers {
		if caller.Client == borlifecycle.Client {
			ids = append(ids, caller.ID)
		}
	}
	sort.Strings(ids)
	return saveJSON(*out, map[string]any{"gaps": lifecycle.Gaps(m, borlifecycle.Client, sum), "callers": ids})
}

// crash runs `restart` with its arguments as a child process that exits at a
// crash point, and records the child's exit code and the end of its log. The
// command itself succeeds when the child stopped at the crash point (exit 3).
func crash(args []string) error {
	flags := flag.NewFlagSet("crash", flag.ContinueOnError)
	out := flags.String("record", "", "new crash record file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	child := exec.Command(os.Args[0], append([]string{"restart"}, flags.Args()...)...)
	var stderr bytes.Buffer
	child.Stdout, child.Stderr = io.Discard, &stderr
	err := child.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		return err
	}
	log := stderr.String()
	if len(log) > 4096 {
		log = log[len(log)-4096:]
	}
	if err := saveJSON(*out, map[string]any{"exit": code, "stderr_tail": log}); err != nil {
		return err
	}
	if code != 3 {
		return fmt.Errorf("the crash child exited with %d, not at the crash point", code)
	}
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe generate|scenario|restart|crash|fault|native-caller|manual|profile|gaps ...")
		os.Exit(2)
	}
	commands := map[string]func([]string) error{"generate": generate, "scenario": scenario, "restart": restart,
		"fault": fault, "crash": crash, "manual-one": manualOne, "native-caller": nativeCaller, "manual": manual, "profile": profileCheck, "gaps": gaps}
	command, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
	for i, a := range os.Args[2:] {
		if a == "-out" && i+3 < len(os.Args) {
			*flagOut = os.Args[i+3]
		}
	}
	if err := command(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "probe:", err)
		os.Exit(1)
	}
}
