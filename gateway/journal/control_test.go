package journal

import (
	"crypto/sha256"
	"errors"
	"strconv"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// ackedBatch drives one batch to a durable NATS_ACKED marker.
func ackedBatch(t *testing.T, j *Journal, seq uint64) planFixture {
	t.Helper()
	pf := closedPlan(t, j, seq)
	if err := j.MarkNATSACKed(pf.id, pf.planDigest, pf.res.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	return pf
}

func TestBatchStateReportsEveryPhase(t *testing.T) {
	j, _ := newJournal(t)

	// A batch the store never saw is NOT_FOUND (State 0).
	page, err := j.BatchState(testBatch(1), [32]byte{}, nil, 8)
	if err != nil || page.State != 0 || page.Ack != nil || len(page.Cursors) != 0 {
		t.Fatalf("unknown batch: %+v %v", page, err)
	}

	// A PREPARING batch has no plan yet: the state is reported as it is.
	manifest := []byte("state manifest")
	digest := sha256.Sum256(manifest)
	id := testBatch(1)
	if _, err := j.BeginPrepare(id, digest, 2, uint64(len(manifest)), PrepareNormal, nil); err != nil {
		t.Fatal(err)
	}
	page, err = j.BatchState(id, [32]byte{1}, nil, 8)
	if err != nil || page.State != StatePreparing || page.Ack != nil || len(page.Cursors) != 0 {
		t.Fatalf("preparing: %+v %v", page, err)
	}
	chunkify(t, j, id, digest, 0, manifest, 4)
	res, err := j.FinalizePrepare(id, digest)
	if err != nil {
		t.Fatal(err)
	}

	// PREPARED binds the plan digest of its reservation.
	plan := PlanDigest(res.Seed)
	if page, err = j.BatchState(id, [32]byte{1}, nil, 8); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("prepared with another plan: got %+v %v, want ErrIdentityMismatch", page, err)
	}
	if page, err = j.BatchState(id, plan, nil, 8); err != nil || page.State != StatePrepared || page.Ack != nil || len(page.Cursors) != 0 {
		t.Fatalf("prepared: %+v %v", page, err)
	}

	// The later states walk a fresh store whose only batch is sequence 1:
	// release is strictly ordered, so the retire commit below needs it.
	j, _ = newJournal(t)

	// CLOSED lists its declared frames and carries no ack yet.
	pf := closedPlan(t, j, 1)
	if _, err = j.BatchState(pf.id, [32]byte{1}, nil, 8); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("closed with another plan: got %v, want ErrIdentityMismatch", err)
	}
	page, err = j.BatchState(pf.id, pf.planDigest, nil, 8)
	if err != nil || page.State != StateClosed || page.Ack != nil || len(page.Cursors) != 3 {
		t.Fatalf("closed: %+v %v", page, err)
	}

	// NATS_ACKED is the first state whose IngestAck is durable.
	if err := j.MarkNATSACKed(pf.id, pf.planDigest, pf.res.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	page, err = j.BatchState(pf.id, pf.planDigest, nil, 8)
	if err != nil || page.State != StateNatsAcked || page.Ack == nil {
		t.Fatalf("nats acked: %+v %v", page, err)
	}
	want := IngestAck{Batch: pf.id, ManifestDigest: pf.res.ManifestDigest, PlanDigest: pf.planDigest,
		LayoutDigest: pf.layout, BindingDigest: testBinding, IssuerEpoch: j.epoch}
	if *page.Ack != want {
		t.Fatalf("ingest ack = %+v, want %+v", *page.Ack, want)
	}
	// RELEASING still holds the marker, so the ack is still durable.
	if err := j.BeginRelease(pf.id, pf.planDigest); err != nil {
		t.Fatal(err)
	}
	if page, err = j.BatchState(pf.id, pf.planDigest, nil, 8); err != nil || page.State != StateReleasing || page.Ack == nil {
		t.Fatalf("releasing: %+v %v", page, err)
	}

	// A retired batch is gone: no plan is compared, no ack is returned.
	if done, err := j.ReleaseCleanup(pf.id, 50); err != nil || !done {
		t.Fatalf("cleanup: done=%v err=%v", done, err)
	}
	if _, err := j.CompleteRelease(pf.id); err != nil {
		t.Fatal(err)
	}
	page, err = j.BatchState(pf.id, [32]byte{1}, nil, 8)
	if err != nil || page.State != StateRetired || page.Ack != nil || len(page.Cursors) != 0 {
		t.Fatalf("retired: %+v %v", page, err)
	}
	if _, err := j.BatchState(pf.id, pf.planDigest, nil, 0); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("zero limit: got %v, want ErrStateConflict", err)
	}
}

