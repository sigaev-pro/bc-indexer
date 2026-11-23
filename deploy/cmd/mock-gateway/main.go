// The mock gateway of the deployment stack: the production-shaped
// publication half of one network circuit. It loads the strict gateway
// YAML and the immutable DestinationBinding, verifies the NATS
// destination online (account, stream, subjects, limits and the two D77
// identity keys) before it admits any event, accepts only its own
// network's adapter over mutual TLS, publishes every frame under its
// exact saved id with the fixed header layout, and journals every
// confirmed PubAck.
//
// This binary is the pre-build stand-in for the real gateway service of
// E06-08: the transport is the mock wire of internal/wire instead of the
// pinned gRPC ingest protocol, and the payload of an event is a small
// JSON record instead of codec fragments. Every identity, route and
// delivery rule is the contract behavior (LIVE_CONFIG_SPEC, D52, D77,
// D54).
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sigaev-pro/bc-indexer/deploy/internal/deployconfig"
	"github.com/sigaev-pro/bc-indexer/deploy/internal/eventlog"
	"github.com/sigaev-pro/bc-indexer/deploy/internal/wire"
)

func main() {
	configPath := flag.String("config", "/config/gateway.yml", "path of the strict gateway YAML")
	flag.Parse()

	instance := fmt.Sprintf("gateway-%d", os.Getpid())
	exit := func(code int) { os.Exit(code) }
	_ = exit

	cfg, network, binding, log := mustSetup(instance, *configPath)

	// The journal is the durable publication record of this gateway.
	journal, err := openJournal(cfg.Storage.GatewayDir)
	if err != nil {
		log.Fatal("STORE_OPENED", 5, eventlog.Errorf(err))
	}
	log.Event("STORE_OPENED", map[string]any{"gateway_dir": cfg.Storage.GatewayDir})

	// NATS connection and the online destination check run in the
	// background: the ingest listener opens regardless, because a NATS
	// outage must not stop safe capture on the adapter side, and the
	// gateway keeps answering TRANSIENT refusals until the destination
	// verifies (publication alone stays blocked).
	pub := newPublisher(cfg, network, binding, log)
	go pub.connectLoop(context.Background())

	// The mTLS ingest listener.
	ln, err := listenIngest(cfg, network, log)
	if err != nil {
		log.Fatal("INIT_READY", 2, eventlog.Errorf(err))
	}
	log.Event("INIT_READY", map[string]any{"listen": cfg.Gateway.Listen})

	var wg sync.WaitGroup
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	wg.Add(1)
	go func() {
		defer wg.Done()
		serveIngest(ctx, ln, pub, journal, network, binding, log)
	}()

	<-ctx.Done()
	log.Event("STOPPED", map[string]any{"reason": "signal"})
	ln.Close()
	pub.close()
	journal.close()
	wg.Wait()
}

// mustSetup loads and cross-validates the gateway file, the network
// table and the binding descriptor: CONFIG_VALIDATED happens only when
// every identity agrees.
func mustSetup(instance, configPath string) (deployconfig.GatewayConfig, deployconfig.NetworkDefinition, deployconfig.Binding, *eventlog.Logger) {
	cfg, err := deployconfig.LoadGatewayConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "{\"event_code\":\"FATAL\",\"error\":%q}\n", err.Error())
		os.Exit(2)
	}
	network, err := deployconfig.ByProfile(cfg.Network.Profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "{\"event_code\":\"FATAL\",\"error\":%q}\n", err.Error())
		os.Exit(2)
	}
	log := eventlog.New(instance, string(network.Key), "gateway", cfg.Runtime.LogLevel)
	binding, err := deployconfig.LoadBindingFile(cfg.Destination.BindingFile)
	if err != nil {
		log.Fatal("CONFIG_VALIDATED", 2, eventlog.Errorf(err))
	}
	if err := binding.MatchesNetwork(network); err != nil {
		log.Fatal("CONFIG_VALIDATED", 2, eventlog.Errorf(err))
	}
	if cfg.Gateway.NATS.Stream != binding.Stream || cfg.Gateway.NATS.SubjectPrefix != binding.SubjectPrefix {
		log.Fatal("CONFIG_VALIDATED", 2, map[string]any{
			"error": "gateway stream or subject prefix differs from the binding descriptor",
		})
	}
	log.Event("CONFIG_VALIDATED", map[string]any{
		"profile": network.Profile, "binding_file": cfg.Destination.BindingFile,
		"stream": binding.Stream, "max_frame_bytes": cfg.Transport.MaxFrameBytes,
	})
	return cfg, network, binding, log
}

