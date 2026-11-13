package bscdecision

// The BSC probe of E01-03.smoke_corpus. The driver
// (test/native_corpus/bsc/bsc_corpus.py) installs this file next to the
// accepted BSC decision package and its native probe, whose node, store and
// mirror helpers it reuses. TestBSCCorpus runs one step per process, chosen
// by EXTRACTOR_CORPUS_STEP:
//
//   - generate: native Parlia seals the corpus blocks on an in-memory chain
//     with the public test validator key; block 1 holds the three corpus
//     transactions signed with public test key 1, and Parlia appends its
//     system transactions; block 2 is empty.
//   - import: a new node with the admission and the decision bridge recovers,
//     goes live and imports both blocks through InsertChain.
//   - reimport: the node starts again on the same home, recovers, imports
//     both blocks again, and exports the native output.
//
// The probe never reads the expected file: it takes only the list of
// accounts to read from EXTRACTOR_CORPUS_ACCOUNTS.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/parlia"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/systemcontracts"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/tracers"
	_ "github.com/ethereum/go-ethereum/eth/tracers/native"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	envCorpusStep     = "EXTRACTOR_CORPUS_STEP"
	envCorpusInputs   = "EXTRACTOR_CORPUS_INPUTS"
	envCorpusWork     = "EXTRACTOR_CORPUS_WORK"
	envCorpusAccounts = "EXTRACTOR_CORPUS_ACCOUNTS"
	corpusValidator   = "0x03735c2ed70a56cd221e0024eb4bf90243c9d6e9"
)

// corpusTx is one corpus transaction of block 1 (test/native_corpus/ORACLE.md, BSC table).
type corpusTx struct {
	to       string
	value    int64
	gas      uint64
	gasPrice int64
}

var corpusTxs = []corpusTx{
	{"0x4000000000000000000000000000000000000001", 1_000_000_000_000_000_000, 21_000, 5_000_000_000},
	{"0x3000000000000000000000000000000000000001", 5, 200_000, 5_000_000_000},
	{"0x3000000000000000000000000000000000000002", 7, 100_000, 5_000_000_000},
}

func corpusGenesis(t *testing.T) ([]byte, *core.Genesis) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(probeEnv(t, envCorpusInputs), "genesis.json"))
	if err != nil {
		t.Fatal(err)
	}
	genesis := new(core.Genesis)
	if err := json.Unmarshal(data, genesis); err != nil {
		t.Fatal(err)
	}
	if genesis.Config.ChainID.Cmp(big.NewInt(714)) != 0 || genesis.Config.Parlia == nil {
		t.Fatal("not the private Parlia chain 714")
	}
	return data, genesis
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
}

func TestBSCCorpus(t *testing.T) {
	work := probeEnv(t, envCorpusWork)
	switch step := probeEnv(t, envCorpusStep); step {
	case "generate":
		corpusGenerate(t, work)
	case "import":
		corpusImport(t, work)
	case "reimport":
		corpusReimport(t, work)
	default:
		t.Fatalf("unknown step %q", step)
	}
}

// ---- generate ----

