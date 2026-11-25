package ingestclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"sort"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sigaev-pro/bc-indexer/adapters/ingestclient/ingest"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// fakeBatch is what the scripted gateway holds of one batch.
type fakeBatch struct {
	manifest []byte
	digest   proto.Hash32
	events   uint64
	seed     []byte
	imported bool
	state    ingest.BatchState
	layout   proto.Hash32
	// frames maps each frame to the bytes the gateway holds.
	frames map[frameKey][]byte
	// chunks is the number of data chunks stored, largest the size of the
	// largest, declared the number of chunks that carried a declaration.
	chunks, largest, declared int
	// closes and releases count the commits: a repeated call of a closed
	// or retired batch changes nothing.
	closes, releases int
}

// fakeSession is one token the scripted gateway issued.
type fakeSession struct{ connection, admission uint64 }

// fakeGateway is a scripted ingest gateway: the rules of the contract the
// driver depends on, on the stubs of this module. The real ingest.Server is
// exercised by the handshake tests.
type fakeGateway struct {
	ingest.UnimplementedIngestServer
	network proto.NetworkIdentity
	binding proto.Hash32
	epoch   proto.ID16

	mu         sync.Mutex
	sequence   uint64 // the chain sequence the next normal reservation starts at
	reserved   uint64 // reservations allocated
	batches    map[uint64]*fakeBatch
	challenges map[string]bool
	sessions   map[string]*fakeSession
	retired    uint64
	// lose counts, by step, the calls whose reply is lost after the effect.
	lose map[string]int
	// mismatches counts the OFFSET_MISMATCH answers of each data step.
	mismatches map[string]int
	// swallow counts, by data step, the chunks the gateway answers as
	// stored and keeps nothing: the cursor of the next chunk is wrong.
	swallow map[string]int
	// before runs before the effect of a call, with the batch sequence of
	// the request (zero for the handshake calls); it holds no lock.
	before func(step string, seq uint64)
	// onOpen runs while OpenSession is handled, with the connection
	// generation of the request; opens holds those generations.
	onOpen     func(generation uint64)
	opens      []uint64
	modes      []ingest.PrepareMode
	calls      []string
	ackDamage  string
	autoAck    bool
	refuseBind bool
}

func newFakeGateway(network proto.NetworkIdentity, binding proto.Hash32) *fakeGateway {
	return &fakeGateway{network: network, binding: binding, epoch: proto.ID16{0xCD}, sequence: 1,
		batches: make(map[uint64]*fakeBatch), challenges: make(map[string]bool),
		sessions: make(map[string]*fakeSession), lose: make(map[string]int), swallow: make(map[string]int),
		mismatches: make(map[string]int)}
}

// refused answers a typed error of the registry.
func refused(code ingest.ErrorCode, action ingest.RetryAction, detail string, next uint64) *ingest.Error {
	return &ingest.Error{Code: code, RetryAction: action, Detail: detail, OwnerState: "gateway"}
}

// lostReply is the transport error of a call whose reply disappeared.
func lostReply() error { return status.Error(codes.Unavailable, "lost reply") }

// note records the call and runs the hook before the effect of the call.
// The hook runs without the lock, so it may read and change the gateway.
func (g *fakeGateway) note(step string, seq uint64) {
	g.mu.Lock()
	g.calls = append(g.calls, step)
	before := g.before
	g.mu.Unlock()
	if before != nil {
		before(step, seq)
	}
}

// loseReply reports whether the reply of this call is lost. The caller
// holds the lock and the effect of the call already happened.
func (g *fakeGateway) loseReply(step string) error {
	if g.lose[step] > 0 {
		g.lose[step]--
		return lostReply()
	}
	return nil
}

// answer finishes a call whose effect happened: it loses the reply when
// the test asked for it. The caller holds the lock.
func answer[T any](g *fakeGateway, resp *T, step string) (*T, error) {
	if err := g.loseReply(step); err != nil {
		return nil, err
	}
	return resp, nil
}

// Lose makes the next count replies of a step disappear after their effect.
func (g *fakeGateway) Lose(step string, count int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lose[step] += count
}

// Swallow makes the gateway answer the next count chunks of a data step as
// stored and keep nothing.
func (g *fakeGateway) Swallow(step string, count int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.swallow[step] += count
}

// Restart drops every session and challenge, as a gateway boot does.
func (g *fakeGateway) Restart() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sessions = make(map[string]*fakeSession)
	g.challenges = make(map[string]bool)
}

