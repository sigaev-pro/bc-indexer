// Package ingest is the E03-02.rpc_streaming output: the gRPC control wire
// of the gateway ingest boundary. The .proto file defines the service
// (HelloChallenge, OpenSession, BeginPrepare, PrepareChunk, FinalizePrepare,
// BeginPublish, a bidirectional PublishChunk stream, FinalizePublish and
// SealImport for IMPORT_SEALED recovery, then GetBatchState, Release,
// RecoveryRelease and the SealGeneration, PrepareSwitch, CommitSwitch and
// Resume cutover RPCs); this server implements it on top of
// the gateway journal (E03-01.gateway_store) and the session fencing
// boundary (E03-02.fencing).
//
// Contract held here (CONTROL_WIRE_CONTRACT unless noted):
//
//   - Every mutating RPC validates the session header (protocol version,
//     session token, connection and admission generation, binding digest,
//     producer/run identity) and then runs the journal mutation inside
//     session.Gate.Mutate: the token check happens together with the commit
//     under the gate's serialized ordering, not only when the RPC is read.
//   - Typed errors answer with the registry's code and retry action; the
//     gRPC status is only a transport categorization. A session-stale RPC
//     after a barrier creates no state. A sealed admission generation
//     opens no session, also after a gateway restart.
//   - Bounds: one RPC including its wrapper is at most 256 KiB (the server
//     rejects larger messages at the transport level), one data chunk is at
//     most 64 KiB (LIMIT_INCOMPATIBLE).
//   - IMPORT_SEALED carries the original seed in BeginPrepare; no new
//     sequence is allocated and the original issuer epoch survives.
//   - Offsets, conflicts and the manifest digest are validated before any
//     CLOSED answer.
package ingest

import (
	"context"
	"crypto/rand"
	"io"
	"sync"

	"github.com/sigaev-pro/bc-indexer/gateway/journal"
	"github.com/sigaev-pro/bc-indexer/gateway/session"
	"google.golang.org/grpc"
)

// ProtocolVersion is the pinned control wire version.
const ProtocolVersion = 2

// outstandingChallengeLimit bounds the remembered challenges of one boot.
const outstandingChallengeLimit = 1024

// sessionRecord is one open session of this process.
type sessionRecord struct {
	token         session.Token
	producerID    [16]byte
	runID         [16]byte
	connectionGen uint64
	admissionGen  uint64
}

// Server implements IngestServer. Construct it with New and register it on
// a grpc.Server created with GRPCServerOptions (the 256 KiB message bound).
type Server struct {
	UnimplementedIngestServer

	journal *journal.Journal
	gate    *session.Gate
	// publisher fences the internal publisher worker. Release and
	// SealGeneration raise its barriers; the process wiring hands the same
	// gate to the publisher (PublisherGate).
	publisher *session.PublisherGate

	mu         sync.Mutex
	boot       [16]byte
	challenges map[[16]byte]struct{}
	sessions   map[[32]byte]*sessionRecord
	verifier   TargetVerifier
}

// New creates the server over one gateway journal and one session gate.
func New(j *journal.Journal, g *session.Gate) (*Server, error) {
	var boot [16]byte
	if _, err := rand.Read(boot[:]); err != nil {
		return nil, err
	}
	return &Server{
		journal:    j,
		gate:       g,
		publisher:  session.NewPublisherGate(),
		boot:       boot,
		challenges: make(map[[16]byte]struct{}),
		sessions:   make(map[[32]byte]*sessionRecord),
	}, nil
}

// PublisherGate returns the publisher fence the Release and SealGeneration
// barriers raise. The publisher worker applies its PubAck callbacks through
// the same gate, so a callback of an older generation or of a released batch
// never creates state.
func (s *Server) PublisherGate() *session.PublisherGate {
	return s.publisher
}

// GRPCServerOptions returns the server options every deployment must use:
// the 256 KiB receive bound of one control/chunk RPC.
func GRPCServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{grpc.MaxRecvMsgSize(MaxMessageBytes)}
}

// FenceAdmission raises the admission barrier (SealGeneration/Resume) and
// forgets the sessions of older epochs: their tokens answer SESSION_STALE
// and create no state. Production callers drive this from the maintenance
// cutover RPCs; tests use it directly.
func (s *Server) FenceAdmission(epoch uint64) {
	s.gate.Raise(epoch)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, rec := range s.sessions {
		if rec.token.Epoch < epoch {
			delete(s.sessions, id)
		}
	}
}

