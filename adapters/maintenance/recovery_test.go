// The recovery-side tests of E05-03.drain: the retained-state stores
// (saved obligations, pins, lifecycle activations with their ACKed
// references), the lost-ResumeAck recovery over the exact MAINTENANCE
// intent in both binding variants of AC-DLV-11-03, the cutover lifecycle
// rule of AC-DLV-12-01 (D59), and the concurrency of the mode barrier.
package maintenance

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// TestFrontierSteps checks the saved-obligation rules: ordered one-step
// recovery, the closing batch allocation and the refusals.
func TestFrontierSteps(t *testing.T) {
	f := NewMemoryFrontier()
	if err := f.Save(Obligation{Sequence: 0, Stage: ObligAdmitted}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero sequence: %v", err)
	}
	if err := f.Save(Obligation{Sequence: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing stage: %v", err)
	}
	if err := f.Save(Obligation{Sequence: 1, Stage: ObligDecided, Batch: 3}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("batch before the closing step: %v", err)
	}
	if err := f.Save(Obligation{Sequence: 1, Stage: ObligAdmitted}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := f.Save(Obligation{Sequence: 1, Stage: ObligAdmitted}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("double save: %v", err)
	}
	if err := f.Advance(2, ObligExecReady, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing obligation: %v", err)
	}
	if err := f.Advance(1, ObligDecided, 0); !errors.Is(err, ErrTransition) {
		t.Fatalf("skipped stage: %v", err)
	}
	if err := f.Advance(1, ObligAdmitted, 0); !errors.Is(err, ErrTransition) {
		t.Fatalf("backwards stage: %v", err)
	}
	if err := f.Advance(1, ObligExecReady, 5); !errors.Is(err, ErrTransition) {
		t.Fatalf("batch outside the closing step: %v", err)
	}
	for _, stage := range []ObligationStage{ObligExecReady, ObligDecided} {
		if err := f.Advance(1, stage, 0); err != nil {
			t.Fatalf("Advance to %s: %v", stage, err)
		}
	}
	if got := f.Obligations(); len(got) != 1 || !got[0].BatchCapable() {
		t.Fatalf("a DECIDED obligation without a batch must stay batch-capable: %+v", got)
	}
	if err := f.Advance(1, ObligFinished, 3); err != nil {
		t.Fatalf("Advance to FINISHED: %v", err)
	}
	if err := f.Advance(1, ObligFinished, 4); !errors.Is(err, ErrTransition) {
		t.Fatalf("a closed obligation takes no further step: %v", err)
	}
	got := f.Obligations()
	if len(got) != 1 || got[0].BatchCapable() || got[0].Batch != 3 {
		t.Fatalf("the closed obligation is %+v", got[0])
	}
}

// TestPinLedgerRules checks the retained-pin rules: idempotent retention,
// immutable content and the release that only the GC owner performs.
func TestPinLedgerRules(t *testing.T) {
	l := NewPinLedger()
	if err := l.Retain(Pin{ID: "", Reason: "anchor"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty id: %v", err)
	}
	pin := Pin{ID: "side-branch", Reason: "future finalized anchor"}
	if err := l.Retain(pin); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if err := l.Retain(pin); err != nil {
		t.Fatalf("idempotent retain: %v", err)
	}
	if err := l.Retain(Pin{ID: "side-branch", Reason: "other"}); !errors.Is(err, ErrImmutable) {
		t.Fatalf("changed reason: %v", err)
	}
	if err := l.Retain(Pin{ID: "replay", Reason: "future finalized anchor"}); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	kept := l.Pins()
	if len(kept) != 2 || kept[0].ID != "side-branch" || kept[1].ID != "replay" {
		t.Fatalf("retention order changed: %+v", kept)
	}
	if err := l.Release("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing pin: %v", err)
	}
	if err := l.Release("side-branch"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if kept = l.Pins(); len(kept) != 1 || kept[0].ID != "replay" {
		t.Fatalf("the wrong pin was released: %+v", kept)
	}
}

// TestLifecyclesRules checks the retained-activation rules: the ACKed
// Applied reference is immutable, and a planned event keeps it while
// targeting the current binding.
func TestLifecyclesRules(t *testing.T) {
	l := NewLifecycles()
	old := proto.Hash32{0x0a}
	next := proto.Hash32{0x0b}
	act := Activation{ID: "act-1", Applied: Reference{Binding: old, Batch: 4}}
	if err := l.Retain(act); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if err := l.Retain(act); err != nil {
		t.Fatalf("idempotent retain: %v", err)
	}
	if err := l.Retain(Activation{ID: "act-1", Applied: Reference{Binding: next}}); !errors.Is(err, ErrImmutable) {
		t.Fatalf("rewritten reference: %v", err)
	}
	if _, err := l.PlanEvent(next, "act-1", EventRemoved, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero sequence: %v", err)
	}
	if _, err := l.PlanEvent(proto.Hash32{}, "act-1", EventStatus, 9); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero target: %v", err)
	}
	if _, err := l.PlanEvent(next, "act-1", "APPLIED", 9); !errors.Is(err, ErrInvalid) {
		t.Fatalf("applied kind: %v", err)
	}
	if _, err := l.PlanEvent(next, "missing", EventRemoved, 9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing activation: %v", err)
	}
	plan, err := l.PlanEvent(next, "act-1", EventRemoved, 9)
	if err != nil {
		t.Fatalf("PlanEvent: %v", err)
	}
	if plan.Target != next || plan.Prior != (Reference{Binding: old, Batch: 4}) {
		t.Fatalf("the planned event is %+v", plan)
	}
	saved, ok := l.Get("act-1")
	if !ok || saved != act {
		t.Fatalf("planning changed the saved activation: %+v", saved)
	}
	if ids := l.IDs(); len(ids) != 1 || ids[0] != "act-1" {
		t.Fatalf("ids: %v", ids)
	}
}

// drainAndSeal completes a drain of the harness and returns the seal ack.
func drainAndSeal(t *testing.T, h *harness, mark byte) delivery.SealAck {
	t.Helper()
	h.enterDrain(t)
	ack, err := h.ctrl.Seal(context.Background(), h.engine, h.fence(delivery.SessionNormal, 1),
		proto.ID16{byte(h.slot.Family), mark})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if mode := h.prod.Mode(); mode != delivery.ModeDrained {
		t.Fatalf("mode is %s, want DRAINED", mode)
	}
	return ack
}

// lostAckWindow drives the crash window of AC-DLV-11-03: the producer
// saves the resume intent, the gateway accepts exactly the next admission
// generation and answers durably, and the answer is lost before the
// producer's LIVE commit.
func lostAckWindow(t *testing.T, h *harness, seal delivery.SealAck, binding proto.Hash32, resumeID proto.ID16) delivery.ResumeAck {
	t.Helper()
	intent := delivery.ResumeIntent{ResumeID: resumeID, OldSeal: seal,
		BindingDigest: binding, NextAdmissionGeneration: seal.AdmissionGeneration + 1}
	if err := h.prod.SaveResumeIntent(intent); err != nil {
		t.Fatalf("SaveResumeIntent: %v", err)
	}
	ack, err := h.gw.Resume(h.fence(delivery.SessionNormal, 1), intent)
	if err != nil {
		t.Fatalf("gateway Resume: %v", err)
	}
	// The crash: the durable gateway receipt stands, the producer has not
	// committed LIVE.
	if mode := h.prod.Mode(); mode != delivery.ModeDrained {
		t.Fatalf("mode inside the crash window is %s", mode)
	}
	return ack
}

// TestLostResumeAckRecoversSameIntent is AC-DLV-11-03 in both binding
// variants: with the active binding unchanged, and after a completed
// switch. The exact MAINTENANCE intent returns the saved ack, the
// generation is never incremented twice, the active binding stays the
// variant's binding, and capture stays closed until the LIVE commit.
func TestLostResumeAckRecoversSameIntent(t *testing.T) {
	for _, slot := range Slots() {
		for _, switched := range []bool{false, true} {
			name := slot.String() + "/same-binding"
			if switched {
				name = slot.String() + "/switched"
			}
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, slot)
				seal := drainAndSeal(t, h, 0x91)

				active := h.oldBind
				if switched {
					// A completed cutover before the resume: the desired
					// binding verifies, the producer commits it and the
					// gateway activates it.
					desired := delivery.BindingView{DestinationID: proto.ID16{byte(h.slot.Family), 9},
						Generation: 2, Stream: h.stream + "_NEXT", DescriptorDigest: h.newBind}
					switchID := proto.ID16{byte(h.slot.Family), 0x92}
					if _, err := h.engine.PrepareSwitch(context.Background(),
						h.fence(delivery.SessionNormal, 1), desired, switchID); err != nil {
						t.Fatalf("PrepareSwitch: %v", err)
					}
					if err := h.engine.ProducerCommitBinding(desired); err != nil {
						t.Fatalf("ProducerCommitBinding: %v", err)
					}
					if _, err := h.engine.CommitSwitch(context.Background(),
						h.fence(delivery.SessionNormal, 1), switchID); err != nil {
						t.Fatalf("CommitSwitch: %v", err)
					}
					active = h.newBind
				}

				resumeID := proto.ID16{byte(h.slot.Family), 0x93}
				acked := lostAckWindow(t, h, seal, active, resumeID)

				// Capture is closed while the store is DRAINED.
				if err := h.ctrl.Admit(LiveCallback{Kind: CallbackNewBlock,
					Sequence: h.prod.RetiredPrefix() + 1}); !isAdmissionClosed(err) {
					t.Fatalf("capture opened before the LIVE commit: %v", err)
				}

				// The MAINTENANCE session finishes the exact saved intent.
				maint := h.fence(delivery.SessionMaintenance, seal.AdmissionGeneration)
				maint.BindingDigest = h.oldBind
				if err := h.gw.OpenSession(maint); err != nil {
					t.Fatalf("OpenSession: %v", err)
				}
				if _, err := h.ctrl.FinishResume(h.engine, h.fence(delivery.SessionNormal, 1), resumeID); !errors.Is(err, ErrFence) {
					t.Fatalf("a normal session finished the resume: %v", err)
				}
				recovered, err := h.ctrl.FinishResume(h.engine, maint, resumeID)
				if err != nil {
					t.Fatalf("FinishResume: %v", err)
				}
				if !recovered.SameResume(acked) {
					t.Fatalf("the recovered ack is %+v, want the saved %+v", recovered, acked)
				}
				if recovered.NewAdmissionGeneration != seal.AdmissionGeneration+1 {
					t.Fatalf("the admission generation moved to %d", recovered.NewAdmissionGeneration)
				}
				if recovered.BindingDigest != active {
					t.Fatalf("the recovered binding is %v, want the active one", recovered.BindingDigest)
				}
				if mode := h.prod.Mode(); mode != delivery.ModeLive {
					t.Fatalf("mode is %s after the LIVE commit", mode)
				}
				// The gateway's receipt is unchanged: a repeated recover
				// of the same intent answers the same generation.
				saved, ok := h.gw.ResumeState()
				if !ok || !saved.SameResume(acked) {
					t.Fatalf("the durable resume receipt changed: %+v", saved)
				}
				if ack, err := h.gw.Resume(maint, delivery.ResumeIntent{ResumeID: resumeID,
					OldSeal: seal, BindingDigest: active,
					NextAdmissionGeneration: seal.AdmissionGeneration + 1}); err != nil || !ack.SameResume(acked) {
					t.Fatalf("the repeated resume intent did not return the same receipt: %v %+v", err, ack)
				}
				// Capture opens exactly at the LIVE commit.
				if err := h.ctrl.Admit(LiveCallback{Kind: CallbackNewBlock,
					Sequence: h.prod.RetiredPrefix() + 1}); err != nil {
					t.Fatalf("capture stayed closed after the LIVE commit: %v", err)
				}
			})
		}
	}
}

