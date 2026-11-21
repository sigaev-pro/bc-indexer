// Tests of the FragmentWriter: layout-driven COMPLETE/PART round-trips
// through the FragmentReader, the exact-fit boundary around one 65536-byte
// fragment, plan and budget rejections, streamed chunk semantics with
// bounded reads, and the memory-bound verification (no second full copy of
// the record).
package codec

import (
	"bytes"
	"io"
	"runtime"
	"sort"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// batchFixture builds a record's fragments and the layout plan that carries
// them under one budget.
type batchFixture struct {
	event     proto.EventDescriptor
	frags     []proto.FragmentDescriptor
	fragments [][]byte
	plan      *proto.LayoutPlan
	budget    uint64
	stream    string
	seqBase   uint64
}

// newBatchFixture plans one event with one tree-built record.
func newBatchFixture(t *testing.T, budget uint64, nodes []TreeNode) *batchFixture {
	t.Helper()
	f := &batchFixture{budget: budget, stream: "EXT_TX", seqBase: 1000}
	f.fragments = buildTreeFragments(t, nodes)
	event, frags, err := proto.BuildEventDescriptor(0, testEventID(7), proto.FamilyTx,
		proto.KindApplied, f.fragments)
	if err != nil {
		t.Fatalf("BuildEventDescriptor: %v", err)
	}
	f.event, f.frags = event, frags
	lengths := make([]uint64, len(frags))
	for i, frag := range frags {
		lengths[i] = frag.EncodedLength
	}
	plan, err := proto.ComputeLayout(proto.LayoutRequest{Budget: budget, Stream: f.stream,
		Network: testNetwork(), PublicationEpoch: testEpoch(), SequenceBase: f.seqBase,
		Events: []proto.EventFragments{{Ordinal: 0, FragmentLengths: lengths}}})
	if err != nil {
		t.Fatalf("ComputeLayout: %v", err)
	}
	f.plan = plan
	return f
}

// writer builds the FragmentWriter of the fixture.
func (f *batchFixture) writer(t *testing.T) *FragmentWriter {
	t.Helper()
	writer, err := NewFragmentWriter(f.plan, WriterConfig{Network: testNetwork(),
		EventID: f.event.EventID, RecordDigest: f.event.RecordDigest,
		PublicationEpoch: testEpoch(), FragmentCount: f.event.FragmentCount})
	if err != nil {
		t.Fatalf("NewFragmentWriter: %v", err)
	}
	return writer
}

// publishAll writes every fragment of the fixture into the sink.
func (f *batchFixture) publishAll(t *testing.T, writer *FragmentWriter, sink FrameSink) {
	t.Helper()
	if len(writer.plan.Entries) != len(f.fragments) {
		t.Fatalf("plan carries %d entries for %d fragments",
			len(writer.plan.Entries), len(f.fragments))
	}
	for i, entry := range writer.plan.Entries {
		seq, err := ChainSequence(f.seqBase, entry.Ordinal)
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Write(entry, seq, f.fragments[i], sink); err != nil {
			t.Fatalf("Write fragment %d: %v", i, err)
		}
	}
}

func TestWriterReaderRoundTripComplete(t *testing.T) {
	f := newBatchFixture(t, 2*1024*1024, sampleTree())
	writer := f.writer(t)
	var encoded [][]byte
	f.publishAll(t, writer, func(ef *EmittedFrame) error {
		encoded = append(encoded, ef.Encoded)
		return nil
	})
	reader := NewFragmentReader(ReaderOptions{})
	for _, frame := range encoded {
		if err := reader.Add(frame); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	reassembled, err := reader.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if len(reassembled) != len(f.fragments) {
		t.Fatalf("reassembled %d fragments, want %d", len(reassembled), len(f.fragments))
	}
	for i := range f.fragments {
		if !bytes.Equal(reassembled[i], f.fragments[i]) {
			t.Fatalf("fragment %d differs after transport", i)
		}
	}
}

func TestWriterReaderRoundTripParts(t *testing.T) {
	// A small budget forces the big blobs fragment of the record into
	// parts while the tiny items fragment may stay COMPLETE: mixed forms
	// inside one record are the contract's normal case.
	f := newBatchFixture(t, 900, bigBlobTree(5000))
	forms := map[proto.Form]int{}
	for _, entry := range f.plan.Entries {
		forms[entry.Form]++
	}
	if forms[proto.FormPart] == 0 {
		t.Fatalf("layout chose COMPLETE for every fragment: %v", forms)
	}
	writer := f.writer(t)
	reader := NewFragmentReader(ReaderOptions{})
	f.publishAll(t, writer, func(ef *EmittedFrame) error {
		// Every frame must fit the budget exactly: encoded bytes plus the
		// NATS header.
		if ef.EncodedSize+ef.HeaderSize > f.budget {
			t.Fatalf("frame of %d+%d bytes exceeds the budget %d",
				ef.EncodedSize, ef.HeaderSize, f.budget)
		}
		return reader.Add(ef.Encoded)
	})
	reassembled, err := reader.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	for i := range f.fragments {
		if !bytes.Equal(reassembled[i], f.fragments[i]) {
			t.Fatalf("fragment %d differs after parts transport", i)
		}
	}
}

func TestWriterBoundaryExactFit(t *testing.T) {
	// Plan one synthetic 65536-byte fragment; find the exact COMPLETE
	// boundary through proto's arithmetic, then check both sides.
	fragment := testPattern(0x33, proto.FragmentMaxBytes)
	f := &batchFixture{budget: 2 * 1024 * 1024, stream: "EXT", seqBase: 5}
	f.fragments = [][]byte{fragment}
	plan, err := proto.ComputeLayout(proto.LayoutRequest{Budget: f.budget, Stream: f.stream,
		Network: testNetwork(), PublicationEpoch: testEpoch(), SequenceBase: f.seqBase,
		Events: []proto.EventFragments{{Ordinal: 0, FragmentLengths: []uint64{uint64(len(fragment))}}}})
	if err != nil {
		t.Fatalf("ComputeLayout: %v", err)
	}
	f.plan = plan
	if plan.Entries[0].Form != proto.FormComplete {
		t.Fatal("large budget did not choose COMPLETE")
	}
	writer, err := NewFragmentWriter(plan, WriterConfig{Network: testNetwork(),
		EventID: testEventID(3), RecordDigest: sha256Of(fragment), PublicationEpoch: testEpoch(),
		FragmentCount: 3})
	if err != nil {
		t.Fatalf("NewFragmentWriter: %v", err)
	}
	var total uint64
	err = writer.Write(plan.Entries[0], 5, fragment, func(ef *EmittedFrame) error {
		total = ef.EncodedSize + ef.HeaderSize
		return nil
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	// At exactly total the plan stays COMPLETE; one byte below it turns
	// into PART.
	exact, err := proto.ComputeLayout(proto.LayoutRequest{Budget: total, Stream: f.stream,
		Network: testNetwork(), PublicationEpoch: testEpoch(), SequenceBase: f.seqBase,
		Events: []proto.EventFragments{{Ordinal: 0, FragmentLengths: []uint64{uint64(len(fragment))}}}})
	if err != nil {
		t.Fatalf("ComputeLayout exact: %v", err)
	}
	if exact.Entries[0].Form != proto.FormComplete {
		t.Fatal("exact budget did not choose COMPLETE")
	}
	below, err := proto.ComputeLayout(proto.LayoutRequest{Budget: total - 1, Stream: f.stream,
		Network: testNetwork(), PublicationEpoch: testEpoch(), SequenceBase: f.seqBase,
		Events: []proto.EventFragments{{Ordinal: 0, FragmentLengths: []uint64{uint64(len(fragment))}}}})
	if err != nil {
		t.Fatalf("ComputeLayout below: %v", err)
	}
	if below.Entries[0].Form != proto.FormPart {
		t.Fatal("budget one byte below still chose COMPLETE")
	}

	// The multi-part form at the boundary round-trips.
	writerBelow, err := NewFragmentWriter(below, WriterConfig{Network: testNetwork(),
		EventID: testEventID(3), RecordDigest: sha256Of(fragment), PublicationEpoch: testEpoch(),
		FragmentCount: 3})
	if err != nil {
		t.Fatalf("NewFragmentWriter below: %v", err)
	}
	var completed [][]byte
	reader := NewFragmentReader(ReaderOptions{OnFragment: func(index uint64, fragment []byte) error {
		completed = append(completed, append([]byte(nil), fragment...))
		return nil
	}})
	entry := below.Entries[0]
	if entry.PartCount < 2 {
		t.Fatalf("boundary split into %d parts, want several", entry.PartCount)
	}
	err = writerBelow.WriteFromReader(entry, 5, sha256Of(fragment),
		bytes.NewReader(fragment), func(ef *EmittedFrame) error {
			return reader.Add(ef.Encoded)
		})
	if err != nil {
		t.Fatalf("WriteFromReader: %v", err)
	}
	if len(completed) != 1 || !bytes.Equal(completed[0], fragment) {
		t.Fatalf("boundary fragment did not reassemble: %d completed", len(completed))
	}
	// One fragment of a three-fragment record is a truncation.
	_, err = reader.Finish()
	assertSentinel(t, ErrTruncated, err)
}

func TestWriterBoundaryTruncationDetected(t *testing.T) {
	fragment := testPattern(0x33, proto.FragmentMaxBytes)
	plan, err := proto.ComputeLayout(proto.LayoutRequest{Budget: 40000, Stream: "EXT",
		Network: testNetwork(), PublicationEpoch: testEpoch(), SequenceBase: 5,
		Events: []proto.EventFragments{{Ordinal: 0, FragmentLengths: []uint64{uint64(len(fragment))}}}})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewFragmentWriter(plan, WriterConfig{Network: testNetwork(),
		EventID: testEventID(3), RecordDigest: sha256Of(fragment), PublicationEpoch: testEpoch(),
		FragmentCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	entry := plan.Entries[0]
	err = writer.WriteFromReader(entry, 5, sha256Of(fragment),
		bytes.NewReader(fragment[:len(fragment)-1]), func(ef *EmittedFrame) error { return nil })
	assertSentinel(t, ErrTruncated, err)
}

func TestWriterRejectsStreamedDigestMismatch(t *testing.T) {
	fragment := testPattern(0x33, 1000)
	plan, err := proto.ComputeLayout(proto.LayoutRequest{Budget: 40000, Stream: "EXT",
		Network: testNetwork(), PublicationEpoch: testEpoch(), SequenceBase: 5,
		Events: []proto.EventFragments{{Ordinal: 0, FragmentLengths: []uint64{uint64(len(fragment))}}}})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewFragmentWriter(plan, WriterConfig{Network: testNetwork(),
		EventID: testEventID(3), RecordDigest: sha256Of(fragment), PublicationEpoch: testEpoch(),
		FragmentCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	wrong := sha256Of([]byte("other"))
	err = writer.WriteFromReader(plan.Entries[0], 5, wrong, bytes.NewReader(fragment),
		func(ef *EmittedFrame) error { return nil })
	assertSentinel(t, ErrDigestMismatch, err)
}

func TestWriterRejectsInvalidInputs(t *testing.T) {
	f := newBatchFixture(t, 2*1024*1024, sampleTree())
	writer := f.writer(t)
	noop := func(ef *EmittedFrame) error { return nil }

	t.Run("payload length mismatch", func(t *testing.T) {
		err := writer.Write(f.plan.Entries[0], 1000, f.fragments[0][:10], noop)
		assertSentinel(t, ErrInvalidValue, err)
	})

	t.Run("entry not from the plan", func(t *testing.T) {
		foreign := f.plan.Entries[0]
		foreign.Ordinal++
		err := writer.Write(foreign, 1000, f.fragments[0], noop)
		assertSentinel(t, ErrConflict, err)
	})

	t.Run("zero chain sequence", func(t *testing.T) {
		err := writer.Write(f.plan.Entries[0], 0, f.fragments[0], noop)
		assertSentinel(t, ErrInvalidValue, err)
	})

	t.Run("frame above the budget", func(t *testing.T) {
		shrunk := *f.plan
		shrunk.Budget = 200 // below even the smallest header fragment frame
		strict, err := NewFragmentWriter(&shrunk, WriterConfig{Network: testNetwork(),
			EventID: f.event.EventID, RecordDigest: f.event.RecordDigest,
			PublicationEpoch: testEpoch(), FragmentCount: f.event.FragmentCount})
		if err != nil {
			t.Fatal(err)
		}
		err = strict.Write(f.plan.Entries[0], 1000, f.fragments[0], noop)
		assertSentinel(t, ErrLimitIncompatible, err)
	})

	t.Run("fragment count below minimum", func(t *testing.T) {
		_, err := NewFragmentWriter(f.plan, WriterConfig{Network: testNetwork(),
			EventID: f.event.EventID, RecordDigest: f.event.RecordDigest,
			PublicationEpoch: testEpoch(), FragmentCount: 2})
		assertSentinel(t, ErrFragments, err)
	})

	t.Run("zero epoch", func(t *testing.T) {
		_, err := NewFragmentWriter(f.plan, WriterConfig{Network: testNetwork(),
			EventID: f.event.EventID, RecordDigest: f.event.RecordDigest,
			PublicationEpoch: proto.ID16{}, FragmentCount: f.event.FragmentCount})
		assertSentinel(t, ErrInvalidValue, err)
	})
}

func TestWriterNatsIdentityPerFrame(t *testing.T) {
	f := newBatchFixture(t, 900, bigBlobTree(5000))
	writer := f.writer(t)
	ids := map[string]bool{}
	var headerSample []byte
	f.publishAll(t, writer, func(ef *EmittedFrame) error {
		if len(ef.MessageID) != 123 || ef.MessageID[:4] != "ex2." {
			t.Fatalf("message id %q is malformed", ef.MessageID)
		}
		if ids[ef.MessageID] {
			t.Fatalf("message id %q repeats", ef.MessageID)
		}
		ids[ef.MessageID] = true
		want := "NATS/1.0\r\nNats-Msg-Id: " + ef.MessageID +
			"\r\nNats-Expected-Stream: " + f.stream + "\r\n\r\n"
		if string(ef.NATSHeader) != want {
			t.Fatalf("header bytes differ: %q", ef.NATSHeader)
		}
		headerSample = ef.NATSHeader
		if ef.EncodedSize != uint64(len(ef.Encoded)) {
			t.Fatal("EncodedSize disagrees with the encoded bytes")
		}
		return nil
	})
	if len(ids) < 3 {
		t.Fatalf("%d frames emitted, want several across parts and fragments", len(ids))
	}
	if len(headerSample) == 0 {
		t.Fatal("no header captured")
	}
}

// spyReadRecorder records the size of every read and every emission in
// order, proving that the streamed writer reads capacity-sized chunks
// interleaved with emissions.
type spyReadRecorder struct {
	events []string
	sizes  []int
	r      io.Reader
}

func (s *spyReadRecorder) Read(b []byte) (int, error) {
	n, err := s.r.Read(b)
	s.events = append(s.events, "read")
	s.sizes = append(s.sizes, len(b))
	return n, err
}

func TestWriterStreamingReadsAreChunkBounded(t *testing.T) {
	fragment := testPattern(0x33, 60000)
	plan, err := proto.ComputeLayout(proto.LayoutRequest{Budget: 40000, Stream: "EXT",
		Network: testNetwork(), PublicationEpoch: testEpoch(), SequenceBase: 5,
		Events: []proto.EventFragments{{Ordinal: 0, FragmentLengths: []uint64{uint64(len(fragment))}}}})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewFragmentWriter(plan, WriterConfig{Network: testNetwork(),
		EventID: testEventID(3), RecordDigest: sha256Of(fragment), PublicationEpoch: testEpoch(),
		FragmentCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	entry := plan.Entries[0]
	if entry.Form != proto.FormPart {
		t.Fatal("fixture does not exercise parts")
	}
	spy := &spyReadRecorder{r: bytes.NewReader(fragment)}
	err = writer.WriteFromReader(entry, 5, sha256Of(fragment), spy,
		func(ef *EmittedFrame) error {
			spy.events = append(spy.events, "emit")
			return nil
		})
	if err != nil {
		t.Fatalf("WriteFromReader: %v", err)
	}
	// Every read asks for at most the part capacity.
	for _, size := range spy.sizes {
		if uint64(size) > entry.Capacity {
			t.Fatalf("read of %d bytes above the %d capacity", size, entry.Capacity)
		}
	}
	// Reads and emissions interleave: no read happens after the last
	// emission, and the first event is a read.
	if spy.events[0] != "read" || spy.events[len(spy.events)-1] != "emit" {
		t.Fatalf("stream order %v", spy.events)
	}
	emits := 0
	for _, event := range spy.events {
		if event == "emit" {
			emits++
		}
	}
	if emits != int(entry.PartCount) {
		t.Fatalf("%d emissions for %d parts", emits, entry.PartCount)
	}
}

func TestWriterMemoryBoundedByChunk(t *testing.T) {
	// A synthetic multi-fragment record: 192 fragments of 60000 bytes
	// (11.5 MiB) through a 40000-byte budget, streamed from a pattern
	// reader. The sink discards frames and samples the heap; the peak must
	// stay far below the record size.
	const fragmentSize = 60000
	const fragmentsCount = 192
	lengths := make([]uint64, fragmentsCount)
	for i := range lengths {
		lengths[i] = fragmentSize
	}
	plan, err := proto.ComputeLayout(proto.LayoutRequest{Budget: 40000, Stream: "EXT",
		Network: testNetwork(), PublicationEpoch: testEpoch(), SequenceBase: 1,
		Events: []proto.EventFragments{{Ordinal: 0, FragmentLengths: lengths}}})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewFragmentWriter(plan, WriterConfig{Network: testNetwork(),
		EventID: testEventID(3), RecordDigest: sha256Of(testPattern(1, 32)),
		PublicationEpoch: testEpoch(), FragmentCount: fragmentsCount})
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, fragmentSize)
	if _, err := io.ReadFull(newPatternReader(0x77, fragmentSize), payload); err != nil {
		t.Fatal(err)
	}
	digest := sha256Of(payload)
	var peak uint64
	emitted := 0
	runtime.GC()
	for i, entry := range plan.Entries {
		reader := newPatternReader(0x77, fragmentSize)
		err := writer.WriteFromReader(entry, 1, digest, reader, func(ef *EmittedFrame) error {
			emitted++
			if emitted%64 == 0 {
				runtime.GC()
				var stats runtime.MemStats
				runtime.ReadMemStats(&stats)
				if stats.HeapAlloc > peak {
					peak = stats.HeapAlloc
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("fragment %d: %v", i, err)
		}
	}
	const recordBytes = fragmentSize * fragmentsCount
	if peak > 8*1024*1024 {
		t.Fatalf("peak heap %d bytes for an %d-byte record", peak, recordBytes)
	}
	if emitted < fragmentsCount {
		t.Fatalf("only %d frames emitted", emitted)
	}
}

func TestWriterZeroCopyDoesNotDoubleAllocate(t *testing.T) {
	// The slice-based Write must not copy the payload into a second buffer:
	// total allocations stay close to the emitted output size.
	const fragmentSize = 60000
	const fragmentsCount = 64
	lengths := make([]uint64, fragmentsCount)
	for i := range lengths {
		lengths[i] = fragmentSize
	}
	plan, err := proto.ComputeLayout(proto.LayoutRequest{Budget: 40000, Stream: "EXT",
		Network: testNetwork(), PublicationEpoch: testEpoch(), SequenceBase: 1,
		Events: []proto.EventFragments{{Ordinal: 0, FragmentLengths: lengths}}})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewFragmentWriter(plan, WriterConfig{Network: testNetwork(),
		EventID: testEventID(3), RecordDigest: sha256Of(testPattern(1, 32)),
		PublicationEpoch: testEpoch(), FragmentCount: fragmentsCount})
	if err != nil {
		t.Fatal(err)
	}
	payload := testPattern(0x77, fragmentSize)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	encodedTotal := uint64(0)
	for i, entry := range plan.Entries {
		err := writer.Write(entry, 1, payload, func(ef *EmittedFrame) error {
			encodedTotal += ef.EncodedSize
			return nil
		})
		if err != nil {
			t.Fatalf("fragment %d: %v", i, err)
		}
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	// The writer itself never copies the payload; protocol/proto's
	// canonical wrapper encode builds one transient inner message per
	// frame (bounded by the frame budget), so steady-state allocation
	// stays near twice the emitted bytes. A writer that held a second full
	// copy of the record would add the whole record on top and cross the
	// bound below.
	if allocated > encodedTotal*9/4 {
		t.Fatalf("allocated %d bytes for %d emitted bytes", allocated, encodedTotal)
	}
}

func TestFullStackRoundTrip(t *testing.T) {
	// Tree to fragments to descriptors to manifest to layout to frames to
	// reassembled fragments to the same tree, then a byte-identical
	// rebuild — the codec's flagship path.
	nodes := sampleTree()
	fragments := buildTreeFragments(t, nodes)
	event, frags, err := proto.BuildEventDescriptor(0, testEventID(7), proto.FamilyTx,
		proto.KindApplied, fragments)
	if err != nil {
		t.Fatal(err)
	}
	manifest := NewManifestBuilder()
	if err := manifest.AddEvent(event, frags); err != nil {
		t.Fatal(err)
	}
	var planDigest proto.Hash32
	copy(planDigest[:], testPattern(9, 32))
	entries, layoutDigest, err := manifest.BuildLayout(proto.LayoutRequest{
		Budget: 900, Stream: "EXT_TX", Network: testNetwork(),
		PublicationEpoch: testEpoch(), SequenceBase: 42}, planDigest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(fragments) {
		t.Fatalf("layout planned %d entries for %d fragments", len(entries), len(fragments))
	}
	plan := &proto.LayoutPlan{Budget: 900, Stream: "EXT_TX", StreamHeaderBytes: 0, Entries: entries}
	writer, err := NewFragmentWriter(plan, WriterConfig{Network: testNetwork(),
		EventID: event.EventID, RecordDigest: event.RecordDigest,
		PublicationEpoch: testEpoch(), FragmentCount: event.FragmentCount})
	if err != nil {
		t.Fatal(err)
	}
	reader := NewFragmentReader(ReaderOptions{})
	for i, entry := range entries {
		if err := writer.Write(entry, 42, fragments[i], func(ef *EmittedFrame) error {
			return reader.Add(ef.Encoded)
		}); err != nil {
			t.Fatalf("Write fragment %d: %v", i, err)
		}
	}
	reassembled, err := reader.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	for i := range fragments {
		if !bytes.Equal(fragments[i], reassembled[i]) {
			t.Fatalf("fragment %d differs end to end", i)
		}
	}
	header, readNodes, err := ReadRecord(reassembled)
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if header.RootType != 100 || int(header.NodeCount) != len(nodes) {
		t.Fatalf("header %+v", header)
	}
	rebuilt := buildTreeFragments(t, readBackToTree(readNodes))
	for i := range fragments {
		if !bytes.Equal(fragments[i], rebuilt[i]) {
			t.Fatalf("rebuild fragment %d differs", i)
		}
	}
	if len(layoutDigest) != 32 {
		t.Fatal("layout digest is not 32 bytes")
	}
}

func TestWriterEntriesSortedByOrdinalAndFragment(t *testing.T) {
	f := newBatchFixture(t, 2*1024*1024, sampleTree())
	var keys []int
	for i, entry := range f.plan.Entries {
		if entry.FragmentIndex != uint64(i) {
			t.Fatalf("entry %d carries fragment index %d", i, entry.FragmentIndex)
		}
		keys = append(keys, int(entry.Ordinal))
	}
	if !sort.IntsAreSorted(keys) {
		t.Fatal("entries are not in ordinal order")
	}
}

func TestNewFragmentWriterRejections(t *testing.T) {
	f := newBatchFixture(t, 2*1024*1024, sampleTree())
	cfg := WriterConfig{Network: testNetwork(), EventID: f.event.EventID,
		RecordDigest: f.event.RecordDigest, PublicationEpoch: testEpoch(),
		FragmentCount: f.event.FragmentCount}
	cases := []struct {
		name string
		run  func() error
	}{
		{"nil plan", func() error {
			_, err := NewFragmentWriter(nil, cfg)
			return err
		}},
		{"plan without budget", func() error {
			empty := &proto.LayoutPlan{}
			_, err := NewFragmentWriter(empty, cfg)
			return err
		}},
		{"plan without entries", func() error {
			noEntries := &proto.LayoutPlan{Budget: 4096, Stream: "EXT"}
			_, err := NewFragmentWriter(noEntries, cfg)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSentinel(t, ErrInvalidValue, tc.run())
		})
	}
}

func TestChainSequenceOverflow(t *testing.T) {
	if _, err := ChainSequence(^uint64(0), 1); err == nil {
		t.Fatal("overflowing chain sequence accepted")
	}
	seq, err := ChainSequence(1000, 7)
	if err != nil || seq != 1007 {
		t.Fatalf("chain sequence %d, err %v", seq, err)
	}
}
