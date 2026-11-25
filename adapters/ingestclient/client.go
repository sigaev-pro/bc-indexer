package ingestclient

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"

	"github.com/sigaev-pro/bc-indexer/adapters/ingestclient/ingest"
	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// dialGateway opens the connection to the gateway: TCP with the mutual TLS
// credentials of the configuration. The connection is lazy, gRPC connects
// at the first call and reconnects on its own; the connect backoff is capped
// so that a gateway that comes back is reached within one cap.
//
// DECISION (contract reference: CONTROL_WIRE_CONTRACT, "Errors": TRANSIENT
// is a backoff without an attempt limit): the client sends no keepalive
// ping. The gateway of the tracer bullet listens on one Compose network,
// where a stopped peer closes or refuses the connection at once; a call
// that gets no answer ends at callTimeout and is repeated. A client ping
// without a stream needs a server that admits it, which the gateway does
// not configure.
var dialGateway = func(_ context.Context, cfg Config) (*grpc.ClientConn, error) {
	creds, err := transportCredentials(cfg.Endpoint, cfg.TLS)
	if err != nil {
		return nil, err
	}
	return grpc.NewClient(cfg.Endpoint.Address(),
		grpc.WithTransportCredentials(creds),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.Config{BaseDelay: time.Second, Multiplier: 1.6, Jitter: 0.2, MaxDelay: backoffMaxDelay},
			MinConnectTimeout: 5 * time.Second,
		}),
	)
}

// client is one connection to the gateway with at most one session. The
// driver goroutine alone uses it.
type client struct {
	cfg  Config
	stub ingest.IngestClient
	conn *grpc.ClientConn
	// session is nil until the handshake opened one, and again after every
	// RECONNECT.
	session *session
	bo      retryBackoff
	// st holds the connection generation; id, admission and retiredPrefix
	// read the producer journal.
	// st holds the connection generation.
	st            storage.Storage
	id            producer.Identity
	admission     func() uint64
	retiredPrefix func() uint64
	// dial opens the connection; the tests of this package substitute it.
	dial func(context.Context, Config) (*grpc.ClientConn, error)
}

// newClient prepares a client and performs no I/O.
func newClient(cfg Config, st storage.Storage, store *producer.Store) *client {
	return &client{cfg: cfg, st: st, id: store.Identity(), dial: dialGateway,
		admission:     func() uint64 { return store.ModeInfo().AdmissionGeneration },
		retiredPrefix: func() uint64 { return store.RetiredPrefix() }}
}

// connect dials the gateway.
func (c *client) connect(ctx context.Context) error {
	conn, err := c.dial(ctx, c.cfg)
	if err != nil {
		return err
	}
	c.conn = conn
	c.stub = ingest.NewIngestClient(conn)
	return nil
}

// close releases the connection.
func (c *client) close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

// header builds the session header of one request.
func (c *client) header(requestID []byte) *ingest.SessionHeader {
	return c.session.header(requestID, c.id, c.cfg.Binding.DescriptorDigest)
}

// call is the body of one RPC. It answers nil when the gateway accepted the
// call, a *WireError when the gateway refused it, or the transport failure.
type call func(ctx context.Context) error

// runCall repeats one call until it is accepted, the run ends or an answer
// stops it. Each pass makes sure a session is open, bounds the call by
// callTimeout and acts on the failure the way its retry rule says: a
// transport failure and RETRY_BACKOFF back off and repeat the same call
// with the same request id; RECONNECT drops the session, and the next pass
// opens a new one under the next connection generation; RESUME_AT_OFFSET
// and the stopping answers are returned to the caller.
func (c *client) runCall(ctx context.Context, step string, fn call) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.ensureSession(ctx); err != nil {
			return err
		}
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		err := fn(callCtx)
		cancel()
		if err == nil {
			c.bo.reset()
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch classify(err) {
		case actionRetry:
			if c.bo.sleep(ctx) {
				return ctx.Err()
			}
		case actionReconnect:
			c.session = nil
		default:
			return err
		}
	}
}

