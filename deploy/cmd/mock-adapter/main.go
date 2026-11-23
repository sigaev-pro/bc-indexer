// The mock adapter of the deployment stack: the production-shaped
// capture half of one network circuit. It loads the strict live YAML
// and the immutable DestinationBinding, opens the durable WAL spool,
// replays pending events before any new capture, fixes the live
// boundary H and captures from H+1, and keeps capturing durably while
// the gateway or NATS is down: WAL_SAVED is declared only after the
// fsync, DELIVERY_CONFIRMED only after the ack (LIVE_CONFIG_SPEC,
// "Состояние и запуск"; D28/D57).
//
// This binary is the pre-build stand-in for the native client with the
// embedded extractor adapter of E06: the mock produces small JSON
// records instead of normalized codec fragments, on the mock wire of
// internal/wire instead of the pinned gRPC control protocol.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sigaev-pro/bc-indexer/deploy/internal/deployconfig"
	"github.com/sigaev-pro/bc-indexer/deploy/internal/eventlog"
	"github.com/sigaev-pro/bc-indexer/deploy/internal/walspool"
	"github.com/sigaev-pro/bc-indexer/deploy/internal/wire"
)

func main() {
	configPath := flag.String("config", "/config/live.yml", "path of the strict live YAML")
	mode := flag.String("extractor-mode", "live", "live captures from H+1; drain replays pending and exits after durable DRAINED")
	flag.Parse()

	cfg, err := deployconfig.LoadLiveConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "{\"event_code\":\"FATAL\",\"error\":%q}\n", err.Error())
		os.Exit(2)
	}
	network, err := deployconfig.ByProfile(cfg.Network.Profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "{\"event_code\":\"FATAL\",\"error\":%q}\n", err.Error())
		os.Exit(2)
	}
	log := eventlog.New(fmt.Sprintf("adapter-%d", os.Getpid()), string(network.Key), "adapter", cfg.Runtime.LogLevel)
	binding, err := deployconfig.LoadBindingFile(cfg.Destination.BindingFile)
	if err != nil {
		log.Fatal("CONFIG_VALIDATED", 2, eventlog.Errorf(err))
	}
	if err := binding.MatchesNetwork(network); err != nil {
		log.Fatal("CONFIG_VALIDATED", 2, eventlog.Errorf(err))
	}
	log.Event("CONFIG_VALIDATED", map[string]any{
		"profile": network.Profile, "native_kind": cfg.Native.Kind,
		"binding_file":     cfg.Destination.BindingFile,
		"gateway_endpoint": cfg.Delivery.GatewayEndpoint,
	})
	if *mode != "live" && *mode != "drain" {
		log.Fatal("CONFIG_VALIDATED", 2, map[string]any{"error": fmt.Sprintf("unknown extractor mode %q", *mode)})
	}

	spool, err := walspool.Open(cfg.Storage.LiveDir)
	if err != nil {
		log.Fatal("STORE_OPENED", 5, eventlog.Errorf(err))
	}
	log.Event("STORE_OPENED", map[string]any{
		"live_dir":    cfg.Storage.LiveDir,
		"producer_id": spool.ProducerID(), "run_id": spool.RunID(),
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	drive := newDriver(cfg, network, binding, spool, log)
	switch *mode {
	case "drain":
		drive.drain(ctx)
	case "live":
		drive.live(ctx)
	}
}

// driver connects one spool to one gateway with retry and backoff.
type driver struct {
	cfg     deployconfig.LiveConfig
	network deployconfig.NetworkDefinition
	binding deployconfig.Binding
	spool   *walspool.Spool
	log     *eventlog.Logger

	dialMu sync.Mutex
	conn   net.Conn
}

func newDriver(cfg deployconfig.LiveConfig, network deployconfig.NetworkDefinition, binding deployconfig.Binding, spool *walspool.Spool, log *eventlog.Logger) *driver {
	return &driver{cfg: cfg, network: network, binding: binding, spool: spool, log: log}
}

// connect dials the gateway with mTLS, verifies the server identity and
// opens one session. It returns nil while the gateway is unreachable so
// capture continues into the WAL.
func (d *driver) connect(ctx context.Context) net.Conn {
	u, err := url.Parse(d.cfg.Delivery.GatewayEndpoint)
	if err != nil {
		d.log.Fatal("CONFIG_VALIDATED", 2, eventlog.Errorf(err))
	}
	cert, err := tls.LoadX509KeyPair(d.cfg.Delivery.TLS.CertFile, d.cfg.Delivery.TLS.KeyFile)
	if err != nil {
		d.log.Fatal("CONFIG_VALIDATED", 2, eventlog.Errorf(fmt.Errorf("adapter tls material: %w", err)))
	}
	caPEM, err := os.ReadFile(d.cfg.Delivery.TLS.CAFile)
	if err != nil {
		d.log.Fatal("CONFIG_VALIDATED", 2, eventlog.Errorf(fmt.Errorf("adapter tls ca_file: %w", err)))
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		d.log.Fatal("CONFIG_VALIDATED", 2, map[string]any{"error": "adapter tls ca_file holds no certificate"})
	}
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   u.Hostname(),
		MinVersion:   tls.VersionTLS12,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("gateway presented no certificate")
			}
			cn := cs.PeerCertificates[0].Subject.CommonName
			if cn != d.network.GatewayIdentity() {
				return fmt.Errorf("gateway identity %q is not %q", cn, d.network.GatewayIdentity())
			}
			return nil
		},
	}
	dialer := &tls.Dialer{Config: tlsConf, NetDialer: &net.Dialer{Timeout: 5 * time.Second}}
	conn, err := dialer.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		d.log.Event("TRANSPORT_RETRY", map[string]any{
			"class": "TRANSIENT", "target": u.Host, "error": err.Error()})
		return nil
	}
	hello := wire.Hello{
		Type: "hello", Protocol: wire.Protocol, Role: "adapter",
		NetworkProfile: d.network.Profile,
		ProducerID:     d.spool.ProducerID(),
		RunID:          d.spool.RunID(),
		BindingDigest:  hex.EncodeToString(d.binding.DescriptorDigest[:]),
	}
	if err := wire.Write(conn, hello); err != nil {
		conn.Close()
		d.log.Event("TRANSPORT_RETRY", map[string]any{"class": "TRANSIENT", "error": err.Error()})
		return nil
	}
	var ack wire.HelloAck
	if err := wire.Read(conn, &ack); err != nil {
		conn.Close()
		d.log.Event("CONFIG_BLOCKED", map[string]any{
			"reasons": []string{fmt.Sprintf("session refused: %v", err)}})
		return nil
	}
	if ack.BindingDigest != hello.BindingDigest || ack.NetworkProfile != d.network.Profile {
		conn.Close()
		d.log.Event("CONFIG_BLOCKED", map[string]any{
			"reasons": []string{"gateway session identity differs from the binding"}})
		return nil
	}
	d.log.Event("SOURCE_SYNCING", map[string]any{
		"gateway": u.Host, "gateway_epoch": ack.GatewayEpoch})
	return conn
}

