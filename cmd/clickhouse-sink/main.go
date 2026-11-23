// Command clickhouse-sink drains the extractor NATS JetStream subjects
// into the universal ClickHouse event table (deploy/clickhouse/schema.sql).
//
// The live topology is not a single stream: the adapter provisions one
// stream per network (EXTRACTOR_<NET>) and older stacks run per-family
// demo streams. The sink therefore discovers every stream whose subjects
// intersect the sink filter (`extractor.>`) and runs one durable consumer
// per stream, consuming them all concurrently. Streams provisioned or
// deleted at runtime are followed by a periodic rescan. A bare NATS with
// no matching stream gets the configured NATS_STREAM created instead.
//
// Every consumer is durable, so the service survives restarts without
// losing events: a NATS message is acked only after the batch that
// contains it was inserted into ClickHouse. Rows are buffered and flushed
// every SINK_BATCH_SIZE events or SINK_FLUSH_INTERVAL, whichever comes
// first. Replayed rows (a crash between insert and ack, or a later reorg
// update) are collapsed by ReplacingMergeTree, which keeps the row with
// the highest ingested_at.
//
// Every event body is the adapter envelope: block events carry
// number/hash/parent_hash/timestamp, transaction events carry
// block_number/block_hash/tx_index/hash/from/to/value/gas/status, and both
// carry family/kind/source_sequence/entity_ordinal/descriptor_digest/
// record_digest. The network and the event type are derived from the NATS
// subject by stripping the literal prefix of the configured filter: the
// default `extractor.>` yields network/type from
// `extractor.<network>.<type>`, while `evm.v3.>` yields them from
// `evm.v3.<network>.<family>.<kind>`. The full original JSON is kept in
// the raw column, so a mapping gap never loses data.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// config is the environment contract of the service.
type config struct {
	NatsURL        string        // NATS_URL
	NatsStream     string        // NATS_STREAM
	NatsSubject    string        // NATS_SUBJECT
	Durable        string        // SINK_DURABLE
	ClickHouseURL  string        // CLICKHOUSE_URL
	ClickHouseDB   string        // CLICKHOUSE_DB
	CHUser         string        // CLICKHOUSE_USER
	CHPassword     string        // CLICKHOUSE_PASSWORD
	BatchSize      int           // SINK_BATCH_SIZE
	FlushInterval  time.Duration // SINK_FLUSH_INTERVAL
	AckWait        time.Duration // SINK_ACK_WAIT
	MaxAckPending  int           // SINK_MAX_ACK_PENDING
	DeliverPolicy  string        // SINK_DELIVER_POLICY
	RescanInterval time.Duration // SINK_RESCAN_INTERVAL
}

func loadConfig() config {
	return config{
		NatsURL:        envString("NATS_URL", "nats://localhost:4222"),
		NatsStream:     envString("NATS_STREAM", "EXTRACTOR"),
		NatsSubject:    envString("NATS_SUBJECT", "extractor.>"),
		Durable:        envString("SINK_DURABLE", "clickhouse-sink"),
		ClickHouseURL:  envString("CLICKHOUSE_URL", "localhost:9000"),
		ClickHouseDB:   envString("CLICKHOUSE_DB", "extractor"),
		CHUser:         envString("CLICKHOUSE_USER", "default"),
		CHPassword:     envString("CLICKHOUSE_PASSWORD", ""),
		BatchSize:      envInt("SINK_BATCH_SIZE", 100),
		FlushInterval:  envDuration("SINK_FLUSH_INTERVAL", time.Second),
		AckWait:        envDuration("SINK_ACK_WAIT", 60*time.Second),
		MaxAckPending:  envInt("SINK_MAX_ACK_PENDING", 512),
		DeliverPolicy:  envString("SINK_DELIVER_POLICY", "all"),
		RescanInterval: envDuration("SINK_RESCAN_INTERVAL", 30*time.Second),
	}
}

func envString(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		log.Fatalf("CONFIG_INVALID env=%s value=%q must be a positive integer", name, v)
	}
	return def
}

func envDuration(name string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		log.Fatalf("CONFIG_INVALID env=%s value=%q must be a positive duration", name, v)
	}
	return def
}

// chainIDs maps the known network subject tokens to their EIP-155 chain
// ids; unknown networks keep 0.
var chainIDs = map[string]uint64{
	"eth-sepolia":  11155111,
	"eth-holesky":  17000,
	"eth-mainnet":  1,
	"bsc-testnet":  97,
	"bsc-mainnet":  56,
	"poly-testnet": 80002,
	"poly-mainnet": 137,
}

