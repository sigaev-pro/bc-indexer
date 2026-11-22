package block

import (
	"errors"
	"math"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

func sourceHeader() SourceHeader {
	var hash block.Hash32
	hash[0] = 0xaa
	var parent block.Hash32
	parent[0] = 0x01
	return SourceHeader{
		Hash:             hash,
		ParentHash:       parent,
		UncleHash:        block.Hash32{0xdd},
		Beneficiary:      block.Address20{0x68},
		StateRoot:        block.Hash32{0x02},
		TransactionsRoot: block.Hash32{0x03},
		ReceiptsRoot:     block.Hash32{0x04},
		LogsBloom:        block.Bloom{0x05},
		Difficulty:       block.UInt256{0x06},
		Number:           7,
		GasLimit:         40000000,
		GasUsed:          100000,
		Time:             1795046400,
		ExtraData:        []byte{0x07},
		MixDigest:        block.Hash32{0x08},
		Nonce:            block.Nonce{0x09},
		SourceRLP:        []byte{0xf9, 0x01},
	}
}

// The pinned BSC rule: MilliTimestamp = Time*1000 + low_u64(MixDigest).
func TestMilliTimestamp(t *testing.T) {
	mix := block.Hash32{}
	mix[31] = 137 // low u64 keeps the millisecond remainder
	ms, err := MilliTimestamp(1795046400, mix)
	if err != nil {
		t.Fatalf("milli timestamp: %v", err)
	}
	if ms != 1795046400*1000+137 {
		t.Fatalf("milli timestamp = %d", ms)
	}

	noRemainder := block.Hash32{}
	ms, err = MilliTimestamp(1795046400, noRemainder)
	if err != nil || ms != 1795046400000 {
		t.Fatalf("whole-second timestamp = %d err %v", ms, err)
	}

	if _, err := MilliTimestamp(math.MaxUint64, noRemainder); !errors.Is(err, ErrMilliOverflow) {
		t.Fatalf("overflow error = %v", err)
	}
	maxLow := block.Hash32{}
	for i := 24; i < 32; i++ {
		maxLow[i] = 0xff
	}
	if _, err := MilliTimestamp(math.MaxUint64/1000+1, maxLow); !errors.Is(err, ErrMilliOverflow) {
		t.Fatalf("low-bits overflow error = %v", err)
	}
}

func TestTimestampPrecisionIsMilliseconds(t *testing.T) {
	mix := block.Hash32{}
	mix[31] = 250
	stamp, err := Timestamp(1795046400, mix)
	if err != nil {
		t.Fatalf("timestamp: %v", err)
	}
	if stamp.Precision != block.PrecisionMilliseconds {
		t.Fatalf("precision = %s", stamp.Precision)
	}
	if stamp.Seconds != 1795046400 || stamp.Nanos != 250*1_000_000 {
		t.Fatalf("timestamp = %+v", stamp)
	}
	if err := stamp.Validate(); err != nil {
		t.Fatalf("timestamp validate: %v", err)
	}
}

func TestProjectCommonHeader(t *testing.T) {
	metadata, err := Project(sourceHeader(), 3, ParliaFacts{})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if metadata.SourceTransactionCount != 3 || metadata.Number != 7 {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
	if metadata.Timestamp.Precision != block.PrecisionMilliseconds {
		t.Fatal("BSC timestamp precision must be MILLISECONDS")
	}
	if string(metadata.Header.SourceRLP) != string(sourceHeader().SourceRLP) {
		t.Fatal("source RLP not preserved")
	}
	if metadata.Details.Bsc == nil {
		t.Fatal("BSC family details missing")
	}
	if _, ok := metadata.Details.Bsc.ParliaSystemTransactionCount.Get(); ok {
		t.Fatal("absent parlia count must stay absent")
	}
}

func TestOptionalForkPresence(t *testing.T) {
	src := sourceHeader()
	src.BaseFee = &block.UInt256{} // present zero: pre-London base fee 0
	metadata, err := Project(src, 0, ParliaFacts{})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	baseFee, ok := metadata.Header.BaseFee.Get()
	if !ok || !baseFee.IsZero() {
		t.Fatal("present zero base fee must stay present")
	}
	if _, ok := metadata.Header.WithdrawalsRoot.Get(); ok {
		t.Fatal("absent withdrawals root became present")
	}
	if _, ok := metadata.Header.BlobGasUsed.Get(); ok {
		t.Fatal("absent blob gas used became present")
	}
}

func TestParliaSystemCountObservation(t *testing.T) {
	count := uint64(8)
	metadata, err := Project(sourceHeader(), 12, ParliaFacts{SystemTransactionCount: &count})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	observed, ok := metadata.Details.Bsc.ParliaSystemTransactionCount.Get()
	if !ok || observed != 8 {
		t.Fatalf("parlia count observation = %d present %v", observed, ok)
	}
}

func TestEmptyBlockIsFirstClass(t *testing.T) {
	metadata, err := Project(sourceHeader(), 0, ParliaFacts{SystemTransactionCount: new(uint64)})
	if err != nil {
		t.Fatalf("empty block rejected: %v", err)
	}
	if metadata.SourceTransactionCount != 0 {
		t.Fatal("empty block count must be the observed zero")
	}
}

func TestSourceRLPMandatory(t *testing.T) {
	src := sourceHeader()
	src.SourceRLP = nil
	if _, err := Project(src, 1, ParliaFacts{}); !errors.Is(err, ErrEmptySourceRLP) {
		t.Fatalf("missing RLP error = %v", err)
	}
}

func TestZeroHashRejected(t *testing.T) {
	src := sourceHeader()
	src.Hash = block.Hash32{}
	if _, err := Project(src, 1, ParliaFacts{}); err == nil {
		t.Fatal("zero block hash must be rejected by the schema")
	}
}
