package journal

import (
	"crypto/sha256"
	"errors"
	"testing"
)

// planFixture is one small publishable plan: event 0 fragment 0 as one
// COMPLETE frame, event 1 fragment 0 as two PART frames.
type planFixture struct {
	id         BatchID
	res        Reservation
	planDigest [32]byte
	layout     [32]byte
	entries    []LayoutEntry
	frames     []frameFixture
}

type frameFixture struct {
	addr  FrameAddr
	meta  FrameMeta
	bytes []byte
}

func newPlan(t *testing.T, j *Journal, seq uint64, importMode bool) planFixture {
	t.Helper()
	id := testBatch(seq)
	manifest := []byte("extractor.manifest.v2\x00payload")
	var res Reservation
	if importMode {
		digest := sha256.Sum256(manifest)
		seed := BuildSeed(SeedLayout{
			ReservationID: [16]byte{0xCC}, IssuerEpoch: [16]byte{0xDD}, Batch: id,
			ManifestDigest: digest, EventCount: 2, SequenceBase: 9,
			BindingDigest: testBinding, FrameLimit: DefaultFrameLimit,
		})
		if _, err := j.BeginPrepare(id, digest, 2, uint64(len(manifest)), PrepareImportSealed, seed); err != nil {
			t.Fatal(err)
		}
		chunkify(t, j, id, digest, 0, manifest, 7)
		var err error
		res, err = j.FinalizePrepare(id, digest)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		res = prepareBatch(t, j, id, manifest, 2)
	}

	complete := FrameMeta{Form: 0, Capacity: 6, PartCount: 1, FragmentTotal: 6}
	part := FrameMeta{Form: 1, Capacity: 4, PartCount: 2, FragmentTotal: 6}
	pf := planFixture{
		id:         id,
		res:        res,
		planDigest: PlanDigest(res.Seed),
		entries: []LayoutEntry{
			{Ordinal: 0, FragmentIndex: 0, Form: 0, Capacity: 6, PartCount: 1, FragmentTotal: 6},
			{Ordinal: 1, FragmentIndex: 0, Form: 1, Capacity: 4, PartCount: 2, FragmentTotal: 6},
		},
		frames: []frameFixture{
			{FrameAddr{0, 0, 0}, complete, []byte("abcdef")},
			{FrameAddr{1, 0, 0}, part, []byte("wxyz")},
			{FrameAddr{1, 0, 1}, part, []byte("uv")},
		},
	}
	pf.layout = computeLayoutDigest(pf.planDigest, pf.entries)
	return pf
}

// computeLayoutDigest is an independent reimplementation of the layout
// manifest digest for the test oracle.
func computeLayoutDigest(planDigest [32]byte, entries []LayoutEntry) [32]byte {
	h := sha256.New()
	h.Write([]byte("extractor.layout.v2"))
	h.Write([]byte{0})
	h.Write(planDigest[:])
	var count [8]byte
	for i := 0; i < 8; i++ {
		count[i] = byte(uint64(len(entries)) >> (8 * uint(7-i)))
	}
	h.Write(count[:])
	for _, e := range entries {
		h.Write(u64be(e.Ordinal))
		h.Write(u64be(e.FragmentIndex))
		h.Write([]byte{e.Form})
		h.Write(u64be(e.Capacity))
		h.Write(u64be(e.PartCount))
		h.Write(u64be(e.FragmentTotal))
	}
	var d [32]byte
	copy(d[:], h.Sum(nil))
	return d
}

func (pf planFixture) beginPublish(t *testing.T, j *Journal) PublishResume {
	t.Helper()
	resume, err := j.BeginPublish(pf.id, pf.res.Seed, pf.planDigest, pf.layout, uint64(len(pf.entries)))
	if err != nil {
		t.Fatal(err)
	}
	return resume
}

