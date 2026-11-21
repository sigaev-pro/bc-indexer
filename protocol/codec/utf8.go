// Incremental UTF-8 validation of typed UTF-8 nodes. Blob chunk boundaries
// may split one multi-byte rune, so validation carries a partial rune across
// writes; no Unicode normalization is applied (MESSAGE_CONTRACT section 6.1,
// tree rule 3).
package codec

import (
	"fmt"
	"unicode/utf8"
)

// utf8Scanner validates a UTF-8 byte stream incrementally with at most three
// carried bytes of state.
type utf8Scanner struct {
	pending    [utf8.UTFMax]byte
	pendingLen int
}

// runeLen returns the encoded length the lead byte c announces, or an error
// when c cannot start a rune (stray continuation, overlong two-byte lead,
// lead above U+10FFFF).
func runeLen(c byte) (int, error) {
	switch {
	case c < 0x80:
		return 1, nil
	case c < 0xC2:
		return 0, fmt.Errorf("%w: byte 0x%02x cannot start a rune", ErrUTF8, c)
	case c < 0xE0:
		return 2, nil
	case c < 0xF0:
		return 3, nil
	case c < 0xF5:
		return 4, nil
	default:
		return 0, fmt.Errorf("%w: byte 0x%02x cannot start a rune", ErrUTF8, c)
	}
}

// write consumes p and rejects any invalid encoding, including overlong
// forms, surrogates and stray continuation bytes. Bytes of a rune that is
// only partially present stay pending for the next write.
func (s *utf8Scanner) write(p []byte) error {
	for len(p) > 0 {
		var want int
		if s.pendingLen == 0 {
			n, err := runeLen(p[0])
			if err != nil {
				return err
			}
			want = n
		} else {
			want, _ = runeLen(s.pending[0])
		}
		need := want - s.pendingLen
		if len(p) < need {
			copy(s.pending[s.pendingLen:], p)
			s.pendingLen += len(p)
			return nil
		}
		var buf [utf8.UTFMax]byte
		copy(buf[:s.pendingLen], s.pending[:s.pendingLen])
		copy(buf[s.pendingLen:], p[:need])
		if r, size := utf8.DecodeRune(buf[:want]); r == utf8.RuneError && size <= 1 {
			return fmt.Errorf("%w: invalid %d-byte rune encoding", ErrUTF8, want)
		}
		p = p[need:]
		s.pendingLen = 0
	}
	return nil
}

// end rejects a stream that stops inside a rune.
func (s *utf8Scanner) end() error {
	if s.pendingLen != 0 {
		want, _ := runeLen(s.pending[0])
		return fmt.Errorf("%w: stream ends inside a %d-byte rune", ErrUTF8, want)
	}
	return nil
}
