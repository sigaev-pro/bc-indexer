package session

import (
	"errors"
	"testing"
	"time"
)

func TestMutateRaiseCommitsAndFences(t *testing.T) {
	g := NewGate()
	old, err := g.Open(1)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("step failed")
	if err := g.MutateRaise(old, 2, func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("failed step: got %v, want the step error", err)
	}
	// A failed step leaves the barrier where it was: the token still works.
	if g.Barrier().Epoch() != 0 {
		t.Fatalf("barrier after a failed step = %d, want 0", g.Barrier().Epoch())
	}
	if err := g.Check(old); err != nil {
		t.Fatalf("token after a failed step: %v", err)
	}

	ran := false
	if err := g.MutateRaise(old, 2, func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("raise step: ran=%v err=%v", ran, err)
	}
	if g.Barrier().Epoch() != 2 {
		t.Fatalf("barrier = %d, want 2", g.Barrier().Epoch())
	}
	// The step fenced its own generation: every older token is stale.
	if err := g.Check(old); !errors.Is(err, ErrSessionStale) {
		t.Fatalf("older token: got %v, want ErrSessionStale", err)
	}

	// The generation the barrier opened is the current one.
	fresh, err := g.Open(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.MutateRaise(fresh, 2, func() error { return nil }); err != nil {
		t.Fatalf("current generation: %v", err)
	}

	// A stale token never runs its step and never moves the barrier.
	ran = false
	if err := g.MutateRaise(old, 9, func() error { ran = true; return nil }); !errors.Is(err, ErrSessionStale) || ran {
		t.Fatalf("stale raise: ran=%v err=%v", ran, err)
	}
	if g.Barrier().Epoch() != 2 {
		t.Fatalf("barrier after a stale raise = %d, want 2", g.Barrier().Epoch())
	}
	// The barrier never lowers.
	if err := g.MutateRaise(fresh, 1, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if g.Barrier().Epoch() != 2 {
		t.Fatalf("barrier lowered to %d, want 2", g.Barrier().Epoch())
	}
}

// The commit of the step and the barrier are one serialized step: no other
// gate call runs between the token check and the raised barrier.
func TestMutateRaiseHoldsGateUntilCommitted(t *testing.T) {
	g := NewGate()
	old, err := g.Open(3)
	if err != nil {
		t.Fatal(err)
	}
	next, err := g.Open(4)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- g.MutateRaise(next, 5, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	checked := make(chan error, 1)
	go func() { checked <- g.Check(old) }()
	select {
	case <-checked:
		t.Fatal("an older token was checked while the raise step was still running")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The first check that ran after the step sees the raised barrier.
	if err := <-checked; !errors.Is(err, ErrSessionStale) {
		t.Fatalf("older token after the raise: got %v, want ErrSessionStale", err)
	}
}
