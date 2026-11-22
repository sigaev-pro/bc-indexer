package errors

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
)

func TestClassificationTable(t *testing.T) {
	c := NewClassifier()
	cases := []struct {
		err    error
		class  Class
		action Action
	}{
		{fmt.Errorf("wrap: %w", ErrLinkLost), ClassRemoteOutage, ActionContinueLocal},
		{fmt.Errorf("wrap: %w", ErrLocalDiskFatal), ClassLocalFatal, ActionStopLocalOwner},
		{fmt.Errorf("wrap: %w", ErrJournalFatal), ClassLocalFatal, ActionStopLocalOwner},
		{fmt.Errorf("wrap: %w", ErrNoSpaceLeft), ClassNoSpace, ActionStopCapture},
		{fmt.Errorf("wrap: %w", ErrConfiguration), ClassConfigBlocked, ActionStopPublication},
		{fmt.Errorf("wrap: %w", ErrPoisonBatch), ClassPoison, ActionHoldFrontier},
		{context.DeadlineExceeded, ClassTransient, ActionRetryBackoff},
		{&netTimeoutError{}, ClassTransient, ActionRetryBackoff},
		{syscall.ENOSPC, ClassNoSpace, ActionStopCapture},
		{errors.New("something odd"), ClassUnknown, ActionDiagnose},
		{nil, ClassUnknown, ActionDiagnose},
	}
	for _, tc := range cases {
		class, known := c.Classify(tc.err)
		if class != tc.class {
			t.Errorf("Classify(%v) = %s, want %s", tc.err, class, tc.class)
		}
		if got := MustClassifyForTest(c, tc.err); got != tc.action {
			t.Errorf("action of %v = %s, want %s", tc.err, got, tc.action)
		}
		if tc.err != nil && tc.class != ClassUnknown && !known {
			t.Errorf("Classify(%v) reported unknown for %s", tc.err, tc.class)
		}
	}
}

func MustClassifyForTest(c *Classifier, err error) Action { return c.MustClassify(err) }

type netTimeoutError struct{}

func (*netTimeoutError) Error() string   { return "i/o timeout" }
func (*netTimeoutError) Timeout() bool   { return true }
func (*netTimeoutError) Temporary() bool { return true }

func TestClassifierRegistersEngineSentinels(t *testing.T) {
	c := NewClassifier()
	// The publisher and destination engines register their sentinels at
	// wiring time; a foreign sentinel is unknown before that.
	foreign := errors.New("nats: connection closed")
	if class, known := c.Classify(foreign); known || class != ClassUnknown {
		t.Fatalf("foreign sentinel classified as %s before registration", class)
	}
	if err := c.Register(foreign, ClassTransient); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if class, _ := c.Classify(foreign); class != ClassTransient {
		t.Fatalf("registered sentinel classified as %s", class)
	}
	// Re-registering with another class is a wiring mistake.
	if err := c.Register(foreign, ClassPoison); err == nil {
		t.Fatalf("re-registration with another class accepted")
	}
	if err := c.Register(nil, ClassTransient); err == nil {
		t.Fatalf("nil sentinel accepted")
	}
}

func TestLocalFatalDiffersFromRemoteOutage(t *testing.T) {
	c := NewClassifier()
	local := c.MustClassify(fmt.Errorf("wal: %w", ErrJournalFatal))
	remote := c.MustClassify(fmt.Errorf("gateway link: %w", ErrLinkLost))
	if local != ActionStopLocalOwner {
		t.Fatalf("local journal fatal prescribes %s", local)
	}
	if remote != ActionContinueLocal {
		t.Fatalf("lost link prescribes %s; a lost link is never proof of remote corruption", remote)
	}
	if local == remote {
		t.Fatalf("local fatal and remote outage collapsed into one action")
	}
}

