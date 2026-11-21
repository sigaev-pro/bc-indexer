package proto

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"math"
	"math/rand/v2"
	"reflect"
	"testing"
)

// Golden encodings from the independent Python implementation of the pinned
// v2 table (hashlib only); the Go encoder must reproduce them byte for byte.
const (
	goldenCompleteFrameHex = "0802124608011220151b1389a1dbc6cfa461c6d445dc7e5771afcb3ab44e7ac1d3e0e56b4eaf52d31a20a4fa7f95d372993a6811a2432dec9355bec4dd45e225fb0b2944ff241d3bfa8f1a2800112233445566778899aabbccddeeff0f0e0d0c0b0a090807060504030201000000000000000007222053149780f62771b258efafba7540d3495403add1367543acebfadf03ab51feb72a10deadbeefcafebabe0123456789abcdef30b2f2193a660a6430313233343536373839616263646566303132333435363738396162636465663031323334353637383961626364656630313233343536373839616263646566303132333435363738396162636465663031323334353637383961626364656630313233480150035a205ad174134c5d0173e87eb427c7608d0ccec7fea0341544d70b3bd6c3443e94c36001"
	goldenPartFrameHex     = "0802124608011220151b1389a1dbc6cfa461c6d445dc7e5771afcb3ab44e7ac1d3e0e56b4eaf52d31a20a4fa7f95d372993a6811a2432dec9355bec4dd45e225fb0b2944ff241d3bfa8f1a2800112233445566778899aabbccddeeff0f0e0d0c0b0a090807060504030201000000000000000007222053149780f62771b258efafba7540d3495403add1367543acebfadf03ab51feb72a10deadbeefcafebabe0123456789abcdef30b2f219426d08001025188020226466656463626139383736353433323130666564636261393837363534333231306665646362613938373635343332313066656463626139383736353433323130666564636261393837363534333231306665646362613938373635343332313066656463480150035a205ad174134c5d0173e87eb427c7608d0ccec7fea0341544d70b3bd6c3443e94c36001"
	goldenMaxVarFrameHex   = "0802124608011220151b1389a1dbc6cfa461c6d445dc7e5771afcb3ab44e7ac1d3e0e56b4eaf52d31a20a4fa7f95d372993a6811a2432dec9355bec4dd45e225fb0b2944ff241d3bfa8f1a2800112233445566778899aabbccddeeff0f0e0d0c0b0a090807060504030201000000000000000007222053149780f62771b258efafba7540d3495403add1367543acebfadf03ab51feb72a10deadbeefcafebabe0123456789abcdef30ffffffffffffffffff01421d08feffffffffffffffff0110ffffffffffffffffff011880800422015a48ffffffffffffffffff0150ffffffffffffffffff015a205ad174134c5d0173e87eb427c7608d0ccec7fea0341544d70b3bd6c3443e94c36001"
)

func TestGoldenCompleteFrame(t *testing.T) {
	frame := goldenCompleteFrame(t)
	encoded, err := frame.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	want := mustHex(t, goldenCompleteFrameHex)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("complete frame bytes differ from the golden vector:\n got %x\nwant %x", encoded, want)
	}
	size, err := frame.EncodedFrameSize()
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	if size != uint64(len(want)) || size != 316 {
		t.Fatalf("EncodedFrameSize %d, want 316 (exact golden length)", size)
	}
	decoded, err := DecodeTransportFrame(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(frame, decoded) {
		t.Fatalf("round trip changed the frame:\n got %+v\nwant %+v", decoded, frame)
	}
}

func TestGoldenPartFrame(t *testing.T) {
	frame := goldenPartFrame(t)
	encoded, err := frame.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	want := mustHex(t, goldenPartFrameHex)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("part frame bytes differ from the golden vector:\n got %x\nwant %x", encoded, want)
	}
	size, err := frame.EncodedFrameSize()
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	if size != uint64(len(want)) || size != 323 {
		t.Fatalf("EncodedFrameSize %d, want 323 (exact golden length)", size)
	}
	decoded, err := DecodeTransportFrame(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(frame, decoded) {
		t.Fatalf("round trip changed the frame")
	}
}

