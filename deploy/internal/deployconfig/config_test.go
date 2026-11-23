// Parse and validation tests of the strict YAML schemas: every unknown
// or duplicate key is rejected at every level, and every field rule of
// LIVE_CONFIG_SPEC ("Общая схема YAML v1") holds for the live collector
// file and the gateway file. The shipped configuration files of the
// three networks must parse cleanly.
package deployconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile writes one fixture and returns its path.
func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// validLiveYAML mirrors deploy/config/eth/live.yml.
const validLiveYAML = `schema_version: 1
network:
  profile: ethereum-mainnet
native:
  kind: geth
destination:
  binding_file: /config/binding.json
delivery:
  gateway_endpoint: https://eth-gateway:50051
  tls:
    mode: mutual
    ca_file: /run/secrets/ca.pem
    cert_file: /run/secrets/adapter.crt
    key_file: /run/secrets/adapter.key
storage:
  live_dir: /data/wal
runtime:
  log_level: info
`

// validGatewayYAML mirrors deploy/config/eth/gateway.yml.
const validGatewayYAML = `schema_version: 1
network:
  profile: ethereum-mainnet
destination:
  binding_file: /config/binding.json
gateway:
  listen: https://0.0.0.0:50051
  tls:
    mode: mutual
    ca_file: /run/secrets/ca.pem
    cert_file: /run/secrets/gateway.crt
    key_file: /run/secrets/gateway.key
  nats:
    server: tls://nats:4222
    tls:
      mode: server
      ca_file: /run/secrets/nats-ca.pem
    credentials_file: /run/secrets/nats.creds
    stream: EXTRACTOR_ETH_V3
    subject_prefix: evm.v3.eth
storage:
  gateway_dir: /data/journal
transport:
  max_frame_bytes: 2097152
runtime:
  log_level: info
`

// replace swaps the first occurrence of old in the fixture.
func replace(t *testing.T, fixture, old, new string) string {
	t.Helper()
	if !strings.Contains(fixture, old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return strings.Replace(fixture, old, new, 1)
}

func TestLiveConfigValidParse(t *testing.T) {
	cfg, err := LoadLiveConfig(writeFile(t, validLiveYAML))
	if err != nil {
		t.Fatalf("valid live config rejected: %v", err)
	}
	if cfg.Network.Profile != "ethereum-mainnet" || cfg.Native.Kind != "geth" {
		t.Fatalf("fields: %+v", cfg)
	}
	if cfg.Delivery.GatewayEndpoint != "https://eth-gateway:50051" {
		t.Fatalf("gateway endpoint: %q", cfg.Delivery.GatewayEndpoint)
	}
	if cfg.Delivery.TLS.Mode != TLSMutual || cfg.Delivery.TLS.CAFile == "" {
		t.Fatalf("tls: %+v", cfg.Delivery.TLS)
	}
	if cfg.Storage.LiveDir != "/data/wal" || cfg.Runtime.LogLevel != "info" {
		t.Fatalf("storage/runtime: %+v", cfg)
	}
}

func TestLiveConfigRejections(t *testing.T) {
	cases := map[string]func(string) string{
		"unknown top-level key": func(s string) string {
			return s + "\nextra: 1\n"
		},
		"unknown nested key": func(s string) string {
			return replace(t, s, "  profile: ethereum-mainnet", "  profile: ethereum-mainnet\n  zone: x")
		},
		"duplicate top-level key": func(s string) string {
			return s + "\nruntime:\n  log_level: debug\n"
		},
		"duplicate nested key": func(s string) string {
			return replace(t, s, "  tls:\n", "  tls:\n    mode: mutual\n")
		},
		"schema version": func(s string) string {
			return replace(t, s, "schema_version: 1", "schema_version: 2")
		},
		"unknown profile": func(s string) string {
			return replace(t, s, "profile: ethereum-mainnet", "profile: near-mainnet")
		},
		"native kind mismatch": func(s string) string {
			return replace(t, s, "kind: geth", "kind: bor")
		},
		"missing binding file": func(s string) string {
			return replace(t, s, "binding_file: /config/binding.json", "binding_file: \"\"")
		},
		"plaintext endpoint": func(s string) string {
			return replace(t, s, "https://eth-gateway:50051", "http://eth-gateway:50051")
		},
		"endpoint path": func(s string) string {
			return replace(t, s, "https://eth-gateway:50051", "https://eth-gateway:50051/path")
		},
		"unix endpoint with tls": func(s string) string {
			return replace(t, s, "gateway_endpoint: https://eth-gateway:50051", "gateway_endpoint: unix:///run/gateway.sock")
		},
		"relative unix endpoint": func(s string) string {
			return replace(t, s, "gateway_endpoint: https://eth-gateway:50051", "gateway_endpoint: unix://run/gateway.sock")
		},
		"server-only tls mode": func(s string) string {
			return replace(t, s, "mode: mutual\n    ca_file: /run/secrets/ca.pem\n    cert_file: /run/secrets/adapter.crt\n    key_file: /run/secrets/adapter.key",
				"mode: server\n    ca_file: /run/secrets/ca.pem")
		},
		"disabled tls with cert": func(s string) string {
			return replace(t, s, "mode: mutual\n    ca_file: /run/secrets/ca.pem\n    cert_file: /run/secrets/adapter.crt\n    key_file: /run/secrets/adapter.key",
				"mode: disabled\n    cert_file: /run/secrets/adapter.crt")
		},
		"mutual tls missing key": func(s string) string {
			return replace(t, s, "\n    key_file: /run/secrets/adapter.key", "")
		},
		"unknown tls mode": func(s string) string {
			return replace(t, s, "mode: mutual", "mode: strict")
		},
		"missing live dir": func(s string) string {
			return replace(t, s, "live_dir: /data/wal", "live_dir: \"\"")
		},
		"bad log level": func(s string) string {
			return replace(t, s, "log_level: info", "log_level: chatty")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadLiveConfig(writeFile(t, mutate(validLiveYAML))); err == nil {
				t.Fatalf("invalid live config accepted")
			}
		})
	}
}

