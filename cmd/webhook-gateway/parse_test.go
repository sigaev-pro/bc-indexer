package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSplitSubject(t *testing.T) {
	cases := []struct {
		subject        string
		network, etype string
		ok             bool
	}{
		{"extractor.eth-sepolia.block", "eth-sepolia", "block", true},
		{"extractor.bsc-testnet.transaction", "bsc-testnet", "transaction", true},
		{"extractor.poly-testnet.transaction", "poly-testnet", "transaction", true},
		{"evm.v3.eth-sepolia.tx", "", "", false},
		{"extractor.eth-sepolia", "", "", false},
		{"other.eth-sepolia.block", "", "", false},
		{"extractor..block", "", "", false},
	}
	for _, c := range cases {
		network, etype, ok := splitSubject(c.subject)
		if ok != c.ok || network != c.network || etype != c.etype {
			t.Errorf("splitSubject(%q) = %q,%q,%v want %q,%q,%v",
				c.subject, network, etype, ok, c.network, c.etype, c.ok)
		}
	}
}

func TestSubjectsIntersect(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"extractor.>", "extractor.eth-sepolia.>", true},
		{"extractor.>", "extractor.eth-sepolia.block", true},
		{"extractor.>", "evm.v3.eth-sepolia.tx", false},
		{"extractor.>", "extractor.>", true},
		{"extractor.*.block", "extractor.eth-sepolia.block", true},
		{"extractor.*.block", "extractor.eth-sepolia.transaction", false},
		{"extractor.eth-sepolia.>", "extractor.bsc-testnet.>", false},
		{"extractor.>", "other.>", false},
		{"extractor.a.b", "extractor.a.b.c", false},
		{"extractor.a.>", "extractor.a.b.c", true},
	}
	for _, c := range cases {
		got := subjectsIntersect(split(c.a), split(c.b))
		if got != c.want {
			t.Errorf("subjectsIntersect(%q, %q) = %v want %v", c.a, c.b, got, c.want)
		}
	}
}

