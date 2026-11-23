# clickhouse-sink

A standalone service that drains the extractor NATS JetStream subjects into
the universal ClickHouse event table. It is deliberately separate from the
extractor binaries: it only reads the extractor streams (it creates a
stream only on a bare NATS where none exists) and its only dependencies are
the pinned NATS and ClickHouse Go clients.

```
extractor adapter ──▶ NATS JetStream ──▶ clickhouse-sink ──▶ ClickHouse
                     extractor.<network>.<type>              extractor.events
```

## How it works

1. Connects to ClickHouse (retrying until it is up) and creates the
   database and the `events` table if they do not exist — the same DDL as
   `deploy/clickhouse/schema.sql`, so a bare ClickHouse converges on the
   right schema.
2. Connects to NATS and lists all streams, picking every stream whose
   subjects intersect the sink filter `extractor.>` (token-wildcard
   intersection: `extractor.>` matches `extractor.eth-sepolia.>`,
   `extractor.eth.transaction`, …). The live topology is multi-stream —
   the real adapter provisions one stream per network
   (`EXTRACTOR_<NETWORK>`) — so the sink runs one durable consumer per
   stream (`<SINK_DURABLE>-<STREAM>`, e.g. `clickhouse-sink-EXTRACTOR_ETH_SEPOLIA`)
   and consumes them all concurrently.
3. On a bare NATS where no stream matches, it creates the configured
   `NATS_STREAM` with subject `NATS_SUBJECT` instead; an existing stream
   is never reconfigured.
4. A background rescan (every `SINK_RESCAN_INTERVAL`, default 30s) re-lists
   the streams: a stream provisioned by an adapter that just started gets a
   consumer without a restart, and a deleted stream has its consumer
   stopped.
5. Parses each event body (the adapter envelope) and maps it onto the
   universal columns. `network` and `event_type` come from the subject by
   stripping the filter's literal prefix (see *Subject contract* below);
   the body supplies the rest; the full original JSON is kept in `raw`.
   A subject that does not match the configured prefix, an undecodable
   body, a transaction without a hash, or a block timestamp outside
   2000-01-01..2100-01-01 UTC is rejected (`EVENT_REJECTED`) and
   terminated — the message itself stays in the stream.
6. Buffers rows and inserts them in batches: every **100 messages** or
   **1 second**, whichever comes first. Redelivered copies of messages
   still sitting in the pending batch are dropped (`DUPLICATE_DROPPED`).
   A batch that keeps failing insert is retried three times, then (only
   when the server answers, so an outage never evicts data) bisected to
   isolate the bad row(s): those are logged as `ROW_EVICTED` and
   terminated, the rest is inserted.
7. Acks a NATS message only **after** the batch containing it was inserted
   (at-least-once). Replayed duplicates and later reorg updates are
   collapsed by `ReplacingMergeTree`, which keeps the row with the highest
   `ingested_at`.
8. On SIGTERM/SIGINT it drains every consume handle and flushes the
   pending batch before exiting.

Log lines are plain key=value records on stdout. A progress line appears
per 1000 inserted messages:

```
INSERTED network=eth-sepolia total=5000
```

## Configuration (environment)

| Variable | Default | Meaning |
|---|---|---|
| `NATS_URL` | `nats://localhost:4222` | NATS server |
| `CLICKHOUSE_URL` | `localhost:9000` | ClickHouse native address (host:port) |
| `CLICKHOUSE_DB` | `extractor` | database (and table `events` inside it) |
| `CLICKHOUSE_USER` | `default` | ClickHouse user |
| `CLICKHOUSE_PASSWORD` | *(empty)* | ClickHouse password |
| `NATS_STREAM` | `EXTRACTOR` | stream created when no matching stream exists |
| `NATS_SUBJECT` | `extractor.>` | subject filter matched against every stream |
| `SINK_DURABLE` | `clickhouse-sink` | durable consumer prefix (per stream: `<prefix>-<stream>`) |
| `SINK_DELIVER_POLICY` | `all` | first-connect delivery: `all`, `last` (only the last message per stream) or `new` (skip the backlog) — use `last`/`new` to skip a large existing backlog; an existing durable keeps its stored policy (delete it to apply a change) |
| `SINK_RESCAN_INTERVAL` | `30s` | how often new/deleted streams are re-scanned |
| `SINK_BATCH_SIZE` | `100` | max rows per insert |
| `SINK_FLUSH_INTERVAL` | `1s` | max delay before an insert |
| `SINK_ACK_WAIT` | `60s` | consumer ack wait |
| `SINK_MAX_ACK_PENDING` | `512` | consumer in-flight cap (per stream) |

