package native_concurrency

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/core/delivery/release"
	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/gateway/session"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
	lifecycle "github.com/sigaev-pro/bc-indexer/test/lifecycle_validator"
)

// Race 1: concurrent replays of one native decision marker — worker
// retries after a crash, a reorg replay, an old worker repeating the
// marker — never allocate a second batch for the decision and never bind a
// second manifest. Concurrent distinct decisions receive distinct
// contiguous sequences (D79, DELIVERY_STATE_MACHINE "Identity": one
// decision, one batch, no holes dropped without completion).
func TestMarkerReplayRacesAllocateOneBatch(t *testing.T) {
	for _, slot := range slots {
		t.Run(slot.name, func(t *testing.T) {
			h := newHarness(slot)
			spec := appliedBlock(1, blockHash(1), 2)

			// Eight workers replay the same marker simultaneously.
			const workers = 8
			results := make([]struct {
				seq   uint64
				fresh bool
			}, workers)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					seq, _, fresh, err := h.applyDecision(spec)
					if err != nil {
						t.Errorf("marker replay %d: %v", i, err)
						return
					}
					results[i].seq, results[i].fresh = seq, fresh
				}(i)
			}
			close(start)
			wg.Wait()

			freshCount := 0
			for i, r := range results {
				if r.seq != 1 {
					t.Fatalf("replay %d bound sequence %d, want 1", i, r.seq)
				}
				if r.fresh {
					freshCount++
				}
			}
			if freshCount != 1 {
				t.Fatalf("%d replays allocated the batch, want exactly 1", freshCount)
			}
			rec, ok := h.prod.Batch(1)
			if !ok || rec.State != delivery.StateReady || !rec.HasIdentity {
				t.Fatalf("batch 1 after the replays: state=%s identity=%v ok=%v",
					rec.State, rec.HasIdentity, ok)
			}

			// Concurrent distinct decisions: four workers, four decisions,
			// every sequence used exactly once and contiguously.
			seqs := make(chan uint64, 4)
			start2 := make(chan struct{})
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start2
					seq, _, _, err := h.applyDecision(appliedBlock(uint64(10+i), blockHash(uint64(10+i)), 1))
					if err != nil {
						t.Errorf("decision %d: %v", 10+i, err)
						return
					}
					seqs <- seq
				}(i)
			}
			close(start2)
			wg.Wait()
			close(seqs)
			seen := map[uint64]bool{}
			for seq := range seqs {
				if seq < 2 || seq > 5 {
					t.Fatalf("decision allocated out-of-range sequence %d", seq)
				}
				if seen[seq] {
					t.Fatalf("sequence %d allocated twice", seq)
				}
				seen[seq] = true
			}
			if len(seen) != 4 {
				t.Fatalf("%d distinct sequences for 4 decisions", len(seen))
			}

			// A second, different manifest under the same decision is an
			// immutable conflict: the first manifest stands and the journal
			// binding is unchanged.
			mutated := appliedBlock(1, blockHash(1), 1)
			if _, _, _, err := h.applyDecision(mutated); !errors.Is(err, delivery.ErrImmutableConflict) {
				t.Fatalf("second manifest of decision 1: %v, want ErrImmutableConflict", err)
			}
			seq, fresh, err := h.journal.mark(spec)
			if err != nil || seq != 1 || fresh {
				t.Fatalf("marker after everything: seq=%d fresh=%v err=%v, want 1/false/nil", seq, fresh, err)
			}
			if got := h.journal.headOf(); got != 13 {
				t.Fatalf("journal head %d, want 13", got)
			}
		})
	}
}

