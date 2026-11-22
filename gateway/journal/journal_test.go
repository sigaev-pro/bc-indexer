package journal

import (
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

var testBinding [32]byte

func init() {
	testBinding = sha256.Sum256([]byte("gateway test binding"))
}

func newJournal(t *testing.T) (*Journal, *storage.MemoryStorage) {
	t.Helper()
	st := storage.NewMemory()
	j, err := Open(st, Options{BindingDigest: testBinding})
	if err != nil {
		t.Fatal(err)
	}
	return j, st
}

func testBatch(seq uint64) BatchID {
	return BatchID{ProducerID: [16]byte{1}, RunID: [16]byte{2}, Seq: seq}
}

// chunkify writes a byte stream through PrepareChunk in bounded pieces at
// absolute offsets starting at base.
func chunkify(t *testing.T, j *Journal, id BatchID, digest [32]byte, base uint64, stream []byte, size int) {
	t.Helper()
	for off := 0; off < len(stream); off += size {
		end := off + size
		if end > len(stream) {
			end = len(stream)
		}
		data := stream[off:end]
		if _, err := j.PrepareChunk(id, digest, base+uint64(off), sha256.Sum256(data), data); err != nil {
			t.Fatal(err)
		}
	}
}

func prepareBatch(t *testing.T, j *Journal, id BatchID, manifest []byte, eventCount uint64) Reservation {
	t.Helper()
	digest := sha256.Sum256(manifest)
	if _, err := j.BeginPrepare(id, digest, eventCount, uint64(len(manifest)), PrepareNormal, nil); err != nil {
		t.Fatal(err)
	}
	chunkify(t, j, id, digest, 0, manifest, 8)
	res, err := j.FinalizePrepare(id, digest)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestOpenKeepsStoreEpoch(t *testing.T) {
	j, st := newJournal(t)
	epoch := j.StoreEpoch()
	if epoch == ([16]byte{}) {
		t.Fatal("fresh store must generate an epoch")
	}
	again, err := Open(st, Options{BindingDigest: testBinding})
	if err != nil {
		t.Fatal(err)
	}
	if again.StoreEpoch() != epoch {
		t.Fatal("store_epoch must survive reopen")
	}
	other, err := Open(storage.NewMemory(), Options{BindingDigest: testBinding})
	if err != nil {
		t.Fatal(err)
	}
	if other.StoreEpoch() == epoch {
		t.Fatal("independent stores generated the same epoch")
	}
}

func TestBeginPrepareResumeAndConflicts(t *testing.T) {
	j, _ := newJournal(t)
	id := testBatch(1)
	manifest := []byte("extractor.manifest.v2\x00descriptors")
	digest := sha256.Sum256(manifest)

	if _, err := j.BeginPrepare(id, digest, 2, uint64(len(manifest)), PrepareNormal, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := j.PrepareChunk(id, digest, 0, sha256.Sum256(manifest[:5]), manifest[:5]); err != nil {
		t.Fatal(err)
	}
	// Reopening the same stream resumes at the durable offset; a different
	// digest for the same batch is a conflict.
	resume, err := j.BeginPrepare(id, digest, 2, uint64(len(manifest)), PrepareNormal, nil)
	if err != nil || resume.Reservation != nil || resume.ResumeOffset != 5 {
		t.Fatalf("resume: %+v err=%v, want offset 5", resume, err)
	}
	if _, err := j.BeginPrepare(id, [32]byte{9}, 2, 8, PrepareNormal, nil); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("second digest: got %v, want ErrImmutableConflict", err)
	}
	if _, err := j.BeginPrepare(id, digest, 0, 8, PrepareNormal, nil); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("zero event count: got %v, want ErrIncomplete", err)
	}
	// Unknown batch chunks are rejected.
	if _, err := j.PrepareChunk(testBatch(9), digest, 0, sha256.Sum256([]byte("x")), []byte("x")); !errors.Is(err, ErrBatchNotFound) {
		t.Fatalf("unknown batch: got %v, want ErrBatchNotFound", err)
	}
}

func TestPrepareChunkRules(t *testing.T) {
	j, _ := newJournal(t)
	id := testBatch(1)
	digest := sha256.Sum256([]byte("manifest"))
	if _, err := j.BeginPrepare(id, digest, 1, 16, PrepareNormal, nil); err != nil {
		t.Fatal(err)
	}

	next, err := j.PrepareChunk(id, digest, 0, sha256.Sum256([]byte("aaaa")), []byte("aaaa"))
	if err != nil || next != 4 {
		t.Fatalf("first chunk: next=%d err=%v", next, err)
	}
	// Idempotent retry returns the same next offset.
	next, err = j.PrepareChunk(id, digest, 0, sha256.Sum256([]byte("aaaa")), []byte("aaaa"))
	if err != nil || next != 4 {
		t.Fatalf("retry: next=%d err=%v", next, err)
	}
	// A gap reports the expected cursor.
	next, err = j.PrepareChunk(id, digest, 8, sha256.Sum256([]byte("bbbb")), []byte("bbbb"))
	if !errors.Is(err, ErrOffsetMismatch) || next != 4 {
		t.Fatalf("gap: next=%d err=%v, want cursor 4 and ErrOffsetMismatch", next, err)
	}
	// A conflicting rewrite is an immutable conflict.
	next, err = j.PrepareChunk(id, digest, 0, sha256.Sum256([]byte("AAAA")), []byte("AAAA"))
	if !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("conflicting rewrite: got %v", err)
	}
	// A digest that does not match the payload is rejected.
	if _, err := j.PrepareChunk(id, digest, 4, sha256.Sum256([]byte("nope")), []byte("bbbb")); err == nil {
		t.Fatal("payload digest mismatch must be rejected")
	}
	// The data chunk bound is 64 KiB.
	big := make([]byte, MaxDataChunkBytes+1)
	if _, err := j.PrepareChunk(id, digest, 4, sha256.Sum256(big), big); !errors.Is(err, ErrChunkTooLarge) {
		t.Fatalf("oversized chunk: got %v, want ErrChunkTooLarge", err)
	}
	// Wrong manifest digest binding.
	if _, err := j.PrepareChunk(id, [32]byte{7}, 4, sha256.Sum256([]byte("b")), []byte("b")); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("wrong digest binding: got %v", err)
	}
}

