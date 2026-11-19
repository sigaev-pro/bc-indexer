package main

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/holiman/uint256"
	"math/big"
	"os"
	"path/filepath"
)

var ethForks = []string{"berlinBlock", "londonBlock", "shanghaiBlock", "cancunBlock", "pragueBlock"}
var borForks = []string{"jaipurBlock", "delhiBlock", "indoreBlock", "ahmedabadBlock", "bhilaiBlock", "rioBlock", "madhugiriBlock", "madhugiriProBlock", "dandeliBlock", "lisovoBlock", "lisovoProBlock", "giuglianoBlock", "chicagoBlock", "valenciaBlock", "austinBlock", "uburuBlock"}

func corpusTransactions(config *params.ChainConfig, key *ecdsa.PrivateKey) (types.Transactions, error) {
	tx, err := types.SignNewTx(key, types.LatestSigner(config), &types.LegacyTx{Nonce: 0, GasPrice: big.NewInt(5_000_000_000), Gas: 21_000, To: &recipient, Value: big.NewInt(7)})
	return types.Transactions{tx}, err
}

func historical(input, output string, ethPrefix, borPrefix int) (inputs, error) {
	data, err := readBounded(filepath.Join(input, "genesis.json"), 4*1024*1024)
	if err != nil {
		return inputs{}, err
	}
	var document map[string]any
	if err = json.Unmarshal(data, &document); err != nil {
		return inputs{}, err
	}
	config := document["config"].(map[string]any)
	engine := config["bor"].(map[string]any)
	for i, name := range ethForks {
		value := 8
		if i < ethPrefix {
			value = 0
		}
		config[name] = value
	}
	for i, name := range borForks {
		value := 8
		if i < borPrefix {
			value = 0
		}
		engine[name] = value
	}
	if ethPrefix < 2 {
		delete(document, "baseFeePerGas")
	}
	genesis, err := json.Marshal(document)
	if err != nil {
		return inputs{}, err
	}
	chain, err := json.Marshal(config)
	if err != nil {
		return inputs{}, err
	}
	if err = os.Mkdir(output, 0700); err != nil {
		return inputs{}, err
	}
	if err = writeNew(filepath.Join(output, "genesis.json"), genesis); err != nil {
		return inputs{}, err
	}
	if err = writeNew(filepath.Join(output, "chain-config.json"), chain); err != nil {
		return inputs{}, err
	}
	in, err := loadInputs(filepath.Join(output, "genesis.json"), filepath.Join(output, "chain-config.json"))
	if err == nil {
		err = in.genesis.Config.CheckConfigForkOrder()
	}
	return in, err
}

func activationTransaction(config *params.ChainConfig, kind byte) (*types.Transaction, error) {
	if kind == types.StateSyncTxType {
		return types.NewTx(&types.StateSyncTx{StateSyncData: []*types.StateSyncData{{ID: 1, Contract: common.HexToAddress("0x3000000000000000000000000000000000000003"), Data: []byte{37}, TxHash: common.HexToHash("0x01")}}}), nil
	}
	key, err := senderKey()
	if err != nil {
		return nil, err
	}
	var inner types.TxData
	switch kind {
	case 1:
		inner = &types.AccessListTx{ChainID: config.ChainID, Nonce: 1, GasPrice: big.NewInt(5_000_000_000), Gas: 21_000, To: &recipient, Value: big.NewInt(7)}
	case 2:
		inner = &types.DynamicFeeTx{ChainID: config.ChainID, Nonce: 1, GasTipCap: big.NewInt(2_000_000_000), GasFeeCap: big.NewInt(5_000_000_000), Gas: 21_000, To: &recipient, Value: big.NewInt(7)}
	case 4:
		authority, err := crypto.HexToECDSA(fmt.Sprintf("%064x", 2))
		if err != nil {
			return nil, err
		}
		auth, err := types.SignSetCode(authority, types.SetCodeAuthorization{ChainID: *uint256.MustFromBig(config.ChainID), Address: recipient, Nonce: 0})
		if err != nil {
			return nil, err
		}
		inner = &types.SetCodeTx{ChainID: uint256.MustFromBig(config.ChainID), Nonce: 1, GasTipCap: uint256.NewInt(2_000_000_000), GasFeeCap: uint256.NewInt(5_000_000_000), Gas: 100_000, To: recipient, Value: uint256.NewInt(7), AuthList: []types.SetCodeAuthorization{auth}}
	default:
		return nil, fmt.Errorf("unsupported activation fixture type %d", kind)
	}
	return types.SignNewTx(key, types.LatestSigner(config), inner)
}

