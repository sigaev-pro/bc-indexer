package ingestclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/sigaev-pro/bc-indexer/adapters/ingestclient/ingest"
	"github.com/sigaev-pro/bc-indexer/adapters/ingestclient/ingesttest"
	"github.com/sigaev-pro/bc-indexer/config"
	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// The driver against a scripted ingest gateway, on the gRPC of this module
// over real mutual TLS: the whole call order of the contract end to end on
// the third-party versions the node compiles, and the retry rules under
// faults. The real ingest.Server is exercised by test/ingest_handshake.

// manyBlocks returns count small block facts, for a batch of many events.
func manyBlocks(count int) []ingesttest.Block {
	blocks := make([]ingesttest.Block, count)
	for i := range blocks {
		blocks[i] = blockOf(nil)
		blocks[i].Number = uint64(i)
	}
	return blocks
}

// bigExtra makes a record with one frame of 65,700 bytes: above one data
// chunk, so the driver sends it in two pieces.
var bigExtra = make([]byte, 65300)

// blockOf returns the block fact of a test record; extra widens the record.
func blockOf(extra []byte) ingesttest.Block {
	return ingesttest.Block{Number: 49123456, Hash: sha256.Sum256([]byte("block hash")),
		ParentHash: sha256.Sum256([]byte("parent")), Timestamp: 1732000000, TxCount: 12, Extra: extra}
}

// rig is one producer journal and one scripted gateway, linked by real
// mutual TLS.
type rig struct {
	t        *testing.T
	st       *ingesttest.Spy
	store    *producer.Store
	gateway  *fakeGateway
	binding  destination.Binding
	identity ingesttest.Identity
	logs     *bytes.Buffer
	cfg      Config
	// generations pairs each connection generation of an OpenSession with
	// the one the producer storage held when the call arrived.
	generations [][2]uint64
	// plans holds the frames of the plan of each batch, read when the
	// gateway is first asked to publish it; planErr is the error of that
	// read.
	plans   map[uint64][]storedFrame
	planErr error
	// onStep is the extra hook of a test, called before the effect of
	// every call of the gateway.
	onStep func(step string, seq uint64)
}

func newRig(t *testing.T) *rig {
	t.Helper()
	fastTimings(t)
	binding := ingesttest.Binding(ingesttest.Network())
	st := ingesttest.NewSpy(storage.NewMemory())
	r := &rig{t: t, st: st, binding: binding, logs: &bytes.Buffer{}, plans: make(map[uint64][]storedFrame)}
	r.store = ingesttest.OpenStore(t, st, binding)
	r.gateway = newFakeGateway(binding.Network, binding.DescriptorDigest)
	r.gateway.onOpen = func(generation uint64) {
		saved, _ := readConnectionGeneration(st)
		r.generations = append(r.generations, [2]uint64{generation, saved})
	}
	r.gateway.before = r.step
	r.identity = ingesttest.NewIdentity(t, "localhost", "127.0.0.1")
	listener := ingesttest.Listen(t)
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(r.identity.ServerTLS(t))))
	ingest.RegisterIngestServer(server, r.gateway)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	r.cfg = Config{Endpoint: ingesttest.Endpoint(listener), TLS: config.ResolvedTLS{Mode: config.TLSMutual,
		CAFile: r.identity.CAFile, CertFile: r.identity.CertFile, KeyFile: r.identity.KeyFile},
		Network: binding.Network, Binding: binding, PollEvery: 5 * time.Millisecond}
	return r
}

// step is the hook of the rig: it keeps the plan of a batch when the
// gateway is first asked to publish it, then calls the hook of the test.
func (r *rig) step(step string, seq uint64) {
	if step == "BeginPublish" && seq != 0 {
		if _, seen := r.plans[seq]; !seen {
			frames, err := r.readPlan(seq)
			if r.planErr == nil {
				r.planErr = err
			}
			r.plans[seq] = frames
		}
	}
	if r.onStep != nil {
		r.onStep(step, seq)
	}
}

// fastTimings shortens the delays of the client for one test.
func fastTimings(t *testing.T) {
	t.Helper()
	base, ceiling, timeout := backoffBaseDelay, backoffMaxDelay, callTimeout
	backoffBaseDelay, backoffMaxDelay, callTimeout = 2*time.Millisecond, 10*time.Millisecond, 3*time.Second
	t.Cleanup(func() { backoffBaseDelay, backoffMaxDelay, callTimeout = base, ceiling, timeout })
}

