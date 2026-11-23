package config

import (
	"strings"
	"testing"
)

// Parse tests: the closed document (AC-OPS-01-01). Unknown and
// duplicate keys, wrong scalar types, aliases, merge keys, extra
// documents and oversized files are rejected with a field path and a
// line, and nothing is silently coerced or defaulted.

func TestParseAcceptsValidLiveDocument(t *testing.T) {
	f, err := Parse([]byte(validLiveYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", f.SchemaVersion)
	}
	if f.Network.Profile != "ethereum-mainnet" {
		t.Fatalf("profile = %s", f.Network.Profile)
	}
	if f.Native == nil || f.Native.Kind != "geth" {
		t.Fatalf("native.kind = %+v", f.Native)
	}
	if f.Delivery == nil || f.Delivery.TLS.Mode != "mutual" || f.Delivery.TLS.CAFile != "tls/ca.pem" {
		t.Fatalf("delivery = %+v", f.Delivery)
	}
	if f.Storage.LiveDir != "../wal/eth" {
		t.Fatalf("live_dir = %s", f.Storage.LiveDir)
	}
	if f.Runtime.LogLevel != "warn" {
		t.Fatalf("log_level = %s", f.Runtime.LogLevel)
	}
	if f.Gateway != nil || f.Finality != nil {
		t.Fatalf("absent sections must stay nil")
	}
}

func TestParseRejectsUnknownKeyAtEveryLevel(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"root", "schema_version: 1\nbogus: 1\n", "bogus"},
		{"network", "network:\n  profile: ethereum-mainnet\n  bogus: 1\n", "network.bogus"},
		{"delivery-tls", "delivery:\n  tls:\n    mode: mutual\n    bogus: 1\n", "delivery.tls.bogus"},
		{"nats", "gateway:\n  nats:\n    bogus: 1\n", "gateway.nats.bogus"},
		{"finality", "finality:\n  polygon:\n    bogus: 1\n", "finality.polygon.bogus"},
		{"history-section", "schema_version: 1\nhistory:\n  from: 1\n", "history"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			if err == nil {
				t.Fatalf("unknown key accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name the field %q", err.Error(), tc.want)
			}
			if Exit(err) != ExitConfig {
				t.Fatalf("exit = %d, want 2", Exit(err))
			}
		})
	}
}

func TestParseRejectsDuplicateKeyAtEveryLevel(t *testing.T) {
	cases := []struct {
		name string
		body string
		key  string
	}{
		{"root", "schema_version: 1\nschema_version: 2\n", "schema_version"},
		{"network", "network:\n  profile: a\n  profile: b\n", "profile"},
		{"tls-mode", "delivery:\n  tls:\n    mode: mutual\n    mode: disabled\n", "mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			if err == nil {
				t.Fatalf("duplicate key accepted")
			}
			if !strings.Contains(err.Error(), "duplicate key '"+tc.key+"'") {
				t.Fatalf("error %q does not report the duplicate %q", err.Error(), tc.key)
			}
		})
	}
}

func TestParseRejectsWrongScalarType(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"string-version", "schema_version: one\n", "schema_version"},
		{"float-version", "schema_version: 1.0\n", "schema_version"},
		{"bool-version", "schema_version: true\n", "schema_version"},
		{"int-profile", "network:\n  profile: 123\n", "network.profile"},
		{"int-binding", "destination:\n  binding_file: 42\n", "destination.binding_file"},
		{"int-loglevel", "runtime:\n  log_level: 7\n", "runtime.log_level"},
		{"float-interval", "finality:\n  polygon:\n    expected_child_block_interval: 2.5\n", "expected_child_block_interval"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			if err == nil {
				t.Fatalf("wrong scalar type accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name the field %q", err.Error(), tc.want)
			}
		})
	}
}

func TestParseRejectsIntOverflow(t *testing.T) {
	_, err := Parse([]byte("transport:\n  max_frame_bytes: 99999999999999999999999999\n"))
	if err == nil {
		t.Fatalf("overflowing integer accepted")
	}
	if !strings.Contains(err.Error(), "max_frame_bytes") && !strings.Contains(err.Error(), "config") {
		t.Fatalf("unexpected error %q", err.Error())
	}
}

