package eth

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// corpusBlockObservation builds the observation of corpus block 1 or 2
// from expected/eth.json: base fee 875000000 (block 1) / 770624269
// (block 2), gas used 228538 / 0, four transactions / none.
func corpusBlockObservation(number uint64) BlockObservation {
	obs := BlockObservation{
		Hash:             patternHash(byte(number)),
		Number:           number,
		TransactionCount: 4,
	}
	h := &obs.Header
	parentHash := patternHash(byte(number - 1))
	h.ParentHash = parentHash
	copy(h.UncleHash[:], emptyRootHex)
	h.Beneficiary = corpusCoinbase
	h.StateRoot = patternHash(0x50)
	h.TransactionsRoot = patternHash(0x51)
	h.ReceiptsRoot = patternHash(0x52)
	h.Difficulty = u256("2")
	h.Number = number
	h.GasLimit = 10000000
	h.Time = 1000 + number
	h.MixDigest = patternHash(0x60)
	for i := range h.Nonce {
		h.Nonce[i] = byte(i)
	}
	h.BaseFee = Some(u256("875000000"))
	h.SourceRLP = []byte{0xf9, 0x02, 0xd4, byte(number)}
	if number == 2 {
		h.BaseFee = Some(u256("770624269"))
		h.GasUsed = 0
		obs.TransactionCount = 0
	} else {
		h.GasUsed = 228538
	}
	return obs
}

