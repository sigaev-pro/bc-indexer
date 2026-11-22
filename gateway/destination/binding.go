// The immutable DestinationBinding descriptor: its strict JSON form, its
// network and descriptor digests, and its route rules
// (CONTROL_WIRE_CONTRACT, "DestinationBinding file"). The file is the
// desired descriptor; the old binding, seal, drain id and generation come
// only from the durable store, never from new YAML fields.
package destination

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// MaxDescriptorBytes bounds the full JSON descriptor (16 KiB).
const MaxDescriptorBytes = 16 * 1024

// bindingDigestDomain opens the descriptor digest preimage.
const bindingDigestDomain = "extractor.binding.v1"

// schemaVersion1 is the only descriptor schema version.
const schemaVersion1 = 1

// Named errors of the binding layer.
var (
	// ErrDescriptor rejects a structurally invalid descriptor: duplicate
	// or unknown keys, wrong types, wrong widths, overflow.
	ErrDescriptor = fmt.Errorf("destination: invalid binding descriptor")

	// ErrDigestMismatch rejects a descriptor whose stated digest differs
	// from the computed digest of its content.
	ErrDigestMismatch = fmt.Errorf("destination: descriptor digest mismatch")

	// ErrGeneration rejects an invalid or conflicting binding generation:
	// non-canonical text, zero, or an unexpected jump.
	ErrGeneration = fmt.Errorf("destination: invalid binding generation")
)

// Binding is the immutable destination descriptor:
// {destination_id, generation, network, expected_account, stream,
// subject_prefix, descriptor_digest}. Changing account, stream or prefix
// requires the next binding generation of the same destination_id and a
// DRAINED store; changing the network identity is never allowed.
type Binding struct {
	DestinationID    proto.ID16
	Generation       uint64
	Network          proto.NetworkIdentity
	ExpectedAccount  string
	Stream           string
	SubjectPrefix    string
	DescriptorDigest proto.Hash32
}

// NetworkDigest returns the network digest of the binding:
// SHA256("extractor.network.v1" || 0x00 || 0x01 || chain_id || genesis),
// the exact value the stream metadata key extractor.network_digest must
// carry.
func (b Binding) NetworkDigest() (proto.Hash32, error) {
	return b.Network.Digest()
}

// DescriptorDigestComputed returns the computed digest of the binding:
//
//	SHA256("extractor.binding.v1" || 0x00 || destination_id16 ||
//	    generation_u64be || 0x01 || chain_id32 || genesis32 ||
//	    LP(expected_account) || LP(stream) || LP(subject_prefix))
//
// with LP(s)=u32be(len(UTF8(s))) || UTF8(s). The digest never covers
// itself, the URL, TLS credentials or frame limits, so JSON formatting
// cannot change it and identity fields always do.
func (b Binding) DescriptorDigestComputed() (proto.Hash32, error) {
	if err := b.validateContent(); err != nil {
		return proto.Hash32{}, err
	}
	var out proto.Hash32
	h := sha256.New()
	h.Write([]byte(bindingDigestDomain))
	h.Write([]byte{0})
	h.Write(b.DestinationID[:])
	var gen [8]byte
	binary.BigEndian.PutUint64(gen[:], b.Generation)
	h.Write(gen[:])
	h.Write([]byte{byte(proto.NetworkFamilyEVM)})
	h.Write(b.Network.ChainID[:])
	h.Write(b.Network.GenesisHash[:])
	writeLP(h, b.ExpectedAccount)
	writeLP(h, b.Stream)
	writeLP(h, b.SubjectPrefix)
	h.Sum(out[:0])
	return out, nil
}

// writeLP writes the length-prefixed UTF-8 form of s.
func writeLP(h interface{ Write([]byte) (int, error) }, s string) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(s)))
	h.Write(size[:])
	h.Write([]byte(s))
}

