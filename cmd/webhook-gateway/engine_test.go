package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// testEnv bundles an engine over a fresh database with a settable
// subscription registry.
type testEnv struct {
	db  *sql.DB
	reg *registry
	eng *engine
}

func newEnv(t *testing.T, subs ...subscription) *testEnv {
	t.Helper()
	db, err := openState(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	reg := newRegistry(subs)
	eng := newEngine(db, reg, 2000)
	return &testEnv{db: db, reg: reg, eng: eng}
}

func (e *testEnv) apply(t *testing.T, ev *parsedEvent) {
	t.Helper()
	if _, err := e.eng.ApplyEvent(context.Background(), ev); err != nil {
		t.Fatalf("apply %s/%d: %v", ev.eventType, ev.blockNumber, err)
	}
}

func (e *testEnv) block(t *testing.T, network string, n uint64, hash, parent string) {
	t.Helper()
	e.apply(t, mustParse(t, "extractor."+network+".block", blockEnvelope(network, n, hash, parent)))
}

func (e *testEnv) tx(t *testing.T, network string, block uint64, blockHash string, index uint64,
	hash, from, to, value string, status any) {
	t.Helper()
	e.apply(t, mustParse(t, "extractor."+network+".transaction",
		txEnvelope(network, block, blockHash, index, hash, from, to, value, status)))
}

// delivery returns the delivery row for (sub, event_key, kind) or nil.
func (e *testEnv) delivery(sub, key, kind string) *deliveryRow {
	var r deliveryRow
	err := e.db.QueryRow(`SELECT subscription_id, event_key, event_kind, network,
			block_number, entity, webhook_id, payload, attempts
			FROM deliveries WHERE subscription_id=? AND event_key=? AND event_kind=?`,
		sub, key, kind).
		Scan(&r.SubscriptionID, &r.EventKey, &r.EventKind, &r.Network,
			&r.BlockNumber, &r.Entity, &r.WebhookID, &r.Payload, &r.Attempts)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return nil
	}
	return &r
}

func (e *testEnv) deliveryState(sub, key, kind string) string {
	r := e.delivery(sub, key, kind)
	if r == nil {
		return ""
	}
	var state string
	e.db.QueryRow(`SELECT state FROM deliveries WHERE subscription_id=? AND event_key=? AND event_kind=?`,
		sub, key, kind).Scan(&state)
	return state
}

// markDelivered simulates a successful webhook POST for the row.
func (e *testEnv) markDelivered(sub, key, kind string) {
	_, _ = e.db.Exec(`UPDATE deliveries SET state='delivered', attempts=1 WHERE
		subscription_id=? AND event_key=? AND event_kind=?`, sub, key, kind)
}

// forceDue makes every pending row due immediately.
func (e *testEnv) forceDue() {
	_, _ = e.db.Exec(`UPDATE deliveries SET next_attempt_at=0 WHERE state=?`, statePending)
}

func sub(id string, confirmations int64, eventType string) subscription {
	return subscription{ID: id, Network: "eth-sepolia", EventType: eventType,
		Confirmations: confirmations, URL: "http://example.test/hook"}
}

