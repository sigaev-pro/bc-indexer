package block

import (
	"reflect"
	"testing"
)

// The tests in this file pin per-family field presence against values
// extracted from the retained E01-05 run records (see
// fixtures_data_test.go for the provenance). They check what the schema
// must express; they are not native run evidence.

// TestEthFixturePresence pins the ETH post-Prague header shape of the
// retained run-02 block 1: every optional fork field is present, including
// the beacon root and the requests hash, and present zero values stay
// present.
func TestEthFixturePresence(t *testing.T) {
	meta := ethFixtureMetadata(t)
	if err := meta.Validate(); err != nil {
		t.Fatalf("ETH fixture must validate: %v", err)
	}

	h := &meta.Header
	if !h.RequestsHash.Present() {
		t.Fatal("post-Prague header must carry the requests hash")
	}
	if got := h.RequestsHash.MustGet().String(); got != fixtureEmptyRequestsHash {
		t.Fatalf("requests hash mismatch: %s", got)
	}
	if !h.ParentBeaconBlockRoot.Present() {
		t.Fatal("post-merge header must carry the parent beacon block root")
	}
	if got := h.ParentBeaconBlockRoot.MustGet().String(); got != fixtureEthBlock1BeaconRoot {
		t.Fatalf("beacon root mismatch: %s", got)
	}
	if !h.WithdrawalsRoot.Present() || !h.BlobGasUsed.Present() || !h.ExcessBlobGas.Present() || !h.BaseFee.Present() {
		t.Fatal("Shanghai/Cancun fork fields must be present")
	}
	if h.ExcessBlobGas.MustGet() != 0 {
		t.Fatalf("excess blob gas must keep the observed zero, got %d", h.ExcessBlobGas.MustGet())
	}
	if h.Difficulty.IsZero() {
		// Present zero difficulty is the post-merge value; it must not be
		// read as an absent field.
		if !reflect.DeepEqual(h.Difficulty, (UInt256{})) {
			t.Fatal("unreachable")
		}
	}
	if meta.Timestamp != (Timestamp{Seconds: fixtureEthBlock1Timestamp, Nanos: 0, Precision: PrecisionSeconds}) {
		t.Fatalf("ETH timestamp must preserve whole seconds: %+v", meta.Timestamp)
	}
	if meta.SourceTransactionCount != fixtureEthBlock1TxCount {
		t.Fatalf("source count mismatch: %d", meta.SourceTransactionCount)
	}
	if meta.Details.Family() != FamilyEth {
		t.Fatal("family mismatch")
	}
	// Geth-only extensions were not present in this fixture header.
	if meta.Details.Eth.BlockAccessListHash.Present() || meta.Details.Eth.SlotNumber.Present() {
		t.Fatal("unexpected Geth extensions in the fixture")
	}
}

