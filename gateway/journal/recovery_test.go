package journal

import (
	"crypto/sha256"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// Crash recovery: dropping the Journal and reopening over the same storage
// is the view a restarted gateway sees after sync commits. Every phase below
// reopens mid-flight and re-drives the protocol.

func reopen(t *testing.T, st storage.Storage) *Journal {
	t.Helper()
	j, err := Open(st, Options{BindingDigest: testBinding})
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestRecoveryAtEveryPhase(t *testing.T) {
	st := storage.NewMemory()
	j := reopen(t, st)
	id := testBatch(1)
	manifest := []byte("extractor.manifest.v2\x00recovery")
	digest := sha256.Sum256(manifest)

	// Partial manifest stream resumes at the durable offset.
	if _, err := j.BeginPrepare(id, digest, 2, uint64(len(manifest)), PrepareNormal, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := j.PrepareChunk(id, digest, 0, sha256.Sum256(manifest[:6]), manifest[:6]); err != nil {
		t.Fatal(err)
	}
	j = reopen(t, st)
	resume, err := j.BeginPrepare(id, digest, 2, uint64(len(manifest)), PrepareNormal, nil)
	if err != nil || resume.ResumeOffset != 6 {
		t.Fatalf("resume after reopen: %+v %v", resume, err)
	}
	if _, err := j.PrepareChunk(id, digest, 6, sha256.Sum256(manifest[6:]), manifest[6:]); err != nil {
		t.Fatal(err)
	}

	// Reservation survives reopen byte-for-byte; the retry allocates nothing.
	res, err := j.FinalizePrepare(id, digest)
	if err != nil {
		t.Fatal(err)
	}
	j = reopen(t, st)
	again, err := j.FinalizePrepare(id, digest)
	if err != nil || string(again.Seed) != string(res.Seed) {
		t.Fatalf("reservation after reopen: %+v %v", again, err)
	}

	// Publish resumes at per-frame cursors.
	plan := PlanDigest(res.Seed)
	entries := []LayoutEntry{
		{Ordinal: 0, FragmentIndex: 0, Form: 0, Capacity: 4, PartCount: 1, FragmentTotal: 4},
	}
	layout := computeLayoutDigest(plan, entries)
	if _, err := j.BeginPublish(id, res.Seed, plan, layout, 1); err != nil {
		t.Fatal(err)
	}
	meta := FrameMeta{Form: 0, Capacity: 4, PartCount: 1, FragmentTotal: 4}
	if _, err := j.PublishChunk(id, plan, FrameAddr{0, 0, 0}, &meta, 0, sha256.Sum256([]byte("ab")), []byte("ab")); err != nil {
		t.Fatal(err)
	}
	j = reopen(t, st)
	resumePub, err := j.BeginPublish(id, res.Seed, plan, layout, 1)
	if err != nil || len(resumePub.Frames) != 1 || resumePub.Frames[0].NextBytes != 2 {
		t.Fatalf("publish cursors after reopen: %+v %v", resumePub, err)
	}
	if _, err := j.PublishChunk(id, plan, FrameAddr{0, 0, 0}, &meta, 2, sha256.Sum256([]byte("cd")), []byte("cd")); err != nil {
		t.Fatal(err)
	}
	if err := j.FinalizePublish(id, plan, digest, layout); err != nil {
		t.Fatal(err)
	}

	// CLOSED survives reopen; closing again is idempotent, acks work.
	j = reopen(t, st)
	if err := j.FinalizePublish(id, plan, digest, layout); err != nil {
		t.Fatal(err)
	}
	if _, err := j.RecordPubAck(id, plan, FrameAddr{0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := j.MarkNATSACKed(id, plan, digest); err != nil {
		t.Fatal(err)
	}

	// NATS_ACKED survives reopen; release resumes and completes.
	j = reopen(t, st)
	if err := j.MarkNATSACKed(id, plan, digest); err != nil {
		t.Fatal(err)
	}
	if err := j.BeginRelease(id, plan); err != nil {
		t.Fatal(err)
	}
	done, err := j.ReleaseCleanup(id, 1)
	if err != nil {
		t.Fatal(err)
	}
	j = reopen(t, st)
	for {
		done, err = j.ReleaseCleanup(id, 2)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
	}
	prefix, err := j.CompleteRelease(id)
	if err != nil || prefix != id.Seq {
		t.Fatalf("complete after reopen: prefix=%d err=%v", prefix, err)
	}
	j = reopen(t, st)
	if p, err := j.RetiredPrefix(id); err != nil || p != id.Seq {
		t.Fatalf("retired prefix after reopen: %d %v", p, err)
	}
	info, err := j.Batch(id)
	if err != nil || info.State != StateRetired {
		t.Fatalf("retired state after reopen: %+v %v", info, err)
	}
}

func TestRecoveryImportSeedSurvivesReopen(t *testing.T) {
	st := storage.NewMemory()
	j := reopen(t, st)
	pf := newPlan(t, j, 1, true)

	j = reopen(t, st)
	resume, err := j.FinalizePrepare(pf.id, pf.res.ManifestDigest)
	if err != nil || string(resume.Seed) != string(pf.res.Seed) {
		t.Fatalf("import seed after reopen: %+v %v", resume, err)
	}
	if resume.IssuerEpoch != pf.res.IssuerEpoch {
		t.Fatal("the original issuer epoch must survive reopen")
	}
	// The import continues to a full SealImport after the restart.
	if _, err := j.BeginPublish(pf.id, pf.res.Seed, pf.planDigest, pf.layout, uint64(len(pf.entries))); err != nil {
		t.Fatal(err)
	}
	pf.publishFrames(t, j)
	if err := j.SealImport(pf.id, pf.planDigest, pf.res.ManifestDigest, pf.layout); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverySequenceAllocationContinues(t *testing.T) {
	st := storage.NewMemory()
	j := reopen(t, st)
	first := prepareBatch(t, j, testBatch(1), []byte("one"), 4)
	// Crash right after the reservation commit: the counter moved with it,
	// so the reopened store continues without gaps or reuse.
	j = reopen(t, st)
	second := prepareBatch(t, j, testBatch(2), []byte("two"), 3)
	if second.SequenceBase != first.SequenceBase+4 {
		t.Fatalf("second base = %d, want %d", second.SequenceBase, first.SequenceBase+4)
	}
}