// TestGoldenMaxVarintFrame covers the widest varints of every varying
// scalar: the conservative sizing case of the layout planner.
func TestGoldenMaxVarintFrame(t *testing.T) {
	frame := &TransportFrame{
		TransportVersion: TransportVersion,
		Network:          goldenNetwork(t),
		EventID:          goldenEventID(t),
		RecordDigest:     digest32(t, "proto-golden-record"),
		PublicationEpoch: goldenEpoch(t),
		ChainSequence:    math.MaxUint64,
		Part: &FragmentPart{PartIndex: math.MaxUint64 - 1, PartCount: math.MaxUint64,
			FragmentTotalBytes: 65536, Data: []byte{0x5a}},
		FragmentIndex:       math.MaxUint64,
		FragmentCount:       math.MaxUint64,
		FragmentDigest:      digest32(t, "proto-golden-fragment"),
		RecordFormatVersion: RecordFormatVersion,
	}
	encoded, err := frame.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	want := mustHex(t, goldenMaxVarFrameHex)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("max-varint frame bytes differ from the golden vector:\n got %x\nwant %x", encoded, want)
	}
	size, err := frame.EncodedFrameSize()
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	if size != uint64(len(want)) || size != 268 {
		t.Fatalf("EncodedFrameSize %d, want 268 (exact golden length)", size)
	}
	decoded, err := DecodeTransportFrame(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(frame, decoded) {
		t.Fatalf("round trip changed the frame")
	}
}

// TestFrameEncodeIsReproducible is the property-style reproducibility check:
// random valid frames encode byte-identically on every attempt, decode and
// re-encode to the same bytes, and report the exact encoded size.
func TestFrameEncodeIsReproducible(t *testing.T) {
	rnd := rand.New(rand.NewPCG(0xE02, 0x26))
	for i := 0; i < 500; i++ {
		frame := randomFrame(rnd, i)
		first, err := frame.Encode()
		if err != nil {
			t.Fatalf("iteration %d encode: %v", i, err)
		}
		second, err := frame.Encode()
		if err != nil {
			t.Fatalf("iteration %d re-encode: %v", i, err)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("iteration %d: two encodings of one frame differ", i)
		}
		decoded, err := DecodeTransportFrame(first)
		if err != nil {
			t.Fatalf("iteration %d decode: %v", i, err)
		}
		reencoded, err := decoded.Encode()
		if err != nil {
			t.Fatalf("iteration %d re-encode of decoded: %v", i, err)
		}
		if !bytes.Equal(first, reencoded) {
			t.Fatalf("iteration %d: decode/re-encode is not byte-identical", i)
		}
		size, err := frame.EncodedFrameSize()
		if err != nil {
			t.Fatalf("iteration %d size: %v", i, err)
		}
		if size != uint64(len(first)) {
			t.Fatalf("iteration %d: EncodedFrameSize %d, encoded length %d", i, size, len(first))
		}
	}
}

func randomFrame(rnd *rand.Rand, seed int) *TransportFrame {
	identity := sha256.Sum256([]byte{byte(seed), byte(seed >> 8), byte(seed >> 16)})
	var producer, run, epoch ID16
	copy(producer[:], identity[:16])
	copy(run[:], identity[16:])
	copy(epoch[:], identity[:16])
	var chain, genesis [32]byte
	chainSum := sha256.Sum256([]byte("chain"))
	genesisSum := sha256.Sum256([]byte("genesis"))
	copy(chain[:], chainSum[:])
	copy(genesis[:], genesisSum[:])
	frame := &TransportFrame{
		TransportVersion:    TransportVersion,
		Network:             NetworkIdentity{Family: NetworkFamilyEVM, ChainID: chain, GenesisHash: genesis},
		EventID:             NewEventID(producer, run, rnd.Uint64()),
		RecordDigest:        Hash32(identity),
		PublicationEpoch:    epoch,
		ChainSequence:       rnd.Uint64()%math.MaxUint64 + 1,
		FragmentIndex:       rnd.Uint64() % 4096,
		FragmentCount:       rnd.Uint64()%64 + 1,
		FragmentDigest:      Hash32(identity),
		RecordFormatVersion: RecordFormatVersion,
	}
	if rnd.IntN(2) == 0 {
		frame.Complete = &CompleteFragment{FragmentBytes: counterBytes(byte(seed), 1+rnd.IntN(60000))}
	} else {
		total := uint64(1) + rnd.Uint64()%FragmentMaxBytes
		partCount := uint64(1) + rnd.Uint64()%40
		dataMax := total
		if dataMax > 700 {
			dataMax = 700
		}
		frame.Part = &FragmentPart{
			PartIndex:          rnd.Uint64() % partCount,
			PartCount:          partCount,
			FragmentTotalBytes: total,
			Data:               counterBytes(byte(seed)+1, 1+int(rnd.Uint64()%dataMax)),
		}
	}
	return frame
}

