package delivery

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// fenceAt is the test fence at one admission generation.
func fenceAt(admission uint64) Fence {
	f := testFence(1)
	f.AdmissionGeneration = admission
	return f
}

// importAt opens a session at one admission generation and imports one
// CLOSED batch through it: the single data exit of the reference engine.
func importAt(t *testing.T, g *MemoryGateway, admission, seq uint64) error {
	t.Helper()
	fence := fenceAt(admission)
	if err := g.OpenSession(fence); err != nil {
		t.Fatalf("OpenSession at generation %d: %v", admission, err)
	}
	return g.ImportClosed(fence, SealedPlan{Identity: testIdentity(seq), Stream: "s",
		FrameKeys: []FrameKey{{Ordinal: 0, Fragment: 0, Complete: true}}}, false)
}

// sealRunAt drains batch 1 of the run and seals its admission generation at
// highwater 1. The reference engine rejects a seal at the empty frontier.
func sealRunAt(t *testing.T, g *MemoryGateway, admission uint64) SealAck {
	t.Helper()
	fence := fenceAt(admission)
	if err := g.OpenSession(fence); err != nil {
		t.Fatal(err)
	}
	plan := SealedPlan{Identity: testIdentity(1), Stream: "s",
		FrameKeys: []FrameKey{{Ordinal: 0, Fragment: 0, Complete: true}}}
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
	seal, err := g.SealGeneration(fence, SealIntent{BindingGeneration: 1, AdmissionGeneration: admission,
		DrainID: proto.ID16{9}, Highwater: 1, BindingDigest: testBinding})
	if err != nil {
		t.Fatalf("SealGeneration: %v", err)
	}
	return seal
}

// importOther opens a session of another run and imports one CLOSED batch
// of it at one admission generation.
func importOther(t *testing.T, g *MemoryGateway, admission uint64) error {
	t.Helper()
	other := Fence{SessionToken: []byte("other"), Producer: proto.ID16{7}, Run: proto.ID16{8},
		ConnectionGeneration: 1, AdmissionGeneration: admission, BindingDigest: testBinding, Kind: SessionNormal}
	if err := g.OpenSession(other); err != nil {
		t.Fatal(err)
	}
	identity := testIdentity(1)
	identity.Batch = BatchID{Producer: other.Producer, Run: other.Run, Seq: 1}
	return g.ImportClosed(other, SealedPlan{Identity: identity, Stream: "s"}, false)
}

// TestGatewayDataAdmissionWaitsForResume locks the data admission rule of
// the drain and cutover protocol (acceptance case AC-DLV-11-02) in the
// reference engine: a sealed run admits data only in the generation its
// Resume opened.
func TestGatewayDataAdmissionWaitsForResume(t *testing.T) {
	g := newTestGateway(t)
	if err := g.SetActiveBinding(1, testBinding); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}

	// A run without a seal admits data in every generation.
	for _, admission := range []uint64{1, 2, 3} {
		if err := importAt(t, g, admission, 5); err != nil {
			t.Fatalf("unsealed run, generation %d: %v", admission, err)
		}
	}

	// The seal closes the whole run: the next generation admits no data
	// before the Resume.
	seal := sealRunAt(t, g, 1)
	for _, admission := range []uint64{1, 2, 3} {
		if err := importAt(t, g, admission, 6); !errors.Is(err, ErrState) {
			t.Fatalf("sealed run without a Resume, generation %d: got %v, want ErrState", admission, err)
		}
	}
	// The reference engine keeps one seal for the whole store, not one per
	// run: another run is closed by the same seal. The gateway journal
	// keeps the seal per run.
	if err := importOther(t, g, 1); !errors.Is(err, ErrState) {
		t.Fatalf("another run beside a sealed one: got %v, want ErrState", err)
	}

	// The Resume opens generation 2, and only that one.
	if err := g.OpenSession(fenceAt(2)); err != nil {
		t.Fatal(err)
	}
	resumed, err := g.Resume(fenceAt(2), ResumeIntent{ResumeID: proto.ID16{5}, OldSeal: seal,
		BindingDigest: testBinding, NextAdmissionGeneration: 2})
	if err != nil || resumed.NewAdmissionGeneration != 2 {
		t.Fatalf("Resume: %+v %v", resumed, err)
	}
	if err := importAt(t, g, 2, 7); err != nil {
		t.Fatalf("resumed generation: %v", err)
	}
	for _, admission := range []uint64{1, 3} {
		if err := importAt(t, g, admission, 8); !errors.Is(err, ErrState) {
			t.Fatalf("generation %d beside the resumed one: got %v, want ErrState", admission, err)
		}
	}
}

