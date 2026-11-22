package producer

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

var testBinding [32]byte

func init() {
	testBinding = sha256.Sum256([]byte("test binding"))
}

func newTestStore(t *testing.T) (*Store, *storage.MemoryStorage) {
	t.Helper()
	st := storage.NewMemory()
	s, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDestinationBinding(testBinding); err != nil {
		t.Fatal(err)
	}
	return s, st
}

// driveToReady walks one batch through INTENT -> STAGING -> READY.
func driveToReady(t *testing.T, s *Store, decision uint64, manifest []byte) (Attempt, Ready) {
	t.Helper()
	att, replayed, err := s.AllocateBatch(decision, 2)
	if err != nil || replayed {
		t.Fatalf("AllocateBatch: att=%v replayed=%v err=%v", att, replayed, err)
	}
	att, err = s.StartAttempt(att.BatchSeq, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAttemptData(att.BatchSeq, att.Generation, 0, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAttemptManifest(att.BatchSeq, att.Generation, 0, manifest); err != nil {
		t.Fatal(err)
	}
	ready, err := s.CommitReady(att.BatchSeq, att.Generation, uint64(len(manifest)), []byte("checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	return att, ready
}

func mustSeal(t *testing.T, s *Store, seq uint64, seed []byte, chunks [][]byte) Sealed {
	t.Helper()
	gen, _, err := s.BeginPlan(seq, seed, [32]byte{5}, []byte("header"))
	if err != nil {
		t.Fatal(err)
	}
	var off uint64
	for _, c := range chunks {
		if err := s.WritePlanChunk(seq, gen, off, c); err != nil {
			t.Fatal(err)
		}
		off += uint64(len(c))
	}
	sealed, err := s.Seal(seq, gen, off)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestOpenCreatesAndKeepsIdentity(t *testing.T) {
	st := storage.NewMemory()
	s, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	id := s.Identity()
	if id.ProducerID == ([16]byte{}) || id.RunID == ([16]byte{}) {
		t.Fatal("fresh store must generate a nonzero identity")
	}
	md := s.ModeInfo()
	if md.Mode != ModeLIVE || md.AdmissionGeneration != 1 {
		t.Fatalf("fresh mode = %v gen %d, want LIVE 1", md.Mode, md.AdmissionGeneration)
	}
	if s.RetiredPrefix() != 0 {
		t.Fatalf("fresh retired prefix = %d, want 0", s.RetiredPrefix())
	}

	again, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	if again.Identity() != id {
		t.Fatal("identity must survive reopen")
	}
	// A different storage gets a different identity: a lost store is a new
	// identity, never a borrowed pending run.
	other, err := Open(storage.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	if other.Identity() == id {
		t.Fatal("independent stores generated the same identity")
	}
}

func TestBindingRules(t *testing.T) {
	s, st := newTestStore(t)
	if s.BindingDigest() != testBinding {
		t.Fatal("binding digest not saved")
	}
	if err := s.SetDestinationBinding(testBinding); err != nil {
		t.Fatalf("same digest must be idempotent, got %v", err)
	}
	if err := s.SetDestinationBinding([32]byte{9}); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("different digest: got %v, want ErrImmutableConflict", err)
	}
	bare, err := Open(storage.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	_ = st
	if _, _, err := bare.AllocateBatch(1, 1); !errors.Is(err, ErrBindingRequired) {
		t.Fatalf("capture before binding: got %v, want ErrBindingRequired", err)
	}
}

func TestAllocateBatchGuardsAndReplay(t *testing.T) {
	s, _ := newTestStore(t)
	if _, _, err := s.AllocateBatch(1, 0); !errors.Is(err, ErrEmptyBatch) {
		t.Fatalf("zero event count: got %v, want ErrEmptyBatch", err)
	}
	if err := s.SetDrain(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AllocateBatch(1, 2); !errors.Is(err, ErrModeConflict) {
		t.Fatalf("capture in DRAIN: got %v, want ErrModeConflict", err)
	}
	if _, err := s.ResumeToLive(); err == nil {
		t.Fatal("ResumeToLive must require DRAINED")
	}
	// DRAIN -> DRAINED -> Resume -> LIVE, then allocation works again.
	if err := s.SetDrained(); err != nil {
		t.Fatal(err)
	}
	gen, err := s.ResumeToLive()
	if err != nil || gen != 2 {
		t.Fatalf("ResumeToLive: gen=%d err=%v, want 2 nil", gen, err)
	}
	first, replayed, err := s.AllocateBatch(1, 2)
	if err != nil || replayed {
		t.Fatalf("first allocation: %v %v %v", first, replayed, err)
	}
	if first.BatchSeq != 1 || first.Generation != 0 {
		t.Fatalf("first allocation seq=%d gen=%d, want 1 0 (no attempt yet)", first.BatchSeq, first.Generation)
	}
	same, replayed, err := s.AllocateBatch(1, 2)
	if err != nil || !replayed || same.BatchSeq != first.BatchSeq {
		t.Fatalf("decision replay: %v %v %v, want same batch with replayed=true", same, replayed, err)
	}
	if _, _, err := s.AllocateBatch(1, 3); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("decision replay with different event count: got %v, want ErrImmutableConflict", err)
	}
	next, _, err := s.AllocateBatch(2, 1)
	if err != nil || next.BatchSeq != 2 {
		t.Fatalf("second decision must consume the next batch_seq: %v %v", next, err)
	}
}

func TestModeTransitions(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SetDrained(); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("LIVE -> DRAINED: got %v, want ErrStateConflict", err)
	}
	if err := s.SetDrain(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDrain(); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("double drain: got %v, want ErrStateConflict", err)
	}
	if err := s.SetDrained(); err != nil {
		t.Fatal(err)
	}
	if md := s.ModeInfo(); md.Mode != ModeDRAINED || md.AdmissionGeneration != 1 {
		t.Fatalf("drained mode = %+v", md)
	}
	// A blocked store can be blocked again idempotently from any mode.
	if err := s.SetBlocked(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBlocked(); err != nil {
		t.Fatal("double block must be idempotent")
	}
	if _, err := s.ResumeToLive(); err == nil {
		t.Fatal("BLOCKED cannot resume to LIVE directly")
	}
}

func TestStartAttemptFreshSequencesSameIdentity(t *testing.T) {
	s, _ := newTestStore(t)
	att, _, err := s.AllocateBatch(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.StartAttempt(att.BatchSeq, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.EventSequenceBase == 0 {
		t.Fatal("event sequences start at 1")
	}
	if err := s.WriteAttemptData(att.BatchSeq, first.Generation, 0, []byte("old")); err != nil {
		t.Fatal(err)
	}
	second, err := s.StartAttempt(att.BatchSeq, 2)
	if err != nil {
		t.Fatal(err)
	}
	if second.BatchSeq != first.BatchSeq {
		t.Fatal("retry must keep batch_id")
	}
	if second.Generation != first.Generation+1 {
		t.Fatalf("attempt generation %d, want %d", second.Generation, first.Generation+1)
	}
	if second.EventSequenceBase != first.EventSequenceBase+2 {
		t.Fatalf("retry must reserve fresh event sequences: base %d after %d", second.EventSequenceBase, first.EventSequenceBase)
	}
	if _, err := s.StartAttempt(att.BatchSeq, 3); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("different event count: got %v, want ErrImmutableConflict", err)
	}
}

func TestAttemptChunkWriteRules(t *testing.T) {
	s, _ := newTestStore(t)
	att, _, _ := s.AllocateBatch(1, 1)
	att, _ = s.StartAttempt(att.BatchSeq, 1)
	seq, gen := att.BatchSeq, att.Generation

	if err := s.WriteAttemptManifest(seq, gen, 8, []byte("gap")); !errors.Is(err, ErrOffsetGap) {
		t.Fatalf("gap write: got %v, want ErrOffsetGap", err)
	}
	if err := s.WriteAttemptManifest(seq, gen, 0, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAttemptManifest(seq, gen, 0, []byte("hello")); err != nil {
		t.Fatalf("identical retry must be a no-op, got %v", err)
	}
	if err := s.WriteAttemptManifest(seq, gen, 0, []byte("HELLO")); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("conflicting rewrite: got %v, want ErrImmutableConflict", err)
	}
	if err := s.WriteAttemptManifest(seq, gen, 5, []byte(" world")); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAttemptData(seq, gen, 0, []byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAttemptManifest(seq, gen+1, 0, nil); err == nil {
		t.Fatal("writes for a non-current generation must fail")
	}
}

func TestCommitReadyClosingCommit(t *testing.T) {
	s, _ := newTestStore(t)
	att, _, _ := s.AllocateBatch(1, 2)
	att, _ = s.StartAttempt(att.BatchSeq, 2)
	seq, gen := att.BatchSeq, att.Generation

	manifest := []byte("extractor.manifest.v2\x00")
	if err := s.WriteAttemptManifest(seq, gen, 0, manifest[:10]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitReady(seq, gen, uint64(len(manifest)), nil); !errors.Is(err, ErrOffsetGap) {
		t.Fatalf("incomplete manifest: got %v, want ErrOffsetGap", err)
	}
	if err := s.WriteAttemptManifest(seq, gen, 10, manifest[10:]); err != nil {
		t.Fatal(err)
	}
	ready, err := s.CommitReady(seq, gen, uint64(len(manifest)), []byte("cp"))
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(manifest)
	if ready.ManifestDigest != want || ready.ManifestLength != uint64(len(manifest)) || ready.EventCount != 2 {
		t.Fatalf("ready marker = %+v", ready)
	}
	info, err := s.Batch(seq)
	if err != nil || info.State != StateReady {
		t.Fatalf("state after commit: %+v %v", info, err)
	}
	cp, err := s.Checkpoint(seq)
	if err != nil || string(cp) != "cp" {
		t.Fatalf("checkpoint = %q %v", cp, err)
	}

	// Idempotent retry after READY returns the same marker.
	again, err := s.CommitReady(seq, gen, uint64(len(manifest)), []byte("cp"))
	if err != nil || again != ready {
		t.Fatalf("CommitReady retry: %+v %v", again, err)
	}
	// A late StartAttempt is refused: event identity is immutable now.
	if _, err := s.StartAttempt(seq, 2); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("post-READY attempt: got %v, want ErrStateConflict", err)
	}
	if err := s.WriteAttemptManifest(seq, gen, 0, manifest[:10]); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("post-READY write: got %v, want ErrStateConflict", err)
	}
}

func TestCommitReadyLeavesReplacedAttemptForBoundedPrune(t *testing.T) {
	s, st := newTestStore(t)
	att, _, _ := s.AllocateBatch(1, 1)
	att, _ = s.StartAttempt(att.BatchSeq, 1)
	if err := s.WriteAttemptData(att.BatchSeq, 1, 0, []byte("old attempt data")); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAttemptManifest(att.BatchSeq, 1, 0, []byte("old manifest")); err != nil {
		t.Fatal(err)
	}
	att, _ = s.StartAttempt(att.BatchSeq, 1)
	if err := s.WriteAttemptManifest(att.BatchSeq, att.Generation, 0, []byte("new manifest")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitReady(att.BatchSeq, att.Generation, 12, nil); err != nil {
		t.Fatal(err)
	}
	// The closing commit records only the READY marker; superseded keys
	// stay for the bounded post-commit prune (the 1 MiB WriteBatch bound
	// must hold for any chunk count — the review P2).
	lo, hi := keyRange(attemptRoot(att.BatchSeq))
	count := 0
	_ = st.Iterate(lo, hi, func(k, v []byte) error {
		if genOfAttemptKey(k, att.BatchSeq) < att.Generation {
			count++
		}
		return nil
	})
	if count == 0 {
		t.Fatal("superseded keys should still exist after the closing commit (pruned later)")
	}
	// The ready state is committed and readable.
	r, err := s.readReady(att.BatchSeq)
	if err != nil || r.Attempt != att.Generation {
		t.Fatalf("ready after commit: %+v %v", r, err)
	}
}

func TestPlanLifecycle(t *testing.T) {
	s, _ := newTestStore(t)
	att, _ := driveToReady(t, s, 1, []byte("manifest"))
	seq := att.BatchSeq

	if _, _, err := s.BeginPlan(seq, []byte("seed"), [32]byte{5}, nil); err != nil {
		t.Fatalf("plan after READY must be allowed: %v", err)
	}
	// Retry with identical bytes is idempotent.
	gen1, digest1, err := s.BeginPlan(seq, []byte("seed"), [32]byte{5}, []byte("h"))
	if err != nil || gen1 != 1 {
		t.Fatalf("BeginPlan retry: gen=%d err=%v", gen1, err)
	}
	wantDigest := sha256.Sum256([]byte("seed"))
	if digest1 != wantDigest {
		t.Fatal("plan digest must be SHA-256 of the seed bytes")
	}
	// A different unpublished plan needs a durable abandonment first.
	if _, _, err := s.BeginPlan(seq, []byte("seed2"), [32]byte{5}, nil); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("second active plan: got %v, want ErrStateConflict", err)
	}
	if err := s.AbandonPlan(seq); err != nil {
		t.Fatal(err)
	}
	if err := s.AbandonPlan(seq); err != nil {
		t.Fatal("abandon retry must be idempotent")
	}
	gen2, _, err := s.BeginPlan(seq, []byte("seed2"), [32]byte{6}, nil)
	if err != nil || gen2 != 2 {
		t.Fatalf("replan: gen=%d err=%v", gen2, err)
	}

	if err := s.WritePlanChunk(seq, gen2, 4, []byte("gap")); !errors.Is(err, ErrOffsetGap) {
		t.Fatalf("plan gap: got %v, want ErrOffsetGap", err)
	}
	if err := s.WritePlanChunk(seq, gen1, 0, []byte("stale gen")); err == nil {
		t.Fatal("writes to an abandoned generation must fail")
	}
	if err := s.WritePlanChunk(seq, gen2, 0, []byte("frame-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := s.WritePlanChunk(seq, gen2, 0, []byte("frame-bytes")); err != nil {
		t.Fatal("identical plan chunk retry must be a no-op")
	}
	if err := s.WritePlanChunk(seq, gen2, 0, []byte("other")); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("plan chunk conflict: got %v, want ErrImmutableConflict", err)
	}
	if _, err := s.Seal(seq, gen2, 5); !errors.Is(err, ErrOffsetGap) {
		t.Fatalf("seal with wrong total: got %v, want ErrOffsetGap", err)
	}
	sealed, err := s.Seal(seq, gen2, 11)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.PlanDigest != sha256.Sum256([]byte("seed2")) || sealed.Generation != gen2 || sealed.TotalBytes != 11 {
		t.Fatalf("sealed marker = %+v", sealed)
	}
	info, _ := s.Batch(seq)
	if info.State != StateSealed {
		t.Fatalf("state after seal = %v", info.State)
	}
	again, err := s.Seal(seq, gen2, 11)
	if err != nil || again != sealed {
		t.Fatalf("seal retry: %+v %v", again, err)
	}
	if _, err := s.Seal(seq, gen2, 12); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("reseal with different total: got %v, want ErrImmutableConflict", err)
	}
	if _, _, err := s.BeginPlan(seq, []byte("seed3"), [32]byte{7}, nil); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("post-SEALED replan: got %v, want ErrImmutableConflict", err)
	}
	if g, d, err := s.BeginPlan(seq, []byte("seed2"), [32]byte{6}, nil); err != nil || g != gen2 || d != sealed.PlanDigest {
		t.Fatalf("post-SEALED replay: gen=%d err=%v", g, err)
	}
	if err := s.AbandonPlan(seq); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("post-SEALED abandon: got %v, want ErrStateConflict", err)
	}
}

func TestIngestAckCleanupOrderedRelease(t *testing.T) {
	s, _ := newTestStore(t)
	att1, _ := driveToReady(t, s, 1, []byte("manifest-one"))
	att2, _ := driveToReady(t, s, 2, []byte("manifest-two"))
	sealed1 := mustSeal(t, s, att1.BatchSeq, []byte("seed-one"), [][]byte{[]byte("aaaa"), []byte("bbbb")})
	sealed2 := mustSeal(t, s, att2.BatchSeq, []byte("seed-two"), [][]byte{[]byte("cccc")})

	ack1 := IngestAck{
		ManifestDigest: sha256.Sum256([]byte("manifest-one")),
		PlanDigest:     sealed1.PlanDigest,
		LayoutDigest:   sealed1.LayoutDigest,
		BindingDigest:  testBinding,
		IssuerEpoch:    [16]byte{1},
	}
	if err := s.OnIngestAck(att2.BatchSeq, IngestAck{ManifestDigest: sha256.Sum256([]byte("manifest-two")),
		PlanDigest: sealed2.PlanDigest, LayoutDigest: sealed2.LayoutDigest, BindingDigest: testBinding}); err != nil {
		t.Fatal(err)
	}
	if err := s.OnIngestAck(att1.BatchSeq, ack1); err != nil {
		t.Fatal(err)
	}
	if err := s.OnIngestAck(att1.BatchSeq, ack1); err != nil {
		t.Fatal("identical ack retry must be a no-op")
	}
	bad := ack1
	bad.IssuerEpoch = [16]byte{9}
	if err := s.OnIngestAck(att1.BatchSeq, bad); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("different ack: got %v, want ErrImmutableConflict", err)
	}
	wrong := ack1
	wrong.PlanDigest = [32]byte{7}
	if err := s.OnIngestAck(att1.BatchSeq, wrong); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("conflicting ack on ACKED batch: got %v, want ErrImmutableConflict", err)
	}

	entries, err := s.Outbox()
	if err != nil || len(entries) != 2 || entries[0].Seq != att1.BatchSeq || entries[1].Seq != att2.BatchSeq {
		t.Fatalf("outbox = %+v err=%v, want both batches in order", entries, err)
	}

	// Release is strictly ordered: batch 2 cannot retire before batch 1.
	if err := s.OnReleaseAck(att2.BatchSeq); !errors.Is(err, ErrOutOfOrderRelease) {
		t.Fatalf("late release jump: got %v, want ErrOutOfOrderRelease", err)
	}
	done, err := s.Cleanup(att1.BatchSeq, 1000)
	if err != nil || !done {
		t.Fatalf("cleanup batch 1: done=%v err=%v", done, err)
	}
	if err := s.OnReleaseAck(att1.BatchSeq); err != nil {
		t.Fatal(err)
	}
	if s.RetiredPrefix() != att1.BatchSeq {
		t.Fatalf("retired prefix = %d, want %d", s.RetiredPrefix(), att1.BatchSeq)
	}
	if err := s.OnReleaseAck(att1.BatchSeq); err != nil {
		t.Fatal("replayed release ack for a retired prefix must succeed")
	}
	// Batch 2 was already ACKED; a late ack replay after retirement of batch
	// 1 changes nothing.
	if err := s.OnIngestAck(att2.BatchSeq, IngestAck{ManifestDigest: sha256.Sum256([]byte("manifest-two")),
		PlanDigest: sealed2.PlanDigest, LayoutDigest: sealed2.LayoutDigest, BindingDigest: testBinding}); err != nil {
		t.Fatal(err)
	}
	done, err = s.Cleanup(att2.BatchSeq, 1000)
	if err != nil || !done {
		t.Fatalf("cleanup batch 2: done=%v err=%v", done, err)
	}
	if err := s.OnReleaseAck(att2.BatchSeq); err != nil {
		t.Fatal(err)
	}
	if s.RetiredPrefix() != att2.BatchSeq {
		t.Fatalf("final retired prefix = %d, want %d", s.RetiredPrefix(), att2.BatchSeq)
	}
	entries, err = s.Outbox()
	if err != nil || len(entries) != 0 {
		t.Fatalf("outbox after retire = %+v err=%v, want empty", entries, err)
	}
	info, err := s.Batch(att1.BatchSeq)
	if err != nil || info.State != StateRetired {
		t.Fatalf("retired batch info: %+v %v", info, err)
	}
}

func TestAckMismatchRejected(t *testing.T) {
	s, _ := newTestStore(t)
	att, ready := driveToReady(t, s, 1, []byte("manifest"))
	if err := s.OnIngestAck(att.BatchSeq, IngestAck{}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("ack before SEALED: got %v, want ErrStateConflict", err)
	}
	sealed := mustSeal(t, s, att.BatchSeq, []byte("seed"), [][]byte{[]byte("x")})
	base := IngestAck{
		ManifestDigest: ready.ManifestDigest,
		PlanDigest:     sealed.PlanDigest,
		LayoutDigest:   sealed.LayoutDigest,
		BindingDigest:  testBinding,
		IssuerEpoch:    [16]byte{2},
	}
	for name, mutate := range map[string]func(*IngestAck){
		"manifest": func(a *IngestAck) { a.ManifestDigest = [32]byte{1} },
		"plan":     func(a *IngestAck) { a.PlanDigest = [32]byte{1} },
		"layout":   func(a *IngestAck) { a.LayoutDigest = [32]byte{1} },
		"binding":  func(a *IngestAck) { a.BindingDigest = [32]byte{1} },
	} {
		bad := base
		mutate(&bad)
		if err := s.OnIngestAck(att.BatchSeq, bad); !errors.Is(err, ErrIngestAckMismatch) {
			t.Fatalf("%s digest mismatch: got %v, want ErrIngestAckMismatch", name, err)
		}
	}
	if err := s.OnIngestAck(att.BatchSeq, base); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupBoundedAndBlocksRelease(t *testing.T) {
	s, _ := newTestStore(t)
	att, _ := driveToReady(t, s, 1, []byte("manifest"))
	sealed := mustSeal(t, s, att.BatchSeq, []byte("seed"), [][]byte{[]byte("p1"), []byte("p2"), []byte("p3")})
	if err := s.OnIngestAck(att.BatchSeq, IngestAck{
		ManifestDigest: sha256.Sum256([]byte("manifest")),
		PlanDigest:     sealed.PlanDigest,
		LayoutDigest:   sealed.LayoutDigest,
		BindingDigest:  testBinding,
	}); err != nil {
		t.Fatal(err)
	}
	// Two payload keys exist (attempt manifest chunk + plan chunks); a bound
	// of one key per call must need several bounded steps.
	done, err := s.Cleanup(att.BatchSeq, 1)
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("cleanup cannot finish with a bound below the key count")
	}
	if err := s.OnReleaseAck(att.BatchSeq); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("release before finished cleanup: got %v, want ErrStateConflict", err)
	}
	for {
		done, err = s.Cleanup(att.BatchSeq, 1)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
	}
	if err := s.OnReleaseAck(att.BatchSeq); err != nil {
		t.Fatal(err)
	}
	// The IngestAck evidence was never deleted by payload cleanup.
	info, err := s.Batch(att.BatchSeq)
	if err != nil || info.State != StateRetired {
		t.Fatalf("retired info: %+v %v", info, err)
	}
	done, err = s.Cleanup(att.BatchSeq, 5)
	if err != nil || !done {
		t.Fatalf("cleanup of a retired batch: done=%v err=%v", done, err)
	}
}

func TestEventIDShape(t *testing.T) {
	s, _ := newTestStore(t)
	id := s.Identity()
	eventID := s.EventID(7)
	var want [40]byte
	copy(want[:16], id.ProducerID[:])
	copy(want[16:32], id.RunID[:])
	copy(want[32:], u64be(7))
	if eventID != want {
		t.Fatalf("event id = %x", eventID)
	}
	if !bytes.Equal(eventID[32:], []byte{0, 0, 0, 0, 0, 0, 0, 7}) {
		t.Fatal("event sequence must be big endian")
	}
}
