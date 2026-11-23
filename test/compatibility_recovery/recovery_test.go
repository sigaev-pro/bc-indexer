// The matrix of E05-03.upgrade: prewrite rejections before the drain,
// the old-build drain that finishes the saved obligations, the
// compatible replay of pending bytes, the rollback matrix, the
// incompatible persistent store format rejected prewrite in every mode,
// and the retained lifecycle metadata through all of it. Every scenario
// runs once per applicability slot (ETH/BSC/POLY): a PASS of one family
// never closes the others.
package compatibility_recovery

import (
	"errors"
	"fmt"
	"testing"

	native "github.com/sigaev-pro/bc-indexer/adapters/maintenance"
	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/core/delivery/release"
	"github.com/sigaev-pro/bc-indexer/protocol/compatibility"
)

// blocked asserts an ErrTransitionBlocked rejection and returns it.
func blocked(t *testing.T, err error) *compatibility.ErrTransitionBlocked {
	t.Helper()
	var out *compatibility.ErrTransitionBlocked
	if !errors.As(err, &out) {
		t.Fatalf("want a drain-guard rejection, got %v", err)
	}
	return out
}

// storeRejected asserts an ErrStoreTransitionRejected rejection.
func storeRejected(t *testing.T, err error) {
	t.Helper()
	var out *compatibility.ErrStoreTransitionRejected
	if !errors.As(err, &out) {
		t.Fatalf("want a store-format rejection, got %v", err)
	}
}

// unknownVersion asserts an ErrUnknownVersion rejection.
func unknownVersion(t *testing.T, err error) {
	t.Helper()
	var out *compatibility.ErrUnknownVersion
	if !errors.As(err, &out) {
		t.Fatalf("want an unknown-version rejection, got %v", err)
	}
}

// TestWireUpgradeRejectedBeforeDrain is the first half of AC-DLV-14-02:
// with the wire/plan version incompatible with the new build, the
// preflight before the drain rejects the upgrade in every mode that
// still holds obligations or an unfinished drain, and nothing is
// written. Reading and replaying the pending records stays allowed: the
// new build continues the old obligations, which is not an upgrade.
func TestWireUpgradeRejectedBeforeDrain(t *testing.T) {
	for _, slot := range native.Slots() {
		for _, kind := range wireKinds {
			t.Run(slot.String()+"/"+string(kind.pinned), func(t *testing.T) {
				_, newBuild := buildsOf(kind.pinned, kind.successor)
				cases := []struct {
					name    string
					setup   func(s *scenario)
					pending int
				}{
					{"live-with-pending", func(s *scenario) { s.writePending(delivery.StateSealed) }, 1},
					{"drain-with-pending", func(s *scenario) {
						s.writePending(delivery.StateSealed)
						if err := s.prod.SetMode(delivery.ModeDrain); err != nil {
							t.Fatalf("SetMode: %v", err)
						}
					}, 1},
					{"drain-in-progress", func(s *scenario) {
						if err := s.prod.SetMode(delivery.ModeDrain); err != nil {
							t.Fatalf("SetMode: %v", err)
						}
					}, 0},
					{"blocked-with-pending", func(s *scenario) {
						s.writePending(delivery.StateIntent)
						if err := s.prod.SetMode(delivery.ModeBlocked); err != nil {
							t.Fatalf("SetMode: %v", err)
						}
					}, 1},
				}
				for _, tc := range cases {
					t.Run(tc.name, func(t *testing.T) {
						oldBuild, _ := buildsOf(kind.pinned, kind.successor)
						s := newScenario(t, slot, oldBuild)
						tc.setup(s)
						if got := s.durable().Pending; got != tc.pending {
							t.Fatalf("pending count is %d, want %d", got, tc.pending)
						}
						before := s.fingerprint()
						rejected := blocked(t, newBuild.wirePol.PreflightTransition(s.durable(),
							newBuild.wire, compatibility.ClaimWirePlan))
						if rejected.Snapshot.Mode != s.prod.Mode() || rejected.Snapshot.Pending != tc.pending {
							t.Fatalf("the rejection names the wrong snapshot: %+v", rejected.Snapshot)
						}
						if s.fingerprint() != before {
							t.Fatalf("the rejected prewrite changed the store image")
						}
						// The new build may still read and replay the old
						// pending records: continuing saved obligations is
						// what the old-build drain relies on, not an
						// upgrade.
						if err := newBuild.wirePol.ContinuePending(s.durable()); err != nil {
							t.Fatalf("ContinuePending: %v", err)
						}
						if s.fingerprint() != before {
							t.Fatalf("the replay decision changed the store image")
						}
					})
				}
			})
		}
	}
}