// Calls answers the steps the gateway handled, in order.
func (g *fakeGateway) Calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

// Opens answers the connection generations of the OpenSession calls.
func (g *fakeGateway) Opens() []uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]uint64(nil), g.opens...)
}

// Modes answers the prepare modes of the BeginPrepare calls.
func (g *fakeGateway) Modes() []ingest.PrepareMode {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]ingest.PrepareMode(nil), g.modes...)
}

// Reserved answers the number of reservations the gateway allocated.
func (g *fakeGateway) Reserved() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reserved
}

// Mismatches answers how often the gateway answered OFFSET_MISMATCH at a
// data step.
func (g *fakeGateway) Mismatches(step string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.mismatches[step]
}

// Batch answers a copy of what the gateway holds of one batch, nil for none.
func (g *fakeGateway) Batch(seq uint64) fakeBatch {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.batches[seq] == nil {
		return fakeBatch{}
	}
	return *g.batches[seq]
}

// Holds reports whether the gateway holds a record of the batch.
func (g *fakeGateway) Holds(seq uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.batches[seq] != nil
}

// Frames answers the frames of one batch in (ordinal, fragment, frame) order.
func (g *fakeGateway) Frames(seq uint64) [][]byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.batches[seq] == nil {
		return nil
	}
	keys := make([]frameKey, 0, len(g.batches[seq].frames))
	for key := range g.batches[seq].frames {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.ordinal != b.ordinal {
			return a.ordinal < b.ordinal
		}
		if a.fragment != b.fragment {
			return a.fragment < b.fragment
		}
		return a.frame < b.frame
	})
	out := make([][]byte, 0, len(keys))
	for _, key := range keys {
		out = append(out, g.batches[seq].frames[key])
	}
	return out
}

// Acknowledge marks a closed batch acknowledged, as the publisher does.
func (g *fakeGateway) Acknowledge(seq uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.batches[seq].state = ingest.BatchState_NATS_ACKED
}

// RetireEarly marks a batch retired at the gateway, behind the producer's back.
func (g *fakeGateway) RetireEarly(seq uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.batches[seq] == nil {
		g.batches[seq] = &fakeBatch{frames: make(map[frameKey][]byte)}
	}
	g.batches[seq].state = ingest.BatchState_RETIRED
	g.retired = seq
}

// Forget drops the record of a batch: the gateway store lost it.
func (g *fakeGateway) Forget(seq uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.batches, seq)
}

// DamageAck makes the acknowledgements carry a wrong field.
func (g *fakeGateway) DamageAck(field string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ackDamage = field
}

// admit checks the session header of a mutating request as the gateway
// does. The caller holds the lock.
func (g *fakeGateway) admit(header *ingest.SessionHeader) *ingest.Error {
	sess, ok := g.sessions[string(header.GetSessionToken())]
	switch {
	case header.GetProtocolVersion() != ProtocolVersion:
		return refused(ingest.ErrorCode_VERSION_UNSUPPORTED, ingest.RetryAction_DRAIN_OLD_BUILD, "protocol version must be 2", 0)
	case len(header.GetRequestId()) != 16:
		return refused(ingest.ErrorCode_IDENTITY_MISMATCH, ingest.RetryAction_RETRY_BACKOFF, "request id must be 16 bytes", 0)
	case !ok:
		return refused(ingest.ErrorCode_SESSION_STALE, ingest.RetryAction_RECONNECT, "unknown session token for this boot", 0)
	case header.GetConnectionGeneration() != sess.connection || header.GetAdmissionGeneration() != sess.admission:
		return refused(ingest.ErrorCode_GENERATION_CONFLICT, ingest.RetryAction_RECONNECT, "header generations do not match the session", 0)
	case string(header.GetBindingDigest()) != string(g.binding[:]):
		return refused(ingest.ErrorCode_BINDING_MISMATCH, ingest.RetryAction_FIX_CONFIG, "binding digest does not match the gateway store", 0)
	}
	return nil
}

func (g *fakeGateway) HelloChallenge(_ context.Context, req *ingest.HelloChallengeRequest) (*ingest.HelloChallengeResponse, error) {
	g.note("HelloChallenge", 0)
	g.mu.Lock()
	defer g.mu.Unlock()
	challenge := make([]byte, 16)
	_, _ = rand.Read(challenge)
	g.challenges[string(challenge)] = true
	network, _ := g.network.Digest()
	if req.GetRole() != Role || string(req.GetNetworkDigest()) != string(network[:]) ||
		len(req.GetProducerId()) != 16 || len(req.GetRunId()) != 16 {
		return answer(g, &ingest.HelloChallengeResponse{Error: refused(ingest.ErrorCode_IDENTITY_MISMATCH,
			ingest.RetryAction_RETRY_BACKOFF, "hello carries another role, network or identity", 0)}, "HelloChallenge")
	}
	return answer(g, &ingest.HelloChallengeResponse{BootNonce: make([]byte, 16), Challenge: challenge}, "HelloChallenge")
}

