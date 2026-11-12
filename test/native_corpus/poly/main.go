// Test-only probe of the POLY (Bor) block of E01-03.smoke_corpus. It builds
// solely in the accepted Bor native module graph after the preparation has
// installed the accepted admission and decision patch sets
// (test/harness/bor_admission_tree.py): the driver copies it to
// cmd/extractor-bor-corpus-probe and the Heimdall stub of the E01-02.inputs
// Bor probe to its heimdallstub subdirectory.
//
// generate: seal the corpus blocks 1 to 4 with native Bor (the corpus
// transactions in block 1, empty blocks 2 and 3, and the first sprint start,
// block 4, whose state sync event comes from the in-process Heimdall stub,
// D98 item 6).
// import: start a native Bor node with the admission and the decision bridge
// (borimport.Start, bordecision.Start) and import every block through the
// product import path with per-block observations; record the native state
// sync data of each block, the decisions and the delivery obligations.
// reimport: start the node again on the same instance (recovery, then live),
// import the same blocks again, count new observations and decisions, then
// export the native output of the stored chain: blocks, receipts, call frames
// of the native call tracer, native state reads at the parent root and after
// each transaction (a native re-execution of the block prefix on the parent
// state), the block-end state, the state sync receipt and lastStateId().
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
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/cmd/extractor-bor-corpus-probe/heimdallstub"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/eth/tracers"
	_ "github.com/ethereum/go-ethereum/eth/tracers/native"
	"github.com/ethereum/go-ethereum/extractor/bordecision"
	"github.com/ethereum/go-ethereum/extractor/borimport"
	_ "github.com/ethereum/go-ethereum/extractor/borwire"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	signerAddress = "0x71562b71999873db5b286df957af199ec94617f7"
	// The corpus sender is public test key 1 (ORACLE.md, Fixtures).
	senderAddress        = "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf"
	producerID           = "bor"
	runID                = "run-1"
	storeID              = "0123456789abcdef0123456789abcdef"
	normalizationProfile = "test-only-probe-v1"
	instanceName         = "bor"
	corpusBlocks         = 4
	chainID              = 15001
)

var (
	recipient = common.HexToAddress("0x4000000000000000000000000000000000000001")
	parent    = common.HexToAddress("0x3000000000000000000000000000000000000001")
	child     = common.HexToAddress("0x3000000000000000000000000000000000000002")
	gwei      = big.NewInt(1_000_000_000)
)

func readBounded(path string, maximum int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maximum)
	}
	return data, nil
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

func saveJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", " ")
	if err != nil {
		return err
	}
	return writeNew(path, append(data, '\n'))
}

// signerKey reads the tracked span-0 test key of the accepted Bor tests.
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

// senderKey is public test key 1, the corpus sender.
func senderKey() (*ecdsa.PrivateKey, error) {
	key, err := crypto.HexToECDSA(strings.Repeat("0", 63) + "1")
	if err != nil || crypto.PubkeyToAddress(key.PublicKey) != common.HexToAddress(senderAddress) {
		return nil, errors.New("test key 1 identity mismatch")
	}
	return key, nil
}

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
	if config.Bor == nil || config.ChainID.Cmp(big.NewInt(chainID)) != 0 {
		return inputs{}, errors.New("expected the private Bor genesis 15001")
	}
	genesis.Config = &config
	return inputs{genesisBytes: genesisBytes, configBytes: configBytes, genesis: &genesis}, nil
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

// backend starts the native Bor backend in the sequential capture profile
// (hash scheme archive, full sync, no parallel, pipelined, stateless or
// preconf paths), Heimdall behind the process transport.
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

