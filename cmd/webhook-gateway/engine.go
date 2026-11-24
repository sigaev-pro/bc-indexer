// The decision core: turns parsed extractor events into delivery-queue
// rows. Confirmations: an event at height h becomes deliverable for a
// subscription when the canonical head reaches h + N (N = 0 delivers on
// arrival). Reorg detection is self-contained from block events only: the
// per-network height->hash window (the blocks table) reveals a fork the
// moment a block arrives whose hash differs from the stored hash at its
// height, or whose parent hash mismatches the stored hash at height-1
// while the block itself is canonical going forward. The affected range
// [divergence..old_head] is then retired: pending notifications are
// superseded (re-delivered fresh if the entity re-appears, announced as
// event_removed otherwise), delivered ones are announced as
// event_invalidated — the "event became bad" notification — and every
// matching subscription receives one reorg notification for the range.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// defaultWindow is the per-network block-hash window: how many heights of
// blocks/events the gateway keeps for reorg detection and late matching.
const defaultWindow = int64(2000)

// engine owns the state and the decisions. All ApplyEvent calls run under
// one lock, so per-network ordering (block before its transactions, which
// the publisher guarantees per stream) survives the cross-stream fan-in.
type engine struct {
	db     *sql.DB
	reg    *registry
	window int64
	wake   func() // called after work enters the queue (dispatcher nudge)

	mu sync.Mutex
}

func newEngine(db *sql.DB, reg *registry, window int64) *engine {
	if window <= 0 {
		window = defaultWindow
	}
	return &engine{db: db, reg: reg, window: window}
}

// SetWake wires the dispatcher nudge.
func (e *engine) SetWake(fn func()) { e.wake = fn }

func (e *engine) nudge() {
	if e.wake != nil {
		e.wake()
	}
}

// head is the canonical head of one network.
type head struct {
	number int64
	hash   string
}

// ApplyEvent records one extractor event and runs its consequences. It
// returns whether new deliveries entered the queue.
func (e *engine) ApplyEvent(ctx context.Context, ev *parsedEvent) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var queued bool
	err := withTx(ctx, e.db, func(tx *sql.Tx) error {
		var err error
		switch ev.eventType {
		case "block":
			queued, err = e.applyBlock(ctx, tx, ev)
		default: // transaction
			queued, err = e.applyTx(ctx, tx, ev)
		}
		return err
	})
	if err == nil && queued {
		e.nudge()
	}
	return queued, err
}