// TestCutoverLifecycleEventKeepsOldReference is AC-DLV-12-01 (D59): an
// Applied event ACKed in the old destination, a completed cutover, and a
// new Removed of the same saved activation — the new event goes only to
// the new binding carrying the old reference, and the ACKed Applied
// history is not copied.
func TestCutoverLifecycleEventKeepsOldReference(t *testing.T) {
	for _, slot := range Slots() {
		t.Run(slot.String(), func(t *testing.T) {
			h := newHarness(t, slot)
			appliedBatch := h.prod.RetiredPrefix()
			lifecycles := NewLifecycles()
			activation := Activation{ID: "act-cutover",
				Applied: Reference{Binding: h.oldBind, Batch: appliedBatch}}
			if err := lifecycles.Retain(activation); err != nil {
				t.Fatalf("Retain: %v", err)
			}

			seal := drainAndSeal(t, h, 0xa1)
			// The completed cutover and the resume that reopens capture.
			desired := delivery.BindingView{DestinationID: proto.ID16{byte(h.slot.Family), 9},
				Generation: 2, Stream: h.stream + "_NEXT", DescriptorDigest: h.newBind}
			switchID := proto.ID16{byte(h.slot.Family), 0xa2}
			if _, err := h.engine.PrepareSwitch(context.Background(),
				h.fence(delivery.SessionNormal, 1), desired, switchID); err != nil {
				t.Fatalf("PrepareSwitch: %v", err)
			}
			if err := h.engine.ProducerCommitBinding(desired); err != nil {
				t.Fatalf("ProducerCommitBinding: %v", err)
			}
			if _, err := h.engine.CommitSwitch(context.Background(),
				h.fence(delivery.SessionNormal, 1), switchID); err != nil {
				t.Fatalf("CommitSwitch: %v", err)
			}
			resumeID := proto.ID16{byte(h.slot.Family), 0xa3}
			// The resume of the switched variant carries the new active
			// binding; the intent, the gateway answer and the LIVE commit
			// run without a crash here.
			resumeIntent := delivery.ResumeIntent{ResumeID: resumeID, OldSeal: seal,
				BindingDigest: h.newBind, NextAdmissionGeneration: seal.AdmissionGeneration + 1}
			if err := h.prod.SaveResumeIntent(resumeIntent); err != nil {
				t.Fatalf("SaveResumeIntent: %v", err)
			}
			resumeAck, err := h.gw.Resume(h.fence(delivery.SessionNormal, 1), resumeIntent)
			if err != nil {
				t.Fatalf("gateway Resume: %v", err)
			}
			if err := h.prod.CommitLive(resumeAck); err != nil {
				t.Fatalf("CommitLive: %v", err)
			}
			if digest, _, ok := h.prod.CommittedBinding(); !ok || digest != h.newBind {
				t.Fatalf("the producer's committed binding is not the new one")
			}

			// The new Removed goes only to the new binding, with the old
			// reference traveling unchanged.
			plan, err := lifecycles.PlanEvent(h.newBind, activation.ID, EventRemoved, appliedBatch+1)
			if err != nil {
				t.Fatalf("PlanEvent: %v", err)
			}
			if plan.Target != h.newBind {
				t.Fatalf("the new event targets %v", plan.Target)
			}
			if plan.Prior != activation.Applied {
				t.Fatalf("the retained reference changed: %+v", plan.Prior)
			}
			// The delivery of the new event runs in the new admission
			// generation and the new binding.
			next := h.dataFence(h.newBind, seal.AdmissionGeneration+1)
			if err := h.gw.OpenSession(next); err != nil {
				t.Fatalf("OpenSession: %v", err)
			}
			seq := h.runBatch(t, next)
			if seq != appliedBatch+1 {
				t.Fatalf("the new event's batch is %d, want %d", seq, appliedBatch+1)
			}
			if h.delivered[h.oldBind] != 1 || h.delivered[h.newBind] != 1 {
				t.Fatalf("delivery counts moved: old=%d new=%d", h.delivered[h.oldBind], h.delivered[h.newBind])
			}
			saved, ok := lifecycles.Get(activation.ID)
			if !ok || saved != activation {
				t.Fatalf("the ACKed Applied history was rewritten: %+v", saved)
			}
		})
	}
}

