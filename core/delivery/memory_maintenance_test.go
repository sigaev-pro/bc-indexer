package delivery

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// TestProducerMaintenanceStateRules locks the producer-side maintenance
// seams of the memory store: drain point guards, seal ack binding, switch
// intent idempotency and conflict, binding commit mode and the live
// commit's resume binding.
func TestProducerMaintenanceStateRules(t *testing.T) {
	p := newTestProducer(t)

	// The drain point needs DRAIN mode and the retired highwater.
	drain := proto.ID16{9}
	if err := p.SetDrainPoint(drain, 0); !errors.Is(err, ErrState) {
		t.Fatalf("drain point from LIVE: %v", err)
	}
	if err := p.SetMode(ModeDrain); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if _, _, ok := p.DrainPoint(); ok {
		t.Fatalf("drain point appeared before it was set")
	}
	if err := p.SetDrainPoint(drain, 0); err != nil {
		t.Fatalf("SetDrainPoint: %v", err)
		if got, _, _ := p.DrainPoint(); got != drain {
			t.Fatalf("drain id %v", got)
		}
	}

	// The seal ack binds the drain point; a different drain or highwater
	// conflicts; the same ack is idempotent and sets DRAINED.
	ack := SealAck{DrainID: drain, Highwater: 0, AdmissionGeneration: 1,
		BindingDigest: testBinding, IssuerStoreEpoch: testEpoch}
	if err := p.SaveSealAck(ack); err != nil {
		t.Fatalf("SaveSealAck: %v", err)
	}
	if p.Mode() != ModeDrained {
		t.Fatalf("mode after seal ack: %s", p.Mode())
	}
	if err := p.SaveSealAck(ack); err != nil {
		t.Fatalf("idempotent seal ack: %v", err)
	}
	other := ack
	other.Highwater = 5
	if err := p.SaveSealAck(other); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("conflicting seal ack: %v", err)
	}
	if _, ok := p.SealAckSaved(); !ok {
		t.Fatalf("the seal ack is not saved")
	}

	// The switch intent is idempotent for the same identity and conflicts
	// for a jumping one.
	switchID := proto.ID16{5}
	if err := p.SaveSwitchIntent(switchID, testBinding); err != nil {
		t.Fatalf("SaveSwitchIntent: %v", err)
	}
	if err := p.SaveSwitchIntent(switchID, testBinding); err != nil {
		t.Fatalf("idempotent switch intent: %v", err)
	}
	if err := p.SaveSwitchIntent(proto.ID16{6}, testBinding); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("jumping switch intent: %v", err)
	}
	gotID, gotDigest, ok := p.SwitchIntentSaved()
	if !ok || gotID != switchID || gotDigest != testBinding {
		t.Fatalf("switch intent %v/%v ok=%v", gotID, gotDigest, ok)
	}

	// The binding commit needs DRAINED; it never rolls back here.
	if err := p.CommitBinding(testBinding, 2); err != nil {
		t.Fatalf("CommitBinding: %v", err)
	}
	digest, generation, ok := p.CommittedBinding()
	if !ok || digest != testBinding || generation != 2 {
		t.Fatalf("committed binding %v/%d ok=%v", digest, generation, ok)
	}

	// The resume intent is idempotent and conflicts for a new one.
	resume := ResumeIntent{ResumeID: proto.ID16{7}, OldSeal: ack,
		BindingDigest: testBinding, NextAdmissionGeneration: 2}
	if err := p.SaveResumeIntent(resume); err != nil {
		t.Fatalf("SaveResumeIntent: %v", err)
	}
	if err := p.SaveResumeIntent(resume); err != nil {
		t.Fatalf("idempotent resume intent: %v", err)
	}
	jump := resume
	jump.ResumeID = proto.ID16{8}
	if err := p.SaveResumeIntent(jump); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("jumping resume intent: %v", err)
	}
	saved, ok := p.ResumeIntentSaved()
	if !ok || saved.ResumeID != resume.ResumeID {
		t.Fatalf("resume intent %+v ok=%v", saved, ok)
	}

	// The live commit binds the saved resume; a foreign ack is refused.
	if err := p.CommitLive(ResumeAck{ResumeID: proto.ID16{99}}); !errors.Is(err, ErrState) {
		t.Fatalf("live commit of a foreign resume: %v", err)
	}
	live := ResumeAck{ResumeID: resume.ResumeID, BindingDigest: testBinding,
		OldAdmissionGeneration: 1, NewAdmissionGeneration: 2,
		DrainID: drain, IssuerStoreEpoch: testEpoch}
	if err := p.CommitLive(live); err != nil {
		t.Fatalf("CommitLive: %v", err)
	}
	if p.Mode() != ModeLive {
		t.Fatalf("mode after live commit: %s", p.Mode())
	}
	if got, ok := p.ResumeAckSaved(); !ok || !got.SameResume(live) {
		t.Fatalf("resume ack %+v ok=%v", got, ok)
	}

	// An invalid resume intent is rejected.
	if err := p.SaveResumeIntent(ResumeIntent{}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("empty resume intent: %v", err)
	}
}