func (pf planFixture) publishFrames(t *testing.T, j *Journal) {
	t.Helper()
	for _, f := range pf.frames {
		meta := f.meta
		if _, err := j.PublishChunk(pf.id, pf.planDigest, f.addr, &meta, 0, sha256.Sum256(f.bytes), f.bytes); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBeginPublishValidation(t *testing.T) {
	j, _ := newJournal(t)
	pf := newPlan(t, j, 1, false)

	if _, err := j.BeginPublish(pf.id, []byte("other-seed"), pf.planDigest, pf.layout, 2); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("foreign seed: got %v, want ErrIdentityMismatch", err)
	}
	if _, err := j.BeginPublish(pf.id, pf.res.Seed, [32]byte{1}, pf.layout, 2); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("wrong plan digest: got %v, want ErrIdentityMismatch", err)
	}
	if _, err := j.BeginPublish(pf.id, pf.res.Seed, pf.planDigest, pf.layout, 0); err == nil {
		t.Fatal("zero entry count must be rejected")
	}
	// An unprepared batch cannot publish.
	if _, err := j.BeginPublish(testBatch(9), pf.res.Seed, pf.planDigest, pf.layout, 2); !errors.Is(err, ErrBatchNotFound) {
		t.Fatalf("unprepared batch: got %v", err)
	}

	pf.beginPublish(t, j)
	// A retry with the same identity returns durable cursors.
	resume, err := j.BeginPublish(pf.id, pf.res.Seed, pf.planDigest, pf.layout, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(resume.Frames) != 0 {
		t.Fatalf("fresh retry cursors = %+v, want none", resume.Frames)
	}
	// A conflicting plan for the same batch is rejected.
	if _, err := j.BeginPublish(pf.id, pf.res.Seed, pf.planDigest, [32]byte{2}, 2); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("changed layout digest: got %v", err)
	}
}

func TestPublishChunkValidation(t *testing.T) {
	j, _ := newJournal(t)
	pf := newPlan(t, j, 1, false)
	pf.beginPublish(t, j)
	first := pf.frames[0]
	meta := first.meta

	// Undeclared frame: no metadata and no stored head.
	if _, err := j.PublishChunk(pf.id, pf.planDigest, FrameAddr{5, 5, 5}, nil, 0, sha256.Sum256([]byte("x")), []byte("x")); !errors.Is(err, ErrBatchNotFound) {
		t.Fatalf("undeclared frame: got %v, want ErrBatchNotFound", err)
	}
	next, err := j.PublishChunk(pf.id, pf.planDigest, first.addr, &meta, 0, sha256.Sum256([]byte("abc")), []byte("abc"))
	if err != nil || next != 3 {
		t.Fatalf("first frame chunk: next=%d err=%v", next, err)
	}
	// Idempotent retry and conflicting rewrite.
	if next, err = j.PublishChunk(pf.id, pf.planDigest, first.addr, &meta, 0, sha256.Sum256([]byte("abc")), []byte("abc")); err != nil || next != 3 {
		t.Fatalf("retry: next=%d err=%v", next, err)
	}
	if _, err := j.PublishChunk(pf.id, pf.planDigest, first.addr, &meta, 0, sha256.Sum256([]byte("xyz")), []byte("xyz")); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("conflicting rewrite: got %v", err)
	}
	// A changed declaration is an immutable conflict.
	changed := meta
	changed.Capacity = 7
	changed.FragmentTotal = 7
	if _, err := j.PublishChunk(pf.id, pf.planDigest, first.addr, &changed, 3, sha256.Sum256([]byte("def")), []byte("def")); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("changed declaration: got %v", err)
	}
	// Gap reports the expected cursor.
	if next, err = j.PublishChunk(pf.id, pf.planDigest, first.addr, &meta, 5, sha256.Sum256([]byte("f")), []byte("f")); !errors.Is(err, ErrOffsetMismatch) || next != 3 {
		t.Fatalf("gap: next=%d err=%v, want cursor 3", next, err)
	}
	// Bytes beyond the declared frame total are rejected.
	if _, err := j.PublishChunk(pf.id, pf.planDigest, first.addr, &meta, 3, sha256.Sum256([]byte("defg")), []byte("defg")); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("overrun of declared total: got %v", err)
	}
	// A chunk may not exceed 64 KiB.
	big := make([]byte, MaxDataChunkBytes+1)
	if _, err := j.PublishChunk(pf.id, pf.planDigest, first.addr, &meta, 3, sha256.Sum256(big), big); !errors.Is(err, ErrChunkTooLarge) {
		t.Fatalf("oversized frame chunk: got %v", err)
	}
	// Wrong plan digest.
	if _, err := j.PublishChunk(pf.id, [32]byte{8}, first.addr, &meta, 3, sha256.Sum256([]byte("def")), []byte("def")); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("wrong plan digest: got %v", err)
	}
	// Invalid declarations.
	badForm := meta
	badForm.Form = 9
	if _, err := j.PublishChunk(pf.id, pf.planDigest, first.addr, &badForm, 3, sha256.Sum256([]byte("def")), []byte("def")); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("bad form: got %v", err)
	}
	badParts := FrameMeta{Form: 1, Capacity: 2, PartCount: 2, FragmentTotal: 9}
	if _, err := j.PublishChunk(pf.id, pf.planDigest, FrameAddr{2, 0, 0}, &badParts, 0, sha256.Sum256([]byte("ab")), []byte("ab")); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("parts cannot cover fragment: got %v", err)
	}
	badIndex := FrameMeta{Form: 1, Capacity: 2, PartCount: 2, FragmentTotal: 3}
	if _, err := j.PublishChunk(pf.id, pf.planDigest, FrameAddr{2, 0, 2}, &badIndex, 0, sha256.Sum256([]byte("ab")), []byte("ab")); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("frame index beyond part count: got %v", err)
	}
	// COMPLETE with a nonzero frame index is invalid.
	badComplete := FrameMeta{Form: 0, Capacity: 3, PartCount: 1, FragmentTotal: 3}
	if _, err := j.PublishChunk(pf.id, pf.planDigest, FrameAddr{3, 0, 1}, &badComplete, 0, sha256.Sum256([]byte("ab")), []byte("ab")); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("COMPLETE with frame index: got %v", err)
	}

	// Publish on a PREPARED batch that never began: rejected.
	other := newPlan(t, j, 2, false)
	if _, err := j.PublishChunk(other.id, other.planDigest, other.frames[0].addr, &other.frames[0].meta, 0, sha256.Sum256([]byte("abc")), []byte("abc")); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("publish before BeginPublish: got %v", err)
	}
}

