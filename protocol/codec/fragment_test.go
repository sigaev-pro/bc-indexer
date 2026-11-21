// Tests of the D73 fragment messages: canonical encode/decode round-trips
// of every payload kind, exact-size arithmetic against the appended bytes,
// and the strict rejections of the canonical form.
package codec

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// sampleHeader returns a valid record header fixture.
func sampleHeader() *RecordHeader {
	return &RecordHeader{SchemaVersion: SchemaVersion, Network: testNetwork(),
		EventFamily: uint32(proto.FamilyTx), EventKind: uint32(proto.KindApplied),
		RootType: 100, NodeCount: 15, BlobCount: 3, TotalBlobBytes: 400,
		FragmentCount: 5}
}

// sampleItems returns items covering every value kind.
func sampleItems() []TypedItem {
	return []TypedItem{
		{NodeID: 0, Kind: ValueMessage, Message: MessageValue{TypeKey: 100, DirectChildCount: 8}},
		{NodeID: 1, HasParentID: true, ParentID: 0, HasFieldTag: true, FieldTag: 1,
			Kind: ValueMessage, Message: MessageValue{TypeKey: 201, DirectChildCount: 3}},
		{NodeID: 2, HasParentID: true, ParentID: 1, HasFieldTag: true, FieldTag: 1,
			Kind: ValueBool, BoolValue: true},
		{NodeID: 3, HasParentID: true, ParentID: 1, HasFieldTag: true, FieldTag: 2,
			Kind: ValueUint32, Uint32Value: 0xDEADBEEF},
		{NodeID: 4, HasParentID: true, ParentID: 1, HasFieldTag: true, FieldTag: 3,
			Kind: ValueBytes, BytesValue: BlobRef{BlobID: 4, Length: 300,
				SHA256: blobDigestOf(testPattern(0xAB, 300))}},
		{NodeID: 5, HasParentID: true, ParentID: 0, HasFieldTag: true, FieldTag: 2,
			Kind: ValueList, List: ListValue{Count: 3}},
		{NodeID: 6, HasParentID: true, ParentID: 5, HasElementIndex: true, ElementIndex: 0,
			Kind: ValueUint64, Uint64Value: 1 << 40},
		{NodeID: 7, HasParentID: true, ParentID: 5, HasElementIndex: true, ElementIndex: 1,
			Kind: ValueEnum, EnumValue: 2},
		{NodeID: 8, HasParentID: true, ParentID: 5, HasElementIndex: true, ElementIndex: 2,
			Kind: ValueUTF8, UTF8Value: BlobRef{BlobID: 8, Length: 24,
				SHA256: blobDigestOf([]byte("ledger ✓ αβγ 🜛ᛗ end"))}},
	}
}

// sampleSeal returns a valid seal fixture.
func sampleSeal() *RecordSeal {
	return &RecordSeal{FragmentCount: 5, ContentBytes: 700,
		RecordDigest: blobDigestOf([]byte("content"))}
}

func TestFragmentRoundTripAllPayloads(t *testing.T) {
	fixtures := []*RecordFragment{
		{RecordFormatVersion: proto.RecordFormatVersion, EventID: testEventID(7),
			FragmentIndex: 0, Payload: PayloadHeader, Header: sampleHeader()},
		{RecordFormatVersion: proto.RecordFormatVersion, EventID: testEventID(7),
			FragmentIndex: 1, Payload: PayloadItems, Items: sampleItems()},
		{RecordFormatVersion: proto.RecordFormatVersion, EventID: testEventID(7),
			FragmentIndex: 2, Payload: PayloadBlobs, Blobs: []BlobChunk{
				{BlobID: 4, Offset: 0, Data: testPattern(0xAB, 300)},
				{BlobID: 8, Offset: 0, Data: []byte("ledger ✓ αβγ 🜛ᛗ end")},
			}},
		{RecordFormatVersion: proto.RecordFormatVersion, EventID: testEventID(7),
			FragmentIndex: 4, Payload: PayloadSeal, Seal: sampleSeal()},
	}
	for _, fragment := range fixtures {
		encoded, err := fragment.Encode()
		if err != nil {
			t.Fatalf("encode %s: %v", fragment.Payload, err)
		}
		decoded, err := DecodeRecordFragment(encoded)
		if err != nil {
			t.Fatalf("decode %s: %v", fragment.Payload, err)
		}
		reencoded, err := decoded.Encode()
		if err != nil {
			t.Fatalf("re-encode %s: %v", fragment.Payload, err)
		}
		if !bytes.Equal(encoded, reencoded) {
			t.Fatalf("%s fragment re-encodes differently", fragment.Payload)
		}
		if decoded.Payload != fragment.Payload {
			t.Fatalf("payload %s, want %s", decoded.Payload, fragment.Payload)
		}
		if decoded.EventID != fragment.EventID || decoded.FragmentIndex != fragment.FragmentIndex {
			t.Fatalf("identity decoded wrongly: %+v", decoded)
		}
	}
}