var emptyRootHex = mustDecodeHex("56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

func mustDecodeHex(s string) []byte {
	h, err := decodeFixedHex(s, 32)
	if err != nil {
		panic(err)
	}
	return h
}

// corpusSnapshot builds the minimal valid ETH lifecycle snapshot.
func corpusSnapshot() block.LifecycleSnapshot {
	return block.LifecycleSnapshot{
		NativeDecision: block.NativeDecisionRef{
			NodeInstanceID:   block.Id16(patternID16(1)),
			ProducerID:       block.Id16(patternID16(2)),
			RunID:            block.Id16(patternID16(3)),
			DecisionSequence: 7,
		},
		ObservedExecutionHead: block.AnchorRef{Height: 2, Hash: block.Hash32(patternHash(2))},
		Markers: block.FinalityMarkers{Eth: &block.EthereumMarkers{
			Safe: block.NotObserved(), Finalized: block.NotObserved(), SafeBasis: block.SafeBasisUnset,
		}},
	}
}

func corpusIdentity() BlockIdentity {
	return BlockIdentity{
		Network:      block.NetworkIdentity{Family: "EVM", ChainID: block.UInt256(u256("31337")), GenesisHash: block.Hash32(patternHash(0x0a))},
		ProducerID:   semantic.ID16(patternID16(2)),
		RunID:        semantic.ID16(patternID16(3)),
		DecisionSequ: 7,
		Source:       semantic.SourceRef{NodeInstanceID: semantic.ID16(patternID16(1)), WriterEpoch: semantic.ID16(patternID16(4)), SourceSequence: 1},
	}
}

func TestBuildBlockRecordNonemptyAndEmpty(t *testing.T) {
	id := corpusIdentity()
	admission := block.ObservationRef{ProducerID: block.Id16(patternID16(2)), RunID: block.Id16(patternID16(3)), ObservationSequence: 1}

	one, _, err := BuildBlockRecord(id, corpusBlockObservation(1), admission, corpusSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if one.Metadata.SourceTransactionCount != 4 {
		t.Errorf("block 1 count = %d, want 4", one.Metadata.SourceTransactionCount)
	}
	if one.Metadata.Header.GasUsed != 228538 {
		t.Errorf("block 1 gas used = %d, want 228538", one.Metadata.Header.GasUsed)
	}
	if baseFee, ok := one.Metadata.Header.BaseFee.Get(); !ok || blockDecimal(baseFee) != "875000000" {
		t.Errorf("block 1 base fee = %v %s, want the present 875000000", ok, blockDecimal(baseFee))
	}

	two, _, err := BuildBlockRecord(id, corpusBlockObservation(2), admission, corpusSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	// The empty block is an exact first-class record: count 0 is an
	// observed value, not an absence.
	if two.Metadata.SourceTransactionCount != 0 {
		t.Errorf("block 2 count = %d, want 0", two.Metadata.SourceTransactionCount)
	}
	if two.Metadata.Header.GasUsed != 0 {
		t.Errorf("block 2 gas used = %d, want 0", two.Metadata.Header.GasUsed)
	}
	if baseFee, ok := two.Metadata.Header.BaseFee.Get(); !ok || blockDecimal(baseFee) != "770624269" {
		t.Errorf("block 2 base fee = %v %s, want the present 770624269", ok, blockDecimal(baseFee))
	}
}

func TestBuildBlockRecordTimestampPrecisionIsSeconds(t *testing.T) {
	obs := corpusBlockObservation(1)
	obs.Header.Time = 1732000000
	id := corpusIdentity()
	record, _, err := BuildBlockRecord(id, obs, block.ObservationRef{ProducerID: block.Id16(patternID16(2)), RunID: block.Id16(patternID16(3))}, corpusSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	ts := record.Metadata.Timestamp
	if ts.Seconds != 1732000000 || ts.Nanos != 0 {
		t.Fatalf("timestamp = %d.%d, want whole seconds without nanos", ts.Seconds, ts.Nanos)
	}
	if ts.Precision != block.PrecisionSeconds {
		t.Fatalf("precision = %s, want SECONDS", ts.Precision)
	}
}

func TestBuildBlockRecordOptionalPresenceFollowsNativeHeader(t *testing.T) {
	obs := corpusBlockObservation(1)
	// A London-era header: withdrawals root, beacon root, requests hash,
	// blob fields and the Geth extensions are absent, and absence never
	// becomes a zero value.
	id := corpusIdentity()
	record, _, err := BuildBlockRecord(id, obs, block.ObservationRef{ProducerID: block.Id16(patternID16(2)), RunID: block.Id16(patternID16(3))}, corpusSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	h := record.Metadata.Header
	if h.WithdrawalsRoot.Present() || h.ParentBeaconBlockRoot.Present() || h.RequestsHash.Present() {
		t.Error("absent fork fields must stay absent")
	}
	if h.BlobGasUsed.Present() || h.ExcessBlobGas.Present() {
		t.Error("absent blob fields must stay absent")
	}
	details := record.Metadata.Details.Eth
	if details.BlockAccessListHash.Present() || details.SlotNumber.Present() {
		t.Error("absent Geth extensions must stay absent")
	}
	// A Prague-era header carries the new fields.
	prague := corpusBlockObservation(3)
	prague.Header.WithdrawalsRoot = Some(patternHash(0x71))
	prague.Header.BlobGasUsed = Some(uint64(0))
	prague.Header.ExcessBlobGas = Some(uint64(1))
	prague.Header.ParentBeaconBlockRoot = Some(patternHash(0x72))
	prague.Header.RequestsHash = Some(patternHash(0x73))
	prague.Header.BlockAccessListHash = Some(patternHash(0x74))
	prague.Header.SlotNumber = Some(uint64(9))
	record, _, err = BuildBlockRecord(id, prague, block.ObservationRef{ProducerID: block.Id16(patternID16(2)), RunID: block.Id16(patternID16(3))}, corpusSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	h = record.Metadata.Header
	if !h.WithdrawalsRoot.Present() || !h.RequestsHash.Present() {
		t.Error("present fork fields must stay present")
	}
	if v, ok := h.BlobGasUsed.Get(); !ok || v != 0 {
		t.Error("a present zero blob gas stays a present zero")
	}
	if !record.Metadata.Details.Eth.BlockAccessListHash.Present() {
		t.Error("the Geth block access list hash belongs to the ETH details")
	}
}

func TestBuildBlockRecordRequiresSourceRLP(t *testing.T) {
	obs := corpusBlockObservation(1)
	obs.Header.SourceRLP = nil
	id := corpusIdentity()
	if _, _, err := BuildBlockRecord(id, obs, block.ObservationRef{ProducerID: block.Id16(patternID16(2)), RunID: block.Id16(patternID16(3))}, corpusSnapshot()); !errors.Is(err, ErrBlockShape) {
		t.Fatalf("missing source_rlp error = %v, want ErrBlockShape", err)
	}
}

func TestBuildBlockRecordDescriptorIsBlockApplied(t *testing.T) {
	id := corpusIdentity()
	_, descriptor, err := BuildBlockRecord(id, corpusBlockObservation(1),
		block.ObservationRef{ProducerID: block.Id16(patternID16(2)), RunID: block.Id16(patternID16(3))}, corpusSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Family != semantic.FamilyBlock || descriptor.Kind != semantic.KindApplied {
		t.Fatalf("descriptor = %s/%s, want block/applied", descriptor.Family, descriptor.Kind)
	}
	if descriptor.Activation.ActivationID.EntityOrdinal != 0 || descriptor.Revision != 0 {
		t.Fatalf("descriptor entity = %d revision = %d, want ordinal 0 revision 0",
			descriptor.Activation.ActivationID.EntityOrdinal, descriptor.Revision)
	}
	if err := descriptor.Validate(); err != nil {
		t.Fatal(err)
	}
}