func activationView(run *nodeRun, block *types.Block, stored bool) (map[string]any, error) {
	raw, err := rlp.EncodeToBytes(block)
	if err != nil {
		return nil, err
	}
	txProof, err := orderedProofs(block.Transactions(), block.TxHash())
	if err != nil {
		return nil, err
	}
	row := map[string]any{"number": block.NumberU64(), "hash": block.Hash().Hex(), "parent": block.ParentHash().Hex(), "root": block.Root().Hex(), "raw_block": hex.EncodeToString(raw), "transaction_proofs": txProof}
	if stored {
		receipts := run.chain.GetReceiptsByHash(block.Hash())
		proof, err := orderedProofs(receipts, block.ReceiptHash())
		if err != nil {
			return nil, err
		}
		accounts, err := accountProofs(run, block)
		if err != nil {
			return nil, err
		}
		state, err := run.chain.StateAt(block.Root())
		if err != nil {
			return nil, err
		}
		row["receipts"], row["receipt_proofs"], row["account_proofs"], row["state_after"] = receipts, proof, accounts, readAccounts(state, watched)
	}
	return row, nil
}

func activationRecords(run *nodeRun, row map[string]any) error {
	row["head_hash"] = run.chain.CurrentBlock().Hash().Hex()
	for label, prefix := range map[string]string{"admission": "extractor-admission-v1/", "decisions": "extractor-decision-v1/"} {
		records, err := durableRecords(run.eth.ChainDb(), prefix)
		if err != nil {
			return err
		}
		row[label] = records
	}
	run.count.mu.Lock()
	defer run.count.mu.Unlock()
	row["observed"], row["keys"], row["observer_errors"] = run.count.seen, run.count.keys, run.count.errs
	return nil
}

func activationCase(input, bodies, keySource, directory string, kind byte, ethPrefix, borPrefix int, result map[string]any) error {
	if err := os.Mkdir(directory, 0700); err != nil {
		return err
	}
	in, err := historical(input, filepath.Join(directory, "inputs"), ethPrefix, borPrefix)
	if err != nil {
		return err
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
	control := map[string]any{}
	result["control"] = control
	run, err := start(in, bodies, filepath.Join(directory, "control"), pinnedCommit, control)
	if err != nil {
		return err
	}
	err = func() error {
		defer run.close()
		rows := []map[string]any{}
		for _, block := range blocks {
			if _, err := run.chain.InsertChain([]*types.Block{block}, false); err != nil {
				return err
			}
			view, err := activationView(run, block, true)
			if err != nil {
				return err
			}
			rows = append(rows, view)
		}
		control["blocks"] = rows
		return activationRecords(run, control)
	}()
	if err != nil {
		return err
	}
	attempt := map[string]any{}
	result["attempt"] = attempt
	run, err = start(in, bodies, filepath.Join(directory, "attempt"), pinnedCommit, attempt)
	if err != nil {
		return err
	}
	defer run.close()
	if _, err = run.chain.InsertChain(blocks[:1], false); err != nil {
		return err
	}
	tx, err := activationTransaction(in.genesis.Config, kind)
	if err != nil {
		return err
	}
	header := blocks[1].Header()
	body := types.Body{Transactions: types.Transactions{tx}}
	header.TxHash = types.DeriveSha(types.Transactions(body.Transactions), trie.NewStackTrie(nil))
	signer, err := signerKey(keySource)
	if err != nil {
		return err
	}
	seal, err := crypto.Sign(bor.SealHash(header, in.genesis.Config.Bor).Bytes(), signer)
	if err != nil {
		return err
	}
	copy(header.Extra[len(header.Extra)-types.ExtraSealLength:], seal)
	candidate := types.NewBlockWithHeader(header).WithBody(body)
	attempt["head_before"] = run.chain.CurrentBlock().Hash().Hex()
	_, importErr := run.chain.InsertChain([]*types.Block{candidate}, false)
	attempt["accepted"] = importErr == nil
	if importErr != nil {
		attempt["error"] = importErr.Error()
	}
	view, err := activationView(run, candidate, importErr == nil)
	if err != nil {
		return err
	}
	attempt["block"] = view
	if err = activationRecords(run, attempt); err != nil {
		return err
	}
	if kind != types.StateSyncTxType && !errors.Is(importErr, core.ErrTxTypeNotSupported) {
		return fmt.Errorf("expected native preactivation type refusal, got %v", importErr)
	}
	if kind == types.StateSyncTxType {
		attempt["classification"] = "native-preMadhugiri-observation-no-refusal-credit"
	}
	return nil
}

func activationMain(input, bodies, keySource, output, report string) (returned error) {
	result := map[string]any{"native_source": pinnedCommit, "scope": "private-preactivation", "public_profile_credit": false, "cases": []map[string]any{}}
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
		name     string
		kind     byte
		eth, bor int
	}{{"preBerlin", 1, 0, 0}, {"preLondon", 2, 1, 0}, {"prePrague", 4, 4, 4}, {"preMadhugiri", 127, 5, 6}} {
		row := map[string]any{"name": item.name, "type": item.kind}
		result["cases"] = append(result["cases"].([]map[string]any), row)
		if err := activationCase(input, bodies, keySource, filepath.Join(output, item.name), item.kind, item.eth, item.bor, row); err != nil {
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
		fmt.Fprintln(os.Stderr, "usage: activation INPUT BODIES KEY OUTPUT REPORT")
		os.Exit(2)
	}
	if err := activationMain(os.Args[1], os.Args[2], os.Args[3], os.Args[4], os.Args[5]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
