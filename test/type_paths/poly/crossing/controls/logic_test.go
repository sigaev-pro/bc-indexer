package main

import (
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// These doubles expose only the dependencies of the selected real function
// bodies. Native signatures, account/state handling and import are not tested.
type nodeRun struct {
	values map[string]any
	err    error
}

func activationRecords(run *nodeRun, row map[string]any) error {
	if run.err != nil {
		return run.err
	}
	for key, value := range run.values {
		row[key] = value
	}
	return nil
}

var calledKey *ecdsa.PrivateKey
var transactionError error

func corpusTransactions(_ *params.ChainConfig, key *ecdsa.PrivateKey) (types.Transactions, error) {
	calledKey = key
	return types.Transactions{&types.Transaction{Kind: 0, Nonce: 0}}, transactionError
}

func crossingTransaction(_ *params.ChainConfig, kind byte, nonce uint64) (*types.Transaction, error) {
	return &types.Transaction{Kind: kind, Nonce: nonce}, transactionError
}

func TestCrossingKind(t *testing.T) {
	for _, row := range []struct {
		config params.ChainConfig
		kind   byte
	}{{params.ChainConfig{BerlinBlock: big.NewInt(2)}, 1},
		{params.ChainConfig{BerlinBlock: big.NewInt(0), LondonBlock: big.NewInt(2), PragueBlock: big.NewInt(8)}, 2},
		{params.ChainConfig{BerlinBlock: big.NewInt(0), LondonBlock: big.NewInt(0), PragueBlock: big.NewInt(2)}, 4}} {
		kind, err := crossingKind(&row.config)
		if err != nil || kind != row.kind {
			t.Fatalf("wrong gate: kind=%d error=%v want=%d", kind, err, row.kind)
		}
	}
	for _, config := range []params.ChainConfig{{}, {BerlinBlock: big.NewInt(1)}, {LondonBlock: big.NewInt(3)},
		{BerlinBlock: big.NewInt(2), LondonBlock: big.NewInt(2)}, {BerlinBlock: big.NewInt(2), PragueBlock: big.NewInt(2)}} {
		if _, err := crossingKind(&config); err == nil {
			t.Fatal("missing or ambiguous block-2 gate was accepted")
		}
	}
}

func TestCrossingTransactionSelection(t *testing.T) {
	defer func() { transactionError = nil }()
	key := new(ecdsa.PrivateKey)
	for _, config := range []params.ChainConfig{{BerlinBlock: big.NewInt(2)}, {LondonBlock: big.NewInt(2)}, {PragueBlock: big.NewInt(2)}} {
		kind, _ := crossingKind(&config)
		for _, number := range []uint64{1, 2} {
			txs, err := crossingTransactions(&config, key, number)
			want := &types.Transaction{Kind: kind, Nonce: 1}
			if number == 1 {
				want = &types.Transaction{Kind: 0, Nonce: 0}
			}
			if err != nil || len(txs) != 1 || !reflect.DeepEqual(txs[0], want) || calledKey != key {
				t.Fatalf("wrong block %d selection: %v %v", number, txs, err)
			}
		}
	}
	for _, number := range []uint64{0, 3} {
		if _, err := crossingTransactions(&params.ChainConfig{BerlinBlock: big.NewInt(2)}, key, number); err == nil {
			t.Fatal("out-of-scope block accepted")
		}
	}
	if _, err := crossingTransactions(&params.ChainConfig{}, key, 2); err == nil {
		t.Fatal("missing gate error lost")
	}
	transactionError = errors.New("fixture transaction failed")
	for _, number := range []uint64{1, 2} {
		if _, err := crossingTransactions(&params.ChainConfig{BerlinBlock: big.NewInt(2)}, key, number); !errors.Is(err, transactionError) {
			t.Fatal("transaction error lost")
		}
	}
}

func TestBoundarySnapshotIsolationAndErrors(t *testing.T) {
	keys := map[string][]string{"1": {"first"}}
	row := map[string]any{"block": map[string]any{"hash": "before"}}
	run := &nodeRun{values: map[string]any{"keys": keys, "observed": []uint64{1}}}
	got, err := crossingRecords(run, row)
	if err != nil {
		t.Fatal(err)
	}
	keys["1"] = append(keys["1"], "later")
	row["block"].(map[string]any)["hash"] = "after"
	if got["block"].(map[string]any)["hash"] != "before" || len(got["keys"].(map[string]any)["1"].([]any)) != 1 {
		t.Fatal("snapshot aliases mutable native boundary")
	}
	run.err = errors.New("durable record read failed")
	if _, err := crossingRecords(run, row); !errors.Is(err, run.err) {
		t.Fatal("durable record failure lost")
	}
	run.err = nil
	if _, err := crossingRecords(run, map[string]any{"unsupported": make(chan int)}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatal("snapshot encoding error lost")
	}
	if _, err := crossingRecords(run, map[string]any{"overflow": json.Number("1e1000")}); err == nil {
		t.Fatal("snapshot decoding error lost")
	}
}