func corpusGenerate(t *testing.T, work string) {
	_, genesis := corpusGenesis(t)
	data, err := os.ReadFile(filepath.Join(probeEnv(t, envCorpusInputs), "validator-keystore.json"))
	if err != nil {
		t.Fatal(err)
	}
	validatorKey, err := keystore.DecryptKey(data, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(validatorKey.Address.Hex(), corpusValidator) ||
		crypto.PubkeyToAddress(validatorKey.PrivateKey.PublicKey) != validatorKey.Address {
		t.Fatal("public validator identity mismatch")
	}
	db := rawdb.NewMemoryDatabase()
	defer db.Close()
	engine := parlia.New(genesis.Config, db, nil, genesis.ToBlock().Hash())
	defer engine.Close()
	engine.Authorize(validatorKey.Address,
		func(account accounts.Account, mime string, data []byte) ([]byte, error) {
			if account.Address != validatorKey.Address || mime != accounts.MimetypeParlia {
				return nil, errors.New("unexpected header signer")
			}
			return crypto.Sign(crypto.Keccak256(data), validatorKey.PrivateKey)
		},
		func(account accounts.Account, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error) {
			if account.Address != validatorKey.Address || chainID.Cmp(big.NewInt(714)) != 0 {
				return nil, errors.New("unexpected transaction signer")
			}
			return types.SignTx(tx, types.LatestSignerForChainID(chainID), validatorKey.PrivateKey)
		})
	config := core.DefaultConfig()
	config.ArchiveMode, config.SnapshotLimit = true, 0
	chain, err := core.NewBlockChain(db, genesis, engine, config)
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()
	sender, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	var blocks bytes.Buffer
	rows := []map[string]any{}
	for number := uint64(1); number <= 2; number++ {
		parent := chain.CurrentBlock()
		header := &types.Header{ParentHash: parent.Hash(), Number: new(big.Int).SetUint64(number),
			GasLimit: parent.GasLimit, Time: parent.Time + 3, Difficulty: big.NewInt(1)}
		if err := engine.Prepare(chain, header); err != nil {
			t.Fatal(err)
		}
		statedb, err := chain.StateAt(parent)
		if err != nil {
			t.Fatal(err)
		}
		body := &types.Body{}
		receipts := []*types.Receipt{}
		if number == 1 {
			signer := types.MakeSigner(genesis.Config, header.Number, header.Time)
			pool := core.NewGasPool(header.GasLimit)
			evm := vm.NewEVM(core.NewEVMBlockContext(header, chain, &header.Coinbase), statedb, genesis.Config, vm.Config{})
			for nonce, row := range corpusTxs {
				tx, err := types.SignTx(types.NewTransaction(uint64(nonce), common.HexToAddress(row.to),
					big.NewInt(row.value), row.gas, big.NewInt(row.gasPrice), nil), signer, sender)
				if err != nil {
					t.Fatal(err)
				}
				statedb.SetTxContext(tx.Hash(), len(body.Transactions))
				receipt, err := core.ApplyTransaction(evm, pool, statedb, header, tx, core.NewReceiptBloomGenerator())
				if err != nil {
					t.Fatal(err)
				}
				body.Transactions = append(body.Transactions, tx)
				receipts = append(receipts, receipt)
			}
			header.GasUsed = pool.Used()
		}
		block, _, err := core.AssembleBlock(engine, chain, header, statedb, body, receipts)
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan *types.Block, 1)
		stop := make(chan struct{})
		if err := engine.Seal(chain, block, result, stop); err != nil {
			t.Fatal(err)
		}
		select {
		case block = <-result:
		case <-time.After(30 * time.Second):
			close(stop)
			t.Fatal("native seal deadline")
		}
		close(stop)
		if count, err := chain.InsertChain(types.Blocks{block}); err != nil || count != 1 {
			t.Fatalf("native insertion of block %d: %d %v", number, count, err)
		}
		if err := rlp.Encode(&blocks, block); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, map[string]any{"number": number, "hash": hexOf(block.Hash().Bytes()),
			"state_root": hexOf(block.Root().Bytes()), "timestamp": block.Time(),
			"transactions": len(block.Transactions()), "gas_used": block.GasUsed()})
	}
	if err := os.WriteFile(filepath.Join(work, "blocks.rlp"), blocks.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(work, "generated.json"), map[string]any{
		"genesis_hash": hexOf(genesis.ToBlock().Hash().Bytes()), "blocks": rows})
}

// ---- import ----

func corpusFixture(t *testing.T, work string) fixture {
	t.Helper()
	genesisFile, genesis := corpusGenesis(t)
	fx := fixture{genesisFile: genesisFile, genesis: genesis, commit: probeEnv(t, envCommit)}
	data, err := os.ReadFile(filepath.Join(work, "blocks.rlp"))
	if err != nil {
		t.Fatal(err)
	}
	stream := rlp.NewStream(bytes.NewReader(data), 0)
	for {
		block := new(types.Block)
		if err := stream.Decode(block); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		fx.blocks = append(fx.blocks, block)
	}
	if len(fx.blocks) != 2 || fx.blocks[0].NumberU64() != 1 || fx.blocks[1].NumberU64() != 2 {
		t.Fatalf("corpus blocks %d", len(fx.blocks))
	}
	return fx
}

func hexOf(data []byte) string { return hex.EncodeToString(data) }

// bridgeState is the decision bridge state of a node: its records, the
// observation of each block and the delivery obligations.
type bridgeState struct {
	Admission    map[string]json.RawMessage `json:"admission_records"`
	Decisions    map[string]json.RawMessage `json:"decision_records"`
	Obligations  []obligationRow            `json:"obligations"`
	Observations []observationRow           `json:"open_observations"`
	Counters     []uint64                   `json:"counters"`
	Mode         string                     `json:"mode"`
	HeadNumber   uint64                     `json:"head_number"`
	HeadHash     string                     `json:"head_hash"`
}

