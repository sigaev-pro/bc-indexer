// Private Polygon type-path probe, compiled alongside the pinned corpus shell.
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

const typePathTracer = "extractor-private-poly-type-paths"
const pinnedCommit = "3a018e38467594508ca31ebeaf804837fb92834a"

var live = &typeRecorder{Rows: []map[string]any{}, Calls: []map[string]any{}}
var watched = []common.Address{
	common.HexToAddress(senderAddress), recipient, child,
	common.HexToAddress("0x2b5ad5c4795c026514f8317c7a215e218dccd6cf"),
	common.HexToAddress("0x3000000000000000000000000000000000000003"),
	common.HexToAddress("0x0000000000000000000000000000000000001001"),
}

// The accepted corpus backend sets no VMTrace. Install a live observer through
// the native supported registry, leaving accepted import/capture hooks intact.
func init() {
	tracers.LiveDirectory.Register(typePathTracer, func(json.RawMessage) (*tracing.Hooks, error) {
		return live.hooks(), nil
	})
}

func corpusTransactions(config *params.ChainConfig, key *ecdsa.PrivateKey) (types.Transactions, error) {
	price := big.NewInt(5_000_000_000)
	var unsigned []types.TxData
	for _, typ := range []byte{0, 1, 2, 4} {
		for _, to := range []common.Address{recipient, child} {
			nonce := uint64(len(unsigned))
			switch typ {
			case 0:
				unsigned = append(unsigned, &types.LegacyTx{Nonce: nonce, GasPrice: price, Gas: 150_000, To: &to, Value: big.NewInt(7)})
			case 1:
				unsigned = append(unsigned, &types.AccessListTx{ChainID: config.ChainID, Nonce: nonce, GasPrice: price,
					Gas: 150_000, To: &to, Value: big.NewInt(7), AccessList: types.AccessList{{Address: child, StorageKeys: []common.Hash{{}}}}})
			case 2:
				unsigned = append(unsigned, &types.DynamicFeeTx{ChainID: config.ChainID, Nonce: nonce,
					GasTipCap: big.NewInt(2_000_000_000), GasFeeCap: price, Gas: 150_000, To: &to, Value: big.NewInt(7)})
			case 4:
				authority, err := crypto.HexToECDSA(strings.Repeat("0", 63) + "2")
				if err != nil {
					return nil, err
				}
				auth, err := types.SignSetCode(authority, types.SetCodeAuthorization{
					ChainID: *uint256.MustFromBig(config.ChainID), Address: recipient, Nonce: nonce - 6})
				if err != nil {
					return nil, err
				}
				unsigned = append(unsigned, &types.SetCodeTx{ChainID: uint256.MustFromBig(config.ChainID), Nonce: nonce,
					GasTipCap: uint256.NewInt(2_000_000_000), GasFeeCap: uint256.NewInt(5_000_000_000), Gas: 150_000,
					To: to, Value: uint256.NewInt(7), AuthList: []types.SetCodeAuthorization{auth}})
			}
		}
	}
	var out types.Transactions
	for _, tx := range unsigned {
		signed, err := types.SignNewTx(key, types.LatestSigner(config), tx)
		if err != nil {
			return nil, err
		}
		out = append(out, signed)
	}
	return out, nil
}

type typeRecorder struct {
	Rows    []map[string]any
	Calls   []map[string]any
	state   tracing.StateDB
	current map[string]any
	stack   map[int]int
}

func traceState(db tracing.StateDB) map[string]any {
	out := map[string]any{}
	for _, address := range watched {
		slots := map[string]string{}
		for i := int64(0); i < 4; i++ {
			key := common.BigToHash(big.NewInt(i))
			slots[key.Hex()] = db.GetState(address, key).Hex()
		}
		out[strings.ToLower(address.Hex())] = map[string]any{"balance": db.GetBalance(address).String(),
			"nonce": db.GetNonce(address), "code": hex.EncodeToString(db.GetCode(address)), "storage": slots}
	}
	return out
}