// TestFrameDecodeRejects mutates the golden COMPLETE encoding; every mutant
// must be rejected by a named error class, never silently accepted.
func TestFrameDecodeRejects(t *testing.T) {
	encoded, err := goldenCompleteFrame(t).Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	neither, err := goldenCompleteFrame(t).Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	neither = cutSpan(t, neither, "3a660a64", 100) // drop the whole field 7
	badIDLen := splice(t, encoded, "1a28", "1a27")
	badIDLen = splice(t, badIDLen, "00000000000000072220", "000000000000002220")
	cases := []struct {
		name   string
		mutate []byte
		want   error
	}{
		{"unknown field 13", append(append([]byte{}, encoded...), 0x68, 0x01), ErrUnknownField},
		{"duplicate field 9", insert(t, encoded, "4801", "4802"), ErrDuplicateField},
		{"field 6 after field 9", insert(t, encoded, "4801", "30b2f219"), ErrFieldOrder},
		{"trailing field 1 after field 12", append(append([]byte{}, encoded...), 0x08, 0x02), ErrFieldOrder},
		{"non-minimal chain sequence varint", splice(t, encoded, "30b2f219", "308000"), ErrMalformed},
		{"truncated tail", encoded[:len(encoded)-1], ErrMalformed},
		{"missing publication epoch", cut(t, encoded, "2a10deadbeefcafebabe0123456789abcdef"), ErrMissingField},
		{"missing oneof payload", neither, ErrMissingField},
		{"field 9 with bytes wire type", splice(t, encoded, "4801", "4a0101"), ErrMalformed},
		{"unknown transport version", splice(t, encoded, "0802", "0803"), ErrVersionUnsupported},
		{"unknown record format version", splice(t, encoded, "6001", "6002"), ErrVersionUnsupported},
		{"unknown network family", splice(t, encoded, "0801", "0805"), ErrClosedRegistry},
		{"both oneof alternatives", insertBefore(t, encoded, "4801", "4209080010011801220161"), ErrInvalidValue},
		{"event id 39 bytes", badIDLen, ErrInvalidValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeTransportFrame(tc.mutate)
			if err == nil {
				t.Fatalf("mutant accepted")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("error %v does not carry %v", err, tc.want)
			}
		})
	}
}

// cutSpan removes the marker and n bytes following it.
func cutSpan(t *testing.T, b []byte, markerHex string, n int) []byte {
	t.Helper()
	marker := mustHex(t, markerHex)
	at := bytes.Index(b, marker)
	if at < 0 {
		t.Fatalf("marker %s not found", markerHex)
	}
	end := at + len(marker) + n
	if end > len(b) {
		t.Fatalf("span beyond end")
	}
	out := make([]byte, 0, len(b)-(end-at))
	out = append(out, b[:at]...)
	return append(out, b[end:]...)
}

