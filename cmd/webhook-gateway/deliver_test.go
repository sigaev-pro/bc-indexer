package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type receivedHook struct {
	body    []byte
	headers http.Header
}

// hookRecorder is a receiver that verifies the HMAC signature of every
// POST, like a real client would.
type hookRecorder struct {
	mu     sync.Mutex
	hooks  []receivedHook
	secret string
	fail   int // respond 500 for the first N posts
}

func (h *hookRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	h.hooks = append(h.hooks, receivedHook{body: body, headers: r.Header.Clone()})
	n := len(h.hooks)
	h.mu.Unlock()
	sig := r.Header.Get("X-Webhook-Signature")
	if sig == "" || !verifySignature(h.secret, body, sig) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if n <= h.fail {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *hookRecorder) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.hooks)
}

func (h *hookRecorder) last() receivedHook {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hooks[len(h.hooks)-1]
}

func newDeliverEnv(t *testing.T, rec *hookRecorder, subs ...subscription) (*sql.DB, *dispatcher) {
	t.Helper()
	db, err := openState(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := newRegistry(subs)
	disp := newDispatcher(db, reg)
	return db, disp
}

func enqueueDelivery(t *testing.T, db *sql.DB, sub, key, kind, payload string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO deliveries (subscription_id, event_key, event_kind, network,
		block_number, entity, webhook_id, payload, state, attempts, next_attempt_at, last_response)
		VALUES (?,?,?,?,?,?,?,?,?,0,0,'')`,
		sub, key, kind, "eth-sepolia", 100, "0xe", newWebhookID(), payload, statePending)
	if err != nil {
		t.Fatal(err)
	}
}

func deliveryColumn(t *testing.T, db *sql.DB, sub, key, kind, column string) any {
	t.Helper()
	var v any
	err := db.QueryRow(`SELECT `+column+` FROM deliveries
		WHERE subscription_id=? AND event_key=? AND event_kind=?`, sub, key, kind).Scan(&v)
	if err != nil {
		t.Fatalf("query %s: %v", column, err)
	}
	return v
}

func TestWebhookDeliveryAndSignature(t *testing.T) {
	rec := &hookRecorder{secret: "whsec_test"}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	db, disp := newDeliverEnv(t, rec, subscription{ID: "payin", URL: srv.URL, Secret: "whsec_test"})

	payload := `{"type":"transaction","network":"eth-sepolia","tx_hash":"0xtx"}`
	enqueueDelivery(t, db, "payin", "eth-sepolia:100:0xtx:0", kindTransaction, payload)

	if n, err := disp.DispatchDue(context.Background()); err != nil || n != 1 {
		t.Fatalf("dispatch = %d, %v", n, err)
	}
	if rec.count() != 1 {
		t.Fatalf("receiver saw %d posts want 1", rec.count())
	}
	hook := rec.last()
	if string(hook.body) != payload {
		t.Fatalf("body = %q want %q", hook.body, payload)
	}
	if got := hook.headers.Get("X-Webhook-Event"); got != "transaction" {
		t.Fatalf("X-Webhook-Event = %q", got)
	}
	if got := hook.headers.Get("X-Webhook-Delivery"); got != "1" {
		t.Fatalf("X-Webhook-Delivery = %q want 1", got)
	}
	if got := hook.headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if hook.headers.Get("X-Webhook-Id") == "" {
		t.Fatal("X-Webhook-Id missing")
	}
	// The signature was verified by the receiver itself (it answered 200
	// only on a valid HMAC); double-check the shape here too.
	if sig := hook.headers.Get("X-Webhook-Signature"); len(sig) != 71 || sig[:7] != "sha256=" {
		t.Fatalf("signature shape = %q", sig)
	}
	if got := deliveryColumn(t, db, "payin", "eth-sepolia:100:0xtx:0", kindTransaction, "state"); got != stateDelivered {
		t.Fatalf("state = %v want delivered", got)
	}
	// Delivered rows are never picked again: the queue is empty.
	if n, _ := disp.DispatchDue(context.Background()); n != 0 {
		t.Fatalf("second dispatch picked %d rows", n)
	}
	// The webhook id stays stable in the row (it would be resent verbatim
	// on a crash-replay).
	if id := deliveryColumn(t, db, "payin", "eth-sepolia:100:0xtx:0", kindTransaction, "webhook_id"); id == "" {
		t.Fatal("webhook_id not persisted")
	}
}

func TestWebhookRetryStateMachine(t *testing.T) {
	rec := &hookRecorder{secret: "s", fail: 100} // always 500
	srv := httptest.NewServer(rec)
	defer srv.Close()
	db, disp := newDeliverEnv(t, rec, subscription{ID: "flaky", URL: srv.URL, Secret: "s"})
	enqueueDelivery(t, db, "flaky", "k", kindTransaction, `{}`)

	ctx := context.Background()
	for attempt := int64(1); attempt <= 8; attempt++ {
		if _, err := disp.DispatchDue(ctx); err != nil {
			t.Fatal(err)
		}
		state := deliveryColumn(t, db, "flaky", "k", kindTransaction, "state")
		attempts := deliveryColumn(t, db, "flaky", "k", kindTransaction, "attempts")
		if attempts != attempt {
			t.Fatalf("attempts after round %d = %v", attempt, attempts)
		}
		if attempt < 8 {
			if state != statePending {
				t.Fatalf("state after attempt %d = %v want pending", attempt, state)
			}
			// Not due again before the backoff elapses.
			if n, _ := disp.DispatchDue(ctx); n != 0 {
				t.Fatalf("row picked before its backoff elapsed (round %d)", attempt)
			}
			// The recorded next attempt sits in the expected window.
			next := deliveryColumn(t, db, "flaky", "k", kindTransaction, "next_attempt_at").(int64)
			want := time.Now().UTC().Add(disp.backoff(attempt)).Unix()
			if next < want-2 || next > want+2 {
				t.Fatalf("next_attempt_at = %d want about %d", next, want)
			}
			// Simulate the wait.
			_, _ = db.Exec(`UPDATE deliveries SET next_attempt_at=0 WHERE subscription_id='flaky'`)
		} else {
			if state != stateFailed {
				t.Fatalf("state after attempt 8 = %v want failed", state)
			}
		}
	}
	if rec.count() != 8 {
		t.Fatalf("receiver saw %d posts want 8", rec.count())
	}
	// failed is terminal: nothing is due anymore.
	if n, _ := disp.DispatchDue(ctx); n != 0 {
		t.Fatalf("failed row picked again: %d", n)
	}
	// last_response keeps the failure cause for inspection.
	if lr := deliveryColumn(t, db, "flaky", "k", kindTransaction, "last_response").(string); lr == "" {
		t.Fatal("last_response empty")
	}
}

func TestBackoffSchedule(t *testing.T) {
	d := &dispatcher{minBackoff: time.Second, maxBackoff: 15 * time.Second}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second}
	for i, w := range want {
		if got := d.backoff(int64(i + 1)); got != w {
			t.Errorf("backoff(%d) = %s want %s", i+1, got, w)
		}
	}
}

func TestRecoveredAfterRetries(t *testing.T) {
	rec := &hookRecorder{secret: "s", fail: 2} // two 500s, then 200
	srv := httptest.NewServer(rec)
	defer srv.Close()
	db, disp := newDeliverEnv(t, rec, subscription{ID: "later", URL: srv.URL, Secret: "s"})
	enqueueDelivery(t, db, "later", "k", kindTransaction, `{}`)

	ctx := context.Background()
	for round := 0; round < 3; round++ {
		_, _ = disp.DispatchDue(ctx)
		_, _ = db.Exec(`UPDATE deliveries SET next_attempt_at=0 WHERE subscription_id='later'`)
	}
	if got := deliveryColumn(t, db, "later", "k", kindTransaction, "state"); got != stateDelivered {
		t.Fatalf("state = %v want delivered after the endpoint recovered", got)
	}
	if rec.count() != 3 {
		t.Fatalf("posts = %d want 3", rec.count())
	}
	hook := rec.last()
	if got := hook.headers.Get("X-Webhook-Delivery"); got != "3" {
		t.Fatalf("X-Webhook-Delivery = %q want 3", got)
	}
}

func TestDeliveryOfRemovedSubscription(t *testing.T) {
	rec := &hookRecorder{secret: "s"}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	// The registry holds "live"; the queued row belongs to "gone".
	db, disp := newDeliverEnv(t, rec, subscription{ID: "live", URL: srv.URL, Secret: "s"})
	enqueueDelivery(t, db, "gone", "k", kindTransaction, `{}`)
	if _, err := disp.DispatchDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := deliveryColumn(t, db, "gone", "k", kindTransaction, "state"); got != stateRemoved {
		t.Fatalf("state = %v want removed", got)
	}
	if rec.count() != 0 {
		t.Fatalf("posts = %d want 0", rec.count())
	}
}

func TestSignBodyVectors(t *testing.T) {
	// RFC 4231 test case 1 semantics adapted to the gateway header shape.
	got := signBody("key", []byte("The quick brown fox jumps over the lazy dog"))
	want := "sha256=f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"
	if got != want {
		t.Fatalf("signBody = %s want %s", got, want)
	}
	if !verifySignature("key", []byte("The quick brown fox jumps over the lazy dog"), got) {
		t.Fatal("verifySignature rejected a valid signature")
	}
	if verifySignature("key", []byte("tampered"), got) {
		t.Fatal("verifySignature accepted a tampered body")
	}
	if verifySignature("other", []byte("The quick brown fox jumps over the lazy dog"), got) {
		t.Fatal("verifySignature accepted the wrong secret")
	}
}

func TestWebhookIDIsUUIDv4(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := newWebhookID()
		if seen[id] {
			t.Fatalf("duplicate webhook id %s", id)
		}
		seen[id] = true
		if len(id) != 36 || id[14] != '4' || (id[19] != '8' && id[19] != '9' && id[19] != 'a' && id[19] != 'b') {
			t.Fatalf("id %s is not a v4 UUID", id)
		}
	}
}

// TestNotificationEndToEnd drives one transaction through parse, engine,
// dispatcher and the wire, and checks the exact JSON the client receives.
func TestNotificationEndToEnd(t *testing.T) {
	rec := &hookRecorder{secret: "whsec_e2e"}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	subs := []subscription{{
		ID: "e2e", Network: "eth-sepolia", EventType: "transaction", TxTo: "0xpayee",
		Confirmations: 1, URL: srv.URL, Secret: "whsec_e2e",
	}}
	db, err := openState(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reg := newRegistry(subs)
	eng := newEngine(db, reg, 2000)
	disp := newDispatcher(db, reg)
	eng.SetWake(disp.Wake)

	// Block 100 then the tx; conf 1 means delivery only at head 101.
	for _, m := range []struct{ subject, body string }{
		{"extractor.eth-sepolia.block", blockEnvelope("eth-sepolia", 100, "0xb100", "0xb99")},
		{"extractor.eth-sepolia.transaction", txEnvelope("eth-sepolia", 100, "0xb100", 2,
			"0xtx9", "0xsender", "0xPAYEE", "123456789012345678", 1)},
		{"extractor.eth-sepolia.block", blockEnvelope("eth-sepolia", 101, "0xb101", "0xb100")},
	} {
		ev, err := parseMessage(m.subject, []byte(m.body), time.Unix(1732000100, 0).UTC())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := eng.ApplyEvent(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := disp.DispatchDue(context.Background()); err != nil || n != 1 {
		t.Fatalf("dispatch = %d, %v", n, err)
	}
	if rec.count() != 1 {
		t.Fatalf("posts = %d want 1 (nothing before head 101)", rec.count())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.last().body, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"type": "transaction", "network": "eth-sepolia", "chain_id": float64(11155111),
		"block_number": float64(100), "block_hash": "0xb100", "tx_hash": "0xtx9",
		"tx_index": float64(2), "from": "0xsender", "to": "0xPAYEE",
		"value": "123456789012345678", "gas": float64(21000), "status": "success",
		"confirmations": float64(1), "kind": "applied",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%q] = %v want %v", k, body[k], v)
		}
	}
	if _, ok := body["captured_at"]; !ok {
		t.Error("captured_at missing")
	}
}