func TestTypedItemSizeMatchesAppend(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 500; i++ {
		item := randomItem(rng)
		appended := appendTypedItem(nil, &item)
		if size := typedItemSize(&item); size != uint64(len(appended)) {
			t.Fatalf("item %d size %d, appended %d bytes", i, size, len(appended))
		}
		wire := itemWireSize(&item)
		if want := uint64(len(appended)) + 1 + uint64(varintLen(uint64(len(appended)))); wire != want {
			t.Fatalf("item %d wire %d, want %d", i, wire, want)
		}
	}
}

// randomItem generates one item with random kind and values, including
// varint-boundary values.
func randomItem(rng *rand.Rand) TypedItem {
	item := TypedItem{NodeID: rng.Uint64()}
	switch rng.Intn(8) {
	case 0:
		item.Kind = ValueMessage
		item.Message = MessageValue{TypeKey: uint32(1 + rng.Int63n(1<<20)),
			DirectChildCount: rng.Uint64()}
	case 1:
		item.Kind = ValueList
		item.List = ListValue{Count: rng.Uint64()}
	case 2:
		item.Kind = ValueBool
		item.BoolValue = rng.Intn(2) == 1
	case 3:
		item.Kind = ValueUint32
		item.Uint32Value = uint32(rng.Uint64())
	case 4:
		item.Kind = ValueUint64
		item.Uint64Value = rng.Uint64()
	case 5:
		item.Kind = ValueEnum
		item.EnumValue = uint32(rng.Uint64())
	case 6, 7:
		ref := BlobRef{BlobID: rng.Uint64(), Length: rng.Uint64()}
		copy(ref.SHA256[:], testPattern(byte(rng.Intn(256)), 32))
		if item.Kind = ValueBytes; rng.Intn(2) == 0 {
			item.Kind = ValueUTF8
			item.UTF8Value = ref
		} else {
			item.BytesValue = ref
		}
	}
	if rng.Intn(2) == 0 {
		item.HasParentID = true
		item.ParentID = rng.Uint64()
	}
	if rng.Intn(2) == 0 {
		item.HasFieldTag = true
		item.FieldTag = uint32(1 + rng.Int63n(1<<28))
	}
	if rng.Intn(2) == 0 {
		item.HasElementIndex = true
		item.ElementIndex = rng.Uint64()
	}
	return item
}

func TestFragmentEncodedSizeMatchesEncode(t *testing.T) {
	items := sampleItems()
	var inner uint64
	for i := range items {
		inner += itemWireSize(&items[i])
	}
	fragment := &RecordFragment{RecordFormatVersion: proto.RecordFormatVersion,
		EventID: testEventID(9), FragmentIndex: 1234, Payload: PayloadItems, Items: items}
	encoded, err := fragment.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if size := fragmentEncodedSize(1234, inner); size != uint64(len(encoded)) {
		t.Fatalf("fragmentEncodedSize %d, encoded %d", size, len(encoded))
	}
}

func TestFragmentSizeGrowsWithVarintIndex(t *testing.T) {
	small := fragmentEncodedSize(1, 100)
	large := fragmentEncodedSize(1<<35, 100)
	if large <= small {
		t.Fatal("fragment size must grow with the fragment index varint")
	}
}