func TestBatchStateCursorPages(t *testing.T) {
	j, _ := newJournal(t)
	pf := closedPlan(t, j, 1)

	// One cursor per page walks every declared frame exactly once.
	var all []FrameCursor
	var after *FrameAddr
	pages := 0
	for {
		page, err := j.BatchState(pf.id, pf.planDigest, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		all = append(all, page.Cursors...)
		if !page.More {
			break
		}
		addr := page.Cursors[len(page.Cursors)-1].Addr
		after = &FrameAddr{Ordinal: addr.Ordinal, FragmentIndex: addr.FragmentIndex, FrameIndex: addr.FrameIndex}
	}
	full, err := j.frameCursors(pf.id)
	if err != nil || pages != 3 || len(all) != len(full.Frames) {
		t.Fatalf("pages = %d, cursors = %d, want 3 pages and %d cursors (%v)", pages, len(all), len(full.Frames), err)
	}
	for i := range all {
		if all[i] != full.Frames[i] {
			t.Fatalf("cursor %d = %+v, want %+v", i, all[i], full.Frames[i])
		}
	}
	// The order is (ordinal, fragment, frame).
	if all[1].Addr.Ordinal != 1 || all[1].Addr.FragmentIndex != 0 || all[2].Addr.FrameIndex != 1 {
		t.Fatalf("cursor order: %+v", all)
	}
	// A page larger than the batch reports no continuation.
	page, err := j.BatchState(pf.id, pf.planDigest, nil, 64)
	if err != nil || page.More || len(page.Cursors) != 3 {
		t.Fatalf("whole batch: %+v %v", page, err)
	}
	// A page may resume past the last frame.
	last := all[2].Addr
	if page, err = j.BatchState(pf.id, pf.planDigest, &last, 64); err != nil || page.More || len(page.Cursors) != 0 {
		t.Fatalf("page after the last frame: %+v %v", page, err)
	}
	// A continuation may start anywhere: from the first frame of the second
	// entry it lists exactly the frames after it.
	middle := all[1].Addr
	if page, err = j.BatchState(pf.id, pf.planDigest, &middle, 64); err != nil || page.More || len(page.Cursors) != 1 ||
		page.Cursors[0] != all[2] {
		t.Fatalf("page from the middle: %+v %v", page, err)
	}
	// A position that names no declared frame still orders correctly: one
	// past the last frame of a fragment.
	past := FrameAddr{Ordinal: all[0].Addr.Ordinal, FragmentIndex: all[0].Addr.FragmentIndex + 1}
	if page, err = j.BatchState(pf.id, pf.planDigest, &past, 64); err != nil || len(page.Cursors) != 2 ||
		page.Cursors[0] != all[1] || page.Cursors[1] != all[2] {
		t.Fatalf("page from an undeclared position: %+v %v", page, err)
	}
}

func TestReleaseRetiresNextAckedBatch(t *testing.T) {
	j, _ := newJournal(t)
	first := ackedBatch(t, j, 1)
	second := closedPlan(t, j, 2) // CLOSED, not acknowledged yet

	// Release is strictly ordered: the second batch waits for the first.
	if _, err := j.Release(second.id, second.res.ManifestDigest, second.planDigest, 50); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("release out of order: got %v, want ErrStateConflict", err)
	}
	// The whole identity binds the release: both digests are verified.
	if _, err := j.Release(first.id, [32]byte{1}, first.planDigest, 50); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("release with another manifest: got %v, want ErrIdentityMismatch", err)
	}
	if _, err := j.Release(first.id, first.res.ManifestDigest, [32]byte{1}, 50); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("release with another plan: got %v, want ErrIdentityMismatch", err)
	}
	if _, err := j.Release(first.id, first.res.ManifestDigest, first.planDigest, 0); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("zero step: got %v, want ErrStateConflict", err)
	}
	// Nothing above changed the store.
	if prefix, err := j.RetiredPrefix(first.id); err != nil || prefix != 0 {
		t.Fatalf("prefix after refusals = %d %v", prefix, err)
	}
	if info, err := j.Batch(first.id); err != nil || info.State != StateNatsAcked {
		t.Fatalf("state after refusals: %+v %v", info, err)
	}

	// Bounded steps still end in one answer with the retired prefix.
	prefix, err := j.Release(first.id, first.res.ManifestDigest, first.planDigest, 1)
	if err != nil || prefix != 1 {
		t.Fatalf("release: prefix=%d err=%v", prefix, err)
	}
	info, err := j.Batch(first.id)
	if err != nil || info.State != StateRetired {
		t.Fatalf("state after release: %+v %v", info, err)
	}
	// A retry answers the durable prefix again: the gateway never rolls back.
	if prefix, err = j.Release(first.id, first.res.ManifestDigest, first.planDigest, 50); err != nil || prefix != 1 {
		t.Fatalf("release retry: prefix=%d err=%v", prefix, err)
	}
	// The retired sequence creates no keys.
	if _, err := j.BeginPrepare(first.id, [32]byte{5}, 1, 1, PrepareNormal, nil); !errors.Is(err, ErrAlreadyRetired) {
		t.Fatalf("prepare of a retired batch: got %v, want ErrAlreadyRetired", err)
	}

	// A batch that is not NATS_ACKED never releases.
	if _, err := j.Release(second.id, second.res.ManifestDigest, second.planDigest, 50); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("release of a CLOSED batch: got %v, want ErrStateConflict", err)
	}
	if err := j.MarkNATSACKed(second.id, second.planDigest, second.res.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	// A crashed cleanup resumes from the remaining keys.
	if err := j.BeginRelease(second.id, second.planDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := j.ReleaseCleanup(second.id, 1); err != nil {
		t.Fatal(err)
	}
	if prefix, err = j.Release(second.id, second.res.ManifestDigest, second.planDigest, 50); err != nil || prefix != 2 {
		t.Fatalf("release after a crashed cleanup: prefix=%d err=%v", prefix, err)
	}
}

func TestReleaseOfUnknownNextBatch(t *testing.T) {
	j, _ := newJournal(t)
	if _, err := j.Release(testBatch(1), [32]byte{}, [32]byte{}, 50); !errors.Is(err, ErrBatchNotFound) {
		t.Fatalf("release of an unknown batch: got %v, want ErrBatchNotFound", err)
	}
}

func sampleAck(id BatchID, manifest, plan, layout [32]byte) (IngestAck, OutboxAssertion) {
	ack := IngestAck{Batch: id, ManifestDigest: manifest, PlanDigest: plan, LayoutDigest: layout,
		BindingDigest: testBinding, IssuerEpoch: [16]byte{0xCD}}
	return ack, OutboxAssertion{Batch: id, PlanDigest: plan, BindingDigest: testBinding}
}

func TestRecoveryReleaseAdvancesExactlyNextSequence(t *testing.T) {
	j, st := newJournal(t)
	id := testBatch(1)
	ack, assertion := sampleAck(id, [32]byte{1}, [32]byte{2}, [32]byte{3})

	// The assertion and the ack must name one identity.
	other, otherAssertion := sampleAck(testBatch(2), [32]byte{1}, [32]byte{2}, [32]byte{3})
	if _, err := j.RecoveryRelease(id, other, otherAssertion); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("assertion of another batch: got %v, want ErrIdentityMismatch", err)
	}
	foreign := assertion
	foreign.PlanDigest = [32]byte{9}
	if _, err := j.RecoveryRelease(id, ack, foreign); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("assertion with another plan: got %v, want ErrIdentityMismatch", err)
	}
	zero := ack
	zero.IssuerEpoch = [16]byte{}
	if _, err := j.RecoveryRelease(id, zero, assertion); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("zero issuer epoch: got %v, want ErrIdentityMismatch", err)
	}
	// Only the active destination is released.
	elsewhere := ack
	elsewhere.BindingDigest = [32]byte{7}
	elsewhereAssertion := assertion
	elsewhereAssertion.BindingDigest = [32]byte{7}
	if _, err := j.RecoveryRelease(id, elsewhere, elsewhereAssertion); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("ack of another binding: got %v, want ErrBindingMismatch", err)
	}
	// An arbitrary jump never recovers.
	jump, jumpAssertion := sampleAck(testBatch(4), [32]byte{1}, [32]byte{2}, [32]byte{3})
	if _, err := j.RecoveryRelease(jump.Batch, jump, jumpAssertion); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("jump: got %v, want ErrStateConflict", err)
	}
	if prefix, _ := j.RetiredPrefix(id); prefix != 0 {
		t.Fatalf("prefix after refusals = %d", prefix)
	}

	// The missing batch recovers without any payload.
	prefix, err := j.RecoveryRelease(id, ack, assertion)
	if err != nil || prefix != 1 {
		t.Fatalf("recovery release: prefix=%d err=%v", prefix, err)
	}
	info, err := j.Batch(id)
	if err != nil || info.State != StateRetired || info.Reservation != nil {
		t.Fatalf("recovered batch: %+v %v", info, err)
	}
	// A retry answers the same prefix; a different assertion is a conflict.
	if prefix, err = j.RecoveryRelease(id, ack, assertion); err != nil || prefix != 1 {
		t.Fatalf("recovery retry: prefix=%d err=%v", prefix, err)
	}
	changed := ack
	changed.LayoutDigest = [32]byte{9}
	if _, err = j.RecoveryRelease(id, changed, assertion); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("recovery retry with another ack: got %v, want ErrImmutableConflict", err)
	}
	// The accepted assertion is durable.
	again := reopen(t, st)
	if prefix, err = again.RetiredPrefix(id); err != nil || prefix != 1 {
		t.Fatalf("prefix after reopen: %d %v", prefix, err)
	}
	if prefix, err = again.RecoveryRelease(id, ack, assertion); err != nil || prefix != 1 {
		t.Fatalf("recovery retry after reopen: prefix=%d err=%v", prefix, err)
	}
	if _, err = again.RecoveryRelease(id, changed, assertion); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("another assertion after reopen: got %v, want ErrImmutableConflict", err)
	}
}

