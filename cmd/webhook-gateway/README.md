# webhook-gateway

A standalone service that turns extractor NATS JetStream events into signed
webhook notifications. Clients subscribe to event types in a YAML config
and receive HTTPS POSTs either immediately or after N block confirmations;
when a reorg replaces blocks the gateway tells them the event became bad.
It is deliberately separate from the extractor binaries: it only reads the
extractor streams and its only dependencies are the pinned NATS client and
the pure-Go SQLite driver (no CGO).

```
extractor adapter --> NATS JetStream --> webhook-gateway --> client webhooks
                     extractor.<network>.<type>   (HMAC-signed JSON POSTs)
```

## How it works

1. Connects to NATS (retrying until it is up) and lists all streams,
   picking every stream whose subjects intersect `extractor.>` (token
   wildcard intersection, the same discovery the ClickHouse sink uses).
   The live topology is multi-stream - one stream per network
   (`EXTRACTOR_<NETWORK>`) - so the gateway runs one durable consumer per
   stream named `<GATEWAY_ID>-<STREAM>` (for example
   `webhook-gateway-EXTRACTOR_ETH_SEPOLIA`), consumes them concurrently,
   and re-lists every 30 seconds so streams that appear or disappear at
   runtime are followed without a restart. The consumers are created or
   updated explicitly with FilterSubject `extractor.>`, DeliverAll and
   explicit acks, and they are never deleted on drain, so a restart
   resumes from the ack floor. DeliverAll replays at most the five
   minutes of stream retention, so recovery after downtime is bounded.
2. Parses each event body (the adapter envelope: block bodies carry
   number/hash/parent_hash/timestamp/gas_used/tx_count, transaction
   bodies carry block_number/block_hash/tx_index/hash/from/to/value/gas/
   status, and both carry family/kind/source_sequence/entity_ordinal/
   descriptor_digest/record_digest; network and event type come from the
   subject). The field mapping mirrors cmd/clickhouse-sink so both
   consumers read the stream the same way. A message is acked only after
   its effects are committed to SQLite; a broken subject or body is
   terminated (EVENT_REJECTED) and stays in the stream for inspection.
3. Keeps state in one SQLite database: the event window, the canonical
   head per network, the block-hash window (2000 heights per network -
   the reorg detection window) and the delivery queue. State older than
   the window is pruned once a minute.
4. Runs the confirmations matcher on every event arrival: an event at
   height h becomes deliverable for a subscription when the canonical
   head reaches h + N (N is the subscription's `confirmations`; 0
   delivers on arrival). The matcher is idempotent - the queue primary
   key (subscription_id, event_key, event_kind) guarantees a logical
   notification is sent once per process life.
5. Detects reorgs from block events alone (see *Reorg semantics* below)
   and announces them: one `reorg` webhook per subscription for the
   replaced range, `event_invalidated` for already-delivered events, and
   `event_removed` for pending events that never came back.
6. Delivers through a dispatcher goroutine with a worker pool of four:
   HTTP POST, 10 second timeout per attempt, exponential backoff 1s, 2s,
   4s, 8s, then a 15s cap, at most 8 attempts, then the row parks as
   failed in the database for inspection. Delivery is at-least-once: a
   crash between a successful POST and its commit can resend once after
   a restart.
7. Reloads the subscriptions file on SIGHUP: the running set is swapped
   atomically, new subscriptions immediately see the events already in
   the window, and the pending work of removed subscriptions is dropped.
   A broken file keeps the running set untouched (CONFIG_ERROR).
8. On SIGTERM/SIGINT it drains every consume handle, gives in-flight
   handlers a moment to commit and ack, and closes the database.

Log lines are plain key=value records on stdout (GATEWAY_READY,
CONSUMER_STARTED, WEBHOOK_DELIVERED sub=... status=200, WEBHOOK_RETRY,
WEBHOOK_FAILED, REORG_DETECTED network=... range=..., EVENT_REMOVED_QUEUED,
CONFIG_RELOADED, GATEWAY_STOPPED).

## Environment variables

