package eth

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
	registry "github.com/sigaev-pro/bc-indexer/adapters/lifecycle_registry"
	recoverygc "github.com/sigaev-pro/bc-indexer/adapters/recovery_gc"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
	validator "github.com/sigaev-pro/bc-indexer/test/lifecycle_validator"
)

// branchA and branchB are the two competing branches of the scenarios:
// genesis -> a1 -> a2 and genesis -> b1 -> b2, one body transaction each.
func branchA() []Block {
	return []Block{
		{Number: 1, Hash: blockHash("a1").String(), Parent: blockHash("genesis").String(),
			Tx: blockHash("a1-tx").String()},
		{Number: 2, Hash: blockHash("a2").String(), Parent: blockHash("a1").String(),
			Tx: blockHash("a2-tx").String()},
	}
}

func branchB() []Block {
	return []Block{
		{Number: 1, Hash: blockHash("b1").String(), Parent: blockHash("genesis").String(),
			Tx: blockHash("b1-tx").String()},
		{Number: 2, Hash: blockHash("b2").String(), Parent: blockHash("b1").String(),
			Tx: blockHash("b2-tx").String()},
	}
}

func descriptorKinds(m *Manifest) []string {
	out := make([]string, len(m.Descriptors))
	for i, d := range m.Descriptors {
		out[i] = fmt.Sprintf("%s/%d", d.Kind, d.Revision)
	}
	return out
}

// TestRemovedForReadyAppliedRegardlessOfPubAck is AC-LIF-02-01: an Applied
// manifest is READY; the canonical reorg happens before any Publish and
// after one published part. The durable Removed of the reorg decision
// references the former activation in every variant, and the Applied
// delivery is not guessed from the publication state: the switch step of
// the model has no delivery input at all.
func TestRemovedForReadyAppliedRegardlessOfPubAck(t *testing.T) {
	for _, variant := range []DeliveryKind{DeliveryUnpublished, DeliveryOnePart, DeliveryAcked} {
		t.Run(string(variant), func(t *testing.T) {
			n := newNode()
			imports := n.importBlocks(branchA()...)
			for _, m := range imports {
				if !m.Ready {
					t.Fatalf("import manifest of decision %d is not READY", m.Decision)
				}
			}
			delivery := newDelivery()
			delivery.publish(imports[0].Decision, variant)
			if delivery.of(imports[0].Decision) != variant {
				t.Fatal("the delivery state did not stick")
			}
			// The competing branch is stored as side blocks, then the
			// canonical switch replaces both blocks of A.
			for _, b := range branchB() {
				n.sideImport(b)
			}
			switches := n.switchCanonical(branchB()[1].Hash)
			reorg := switches[0]
			if !reorg.Ready {
				t.Fatal("the reorg manifest is not READY")
			}
			var removed []semantic.SemanticDescriptor
			for _, d := range reorg.Descriptors {
				if d.Kind == semantic.KindRemoved {
					removed = append(removed, d)
				}
			}
			if len(removed) != 2 {
				t.Fatalf("the reorg created %d Removed events, want one per replaced block", len(removed))
			}
			for i, d := range removed {
				want := semantic.ActivationID{DecisionSequence: imports[i].Decision, EntityOrdinal: 0}
				if d.Activation.ActivationID != want || d.Revision == 0 {
					t.Fatalf("Removed %d references %+v rev %d, want the former activation %v rev > 0",
						i, d.Activation.ActivationID, d.Revision, want)
				}
			}
		})
	}
}

