package ingest_handshake

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/sigaev-pro/bc-indexer/adapters/ingestclient"
	"github.com/sigaev-pro/bc-indexer/adapters/ingestclient/ingest"
	"github.com/sigaev-pro/bc-indexer/adapters/ingestclient/ingesttest"
	"github.com/sigaev-pro/bc-indexer/config"
	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/gateway/journal"
	"github.com/sigaev-pro/bc-indexer/gateway/session"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// The test supports of the handshake: the real ingest.Server on mutual TLS
// with fault injection, the test publisher that acknowledges frames, and the
// producer journal with its driver.

// faults is the fault injection of the tests: two interceptors around the
// real ingest server. They record every call and let a test act around a
// call without changing the server.
type faults struct {
	mu    sync.Mutex
	calls []string
	// before runs before the handler of a call. An error it answers
	// reaches the client and nothing happens at the gateway.
	before func(step string) error
	// after runs after the handler of a call committed. An error it
	// answers reaches the client instead of the reply: a lost reply.
	after func(step string) error
	// swallowPrepare answers a PrepareChunk itself, as stored, without the
	// handler: the cursor of the next chunk is wrong.
	swallowPrepare func(req *ingest.PrepareChunkRequest) (*ingest.PrepareChunkResponse, bool)
	// swallowChunk answers a PublishChunk itself, as stored, without the
	// handler.
	swallowChunk func(req *ingest.PublishChunkRequest) (*ingest.PublishChunkResponse, bool)
}

// hooks copies the hooks of the moment; a hook runs without the lock.
func (f *faults) hooks() (before, after func(step string) error,
	swallowPrepare func(*ingest.PrepareChunkRequest) (*ingest.PrepareChunkResponse, bool),
	swallowChunk func(*ingest.PublishChunkRequest) (*ingest.PublishChunkResponse, bool)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.before, f.after, f.swallowPrepare, f.swallowChunk
}

// Calls answers the steps the gateway handled, in order.
func (f *faults) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// stepOf shortens a full method name to the RPC it names.
func stepOf(fullMethod string) string {
	return fullMethod[strings.LastIndex(fullMethod, "/")+1:]
}

// lost is the transport error of a lost reply.
func lost() error { return status.Error(codes.Unavailable, "lost reply") }

