// Native ETH type-path fixture. Installed as cmd/extractor-type-paths-eth
// in the accepted admission/decision tree; never changes shipped code.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	extractorgeth "github.com/ethereum/go-ethereum/extractor/admission/native"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/holiman/uint256"
)

const nativeCommit = "1df30b18d9ed35ee9d28ffd4648442c2204554ad"

var (
	senderKey, _    = crypto.HexToECDSA(strings.Repeat("0", 63) + "1")
	authorityKey, _ = crypto.HexToECDSA(strings.Repeat("0", 63) + "2")
	sender          = crypto.PubkeyToAddress(senderKey.PublicKey)
	authority       = crypto.PubkeyToAddress(authorityKey.PublicKey)
	recipient       = common.HexToAddress("0x4000000000000000000000000000000000000001")
	reverter        = common.HexToAddress("0x3000000000000000000000000000000000000002")
	delegate        = common.HexToAddress("0x3000000000000000000000000000000000000004")
	coinbase        = common.HexToAddress("0x2000000000000000000000000000000000000000")
	accounts        = []common.Address{sender, authority, recipient, reverter, delegate, coinbase}
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}
func errText(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}
func hexBytes(value []byte) string { return hex.EncodeToString(value) }

func genesis() *core.Genesis {
	config := *params.MergedTestChainConfig
	config.ChainID = big.NewInt(31337)
	config.BogotaTime, config.UBTTime = nil, nil
	alloc := types.GenesisAlloc{
		sender:                           {Balance: new(big.Int).Mul(big.NewInt(100), big.NewInt(params.Ether))},
		reverter:                         {Balance: new(big.Int), Code: common.FromHex("600160005560006000fd")},
		delegate:                         {Balance: new(big.Int), Code: common.FromHex("00")},
		params.BeaconRootsAddress:        {Balance: new(big.Int), Code: params.BeaconRootsCode},
		params.HistoryStorageAddress:     {Balance: new(big.Int), Code: params.HistoryStorageCode},
		params.WithdrawalQueueAddress:    {Balance: new(big.Int), Code: params.WithdrawalQueueCode},
		params.ConsolidationQueueAddress: {Balance: new(big.Int), Code: params.ConsolidationQueueCode},
	}
	return &core.Genesis{Config: &config, Alloc: alloc, BaseFee: big.NewInt(params.InitialBaseFee),
		Difficulty: big.NewInt(0), GasLimit: 10_000_000}
}

func transaction(kind byte, nonce, authNonce uint64, target common.Address) *types.Transaction {
	price := big.NewInt(2_000_000_000)
	access := types.AccessList{{Address: target, StorageKeys: []common.Hash{{}, {}}}, {Address: target, StorageKeys: []common.Hash{}}}
	var inner types.TxData
	switch kind {
	case 0:
		inner = &types.LegacyTx{Nonce: nonce, To: &target, Value: big.NewInt(1), Gas: 150000, GasPrice: price}
	case 1:
		inner = &types.AccessListTx{ChainID: big.NewInt(31337), Nonce: nonce, To: &target, Value: big.NewInt(1),
			Gas: 150000, GasPrice: price, AccessList: access}
	case 2:
		inner = &types.DynamicFeeTx{ChainID: big.NewInt(31337), Nonce: nonce, To: &target, Value: big.NewInt(1),
			Gas: 150000, GasFeeCap: price, GasTipCap: big.NewInt(1_000_000_000), AccessList: access}
	case 3:
		inner = &types.BlobTx{ChainID: uint256.NewInt(31337), Nonce: nonce, To: target, Value: uint256.NewInt(1),
			Gas: 150000, GasFeeCap: uint256.NewInt(2_000_000_000), GasTipCap: uint256.NewInt(1_000_000_000),
			BlobFeeCap: uint256.NewInt(1_000_000_000), BlobHashes: []common.Hash{{1}}, AccessList: access}
	case 4:
		auth, err := types.SignSetCode(authorityKey, types.SetCodeAuthorization{
			ChainID: *uint256.NewInt(31337), Address: delegate, Nonce: authNonce})
		check(err)
		inner = &types.SetCodeTx{ChainID: uint256.NewInt(31337), Nonce: nonce, To: target, Value: uint256.NewInt(1),
			Gas: 150000, GasFeeCap: uint256.NewInt(2_000_000_000), GasTipCap: uint256.NewInt(1_000_000_000),
			AccessList: access, AuthList: []types.SetCodeAuthorization{auth}}
	default:
		panic("unsupported fixture type")
	}
	tx, err := types.SignNewTx(senderKey, types.NewPragueSigner(big.NewInt(31337)), inner)
	check(err)
	return tx
}

