package ingest

import (
	"context"
	"crypto/sha256"
	"net"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/gateway/journal"
	"github.com/sigaev-pro/bc-indexer/gateway/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

var (
	testBinding  [32]byte
	testProducer = [16]byte{1}
	testRun      = [16]byte{2}
)

func init() {
	testBinding = sha256.Sum256([]byte("ingest test binding"))
}

// harness is one in-process gateway: journal over memory storage, session
// gate, server on a bufconn listener.
type harness struct {
	t       *testing.T
	st      storage.Storage
	j       *journal.Journal
	gate    *session.Gate
	server  *Server
	conn    *grpc.ClientConn
	client  IngestClient
	listen  *bufconn.Listener
	grpcSrv *grpc.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st := storage.NewMemory()
	j := openJournal(t, st)
	return startHarness(t, st, j, session.NewGate())
}

// openJournal opens or reopens the journal over one storage.
func openJournal(t *testing.T, st storage.Storage) *journal.Journal {
	t.Helper()
	j, err := journal.Open(st, journal.Options{BindingDigest: testBinding})
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func startHarness(t *testing.T, st storage.Storage, j *journal.Journal, gate *session.Gate) *harness {
	t.Helper()
	srv, err := New(j, gate)
	if err != nil {
		t.Fatal(err)
	}
	listen := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer(GRPCServerOptions()...)
	RegisterIngestServer(grpcSrv, srv)
	go func() { _ = grpcSrv.Serve(listen) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listen.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t: t, st: st, j: j, gate: gate, server: srv, conn: conn,
		client: NewIngestClient(conn), listen: listen, grpcSrv: grpcSrv,
	}
	t.Cleanup(func() {
		_ = conn.Close()
		grpcSrv.Stop()
	})
	return h
}

// openSessionAt performs the handshake and returns the header builder.
func (h *harness) openSessionAt(t *testing.T, admission, connection uint64) *sessionCtx {
	t.Helper()
	ctx := context.Background()
	hello, err := h.client.HelloChallenge(ctx, &HelloChallengeRequest{
		ProducerId: testProducer[:], RunId: testRun[:], Role: "producer", NetworkDigest: []byte{3},
	})
	if err != nil || hello.GetError() != nil {
		t.Fatalf("hello: %v %+v", err, hello.GetError())
	}
	open, err := h.client.OpenSession(ctx, &OpenSessionRequest{
		EchoedChallenge:      hello.GetChallenge(),
		Mode:                 OpenSessionRequest_NORMAL,
		ActiveBindingDigest:  testBinding[:],
		AdmissionGeneration:  admission,
		ConnectionGeneration: connection,
		ProducerId:           testProducer[:],
		RunId:                testRun[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	if e := open.GetError(); e != nil {
		t.Fatalf("open session: %s %s", e.GetCode(), e.GetDetail())
	}
	return &sessionCtx{token: open.GetSessionToken(), admission: admission, connection: connection}
}

type sessionCtx struct {
	token      []byte
	admission  uint64
	connection uint64
}

func (c *sessionCtx) header() *SessionHeader {
	return &SessionHeader{
		ProtocolVersion:      ProtocolVersion,
		SessionToken:         c.token,
		ProducerId:           testProducer[:],
		RunId:                testRun[:],
		ConnectionGeneration: c.connection,
		AdmissionGeneration:  c.admission,
		BindingDigest:        testBinding[:],
	}
}

func batchMsg(seq uint64) *BatchId {
	return &BatchId{ProducerId: testProducer[:], RunId: testRun[:], BatchSeq: seq}
}

func sha32(b []byte) []byte {
	d := sha256.Sum256(b)
	return d[:]
}

func TestHandshakeAndSessionRules(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	hello, err := h.client.HelloChallenge(ctx, &HelloChallengeRequest{ProducerId: testProducer[:], RunId: testRun[:]})
	if err != nil || len(hello.GetBootNonce()) != 16 || len(hello.GetChallenge()) != 16 {
		t.Fatalf("hello: %v %+v", err, hello)
	}

	// A challenge no boot issued (or an old boot's) is stale.
	open, err := h.client.OpenSession(ctx, &OpenSessionRequest{
		EchoedChallenge: make([]byte, 16), ActiveBindingDigest: testBinding[:],
		AdmissionGeneration: 1, ConnectionGeneration: 1, ProducerId: testProducer[:], RunId: testRun[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	if open.GetError().GetCode() != ErrorCode_SESSION_STALE {
		t.Fatalf("unknown challenge: %s", open.GetError().GetCode())
	}

	// Binding mismatch, maintenance mode and zero generations are typed
	// errors, never session state.
	cases := []struct {
		name string
		req  *OpenSessionRequest
		code ErrorCode
	}{
		{"binding", &OpenSessionRequest{
			EchoedChallenge: hello.GetChallenge(), ActiveBindingDigest: []byte{1},
			AdmissionGeneration: 1, ConnectionGeneration: 1, ProducerId: testProducer[:], RunId: testRun[:],
		}, ErrorCode_BINDING_MISMATCH},
		{"maintenance", &OpenSessionRequest{
			EchoedChallenge: hello.GetChallenge(), Mode: OpenSessionRequest_MAINTENANCE,
			ActiveBindingDigest: testBinding[:], AdmissionGeneration: 1, ConnectionGeneration: 1,
			ProducerId: testProducer[:], RunId: testRun[:],
		}, ErrorCode_MODE_CONFLICT},
		{"generations", &OpenSessionRequest{
			EchoedChallenge: hello.GetChallenge(), ActiveBindingDigest: testBinding[:],
			ProducerId: testProducer[:], RunId: testRun[:],
		}, ErrorCode_GENERATION_CONFLICT},
	}
	for _, tc := range cases {
		open, err = h.client.OpenSession(ctx, tc.req)
		if err != nil {
			t.Fatal(err)
		}
		if open.GetError().GetCode() != tc.code {
			t.Fatalf("%s: code %s, want %s", tc.name, open.GetError().GetCode(), tc.code)
		}
	}

	open, err = h.client.OpenSession(ctx, &OpenSessionRequest{
		EchoedChallenge: hello.GetChallenge(), ActiveBindingDigest: testBinding[:],
		AdmissionGeneration: 1, ConnectionGeneration: 1, ProducerId: testProducer[:], RunId: testRun[:],
	})
	if err != nil || open.GetError() != nil {
		t.Fatalf("open: %v %+v", err, open.GetError())
	}
	if len(open.GetSessionToken()) != 32 || open.GetRecoveryAction() != OpenSessionResponse_NONE {
		t.Fatalf("session answer: %+v", open)
	}
	// One challenge opens exactly one session.
	again, err := h.client.OpenSession(ctx, &OpenSessionRequest{
		EchoedChallenge: hello.GetChallenge(), ActiveBindingDigest: testBinding[:],
		AdmissionGeneration: 1, ConnectionGeneration: 1, ProducerId: testProducer[:], RunId: testRun[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.GetError().GetCode() != ErrorCode_SESSION_STALE {
		t.Fatalf("challenge reuse: %s", again.GetError().GetCode())
	}
}

func TestMutatingHeaderValidation(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()
	digest := sha32([]byte("manifest"))

	mutate := func(header *SessionHeader) ErrorCode {
		resp, err := h.client.BeginPrepare(ctx, &BeginPrepareRequest{
			Header: header, Batch: batchMsg(1), ManifestDigest: digest, EventCount: 1, DescriptorBytesTotal: 7,
		})
		if err != nil {
			t.Fatal(err)
		}
		if resp.GetError() == nil {
			t.Fatal("mutation unexpectedly succeeded")
		}
		return resp.GetError().GetCode()
	}

	if code := mutate(nil); code != ErrorCode_IDENTITY_MISMATCH {
		t.Fatalf("no header: %s", code)
	}
	badProto := s.header()
	badProto.ProtocolVersion = 1
	if code := mutate(badProto); code != ErrorCode_VERSION_UNSUPPORTED {
		t.Fatalf("bad protocol: %s", code)
	}
	if code := mutate(&SessionHeader{ProtocolVersion: 2, SessionToken: []byte("short")}); code != ErrorCode_SESSION_STALE {
		t.Fatalf("short token: %s", code)
	}
	if code := mutate(&SessionHeader{ProtocolVersion: 2, SessionToken: make([]byte, 32)}); code != ErrorCode_SESSION_STALE {
		t.Fatalf("unknown token: %s", code)
	}
	badGen := s.header()
	badGen.ConnectionGeneration = 9
	if code := mutate(badGen); code != ErrorCode_GENERATION_CONFLICT {
		t.Fatalf("generation mismatch: %s", code)
	}
	badBinding := s.header()
	badBinding.BindingDigest = []byte{1}
	if code := mutate(badBinding); code != ErrorCode_BINDING_MISMATCH {
		t.Fatalf("binding mismatch: %s", code)
	}
	foreign := s.header()
	foreign.RunId = []byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}
	if code := mutate(foreign); code != ErrorCode_IDENTITY_MISMATCH {
		t.Fatalf("foreign run: %s", code)
	}
}

func TestPrepareFlowOverRPC(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()
	manifest := []byte("extractor.manifest.v2\x00over-rpc")
	digest := sha32(manifest)

	begin, err := h.client.BeginPrepare(ctx, &BeginPrepareRequest{
		Header: s.header(), Batch: batchMsg(1), ManifestDigest: digest,
		EventCount: 2, DescriptorBytesTotal: uint64(len(manifest)),
	})
	if err != nil || begin.GetError() != nil {
		t.Fatalf("begin: %v %+v", err, begin.GetError())
	}
	if begin.GetResumeOffset() != 0 {
		t.Fatalf("fresh resume offset = %d, want 0", begin.GetResumeOffset())
	}

	chunk := func(off uint64, data []byte) *PrepareChunkResponse {
		t.Helper()
		resp, err := h.client.PrepareChunk(ctx, &PrepareChunkRequest{
			Header: s.header(), Batch: batchMsg(1), ManifestDigest: digest,
			Offset: off, ChunkSha256: sha32(data), Data: data,
		})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := chunk(0, manifest[:10])
	if resp.GetError() != nil || resp.GetNextOffset() != 10 {
		t.Fatalf("chunk 1: %+v %+v", resp, resp.GetError())
	}
	if resp := chunk(0, manifest[:10]); resp.GetError() != nil || resp.GetNextOffset() != 10 {
		t.Fatalf("chunk retry: %+v", resp)
	}
	// A gap answers OFFSET_MISMATCH with the expected cursor.
	resp = chunk(20, manifest[20:])
	if resp.GetError().GetCode() != ErrorCode_OFFSET_MISMATCH || resp.GetNextOffset() != 10 {
		t.Fatalf("gap: %+v %+v", resp, resp.GetError())
	}
	// A conflicting rewrite is an immutable conflict.
	if resp := chunk(0, []byte("XXXXXXXXXX")); resp.GetError().GetCode() != ErrorCode_IMMUTABLE_CONFLICT {
		t.Fatalf("rewrite: %s", resp.GetError().GetCode())
	}
	if resp := chunk(10, manifest[10:]); resp.GetError() != nil {
		t.Fatalf("chunk 2: %+v", resp)
	}

	fin, err := h.client.FinalizePrepare(ctx, &FinalizePrepareRequest{
		Header: s.header(), Batch: batchMsg(1), ManifestDigest: digest,
	})
	if err != nil || fin.GetError() != nil {
		t.Fatalf("finalize: %v %+v", err, fin.GetError())
	}
	seed := append([]byte(nil), fin.GetSeed()...)
	if len(seed) == 0 || string(fin.GetPlanDigest()) != string(sha32(seed)) {
		t.Fatal("finalize must return the exact seed and plan digest")
	}
	// The BeginPrepare retry now answers the saved reservation, and the
	// next batch's sequence continues without a gap.
	retry, err := h.client.BeginPrepare(ctx, &BeginPrepareRequest{
		Header: s.header(), Batch: batchMsg(1), ManifestDigest: digest,
		EventCount: 2, DescriptorBytesTotal: uint64(len(manifest)),
	})
	if err != nil || retry.GetError() != nil {
		t.Fatalf("begin retry: %v %+v", err, retry.GetError())
	}
	if retry.GetReservation() == nil || string(retry.GetReservation().GetSeed()) != string(seed) {
		t.Fatal("begin retry must return the saved reservation seed")
	}
	// A second batch over the wire continues the sequence range exactly
	// after the first reservation.
	if _, err := h.client.BeginPrepare(ctx, &BeginPrepareRequest{
		Header: s.header(), Batch: batchMsg(2), ManifestDigest: sha32([]byte("second")),
		EventCount: 1, DescriptorBytesTotal: 6,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.PrepareChunk(ctx, &PrepareChunkRequest{
		Header: s.header(), Batch: batchMsg(2), ManifestDigest: sha32([]byte("second")),
		Offset: 0, ChunkSha256: sha32([]byte("second")), Data: []byte("second"),
	}); err != nil {
		t.Fatal(err)
	}
	if fin2, err := h.client.FinalizePrepare(ctx, &FinalizePrepareRequest{
		Header: s.header(), Batch: batchMsg(2), ManifestDigest: sha32([]byte("second")),
	}); err != nil || fin2.GetError() != nil {
		t.Fatalf("second finalize: %v %+v", err, fin2.GetError())
	}
	first, err := h.j.Batch(journal.BatchID{ProducerID: testProducer, RunID: testRun, Seq: 1})
	second, err2 := h.j.Batch(journal.BatchID{ProducerID: testProducer, RunID: testRun, Seq: 2})
	if err != nil || err2 != nil || first.Reservation == nil || second.Reservation == nil {
		t.Fatalf("journal states: %+v %+v %v %v", first, second, err, err2)
	}
	if second.Reservation.SequenceBase != first.Reservation.SequenceBase+first.Reservation.EventCount {
		t.Fatalf("sequence continuation: %d after %d+%d",
			second.Reservation.SequenceBase, first.Reservation.SequenceBase, first.Reservation.EventCount)
	}
}

func TestChunkBoundsOverRPC(t *testing.T) {
	h := newHarness(t)
	s := h.openSessionAt(t, 1, 1)
	ctx := context.Background()
	digest := sha32([]byte("m"))
	if _, err := h.client.BeginPrepare(ctx, &BeginPrepareRequest{
		Header: s.header(), Batch: batchMsg(1), ManifestDigest: digest, EventCount: 1, DescriptorBytesTotal: 9,
	}); err != nil {
		t.Fatal(err)
	}

	// One data chunk over 64 KiB is a typed LIMIT_INCOMPATIBLE error.
	big := make([]byte, MaxDataChunk+1)
	resp, err := h.client.PrepareChunk(ctx, &PrepareChunkRequest{
		Header: s.header(), Batch: batchMsg(1), ManifestDigest: digest,
		Offset: 0, ChunkSha256: sha32(big), Data: big,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetError().GetCode() != ErrorCode_LIMIT_INCOMPATIBLE {
		t.Fatalf("64 KiB bound: %s", resp.GetError().GetCode())
	}

	// A whole RPC over 256 KiB including the wrapper is refused by the
	// transport: gRPC ResourceExhausted, not a typed success.
	huge := make([]byte, MaxMessageBytes+64*1024)
	_, err = h.client.PrepareChunk(ctx, &PrepareChunkRequest{
		Header: s.header(), Batch: batchMsg(1), ManifestDigest: digest,
		Offset: 0, ChunkSha256: sha32(huge), Data: huge,
	})
	if err == nil {
		t.Fatal("oversized RPC must fail at the transport level")
	}
	if code := status.Code(err); code != codes.ResourceExhausted && code != codes.OutOfRange {
		t.Fatalf("oversized RPC: %v (%s)", err, code)
	}
}

func TestWireErrorFormatting(t *testing.T) {
	we := &wireError{ErrorCode_SESSION_STALE, RetryAction_RECONNECT, "session fenced"}
	if we.Error() == "" {
		t.Fatal("wireError must render a readable message")
	}
	if we.toProto(nil).GetCode() != ErrorCode_SESSION_STALE {
		t.Fatal("toProto must carry the registry code")
	}
}