## Subject contract

The sink derives `network` and `event_type` from the delivered subject by
stripping the literal prefix of `NATS_SUBJECT` (which must be literal
tokens plus the `>` tail): the first token after the prefix is the
network, the next one is the event type. Extra trailing tokens are
ignored.

- **`extractor.>`** (default): `extractor.<network>.<type>` as published
  by the JSON-envelope adapter `cmd/extractor-rpc`. That adapter currently
  lives on the `feature/real-testnet` branch, pending merge — until it
  lands on main, live `extractor.*` traffic only exists where that branch
  is deployed.
- **`evm.v3.>`**: the production stack on main (`deploy/bootstrap`)
  provisions per-network streams with subjects
  `evm.v3.<net>.<family>.<kind>` (net = eth/bsc/poly, family = tx/block,
  kind = applied/removed/status). Point the sink at it with
  `NATS_SUBJECT=evm.v3.>`: network and family are taken from the subject
  tokens; the mock payloads of that stack use their own key spellings
  (`height`, `tx_hash`, `from_addr`, `to_addr`, `block_hash`), which are
  mapped as aliases; everything else lands in `raw` untouched.

## Running

Full stack (ClickHouse + NATS + sink) from `deploy/clickhouse`:

```sh
cd deploy/clickhouse
docker compose up --build
```

Then watch the rows arrive:

```sh
docker compose exec clickhouse clickhouse-client --query \
  "SELECT event_id, network, event_type, kind, block_number, tx_hash, tx_status, captured_at
   FROM extractor.events ORDER BY captured_at DESC LIMIT 10"
```

Standalone against an already running ClickHouse and NATS:

```sh
cd cmd/clickhouse-sink
go build .
NATS_URL=nats://localhost:4222 CLICKHOUSE_URL=localhost:9000 ./clickhouse-sink
```

Publishing a test event (any NATS client; a plain publish lands in the
stream because its subjects cover `extractor.>`):

```sh
nats pub extractor.eth-sepolia.block '{"number":123,"hash":"0xabc","parent_hash":"0xab9","timestamp":1732000000,"gas_used":100000,"tx_count":1,"schema":"extractor.rpc.v1","network":"eth-sepolia","family":"block","kind":"applied","source_sequence":17,"entity_ordinal":0,"descriptor_digest":"aa","record_digest":"bb"}'
```

## Schema

`extractor.events` (see `deploy/clickhouse/schema.sql`) is one universal
table for every event of every network:

- **Identity**: `event_id` (UUID generated by the sink), `network`
  (subject token), `chain_id` (from the payload when present, else the
  known table below; unknown networks keep 0).

  | network | chain_id |
  |---|---|
  | `eth-mainnet` | 1 |
  | `eth-holesky` | 17000 |
  | `eth-sepolia` | 11155111 |
  | `bsc-mainnet` | 56 |
  | `bsc-testnet` | 97 |
  | `poly-mainnet` | 137 |
  | `poly-testnet` | 80002 |

- **Classification**: `event_type` (subject token: `block`,
  `transaction`/`tx`), envelope `family` (`block`/`tx`) and `kind`
  (lifecycle kind, e.g. `applied`), and `entity` — the collapse identity
  of the row: the block hash for blocks, the tx hash for transactions
  (a transaction without a hash is rejected), the record digest for every
  other family (balance and friends), so two distinct events never share
  a dedup key.
- **Block context**: `block_number`, `block_hash`, `parent_hash`,
  `block_timestamp` — `Nullable(DateTime('UTC'))`: transaction events
  leave it NULL until joined with their block — plus `block_gas_used` and
  `block_tx_count`. A block timestamp outside 2000-01-01..2100-01-01 UTC
  is rejected at parse time.