func TestFinalizePrepareValidations(t *testing.T) {
	j, _ := newJournal(t)
	id := testBatch(1)
	manifest := []byte("manifest-bytes")
	digest := sha256.Sum256(manifest)

	if _, err := j.BeginPrepare(id, digest, 1, uint64(len(manifest)), PrepareNormal, nil); err != nil {
		t.Fatal(err)
	}
	chunkify(t, j, id, digest, 0, manifest[:4], 4)
	if _, err := j.FinalizePrepare(id, digest); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("incomplete stream: got %v, want ErrIncomplete", err)
	}
	chunkify(t, j, id, digest, 4, manifest[4:], 4)
	// The declared digest was computed over different bytes: complete but
	// mismatching.
	if _, err := j.FinalizePrepare(id, [32]byte{3}); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("foreign digest: got %v, want ErrImmutableConflict", err)
	}
	// Begin a batch whose declared digest never matches its bytes.
	id2 := testBatch(2)
	if _, err := j.BeginPrepare(id2, [32]byte{5}, 1, 4, PrepareNormal, nil); err != nil {
		t.Fatal(err)
	}
	chunkify(t, j, id2, [32]byte{5}, 0, []byte("abcd"), 4)
	if _, err := j.FinalizePrepare(id2, [32]byte{5}); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("digest mismatch: got %v, want ErrManifestMismatch", err)
	}
}