func split(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func TestDurableName(t *testing.T) {
	cs := &consumerSet{gatewayID: "webhook-gateway"}
	if got := cs.durableName("EXTRACTOR_ETH_SEPOLIA"); got != "webhook-gateway-EXTRACTOR_ETH_SEPOLIA" {
		t.Fatalf("durableName = %q", got)
	}
}

// blockEnvelope builds an adapter-shaped block body plus envelope.
func blockEnvelope(network string, number uint64, hash, parent string) string {
	return `{"schema":"extractor.rpc.v1","network":` + quote(network) +
		`,"family":"block","kind":"applied","decision_sequence":1,"entity_ordinal":0,` +
		`"source_sequence":7,"descriptor_digest":"aa11","record_digest":"bb22",` +
		`"number":` + itoa(number) + `,"hash":` + quote(hash) + `,"parent_hash":` + quote(parent) +
		`,"timestamp":1732000000,"gas_used":30000,"tx_count":2}`
}

// txEnvelope builds an adapter-shaped transaction body plus envelope.
func txEnvelope(network string, block uint64, blockHash string, index uint64,
	hash, from, to, value string, status any) string {
	statusJSON, _ := json.Marshal(status)
	return `{"schema":"extractor.rpc.v1","network":` + quote(network) +
		`,"family":"tx","kind":"applied","decision_sequence":1,"entity_ordinal":` + itoa(index+1) +
		`,"source_sequence":8,"descriptor_digest":"cc33","record_digest":"dd44",` +
		`"block_number":` + itoa(block) + `,"block_hash":` + quote(blockHash) +
		`,"tx_index":` + itoa(index) + `,"hash":` + quote(hash) + `,"from":` + quote(from) +
		`,"to":` + (map[bool]string{true: quote(to), false: "null"}[to != ""]) +
		`,"value":` + quote(value) + `,"gas":21000,"status":` + string(statusJSON) + `}`
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }
func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func mustParse(t *testing.T, subject, body string) *parsedEvent {
	t.Helper()
	ev, err := parseMessage(subject, []byte(body), time.Unix(1732000000, 0).UTC())
	if err != nil {
		t.Fatalf("parseMessage(%s): %v", subject, err)
	}
	return ev
}

func TestParseBlockEnvelope(t *testing.T) {
	ev := mustParse(t, "extractor.eth-sepolia.block", blockEnvelope("eth-sepolia", 123, "0xa1", "0xa0"))
	if ev.network != "eth-sepolia" || ev.eventType != "block" || ev.kind != "applied" {
		t.Fatalf("identity: %+v", ev)
	}
	if ev.blockNumber != 123 || ev.blockHash != "0xa1" || ev.parentHash != "0xa0" {
		t.Fatalf("body: %+v", ev)
	}
	if ev.timestamp != 1732000000 || ev.gasUsed != 30000 || ev.txCount != 2 {
		t.Fatalf("counts: %+v", ev)
	}
	if ev.raw == "" || !json.Valid([]byte(ev.raw)) {
		t.Fatalf("raw bytes not preserved: %q", ev.raw)
	}
}

func TestParseTxEnvelope(t *testing.T) {
	ev := mustParse(t, "extractor.eth-sepolia.transaction",
		txEnvelope("eth-sepolia", 123, "0xa1", 4, "0xtx", "0xfrom", "0xto", "1000000000000000000", 1))
	if ev.eventType != "transaction" || ev.txHash != "0xtx" || ev.txIndex != 4 {
		t.Fatalf("body: %+v", ev)
	}
	if ev.to != "0xto" || ev.from != "0xfrom" || ev.value != "1000000000000000000" || ev.gas != 21000 {
		t.Fatalf("fields: %+v", ev)
	}
	if ev.status != "success" {
		t.Fatalf("status = %q want success", ev.status)
	}
	// null to (contract creation) and null status
	ev = mustParse(t, "extractor.eth-sepolia.transaction",
		txEnvelope("eth-sepolia", 123, "0xa1", 0, "0xtx2", "0xfrom", "", "0", nil))
	if ev.to != "" {
		t.Fatalf("null to decoded to %q", ev.to)
	}
	if ev.status != "unknown" {
		t.Fatalf("null status = %q want unknown", ev.status)
	}
	// status 0 -> reverted
	ev = mustParse(t, "extractor.eth-sepolia.transaction",
		txEnvelope("eth-sepolia", 123, "0xa1", 0, "0xtx3", "0xfrom", "0xto", "0", 0))
	if ev.status != "reverted" {
		t.Fatalf("status 0 = %q want reverted", ev.status)
	}
}

func TestParseRejects(t *testing.T) {
	if _, err := parseMessage("other.net.block", []byte("{}"), time.Now()); err == nil {
		t.Fatal("foreign subject accepted")
	}
	if _, err := parseMessage("extractor.net.block", []byte("{not json"), time.Now()); err == nil {
		t.Fatal("broken body accepted")
	}
	if _, err := parseMessage("extractor.net.block", []byte(`{"family":"block","kind":"applied"}`), time.Now()); err == nil {
		t.Fatal("block without hash accepted")
	}
	if _, err := parseMessage("extractor.net.transaction", []byte(`{"family":"tx","kind":"applied","hash":""}`), time.Now()); err == nil {
		t.Fatal("tx without hash accepted")
	}
	if _, err := parseMessage("extractor.net.balance", []byte(`{}`), time.Now()); err == nil {
		t.Fatal("unknown event type accepted")
	}
}

func TestNotificationTransaction(t *testing.T) {
	var p payload
	body := txEnvelope("eth-sepolia", 123, "0xa1", 4, "0xtx", "0xfrom", "0xTO", "5", 1)
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	note := notificationEvent(&p, "eth-sepolia", "transaction", "2026-01-02T03:04:05Z", 12)
	encoded, _ := json.Marshal(note)
	var m map[string]any
	if err := json.Unmarshal(encoded, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"type": "transaction", "network": "eth-sepolia", "chain_id": float64(11155111),
		"block_number": float64(123), "block_hash": "0xa1", "tx_hash": "0xtx",
		"tx_index": float64(4), "from": "0xfrom", "to": "0xTO", "value": "5",
		"gas": float64(21000), "status": "success", "confirmations": float64(12),
		"kind": "applied", "captured_at": "2026-01-02T03:04:05Z",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("payload[%q] = %v want %v", k, m[k], v)
		}
	}
	// null to stays null
	var p2 payload
	if err := json.Unmarshal([]byte(txEnvelope("eth-sepolia", 1, "0xb", 0, "0xt", "0xf", "", "0", nil)), &p2); err != nil {
		t.Fatal(err)
	}
	note2, _ := json.Marshal(notificationEvent(&p2, "eth-sepolia", "transaction", "t", 0))
	var m2 map[string]any
	_ = json.Unmarshal(note2, &m2)
	if _, exists := m2["to"]; !exists {
		t.Error("to key missing")
	} else if m2["to"] != nil {
		t.Errorf("to = %v want null", m2["to"])
	}
}

