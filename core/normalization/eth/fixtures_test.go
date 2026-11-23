package eth

import (
	"os"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// The fixtures of this file are the retained E01 evidence values. Every
// literal cites its source file; nothing here is derived from a native run
// of this module (D96: no native runs).
//
//   - test/native_corpus/expected/eth.json — the smoke corpus oracle of
//     block 1 (four transactions) and block 2 (empty).
//   - test/native_corpus/eth/probe.go — the corpus contracts and the
//     genesis allocation.
//   - test/type_paths/eth/ORACLE.md and probe.go — transaction types 0-4
//     and the EIP-7702 delegation fixtures.

// Corpus identities (probe.go of the smoke corpus).
var (
	corpusCoinbase  = balance.MustParseAddress("0x2000000000000000000000000000000000000000")
	corpusParent    = balance.MustParseAddress("0x3000000000000000000000000000000000000001")
	corpusChild     = balance.MustParseAddress("0x3000000000000000000000000000000000000002")
	corpusRecipient = balance.MustParseAddress("0x4000000000000000000000000000000000000001")
	corpusSender    = balance.MustParseAddress("0x7e5f4552091a69125d5dfcb7b8c2659029395bdf")
)

const (
	corpusParentCode = "6001600055602060006000600034733000000000000000000000000000000000000002617530f1156001553d6002556000516003553460005260a160206000a100"
	corpusChildCode  = "600160005560c160006000a160ee60005260206000fd"

	// Type-path fixtures (ORACLE.md of test/type_paths/eth).
	typePathAuthority = "0x2b5ad5c4795c026514f8317c7a215e218dccd6cf"
	typePathDelegate  = "0x3000000000000000000000000000000000000004"
)

// u256 is the decimal fixture helper.
func u256(s string) UInt256 { return balance.MustParseUInt256(s) }

// u256g builds a value from gwei for readability.
func gwei(v uint64) UInt256 { return balance.UInt256FromUint64(v * 1_000_000_000) }

// mustAddr parses a fixture address.
func mustAddr(s string) Address20 { return balance.MustParseAddress(s) }

// corpusTransferTx returns the observation of corpus transaction (1,0):
// the legacy 1 ETH transfer at 2 gwei (expected/eth.json, case transfer).
func corpusTransferTx() TxObservation {
	obs := TxObservation{
		Inclusion:  Inclusion{NativePosition: 0, TransactionHash: patternHash(0x11)},
		NativeType: 0,
		RawEncodings: []RawEncoding{
			{Kind: RawLegacyRLP, Bytes: []byte{0x01}},
		},
		Projection: EvmMessage{
			Sender:            Effective[Address20]{Value: corpusSender, Provenance: ProvenanceEncoded},
			Target:            Some(Effective[Address20]{Value: corpusRecipient, Provenance: ProvenanceEncoded}),
			Value:             Effective[UInt256]{Value: u256("1000000000000000000"), Provenance: ProvenanceEncoded},
			Input:             Effective[[]byte]{Value: []byte{}, Provenance: ProvenanceEncoded},
			Nonce:             Effective[uint64]{Value: 0, Provenance: ProvenanceEncoded},
			GasLimit:          21000,
			EffectiveGasPrice: gwei(2),
		},
		Receipt: Receipt{
			Status:            Some(uint8(1)),
			Result:            ResultSuccess,
			CumulativeGasUsed: 21000,
			GasUsed:           Some(uint64(21000)),
			EffectiveGasPrice: Some(gwei(2)),
			Logs:              nil,
		},
		ScopeOps: []ScopeOp{
			{Kind: ScopeEnter, Parent: 0, OwnerFrame: uint64Ptr(0)},
		},
		Frames: []FrameInput{{
			Kind: KindCall, Engine: balance.EngineEVM, LocalOutcome: ResultSuccess,
			Caller: &corpusSender, Context: addrPtr(corpusRecipient), Code: addrPtr(corpusRecipient),
			MsgSender: &corpusSender, Target: addrPtr(corpusRecipient),
			Value: Some(u256("1000000000000000000")), Gas: Some(uint64(21000)), GasUsed: Some(uint64(21000)),
			Input: []byte{}, Output: []byte{}, RevertData: []byte{},
		}},
		Transfers: []TransferObservation{{
			FrameID: 0, From: corpusSender, To: corpusRecipient, Amount: u256("1000000000000000000"),
			Scope: 1, Committed: true,
			Effects: []balance.BalanceEffect{
				{EffectIndex: 0, Address: corpusSender, Before: u256("9999958000000000000"), After: u256("8999958000000000000")},
				{EffectIndex: 1, Address: corpusRecipient, Before: u256("0"), After: u256("1000000000000000000")},
			},
		}},
		Fee: FeeObservation{
			Payer: corpusSender, GasLimit: 21000, GasUsed: 21000, EffectiveGasPrice: gwei(2),
			Recipient: corpusCoinbase, BaseFee: u256("875000000"),
			PayerBalanceBefore: u256("10000000000000000000"),
			// The observed point before the (zero) return: the payer after
			// the transfer. expected/eth.json state_after of (1,0).
			PayerBalanceAtReturn:   Some(u256("8999958000000000000")),
			RecipientBalanceBefore: u256("0"),
		},
		Ops: []TxOp{
			{Kind: OpGasPurchase},
			{Kind: OpFrameEntry, FrameIndex: 0},
			{Kind: OpTransfer, TransferIndex: 0},
			{Kind: OpGasReturn},
			{Kind: OpFeeCredit},
		},
		State: []AccountObservation{
			accountObservation(corpusCoinbase, false, "0", 0, "", true, "23625000000000", 0, ""),
			accountObservation(corpusRecipient, false, "0", 0, "", true, "1000000000000000000", 0, ""),
			accountObservation(corpusSender, true, "10000000000000000000", 0, "", true, "8999958000000000000", 1, ""),
		},
	}
	return obs
}

// corpusNestedRevertTx returns the observation of corpus transaction (1,2):
// the parent call whose child call reverts and is caught
// (expected/eth.json, case nested-revert, status 1).
func corpusNestedRevertTx() TxObservation {
	obs := TxObservation{
		Inclusion:    Inclusion{NativePosition: 2, TransactionHash: patternHash(0x22)},
		NativeType:   0,
		RawEncodings: []RawEncoding{{Kind: RawLegacyRLP, Bytes: []byte{0x03}}},
		Projection: EvmMessage{
			Sender:            Effective[Address20]{Value: corpusSender, Provenance: ProvenanceEncoded},
			Target:            Some(Effective[Address20]{Value: corpusParent, Provenance: ProvenanceEncoded}),
			Value:             Effective[UInt256]{Value: u256("5"), Provenance: ProvenanceEncoded},
			Input:             Effective[[]byte]{Value: []byte{}, Provenance: ProvenanceEncoded},
			Nonce:             Effective[uint64]{Value: 2, Provenance: ProvenanceEncoded},
			GasLimit:          200000,
			EffectiveGasPrice: gwei(2),
		},
		Receipt: Receipt{
			Status:            Some(uint8(1)),
			Result:            ResultSuccess,
			CumulativeGasUsed: 184655,
			GasUsed:           Some(uint64(142655)),
			EffectiveGasPrice: Some(gwei(2)),
			Logs: []Log{{
				Address:       corpusParent,
				Topics:        []Hash32{valueHash(0xa1)},
				Data:          value32(5),
				TxLogIndex:    0,
				BlockLogIndex: 0,
			}},
		},
		// Scope 1: the root execution; scope 2: the child call, rolled
		// back directly and caught by the parent.
		ScopeOps: []ScopeOp{
			{Kind: ScopeEnter, Parent: 0, OwnerFrame: uint64Ptr(0)},
			{Kind: ScopeEnter, Parent: 1, OwnerFrame: uint64Ptr(1)},
			{Kind: ScopeMarkRollback, Scope: 2},
		},
		Frames: []FrameInput{
			{
				Kind: KindCall, Engine: balance.EngineEVM, LocalOutcome: ResultSuccess,
				Caller: &corpusSender, Context: addrPtr(corpusParent), Code: addrPtr(corpusParent),
				MsgSender: &corpusSender, Target: addrPtr(corpusParent),
				Value: Some(u256("5")), Gas: Some(uint64(200000)), GasUsed: Some(uint64(142655)),
				Input: []byte{}, Output: []byte{}, RevertData: []byte{},
			},
			{
				ParentIndex: 0, Kind: KindCall, Engine: balance.EngineEVM, LocalOutcome: ResultRevert,
				Caller: addrPtr(corpusParent), Context: addrPtr(corpusChild), Code: addrPtr(corpusChild),
				MsgSender: addrPtr(corpusParent), Target: addrPtr(corpusChild),
				Value: Some(u256("5")), Gas: Some(uint64(30000)),
				ErrorCode:  Some("execution reverted"),
				RevertData: value32(0xee),
				Input:      []byte{}, Output: []byte{},
			},
		},
		Transfers: []TransferObservation{
			{
				FrameID: 0, From: corpusSender, To: corpusParent, Amount: u256("5"),
				Scope: 1, Committed: true,
				Effects: []balance.BalanceEffect{
					{EffectIndex: 0, Address: corpusSender, Before: u256("8999511799000000000"), After: u256("8999511798999999995")},
					{EffectIndex: 1, Address: corpusParent, Before: u256("0"), After: u256("5")},
				},
			},
			{
				FrameID: 1, From: corpusParent, To: corpusChild, Amount: u256("5"),
				Scope: 2, Committed: false,
				Effects: []balance.BalanceEffect{
					{EffectIndex: 0, Address: corpusParent, Before: u256("5"), After: u256("0")},
					{EffectIndex: 1, Address: corpusChild, Before: u256("0"), After: u256("5")},
				},
			},
		},
		Fee: FeeObservation{
			Payer: corpusSender, GasLimit: 200000, GasUsed: 142655, EffectiveGasPrice: gwei(2),
			Recipient: corpusCoinbase, BaseFee: u256("875000000"),
			PayerBalanceBefore:     u256("8999911799000000000"),
			PayerBalanceAtReturn:   Some(u256("8999511798999999995")),
			RecipientBalanceBefore: u256("51450000000000"),
		},
		Ops: []TxOp{
			{Kind: OpGasPurchase},
			{Kind: OpFrameEntry, FrameIndex: 0},
			{Kind: OpTransfer, TransferIndex: 0},
			{Kind: OpFrameEntry, FrameIndex: 1},
			{Kind: OpTransfer, TransferIndex: 1},
			{Kind: OpGasReturn},
			{Kind: OpFeeCredit},
		},
		State: []AccountObservation{
			{
				Address: corpusChild,
				// The child contract received 5 wei and stored 1 in slot 0
				// inside the call that reverted: both the value and the
				// storage write are rolled back, so the boundary points
				// are identical (expected/eth.json state_before/after).
				Before: AccountPoint{Exists: true, CodeHash: codeHashOf(corpusChildCode), Code: decodeHexOrEmpty(corpusChildCode),
					Storage: map[Hash32]Hash32{slotHash(0): {}}},
				After: AccountPoint{Exists: true, CodeHash: codeHashOf(corpusChildCode), Code: decodeHexOrEmpty(corpusChildCode),
					Storage: map[Hash32]Hash32{slotHash(0): {}}},
			},
			accountObservation(corpusCoinbase, true, "51450000000000", 0, "", true, "211936875000000", 0, ""),
			accountObservation(corpusParent, true, "0", 0, corpusParentCode, true, "5", 0, corpusParentCode),
			accountObservation(corpusSender, true, "8999911799000000000", 2, "", true, "8999626488999999995", 3, ""),
		},
	}
	return obs
}

// accountObservation builds one simple account observation without
// storage.
func accountObservation(addr Address20, existed bool, balanceBefore string, nonceBefore uint64, codeBefore string,
	existsAfter bool, balanceAfter string, nonceAfter uint64, codeAfter string) AccountObservation {
	before := AccountPoint{Exists: existed, Balance: u256(balanceBefore), Nonce: nonceBefore,
		Code: decodeHexOrEmpty(codeBefore), CodeHash: codeHashOf(codeBefore)}
	after := AccountPoint{Exists: existsAfter, Balance: u256(balanceAfter), Nonce: nonceAfter,
		Code: decodeHexOrEmpty(codeAfter), CodeHash: codeHashOf(codeAfter)}
	return AccountObservation{Address: addr, Before: before, After: after}
}

// TestCorpusEvidenceCrossCheck compares the embedded fixture literals with
// the retained oracle file when it is present, so drift between the
// literals and the evidence is caught in any checkout that carries
// test/native_corpus/expected/eth.json.
func TestCorpusEvidenceCrossCheck(t *testing.T) {
	path := "../../../test/native_corpus/expected/eth.json"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("the retained oracle file is not present: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("the retained oracle file is empty")
	}
	// The exact numeric anchors used by the tests below.
	for _, anchor := range []string{
		"875000000", "228538", "42000000000000", "23625000000000",
		"18375000000000", "160486875000000", "285310000000000",
		"8999958000000000000", "8999626488999999995", "8999538722999999995",
		"2000261305250000000", "4000261305250000000",
	} {
		if !contains(string(data), anchor) {
			t.Errorf("the retained oracle no longer contains the anchor %q used by the fixtures", anchor)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