func (g *fakeGateway) OpenSession(_ context.Context, req *ingest.OpenSessionRequest) (*ingest.OpenSessionResponse, error) {
	g.note("OpenSession", 0)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.opens = append(g.opens, req.GetConnectionGeneration())
	if g.onOpen != nil {
		saved := g.onOpen
		g.mu.Unlock()
		saved(req.GetConnectionGeneration())
		g.mu.Lock()
	}
	if !g.challenges[string(req.GetEchoedChallenge())] {
		return answer(g, &ingest.OpenSessionResponse{Error: refused(ingest.ErrorCode_SESSION_STALE,
			ingest.RetryAction_RECONNECT, "unknown challenge for this boot", 0)}, "OpenSession")
	}
	delete(g.challenges, string(req.GetEchoedChallenge()))
	if g.refuseBind || req.GetMode() != ingest.OpenSessionRequest_NORMAL ||
		string(req.GetActiveBindingDigest()) != string(g.binding[:]) ||
		string(req.GetDesiredBindingDigest()) != string(g.binding[:]) {
		return answer(g, &ingest.OpenSessionResponse{Error: refused(ingest.ErrorCode_BINDING_MISMATCH,
			ingest.RetryAction_FIX_CONFIG, "active binding digest does not match the gateway store", 0)}, "OpenSession")
	}
	token := make([]byte, 32)
	_, _ = rand.Read(token)
	g.sessions[string(token)] = &fakeSession{connection: req.GetConnectionGeneration(), admission: req.GetAdmissionGeneration()}
	return answer(g, &ingest.OpenSessionResponse{SessionToken: token, StoreEpoch: g.epoch[:], BindingDigest: g.binding[:],
		RetiredPrefix: g.retired, RecoveryAction: ingest.OpenSessionResponse_NONE}, "OpenSession")
}

func (g *fakeGateway) BeginPrepare(_ context.Context, req *ingest.BeginPrepareRequest) (*ingest.BeginPrepareResponse, error) {
	seq := req.GetBatch().GetBatchSeq()
	g.note("BeginPrepare", seq)
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.admit(req.GetHeader()); e != nil {
		return answer(g, &ingest.BeginPrepareResponse{Error: e}, "BeginPrepare")
	}
	g.modes = append(g.modes, req.GetMode())
	if seq <= g.retired {
		return answer(g, &ingest.BeginPrepareResponse{Error: refused(ingest.ErrorCode_ALREADY_RETIRED,
			ingest.RetryAction_RETRY_UNSPECIFIED, "batch already retired", 0)}, "BeginPrepare")
	}
	imported := req.GetMode() == ingest.PrepareMode_PREPARE_IMPORT_SEALED
	if b := g.batches[seq]; b != nil {
		if string(req.GetManifestDigest()) != string(b.digest[:]) {
			return answer(g, &ingest.BeginPrepareResponse{Error: refused(ingest.ErrorCode_IMMUTABLE_CONFLICT,
				ingest.RetryAction_DIAGNOSE_IMMUTABLE, "another manifest digest", 0)}, "BeginPrepare")
		}
		if b.seed != nil {
			if b.imported != imported {
				return answer(g, &ingest.BeginPrepareResponse{Error: refused(ingest.ErrorCode_MODE_CONFLICT,
					ingest.RetryAction_RETRY_UNSPECIFIED, "prepare mode of another kind", 0)}, "BeginPrepare")
			}
			// A prepared batch answers its saved reservation.
			return answer(g, &ingest.BeginPrepareResponse{Outcome: &ingest.BeginPrepareResponse_Reservation{
				Reservation: &ingest.Reservation{Seed: b.seed, PlanDigest: digestOf(b.seed)}}}, "BeginPrepare")
		}
		// An open manifest stream resumes at its cursor.
		return answer(g, &ingest.BeginPrepareResponse{Outcome: &ingest.BeginPrepareResponse_ResumeOffset{
			ResumeOffset: uint64(len(b.manifest))}}, "BeginPrepare")
	}
	b := &fakeBatch{digest: hash32Of(req.GetManifestDigest()), events: req.GetEventCount(),
		frames: make(map[frameKey][]byte), state: ingest.BatchState_PREPARING}
	if imported {
		seed, err := ParseSeed(req.GetImportSeed())
		if err != nil || seed.BatchSeq != seq || seed.ManifestDigest != b.digest ||
			seed.EventCount != req.GetEventCount() || seed.BindingDigest != g.binding {
			return answer(g, &ingest.BeginPrepareResponse{Error: refused(ingest.ErrorCode_IDENTITY_MISMATCH,
				ingest.RetryAction_RETRY_BACKOFF, "invalid import seed", 0)}, "BeginPrepare")
		}
		b.seed, b.imported = append([]byte(nil), req.GetImportSeed()...), true
	}
	g.batches[seq] = b
	return answer(g, &ingest.BeginPrepareResponse{}, "BeginPrepare")
}