func (r *typeRecorder) hooks() *tracing.Hooks {
	return &tracing.Hooks{
		OnTxStart: func(ctx *tracing.VMContext, tx *types.Transaction, from common.Address) {
			r.state, r.stack = ctx.StateDB, map[int]int{}
			raw, err := tx.MarshalBinary()
			if err != nil {
				panic(err)
			}
			r.current = map[string]any{"hash": tx.Hash().Hex(), "type": tx.Type(), "from": from.Hex(),
				"raw": hex.EncodeToString(raw), "before": traceState(r.state)}
		},
		OnTxEnd: func(receipt *types.Receipt, err error) {
			if r.current == nil {
				return
			}
			r.current["after"] = traceState(r.state)
			r.current["receipt"] = receipt
			if err != nil {
				r.current["error"] = err.Error()
			}
			r.Rows = append(r.Rows, r.current)
			r.current = nil
		},
		OnEnter: func(depth int, typ byte, from, to common.Address, input []byte, gas uint64, value *big.Int) {
			if r.current == nil {
				return
			}
			row := map[string]any{"transaction": r.current["hash"], "depth": depth, "type": typ,
				"from": from.Hex(), "to": to.Hex(), "input": hex.EncodeToString(input), "before": traceState(r.state)}
			r.stack[depth] = len(r.Calls)
			r.Calls = append(r.Calls, row)
		},
		OnExit: func(depth int, output []byte, gasUsed uint64, err error, reverted bool) {
			if r.current == nil {
				return
			}
			index, ok := r.stack[depth]
			if !ok {
				return
			}
			row := r.Calls[index]
			row["after"], row["reverted"], row["output"] = traceState(r.state), reverted, hex.EncodeToString(output)
			if err != nil {
				row["error"] = err.Error()
			}
			delete(r.stack, depth)
		},
	}
}

func durableRecords(db ethdb.Database, prefix string) (map[string]string, error) {
	out := map[string]string{}
	it := db.NewIterator([]byte(prefix), nil)
	defer it.Release()
	for it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	return out, it.Error()
}

func orderedProofs(items types.DerivableList, expected common.Hash) ([]map[string]any, error) {
	database := triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil)
	defer database.Close()
	tree := trie.NewEmpty(database)
	values := make([][]byte, items.Len())
	keys := make([][]byte, items.Len())
	for i := 0; i < items.Len(); i++ {
		var encoded bytes.Buffer
		items.EncodeIndex(i, &encoded)
		values[i] = append([]byte(nil), encoded.Bytes()...)
		key, err := rlp.EncodeToBytes(uint(i))
		if err != nil {
			return nil, err
		}
		keys[i] = key
		if err := tree.Update(key, values[i]); err != nil {
			return nil, err
		}
	}
	if tree.Hash() != expected {
		return nil, fmt.Errorf("ordered trie differs from imported header")
	}
	rows := []map[string]any{}
	for i, key := range keys {
		proof := memorydb.New()
		if err := tree.Prove(key, proof); err != nil {
			proof.Close()
			return nil, err
		}
		iterator := proof.NewIterator(nil, nil)
		nodes := []string{}
		for iterator.Next() {
			nodes = append(nodes, hex.EncodeToString(iterator.Value()))
		}
		err := iterator.Error()
		iterator.Release()
		proof.Close()
		if err != nil {
			return nil, err
		}
		rows = append(rows, map[string]any{"index": i, "key": hex.EncodeToString(key),
			"value": hex.EncodeToString(values[i]), "nodes": nodes})
	}
	return rows, nil
}

func accountProofs(run *nodeRun, block *types.Block) (map[string]any, error) {
	api := ethapi.NewBlockChainAPI(run.eth.APIBackend)
	rows := map[string]any{}
	for _, address := range watched {
		proof, err := api.GetProof(context.Background(), address, []string{"0x0", "0x1", "0x2", "0x3"},
			rpc.BlockNumberOrHashWithHash(block.Hash(), true))
		if err != nil {
			return nil, err
		}
		rows[strings.ToLower(address.Hex())] = proof
	}
	return rows, nil
}