func TestRecoveryReleaseRefusesLocalRecord(t *testing.T) {
	j, _ := newJournal(t)
	// A local record of the next sequence conflicts and is never deleted.
	closed := closedPlan(t, j, 1)
	ack, assertion := sampleAck(closed.id, closed.res.ManifestDigest, closed.planDigest, closed.layout)
	if _, err := j.RecoveryRelease(closed.id, ack, assertion); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("recovery over a local record: got %v, want ErrImmutableConflict", err)
	}
	info, err := j.Batch(closed.id)
	if err != nil || info.State != StateClosed || info.Reservation == nil {
		t.Fatalf("local record after the refusal: %+v %v", info, err)
	}
	if prefix, _ := j.RetiredPrefix(closed.id); prefix != 0 {
		t.Fatalf("prefix after the refusal = %d", prefix)
	}
	// A batch retired through Release answers the prefix to a later recovery
	// assertion: the gateway is one release ahead.
	if err := j.MarkNATSACKed(closed.id, closed.planDigest, closed.res.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	if prefix, err := j.Release(closed.id, closed.res.ManifestDigest, closed.planDigest, 50); err != nil || prefix != 1 {
		t.Fatalf("release: prefix=%d err=%v", prefix, err)
	}
	if prefix, err := j.RecoveryRelease(closed.id, ack, assertion); err != nil || prefix != 1 {
		t.Fatalf("recovery after a normal release: prefix=%d err=%v", prefix, err)
	}
}

