//go:build extractor_bor

package main

import (
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
)

// insertChain calls the Bor import entry point without witness generation.
func insertChain(chain *core.BlockChain, blocks types.Blocks) (int, error) {
	return chain.InsertChain(blocks, false)
}
