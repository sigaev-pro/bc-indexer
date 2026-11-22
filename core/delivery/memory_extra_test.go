package delivery

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// TestGatewayRAMFrameChunksDiscarded: the RECEIVING overlay is RAM only;
// a reopen loses it while the durable batch states stand.
func TestGatewayRAMFrameChunksDiscarded(t *testing.T) {
	g := newTestGateway(t)
	fence := testFence(1)
	id := BatchID{Producer: testProducer, Run: testRun, Seq: 1}
	if err := g.StageFrameChunk(fence, id, "frame-chunk-a"); err != nil {
		t.Fatalf("StageFrameChunk: %v", err)
	}
	if err := g.StageFrameChunk(fence, id, "frame-chunk-b"); err != nil {
		t.Fatalf("StageFrameChunk: %v", err)
	}
	if got := len(g.StagedFrameChunks(fence, 1)); got != 2 {
		t.Fatalf("staged %d chunks", got)
	}
	g.Reopen()
	if err := g.OpenSession(fence); err != nil {
		t.Fatalf("OpenSession after reopen: %v", err)
	}
	if got := len(g.StagedFrameChunks(fence, 1)); got != 0 {
		t.Fatalf("reopen kept RAM frame chunks: %d", got)
	}
	// A retired sequence refuses staged chunks without creating keys.
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
	if err := g.StageFrameChunk(fence, id, "late"); !errors.Is(err, ErrAlreadyRetired) {
		t.Fatalf("staged chunk of a retired sequence: %v, want ErrAlreadyRetired", err)
	}
}

