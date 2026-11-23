// Admission tests of the mock gateway ingest listener: only the
// adapter certificate of the gateway's own network completes the
// mutual TLS handshake (a cross-network certificate is rejected during
// the handshake, before any protocol state changes), and the session
// admission verifies the protocol, the network profile and the binding
// digest before any event is accepted. Publication stays TRANSIENT
// until the NATS destination verifies, so an event over an admitted
// session is answered with a TRANSIENT nak that keeps it pending.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sigaev-pro/bc-indexer/deploy/internal/deployconfig"
	"github.com/sigaev-pro/bc-indexer/deploy/internal/eventlog"
	"github.com/sigaev-pro/bc-indexer/deploy/internal/wire"
)

// testCerts is one throwaway PKI: a CA, the gateway server certificate
// of the eth circuit and two adapter certificates (eth and bsc). The
// PEM material is generated once per test binary; each gateway writes
// its own files into its own temporary directory.
type testCerts struct {
	caPEM      []byte
	ethGateway certPair
	ethAdapter certPair
	bscAdapter certPair
}

type certPair struct {
	certPEM []byte
	keyPEM  []byte
}

var (
	certsOnce sync.Once
	certs     *testCerts
)

func loadCerts(t *testing.T) *testCerts {
	t.Helper()
	certsOnce.Do(func() {
		certs = buildCerts(t)
	})
	return certs
}

func buildCerts(t *testing.T) *testCerts {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "deploy-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caKey.Public(), caKey)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}

	newLeaf := func(cn string, usage x509.ExtKeyUsage) certPair {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("leaf key: %v", err)
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, caCert, key.Public(), caKey)
		if err != nil {
			t.Fatalf("leaf cert %s: %v", cn, err)
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatalf("marshal key %s: %v", cn, err)
		}
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		return certPair{certPEM: certPEM, keyPEM: keyPEM}
	}

	return &testCerts{
		caPEM:      pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		ethGateway: newLeaf("deploy/eth/gateway", x509.ExtKeyUsageServerAuth),
		ethAdapter: newLeaf("deploy/eth/adapter", x509.ExtKeyUsageClientAuth),
		bscAdapter: newLeaf("deploy/bsc/adapter", x509.ExtKeyUsageClientAuth),
	}
}

// testGateway runs the real listener, ingest loop and journal with a
// publisher that has no NATS connection: publication answers TRANSIENT,
// which is exactly the admission behavior under test.
type testGateway struct {
	network deployconfig.NetworkDefinition
	binding deployconfig.Binding
	addr    string
	cancel  context.CancelFunc
	done    chan struct{}
}

func startGateway(t *testing.T) *testGateway {
	t.Helper()
	material := loadCerts(t)
	network, err := deployconfig.ByProfile("ethereum-mainnet")
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	binding, err := deployconfig.GenerateBinding(network)
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	cfg := deployconfig.GatewayConfig{}
	cfg.Gateway.Listen = "https://127.0.0.1:0"
	cfg.Gateway.TLS.CertFile = write("eth-gateway.crt", material.ethGateway.certPEM)
	cfg.Gateway.TLS.KeyFile = write("eth-gateway.key", material.ethGateway.keyPEM)
	cfg.Gateway.TLS.CAFile = write("ca.crt", material.caPEM)

	log := eventlog.New("gateway-test", string(network.Key), "gateway", "error")
	journal, err := openJournal(t.TempDir())
	if err != nil {
		t.Fatalf("journal: %v", err)
	}
	pub := newPublisher(cfg, network, binding, log)
	ln, err := listenIngest(cfg, network, log)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	g := &testGateway{
		network: network,
		binding: binding,
		addr:    ln.Addr().String(),
		done:    make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	go func() {
		defer close(g.done)
		serveIngest(ctx, ln, pub, journal, network, binding, log)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-g.done:
		case <-time.After(10 * time.Second):
			t.Errorf("serveIngest did not stop on cancel")
		}
	})
	return g
}

