# Extractor Demo Environment

Quick-start demo for visualizing the bc-indexer data pipeline.

## Start

```bash
cd demo
docker compose up -d
```

## What you get

| URL | What |
|-----|------|
| http://localhost:8080 | **Web dashboard** — live transaction feed with stats |
| http://localhost:8222 | **NATS monitoring** — JetStream streams, consumers, lag |
| http://localhost:8081/recent | Recent transactions (JSON) |
| http://localhost:8081/stats | Event counts (JSON) |

## SQLite database

The sink service writes everything to `/data/demo.db` (Docker volume `sink-data`).

To browse with sqlite3:
```bash
# From inside the container
docker compose exec sink sqlite3 /data/demo.db "SELECT * FROM transactions ORDER BY timestamp DESC LIMIT 20;"

# Or copy it out
docker compose cp sink:/data/demo.db ./demo.db
open demo.db  # with DB Browser for SQLite / TablePlus
```

Useful queries:
```sql
-- Latest transactions per network
SELECT network, MAX(block) as latest_block, COUNT(*) as tx_count
FROM transactions GROUP BY network;

-- Balance history for an address
SELECT block, balance_after, change FROM balances
WHERE address = '0x...' AND network = 'ETH'
ORDER BY block;

-- Reverted transactions
SELECT network, block, tx_hash FROM transactions
WHERE status = 'reverted' ORDER BY block DESC;
```

## Scaling mock producers

```bash
docker compose up -d --scale mock-producer=3
```

Each producer generates transactions for all networks at 2-second intervals.

## Configuration

| Env var | Default | Description |
|---------|---------|-------------|
| `NATS_URL` | `nats://nats:4222` | NATS server |
| `NETWORKS` | `ETH,BSC,POLY` | Which networks to simulate |
| `TX_INTERVAL` | `2s` | How often to generate transactions |
| `DB_PATH` | `/data/demo.db` | SQLite database path |

## Stopping

```bash
docker compose down -v  # -v also removes data volumes
```
