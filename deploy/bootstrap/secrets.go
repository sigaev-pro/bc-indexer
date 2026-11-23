// The trust material and the NATS account configuration of the
// deployment: one private CA, the service certificates with deployment,
// network and role identities, per-account users with the least
// permissions of their role, and the rendered nats.conf. The CA key
// stays with the operator: it is written under the secrets directory
// and never mounted into a runtime service
// (DEPLOYMENT_BOOTSTRAP_CONTRACT, "Trust bootstrap и NATS permissions").
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/sigaev-pro/bc-indexer/deploy/internal/deployconfig"
)

// cmdSecrets generates the CA, the certificates, the account passwords
// and the nats.conf. Existing material is never silently replaced: the
// command refuses to overwrite unless the operator removes the secrets
// directory, so a regenerated CA can never invalidate running mounts by
// accident.
func cmdSecrets(configDir, secretsDir string) error {
	if _, err := os.Stat(filepath.Join(secretsDir, "ca.key")); err == nil {
		return fmt.Errorf("%s already holds a CA; remove the directory explicitly to regenerate all trust material", secretsDir)
	}
	dirs := []string{
		secretsDir,
		filepath.Join(secretsDir, "nats"),
	}
	for _, n := range deployconfig.Networks {
		dirs = append(dirs, filepath.Join(secretsDir, string(n.Key)))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}

	caKey, caCert, err := newCA()
	if err != nil {
		return err
	}
	if err := writeKey(filepath.Join(secretsDir, "ca.key"), caKey); err != nil {
		return err
	}
	if err := writeCert(filepath.Join(secretsDir, "ca.crt"), caCert); err != nil {
		return err
	}
	fmt.Printf("ca: %s/ca.crt (keep %s/ca.key with the operator)\n", secretsDir, secretsDir)

	// The NATS server certificate: SANs match the service DNS names and
	// the diagnostic loopback address.
	natsKey, natsCert, err := newLeaf(caKey, caCert, "deploy/nats",
		[]string{"nats", "localhost"}, []net.IP{net.ParseIP("127.0.0.1")}, false)
	if err != nil {
		return err
	}
	if err := writeKey(filepath.Join(secretsDir, "nats", "server.key"), natsKey); err != nil {
		return err
	}
	if err := writeCert(filepath.Join(secretsDir, "nats", "server.crt"), natsCert); err != nil {
		return err
	}
	fmt.Printf("nats: server certificate issued\n")

	for _, n := range deployconfig.Networks {
		gwKey, gwCert, err := newLeaf(caKey, caCert, n.GatewayIdentity(),
			[]string{n.GatewayService, "localhost"}, nil, false)
		if err != nil {
			return err
		}
		if err := writeKey(filepath.Join(secretsDir, string(n.Key), "gateway.key"), gwKey); err != nil {
			return err
		}
		if err := writeCert(filepath.Join(secretsDir, string(n.Key), "gateway.crt"), gwCert); err != nil {
			return err
		}
		adKey, adCert, err := newLeaf(caKey, caCert, n.AdapterIdentity(), nil, nil, true)
		if err != nil {
			return err
		}
		if err := writeKey(filepath.Join(secretsDir, string(n.Key), "adapter.key"), adKey); err != nil {
			return err
		}
		if err := writeCert(filepath.Join(secretsDir, string(n.Key), "adapter.crt"), adCert); err != nil {
			return err
		}
		// Two users per network account: the bootstrap user holds stream
		// administration inside the account, the gateway user holds the
		// runtime publish surface.
		bootstrapUser := natsUser{
			Name:     fmt.Sprintf("%s-bootstrap", n.Key),
			Password: newPassword(),
			Publish:  []string{"$JS.API.>"},
		}
		gatewayUser := natsUser{
			Name:     fmt.Sprintf("%s-gateway", n.Key),
			Password: newPassword(),
			Publish: []string{
				n.SubjectPrefix + ".>",
				"$JS.API.INFO",
				fmt.Sprintf("$JS.API.STREAM.INFO.%s", n.Stream),
				"$SYS.REQ.USER.INFO",
			},
		}
		if err := writeCreds(filepath.Join(secretsDir, "nats", fmt.Sprintf("bootstrap-%s.creds", n.Key)), bootstrapUser); err != nil {
			return err
		}
		if err := writeCreds(filepath.Join(secretsDir, string(n.Key), "nats.creds"), gatewayUser); err != nil {
			return err
		}
		networkUsers[n.Key] = accountUsers{Bootstrap: bootstrapUser, Gateway: gatewayUser}
		fmt.Printf("%s: gateway and adapter certificates issued, account %s prepared\n", n.Key, n.Account)
	}
	return writeNATSConf(secretsDir)
}