// TestReorgIsOneDecisionWithOneBatch is AC-CAP-08-02: one native reorg
// decision replaces several blocks; its manifest carries the complete
// Removed set of the replaced activations together with its Applied
// entities under exactly one batch, built in chunks, and the events are
// not split into block batches. The SIDE decisions of the side blocks
// allocate no batch.
func TestReorgIsOneDecisionWithOneBatch(t *testing.T) {
	n := newNode()
	imports := n.importBlocks(branchA()...)
	var sides []uint64
	for _, b := range branchB() {
		sides = append(sides, n.sideImport(b).Sequence)
	}
	switches := n.switchCanonical(branchB()[1].Hash)
	reorg := switches[0]
	if reorg.Decision == imports[1].Decision {
		t.Fatal("the reorg is the same decision as the import")
	}
	// The full ordered set of the reorg decision: both Removed events,
	// then the block and transaction Applied events of its own block. The
	// Removed set of the multi-block replacement is complete in this one
	// batch, not split per replaced block.
	want := []string{"removed/1", "removed/1", "applied/0", "applied/0"}
	if got := descriptorKinds(reorg); !slices.Equal(got, want) {
		t.Fatalf("reorg descriptors %v, want %v", got, want)
	}
	// Exactly one batch allocation for the reorg decision; the SIDE
	// decisions allocated none.
	if _, err := n.alloc.Allocation(reorg.Decision); err != nil {
		t.Fatalf("the reorg decision has no batch: %v", err)
	}
	for _, seq := range sides {
		if _, err := n.alloc.Allocation(seq); !errors.Is(err, semantic.ErrUnallocated) {
			t.Fatalf("SIDE decision %d holds a batch: %v", seq, err)
		}
	}
	// The header counts every descriptor of the decision.
	header := headerOf(t, n, reorg.Decision)
	if header.Count != len(reorg.Descriptors) || header.Decision != reorg.Decision {
		t.Fatalf("header %+v does not cover the manifest of %d", header, reorg.Decision)
	}
	// The second block of the branch is applied by its own decision, with
	// no Removed group of its own.
	if len(switches) != 2 || slices.Contains(descriptorKinds(switches[1]), "removed/1") {
		t.Fatalf("the branch walk produced %d manifests", len(switches))
	}
	// The registry committed the same full ordered set: the reorg decision
	// is past the applied-decision frontier with exactly these events.
	d, ok, err := n.registry.Decision(registry.NativeDecisionRef{NodeInstanceID: nodeID,
		ProducerID: producerID, RunID: runID, DecisionSequence: reorg.Decision})
	if err != nil || !ok || d.State != registry.StateApplied {
		t.Fatalf("the reorg decision is %+v %v %v, want an applied registry decision", d, ok, err)
	}
	if len(d.Application.Events) != len(reorg.Descriptors) {
		t.Fatalf("the registry applied %d events, want the %d of the manifest",
			len(d.Application.Events), len(reorg.Descriptors))
	}
}

// TestKnownBranchBecomesCanonicalWithoutNewExecution: a side block stored
// by a SIDE decision retains its corpus and observation; the canonical
// switch then applies it with no new execution callback, no targeted
// replay and no new admission. The registry reuses the retained speculative
// corpus, and the reorg decision of the switch removes the replaced
// activations.
func TestKnownBranchBecomesCanonicalWithoutNewExecution(t *testing.T) {
	n := newNode()
	imports := n.importBlocks(branchA()...)
	side := branchB()[0]
	record := n.sideImport(side)
	// The SIDE decision retains its member: no batch, no public events.
	if _, err := n.alloc.Allocation(record.Sequence); !errors.Is(err, semantic.ErrUnallocated) {
		t.Fatalf("the SIDE decision allocated a batch: %v", err)
	}
	if _, ok := n.observationOf(side.Hash); !ok {
		t.Fatal("the SIDE decision did not retain its observation")
	}
	// The speculative corpus stays recorded under the SIDE decision.
	corpus, ok, err := n.registry.Corpus(producerID, runID, blockHash(side.Hash))
	if err != nil || !ok {
		t.Fatalf("the SIDE decision did not retain its corpus: %v %v", ok, err)
	}
	if corpus.CapturedBy.DecisionSequence != record.Sequence {
		t.Fatalf("the corpus was captured by decision %d, want the SIDE decision %d",
			corpus.CapturedBy.DecisionSequence, record.Sequence)
	}
	// The known branch switch: the retained observation is the decision
	// member; nothing is replayed.
	switches := n.switchCanonical(side.Hash)
	if len(switches) != 1 {
		t.Fatalf("the switch produced %d decisions", len(switches))
	}
	if len(n.replayed) != 0 {
		t.Fatalf("the known branch replayed %v", n.replayed)
	}
	manifest := switches[0]
	if !manifest.Ready || len(manifest.Descriptors) != 4 {
		t.Fatalf("switch manifest %+v", manifest.Descriptors)
	}
	if manifest.Descriptors[0].Kind != semantic.KindRemoved ||
		manifest.Descriptors[0].Activation.ActivationID.DecisionSequence != imports[0].Decision ||
		manifest.Descriptors[1].Activation.ActivationID.DecisionSequence != imports[1].Decision {
		t.Fatal("the switch does not remove the replaced activations")
	}
	// The new activation of the side block belongs to the switch decision,
	// and its corpus is the speculative one the SIDE decision captured.
	applied := manifest.Descriptors[2]
	if applied.Kind != semantic.KindApplied ||
		applied.Activation.ActivationID.DecisionSequence != manifest.Decision {
		t.Fatalf("the side block was not applied by the switch: %+v", applied)
	}
	activation, ok, err := n.registry.Activation(activationKey(manifest.Decision))
	if err != nil || !ok {
		t.Fatalf("the switch applied no activation: %v %v", ok, err)
	}
	if !activation.Speculative || activation.CorpusDigest != corpus.CorpusDigest {
		t.Fatalf("the applied activation is %+v, want the speculative corpus of the SIDE decision",
			activation)
	}
}