// TestFrameValidateRejects checks the value-level rules with named errors.
func TestFrameValidateRejects(t *testing.T) {
	base := goldenCompleteFrame(t)
	small := func() *TransportFrame { c := *base; return &c }
	cases := []struct {
		name string
		mut  func(*TransportFrame)
		want error
	}{
		{"transport version 3", func(f *TransportFrame) { f.TransportVersion = 3 }, ErrVersionUnsupported},
		{"record format version 0", func(f *TransportFrame) { f.RecordFormatVersion = 0 }, ErrVersionUnsupported},
		{"network family 0", func(f *TransportFrame) { f.Network.Family = 0 }, ErrClosedRegistry},
		{"zero event id", func(f *TransportFrame) { f.EventID = EventID{} }, ErrInvalidValue},
		{"zero publication epoch", func(f *TransportFrame) { f.PublicationEpoch = ID16{} }, ErrInvalidValue},
		{"chain sequence 0", func(f *TransportFrame) { f.ChainSequence = 0 }, ErrInvalidValue},
		{"no oneof alternative", func(f *TransportFrame) { f.Complete = nil }, ErrInvalidValue},
		{"both oneof alternatives", func(f *TransportFrame) {
			f.Part = &FragmentPart{PartIndex: 0, PartCount: 1, FragmentTotalBytes: 4, Data: []byte{1}}
		}, ErrInvalidValue},
		{"empty complete fragment", func(f *TransportFrame) { f.Complete.FragmentBytes = nil }, ErrInvalidValue},
		{"complete fragment over bound", func(f *TransportFrame) {
			f.Complete.FragmentBytes = make([]byte, FragmentMaxBytes+1)
		}, ErrInvalidValue},
		{"fragment count 0", func(f *TransportFrame) { f.FragmentCount = 0 }, ErrInvalidValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := small()
			tc.mut(frame)
			err := frame.Validate()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Validate error %v does not carry %v", err, tc.want)
			}
			if _, err := frame.Encode(); !errors.Is(err, tc.want) {
				t.Fatalf("Encode error %v does not carry %v", err, tc.want)
			}
		})
	}
}

// TestFragmentPartValidateRejects covers the part assembly bounds.
func TestFragmentPartValidateRejects(t *testing.T) {
	valid := FragmentPart{PartIndex: 0, PartCount: 2, FragmentTotalBytes: 10, Data: []byte{1, 2}}
	cases := []struct {
		name string
		mut  func(*FragmentPart)
	}{
		{"index equals count", func(p *FragmentPart) { p.PartIndex = 2 }},
		{"index above count", func(p *FragmentPart) { p.PartIndex = 9; p.PartCount = 2 }},
		{"zero total", func(p *FragmentPart) { p.FragmentTotalBytes = 0 }},
		{"total over bound", func(p *FragmentPart) { p.FragmentTotalBytes = FragmentMaxBytes + 1 }},
		{"zero data", func(p *FragmentPart) { p.Data = nil }},
		{"data over bound", func(p *FragmentPart) { p.Data = make([]byte, FragmentMaxBytes+1) }},
		{"data longer than total", func(p *FragmentPart) { p.Data = make([]byte, 11) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			part := valid
			tc.mut(&part)
			if err := part.Validate(); err == nil {
				t.Fatalf("invalid part accepted")
			}
		})
	}
}

// TestEncodedFrameSizeExactSmallScalars pins the exact size arithmetic for
// the smallest varints, against both the golden encoder and a hand sum.
func TestEncodedFrameSizeExactSmallScalars(t *testing.T) {
	frame := &TransportFrame{
		TransportVersion:    TransportVersion,
		Network:             goldenNetwork(t),
		EventID:             goldenEventID(t),
		RecordDigest:        Hash32{},
		PublicationEpoch:    goldenEpoch(t),
		ChainSequence:       1, // 1-byte varint
		Complete:            &CompleteFragment{FragmentBytes: []byte{7}},
		FragmentIndex:       0, // explicit zero, 1-byte varint
		FragmentCount:       1,
		FragmentDigest:      Hash32{},
		RecordFormatVersion: RecordFormatVersion,
	}
	size, err := frame.EncodedFrameSize()
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	encoded, err := frame.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if size != uint64(len(encoded)) {
		t.Fatalf("EncodedFrameSize %d, encoded length %d", size, len(encoded))
	}
	// Hand sum of the pinned table: f1 2, f2 72, f3 42, f4 34, f5 18,
	// f6 2, f7 1+1+(1+1+1)=5, f9 2, f10 2, f11 34, f12 2.
	const handSum = 2 + 72 + 42 + 34 + 18 + 2 + 5 + 2 + 2 + 34 + 2
	if size != handSum {
		t.Fatalf("EncodedFrameSize %d, hand-computed sum %d", size, handSum)
	}
}
