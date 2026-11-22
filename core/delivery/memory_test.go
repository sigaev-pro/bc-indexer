package delivery

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// Fixed test identities: any nonzero 16-byte ids and digests.
var (
	testProducer = proto.ID16{1}
	testRun      = proto.ID16{2}
	testEpoch    = proto.ID16{3}
	testBinding  = proto.Hash32{4}
)

func testIdentity(seq uint64) BatchIdentity {
	return BatchIdentity{
		Batch:                BatchID{Producer: testProducer, Run: testRun, Seq: seq},
		ManifestDigest:       proto.Hash32{byte(seq), 1},
		PlanDigest:           proto.Hash32{byte(seq), 2},
		LayoutManifestDigest: proto.Hash32{byte(seq), 3},
		BindingDigest:        testBinding,
		IssuerStoreEpoch:     testEpoch,
	}
}

func testFence(gen uint64) Fence {
	return Fence{
		SessionToken:         []byte("token"),
		Producer:             testProducer,
		Run:                  testRun,
		ConnectionGeneration: gen,
		AdmissionGeneration:  1,
		BindingDigest:        testBinding,
		Kind:                 SessionNormal,
	}
}

func newTestProducer(t *testing.T) *MemoryProducer {
	t.Helper()
	p, err := NewMemoryProducer(testProducer, testRun)
	if err != nil {
		t.Fatalf("NewMemoryProducer: %v", err)
	}
	return p
}

func newTestGateway(t *testing.T) *MemoryGateway {
	t.Helper()
	g, err := NewMemoryGateway(testEpoch)
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := g.OpenSession(testFence(1)); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	return g
}

// TestProducerClosingCommitSurvivesReopen is the durable/RAM oracle of
// fault 1: staged chunks are RAM only, the closing commit is durable.
func TestProducerClosingCommitSurvivesReopen(t *testing.T) {
	p := newTestProducer(t)
	seq, err := p.AllocateIntent()
	if err != nil {
		t.Fatalf("AllocateIntent: %v", err)
	}
	if err := p.StageChunk(seq, "chunk-a"); err != nil {
		t.Fatalf("StageChunk: %v", err)
	}
	p.Reopen()
	if got := p.StagedChunks(seq); len(got) != 0 {
		t.Fatalf("reopen kept staged RAM chunks: %v", got)
	}
	rec, ok := p.Batch(seq)
	if !ok || rec.State != StateStaging {
		t.Fatalf("intent/staging state did not survive reopen: %+v ok=%v", rec, ok)
	}
	// The batch is not READY: no publish path may treat it as closed.
	if rec.State == StateReady || rec.HasIdentity {
		t.Fatalf("STAGING must not carry a READY identity")
	}
	if err := p.CommitReady(seq, testIdentity(seq)); err != nil {
		t.Fatalf("CommitReady: %v", err)
	}
	p.Reopen()
	rec, ok = p.Batch(seq)
	if !ok || rec.State != StateReady || !rec.HasIdentity {
		t.Fatalf("READY closing commit did not survive reopen: %+v ok=%v", rec, ok)
	}
}

// TestProducerIdentityImmutableAfterReady: a different digest after READY
// is a conflict, never a replacement.
func TestProducerIdentityImmutableAfterReady(t *testing.T) {
	p := newTestProducer(t)
	seq, _ := p.AllocateIntent()
	if err := p.CommitReady(seq, testIdentity(seq)); err != nil {
		t.Fatalf("CommitReady: %v", err)
	}
	other := testIdentity(seq)
	other.ManifestDigest = proto.Hash32{9}
	if err := p.CommitReady(seq, other); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("CommitReady with a different digest: got %v, want ErrImmutableConflict", err)
	}
}