// stage writes one READY batch of the blocks, in chunks of chunkBytes, and
// answers its sequence.
func (r *rig) stage(decision uint64, chunkBytes int, blocks ...ingesttest.Block) uint64 {
	r.t.Helper()
	seq, _ := ingesttest.StageChunked(r.t, r.store, decision, chunkBytes, r.binding.Network, blocks...)
	return seq
}

// start runs a driver until stop.
func (r *rig) start() (context.CancelFunc, chan error) {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	driver, err := NewDriver(DriverConfig{Client: r.cfg, Store: r.store, Storage: r.st,
		Reporter: config.NewReporter(config.LevelInfo, r.logs)})
	if err != nil {
		r.t.Fatal(err)
	}
	go func() { done <- driver.Run(ctx) }()
	return cancel, done
}

// stop cancels the driver and answers the result of its run.
func (r *rig) stop(cancel context.CancelFunc, done chan error) error {
	r.t.Helper()
	cancel()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		r.t.Fatal("the driver did not stop")
		return nil
	}
}

// runErr waits for a run the test expects to end by itself.
func (r *rig) runErr(done chan error) error {
	r.t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		r.t.Fatal("the driver did not end")
		return nil
	}
}

// batch answers the durable state of one batch.
func (r *rig) batch(seq uint64) producer.BatchInfo {
	r.t.Helper()
	info, err := r.store.Batch(seq)
	if err != nil {
		r.t.Fatal(err)
	}
	return info
}

// readPlan answers the frames of the plan of a batch that holds one.
func (r *rig) readPlan(seq uint64) ([]storedFrame, error) {
	generation, _, _, _, err := r.store.Plan(seq)
	if err != nil {
		return nil, err
	}
	var payload []byte
	if err := r.store.PlanChunks(seq, generation, func(_ uint64, chunk []byte) error {
		payload = append(payload, chunk...)
		return nil
	}); err != nil {
		return nil, err
	}
	return decodePlan(payload)
}

// waitUntil polls cond for a while.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// retired waits until the batch is RETIRED in the producer journal.
func (r *rig) retired(seq uint64) {
	r.t.Helper()
	waitUntil(r.t, "batch RETIRED", func() bool { return r.batch(seq).State == producer.StateRetired })
}

// checkDelivered requires what one delivered batch left on both sides: the
// frames at the gateway are the frames of the plan, each declared once and
// sent in pieces of at most one data chunk.
func (r *rig) checkDelivered(seq uint64) {
	r.t.Helper()
	if r.planErr != nil {
		r.t.Fatalf("the plan of batch %d could not be read: %v", seq, r.planErr)
	}
	plan := r.plans[seq]
	if info := r.batch(seq); info.State != producer.StateRetired || info.Ack != nil || r.store.RetiredPrefix() < seq {
		r.t.Fatalf("batch %d is %s in the producer journal with prefix %d; want RETIRED and no outbox entry",
			seq, info.State, r.store.RetiredPrefix())
	}
	held := r.gateway.Frames(seq)
	if len(held) != len(plan) || len(plan) == 0 {
		r.t.Fatalf("the gateway holds %d frames, the plan has %d", len(held), len(plan))
	}
	for i := range plan {
		if !bytes.Equal(held[i], plan[i].Encoded) {
			r.t.Fatalf("frame %d of batch %d at the gateway differs from the plan", i, seq)
		}
	}
	got := r.gateway.Batch(seq)
	if got.state != ingest.BatchState_RETIRED || got.largest > MaxDataChunk || got.declared != len(plan) || got.releases != 1 {
		r.t.Fatalf("gateway: state %s, largest chunk %d, %d declarations for %d frames, %d release commits",
			got.state, got.largest, got.declared, len(plan), got.releases)
	}
}

// checkGenerations requires that every session was opened under a
// connection generation that was saved before the call and never used twice.
func (r *rig) checkGenerations() {
	r.t.Helper()
	for i, pair := range r.generations {
		if pair[0] != pair[1] || i > 0 && pair[0] <= r.generations[i-1][0] {
			r.t.Fatalf("OpenSession generations %v: each must be saved first and larger than the last", r.generations)
		}
	}
}