func TestBlobChunkSizeMatchesAppend(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 200; i++ {
		chunk := BlobChunk{BlobID: rng.Uint64(), Offset: rng.Uint64(),
			Data: testPattern(byte(i), 1+rng.Intn(BlobChunkMaxData))}
		appended := appendBlobChunk(nil, &chunk)
		if size := blobChunkSize(&chunk); size != uint64(len(appended)) {
			t.Fatalf("chunk %d size %d, appended %d", i, size, len(appended))
		}
	}
}

// corrupt wraps one helper that appends extra bytes to a valid encoding.
func withSuffix(base, suffix []byte) []byte {
	return append(append([]byte{}, base...), suffix...)
}

func TestFragmentDecodeRejections(t *testing.T) {
	valid := func() []byte {
		fragment := &RecordFragment{RecordFormatVersion: proto.RecordFormatVersion,
			EventID: testEventID(1), FragmentIndex: 0, Payload: PayloadHeader,
			Header: sampleHeader()}
		encoded, err := fragment.Encode()
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return encoded
	}
	cases := []struct {
		name string
		body []byte
		want error
	}{
		{"empty input", nil, ErrInvalidValue},
		{"oversized input", make([]byte, proto.FragmentMaxBytes+1), ErrInvalidValue},
		{"trailing bytes", withSuffix(valid(), []byte{0x08}), ErrMalformed},
		{"unknown field", withSuffix(valid(), appendVarintField(nil, 9, 1)), ErrMalformed},
		{"duplicate scalar field", withSuffix(valid(), appendVarintField(nil, 1, 1)), ErrMalformed},
		{"second payload", withSuffix(valid(), func() []byte {
			seal := &RecordSeal{FragmentCount: 3, ContentBytes: 1,
				RecordDigest: blobDigestOf([]byte("x"))}
			return appendBytesField(nil, fieldSealTag, appendRecordSeal(nil, seal))
		}()), ErrInvalidValue},
		{"event id of 39 bytes", func() []byte {
			body := appendVarintField(nil, 1, uint64(proto.RecordFormatVersion))
			body = appendBytesField(body, 2, eventIDBytes(1)[:39])
			return body
		}(), ErrInvalidValue},
		{"wrong record format version", func() []byte {
			body := appendVarintField(nil, 1, 2)
			body = appendBytesField(body, 2, eventIDBytes(1))
			return appendVarintField(body, 3, 0)
		}(), ErrVersionUnsupported},
		{"missing fragment index", func() []byte {
			body := appendVarintField(nil, 1, uint64(proto.RecordFormatVersion))
			return appendBytesField(body, 2, eventIDBytes(1))
		}(), ErrMissingField},
		{"payload without content", func() []byte {
			body := appendVarintField(nil, 1, uint64(proto.RecordFormatVersion))
			body = appendBytesField(body, 2, eventIDBytes(1))
			return appendVarintField(body, 3, 0)
		}(), ErrMissingField},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeRecordFragment(tc.body)
			assertSentinel(t, tc.want, err)
		})
	}
}

// encodeField helper removed: corruption fixtures build payload fields
// directly with appendBytesField.

func TestTypedItemDecodeRejections(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want error
	}{
		{"missing node id", func() []byte {
			return appendVarintField(nil, 7, 1)
		}(), ErrMissingField},
		{"no value kind", func() []byte {
			item := sampleItems()[2]
			return appendVarintField(nil, 1, item.NodeID)
		}(), ErrInvalidValue},
		{"two value kinds", func() []byte {
			item := sampleItems()[2]
			body := appendVarintField(nil, 1, item.NodeID)
			return appendVarintField(appendVarintField(body, 7, 1), 9, 5)
		}(), ErrInvalidValue},
		{"bool value two", func() []byte {
			item := sampleItems()[2]
			return appendVarintField(appendVarintField(nil, 1, item.NodeID), 7, 2)
		}(), ErrInvalidValue},
		{"zero field tag", func() []byte {
			item := sampleItems()[2]
			body := appendVarintField(nil, 1, item.NodeID)
			return appendVarintField(appendVarintField(body, 3, 0), 7, 1)
		}(), ErrInvalidValue},
		{"zero type key", func() []byte {
			inner := appendVarintField(nil, 1, 0)
			inner = appendVarintField(inner, 2, 1)
			body := appendVarintField(nil, 1, 42)
			return appendBytesField(body, 5, inner)
		}(), ErrInvalidValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeTypedItem(tc.body)
			assertSentinel(t, tc.want, err)
		})
	}
}

