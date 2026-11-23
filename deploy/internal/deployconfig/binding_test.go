// Digest verification tests of the immutable DestinationBinding
// descriptor: deterministic generation, byte-exact rendering, strict
// parsing with the stated digest equal to the computed digest
// (CONTROL_WIRE_CONTRACT, "DestinationBinding file"), and the identity
// match against the network table. The shipped descriptors of the
// three networks must verify exactly.
package deployconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ethBinding(t *testing.T) Binding {
	t.Helper()
	network, err := ByProfile("ethereum-mainnet")
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	b, err := GenerateBinding(network)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return b
}

// TestGenerateBindingDeterministic pins the destination identity: the
// same network line always regenerates the same descriptor bytes and
// digests, and different networks never collide.
func TestGenerateBindingDeterministic(t *testing.T) {
	first, err := GenerateBinding(Networks[0])
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	second, err := GenerateBinding(Networks[0])
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	a, _ := first.MarshalJSON()
	b, _ := second.MarshalJSON()
	if !bytes.Equal(a, b) {
		t.Fatalf("generation is not deterministic:\n%s\n%s", a, b)
	}
	seen := map[[32]byte]string{}
	for _, network := range Networks {
		binding, err := GenerateBinding(network)
		if err != nil {
			t.Fatalf("generate %s: %v", network.Key, err)
		}
		if other, ok := seen[binding.DescriptorDigest]; ok {
			t.Fatalf("networks %s and %s share a descriptor digest", other, network.Key)
		}
		seen[binding.DescriptorDigest] = string(network.Key)
		if len(binding.DestinationIDHex()) != 32 || len(binding.NetworkDigestHex()) != 64 {
			t.Fatalf("identity lengths of %s", network.Key)
		}
	}
}

// TestBindingRoundTrip walks generate -> render -> parse and requires
// every field and both digests to survive byte-exactly.
func TestBindingRoundTrip(t *testing.T) {
	for _, network := range Networks {
		t.Run(string(network.Key), func(t *testing.T) {
			binding, err := GenerateBinding(network)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			data, err := binding.MarshalJSON()
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			parsed, err := ParseBinding(data)
			if err != nil {
				t.Fatalf("parse of a generated descriptor: %v", err)
			}
			if parsed != binding {
				t.Fatalf("round trip changed the descriptor:\n%+v\n%+v", parsed, binding)
			}
			// The parse itself verifies the stated digest against the
			// computed digest; recompute once more explicitly.
			computed, err := parsed.DescriptorDigestComputed()
			if err != nil {
				t.Fatalf("compute: %v", err)
			}
			if parsed.DescriptorDigest != computed {
				t.Fatalf("stated digest %x differs from computed %x", parsed.DescriptorDigest, computed)
			}
			if err := parsed.MatchesNetwork(network); err != nil {
				t.Fatalf("generated descriptor does not match its own network: %v", err)
			}
		})
	}
}

