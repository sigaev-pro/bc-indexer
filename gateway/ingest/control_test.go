package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/gateway/journal"
	"github.com/sigaev-pro/bc-indexer/gateway/session"
)

// epochOf is the store epoch of the harness as wire bytes.
func epochOf(h *harness) []byte {
	e := h.j.StoreEpoch()
	return e[:]
}

// tryOpenSession attempts OpenSession at one admission generation and
// returns its typed error, nil when the session opened.
func (h *harness) tryOpenSession(t *testing.T, admission, connection uint64) *Error {
	t.Helper()
	hello, err := h.client.HelloChallenge(context.Background(), &HelloChallengeRequest{ProducerId: testProducer[:], RunId: testRun[:]})
	if err != nil || hello.GetError() != nil {
		t.Fatalf("hello: %v %+v", err, hello.GetError())
	}
	open, err := h.client.OpenSession(context.Background(), &OpenSessionRequest{EchoedChallenge: hello.GetChallenge(),
		ActiveBindingDigest: testBinding[:], AdmissionGeneration: admission, ConnectionGeneration: connection,
		ProducerId: testProducer[:], RunId: testRun[:]})
	if err != nil {
		t.Fatal(err)
	}
	return open.GetError()
}

func jid(seq uint64) journal.BatchID {
	return journal.BatchID{ProducerID: testProducer, RunID: testRun, Seq: seq}
}

// wirePlan is one batch closed over the wire, with the identity the
// acknowledgement and the release are bound to.
type wirePlan struct {
	framePlan
	manifest [32]byte
}

