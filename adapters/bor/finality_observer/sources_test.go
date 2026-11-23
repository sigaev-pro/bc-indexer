package finalityobserver

import (
	"errors"
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
)

func TestParseSourcesAcceptsTheClosedSchema(t *testing.T) {
	src, err := ParseSources(testSources(""))
	if err != nil {
		t.Fatal(err)
	}
	if src.SchemaVersion != 1 {
		t.Fatalf("schema version %d", src.SchemaVersion)
	}
	if src.L1.Role != RoleL1Execution || src.Heimdall.Role != RoleHeimdall {
		t.Fatal("roles must be the two closed role names")
	}
	if src.L1.Network != testL1Network {
		t.Fatalf("l1 network %v", src.L1.Network)
	}
	if src.Heimdall.Identity() != testHeimdallIdentity {
		t.Fatalf("heimdall identity %v", src.Heimdall.Identity())
	}
	want := profiles.ComputeSourcesBindingDigest([]byte(testSources("")))
	if src.Digest != Hash32Of(want) {
		t.Fatal("the digest is the SHA-256 of the exact file bytes")
	}
}

func TestParseSourcesWhitespaceChangesDigestOnly(t *testing.T) {
	compact, err := ParseSources(testSources(""))
	if err != nil {
		t.Fatal(err)
	}
	spaced, err := ParseSources(testSources("  "))
	if err != nil {
		t.Fatal(err)
	}
	if compact.Digest == spaced.Digest {
		t.Fatal("whitespace changes the digest")
	}
	if compact.L1.DeploymentID != spaced.L1.DeploymentID ||
		compact.Heimdall.DeploymentID != spaced.Heimdall.DeploymentID {
		t.Fatal("whitespace does not change the deployment IDs")
	}
}

func TestParseSourcesRejectsBrokenFiles(t *testing.T) {
	base := string(testSources(""))
	cases := map[string]string{
		"empty":            "",
		"not utf-8":        "{\"schema_version\":1,\"x\":\"\xff\"}",
		"unknown field":    strings.Replace(base, `"schema_version": 1`, `"schema_version": 1, "extra": true`, 1),
		"duplicate field":  strings.Replace(base, `"schema_version": 1`, `"schema_version": 1, "schema_version": 2`, 1),
		"wrong version":    strings.Replace(base, `"schema_version": 1`, `"schema_version": 2`, 1),
		"wrong l1 role":    strings.Replace(base, `POLYGON_L1_EXECUTION`, `POLYGON_HEIMDALL`, 1),
		"wrong bor role":   strings.Replace(base, `"role": "POLYGON_HEIMDALL"`, `"role": "POLYGON_L1_EXECUTION"`, 1),
		"zero l1 id":       strings.Replace(base, mustID("l1-deployment").String(), strings.Repeat("0", 32), 1),
		"zero genesis":     strings.Replace(base, testHeimdallIdentity.GenesisDocumentSHA256.String(), strings.Repeat("0", 64), 1),
		"empty endpoint":   strings.Replace(base, `http://heimdall:1317`, ``, 1),
		"empty chain id":   strings.Replace(base, `"chain_id": "heimdall-137"`, `"chain_id": ""`, 1),
		"trailing data":    base + " {}",
		"comment":          strings.Replace(base, "{", "{ // comment", 1),
		"missing heimdall": heimdallRemoved(base),
	}
	for name, file := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSources([]byte(file)); !errors.Is(err, ErrSources) {
				t.Fatalf("error %v, want ErrSources", err)
			}
		})
	}
}

func heimdallRemoved(base string) string {
	start := strings.Index(base, `"heimdall"`)
	end := strings.LastIndex(base, "}")
	return base[:start-3] + base[end:]
}

// Hash32Of converts the profiles digest type to the block hash type used
// by the evidence.
func Hash32Of(d profiles.SourcesBindingDigest) [32]byte {
	var out [32]byte
	copy(out[:], d[:])
	return out
}