func (g *fakeGateway) PrepareChunk(_ context.Context, req *ingest.PrepareChunkRequest) (*ingest.PrepareChunkResponse, error) {
	seq := req.GetBatch().GetBatchSeq()
	g.note("PrepareChunk", seq)
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.admit(req.GetHeader()); e != nil {
		return answer(g, &ingest.PrepareChunkResponse{Error: e}, "PrepareChunk")
	}
	b := g.batches[seq]
	cover := uint64(len(b.manifest))
	switch {
	case req.GetOffset() > cover:
		// The expected cursor travels with the answer.
		g.mismatches["PrepareChunk"]++
		return answer(g, &ingest.PrepareChunkResponse{NextOffset: cover, Error: refused(ingest.ErrorCode_OFFSET_MISMATCH,
			ingest.RetryAction_RESUME_AT_OFFSET, "chunk offset mismatch", cover)}, "PrepareChunk")
	case req.GetOffset() < cover:
		// A stored retry answers the cursor, as the journal does.
		return answer(g, &ingest.PrepareChunkResponse{NextOffset: cover}, "PrepareChunk")
	case g.swallow["PrepareChunk"] > 0:
		g.swallow["PrepareChunk"]--
		return answer(g, &ingest.PrepareChunkResponse{NextOffset: cover + uint64(len(req.GetData()))}, "PrepareChunk")
	}
	b.manifest = append(b.manifest, req.GetData()...)
	return answer(g, &ingest.PrepareChunkResponse{NextOffset: uint64(len(b.manifest))}, "PrepareChunk")
}

func (g *fakeGateway) FinalizePrepare(_ context.Context, req *ingest.FinalizePrepareRequest) (*ingest.FinalizePrepareResponse, error) {
	seq := req.GetBatch().GetBatchSeq()
	g.note("FinalizePrepare", seq)
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.admit(req.GetHeader()); e != nil {
		return answer(g, &ingest.FinalizePrepareResponse{Error: e}, "FinalizePrepare")
	}
	b := g.batches[seq]
	if b == nil || proto.Hash32(sha256.Sum256(b.manifest)) != b.digest {
		return answer(g, &ingest.FinalizePrepareResponse{Error: refused(ingest.ErrorCode_INVALID_DESCRIPTOR,
			ingest.RetryAction_RETRY_BACKOFF, "manifest digest mismatch", 0)}, "FinalizePrepare")
	}
	if b.seed == nil {
		// One reservation per batch; the sequence range follows the last.
		reservation := make([]byte, 16)
		_, _ = rand.Read(reservation)
		b.seed = contractSeed{reservation: [16]byte(reservation), epoch: g.epoch,
			producer: [16]byte(req.GetHeader().GetProducerId()[:16]), run: [16]byte(req.GetHeader().GetRunId()[:16]),
			seq: seq, manifest: b.digest, events: b.events, sequenceBase: g.sequence,
			binding: g.binding, frameLimit: 2097152, versions: pinned}.bytes()
		g.sequence += b.events
		g.reserved++
	}
	b.state = ingest.BatchState_PREPARED
	return answer(g, &ingest.FinalizePrepareResponse{Seed: b.seed, PlanDigest: digestOf(b.seed)}, "FinalizePrepare")
}