// validateContent checks every field of the binding except the stated
// digest, so both parsing and digest computation share one rule set.
func (b Binding) validateContent() error {
	if b.DestinationID.IsZero() {
		return fmt.Errorf("%w: destination id is zero", ErrDescriptor)
	}
	if b.Generation == 0 {
		return fmt.Errorf("%w: generation is zero", ErrGeneration)
	}
	if err := b.Network.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrDescriptor, err)
	}
	if b.ExpectedAccount == "" {
		return fmt.Errorf("%w: expected account is empty", ErrDescriptor)
	}
	for _, r := range b.ExpectedAccount {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: expected account holds a control character", ErrDescriptor)
		}
	}
	if err := ValidateStreamName(b.Stream); err != nil {
		return err
	}
	if err := ValidateSubjectPrefix(b.SubjectPrefix); err != nil {
		return err
	}
	return nil
}

// Validate checks the content and that the stated digest equals the
// computed digest.
func (b Binding) Validate() error {
	if err := b.validateContent(); err != nil {
		return err
	}
	computed, err := b.DescriptorDigestComputed()
	if err != nil {
		return err
	}
	if b.DescriptorDigest != computed {
		return fmt.Errorf("%w: stated %x, computed %x",
			ErrDigestMismatch, b.DescriptorDigest, computed)
	}
	return nil
}

// ValidateStreamName rejects an empty stream name, whitespace or NATS
// wildcard characters: the stream name is a literal, never a pattern.
func ValidateStreamName(stream string) error {
	if stream == "" {
		return fmt.Errorf("%w: stream name is empty", ErrDescriptor)
	}
	return validateLiteralTokens(stream, "stream name", false)
}

// ValidateSubjectPrefix rejects an empty prefix, whitespace, wildcards and
// empty dot-separated tokens.
func ValidateSubjectPrefix(prefix string) error {
	if prefix == "" {
		return fmt.Errorf("%w: subject prefix is empty", ErrDescriptor)
	}
	return validateLiteralTokens(prefix, "subject prefix", true)
}

// validateLiteralTokens checks the no-whitespace/no-wildcard rule; when
// dotTokens is set it also rejects empty tokens around dots.
func validateLiteralTokens(s, what string, dotTokens bool) error {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			return fmt.Errorf("%w: %s holds whitespace", ErrDescriptor, what)
		}
		if c == '*' || c == '>' {
			return fmt.Errorf("%w: %s holds a wildcard %q", ErrDescriptor, what, string(c))
		}
	}
	if dotTokens {
		tokens := strings.Split(s, ".")
		for _, tok := range tokens {
			if tok == "" {
				return fmt.Errorf("%w: %s holds an empty token", ErrDescriptor, what)
			}
		}
	}
	return nil
}

// ParseBinding decodes and validates one descriptor: UTF-8 without BOM, at
// most 16 KiB, JSON objects with exactly the pinned keys, no duplicate or
// unknown keys, no comments, schema_version 1, generation as a canonical
// decimal string, all hex fields lowercase without 0x, and the stated
// descriptor digest equal to the computed digest. JSON key order and
// whitespace never matter.
func ParseBinding(data []byte) (Binding, error) {
	b, err := parseDescriptor(data)
	if err != nil {
		return b, err
	}
	computed, err := b.DescriptorDigestComputed()
	if err != nil {
		return b, err
	}
	if b.DescriptorDigest != computed {
		return b, fmt.Errorf("%w: stated %x, computed %x",
			ErrDigestMismatch, b.DescriptorDigest, computed)
	}
	return b, nil
}