// TestGatewaySwitchAndResumeSeams locks the gateway-side maintenance
// seams: await-switch drift, generation rules and resume binding checks.
func TestGatewaySwitchAndResumeSeams(t *testing.T) {
	g := newTestGateway(t)
	fence := testFence(1)
	if err := g.SetActiveBinding(1, testBinding); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	newDigest := proto.Hash32{7}

	// AWAIT_SWITCH: drift of the desired digest conflicts; the same
	// digest is idempotent.
	if err := g.EnterAwaitSwitch(newDigest); err != nil {
		t.Fatalf("EnterAwaitSwitch: %v", err)
	}
	if err := g.EnterAwaitSwitch(newDigest); err != nil {
		t.Fatalf("idempotent await: %v", err)
	}
	if err := g.EnterAwaitSwitch(proto.Hash32{8}); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("desired drift: %v", err)
	}
	if phase, _ := g.SwitchState(); phase != SwitchAwait {
		t.Fatalf("phase %s", phase)
	}
	// Preparing without a seal is refused (DRAIN_REQUIRED).
	view := BindingView{DestinationID: proto.ID16{7}, Generation: 2,
		Network:         proto.NetworkIdentity{Family: proto.NetworkFamilyEVM},
		ExpectedAccount: "A", Stream: "S2", SubjectPrefix: "evm.v3.bsc",
		DescriptorDigest: newDigest}
	if _, err := g.PrepareSwitch(fence, view, proto.ID16{5}); !errors.Is(err, ErrState) {
		t.Fatalf("prepare without seal: %v", err)
	}

	// Seal through the flow so the switch may prepare.
	plan := SealedPlan{Identity: testIdentity(1), Stream: "s", FrameKeys: []FrameKey{
		{Ordinal: 0, Fragment: 0, Complete: true},
	}}
	if err := g.ImportClosed(fence, plan, false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	if err := g.RecordPubAck(fence, plan.Identity.Batch, plan.Identity.PlanDigest, plan.FrameKeys[0]); err != nil {
		t.Fatalf("RecordPubAck: %v", err)
	}
	if _, err := g.MarkNATSACKed(fence, plan.Identity); err != nil {
		t.Fatalf("MarkNATSACKed: %v", err)
	}
	if err := g.BeginRelease(fence, plan.Identity.Batch); err != nil {
		t.Fatalf("BeginRelease: %v", err)
	}
	if _, err := g.CleanupStep(fence, plan.Identity.Batch); err != nil {
		t.Fatalf("CleanupStep: %v", err)
	}
	if _, err := g.FinishRelease(fence, plan.Identity.Batch); err != nil {
		t.Fatalf("FinishRelease: %v", err)
	}
	intent := SealIntent{BindingGeneration: 1, AdmissionGeneration: 1,
		DrainID: proto.ID16{9}, Highwater: 1, BindingDigest: testBinding}
	seal, err := g.SealGeneration(fence, intent)
	if err != nil {
		t.Fatalf("SealGeneration: %v", err)
	}
	if got, ok := g.SealedGeneration(); !ok || !got.SameSeal(seal) {
		t.Fatalf("sealed generation %+v ok=%v", got, ok)
	}

	// The desired generation must be exactly the next one.
	wrongGen := view
	wrongGen.Generation = 3
	if _, err := g.PrepareSwitch(fence, wrongGen, proto.ID16{5}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("generation jump: %v", err)
	}
	sameAsActive := view
	sameAsActive.DescriptorDigest = testBinding
	if _, err := g.PrepareSwitch(fence, sameAsActive, proto.ID16{5}); !errors.Is(err, ErrState) {
		t.Fatalf("desired equals active: %v", err)
	}
	prepared, err := g.PrepareSwitch(fence, view, proto.ID16{5})
	if err != nil {
		t.Fatalf("PrepareSwitch: %v", err)
	}
	if prepared.OldDigest != testBinding || prepared.NewDigest != newDigest {
		t.Fatalf("prepared %+v", prepared)
	}
	// A second switch id conflicts while one is prepared.
	if _, err := g.PrepareSwitch(fence, view, proto.ID16{6}); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("second switch: %v", err)
	}
	// Commit of an unknown id is refused; the right id commits and the
	// retry returns the receipt.
	if _, err := g.CommitSwitch(fence, proto.ID16{6}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown switch commit: %v", err)
	}
	committed, err := g.CommitSwitch(fence, proto.ID16{5})
	if err != nil {
		t.Fatalf("CommitSwitch: %v", err)
	}
	if !committed.Prepared.SameSwitch(prepared) {
		t.Fatalf("committed receipt %+v", committed)
	}
	retry, err := g.CommitSwitch(fence, proto.ID16{5})
	if err != nil || !retry.Prepared.SameSwitch(prepared) {
		t.Fatalf("committed retry: %+v err=%v", retry, err)
	}
	if gen, digest := g.ActiveBinding(); gen != 2 || digest != newDigest {
		t.Fatalf("active binding %d/%v after commit", gen, digest)
	}

	// Resume: without a seal there is nothing to continue, and the next
	// generation must be exactly one above the sealed one.
	resume := ResumeIntent{ResumeID: proto.ID16{7}, OldSeal: seal,
		BindingDigest: newDigest, NextAdmissionGeneration: seal.AdmissionGeneration + 1}
	if _, err := g.Resume(fence, resume); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got, ok := g.ResumeState(); !ok || got.ResumeID != resume.ResumeID {
		t.Fatalf("resume state %+v ok=%v", got, ok)
	}
	jump := resume
	jump.ResumeID = proto.ID16{8}
	if _, err := g.Resume(fence, jump); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("second resume: %v", err)
	}
	wrongBinding := resume
	wrongBinding.ResumeID = proto.ID16{10}
	wrongBinding.BindingDigest = testBinding
	// The binding digest now differs from the active one; the conflict is
	// reported by the already accepted resume first.
	if _, err := g.Resume(fence, wrongBinding); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("resume of another binding: %v", err)
	}
	// An empty resume intent is invalid.
	if _, err := g.Resume(fence, ResumeIntent{}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("empty resume: %v", err)
	}
	// EnterAwaitSwitch of the now-active digest is refused.
	if err := g.EnterAwaitSwitch(newDigest); !errors.Is(err, ErrState) {
		t.Fatalf("await of the active binding: %v", err)
	}
	// A commit without a prepared switch is refused.
	g2 := newTestGateway(t)
	if err := g2.SetActiveBinding(1, testBinding); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	if _, err := g2.CommitSwitch(fence, proto.ID16{5}); !errors.Is(err, ErrState) {
		t.Fatalf("commit without prepare: %v", err)
	}
	if err := g2.SetActiveBinding(0, testBinding); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("zero binding generation accepted")
	}
}