func TestConfirmationsBoundary(t *testing.T) {
	// Two subscriptions on the same stream: conf 0 and conf 2.
	e := newEnv(t, sub("imm", 0, "transaction"), sub("two", 2, "transaction"))
	e.block(t, "eth-sepolia", 100, "0xh100", "0xh99")
	e.tx(t, "eth-sepolia", 100, "0xh100", 0, "0xtxA", "0xf", "0xA", "1", 1)

	// conf=0 delivers on arrival (head 100 exists: the block came first).
	if got := e.deliveryState("imm", "eth-sepolia:100:0xtxA:0", kindTransaction); got != statePending {
		t.Fatalf("immediate state = %q want pending", got)
	}
	// conf=2 must not deliver at head 100 or 101: head >= 100+2 only at 102.
	if r := e.delivery("two", "eth-sepolia:100:0xtxA:0", kindTransaction); r != nil {
		t.Fatalf("conf=2 delivered early at head 100")
	}
	e.block(t, "eth-sepolia", 101, "0xh101", "0xh100")
	if r := e.delivery("two", "eth-sepolia:100:0xtxA:0", kindTransaction); r != nil {
		t.Fatalf("conf=2 delivered early at head 101")
	}
	e.block(t, "eth-sepolia", 102, "0xh102", "0xh101")
	if got := e.deliveryState("two", "eth-sepolia:100:0xtxA:0", kindTransaction); got != statePending {
		t.Fatalf("conf=2 state at head 102 = %q want pending (queued)", got)
	}
	// The matcher is idempotent: re-applying the tx changes nothing.
	e.tx(t, "eth-sepolia", 100, "0xh100", 0, "0xtxA", "0xf", "0xA", "1", 1)
	var n int
	e.db.QueryRow(`SELECT count(*) FROM deliveries WHERE subscription_id='two'
		AND event_key='eth-sepolia:100:0xtxA:0'`).Scan(&n)
	if n != 1 {
		t.Fatalf("duplicate delivery rows: %d", n)
	}
	// The notification body carries the configured confirmations.
	r := e.delivery("imm", "eth-sepolia:100:0xtxA:0", kindTransaction)
	var body map[string]any
	if err := json.Unmarshal([]byte(r.Payload), &body); err != nil {
		t.Fatal(err)
	}
	if body["confirmations"] != float64(0) || body["type"] != "transaction" ||
		body["status"] != "success" || body["chain_id"] != float64(11155111) {
		t.Fatalf("immediate payload: %v", body)
	}
	r = e.delivery("two", "eth-sepolia:100:0xtxA:0", kindTransaction)
	_ = json.Unmarshal([]byte(r.Payload), &body)
	if body["confirmations"] != float64(2) {
		t.Fatalf("conf=2 payload confirmations: %v", body["confirmations"])
	}
}

func TestSubscriptionFilters(t *testing.T) {
	payin := subscription{ID: "payin", Network: "*", EventType: "transaction",
		TxTo: "0x42ed", Confirmations: 0, URL: "http://x/"}
	blocks := subscription{ID: "blocks", Network: "bsc-testnet", EventType: "block",
		Confirmations: 0, URL: "http://x/"}
	e := newEnv(t, payin, blocks)

	e.block(t, "eth-sepolia", 10, "0xe10", "0xe9")
	e.block(t, "bsc-testnet", 10, "0xb10", "0xb9")
	// tx to the watched address (lowercase filter, checksummed envelope)
	e.tx(t, "eth-sepolia", 10, "0xe10", 0, "0xhit", "0xf", "0x42ED", "1", 1)
	// tx to another address, plus a contract creation (to null)
	e.tx(t, "eth-sepolia", 10, "0xe10", 1, "0xmiss", "0xf", "0xother", "1", 1)
	e.tx(t, "eth-sepolia", 10, "0xe10", 2, "0xcreate", "0xf", "", "1", 1)

	if r := e.delivery("payin", "eth-sepolia:10:0xhit:0", kindTransaction); r == nil {
		t.Fatal("tx_to match missing (case-insensitive)")
	}
	if r := e.delivery("payin", "eth-sepolia:10:0xmiss:1", kindTransaction); r != nil {
		t.Fatal("tx_to mismatch delivered")
	}
	if r := e.delivery("payin", "eth-sepolia:10:0xcreate:2", kindTransaction); r != nil {
		t.Fatal("creation (null to) delivered to tx_to filter")
	}
	if r := e.delivery("blocks", "bsc-testnet:10:0xb10", kindBlock); r == nil {
		t.Fatal("block notification missing for the bsc block subscription")
	}
	if r := e.delivery("blocks", "eth-sepolia:10:0xe10", kindBlock); r != nil {
		t.Fatal("block delivered for a network the subscription does not cover")
	}
	if r := e.delivery("payin", "eth-sepolia:10:0xe10", kindBlock); r != nil {
		t.Fatal("block delivered to a transaction-only subscription")
	}
}

// buildChain applies blocks n0..n1 with derived hashes and one tx per
// block whose "to" is the given address.
func buildChain(t *testing.T, e *testEnv, network string, n0, n1 uint64) {
	for n := n0; n <= n1; n++ {
		hash, parent := blockHashes(n)
		e.block(t, network, n, hash, parent)
	}
}

