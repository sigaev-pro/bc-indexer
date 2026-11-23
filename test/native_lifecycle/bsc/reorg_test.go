package bsc

// The reorg tests of the BSC slot of E05-02.reorg: reorg, reapply,
// crash-and-marker-repeat and the finality interplay of the BSC native
// decisions on an in-memory store. They drive the real admission and
// decision packages, the real lifecycle registry (the activation and
// revision records, the corpus rules, the applied-decision frontier that
// closes the bridge obligations and their marker cleanup), the real
// recovery GC engine behind the BSC finality observatory, and the E02-03
// lifecycle validator oracle over the published event set. The model is in
// lifecycle_bsc.go and store.go; the design is in ../README.md.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/extractor/admission"
	bscfinality "github.com/ethereum/go-ethereum/extractor/bsc/finality"
	"github.com/ethereum/go-ethereum/extractor/decision"
	registry "github.com/sigaev-pro/bc-indexer/adapters/lifecycle_registry"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
	validator "github.com/sigaev-pro/bc-indexer/test/lifecycle_validator"
)

// TestReorgCreatesRemovedForReadyApplied tests REQ-LIF-02 (AC-LIF-02-01):
// a canonical reorg of several blocks creates a durable Removed for every
// applied activation it abandoned, and the derivation reads only the
// durable decision records: no activation is READY or acknowledged, no
// PubAck exists, and the bridge cleanup deleted the markers of the applied
// decisions long before.
func TestReorgCreatesRemovedForReadyApplied(t *testing.T) {
	heads, anc := reorgChain()
	f := newFixture(t)
	// Import A1..A5; A3..A5 are the activations under test.
	first, last := decision.IntentRecord{}, decision.IntentRecord{}
	tested := map[uint64]bool{}
	parent := heads["G"]
	for i := 1; i <= 5; i++ {
		h := heads[fmt.Sprintf("A%d", i)]
		record := f.importBlock(h, parent)
		parent = h
		if i == 1 {
			first = record
		}
		if i >= 3 {
			tested[record.Sequence] = true
		}
		last = record
	}
	// The applied activations are neither READY nor acknowledged: no
	// publication and no PubAck ever happened, and the bridge cleanup
	// deleted their native markers because the registry applied them.
	for _, record := range []decision.IntentRecord{first, last} {
		activation, ok, err := f.registry.Activation(activationKey(record.Sequence))
		if err != nil || !ok {
			t.Fatalf("activation of %d: %v %v", record.Sequence, ok, err)
		}
		if activation.Ready || activation.Acked {
			t.Fatalf("activation of %d is published %+v", record.Sequence, activation)
		}
		marker, err := f.db.Has(decision.MarkerKey(admission.Producer{ProducerID: "bsc-producer",
			RunID: "run-1"}, record.Sequence))
		if err != nil {
			t.Fatalf("marker read: %v", err)
		}
		if marker {
			t.Fatalf("the marker of the applied decision %d survived", record.Sequence)
		}
	}
	// One canonical reorg decision from A5 to A2 (the BSC SetHead reorg
	// path): no new execution, the B-branch has no retained corpus yet.
	reorgRecord := f.rewind(heads["A5"], heads["A2"])
	removed := f.onCanonical(heads["A5"], heads["A2"], reorgRecord, anc)
	if len(removed) != 3 {
		t.Fatalf("%d Removed descriptors, want the three abandoned activations A3 A4 A5", len(removed))
	}
	for _, d := range removed {
		if d.Kind != semantic.KindRemoved || d.Revision == 0 {
			t.Fatalf("removed descriptor %+v", d)
		}
		if d.Activation.ActivationID.EntityOrdinal != 0 {
			t.Fatalf("block removal of ordinal %d", d.Activation.ActivationID.EntityOrdinal)
		}
	}
	// Every Removed names the activation of its own former Applied: the
	// decision sequences of the A3..A5 imports.
	bySequence := map[uint64]bool{}
	for _, d := range removed {
		bySequence[d.Activation.ActivationID.DecisionSequence] = true
	}
	if len(bySequence) != len(tested) {
		t.Fatalf("%d removed activations, want %d", len(bySequence), len(tested))
	}
	for seq := range tested {
		if !bySequence[seq] {
			t.Fatalf("no Removed for the activation of decision %d", seq)
		}
	}
	// The canonical decision alone fabricates no Applied for the B branch:
	// without executed and decided blocks there is nothing to apply. The
	// five imports are applied, the canonical decision holds no
	// application, and the published event set is the five Applied alone.
	for sequence := uint64(0); sequence < 5; sequence++ {
		d, ok, err := f.registry.Decision(dref(sequence))
		if err != nil || !ok || d.State != registry.StateApplied {
			t.Fatalf("import %d is %+v %v %v", sequence, d, ok, err)
		}
	}
	d, ok, err := f.registry.Decision(dref(reorgRecord.Sequence))
	if err != nil || !ok || len(d.Application.Events) != 3 {
		t.Fatalf("the canonical decision applied %+v %v %v, want its three Removed alone",
			d.Application, ok, err)
	}
	for _, event := range d.Application.Events {
		if event.Descriptor.Kind != semantic.KindApplied {
			continue
		}
		t.Fatalf("the canonical decision fabricated an Applied: %+v", event.Descriptor)
	}
	events := f.published()
	if len(events) != 8 {
		t.Fatalf("%d published events, want the five Applied and the three Removed", len(events))
	}
	for _, event := range events {
		if event.Descriptor.Kind != semantic.KindApplied && event.Descriptor.Kind != semantic.KindRemoved {
			t.Fatalf("unexpected published %s", event.Descriptor.Kind)
		}
	}
}