func TestBackoffCapsAndJitter(t *testing.T) {
	// A deterministic source of exactly one half keeps every delay at the
	// computed midpoint, so the exponential growth and the cap are exact.
	b := NewBackoff(midSource{})
	if err := b.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	prev := time.Duration(0)
	for attempt := uint64(1); attempt <= 20; attempt++ {
		d := b.Delay(attempt)
		if d > DefaultCapDelay {
			t.Fatalf("attempt %d delay %v above the cap %v", attempt, d, DefaultCapDelay)
		}
		if d < prev {
			t.Fatalf("attempt %d delay %v decreased below %v", attempt, d, prev)
		}
		prev = d
	}
	if prev != DefaultCapDelay {
		t.Fatalf("the sequence never reached the cap: %v", prev)
	}
	// The delay stays within the jitter band: the extreme sources bound
	// every delay of the same attempt.
	low := NewBackoff(zeroSource{})
	high := NewBackoff(oneSource{})
	for attempt := uint64(1); attempt <= 10; attempt++ {
		lo, hi := low.Delay(attempt), high.Delay(attempt)
		if lo > hi {
			t.Fatalf("jitter band inverted at attempt %d: %v > %v", attempt, lo, hi)
		}
		spread := time.Duration(float64(hi) - float64(lo))
		if spread > time.Duration(2*DefaultJitterFraction*float64(DefaultCapDelay)) {
			t.Fatalf("jitter spread %v exceeds the fraction", spread)
		}
	}
}

type zeroSource struct{}
type oneSource struct{}
type midSource struct{}

func (midSource) Int63() int64   { return math.MaxInt64 / 2 }
func (midSource) Seed(int64)     {}
func (midSource) Uint64() uint64 { return math.MaxUint64 / 2 }

func (zeroSource) Int63() int64 { return 0 }

// MaxInt64-2048 is the largest Int63 whose float64 division stays below
// one: values closer to 2^63 round up to 1.0 and resample forever.
func (oneSource) Int63() int64      { return math.MaxInt64 - 2048 }
func (s zeroSource) Seed(int64)     {}
func (s oneSource) Seed(int64)      {}
func (s zeroSource) Uint64() uint64 { return 0 }
func (s oneSource) Uint64() uint64  { return math.MaxUint64 - 2048 }

func TestWindowBoundedInFlight(t *testing.T) {
	w, err := NewWindow(2)
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	if err := w.TryBegin(); err != nil {
		t.Fatalf("first begin: %v", err)
	}
	if err := w.TryBegin(); err != nil {
		t.Fatalf("second begin: %v", err)
	}
	if err := w.TryBegin(); !errors.Is(err, delivery.ErrPressured) {
		t.Fatalf("third begin: %v, want ErrPressured", err)
	}
	if got := w.InFlight(); got != 2 {
		t.Fatalf("in flight %d", got)
	}
	w.Done()
	if err := w.TryBegin(); err != nil {
		t.Fatalf("begin after done: %v", err)
	}
	if _, err := NewWindow(0); err == nil {
		t.Fatalf("zero window accepted")
	}
}

func TestWindowBlockingBeginWakes(t *testing.T) {
	w, _ := NewWindow(1)
	if err := w.TryBegin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	done := make(chan struct{})
	go func() {
		w.Begin()
		close(done)
	}()
	select {
	case <-done:
		t.Fatalf("Begin returned while the window was full")
	case <-time.After(10 * time.Millisecond):
	}
	w.Done()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Begin never woke after Done")
	}
	if got := w.InFlight(); got != 1 {
		t.Fatalf("in flight %d after handover", got)
	}
}

func TestPendingQueueNoTTLNoQuota(t *testing.T) {
	q := NewPendingQueue()
	for i := uint64(1); i <= 1000; i++ {
		q.Push(i)
	}
	if q.Len() != 1000 || q.MaxLen() != 1000 {
		t.Fatalf("queue len %d max %d; pending has no quota", q.Len(), q.MaxLen())
	}
	// Nothing ever drops: only Complete removes, from the head, in order.
	head, ok := q.Pop()
	if !ok || head != 1 {
		t.Fatalf("head %d ok=%v", head, ok)
	}
	if err := q.Complete(1); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := q.Complete(3); err == nil {
		t.Fatalf("out-of-order complete accepted")
	}
	if q.Len() != 999 {
		t.Fatalf("len %d after one complete", q.Len())
	}
	// A crashed retry loop re-reads the same head.
	again, _ := q.Pop()
	if again != 2 {
		t.Fatalf("re-read head %d", again)
	}
}

func TestWindowUnderConcurrency(t *testing.T) {
	w, _ := NewWindow(4)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := w.TryBegin(); err == nil {
					w.Done()
				}
			}
		}()
	}
	wg.Wait()
	if got := w.InFlight(); got != 0 {
		t.Fatalf("in flight %d after all workers done", got)
	}
}