// unary is the interceptor of the unary RPCs.
func (f *faults) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo,
	next grpc.UnaryHandler) (any, error) {
	step := stepOf(info.FullMethod)
	before, after, swallowPrepare, _ := f.hooks()
	f.mu.Lock()
	f.calls = append(f.calls, step)
	f.mu.Unlock()
	if before != nil {
		if err := before(step); err != nil {
			return nil, err
		}
	}
	if step == "PrepareChunk" && swallowPrepare != nil {
		if forged, ok := swallowPrepare(req.(*ingest.PrepareChunkRequest)); ok {
			return forged, nil
		}
	}
	resp, err := next(ctx, req)
	if err != nil {
		return resp, err
	}
	if after != nil {
		if err := after(step); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// chunkStream wraps the publish stream: a swallowed chunk is answered from
// the interceptor and never reaches the handler.
type chunkStream struct {
	grpc.ServerStream
	f *faults
}

func (s *chunkStream) RecvMsg(m any) error {
	for {
		if err := s.ServerStream.RecvMsg(m); err != nil {
			return err
		}
		_, _, _, swallowChunk := s.f.hooks()
		if swallowChunk == nil {
			return nil
		}
		if forged, ok := swallowChunk(m.(*ingest.PublishChunkRequest)); !ok {
			return nil
		} else if err := s.ServerStream.SendMsg(forged); err != nil {
			return err
		}
	}
}

func (s *chunkStream) SendMsg(m any) error {
	// The handler committed the chunk before it sends the answer.
	if _, after, _, _ := s.f.hooks(); after != nil {
		if err := after("PublishChunk"); err != nil {
			return err
		}
	}
	return s.ServerStream.SendMsg(m)
}

// stream is the interceptor of the publish stream.
func (f *faults) stream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo,
	next grpc.StreamHandler) error {
	if stepOf(info.FullMethod) != "PublishChunk" {
		return next(srv, ss)
	}
	return next(srv, &chunkStream{ServerStream: ss, f: f})
}

// record notes the PublishChunk calls of a stream: the stream interceptor
// has no per-call point of its own.
type recordingStream struct {
	grpc.ServerStream
	f *faults
}

func (s *recordingStream) RecvMsg(m any) error {
	err := s.ServerStream.RecvMsg(m)
	if err == nil {
		s.f.mu.Lock()
		s.f.calls = append(s.f.calls, "PublishChunk")
		s.f.mu.Unlock()
	}
	return err
}

func (f *faults) recording(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo,
	next grpc.StreamHandler) error {
	if stepOf(info.FullMethod) != "PublishChunk" {
		return next(srv, ss)
	}
	return next(srv, &recordingStream{ServerStream: ss, f: f})
}

// gateway is the real gateway of the tests: the ingest server over the
// gateway journal on the memory store, on mutual TLS, with a test publisher.
type gateway struct {
	t          *testing.T
	binding    destination.Binding
	frameLimit uint64
	identity   ingesttest.Identity
	listener   net.Listener
	faults     *faults
	grpc       *grpc.Server
	st         storage.Storage
	journal    *journal.Journal
	server     *ingest.Server
	gate       *session.PublisherGate

	mu       sync.Mutex
	watched  map[journal.BatchID]bool
	acks     map[journal.BatchID]journal.IngestAck
	frames   map[journal.BatchID][][]byte
	releases []releaseSeen
	autoAck  bool
}

// releaseSeen is one Release call with the retired prefix the producer
// journal held when it arrived.
type releaseSeen struct {
	id     journal.BatchID
	prefix uint64
}

func newGateway(t *testing.T, frameLimit uint64) *gateway {
	t.Helper()
	g := &gateway{t: t, binding: ingesttest.Binding(ingesttest.Network()), frameLimit: frameLimit,
		identity: ingesttest.NewIdentity(t, "localhost", "127.0.0.1"), faults: &faults{},
		watched: make(map[journal.BatchID]bool), acks: make(map[journal.BatchID]journal.IngestAck),
		frames: make(map[journal.BatchID][][]byte)}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g.listener = listener
	t.Cleanup(func() { _ = listener.Close(); g.grpc.Stop() })
	g.open()
	return g
}

// open starts a gateway process over the current storage: the first one, or
// the next one after a restart or a lost store.
func (g *gateway) open() {
	g.t.Helper()
	if g.st == nil {
		g.st = storage.NewMemory()
	}
	j, err := journal.Open(g.st, journal.Options{BindingDigest: g.binding.DescriptorDigest, FrameLimit: g.frameLimit})
	if err != nil {
		g.t.Fatal(err)
	}
	g.journal = j
	gate := session.NewGate()
	server, err := ingest.New(j, gate)
	if err != nil {
		g.t.Fatal(err)
	}
	g.server, g.gate = server, server.PublisherGate()
	grpcServer := grpc.NewServer(append(ingest.GRPCServerOptions(),
		grpc.Creds(credentials.NewTLS(g.identity.ServerTLS(g.t))),
		grpc.ChainUnaryInterceptor(g.faults.unary, func(ctx context.Context, req any, info *grpc.UnaryServerInfo,
			next grpc.UnaryHandler) (any, error) {
			return next(ctx, req)
		}),
		grpc.ChainStreamInterceptor(g.faults.stream, g.faults.recording))...)
	ingest.RegisterIngestServer(grpcServer, g.server)
	g.grpc = grpcServer
	// The listener belongs to the test, not to the server: a restart serves
	// the same endpoint.
	go func() {
		for {
			conn, err := g.listener.Accept()
			if err != nil {
				return
			}
			go grpcServer.ServeConn(conn)
		}
	}()
}

// restart boots a new gateway process over the same journal: new boot
// nonce, no session, no challenge.
func (g *gateway) restart() {
	g.grpc.Stop()
	g.open()
}

// loseStore boots a new gateway process over an empty store.
func (g *gateway) loseStore() {
	g.grpc.Stop()
	g.st = nil
	g.open()
}

// endpoint is the gateway link of a driver.
func (g *gateway) clientConfig(pollEvery time.Duration) ingestclient.Config {
	return ingestclient.Config{Endpoint: ingesttest.Endpoint(g.listener), TLS: config.ResolvedTLS{Mode: config.TLSMutual,
		CAFile: g.identity.CAFile, CertFile: g.identity.CertFile, KeyFile: g.identity.KeyFile},
		Network: g.binding.Network, Binding: g.binding, PollEvery: pollEvery}
}

// watch asks the test publisher to acknowledge the batch once it is CLOSED.
func (g *gateway) watch(id journal.BatchID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.watched[id] = true
}

// acknowledge is the test publisher: it records every frame of the CLOSED
// batch as acknowledged through the publisher gate and marks the batch
// NATS_ACKED. It keeps the marker and the frame bytes it saw.
func (g *gateway) acknowledge(id journal.BatchID) error {
	info, err := g.journal.Batch(id)
	if err != nil {
		return err
	}
	if info.State != journal.StateClosed && info.State != journal.StateNatsAcked && info.State != journal.StateReleasing {
		return nil
	}
	if info.State == journal.StateClosed {
		page, err := g.journal.BatchState(id, info.Publish.PlanDigest, nil, 1<<20)
		if err != nil {
			return err
		}
		for _, cursor := range page.Cursors {
			callback := session.Callback{Batch: id.Bytes(), PlanDigest: info.Publish.PlanDigest,
				Generation: g.gate.Generation()}
			if err := g.gate.Apply(callback, func() error {
				_, err := g.journal.RecordPubAck(id, info.Publish.PlanDigest, cursor.Addr)
				return err
			}); err != nil {
				return err
			}
		}
		if err := g.journal.MarkNATSACKed(id, info.Publish.PlanDigest, info.Reservation.ManifestDigest); err != nil {
			return err
		}
	}
	page, err := g.journal.BatchState(id, info.Publish.PlanDigest, nil, 1<<20)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.acks[id] = *page.Ack
	g.frames[id] = g.frameBytes(id)
	return nil
}

// marker answers the IngestAck the publisher saw for the batch.
func (g *gateway) marker(id journal.BatchID) (journal.IngestAck, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ack, ok := g.acks[id]
	return ack, ok
}

// heldFrames answers the frames of the batch as the gateway held them when
// they were acknowledged, in (ordinal, fragment, frame) order.
func (g *gateway) heldFrames(id journal.BatchID) [][]byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.frames[id]
}