// Race 2 (AC-DLV-11-08): a Resume completed and a new batch of the current
// admission generation flows; late mutating RPCs of the previously sealed
// generation — same session token, old generation — race the live batch
// and must be fenced off without touching it.
func TestLateSealedGenerationRPCIsFencedOffLiveBatch(t *testing.T) {
	for _, slot := range slots {
		t.Run(slot.name, func(t *testing.T) {
			h := newHarness(slot)
			f1 := h.fence(1)

			// Batch 1 flows completely under admission generation 1.
			spec1 := appliedBlock(1, blockHash(1), 2)
			if _, err := h.runDecision(f1, spec1); err != nil {
				t.Fatalf("batch 1: %v", err)
			}
			if got := h.prod.RetiredPrefix(); got != 1 {
				t.Fatalf("producer prefix %d after batch 1", got)
			}

			// Drain, seal and resume: the seal closes admission generation
			// 1, the resume opens generation 2, the producer commits LIVE.
			if err := h.prod.SetMode(delivery.ModeDrain); err != nil {
				t.Fatalf("SetMode: %v", err)
			}
			engine := h.maintenanceStack()
			seal, err := engine.SealToDrained(context.Background(), f1, proto.ID16{9})
			if err != nil {
				t.Fatalf("SealToDrained: %v", err)
			}
			if seal.AdmissionGeneration != 1 {
				t.Fatalf("seal closed generation %d", seal.AdmissionGeneration)
			}
			resume, err := engine.Resume(f1, proto.ID16{10})
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			if resume.NewAdmissionGeneration != 2 || h.prod.Mode() != delivery.ModeLive {
				t.Fatalf("resume opened generation %d, producer mode %s",
					resume.NewAdmissionGeneration, h.prod.Mode())
			}

			// The session switch to the new generation: the old fence dies
			// with the old session.
			f2 := h.fence(2)
			if err := h.gw.OpenSession(f2); err != nil {
				t.Fatalf("OpenSession: %v", err)
			}

			// The new batch of the current generation reaches CLOSED.
			spec2 := appliedBlock(2, blockHash(2), 3)
			seq2, manifest2, _, err := h.applyDecision(spec2)
			if err != nil {
				t.Fatalf("applyDecision: %v", err)
			}
			frames2, err := h.frames(spec2)
			if err != nil {
				t.Fatalf("frames: %v", err)
			}
			id2 := h.sealedIdentity(manifest2)
			if err := h.prod.CommitSealed(seq2,
				release.NewSealedPlan(id2, 1000+seq2, h.slot.stream, keysOf(frames2))); err != nil {
				t.Fatalf("CommitSealed: %v", err)
			}
			if err := h.gw.ImportClosed(f2,
				release.NewSealedPlan(id2, 1000+seq2, h.slot.stream, keysOf(frames2)), false); err != nil {
				t.Fatalf("ImportClosed: %v", err)
			}

			// The live publish races late sealed-generation RPCs. The late
			// fence carries the same token and owner: the admission
			// generation alone must fence it.
			late := h.fence(1)
			lateFrames := frames2 // identical bytes: only the fence differs
			var wg sync.WaitGroup
			publishErr := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := h.engine.Publish(f2, h.target, publisher.Batch{Identity: id2, Frames: frames2})
				publishErr <- err
			}()
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					step := fmt.Sprintf("late RPC %d", i)
					if err := h.gw.ImportClosed(late,
						release.NewSealedPlan(id2, 1000+seq2, h.slot.stream, keysOf(lateFrames)),
						false); !errors.Is(err, delivery.ErrFenced) {
						t.Errorf("%s ImportClosed: %v, want ErrFenced", step, err)
					}
					if err := h.gw.RecordPubAck(late, id2.Batch, id2.PlanDigest,
						lateFrames[0].Key); !errors.Is(err, delivery.ErrFenced) {
						t.Errorf("%s RecordPubAck: %v, want ErrFenced", step, err)
					}
					if _, err := h.gw.MarkNATSACKed(late, id2); !errors.Is(err, delivery.ErrFenced) {
						t.Errorf("%s MarkNATSACKed: %v, want ErrFenced", step, err)
					}
					if err := h.gw.BeginRelease(late, id2.Batch); !errors.Is(err, delivery.ErrFenced) {
						t.Errorf("%s BeginRelease: %v, want ErrFenced", step, err)
					}
					if err := h.gw.StageFrameChunk(late, id2.Batch,
						fmt.Sprintf("late-%d", i)); !errors.Is(err, delivery.ErrFenced) {
						t.Errorf("%s StageFrameChunk: %v, want ErrFenced", step, err)
					}
					if err := h.gw.PrepareGuard(late, id2.Batch); !errors.Is(err, delivery.ErrFenced) {
						t.Errorf("%s PrepareGuard: %v, want ErrFenced", step, err)
					}
					if _, err := h.engine.Publish(late, h.target,
						publisher.Batch{Identity: id2, Frames: lateFrames}); !errors.Is(err, delivery.ErrFenced) {
						t.Errorf("%s engine publish: %v, want ErrFenced", step, err)
					}
				}(i)
			}
			wg.Wait()
			if err := <-publishErr; err != nil {
				t.Fatalf("live publish of the current generation: %v", err)
			}

			// The current LIVE and the new batch are untouched: the batch
			// carries the full PubAck set and its exact identity, and NATS
			// holds exactly the two published batches.
			rec, ok := h.gw.Batch(f2, id2.Batch)
			if !ok || rec.State != delivery.StateNATSACKed || !rec.HasAck {
				t.Fatalf("batch 2 after the races: state=%s ack=%v ok=%v",
					rec.State, rec.HasAck, ok)
			}
			if !rec.Ack.Identity.SameIdentity(id2) {
				t.Fatalf("batch 2 ack binds another identity")
			}
			msgs := h.messagesByID()
			if len(msgs) != 7 { // 3 frames of batch 1 + 4 frames of batch 2
				t.Fatalf("NATS holds %d unique messages, want 7", len(msgs))
			}
			// The late RPCs created no state: sequence 1 stays retired and
			// no third batch exists.
			if _, ok := h.gw.Batch(f2, delivery.BatchID{Producer: h.slot.producer, Run: h.slot.run, Seq: 3}); ok {
				t.Fatalf("a late RPC created a batch record")
			}

			// The delivery of the live batch completes and the retired
			// prefix advances to it.
			ack2 := delivery.IngestAck{Identity: id2}
			if err := h.finish(f2, seq2, ack2); err != nil {
				t.Fatalf("finish batch 2: %v", err)
			}
			if got := h.prod.RetiredPrefix(); got != 2 {
				t.Fatalf("producer prefix %d after batch 2", got)
			}
		})
	}
}

