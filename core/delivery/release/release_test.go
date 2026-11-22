package release

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

var (
	rProducer = proto.ID16{1}
	rRun      = proto.ID16{2}
	rEpoch    = proto.ID16{3}
	rBinding  = proto.Hash32{4}
)

func rIdentity(seq uint64) delivery.BatchIdentity {
	return delivery.BatchIdentity{
		Batch:                delivery.BatchID{Producer: rProducer, Run: rRun, Seq: seq},
		ManifestDigest:       proto.Hash32{byte(seq), 1},
		PlanDigest:           proto.Hash32{byte(seq), 2},
		LayoutManifestDigest: proto.Hash32{byte(seq), 3},
		BindingDigest:        rBinding,
		IssuerStoreEpoch:     rEpoch,
	}
}

func rFence() delivery.Fence {
	return delivery.Fence{SessionToken: []byte("t"), Producer: rProducer, Run: rRun,
		ConnectionGeneration: 1, AdmissionGeneration: 1, BindingDigest: rBinding,
		Kind: delivery.SessionNormal}
}

func rProducerStore(t *testing.T) *delivery.MemoryProducer {
	t.Helper()
	p, err := delivery.NewMemoryProducer(rProducer, rRun)
	if err != nil {
		t.Fatalf("NewMemoryProducer: %v", err)
	}
	return p
}

func rGateway(t *testing.T) *delivery.MemoryGateway {
	t.Helper()
	g, err := delivery.NewMemoryGateway(rEpoch)
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := g.OpenSession(rFence()); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	return g
}

