package eth

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/extractor/decision"
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
}

// TestKnownBranchBecomesCanonicalWithoutNewExecution: a side block stored
// by a SIDE decision retains its corpus and observation; the canonical
// switch then applies it with no new execution callback, no targeted
// replay and no new admission, and the reorg decision of the switch
// removes the replaced activations.
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
	// The new activation of the side block belongs to the switch decision.
	applied := manifest.Descriptors[2]
	if applied.Kind != semantic.KindApplied ||
		applied.Activation.ActivationID.DecisionSequence != manifest.Decision {
		t.Fatalf("the side block was not applied by the switch: %+v", applied)
	}
}

// TestReapplyCreatesNewActivations: the same branch returning canonical
// after a reorg receives new activations (a second application of one
// branch never shares an activation with the first), its corpus is rebuilt
// by targeted replay of the retained inputs, and the old activations stay
// terminal.
func TestReapplyCreatesNewActivations(t *testing.T) {
	n := newNode()
	imports := n.importBlocks(branchA()...)
	for _, b := range branchB() {
		n.sideImport(b)
	}
	reorg := n.switchCanonical(branchB()[1].Hash)
	// The branch of A returns canonical: its blocks are known side blocks
	// without open observations, so their corpus is rebuilt by targeted
	// replay before the switch decisions.
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
	oldA1 := semantic.ActivationID{DecisionSequence: imports[0].Decision, EntityOrdinal: 0}
	newA1 := semantic.ActivationID{DecisionSequence: reapply[0].Decision, EntityOrdinal: 0}
	if oldA1 == newA1 {
		t.Fatal("the reapply reused the first activation")
	}
	if n.registry.byID[oldA1] == nil || !n.registry.byID[oldA1].removed {
		t.Fatal("the first activation of a1 is not terminal after the reorg")
	}
	if n.registry.byID[newA1] == nil || n.registry.byID[newA1].removed {
		t.Fatal("the reapplied activation of a1 is not applied")
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

// TestReorderedEventsFoldIdentically: the Removed, Applied and Status
// descriptors of the whole history fold to the same activation records in
// every delivery order (the E02-03 oracle): a terminal Removed survives a
// late Applied, the reapply is a separate record, and a status revision
// never reactivates anything.
func TestReorderedEventsFoldIdentically(t *testing.T) {
	n := newNode()
	imports := n.importBlocks(branchA()...)
	for _, b := range branchB() {
		n.sideImport(b)
	}
	reorg := n.switchCanonical(branchB()[1].Hash)
	reapply := n.switchCanonical(branchA()[1].Hash)
	// Finality covers the reapplied branch: the status descriptors are
	// derived from the finality evidence with revisions of the same
	// activation counter.
	n.setFinalized(branchA()[1].Hash)
	status := n.statusDescriptors()
	if len(status) == 0 {
		t.Fatal("no activation is covered by the finality evidence")
	}
	var events []semantic.SemanticDescriptor
	for _, m := range imports {
		events = append(events, m.Descriptors...)
	}
	for _, m := range reorg {
		events = append(events, m.Descriptors...)
	}
	for _, m := range reapply {
		events = append(events, m.Descriptors...)
	}
	events = append(events, status...)
	orders := map[string][]semantic.SemanticDescriptor{
		"natural":     append([]semantic.SemanticDescriptor{}, events...),
		"reversed":    reversed(events),
		"removed":     removedFirst(events),
		"statusFirst": statusFirst(events),
	}
	var reference map[validator.ActivationKey]validator.Record
	for name, order := range orders {
		automaton := validator.New()
		for _, d := range order {
			digest, err := d.Digest()
			if err != nil {
				t.Fatalf("%s: digest: %v", name, err)
			}
			if _, err := automaton.Apply(d, validator.Digest(digest)); err != nil {
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
	// The terminal and applied states of the history are order-independent.
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
}

// TestManifestCrashAndMarkerRepeatRestoresOneBatch is AC-CAP-08-02: a
// crash inside the manifest construction leaves a partial manifest that is
// not READY; repeating the same native marker restores the one former
// batch and the full ordered set of descriptors, without splitting the
// events into block batches.
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
				Members: []decision.Member{{Observation: obs.Key, ExecReadyDigest: obs.ExecReady.Digest,
					ChildRoot: childRoot}},
				EvidenceDigest: intentEvidence(decision.Canonical, newHead), Events: true})
			if err != nil {
				t.Fatalf("intend: %v", err)
			}
			n.marker(record)
			if _, err := n.bridge.Resolve(n.token, record.Sequence); err != nil {
				t.Fatalf("resolve: %v", err)
			}
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

func reversed(in []semantic.SemanticDescriptor) []semantic.SemanticDescriptor {
	out := make([]semantic.SemanticDescriptor, len(in))
	for i, d := range in {
		out[len(in)-1-i] = d
	}
	return out
}

func removedFirst(in []semantic.SemanticDescriptor) []semantic.SemanticDescriptor {
	out := append([]semantic.SemanticDescriptor{}, in...)
	slices.SortStableFunc(out, func(a, b semantic.SemanticDescriptor) int {
		left, right := 0, 0
		if a.Kind == semantic.KindRemoved {
			left = 1
		}
		if b.Kind == semantic.KindRemoved {
			right = 1
		}
		return right - left
	})
	return out
}

func statusFirst(in []semantic.SemanticDescriptor) []semantic.SemanticDescriptor {
	out := append([]semantic.SemanticDescriptor{}, in...)
	slices.SortStableFunc(out, func(a, b semantic.SemanticDescriptor) int {
		left, right := 0, 0
		if a.Kind == semantic.KindStatus {
			left = 1
		}
		if b.Kind == semantic.KindStatus {
			right = 1
		}
		return left - right
	})
	return out
}
