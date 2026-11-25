package ingest

import (
	"context"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/sigaev-pro/bc-indexer/gateway/journal"
	"github.com/sigaev-pro/bc-indexer/gateway/session"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// framePlan is one publishable plan over the wire: event 0 fragment 0 as
// one COMPLETE frame, event 1 fragment 0 as two PART frames.
type framePlan struct {
	batchSeq   uint64
	manifest   []byte
	seed       []byte
	planDigest []byte
	layout     [32]byte
	frames     []frameSend
}

type frameSend struct {
	ordinal, fragment, frame     uint64
	form, capacity, parts, total uint64
	data                         []byte
}

func newFramePlan(t *testing.T, h *harness, s *sessionCtx, seq uint64, importMode bool) framePlan {
	t.Helper()
	// A real descriptor manifest of two events; the plan carries fragment 0
	// of each event. Its frames are the encoded TransportFrames.
	manifest := wireManifest(t, []uint64{6, 3, 3}, []uint64{6, 3, 3})
	digest := sha32(manifest)

	var seed []byte
	mode := PrepareMode_PREPARE_NORMAL
	if importMode {
		id := journal.BatchID{ProducerID: testProducer, RunID: testRun, Seq: seq}
		seed = journal.BuildSeed(journal.SeedLayout{
			ReservationID: [16]byte{0xAB}, IssuerEpoch: [16]byte{0xCD}, Batch: id,
			ManifestDigest: as32(digest), EventCount: 2, SequenceBase: 500,
			BindingDigest: testBinding, FrameLimit: journal.DefaultFrameLimit,
		})
		mode = PrepareMode_PREPARE_IMPORT_SEALED
	}
	begin, err := h.client.BeginPrepare(context.Background(), &BeginPrepareRequest{
		Header: s.header(), Batch: batchMsg(seq), ManifestDigest: digest,
		EventCount: 2, DescriptorBytesTotal: uint64(len(manifest)), Mode: mode, ImportSeed: seed,
	})
	if err != nil || begin.GetError() != nil {
		t.Fatalf("begin prepare: %v %+v", err, begin.GetError())
	}
	for off := 0; off < len(manifest); off += 7 {
		end := off + 7
		if end > len(manifest) {
			end = len(manifest)
		}
		data := manifest[off:end]
		if _, err := h.client.PrepareChunk(context.Background(), &PrepareChunkRequest{
			Header: s.header(), Batch: batchMsg(seq), ManifestDigest: digest,
			Offset: uint64(off), ChunkSha256: sha32(data), Data: data,
		}); err != nil {
			t.Fatal(err)
		}
	}
	fin, err := h.client.FinalizePrepare(context.Background(), &FinalizePrepareRequest{
		Header: s.header(), Batch: batchMsg(seq), ManifestDigest: digest,
	})
	if err != nil || fin.GetError() != nil {
		t.Fatalf("finalize prepare: %v %+v", err, fin.GetError())
	}
	// The frames are the encoded TransportFrames of the layout entries, built
	// on the sequence base of the reservation the gateway allocated.
	layout, err := journal.ParseSeed(fin.GetSeed())
	if err != nil {
		t.Fatal(err)
	}
	base := layout.SequenceBase
	wrap := func(e proto.LayoutManifestEntry, part uint64, data []byte) []byte {
		return wireFrame(base, e, 3, part, data)
	}
	complete := proto.LayoutManifestEntry{Ordinal: 0, FragmentIndex: 0, Form: proto.FormComplete, Capacity: 6, PartCount: 1, FragmentTotalBytes: 6}
	split := proto.LayoutManifestEntry{Ordinal: 1, FragmentIndex: 0, Form: proto.FormPart, Capacity: 4, PartCount: 2, FragmentTotalBytes: 6}
	pf := framePlan{
		batchSeq:   seq,
		manifest:   manifest,
		seed:       fin.GetSeed(),
		planDigest: fin.GetPlanDigest(),
		frames: []frameSend{
			{0, 0, 0, 0, 6, 1, 6, wrap(complete, 0, []byte("abcdef"))},
			{1, 0, 0, 1, 4, 2, 6, wrap(split, 0, []byte("wxyz"))},
			{1, 0, 1, 1, 4, 2, 6, wrap(split, 1, []byte("uv"))},
		},
	}
	pf.layout = layoutDigestOf(as32(fin.GetPlanDigest()))
	return pf
}

func as32(b []byte) [32]byte {
	var v [32]byte
	copy(v[:], b)
	return v
}

// layoutDigestOf recomputes the extractor.layout.v2 digest over the plan of
// newFramePlan (two entries: COMPLETE 6 bytes, PART 4+2).
func layoutDigestOf(planDigest [32]byte) [32]byte {
	h := sha256.New()
	h.Write([]byte("extractor.layout.v2"))
	h.Write([]byte{0})
	h.Write(planDigest[:])
	h.Write([]byte{0, 0, 0, 0, 0, 0, 0, 2}) // entry count
	for _, e := range [][6]uint64{
		{0, 0, 0, 6, 1, 6},
		{1, 0, 1, 4, 2, 6},
	} {
		for _, v := range e[:2] {
			h.Write(be64(v))
		}
		h.Write([]byte{byte(e[2])})
		for _, v := range e[3:] {
			h.Write(be64(v))
		}
	}
	var d [32]byte
	copy(d[:], h.Sum(nil))
	return d
}

func be64(v uint64) []byte {
	var b [8]byte
	for i := 0; i < 8; i++ {
		b[7-i] = byte(v >> (8 * i))
	}
	return b[:]
}

func (p framePlan) beginPublish(t *testing.T, h *harness, s *sessionCtx) {
	t.Helper()
	resp, err := h.client.BeginPublish(context.Background(), &BeginPublishRequest{
		Header: s.header(), Batch: batchMsg(p.batchSeq), Seed: p.seed, PlanDigest: p.planDigest,
		LayoutManifestDigest: p.layout[:], EntryCount: 2,
	})
	if err != nil || resp.GetError() != nil {
		t.Fatalf("begin publish: %v %+v", err, resp.GetError())
	}
}

// streamOne sends one frame chunk and returns the response.
func (h *harness) streamOne(t *testing.T, stream Ingest_PublishChunkClient, s *sessionCtx, p framePlan, f frameSend, offset uint64, data []byte) *PublishChunkResponse {
	t.Helper()
	if err := stream.Send(&PublishChunkRequest{
		Header: s.header(), Batch: batchMsg(p.batchSeq), PlanDigest: p.planDigest,
		Ordinal: f.ordinal, FragmentIndex: f.fragment, FrameIndex: f.frame,
		Meta:   &FrameMeta{Form: uint32(f.form), Capacity: f.capacity, PartCount: f.parts, FragmentTotalBytes: f.total},
		Offset: offset, ChunkSha256: sha32(data), Data: data,
	}); err != nil {
		t.Fatal(err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestPublishStreamOverRPC(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	p := newFramePlan(t, h, s, 1, false)
	p.beginPublish(t, h, s)
	ctx := context.Background()

	stream, err := h.client.PublishChunk(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// First frame in two chunks, with an idempotent retry between them.
	f0 := p.frames[0]
	if resp := h.streamOne(t, stream, s, p, f0, 0, f0.data[:3]); resp.GetError() != nil || resp.GetNextOffset() != 3 {
		t.Fatalf("chunk 1: %+v", resp)
	}
	if resp := h.streamOne(t, stream, s, p, f0, 0, f0.data[:3]); resp.GetError() != nil || resp.GetNextOffset() != 3 {
		t.Fatalf("chunk retry: %+v", resp)
	}
	// A gap answers OFFSET_MISMATCH with the expected cursor, and the
	// stream stays open for the next message.
	if resp := h.streamOne(t, stream, s, p, f0, 5, f0.data[5:]); resp.GetError().GetCode() != ErrorCode_OFFSET_MISMATCH || resp.GetNextOffset() != 3 {
		t.Fatalf("gap: %+v", resp)
	}
	if resp := h.streamOne(t, stream, s, p, f0, 3, f0.data[3:]); resp.GetError() != nil {
		t.Fatalf("chunk 2: %+v", resp)
	}
	for _, f := range p.frames[1:] {
		if resp := h.streamOne(t, stream, s, p, f, 0, f.data); resp.GetError() != nil {
			t.Fatalf("frame: %+v", resp)
		}
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("stream end: %v, want io.EOF", err)
	}

	fin, err := h.client.FinalizePublish(ctx, &FinalizePublishRequest{
		Header: s.header(), Batch: batchMsg(1), PlanDigest: p.planDigest,
		ManifestDigest: sha32(p.manifest), LayoutManifestDigest: p.layout[:],
	})
	if err != nil || fin.GetError() != nil {
		t.Fatalf("finalize publish: %v %+v", err, fin.GetError())
	}
	if fin.GetState() != BatchState_CLOSED {
		t.Fatalf("state = %s, want CLOSED", fin.GetState())
	}
	// Idempotent close over the wire.
	if fin, err := h.client.FinalizePublish(ctx, &FinalizePublishRequest{
		Header: s.header(), Batch: batchMsg(1), PlanDigest: p.planDigest,
		ManifestDigest: sha32(p.manifest), LayoutManifestDigest: p.layout[:],
	}); err != nil || fin.GetState() != BatchState_CLOSED {
		t.Fatalf("finalize retry: %v %+v", err, fin.GetError())
	}
	// An incomplete batch is never CLOSED: the layout cannot be closed with
	// a wrong digest.
	other := newFramePlan(t, h, s, 2, false)
	other.beginPublish(t, h, s)
	st, err := h.client.PublishChunk(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f := other.frames[0]
	if resp := h.streamOne(t, st, s, other, f, 0, f.data); resp.GetError() != nil {
		t.Fatalf("frame: %+v", resp)
	}
	_ = st.CloseSend()
	wrong := other.layout
	wrong[0] ^= 0xFF
	fin, err = h.client.FinalizePublish(ctx, &FinalizePublishRequest{
		Header: s.header(), Batch: batchMsg(2), PlanDigest: other.planDigest,
		ManifestDigest: sha32(other.manifest), LayoutManifestDigest: wrong[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	if fin.GetError().GetCode() != ErrorCode_IDENTITY_MISMATCH {
		t.Fatalf("wrong layout: %s", fin.GetError().GetCode())
	}
}

func TestImportSealedOverRPC(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	p := newFramePlan(t, h, s, 1, true)
	p.beginPublish(t, h, s)
	ctx := context.Background()

	stream, err := h.client.PublishChunk(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range p.frames {
		if resp := h.streamOne(t, stream, s, p, f, 0, f.data); resp.GetError() != nil {
			t.Fatalf("frame: %+v", resp)
		}
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatal(err)
	}

	// FinalizePublish refuses an imported batch; SealImport closes it.
	fin, err := h.client.FinalizePublish(ctx, &FinalizePublishRequest{
		Header: s.header(), Batch: batchMsg(1), PlanDigest: p.planDigest,
		ManifestDigest: sha32(p.manifest), LayoutManifestDigest: p.layout[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	if fin.GetError().GetCode() != ErrorCode_MODE_CONFLICT {
		t.Fatalf("finalize on import: %s", fin.GetError().GetCode())
	}
	sealed, err := h.client.SealImport(ctx, &FinalizePublishRequest{
		Header: s.header(), Batch: batchMsg(1), PlanDigest: p.planDigest,
		ManifestDigest: sha32(p.manifest), LayoutManifestDigest: p.layout[:],
	})
	if err != nil || sealed.GetError() != nil {
		t.Fatalf("seal import: %v %+v", err, sealed.GetError())
	}
	if sealed.GetState() != BatchState_CLOSED {
		t.Fatalf("state = %s, want CLOSED", sealed.GetState())
	}

	// The imported seed kept the original issuer epoch and allocated no
	// sequence: a later normal batch starts at 1.
	info, err := h.j.Batch(journal.BatchID{ProducerID: testProducer, RunID: testRun, Seq: 1})
	if err != nil || info.Reservation == nil || info.Reservation.IssuerEpoch != [16]byte{0xCD} {
		t.Fatalf("imported reservation: %+v %v", info.Reservation, err)
	}
	newFramePlan(t, h, s, 2, false)
	info, err = h.j.Batch(journal.BatchID{ProducerID: testProducer, RunID: testRun, Seq: 2})
	if err != nil || info.Reservation == nil || info.Reservation.SequenceBase != 1 {
		t.Fatalf("normal sequence after import: %+v %v", info.Reservation, err)
	}
}

// The E03-02.fencing acceptance over the wire: after the admission barrier,
// a stale RPC answers SESSION_STALE and creates no state.
func TestFencingBarrierCreatesNoState(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()
	digest := sha32([]byte("fenced"))
	if _, err := h.client.BeginPrepare(ctx, &BeginPrepareRequest{
		Header: s.header(), Batch: batchMsg(1), ManifestDigest: digest,
		EventCount: 1, DescriptorBytesTotal: 6,
	}); err != nil {
		t.Fatal(err)
	}

	// The seal/resume barrier: the old admission generation is fenced.
	h.server.FenceAdmission(2)

	resp, err := h.client.PrepareChunk(ctx, &PrepareChunkRequest{
		Header: s.header(), Batch: batchMsg(1), ManifestDigest: digest,
		Offset: 0, ChunkSha256: sha32([]byte("bytes")), Data: []byte("bytes"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetError().GetCode() != ErrorCode_SESSION_STALE {
		t.Fatalf("stale chunk: %s", resp.GetError().GetCode())
	}
	if resp.GetError().GetRetryAction() != RetryAction_RECONNECT {
		t.Fatalf("stale chunk retry action: %s", resp.GetError().GetRetryAction())
	}
	// No state was created: the manifest stream is still empty.
	id := journal.BatchID{ProducerID: testProducer, RunID: testRun, Seq: 1}
	info, err := h.j.Batch(id)
	if err != nil || info.State != journal.StatePreparing {
		t.Fatalf("fenced state: %+v %v", info, err)
	}
	chunks := 0
	idBytes := id.Bytes()
	prefix := append([]byte("batch/"), idBytes[:]...)
	prefix = append(prefix, []byte("/prepare/")...)
	err = h.st.Iterate(nil, nil, func(k, v []byte) error {
		if len(k) == len(prefix)+8 && string(k[:len(prefix)]) == string(prefix) {
			chunks++
		}
		return nil
	})
	if err != nil || chunks != 0 {
		t.Fatalf("stale RPC created %d manifest chunks (err=%v)", chunks, err)
	}
	// The stale session also cannot finalize.
	fin, err := h.client.FinalizePrepare(ctx, &FinalizePrepareRequest{
		Header: s.header(), Batch: batchMsg(1), ManifestDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fin.GetError().GetCode() != ErrorCode_SESSION_STALE {
		t.Fatalf("stale finalize: %s", fin.GetError().GetCode())
	}

	// A new session at the new admission generation works.
	fresh := h.openSessionAt(t, 2, 2)
	if _, err := h.client.PrepareChunk(ctx, &PrepareChunkRequest{
		Header: fresh.header(), Batch: batchMsg(1), ManifestDigest: digest,
		Offset: 0, ChunkSha256: sha32([]byte("bytes")), Data: []byte("bytes"),
	}); err != nil {
		t.Fatal(err)
	}
	// And the old session header stays fenced.
	resp, err = h.client.PrepareChunk(ctx, &PrepareChunkRequest{
		Header: s.header(), Batch: batchMsg(1), ManifestDigest: digest,
		Offset: 4, ChunkSha256: sha32([]byte("more")), Data: []byte("more"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetError().GetCode() != ErrorCode_SESSION_STALE {
		t.Fatalf("old header after fence: %s", resp.GetError().GetCode())
	}
}

// Crash recovery over the wire: a restarted server over the same storage
// resumes the manifest stream at the durable offset and returns the same
// reservation.
func TestServerRestartResumesOverSameStorage(t *testing.T) {
	st := newHarness(t)
	h := st
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()
	manifest := []byte("extractor.manifest.v2\x00restart")
	digest := sha32(manifest)
	if _, err := h.client.BeginPrepare(ctx, &BeginPrepareRequest{
		Header: s.header(), Batch: batchMsg(1), ManifestDigest: digest,
		EventCount: 1, DescriptorBytesTotal: uint64(len(manifest)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.PrepareChunk(ctx, &PrepareChunkRequest{
		Header: s.header(), Batch: batchMsg(1), ManifestDigest: digest,
		Offset: 0, ChunkSha256: sha32(manifest[:5]), Data: manifest[:5],
	}); err != nil {
		t.Fatal(err)
	}

	// Restart: same storage, stopped first server, new process, new
	// session.
	st.stop()
	h2 := startHarness2(t, st)
	s2 := h2.openSessionAt(t, 1, 2)
	resume, err := h2.client.BeginPrepare(ctx, &BeginPrepareRequest{
		Header: s2.header(), Batch: batchMsg(1), ManifestDigest: digest,
		EventCount: 1, DescriptorBytesTotal: uint64(len(manifest)),
	})
	if err != nil || resume.GetError() != nil {
		t.Fatalf("resume: %v %+v", err, resume.GetError())
	}
	if resume.GetResumeOffset() != 5 {
		t.Fatalf("resume offset = %d, want 5", resume.GetResumeOffset())
	}
	if _, err := h2.client.PrepareChunk(ctx, &PrepareChunkRequest{
		Header: s2.header(), Batch: batchMsg(1), ManifestDigest: digest,
		Offset: 5, ChunkSha256: sha32(manifest[5:]), Data: manifest[5:],
	}); err != nil {
		t.Fatal(err)
	}
	fin, err := h2.client.FinalizePrepare(ctx, &FinalizePrepareRequest{
		Header: s2.header(), Batch: batchMsg(1), ManifestDigest: digest,
	})
	if err != nil || fin.GetError() != nil {
		t.Fatalf("finalize after restart: %v %+v", err, fin.GetError())
	}
	// The reservation was allocated once: a retry returns the same seed.
	fin2, err := h2.client.FinalizePrepare(ctx, &FinalizePrepareRequest{
		Header: s2.header(), Batch: batchMsg(1), ManifestDigest: digest,
	})
	if err != nil || string(fin2.GetSeed()) != string(fin.GetSeed()) {
		t.Fatalf("seed stability after restart: %v %v", err, fin2)
	}
}

// stop tears one server process down (the restart point of a crash test).
func (h *harness) stop() {
	h.t.Helper()
	_ = h.conn.Close()
	h.grpcSrv.Stop()
}

// startHarness2 starts a fresh server process over the surviving storage.
func startHarness2(t *testing.T, h *harness) *harness {
	t.Helper()
	return startHarness(t, h.st, openJournal(t, h.st), session.NewGate())
}