func TestBlobRefDecodeRejectsWrongDigestLength(t *testing.T) {
	body := appendVarintField(nil, 1, 5)
	body = appendVarintField(body, 2, 10)
	body = appendBytesField(body, 3, make([]byte, 31))
	_, err := decodeBlobRef(body)
	assertSentinel(t, ErrInvalidValue, err)
}

func TestBlobChunkDecodeRejectsOversizedData(t *testing.T) {
	body := appendVarintField(nil, 1, 5)
	body = appendVarintField(body, 2, 0)
	body = appendBytesField(body, 3, make([]byte, BlobChunkMaxData+1))
	_, err := decodeBlobChunk(body)
	assertSentinel(t, ErrInvalidValue, err)
}

func TestHeaderValidateRejections(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*RecordHeader)
		want   error
	}{
		{"wrong schema version", func(h *RecordHeader) { h.SchemaVersion = 3 }, ErrVersionUnsupported},
		{"unknown family", func(h *RecordHeader) { h.EventFamily = 9 }, ErrClosedRegistry},
		{"unknown kind", func(h *RecordHeader) { h.EventKind = 0 }, ErrClosedRegistry},
		{"zero root type", func(h *RecordHeader) { h.RootType = 0 }, ErrInvalidValue},
		{"zero node count", func(h *RecordHeader) { h.NodeCount = 0 }, ErrInvalidValue},
		{"low fragment count", func(h *RecordHeader) { h.FragmentCount = 2 }, ErrFragments},
		{"unknown network family", func(h *RecordHeader) {
			h.Network.Family = proto.NetworkFamily(7)
		}, ErrClosedRegistry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := sampleHeader()
			tc.mutate(header)
			assertSentinel(t, tc.want, header.Validate())
		})
	}
}

func TestSealValidateRejectsLowFragmentCount(t *testing.T) {
	seal := sampleSeal()
	seal.FragmentCount = 1
	assertSentinel(t, ErrFragments, seal.Validate())
}

func TestItemsFragmentRejectsEmpty(t *testing.T) {
	empty := &RecordFragment{RecordFormatVersion: proto.RecordFormatVersion,
		EventID: testEventID(1), FragmentIndex: 1, Payload: PayloadItems}
	assertSentinel(t, ErrFragments, empty.Validate())
}

func TestSinglePieceFitsRejectsImpossibleItem(t *testing.T) {
	// No single item of the pinned table is this large; the guard exists
	// so future table growth fails closed instead of truncating.
	assertSentinel(t, ErrInvalidValue, singlePieceFits(1, proto.FragmentMaxBytes))
	if err := singlePieceFits(1, 100); err != nil {
		t.Fatalf("small piece rejected: %v", err)
	}
}