// withGateway runs fn with one healthy gateway session, dialing with
// bounded backoff while fn reports a transient failure.
func (d *driver) withGateway(ctx context.Context, fn func(ctx context.Context, conn net.Conn) bool) {
	backoff := time.Second
	const maxBackoff = 15 * time.Second
	for ctx.Err() == nil {
		conn := d.connect(ctx)
		if conn != nil {
			if fn(ctx, conn) {
				conn.Close()
				return
			}
			conn.Close()
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
			if backoff < maxBackoff {
				backoff *= 2
			}
		}
	}
}

// sendPending delivers the pending suffix of the WAL to the gateway,
// oldest event first, and persists the acknowledged watermark. A
// transient refusal keeps the event pending and lets the caller retry;
// the return value reports whether the suffix fully drained.
func (d *driver) sendPending(ctx context.Context, conn net.Conn) (bool, string) {
	pending, err := d.spool.Pending()
	if err != nil {
		d.log.Fatal("RECOVERY_STARTED", 5, eventlog.Errorf(err))
	}
	for _, ev := range pending {
		if ctx.Err() != nil {
			return false, "canceled"
		}
		wireEvent := wire.Event{
			Type: "event", EventSeq: ev.ID, Height: ev.Height,
			Route: ev.Route, Payload: ev.Payload,
		}
		if err := wire.Write(conn, wireEvent); err != nil {
			return false, "TRANSIENT"
		}
		var answer struct {
			Type     string `json:"type"`
			EventSeq uint64 `json:"event_seq"`
			Stream   string `json:"stream"`
			Code     string `json:"code"`
			Detail   string `json:"detail"`
		}
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		if err := wire.Read(conn, &answer); err != nil {
			return false, "TRANSIENT"
		}
		conn.SetReadDeadline(time.Time{})
		switch answer.Type {
		case "ack":
			if err := d.spool.Ack(answer.EventSeq); err != nil {
				d.log.Fatal("RECOVERY_STARTED", 5, eventlog.Errorf(err))
			}
			d.log.Event("DELIVERY_CONFIRMED", map[string]any{
				"event_seq": answer.EventSeq, "stream": answer.Stream,
				"replayed": true, "duplicate": false,
			})
		case "nak":
			if answer.Code == "TRANSIENT" {
				return false, "TRANSIENT"
			}
			// CONFIG_BLOCKED and LIMIT_INCOMPATIBLE stop the publication
			// of this event with a named reason; the event stays pending
			// and nothing is dropped or rewritten.
			d.log.Event("CONFIG_BLOCKED", map[string]any{
				"class": answer.Code, "event_seq": answer.EventSeq, "detail": answer.Detail})
			return false, answer.Code
		default:
			return false, "PROTOCOL"
		}
	}
	return true, ""
}

