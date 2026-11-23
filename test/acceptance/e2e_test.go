// E07-02.e2e (#72): the end-to-end product verdict of each network. One
// composed pipeline per slot — the native decision journal, the
// normalization, the codec, the layout planner, the producer WAL, the
// gateway journal, the publisher engine and the in-memory NATS — runs a
// live scenario (blocks with bodies, an empty block, a system-metadata
// block, a reorg with its Removed/Applied pair, a finality-only decision)
// and the independent oracle compares the full NATS content against the
// fixture (TASK_DECOMPOSITION E07-02.e2e; EVID, MSG, SEM).
//
// The crash matrix drives every durable boundary of one decision — the
// marker, the intent, the staging, READY, SEALED, the gateway import, a
// partial PubAck set, the completed publish, the ingest ack and the
// bounded cleanup — and proves each recovery converges to the identical
// stream: the same messages, byte for byte, with no invented and no lost
// records (REQ-CAP-02, REQ-CAP-07, REQ-DLV-02, REQ-DLV-06).
package acceptance

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/delivery/release"
	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
	lifecycle "github.com/sigaev-pro/bc-indexer/test/lifecycle_validator"
)

// admitE2EScenario drives the live scenario of a slot through its source
// (the stack's window [100, 120] leaves headroom above the scenario, so
// the boundary refusals of the scope suite never interfere here) and returns
// the admitted decisions in order:
//
//	100 — a block with three body transactions (every third splits into
//	      PART frames) and the slot's optional-family events;
//	101 — an empty block: no transactions, still published;
//	102 — a system-metadata block: the optional header field is present;
//	reorg of 102 — Removed of every entity of the old application, then
//	      the Applied of the replacement body;
//	103 — a dense block; finalized at 101 afterwards (finality-only
//	      decision: the decision head moves, the block head does not).
func admitE2EScenario(t testingT, h *acceptanceStack) []decisionSpec {
	t.Helper()
	admit := func(b blockFixture) decisionSpec {
		d, err := h.source.admitLive(b)
		if err != nil {
			t.Fatalf("slot %s: admit height %d: %v", h.slot.name, b.height, err)
		}
		return d
	}
	balances := slotOptionalEvents(h.slot)
	decisions := []decisionSpec{
		admit(fixtureBlock(100, 3, balances)),
		admit(emptyFixtureBlock(101)),
		admit(systemFixtureBlock(102, 2)),
	}
	reorg, err := h.source.reorgAt(102, reorgFixtureBlock(102, 4, 1))
	if err != nil {
		t.Fatalf("slot %s: reorg of 102: %v", h.slot.name, err)
	}
	decisions = append(decisions, reorg...)
	decisions = append(decisions, admit(fixtureBlock(103, 5, 0)))
	status, err := h.source.finalizeAt(101)
	if err != nil {
		t.Fatalf("slot %s: finalize 101: %v", h.slot.name, err)
	}
	decisions = append(decisions, status)
	return decisions
}

// slotOptionalEvents fixes the optional-family event count of a slot's
// blocks: the BSC system scope and the Bor StateSync scope are mandatory
// native event sources (REQ-SCP-04), while the ETH outside-tx balance
// scope stays default-OFF in the acceptance configuration (REQ-SCP-05).
func slotOptionalEvents(slot networkSlot) int {
	if slot.name == "ETH" {
		return 0
	}
	return 2
}