// Race 3: the publisher barrier of a Release crossing an old worker
// mid-run fences the rest of its run, the already-sent frame may return as
// a duplicate but never as a second action, a new run of the same worker
// completes the batch with identical bytes, and a late PubAck after
// retirement resurrects nothing (DELIVERY_STATE_MACHINE, "Fencing").
func TestPublisherBarrierMidRunAndLatePubAck(t *testing.T) {
	for _, slot := range slots {
		t.Run(slot.name, func(t *testing.T) {
			h := newHarness(slot)
			f := h.fence(1)
			spec := appliedBlock(1, blockHash(1), 3) // block + 3 tx = 4 frames
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

			// The old worker publishes through a connection that raises the
			// publisher barrier exactly when the third frame answers — the
			// Release barrier crossing the run.
			wrapped := newBarrierConn(h.server, 3)
			worker, err := publisher.New(wrapped, h.gw)
			if err != nil {
				t.Fatalf("worker engine: %v", err)
			}
			wrapped.raise = worker.Barrier
			if _, err := worker.Publish(f, h.target, publisher.Batch{Identity: id, Frames: frames}); !errors.Is(err, delivery.ErrFenced) {
				t.Fatalf("mid-run barrier: %v, want ErrFenced", err)
			}
			// The frames before the barrier are durably accounted; the
			// in-flight one is on the wire but not accounted; the last was
			// never sent.
			rec, ok := h.gw.Batch(f, id.Batch)
			if !ok || len(rec.Acked) != 2 {
				t.Fatalf("accounted %d frames after the barrier, want 2", len(rec.Acked))
			}

			// The same worker retries with a new generation: the accounted
			// frames are skipped, the in-flight frame returns as a
			// duplicate of the same message id, and the batch completes
			// with the identical bytes.
			ack, err := worker.Publish(f, h.target, publisher.Batch{Identity: id, Frames: frames})
			if err != nil {
				t.Fatalf("retry after the barrier: %v", err)
			}
			if !ack.Identity.SameIdentity(id) {
				t.Fatalf("retry ack binds another identity")
			}
			msgs := h.messagesByID()
			if len(msgs) != 4 {
				t.Fatalf("NATS holds %d unique messages, want 4", len(msgs))
			}
			for i, frame := range frames {
				m, ok := msgs[frame.MessageID]
				if !ok {
					t.Fatalf("frame %d never stored", i)
				}
				if string(m.Payload) != string(frame.Payload) || m.Subject != frame.Subject {
					t.Fatalf("frame %d stored with foreign bytes or route", i)
				}
			}

			// The release retires the batch; late PubAck answers and a late
			// publish then resurrect nothing.
			if err := h.finish(f, seq, ack); err != nil {
				t.Fatalf("finish: %v", err)
			}
			if _, ok := h.gw.Batch(f, id.Batch); ok {
				t.Fatalf("the retired batch record survived")
			}
			if err := h.gw.RecordPubAck(f, id.Batch, id.PlanDigest, frames[0].Key); !errors.Is(err, delivery.ErrAlreadyRetired) {
				t.Fatalf("late PubAck of a retired batch: %v, want ErrAlreadyRetired", err)
			}
			if _, err := worker.Publish(f, h.target, publisher.Batch{Identity: id, Frames: frames}); !errors.Is(err, delivery.ErrAlreadyRetired) {
				t.Fatalf("late publish of a retired batch: %v, want ErrAlreadyRetired", err)
			}
			if len(h.messagesByID()) != 4 {
				t.Fatalf("a late answer stored new messages")
			}
		})
	}
}