func stateView(state tracing.StateDB) map[string]any {
	out := map[string]any{}
	for _, address := range accounts {
		out[strings.ToLower(address.Hex())] = map[string]any{"balance": state.GetBalance(address).ToBig().String(),
			"nonce": state.GetNonce(address), "code": hexBytes(state.GetCode(address)),
			"slot0": state.GetState(address, common.Hash{}).Hex()}
	}
	return out
}

type recorder struct {
	rows    []map[string]any
	systems []map[string]any
	active  map[string]any
	state   tracing.StateDB
	block   uint64
	system  bool
}

func (r *recorder) hooks() *tracing.Hooks {
	return &tracing.Hooks{
		OnTxStart: func(ctx *tracing.VMContext, tx *types.Transaction, from common.Address) {
			if r.active != nil || r.system {
				panic("overlapping transaction hooks")
			}
			r.state, r.block = ctx.StateDB, ctx.BlockNumber.Uint64()
			wire, err := tx.MarshalBinary()
			check(err)
			fields, err := tx.MarshalJSON()
			check(err)
			var source any
			check(json.Unmarshal(fields, &source))
			r.active = map[string]any{"block": r.block, "hash": tx.Hash().Hex(), "type": tx.Type(),
				"nonce": tx.Nonce(), "from": strings.ToLower(from.Hex()), "wire": hexBytes(wire),
				"fields": source, "before": stateView(ctx.StateDB)}
		},
		OnTxEnd: func(receipt *types.Receipt, err error) {
			if r.active == nil {
				panic("transaction end without start")
			}
			r.active["after"], r.active["execution_error"] = stateView(r.state), errText(err)
			if receipt != nil {
				r.active["status"], r.active["gas_used"] = receipt.Status, receipt.GasUsed
			}
			r.rows = append(r.rows, r.active)
			r.active = nil
		},
		OnSystemCallStartV2: func(ctx *tracing.VMContext) {
			if r.active != nil || r.system {
				panic("overlapping system hooks")
			}
			r.system, r.block = true, ctx.BlockNumber.Uint64()
		},
		OnSystemCallEnd: func() {
			if !r.system {
				panic("system end without start")
			}
			r.system = false
		},
		OnEnter: func(depth int, typ byte, from, to common.Address, input []byte, gas uint64, value *big.Int) {
			if r.system && depth == 0 {
				r.systems = append(r.systems, map[string]any{
					"block": r.block, "from": strings.ToLower(from.Hex()), "to": strings.ToLower(to.Hex())})
			}
		},
	}
}

func store(path string) ethdb.Database {
	kv, err := pebble.New(path, 16, 16, "", false)
	check(err)
	db, err := rawdb.Open(kv, rawdb.OpenOptions{Ancient: filepath.Join(path, "ancient")})
	check(err)
	return db
}

func records(db ethdb.Database, prefix string) map[string]string {
	out := map[string]string{}
	it := db.NewIterator([]byte(prefix), nil)
	defer it.Release()
	for it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	check(it.Error())
	return out
}