// TestShippedBindingsVerify reads the committed descriptors of the
// three networks: each parses with a verified digest, matches its
// network line, and is byte-identical to what generation produces.
func TestShippedBindingsVerify(t *testing.T) {
	for _, network := range Networks {
		t.Run(string(network.Key), func(t *testing.T) {
			path := filepath.Join("..", "..", "config", string(network.Key), "binding.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			binding, err := ParseBinding(data)
			if err != nil {
				t.Fatalf("shipped descriptor failed verification: %v", err)
			}
			if err := binding.MatchesNetwork(network); err != nil {
				t.Fatalf("shipped descriptor does not match the network table: %v", err)
			}
			generated, err := GenerateBinding(network)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			rendered, err := generated.MarshalJSON()
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if !bytes.Equal(bytes.TrimSpace(data), rendered) {
				t.Fatalf("shipped descriptor differs from the deterministic generation:\nfile:      %s\ngenerated: %s", data, rendered)
			}
		})
	}
}

// TestParseBindingRejectsTampering: any change of a covered field
// without a new digest, and any malformed identity, is refused.
func TestParseBindingRejectsTampering(t *testing.T) {
	binding := ethBinding(t)
	valid, err := binding.MarshalJSON()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	cases := map[string]func(string) string{
		"stream changed without new digest": func(s string) string {
			return strings.Replace(s, `"EXTRACTOR_ETH_V3"`, `"EXTRACTOR_ETH_V4"`, 1)
		},
		"account changed without new digest": func(s string) string {
			return strings.Replace(s, `"IDX_ETH"`, `"IDX_OTHER"`, 1)
		},
		"genesis changed without new digest": func(s string) string {
			return strings.Replace(s,
				`"d4e56740f876aef8c010b86a40d5f56745a118d0906a34e69aec8c0db1cb8fa3"`,
				strings.Repeat("ab", 32), 1)
		},
		"corrupt stated digest": func(s string) string {
			return strings.Replace(s, `"descriptor_digest": "b037`, `"descriptor_digest": "ffff`, 1)
		},
		"missing key": func(s string) string {
			return strings.Replace(s, "  \"expected_account\": \"IDX_ETH\",\n", "", 1)
		},
		"unknown key": func(s string) string {
			return strings.Replace(s, "{\n", "{\n  \"extra\": 1,\n", 1)
		},
		"uppercase destination id": func(s string) string {
			return strings.Replace(s, `"f13547dc9857ec1b9b0e0da8fd558261"`, strings.ToUpper("f13547dc9857ec1b9b0e0da8fd558261"), 1)
		},
		"short destination id": func(s string) string {
			return strings.Replace(s, `"f13547dc9857ec1b9b0e0da8fd558261"`, `"f13547"`, 1)
		},
		"0x-prefixed chain id": func(s string) string {
			return strings.Replace(s,
				`"chain_id": "0000000000000000000000000000000000000000000000000000000000000001"`,
				`"chain_id": "0x0000000000000000000000000000000000000000000000000000000000000001"`, 1)
		},
		"generation with leading zero": func(s string) string {
			return strings.Replace(s, `"generation": "1"`, `"generation": "01"`, 1)
		},
		"generation zero": func(s string) string {
			return strings.Replace(s, `"generation": "1"`, `"generation": "0"`, 1)
		},
		"generation as number": func(s string) string {
			return strings.Replace(s, `"generation": "1"`, `"generation": 1`, 1)
		},
		"wrong family": func(s string) string {
			return strings.Replace(s, `"family": "EVM"`, `"family": "SOL"`, 1)
		},
		"network key count": func(s string) string {
			return strings.Replace(s, "\"genesis_hash\": \"d4e56740f876aef8c010b86a40d5f56745a118d0906a34e69aec8c0db1cb8fa3\"\n",
				"\"genesis_hash\": \"d4e56740f876aef8c010b86a40d5f56745a118d0906a34e69aec8c0db1cb8fa3\",\n    \"extra\": \"x\"\n", 1)
		},
		"empty account": func(s string) string {
			return strings.Replace(s, `"IDX_ETH"`, `""`, 1)
		},
		"wildcard stream": func(s string) string {
			return strings.Replace(s, `"stream": "EXTRACTOR_ETH_V3"`, `"stream": "EXTRACTOR_*"`, 1)
		},
		"empty subject token": func(s string) string {
			return strings.Replace(s, `"subject_prefix": "evm.v3.eth"`, `"subject_prefix": "evm..eth"`, 1)
		},
		"bom": func(s string) string {
			return "\xef\xbb\xbf" + s
		},
		"invalid utf-8": func(s string) string {
			return strings.Replace(s, "IDX_ETH", "IDX_\xff\xfe", 1)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			mutated := mutate(string(valid))
			if mutated == string(valid) && name != "bom" {
				t.Fatalf("mutation %s changed nothing", name)
			}
			if _, err := ParseBinding([]byte(mutated)); err == nil {
				t.Fatalf("tampered descriptor accepted")
			}
		})
	}
}

// TestParseBindingEmptyAndOversize bounds the descriptor size.
func TestParseBindingEmptyAndOversize(t *testing.T) {
	if _, err := ParseBinding(nil); err == nil {
		t.Fatalf("empty descriptor accepted")
	}
	big := bytes.Repeat([]byte{'a'}, MaxDescriptorBytes+1)
	if _, err := ParseBinding(big); err == nil {
		t.Fatalf("oversize descriptor accepted")
	}
}

// TestMatchesNetworkRejectsCrossNetwork: an eth binding never matches
// another network line.
func TestMatchesNetworkRejectsCrossNetwork(t *testing.T) {
	binding := ethBinding(t)
	for _, network := range Networks[1:] {
		if err := binding.MatchesNetwork(network); err == nil {
			t.Fatalf("eth binding matched network %s", network.Key)
		}
	}
}

// TestNetworkDigestFormula pins the D77 network digest derivation with
// an independently computed vector.
func TestNetworkDigestFormula(t *testing.T) {
	binding := ethBinding(t)
	// SHA256("extractor.network.v1" || 0x00 || 0x01 || chain_id || genesis)
	h := sha256.New()
	h.Write([]byte("extractor.network.v1"))
	h.Write([]byte{0})
	h.Write([]byte{1})
	h.Write([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1})
	h.Write(binding.GenesisHash[:])
	want := h.Sum(nil)
	if got := binding.NetworkDigest(); hex.EncodeToString(got[:]) != hex.EncodeToString(want) {
		t.Fatalf("network digest %x, want %x", got, want)
	}
}
