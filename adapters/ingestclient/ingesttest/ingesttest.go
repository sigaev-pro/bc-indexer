// Package ingesttest builds READY batches of real canonical records for the
// tests of the ingest client and of the gateway handshake, and holds the
// other test supports those suites share: the TLS material of a mutual TLS
// link and a storage spy that stops a driver at one durable step.
//
// DECISION (contract reference: MESSAGE_CONTRACT, section 6.1, the typed
// record format; CONTROL_WIRE_CONTRACT, "READY manifest и reservation"):
// the block event builder of the schema registry block
// (core/normalization/bsc/blockrecord) is not merged at the commit this file
// was written against. Every test record goes through the one function
// BuildBlockEvent below, which maps a few block facts to a typed tree with
// placeholder type keys and field tags and lets the codec of protocol/codec
// produce the canonical fragments and the READY descriptors. When the
// builder merges, the body of BuildBlockEvent becomes a call to it and the
// tests keep their shape.
package ingesttest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sigaev-pro/bc-indexer/config"
	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// The placeholder type key and field tags of the test block record. The
// registry membership of keys and tags is the schema layer's check; the
// codec verifies structure.
const (
	blockRecordType     uint32 = 1
	tagNumber           uint32 = 1
	tagHash             uint32 = 2
	tagParentHash       uint32 = 3
	tagTimestamp        uint32 = 4
	tagTransactionCount uint32 = 5
	tagExtra            uint32 = 6
)

// Block holds the facts of one decided block as a test record carries them.
type Block struct {
	Number     uint64
	Hash       [32]byte
	ParentHash [32]byte
	Timestamp  uint64
	TxCount    uint32
	// Extra, when not nil, is one more bytes field of the record. A large
	// value makes the record span several fragments.
	Extra []byte
}

// Event is one built block event: its canonical fragments and its READY
// descriptors, in the shape of the BuiltEvent of the block event builder.
type Event struct {
	Fragments           [][]byte
	Descriptor          proto.EventDescriptor
	FragmentDescriptors []proto.FragmentDescriptor
}

// BuildBlockEvent builds the BlockApplied event of one decided block: family
// block, kind applied, at its ordinal in the batch. eventID is the event id
// the producer journal reserved for that ordinal.
func BuildBlockEvent(ordinal uint64, eventID proto.EventID, network proto.NetworkIdentity, block Block) (Event, error) {
	nodes := []codec.TreeNode{
		{Kind: codec.ValueMessage, TypeKey: blockRecordType, ChildCount: 5},
		{Kind: codec.ValueUint64, HasFieldTag: true, FieldTag: tagNumber, Uint64: block.Number},
		{Kind: codec.ValueBytes, HasFieldTag: true, FieldTag: tagHash, Value: block.Hash[:]},
		{Kind: codec.ValueBytes, HasFieldTag: true, FieldTag: tagParentHash, Value: block.ParentHash[:]},
		{Kind: codec.ValueUint64, HasFieldTag: true, FieldTag: tagTimestamp, Uint64: block.Timestamp},
		{Kind: codec.ValueUint32, HasFieldTag: true, FieldTag: tagTransactionCount, Uint32: block.TxCount},
	}
	if block.Extra != nil {
		nodes[0].ChildCount++
		nodes = append(nodes, codec.TreeNode{Kind: codec.ValueBytes, HasFieldTag: true, FieldTag: tagExtra, Value: block.Extra})
	}
	var fragments [][]byte
	if _, err := codec.BuildRecord(eventID, network, proto.FamilyBlock, proto.KindApplied, blockRecordType, nodes,
		func(_ uint64, fragment []byte) error {
			fragments = append(fragments, append([]byte(nil), fragment...))
			return nil
		}); err != nil {
		return Event{}, err
	}
	descriptor, descriptors, err := proto.BuildEventDescriptor(ordinal, eventID, proto.FamilyBlock, proto.KindApplied, fragments)
	if err != nil {
		return Event{}, err
	}
	return Event{Fragments: fragments, Descriptor: descriptor, FragmentDescriptors: descriptors}, nil
}

// Stage writes one READY batch into a producer journal as the collector
// does, in chunks of producer.ChunkBytes. See StageChunked.
func Stage(t testing.TB, store *producer.Store, decision uint64, network proto.NetworkIdentity, blocks ...Block) (uint64, producer.Ready) {
	t.Helper()
	return StageChunked(t, store, decision, producer.ChunkBytes, network, blocks...)
}