// corpusTransactions are the block 1 transactions of ORACLE.md, Polygon PoS.
func corpusTransactions(config *params.ChainConfig, key *ecdsa.PrivateKey) (types.Transactions, error) {
	coin := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	price := new(big.Int).Mul(big.NewInt(5), gwei)
	unsigned := []types.TxData{
		&types.LegacyTx{Nonce: 0, GasPrice: price, Gas: 21_000, To: &recipient, Value: coin},
		&types.DynamicFeeTx{ChainID: big.NewInt(chainID), Nonce: 1, GasTipCap: new(big.Int).Mul(big.NewInt(2), gwei),
			GasFeeCap: price, Gas: 21_000, To: &recipient, Value: new(big.Int).Set(gwei)},
		&types.LegacyTx{Nonce: 2, GasPrice: price, Gas: 200_000, To: &parent, Value: big.NewInt(5)},
		&types.LegacyTx{Nonce: 3, GasPrice: price, Gas: 100_000, To: &child, Value: big.NewInt(7)},
	}
	signer := types.LatestSigner(config)
	out := make(types.Transactions, 0, len(unsigned))
	for _, data := range unsigned {
		tx, err := types.SignNewTx(key, signer, data)
		if err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, nil
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
	sender, err := senderKey()
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
	txs, err := corpusTransactions(config, sender)
	if err != nil {
		return err
	}
	var encoded bytes.Buffer
	prepared := make([]map[string]any, 0, corpusBlocks)
	ctx := context.Background()
	for number := uint64(1); number <= corpusBlocks; number++ {
		parentBlock := chain.CurrentBlock()
		header := &types.Header{ParentHash: parentBlock.Hash(), Number: new(big.Int).SetUint64(number),
			GasLimit: parentBlock.GasLimit, Time: parentBlock.Time + 1, Difficulty: big.NewInt(1)}
		if config.IsLondon(header.Number) {
			header.BaseFee = eip1559.CalcBaseFee(config, parentBlock)
			if !config.IsLondon(parentBlock.Number) {
				limit := parentBlock.GasLimit * config.ElasticityMultiplier()
				header.GasLimit = core.CalcGasLimit(limit, limit)
			}
		}
		if err = engine.Prepare(chain, header, false); err != nil {
			return fmt.Errorf("prepare block %d: %w", number, err)
		}
		// Prepare moves a past header time to the wall clock and Seal then
		// waits for it. The earliest time native verification accepts keeps
		// every header in the past, so the chain is deterministic.
		header.Time = parentBlock.Time + bor.CalcProducerDelay(number, 0, config.Bor)
		statedb, err := chain.StateAt(parentBlock.Root)
		if err != nil {
			return err
		}
		body := &types.Body{}
		var receipts []*types.Receipt
		if number == 1 {
			// The native block producer applies the transactions with the
			// signer as the execution beneficiary.
			evm := vm.NewEVM(core.NewEVMBlockContext(header, chain, &author), statedb, config, vm.Config{})
			pool := new(core.GasPool).AddGas(header.GasLimit)
			used := uint64(0)
			for i, tx := range txs {
				statedb.SetTxContext(tx.Hash(), i)
				receipt, err := core.ApplyTransaction(evm, pool, statedb, header, tx, &used)
				if err != nil {
					return fmt.Errorf("apply corpus transaction %d: %w", i, err)
				}
				receipts = append(receipts, receipt)
			}
			header.GasUsed = used
			body.Transactions = txs
		}
		block, _, _, err := engine.FinalizeAndAssemble(chain, header, statedb, body, receipts)
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
			"state_root": block.Root().Hex(), "timestamp": block.Time(), "transactions": len(block.Transactions())})
	}
	if err = writeNew(filepath.Join(*output, "blocks.rlp"), encoded.Bytes()); err != nil {
		return err
	}
	return saveJSON(filepath.Join(*output, "generated.json"), map[string]any{
		"genesis_hash": chain.GetBlockByNumber(0).Hash().Hex(), "blocks": prepared})
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
			break
		} else if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	if len(blocks) != corpusBlocks {
		return nil, fmt.Errorf("expected %d corpus blocks, read %d", corpusBlocks, len(blocks))
	}
	return blocks, nil
}

// counter wraps the admission observer and counts the observations it opens.
type counter struct {
	node *borimport.Node
	mu   sync.Mutex
	seen []uint64
	keys map[uint64][]string
	errs []string
}

type counted struct {
	c     *counter
	inner core.ExtractorObservation
}

func (c *counter) Observe(block *types.Block, parentHeader *types.Header) (core.ExtractorObservation, error) {
	c.mu.Lock()
	c.seen = append(c.seen, block.NumberU64())
	c.mu.Unlock()
	inner, err := c.node.Observe(block, parentHeader)
	if err != nil {
		c.fail(err)
		return nil, err
	}
	return counted{c: c, inner: inner}, nil
}

func (c *counter) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errs = append(c.errs, err.Error())
}

