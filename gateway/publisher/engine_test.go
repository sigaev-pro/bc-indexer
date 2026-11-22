package publisher

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

var (
	pProducer = proto.ID16{1}
	pRun      = proto.ID16{2}
	pEpoch    = proto.ID16{3}
)

// pBindingDigest returns the descriptor digest of the test binding; the
// batch identity and the fence carry the same digest the target verified.
var pBindingDigest = sync.OnceValue(func() proto.Hash32 {
	b := pTestBinding()
	digest, err := b.DescriptorDigestComputed()
	if err != nil {
		panic(err)
	}
	return digest
})

// pTestBinding builds the binding every fixture of this file shares.
func pTestBinding() destination.Binding {
	return destination.Binding{
		DestinationID: proto.ID16{7}, Generation: 1,
		Network:         proto.NetworkIdentity{Family: proto.NetworkFamilyEVM},
		ExpectedAccount: "IDX", Stream: "EXTRACTOR_V3", SubjectPrefix: "evm.v3.eth",
	}
}

func pIdentity(seq uint64) delivery.BatchIdentity {
	return delivery.BatchIdentity{
		Batch:                delivery.BatchID{Producer: pProducer, Run: pRun, Seq: seq},
		ManifestDigest:       proto.Hash32{byte(seq), 1},
		PlanDigest:           proto.Hash32{byte(seq), 2},
		LayoutManifestDigest: proto.Hash32{byte(seq), 3},
		BindingDigest:        pBindingDigest(),
		IssuerStoreEpoch:     pEpoch,
	}
}

func pFence() delivery.Fence {
	return delivery.Fence{SessionToken: []byte("t"), Producer: pProducer, Run: pRun,
		ConnectionGeneration: 1, AdmissionGeneration: 1, BindingDigest: pBindingDigest(),
		Kind: delivery.SessionNormal}
}

// pTarget builds a verified target matching pBinding.
func pTarget(t *testing.T) destination.VerifiedTarget {
	t.Helper()
	binding := pTestBinding()
	digest, err := binding.DescriptorDigestComputed()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	binding.DescriptorDigest = digest
	target, err := destination.NewVerifiedTarget(binding, []string{
		"evm.v3.eth.tx.applied", "evm.v3.eth.tx.removed", "evm.v3.eth.block.applied",
	}, 1<<20)
	if err != nil {
		t.Fatalf("NewVerifiedTarget: %v", err)
	}
	return target
}

// pFrames builds n immutable frames of one batch.
func pFrames(t *testing.T, seq uint64, subject string, n int) []Frame {
	t.Helper()
	eventID := proto.NewEventID(pProducer, pRun, 100+seq)
	frames := make([]Frame, 0, n)
	for i := 0; i < n; i++ {
		msgID, err := proto.NatsMsgID(proto.Hash32{5}, eventID, uint64(i), proto.FormComplete, 0)
		if err != nil {
			t.Fatalf("NatsMsgID: %v", err)
		}
		header, err := proto.NatsHeader(msgID, "EXTRACTOR_V3")
		if err != nil {
			t.Fatalf("NatsHeader: %v", err)
		}
		payload := []byte(fmt.Sprintf("frame-%d-payload", i))
		frames = append(frames, Frame{
			Key:     delivery.FrameKey{Ordinal: uint64(i), Fragment: 0, Complete: true},
			Subject: subject, MessageID: msgID, Header: header, Payload: payload,
		})
	}
	return frames
}