var (
	testDestination = [16]byte{7}
	testChain       = sha256.Sum256([]byte("chain id"))
	testGenesis     = sha256.Sum256([]byte("genesis"))
	testDrain       = [16]byte{9}
)

// newBoundJournal opens a journal that knows its active binding identity.
func newBoundJournal(t *testing.T) (*Journal, *storage.MemoryStorage) {
	t.Helper()
	st := storage.NewMemory()
	b := Binding{DestinationID: testDestination, Generation: 3, ChainID: testChain,
		GenesisHash: testGenesis, DescriptorDigest: testBinding}
	j, err := Open(st, Options{BindingDigest: testBinding, Binding: &b})
	if err != nil {
		t.Fatal(err)
	}
	return j, st
}

// nextBinding is a legal successor descriptor of the active test binding.
// Its digest depends on the generation, so each generation is another
// binding.
func nextBinding(gen uint64) Binding {
	return Binding{DestinationID: testDestination, Generation: gen, ChainID: testChain,
		GenesisHash: testGenesis, DescriptorDigest: sha256.Sum256([]byte("next binding " + strconv.FormatUint(gen, 10)))}
}

// sealRun seals admission generation 1 of the test run at a highwater.
func sealRun(t *testing.T, j *Journal, highwater uint64) Seal {
	t.Helper()
	seal, err := j.SealGeneration(testBatch(0), SealIntent{BindingGeneration: 3, AdmissionGeneration: 1,
		DrainID: testDrain, Highwater: highwater})
	if err != nil {
		t.Fatal(err)
	}
	return seal
}

func TestSealGenerationBindsWholeIntent(t *testing.T) {
	j, st := newBoundJournal(t)
	run := testBatch(0)
	intent := SealIntent{BindingGeneration: 3, AdmissionGeneration: 1, DrainID: testDrain, Highwater: 1}

	if _, err := j.SealGeneration(run, SealIntent{BindingGeneration: 3, AdmissionGeneration: 1, Highwater: 1}); !errors.Is(err, ErrInvalidIntent) {
		t.Fatalf("zero drain id: got %v, want ErrInvalidIntent", err)
	}
	// The binding generation must be the active one.
	if _, err := j.SealGeneration(run, SealIntent{BindingGeneration: 4, AdmissionGeneration: 1,
		DrainID: testDrain, Highwater: 0}); !errors.Is(err, ErrBindingGeneration) {
		t.Fatalf("seal with another binding generation: got %v, want ErrBindingGeneration", err)
	}
	// The retired prefix must equal the highwater.
	pf := ackedBatch(t, j, 1)
	if _, err := j.SealGeneration(run, intent); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("seal above the prefix: got %v, want ErrStateConflict", err)
	}
	if _, err := j.Release(pf.id, pf.res.ManifestDigest, pf.planDigest, 50); err != nil {
		t.Fatal(err)
	}

	seal, err := j.SealGeneration(run, intent)
	if err != nil {
		t.Fatal(err)
	}
	want := Seal{DrainID: testDrain, BindingDigest: testBinding, AdmissionGeneration: 1, Highwater: 1,
		IssuerEpoch: j.epoch, BindingGeneration: 3}
	if seal != want {
		t.Fatalf("seal = %+v, want %+v", seal, want)
	}
	// The retry returns the same SealAck; one matching number is not enough.
	if again, err := j.SealGeneration(run, intent); err != nil || again != seal {
		t.Fatalf("seal retry: %+v %v", again, err)
	}
	changed := intent
	changed.Highwater = 2
	if _, err := j.SealGeneration(run, changed); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("same drain with another highwater: got %v, want ErrImmutableConflict", err)
	}
	changed = intent
	changed.DrainID = [16]byte{8}
	if _, err := j.SealGeneration(run, changed); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("another drain of a sealed generation: got %v, want ErrImmutableConflict", err)
	}
	// The seal is durable.
	again := reopen(t, st)
	if saved, ok, err := again.SealedGeneration(run); err != nil || !ok || saved != seal {
		t.Fatalf("seal after reopen: %+v %v %v", saved, ok, err)
	}
	if saved, err := again.SealGeneration(run, intent); err != nil || saved != seal {
		t.Fatalf("seal retry after reopen: %+v %v", saved, err)
	}
}

