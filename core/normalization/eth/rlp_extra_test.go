package eth

import (
	"bytes"
	"errors"
	"testing"
)

func TestRLPReaderRejectsNonCanonicalForms(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want error
	}{
		{"empty", nil, ErrRLP},
		{"long string for short payload", append([]byte{0xb8, 0x02}, 0x01), ErrRLPWidth},
		{"long list for short payload", append([]byte{0xf8, 0x02}, 0xc0), ErrRLPWidth},
		{"short string as long", append([]byte{0xb8, 0x01}, 0xff), ErrRLPWidth},
		{"leading zero length", []byte{0xb9, 0x00, 0x01, 0x01}, ErrRLPWidth},
		{"short list truncation", []byte{0xc5, 0x01}, ErrRLP},
		{"single byte as string", []byte{0x81, 0x01}, ErrRLPWidth},
	}
	for _, c := range cases {
		if _, _, err := rlpRead(c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: error = %v, want %v", c.name, err, c.want)
		}
	}
	// A minimal canonical pair still reads.
	item, rest, err := rlpRead([]byte{0x01, 0x02})
	if err != nil || item.IsList || !bytes.Equal(item.String, []byte{0x01}) || len(rest) != 1 {
		t.Fatalf("canonical read failed: %+v %v", item, err)
	}
	// Non-minimal scalars are rejected.
	if _, err := rlpUintValue([]byte{0x00, 0x01}); !errors.Is(err, ErrRLPWidth) {
		t.Errorf("leading zero scalar error = %v", err)
	}
	if v, err := rlpUintValue([]byte{0x80}); err != nil || v != 128 {
		t.Errorf("scalar 0x80 string = %d, %v; want 128 (the canonical form of 128)", v, err)
	}
	if item, _, err := rlpRead([]byte{0x7f}); err != nil {
		t.Errorf("self-encoded 127 read error = %v", err)
	} else if v, err := item.uintValue(); err != nil || v != 127 {
		t.Errorf("self-encoded 127 = %d, %v", v, err)
	}
	if v, err := rlpUintValue(nil); err != nil || v != 0 {
		t.Errorf("empty scalar = %d, %v", v, err)
	}
}

func TestWriteConsensusUintBoundary(t *testing.T) {
	// Cumulative gas 0 encodes as the single byte 0x80, 127 as itself and
	// 128 as 0x81 0x80 (Geth consensus encoding of the scalar).
	r := corpusReceiptNoLogs()
	for _, c := range []struct {
		gas  uint64
		want string
	}{{0, "80"}, {127, "7f"}, {128, "8180"}, {65536, "83010000"}} {
		rr := *r
		rr.CumulativeGasUsed = c.gas
		enc, err := SourceEncoding(&rr, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifySourceEncoding(enc, &rr); err != nil {
			t.Fatalf("gas %d: %v", c.gas, err)
		}
		// The decoded consensus list carries the canonical scalar.
		item, rest, err := rlpRead(enc.Bytes)
		if err != nil || len(rest) != 0 {
			t.Fatalf("gas %d: consensus list unreadable: %v", c.gas, err)
		}
		fields, err := rlpReadListItems(item)
		if err != nil || len(fields) != 4 {
			t.Fatalf("gas %d: fields = %d, %v", c.gas, len(fields), err)
		}
		scalar, err := fields[1].uintValue()
		if err != nil || scalar != c.gas {
			t.Fatalf("gas %d decoded back as %d, %v", c.gas, scalar, err)
		}
		var buf bytes.Buffer
		if err := writeRLPUint(&buf, c.gas); err != nil {
			t.Fatal(err)
		}
		if got := hexStringOf(buf.Bytes()); got != c.want {
			t.Fatalf("gas %d encoded as %s, want %s", c.gas, got, c.want)
		}
	}
}

func hexStringOf(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, v := range b {
		out = append(out, hexdigits[v>>4], hexdigits[v&0xf])
	}
	return string(out)
}