// TestE2EFullPipelinePerNetwork is the positive end-to-end verdict: every
// block of the scenario produces exactly its transaction, balance and
// lifecycle events; the empty block is published; the reorg produces the
// correct Removed and Applied events; the lifecycle oracle accepts the
// whole sequence; and one decision allocates exactly one retired batch.
func TestE2EFullPipelinePerNetwork(t *testing.T) {
	for _, slot := range networkSlots {
		t.Run(slot.name, func(t *testing.T) {
			server, err := publisher.NewMemoryJetStream(publisher.StreamConfig{
				Name: slot.stream, Subjects: []string{slot.prefix + ".>"},
				MaxMsgSize: acceptanceBudget,
			})
			if err != nil {
				t.Fatalf("server: %v", err)
			}
			h := newStack(slot, server, 100, 120)
			decisions := admitE2EScenario(t, h)
			h.deliverAll(t, server)

			// Full-set comparison against the fixture oracle: every event
			// of every decision, nothing more, byte-exact content.
			verifyStream(t, server, slot, h.source).mustBeClean(t, slot)

			// Per-block expectations, stated per decision for visibility.
			assertDecisionEvents(t, h, decisions[0], 3, slotOptionalEvents(h.slot),
				semantic.KindApplied) // block + 3 tx + the slot's optional scope
			assertDecisionEvents(t, h, decisions[1], 0, 0, semantic.KindApplied) // empty block: exactly the block event
			assertDecisionEvents(t, h, decisions[2], 2, 0, semantic.KindApplied)
			assertDecisionEvents(t, h, decisions[3], 2, 0, semantic.KindRemoved) // reorg: old entities removed
			assertDecisionEvents(t, h, decisions[4], 4, 1, semantic.KindApplied) // replacement applied
			assertDecisionEvents(t, h, decisions[5], 5, 0, semantic.KindApplied)
			assertDecisionEvents(t, h, decisions[6], 0, 0, semantic.KindStatus) // finality-only: 101 had no body

			// The finality-only decision advanced the decision head but
			// never the block head (REQ-LIF-04).
			decisionHead, blockHead := h.source.heads()
			if decisionHead != decisions[len(decisions)-1].sequence {
				t.Fatalf("slot %s: decision head %d, want the finality decision %d",
					slot.name, decisionHead, decisions[len(decisions)-1].sequence)
			}
			if blockHead != 103 {
				t.Fatalf("slot %s: block head %d moved on a finality-only decision", slot.name, blockHead)
			}

			// The empty block reached its route with a valid record.
			assertEmptyBlockPublished(t, server, h, decisions[1])

			// The reorg produced the Removed route of the block and of
			// every body transaction, then the replacement Applied.
			assertReorgRoutes(t, server, h, decisions[3], decisions[4])

			// The lifecycle oracle accepts the whole delivered sequence,
			// and the removed activation is terminal.
			assertLifecycleAccepted(t, h, decisions)

			// One decision allocates exactly one batch, and every batch of
			// the run is retired (REQ-DLV-02).
			if got := h.mem.RetiredPrefix(); got != uint64(len(decisions)) {
				t.Fatalf("slot %s: retired prefix %d, want %d batches", slot.name, got, len(decisions))
			}
			for _, d := range decisions {
				if _, alive := h.prod.Batch(d.sequence); alive {
					t.Fatalf("slot %s: batch of decision %d is not retired", slot.name, d.sequence)
				}
			}

			// A full replay of an already-delivered run publishes nothing
			// new: the frontier answers retired decisions.
			before := snapshotMessages(server, slot)
			h.deliverAll(t, server)
			assertSnapshotUnchanged(t, slot, server, before)
		})
	}
}

// assertDecisionEvents verifies one decision produced exactly its expected
// event set: the block event, the body transactions and the optional
// events, all under the decision's kind.
func assertDecisionEvents(t *testing.T, h *acceptanceStack, d decisionSpec, txCount, balances int, kind semantic.Kind) {
	t.Helper()
	descs, err := h.descriptors(d)
	if err != nil {
		t.Fatalf("slot %s: descriptors of decision %d: %v", h.slot.name, d.sequence, err)
	}
	want := 1 + txCount + balances
	if len(descs) != want {
		t.Fatalf("slot %s: decision %d derived %d events, want %d (block + %d tx + %d optional)",
			h.slot.name, d.sequence, len(descs), want, txCount, balances)
	}
	blockKind := descs[0].Kind
	if blockKind != kind {
		t.Fatalf("slot %s: decision %d derived block kind %s, want %s", h.slot.name, d.sequence, blockKind, kind)
	}
}