// rReady drives one batch of the producer to a released-ready state: two
// payload chunks, acknowledged and cleaned.
func rReady(t *testing.T, prod *delivery.MemoryProducer, gw *delivery.MemoryGateway, seq uint64) delivery.BatchIdentity {
	t.Helper()
	identity := rIdentity(seq)
	sSeq, err := prod.AllocateIntent()
	if err != nil {
		t.Fatalf("AllocateIntent: %v", err)
	}
	if sSeq != seq {
		t.Fatalf("allocated %d, want %d", sSeq, seq)
	}
	if err := prod.StageChunk(seq, "chunk-1"); err != nil {
		t.Fatalf("StageChunk: %v", err)
	}
	if err := prod.StageChunk(seq, "chunk-2"); err != nil {
		t.Fatalf("StageChunk: %v", err)
	}
	if err := prod.CommitReady(seq, identity); err != nil {
		t.Fatalf("CommitReady: %v", err)
	}
	keys := []delivery.FrameKey{{Ordinal: 0, Fragment: 0, Complete: true}, {Ordinal: 1, Fragment: 0, Complete: true}}
	if err := prod.CommitSealed(seq, NewSealedPlan(identity, 1000, "S", keys)); err != nil {
		t.Fatalf("CommitSealed: %v", err)
	}
	if err := gw.ImportClosed(rFence(), NewSealedPlan(identity, 1000, "S", keys), false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	fence := rFence()
	for _, k := range keys {
		if err := gw.RecordPubAck(fence, identity.Batch, identity.PlanDigest, k); err != nil {
			t.Fatalf("RecordPubAck: %v", err)
		}
	}
	ack, err := gw.MarkNATSACKed(fence, identity)
	if err != nil {
		t.Fatalf("MarkNATSACKed: %v", err)
	}
	if err := OnIngestAck(prod, seq, ack); err != nil {
		t.Fatalf("OnIngestAck: %v", err)
	}
	if ReleaseReady(prod, seq) {
		t.Fatalf("release ready before payload cleanup")
	}
	if remains, err := CleanupPayload(prod, seq, 1); err != nil || !remains {
		t.Fatalf("first bounded cleanup step: remains=%v err=%v", remains, err)
	}
	if remains, err := CleanupPayload(prod, seq, 10); err != nil || remains {
		t.Fatalf("cleanup to the end: remains=%v err=%v", remains, err)
	}
	if !ReleaseReady(prod, seq) {
		t.Fatalf("release not ready after cleanup")
	}
	return identity
}

func TestOrderedReleaseEndToEnd(t *testing.T) {
	prod := rProducerStore(t)
	gw := rGateway(t)
	first := rReady(t, prod, gw, 1)
	second := rReady(t, prod, gw, 2)

	// Releasing the second batch first is refused: order is strict.
	if _, err := Release(gw, rFence(), second.Batch, 10); !errors.Is(err, delivery.ErrState) {
		t.Fatalf("out-of-order release: %v, want ErrState", err)
	}
	ack, err := Release(gw, rFence(), first.Batch, 10)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if ack.RetiredPrefix != 1 {
		t.Fatalf("ack prefix %d", ack.RetiredPrefix)
	}
	if err := OnReleaseAck(prod, ack); err != nil {
		t.Fatalf("OnReleaseAck: %v", err)
	}
	if got := prod.RetiredPrefix(); got != 1 {
		t.Fatalf("producer prefix %d", got)
	}
	if _, ok := prod.Batch(1); ok {
		t.Fatalf("the retired batch record survived")
	}
	// Repeating the release answers the same prefix without new work.
	repeat, err := Release(gw, rFence(), first.Batch, 10)
	if err != nil || repeat.RetiredPrefix != 1 {
		t.Fatalf("repeated release: %+v err=%v", repeat, err)
	}
	if err := OnReleaseAck(prod, repeat); err != nil {
		t.Fatalf("repeated OnReleaseAck: %v", err)
	}
	// Now the second batch releases in order.
	ack2, err := Release(gw, rFence(), second.Batch, 10)
	if err != nil {
		t.Fatalf("Release 2: %v", err)
	}
	if err := OnReleaseAck(prod, ack2); err != nil {
		t.Fatalf("OnReleaseAck 2: %v", err)
	}
	if got := prod.RetiredPrefix(); got != 2 {
		t.Fatalf("producer prefix %d after two releases", got)
	}
}

func TestReleaseBoundedCleanupResumesAfterCrash(t *testing.T) {
	prod := rProducerStore(t)
	gw := rGateway(t)
	identity := rReady(t, prod, gw, 1)
	if err := gw.BeginRelease(rFence(), identity.Batch); err != nil {
		t.Fatalf("BeginRelease: %v", err)
	}
	// One bounded step, then a restart of the cleanup.
	if more, err := gw.CleanupStep(rFence(), identity.Batch); err != nil || !more {
		t.Fatalf("first cleanup step: more=%v err=%v", more, err)
	}
	gw.Reopen()
	if err := gw.OpenSession(rFence()); err != nil {
		t.Fatalf("OpenSession after reopen: %v", err)
	}
	// RELEASING survives; the cursor resumes and the release completes.
	ack, err := Release(gw, rFence(), identity.Batch, 10)
	if err != nil {
		t.Fatalf("Release after reopen: %v", err)
	}
	if ack.RetiredPrefix != 1 {
		t.Fatalf("prefix %d", ack.RetiredPrefix)
	}
}

func TestGatewayOneAheadOfProducer(t *testing.T) {
	prod := rProducerStore(t)
	gw := rGateway(t)
	identity := rReady(t, prod, gw, 1)
	// The gateway releases and its ack is lost before the producer sees
	// it: the producer repeats the release and gets the stored answer.
	ack, err := Release(gw, rFence(), identity.Batch, 10)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	lost := ack
	repeat, err := Release(gw, rFence(), identity.Batch, 10)
	if err != nil {
		t.Fatalf("repeated Release after lost ack: %v", err)
	}
	if repeat.RetiredPrefix != lost.RetiredPrefix {
		t.Fatalf("repeated ack prefix %d, want %d", repeat.RetiredPrefix, lost.RetiredPrefix)
	}
	if err := OnReleaseAck(prod, repeat); err != nil {
		t.Fatalf("producer applying the repeated ack: %v", err)
	}
	if got := prod.RetiredPrefix(); got != 1 {
		t.Fatalf("producer prefix %d", got)
	}
}

func TestRecoveryReleaseWithoutPayload(t *testing.T) {
	prod := rProducerStore(t)
	gw := rGateway(t)
	identity := rReady(t, prod, gw, 1)
	// The producer deletes the payload; the gateway loses its store.
	if _, err := CleanupPayload(prod, 1, 10); err != nil {
		t.Fatalf("CleanupPayload: %v", err)
	}
	fresh, err := delivery.NewMemoryGateway(proto.ID16{9})
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := fresh.OpenSession(rFence()); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	// A recovery release that jumps the frontier refuses: the assertion
	// must name exactly the next batch of the producer prefix.
	if _, err := RecoveryRelease(prod, fresh, rFence(), 2); !errors.Is(err, delivery.ErrState) {
		t.Fatalf("recovery jump: %v, want ErrState", err)
	}
	if err := AdoptBaseline(fresh, rFence(), 0); err != nil {
		t.Fatalf("AdoptBaseline: %v", err)
	}
	ack, err := RecoveryRelease(prod, fresh, rFence(), 1)
	if err != nil {
		t.Fatalf("RecoveryRelease: %v", err)
	}
	if ack.RetiredPrefix != 1 || ack.Batch != identity.Batch {
		t.Fatalf("recovery ack %+v", ack)
	}
	if got := fresh.RetiredPrefix(rProducer, rRun); got != 1 {
		t.Fatalf("fresh gateway prefix %d", got)
	}
	// The producer applies the ack and finishes its outbox.
	if err := OnReleaseAck(prod, ack); err != nil {
		t.Fatalf("OnReleaseAck: %v", err)
	}
	// A second recovery release of the same sequence is a replay of a
	// retired batch: the gateway answers from its prefix.
	if _, err := RecoveryRelease(prod, fresh, rFence(), 1); !errors.Is(err, delivery.ErrState) {
		t.Fatalf("replayed recovery: %v, want ErrState (the producer moved on)", err)
	}
}

func TestRecoveryReleaseRejectsIncompleteAssertion(t *testing.T) {
	prod := rProducerStore(t)
	gw := rGateway(t)
	rReady(t, prod, gw, 1)
	// An unacknowledged batch (no outbox) never recovers as delivered.
	sSeq, _ := prod.AllocateIntent()
	if sSeq != 2 {
		t.Fatalf("allocated %d", sSeq)
	}
	if err := prod.CommitReady(2, rIdentity(2)); err != nil {
		t.Fatalf("CommitReady: %v", err)
	}
	if _, err := RecoveryRelease(prod, gw, rFence(), 2); !errors.Is(err, delivery.ErrState) {
		t.Fatalf("unACKed recovery: %v, want ErrState", err)
	}
	// A conflicting local record blocks the recovery release.
	conflict := rGateway(t)
	if err := conflict.ImportClosed(rFence(), NewSealedPlan(rIdentity(1), 1000, "S",
		[]delivery.FrameKey{{Ordinal: 0, Fragment: 0, Complete: true}}), false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	if _, err := RecoveryRelease(prod, conflict, rFence(), 1); !errors.Is(err, delivery.ErrConflictPending) {
		t.Fatalf("conflicting local record: %v, want ErrConflictPending", err)
	}
	if got := conflict.RetiredPrefix(rProducer, rRun); got != 0 {
		t.Fatalf("prefix advanced to %d on a rejected recovery", got)
	}
}

func TestImportSealedPreservesIssuerIdentity(t *testing.T) {
	prod := rProducerStore(t)
	gw := rGateway(t)
	identity := rReady(t, prod, gw, 1)
	// Gateway store loss: a fresh store imports the exact issuer plan.
	fresh, err := delivery.NewMemoryGateway(proto.ID16{9})
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := fresh.OpenSession(rFence()); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	imported, err := ImportSealed(prod, fresh, rFence(), 1)
	if err != nil {
		t.Fatalf("ImportSealed: %v", err)
	}
	if !imported.SameIdentity(identity) {
		t.Fatalf("imported identity changed: %+v", imported)
	}
	if imported.IssuerStoreEpoch != rEpoch {
		t.Fatalf("issuer epoch rewritten to %v", imported.IssuerStoreEpoch)
	}
	record, ok := fresh.Batch(rFence(), identity.Batch)
	if !ok || record.State != delivery.StateClosed {
		t.Fatalf("imported batch state: %+v ok=%v", record, ok)
	}
	if len(record.FrameKeys) != 2 {
		t.Fatalf("imported frame keys %d", len(record.FrameKeys))
	}
	// An idempotent replay of the same plan returns the same identity.
	if imported2, err := ImportSealed(prod, fresh, rFence(), 1); err != nil || !imported2.SameIdentity(identity) {
		t.Fatalf("idempotent import: %+v err=%v", imported2, err)
	}
	// A different manifest under the same batch id is an immutable
	// conflict: the original stands.
	mutatedPlan := NewSealedPlan(identity, 1000, "S", []delivery.FrameKey{{Ordinal: 0, Fragment: 0, Complete: true}})
	mutatedPlan.Identity.ManifestDigest = proto.Hash32{9}
	if err := fresh.ImportClosed(rFence(), mutatedPlan, true); !errors.Is(err, delivery.ErrImmutableConflict) {
		t.Fatalf("mutated import: %v, want ErrImmutableConflict", err)
	}
}