func importCase(work string, spec *core.Genesis, blocks []*types.Block, label string) map[string]any {
	db := store(filepath.Join(work, label, "chaindata"))
	defer db.Close()
	actual := &recorder{rows: []map[string]any{}, systems: []map[string]any{}}
	options := core.DefaultConfig()
	options.VmConfig = vm.Config{Tracer: actual.hooks()}
	chain, err := core.NewBlockChain(db, spec, beacon.New(ethash.NewFaker()), options)
	check(err)
	defer chain.Stop()
	cfg := extractorgeth.Config{StoreID: "e0105e71000000000000000000000001", ProducerID: "type-paths-eth",
		ChainID: "31337", GenesisHash: spec.ToBlock().Hash(), NativeCommit: nativeCommit, Profile: "private-type-paths-osaka-v1"}
	_, attachErr := extractorgeth.AttachDecisions(chain, db, cfg, extractorgeth.Options{}, extractorgeth.DecisionOptions{})
	result := map[string]any{"attach_error": errText(attachErr)}
	if attachErr != nil {
		return result
	}
	index, importErr := chain.InsertChain(blocks)
	result["import_index"], result["import_error"] = index, errText(importErr)
	result["head"] = chain.CurrentBlock().Number.Uint64()
	result["head_hash"] = chain.CurrentBlock().Hash().Hex()
	result["transactions"], result["system_calls"] = actual.rows, actual.systems
	result["admission_records"] = records(db, "extractor-admission-v1/")
	result["decision_records"] = records(db, "extractor-decision-v1/")
	if importErr != nil {
		return result
	}
	views := []map[string]any{}
	replayed := &recorder{rows: []map[string]any{}, systems: []map[string]any{}}
	for _, block := range blocks {
		header := block.Header()
		canonical := rawdb.ReadCanonicalHash(db, block.NumberU64())
		stored := rawdb.ReadBlock(db, canonical, block.NumberU64())
		if stored == nil {
			panic("imported block is not canonical")
		}
		originalBytes, err := rlp.EncodeToBytes(block)
		check(err)
		storedBytes, err := rlp.EncodeToBytes(stored)
		check(err)
		receipts := rawdb.ReadReceipts(db, block.Hash(), block.NumberU64(), block.Time(), spec.Config)
		parent := chain.GetHeaderByHash(block.ParentHash())
		state, err := chain.StateAt(parent.Root, parent.Number, parent.Time)
		check(err)
		processed, replayErr := core.NewStateProcessor(chain).Process(context.Background(), block, state, nil, nil,
			vm.Config{Tracer: replayed.hooks()}, nil)
		view := map[string]any{"number": block.NumberU64(), "hash": block.Hash().Hex(), "body_equal": bytes.Equal(originalBytes, storedBytes),
			"replay_error": errText(replayErr), "raw_block": hexBytes(storedBytes), "receipt_count": len(receipts)}
		if replayErr == nil {
			root := state.IntermediateRoot(spec.Config.Rules(header.Number, header.Difficulty.Sign() == 0, header.Time))
			view["root_equal"], view["root"], view["header_root"] = root == header.Root, root.Hex(), header.Root.Hex()
			view["gas_equal"] = processed.GasUsed == header.GasUsed
			rows := []map[string]any{}
			for i, receipt := range receipts {
				wire, err := receipt.MarshalBinary()
				check(err)
				replayWire, err := processed.Receipts[i].MarshalBinary()
				check(err)
				txWire, err := block.Transactions()[i].MarshalBinary()
				check(err)
				row := map[string]any{"index": i, "type": receipt.Type, "status": receipt.Status, "gas_used": receipt.GasUsed,
					"effective_gas_price": receipt.EffectiveGasPrice.String(), "wire": hexBytes(wire),
					"transaction_wire": hexBytes(txWire), "receipt_equal": bytes.Equal(wire, replayWire),
					"blob_gas_used": receipt.BlobGasUsed}
				if receipt.BlobGasPrice != nil {
					row["blob_gas_price"] = receipt.BlobGasPrice.String()
				}
				rows = append(rows, row)
			}
			view["receipts"] = rows
		}
		views = append(views, view)
	}
	result["blocks"] = views
	result["replay_transactions_equal"] = reflect.DeepEqual(actual.rows, replayed.rows)
	result["replay_system_calls_equal"] = reflect.DeepEqual(actual.systems, replayed.systems)
	result["hooks_closed"] = actual.active == nil && !actual.system && replayed.active == nil && !replayed.system
	return result
}