// ---------------------------------------------------------------------------
// Handshake and session
// ---------------------------------------------------------------------------

func (s *Server) HelloChallenge(_ context.Context, req *HelloChallengeRequest) (*HelloChallengeResponse, error) {
	var challenge [16]byte
	if _, err := rand.Read(challenge[:]); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.challenges) >= outstandingChallengeLimit {
		s.challenges = make(map[[16]byte]struct{}) // old challenges never survive in bulk
	}
	s.challenges[challenge] = struct{}{}
	return &HelloChallengeResponse{BootNonce: s.boot[:], Challenge: challenge[:]}, nil
}

func (s *Server) OpenSession(_ context.Context, req *OpenSessionRequest) (*OpenSessionResponse, error) {
	resp, we := s.openSession(req)
	if we != nil {
		return &OpenSessionResponse{Error: we.toProto(nil)}, nil
	}
	return resp, nil
}

// openSession runs the challenge echo, binding and generation checks and
// issues one session; it returns the success answer or a typed error.
func (s *Server) openSession(req *OpenSessionRequest) (*OpenSessionResponse, *wireError) {
	if req.GetMode() == OpenSessionRequest_MAINTENANCE {
		// MAINTENANCE accepts no new Prepare/Publish; its recovery flows
		// belong to the E03-03 cutover block.
		return nil, &wireError{ErrorCode_MODE_CONFLICT, RetryAction_RETRY_UNSPECIFIED, "MAINTENANCE sessions are not part of this block"}
	}
	if len(req.GetEchoedChallenge()) != 16 {
		return nil, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RECONNECT, "challenge echo must be 16 bytes"}
	}
	var echo [16]byte
	copy(echo[:], req.GetEchoedChallenge())
	var producerID, runID [16]byte
	if len(req.GetProducerId()) != 16 || len(req.GetRunId()) != 16 {
		return nil, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, "producer and run ids must be 16 bytes"}
	}
	copy(producerID[:], req.GetProducerId())
	copy(runID[:], req.GetRunId())
	if req.GetAdmissionGeneration() == 0 || req.GetConnectionGeneration() == 0 {
		return nil, &wireError{ErrorCode_GENERATION_CONFLICT, RetryAction_RETRY_BACKOFF, "generations start at 1"}
	}
	bnd := s.journal.BindingDigest()
	if len(req.GetActiveBindingDigest()) != 32 || string(req.GetActiveBindingDigest()) != string(bnd[:]) {
		return nil, &wireError{ErrorCode_BINDING_MISMATCH, RetryAction_FIX_CONFIG, "active binding digest does not match the gateway store"}
	}

	s.mu.Lock()
	_, known := s.challenges[echo]
	s.mu.Unlock()
	if !known {
		// An old boot's challenge never works in a new boot.
		return nil, &wireError{ErrorCode_SESSION_STALE, RetryAction_RECONNECT, "unknown challenge for this boot"}
	}

	// A sealed admission generation opens no session. The admission barrier
	// of the gate lives in the process, so after a gateway restart only the
	// durable seal still forbids the data RPCs of the sealed generation. The
	// retry of the seal and its Resume come from the next generation.
	if seal, sealed, err := s.journal.SealedGeneration(journal.BatchID{ProducerID: producerID, RunID: runID}); err != nil {
		return nil, mapJournalError(err)
	} else if sealed && req.GetAdmissionGeneration() <= seal.AdmissionGeneration {
		return nil, &wireError{ErrorCode_GENERATION_CONFLICT, RetryAction_RECONNECT, "admission generation is sealed"}
	}

	tok, err := s.gate.Open(req.GetAdmissionGeneration())
	if err != nil {
		return nil, mapJournalError(err)
	}
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, &wireError{ErrorCode_TRANSIENT_UNAVAILABLE, RetryAction_RETRY_BACKOFF, err.Error()}
	}
	prefix, err := s.journal.RetiredPrefix(journal.BatchID{ProducerID: producerID, RunID: runID})
	if err != nil {
		return nil, mapJournalError(err)
	}
	s.mu.Lock()
	delete(s.challenges, echo) // one challenge opens one session
	s.sessions[tokenBytes] = &sessionRecord{
		token:         tok,
		producerID:    producerID,
		runID:         runID,
		connectionGen: req.GetConnectionGeneration(),
		admissionGen:  req.GetAdmissionGeneration(),
	}
	s.mu.Unlock()

	epoch := s.journal.StoreEpoch()
	return &OpenSessionResponse{
		SessionToken:   tokenBytes[:],
		StoreEpoch:     epoch[:],
		BindingDigest:  bnd[:],
		RetiredPrefix:  prefix,
		RecoveryAction: OpenSessionResponse_NONE,
	}, nil
}

