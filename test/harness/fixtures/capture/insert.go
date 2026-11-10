//go:build !extractor_bor

package main

import (
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
)

// insertChain calls the Geth and BSC import entry point.
func insertChain(chain *core.BlockChain, blocks types.Blocks) (int, error) {
	return chain.InsertChain(blocks)
}
