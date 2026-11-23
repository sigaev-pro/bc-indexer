package poly

import "errors"

// A minimal, strict RLP reader and a small encoder for the exact native
// structures the normalizer must verify: the StateSync transaction envelope
// (0x7f || RLP(items)), the clerk event record, and the consensus/storage
// receipt framing of D75. It is deliberately not a general codec: every
// decode is canonical (minimal length encodings, no trailing bytes) and
// depth- and size-bounded by the caller-visible inputs, so malformed source
// data is an explicit error, never a silently accepted value.

var (
	// ErrRLP marks every RLP decode violation.
	ErrRLP = errors.New("poly: invalid RLP")
	// ErrTrailing marks input with bytes after the decoded item.
	ErrTrailing = errors.New("poly: trailing bytes after RLP item")
)

// rlpString is one decoded RLP value: either a byte string or a list whose
// content is the exact inner encoding.
type rlpString struct {
	IsList bool
	Bytes  []byte
}

// rlpDecode decodes exactly one item from data and requires that no bytes
// remain.
func rlpDecode(data []byte) (rlpString, error) {
	item, rest, err := rlpItem(data)
	if err != nil {
		return item, err
	}
	if len(rest) != 0 {
		return item, ErrTrailing
	}
	return item, nil
}

// rlpItem decodes one item and returns the unread remainder.
func rlpItem(data []byte) (rlpString, []byte, error) {
	if len(data) == 0 {
		return rlpString{}, nil, ErrRLP
	}
	prefix := data[0]
	switch {
	case prefix <= 0x7f:
		// A single byte is its own encoding.
		return rlpString{Bytes: data[:1]}, data[1:], nil
	case prefix <= 0xb7:
		n := int(prefix - 0x80)
		if len(data) < 1+n {
			return rlpString{}, nil, ErrRLP
		}
		str := data[1 : 1+n]
		if n == 1 && str[0] <= 0x7f {
			return rlpString{}, nil, ErrRLP // non-minimal single byte
		}
		return rlpString{Bytes: str}, data[1+n:], nil
	case prefix <= 0xbf:
		lenOfLen := int(prefix - 0xb7)
		if lenOfLen > 8 {
			return rlpString{}, nil, ErrRLP
		}
		if len(data) < 1+lenOfLen {
			return rlpString{}, nil, ErrRLP
		}
		n, _, err := rlpUint(data[1 : 1+lenOfLen])
		if err != nil || n < 56 || uint64(len(data)-1-lenOfLen) < n {
			return rlpString{}, nil, ErrRLP
		}
		payload := data[1+lenOfLen:]
		return rlpString{Bytes: payload[:n]}, payload[n:], nil
	case prefix <= 0xf7:
		n := int(prefix - 0xc0)
		if len(data) < 1+n {
			return rlpString{}, nil, ErrRLP
		}
		return rlpString{IsList: true, Bytes: data[1 : 1+n]}, data[1+n:], nil
	default:
		lenOfLen := int(prefix - 0xf7)
		if lenOfLen > 8 {
			return rlpString{}, nil, ErrRLP
		}
		if len(data) < 1+lenOfLen {
			return rlpString{}, nil, ErrRLP
		}
		n, _, err := rlpUint(data[1 : 1+lenOfLen])
		if err != nil || n < 56 || uint64(len(data)-1-lenOfLen) < n {
			return rlpString{}, nil, ErrRLP
		}
		payload := data[1+lenOfLen:]
		return rlpString{IsList: true, Bytes: payload[:n]}, payload[n:], nil
	}
}

// rlpList decodes a list item and returns its element encodings.
func rlpList(data []byte) ([]rlpString, error) {
	item, err := rlpDecode(data)
	if err != nil {
		return nil, err
	}
	if !item.IsList {
		return nil, ErrRLP
	}
	return rlpListContent(item.Bytes)
}

func rlpListContent(content []byte) ([]rlpString, error) {
	var out []rlpString
	for len(content) > 0 {
		item, rest, err := rlpItem(content)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
		content = rest
	}
	return out, nil
}

// rlpUint decodes a canonical minimal big-endian length field of exactly
// width bytes and returns the remainder.
func rlpUint(data []byte) (uint64, []byte, error) {
	if len(data) == 0 || len(data) > 8 {
		return 0, nil, ErrRLP
	}
	if data[0] == 0 {
		return 0, nil, ErrRLP // no leading zeros in a canonical length
	}
	var v uint64
	for _, b := range data {
		v = v<<8 | uint64(b)
	}
	return v, nil, nil
}

// rlpDecodeUint decodes an RLP byte string as a canonical unsigned integer:
// minimal encoding, no leading zero bytes, and a value of zero only as the
// empty string.
func rlpDecodeUint(item rlpString) (uint64, error) {
	if item.IsList {
		return 0, ErrRLP
	}
	if len(item.Bytes) == 0 {
		return 0, nil
	}
	if len(item.Bytes) > 8 || item.Bytes[0] == 0 {
		return 0, ErrRLP
	}
	var v uint64
	for _, b := range item.Bytes {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

// rlpEncodeUint encodes a non-negative integer as a canonical RLP byte
// string.
func rlpEncodeUint(v uint64) []byte {
	if v == 0 {
		return []byte{0x80}
	}
	var digits [8]byte
	n := 0
	for x := v; x > 0; x >>= 8 {
		n++
		digits[8-n] = byte(x)
	}
	return rlpEncodeBytes(digits[8-n:])
}

// rlpEncodeBytes encodes a byte string.
func rlpEncodeBytes(b []byte) []byte {
	if len(b) == 1 && b[0] <= 0x7f {
		return []byte{b[0]}
	}
	return append(rlpLengthPrefix(len(b), 0x80), b...)
}

// rlpLengthPrefix returns the string (offset 0x80) or list (offset 0xc0)
// length prefix for a payload of n bytes.
func rlpLengthPrefix(n int, offset byte) []byte {
	if n < 56 {
		return []byte{offset + byte(n)}
	}
	var digits [8]byte
	count := 0
	for x := n; x > 0; x >>= 8 {
		count++
		digits[8-count] = byte(x)
	}
	prefix := []byte{offset + 0x37 + byte(count)}
	return append(prefix, digits[8-count:]...)
}

// rlpEncodeList encodes a list from the already-encoded elements.
func rlpEncodeList(elements ...[]byte) []byte {
	var content []byte
	for _, e := range elements {
		content = append(content, e...)
	}
	return append(rlpLengthPrefix(len(content), 0xc0), content...)
}