// ensureSession opens a session when the client holds none. The connection
// generation is incremented and saved before the calls that open the
// session, so a session is never opened under a saved value twice.
func (c *client) ensureSession(ctx context.Context) error {
	if c.session != nil {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		generation, err := saveConnectionGeneration(c.st)
		if err != nil {
			return err
		}
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		opened, failure := c.handshake(callCtx, c.id, c.retiredPrefix(), generation)
		cancel()
		if failure == nil {
			c.session = opened
			c.bo.reset()
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch classify(failure) {
		case actionRetry, actionReconnect:
			// A lost reply, a gateway that is down or one that restarted
			// between the two calls: the next pass opens a session under
			// the next connection generation.
			if c.bo.sleep(ctx) {
				return ctx.Err()
			}
		default:
			return failure
		}
	}
}

// typedError answers the typed error of one refused call, nil when the
// gateway accepted it.
func typedError(step string, e *ingest.Error) error {
	if e == nil {
		return nil
	}
	return wireError(step, e, 0)
}

// batchState asks the durable state of one batch (step 10, and the probe
// before the steps that depend on what the gateway holds). The call carries
// no cursor: the driver reads the state and the acknowledgement, not the
// frame cursors.
func (c *client) batchState(ctx context.Context, requestID []byte, batch *ingest.BatchId, planDigest proto.Hash32) (*ingest.GetBatchStateResponse, error) {
	var resp *ingest.GetBatchStateResponse
	err := c.runCall(ctx, "GetBatchState", func(ctx context.Context) error {
		var failure error
		resp, failure = c.stub.GetBatchState(ctx, &ingest.GetBatchStateRequest{
			Header: c.header(requestID), Batch: batch, PlanDigest: planDigest[:]})
		if failure != nil {
			return failure
		}
		return typedError("GetBatchState", resp.GetError())
	})
	return resp, err
}

// beginPrepare opens or resumes the preparation of one batch (step 3). The
// answer is the offset the manifest stream resumes at, or the reservation
// the gateway already committed.
func (c *client) beginPrepare(ctx context.Context, requestID []byte, req *ingest.BeginPrepareRequest) (*ingest.BeginPrepareResponse, error) {
	var resp *ingest.BeginPrepareResponse
	err := c.runCall(ctx, "BeginPrepare", func(ctx context.Context) error {
		req.Header = c.header(requestID)
		var failure error
		resp, failure = c.stub.BeginPrepare(ctx, req)
		if failure != nil {
			return failure
		}
		return typedError("BeginPrepare", resp.GetError())
	})
	return resp, err
}

// streamManifest sends the manifest chunks of one batch from resumeOffset
// (step 4): pieces of at most MaxDataChunk bytes at increasing offsets.
// OFFSET_MISMATCH moves the cursor to the one the answer carries and the
// stream continues there; a cursor that does not advance is a malformed
// answer, never a loop.
func (c *client) streamManifest(ctx context.Context, requestID []byte, batch *ingest.BatchId,
	manifestDigest proto.Hash32, manifest []byte, resumeOffset uint64) error {
	if resumeOffset > uint64(len(manifest)) {
		return &WireError{Step: "BeginPrepare", Code: ingest.ErrorCode_OFFSET_MISMATCH,
			Action: ingest.RetryAction_RESUME_AT_OFFSET, Detail: "resume offset beyond the manifest", NextOffset: resumeOffset}
	}
	offset := resumeOffset
	for offset < uint64(len(manifest)) {
		end := min(offset+MaxDataChunk, uint64(len(manifest)))
		data := manifest[offset:end]
		var resp *ingest.PrepareChunkResponse
		sent := offset
		err := c.runCall(ctx, "PrepareChunk", func(ctx context.Context) error {
			var failure error
			resp, failure = c.stub.PrepareChunk(ctx, &ingest.PrepareChunkRequest{
				Header: c.header(requestID), Batch: batch, ManifestDigest: manifestDigest[:],
				Offset: sent, ChunkSha256: sha256Sum(data), Data: data})
			if failure != nil {
				return failure
			}
			return typedError("PrepareChunk", resp.GetError())
		})
		if err != nil {
			var wire *WireError
			if !errors.As(err, &wire) || wire.Action != ingest.RetryAction_RESUME_AT_OFFSET {
				return err
			}
			// OFFSET_MISMATCH: continue at the cursor the answer carries.
		}
		next := resp.GetNextOffset()
		if next == sent || next > uint64(len(manifest)) {
			return fmtError(ErrProtocol, "PrepareChunk: next offset %d after a chunk at %d of a %d-byte manifest",
				next, sent, len(manifest))
		}
		offset = next
	}
	return nil
}

// finalizePrepare closes the manifest stream at the gateway and answers the
// reservation (step 5).
func (c *client) finalizePrepare(ctx context.Context, requestID []byte, batch *ingest.BatchId,
	manifestDigest proto.Hash32) (*ingest.FinalizePrepareResponse, error) {
	var resp *ingest.FinalizePrepareResponse
	err := c.runCall(ctx, "FinalizePrepare", func(ctx context.Context) error {
		var failure error
		resp, failure = c.stub.FinalizePrepare(ctx, &ingest.FinalizePrepareRequest{
			Header: c.header(requestID), Batch: batch, ManifestDigest: manifestDigest[:]})
		if failure != nil {
			return failure
		}
		return typedError("FinalizePrepare", resp.GetError())
	})
	return resp, err
}

// publishRequest carries what the publish stream of one batch needs.
type publishRequest struct {
	batch        *ingest.BatchId
	seed         []byte
	planDigest   proto.Hash32
	layoutDigest proto.Hash32
	entryCount   uint64
	frames       []planFrame
}

// frameKey addresses one frame of the wire.
type frameKey struct{ ordinal, fragment, frame uint64 }

// publishFrames sends the frames of one sealed plan (steps 7 and 8). Each
// pass asks the gateway for its resume cursors, so a pass that was cut off
// continues where the gateway is and sends nothing twice: a frame whose
// cursor equals its length is skipped. A TransportFrame may exceed
// MaxDataChunk, so a frame goes in pieces of at most MaxDataChunk bytes at
// increasing offsets; the first piece of a frame carries its declaration.
func (c *client) publishFrames(ctx context.Context, requestID []byte, req publishRequest) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		cursors, err := c.resumeCursors(ctx, requestID, req)
		if err != nil {
			return err
		}
		failure := c.sendFrames(ctx, requestID, req, cursors)
		if failure == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch classify(failure) {
		case actionRetry:
			if c.bo.sleep(ctx) {
				return ctx.Err()
			}
		case actionReconnect:
			c.session = nil
		case actionResume:
			// The cursors of the next pass say where the gateway is.
		default:
			return failure
		}
	}
}

