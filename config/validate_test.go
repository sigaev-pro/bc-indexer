package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Role check tests (E06-07, AC-OPS-01-01/04/05): required fields per
// role, D78 path resolution against the YAML directory, the closed
// profile registry, TLS shapes and pairings, endpoint forms, the
// polygon-only finality rules and the binding descriptor check.

func TestCheckLiveAcceptsValidFile(t *testing.T) {
	dir, path := setupLiveDir(t, validLiveYAML, testBinding(t))
	c, err := CheckLive(path)
	if err != nil {
		t.Fatalf("CheckLive: %v", err)
	}
	if c.Profile.Name != "ethereum-mainnet" || c.NativeKind != NativeGeth {
		t.Fatalf("profile = %+v kind = %s", c.Profile, c.NativeKind)
	}
	wantDir := filepath.Clean(filepath.Join(dir, "..", "wal", "eth"))
	if c.LiveDir != wantDir {
		t.Fatalf("live_dir = %s, want %s", c.LiveDir, wantDir)
	}
	if !filepath.IsAbs(c.BindingPath) || filepath.Base(c.BindingPath) != "binding.json" {
		t.Fatalf("binding path = %s", c.BindingPath)
	}
	if c.Binding.Stream != "EXTRACTOR_ETH_V3" {
		t.Fatalf("binding stream = %s", c.Binding.Stream)
	}
	if c.TLS.Mode != TLSMutual || !filepath.IsAbs(c.TLS.CAFile) {
		t.Fatalf("tls = %+v", c.TLS)
	}
	if c.Endpoint.Scheme != "https" || c.Endpoint.Address() != "gw.internal:50051" {
		t.Fatalf("endpoint = %+v", c.Endpoint)
	}
	if c.LogLevel != LevelWarn {
		t.Fatalf("log level = %s", c.LogLevel)
	}
	if c.Polygon != nil {
		t.Fatalf("polygon section on an ethereum profile")
	}
}

func TestCheckLiveResolvesRelativeToYAMLNotCWD(t *testing.T) {
	dir, path := setupLiveDir(t, validLiveYAML, testBinding(t))
	other := t.TempDir()
	t.Chdir(other)
	c, err := CheckLive(path)
	if err != nil {
		t.Fatalf("CheckLive from another cwd: %v", err)
	}
	wantDir := filepath.Clean(filepath.Join(dir, "..", "wal", "eth"))
	if c.LiveDir != wantDir {
		t.Fatalf("live_dir = %s, want %s", c.LiveDir, wantDir)
	}
}

func TestCheckCreatesNothing(t *testing.T) {
	_, path := setupLiveDir(t, validLiveYAML, testBinding(t))
	if _, err := CheckLive(path); err != nil {
		t.Fatalf("CheckLive: %v", err)
	}
	dir := filepath.Dir(path)
	wal := filepath.Clean(filepath.Join(dir, "..", "wal"))
	if _, err := os.Stat(wal); !os.IsNotExist(err) {
		t.Fatalf("check created %s", wal)
	}
	// The referenced files exist and were only read.
	if _, err := os.Stat(filepath.Join(dir, "binding.json")); err != nil {
		t.Fatalf("binding file disappeared: %v", err)
	}
}

func TestCheckLiveRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"schema-version", replaceLine(validLiveYAML, "schema_version: 1", "schema_version: 2"), "schema_version"},
		{"profile", setField(validLiveYAML, "profile", `""`), "network.profile"},
		{"native-section", removeSection(validLiveYAML, "native:"), "native"},
		{"delivery-section", removeSection(validLiveYAML, "delivery:"), "delivery"},
		{"live-dir", removeLine(validLiveYAML, "  live_dir: ../wal/eth"), "storage.live_dir"},
		{"binding-file", setField(validLiveYAML, "binding_file", `""`), "destination.binding_file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, path := setupLiveDir(t, tc.body, testBinding(t))
			_, err := CheckLive(path)
			if err == nil {
				t.Fatalf("missing %s accepted", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err.Error(), tc.want)
			}
			if Exit(err) != ExitConfig {
				t.Fatalf("exit = %d, want 2", Exit(err))
			}
		})
	}
}