// assertEmptyBlockPublished verifies the REQ-DAT-10/REQ-SCP-04 rule: an
// empty block publishes its own record on the block applied route — no
// transaction exists to publish, and the block event is not skipped.
func assertEmptyBlockPublished(t *testing.T, server *publisher.MemoryJetStream, h *acceptanceStack, d decisionSpec) {
	t.Helper()
	subject, err := subjectOf(h.slot, proto.FamilyBlock, proto.KindApplied)
	if err != nil {
		t.Fatalf("subject: %v", err)
	}
	eventID := proto.NewEventID(h.slot.producer, h.slot.run, eventSequence(d, 0))
	found := false
	for _, msg := range messagesOf(server, h.slot) {
		if msg.Subject != subject {
			continue
		}
		frame, err := proto.DecodeTransportFrame(msg.Payload)
		if err != nil || frame.EventID != eventID {
			continue
		}
		found = true
	}
	if !found {
		t.Fatalf("slot %s: the empty block of decision %d never reached %q", h.slot.name, d.sequence, subject)
	}
}

// assertReorgRoutes verifies the reorg pair: every entity of the removed
// application reached its Removed route, and the replacement block its
// Applied route (REQ-LIF-01, REQ-LIF-02).
func assertReorgRoutes(t *testing.T, server *publisher.MemoryJetStream, h *acceptanceStack, removed, applied decisionSpec) {
	t.Helper()
	report := readStream(t, server, h.slot)
	removedBlock := proto.NewEventID(h.slot.producer, h.slot.run, eventSequence(removed, 0))
	appliedBlock := proto.NewEventID(h.slot.producer, h.slot.run, eventSequence(applied, 0))
	for _, check := range []struct {
		id    proto.EventID
		kind  proto.Kind
		label string
	}{
		{removedBlock, proto.KindRemoved, "removed block"},
		{appliedBlock, proto.KindApplied, "replacement block"},
	} {
		rec, ok := report.records[check.id]
		if !ok {
			t.Fatalf("slot %s: the %s of the reorg is absent from the stream", h.slot.name, check.label)
		}
		wantSubject, err := subjectOf(h.slot, proto.FamilyBlock, check.kind)
		if err != nil {
			t.Fatalf("subject: %v", err)
		}
		if rec.subject != wantSubject {
			t.Fatalf("slot %s: the %s of the reorg sits on %q, want %q", h.slot.name,
				check.label, rec.subject, wantSubject)
		}
	}
	// Every body transaction of the old application was removed too.
	old, ok := h.source.decision(removed.target)
	if !ok {
		t.Fatalf("slot %s: the removed application vanished", h.slot.name)
	}
	for i := range old.block.txs {
		id := proto.NewEventID(h.slot.producer, h.slot.run, eventSequence(removed, uint64(i)+1))
		if _, ok := report.records[id]; !ok {
			t.Fatalf("slot %s: body transaction %d of the removed application has no Removed event",
				h.slot.name, i)
		}
	}
}