// bscFixtureMetadata builds a BSC block metadata from the retained run-09
// record.
func bscFixtureMetadata(t *testing.T, block int) BlockMetadata {
	t.Helper()
	blocks := map[int]struct {
		number                                                                  uint64
		hash, parent, state, txRoot, rcptRoot, coinbase, mix, extra, bloom, rlp string
		gasUsed, gasLimit, timestamp, txCount, baseFee, blobGas                 uint64
		beaconZero                                                              bool
	}{
		1: {1, fixtureBscBlock1Hash, fixtureBscGenesis, fixtureBscBlock1State,
			fixtureBscBlock1TxRoot, fixtureBscBlock1RcptRoot, fixtureBscBlock1Coinbase,
			fixtureBscBlock1Mix, fixtureBscBlock1Extra, fixtureBscBlock1Bloom, fixtureBscBlock1SrcRLP,
			fixtureBscBlock1GasUsed, fixtureBscBlock1GasLimit, fixtureBscBlock1Timestamp,
			fixtureBscBlock1TxCount, fixtureBscBlock1BaseFee, 0, false},
		6: {6, fixtureBscBlock6Hash, fixtureBscBlock6Parent, fixtureBscBlock6State,
			fixtureBscBlock6TxRoot, fixtureBscBlock6RcptRoot, fixtureBscBlock6Coinbase,
			fixtureBscBlock6Mix, fixtureBscBlock6Extra, fixtureBscBlock6Bloom, fixtureBscBlock6SrcRLP,
			fixtureBscBlock6GasUsed, fixtureBscBlock6GasLimit, fixtureBscBlock6Timestamp,
			fixtureBscBlock6TxCount, fixtureBscBlock6BaseFee, fixtureBscBlock6BlobGas, true},
		15: {15, fixtureBscBlock15Hash, fixtureBscBlock15Parent, fixtureBscBlock15State,
			fixtureBscBlock15TxRoot, fixtureBscBlock15RcptRoot, fixtureBscBlock15Coinbase,
			fixtureBscBlock15Mix, fixtureBscBlock15Extra, fixtureBscBlock15Bloom, fixtureBscBlock15SrcRLP,
			fixtureBscBlock15GasUsed, fixtureBscBlock15GasLimit, fixtureBscBlock15Timestamp,
			fixtureBscBlock15TxCount, fixtureBscBlock15BaseFee, fixtureBscBlock15BlobGas, true},
	}
	f, ok := blocks[block]
	if !ok {
		t.Fatalf("no BSC fixture for block %d", block)
	}

	h := Header{
		ParentHash:       mustHash32(t, f.parent),
		UncleHash:        mustHash32(t, fixtureEmptyUncleHash),
		Beneficiary:      mustAddr20(t, f.coinbase),
		StateRoot:        mustHash32(t, f.state),
		TransactionsRoot: mustHash32(t, f.txRoot),
		ReceiptsRoot:     mustHash32(t, f.rcptRoot),
		LogsBloom:        Bloom(mustBytes256(t, f.bloom)),
		Difficulty:       u256(2),
		Number:           f.number,
		GasLimit:         f.gasLimit,
		GasUsed:          f.gasUsed,
		TimestampSeconds: f.timestamp,
		ExtraData:        mustBytes(t, f.extra),
		MixDigest:        mustHash32(t, f.mix),
		BaseFee:          Some(u256(f.baseFee)),
		SourceRLP:        mustBytes(t, f.rlp),
	}
	if block != 1 {
		h.WithdrawalsRoot = Some(mustHash32(t, fixtureEmptyTrieRoot))
		h.BlobGasUsed = Some(f.blobGas)
		h.ExcessBlobGas = Some(uint64(0))
		h.ParentBeaconBlockRoot = Some(Hash32{}) // present zero in the fixture.
		h.RequestsHash = Some(mustHash32(t, fixtureEmptyRequestsHash))
	}

	return BlockMetadata{
		Number:                 f.number,
		Hash:                   mustHash32(t, f.hash),
		ParentHash:             mustHash32(t, f.parent),
		Timestamp:              TimestampFromMilliseconds(f.timestamp*1000 + milliRemainder(block)),
		SourceTransactionCount: f.txCount,
		Header:                 h,
		Details:                FamilyDetails{Bsc: &BscBlockDetails{}},
	}
}

// mustBytes256 decodes 512 hex digits into a Bloom.
func mustBytes256(t *testing.T, s string) [256]byte {
	t.Helper()
	b := mustBytes(t, s)
	if len(b) != 256 {
		t.Fatalf("bloom fixture has %d bytes", len(b))
	}
	var out [256]byte
	copy(out[:], b)
	return out
}

// milliRemainder returns the millisecond remainder the fixture BSC header
// carries in low_u64(MixDigest), in whole milliseconds.
func milliRemainder(block int) uint64 {
	switch block {
	case 1:
		return fixtureBscBlock1MilliNanos / 1_000_000
	case 6:
		return fixtureBscBlock6MilliNanos / 1_000_000
	case 15:
		return fixtureBscBlock15MilliNanos / 1_000_000
	}
	return 0
}

