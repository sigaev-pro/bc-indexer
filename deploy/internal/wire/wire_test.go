// Format tests of the mock wire: the exact Nats-Msg-Id of
// CONTROL_WIRE_CONTRACT (89-byte tuple, base64url without padding, the
// fixed "ex2." prefix, 123 ASCII characters), the fixed NATS header
// layout, the length-prefixed framing and the identity decoders.
package wire

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

// goldenTuple builds a fully populated known tuple: the network digest
// 00..1f, the producer id 10..1f, the run id 20..2f and the sequence
// 0x1122334455667788. The zero fields stay zero: one complete fragment
// (index 0), form COMPLETE (0), part index 0.
func goldenTuple() (networkDigest [32]byte, producerID, runID [16]byte, sequence uint64) {
	for i := range networkDigest {
		networkDigest[i] = byte(i)
	}
	for i := range producerID {
		producerID[i] = byte(0x10 + i)
		runID[i] = byte(0x20 + i)
	}
	return networkDigest, producerID, runID, 0x1122334455667788
}

// TestNATSMsgIDFormat pins the exact id of a known tuple against an
// independently written golden value.
func TestNATSMsgIDFormat(t *testing.T) {
	networkDigest, producerID, runID, sequence := goldenTuple()
	const want = "ex2.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8QERITFBUWFxgZGhscHR4fICEiIyQlJicoKSorLC0uLxEiM0RVZneIAAAAAAAAAAAAAAAAAAAAAAA"
	got := NATSMsgID(networkDigest, producerID, runID, sequence)
	if got != want {
		t.Fatalf("msg id\n got %s\nwant %s", got, want)
	}
	if len(got) != 123 {
		t.Fatalf("msg id length %d, want 123 (4 prefix + 119 base64url of 89 bytes)", len(got))
	}
	if !strings.HasPrefix(got, "ex2.") {
		t.Fatalf("msg id prefix: %q", got[:4])
	}
	// base64url without padding only.
	for _, c := range got[4:] {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", c) {
			t.Fatalf("character %q is not base64url", c)
		}
	}
}

