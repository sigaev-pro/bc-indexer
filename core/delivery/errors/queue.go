// The bounded queues of the delivery path: a bounded in-flight window and
// the disk-backed pending queue model. Pending has no TTL and no quota —
// the bound lives on the in-flight window, and nothing in this package
// ever drops, expires or reorders durable pending data
// (DELIVERY_RECOVERY: "the absence of a disk quota for the WAL does not mean loading the entire queue into RAM").
package errors

import (
	"fmt"
	"sync"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
)

// Window is the bounded in-flight window over the unbounded durable
// pending queue: at most Max in-flight operations exist at any moment, so
// memory stays bounded while the queue lives on disk. Begin refuses with
// delivery.ErrPressured when the window is full — backpressure to the
// caller, never data loss.
type Window struct {
	mu      sync.Mutex
	max     int
	inUse   int
	blocked chan struct{} // closed and rebuilt to wake waiters on Done
}

// NewWindow returns a window admitting at most max concurrent in-flight
// operations.
func NewWindow(max int) (*Window, error) {
	if max <= 0 {
		return nil, fmt.Errorf("delivery/errors: window max %d is not positive", max)
	}
	return &Window{max: max, blocked: make(chan struct{})}, nil
}

// TryBegin takes one in-flight slot or fails with delivery.ErrPressured.
func (w *Window) TryBegin() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inUse >= w.max {
		return fmt.Errorf("%w: %d of %d in flight", delivery.ErrPressured, w.inUse, w.max)
	}
	w.inUse++
	return nil
}

// Begin blocks until one in-flight slot frees or ctx... the window has no
// context: Begin waits. Callers that must not block use TryBegin.
func (w *Window) Begin() {
	w.mu.Lock()
	if w.inUse < w.max {
		w.inUse++
		w.mu.Unlock()
		return
	}
	ch := w.blocked
	w.mu.Unlock()
	<-ch
	// A slot freed; retry the fast path once — the waker reserved nothing,
	// so race with other waiters is fine for the bound: TryBegin keeps it
	// exact, Begin admits at most max by re-checking.
	w.mu.Lock()
	if w.inUse >= w.max {
		w.mu.Unlock()
		w.Begin()
		return
	}
	w.inUse++
	w.mu.Unlock()
}

// Done frees one in-flight slot and wakes one waiter.
func (w *Window) Done() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inUse == 0 {
		panic("delivery/errors: Done without Begin")
	}
	w.inUse--
	if w.inUse < w.max {
		close(w.blocked)
		w.blocked = make(chan struct{})
	}
}

// InFlight returns the current in-flight count.
func (w *Window) InFlight() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inUse
}

// Max returns the window bound.
func (w *Window) Max() int { return w.max }

// PendingQueue is the bounded-memory model of the durable pending queue:
// Push always succeeds (the queue lives on disk and has no quota), Pop
// hands the oldest item to the retry loop, and Complete removes it. No
// method drops, expires or reorders items: there is no TTL and no quota.
type PendingQueue struct {
	mu      sync.Mutex
	items   []uint64
	maxSeen int
	done    int
}

// NewPendingQueue returns an empty pending queue.
func NewPendingQueue() *PendingQueue { return &PendingQueue{} }

// Push appends one item. It never fails and never drops.
func (q *PendingQueue) Push(id uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, id)
	if len(q.items) > q.maxSeen {
		q.maxSeen = len(q.items)
	}
}

// Pop returns the oldest pending item without removing it: the item stays
// until its work completes, so a crash re-reads it.
func (q *PendingQueue) Pop() (uint64, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return 0, false
	}
	return q.items[0], true
}

// Complete removes the oldest pending item after its work is durably done.
// It is the only removing operation.
func (q *PendingQueue) Complete(id uint64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 || q.items[0] != id {
		return fmt.Errorf("delivery/errors: complete of %d which is not the queue head", id)
	}
	q.items = q.items[1:]
	q.done++
	return nil
}

// Len returns the current pending count.
func (q *PendingQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// MaxLen returns the largest pending count observed — the counter that
// proves the queue is unbounded by design and bounded only by the disk.
func (q *PendingQueue) MaxLen() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.maxSeen
}