// closedOverWire drives one batch to CLOSED over the wire.
func closedOverWire(t *testing.T, h *harness, s *sessionCtx, seq uint64) wirePlan {
	t.Helper()
	p := newFramePlan(t, h, s, seq, false)
	p.beginPublish(t, h, s)
	stream, err := h.client.PublishChunk(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range p.frames {
		if resp := h.streamOne(t, stream, s, p, f, 0, f.data); resp.GetError() != nil {
			t.Fatalf("frame: %+v", resp.GetError())
		}
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("stream end: %v, want io.EOF", err)
	}
	fin, err := h.client.FinalizePublish(context.Background(), &FinalizePublishRequest{
		Header: s.header(), Batch: batchMsg(seq), PlanDigest: p.planDigest,
		ManifestDigest: sha32(p.manifest), LayoutManifestDigest: p.layout[:],
	})
	if err != nil || fin.GetError() != nil || fin.GetState() != BatchState_CLOSED {
		t.Fatalf("finalize publish: %v %+v", err, fin.GetError())
	}
	return wirePlan{framePlan: p, manifest: as32(sha32(p.manifest))}
}

// ackInJournal records the PubAcks and the NATS_ACKED marker the publisher
// engine writes once every frame of the batch is acknowledged.
func (w wirePlan) ackInJournal(t *testing.T, h *harness) {
	t.Helper()
	id := jid(w.batchSeq)
	for _, f := range w.frames {
		addr := journal.FrameAddr{Ordinal: f.ordinal, FragmentIndex: f.fragment, FrameIndex: f.frame}
		if _, err := h.j.RecordPubAck(id, as32(w.planDigest), addr); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.j.MarkNATSACKed(id, as32(w.planDigest), w.manifest); err != nil {
		t.Fatal(err)
	}
}

// wireErr returns the typed error of one answer; a transport failure is a
// test failure, because the typed error never travels as a gRPC status.
func wireErr(t *testing.T, e *Error, err error) *Error {
	t.Helper()
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if e == nil {
		t.Fatal("the RPC answered a success, want a typed error")
	}
	return e
}

// wantCode fails unless the typed error carries the code, and the retry
// action when one is given. A nil error fails: the RPC answered a success.
func wantCode(t *testing.T, name string, e *Error, code ErrorCode, action RetryAction) {
	t.Helper()
	if e.GetCode() != code {
		t.Fatalf("%s: code %s (%s), want %s", name, e.GetCode(), e.GetDetail(), code)
	}
	if action != RetryAction_RETRY_UNSPECIFIED && e.GetRetryAction() != action {
		t.Fatalf("%s: retry action %s, want %s", name, e.GetRetryAction(), action)
	}
}

func TestGetBatchStateOverRPC(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()

	state := func(plan []byte, cursor *FrameCursor) *GetBatchStateResponse {
		t.Helper()
		resp, err := h.client.GetBatchState(ctx, &GetBatchStateRequest{
			Header: s.header(), Batch: batchMsg(1), PlanDigest: plan, Cursor: cursor,
		})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// A batch the gateway never saw is NOT_FOUND, not an error.
	if resp := state(sha32([]byte("a plan nobody used")), nil); resp.GetState() != BatchState_NOT_FOUND || resp.GetError() != nil {
		t.Fatalf("unknown batch: %s %+v", resp.GetState(), resp.GetError())
	}

	p := closedOverWire(t, h, s, 1)
	// CLOSED lists its cursors and carries no acknowledgement.
	resp := state(p.planDigest, nil)
	if resp.GetError() != nil || resp.GetState() != BatchState_CLOSED || resp.GetAck() != nil || len(resp.GetCursors()) != 3 {
		t.Fatalf("closed: %s ack=%v cursors=%d %+v", resp.GetState(), resp.GetAck(), len(resp.GetCursors()), resp.GetError())
	}
	if c := resp.GetCursors()[0]; c.GetOrdinal() != 0 || c.GetFragmentIndex() != 0 || c.GetFrameIndex() != 0 || c.GetNextOffset() != 6 {
		t.Fatalf("first cursor: %+v", c)
	}
	// Another plan never reads this batch.
	wantCode(t, "another plan", wireErr(t, state(sha32([]byte("another plan")), nil).GetError(), nil),
		ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)

	// NATS_ACKED is the first state whose IngestAck is durable.
	p.ackInJournal(t, h)
	resp = state(p.planDigest, nil)
	if resp.GetError() != nil || resp.GetState() != BatchState_NATS_ACKED || resp.GetAck() == nil {
		t.Fatalf("nats acked: %s %+v %+v", resp.GetState(), resp.GetAck(), resp.GetError())
	}
	ack := resp.GetAck()
	if string(ack.GetManifestDigest()) != string(p.manifest[:]) || string(ack.GetPlanDigest()) != string(p.planDigest) ||
		string(ack.GetLayoutManifestDigest()) != string(p.layout[:]) || string(ack.GetBindingDigest()) != string(testBinding[:]) ||
		string(ack.GetIssuerStoreEpoch()) != string(epochOf(h)) || ack.GetBatch().GetBatchSeq() != 1 {
		t.Fatalf("ingest ack over the wire: %+v", ack)
	}
	// RELEASING still answers the durable acknowledgement.
	if err := h.j.BeginRelease(jid(1), as32(p.planDigest)); err != nil {
		t.Fatal(err)
	}
	if resp = state(p.planDigest, nil); resp.GetError() != nil || resp.GetState() != BatchState_RELEASING || resp.GetAck() == nil {
		t.Fatalf("releasing: %s %+v %+v", resp.GetState(), resp.GetAck(), resp.GetError())
	}
	// RETIRED answers without any acknowledgement, whatever plan is named.
	if _, err := h.j.ReleaseCleanup(jid(1), 50); err != nil {
		t.Fatal(err)
	}
	if _, err := h.j.CompleteRelease(jid(1)); err != nil {
		t.Fatal(err)
	}
	if resp = state(sha32([]byte("any plan for a retired batch")), nil); resp.GetError() != nil || resp.GetState() != BatchState_RETIRED || resp.GetAck() != nil || len(resp.GetCursors()) != 0 {
		t.Fatalf("retired: %s %+v %+v", resp.GetState(), resp.GetAck(), resp.GetError())
	}

	// The states before a plan exists report as they are. PREPARING has no
	// plan to compare yet; PREPARED is bound to the plan digest of its
	// reservation.
	manifest := []byte("extractor.manifest.v2\x00states")
	digest := sha32(manifest)
	if _, err := h.client.BeginPrepare(ctx, &BeginPrepareRequest{Header: s.header(), Batch: batchMsg(2), ManifestDigest: digest,
		EventCount: 1, DescriptorBytesTotal: uint64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	stateOf := func(plan []byte) *GetBatchStateResponse {
		t.Helper()
		r, err := h.client.GetBatchState(ctx, &GetBatchStateRequest{Header: s.header(), Batch: batchMsg(2), PlanDigest: plan})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if resp = stateOf(sha32([]byte("no plan yet"))); resp.GetError() != nil || resp.GetState() != BatchState_PREPARING ||
		resp.GetAck() != nil || len(resp.GetCursors()) != 0 {
		t.Fatalf("preparing: %s %+v %+v", resp.GetState(), resp.GetAck(), resp.GetError())
	}
	for off := 0; off < len(manifest); off += 5 {
		end := off + 5
		if end > len(manifest) {
			end = len(manifest)
		}
		if _, err := h.client.PrepareChunk(ctx, &PrepareChunkRequest{Header: s.header(), Batch: batchMsg(2), ManifestDigest: digest,
			Offset: uint64(off), ChunkSha256: sha32(manifest[off:end]), Data: manifest[off:end]}); err != nil {
			t.Fatal(err)
		}
	}
	fin, err := h.client.FinalizePrepare(ctx, &FinalizePrepareRequest{Header: s.header(), Batch: batchMsg(2), ManifestDigest: digest})
	if err != nil || fin.GetError() != nil {
		t.Fatalf("finalize prepare: %v %+v", err, fin.GetError())
	}
	if resp = stateOf(fin.GetPlanDigest()); resp.GetError() != nil || resp.GetState() != BatchState_PREPARED || resp.GetAck() != nil || len(resp.GetCursors()) != 0 {
		t.Fatalf("prepared: %s %+v %+v", resp.GetState(), resp.GetAck(), resp.GetError())
	}
	wantCode(t, "prepared with another plan", wireErr(t, stateOf(sha32([]byte("another plan"))).GetError(), nil),
		ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
}

// The cursor page is bounded: a batch with more frames than one page holds
// continues through next_cursor until every frame was listed once.
func TestGetBatchStateCursorContinuationOverRPC(t *testing.T) {
	page := stateCursorsPerPage
	stateCursorsPerPage = 2
	t.Cleanup(func() { stateCursorsPerPage = page })

	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()
	p := closedOverWire(t, h, s, 1)

	var listed []*FrameCursor
	var cursor *FrameCursor
	pages := 0
	for {
		resp, err := h.client.GetBatchState(ctx, &GetBatchStateRequest{
			Header: s.header(), Batch: batchMsg(1), PlanDigest: p.planDigest, Cursor: cursor,
		})
		if err != nil || resp.GetError() != nil {
			t.Fatalf("page %d: %v %+v", pages, err, resp.GetError())
		}
		pages++
		listed = append(listed, resp.GetCursors()...)
		if resp.GetNextCursor() == nil {
			break
		}
		cursor = resp.GetNextCursor()
		if pages > 4 {
			t.Fatal("the continuation does not end")
		}
	}
	if pages != 2 || len(listed) != 3 {
		t.Fatalf("pages = %d, cursors = %d, want 2 pages and 3 cursors", pages, len(listed))
	}
	for i, c := range listed {
		f := p.frames[i]
		if c.GetOrdinal() != f.ordinal || c.GetFragmentIndex() != f.fragment || c.GetFrameIndex() != f.frame {
			t.Fatalf("cursor %d = %+v, want frame %+v", i, c, f)
		}
	}
}

func TestReleaseOverRPC(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()
	first := closedOverWire(t, h, s, 1)
	second := closedOverWire(t, h, s, 2)
	third := closedOverWire(t, h, s, 3)
	first.ackInJournal(t, h)
	second.ackInJournal(t, h)

	release := func(seq uint64, manifest, plan []byte) (*ReleaseResponse, *Error) {
		t.Helper()
		resp, err := h.client.Release(ctx, &ReleaseRequest{Header: s.header(), Batch: batchMsg(seq),
			ManifestDigest: manifest, PlanDigest: plan})
		if err != nil {
			t.Fatal(err)
		}
		return resp, resp.GetError()
	}

	var e *Error
	// Release is strictly ordered: the second batch waits for the first.
	_, e = release(2, second.manifest[:], second.planDigest)
	wantCode(t, "out of order", e, ErrorCode_MODE_CONFLICT, RetryAction_RETRY_BACKOFF)
	// Both digests bind the release of the next batch.
	_, e = release(1, sha32([]byte("another manifest")), first.planDigest)
	wantCode(t, "another manifest", e, ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
	_, e = release(1, first.manifest[:], third.planDigest)
	wantCode(t, "another plan", e, ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
	if prefix, _ := h.j.RetiredPrefix(jid(1)); prefix != 0 {
		t.Fatalf("prefix after the refusals = %d", prefix)
	}
	if info, err := h.j.Batch(jid(1)); err != nil || info.State != journal.StateNatsAcked {
		t.Fatalf("state of the first batch after the refusals: %+v %v", info, err)
	}

	// The release ends in ReleaseAck with the retired prefix.
	var resp *ReleaseResponse
	resp, e = release(1, first.manifest[:], first.planDigest)
	if e != nil || resp.GetAck() == nil || resp.GetAck().GetRetiredPrefix() != 1 || resp.GetAck().GetBatch().GetBatchSeq() != 1 {
		t.Fatalf("release: %+v %+v", resp, e)
	}
	// A retry answers the durable prefix again: the gateway never rolls back.
	if resp, e = release(1, first.manifest[:], first.planDigest); e != nil || resp.GetAck().GetRetiredPrefix() != 1 {
		t.Fatalf("release retry: %+v %+v", resp, e)
	}
	resp, e = release(2, second.manifest[:], second.planDigest)
	if e != nil || resp.GetAck().GetRetiredPrefix() != 2 {
		t.Fatalf("second release: %+v %+v", resp, e)
	}
	// A batch that is not acknowledged never releases. The third batch is
	// the next in order here, so only its missing acknowledgement refuses.
	_, e = release(3, third.manifest[:], third.planDigest)
	wantCode(t, "unacknowledged", e, ErrorCode_MODE_CONFLICT, RetryAction_RETRY_BACKOFF)
	if info, err := h.j.Batch(jid(3)); err != nil || info.State != journal.StateClosed {
		t.Fatalf("the unacknowledged batch after the refusal: %+v %v", info, err)
	}
	if info, err := h.j.Batch(jid(2)); err != nil || info.State != journal.StateRetired {
		t.Fatalf("state after the release: %+v %v", info, err)
	}
	// The released batch is RETIRED for a state read and creates no keys.
	state, err := h.client.GetBatchState(ctx, &GetBatchStateRequest{Header: s.header(), Batch: batchMsg(2), PlanDigest: second.planDigest})
	if err != nil || state.GetError() != nil || state.GetState() != BatchState_RETIRED {
		t.Fatalf("state of the released batch: %s %+v %v", state.GetState(), state.GetError(), err)
	}
	begin, err := h.client.BeginPrepare(ctx, &BeginPrepareRequest{Header: s.header(), Batch: batchMsg(2),
		ManifestDigest: second.manifest[:], EventCount: 2, DescriptorBytesTotal: 7})
	if err != nil || begin.GetError().GetCode() != ErrorCode_ALREADY_RETIRED {
		t.Fatalf("prepare of a released batch: %v %+v", err, begin.GetError())
	}
	// The release barrier fences the batch for publisher callbacks.
	if err := h.server.PublisherGate().Apply(session.Callback{Batch: jid(2).Bytes(), PlanDigest: as32(second.planDigest),
		Generation: h.server.PublisherGate().Generation()}, func() error { return nil }); !errors.Is(err, session.ErrBatchReleased) {
		t.Fatalf("callback of a released batch: %v", err)
	}
}

func ackProto(seq uint64, manifest, plan, layout [32]byte, binding []byte) *IngestAck {
	return &IngestAck{Batch: batchMsg(seq), ManifestDigest: manifest[:], PlanDigest: plan[:],
		LayoutManifestDigest: layout[:], BindingDigest: binding, IssuerStoreEpoch: []byte{0xCD, 0xCD, 0xCD, 0xCD, 0xCD, 0xCD, 0xCD, 0xCD, 0xCD, 0xCD, 0xCD, 0xCD, 0xCD, 0xCD, 0xCD, 0xCD}}
}

func outboxProto(seq uint64, plan [32]byte, binding []byte) *OutboxAssertion {
	return &OutboxAssertion{SchemaVersion: 1, Batch: batchMsg(seq), PlanDigest: plan[:], BindingDigest: binding}
}

func TestRecoveryReleaseOverRPC(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()
	var manifest, plan, layout [32]byte
	copy(manifest[:], "recovered manifest digest")
	copy(plan[:], "recovered plan digest value")
	copy(layout[:], "recovered layout digest xx")

	// A jump over the retired prefix never recovers.
	recover := func(seq uint64, ack *IngestAck, assertion *OutboxAssertion) (*ReleaseResponse, *Error) {
		t.Helper()
		resp, err := h.client.RecoveryRelease(ctx, &RecoveryReleaseRequest{Header: s.header(), Batch: batchMsg(seq),
			IngestAck: ack, Assertion: assertion})
		if err != nil {
			t.Fatal(err)
		}
		return resp, resp.GetError()
	}
	_, e := recover(2, ackProto(2, manifest, plan, layout, testBinding[:]), outboxProto(2, plan, testBinding[:]))
	wantCode(t, "jump", e, ErrorCode_MODE_CONFLICT, RetryAction_RETRY_BACKOFF)
	// The assertion and the ack must name the batch of the call.
	_, e = recover(1, ackProto(2, manifest, plan, layout, testBinding[:]), outboxProto(1, plan, testBinding[:]))
	wantCode(t, "ack of another batch", e, ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
	// The assertion must name the plan of the ack it accompanies.
	_, e = recover(1, ackProto(1, manifest, plan, layout, testBinding[:]), outboxProto(1, as32(sha32([]byte("another plan"))), testBinding[:]))
	wantCode(t, "assertion of another plan", e, ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
	// Only an acknowledgement of the active destination recovers.
	otherBinding := sha32([]byte("another binding"))
	_, e = recover(1, ackProto(1, manifest, plan, layout, otherBinding), outboxProto(1, plan, otherBinding))
	wantCode(t, "ack of another binding", e, ErrorCode_BINDING_MISMATCH, RetryAction_FIX_CONFIG)
	// The assertion schema is fixed.
	bad := outboxProto(1, plan, testBinding[:])
	bad.SchemaVersion = 2
	_, e = recover(1, ackProto(1, manifest, plan, layout, testBinding[:]), bad)
	wantCode(t, "assertion schema", e, ErrorCode_VERSION_UNSUPPORTED, RetryAction_DRAIN_OLD_BUILD)
	// A batch the store still holds goes through Release, not recovery.
	local := closedOverWire(t, h, s, 1)
	localAck := ackProto(1, local.manifest, as32(local.planDigest), local.layout, testBinding[:])
	_, e = recover(1, localAck, outboxProto(1, as32(local.planDigest), testBinding[:]))
	wantCode(t, "local record", e, ErrorCode_IMMUTABLE_CONFLICT, RetryAction_DIAGNOSE_IMMUTABLE)
	if info, err := h.j.Batch(jid(1)); err != nil || info.State != journal.StateClosed {
		t.Fatalf("local record after the refusal: %+v %v", info, err)
	}
	if prefix, _ := h.j.RetiredPrefix(jid(1)); prefix != 0 {
		t.Fatalf("prefix after the refusals = %d", prefix)
	}

	// Release the held batch, then recover the next one the store lost
	// without any payload.
	local.ackInJournal(t, h)
	if resp, err := h.client.Release(ctx, &ReleaseRequest{Header: s.header(), Batch: batchMsg(1),
		ManifestDigest: local.manifest[:], PlanDigest: local.planDigest}); err != nil || resp.GetError() != nil {
		t.Fatalf("release: %v %+v", err, resp.GetError())
	}
	resp, e := recover(2, ackProto(2, manifest, plan, layout, testBinding[:]), outboxProto(2, plan, testBinding[:]))
	if e != nil || resp.GetAck().GetRetiredPrefix() != 2 || resp.GetAck().GetBatch().GetBatchSeq() != 2 {
		t.Fatalf("recovery release: %+v %+v", resp, e)
	}
	if prefix, _ := h.j.RetiredPrefix(jid(2)); prefix != 2 {
		t.Fatalf("prefix after the recovery = %d", prefix)
	}
	// The retry answers the same prefix; another assertion is a conflict.
	if resp, e = recover(2, ackProto(2, manifest, plan, layout, testBinding[:]), outboxProto(2, plan, testBinding[:])); e != nil || resp.GetAck().GetRetiredPrefix() != 2 {
		t.Fatalf("recovery retry: %+v %+v", resp, e)
	}
	other := layout
	other[0] ^= 0xFF
	_, e = recover(2, ackProto(2, manifest, plan, other, testBinding[:]), outboxProto(2, plan, testBinding[:]))
	wantCode(t, "another assertion", e, ErrorCode_IMMUTABLE_CONFLICT, RetryAction_DIAGNOSE_IMMUTABLE)
	// The recovered sequence is retired.
	state, err := h.client.GetBatchState(ctx, &GetBatchStateRequest{Header: s.header(), Batch: batchMsg(2), PlanDigest: plan[:]})
	if err != nil || state.GetError() != nil || state.GetState() != BatchState_RETIRED {
		t.Fatalf("state of the recovered batch: %s %+v %v", state.GetState(), state.GetError(), err)
	}
}

var testDrainID = []byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}

func TestSealGenerationOverRPC(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()
	p := closedOverWire(t, h, s, 1)
	p.ackInJournal(t, h)

	seal := func(header *SessionHeader, binding, admission, highwater uint64, drain []byte) (*SealGenerationResponse, *Error) {
		t.Helper()
		resp, err := h.client.SealGeneration(ctx, &SealGenerationRequest{Header: header, BindingGeneration: binding,
			AdmissionGeneration: admission, DrainId: drain, BatchHighwater: highwater})
		if err != nil {
			t.Fatal(err)
		}
		return resp, resp.GetError()
	}

	// The frontier must be exhausted: the retired prefix equals the highwater.
	_, e := seal(s.header(), 1, 1, 1, testDrainID)
	wantCode(t, "unfinished frontier", e, ErrorCode_MODE_CONFLICT, RetryAction_RETRY_BACKOFF)
	// The binding generation must be the active one.
	_, e = seal(s.header(), 9, 1, 0, testDrainID)
	wantCode(t, "binding generation", e, ErrorCode_GENERATION_CONFLICT, RetryAction_FIX_CONFIG)
	// The sealed generation is the session's or the one below it.
	_, e = seal(s.header(), 1, 7, 0, testDrainID)
	wantCode(t, "admission generation", e, ErrorCode_GENERATION_CONFLICT, RetryAction_RECONNECT)
	// A zero drain id is no identity.
	_, e = seal(s.header(), 1, 1, 0, make([]byte, 16))
	wantCode(t, "zero drain id", e, ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
	if _, open, err := h.j.SealedGeneration(jid(0)); err != nil || open {
		t.Fatalf("a refused seal left a record: open=%v %v", open, err)
	}

	if _, err := h.client.Release(ctx, &ReleaseRequest{Header: s.header(), Batch: batchMsg(1),
		ManifestDigest: p.manifest[:], PlanDigest: p.planDigest}); err != nil {
		t.Fatal(err)
	}
	resp, e := seal(s.header(), 1, 1, 1, testDrainID)
	if e != nil || resp.GetSeal() == nil {
		t.Fatalf("seal: %+v %+v", resp, e)
	}
	got := resp.GetSeal()
	if string(got.GetDrainId()) != string(testDrainID) || string(got.GetBindingDigest()) != string(testBinding[:]) ||
		got.GetAdmissionGeneration() != 1 || got.GetHighwater() != 1 || string(got.GetIssuerStoreEpoch()) != string(epochOf(h)) {
		t.Fatalf("seal ack over the wire: %+v", got)
	}

	// The seal fenced the sealed generation: its session is stale for every
	// RPC and creates no state.
	stale, err := h.client.PrepareChunk(ctx, &PrepareChunkRequest{Header: s.header(), Batch: batchMsg(5),
		ManifestDigest: p.manifest[:], Offset: 0, ChunkSha256: sha32([]byte("late")), Data: []byte("late")})
	if err != nil || stale.GetError().GetCode() != ErrorCode_SESSION_STALE {
		t.Fatalf("late RPC of the sealed generation: %v %+v", err, stale.GetError())
	}
	_, e = seal(s.header(), 1, 1, 1, testDrainID)
	wantCode(t, "seal retry from the fenced session", e, ErrorCode_SESSION_STALE, RetryAction_RECONNECT)

	// The retry comes from a session of the next generation and returns the
	// same SealAck; the same drain with another intent conflicts.
	fresh := h.openSessionAt(t, 2, 2)
	retry, e := seal(fresh.header(), 1, 1, 1, testDrainID)
	if e != nil || string(retry.GetSeal().GetIssuerStoreEpoch()) != string(got.GetIssuerStoreEpoch()) ||
		retry.GetSeal().GetHighwater() != 1 || retry.GetSeal().GetAdmissionGeneration() != 1 {
		t.Fatalf("seal retry: %+v %+v", retry.GetSeal(), e)
	}
	_, e = seal(fresh.header(), 1, 1, 2, testDrainID)
	wantCode(t, "same drain, another highwater", e, ErrorCode_IMMUTABLE_CONFLICT, RetryAction_DIAGNOSE_IMMUTABLE)
	// A session of the sealed generation no longer opens.
	old, err := h.client.HelloChallenge(ctx, &HelloChallengeRequest{ProducerId: testProducer[:], RunId: testRun[:]})
	if err != nil {
		t.Fatal(err)
	}
	denied, err := h.client.OpenSession(ctx, &OpenSessionRequest{EchoedChallenge: old.GetChallenge(), ActiveBindingDigest: testBinding[:],
		AdmissionGeneration: 1, ConnectionGeneration: 3, ProducerId: testProducer[:], RunId: testRun[:]})
	if err != nil || denied.GetError().GetCode() != ErrorCode_GENERATION_CONFLICT {
		t.Fatalf("session of a sealed generation: %v %+v", err, denied.GetError())
	}

	// After a gateway restart the admission barrier starts empty. The
	// durable seal alone keeps the sealed generation closed; the next one
	// opens.
	h.stop()
	h2 := startHarness2(t, h)
	wantCode(t, "sealed generation after a restart", h2.tryOpenSession(t, 1, 1), ErrorCode_GENERATION_CONFLICT, RetryAction_RECONNECT)
	after := h2.openSessionAt(t, 2, 1)
	// The seal retry from the new process returns the same SealAck and puts
	// the admission barrier back.
	retried, err := h2.client.SealGeneration(ctx, &SealGenerationRequest{Header: after.header(), BindingGeneration: 1,
		AdmissionGeneration: 1, DrainId: testDrainID, BatchHighwater: 1})
	if err != nil || retried.GetError() != nil || retried.GetSeal().String() != got.String() {
		t.Fatalf("seal retry after a restart: %v %+v", err, retried.GetError())
	}
	if epoch := h2.gate.Barrier().Epoch(); epoch != 2 {
		t.Fatalf("admission barrier after the seal retry = %d, want 2", epoch)
	}
	// The batch state and the retired prefix survived the restart.
	state, err := h2.client.GetBatchState(ctx, &GetBatchStateRequest{Header: after.header(), Batch: batchMsg(1), PlanDigest: p.planDigest})
	if err != nil || state.GetError() != nil || state.GetState() != BatchState_RETIRED {
		t.Fatalf("state after a restart: %s %+v %v", state.GetState(), state.GetError(), err)
	}
	if prefix, _ := h2.j.RetiredPrefix(jid(1)); prefix != 1 {
		t.Fatalf("retired prefix after a restart = %d, want 1", prefix)
	}
}

func TestResumeOverRPC(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()

	resume := func(header *SessionHeader, old *SealAck, next uint64, id []byte) (*ResumeResponse, *Error) {
		t.Helper()
		resp, err := h.client.Resume(ctx, &ResumeRequest{Header: header, OldSeal: old, ResumeId: id,
			ActiveBindingDigest: testBinding[:], NextAdmissionGeneration: next})
		if err != nil {
			t.Fatal(err)
		}
		return resp, resp.GetError()
	}
	resumeID := []byte{5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5}
	anySeal := &SealAck{DrainId: testDrainID, BindingDigest: testBinding[:], AdmissionGeneration: 1,
		Highwater: 0, IssuerStoreEpoch: epochOf(h)}

	// Resume needs a sealed generation.
	_, e := resume(s.header(), anySeal, 2, resumeID)
	wantCode(t, "resume without a seal", e, ErrorCode_DRAIN_REQUIRED, RetryAction_DRAIN_OLD_BUILD)

	if _, err := h.client.SealGeneration(ctx, &SealGenerationRequest{Header: s.header(), BindingGeneration: 1,
		AdmissionGeneration: 1, DrainId: testDrainID, BatchHighwater: 0}); err != nil {
		t.Fatal(err)
	}
	fresh := h.openSessionAt(t, 2, 2)
	// The old seal must match entirely.
	wrong := &SealAck{DrainId: testDrainID, BindingDigest: testBinding[:], AdmissionGeneration: 1,
		Highwater: 3, IssuerStoreEpoch: epochOf(h)}
	_, e = resume(fresh.header(), wrong, 2, resumeID)
	wantCode(t, "another old seal", e, ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
	// The binding the resume names must be the active one.
	under, err := h.client.Resume(ctx, &ResumeRequest{Header: fresh.header(), OldSeal: anySeal, ResumeId: resumeID,
		ActiveBindingDigest: sha32([]byte("another binding")), NextAdmissionGeneration: 2})
	wantCode(t, "resume under another binding", wireErr(t, under.GetError(), err), ErrorCode_BINDING_MISMATCH, RetryAction_FIX_CONFIG)
	// Exactly the next admission generation.
	_, e = resume(fresh.header(), anySeal, 3, resumeID)
	wantCode(t, "not the next generation", e, ErrorCode_GENERATION_CONFLICT, RetryAction_RECONNECT)
	// A zero resume id is no identity.
	_, e = resume(fresh.header(), anySeal, 2, make([]byte, 16))
	wantCode(t, "zero resume id", e, ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)

	resp, e := resume(fresh.header(), anySeal, 2, resumeID)
	if e != nil || resp.GetAck() == nil {
		t.Fatalf("resume: %+v %+v", resp, e)
	}
	ack := resp.GetAck()
	if string(ack.GetResumeId()) != string(resumeID) || string(ack.GetBindingDigest()) != string(testBinding[:]) ||
		ack.GetOldAdmissionGeneration() != 1 || ack.GetNewAdmissionGeneration() != 2 ||
		string(ack.GetDrainId()) != string(testDrainID) || string(ack.GetIssuerStoreEpoch()) != string(epochOf(h)) {
		t.Fatalf("resume ack over the wire: %+v", ack)
	}
	// A repeated intent returns the same ResumeAck; another one conflicts.
	if again, e := resume(fresh.header(), anySeal, 2, resumeID); e != nil || string(again.GetAck().GetResumeId()) != string(resumeID) ||
		again.GetAck().GetNewAdmissionGeneration() != 2 {
		t.Fatalf("resume retry: %+v %+v", again.GetAck(), e)
	}
	_, e = resume(fresh.header(), anySeal, 2, []byte{6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6})
	wantCode(t, "another resume id", e, ErrorCode_IMMUTABLE_CONFLICT, RetryAction_DIAGNOSE_IMMUTABLE)

	// The seal and the resume outlive the process; the admission barrier
	// does not. After a gateway restart the resumed generation opens, the
	// sealed one below it does not, and the resume retry answers its ack.
	h.stop()
	h2 := startHarness2(t, h)
	wantCode(t, "sealed generation after a restart", h2.tryOpenSession(t, 1, 2), ErrorCode_GENERATION_CONFLICT, RetryAction_RECONNECT)
	live := h2.openSessionAt(t, 2, 2)
	again, err := h2.client.Resume(ctx, &ResumeRequest{Header: live.header(), OldSeal: anySeal, ResumeId: resumeID,
		ActiveBindingDigest: testBinding[:], NextAdmissionGeneration: 2})
	if err != nil || again.GetError() != nil || again.GetAck().String() != ack.String() {
		t.Fatalf("resume retry after a restart: %v %+v", err, again.GetError())
	}
}

// bindingSpec is a destination descriptor of the test fixtures.
type bindingSpec struct {
	dst             [16]byte
	gen             uint64
	chain, genesis  [32]byte
	account, stream string
	prefix          string
}

// digest is an independent oracle of the descriptor digest formula of the
// contract, so the fixture does not trust the code under test for it.
func (sp bindingSpec) digest() [32]byte {
	h := sha256.New()
	h.Write([]byte("extractor.binding.v1"))
	h.Write([]byte{0})
	h.Write(sp.dst[:])
	h.Write(be64(sp.gen))
	h.Write([]byte{1})
	h.Write(sp.chain[:])
	h.Write(sp.genesis[:])
	for _, s := range []string{sp.account, sp.stream, sp.prefix} {
		h.Write(be64(uint64(len(s)))[4:]) // u32be(length)
		h.Write([]byte(s))
	}
	return as32(h.Sum(nil))
}

// json is the strict descriptor form of the spec.
func (sp bindingSpec) json() []byte {
	d := sp.digest()
	doc := map[string]interface{}{
		"schema_version":    1,
		"destination_id":    hex.EncodeToString(sp.dst[:]),
		"generation":        strconv.FormatUint(sp.gen, 10),
		"network":           map[string]string{"family": "EVM", "chain_id": hex.EncodeToString(sp.chain[:]), "genesis_hash": hex.EncodeToString(sp.genesis[:])},
		"expected_account":  sp.account,
		"stream":            sp.stream,
		"subject_prefix":    sp.prefix,
		"descriptor_digest": hex.EncodeToString(d[:]),
	}
	out, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return out
}

var (
	activeSpec = bindingSpec{dst: [16]byte{7}, gen: 3, account: "account", stream: "extractor", prefix: "evm",
		chain: sha256.Sum256([]byte("chain id")), genesis: sha256.Sum256([]byte("genesis hash"))}
	switchID      = []byte{0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB}
	otherSwitchID = []byte{0xAC, 0xAC, 0xAC, 0xAC, 0xAC, 0xAC, 0xAC, 0xAC, 0xAC, 0xAC, 0xAC, 0xAC, 0xAC, 0xAC, 0xAC, 0xAC}
)

// boundCtx is a session opened for one binding digest.
type boundCtx struct {
	sessionCtx
	binding [32]byte
}

func (c *boundCtx) header() *SessionHeader {
	h := c.sessionCtx.header()
	h.BindingDigest = c.binding[:]
	return h
}

// newSwitchHarness starts a gateway whose journal knows the identity of its
// active binding, which the switch checks need.
func newSwitchHarness(t *testing.T) *harness {
	t.Helper()
	st := storage.NewMemory()
	active := journal.Binding{DestinationID: activeSpec.dst, Generation: activeSpec.gen, ChainID: activeSpec.chain,
		GenesisHash: activeSpec.genesis, DescriptorDigest: activeSpec.digest()}
	j, err := journal.Open(st, journal.Options{BindingDigest: activeSpec.digest(), Binding: &active})
	if err != nil {
		t.Fatal(err)
	}
	return startHarness(t, st, j, session.NewGate())
}

func (h *harness) openSessionBound(t *testing.T, admission, connection uint64, binding [32]byte) *boundCtx {
	t.Helper()
	ctx := context.Background()
	hello, err := h.client.HelloChallenge(ctx, &HelloChallengeRequest{ProducerId: testProducer[:], RunId: testRun[:]})
	if err != nil || hello.GetError() != nil {
		t.Fatalf("hello: %v %+v", err, hello.GetError())
	}
	open, err := h.client.OpenSession(ctx, &OpenSessionRequest{EchoedChallenge: hello.GetChallenge(),
		ActiveBindingDigest: binding[:], AdmissionGeneration: admission, ConnectionGeneration: connection,
		ProducerId: testProducer[:], RunId: testRun[:]})
	if err != nil {
		t.Fatal(err)
	}
	if e := open.GetError(); e != nil {
		t.Fatalf("open session: %s %s", e.GetCode(), e.GetDetail())
	}
	return &boundCtx{sessionCtx: sessionCtx{token: open.GetSessionToken(), admission: admission, connection: connection}, binding: binding}
}

// fakeVerifier counts the online target checks and fails on demand.
type fakeVerifier struct {
	calls int32
	err   error
}

func (v *fakeVerifier) VerifyTarget(context.Context, [32]byte) error {
	atomic.AddInt32(&v.calls, 1)
	return v.err
}

func (v *fakeVerifier) count() int { return int(atomic.LoadInt32(&v.calls)) }

func TestSwitchOverRPC(t *testing.T) {
	h := newSwitchHarness(t)
	ctx := context.Background()
	a := h.openSessionBound(t, 1, 1, activeSpec.digest())
	active := activeSpec.digest()

	// The switch continues a sealed generation.
	seal, err := h.client.SealGeneration(ctx, &SealGenerationRequest{Header: a.header(), BindingGeneration: activeSpec.gen,
		AdmissionGeneration: 1, DrainId: testDrainID, BatchHighwater: 0})
	if err != nil || seal.GetError() != nil {
		t.Fatalf("seal: %v %+v", err, seal.GetError())
	}
	b := h.openSessionBound(t, 2, 2, active)
	desired := activeSpec
	desired.gen = 4
	desired.prefix = "evm2"
	newDigest := desired.digest()

	prepare := func(header *SessionHeader, old *SealAck, oldBinding, descriptor, id []byte) (*PrepareSwitchResponse, *Error) {
		t.Helper()
		resp, err := h.client.PrepareSwitch(ctx, &PrepareSwitchRequest{Header: header, OldSeal: old,
			OldBindingDigest: oldBinding, DesiredDescriptor: descriptor, SwitchId: id})
		if err != nil {
			t.Fatal(err)
		}
		return resp, resp.GetError()
	}
	// A switch without a seal is refused: drain first.
	fresh := newSwitchHarness(t)
	noseal := fresh.openSessionBound(t, 1, 1, active)
	// The target is down: a durable refusal is answered without it.
	fresh.server.SetTargetVerifier(&fakeVerifier{err: ErrTargetUnavailable})
	drained, err := fresh.client.PrepareSwitch(ctx, &PrepareSwitchRequest{Header: noseal.header(), OldSeal: seal.GetSeal(),
		OldBindingDigest: active[:], DesiredDescriptor: desired.json(), SwitchId: switchID})
	wantCode(t, "switch without a seal", wireErr(t, drained.GetError(), err), ErrorCode_DRAIN_REQUIRED, RetryAction_DRAIN_OLD_BUILD)

	var e *Error
	// The online check of the desired target is mandatory: without a
	// verifier, or with an unreachable target, nothing becomes durable.
	_, e = prepare(b.header(), seal.GetSeal(), active[:], desired.json(), switchID)
	wantCode(t, "no verifier", e, ErrorCode_TRANSIENT_UNAVAILABLE, RetryAction_RETRY_BACKOFF)
	verifier := &fakeVerifier{err: ErrTargetUnavailable}
	h.server.SetTargetVerifier(verifier)
	_, e = prepare(b.header(), seal.GetSeal(), active[:], desired.json(), switchID)
	wantCode(t, "unavailable target", e, ErrorCode_TRANSIENT_UNAVAILABLE, RetryAction_RETRY_BACKOFF)
	verifier.err = errors.New("stream metadata key is missing")
	_, e = prepare(b.header(), seal.GetSeal(), active[:], desired.json(), switchID)
	wantCode(t, "mismatched target", e, ErrorCode_BINDING_MISMATCH, RetryAction_FIX_CONFIG)
	// A malformed or oversized descriptor is refused before any check.
	calls := verifier.count()
	_, e = prepare(b.header(), seal.GetSeal(), active[:], []byte("{not json"), switchID)
	wantCode(t, "malformed descriptor", e, ErrorCode_INVALID_DESCRIPTOR, RetryAction_FIX_CONFIG)
	_, e = prepare(b.header(), seal.GetSeal(), active[:], make([]byte, destination.MaxDescriptorBytes+1), switchID)
	wantCode(t, "oversized descriptor", e, ErrorCode_LIMIT_INCOMPATIBLE, RetryAction_FIX_CONFIG)
	// The old binding the switch continues must be the session's binding.
	_, e = prepare(b.header(), seal.GetSeal(), newDigest[:], desired.json(), switchID)
	wantCode(t, "old binding differs from the session", e, ErrorCode_BINDING_MISMATCH, RetryAction_FIX_CONFIG)
	if verifier.count() != calls {
		t.Fatalf("a request refused before its target check reached the target: %d checks, want %d", verifier.count(), calls)
	}
	if _, open, err := h.j.CurrentSwitch(); err != nil || open {
		t.Fatalf("a refused switch left a record: open=%v %v", open, err)
	}
	// With a verified target the journal checks the durable rules: the old
	// seal must be the sealed one, and the descriptor a legal successor (the
	// next generation of the same binding line, and another binding).
	verifier.err = nil
	wrong := &SealAck{DrainId: testDrainID, BindingDigest: active[:], AdmissionGeneration: 1,
		Highwater: 2, IssuerStoreEpoch: epochOf(h)}
	_, e = prepare(b.header(), wrong, active[:], desired.json(), switchID)
	wantCode(t, "another old seal", e, ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
	jump := activeSpec
	jump.gen = 6
	jump.prefix = "evm6"
	_, e = prepare(b.header(), seal.GetSeal(), active[:], jump.json(), switchID)
	wantCode(t, "generation jump", e, ErrorCode_GENERATION_CONFLICT, RetryAction_FIX_CONFIG)
	// The desired binding must be another one.
	_, e = prepare(b.header(), seal.GetSeal(), active[:], activeSpec.json(), switchID)
	wantCode(t, "desired equals active", e, ErrorCode_MODE_CONFLICT, RetryAction_RETRY_BACKOFF)
	if _, open, err := h.j.CurrentSwitch(); err != nil || open {
		t.Fatalf("a refused switch left a record: open=%v %v", open, err)
	}

	// A verified target produces SwitchPrepared; the active binding stays.
	var prepared *PrepareSwitchResponse
	prepared, e = prepare(b.header(), seal.GetSeal(), active[:], desired.json(), switchID)
	if e != nil || prepared.GetPrepared() == nil {
		t.Fatalf("prepare switch: %+v %+v", prepared, e)
	}
	receipt := prepared.GetPrepared()
	if string(receipt.GetSwitchId()) != string(switchID) || string(receipt.GetOldBindingDigest()) != string(active[:]) ||
		string(receipt.GetNewBindingDigest()) != string(newDigest[:]) || string(receipt.GetDrainId()) != string(testDrainID) ||
		receipt.GetAdmissionGeneration() != 1 || string(receipt.GetIssuerStoreEpoch()) != string(epochOf(h)) {
		t.Fatalf("switch prepared over the wire: %+v", receipt)
	}
	if digest := h.j.BindingDigest(); digest != active {
		t.Fatalf("active binding after prepare: %x", digest)
	}
	// A retry answers the same receipt without a new check, even when the
	// target is down now.
	calls = verifier.count()
	verifier.err = ErrTargetUnavailable
	if again, e := prepare(b.header(), seal.GetSeal(), active[:], desired.json(), switchID); e != nil ||
		string(again.GetPrepared().GetSwitchId()) != string(switchID) || verifier.count() != calls {
		t.Fatalf("prepare retry: %+v %+v checks=%d", again.GetPrepared(), e, verifier.count())
	}
	_, e = prepare(b.header(), seal.GetSeal(), active[:], desired.json(), otherSwitchID)
	wantCode(t, "another switch id", e, ErrorCode_IMMUTABLE_CONFLICT, RetryAction_DIAGNOSE_IMMUTABLE)

	commit := func(header *SessionHeader, prepared *SwitchPrepared, assertion *BindingCommitAssertion) (*CommitSwitchResponse, *Error) {
		t.Helper()
		resp, err := h.client.CommitSwitch(ctx, &CommitSwitchRequest{Header: header, SwitchId: switchID,
			Prepared: prepared, Assertion: assertion})
		if err != nil {
			t.Fatal(err)
		}
		return resp, resp.GetError()
	}
	assertion := &BindingCommitAssertion{SchemaVersion: 1, SwitchId: switchID, BindingDigest: newDigest[:], BindingGeneration: 4}
	// The exact receipt and the producer's binding commit are both required.
	other := &SwitchPrepared{SwitchId: receipt.GetSwitchId(), OldBindingDigest: receipt.GetOldBindingDigest(),
		NewBindingDigest: active[:], DrainId: receipt.GetDrainId(),
		AdmissionGeneration: receipt.GetAdmissionGeneration(), IssuerStoreEpoch: receipt.GetIssuerStoreEpoch()}
	_, e = commit(b.header(), other, assertion)
	wantCode(t, "another receipt", e, ErrorCode_IMMUTABLE_CONFLICT, RetryAction_DIAGNOSE_IMMUTABLE)
	badAssert := &BindingCommitAssertion{SchemaVersion: 1, SwitchId: switchID, BindingDigest: active[:], BindingGeneration: 4}
	_, e = commit(b.header(), receipt, badAssert)
	wantCode(t, "assertion of another binding", e, ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
	badAssert = &BindingCommitAssertion{SchemaVersion: 2, SwitchId: switchID, BindingDigest: newDigest[:], BindingGeneration: 4}
	_, e = commit(b.header(), receipt, badAssert)
	wantCode(t, "assertion schema", e, ErrorCode_VERSION_UNSUPPORTED, RetryAction_DRAIN_OLD_BUILD)
	// The first activation re-checks the target; a failure changes nothing.
	_, e = commit(b.header(), receipt, assertion)
	wantCode(t, "target down at commit", e, ErrorCode_TRANSIENT_UNAVAILABLE, RetryAction_RETRY_BACKOFF)
	if cur, open, err := h.j.CurrentSwitch(); err != nil || !open || cur.Phase != journal.SwitchPreparedPhase {
		t.Fatalf("switch phase after the refused commit: %+v %v %v", cur, open, err)
	}
	verifier.err = errors.New("stream metadata key is missing")
	_, e = commit(b.header(), receipt, assertion)
	wantCode(t, "target mismatch at commit", e, ErrorCode_BINDING_MISMATCH, RetryAction_FIX_CONFIG)

	verifier.err = nil
	committed, e := commit(b.header(), receipt, assertion)
	if e != nil || committed.GetCommitted() == nil || committed.GetCommitted().GetPrepared().String() != receipt.String() {
		t.Fatalf("commit switch: %+v %+v", committed, e)
	}
	if digest := h.j.BindingDigest(); digest != newDigest {
		t.Fatalf("active binding after the commit: %x, want %x", digest, newDigest)
	}
	// The committed receipt answers a retry without the target, from the
	// session that prepared the switch (bound to the old binding).
	calls = verifier.count()
	verifier.err = ErrTargetUnavailable
	if again, e := commit(b.header(), receipt, assertion); e != nil ||
		again.GetCommitted().GetPrepared().String() != receipt.String() || verifier.count() != calls {
		t.Fatalf("commit retry: %+v %+v checks=%d", again.GetCommitted(), e, verifier.count())
	}
	// That old session no longer runs data RPCs: it is bound to the old binding.
	denied, err := h.client.GetBatchState(ctx, &GetBatchStateRequest{Header: b.header(), Batch: batchMsg(1), PlanDigest: active[:]})
	if err != nil || denied.GetError().GetCode() != ErrorCode_BINDING_MISMATCH {
		t.Fatalf("data RPC of the old binding: %v %+v", err, denied.GetError())
	}
	// Resume opens the next admission generation under the new binding.
	c := h.openSessionBound(t, 2, 3, newDigest)
	resumed, err := h.client.Resume(ctx, &ResumeRequest{Header: c.header(), OldSeal: seal.GetSeal(),
		ResumeId: []byte{5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5}, ActiveBindingDigest: newDigest[:],
		NextAdmissionGeneration: 2})
	if err != nil || resumed.GetError() != nil {
		t.Fatalf("resume after the switch: %v %+v", err, resumed.GetError())
	}
	if string(resumed.GetAck().GetBindingDigest()) != string(newDigest[:]) || resumed.GetAck().GetNewAdmissionGeneration() != 2 {
		t.Fatalf("resume ack after the switch: %+v", resumed.GetAck())
	}
	// Another switch id never jumps over the committed one.
	_, e = prepare(c.header(), seal.GetSeal(), newDigest[:], desired.json(), otherSwitchID)
	wantCode(t, "another switch over a committed one", e, ErrorCode_IMMUTABLE_CONFLICT, RetryAction_DIAGNOSE_IMMUTABLE)

	// A later drain cycle switches again: the committed switch of the
	// earlier seal is history and blocks nothing.
	secondDrain := sha32([]byte("second drain"))[:16]
	seal2, err := h.client.SealGeneration(ctx, &SealGenerationRequest{Header: c.header(), BindingGeneration: 4,
		AdmissionGeneration: 2, DrainId: secondDrain, BatchHighwater: 0})
	if err != nil || seal2.GetError() != nil {
		t.Fatalf("second seal: %v %+v", err, seal2.GetError())
	}
	if string(seal2.GetSeal().GetBindingDigest()) != string(newDigest[:]) || seal2.GetSeal().GetAdmissionGeneration() != 2 {
		t.Fatalf("second seal ack: %+v", seal2.GetSeal())
	}
	d := h.openSessionBound(t, 3, 4, newDigest)
	next := activeSpec
	next.gen = 5
	next.prefix = "evm5"
	nextDigest := next.digest()
	verifier.err = nil
	second, err := h.client.PrepareSwitch(ctx, &PrepareSwitchRequest{Header: d.header(), OldSeal: seal2.GetSeal(),
		OldBindingDigest: newDigest[:], DesiredDescriptor: next.json(), SwitchId: otherSwitchID})
	if err != nil || second.GetError() != nil || second.GetPrepared() == nil {
		t.Fatalf("second switch: %v %+v", err, second.GetError())
	}
	if string(second.GetPrepared().GetOldBindingDigest()) != string(newDigest[:]) ||
		string(second.GetPrepared().GetNewBindingDigest()) != string(nextDigest[:]) ||
		second.GetPrepared().GetAdmissionGeneration() != 2 || string(second.GetPrepared().GetDrainId()) != string(secondDrain) {
		t.Fatalf("second switch prepared: %+v", second.GetPrepared())
	}
	committed2, err := h.client.CommitSwitch(ctx, &CommitSwitchRequest{Header: d.header(), SwitchId: otherSwitchID,
		Prepared: second.GetPrepared(), Assertion: &BindingCommitAssertion{SchemaVersion: 1, SwitchId: otherSwitchID,
			BindingDigest: nextDigest[:], BindingGeneration: 5}})
	if err != nil || committed2.GetError() != nil {
		t.Fatalf("second commit: %v %+v", err, committed2.GetError())
	}
	if digest := h.j.BindingDigest(); digest != nextDigest {
		t.Fatalf("active binding after the second switch: %x, want %x", digest, nextDigest)
	}
	// The intent of the first switch is retired.
	_, e = commit(d.header(), receipt, assertion)
	wantCode(t, "commit retry of a retired switch", e, ErrorCode_IMMUTABLE_CONFLICT, RetryAction_DIAGNOSE_IMMUTABLE)
	// The second cycle resumes under the binding it activated.
	last := h.openSessionBound(t, 3, 5, nextDigest)
	resumed2, err := h.client.Resume(ctx, &ResumeRequest{Header: last.header(), OldSeal: seal2.GetSeal(),
		ResumeId: []byte{7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7}, ActiveBindingDigest: nextDigest[:],
		NextAdmissionGeneration: 3})
	if err != nil || resumed2.GetError() != nil || resumed2.GetAck().GetOldAdmissionGeneration() != 2 ||
		resumed2.GetAck().GetNewAdmissionGeneration() != 3 || string(resumed2.GetAck().GetBindingDigest()) != string(nextDigest[:]) {
		t.Fatalf("resume after the second switch: %v %+v", err, resumed2.GetError())
	}
}

func TestSwitchNeedsKnownActiveBinding(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()
	seal, err := h.client.SealGeneration(ctx, &SealGenerationRequest{Header: s.header(), BindingGeneration: 1,
		AdmissionGeneration: 1, DrainId: testDrainID, BatchHighwater: 0})
	if err != nil || seal.GetError() != nil {
		t.Fatalf("seal: %v %+v", err, seal.GetError())
	}
	fresh := h.openSessionAt(t, 2, 2)
	h.server.SetTargetVerifier(&fakeVerifier{})
	resp, err := h.client.PrepareSwitch(ctx, &PrepareSwitchRequest{Header: fresh.header(), OldSeal: seal.GetSeal(),
		OldBindingDigest: testBinding[:], DesiredDescriptor: activeSpec.json(), SwitchId: switchID})
	if err != nil {
		t.Fatal(err)
	}
	// The continuity checks of a switch need the active binding identity;
	// without it the switch is refused, never skipped.
	wantCode(t, "unknown active binding", wireErr(t, resp.GetError(), nil), ErrorCode_BINDING_MISMATCH, RetryAction_FIX_CONFIG)
}

// Every control RPC refuses a request whose session header is not valid
// before anything runs; the typed error carries the registry code.
func TestControlRPCsValidateSessionHeader(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()
	p := closedOverWire(t, h, s, 1)
	epoch := h.j.StoreEpoch()

	rpcs := map[string]func(header *SessionHeader) *Error{
		"GetBatchState": func(hd *SessionHeader) *Error {
			resp, err := h.client.GetBatchState(ctx, &GetBatchStateRequest{Header: hd, Batch: batchMsg(1), PlanDigest: p.planDigest})
			return wireErr(t, resp.GetError(), err)
		},
		"Release": func(hd *SessionHeader) *Error {
			resp, err := h.client.Release(ctx, &ReleaseRequest{Header: hd, Batch: batchMsg(1),
				ManifestDigest: p.manifest[:], PlanDigest: p.planDigest})
			return wireErr(t, resp.GetError(), err)
		},
		"RecoveryRelease": func(hd *SessionHeader) *Error {
			resp, err := h.client.RecoveryRelease(ctx, &RecoveryReleaseRequest{Header: hd, Batch: batchMsg(2),
				IngestAck: ackProto(2, p.manifest, as32(p.planDigest), p.layout, testBinding[:]),
				Assertion: outboxProto(2, as32(p.planDigest), testBinding[:])})
			return wireErr(t, resp.GetError(), err)
		},
		"SealGeneration": func(hd *SessionHeader) *Error {
			resp, err := h.client.SealGeneration(ctx, &SealGenerationRequest{Header: hd, BindingGeneration: 1,
				AdmissionGeneration: 1, DrainId: testDrainID, BatchHighwater: 0})
			return wireErr(t, resp.GetError(), err)
		},
		"PrepareSwitch": func(hd *SessionHeader) *Error {
			resp, err := h.client.PrepareSwitch(ctx, &PrepareSwitchRequest{Header: hd, OldSeal: &SealAck{DrainId: testDrainID,
				BindingDigest: testBinding[:], AdmissionGeneration: 1, IssuerStoreEpoch: epoch[:]},
				OldBindingDigest: testBinding[:], DesiredDescriptor: activeSpec.json(), SwitchId: switchID})
			return wireErr(t, resp.GetError(), err)
		},
		"CommitSwitch": func(hd *SessionHeader) *Error {
			resp, err := h.client.CommitSwitch(ctx, &CommitSwitchRequest{Header: hd, SwitchId: switchID})
			return wireErr(t, resp.GetError(), err)
		},
		"Resume": func(hd *SessionHeader) *Error {
			resp, err := h.client.Resume(ctx, &ResumeRequest{Header: hd, OldSeal: &SealAck{DrainId: testDrainID,
				BindingDigest: testBinding[:], AdmissionGeneration: 1, IssuerStoreEpoch: epoch[:]},
				ResumeId:            []byte{5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5},
				ActiveBindingDigest: testBinding[:], NextAdmissionGeneration: 2})
			return wireErr(t, resp.GetError(), err)
		},
	}

	badHeaders := []struct {
		name   string
		change func(h *SessionHeader)
		code   ErrorCode
		action RetryAction
	}{
		{"stale session token", func(h *SessionHeader) { h.SessionToken = make([]byte, 32) }, ErrorCode_SESSION_STALE, RetryAction_RECONNECT},
		{"short session token", func(h *SessionHeader) { h.SessionToken = []byte("short") }, ErrorCode_SESSION_STALE, RetryAction_RECONNECT},
		{"wrong connection generation", func(h *SessionHeader) { h.ConnectionGeneration = 9 }, ErrorCode_GENERATION_CONFLICT, RetryAction_RECONNECT},
		{"wrong admission generation", func(h *SessionHeader) { h.AdmissionGeneration = 9 }, ErrorCode_GENERATION_CONFLICT, RetryAction_RECONNECT},
		{"foreign run", func(h *SessionHeader) { h.RunId = []byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9} }, ErrorCode_IDENTITY_MISMATCH, RetryAction_RECONNECT},
		{"another binding", func(h *SessionHeader) { h.BindingDigest = []byte{1} }, ErrorCode_BINDING_MISMATCH, RetryAction_FIX_CONFIG},
		{"protocol version 1", func(h *SessionHeader) { h.ProtocolVersion = 1 }, ErrorCode_VERSION_UNSUPPORTED, RetryAction_DRAIN_OLD_BUILD},
	}
	for name, call := range rpcs {
		wantCode(t, name+" without header", call(nil), ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
		for _, bad := range badHeaders {
			header := s.header()
			bad.change(header)
			wantCode(t, name+": "+bad.name, call(header), bad.code, bad.action)
		}
	}

	// The barrier of a seal or resume makes the real session stale: every
	// control RPC then answers SESSION_STALE and creates no state.
	h.server.FenceAdmission(2)
	for name, call := range rpcs {
		wantCode(t, name+" after the barrier", call(s.header()), ErrorCode_SESSION_STALE, RetryAction_RECONNECT)
	}
	if prefix, _ := h.j.RetiredPrefix(jid(1)); prefix != 0 {
		t.Fatalf("a fenced RPC advanced the retired prefix to %d", prefix)
	}
}
