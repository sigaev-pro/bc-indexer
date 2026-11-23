// E07-09.nats_acceptance (#76): the final NATS record validation. The
// independent reader takes the stored messages of each network's final
// corpus and validates the whole wire surface: every payload decodes as a
// TransportFrame, every Nats-Msg-Id is unique, well-formed and equal to
// the identity recomputed from its own frame, the header bytes are the
// exact canonical form, every record's fragments reassemble under the
// bounded reader with matching digests, the lifecycle event order in the
// stream is the legal native order, the empty block keeps its metadata,
// and an exact retry of published bytes stays one physical message
// (TASK_DECOMPOSITION E07-09.nats_acceptance; MSG, CONTROL, EVID;
// REQ-DLV-04, REQ-LIF-03).
package acceptance

import (
	"bytes"
	"testing"

	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
	lifecycle "github.com/sigaev-pro/bc-indexer/test/lifecycle_validator"
)

// natsScenario admits the final corpus of one slot: a dense block whose
// every third transaction splits into PART frames, an empty block, a
// system-metadata block, a reorg of the dense block and a finality-only
// decision — the full record-shape coverage of the final acceptance read.
func natsScenario(t testingT, h *acceptanceStack) []decisionSpec {
	t.Helper()
	admit := func(b blockFixture) decisionSpec {
		d, err := h.source.admitLive(b)
		if err != nil {
			t.Fatalf("slot %s: admit height %d: %v", h.slot.name, b.height, err)
		}
		return d
	}
	decisions := []decisionSpec{
		admit(fixtureBlock(500, 6, 1)),
		admit(emptyFixtureBlock(501)),
		admit(systemFixtureBlock(502, 1)),
	}
	pair, err := h.source.reorgAt(500, reorgFixtureBlock(500, 2, 0))
	if err != nil {
		t.Fatalf("slot %s: reorg: %v", h.slot.name, err)
	}
	decisions = append(decisions, pair...)
	status, err := h.source.finalizeAt(502)
	if err != nil {
		t.Fatalf("slot %s: finalize: %v", h.slot.name, err)
	}
	return append(decisions, status)
}

