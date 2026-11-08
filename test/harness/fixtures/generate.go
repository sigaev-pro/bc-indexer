// This program prepares input blocks only. It does not generate expected results.
package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	if len(os.Args) != 3 {
		panic("usage: generate GENESIS OUTPUT_DIRECTORY")
	}
	data, err := os.ReadFile(os.Args[1])
	check(err)
	var genesis core.Genesis
	check(json.Unmarshal(data, &genesis))
	key, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	check(err)
	tx, err := types.SignTx(types.NewTransaction(0, common.HexToAddress("0x1000000000000000000000000000000000000000"), big.NewInt(1000000000000000000), 21000, big.NewInt(2000000000), nil), types.LatestSigner(genesis.Config), key)
	check(err)
	db, blocks, _ := core.GenerateChainWithGenesis(&genesis, ethash.NewFaker(), 2, func(index int, block *core.BlockGen) {
		block.SetCoinbase(common.HexToAddress("0x2000000000000000000000000000000000000000"))
		if index == 0 {
			block.AddTx(tx)
		}
	})
	defer db.Close()
	input, err := os.OpenFile(filepath.Join(os.Args[2], "blocks.rlp"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	check(err)
	full, err := os.OpenFile(filepath.Join(os.Args[2], "full-chain.rlp"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	check(err)
	check(rlp.Encode(full, genesis.ToBlock()))
	rows := make([]map[string]interface{}, 0, 2)
	for _, block := range blocks {
		check(rlp.Encode(input, block))
		check(rlp.Encode(full, block))
		rows = append(rows, map[string]interface{}{"number": block.NumberU64(), "hash": block.Hash().Hex(), "state_root": block.Root().Hex(), "gas_used": block.GasUsed(), "transactions": len(block.Transactions()), "base_fee": block.BaseFee().String()})
	}
	check(input.Sync())
	check(input.Close())
	check(full.Sync())
	check(full.Close())
	metadata, err := json.MarshalIndent(map[string]interface{}{"genesis_hash": genesis.ToBlock().Hash().Hex(), "blocks": rows}, "", "  ")
	check(err)
	check(os.WriteFile(filepath.Join(os.Args[2], "generated.json"), append(metadata, '\n'), 0600))
	fmt.Println("Prepared two local input blocks; no native import or oracle claim.")
}
