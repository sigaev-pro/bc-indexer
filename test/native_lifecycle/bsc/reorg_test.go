package finality

// The BSC slot of E05-02.reorg (test/native_lifecycle): reorg, reapply,
// known-branch and reordered-event behavior of the BSC native decisions on
// an in-memory store. It drives the real admission and decision packages
// and the BSC finality observatory of this package, and folds the resulting
// lifecycle descriptors over the real protocol/semantic identity model. The
// mini-registry below is a test-native model of the E05-01.registry rules
// this evidence needs; the product registry is not created here.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// reorgPins is a durable pin ledger of the import path.
type reorgPins struct {
	roots   map[string]string
	digests map[string]string
}

func (p *reorgPins) Verify(id, root string) (string, error) {
	got, ok := p.roots[id]
	if !ok {
		return "", fmt.Errorf("no pin %s", id)
	}
	if got != root {
		return "", fmt.Errorf("pin %s holds %s", id, got)
	}
	return p.digests[id], nil
}

func (p *reorgPins) Release(id string) error {
	delete(p.roots, id)
	delete(p.digests, id)
	return nil
}

// reorgInputs holds the sealed capsules and closed consumed manifests.
type reorgInputs struct {
	capsules map[string]string
	consumed map[string]admission.Consumed
}

func (i *reorgInputs) CapsuleDigest(id string) (string, error) {
	d, ok := i.capsules[id]
	if !ok {
		return "", fmt.Errorf("no capsule %s", id)
	}
	return d, nil
}

func (i *reorgInputs) Consumed(id string) (admission.Consumed, error) {
	c, ok := i.consumed[id]
	if !ok {
		return admission.Consumed{}, fmt.Errorf("no consumed manifest %s", id)
	}
	return c, nil
}

type reorgRoots struct{ durable map[string]bool }

func (r reorgRoots) Durable(root string) (bool, error) { return r.durable[root], nil }

type reorgMirror struct{ applied map[uint64]string }

func (m reorgMirror) Applied(seq uint64, digest string) (bool, error) {
	return m.applied[seq] == digest, nil
}