func TestFinalizePublishValidation(t *testing.T) {
	j, _ := newJournal(t)
	pf := newPlan(t, j, 1, false)
	pf.beginPublish(t, j)

	// Incomplete frames.
	pf.publishFirstFrame(t, j)
	if err := j.FinalizePublish(pf.id, pf.planDigest, pf.res.ManifestDigest, pf.layout); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("incomplete frames: got %v, want ErrIncomplete", err)
	}
	pf.publishFrames(t, j)

	// A layout digest that disagrees with the declared entries: the plan
	// bound a wrong digest at BeginPublish, and the recomputation over the
	// declared entries must catch it.
	wrong := newPlan(t, j, 3, false)
	wrongLayout := [32]byte{4}
	if _, err := j.BeginPublish(wrong.id, wrong.res.Seed, wrong.planDigest, wrongLayout, uint64(len(wrong.entries))); err != nil {
		t.Fatal(err)
	}
	wrong.publishFrames(t, j)
	if err := j.FinalizePublish(wrong.id, wrong.planDigest, wrong.res.ManifestDigest, wrongLayout); !errors.Is(err, ErrLayoutMismatch) {
		t.Fatalf("wrong layout digest: got %v, want ErrLayoutMismatch", err)
	}
	// Wrong manifest digest binding.
	if err := j.FinalizePublish(pf.id, pf.planDigest, [32]byte{4}, pf.layout); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("wrong manifest digest: got %v, want ErrIdentityMismatch", err)
	}
	// Wrong layout digest binding (differs from the plan's declared digest).
	if err := j.FinalizePublish(pf.id, pf.planDigest, pf.res.ManifestDigest, [32]byte{5}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("wrong layout binding: got %v, want ErrIdentityMismatch", err)
	}
	// Correct close.
	if err := j.FinalizePublish(pf.id, pf.planDigest, pf.res.ManifestDigest, pf.layout); err != nil {
		t.Fatal(err)
	}
	info, err := j.Batch(pf.id)
	if err != nil || info.State != StateClosed {
		t.Fatalf("state after close: %+v %v", info, err)
	}
	if info.Closed == nil || info.Closed.PlanDigest != pf.planDigest {
		t.Fatalf("closed marker: %+v", info.Closed)
	}
	// Idempotent retry and conflicting digests.
	if err := j.FinalizePublish(pf.id, pf.planDigest, pf.res.ManifestDigest, pf.layout); err != nil {
		t.Fatal(err)
	}
	if err := j.FinalizePublish(pf.id, pf.planDigest, pf.res.ManifestDigest, [32]byte{4}); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("conflicting close: got %v", err)
	}
	// The stream cannot grow after CLOSED; stored retries stay no-ops.
	first := pf.frames[0]
	if _, err := j.PublishChunk(pf.id, pf.planDigest, first.addr, &first.meta, 6, sha256.Sum256([]byte("zz")), []byte("zz")); err == nil {
		t.Fatal("extension after CLOSED must be rejected")
	}
	if _, err := j.PublishChunk(pf.id, pf.planDigest, first.addr, &first.meta, 0, sha256.Sum256(first.bytes), first.bytes); err != nil {
		t.Fatal("idempotent retry after CLOSED must succeed")
	}
}