// journal appends one durable JSON line per confirmed publication.
type journal struct {
	mu sync.Mutex
	f  *os.File
}

func openJournal(dir string) (*journal, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "journal.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &journal{f: f}, nil
}

func (j *journal) record(entry map[string]any) error {
	entry["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.f.Write(append(line, '\n')); err != nil {
		return err
	}
	return j.f.Sync()
}

func (j *journal) close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.f.Close()
}

// publisher owns the NATS connection and the online destination check.
type publisher struct {
	cfg     deployconfig.GatewayConfig
	network deployconfig.NetworkDefinition
	binding deployconfig.Binding
	log     *eventlog.Logger

	mu       sync.Mutex
	nc       *nats.Conn
	js       nats.JetStreamContext
	verified bool
}

func newPublisher(cfg deployconfig.GatewayConfig, network deployconfig.NetworkDefinition, binding deployconfig.Binding, log *eventlog.Logger) *publisher {
	return &publisher{cfg: cfg, network: network, binding: binding, log: log}
}

// natsTLS builds the client TLS config from the strict YAML fields.
func natsTLS(cfg deployconfig.GatewayConfig) (*tls.Config, error) {
	u, err := url.Parse(cfg.Gateway.NATS.Server)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(cfg.Gateway.NATS.TLS.CAFile)
	if err != nil {
		return nil, fmt.Errorf("nats ca_file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("nats ca_file holds no certificate")
	}
	return &tls.Config{RootCAs: pool, ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}, nil
}

// readCredentials reads the NATS credentials file: the username on the
// first line, the password on the second.
func readCredentials(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("nats credentials_file: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return "", "", fmt.Errorf("nats credentials_file %s must hold the username and the password on two lines", path)
	}
	return lines[0], lines[1], nil
}

// connectLoop keeps the NATS connection alive and re-runs the online
// destination check after every fresh connection: an old success never
// carries over (CONTROL_WIRE_CONTRACT, "Stream metadata identity D77").
func (p *publisher) connectLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := p.connectOnce(ctx); err != nil {
			p.log.Event("TRANSPORT_RETRY", map[string]any{
				"class": "TRANSIENT", "target": p.cfg.Gateway.NATS.Server, "error": err.Error(),
			})
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}
}