func TestSealAcceptsEmptyFrontierAndResealsResumedGeneration(t *testing.T) {
	j, _ := newBoundJournal(t)
	run := testBatch(0)

	// A run that never created a batch seals at the empty frontier.
	seal, err := j.SealGeneration(run, SealIntent{BindingGeneration: 3, AdmissionGeneration: 1,
		DrainID: testDrain, Highwater: 0})
	if err != nil {
		t.Fatalf("seal of an empty run: %v", err)
	}
	resume := ResumeIntent{ResumeID: [16]byte{5}, BindingDigest: testBinding,
		NextAdmissionGeneration: 2, OldSeal: seal}
	if _, err := j.Resume(run, resume); err != nil {
		t.Fatal(err)
	}
	// The old generation stays sealed; the resumed one is sealed by the
	// next drain, which retires the resume intent.
	next := SealIntent{BindingGeneration: 3, AdmissionGeneration: 2, DrainID: [16]byte{6}, Highwater: 0}
	old := next
	old.AdmissionGeneration = 1
	if _, err := j.SealGeneration(run, old); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("seal of the old generation after a resume: got %v, want ErrImmutableConflict", err)
	}
	resealed, err := j.SealGeneration(run, next)
	if err != nil || resealed.AdmissionGeneration != 2 {
		t.Fatalf("reseal: %+v %v", resealed, err)
	}
	if _, ok, err := j.ResumedGeneration(run); err != nil || ok {
		t.Fatalf("resume record after the reseal: ok=%v %v", ok, err)
	}
	// The retired resume intent is rejected.
	if _, err := j.Resume(run, resume); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("retired resume intent: got %v, want ErrIdentityMismatch", err)
	}
}

func TestPrepareSwitchChecksContinuity(t *testing.T) {
	j, _ := newBoundJournal(t)
	run := testBatch(0)
	desired := nextBinding(4)
	switchID := [16]byte{0xAB}

	// A switch continues a sealed generation.
	if _, err := j.PrepareSwitch(run, Seal{}, desired, switchID); !errors.Is(err, ErrDrainRequired) {
		t.Fatalf("switch without a seal: got %v, want ErrDrainRequired", err)
	}
	seal := sealRun(t, j, 0)
	// The whole old seal must match.
	oldSeal := seal
	oldSeal.Highwater = 1
	if _, err := j.PrepareSwitch(run, oldSeal, desired, switchID); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("switch with another old seal: got %v, want ErrIdentityMismatch", err)
	}
	// The desired descriptor continues the same destination line and the
	// same network at exactly the next generation.
	if _, err := j.PrepareSwitch(run, seal, j.bindingLocked(), switchID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("desired equals active: got %v, want ErrStateConflict", err)
	}
	other := desired
	other.DestinationID = [16]byte{8}
	if _, err := j.PrepareSwitch(run, seal, other, switchID); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("another destination: got %v, want ErrBindingMismatch", err)
	}
	other = desired
	other.GenesisHash = sha256.Sum256([]byte("another genesis"))
	if _, err := j.PrepareSwitch(run, seal, other, switchID); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("another network: got %v, want ErrBindingMismatch", err)
	}
	for _, gen := range []uint64{2, 5} {
		if _, err := j.PrepareSwitch(run, seal, nextBinding(gen), switchID); !errors.Is(err, ErrBindingGeneration) {
			t.Fatalf("generation %d: got %v, want ErrBindingGeneration", gen, err)
		}
	}

	prepared, err := j.PrepareSwitch(run, seal, desired, switchID)
	if err != nil {
		t.Fatal(err)
	}
	want := SwitchPrepared{SwitchID: switchID, OldDigest: testBinding, NewDigest: desired.DescriptorDigest,
		DrainID: testDrain, AdmissionGeneration: 1, IssuerEpoch: j.epoch}
	if prepared != want {
		t.Fatalf("prepared = %+v, want %+v", prepared, want)
	}
	// The active binding is still the old one.
	if cur, ok := j.ActiveBinding(); !ok || cur != j.bindingLocked() || cur.DescriptorDigest != testBinding {
		t.Fatalf("active binding after prepare: %+v %v", cur, ok)
	}
	// A retry of the same intent returns the same receipt.
	if again, err := j.PrepareSwitch(run, seal, desired, switchID); err != nil || again != prepared {
		t.Fatalf("prepare retry: %+v %v", again, err)
	}
	// The same switch id must carry the same intent, and a new switch never
	// jumps an unfinished one.
	another := desired
	another.DescriptorDigest = sha256.Sum256([]byte("another descriptor of the same generation"))
	if _, err := j.PrepareSwitch(run, seal, another, switchID); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("same id with another descriptor: got %v, want ErrImmutableConflict", err)
	}
	if _, err := j.PrepareSwitch(run, seal, desired, [16]byte{0xAC}); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("another switch id: got %v, want ErrImmutableConflict", err)
	}
	// The committed switch keeps refusing a new switch id.
	if _, err := j.CommitSwitch(switchID, prepared, desired.DescriptorDigest, desired.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := j.PrepareSwitch(run, seal, desired, [16]byte{0xAC}); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("another switch id after the commit: got %v, want ErrImmutableConflict", err)
	}
}

func TestPrepareSwitchNeedsSealNotResumed(t *testing.T) {
	j, _ := newBoundJournal(t)
	run := testBatch(0)
	seal := sealRun(t, j, 0)
	if _, err := j.Resume(run, ResumeIntent{ResumeID: [16]byte{5}, BindingDigest: testBinding,
		NextAdmissionGeneration: 2, OldSeal: seal}); err != nil {
		t.Fatal(err)
	}
	// The sealed generation was already resumed: a switch needs a new drain.
	if _, err := j.PrepareSwitch(run, seal, nextBinding(4), [16]byte{0xAB}); !errors.Is(err, ErrDrainRequired) {
		t.Fatalf("switch after a resume: got %v, want ErrDrainRequired", err)
	}
}

