// Canonical wire primitives of the v2 wrapper encoding: minimal unsigned
// varints, protobuf length-delimited fields, single-byte keys for the pinned
// field tables. This is a locally defined canonical encoding for the bounded
// wrapper messages of CONTROL_WIRE_CONTRACT ("TransportFrame layout v2"), not
// a claim about canonical Protobuf in general.
package proto

import (
	"fmt"
	"math"
)

// maxVarintLen is the longest minimal unsigned varint: 10 bytes for 64 bits.
const maxVarintLen = 10

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

// wireReader walks canonical wire bytes. Every read enforces minimality.
type wireReader struct {
	b   []byte
	pos int
}

// done reports whether all bytes were consumed.
func (r *wireReader) done() bool { return r.pos == len(r.b) }

// readVarint reads one minimal unsigned varint. A final zero byte of a
// multi-byte varint and any encoding wider than the value needs are
// malformed; a 10-byte varint whose last byte exceeds 0x01 overflows 64 bits.
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
	if key>>3 > math.MaxUint32 {
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

// fieldSet tracks which fields of one message were seen, so decoders can
// reject a missing named scalar — including a required explicit zero.
type fieldSet uint32

// add records field and reports a duplicate. The field numbers of every
// pinned table fit in bits 0..11.
func (s *fieldSet) add(field uint32) error {
	bit := fieldSet(1) << (field - 1)
	if *s&bit != 0 {
		return fmt.Errorf("%w: field %d appears twice", ErrDuplicateField, field)
	}
	*s |= bit
	return nil
}

// has reports whether field was seen.
func (s fieldSet) has(field uint32) bool { return s&(fieldSet(1)<<(field-1)) != 0 }

// require rejects a missing mandatory field by number.
func (s fieldSet) require(field uint32) error {
	if !s.has(field) {
		return fmt.Errorf("%w: field %d", ErrMissingField, field)
	}
	return nil
}

// seenOrder enforces ascending tag order between consecutive fields. A
// duplicate is reported as a duplicate, a smaller following field as an
// order violation; both are canonical-encoding errors.
func seenOrder(last, field uint32) error {
	if field == last {
		return fmt.Errorf("%w: field %d appears twice", ErrDuplicateField, field)
	}
	if field < last {
		return fmt.Errorf("%w: field %d follows field %d", ErrFieldOrder, field, last)
	}
	return nil
}

// wire type constants used by the pinned tables.
const (
	wireVarint = 0
	wireBytes  = 2
)