// createDatabaseDDL and tableDDL mirror deploy/clickhouse/schema.sql so a
// bare ClickHouse (no init script mounted) still converges on the universal
// schema.
const createDatabaseDDL = "CREATE DATABASE IF NOT EXISTS %s"

const tableDDL = `CREATE TABLE IF NOT EXISTS %s.events (
    event_id String,
    network LowCardinality(String),
    chain_id UInt64,
    event_type LowCardinality(String),
    family LowCardinality(String) DEFAULT '',
    kind LowCardinality(String) DEFAULT '',
    entity String DEFAULT '',
    block_number UInt64,
    block_hash String,
    parent_hash String,
    block_timestamp Nullable(DateTime('UTC')),
    block_gas_used UInt64 DEFAULT 0,
    block_tx_count UInt64 DEFAULT 0,
    tx_index UInt64 DEFAULT 0,
    tx_hash String DEFAULT '',
    tx_from String DEFAULT '',
    tx_to String DEFAULT '',
    tx_value String DEFAULT '',
    tx_gas UInt64 DEFAULT 0,
    tx_gas_price String DEFAULT '',
    tx_status LowCardinality(String) DEFAULT '',
    tx_type LowCardinality(String) DEFAULT '',
    lifecycle_event LowCardinality(String) DEFAULT '',
    lifecycle_status LowCardinality(String) DEFAULT '',
    source_sequence UInt64 DEFAULT 0,
    entity_ordinal UInt64 DEFAULT 0,
    descriptor_digest String DEFAULT '',
    record_digest String DEFAULT '',
    captured_at DateTime64(3, 'UTC'),
    ingested_at DateTime64(3, 'UTC') DEFAULT now(),
    raw String
) ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMM(ifNull(block_timestamp, toDateTime(captured_at, 'UTC')))
ORDER BY (network, event_type, block_number, entity)
TTL ifNull(block_timestamp, toDateTime(captured_at, 'UTC')) + INTERVAL 365 DAY`

// eventRow is one row of extractor.events, matched to the table columns by
// the ch tags. block_timestamp is a pointer so transaction events store
// NULL until they are joined with their block.
type eventRow struct {
	EventID          string     `ch:"event_id"`
	Network          string     `ch:"network"`
	ChainID          uint64     `ch:"chain_id"`
	EventType        string     `ch:"event_type"`
	Family           string     `ch:"family"`
	Kind             string     `ch:"kind"`
	Entity           string     `ch:"entity"`
	BlockNumber      uint64     `ch:"block_number"`
	BlockHash        string     `ch:"block_hash"`
	ParentHash       string     `ch:"parent_hash"`
	BlockTimestamp   *time.Time `ch:"block_timestamp"`
	BlockGasUsed     uint64     `ch:"block_gas_used"`
	BlockTxCount     uint64     `ch:"block_tx_count"`
	TxIndex          uint64     `ch:"tx_index"`
	TxHash           string     `ch:"tx_hash"`
	TxFrom           string     `ch:"tx_from"`
	TxTo             string     `ch:"tx_to"`
	TxValue          string     `ch:"tx_value"`
	TxGas            uint64     `ch:"tx_gas"`
	TxGasPrice       string     `ch:"tx_gas_price"`
	TxStatus         string     `ch:"tx_status"`
	TxType           string     `ch:"tx_type"`
	LifecycleEvent   string     `ch:"lifecycle_event"`
	LifecycleStatus  string     `ch:"lifecycle_status"`
	SourceSequence   uint64     `ch:"source_sequence"`
	EntityOrdinal    uint64     `ch:"entity_ordinal"`
	DescriptorDigest string     `ch:"descriptor_digest"`
	RecordDigest     string     `ch:"record_digest"`
	CapturedAt       time.Time  `ch:"captured_at"`
	IngestedAt       time.Time  `ch:"ingested_at"`
	Raw              string     `ch:"raw"`
}

// pendingEvent couples a parsed row with the NATS message it came from: the
// message is acked only after the row is inserted. key identifies the
// stream message for redelivery-duplicate detection.
type pendingEvent struct {
	row eventRow
	msg jetstream.Msg
	key string
}

// sink owns the consumers' shared batch and the insert counters.
type sink struct {
	cfg      config
	ch       driver.Conn
	subjects subjectParser

	mu            sync.Mutex
	pending       []pendingEvent
	pendingKeys   map[string]struct{}
	inserted      uint64
	lastLogged    uint64
	lastNetwork   string
	flushFailures int
	duplicates    uint64
}

// flexNumber accepts a JSON number or a numeric string ("1732000000",
// "1000000000000000000") and keeps the literal; empty means null/absent.
type flexNumber string

func (f *flexNumber) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	if len(s) > 0 && s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*f = flexNumber(str)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexNumber(n)
	return nil
}