// TestNatsAcceptanceFinalRecordsPerNetwork is the final record verdict of
// each network's corpus.
func TestNatsAcceptanceFinalRecordsPerNetwork(t *testing.T) {
	for _, slot := range networkSlots {
		t.Run(slot.name, func(t *testing.T) {
			server, err := publisher.NewMemoryJetStream(publisher.StreamConfig{
				Name: slot.stream, Subjects: []string{slot.prefix + ".>"},
				MaxMsgSize: acceptanceBudget,
			})
			if err != nil {
				t.Fatalf("server: %v", err)
			}
			h := newStack(slot, server, 500, 520)
			decisions := natsScenario(t, h)
			h.deliverAll(t, server)

			msgs := messagesOf(server, slot)
			if len(msgs) == 0 {
				t.Fatalf("slot %s: the final corpus published nothing", slot.name)
			}

			// 1. Every stored payload decodes and validates as a
			// TransportFrame, and both transport forms occur.
			forms := map[proto.Form]int{}
			frames := make([]*proto.TransportFrame, 0, len(msgs))
			for _, msg := range msgs {
				frame, err := proto.DecodeTransportFrame(msg.Payload)
				if err != nil {
					t.Fatalf("slot %s: %s does not decode: %v", slot.name, msg.MsgID, err)
				}
				if err := frame.Validate(); err != nil {
					t.Fatalf("slot %s: frame of %s is invalid: %v", slot.name, msg.MsgID, err)
				}
				frames = append(frames, frame)
				if frame.Complete != nil {
					forms[proto.FormComplete]++
				} else {
					forms[proto.FormPart]++
				}
			}
			if forms[proto.FormPart] == 0 || forms[proto.FormComplete] == 0 {
				t.Fatalf("slot %s: the corpus lost a transport form: %+v", slot.name, forms)
			}

			// 2. Nats-Msg-Id: unique, of the v2 shape, and equal to the
			// identity recomputed from the frame's own fields; the header
			// bytes are the canonical NATS header of that id and stream.
			networkDigest, err := h.binding.NetworkDigest()
			if err != nil {
				t.Fatalf("network digest: %v", err)
			}
			byID := make(map[string]publisher.MemoryMessage, len(msgs))
			for i, msg := range msgs {
				if len(msg.MsgID) != 123 || msg.MsgID[:4] != proto.NatsMsgIDPrefix {
					t.Fatalf("slot %s: message id %q is not a v2 id", slot.name, msg.MsgID)
				}
				if _, held := byID[msg.MsgID]; held {
					t.Fatalf("slot %s: Nats-Msg-Id %q is not unique", slot.name, msg.MsgID)
				}
				byID[msg.MsgID] = msg
				frame := frames[i]
				form, part := proto.FormComplete, uint64(0)
				if frame.Part != nil {
					form, part = proto.FormPart, frame.Part.PartIndex
				}
				recomputed, err := proto.NatsMsgID(networkDigest, frame.EventID,
					frame.FragmentIndex, form, part)
				if err != nil {
					t.Fatalf("slot %s: recompute id of %s: %v", slot.name, msg.MsgID, err)
				}
				if recomputed != msg.MsgID {
					t.Fatalf("slot %s: stored id %s differs from its own frame's id %s",
						slot.name, msg.MsgID, recomputed)
				}
				header, err := proto.NatsHeader(msg.MsgID, slot.stream)
				if err != nil {
					t.Fatalf("slot %s: canonical header: %v", slot.name, err)
				}
				if !bytes.Equal(header, msg.Header) {
					t.Fatalf("slot %s: header bytes of %s are not the canonical form", slot.name, msg.MsgID)
				}
				// The wrapper identity the reader pins: one network, one
				// record digest per event, one chain sequence per frame.
				if frame.Network != protoNet(slot) {
					t.Fatalf("slot %s: frame of %s carries another network identity", slot.name, msg.MsgID)
				}
			}

			// 3. Record digests: every frame of one event agrees on the
			// digest; the reassembled fragments hash to it.
			report := readStream(t, server, slot)
			verifyStream(t, server, slot, h.source).mustBeClean(t, slot)
			digests := make(map[proto.EventID]proto.Hash32)
			for _, frame := range frames {
				if held, ok := digests[frame.EventID]; ok && held != frame.RecordDigest {
					t.Fatalf("slot %s: event %x carries two record digests", slot.name, frame.EventID)
				}
				digests[frame.EventID] = frame.RecordDigest
			}
			for eventID := range report.records {
				if _, ok := digests[eventID]; !ok {
					t.Fatalf("slot %s: rebuilt record %x has no frame digest", slot.name, eventID)
				}
			}
			// The reader reassembled every event through the bounded
			// FragmentReader, whose Finish verifies the record digest
			// over the content fragments; re-derive it independently per
			// event from the raw messages.
			grouped := groupByEvent(msgs)
			for eventID, eventMsgs := range grouped {
				reader := codec.NewFragmentReader(codec.ReaderOptions{})
				for _, msg := range eventMsgs {
					if err := reader.Add(msg.Payload); err != nil {
						t.Fatalf("slot %s: event %x rejects its frame: %v", slot.name, eventID, err)
					}
				}
				fragments, err := reader.Finish()
				if err != nil {
					t.Fatalf("slot %s: event %x does not finish: %v", slot.name, eventID, err)
				}
				digest, err := proto.RecordDigest(fragments)
				if err != nil {
					t.Fatalf("slot %s: digest of %x: %v", slot.name, eventID, err)
				}
				if digest != digests[eventID] {
					t.Fatalf("slot %s: event %x digest mismatch between frames and reassembly",
						slot.name, eventID)
				}
			}

			// 4. Lifecycle event ordering in stream order: every Applied
			// precedes the Removed/Status events of its activation; the
			// Removed of the reorged activation precedes the replacement
			// Applied; the independent automaton accepts the sequence.
			assertStreamLifecycleOrder(t, slot, msgs, decisions)
			assertLifecycleAccepted(t, h, decisions)

			// 5. The empty block keeps its metadata: exactly one block
			// record, zero transactions, no invented optional field.
			assertEmptyBlockMetadata(t, slot, report, decisions[1])

			// 6. An exact retry of published bytes stays one physical
			// message: the shared server dedups by identity, and the
			// identical bytes never create a second logical event.
			var probe publisher.MemoryMessage = msgs[0]
			before := len(server.Messages())
			if _, err := server.Publish(probe.Subject, probe.Header, probe.Payload); err != nil {
				t.Fatalf("slot %s: exact retry publish: %v", slot.name, err)
			}
			if got := len(server.Messages()); got != before {
				t.Fatalf("slot %s: the exact retry stored a second copy (%d -> %d)",
					slot.name, before, got)
			}
		})
	}
}