// ---------------------------------------------------------------------------
// Mutating RPC boundary
// ---------------------------------------------------------------------------

// mutate validates the header and runs fn under the gate's serialized
// ordering together with the token check.
func (s *Server) mutate(header *SessionHeader, fn func() error) *wireError {
	rec, we := s.checkHeader(header)
	if we != nil {
		return we
	}
	err := s.gate.Mutate(rec.token, fn)
	return toWireError(err)
}

func (s *Server) checkHeader(header *SessionHeader) (*sessionRecord, *wireError) {
	return s.checkHeaderFor(header, nil)
}

// checkHeaderFor is checkHeader with one more admitted binding digest:
// admitBinding lets the commit of a destination switch answer the session
// that prepared it, which is bound to the old binding of that switch. The
// header binding must then equal the store binding or admitBinding.
func (s *Server) checkHeaderFor(header *SessionHeader, admitBinding *[32]byte) (*sessionRecord, *wireError) {
	if header == nil {
		return nil, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, "missing session header"}
	}
	if header.GetProtocolVersion() != ProtocolVersion {
		return nil, &wireError{ErrorCode_VERSION_UNSUPPORTED, RetryAction_DRAIN_OLD_BUILD, "protocol version must be 2"}
	}
	if len(header.GetSessionToken()) != 32 {
		return nil, &wireError{ErrorCode_SESSION_STALE, RetryAction_RECONNECT, "session token must be 32 bytes"}
	}
	var token [32]byte
	copy(token[:], header.GetSessionToken())
	s.mu.Lock()
	rec, ok := s.sessions[token]
	s.mu.Unlock()
	if !ok {
		return nil, &wireError{ErrorCode_SESSION_STALE, RetryAction_RECONNECT, "unknown session token for this boot"}
	}
	if err := s.gate.Check(rec.token); err != nil {
		return nil, mapJournalError(err)
	}
	if header.GetConnectionGeneration() != rec.connectionGen || header.GetAdmissionGeneration() != rec.admissionGen {
		return nil, &wireError{ErrorCode_GENERATION_CONFLICT, RetryAction_RECONNECT, "header generations do not match the session"}
	}
	if len(header.GetProducerId()) != 16 || len(header.GetRunId()) != 16 {
		return nil, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, "producer and run ids must be 16 bytes"}
	}
	if string(header.GetProducerId()) != string(rec.producerID[:]) || string(header.GetRunId()) != string(rec.runID[:]) {
		return nil, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RECONNECT, "header identity does not match the session"}
	}
	bnd := s.journal.BindingDigest()
	digest := header.GetBindingDigest()
	bindingOK := len(digest) == 32 && (string(digest) == string(bnd[:]) ||
		(admitBinding != nil && string(digest) == string(admitBinding[:])))
	if !bindingOK {
		return nil, &wireError{ErrorCode_BINDING_MISMATCH, RetryAction_FIX_CONFIG, "binding digest does not match the gateway store"}
	}
	return rec, nil
}

func (s *Server) batchID(header *SessionHeader, b *BatchId) (journal.BatchID, *wireError) {
	if b == nil || len(b.GetProducerId()) != 16 || len(b.GetRunId()) != 16 {
		return journal.BatchID{}, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, "batch id must carry 16-byte producer and run ids"}
	}
	var id journal.BatchID
	copy(id.ProducerID[:], b.GetProducerId())
	copy(id.RunID[:], b.GetRunId())
	id.Seq = b.GetBatchSeq()
	if id.Seq == 0 {
		return journal.BatchID{}, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, "batch sequence starts at 1"}
	}
	if string(id.ProducerID[:]) != string(header.GetProducerId()) || string(id.RunID[:]) != string(header.GetRunId()) {
		return journal.BatchID{}, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, "batch identity does not match the session header"}
	}
	return id, nil
}

func hash32(b []byte, what string) ([32]byte, *wireError) {
	var h [32]byte
	if len(b) != 32 {
		return h, &wireError{ErrorCode_INVALID_DESCRIPTOR, RetryAction_RETRY_BACKOFF, what + " must be 32 bytes"}
	}
	copy(h[:], b)
	return h, nil
}

