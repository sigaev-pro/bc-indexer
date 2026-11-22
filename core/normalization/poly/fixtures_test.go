package poly

import (
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// Fixture builders for the area tests. The shapes come from the retained
// E01 evidence: the Madhugiri crossing case of
// .project/evidence/E01-poly-madhugiri-design-20261121.md (block 2, sprint
// 2, body [legacy transfer, 0x7f with two records: a successful target and
// a caught revert]) and the pre-Madhugiri corpus case of
// test/native_corpus/poly (block 4, sprint 4, empty body, one state-sync
// event, separate Bor receipt). Byte-level vectors that pin the encoders
// come from the independent E01 codec and live in rlp_test.go and
// statesync_test.go.

// Fixture addresses of the E01 corpus.
var (
	fixtureSender     = mustAddress("0x7e5f4552091a69125d5dfcb7b8c2659029395bdf")
	fixtureCoinbase   = mustAddress("0x71562b71999873db5b286df957af199ec94617f7")
	fixtureSuccess    = mustAddress("0x4000000000000000000000000000000000000001")
	fixtureRevert     = mustAddress("0x3000000000000000000000000000000000000002")
	fixtureTarget     = mustAddress("0x3000000000000000000000000000000000000003")
	fixtureReceiver   = mustAddress("0x0000000000000000000000000000000000001001")
	fixtureSystem     = SystemCallerAddress
	fixtureBlockHash  = mustHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	fixtureParentHash = mustHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	fixtureData       = []byte("extractor-corpus-state-sync-1")
)

var (
	gwei     = balance.UInt256FromUint64(1_000_000_000)
	fiveGwei = balance.MustParseUInt256("5000000000")
	tenMatic = balance.MustParseUInt256("10000000000000000000")
)

const (
	purchaseAmount = 105_000_000_000_000 // 21000 gas at 5 gwei
	tipAmount      = 84_000_000_000_000  // 21000 gas at 4 gwei
	burnAmount     = 21_000_000_000_000  // 21000 gas at 1 gwei base fee
	transferValue  = 5
)

// fixtureBloom computes the receipt bloom the way the pinned native path
// does: keccak of the address and every topic, three bit pairs each.
func fixtureBloom(logs []LogCapture) Bloom {
	var bloom Bloom
	setBits := func(item []byte) {
		digest := Keccak256(item)
		for _, offset := range []int{0, 2, 4} {
			bit := (uint(digest[offset])<<8 | uint(digest[offset+1])) & 2047
			bloom[256-1-bit/8] |= 1 << (bit % 8)
		}
	}
	for _, log := range logs {
		setBits(log.Address[:])
		for _, topic := range log.Topics {
			setBits(topic[:])
		}
	}
	return bloom
}

var oneData = Hash32(balance.UInt256FromUint64(1))

func fixtureLog(addr Address20, dataValue uint64) LogCapture {
	topic := StateSyncEventLogTopic
	data := balance.UInt256FromUint64(dataValue)
	return LogCapture{
		Address:             addr,
		Topics:              []Hash32{topic},
		Data:                data[:],
		TransactionLogIndex: 0,
		BlockLogIndex:       0,
	}
}

func fixtureReceiptWire(t *testing.T, typeByte *byte, cumulative uint64, bloom Bloom, logs []LogCapture) []byte {
	t.Helper()
	var logEncodings [][]byte
	for _, log := range logs {
		topics := make([][]byte, 0, len(log.Topics))
		for _, topic := range log.Topics {
			topics = append(topics, rlpEncodeBytes(topic[:]))
		}
		logEncodings = append(logEncodings, rlpEncodeList(
			rlpEncodeBytes(log.Address[:]),
			rlpEncodeList(topics...),
			rlpEncodeBytes(log.Data),
		))
	}
	receipt := rlpEncodeList(
		rlpEncodeBytes([]byte{1}),
		rlpEncodeUint(cumulative),
		rlpEncodeBytes(bloom[:]),
		rlpEncodeList(logEncodings...),
	)
	if typeByte == nil {
		return receipt
	}
	return append([]byte{*typeByte}, receipt...)
}

// repeatHash builds the test code hash 0xNNNN.. from one repeated hex
// digit, for fixture accounts whose exact code hash is irrelevant.
func repeatHash(digit string) Hash32 {
	if len(digit) != 1 {
		panic("fixture: one hex digit only")
	}
	var d byte
	switch {
	case digit[0] >= '0' && digit[0] <= '9':
		d = digit[0] - '0'
	case digit[0] >= 'a' && digit[0] <= 'f':
		d = digit[0] - 'a' + 10
	default:
		panic("fixture: bad hex digit")
	}
	var out Hash32
	b := d<<4 | d
	for i := range out {
		out[i] = b
	}
	return out
}

func boolPtr(v bool) *bool { return &v }

// crossingLegacyTx builds the legacy transfer of the crossing body: one
// successful 21000-gas transfer with purchase, tip and burn fee actions.
func crossingLegacyTx(t *testing.T) TxCapture {
	t.Helper()
	wire := []byte{0x02, 0xee, 0xff} // a legacy envelope; only its hash matters here
	return TxCapture{
		Position:             0,
		BodyTransactionCount: 2,
		BlockNumber:          2,
		NativeType:           0,
		Wire:                 wire,
		ObservedTxHash:       Keccak256(wire),
		Sender:               addrPtr(fixtureSender),
		Message: &EvmMessageCapture{
			Sender:            fixtureSender,
			SenderProvenance:  ProvenanceNativeDerived,
			Target:            addrPtr(fixtureSuccess),
			TargetProvenance:  ProvenanceEncoded,
			Value:             balance.UInt256FromUint64(transferValue),
			ValueProvenance:   ProvenanceEncoded,
			Input:             []byte{},
			InputProvenance:   ProvenanceEncoded,
			Nonce:             0,
			NonceProvenance:   ProvenanceEncoded,
			GasLimit:          21000,
			EffectiveGasPrice: fiveGwei,
		},
		Frames: []FrameCapture{{
			Entry:          2,
			Parent:         -1,
			Depth:          0,
			Engine:         balance.EngineEVM,
			Kind:           FrameCall,
			CallerAddress:  addrPtr(fixtureSender),
			ContextAddress: addrPtr(fixtureSuccess),
			Target:         addrPtr(fixtureSuccess),
			Value:          u256Ptr(balance.UInt256FromUint64(transferValue)),
			Outcome:        OutcomeSuccess,
			Committed:      true,
		}},
		Operations: []OpCapture{
			{
				Sequence:  1,
				Cause:     OpCauseProtocol,
				Operation: balance.OpGasPurchase,
				From:      addrPtr(fixtureSender),
				Amount:    balance.UInt256FromUint64(purchaseAmount),
				Effects: []EffectCapture{{
					Address: fixtureSender,
					Before:  tenMatic,
					After:   balance.MustParseUInt256("9999895000000000000"),
				}},
				Committed: true,
				Snapshot:  -1,
			},
			{
				Sequence: 3,
				Cause:    OpCauseFrame,
				Frame:    0,
				From:     addrPtr(fixtureSender),
				To:       addrPtr(fixtureSuccess),
				Amount:   balance.UInt256FromUint64(transferValue),
				Effects: []EffectCapture{
					{
						Address: fixtureSender,
						Before:  balance.MustParseUInt256("9999895000000000000"),
						After:   balance.MustParseUInt256("9999894999999999995"),
					},
					{
						Address: fixtureSuccess,
						Before:  balance.UInt256FromUint64(0),
						After:   balance.UInt256FromUint64(transferValue),
					},
				},
				Committed: true,
				Snapshot:  0,
			},
			{
				Sequence:  4,
				Cause:     OpCauseProtocol,
				Operation: balance.OpFeeRecipientCredit,
				To:        addrPtr(fixtureCoinbase),
				Amount:    balance.UInt256FromUint64(tipAmount),
				Effects: []EffectCapture{{
					Address: fixtureCoinbase,
					Before:  balance.UInt256FromUint64(0),
					After:   balance.UInt256FromUint64(tipAmount),
				}},
				Committed: true,
				Snapshot:  -1,
			},
			{
				Sequence:  5,
				Cause:     OpCauseProtocol,
				Operation: balance.OpFeeRecipientCredit,
				To:        addrPtr(BurnAddress),
				Amount:    balance.UInt256FromUint64(burnAmount),
				Effects: []EffectCapture{{
					Address: BurnAddress,
					Before:  balance.UInt256FromUint64(0),
					After:   balance.UInt256FromUint64(burnAmount),
				}},
				Committed: true,
				Snapshot:  -1,
			},
		},
		Snapshots: []SnapshotCapture{{
			NativeID: 1, Parent: -1, OwnerFrame: 0, RolledBack: false,
		}},
		Receipt: ReceiptCapture{
			Status:            boolPtr(true),
			Result:            ResultSuccess,
			CumulativeGasUsed: 21000,
			GasUsed:           u64Ptr(21000),
			EffectiveGasPrice: &fiveGwei,
			Logs:              nil,
			Consensus:         nil, // set by the caller: needs the bloom helper
		},
		Accounts: []AccountBoundaryCapture{
			{
				Address: fixtureSender, ExistedBefore: true, ExistsAfter: true,
				BalanceBefore: tenMatic,
				BalanceAfter:  balance.MustParseUInt256("9999894999999999995"),
				NonceBefore:   0, NonceAfter: 1,
			},
			{
				Address: fixtureSuccess, ExistedBefore: true, ExistsAfter: true,
				BalanceBefore: balance.UInt256FromUint64(0),
				BalanceAfter:  balance.UInt256FromUint64(transferValue),
			},
			{
				Address: fixtureCoinbase, ExistedBefore: true, ExistsAfter: true,
				BalanceBefore: balance.UInt256FromUint64(0),
				BalanceAfter:  balance.UInt256FromUint64(tipAmount),
			},
			{
				Address: BurnAddress, ExistedBefore: true, ExistsAfter: true,
				BalanceBefore: balance.UInt256FromUint64(0),
				BalanceAfter:  balance.UInt256FromUint64(burnAmount),
			},
		},
		Forks: ForkState{MadhugiriBlock: 2, MadhugiriActive: true, ChainID: "15001"},
	}
}

func u256Ptr(v UInt256) *UInt256 { return &v }

// crossingStateSyncTx builds the 0x7f body transaction of the crossing
// body from the retained native observation: the synthetic root frame, two
// receiver commits with their nested target calls (the second one a caught
// revert) and the one-log receipt.
func crossingStateSyncTx(t *testing.T) TxCapture {
	t.Helper()
	wire := mustHex(t, twoItemStateSyncWire)
	l1One := mustHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	l1Two := mustHash("0x0000000000000000000000000000000000000000000000000000000000000002")

	clerkOne := ClerkEventCapture{
		ID: 1, Contract: fixtureTarget, Data: fixtureData, TxHash: l1One,
		LogIndex: 1, ChainID: "15001",
	}
	clerkTwo := ClerkEventCapture{
		ID: 2, Contract: fixtureRevert, Data: fixtureData, TxHash: l1Two,
		LogIndex: 2, ChainID: "15001",
	}

	logs := []LogCapture{fixtureLog(fixtureTarget, 1)}
	bloom := fixtureBloom(logs)
	typeByte := StateSyncTxType
	errText := "execution reverted"
	revertData := make([]byte, 32)
	revertData[31] = 0xee

	return TxCapture{
		Position:             1,
		BodyTransactionCount: 2,
		BlockNumber:          2,
		NativeType:           StateSyncTxType,
		Wire:                 wire,
		ObservedTxHash:       Keccak256(wire),
		StateSync: &StateSyncCapture{
			Records: []StateSyncRecordCapture{
				{ID: 1, Contract: fixtureTarget, Data: fixtureData, L1TxHash: l1One, FullEvent: &clerkOne},
				{ID: 2, Contract: fixtureRevert, Data: fixtureData, L1TxHash: l1Two, FullEvent: &clerkTwo},
			},
			Receiver:          fixtureReceiver,
			RecordTime:        1558348306,
			LastStateIDBefore: 0,
		},
		Frames: []FrameCapture{
			{
				Entry: 1, Parent: -1, Depth: 0,
				Engine: balance.EngineNativeProtocol, Kind: FrameNativeProtocol,
				CallerAddress: addrPtr(fixtureSystem), ContextAddress: addrPtr(fixtureReceiver),
				Target: addrPtr(fixtureReceiver), Outcome: OutcomeSuccess, Committed: true,
			},
			{
				Entry: 2, Parent: 0, Depth: 1,
				Engine: balance.EngineEVM, Kind: FrameCall,
				CallerAddress: addrPtr(fixtureSystem), ContextAddress: addrPtr(fixtureReceiver),
				Target: addrPtr(fixtureReceiver), Input: commitCallInput(1558348306, clerkOne),
				Outcome: OutcomeSuccess, Committed: true,
			},
			{
				Entry: 3, Parent: 1, Depth: 2,
				Engine: balance.EngineEVM, Kind: FrameCall,
				CallerAddress: addrPtr(fixtureReceiver), ContextAddress: addrPtr(fixtureTarget),
				Target: addrPtr(fixtureTarget), Input: onStateReceiveInput(1, fixtureData),
				Outcome: OutcomeSuccess, Committed: true,
			},
			{
				Entry: 4, Parent: 0, Depth: 1,
				Engine: balance.EngineEVM, Kind: FrameCall,
				CallerAddress: addrPtr(fixtureSystem), ContextAddress: addrPtr(fixtureReceiver),
				Target: addrPtr(fixtureReceiver), Input: commitCallInput(1558348306, clerkTwo),
				Outcome: OutcomeSuccess, Committed: true,
			},
			{
				Entry: 5, Parent: 3, Depth: 2,
				Engine: balance.EngineEVM, Kind: FrameCall,
				CallerAddress: addrPtr(fixtureReceiver), ContextAddress: addrPtr(fixtureRevert),
				Target: addrPtr(fixtureRevert), Input: onStateReceiveInput(2, fixtureData),
				Outcome: OutcomeRevert, ErrorCode: &errText, RevertData: revertData,
			},
		},
		Receipt: ReceiptCapture{
			Status:            boolPtr(true),
			Result:            ResultSuccess,
			CumulativeGasUsed: 21000,
			GasUsed:           u64Ptr(0),
			Logs:              logs,
			LogsBloom:         bloom,
			Consensus:         fixtureReceiptWire(t, &typeByte, 21000, bloom, logs),
			LogScopes:         []LogScopeCapture{{TransactionLogIndex: 0, Scope: LogScopeTXStateSync}},
		},
		Accounts: []AccountBoundaryCapture{
			{
				Address: fixtureReceiver, ExistedBefore: true, ExistsAfter: true,
				CodeHashBefore: repeatHash("a"), CodeHashAfter: repeatHash("a"),
				Storage: []StorageBoundaryCapture{{
					Key: Hash32{}, Before: Hash32{}, After: Hash32(balance.UInt256FromUint64(2)),
				}},
			},
			{
				Address: fixtureTarget, ExistedBefore: true, ExistsAfter: true,
				CodeHashBefore: repeatHash("b"), CodeHashAfter: repeatHash("b"),
				Storage: []StorageBoundaryCapture{{
					Key: Hash32{}, Before: Hash32{}, After: Hash32(balance.UInt256FromUint64(1)),
				}},
			},
		},
		Forks: ForkState{MadhugiriBlock: 2, MadhugiriActive: true, ChainID: "15001"},
	}
}

// crossingBlock builds the full Madhugiri crossing block capture.
func crossingBlock(t *testing.T) BlockCapture {
	t.Helper()
	legacy := crossingLegacyTx(t)
	legacy.Receipt.Consensus = fixtureReceiptWire(t, nil, 21000, fixtureBloom(nil), nil)
	stateSync := crossingStateSyncTx(t)
	baseFee := gwei
	return BlockCapture{
		Number:      2,
		Hash:        fixtureBlockHash,
		ParentHash:  fixtureParentHash,
		TimeSeconds: 1558348307,
		Header: HeaderCapture{
			ParentHash:       fixtureParentHash,
			Beneficiary:      fixtureCoinbase,
			Number:           2,
			GasLimit:         20000000,
			GasUsed:          21000,
			TimestampSeconds: 1558348307,
			BaseFee:          &baseFee,
			SourceRLP:        []byte{0xf9, 0x01, 0x00},
		},
		Transactions:  []TxCapture{legacy, stateSync},
		SprintLength:  2,
		SprintEnd:     true,
		StateSyncRows: 2,
		HeimdallCheckpoints: []HeimdallRefCapture{{
			Kind: HeimdallMilestone, EndHeight: 2, EndHash: fixtureBlockHash,
		}},
		Forks: ForkState{MadhugiriBlock: 2, MadhugiriActive: true, ChainID: "15001"},
	}
}

// legacyCorpusBlock builds the pre-Madhugiri corpus block 4 capture: an
// empty body whose state-sync work is the separate Bor receipt.
func legacyCorpusBlock(t *testing.T) BlockCapture {
	t.Helper()
	logs := []LogCapture{fixtureLog(fixtureTarget, 1)}
	storage := rlpEncodeList(
		rlpEncodeBytes([]byte{1}),
		rlpEncodeUint(0),
		rlpEncodeList(rlpEncodeList(
			rlpEncodeBytes(fixtureTarget[:]),
			rlpEncodeList(rlpEncodeBytes(StateSyncEventLogTopic[:])),
			rlpEncodeBytes(oneData[:]),
		)),
	)
	return BlockCapture{
		Number:      4,
		Hash:        fixtureBlockHash,
		ParentHash:  fixtureParentHash,
		TimeSeconds: 1558348312,
		Header: HeaderCapture{
			ParentHash:       fixtureParentHash,
			Beneficiary:      fixtureCoinbase,
			Number:           4,
			GasLimit:         20000000,
			GasUsed:          0,
			TimestampSeconds: 1558348312,
			SourceRLP:        []byte{0xf9, 0x01, 0x00},
		},
		SprintLength:  4,
		SprintEnd:     true,
		StateSyncRows: 1,
		LegacyReceipt: &LegacyReceiptCapture{
			BlockNumber:          4,
			BlockHash:            fixtureBlockHash,
			Present:              true,
			StorageRLP:           storage,
			ObservedHash:         DeriveBorReceiptHash(4, fixtureBlockHash),
			OrdinaryReceiptCount: 0,
			Status:               1,
			CumulativeGasUsed:    0,
			Logs:                 logs,
			LogPhases:            []LogPhaseCapture{{TransactionLogIndex: 0, Phase: LogScopeTXStateSync}},
		},
		Forks: ForkState{MadhugiriBlock: 8, MadhugiriActive: true, ChainID: "15001"},
	}
}