// TestOldBuildDrainsThenUpgradePasses is the second half of AC-DLV-14-02:
// after the pre-drain rejection, the old build drains the store by the
// prescribed order — no silent migration of the unfinished records — and
// once it is DRAINED the new build's preflight passes for every wire/plan
// kind, with the retained lifecycle metadata intact.
func TestOldBuildDrainsThenUpgradePasses(t *testing.T) {
	for _, slot := range native.Slots() {
		t.Run(slot.String(), func(t *testing.T) {
			for _, kind := range wireKinds {
				oldBuild, newBuild := buildsOf(kind.pinned, kind.successor)
				s := newScenario(t, slot, oldBuild)
				s.writePending(delivery.StateSealed)
				s.writePending(delivery.StateIntent)

				before := s.fingerprint()
				blocked(t, newBuild.wirePol.PreflightTransition(s.durable(), newBuild.wire,
					compatibility.ClaimWirePlan))
				if s.fingerprint() != before {
					t.Fatalf("the rejected upgrade wrote something")
				}

				ack := s.drain(0xd1)
				if ack.Highwater != 2 {
					t.Fatalf("highwater is %d, want 2", ack.Highwater)
				}
				if len(s.pending) != 0 {
					t.Fatalf("the drain left %d pending record(s)", len(s.pending))
				}
				// DRAINED by the old build opens the incompatible
				// wire/plan upgrade; writing in the store's own current
				// version stays a non-transition.
				if err := newBuild.wirePol.PreflightTransition(s.durable(), newBuild.wire,
					compatibility.ClaimWirePlan); err != nil {
					t.Fatalf("preflight after the drain: %v", err)
				}
				if err := oldBuild.wirePol.PreflightTransition(s.durable(), oldBuild.wire,
					compatibility.ClaimWirePlan); err != nil {
					t.Fatalf("the old build's own version stopped being writable: %v", err)
				}
				// The store-format pair stays rejected even now.
				storeRejected(t, newBuild.storePol.PreflightTransition(storeDurable(s, storeV1),
					storeV2, compatibility.ClaimStoreFormat))
				if producer, run := s.prod.Identity(); producer != s.producer || run != s.run {
					t.Fatalf("the drain changed the producer identity")
				}
			}
		})
	}
}

// TestCompatibleReplayRepublishesExactBytes checks the compatible-replay
// column: a build that reads the old version continues the pending
// obligations, the sealed plan replays through IMPORT_SEALED with the
// original issuer identity and frame layout, and a pending record's
// version is immutable — the same-version retry passes and any other
// version is rejected before a write.
func TestCompatibleReplayRepublishesExactBytes(t *testing.T) {
	for _, slot := range native.Slots() {
		for _, kind := range wireKinds {
			t.Run(slot.String()+"/"+string(kind.pinned), func(t *testing.T) {
				oldBuild, newBuild := buildsOf(kind.pinned, kind.successor)
				s := newScenario(t, slot, oldBuild)
				seq := s.writePending(delivery.StateSealed)

				if err := newBuild.wirePol.ContinuePending(s.durable()); err != nil {
					t.Fatalf("ContinuePending: %v", err)
				}
				rec, ok := s.prod.Batch(seq)
				if !ok || rec.State != delivery.StateSealed {
					t.Fatalf("the pending batch is not SEALED: %+v", rec)
				}
				replayed, err := release.ImportSealed(s.prod, s.gw, s.fence, seq)
				if err != nil {
					t.Fatalf("ImportSealed: %v", err)
				}
				if !replayed.SameIdentity(rec.Identity) {
					t.Fatalf("the replayed identity %v differs from %v", replayed, rec.Identity)
				}
				plan, _ := s.prod.SealedPlan(seq)
				gwRec, ok := s.gw.Batch(s.fence, replayed.Batch)
				if !ok || len(gwRec.FrameKeys) != len(plan.FrameKeys) {
					t.Fatalf("the replayed frame layout differs from the sealed plan")
				}
				// From here the image is the replayed baseline: the
				// version decisions below must not touch it.
				before := s.fingerprint()

				record := s.pending[seq]
				if err := compatibility.RewritePending(record, kind.pinned); err != nil {
					t.Fatalf("the same-version retry must pass: %v", err)
				}
				err = compatibility.RewritePending(record, kind.successor)
				var immutable *compatibility.ErrPendingImmutable
				if !errors.As(err, &immutable) || immutable.Record != record.ID ||
					immutable.Original != kind.pinned || immutable.Attempted != kind.successor {
					t.Fatalf("want a pending-immutable rejection naming the record, got %v", err)
				}
				if s.fingerprint() != before {
					t.Fatalf("a compatibility decision rewrote the store image")
				}
				s.finishPending(seq)
				if len(s.pending) != 0 {
					t.Fatalf("the replayed batch stayed pending")
				}
			})
		}
	}
}

