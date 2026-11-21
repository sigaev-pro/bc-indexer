package block

import (
	"encoding/hex"
	"strings"
	"testing"
)

// mustHash32 decodes 64 hex digits into a Hash32.
func mustHash32(t *testing.T, s string) Hash32 {
	t.Helper()
	if len(s) != 64 {
		t.Fatalf("hash32 fixture %q is not 64 hex digits", s)
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hash32 fixture %q: %v", s, err)
	}
	var h Hash32
	copy(h[:], b)
	return h
}

// mustAddr20 decodes 40 hex digits into an Address20.
func mustAddr20(t *testing.T, s string) Address20 {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 20 {
		t.Fatalf("address20 fixture %q", s)
	}
	var a Address20
	copy(a[:], b)
	return a
}

// mustBytes decodes any even-length hex string.
func mustBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bytes fixture %.32s...: %v", s, err)
	}
	return b
}

// u256 converts a uint64 into its 32-byte big-endian form.
func u256(v uint64) UInt256 {
	var out UInt256
	for i := 0; i < 8; i++ {
		out[31-i] = byte(v >> (8 * i))
	}
	return out
}

// mustID16 builds a nonzero identity from a byte pattern.
func mustID16(seed byte) Id16 {
	var id Id16
	for i := range id {
		id[i] = seed
	}
	return id
}

func TestOptionalPresenceIsExplicit(t *testing.T) {
	var none Optional[uint64]
	if none.Present() {
		t.Fatal("zero Optional must be absent")
	}
	if _, ok := none.Get(); ok {
		t.Fatal("absent Optional must not yield a value")
	}

	zero := Some(uint64(0))
	if !zero.Present() {
		t.Fatal("present zero must stay present")
	}
	if v, ok := zero.Get(); !ok || v != 0 {
		t.Fatalf("present zero must read back as 0, got %d, %v", v, ok)
	}
	if zero.Equal(none) {
		t.Fatal("present zero and absent must differ")
	}
	if !None[uint64]().Equal(none) {
		t.Fatal("two absent optionals must be equal")
	}
	if !Some(uint64(7)).Equal(Some(uint64(7))) {
		t.Fatal("equal optionals must be equal")
	}
}

func TestOptionalJSONZeroIsNotAbsence(t *testing.T) {
	present, err := Marshal(Some(uint64(0)))
	if err != nil {
		t.Fatal(err)
	}
	if string(present) != "0" {
		t.Fatalf("present zero must encode as 0, got %s", present)
	}
	absent, err := Marshal(None[uint64]())
	if err != nil {
		t.Fatal(err)
	}
	if string(absent) != "null" {
		t.Fatalf("absent must encode as null, got %s", absent)
	}

	var back Optional[uint64]
	if err := Unmarshal(present, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Present() || back.MustGet() != 0 {
		t.Fatalf("present zero lost in round trip: %v", back)
	}
	if err := Unmarshal(absent, &back); err != nil {
		t.Fatal(err)
	}
	if back.Present() {
		t.Fatal("null decoded as present")
	}
}

func TestFixedWidthHexEncoding(t *testing.T) {
	h := mustHash32(t, fixtureEthBlock1Hash)
	if h.String() != fixtureEthBlock1Hash {
		t.Fatalf("hash text form mismatch: %s", h)
	}
	if h.IsZero() {
		t.Fatal("real block hash must not be zero")
	}
	var zero Hash32
	if !zero.IsZero() {
		t.Fatal("zero hash must report zero")
	}

	text, err := h.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(text), fixtureEthBlock1Hash[:8]) {
		t.Fatalf("marshal text mismatch: %s", text)
	}

	var back Hash32
	if err := back.UnmarshalText([]byte("0x" + fixtureEthBlock1Hash)); err != nil {
		t.Fatal(err)
	}
	if back != h {
		t.Fatal("0x-prefixed hash must decode identically")
	}
	var bad Hash32
	if err := bad.UnmarshalText([]byte("1234")); err == nil {
		t.Fatal("short hex must be rejected")
	}
}

func TestTimestampPrecisionRules(t *testing.T) {
	sec := TimestampFromSeconds(1558348307)
	if err := sec.Validate(); err != nil {
		t.Fatalf("SECONDS timestamp rejected: %v", err)
	}
	if sec.Nanos != 0 || sec.Precision != PrecisionSeconds {
		t.Fatalf("seconds constructor mismatch: %+v", sec)
	}

	ms := TimestampFromMilliseconds(1794959997*1000 + 800)
	if err := ms.Validate(); err != nil {
		t.Fatalf("MILLISECONDS timestamp rejected: %v", err)
	}
	if ms.Seconds != 1794959997 || ms.Nanos != 800_000_000 || ms.Precision != PrecisionMilliseconds {
		t.Fatalf("millisecond derivation mismatch: %+v", ms)
	}

	cases := []struct {
		name string
		ts   Timestamp
	}{
		{"nanos overflow", Timestamp{1, 1_000_000_000, PrecisionSeconds}},
		{"seconds with nanos", Timestamp{1, 1, PrecisionSeconds}},
		{"millis with sub-milli nanos", Timestamp{1, 500, PrecisionMilliseconds}},
		{"unknown precision", Timestamp{1, 0, Precision(9)}},
	}
	for _, c := range cases {
		if err := c.ts.Validate(); err == nil {
			t.Fatalf("%s must be rejected", c.name)
		}
	}

	// The local encoding round trips the precision by name.
	data, err := Marshal(ms)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"MILLISECONDS"`) {
		t.Fatalf("precision name missing: %s", data)
	}
	var back Timestamp
	if err := Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back != ms {
		t.Fatalf("timestamp round trip mismatch: %+v vs %+v", back, ms)
	}
	if err := Unmarshal([]byte(`{"seconds":1,"nanos":0,"precision":"HOURS"}`), &back); err == nil {
		t.Fatal("unknown precision name must be rejected")
	}
}

func TestNetworkIdentityRequiresRealIdentities(t *testing.T) {
	net := NetworkIdentity{Family: "EVM", ChainID: u256(1), GenesisHash: mustHash32(t, fixtureEthGenesis)}
	if err := net.Validate(); err != nil {
		t.Fatalf("valid network rejected: %v", err)
	}
	if err := (NetworkIdentity{Family: "ETC", ChainID: u256(1), GenesisHash: net.GenesisHash}).Validate(); err == nil {
		t.Fatal("non-EVM family must be rejected")
	}
	if err := (NetworkIdentity{Family: "EVM", ChainID: u256(0), GenesisHash: net.GenesisHash}).Validate(); err == nil {
		t.Fatal("zero chain id must be rejected")
	}
	if err := (NetworkIdentity{Family: "EVM", ChainID: u256(1), GenesisHash: Hash32{}}).Validate(); err == nil {
		t.Fatal("zero genesis hash must be rejected: the real genesis has a source hash")
	}
	if err := (AnchorRef{Height: 5, Hash: Hash32{}}).Validate(); err == nil {
		t.Fatal("zero anchor hash must be rejected")
	}
}
