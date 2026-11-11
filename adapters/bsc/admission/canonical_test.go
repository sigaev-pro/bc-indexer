package admission

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// TestCanonicalVectors checks every case of vectors.json: the output equals
// the independent Python output, and a refused input is ErrCanonical.
func TestCanonicalVectors(t *testing.T) {
	v := loadVectors(t)
	if len(v.Canonical) < 30 {
		t.Fatalf("only %d canonical cases", len(v.Canonical))
	}
	accepted := 0
	for i, c := range v.Canonical {
		input, err := hex.DecodeString(c.Input)
		if err != nil {
			t.Fatalf("case %d input: %v", i, err)
		}
		got, err := CanonicalJSON(input)
		if c.Want == nil {
			if !errors.Is(err, ErrCanonical) || got != nil {
				t.Fatalf("case %d %q: got %q, %v; want ErrCanonical", i, input, got, err)
			}
			continue
		}
		accepted++
		want, _ := hex.DecodeString(*c.Want)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("case %d %q: got %q, %v; want %q", i, input, got, err, want)
		}
		again, err := CanonicalJSON(got)
		if err != nil || !bytes.Equal(again, got) {
			t.Fatalf("case %d: canonical output %q is not stable: %q, %v", i, got, again, err)
		}
	}
	if accepted < 8 {
		t.Fatalf("only %d accepted canonical cases", accepted)
	}
}

func TestCanonicalDepthLimit(t *testing.T) {
	deep := func(n int, open, close string) []byte {
		return append(bytes.Repeat([]byte(open), n), bytes.Repeat([]byte(close), n)...)
	}
	if _, err := CanonicalJSON(deep(MaxJSONDepth, "[", "]")); err != nil {
		t.Fatalf("depth %d arrays: %v", MaxJSONDepth, err)
	}
	if _, err := CanonicalJSON(deep(MaxJSONDepth+1, "[", "]")); !errors.Is(err, ErrCanonical) {
		t.Fatalf("depth %d arrays: %v", MaxJSONDepth+1, err)
	}
	if _, err := CanonicalJSON(deep(MaxJSONDepth, `{"a":`, "}")[:0]); !errors.Is(err, ErrCanonical) {
		t.Fatalf("empty input: %v", err)
	}
	objects := append(bytes.Repeat([]byte(`{"a":`), MaxJSONDepth), '1')
	objects = append(objects, bytes.Repeat([]byte("}"), MaxJSONDepth)...)
	if _, err := CanonicalJSON(objects); err != nil {
		t.Fatalf("depth %d objects: %v", MaxJSONDepth, err)
	}
	objects = append([]byte(`{"a":`), append(objects, '}')...)
	if _, err := CanonicalJSON(objects); !errors.Is(err, ErrCanonical) {
		t.Fatalf("depth %d objects: %v", MaxJSONDepth+1, err)
	}
}

// TestCanonicalRefusalReasons checks that each refusal names its rule.
func TestCanonicalRefusalReasons(t *testing.T) {
	cases := map[string]string{
		"1.0": "not an integer", "[1e3]": "not an integer", "1E3": "not an integer", "-0": "negative zero",
		"01": "leading zero", "-": "without digits", `"\udc00\udc00"`: "lone surrogate", `"\ud800"`: "lone surrogate",
		`{"a":1,"a":2}`: "duplicate key", "\"\x01\"": "raw control character", "{} 1": "data after the value",
	}
	for input, reason := range cases {
		_, err := CanonicalJSON([]byte(input))
		if !errors.Is(err, ErrCanonical) || !strings.Contains(err.Error(), reason) {
			t.Fatalf("%q: %v, want %q", input, err, reason)
		}
	}
}