// TestNATSMsgIDLayout decodes the id back into the tuple and checks
// every field position: digest 0:32, producer 32:48, run 48:64,
// sequence 64:72, fragment 72:80, form 80, part 81:89.
func TestNATSMsgIDLayout(t *testing.T) {
	networkDigest, producerID, runID, sequence := goldenTuple()
	raw, err := base64.RawURLEncoding.DecodeString(NATSMsgID(networkDigest, producerID, runID, sequence)[4:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != 89 {
		t.Fatalf("tuple length %d, want 89", len(raw))
	}
	if !bytes.Equal(raw[0:32], networkDigest[:]) {
		t.Fatalf("network digest position: %x", raw[0:32])
	}
	if !bytes.Equal(raw[32:48], producerID[:]) {
		t.Fatalf("producer id position: %x", raw[32:48])
	}
	if !bytes.Equal(raw[48:64], runID[:]) {
		t.Fatalf("run id position: %x", raw[48:64])
	}
	if got := binary.BigEndian.Uint64(raw[64:72]); got != sequence {
		t.Fatalf("sequence position: %x", raw[64:72])
	}
	if !bytes.Equal(raw[72:80], make([]byte, 8)) {
		t.Fatalf("fragment index position: %x", raw[72:80])
	}
	if raw[80] != 0 {
		t.Fatalf("form byte: %d", raw[80])
	}
	if !bytes.Equal(raw[81:89], make([]byte, 8)) {
		t.Fatalf("part index position: %x", raw[81:89])
	}
}

// TestNATSMsgIDDeterminismAndDifference: the id is stable per tuple and
// differs on every identity component.
func TestNATSMsgIDDeterminismAndDifference(t *testing.T) {
	networkDigest, producerID, runID, sequence := goldenTuple()
	base := NATSMsgID(networkDigest, producerID, runID, sequence)
	if again := NATSMsgID(networkDigest, producerID, runID, sequence); again != base {
		t.Fatalf("msg id is not deterministic")
	}
	otherDigest := networkDigest
	otherDigest[0] ^= 0xff
	otherProducer := producerID
	otherProducer[0] ^= 0xff
	otherRun := runID
	otherRun[0] ^= 0xff
	variants := map[string]string{
		"network digest": NATSMsgID(otherDigest, producerID, runID, sequence),
		"producer id":    NATSMsgID(networkDigest, otherProducer, runID, sequence),
		"run id":         NATSMsgID(networkDigest, producerID, otherRun, sequence),
		"sequence":       NATSMsgID(networkDigest, producerID, runID, sequence+1),
		"sequence zero":  NATSMsgID(networkDigest, producerID, runID, 0),
	}
	for name, id := range variants {
		if id == base {
			t.Fatalf("changing the %s did not change the msg id", name)
		}
	}
	if !VerifyNATSMsgID(base, networkDigest, producerID, runID, sequence) {
		t.Fatalf("verify rejected the matching tuple")
	}
	if VerifyNATSMsgID(base, networkDigest, producerID, runID, sequence+1) {
		t.Fatalf("verify accepted a foreign sequence")
	}
	if VerifyNATSMsgID("ex2."+strings.Repeat("A", 119), otherDigest, producerID, runID, sequence) {
		t.Fatalf("verify accepted a foreign network digest")
	}
}

// TestHeaderBytes pins the exact NATS header layout of a published
// frame: NATS/1.0, Nats-Msg-Id, Nats-Expected-Stream, CRLF-terminated.
func TestHeaderBytes(t *testing.T) {
	want := "NATS/1.0\r\nNats-Msg-Id: ex2.ABC\r\nNats-Expected-Stream: EXTRACTOR_ETH_V3\r\n\r\n"
	if got := string(HeaderBytes("ex2.ABC", "EXTRACTOR_ETH_V3")); got != want {
		t.Fatalf("header bytes:\n got %q\nwant %q", got, want)
	}
}

// TestFramingRoundTrip frames and reads every message kind.
func TestFramingRoundTrip(t *testing.T) {
	hello := Hello{Type: "hello", Protocol: Protocol, Role: "adapter",
		NetworkProfile: "ethereum-mainnet", ProducerID: "01", RunID: "02", BindingDigest: "aa"}
	helloAck := HelloAck{Type: "hello_ack", NetworkProfile: "ethereum-mainnet", BindingDigest: "aa", GatewayEpoch: "bb"}
	event := Event{Type: "event", EventSeq: 7, Height: 9, Route: "tx.applied", Payload: []byte(`{"x":1}`)}
	ack := Ack{Type: "ack", EventSeq: 7, Stream: "EXTRACTOR_ETH_V3", Sequence: 42, Duplicate: true, MsgID: "ex2.x"}
	nak := Nak{Type: "nak", EventSeq: 7, Code: "TRANSIENT", Detail: "later"}
	refuse := ErrorRefuse{Type: "error", Code: "BINDING", Detail: "no"}

	var buf bytes.Buffer
	for _, msg := range []any{hello, helloAck, event, ack, nak, refuse} {
		if err := Write(&buf, msg); err != nil {
			t.Fatalf("write %T: %v", msg, err)
		}
	}
	var gotHello Hello
	var gotHelloAck HelloAck
	var gotEvent Event
	var gotAck Ack
	var gotNak Nak
	var gotRefuse ErrorRefuse
	for _, out := range []any{&gotHello, &gotHelloAck, &gotEvent, &gotAck, &gotNak, &gotRefuse} {
		if err := Read(&buf, out); err != nil {
			t.Fatalf("read %T: %v", out, err)
		}
	}
	if buf.Len() != 0 {
		t.Fatalf("%d bytes left in the stream", buf.Len())
	}
	if gotHello != hello || gotHelloAck != helloAck || gotAck != ack || gotNak != nak || gotRefuse != refuse {
		t.Fatalf("round trip changed a message")
	}
	if gotEvent.EventSeq != event.EventSeq || gotEvent.Route != event.Route ||
		!bytes.Equal(gotEvent.Payload, event.Payload) || gotEvent.Height != event.Height {
		t.Fatalf("event round trip: %+v", gotEvent)
	}
}

// TestFramingBounds: an oversized message is refused by Write, and a
// bogus frame length by Read.
func TestFramingBounds(t *testing.T) {
	big := Event{Type: "event", Payload: make([]byte, MaxMessageBytes)}
	if err := Write(io.Discard, big); err == nil {
		t.Fatalf("oversized message accepted")
	}
	var zero [4]byte
	if err := Read(bytes.NewReader(zero[:]), &Event{}); err == nil {
		t.Fatalf("zero-length frame accepted")
	}
	var huge [4]byte
	binary.BigEndian.PutUint32(huge[:], MaxMessageBytes+1)
	if err := Read(bytes.NewReader(huge[:]), &Event{}); err == nil {
		t.Fatalf("oversized frame accepted")
	}
}

// TestIdentityDecoders covers the hex helpers of the wire identities.
func TestIdentityDecoders(t *testing.T) {
	digest, err := Digest32Hex(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if digest[0] != 0xab || digest[31] != 0xab {
		t.Fatalf("digest bytes: %x", digest)
	}
	if _, err := Digest32Hex(strings.Repeat("ab", 31)); err == nil {
		t.Fatalf("short digest accepted")
	}
	if _, err := Digest32Hex(strings.Repeat("AB", 32)); err == nil {
		t.Fatalf("uppercase digest accepted")
	}
	if _, err := Digest32Hex(strings.Repeat("zz", 32)); err == nil {
		t.Fatalf("non-hex digest accepted")
	}
	id, err := ID16Hex(strings.Repeat("cd", 16))
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	if HexID16(id) != strings.Repeat("cd", 16) {
		t.Fatalf("id round trip: %s", HexID16(id))
	}
	if _, err := ID16Hex(strings.Repeat("cd", 15)); err == nil {
		t.Fatalf("short id accepted")
	}
}
