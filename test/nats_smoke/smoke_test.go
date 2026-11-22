package nats_smoke

import (
	"context"
	"errors"
	"fmt"
	sort "sort"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// smokeFence builds the session fence of the corpus run.
func smokeFence(bindingDigest proto.Hash32) delivery.Fence {
	return delivery.Fence{SessionToken: []byte("smoke"), Producer: smokeProducer, Run: smokeRun,
		ConnectionGeneration: 1, AdmissionGeneration: 1,
		BindingDigest: bindingDigest, Kind: delivery.SessionNormal}
}

// smokeEnvironment builds the verified target and the in-memory server of
// the corpus.
func smokeEnvironment(t *testing.T, binding destination.Binding) (destination.Remote, *publisher.MemoryJetStream, destination.VerifiedTarget) {
	t.Helper()
	networkDigest, err := binding.NetworkDigest()
	if err != nil {
		t.Fatalf("NetworkDigest: %v", err)
	}
	stream := destination.StreamInfo{
		Name: binding.Stream, Subjects: []string{binding.SubjectPrefix + ".>"},
		Metadata: map[string]string{
			destination.MetadataNetworkDigest: hexOf(networkDigest[:]),
			destination.MetadataDestinationID: hexOf(binding.DestinationID[:]),
		},
		MaxMsgSize: smokeBudget,
	}
	remote, err := destination.NewMemoryRemote(binding.ExpectedAccount, stream)
	if err != nil {
		t.Fatalf("NewMemoryRemote: %v", err)
	}
	server, err := publisher.NewMemoryJetStream(publisher.StreamConfig{
		Name: binding.Stream, Subjects: []string{binding.SubjectPrefix + ".>"},
		MaxMsgSize: smokeBudget,
	})
	if err != nil {
		t.Fatalf("NewMemoryJetStream: %v", err)
	}
	subjects := make([]string, 0, 6)
	for _, family := range []proto.Family{proto.FamilyBlock, proto.FamilyTx} {
		for _, kind := range []proto.Kind{proto.KindApplied, proto.KindRemoved, proto.KindStatus} {
			s, err := destination.Subject(binding.SubjectPrefix, family, kind)
			if err != nil {
				t.Fatalf("Subject: %v", err)
			}
			subjects = append(subjects, s)
		}
	}
	if report := destination.Check(context.Background(), remote, binding, subjects, smokeBudget); !report.OK() {
		t.Fatalf("destination check failed: %+v", report)
	}
	target, err := destination.NewVerifiedTarget(binding, subjects, smokeBudget)
	if err != nil {
		t.Fatalf("NewVerifiedTarget: %v", err)
	}
	return remote, server, target
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// planSmoke builds the planned corpus batch bound to the verified binding.
func planSmoke(t *testing.T) (*plannedCorpus, destination.Binding, proto.Hash32) {
	t.Helper()
	binding, err := smokeBinding()
	if err != nil {
		t.Fatalf("smokeBinding: %v", err)
	}
	events, err := smokeCorpus(smokeProducer, smokeRun, 1000)
	if err != nil {
		t.Fatalf("smokeCorpus: %v", err)
	}
	planDigest := smokePlanDigest()
	planned, err := planCorpus(events, 1000, planDigest)
	if err != nil {
		t.Fatalf("planCorpus: %v", err)
	}
	planned.identity.BindingDigest = binding.DescriptorDigest
	return planned, binding, planDigest
}

func TestSmokeCorpusPublishesAndReassembles(t *testing.T) {
	planned, binding, _ := planSmoke(t)
	_, server, target := smokeEnvironment(t, binding)
	fence := smokeFence(binding.DescriptorDigest)

	gw, err := delivery.NewMemoryGateway(smokeEpoch)
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := gw.OpenSession(fence); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	keys := make([]delivery.FrameKey, len(planned.frames))
	for i := range planned.frames {
		keys[i] = planned.frames[i].Key
	}
	if err := gw.ImportClosed(fence, delivery.SealedPlan{Identity: planned.identity,
		SequenceBase: 1000, Stream: smokeStream, FrameKeys: keys}, false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	engine, err := publisher.New(server, gw)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ack, err := engine.Publish(fence, target, publisher.Batch{Identity: planned.identity, Frames: planned.frames})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !ack.Identity.SameIdentity(planned.identity) {
		t.Fatalf("IngestAck identity differs from the plan")
	}

	msgs := server.Messages()
	if len(msgs) != len(planned.frames) {
		t.Fatalf("stored %d messages for %d planned frames", len(msgs), len(planned.frames))
	}

	// Every message id follows the v2 format and every subject is the
	// event's route.
	byMsgID := make(map[string]publisher.MemoryMessage, len(msgs))
	for _, msg := range msgs {
		if len(msg.MsgID) != 123 || msg.MsgID[:4] != proto.NatsMsgIDPrefix {
			t.Fatalf("message id %q is not a v2 id", msg.MsgID)
		}
		byMsgID[msg.MsgID] = msg
	}
	subjectOfEvent := func(e *corpusEvent) string {
		s, err := destination.Subject(smokePrefix, e.family, e.kind)
		if err != nil {
			t.Fatalf("Subject: %v", err)
		}
		return s
	}
	for i := range planned.frames {
		frame := &planned.frames[i]
		msg, ok := byMsgID[frame.MessageID]
		if !ok {
			t.Fatalf("frame %s never reached the stream", frame.Key)
		}
		if msg.Subject != frame.Subject {
			t.Fatalf("frame %s published to %q, planned %q", frame.Key, msg.Subject, frame.Subject)
		}
		if msg.Stream != smokeStream {
			t.Fatalf("frame %s acked by stream %q", frame.Key, msg.Stream)
		}
	}

	// The corpus covers both transport forms: the large blob splits into
	// parts, the small fragments stay complete.
	forms := map[proto.Form]int{}
	for _, entry := range planned.entries {
		forms[entry.Form]++
	}
	if forms[proto.FormPart] == 0 {
		t.Fatalf("the corpus produced no PART frames; the smoke budget lost its meaning")
	}
	if forms[proto.FormComplete] == 0 {
		t.Fatalf("the corpus produced no COMPLETE frames")
	}

	// Independent oracle: decode every stored payload and reassemble each
	// record from its parts; the bytes must equal the corpus fragments.
	fragmentsOf := map[proto.EventID][][]byte{}
	kindsOf := map[proto.EventID]*corpusEvent{}
	for _, event := range planned.events {
		fragmentsOf[event.descriptor.EventID] = event.fragments
		kindsOf[event.descriptor.EventID] = event
	}
	type partKey struct {
		event    proto.EventID
		fragment uint64
	}
	parts := map[partKey]map[uint64][]byte{}
	for _, msg := range msgs {
		frame, err := proto.DecodeTransportFrame(msg.Payload)
		if err != nil {
			t.Fatalf("stored payload of %s does not decode: %v", msg.MsgID, err)
		}
		key := partKey{event: frame.EventID, fragment: frame.FragmentIndex}
		if parts[key] == nil {
			parts[key] = make(map[uint64][]byte)
		}
		if frame.Complete != nil {
			parts[key][0] = frame.Complete.FragmentBytes
		} else {
			parts[key][frame.Part.PartIndex] = frame.Part.Data
		}
	}
	for key, byPart := range parts {
		want := fragmentsOf[key.event]
		if int(key.fragment) >= len(want) {
			t.Fatalf("fragment %d of event %x outside the corpus", key.fragment, key.event)
		}
		var got []byte
		indexes := make([]int, 0, len(byPart))
		for idx := range byPart {
			indexes = append(indexes, int(idx))
		}
		sort.Ints(indexes)
		for _, idx := range indexes {
			got = append(got, byPart[uint64(idx)]...)
		}
		if string(got) != string(want[key.fragment]) {
			t.Fatalf("event %x fragment %d: reassembled %d bytes, corpus fragment is %d bytes",
				key.event, key.fragment, len(got), len(want[key.fragment]))
		}
	}

	// The empty block keeps its own event and route: its record reaches
	// the block applied subject with the descriptor's digest.
	var emptyBlock *corpusEvent
	for _, event := range planned.events {
		if event.family == proto.FamilyBlock && event.kind == proto.KindApplied &&
			len(event.nodes[2].Value) == 0 {
			emptyBlock = event
		}
	}
	if emptyBlock == nil {
		t.Fatalf("the corpus lost its empty block event")
	}
	emptySubject := subjectOfEvent(emptyBlock)
	found := false
	for _, msg := range msgs {
		if msg.Subject != emptySubject {
			continue
		}
		frame, err := proto.DecodeTransportFrame(msg.Payload)
		if err != nil {
			t.Fatalf("empty block payload does not decode: %v", err)
		}
		if frame.EventID != emptyBlock.descriptor.EventID {
			continue
		}
		if frame.RecordDigest != emptyBlock.descriptor.RecordDigest {
			t.Fatalf("empty block record digest mismatch")
		}
		found = true
	}
	if !found {
		t.Fatalf("the empty block event never reached %q", emptySubject)
	}

	// The lifecycle events are present with their own routes.
	for _, want := range []struct {
		family proto.Family
		kind   proto.Kind
	}{{proto.FamilyTx, proto.KindRemoved}, {proto.FamilyTx, proto.KindStatus}} {
		subject, err := destination.Subject(smokePrefix, want.family, want.kind)
		if err != nil {
			t.Fatalf("Subject: %v", err)
		}
		seen := false
		for _, msg := range msgs {
			if msg.Subject == subject {
				seen = true
			}
		}
		if !seen {
			t.Fatalf("lifecycle route %q never used", subject)
		}
	}

	// A retry of the completed batch republishes identical bytes: the
	// stream dedups them and the IngestAck stays the same.
	retryAck, err := engine.Publish(fence, target, publisher.Batch{Identity: planned.identity, Frames: planned.frames})
	if err != nil {
		t.Fatalf("retry Publish: %v", err)
	}
	if !retryAck.Identity.SameIdentity(ack.Identity) {
		t.Fatalf("retry ack differs")
	}
	if got := len(server.Messages()); got != len(planned.frames) {
		t.Fatalf("retry stored %d unique messages, want %d", got, len(planned.frames))
	}
}

// TestSmokeOneAckIsNotTheBatch proves the rule the card pins: a single
// transport ACK (or a partial PubAck set) never completes the batch.
func TestSmokeOneAckIsNotTheBatch(t *testing.T) {
	planned, binding, _ := planSmoke(t)
	_, server, target := smokeEnvironment(t, binding)
	fence := smokeFence(binding.DescriptorDigest)

	gw, err := delivery.NewMemoryGateway(smokeEpoch)
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := gw.OpenSession(fence); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	keys := make([]delivery.FrameKey, len(planned.frames))
	for i := range planned.frames {
		keys[i] = planned.frames[i].Key
	}
	if err := gw.ImportClosed(fence, delivery.SealedPlan{Identity: planned.identity,
		SequenceBase: 1000, Stream: smokeStream, FrameKeys: keys}, false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	// Fail the publish after several successful frames: many PubAcks
	// exist, the batch is still not NATS_ACKED.
	counting := &failingConn{Conn: server, after: 3}
	engine, err := publisher.New(counting, gw)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = engine.Publish(fence, target, publisher.Batch{Identity: planned.identity, Frames: planned.frames})
	var incomplete *publisher.ErrIncomplete
	if !errors.As(err, &incomplete) || incomplete.Missing == 0 {
		t.Fatalf("planned failure returned %v, want ErrIncomplete", err)
	}
	record, ok := gw.Batch(fence, planned.identity.Batch)
	if !ok || record.State != delivery.StateClosed {
		t.Fatalf("partial run moved the batch to %s", record.State)
	}
	if got := counting.successes(); got == 0 {
		t.Fatalf("the run never acknowledged a frame; the fixture is vacuous")
	}
	if got := len(server.Messages()); got != counting.successes() {
		t.Fatalf("server holds %d messages for %d acked frames", got, counting.successes())
	}
	// The retry completes the batch and dedups the already-stored frames.
	complete, err := publisher.New(server, gw)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ack, err := complete.Publish(fence, target, publisher.Batch{Identity: planned.identity, Frames: planned.frames})
	if err != nil {
		t.Fatalf("retry Publish: %v", err)
	}
	if !ack.Identity.SameIdentity(planned.identity) {
		t.Fatalf("retry ack differs")
	}
	if got := len(server.Messages()); got != len(planned.frames) {
		t.Fatalf("retry stored %d unique messages, want %d", got, len(planned.frames))
	}
}

// failingConn fails once after n successful publishes.
type failingConn struct {
	publisher.Conn
	after, ok int
	failed    bool
}

func (c *failingConn) Publish(subject string, header, payload []byte) (publisher.PubAck, error) {
	if !c.failed && c.ok >= c.after {
		c.failed = true
		return publisher.PubAck{}, fmt.Errorf("planned transient failure")
	}
	ack, err := c.Conn.Publish(subject, header, payload)
	if err == nil {
		c.ok++
	}
	return ack, err
}

func (c *failingConn) successes() int { return c.ok }
