package maintenance

import (
	"context"
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

var (
	mProducer = proto.ID16{1}
	mRun      = proto.ID16{2}
	mEpoch    = proto.ID16{3}
	mOldBind  = proto.Hash32{0x0a}
	mNewBind  = proto.Hash32{0x0b}
)

func mFence(kind delivery.SessionKind, admission uint64) delivery.Fence {
	return delivery.Fence{SessionToken: []byte("t"), Producer: mProducer, Run: mRun,
		ConnectionGeneration: 1, AdmissionGeneration: admission,
		BindingDigest: mOldBind, Kind: kind}
}

// countingVerifier counts the D77 checks and fails on demand.
type countingVerifier struct {
	calls  int
	failOn map[int]error
}

func (v *countingVerifier) VerifyTarget(_ context.Context, _ proto.Hash32) error {
	v.calls++
	if err, ok := v.failOn[v.calls]; ok {
		return err
	}
	return nil
}

func mStores(t *testing.T) (*delivery.MemoryProducer, *delivery.MemoryGateway) {
	t.Helper()
	prod, err := delivery.NewMemoryProducer(mProducer, mRun)
	if err != nil {
		t.Fatalf("NewMemoryProducer: %v", err)
	}
	gw, err := delivery.NewMemoryGateway(mEpoch)
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := gw.SetActiveBinding(1, mOldBind); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	if err := gw.OpenSession(mFence(delivery.SessionNormal, 1)); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	return prod, gw
}

func mEngine(t *testing.T, prod *delivery.MemoryProducer, gw *delivery.MemoryGateway, v TargetVerifier) *Engine {
	t.Helper()
	e, err := New(prod, gw, v)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestSealToDrainedAndIdempotentRepeat(t *testing.T) {
	prod, gw := mStores(t)
	verifier := &countingVerifier{}
	engine := mEngine(t, prod, gw, verifier)
	drain := proto.ID16{9}

	// LIVE refuses: the seal needs DRAIN.
	if _, err := engine.SealToDrained(context.Background(), mFence(delivery.SessionNormal, 1), drain); !errors.Is(err, delivery.ErrState) {
		t.Fatalf("seal from LIVE: %v, want ErrState", err)
	}
	if err := prod.SetMode(delivery.ModeDrain); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	// An open batch refuses: the frontier must be exhausted.
	seq, _ := prod.AllocateIntent()
	if _, err := engine.SealToDrained(context.Background(), mFence(delivery.SessionNormal, 1), drain); !errors.Is(err, delivery.ErrState) {
		t.Fatalf("seal with open batch: %v, want ErrState", err)
	}
	// Retire the batch through the ordinary flow surface.
	identity := mIdentity(seq)
	if err := prod.CommitReady(seq, identity); err != nil {
		t.Fatalf("CommitReady: %v", err)
	}
	if err := prod.CommitSealed(seq, delivery.SealedPlan{Identity: identity, Stream: "S",
		FrameKeys: []delivery.FrameKey{{Ordinal: 0, Fragment: 0, Complete: true}}}); err != nil {
		t.Fatalf("CommitSealed: %v", err)
	}
	if err := prod.SaveAckOutbox(seq, delivery.IngestAck{Identity: identity}); err != nil {
		t.Fatalf("SaveAckOutbox: %v", err)
	}
	for {
		remains, err := prod.CleanupPayload(seq)
		if err != nil {
			t.Fatalf("CleanupPayload: %v", err)
		}
		if !remains {
			break
		}
	}
	if err := prod.Retire(seq, delivery.IngestAck{Identity: identity}); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	// Finish the gateway side too: the seal compares the retired prefix
	// of the run with the highwater.
	fence := mFence(delivery.SessionNormal, 1)
	keys := []delivery.FrameKey{{Ordinal: 0, Fragment: 0, Complete: true}}
	if err := gw.ImportClosed(fence, delivery.SealedPlan{Identity: identity, Stream: "S",
		FrameKeys: keys}, false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	for _, k := range keys {
		if err := gw.RecordPubAck(fence, identity.Batch, identity.PlanDigest, k); err != nil {
			t.Fatalf("RecordPubAck: %v", err)
		}
	}
	if _, err := gw.MarkNATSACKed(fence, identity); err != nil {
		t.Fatalf("MarkNATSACKed: %v", err)
	}
	if err := gw.BeginRelease(fence, identity.Batch); err != nil {
		t.Fatalf("BeginRelease: %v", err)
	}
	for {
		more, err := gw.CleanupStep(fence, identity.Batch)
		if err != nil {
			t.Fatalf("CleanupStep: %v", err)
		}
		if !more {
			break
		}
	}
	if _, err := gw.FinishRelease(fence, identity.Batch); err != nil {
		t.Fatalf("FinishRelease: %v", err)
	}
	ack, err := engine.SealToDrained(context.Background(), mFence(delivery.SessionNormal, 1), drain)
	if err != nil {
		t.Fatalf("SealToDrained: %v", err)
	}
	if ack.DrainID != drain || ack.Highwater != 1 || ack.AdmissionGeneration != 1 {
		t.Fatalf("seal ack %+v", ack)
	}
	if prod.Mode() != delivery.ModeDrained {
		t.Fatalf("mode after seal: %s", prod.Mode())
	}
	// The repeated seal returns the same ack.
	again, err := engine.SealToDrained(context.Background(), mFence(delivery.SessionNormal, 1), drain)
	if err != nil || !again.SameSeal(ack) {
		t.Fatalf("repeated seal: %+v err=%v", again, err)
	}
	// A different drain id on a sealed generation is a conflict.
	other, err := engine.SealToDrained(context.Background(), mFence(delivery.SessionNormal, 1), proto.ID16{8})
	if err == nil || other.SameSeal(ack) {
		t.Fatalf("second seal of the same generation accepted")
	}
	// SEALED_GENERATION forbids Prepare of the sealed admission
	// generation: a late import of the old generation is rejected.
	plan := delivery.SealedPlan{Identity: mIdentity(2), Stream: "S",
		FrameKeys: []delivery.FrameKey{{Ordinal: 0, Fragment: 0, Complete: true}}}
	if err := gw.ImportClosed(mFence(delivery.SessionNormal, 1), plan, false); !errors.Is(err, delivery.ErrState) {
		t.Fatalf("Prepare of the sealed generation: %v, want ErrState", err)
	}
}

func TestSealRefusesUnfinishedHighwater(t *testing.T) {
	prod, gw := mStores(t)
	engine := mEngine(t, prod, gw, &countingVerifier{})
	if err := prod.SetMode(delivery.ModeDrain); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	// A batch at NATS_ACKED on the gateway but not released: the gateway
	// retired prefix (0) differs from any claimed highwater and the open
	// batch blocks the seal.
	seq, _ := prod.AllocateIntent()
	identity := mIdentity(seq)
	if err := prod.CommitReady(seq, identity); err != nil {
		t.Fatalf("CommitReady: %v", err)
	}
	if err := prod.CommitSealed(seq, delivery.SealedPlan{Identity: identity, Stream: "S",
		FrameKeys: []delivery.FrameKey{{Ordinal: 0, Fragment: 0, Complete: true}}}); err != nil {
		t.Fatalf("CommitSealed: %v", err)
	}
	if _, err := engine.SealToDrained(context.Background(), mFence(delivery.SessionNormal, 1), proto.ID16{9}); !errors.Is(err, delivery.ErrState) {
		t.Fatalf("seal over an unreleased batch: %v, want ErrState", err)
	}
}

func TestSwitchFlowWithD77Checks(t *testing.T) {
	prod, gw := mStores(t)
	verifier := &countingVerifier{}
	engine := mEngine(t, prod, gw, verifier)
	mSealAndDrain(t, prod, gw, engine)

	desired := delivery.BindingView{DestinationID: proto.ID16{7}, Generation: 2,
		Network:         proto.NetworkIdentity{Family: proto.NetworkFamilyEVM},
		ExpectedAccount: "IDX", Stream: "EXTRACTOR_V3", SubjectPrefix: "evm.v3.bsc",
		DescriptorDigest: mNewBind}
	switchID := proto.ID16{5}

	// The gateway opens in AWAIT_SWITCH: old binding stays active.
	if err := engine.EnterAwaitSwitch(mNewBind); err != nil {
		t.Fatalf("EnterAwaitSwitch: %v", err)
	}
	if gen, digest := gw.ActiveBinding(); gen != 1 || digest != mOldBind {
		t.Fatalf("await-switch changed the active binding to %d/%v", gen, digest)
	}
	// The D77 check gates SwitchPrepared: a failing check leaves no
	// prepared receipt and the old binding active.
	verifier.failOn = map[int]error{1: errors.New("metadata mismatch")}
	if _, err := engine.PrepareSwitch(context.Background(), mFence(delivery.SessionNormal, 1), desired, switchID); err == nil {
		t.Fatalf("PrepareSwitch with a failing target check succeeded")
	}
	if phase, _ := gw.SwitchState(); phase != delivery.SwitchAwait {
		t.Fatalf("a failed check left phase %s", phase)
	}
	// The check passes: SwitchPrepared binds the whole identity.
	prepared, err := engine.PrepareSwitch(context.Background(), mFence(delivery.SessionNormal, 1), desired, switchID)
	if err != nil {
		t.Fatalf("PrepareSwitch: %v", err)
	}
	if prepared.SwitchID != switchID || prepared.OldDigest != mOldBind || prepared.NewDigest != mNewBind {
		t.Fatalf("prepared receipt %+v", prepared)
	}
	if gen, digest := gw.ActiveBinding(); gen != 1 || digest != mOldBind {
		t.Fatalf("prepare activated the new binding early")
	}
	// The producer commits its binding; the commit never rolls back.
	if err := engine.ProducerCommitBinding(desired); err != nil {
		t.Fatalf("ProducerCommitBinding: %v", err)
	}
	// The first CommitSwitch re-verifies the target.
	if _, err := engine.CommitSwitch(context.Background(), mFence(delivery.SessionNormal, 1), switchID); err != nil {
		t.Fatalf("CommitSwitch: %v", err)
	}
	if gen, digest := gw.ActiveBinding(); gen != 2 || digest != mNewBind {
		t.Fatalf("commit left the active binding at %d/%v", gen, digest)
	}
	callsAfterCommit := verifier.calls
	// The retry of the committed switch returns the durable receipt
	// without a new target check — an outage never cancels it.
	verifier.failOn = map[int]error{callsAfterCommit + 1: errors.New("nats down")}
	retry, err := engine.CommitSwitch(context.Background(), mFence(delivery.SessionNormal, 1), switchID)
	if err != nil {
		t.Fatalf("committed switch retry: %v", err)
	}
	if !retry.Prepared.SameSwitch(prepared) {
		t.Fatalf("committed retry receipt differs")
	}
	if verifier.calls != callsAfterCommit {
		t.Fatalf("committed retry contacted the target again (%d calls)", verifier.calls-callsAfterCommit)
	}
}

func TestCommitSwitchFailedRecheckKeepsState(t *testing.T) {
	prod, gw := mStores(t)
	verifier := &countingVerifier{}
	engine := mEngine(t, prod, gw, verifier)
	mSealAndDrain(t, prod, gw, engine)
	desired := delivery.BindingView{DestinationID: proto.ID16{7}, Generation: 2,
		DescriptorDigest: mNewBind, ExpectedAccount: "IDX", Stream: "S2", SubjectPrefix: "evm.v3.bsc",
		Network: proto.NetworkIdentity{Family: proto.NetworkFamilyEVM}}
	switchID := proto.ID16{5}
	if err := engine.EnterAwaitSwitch(mNewBind); err != nil {
		t.Fatalf("EnterAwaitSwitch: %v", err)
	}
	if _, err := engine.PrepareSwitch(context.Background(), mFence(delivery.SessionNormal, 1), desired, switchID); err != nil {
		t.Fatalf("PrepareSwitch: %v", err)
	}
	if err := engine.ProducerCommitBinding(desired); err != nil {
		t.Fatalf("ProducerCommitBinding: %v", err)
	}
	// The pre-commit re-check fails: the producer stays on the new
	// binding (no automatic rollback), the gateway stays PREPARED, and
	// nothing published anywhere.
	verifier.failOn = map[int]error{2: errors.New("metadata changed")}
	if _, err := engine.CommitSwitch(context.Background(), mFence(delivery.SessionNormal, 1), switchID); err == nil {
		t.Fatalf("CommitSwitch with a failed re-check succeeded")
	}
	if phase, _ := gw.SwitchState(); phase != delivery.SwitchPreparedPhase {
		t.Fatalf("failed re-check moved the phase to %s", phase)
	}
	if digest, gen, ok := prod.CommittedBinding(); !ok || digest != mNewBind || gen != 2 {
		t.Fatalf("the producer binding commit was rolled back: %v/%d/%v", digest, gen, ok)
	}
	// After the fix the commit succeeds and the receipt is durable.
	verifier.failOn = nil
	if _, err := engine.CommitSwitch(context.Background(), mFence(delivery.SessionNormal, 1), switchID); err != nil {
		t.Fatalf("CommitSwitch after the fix: %v", err)
	}
}

func TestResumeOpensNextAdmissionGeneration(t *testing.T) {
	prod, gw := mStores(t)
	engine := mEngine(t, prod, gw, &countingVerifier{})
	mSealAndDrain(t, prod, gw, engine)
	resumeID := proto.ID16{6}

	// Same binding: the switch is skipped, the resume is mandatory.
	ack, err := engine.Resume(mFence(delivery.SessionNormal, 1), resumeID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if ack.OldAdmissionGeneration != 1 || ack.NewAdmissionGeneration != 2 {
		t.Fatalf("resume ack %+v", ack)
	}
	if prod.Mode() != delivery.ModeLive {
		t.Fatalf("mode after resume: %s", prod.Mode())
	}
	// The repeated intent returns the same result.
	again, err := engine.Resume(mFence(delivery.SessionNormal, 1), resumeID)
	if err != nil || !again.SameResume(ack) {
		t.Fatalf("repeated resume: %+v err=%v", again, err)
	}
	// A late RPC of the old admission generation creates no state.
	oldFence := mFence(delivery.SessionNormal, 1)
	oldFence.AdmissionGeneration = 1
	identity := mIdentity(9)
	if err := gw.OpenSession(mFence(delivery.SessionNormal, 2)); err != nil {
		t.Fatalf("OpenSession next generation: %v", err)
	}
	if err := gw.ImportClosed(oldFence, delivery.SealedPlan{Identity: identity, Stream: "S"}, false); !errors.Is(err, delivery.ErrFenced) {
		t.Fatalf("late RPC of the old admission generation: %v, want ErrFenced", err)
	}
}

func TestMaintenanceRecoveryFromQuiescedProducer(t *testing.T) {
	prod, gw := mStores(t)
	verifier := &countingVerifier{}
	engine := mEngine(t, prod, gw, verifier)
	mSealAndDrain(t, prod, gw, engine)

	// Drive a switch whose CommitSwitch answer is lost after the commit.
	desired := delivery.BindingView{DestinationID: proto.ID16{7}, Generation: 2,
		DescriptorDigest: mNewBind, ExpectedAccount: "IDX", Stream: "S2", SubjectPrefix: "evm.v3.bsc",
		Network: proto.NetworkIdentity{Family: proto.NetworkFamilyEVM}}
	switchID := proto.ID16{5}
	if err := engine.EnterAwaitSwitch(mNewBind); err != nil {
		t.Fatalf("EnterAwaitSwitch: %v", err)
	}
	if _, err := engine.PrepareSwitch(context.Background(), mFence(delivery.SessionNormal, 1), desired, switchID); err != nil {
		t.Fatalf("PrepareSwitch: %v", err)
	}
	if err := engine.ProducerCommitBinding(desired); err != nil {
		t.Fatalf("ProducerCommitBinding: %v", err)
	}
	if _, err := engine.CommitSwitch(context.Background(), mFence(delivery.SessionNormal, 1), switchID); err != nil {
		t.Fatalf("CommitSwitch: %v", err)
	}
	// The MAINTENANCE session recovers the committed receipt; a lost
	// response after the commit is answered by the receipt.
	maint := mFence(delivery.SessionMaintenance, 1)
	committed, _, err := engine.RecoverTransition(maint, switchID, proto.ID16{})
	if err != nil {
		t.Fatalf("RecoverTransition: %v", err)
	}
	if committed.Prepared.SwitchID != switchID {
		t.Fatalf("recovered receipt %+v", committed)
	}
	// The MAINTENANCE session never accepts data steps.
	if err := gw.ImportClosed(maint, delivery.SealedPlan{Identity: mIdentity(9), Stream: "S"}, false); !errors.Is(err, delivery.ErrFenced) {
		t.Fatalf("maintenance Prepare: %v, want ErrFenced", err)
	}
	// An unfinished prepared switch without the producer's committed
	// assertion is not finished by recovery.
	prod2, gw2 := mStores(t)
	engine2 := mEngine(t, prod2, gw2, &countingVerifier{})
	mSealAndDrain(t, prod2, gw2, engine2)
	if err := engine2.EnterAwaitSwitch(mNewBind); err != nil {
		t.Fatalf("EnterAwaitSwitch: %v", err)
	}
	if _, err := engine2.PrepareSwitch(context.Background(), mFence(delivery.SessionNormal, 1), desired, switchID); err != nil {
		t.Fatalf("PrepareSwitch: %v", err)
	}
	maint2 := mFence(delivery.SessionMaintenance, 1)
	if _, _, err := engine2.RecoverTransition(maint2, switchID, proto.ID16{}); err == nil {
		t.Fatalf("recovery committed a switch without the producer assertion")
	}
	// With the producer assertion, recovery finishes it.
	if err := engine2.ProducerCommitBinding(desired); err != nil {
		t.Fatalf("ProducerCommitBinding: %v", err)
	}
	if _, _, err := engine2.RecoverTransition(maint2, switchID, proto.ID16{}); err != nil {
		t.Fatalf("recovery with the producer assertion: %v", err)
	}
}

func TestResumeCrashBetweenAnswerAndCommit(t *testing.T) {
	prod, gw := mStores(t)
	engine := mEngine(t, prod, gw, &countingVerifier{})
	mSealAndDrain(t, prod, gw, engine)
	resumeID := proto.ID16{6}
	// Model the crash: the gateway answers, the producer never commits.
	seal, _ := prod.SealAckSaved()
	intent := delivery.ResumeIntent{ResumeID: resumeID, OldSeal: seal,
		BindingDigest: seal.BindingDigest, NextAdmissionGeneration: seal.AdmissionGeneration + 1}
	if err := prod.SaveResumeIntent(intent); err != nil {
		t.Fatalf("SaveResumeIntent: %v", err)
	}
	if _, err := gw.Resume(mFence(delivery.SessionNormal, 1), intent); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if prod.Mode() != delivery.ModeDrained {
		t.Fatalf("producer left DRAINED before its commit")
	}
	// The restart continues the same intent instead of allocating a new
	// generation.
	ack, err := engine.Resume(mFence(delivery.SessionNormal, 1), resumeID)
	if err != nil {
		t.Fatalf("Resume after crash: %v", err)
	}
	if ack.ResumeID != resumeID || prod.Mode() != delivery.ModeLive {
		t.Fatalf("resumed %+v mode %s", ack, prod.Mode())
	}
}

func TestEngineRejectsNilVerifier(t *testing.T) {
	prod, gw := mStores(t)
	if _, err := New(prod, gw, nil); err == nil {
		t.Fatalf("nil verifier accepted")
	}
	if _, err := New(nil, gw, &countingVerifier{}); err == nil {
		t.Fatalf("nil producer accepted")
	}
}

// mIdentity builds one batch identity of the maintenance fixtures.
func mIdentity(seq uint64) delivery.BatchIdentity {
	return delivery.BatchIdentity{
		Batch:                delivery.BatchID{Producer: mProducer, Run: mRun, Seq: seq},
		ManifestDigest:       proto.Hash32{byte(seq), 1},
		PlanDigest:           proto.Hash32{byte(seq), 2},
		LayoutManifestDigest: proto.Hash32{byte(seq), 3},
		BindingDigest:        mOldBind,
		IssuerStoreEpoch:     mEpoch,
	}
}

// mSealAndDrain drains one released batch and seals generation 1.
func mSealAndDrain(t *testing.T, prod *delivery.MemoryProducer, gw *delivery.MemoryGateway, engine *Engine) delivery.SealAck {
	t.Helper()
	if err := prod.SetMode(delivery.ModeDrain); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	seq, _ := prod.AllocateIntent()
	identity := mIdentity(seq)
	if err := prod.CommitReady(seq, identity); err != nil {
		t.Fatalf("CommitReady: %v", err)
	}
	if err := prod.CommitSealed(seq, delivery.SealedPlan{Identity: identity, Stream: "S",
		FrameKeys: []delivery.FrameKey{{Ordinal: 0, Fragment: 0, Complete: true}}}); err != nil {
		t.Fatalf("CommitSealed: %v", err)
	}
	if err := prod.SaveAckOutbox(seq, delivery.IngestAck{Identity: identity}); err != nil {
		t.Fatalf("SaveAckOutbox: %v", err)
	}
	for {
		remains, err := prod.CleanupPayload(seq)
		if err != nil {
			t.Fatalf("CleanupPayload: %v", err)
		}
		if !remains {
			break
		}
	}
	if err := prod.Retire(seq, delivery.IngestAck{Identity: identity}); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	// Close, acknowledge and release the batch on the gateway so its
	// retired prefix equals the highwater.
	fence := mFence(delivery.SessionNormal, 1)
	keys := []delivery.FrameKey{{Ordinal: 0, Fragment: 0, Complete: true}}
	if err := gw.ImportClosed(fence, delivery.SealedPlan{Identity: identity, Stream: "S",
		FrameKeys: keys}, false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	for _, k := range keys {
		if err := gw.RecordPubAck(fence, identity.Batch, identity.PlanDigest, k); err != nil {
			t.Fatalf("RecordPubAck: %v", err)
		}
	}
	if _, err := gw.MarkNATSACKed(fence, identity); err != nil {
		t.Fatalf("MarkNATSACKed: %v", err)
	}
	if err := gw.BeginRelease(fence, identity.Batch); err != nil {
		t.Fatalf("BeginRelease: %v", err)
	}
	for {
		more, err := gw.CleanupStep(fence, identity.Batch)
		if err != nil {
			t.Fatalf("CleanupStep: %v", err)
		}
		if !more {
			break
		}
	}
	if _, err := gw.FinishRelease(fence, identity.Batch); err != nil {
		t.Fatalf("FinishRelease: %v", err)
	}
	ack, err := engine.SealToDrained(context.Background(), fence, proto.ID16{9})
	if err != nil {
		t.Fatalf("SealToDrained: %v", err)
	}
	return ack
}