func (g *fakeGateway) BeginPublish(_ context.Context, req *ingest.BeginPublishRequest) (*ingest.BeginPublishResponse, error) {
	seq := req.GetBatch().GetBatchSeq()
	g.note("BeginPublish", seq)
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.admit(req.GetHeader()); e != nil {
		return answer(g, &ingest.BeginPublishResponse{Error: e}, "BeginPublish")
	}
	if seq <= g.retired {
		return answer(g, &ingest.BeginPublishResponse{Error: refused(ingest.ErrorCode_ALREADY_RETIRED,
			ingest.RetryAction_RETRY_UNSPECIFIED, "batch already retired", 0)}, "BeginPublish")
	}
	b := g.batches[seq]
	if b == nil || b.seed == nil || !bytes.Equal(b.seed, req.GetSeed()) {
		return answer(g, &ingest.BeginPublishResponse{Error: refused(ingest.ErrorCode_IDENTITY_MISMATCH,
			ingest.RetryAction_RETRY_BACKOFF, "batch not found or another seed", 0)}, "BeginPublish")
	}
	if b.state == ingest.BatchState_PREPARED {
		b.state, b.layout = ingest.BatchState_RECEIVING, hash32Of(req.GetLayoutManifestDigest())
	}
	// A retry answers the cursor of every frame the gateway holds.
	var resume []*ingest.FrameCursor
	for key, held := range b.frames {
		resume = append(resume, &ingest.FrameCursor{Ordinal: key.ordinal, FragmentIndex: key.fragment,
			FrameIndex: key.frame, NextOffset: uint64(len(held))})
	}
	return answer(g, &ingest.BeginPublishResponse{Resume: resume}, "BeginPublish")
}

