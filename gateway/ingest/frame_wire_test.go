package ingest

// Frames on the wire. A frame stream is the canonical encoding of one
// TransportFrame, so these tests send real frames. The helpers wrap data
// the way the producer's wrapper does, and the regression test builds whole
// records with the real codec and drives them through the real server to
// CLOSED.

import (
	"context"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/gateway/journal"
	"github.com/sigaev-pro/bc-indexer/gateway/session"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// The destination the real planner and writer are given.
var (
	wireNetwork = proto.NetworkIdentity{Family: proto.NetworkFamilyEVM}
	wireEpoch   = proto.ID16{7}
	wireStream  = "EXT_TX"
)

// eventDescriptors is one event and its fragment descriptors for fragment
// lengths, in the fixed order of the manifest.
func eventDescriptors(producer, run [16]byte, ordinal uint64, lengths []uint64) (proto.EventDescriptor, []proto.FragmentDescriptor, error) {
	frags := make([]proto.FragmentDescriptor, len(lengths))
	var total uint64
	for i, n := range lengths {
		frags[i] = proto.FragmentDescriptor{FragmentIndex: uint64(i), EncodedLength: n,
			FragmentSHA256: proto.Hash32(sha256.Sum256([]byte("fragment")))}
		total += n
	}
	eventID := proto.NewEventID(proto.ID16(producer), proto.ID16(run), ordinal+1)
	digest, err := proto.FragmentManifestDigest(eventID, frags)
	if err != nil {
		return proto.EventDescriptor{}, nil, err
	}
	return proto.EventDescriptor{Ordinal: ordinal, EventID: eventID, Family: proto.FamilyTx,
		Kind: proto.KindApplied, RecordLength: total,
		RecordDigest: proto.Hash32(sha256.Sum256([]byte("record"))), FragmentCount: uint64(len(frags)),
		FragmentManifestDigest: digest}, frags, nil
}

// wireManifest builds a real manifest with the producer's builder: one
// event per slice of fragment lengths.
func wireManifest(t *testing.T, events ...[]uint64) []byte {
	t.Helper()
	b := codec.NewManifestBuilder()
	for ordinal, lengths := range events {
		event, frags, err := eventDescriptors(testProducer, testRun, uint64(ordinal), lengths)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.AddEvent(event, frags); err != nil {
			t.Fatalf("manifest event %d: %v", ordinal, err)
		}
	}
	manifest, err := b.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

// wireFrame is the canonical encoding of one transport frame of a layout
// entry that carries data: what the producer's FragmentWriter emits.
func wireFrame(sequenceBase uint64, entry proto.LayoutManifestEntry, fragmentCount, frame uint64, data []byte) []byte {
	f := proto.TransportFrame{
		TransportVersion:    proto.TransportVersion,
		Network:             wireNetwork,
		EventID:             proto.NewEventID(testProducer, testRun, entry.Ordinal+1),
		RecordDigest:        proto.Hash32(sha256.Sum256([]byte("record"))),
		PublicationEpoch:    wireEpoch,
		ChainSequence:       sequenceBase + entry.Ordinal,
		FragmentIndex:       entry.FragmentIndex,
		FragmentCount:       fragmentCount,
		FragmentDigest:      proto.Hash32(sha256.Sum256(data)),
		RecordFormatVersion: proto.RecordFormatVersion,
	}
	if entry.Form == proto.FormComplete {
		f.Complete = &proto.CompleteFragment{FragmentBytes: data}
	} else {
		f.Part = &proto.FragmentPart{PartIndex: frame, PartCount: entry.PartCount,
			FragmentTotalBytes: entry.FragmentTotalBytes, Data: data}
	}
	encoded, err := f.Encode()
	if err != nil {
		panic("wire frame: " + err.Error())
	}
	return encoded
}

// realRecord is one record built by codec.BuildRecord, with the descriptors
// the manifest carries for it.
type realRecord struct {
	event       proto.EventDescriptor
	descriptors []proto.FragmentDescriptor
	fragments   [][]byte
}

// realManifest is a batch of real records and the manifest bytes their
// descriptors compose.
type realManifest struct {
	records []realRecord
	bytes   []byte
}

// eventCount is the number of events of the manifest.
func (m *realManifest) eventCount() uint64 { return uint64(len(m.records)) }

// largestFragment is the encoded length of the longest record fragment.
func (m *realManifest) largestFragment() uint64 {
	var largest uint64
	for _, r := range m.records {
		for _, f := range r.fragments {
			if uint64(len(f)) > largest {
				largest = uint64(len(f))
			}
		}
	}
	return largest
}

// buildRealRecords builds one record per tree with codec.BuildRecord and the
// manifest of the batch with codec.ManifestBuilder.
func buildRealRecords(t *testing.T, trees ...[]codec.TreeNode) *realManifest {
	t.Helper()
	b := codec.NewManifestBuilder()
	m := &realManifest{}
	for ordinal, nodes := range trees {
		eventID := proto.NewEventID(testProducer, testRun, uint64(ordinal)+1)
		var fragments [][]byte
		if _, err := codec.BuildRecord(eventID, wireNetwork, proto.FamilyTx, proto.KindApplied, 100, nodes,
			func(_ uint64, fragment []byte) error {
				fragments = append(fragments, append([]byte(nil), fragment...))
				return nil
			}); err != nil {
			t.Fatalf("BuildRecord of event %d: %v", ordinal, err)
		}
		event, descriptors, err := proto.BuildEventDescriptor(uint64(ordinal), eventID,
			proto.FamilyTx, proto.KindApplied, fragments)
		if err != nil {
			t.Fatalf("BuildEventDescriptor of event %d: %v", ordinal, err)
		}
		if err := b.AddEvent(event, descriptors); err != nil {
			t.Fatalf("manifest event %d: %v", ordinal, err)
		}
		m.records = append(m.records, realRecord{event: event, descriptors: descriptors, fragments: fragments})
	}
	var err error
	if m.bytes, err = b.Bytes(); err != nil {
		t.Fatal(err)
	}
	return m
}

// realFrame is one frame the real FragmentWriter emitted.
type realFrame struct {
	entry   proto.LayoutManifestEntry
	part    uint64
	encoded []byte
}

// realBatch is a manifest of real records under one transport plan: the
// layout entries and every canonical frame, in plan order.
type realBatch struct {
	manifest *realManifest
	layout   [32]byte
	entries  []proto.LayoutManifestEntry
	frames   []realFrame
}

// planRealBatch lays a manifest out with proto.ComputeLayout and emits the
// canonical frame of every entry with codec.FragmentWriter, as a producer
// does once the gateway allocated the reservation.
func planRealBatch(t *testing.T, m *realManifest, budget uint64, reservation journal.Reservation, planDigest [32]byte) *realBatch {
	t.Helper()
	events := make([]proto.EventFragments, len(m.records))
	for i, r := range m.records {
		lengths := make([]uint64, len(r.descriptors))
		for k := range r.descriptors {
			lengths[k] = r.descriptors[k].EncodedLength
		}
		events[i] = proto.EventFragments{Ordinal: r.event.Ordinal, FragmentLengths: lengths}
	}
	plan, err := proto.ComputeLayout(proto.LayoutRequest{Budget: budget, Stream: wireStream,
		Network: wireNetwork, PublicationEpoch: reservation.IssuerEpoch,
		SequenceBase: reservation.SequenceBase, Events: events})
	if err != nil {
		t.Fatalf("ComputeLayout: %v", err)
	}
	batch := &realBatch{manifest: m, entries: plan.Entries}
	if batch.layout, err = proto.LayoutManifestDigest(planDigest, plan.Entries); err != nil {
		t.Fatal(err)
	}
	// The entries come in ordinal and fragment order, so the entries of one
	// record follow those of the records before it.
	next := 0
	for _, r := range m.records {
		writer, err := codec.NewFragmentWriter(plan, codec.WriterConfig{Network: wireNetwork,
			EventID: r.event.EventID, RecordDigest: r.event.RecordDigest,
			PublicationEpoch: reservation.IssuerEpoch, FragmentCount: r.event.FragmentCount})
		if err != nil {
			t.Fatal(err)
		}
		sequence, err := codec.ChainSequence(reservation.SequenceBase, r.event.Ordinal)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range r.fragments {
			entry := plan.Entries[next]
			next++
			if err := writer.Write(entry, sequence, fragment, func(ef *codec.EmittedFrame) error {
				batch.frames = append(batch.frames, realFrame{entry: entry, part: ef.PartIndex,
					encoded: append([]byte(nil), ef.Encoded...)})
				return nil
			}); err != nil {
				t.Fatalf("Write of ordinal %d fragment %d: %v", entry.Ordinal, entry.FragmentIndex, err)
			}
		}
	}
	return batch
}

// reservationOf is the reservation the gateway allocated for one batch,
// read from the journal's own records.
func reservationOf(t *testing.T, h *harness, seq uint64) journal.Reservation {
	t.Helper()
	info, err := h.j.Batch(jid(seq))
	if err != nil || info.Reservation == nil {
		t.Fatalf("reservation of batch %d: %+v %v", seq, info, err)
	}
	return *info.Reservation
}

// uploadManifest prepares one batch with the given manifest bytes.
func (h *harness) uploadManifest(t *testing.T, header headerOf, seq uint64, manifest []byte, events uint64) {
	t.Helper()
	ctx := context.Background()
	digest := sha32(manifest)
	begin, err := h.client.BeginPrepare(ctx, &BeginPrepareRequest{Header: header(), Batch: batchMsg(seq),
		ManifestDigest: digest, EventCount: events, DescriptorBytesTotal: uint64(len(manifest))})
	if err != nil || begin.GetError() != nil {
		t.Fatalf("begin prepare: %v %+v", err, begin.GetError())
	}
	for off := 0; off < len(manifest); off += 400 {
		end := off + 400
		if end > len(manifest) {
			end = len(manifest)
		}
		if _, err := h.client.PrepareChunk(ctx, &PrepareChunkRequest{Header: header(), Batch: batchMsg(seq),
			ManifestDigest: digest, Offset: uint64(off), ChunkSha256: sha32(manifest[off:end]), Data: manifest[off:end]}); err != nil {
			t.Fatal(err)
		}
	}
	fin, err := h.client.FinalizePrepare(ctx, &FinalizePrepareRequest{Header: header(), Batch: batchMsg(seq), ManifestDigest: digest})
	if err != nil || fin.GetError() != nil {
		t.Fatalf("finalize prepare: %v %+v", err, fin.GetError())
	}
}

// beginRealPublish starts the publish phase of a planned batch and returns
// the plan digest.
func (h *harness) beginRealPublish(t *testing.T, header headerOf, seq uint64, reservation journal.Reservation, batch *realBatch) []byte {
	t.Helper()
	planDigest := journal.PlanDigest(reservation.Seed)
	resp, err := h.client.BeginPublish(context.Background(), &BeginPublishRequest{Header: header(),
		Batch: batchMsg(seq), Seed: reservation.Seed, PlanDigest: planDigest[:],
		LayoutManifestDigest: batch.layout[:], EntryCount: uint64(len(batch.entries))})
	if err != nil || resp.GetError() != nil {
		t.Fatalf("begin publish: %v %+v", err, resp.GetError())
	}
	return planDigest[:]
}

// frameStream is the PublishChunk stream of one session and one batch: the
// pieces of encoded frames go up, one answer per message comes back.
type frameStream struct {
	t          *testing.T
	stream     Ingest_PublishChunkClient
	header     headerOf
	seq        uint64
	planDigest []byte
}

// openFrames opens the PublishChunk stream of one batch.
func (h *harness) openFrames(t *testing.T, header headerOf, seq uint64, planDigest []byte) *frameStream {
	t.Helper()
	stream, err := h.client.PublishChunk(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return &frameStream{t: t, stream: stream, header: header, seq: seq, planDigest: planDigest}
}

// piece sends one piece of one frame and returns the durable next offset
// with the typed error, nil when the piece was stored.
func (fs *frameStream) piece(f realFrame, offset uint64, data []byte) (uint64, *Error) {
	fs.t.Helper()
	if err := fs.stream.Send(&PublishChunkRequest{Header: fs.header(), Batch: batchMsg(fs.seq),
		PlanDigest: fs.planDigest, Ordinal: f.entry.Ordinal, FragmentIndex: f.entry.FragmentIndex,
		FrameIndex: f.part, Meta: &FrameMeta{Form: uint32(f.entry.Form), Capacity: f.entry.Capacity,
			PartCount: f.entry.PartCount, FragmentTotalBytes: f.entry.FragmentTotalBytes},
		Offset: offset, ChunkSha256: sha32(data), Data: data}); err != nil {
		fs.t.Fatal(err)
	}
	resp, err := fs.stream.Recv()
	if err != nil {
		fs.t.Fatalf("the stream closed instead of answering: %v", err)
	}
	return resp.GetNextOffset(), resp.GetError()
}

// frame uploads one encoded frame in pieces of at most one data chunk, with
// byte offsets that count bytes inside the frame.
func (fs *frameStream) frame(f realFrame) {
	fs.t.Helper()
	for off := 0; off < len(f.encoded); off += MaxDataChunk {
		end := off + MaxDataChunk
		if end > len(f.encoded) {
			end = len(f.encoded)
		}
		next, e := fs.piece(f, uint64(off), f.encoded[off:end])
		if e != nil || next != uint64(end) {
			fs.t.Fatalf("frame ordinal %d fragment %d part %d at %d: next=%d %+v", f.entry.Ordinal,
				f.entry.FragmentIndex, f.part, off, next, e)
		}
	}
}

// close ends the stream and fails unless the server closed it in turn.
func (fs *frameStream) close() {
	fs.t.Helper()
	if err := fs.stream.CloseSend(); err != nil {
		fs.t.Fatal(err)
	}
	if _, err := fs.stream.Recv(); err != io.EOF {
		fs.t.Fatalf("stream end: %v, want io.EOF", err)
	}
}

// closeRealBatch closes a planned batch and fails unless it is CLOSED.
func (h *harness) closeRealBatch(t *testing.T, header headerOf, seq uint64, planDigest []byte, batch *realBatch) {
	t.Helper()
	fin, err := h.client.FinalizePublish(context.Background(), &FinalizePublishRequest{Header: header(),
		Batch: batchMsg(seq), PlanDigest: planDigest, ManifestDigest: sha32(batch.manifest.bytes),
		LayoutManifestDigest: batch.layout[:]})
	if err != nil || fin.GetError() != nil || fin.GetState() != BatchState_CLOSED {
		t.Fatalf("finalize publish: %v %+v, state %s", err, fin.GetError(), fin.GetState())
	}
}

// blobTree is a record tree with one bytes field of the given size.
func blobTree(size int) []codec.TreeNode {
	value := make([]byte, size)
	for i := range value {
		value[i] = byte(i)
	}
	return []codec.TreeNode{
		{Kind: codec.ValueMessage, TypeKey: 100, ChildCount: 1},
		{Kind: codec.ValueBytes, HasFieldTag: true, FieldTag: 1, Value: value},
	}
}

// The probe of the S8 agent, as a regression test: records built by the
// real codec go through the real server to CLOSED, in both forms. Before
// the frame accounting the journal counted data bytes, every PublishChunk
// of an encoded frame was refused with MODE_CONFLICT, and FinalizePublish
// answered that the stream was incomplete.
func TestRealFramesReachClosedOverRPC(t *testing.T) {
	records := buildRealRecords(t, blobTree(2400), blobTree(120))
	for _, tc := range []struct {
		name   string
		budget uint64
		parts  bool
	}{
		// Every fragment fits its COMPLETE frame.
		{"complete frames", journal.DefaultFrameLimit, false},
		// The longest fragment no longer fits as a whole: the planner
		// splits it into PART frames.
		{"part frames", records.largestFragment() + 150, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := storage.NewMemory()
			j, err := journal.Open(st, journal.Options{BindingDigest: testBinding, FrameLimit: tc.budget})
			if err != nil {
				t.Fatal(err)
			}
			h := startHarness(t, st, j, session.NewGate())
			s := h.openSessionAt(t, 1, 1)

			h.uploadManifest(t, s.header, 1, records.bytes, records.eventCount())
			reservation := reservationOf(t, h, 1)
			planDigest := journal.PlanDigest(reservation.Seed)
			batch := planRealBatch(t, records, tc.budget, reservation, planDigest)
			digest := h.beginRealPublish(t, s.header, 1, reservation, batch)
			frames := h.openFrames(t, s.header, 1, digest)
			for _, f := range batch.frames {
				frames.frame(f)
			}
			frames.close()
			h.closeRealBatch(t, s.header, 1, digest, batch)

			var parts int
			for _, f := range batch.frames {
				if f.entry.Form == proto.FormPart {
					parts++
					// The last part is shorter than the capacity; every
					// part is still longer than its data: the wrapper adds
					// bytes.
					data := f.entry.FragmentTotalBytes - f.part*f.entry.Capacity
					if data > f.entry.Capacity {
						data = f.entry.Capacity
					}
					if uint64(len(f.encoded)) <= data {
						t.Fatalf("PART frame %d of capacity %d encodes to %d bytes for %d data bytes",
							f.part, f.entry.Capacity, len(f.encoded), data)
					}
				}
			}
			if (tc.parts && parts == 0) || (!tc.parts && parts != 0) {
				t.Fatalf("the plan carries %d PART frames under budget %d", parts, tc.budget)
			}
			if info, err := h.j.Batch(jid(1)); err != nil || info.State != journal.StateClosed {
				t.Fatalf("journal state: %+v %v", info, err)
			}
		})
	}
}

// A frame larger than one data chunk arrives in pieces through the server:
// the whole frame in one chunk is refused by the 64 KiB chunk bound, the
// pieces are accepted with offsets inside the frame, a byte beyond the
// frame is refused, and the batch closes.
func TestFrameAboveChunkBoundOverRPC(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()
	entry := proto.LayoutManifestEntry{Ordinal: 0, FragmentIndex: 0, Form: proto.FormComplete,
		Capacity: proto.FragmentMaxBytes, PartCount: 1, FragmentTotalBytes: proto.FragmentMaxBytes}
	manifest := wireManifest(t, []uint64{proto.FragmentMaxBytes, 3, 3})
	h.uploadManifest(t, s.header, 1, manifest, 1)
	reservation := reservationOf(t, h, 1)
	planDigest := journal.PlanDigest(reservation.Seed)
	layout, err := proto.LayoutManifestDigest(planDigest, []proto.LayoutManifestEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	batch := &realBatch{manifest: &realManifest{bytes: manifest}, layout: layout, entries: []proto.LayoutManifestEntry{entry}}
	digest := h.beginRealPublish(t, s.header, 1, reservation, batch)
	frame := wireFrame(reservation.SequenceBase, entry, 3, 0, make([]byte, proto.FragmentMaxBytes))
	if uint64(len(frame)) <= MaxDataChunk {
		t.Fatalf("the frame of a %d-byte fragment encodes to %d bytes, want more than one chunk",
			proto.FragmentMaxBytes, len(frame))
	}
	one := realFrame{entry: entry, encoded: frame}
	frames := h.openFrames(t, s.header, 1, digest)
	chunk := func(offset uint64, data []byte) (uint64, *Error) { return frames.piece(one, offset, data) }

	// The whole frame does not fit one data chunk.
	if _, e := chunk(0, frame); e == nil {
		t.Fatal("a frame over one data chunk was stored in one piece")
	} else {
		wantCode(t, "frame in one chunk", e, ErrorCode_LIMIT_INCOMPATIBLE, RetryAction_FIX_CONFIG)
	}
	// In two pieces it does, and a retry of a stored piece is a no-op.
	first, rest := frame[:MaxDataChunk], frame[MaxDataChunk:]
	if next, e := chunk(0, first); e != nil || next != MaxDataChunk {
		t.Fatalf("first piece: next=%d %+v", next, e)
	}
	if next, e := chunk(0, first); e != nil || next != MaxDataChunk {
		t.Fatalf("first piece retry: next=%d %+v", next, e)
	}
	if next, e := chunk(MaxDataChunk, rest); e != nil || next != uint64(len(frame)) {
		t.Fatalf("second piece: next=%d %+v", next, e)
	}
	// A byte beyond the encoded size of the frame is refused, and the
	// stream stays open for the next message.
	if _, e := chunk(uint64(len(frame)), []byte("x")); e == nil {
		t.Fatal("a byte beyond the frame was stored")
	} else {
		wantCode(t, "byte beyond the frame", e, ErrorCode_MODE_CONFLICT, RetryAction_RETRY_BACKOFF)
	}
	// A stored piece is still a no-op after the refusal and answers the
	// current cursor of the frame; then the stream ends in order.
	if next, e := chunk(0, first); e != nil || next != uint64(len(frame)) {
		t.Fatalf("stored piece after a refusal: next=%d %+v", next, e)
	}
	frames.close()

	h.closeRealBatch(t, s.header, 1, digest, batch)
	// The state read reports the frame with its full encoded size.
	state, err := h.client.GetBatchState(ctx, &GetBatchStateRequest{Header: s.header(), Batch: batchMsg(1), PlanDigest: digest})
	if err != nil || state.GetError() != nil || len(state.GetCursors()) != 1 || state.GetCursors()[0].GetNextOffset() != uint64(len(frame)) {
		t.Fatalf("cursors of the chunked frame: %v %+v %v", err, state.GetCursors(), state.GetError())
	}
}

// A manifest that is not a descriptor stream prepares and reserves, and
// its first frame declaration is refused by a named reason: the fragment
// count of the event cannot be read from it.
func TestManifestWithoutFragmentCountsRefusesPublishOverRPC(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	entry := proto.LayoutManifestEntry{Ordinal: 0, FragmentIndex: 0, Form: proto.FormComplete,
		Capacity: 6, PartCount: 1, FragmentTotalBytes: 6}
	manifest := []byte("extractor.manifest.v2\x00payload")
	h.uploadManifest(t, s.header, 1, manifest, 1)
	reservation := reservationOf(t, h, 1)
	planDigest := journal.PlanDigest(reservation.Seed)
	layout, err := proto.LayoutManifestDigest(planDigest, []proto.LayoutManifestEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	batch := &realBatch{manifest: &realManifest{bytes: manifest}, layout: layout, entries: []proto.LayoutManifestEntry{entry}}
	digest := h.beginRealPublish(t, s.header, 1, reservation, batch)
	frames := h.openFrames(t, s.header, 1, digest)
	_, e := frames.piece(realFrame{entry: entry}, 0, []byte("abc"))
	wantCode(t, "frame declaration on a manifest without descriptors", e,
		ErrorCode_INVALID_DESCRIPTOR, RetryAction_DIAGNOSE_IMMUTABLE)
	// The refused declaration stored no frame chunk.
	assertNoFrameChunkOf(t, h, jid(1))
}

// assertNoFrameChunkOf fails when the store holds a frame chunk of one
// batch.
func assertNoFrameChunkOf(t *testing.T, h *harness, id journal.BatchID) {
	t.Helper()
	raw := id.Bytes()
	prefix := append([]byte("batch/"), raw[:]...)
	prefix = append(prefix, "/frames/"...)
	keys := 0
	if err := h.st.Iterate(nil, nil, func(k, _ []byte) error {
		if len(k) >= len(prefix) && string(k[:len(prefix)]) == string(prefix) {
			keys++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if keys != 0 {
		t.Fatalf("the store holds %d frame chunk(s) of the batch", keys)
	}
}