// applyTx stores the event and (when a head exists, so confirmations can
// be judged) runs the matcher. Redelivered duplicates collapse on the
// events primary key.
func (e *engine) applyTx(ctx context.Context, tx *sql.Tx, ev *parsedEvent) (bool, error) {
	if _, err := tx.ExecContext(ctx, `REPLACE INTO events
			(network, block_number, event_type, entity, payload, captured_at)
			VALUES (?,?,?,?,?,?)`,
		ev.network, int64(ev.blockNumber), ev.eventType, txEntity(ev.txHash, ev.txIndex),
		rawEnvelope(ev), ev.capturedAt.Format(time.RFC3339Nano)); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE replaced SET reappeared=1 WHERE network=? AND entity=?`,
		ev.network, ev.txHash); err != nil {
		return false, err
	}
	h, err := getHead(ctx, tx, ev.network)
	if err != nil || h == nil {
		return false, err
	}
	queued, err := e.matchAll(ctx, tx, ev.network, h.number)
	return queued, err
}

// applyBlock runs reorg detection, advances the head and runs the matcher
// and the supersession sweep.
func (e *engine) applyBlock(ctx context.Context, tx *sql.Tx, ev *parsedEvent) (bool, error) {
	h, err := getHead(ctx, tx, ev.network)
	if err != nil {
		return false, err
	}
	if h != nil {
		stored, err := blockHashAt(ctx, tx, ev.network, int64(ev.blockNumber))
		if err != nil {
			return false, err
		}
		if stored == ev.blockHash && int64(ev.blockNumber) <= h.number {
			return false, nil // a duplicate of a block already on the chain
		}
		if stored == "" && int64(ev.blockNumber) < h.number {
			// Below the hash window and absent: either pruned long ago or
			// out-of-band. Reorg detection cannot see this deep, and
			// accepting it would silently regress the head, so it is
			// dropped (the window is orders of magnitude deeper than the
			// reorg depths the chain produces).
			log.Printf("BLOCK_STALE network=%s height=%d head=%d (below the hash window; ignored)",
				ev.network, ev.blockNumber, h.number)
			return false, nil
		}
	}
	var queued bool
	if h != nil {
		divergence, oldHead, reorg, err := e.detectReorg(ctx, tx, ev, h)
		if err != nil {
			return false, err
		}
		if reorg {
			if queued, err = e.handleReorg(ctx, tx, ev, divergence, oldHead); err != nil {
				return false, err
			}
		} else if int64(ev.blockNumber) > h.number+1 {
			log.Printf("HEAD_GAP network=%s from=%d to=%d (heights were skipped or arrived out of band)",
				ev.network, h.number, ev.blockNumber)
		}
	}
	if _, err := tx.ExecContext(ctx, `REPLACE INTO blocks
			(network, block_number, block_hash, parent_hash) VALUES (?,?,?,?)`,
		ev.network, int64(ev.blockNumber), ev.blockHash, ev.parentHash); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `REPLACE INTO heads (network, block_number, block_hash)
			VALUES (?,?,?)`, ev.network, int64(ev.blockNumber), ev.blockHash); err != nil {
		return false, err
	}
	// A rebuilt block re-appears under its own hash; pending block
	// notifications of the old chain never share that hash, so this only
	// ever clears exotic same-hash cases.
	if _, err := tx.ExecContext(ctx,
		`UPDATE replaced SET reappeared=1 WHERE network=? AND entity=?`,
		ev.network, ev.blockHash); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `REPLACE INTO events
			(network, block_number, event_type, entity, payload, captured_at)
			VALUES (?,?,?,?,?,?)`,
		ev.network, int64(ev.blockNumber), ev.eventType, ev.blockHash,
		rawEnvelope(ev), ev.capturedAt.Format(time.RFC3339Nano)); err != nil {
		return false, err
	}
	matched, err := e.matchAll(ctx, tx, ev.network, int64(ev.blockNumber))
	if err != nil {
		return false, err
	}
	queued = queued || matched
	swept, err := e.sweepSuperseded(ctx, tx, ev.network, int64(ev.blockNumber))
	if err != nil {
		return false, err
	}
	queued = queued || swept
	return queued, nil
}

// detectReorg finds the divergence height of an incoming block against
// the stored canonical window. Case one: the stored hash at H differs —
// the chain changed at H. Case two: the stored hash at H-1 differs from
// the incoming parent hash — the chain changed at H-1 and the incoming
// block builds on the replacement.
func (e *engine) detectReorg(ctx context.Context, tx *sql.Tx, ev *parsedEvent, h *head) (divergence, oldHead int64, reorg bool, err error) {
	H := int64(ev.blockNumber)
	storedAtH, err := blockHashAt(ctx, tx, ev.network, H)
	if err != nil {
		return 0, 0, false, err
	}
	if storedAtH != "" && storedAtH != ev.blockHash && H <= h.number {
		return H, h.number, true, nil
	}
	if H > 0 {
		storedAtPrev, err := blockHashAt(ctx, tx, ev.network, H-1)
		if err != nil {
			return 0, 0, false, err
		}
		if storedAtPrev != "" && storedAtPrev != ev.parentHash && H-1 <= h.number {
			return H - 1, h.number, true, nil
		}
	}
	return 0, 0, false, nil
}

// handleReorg retires the replaced range [divergence..oldHead]: the old
// blocks and events leave the window, pending deliveries are superseded
// (their entities are recorded for the reappearance check), delivered
// ones are invalidated with an event_invalidated notification each, and
// every subscription matching the network gets one reorg notification.
func (e *engine) handleReorg(ctx context.Context, tx *sql.Tx, ev *parsedEvent, divergence, oldHead int64) (bool, error) {
	common := divergence - 1
	log.Printf("REORG_DETECTED network=%s range=%d..%d replaced_common_height=%d new_block=%s",
		ev.network, divergence, oldHead, common, ev.blockHash)
	if _, err := tx.ExecContext(ctx, `DELETE FROM blocks
			WHERE network=? AND block_number BETWEEN ? AND ?`,
		ev.network, divergence, oldHead); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM events
			WHERE network=? AND block_number BETWEEN ? AND ?`,
		ev.network, divergence, oldHead); err != nil {
		return false, err
	}
	queued := false

	// Pending -> superseded; their entities are recorded for the
	// reappearance check with the old head as the rebuild horizon.
	rows, err := tx.QueryContext(ctx, `SELECT subscription_id, event_key, event_kind, entity
			FROM deliveries WHERE network=? AND state=? AND block_number BETWEEN ? AND ?`,
		ev.network, statePending, divergence, oldHead)
	if err != nil {
		return false, err
	}
	type key struct{ sub, key, kind, entity string }
	var pendings []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.sub, &k.key, &k.kind, &k.entity); err != nil {
			rows.Close()
			return false, err
		}
		pendings = append(pendings, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, k := range pendings {
		if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET state=?
				WHERE subscription_id=? AND event_key=? AND event_kind=?`,
			stateSuperseded, k.sub, k.key, k.kind); err != nil {
			return false, err
		}
		if (k.kind == kindTransaction || k.kind == kindBlock) && k.entity != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO replaced (network, entity, horizon, reappeared)
					VALUES (?,?,?,0) ON CONFLICT(network, entity) DO NOTHING`,
				ev.network, k.entity, oldHead); err != nil {
				return false, err
			}
		}
	}

	// Delivered -> invalidated with an event_invalidated notification.
	rows, err = tx.QueryContext(ctx, `SELECT subscription_id, event_key, event_kind, entity, webhook_id, payload, block_number
			FROM deliveries WHERE network=? AND state=? AND block_number BETWEEN ? AND ?
			  AND event_kind IN (?,?)`,
		ev.network, stateDelivered, divergence, oldHead, kindTransaction, kindBlock)
	if err != nil {
		return false, err
	}
	type deliveredRow struct {
		sub, key, kind, entity, webhookID, payload string
		blockNumber                                int64
	}
	var delivered []deliveredRow
	for rows.Next() {
		var d deliveredRow
		if err := rows.Scan(&d.sub, &d.key, &d.kind, &d.entity, &d.webhookID, &d.payload, &d.blockNumber); err != nil {
			rows.Close()
			return false, err
		}
		delivered = append(delivered, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, d := range delivered {
		if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET state=?
				WHERE subscription_id=? AND event_key=? AND event_kind=?`,
			stateInvalidated, d.sub, d.key, d.kind); err != nil {
			return false, err
		}
		body, err := json.Marshal(notificationInvalidated(ev.network, uint64(divergence),
			d.webhookID, json.RawMessage(d.payload)))
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deliveries
				(subscription_id, event_key, event_kind, network, block_number, entity, webhook_id,
				 payload, state, attempts, next_attempt_at, last_response)
				VALUES (?,?,?,?,?,?,?,?,?,0,0,'')
				ON CONFLICT(subscription_id, event_key, event_kind) DO NOTHING`,
			d.sub, d.key, kindEventInvalidated, ev.network, d.blockNumber, d.entity,
			newWebhookID(), string(body), statePending); err != nil {
			return false, err
		}
		queued = true
	}

	// One reorg notification per subscription matching the network.
	reorgNote, err := json.Marshal(notificationReorg(ev.network,
		uint64(divergence), uint64(oldHead), uint64(max64(common, 0))))
	if err != nil {
		return false, err
	}
	reorgKey := fmt.Sprintf("reorg:%d:%d:%s", divergence, oldHead, ev.blockHash)
	for _, sub := range e.reg.Snapshot() {
		if !sub.MatchesNetwork(ev.network) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deliveries
				(subscription_id, event_key, event_kind, network, block_number, entity, webhook_id,
				 payload, state, attempts, next_attempt_at, last_response)
				VALUES (?,?,?,?,?,?,?,?,?,0,0,'')
				ON CONFLICT(subscription_id, event_key, event_kind) DO NOTHING`,
			sub.ID, reorgKey, kindReorg, ev.network, divergence, "",
			newWebhookID(), string(reorgNote), statePending); err != nil {
			return false, err
		}
		queued = true
	}
	return queued, nil
}

// sweepSuperseded runs after every head advance. When the rebuilt chain
// has moved past a replaced range's horizon (the old head at reorg time;
// strictly beyond it, so the transactions of the horizon block itself
// have certainly arrived — the publisher emits a block's transactions
// before the next block), every still-superseded notification settles:
// event_removed when its entity never re-appeared, a silent removal when
// it did (the fresh event delivers it anew).
func (e *engine) sweepSuperseded(ctx context.Context, tx *sql.Tx, network string, headNumber int64) (bool, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT entity, horizon, reappeared FROM replaced WHERE network=?`, network)
	if err != nil {
		return false, err
	}
	type replacedEntry struct {
		horizon    int64
		reappeared bool
	}
	entries := map[string]replacedEntry{}
	for rows.Next() {
		var entity string
		var ent replacedEntry
		if err := rows.Scan(&entity, &ent.horizon, &ent.reappeared); err != nil {
			rows.Close()
			return false, err
		}
		entries[entity] = ent
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	anySweepable := false
	for _, ent := range entries {
		if ent.horizon < headNumber {
			anySweepable = true
			break
		}
	}
	if !anySweepable {
		return false, nil
	}

	rows, err = tx.QueryContext(ctx, `SELECT subscription_id, event_key, event_kind, entity, payload, block_number
			FROM deliveries WHERE network=? AND state=?`, network, stateSuperseded)
	if err != nil {
		return false, err
	}
	type supersededRow struct {
		sub, key, kind, entity, payload string
		blockNumber                     int64
	}
	var superseded []supersededRow
	for rows.Next() {
		var s supersededRow
		if err := rows.Scan(&s.sub, &s.key, &s.kind, &s.entity, &s.payload, &s.blockNumber); err != nil {
			rows.Close()
			return false, err
		}
		superseded = append(superseded, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	queued := false
	for _, s := range superseded {
		if ent, ok := entries[s.entity]; ok && ent.horizon >= headNumber {
			continue // this range is still being rebuilt
		}
		if (s.kind == kindTransaction || s.kind == kindBlock) &&
			!(entries[s.entity].reappeared) {
			body, err := json.Marshal(notificationRemoved(network, json.RawMessage(s.payload)))
			if err != nil {
				return false, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO deliveries
					(subscription_id, event_key, event_kind, network, block_number, entity, webhook_id,
					 payload, state, attempts, next_attempt_at, last_response)
					VALUES (?,?,?,?,?,?,?,?,?,0,0,'')
					ON CONFLICT(subscription_id, event_key, event_kind) DO NOTHING`,
				s.sub, s.key, kindEventRemoved, network, s.blockNumber, s.entity,
				newWebhookID(), string(body), statePending); err != nil {
				return false, err
			}
			queued = true
			log.Printf("EVENT_REMOVED_QUEUED network=%s sub=%s key=%s block=%d",
				network, s.sub, s.key, s.blockNumber)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE deliveries SET state=?
				WHERE subscription_id=? AND event_key=? AND event_kind=?`,
			stateRemoved, s.sub, s.key, s.kind); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM replaced WHERE network=? AND horizon<?`, network, headNumber); err != nil {
		return false, err
	}
	return queued, nil
}

// matchAll runs the matcher for every subscription covering the network.
func (e *engine) matchAll(ctx context.Context, tx *sql.Tx, network string, headNumber int64) (bool, error) {
	queued := false
	for _, sub := range e.reg.Snapshot() {
		if !sub.MatchesNetwork(network) {
			continue
		}
		matched, err := e.matchOne(ctx, tx, &sub, network, headNumber)
		if err != nil {
			return queued, err
		}
		queued = queued || matched
	}
	return queued, nil
}

// matchOne enqueues notifications for every stored event of the network
// that has reached the subscription's confirmation depth and passes its
// filters. The deliverability rule is exactly head >= height + N.
func (e *engine) matchOne(ctx context.Context, tx *sql.Tx, sub *subscription, network string, headNumber int64) (bool, error) {
	threshold := headNumber - sub.Confirmations
	floor := headNumber - e.window
	rows, err := tx.QueryContext(ctx, `SELECT block_number, event_type, entity, payload, captured_at
			FROM events
			WHERE network=? AND block_number<=? AND block_number>?
			  AND (?='*' OR event_type=?)
			  AND NOT EXISTS (
			    SELECT 1 FROM deliveries d
			    WHERE d.subscription_id=?
			      AND d.event_kind=events.event_type
			      AND d.event_key=events.network||':'||events.block_number||':'||events.entity
			      AND d.state IN (?,?,?))`,
		network, threshold, floor, sub.EventType, sub.EventType, sub.ID,
		statePending, stateDelivered, stateFailed)
	if err != nil {
		return false, err
	}
	type candidate struct {
		blockNumber                        int64
		eventType, entity, payload, captur string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.blockNumber, &c.eventType, &c.entity, &c.payload, &c.captur); err != nil {
			rows.Close()
			return false, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	queued := false
	now := time.Now().UTC().Unix()
	for _, c := range candidates {
		var p payload
		if err := json.Unmarshal([]byte(c.payload), &p); err != nil {
			continue // a body that no longer parses is skipped, not fatal
		}
		if sub.TxTo != "" {
			if c.eventType != kindTransaction || !addrEqual(string(p.To), sub.TxTo) {
				continue
			}
		}
		note := notificationEvent(&p, network, c.eventType, c.captur, sub.Confirmations)
		body, err := json.Marshal(note)
		if err != nil {
			return queued, err
		}
		entity := c.entity
		if c.eventType == kindTransaction {
			entity = string(p.Hash) // the reappearance identity is the bare tx hash
		}
		key := eventKey(network, uint64(c.blockNumber), c.entity)
		res, err := tx.ExecContext(ctx, `INSERT INTO deliveries
				(subscription_id, event_key, event_kind, network, block_number, entity, webhook_id,
				 payload, state, attempts, next_attempt_at, last_response)
				VALUES (?,?,?,?,?,?,?,?,?,0,?,'')
				ON CONFLICT(subscription_id, event_key, event_kind) DO UPDATE SET
				  state='pending', attempts=0, next_attempt_at=0,
				  payload=excluded.payload, webhook_id=excluded.webhook_id, last_response=''
				WHERE deliveries.state IN (?,?,?)`,
			sub.ID, key, c.eventType, network, c.blockNumber, entity, newWebhookID(),
			string(body), statePending, now,
			stateSuperseded, stateInvalidated, stateRemoved)
		if err != nil {
			return queued, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			queued = true
		}
	}
	return queued, nil
}

// Prune drops state older than the window so the database stays bounded.
func (e *engine) Prune(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return withTx(ctx, e.db, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT network, block_number FROM heads`)
		if err != nil {
			return err
		}
		type nh struct {
			network string
			head    int64
		}
		var heads []nh
		for rows.Next() {
			var n nh
			if err := rows.Scan(&n.network, &n.head); err != nil {
				rows.Close()
				return err
			}
			heads = append(heads, n)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, n := range heads {
			floor := n.head - e.window
			if _, err := tx.ExecContext(ctx, `DELETE FROM blocks WHERE network=? AND block_number<=?`,
				n.network, floor); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE network=? AND block_number<=?`,
				n.network, floor); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM deliveries WHERE network=? AND block_number<=? AND state IN (?,?,?,?)`,
				n.network, floor, stateDelivered, stateRemoved, stateInvalidated, stateFailed); err != nil {
				return err
			}
		}
		return nil
	})
}

// Rematch re-runs the matcher for every known network (after a config
// reload, so new subscriptions see the events already in the window).
func (e *engine) Rematch(ctx context.Context) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	queued := false
	err := withTx(ctx, e.db, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT network, block_number FROM heads`)
		if err != nil {
			return err
		}
		type nh struct {
			network string
			head    int64
		}
		var heads []nh
		for rows.Next() {
			var n nh
			if err := rows.Scan(&n.network, &n.head); err != nil {
				rows.Close()
				return err
			}
			heads = append(heads, n)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, n := range heads {
			matched, err := e.matchAll(ctx, tx, n.network, n.head)
			if err != nil {
				return err
			}
			queued = queued || matched
		}
		return nil
	})
	if err == nil && queued {
		e.nudge()
	}
	return queued, err
}

// CleanupSubscriptions drops the pending work of subscriptions that left
// the config; terminal rows stay for inspection until the window prunes
// them.
func (e *engine) CleanupSubscriptions(ctx context.Context, live []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return withTx(ctx, e.db, func(tx *sql.Tx) error {
		if len(live) == 0 {
			_, err := tx.ExecContext(ctx, `DELETE FROM deliveries WHERE state IN (?,?)`,
				statePending, stateSuperseded)
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM deliveries WHERE state IN (?,?) AND subscription_id NOT IN (`+
			placeholders(len(live))+`)`,
			append([]any{statePending, stateSuperseded}, toAny(live)...)...)
		return err
	})
}