// TestReapplyCreatesNewActivations: the same branch returning canonical
// after a reorg receives new activations (a second application of one
// branch never shares an activation with the first), its corpus is rebuilt
// by a native targeted replay of the retained inputs while the registry
// reuses the corpus record of the first application, and the old
// activations stay terminal.
func TestReapplyCreatesNewActivations(t *testing.T) {
	n := newNode()
	imports := n.importBlocks(branchA()...)
	for _, b := range branchB() {
		n.sideImport(b)
	}
	reorg := n.switchCanonical(branchB()[1].Hash)
	// The branch of A returns canonical: its blocks are known side blocks
	// without open observations, so their corpus is rebuilt by a native
	// targeted replay before the switch decisions.
	reapply := n.switchCanonical(branchA()[1].Hash)
	for _, block := range branchA() {
		if !n.replayed[block.Hash] {
			t.Fatalf("block %s was not replayed from its retained inputs", block.Hash)
		}
	}
	if reapply[0].Decision == reorg[0].Decision {
		t.Fatal("the reapply is the same decision as the reorg")
	}
	// The reapplied blocks get new activations; the old ones are terminal.
	oldA1 := activationKey(imports[0].Decision)
	newA1 := activationKey(reapply[0].Decision)
	if oldA1 == newA1 {
		t.Fatal("the reapply reused the first activation")
	}
	oldRecord, ok, err := n.registry.Activation(oldA1)
	if err != nil || !ok {
		t.Fatalf("the first activation of a1 is gone: %v %v", ok, err)
	}
	if oldRecord.State != registry.ActivationRemoved {
		t.Fatalf("the first activation of a1 is %s, want REMOVED", oldRecord.State)
	}
	newRecord, ok, err := n.registry.Activation(newA1)
	if err != nil || !ok || newRecord.State != registry.ActivationActive {
		t.Fatalf("the reapplied activation of a1 is %+v %v %v, want ACTIVE", newRecord, ok, err)
	}
	// The reorg branch's activations are removed by the reapply decision.
	var removedB []uint64
	for _, d := range reapply[0].Descriptors {
		if d.Kind == semantic.KindRemoved {
			removedB = append(removedB, d.Activation.ActivationID.DecisionSequence)
		}
	}
	if !slices.Equal(removedB, []uint64{reorg[0].Decision, reorg[1].Decision}) {
		t.Fatalf("the reapply removed %v, want the reorg activations %d and %d", removedB,
			reorg[0].Decision, reorg[1].Decision)
	}
}

