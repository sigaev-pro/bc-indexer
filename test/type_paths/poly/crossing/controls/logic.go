// Portable function-body controls with explicit native-type doubles.
package main

import (
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"math/big"
)

func crossingKind(config *params.ChainConfig) (byte, error) {
	var kind byte
	for _, fork := range []struct {
		block *big.Int
		kind  byte
	}{{config.BerlinBlock, 1}, {config.LondonBlock, 2}, {config.PragueBlock, 4}} {
		if fork.block != nil && fork.block.Cmp(big.NewInt(2)) == 0 {
			if kind != 0 {
				return 0, errors.New("multiple ordinary-type gates at block 2")
			}
			kind = fork.kind
		}
	}
	if kind == 0 {
		return 0, errors.New("missing ordinary-type gate at block 2")
	}
	return kind, nil
}

func crossingTransactions(config *params.ChainConfig, key *ecdsa.PrivateKey, number uint64) (types.Transactions, error) {
	if number == 1 {
		return corpusTransactions(config, key)
	}
	if number != 2 {
		return nil, fmt.Errorf("unexpected crossing block %d", number)
	}
	kind, err := crossingKind(config)
	if err != nil {
		return nil, err
	}
	tx, err := crossingTransaction(config, kind, 1)
	if err != nil {
		return nil, err
	}
	return types.Transactions{tx}, nil
}

// The accepted helper reports live observer maps. Freeze each boundary before
// a later import mutates those maps, retaining the original JSON field shapes.
func crossingRecords(run *nodeRun, row map[string]any) (map[string]any, error) {
	if err := activationRecords(run, row); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}
	var snapshot map[string]any
	if err = json.Unmarshal(encoded, &snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}