func (g *fakeGateway) PublishChunk(stream ingest.Ingest_PublishChunkServer) error {
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		seq := req.GetBatch().GetBatchSeq()
		g.note("PublishChunk", seq)
		g.mu.Lock()
		resp := &ingest.PublishChunkResponse{}
		switch e := g.admit(req.GetHeader()); {
		case e != nil:
			resp.Error = e
		default:
			b := g.batches[seq]
			if b == nil || b.state != ingest.BatchState_RECEIVING {
				resp.Error = refused(ingest.ErrorCode_MODE_CONFLICT, ingest.RetryAction_RETRY_BACKOFF, "not receiving", 0)
				break
			}
			key := frameKey{req.GetOrdinal(), req.GetFragmentIndex(), req.GetFrameIndex()}
			held := b.frames[key]
			cover := uint64(len(held))
			switch {
			case req.GetOffset() > cover:
				g.mismatches["PublishChunk"]++
				resp.NextOffset = cover
				resp.Error = refused(ingest.ErrorCode_OFFSET_MISMATCH, ingest.RetryAction_RESUME_AT_OFFSET, "chunk offset mismatch", cover)
			case req.GetOffset() < cover:
				resp.NextOffset = cover // a stored retry answers the cursor of the frame
			case g.swallow["PublishChunk"] > 0 && len(req.GetData()) == MaxDataChunk:
				// Only a full piece is swallowed: a longer frame follows it.
				g.swallow["PublishChunk"]--
				resp.NextOffset = cover + uint64(len(req.GetData()))
			default:
				b.frames[key] = append(append([]byte(nil), held...), req.GetData()...)
				b.chunks++
				if req.GetMeta() != nil {
					b.declared++
				}
				b.largest = max(b.largest, len(req.GetData()))
				resp.NextOffset = uint64(len(b.frames[key]))
			}
		}
		lost := g.loseReply("PublishChunk")
		g.mu.Unlock()
		if lost != nil {
			return lost
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

func (g *fakeGateway) FinalizePublish(_ context.Context, req *ingest.FinalizePublishRequest) (*ingest.FinalizePublishResponse, error) {
	return g.close(req, false, "FinalizePublish")
}

func (g *fakeGateway) SealImport(_ context.Context, req *ingest.FinalizePublishRequest) (*ingest.FinalizePublishResponse, error) {
	return g.close(req, true, "SealImport")
}

// close is the shared body of the two closing calls. A retry of a closed
// batch answers CLOSED again.
func (g *fakeGateway) close(req *ingest.FinalizePublishRequest, imported bool, step string) (*ingest.FinalizePublishResponse, error) {
	seq := req.GetBatch().GetBatchSeq()
	g.note(step, seq)
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.admit(req.GetHeader()); e != nil {
		return answer(g, &ingest.FinalizePublishResponse{Error: e}, step)
	}
	b := g.batches[seq]
	closed := b != nil && (b.state == ingest.BatchState_RECEIVING || b.state == ingest.BatchState_CLOSED ||
		b.state == ingest.BatchState_NATS_ACKED)
	if !closed || b.imported != imported || !bytes.Equal(req.GetPlanDigest(), digestOf(b.seed)) {
		return answer(g, &ingest.FinalizePublishResponse{Error: refused(ingest.ErrorCode_MODE_CONFLICT,
			ingest.RetryAction_RETRY_UNSPECIFIED, "wrong closing mode or state", 0)}, step)
	}
	if b.state == ingest.BatchState_RECEIVING {
		b.closes++
		b.state = ingest.BatchState_CLOSED
		if g.autoAck {
			b.state = ingest.BatchState_NATS_ACKED
		}
	}
	return answer(g, &ingest.FinalizePublishResponse{State: ingest.BatchState_CLOSED}, step)
}

func (g *fakeGateway) GetBatchState(_ context.Context, req *ingest.GetBatchStateRequest) (*ingest.GetBatchStateResponse, error) {
	seq := req.GetBatch().GetBatchSeq()
	g.note("GetBatchState", seq)
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.admit(req.GetHeader()); e != nil {
		return answer(g, &ingest.GetBatchStateResponse{Error: e}, "GetBatchState")
	}
	resp := &ingest.GetBatchStateResponse{}
	b := g.batches[seq]
	switch {
	case seq <= g.retired:
		resp.State = ingest.BatchState_RETIRED
	case b == nil:
		resp.State = ingest.BatchState_NOT_FOUND
	default:
		resp.State = b.state
		if b.state == ingest.BatchState_NATS_ACKED {
			resp.Ack = g.ack(b, req.GetBatch())
		}
	}
	return answer(g, resp, "GetBatchState")
}

// ack builds the IngestAck of a batch, with the field the test damaged.
func (g *fakeGateway) ack(b *fakeBatch, batch *ingest.BatchId) *ingest.IngestAck {
	if g.ackDamage == "missing" {
		return nil
	}
	ack := &ingest.IngestAck{Batch: batch, ManifestDigest: b.digest[:], PlanDigest: digestOf(b.seed),
		LayoutManifestDigest: b.layout[:], BindingDigest: g.binding[:], IssuerStoreEpoch: g.epoch[:]}
	zero := func(b []byte) []byte { return make([]byte, len(b)) }
	switch g.ackDamage {
	case "batch":
		ack.Batch = &ingest.BatchId{ProducerId: batch.GetProducerId(), RunId: batch.GetRunId(), BatchSeq: batch.GetBatchSeq() + 1}
	case "run":
		ack.Batch = &ingest.BatchId{ProducerId: batch.GetProducerId(), RunId: zero(batch.GetRunId()), BatchSeq: batch.GetBatchSeq()}
	case "manifest":
		ack.ManifestDigest = zero(ack.ManifestDigest)
	case "plan":
		ack.PlanDigest = zero(ack.PlanDigest)
	case "layout":
		ack.LayoutManifestDigest = zero(ack.LayoutManifestDigest)
	case "binding":
		ack.BindingDigest = zero(ack.BindingDigest)
	case "epoch":
		ack.IssuerStoreEpoch = zero(ack.IssuerStoreEpoch)
	}
	return ack
}

func (g *fakeGateway) Release(_ context.Context, req *ingest.ReleaseRequest) (*ingest.ReleaseResponse, error) {
	seq := req.GetBatch().GetBatchSeq()
	g.note("Release", seq)
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.admit(req.GetHeader()); e != nil {
		return answer(g, &ingest.ReleaseResponse{Error: e}, "Release")
	}
	// Only an acknowledged batch releases; a repeated Release of a retired
	// sequence answers the prefix again.
	b := g.batches[seq]
	if b == nil || b.state != ingest.BatchState_NATS_ACKED && seq > g.retired ||
		!bytes.Equal(req.GetPlanDigest(), digestOf(b.seed)) || !bytes.Equal(req.GetManifestDigest(), b.digest[:]) {
		return answer(g, &ingest.ReleaseResponse{Error: refused(ingest.ErrorCode_MODE_CONFLICT,
			ingest.RetryAction_RETRY_BACKOFF, "release of a batch that is not acknowledged", 0)}, "Release")
	}
	if b.state == ingest.BatchState_NATS_ACKED {
		b.releases++
		b.state = ingest.BatchState_RETIRED
		g.retired = seq
	}
	return answer(g, &ingest.ReleaseResponse{Ack: &ingest.ReleaseAck{Batch: req.GetBatch(), RetiredPrefix: seq}}, "Release")
}

// count answers how often a step was called.
func count(calls []string, step string) int {
	n := 0
	for _, call := range calls {
		if call == step {
			n++
		}
	}
	return n
}