func componentControls(run *nodeRun, stateSyncBlock *types.Block) (map[string]any, error) {
	key, err := senderKey()
	if err != nil {
		return nil, err
	}
	config := run.chain.Config()
	blob, err := types.SignNewTx(key, types.LatestSigner(config), &types.BlobTx{
		ChainID: uint256.MustFromBig(config.ChainID), Nonce: 8, To: recipient, Gas: 21_000,
		GasTipCap: uint256.NewInt(2_000_000_000), GasFeeCap: uint256.NewInt(5_000_000_000),
		Value: new(uint256.Int), BlobFeeCap: uint256.NewInt(1), BlobHashes: []common.Hash{{1}},
	})
	if err != nil {
		return nil, err
	}
	raw, err := blob.MarshalBinary()
	if err != nil {
		return nil, err
	}
	var decoded types.Transaction
	if err := decoded.UnmarshalBinary(raw); err != nil {
		return nil, err
	}
	if decoded.Hash() != blob.Hash() {
		return nil, fmt.Errorf("blob decode identity changed")
	}
	head := run.chain.CurrentBlock().Hash()
	errs := run.eth.TxPool().Add([]*types.Transaction{blob}, true)
	if len(errs) != 1 || !errors.Is(errs[0], core.ErrTxTypeNotSupported) {
		return nil, fmt.Errorf("expected native blob pool refusal, got %v", errs)
	}
	if run.chain.CurrentBlock().Hash() != head {
		return nil, fmt.Errorf("pool control changed native head")
	}
	blockRaw, err := rlp.EncodeToBytes(stateSyncBlock)
	if err != nil {
		return nil, err
	}
	var fields []rlp.RawValue
	if err := rlp.DecodeBytes(blockRaw, &fields); err != nil {
		return nil, err
	}
	var txs []rlp.RawValue
	if err := rlp.DecodeBytes(fields[1], &txs); err != nil {
		return nil, err
	}
	if len(txs) != 1 {
		return nil, fmt.Errorf("unknown control needs one typed body transaction")
	}
	var envelope []byte
	if err := rlp.DecodeBytes(txs[0], &envelope); err != nil {
		return nil, err
	}
	unknown := []map[string]any{}
	for typ := byte(5); typ < types.StateSyncTxType; typ++ {
		changed := append([]byte(nil), envelope...)
		changed[0] = typ
		txWire, err := rlp.EncodeToBytes(changed)
		if err != nil {
			return nil, err
		}
		fields[1], err = rlp.EncodeToBytes([]rlp.RawValue{txWire})
		if err != nil {
			return nil, err
		}
		wire, err := rlp.EncodeToBytes(fields)
		if err != nil {
			return nil, err
		}
		var block types.Block
		err = rlp.DecodeBytes(wire, &block)
		if err == nil {
			return nil, fmt.Errorf("native block decoder accepted unknown type %d", typ)
		}
		unknown = append(unknown, map[string]any{"type": typ, "error": err.Error()})
	}
	return map[string]any{"blob_raw": hex.EncodeToString(raw), "blob_hash": blob.Hash().Hex(),
		"blob_pool_error": errs[0].Error(), "head_before": head.Hex(), "head_after": run.chain.CurrentBlock().Hash().Hex(),
		"blob_body_import": "NOT_RUN", "unknown_block_decode": unknown}, nil
}

func rejectedStateSync(in inputs, bodies, directory, keySource string, blocks []*types.Block) ([]map[string]any, error) {
	key, err := signerKey(keySource)
	if err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	for _, name := range []string{"missing", "non-final", "mismatched-l1-hash", "reordered-items", "duplicate-body"} {
		row := map[string]any{"case": name}
		rows = append(rows, row)
		run, err := start(in, bodies, filepath.Join(directory, name), pinnedCommit, row)
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer run.close()
			for _, block := range blocks[:3] {
				if _, err := run.chain.InsertChain([]*types.Block{block}, false); err != nil {
					return err
				}
			}
			original := blocks[3]
			tx := original.Transactions()[0]
			body := &types.Body{}
			header := original.Header()
			switch name {
			case "missing":
			case "non-final":
				sender, err := senderKey()
				if err != nil {
					return err
				}
				normal, err := types.SignNewTx(sender, types.LatestSigner(in.genesis.Config), &types.LegacyTx{
					Nonce: 8, GasPrice: big.NewInt(5_000_000_000), Gas: 21_000, To: &recipient, Value: big.NewInt(1)})
				if err != nil {
					return err
				}
				body.Transactions = types.Transactions{tx, normal}
				header.GasUsed = 21_000
			case "duplicate-body":
				body.Transactions = types.Transactions{tx, tx}
			default:
				items := []*types.StateSyncData{}
				for _, item := range tx.GetStateSyncData() {
					copy := *item
					copy.Data = append([]byte(nil), item.Data...)
					items = append(items, &copy)
				}
				if name == "mismatched-l1-hash" {
					items[0].TxHash = common.HexToHash("0x99")
				} else {
					items[0], items[1] = items[1], items[0]
				}
				body.Transactions = types.Transactions{types.NewTx(&types.StateSyncTx{StateSyncData: items})}
			}
			header.TxHash = types.DeriveSha(body.Transactions, trie.NewStackTrie(nil))
			signature, err := crypto.Sign(bor.SealHash(header, in.genesis.Config.Bor).Bytes(), key)
			if err != nil {
				return err
			}
			copy(header.Extra[len(header.Extra)-types.ExtraSealLength:], signature)
			candidate := types.NewBlockWithHeader(header).WithBody(*body)
			before := run.chain.CurrentBlock().Hash()
			_, importErr := run.chain.InsertChain([]*types.Block{candidate}, false)
			raw, err := rlp.EncodeToBytes(candidate)
			if err != nil {
				return err
			}
			row["raw_block"] = hex.EncodeToString(raw)
			row["number"], row["hash"], row["root"], row["parent"] = candidate.NumberU64(), candidate.Hash().Hex(), candidate.Root().Hex(), candidate.ParentHash().Hex()
			proofs, err := orderedProofs(candidate.Transactions(), candidate.TxHash())
			if err != nil {
				return err
			}
			row["transaction_proofs"] = proofs
			if importErr != nil {
				row["error"] = importErr.Error()
			}
			row["head_before"], row["head_after"] = before.Hex(), run.chain.CurrentBlock().Hash().Hex()
			for label, prefix := range map[string]string{"admission": "extractor-admission-v1/", "decisions": "extractor-decision-v1/"} {
				records, err := durableRecords(run.eth.ChainDb(), prefix)
				if err != nil {
					return err
				}
				row[label] = records
			}
			if !errors.Is(importErr, core.ErrStateSyncMismatch) {
				return fmt.Errorf("%s: expected StateSync mismatch, got %v", name, importErr)
			}
			if run.chain.CurrentBlock().Hash() != before {
				return fmt.Errorf("rejected StateSync moved head")
			}
			return nil
		}()
		if err != nil {
			row["control_error"] = err.Error()
			return rows, err
		}
	}
	return rows, nil
}