func TestCheckGatewayAcceptsValidFileAndDefaults(t *testing.T) {
	// No transport section: the D52 default must apply.
	_, path := setupGatewayDir(t, validGatewayYAML, testBinding(t))
	c, err := CheckGateway(path)
	if err != nil {
		t.Fatalf("CheckGateway: %v", err)
	}
	if c.MaxFrameBytes != DefaultMaxFrameBytes {
		t.Fatalf("max_frame_bytes = %d, want default %d", c.MaxFrameBytes, DefaultMaxFrameBytes)
	}
	if c.LogLevel != LevelInfo {
		t.Fatalf("log level = %s, want the info default", c.LogLevel)
	}
	if c.NATS.Scheme != "nats" || c.NATS.Address() != "nats.internal:4222" {
		t.Fatalf("nats endpoint = %+v", c.NATS)
	}
	if c.NATSTLS.Mode != TLSDisabled {
		t.Fatalf("nats tls = %+v", c.NATSTLS)
	}
	if c.Stream != "EXTRACTOR_ETH_V3" || c.SubjectPrefix != "evm.v3.eth" {
		t.Fatalf("stream/prefix = %s/%s", c.Stream, c.SubjectPrefix)
	}
	if c.CredentialsPath != "" {
		t.Fatalf("credentials = %s, want none", c.CredentialsPath)
	}
	// An explicit limit is honored.
	body := validGatewayYAML + "transport:\n  max_frame_bytes: 1048576\n"
	_, path = setupGatewayDir(t, body, testBinding(t))
	c, err = CheckGateway(path)
	if err != nil {
		t.Fatalf("CheckGateway explicit limit: %v", err)
	}
	if c.MaxFrameBytes != 1048576 {
		t.Fatalf("max_frame_bytes = %d", c.MaxFrameBytes)
	}
}

func TestCheckGatewayRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"gateway-section", removeSection(validGatewayYAML, "gateway:"), "gateway"},
		{"listen", removeLine(validGatewayYAML, "  listen: https://gw.internal:50051"), "gateway.listen"},
		{"gateway-dir", removeSection(validGatewayYAML, "storage:"), "storage.gateway_dir"},
		{"stream", setField(validGatewayYAML, "stream", `""`), "gateway.nats.stream"},
		{"subject-prefix", setField(validGatewayYAML, "subject_prefix", `""`), "gateway.nats.subject_prefix"},
		{"nats-server", removeLine(validGatewayYAML, "    server: nats://nats.internal:4222"), "gateway.nats.server"},
		{"frame-limit-zero", validGatewayYAML + "transport:\n  max_frame_bytes: 0\n", "transport.max_frame_bytes"},
		{"frame-limit-negative", validGatewayYAML + "transport:\n  max_frame_bytes: -5\n", "transport"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, path := setupGatewayDir(t, tc.body, testBinding(t))
			_, err := CheckGateway(path)
			if err == nil {
				t.Fatalf("missing %s accepted", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err.Error(), tc.want)
			}
		})
	}
}

func TestCheckGatewayStreamAndPrefixMatchBinding(t *testing.T) {
	b := testBinding(t)
	body := setField(validGatewayYAML, "stream", "OTHER_STREAM")
	_, path := setupGatewayDir(t, body, b)
	_, err := CheckGateway(path)
	if err == nil || !strings.Contains(err.Error(), "gateway.nats.stream") {
		t.Fatalf("stream mismatch accepted: %v", err)
	}
	body = setField(validGatewayYAML, "subject_prefix", "evm.v3.other")
	_, path = setupGatewayDir(t, body, b)
	_, err = CheckGateway(path)
	if err == nil || !strings.Contains(err.Error(), "gateway.nats.subject_prefix") {
		t.Fatalf("prefix mismatch accepted: %v", err)
	}
}