func blockHashes(n uint64) (hash, parent string) {
	return "0xh" + itoa(n), "0xh" + itoa(n-1)
}

func TestReorgHashMismatch(t *testing.T) {
	star := subscription{ID: "star", Network: "*", EventType: "*",
		Confirmations: 0, URL: "http://x/"}
	e := newEnv(t, star)
	buildChain(t, e, "eth-sepolia", 100, 102)
	e.tx(t, "eth-sepolia", 101, "0xh101", 0, "0xtxR", "0xf", "0xA", "1", 1)
	e.tx(t, "eth-sepolia", 102, "0xh102", 0, "0xtxX", "0xf", "0xA", "1", 1)
	e.markDelivered("star", "eth-sepolia:101:0xtxR:0", kindTransaction)
	e.markDelivered("star", "eth-sepolia:102:0xtxX:0", kindTransaction)

	// Alternative 101' with a different hash, same parent (100).
	e.block(t, "eth-sepolia", 101, "0xh101prime", "0xh100")

	// Head moved to the new 101'; the old 102 left the window.
	var headNum int64
	var headHash string
	e.db.QueryRow(`SELECT block_number, block_hash FROM heads WHERE network='eth-sepolia'`).
		Scan(&headNum, &headHash)
	if headNum != 101 || headHash != "0xh101prime" {
		t.Fatalf("head = %d/%s want 101/0xh101prime", headNum, headHash)
	}
	var n int
	e.db.QueryRow(`SELECT count(*) FROM blocks WHERE network='eth-sepolia'`).Scan(&n)
	if n != 2 { // 100 and 101'
		t.Fatalf("blocks after reorg = %d want 2", n)
	}
	e.db.QueryRow(`SELECT count(*) FROM events WHERE network='eth-sepolia'`).Scan(&n)
	if n != 2 { // block 100, block 101'
		t.Fatalf("events after reorg = %d want 2", n)
	}

	// Delivered events in the replaced range became invalidated with an
	// event_invalidated notification each.
	if got := e.deliveryState("star", "eth-sepolia:101:0xtxR:0", kindTransaction); got != stateInvalidated {
		t.Fatalf("txR state = %q want invalidated", got)
	}
	if got := e.deliveryState("star", "eth-sepolia:102:0xtxX:0", kindTransaction); got != stateInvalidated {
		t.Fatalf("txX state = %q want invalidated", got)
	}
	inv := e.delivery("star", "eth-sepolia:101:0xtxR:0", kindEventInvalidated)
	if inv == nil {
		t.Fatal("event_invalidated for txR missing")
	}
	var invBody map[string]any
	if err := json.Unmarshal([]byte(inv.Payload), &invBody); err != nil {
		t.Fatal(err)
	}
	if invBody["type"] != "event_invalidated" || invBody["reason"] != "reorg" ||
		invBody["replaced_by_height"] != float64(101) {
		t.Fatalf("invalidation payload: %v", invBody)
	}
	event, _ := invBody["event"].(map[string]any)
	if event == nil || event["tx_hash"] != "0xtxR" {
		t.Fatalf("invalidation embeds no original event: %v", invBody["event"])
	}

	// One reorg notification for the range.
	var reorgRows int
	e.db.QueryRow(`SELECT count(*) FROM deliveries WHERE subscription_id='star' AND event_kind='reorg'`).
		Scan(&reorgRows)
	if reorgRows != 1 {
		t.Fatalf("reorg notifications = %d want 1", reorgRows)
	}
	var reorgPayload string
	e.db.QueryRow(`SELECT payload FROM deliveries WHERE subscription_id='star' AND event_kind='reorg'`).
		Scan(&reorgPayload)
	var reorgBody map[string]any
	if err := json.Unmarshal([]byte(reorgPayload), &reorgBody); err != nil {
		t.Fatal(err)
	}
	if reorgBody["from_height"] != float64(101) || reorgBody["to_height"] != float64(102) ||
		reorgBody["replaced_common_height"] != float64(100) {
		t.Fatalf("reorg payload: %v", reorgBody)
	}
}

