// SQLite sink: subscribes to NATS JetStream, writes everything to a
// SQLite database for easy browsing, and serves a simple HTTP query API.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nats-io/nats.go"
)

type TxEvent struct {
	Network  string `json:"network"`
	Block    int64  `json:"block"`
	TxHash   string `json:"tx_hash"`
	From     string `json:"from_addr"`
	To       string `json:"to_addr"`
	Value    string `json:"value"`
	Type     string `json:"type"`
	Status   string `json:"status"`
}

type BalanceEvent struct {
	Network      string `json:"network"`
	Block        int64  `json:"block"`
	Address      string `json:"address"`
	BalanceAfter string `json:"balance_after"`
	Change       string `json:"change"`
}

func main() {
	natsURL := env("NATS_URL", "nats://localhost:4222")
	dbPath := env("DB_PATH", "/data/demo.db")

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	initSchema(db)

	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatal(err)
	}
	defer nc.Close()

	// Subscribe to all extractor subjects
	nc.Subscribe("extractor.>", func(m *nats.Msg) {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		subject := m.Subject

		// Determine event type from subject
		switch {
		case contains(subject, "transaction"):
			var tx TxEvent
			if json.Unmarshal(m.Data, &tx) == nil {
				db.Exec(`INSERT OR REPLACE INTO transactions
					(timestamp, subject, network, block, tx_hash, from_addr, to_addr, value, type, status)
					VALUES(?,?,?,?,?,?,?,?,?,?)`,
					now, subject, tx.Network, tx.Block, tx.TxHash, tx.From, tx.To, tx.Value, tx.Type, tx.Status)
			}
		case contains(subject, "balance"):
			var bal BalanceEvent
			if json.Unmarshal(m.Data, &bal) == nil {
				db.Exec(`INSERT OR REPLACE INTO balances
					(timestamp, subject, network, block, address, balance_after, change)
					VALUES(?,?,?,?,?,?,?)`,
					now, subject, bal.Network, bal.Block, bal.Address, bal.BalanceAfter, bal.Change)
			}
		default:
			// Store raw events
			db.Exec(`INSERT INTO events(timestamp, subject, data) VALUES(?,?,?)`,
				now, subject, string(m.Data))
		}
	})

	// Simple HTTP query endpoint
	http.HandleFunc("/recent", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		rows, _ := db.Query(`SELECT timestamp, network, block, tx_hash, from_addr, to_addr, value, status
			FROM transactions ORDER BY timestamp DESC LIMIT 50`)
		defer rows.Close()
		fmt.Fprintf(w, "[")
		first := true
		for rows.Next() {
			var ts, net, hash, from, to, val, status string
			var block int64
			rows.Scan(&ts, &net, &block, &hash, &from, &to, &val, &status)
			if !first {
				fmt.Fprintf(w, ",")
			}
			first = false
			json.NewEncoder(w).Encode(map[string]interface{}{
				"timestamp": ts, "network": net, "block": block,
				"tx_hash": hash, "from": from, "to": to, "value": val, "status": status,
			})
		}
		fmt.Fprintf(w, "]")
	})

	http.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var txCount, balCount, evtCount int
		db.QueryRow("SELECT COUNT(*) FROM transactions").Scan(&txCount)
		db.QueryRow("SELECT COUNT(*) FROM balances").Scan(&balCount)
		db.QueryRow("SELECT COUNT(*) FROM events").Scan(&evtCount)
		json.NewEncoder(w).Encode(map[string]int{
			"transactions": txCount, "balances": balCount, "events": evtCount,
		})
	})

	log.Println("Sink listening on :8081, NATS:", natsURL, "DB:", dbPath)
	log.Fatal(http.ListenAndServe(":8081", nil))
}

func initSchema(db *sql.DB) {
	db.Exec(`CREATE TABLE IF NOT EXISTS transactions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT NOT NULL,
		subject TEXT NOT NULL,
		network TEXT NOT NULL,
		block INTEGER NOT NULL,
		tx_hash TEXT NOT NULL,
		from_addr TEXT NOT NULL,
		to_addr TEXT NOT NULL,
		value TEXT NOT NULL,
		type TEXT,
		status TEXT
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS balances (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT NOT NULL,
		subject TEXT NOT NULL,
		network TEXT NOT NULL,
		block INTEGER NOT NULL,
		address TEXT NOT NULL,
		balance_after TEXT NOT NULL,
		change TEXT NOT NULL
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT NOT NULL,
		subject TEXT NOT NULL,
		data TEXT NOT NULL
	)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_tx_block ON transactions(network, block)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_bal_addr ON balances(network, address)`)
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > len(sub) &&
		(s[:len(sub)] == sub || s[len(s)-len(sub):] == sub ||
			indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