func TestProfileRegistryIsClosed(t *testing.T) {
	want := []string{
		"ethereum-mainnet", "bsc-mainnet", "polygon-pos-mainnet",
		"ethereum-sepolia", "ethereum-hoodi", "bsc-testnet", "polygon-pos-amoy",
	}
	got := ProfileNames()
	if len(got) != len(want) {
		t.Fatalf("registry has %d profiles, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("profile %d = %s, want %s", i, got[i], want[i])
		}
	}
	for _, rejected := range []string{"base-mainnet", "arbitrum-one", "ethereum-classic", "polygon-pos-amoy ", ""} {
		if _, ok := LookupProfile(rejected); ok {
			t.Fatalf("profile %q resolved", rejected)
		}
	}
	// A public testnet is its own entry, never a mainnet alias.
	amoy, _ := LookupProfile("polygon-pos-amoy")
	main, _ := LookupProfile("polygon-pos-mainnet")
	if !amoy.IsTestnet || main.IsTestnet {
		t.Fatalf("testnet flags wrong: amoy=%v main=%v", amoy.IsTestnet, main.IsTestnet)
	}
}

func TestCheckLiveRejectsProfileKindMismatch(t *testing.T) {
	body := setField(validLiveYAML, "profile", "bsc-mainnet")
	_, path := setupLiveDir(t, body, testBinding(t))
	_, err := CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "native.kind") {
		t.Fatalf("kind mismatch accepted: %v", err)
	}
	body = setField(validLiveYAML, "kind", "bor")
	_, path = setupLiveDir(t, body, testBinding(t))
	_, err = CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "native.kind") {
		t.Fatalf("unknown kind accepted: %v", err)
	}
}

// liveTLSBody rebuilds the live YAML with the delivery.tls block
// replaced by the given indented lines.
func liveTLSBody(tls string) string {
	body := removeLine(validLiveYAML, "    mode: mutual")
	body = removeLine(body, "    ca_file: tls/ca.pem")
	body = removeLine(body, "    cert_file: tls/cert.pem")
	body = removeLine(body, "    key_file: tls/key.pem")
	return insertAfter(body, "  tls:", tls)
}

func TestCheckTLSShapes(t *testing.T) {
	cases := []struct {
		name string
		tls  string
		want string
	}{
		{"disabled-with-files", "    mode: disabled\n    ca_file: tls/ca.pem", "delivery.tls"},
		{"server-no-ca", "    mode: server", "delivery.tls.mode"},
		{"mutual-no-key", "    mode: mutual\n    ca_file: tls/ca.pem\n    cert_file: tls/cert.pem", "delivery.tls.key_file"},
		{"unknown-mode", "    mode: strict", "delivery.tls.mode"},
		{"missing-mode", "    ca_file: tls/ca.pem", "delivery.tls.mode"},
		{"server-with-cert", "    mode: server\n    ca_file: tls/ca.pem\n    cert_file: tls/cert.pem", "delivery.tls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, path := setupLiveDir(t, liveTLSBody(tc.tls), testBinding(t))
			_, err := CheckLive(path)
			if err == nil {
				t.Fatalf("tls shape %s accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err.Error(), tc.want)
			}
		})
	}
}

func TestCheckServerTLSForbiddenOnDeliveryLink(t *testing.T) {
	body := replaceLine(validLiveYAML, "    mode: mutual", "    mode: server")
	body = replaceLine(body, "    cert_file: tls/cert.pem", "")
	body = replaceLine(body, "    key_file: tls/key.pem", "")
	_, path := setupLiveDir(t, body, testBinding(t))
	_, err := CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "server-only TLS is not allowed") {
		t.Fatalf("server-only delivery TLS accepted: %v", err)
	}
}

func TestCheckTLSSchemePairing(t *testing.T) {
	// UDS endpoint with mutual TLS.
	body := setField(validLiveYAML, "gateway_endpoint", "unix:///run/extractor/gw.sock")
	_, path := setupLiveDir(t, body, testBinding(t))
	_, err := CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "delivery.tls") {
		t.Fatalf("mutual TLS on a UDS endpoint accepted: %v", err)
	}
	// TCP endpoint with disabled TLS.
	body2 := replaceLine(validGatewayYAML, "    mode: mutual", "    mode: disabled")
	body2 = removeLine(body2, "    ca_file: tls/ca.pem")
	body2 = removeLine(body2, "    cert_file: tls/cert.pem")
	body2 = removeLine(body2, "    key_file: tls/key.pem")
	_, path = setupGatewayDir(t, body2, testBinding(t))
	_, err = CheckGateway(path)
	if err == nil || !strings.Contains(err.Error(), "gateway.tls") {
		t.Fatalf("disabled TLS on a TCP listener accepted: %v", err)
	}
}

