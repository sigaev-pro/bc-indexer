package parlia

// This test is installed in the pinned client's consensus/parlia package.
// It calls the native decoder, block-specific signer and system classifier.
// It is not an import/observation or public-network acceptance test.
import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

func TestExtractorBSCTypePaths(t *testing.T) {
	// Public deterministic fixture key, never a funded account.
	key, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	config := params.BSCChainConfig
	chain := config.ChainID
	to := common.HexToAddress("0x1000")
	signer := types.LatestSigner(config)
	wantSender := crypto.PubkeyToAddress(key.PublicKey)
	makeTx := func(kind byte, recipient *common.Address, price int64) *types.Transaction {
		var data types.TxData
		switch kind {
		case types.LegacyTxType:
			data = &types.LegacyTx{To: recipient, Value: big.NewInt(0), Gas: 100000, GasPrice: big.NewInt(price)}
		case types.AccessListTxType:
			data = &types.AccessListTx{ChainID: chain, To: recipient, Value: big.NewInt(0), Gas: 100000, GasPrice: big.NewInt(price)}
		case types.DynamicFeeTxType:
			data = &types.DynamicFeeTx{ChainID: chain, To: recipient, Value: big.NewInt(0), Gas: 100000, GasFeeCap: big.NewInt(price), GasTipCap: big.NewInt(price)}
		case types.BlobTxType:
			data = &types.BlobTx{ChainID: uint256.MustFromBig(chain), To: *recipient, Value: new(uint256.Int), Gas: 100000,
				GasFeeCap: uint256.NewInt(uint64(price)), GasTipCap: uint256.NewInt(uint64(price)), BlobFeeCap: uint256.NewInt(1), BlobHashes: []common.Hash{{1}}}
		case types.SetCodeTxType:
			data = &types.SetCodeTx{ChainID: uint256.MustFromBig(chain), To: *recipient, Value: new(uint256.Int), Gas: 100000,
				GasFeeCap: uint256.NewInt(uint64(price)), GasTipCap: uint256.NewInt(uint64(price))}
		default:
			t.Fatalf("unknown fixture kind %d", kind)
		}
		tx, err := types.SignTx(types.NewTx(data), signer, key)
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	rows := make([]map[string]any, 0)
	for kind := byte(0); kind <= types.SetCodeTxType; kind++ {
		tx := makeTx(kind, &to, 0)
		wire, err := tx.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		var decoded types.Transaction
		if err := decoded.UnmarshalBinary(wire); err != nil {
			t.Fatal(err)
		}
		again, err := decoded.MarshalBinary()
		if err != nil || !bytes.Equal(wire, again) || decoded.Type() != kind || decoded.Hash() != tx.Hash() {
			t.Fatalf("wire roundtrip type %d", kind)
		}
		// November 19, 2026, after BSC Pasteur and before Jenner; actual
		// native configuration is unchanged, and the height exceeds Hertz.
		active := types.MakeSigner(config, big.NewInt(100000000), 1795046400)
		from, err := types.Sender(active, &decoded)
		if err != nil || from != wantSender {
			t.Fatalf("current signer type %d: %v", kind, err)
		}
		rows = append(rows, map[string]any{"case": "roundtrip-current-signer", "type": kind, "hash": tx.Hash().Hex(), "sender": from.Hex()})
		if kind == 0 {
			continue
		}
		block, when := big.NewInt(100000000), uint64(1795046400)
		switch kind {
		case types.AccessListTxType:
			block = new(big.Int).Sub(config.BerlinBlock, big.NewInt(1))
		case types.DynamicFeeTxType:
			block = new(big.Int).Sub(config.LondonBlock, big.NewInt(1))
		case types.BlobTxType:
			when = *config.CancunTime - 1
		case types.SetCodeTxType:
			when = *config.PragueTime - 1
		}
		_, err = types.Sender(types.MakeSigner(config, block, when), tx)
		if !errors.Is(err, types.ErrTxTypeNotSupported) {
			t.Fatalf("pre-activation type %d: %v", kind, err)
		}
		rows = append(rows, map[string]any{"case": "pre-activation-signer-refusal", "type": kind, "block": block.String(), "time": when})
	}
	unknown := 0
	for kind := byte(5); kind < 128; kind++ {
		var tx types.Transaction
		if err := tx.UnmarshalBinary([]byte{kind, 0xc0}); !errors.Is(err, types.ErrTxTypeNotSupported) {
			t.Fatalf("unknown wire type %d: %v", kind, err)
		}
		unknown++
	}
	engine := &Parlia{signer: signer}
	header := &types.Header{Coinbase: wantSender}
	classification := make([]map[string]any, 0)
	for kind := byte(0); kind <= types.SetCodeTxType; kind++ {
		tx := makeTx(kind, &to, 0)
		before := tx.Hash()
		system, err := engine.IsSystemTransaction(tx, header)
		if err != nil || !system || tx.Type() != kind || tx.Hash() != before {
			t.Fatalf("system classification type %d: %v", kind, err)
		}
		classification = append(classification, map[string]any{"type": kind, "system": system, "hash": before.Hex()})
	}
	other := common.HexToAddress("0x123456")
	controls := []struct {
		name      string
		tx        *types.Transaction
		header    *types.Header
		wantError bool
	}{
		{"non-system-recipient", makeTx(0, &other, 0), header, false},
		{"contract-creation", makeTx(0, nil, 0), header, false},
		{"nonzero-price", makeTx(0, &to, 1), header, false},
		{"wrong-coinbase", makeTx(0, &to, 0), &types.Header{Coinbase: other}, false},
		{"invalid-signature", types.NewTx(&types.LegacyTx{To: &to, Value: big.NewInt(0), GasPrice: big.NewInt(0), Gas: 100000}), header, true},
	}
	for _, control := range controls {
		system, err := engine.IsSystemTransaction(control.tx, control.header)
		if system || (err != nil) != control.wantError {
			t.Fatalf("classifier control %s: system=%v error=%v", control.name, system, err)
		}
	}
	output := os.Getenv("EXTRACTOR_BSC_TYPE_REPORT")
	if output == "" {
		t.Fatal("EXTRACTOR_BSC_TYPE_REPORT is required")
	}
	report := map[string]any{"schema_version": 1, "scope": "native-decoder-signer-classifier", "rows": rows,
		"unknown_typed_ids_rejected": unknown, "classification": classification, "classification_controls": len(controls),
		"import_observation": "NOT_RUN", "system_transaction_state": "NOT_RUN", "public_profiles": "NOT_RUN"}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}
