// The DestinationBinding descriptor of the deployment: deterministic
// generation, strict self-contained reading and the two exact digests of
// CONTROL_WIRE_CONTRACT. The bootstrap tool additionally validates every
// file with the repository's gateway/destination implementation, so any
// drift between this reader and the contract breaks bootstrap loudly.
package deployconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxDescriptorBytes bounds the full JSON descriptor (16 KiB).
const MaxDescriptorBytes = 16 * 1024

// Binding is the immutable destination descriptor. See CONTROL_WIRE_CONTRACT,
// "DestinationBinding file": changing account, stream or prefix requires
// the next generation of the same destination id after DRAINED; changing
// the network identity is never allowed.
type Binding struct {
	SchemaVersion    int
	DestinationID    [16]byte
	Generation       uint64
	Family           string
	ChainID          [32]byte
	GenesisHash      [32]byte
	ExpectedAccount  string
	Stream           string
	SubjectPrefix    string
	DescriptorDigest [32]byte
}

// destinationDomain derives the deterministic destination id of one
// network line: the deployment pins its bindings, so regeneration always
// produces the same identity for the same network.
const destinationDomain = "extractor.deploy.destination.v1"

// GenerateBinding builds the generation-1 descriptor of one network.
// The destination id is the first 16 bytes of
// SHA256(destinationDomain || 0x00 || network key), pinned per network.
func GenerateBinding(n NetworkDefinition) (Binding, error) {
	b := Binding{
		SchemaVersion:   1,
		Generation:      1,
		Family:          "EVM",
		ExpectedAccount: n.Account,
		Stream:          n.Stream,
		SubjectPrefix:   n.SubjectPrefix,
	}
	h := sha256.New()
	h.Write([]byte(destinationDomain))
	h.Write([]byte{0})
	h.Write([]byte(n.Key))
	copy(b.DestinationID[:], h.Sum(nil)[:16])
	chain, err := hexStringTo32(n.ChainID)
	if err != nil {
		return Binding{}, fmt.Errorf("network %s chain id: %w", n.Key, err)
	}
	b.ChainID = chain
	genesis, err := hexStringTo32(n.GenesisHash)
	if err != nil {
		return Binding{}, fmt.Errorf("network %s genesis hash: %w", n.Key, err)
	}
	b.GenesisHash = genesis
	digest, err := b.DescriptorDigestComputed()
	if err != nil {
		return Binding{}, err
	}
	b.DescriptorDigest = digest
	return b, nil
}

// MarshalJSON renders the descriptor in the fixed key order of the
// contract table, so generation is byte-deterministic.
func (b Binding) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("{\n")
	fmt.Fprintf(&buf, "  \"schema_version\": %d,\n", b.SchemaVersion)
	fmt.Fprintf(&buf, "  \"destination_id\": \"%s\",\n", hex.EncodeToString(b.DestinationID[:]))
	fmt.Fprintf(&buf, "  \"generation\": \"%s\",\n", strconv.FormatUint(b.Generation, 10))
	buf.WriteString("  \"network\": {\n")
	fmt.Fprintf(&buf, "    \"family\": %q,\n", b.Family)
	fmt.Fprintf(&buf, "    \"chain_id\": \"%s\",\n", hex.EncodeToString(b.ChainID[:]))
	fmt.Fprintf(&buf, "    \"genesis_hash\": \"%s\"\n", hex.EncodeToString(b.GenesisHash[:]))
	buf.WriteString("  },\n")
	fmt.Fprintf(&buf, "  \"expected_account\": %q,\n", b.ExpectedAccount)
	fmt.Fprintf(&buf, "  \"stream\": %q,\n", b.Stream)
	fmt.Fprintf(&buf, "  \"subject_prefix\": %q,\n", b.SubjectPrefix)
	fmt.Fprintf(&buf, "  \"descriptor_digest\": \"%s\"\n", hex.EncodeToString(b.DescriptorDigest[:]))
	buf.WriteString("}")
	return buf.Bytes(), nil
}