// TestReorderedEventsFoldIdentically: the events the registry publishes —
// Removed, Applied and StatusChanged of the whole history — fold to the
// same activation records in every delivery order (the E02-03 oracle): a
// terminal Removed survives a late Applied, the reapply is a separate
// record, and a status revision never reactivates anything. The SIDE
// decisions of the side blocks contribute no event at all.
func TestReorderedEventsFoldIdentically(t *testing.T) {
	n := newNode()
	imports := n.importBlocks(branchA()...)
	sides := map[uint64]bool{}
	for _, b := range branchB() {
		sides[n.sideImport(b).Sequence] = true
	}
	n.switchCanonical(branchB()[1].Hash)
	reapply := n.switchCanonical(branchA()[1].Hash)
	// Finality covers the reapplied branch: the StatusChanged events are
	// committed through the registry under the FINALITY decision of the
	// setter call, with revisions of the same activation counters.
	n.setFinalized(branchA()[1].Hash)
	events := n.published()
	if len(events) == 0 {
		t.Fatal("the registry published no events")
	}
	for _, event := range events {
		if sides[event.Descriptor.Activation.ActivationID.DecisionSequence] {
			t.Fatalf("a SIDE decision published %s", event.Descriptor.Kind)
		}
	}
	orders := map[string][]registry.PublishedEvent{
		"natural":     append([]registry.PublishedEvent{}, events...),
		"reversed":    reversedEvents(events),
		"removed":     removedFirst(events),
		"statusFirst": statusFirst(events),
	}
	var reference map[validator.ActivationKey]validator.Record
	for name, order := range orders {
		automaton := validator.New()
		for _, event := range order {
			if _, err := event.Descriptor.Digest(); err != nil {
				t.Fatalf("%s: digest: %v", name, err)
			}
			if _, err := automaton.Apply(event.Descriptor, digest32(event.Content)); err != nil {
				t.Fatalf("%s: apply: %v", name, err)
			}
		}
		records := automaton.Records()
		if reference == nil {
			reference = records
			continue
		}
		if len(records) != len(reference) {
			t.Fatalf("%s folded %d records, want %d", name, len(records), len(reference))
		}
		for key, record := range reference {
			other, ok := records[key]
			if !ok || !record.Equal(other) {
				t.Fatalf("%s folded %+v differently: %s vs %s", name, key, record, other)
			}
		}
	}
	// The terminal and applied states of the history are order-independent
	// and match the registry records.
	firstA1 := validator.ActivationKey{Network: network, ProducerID: producerID, RunID: runID,
		Activation: semantic.ActivationID{DecisionSequence: imports[0].Decision}}
	if record := reference[firstA1]; record.State != validator.StateRemoved || record.RemovedRevision != 1 {
		t.Fatalf("the first activation of a1 folded to %s, want REMOVED at revision 1", record)
	}
	reappliedA1 := validator.ActivationKey{Network: network, ProducerID: producerID, RunID: runID,
		Activation: semantic.ActivationID{DecisionSequence: reapply[0].Decision}}
	if record := reference[reappliedA1]; record.State != validator.StateActive {
		t.Fatalf("the reapplied activation folded to %s, want ACTIVE", record.State)
	}
	// The status revision of the covered activation is the registry
	// frontier, and it reactivated nothing.
	status, ok, err := n.registry.Activation(activationKey(reapply[0].Decision))
	if err != nil || !ok {
		t.Fatalf("the reapplied activation of a1 is gone: %v %v", ok, err)
	}
	if statusRecord := reference[reappliedA1]; statusRecord.State != validator.StateActive ||
		statusRecord.MaxRevision != status.MaxRevision || status.MaxRevision == 0 {
		t.Fatalf("the status of the reapplied activation folded to %+v, want ACTIVE at revision %d",
			statusRecord, status.MaxRevision)
	}
}