// Race 3b: the publisher gate of E03-02.fencing under real concurrency —
// callbacks of stale generations and callbacks of released batches never
// run their effect, duplicates of still-valid callbacks stay idempotent,
// and every successful Apply ran its effect exactly once.
func TestPublisherGateConcurrentCallbacks(t *testing.T) {
	gate := session.NewPublisherGate()
	first := session.Callback{Batch: batchKeyOf(1), PlanDigest: proto.Hash32{1}, Generation: gate.Generation()}
	released := session.Callback{Batch: batchKeyOf(2), PlanDigest: proto.Hash32{2}, Generation: gate.Generation()}

	// A duplicate of a still-valid callback stays allowed: already-sent
	// network bytes cannot be recalled, the journal answers idempotently.
	runs := 0
	if err := gate.Apply(first, func() error { runs++; return nil }); err != nil {
		t.Fatalf("first callback: %v", err)
	}
	if err := gate.Apply(first, func() error { runs++; return nil }); err != nil {
		t.Fatalf("duplicate callback: %v", err)
	}
	if runs != 2 {
		t.Fatalf("the gate ran the duplicate %d times", runs)
	}

	// Race: eight goroutines keep applying callbacks of the opening
	// generation while the generation rises (SealGeneration/Release) and a
	// batch barrier goes up. Whatever interleaving wins: the released batch
	// never runs, and successes equal effects.
	var effects atomic.Int64
	var successes atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 50; j++ {
				cb := session.Callback{Batch: batchKeyOf(1), PlanDigest: proto.Hash32{1},
					Generation: gate.Generation()}
				if err := gate.Apply(cb, func() error { effects.Add(1); return nil }); err == nil {
					successes.Add(1)
				} else if !errors.Is(err, session.ErrCallbackStale) {
					t.Errorf("apply: %v", err)
					return
				}
			}
		}()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for j := 0; j < 10; j++ {
			gate.NewGeneration()
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		gate.Release(released.Batch)
	}()
	close(start)
	wg.Wait()

	if got := effects.Load(); got != successes.Load() {
		t.Fatalf("%d effects for %d successful applies", got, successes.Load())
	}
	// After everything settled, the fenced answers are final.
	if err := gate.Apply(first, func() error { t.Errorf("a stale callback ran"); return nil }); !errors.Is(err, session.ErrCallbackStale) {
		t.Fatalf("stale callback: %v, want ErrCallbackStale", err)
	}
	if err := gate.Apply(session.Callback{Batch: released.Batch, PlanDigest: released.PlanDigest,
		Generation: gate.Generation()}, func() error {
		t.Errorf("a callback of a released batch ran")
		return nil
	}); !errors.Is(err, session.ErrBatchReleased) {
		t.Fatalf("released callback: %v, want ErrBatchReleased", err)
	}
}

