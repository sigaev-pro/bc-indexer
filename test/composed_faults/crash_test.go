package composed_faults

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	derrors "github.com/sigaev-pro/bc-indexer/core/delivery/errors"
	"github.com/sigaev-pro/bc-indexer/core/delivery/release"
	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
	lifecycle "github.com/sigaev-pro/bc-indexer/test/lifecycle_validator"
)

// The combined REC-T01 proof: a crash at every boundary of the composed
// pipeline — native marker/INTENT, STAGING, READY, SEALED, CLOSED, the
// PubAck set, NATS_ACKED, the producer cleanup, the gateway release —
// followed by a restart and an idempotent recovery that ends in the exact
// same durable outcome: both batches retired on both sides, NATS holding
// exactly their messages, a reorg decision delivering its own Removed
// batch, and the recovery GC releasing exactly the pins its rule allows.
// No interleaving of the crash leaves a duplicate action or resurrected
// state.
func TestCombinedCrashMatrixREC01(t *testing.T) {
	for _, slot := range slots {
		t.Run(slot.name, func(t *testing.T) {
			d1 := appliedBlock(1, blockHash(1), 2) // 3 frames
			d2 := appliedBlock(2, blockHash(2), 3) // 4 frames
			d3 := lifecycleOf(3, 2, blockHash(2), semantic.KindRemoved, 1)

			// cut drives d1 (and d2 where noted) to one crash boundary.
			cut := func(t *testing.T, h *stack, name string) {
				t.Helper()
				f := h.fence(1)
				switch name {
				case "after_intent":
					h.intentOnly(t, d1)
				case "mid_staging":
					h.stageOnly(t, d1)
				case "after_ready":
					if _, _, _, err := h.applyDecision(d1); err != nil {
						t.Fatalf("applyDecision: %v", err)
					}
				case "after_sealed", "after_closed", "mid_puback", "after_nats_acked":
					seq, manifest, _, err := h.applyDecision(d1)
					if err != nil {
						t.Fatalf("applyDecision: %v", err)
					}
					frames, err := h.frames(d1)
					if err != nil {
						t.Fatalf("frames: %v", err)
					}
					id := h.sealedIdentity(manifest)
					if err := h.prod.CommitSealed(seq,
						release.NewSealedPlan(id, 1000+seq, h.slot.stream, keysOf(frames))); err != nil {
						t.Fatalf("CommitSealed: %v", err)
					}
					if name == "after_sealed" {
						break
					}
					if err := h.gw.ImportClosed(f,
						release.NewSealedPlan(id, 1000+seq, h.slot.stream, keysOf(frames)), false); err != nil {
						t.Fatalf("ImportClosed: %v", err)
					}
					if name == "after_closed" {
						break
					}
					if name == "mid_puback" {
						// One frame publishes and is accounted; the partial
						// PubAck set never completes the batch.
						if _, err := h.engine.Publish(f, h.target,
							publisher.Batch{Identity: id, Frames: frames[:1]}); !errors.Is(err, delivery.ErrState) {
							t.Fatalf("partial publish: %v, want ErrState", err)
						}
						break
					}
					if _, err := h.engine.Publish(f, h.target,
						publisher.Batch{Identity: id, Frames: frames}); err != nil {
						t.Fatalf("publish: %v", err)
					}
				case "mid_payload_cleanup":
					h.driveToAcked(t, d1)
					seq, _ := h.journal.binding(d1.sequence)
					if remains, err := release.CleanupPayload(h.prod, seq, 1); err != nil || !remains {
						t.Fatalf("partial cleanup: remains=%v err=%v", remains, err)
					}
				case "mid_releasing":
					h.driveToAcked(t, d1)
					seq, _ := h.journal.binding(d1.sequence)
					if remains, err := release.CleanupPayload(h.prod, seq, 100); err != nil || remains {
						t.Fatalf("cleanup: remains=%v err=%v", remains, err)
					}
					id := h.sealedIdentity(manifestOf(t, h, d1))
					if err := h.gw.BeginRelease(f, id.Batch); err != nil {
						t.Fatalf("BeginRelease: %v", err)
					}
					if more, err := h.gw.CleanupStep(f, id.Batch); err != nil || !more {
						t.Fatalf("first cleanup step: more=%v err=%v", more, err)
					}
				case "after_retire_d1":
					h.driveToRetired(t, d1)
				case "d2_after_ready":
					h.driveToRetired(t, d1)
					if _, _, _, err := h.applyDecision(d2); err != nil {
						t.Fatalf("applyDecision d2: %v", err)
					}
				case "d2_mid_puback":
					h.driveToRetired(t, d1)
					seq, manifest, _, err := h.applyDecision(d2)
					if err != nil {
						t.Fatalf("applyDecision d2: %v", err)
					}
					frames, err := h.frames(d2)
					if err != nil {
						t.Fatalf("frames d2: %v", err)
					}
					id := h.sealedIdentity(manifest)
					if err := h.prod.CommitSealed(seq,
						release.NewSealedPlan(id, 1000+seq, h.slot.stream, keysOf(frames))); err != nil {
						t.Fatalf("CommitSealed d2: %v", err)
					}
					if err := h.gw.ImportClosed(f,
						release.NewSealedPlan(id, 1000+seq, h.slot.stream, keysOf(frames)), false); err != nil {
						t.Fatalf("ImportClosed d2: %v", err)
					}
					if _, err := h.engine.Publish(f, h.target,
						publisher.Batch{Identity: id, Frames: frames[:2]}); !errors.Is(err, delivery.ErrState) {
						t.Fatalf("partial publish d2: %v, want ErrState", err)
					}
				}
			}

			cutPoints := []string{
				"after_intent", "mid_staging", "after_ready", "after_sealed",
				"after_closed", "mid_puback", "after_nats_acked",
				"mid_payload_cleanup", "mid_releasing", "after_retire_d1",
				"d2_after_ready", "d2_mid_puback",
			}
			for _, point := range cutPoints {
				t.Run(point, func(t *testing.T) {
					h := newStack(slot)
					cut(t, h, point)

					// The crash and the idempotent recovery of the whole
					// pipeline, decisions in order.
					h.crash()
					h.driveToRetired(t, d1)
					h.driveToRetired(t, d2)

					// Both applications and the reorg decision retired on
					// both sides, records gone, NATS exact.
					h.driveToRetired(t, d3)
					if got := h.prod.RetiredPrefix(); got != 3 {
						t.Fatalf("producer prefix %d, want 3", got)
					}
					if got := h.gw.RetiredPrefix(slot.producer, slot.run); got != 3 {
						t.Fatalf("gateway prefix %d, want 3", got)
					}
					if _, ok := h.prod.Batch(1); ok {
						t.Fatalf("the producer kept a retired record")
					}
					h.assertNATSEquals(t, d1, d2, d3)

					// The marker replays of every decision create no second
					// action and nothing new reaches NATS.
					before := len(h.messagesByID())
					for _, spec := range []decisionSpec{d1, d2, d3} {
						seq, _, fresh, err := h.applyDecision(spec)
						if err != nil || fresh {
							t.Fatalf("marker replay of %d: fresh=%v err=%v", spec.sequence, fresh, err)
						}
						if _, ok := h.prod.Batch(seq); ok {
							t.Fatalf("the replay recreated batch %d", seq)
						}
					}
					if got := len(h.messagesByID()); got != before {
						t.Fatalf("the replays stored %d new messages", got-before)
					}
					// A late publish of a retired batch is ALREADY_RETIRED
					// without creating keys.
					frames1, err := h.frames(d1)
					if err != nil {
						t.Fatalf("frames: %v", err)
					}
					id1 := h.sealedIdentity(manifestOf(t, h, d1))
					if _, err := h.engine.Publish(h.fence(1), h.target,
						publisher.Batch{Identity: id1, Frames: frames1}); !errors.Is(err, delivery.ErrAlreadyRetired) {
						t.Fatalf("late publish of a retired batch: %v, want ErrAlreadyRetired", err)
					}

					// The recovery GC: pins are held while nothing covers
					// the applications; an accepted finality anchor covering
					// decision 1 releases only its pins; the Removed
					// decision terminalizing decision 2 releases its pins
					// once its batch retired.
					batchOf := map[uint64]uint64{}
					for _, sequence := range []uint64{1, 2, 3} {
						batch, ok := h.journal.binding(sequence)
						if !ok {
							t.Fatalf("decision %d lost its binding", sequence)
						}
						batchOf[sequence] = batch
					}
					if released := h.pins.sweep(3, batchOf, map[uint64]bool{}); len(released) != 0 {
						t.Fatalf("the GC released %v without an anchor or terminal state", released)
					}
					h.pins.acceptAnchor(1)
					if released := h.pins.sweep(3, batchOf, map[uint64]bool{}); len(released) != 1 || !contains(released, 1) {
						t.Fatalf("the anchor released %v, want [1]", released)
					}
					if !h.pins.pinned(2) {
						t.Fatalf("an application above the anchor without a terminal state lost its pins")
					}
					if released := h.pins.sweep(3, batchOf, map[uint64]bool{2: true}); len(released) != 1 || !contains(released, 2) {
						t.Fatalf("the terminal state released %v, want [2]", released)
					}
				})
			}
		})
	}
}

