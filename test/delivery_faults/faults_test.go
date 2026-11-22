package delivery_faults

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	derrors "github.com/sigaev-pro/bc-indexer/core/delivery/errors"
	"github.com/sigaev-pro/bc-indexer/core/delivery/release"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/compatibility"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// Fault 1: STAGING chunks are RAM only. Before the closing marker there
// is no publishable batch and no IngestAck; a reopen loses the staged
// bytes; after the closing marker the immutable READY state survives.
func TestFault1StagingChunksAreRAMOnly(t *testing.T) {
	h := newHarness()
	seq, _ := h.driveToReadyOnly(t)

	// STAGING: staged chunks exist, nothing is publishable.
	staged := h.prod.StagedChunks(seq)
	if len(staged) == 0 {
		t.Fatalf("the fixture lost its staged chunks")
	}
	rec, ok := h.prod.Batch(seq)
	if !ok || rec.State != delivery.StateStaging {
		t.Fatalf("batch state %s ok=%v", rec.State, ok)
	}
	if rec.HasIdentity {
		t.Fatalf("STAGING carries a READY identity")
	}
	// No IngestAck can be applied to a non-SEALED batch.
	fakeAck := delivery.IngestAck{Identity: sealedIdentity(manifestIdentity(seq), fEpoch)}
	if err := release.OnIngestAck(h.prod, seq, fakeAck); !errors.Is(err, delivery.ErrState) {
		t.Fatalf("IngestAck on STAGING: %v, want ErrState", err)
	}
	// A reopen (power loss) discards the RAM chunks but keeps the intent.
	h.prod.Reopen()
	if got := h.prod.StagedChunks(seq); len(got) != 0 {
		t.Fatalf("reopen kept staged RAM chunks: %v", got)
	}
	if _, ok := h.prod.Batch(seq); !ok {
		t.Fatalf("the durable intent did not survive the reopen")
	}
	// The closing commit makes the state durable; it survives a reopen
	// and is immutable afterwards.
	id := manifestIdentity(seq)
	if err := h.prod.CommitReady(seq, id); err != nil {
		t.Fatalf("CommitReady: %v", err)
	}
	h.prod.Reopen()
	rec, ok = h.prod.Batch(seq)
	if !ok || rec.State != delivery.StateReady || !rec.HasIdentity {
		t.Fatalf("READY did not survive the reopen: %+v ok=%v", rec, ok)
	}
	mutated := id
	mutated.ManifestDigest = proto.Hash32{9}
	if err := h.prod.CommitReady(seq, mutated); !errors.Is(err, delivery.ErrImmutableConflict) {
		t.Fatalf("a different digest after READY: %v, want ErrImmutableConflict", err)
	}
}

// driveToReadyOnly leaves one batch in STAGING with staged chunks.
func (h *harness) driveToReadyOnly(t *testing.T) (uint64, delivery.BatchIdentity) {
	t.Helper()
	seq, err := h.prod.AllocateIntent()
	if err != nil {
		t.Fatalf("AllocateIntent: %v", err)
	}
	if err := h.prod.StageChunk(seq, "chunk-1"); err != nil {
		t.Fatalf("StageChunk: %v", err)
	}
	if err := h.prod.StageChunk(seq, "chunk-2"); err != nil {
		t.Fatalf("StageChunk: %v", err)
	}
	return seq, manifestIdentity(seq)
}

