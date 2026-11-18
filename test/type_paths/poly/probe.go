// Private Polygon type-path probe, compiled alongside the pinned corpus shell.
package main

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
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

func observe(directory, genesisPath, configPath, bodies, blocksPath, output string) (returned error) {
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
	defer run.close()
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
		rows = append(rows, map[string]any{"number": block.NumberU64(), "hash": block.Hash().Hex(),
			"parent": block.ParentHash().Hex(), "root": block.Root().Hex(), "raw_block": hex.EncodeToString(raw),
			"transactions": live.Rows, "calls": live.Calls, "receipts": run.chain.GetReceiptsByHash(block.Hash()),
			"state_after": readAccounts(after, watched), "state_sync": run.chain.HeaderChain().GetStateSync()})
	}
	result["blocks"], result["head_hash"] = rows, run.chain.CurrentBlock().Hash().Hex()
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
			filepath.Join(output, "blocks.rlp"), os.Args[5])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