// TestRollbackMatrix checks the rollback columns: an old build that never
// knew the new wire version answers unknown version with no fallback; an
// old build that knows it read-only cannot replay it and its write
// preflight is blocked until the build that understands the new version
// drains the store; after that drain the old version writes again, with
// the retained lifecycle metadata unchanged.
func TestRollbackMatrix(t *testing.T) {
	for _, slot := range native.Slots() {
		for _, kind := range wireKinds {
			t.Run(slot.String()+"/"+string(kind.pinned), func(t *testing.T) {
				// Column 1: the old build never registered the successor.
				oldUnaware := newBuild("old-unaware", kind.pinned, storeV1, nil, nil, nil)
				aware := newScenario(t, slot, newBuild("new", kind.successor, storeV2,
					[]compatibility.VersionID{kind.pinned}, nil, []compatibility.VersionID{storeV1}))
				aware.writePending(delivery.StateSealed)
				before := aware.fingerprint()
				unknownVersion(t, oldUnaware.wirePol.ContinuePending(aware.durable()))
				unknownVersion(t, oldUnaware.wirePol.PreflightTransition(aware.durable(),
					oldUnaware.wire, compatibility.ClaimWirePlan))
				if aware.fingerprint() != before {
					t.Fatalf("the unknown-version rejection wrote something")
				}

				// Column 2: the old build registered the successor
				// read-only, without a read claim.
				oldAware := newBuild("old-aware", kind.pinned, storeV1, nil,
					[]compatibility.VersionID{kind.successor}, []compatibility.VersionID{storeV2})
				var replay *compatibility.ErrReplayUnsupported
				if err := oldAware.wirePol.ContinuePending(aware.durable()); !errors.As(err, &replay) {
					t.Fatalf("want a replay rejection, got %v", err)
				}
				blocked(t, oldAware.wirePol.PreflightTransition(aware.durable(), oldAware.wire,
					compatibility.ClaimWirePlan))
				if aware.fingerprint() != before {
					t.Fatalf("the blocked rollback wrote something")
				}

				// The build that understands the successor drains the
				// store; only then does the old version write again.
				aware.drain(0xe1)
				if err := oldAware.wirePol.PreflightTransition(aware.durable(), oldAware.wire,
					compatibility.ClaimWirePlan); err != nil {
					t.Fatalf("preflight of the rolled-back version after the drain: %v", err)
				}
				if producer, run := aware.prod.Identity(); producer != aware.producer || run != aware.run {
					t.Fatalf("the rollback dance changed the producer identity")
				}
			})
		}
	}
}

// TestStoreFormatRejectedPrewriteInEveryMode checks the never-rule: an
// incompatible persistent store format is rejected before any write in
// every producer mode — including a completed drain with an empty
// payload (UPGRADE-01) — in both directions, and the store image never
// changes.
func TestStoreFormatRejectedPrewriteInEveryMode(t *testing.T) {
	for _, slot := range native.Slots() {
		t.Run(slot.String(), func(t *testing.T) {
			oldBuild, newBuild := buildsOf(wireKinds[0].pinned, wireKinds[0].successor)
			cases := []struct {
				name  string
				setup func(t *testing.T, s *scenario)
			}{
				{"live-with-pending", func(t *testing.T, s *scenario) { s.writePending(delivery.StateSealed) }},
				{"drain-with-pending", func(t *testing.T, s *scenario) {
					s.writePending(delivery.StateReady)
					if err := s.prod.SetMode(delivery.ModeDrain); err != nil {
						t.Fatalf("SetMode: %v", err)
					}
				}},
				{"drain-empty", func(t *testing.T, s *scenario) {
					if err := s.prod.SetMode(delivery.ModeDrain); err != nil {
						t.Fatalf("SetMode: %v", err)
					}
				}},
				{"blocked", func(t *testing.T, s *scenario) {
					if err := s.prod.SetMode(delivery.ModeBlocked); err != nil {
						t.Fatalf("SetMode: %v", err)
					}
				}},
				{"drained-empty-payload", func(t *testing.T, s *scenario) {
					s.writePending(delivery.StateSealed)
					s.finishPending(s.pendingSequences()[0])
					s.drain(0xf1)
				}},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					s := newScenario(t, slot, oldBuild)
					tc.setup(t, s)
					before := s.fingerprint()
					// The upgrade direction: the new store format over a
					// store carrying the old one.
					storeRejected(t, newBuild.storePol.PreflightTransition(storeDurable(s, storeV1),
						storeV2, compatibility.ClaimStoreFormat))
					// The rollback direction: the old store format over a
					// store carrying the new one.
					s.storeFmt = storeV2
					storeRejected(t, oldBuild.storePol.PreflightTransition(storeDurable(s, storeV2),
						storeV1, compatibility.ClaimStoreFormat))
					s.storeFmt = storeV1
					if s.fingerprint() != before {
						t.Fatalf("a rejected store-format prewrite changed the store image")
					}
				})
			}
		})
	}
}