- **Transaction fields** (defaults on non-tx events): `tx_index`,
  `tx_hash`, `tx_from`, `tx_to` (`''` on contract creation), `tx_value`
  and `tx_gas_price` (decimal strings — they can exceed UInt64), `tx_gas`,
  `tx_status` (`success`/`reverted`/`''` when the receipt is unknown),
  `tx_type`. The current adapter sends no `gas_price`/`type`; the columns
  stay defaulted until it does.
- **Lifecycle** (reorg-ready): `lifecycle_event` (= `kind`), derived
  `lifecycle_status` (`applied`→`active`, `removed`→`removed`).
- **Envelope**: `source_sequence`, `entity_ordinal`,
  `descriptor_digest`, `record_digest`.
- **Timestamps**: `captured_at` (sink receive time, ms precision UTC),
  `ingested_at` (insert time; the ReplacingMergeTree version column).
- **`raw`**: the full original JSON body.

Engine: `ReplacingMergeTree(ingested_at)` partitioned by month of
`block_timestamp` (falling back to `captured_at` while it is NULL), ordered
by `(network, event_type, block_number, entity)`, TTL 365 days. The
ORDER BY key is the collapse identity per family (block hash / tx hash /
record digest in `entity`, scoped by `event_type`), so at-least-once
redeliveries and reorg updates replace the row they describe instead of
each other.

The schema is not frozen yet: the table is created with
`CREATE TABLE IF NOT EXISTS` and never migrated in place, so when upgrading
between pre-freeze versions that changed the schema, drop the table first
(`DROP TABLE extractor.events`) and let the sink recreate it — inserts into
a stale layout fail at the batch level, and the sink aborts isolation on
batch-level failures (rows stay pending) rather than evicting the backlog.

## Example queries

Latest blocks per network:

```sql
SELECT network, block_number, block_hash, block_timestamp, kind
FROM extractor.events
WHERE event_type = 'block'
ORDER BY network, block_number DESC
LIMIT 10 BY network;
```

A transaction by hash, with duplicates collapsed:

```sql
SELECT * FROM extractor.events FINAL
WHERE network = 'eth-sepolia' AND tx_hash = '0x...';
```

Per-network event counts by type:

```sql
SELECT network, event_type, count() AS n, max(block_number) AS head
FROM extractor.events
GROUP BY network, event_type
ORDER BY network, event_type;
```

Failed transactions of a window:

```sql
SELECT block_number, tx_hash, tx_from, tx_value
FROM extractor.events FINAL
WHERE tx_status = 'reverted'
  AND captured_at >= now() - INTERVAL 1 HOUR;
```

Transactions with their block time (join on the block):

```sql
SELECT t.network, t.tx_hash, b.block_timestamp
FROM extractor.events AS t
INNER JOIN extractor.events AS b
    ON b.network = t.network AND b.block_number = t.block_number AND b.event_type = 'block'
WHERE t.event_type = 'transaction'
LIMIT 100;
```

Inspect what the mapping kept of any event:

```sql
SELECT raw FROM extractor.events WHERE event_id = '...';
```

## Semantics and limits

- **At-least-once**: a crash between insert and ack replays rows;
  `ReplacingMergeTree` collapses duplicates during merges (`SELECT ...
  FINAL` sees the collapsed view immediately). Redelivered copies of
  messages still pending in a batch are dropped before insert.
- **Reorg updates**: an event replayed with a new `kind` (e.g.
  `removed`) eventually replaces the previous row because it shares the
  `(network, event_type, block_number, entity)` key and has a higher
  `ingested_at`.
- **Poison messages**: a body that cannot be decoded, a subject that
  does not match the configured filter prefix, a transaction without a
  hash, or a block timestamp outside the supported range is terminated —
  not redelivered — and logged as `EVENT_REJECTED`; the message itself
  stays in the stream. A row that survives parsing but cannot be
  inserted is isolated by batch bisection and terminated as
  `ROW_EVICTED` (only when the server is reachable, so a ClickHouse
  outage never evicts data).
- **Backpressure**: each stream's consumer stops receiving above
  `SINK_MAX_ACK_PENDING` unacked messages; if ClickHouse is down, the
  sink keeps retrying the same batch while JetStream retains everything.