// rewind runs one CANONICAL decision without a new execution from old to
// target, as the BSC SetHead reorg path does, and returns its record.
func (f *fixture) rewind(old, target decision.Head) decision.IntentRecord {
	f.t.Helper()
	record, err := f.bridge.Intend(f.token, decision.Intent{Kind: decision.Canonical, OldHead: old,
		NewHead: target, Events: true, EvidenceDigest: sum64("canonical", old.Hash, target.Hash)})
	if err != nil {
		f.t.Fatalf("intend canonical: %v", err)
	}
	f.marker(record)
	f.mirrorIntent(record, nil)
	if _, err := f.bridge.Resolve(f.token, record.Sequence); err != nil {
		f.t.Fatalf("resolve canonical: %v", err)
	}
	f.settle(record)
	// The canonical decision without a new execution applies nothing here:
	// its Removed set is derived by onCanonical once the branch settled.
	f.cleanup()
	return record
}

// TestReapplyIsANewActivation tests REQ-CAP-08 (AC-CAP-08-02): one native
// reorg decision replaces several blocks, and reapplying a block of the
// abandoned branch after the reorg is a new activation with a new decision
// sequence, batch and lifecycle identity; the removed activation stays
// terminal, and the registry's published event stream folds through the
// E02-03 oracle to one unambiguous result per activation in every order.
func TestReapplyIsANewActivation(t *testing.T) {
	heads, anc := reorgChain()
	f := newFixture(t)
	var a5First decision.IntentRecord
	parent := heads["G"]
	for i := 1; i <= 5; i++ {
		h := heads[fmt.Sprintf("A%d", i)]
		record := f.importBlock(h, parent)
		parent = h
		if i == 5 {
			a5First = record
		}
	}
	first := f.descriptorOf(t, a5First.Sequence)
	batch, allocated, err := f.alloc.EnsureBatch(a5First.Sequence)
	if err != nil || !allocated {
		t.Fatalf("batch of the first A5 decision: %v %v", batch, err)
	}
	// The reorg to B3..B5: rewind to A2, remove the abandoned activations,
	// then import the B branch — the registry commits the applications in
	// decision order.
	reorg := f.rewind(heads["A5"], heads["A2"])
	f.onCanonical(heads["A5"], heads["A2"], reorg, anc)
	parent = heads["A2"]
	for i := 3; i <= 5; i++ {
		h := heads[fmt.Sprintf("B%d", i)]
		f.importBlock(h, parent)
		parent = h
	}
	// Reapply A5 on top of B5: a new activation of the same block hash.
	reapplied := f.importBlock(heads["A5"], heads["B5"])
	second := f.descriptorOf(t, reapplied.Sequence)
	if first.Activation.ActivationID == second.Activation.ActivationID {
		t.Fatal("the reapplied A5 reused the activation of the removed A5")
	}
	if first.Activation.BlockHash != second.Activation.BlockHash {
		t.Fatalf("the reapply changed the block: %s %s", first.Activation.BlockHash,
			second.Activation.BlockHash)
	}
	if second.Revision != 0 {
		t.Fatalf("an Applied with revision %d", second.Revision)
	}
	// One batch per decision, and the reapplied decision has its own.
	reappliedBatch, allocated, err := f.alloc.EnsureBatch(reapplied.Sequence)
	if err != nil || !allocated || reappliedBatch == batch {
		t.Fatalf("batch of the reapplied decision: %v %v", reappliedBatch, err)
	}
	if _, allocated, err := f.alloc.EnsureBatch(a5First.Sequence); err != nil || allocated {
		t.Fatalf("a repeated allocation of decision %d: %v", a5First.Sequence, err)
	}
	// The first A5 activation is terminal in the registry; the reapply is
	// a separate live one.
	firstRecord, ok, err := f.registry.Activation(activationKey(a5First.Sequence))
	if err != nil || !ok {
		t.Fatalf("the first activation of A5 is gone: %v %v", ok, err)
	}
	if firstRecord.State != registry.ActivationRemoved {
		t.Fatalf("the first activation of A5 is %s, want REMOVED", firstRecord.State)
	}
	secondRecord, ok, err := f.registry.Activation(activationKey(reapplied.Sequence))
	if err != nil || !ok || secondRecord.State != registry.ActivationActive {
		t.Fatalf("the reapplied activation is %+v %v %v, want ACTIVE", secondRecord, ok, err)
	}
	// The published event stream folds to one unambiguous result per
	// activation in every order (the E02-03 oracle): the removed A5 stays
	// removed, its reapply is a separate live activation.
	events := f.published()
	straight := foldAll(t, events)
	shuffled := append([]registry.PublishedEvent{events[len(events)-1]}, events[:len(events)-1]...)
	reordered := foldAll(t, shuffled)
	if len(straight) != len(reordered) {
		t.Fatalf("%d activations straight, %d reordered", len(straight), len(reordered))
	}
	for key, record := range straight {
		other, ok := reordered[key]
		if !ok || !record.Equal(other) {
			t.Fatalf("activation %d/%d folded %s reordered %s",
				key.Activation.DecisionSequence, key.Activation.EntityOrdinal, record, other)
		}
	}
	if record := straight[validator.KeyOf(first)]; record.State != validator.StateRemoved ||
		record.RemovedRevision != 1 {
		t.Fatalf("the first A5 activation folded to %s, want REMOVED at revision 1", record)
	}
	if record := straight[validator.KeyOf(second)]; record.State != validator.StateActive {
		t.Fatalf("the reapplied A5 activation folded to %s, want ACTIVE", record.State)
	}
}