// TestProducerModeAndCounterRules locks the mode registry and the
// sequence counter behavior.
func TestProducerModeAndCounterRules(t *testing.T) {
	p := newTestProducer(t)
	if err := p.SetMode(ProducerMode(9)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("unknown mode: %v", err)
	}
	for _, mode := range []ProducerMode{ModeDrain, ModeDrained, ModeBlocked, ModeLive} {
		if err := p.SetMode(mode); err != nil {
			t.Fatalf("SetMode(%s): %v", mode, err)
		}
		if p.Mode() != mode {
			t.Fatalf("mode %s did not stick", mode)
		}
	}
	seq1, _ := p.AllocateIntent()
	seq2, _ := p.AllocateIntent()
	if seq1 != 1 || seq2 != 2 {
		t.Fatalf("sequences %d then %d", seq1, seq2)
	}
	// Staging before any intent is an error.
	if err := p.StageChunk(9, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("staging without intent: %v", err)
	}
	// Cleanup of an unknown batch is an error.
	if _, err := p.CleanupPayload(9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cleanup without record: %v", err)
	}
	// Retire of an unknown sequence above the prefix is an error; of an
	// already retired sequence it is idempotent.
	if err := p.Retire(9, IngestAck{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retire without record: %v", err)
	}
	if err := p.Retire(0, IngestAck{}); err != nil {
		t.Fatalf("retire of the empty frontier must be idempotent: %v", err)
	}
	// Payload chunk accounting is observable.
	if err := p.CommitReady(1, testIdentity(1)); err != nil {
		t.Fatalf("CommitReady: %v", err)
	}
	if got := p.PayloadChunkCount(1); got != 1 {
		t.Fatalf("payload chunks %d, want 1 (no staged chunks)", got)
	}
	// Cleanup before the outbox exists is refused.
	if _, err := p.CleanupPayload(1); !errors.Is(err, ErrState) {
		t.Fatalf("cleanup before ack: %v, want ErrState", err)
	}
}

// TestGatewayImportConflictAndEpochRules: a second import of the same
// batch under a different identity conflicts; a non-preserved import
// re-issues under the local store epoch.
func TestGatewayImportConflictAndEpochRules(t *testing.T) {
	g := newTestGateway(t)
	fence := testFence(1)
	plan := SealedPlan{Identity: testIdentity(1), Stream: "s"}
	if err := g.ImportClosed(fence, plan, false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	record, ok := g.Batch(fence, plan.Identity.Batch)
	if !ok || record.State != StateClosed || len(record.Acked) != 0 {
		t.Fatalf("imported record: %+v ok=%v", record, ok)
	}
	if record.Identity.IssuerStoreEpoch != testEpoch {
		t.Fatalf("non-preserved import must carry the local store epoch")
	}
	other := plan
	other.Identity.ManifestDigest = proto.Hash32{9}
	if err := g.ImportClosed(fence, other, false); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("conflicting import: %v", err)
	}
	// A preserved import keeps the carried epoch: the IMPORT_SEALED rule.
	fresh, err := NewMemoryGateway(proto.ID16{77})
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := fresh.OpenSession(fence); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	preserved := plan
	preserved.Identity.IssuerStoreEpoch = proto.ID16{88}
	if err := fresh.ImportClosed(fence, preserved, true); err != nil {
		t.Fatalf("preserved import: %v", err)
	}
	record, _ = fresh.Batch(fence, preserved.Identity.Batch)
	if record.Identity.IssuerStoreEpoch != (proto.ID16{88}) {
		t.Fatalf("preserved import rewrote the issuer epoch")
	}
	// The session binding gates the import.
	wrongBinding := fence
	wrongBinding.BindingDigest = proto.Hash32{9}
	if err := fresh.ImportClosed(wrongBinding, SealedPlan{Identity: testIdentity(2)}, false); !errors.Is(err, ErrFenced) {
		t.Fatalf("import under another binding: %v", err)
	}
	// An invalid batch id is rejected before any state changes.
	bad := BatchIdentity{}.withBatch(BatchID{Seq: 3})
	if err := fresh.ImportClosed(fence, SealedPlan{Identity: bad}, false); err == nil {
		t.Fatalf("invalid batch accepted")
	}
}

// withBatch is a small test helper completing a minimal identity.
func (i BatchIdentity) withBatch(b BatchID) BatchIdentity {
	i.Batch = b
	i.IssuerStoreEpoch = testEpoch
	i.BindingDigest = testBinding
	return i
}

// TestGatewayAdoptBaselineEqualAndMaintenanceSeal: an equal baseline is a
// no-op; the seal guard covers a batch at or below the highwater.
func TestGatewayAdoptBaselineEqualAndSealGuards(t *testing.T) {
	g := newTestGateway(t)
	fence := testFence(1)
	if err := g.SetActiveBinding(1, testBinding); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	if err := g.AdoptBaseline(fence, testProducer, testRun, 0); err != nil {
		t.Fatalf("equal baseline: %v", err)
	}
	// Seal with an open batch below the highwater refuses.
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
	intent := SealIntent{BindingGeneration: 1, AdmissionGeneration: 1,
		DrainID: proto.ID16{9}, Highwater: 1, BindingDigest: testBinding}
	if _, err := g.SealGeneration(fence, intent); !errors.Is(err, ErrState) {
		t.Fatalf("seal over an unreleased batch: %v, want ErrState", err)
	}
	// Release it, then the seal binds and repeats idempotently.
	if err := g.BeginRelease(fence, plan.Identity.Batch); err != nil {
		t.Fatalf("BeginRelease: %v", err)
	}
	if _, err := g.CleanupStep(fence, plan.Identity.Batch); err != nil {
		t.Fatalf("CleanupStep: %v", err)
	}
	if _, err := g.FinishRelease(fence, plan.Identity.Batch); err != nil {
		t.Fatalf("FinishRelease: %v", err)
	}
	ack, err := g.SealGeneration(fence, intent)
	if err != nil {
		t.Fatalf("SealGeneration: %v", err)
	}
	if ack.DrainID != intent.DrainID || ack.Highwater != 1 {
		t.Fatalf("seal ack %+v", ack)
	}
	repeat, err := g.SealGeneration(fence, intent)
	if err != nil || !repeat.SameSeal(ack) {
		t.Fatalf("repeated seal: %+v err=%v", repeat, err)
	}
	// A different drain id on the sealed generation conflicts.
	other := intent
	other.DrainID = proto.ID16{8}
	if _, err := g.SealGeneration(fence, other); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("second drain of a sealed generation: %v", err)
	}
	// A seal intent of another binding is refused.
	wrong := intent
	wrong.BindingDigest = proto.Hash32{9}
	wrong.DrainID = proto.ID16{7}
	if _, err := g.SealGeneration(fence, wrong); !errors.Is(err, ErrState) {
		t.Fatalf("seal of another binding: %v", err)
	}
	// FinishRelease before the cleanup completes is refused.
	g2 := newTestGateway(t)
	plan2 := SealedPlan{Identity: testIdentity(1), Stream: "s", FrameKeys: []FrameKey{
		{Ordinal: 0, Fragment: 0, Complete: true}, {Ordinal: 1, Fragment: 0, Complete: true},
	}}
	if err := g2.ImportClosed(fence, plan2, false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	for _, k := range plan2.FrameKeys {
		if err := g2.RecordPubAck(fence, plan2.Identity.Batch, plan2.Identity.PlanDigest, k); err != nil {
			t.Fatalf("RecordPubAck: %v", err)
		}
	}
	if _, err := g2.MarkNATSACKed(fence, plan2.Identity); err != nil {
		t.Fatalf("MarkNATSACKed: %v", err)
	}
	if err := g2.BeginRelease(fence, plan2.Identity.Batch); err != nil {
		t.Fatalf("BeginRelease: %v", err)
	}
	if more, err := g2.CleanupStep(fence, plan2.Identity.Batch); err != nil || !more {
		t.Fatalf("first cleanup step: more=%v err=%v", more, err)
	}
	if _, err := g2.FinishRelease(fence, plan2.Identity.Batch); !errors.Is(err, ErrState) {
		t.Fatalf("finish before cleanup end: %v, want ErrState", err)
	}
}
