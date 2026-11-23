package eth

import (
	"errors"
	"fmt"
	"io"
)

// RLP errors. Every rejection names the offending position; a malformed or
// oversized input is an explicit normalization error, never empty fallback
// bytes (D76).
var (
	// ErrRLP reports malformed RLP framing.
	ErrRLP = errors.New("eth: malformed RLP")
	// ErrRLPTrailing reports bytes after the end of the top-level item.
	ErrRLPTrailing = errors.New("eth: trailing bytes after the RLP payload")
	// ErrRLPWidth reports a scalar or string whose width violates the
	// canonical minimal-encoding rules.
	ErrRLPWidth = errors.New("eth: non-canonical RLP width")
)

// rlpStringSize returns the encoded size of a byte string of length n.
func rlpStringSize(n uint64) uint64 {
	switch {
	case n < 56:
		return n + 1
	case n < 1<<8:
		return n + 2
	case n < 1<<16:
		return n + 3
	case n < 1<<24:
		return n + 4
	default:
		return n + 5
	}
}

// rlpListSize returns the encoded size of a list whose payload has size p.
func rlpListSize(p uint64) uint64 {
	switch {
	case p < 56:
		return p + 1
	case p < 1<<8:
		return p + 2
	case p < 1<<16:
		return p + 3
	case p < 1<<24:
		return p + 4
	default:
		return p + 5
	}
}

// rlpUintSize returns the minimal byte width of an unsigned value.
func rlpUintSize(v uint64) uint64 {
	switch {
	case v < 0x80:
		return 1
	case v <= 0xff:
		return 1 + 1
	case v <= 0xffff:
		return 1 + 2
	case v <= 0xffffff:
		return 1 + 3
	case v <= 0xffffffff:
		return 1 + 4
	case v <= 0xffffffffff:
		return 1 + 5
	case v <= 0xffffffffffff:
		return 1 + 6
	case v <= 0xffffffffffffff:
		return 1 + 7
	default:
		return 1 + 8
	}
}

