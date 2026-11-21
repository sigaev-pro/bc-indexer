package block

import (
	"reflect"
	"strings"
	"testing"
)

// ethFixtureMetadata builds the ETH block-1 metadata from the retained
// run-02 record: a post-Prague header with every optional fork field
// present.
func ethFixtureMetadata(t *testing.T) BlockMetadata {
	t.Helper()
	return BlockMetadata{
		Number:                 fixtureEthBlock1Number,
		Hash:                   mustHash32(t, fixtureEthBlock1Hash),
		ParentHash:             mustHash32(t, fixtureEthGenesis),
		Timestamp:              TimestampFromSeconds(fixtureEthBlock1Timestamp),
		SourceTransactionCount: fixtureEthBlock1TxCount,
		Header: Header{
			ParentHash:       mustHash32(t, fixtureEthGenesis),
			UncleHash:        mustHash32(t, fixtureEmptyUncleHash),
			Beneficiary:      mustAddr20(t, fixtureEthBlock1Coinbase),
			StateRoot:        mustHash32(t, fixtureEthBlock1State),
			TransactionsRoot: mustHash32(t, fixtureEthBlock1TxRoot),
			ReceiptsRoot:     mustHash32(t, fixtureEthBlock1RcptRoot),
			Difficulty:       u256(0), // present zero: post-merge difficulty.
			Number:           fixtureEthBlock1Number,
			GasLimit:         fixtureEthBlock1GasLimit,
			GasUsed:          fixtureEthBlock1GasUsed,
			TimestampSeconds: fixtureEthBlock1Timestamp,
			ExtraData:        nil,
			MixDigest:        Hash32{},
			BaseFee:          Some(u256(fixtureEthBlock1BaseFee)),
			WithdrawalsRoot:  Some(mustHash32(t, fixtureEmptyTrieRoot)),
			BlobGasUsed:      Some(uint64(fixtureEthBlock1BlobGas)),
			ExcessBlobGas:    Some(uint64(fixtureEthBlock1ExcessBlob)),
			ParentBeaconBlockRoot: Some(
				mustHash32(t, fixtureEthBlock1BeaconRoot)),
			RequestsHash: Some(mustHash32(t, fixtureEmptyRequestsHash)),
			SourceRLP:    mustBytes(t, fixtureEthBlock1SrcRLP),
		},
		Details: FamilyDetails{Eth: &EthBlockDetails{}},
	}
}

// borEmptyFixtureMetadata builds the empty BOR block-2 metadata from the
// retained run-04 record: zero transactions, zero gas used, no fork
// extension fields beyond base fee.
func borEmptyFixtureMetadata(t *testing.T) BlockMetadata {
	t.Helper()
	return BlockMetadata{
		Number:     2,
		Hash:       mustHash32(t, fixtureBorBlock2Hash),
		ParentHash: mustHash32(t, fixtureBorBlock2Parent),
		Timestamp:  TimestampFromSeconds(fixtureBorBlock2Timestamp),
		// The empty block is a first-class record: observed count 0.
		SourceTransactionCount: 0,
		Header: Header{
			ParentHash:       mustHash32(t, fixtureBorBlock2Parent),
			UncleHash:        mustHash32(t, fixtureEmptyUncleHash),
			Beneficiary:      mustAddr20(t, fixtureBorBlock2Coinbase),
			StateRoot:        mustHash32(t, fixtureBorBlock2State),
			TransactionsRoot: mustHash32(t, fixtureBorBlock2TxRoot),
			ReceiptsRoot:     mustHash32(t, fixtureBorBlock2RcptRoot),
			Difficulty:       u256(1),
			Number:           2,
			GasLimit:         fixtureBorBlock2GasLimit,
			GasUsed:          fixtureBorBlock2GasUsed, // zero: observed value.
			TimestampSeconds: fixtureBorBlock2Timestamp,
			ExtraData:        mustBytes(t, fixtureBorBlock2Extra),
			BaseFee:          Some(u256(fixtureBorBlock2BaseFee)),
			SourceRLP:        mustBytes(t, fixtureBorBlock2SrcRLP),
		},
		Details: FamilyDetails{Bor: &BorBlockDetails{}},
	}
}

