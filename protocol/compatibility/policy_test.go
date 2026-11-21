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

func TestPreflightIncompatibleStoreTransitionRejectedRegardlessOfDrain(t *testing.T) {
	p := NewPolicy(newTestMatrix(t))
	// The guard snapshot is fully quiet: the incompatible pair is still
	// rejected by the matrix, before any write, matching UPGRADE-01
	// (refusal of a persistent format change even after a drain). The
	// prewrite rejection holds for every drain state, so no drain state
	// is ever spent on permitting it.
	cases := []struct {
		name string
		d    DurableState
	}{
		{
			"quiet drained store",
			DurableState{Version: idUnread, Mode: ModeDrained, Pending: 0, MaxDelivery: StateNone},
		},
		{
			"quiet live store",
			DurableState{Version: idUnread, Mode: ModeLive, Pending: 0, MaxDelivery: StateNone},
		},
		{
			"quiet blocked store",
			DurableState{Version: idUnread, Mode: ModeBlocked, Pending: 0, MaxDelivery: StateNone},
		},
		{
			"mid-drain with pending",
			DurableState{Version: idUnread, Mode: ModeDrain, Pending: 5, MaxDelivery: StateOutbox},
		},
		{
			"live with pending",
			DurableState{Version: idUnread, Mode: ModeLive, Pending: 2, MaxDelivery: StateReady},
		},
	}
	for _, tc := range cases {
		err := p.PreflightTransition(tc.d, idCur, ClaimStoreFormat)
		var s *ErrStoreTransitionRejected
		if !errors.As(err, &s) || s.From != idUnread || s.To != idCur {
			t.Fatalf("%s: must be ErrStoreTransitionRejected naming both versions, got %v", tc.name, err)
		}
	}
}

func TestPreflightIncompatibleWireUpgradeRequiresDrained(t *testing.T) {
	p := NewPolicy(newTestMatrix(t))
	// An incompatible wire/plan pair passes the matrix, but the upgrade
	// is a timing question: the old build must be DRAINED first. A quiet
	// LIVE or BLOCKED store, an in-progress drain and any pending
	// obligation are all rejected.
	cases := []struct {
		name string
		d    DurableState
	}{
		{
			"quiet live store",
			DurableState{Version: idUnread, Mode: ModeLive, Pending: 0, MaxDelivery: StateNone},
		},
		{
			"quiet blocked store",
			DurableState{Version: idUnread, Mode: ModeBlocked, Pending: 0, MaxDelivery: StateNone},
		},
		{
			"drain in progress",
			DurableState{Version: idUnread, Mode: ModeDrain, Pending: 0, MaxDelivery: StateNone},
		},
		{
			"drained but pending obligations remain",
			DurableState{Version: idUnread, Mode: ModeDrained, Pending: 1, MaxDelivery: StateSealed},
		},
	}
	for _, tc := range cases {
		err := p.PreflightTransition(tc.d, idCur, ClaimWirePlan)
		if err == nil {
			t.Fatalf("%s: an incompatible wire/plan upgrade must wait for DRAINED", tc.name)
		}
		var b *ErrTransitionBlocked
		if !errors.As(err, &b) {
			t.Fatalf("%s: must be ErrTransitionBlocked (drain-guard veto), got %T", tc.name, err)
		}
	}
}

func TestPreflightIncompatibleWireUpgradeAllowedAfterDrained(t *testing.T) {
	p := NewPolicy(newTestMatrix(t))
	// DELIVERY_STATE_MACHINE: an incompatible wire/plan upgrade requires
	// DRAINED by the old build — and DRAINED is exactly enough, because
	// the completed drain closed every saved obligation, so the current
	// version's inability to read the old format stops mattering for new
	// writes.
	drained := DurableState{Version: idUnread, Mode: ModeDrained, Pending: 0, MaxDelivery: StateNone}
	if err := p.PreflightTransition(drained, idCur, ClaimWirePlan); err != nil {
		t.Fatalf("an incompatible wire/plan upgrade after a completed drain must be allowed: %v", err)
	}
	// The same pair stays rejected in the store-format family: a drain
	// never permits a persistent store format change.
	err := p.PreflightTransition(drained, idCur, ClaimStoreFormat)
	var s *ErrStoreTransitionRejected
	if !errors.As(err, &s) {
		t.Fatalf("the same drained snapshot must not permit a store format change, got %v", err)
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
	for _, claim := range []ClaimType{ClaimWirePlan, ClaimStoreFormat} {
		for _, tc := range cases {
			err := p.PreflightTransition(tc.d, idCur, claim)
			if err == nil {
				t.Fatalf("%s (%s): a compatible transition must still be blocked", tc.name, claim)
			}
			var b *ErrTransitionBlocked
			if !errors.As(err, &b) {
				t.Fatalf("%s (%s): must be ErrTransitionBlocked (guard veto regardless of the matrix), got %T", tc.name, claim, err)
			}
		}
	}
}

func TestPreflightCompatibleTransitionAllowedOnQuietStore(t *testing.T) {
	p := NewPolicy(newTestMatrix(t))
	// A completed drain closed every obligation; the matrix allowed the
	// pair, so the new build may take over the store.
	drained := DurableState{Version: idOld, Mode: ModeDrained, Pending: 0, MaxDelivery: StateNone}
	live := DurableState{Version: idOld, Mode: ModeLive, Pending: 0, MaxDelivery: StateNone}
	for _, claim := range []ClaimType{ClaimWirePlan, ClaimStoreFormat} {
		if err := p.PreflightTransition(drained, idCur, claim); err != nil {
			t.Fatalf("%s: a compatible transition on a quiet drained store must be allowed: %v", claim, err)
		}
		if err := p.PreflightTransition(live, idCur, claim); err != nil {
			t.Fatalf("%s: a compatible transition on a quiet live store must be allowed: %v", claim, err)
		}
	}
}

func TestPreflightSameVersionWritesIgnoreGuard(t *testing.T) {
	p := NewPolicy(newTestMatrix(t))
	// Writing in the store's current version is not a transition: a live
	// build keeps capturing while earlier batches are still pending.
	d := DurableState{Version: idCur, Mode: ModeLive, Pending: 100, MaxDelivery: StateOutbox}
	if err := p.PreflightTransition(d, idCur, ClaimWirePlan); err != nil {
		t.Fatalf("same-version new writes must not be drain-guarded: %v", err)
	}
}

func TestPreflightRejectsNonCurrentTarget(t *testing.T) {
	p := NewPolicy(newTestMatrix(t))
	d := DurableState{Version: idCur, Mode: ModeDrained, Pending: 0, MaxDelivery: StateNone}
	err := p.PreflightTransition(d, idNext, ClaimStoreFormat)
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
				if err := p.PreflightTransition(d, "record-base", ClaimWirePlan); err != nil {
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
