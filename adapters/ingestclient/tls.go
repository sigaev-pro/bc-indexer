package ingestclient

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc/credentials"

	"github.com/sigaev-pro/bc-indexer/config"
)

// transportCredentials builds the client TLS credentials of the gateway
// link. The link is TCP with mutual TLS (DELIVERY_STATE_MACHINE, "Errors",
// CONFIG_BLOCKED: "Только поверх mTLS или разрешённого authenticated UDS"
// of CONTROL_WIRE_CONTRACT): the client verifies the gateway certificate
// against ca_file, checks the endpoint host against the gateway certificate,
// and presents cert_file and key_file. The server name must be in the
// gateway certificate SANs (the bootstrap tool issues <network>-gateway and
// localhost).
func transportCredentials(endpoint config.Endpoint, tlsBlock config.ResolvedTLS) (credentials.TransportCredentials, error) {
	if err := checkLink(endpoint, tlsBlock); err != nil {
		return nil, err
	}
	ca, err := os.ReadFile(tlsBlock.CAFile)
	if err != nil {
		return nil, fmt.Errorf("%w: ca_file: %v", ErrConfig, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("%w: ca_file holds no certificate", ErrConfig)
	}
	pair, err := tls.LoadX509KeyPair(tlsBlock.CertFile, tlsBlock.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("%w: cert_file and key_file: %v", ErrConfig, err)
	}
	return credentials.NewTLS(&tls.Config{
		ServerName:   serverName(endpoint.Host),
		RootCAs:      pool,
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2"},
	}), nil
}

// checkLink validates the shape of the gateway link without reading a file:
// an https endpoint with a host and a port, and mutual TLS with the three
// files named. It is what NewDriver checks before any I/O.
func checkLink(endpoint config.Endpoint, tlsBlock config.ResolvedTLS) error {
	if endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.Port == "" {
		return fmt.Errorf("%w: endpoint %q, want https://host:port", ErrConfig, endpoint.Raw)
	}
	if tlsBlock.Mode != config.TLSMutual || tlsBlock.CAFile == "" || tlsBlock.CertFile == "" || tlsBlock.KeyFile == "" {
		return fmt.Errorf("%w: delivery.tls must be mutual with ca_file, cert_file and key_file", ErrConfig)
	}
	return nil
}

// serverName returns the name the gateway certificate is verified against:
// the host of the endpoint, without the brackets of an IPv6 literal.
func serverName(host string) string {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host[1 : len(host)-1]
	}
	return host
}