// The acceptance case of E03-01.gateway_store: a reservation retry must not
// allocate a new sequence; the saved seed returns byte-for-byte.
func TestReservationRetryKeepsSequence(t *testing.T) {
	j, _ := newJournal(t)
	id := testBatch(1)
	manifest := []byte("extractor.manifest.v2\x00events")
	res := prepareBatch(t, j, id, manifest, 3)
	if res.SequenceBase != 1 {
		t.Fatalf("first sequence base = %d, want 1", res.SequenceBase)
	}
	seed := append([]byte(nil), res.Seed...)

	// Retry FinalizePrepare: identical seed, nothing consumed.
	again, err := j.FinalizePrepare(id, sha256.Sum256(manifest))
	if err != nil {
		t.Fatal(err)
	}
	if string(again.Seed) != string(seed) || again.ReservationID != res.ReservationID ||
		again.SequenceBase != res.SequenceBase || again.EventCount != res.EventCount {
		t.Fatalf("finalize retry changed the reservation: %+v vs %+v", again, res)
	}
	// Retry the whole BeginPrepare: the reservation answers directly.
	resume, err := j.BeginPrepare(id, sha256.Sum256(manifest), 3, uint64(len(manifest)), PrepareNormal, nil)
	if err != nil || resume.Reservation == nil {
		t.Fatalf("begin retry: %+v %v", resume, err)
	}
	if string(resume.Reservation.Seed) != string(seed) {
		t.Fatal("BeginPrepare retry must return the saved seed byte-for-byte")
	}
	// A different digest after the reservation is a conflict, never a new
	// allocation.
	if _, err := j.BeginPrepare(id, [32]byte{1}, 3, 4, PrepareNormal, nil); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("conflicting digest after reservation: got %v", err)
	}

	// The next batch's sequence range continues exactly after the first:
	// no value was consumed twice, no gap was introduced by the retries.
	next := prepareBatch(t, j, testBatch(2), []byte("second"), 2)
	if next.SequenceBase != res.SequenceBase+3 {
		t.Fatalf("second sequence base = %d, want %d (retry must not shift it)", next.SequenceBase, res.SequenceBase+3)
	}
}

func TestSeedLayoutMatchesContract(t *testing.T) {
	j, _ := newJournal(t)
	id := testBatch(4)
	res := prepareBatch(t, j, id, []byte("m"), 1)

	want := BuildSeed(SeedLayout{
		ReservationID:  res.ReservationID,
		IssuerEpoch:    j.StoreEpoch(),
		Batch:          id,
		ManifestDigest: res.ManifestDigest,
		EventCount:     1,
		SequenceBase:   res.SequenceBase,
		BindingDigest:  testBinding,
		FrameLimit:     DefaultFrameLimit,
	})
	if string(res.Seed) != string(want) {
		t.Fatalf("seed bytes differ from the contract layout:\n got %x\nwant %x", res.Seed, want)
	}
	if len(res.Seed) != seedTotalLen {
		t.Fatalf("seed length %d, want %d", len(res.Seed), seedTotalLen)
	}
	if PlanDigest(res.Seed) != sha256.Sum256(res.Seed) {
		t.Fatal("PlanDigest must be SHA-256 of the seed bytes")
	}
	parsed, err := ParseSeed(res.Seed)
	if err != nil || parsed.Batch != id || parsed.EventCount != 1 {
		t.Fatalf("ParseSeed: %+v %v", parsed, err)
	}
	if _, err := ParseSeed(append([]byte(nil), res.Seed[:len(res.Seed)-1]...)); !errors.Is(err, ErrInvalidSeed) {
		t.Fatalf("truncated seed: got %v, want ErrInvalidSeed", err)
	}
}