// TestGatewayDataAdmissionClosesUnderPreparedSwitch locks the other two
// rules the reference engine shared with the journal: a prepared switch
// closes data admission for every run until its commit, and the Resume of
// the seal waits for that commit.
func TestGatewayDataAdmissionClosesUnderPreparedSwitch(t *testing.T) {
	g := newTestGateway(t)
	if err := g.SetActiveBinding(1, testBinding); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	seal := sealRunAt(t, g, 1)
	next := proto.Hash32{7}
	view := BindingView{DestinationID: proto.ID16{7}, Generation: 2,
		Network:         proto.NetworkIdentity{Family: proto.NetworkFamilyEVM},
		ExpectedAccount: "A", Stream: "S2", SubjectPrefix: "evm.v3.bsc",
		DescriptorDigest: next}
	if err := g.OpenSession(fenceAt(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.PrepareSwitch(fenceAt(2), view, proto.ID16{5}); err != nil {
		t.Fatalf("PrepareSwitch: %v", err)
	}

	// The switch is only prepared: the sealed run admits no data, and the
	// Resume of its seal waits for the commit.
	if err := importAt(t, g, 2, 5); !errors.Is(err, ErrState) {
		t.Fatalf("sealed run under a prepared switch: got %v, want ErrState", err)
	}
	resume := ResumeIntent{ResumeID: proto.ID16{5}, OldSeal: seal,
		BindingDigest: testBinding, NextAdmissionGeneration: 2}
	if _, err := g.Resume(fenceAt(2), resume); !errors.Is(err, ErrState) {
		t.Fatalf("resume under a prepared switch: got %v, want ErrState", err)
	}
	// The commit alone does not reopen the sealed run; the Resume then
	// continues under the new binding and opens the generation.
	if err := g.OpenSession(fenceAt(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CommitSwitch(fenceAt(2), proto.ID16{5}); err != nil {
		t.Fatalf("CommitSwitch: %v", err)
	}
	if err := importAt(t, g, 2, 5); !errors.Is(err, ErrState) {
		t.Fatalf("sealed run after the switch commit: got %v, want ErrState", err)
	}
	resume.BindingDigest = next
	if err := g.OpenSession(fenceAt(2)); err != nil {
		t.Fatal(err)
	}
	resumed, err := g.Resume(fenceAt(2), resume)
	if err != nil || resumed.BindingDigest != next {
		t.Fatalf("Resume after the commit: %+v %v", resumed, err)
	}
	if err := importAt(t, g, 2, 5); err != nil {
		t.Fatalf("resumed generation after the switch: %v", err)
	}
}

// TestGatewayDataAdmissionClosesUnderPreparedSwitchAfterResume isolates
// the prepared switch from the seal: once the Resume opened generation 2, a
// prepared switch alone closes data for every run until its commit.
func TestGatewayDataAdmissionClosesUnderPreparedSwitchAfterResume(t *testing.T) {
	g := newTestGateway(t)
	if err := g.SetActiveBinding(1, testBinding); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	seal := sealRunAt(t, g, 1)
	next := proto.Hash32{7}
	if err := g.OpenSession(fenceAt(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Resume(fenceAt(2), ResumeIntent{ResumeID: proto.ID16{5}, OldSeal: seal,
		BindingDigest: testBinding, NextAdmissionGeneration: 2}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// Generation 2 is open.
	if err := importAt(t, g, 2, 5); err != nil {
		t.Fatalf("resumed generation: %v", err)
	}
	if err := importOther(t, g, 2); err != nil {
		t.Fatalf("another run in the resumed generation: %v", err)
	}
	view := BindingView{DestinationID: proto.ID16{7}, Generation: 2,
		Network:         proto.NetworkIdentity{Family: proto.NetworkFamilyEVM},
		ExpectedAccount: "A", Stream: "S2", SubjectPrefix: "evm.v3.bsc",
		DescriptorDigest: next}
	if err := g.OpenSession(fenceAt(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.PrepareSwitch(fenceAt(2), view, proto.ID16{5}); err != nil {
		t.Fatalf("PrepareSwitch: %v", err)
	}

	// The switch is only prepared: no run admits data, although the
	// generation is open and the other run was never sealed.
	if err := importAt(t, g, 2, 6); !errors.Is(err, ErrState) {
		t.Fatalf("resumed run under a prepared switch: got %v, want ErrState", err)
	}
	if err := importOther(t, g, 2); !errors.Is(err, ErrState) {
		t.Fatalf("another run under a prepared switch: got %v, want ErrState", err)
	}

	// The commit reopens admission for every run.
	if err := g.OpenSession(fenceAt(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CommitSwitch(fenceAt(2), proto.ID16{5}); err != nil {
		t.Fatalf("CommitSwitch: %v", err)
	}
	if err := importAt(t, g, 2, 6); err != nil {
		t.Fatalf("resumed run after the switch commit: %v", err)
	}
	if err := importOther(t, g, 2); err != nil {
		t.Fatalf("another run after the switch commit: %v", err)
	}
}