// u64 parses the literal as an unsigned integer; false means absent or
// unparsable.
func (f flexNumber) u64() (uint64, bool) {
	if f == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(f)), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// flexString accepts a JSON string, number or null (null decodes to the
// empty string, so a null "to" of a contract creation lands as empty).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	if len(s) > 0 && s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*f = flexString(str)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexString(n.String())
	return nil
}

// payload is the union of the block-shaped and transaction-shaped event
// bodies plus the adapter envelope present on every event. The evm.v3
// mock stack of the deploy service uses its own key spellings (height,
// tx_hash, from_addr, to_addr); they are accepted as aliases.
type payload struct {
	// Block shape: {"number":123,"hash":"0x...","parent_hash":"0x...",
	// "timestamp":1732000000,"gas_used":100000,"tx_count":5}
	Number     flexNumber `json:"number"`
	Hash       flexString `json:"hash"`
	ParentHash flexString `json:"parent_hash"`
	Timestamp  flexNumber `json:"timestamp"`
	GasUsed    flexNumber `json:"gas_used"`
	TxCount    flexNumber `json:"tx_count"`

	// Transaction shape: {"block_number":123,"block_hash":"0x...",
	// "tx_index":0,"hash":"0x...","from":"0x...","to":"0x..."|null,
	// "value":"1000000000000000000","gas":21000,"status":1|0|null}
	BlockNumber flexNumber `json:"block_number"`
	BlockHash   flexString `json:"block_hash"`
	TxIndex     flexNumber `json:"tx_index"`
	From        flexString `json:"from"`
	To          flexString `json:"to"`
	Value       flexString `json:"value"`
	Gas         flexNumber `json:"gas"`
	GasPrice    flexString `json:"gas_price"`
	Status      flexNumber `json:"status"`
	TxType      flexString `json:"type"`

	// evm.v3 mock aliases: {"height":N,"tx_hash":"0x...","from_addr":...}
	Height   flexNumber `json:"height"`
	TxHash   flexString `json:"tx_hash"`
	FromAddr flexString `json:"from_addr"`
	ToAddr   flexString `json:"to_addr"`

	// Envelope on every event (network and decision_sequence live only in
	// raw: the network comes from the subject).
	Family           flexString `json:"family"`
	Kind             flexString `json:"kind"`
	ChainID          flexNumber `json:"chain_id"`
	EntityOrdinal    flexNumber `json:"entity_ordinal"`
	SourceSequence   flexNumber `json:"source_sequence"`
	DescriptorDigest flexString `json:"descriptor_digest"`
	RecordDigest     flexString `json:"record_digest"`
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	cfg := loadConfig()
	log.Printf("STARTING nats=%s subject=%s durable=%s clickhouse=%s db=%s batch_size=%d flush_interval=%s deliver_policy=%s rescan_interval=%s",
		cfg.NatsURL, cfg.NatsSubject, cfg.Durable, cfg.ClickHouseURL,
		cfg.ClickHouseDB, cfg.BatchSize, cfg.FlushInterval, cfg.DeliverPolicy, cfg.RescanInterval)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ClickHouse first: the sink is useless without the target table.
	conn, err := connectClickHouse(ctx, cfg)
	if err != nil {
		log.Fatalf("CLICKHOUSE_UNAVAILABLE error=%v", err)
	}
	defer conn.Close()
	log.Printf("CLICKHOUSE_READY url=%s db=%s", cfg.ClickHouseURL, cfg.ClickHouseDB)

	nc, err := connectNATS(ctx, cfg)
	if err != nil {
		log.Fatalf("NATS_UNAVAILABLE error=%v", err)
	}
	defer nc.Drain()
	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("NATS_JETSTREAM_ERROR error=%v", err)
	}
	deliver, err := deliverPolicy(cfg.DeliverPolicy)
	if err != nil {
		log.Fatalf("CONFIG_INVALID SINK_DELIVER_POLICY=%s error=%v", cfg.DeliverPolicy, err)
	}
	subjects, err := newSubjectParser(cfg.NatsSubject)
	if err != nil {
		log.Fatalf("CONFIG_INVALID NATS_SUBJECT=%s error=%v", cfg.NatsSubject, err)
	}

	s := &sink{cfg: cfg, ch: conn, subjects: subjects, pendingKeys: make(map[string]struct{})}
	streams := newConsumerSet(cfg, js, s, deliver)
	if err := streams.initialScan(ctx); err != nil {
		log.Fatalf("NATS_STREAM_ERROR error=%v", err)
	}
	go streams.runScanLoop(ctx)

	// The time-based half of the batching policy: flush whatever is
	// pending every FlushInterval.
	go func() {
		ticker := time.NewTicker(cfg.FlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				s.mu.Lock()
				err := s.flushLocked(fctx)
				remaining := len(s.pending)
				s.mu.Unlock()
				cancel()
				if err != nil {
					log.Printf("FLUSH_FAILED pending=%d error=%v", remaining, err)
				}
			}
		}
	}()

	<-ctx.Done()
	log.Printf("STOPPING reason=signal")

	// Stop fetching, then give the consume loops a short grace period to
	// push the messages they already buffered through the handler (Drain
	// processes the buffer instead of discarding it). Anything that does
	// not make it into a batch stays unacked and the durable consumers
	// replay it after the restart — a redelivery, never a loss.
	streams.drainAll()
	time.Sleep(2 * time.Second)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		s.mu.Lock()
		err := s.flushLocked(shutdownCtx)
		remaining := len(s.pending)
		s.mu.Unlock()
		if err == nil {
			break
		}
		log.Printf("FLUSH_FAILED pending=%d error=%v", remaining, err)
		if remaining == 0 || shutdownCtx.Err() != nil {
			log.Printf("STOP_INCOMPLETE pending=%d", remaining)
			break
		}
		select {
		case <-shutdownCtx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}
	s.mu.Lock()
	total := s.inserted
	s.mu.Unlock()
	log.Printf("STOPPED inserted_total=%d", total)
}

