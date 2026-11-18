package admission

import (
	"testing"
	"time"
)

// These portable controls compile the byte-identical installed admission
// package. They do not substitute for the native Bor vote handoff.
func assertHookMutexHeld(t *testing.T, g *Guard) {
	t.Helper()
	if g.lock.TryLock() {
		// Keep the unexpectedly free mutex held so token cleanup is valid.
		t.Error("Acquire returned or reported acquisition without holding the mutex")
	}
}

func TestFaultGuardNilHook(t *testing.T) {
	ExtractorFaultGuardAcquire = nil
	g := NewGuard()
	token := g.Acquire()
	assertHookMutexHeld(t, g)
	if err := token.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFaultGuardUncontendedHook(t *testing.T) {
	g := NewGuard()
	events := []string{}
	ExtractorFaultGuardAcquire = func(actual *Guard, step string) {
		if actual != g {
			t.Error("hook received another guard")
		}
		events = append(events, step)
		assertHookMutexHeld(t, g)
	}
	defer func() { ExtractorFaultGuardAcquire = nil }()
	token := g.Acquire()
	if len(events) != 1 || events[0] != "acquired" {
		t.Errorf("uncontended events: %v", events)
	}
	if err := token.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFaultGuardContendedHook(t *testing.T) {
	ExtractorFaultGuardAcquire = nil
	g := NewGuard()
	holder := g.Acquire()
	contended := make(chan struct{}, 1)
	released := make(chan struct{})
	acquired := make(chan struct{}, 1)
	done := make(chan *Token, 1)
	ExtractorFaultGuardAcquire = func(actual *Guard, step string) {
		if actual != g {
			t.Error("hook received another guard")
		}
		switch step {
		case "contended":
			assertHookMutexHeld(t, g)
			contended <- struct{}{}
		case "acquired":
			// Inspect ownership after the original holder released. A mutant
			// that omits the blocking Lock cannot hide behind that holder.
			<-released
			assertHookMutexHeld(t, g)
			acquired <- struct{}{}
		default:
			t.Errorf("unknown hook event: %s", step)
		}
	}
	defer func() { ExtractorFaultGuardAcquire = nil }()
	go func() { done <- g.Acquire() }()
	select {
	case <-contended:
	case <-time.After(time.Second):
		t.Error("held guard did not report actual contention")
	}
	if err := holder.Release(); err != nil {
		t.Error(err)
	}
	close(released)
	select {
	case token := <-done:
		select {
		case <-acquired:
		default:
			t.Error("guard returned without reporting acquisition")
		}
		if err := token.Release(); err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Fatal("guard did not acquire after holder release")
	}
}
