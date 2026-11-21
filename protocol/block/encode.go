package block

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

// The helpers in this file implement the package's local record encoding.
// It exists so the schema is checkable end to end (presence, zero values,
// closed unions, unknown-field rejection) before E02-02.schemas and
// E02-02.codec fix the real D73 wire field numbers. It assigns no wire tags
// and makes no canonical-bytes claim; the wire transport never uses it.

// jsonMarshal encodes v without HTML escaping and without a trailing newline.
func jsonMarshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// jsonUnmarshal decodes data into v with unknown fields rejected. Silently
// accepted unknown fields would be an ANY-style bypass of the closed schema
// (AC-DAT-01-02), so the local encoding never tolerates them.
func jsonUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("%w: trailing JSON value", ErrInvalidValue)
	}
	return nil
}

// decodeEnumName decodes a JSON string that names an enum value.
func decodeEnumName(data []byte) (string, error) {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return "", fmt.Errorf("%w: enum value: %v", ErrInvalidValue, err)
	}
	return name, nil
}

// anyEqual compares two values of the same type for Optional.Equal.
func anyEqual(a, b any) bool {
	return reflect.DeepEqual(a, b)
}

// Marshal encodes any schema value with the local record encoding.
func Marshal(v any) ([]byte, error) { return jsonMarshal(v) }

// Unmarshal decodes any schema value with unknown fields rejected.
func Unmarshal(data []byte, v any) error { return jsonUnmarshal(data, v) }