// connectClickHouse opens the native connection and converges the schema,
// retrying until the context stops (ClickHouse may still be booting).
func connectClickHouse(ctx context.Context, cfg config) (driver.Conn, error) {
	addr := strings.TrimSpace(cfg.ClickHouseURL)
	addr = strings.TrimPrefix(addr, "clickhouse://")
	var conn driver.Conn
	err := retry(ctx, 2*time.Second, func() error {
		c, err := clickhouse.Open(&clickhouse.Options{
			Addr:        []string{addr},
			Auth:        clickhouse.Auth{Database: "default", Username: cfg.CHUser, Password: cfg.CHPassword},
			DialTimeout: 5 * time.Second,
			Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
		})
		if err != nil {
			return err
		}
		if err := c.Ping(ctx); err != nil {
			c.Close()
			return err
		}
		conn = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	db, err := quoteIdent(cfg.ClickHouseDB)
	if err != nil {
		return nil, err
	}
	if err := conn.Exec(ctx, fmt.Sprintf(createDatabaseDDL, db)); err != nil {
		return nil, fmt.Errorf("create database: %w", err)
	}
	if err := conn.Exec(ctx, fmt.Sprintf(tableDDL, db)); err != nil {
		return nil, fmt.Errorf("create table: %w", err)
	}
	return conn, nil
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

// ensureStream creates the extractor stream if it is missing; an existing
// stream is never reconfigured.
func ensureStream(ctx context.Context, js jetstream.JetStream, cfg config) error {
	_, err := js.Stream(ctx, cfg.NatsStream)
	if err == nil {
		return nil
	}
	if !errors.Is(err, jetstream.ErrStreamNotFound) {
		return fmt.Errorf("stream info: %w", err)
	}
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     cfg.NatsStream,
		Subjects: []string{cfg.NatsSubject},
		Storage:  jetstream.FileStorage,
	})
	if err != nil {
		return fmt.Errorf("create stream: %w", err)
	}
	log.Printf("STREAM_CREATED stream=%s subjects=%s", cfg.NatsStream, cfg.NatsSubject)
	return nil
}

// consumerSet runs one durable consumer per extractor stream. The sink
// filter (`extractor.>`) is matched against every stream's subjects, so
// the per-network adapter streams (EXTRACTOR_<NET>) and the per-family
// demo streams are all consumed concurrently; handleMessage is
// mutex-guarded, so sharing it across streams is safe.
type consumerSet struct {
	cfg     config
	js      jetstream.JetStream
	sink    *sink
	deliver jetstream.DeliverPolicy

	mu     sync.Mutex
	active map[string]jetstream.ConsumeContext
}

func newConsumerSet(cfg config, js jetstream.JetStream, s *sink, deliver jetstream.DeliverPolicy) *consumerSet {
	return &consumerSet{
		cfg:     cfg,
		js:      js,
		sink:    s,
		deliver: deliver,
		active:  make(map[string]jetstream.ConsumeContext),
	}
}

// durableName derives the per-stream durable name (stream names and
// durable names share the [a-zA-Z0-9-_.] alphabet).
func (cs *consumerSet) durableName(stream string) string {
	return cs.cfg.Durable + "-" + stream
}

// initialScan discovers the extractor streams and, on a bare NATS where
// none exists, falls back to creating the configured NATS_STREAM with the
// configured subject.
func (cs *consumerSet) initialScan(ctx context.Context) error {
	names, err := matchingStreams(ctx, cs.js, cs.cfg.NatsSubject)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		if err := ensureStream(ctx, cs.js, cs.cfg); err != nil {
			return err
		}
	}
	return cs.reconcile(ctx)
}

