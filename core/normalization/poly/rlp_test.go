package poly

import (
	"bytes"
	"errors"
	"testing"
)

// The expected encodings come from the independent E01 fixture codec
// (test/type_paths/poly/poly_codec.py), executed separately.

const twoItemStateSyncWire = "7ff8aef855019430000000000000000000000000000000000000039d657874726163746f722d636f727075732d73746174652d73796e632d31a00000000000000000000000000000000000000000000000000000000000000001f855029430000000000000000000000000000000000000029d657874726163746f722d636f727075732d73746174652d73796e632d31a00000000000000000000000000000000000000000000000000000000000000002"
const clerkRecordOneWire = "f85c019430000000000000000000000000000000000000039d657874726163746f722d636f727075732d73746174652d73796e632d31a0000000000000000000000000000000000000000000000000000000000000000101853135303031"

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	raw := make([]byte, len(s)/2)
	for i := range raw {
		d1, ok1 := hexNibble(s[2*i])
		d2, ok2 := hexNibble(s[2*i+1])
		if !ok1 || !ok2 {
			t.Fatalf("bad hex literal at %d", i)
		}
		raw[i] = d1<<4 | d2
	}
	return raw
}

func TestRLPDecodeStateSyncWire(t *testing.T) {
	wire := mustHex(t, twoItemStateSyncWire)
	if wire[0] != StateSyncTxType {
		t.Fatalf("missing 0x7f prefix")
	}
	items, err := rlpList(wire[1:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	for i, item := range items {
		if !item.IsList {
			t.Fatalf("item %d is not a list", i)
		}
		fields, err := rlpListContent(item.Bytes)
		if err != nil {
			t.Fatalf("item %d fields: %v", i, err)
		}
		if len(fields) != 4 {
			t.Fatalf("item %d has %d fields, want 4", i, len(fields))
		}
		id, err := rlpDecodeUint(fields[0])
		if err != nil || id != uint64(i+1) {
			t.Fatalf("item %d id: %d err %v", i, id, err)
		}
	}
}

func TestRLPDecodeRejectsTrailingAndNonCanonical(t *testing.T) {
	wire := mustHex(t, clerkRecordOneWire)
	if _, err := rlpDecode(wire); err != nil {
		t.Fatalf("clean decode failed: %v", err)
	}
	if _, err := rlpDecode(append(wire, 0)); !errors.Is(err, ErrTrailing) {
		t.Fatalf("trailing byte: got %v", err)
	}
	// A single byte below 0x80 must be its own encoding, so prefixing a
	// string encoding for it is non-canonical.
	if _, _, err := rlpItem([]byte{0x81, 0x05}); err == nil {
		t.Fatalf("non-minimal single byte accepted")
	}
	// A leading zero in a length field is rejected.
	if _, _, err := rlpItem([]byte{0xb8, 0x00}); err == nil {
		t.Fatalf("zero length-of-length accepted")
	}
	if _, err := rlpDecode([]byte{}); err == nil {
		t.Fatalf("empty input accepted")
	}
}

func TestRLPEncodeMatchesIndependentCodec(t *testing.T) {
	data := []byte("extractor-corpus-state-sync-1")
	target := mustAddress("0x3000000000000000000000000000000000000003")
	l1 := mustHash("0x0000000000000000000000000000000000000000000000000000000000000001")

	item := rlpEncodeList(
		rlpEncodeUint(1), rlpEncodeBytes(target[:]), rlpEncodeBytes(data), rlpEncodeBytes(l1[:]),
	)
	wantItem := "f855019430000000000000000000000000000000000000039d657874726163746f722d636f727075732d73746174652d73796e632d31a00000000000000000000000000000000000000000000000000000000000000001"
	if !bytes.Equal(item, mustHex(t, wantItem)) {
		t.Fatalf("body item encode differs: %x", item)
	}
	if !bytes.Equal(rlpEncodeUint(0), []byte{0x80}) {
		t.Fatalf("zero uint encoding differs")
	}
	if !bytes.Equal(rlpEncodeUint(1), []byte{0x01}) {
		t.Fatalf("one uint encoding differs: %x", rlpEncodeUint(1))
	}
	if !bytes.Equal(rlpEncodeUint(21000), []byte{0x82, 0x52, 0x08}) {
		t.Fatalf("21000 uint encoding differs: %x", rlpEncodeUint(21000))
	}
}