| Variable       | Default                | Meaning                          |
|----------------|------------------------|----------------------------------|
| NATS_URL       | nats://localhost:4222  | NATS connect URL                 |
| WEBHOOK_CONFIG | ./webhook-gateway.yml  | subscriptions file (--config overrides) |
| WEBHOOK_STATE  | ./webhook-gateway.db   | SQLite state path                |
| GATEWAY_ID     | webhook-gateway        | durable-consumer name prefix     |

## Configuration reference

The config file holds one list of subscriptions (see
webhook-gateway.example.yml):

```yaml
subscriptions:
  - id: payin-watcher          # stable string id (required, unique)
    network: eth-sepolia       # network subject token, or "*" for all (default "*")
    event_type: transaction    # block | transaction | "*" (default "*")
    tx_to: "0x42eD..."         # optional: only transactions whose "to" equals
                               # this address (case-insensitive hex compare);
                               # requires event_type transaction
    confirmations: 12          # deliver when head >= height + N; 0 = immediate (default 0)
    url: https://client.example/hooks/payin   # required, http or https
    secret: whsec_...          # HMAC-SHA256 key; empty omits the signature header
```

`confirmations` must be smaller than the 2000-height state window: the
matcher only scans events inside the window, so an N of 2000 or more could
never fire and is rejected at load time with a clear error.

The parser understands exactly this subset of YAML (a top-level
`subscriptions:` list of flat scalar maps, quoted or bare values, blank
lines and full-line or trailing comments); no YAML library is pulled in.
Reload with `kill -HUP <pid>`.

## Webhook contract

Every notification is one HTTP POST with Content-Type application/json
and these headers:

| Header               | Value                                              |
|----------------------|----------------------------------------------------|
| X-Webhook-Id         | stable UUIDv4 identity of the logical notification |
| X-Webhook-Event      | transaction, block, reorg, event_removed or event_invalidated |
| X-Webhook-Delivery   | attempt number (1 on the first try)                |
| X-Webhook-Signature  | "sha256=" + hex(HMAC-SHA256(secret, body)) over the exact body bytes |

A receiver should verify the signature before trusting the body and may
deduplicate by X-Webhook-Id. Respond 2xx to accept; anything else (or a
timeout) consumes one of the eight attempts.

Transaction payload:

```json
{
  "type": "transaction",
  "network": "eth-sepolia",
  "chain_id": 11155111,
  "block_number": 123,
  "block_hash": "0x...",
  "tx_hash": "0x...",
  "tx_index": 0,
  "from": "0x...",
  "to": "0x...",
  "value": "1000000000000000000",
  "gas": 21000,
  "status": "success",
  "confirmations": 12,
  "kind": "applied",
  "captured_at": "2026-11-24T00:00:00Z"
}
```

`to` is null for contract creations. `status` maps the receipt status: 1
to success, 0 to reverted, null or absent to unknown. `confirmations` is
the N the subscription configured (not the observed depth). `chain_id`
comes from the known network table (eth-sepolia 11155111, eth-holesky
17000, eth-mainnet 1, bsc-testnet 97, bsc-mainnet 56, poly-testnet 80002,
poly-mainnet 137; an explicit envelope chain_id wins). Block payloads
carry type, network, chain_id, block_number, block_hash, parent_hash,
timestamp, gas_used, tx_count, confirmations, kind and captured_at.

## Reorg semantics

Detection is self-contained from block events only. The gateway keeps a
per-network map height to hash for the last 2000 heights. When a block
event arrives at height H whose hash differs from the stored hash at H,
or whose parent hash mismatches the stored hash at H-1 while the block
itself is canonical going forward, a reorg is detected for the affected
range [divergence .. old_head]:

1. Every subscription matching the network receives one `reorg`
   webhook:

   ```json
   {
     "type": "reorg",
     "network": "eth-sepolia",
     "from_height": 101,
     "to_height": 102,
     "replaced_common_height": 100
   }
   ```

   from_height/to_height bound the replaced range;
   replaced_common_height is the last height that stayed canonical on
   both sides of the fork.