// StageChunked writes one READY batch into a producer journal: one
// decision, one block event per block, in attempt data and a manifest cut
// into chunks of at most chunkBytes. It answers the batch sequence and the
// READY marker. The store must already hold its destination binding.
func StageChunked(t testing.TB, store *producer.Store, decision uint64, chunkBytes int, network proto.NetworkIdentity, blocks ...Block) (uint64, producer.Ready) {
	t.Helper()
	att, replayed, err := store.AllocateBatch(decision, uint64(len(blocks)))
	if err != nil || replayed {
		t.Fatalf("AllocateBatch: att=%v replayed=%v err=%v", att, replayed, err)
	}
	att, err = store.StartAttempt(att.BatchSeq, uint64(len(blocks)))
	if err != nil {
		t.Fatal(err)
	}
	builder := codec.NewManifestBuilder()
	var data []byte
	for ordinal, block := range blocks {
		event, err := BuildBlockEvent(uint64(ordinal), proto.EventID(store.EventID(att.EventSequenceBase+uint64(ordinal))), network, block)
		if err != nil {
			t.Fatal(err)
		}
		if err := builder.AddEvent(event.Descriptor, event.FragmentDescriptors); err != nil {
			t.Fatal(err)
		}
		for _, fragment := range event.Fragments {
			data = append(data, fragment...)
		}
	}
	for offset := 0; offset < len(data); offset += chunkBytes {
		end := min(offset+chunkBytes, len(data))
		if err := store.WriteAttemptData(att.BatchSeq, att.Generation, uint64(offset), data[offset:end]); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := builder.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < len(manifest); offset += chunkBytes {
		end := min(offset+chunkBytes, len(manifest))
		if err := store.WriteAttemptManifest(att.BatchSeq, att.Generation, uint64(offset), manifest[offset:end]); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := store.CommitReady(att.BatchSeq, att.Generation, uint64(len(manifest)), []byte(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	return att.BatchSeq, ready
}

// Network returns the network identity of a test binding: chain id 97 of
// the Chapel testnet and a placeholder genesis hash, since no test talks to
// the chain.
func Network() proto.NetworkIdentity {
	var chainID [32]byte
	chainID[31] = 97
	return proto.NetworkIdentity{Family: proto.NetworkFamilyEVM, ChainID: chainID,
		GenesisHash: proto.Hash32{0x6d, 0x3c, 0x66}}
}

// Binding returns a valid destination binding of the stream the tracer
// bullet publishes to, with its computed descriptor digest.
func Binding(network proto.NetworkIdentity) destination.Binding {
	b := destination.Binding{
		DestinationID:   proto.ID16{0x7e, 0x0a, 0x5c, 0x93},
		Generation:      1,
		Network:         network,
		ExpectedAccount: "IDX_BSC_TESTNET",
		Stream:          "EXTRACTOR_BSC_TESTNET_V3",
		SubjectPrefix:   "evm.v3.bsc-testnet",
	}
	b.DescriptorDigest, _ = b.DescriptorDigestComputed()
	return b
}

// OpenStore opens a producer journal over st and saves the binding.
func OpenStore(t testing.TB, st storage.Storage, binding destination.Binding) *producer.Store {
	t.Helper()
	store, err := producer.Open(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetDestinationBinding(binding.DescriptorDigest); err != nil {
		t.Fatal(err)
	}
	return store
}

// Identity holds the TLS material of one mutual TLS link. The client side is
// the three files the live configuration names; the server side is a
// certificate the test loads itself.
type Identity struct {
	// CAFile, CertFile and KeyFile are the files of the client.
	CAFile, CertFile, KeyFile string
	// ServerCertFile and ServerKeyFile are the files of the gateway.
	ServerCertFile, ServerKeyFile string
	// OtherCertFile and OtherKeyFile belong to a client another CA
	// signed, for the refusal a wrong certificate must meet.
	OtherCertFile, OtherKeyFile string
	// CAPEM is the certificate of the CA.
	CAPEM []byte
}

// NewIdentity issues a test CA, a gateway certificate for the hosts and a
// client certificate, and writes the PEM files into a directory of the test.
func NewIdentity(t testing.TB, hosts ...string) Identity {
	t.Helper()
	dir := t.TempDir()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "extractor test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, BasicConstraintsValid: true, IsCA: true}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	// issue signs one leaf certificate with the CA of its choice.
	issue := func(name string, serial int64, usage x509.ExtKeyUsage, dnsNames []string, signer *x509.Certificate, signerKey *ecdsa.PrivateKey) ([]byte, *ecdsa.PrivateKey) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		// An IP host goes into the SANs of IP addresses, a name into the
		// DNS SANs: the endpoint host is verified against them.
		for _, host := range dnsNames {
			if ip := net.ParseIP(host); ip != nil {
				template.IPAddresses = append(template.IPAddresses, ip)
			} else {
				template.DNSNames = append(template.DNSNames, host)
			}
		}
		der, err := x509.CreateCertificate(rand.Reader, template, signer, &key.PublicKey, signerKey)
		if err != nil {
			t.Fatal(err)
		}
		return der, key
	}
	// The gateway and the client are signed by the CA; another client is
	// signed by another CA, so the gateway refuses it.
	otherCAKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherCA := &x509.Certificate{SerialNumber: big.NewInt(9), NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	otherCADER, err := x509.CreateCertificate(rand.Reader, otherCA, otherCA, &otherCAKey.PublicKey, otherCAKey)
	if err != nil {
		t.Fatal(err)
	}
	otherCA, err = x509.ParseCertificate(otherCADER)
	if err != nil {
		t.Fatal(err)
	}
	serverDER, serverKey := issue("bsc-testnet-gateway", 2, x509.ExtKeyUsageServerAuth, hosts, ca, caKey)
	clientDER, clientKey := issue("bsc-testnet-adapter", 3, x509.ExtKeyUsageClientAuth, nil, ca, caKey)
	otherDER, otherKey := issue("client-of-another-ca", 4, x509.ExtKeyUsageClientAuth, nil, otherCA, otherCAKey)
	// write stores one PEM block and answers its path.
	write := func(name, blockType string, der []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// keyFile stores one private key and answers its path.
	keyFile := func(name string, key *ecdsa.PrivateKey) string {
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return write(name, "EC PRIVATE KEY", der)
	}
	return Identity{
		CAFile: write("ca.pem", "CERTIFICATE", caDER), CAPEM: caDER,
		CertFile: write("client.pem", "CERTIFICATE", clientDER), KeyFile: keyFile("client.key", clientKey),
		ServerCertFile: write("gateway.pem", "CERTIFICATE", serverDER), ServerKeyFile: keyFile("gateway.key", serverKey),
		OtherCertFile: write("other.pem", "CERTIFICATE", otherDER), OtherKeyFile: keyFile("other.key", otherKey),
	}
}

// ServerTLS returns the TLS configuration of a gateway that requires a
// client certificate of the CA: the mutual TLS of the ingest link.
func (id Identity) ServerTLS(t testing.TB) *tls.Config {
	t.Helper()
	certificate, err := tls.LoadX509KeyPair(id.ServerCertFile, id.ServerKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: id.CAPEM})) {
		t.Fatal("the test CA is not a certificate")
	}
	return &tls.Config{Certificates: []tls.Certificate{certificate}, ClientCAs: pool,
		ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}}
}

// Endpoint returns the https endpoint of a listener of the test.
func Endpoint(listener net.Listener) config.Endpoint {
	return config.Endpoint{Raw: "https://" + listener.Addr().String(), Scheme: "https",
		Host: listener.Addr().(*net.TCPAddr).IP.String(), Port: strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)}
}

// Listen starts a loopback listener and closes it when the test ends.
func Listen(t testing.TB) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

// ErrCrash is the storage failure the spy answers once it was told to stop
// the process at a step.
var ErrCrash = fmt.Errorf("ingesttest: simulated crash")

// Spy wraps a storage and lets a test fail the writes of a driver from one
// durable step on, which is how a test stops a driver at that step.
type Spy struct {
	storage.Storage
	mu   sync.Mutex
	stop func(op string, key []byte) bool
	dead bool
}

// NewSpy wraps st.
func NewSpy(st storage.Storage) *Spy { return &Spy{Storage: st} }

// CrashWhen makes every write from the first one match accepts on fail with
// ErrCrash: the process stopped at that step and that write did not happen.
// A closing commit is one apply without a key.
func (s *Spy) CrashWhen(match func(op string, key []byte) bool) {
	s.mu.Lock()
	s.stop, s.dead = match, false
	s.mu.Unlock()
}

// Revive ends the simulated crash: writes pass again.
func (s *Spy) Revive() {
	s.mu.Lock()
	s.dead = false
	s.mu.Unlock()
}

// failing reports whether this write fails.
func (s *Spy) failing(op string, key []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return true
	}
	if s.stop != nil && s.stop(op, key) {
		s.dead = true
		return true
	}
	return false
}

// Put stores value under key, or fails as the spy was told.
func (s *Spy) Put(key, value []byte) error {
	if s.failing("put", key) {
		return ErrCrash
	}
	return s.Storage.Put(key, value)
}

// Delete removes key, or fails as the spy was told.
func (s *Spy) Delete(key []byte) error {
	if s.failing("delete", key) {
		return ErrCrash
	}
	return s.Storage.Delete(key)
}

// Apply applies the batch, or fails as the spy was told.
func (s *Spy) Apply(b *storage.Batch) error {
	if s.failing("apply", nil) {
		return ErrCrash
	}
	return s.Storage.Apply(b)
}
