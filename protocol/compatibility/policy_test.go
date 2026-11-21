package compatibility

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestContinuePendingAllowsReadableOldObligations(t *testing.T) {
	p := NewPolicy(newTestMatrix(t))
	d := DurableState{Version: idOld, Mode: ModeDrain, Pending: 4, MaxDelivery: StateSealed}
	if err := p.ContinuePending(d); err != nil {
		t.Fatalf("a build whose current version reads the durable version must continue pending: %v", err)
	}
	// The durable state is a read-only input of the decision.
	if d.Version != idOld || d.Pending != 4 {
		t.Fatalf("the durable state must be unchanged: %+v", d)
	}
}

func TestContinuePendingRejectsIncompatibleStore(t *testing.T) {
	p := NewPolicy(newTestMatrix(t))
	// A store written by a newer build cannot be continued by this one;
	// the pending bytes and their version stay untouched.
	err := p.ContinuePending(DurableState{Version: idNext, Mode: ModeDrain, Pending: 1, MaxDelivery: StateReady})
	if err == nil {
		t.Fatal("continuing pending of an unreadable version must be rejected")
	}
	var r *ErrReplayUnsupported
	if !errors.As(err, &r) || r.From != idNext {
		t.Fatalf("must be ErrReplayUnsupported naming the durable version, got %v", err)
	}
}

func TestPreflightIncompatibleTransitionRejectedRegardlessOfDrain(t *testing.T) {
	p := NewPolicy(newTestMatrix(t))
	// The guard snapshot is fully quiet: the incompatible pair is still
	// rejected by the matrix, before any write, matching UPGRADE-01
	// (refusal of a persistent format change even after a drain).
	quiet := DurableState{Version: idUnread, Mode: ModeDrained, Pending: 0, MaxDelivery: StateNone}
	err := p.PreflightTransition(quiet, idCur)
	var s *ErrStoreTransitionRejected
	if !errors.As(err, &s) || s.From != idUnread || s.To != idCur {
		t.Fatalf("must be ErrStoreTransitionRejected naming both versions, got %v", err)
	}
	// The same incompatible pair is rejected mid-drain as well; the guard
	// cannot rescue what the matrix refuses.
	midDrain := DurableState{Version: idUnread, Mode: ModeDrain, Pending: 5, MaxDelivery: StateOutbox}
	err = p.PreflightTransition(midDrain, idCur)
	if !errors.As(err, &s) {
		t.Fatalf("an incompatible transition must stay rejected mid-drain, got %v", err)
	}
}

func TestPreflightCompatibleTransitionBlockedByPendingOrDrain(t *testing.T) {
	p := NewPolicy(newTestMatrix(t))
	cases := []struct {
		name string
		d    DurableState
	}{
		{
			"drain in progress",
			DurableState{Version: idOld, Mode: ModeDrain, Pending: 0, MaxDelivery: StateNone},
		},
		{
			"pending obligations remain",
			DurableState{Version: idOld, Mode: ModeLive, Pending: 3, MaxDelivery: StateReady},
		},
		{
			"sealed pending remain",
			DurableState{Version: idOld, Mode: ModeLive, Pending: 1, MaxDelivery: StateSealed},
		},
	}
	for _, tc := range cases {
		err := p.PreflightTransition(tc.d, idCur)
		if err == nil {
			t.Fatalf("%s: a compatible transition must still be blocked", tc.name)
		}
		var b *ErrTransitionBlocked
		if !errors.As(err, &b) {
			t.Fatalf("%s: must be ErrTransitionBlocked (guard veto regardless of the matrix), got %T", tc.name, err)
		}
	}
}

func TestPreflightCompatibleTransitionAllowedOnQuietStore(t *testing.T) {
	p := NewPolicy(newTestMatrix(t))
	// A completed drain closed every obligation; the matrix allowed the
	// pair, so the new build may take over the store.
	drained := DurableState{Version: idOld, Mode: ModeDrained, Pending: 0, MaxDelivery: StateNone}
	if err := p.PreflightTransition(drained, idCur); err != nil {
		t.Fatalf("a compatible transition on a quiet drained store must be allowed: %v", err)
	}
	live := DurableState{Version: idOld, Mode: ModeLive, Pending: 0, MaxDelivery: StateNone}
	if err := p.PreflightTransition(live, idCur); err != nil {
		t.Fatalf("a compatible transition on a quiet live store must be allowed: %v", err)
	}
}

func TestPreflightSameVersionWritesIgnoreGuard(t *testing.T) {
	p := NewPolicy(newTestMatrix(t))
	// Writing in the store's current version is not a transition: a live
	// build keeps capturing while earlier batches are still pending.
	d := DurableState{Version: idCur, Mode: ModeLive, Pending: 100, MaxDelivery: StateOutbox}
	if err := p.PreflightTransition(d, idCur); err != nil {
		t.Fatalf("same-version new writes must not be drain-guarded: %v", err)
	}
}

func TestPreflightRejectsNonCurrentTarget(t *testing.T) {
	p := NewPolicy(newTestMatrix(t))
	d := DurableState{Version: idCur, Mode: ModeDrained, Pending: 0, MaxDelivery: StateNone}
	err := p.PreflightTransition(d, idNext)
	var w *ErrWriteTargetNotCurrent
	if !errors.As(err, &w) || w.Target != idNext {
		t.Fatalf("must be ErrWriteTargetNotCurrent naming the target, got %v", err)
	}
}

func TestConcurrentDecisionsDuringRegistration(t *testing.T) {
	m, err := NewMatrix(fakeVersion{id: "record-base", writes: true})
	if err != nil {
		t.Fatalf("NewMatrix: %v", err)
	}
	p := NewPolicy(m)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := m.CanRead("record-base", "record-base"); err != nil {
					t.Errorf("identity read must stay allowed during registration: %v", err)
					return
				}
				d := DurableState{Version: "record-base", Mode: ModeLive, Pending: 0, MaxDelivery: StateNone}
				if err := p.PreflightTransition(d, "record-base"); err != nil {
					t.Errorf("same-version preflight must stay allowed during registration: %v", err)
					return
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		v := fakeVersion{
			id:     VersionID(fmt.Sprintf("record-%d", i)),
			reads:  []VersionID{"record-base"},
			writes: true,
		}
		if err := m.Register(v); err != nil {
			t.Fatalf("registration %d: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
}
