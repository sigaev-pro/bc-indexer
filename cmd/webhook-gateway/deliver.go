// The webhook delivery side: a dispatcher goroutine drains the deliveries
// queue, a worker pool of four POSTs the JSON bodies, and every attempt
// carries the identity headers and the HMAC-SHA256 signature the client
// verifies. Failures back off exponentially (1s doubling to a 15s cap,
// eight attempts) and then park the row as failed for inspection; the
// primary key (subscription_id, event_key, event_kind) makes each logical
// notification a single row, so a notification is never sent twice within
// one process life. A crash between a successful POST and its commit can
// resend once after restart — at-least-once, the documented contract.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// dispatcher drains the due deliveries.
type dispatcher struct {
	db     *sql.DB
	reg    *registry
	client *http.Client
	wake   chan struct{}

	workers       int
	maxAttempts   int64
	minBackoff    time.Duration
	maxBackoff    time.Duration
	pickBatchSize int
}

func newDispatcher(db *sql.DB, reg *registry) *dispatcher {
	return &dispatcher{
		db:     db,
		reg:    reg,
		client: &http.Client{Timeout: 10 * time.Second},
		wake:   make(chan struct{}, 1),
		// 1s, 2s, 4s, 8s, then the 15s cap; eight attempts total.
		workers: 4, maxAttempts: 8,
		minBackoff: time.Second, maxBackoff: 15 * time.Second,
		pickBatchSize: 32,
	}
}

// Wake nudges the loop out of its ticker sleep.
func (d *dispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run drives the dispatcher until ctx stops.
func (d *dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-ticker.C:
		}
		for {
			n, err := d.DispatchDue(ctx)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("DISPATCH_ERROR error=%v", err)
				}
				break
			}
			if n == 0 {
				break
			}
		}
	}
}

// DispatchDue picks the due rows and sends them through the worker pool;
// it returns how many rows were picked (0 means the queue has nothing
// due).
func (d *dispatcher) DispatchDue(ctx context.Context) (int, error) {
	now := time.Now().UTC().Unix()
	rows, err := d.db.QueryContext(ctx, `SELECT subscription_id, event_key, event_kind, network,
			block_number, entity, webhook_id, payload, attempts
			FROM deliveries WHERE state=? AND next_attempt_at<=?
			ORDER BY next_attempt_at LIMIT ?`,
		statePending, now, d.pickBatchSize)
	if err != nil {
		return 0, err
	}
	var due []deliveryRow
	for rows.Next() {
		var r deliveryRow
		if err := rows.Scan(&r.SubscriptionID, &r.EventKey, &r.EventKind, &r.Network,
			&r.BlockNumber, &r.Entity, &r.WebhookID, &r.Payload, &r.Attempts); err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(due) == 0 {
		return 0, nil
	}
	sem := make(chan struct{}, d.workers)
	var wg sync.WaitGroup
	for _, r := range due {
		wg.Add(1)
		sem <- struct{}{}
		row := r
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			d.sendOne(ctx, row)
		}()
	}
	wg.Wait()
	return len(due), nil
}