func TestDriverRunsEndToEndOnTheModuleGRPC(t *testing.T) {
	// The point of this suite: the whole driver on the gRPC the node
	// compiles, over real mutual TLS.
	if grpc.Version != "1.80.0" {
		t.Fatalf("this suite runs on gRPC %s, the accepted version", grpc.Version)
	}
	r := newRig(t)
	r.gateway.autoAck = true
	first := r.stage(1, producer.ChunkBytes, blockOf([]byte("small extra")))
	second := r.stage(2, producer.ChunkBytes, blockOf(bigExtra))
	cancel, done := r.start()
	r.retired(first)
	r.retired(second)
	if err := r.stop(cancel, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	r.checkDelivered(first)
	r.checkDelivered(second)

	// One reservation per batch, the sequence ranges in a row, and one
	// session for the whole run.
	if reserved, opens := r.gateway.Reserved(), r.gateway.Opens(); reserved != 2 || len(opens) != 1 || opens[0] != 1 {
		t.Fatalf("%d reservations, OpenSession at generations %v; want 2 and [1]", reserved, opens)
	}
	r.checkGenerations()
	// A frame above one data chunk went in pieces of at most one chunk.
	big, plan := r.gateway.Batch(second), r.plans[second]
	if big.chunks <= len(plan) || big.largest != MaxDataChunk {
		t.Fatalf("the large batch went in %d chunks for %d frames, largest %d; want a frame split at the chunk bound",
			big.chunks, len(plan), big.largest)
	}
	// The log lines of the driver follow each other in order.
	line, previous := r.logs.String(), -1
	for _, want := range []string{"event=DELIVERY batch=1 state=SEALED", "event=DELIVERY batch=1 state=ACKED_CLEANUP",
		"event=DELIVERY owner=producer state=RETIRED prefix=1", "event=DELIVERY batch=2 state=SEALED",
		"event=DELIVERY batch=2 state=ACKED_CLEANUP", "event=DELIVERY owner=producer state=RETIRED prefix=2"} {
		at := strings.Index(line, want)
		if at < previous {
			t.Fatalf("log line %q is missing or out of order in:\n%s", want, line)
		}
		previous = at
	}
	if strings.Contains(line, "event=ERROR") {
		t.Fatalf("a clean run logs no failure:\n%s", line)
	}
}

func TestLostReplyAtEveryRPC(t *testing.T) {
	for _, step := range []string{"HelloChallenge", "OpenSession", "BeginPrepare", "PrepareChunk", "FinalizePrepare",
		"BeginPublish", "PublishChunk", "FinalizePublish", "GetBatchState", "Release"} {
		t.Run(step, func(t *testing.T) {
			r := newRig(t)
			r.gateway.autoAck = step != "GetBatchState"
			seq := r.stage(1, producer.ChunkBytes, blockOf(bigExtra))
			r.gateway.Lose(step, 1)
			cancel, done := r.start()
			if step == "GetBatchState" {
				// The publisher acknowledges while the state poll is
				// repeated.
				waitUntil(t, "CLOSED at the gateway", func() bool { return r.gateway.Batch(seq).state == ingest.BatchState_CLOSED })
				r.gateway.Acknowledge(seq)
			}
			r.retired(seq)
			if err := r.stop(cancel, done); err != nil {
				t.Fatalf("Run: %v", err)
			}
			r.checkDelivered(seq)
			// Nothing was committed twice: one reservation, one closing
			// commit and one release, however often a call was repeated.
			calls, got := r.gateway.Calls(), r.gateway.Batch(seq)
			if r.gateway.Reserved() != 1 || got.closes != 1 || got.releases != 1 || count(calls, "SealImport") != 0 {
				t.Fatalf("%d reservations, %d closing commits, %d release commits, %d imports; want 1, 1, 1, 0",
					r.gateway.Reserved(), got.closes, got.releases, count(calls, "SealImport"))
			}
			// The same call came again.
			if count(calls, step) < 2 {
				t.Fatalf("%s was called %d times; the lost reply must be repeated", step, count(calls, step))
			}
			r.checkGenerations()
		})
	}
}

func TestOffsetMismatchContinuesAtTheGatewayCursor(t *testing.T) {
	for _, tc := range []struct{ step, name string }{{"PrepareChunk", "manifest chunk"}, {"PublishChunk", "frame piece"}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.gateway.autoAck = true
			var seq uint64
			if tc.step == "PrepareChunk" {
				// A manifest above one data chunk goes in two pieces: the
				// first is answered as stored and kept nothing.
				seq = r.stage(1, producer.ChunkBytes, manyBlocks(250)...)
				if manifest := r.manifestBytes(seq); len(manifest) <= MaxDataChunk {
					t.Fatalf("the manifest has %d bytes; the case needs more than one piece", len(manifest))
				}
			} else {
				// The first piece of a frame above one data chunk.
				seq = r.stage(1, producer.ChunkBytes, blockOf(bigExtra))
			}
			r.gateway.Swallow(tc.step, 1)
			cancel, done := r.start()
			r.retired(seq)
			if err := r.stop(cancel, done); err != nil {
				t.Fatalf("Run: %v", err)
			}
			r.checkDelivered(seq)
			// The gateway answered OFFSET_MISMATCH with its cursor, and
			// the driver continued there.
			if r.gateway.Mismatches(tc.step) != 1 {
				t.Fatalf("the gateway answered OFFSET_MISMATCH %d times at %s, want once", r.gateway.Mismatches(tc.step), tc.step)
			}
			if r.gateway.Reserved() != 1 {
				t.Fatalf("%d reservations; the cursor cost none", r.gateway.Reserved())
			}
		})
	}
}