// assertLifecycleAccepted folds the delivered descriptor sequence of the
// decisions into the independent E02-03 lifecycle oracle: every event is
// the legal continuation of its activation, no conflict and no invented
// activation exists, and the removed activation ends terminal.
func assertLifecycleAccepted(t *testing.T, h *acceptanceStack, decisions []decisionSpec) {
	t.Helper()
	automaton := lifecycle.New()
	removed := map[lifecycle.ActivationKey]bool{}
	for _, d := range decisions {
		descs, err := h.descriptors(d)
		if err != nil {
			t.Fatalf("slot %s: descriptors of decision %d: %v", h.slot.name, d.sequence, err)
		}
		for _, desc := range descs {
			digest, err := desc.Digest()
			if err != nil {
				t.Fatalf("slot %s: descriptor digest: %v", h.slot.name, err)
			}
			content := lifecycle.Digest(digest)
			result, err := automaton.Apply(desc, content)
			if err != nil {
				t.Fatalf("slot %s: lifecycle oracle rejected event of decision %d: %v",
					h.slot.name, d.sequence, err)
			}
			switch result.Class {
			case lifecycle.ClassAccepted, lifecycle.ClassStale:
			case lifecycle.ClassDuplicate:
				t.Fatalf("slot %s: decision %d re-delivered an event as a duplicate", h.slot.name, d.sequence)
			case lifecycle.ClassConflict:
				t.Fatalf("slot %s: decision %d conflicts with the recorded lifecycle: %s",
					h.slot.name, d.sequence, result.Reason)
			case lifecycle.ClassUnknown:
				t.Fatalf("slot %s: decision %d references an activation the stream never applied: %s",
					h.slot.name, d.sequence, result.Reason)
			}
			if result.State == lifecycle.StateRemoved {
				removed[result.Key] = true
			}
		}
	}
	if len(removed) == 0 {
		t.Fatalf("slot %s: the reorg left no terminal activation", h.slot.name)
	}
}

// cutSpec fixes one crash cut point of the delivery drive: the durable
// state the process leaves behind before the cut.
type cutSpec struct {
	name string
	run  func(t testingT, h *acceptanceStack, conn publisher.Conn, d decisionSpec)
}

// The named cut steps: each drives the durable state of one boundary and
// builds on the previous step, so the cut points stay composable without
// self-referential initializers.

// cutIntent leaves the native marker and the WAL intent committed.
func cutIntent(t testingT, h *acceptanceStack, d decisionSpec) {
	t.Helper()
	batch, _, err := h.source.allocator.EnsureBatch(d.sequence)
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	_, _, seq := batch.Decode()
	if allocated, err := h.prod.AllocateIntent(); err != nil || allocated != seq {
		t.Fatalf("intent: allocated=%d err=%v", allocated, err)
	}
}

// cutStaging leaves one staged RAM chunk behind the intent.
func cutStaging(t testingT, h *acceptanceStack, d decisionSpec) {
	t.Helper()
	cutIntent(t, h, d)
	if err := h.stageChunk(d.sequence, fmt.Sprintf("decision-%d-a", d.sequence)); err != nil {
		t.Fatalf("staging: %v", err)
	}
}

// cutReady leaves the closing READY commit behind the staged chunks.
func cutReady(t testingT, h *acceptanceStack, d decisionSpec) {
	t.Helper()
	if _, err := h.driveMarker(t, d); err != nil {
		t.Fatalf("marker: %v", err)
	}
}

// cutSealed leaves the producer seal behind the READY commit.
func cutSealed(t testingT, h *acceptanceStack, d decisionSpec) *plannedBatch {
	t.Helper()
	cutReady(t, h, d)
	planned, err := h.planDecision(d)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := h.prod.CommitSealed(planned.identity.Batch.Seq,
		release.NewSealedPlan(planned.identity, d.sequence*1000, h.slot.stream,
			keysOf(planned.frames))); err != nil {
		t.Fatalf("seal: %v", err)
	}
	return planned
}

// cutImport leaves the gateway CLOSED registration behind the seal.
func cutImport(t testingT, h *acceptanceStack, d decisionSpec) *plannedBatch {
	t.Helper()
	planned := cutSealed(t, h, d)
	if err := h.gw.ImportClosed(h.fence(1),
		release.NewSealedPlan(planned.identity, d.sequence*1000, h.slot.stream,
			keysOf(planned.frames)), false); err != nil {
		t.Fatalf("import: %v", err)
	}
	return planned
}

