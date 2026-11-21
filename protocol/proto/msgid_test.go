package proto

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"testing"
)

// Golden ids from the independent Python implementation (hashlib, base64).
const (
	goldenNetworkDigestHex = "c2983f67ce2c3dd7313b0fcf3553184dc20bb62ac5fdce02604c0cadf84ee493"
	goldenMsgIDComplete    = "ex2.wpg_Z84sPdcxOw_PNVMYTcILtirF_c4CYEwMrfhO5JMAESIzRFVmd4iZqrvM3e7_Dw4NDAsKCQgHBgUEAwIBAAAAAAAAAAAHAAAAAAAAAAEAAAAAAAAAAAA"
	goldenMsgIDPart        = "ex2.wpg_Z84sPdcxOw_PNVMYTcILtirF_c4CYEwMrfhO5JMAESIzRFVmd4iZqrvM3e7_Dw4NDAsKCQgHBgUEAwIBAAAAAAAAAAAHAAAAAAAAAAIBAAAAAAAAAAU"
)

func TestNetworkDigestGolden(t *testing.T) {
	digest, err := goldenNetwork(t).Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	var want Hash32
	copy(want[:], mustHex(t, goldenNetworkDigestHex))
	if digest != want {
		t.Fatalf("network digest %s, want %s", digest, want)
	}
	if err := (NetworkIdentity{Family: 2}.Validate()); !errors.Is(err, ErrClosedRegistry) {
		t.Fatalf("unknown network family error %v", err)
	}
	if _, err := (NetworkIdentity{Family: 2}.Digest()); !errors.Is(err, ErrClosedRegistry) {
		t.Fatalf("digest of unknown family error %v", err)
	}
}

func TestNatsMsgIDGoldenComplete(t *testing.T) {
	var digest Hash32
	copy(digest[:], mustHex(t, goldenNetworkDigestHex))
	id, err := NatsMsgID(digest, goldenEventID(t), 1, FormComplete, 0)
	if err != nil {
		t.Fatalf("msgid: %v", err)
	}
	if id != goldenMsgIDComplete {
		t.Fatalf("COMPLETE message id\n got %s\nwant %s", id, goldenMsgIDComplete)
	}
	if len(id) != 123 {
		t.Fatalf("id length %d, want 123 ASCII bytes", len(id))
	}
}

func TestNatsMsgIDGoldenPart(t *testing.T) {
	var digest Hash32
	copy(digest[:], mustHex(t, goldenNetworkDigestHex))
	id, err := NatsMsgID(digest, goldenEventID(t), 2, FormPart, 5)
	if err != nil {
		t.Fatalf("msgid: %v", err)
	}
	if id != goldenMsgIDPart {
		t.Fatalf("PART message id\n got %s\nwant %s", id, goldenMsgIDPart)
	}
}

// TestNatsMsgIDLengthInvariant: every valid input yields the fixed 89-byte
// tuple and the 123-ASCII-byte id, so header sizes never depend on values.
func TestNatsMsgIDLengthInvariant(t *testing.T) {
	rnd := rand.New(rand.NewPCG(7, 0x1D))
	for i := 0; i < 200; i++ {
		var digest Hash32
		copy(digest[:], counterBytes(byte(i), 32))
		id, err := NatsMsgID(digest, randomFrame(rnd, i).EventID, rnd.Uint64(), FormPart, rnd.Uint64())
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if len(id) != 123 {
			t.Fatalf("iteration %d: id length %d, want 123", i, len(id))
		}
	}
}

func TestNatsMsgIDRejects(t *testing.T) {
	var digest Hash32
	if _, err := NatsMsgID(digest, EventID{}, 0, FormComplete, 0); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("zero event id error %v", err)
	}
	if _, err := NatsMsgID(digest, goldenEventID(t), 0, Form(2), 0); !errors.Is(err, ErrClosedRegistry) {
		t.Fatalf("unknown form error %v", err)
	}
}

// TestNatsHeaderExactBytes pins the immutable header contract: exact bytes,
// exact order, no status line, no extra headers.
func TestNatsHeaderExactBytes(t *testing.T) {
	header, err := NatsHeader(goldenMsgIDComplete, "EXTRACTOR_ETH")
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	want := []byte("NATS/1.0\r\nNats-Msg-Id: " + goldenMsgIDComplete +
		"\r\nNats-Expected-Stream: EXTRACTOR_ETH\r\n\r\n")
	if !bytes.Equal(header, want) {
		t.Fatalf("header bytes\n got %q\nwant %q", header, want)
	}
	size, err := NatsHeaderSize("EXTRACTOR_ETH")
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	if size != uint64(len(want)) || size != 187 {
		t.Fatalf("header size %d, want 187", size)
	}
	// The size depends only on the stream name: every valid id has 123 bytes.
	for _, stream := range []string{"A", "EXTRACTOR_BSC", "extraction.stream.v3.long-name"} {
		size, err := NatsHeaderSize(stream)
		if err != nil {
			t.Fatalf("size %q: %v", stream, err)
		}
		if size != 174+uint64(len(stream)) {
			t.Fatalf("stream %q: header size %d, want %d", stream, size, 174+len(stream))
		}
	}
}

func TestNatsHeaderRejects(t *testing.T) {
	if _, err := NatsHeader("short", "S"); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("short id error %v", err)
	}
	if _, err := NatsHeader("ex1."+goldenMsgIDComplete[4:], "S"); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("wrong prefix error %v", err)
	}
	if _, err := NatsHeader(goldenMsgIDComplete, ""); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("empty stream error %v", err)
	}
	if _, err := NatsHeaderSize(""); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("empty stream size error %v", err)
	}
}