// releasesInOrder answers the Release calls with the producer prefix each
// met.
func (g *gateway) releasesSeen() []releaseSeen {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]releaseSeen(nil), g.releases...)
}

// frameBytes reads the stored frames of a batch from the gateway storage.
// A key is batch/<id>/frames/<ordinal>/<fragment>/<frame>/<offset> with
// 8-byte big endian numbers, so the byte order of the keys is the layout
// order.
func (g *gateway) frameBytes(id journal.BatchID) [][]byte {
	prefix := append([]byte("batch/"), id.Bytes()[:]...)
	prefix = append(prefix, "frames/"...)
	type chunk struct{ key, value []byte }
	var chunks []chunk
	_ = g.st.Iterate(prefix, nil, func(key, value []byte) error {
		chunks = append(chunks, chunk{append([]byte(nil), key...), append([]byte(nil), value...)})
		return nil
	})
	sort.Slice(chunks, func(i, j int) bool { return bytes.Compare(chunks[i].key, chunks[j].key) < 0 })
	var frames [][]byte
	var current []byte
	for i, chunk := range chunks {
		framePrefix := len(chunk.key) - 8
		if i == 0 || !bytes.Equal(chunk.key[:framePrefix], chunks[i-1].key[:len(chunks[i-1].key)-8]) {
			current = nil
			frames = append(frames, current)
		}
		current = append(current, chunk.value...)
		frames[len(frames)-1] = current
	}
	return frames
}

// env is one producer journal with its driver, and one gateway.
type env struct {
	t        *testing.T
	binding  destination.Binding
	st       *ingesttest.Spy
	store    *producer.Store
	gw       *gateway
	logs     *bytes.Buffer
	// plans holds the plan payload of each batch, read when the gateway is
	// first asked to publish it.
	plans map[journal.BatchID][]byte
	// producerAcks holds the IngestAck the producer recorded, read when
	// the Release of the batch reaches the gateway.
	producerAcks map[journal.BatchID]producer.IngestAck
	// before is the hook of a test around the calls of the gateway.
	before func(step string) error
}

func newEnv(t *testing.T, frameLimit uint64) *env {
	t.Helper()
	gw := newGateway(t, frameLimit)
	st := ingesttest.NewSpy(storage.NewMemory())
	e := &env{t: t, binding: gw.binding, st: st, gw: gw, logs: &bytes.Buffer{},
		plans: make(map[journal.BatchID][]byte), producerAcks: make(map[journal.BatchID]producer.IngestAck)}
	e.store = ingesttest.OpenStore(t, st, gw.binding)
	gw.faults.before = e.gatewayCall
	return e
}