func TestReorgParentMismatch(t *testing.T) {
	e := newEnv(t, sub("s", 0, "transaction"))
	buildChain(t, e, "eth-sepolia", 100, 102)
	// A block at 103 (no stored hash at its own height) whose parent is
	// NOT the stored 102: the divergence is 102, the parent's height.
	e.block(t, "eth-sepolia", 103, "0xalt103", "0xnever102")
	var n int
	e.db.QueryRow(`SELECT count(*) FROM blocks WHERE network='eth-sepolia'`).Scan(&n)
	if n != 3 { // 100, 101 and the new 103; 102 was the divergence and left
		t.Fatalf("blocks = %d want 3", n)
	}
	var headNum int64
	e.db.QueryRow(`SELECT block_number FROM heads WHERE network='eth-sepolia'`).Scan(&headNum)
	if headNum != 103 {
		t.Fatalf("head = %d want 103", headNum)
	}
	var reorgs int
	e.db.QueryRow(`SELECT count(*) FROM deliveries WHERE event_kind='reorg'`).Scan(&reorgs)
	if reorgs != 1 {
		t.Fatalf("reorg notifications = %d want 1", reorgs)
	}
	var reorgPayload string
	e.db.QueryRow(`SELECT payload FROM deliveries WHERE event_kind='reorg'`).Scan(&reorgPayload)
	if reorgPayload == "" || !strings.Contains(reorgPayload, `"from_height":102`) {
		t.Fatalf("reorg payload: %s", reorgPayload)
	}
}

func TestReorgSupersessionAndRemoval(t *testing.T) {
	s0 := sub("s0", 0, "transaction")
	slow := subscription{ID: "slow", Network: "*", EventType: "transaction",
		Confirmations: 12, URL: "http://x/"}
	e := newEnv(t, s0, slow)
	buildChain(t, e, "eth-sepolia", 100, 102)
	e.tx(t, "eth-sepolia", 101, "0xh101", 0, "0xtxR", "0xf", "0xA", "1", 1) // delivered below
	e.tx(t, "eth-sepolia", 102, "0xh102", 0, "0xtxC", "0xf", "0xB", "1", 1) // slow-pending
	e.markDelivered("s0", "eth-sepolia:101:0xtxR:0", kindTransaction)
	// slow has txC pending (conf 12 reached? head 102 < 114 -> not even queued)
	// Queue it artificially to represent a slow-confirmed pending row.
	_, _ = e.db.Exec(`INSERT INTO deliveries (subscription_id, event_key, event_kind, network,
		block_number, entity, webhook_id, payload, state, attempts, next_attempt_at, last_response)
		VALUES ('slow','eth-sepolia:102:0xtxC:0','transaction','eth-sepolia',102,'0xtxC','wh-c',
		'{"type":"transaction"}','pending',0,0,'')`)

	// Reorg: alternative 101' containing txR again (same hash), then 102', 103'.
	e.block(t, "eth-sepolia", 101, "0xh101p", "0xh100")
	e.tx(t, "eth-sepolia", 101, "0xh101p", 0, "0xtxR", "0xf", "0xA", "1", 1)

	// txR was delivered -> invalidated; its reappearance resets the row to
	// a fresh pending delivery under the new block hash.
	if got := e.deliveryState("s0", "eth-sepolia:101:0xtxR:0", kindTransaction); got != statePending {
		t.Fatalf("txR after reappearance = %q want pending (fresh)", got)
	}
	fresh := e.delivery("s0", "eth-sepolia:101:0xtxR:0", kindTransaction)
	var freshBody map[string]any
	_ = json.Unmarshal([]byte(fresh.Payload), &freshBody)
	if freshBody["block_hash"] != "0xh101p" {
		t.Fatalf("fresh txR payload block_hash = %v want 0xh101p", freshBody["block_hash"])
	}
	// slow's txC became superseded.
	if got := e.deliveryState("slow", "eth-sepolia:102:0xtxC:0", kindTransaction); got != stateSuperseded {
		t.Fatalf("txC state = %q want superseded", got)
	}

	// Rebuild past the horizon: 102' then 103'; the sweep fires once the
	// head is strictly beyond the old head (102).
	e.block(t, "eth-sepolia", 102, "0xh102p", "0xh101p")
	if r := e.delivery("slow", "eth-sepolia:102:0xtxC:0", kindEventRemoved); r != nil {
		t.Fatal("event_removed fired before the head passed the horizon")
	}
	e.block(t, "eth-sepolia", 103, "0xh103p", "0xh102p")

	// txC never re-appeared: the slow subscription gets event_removed.
	if got := e.deliveryState("slow", "eth-sepolia:102:0xtxC:0", kindTransaction); got != stateRemoved {
		t.Fatalf("txC state after sweep = %q want removed", got)
	}
	rem := e.delivery("slow", "eth-sepolia:102:0xtxC:0", kindEventRemoved)
	if rem == nil {
		t.Fatal("event_removed for txC missing")
	}
	var remBody map[string]any
	if err := json.Unmarshal([]byte(rem.Payload), &remBody); err != nil {
		t.Fatal(err)
	}
	if remBody["type"] != "event_removed" || remBody["reason"] != "reorg" {
		t.Fatalf("event_removed payload: %v", remBody)
	}
	if ev, _ := remBody["event"].(map[string]any); ev == nil {
		t.Fatalf("event_removed embeds no original event: %v", remBody["event"])
	}
	// The replaced ledger is clean.
	var n int
	e.db.QueryRow(`SELECT count(*) FROM replaced`).Scan(&n)
	if n != 0 {
		t.Fatalf("replaced rows left: %d", n)
	}
}