// cutPartialPublish leaves a partial PubAck set: several frames are
// acknowledged at the server, the batch is still not NATS_ACKED.
func cutPartialPublish(t testingT, h *acceptanceStack, d decisionSpec) {
	t.Helper()
	planned := cutImport(t, h, d)
	counting := &failingConn{Conn: h.server, after: 2}
	_, err := h.engine(counting).Publish(h.fence(1), h.target,
		publisher.Batch{Identity: planned.identity, Frames: planned.frames})
	var incomplete *publisher.ErrIncomplete
	if !errors.As(err, &incomplete) {
		t.Fatalf("partial publish returned %v, want ErrIncomplete", err)
	}
	if got := counting.successes(); got != 2 {
		t.Fatalf("partial run acknowledged %d frames, want the planned 2", got)
	}
}

// cutPublish leaves the completed publish — the gateway holds NATS_ACKED —
// without the producer's ingest ack.
func cutPublish(t testingT, h *acceptanceStack, d decisionSpec) *plannedBatch {
	t.Helper()
	planned := cutImport(t, h, d)
	if _, err := h.engine(h.server).Publish(h.fence(1), h.target,
		publisher.Batch{Identity: planned.identity, Frames: planned.frames}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	return planned
}

// cutIngestAck leaves the producer's ingest ack behind the publish,
// without the payload cleanup.
func cutIngestAck(t testingT, h *acceptanceStack, d decisionSpec) {
	t.Helper()
	planned := cutPublish(t, h, d)
	ack, err := h.engine(h.server).Publish(h.fence(1), h.target,
		publisher.Batch{Identity: planned.identity, Frames: planned.frames})
	if err != nil {
		t.Fatalf("publish replay: %v", err)
	}
	if err := release.OnIngestAck(h.prod, planned.identity.Batch.Seq, ack); err != nil {
		t.Fatalf("ingest ack: %v", err)
	}
}

// e2eCutPoints enumerates the durable boundaries of one decision a crash
// can follow: the native marker, the WAL intent, the staged chunks, the
// READY commit, the seal, the gateway import, a partial PubAck set, the
// completed publish (NATS_ACKED without the producer ack), the ingest ack
// and the bounded payload cleanup (REQ-CAP-02, REQ-CAP-07, REQ-DLV-05,
// REQ-DLV-06).
var e2eCutPoints = []cutSpec{
	{"after-marker", func(t testingT, h *acceptanceStack, _ publisher.Conn, _ decisionSpec) {
		// The decision is admitted in the source; nothing sits in the WAL.
	}},
	{"after-intent", func(t testingT, h *acceptanceStack, _ publisher.Conn, d decisionSpec) {
		cutIntent(t, h, d)
	}},
	{"after-staging", func(t testingT, h *acceptanceStack, _ publisher.Conn, d decisionSpec) {
		cutStaging(t, h, d)
	}},
	{"after-ready", func(t testingT, h *acceptanceStack, _ publisher.Conn, d decisionSpec) {
		cutReady(t, h, d)
	}},
	{"after-sealed", func(t testingT, h *acceptanceStack, _ publisher.Conn, d decisionSpec) {
		cutSealed(t, h, d)
	}},
	{"after-import", func(t testingT, h *acceptanceStack, _ publisher.Conn, d decisionSpec) {
		cutImport(t, h, d)
	}},
	{"after-partial-publish", func(t testingT, h *acceptanceStack, _ publisher.Conn, d decisionSpec) {
		cutPartialPublish(t, h, d)
	}},
	{"after-publish", func(t testingT, h *acceptanceStack, _ publisher.Conn, d decisionSpec) {
		cutPublish(t, h, d)
	}},
	{"after-ingest-ack", func(t testingT, h *acceptanceStack, _ publisher.Conn, d decisionSpec) {
		cutIngestAck(t, h, d)
	}},
	{"after-cleanup", func(t testingT, h *acceptanceStack, _ publisher.Conn, d decisionSpec) {
		cutIngestAck(t, h, d)
		if remains, err := release.CleanupPayload(h.prod, d.sequence, 100); err != nil || remains {
			t.Fatalf("cleanup: remains=%v err=%v", remains, err)
		}
	}},
}

// TestE2ECrashAtEveryBoundaryRecovers is the crash half of the verdict:
// for every durable cut point of the second decision — after the first ran
// to completion — both adapter processes crash and recover on their
// surviving stores, the full replay finishes every admitted obligation,
// and the stream equals the fixture oracle exactly: the same messages, no
// duplicates with foreign bytes, no lost records.
func TestE2ECrashAtEveryBoundaryRecovers(t *testing.T) {
	for _, slot := range networkSlots {
		for _, cut := range e2eCutPoints {
			t.Run(slot.name+"/"+cut.name, func(t *testing.T) {
				server, err := publisher.NewMemoryJetStream(publisher.StreamConfig{
					Name: slot.stream, Subjects: []string{slot.prefix + ".>"},
					MaxMsgSize: acceptanceBudget,
				})
				if err != nil {
					t.Fatalf("server: %v", err)
				}
				h := newStack(slot, server, 100, 120)
				first, err := h.source.admitLive(fixtureBlock(100, 2, 1))
				if err != nil {
					t.Fatalf("admit 100: %v", err)
				}
				h.deliverDecision(t, server, first)
				victim, err := h.source.admitLive(fixtureBlock(101, 4, 0))
				if err != nil {
					t.Fatalf("admit 101: %v", err)
				}
				// A reorg decision is also in flight: the recovery must
				// finish the whole multi-event obligation set in order.
				if _, err := h.source.reorgAt(101, reorgFixtureBlock(101, 3, 0)); err != nil {
					t.Fatalf("reorg 101: %v", err)
				}
				cut.run(t, h, server, victim)
				h.crash()
				h.deliverAll(t, server)
				verifyStream(t, server, slot, h.source).mustBeClean(t, slot)
				if got := h.mem.RetiredPrefix(); got != 4 {
					t.Fatalf("slot %s: retired prefix %d after recovery, want 4", slot.name, got)
				}
			})
		}
	}
}

// TestE2EReorgRemovalSurvivesPublish verifies REQ-LIF-02: a READY Applied
// can always receive its Removed — the reorg removal is published even
// after the application is fully ACKed, and the terminal state survives.
func TestE2EReorgRemovalSurvivesPublish(t *testing.T) {
	for _, slot := range networkSlots {
		t.Run(slot.name, func(t *testing.T) {
			server, err := publisher.NewMemoryJetStream(publisher.StreamConfig{
				Name: slot.stream, Subjects: []string{slot.prefix + ".>"},
				MaxMsgSize: acceptanceBudget,
			})
			if err != nil {
				t.Fatalf("server: %v", err)
			}
			h := newStack(slot, server, 100, 120)
			applied, err := h.source.admitLive(fixtureBlock(100, 3, 0))
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			h.deliverDecision(t, server, applied)
			if got := h.mem.RetiredPrefix(); got != 1 {
				t.Fatalf("the application is not fully retired")
			}
			pair, err := h.source.reorgAt(100, reorgFixtureBlock(100, 2, 0))
			if err != nil {
				t.Fatalf("reorg: %v", err)
			}
			h.deliverDecision(t, server, pair[0])
			h.deliverDecision(t, server, pair[1])
			verifyStream(t, server, slot, h.source).mustBeClean(t, slot)
			assertReorgRoutes(t, server, h, pair[0], pair[1])
		})
	}
}

// failingConn fails once after n successful publishes; the frames already
// acknowledged stay acknowledged, exactly the partial PubAck set the
// delivery contract must survive.
type failingConn struct {
	publisher.Conn
	after  int
	ok     int
	failed bool
}

func (c *failingConn) Publish(subject string, header, payload []byte) (publisher.PubAck, error) {
	if !c.failed && c.ok >= c.after {
		c.failed = true
		return publisher.PubAck{}, fmt.Errorf("acceptance: planned transient failure")
	}
	ack, err := c.Conn.Publish(subject, header, payload)
	if err == nil {
		c.ok++
	}
	return ack, err
}

// successes returns the acknowledged frame count of the partial run.
func (c *failingConn) successes() int { return c.ok }