// TestGcAnchorAndMarkerCleanup: the markers of the decisions the registry
// applied are deleted by the bridge cleanup, and the GC anchor acceptance
// from the durable finalized evidence survives it; the accepted anchor is
// the shared recovery_gc record, and the removed activations below it are
// releasable covered entities.
func TestGcAnchorAndMarkerCleanup(t *testing.T) {
	n := newNode()
	imports := n.importBlocks(branchA()...)
	for _, b := range branchB() {
		n.sideImport(b)
	}
	reorg := n.switchCanonical(branchB()[1].Hash)
	n.setFinalized(branchB()[1].Hash)
	// The markers of the applied decisions are gone; the anchor acceptance
	// from the finalized observation still holds.
	for _, sequence := range []uint64{imports[0].Decision, reorg[0].Decision} {
		ok, err := n.db.Has(decision.MarkerKey(admission.Producer{ProducerID: "producer-1",
			RunID: "run-1"}, sequence))
		if err != nil {
			t.Fatalf("marker read: %v", err)
		}
		if ok {
			t.Fatalf("the cleanup kept the marker of the applied decision %d", sequence)
		}
	}
	anchor, err := n.acceptAnchor()
	if err != nil {
		t.Fatalf("accept after the marker cleanup: %v", err)
	}
	want := hashOfHex(branchB()[1].Hash)
	if anchor.Anchor.Hash != want || anchor.Anchor.Height != 2 || anchor.Network != network {
		t.Fatalf("anchor %+v, want the finalized block of the B branch", anchor.Anchor)
	}
	if anchor.Decision.DecisionSequence == 0 || anchor.EvidenceDigest == "" {
		t.Fatalf("anchor %+v binds no FINALITY decision evidence", anchor)
	}
	// The abandoned A activations are terminal in the registry; the GC
	// engine releases them as entities of the branch proven divergent from
	// the accepted anchor of the B branch.
	entities := []recoverygc.Entity{}
	for _, sequence := range []uint64{imports[0].Decision, imports[1].Decision} {
		record, ok, err := n.registry.Activation(activationKey(sequence))
		if err != nil || !ok || record.State != registry.ActivationRemoved {
			t.Fatalf("activation %d is %+v %v %v, want REMOVED", sequence, record, ok, err)
		}
		entities = append(entities, recoverygc.Entity{
			ID: recoverygc.EntityID{Network: network, ProducerID: producerID, RunID: runID,
				DecisionSequence: sequence, EntityOrdinal: 0},
			Block: recoverygc.AnchorRef{Height: n.chain.number[record.BlockHash.String()],
				Hash: record.BlockHash},
			Branch: recoverygc.BranchNoncanonical,
			Terminal: &recoverygc.TerminalEvidence{Revision: record.RemovedRevision,
				Decision: recoverygc.NativeDecisionRef{NodeInstanceID: nodeID, ProducerID: producerID,
					RunID: runID, DecisionSequence: reorg[0].Decision}},
			Pins: []string{"a-pin"},
		})
	}
	decisions, err := n.gc.Evaluate(entities)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	for i, decision := range decisions {
		if !decision.Release || decision.Reason != recoverygc.ReasonReleaseDivergentBranch {
			t.Fatalf("entity %d evaluated to %s, want a divergent-branch release", i, decision)
		}
	}
	// The applied activation of the accepted canonical branch below the
	// anchor is a covered release: the finalized B2 covers B1.
	live := recoverygc.Entity{
		ID: recoverygc.EntityID{Network: network, ProducerID: producerID, RunID: runID,
			DecisionSequence: reorg[0].Decision, EntityOrdinal: 0},
		Block:   recoverygc.AnchorRef{Height: 1, Hash: hashOfHex(branchB()[0].Hash)},
		Branch:  recoverygc.BranchCanonical,
		Applied: true,
	}
	got, err := n.gc.Evaluate([]recoverygc.Entity{live})
	if err != nil {
		t.Fatalf("evaluate the covered entity: %v", err)
	}
	if !got[0].Release || got[0].Reason != recoverygc.ReasonReleaseCovered {
		t.Fatalf("the covered entity evaluated to %s, want a covered release", got[0])
	}
}