func TestReorgSupersededBlockNotifications(t *testing.T) {
	blocks := subscription{ID: "blocks", Network: "*", EventType: "block",
		Confirmations: 1, URL: "http://x/"}
	e := newEnv(t, blocks)
	buildChain(t, e, "eth-sepolia", 100, 103)
	// conf 1 at head 103: blocks 100, 101 and 102 are queued (threshold
	// 102), 103 is not yet.
	if got := e.deliveryState("blocks", "eth-sepolia:100:0xh100", kindBlock); got != statePending {
		t.Fatalf("block100 = %q", got)
	}
	if got := e.deliveryState("blocks", "eth-sepolia:102:0xh102", kindBlock); got != statePending {
		t.Fatalf("block102 = %q", got)
	}
	if r := e.delivery("blocks", "eth-sepolia:103:0xh103", kindBlock); r != nil {
		t.Fatal("block 103 queued before its confirmation depth")
	}
	// Reorg at 102 with a different hash.
	e.block(t, "eth-sepolia", 102, "0xh102p", "0xh101")
	if got := e.deliveryState("blocks", "eth-sepolia:102:0xh102", kindBlock); got != stateSuperseded {
		t.Fatalf("superseded block = %q", got)
	}
	if got := e.deliveryState("blocks", "eth-sepolia:100:0xh100", kindBlock); got != statePending {
		t.Fatalf("block outside range touched: %q", got)
	}
	// Rebuild: 103' (head 103 == horizon 103, no sweep yet), 104' (sweep).
	e.block(t, "eth-sepolia", 103, "0xh103p", "0xh102p")
	if got := e.deliveryState("blocks", "eth-sepolia:102:0xh102", kindBlock); got != stateSuperseded {
		t.Fatalf("sweep fired early: %q", got)
	}
	e.block(t, "eth-sepolia", 104, "0xh104p", "0xh103p")
	if got := e.deliveryState("blocks", "eth-sepolia:102:0xh102", kindBlock); got != stateRemoved {
		t.Fatalf("swept block = %q", got)
	}
	if r := e.delivery("blocks", "eth-sepolia:102:0xh102", kindEventRemoved); r == nil {
		t.Fatal("event_removed for the old 102 block notification missing")
	}
	// The replacement 102' notification exists and is distinct.
	if r := e.delivery("blocks", "eth-sepolia:102:0xh102p", kindBlock); r == nil {
		t.Fatal("fresh 102' block notification missing")
	}
}

func TestDuplicateBlockIsIgnored(t *testing.T) {
	e := newEnv(t, sub("s", 0, "block"))
	buildChain(t, e, "eth-sepolia", 100, 101)
	e.block(t, "eth-sepolia", 101, "0xh101", "0xh100") // duplicate
	var n int
	e.db.QueryRow(`SELECT count(*) FROM deliveries WHERE subscription_id='s' AND event_kind='block'`).
		Scan(&n)
	if n != 2 { // 100 and 101, delivered once each
		t.Fatalf("deliveries after duplicate = %d want 2", n)
	}
}