func planDigestOf(seed []byte) []byte {
	d := journal.PlanDigest(seed)
	return d[:]
}

// ---------------------------------------------------------------------------
// Prepare RPCs
// ---------------------------------------------------------------------------

func (s *Server) BeginPrepare(_ context.Context, req *BeginPrepareRequest) (*BeginPrepareResponse, error) {
	resp := &BeginPrepareResponse{}
	we := s.mutate(req.GetHeader(), func() error {
		id, werr := s.batchID(req.GetHeader(), req.GetBatch())
		if werr != nil {
			return werr
		}
		digest, werr := hash32(req.GetManifestDigest(), "manifest digest")
		if werr != nil {
			return werr
		}
		var mode journal.PrepareMode
		switch req.GetMode() {
		case PrepareMode_PREPARE_NORMAL:
			mode = journal.PrepareNormal
		case PrepareMode_PREPARE_IMPORT_SEALED:
			mode = journal.PrepareImportSealed
		default:
			return &wireError{ErrorCode_INVALID_DESCRIPTOR, RetryAction_RETRY_BACKOFF, "unknown prepare mode"}
		}
		resume, err := s.journal.BeginPrepare(id, digest, req.GetEventCount(), req.GetDescriptorBytesTotal(), mode, req.GetImportSeed())
		if err != nil {
			return toWireError(err)
		}
		if resume.Reservation != nil {
			resp.Outcome = &BeginPrepareResponse_Reservation{Reservation: &Reservation{
				Seed:       resume.Reservation.Seed,
				PlanDigest: planDigestOf(resume.Reservation.Seed),
			}}
		} else {
			resp.Outcome = &BeginPrepareResponse_ResumeOffset{ResumeOffset: resume.ResumeOffset}
		}
		return nil
	})
	if we != nil {
		return &BeginPrepareResponse{Error: we.toProto(req.GetHeader())}, nil
	}
	return resp, nil
}

func (s *Server) PrepareChunk(_ context.Context, req *PrepareChunkRequest) (*PrepareChunkResponse, error) {
	resp := &PrepareChunkResponse{}
	we := s.mutate(req.GetHeader(), fnPrepareChunk(s, req, resp))
	if we != nil {
		return &PrepareChunkResponse{NextOffset: resp.GetNextOffset(), Error: we.toProto(req.GetHeader())}, nil
	}
	return resp, nil
}

func fnPrepareChunk(s *Server, req *PrepareChunkRequest, resp *PrepareChunkResponse) func() error {
	return func() error {
		id, werr := s.batchID(req.GetHeader(), req.GetBatch())
		if werr != nil {
			return werr
		}
		digest, werr := hash32(req.GetManifestDigest(), "manifest digest")
		if werr != nil {
			return werr
		}
		sha, werr := hash32(req.GetChunkSha256(), "chunk sha256")
		if werr != nil {
			return werr
		}
		if len(req.GetData()) > MaxDataChunk {
			return &wireError{ErrorCode_LIMIT_INCOMPATIBLE, RetryAction_FIX_CONFIG, "data chunk exceeds 64 KiB"}
		}
		next, err := s.journal.PrepareChunk(id, digest, req.GetOffset(), sha, req.GetData())
		resp.NextOffset = next
		if err != nil {
			return toWireError(err)
		}
		return nil
	}
}

