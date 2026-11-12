// This test-only probe produces the ETH native corpus observation of
// E01-03.smoke_corpus on the accepted patched Geth tree (E01-02.admission and
// E01-02.decision_bridge patch sets). Preparation copies it to
// cmd/extractor-corpus-eth.
//
// It generates corpus blocks 1 and 2 with Geth's own chain generator on the
// private fixture (test/harness/fixtures/genesis.json plus the corpus
// contracts), imports them with core.BlockChain.InsertChain on a node store
// attached with AttachDecisions and the options of the shipped node, restarts
// and imports them again, then exports native output only: blocks and
// receipts from the chain database, the native call tracer (callTracer) of
// each transaction, native state reads before and after each transaction
// (a native re-execution of the block on the parent state, whose receipts
// and final root must equal the imported ones), the state at each block
// root, and the decision bridge and admission records. It writes no expected
// value; corpus_compare.py judges the observation.
//
// Usage:
//
//	corpus run GENESIS WORKDIR OUTPUT NATIVE_COMMIT
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/tracers"
	_ "github.com/ethereum/go-ethereum/eth/tracers/native"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	extractorgeth "github.com/ethereum/go-ethereum/extractor/admission/native"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	storeID    = "e01035000c0a9e75e01035000c0a9e75"
	producerID = "geth-corpus"
	profile    = "test-only-corpus-v1"
	gwei       = 1_000_000_000
)

// The corpus accounts of ORACLE.md ("Fixtures" and "Contracts"). The sender,
// public test key 1, is already in the fixture allocation.
var (
	coinbase  = common.HexToAddress("0x2000000000000000000000000000000000000000")
	parent    = common.HexToAddress("0x3000000000000000000000000000000000000001")
	child     = common.HexToAddress("0x3000000000000000000000000000000000000002")
	recipient = common.HexToAddress("0x4000000000000000000000000000000000000001")
	sender    = common.HexToAddress("0x7e5f4552091a69125d5dfcb7b8c2659029395bdf")
	listed    = []common.Address{coinbase, parent, child, recipient, sender}
	withSlots = map[common.Address]bool{parent: true, child: true}
)

const (
	childCode  = "600160005560c160006000a160ee60005260206000fd"
	parentCode = "6001600055602060006000600034733000000000000000000000000000000000000002617530f1156001553d6002556000516003553460005260a160206000a100"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func mustHex(text string) []byte {
	data, err := hex.DecodeString(text)
	check(err)
	return data
}

// corpusInputs returns the fixture genesis with the corpus contracts and the
// generated blocks 1 (the four corpus transactions) and 2 (empty).
func corpusInputs(path string) (*core.Genesis, []*types.Block) {
	data, err := os.ReadFile(path)
	check(err)
	genesis := new(core.Genesis)
	check(json.Unmarshal(data, genesis))
	for _, address := range []common.Address{parent, child, recipient} {
		if _, ok := genesis.Alloc[address]; ok {
			panic(fmt.Sprintf("corpus address %s is already in the fixture allocation", address.Hex()))
		}
	}
	if account, ok := genesis.Alloc[sender]; !ok || account.Balance.Cmp(new(big.Int).Mul(big.NewInt(10), big.NewInt(1e18))) != 0 {
		panic("the fixture must hold the sender with 10 ETH")
	}
	genesis.Alloc[parent] = types.Account{Balance: new(big.Int), Code: mustHex(parentCode)}
	genesis.Alloc[child] = types.Account{Balance: new(big.Int), Code: mustHex(childCode)}
	key, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	check(err)
	signer := types.LatestSigner(genesis.Config)
	sign := func(inner types.TxData) *types.Transaction {
		tx, err := types.SignNewTx(key, signer, inner)
		check(err)
		return tx
	}
	price := big.NewInt(2 * gwei)
	ether := big.NewInt(1e18)
	txs := []*types.Transaction{
		// 0 transfer: legacy, 1 ETH to the absent recipient, 2 gwei.
		sign(&types.LegacyTx{Nonce: 0, To: &recipient, Value: ether, Gas: 21_000, GasPrice: price}),
		// 1 fee: dynamic fee, 1 gwei, max fee 2.2 gwei, max priority 1.5 gwei.
		sign(&types.DynamicFeeTx{ChainID: genesis.Config.ChainID, Nonce: 1, To: &recipient, Value: big.NewInt(gwei),
			Gas: 21_000, GasFeeCap: big.NewInt(2_200_000_000), GasTipCap: big.NewInt(1_500_000_000)}),
		// 2 nested-revert: 5 wei to the parent, gas 200,000.
		sign(&types.LegacyTx{Nonce: 2, To: &parent, Value: big.NewInt(5), Gas: 200_000, GasPrice: price}),
		// 3 nested-revert: 7 wei to the child directly, gas 100,000.
		sign(&types.LegacyTx{Nonce: 3, To: &child, Value: big.NewInt(7), Gas: 100_000, GasPrice: price}),
	}
	_, blocks, _ := core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), 2, func(index int, block *core.BlockGen) {
		block.SetCoinbase(coinbase)
		if index == 0 {
			for _, tx := range txs {
				block.AddTx(tx)
			}
		}
	})
	return genesis, blocks
}