// helpers -------------------------------------------------------------

// withTx runs fn in one transaction, rolling back on failure.
func withTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func getHead(ctx context.Context, tx *sql.Tx, network string) (*head, error) {
	var h head
	err := tx.QueryRowContext(ctx,
		`SELECT block_number, block_hash FROM heads WHERE network=?`, network).
		Scan(&h.number, &h.hash)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func blockHashAt(ctx context.Context, tx *sql.Tx, network string, height int64) (string, error) {
	var hash string
	err := tx.QueryRowContext(ctx,
		`SELECT block_hash FROM blocks WHERE network=? AND block_number=?`, network, height).
		Scan(&hash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return hash, nil
}

// rawEnvelope returns the exact message bytes the event arrived in; the
// events table keeps the original envelope verbatim.
func rawEnvelope(ev *parsedEvent) string { return ev.raw }

// addrEqual compares two hex addresses case-insensitively: the envelope
// carries checksummed 0x addresses while configs are often lowercase.
func addrEqual(a, b string) bool {
	if a == b {
		return true
	}
	return len(a) == len(b) && equalFoldHex(a, b)
}

// equalFoldHex is strings.EqualFold restricted to ASCII, spelled out to
// keep the helper self-evident.
func equalFoldHex(a, b string) bool {
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func placeholders(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			s += ","
		}
		s += "?"
	}
	return s
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
