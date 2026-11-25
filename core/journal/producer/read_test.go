package producer

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// Reader tests of the producer journal (E03-01.producer_store): the pending
// list and the chunk streams, read back after writes that interleave across
// batches and streams, and the refusal of a hole.

// splitBytes cuts b into pieces of at most step bytes.
func splitBytes(b []byte, step int) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		n := step
		if n > len(b) {
			n = len(b)
		}
		out = append(out, b[:n])
		b = b[n:]
	}
	return out
}

// joinChunks concatenates chunks and returns their offsets.
func joinChunks(chunks ...[]byte) (offsets []uint64, joined []byte) {
	var off uint64
	for _, c := range chunks {
		offsets = append(offsets, off)
		joined = append(joined, c...)
		off += uint64(len(c))
	}
	return offsets, joined
}

// readAttempt collects what one attempt stream reader hands to its visitor.
func readAttempt(t *testing.T, s *Store, seq, generation uint64, manifest bool) ([]uint64, []byte, error) {
	t.Helper()
	var offsets []uint64
	var joined []byte
	read := s.AttemptData
	if manifest {
		read = s.AttemptManifest
	}
	err := read(seq, generation, func(off uint64, chunk []byte) error {
		offsets = append(offsets, off)
		joined = append(joined, chunk...)
		return nil
	})
	return offsets, joined, err
}

// readPlanChunks collects what PlanChunks hands to its visitor.
func readPlanChunks(t *testing.T, s *Store, seq, generation uint64) ([]uint64, []byte, error) {
	t.Helper()
	var offsets []uint64
	var joined []byte
	err := s.PlanChunks(seq, generation, func(off uint64, chunk []byte) error {
		offsets = append(offsets, off)
		joined = append(joined, chunk...)
		return nil
	})
	return offsets, joined, err
}

// openAttempt allocates one batch and opens its first attempt, without
// writing anything: the caller writes the chunks.
func openAttempt(t *testing.T, s *Store, decision uint64) (seq, generation uint64) {
	t.Helper()
	att, _, err := s.AllocateBatch(decision, 1)
	if err != nil {
		t.Fatal(err)
	}
	att, err = s.StartAttempt(att.BatchSeq, 1)
	if err != nil {
		t.Fatal(err)
	}
	return att.BatchSeq, att.Generation
}

// stageInterleaved walks one batch to READY, writing its data and manifest
// streams one chunk at a time in alternation.
func stageInterleaved(t *testing.T, s *Store, decision uint64, data, manifest []byte, dataStep, manifestStep int) (seq, generation uint64) {
	t.Helper()
	seq, generation = openAttempt(t, s, decision)
	d, m := splitBytes(data, dataStep), splitBytes(manifest, manifestStep)
	var offD, offM uint64
	for i := 0; i < len(d) || i < len(m); i++ {
		if i < len(d) {
			if err := s.WriteAttemptData(seq, generation, offD, d[i]); err != nil {
				t.Fatal(err)
			}
			offD += uint64(len(d[i]))
		}
		if i < len(m) {
			if err := s.WriteAttemptManifest(seq, generation, offM, m[i]); err != nil {
				t.Fatal(err)
			}
			offM += uint64(len(m[i]))
		}
	}
	if _, err := s.CommitReady(seq, generation, uint64(len(manifest)), []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}
	return seq, generation
}

