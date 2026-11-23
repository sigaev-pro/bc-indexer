package recoverygc

import (
	"errors"
	"testing"

	reg "github.com/sigaev-pro/bc-indexer/adapters/lifecycle_registry"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// The composition test of the E05-02 seam: the lifecycle registry owns the
// activation and revision records, the recovery_gc engine owns the anchor
// and the release markers, and one shared native store holds both without
// key overlap. The registry's records map onto GC entities; the retained
// speculative corpus of a SIDE decision releases only after proven
// divergence.
func TestCompositionWithLifecycleRegistry(t *testing.T) {
	db := newStore()
	registry, completed, err := reg.Open(db)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if completed != 0 {
		t.Fatalf("reconciled %d decisions of an empty store", completed)
	}
	node := gcNode
	producer, run, epoch := gcProducer, gcRun, gcID16(0x44)
	net := gcNet
	blockA := blockAt(5)
	blockB := blockAt(6)
	sideBlock := offChain(7)

	regRef := func(sequence uint64) reg.NativeDecisionRef {
		return reg.NativeDecisionRef{NodeInstanceID: node, ProducerID: producer, RunID: run,
			DecisionSequence: sequence}
	}
	intent := func(sequence byte) string { return gcH32(sequence).String() }
	corpus := func(block AnchorRef) string { return gcH32(block.Hash[0] ^ 0x5A).String() }
	source := func(sequence uint64) semantic.SourceRef {
		return semantic.SourceRef{NodeInstanceID: node, WriterEpoch: epoch, SourceSequence: sequence}
	}
	blockEvent := func(kind semantic.Kind, block AnchorRef, decision, revision, sourceSequence uint64,
	) reg.EventPlan {
		d, derr := semantic.NewBlockDescriptor(kind, net, producer, run, decision, block.Hash,
			revision, source(sourceSequence))
		if derr != nil {
			t.Fatalf("descriptor: %v", derr)
		}
		digest, derr := d.Digest()
		if derr != nil {
			t.Fatalf("digest: %v", derr)
		}
		return reg.EventPlan{Descriptor: d, Content: digest.String()}
	}
	settle := func(sequence uint64, kind reg.DecisionKind) reg.NativeDecisionRef {
		ref := regRef(sequence)
		if err := registry.Intend(ref, net, kind, intent(byte(sequence))); err != nil {
			t.Fatalf("intend %d: %v", sequence, err)
		}
		if err := registry.SettleDecided(ref); err != nil {
			t.Fatalf("settle %d: %v", sequence, err)
		}
		return ref
	}

	// A speculative SIDE execution retains its corpus without publishing.
	side := settle(0, reg.KindSide)
	if err := registry.CaptureCorpus(side, sideBlock.Hash, corpus(sideBlock)); err != nil {
		t.Fatalf("capture side corpus: %v", err)
	}
	// An import applies blockA.
	imported := settle(1, reg.KindImport)
	if err := registry.CaptureCorpus(imported, blockA.Hash, corpus(blockA)); err != nil {
		t.Fatalf("capture corpus: %v", err)
	}
	if _, err := registry.Apply(imported, reg.ApplicationPlan{Events: []reg.EventPlan{
		blockEvent(semantic.KindApplied, blockA, 1, 0, 11)}}); err != nil {
		t.Fatalf("apply import: %v", err)
	}
	// A reorganization removes the blockA activation and applies blockB.
	reorg := settle(2, reg.KindCanonical)
	if err := registry.CaptureCorpus(reorg, blockB.Hash, corpus(blockB)); err != nil {
		t.Fatalf("capture corpus: %v", err)
	}
	if _, err := registry.Apply(reorg, reg.ApplicationPlan{Events: []reg.EventPlan{
		blockEvent(semantic.KindRemoved, blockA, 1, 1, 21),
		blockEvent(semantic.KindApplied, blockB, 2, 0, 22)}}); err != nil {
		t.Fatalf("apply reorg: %v", err)
	}

	// The GC side of the same store.
	h := &harness{db: db, oracle: &chainOracle{chain: chain(11)},
		pending: &pendingMap{pending: map[EntityID]PendingReport{}}, releaser: newReleaser(),
		pins: newPins()}
	h.pins.hold("S0")
	h.pins.hold("S1")
	engine := openEngine(t, h)
	commitAnchorAt(t, engine, 10)

	activation := func(sequence uint64) (reg.ActivationRecord, bool) {
		rec, ok, err := registry.Activation(reg.ActivationKey{Network: net, ProducerID: producer,
			RunID: run, Activation: semantic.ActivationID{DecisionSequence: sequence,
				EntityOrdinal: 0}})
		if err != nil {
			t.Fatalf("activation %d: %v", sequence, err)
		}
		return rec, ok
	}
	entityID := func(sequence uint64) EntityID {
		return EntityID{Network: net, ProducerID: producer, RunID: run,
			DecisionSequence: sequence, EntityOrdinal: 0}
	}

	// The removed activation of the import: durable terminal evidence from
	// the registry, covered by the anchor.
	removed, ok := activation(1)
	if !ok || removed.State != reg.ActivationRemoved {
		t.Fatalf("import activation is %+v", removed)
	}
	removedEntity := Entity{ID: entityID(1), Block: AnchorRef{Height: 5, Hash: removed.BlockHash},
		Branch: BranchCanonical, Applied: true, Terminal: &TerminalEvidence{
			Revision: removed.RemovedRevision,
			Decision: NativeDecisionRef{NodeInstanceID: removed.RemovedDecision.NodeInstanceID,
				ProducerID: removed.RemovedDecision.ProducerID, RunID: removed.RemovedDecision.RunID,
				DecisionSequence: removed.RemovedDecision.DecisionSequence}},
		Pins: []string{"S0"}}
	// The applied activation of the reorganization still owes delivery.
	appliedEntity := Entity{ID: entityID(2), Block: blockB, Branch: BranchCanonical, Applied: true,
		Pins: []string{"S0"}}
	h.pending.pending[appliedEntity.ID] = PendingReport{Delivery: true}
	// The retained speculative corpus of the SIDE branch.
	speculativeEntity := Entity{ID: entityID(0), Block: sideBlock, Branch: BranchNoncanonical,
		Pins: []string{"S1"}}

	decisions := decisionsOf(t, engine, removedEntity, appliedEntity, speculativeEntity)
	want := []Reason{ReasonReleaseCovered, ReasonRetainPending, ReasonReleaseDivergentBranch}
	for i, decision := range decisions {
		if decision.Reason != want[i] {
			t.Fatalf("decision %d is %s, want %s", i, decision.Reason, want[i])
		}
	}

	result, err := engine.Release([]Entity{removedEntity, speculativeEntity}, []string{"S0"})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if len(result.Released) != 2 || !h.releaser.held[entityID(1)] || !h.releaser.held[entityID(0)] {
		t.Fatalf("released %v", result.Released)
	}
	if !h.pins.held["S0"] || h.pins.held["S1"] {
		t.Fatalf("pin state after release: %+v", h.pins.held)
	}
	// The registry side of the shared store is untouched.
	events, err := registry.Events(&net)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("registry events %d, want 3", len(events))
	}
	if _, ok, err := registry.Activation(reg.ActivationKey{Network: net, ProducerID: producer,
		RunID: run, Activation: semantic.ActivationID{DecisionSequence: 1, EntityOrdinal: 0}}); err != nil ||
		!ok {
		t.Fatalf("registry record gone: %v %v", ok, err)
	}
	if err := registry.MarkAcked(reg.ActivationKey{Network: net, ProducerID: producer, RunID: run,
		Activation: semantic.ActivationID{DecisionSequence: 2, EntityOrdinal: 0}}); !errors.Is(err, nil) &&
		err != nil {
		t.Fatalf("registry write after release: %v", err)
	}
}