// A switch is refused when the active binding identity is unknown: its
// continuity checks cannot run.
func TestPrepareSwitchNeedsKnownActiveBinding(t *testing.T) {
	j, _ := newJournal(t)
	run := testBatch(0)
	seal, err := j.SealGeneration(run, SealIntent{BindingGeneration: 1, AdmissionGeneration: 1,
		DrainID: testDrain, Highwater: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.PrepareSwitch(run, seal, nextBinding(2), [16]byte{0xAB}); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("switch without a known binding: got %v, want ErrBindingMismatch", err)
	}
	if _, known := j.ActiveBinding(); known {
		t.Fatal("a store opened without a binding identity must not claim one")
	}
}

func TestCommitSwitchActivatesNewBinding(t *testing.T) {
	j, st := newBoundJournal(t)
	run := testBatch(0)
	desired := nextBinding(4)
	switchID := [16]byte{0xAB}
	seal := sealRun(t, j, 0)
	prepared, err := j.PrepareSwitch(run, seal, desired, switchID)
	if err != nil {
		t.Fatal(err)
	}

	// The exact receipt and the producer's binding commit are both required.
	if _, err := j.CommitSwitch([16]byte{0xAC}, prepared, desired.DescriptorDigest, 4); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("another switch id: got %v, want ErrImmutableConflict", err)
	}
	other := prepared
	other.NewDigest = [32]byte{1}
	if _, err := j.CommitSwitch(switchID, other, desired.DescriptorDigest, 4); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("another receipt: got %v, want ErrImmutableConflict", err)
	}
	if _, err := j.CommitSwitch(switchID, prepared, [32]byte{1}, 4); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("commit of another digest: got %v, want ErrIdentityMismatch", err)
	}
	if _, err := j.CommitSwitch(switchID, prepared, desired.DescriptorDigest, 5); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("commit of another generation: got %v, want ErrIdentityMismatch", err)
	}
	if digest := j.BindingDigest(); digest != testBinding {
		t.Fatalf("binding changed before the commit: %x", digest)
	}

	committed, err := j.CommitSwitch(switchID, prepared, desired.DescriptorDigest, desired.Generation)
	if err != nil || committed != prepared {
		t.Fatalf("commit: %+v %v, want %+v", committed, err, prepared)
	}
	if cur, ok := j.ActiveBinding(); !ok || cur != desired || j.BindingDigest() != desired.DescriptorDigest {
		t.Fatalf("active binding after the commit: %+v %v, want %+v", cur, ok, desired)
	}
	// The durable receipt answers a retry unconditionally.
	if again, err := j.CommitSwitch(switchID, prepared, [32]byte{}, 0); err != nil || again != prepared {
		t.Fatalf("commit retry: %+v %v", again, err)
	}
	// The new binding is durable: a stale option never reverts it.
	again, err := Open(st, Options{BindingDigest: testBinding})
	if err != nil {
		t.Fatal(err)
	}
	if cur, ok := again.ActiveBinding(); !ok || cur != desired || again.BindingDigest() != desired.DescriptorDigest {
		t.Fatalf("active binding after reopen: %+v %v, want %+v", cur, ok, desired)
	}
	if cur, ok, err := again.CurrentSwitch(); err != nil || !ok || cur.Phase != SwitchCommittedPhase || cur.SwitchPrepared != prepared {
		t.Fatalf("switch after reopen: %+v %v %v", cur, ok, err)
	}
	// A later batch is bound to the new binding.
	pf := closedPlan(t, again, 1)
	if pf.res.BindingDigest != desired.DescriptorDigest {
		t.Fatalf("reservation binding after the switch: %x", pf.res.BindingDigest)
	}
}

func TestCommitSwitchNeedsPreparedReceipt(t *testing.T) {
	j, _ := newBoundJournal(t)
	if _, err := j.CommitSwitch([16]byte{1}, SwitchPrepared{}, [32]byte{}, 0); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("commit without a prepared switch: got %v, want ErrStateConflict", err)
	}
}