// TestLiveConfigDisabledTLSUnix checks the one allowed plaintext form:
// TLS disabled over an absolute unix socket.
func TestLiveConfigDisabledTLSUnix(t *testing.T) {
	fixture := replace(t, validLiveYAML,
		"gateway_endpoint: https://eth-gateway:50051\n  tls:\n    mode: mutual\n    ca_file: /run/secrets/ca.pem\n    cert_file: /run/secrets/adapter.crt\n    key_file: /run/secrets/adapter.key",
		"gateway_endpoint: unix:///run/gateway.sock\n  tls:\n    mode: disabled")
	cfg, err := LoadLiveConfig(writeFile(t, fixture))
	if err != nil {
		t.Fatalf("unix endpoint with disabled tls rejected: %v", err)
	}
	if cfg.Delivery.TLS.Mode != TLSDisabled {
		t.Fatalf("tls mode: %q", cfg.Delivery.TLS.Mode)
	}
}

func TestGatewayConfigValidParse(t *testing.T) {
	cfg, err := LoadGatewayConfig(writeFile(t, validGatewayYAML))
	if err != nil {
		t.Fatalf("valid gateway config rejected: %v", err)
	}
	if cfg.Gateway.Listen != "https://0.0.0.0:50051" || cfg.Gateway.NATS.Server != "tls://nats:4222" {
		t.Fatalf("fields: %+v", cfg.Gateway)
	}
	if cfg.Transport.MaxFrameBytes != 2097152 {
		t.Fatalf("max frame bytes: %d", cfg.Transport.MaxFrameBytes)
	}
	if cfg.Gateway.NATS.Stream != "EXTRACTOR_ETH_V3" || cfg.Gateway.NATS.SubjectPrefix != "evm.v3.eth" {
		t.Fatalf("nats: %+v", cfg.Gateway.NATS)
	}
}

// TestGatewayConfigDefaultFrameBytes checks the default budget of an
// omitted transport section.
func TestGatewayConfigDefaultFrameBytes(t *testing.T) {
	fixture := replace(t, validGatewayYAML, "transport:\n  max_frame_bytes: 2097152\n", "")
	cfg, err := LoadGatewayConfig(writeFile(t, fixture))
	if err != nil {
		t.Fatalf("gateway config without transport rejected: %v", err)
	}
	if cfg.Transport.MaxFrameBytes != MaxFrameBytesDefault {
		t.Fatalf("default max frame bytes: %d", cfg.Transport.MaxFrameBytes)
	}
}