// runScanLoop re-lists the streams periodically, so a stream provisioned
// by an adapter that just started (or deleted at runtime) is followed
// without a restart.
func (cs *consumerSet) runScanLoop(ctx context.Context) {
	ticker := time.NewTicker(cs.cfg.RescanInterval)
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
// stops the consume handles of streams that disappeared. Per-stream
// failures are logged and retried on the next scan instead of aborting
// the whole set.
func (cs *consumerSet) reconcile(ctx context.Context) error {
	names, err := matchingStreams(ctx, cs.js, cs.cfg.NatsSubject)
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
		// The durable consumer is created (or updated) explicitly, so
		// the client never owns its lifetime: stopping the sink leaves
		// it on the server with its ack floor, and the next start
		// resumes exactly where this one stopped instead of replaying
		// the whole stream. (A consumer created implicitly by a
		// subscribe call is deleted on unsubscribe/drain — that is the
		// trap this avoids.) A short API timeout keeps a hung server
		// from stalling the scan loop.
		apiCtx, cancelAPI := context.WithTimeout(ctx, 5*time.Second)
		cons, err := cs.js.CreateOrUpdateConsumer(apiCtx, name, jetstream.ConsumerConfig{
			Durable:       cs.durableName(name),
			FilterSubject: cs.cfg.NatsSubject,
			DeliverPolicy: cs.deliver,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       cs.cfg.AckWait,
			MaxAckPending: cs.cfg.MaxAckPending,
		})
		cancelAPI()
		if err != nil {
			log.Printf("CONSUMER_ERROR stream=%s durable=%s error=%v (an existing durable keeps its stored deliver policy; delete it to apply SINK_DELIVER_POLICY)",
				name, cs.durableName(name), err)
			continue
		}
		cc, err := cons.Consume(cs.sink.handleMessage)
		if err != nil {
			log.Printf("CONSUME_ERROR stream=%s durable=%s error=%v", name, cs.durableName(name), err)
			continue
		}
		cs.active[name] = cc
		log.Printf("CONSUMER_STARTED stream=%s durable=%s filter=%s deliver_policy=%s",
			name, cs.durableName(name), cs.cfg.NatsSubject, cs.cfg.DeliverPolicy)
	}
	return nil
}

// drainAll gracefully drains every active consume handle: the messages
// already buffered are still processed by the handler instead of being
// discarded.
func (cs *consumerSet) drainAll() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for name, cc := range cs.active {
		cc.Drain()
		log.Printf("CONSUMER_DRAINED stream=%s durable=%s", name, cs.durableName(name))
	}
}

// matchingStreams lists the names of all streams whose subjects intersect
// the sink subject filter. The listing runs on its own short timeout so a
// hung server API cannot stall the scan loop.
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

// subjectsIntersect reports whether two NATS subject filters can match at
// least one common subject, honoring the token wildcards ("*" exactly one
// token, ">" the non-empty tail).
func subjectsIntersect(a, b []string) bool {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		aEnd, bEnd := i >= len(a), i >= len(b)
		switch {
		case aEnd && bEnd:
			return true
		case aEnd || bEnd:
			// One filter is exhausted, so it matches only subjects of
			// exactly its own length while the other still has tokens.
			return false
		}
		switch ta, tb := a[i], b[i]; {
		case ta == ">" || tb == ">":
			return true
		case ta == "*" || tb == "*" || ta == tb:
			continue
		default:
			return false
		}
	}
	return true
}

// deliverPolicy maps SINK_DELIVER_POLICY onto the consumer deliver policy.
func deliverPolicy(v string) (jetstream.DeliverPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "all":
		return jetstream.DeliverAllPolicy, nil
	case "last":
		return jetstream.DeliverLastPolicy, nil
	case "new":
		return jetstream.DeliverNewPolicy, nil
	default:
		return jetstream.DeliverAllPolicy, fmt.Errorf("unknown deliver policy %q (want all, last or new)", v)
	}
}

