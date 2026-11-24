package main

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleConfig = `# Bonpay webhook gateway subscriptions
subscriptions:
  - id: payin-watcher          # stable string id
    network: eth-sepolia
    event_type: transaction
    tx_to: "0x42eD89C15BfF1Ec17d5cB5E6D2aA0bBc93A0fF11"
    confirmations: 12
    url: https://client.example/hooks/payin
    secret: whsec_payin
  - id: all-blocks
    network: "*"
    event_type: block
    confirmations: 0
    url: http://127.0.0.1:9000/hooks/blocks
`

func TestParseSubscriptions(t *testing.T) {
	subs, err := parseSubscriptions(sampleConfig)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("got %d subscriptions want 2", len(subs))
	}
	s := subs[0]
	if s.ID != "payin-watcher" || s.Network != "eth-sepolia" || s.EventType != "transaction" {
		t.Fatalf("sub0 identity: %+v", s)
	}
	if s.Confirmations != 12 || s.URL != "https://client.example/hooks/payin" || s.Secret != "whsec_payin" {
		t.Fatalf("sub0 fields: %+v", s)
	}
	if s.TxTo != "0x42eD89C15BfF1Ec17d5cB5E6D2aA0bBc93A0fF11" {
		t.Fatalf("sub0 tx_to: %q", s.TxTo)
	}
	b := subs[1]
	if b.Network != "*" || b.EventType != "block" || b.Confirmations != 0 {
		t.Fatalf("sub1: %+v", b)
	}
	if !b.MatchesNetwork("anything") {
		t.Error("\"*\" network must match everything")
	}
	if !b.MatchesEventType("block") || b.MatchesEventType("transaction") {
		t.Error("block event_type filter")
	}
	if !s.MatchesNetwork("eth-sepolia") || s.MatchesNetwork("bsc-testnet") {
		t.Error("network filter")
	}
}

func TestParseSubscriptionsDefaults(t *testing.T) {
	subs, err := parseSubscriptions("subscriptions:\n  - id: x\n    url: http://x/\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := subs[0]
	if s.Network != "*" || s.EventType != "*" || s.Confirmations != 0 {
		t.Fatalf("defaults: %+v", s)
	}
}

func TestParseSubscriptionsErrors(t *testing.T) {
	cases := []string{
		"",                                     // empty
		"subscriptions:\n",                     // no items
		"other: 1\n",                           // unsupported top-level key
		"subscriptions:\n  - url: http://x/\n", // no id
		"subscriptions:\n  - id: a\n    url: http://x/\n  - id: a\n    url: http://y/\n", // dup id
		"subscriptions:\n  - id: a\n",                                         // no url
		"subscriptions:\n  - id: a\n    url: ftp://x/\n",                      // bad url scheme
		"subscriptions:\n  - id: a\n    url: http://x/\n    event_type: tx\n", // bad event_type
		"subscriptions:\n  - id: a\n    url: http://x/\n    confirmations: -1\n",
		"subscriptions:\n  - id: a\n    url: http://x/\n    confirmations: abc\n",
		"subscriptions:\n  - id: a\n    url: http://x/\n    tx_to: 0x1\n    event_type: block\n", // tx_to needs transaction
		"subscriptions:\n  - id: a\n    url: http://x/\n    unknown_key: 1\n",
		"subscriptions:\n    id: a\n", // item content before list dash
	}
	for i, text := range cases {
		if _, err := parseSubscriptions(text); err == nil {
			t.Errorf("case %d accepted: %q", i, text)
		}
	}
}

func TestRegistry(t *testing.T) {
	subs, err := parseSubscriptions(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	reg := newRegistry(subs)
	if len(reg.IDs()) != 2 {
		t.Fatalf("ids: %v", reg.IDs())
	}
	if reg.Find("payin-watcher") == nil || reg.Find("nope") != nil {
		t.Error("Find")
	}
	reg.Swap([]subscription{{ID: "only", URL: "http://x/"}})
	if len(reg.Snapshot()) != 1 || reg.Snapshot()[0].ID != "only" {
		t.Fatal("swap")
	}
	// Snapshot is a copy: mutating it must not leak into the registry.
	snap := reg.Snapshot()
	snap[0].ID = "mutated"
	if reg.Snapshot()[0].ID != "only" {
		t.Fatal("snapshot leaks")
	}
}

func TestLoadSubscriptionsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subs.yml")
	if err := os.WriteFile(path, []byte(sampleConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSubscriptions(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := loadSubscriptions(filepath.Join(dir, "missing.yml")); err == nil {
		t.Fatal("missing file accepted")
	}
	if err := os.WriteFile(path, []byte("subscriptions:\n  - id: a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSubscriptions(path); err == nil {
		t.Fatal("item without url accepted")
	}
}
