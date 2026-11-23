package poly

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/adapters/bor/finality_observer"
	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// newTestChain imports the first canonical blocks with one milestone
// whitelist observed at height 1, and returns the chain, the driver
// session over a fresh journal and the underlying memory storage.
func newTestChain(t *testing.T, blocks int) (*chain, *driver, *storage.MemoryStorage) {
	t.Helper()
	c := newChain()
	shared, err := newFixture(c)
	if err != nil {
		t.Fatal(err)
	}
	mem := storage.NewMemory()
	journal, err := openJournal(mem)
	if err != nil {
		t.Fatal(err)
	}
	d := attachDriver(c, journal, shared)
	// The first block imports with empty markers; a milestone whitelist
	// at it then covers the remaining imports, so the later snapshots
	// carry the real Polygon finality markers.
	if _, err := d.importBlocks(emptyMarkers(), 1); err != nil {
		t.Fatal(err)
	}
	markers, err := d.observeMilestone(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.importBlocks(markers, blocks-1); err != nil {
		t.Fatal(err)
	}
	return c, d, mem
}

// emptyMarkers is the marker set before any finality observation.
func emptyMarkers() block.PolygonMarkers {
	return block.PolygonMarkers{
		Milestone:       block.NotObserved(),
		Checkpoint:      block.NotObserved(),
		NativeFinalized: block.NotObserved(),
		L1Checkpoint:    block.NotObserved(),
	}
}

// canonHash returns the canonical hash at a height.
func (c *chain) canonHash(t *testing.T, n uint64) block.Hash32 {
	t.Helper()
	a, ok := c.canonicalAnchor(n)
	if !ok {
		t.Fatalf("fixture: no canonical block at %d", n)
	}
	return a.Hash
}

// block returns one stored block by hash.
func (c *chain) block(h block.Hash32) *blockInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.blocks[h]
}

// currentMarkers reads the current Polygon markers of the observation
// stream.
func (d *driver) currentMarkers(t *testing.T) block.PolygonMarkers {
	t.Helper()
	markers, err := d.shared.obs.Markers()
	if err != nil {
		t.Fatal(err)
	}
	return markers
}

// sideBranch stores one known branch of count blocks from a parent.
func sideBranch(c *chain, name string, from uint64, parent block.Hash32, count int) *blockInfo {
	var tip *blockInfo
	for i := 0; i < count; i++ {
		tip = c.side(name, from+uint64(i), parent, sprintStateSync(from+uint64(i)))
		parent = tip.Hash
	}
	return tip
}

// extendBranch grows a known branch from tip by count blocks.
func extendBranch(c *chain, name string, tip *blockInfo, count int) *blockInfo {
	parent := tip.Hash
	for i := 1; i <= count; i++ {
		next := c.side(name, tip.Number+uint64(i), parent, sprintStateSync(tip.Number+uint64(i)))
		parent = next.Hash
		tip = next
	}
	return tip
}