func (p *publisher) connectOnce(ctx context.Context) error {
	tlsConf, err := natsTLS(p.cfg)
	if err != nil {
		p.log.Fatal("CONFIG_VALIDATED", 2, eventlog.Errorf(err))
	}
	user, pass, err := readCredentials(p.cfg.Gateway.NATS.CredentialsFile)
	if err != nil {
		p.log.Fatal("CONFIG_VALIDATED", 2, eventlog.Errorf(err))
	}
	nc, err := nats.Connect(p.cfg.Gateway.NATS.Server,
		nats.Secure(tlsConf),
		nats.UserInfo(user, pass),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.ReconnectHandler(func(*nats.Conn) {
			// A fresh connection requires a fresh destination check
			// before the next publication: an old success never carries
			// over (D77 phase table). Publication stays blocked until
			// the check verifies the target again.
			p.mu.Lock()
			p.verified = false
			p.mu.Unlock()
			p.log.Event("TRANSPORT_RETRY", map[string]any{"class": "TRANSIENT", "reason": "nats reconnect"})
			go p.recheckAfterReconnect()
		}),
	)
	if err != nil {
		return err
	}
	js, err := nc.JetStream(nats.Context(ctx))
	if err != nil {
		nc.Close()
		return err
	}
	p.mu.Lock()
	old := p.nc
	p.nc = nc
	p.js = js
	p.verified = false
	p.mu.Unlock()
	if old != nil {
		old.Close()
	}
	// The online destination check of this mock mirrors the phases of
	// the authoritative gateway/destination.Check: account identity,
	// stream identity, subjects, limits and both D77 keys.
	report := p.checkDestination(ctx)
	switch report.status {
	case statusVerified:
		p.mu.Lock()
		p.verified = true
		p.mu.Unlock()
		p.log.Event("DESTINATION_VERIFIED", map[string]any{
			"stream": p.binding.Stream, "account": p.binding.ExpectedAccount,
		})
		return stableWait(ctx, nc)
	case statusConfigBlocked:
		// Publication stops with named reasons; nothing is redirected or
		// dropped. Keep retrying the check in case the environment
		// repairs the target.
		p.log.Event("CONFIG_BLOCKED", map[string]any{"reasons": report.reasons})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
		return fmt.Errorf("destination check blocked")
	default:
		return fmt.Errorf("destination unavailable: %v", report.reasons)
	}
}

// stableWait blocks while the connection stays up, so connectLoop does
// not spin.
func stableWait(ctx context.Context, nc *nats.Conn) error {
	status := nc.Status()
	for status == nats.CONNECTED || status == nats.CONNECTING || status == nats.RECONNECTING {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-nc.StatusChanged():
		}
	}
	return fmt.Errorf("connection lost")
}

// recheckAfterReconnect runs the destination check after a NATS
// auto-reconnect and restores the publication admission only on a
// verified target (CONFIG_BLOCKED keeps publication stopped with named
// reasons, an unavailable target is retried).
func (p *publisher) recheckAfterReconnect() {
	report := p.checkDestination(context.Background())
	p.mu.Lock()
	p.verified = report.status == statusVerified
	p.mu.Unlock()
	switch report.status {
	case statusVerified:
		p.log.Event("DESTINATION_VERIFIED", map[string]any{
			"stream": p.binding.Stream, "account": p.binding.ExpectedAccount,
			"after": "reconnect",
		})
	case statusConfigBlocked:
		p.log.Event("CONFIG_BLOCKED", map[string]any{"reasons": report.reasons, "after": "reconnect"})
	}
}

const (
	statusVerified = iota
	statusConfigBlocked
	statusUnavailable
)

type destReport struct {
	status  int
	reasons []string
}