func TestParseRejectsHiddenSubstitution(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"alias", "network: &n\n  profile: ethereum-mainnet\ndestination:\n  binding_file: b.json\nx: *n\n"},
		{"alias-value", "schema_version: &v 1\nnetwork:\n  profile: *v\n"},
		{"merge-key", "delivery:\n  tls:\n    mode: mutual\nfinality:\n  polygon:\n    <<: {mode: disabled}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			if err == nil {
				t.Fatalf("hidden substitution accepted")
			}
		})
	}
}

func TestParseRejectsAliasedValueUnderKnownKey(t *testing.T) {
	_, err := Parse([]byte("runtime: &r\n  log_level: info\nx: *r\n"))
	if err == nil {
		t.Fatalf("expected an unknown-key error for x")
	}
	// The alias node itself is rejected when it appears under a known key.
	_, err = Parse([]byte("a: &v info\nruntime:\n  log_level: *v\n"))
	if err == nil {
		t.Fatalf("alias under a known key accepted")
	}
	if !strings.Contains(err.Error(), "aliases") {
		t.Fatalf("error %q does not mention aliases", err.Error())
	}
}

func TestParseRejectsExtraDocuments(t *testing.T) {
	_, err := Parse([]byte("schema_version: 1\n---\nschema_version: 2\n"))
	if err == nil {
		t.Fatalf("multi-document stream accepted")
	}
	if !strings.Contains(err.Error(), "one YAML document") {
		t.Fatalf("error %q does not report the extra document", err.Error())
	}
}

func TestParseRejectsNonMappingRoot(t *testing.T) {
	if _, err := Parse([]byte("- 1\n- 2\n")); err == nil {
		t.Fatalf("sequence root accepted")
	}
	if _, err := Parse([]byte("just a scalar\n")); err == nil {
		t.Fatalf("scalar root accepted")
	}
}

func TestParseRejectsEmptyAndMissing(t *testing.T) {
	if _, err := Parse(nil); err == nil {
		t.Fatalf("empty input accepted")
	}
	if _, err := Parse([]byte("# only a comment\n")); err == nil {
		t.Fatalf("comment-only input accepted")
	}
	if _, err := Parse([]byte("\t\n")); err == nil {
		t.Fatalf("whitespace input accepted")
	}
}

func TestParseRejectsOversizedFile(t *testing.T) {
	body := strings.Repeat("# padding line to exceed the yaml bound\n", MaxYAMLBytes/32+2)
	if len(body) <= MaxYAMLBytes {
		t.Fatalf("fixture is %d bytes, want more than %d", len(body), MaxYAMLBytes)
	}
	if _, err := Parse([]byte(body)); err == nil {
		t.Fatalf("oversized file accepted")
	}
}

func TestParseReportsLineNumbers(t *testing.T) {
	_, err := Parse([]byte("schema_version: 1\nnetwork:\n  profile: ethereum-mainnet\n  bogus: 1\n"))
	if err == nil {
		t.Fatalf("unknown key accepted")
	}
	if !strings.Contains(err.Error(), "line 4") {
		t.Fatalf("error %q does not carry the line", err.Error())
	}
}

func TestParseCollectsSeveralViolations(t *testing.T) {
	_, err := Parse([]byte("schema_version: 3\nbogus1: 1\nbogus2: 2\n"))
	if err == nil {
		t.Fatalf("violations accepted")
	}
	for _, want := range []string{"bogus1", "bogus2"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not list %q", err.Error(), want)
		}
	}
}

func TestParseTreatsExplicitNullAsAbsent(t *testing.T) {
	f, err := Parse([]byte("schema_version: 1\nruntime:\nstorage:\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Runtime.LogLevel != "" {
		t.Fatalf("log_level = %s, want empty", f.Runtime.LogLevel)
	}
}

func TestParseRejectsSyntaxError(t *testing.T) {
	if _, err := Parse([]byte("network:\n  profile: [unclosed\n")); err == nil {
		t.Fatalf("syntax error accepted")
	}
}