// gatewayCall is the hook of the environment: it keeps the plan of a batch
// and the acknowledgement the producer holds when its Release arrives, then
// calls the hook of the test.
func (e *env) gatewayCall(step string) error {
	id := e.id(0)
	_ = id
	for seq, payload := range e.planOfCall(step) {
		_ = seq
		_ = payload
	}
	if step == "Release" {
		entries, _ := e.store.Outbox()
		for _, entry := range entries {
			e.producerAcks[e.id(entry.Seq)] = entry.Ack
		}
	}
	if e.before != nil {
		return e.before(step)
	}
	return nil
}

// planOfCall is replaced by the release hook of the environment.
func (e *env) planOfCall(step string) map[journal.BatchID][]byte { return nil }

// id is the batch id of one sequence of this producer.
func (e *env) id(seq uint64) journal.BatchID {
	identity := e.store.Identity()
	return journal.BatchID{ProducerID: identity.ProducerID, RunID: identity.RunID, Seq: seq}
}

// stage writes one READY batch of the blocks, in chunks of chunkBytes, and
// answers its batch id. The test publisher acknowledges it once it is
// CLOSED.
func (e *env) stage(decision uint64, chunkBytes int, blocks ...ingesttest.Block) journal.BatchID {
	e.t.Helper()
	seq, _ := ingesttest.StageChunked(e.t, e.store, decision, chunkBytes, e.binding.Network, blocks...)
	id := e.id(seq)
	e.gw.watch(id)
	return id
}

// driver builds a driver for the gateway of the environment.
func (e *env) driver() *ingestclient.Driver {
	e.t.Helper()
	driver, err := ingestclient.NewDriver(ingestclient.DriverConfig{Client: e.gw.clientConfig(5 * time.Millisecond),
		Store: e.store, Storage: e.st, Reporter: config.NewReporter(config.LevelInfo, e.logs)})
	if err != nil {
		e.t.Fatal(err)
	}
	return driver
}

// run starts a driver and answers its cancel function and its result.
func (e *env) run() (context.CancelFunc, chan error) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.driver().Run(ctx) }()
	return cancel, done
}

// stop cancels the driver and answers the result of its run.
func (e *env) stop(cancel context.CancelFunc, done chan error) error {
	e.t.Helper()
	cancel()
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		e.t.Fatal("the driver did not stop")
		return nil
	}
}

// waitRetired waits until the batch is RETIRED in the producer journal.
func (e *env) waitRetired(id journal.BatchID) {
	e.t.Helper()
	waitFor(e.t, "batch RETIRED", func() bool {
		info, _ := e.store.Batch(id.Seq)
		return info.State == producer.StateRetired
	})
}

// state answers the gateway state of the batch.
func (e *env) state(id journal.BatchID) journal.State {
	e.t.Helper()
	info, err := e.gw.journal.Batch(id)
	if errors.Is(err, journal.ErrBatchNotFound) {
		return 0
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return info.State
}

// planPayload streams the plan payload of a batch.
func (e *env) planPayload(id journal.BatchID) []byte {
	e.t.Helper()
	generation, _, _, _, err := e.store.Plan(id.Seq)
	if err != nil {
		e.t.Fatal(err)
	}
	var payload []byte
	if err := e.store.PlanChunks(id.Seq, generation, func(_ uint64, chunk []byte) error {
		payload = append(payload, chunk...)
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
	return payload
}

// planFrames splits a plan payload into its frame records: each is
// u32be(len(header)) || u32be(len(frame)) || header || frame.
func planFrames(payload []byte) [][]byte {
	var frames [][]byte
	for len(payload) > 0 {
		headerLen, frameLen := binary.BigEndian.Uint32(payload[0:4]), binary.BigEndian.Uint32(payload[4:8])
		frames = append(frames, payload[8+headerLen:8+headerLen+frameLen])
		payload = payload[8+headerLen+frameLen:]
	}
	return frames
}

// waitFor polls cond for a while.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
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

// blockOf returns the block fact of a test record; extra widens the record.
func blockOf(extra []byte, number uint64) ingesttest.Block {
	return ingesttest.Block{Number: number, Hash: proto.Hash32(bytesHash(number)),
		ParentHash: proto.Hash32(bytesHash(number + 1)), Timestamp: 1732000000 + number, TxCount: 12, Extra: extra}
}

// smallBlocks returns count small block facts, for a batch of many events.
func smallBlocks(count int) []ingesttest.Block {
	blocks := make([]ingesttest.Block, count)
	for i := range blocks {
		blocks[i] = blockOf(nil, uint64(i))
	}
	return blocks
}

// bigExtra makes a record with one frame of 65,700 bytes.
var bigExtra = make([]byte, 65300)

// fragmentCount is kept for the readers of the frame decode below.
const fragmentCount = 1

var _ = io.EOF
