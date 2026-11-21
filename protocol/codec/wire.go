// Canonical wire primitives of the D73 RecordFragment encoding: minimal
// unsigned varints, protobuf length-delimited fields and single-byte keys for
// the pinned field tables, with consecutive occurrences allowed for repeated
// fields. The rules mirror the wrapper encoding of protocol/proto (ascending
// tags, no unknown fields, no duplicate singular fields, every named scalar
// present); this is a locally defined canonical encoding for the bounded
// fragment messages of MESSAGE_CONTRACT section 6.1, not a claim about
// canonical Protobuf in general.
package codec

import "fmt"

// maxVarintLen is the longest minimal unsigned varint: 10 bytes for 64 bits.
const maxVarintLen = 10

// wire type constants used by the pinned tables.
const (
	wireVarint = 0
	wireBytes  = 2
)

// appendVarint appends the minimal unsigned varint of v.
func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// varintLen returns the length of the minimal unsigned varint of v.
func varintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		n++
		v >>= 7
	}
	return n
}

// appendVarintField appends one varint field of the pinned table.
func appendVarintField(b []byte, field uint32, v uint64) []byte {
	b = appendVarint(b, uint64(field)<<3|wireVarint)
	return appendVarint(b, v)
}

// appendBytesField appends one length-delimited field of the pinned table.
func appendBytesField(b []byte, field uint32, v []byte) []byte {
	b = appendVarint(b, uint64(field)<<3|wireBytes)
	b = appendVarint(b, uint64(len(v)))
	return append(b, v...)
}

// wireReader walks canonical wire bytes. Every read enforces minimality.
type wireReader struct {
	b   []byte
	pos int
}

// done reports whether all bytes were consumed.
func (r *wireReader) done() bool { return r.pos == len(r.b) }

// readVarint reads one minimal unsigned varint. A final zero byte of a
// multi-byte varint, any wider-than-needed encoding and a 10-byte varint
// whose last byte exceeds 0x01 are malformed.
func (r *wireReader) readVarint() (uint64, error) {
	var v uint64
	for i := 0; i < maxVarintLen; i++ {
		if r.pos >= len(r.b) {
			return 0, fmt.Errorf("%w: varint truncated at byte %d", ErrMalformed, i)
		}
		c := r.b[r.pos]
		r.pos++
		if i == maxVarintLen-1 && c > 1 {
			return 0, fmt.Errorf("%w: varint overflows 64 bits", ErrMalformed)
		}
		v |= uint64(c&0x7F) << (7 * uint(i))
		if c < 0x80 {
			if i > 0 && c == 0 {
				return 0, fmt.Errorf("%w: non-minimal varint with trailing zero byte", ErrMalformed)
			}
			return v, nil
		}
	}
	return 0, fmt.Errorf("%w: varint longer than %d bytes", ErrMalformed, maxVarintLen)
}

// readKey reads one field key and splits it into field number and wire type.
// Field number 0 is invalid on the wire.
func (r *wireReader) readKey() (field uint32, wire byte, err error) {
	key, err := r.readVarint()
	if err != nil {
		return 0, 0, err
	}
	if key>>3 == 0 {
		return 0, 0, fmt.Errorf("%w: field number 0", ErrMalformed)
	}
	if key>>3 > 0xFFFFFFFF {
		return 0, 0, fmt.Errorf("%w: field number above u32", ErrMalformed)
	}
	return uint32(key >> 3), byte(key & 7), nil
}

// readBytes reads one length-delimited field and returns a copy, so decoded
// messages never alias the caller's input buffer.
func (r *wireReader) readBytes() ([]byte, error) {
	n, err := r.readVarint()
	if err != nil {
		return nil, err
	}
	if n > uint64(len(r.b)-r.pos) {
		return nil, fmt.Errorf("%w: length-delimited field truncated (need %d bytes, have %d)",
			ErrMalformed, n, len(r.b)-r.pos)
	}
	out := make([]byte, n)
	copy(out, r.b[r.pos:r.pos+int(n)])
	r.pos += int(n)
	return out, nil
}

// readFixedBytes reads one length-delimited field of exactly want bytes and
// returns it without copying when the caller discards it right away; the
// slice aliases the reader's buffer only until the next read.
func (r *wireReader) readFixedBytes(wire byte, field uint32, want int, name string) ([]byte, error) {
	if wire != wireBytes {
		return nil, fmt.Errorf("%w: field %d (%s) wire type %d", ErrMalformed, field, name, wire)
	}
	n, err := r.readVarint()
	if err != nil {
		return nil, err
	}
	if n != uint64(want) {
		return nil, fmt.Errorf("%w: field %d (%s) length %d, want %d",
			ErrInvalidValue, field, name, n, want)
	}
	if r.pos+want > len(r.b) {
		return nil, fmt.Errorf("%w: field %d (%s) truncated", ErrMalformed, field, name)
	}
	buf := r.b[r.pos : r.pos+want]
	r.pos += want
	return buf, nil
}

// fieldSet tracks which fields of one message were seen so decoders can
// reject a missing named scalar, including a required explicit zero.
type fieldSet uint32

// add records field and reports a duplicate. The field numbers of every
// pinned D73 table fit in bits 0..11.
func (s *fieldSet) add(field uint32) error {
	bit := fieldSet(1) << (field - 1)
	if *s&bit != 0 {
		return fmt.Errorf("%w: field %d appears twice", ErrMalformed, field)
	}
	*s |= bit
	return nil
}

// has reports whether field was seen.
func (s fieldSet) has(field uint32) bool { return s&(fieldSet(1)<<(field-1)) != 0 }

// require rejects a missing mandatory field by number.
func (s fieldSet) require(field uint32, name string) error {
	if !s.has(field) {
		return fmt.Errorf("%w: %s (field %d)", ErrMissingField, name, field)
	}
	return nil
}

// walkFields walks the canonical encoding of one pinned message body and
// returns the set of fields that appeared. Tags must ascend; the members of
// repeated may repeat consecutively; any other repeat, any tag outside
// [first,last], a non-minimal varint and leftover bytes are named errors.
// The handler reads each field value from r; callers check presence of the
// named scalars against the returned set.
func walkFields(name string, r *wireReader, first, last uint32,
	repeated map[uint32]bool,
	handle func(field uint32, wire byte, r *wireReader) error) (fieldSet, error) {
	var lastField uint32
	var seen fieldSet
	for !r.done() {
		field, wire, err := r.readKey()
		if err != nil {
			return 0, err
		}
		if field == lastField {
			if !repeated[field] {
				return 0, fmt.Errorf("%w: %s field %d appears twice", ErrMalformed, name, field)
			}
		} else if field < lastField {
			return 0, fmt.Errorf("%w: %s field %d follows field %d", ErrMalformed, name, field, lastField)
		}
		lastField = field
		if field < first || field > last {
			return 0, fmt.Errorf("%w: %s field %d is outside the pinned table", ErrMalformed, name, field)
		}
		if !seen.has(field) {
			if err := seen.add(field); err != nil {
				return 0, fmt.Errorf("%w: %s %w", ErrMalformed, name, err)
			}
		}
		if err := handle(field, wire, r); err != nil {
			return 0, err
		}
	}
	return seen, nil
}
