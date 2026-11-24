// The durable state: one SQLite database (modernc.org/sqlite, pure Go,
// CGO-free) holding the event window, the canonical heads, the reorg
// detection window and the webhook delivery queue. A NATS message is acked
// only after the transaction that records its effects here has committed,
// so a crash replays the message but never loses the work.
package main

import (
	"database/sql"
	"fmt"

	// The pure-Go, CGO-free SQLite driver (registered as "sqlite").
	_ "modernc.org/sqlite"
)

// schema is idempotent; it is applied at every start.
const schema = `
CREATE TABLE IF NOT EXISTS events (
    network      TEXT NOT NULL,
    block_number INTEGER NOT NULL,
    event_type   TEXT NOT NULL,
    entity       TEXT NOT NULL,
    payload      TEXT NOT NULL,
    captured_at  TEXT NOT NULL,
    PRIMARY KEY (network, event_type, entity)
);
CREATE INDEX IF NOT EXISTS events_window ON events (network, event_type, block_number);

CREATE TABLE IF NOT EXISTS heads (
    network      TEXT PRIMARY KEY,
    block_number INTEGER NOT NULL,
    block_hash   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS blocks (
    network      TEXT NOT NULL,
    block_number INTEGER NOT NULL,
    block_hash   TEXT NOT NULL,
    parent_hash  TEXT NOT NULL,
    PRIMARY KEY (network, block_number)
);

CREATE TABLE IF NOT EXISTS deliveries (
    subscription_id TEXT NOT NULL,
    event_key       TEXT NOT NULL,
    event_kind      TEXT NOT NULL,
    network         TEXT NOT NULL,
    block_number    INTEGER NOT NULL,
    entity          TEXT NOT NULL DEFAULT '',
    webhook_id      TEXT NOT NULL,
    payload         TEXT NOT NULL,
    state           TEXT NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    last_response   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (subscription_id, event_key, event_kind)
);
CREATE INDEX IF NOT EXISTS deliveries_due ON deliveries (state, next_attempt_at);
CREATE INDEX IF NOT EXISTS deliveries_range ON deliveries (network, state, block_number);
CREATE INDEX IF NOT EXISTS deliveries_key ON deliveries (event_key);

CREATE TABLE IF NOT EXISTS replaced (
    network    TEXT NOT NULL,
    entity     TEXT NOT NULL,
    horizon    INTEGER NOT NULL,
    reappeared INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (network, entity)
);
`

// openState opens the SQLite database and converges the schema. WAL plus
// a busy timeout let the engine and the dispatcher share the file with
// short transactions instead of a global database lock.
func openState(path string) (*sql.DB, error) {
	dsn := "file:" + path +
		"?_pragma=busy_timeout(10000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(OFF)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open state %s: %w", path, err)
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return db, nil
}

// deliveryRow is one row of the webhook queue as the dispatcher sees it.
type deliveryRow struct {
	SubscriptionID string
	EventKey       string
	EventKind      string
	Network        string
	BlockNumber    int64
	Entity         string
	WebhookID      string
	Payload        string
	Attempts       int64
}

// delivery states.
const (
	statePending     = "pending"     // waiting for its first or next attempt
	stateSuperseded  = "superseded"  // its block was reorged out before delivery
	stateDelivered   = "delivered"   // the endpoint answered 2xx
	stateRemoved     = "removed"     // superseded and settled (event_removed emitted or reappearance)
	stateInvalidated = "invalidated" // was delivered, then its block was reorged out
	stateFailed      = "failed"      // attempts exhausted; kept for inspection
)

// event kinds (the X-Webhook-Event header values).
const (
	kindTransaction      = "transaction"
	kindBlock            = "block"
	kindReorg            = "reorg"
	kindEventRemoved     = "event_removed"
	kindEventInvalidated = "event_invalidated"
)