// descriptorOf returns the Applied descriptor of one decided import.
func (f *fixture) descriptorOf(t *testing.T, sequence uint64) semantic.SemanticDescriptor {
	t.Helper()
	d, ok, err := f.registry.Decision(dref(sequence))
	if err != nil || !ok || len(d.Application.Events) != 1 {
		t.Fatalf("decision %d is %+v %v %v", sequence, d, ok, err)
	}
	return d.Application.Events[0].Descriptor
}

// foldAll feeds the events into a fresh automaton of the E02-03 oracle.
func foldAll(t *testing.T, events []registry.PublishedEvent) map[validator.ActivationKey]validator.Record {
	t.Helper()
	automaton := validator.New()
	for _, event := range events {
		if _, err := automaton.Apply(event.Descriptor, digest32(event.Content)); err != nil {
			t.Fatalf("oracle rejected %s: %v", event.Descriptor.Kind, err)
		}
	}
	return automaton.Records()
}

// digest32 decodes the hex content digest of an event into the Digest type
// of the E02-03 oracle.
func digest32(value string) validator.Digest {
	var out validator.Digest
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != len(out) {
		panic("bad content digest")
	}
	copy(out[:], raw)
	return out
}

// TestReorgDecisionCrashAndMarkerRepeat tests REQ-CAP-08 (AC-CAP-08-02): a
// power loss inside the reorg decision, between its intent and its marker,
// and the repeated native marker of the same decision after the restart
// restore the one former decision, its batch binding and the complete
// ordered Removed set; no second decision or batch appears. The registry
// applications of the imports survive the power loss durably.
func TestReorgDecisionCrashAndMarkerRepeat(t *testing.T) {
	heads, anc := reorgChain()
	f := newFixture(t)
	imports := []decision.IntentRecord{}
	parent := heads["G"]
	for i := 1; i <= 5; i++ {
		h := heads[fmt.Sprintf("A%d", i)]
		record := f.importBlock(h, parent)
		imports = append(imports, record)
		parent = h
	}
	// The durable allocations of the imports.
	for _, record := range imports {
		if _, _, err := f.alloc.EnsureBatch(record.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	// The reorg intent is recorded and synced; the power loss drops the
	// unsynced marker batch (nothing was written yet).
	record, err := f.bridge.Intend(f.token, decision.Intent{Kind: decision.Canonical, OldHead: heads["A5"],
		NewHead: heads["A2"], Events: true, EvidenceDigest: sum64("canonical", heads["A5"].Hash, heads["A2"].Hash)})
	if err != nil {
		f.t.Fatalf("intend canonical: %v", err)
	}
	reopened := &fixture{t: t, db: f.db.powerLoss(), pins: f.pins, inputs: f.inputs, roots: f.roots,
		token: f.token, storeID: f.storeID}
	reopened.openBridge()
	alloc, err := semantic.NewAllocator(producer, run)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range imports {
		if err := alloc.AdoptBatch(record.Sequence, mustBatch(t, f.alloc, record.Sequence)); err != nil {
			t.Fatalf("adopt %d: %v", record.Sequence, err)
		}
	}
	reopened.alloc = alloc
	// The applications of the imports survived the power loss: the
	// activations are still applied in the reopened registry.
	for _, record := range imports {
		if _, ok, err := reopened.registry.Activation(activationKey(record.Sequence)); err != nil || !ok {
			t.Fatalf("activation of %d after the restart: %v %v", record.Sequence, ok, err)
		}
	}
	// The native replay writes the marker of the same decision; the restart
	// resolves it. A new Intend is refused while the intent is unresolved.
	reopened.marker(record)
	if _, err := reopened.bridge.Intend(reopened.token, decision.Intent{Kind: decision.Finality,
		OldHead: heads["A2"], NewHead: heads["A2"], EvidenceDigest: sum64("finality")}); err == nil {
		t.Fatal("a second decision was allowed while the reorg intent was unresolved")
	}
	decided, err := reopened.bridge.Resolve(reopened.token, record.Sequence)
	if err != nil {
		t.Fatalf("resolve the repeated marker: %v", err)
	}
	if decided.Sequence != record.Sequence {
		t.Fatalf("the repeated marker decided sequence %d, want %d", decided.Sequence, record.Sequence)
	}
	reopened.mirrorIntent(record, nil)
	reopened.settle(record)
	// The former batch bindings are restored, not reallocated.
	if _, allocated, err := alloc.EnsureBatch(imports[2].Sequence); err != nil || allocated {
		t.Fatalf("decision %d reallocated: %v", imports[2].Sequence, err)
	}
	// The complete ordered Removed set derives from the restored decision.
	removed := reopened.onCanonical(heads["A5"], heads["A2"], record, anc)
	if len(removed) != 3 {
		t.Fatalf("%d Removed descriptors after the repeat, want 3", len(removed))
	}
	removedSequences := []uint64{}
	for _, d := range removed {
		removedSequences = append(removedSequences, d.Activation.ActivationID.DecisionSequence)
	}
	want := []uint64{imports[2].Sequence, imports[3].Sequence, imports[4].Sequence}
	if !slices.Equal(removedSequences, want) {
		t.Fatalf("the removed set is %v, want %v", removedSequences, want)
	}
	decisions, err := reopened.bridge.Decisions(reopened.token)
	if err != nil {
		t.Fatal(err)
	}
	canonical := 0
	for _, d := range decisions {
		if d.Intent.Kind == decision.Canonical {
			canonical++
		}
	}
	if canonical != 1 {
		t.Fatalf("%d canonical decisions after the repeat", canonical)
	}
}

// TestSideBranchFinalityKeepsCanonicalActivations tests the BSC reorg and
// finality interplay (AC-LIF-06-03): after the canonical branch moved, a
// delayed side-branch finality observation finalizes no canonical
// activation, gives no GC anchor, and a proven canonical change below the
// accepted anchor is fatal.
func TestSideBranchFinalityKeepsCanonicalActivations(t *testing.T) {
	heads, anc := reorgChain()
	f := newFixture(t)
	observatory := f.openObservatory(anc)
	parent := heads["G"]
	for i := 1; i <= 5; i++ {
		h := heads[fmt.Sprintf("A%d", i)]
		f.importBlock(h, parent)
		parent = h
	}
	// The header snapshot attests A3 on the canonical head A5.
	if result, err := observatory.ObserveHeader(f.token,
		attestedEvidence(heads["A5"], bscfinality.AttestedSnapshot(heads["A5"], heads["A4"], heads["A3"])),
		anc, 6); err != nil || result.Change != bscfinality.Accepted || result.Anchor != heads["A3"] {
		t.Fatalf("anchor: %+v %v", result, err)
	}
	// The accepted anchor is the shared recovery_gc record.
	anchor, ok := observatory.Anchor(f.token)
	if !ok || anchor.Anchor.Height != 3 || anchor.Decision.DecisionSequence != 6 {
		t.Fatalf("anchor %+v %v, want the attested A3 of decision 6", anchor, ok)
	}
	// The canonical reorg to the B branch moves the head to B5; the
	// abandoned A3..A5 activations are removed before the new branch
	// imports apply (the registry commits in decision order).
	reorg := f.rewind(heads["A5"], heads["A2"])
	removed := f.onCanonical(heads["A5"], heads["B5"], reorg, anc)
	if len(removed) != 3 {
		t.Fatalf("%d Removed descriptors after the branch change", len(removed))
	}
	parent = heads["A2"]
	for i := 3; i <= 5; i++ {
		h := heads[fmt.Sprintf("B%d", i)]
		f.importBlock(h, parent)
		parent = h
	}
	// Before the branch change settles, a delayed side-branch callback of
	// the B branch against the canonical head A5 is only refused: it keeps
	// the anchor and finalizes nothing (AC-LIF-06-03).
	delayed := attestedEvidence(heads["A5"],
		bscfinality.AttestedSnapshot(heads["B5"], heads["B4"], heads["B3"]))
	if _, err := observatory.ObserveHeader(f.token, delayed, anc, 7); !errors.Is(err, bscfinality.ErrBranch) {
		t.Fatalf("delayed side-branch callback: %v", err)
	}
	// The accepted anchor A3 is no longer on the canonical execution
	// ancestry of the new head B5: the observation of the new head is a
	// proven regression and stops the observatory.
	newHead := attestedEvidence(heads["B5"],
		bscfinality.AttestedSnapshot(heads["B5"], heads["B4"], heads["B3"]))
	if _, err := observatory.ObserveHeader(f.token, newHead, anc, 8); !errors.Is(err,
		bscfinality.ErrRegression) {
		t.Fatalf("observation below the abandoned anchor: %v", err)
	}
	// The projections of the removed activations use only typed evidence:
	// with the anchor branch abandoned, nothing finalizes them.
	evidence := bscfinality.Evidence{ObservedExecutionHead: heads["B5"],
		ObservedNativeHeaderHead: heads["B5"],
		HeaderFinality:           bscfinality.AttestedSnapshot(heads["B5"], heads["B4"], heads["B3"]),
		NativeEffectiveFinalized: bscfinality.KnownReference(heads["B3"], bscfinality.NativeLive),
		EffectiveBasis:           bscfinality.BasisHeaderSnapshot}
	headerStatus, _, err := bscfinality.Project(evidence, heads["A4"], anc)
	if err != nil || headerStatus != bscfinality.CanonicalUnfinalized {
		t.Fatalf("abandoned activation A4 projects %s %v, want %s", headerStatus, err,
			bscfinality.CanonicalUnfinalized)
	}
}