func TestEmptyBlockRoundTrip(t *testing.T) {
	meta := borEmptyFixtureMetadata(t)
	if err := meta.Validate(); err != nil {
		t.Fatalf("empty block must validate: %v", err)
	}
	if meta.SourceTransactionCount != 0 {
		t.Fatalf("empty block must carry observed count 0, got %d", meta.SourceTransactionCount)
	}
	if meta.Header.GasUsed != 0 {
		t.Fatalf("empty block gas used must stay the observed zero")
	}

	data, err := Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var back BlockMetadata
	if err := Unmarshal(data, &back); err != nil {
		t.Fatalf("empty block round trip decode: %v", err)
	}
	if err := back.Validate(); err != nil {
		t.Fatalf("empty block must validate after round trip: %v", err)
	}
	if !reflect.DeepEqual(meta, back) {
		t.Fatal("empty block round trip changed the record")
	}

	// Absence must survive the round trip too: no withdrawals, beacon or
	// requests fields exist in this header.
	if back.Header.WithdrawalsRoot.Present() || back.Header.ParentBeaconBlockRoot.Present() || back.Header.RequestsHash.Present() {
		t.Fatal("absent fork fields must stay absent through the round trip")
	}
	if !back.Header.BaseFee.Present() {
		t.Fatal("present base fee must stay present")
	}
}

func TestEmptyBlockIsFirstClassRecord(t *testing.T) {
	// The BSC fixture block 6 is also empty and exercises the BSC
	// millisecond timestamp form with a nonzero nanos remainder.
	meta := BlockMetadata{
		Number:                 6,
		Hash:                   mustHash32(t, fixtureBscBlock6Hash),
		ParentHash:             mustHash32(t, fixtureBscBlock6Parent),
		Timestamp:              TimestampFromMilliseconds(fixtureBscBlock6Timestamp*1000 + 800),
		SourceTransactionCount: fixtureBscBlock6TxCount,
		Header: Header{
			ParentHash:       mustHash32(t, fixtureBscBlock6Parent),
			UncleHash:        mustHash32(t, fixtureEmptyUncleHash),
			Beneficiary:      mustAddr20(t, fixtureBscBlock6Coinbase),
			StateRoot:        mustHash32(t, fixtureBscBlock6State),
			TransactionsRoot: mustHash32(t, fixtureBscBlock6TxRoot),
			ReceiptsRoot:     mustHash32(t, fixtureBscBlock6RcptRoot),
			Difficulty:       u256(2),
			Number:           6,
			GasLimit:         fixtureBscBlock6GasLimit,
			GasUsed:          fixtureBscBlock6GasUsed,
			TimestampSeconds: fixtureBscBlock6Timestamp,
			ExtraData:        mustBytes(t, fixtureBscBlock6Extra),
			MixDigest:        mustHash32(t, fixtureBscBlock6Mix),
			BaseFee:          Some(u256(fixtureBscBlock6BaseFee)),
			WithdrawalsRoot:  Some(mustHash32(t, fixtureEmptyTrieRoot)),
			BlobGasUsed:      Some(uint64(fixtureBscBlock6BlobGas)),
			ExcessBlobGas:    Some(uint64(0)),
			// Present but zero: the fixture header carries the beacon
			// root field with the all-zero value.
			ParentBeaconBlockRoot: Some(Hash32{}),
			RequestsHash:          Some(mustHash32(t, fixtureEmptyRequestsHash)),
			SourceRLP:             mustBytes(t, fixtureBscBlock6SrcRLP),
		},
		Details: FamilyDetails{Bsc: &BscBlockDetails{}},
	}
	if meta.SourceTransactionCount != 0 {
		t.Fatalf("BSC empty block must carry count 0, got %d", meta.SourceTransactionCount)
	}
	if err := meta.Validate(); err != nil {
		t.Fatalf("BSC empty block must validate: %v", err)
	}

	data, err := Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var back BlockMetadata
	if err := Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(meta, back) {
		t.Fatal("BSC empty block round trip changed the record")
	}
	// Present zero beacon root must stay present and zero: zero is a
	// value, not absence.
	if !back.Header.ParentBeaconBlockRoot.Present() || !back.Header.ParentBeaconBlockRoot.MustGet().IsZero() {
		t.Fatal("present zero beacon root lost")
	}
}