// dial opens one mutual TLS session with the given client certificate.
// The test client skips server verification on purpose: the object
// under test is the gateway's admission of the client.
func dial(t *testing.T, g *testGateway, pair certPair) (net.Conn, error) {
	t.Helper()
	cert, err := tls.X509KeyPair(pair.certPEM, pair.keyPEM)
	if err != nil {
		t.Fatalf("client pair: %v", err)
	}
	dialer := &tls.Dialer{
		Config: &tls.Config{
			Certificates:       []tls.Certificate{cert},
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		},
		NetDialer: &net.Dialer{Timeout: 5 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return dialer.DialContext(ctx, "tcp", g.addr)
}

// hello builds a session hello of the given shape.
func hello(g *testGateway, bindingDigest, profile string, protocol int) wire.Hello {
	return wire.Hello{
		Type: "hello", Protocol: protocol, Role: "adapter",
		NetworkProfile: profile,
		ProducerID:     "00112233445566778899aabbccddeeff",
		RunID:          "ffeeddccbbaa99887766554433221100",
		BindingDigest:  bindingDigest,
	}
}

// exchange writes one message and reads one answer with deadlines.
func exchange(t *testing.T, conn net.Conn, send any, recv any) {
	t.Helper()
	if err := wire.Write(conn, send); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := wire.Read(conn, recv); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func validDigest(t *testing.T, g *testGateway) string {
	t.Helper()
	return hex.EncodeToString(g.binding.DescriptorDigest[:])
}

func TestIngestAdmitsOwnAdapter(t *testing.T) {
	g := startGateway(t)
	conn, err := dial(t, g, loadCerts(t).ethAdapter)
	if err != nil {
		t.Fatalf("own adapter handshake failed: %v", err)
	}
	defer conn.Close()

	var ack wire.HelloAck
	exchange(t, conn, hello(g, validDigest(t, g), g.network.Profile, wire.Protocol), &ack)
	if ack.Type != "hello_ack" || ack.NetworkProfile != g.network.Profile {
		t.Fatalf("hello ack: %+v", ack)
	}
	if ack.BindingDigest != validDigest(t, g) {
		t.Fatalf("ack digest: %q", ack.BindingDigest)
	}
	if len(ack.GatewayEpoch) != 32 {
		t.Fatalf("gateway epoch: %q", ack.GatewayEpoch)
	}

	// Without a verified NATS destination the publication class is
	// TRANSIENT: the event is refused with a nak that keeps it pending
	// on the adapter side.
	var nak wire.Nak
	exchange(t, conn, wire.Event{
		Type: "event", EventSeq: 1, Height: 1,
		Route: "tx.applied", Payload: []byte(`{}`),
	}, &nak)
	if nak.Type != "nak" || nak.Code != "TRANSIENT" || nak.EventSeq != 1 {
		t.Fatalf("event answer: %+v", nak)
	}
}

func TestIngestRejectsForeignCertificate(t *testing.T) {
	g := startGateway(t)
	material := loadCerts(t)
	conn, err := dial(t, g, material.bscAdapter)
	if err != nil {
		// A handshake failure is one valid rejection outcome.
		return
	}
	defer conn.Close()
	// With TLS 1.3 client authentication the client finishes its flight
	// before the server verifies the certificate, so the rejection
	// surfaces on the first exchange: the server never reaches the
	// protocol.
	if err := wire.Write(conn, hello(g, validDigest(t, g), g.network.Profile, wire.Protocol)); err == nil {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var refuse wire.ErrorRefuse
		if err := wire.Read(conn, &refuse); err == nil {
			t.Fatalf("cross-network adapter certificate was admitted: %+v", refuse)
		}
	}
}

func TestIngestRefusesWrongBindingDigest(t *testing.T) {
	g := startGateway(t)
	conn, err := dial(t, g, loadCerts(t).ethAdapter)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	defer conn.Close()
	var refuse wire.ErrorRefuse
	exchange(t, conn, hello(g, fmt.Sprintf("%064x", 1), g.network.Profile, wire.Protocol), &refuse)
	if refuse.Type != "error" || refuse.Code != "BINDING" {
		t.Fatalf("refuse: %+v", refuse)
	}
}

func TestIngestRefusesWrongProfile(t *testing.T) {
	g := startGateway(t)
	conn, err := dial(t, g, loadCerts(t).ethAdapter)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	defer conn.Close()
	var refuse wire.ErrorRefuse
	exchange(t, conn, hello(g, validDigest(t, g), "bsc-mainnet", wire.Protocol), &refuse)
	if refuse.Type != "error" || refuse.Code != "NETWORK" {
		t.Fatalf("refuse: %+v", refuse)
	}
}

func TestIngestRefusesBadProtocol(t *testing.T) {
	g := startGateway(t)
	conn, err := dial(t, g, loadCerts(t).ethAdapter)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	defer conn.Close()
	var refuse wire.ErrorRefuse
	exchange(t, conn, hello(g, validDigest(t, g), g.network.Profile, wire.Protocol+7), &refuse)
	if refuse.Type != "error" || refuse.Code != "PROTOCOL" {
		t.Fatalf("refuse: %+v", refuse)
	}
}

// TestIngestShutdownClosesSessions: an idle admitted session must not
// hold the shutdown — the gateway closes open connections when the
// context stops, so the ingest loop terminates.
func TestIngestShutdownClosesSessions(t *testing.T) {
	g := startGateway(t)
	conn, err := dial(t, g, loadCerts(t).ethAdapter)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	defer conn.Close()
	var ack wire.HelloAck
	exchange(t, conn, hello(g, validDigest(t, g), g.network.Profile, wire.Protocol), &ack)
	if ack.Type != "hello_ack" {
		t.Fatalf("no session: %+v", ack)
	}

	g.cancel()
	select {
	case <-g.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("serveIngest hung on an idle session after cancel")
	}
	// The session is closed: the read side wakes up.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var drop wire.Nak
	if err := wire.Read(conn, &drop); err == nil {
		t.Fatalf("idle session still answers after shutdown")
	}
}