// ackSealed records the IngestAck of a sealed batch and finishes its payload
// cleanup, which leaves the batch ACKED_CLEANUP.
func ackSealed(t *testing.T, s *Store, seq uint64) {
	t.Helper()
	info, err := s.Batch(seq)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.OnIngestAck(seq, IngestAck{ManifestDigest: info.Ready.ManifestDigest,
		PlanDigest: info.PlanDigest, LayoutDigest: info.LayoutDigest, BindingDigest: testBinding}); err != nil {
		t.Fatal(err)
	}
	for {
		done, err := s.Cleanup(seq, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
	}
}

// planCover returns the covered length of one plan stream.
func planCover(t *testing.T, s *Store, seq, generation uint64) uint64 {
	t.Helper()
	_, joined, err := readPlanChunks(t, s, seq, generation)
	if err != nil {
		t.Fatal(err)
	}
	return uint64(len(joined))
}

// pendingSeqs lists the batch sequences Pending answers.
func pendingSeqs(t *testing.T, s *Store) []uint64 {
	t.Helper()
	pending, err := s.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	var seqs []uint64
	for _, info := range pending {
		seqs = append(seqs, info.Seq)
	}
	return seqs
}

func TestPendingListsUnretiredBatchesInOrder(t *testing.T) {
	s, _ := newTestStore(t)
	if pending := pendingSeqs(t, s); len(pending) != 0 {
		t.Fatalf("a fresh store is pending %v, want none", pending)
	}

	// One batch in each state before RETIRED, allocated out of decision
	// order: the answer follows the batch sequence.
	intent, _, err := s.AllocateBatch(50, 1)
	if err != nil {
		t.Fatal(err)
	}
	staging, _, err := s.AllocateBatch(10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartAttempt(staging.BatchSeq, 1); err != nil {
		t.Fatal(err)
	}
	ready, _ := stageInterleaved(t, s, 20, []byte("data"), []byte("manifest"), 3, 4)
	acked, _ := stageInterleaved(t, s, 30, []byte("data"), []byte("manifest"), 3, 4)
	gen, _, err := s.BeginPlan(acked, []byte("seed"), [32]byte{7}, []byte("STREAM"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WritePlanChunk(acked, gen, 0, []byte("plan")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Seal(acked, gen, 4); err != nil {
		t.Fatal(err)
	}
	ackSealed(t, s, acked)

	pending, err := s.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	var states []State
	for _, info := range pending {
		states = append(states, info.State)
	}
	wantSeqs := []uint64{intent.BatchSeq, staging.BatchSeq, ready, acked}
	if seqs := pendingSeqs(t, s); !reflect.DeepEqual(seqs, wantSeqs) {
		t.Fatalf("pending %v, want %v", seqs, wantSeqs)
	}
	if want := []State{StateIntent, StateStaging, StateReady, StateAckedCleanup}; !reflect.DeepEqual(states, want) {
		t.Fatalf("pending states %v, want %v", states, want)
	}
	// Each entry is the whole durable state of its batch.
	if pending[2].Ready == nil || pending[2].Ready.EventCount != 1 || pending[3].Ack == nil {
		t.Fatalf("pending entries carry no markers: %+v", pending)
	}
	if pending[2].PlanGeneration != 0 || pending[3].PlanGeneration != gen || pending[3].CleanupDone != true {
		t.Fatalf("plan generations %d and %d (cleanup %v), want 0 and %d (cleanup done)",
			pending[2].PlanGeneration, pending[3].PlanGeneration, pending[3].CleanupDone, gen)
	}
}

func TestPendingLeavesOutRetiredBatches(t *testing.T) {
	s, st := newTestStore(t)
	// Two batches in sequence; release is ordered, so the first retires
	// while the second waits in ACKED_CLEANUP.
	first, second := uint64(0), uint64(0)
	for i := 1; i <= 2; i++ {
		seq, _ := stageInterleaved(t, s, uint64(i), []byte("data"), []byte("manifest"), 3, 4)
		gen, _, err := s.BeginPlan(seq, []byte("seed"), [32]byte{7}, []byte("STREAM"))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.WritePlanChunk(seq, gen, 0, []byte("plan")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Seal(seq, gen, 4); err != nil {
			t.Fatal(err)
		}
		ackSealed(t, s, seq)
		if i == 1 {
			first = seq
		} else {
			second = seq
		}
	}
	if err := s.OnReleaseAck(first); err != nil {
		t.Fatal(err)
	}
	if seqs := pendingSeqs(t, s); !reflect.DeepEqual(seqs, []uint64{second}) {
		t.Fatalf("pending after one retire: %v, want only batch %d", seqs, second)
	}
	if err := s.OnReleaseAck(second); err != nil {
		t.Fatal(err)
	}
	if seqs := pendingSeqs(t, s); len(seqs) != 0 {
		t.Fatalf("pending after every batch retired: %v, want none", seqs)
	}

	// An unreadable state marker is corruption of the whole list, never a
	// batch left out, also on a batch that is already RETIRED.
	if err := st.Put(batchKey(first, stateSuffix), []byte{9}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pending(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Pending over a corrupt marker: %v, want ErrCorrupt", err)
	}
}

func TestAttemptStreamsAfterInterleavedWrites(t *testing.T) {
	s, _ := newTestStore(t)

	type stream struct {
		seq, gen uint64
		manifest bool
		chunks   [][]byte
		written  int // chunks written so far
		offset   uint64
	}
	payloads := []struct {
		data, manifest  string
		dataStep, mStep int
	}{
		{"record fragment bytes of the first batch", "extractor.manifest.v2\x00first", 3, 5},
		{"second batch record", "extractor.manifest.v2\x00second batch manifest bytes", 7, 2},
		{"third", "extractor.manifest.v2\x00third manifest", 2, 9},
	}
	// Two streams per batch, next to each other in the list.
	var streams []*stream
	for i, p := range payloads {
		seq, gen := openAttempt(t, s, uint64(i+1))
		streams = append(streams,
			&stream{seq: seq, gen: gen, chunks: splitBytes([]byte(p.data), p.dataStep)},
			&stream{seq: seq, gen: gen, manifest: true, chunks: splitBytes([]byte(p.manifest), p.mStep)})
	}
	check := func(round int) {
		t.Helper()
		for i, str := range streams {
			offsets, got, err := readAttempt(t, s, str.seq, str.gen, str.manifest)
			if err != nil {
				t.Fatalf("round %d stream %d: %v", round, i, err)
			}
			wantOffsets, want := joinChunks(str.chunks[:str.written]...)
			if !reflect.DeepEqual(offsets, wantOffsets) || !bytes.Equal(got, want) {
				t.Fatalf("round %d stream %d: offsets %v bytes %q, want %v %q",
					round, i, offsets, got, wantOffsets, want)
			}
		}
	}
	check(0)

	// Round robin, each round starting at the next stream, so no two rounds
	// write in the same order and every stream is read while it grows.
	for round := 1; ; round++ {
		wrote := false
		for i := range streams {
			str := streams[(round+i)%len(streams)]
			if str.written == len(str.chunks) {
				continue
			}
			chunk := str.chunks[str.written]
			var err error
			if str.manifest {
				err = s.WriteAttemptManifest(str.seq, str.gen, str.offset, chunk)
			} else {
				err = s.WriteAttemptData(str.seq, str.gen, str.offset, chunk)
			}
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
			str.written++
			str.offset += uint64(len(chunk))
			wrote = true
		}
		check(round)
		if !wrote {
			break
		}
	}

	// Every stream is complete and separate: a reader hands out the chunks
	// of its own batch and kind, never those of the stream next to it.
	for i, str := range streams {
		if str.written != len(str.chunks) {
			t.Fatalf("stream %d stopped at %d of %d chunks", i, str.written, len(str.chunks))
		}
	}
}

func TestAttemptStreamGapIsError(t *testing.T) {
	s, st := newTestStore(t)
	seq, gen := stageInterleaved(t, s, 1, []byte("0123456789"), []byte("manifest bytes"), 4, 4)

	// A chunk beyond the end refuses the whole stream.
	orphan := append(append([]byte(nil), attemptKey(seq, gen, "data")...), u64be(40)...)
	if err := st.Put(orphan, []byte("orphan")); err != nil {
		t.Fatal(err)
	}
	// The contiguous prefix is handed over first; a stream reader cannot
	// know of a hole further on before it reaches it.
	if offsets, got, err := readAttempt(t, s, seq, gen, false); !errors.Is(err, ErrOffsetGap) ||
		!reflect.DeepEqual(offsets, []uint64{0, 4, 8}) || string(got) != "0123456789" {
		t.Fatalf("chunk beyond the end: %v %q %v, want [0 4 8] then ErrOffsetGap", offsets, got, err)
	}
	if err := st.Delete(orphan); err != nil {
		t.Fatal(err)
	}

	// A hole in the middle does the same; the manifest stream is whole and
	// still reads.
	middle := append(append([]byte(nil), attemptKey(seq, gen, "data")...), u64be(4)...)
	if err := st.Delete(middle); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readAttempt(t, s, seq, gen, false); !errors.Is(err, ErrOffsetGap) {
		t.Fatalf("hole in the stream: %v, want ErrOffsetGap", err)
	}
	if _, got, err := readAttempt(t, s, seq, gen, true); err != nil || string(got) != "manifest bytes" {
		t.Fatalf("manifest after a data hole: %q %v", got, err)
	}

	// A key of another shape under the prefix is corruption.
	short := append(append([]byte(nil), attemptKey(seq, gen, "manifest")...), 0x01)
	if err := st.Put(short, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readAttempt(t, s, seq, gen, true); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("key of another shape: %v, want ErrCorrupt", err)
	}

	// A plan stream refuses a hole the same way.
	seq2, _ := stageInterleaved(t, s, 2, []byte("data"), []byte("manifest"), 2, 2)
	plan, _, err := s.BeginPlan(seq2, []byte("seed"), [32]byte{7}, []byte("STREAM"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range [][]byte{[]byte("aaaa"), []byte("bbbb"), []byte("cccc")} {
		if err := s.WritePlanChunk(seq2, plan, planCover(t, s, seq2, plan), c); err != nil {
			t.Fatal(err)
		}
	}
	if offsets, got, err := readPlanChunks(t, s, seq2, plan); err != nil || !bytes.Equal(got, []byte("aaaabbbbcccc")) ||
		!reflect.DeepEqual(offsets, []uint64{0, 4, 8}) {
		t.Fatalf("plan chunks: %v %q %v", offsets, got, err)
	}
	second := append(append([]byte(nil), planGenKey(seq2, plan, "chunks/")...), u64be(4)...)
	if err := st.Delete(second); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readPlanChunks(t, s, seq2, plan); !errors.Is(err, ErrOffsetGap) {
		t.Fatalf("plan hole: %v, want ErrOffsetGap", err)
	}
}

func TestAttemptStreamGuards(t *testing.T) {
	s, _ := newTestStore(t)
	nothing := func(uint64, []byte) error { return nil }

	// An unknown batch is not found, in every reader.
	for name, err := range map[string]error{
		"data":     s.AttemptData(9, 1, nothing),
		"manifest": s.AttemptManifest(9, 1, nothing),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown batch %s: %v, want ErrNotFound", name, err)
		}
	}

	// An INTENT batch holds no attempt, whatever generation is asked for.
	att, _, err := s.AllocateBatch(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, gen := range []uint64{0, 1} {
		if err := s.AttemptData(att.BatchSeq, gen, nothing); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("INTENT batch generation %d: %v, want ErrStateConflict", gen, err)
		}
	}

	// A replaced attempt is no longer the current one.
	first, err := s.StartAttempt(att.BatchSeq, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAttemptData(att.BatchSeq, first.Generation, 0, []byte("old attempt")); err != nil {
		t.Fatal(err)
	}
	second, err := s.StartAttempt(att.BatchSeq, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, got, err := readAttempt(t, s, att.BatchSeq, first.Generation, false); !errors.Is(err, ErrStateConflict) || got != nil {
		t.Fatalf("replaced attempt: %q %v, want ErrStateConflict", got, err)
	}
	if _, got, err := readAttempt(t, s, att.BatchSeq, second.Generation, false); err != nil || got != nil {
		t.Fatalf("current attempt: %q %v, want an empty stream", got, err)
	}
	if err := s.WriteAttemptData(att.BatchSeq, second.Generation, 0, []byte("new attempt")); err != nil {
		t.Fatal(err)
	}
	if _, got, err := readAttempt(t, s, att.BatchSeq, second.Generation, false); err != nil || string(got) != "new attempt" {
		t.Fatalf("current attempt: %q %v", got, err)
	}

	// After READY the generation is still the READY attempt.
	if _, err := s.CommitReady(att.BatchSeq, second.Generation, 0, nil); err != nil { // no manifest chunks were written
		t.Fatal(err)
	}
	if _, _, err := readAttempt(t, s, att.BatchSeq, first.Generation, false); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("replaced attempt after READY: %v, want ErrStateConflict", err)
	}
	if _, got, err := readAttempt(t, s, att.BatchSeq, second.Generation, false); err != nil || string(got) != "new attempt" {
		t.Fatalf("READY attempt after commit: %q %v", got, err)
	}

	// A nil visitor is refused, a failing visitor's error is returned as is.
	if err := s.AttemptData(att.BatchSeq, second.Generation, nil); err == nil {
		t.Fatal("a nil visitor must be refused")
	}
	boom := errors.New("visitor failed")
	calls := 0
	if err := s.AttemptData(att.BatchSeq, second.Generation, func(uint64, []byte) error { calls++; return boom }); !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("visitor error: %v after %d calls, want the visitor's error after one call", err, calls)
	}

	// A visitor may stop early without an error.
	seq, gen := stageInterleaved(t, s, 2, []byte("0123456789"), []byte("manifest bytes"), 2, 3)
	var seen []uint64
	err = s.AttemptData(seq, gen, func(off uint64, chunk []byte) error {
		seen = append(seen, off)
		if off == 2 {
			return storage.ErrStopIterate
		}
		return nil
	})
	if err != nil || !reflect.DeepEqual(seen, []uint64{0, 2}) {
		t.Fatalf("early stop: visited %v with error %v, want [0 2] and none", seen, err)
	}
}

func TestPlanReader(t *testing.T) {
	s, _ := newTestStore(t)
	nothing := func(uint64, []byte) error { return nil }

	// An unknown batch is not found.
	if _, _, _, _, err := s.Plan(9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown batch: %v, want ErrNotFound", err)
	}
	if err := s.PlanChunks(9, 1, nothing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown batch chunks: %v, want ErrNotFound", err)
	}

	// Before READY a batch holds no plan.
	att, _, err := s.AllocateBatch(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := s.Plan(att.BatchSeq); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("INTENT batch: %v, want ErrStateConflict", err)
	}
	if err := s.PlanChunks(att.BatchSeq, 1, nothing); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("INTENT batch chunks: %v, want ErrStateConflict", err)
	}
	if _, err := s.StartAttempt(att.BatchSeq, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := s.Plan(att.BatchSeq); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("STAGING batch: %v, want ErrStateConflict", err)
	}

	// READY without a plan: none was begun yet.
	seq, attempt := stageInterleaved(t, s, 2, []byte("data"), []byte("manifest"), 2, 2)
	if _, _, _, _, err := s.Plan(seq); !errors.Is(err, ErrNotFound) {
		t.Fatalf("READY batch without a plan: %v, want ErrNotFound", err)
	}
	if err := s.PlanChunks(seq, 1, nothing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("chunks of a generation never opened: %v, want ErrNotFound", err)
	}

	// The first plan generation: seed, layout digest, header and chunks.
	seed := []byte("extractor.reservation.v2 seed of the first reservation")
	gen, _, err := s.BeginPlan(seq, seed, [32]byte{11}, []byte("EXTRACTOR_BSC_TESTNET_V3"))
	if err != nil || gen != 1 {
		t.Fatalf("BeginPlan: gen %d %v, want 1", gen, err)
	}
	if offsets, got, err := readPlanChunks(t, s, seq, gen); err != nil || offsets != nil || got != nil {
		t.Fatalf("empty plan stream: %v %q %v, want no chunks", offsets, got, err)
	}
	gotGen, gotSeed, gotLayout, gotHeader, err := s.Plan(seq)
	if err != nil || gotGen != 1 || !bytes.Equal(gotSeed, seed) || gotLayout != [32]byte{11} ||
		string(gotHeader) != "EXTRACTOR_BSC_TESTNET_V3" {
		t.Fatalf("Plan: gen %d seed %q layout %x header %q %v", gotGen, gotSeed, gotLayout, gotHeader, err)
	}
	// The answer is a copy: the caller cannot reach into the store.
	gotSeed[0], gotHeader[0] = 'X', 'X'
	if _, again, _, againHeader, _ := s.Plan(seq); !bytes.Equal(again, seed) || string(againHeader) != "EXTRACTOR_BSC_TESTNET_V3" {
		t.Fatalf("Plan handed out store memory: seed %q header %q", again, againHeader)
	}
	for _, c := range [][]byte{[]byte("record one"), []byte("record two, longer"), []byte("three")} {
		if err := s.WritePlanChunk(seq, gen, planCover(t, s, seq, gen), c); err != nil {
			t.Fatal(err)
		}
	}
	if offsets, got, err := readPlanChunks(t, s, seq, gen); err != nil || !bytes.Equal(got, []byte("record onerecord two, longerthree")) ||
		!reflect.DeepEqual(offsets, []uint64{0, 10, 28}) {
		t.Fatalf("plan chunks: %v %q %v", offsets, got, err)
	}

	// An abandoned plan followed by a new one: the reader answers the new
	// generation, and the old chunks stay until the cleanup.
	if err := s.AbandonPlan(seq); err != nil {
		t.Fatal(err)
	}
	seed2 := []byte("extractor.reservation.v2 seed of the second reservation")
	gen2, _, err := s.BeginPlan(seq, seed2, [32]byte{12}, []byte("EXTRACTOR_BSC_TESTNET_V3"))
	if err != nil || gen2 != 2 {
		t.Fatalf("second BeginPlan: gen %d %v, want 2", gen2, err)
	}
	if gotGen, gotSeed, gotLayout, _, err := s.Plan(seq); err != nil || gotGen != 2 || !bytes.Equal(gotSeed, seed2) || gotLayout != [32]byte{12} {
		t.Fatalf("Plan after the new generation: gen %d seed %q layout %x %v", gotGen, gotSeed, gotLayout, err)
	}
	if _, got, err := readPlanChunks(t, s, seq, 1); err != nil || !bytes.Equal(got, []byte("record onerecord two, longerthree")) {
		t.Fatalf("chunks of the abandoned generation: %q %v", got, err)
	}
	if _, got, err := readPlanChunks(t, s, seq, 2); err != nil || got != nil {
		t.Fatalf("chunks of the new generation: %q %v, want no chunks", got, err)
	}
	if err := s.PlanChunks(seq, 3, nothing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("chunks of a generation never opened: %v, want ErrNotFound", err)
	}

	// Sealed: the plan stays readable and the generation is the sealed one.
	if err := s.WritePlanChunk(seq, gen2, 0, []byte("sealed plan")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Seal(seq, gen2, 11); err != nil {
		t.Fatal(err)
	}
	if gotGen, gotSeed, _, gotHeader, err := s.Plan(seq); err != nil || gotGen != 2 || !bytes.Equal(gotSeed, seed2) ||
		string(gotHeader) != "EXTRACTOR_BSC_TESTNET_V3" {
		t.Fatalf("Plan after Seal: gen %d seed %q header %q %v", gotGen, gotSeed, gotHeader, err)
	}
	if _, got, err := readPlanChunks(t, s, seq, 2); err != nil || string(got) != "sealed plan" {
		t.Fatalf("sealed plan chunks: %q %v", got, err)
	}

	// The payload cleanup removes the plan with the attempt chunks.
	ackSealed(t, s, seq)
	if _, _, _, _, err := s.Plan(seq); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Plan after the cleanup: %v, want ErrNotFound", err)
	}
	if err := s.PlanChunks(seq, 2, nothing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PlanChunks after the cleanup: %v, want ErrNotFound", err)
	}
	if _, got, err := readAttempt(t, s, seq, attempt, false); err != nil || got != nil {
		t.Fatalf("attempt after the cleanup: %q %v, want an empty stream", got, err)
	}
}