func (o counted) Done(block *types.Block, receipts types.Receipts, usedGas uint64, err error) error {
	err = o.inner.Done(block, receipts, usedGas, err)
	key := o.inner.(interface{ Key() string }).Key()
	o.c.mu.Lock()
	o.c.keys[block.NumberU64()] = append(o.c.keys[block.NumberU64()], key)
	o.c.mu.Unlock()
	if err != nil {
		o.c.fail(err)
	}
	return err
}

func (o counted) Fail(err error) error {
	err = o.inner.Fail(err)
	if err != nil {
		o.c.fail(err)
	}
	return err
}

// DecisionRow is one decision the bridge lists.
type DecisionRow struct {
	Sequence uint64       `json:"sequence"`
	State    string       `json:"state"`
	Kind     string       `json:"kind"`
	Members  []MemberRow  `json:"members"`
	Marker   bool         `json:"marker"`
}

// MemberRow is one member of a decision's intent.
type MemberRow struct {
	Observation string `json:"observation"`
	Block       string `json:"block"`
}

// StateSyncRow is one native state sync event: the state sync data that the
// native engine records at Finalize during the import.
type StateSyncRow struct {
	Block    uint64 `json:"block"`
	ID       uint64 `json:"id"`
	Contract string `json:"contract"`
	Data     string `json:"data"`
}

type nodeRun struct {
	stack  *node.Node
	eth    *eth.Ethereum
	chain  *core.BlockChain
	import_ *borimport.Node
	decided *bordecision.Node
	count  *counter
}

// start opens the native node on dataDir with the admission and the decision
// bridge: the startup identity, borimport.Start, then the recovery driver and
// GoLive (bordecision.Start), then the live observer.
func start(in inputs, bodies, dataDir, commit string, result map[string]any) (*nodeRun, error) {
	stack, err := newStack(dataDir)
	if err != nil {
		return nil, err
	}
	cfg := borimport.Config{InstanceDir: stack.ResolvePath(""), StoreID: storeID, Producer: producerID, Run: runID,
		Genesis: in.genesisBytes, ChainConfig: in.configBytes, Commit: commit, Normalization: normalizationProfile,
		HeimdallHost: heimdallstub.Host}
	if err = borimport.Preflight(cfg, in.genesis.Config.ChainID.String(), in.genesis.ToBlock().Hash()); err != nil {
		stack.Close()
		return nil, err
	}
	http.DefaultTransport = stubTransport{stub: heimdallstub.Stub{Bodies: bodies, Variant: "corpus"}}
	backend, err := backend(stack, in.genesis)
	if err != nil {
		stack.Close()
		return nil, err
	}
	chain := backend.BlockChain()
	imp, err := borimport.Start(cfg, backend.ChainDb(), chain)
	if err != nil {
		shutdown(stack, backend)
		return nil, err
	}
	run := &nodeRun{stack: stack, eth: backend, chain: chain, import_: imp,
		count: &counter{node: imp, seen: []uint64{}, keys: map[uint64][]string{}, errs: []string{}}}
	decided, report, err := bordecision.Start(imp, nil)
	result["admission_recovery"] = report
	if decided != nil {
		result["decision_recovery"] = decided.Report
	}
	if err != nil {
		imp.Detach()
		shutdown(stack, backend)
		return nil, fmt.Errorf("recovery: %w", err)
	}
	run.decided = decided
	decided.Attach(run.count)
	return run, nil
}

func (r *nodeRun) close() {
	r.decided.Detach()
	shutdown(r.stack, r.eth)
}

func (r *nodeRun) decisions() ([]DecisionRow, []uint64, error) {
	token := r.import_.Driver.Guard.Acquire()
	decisions, err := r.decided.Bridge.Decisions(token)
	var obligations []uint64
	if err == nil {
		list, listErr := r.decided.Bridge.Obligations(token)
		for _, o := range list {
			obligations = append(obligations, o.Decided.Sequence)
		}
		err = listErr
	}
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		return nil, nil, err
	}
	rows := []DecisionRow{}
	for _, d := range decisions {
		row := DecisionRow{Sequence: d.Sequence, State: string(d.State), Kind: string(d.Intent.Kind),
			Members: []MemberRow{}, Marker: d.Marker}
		for _, m := range d.Intent.Members {
			row.Members = append(row.Members, MemberRow{Observation: m.Observation, Block: m.Block})
		}
		rows = append(rows, row)
	}
	if obligations == nil {
		obligations = []uint64{}
	}
	return rows, obligations, nil
}