// TestNatsAcceptanceRejectsLifecycleViolations is the negative control of
// the ordering verdict: the reader-side rules reject the violations the
// product must never publish — a zero revision on a lifecycle event, a
// conflicting duplicate revision, and an activation identity clash.
func TestNatsAcceptanceRejectsLifecycleViolations(t *testing.T) {
	slot := networkSlots[0]
	h := newAcceptanceStackForViolations(t, slot)

	// A StatusChanged or Removed descriptor with revision 0 is invalid
	// input: the lifecycle allocation is dense and positive.
	applied, err := h.source.admitLive(fixtureBlock(600, 1, 0))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	descs, err := h.descriptors(applied)
	if err != nil {
		t.Fatalf("descriptors: %v", err)
	}
	for _, kind := range []semantic.Kind{semantic.KindStatus, semantic.KindRemoved} {
		mutated := descs[0]
		mutated.Kind, mutated.Revision = kind, 0
		if err := mutated.Validate(); err == nil {
			t.Fatalf("a %s descriptor with revision 0 passed validation", kind)
		}
	}

	automaton := lifecycle.New()
	content0 := lifecycle.Digest(firstDigestOf(t, descs))
	result, err := automaton.Apply(descs[0], content0)
	if err != nil || result.Class != lifecycle.ClassAccepted {
		t.Fatalf("the applied event was not accepted: %+v %v", result, err)
	}
	// The body transaction's own activation is applied too, so the clash
	// below hits an existing record.
	if len(descs) < 2 {
		t.Fatalf("the violation fixture lost its body transaction")
	}
	txContent := lifecycle.Digest(firstDigestOf(t, descs[1:2]))
	if result, err := automaton.Apply(descs[1], txContent); err != nil ||
		result.Class != lifecycle.ClassAccepted {
		t.Fatalf("the transaction event was not accepted: %+v %v", result, err)
	}
	// The same revision with different bytes is a silent-overwrite
	// attempt: a conflict, never a pass.
	if _, err := automaton.Apply(descs[0], lifecycle.Digest([32]byte{0xde})); err != nil {
		t.Fatalf("the conflicting duplicate errored instead of conflicting: %v", err)
	} else if result, err := automaton.Apply(descs[0], lifecycle.Digest([32]byte{0xde})); err != nil ||
		result.Class != lifecycle.ClassConflict {
		t.Fatalf("the conflicting duplicate was not rejected: %+v %v", result, err)
	}
	// An activation identity clash — the same activation key under a
	// different entity signature (another transaction hash) — is a
	// conflict, never a silent overwrite.
	foreignTx := applied.block.txs[0].hash
	foreignTx[0] ^= 0xff
	clash, err := semantic.NewTransactionDescriptor(semantic.KindApplied, semNet(slot),
		semID(slot.producer), semID(slot.run), applied.sequence, semHash(applied.block.hash),
		semHash(foreignTx), 0, 0, semantic.SourceRef{NodeInstanceID: semID(slot.node),
			WriterEpoch: semID(slot.writer), SourceSequence: 9_999})
	if err != nil {
		t.Fatalf("clash descriptor: %v", err)
	}
	if _, err := automaton.Apply(clash, lifecycle.Digest([32]byte{0xab})); err != nil {
		t.Fatalf("the identity clash errored instead of conflicting: %v", err)
	} else if result, err := automaton.Apply(clash, lifecycle.Digest([32]byte{0xab})); err != nil ||
		result.Class != lifecycle.ClassConflict {
		t.Fatalf("the activation identity clash was not rejected: %+v %v", result, err)
	}
}

