package compatibility

import (
	"errors"
	"strings"
	"testing"
)

func TestDrainGuardAllowsQuietStore(t *testing.T) {
	g := DrainGuard{}
	for _, mode := range []Mode{ModeLive, ModeDrained, ModeBlocked} {
		s := DrainSnapshot{Mode: mode, Pending: 0, MaxDelivery: StateNone}
		if err := g.AllowFormatTransition(s); err != nil {
			t.Fatalf("%s with zero pending must allow a transition: %v", mode, err)
		}
	}
}

func TestDrainGuardRejectsAnyPendingObligation(t *testing.T) {
	g := DrainGuard{}
	states := []DeliveryState{StateIntent, StateStaging, StateReady, StateSealed, StateAckedCleanup, StateOutbox}
	for _, st := range states {
		s := DrainSnapshot{Mode: ModeLive, Pending: 2, MaxDelivery: st}
		err := g.AllowFormatTransition(s)
		if err == nil {
			t.Fatalf("pending obligations at %s must block a transition", st)
		}
		var b *ErrTransitionBlocked
		if !errors.As(err, &b) {
			t.Fatalf("%s: must be ErrTransitionBlocked, got %T", st, err)
		}
		if b.Snapshot.Pending != 2 || b.Snapshot.Mode != ModeLive || b.Snapshot.MaxDelivery != st {
			t.Fatalf("%s: rejection must carry the snapshot, got %+v", st, b.Snapshot)
		}
		if !strings.Contains(err.Error(), "2 pending") {
			t.Fatalf("%s: message must report the pending count: %v", st, err)
		}
	}
}

func TestDrainGuardNamesSealedOrLaterPending(t *testing.T) {
	g := DrainGuard{}
	for _, st := range []DeliveryState{StateSealed, StateAckedCleanup, StateOutbox} {
		s := DrainSnapshot{Mode: ModeDrained, Pending: 1, MaxDelivery: st}
		err := g.AllowFormatTransition(s)
		var b *ErrTransitionBlocked
		if !errors.As(err, &b) {
			t.Fatalf("%s: must be ErrTransitionBlocked, got %T", st, err)
		}
		if !strings.Contains(b.Reason, "SEALED or later") {
			t.Fatalf("%s: mid-drain pending must be named as SEALED or later, got %q", st, b.Reason)
		}
	}
	// Pre-sealed pending is also blocked, with the general reason.
	s := DrainSnapshot{Mode: ModeLive, Pending: 1, MaxDelivery: StateReady}
	err := g.AllowFormatTransition(s)
	var b *ErrTransitionBlocked
	if !errors.As(err, &b) || b.Reason != "pending obligations remain" {
		t.Fatalf("pre-sealed pending must use the general reason, got %v", err)
	}
}

func TestDrainGuardRejectsDrainInProgressEvenWithoutPending(t *testing.T) {
	g := DrainGuard{}
	s := DrainSnapshot{Mode: ModeDrain, Pending: 0, MaxDelivery: StateNone}
	err := g.AllowFormatTransition(s)
	if err == nil {
		t.Fatal("a drain in progress must block a transition even with zero pending")
	}
	var b *ErrTransitionBlocked
	if !errors.As(err, &b) || !strings.Contains(b.Reason, "drain is in progress") {
		t.Fatalf("must name the in-progress drain, got %v", err)
	}
}

func TestDrainGuardRejectsInvalidSnapshots(t *testing.T) {
	g := DrainGuard{}
	cases := []struct {
		name string
		s    DrainSnapshot
	}{
		{"unset mode", DrainSnapshot{Mode: ModeUnset, Pending: 0, MaxDelivery: StateNone}},
		{"unknown mode", DrainSnapshot{Mode: Mode(9), Pending: 0, MaxDelivery: StateNone}},
		{"negative pending", DrainSnapshot{Mode: ModeLive, Pending: -1, MaxDelivery: StateIntent}},
		{"unknown delivery state", DrainSnapshot{Mode: ModeLive, Pending: 1, MaxDelivery: DeliveryState(9)}},
		{"state without pending", DrainSnapshot{Mode: ModeLive, Pending: 0, MaxDelivery: StateReady}},
		{"pending without state", DrainSnapshot{Mode: ModeLive, Pending: 3, MaxDelivery: StateNone}},
	}
	for _, tc := range cases {
		err := g.AllowFormatTransition(tc.s)
		if err == nil {
			t.Fatalf("%s: must be rejected", tc.name)
		}
		var inv *ErrInvalidSnapshot
		if !errors.As(err, &inv) {
			t.Fatalf("%s: must be ErrInvalidSnapshot, got %T", tc.name, err)
		}
	}
}

func TestIncompatibleWireUpgradeRequiresCompletedDrain(t *testing.T) {
	g := DrainGuard{}
	// Only the completed, sealed drain opens an incompatible wire/plan
	// upgrade; every otherwise-quiet snapshot that is not DRAINED is
	// rejected with the DRAINED requirement named.
	drained := DrainSnapshot{Mode: ModeDrained, Pending: 0, MaxDelivery: StateNone}
	if err := g.AllowIncompatibleWireUpgrade(drained); err != nil {
		t.Fatalf("DRAINED with zero pending must allow an incompatible wire/plan upgrade: %v", err)
	}
	for _, mode := range []Mode{ModeLive, ModeBlocked} {
		s := DrainSnapshot{Mode: mode, Pending: 0, MaxDelivery: StateNone}
		err := g.AllowIncompatibleWireUpgrade(s)
		var b *ErrTransitionBlocked
		if !errors.As(err, &b) {
			t.Fatalf("%s: must be ErrTransitionBlocked, got %T", mode, err)
		}
		if !strings.Contains(b.Reason, "requires DRAINED") {
			t.Fatalf("%s: rejection must name the DRAINED requirement, got %q", mode, b.Reason)
		}
	}
}

func TestIncompatibleWireUpgradeKeepsOrdinaryTransitionRules(t *testing.T) {
	g := DrainGuard{}
	// The ordinary rules still apply first: an in-progress drain and any
	// pending obligation block the incompatible upgrade too.
	cases := []struct {
		name   string
		s      DrainSnapshot
		reason string
	}{
		{
			"drain in progress",
			DrainSnapshot{Mode: ModeDrain, Pending: 0, MaxDelivery: StateNone},
			"drain is in progress",
		},
		{
			"pending obligations remain",
			DrainSnapshot{Mode: ModeDrained, Pending: 2, MaxDelivery: StateReady},
			"pending obligations remain",
		},
		{
			"sealed pending remain",
			DrainSnapshot{Mode: ModeLive, Pending: 1, MaxDelivery: StateSealed},
			"SEALED or later",
		},
	}
	for _, tc := range cases {
		err := g.AllowIncompatibleWireUpgrade(tc.s)
		var b *ErrTransitionBlocked
		if !errors.As(err, &b) {
			t.Fatalf("%s: must be ErrTransitionBlocked, got %T", tc.name, err)
		}
		if !strings.Contains(b.Reason, tc.reason) {
			t.Fatalf("%s: rejection must name %q, got %q", tc.name, tc.reason, b.Reason)
		}
	}
	// Contradictory input stays invalid input here as well.
	err := g.AllowIncompatibleWireUpgrade(DrainSnapshot{Mode: ModeUnset})
	var inv *ErrInvalidSnapshot
	if !errors.As(err, &inv) {
		t.Fatalf("an invalid snapshot must stay ErrInvalidSnapshot, got %T", err)
	}
}