// resumeCursors runs BeginPublish (step 7) and answers the next byte offset
// of every frame of the plan as the gateway holds it.
func (c *client) resumeCursors(ctx context.Context, requestID []byte, req publishRequest) (map[frameKey]uint64, error) {
	var resp *ingest.BeginPublishResponse
	err := c.runCall(ctx, "BeginPublish", func(ctx context.Context) error {
		var failure error
		resp, failure = c.stub.BeginPublish(ctx, &ingest.BeginPublishRequest{
			Header: c.header(requestID), Batch: req.batch, Seed: req.seed,
			PlanDigest: req.planDigest[:], LayoutManifestDigest: req.layoutDigest[:], EntryCount: req.entryCount})
		if failure != nil {
			return failure
		}
		return typedError("BeginPublish", resp.GetError())
	})
	if err != nil {
		return nil, err
	}
	known := make(map[frameKey]bool, len(req.frames))
	for i := range req.frames {
		ordinal, fragment, frame := req.frames[i].address()
		known[frameKey{ordinal, fragment, frame}] = true
	}
	cursors := make(map[frameKey]uint64, len(req.frames))
	for _, cursor := range resp.GetResume() {
		key := frameKey{cursor.GetOrdinal(), cursor.GetFragmentIndex(), cursor.GetFrameIndex()}
		if !known[key] {
			return nil, fmtError(ErrProtocol, "BeginPublish: cursor for frame %v, which the plan does not hold", key)
		}
		cursors[key] = cursor.GetNextOffset()
	}
	return cursors, nil
}