func (s *Server) FinalizePrepare(_ context.Context, req *FinalizePrepareRequest) (*FinalizePrepareResponse, error) {
	resp := &FinalizePrepareResponse{}
	we := s.mutate(req.GetHeader(), func() error {
		id, werr := s.batchID(req.GetHeader(), req.GetBatch())
		if werr != nil {
			return werr
		}
		digest, werr := hash32(req.GetManifestDigest(), "manifest digest")
		if werr != nil {
			return werr
		}
		res, err := s.journal.FinalizePrepare(id, digest)
		if err != nil {
			return toWireError(err)
		}
		resp.Seed = res.Seed
		resp.PlanDigest = planDigestOf(res.Seed)
		return nil
	})
	if we != nil {
		return &FinalizePrepareResponse{Error: we.toProto(req.GetHeader())}, nil
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Publish RPCs
// ---------------------------------------------------------------------------

func (s *Server) BeginPublish(_ context.Context, req *BeginPublishRequest) (*BeginPublishResponse, error) {
	resp := &BeginPublishResponse{}
	we := s.mutate(req.GetHeader(), func() error {
		id, werr := s.batchID(req.GetHeader(), req.GetBatch())
		if werr != nil {
			return werr
		}
		planDigest, werr := hash32(req.GetPlanDigest(), "plan digest")
		if werr != nil {
			return werr
		}
		layoutDigest, werr := hash32(req.GetLayoutManifestDigest(), "layout manifest digest")
		if werr != nil {
			return werr
		}
		resume, err := s.journal.BeginPublish(id, req.GetSeed(), planDigest, layoutDigest, req.GetEntryCount())
		if err != nil {
			return toWireError(err)
		}
		for _, c := range resume.Frames {
			resp.Resume = append(resp.Resume, &FrameCursor{
				Ordinal:       c.Addr.Ordinal,
				FragmentIndex: c.Addr.FragmentIndex,
				FrameIndex:    c.Addr.FrameIndex,
				NextOffset:    c.NextBytes,
			})
		}
		return nil
	})
	if we != nil {
		return &BeginPublishResponse{Error: we.toProto(req.GetHeader())}, nil
	}
	return resp, nil
}

// PublishChunk is the bounded bidirectional frame stream: one response per
// request, in order. A per-chunk typed error answers that message and the
// stream stays open; the client resyncs at the returned cursor.
func (s *Server) PublishChunk(stream Ingest_PublishChunkServer) error {
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		resp := &PublishChunkResponse{}
		we := s.mutate(req.GetHeader(), func() error {
			id, werr := s.batchID(req.GetHeader(), req.GetBatch())
			if werr != nil {
				return werr
			}
			planDigest, werr := hash32(req.GetPlanDigest(), "plan digest")
			if werr != nil {
				return werr
			}
			sha, werr := hash32(req.GetChunkSha256(), "chunk sha256")
			if werr != nil {
				return werr
			}
			if len(req.GetData()) > MaxDataChunk {
				return &wireError{ErrorCode_LIMIT_INCOMPATIBLE, RetryAction_FIX_CONFIG, "data chunk exceeds 64 KiB"}
			}
			addr := journal.FrameAddr{
				Ordinal:       req.GetOrdinal(),
				FragmentIndex: req.GetFragmentIndex(),
				FrameIndex:    req.GetFrameIndex(),
			}
			var meta *journal.FrameMeta
			if m := req.GetMeta(); m != nil {
				meta = &journal.FrameMeta{
					Form:          byte(m.GetForm()),
					Capacity:      m.GetCapacity(),
					PartCount:     m.GetPartCount(),
					FragmentTotal: m.GetFragmentTotalBytes(),
				}
			}
			next, err := s.journal.PublishChunk(id, planDigest, addr, meta, req.GetOffset(), sha, req.GetData())
			resp.NextOffset = next
			if err != nil {
				return toWireError(err)
			}
			return nil
		})
		if we != nil {
			resp.Error = we.toProto(req.GetHeader())
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

func (s *Server) FinalizePublish(_ context.Context, req *FinalizePublishRequest) (*FinalizePublishResponse, error) {
	return s.finalize(req, false)
}

func (s *Server) SealImport(_ context.Context, req *FinalizePublishRequest) (*FinalizePublishResponse, error) {
	return s.finalize(req, true)
}

func (s *Server) finalize(req *FinalizePublishRequest, importMode bool) (*FinalizePublishResponse, error) {
	resp := &FinalizePublishResponse{}
	we := s.mutate(req.GetHeader(), func() error {
		id, werr := s.batchID(req.GetHeader(), req.GetBatch())
		if werr != nil {
			return werr
		}
		planDigest, werr := hash32(req.GetPlanDigest(), "plan digest")
		if werr != nil {
			return werr
		}
		manifestDigest, werr := hash32(req.GetManifestDigest(), "manifest digest")
		if werr != nil {
			return werr
		}
		layoutDigest, werr := hash32(req.GetLayoutManifestDigest(), "layout manifest digest")
		if werr != nil {
			return werr
		}
		var err error
		if importMode {
			err = s.journal.SealImport(id, planDigest, manifestDigest, layoutDigest)
		} else {
			err = s.journal.FinalizePublish(id, planDigest, manifestDigest, layoutDigest)
		}
		if err != nil {
			return toWireError(err)
		}
		resp.State = BatchState_CLOSED
		return nil
	})
	if we != nil {
		return &FinalizePublishResponse{Error: we.toProto(req.GetHeader())}, nil
	}
	return resp, nil
}
