// The capped exponential backoff with jitter of the transient error
// class: attempt delays grow exponentially up to a cap, every delay is
// jittered within a fixed fraction, and the number of attempts is
// unlimited (DELIVERY_STATE_MACHINE, error table row TRANSIENT).
package errors

import (
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"
)

// Default backoff parameters.
const (
	// DefaultBaseDelay is the first retry delay.
	DefaultBaseDelay = 100 * time.Millisecond

	// DefaultCapDelay is the greatest retry delay.
	DefaultCapDelay = 30 * time.Second

	// DefaultJitterFraction bounds the jitter: the actual delay lies
	// within ±fraction of the computed delay.
	DefaultJitterFraction = 0.2

	// defaultFactor doubles the delay per attempt.
	defaultFactor = 2.0
)

// Backoff computes retry delays. It is safe for concurrent use. The
// random source is a seam: production wires a seeded source, tests wire a
// deterministic one.
type Backoff struct {
	mu       sync.Mutex
	base     time.Duration
	cap      time.Duration
	factor   float64
	jitter   float64
	float64n func() float64 // in [0,1)
}

// NewBackoff returns the default backoff over the given random source; a
// nil source uses math/rand's global source.
func NewBackoff(source rand.Source) *Backoff {
	var float64n func() float64
	if source != nil {
		r := rand.New(source)
		float64n = r.Float64
	} else {
		float64n = rand.Float64
	}
	return &Backoff{base: DefaultBaseDelay, cap: DefaultCapDelay,
		factor: defaultFactor, jitter: DefaultJitterFraction, float64n: float64n}
}

// Delay returns the retry delay after the given attempt number, where
// attempt 1 is the first retry. The delay doubles per attempt up to the
// cap, and the returned delay lies within the jitter fraction of the
// computed value, never below the base and never above the cap.
func (b *Backoff) Delay(attempt uint64) time.Duration {
	if attempt == 0 {
		attempt = 1
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	mult := math.Pow(b.factor, float64(attempt-1))
	scaled := float64(b.base) * mult
	if scaled > float64(b.cap) || math.IsInf(scaled, 1) {
		scaled = float64(b.cap)
	}
	spread := scaled * b.jitter
	delay := scaled - spread + b.float64n()*2*spread
	if delay < float64(b.base) {
		delay = float64(b.base)
	}
	if delay > float64(b.cap) {
		delay = float64(b.cap)
	}
	return time.Duration(delay)
}

// Validate checks the backoff parameters of a constructed policy.
func (b *Backoff) Validate() error {
	if b.base <= 0 {
		return fmt.Errorf("delivery/errors: base delay %d is not positive", b.base)
	}
	if b.cap < b.base {
		return fmt.Errorf("delivery/errors: cap %d below base %d", b.cap, b.base)
	}
	if b.jitter < 0 || b.jitter >= 1 {
		return fmt.Errorf("delivery/errors: jitter fraction %v outside [0,1)", b.jitter)
	}
	return nil
}