func TestClosedWithoutAckIsNotADelivery(t *testing.T) {
	r := newRig(t)
	seq := r.stage(1, producer.ChunkBytes, blockOf(nil))
	cancel, done := r.start()
	defer func() { _ = r.stop(cancel, done) }()
	waitUntil(t, "CLOSED at the gateway", func() bool { return r.gateway.Batch(seq).state == ingest.BatchState_CLOSED })
	// Several poll intervals pass without an acknowledgement.
	time.Sleep(15 * r.cfg.PollEvery)
	if info := r.batch(seq); info.State != producer.StateSealed || info.Ack != nil {
		t.Fatalf("CLOSED without an IngestAck was recorded: %+v", info)
	}
	if entries, _ := r.store.Outbox(); len(entries) != 0 {
		t.Fatalf("CLOSED without an IngestAck reached the outbox: %+v", entries)
	}
	r.gateway.Acknowledge(seq)
	r.retired(seq)
}

func TestSessionStaleReconnectsUnderTheNextGeneration(t *testing.T) {
	r := newRig(t)
	r.gateway.autoAck = true
	first := r.stage(1, producer.ChunkBytes, blockOf(nil))
	second := r.stage(2, producer.ChunkBytes, blockOf(nil))
	// The gateway restarts once, after the first batch retired: the next
	// call of the old session answers SESSION_STALE.
	restarted := false
	r.onStep = func(step string, seq uint64) {
		if step == "BeginPrepare" && seq == second && !restarted {
			restarted = true
			r.gateway.Restart()
		}
	}
	cancel, done := r.start()
	r.retired(first)
	r.retired(second)
	if err := r.stop(cancel, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	r.checkDelivered(second)
	if opens := r.gateway.Opens(); len(opens) != 2 || opens[0] != 1 || opens[1] != 2 {
		t.Fatalf("OpenSession at generations %v, want [1 2]", opens)
	}
	if saved, _ := readConnectionGeneration(r.st); saved != 2 {
		t.Fatalf("saved connection generation %d, want 2", saved)
	}
	r.checkGenerations()
}

func TestBindingMismatchStopsWithConfigBlocked(t *testing.T) {
	r := newRig(t)
	r.gateway.refuseBind = true
	seq := r.stage(1, 64, blockOf(nil))
	before := r.manifestBytes(seq)
	_, done := r.start()
	err := r.runErr(done)
	if !errors.Is(err, ErrConfigBlocked) || config.Exit(err) != config.ExitConfig || !strings.Contains(err.Error(), "BINDING_MISMATCH") {
		t.Fatalf("Run answered %v; want CONFIG_BLOCKED of the configuration exit class, naming the code", err)
	}
	// Nothing was deleted and nothing was sent: the batch is READY with
	// every byte.
	if info := r.batch(seq); info.State != producer.StateReady || !bytes.Equal(r.manifestBytes(seq), before) {
		t.Fatalf("batch after CONFIG_BLOCKED: %+v", info)
	}
	if count(r.gateway.Calls(), "BeginPrepare") != 0 {
		t.Fatal("a batch was sent to a gateway of another destination")
	}
	if !strings.Contains(r.logs.String(), "event=ERROR class=config code=2 field=delivery reason=") {
		t.Fatalf("the driver logged no failure line:\n%s", r.logs.String())
	}
}

// manifestBytes streams the manifest chunks of a batch.
func (r *rig) manifestBytes(seq uint64) []byte {
	r.t.Helper()
	var manifest []byte
	if err := r.store.AttemptManifest(seq, r.batch(seq).Ready.Attempt, func(_ uint64, chunk []byte) error {
		manifest = append(manifest, chunk...)
		return nil
	}); err != nil {
		r.t.Fatal(err)
	}
	return manifest
}

func TestAlreadyRetiredIsNotADelivery(t *testing.T) {
	for _, state := range []producer.State{producer.StateReady, producer.StateSealed} {
		t.Run(state.String(), func(t *testing.T) {
			r := newRig(t)
			seq := r.stage(1, producer.ChunkBytes, blockOf(nil))
			if state == producer.StateSealed {
				// The gateway retires the batch behind the producer's back
				// once it is sealed and the driver asks where it stands.
				r.onStep = func(step string, seq uint64) {
					if step == "GetBatchState" {
						r.gateway.RetireEarly(seq)
					}
				}
			} else {
				r.gateway.RetireEarly(seq)
			}
			_, done := r.start()
			err := r.runErr(done)
			if !errors.Is(err, ErrBlocked) || !strings.Contains(err.Error(), "ALREADY_RETIRED is not a delivery") {
				t.Fatalf("Run answered %v; want BLOCKED and no delivery", err)
			}
			if info := r.batch(seq); info.State != state || info.Ack != nil {
				t.Fatalf("batch after ALREADY_RETIRED: %+v; want %s and no acknowledgement", info, state)
			}
			if entries, _ := r.store.Outbox(); len(entries) != 0 || count(r.gateway.Calls(), "Release") != 0 {
				t.Fatalf("ALREADY_RETIRED was released: outbox %+v, calls %v", entries, r.gateway.Calls())
			}
		})
	}
}

func TestIngestAckMismatchIsNotRecorded(t *testing.T) {
	for _, field := range []string{"batch", "run", "manifest", "plan", "layout", "binding", "epoch"} {
		t.Run(field, func(t *testing.T) {
			r := newRig(t)
			r.gateway.autoAck = true
			seq := r.stage(1, producer.ChunkBytes, blockOf(nil))
			r.gateway.DamageAck(field)
			_, done := r.start()
			if err := r.runErr(done); !errors.Is(err, ErrIngestAckMismatch) {
				t.Fatalf("a wrong %s in the IngestAck: Run answered %v; want ErrIngestAckMismatch", field, err)
			}
			if info := r.batch(seq); info.State != producer.StateSealed || info.Ack != nil || count(r.gateway.Calls(), "Release") != 0 {
				t.Fatalf("the mismatched acknowledgement was recorded or released: %+v", info)
			}
		})
	}
}

// An acknowledged state without the IngestAck is a malformed answer.
func TestAcknowledgedStateWithoutAckIsMalformed(t *testing.T) {
	r := newRig(t)
	r.gateway.autoAck = true
	seq := r.stage(1, producer.ChunkBytes, blockOf(nil))
	r.gateway.DamageAck("missing")
	_, done := r.start()
	if err := r.runErr(done); !errors.Is(err, ErrProtocol) || r.batch(seq).State != producer.StateSealed {
		t.Fatalf("Run answered %v with batch %s; want ErrProtocol and nothing recorded", err, r.batch(seq).State)
	}
}

func TestGatewayStoreLostAfterSealReplaysTheOriginalReservation(t *testing.T) {
	r := newRig(t)
	r.gateway.autoAck = true
	seq := r.stage(1, producer.ChunkBytes, blockOf(nil))
	// The record leaves the gateway between the state probe and
	// BeginPublish; the seed the gateway held is kept for the comparison.
	var original []byte
	r.onStep = func(step string, seq uint64) {
		if step == "BeginPublish" && original == nil {
			original = r.gateway.Batch(seq).seed
			r.gateway.Forget(seq)
		}
	}
	cancel, done := r.start()
	r.retired(seq)
	if err := r.stop(cancel, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	r.checkDelivered(seq)
	modes := r.gateway.Modes()
	if len(modes) != 2 || modes[0] != ingest.PrepareMode_PREPARE_NORMAL || modes[1] != ingest.PrepareMode_PREPARE_IMPORT_SEALED {
		t.Fatalf("prepare modes %v, want NORMAL then IMPORT_SEALED", modes)
	}
	calls := r.gateway.Calls()
	if count(calls, "SealImport") != 1 || count(calls, "FinalizePublish") != 0 {
		t.Fatalf("closed %d times normally and %d times by import; want the import", count(calls, "FinalizePublish"), count(calls, "SealImport"))
	}
	// The reservation is the original one: no sequence was allocated and
	// the gateway holds the seed it held before.
	if r.gateway.Reserved() != 1 || !bytes.Equal(r.gateway.Batch(seq).seed, original) {
		t.Fatalf("%d reservations and seed %x for the original %x; the replay must keep the reservation",
			r.gateway.Reserved(), r.gateway.Batch(seq).seed, original)
	}
}

func TestUnsealedPlanLostAtTheGatewayIsAbandoned(t *testing.T) {
	r := newRig(t)
	r.gateway.autoAck = true
	seq := r.stage(1, producer.ChunkBytes, blockOf(bigExtra))
	before := eventIDsOf(r, seq)
	// The first run dies inside the plan chunks: the second write of a
	// plan chunk fails and everything after it.
	planWrites := 0
	r.st.CrashWhen(func(op string, key []byte) bool {
		if op == "put" && bytes.HasPrefix(key, []byte("plan/")) && bytes.Contains(key, []byte("chunks/")) {
			planWrites++
			return planWrites == 2
		}
		return false
	})
	_, done := r.start()
	if err := r.runErr(done); !errors.Is(err, ingesttest.ErrCrash) {
		t.Fatalf("the first run ended with %v; want the simulated crash", err)
	}
	if generation, _, _, _, err := r.store.Plan(seq); r.batch(seq).State != producer.StateReady || err != nil || generation != 1 {
		t.Fatalf("after the crash: %+v, plan generation %d, %v; want READY with plan generation 1", r.batch(seq), generation, err)
	}
	// The gateway loses the reservation of the unsealed plan.
	r.st.Revive()
	r.gateway.Forget(seq)
	// The plan the second run seals is read when the gateway is asked to
	// publish it.
	var published uint64
	r.onStep = func(step string, seq uint64) {
		if step == "BeginPublish" {
			published = r.batch(seq).PlanGeneration
		}
	}
	cancel, done := r.start()
	r.retired(seq)
	if err := r.stop(cancel, done); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	r.checkDelivered(seq)
	if r.gateway.Reserved() != 2 {
		t.Fatalf("%d reservations; the lost one was not replaced", r.gateway.Reserved())
	}
	if published != 2 {
		t.Fatalf("plan generation %d, want a new generation after the abandoned one", published)
	}
	// The frames the gateway holds carry the event ids the READY batch
	// had before the crash.
	if again := frameEventIDs(r.gateway.Frames(seq)); again != before {
		t.Fatalf("the event ids changed from %x to %x", before, again)
	}
}

// frameEventIDs returns the distinct event ids the frames carry, sorted.
func frameEventIDs(frames [][]byte) string {
	seen := make(map[string]bool)
	for _, encoded := range frames {
		frame, err := proto.DecodeTransportFrame(encoded)
		if err != nil {
			return "undecodable frame: " + err.Error()
		}
		seen[string(frame.EventID[:])] = true
	}
	return sortedKeys(seen)
}

// eventIDsOf returns the event ids of the manifest of a batch, sorted.
func eventIDsOf(r *rig, seq uint64) string {
	r.t.Helper()
	events, err := decodeManifest(r.manifestBytes(seq))
	if err != nil {
		r.t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, event := range events {
		seen[string(event.EventID[:])] = true
	}
	return sortedKeys(seen)
}

func sortedKeys(seen map[string]bool) string {
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func TestTLSRefusesAnUnsignedClient(t *testing.T) {
	r := newRig(t)
	r.stage(1, producer.ChunkBytes, blockOf(nil))
	// A client certificate the CA did not sign never reaches the gateway.
	r.cfg.TLS.CertFile, r.cfg.TLS.KeyFile = r.identity.OtherCertFile, r.identity.OtherKeyFile
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	driver, err := NewDriver(DriverConfig{Client: r.cfg, Store: r.store, Storage: r.st})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Run(ctx); err != nil {
		t.Fatalf("Run ended with %v; a run ends without an error when its context does", err)
	}
	if calls := r.gateway.Calls(); len(calls) != 0 {
		t.Fatalf("the gateway handled %v from a client it must refuse", calls)
	}
}

func TestNewDriverValidatesItsInput(t *testing.T) {
	r := newRig(t)
	valid := DriverConfig{Client: r.cfg, Store: r.store, Storage: r.st}
	valid.Client.PollEvery = 0
	if driver, err := NewDriver(valid); err != nil || driver.cfg.PollEvery != DefaultPollEvery {
		t.Fatalf("a valid configuration without PollEvery: %v", err)
	}
	// broken changes one field of the valid configuration.
	broken := func(change func(cfg *DriverConfig)) DriverConfig {
		cfg := valid
		change(&cfg)
		return cfg
	}
	raised := r.binding
	raised.Generation++
	stated := r.binding
	stated.DescriptorDigest = [32]byte{1}
	otherNetwork := r.binding.Network
	otherNetwork.ChainID[0] ^= 1
	for name, cfg := range map[string]DriverConfig{
		"no store":                  broken(func(c *DriverConfig) { c.Store = nil }),
		"no storage":                broken(func(c *DriverConfig) { c.Storage = nil }),
		"negative interval":         broken(func(c *DriverConfig) { c.Client.PollEvery = -time.Second }),
		"unix endpoint":             broken(func(c *DriverConfig) { c.Client.Endpoint.Scheme = "unix" }),
		"no port":                   broken(func(c *DriverConfig) { c.Client.Endpoint.Port = "" }),
		"no host":                   broken(func(c *DriverConfig) { c.Client.Endpoint.Host = "" }),
		"tls disabled":              broken(func(c *DriverConfig) { c.Client.TLS.Mode = config.TLSDisabled }),
		"tls server only":           broken(func(c *DriverConfig) { c.Client.TLS.Mode = config.TLSServer }),
		"no key file":               broken(func(c *DriverConfig) { c.Client.TLS.KeyFile = "" }),
		"no ca file":                broken(func(c *DriverConfig) { c.Client.TLS.CAFile = "" }),
		"unknown family":            broken(func(c *DriverConfig) { c.Client.Network.Family = 0 }),
		"binding digest":            broken(func(c *DriverConfig) { c.Client.Binding = stated }),
		"another network":           broken(func(c *DriverConfig) { c.Client.Network = otherNetwork }),
		"binding not the saved one": broken(func(c *DriverConfig) { c.Client.Binding = raised }),
	} {
		if driver, err := NewDriver(cfg); !errors.Is(err, ErrConfig) || driver != nil {
			t.Fatalf("%s: NewDriver answered %v, %v; want ErrConfig", name, driver, err)
		}
	}
	// A broken file is found when the connection is made, not by
	// NewDriver, which performs no I/O. A file that cannot be loaded ends
	// the run at once as a configuration error; a CA that did not sign the
	// gateway keeps the run waiting, and nothing reaches the gateway.
	for name, change := range map[string]func(cfg *DriverConfig){
		"missing ca file":  func(c *DriverConfig) { c.Client.TLS.CAFile = c.Client.TLS.CAFile + ".missing" },
		"key does not fit": func(c *DriverConfig) { c.Client.TLS.KeyFile = r.identity.OtherKeyFile },
	} {
		driver, err := NewDriver(broken(change))
		if err != nil {
			t.Fatalf("%s: NewDriver reads no file: %v", name, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		if err := driver.Run(ctx); !errors.Is(err, ErrConfig) || config.Exit(err) != config.ExitConfig {
			t.Fatalf("%s: Run answered %v; want ErrConfig", name, err)
		}
		cancel()
		if len(r.gateway.Calls()) != 0 {
			t.Fatalf("%s: the gateway handled %v", name, r.gateway.Calls())
		}
	}
	driver, err := NewDriver(broken(func(c *DriverConfig) { c.Client.TLS.CAFile = r.identity.CertFile }))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := driver.Run(ctx); err != nil {
		t.Fatalf("Run with a CA that did not sign the gateway: %v; a run ends without an error when its context does", err)
	}
	if len(r.gateway.Calls()) != 0 {
		t.Fatalf("the gateway handled %v from a client of another CA", r.gateway.Calls())
	}
}

func TestReadyQueueRules(t *testing.T) {
	r := newRig(t)
	driver, err := NewDriver(DriverConfig{Client: r.cfg, Store: r.store, Storage: r.st})
	if err != nil {
		t.Fatal(err)
	}
	// Before the run every announcement is kept, each once.
	for i := 0; i < 2; i++ {
		if err := driver.Ready(1); err != nil {
			t.Fatal(err)
		}
	}
	for seq := uint64(2); seq <= announceBound; seq++ {
		if err := driver.Ready(seq); err != nil {
			t.Fatalf("announcement %d: %v", seq, err)
		}
	}
	if err := driver.Ready(announceBound + 1); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("announcement beyond the bound: %v, want ErrQueueFull", err)
	}
	// A second run on one driver is refused while it runs and after it
	// ended.
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() { ended <- driver.Run(ctx) }()
	waitUntil(t, "the run to begin", func() bool {
		driver.mu.Lock()
		defer driver.mu.Unlock()
		return driver.running
	})
	if err := driver.Run(ctx); !errors.Is(err, ErrDriverRunning) {
		t.Fatalf("a second Run while the first runs: %v, want ErrDriverRunning", err)
	}
	cancel()
	if err := <-ended; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := driver.Ready(1); !errors.Is(err, ErrDriverStopped) {
		t.Fatalf("announcement after the run: %v, want ErrDriverStopped", err)
	}
	if err := driver.Run(ctx); !errors.Is(err, ErrDriverStopped) {
		t.Fatalf("Run after the run ended: %v, want ErrDriverStopped", err)
	}
}

// A READY batch that no announcement named is found by the scan a driver
// makes at its start.
func TestScanFindsReadyBatchesNoAnnouncementNamed(t *testing.T) {
	r := newRig(t)
	r.gateway.autoAck = true
	seq := r.stage(1, producer.ChunkBytes, blockOf(nil))
	cancel, done := r.start()
	r.retired(seq)
	if err := r.stop(cancel, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	r.checkDelivered(seq)
}

func TestJournalDataThatDoesNotMatchStopsTheDriver(t *testing.T) {
	// A READY batch whose manifest names another event id than the one the
	// journal reserved for its ordinal.
	r := newRig(t)
	att, _, err := r.store.AllocateBatch(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	att, err = r.store.StartAttempt(att.BatchSeq, 1)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := ingesttest.BuildBlockEvent(0, proto.EventID(r.store.EventID(9999)), r.binding.Network, blockOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	builder := codec.NewManifestBuilder()
	if err := builder.AddEvent(foreign.Descriptor, foreign.FragmentDescriptors); err != nil {
		t.Fatal(err)
	}
	manifest, _ := builder.Bytes()
	var data []byte
	for _, fragment := range foreign.Fragments {
		data = append(data, fragment...)
	}
	if err := r.store.WriteAttemptData(att.BatchSeq, att.Generation, 0, data); err != nil {
		t.Fatal(err)
	}
	if err := r.store.WriteAttemptManifest(att.BatchSeq, att.Generation, 0, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.CommitReady(att.BatchSeq, att.Generation, uint64(len(manifest)), nil); err != nil {
		t.Fatal(err)
	}
	_, done := r.start()
	if err := r.runErr(done); !errors.Is(err, ErrJournalData) || config.Exit(err) != config.ExitFatal {
		t.Fatalf("a manifest of another event id: Run answered %v; want ErrJournalData of the fatal class", err)
	}
	if calls := r.gateway.Calls(); count(calls, "BeginPrepare") != 0 {
		t.Fatalf("a batch whose manifest does not match its attempt was sent: %v", calls)
	}
}
