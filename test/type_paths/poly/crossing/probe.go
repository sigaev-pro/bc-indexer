package main

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
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

func crossingCase(input, bodies, keySource, directory, gate, borGate string, kind byte, ethPrefix, borPrefix int, result map[string]any) error {
	if err := os.Mkdir(directory, 0700); err != nil {
		return err
	}
	in, err := crossingInputs(input, filepath.Join(directory, "inputs"), ethPrefix, borPrefix, gate, borGate)
	if err != nil {
		return err
	}
	actualKind, err := crossingKind(in.genesis.Config)
	if err != nil || actualKind != kind {
		return fmt.Errorf("crossing gate/type mismatch: kind=%d error=%v", actualKind, err)
	}
	result["config"] = json.RawMessage(in.configBytes)
	genesis, err := rlp.EncodeToBytes(in.genesis.ToBlock())
	if err != nil {
		return err
	}
	result["genesis_raw"] = hex.EncodeToString(genesis)
	generated := filepath.Join(directory, "generated")
	if err = generate([]string{"--genesis", filepath.Join(directory, "inputs/genesis.json"), "--config", filepath.Join(directory, "inputs/chain-config.json"), "--keysource", keySource, "--bodies", bodies, "--out", generated}); err != nil {
		return err
	}
	blocks, err := readBlocks(filepath.Join(generated, "blocks.rlp"))
	if err != nil {
		return err
	}
	run, err := start(in, bodies, filepath.Join(directory, "import"), pinnedCommit, result)
	if err != nil {
		return err
	}
	defer run.close()
	tx, err := crossingTransaction(in.genesis.Config, kind, 0)
	if err != nil {
		return err
	}
	header := blocks[0].Header()
	header.TxHash = types.DeriveSha(types.Transactions{tx}, trie.NewStackTrie(nil))
	signer, err := signerKey(keySource)
	if err != nil {
		return err
	}
	seal, err := crypto.Sign(bor.SealHash(header, in.genesis.Config.Bor).Bytes(), signer)
	if err != nil {
		return err
	}
	copy(header.Extra[len(header.Extra)-types.ExtraSealLength:], seal)
	candidate := types.NewBlockWithHeader(header).WithBody(types.Body{Transactions: types.Transactions{tx}})
	negative := map[string]any{"head_before": run.chain.CurrentBlock().Hash().Hex()}
	result["before_activation"] = negative
	_, importErr := run.chain.InsertChain([]*types.Block{candidate}, false)
	negative["accepted"] = importErr == nil
	if importErr != nil {
		negative["error"] = importErr.Error()
	}
	view, err := activationView(run, candidate, importErr == nil)
	if err != nil {
		return err
	}
	negative["block"] = view
	negative, err = crossingRecords(run, negative)
	if err != nil {
		return err
	}
	result["before_activation"] = negative
	if !errors.Is(importErr, core.ErrTxTypeNotSupported) || run.chain.CurrentBlock().Hash() != in.genesis.ToBlock().Hash() {
		return fmt.Errorf("missing unchanged-head native type refusal: %v", importErr)
	}
	positive := map[string]any{"blocks": []map[string]any{}}
	result["crossing"] = positive
	for _, block := range blocks {
		if _, err = run.chain.InsertChain([]*types.Block{block}, false); err != nil {
			return fmt.Errorf("import crossing block %d: %w", block.NumberU64(), err)
		}
		if run.chain.CurrentBlock().Hash() != block.Hash() {
			return errors.New("crossing import did not advance the canonical head")
		}
		view, err := activationView(run, block, true)
		if err != nil {
			return err
		}
		positive["blocks"] = append(positive["blocks"].([]map[string]any), view)
	}
	positive, err = crossingRecords(run, positive)
	if err != nil {
		return err
	}
	result["crossing"] = positive
	return nil
}

func crossingMain(input, bodies, keySource, output, report string) (returned error) {
	result := map[string]any{"native_source": pinnedCommit, "scope": "private-ordinary-activation-crossing", "public_profile_credit": false, "cases": []map[string]any{}}
	defer func() {
		if returned != nil {
			result["error"] = returned.Error()
		}
		if err := saveJSON(report, result); err != nil {
			returned = errors.Join(returned, err)
		}
	}()
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	for _, item := range []struct {
		name, gate, borGate string
		kind                byte
		eth, bor            int
	}{{"Berlin", "berlinBlock", "", 1, 0, 0}, {"London", "londonBlock", "", 2, 1, 0}, {"Prague", "pragueBlock", "", 4, 4, 4},
		{"LondonJaipur", "londonBlock", "jaipurBlock", 2, 1, 0}, {"PragueBhilai", "pragueBlock", "bhilaiBlock", 4, 4, 4}} {
		row := map[string]any{"name": item.name, "type": item.kind, "activation_block": 2, "bor_activation": item.borGate}
		result["cases"] = append(result["cases"].([]map[string]any), row)
		if err := crossingCase(input, bodies, keySource, filepath.Join(output, item.name), item.gate, item.borGate, item.kind, item.eth, item.bor, row); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "identity" {
		fmt.Println(pinnedCommit)
		return
	}
	if len(os.Args) != 6 {
		fmt.Fprintln(os.Stderr, "usage: crossing INPUT BODIES KEY OUTPUT REPORT")
		os.Exit(2)
	}
	if err := crossingMain(os.Args[1], os.Args[2], os.Args[3], os.Args[4], os.Args[5]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
