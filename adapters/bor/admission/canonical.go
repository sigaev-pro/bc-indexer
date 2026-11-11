package admission

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrCanonical rejects JSON that the canonical rule does not accept.
var ErrCanonical = errors.New("JSON outside the canonical rule")

// MaxJSONDepth is the largest number of nested arrays and objects.
const MaxJSONDepth = 64

// CanonicalJSON returns the canonical form of a JSON capsule item (D99 item
// 3): integers kept as written, object keys sorted by code point, no
// whitespace, and only '"', '\' and control characters escaped. The README
// gives the full rule; test/harness/admission.py is an independent Python
// implementation.
func CanonicalJSON(data []byte) ([]byte, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: invalid UTF-8", ErrCanonical)
	}
	p := &parser{data: data}
	p.space()
	var out bytes.Buffer
	if err := p.value(&out, 0); err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(p.data) {
		return nil, p.errorf("data after the value")
	}
	return out.Bytes(), nil
}

// simpleEscapes maps the one-character JSON escapes to their bytes.
var simpleEscapes = map[byte]byte{'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}

type parser struct {
	data []byte
	pos  int
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("%w: offset %d: %s", ErrCanonical, p.pos, fmt.Sprintf(format, args...))
}

func (p *parser) space() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) literal(word string) bool {
	if bytes.HasPrefix(p.data[p.pos:], []byte(word)) {
		p.pos += len(word)
		return true
	}
	return false
}

func (p *parser) value(out *bytes.Buffer, depth int) error {
	if p.pos >= len(p.data) {
		return p.errorf("missing value")
	}
	switch c := p.data[p.pos]; {
	case c == '{' || c == '[':
		if depth >= MaxJSONDepth {
			return p.errorf("nesting deeper than %d", MaxJSONDepth)
		}
		if c == '{' {
			return p.object(out, depth+1)
		}
		return p.array(out, depth+1)
	case c == '"':
		text, err := p.str()
		if err != nil {
			return err
		}
		writeString(out, text)
		return nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number(out)
	}
	for _, word := range []string{"true", "false", "null"} {
		if p.literal(word) {
			out.WriteString(word)
			return nil
		}
	}
	return p.errorf("unexpected character %q", p.data[p.pos])
}

func (p *parser) number(out *bytes.Buffer) error {
	start := p.pos
	if p.data[p.pos] == '-' {
		p.pos++
	}
	digits := p.pos
	for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
		p.pos++
	}
	text := string(p.data[start:p.pos])
	switch {
	case p.pos == digits:
		return p.errorf("number without digits")
	case p.data[digits] == '0' && p.pos-digits > 1:
		return p.errorf("number %s has a leading zero", text)
	case text == "-0":
		return p.errorf("negative zero")
	case p.pos < len(p.data) && (p.data[p.pos] == '.' || p.data[p.pos] == 'e' || p.data[p.pos] == 'E'):
		return p.errorf("number %s is not an integer", text)
	}
	out.WriteString(text)
	return nil
}

func (p *parser) hex4() (rune, error) {
	if p.pos+4 > len(p.data) {
		return 0, p.errorf("short unicode escape")
	}
	value, err := strconv.ParseUint(string(p.data[p.pos:p.pos+4]), 16, 32)
	if err != nil {
		return 0, p.errorf("bad unicode escape")
	}
	p.pos += 4
	return rune(value), nil
}

func (p *parser) str() (string, error) {
	p.pos++ // opening quote
	var text []byte
	for {
		if p.pos >= len(p.data) {
			return "", p.errorf("unterminated string")
		}
		c := p.data[p.pos]
		switch {
		case c == '"':
			p.pos++
			return string(text), nil
		case c < 0x20:
			return "", p.errorf("raw control character in a string")
		case c != '\\':
			text = append(text, c)
			p.pos++
			continue
		}
		p.pos++
		if p.pos >= len(p.data) {
			return "", p.errorf("unterminated escape")
		}
		escape := p.data[p.pos]
		p.pos++
		if value, ok := simpleEscapes[escape]; ok {
			text = append(text, value)
			continue
		}
		if escape != 'u' {
			return "", p.errorf("bad escape %q", escape)
		}
		r, err := p.hex4()
		if err != nil {
			return "", err
		}
		if utf16.IsSurrogate(r) {
			if !p.literal(`\u`) {
				return "", p.errorf("lone surrogate escape")
			}
			low, err := p.hex4()
			if err != nil {
				return "", err
			}
			// DecodeRune refuses a low surrogate first and an unpaired high one.
			if r = utf16.DecodeRune(r, low); r == utf8.RuneError {
				return "", p.errorf("lone surrogate escape")
			}
		}
		text = utf8.AppendRune(text, r)
	}
}

func (p *parser) object(out *bytes.Buffer, depth int) error {
	p.pos++ // opening brace
	type member struct {
		key   string
		value []byte
	}
	var members []member
	seen := map[string]bool{}
	p.space()
	if p.literal("}") {
		out.WriteString("{}")
		return nil
	}
	for {
		p.space()
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			return p.errorf("object key is not a string")
		}
		key, err := p.str()
		if err != nil {
			return err
		}
		if seen[key] {
			return p.errorf("duplicate key %q", key)
		}
		seen[key] = true
		p.space()
		if !p.literal(":") {
			return p.errorf("missing colon")
		}
		p.space()
		var value bytes.Buffer
		if err := p.value(&value, depth); err != nil {
			return err
		}
		members = append(members, member{key, value.Bytes()})
		p.space()
		if p.literal("}") {
			break
		}
		if !p.literal(",") {
			return p.errorf("missing comma in an object")
		}
	}
	slices.SortFunc(members, func(x, y member) int { return bytes.Compare([]byte(x.key), []byte(y.key)) })
	out.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			out.WriteByte(',')
		}
		writeString(out, m.key)
		out.WriteByte(':')
		out.Write(m.value)
	}
	out.WriteByte('}')
	return nil
}

func (p *parser) array(out *bytes.Buffer, depth int) error {
	p.pos++ // opening bracket
	p.space()
	out.WriteByte('[')
	if p.literal("]") {
		out.WriteByte(']')
		return nil
	}
	for first := true; ; first = false {
		if !first {
			out.WriteByte(',')
		}
		p.space()
		if err := p.value(out, depth); err != nil {
			return err
		}
		p.space()
		if p.literal("]") {
			out.WriteByte(']')
			return nil
		}
		if !p.literal(",") {
			return p.errorf("missing comma in an array")
		}
	}
}

func writeString(out *bytes.Buffer, text string) {
	out.WriteByte('"')
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '"' || c == '\\':
			out.WriteByte('\\')
			out.WriteByte(c)
		case c < 0x20:
			fmt.Fprintf(out, `\u%04x`, c)
		default:
			out.WriteByte(c)
		}
	}
	out.WriteByte('"')
}