// checkDestination performs the mock's own online check. The bootstrap
// tool runs the authoritative destination.Check against the same
// stream; this check is the runtime admission of the mock gateway.
func (p *publisher) checkDestination(ctx context.Context) destReport {
	p.mu.Lock()
	nc, js := p.nc, p.js
	p.mu.Unlock()
	if nc == nil || js == nil {
		return destReport{status: statusUnavailable, reasons: []string{"no connection"}}
	}
	// 1. Account identity: the exact account field of UserInfo.
	var probe struct {
		Account string `json:"account"`
		Data    struct {
			Account string `json:"account"`
		} `json:"data"`
	}
	if resp, err := nc.RequestWithContext(ctx, "$SYS.REQ.USER.INFO", []byte("{}")); err != nil {
		return destReport{status: statusUnavailable, reasons: []string{fmt.Sprintf("user info: %v", err)}}
	} else {
		if err := json.Unmarshal(resp.Data, &probe); err != nil {
			return destReport{status: statusConfigBlocked, reasons: []string{fmt.Sprintf("user info answer: %v", err)}}
		}
	}
	account := probe.Account
	if account == "" {
		account = probe.Data.Account
	}
	if account != p.binding.ExpectedAccount {
		return destReport{status: statusConfigBlocked, reasons: []string{fmt.Sprintf(
			"connection account %q differs from the binding account %q", account, p.binding.ExpectedAccount)}}
	}
	// 2..5. Stream identity, subjects, limits and the D77 keys.
	info, err := js.StreamInfo(p.binding.Stream, nats.Context(ctx))
	if err != nil {
		return destReport{status: statusUnavailable, reasons: []string{fmt.Sprintf("stream info: %v", err)}}
	}
	var reasons []string
	if info.Config.Name != p.binding.Stream {
		reasons = append(reasons, fmt.Sprintf("stream answered as %q", info.Config.Name))
	}
	for _, subject := range p.network.EventSubjects() {
		covered := false
		for _, pat := range info.Config.Subjects {
			if subjectMatches(pat, subject) {
				covered = true
				break
			}
		}
		if !covered {
			reasons = append(reasons, fmt.Sprintf("subjects %v do not cover %q", info.Config.Subjects, subject))
		}
	}
	if info.Config.MaxMsgSize != 0 && uint64(info.Config.MaxMsgSize) < p.cfg.Transport.MaxFrameBytes {
		reasons = append(reasons, fmt.Sprintf("max message size %d is below the frame budget %d",
			info.Config.MaxMsgSize, p.cfg.Transport.MaxFrameBytes))
	}
	wantDigest := p.binding.NetworkDigestHex()
	wantDest := p.binding.DestinationIDHex()
	meta := info.Config.Metadata
	for key, want := range map[string]string{
		"extractor.network_digest": wantDigest,
		"extractor.destination_id": wantDest,
	} {
		got, ok := meta[key]
		if !ok || got == "" {
			reasons = append(reasons, fmt.Sprintf("stream metadata key %q is missing", key))
			continue
		}
		if got != want {
			reasons = append(reasons, fmt.Sprintf("stream metadata key %q is %q, want %q", key, got, want))
		}
	}
	if len(reasons) > 0 {
		return destReport{status: statusConfigBlocked, reasons: reasons}
	}
	return destReport{status: statusVerified}
}

// subjectMatches implements the NATS token wildcard rules for the
// coverage check.
func subjectMatches(pattern, subject string) bool {
	pt := strings.Split(pattern, ".")
	st := strings.Split(subject, ".")
	for i, tok := range pt {
		if tok == ">" {
			return i <= len(st)
		}
		if i >= len(st) {
			return false
		}
		if tok != "*" && tok != st[i] {
			return false
		}
	}
	return len(pt) == len(st)
}

// close shuts the NATS connection down.
func (p *publisher) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.nc != nil {
		p.nc.Close()
	}
}

// publish publishes one event under its exact id and verifies the ack.
func (p *publisher) publish(ctx context.Context, ev wire.Event, producerID, runID string, log *eventlog.Logger) (wire.Ack, string) {
	p.mu.Lock()
	js, verified := p.js, p.verified
	p.mu.Unlock()
	if js == nil || !verified {
		return wire.Ack{}, "TRANSIENT"
	}
	networkDigest, err := wire.Digest32Hex(p.binding.NetworkDigestHex())
	if err != nil {
		return wire.Ack{}, "CONFIG_BLOCKED"
	}
	producer, err := wire.ID16Hex(producerID)
	if err != nil {
		return wire.Ack{}, "CONFIG_BLOCKED"
	}
	run, err := wire.ID16Hex(runID)
	if err != nil {
		return wire.Ack{}, "CONFIG_BLOCKED"
	}
	msgID := wire.NATSMsgID(networkDigest, producer, run, ev.EventSeq)
	headers := len(wire.HeaderBytes(msgID, p.binding.Stream)) + len(ev.Payload)
	if uint64(headers) > p.cfg.Transport.MaxFrameBytes {
		return wire.Ack{}, "LIMIT_INCOMPATIBLE"
	}
	subject := p.binding.SubjectPrefix + "." + ev.Route
	msg := nats.NewMsg(subject)
	msg.Data = ev.Payload
	msg.Header.Set("Nats-Msg-Id", msgID)
	msg.Header.Set("Nats-Expected-Stream", p.binding.Stream)
	ack, err := js.PublishMsg(msg, nats.Context(ctx))
	if err != nil {
		if errorsConfigClass(err) {
			return wire.Ack{}, "CONFIG_BLOCKED"
		}
		return wire.Ack{}, "TRANSIENT"
	}
	if ack == nil || ack.Stream != p.binding.Stream {
		return wire.Ack{}, "CONFIG_BLOCKED"
	}
	log.Debug("BATCH_DURABLE", map[string]any{"event_seq": ev.EventSeq, "subject": subject})
	return wire.Ack{
		Type: "ack", EventSeq: ev.EventSeq, Stream: ack.Stream,
		Sequence: ack.Sequence, Duplicate: ack.Duplicate, MsgID: msgID,
	}, ""
}