func TestImportSealedSeed(t *testing.T) {
	j, _ := newJournal(t)
	id := testBatch(1)
	manifest := []byte("recovered-manifest")
	digest := sha256.Sum256(manifest)

	original := BuildSeed(SeedLayout{
		ReservationID:  [16]byte{0xAA},
		IssuerEpoch:    [16]byte{0xBB}, // the lost store's epoch, not ours
		Batch:          id,
		ManifestDigest: digest,
		EventCount:     2,
		SequenceBase:   77,
		BindingDigest:  testBinding,
		FrameLimit:     DefaultFrameLimit,
	})

	if _, err := j.BeginPrepare(id, digest, 2, uint64(len(manifest)), PrepareImportSealed, original); err != nil {
		t.Fatal(err)
	}
	chunkify(t, j, id, digest, 0, manifest, 5)
	res, err := j.FinalizePrepare(id, digest)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Seed) != string(original) {
		t.Fatal("the imported seed must be preserved byte-for-byte")
	}
	if res.IssuerEpoch != [16]byte{0xBB} || res.SequenceBase != 77 {
		t.Fatalf("imported identity changed: %+v", res)
	}
	// No sequence was allocated: the next normal batch starts at 1.
	next := prepareBatch(t, j, testBatch(2), []byte("normal"), 1)
	if next.SequenceBase != 1 {
		t.Fatalf("import allocated sequence: next base = %d, want 1", next.SequenceBase)
	}
	// Mode mixing is rejected both ways: an import seed bound to batch 3
	// opens an import stream, and a normal prepare for that stream conflicts.
	b3 := testBatch(3)
	seed3 := BuildSeed(SeedLayout{
		ReservationID: [16]byte{0xEE}, IssuerEpoch: [16]byte{0xFF}, Batch: b3,
		ManifestDigest: digest, EventCount: 2, SequenceBase: 100,
		BindingDigest: testBinding, FrameLimit: DefaultFrameLimit,
	})
	if _, err := j.BeginPrepare(b3, digest, 2, 4, PrepareImportSealed, seed3); err != nil {
		t.Fatal(err)
	}
	if _, err := j.BeginPrepare(b3, sha256.Sum256([]byte("n")), 2, 4, PrepareNormal, nil); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("normal prepare on import stream: got %v", err)
	}

	// An import seed that does not match the batch identity is invalid.
	badBatch := BuildSeed(SeedLayout{
		ReservationID: [16]byte{1}, IssuerEpoch: [16]byte{2}, Batch: testBatch(9),
		ManifestDigest: digest, EventCount: 2, SequenceBase: 1,
		BindingDigest: testBinding, FrameLimit: DefaultFrameLimit,
	})
	if _, err := j.BeginPrepare(testBatch(5), digest, 2, 4, PrepareImportSealed, badBatch); !errors.Is(err, ErrInvalidSeed) {
		t.Fatalf("foreign batch in seed: got %v, want ErrInvalidSeed", err)
	}
	wrongBinding := BuildSeed(SeedLayout{
		ReservationID: [16]byte{1}, IssuerEpoch: [16]byte{2}, Batch: testBatch(6),
		ManifestDigest: digest, EventCount: 2, SequenceBase: 1,
		BindingDigest: [32]byte{9}, FrameLimit: DefaultFrameLimit,
	})
	if _, err := j.BeginPrepare(testBatch(6), digest, 2, 4, PrepareImportSealed, wrongBinding); !errors.Is(err, ErrInvalidSeed) {
		t.Fatalf("foreign binding in seed: got %v, want ErrInvalidSeed", err)
	}
	if _, err := j.BeginPrepare(testBatch(7), digest, 2, 4, PrepareImportSealed, []byte("garbage")); !errors.Is(err, ErrInvalidSeed) {
		t.Fatalf("garbage seed: got %v, want ErrInvalidSeed", err)
	}
}

func TestAlreadyRetiredGuardsCreateNoKeys(t *testing.T) {
	j, st := newJournal(t)
	id := testBatch(1)
	if err := j.retireForTest(id); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("late"))
	if _, err := j.BeginPrepare(id, digest, 1, 4, PrepareNormal, nil); !errors.Is(err, ErrAlreadyRetired) {
		t.Fatalf("retired BeginPrepare: got %v, want ErrAlreadyRetired", err)
	}
	if _, err := j.PublishChunk(id, digest, FrameAddr{}, nil, 0, sha256.Sum256([]byte("x")), []byte("x")); !errors.Is(err, ErrAlreadyRetired) {
		t.Fatalf("retired PublishChunk: got %v, want ErrAlreadyRetired", err)
	}
	if err := j.FinalizePublish(id, digest, digest, digest); !errors.Is(err, ErrAlreadyRetired) {
		t.Fatalf("retired FinalizePublish: got %v, want ErrAlreadyRetired", err)
	}
	lo, hi := keyRange(batchKey(id, ""))
	count := 0
	_ = st.Iterate(lo, hi, func(k, v []byte) error { count++; return nil })
	if count != 0 {
		t.Fatalf("retired guard created %d keys", count)
	}
}

// retireForTest advances the retired prefix directly (the ordered release
// path is covered in release_test.go).
func (j *Journal) retireForTest(id BatchID) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st.Put(retiredKey(id), u64be(id.Seq))
}
