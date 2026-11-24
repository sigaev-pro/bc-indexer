// Command webhook-gateway is the Bonpay webhook gateway: a standalone
// service that turns extractor NATS events into signed webhook
// notifications with N-confirmations and reorg handling.
//
// It consumes every stream whose subjects intersect extractor.> (one
// durable consumer per stream, like the ClickHouse sink), keeps a
// per-network block-hash window in SQLite, and delivers webhook
// notifications when events reach the configured confirmation depth. On a
// detected reorg it notifies the affected subscriptions: one reorg
// notification for the replaced range, event_removed for pending events
// that never re-appeared, and event_invalidated — the "event became bad"
// notification — for events that were already delivered.
//
// Environment: NATS_URL, WEBHOOK_CONFIG (or --config), WEBHOOK_STATE,
// GATEWAY_ID (the durable-consumer prefix). The config file is reloaded
// on SIGHUP; the state database prunes to a 2000-height window.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// config is the environment contract of the service.
type config struct {
	NatsURL    string
	ConfigPath string
	StatePath  string
	GatewayID  string
}

func loadConfig() config {
	cfgPath := flag.String("config", "", "path to the subscriptions YAML (overrides WEBHOOK_CONFIG)")
	flag.Parse()
	c := config{
		NatsURL:    envString("NATS_URL", "nats://localhost:4222"),
		ConfigPath: envString("WEBHOOK_CONFIG", "./webhook-gateway.yml"),
		StatePath:  envString("WEBHOOK_STATE", "./webhook-gateway.db"),
		GatewayID:  envString("GATEWAY_ID", "webhook-gateway"),
	}
	if *cfgPath != "" {
		c.ConfigPath = *cfgPath
	}
	return c
}

func envString(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	cfg := loadConfig()

	subs, err := loadSubscriptions(cfg.ConfigPath)
	if err != nil {
		log.Fatalf("CONFIG_INVALID error=%v", err)
	}
	reg := newRegistry(subs)

	db, err := openState(cfg.StatePath)
	if err != nil {
		log.Fatalf("STATE_UNAVAILABLE error=%v", err)
	}
	eng := newEngine(db, reg, defaultWindow)
	disp := newDispatcher(db, reg)
	eng.SetWake(disp.Wake)

	// SIGTERM/SIGINT stop the service; SIGHUP reloads the config.
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)

	nc, err := connectNATS(ctx, cfg)
	if err != nil {
		db.Close()
		log.Fatalf("NATS_UNAVAILABLE error=%v", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		db.Close()
		log.Fatalf("NATS_JETSTREAM_ERROR error=%v", err)
	}

	gw := &gateway{cfg: cfg, reg: reg, engine: eng, disp: disp}
	gw.streams = newConsumerSet(cfg.GatewayID, js, func(m jetstream.Msg) {
		gw.handleMessage(ctx, m)
	})
	if err := gw.streams.initialScan(ctx); err != nil {
		nc.Drain()
		db.Close()
		log.Fatalf("NATS_STREAM_ERROR error=%v", err)
	}
	go gw.streams.runScanLoop(ctx)

	go disp.Run(ctx)
	go pruneLoop(ctx, eng)
	log.Printf("GATEWAY_READY nats=%s config=%s state=%s subscriptions=%d window=%d",
		cfg.NatsURL, cfg.ConfigPath, cfg.StatePath, len(reg.Snapshot()), defaultWindow)

	for {
		select {
		case <-ctx.Done():
			log.Printf("STOPPING reason=signal")
			gw.streams.drainAll()
			// Give the in-flight handlers a moment to commit and ack.
			time.Sleep(2 * time.Second)
			nc.Drain()
			db.Close()
			log.Printf("GATEWAY_STOPPED")
			return
		case <-hup:
			gw.reloadConfig(ctx)
		}
	}
}

// gateway bundles the long-lived parts the signal loop touches.
type gateway struct {
	cfg    config
	reg    *registry
	engine *engine
	disp   *dispatcher

	streams *consumerSet
}

// handleMessage is the per-message entry: parse, record durably, ack.
// A message whose subject or body cannot be understood is terminated (no
// redelivery for this consumer — the body stays in the stream and in the
// logs); a state failure leaves it unacked so JetStream replays it.
func (g *gateway) handleMessage(ctx context.Context, m jetstream.Msg) {
	ev, err := parseMessage(m.Subject(), m.Data(), time.Now().UTC())
	if err != nil {
		log.Printf("EVENT_REJECTED subject=%s error=%v", m.Subject(), err)
		_ = m.Term()
		return
	}
	if _, err := g.engine.ApplyEvent(ctx, ev); err != nil {
		log.Printf("STATE_ERROR subject=%s error=%v (unacked; redelivery follows)", m.Subject(), err)
		return
	}
	if err := m.Ack(); err != nil {
		log.Printf("ACK_FAILED subject=%s error=%v", m.Subject(), err)
	}
}

// reloadConfig applies a SIGHUP: load, validate, swap, rematch, drop the
// pending work of removed subscriptions. A broken file keeps the running
// set untouched.
func (g *gateway) reloadConfig(ctx context.Context) {
	subs, err := loadSubscriptions(g.cfg.ConfigPath)
	if err != nil {
		log.Printf("CONFIG_ERROR error=%v (keeping the running set)", err)
		return
	}
	g.reg.Swap(subs)
	if err := g.engine.CleanupSubscriptions(ctx, g.reg.IDs()); err != nil {
		log.Printf("CONFIG_CLEANUP_FAILED error=%v", err)
	}
	if _, err := g.engine.Rematch(ctx); err != nil {
		log.Printf("CONFIG_REMATCH_FAILED error=%v", err)
	}
	g.disp.Wake()
	log.Printf("CONFIG_RELOADED subscriptions=%d", len(subs))
}