func sum64(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// reorgFixture is one BSC producer run with its store, admission, decision
// bridge, finality observatory and semantic allocator.
type reorgFixture struct {
	t        *testing.T
	db       *memStore
	pins     *reorgPins
	inputs   *reorgInputs
	roots    *reorgRoots
	mirror   *reorgMirror
	token    *admission.Token
	adm      *admission.Admission
	bridge   *decision.Bridge
	storeID  string
	producer semantic.ID16
	run      semantic.ID16
	network  semantic.Network
	node     semantic.ID16
	epoch    semantic.ID16
	alloc    *semantic.Allocator
}

func id16(seed string) semantic.ID16 {
	var out semantic.ID16
	sum := sha256.Sum256([]byte(seed))
	copy(out[:], sum[:16])
	return out
}

func hash32(value string) semantic.Hash32 {
	var out semantic.Hash32
	sum := sha256.Sum256([]byte(value))
	copy(out[:], sum[:32])
	return out
}

func chainID(seed string) semantic.UInt256 {
	var out semantic.UInt256
	sum := sha256.Sum256([]byte(seed))
	copy(out[:], sum[:32])
	return out
}

func newReorgFixture(t *testing.T) *reorgFixture {
	t.Helper()
	db := newStore()
	token := admission.NewGuard().Acquire()
	producerID, runID := "bsc-producer", "run-1"
	f := &reorgFixture{t: t, db: db,
		pins:   &reorgPins{roots: map[string]string{}, digests: map[string]string{}},
		inputs: &reorgInputs{capsules: map[string]string{}, consumed: map[string]admission.Consumed{}},
		roots:  &reorgRoots{durable: map[string]bool{}},
		mirror: &reorgMirror{applied: map[uint64]string{}},
		token:  token, storeID: "bsc-store-1",
		producer: id16(producerID), run: id16(runID),
		network: semantic.Network{ChainID: chainID("bsc-714"), GenesisHash: hash32("genesis")},
		node:    id16("node"), epoch: id16("epoch")}
	if err := f.openBridge(); err != nil {
		t.Fatalf("bridge open: %v", err)
	}
	if err := f.bridge.GoLive(token); err != nil {
		t.Fatalf("go live: %v", err)
	}
	alloc, err := semantic.NewAllocator(f.producer, f.run)
	if err != nil {
		t.Fatal(err)
	}
	f.alloc = alloc
	return f
}

// openBridge (re)opens the admission and the decision bridge on the store of
// the fixture, as a node start does.
func (f *reorgFixture) openBridge() error {
	adm, err := admission.Open(f.token, f.db, admission.Producer{ProducerID: "bsc-producer", RunID: "run-1"},
		f.pins, f.inputs)
	if err != nil {
		return err
	}
	bridge, err := decision.Open(f.token, f.db, adm, f.storeID, f.roots, f.mirror)
	if err != nil {
		return err
	}
	f.adm, f.bridge = adm, bridge
	return nil
}

// importBlock runs one observed import of block h with parent p to a decided
// IMPORT decision with public events, and returns its record.
func (f *reorgFixture) importBlock(h, p decision.Head) decision.IntentRecord {
	f.t.Helper()
	c := admission.Candidate{Number: h.Number, Hash: h.Hash, ParentHash: p.Hash,
		ParentRoot: sum64("root", p.Hash)}
	obs, err := f.adm.Begin(f.token, c)
	if err != nil {
		f.t.Fatalf("begin %s: %v", h.Hash, err)
	}
	f.pins.roots[obs.Key], f.pins.digests[obs.Key] = c.ParentRoot, sum64("pin", h.Hash)
	f.inputs.capsules[obs.Key] = sum64("capsule", h.Hash)
	if _, err := f.adm.Admit(f.token, obs.Key); err != nil {
		f.t.Fatalf("admit %s: %v", h.Hash, err)
	}
	consumedChain := sum64("chain", f.storeID)
	consumedDigest, err := admission.ConsumedDigest(f.inputs.capsules[obs.Key], 1, consumedChain)
	if err != nil {
		f.t.Fatal(err)
	}
	f.inputs.consumed[obs.Key] = admission.Consumed{Observation: obs.Key,
		CapsuleDigest: f.inputs.capsules[obs.Key], Count: 1, Chain: consumedChain,
		Digest: consumedDigest}
	ready, err := f.adm.ExecReady(f.token, obs.Key, sum64("exec", h.Hash))
	if err != nil {
		f.t.Fatalf("exec ready %s: %v", h.Hash, err)
	}
	childRoot := sum64("child", h.Hash)
	f.roots.durable[childRoot] = true
	intent := decision.Intent{Kind: decision.Import, OldHead: p, NewHead: h, Events: true,
		EvidenceDigest: sum64("evidence", h.Hash),
		Members: []decision.Member{{Observation: ready.Observation, Block: h.Hash,
			ExecReadyDigest: ready.Digest, ChildRoot: childRoot}}}
	record, err := f.bridge.Intend(f.token, intent)
	if err != nil {
		f.t.Fatalf("intend import %s: %v", h.Hash, err)
	}
	f.marker(record)
	if _, err := f.bridge.Resolve(f.token, record.Sequence); err != nil {
		f.t.Fatalf("resolve import %s: %v", h.Hash, err)
	}
	return record
}

// rewind runs one CANONICAL decision without a new execution from old to
// target, as the BSC SetHead reorg path does, and returns its record.
func (f *reorgFixture) rewind(old, target decision.Head) decision.IntentRecord {
	f.t.Helper()
	record, err := f.bridge.Intend(f.token, decision.Intent{Kind: decision.Canonical, OldHead: old,
		NewHead: target, Events: true, EvidenceDigest: sum64("canonical", old.Hash, target.Hash)})
	if err != nil {
		f.t.Fatalf("intend canonical: %v", err)
	}
	f.marker(record)
	if _, err := f.bridge.Resolve(f.token, record.Sequence); err != nil {
		f.t.Fatalf("resolve canonical: %v", err)
	}
	return record
}

// marker models the native patch: the decision marker in the final batch.
func (f *reorgFixture) marker(record decision.IntentRecord) {
	f.t.Helper()
	value, err := decision.MarkerValue(record)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.Put(decision.MarkerKey(admission.Producer{ProducerID: "bsc-producer", RunID: "run-1"},
		record.Sequence), value); err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.SyncKeyValue(); err != nil {
		f.t.Fatal(err)
	}
}

// obligations returns the open delivery obligations of the run.
func (f *reorgFixture) obligations() []decision.Obligation {
	f.t.Helper()
	list, err := f.bridge.Obligations(f.token)
	if err != nil {
		f.t.Fatalf("obligations: %v", err)
	}
	return list
}

// ---------------------------------------------------------------- mini registry

// appliedActivation is one activation of the mini-registry: its Applied
// descriptor, the native head of its block, and the highest revision
// allocated from its counter.
type appliedActivation struct {
	applied   semantic.SemanticDescriptor
	blockHead decision.Head
	revision  uint64
}

// miniRegistry is the test-native model of the registry rules this evidence
// needs: an Applied per decided IMPORT activation (revision 0), a Removed
// for every applied activation the canonical change abandoned, with its
// revision allocated from the same counter, and no Applied for a branch
// without executed and decided blocks. It reads only durable decision
// records; no delivery, mirror or PubAck state can change what it derives.
type miniRegistry struct {
	f           *reorgFixture
	activations map[uint64]*appliedActivation // by decision sequence
	source      uint64
	events      []semantic.SemanticDescriptor
}

func newMiniRegistry(f *reorgFixture) *miniRegistry {
	return &miniRegistry{f: f, activations: map[uint64]*appliedActivation{}}
}

func (r *miniRegistry) nextSource() uint64 {
	r.source++
	return r.source
}

// applyImports derives the Applied descriptor of one decided IMPORT of one
// block: family block, entity ordinal 0, revision 0, a fresh activation of
// its decision sequence.
func (r *miniRegistry) applyImports(record decision.IntentRecord) semantic.SemanticDescriptor {
	r.f.t.Helper()
	if record.Kind != decision.Import || !record.Events {
		r.f.t.Fatalf("applyImports of a %s decision without events", record.Kind)
	}
	block := hash32(record.NewHead.Hash)
	d, err := semantic.NewBlockDescriptor(semantic.KindApplied, r.f.network, r.f.producer, r.f.run,
		record.Sequence, block, 0,
		semantic.SourceRef{NodeInstanceID: r.f.node, WriterEpoch: r.f.epoch, SourceSequence: r.nextSource()})
	if err != nil {
		r.f.t.Fatalf("applied descriptor: %v", err)
	}
	r.activations[record.Sequence] = &appliedActivation{applied: d, blockHead: record.NewHead}
	r.events = append(r.events, d)
	return d
}

// onCanonical derives the Removed descriptors of one CANONICAL decision
// from old to the new head: every applied activation whose block is not on
// the canonical ancestry of the new head is removed, whatever the delivery
// state of its decision. It allocates each Removed revision from the same
// counter as StatusChanged of that activation. The activations are visited
// by decision sequence, so the derived descriptors are deterministic.
func (r *miniRegistry) onCanonical(old, next decision.Head, record decision.IntentRecord,
	anc Ancestry) []semantic.SemanticDescriptor {
	r.f.t.Helper()
	if record.Kind != decision.Canonical {
		r.f.t.Fatalf("onCanonical of a %s decision", record.Kind)
	}
	removed := []semantic.SemanticDescriptor{}
	for _, sequence := range r.sequences() {
		activation := r.activations[sequence]
		onChain, err := anc.IsAncestorOrSelf(activation.blockHead, next)
		if err != nil {
			r.f.t.Fatalf("ancestry: %v", err)
		}
		if onChain {
			continue
		}
		activation.revision++
		d, err := semantic.NewBlockDescriptor(semantic.KindRemoved, r.f.network, r.f.producer, r.f.run,
			sequence, activation.applied.Activation.BlockHash, activation.revision,
			semantic.SourceRef{NodeInstanceID: r.f.node, WriterEpoch: r.f.epoch,
				SourceSequence: r.nextSource()})
		if err != nil {
			r.f.t.Fatalf("removed descriptor: %v", err)
		}
		r.events = append(r.events, d)
		removed = append(removed, d)
	}
	return removed
}

// sequences lists the applied activations by decision sequence.
func (r *miniRegistry) sequences() []uint64 {
	out := make([]uint64, 0, len(r.activations))
	for sequence := range r.activations {
		out = append(out, sequence)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ---------------------------------------------------------------- fold

// lifecycleState is the folded result of one activation.
type lifecycleState struct {
	applied bool
	removed bool
}

// foldLifecycle folds descriptors in the given order into one unambiguous
// result per activation: Applied sets the activation, Removed (revision
// above the folded revision) terminalizes it, and a Removed activation is
// never resurrected; a later Applied of another decision sequence is another
// activation that coexists with the removed one.
func foldLifecycle(events []semantic.SemanticDescriptor) map[semantic.ActivationID]lifecycleState {
	folded := map[semantic.ActivationID]lifecycleState{}
	revisions := map[semantic.ActivationID]uint64{}
	for _, event := range events {
		id := event.Activation.ActivationID
		state := folded[id]
		revision := revisions[id]
		switch event.Kind {
		case semantic.KindApplied:
			if revision == 0 {
				state.applied = true
			}
		case semantic.KindRemoved, semantic.KindStatus:
			if event.Revision > revision {
				revision = event.Revision
				if event.Kind == semantic.KindRemoved {
					state.removed = true
				}
			}
		}
		folded[id] = state
		revisions[id] = revision
	}
	return folded
}

// ---------------------------------------------------------------- tests

// reorgChain builds the chain of the reorg tests: G, the canonical branch
// A1..A5 and the side branch B3..B5 forking at A2.
func reorgChain() (map[string]decision.Head, *chain) {
	heads := map[string]decision.Head{"G": head(0, "genesis")}
	anc := newChain().add(0, heads["G"].Hash, "")
	parent := heads["G"].Hash
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("A%d", i)
		heads[name] = head(uint64(i), name)
		anc.add(uint64(i), heads[name].Hash, parent)
		parent = heads[name].Hash
	}
	parent = heads["A2"].Hash
	for i := 3; i <= 5; i++ {
		name := fmt.Sprintf("B%d", i)
		heads[name] = head(uint64(i), name)
		anc.add(uint64(i), heads[name].Hash, parent)
		parent = heads[name].Hash
	}
	return heads, anc
}

// TestReorgCreatesRemovedForReadyApplied tests REQ-LIF-02 (AC-LIF-02-01):
// a canonical reorg of several blocks creates a durable Removed for every
// READY Applied activation it abandoned, and the derivation reads only the
// durable decision records: the delivery obligation of the removed
// activations is still open, no PubAck or mirror application has happened.
func TestReorgCreatesRemovedForReadyApplied(t *testing.T) {
	heads, anc := reorgChain()
	f := newReorgFixture(t)
	registry := newMiniRegistry(f)
	// Import A1..A5; A3..A5 are the activations under test.
	first, last := decision.IntentRecord{}, decision.IntentRecord{}
	tested := map[uint64]bool{}
	parent := heads["G"]
	for i := 1; i <= 5; i++ {
		h := heads[fmt.Sprintf("A%d", i)]
		record := f.importBlock(h, parent)
		registry.applyImports(record)
		parent = h
		if i == 1 {
			first = record
		}
		if i >= 3 {
			tested[record.Sequence] = true
		}
		last = record
	}
	// The applied activations are READY obligations with no delivery: the
	// mirror applied nothing.
	if obligations := f.obligations(); len(obligations) < 3 {
		t.Fatalf("%d obligations after the imports", len(obligations))
	}
	if f.mirror.applied[first.Sequence] != "" || f.mirror.applied[last.Sequence] != "" {
		t.Fatal("the mirror applied a decision before any PubAck")
	}
	// One canonical reorg decision from A5 to A2 (the BSC SetHead reorg
	// path): no new execution, the B-branch has no retained corpus yet.
	reorgRecord := f.rewind(heads["A5"], heads["A2"])
	removed := registry.onCanonical(heads["A5"], heads["A2"], reorgRecord, anc)
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
	// The Removed exists while the delivery of its Applied is still an open
	// obligation without any PubAck: the delivery state was never read.
	if len(f.obligations()) < 4 {
		t.Fatalf("%d open obligations after the reorg", len(f.obligations()))
	}
	// The canonical decision alone fabricates no Applied for the B branch:
	// without executed and decided blocks there is nothing to apply.
	decisions, err := f.bridge.Decisions(f.token)
	if err != nil {
		t.Fatal(err)
	}
	imports := 0
	for _, d := range decisions {
		if d.Intent.Kind == decision.Import && d.State == decision.Decided {
			imports++
		}
	}
	if imports != 5 {
		t.Fatalf("%d decided imports after the reorg", imports)
	}
}

// TestReapplyIsANewActivation tests REQ-CAP-08 (AC-CAP-08-02): one native
// reorg decision replaces several blocks, and reapplying a block of the
// abandoned branch after the reorg is a new activation with a new decision
// sequence, batch and lifecycle identity; the removed activation stays
// terminal.
func TestReapplyIsANewActivation(t *testing.T) {
	heads, anc := reorgChain()
	f := newReorgFixture(t)
	registry := newMiniRegistry(f)
	var a5First decision.IntentRecord
	parent := heads["G"]
	for i := 1; i <= 5; i++ {
		h := heads[fmt.Sprintf("A%d", i)]
		record := f.importBlock(h, parent)
		registry.applyImports(record)
		parent = h
		if i == 5 {
			a5First = record
		}
	}
	first := registry.activations[a5First.Sequence].applied
	batch, allocated, err := f.alloc.EnsureBatch(a5First.Sequence)
	if err != nil || !allocated {
		t.Fatalf("batch of the first A5 decision: %v %v", batch, err)
	}
	// The reorg to B3..B5: rewind to A2, then import the B branch.
	reorg := f.rewind(heads["A5"], heads["A2"])
	registry.onCanonical(heads["A5"], heads["A2"], reorg, anc)
	parent = heads["A2"]
	for i := 3; i <= 5; i++ {
		h := heads[fmt.Sprintf("B%d", i)]
		registry.applyImports(f.importBlock(h, parent))
		parent = h
	}
	// Reapply A5 on top of B5: a new activation of the same block hash.
	reapplied := registry.applyImports(f.importBlock(heads["A5"], heads["B5"]))
	second := registry.activations[reapplied.Activation.ActivationID.DecisionSequence].applied
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
	reappliedBatch, allocated, err := f.alloc.EnsureBatch(reapplied.Activation.ActivationID.DecisionSequence)
	if err != nil || !allocated || reappliedBatch == batch {
		t.Fatalf("batch of the reapplied decision: %v %v", reappliedBatch, err)
	}
	if _, allocated, err := f.alloc.EnsureBatch(a5First.Sequence); err != nil || allocated {
		t.Fatalf("a repeated allocation of decision %d: %v", a5First.Sequence, err)
	}
	// The reordered event stream has one unambiguous result per activation:
	// the removed A5 stays removed, its reapply is a separate live
	// activation, in every order.
	ordered := append([]semantic.SemanticDescriptor{}, registry.events...)
	shuffled := []semantic.SemanticDescriptor{ordered[len(ordered)-1]}
	shuffled = append(shuffled, ordered[:len(ordered)-1]...)
	straight := foldLifecycle(ordered)
	reordered := foldLifecycle(shuffled)
	if len(straight) != len(reordered) {
		t.Fatalf("%d activations straight, %d reordered", len(straight), len(reordered))
	}
	for id, state := range straight {
		if reordered[id] != state {
			t.Fatalf("activation %d/%d folded %v reordered %v", id.DecisionSequence, id.EntityOrdinal, state,
				reordered[id])
		}
	}
	firstState := straight[first.Activation.ActivationID]
	if !firstState.applied || !firstState.removed {
		t.Fatalf("the first A5 activation folded %+v, want applied and removed", firstState)
	}
	secondState := straight[second.Activation.ActivationID]
	if !secondState.applied || secondState.removed {
		t.Fatalf("the reapplied A5 activation folded %+v, want applied and live", secondState)
	}
}

// TestReorgDecisionCrashAndMarkerRepeat tests REQ-CAP-08 (AC-CAP-08-02): a
// power loss inside the reorg decision, between its intent and its marker,
// and the repeated native marker of the same decision after the restart
// restore the one former decision, its batch binding and the complete
// ordered Removed set; no second decision or batch appears.
func TestReorgDecisionCrashAndMarkerRepeat(t *testing.T) {
	heads, anc := reorgChain()
	f := newReorgFixture(t)
	registry := newMiniRegistry(f)
	imports := []decision.IntentRecord{}
	parent := heads["G"]
	for i := 1; i <= 5; i++ {
		h := heads[fmt.Sprintf("A%d", i)]
		record := f.importBlock(h, parent)
		registry.applyImports(record)
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
		t.Fatalf("intend canonical: %v", err)
	}
	reopened := &reorgFixture{t: t, db: f.db.powerLoss(), pins: f.pins, inputs: f.inputs, roots: f.roots,
		mirror: f.mirror, token: f.token, storeID: f.storeID, producer: f.producer, run: f.run,
		network: f.network, node: f.node, epoch: f.epoch}
	reopened.openBridge()
	alloc, err := semantic.NewAllocator(reopened.producer, reopened.run)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range imports {
		if err := alloc.AdoptBatch(record.Sequence, mustBatch(t, f.alloc, record.Sequence)); err != nil {
			t.Fatalf("adopt %d: %v", record.Sequence, err)
		}
	}
	reopened.alloc = alloc
	registry.f = reopened
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
	// The former batch bindings are restored, not reallocated.
	if _, allocated, err := alloc.EnsureBatch(imports[2].Sequence); err != nil || allocated {
		t.Fatalf("decision %d reallocated: %v", imports[2].Sequence, err)
	}
	// The complete ordered Removed set derives from the restored decision.
	removed := registry.onCanonical(heads["A5"], heads["A2"], record, anc)
	if len(removed) != 3 {
		t.Fatalf("%d Removed descriptors after the repeat, want 3", len(removed))
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

func mustBatch(t *testing.T, allocator *semantic.Allocator, sequence uint64) semantic.BatchID {
	t.Helper()
	batch, _, err := allocator.EnsureBatch(sequence)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

// TestSideBranchFinalityKeepsCanonicalActivations tests the BSC reorg and
// finality interplay (AC-LIF-06-03): after the canonical branch moved, a
// delayed side-branch finality observation finalizes no canonical
// activation, gives no GC anchor, and a proven canonical change below the
// accepted anchor is fatal.
func TestSideBranchFinalityKeepsCanonicalActivations(t *testing.T) {
	heads, anc := reorgChain()
	f := newReorgFixture(t)
	registry := newMiniRegistry(f)
	observatory, err := Open(f.token, f.db, f.adm, f.storeID)
	if err != nil {
		t.Fatalf("observatory open: %v", err)
	}
	parent := heads["G"]
	for i := 1; i <= 5; i++ {
		h := heads[fmt.Sprintf("A%d", i)]
		registry.applyImports(f.importBlock(h, parent))
		parent = h
	}
	// The header snapshot attests A3 on the canonical head A5.
	if result, err := observatory.ObserveHeader(f.token,
		attestedEvidence(heads["A5"], AttestedSnapshot(heads["A5"], heads["A4"], heads["A3"])), anc, 6); err != nil ||
		result.Change != Accepted || result.Anchor != heads["A3"] {
		t.Fatalf("anchor: %+v %v", result, err)
	}
	// The canonical reorg to the B branch moves the head to B5; the
	// abandoned A3..A5 activations are removed.
	reorg := f.rewind(heads["A5"], heads["A2"])
	parent = heads["A2"]
	for i := 3; i <= 5; i++ {
		h := heads[fmt.Sprintf("B%d", i)]
		registry.applyImports(f.importBlock(h, parent))
		parent = h
	}
	removed := registry.onCanonical(heads["A5"], heads["B5"], reorg, anc)
	if len(removed) != 3 {
		t.Fatalf("%d Removed descriptors after the branch change", len(removed))
	}
	// Before the branch change settles, a delayed side-branch callback of
	// the B branch against the canonical head A5 is only refused: it keeps
	// the anchor and finalizes nothing (AC-LIF-06-03).
	delayed := attestedEvidence(heads["A5"], AttestedSnapshot(heads["B5"], heads["B4"], heads["B3"]))
	if _, err := observatory.ObserveHeader(f.token, delayed, anc, 7); !errors.Is(err, ErrBranch) {
		t.Fatalf("delayed side-branch callback: %v", err)
	}
	// The accepted anchor A3 is no longer on the canonical execution
	// ancestry of the new head B5: the observation of the new head is a
	// proven regression and stops the observatory.
	newHead := attestedEvidence(heads["B5"], AttestedSnapshot(heads["B5"], heads["B4"], heads["B3"]))
	if _, err := observatory.ObserveHeader(f.token, newHead, anc, 8); !errors.Is(err, ErrRegression) {
		t.Fatalf("observation below the abandoned anchor: %v", err)
	}
	// The projections of the removed activations use only typed evidence:
	// with the anchor branch abandoned, nothing finalizes them.
	evidence := Evidence{ObservedExecutionHead: heads["B5"], ObservedNativeHeaderHead: heads["B5"],
		HeaderFinality:           AttestedSnapshot(heads["B5"], heads["B4"], heads["B3"]),
		NativeEffectiveFinalized: KnownReference(heads["B3"], NativeLive),
		EffectiveBasis:           BasisHeaderSnapshot}
	headerStatus, _, err := Project(evidence, heads["A4"], anc)
	if err != nil || headerStatus != CanonicalUnfinalized {
		t.Fatalf("abandoned activation A4 projects %s %v, want %s", headerStatus, err, CanonicalUnfinalized)
	}
}