2. Pending (not yet delivered) notifications in the range are
   superseded. If the same entity (tx hash) re-appears in the rebuilt
   chain, it is delivered fresh from the new events - the client sees
   the transaction again under its new block hash. If it does not come
   back, the subscription receives `event_removed` once the rebuilt
   chain has moved strictly past the old head (so the rebuild is known
   complete):

   ```json
   {
     "type": "event_removed",
     "network": "eth-sepolia",
     "reason": "reorg",
     "event": { ...the original notification payload... }
   }
   ```

3. Delivered events in the range are the owner's "event became bad"
   case: each gets an `event_invalidated` webhook (and if the same event
   comes back in the rebuilt chain, a fresh transaction notification
   follows it):

   ```json
   {
     "type": "event_invalidated",
     "network": "eth-sepolia",
     "reason": "reorg",
     "replaced_by_height": 101,
     "webhook_id": "the original X-Webhook-Id",
     "event": { ...the original notification payload... }
   }
   ```

These notifications use the same retry queue as everything else. Reorgs
deeper than the 2000-height window cannot be detected; a block below the
window with an unknown hash is dropped (BLOCK_STALE) rather than
silently regressing the head. Events that had not reached any
subscription's confirmation depth simply leave the window with no
notification - no client ever heard of them.

## Running a test receiver

A one-file Python receiver that verifies signatures:

```python
import hashlib, hmac, json
from http.server import BaseHTTPRequestHandler, HTTPServer

SECRET = b"whsec_test"

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        want = "sha256=" + hmac.new(SECRET, body, hashlib.sha256).hexdigest()
        ok = hmac.compare_digest(self.headers.get("X-Webhook-Signature", ""), want)
        print(self.headers.get("X-Webhook-Event"), "sig", "OK" if ok else "FAIL", body.decode())
        self.send_response(200 if ok else 401)
        self.end_headers()
    def log_message(self, *a):
        pass

HTTPServer(("127.0.0.1", 9000), H).serve_forever()
```

Run it, point a subscription's url at http://127.0.0.1:9000/ with secret
whsec_test, start the gateway, and watch the POSTs:

```
python3 receiver.py &
NATS_URL=nats://localhost:4222 \
WEBHOOK_CONFIG=./webhook-gateway.yml \
WEBHOOK_STATE=./webhook-gateway.db \
go run .
```

To exercise a delivery retry by hand, point a subscription at a closed
port, publish an event, and watch WEBHOOK_RETRY lines with the 1s/2s/4s
backoff; after eight attempts the row is WEBHOOK_FAILED and stays in the
deliveries table with its last_response for inspection.

## State and durability

- events(network, block_number, event_type, entity, payload,
  captured_at): the pending/event window, pruned to the last 2000
  heights per network.
- heads(network, block_number, block_hash): the canonical head per
  network; the confirmation reference.
- blocks(network, block_number, block_hash, parent_hash): the
  block-hash window per network; the reorg detection window.
- deliveries(subscription_id, event_key, event_kind, state, attempts,
  next_attempt_at, last_response, plus the payload and addressing
  columns): the durable webhook queue. event_key is
  network:block:entity, where entity is tx_hash:tx_index for
  transactions and the block hash for blocks.
- replaced(network, entity, horizon, reappeared): the pending
  reappearance ledger while a reorg rebuild is in flight.

State rows whose height left the window are pruned (terminal delivery
states only); pending and superseded rows survive until settled. The
database is SQLite in WAL mode via modernc.org/sqlite, so the service
builds and runs without CGO.

## Docker

The image is one static binary configured by environment variables and
the mounted config file:

```
docker build -t webhook-gateway .
docker run --rm \
  -e NATS_URL=nats://nats:4222 \
  -e WEBHOOK_CONFIG=/etc/webhook-gateway/webhook-gateway.yml \
  -v $PWD/webhook-gateway.example.yml:/etc/webhook-gateway/webhook-gateway.yml:ro \
  webhook-gateway
```

Reload the config inside a container with
`docker kill -s HUP <container>`.