// sendFrames runs one pass of the publish stream: every frame from its
// cursor, one request and one response at a time, then the close of the
// stream. OFFSET_MISMATCH is returned to the caller, which asks the gateway
// for its cursors again.
func (c *client) sendFrames(ctx context.Context, requestID []byte, req publishRequest, cursors map[frameKey]uint64) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.stub.PublishChunk(streamCtx)
	if err != nil {
		return err
	}
	for i := range req.frames {
		frame := &req.frames[i]
		ordinal, fragment, index := frame.address()
		offset := cursors[frameKey{ordinal, fragment, index}]
		if offset > uint64(len(frame.Encoded)) {
			return fmtError(ErrProtocol, "BeginPublish: cursor %d for a frame of %d bytes", offset, len(frame.Encoded))
		}
		for offset < uint64(len(frame.Encoded)) {
			sent := offset
			end := min(offset+MaxDataChunk, uint64(len(frame.Encoded)))
			data := frame.Encoded[offset:end]
			request := &ingest.PublishChunkRequest{
				Header: c.header(requestID), Batch: req.batch, PlanDigest: req.planDigest[:],
				Ordinal: ordinal, FragmentIndex: fragment, FrameIndex: index,
				Offset: sent, ChunkSha256: sha256Sum(data), Data: data}
			if sent == 0 {
				request.Meta = frame.meta() // the first chunk of a frame declares it
			}
			if err := stream.Send(request); err != nil {
				if errors.Is(err, io.EOF) {
					_, err = stream.Recv()
				}
				return err
			}
			resp, err := recvWithin(stream)
			if err != nil {
				return err
			}
			if e := resp.GetError(); e != nil {
				return wireError("PublishChunk", e, resp.GetNextOffset())
			}
			next := resp.GetNextOffset()
			if next == sent || next > uint64(len(frame.Encoded)) {
				return fmtError(ErrProtocol, "PublishChunk: next offset %d after a chunk at %d of a %d-byte frame",
					next, sent, len(frame.Encoded))
			}
			offset = next
		}
	}
	if err := stream.CloseSend(); err != nil {
		return err
	}
	if _, err := stream.Recv(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// recvWithin receives one answer of the publish stream and bounds the wait
// by callTimeout: a stream receive carries no deadline of its own. On a
// timeout the caller cancels the stream, which ends the pending receive.
func recvWithin(stream ingest.Ingest_PublishChunkClient) (*ingest.PublishChunkResponse, error) {
	type answer struct {
		resp *ingest.PublishChunkResponse
		err  error
	}
	results := make(chan answer, 1)
	go func() {
		resp, err := stream.Recv()
		results <- answer{resp, err}
	}()
	select {
	case got := <-results:
		return got.resp, got.err
	case <-time.After(callTimeout):
		return nil, ErrCallTimeout
	}
}

// closeBatch closes a RECEIVING batch (step 9): FinalizePublish for a
// normal batch, SealImport for an imported one. The two modes cannot be
// mixed, so a normal close of an imported record answers MODE_CONFLICT; the
// record then belongs to an import an earlier run began, and SealImport
// closes it. The answer must be CLOSED.
func (c *client) closeBatch(ctx context.Context, requestID []byte, batch *ingest.BatchId,
	planDigest, manifestDigest, layoutDigest proto.Hash32, imported bool) error {
	req := &ingest.FinalizePublishRequest{Batch: batch, PlanDigest: planDigest[:],
		ManifestDigest: manifestDigest[:], LayoutManifestDigest: layoutDigest[:]}
	var resp *ingest.FinalizePublishResponse
	closed := func(step string, send func(context.Context) (*ingest.FinalizePublishResponse, error)) error {
		return c.runCall(ctx, step, func(ctx context.Context) error {
			req.Header = c.header(requestID)
			var failure error
			resp, failure = send(ctx)
			if failure != nil {
				return failure
			}
			return typedError(step, resp.GetError())
		})
	}
	if !imported {
		err := closed("FinalizePublish", func(ctx context.Context) (*ingest.FinalizePublishResponse, error) {
			return c.stub.FinalizePublish(ctx, req)
		})
		var wire *WireError
		if err == nil || !errors.As(err, &wire) || wire.Code != ingest.ErrorCode_MODE_CONFLICT {
			return closedState(err, resp)
		}
	}
	err := closed("SealImport", func(ctx context.Context) (*ingest.FinalizePublishResponse, error) {
		return c.stub.SealImport(ctx, req)
	})
	return closedState(err, resp)
}

// closedState requires the CLOSED answer of a close that was accepted.
func closedState(err error, resp *ingest.FinalizePublishResponse) error {
	if err != nil {
		return err
	}
	if resp.GetState() != ingest.BatchState_CLOSED {
		return fmtError(ErrProtocol, "close of the batch answered state %s, want CLOSED", resp.GetState())
	}
	return nil
}

// release asks the ordered release of one batch (step 12) and answers the
// ReleaseAck of the gateway.
func (c *client) release(ctx context.Context, requestID []byte, batch *ingest.BatchId,
	manifestDigest, planDigest proto.Hash32) (*ingest.ReleaseAck, error) {
	var resp *ingest.ReleaseResponse
	err := c.runCall(ctx, "Release", func(ctx context.Context) error {
		var failure error
		resp, failure = c.stub.Release(ctx, &ingest.ReleaseRequest{Header: c.header(requestID),
			Batch: batch, ManifestDigest: manifestDigest[:], PlanDigest: planDigest[:]})
		if failure != nil {
			return failure
		}
		return typedError("Release", resp.GetError())
	})
	if err != nil {
		return nil, err
	}
	return resp.GetAck(), nil
}

// sha256Sum returns the SHA-256 of one data chunk.
func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