// TestManifestCrashAndMarkerRepeatRestoresOneBatch is AC-CAP-08-02: a
// crash inside the manifest construction leaves a partial manifest that is
// not READY; repeating the same native marker restores the one former
// batch and the full ordered set of descriptors, without splitting the
// events into block batches. The registry application of the decision
// follows the READY header, so the crash applied nothing.
func TestManifestCrashAndMarkerRepeatRestoresOneBatch(t *testing.T) {
	for _, fault := range []string{"allocation", "chunk/0", "chunk/1", "header"} {
		t.Run(fault, func(t *testing.T) {
			n := newNode()
			n.importBlocks(branchA()...)
			for _, b := range branchB() {
				n.sideImport(b)
			}
			// The reorg decision (the first of the branch walk) is made and
			// its marker is durable; the manifest construction crashes at
			// the fault point.
			newHead := branchB()[0].Hash
			fork := n.chain.fork(newHead)
			replaced := append([]string{}, n.chain.canonical[len(fork):]...)
			obs, ok := n.observationOf(newHead)
			if !ok {
				t.Fatalf("branch block %s has no observation", newHead)
			}
			pass, err := n.gate.Enter(n.token, "geth.set-canonical")
			if err != nil {
				t.Fatalf("enter: %v", err)
			}
			record, err := pass.Intend(n.bridge, decision.Intent{Kind: decision.Canonical,
				OldHead: n.head(), NewHead: decision.Head{Number: 1, Hash: newHead},
				Members: []decision.Member{{Observation: obs.Key, Block: newHead,
					ExecReadyDigest: obs.ExecReady.Digest, ChildRoot: childRoot}},
				EvidenceDigest: intentEvidence(decision.Canonical, newHead), Events: true})
			if err != nil {
				t.Fatalf("intend: %v", err)
			}
			n.marker(record)
			n.mirrorIntent(record, nil)
			if _, err := n.bridge.Resolve(n.token, record.Sequence); err != nil {
				t.Fatalf("resolve: %v", err)
			}
			n.settle(record)
			if err := pass.Exit(); err != nil {
				t.Fatalf("exit: %v", err)
			}
			n.fault = fault
			crashErr := func() error {
				defer func() { n.fault = "" }()
				_, err := n.buildManifest(record, replaced)
				return err
			}()
			if !errors.Is(crashErr, errCrash) {
				t.Fatalf("manifest build error %v, want the injected crash", crashErr)
			}
			if n.isReady(record.Sequence) {
				t.Fatal("the partial manifest is READY")
			}
			if d, ok, err := n.registry.Decision(dref(record.Sequence)); err != nil || !ok ||
				d.Application != nil {
				t.Fatalf("the crash applied the decision: %+v %v %v", d, ok, err)
			}
			// The crash may have left unsynced writes; both extremes of
			// the power loss reach the same rebuild from the same marker.
			crashed := n.db
			for _, keep := range []uint{0, ^uint(0)} {
				n.db = crashed.powerLoss(keep)
				n.reopen()
				manifest, err := n.buildManifest(record, replaced)
				if err != nil {
					t.Fatalf("rebuild after keeping pending %x: %v", keep, err)
				}
				if !manifest.Ready || !n.isReady(record.Sequence) {
					t.Fatal("the rebuilt manifest is not READY")
				}
				want := []string{"removed/1", "removed/1", "applied/0", "applied/0"}
				if got := descriptorKinds(manifest); !slices.Equal(got, want) {
					t.Fatalf("rebuilt descriptors %v, want %v", got, want)
				}
				// One former batch restored: the third batch of the run
				// (two imports before it), not a second allocation.
				_, run, sequence := manifest.Batch.Decode()
				if run != runID || sequence != 3 {
					t.Fatalf("rebuilt batch %s, want the former batch sequence 3 of the run", manifest.Batch)
				}
				// The registry application of the rebuilt manifest created
				// exactly the activations of its Applied events.
				for _, d := range manifest.Descriptors {
					if d.Kind != semantic.KindApplied {
						continue
					}
					_, ok, err := n.registry.Activation(registry.ActivationKey{Network: network,
						ProducerID: producerID, RunID: runID, Activation: d.Activation.ActivationID})
					if err != nil || !ok {
						t.Fatalf("the applied event of %d created no activation: %v %v",
							d.Activation.ActivationID.DecisionSequence, ok, err)
					}
				}
			}
		})
	}
}

func headerOf(t *testing.T, n *node, sequence uint64) headerRecord {
	t.Helper()
	data, ok := n.db.durableString(manifestKey(sequence, "header"))
	if !ok {
		t.Fatalf("manifest of %d has no header", sequence)
	}
	var header headerRecord
	if err := json.Unmarshal([]byte(data), &header); err != nil {
		t.Fatalf("header of %d: %v", sequence, err)
	}
	return header
}

// hashOfHex decodes one 64-digit model hash string into the shared Hash32
// value: the anchor references carry the decoded form.
func hashOfHex(value string) semantic.Hash32 {
	var out semantic.Hash32
	raw, err := hex.DecodeString(value)
	if err != nil {
		panic(err)
	}
	copy(out[:], raw)
	return out
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

func reversedEvents(in []registry.PublishedEvent) []registry.PublishedEvent {
	out := make([]registry.PublishedEvent, len(in))
	for i, event := range in {
		out[len(in)-1-i] = event
	}
	return out
}

func removedFirst(in []registry.PublishedEvent) []registry.PublishedEvent {
	out := append([]registry.PublishedEvent{}, in...)
	slices.SortStableFunc(out, func(a, b registry.PublishedEvent) int {
		left, right := 0, 0
		if a.Descriptor.Kind == semantic.KindRemoved {
			left = 1
		}
		if b.Descriptor.Kind == semantic.KindRemoved {
			right = 1
		}
		return right - left
	})
	return out
}

func statusFirst(in []registry.PublishedEvent) []registry.PublishedEvent {
	out := append([]registry.PublishedEvent{}, in...)
	slices.SortStableFunc(out, func(a, b registry.PublishedEvent) int {
		left, right := 0, 0
		if a.Descriptor.Kind == semantic.KindStatus {
			left = 1
		}
		if b.Descriptor.Kind == semantic.KindStatus {
			right = 1
		}
		return left - right
	})
	return out
}