func TestBlockMetadataValidation(t *testing.T) {
	base := ethFixtureMetadata(t)
	if err := base.Validate(); err != nil {
		t.Fatalf("ETH fixture must validate: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*BlockMetadata)
	}{
		{"zero hash", func(m *BlockMetadata) { m.Hash = Hash32{} }},
		{"zero parent above genesis", func(m *BlockMetadata) {
			m.ParentHash = Hash32{}
			m.Header.ParentHash = Hash32{}
		}},
		{"empty source rlp", func(m *BlockMetadata) { m.Header.SourceRLP = nil }},
		{"number mismatch", func(m *BlockMetadata) { m.Header.Number = 7 }},
		{"parent mismatch", func(m *BlockMetadata) { m.Header.ParentHash = mustHash32(t, fixtureBscGenesis) }},
		{"bad timestamp", func(m *BlockMetadata) { m.Timestamp = Timestamp{1, 1, PrecisionSeconds} }},
		{"no family variant", func(m *BlockMetadata) { m.Details = FamilyDetails{} }},
		{"two family variants", func(m *BlockMetadata) {
			m.Details.Bsc = &BscBlockDetails{}
		}},
	}
	for _, c := range cases {
		m := base
		c.mutate(&m)
		if err := m.Validate(); err == nil {
			t.Fatalf("%s must be rejected", c.name)
		}
	}

	// The zero parent hash is legal exactly for the genesis block number.
	genesis := base
	genesis.Number = 0
	genesis.Hash = mustHash32(t, fixtureEthGenesis)
	genesis.Header.Number = 0
	genesis.ParentHash = Hash32{}
	genesis.Header.ParentHash = Hash32{}
	if err := genesis.Validate(); err != nil {
		t.Fatalf("genesis parent zero hash must be allowed: %v", err)
	}
}

func TestUnknownJSONFieldRejected(t *testing.T) {
	meta := borEmptyFixtureMetadata(t)
	data, err := Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), `"number":2`, `"number":2,"block_complete":true`, 1)
	if tampered == string(data) {
		t.Fatal("tamper setup failed")
	}
	var back BlockMetadata
	if err := Unmarshal([]byte(tampered), &back); err == nil {
		t.Fatal("unknown field block_complete must be rejected by the local encoding")
	}
}

func TestFamilyDetailsOneOfJSON(t *testing.T) {
	raw := FamilyDetails{Eth: &EthBlockDetails{SlotNumber: Some(uint64(12))}}
	data, err := Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"family":"ETH"`) {
		t.Fatalf("family discriminator missing: %s", data)
	}
	var back FamilyDetails
	if err := Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Family() != FamilyEth || !back.Eth.SlotNumber.Present() || back.Eth.SlotNumber.MustGet() != 12 {
		t.Fatalf("family details round trip mismatch: %+v", back)
	}

	if err := Unmarshal([]byte(`{"family":"POLY","eth":{}}`), &back); err == nil {
		t.Fatal("family name must match the present variant")
	}
	if err := Unmarshal([]byte(`{"family":"AVM","eth":{}}`), &back); err == nil {
		t.Fatal("unknown family must be rejected without an ANY fallback")
	}
	if err := Unmarshal([]byte(`{}`), &back); err == nil {
		t.Fatal("missing family variant must be rejected")
	}
}