func stateOf(n *node) bridgeState {
	sc := &scenario{}
	n.finish(sc)
	observations, decisions := counters(n)
	return bridgeState{Admission: sc.Admission, Decisions: sc.Decisions, Obligations: sc.Obligations,
		Observations: sc.Observations, Counters: []uint64{observations, decisions}, Mode: sc.Mode,
		HeadNumber: sc.HeadNumber, HeadHash: sc.HeadHash}
}

func corpusImport(t *testing.T, work string) {
	fx := corpusFixture(t, work)
	tr := newTracer(t, filepath.Join(work, "trace-import.jsonl"))
	n, err := startNode(t, fx, filepath.Join(work, "home"), modelNative, tr.emit)
	if err != nil {
		t.Fatal(err)
	}
	defer n.close()
	if err := n.hook.Recover(n.chain); err != nil {
		t.Fatal(err)
	}
	_, importErr := n.chain.InsertChain(fx.blocks)
	writeJSON(t, filepath.Join(work, "import.json"), map[string]any{"error": errText(importErr), "bridge": stateOf(n),
		"blocks": blockIDs(fx)})
}

func blockIDs(fx fixture) []map[string]any {
	out := []map[string]any{}
	for _, block := range fx.blocks {
		out = append(out, map[string]any{"number": block.NumberU64(), "hash": hexOf(block.Hash().Bytes())})
	}
	return out
}

// ---- reimport and export ----

func corpusReimport(t *testing.T, work string) {
	fx := corpusFixture(t, work)
	tr := newTracer(t, filepath.Join(work, "trace-reimport.jsonl"))
	n, err := startNode(t, fx, filepath.Join(work, "home"), modelNative, tr.emit)
	if err != nil {
		t.Fatal(err)
	}
	defer n.close()
	recoverErr := n.hook.Recover(n.chain)
	before := stateOf(n)
	_, reimportErr := n.chain.InsertChain(fx.blocks)
	after := stateOf(n)
	addresses := []common.Address{}
	for _, text := range strings.Split(probeEnv(t, envCorpusAccounts), ",") {
		addresses = append(addresses, common.HexToAddress(text))
	}
	blocks := []map[string]any{}
	for _, block := range fx.blocks {
		blocks = append(blocks, exportBlock(t, n, block, addresses))
	}
	writeJSON(t, filepath.Join(work, "reimport.json"), map[string]any{"recover_error": errText(recoverErr),
		"reimport_error": errText(reimportErr), "before": before, "after": after, "blocks": blocks})
}

func accountsAt(statedb *state.StateDB, addresses []common.Address) map[string]any {
	out := map[string]any{}
	for _, address := range addresses {
		code := statedb.GetCode(address)
		row := map[string]any{"balance": statedb.GetBalance(address).Dec(), "nonce": statedb.GetNonce(address),
			"code": hexOf(code)}
		if len(code) > 0 {
			storage := map[string]string{}
			for slot := int64(0); slot < 4; slot++ {
				value := statedb.GetState(address, common.BigToHash(big.NewInt(slot)))
				storage[fmt.Sprint(slot)] = hexOf(value.Bytes())
			}
			row["storage"] = storage
		}
		out[strings.ToLower(strings.TrimPrefix(address.Hex(), "0x"))] = row
	}
	return out
}

func logsOf(logs []*types.Log) []map[string]any {
	out := []map[string]any{}
	for _, log := range logs {
		topics := []string{}
		for _, topic := range log.Topics {
			topics = append(topics, hexOf(topic.Bytes()))
		}
		out = append(out, map[string]any{"address": hexOf(log.Address.Bytes()), "topics": topics,
			"data": hexOf(log.Data)})
	}
	return out
}

func addressText(address *common.Address) string {
	if address == nil {
		return ""
	}
	return hexOf(address.Bytes())
}