// sealAndAck drives one READY batch through SEALED to the verified
// IngestAck: the full PubAck of the delivery.
func sealAndAck(t *testing.T, d *driver, seq uint64) {
	t.Helper()
	info, err := d.journal.Batch(seq)
	if err != nil {
		t.Fatal(err)
	}
	if info.Ready == nil {
		t.Fatalf("batch %d is not READY", seq)
	}
	gen, _, err := d.journal.BeginPlan(seq, []byte("seed"), [32]byte{7}, []byte("header"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.journal.WritePlanChunk(seq, gen, 0, []byte("frame-bytes")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.journal.Seal(seq, gen, uint64(len("frame-bytes"))); err != nil {
		t.Fatal(err)
	}
	info, err = d.journal.Batch(seq)
	if err != nil {
		t.Fatal(err)
	}
	ack := producer.IngestAck{
		ManifestDigest: info.Ready.ManifestDigest,
		PlanDigest:     info.PlanDigest,
		LayoutDigest:   info.LayoutDigest,
		BindingDigest:  testBinding,
		IssuerEpoch:    idOf("gateway"),
	}
	if err := d.journal.OnIngestAck(seq, ack); err != nil {
		t.Fatal(err)
	}
}

// AC-CAP-08-02, POLY slot: one native reorg decision replaces several
// blocks; the manifest is built from chunks; a crash inside the build and
// the repeat of the same native marker restore the one former batch_id
// with the full ordered set of Removed/Applied descriptors; the partial
// manifest is not READY and the events are not split into block batches.
func TestReorgOneDecisionOneBatchCrashAndRepeat(t *testing.T) {
	c, d, mem := newTestChain(t, 10)
	const reorgBatch = 11 // the ten imports own batch sequences 1..10

	// The live activations the decision replaces.
	old := []*blockInfo{}
	for n := uint64(8); n <= 10; n++ {
		old = append(old, c.block(c.canonHash(t, n)))
	}

	// A known branch replaces the canonical blocks 8..10.
	tip := sideBranch(c, "b1", 8, c.canonHash(t, 7), 3)

	// Power loss inside the manifest build: from the 10th journal write
	// of this session on (the binding is idempotent, so the writes are
	// the allocation, the attempt, the event data chunks, then the
	// manifest chunks) every write fails atomically.
	crash := &failingStorage{inner: mem, failFrom: 10, err: errors.New("power loss inside the manifest build")}
	crashJournal, err := openJournal(crash)
	if err != nil {
		t.Fatal(err)
	}
	crashed := attachDriver(c, crashJournal, d.shared)
	decision, events, err := crashed.reorgDecision(tip, d.currentMarkers(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := crashed.commitReorg(decision, events); err == nil {
		t.Fatal("the injected crash must fail the build")
	}

	// The partial manifest is not READY and no lifecycle state changed.
	partial, err := crashJournal.Batch(reorgBatch)
	if err != nil {
		t.Fatal(err)
	}
	if partial.State == producer.StateReady || partial.State == producer.StateSealed {
		t.Fatalf("partial manifest state %s, want below READY", partial.State)
	}
	for _, b := range old {
		period := d.shared.periods[b.Hash][len(d.shared.periods[b.Hash])-1]
		if period.State != block.StateActive {
			t.Fatalf("block %d folded to %s before the READY commit", b.Number, period.State)
		}
	}

	// Restart over the surviving bytes and repeat the same native marker.
	journal, err := openJournal(mem)
	if err != nil {
		t.Fatal(err)
	}
	restarted := attachDriver(c, journal, d.shared)
	if _, err := restarted.commitReorg(decision, events); err != nil {
		t.Fatal(err)
	}

	// One former batch_id restored with the full ordered set.
	info, err := journal.Batch(reorgBatch)
	if err != nil {
		t.Fatal(err)
	}
	if info.State != producer.StateReady {
		t.Fatalf("recovered batch state %s, want READY", info.State)
	}
	if info.Ready.EventCount != uint64(len(events)) {
		t.Fatalf("event count %d, want %d in one batch", info.Ready.EventCount, len(events))
	}
	// The reorg decision maps to exactly one batch: replaying the marker
	// returns the same batch, and the next decision consumes the next
	// sequence, so the events were not split into per-block batches.
	att, replayed, err := journal.AllocateBatch(decision, uint64(len(events)))
	if err != nil || !replayed || att.BatchSeq != reorgBatch {
		t.Fatalf("replayed marker: att=%v replayed=%v err=%v, want batch %d", att, replayed, err, reorgBatch)
	}
	next, _, err := journal.AllocateBatch(9999, 1)
	if err != nil {
		t.Fatal(err)
	}
	if next.BatchSeq != reorgBatch+1 {
		t.Fatalf("the reorg consumed %d batches, want exactly one", next.BatchSeq-reorgBatch)
	}

	// The ordered set: Removed 8..10 ascending, then Applied 8'..10'.
	if len(events) != 6 {
		t.Fatalf("%d events, want 6 (3 removed + 3 applied)", len(events))
	}
	for i, er := range events {
		if i < 3 {
			if er.event.Kind != block.EventRemoved {
				t.Fatalf("event %d is %s, want REMOVED first", i, er.event.Kind)
			}
			if er.event.Revision == 0 || er.event.Reason != block.RemovalReasonNativeCanonicalChange {
				t.Fatal("a removal carries a fresh revision and the native canonical change reason")
			}
			period := d.shared.periods[er.event.Activation.BlockHash]
			if period[len(period)-1].State != block.StateRemoved {
				t.Fatal("the removed activation is terminal after the fold")
			}
		} else if er.event.Kind != block.EventApplied {
			t.Fatalf("event %d is %s, want APPLIED", i, er.event.Kind)
		}
	}
	// The manifest decodes back to the full ordered set and the READY
	// commit covers it completely.
	manifest, err := encodeManifest(eventsOf(events))
	if err != nil {
		t.Fatal(err)
	}
	if info.Ready.ManifestLength != uint64(len(manifest)) {
		t.Fatal("the ready manifest length must cover the whole ordered set")
	}
	decoded, err := decodeManifest(manifest)
	if err != nil || len(decoded) != len(events) {
		t.Fatalf("manifest decode: %v %d", err, len(decoded))
	}
	// The snapshots of the promoted blocks carry the real Polygon
	// milestone marker of the observation stream.
	if markers := events[3].event.Snapshot.Markers.Polygon; markers == nil || markers.Milestone.Kind != block.ReferenceKnown {
		t.Fatal("the applied snapshot must carry the known Polygon milestone marker")
	}
}

// Removed is created for a READY Applied activation regardless of the
// transport PubAck: the reorg decision and its batch complete with the
// unacked batch still pending delivery.
func TestRemovedRegardlessOfPubAck(t *testing.T) {
	for _, tc := range []struct {
		name   string
		puback func(t *testing.T, d *driver, seq uint64)
	}{
		{"acked before the reorg", sealAndAck},
		{"puback never arrived", func(*testing.T, *driver, uint64) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, d, _ := newTestChain(t, 10)
			// The import batches of blocks 9 and 10 stay READY; block 9's
			// batch gets the full PubAck in the acked variant.
			tc.puback(t, d, 9)
			tip := sideBranch(c, "b2", 9, c.canonHash(t, 8), 2)
			decision, events, err := d.reorgDecision(tip, d.currentMarkers(t))
			if err != nil {
				t.Fatal(err)
			}
			seq, err := d.commitReorg(decision, events)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 4 {
				t.Fatalf("%d events, want 2 removed + 2 applied", len(events))
			}
			for i := 0; i < 2; i++ {
				if events[i].event.Kind != block.EventRemoved {
					t.Fatalf("event %d is %s, want REMOVED", i, events[i].event.Kind)
				}
			}
			info, err := d.journal.Batch(seq)
			if err != nil {
				t.Fatal(err)
			}
			if info.State != producer.StateReady {
				t.Fatalf("the reorg batch is %s, want READY", info.State)
			}
			// The unacked import batch is still pending its delivery: the
			// reorg did not wait for it and did not retract its bytes.
			pending, err := d.journal.Batch(10)
			if err != nil {
				t.Fatal(err)
			}
			if pending.State != producer.StateReady {
				t.Fatalf("the unacked import batch is %s, want READY", pending.State)
			}
		})
	}
}

// Reapply: a block removed by a reorg that returns to the canonical chain
// on a longer known branch gets a new canonical period under the new
// decision; the old period stays terminal and a REMOVED activation is
// never reactivated.
func TestReapplyNewCanonicalPeriod(t *testing.T) {
	c, d, _ := newTestChain(t, 9)
	original8 := c.block(c.canonHash(t, 8))
	original9 := c.block(c.canonHash(t, 9))

	// The first reorg removes 8 and 9 in favor of the side branch.
	tip := sideBranch(c, "b3", 8, c.canonHash(t, 7), 2)
	decision, events, err := d.reorgDecision(tip, d.currentMarkers(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.commitReorg(decision, events); err != nil {
		t.Fatal(err)
	}
	removed := d.shared.periods[original8.Hash]
	if len(removed) != 1 || removed[0].State != block.StateRemoved {
		t.Fatalf("the original block must be REMOVED, got %d periods state %s", len(removed), removed[0].State)
	}
	// A terminal activation is never reactivated by a direct event.
	stale := block.LifecycleEvent{
		Activation: removed[0].Activation,
		Kind:       block.EventStatusChanged,
		Revision:   2,
		Snapshot:   d.shared.records[original8.Hash][0].InitialSnapshot,
	}
	if _, err := removed[0].Apply(stale); !errors.Is(err, block.ErrTerminal) {
		t.Fatalf("terminal apply error %v, want ErrTerminal", err)
	}

	// The known branch grows longer and wins: the original blocks return.
	returnTip := extendBranch(c, "orig", original9, 2)
	decision2, events2, err := d.reorgDecision(returnTip, d.currentMarkers(t))
	if err != nil {
		t.Fatal(err)
	}
	seq2, err := d.commitReorg(decision2, events2)
	if err != nil {
		t.Fatal(err)
	}

	periods := d.shared.periods[original8.Hash]
	if len(periods) != 2 {
		t.Fatalf("the returning block must open a second canonical period, got %d", len(periods))
	}
	if periods[0].State != block.StateRemoved || periods[1].State != block.StateActive {
		t.Fatalf("periods %s then %s, want REMOVED then ACTIVE", periods[0].State, periods[1].State)
	}
	if periods[1].Activation.ActivationID.DecisionSequence == periods[0].Activation.ActivationID.DecisionSequence {
		t.Fatal("a returning block is a new decision, never the old activation")
	}
	if len(d.shared.records[original8.Hash]) != 2 {
		t.Fatal("each canonical period has its own immutable record")
	}
	// One batch for the whole switch-back decision.
	info, err := d.journal.Batch(seq2)
	if err != nil {
		t.Fatal(err)
	}
	if info.Ready.EventCount != uint64(len(events2)) {
		t.Fatal("the reapply decision is one batch with its full event set")
	}
}

// Reordered events of one activation have one unambiguous result on the
// lifecycle automaton: a StatusChanged delivered before the Applied makes
// the activation PENDING_APPLIED until the Applied folds it to ACTIVE
// with the latest evidence, and a stale lower revision never replaces a
// durable newer one.
func TestReorderedEventsOneResult(t *testing.T) {
	c, d, _ := newTestChain(t, 5)
	b, ok := c.canonicalAnchor(4)
	if !ok {
		t.Fatal("fixture: block 4 must exist")
	}
	ref := d.shared.periods[b.Hash][0].Activation

	snapshotA := d.shared.records[b.Hash][0].InitialSnapshot
	snapshotB := snapshotA
	snapshotB.ObservedExecutionHead = c.headAnchor()
	snapshotC := snapshotB
	snapshotC.NativeDecision.DecisionSequence = 999

	applied := block.LifecycleEvent{Activation: ref, Kind: block.EventApplied, Snapshot: snapshotA}
	status1 := block.LifecycleEvent{Activation: ref, Kind: block.EventStatusChanged, Revision: 1, Snapshot: snapshotB}
	status2 := block.LifecycleEvent{Activation: ref, Kind: block.EventStatusChanged, Revision: 2, Snapshot: snapshotC}

	// In-order delivery.
	inOrder, err := block.StartApplied(applied)
	if err != nil {
		t.Fatal(err)
	}
	inOrder, err = inOrder.Apply(status1)
	if err != nil {
		t.Fatal(err)
	}
	inOrder, err = inOrder.Apply(status2)
	if err != nil {
		t.Fatal(err)
	}

	// Reordered delivery: both status events before the Applied.
	pending, err := block.StartPending(status1)
	if err != nil {
		t.Fatal(err)
	}
	pending, err = pending.Apply(status2)
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := pending.Apply(applied)
	if err != nil {
		t.Fatal(err)
	}

	if reordered.State != inOrder.State || reordered.Revision != inOrder.Revision {
		t.Fatalf("reorder result %s rev %d, want %s rev %d", reordered.State, reordered.Revision, inOrder.State, inOrder.Revision)
	}
	if !reordered.Snapshot.Equal(inOrder.Snapshot) {
		t.Fatal("the reorder converges to the latest full evidence")
	}

	// A stale lower revision after the newer durable one is refused, not
	// folded.
	if _, err := inOrder.Apply(status1); !errors.Is(err, block.ErrStaleRevision) {
		t.Fatalf("stale status error %v, want ErrStaleRevision", err)
	}
	// The same revision with different evidence is a conflict.
	conflict := status2
	conflict.Snapshot = snapshotB
	if _, err := inOrder.Apply(conflict); !errors.Is(err, block.ErrRevisionConflict) {
		t.Fatalf("revision conflict error %v, want ErrRevisionConflict", err)
	}
}

// The StateSync lifecycle: sprint-end blocks carry the observed StateSync
// records; a reorg creates Removed for a READY — even PubAcked — sprint
// end; the reapply opens a new period without rewriting the immutable
// removed record.
func TestStateSyncLifecycle(t *testing.T) {
	c, d, _ := newTestChain(t, 8)
	sprintEnd, ok := c.canonicalAnchor(8)
	if !ok || sprintEnd.Hash.IsZero() {
		t.Fatal("fixture: block 8 must exist")
	}
	original7 := c.block(c.canonHash(t, 7))

	// The sprint ends 4 and 8 carry StateSync observations; a plain block
	// does not.
	for _, n := range []uint64{4, 8} {
		record := d.shared.records[c.canonHash(t, n)][0]
		count, ok := record.Metadata.Details.Bor.StateSyncCount.Get()
		if !ok || count == 0 {
			t.Fatalf("block %d must observe its StateSync work", n)
		}
		mode, ok := record.Metadata.Details.Bor.StateSyncMode.Get()
		if !ok || mode != block.StateSyncEndOfBlockCommit {
			t.Fatalf("block %d StateSync mode %v", n, mode)
		}
	}
	plain := d.shared.records[c.canonHash(t, 5)][0]
	if count, ok := plain.Metadata.Details.Bor.StateSyncCount.Get(); ok && count != 0 {
		t.Fatalf("block 5 StateSync count %d, want an observed zero or absence", count)
	}
	if _, hasMode := plain.Metadata.Details.Bor.StateSyncMode.Get(); hasMode {
		t.Fatal("a block without StateSync work carries no mode")
	}

	// The sprint end is published and fully acked, then reorged away: the
	// Removed is created regardless of the PubAck.
	sealAndAck(t, d, 8)
	tip := sideBranch(c, "b4", 7, c.canonHash(t, 6), 2)
	decision, events, err := d.reorgDecision(tip, d.currentMarkers(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.commitReorg(decision, events); err != nil {
		t.Fatal(err)
	}
	sprintRemoved := false
	for _, er := range events {
		if er.event.Kind == block.EventRemoved && er.event.Activation.BlockHash == sprintEnd.Hash {
			sprintRemoved = true
		}
	}
	if !sprintRemoved {
		t.Fatal("the PubAcked sprint end must get its Removed event")
	}

	// The known branch grows and the original sprint end returns: a new
	// period, and the first record keeps its StateSync observation
	// unchanged.
	before := d.shared.records[sprintEnd.Hash][0]
	returnTip := extendBranch(c, "ret", c.block(sprintEnd.Hash), 2)
	decision2, events2, err := d.reorgDecision(returnTip, d.currentMarkers(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.commitReorg(decision2, events2); err != nil {
		t.Fatal(err)
	}
	after := d.shared.records[sprintEnd.Hash][0]
	if !recordsEqual(before, after) {
		t.Fatal("the removed record is immutable across the reapply")
	}
	periods := d.shared.periods[sprintEnd.Hash]
	if len(periods) != 2 || periods[1].State != block.StateActive {
		t.Fatalf("the sprint end reapply must be a second ACTIVE period, got %d", len(periods))
	}
	// The promoted replacement sprint end (8') observes its own StateSync
	// work under this decision, never the removed block's record.
	promoted := d.shared.records[c.block(tip.Hash).Hash]
	if len(promoted) != 1 {
		t.Fatal("the promoted sprint end has its own first period")
	}
	if _, ok := promoted[0].Metadata.Details.Bor.StateSyncCount.Get(); !ok {
		t.Fatal("the promoted sprint end carries its own StateSync observation")
	}
	// The returning original 7 also reopens its own second period under
	// the same decision: one canonical change, one batch.
	originalPeriods := d.shared.periods[original7.Hash]
	if len(originalPeriods) != 2 || originalPeriods[1].State != block.StateActive {
		t.Fatalf("the original block 7 must be REMOVED then ACTIVE, got %d periods", len(originalPeriods))
	}
	if originalPeriods[1].Activation.ActivationID.DecisionSequence != decision2 {
		t.Fatal("both returns of one decision carry that decision")
	}
}

// Known branch and side storage: a block stored without a head change
// creates no lifecycle events and no activation, and a milestone
// whitelist anchored on the side branch is refused by the observer's
// canonical check.
func TestSideChainNoLifecycleChange(t *testing.T) {
	c, d, _ := newTestChain(t, 8)
	headBefore := c.headAnchor()

	side := c.side("b5", 8, c.canonHash(t, 7), sprintStateSync(8))
	if c.headAnchor() != headBefore {
		t.Fatal("side storage must not move the head")
	}
	if periods, ok := d.shared.periods[side.Hash]; ok && len(periods) > 0 {
		t.Fatal("side storage creates no lifecycle activation")
	}
	if _, err := d.shared.obs.ObserveNative(finalityobserver.NativeInput{
		Head:     c.headAnchor(),
		Decision: d.nextDecision(),
		Origin:   block.OriginNativeLive,
		MilestoneWhitelist: block.Some(finalityobserver.RetainedWhitelist{
			Kind:     finalityobserver.WhitelistMilestone,
			Anchor:   block.AnchorRef{Height: 8, Hash: side.Hash},
			Decision: d.nextDecision(),
			Request:  finalityobserver.Request{Operation: finalityobserver.OpMilestoneLatest},
		}),
	}); !errors.Is(err, finalityobserver.ErrInvariant) {
		t.Fatalf("side-branch whitelist error %v, want ErrInvariant", err)
	}

	// Promoting the known branch is one decision with events only for the
	// canonical change; the side block's first period opens under it.
	decision, events, err := d.reorgDecision(side, d.currentMarkers(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.commitReorg(decision, events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 { // Removed canonical 8 + Applied side 8
		t.Fatalf("%d events, want one removed and one applied", len(events))
	}
	if events[1].event.Activation.BlockHash != side.Hash {
		t.Fatal("the promoted side block opens its activation")
	}
}

// recordsEqual compares two records through their canonical encoding.
func recordsEqual(a, b block.BlockRecord) bool {
	ab, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}
