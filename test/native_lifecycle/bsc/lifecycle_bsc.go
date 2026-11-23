// The BSC scenario model of E05-02.reorg: it drives the real admission and
// decision packages and the BSC finality observatory of the assembled
// extractor module over an in-memory native store, mirrors every native
// decision into the real lifecycle registry of E05-01.registry, and
// observes the GC anchor on the real recovery GC engine of
// E05-01.activation_gc. The Applied and Removed descriptors of the
// decisions are committed as the registry application plans, so the
// published event set the registry exposes is the evidence, and it folds
// through the E02-03 lifecycle oracle. The store and the chain oracle are
// in store.go; the tests are in reorg_test.go.
package bsc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"testing"

	registry "github.com/sigaev-pro/bc-indexer/adapters/lifecycle_registry"
	recoverygc "github.com/sigaev-pro/bc-indexer/adapters/recovery_gc"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"

	"github.com/ethereum/go-ethereum/extractor/admission"
	bscfinality "github.com/ethereum/go-ethereum/extractor/bsc/finality"
	"github.com/ethereum/go-ethereum/extractor/decision"
)

// ErrModel rejects a malformed model step.
var ErrModel = errors.New("native lifecycle model error")

// id16 derives the fixed model identity of a seed.
func id16(seed string) semantic.ID16 {
	var out semantic.ID16
	sum := sha256.Sum256([]byte(seed))
	copy(out[:], sum[:16])
	return out
}

// chainID derives the fixed model chain ID of a seed.
func chainID(seed string) semantic.UInt256 {
	var out semantic.UInt256
	sum := sha256.Sum256([]byte(seed))
	copy(out[:], sum[:32])
	return out
}

// blockHashOf derives the model block hash of a head hash string, the same
// formula the original evidence used.
func blockHashOf(value string) semantic.Hash32 {
	var out semantic.Hash32
	sum := sha256.Sum256([]byte(value))
	copy(out[:], sum[:32])
	return out
}