// TestModeBarrierIsRaceFree runs the barrier decisions concurrently
// against one draining store, so the refusals stay consistent under
// concurrent native callbacks, polls and control reads.
func TestModeBarrierIsRaceFree(t *testing.T) {
	for _, slot := range Slots() {
		t.Run(slot.String(), func(t *testing.T) {
			h := newHarness(t, slot)
			if err := h.frontier.Save(Obligation{Sequence: 1, Stage: ObligAdmitted}); err != nil {
				t.Fatalf("Save: %v", err)
			}
			h.enterDrain(t)
			var wg sync.WaitGroup
			for worker := 0; worker < 4; worker++ {
				wg.Add(1)
				go func(worker int) {
					defer wg.Done()
					for i := 0; i < 50; i++ {
						_ = h.ctrl.Admit(LiveCallback{Kind: CallbackNewBlock, Sequence: uint64(i)})
						_ = h.ctrl.Poll(PollTick{Kind: PollGC})
						_ = h.ctrl.Remaining()
						_ = h.ctrl.Snapshot()
					}
				}(worker)
			}
			wg.Wait()
			if remaining := h.ctrl.Remaining(); len(remaining) != 1 || remaining[0].Sequence != 1 {
				t.Fatalf("concurrent barrier reads changed the frontier: %+v", remaining)
			}
		})
	}
}