// TestBscFixturePresence pins the BSC header shapes of the retained run-09
// record: the 16-field pre-London shape of block 1 versus the 21-field
// shape of blocks 6 and 15, the derived millisecond timestamp, and the
// Parlia system-transaction count observation.
func TestBscFixturePresence(t *testing.T) {
	// Block 1: 16 header fields. The base fee is present with value zero;
	// every later fork field is absent.
	b1 := bscFixtureMetadata(t, 1)
	if err := b1.Validate(); err != nil {
		t.Fatalf("BSC block 1 must validate: %v", err)
	}
	if !b1.Header.BaseFee.Present() || b1.Header.BaseFee.MustGet() != u256(0) {
		t.Fatal("BSC block 1 base fee is present zero, not absent")
	}
	if b1.Header.WithdrawalsRoot.Present() || b1.Header.RequestsHash.Present() ||
		b1.Header.BlobGasUsed.Present() || b1.Header.ParentBeaconBlockRoot.Present() {
		t.Fatal("block 1 predates the 21-field shape; those fields must be absent")
	}
	if b1.Timestamp.Precision != PrecisionMilliseconds || b1.Timestamp.Nanos != fixtureBscBlock1MilliNanos {
		t.Fatalf("BSC timestamp must be milliseconds: %+v", b1.Timestamp)
	}

	// Blocks 6 and 15: the 21-field shape with a present zero beacon root.
	for _, block := range []int{6, 15} {
		meta := bscFixtureMetadata(t, block)
		if err := meta.Validate(); err != nil {
			t.Fatalf("BSC block %d must validate: %v", block, err)
		}
		if !meta.Header.RequestsHash.Present() || !meta.Header.WithdrawalsRoot.Present() {
			t.Fatalf("block %d must carry the 21-field fork extensions", block)
		}
		if !meta.Header.ParentBeaconBlockRoot.Present() || !meta.Header.ParentBeaconBlockRoot.MustGet().IsZero() {
			t.Fatalf("block %d carries a present zero beacon root", block)
		}
		if !meta.Header.BlobGasUsed.Present() {
			t.Fatalf("block %d blob gas used must be present", block)
		}
	}

	// The empty block 6 and the type-matrix block 15 keep their exact
	// observed counts; the count is an observation with no minimum.
	b6 := bscFixtureMetadata(t, 6)
	if b6.SourceTransactionCount != 0 || b6.Header.GasUsed != 0 {
		t.Fatalf("BSC empty block must keep observed zeros: count %d gas %d", b6.SourceTransactionCount, b6.Header.GasUsed)
	}
	b15 := bscFixtureMetadata(t, 15)
	b15.Details.Bsc.ParliaSystemTransactionCount = Some(uint64(fixtureBscBlock15SystemTx))
	if err := b15.Validate(); err != nil {
		t.Fatal(err)
	}

	// The Parlia attested anchors observed with the block-1 import cite the
	// genesis snapshot: references, not finality truth.
	b1.Details.Bsc.ParliaSystemTransactionCount = Some(uint64(fixtureBscBlock1SystemTx))
	b1.Details.Bsc.VoteSnapshot = Some(ParliaVoteSnapshot{
		Justified: AnchorRef{Height: 0, Hash: mustHash32(t, fixtureBscGenesis)},
		Finalized: AnchorRef{Height: 0, Hash: mustHash32(t, fixtureBscGenesis)},
	})
	if err := b1.Validate(); err != nil {
		t.Fatalf("vote snapshot reference must validate: %v", err)
	}
	data, err := Marshal(b1)
	if err != nil {
		t.Fatal(err)
	}
	var back BlockMetadata
	if err := Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b1, back) {
		t.Fatal("BSC fixture round trip changed the record")
	}
}

// borSprintFixtureMetadata builds the BOR block-4 metadata of the retained
// run-04 record: the sprint boundary carrying one body StateSync
// transaction with two committed items.
func borSprintFixtureMetadata(t *testing.T) BlockMetadata {
	t.Helper()
	return BlockMetadata{
		Number:     4,
		Hash:       mustHash32(t, fixtureBorBlock4Hash),
		ParentHash: mustHash32(t, fixtureBorBlock4Parent),
		Timestamp:  TimestampFromSeconds(fixtureBorBlock4Timestamp),
		// One body transaction: the post-Madhugiri StateSync transaction.
		SourceTransactionCount: fixtureBorBlock4TxCount,
		Header: Header{
			ParentHash:       mustHash32(t, fixtureBorBlock4Parent),
			UncleHash:        mustHash32(t, fixtureEmptyUncleHash),
			Beneficiary:      mustAddr20(t, fixtureBorBlock4Coinbase),
			StateRoot:        mustHash32(t, fixtureBorBlock4State),
			TransactionsRoot: mustHash32(t, fixtureBorBlock4TxRoot),
			ReceiptsRoot:     mustHash32(t, fixtureBorBlock4RcptRoot),
			LogsBloom:        Bloom(mustBytes256(t, fixtureBorBlock4Bloom)),
			Difficulty:       u256(1),
			Number:           4,
			GasLimit:         fixtureBorBlock4GasLimit,
			GasUsed:          fixtureBorBlock4GasUsed, // zero even with a body tx.
			TimestampSeconds: fixtureBorBlock4Timestamp,
			ExtraData:        mustBytes(t, fixtureBorBlock4Extra),
			MixDigest:        mustHash32(t, fixtureBorBlock4Mix),
			BaseFee:          Some(u256(fixtureBorBlock4BaseFee)),
			SourceRLP:        mustBytes(t, fixtureBorBlock4SrcRLP),
		},
		Details: FamilyDetails{Bor: &BorBlockDetails{
			SprintLength:   Some(uint64(fixtureBorSprintLength)),
			SprintEnd:      Some(true),
			StateSyncCount: Some(uint64(fixtureBorBlock4StateSyncCount)),
			StateSyncMode:  Some(StateSyncBodyTransaction),
		}},
	}
}