func (pf planFixture) publishFirstFrame(t *testing.T, j *Journal) {
	t.Helper()
	f := pf.frames[0]
	meta := f.meta
	if _, err := j.PublishChunk(pf.id, pf.planDigest, f.addr, &meta, 0, sha256.Sum256(f.bytes), f.bytes); err != nil {
		t.Fatal(err)
	}
}

func TestEntryCountMismatch(t *testing.T) {
	j, _ := newJournal(t)
	pf := newPlan(t, j, 1, false)
	// Declare three entries but bind only two: the layout digest check must
	// catch the count mismatch.
	if _, err := j.BeginPublish(pf.id, pf.res.Seed, pf.planDigest, pf.layout, 2); err != nil {
		t.Fatal(err)
	}
	pf.publishFrames(t, j)
	extra := FrameMeta{Form: 0, Capacity: 2, PartCount: 1, FragmentTotal: 2}
	if _, err := j.PublishChunk(pf.id, pf.planDigest, FrameAddr{7, 0, 0}, &extra, 0, sha256.Sum256([]byte("ab")), []byte("ab")); err != nil {
		t.Fatal(err)
	}
	if err := j.FinalizePublish(pf.id, pf.planDigest, pf.res.ManifestDigest, pf.layout); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("entry count mismatch: got %v, want ErrIncomplete", err)
	}
}

func TestSealImportModeSplit(t *testing.T) {
	j, _ := newJournal(t)
	normal := newPlan(t, j, 1, false)
	normal.beginPublish(t, j)
	normal.publishFrames(t, j)
	if err := j.FinalizePublish(normal.id, normal.planDigest, normal.res.ManifestDigest, normal.layout); err != nil {
		t.Fatal(err)
	}

	imported := newPlan(t, j, 2, true)
	imported.beginPublish(t, j)
	imported.publishFrames(t, j)
	// FinalizePublish refuses an IMPORT_SEALED batch; SealImport closes it.
	if err := j.FinalizePublish(imported.id, imported.planDigest, imported.res.ManifestDigest, imported.layout); !errors.Is(err, ErrModeConflict) {
		t.Fatalf("finalize on import batch: got %v, want ErrModeConflict", err)
	}
	if err := j.SealImport(imported.id, imported.planDigest, imported.res.ManifestDigest, imported.layout); err != nil {
		t.Fatal(err)
	}
	info, err := j.Batch(imported.id)
	if err != nil || info.State != StateClosed {
		t.Fatalf("import close: %+v %v", info, err)
	}
	if err := j.SealImport(normal.id, normal.planDigest, normal.res.ManifestDigest, normal.layout); !errors.Is(err, ErrModeConflict) {
		t.Fatalf("seal on normal batch: got %v, want ErrModeConflict", err)
	}
	// The imported reservation kept its original issuer epoch.
	info, err = j.Batch(imported.id)
	if err != nil || info.Reservation == nil || info.Reservation.IssuerEpoch != [16]byte{0xDD} {
		t.Fatalf("imported reservation: %+v %v", info.Reservation, err)
	}
}