// pStore builds a memory gateway with the batch imported CLOSED.
func pStore(t *testing.T, seq uint64, frames []Frame) *delivery.MemoryGateway {
	t.Helper()
	g, err := delivery.NewMemoryGateway(pEpoch)
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := g.OpenSession(pFence()); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	keys := make([]delivery.FrameKey, len(frames))
	for i := range frames {
		keys[i] = frames[i].Key
	}
	if err := g.ImportClosed(pFence(), delivery.SealedPlan{Identity: pIdentity(seq),
		Stream: "EXTRACTOR_V3", FrameKeys: keys}, false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	return g
}

func pServer(t *testing.T) *MemoryJetStream {
	t.Helper()
	s, err := NewMemoryJetStream(StreamConfig{Name: "EXTRACTOR_V3",
		Subjects: []string{"evm.v3.eth.>"}, MaxMsgSize: 1 << 20})
	if err != nil {
		t.Fatalf("NewMemoryJetStream: %v", err)
	}
	return s
}

func TestPublishCompletesBatchWithIngestAck(t *testing.T) {
	frames := pFrames(t, 1, "evm.v3.eth.tx.applied", 3)
	store := pStore(t, 1, frames)
	server := pServer(t)
	engine, err := New(server, store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ack, err := engine.Publish(pFence(), pTarget(t), Batch{Identity: pIdentity(1), Frames: frames})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !ack.Identity.SameIdentity(pIdentity(1)) {
		t.Fatalf("IngestAck identity differs: %+v", ack)
	}
	record, ok := store.Batch(pFence(), pIdentity(1).Batch)
	if !ok || record.State != delivery.StateNATSACKed {
		t.Fatalf("state after publish: %+v ok=%v", record, ok)
	}
	if got := len(server.Messages()); got != 3 {
		t.Fatalf("stored %d messages, want 3", got)
	}
	// Idempotent completion: a second run returns the same durable ack
	// and publishes nothing new.
	again, err := engine.Publish(pFence(), pTarget(t), Batch{Identity: pIdentity(1), Frames: frames})
	if err != nil {
		t.Fatalf("re-Publish: %v", err)
	}
	if !again.Identity.SameIdentity(ack.Identity) {
		t.Fatalf("second ack differs")
	}
	if got := len(server.Messages()); got != 3 {
		t.Fatalf("re-publish stored %d messages, want 3", got)
	}
}

func TestPartialAckDoesNotCompleteBatch(t *testing.T) {
	frames := pFrames(t, 1, "evm.v3.eth.tx.applied", 4)
	store := pStore(t, 1, frames)
	server := pServer(t)
	// A transient failure on the third publish leaves the run incomplete.
	counting := &countingConn{Conn: server, after: 2}
	engine, _ := New(counting, store)
	_, err := engine.Publish(pFence(), pTarget(t), Batch{Identity: pIdentity(1), Frames: frames})
	var incomplete *ErrIncomplete
	if !errors.As(err, &incomplete) || incomplete.Missing != 2 {
		t.Fatalf("Publish error %v, want ErrIncomplete with 2 missing", err)
	}
	record, _ := store.Batch(pFence(), pIdentity(1).Batch)
	if record.State != delivery.StateClosed {
		t.Fatalf("partial run completed the batch to %s", record.State)
	}
	if len(record.Acked) != 2 {
		t.Fatalf("durable acks after partial run: %d, want 2", len(record.Acked))
	}
	// The retry completes the batch and republishes identical bytes: the
	// two stored frames dedup, the two missing frames store.
	ack, err := engine.Publish(pFence(), pTarget(t), Batch{Identity: pIdentity(1), Frames: frames})
	if err != nil {
		t.Fatalf("retry Publish: %v", err)
	}
	if !ack.Identity.SameIdentity(pIdentity(1)) {
		t.Fatalf("retry ack identity differs")
	}
	msgs := server.Messages()
	if len(msgs) != 4 {
		t.Fatalf("stored %d unique messages, want 4", len(msgs))
	}
	for i := range msgs {
		if msgs[i].Subject != "evm.v3.eth.tx.applied" {
			t.Fatalf("message %d published to %q", i, msgs[i].Subject)
		}
	}
}

func TestRetryPreservesIDBytesAndRoute(t *testing.T) {
	frames := pFrames(t, 1, "evm.v3.eth.block.applied", 2)
	store := pStore(t, 1, frames)
	server := pServer(t)
	engine, _ := New(server, store)
	server.SetAvailable(false)
	if _, err := engine.Publish(pFence(), pTarget(t), Batch{Identity: pIdentity(1), Frames: frames}); err == nil {
		t.Fatalf("offline publish succeeded")
	}
	server.SetAvailable(true)
	if _, err := engine.Publish(pFence(), pTarget(t), Batch{Identity: pIdentity(1), Frames: frames}); err != nil {
		t.Fatalf("retry publish: %v", err)
	}
	// No transform happened anywhere: the stored header and payload bytes
	// equal the frame bytes.
	msgs := server.Messages()
	if len(msgs) != 2 {
		t.Fatalf("stored %d messages, want 2", len(msgs))
	}
	for i, msg := range msgs {
		if string(msg.Header) != string(frames[i].Header) {
			t.Fatalf("message %d header changed", i)
		}
		if string(msg.Payload) != string(frames[i].Payload) {
			t.Fatalf("message %d payload changed", i)
		}
		if msg.MsgID != frames[i].MessageID {
			t.Fatalf("message %d id changed", i)
		}
	}
}

func TestPublishRejectsWrongTargetAndSession(t *testing.T) {
	frames := pFrames(t, 1, "evm.v3.eth.tx.applied", 1)
	store := pStore(t, 1, frames)
	server := pServer(t)
	engine, _ := New(server, store)
	target := pTarget(t)
	batch := Batch{Identity: pIdentity(1), Frames: frames}

	// Binding mismatch: acknowledgments of different destinations never
	// compose one NATS_ACKED.
	otherBinding := target
	otherBinding.Binding.Generation = 2
	digest, err := otherBinding.Binding.DescriptorDigestComputed()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	otherBinding.Binding.DescriptorDigest = digest
	if _, err := engine.Publish(pFence(), otherBinding, batch); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("binding mismatch: %v", err)
	}
	// Route outside the verified target.
	wrongRoute := Batch{Identity: pIdentity(1), Frames: pFrames(t, 1, "evm.v3.bsc.tx.applied", 1)}
	if _, err := engine.Publish(pFence(), target, wrongRoute); !errors.Is(err, ErrRouteOutsideTarget) {
		t.Fatalf("route outside target: %v", err)
	}
	// A MAINTENANCE session never publishes.
	maint := pFence()
	maint.Kind = delivery.SessionMaintenance
	if _, err := engine.Publish(maint, target, batch); !errors.Is(err, delivery.ErrFenced) {
		t.Fatalf("maintenance session: %v", err)
	}
	// No message reached the server through any rejected path.
	if got := len(server.Messages()); got != 0 {
		t.Fatalf("rejected paths published %d messages", got)
	}
}

func TestPublishVerifiesPubAckStream(t *testing.T) {
	frames := pFrames(t, 1, "evm.v3.eth.tx.applied", 1)
	store := pStore(t, 1, frames)
	// The server routes the subject to a differently named stream.
	server, err := NewMemoryJetStream(StreamConfig{Name: "OTHER_STREAM",
		Subjects: []string{"evm.v3.eth.>"}, MaxMsgSize: 1 << 20})
	if err != nil {
		t.Fatalf("NewMemoryJetStream: %v", err)
	}
	// The frame header expects EXTRACTOR_V3; routing disagrees first.
	engine, _ := New(server, store)
	if _, err := engine.Publish(pFence(), pTarget(t), Batch{Identity: pIdentity(1), Frames: frames}); !errors.Is(err, ErrUnexpectedStream) {
		t.Fatalf("expected-stream mismatch: %v", err)
	}
	// A PubAck of another stream is rejected even when the header agrees:
	// rewrite the header to name the other stream.
	msgID, _ := proto.NatsMsgID(proto.Hash32{5}, proto.NewEventID(pProducer, pRun, 101), 0, proto.FormComplete, 0)
	header, _ := proto.NatsHeader(msgID, "OTHER_STREAM")
	frames2 := []Frame{{Key: frames[0].Key, Subject: "evm.v3.eth.tx.applied",
		MessageID: msgID, Header: header, Payload: []byte("p")}}
	if _, err := engine.Publish(pFence(), pTarget(t), Batch{Identity: pIdentity(1), Frames: frames2}); !errors.Is(err, ErrUnexpectedStream) {
		t.Fatalf("PubAck stream mismatch: %v", err)
	}
}

// countingConn fails one publish after n successes with a transient
// error, then keeps working.
type countingConn struct {
	Conn
	after  int
	ok     int
	failed bool
}

func (c *countingConn) Publish(subject string, header, payload []byte) (PubAck, error) {
	if c.ok == c.after && !c.failed {
		c.failed = true
		return PubAck{}, fmt.Errorf("%w: planned failure", ErrTransient)
	}
	ack, err := c.Conn.Publish(subject, header, payload)
	if err == nil {
		c.ok++
	}
	return ack, err
}

// barrierConn triggers a barrier after n successful publishes, modeling a
// Release that passes while frames are still in flight. The trigger is
// bound after the engine exists, because the engine owns the barrier.
type barrierConn struct {
	Conn
	after   int
	ok      int
	trigger func()
}

func (c *barrierConn) Publish(subject string, header, payload []byte) (PubAck, error) {
	ack, err := c.Conn.Publish(subject, header, payload)
	if err == nil {
		c.ok++
		if c.ok == c.after && c.trigger != nil {
			c.trigger()
		}
	}
	return ack, err
}

func TestBarrierFencesInFlightPublish(t *testing.T) {
	frames := pFrames(t, 1, "evm.v3.eth.tx.applied", 3)
	store := pStore(t, 1, frames)
	server := pServer(t)
	barriered := &barrierConn{Conn: server, after: 1}
	engine, _ := New(barriered, store)
	barriered.trigger = func() { engine.Barrier() }
	_, err := engine.Publish(pFence(), pTarget(t), Batch{Identity: pIdentity(1), Frames: frames})
	if !errors.Is(err, delivery.ErrFenced) {
		t.Fatalf("mid-run barrier: %v, want ErrFenced", err)
	}
	record, _ := store.Batch(pFence(), pIdentity(1).Batch)
	if record.State != delivery.StateClosed {
		t.Fatalf("a fenced run advanced the state to %s", record.State)
	}
	// A fresh generation completes the batch.
	_, err = engine.Publish(pFence(), pTarget(t), Batch{Identity: pIdentity(1), Frames: frames})
	if err != nil {
		t.Fatalf("a fresh generation completes the batch: %v", err)
	}
}

func TestPublishRetiredSequenceCreatesNoKeys(t *testing.T) {
	frames := pFrames(t, 1, "evm.v3.eth.tx.applied", 1)
	store := pStore(t, 1, frames)
	server := pServer(t)
	engine, _ := New(server, store)
	// Complete and release the batch, then replay an old publish.
	if _, err := engine.Publish(pFence(), pTarget(t), Batch{Identity: pIdentity(1), Frames: frames}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := store.BeginRelease(pFence(), pIdentity(1).Batch); err != nil {
		t.Fatalf("BeginRelease: %v", err)
	}
	for {
		more, err := store.CleanupStep(pFence(), pIdentity(1).Batch)
		if err != nil {
			t.Fatalf("CleanupStep: %v", err)
		}
		if !more {
			break
		}
	}
	if _, err := store.FinishRelease(pFence(), pIdentity(1).Batch); err != nil {
		t.Fatalf("FinishRelease: %v", err)
	}
	before := len(server.Messages())
	if _, err := engine.Publish(pFence(), pTarget(t), Batch{Identity: pIdentity(1), Frames: frames}); !errors.Is(err, delivery.ErrAlreadyRetired) {
		t.Fatalf("retired publish: %v, want ErrAlreadyRetired", err)
	}
	if got := len(server.Messages()); got != before {
		t.Fatalf("retired republish sent %d new messages", got-before)
	}
}

func TestFramesForPlanDerivesPartKeys(t *testing.T) {
	entries := []proto.LayoutManifestEntry{
		{Ordinal: 0, FragmentIndex: 0, Form: proto.FormComplete, Capacity: 10, PartCount: 1, FragmentTotalBytes: 10},
		{Ordinal: 1, FragmentIndex: 0, Form: proto.FormPart, Capacity: 4, PartCount: 2, FragmentTotalBytes: 7},
		{Ordinal: 1, FragmentIndex: 1, Form: proto.FormPart, Capacity: 4, PartCount: 3, FragmentTotalBytes: 11},
	}
	identity := pIdentity(1)
	frames, err := FramesForPlan(identity, entries, func(i int) (string, string, []byte, []byte) {
		return "s", fmt.Sprintf("id-%d", i), []byte("h"), []byte("p")
	})
	if err != nil {
		t.Fatalf("FramesForPlan: %v", err)
	}
	// The part indices restart per (ordinal, fragment) pair.
	if frames[0].Key.Complete != true || frames[0].Key.Part != 0 {
		t.Fatalf("complete key %+v", frames[0].Key)
	}
	if frames[1].Key.Part != 0 || frames[1].Key.Complete {
		t.Fatalf("first part key %+v", frames[1].Key)
	}
	if frames[2].Key.Part != 0 || frames[2].Key.Fragment != 1 {
		t.Fatalf("next fragment part key %+v", frames[2].Key)
	}
	for i := range frames {
		if frames[i].Subject != "s" || frames[i].MessageID != fmt.Sprintf("id-%d", i) {
			t.Fatalf("frame %d lost its immutable identity", i)
		}
	}
	// An invalid identity is rejected before any frame is built.
	bad := identity
	bad.IssuerStoreEpoch = proto.ID16{}
	if _, err := FramesForPlan(bad, entries, func(int) (string, string, []byte, []byte) { return "", "", nil, nil }); err == nil {
		t.Fatalf("invalid identity accepted")
	}
}