func TestGatewayConfigRejections(t *testing.T) {
	cases := map[string]func(string) string{
		"unknown nested key": func(s string) string {
			return replace(t, s, "    stream: EXTRACTOR_ETH_V3", "    stream: EXTRACTOR_ETH_V3\n    cluster: x")
		},
		"duplicate nested key": func(s string) string {
			return replace(t, s, "  nats:\n", "  nats:\n    server: tls://nats:4222\n")
		},
		"http listen": func(s string) string {
			return replace(t, s, "listen: https://0.0.0.0:50051", "listen: http://0.0.0.0:50051")
		},
		"tls mode disagrees with scheme": func(s string) string {
			return replace(t, s,
				"listen: https://0.0.0.0:50051\n  tls:\n    mode: mutual\n    ca_file: /run/secrets/ca.pem\n    cert_file: /run/secrets/gateway.crt\n    key_file: /run/secrets/gateway.key",
				"listen: https://0.0.0.0:50051\n  tls:\n    mode: disabled")
		},
		"plaintext nats with tls": func(s string) string {
			return replace(t, s, "server: tls://nats:4222", "server: nats://nats:4222")
		},
		"tls nats with disabled tls": func(s string) string {
			return replace(t, s,
				"server: tls://nats:4222\n    tls:\n      mode: server\n      ca_file: /run/secrets/nats-ca.pem",
				"server: tls://nats:4222\n    tls:\n      mode: disabled")
		},
		"wildcard stream": func(s string) string {
			return replace(t, s, "stream: EXTRACTOR_ETH_V3", "stream: EXTRACTOR_*>")
		},
		"wildcard subject prefix": func(s string) string {
			return replace(t, s, "subject_prefix: evm.v3.eth", "subject_prefix: evm.*.eth")
		},
		"empty subject token": func(s string) string {
			return replace(t, s, "subject_prefix: evm.v3.eth", "subject_prefix: evm..eth")
		},
		"oversize frame budget": func(s string) string {
			return replace(t, s, "max_frame_bytes: 2097152", "max_frame_bytes: 99999999999")
		},
		"missing credentials file": func(s string) string {
			return replace(t, s, "credentials_file: /run/secrets/nats.creds", "credentials_file: \"\"")
		},
		"missing storage dir": func(s string) string {
			return replace(t, s, "gateway_dir: /data/journal", "gateway_dir: \"\"")
		},
		"bad log level": func(s string) string {
			return replace(t, s, "log_level: info", "log_level: verbose")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadGatewayConfig(writeFile(t, mutate(validGatewayYAML))); err == nil {
				t.Fatalf("invalid gateway config accepted")
			}
		})
	}
}

func TestDecodeStrictRejections(t *testing.T) {
	type shape struct {
		A struct {
			B string `yaml:"b"`
		} `yaml:"a"`
	}
	cases := map[string]string{
		"document root is a sequence": "- a\n- b\n",
		"document root is a scalar":   "a\n",
		"empty document":              "\n",
		"duplicate key deep in tree":  "a:\n  b: x\n  b: y\n",
		"duplicate key in sequence":   "a:\n  b: x\nlist:\n  - c: 1\n    c: 2\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			var out shape
			if err := DecodeStrict([]byte(doc), &out); err == nil {
				t.Fatalf("invalid document accepted")
			}
		})
	}
}

// TestShippedConfigsParse reads the three shipped network files of the
// deployment: they must parse and validate without modification.
func TestShippedConfigsParse(t *testing.T) {
	for _, net := range []string{"eth", "bsc", "polygon"} {
		t.Run(net, func(t *testing.T) {
			livePath := filepath.Join("..", "..", "config", net, "live.yml")
			if _, err := LoadLiveConfig(livePath); err != nil {
				t.Fatalf("shipped live config rejected: %v", err)
			}
			gatewayPath := filepath.Join("..", "..", "config", net, "gateway.yml")
			if _, err := LoadGatewayConfig(gatewayPath); err != nil {
				t.Fatalf("shipped gateway config rejected: %v", err)
			}
		})
	}
}

func TestTLSModeRules(t *testing.T) {
	if err := (TLSConfig{Mode: TLSDisabled}).Validate(); err != nil {
		t.Fatalf("disabled: %v", err)
	}
	if err := (TLSConfig{Mode: TLSDisabled, CAFile: "ca.pem"}).Validate(); err == nil {
		t.Fatalf("disabled with ca_file accepted")
	}
	if err := (TLSConfig{Mode: TLSServer, CAFile: "ca.pem"}).Validate(); err != nil {
		t.Fatalf("server: %v", err)
	}
	if err := (TLSConfig{Mode: TLSServer, CertFile: "c.pem"}).Validate(); err == nil {
		t.Fatalf("server with cert_file accepted")
	}
	if err := (TLSConfig{Mode: TLSMutual, CAFile: "ca.pem", CertFile: "c.pem", KeyFile: "k.pem"}).Validate(); err != nil {
		t.Fatalf("mutual: %v", err)
	}
	if err := (TLSConfig{Mode: TLSMutual, CAFile: "ca.pem"}).Validate(); err == nil {
		t.Fatalf("mutual without cert/key accepted")
	}
	if err := (TLSConfig{Mode: "strict"}).Validate(); err == nil {
		t.Fatalf("unknown mode accepted")
	}
}