func TestStaleBlockBelowWindowIsDropped(t *testing.T) {
	db, err := openState(filepath.Join(t.TempDir(), "s2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// An engine with a tiny window: heights 1..20, window 5.
	reg := newRegistry([]subscription{sub("s", 0, "block")})
	eng := newEngine(db, reg, 5)
	for n := uint64(1); n <= 20; n++ {
		hash, parent := blockHashes(n)
		ev := mustParse(t, "extractor.eth-sepolia.block", blockEnvelope("eth-sepolia", n, hash, parent))
		if _, err := eng.ApplyEvent(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := eng.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM blocks`).Scan(&n)
	if n != 5 { // 16..20
		t.Fatalf("blocks after prune = %d want 5", n)
	}
	db.QueryRow(`SELECT count(*) FROM events`).Scan(&n)
	if n != 5 {
		t.Fatalf("events after prune = %d want 5", n)
	}
	// A block below the window with an unknown hash must not regress the head.
	stale := mustParse(t, "extractor.eth-sepolia.block", blockEnvelope("eth-sepolia", 3, "0xzz", "0xzy"))
	if _, err := eng.ApplyEvent(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	var head int64
	db.QueryRow(`SELECT block_number FROM heads WHERE network='eth-sepolia'`).Scan(&head)
	if head != 20 {
		t.Fatalf("head regressed to %d", head)
	}
	// Delivered rows prune with the window; pending ones survive. The
	// queue holds heights 16..20 (the matcher floor at head 20); marking
	// 16 and 17 delivered, advancing the head to 21 (floor 16) and
	// pruning must retire exactly the delivered height 16.
	_, _ = db.Exec(`UPDATE deliveries SET state='delivered' WHERE block_number<=17`)
	newHead := mustParse(t, "extractor.eth-sepolia.block", blockEnvelope("eth-sepolia", 21, "0xh21", "0xh20"))
	if _, err := eng.ApplyEvent(context.Background(), newHead); err != nil {
		t.Fatal(err)
	}
	if err := eng.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT count(*) FROM deliveries WHERE state='delivered'`).Scan(&n)
	if n != 1 { // height 17 delivered survives (above the floor)
		t.Fatalf("delivered rows after prune = %d want 1", n)
	}
	db.QueryRow(`SELECT count(*) FROM deliveries WHERE state='pending'`).Scan(&n)
	if n != 4 { // 18, 19, 20 still pending, 21 newly queued
		t.Fatalf("pending rows pruned: %d", n)
	}
}

func TestRematchAfterConfigReload(t *testing.T) {
	e := newEnv(t) // starts empty
	buildChain(t, e, "eth-sepolia", 100, 102)
	// A new subscription appears via hot reload.
	e.reg.Swap([]subscription{sub("late", 1, "transaction")})
	if _, err := e.eng.Rematch(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	e.db.QueryRow(`SELECT count(*) FROM deliveries WHERE subscription_id='late'`).Scan(&n)
	if n != 0 {
		// no transaction events exist in the window, so nothing matches
		t.Fatalf("unexpected deliveries: %d", n)
	}
	e.tx(t, "eth-sepolia", 102, "0xh102", 0, "0xtxL", "0xf", "0xA", "1", 1)
	e.block(t, "eth-sepolia", 103, "0xh103", "0xh102") // conf 1: head 103 confirms 102
	e.db.QueryRow(`SELECT count(*) FROM deliveries WHERE subscription_id='late'`).Scan(&n)
	if n != 1 {
		t.Fatalf("late subscription deliveries = %d want 1", n)
	}
	// Reload removed the subscription: its pending row is cleaned up.
	if err := e.eng.CleanupSubscriptions(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	e.db.QueryRow(`SELECT count(*) FROM deliveries WHERE subscription_id='late'`).Scan(&n)
	if n != 0 {
		t.Fatalf("rows of removed subscription survive: %d", n)
	}
}