func openStore(datadir string) ethdb.Database {
	chaindata := filepath.Join(datadir, "geth", "chaindata")
	kv, err := pebble.New(chaindata, 16, 16, "", false)
	check(err)
	db, err := rawdb.Open(kv, rawdb.OpenOptions{Ancient: filepath.Join(chaindata, "ancient")})
	check(err)
	return db
}

func prefixed(db ethdb.Database, prefix string) map[string]string {
	out := map[string]string{}
	iterator := db.NewIterator([]byte(prefix), nil)
	defer iterator.Release()
	for iterator.Next() {
		out[string(iterator.Key())] = string(iterator.Value())
	}
	check(iterator.Error())
	return out
}

func errorText(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

// attachedImport opens the node store the way the shipped node does
// (Preflight, NewBlockChain, AttachDecisions with Options{} and
// DecisionOptions{}), imports blocks with InsertChain, stops the chain and
// returns the import result. When export is true it also exports the native
// output before the chain stops.
func attachedImport(datadir string, genesis *core.Genesis, blocks []*types.Block, cfg extractorgeth.Config,
	export func(chain *core.BlockChain, db ethdb.Database) map[string]any) map[string]any {
	check(extractorgeth.Preflight(filepath.Join(datadir, "geth"), cfg))
	db := openStore(datadir)
	result := map[string]any{
		"admission_records_before": prefixed(db, "extractor-admission-v1/"),
		"decision_records_before":  prefixed(db, "extractor-decision-v1/"),
	}
	chain, err := core.NewBlockChain(db, genesis, ethash.NewFaker(), core.DefaultConfig())
	check(err)
	_, err = extractorgeth.AttachDecisions(chain, db, cfg, extractorgeth.Options{}, extractorgeth.DecisionOptions{})
	result["attach_error"] = errorText(err)
	if err == nil {
		index, err := chain.InsertChain(blocks)
		result["insert_index"], result["insert_error"] = index, errorText(err)
	}
	head := chain.CurrentBlock()
	result["head"] = map[string]any{"number": head.Number.Uint64(), "hash": head.Hash().Hex()}
	if export != nil {
		result["export"] = export(chain, db)
	}
	chain.Stop()
	result["admission_records_after"] = prefixed(db, "extractor-admission-v1/")
	result["decision_records_after"] = prefixed(db, "extractor-decision-v1/")
	result["markers_after"] = prefixed(db, "extractor-decision-marker-v1/")
	check(db.Close())
	return result
}

func plain(address common.Address) string {
	return strings.ToLower(strings.TrimPrefix(address.Hex(), "0x"))
}

func accounts(statedb *state.StateDB) map[string]any {
	out := map[string]any{}
	for _, address := range listed {
		row := map[string]any{
			"balance": statedb.GetBalance(address).ToBig().String(),
			"nonce":   statedb.GetNonce(address),
			"code":    hex.EncodeToString(statedb.GetCode(address)),
		}
		if withSlots[address] {
			slots := map[string]string{}
			for slot := int64(0); slot < 4; slot++ {
				value := statedb.GetState(address, common.BigToHash(big.NewInt(slot)))
				slots[fmt.Sprint(slot)] = hex.EncodeToString(value[:])
			}
			row["storage"] = slots
		}
		out[plain(address)] = row
	}
	return out
}

// hexField turns a callTracer hex string ("0x..." or absent) into lowercase
// hex without 0x.
func hexField(value any) string {
	text, _ := value.(string)
	return strings.ToLower(strings.TrimPrefix(text, "0x"))
}

// frame converts one native callTracer frame to the observation format.
func frame(raw map[string]any) map[string]any {
	value := "0"
	if text, ok := raw["value"].(string); ok {
		number, ok := new(big.Int).SetString(strings.TrimPrefix(text, "0x"), 16)
		if !ok {
			panic("callTracer value is not hex: " + text)
		}
		value = number.String()
	}
	var errText any
	if text, ok := raw["error"].(string); ok {
		errText = text
	}
	calls := []any{}
	if list, ok := raw["calls"].([]any); ok {
		for _, call := range list {
			calls = append(calls, frame(call.(map[string]any)))
		}
	}
	return map[string]any{"type": raw["type"], "from": hexField(raw["from"]), "to": hexField(raw["to"]),
		"value": value, "input": hexField(raw["input"]), "output": hexField(raw["output"]), "error": errText,
		"calls": calls}
}

func logs(list []*types.Log) []any {
	out := []any{}
	for _, log := range list {
		topics := []string{}
		for _, topic := range log.Topics {
			topics = append(topics, hex.EncodeToString(topic[:]))
		}
		out = append(out, map[string]any{"address": plain(log.Address), "topics": topics,
			"data": hex.EncodeToString(log.Data)})
	}
	return out
}

func receiptView(receipt *types.Receipt) map[string]any {
	return map[string]any{"status": receipt.Status, "gas_used": receipt.GasUsed,
		"cumulative_gas_used": receipt.CumulativeGasUsed, "effective_gas_price": receipt.EffectiveGasPrice.String(),
		"logs": logs(receipt.Logs)}
}

func txView(tx *types.Transaction, index int, signer types.Signer) map[string]any {
	from, err := types.Sender(signer, tx)
	check(err)
	row := map[string]any{"index": index, "hash": hex.EncodeToString(tx.Hash().Bytes()), "type": int(tx.Type()),
		"from": plain(from), "nonce": tx.Nonce(), "to": plain(*tx.To()), "value": tx.Value().String(),
		"gas": tx.Gas(), "data": hex.EncodeToString(tx.Data())}
	if tx.Type() == types.DynamicFeeTxType {
		row["max_fee_per_gas"], row["max_priority_fee_per_gas"] = tx.GasFeeCap().String(), tx.GasTipCap().String()
	} else {
		row["gas_price"] = tx.GasPrice().String()
	}
	return row
}

// exportBlocks reads blocks 1..n of the canonical chain from the native
// database and re-executes each on its parent state with the native call
// tracer.
func exportBlocks(chain *core.BlockChain, db ethdb.Database, count int) map[string]any {
	config := chain.Config()
	out := []any{}
	checks := []any{}
	for number := uint64(1); number <= uint64(count); number++ {
		hash := rawdb.ReadCanonicalHash(db, number)
		block := rawdb.ReadBlock(db, hash, number)
		if block == nil {
			panic(fmt.Sprintf("block %d is not in the native database", number))
		}
		header := block.Header()
		parentHeader := chain.GetHeaderByHash(block.ParentHash())
		stored := rawdb.ReadReceipts(db, hash, number, block.Time(), config)
		if len(stored) != len(block.Transactions()) {
			panic(fmt.Sprintf("block %d has %d receipts for %d transactions", number, len(stored), len(block.Transactions())))
		}
		statedb, err := chain.StateAt(parentHeader.Root, parentHeader.Number, parentHeader.Time)
		check(err)
		signer := types.MakeSigner(config, header.Number, header.Time)
		pool := core.NewGasPool(header.GasLimit)
		var cumulative uint64
		transactions := []any{}
		reexecuted := []any{}
		for index, tx := range block.Transactions() {
			before := accounts(statedb)
			tracer, err := tracers.DefaultDirectory.New("callTracer", &tracers.Context{BlockHash: hash,
				BlockNumber: header.Number, TxIndex: index, TxHash: tx.Hash()}, nil, config)
			check(err)
			blockContext := core.NewEVMBlockContext(header, chain, nil)
			evm := vm.NewEVM(blockContext, state.NewHookedState(statedb, tracer.Hooks), config, vm.Config{Tracer: tracer.Hooks})
			message, err := core.TransactionToMessage(tx, signer, header.BaseFee)
			check(err)
			statedb.SetTxContext(tx.Hash(), index, uint32(index+1))
			receipt, _, err := core.ApplyTransactionWithEVM(context.Background(), message, pool, statedb, header.Number,
				hash, header.Time, tx, evm)
			check(err)
			evm.Release()
			cumulative = receipt.CumulativeGasUsed
			result, err := tracer.GetResult()
			check(err)
			var raw map[string]any
			check(json.Unmarshal(result, &raw))
			row := txView(tx, index, signer)
			row["receipt"] = receiptView(stored[index])
			row["frame"] = frame(raw)
			row["state_before"] = before
			row["state_after"] = accounts(statedb)
			transactions = append(transactions, row)
			// ApplyTransactionWithEVM leaves EffectiveGasPrice unset in this
			// Geth version; the re-execution computes it from the
			// transaction and the base fee (EIP-1559) and compares it with
			// the value ReadReceipts derives for the imported receipt.
			tip, err := tx.EffectiveGasTip(header.BaseFee)
			check(err)
			receipt.EffectiveGasPrice = new(big.Int).Add(tip, header.BaseFee)
			again, imported := receiptView(receipt), receiptView(stored[index])
			again["bloom"], imported["bloom"] = hex.EncodeToString(receipt.Bloom[:]), hex.EncodeToString(stored[index].Bloom[:])
			again["type"], imported["type"] = receipt.Type, stored[index].Type
			reexecuted = append(reexecuted, map[string]any{"index": index,
				"receipt_equal": fmt.Sprint(again) == fmt.Sprint(imported)})
		}
		// The block reward, outside any transaction, then the root.
		chain.Engine().Finalize(chain, header, statedb, block.Body())
		root := statedb.IntermediateRoot(config.Rules(header.Number, false, header.Time))
		atRoot, err := chain.StateAt(header.Root, header.Number, header.Time)
		check(err)
		var baseFee any
		if header.BaseFee != nil {
			baseFee = header.BaseFee.String()
		}
		out = append(out, map[string]any{
			"number": number, "hash": hex.EncodeToString(hash[:]), "state_root": hex.EncodeToString(header.Root[:]),
			"base_fee": baseFee, "gas_limit": header.GasLimit, "gas_used": header.GasUsed,
			"body_tx_count": len(block.Transactions()), "transactions": transactions,
			"system_transactions": []any{}, "state_sync": []any{}, "state_after_block": accounts(atRoot),
		})
		checks = append(checks, map[string]any{"number": number, "receipts": reexecuted,
			"cumulative_gas_used": cumulative, "header_gas_used": header.GasUsed,
			"root_equal": root == header.Root, "reexecuted_root": root.Hex(), "header_root": header.Root.Hex()})
	}
	return map[string]any{"blocks": out, "reexecution": checks}
}

type intentRow struct {
	Kind    string `json:"kind"`
	NewHead struct {
		Number uint64 `json:"number"`
		Hash   string `json:"hash"`
	} `json:"new_head"`
	Members []struct {
		Block string `json:"block"`
	} `json:"members"`
}

// decisions derives the NATIVE_DECIDED decisions from the retained decision
// records: one row per sequence with an intent and a decided record, naming
// the chain number of its single member (the written block; every decision
// of this corpus import has one member). It also returns the keys of
// intents without a decided record, which the driver refuses.
func decisions(records map[string]string, numbers map[string]uint64) ([]any, []string) {
	type row struct {
		key   string
		block uint64
		kind  string
	}
	rows := []row{}
	undecided := []string{}
	for key, value := range records {
		if !strings.HasSuffix(key, "/intent") {
			continue
		}
		base := strings.TrimSuffix(key, "/intent")
		if _, ok := records[base+"/decided"]; !ok {
			undecided = append(undecided, key)
			continue
		}
		var intent intentRow
		check(json.Unmarshal([]byte(value), &intent))
		if len(intent.Members) != 1 {
			panic(fmt.Sprintf("decided intent %s has %d members, want 1", key, len(intent.Members)))
		}
		number, ok := numbers[strings.ToLower(strings.TrimPrefix(intent.Members[0].Block, "0x"))]
		if !ok {
			panic("decided intent of an unknown block: " + key)
		}
		rows = append(rows, row{key, number, intent.Kind})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })
	out := []any{}
	for _, r := range rows {
		out = append(out, map[string]any{"block": r.block, "kind": r.kind})
	}
	sort.Strings(undecided)
	return out, undecided
}