// assertStreamLifecycleOrder verifies the stream-sequence order of the
// lifecycle events: for every activation, the Applied message precedes
// every Removed/Status message that references it, and the removal of the
// reorged activation precedes the replacement application.
func assertStreamLifecycleOrder(t *testing.T, slot networkSlot, msgs []publisher.MemoryMessage, decisions []decisionSpec) {
	t.Helper()
	// firstSeen: the stream sequence of the first message of each event.
	firstSeen := make(map[proto.EventID]uint64)
	for _, msg := range msgs {
		frame, err := proto.DecodeTransportFrame(msg.Payload)
		if err != nil {
			t.Fatalf("slot %s: %s does not decode: %v", slot.name, msg.MsgID, err)
		}
		if _, held := firstSeen[frame.EventID]; !held {
			firstSeen[frame.EventID] = msg.Sequence
		}
	}
	// activationEvents maps each activation (the applying decision) to
	// its Applied event and the lifecycle events that reference it.
	type activationRefs struct {
		applied proto.EventID
		later   []proto.EventID
	}
	activations := make(map[uint64]*activationRefs)
	for _, d := range decisions {
		if d.kind == semantic.KindApplied {
			activations[d.sequence] = &activationRefs{
				applied: proto.NewEventID(slot.producer, slot.run, eventSequence(d, 0))}
			continue
		}
		target, held := activations[d.target]
		if !held {
			t.Fatalf("slot %s: lifecycle decision %d references an unlisted activation", slot.name, d.sequence)
		}
		target.later = append(target.later, proto.NewEventID(slot.producer, slot.run, eventSequence(d, 0)))
	}
	for activation, refs := range activations {
		appliedSeq, ok := firstSeen[refs.applied]
		if !ok {
			t.Fatalf("slot %s: the Applied of activation %d is absent from the stream", slot.name, activation)
		}
		for _, later := range refs.later {
			seq, ok := firstSeen[later]
			if !ok {
				t.Fatalf("slot %s: a lifecycle event of activation %d is absent", slot.name, activation)
			}
			if seq < appliedSeq {
				t.Fatalf("slot %s: a lifecycle event of activation %d (stream %d) precedes its Applied (stream %d)",
					slot.name, activation, seq, appliedSeq)
			}
		}
	}
	// The removal of the reorged activation precedes the replacement
	// application in the stream.
	if len(decisions) >= 5 && decisions[3].kind == semantic.KindRemoved {
		removedSeq := firstSeen[proto.NewEventID(slot.producer, slot.run, eventSequence(decisions[3], 0))]
		replacementSeq := firstSeen[proto.NewEventID(slot.producer, slot.run, eventSequence(decisions[4], 0))]
		if removedSeq > replacementSeq {
			t.Fatalf("slot %s: the replacement application (stream %d) precedes the removal (stream %d)",
				slot.name, replacementSeq, removedSeq)
		}
	}
}

// assertEmptyBlockMetadata verifies the empty block's final record: one
// block record, a zero transaction count, the optional metadata field
// absent, and its own unique identity.
func assertEmptyBlockMetadata(t *testing.T, slot networkSlot, report streamReport, empty decisionSpec) {
	t.Helper()
	eventID := proto.NewEventID(slot.producer, slot.run, eventSequence(empty, 0))
	rec, ok := report.records[eventID]
	if !ok {
		t.Fatalf("the empty block record is absent from the final corpus")
	}
	if rec.header.EventFamily != uint32(proto.FamilyBlock) ||
		rec.header.EventKind != uint32(proto.KindApplied) {
		t.Fatalf("the empty block record is not an applied block: %+v", rec.header)
	}
	if rec.header.FragmentCount == 0 {
		t.Fatalf("the empty block record has no fragments")
	}
	var (
		txCount  uint64
		metadata bool
		fields   int
	)
	for _, node := range rec.nodes[1:] {
		if !node.HasFieldTag {
			t.Fatalf("the empty block record carries a non-field child")
		}
		fields++
		switch node.FieldTag {
		case 2:
			txCount = node.Uint64
		case 4:
			metadata = true
		}
	}
	if txCount != 0 {
		t.Fatalf("the empty block reports %d transactions", txCount)
	}
	if metadata {
		t.Fatalf("the empty block invented its absent metadata field")
	}
	if fields != 3 {
		t.Fatalf("the empty block carries %d fields, want the closed three", fields)
	}
}

// groupByEvent groups stored messages by their event identity, keeping
// the stored order inside each group.
func groupByEvent(msgs []publisher.MemoryMessage) map[proto.EventID][]publisher.MemoryMessage {
	out := make(map[proto.EventID][]publisher.MemoryMessage)
	for _, msg := range msgs {
		frame, err := proto.DecodeTransportFrame(msg.Payload)
		if err != nil {
			continue // readStream already failed on any malformed frame
		}
		out[frame.EventID] = append(out[frame.EventID], msg)
	}
	return out
}

// firstDigestOf derives a nonzero content digest for the automaton feed.
func firstDigestOf(t *testing.T, descs []semantic.SemanticDescriptor) [32]byte {
	t.Helper()
	digest, err := descs[0].Digest()
	if err != nil {
		t.Fatalf("descriptor digest: %v", err)
	}
	return digest
}

// newAcceptanceStackForViolations builds a minimal stack for the negative
// lifecycle controls.
func newAcceptanceStackForViolations(t *testing.T, slot networkSlot) *acceptanceStack {
	t.Helper()
	server, err := publisher.NewMemoryJetStream(publisher.StreamConfig{
		Name: slot.stream, Subjects: []string{slot.prefix + ".>"},
		MaxMsgSize: acceptanceBudget,
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	return newStack(slot, server, 600, 610)
}