// sum64 returns the hex SHA-256 of the parts.
func sum64(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// dref returns the native decision reference of one bridge decision
// sequence on the semantic identity model.
func dref(sequence uint64) registry.NativeDecisionRef {
	return registry.NativeDecisionRef{NodeInstanceID: nodeID, ProducerID: producer, RunID: run,
		DecisionSequence: sequence}
}

// activationKey returns the activation key of the block entity of one
// decision: ordinal 0.
func activationKey(sequence uint64) registry.ActivationKey {
	return registry.ActivationKey{Network: network, ProducerID: producer, RunID: run,
		Activation: semantic.ActivationID{DecisionSequence: sequence, EntityOrdinal: 0}}
}

// registryMirror answers the applied-decision frontier of the bridge from
// the lifecycle registry: a decision the registry applied needs its native
// marker no longer, so the bridge cleanup deletes it.
type registryMirror struct {
	registry *registry.Registry
}

func (m registryMirror) Applied(sequence uint64, digest string) (bool, error) {
	d, ok, err := m.registry.Decision(dref(sequence))
	if err != nil || !ok {
		return false, err
	}
	if d.State != registry.StateApplied {
		return false, nil
	}
	delivery := decision.Batch
	if d.Kind == registry.KindSide {
		delivery = decision.Checkpoint
	}
	want, err := decision.DecidedDigest(d.IntentDigest, delivery)
	if err != nil {
		return false, err
	}
	return want == digest, nil
}

// fixture is one BSC producer run: its store, admission, decision bridge,
// finality observatory, recovery GC engine, lifecycle registry and semantic
// allocator.
type fixture struct {
	t        *testing.T
	db       *memStore
	pins     *reorgPins
	inputs   *reorgInputs
	roots    *reorgRoots
	token    *admission.Token
	adm      *admission.Admission
	bridge   *decision.Bridge
	registry *registry.Registry
	gc       *recoverygc.Engine
	storeID  string
	alloc    *semantic.Allocator
	// source sequences the descriptor source refs of the model.
	source uint64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := newStore()
	token := admission.NewGuard().Acquire()
	f := &fixture{t: t, db: db,
		pins:   &reorgPins{roots: map[string]string{}, digests: map[string]string{}},
		inputs: &reorgInputs{capsules: map[string]string{}, consumed: map[string]admission.Consumed{}},
		roots:  &reorgRoots{durable: map[string]bool{}},
		token:  token, storeID: "bsc-store-1"}
	f.openBridge()
	if err := f.bridge.GoLive(token); err != nil {
		t.Fatalf("go live: %v", err)
	}
	alloc, err := semantic.NewAllocator(producer, run)
	if err != nil {
		t.Fatal(err)
	}
	f.alloc = alloc
	return f
}

// openBridge (re)opens the admission, the registry, the recovery GC engine
// and the decision bridge on the store of the fixture, as a node start
// does. The observatory of a test is opened separately with the chain of
// that test, because the engine proves ancestry on it.
func (f *fixture) openBridge() {
	f.t.Helper()
	adm, err := admission.Open(f.token, f.db, admission.Producer{ProducerID: "bsc-producer", RunID: "run-1"},
		f.pins, f.inputs)
	if err != nil {
		f.t.Fatalf("admission open: %v", err)
	}
	reg, _, err := registry.Open(f.db)
	if err != nil {
		f.t.Fatalf("registry open: %v", err)
	}
	bridge, err := decision.Open(f.token, f.db, adm, f.storeID, f.roots, registryMirror{registry: reg})
	if err != nil {
		f.t.Fatalf("bridge open: %v", err)
	}
	f.adm, f.bridge, f.registry = adm, bridge, reg
}

// openObservatory opens the BSC finality observatory with the recovery GC
// engine over the given chain.
func (f *fixture) openObservatory(anc *chain) *bscfinality.Observatory {
	f.t.Helper()
	gc, _, err := recoverygc.Open(f.db, gcOracle{chain: anc}, gcNothingPending{}, gcReleaser{}, gcPins{})
	if err != nil {
		f.t.Fatalf("recoverygc open: %v", err)
	}
	f.gc = gc
	observatory, err := bscfinality.Open(f.token, f.db, f.adm, f.storeID,
		bscfinality.RunIdentity{Network: network, Node: nodeID, Producer: producer, Run: run}, gc)
	if err != nil {
		f.t.Fatalf("observatory open: %v", err)
	}
	return observatory
}

// sourceRef returns the next source provenance of the model descriptors.
func (f *fixture) sourceRef() semantic.SourceRef {
	f.source++
	return semantic.SourceRef{NodeInstanceID: nodeID, WriterEpoch: epochID, SourceSequence: f.source}
}

// registryKind maps the bridge decision kind to the registry kind of the
// same native decision.
func registryKind(kind decision.Kind) registry.DecisionKind {
	switch kind {
	case decision.Import:
		return registry.KindImport
	case decision.Side:
		return registry.KindSide
	case decision.Canonical:
		return registry.KindCanonical
	case decision.Finality:
		return registry.KindFinality
	}
	panic(fmt.Sprintf("%v: bridge kind %s", ErrModel, kind))
}

// mirrorIntent mirrors one intended bridge decision into the registry
// before native settles it, and captures the corpus of every member block
// under it: the durable evidence of a completed execution.
func (f *fixture) mirrorIntent(record decision.IntentRecord, corpusBlocks []decision.Member) {
	ref := dref(record.Sequence)
	if err := f.registry.Intend(ref, network, registryKind(record.Kind), record.Digest); err != nil {
		f.t.Fatalf("registry intend %d: %v", record.Sequence, err)
	}
	for _, member := range corpusBlocks {
		// The registry corpus of a block is the digest of its captured
		// extraction corpus, deterministic per block: a re-execution of
		// the same block recaptures the same corpus (the admission-level
		// ExecReady digest of the member stays per-observation).
		if err := f.registry.CaptureCorpus(ref, blockHashOf(member.Block), sum64("corpus", member.Block)); err != nil {
			f.t.Fatalf("registry corpus %s: %v", member.Block, err)
		}
	}
}

// settle mirrors the settled NATIVE_DECIDED marker of a bridge decision
// into the registry.
func (f *fixture) settle(record decision.IntentRecord) {
	f.t.Helper()
	if err := f.registry.SettleDecided(dref(record.Sequence)); err != nil {
		f.t.Fatalf("registry settle %d: %v", record.Sequence, err)
	}
}

// importBlock runs one observed import of block h with parent p to a
// decided IMPORT decision with public events, mirrors it into the registry
// and applies its Applied events. It returns the record.
func (f *fixture) importBlock(h, p decision.Head) decision.IntentRecord {
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
	f.mirrorIntent(record, intent.Members)
	if _, err := f.bridge.Resolve(f.token, record.Sequence); err != nil {
		f.t.Fatalf("resolve import %s: %v", h.Hash, err)
	}
	f.settle(record)
	f.applyImports(record)
	f.cleanup()
	return record
}

// applyImports applies the Applied events of one decided IMPORT decision:
// family block, entity ordinal 0, revision 0, a fresh activation of its
// decision sequence. It returns the Applied descriptor.
func (f *fixture) applyImports(record decision.IntentRecord) semantic.SemanticDescriptor {
	f.t.Helper()
	if record.Kind != decision.Import || !record.Events {
		f.t.Fatalf("applyImports of a %s decision without events", record.Kind)
	}
	block := blockHashOf(record.NewHead.Hash)
	d, err := semantic.NewBlockDescriptor(semantic.KindApplied, network, producer, run,
		record.Sequence, block, 0, f.sourceRef())
	if err != nil {
		f.t.Fatalf("applied descriptor: %v", err)
	}
	digest, err := d.Digest()
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.registry.Apply(dref(record.Sequence), registry.ApplicationPlan{Events: []registry.EventPlan{
		{Descriptor: d, Content: digest.String()}}}); err != nil {
		f.t.Fatalf("registry apply %d: %v", record.Sequence, err)
	}
	return d
}

// onCanonical applies the Removed events of one decided CANONICAL decision
// from old to the new head: every applied activation whose block is not on
// the canonical ancestry of the new head is removed, whatever the delivery
// state of its decision. Each Removed revision comes from the same
// activation counter as StatusChanged. The activations are visited by
// decision sequence, so the derived events are deterministic. It returns
// the Removed descriptors.
func (f *fixture) onCanonical(old, next decision.Head, record decision.IntentRecord,
	anc *chain) []semantic.SemanticDescriptor {
	f.t.Helper()
	if record.Kind != decision.Canonical {
		f.t.Fatalf("onCanonical of a %s decision", record.Kind)
	}
	events := []registry.EventPlan{}
	removed := []semantic.SemanticDescriptor{}
	for _, sequence := range f.appliedSequences() {
		key := activationKey(sequence)
		rec, ok, err := f.registry.Activation(key)
		if err != nil {
			f.t.Fatalf("activation %d: %v", sequence, err)
		}
		if !ok || rec.State != registry.ActivationActive {
			continue
		}
		blockHead := anc.headOfBlock(rec.BlockHash)
		onChain, err := anc.IsAncestorOrSelf(blockHead, next)
		if err != nil {
			f.t.Fatalf("ancestry: %v", err)
		}
		if onChain {
			continue
		}
		revision, err := f.registry.NextRevision(key)
		if err != nil {
			f.t.Fatalf("revision of %d: %v", sequence, err)
		}
		d, err := semantic.NewBlockDescriptor(semantic.KindRemoved, network, producer, run,
			sequence, rec.BlockHash, revision, f.sourceRef())
		if err != nil {
			f.t.Fatalf("removed descriptor: %v", err)
		}
		digest, err := d.Digest()
		if err != nil {
			f.t.Fatal(err)
		}
		events = append(events, registry.EventPlan{Descriptor: d, Content: digest.String()})
		removed = append(removed, d)
	}
	// The complete Removed set is one application of the canonical
	// decision, never one per replaced block.
	if len(events) != 0 {
		if _, err := f.registry.Apply(dref(record.Sequence), registry.ApplicationPlan{Events: events}); err != nil {
			f.t.Fatalf("registry apply the removals of %d: %v", record.Sequence, err)
		}
	}
	return removed
}

// appliedSequences lists the decision sequences of the applied block
// activations of the model in a deterministic order.
func (f *fixture) appliedSequences() []uint64 {
	out := []uint64{}
	for sequence := uint64(0); ; sequence++ {
		d, ok, err := f.registry.Decision(dref(sequence))
		if err != nil {
			f.t.Fatal(err)
		}
		if !ok {
			break
		}
		if d.Application == nil {
			continue
		}
		for _, event := range d.Application.Events {
			if event.Descriptor.Kind == semantic.KindApplied {
				out = append(out, sequence)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// marker models the native patch: the decision marker in the final batch.
func (f *fixture) marker(record decision.IntentRecord) {
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

// cleanup runs the bridge cleanup: the native markers of the decisions the
// registry applied are deleted, as they are needed no longer.
func (f *fixture) cleanup() {
	f.t.Helper()
	if err := f.bridge.Cleanup(f.token); err != nil {
		f.t.Fatalf("cleanup: %v", err)
	}
}

// obligations returns the open delivery obligations of the run.
func (f *fixture) obligations() []decision.Obligation {
	f.t.Helper()
	list, err := f.bridge.Obligations(f.token)
	if err != nil {
		f.t.Fatalf("obligations: %v", err)
	}
	return list
}

// published returns the published event set of the registry: the evidence
// the E02-03 oracle folds.
func (f *fixture) published() []registry.PublishedEvent {
	events, err := f.registry.Events(&network)
	if err != nil {
		f.t.Fatal(err)
	}
	return events
}

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

// head derives the fixed model head of a seed.
func head(number uint64, seed string) decision.Head {
	h := sum64(seed)
	return decision.Head{Number: number, Hash: h}
}

// mustBatch returns the allocated batch of one decision.
func mustBatch(t *testing.T, allocator *semantic.Allocator, sequence uint64) semantic.BatchID {
	t.Helper()
	batch, _, err := allocator.EnsureBatch(sequence)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}