// errorsConfigClass separates named destination errors from the
// transient class: an unreachable or timed-out server keeps the event
// pending; an expected-stream or permission violation names a
// configuration problem of the target.
func errorsConfigClass(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, nats.ErrNoResponders) || errors.Is(err, nats.ErrTimeout) ||
		errors.Is(err, nats.ErrConnectionClosed) {
		return false
	}
	switch {
	case strings.Contains(err.Error(), "expected stream"),
		strings.Contains(err.Error(), "permissions"),
		strings.Contains(err.Error(), "account authentication expired"):
		return true
	}
	return false
}

// listenIngest opens the mTLS listener and enforces the client
// identity: only the adapter of this network connects, a cross-network
// or cross-role certificate is rejected during the handshake, before
// any state changes (DEPLOYMENT_BOOTSTRAP_CONTRACT, "Trust bootstrap").
func listenIngest(cfg deployconfig.GatewayConfig, network deployconfig.NetworkDefinition, log *eventlog.Logger) (net.Listener, error) {
	u, err := url.Parse(cfg.Gateway.Listen)
	if err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(cfg.Gateway.TLS.CertFile, cfg.Gateway.TLS.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("gateway tls material: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.Gateway.TLS.CAFile)
	if err != nil {
		return nil, fmt.Errorf("gateway tls ca_file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("gateway tls ca_file holds no certificate")
	}
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("ingest: no client certificate")
			}
			cn := cs.PeerCertificates[0].Subject.CommonName
			if cn != network.AdapterIdentity() {
				return fmt.Errorf("ingest: client identity %q is not %q", cn, network.AdapterIdentity())
			}
			return nil
		},
	}
	addr := u.Host
	ln, err := tls.Listen("tcp", addr, tlsConf)
	if err != nil {
		return nil, err
	}
	log.Event("INIT_PHASE", map[string]any{"phase": "LISTEN", "addr": addr})
	return ln, nil
}

// serveIngest accepts adapter sessions until the context stops.
func serveIngest(ctx context.Context, ln net.Listener, pub *publisher, journal *journal, network deployconfig.NetworkDefinition, binding deployconfig.Binding, log *eventlog.Logger) {
	var wg sync.WaitGroup
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Event("TRANSPORT_RETRY", map[string]any{"class": "TRANSIENT", "error": err.Error()})
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			serveConn(ctx, conn, pub, journal, network, binding, log)
		}()
	}
	wg.Wait()
}