// counted returns the sum of the "next" values of every counter record under
// a record set: the number of sequences ever started.
func counted(records map[string]string) int {
	total := 0
	for key, value := range records {
		if strings.HasSuffix(key, "/counter") {
			var counter struct {
				Next int `json:"next"`
			}
			check(json.Unmarshal([]byte(value), &counter))
			total += counter.Next
		}
	}
	return total
}

func run(genesisPath, work, output, commit string) {
	check(os.Mkdir(work, 0o700))
	genesis, blocks := corpusInputs(genesisPath)
	numbers := map[string]uint64{}
	inputs := []any{}
	for _, block := range blocks {
		numbers[hex.EncodeToString(block.Hash().Bytes())] = block.NumberU64()
		data, err := rlp.EncodeToBytes(block)
		check(err)
		inputs = append(inputs, map[string]any{"number": block.NumberU64(), "hash": block.Hash().Hex(),
			"rlp_sha256": fmt.Sprintf("%x", sha256.Sum256(data))})
	}
	cfg := extractorgeth.Config{StoreID: storeID, ProducerID: producerID, ChainID: genesis.Config.ChainID.String(),
		GenesisHash: genesis.ToBlock().Hash(), NativeCommit: commit, Profile: profile}
	datadir := filepath.Join(work, "node")
	check(os.MkdirAll(filepath.Join(datadir, "geth"), 0o700))
	live := attachedImport(datadir, genesis, blocks, cfg, nil)
	restart := attachedImport(datadir, genesis, blocks, cfg, func(chain *core.BlockChain, db ethdb.Database) map[string]any {
		return exportBlocks(chain, db, len(blocks))
	})
	export := restart["export"].(map[string]any)
	delete(restart, "export")
	liveRecords := live["decision_records_after"].(map[string]string)
	decided, undecided := decisions(liveRecords, numbers)
	observation := map[string]any{
		"schema_version": 1, "kind": "native-corpus-observation", "family": "eth",
		"chain_id": genesis.Config.ChainID.Int64(),
		"blocks":   export["blocks"],
		// NATIVE_DECIDED records of the live import.
		"decisions": decided,
		// After the restart and the re-import of the same blocks.
		"reimport": map[string]any{
			"new_observations": counted(restart["admission_records_after"].(map[string]string)) -
				counted(restart["admission_records_before"].(map[string]string)),
			"new_decisions": counted(restart["decision_records_after"].(map[string]string)) -
				counted(restart["decision_records_before"].(map[string]string)),
		},
		"native": map[string]any{
			"native_commit": commit, "genesis_hash": genesis.ToBlock().Hash().Hex(), "generated": inputs,
			"live_import": live, "restart_import": restart, "reexecution": export["reexecution"],
			"undecided_intents": undecided,
		},
	}
	data, err := json.MarshalIndent(observation, "", " ")
	check(err)
	check(os.WriteFile(output, append(data, '\n'), 0o600))
	fmt.Println("Recorded the ETH corpus observation; the oracle comparison is separate.")
}

func main() {
	if len(os.Args) == 6 && os.Args[1] == "run" {
		run(os.Args[2], os.Args[3], os.Args[4], os.Args[5])
		return
	}
	fmt.Fprintln(os.Stderr, "usage: corpus run GENESIS WORKDIR OUTPUT NATIVE_COMMIT")
	os.Exit(2)
}