// gatewayNATSTLSBody rebuilds the gateway YAML with the nats.tls block
// replaced by the given indented lines.
func gatewayNATSTLSBody(body, tls string) string {
	body = removeLine(body, "      mode: disabled")
	return insertAfter(body, "    tls:", tls)
}

func TestCheckNATSSchemePairing(t *testing.T) {
	// nats:// with server TLS.
	body := gatewayNATSTLSBody(validGatewayYAML, "      mode: server\n      ca_file: tls/ca.pem")
	_, path := setupGatewayDir(t, body, testBinding(t))
	_, err := CheckGateway(path)
	if err == nil || !strings.Contains(err.Error(), "gateway.nats.tls") {
		t.Fatalf("nats:// with TLS accepted: %v", err)
	}
	// tls:// with disabled TLS.
	body2 := setField(validGatewayYAML, "server", "tls://nats.internal:4222")
	_, path = setupGatewayDir(t, body2, testBinding(t))
	_, err = CheckGateway(path)
	if err == nil || !strings.Contains(err.Error(), "gateway.nats.tls") {
		t.Fatalf("tls:// with disabled TLS accepted: %v", err)
	}
	// tls:// with server TLS and a CA file is valid.
	body3 := setField(validGatewayYAML, "server", "tls://nats.internal:4222")
	body3 = gatewayNATSTLSBody(body3, "      mode: server\n      ca_file: tls/ca.pem")
	_, path = setupGatewayDir(t, body3, testBinding(t))
	if _, err = CheckGateway(path); err != nil {
		t.Fatalf("tls:// with server TLS rejected: %v", err)
	}
}

func TestCheckEndpointForms(t *testing.T) {
	cases := []struct {
		value string
		ok    bool
	}{
		{"https://gw.internal:50051", true},
		{"unix:///run/extractor/gw.sock", true},
		{"unix://run/extractor/gw.sock", false},   // relative socket path
		{"unix:///run/x.sock?extra=1", false},     // query part
		{"http://gw.internal:50051", false},       // plaintext TCP on the gateway link
		{"https://gw.internal", false},            // no port
		{"https://gw.internal:0", false},          // port 0
		{"https://gw.internal:65536", false},      // port overflow
		{"https://gw.internal:50051/path", false}, // path part
		{"https://user:pass@gw.internal:50051", false},
		{"https://:50051", false},
		{`"[::1]:50051"`, false}, // no scheme (quoted for YAML)
		{"https://[::1]:50051", true},
		{"nats://nats.internal:4222", false}, // wrong scheme for this field
	}
	for _, tc := range cases {
		body := setField(validLiveYAML, "gateway_endpoint", tc.value)
		if tc.value == "nats://nats.internal:4222" || tc.value == "unix:///run/extractor/gw.sock" ||
			tc.value == "unix://run/extractor/gw.sock" || tc.value == "unix:///run/x.sock?extra=1" {
			// UDS endpoints pair with disabled TLS.
			body = replaceLine(body, "    mode: mutual", "    mode: disabled")
			body = removeLine(body, "    ca_file: tls/ca.pem")
			body = removeLine(body, "    cert_file: tls/cert.pem")
			body = removeLine(body, "    key_file: tls/key.pem")
		}
		_, path := setupLiveDir(t, body, testBinding(t))
		_, err := CheckLive(path)
		if tc.ok && err != nil {
			t.Fatalf("endpoint %s rejected: %v", tc.value, err)
		}
		if !tc.ok {
			if err == nil {
				t.Fatalf("endpoint %s accepted", tc.value)
			}
			if !strings.Contains(err.Error(), "delivery.gateway_endpoint") {
				t.Fatalf("error %q does not name the endpoint field", err.Error())
			}
		}
	}
}