func TestResumeOpensExactlyNextAdmissionGeneration(t *testing.T) {
	j, st := newBoundJournal(t)
	run := testBatch(0)
	intent := ResumeIntent{ResumeID: [16]byte{5}, BindingDigest: testBinding, NextAdmissionGeneration: 2}

	// Resume needs a sealed generation and a set identity.
	if _, err := j.Resume(run, ResumeIntent{ResumeID: [16]byte{5}, BindingDigest: testBinding,
		NextAdmissionGeneration: 2}); !errors.Is(err, ErrDrainRequired) {
		t.Fatalf("resume without a seal: got %v, want ErrDrainRequired", err)
	}
	if _, err := j.Resume(run, ResumeIntent{NextAdmissionGeneration: 2}); !errors.Is(err, ErrInvalidIntent) {
		t.Fatalf("zero resume id: got %v, want ErrInvalidIntent", err)
	}
	seal := sealRun(t, j, 0)
	intent.OldSeal = seal
	// The whole old seal must match.
	other := intent
	other.OldSeal = seal
	other.OldSeal.IssuerEpoch = [16]byte{1}
	if _, err := j.Resume(run, other); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("resume with another old seal: got %v, want ErrIdentityMismatch", err)
	}
	other = intent
	other.BindingDigest = [32]byte{1}
	if _, err := j.Resume(run, other); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("resume under another binding: got %v, want ErrBindingMismatch", err)
	}
	// Exactly the next generation.
	for _, next := range []uint64{1, 3, 5} {
		other = intent
		other.NextAdmissionGeneration = next
		if _, err := j.Resume(run, other); !errors.Is(err, ErrGenerationConflict) {
			t.Fatalf("resume at generation %d: got %v, want ErrGenerationConflict", next, err)
		}
	}

	ack, err := j.Resume(run, intent)
	if err != nil {
		t.Fatal(err)
	}
	want := ResumeAck{ResumeID: [16]byte{5}, BindingDigest: testBinding, OldAdmissionGeneration: 1,
		NewAdmissionGeneration: 2, DrainID: testDrain, IssuerEpoch: j.epoch}
	if ack != want {
		t.Fatalf("resume ack = %+v, want %+v", ack, want)
	}
	// A repeated intent returns the same result.
	if again, err := j.Resume(run, intent); err != nil || again != ack {
		t.Fatalf("resume retry: %+v %v", again, err)
	}
	// The same id with another intent, or another id, is a conflict.
	other = intent
	other.NextAdmissionGeneration = 3
	if _, err := j.Resume(run, other); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("same resume id with another intent: got %v, want ErrImmutableConflict", err)
	}
	other = intent
	other.ResumeID = [16]byte{6}
	if _, err := j.Resume(run, other); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("another resume id: got %v, want ErrImmutableConflict", err)
	}
	// The resume is durable.
	again := reopen(t, st)
	if saved, ok, err := again.ResumedGeneration(run); err != nil || !ok || saved != ack {
		t.Fatalf("resume after reopen: %+v %v %v", saved, ok, err)
	}
	if saved, err := again.Resume(run, intent); err != nil || saved != ack {
		t.Fatalf("resume retry after reopen: %+v %v", saved, err)
	}
}

func TestOpenBindingOption(t *testing.T) {
	// The option must agree with the digest option and carry identities.
	for name, b := range map[string]Binding{
		"another digest":   {DestinationID: testDestination, Generation: 1, DescriptorDigest: [32]byte{1}},
		"zero destination": {Generation: 1, DescriptorDigest: testBinding},
		"zero generation":  {DestinationID: testDestination, DescriptorDigest: testBinding},
	} {
		if _, err := Open(storage.NewMemory(), Options{BindingDigest: testBinding, Binding: &b}); !errors.Is(err, ErrInvalidBinding) {
			t.Fatalf("%s: got %v, want ErrInvalidBinding", name, err)
		}
	}
}

// countKeys is the number of records the store holds.
func countKeys(t *testing.T, st storage.Storage) int {
	t.Helper()
	n := 0
	if err := st.Iterate(nil, nil, func(_, _ []byte) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	return n
}

// The check-only entry points of the switch answer every rule of their
// commit and never write: the RPC boundary runs them before the online
// target check, so a durable refusal does not depend on the target.
func TestSwitchChecksChangeNothing(t *testing.T) {
	j, st := newBoundJournal(t)
	run := testBatch(0)
	desired := nextBinding(4)
	switchID := [16]byte{0xAB}
	unchanged := func() {
		t.Helper()
		if _, open, err := j.CurrentSwitch(); err != nil || open {
			t.Fatalf("a check left a switch record: open=%v %v", open, err)
		}
	}

	if receipt, err := j.CheckPrepareSwitch(run, Seal{}, desired, switchID); !errors.Is(err, ErrDrainRequired) || receipt != nil {
		t.Fatalf("check without a seal: receipt=%v err=%v, want ErrDrainRequired", receipt, err)
	}
	unchanged()
	seal := sealRun(t, j, 0)
	keys := countKeys(t, st)
	if receipt, err := j.CheckPrepareSwitch(run, seal, desired, switchID); err != nil || receipt != nil {
		t.Fatalf("check of a legal switch: receipt=%v err=%v, want neither", receipt, err)
	}
	if countKeys(t, st) != keys {
		t.Fatal("the check of a legal switch wrote to the store")
	}
	unchanged()

	prepared, err := j.PrepareSwitch(run, seal, desired, switchID)
	if err != nil {
		t.Fatal(err)
	}
	keys = countKeys(t, st)
	if receipt, err := j.CheckPrepareSwitch(run, seal, desired, switchID); err != nil || receipt == nil || *receipt != prepared {
		t.Fatalf("check of a retry: receipt=%+v err=%v, want %+v", receipt, err, prepared)
	}
	if _, err := j.CheckPrepareSwitch(run, seal, desired, [16]byte{0xAC}); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("check of another switch id: got %v, want ErrImmutableConflict", err)
	}
	other := prepared
	other.NewDigest = [32]byte{1}
	if done, err := j.CheckCommitSwitch(switchID, other, desired.DescriptorDigest, 4); !errors.Is(err, ErrImmutableConflict) || done {
		t.Fatalf("commit check of another receipt: done=%v err=%v, want ErrImmutableConflict", done, err)
	}
	if done, err := j.CheckCommitSwitch(switchID, prepared, [32]byte{1}, 4); !errors.Is(err, ErrIdentityMismatch) || done {
		t.Fatalf("commit check of another binding: done=%v err=%v, want ErrIdentityMismatch", done, err)
	}
	if done, err := j.CheckCommitSwitch(switchID, prepared, desired.DescriptorDigest, 4); err != nil || done {
		t.Fatalf("commit check of the prepared switch: done=%v err=%v, want neither", done, err)
	}
	if countKeys(t, st) != keys || j.BindingDigest() != testBinding {
		t.Fatal("a commit check changed the store or the active binding")
	}

	if _, err := j.CommitSwitch(switchID, prepared, desired.DescriptorDigest, 4); err != nil {
		t.Fatal(err)
	}
	if done, err := j.CheckCommitSwitch(switchID, prepared, [32]byte{}, 0); err != nil || !done {
		t.Fatalf("commit check of the committed switch: done=%v err=%v, want done", done, err)
	}
	if done, err := j.CheckCommitSwitch([16]byte{1}, SwitchPrepared{}, [32]byte{}, 0); !errors.Is(err, ErrImmutableConflict) || done {
		t.Fatalf("commit check of another switch id: done=%v err=%v, want ErrImmutableConflict", done, err)
	}
}

