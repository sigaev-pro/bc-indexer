package config

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// Shared fixtures of the config tests: a written YAML file, a valid
// binding descriptor built through the destination package itself, and
// TLS material. Everything lives inside the test's temporary directory.

// validLiveYAML is a complete, valid live-role document. Relative paths
// exercise the D78 resolution rule.
const validLiveYAML = `schema_version: 1
network:
  profile: ethereum-mainnet
destination:
  binding_file: binding.json
native:
  kind: geth
delivery:
  gateway_endpoint: https://gw.internal:50051
  tls:
    mode: mutual
    ca_file: tls/ca.pem
    cert_file: tls/cert.pem
    key_file: tls/key.pem
storage:
  live_dir: ../wal/eth
runtime:
  log_level: warn
`

// validGatewayYAML is a complete, valid gateway-role document whose
// stream and subject_prefix equal the standard test binding.
const validGatewayYAML = `schema_version: 1
network:
  profile: ethereum-mainnet
destination:
  binding_file: binding.json
gateway:
  listen: https://gw.internal:50051
  tls:
    mode: mutual
    ca_file: tls/ca.pem
    cert_file: tls/cert.pem
    key_file: tls/key.pem
  nats:
    server: nats://nats.internal:4222
    tls:
      mode: disabled
    stream: EXTRACTOR_ETH_V3
    subject_prefix: evm.v3.eth
storage:
  gateway_dir: ../journal/eth
`

// validPolygonYAML replaces the network sections of a live file with a
// complete polygon finality section.
const validPolygonYAML = `schema_version: 1
network:
  profile: polygon-pos-amoy
destination:
  binding_file: binding.json
native:
  kind: bor
delivery:
  gateway_endpoint: unix:///run/extractor/gw.sock
  tls:
    mode: disabled
storage:
  live_dir: wal/poly
finality:
  polygon:
    l1_endpoint: http://l1.internal:8545
    sources_file: /etc/extractor/polygon-sources.json
    l1_tls:
      mode: disabled
    expected_root_chain_address: 8f5b8c8e13e6f6d4a1f0e2d4c3b2a1908f7e6d5c
    expected_child_block_interval: 256
`

// writeYAML writes one YAML body into dir and returns its path.
func writeYAML(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "network.yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return path
}

// writeTLSMaterial writes the standard CA, certificate and key files.
func writeTLSMaterial(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"ca.pem", "cert.pem", "key.pem"} {
		p := filepath.Join(dir, "tls", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir tls: %v", err)
		}
		if err := os.WriteFile(p, []byte("test material "+name+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// testBinding builds the standard test binding through the exported
// destination API.
func testBinding(t *testing.T) destination.Binding {
	t.Helper()
	var id proto.ID16
	for i := range id {
		id[i] = byte(0x0a)
	}
	var chain, genesis [32]byte
	for i := range chain {
		chain[i] = byte(0x11)
		genesis[i] = byte(0x22)
	}
	b := destination.Binding{
		DestinationID:   id,
		Generation:      1,
		Network:         proto.NetworkIdentity{Family: proto.NetworkFamilyEVM, ChainID: chain, GenesisHash: genesis},
		ExpectedAccount: "IDX_ETH",
		Stream:          "EXTRACTOR_ETH_V3",
		SubjectPrefix:   "evm.v3.eth",
	}
	digest, err := b.DescriptorDigestComputed()
	if err != nil {
		t.Fatalf("DescriptorDigestComputed: %v", err)
	}
	b.DescriptorDigest = digest
	return b
}

// bindingJSON renders one binding in the strict descriptor JSON form.
func bindingJSON(b destination.Binding) []byte {
	body := map[string]any{
		"schema_version":    1,
		"destination_id":    hex.EncodeToString(b.DestinationID[:]),
		"generation":        strconv.FormatUint(b.Generation, 10),
		"network":           map[string]any{"family": "EVM", "chain_id": hex.EncodeToString(b.Network.ChainID[:]), "genesis_hash": hex.EncodeToString(b.Network.GenesisHash[:])},
		"expected_account":  b.ExpectedAccount,
		"stream":            b.Stream,
		"subject_prefix":    b.SubjectPrefix,
		"descriptor_digest": hex.EncodeToString(b.DescriptorDigest[:]),
	}
	out, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return out
}

// writeBindingFile writes the descriptor of one binding into dir and
// returns its path.
func writeBindingFile(t *testing.T, dir string, b destination.Binding) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "binding.json"), bindingJSON(b), 0o644); err != nil {
		t.Fatalf("write binding: %v", err)
	}
	return filepath.Join(dir, "binding.json")
}

// setupLiveDir prepares a config directory holding the valid live YAML,
// the binding of b and the TLS material; it returns the YAML path.
func setupLiveDir(t *testing.T, body string, b destination.Binding) (dir, yamlPath string) {
	t.Helper()
	dir = t.TempDir()
	writeBindingFile(t, dir, b)
	writeTLSMaterial(t, dir)
	return dir, writeYAML(t, dir, body)
}

// setupGatewayDir prepares a config directory holding the valid gateway
// YAML, the binding of b and the TLS material.
func setupGatewayDir(t *testing.T, body string, b destination.Binding) (dir, yamlPath string) {
	t.Helper()
	dir = t.TempDir()
	writeBindingFile(t, dir, b)
	writeTLSMaterial(t, dir)
	return dir, writeYAML(t, dir, body)
}

// replaceLine swaps every occurrence of one exact line in a YAML body.
func replaceLine(body, oldLine, newLine string) string {
	out := ""
	for _, line := range splitLines(body) {
		if line == oldLine {
			out += newLine + "\n"
		} else {
			out += line + "\n"
		}
	}
	return out
}

// setField replaces the value of every "<name>: <value>" line, whatever
// its indentation.
func setField(body, name, value string) string {
	out := ""
	prefix := name + ":"
	for _, line := range splitLines(body) {
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, prefix) {
			indent := line[:len(line)-len(trimmed)]
			out += indent + prefix + " " + value + "\n"
		} else {
			out += line + "\n"
		}
	}
	return out
}

// splitLines splits a body without keeping a trailing empty line.
func splitLines(body string) []string {
	lines := strings.Split(body, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