// handleMessage parses one delivered message into a row and adds it to the
// pending batch, flushing synchronously when the batch is full. A message
// whose subject or body cannot be understood is terminated (no redelivery
// for this consumer): the body stays in the stream and in the logs.
// Redelivered copies of messages that are still pending (an insert outage
// longer than the ack wait) are dropped — the original copy is already in
// the batch and will be acked with it.
func (s *sink) handleMessage(m jetstream.Msg) {
	now := time.Now().UTC()
	row, err := s.parseEvent(m.Subject(), m.Data(), now)
	if err != nil {
		log.Printf("EVENT_REJECTED subject=%s error=%v", m.Subject(), err)
		_ = m.Term()
		return
	}
	key := messageKey(m)
	s.mu.Lock()
	if key != "" {
		if _, dup := s.pendingKeys[key]; dup {
			s.duplicates++
			if s.duplicates == 1 || s.duplicates%100 == 0 {
				log.Printf("DUPLICATE_DROPPED subject=%s total=%d", m.Subject(), s.duplicates)
			}
			s.mu.Unlock()
			return
		}
		s.pendingKeys[key] = struct{}{}
	}
	s.pending = append(s.pending, pendingEvent{row: row, msg: m, key: key})
	s.lastNetwork = row.Network
	var flushErr error
	remaining := len(s.pending)
	if remaining >= s.cfg.BatchSize {
		fctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		flushErr = s.flushLocked(fctx)
		remaining = len(s.pending)
		cancel()
	}
	s.mu.Unlock()
	if flushErr != nil {
		log.Printf("FLUSH_FAILED pending=%d error=%v", remaining, flushErr)
	}
}

// messageKey identifies a stream message for duplicate detection; "" when
// no JetStream metadata is available.
func messageKey(m jetstream.Msg) string {
	meta, err := m.Metadata()
	if err != nil {
		return ""
	}
	return meta.Stream + "/" + strconv.FormatUint(meta.Sequence.Stream, 10)
}

// flushLocked inserts the pending batch, acks its messages on success and
// keeps them pending on failure. After three consecutive failures with a
// reachable server, the batch is bisected to isolate deterministic bad
// rows; those are logged as ROW_EVICTED and terminated, the rest is
// inserted. Callers hold s.mu.
func (s *sink) flushLocked(ctx context.Context) error {
	if len(s.pending) == 0 {
		return nil
	}
	err := s.insertRows(ctx, s.pending)
	if err == nil {
		s.ackRows(s.pending)
		s.pending = s.pending[:0]
		s.flushFailures = 0
		return nil
	}
	s.flushFailures++
	if s.flushFailures < 3 {
		return err
	}
	// A ClickHouse outage must not turn into mass eviction: only bisect
	// when the server itself answers, so the failure is data-shaped.
	if pingErr := s.ch.Ping(ctx); pingErr != nil {
		return err
	}
	log.Printf("FLUSH_BISECT pending=%d", len(s.pending))
	s.insertWithIsolation(ctx, s.pending)
	s.pending = s.pending[:0]
	s.flushFailures = 0
	return nil
}