// TestBorFixturePresence pins the Polygon (Bor) shapes of the retained
// run-04 record: the empty block 2, the sprint-boundary block 4 with its
// body StateSync observation, and a Heimdall checkpoint reference.
func TestBorFixturePresence(t *testing.T) {
	empty := borEmptyFixtureMetadata(t)
	if err := empty.Validate(); err != nil {
		t.Fatalf("BOR empty block must validate: %v", err)
	}
	if empty.Details.Bor.StateSyncCount.Present() || empty.Details.Bor.SprintEnd.Present() {
		t.Fatal("the empty mid-sprint block carries no state sync or sprint-end observation")
	}

	sprint := borSprintFixtureMetadata(t)
	if err := sprint.Validate(); err != nil {
		t.Fatalf("BOR sprint block must validate: %v", err)
	}
	if got := sprint.Details.Bor.StateSyncCount.MustGet(); got != fixtureBorBlock4StateSyncCount {
		t.Fatalf("state sync count mismatch: %d", got)
	}
	if sprint.Details.Bor.StateSyncMode.MustGet() != StateSyncBodyTransaction {
		t.Fatal("post-Madhugiri state sync is a body transaction")
	}
	if !sprint.Details.Bor.SprintEnd.MustGet() || sprint.Details.Bor.SprintLength.MustGet() != fixtureBorSprintLength {
		t.Fatal("block 4 is the end of a four-block sprint")
	}
	if sprint.Header.GasUsed != 0 || sprint.SourceTransactionCount != 1 {
		t.Fatalf("sprint boundary observations mismatch: gas %d count %d",
			sprint.Header.GasUsed, sprint.SourceTransactionCount)
	}

	// A count without a mode is rejected; the schema never leaves the
	// commitment path ambiguous. The details variant is rebuilt so the
	// sprint fixture above stays untouched.
	bad := sprint
	bad.Details.Bor = &BorBlockDetails{
		SprintLength:   sprint.Details.Bor.SprintLength,
		SprintEnd:      sprint.Details.Bor.SprintEnd,
		StateSyncCount: sprint.Details.Bor.StateSyncCount,
	}
	if err := bad.Validate(); err == nil {
		t.Fatal("state sync count above zero requires its mode")
	}

	// A Heimdall checkpoint reference cites the covering range end. The
	// milestone end anchor is the Bor end block hash of the covered range.
	sprint.Details.Bor.HeimdallCheckpoints = []HeimdallCheckpointRef{{
		Kind: CheckpointMilestone,
		End:  AnchorRef{Height: 4, Hash: mustHash32(t, fixtureBorBlock4Hash)},
	}}
	if err := sprint.Validate(); err != nil {
		t.Fatalf("heimdall milestone reference must validate: %v", err)
	}
	sprint.Details.Bor.HeimdallCheckpoints[0].Kind = CheckpointArea
	sprint.Details.Bor.HeimdallCheckpoints[0].CheckpointID = Some(uint64(2))
	if err := sprint.Validate(); err != nil {
		t.Fatalf("heimdall checkpoint reference must validate: %v", err)
	}

	data, err := Marshal(sprint)
	if err != nil {
		t.Fatal(err)
	}
	var back BlockMetadata
	if err := Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sprint, back) {
		t.Fatal("BOR sprint fixture round trip changed the record")
	}
}