func TestCheckPolygonFinality(t *testing.T) {
	b := testBinding(t)
	dir := t.TempDir()
	writeBindingFile(t, dir, b)

	// The sources file lives at an absolute path of the test's own
	// temporary directory; D78 keeps it absolute-only.
	sources := filepath.Join(t.TempDir(), "polygon-sources.json")
	if err := os.WriteFile(sources, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write sources: %v", err)
	}
	body := setField(validPolygonYAML, "sources_file", sources)

	path := writeYAML(t, dir, body)
	c, err := CheckLive(path)
	if err != nil {
		t.Fatalf("CheckLive polygon: %v", err)
	}
	if c.Polygon == nil {
		t.Fatalf("polygon section missing")
	}
	if c.Polygon.ExpectedChildBlockInterval != 256 {
		t.Fatalf("interval = %d", c.Polygon.ExpectedChildBlockInterval)
	}
	if c.Polygon.SourcesFile != sources {
		t.Fatalf("sources = %s", c.Polygon.SourcesFile)
	}

	// A relative sources_file is an error even when the file sits next
	// to the YAML (AC-OPS-01-05).
	relBody := setField(body, "sources_file", "polygon-sources.json")
	if err := os.WriteFile(filepath.Join(dir, "polygon-sources.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write neighbor sources: %v", err)
	}
	path = writeYAML(t, dir, relBody)
	_, err = CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "finality.polygon.sources_file") ||
		!strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative sources_file accepted: %v", err)
	}

	// A missing polygon section on a bor profile is an error.
	noFinality := removeSection(validPolygonYAML, "finality:")
	path = writeYAML(t, dir, noFinality)
	_, err = CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "finality.polygon") {
		t.Fatalf("missing polygon finality accepted: %v", err)
	}

	// A bad root-chain address is an error.
	badAddr := setField(body, "expected_root_chain_address", "0x8f5b8c8e13e6f6d4a1f0e2d4c3b2a1908f7e6d5c")
	path = writeYAML(t, dir, badAddr)
	_, err = CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "expected_root_chain_address") {
		t.Fatalf("0x-prefixed address accepted: %v", err)
	}
	upper := setField(body, "expected_root_chain_address", "8F5B8C8E13E6F6D4A1F0E2D4C3B2A1908F7E6D5C")
	path = writeYAML(t, dir, upper)
	_, err = CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "expected_root_chain_address") {
		t.Fatalf("uppercase address accepted: %v", err)
	}

	// A zero interval is an error.
	zeroInterval := setField(body, "expected_child_block_interval", "0")
	path = writeYAML(t, dir, zeroInterval)
	_, err = CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "expected_child_block_interval") {
		t.Fatalf("zero interval accepted: %v", err)
	}

	// An https L1 endpoint needs TLS, not disabled.
	httpsL1 := setField(body, "l1_endpoint", "https://l1.internal:8545")
	path = writeYAML(t, dir, httpsL1)
	_, err = CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "finality.polygon.l1_tls") {
		t.Fatalf("https L1 with disabled TLS accepted: %v", err)
	}
}

func TestCheckFinalityOnlyInPolygonLive(t *testing.T) {
	b := testBinding(t)
	// Ethereum live role with a finality section.
	body := validLiveYAML + "finality:\n  polygon:\n    l1_endpoint: http://l1:8545\n"
	_, path := setupLiveDir(t, body, b)
	_, err := CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "finality.polygon") {
		t.Fatalf("finality on an ethereum profile accepted: %v", err)
	}
	// Gateway role of a polygon network with a finality section.
	gwBody := setField(validGatewayYAML, "profile", "polygon-pos-mainnet")
	gwBody += "finality:\n  polygon:\n    l1_endpoint: http://l1:8545\n"
	_, path = setupGatewayDir(t, gwBody, b)
	_, err = CheckGateway(path)
	if err == nil || !strings.Contains(err.Error(), "finality.polygon") {
		t.Fatalf("finality in a gateway role accepted: %v", err)
	}
}

func TestCheckStorageSeparation(t *testing.T) {
	b := testBinding(t)
	body := replaceLine(validLiveYAML, "  live_dir: ../wal/eth", "  live_dir: /tmp/extractor-shared")
	body = replaceLine(body, "storage:", "storage:\n  gateway_dir: /tmp/extractor-shared")
	_, path := setupLiveDir(t, body, b)
	_, err := CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "storage.gateway_dir") {
		t.Fatalf("equal stores accepted: %v", err)
	}
	body = replaceLine(validLiveYAML, "  live_dir: ../wal/eth", "  live_dir: /tmp/extractor-shared")
	body = replaceLine(body, "storage:", "storage:\n  gateway_dir: /tmp/extractor-shared/sub")
	_, path = setupLiveDir(t, body, b)
	_, err = CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "must not contain one another") {
		t.Fatalf("nested stores accepted: %v", err)
	}
}