// insertRows appends and sends one batch of rows.
func (s *sink) insertRows(ctx context.Context, rows []pendingEvent) error {
	if len(rows) == 0 {
		return nil
	}
	stmt := fmt.Sprintf("INSERT INTO %s.events", quoteIdentLoose(s.cfg.ClickHouseDB))
	batch, err := s.ch.PrepareBatch(ctx, stmt)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, p := range rows {
		if err := batch.AppendStruct(&p.row); err != nil {
			return fmt.Errorf("append row (subject=%s): %w", p.msg.Subject(), err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send batch: %w", err)
	}
	return nil
}

// insertWithIsolation inserts rows, bisecting on failure until single
// uninsertable rows are isolated: those are logged as ROW_EVICTED and
// terminated, every inserted sub-batch is acked. Callers hold s.mu.
func (s *sink) insertWithIsolation(ctx context.Context, rows []pendingEvent) {
	if len(rows) == 0 {
		return
	}
	if err := s.insertRows(ctx, rows); err == nil {
		s.ackRows(rows)
		return
	} else if len(rows) == 1 {
		p := rows[0]
		log.Printf("ROW_EVICTED subject=%s record_digest=%s tx_hash=%s raw_prefix=%q",
			p.msg.Subject(), p.row.RecordDigest, p.row.TxHash, truncate(p.row.Raw, 80))
		_ = p.msg.Term()
		s.dropKey(p.key)
		return
	}
	mid := len(rows) / 2
	s.insertWithIsolation(ctx, rows[:mid])
	s.insertWithIsolation(ctx, rows[mid:])
}

// ackRows acks the messages of an inserted batch and advances the
// counters. The rows are durable: acking stops JetStream from offering
// them again, and a crash before an ack replays the row, which
// ReplacingMergeTree collapses later. Callers hold s.mu.
func (s *sink) ackRows(rows []pendingEvent) {
	for _, p := range rows {
		if err := p.msg.Ack(); err != nil {
			log.Printf("ACK_FAILED subject=%s error=%v", p.msg.Subject(), err)
		}
		s.dropKey(p.key)
	}
	s.inserted += uint64(len(rows))
	if s.inserted-s.lastLogged >= 1000 {
		s.lastLogged = s.inserted
		log.Printf("INSERTED network=%s total=%d", s.lastNetwork, s.inserted)
	}
}

// dropKey forgets a pending message identity; callers hold s.mu.
func (s *sink) dropKey(key string) {
	if key != "" {
		delete(s.pendingKeys, key)
	}
}

// truncate clips s to at most n bytes for log lines.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// parseEvent maps one NATS message onto a row. The network and the event
// type come from the subject by stripping the literal prefix of the
// configured filter (default `extractor.>` gives
// `extractor.<network>.<type>`, `evm.v3.>` gives
// `evm.v3.<network>.<family>.<kind>`); the body is the adapter envelope
// (family/kind/... on every event; block events shaped
// number/hash/timestamp, transaction events shaped
// block_number/block_hash/hash/from/to/value/gas/status).
func (s *sink) parseEvent(subject string, data []byte, now time.Time) (eventRow, error) {
	network, eventType, ok := s.subjects.splitSubject(subject)
	if !ok {
		return eventRow{}, fmt.Errorf("subject %q does not match the configured filter prefix %s<network>.<type>",
			subject, s.subjects.prefixDot())
	}
	var p payload
	if err := json.Unmarshal(data, &p); err != nil {
		return eventRow{}, fmt.Errorf("decode json body: %w", err)
	}

	row := eventRow{
		EventID:          newEventID(),
		Network:          network,
		ChainID:          chainID(network, p.ChainID),
		EventType:        eventType,
		Family:           string(p.Family),
		Kind:             string(p.Kind),
		CapturedAt:       now,
		IngestedAt:       now,
		Raw:              string(data),
		SourceSequence:   firstU64(p.SourceSequence),
		EntityOrdinal:    firstU64(p.EntityOrdinal),
		DescriptorDigest: string(p.DescriptorDigest),
		RecordDigest:     string(p.RecordDigest),
	}
	row.LifecycleEvent, row.LifecycleStatus = lifecycle(p.Kind)

	// Block events use number/hash; transaction events use
	// block_number/block_hash and hash is the tx hash. Both shapes are
	// accepted for block_number, the hash columns are filled per type.
	// The evm.v3 mock stack spells them height/tx_hash/from_addr/to_addr.
	row.BlockNumber = firstU64(p.BlockNumber, p.Number, p.Height)

	switch {
	case eventType == "block" || (eventType == "" && p.Family == "block"):
		row.BlockHash = firstString(p.Hash, p.BlockHash)
		row.ParentHash = string(p.ParentHash)
		row.BlockGasUsed = firstU64(p.GasUsed)
		row.BlockTxCount = firstU64(p.TxCount)
		// entity is the dedup/reorg identity of the row: the block hash
		// for blocks, so a replayed block with a new kind replaces the
		// old row under the same ORDER BY key.
		row.Entity = row.BlockHash
		if sec, ok := p.Timestamp.u64(); ok {
			if sec < minBlockTimestamp || sec > maxBlockTimestamp {
				return eventRow{}, fmt.Errorf("block timestamp %d outside %d..%d (2000-01-01..2100-01-01 UTC)",
					sec, minBlockTimestamp, maxBlockTimestamp)
			}
			t := time.Unix(int64(sec), 0).UTC()
			row.BlockTimestamp = &t
		}
	case eventType == "transaction" || eventType == "tx" || p.Family == "tx" || p.Family == "transaction":
		row.BlockHash = string(p.BlockHash)
		row.TxIndex = firstU64(p.TxIndex)
		row.TxHash = firstString(p.Hash, p.TxHash)
		row.TxFrom = firstString(p.From, p.FromAddr)
		row.TxTo = firstString(p.To, p.ToAddr) // null on contract creation decodes to ''
		row.TxValue = string(p.Value)
		row.TxGas = firstU64(p.Gas)
		row.TxGasPrice = string(p.GasPrice)
		row.TxStatus = txStatus(p.Status)
		row.TxType = string(p.TxType)
		if row.TxHash == "" {
			// Without a tx hash the row would key on the empty entity and
			// could collide with (and replace) unrelated rows: reject it
			// loudly instead of corrupting the block/tx namespaces.
			return eventRow{}, fmt.Errorf("transaction event without a hash (hash/tx_hash both absent)")
		}
		row.Entity = row.TxHash
		// block_timestamp stays NULL until the event is joined with its
		// block; the partition expression falls back to captured_at.
	default:
		// Families without block context (balance and friends): the
		// record digest is stable per event, so two distinct events never
		// share an ORDER BY key.
		row.Entity = row.RecordDigest
	}
	return row, nil
}

// Timestamp bounds for parsed block timestamps: everything outside is a
// broken source value (a wrapped or negative unix seconds would make
// Nullable(DateTime) reject the insert), so it is refused at parse time.
const (
	minBlockTimestamp = 946684800  // 2000-01-01T00:00:00Z
	maxBlockTimestamp = 4102444800 // 2100-01-01T00:00:00Z
)

// subjectParser derives the network and event type tokens of a delivered
// subject by stripping the literal prefix of the configured filter: for
// `extractor.>` the prefix is [extractor], for `evm.v3.>` it is
// [evm, v3]. The filter itself must be literal tokens plus the `>` tail.
type subjectParser struct {
	prefix []string
}

// newSubjectParser validates the subject filter and precomputes its
// literal prefix tokens.
func newSubjectParser(filter string) (subjectParser, error) {
	tokens := strings.Split(strings.TrimSpace(filter), ".")
	if len(tokens) < 2 || tokens[len(tokens)-1] != ">" {
		return subjectParser{}, fmt.Errorf("filter %q must be literal tokens ending in the > wildcard", filter)
	}
	prefix := tokens[:len(tokens)-1]
	for _, t := range prefix {
		if t == "" || t == "*" || t == ">" {
			return subjectParser{}, fmt.Errorf("filter %q must hold only literal tokens before the > wildcard", filter)
		}
	}
	return subjectParser{prefix: prefix}, nil
}

// splitSubject strips the filter prefix and returns the network and event
// type tokens. Extra trailing tokens (the kind of
// evm.v3.<net>.<family>.<kind>) are tolerated and ignored; a subject
// without the prefix or with fewer than two tokens after it is a
// structural mismatch against the configured filter.
func (p subjectParser) splitSubject(subject string) (network, eventType string, ok bool) {
	tokens := strings.Split(subject, ".")
	if len(tokens) < len(p.prefix)+2 {
		return "", "", false
	}
	for i, want := range p.prefix {
		if tokens[i] != want {
			return "", "", false
		}
	}
	network, eventType = tokens[len(p.prefix)], tokens[len(p.prefix)+1]
	if network == "" || eventType == "" {
		return "", "", false
	}
	return network, eventType, true
}

// prefixDot renders the prefix for log lines ("evm.v3.").
func (p subjectParser) prefixDot() string {
	if len(p.prefix) == 0 {
		return ""
	}
	return strings.Join(p.prefix, ".") + "."
}

// chainID prefers an explicit payload value, then the known table.
func chainID(network string, fromPayload flexNumber) uint64 {
	if v, ok := fromPayload.u64(); ok {
		return v
	}
	return chainIDs[network]
}

// txStatus maps the numeric receipt status: 1 success, 0 reverted,
// null/absent stays unknown (empty).
func txStatus(v flexNumber) string {
	switch v {
	case "1":
		return "success"
	case "0":
		return "reverted"
	case "":
		return ""
	default:
		return string(v)
	}
}

// lifecycle maps the envelope kind onto the reorg-ready lifecycle columns.
func lifecycle(kind flexString) (event, status string) {
	switch kind {
	case "":
		return "", ""
	case "applied":
		return "applied", "active"
	case "removed":
		return "removed", "removed"
	default:
		return string(kind), ""
	}
}

// newEventID returns a random RFC 4122 version 4 UUID in canonical hex.
func newEventID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on a healthy host; degrade to a
		// time-derived id rather than dropping the event.
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16]))
}

// firstU64 returns the first literal that parses, 0 otherwise.
func firstU64(vals ...flexNumber) uint64 {
	for _, v := range vals {
		if n, ok := v.u64(); ok {
			return n
		}
	}
	return 0
}

// firstString returns the first non-empty value.
func firstString(vals ...flexString) string {
	for _, v := range vals {
		if v != "" {
			return string(v)
		}
	}
	return ""
}

// quoteIdent validates a database identifier for DDL interpolation.
func quoteIdent(name string) (string, error) {
	for _, r := range name {
		if !(r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return "", fmt.Errorf("database name %q holds a character outside [A-Za-z0-9_]", name)
		}
	}
	if name == "" {
		return "", fmt.Errorf("database name is empty")
	}
	return "`" + name + "`", nil
}

// quoteIdentLoose is quoteIdent for statements whose name was already
// validated at startup; it never fails.
func quoteIdentLoose(name string) string {
	q, err := quoteIdent(name)
	if err != nil {
		return "`extractor`"
	}
	return q
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