// exportBlock reads one stored block: header fields and receipts from the
// native database, the state before and after each user transaction from a
// native re-execution of the block prefix on the parent state (checked
// against the stored receipts), the call frames from the native callTracer,
// the Parlia system transactions, and the block-end state at the block root.
func exportBlock(t *testing.T, n *node, block *types.Block, addresses []common.Address) map[string]any {
	t.Helper()
	chain, db := n.chain, n.db
	config := chain.Config()
	header := rawdb.ReadHeader(db, block.Hash(), block.NumberU64())
	if header == nil {
		t.Fatalf("block %d is not stored", block.NumberU64())
	}
	stored := rawdb.ReadBlock(db, block.Hash(), block.NumberU64())
	receipts := rawdb.ReadReceipts(db, block.Hash(), block.NumberU64(), block.Time(), config)
	if stored == nil || len(receipts) != len(stored.Transactions()) {
		t.Fatalf("block %d body or receipts", block.NumberU64())
	}
	engine := chain.Engine().(*parlia.Parlia)
	parent := chain.GetHeaderByHash(header.ParentHash)
	statedb, err := chain.StateAt(parent)
	if err != nil {
		t.Fatal(err)
	}
	systemcontracts.TryUpdateBuildInSystemContract(config, header.Number, parent.Time, header.Time, statedb, true)
	signer := types.MakeSigner(config, header.Number, header.Time)
	pool := core.NewGasPool(header.GasLimit)
	transactions := []map[string]any{}
	system := []map[string]any{}
	for i, tx := range stored.Transactions() {
		from, err := types.Sender(signer, tx)
		if err != nil {
			t.Fatal(err)
		}
		receipt := receipts[i]
		isSystem, err := engine.IsSystemTransaction(tx, header)
		if err != nil {
			t.Fatal(err)
		}
		if isSystem {
			system = append(system, map[string]any{"index": i, "from": hexOf(from.Bytes()), "nonce": tx.Nonce(),
				"to": addressText(tx.To()), "value": tx.Value().String(), "data": hexOf(tx.Data()),
				"gas": tx.Gas(), "gas_price": tx.GasPrice().String(), "status": receipt.Status})
			continue
		}
		if len(system) > 0 {
			t.Fatalf("block %d: user transaction %d after a system transaction", block.NumberU64(), i)
		}
		before := accountsAt(statedb, addresses)
		tracer, err := tracers.DefaultDirectory.New("callTracer", &tracers.Context{BlockHash: block.Hash(),
			BlockNumber: block.Number(), TxIndex: i, TxHash: tx.Hash()}, nil, config)
		if err != nil {
			t.Fatal(err)
		}
		evm := vm.NewEVM(core.NewEVMBlockContext(header, chain, nil), statedb, config, vm.Config{Tracer: tracer.Hooks})
		statedb.SetTxContext(tx.Hash(), i)
		again, err := core.ApplyTransaction(evm, pool, statedb, header, tx)
		if err != nil {
			t.Fatal(err)
		}
		if again.Status != receipt.Status || again.GasUsed != receipt.GasUsed ||
			again.CumulativeGasUsed != receipt.CumulativeGasUsed || !sameLogs(again.Logs, receipt.Logs) {
			t.Fatalf("block %d tx %d: the re-execution differs from the stored receipt", block.NumberU64(), i)
		}
		frame, err := tracer.GetResult()
		if err != nil {
			t.Fatal(err)
		}
		row := map[string]any{"index": i, "hash": hexOf(tx.Hash().Bytes()), "type": int(tx.Type()),
			"from": hexOf(from.Bytes()), "nonce": tx.Nonce(), "to": addressText(tx.To()),
			"value": tx.Value().String(), "gas": tx.Gas(), "data": hexOf(tx.Data()),
			"receipt": map[string]any{"status": receipt.Status, "gas_used": receipt.GasUsed,
				"cumulative_gas_used": receipt.CumulativeGasUsed,
				"effective_gas_price": receipt.EffectiveGasPrice.String(), "logs": logsOf(receipt.Logs)},
			"call_tracer": frame, "state_before": before, "state_after": accountsAt(statedb, addresses)}
		if tx.Type() == types.DynamicFeeTxType {
			row["max_fee_per_gas"], row["max_priority_fee_per_gas"] = tx.GasFeeCap().String(), tx.GasTipCap().String()
		} else {
			row["gas_price"] = tx.GasPrice().String()
		}
		transactions = append(transactions, row)
	}
	end, err := chain.StateAt(header)
	if err != nil {
		t.Fatal(err)
	}
	validator := common.HexToAddress(corpusValidator)
	var baseFee any
	if header.BaseFee != nil {
		baseFee = header.BaseFee.String()
	}
	return map[string]any{"number": header.Number.Uint64(), "hash": hexOf(header.Hash().Bytes()),
		"state_root": hexOf(header.Root.Bytes()), "base_fee": baseFee, "gas_limit": header.GasLimit,
		"gas_used": header.GasUsed, "body_tx_count": len(stored.Transactions()), "transactions": transactions,
		"system_transactions": system,
		"system_nonce_after":  map[string]uint64{hexOf(validator.Bytes()): end.GetNonce(validator)},
		"state_after_block":   accountsAt(end, addresses), "coinbase": hexOf(header.Coinbase.Bytes())}
}

func sameLogs(a, b []*types.Log) bool {
	left, _ := json.Marshal(logsOf(a))
	right, _ := json.Marshal(logsOf(b))
	return bytes.Equal(left, right)
}