// serveConn serves one adapter session.
func serveConn(ctx context.Context, conn net.Conn, pub *publisher, journal *journal, network deployconfig.NetworkDefinition, binding deployconfig.Binding, log *eventlog.Logger) {
	defer conn.Close()
	if tc, ok := conn.(*tls.Conn); ok {
		if err := tc.HandshakeContext(ctx); err != nil {
			// A failed handshake is transport noise (for example a
			// port probe); the peer never reached the protocol, so no
			// configuration statement is logged.
			log.Debug("TRANSPORT_RETRY", map[string]any{
				"class": "TRANSIENT", "detail": fmt.Sprintf("ingest handshake: %v", err)})
			return
		}
	}
	peer := conn.RemoteAddr().String()
	var hello wire.Hello
	if err := wire.Read(conn, &hello); err != nil {
		log.Event("CONFIG_BLOCKED", map[string]any{
			"reasons": []string{fmt.Sprintf("session hello: %v", err)}})
		return
	}
	if hello.Protocol != wire.Protocol || hello.Role != "adapter" {
		refuse(conn, "PROTOCOL", "unsupported hello")
		return
	}
	if hello.NetworkProfile != network.Profile {
		refuse(conn, "NETWORK", fmt.Sprintf("profile %q is not %q", hello.NetworkProfile, network.Profile))
		return
	}
	if hello.BindingDigest != fmt.Sprintf("%x", binding.DescriptorDigest[:]) {
		refuse(conn, "BINDING", "binding digest differs from the gateway binding")
		return
	}
	var epoch [16]byte
	rand.Read(epoch[:])
	if err := wire.Write(conn, wire.HelloAck{
		Type: "hello_ack", NetworkProfile: network.Profile,
		BindingDigest: hello.BindingDigest, GatewayEpoch: hex.EncodeToString(epoch[:]),
	}); err != nil {
		return
	}
	log.Debug("SESSION_OPENED", map[string]any{"peer": peer, "producer_id": hello.ProducerID, "run_id": hello.RunID})
	for {
		if ctx.Err() != nil {
			return
		}
		var ev wire.Event
		if err := wire.Read(conn, &ev); err != nil {
			if ctx.Err() == nil {
				log.Debug("SESSION_CLOSED", map[string]any{"peer": peer, "error": err.Error()})
			}
			return
		}
		if ev.Type != "event" || ev.Route == "" {
			refuseEvent(conn, ev.EventSeq, "PROTOCOL", "malformed event")
			continue
		}
		ack, class := pub.publish(ctx, ev, hello.ProducerID, hello.RunID, log)
		switch class {
		case "":
			if err := journal.record(map[string]any{
				"event_code": "DELIVERY_CONFIRMED", "stream": ack.Stream,
				"subject":  binding.SubjectPrefix + "." + ev.Route,
				"sequence": ack.Sequence, "msg_id": ack.MsgID,
				"producer_id": hello.ProducerID, "run_id": hello.RunID,
				"event_seq": ev.EventSeq, "duplicate": ack.Duplicate,
			}); err != nil {
				log.Fatal("FATAL", 5, eventlog.Errorf(err))
			}
			log.Event("DELIVERY_CONFIRMED", map[string]any{
				"event_seq": ev.EventSeq, "stream": ack.Stream, "sequence": ack.Sequence,
				"subject": binding.SubjectPrefix + "." + ev.Route, "duplicate": ack.Duplicate,
			})
			wire.Write(conn, ack)
		case "TRANSIENT":
			log.Event("TRANSPORT_RETRY", map[string]any{
				"class": "TRANSIENT", "event_seq": ev.EventSeq})
			refuseEvent(conn, ev.EventSeq, "TRANSIENT", "destination unavailable, keep pending")
		default:
			log.Event("CONFIG_BLOCKED", map[string]any{
				"class": class, "event_seq": ev.EventSeq})
			refuseEvent(conn, ev.EventSeq, class, "publication refused")
		}
	}
}

// refuse answers one refused session.
func refuse(conn net.Conn, code, detail string) {
	wire.Write(conn, wire.ErrorRefuse{Type: "error", Code: code, Detail: detail})
}

// refuseEvent answers one refused event.
func refuseEvent(conn net.Conn, seq uint64, code, detail string) {
	wire.Write(conn, wire.Nak{Type: "nak", EventSeq: seq, Code: code, Detail: detail})
}