// storeDurable is the policy input for a store-format decision: the
// store's format version with the same drain facts as the wire input.
func storeDurable(s *scenario, version compatibility.VersionID) compatibility.DurableState {
	d := s.durable()
	d.Version = version
	return d
}

// TestRetainedLifecycleMetadataSurvivesDrainAndReplay checks the retained
// lifecycle metadata through compatible replay and the old-build drain:
// the producer/run identity, the ACKed Applied reference of a saved
// activation (D59: later events keep pointing at the delivery that
// acknowledged them) and the retained future-GC pins survive unchanged,
// and a later event of the saved activation still plans against the
// retained reference after the drain.
func TestRetainedLifecycleMetadataSurvivesDrainAndReplay(t *testing.T) {
	for _, slot := range native.Slots() {
		t.Run(slot.String(), func(t *testing.T) {
			oldBuild, newBuild := buildsOf(wireKinds[0].pinned, wireKinds[0].successor)
			s := newScenario(t, slot, oldBuild)
			applied := s.writePending(delivery.StateSealed)
			s.finishPending(applied)
			activation := native.Activation{ID: fmt.Sprintf("%s-act-1", slot.Client),
				Applied: native.Reference{Binding: s.binding, Batch: applied}}
			if err := s.lifecycles.Retain(activation); err != nil {
				t.Fatalf("Retain: %v", err)
			}
			pin := native.Pin{ID: fmt.Sprintf("%s-pin-1", slot.Client), Reason: "future finalized anchor"}
			if err := s.pins.Retain(pin); err != nil {
				t.Fatalf("Retain pin: %v", err)
			}
			pending := s.writePending(delivery.StateSealed)

			// The new build replays the pending record, the old build
			// finishes and drains.
			if err := newBuild.wirePol.ContinuePending(s.durable()); err != nil {
				t.Fatalf("ContinuePending: %v", err)
			}
			s.finishPending(pending)
			s.drain(0xc1)

			if saved, ok := s.lifecycles.Get(activation.ID); !ok || saved != activation {
				t.Fatalf("the ACKed Applied reference changed: %+v", saved)
			}
			if kept := s.pins.Pins(); len(kept) != 1 || kept[0] != pin {
				t.Fatalf("the retained pins changed: %+v", kept)
			}
			if producer, run := s.prod.Identity(); producer != s.producer || run != s.run {
				t.Fatalf("the drain changed the producer identity")
			}
			// A later event of the saved activation plans against the
			// retained reference after the drain.
			plan, err := s.lifecycles.PlanEvent(s.binding, activation.ID, native.EventStatus, applied+10)
			if err != nil {
				t.Fatalf("PlanEvent: %v", err)
			}
			if plan.Prior != activation.Applied {
				t.Fatalf("the planned event lost the retained reference: %+v", plan.Prior)
			}
			// The compatible build continues the (now empty) store, and
			// the store-format pair still never transitions.
			if err := newBuild.wirePol.ContinuePending(s.durable()); err != nil {
				t.Fatalf("ContinuePending after the drain: %v", err)
			}
			storeRejected(t, newBuild.storePol.PreflightTransition(storeDurable(s, storeV1),
				storeV2, compatibility.ClaimStoreFormat))
		})
	}
}