func TestCheckBindingFileErrors(t *testing.T) {
	b := testBinding(t)
	// Missing descriptor file.
	body := setField(validLiveYAML, "binding_file", "absent.json")
	_, path := setupLiveDir(t, body, b)
	_, err := CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "destination.binding_file") {
		t.Fatalf("missing descriptor accepted: %v", err)
	}
	// A descriptor whose stated digest does not match its content.
	dir := t.TempDir()
	writeTLSMaterial(t, dir)
	bad := b
	bad.Stream = "TAMPERED_STREAM"
	if err := os.WriteFile(filepath.Join(dir, "binding.json"), bindingJSON(bad), 0o644); err != nil {
		t.Fatalf("write binding: %v", err)
	}
	path = writeYAML(t, dir, validLiveYAML)
	_, err = CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "destination.binding_file") {
		t.Fatalf("tampered descriptor accepted: %v", err)
	}
}

func TestCheckMissingTLSFiles(t *testing.T) {
	b := testBinding(t)
	dir := t.TempDir()
	writeBindingFile(t, dir, b)
	// No tls/ directory at all.
	path := writeYAML(t, dir, validLiveYAML)
	_, err := CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "delivery.tls.ca_file") {
		t.Fatalf("missing CA accepted: %v", err)
	}
}

func TestCheckOtherRoleSectionOptionalButStructural(t *testing.T) {
	b := testBinding(t)
	// A live file may also carry a structurally valid gateway section.
	body := validLiveYAML + "gateway:\n  listen: https://gw.internal:50051\n"
	_, path := setupLiveDir(t, body, b)
	if _, err := CheckLive(path); err != nil {
		t.Fatalf("live file with a valid gateway section rejected: %v", err)
	}
	// A structurally invalid gateway section is rejected even for the
	// live role: the whole document is checked.
	body = validLiveYAML + "gateway:\n  listen: https://gw.internal\n"
	_, path = setupLiveDir(t, body, b)
	_, err := CheckLive(path)
	if err == nil || !strings.Contains(err.Error(), "gateway.listen") {
		t.Fatalf("invalid gateway section in a live file accepted: %v", err)
	}
}

func TestCheckRoleDispatch(t *testing.T) {
	_, path := setupLiveDir(t, validLiveYAML, testBinding(t))
	if _, err := CheckRole(path, RoleLive); err != nil {
		t.Fatalf("live dispatch: %v", err)
	}
	if _, err := CheckRole(path, "history"); err == nil {
		t.Fatalf("unknown role dispatched")
	}
}

func TestParseRoleRejectsUnknown(t *testing.T) {
	if _, err := ParseRole("history"); err == nil {
		t.Fatalf("history role parsed")
	}
	if _, err := ParseRole("gateway"); err != nil {
		t.Fatalf("gateway role rejected: %v", err)
	}
}

// removeSection drops a top-level section (the named head line and its
// indented body).
func removeSection(body, head string) string {
	out := ""
	skipping := false
	for _, line := range splitLines(body) {
		if line == head {
			skipping = true
			continue
		}
		if skipping {
			if line == "" || (len(line) > 0 && (line[0] == ' ' || line[0] == '-')) {
				continue
			}
			skipping = false
		}
		out += line + "\n"
	}
	return out
}

// removeLine drops one exact line.
func removeLine(body, line string) string {
	out := ""
	for _, l := range splitLines(body) {
		if l == line {
			continue
		}
		out += l + "\n"
	}
	return out
}

// insertAfter inserts indented lines after one exact line.
func insertAfter(body, anchor, insertion string) string {
	out := ""
	for _, l := range splitLines(body) {
		out += l + "\n"
		if l == anchor && insertion != "" {
			out += insertion + "\n"
		}
	}
	return out
}