// parseDescriptor decodes and content-validates one descriptor without
// comparing the stated digest, so a caller can compute the digest of the
// content it just built.
func parseDescriptor(data []byte) (Binding, error) {
	var b Binding
	if len(data) == 0 {
		return b, fmt.Errorf("%w: descriptor is empty", ErrDescriptor)
	}
	if len(data) > MaxDescriptorBytes {
		return b, fmt.Errorf("%w: descriptor is %d bytes, bound is %d",
			ErrDescriptor, len(data), MaxDescriptorBytes)
	}
	if !utf8.Valid(data) {
		return b, fmt.Errorf("%w: descriptor is not valid UTF-8", ErrDescriptor)
	}
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		return b, fmt.Errorf("%w: descriptor carries a BOM", ErrDescriptor)
	}
	fields, err := decodeObject(data)
	if err != nil {
		return b, err
	}
	seen := make(map[string]bool)
	for _, f := range fields {
		if seen[f.name] {
			return b, fmt.Errorf("%w: duplicate key %q", ErrDescriptor, f.name)
		}
		seen[f.name] = true
		switch f.name {
		case "schema_version":
			v, err := decodeInt(f.value)
			if err != nil {
				return b, fmt.Errorf("%w: schema_version: %w", ErrDescriptor, err)
			}
			if v != schemaVersion1 {
				return b, fmt.Errorf("%w: schema_version %d, want %d", ErrDescriptor, v, schemaVersion1)
			}
		case "destination_id":
			if err := decodeHex16(f.value, b.DestinationID[:]); err != nil {
				return b, fmt.Errorf("%w: destination_id: %w", ErrDescriptor, err)
			}
		case "generation":
			g, err := decodeGeneration(f.value)
			if err != nil {
				return b, err
			}
			b.Generation = g
		case "network":
			if err := decodeNetwork(f.value, &b.Network); err != nil {
				return b, err
			}
		case "expected_account":
			s, err := decodeString(f.value)
			if err != nil {
				return b, fmt.Errorf("%w: expected_account: %w", ErrDescriptor, err)
			}
			b.ExpectedAccount = s
		case "stream":
			s, err := decodeString(f.value)
			if err != nil {
				return b, fmt.Errorf("%w: stream: %w", ErrDescriptor, err)
			}
			b.Stream = s
		case "subject_prefix":
			s, err := decodeString(f.value)
			if err != nil {
				return b, fmt.Errorf("%w: subject_prefix: %w", ErrDescriptor, err)
			}
			b.SubjectPrefix = s
		case "descriptor_digest":
			if err := decodeHex32(f.value, b.DescriptorDigest[:]); err != nil {
				return b, fmt.Errorf("%w: descriptor_digest: %w", ErrDescriptor, err)
			}
		default:
			return b, fmt.Errorf("%w: unknown key %q", ErrDescriptor, f.name)
		}
	}
	for _, required := range []string{"schema_version", "destination_id", "generation",
		"network", "expected_account", "stream", "subject_prefix", "descriptor_digest"} {
		if !seen[required] {
			return b, fmt.Errorf("%w: missing key %q", ErrDescriptor, required)
		}
	}
	if err := b.validateContent(); err != nil {
		return b, err
	}
	return b, nil
}

// decodeObject walks the top-level JSON object with a token decoder,
// returning its members. Duplicate keys are reported by the caller against
// this list; comments are never valid JSON, so the decoder rejects them.
func decodeObject(data []byte) ([]field, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDescriptor, err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("%w: descriptor is not a JSON object", ErrDescriptor)
	}
	var fields []field
	for dec.More() {
		nameTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrDescriptor, err)
		}
		name, ok := nameTok.(string)
		if !ok {
			return nil, fmt.Errorf("%w: object key is not a string", ErrDescriptor)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("%w: key %q: %w", ErrDescriptor, name, err)
		}
		fields = append(fields, field{name: name, value: raw})
	}
	if _, err := dec.Token(); err != nil { // closing brace
		return nil, fmt.Errorf("%w: %w", ErrDescriptor, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing content after the object", ErrDescriptor)
	}
	return fields, nil
}

// field is one decoded object member.
type field struct {
	name  string
	value json.RawMessage
}

// decodeNetwork decodes the network subobject: family "EVM", chain_id and
// genesis_hash as 64 lowercase hex characters.
func decodeNetwork(raw json.RawMessage, out *proto.NetworkIdentity) error {
	fields, err := decodeRawObject(raw, "network")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, f := range fields {
		if seen[f.name] {
			return fmt.Errorf("%w: network holds duplicate key %q", ErrDescriptor, f.name)
		}
		seen[f.name] = true
		switch f.name {
		case "family":
			s, err := decodeString(f.value)
			if err != nil {
				return fmt.Errorf("%w: network.family: %w", ErrDescriptor, err)
			}
			if s != proto.NetworkFamilyEVM.String() {
				return fmt.Errorf("%w: network family %q, want %q",
					ErrDescriptor, s, proto.NetworkFamilyEVM)
			}
			out.Family = proto.NetworkFamilyEVM
		case "chain_id":
			var hex32 proto.Hash32
			if err := decodeHex32(f.value, hex32[:]); err != nil {
				return fmt.Errorf("%w: network.chain_id: %w", ErrDescriptor, err)
			}
			out.ChainID = hex32
		case "genesis_hash":
			var hex32 proto.Hash32
			if err := decodeHex32(f.value, hex32[:]); err != nil {
				return fmt.Errorf("%w: network.genesis_hash: %w", ErrDescriptor, err)
			}
			out.GenesisHash = hex32
		default:
			return fmt.Errorf("%w: unknown network key %q", ErrDescriptor, f.name)
		}
	}
	for _, required := range []string{"family", "chain_id", "genesis_hash"} {
		if !seen[required] {
			return fmt.Errorf("%w: network misses key %q", ErrDescriptor, required)
		}
	}
	return nil
}