// Fault 2: a gateway store loss before SEALED allows a new plan
// generation over the same READY fragments; after SEALED the exact issuer
// plan is imported and the published bytes, layout and message ids never
// change.
func TestFault2GatewayStoreLoss(t *testing.T) {
	h := newHarness()
	freshEpoch := proto.ID16{42}

	// --- before SEALED: the READY manifest survives, a new reservation
	// of the recovered gateway is allowed because nothing was published
	// from the lost attempt.
	seq, manifest := h.driveToReady(t)
	frames := fFrames(t, seq, proto.FamilyTx, proto.KindApplied, 2)
	gw2, err := delivery.NewMemoryGateway(freshEpoch)
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := gw2.SetActiveBinding(1, fBindingDigest); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	if err := gw2.OpenSession(fFence(delivery.SessionNormal, 1)); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	h.gw = gw2
	h.engine, err = publisher.New(h.server, gw2)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The producer seals against the new reservation: same READY
	// manifest, same fragment ids and bytes, a new plan generation.
	id := h.driveToSealed(t, seq, manifest, fKeys(frames))
	if id.IssuerStoreEpoch != freshEpoch {
		t.Fatalf("the new plan kept the lost store's epoch")
	}
	ack := h.publishClosed(t, id, frames)
	if !ack.Identity.SameIdentity(id) {
		t.Fatalf("the ack binds another identity")
	}

	// --- after SEALED: the exact plan imports, bytes and ids never change.
	seq2, manifest2 := h.driveToReady(t)
	frames2 := fFrames(t, seq2, proto.FamilyTx, proto.KindApplied, 3)
	id2 := h.driveToSealed(t, seq2, manifest2, fKeys(frames2))
	// The batch closes and one frame publishes when the gateway store is
	// lost. The single-frame run cannot complete the batch (a partial
	// PubAck set never completes it), but its frame is durably
	// accounted — the least state a crash mid-publish leaves.
	if err := h.gw.ImportClosed(fFence(delivery.SessionNormal, 1),
		release.NewSealedPlan(id2, 1000+seq2, fStream, fKeys(frames2)), false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	if _, err := h.engine.Publish(fFence(delivery.SessionNormal, 1), h.target,
		publisher.Batch{Identity: id2, Frames: frames2[:1]}); !errors.Is(err, delivery.ErrState) {
		t.Fatalf("single-frame run: %v, want ErrState (partial set)", err)
	}
	stored := len(h.server.Messages())
	if stored != 3 { // batch 1 left two frames, the partial run added one
		t.Fatalf("fixture published %d messages, want 3", stored)
	}
	// Gateway store loss: a fresh store imports the sealed plan.
	gw3, err := delivery.NewMemoryGateway(proto.ID16{43})
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := gw3.SetActiveBinding(1, fBindingDigest); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	if err := gw3.OpenSession(fFence(delivery.SessionNormal, 1)); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	imported, err := release.ImportSealed(h.prod, gw3, fFence(delivery.SessionNormal, 1), seq2)
	if err != nil {
		t.Fatalf("ImportSealed: %v", err)
	}
	if !imported.SameIdentity(id2) || imported.IssuerStoreEpoch != id2.IssuerStoreEpoch {
		t.Fatalf("the imported plan changed identity or issuer epoch: %+v", imported)
	}
	// The replay publishes identical bytes: the already-stored frame
	// dedups by its message id, the missing frames store.
	engine3, err := publisher.New(h.server, gw3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	full, err := engine3.Publish(fFence(delivery.SessionNormal, 1), h.target,
		publisher.Batch{Identity: id2, Frames: frames2})
	if err != nil {
		t.Fatalf("replay publish: %v", err)
	}
	if !full.Identity.SameIdentity(id2) {
		t.Fatalf("replay ack binds another identity")
	}
	if got := len(h.server.Messages()); got != 5 { // 2 of batch 1 + 3 of batch 2
		t.Fatalf("replay stored %d unique messages, want 5", got)
	}
	// The message ids of the replay equal the ids of the first attempt.
	for i, frame := range frames2 {
		msgs := h.server.Messages()
		found := false
		for _, msg := range msgs {
			if msg.MsgID == frame.MessageID {
				found = true
				if string(msg.Payload) != string(frame.Payload) {
					t.Fatalf("frame %d replayed with different bytes", i)
				}
			}
		}
		if !found {
			t.Fatalf("frame %d of the replay never stored", i)
		}
	}
	// A different manifest under the sealed batch id is a conflict.
	mutated := id2
	mutated.ManifestDigest = proto.Hash32{9}
	if err := gw3.ImportClosed(fFence(delivery.SessionNormal, 1),
		release.NewSealedPlan(mutated, 1000+seq2, fStream, fKeys(frames2)), true); !errors.Is(err, delivery.ErrImmutableConflict) {
		t.Fatalf("mutated import: %v, want ErrImmutableConflict", err)
	}
}

// Fault 3: the producer holds ACKED/outbox N with the payload deleted and
// prefix N-1; the gateway store is lost. The agreed RecoveryRelease
// advances the prefix to N without the payload. Incomplete assertions,
// jumps and conflicting local records are rejected without advancing.
func TestFault3RecoveryReleaseWithoutPayload(t *testing.T) {
	h := newHarness()
	h.runOneBatch(t) // batch 1 fully released

	// Batch 2 reaches ACKED with the payload deleted; the gateway store
	// is then lost.
	seq2, manifest2 := h.driveToReady(t)
	frames2 := fFrames(t, seq2, proto.FamilyTx, proto.KindApplied, 2)
	id2 := h.driveToSealed(t, seq2, manifest2, fKeys(frames2))
	ack2 := h.publishClosed(t, id2, frames2)
	h.cleanupProducer(t, seq2, ack2)
	if rec, _ := h.prod.Batch(seq2); rec.HasPayload {
		t.Fatalf("the fixture kept the payload")
	}

	fresh, err := delivery.NewMemoryGateway(proto.ID16{44})
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := fresh.SetActiveBinding(1, fBindingDigest); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	if err := fresh.OpenSession(fFence(delivery.SessionNormal, 1)); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	// An incomplete assertion never recovers: batch 3 exists but was
	// never acknowledged.
	seq3, manifest3 := h.driveToReady(t)
	frames3 := fFrames(t, seq3, proto.FamilyTx, proto.KindApplied, 2)
	id3 := h.driveToSealed(t, seq3, manifest3, fKeys(frames3))
	h.gw = fresh
	h.engine, err = publisher.New(h.server, fresh)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The fresh store adopts the producer baseline (prefix 1) only after
	// fencing; the baseline install is the handshake step.
	if err := release.AdoptBaseline(fresh, fFence(delivery.SessionNormal, 1), 1); err != nil {
		t.Fatalf("AdoptBaseline: %v", err)
	}
	// The unacknowledged batch 3 is not delivered by any recovery: the
	// producer prefix is 1, so only sequence 2 may recover.
	if _, err := release.RecoveryRelease(h.prod, fresh, fFence(delivery.SessionNormal, 1), seq3); !errors.Is(err, delivery.ErrState) {
		t.Fatalf("unACKed recovery: %v, want ErrState", err)
	}
	// Sequence 2 recovers without any payload transfer.
	releaseAck, err := release.RecoveryRelease(h.prod, fresh, fFence(delivery.SessionNormal, 1), seq2)
	if err != nil {
		t.Fatalf("RecoveryRelease: %v", err)
	}
	if releaseAck.RetiredPrefix != 2 || releaseAck.Batch != id2.Batch {
		t.Fatalf("recovery ack %+v", releaseAck)
	}
	// A stale fence never advances anything.
	stale := fFence(delivery.SessionNormal, 1)
	stale.SessionToken = []byte("stale")
	if _, err := fresh.RecordRecoveryRelease(stale, ack2); !errors.Is(err, delivery.ErrFenced) {
		t.Fatalf("stale fence recovery: %v, want ErrFenced", err)
	}
	// The producer finishes its outbox by the ack.
	if err := release.OnReleaseAck(h.prod, releaseAck); err != nil {
		t.Fatalf("OnReleaseAck: %v", err)
	}
	if got := h.prod.RetiredPrefix(); got != 2 {
		t.Fatalf("producer prefix %d, want 2", got)
	}
	// The unacknowledged batch 3 still flows to delivery on the recovered
	// store: it was SEALED, so its exact plan imports (issuer epoch
	// preserved, no new reservation) and publishes; then it releases in
	// order.
	ack3 := h.publishImported(t, seq3)
	if !ack3.Identity.SameIdentity(id3) {
		t.Fatalf("the imported ack changed the sealed identity")
	}
	h.cleanupProducer(t, seq3, ack3)
	h.releaseBatch(t, id3)
	if got := h.prod.RetiredPrefix(); got != 3 {
		t.Fatalf("producer prefix %d after batch 3", got)
	}
	// A conflicting local record blocks a recovery release without
	// advancing anything.
	conflict, _ := delivery.NewMemoryGateway(proto.ID16{45})
	if err := conflict.SetActiveBinding(1, fBindingDigest); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	if err := conflict.OpenSession(fFence(delivery.SessionNormal, 1)); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	seq4, manifest4 := h.driveToReady(t)
	frames4 := fFrames(t, seq4, proto.FamilyTx, proto.KindApplied, 1)
	id4 := h.driveToSealed(t, seq4, manifest4, fKeys(frames4))
	if err := conflict.ImportClosed(fFence(delivery.SessionNormal, 1),
		release.NewSealedPlan(id4, 1000+seq4, fStream, fKeys(frames4)), false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	if _, err := release.RecoveryRelease(h.prod, conflict, fFence(delivery.SessionNormal, 1), seq4); !errors.Is(err, delivery.ErrState) {
		t.Fatalf("recovery over the producer's own READY batch: %v, want ErrState", err)
	}
}

// Fault 4: a compatible replacement reads the capability matrix before
// any writable mutation and replays the old bytes; an incompatible
// persistent store format is rejected before a write even with empty
// payload and even after a completed drain.
func TestFault4CompatibleReplacementReplaysOldBytes(t *testing.T) {
	// The preflight matrix of the replacement builds.
	v1, err := compatibility.NewProtocolVersion("store.format.v1", nil, true)
	if err != nil {
		t.Fatalf("NewProtocolVersion: %v", err)
	}
	v2Compatible, err := compatibility.NewProtocolVersion("store.format.v2", []compatibility.VersionID{"store.format.v1"}, true)
	if err != nil {
		t.Fatalf("NewProtocolVersion: %v", err)
	}
	v3Incompatible, err := compatibility.NewProtocolVersion("store.format.v3", nil, true)
	if err != nil {
		t.Fatalf("NewProtocolVersion: %v", err)
	}
	// The replacement build runs its own matrix whose current (new-write)
	// version is its own format; the preflight questions run before any
	// writable mutation.
	compatibleBuild, err := compatibility.NewMatrix(v2Compatible, v1)
	if err != nil {
		t.Fatalf("NewMatrix: %v", err)
	}
	if err := compatibleBuild.CanRead("store.format.v1", "store.format.v2"); err != nil {
		t.Fatalf("compatible read: %v", err)
	}
	if err := compatibleBuild.CanReplay("store.format.v1", "store.format.v2"); err != nil {
		t.Fatalf("compatible replay: %v", err)
	}
	if err := compatibleBuild.CanWriteNew("store.format.v1", "store.format.v2", compatibility.ClaimStoreFormat); err != nil {
		t.Fatalf("compatible new write: %v", err)
	}
	// The incompatible persistent format is rejected before a write,
	// drain state notwithstanding.
	incompatibleBuild, err := compatibility.NewMatrix(v3Incompatible, v1)
	if err != nil {
		t.Fatalf("NewMatrix: %v", err)
	}
	err = incompatibleBuild.CanWriteNew("store.format.v1", "store.format.v3", compatibility.ClaimStoreFormat)
	var rejected *compatibility.ErrStoreTransitionRejected
	if !errors.As(err, &rejected) {
		t.Fatalf("incompatible store format: %v, want ErrStoreTransitionRejected", err)
	}

	// The compatible replacement replays the old bytes exactly: the
	// sealed plan republishes with identical ids and payloads.
	h := newHarness()
	seq, manifest := h.driveToReady(t)
	frames := fFrames(t, seq, proto.FamilyTx, proto.KindApplied, 2)
	id := h.driveToSealed(t, seq, manifest, fKeys(frames))
	ack := h.publishClosed(t, id, frames)
	h.cleanupProducer(t, seq, ack)
	h.releaseBatch(t, id)
	// A replacement gateway adopts the producer's retired prefix from the
	// current fenced owner; a replayed publish of the retired batch is
	// then refused with ALREADY_RETIRED and creates no keys — the rule a
	// replacement build must honor for retired history.
	replacement, err := delivery.NewMemoryGateway(proto.ID16{46})
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := replacement.SetActiveBinding(1, fBindingDigest); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	if err := replacement.OpenSession(fFence(delivery.SessionNormal, 1)); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if err := release.AdoptBaseline(replacement, fFence(delivery.SessionNormal, 1), 1); err != nil {
		t.Fatalf("AdoptBaseline: %v", err)
	}
	before := len(h.server.Messages())
	h.gw = replacement
	h.engine, err = publisher.New(h.server, replacement)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The batch is already retired on the producer: the replayed publish
	// is refused with ALREADY_RETIRED and creates no keys — exactly the
	// rule a replacement build must honor for retired history.
	if _, err := h.engine.Publish(fFence(delivery.SessionNormal, 1), h.target,
		publisher.Batch{Identity: id, Frames: frames}); !errors.Is(err, delivery.ErrAlreadyRetired) {
		t.Fatalf("replacement publish of a retired batch: %v, want ErrAlreadyRetired", err)
	}
	if got := len(h.server.Messages()); got != before {
		t.Fatalf("the replacement stored %d new messages", got-before)
	}
}

// The frontier fixture: 100 released batches, then a gateway store loss;
// batch 101 flows on the recovered store and a late publish of batch 50
// answers ALREADY_RETIRED without creating keys.
func TestFrontierLatePublishAfterRecovery(t *testing.T) {
	h := newHarness()
	for i := 0; i < 100; i++ {
		h.runOneBatch(t)
	}
	if got := h.prod.RetiredPrefix(); got != 100 {
		t.Fatalf("producer prefix %d after 100 batches", got)
	}
	lateSeq := uint64(50)
	lateID := sealedIdentity(manifestIdentity(lateSeq), fEpoch)
	lateFrames := fFrames(t, lateSeq, proto.FamilyTx, proto.KindApplied, 2)

	// Gateway store loss and recovery.
	fresh, err := delivery.NewMemoryGateway(proto.ID16{47})
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := fresh.SetActiveBinding(1, fBindingDigest); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	if err := fresh.OpenSession(fFence(delivery.SessionNormal, 1)); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if err := release.AdoptBaseline(fresh, fFence(delivery.SessionNormal, 1), 100); err != nil {
		t.Fatalf("AdoptBaseline: %v", err)
	}
	h.gw = fresh
	h.engine, err = publisher.New(h.server, fresh)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The late publish of sequence 50 is refused without new keys.
	before := len(h.server.Messages())
	if _, err := h.engine.Publish(fFence(delivery.SessionNormal, 1), h.target,
		publisher.Batch{Identity: lateID, Frames: lateFrames}); !errors.Is(err, delivery.ErrAlreadyRetired) {
		t.Fatalf("late publish of batch 50: %v, want ErrAlreadyRetired", err)
	}
	if got := len(h.server.Messages()); got != before {
		t.Fatalf("the late publish stored %d new messages", got-before)
	}
	// Batch 101 flows the ordinary path on the recovered store.
	seq101, manifest101 := h.driveToReady(t)
	if seq101 != 101 {
		t.Fatalf("allocated %d, want 101", seq101)
	}
	frames101 := fFrames(t, seq101, proto.FamilyTx, proto.KindApplied, 2)
	id101 := h.driveToSealed(t, seq101, manifest101, fKeys(frames101))
	ack101 := h.publishClosed(t, id101, frames101)
	h.cleanupProducer(t, seq101, ack101)
	h.releaseBatch(t, id101)
	if got := h.prod.RetiredPrefix(); got != 101 {
		t.Fatalf("producer prefix %d after batch 101", got)
	}
	if got := fresh.RetiredPrefix(fProducer, fRun); got != 101 {
		t.Fatalf("gateway prefix %d after batch 101", got)
	}
}

// Partial cleanup: the releasing gateway crashes mid-cleanup and loses
// its store. The producer's ACKED/outbox assertion completes the release
// through RecoveryRelease; missing keys after the crash never republish.
func TestPartialCleanupThenGatewayLoss(t *testing.T) {
	h := newHarness()
	seq, manifest := h.driveToReady(t)
	frames := fFrames(t, seq, proto.FamilyTx, proto.KindApplied, 3)
	id := h.driveToSealed(t, seq, manifest, fKeys(frames))
	ack := h.publishClosed(t, id, frames)
	h.cleanupProducer(t, seq, ack)

	// The release starts and one bounded cleanup step commits.
	if err := h.gw.BeginRelease(fFence(delivery.SessionNormal, 1), id.Batch); err != nil {
		t.Fatalf("BeginRelease: %v", err)
	}
	if more, err := h.gw.CleanupStep(fFence(delivery.SessionNormal, 1), id.Batch); err != nil || !more {
		t.Fatalf("first cleanup step: more=%v err=%v", more, err)
	}
	// The gateway store is lost here.
	fresh, err := delivery.NewMemoryGateway(proto.ID16{48})
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := fresh.SetActiveBinding(1, fBindingDigest); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	if err := fresh.OpenSession(fFence(delivery.SessionNormal, 1)); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if err := release.AdoptBaseline(fresh, fFence(delivery.SessionNormal, 1), 0); err != nil {
		t.Fatalf("AdoptBaseline: %v", err)
	}
	releaseAck, err := release.RecoveryRelease(h.prod, fresh, fFence(delivery.SessionNormal, 1), seq)
	if err != nil {
		t.Fatalf("RecoveryRelease: %v", err)
	}
	if releaseAck.RetiredPrefix != seq {
		t.Fatalf("prefix %d, want %d", releaseAck.RetiredPrefix, seq)
	}
	if err := release.OnReleaseAck(h.prod, releaseAck); err != nil {
		t.Fatalf("OnReleaseAck: %v", err)
	}
	// The payload was deleted before the crash: nothing republished, and
	// the producer holds only the compact prefix.
	if rec, ok := h.prod.Batch(seq); ok {
		t.Fatalf("the producer kept the retired record: %+v", rec)
	}
}

// The composed error classification: publisher transient and destination
// unavailable errors retry; immutable conflicts hold the frontier; local
// fatal and remote outage prescribe different owner actions.
func TestComposedErrorClassification(t *testing.T) {
	h := newHarness()
	cases := []struct {
		err    error
		class  derrors.Class
		action derrors.Action
	}{
		{fmt.Errorf("publish: %w", publisher.ErrTransient), derrors.ClassTransient, derrors.ActionRetryBackoff},
		{fmt.Errorf("route: %w", publisher.ErrNoStream), derrors.ClassConfigBlocked, derrors.ActionStopPublication},
		{fmt.Errorf("plan: %w", delivery.ErrImmutableConflict), derrors.ClassPoison, derrors.ActionHoldFrontier},
		{fmt.Errorf("old: %w", delivery.ErrAlreadyRetired), derrors.ClassPoison, derrors.ActionHoldFrontier},
		{fmt.Errorf("wal: %w", derrors.ErrLocalDiskFatal), derrors.ClassLocalFatal, derrors.ActionStopLocalOwner},
		{fmt.Errorf("gateway: %w", derrors.ErrLinkLost), derrors.ClassRemoteOutage, derrors.ActionContinueLocal},
	}
	for _, tc := range cases {
		class, known := h.classify.Classify(tc.err)
		if !known || class != tc.class {
			t.Errorf("Classify(%v) = %s known=%v, want %s", tc.err, class, known, tc.class)
			continue
		}
		if got := h.classify.MustClassify(tc.err); got != tc.action {
			t.Errorf("action of %v = %s, want %s", tc.err, got, tc.action)
		}
	}
	// An unknown error prescribes diagnosis, never a silent retry.
	if got := h.classify.MustClassify(errors.New("mystery")); got != derrors.ActionDiagnose {
		t.Fatalf("unknown error prescribes %s", got)
	}
}

// The maintenance engines compose with the destination adapter: a D77
// mismatch of the desired target blocks SwitchPrepared through the real
// destination check.
func TestMaintenanceComposesWithDestinationCheck(t *testing.T) {
	h := newHarness()
	// Drain and seal through the composed stack.
	if err := h.prod.SetMode(delivery.ModeDrain); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	h.runOneBatch(t)
	verifier, remote := newDestVerifierWithRemote(t)
	engine := h.maintenanceStack(verifier)
	if _, err := engine.SealToDrained(context.Background(), fFence(delivery.SessionNormal, 1), proto.ID16{9}); err != nil {
		t.Fatalf("SealToDrained: %v", err)
	}
	// The desired binding of the next generation.
	desired := fBinding()
	desired.Generation = 2
	desired.Stream = "EXTRACTOR_ETH_V4"
	desired.SubjectPrefix = "evm.v4.eth"
	digest, err := desired.DescriptorDigestComputed()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	desired.DescriptorDigest = digest
	verifier.add(desired)
	// The desired stream carries wrong metadata: the D77 check blocks
	// the preparation through the composed adapter.
	if err := engine.EnterAwaitSwitch(desired.DescriptorDigest); err != nil {
		t.Fatalf("EnterAwaitSwitch: %v", err)
	}
	if _, err := engine.PrepareSwitch(context.Background(), fFence(delivery.SessionNormal, 1),
		delivery.BindingView{
			DestinationID: desired.DestinationID, Generation: desired.Generation,
			Network: desired.Network, ExpectedAccount: desired.ExpectedAccount,
			Stream: desired.Stream, SubjectPrefix: desired.SubjectPrefix,
			DescriptorDigest: desired.DescriptorDigest,
		}, proto.ID16{5}); err == nil {
		t.Fatalf("PrepareSwitch succeeded against a mismatching target")
	}
	if phase, _ := h.gw.SwitchState(); phase != delivery.SwitchAwait {
		t.Fatalf("a blocked check left phase %s", phase)
	}
	// Fix the environment: the same switch now prepares.
	fixed := destination.StreamInfo{Name: desired.Stream, Subjects: []string{"evm.v4.eth.>"}}
	networkDigest, _ := desired.NetworkDigest()
	fixed.Metadata = map[string]string{
		destination.MetadataNetworkDigest: hexOf(networkDigest[:]),
		destination.MetadataDestinationID: hexOf(desired.DestinationID[:]),
	}
	remote.SetStream(fixed)
	prepared, err := engine.PrepareSwitch(context.Background(), fFence(delivery.SessionNormal, 1),
		delivery.BindingView{
			DestinationID: desired.DestinationID, Generation: desired.Generation,
			Network: desired.Network, ExpectedAccount: desired.ExpectedAccount,
			Stream: desired.Stream, SubjectPrefix: desired.SubjectPrefix,
			DescriptorDigest: desired.DescriptorDigest,
		}, proto.ID16{5})
	if err != nil {
		t.Fatalf("PrepareSwitch after the fix: %v", err)
	}
	if prepared.NewDigest != desired.DescriptorDigest {
		t.Fatalf("prepared receipt binds %v", prepared.NewDigest)
	}
}

// wrongDigest and wrongID are deliberately wrong metadata values.
var (
	wrongDigest = func() proto.Hash32 { var h proto.Hash32; h[0] = 1; return h }()
	wrongID     = func() proto.Hash32 { var h proto.Hash32; h[0] = 8; return h }()
)

// newDestVerifierWithRemote builds the destination adapter with its
// remote; the desired stream initially carries wrong metadata.
func newDestVerifierWithRemote(t *testing.T) (*destVerifier, *destination.MemoryRemote) {
	t.Helper()
	remote, err := destination.NewMemoryRemote("IDX_ETH", destination.StreamInfo{
		Name: "EXTRACTOR_ETH_V4", Subjects: []string{"evm.v4.eth.>"},
		Metadata: map[string]string{
			destination.MetadataNetworkDigest: hexOf(wrongDigest[:]),
			destination.MetadataDestinationID: hexOf(wrongID[:]),
		},
	})
	if err != nil {
		t.Fatalf("NewMemoryRemote: %v", err)
	}
	return newDestVerifier(remote), remote
}