// sendOne performs one delivery attempt and records the outcome. A
// shutdown mid-attempt leaves the row exactly as it was (still pending,
// still due) so the restart takes it again.
func (d *dispatcher) sendOne(ctx context.Context, r deliveryRow) {
	sub := d.reg.Find(r.SubscriptionID)
	if sub == nil {
		// The subscription left the config between enqueue and now; the
		// reload cleanup usually removes the row first, this is the
		// race-free backstop.
		_, _ = d.db.Exec(`UPDATE deliveries SET state=?, last_response='subscription removed from config'
			WHERE subscription_id=? AND event_key=? AND event_kind=?`,
			stateRemoved, r.SubscriptionID, r.EventKey, r.EventKind)
		return
	}
	attempt := r.Attempts + 1
	body := []byte(r.Payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(body))
	if err != nil {
		d.recordFailure(ctx, r, attempt, fmt.Sprintf("build request: %v", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Id", r.WebhookID)
	req.Header.Set("X-Webhook-Event", r.EventKind)
	req.Header.Set("X-Webhook-Delivery", strconv.FormatInt(attempt, 10))
	if sub.Secret != "" {
		req.Header.Set("X-Webhook-Signature", signBody(sub.Secret, body))
	}
	resp, err := d.client.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
	}
	if ctx.Err() != nil {
		return // shutdown: leave the row pending and due
	}
	if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if _, uerr := d.db.Exec(`UPDATE deliveries SET state=?, attempts=?, last_response=?
				WHERE subscription_id=? AND event_key=? AND event_kind=?`,
			stateDelivered, attempt, fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode)),
			r.SubscriptionID, r.EventKey, r.EventKind); uerr != nil {
			log.Printf("WEBHOOK_COMMIT_FAILED sub=%s key=%s error=%v", r.SubscriptionID, r.EventKey, uerr)
			return
		}
		log.Printf("WEBHOOK_DELIVERED sub=%s event=%s key=%s status=%d attempt=%d webhook_id=%s",
			r.SubscriptionID, r.EventKind, r.EventKey, resp.StatusCode, attempt, r.WebhookID)
		return
	}
	statusOrErr := fmt.Sprintf("%v", err)
	if err == nil {
		statusOrErr = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	d.recordFailure(ctx, r, attempt, statusOrErr)
}

// recordFailure advances the retry state machine: one more attempt spent,
// the next try scheduled by the exponential backoff, or the terminal
// failed state once the budget is exhausted.
func (d *dispatcher) recordFailure(ctx context.Context, r deliveryRow, attempt int64, cause string) {
	if attempt >= d.maxAttempts {
		if _, err := d.db.Exec(`UPDATE deliveries SET state=?, attempts=?, last_response=?
				WHERE subscription_id=? AND event_key=? AND event_kind=?`,
			stateFailed, attempt, cause, r.SubscriptionID, r.EventKey, r.EventKind); err != nil {
			log.Printf("WEBHOOK_COMMIT_FAILED sub=%s key=%s error=%v", r.SubscriptionID, r.EventKey, err)
			return
		}
		log.Printf("WEBHOOK_FAILED sub=%s event=%s key=%s attempts=%d last_error=%q webhook_id=%s",
			r.SubscriptionID, r.EventKind, r.EventKey, attempt, cause, r.WebhookID)
		return
	}
	retryIn := d.backoff(attempt)
	next := time.Now().UTC().Add(retryIn).Unix()
	if _, err := d.db.Exec(`UPDATE deliveries SET attempts=?, next_attempt_at=?, last_response=?
			WHERE subscription_id=? AND event_key=? AND event_kind=?`,
		attempt, next, cause, r.SubscriptionID, r.EventKey, r.EventKind); err != nil {
		log.Printf("WEBHOOK_COMMIT_FAILED sub=%s key=%s error=%v", r.SubscriptionID, r.EventKey, err)
		return
	}
	log.Printf("WEBHOOK_RETRY sub=%s event=%s key=%s attempt=%d retry_in=%s last_error=%q",
		r.SubscriptionID, r.EventKind, r.EventKey, attempt, retryIn, cause)
}

// backoff returns the wait after the n-th failed attempt: 1s, 2s, 4s, 8s,
// then the 15s cap.
func (d *dispatcher) backoff(n int64) time.Duration {
	if n < 1 {
		n = 1
	}
	wait := d.minBackoff << uint(n-1)
	if wait > d.maxBackoff || wait <= 0 {
		wait = d.maxBackoff
	}
	return wait
}

// signBody computes the X-Webhook-Signature value:
// "sha256=" + hex(HMAC-SHA256(secret, body)).
func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// verifySignature checks a signature the way a client would; used by the
// tests and a faithful reference for receiver implementations.
func verifySignature(secret string, body []byte, header string) bool {
	want := signBody(secret, body)
	return hmac.Equal([]byte(want), []byte(header))
}

// newWebhookID mints the stable identity of one logical notification:
// a random RFC 4122 version 4 UUID in canonical hex (crypto/rand).
func newWebhookID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on a healthy host; degrade to a
		// time-derived id rather than dropping the notification.
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]))
}
