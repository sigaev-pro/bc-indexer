// Tests of the canonical wire primitives: minimal varints, field keys,
// length-delimited reads and the canonical walk rules (ascending tags,
// consecutive repeats only for repeated fields, unknown fields rejected).
package codec

import (
	"errors"
	"testing"
)

func TestVarintRoundTripAndLength(t *testing.T) {
	values := []uint64{0, 1, 0x7F, 0x80, 0x3FF, 0x400, 1 << 20, 1<<32 - 1,
		1 << 32, 1<<63 - 1, ^uint64(0)}
	for _, v := range values {
		encoded := appendVarint(nil, v)
		if len(encoded) != varintLen(v) {
			t.Fatalf("varintLen(%d)=%d, encoded %d bytes", v, varintLen(v), len(encoded))
		}
		reader := &wireReader{b: encoded}
		got, err := reader.readVarint()
		if err != nil || got != v {
			t.Fatalf("varint %d: got %d, err %v", v, got, err)
		}
	}
}

func TestReadVarintRejectsNonMinimal(t *testing.T) {
	reader := &wireReader{b: []byte{0x80, 0x00}}
	_, err := reader.readVarint()
	assertSentinel(t, ErrMalformed, err)
}

func TestReadVarintRejectsTruncation(t *testing.T) {
	reader := &wireReader{b: []byte{0x80}}
	_, err := reader.readVarint()
	assertSentinel(t, ErrMalformed, err)
}

func TestReadVarintRejectsOverflow(t *testing.T) {
	reader := &wireReader{b: []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x7F}}
	_, err := reader.readVarint()
	assertSentinel(t, ErrMalformed, err)
}

func TestReadKeyRejectsFieldZero(t *testing.T) {
	reader := &wireReader{b: []byte{0x00}}
	_, _, err := reader.readKey()
	assertSentinel(t, ErrMalformed, err)
}

func TestReadBytesCopies(t *testing.T) {
	source := []byte{0x03, 'a', 'b', 'c'}
	reader := &wireReader{b: source}
	got, err := reader.readBytes()
	if err != nil {
		t.Fatalf("readBytes: %v", err)
	}
	got[0] = 'x'
	if source[1] != 'a' {
		t.Fatal("readBytes returned a slice aliasing the input")
	}
}

func TestReadBytesRejectsTruncation(t *testing.T) {
	reader := &wireReader{b: []byte{0x10, 'a'}}
	_, err := reader.readBytes()
	assertSentinel(t, ErrMalformed, err)
}

func TestWalkFieldsAcceptsConsecutiveRepeats(t *testing.T) {
	// Two consecutive occurrences of repeated field 1.
	body := append(appendBytesField(nil, 1, []byte{1}), appendBytesField(nil, 1, []byte{2})...)
	reader := &wireReader{b: body}
	seen := 0
	_, err := walkFields("M", reader, 1, 1, map[uint32]bool{1: true},
		func(field uint32, wire byte, r *wireReader) error {
			if _, err := r.readBytes(); err != nil {
				return err
			}
			seen++
			return nil
		})
	if err != nil || seen != 2 {
		t.Fatalf("repeated fields: seen %d, err %v", seen, err)
	}
}

func TestWalkFieldsRejectsNonConsecutiveRepeat(t *testing.T) {
	// Field 1, field 2, field 1 again: a singular repeat after another tag.
	body := appendBytesField(nil, 1, []byte{1})
	body = appendVarintField(body, 2, 7)
	body = appendBytesField(body, 1, []byte{2})
	reader := &wireReader{b: body}
	_, err := walkFields("M", reader, 1, 2, nil,
		func(field uint32, wire byte, r *wireReader) error { return nil })
	assertSentinel(t, ErrMalformed, err)
}

func TestWalkFieldsRejectsUnknownFields(t *testing.T) {
	body := appendVarintField(nil, 1, 1)
	body = appendVarintField(body, 9, 1)
	reader := &wireReader{b: body}
	_, err := walkFields("M", reader, 1, 2, nil,
		func(field uint32, wire byte, r *wireReader) error { return nil })
	assertSentinel(t, ErrMalformed, err)
}

func TestWalkFieldsRejectsSingularDuplicate(t *testing.T) {
	body := append(appendVarintField(nil, 1, 1), appendVarintField(nil, 1, 2)...)
	reader := &wireReader{b: body}
	_, err := walkFields("M", reader, 1, 2, nil,
		func(field uint32, wire byte, r *wireReader) error { return nil })
	assertSentinel(t, ErrMalformed, err)
}

func TestWalkFieldsRejectsDescendingOrder(t *testing.T) {
	body := append(appendVarintField(nil, 2, 1), appendVarintField(nil, 1, 2)...)
	reader := &wireReader{b: body}
	_, err := walkFields("M", reader, 1, 2, nil,
		func(field uint32, wire byte, r *wireReader) error { return nil })
	assertSentinel(t, ErrMalformed, err)
}

func TestUTF8ScannerAcceptsSplitRunes(t *testing.T) {
	// Split the value after every possible boundary, including inside the
	// two-byte "✓" and the four-byte "🜛".
	value := []byte("ascii ✓ prefix 🜛ᛗ tail")
	for split := 0; split <= len(value); split++ {
		var scanner utf8Scanner
		if err := scanner.write(value[:split]); err != nil {
			t.Fatalf("split %d first half: %v", split, err)
		}
		if err := scanner.write(value[split:]); err != nil {
			t.Fatalf("split %d second half: %v", split, err)
		}
		if err := scanner.end(); err != nil {
			t.Fatalf("split %d end: %v", split, err)
		}
	}
}

func TestUTF8ScannerRejectsInvalidSequences(t *testing.T) {
	cases := [][]byte{
		{0x80},                   // stray continuation
		{0xC0, 0x80},             // overlong two-byte lead
		{0xC2},                   // rune cut at end of stream
		{0xE0, 0x80, 0x80},       // overlong three-byte
		{0xED, 0xA0, 0x80},       // surrogate
		{0xF5, 0x80, 0x80, 0x80}, // above U+10FFFF
		{0xF0, 0x9F, 0x98},       // rune cut at end of stream
		{0xF0, 0x9F, 0x41, 0x80}, // ASCII byte inside a rune
		{0x61, 0xC2, 0x61},       // two-byte lead with ASCII continuation
	}
	for _, invalid := range cases {
		var scanner utf8Scanner
		err := scanner.write(invalid)
		if err == nil {
			err = scanner.end()
		}
		if err == nil {
			t.Fatalf("invalid UTF-8 % X accepted", invalid)
		}
		if !errors.Is(err, ErrUTF8) {
			t.Fatalf("invalid UTF-8 % X: %v", invalid, err)
		}
	}
}

func TestUTF8ScannerAcceptsKnownText(t *testing.T) {
	var scanner utf8Scanner
	text := []byte("ledger ✓ αβγ 🜛ᛗ end")
	for i := 0; i < len(text); i++ {
		if err := scanner.write(text[i : i+1]); err != nil {
			t.Fatalf("byte %d: %v", i, err)
		}
	}
	if err := scanner.end(); err != nil {
		t.Fatalf("end: %v", err)
	}
}