// drain implements --extractor-mode=drain: replay the pending suffix,
// then exit after the durable DRAINED state (D57).
func (d *driver) drain(ctx context.Context) {
	pending, err := d.spool.Pending()
	if err != nil {
		d.log.Fatal("DRAIN_STARTED", 5, eventlog.Errorf(err))
	}
	d.log.Event("DRAIN_STARTED", map[string]any{"pending": len(pending)})
	d.withGateway(ctx, func(ctx context.Context, conn net.Conn) bool {
		done, class := d.sendPending(ctx, conn)
		if done {
			return true
		}
		if class == "TRANSIENT" || class == "canceled" {
			return false
		}
		// A permanent refusal keeps the pending bytes in place and stops
		// the drain attempt without deleting anything.
		return true
	})
	left, _ := d.spool.Pending()
	d.log.Event("DRAIN_COMPLETED", map[string]any{"pending": len(left)})
	d.log.Event("STOPPED", map[string]any{"mode": "drain"})
}

// live implements the native automaton of the mock: recovery first,
// then the boundary fix and capture from H+1.
func (d *driver) live(ctx context.Context) {
	pending, err := d.spool.Pending()
	if err != nil {
		d.log.Fatal("RECOVERY_STARTED", 5, eventlog.Errorf(err))
	}
	d.log.Event("RECOVERY_STARTED", map[string]any{"pending": len(pending)})
	// Pending delivery never waits for a new live sync: if the gateway
	// is reachable the suffix drains before capture starts.
	d.withGateway(ctx, func(ctx context.Context, conn net.Conn) bool {
		done, class := d.sendPending(ctx, conn)
		return done || class != "TRANSIENT" && class != "canceled"
	})
	left, _ := d.spool.Pending()
	d.log.Event("RECOVERY_COMPLETED", map[string]any{"pending": len(left)})

	// The live boundary: a new observation run fixes the current head H
	// and captures from H+1 (D39). The mock head is the persisted height.
	H := d.spool.Height()
	d.log.Event("LIVE_BOUNDARY", map[string]any{"H": H})
	d.log.Event("CAPTURE_STARTED", map[string]any{"from": H + 1})

	interval, err := time.ParseDuration(d.network.BlockInterval)
	if err != nil {
		d.log.Fatal("CAPTURE_STARTED", 2, eventlog.Errorf(err))
	}
	generate := newGenerator(d.network)

	var mu sync.Mutex
	conn := d.connect(ctx)
	defer func() {
		mu.Lock()
		if conn != nil {
			conn.Close()
		}
		mu.Unlock()
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var backoff time.Duration
	for {
		select {
		case <-ctx.Done():
			d.log.Event("STOPPED", map[string]any{"reason": "signal"})
			return
		case <-ticker.C:
		}
		events := generate.block(H + 1)
		// WAL_SAVED only after the fsync of every event of the block.
		for _, ev := range events {
			if _, err := d.spool.Append(ev.height, ev.route, ev.payload); err != nil {
				d.log.Fatal("FATAL", 5, eventlog.Errorf(err))
			}
		}
		d.log.Event("WAL_SAVED", map[string]any{
			"H": H + 1, "events": len(events),
			"pending": true,
		})
		H++

		mu.Lock()
		connected := conn != nil
		mu.Unlock()
		if !connected {
			if backoff == 0 {
				backoff = time.Second
			}
			select {
			case <-ctx.Done():
				continue
			case <-time.After(backoff):
				if backoff < 15*time.Second {
					backoff *= 2
				}
			}
			mu.Lock()
			conn = d.connect(ctx)
			mu.Unlock()
			if conn == nil {
				continue
			}
		}
		// Deliver the unacknowledged suffix over the current session.
		mu.Lock()
		done, class := d.sendPending(ctx, conn)
		if !done && class == "TRANSIENT" {
			conn.Close()
			conn = nil
		}
		mu.Unlock()
		if done {
			d.log.Event("DELIVERY_CONFIRMED", map[string]any{"H": H, "events": len(events)})
			backoff = 0
		}
	}
}

// mockEvent is one generated event of the mock capture.
type mockEvent struct {
	height  uint64
	route   string
	payload []byte
}

// generator produces deterministic-looking mock records for one
// network.
type generator struct {
	network deployconfig.NetworkDefinition
}

func newGenerator(n deployconfig.NetworkDefinition) *generator {
	return &generator{network: n}
}

// block builds the events of one mock block: one block.applied record
// and a small random number of tx.applied records.
func (g *generator) block(height uint64) []mockEvent {
	var out []mockEvent
	blockPayload, _ := json.Marshal(map[string]any{
		"schema": "mock.v1", "network": g.network.Profile,
		"kind": "block.applied", "height": height,
		"block_hash": mockHash(height, "block"),
	})
	out = append(out, mockEvent{height: height, route: "block.applied", payload: blockPayload})
	txs := 1 + int(mockRandom(3))
	for i := 0; i < txs; i++ {
		payload, _ := json.Marshal(map[string]any{
			"schema": "mock.v1", "network": g.network.Profile,
			"kind": "tx.applied", "height": height,
			"tx_index": i, "tx_hash": mockHash(height, fmt.Sprintf("tx-%d", i)),
			"from_addr": mockHash(height, fmt.Sprintf("from-%d", i))[:42],
			"to_addr":   mockHash(height, fmt.Sprintf("to-%d", i))[:42],
			"value":     new(big.Int).SetUint64(mockRandom(1 << 40)).String(),
			"status":    "applied",
		})
		out = append(out, mockEvent{height: height, route: "tx.applied", payload: payload})
	}
	return out
}

// mockHash derives a stable 0x-prefixed 64-hex string from a seed.
func mockHash(height uint64, salt string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%s", "mock", height, salt)))
	return "0x" + hex.EncodeToString(sum[:])
}

// mockRandom returns a crypto-random uint64 below max.
func mockRandom(max uint64) uint64 {
	if max == 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0
	}
	return v.Uint64()
}

// unused guards imports used only by future transport modes.
var _ = strings.TrimSpace