// Race 4: the session gate — Release and SealGeneration raise barriers
// while mutations are in flight; a mutation that passed validation
// completes, every mutation after the barrier fails without running, a
// session switch (Revoke) fences its old token, and an epoch below the
// barrier can never open new work.
func TestGateBarrierRacesMutations(t *testing.T) {
	gate := session.NewGate()
	token, err := gate.Open(1)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var effects, successes atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				err := gate.Mutate(token, func() error { effects.Add(1); return nil })
				if err == nil {
					successes.Add(1)
				} else if !errors.Is(err, session.ErrSessionStale) {
					t.Errorf("mutate: %v", err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for j := 0; j < 20; j++ {
			gate.Raise(uint64(1 + j)) // Release/Seal barriers rise monotonically
		}
	}()
	close(start)
	wg.Wait()

	if got := effects.Load(); got != successes.Load() {
		t.Fatalf("%d effects for %d successful mutations", got, successes.Load())
	}
	if err := gate.Mutate(token, func() error {
		t.Errorf("a fenced mutation ran")
		return nil
	}); !errors.Is(err, session.ErrSessionStale) {
		t.Fatalf("old token after the barriers: %v, want ErrSessionStale", err)
	}
	// An epoch below the barrier is a generation conflict: an old seal
	// cannot open new work.
	if _, err := gate.Open(1); !errors.Is(err, session.ErrGenerationConflict) {
		t.Fatalf("open below the barrier: %v, want ErrGenerationConflict", err)
	}
	// The next session at the current barrier works until the switch
	// revokes it.
	fresh, err := gate.Open(21)
	if err != nil {
		t.Fatalf("open at the barrier: %v", err)
	}
	if err := gate.Mutate(fresh, func() error { return nil }); err != nil {
		t.Fatalf("mutate at the barrier: %v", err)
	}
	gate.Revoke(fresh.SessionID)
	if err := gate.Mutate(fresh, func() error { return nil }); !errors.Is(err, session.ErrSessionStale) {
		t.Fatalf("revoked session: %v, want ErrSessionStale", err)
	}
}

// Race 5: a canonical reorg produces the Removed decision of an Applied
// activation while the Applied batch is still publishing. The Removed
// batch exists regardless of the Applied PubAck state, both batches
// deliver exactly once, every delivery order of the two decisions folds
// to the same terminal lifecycle state, late Applied evidence never
// reactivates the removed activation, and the marker replay of the applied
// decision after the reorg returns its former batch (the
// applied-decision frontier suppresses a second application).
func TestReorgRemovalRacesPublishDelivery(t *testing.T) {
	for _, slot := range slots {
		t.Run(slot.name, func(t *testing.T) {
			h := newHarness(slot)
			f := h.fence(1)
			spec1 := appliedBlock(1, blockHash(1), 2)
			seq1, manifest1, _, err := h.applyDecision(spec1)
			if err != nil {
				t.Fatalf("applyDecision: %v", err)
			}
			frames1, err := h.frames(spec1)
			if err != nil {
				t.Fatalf("frames: %v", err)
			}
			id1 := h.sealedIdentity(manifest1)
			if err := h.prod.CommitSealed(seq1,
				release.NewSealedPlan(id1, 1000+seq1, h.slot.stream, keysOf(frames1))); err != nil {
				t.Fatalf("CommitSealed: %v", err)
			}
			if err := h.gw.ImportClosed(f,
				release.NewSealedPlan(id1, 1000+seq1, h.slot.stream, keysOf(frames1)), false); err != nil {
				t.Fatalf("ImportClosed: %v", err)
			}

			// The reorg decision: Removed of every entity of decision 1.
			spec2 := lifecycleOf(2, 1, blockHash(1), semantic.KindRemoved, 1)
			var wg sync.WaitGroup
			publishErr := make(chan error, 1)
			reorgErr := make(chan error, 1)
			wg.Add(2)
			go func() {
				defer wg.Done()
				// The Applied batch publishes while the reorg decision is
				// being made.
				_, err := h.engine.Publish(f, h.target, publisher.Batch{Identity: id1, Frames: frames1})
				publishErr <- err
			}()
			go func() {
				defer wg.Done()
				// The Removed batch is created regardless of the Applied
				// PubAck state: the reorg decision allocates its own batch.
				seq2, manifest2, _, err := h.applyDecision(spec2)
				if err != nil {
					reorgErr <- err
					return
				}
				frames2, err := h.frames(spec2)
				if err != nil {
					reorgErr <- err
					return
				}
				id2 := h.sealedIdentity(manifest2)
				if err := h.prod.CommitSealed(seq2,
					release.NewSealedPlan(id2, 1000+seq2, h.slot.stream, keysOf(frames2))); err != nil {
					reorgErr <- err
					return
				}
				if err := h.gw.ImportClosed(f,
					release.NewSealedPlan(id2, 1000+seq2, h.slot.stream, keysOf(frames2)), false); err != nil {
					reorgErr <- err
					return
				}
				reorgErr <- nil
			}()
			wg.Wait()
			if err := <-publishErr; err != nil {
				t.Fatalf("applied publish: %v", err)
			}
			if err := <-reorgErr; err != nil {
				t.Fatalf("reorg decision: %v", err)
			}

			// Both batches delivered: the Removed batch publishes its own
			// frames; NATS holds each message exactly once.
			spec2Frames, err := h.frames(spec2)
			if err != nil {
				t.Fatalf("removed frames: %v", err)
			}
			seq2, ok := h.journal.binding(spec2.sequence)
			if !ok {
				t.Fatalf("the reorg decision allocated no batch")
			}
			manifest2, err := h.manifestIdentity(spec2, seq2)
			if err != nil {
				t.Fatalf("removed manifest: %v", err)
			}
			id2 := h.sealedIdentity(manifest2)
			ack2, err := h.publish(f, id2, spec2Frames)
			if err != nil {
				t.Fatalf("removed publish: %v", err)
			}
			msgs := h.messagesByID()
			if len(msgs) != len(frames1)+len(spec2Frames) {
				t.Fatalf("NATS holds %d unique messages, want %d",
					len(msgs), len(frames1)+len(spec2Frames))
			}
			if err := h.finish(f, seq1, delivery.IngestAck{Identity: id1}); err != nil {
				t.Fatalf("finish applied batch: %v", err)
			}
			if err := h.finish(f, ack2.Identity.Batch.Seq, ack2); err != nil {
				t.Fatalf("finish removed batch: %v", err)
			}
			if got := h.prod.RetiredPrefix(); got != 2 {
				t.Fatalf("producer prefix %d after both batches", got)
			}

			// The lifecycle oracle: every delivery order of the six events
			// (three Applied, three Removed) converges to the same
			// terminal records; the removed-first orders prove a late
			// Applied cannot resurrect the activation.
			applied, err := h.journal.descriptorsOf(spec1)
			if err != nil {
				t.Fatalf("applied descriptors: %v", err)
			}
			removed, err := h.journal.descriptorsOf(spec2)
			if err != nil {
				t.Fatalf("removed descriptors: %v", err)
			}
			events := append(append([]semantic.SemanticDescriptor{}, applied...), removed...)
			orders := [][]int{
				{0, 1, 2, 3, 4, 5}, {5, 4, 3, 2, 1, 0}, {3, 4, 5, 0, 1, 2},
				{0, 3, 1, 4, 2, 5}, {5, 0, 4, 1, 3, 2}, {2, 1, 0, 5, 4, 3},
				{3, 0, 4, 2, 1, 5}, {1, 5, 0, 4, 3, 2},
			}
			results := make([]map[lifecycle.ActivationKey]lifecycle.Record, len(orders))
			staleSeen := make([]bool, len(orders))
			var foldWG sync.WaitGroup
			for i, order := range orders {
				foldWG.Add(1)
				go func(i int, order []int) {
					defer foldWG.Done()
					records, stale, err := foldOrder(events, order)
					if err != nil {
						t.Errorf("fold %d: %v", i, err)
						return
					}
					results[i] = records
					staleSeen[i] = stale
				}(i, order)
			}
			foldWG.Wait()
			for i := range results {
				if results[i] == nil {
					t.Fatalf("fold %d produced no records", i)
				}
				if len(results[i]) != 3 {
					t.Fatalf("fold %d holds %d records, want 3", i, len(results[i]))
				}
				for key, rec := range results[i] {
					if rec.State != lifecycle.StateRemoved {
						t.Fatalf("fold %d leaves activation %v %s", i, key, rec.State)
					}
					if !rec.Equal(results[0][key]) {
						t.Fatalf("fold %d diverges from fold 0 at %v", i, key)
					}
				}
			}
			// The removed-first folds saw the late Applied as stale
			// evidence, and the applied-first folds saw Removed terminate
			// the activation; both ends are the same terminal state.
			if !staleSeen[2] || staleSeen[0] {
				t.Fatalf("stale flags: removed-first=%v applied-first=%v", staleSeen[2], staleSeen[0])
			}

			// The applied-decision frontier: after the reorg, the marker
			// replay of decision 1 returns its former batch and cannot
			// create a second application.
			seqR, freshR, err := h.journal.mark(spec1)
			if err != nil || seqR != 1 || freshR {
				t.Fatalf("marker replay after the reorg: seq=%d fresh=%v err=%v", seqR, freshR, err)
			}
			if seqAgain, idAgain, freshAgain, err := h.applyDecision(spec1); err != nil ||
				seqAgain != 1 || freshAgain || !idAgain.SameIdentity(manifest1) {
				t.Fatalf("re-drive of decision 1: seq=%d fresh=%v id=%v err=%v",
					seqAgain, freshAgain, idAgain, err)
			}
			if got := len(h.messagesByID()); got != len(frames1)+len(spec2Frames) {
				t.Fatalf("the replay stored %d messages", got)
			}
		})
	}
}

// foldOrder folds the events in the given order into a fresh automaton and
// returns the final records plus whether any event folded as stale.
func foldOrder(events []semantic.SemanticDescriptor, order []int) (
	map[lifecycle.ActivationKey]lifecycle.Record, bool, error) {
	automaton := lifecycle.New()
	stale := false
	for _, idx := range order {
		d := events[idx]
		digest, err := d.Digest()
		if err != nil {
			return nil, false, err
		}
		result, err := automaton.Apply(d, lifecycle.Digest(digest))
		if err != nil {
			return nil, false, err
		}
		if result.Class == lifecycle.ClassStale {
			stale = true
		}
	}
	return automaton.Records(), stale, nil
}

// batchKeyOf derives the 40-byte batch key of a small sequence.
func batchKeyOf(seq uint64) [40]byte {
	var key [40]byte
	key[39] = byte(seq)
	return key
}

// barrierConn wraps a Conn and raises a publisher barrier right after the
// n-th publish answers, modeling the Release or SealGeneration barrier
// crossing a worker mid-run.
type barrierConn struct {
	publisher.Conn
	mu      sync.Mutex
	count   int
	raiseAt int
	raise   func() uint64
}

func newBarrierConn(conn publisher.Conn, raiseAt int) *barrierConn {
	return &barrierConn{Conn: conn, raiseAt: raiseAt}
}

// Publish forwards the publish and raises the barrier at the fixed count.
func (c *barrierConn) Publish(subject string, header, payload []byte) (publisher.PubAck, error) {
	ack, err := c.Conn.Publish(subject, header, payload)
	c.mu.Lock()
	c.count++
	raise := c.raise
	due := c.count == c.raiseAt
	c.mu.Unlock()
	if due && raise != nil {
		raise()
	}
	return ack, err
}