func observe(directory, genesisPath, configPath, bodies, blocksPath, keySource, output string) (returned error) {
	result := map[string]any{"native_source": pinnedCommit, "public_profile_credit": false}
	defer func() {
		if returned != nil {
			result["error"] = returned.Error()
		}
		if err := saveJSON(output, result); returned == nil {
			returned = err
		}
	}()
	in, err := loadInputs(genesisPath, configPath)
	if err != nil {
		return err
	}
	blocks, err := readBlocks(blocksPath)
	if err != nil {
		return err
	}
	run, err := start(in, bodies, directory, pinnedCommit, result)
	if err != nil {
		return err
	}
	mainOpen := true
	defer func() {
		if mainOpen {
			run.close()
		}
	}()
	rows := []map[string]any{}
	for _, block := range blocks {
		live.Rows, live.Calls = []map[string]any{}, []map[string]any{}
		_, err := run.chain.InsertChain([]*types.Block{block}, false)
		if err != nil {
			return fmt.Errorf("native import %d: %w", block.NumberU64(), err)
		}
		if run.chain.CurrentBlock().Hash() != block.Hash() {
			return fmt.Errorf("native head differs from imported block")
		}
		raw, err := rlp.EncodeToBytes(block)
		if err != nil {
			return err
		}
		after, err := run.chain.StateAt(block.Root())
		if err != nil {
			return err
		}
		receipts := run.chain.GetReceiptsByHash(block.Hash())
		receiptProof, err := orderedProofs(receipts, block.ReceiptHash())
		if err != nil {
			return err
		}
		txProof, err := orderedProofs(block.Transactions(), block.TxHash())
		if err != nil {
			return err
		}
		accountProof, err := accountProofs(run, block)
		if err != nil {
			return err
		}
		rows = append(rows, map[string]any{"number": block.NumberU64(), "hash": block.Hash().Hex(),
			"parent": block.ParentHash().Hex(), "root": block.Root().Hex(), "raw_block": hex.EncodeToString(raw),
			"transactions": live.Rows, "calls": live.Calls, "receipts": receipts,
			"receipt_proofs": receiptProof, "transaction_proofs": txProof, "account_proofs": accountProof,
			"state_after": readAccounts(after, watched), "state_sync": run.chain.HeaderChain().GetStateSync()})
	}
	result["blocks"], result["head_hash"] = rows, run.chain.CurrentBlock().Hash().Hex()
	controls, err := componentControls(run, blocks[3])
	if err != nil {
		return err
	}
	result["components"] = controls
	run.count.mu.Lock()
	result["observed"], result["keys"], result["observer_errors"] = run.count.seen, run.count.keys, run.count.errs
	run.count.mu.Unlock()
	for name, prefix := range map[string]string{"admission": "extractor-admission-v1/", "decisions": "extractor-decision-v1/"} {
		records, err := durableRecords(run.eth.ChainDb(), prefix)
		if err != nil {
			return err
		}
		result[name] = records
	}
	run.close()
	mainOpen = false
	rejections, err := rejectedStateSync(in, bodies, directory+"-refusals", keySource, blocks)
	result["state_sync_refusals"] = rejections
	if err != nil {
		return err
	}
	return nil
}

func main() {
	var err error
	if len(os.Args) == 2 && os.Args[1] == "identity" {
		fmt.Println(pinnedCommit)
		return
	}
	if len(os.Args) != 6 {
		fmt.Fprintln(os.Stderr, "usage: probe INPUT_DIRECTORY BODIES_DIRECTORY KEY_SOURCE NEW_OUTPUT OBSERVATION")
		os.Exit(2)
	}
	input, bodies, keySource, output := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	genesis, config := filepath.Join(input, "genesis.json"), filepath.Join(input, "chain-config.json")
	err = generate([]string{"--genesis", genesis, "--config", config, "--keysource", keySource,
		"--bodies", bodies, "--out", output})
	if err == nil {
		err = observe(filepath.Join(output, "import-data"), genesis, config, bodies,
			filepath.Join(output, "blocks.rlp"), keySource, os.Args[5])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