func TestNotificationBlockAndReorg(t *testing.T) {
	var p payload
	if err := json.Unmarshal([]byte(blockEnvelope("bsc-testnet", 9, "0xb9", "0xb8")), &p); err != nil {
		t.Fatal(err)
	}
	note, _ := json.Marshal(notificationEvent(&p, "bsc-testnet", "block", "t", 3))
	var m map[string]any
	_ = json.Unmarshal(note, &m)
	if m["type"] != "block" || m["chain_id"] != float64(97) || m["confirmations"] != float64(3) {
		t.Errorf("block note: %v", m)
	}
	if m["parent_hash"] != "0xb8" || m["gas_used"] != float64(30000) {
		t.Errorf("block fields: %v", m)
	}
	r, _ := json.Marshal(notificationReorg("eth-sepolia", 101, 102, 100))
	var rm map[string]any
	_ = json.Unmarshal(r, &rm)
	if rm["type"] != "reorg" || rm["from_height"] != float64(101) ||
		rm["to_height"] != float64(102) || rm["replaced_common_height"] != float64(100) {
		t.Errorf("reorg note: %v", rm)
	}
	inv, _ := json.Marshal(notificationInvalidated("eth-sepolia", 101, "wh-1", json.RawMessage(`{"a":1}`)))
	var im map[string]any
	_ = json.Unmarshal(inv, &im)
	if im["type"] != "event_invalidated" || im["reason"] != "reorg" ||
		im["replaced_by_height"] != float64(101) || im["webhook_id"] != "wh-1" {
		t.Errorf("invalidation note: %v", im)
	}
	rem, _ := json.Marshal(notificationRemoved("eth-sepolia", json.RawMessage(`{"b":2}`)))
	var rmm map[string]any
	_ = json.Unmarshal(rem, &rmm)
	if rmm["type"] != "event_removed" || rmm["reason"] != "reorg" {
		t.Errorf("removed note: %v", rmm)
	}
}

func TestChainIDTable(t *testing.T) {
	cases := map[string]uint64{
		"eth-sepolia": 11155111, "bsc-testnet": 97, "poly-testnet": 80002,
		"eth-mainnet": 1, "unknown-net": 0,
	}
	for net, want := range cases {
		if got := chainID(net, ""); got != want {
			t.Errorf("chainID(%q) = %d want %d", net, got, want)
		}
	}
	if got := chainID("unknown-net", "42"); got != 42 {
		t.Errorf("payload chain_id ignored: %d", got)
	}
}

func TestEventKey(t *testing.T) {
	if got := eventKey("eth-sepolia", 101, "0xtx:0"); got != "eth-sepolia:101:0xtx:0" {
		t.Fatalf("eventKey = %q", got)
	}
	if got := txEntity("0xtx", 3); got != "0xtx:3" {
		t.Fatalf("txEntity = %q", got)
	}
}