// writeRLPHeader writes the prefix of a string (isList false) or list
// (isList true) whose payload has length n.
func writeRLPHeader(w io.Writer, isList bool, n uint64) error {
	var kind byte = 0x80
	if isList {
		kind = 0xc0
	}
	var head []byte
	switch {
	case n < 56:
		head = []byte{kind + byte(n)}
	case n < 1<<8:
		head = []byte{kind + 56, byte(n)}
	case n < 1<<16:
		head = []byte{kind + 57, byte(n >> 8), byte(n)}
	case n < 1<<24:
		head = []byte{kind + 58, byte(n >> 16), byte(n >> 8), byte(n)}
	default:
		head = []byte{kind + 59, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
	_, err := w.Write(head)
	return err
}

// writeRLPBytes writes one byte string in canonical form: a single byte
// below 0x80 encodes itself; everything else gets the minimal header.
func writeRLPBytes(w io.Writer, b []byte) error {
	if len(b) == 1 && b[0] < 0x80 {
		_, err := w.Write(b)
		return err
	}
	if err := writeRLPHeader(w, false, uint64(len(b))); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// writeRLPUint writes one unsigned scalar in minimal big-endian form: the
// canonical encoding of zero is the empty string 0x80.
func writeRLPUint(w io.Writer, v uint64) error {
	if v == 0 {
		_, err := w.Write([]byte{0x80})
		return err
	}
	if v < 0x80 {
		_, err := w.Write([]byte{byte(v)})
		return err
	}
	width := rlpUintSize(v) - 1
	var buf [9]byte
	buf[0] = 0x80 + byte(width)
	for i := int(width); i > 0; i-- {
		buf[i] = byte(v)
		v >>= 8
	}
	_, err := w.Write(buf[:width+1])
	return err
}

// rlpItem is one decoded RLP item: a string view (IsList false) or a list
// whose payload spans Payload in the source bytes. SelfEncoded marks a
// single byte below 0x80 that encoded itself, which is the canonical form
// of small scalars.
type rlpItem struct {
	IsList      bool
	SelfEncoded bool
	String      []byte
	Payload     []byte
}

// rlpRead decodes exactly one item from the front of src and returns the
// item and the rest. It copies nothing: the views alias src.
func rlpRead(src []byte) (rlpItem, []byte, error) {
	if len(src) == 0 {
		return rlpItem{}, nil, fmt.Errorf("%w: empty input", ErrRLP)
	}
	prefix := src[0]
	rest := src[1:]
	switch {
	case prefix < 0x80:
		return rlpItem{String: src[:1], SelfEncoded: true}, rest, nil
	case prefix < 0xb8:
		n := int(prefix - 0x80)
		if len(rest) < n {
			return rlpItem{}, nil, fmt.Errorf("%w: short string of %d bytes", ErrRLP, n)
		}
		if n == 1 && rest[0] < 0x80 {
			return rlpItem{}, nil, fmt.Errorf("%w: single byte %d must encode itself", ErrRLPWidth, rest[0])
		}
		return rlpItem{String: rest[:n]}, rest[n:], nil
	case prefix < 0xc0:
		width := int(prefix - 0xb7)
		n, err := rlpLength(rest, width)
		if err != nil {
			return rlpItem{}, nil, err
		}
		if n < 56 {
			return rlpItem{}, nil, fmt.Errorf("%w: long string header for %d bytes", ErrRLPWidth, n)
		}
		body := rest[width:]
		if uint64(len(body)) < n {
			return rlpItem{}, nil, fmt.Errorf("%w: short long-string of %d bytes", ErrRLP, n)
		}
		return rlpItem{String: body[:n]}, body[n:], nil
	case prefix < 0xf8:
		n := int(prefix - 0xc0)
		if len(rest) < n {
			return rlpItem{}, nil, fmt.Errorf("%w: short list of %d bytes", ErrRLP, n)
		}
		return rlpItem{IsList: true, Payload: rest[:n]}, rest[n:], nil
	default:
		width := int(prefix - 0xf7)
		n, err := rlpLength(rest, width)
		if err != nil {
			return rlpItem{}, nil, err
		}
		if n < 56 {
			return rlpItem{}, nil, fmt.Errorf("%w: long list header for %d bytes", ErrRLPWidth, n)
		}
		body := rest[width:]
		if uint64(len(body)) < n {
			return rlpItem{}, nil, fmt.Errorf("%w: short long-list of %d bytes", ErrRLP, n)
		}
		return rlpItem{IsList: true, Payload: body[:n]}, body[n:], nil
	}
}

// rlpLength reads a width-byte big-endian length and checks it is minimal.
func rlpLength(rest []byte, width int) (uint64, error) {
	if len(rest) < width {
		return 0, fmt.Errorf("%w: truncated length prefix", ErrRLP)
	}
	var n uint64
	for i := 0; i < width; i++ {
		n = n<<8 | uint64(rest[i])
	}
	if width > 1 && rest[0] == 0 {
		return 0, fmt.Errorf("%w: leading zero in length prefix", ErrRLPWidth)
	}
	return n, nil
}

// rlpReadListItems iterates the payload of a list item.
func rlpReadListItems(item rlpItem) ([]rlpItem, error) {
	if !item.IsList {
		return nil, fmt.Errorf("%w: expected a list", ErrRLP)
	}
	var out []rlpItem
	payload := item.Payload
	for len(payload) > 0 {
		next, rest, err := rlpRead(payload)
		if err != nil {
			return nil, err
		}
		out = append(out, next)
		payload = rest
	}
	return out, nil
}

// uintValue decodes a minimal unsigned scalar from this item: a
// self-encoded byte is its own value; a string payload follows the string
// rules (empty is zero, one byte must be at least 0x80, no leading zero).
func (i rlpItem) uintValue() (uint64, error) {
	if i.IsList {
		return 0, fmt.Errorf("%w: scalar is a list", ErrRLP)
	}
	if i.SelfEncoded {
		return uint64(i.String[0]), nil
	}
	return rlpUintValue(i.String)
}

// rlpUintValue decodes a minimal unsigned scalar from a string item.
func rlpUintValue(s []byte) (uint64, error) {
	switch len(s) {
	case 0:
		return 0, nil
	case 1:
		// A single byte below 0x80 must have encoded itself; 0x80 and
		// above are the legitimate one-byte payloads of a string header.
		if s[0] < 0x80 {
			return 0, fmt.Errorf("%w: non-minimal single byte %d", ErrRLPWidth, s[0])
		}
		return uint64(s[0]), nil
	default:
		if s[0] == 0 {
			return 0, fmt.Errorf("%w: leading zero in scalar", ErrRLPWidth)
		}
		var v uint64
		for _, b := range s {
			v = v<<8 | uint64(b)
		}
		return v, nil
	}
}