// TestGatewayPartialPubAckNeverCompletes: NATS_ACKED requires the PubAck
// of every expected frame; one missing key keeps the batch CLOSED.
func TestGatewayPartialPubAckNeverCompletes(t *testing.T) {
	g := newTestGateway(t)
	plan := SealedPlan{Identity: testIdentity(1), Stream: "s", FrameKeys: []FrameKey{
		{Ordinal: 0, Fragment: 0, Part: 0, Complete: true},
		{Ordinal: 1, Fragment: 0, Part: 0, Complete: true},
	}}
	if err := g.ImportClosed(testFence(1), plan, false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	if err := g.RecordPubAck(testFence(1), plan.Identity.Batch, plan.Identity.PlanDigest, plan.FrameKeys[0]); err != nil {
		t.Fatalf("RecordPubAck: %v", err)
	}
	if _, err := g.MarkNATSACKed(testFence(1), plan.Identity); !errors.Is(err, ErrState) {
		t.Fatalf("MarkNATSACKed with a partial set: got %v, want ErrState", err)
	}
	if err := g.RecordPubAck(testFence(1), plan.Identity.Batch, plan.Identity.PlanDigest, plan.FrameKeys[1]); err != nil {
		t.Fatalf("RecordPubAck: %v", err)
	}
	ack, err := g.MarkNATSACKed(testFence(1), plan.Identity)
	if err != nil {
		t.Fatalf("MarkNATSACKed: %v", err)
	}
	if !ack.Identity.SameIdentity(plan.Identity) {
		t.Fatalf("IngestAck identity differs from the batch identity")
	}
}

// TestGatewayLatePubAckCannotResurrect: after the release barrier a late
// PubAck callback is fenced, not recorded.
func TestGatewayLatePubAckCannotResurrect(t *testing.T) {
	g := newTestGateway(t)
	plan := SealedPlan{Identity: testIdentity(1), Stream: "s", FrameKeys: []FrameKey{
		{Ordinal: 0, Fragment: 0, Part: 0, Complete: true},
	}}
	fence := testFence(1)
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
	err := g.RecordPubAck(fence, plan.Identity.Batch, plan.Identity.PlanDigest, plan.FrameKeys[0])
	if !errors.Is(err, ErrFenced) {
		t.Fatalf("late PubAck after the release barrier: got %v, want ErrFenced", err)
	}
}

// TestGatewayStaleFenceRejected: an old token, an old connection
// generation or a late admission generation creates no state.
func TestGatewayStaleFenceRejected(t *testing.T) {
	g := newTestGateway(t)
	plan := SealedPlan{Identity: testIdentity(1), Stream: "s"}
	stale := testFence(1)
	stale.SessionToken = []byte("old")
	if err := g.ImportClosed(stale, plan, false); !errors.Is(err, ErrFenced) {
		t.Fatalf("stale token: got %v, want ErrFenced", err)
	}
	oldGen := testFence(1)
	oldGen.ConnectionGeneration = 0
	if err := g.ImportClosed(oldGen, plan, false); !errors.Is(err, ErrFenced) {
		t.Fatalf("old connection generation: got %v, want ErrFenced", err)
	}
	oldAdmission := testFence(1)
	oldAdmission.AdmissionGeneration = 0
	if err := g.ImportClosed(oldAdmission, plan, false); !errors.Is(err, ErrFenced) {
		t.Fatalf("late admission generation: got %v, want ErrFenced", err)
	}
}

// TestGatewayRetireGuard: a sequence at or below the retired prefix
// answers ALREADY_RETIRED without creating keys.
func TestGatewayRetireGuard(t *testing.T) {
	g := newTestGateway(t)
	fence := testFence(1)
	plan := SealedPlan{Identity: testIdentity(1), Stream: "s", FrameKeys: []FrameKey{
		{Ordinal: 0, Fragment: 0, Part: 0, Complete: true},
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
	late := SealedPlan{Identity: testIdentity(1), Stream: "s"}
	if err := g.ImportClosed(fence, late, false); !errors.Is(err, ErrAlreadyRetired) {
		t.Fatalf("import of a retired sequence: got %v, want ErrAlreadyRetired", err)
	}
	if err := g.PrepareGuard(fence, plan.Identity.Batch); !errors.Is(err, ErrAlreadyRetired) {
		t.Fatalf("PrepareGuard: got %v, want ErrAlreadyRetired", err)
	}
}

// TestGatewayRecoveryReleaseRules: exactly the next sequence, no local
// conflict, no jump; the payload is never required.
func TestGatewayRecoveryReleaseRules(t *testing.T) {
	g := newTestGateway(t)
	fence := testFence(1)
	if err := g.AdoptBaseline(fence, testProducer, testRun, 5); err != nil {
		t.Fatalf("AdoptBaseline: %v", err)
	}
	if _, err := g.RecordRecoveryRelease(fence, IngestAck{Identity: testIdentity(7)}); !errors.Is(err, ErrState) {
		t.Fatalf("prefix jump: got %v, want ErrState", err)
	}
	ack, err := g.RecordRecoveryRelease(fence, IngestAck{Identity: testIdentity(6)})
	if err != nil {
		t.Fatalf("RecordRecoveryRelease: %v", err)
	}
	if ack.RetiredPrefix != 6 || ack.Batch.Seq != 6 {
		t.Fatalf("ReleaseAck %+v does not advance the prefix to 6", ack)
	}
	if err := g.AdoptBaseline(fence, testProducer, testRun, 2); err != nil {
		t.Fatalf("lower producer prefix must not roll the gateway back: %v", err)
	}
	if got := g.RetiredPrefix(testProducer, testRun); got != 6 {
		t.Fatalf("prefix rolled back to %d", got)
	}
}

// TestGatewayAdoptBaselineConflict: a claimed baseline over an unconfirmed
// local batch is an error and deletes nothing.
func TestGatewayAdoptBaselineConflict(t *testing.T) {
	g := newTestGateway(t)
	fence := testFence(1)
	plan := SealedPlan{Identity: testIdentity(1), Stream: "s"}
	if err := g.ImportClosed(fence, plan, false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	if err := g.AdoptBaseline(fence, testProducer, testRun, 1); !errors.Is(err, ErrConflictPending) {
		t.Fatalf("baseline over unconfirmed batch: got %v, want ErrConflictPending", err)
	}
	if _, ok := g.Batch(fence, plan.Identity.Batch); !ok {
		t.Fatalf("the conflicting pending batch was deleted")
	}
}

// TestBatchIDAndAckValidation locks the identity rules.
func TestBatchIDAndAckValidation(t *testing.T) {
	bad := BatchID{Producer: testProducer, Run: testRun}
	if err := bad.Validate(); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("zero sequence: got %v", err)
	}
	bad = BatchID{Seq: 1}
	if err := bad.Validate(); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("zero ids: got %v", err)
	}
	id := testIdentity(1)
	if id.IssuerStoreEpoch.IsZero() {
		t.Fatalf("test identity carries no epoch")
	}
	goodAck := IngestAck{Identity: id}
	if err := goodAck.Validate(); err != nil {
		t.Fatalf("valid ack rejected: %v", err)
	}
	noEpoch := id
	noEpoch.IssuerStoreEpoch = proto.ID16{}
	noAck := IngestAck{Identity: noEpoch}
	if err := noAck.Validate(); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("ack without issuer epoch: got %v", err)
	}
}