func importCommand(args []string) (returned error) {
	flags := flag.NewFlagSet("import", flag.ContinueOnError)
	genesisPath := flags.String("genesis", "", "canonical genesis JSON")
	configPath := flags.String("config", "", "canonical chain-config JSON")
	blocksPath := flags.String("blocks", "", "blocks.rlp to import")
	bodies := flags.String("bodies", "", "stub response directory")
	dataDir := flags.String("datadir", "", "persistent native data directory")
	commit := flags.String("commit", "", "Bor source commit")
	output := flags.String("out", "", "result JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	result := map[string]any{"args": args}
	defer func() {
		if returned != nil {
			result["error"] = returned.Error()
		}
		if err := saveJSON(*output, result); returned == nil {
			returned = err
		}
	}()
	in, err := loadInputs(*genesisPath, *configPath)
	if err != nil {
		return err
	}
	blocks, err := readBlocks(*blocksPath)
	if err != nil {
		return err
	}
	run, err := start(in, *bodies, *dataDir, *commit, result)
	if err != nil {
		return err
	}
	defer run.close()
	events := []StateSyncRow{}
	for _, block := range blocks {
		if _, err := run.chain.InsertChain([]*types.Block{block}, false); err != nil {
			return fmt.Errorf("import block %d: %w", block.NumberU64(), err)
		}
		// The state sync data the native engine committed for this block:
		// Bor's Finalize records it on the header chain of the native
		// processor for every block (empty without a state sync).
		for _, data := range run.chain.HeaderChain().GetStateSync() {
			events = append(events, StateSyncRow{Block: block.NumberU64(), ID: data.ID,
				Contract: hex.EncodeToString(data.Contract[:]), Data: hex.EncodeToString(data.Data)})
		}
		if run.chain.CurrentBlock().Hash() != block.Hash() {
			return fmt.Errorf("block %d did not become the head", block.NumberU64())
		}
	}
	decisions, obligations, err := run.decisions()
	if err != nil {
		return err
	}
	run.count.mu.Lock()
	result["observed"], result["keys"], result["observer_errors"] = run.count.seen, run.count.keys, run.count.errs
	run.count.mu.Unlock()
	result["state_sync"], result["decisions"], result["obligations"] = events, decisions, obligations
	head := run.chain.CurrentBlock()
	result["head_number"], result["head_hash"] = head.Number.Uint64(), head.Hash().Hex()
	return nil
}

// Account is one native state read.
type Account struct {
	Balance string            `json:"balance"`
	Nonce   uint64            `json:"nonce"`
	Code    string            `json:"code"`
	Storage map[string]string `json:"storage"`
}

func readAccounts(statedb *state.StateDB, addresses []common.Address) map[string]Account {
	out := map[string]Account{}
	for _, address := range addresses {
		storage := map[string]string{}
		for slot := 0; slot < 4; slot++ {
			value := statedb.GetState(address, common.BigToHash(big.NewInt(int64(slot))))
			storage[fmt.Sprint(slot)] = hex.EncodeToString(value[:])
		}
		out[hex.EncodeToString(address[:])] = Account{Balance: statedb.GetBalance(address).ToBig().String(),
			Nonce: statedb.GetNonce(address), Code: hex.EncodeToString(statedb.GetCode(address)), Storage: storage}
	}
	return out
}

// LogRow is one native log.
type LogRow struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

func logRows(logs []*types.Log) []LogRow {
	out := []LogRow{}
	for _, l := range logs {
		row := LogRow{Address: hex.EncodeToString(l.Address[:]), Topics: []string{}, Data: hex.EncodeToString(l.Data)}
		for _, t := range l.Topics {
			row.Topics = append(row.Topics, hex.EncodeToString(t[:]))
		}
		out = append(out, row)
	}
	return out
}

// TxRow is the native output of one transaction.
type TxRow struct {
	Index       int                `json:"index"`
	Hash        string             `json:"hash"`
	Type        uint8              `json:"type"`
	From        string             `json:"from"`
	Nonce       uint64             `json:"nonce"`
	To          string             `json:"to"`
	Value       string             `json:"value"`
	Gas         uint64             `json:"gas"`
	GasPrice    string             `json:"gas_price,omitempty"`
	MaxFee      string             `json:"max_fee_per_gas,omitempty"`
	MaxPriority string             `json:"max_priority_fee_per_gas,omitempty"`
	Data        string             `json:"data"`
	Receipt     map[string]any     `json:"receipt"`
	Trace       json.RawMessage    `json:"call_tracer"`
	Before      map[string]Account `json:"state_before"`
	After       map[string]Account `json:"state_after"`
}

// BlockRow is the native output of one block.
type BlockRow struct {
	Number       uint64             `json:"number"`
	Hash         string             `json:"hash"`
	StateRoot    string             `json:"state_root"`
	BaseFee      *string            `json:"base_fee"`
	GasLimit     uint64             `json:"gas_limit"`
	GasUsed      uint64             `json:"gas_used"`
	BodyTxCount  int                `json:"body_tx_count"`
	Transactions []TxRow            `json:"transactions"`
	BorReceipt   []LogRow           `json:"state_sync_receipt_logs"`
	HasBorReceipt bool              `json:"state_sync_receipt"`
	LastStateID  string             `json:"state_receiver_last_state_id"`
	StateAfter   map[string]Account `json:"state_after_block"`
}

func exportChain(run *nodeRun, blocks []*types.Block, addresses []common.Address) ([]BlockRow, error) {
	chain, config := run.chain, run.chain.Config()
	engine := run.eth.Engine().(*bor.Bor)
	tracer := "callTracer"
	api := tracers.NewAPI(run.eth.APIBackend)
	out := []BlockRow{}
	for _, want := range blocks {
		number := want.NumberU64()
		block := chain.GetBlockByNumber(number)
		if block == nil || block.Hash() != want.Hash() {
			return nil, fmt.Errorf("block %d is not the canonical corpus block", number)
		}
		header := block.Header()
		row := BlockRow{Number: number, Hash: hex.EncodeToString(block.Hash().Bytes()),
			StateRoot: hex.EncodeToString(block.Root().Bytes()), GasLimit: header.GasLimit, GasUsed: header.GasUsed,
			BodyTxCount: len(block.Transactions()), Transactions: []TxRow{}, BorReceipt: []LogRow{}}
		if header.BaseFee != nil {
			value := header.BaseFee.String()
			row.BaseFee = &value
		}
		receipts := chain.GetReceiptsByHash(block.Hash())
		if len(receipts) != len(block.Transactions()) {
			return nil, fmt.Errorf("block %d: %d receipts for %d transactions", number, len(receipts), len(block.Transactions()))
		}
		parentHeader := chain.GetHeader(header.ParentHash, number-1)
		statedb, err := chain.StateAt(parentHeader.Root)
		if err != nil {
			return nil, err
		}
		// Native re-execution of the block prefix on the parent state, as the
		// native processor applies it (author nil: the engine's author, the
		// recovered signer).
		evm := vm.NewEVM(core.NewEVMBlockContext(header, chain, nil), statedb, config, vm.Config{})
		pool := new(core.GasPool).AddGas(header.GasLimit)
		used := uint64(0)
		signer := types.MakeSigner(config, header.Number, header.Time)
		for i, tx := range block.Transactions() {
			from, err := types.Sender(signer, tx)
			if err != nil {
				return nil, err
			}
			txRow := TxRow{Index: i, Hash: hex.EncodeToString(tx.Hash().Bytes()), Type: tx.Type(),
				From: hex.EncodeToString(from[:]), Nonce: tx.Nonce(), Value: tx.Value().String(), Gas: tx.Gas(),
				Data: hex.EncodeToString(tx.Data())}
			if tx.To() != nil {
				txRow.To = hex.EncodeToString(tx.To()[:])
			}
			if tx.Type() == types.DynamicFeeTxType {
				txRow.MaxFee, txRow.MaxPriority = tx.GasFeeCap().String(), tx.GasTipCap().String()
			} else {
				txRow.GasPrice = tx.GasPrice().String()
			}
			stored := receipts[i]
			txRow.Receipt = map[string]any{"status": stored.Status, "gas_used": stored.GasUsed,
				"cumulative_gas_used": stored.CumulativeGasUsed, "effective_gas_price": stored.EffectiveGasPrice.String(),
				"logs": logRows(stored.Logs)}
			trace, err := api.TraceTransaction(context.Background(), tx.Hash(), &tracers.TraceConfig{Tracer: &tracer})
			if err != nil {
				return nil, fmt.Errorf("trace block %d transaction %d: %w", number, i, err)
			}
			if txRow.Trace, err = json.Marshal(trace); err != nil {
				return nil, err
			}
			txRow.Before = readAccounts(statedb, addresses)
			statedb.SetTxContext(tx.Hash(), i)
			receipt, err := core.ApplyTransaction(evm, pool, statedb, header, tx, &used)
			if err != nil {
				return nil, fmt.Errorf("re-execute block %d transaction %d: %w", number, i, err)
			}
			// The re-execution must reproduce the stored receipt.
			if receipt.Status != stored.Status || receipt.GasUsed != stored.GasUsed ||
				receipt.CumulativeGasUsed != stored.CumulativeGasUsed || len(receipt.Logs) != len(stored.Logs) {
				return nil, fmt.Errorf("block %d transaction %d: the re-execution differs from the stored receipt", number, i)
			}
			for j := range receipt.Logs {
				a, b := receipt.Logs[j], stored.Logs[j]
				if a.Address != b.Address || !bytes.Equal(a.Data, b.Data) || len(a.Topics) != len(b.Topics) {
					return nil, fmt.Errorf("block %d transaction %d log %d: the re-execution differs", number, i, j)
				}
			}
			statedb.Finalise(true)
			txRow.After = readAccounts(statedb, addresses)
			row.Transactions = append(row.Transactions, txRow)
		}
		after, err := chain.StateAt(block.Root())
		if err != nil {
			return nil, err
		}
		row.StateAfter = readAccounts(after, addresses)
		if borReceipt := rawdb.ReadBorReceipt(run.eth.ChainDb(), block.Hash(), number, config); borReceipt != nil {
			row.HasBorReceipt = true
			row.BorReceipt = logRows(borReceipt.Logs)
		}
		last, err := engine.GenesisContractsClient.LastStateId(nil, number, block.Hash())
		if err != nil {
			return nil, fmt.Errorf("lastStateId at block %d: %w", number, err)
		}
		row.LastStateID = last.String()
		out = append(out, row)
	}
	return out, nil
}

func reimportCommand(args []string) (returned error) {
	flags := flag.NewFlagSet("reimport", flag.ContinueOnError)
	genesisPath := flags.String("genesis", "", "canonical genesis JSON")
	configPath := flags.String("config", "", "canonical chain-config JSON")
	blocksPath := flags.String("blocks", "", "blocks.rlp to import again")
	bodies := flags.String("bodies", "", "stub response directory")
	dataDir := flags.String("datadir", "", "persistent native data directory")
	commit := flags.String("commit", "", "Bor source commit")
	accountList := flags.String("accounts", "", "comma-separated accounts to read")
	output := flags.String("out", "", "result JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	result := map[string]any{"args": args}
	defer func() {
		if returned != nil {
			result["error"] = returned.Error()
		}
		if err := saveJSON(*output, result); returned == nil {
			returned = err
		}
	}()
	var addresses []common.Address
	for _, item := range strings.Split(*accountList, ",") {
		if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(item) {
			return fmt.Errorf("bad account %q", item)
		}
		addresses = append(addresses, common.HexToAddress(item))
	}
	in, err := loadInputs(*genesisPath, *configPath)
	if err != nil {
		return err
	}
	blocks, err := readBlocks(*blocksPath)
	if err != nil {
		return err
	}
	run, err := start(in, *bodies, *dataDir, *commit, result)
	if err != nil {
		return err
	}
	defer run.close()
	before, _, err := run.decisions()
	if err != nil {
		return err
	}
	result["decisions_before"] = before
	// The stored blocks again through the product import path.
	inserted, err := run.chain.InsertChain(blocks, false)
	result["reimported"] = inserted
	if err != nil {
		result["reimport_error"] = err.Error()
	}
	after, obligations, err := run.decisions()
	if err != nil {
		return err
	}
	run.count.mu.Lock()
	result["observed"], result["observer_errors"] = run.count.seen, run.count.errs
	run.count.mu.Unlock()
	result["decisions_after"], result["obligations"] = after, obligations
	head := run.chain.CurrentBlock()
	result["head_number"], result["head_hash"] = head.Number.Uint64(), head.Hash().Hex()
	// The export reads the stored chain; no block is imported from here on.
	run.decided.Detach()
	exported, err := exportChain(run, blocks, addresses)
	if err != nil {
		return err
	}
	result["blocks"] = exported
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe generate|import|reimport ...")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "generate":
		err = generate(os.Args[2:])
	case "import":
		err = importCommand(os.Args[2:])
	case "reimport":
		err = reimportCommand(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