// decodeRawObject decodes one nested object from raw bytes.
func decodeRawObject(raw json.RawMessage, what string) ([]field, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrDescriptor, what, err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("%w: %s is not an object", ErrDescriptor, what)
	}
	var fields []field
	for dec.More() {
		nameTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrDescriptor, what, err)
		}
		name := nameTok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("%w: %s key %q: %w", ErrDescriptor, what, name, err)
		}
		fields = append(fields, field{name: name, value: value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrDescriptor, what, err)
	}
	return fields, nil
}

// decodeString decodes one JSON string.
func decodeString(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}

// decodeInt decodes one JSON integer (no fraction or exponent).
func decodeInt(raw json.RawMessage) (int64, error) {
	num, err := decodeNumber(raw)
	if err != nil {
		return 0, err
	}
	v, err := num.Int64()
	if err != nil {
		return 0, err
	}
	return v, nil
}

// decodeNumber decodes one JSON number token.
func decodeNumber(raw json.RawMessage) (json.Number, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	num, ok := tok.(json.Number)
	if !ok {
		return "", fmt.Errorf("value is not a number")
	}
	return num, nil
}

// decodeGeneration decodes the canonical decimal string form: positive,
// no sign, no leading zeros, no overflow.
func decodeGeneration(raw json.RawMessage) (uint64, error) {
	s, err := decodeString(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: generation: %w", ErrDescriptor, err)
	}
	if s == "0" || s == "" {
		return 0, fmt.Errorf("%w: generation %q is not positive", ErrGeneration, s)
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, fmt.Errorf("%w: generation %q has a leading zero", ErrGeneration, s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("%w: generation %q is not a canonical decimal string",
				ErrGeneration, s)
		}
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: generation %q overflows u64: %w", ErrGeneration, s, err)
	}
	if v > math.MaxUint64 {
		return 0, fmt.Errorf("%w: generation %q overflows u64", ErrGeneration, s)
	}
	return v, nil
}

// decodeHex16 decodes exactly 32 lowercase hex characters into 16 bytes.
func decodeHex16(raw json.RawMessage, out []byte) error {
	s, err := decodeString(raw)
	if err != nil {
		return err
	}
	if len(s) != 32 {
		return fmt.Errorf("length %d, want 32 lowercase hex characters", len(s))
	}
	return hexDecodeInto(s, out)
}

// decodeHex32 decodes exactly 64 lowercase hex characters into 32 bytes.
func decodeHex32(raw json.RawMessage, out []byte) error {
	s, err := decodeString(raw)
	if err != nil {
		return err
	}
	if len(s) != 64 {
		return fmt.Errorf("length %d, want 64 lowercase hex characters", len(s))
	}
	return hexDecodeInto(s, out)
}

// hexDecodeInto decodes lowercase hex without 0x into out.
func hexDecodeInto(s string, out []byte) error {
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return fmt.Errorf("value %q carries a 0x prefix", s)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("value %q is not lowercase hex", s)
		}
	}
	const digits = "0123456789abcdef"
	for i := 0; i < len(out); i++ {
		hi := strings.IndexByte(digits, s[2*i])
		lo := strings.IndexByte(digits, s[2*i+1])
		if hi < 0 || lo < 0 {
			return fmt.Errorf("value %q is not hex", s)
		}
		out[i] = byte(hi<<4 | lo)
	}
	return nil
}
