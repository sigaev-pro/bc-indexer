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
	if kind == 0 && config.Bor != nil && config.Bor.MadhugiriBlock != nil && config.Bor.MadhugiriBlock.Cmp(big.NewInt(2)) == 0 {
		return types.StateSyncTxType, nil
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
	if kind == 0 || kind == types.StateSyncTxType {
		// The StateSync carrier bodies keep only the ordinary legacy transfer.
		// With Madhugiri active at block 2, native FinalizeAndAssemble appends
		// the events-derived 0x7f itself in the final position; without it the
		// events commit off-body as a separate Bor receipt.
		tx, err := types.SignNewTx(key, types.LatestSigner(config), &types.LegacyTx{
			Nonce: 1, GasPrice: big.NewInt(5_000_000_000), Gas: 21_000, To: &recipient, Value: big.NewInt(7)})
		return types.Transactions{tx}, err
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

// crossingView extends the pinned activation view with the StateSync carrier
// observations of the imported block: the live observer transaction rows and
// call frames, the separate pre-Madhugiri Bor receipt and the native
// state-sync records of the header chain.
func crossingView(run *nodeRun, block *types.Block, stored bool) (map[string]any, error) {
	view, err := activationView(run, block, stored)
	if err != nil {
		return nil, err
	}
	receipt := rawdb.ReadBorReceipt(run.eth.ChainDb(), block.Hash(), block.NumberU64(), run.chain.Config())
	borReceipt := map[string]any{"present": receipt != nil, "logs": []LogRow{}}
	if receipt != nil {
		borReceipt["logs"] = logRows(receipt.Logs)
	}
	view["bor_receipt"] = borReceipt
	stateSync := run.chain.HeaderChain().GetStateSync()
	if stateSync == nil {
		stateSync = []*types.StateSyncData{}
	}
	view["state_sync"], view["transactions"], view["calls"] = stateSync, live.Rows, live.Calls
	return view, nil
}

// crossingObservation inserts a hand-built block-1 candidate whose only body
// transaction is the events-derived 0x7f before the Madhugiri gate. Block 1
// is not a sprint start, so native Finalize returns before any body
// requirement and the processor skips the reserved envelope: the candidate
// imports without executing it. The skip-execution commitments (empty
// receipts, no gas, no bloom, unchanged parent state root) mirror the
// accepted preactivation observation; no refusal credit is claimed.
func crossingObservation(run *nodeRun, in inputs, keySource string, blocks []*types.Block, view func(*nodeRun, *types.Block, bool) (map[string]any, error), result map[string]any) error {
	tx, err := crossingTransaction(in.genesis.Config, types.StateSyncTxType, 0)
	if err != nil {
		return err
	}
	header := blocks[0].Header()
	header.Root = in.genesis.ToBlock().Root()
	header.GasUsed = 0
	header.Bloom = types.Bloom{}
	header.ReceiptHash = types.EmptyRootHash
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
	live.Rows, live.Calls = []map[string]any{}, []map[string]any{}
	_, importErr := run.chain.InsertChain([]*types.Block{candidate}, false)
	negative["accepted"] = importErr == nil
	if importErr != nil {
		negative["error"] = importErr.Error()
	}
	row, err := view(run, candidate, importErr == nil)
	if err != nil {
		return err
	}
	negative["block"] = row
	negative["classification"] = "native-preMadhugiri-observation-no-refusal-credit"
	negative, err = crossingRecords(run, negative)
	if err != nil {
		return err
	}
	result["before_activation"] = negative
	if importErr != nil {
		return fmt.Errorf("missing native pre-Madhugiri observation acceptance: %v", importErr)
	}
	if run.chain.CurrentBlock().Hash() != candidate.Hash() {
		return errors.New("accepted observation did not advance the canonical head")
	}
	return nil
}

// crossingTypedRefusal inserts the selected ordinary type with nonce 0 at
// block 1 and requires the exact native unsupported-type error with the
// genesis head unchanged.
func crossingTypedRefusal(run *nodeRun, in inputs, keySource string, blocks []*types.Block, kind byte, result map[string]any) error {
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
	return nil
}

// crossingDecodeSweep rewrites the envelope tag of the crossing block's
// native 0x7f transaction across every unknown typed ID while keeping the
// preceding ordinary transaction, and requires each wire to fail native
// block decoding. Type 127 itself decodes; its refusal lives at the import
// layer, so the sweep stays separate from crossingRefusals.
func crossingDecodeSweep(block *types.Block) ([]map[string]any, error) {
	raw, err := rlp.EncodeToBytes(block)
	if err != nil {
		return nil, err
	}
	var fields []rlp.RawValue
	if err := rlp.DecodeBytes(raw, &fields); err != nil {
		return nil, err
	}
	var txs []rlp.RawValue
	if err := rlp.DecodeBytes(fields[1], &txs); err != nil {
		return nil, err
	}
	if len(txs) == 0 {
		return nil, errors.New("unknown control needs the crossing body transactions")
	}
	var envelope []byte
	if err := rlp.DecodeBytes(txs[len(txs)-1], &envelope); err != nil {
		return nil, err
	}
	if len(envelope) == 0 || envelope[0] != types.StateSyncTxType {
		return nil, errors.New("unknown control needs the final state-sync transaction")
	}
	unknown := []map[string]any{}
	for typ := byte(5); typ < types.StateSyncTxType; typ++ {
		changed := append([]byte(nil), envelope...)
		changed[0] = typ
		txWire, err := rlp.EncodeToBytes(changed)
		if err != nil {
			return nil, err
		}
		body := append([]rlp.RawValue(nil), txs[:len(txs)-1]...)
		body = append(body, txWire)
		fields[1], err = rlp.EncodeToBytes(body)
		if err != nil {
			return nil, err
		}
		wire, err := rlp.EncodeToBytes(fields)
		if err != nil {
			return nil, err
		}
		var decoded types.Block
		err = rlp.DecodeBytes(wire, &decoded)
		if err == nil {
			return nil, fmt.Errorf("native block decoder accepted unknown type %d", typ)
		}
		unknown = append(unknown, map[string]any{"type": typ, "error": err.Error()})
	}
	return unknown, nil
}

// crossingRefusals are the post-gate body-StateSync negative controls. A
// fresh importer seeds block 1, then must refuse every crossing-block body
// whose final transaction is not exactly the events-derived 0x7f the native
// Finalize rebuilds from the stub: the dropped carrier, the displaced
// carrier and the carrier rebuilt with a wrong L1 event hash.
func crossingRefusals(in inputs, bodies, directory, keySource string, blocks []*types.Block) ([]map[string]any, error) {
	key, err := signerKey(keySource)
	if err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	for _, name := range []string{"missing", "non-final", "mismatched-l1-hash"} {
		row := map[string]any{"case": name}
		rows = append(rows, row)
		run, err := start(in, bodies, filepath.Join(directory, name), pinnedCommit, row)
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer run.close()
			for _, block := range blocks[:1] {
				if _, err := run.chain.InsertChain([]*types.Block{block}, false); err != nil {
					return err
				}
			}
			original := blocks[1]
			transactions := original.Transactions()
			if len(transactions) < 2 || transactions[len(transactions)-1].Type() != types.StateSyncTxType {
				return errors.New("refusal control needs the ordinary and state-sync crossing body")
			}
			legacy, stateSync := transactions[0], transactions[len(transactions)-1]
			header := original.Header()
			var body types.Transactions
			switch name {
			case "missing":
				body = types.Transactions{legacy}
			case "non-final":
				body = types.Transactions{stateSync, legacy}
			default:
				items := []*types.StateSyncData{}
				for _, item := range stateSync.GetStateSyncData() {
					copy := *item
					copy.Data = append([]byte(nil), item.Data...)
					items = append(items, &copy)
				}
				items[0].TxHash = common.HexToHash("0x99")
				body = types.Transactions{legacy, types.NewTx(&types.StateSyncTx{StateSyncData: items})}
			}
			header.TxHash = types.DeriveSha(body, trie.NewStackTrie(nil))
			header.GasUsed = 21_000
			signature, err := crypto.Sign(bor.SealHash(header, in.genesis.Config.Bor).Bytes(), key)
			if err != nil {
				return err
			}
			copy(header.Extra[len(header.Extra)-types.ExtraSealLength:], signature)
			candidate := types.NewBlockWithHeader(header).WithBody(types.Body{Transactions: body})
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
				return errors.New("rejected StateSync moved head")
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