// Every later drain cycle can switch again: a committed switch of an earlier
// seal is history and blocks nothing. The intent of a finished transition is
// retired and no longer answers its old receipt once a newer switch exists.
func TestSecondDrainCycleSwitchesAgain(t *testing.T) {
	j, st := newBoundJournal(t)
	run := testBatch(0)

	cycle := func(name string, admission, bindingGen uint64, drain, resumeID, switchID [16]byte) (Seal, SwitchPrepared, Binding, ResumeAck) {
		t.Helper()
		seal, err := j.SealGeneration(run, SealIntent{BindingGeneration: bindingGen, AdmissionGeneration: admission,
			DrainID: drain, Highwater: 0})
		if err != nil {
			t.Fatalf("%s: seal: %v", name, err)
		}
		desired := nextBinding(bindingGen + 1)
		prepared, err := j.PrepareSwitch(run, seal, desired, switchID)
		if err != nil {
			t.Fatalf("%s: prepare switch: %v", name, err)
		}
		if prepared.OldDigest != seal.BindingDigest || prepared.NewDigest != desired.DescriptorDigest || prepared.DrainID != drain {
			t.Fatalf("%s: receipt = %+v", name, prepared)
		}
		if cur, ok := j.ActiveBinding(); !ok || cur.DescriptorDigest != seal.BindingDigest {
			t.Fatalf("%s: the active binding moved before the commit: %+v", name, cur)
		}
		if _, err := j.CommitSwitch(switchID, prepared, desired.DescriptorDigest, desired.Generation); err != nil {
			t.Fatalf("%s: commit switch: %v", name, err)
		}
		if cur, ok := j.ActiveBinding(); !ok || cur != desired {
			t.Fatalf("%s: active binding after the commit: %+v %v", name, cur, ok)
		}
		ack, err := j.Resume(run, ResumeIntent{ResumeID: resumeID, OldSeal: seal,
			BindingDigest: desired.DescriptorDigest, NextAdmissionGeneration: admission + 1})
		if err != nil {
			t.Fatalf("%s: resume: %v", name, err)
		}
		if ack.OldAdmissionGeneration != admission || ack.NewAdmissionGeneration != admission+1 ||
			ack.BindingDigest != desired.DescriptorDigest || ack.DrainID != drain {
			t.Fatalf("%s: resume ack = %+v", name, ack)
		}
		return seal, prepared, desired, ack
	}

	first, firstReceipt, firstBinding, _ := cycle("first cycle", 1, 3, [16]byte{9}, [16]byte{5}, [16]byte{0x01})
	second, secondReceipt, secondBinding, secondAck := cycle("second cycle", 2, 4, [16]byte{10}, [16]byte{6}, [16]byte{0x02})
	if second.AdmissionGeneration != 2 || secondAck.OldAdmissionGeneration != 2 || secondAck.NewAdmissionGeneration != 3 {
		t.Fatalf("second cycle generations: seal %+v resume %+v", second, secondAck)
	}

	// The intent of the first transition is retired: its commit retry no
	// longer answers the old receipt once a newer switch exists.
	if _, err := j.CommitSwitch([16]byte{0x01}, firstReceipt, firstBinding.DescriptorDigest, firstBinding.Generation); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("commit retry of a retired switch: got %v, want ErrImmutableConflict", err)
	}
	if _, err := j.PrepareSwitch(run, first, firstBinding, [16]byte{0x01}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("prepare retry of a retired switch: got %v, want ErrIdentityMismatch", err)
	}
	// The latest switch is the second one, and it answers its own retry.
	if again, err := j.CommitSwitch([16]byte{0x02}, secondReceipt, [32]byte{}, 0); err != nil || again != secondReceipt {
		t.Fatalf("commit retry of the current switch: %+v %v", again, err)
	}
	// The third cycle continues the line after a reopen of the store.
	again := reopen(t, st)
	third, err := again.SealGeneration(run, SealIntent{BindingGeneration: 5, AdmissionGeneration: 3,
		DrainID: [16]byte{11}, Highwater: 0})
	if err != nil {
		t.Fatalf("third seal after reopen: %v", err)
	}
	if third.BindingDigest != secondBinding.DescriptorDigest {
		t.Fatalf("third seal binding: %x, want %x", third.BindingDigest, secondBinding.DescriptorDigest)
	}
	latest := nextBinding(6)
	if _, err := again.PrepareSwitch(run, third, latest, [16]byte{0x03}); err != nil {
		t.Fatalf("third switch: %v", err)
	}
}