// accountUsers collects the generated users of one network account.
type accountUsers struct {
	Bootstrap natsUser
	Gateway   natsUser
}

// networkUsers is filled by cmdSecrets for the nats.conf renderer; the
// command runs single-threaded once per environment.
var networkUsers = map[deployconfig.NetworkKey]accountUsers{}

// natsUser is one generated NATS user.
type natsUser struct {
	Name     string
	Password string
	Publish  []string
}

// writeNATSConf renders the NATS server configuration: explicit
// max_payload of the 2 MiB frame budget, JetStream file storage, the
// client TLS listener and the per-network accounts with their two users
// each.
func writeNATSConf(secretsDir string) error {
	var b []byte
	line := func(format string, args ...any) { b = append(b, []byte(fmt.Sprintf(format, args...))...) }
	line("# Generated by deploy bootstrap; do not edit.\n")
	line("host: \"0.0.0.0\"\n")
	line("port: 4222\n\n")
	line("# The full frame with its NATS headers must pass: 2 MiB, not the 1 MiB default.\n")
	line("max_payload: %d\n\n", deployconfig.MaxFrameBytesDefault)
	line("tls {\n")
	line("  cert_file: \"/etc/nats/tls/server.crt\"\n")
	line("  key_file: \"/etc/nats/tls/server.key\"\n")
	line("}\n\n")
	line("jetstream {\n")
	line("  store_dir: \"/data/jetstream\"\n")
	line("}\n\n")
	line("http_port: 8222\n\n")
	line("accounts {\n")
	for _, n := range deployconfig.Networks {
		u := networkUsers[n.Key]
		line("  %s {\n", n.Account)
		line("    jetstream: enabled\n")
		line("    users [\n")
		writeUser := func(u natsUser) {
			line("      { user: %q, pass: %q, permissions: {\n", u.Name, u.Password)
			line("        publish: [")
			for i, p := range u.Publish {
				if i > 0 {
					line(", ")
				}
				line("%q", p)
			}
			line("],\n")
			line("        subscribe: { allow: [\"_INBOX.>\"] },\n")
			line("      } },\n")
		}
		writeUser(u.Bootstrap)
		writeUser(u.Gateway)
		line("    ]\n")
		line("  }\n")
	}
	line("}\n")
	path := filepath.Join(secretsDir, "nats", "nats.conf")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	fmt.Printf("nats.conf: %s\n", path)
	return nil
}

// writeCreds writes one two-line credentials file.
func writeCreds(path string, u natsUser) error {
	return os.WriteFile(path, []byte(u.Name+"\n"+u.Password+"\n"), 0o600)
}

// newPassword returns a 24-byte random password in hex.
func newPassword() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// newCA creates the private root certificate of the deployment.
func newCA() (*ecdsa.PrivateKey, *x509.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"bc-indexer"},
			CommonName:   "deploy-root",
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return key, cert, nil
}

// newLeaf creates one certificate signed by the CA. The common name is
// the deployment/network/role identity; server certificates carry the
// service DNS SANs, client certificates carry the clientAuth EKU.
func newLeaf(caKey *ecdsa.PrivateKey, caCert *x509.Certificate, identity string, dnsNames []string, ips []net.IP, clientOnly bool) (*ecdsa.PrivateKey, *x509.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"bc-indexer"},
			CommonName:   identity,
		},
		NotBefore:   now.Add(-time.Hour),
		NotAfter:    now.AddDate(3, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		DNSNames:    dnsNames,
		IPAddresses: ips,
	}
	if clientOnly {
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	} else {
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return key, cert, nil
}

// randomSerial returns a positive random serial number.
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 127)
	return rand.Int(rand.Reader, limit)
}

// writeKey writes one ECDSA private key in PKCS 8 form.
func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	return os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
}

// writeCert writes one certificate in PEM form.
func writeCert(path string, cert *x509.Certificate) error {
	block := &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}
	return os.WriteFile(path, pem.EncodeToMemory(block), 0o644)
}