// ParseBinding decodes and fully validates one descriptor: UTF-8 without
// BOM, at most 16 KiB, exactly the pinned keys with no duplicates, no
// unknown keys, schema version 1, generation as a canonical decimal
// string, lowercase hex fields, literal stream and prefix tokens, and
// the stated descriptor digest equal to the computed digest.
func ParseBinding(data []byte) (Binding, error) {
	if len(data) == 0 {
		return Binding{}, fmt.Errorf("binding: descriptor is empty")
	}
	if len(data) > MaxDescriptorBytes {
		return Binding{}, fmt.Errorf("binding: descriptor is %d bytes, bound is %d", len(data), MaxDescriptorBytes)
	}
	if !utf8.Valid(data) {
		return Binding{}, fmt.Errorf("binding: descriptor is not valid UTF-8")
	}
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		return Binding{}, fmt.Errorf("binding: descriptor carries a BOM")
	}
	var raw struct {
		SchemaVersion    *int               `json:"schema_version"`
		DestinationID    *string            `json:"destination_id"`
		Generation       *string            `json:"generation"`
		Network          *map[string]string `json:"network"`
		ExpectedAccount  *string            `json:"expected_account"`
		Stream           *string            `json:"stream"`
		SubjectPrefix    *string            `json:"subject_prefix"`
		DescriptorDigest *string            `json:"descriptor_digest"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Binding{}, fmt.Errorf("binding: %w", err)
	}
	if raw.SchemaVersion == nil || raw.DestinationID == nil || raw.Generation == nil ||
		raw.Network == nil || raw.ExpectedAccount == nil || raw.Stream == nil ||
		raw.SubjectPrefix == nil || raw.DescriptorDigest == nil {
		return Binding{}, fmt.Errorf("binding: descriptor holds a missing key")
	}
	b := Binding{
		SchemaVersion:   *raw.SchemaVersion,
		ExpectedAccount: *raw.ExpectedAccount,
		Stream:          *raw.Stream,
		SubjectPrefix:   *raw.SubjectPrefix,
	}
	if b.SchemaVersion != 1 {
		return Binding{}, fmt.Errorf("binding: schema_version %d, want 1", b.SchemaVersion)
	}
	if err := hexInto16(*raw.DestinationID, b.DestinationID[:]); err != nil {
		return Binding{}, fmt.Errorf("binding: destination_id: %w", err)
	}
	gen, err := parseGeneration(*raw.Generation)
	if err != nil {
		return Binding{}, err
	}
	b.Generation = gen
	if len(*raw.Network) != 3 {
		return Binding{}, fmt.Errorf("binding: network holds %d keys, want exactly family, chain_id, genesis_hash", len(*raw.Network))
	}
	family, ok := (*raw.Network)["family"]
	if !ok {
		return Binding{}, fmt.Errorf("binding: network holds a missing family key")
	}
	if family != "EVM" {
		return Binding{}, fmt.Errorf("binding: network family %q, want \"EVM\"", family)
	}
	chainID, ok := (*raw.Network)["chain_id"]
	if !ok {
		return Binding{}, fmt.Errorf("binding: network holds a missing chain_id key")
	}
	genesis, ok := (*raw.Network)["genesis_hash"]
	if !ok {
		return Binding{}, fmt.Errorf("binding: network holds a missing genesis_hash key")
	}
	if err := hexInto32(chainID, b.ChainID[:]); err != nil {
		return Binding{}, fmt.Errorf("binding: network.chain_id: %w", err)
	}
	if err := hexInto32(genesis, b.GenesisHash[:]); err != nil {
		return Binding{}, fmt.Errorf("binding: network.genesis_hash: %w", err)
	}
	if b.ExpectedAccount == "" {
		return Binding{}, fmt.Errorf("binding: expected_account is empty")
	}
	for _, r := range b.ExpectedAccount {
		if unicode.IsControl(r) {
			return Binding{}, fmt.Errorf("binding: expected_account holds a control character")
		}
	}
	if err := validateLiteral(b.Stream, "stream name", false); err != nil {
		return Binding{}, err
	}
	if err := validateLiteral(b.SubjectPrefix, "subject prefix", true); err != nil {
		return Binding{}, err
	}
	if err := hexInto32(*raw.DescriptorDigest, b.DescriptorDigest[:]); err != nil {
		return Binding{}, fmt.Errorf("binding: descriptor_digest: %w", err)
	}
	computed, err := b.DescriptorDigestComputed()
	if err != nil {
		return Binding{}, err
	}
	if b.DescriptorDigest != computed {
		return Binding{}, fmt.Errorf("binding: descriptor digest mismatch: stated %s, computed %s",
			hex.EncodeToString(b.DescriptorDigest[:]), hex.EncodeToString(computed[:]))
	}
	return b, nil
}

// NetworkDigest returns the D77 network digest:
// SHA256("extractor.network.v1" || 0x00 || 0x01 || chain_id || genesis).
func (b Binding) NetworkDigest() [32]byte {
	var out [32]byte
	h := sha256.New()
	h.Write([]byte("extractor.network.v1"))
	h.Write([]byte{0})
	h.Write([]byte{1}) // EVM family byte
	h.Write(b.ChainID[:])
	h.Write(b.GenesisHash[:])
	h.Sum(out[:0])
	return out
}

// DestinationIDHex returns the 32 lowercase hex characters of the
// destination id: the exact value of the D77 metadata key
// extractor.destination_id.
func (b Binding) DestinationIDHex() string {
	return hex.EncodeToString(b.DestinationID[:])
}

// NetworkDigestHex returns the 64 lowercase hex characters of the
// network digest: the exact value of the D77 metadata key
// extractor.network_digest.
func (b Binding) NetworkDigestHex() string {
	d := b.NetworkDigest()
	return hex.EncodeToString(d[:])
}

// DescriptorDigestComputed returns
// SHA256("extractor.binding.v1" || 0x00 || destination_id16 ||
// generation_u64be || 0x01 || chain_id32 || genesis32 ||
// LP(expected_account) || LP(stream) || LP(subject_prefix)) with
// LP(s) = u32be(len(s)) || s.
func (b Binding) DescriptorDigestComputed() ([32]byte, error) {
	if b.Generation == 0 {
		return [32]byte{}, fmt.Errorf("binding: generation is zero")
	}
	var out [32]byte
	h := sha256.New()
	h.Write([]byte("extractor.binding.v1"))
	h.Write([]byte{0})
	h.Write(b.DestinationID[:])
	var gen [8]byte
	binary.BigEndian.PutUint64(gen[:], b.Generation)
	h.Write(gen[:])
	h.Write([]byte{1}) // EVM family byte
	h.Write(b.ChainID[:])
	h.Write(b.GenesisHash[:])
	writeLP(h, b.ExpectedAccount)
	writeLP(h, b.Stream)
	writeLP(h, b.SubjectPrefix)
	h.Sum(out[:0])
	return out, nil
}

// ChainIDUint returns the chain id as a big integer, for profile checks.
func (b Binding) ChainIDUint() *big.Int {
	v := new(big.Int).SetBytes(b.ChainID[:])
	return v
}

// MatchesNetwork checks the chain identity of the binding against a
// network definition: a wrong network in a live or gateway file is a
// CONFIG error before any store is opened.
func (b Binding) MatchesNetwork(n NetworkDefinition) error {
	var wantChain, wantGenesis [32]byte
	if _, err := hex.Decode(wantChain[:], []byte(n.ChainID)); err != nil {
		return fmt.Errorf("network %s chain id: %w", n.Key, err)
	}
	if _, err := hex.Decode(wantGenesis[:], []byte(n.GenesisHash)); err != nil {
		return fmt.Errorf("network %s genesis hash: %w", n.Key, err)
	}
	if b.ChainID != wantChain || b.GenesisHash != wantGenesis {
		return fmt.Errorf("binding network identity does not match profile %s", n.Profile)
	}
	if b.ExpectedAccount != n.Account || b.Stream != n.Stream || b.SubjectPrefix != n.SubjectPrefix {
		return fmt.Errorf("binding route does not match the deployment table of %s", n.Profile)
	}
	return nil
}

// writeLP writes the length-prefixed UTF-8 form of s.
func writeLP(h interface{ Write([]byte) (int, error) }, s string) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(s)))
	h.Write(size[:])
	h.Write([]byte(s))
}

// parseGeneration decodes a canonical positive decimal string.
func parseGeneration(s string) (uint64, error) {
	if s == "0" || s == "" {
		return 0, fmt.Errorf("binding: generation %q is not positive", s)
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, fmt.Errorf("binding: generation %q has a leading zero", s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("binding: generation %q is not a canonical decimal string", s)
		}
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("binding: generation %q overflows u64: %w", s, err)
	}
	return v, nil
}

// validateLiteral rejects whitespace, wildcards and, for dot tokens,
// empty tokens.
func validateLiteral(s, what string, dotTokens bool) error {
	if s == "" {
		return fmt.Errorf("binding: %s is empty", what)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			return fmt.Errorf("binding: %s holds whitespace", what)
		}
		if c == '*' || c == '>' {
			return fmt.Errorf("binding: %s holds a wildcard %q", what, string(c))
		}
	}
	if dotTokens {
		for _, tok := range strings.Split(s, ".") {
			if tok == "" {
				return fmt.Errorf("binding: %s holds an empty token", what)
			}
		}
	}
	return nil
}

// hexStringTo32 decodes exactly 64 lowercase hex characters.
func hexStringTo32(s string) ([32]byte, error) {
	var out [32]byte
	if err := hexInto32(s, out[:]); err != nil {
		return out, err
	}
	return out, nil
}

// hexInto16 decodes exactly 32 lowercase hex characters into 16 bytes.
func hexInto16(s string, out []byte) error {
	if len(s) != 32 {
		return fmt.Errorf("length %d, want 32 lowercase hex characters", len(s))
	}
	return hexStrict(s, out)
}

// hexInto32 decodes exactly 64 lowercase hex characters into 32 bytes.
func hexInto32(s string, out []byte) error {
	if len(s) != 64 {
		return fmt.Errorf("length %d, want 64 lowercase hex characters", len(s))
	}
	return hexStrict(s, out)
}

// hexStrict rejects uppercase characters, 0x prefixes and padding.
func hexStrict(s string, out []byte) error {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("character %q is not a lowercase hex digit", string(c))
		}
	}
	dst := make([]byte, len(out))
	if _, err := hex.Decode(dst, []byte(s)); err != nil {
		return err
	}
	copy(out, dst)
	return nil
}