func successAndRevert(work string) map[string]any {
	spec := genesis()
	_, blocks, _ := core.GenerateChainWithGenesis(spec, beacon.New(ethash.NewFaker()), 2, func(index int, gen *core.BlockGen) {
		gen.SetCoinbase(coinbase)
		gen.SetParentBeaconRoot(common.Hash{byte(index + 1)})
		target := recipient
		if index == 1 {
			target = reverter
		}
		for kind := byte(0); kind <= 4; kind++ {
			gen.AddTx(transaction(kind, uint64(index*5)+uint64(kind), uint64(index), target))
		}
	})
	out := importCase(work, spec, blocks, "all-types")
	out["genesis"] = spec
	return out
}

func forkCases(work string) []map[string]any {
	out := []map[string]any{}
	for kind := byte(1); kind <= 4; kind++ {
		spec := genesis()
		c := spec.Config
		// Each configuration has exactly one selected activation boundary.
		c.OsakaTime, c.BogotaTime, c.UBTTime = nil, nil, nil
		switch kind {
		case 1:
			c.BerlinBlock = big.NewInt(2)
			c.LondonBlock = nil
			c.ArrowGlacierBlock, c.GrayGlacierBlock, c.MergeNetsplitBlock = nil, nil, nil
			c.ShanghaiTime, c.CancunTime, c.PragueTime = nil, nil, nil
		case 2:
			c.LondonBlock = big.NewInt(2)
			c.ArrowGlacierBlock, c.GrayGlacierBlock = nil, nil
			c.MergeNetsplitBlock = nil
			c.ShanghaiTime, c.CancunTime, c.PragueTime = nil, nil, nil
		case 3:
			v := uint64(20)
			c.CancunTime = &v
			c.PragueTime = nil
		case 4:
			v := uint64(20)
			c.PragueTime = &v
		}
		if kind <= 2 {
			spec.BaseFee = nil
			// Berlin/London precede the merge. PoS validation always applies
			// EIP-1559 header rules and cannot model their pre-activation blocks.
			c.TerminalTotalDifficulty = nil
			spec.Difficulty = new(big.Int).Set(params.MinimumDifficulty)
		}
		tx := transaction(kind, 0, 0, recipient)
		_, blocks, _ := core.GenerateChainWithGenesis(spec, beacon.New(ethash.NewFaker()), 2, func(i int, gen *core.BlockGen) {
			gen.SetCoinbase(coinbase)
			if i == 1 {
				gen.AddTx(tx)
			}
		})
		good := importCase(work, spec, blocks, fmt.Sprintf("fork-%d-good", kind))
		body := blocks[0].Body()
		body.Transactions = []*types.Transaction{tx}
		bad := types.NewBlock(blocks[0].Header(), body, nil, trie.NewStackTrie(nil))
		refused := importCase(work, spec, []*types.Block{bad}, fmt.Sprintf("fork-%d-before", kind))
		_, signerErr := core.TransactionToMessage(tx, types.MakeSigner(c, blocks[0].Number(), blocks[0].Time()), blocks[0].BaseFee())
		out = append(out, map[string]any{"type": kind, "before_number": blocks[0].NumberU64(), "before_time": blocks[0].Time(),
			"at_number": blocks[1].NumberU64(), "at_time": blocks[1].Time(), "accepted": good, "rejected": refused,
			"signer_error": errText(signerErr), "signer_unsupported": errors.Is(signerErr, types.ErrTxTypeNotSupported)})
	}
	return out
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: typepaths run WORKDIR OUTPUT")
		os.Exit(2)
	}
	check(os.Mkdir(os.Args[2], 0700))
	unknown := []byte{0x7e, 0xc0}
	var tx types.Transaction
	err := tx.UnmarshalBinary(unknown)
	out := map[string]any{"schema_version": 1, "kind": "native-type-paths-eth", "native_commit": nativeCommit,
		"private_profile": "private-type-paths-osaka-v1", "chain_id": 31337,
		"main": successAndRevert(os.Args[2]), "forks": forkCases(os.Args[2]),
		"unknown": map[string]any{"wire": hexBytes(unknown), "error": errText(err), "unsupported": errors.Is(err, types.ErrTxTypeNotSupported)}}
	data, err := json.MarshalIndent(out, "", " ")
	check(err)
	check(os.WriteFile(os.Args[3], append(data, '\n'), 0600))
	fmt.Println("Recorded private native type-path facts; the independent judge runs separately.")
}