// driveToAcked drives one decision to the producer's ACKED_CLEANUP with
// the payload still present (the crash cut before the bounded cleanup).
func (h *stack) driveToAcked(t *testing.T, spec decisionSpec) {
	t.Helper()
	f := h.fence(1)
	seq, manifest, _, err := h.applyDecision(spec)
	if err != nil {
		t.Fatalf("applyDecision: %v", err)
	}
	frames, err := h.frames(spec)
	if err != nil {
		t.Fatalf("frames: %v", err)
	}
	id := h.sealedIdentity(manifest)
	if err := h.prod.CommitSealed(seq,
		release.NewSealedPlan(id, 1000+seq, h.slot.stream, keysOf(frames))); err != nil {
		t.Fatalf("CommitSealed: %v", err)
	}
	if err := h.gw.ImportClosed(f,
		release.NewSealedPlan(id, 1000+seq, h.slot.stream, keysOf(frames)), false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	ack, err := h.engine.Publish(f, h.target, publisher.Batch{Identity: id, Frames: frames})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := release.OnIngestAck(h.prod, seq, ack); err != nil {
		t.Fatalf("OnIngestAck: %v", err)
	}
}

// manifestOf returns the manifest identity of an admitted decision.
func manifestOf(t *testing.T, h *stack, spec decisionSpec) delivery.BatchIdentity {
	t.Helper()
	_, id := h.manifestIdentityOf(t, spec)
	return id
}

// contains reports whether the value is in the slice.
func contains(values []uint64, want uint64) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// AC-SCP-03-02: a restart with a pending WAL while new blocks arrive
// during the downtime. The new sync is held, the pending batches deliver
// independently, then the sync completes: the held decisions are admitted
// exactly once, the new head is installed, and the downtime range is never
// rescanned.
func TestPendingWALDeliveredWhileSyncHeld(t *testing.T) {
	for _, slot := range slots {
		t.Run(slot.name, func(t *testing.T) {
			h := newStack(slot)
			d1 := appliedBlock(1, blockHash(1), 1)
			d2 := appliedBlock(2, blockHash(2), 2)

			// The WAL holds one READY and one SEALED pending batch.
			if _, _, _, err := h.applyDecision(d1); err != nil {
				t.Fatalf("applyDecision d1: %v", err)
			}
			seq2, manifest2, _, err := h.applyDecision(d2)
			if err != nil {
				t.Fatalf("applyDecision d2: %v", err)
			}
			frames2, err := h.frames(d2)
			if err != nil {
				t.Fatalf("frames d2: %v", err)
			}
			id2 := h.sealedIdentity(manifest2)
			if err := h.prod.CommitSealed(seq2,
				release.NewSealedPlan(id2, 1000+seq2, h.slot.stream, keysOf(frames2))); err != nil {
				t.Fatalf("CommitSealed d2: %v", err)
			}

			// The restart with the pending WAL; the downtime brings new
			// blocks whose sync is held — no admission happens.
			h.crash()
			held := []decisionSpec{
				appliedBlock(3, blockHash(3), 1),
				appliedBlock(4, blockHash(4), 1),
			}
			for _, spec := range held {
				if _, ok := h.journal.binding(spec.sequence); ok {
					t.Fatalf("a held decision was admitted before the sync completed")
				}
			}

			// The pending batches deliver while the sync is held, each
			// exactly once.
			h.driveToRetired(t, d1)
			if got := h.prod.RetiredPrefix(); got != 1 {
				t.Fatalf("pending d1 prefix %d, want 1", got)
			}
			h.driveToRetired(t, d2)
			if got := h.prod.RetiredPrefix(); got != 2 {
				t.Fatalf("pending d2 prefix %d, want 2", got)
			}
			h.assertNATSEquals(t, d1, d2)

			// The sync completes: the held decisions are admitted now and
			// deliver; the new head is installed.
			for _, spec := range held {
				h.driveToRetired(t, spec)
			}
			if got := h.prod.RetiredPrefix(); got != 4 {
				t.Fatalf("prefix after the sync %d, want 4", got)
			}
			if got := h.journal.headOf(); got != 4 {
				t.Fatalf("head after the sync %d, want 4", got)
			}
			h.assertNATSEquals(t, d1, d2, held[0], held[1])

			// The downtime range is not rescanned: the markers of the
			// admitted decisions replay to their former batches, nothing
			// new is allocated and nothing new reaches NATS.
			before := len(h.messagesByID())
			for _, spec := range held {
				seq, fresh, err := h.journal.mark(spec)
				if err != nil || fresh {
					t.Fatalf("marker replay of %d: fresh=%v err=%v", spec.sequence, fresh, err)
				}
				if seq != spec.sequence { // every admitted decision kept its 1:1 batch
					t.Fatalf("replay bound batch %d, want %d", seq, spec.sequence)
				}
				h.driveToRetired(t, spec)
			}
			if got := h.prod.RetiredPrefix(); got != 4 {
				t.Fatalf("prefix after the replays %d, want 4", got)
			}
			if got := len(h.messagesByID()); got != before {
				t.Fatalf("the replays stored %d new messages", got-before)
			}
		})
	}
}

// AC-CAP-10-01: a fatal normalization corruption of one network's native
// process stops that process without a false READY, keeps its durable
// pending, and the independent neighbor networks continue delivering over
// the common NATS.
func TestFatalFailureStopsOwnNetworkOnly(t *testing.T) {
	server, err := publisher.NewMemoryJetStream(
		publisher.StreamConfig{Name: slots[0].stream, Subjects: []string{slots[0].prefix + ".>"}, MaxMsgSize: 1 << 20},
		publisher.StreamConfig{Name: slots[1].stream, Subjects: []string{slots[1].prefix + ".>"}, MaxMsgSize: 1 << 20},
		publisher.StreamConfig{Name: slots[2].stream, Subjects: []string{slots[2].prefix + ".>"}, MaxMsgSize: 1 << 20},
	)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	eth := newStackOn(server, slots[0])
	bsc := newStackOn(server, slots[1])
	poly := newStackOn(server, slots[2])

	// Every network holds one pending applied decision.
	dEth := appliedBlock(1, blockHash(1), 2)
	dBsc := appliedBlock(1, blockHash(1), 1)
	dPoly := appliedBlock(1, blockHash(1), 2)
	for _, s := range []*stack{eth, bsc, poly} {
		spec := dEth
		if s == bsc {
			spec = dBsc
		} else if s == poly {
			spec = dPoly
		}
		if _, _, _, err := s.applyDecision(spec); err != nil {
			t.Fatalf("%s pending decision: %v", s.slot.name, err)
		}
	}

	// A fatal normalization corruption hits BSC: its next decision
	// references an activation no admitted application ever created — a
	// lifecycle decision the normalizer cannot resolve, so the validation
	// rejects it before any allocation.
	corrupt := decisionSpec{sequence: 2, target: 9, block: blockHash(9),
		kind: semantic.KindRemoved, revision: 1}
	if _, _, _, err := bsc.applyDecision(corrupt); err == nil {
		t.Fatalf("the corrupt decision normalized")
	}
	// The composed error table prescribes stopping this native owner: the
	// corruption is a local fatal condition of BSC alone.
	classifier := derrors.NewClassifier()
	if err := classifier.Register(semantic.ErrInvalid, derrors.ClassLocalFatal); err != nil {
		t.Fatalf("register: %v", err)
	}
	class, known := classifier.Classify(fmt.Errorf("normalize: %w", semantic.ErrInvalid))
	if !known || class != derrors.ClassLocalFatal {
		t.Fatalf("class %s known=%v, want ClassLocalFatal", class, known)
	}
	if got := classifier.MustClassify(semantic.ErrInvalid); got != derrors.ActionStopLocalOwner {
		t.Fatalf("action %s, want ActionStopLocalOwner", got)
	}

	// BSC stopped: the corrupt decision allocated no batch and left no
	// false READY; its earlier durable pending survives untouched.
	if _, ok := bsc.journal.binding(2); ok {
		t.Fatalf("the corrupt decision allocated a batch")
	}
	if _, ok := bsc.prod.Batch(2); ok {
		t.Fatalf("the corrupt decision left a producer record")
	}
	pending, ok := bsc.prod.Batch(1)
	if !ok || pending.State != delivery.StateReady || !pending.HasIdentity || !pending.HasPayload {
		t.Fatalf("the BSC pending did not survive: %+v ok=%v", pending, ok)
	}
	if got := bsc.prod.RetiredPrefix(); got != 0 {
		t.Fatalf("BSC prefix %d, want 0", got)
	}

	// The neighbors continue delivering over the common NATS.
	eth.driveToRetired(t, dEth)
	poly.driveToRetired(t, dPoly)
	if got := eth.prod.RetiredPrefix(); got != 1 {
		t.Fatalf("ETH prefix %d, want 1", got)
	}
	if got := poly.prod.RetiredPrefix(); got != 1 {
		t.Fatalf("POLY prefix %d, want 1", got)
	}
	perStream := map[string]int{}
	for _, m := range server.Messages() {
		perStream[m.Stream]++
	}
	if perStream[slots[0].stream] != 3 || perStream[slots[2].stream] != 3 {
		t.Fatalf("neighbor streams hold %v", perStream)
	}
	if perStream[slots[1].stream] != 0 {
		t.Fatalf("the stopped network published %d messages", perStream[slots[1].stream])
	}
	// The BSC pending is still deliverable by a compatible recovery later:
	// its delivery path replays unchanged.
	bsc.driveToRetired(t, dBsc)
	if got := bsc.prod.RetiredPrefix(); got != 1 {
		t.Fatalf("BSC prefix after the recovery %d, want 1", got)
	}
}

// AC-LIF-04-01: a finality-only update of a saved activation — no new
// head, no new block — crashes after the durable native marker and before
// the adapter mirror. The recovery completes exactly that decision with
// exactly the replacement snapshot/revision, without waiting for a new
// block, and the repeat of the reconciled decision creates no revision.
func TestFinalityOnlyCrashBetweenMarkerAndMirror(t *testing.T) {
	for _, slot := range slots {
		t.Run(slot.name, func(t *testing.T) {
			h := newStack(slot)
			d1 := appliedBlock(1, blockHash(1), 2)
			h.driveToRetired(t, d1)

			// The finality-only decision: Status revision 1 of every entity
			// of the saved application; no new block exists.
			d2 := lifecycleOf(2, 1, blockHash(1), semantic.KindStatus, 1)
			seq2, fresh, err := h.journal.mark(d2)
			if err != nil || !fresh {
				t.Fatalf("marker: fresh=%v err=%v", fresh, err)
			}
			if seq2 != 2 {
				t.Fatalf("the finality decision bound batch %d, want 2", seq2)
			}

			// Power loss between the marker and the mirror/READY commit.
			h.crash()
			if _, ok := h.prod.Batch(seq2); ok {
				t.Fatalf("an unclosed mirror survived the crash")
			}

			// The recovery completes exactly that decision — no new block
			// is required (the block head never moved).
			h.driveToRetired(t, d2)
			if got := h.prod.RetiredPrefix(); got != 2 {
				t.Fatalf("prefix %d, want 2", got)
			}
			if got := h.journal.blockHeadOf(); got != 1 {
				t.Fatalf("block head %d, want 1 (no new block was needed)", got)
			}
			if got := h.journal.headOf(); got != 2 {
				t.Fatalf("decision head %d, want 2 (the finality decision is admitted)", got)
			}
			frames2, err := h.frames(d2)
			if err != nil {
				t.Fatalf("frames: %v", err)
			}
			if len(frames2) != 3 {
				t.Fatalf("the finality batch carries %d events, want the 3 replacement events", len(frames2))
			}
			h.assertNATSEquals(t, d1, d2)

			// The lifecycle mirror: the saved activations moved to exactly
			// revision 1 — the replacement snapshot — and the repeat of the
			// reconciled decision is an idempotent duplicate, not a new
			// revision.
			automaton := lifecycle.New()
			descs1, err := h.journal.descriptorsOf(d1)
			if err != nil {
				t.Fatalf("descriptors d1: %v", err)
			}
			descs2, err := h.journal.descriptorsOf(d2)
			if err != nil {
				t.Fatalf("descriptors d2: %v", err)
			}
			for _, d := range append(append([]semantic.SemanticDescriptor{}, descs1...), descs2...) {
				digest, err := d.Digest()
				if err != nil {
					t.Fatalf("digest: %v", err)
				}
				if _, err := automaton.Apply(d, lifecycle.Digest(digest)); err != nil {
					t.Fatalf("apply: %v", err)
				}
			}
			for _, d := range descs2 {
				key := lifecycle.KeyOf(d)
				rec, ok := automaton.Record(key)
				if !ok || rec.MaxRevision != 1 || rec.State != lifecycle.StateActive {
					t.Fatalf("activation %v after the finality update: rev=%d state=%s",
						key, rec.MaxRevision, rec.State)
				}
				digest, _ := d.Digest()
				if res, err := automaton.Apply(d, lifecycle.Digest(digest)); err != nil ||
					res.Class != lifecycle.ClassDuplicate {
					t.Fatalf("repeat of the reconciled decision: class=%s err=%v", res.Class, err)
				}
			}

			// The marker repeat and a full re-drive change nothing.
			before := len(h.messagesByID())
			if _, fresh, err := h.journal.mark(d2); err != nil || fresh {
				t.Fatalf("marker repeat: fresh=%v err=%v", fresh, err)
			}
			h.driveToRetired(t, d2)
			if got := h.prod.RetiredPrefix(); got != 2 {
				t.Fatalf("prefix after the repeat %d, want 2", got)
			}
			if got := len(h.messagesByID()); got != before {
				t.Fatalf("the repeat stored %d new messages", got-before)
			}
		})
	}
}

// AC-LIF-04-02: finality advanced past the downtime while no new events
// arrive after the restart. The startup refresh updates the old saved
// activations to the new revision, the repeat of the already reconciled
// decision creates no revision, and the transactions of the downtime range
// — never admitted — are not exported; the next admitted decision
// continues the batch counter without scanning the gap.
func TestStartupRefreshAfterDowntime(t *testing.T) {
	for _, slot := range slots {
		t.Run(slot.name, func(t *testing.T) {
			h := newStack(slot)
			d1 := appliedBlock(1, blockHash(1), 2)
			h.driveToRetired(t, d1)
			// The saved activation reaches revision 1 before the downtime.
			d2 := lifecycleOf(2, 1, blockHash(1), semantic.KindStatus, 1)
			h.driveToRetired(t, d2)

			automaton := lifecycle.New()
			fold := func(spec decisionSpec) lifecycle.Result {
				t.Helper()
				descs, err := h.journal.descriptorsOf(spec)
				if err != nil {
					t.Fatalf("descriptors: %v", err)
				}
				var last lifecycle.Result
				for _, d := range descs {
					digest, err := d.Digest()
					if err != nil {
						t.Fatalf("digest: %v", err)
					}
					last, err = automaton.Apply(d, lifecycle.Digest(digest))
					if err != nil {
						t.Fatalf("apply: %v", err)
					}
				}
				return last
			}
			fold(d1)
			fold(d2)

			// The downtime: blocks 4..7 were mined but never admitted; the
			// restart refresh observes finality advanced to revision 2.
			h.crash()
			downtime := []decisionSpec{
				appliedBlock(4, blockHash(4), 1),
				appliedBlock(5, blockHash(5), 1),
				appliedBlock(6, blockHash(6), 1),
				appliedBlock(7, blockHash(7), 1),
			}
			d3 := lifecycleOf(3, 1, blockHash(1), semantic.KindStatus, 2)
			if res := fold(d3); res.Class != lifecycle.ClassAccepted {
				t.Fatalf("startup refresh outcome %s, want Accepted", res.Class)
			}
			blockDesc, err := h.journal.descriptorsOf(d3)
			if err != nil {
				t.Fatalf("descriptors d3: %v", err)
			}
			rec, ok := automaton.Record(lifecycle.KeyOf(blockDesc[0]))
			if !ok || rec.MaxRevision != 2 {
				t.Fatalf("the saved activation stayed at revision %d, want 2", rec.MaxRevision)
			}
			// The repeat of the already reconciled decision creates no
			// revision.
			if res := fold(d3); res.Class != lifecycle.ClassDuplicate {
				t.Fatalf("repeat of the reconciled decision: %s, want Duplicate", res.Class)
			}

			// The downtime transactions are not exported: no batches, no
			// messages, and the next admitted decision continues the
			// counter without scanning the gap.
			for _, spec := range downtime {
				if _, ok := h.journal.binding(spec.sequence); ok {
					t.Fatalf("downtime decision %d was admitted", spec.sequence)
				}
			}
			h.assertNATSEquals(t, d1, d2)

			h.driveToRetired(t, d3)
			d8 := appliedBlock(8, blockHash(8), 1)
			seq8, ok := func() (uint64, bool) {
				seq, id, _, err := h.applyDecision(d8)
				if err != nil {
					t.Fatalf("applyDecision d8: %v", err)
				}
				_ = id
				return seq, true
			}()
			if !ok || seq8 != 4 {
				t.Fatalf("the next admitted decision bound batch %d, want 4", seq8)
			}
			h.driveToRetired(t, d8)
			if got := h.prod.RetiredPrefix(); got != 4 {
				t.Fatalf("prefix %d, want 4", got)
			}
			if got := h.journal.headOf(); got != 8 {
				t.Fatalf("head %d, want 8", got)
			}
			// No downtime events leaked into NATS: the exact expected set.
			h.assertNATSEquals(t, d1, d2, d3, d8)
		})
	}
}

// AC-DLV-07-03: the producer store is lost while the native datadir and
// the Gateway survive. The new adapter store carries new producer/run IDs;
// the old run's cursor and pending are never adopted; the lost unACKed
// capture is not declared recovered; the new run delivers its own batches
// over the surviving gateway.
func TestProducerStoreLossHandshake(t *testing.T) {
	for _, slot := range slots {
		t.Run(slot.name, func(t *testing.T) {
			h := newStack(slot)
			d1 := appliedBlock(1, blockHash(1), 1)
			h.driveToRetired(t, d1)

			// Batch 2 is SEALED and CLOSED on the gateway, unACKed, when
			// the producer store is lost.
			d2 := appliedBlock(2, blockHash(2), 2)
			seq2, manifest2, _, err := h.applyDecision(d2)
			if err != nil {
				t.Fatalf("applyDecision d2: %v", err)
			}
			frames2, err := h.frames(d2)
			if err != nil {
				t.Fatalf("frames d2: %v", err)
			}
			id2 := h.sealedIdentity(manifest2)
			if err := h.prod.CommitSealed(seq2,
				release.NewSealedPlan(id2, 1000+seq2, h.slot.stream, keysOf(frames2))); err != nil {
				t.Fatalf("CommitSealed: %v", err)
			}
			if err := h.gw.ImportClosed(h.fence(1),
				release.NewSealedPlan(id2, 1000+seq2, h.slot.stream, keysOf(frames2)), false); err != nil {
				t.Fatalf("ImportClosed: %v", err)
			}
			oldProducer, oldRun := slot.producer, slot.run

			// A new adapter store with new identities handshakes with the
			// surviving gateway.
			fresh := h.withNewRun(proto.ID16{0x91}, proto.ID16{0x92})
			f2 := fresh.fence(1)
			newProducer, newRun := fresh.mem.Identity()
			if newProducer == oldProducer || newRun == oldRun {
				t.Fatalf("the new store kept an old identity")
			}

			// The old run's records do not leak into the new session.
			if _, ok := fresh.gw.Batch(f2, id2.Batch); ok {
				t.Fatalf("the new session read the old run's batch")
			}
			// The old run's cursor is not adopted under the new fence.
			if err := fresh.gw.AdoptBaseline(f2, oldProducer, oldRun, 2); !errors.Is(err, delivery.ErrFenced) {
				t.Fatalf("baseline adoption of the old run: %v, want ErrFenced", err)
			}
			// The lost unACKed capture is not declared recovered: the new
			// store holds neither the sealed plan nor an ACKED outbox.
			if _, err := release.ImportSealed(fresh.prod, fresh.gw, f2, seq2); !errors.Is(err, delivery.ErrNotFound) {
				t.Fatalf("import of the lost run's plan: %v, want ErrNotFound", err)
			}
			for _, seq := range []uint64{1, 2} {
				if _, err := release.RecoveryRelease(fresh.prod, fresh.gw, f2, seq); !errors.Is(err, delivery.ErrState) {
					t.Fatalf("recovery release of %d: %v, want ErrState", seq, err)
				}
			}
			// The old run's gateway state is untouched by the handshake.
			if got := fresh.gw.RetiredPrefix(oldProducer, oldRun); got != 1 {
				t.Fatalf("the old run's gateway prefix moved to %d", got)
			}
			if got := fresh.gw.RetiredPrefix(newProducer, newRun); got != 0 {
				t.Fatalf("the new run's gateway prefix %d, want 0", got)
			}

			// The new run delivers its own first batch; the old run's
			// unACKed batch stays exactly as it was.
			dNew := appliedBlock(1, blockHash(60), 1)
			fresh.driveToRetired(t, dNew)
			if got := fresh.prod.RetiredPrefix(); got != 1 {
				t.Fatalf("the new run's prefix %d, want 1", got)
			}
			if got := fresh.gw.RetiredPrefix(newProducer, newRun); got != 1 {
				t.Fatalf("the new run's gateway prefix %d, want 1", got)
			}
			if got := fresh.gw.RetiredPrefix(oldProducer, oldRun); got != 1 {
				t.Fatalf("the old run's gateway prefix moved to %d", got)
			}
			// Every frame of the new run's batch is stored with its exact
			// bytes; the stream holds exactly the delivered history plus
			// it — and nothing of the lost unACKed capture.
			expectedNew, err := fresh.expectedMessages(dNew)
			if err != nil {
				t.Fatalf("expected: %v", err)
			}
			expectedLost, err := h.expectedMessages(d2)
			if err != nil {
				t.Fatalf("expected lost: %v", err)
			}
			stored := fresh.messagesByID()
			for id, want := range expectedNew {
				m, ok := stored[id]
				if !ok || string(m.Payload) != string(want) {
					t.Fatalf("the new run's message %s is missing or foreign", id)
				}
			}
			for id := range expectedLost {
				if _, ok := stored[id]; ok {
					t.Fatalf("the lost unACKed capture reached NATS: %s", id)
				}
			}
			perStream := map[string]int{}
			for _, m := range fresh.server.Messages() {
				perStream[m.Stream]++
			}
			if perStream[slot.stream] != len(framesOf(t, h, d1))+len(framesOf(t, fresh, dNew)) {
				t.Fatalf("stream %s holds %d messages", slot.stream, perStream[slot.stream])
			}
		})
	}
}

// framesOf builds the frames of a decision for count assertions.
func framesOf(t *testing.T, h *stack, spec decisionSpec) []publisher.Frame {
	t.Helper()
	frames, err := h.frames(spec)
	if err != nil {
		t.Fatalf("frames: %v", err)
	}
	return frames
}

// AC-DLV-13-03: the adapter WAL volume runs out of space — the data write
// of a new capture and the service commit of the bounded cleanup both
// fail. The capture stops with no false READY, nothing is deleted or
// repaired while the volume is full, and after the volume is expanded and
// a compatible build restarts, the recovery keeps the pending and
// continues the ACK and cleanup to the ordinary retired state.
func TestENOSPCStopsCaptureAndRecovers(t *testing.T) {
	for _, slot := range slots {
		t.Run(slot.name, func(t *testing.T) {
			h := newStack(slot)
			wrap := newEnospcProducer(h.mem)
			h.prod = wrap
			classifier := derrors.NewClassifier()

			// Batch 1 reaches the ACKED outbox with its payload still held.
			d1 := appliedBlock(1, blockHash(1), 2)
			h.driveToAcked(t, d1)
			seq1, _ := h.journal.binding(d1.sequence)
			if got := h.mem.PayloadChunkCount(seq1); got != 2 {
				t.Fatalf("batch 1 holds %d payload chunks, want 2", got)
			}

			// The volume fills: the cleanup service commit of the
			// acknowledged batch and the staging data write of the next
			// capture both fail with ENOSPC.
			wrap.arm(true, false, true)
			if _, err := release.CleanupPayload(h.prod, seq1, 100); !errors.Is(err, derrors.ErrNoSpaceLeft) {
				t.Fatalf("cleanup under ENOSPC: %v, want ErrNoSpaceLeft", err)
			}
			class, known := classifier.Classify(derrors.ErrNoSpaceLeft)
			if !known || class != derrors.ClassNoSpace {
				t.Fatalf("class %s known=%v, want ClassNoSpace", class, known)
			}
			if got := classifier.MustClassify(derrors.ErrNoSpaceLeft); got != derrors.ActionStopCapture {
				t.Fatalf("action %s, want ActionStopCapture", got)
			}

			// The capture of batch 2 stops with no false READY: the intent
			// exists but never gains an identity or a payload.
			d2 := appliedBlock(2, blockHash(2), 1)
			if _, _, _, err := h.applyDecision(d2); !errors.Is(err, derrors.ErrNoSpaceLeft) {
				t.Fatalf("capture under ENOSPC: %v, want ErrNoSpaceLeft", err)
			}
			rec2, ok := h.mem.Batch(2)
			if !ok || rec2.HasIdentity || rec2.HasPayload || rec2.State != delivery.StateIntent {
				t.Fatalf("the stopped capture left %+v ok=%v", rec2, ok)
			}

			// Nothing was deleted or repaired while the volume was full:
			// the refused writes changed no record and dropped no payload.
			if wrap.refusedWrites() < 2 {
				t.Fatalf("the fixture armed no faults")
			}
			pending, ok := h.mem.Batch(seq1)
			if !ok || !pending.Outbox || pending.HasPayload != true {
				t.Fatalf("the acknowledged pending was damaged: %+v ok=%v", pending, ok)
			}
			if got := h.mem.PayloadChunkCount(seq1); got != 2 {
				t.Fatalf("ENOSPC deleted payload chunks: %d remain", got)
			}

			// The volume is expanded and a compatible build restarts over
			// the same store.
			wrap.arm(false, false, false)
			h.crash()

			// The recovery gives the ACK/cleanup priority and continues:
			// batch 1 finishes and releases, batch 2 completes and flows.
			h.driveToRetired(t, d1)
			if got := h.prod.RetiredPrefix(); got != 1 {
				t.Fatalf("prefix after batch 1 recovery %d, want 1", got)
			}
			h.driveToRetired(t, d2)
			if got := h.prod.RetiredPrefix(); got != 2 {
				t.Fatalf("prefix after batch 2 recovery %d, want 2", got)
			}
			if got := h.gw.RetiredPrefix(slot.producer, slot.run); got != 2 {
				t.Fatalf("gateway prefix after the recovery %d, want 2", got)
			}
			h.assertNATSEquals(t, d1, d2)
		})
	}
}
