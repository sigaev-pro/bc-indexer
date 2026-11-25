package ingestclient

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sigaev-pro/bc-indexer/adapters/ingestclient/ingesttest"
	"github.com/sigaev-pro/bc-indexer/config"
)

func TestServerNameStripsTheBracketsOfAnIPV6Literal(t *testing.T) {
	for host, want := range map[string]string{"[::1]": "::1", "localhost": "localhost", "127.0.0.1": "127.0.0.1", "[": "["} {
		if got := serverName(host); got != want {
			t.Fatalf("serverName(%q) = %q, want %q", host, got, want)
		}
	}
}

// The mutual TLS of the link loads three files; a file that is missing,
// empty of certificates or mismatched is a configuration error, found when
// the connection is made.
func TestTransportCredentialsRefuseBrokenFiles(t *testing.T) {
	identity := ingesttest.NewIdentity(t, "localhost")
	endpoint := config.Endpoint{Scheme: "https", Host: "localhost", Port: "50051"}
	mutual := config.ResolvedTLS{Mode: config.TLSMutual, CAFile: identity.CAFile,
		CertFile: identity.CertFile, KeyFile: identity.KeyFile}
	if creds, err := transportCredentials(endpoint, mutual); err != nil || creds == nil {
		t.Fatalf("the files of the identity: %v", err)
	}
	notCADir := t.TempDir()
	empty := filepath.Join(notCADir, "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]config.ResolvedTLS{
		"missing ca file":  {Mode: config.TLSMutual, CAFile: filepath.Join(notCADir, "missing.pem"), CertFile: identity.CertFile, KeyFile: identity.KeyFile},
		"ca file is empty": {Mode: config.TLSMutual, CAFile: empty, CertFile: identity.CertFile, KeyFile: identity.KeyFile},
		"key of another":   {Mode: config.TLSMutual, CAFile: identity.CAFile, CertFile: identity.CertFile, KeyFile: identity.OtherKeyFile},
		"missing key file": {Mode: config.TLSMutual, CAFile: identity.CAFile, CertFile: identity.CertFile, KeyFile: filepath.Join(notCADir, "missing.key")},
		"no ca file named": {Mode: config.TLSMutual, CertFile: identity.CertFile, KeyFile: identity.KeyFile},
		"server tls":       {Mode: config.TLSServer, CAFile: identity.CAFile},
		"tls disabled":     {Mode: config.TLSDisabled},
	}
	for name, tlsBlock := range cases {
		if creds, err := transportCredentials(endpoint, tlsBlock); !errors.Is(err, ErrConfig) || creds != nil {
			t.Fatalf("%s: transportCredentials answered %v, %v; want ErrConfig", name, creds, err)
		}
	}
	for name, bad := range map[string]config.Endpoint{
		"http":  {Scheme: "http", Host: "localhost", Port: "50051"},
		"unix":  {Scheme: "unix", Path: "/tmp/gateway.sock"},
		"host":  {Scheme: "https", Port: "50051"},
		"port":  {Scheme: "https", Host: "localhost"},
		"empty": {},
	} {
		if creds, err := transportCredentials(bad, mutual); !errors.Is(err, ErrConfig) || creds != nil {
			t.Fatalf("endpoint %s: transportCredentials answered %v, %v; want ErrConfig", name, creds, err)
		}
	}
}