// pruneLoop bounds the state window on a timer.
func pruneLoop(ctx context.Context, eng *engine) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := eng.Prune(ctx); err != nil {
				log.Printf("PRUNE_FAILED error=%v", err)
			}
		}
	}
}

// connectNATS connects with auto-reconnect enabled; the JetStream
// subscription survives reconnects on its own.
func connectNATS(ctx context.Context, cfg config) (*nats.Conn, error) {
	var nc *nats.Conn
	err := retry(ctx, 2*time.Second, func() error {
		c, err := nats.Connect(cfg.NatsURL,
			nats.MaxReconnects(-1),
			nats.ReconnectWait(2*time.Second),
			nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
				log.Printf("NATS_DISCONNECTED error=%v", err)
			}),
			nats.ReconnectHandler(func(c *nats.Conn) {
				log.Printf("NATS_RECONNECTED url=%s", c.ConnectedUrl())
			}),
		)
		if err != nil {
			return err
		}
		nc = c
		return nil
	})
	return nc, err
}

// consumerSet runs one durable consumer per extractor stream, mirroring
// the sink: every stream whose subjects intersect extractor.> gets
// <GATEWAY_ID>-<STREAM> with FilterSubject extractor.>, explicit acks and
// DeliverAll (the streams keep five minutes of retention, so a replay
// after downtime is bounded). The consumers are created or updated
// explicitly and never deleted on drain, so a restart resumes from the
// ack floor.
type consumerSet struct {
	gatewayID string
	js        jetstream.JetStream

	mu     sync.Mutex
	active map[string]jetstream.ConsumeContext
	handle func(jetstream.Msg)
}

func newConsumerSet(gatewayID string, js jetstream.JetStream, handle func(jetstream.Msg)) *consumerSet {
	return &consumerSet{gatewayID: gatewayID, js: js, active: map[string]jetstream.ConsumeContext{}, handle: handle}
}

// durableName derives the per-stream durable name.
func (cs *consumerSet) durableName(stream string) string {
	return cs.gatewayID + "-" + stream
}

// initialScan discovers the extractor streams.
func (cs *consumerSet) initialScan(ctx context.Context) error {
	return cs.reconcile(ctx)
}

// runScanLoop re-lists the streams periodically, so a stream provisioned
// by an adapter that just started (or deleted at runtime) is followed
// without a restart.
func (cs *consumerSet) runScanLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := cs.reconcile(ctx); err != nil {
				log.Printf("SCAN_FAILED error=%v", err)
			}
		}
	}
}

// reconcile starts consumers for matching streams that have none yet and
// stops the consume handles of streams that disappeared.
func (cs *consumerSet) reconcile(ctx context.Context) error {
	names, err := matchingStreams(ctx, cs.js, subjectFilter)
	if err != nil {
		return err
	}
	want := make(map[string]bool, len(names))
	for _, name := range names {
		want[name] = true
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for name, cc := range cs.active {
		if want[name] {
			continue
		}
		cc.Stop()
		delete(cs.active, name)
		log.Printf("CONSUMER_STOPPED stream=%s durable=%s", name, cs.durableName(name))
	}
	for _, name := range names {
		if _, ok := cs.active[name]; ok {
			continue
		}
		apiCtx, cancelAPI := context.WithTimeout(ctx, 5*time.Second)
		cons, err := cs.js.CreateOrUpdateConsumer(apiCtx, name, jetstream.ConsumerConfig{
			Durable:       cs.durableName(name),
			FilterSubject: subjectFilter,
			DeliverPolicy: jetstream.DeliverAllPolicy,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       60 * time.Second,
			MaxAckPending: 512,
		})
		cancelAPI()
		if err != nil {
			log.Printf("CONSUMER_ERROR stream=%s durable=%s error=%v",
				name, cs.durableName(name), err)
			continue
		}
		cc, err := cons.Consume(cs.handle)
		if err != nil {
			log.Printf("CONSUME_ERROR stream=%s durable=%s error=%v", name, cs.durableName(name), err)
			continue
		}
		cs.active[name] = cc
		log.Printf("CONSUMER_STARTED stream=%s durable=%s filter=%s deliver_policy=all",
			name, cs.durableName(name), subjectFilter)
	}
	return nil
}

// drainAll gracefully drains every active consume handle: the messages
// already buffered are still processed instead of being discarded.
func (cs *consumerSet) drainAll() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for name, cc := range cs.active {
		cc.Drain()
		log.Printf("CONSUMER_DRAINED stream=%s durable=%s", name, cs.durableName(name))
	}
}

// matchingStreams lists the names of all streams whose subjects intersect
// the gateway subject filter.
func matchingStreams(ctx context.Context, js jetstream.JetStream, filter string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	filterTokens := strings.Split(filter, ".")
	var names []string
	it := js.ListStreams(ctx)
	for info := range it.Info() {
		for _, subject := range info.Config.Subjects {
			if subjectsIntersect(filterTokens, strings.Split(subject, ".")) {
				names = append(names, info.Config.Name)
				break
			}
		}
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// retry calls fn every interval until it succeeds or ctx stops; it logs
// the first failure and then every 15th, so a booting dependency does not
// flood the log.
func retry(ctx context.Context, interval time.Duration, fn func() error) error {
	var lastErr error
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			if lastErr == nil {
				return ctx.Err()
			}
			return lastErr
		}
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
			if attempt == 1 || attempt%15 == 0 {
				log.Printf("RETRYING attempt=%d error=%v", attempt, err)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(interval):
		}
	}
}