func TestChunkFragmentsRoundTripPreservesData(t *testing.T) {
	chunks := []BlobChunk{
		{BlobID: 4, Offset: 0, Data: testPattern(1, BlobChunkMaxData)},
		{BlobID: 4, Offset: BlobChunkMaxData, Data: testPattern(2, 1000)},
	}
	fragment := &RecordFragment{RecordFormatVersion: proto.RecordFormatVersion,
		EventID: testEventID(2), FragmentIndex: 3, Payload: PayloadBlobs, Blobs: chunks}
	encoded, err := fragment.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeRecordFragment(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !chunksEqual(chunks, decoded.Blobs) {
		t.Fatal("chunks did not round-trip")
	}
}

// eventIDBytes returns the event id as a slice for byte-level fixtures.
func eventIDBytes(sequence uint64) []byte {
	id := testEventID(sequence)
	return id[:]
}

func TestEnumStrings(t *testing.T) {
	payloads := []struct {
		value FragmentPayload
		want  string
	}{
		{PayloadNone, "none"}, {PayloadHeader, "header"}, {PayloadItems, "items"},
		{PayloadBlobs, "blobs"}, {PayloadSeal, "seal"}, {FragmentPayload(9), "FragmentPayload(9)"},
	}
	for _, tc := range payloads {
		if got := tc.value.String(); got != tc.want {
			t.Fatalf("payload string %q, want %q", got, tc.want)
		}
	}
	kinds := []struct {
		value ValueKind
		want  string
	}{
		{ValueNone, "none"}, {ValueMessage, "message"}, {ValueList, "list"},
		{ValueBool, "bool"}, {ValueUint32, "uint32"}, {ValueUint64, "uint64"},
		{ValueEnum, "enum"}, {ValueBytes, "bytes"}, {ValueUTF8, "utf8"},
		{ValueKind(42), "ValueKind(42)"},
	}
	for _, tc := range kinds {
		if got := tc.value.String(); got != tc.want {
			t.Fatalf("kind string %q, want %q", got, tc.want)
		}
	}
}

func TestFragmentWireLevelRejections(t *testing.T) {
	// A valid wrapper with a broken TypedItems payload: field 1 as varint.
	fragmentBody := func(inner []byte) []byte {
		body := appendVarintField(nil, 1, uint64(proto.RecordFormatVersion))
		body = appendBytesField(body, 2, eventIDBytes(1))
		body = appendVarintField(body, 3, 1)
		return appendBytesField(body, fieldItemsTag, inner)
	}
	t.Run("typed items wrong wire type", func(t *testing.T) {
		_, err := DecodeRecordFragment(fragmentBody(appendVarintField(nil, 1, 5)))
		assertSentinel(t, ErrMalformed, err)
	})
	t.Run("typed items item decode error", func(t *testing.T) {
		// One item whose node id is missing.
		badItem := appendVarintField(nil, 7, 1)
		inner := appendBytesField(nil, 1, badItem)
		_, err := DecodeRecordFragment(fragmentBody(inner))
		assertSentinel(t, ErrMissingField, err)
	})
	t.Run("blobs chunks wrong wire type", func(t *testing.T) {
		body := appendVarintField(nil, 1, uint64(proto.RecordFormatVersion))
		body = appendBytesField(body, 2, eventIDBytes(1))
		body = appendVarintField(body, 3, 1)
		body = appendBytesField(body, fieldBlobsTag, appendVarintField(nil, 1, 5))
		_, err := DecodeRecordFragment(body)
		assertSentinel(t, ErrMalformed, err)
	})
	t.Run("field number above u32", func(t *testing.T) {
		body := appendVarintField(nil, 1, uint64(proto.RecordFormatVersion))
		body = appendVarint(body, uint64(0x100000000)<<3|wireVarint)
		_, err := DecodeRecordFragment(body)
		assertSentinel(t, ErrMalformed, err)
	})
	t.Run("fixed bytes wrong wire type", func(t *testing.T) {
		// The event id field as a varint.
		body := appendVarintField(nil, 1, uint64(proto.RecordFormatVersion))
		body = appendVarintField(body, 2, 40)
		_, err := DecodeRecordFragment(body)
		assertSentinel(t, ErrMalformed, err)
	})
}

func TestFragmentSubmessageTrailingBytes(t *testing.T) {
	// A MessageValue with a trailing fourth field.
	inner := appendVarintField(nil, 1, 7)
	inner = appendVarintField(inner, 2, 1)
	inner = appendVarintField(inner, 4, 1)
	item := appendVarintField(nil, 1, 9)
	item = appendBytesField(item, 5, inner)
	body := appendVarintField(nil, 1, uint64(proto.RecordFormatVersion))
	body = appendBytesField(body, 2, eventIDBytes(1))
	body = appendVarintField(body, 3, 1)
	body = appendBytesField(body, fieldItemsTag, appendBytesField(nil, 1, item))
	_, err := DecodeRecordFragment(body)
	assertSentinel(t, ErrMalformed, err)
}
