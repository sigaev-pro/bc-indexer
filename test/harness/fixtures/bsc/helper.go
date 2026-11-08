// This BSC-only tool prepares inputs and observes an imported database.
// It never reads or generates the independent oracle.
package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/parlia"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/rlp"
)

const validator = "0x03735c2ed70a56cd221e0024eb4bf90243c9d6e9"

func check(err error) {
	if err != nil {
		panic(err)
	}
}
func require(ok bool, text string) {
	if !ok {
		panic(text)
	}
}
func address(value common.Address) string { return strings.ToLower(value.Hex()) }
func save(path string, value interface{}) {
	data, err := json.MarshalIndent(value, "", "  ")
	check(err)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	check(err)
	_, err = file.Write(append(data, '\n'))
	check(err)
	check(file.Sync())
	check(file.Close())
}
func key(path string) *keystore.Key {
	data, err := os.ReadFile(path)
	check(err)
	value, err := keystore.DecryptKey(data, "")
	check(err)
	require(address(crypto.PubkeyToAddress(value.PrivateKey.PublicKey)) == validator && address(value.Address) == validator,
		"Public validator identity mismatch")
	return value
}

func generate(genesisPath, keyPath, output string) {
	data, err := os.ReadFile(genesisPath)
	check(err)
	var genesis core.Genesis
	check(json.Unmarshal(data, &genesis))
	require(genesis.Config.ChainID.Cmp(big.NewInt(714)) == 0 && genesis.Config.Parlia != nil, "Wrong private Parlia config")
	validatorKey := key(keyPath)
	db := rawdb.NewMemoryDatabase()
	defer db.Close()
	engine := parlia.New(genesis.Config, db, nil, genesis.ToBlock().Hash())
	defer engine.Close()
	engine.Authorize(validatorKey.Address,
		func(account accounts.Account, mime string, data []byte) ([]byte, error) {
			require(account.Address == validatorKey.Address && mime == accounts.MimetypeParlia, "Unexpected header signer")
			return crypto.Sign(crypto.Keccak256(data), validatorKey.PrivateKey)
		},
		func(account accounts.Account, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error) {
			require(account.Address == validatorKey.Address && chainID.Cmp(big.NewInt(714)) == 0, "Unexpected transaction signer")
			return types.SignTx(tx, types.LatestSignerForChainID(chainID), validatorKey.PrivateKey)
		})
	cfg := core.DefaultConfig()
	cfg.ArchiveMode = true
	cfg.Preimages = true
	cfg.SnapshotLimit = 0
	chain, err := core.NewBlockChain(db, &genesis, engine, cfg)
	check(err)
	defer chain.Stop()
	sender, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	check(err)
	input, err := os.OpenFile(filepath.Join(output, "blocks.rlp"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	check(err)
	full, err := os.OpenFile(filepath.Join(output, "full-chain.rlp"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	check(err)
	check(rlp.Encode(full, genesis.ToBlock()))
	rows := []map[string]interface{}{}
	for number := uint64(1); number <= 2; number++ {
		parent := chain.CurrentBlock()
		header := &types.Header{ParentHash: parent.Hash(), Number: new(big.Int).SetUint64(number),
			GasLimit: parent.GasLimit, Time: parent.Time + 3, Difficulty: big.NewInt(1)}
		check(engine.Prepare(chain, header))
		state, err := chain.StateAt(parent)
		check(err)
		body := &types.Body{}
		receipts := []*types.Receipt{}
		if number == 1 {
			tx, err := types.SignTx(types.NewTransaction(0, common.HexToAddress("0x1000000000000000000000000000000000000000"),
				big.NewInt(1000000000000000000), 21000, big.NewInt(5000000000), nil), types.MakeSigner(genesis.Config, header.Number, header.Time), sender)
			check(err)
			pool := core.NewGasPool(header.GasLimit)
			evm := vm.NewEVM(core.NewEVMBlockContext(header, chain, &header.Coinbase), state, genesis.Config, vm.Config{})
			state.SetTxContext(tx.Hash(), 0)
			receipt, err := core.ApplyTransaction(evm, pool, state, header, tx, core.NewReceiptBloomGenerator())
			check(err)
			header.GasUsed = pool.Used()
			body.Transactions = append(body.Transactions, tx)
			receipts = append(receipts, receipt)
		}
		block, _, err := core.AssembleBlock(engine, chain, header, state, body, receipts)
		check(err)
		result := make(chan *types.Block, 1)
		stop := make(chan struct{})
		check(engine.Seal(chain, block, result, stop))
		select {
		case block = <-result:
		case <-time.After(30 * time.Second):
			close(stop)
			panic("Native seal deadline")
		}
		close(stop)
		require(block != nil, "Missing native sealed block")
		count, err := chain.InsertChain(types.Blocks{block})
		check(err)
		require(count == 1, "Native block insertion incomplete")
		check(rlp.Encode(input, block))
		check(rlp.Encode(full, block))
		rows = append(rows, map[string]interface{}{"number": number, "hash": block.Hash().Hex(), "state_root": block.Root().Hex(),
			"timestamp": block.Time(), "transactions": len(block.Transactions()), "gas_used": block.GasUsed()})
	}
	check(input.Sync())
	check(input.Close())
	check(full.Sync())
	check(full.Close())
	save(filepath.Join(output, "generated.json"), map[string]interface{}{"genesis_hash": genesis.ToBlock().Hash().Hex(), "config": genesis.Config, "blocks": rows})
	fmt.Println("Prepared two genuinely sealed and validated private Parlia blocks; oracle not read.")
}

func observe(datadir, output string) {
	path := filepath.Join(datadir, "geth", "chaindata")
	var kv ethdb.KeyValueStore
	var err error
	switch rawdb.PreexistingDatabase(path) {
	case "pebble":
		kv, err = pebble.New(path, 32, 16, "harness", true)
	case "leveldb":
		kv, err = leveldb.New(path, 32, 16, "harness", true)
	default:
		panic("Expected an existing native database")
	}
	check(err)
	require(rawdb.ReadLegacyOffset(kv) == 0, "Legacy DB layout is not this fresh fixture")
	db, err := rawdb.Open(kv, rawdb.OpenOptions{Ancient: filepath.Join(path, "ancient"), ReadOnly: true})
	check(err)
	defer db.Close()
	genesisHash := rawdb.ReadCanonicalHash(db, 0)
	require(genesisHash != (common.Hash{}), "Missing genesis")
	config := rawdb.ReadChainConfig(db, genesisHash)
	require(config != nil, "Missing chain config")
	head := rawdb.ReadHeadHeaderHash(db)
	height, present := rawdb.ReadHeaderNumber(db, head)
	require(present && height == 2, "Wrong imported head")
	rows := []map[string]interface{}{}
	for number := uint64(1); number <= 2; number++ {
		hash := rawdb.ReadCanonicalHash(db, number)
		block := rawdb.ReadBlock(db, hash, number)
		require(hash != (common.Hash{}) && block != nil && block.Hash() == hash && block.NumberU64() == number, "Missing/inconsistent canonical block")
		require(len(rawdb.ReadReceiptsRLP(db, hash, number)) > 0, "Missing receipt storage")
		receipts := rawdb.ReadReceipts(db, hash, number, block.Time(), config)
		require(receipts != nil && len(receipts) == len(block.Transactions()), "Incomplete native receipts")
		txs := []map[string]interface{}{}
		rxs := []map[string]interface{}{}
		for index, tx := range block.Transactions() {
			from, err := types.Sender(types.MakeSigner(config, block.Number(), block.Time()), tx)
			check(err)
			require(tx.To() != nil, "Unexpected creation")
			txs = append(txs, map[string]interface{}{"hash": tx.Hash().Hex(), "index": index, "type": tx.Type(), "from": address(from),
				"to": address(*tx.To()), "nonce": tx.Nonce(), "gas": fmt.Sprint(tx.Gas()), "gas_price": tx.GasPrice().String(), "value": tx.Value().String(), "input": hexutil.Encode(tx.Data())})
			rx := receipts[index]
			if rx.Logs == nil {
				rx.Logs = []*types.Log{}
			}
			rxs = append(rxs, map[string]interface{}{"transaction_hash": rx.TxHash.Hex(), "transaction_index": rx.TransactionIndex,
				"block_hash": rx.BlockHash.Hex(), "block_number": rx.BlockNumber.Uint64(), "status": rx.Status, "gas_used": rx.GasUsed,
				"cumulative_gas_used": rx.CumulativeGasUsed, "logs": rx.Logs})
		}
		var baseFee interface{}
		if block.BaseFee() != nil {
			baseFee = block.BaseFee().String()
		}
		rows = append(rows, map[string]interface{}{"number": number, "hash": hash.Hex(), "timestamp": block.Time(), "parent_hash": block.ParentHash().Hex(),
			"state_root": block.Root().Hex(), "coinbase": address(block.Coinbase()), "difficulty": block.Difficulty().String(),
			"gas_used": block.GasUsed(), "base_fee": baseFee, "withdrawals": len(block.Withdrawals()), "transactions": txs, "receipts": rxs})
	}
	save(filepath.Join(output, "observation.json"), map[string]interface{}{"schema_version": 1, "chain_id": config.ChainID,
		"genesis_hash": genesisHash.Hex(), "head_number": height, "head_hash": head.Hex(), "blocks": rows})
	save(filepath.Join(output, "native-config.json"), config)
}

func main() {
	require(len(os.Args) >= 2, "Expected verify-key, generate or observe")
	switch os.Args[1] {
	case "verify-key":
		require(len(os.Args) == 3, "Expected key path")
		fmt.Println(address(key(os.Args[2]).Address))
	case "generate":
		require(len(os.Args) == 5, "Expected genesis, key, output")
		generate(os.Args[2], os.Args[3], os.Args[4])
	case "observe":
		require(len(os.Args) == 4, "Expected imported datadir and output")
		observe(os.Args[2], os.Args[3])
	default:
		panic("Unknown mode")
	}
}
