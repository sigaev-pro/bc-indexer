// Package eth is the ETH scenario model of E05-02.reorg
// (test/native_lifecycle): it drives the real admission, decision bridge,
// lifecycle gate and finality owner of the assembled extractor module over
// an in-memory native store, mirrors every native decision into the real
// lifecycle registry of E05-01.registry (the activation and revision
// records, the corpus retention and the speculative rules), and observes
// the GC anchor on the real recovery GC engine of E05-01.activation_gc.
// The semantic descriptors of each decision (Applied of the new entities,
// Removed of the replaced activations, StatusChanged of the finality-
// covered ones) are committed as the registry application plans, so the
// published event set the registry exposes is the evidence, and it folds
// through the E02-03 lifecycle oracle. The manifest ledger keeps one batch
// allocation per decision in durable chunks. It proves the reorg, reapply,
// known-branch, reordered and removed-before-PubAck evidence of REQ-CAP-08
// (AC-CAP-08-02) and REQ-LIF-02 (AC-LIF-02-01) for ETH. It is a test
// model, not a production consumer; the design is in ../README.md.
package eth

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/finality"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
	registry "github.com/sigaev-pro/bc-indexer/adapters/lifecycle_registry"
	recoverygc "github.com/sigaev-pro/bc-indexer/adapters/recovery_gc"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// Fixed identities of the producer run and the network.
var (
	producerID = semantic.ID16{1}
	runID      = semantic.ID16{2}
	nodeID     = semantic.ID16{3}
	writerID   = semantic.ID16{4}
	network    = semantic.Network{ChainID: semantic.UInt256{1},
		GenesisHash: blockHash("network-genesis")}
	// storeID is the native store identity of the model.
	storeID = "native-store-1"
	// noneRef is the "none yet" native finality reference.
	noneRef = decision.Head{Hash: strings.Repeat("0", 64)}
)

var (
	// ErrModel rejects a malformed model argument or step.
	ErrModel = errors.New("native lifecycle model error")
	// ErrNotReady reports a manifest that is not READY.
	ErrNotReady = errors.New("manifest is not ready")
)

// Block is one model block: a height, a hash, a parent and one body
// transaction.
type Block struct {
	Number uint64
	Hash   string
	Parent string
	Tx     string
}

// blockHash derives the fixed model hash of a seed: the seed bytes in a
// 32-byte value, so every hash of the model is a valid semantic Hash32.
func blockHash(seed string) semantic.Hash32 {
	var sum semantic.Hash32
	copy(sum[:], []byte(seed))
	return sum
}

// chain is the block tree and the canonical path of the model.
type chain struct {
	parent    map[string]string
	number    map[string]uint64
	tx        map[string]string
	canonical []string // genesis..head, in height order
}

func newChain(genesisHash string) *chain {
	return &chain{parent: map[string]string{}, number: map[string]uint64{genesisHash: 0},
		tx: map[string]string{}, canonical: []string{genesisHash}}
}

func (c *chain) add(b Block) *chain {
	c.parent[b.Hash], c.number[b.Hash], c.tx[b.Hash] = b.Parent, b.Number, b.Tx
	return c
}

// isAncestorOrSelf reports whether ancestor is block or an ancestor of it.
func (c *chain) isAncestorOrSelf(ancestor, block finality.Ref) (bool, error) {
	for {
		if block.Hash == ancestor.Hash {
			return true, nil
		}
		parent, ok := c.parent[block.Hash]
		if !ok {
			return false, nil
		}
		block = finality.Ref{Hash: parent}
	}
}

// fork returns the canonical prefix that survives a switch to newHead: the
// path of the current head up to the common ancestor with newHead.
func (c *chain) fork(newHead string) []string {
	onNew := map[string]bool{}
	for h := newHead; h != ""; h = c.parent[h] {
		onNew[h] = true
	}
	for i := len(c.canonical) - 1; i >= 0; i-- {
		if onNew[c.canonical[i]] {
			return c.canonical[:i+1]
		}
	}
	return c.canonical[:0]
}

// branch returns the blocks of the path fork tip..head in height order,
// excluding the fork tip itself.
func (c *chain) branch(fork []string, head string) []string {
	tip := fork[len(fork)-1]
	var path []string
	for h := head; h != tip; h = c.parent[h] {
		path = append([]string{h}, path...)
	}
	return path
}

// blockOf returns the chain block string whose model hash is hash: every
// block identity of the model is the fixed hash of its string form.
func (c *chain) blockOf(hash semantic.Hash32) string {
	for block := range c.parent {
		if blockHash(block) == hash {
			return block
		}
	}
	return hash.String()
}

// dref returns the native decision reference of one bridge decision
// sequence on the semantic identity model.
func dref(sequence uint64) registry.NativeDecisionRef {
	return registry.NativeDecisionRef{NodeInstanceID: nodeID, ProducerID: producerID, RunID: runID,
		DecisionSequence: sequence}
}

// activationKey returns the activation key of one decision entity of the
// model: one block entity, ordinal 0.
func activationKey(sequence uint64) registry.ActivationKey {
	return registry.ActivationKey{Network: network, ProducerID: producerID, RunID: runID,
		Activation: semantic.ActivationID{DecisionSequence: sequence, EntityOrdinal: 0}}
}

// gcOracle adapts the model chain into the ancestry oracle of the recovery
// GC engine: a lookup failure is unknown evidence, never a guess.
type gcOracle struct{ chain *chain }

func (o gcOracle) Contains(ancestor, descendant recoverygc.AnchorRef) recoverygc.Verdict {
	ok, err := o.chain.isAncestorOrSelf(finality.Ref{Height: ancestor.Height, Hash: ancestor.Hash.String()},
		finality.Ref{Height: descendant.Height, Hash: descendant.Hash.String()})
	if err != nil {
		return recoverygc.VerdictUnknown
	}
	if ok {
		return recoverygc.VerdictContains
	}
	return recoverygc.VerdictDivergent
}

// gcNothingPending, gcReleaser and gcPins are the inert GC seams of the
// model: the reorg evidence releases nothing here.
type gcNothingPending struct{}

func (gcNothingPending) Pending(recoverygc.EntityID) (recoverygc.PendingReport, error) {
	return recoverygc.PendingReport{}, nil
}

type gcReleaser struct{}

func (gcReleaser) ReleaseEntity(recoverygc.EntityID) error { return nil }

type gcPins struct{}

func (gcPins) Release(string) error { return nil }

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

// node is one live producer run of the model: the real admission, decision
// bridge, lifecycle gate and finality owner, the real lifecycle registry
// and recovery GC engine, plus the manifest ledger, the block tree and the
// applied-activation index of the scenarios.
type node struct {
	token    *admission.Token
	db       *memStore
	pins     *fakePins
	inputs   *fakeInputs
	roots    *fakeRoots
	chain    *chain
	adm      *admission.Admission
	bridge   *decision.Bridge
	gate     *lifecycle.Gate
	finality *finality.Tracker
	gc       *recoverygc.Engine
	registry *registry.Registry
	alloc    *semantic.Allocator
	// appliedBy maps each applied block to the decision sequence whose
	// application created its activation; it is scenario bookkeeping over
	// the registry records, not a lifecycle rule.
	appliedBy map[string]uint64
	// source sequences the descriptor source refs of the model.
	source uint64
	// replayed blocks: their corpus was rebuilt by a native targeted
	// replay without a new execution callback.
	replayed map[string]bool
	// fault names the manifest ledger part whose write crashes: a model
	// fault point of AC-CAP-08-02. nil disables it.
	fault string
}

// newNode opens the model over a fresh store with the genesis at head.
func newNode() *node {
	db := newStore()
	genesisHash := blockHash("genesis").String()
	token := admission.NewGuard().Acquire()
	n := &node{token: token, db: db,
		pins:   &fakePins{roots: map[string]string{}, digests: map[string]string{}},
		inputs: &fakeInputs{capsules: map[string]string{}, consumed: map[string]admission.Consumed{}},
		roots:  &fakeRoots{durable: map[string]bool{}},
		chain:  newChain(genesisHash), appliedBy: map[string]uint64{}, replayed: map[string]bool{}}
	n.roots.durable[childRoot] = true
	n.open(db)
	if _, err := n.gate.GoLive(token, decision.Head{Hash: genesisHash}, childRoot, n.roots, n.bridge); err != nil {
		panic(err)
	}
	return n
}

// reopen (re)opens the packages over db, as a restart does.
func (n *node) reopen() {
	n.open(n.db)
}

func (n *node) open(db *memStore) {
	n.db = db
	adm, err := admission.Open(n.token, db, admission.Producer{ProducerID: "producer-1", RunID: "run-1"},
		n.pins, n.inputs)
	if err != nil {
		panic(err)
	}
	reg, _, err := registry.Open(db)
	if err != nil {
		panic(err)
	}
	bridge, err := decision.Open(n.token, db, adm, storeID, n.roots, registryMirror{registry: reg})
	if err != nil {
		panic(err)
	}
	gate, err := lifecycle.OpenMap(n.token, db, adm, storeID, "geth", false, callerMap())
	if err != nil {
		panic(err)
	}
	gc, _, err := recoverygc.Open(db, gcOracle{chain: n.chain}, gcNothingPending{}, gcReleaser{}, gcPins{})
	if err != nil {
		panic(err)
	}
	tracker, err := finality.Open(n.token, db, adm, storeID,
		finality.RunIdentity{Network: network, Node: nodeID, Producer: producerID, Run: runID},
		n.chain.isAncestorOrSelf, gc)
	if err != nil {
		panic(err)
	}
	alloc, err := semantic.NewAllocator(producerID, runID)
	if err != nil {
		panic(err)
	}
	// The producer store rebuilds its batch bindings at startup: every
	// durable allocation of the manifest ledger is adopted, so the batch
	// counter never reuses a sequence after a crash.
	for sequence := uint64(0); sequence < 256; sequence++ {
		data, ok := db.durableString(manifestKey(sequence, "allocation"))
		if !ok {
			continue
		}
		var stored allocationRecord
		if err := json.Unmarshal([]byte(data), &stored); err != nil {
			panic(err)
		}
		raw, err := hex.DecodeString(stored.Batch)
		if err != nil {
			panic(err)
		}
		var batch semantic.BatchID
		copy(batch[:], raw)
		if err := alloc.AdoptBatch(sequence, batch); err != nil {
			panic(err)
		}
	}
	n.adm, n.bridge, n.gate, n.finality, n.gc, n.registry, n.alloc = adm, bridge, gate, tracker, gc, reg, alloc
	// The applied-activation index is rebuilt from the stored applications
	// of the registry: every Applied event names the block and the decision
	// entity its activation fixed.
	n.appliedBy = map[string]uint64{}
	for sequence := uint64(0); ; sequence++ {
		d, ok, err := reg.Decision(dref(sequence))
		if err != nil {
			panic(err)
		}
		if !ok {
			break
		}
		if d.Application == nil {
			continue
		}
		for _, event := range d.Application.Events {
			if event.Descriptor.Kind != semantic.KindApplied {
				continue
			}
			n.appliedBy[n.chain.blockOf(event.Descriptor.Activation.BlockHash)] =
				event.Descriptor.Activation.ActivationID.DecisionSequence
		}
	}
}

// callerMap is the lifecycle map of the model: the geth rows the scenarios
// enter, with the bsc and bor rows the map rules require.
func callerMap() lifecycle.Map {
	m := lifecycle.Map{SchemaVersion: 1, Sources: map[string]string{}}
	caller := func(client, name string, route lifecycle.Route, trigger lifecycle.Trigger,
		effects []lifecycle.Effect, kinds []decision.Kind) lifecycle.Caller {
		c := lifecycle.Caller{ID: client + "." + name, Client: client,
			Source: "core/blockchain.go:" + strings.ReplaceAll(name, "-", ""),
			Entry:  []string{"Entry"}, Effects: effects, Trigger: trigger, Route: route, Kinds: kinds,
			Status: lifecycle.Planned, Owner: "E05-02.reorg"}
		if route == lifecycle.Refuse {
			c.Refusal = "refused by the profile check"
		}
		return c
	}
	for _, client := range lifecycle.Clients {
		m.Sources[client] = strings.Repeat("a", 40)
		m.Callers = append(m.Callers,
			caller(client, "import", lifecycle.Observation, lifecycle.NewBlock,
				[]lifecycle.Effect{lifecycle.Execution, lifecycle.System, lifecycle.Canonical},
				[]decision.Kind{decision.Import, decision.Side, decision.Canonical}),
			caller(client, "insert-without-head", lifecycle.Observation, lifecycle.NewBlock,
				[]lifecycle.Effect{lifecycle.Execution, lifecycle.System}, []decision.Kind{decision.Side}),
			caller(client, "set-canonical", lifecycle.Decision, lifecycle.KnownBranch,
				[]lifecycle.Effect{lifecycle.Canonical}, []decision.Kind{decision.Canonical}),
			caller(client, "set-safe", lifecycle.Decision, lifecycle.NoNewBlock,
				[]lifecycle.Effect{lifecycle.Finality}, []decision.Kind{decision.Finality}),
			caller(client, "set-finalized", lifecycle.Decision, lifecycle.NoNewBlock,
				[]lifecycle.Effect{lifecycle.Finality}, []decision.Kind{decision.Finality}),
			caller(client, "recovery", lifecycle.Observation, lifecycle.Startup,
				[]lifecycle.Effect{lifecycle.Execution}, []decision.Kind{decision.Import}),
			caller(client, "miner", lifecycle.Refuse, lifecycle.Sealing,
				[]lifecycle.Effect{lifecycle.Execution, lifecycle.Canonical}, nil),
			caller(client, "eth-call", lifecycle.ReadOnly, lifecycle.RPC, nil, nil))
	}
	return m
}

var (
	pinDigest   = strings.Repeat("11", 32)
	capsuleHash = strings.Repeat("22", 32)
	execHash    = strings.Repeat("44", 32)
	childRoot   = strings.Repeat("55", 32)
)

// corpus seeds the pins and inputs of an observation of b and runs it to a
// real EXEC_READY record. replay marks a targeted replay of a known block:
// no new native execution runs; the retained inputs rebuild the corpus the
// known branch needs before it becomes canonical.
func (n *node) corpus(b Block, replay bool) admission.ExecReadyRecord {
	candidate := admission.Candidate{Number: b.Number, Hash: b.Hash, ParentHash: b.Parent,
		ParentRoot: childRoot}
	obs, err := n.adm.Begin(n.token, candidate)
	if err != nil {
		panic(err)
	}
	n.pins.roots[obs.Key], n.pins.digests[obs.Key] = candidate.ParentRoot, pinDigest
	n.inputs.capsules[obs.Key] = capsuleHash
	if _, err := n.adm.Admit(n.token, obs.Key); err != nil {
		panic(err)
	}
	consumedDigest, err := admission.ConsumedDigest(capsuleHash, 1, capsuleHash)
	if err != nil {
		panic(err)
	}
	n.inputs.consumed[obs.Key] = admission.Consumed{Observation: obs.Key, CapsuleDigest: capsuleHash,
		Count: 1, Chain: capsuleHash, Digest: consumedDigest}
	ready, err := n.adm.ExecReady(n.token, obs.Key, execHash)
	if err != nil {
		panic(err)
	}
	if replay {
		n.replayed[b.Hash] = true
	}
	return ready
}

// observationOf finds the open EXEC_READY observation of a block, if any.
func (n *node) observationOf(block string) (admission.Observation, bool) {
	observations, err := n.adm.Observations(n.token)
	if err != nil {
		panic(err)
	}
	for _, obs := range observations {
		if obs.Candidate.Hash == block && obs.State == admission.ExecReady {
			return obs, true
		}
	}
	return admission.Observation{}, false
}

// marker writes the native decision marker of an intent in its final
// authoritative batch.
func (n *node) marker(record decision.IntentRecord) {
	value, err := decision.MarkerValue(record)
	if err != nil {
		panic(err)
	}
	if err := n.db.Put(decision.MarkerKey(admission.Producer{ProducerID: "producer-1", RunID: "run-1"},
		record.Sequence), value); err != nil {
		panic(err)
	}
	if err := n.db.SyncKeyValue(); err != nil {
		panic(err)
	}
}

// head returns the current canonical head of the model.
func (n *node) head() decision.Head {
	h := n.chain.canonical[len(n.chain.canonical)-1]
	return decision.Head{Number: n.chain.number[h], Hash: h}
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
// under it: the durable evidence of a completed execution, speculative
// until a canonical decision applies the block.
func (n *node) mirrorIntent(record decision.IntentRecord, corpusDigests map[string]string) {
	ref := dref(record.Sequence)
	if err := n.registry.Intend(ref, network, registryKind(record.Kind), record.Digest); err != nil {
		panic(err)
	}
	for block, digest := range corpusDigests {
		if err := n.registry.CaptureCorpus(ref, blockHash(block), digest); err != nil {
			panic(err)
		}
	}
}

// settle mirrors the settled NATIVE_DECIDED marker of a bridge decision
// into the registry.
func (n *node) settle(record decision.IntentRecord) {
	delivery := decision.Batch
	if record.Kind == decision.Side {
		delivery = decision.Checkpoint
	}
	digest, err := decision.DecidedDigest(record.Digest, delivery)
	if err != nil {
		panic(err)
	}
	if err := n.registry.SettleDecided(dref(record.Sequence), digest); err != nil {
		panic(err)
	}
}

// sourceRef returns the next source provenance of the model descriptors.
func (n *node) sourceRef() semantic.SourceRef {
	n.source++
	return semantic.SourceRef{NodeInstanceID: nodeID, WriterEpoch: writerID, SourceSequence: n.source}
}

// planOf derives the application plan of a decided canonical decision from
// the registry records: the Removed events of the replaced activations
// with revisions from the activation frontier, then the Applied events of
// the new entities. A rebuild of the same decision reuses the application
// the registry already stored, so a repeated marker derives the same
// events, revisions and batch bindings.
func (n *node) planOf(record decision.IntentRecord, replaced []string) registry.ApplicationPlan {
	if d, ok, err := n.registry.Decision(dref(record.Sequence)); err != nil {
		panic(err)
	} else if ok && d.Application != nil && (d.State == registry.StateDecided ||
		d.State == registry.StateApplied) && len(d.Application.Events) != 0 {
		return *d.Application
	}
	var events []registry.EventPlan
	if record.Kind == decision.Canonical {
		var blocks []string
		for _, block := range replaced {
			if _, ok := n.appliedBy[block]; ok {
				blocks = append(blocks, block)
			}
		}
		sort.Slice(blocks, func(i, j int) bool {
			return n.appliedBy[blocks[i]] < n.appliedBy[blocks[j]]
		})
		for _, block := range blocks {
			key := activationKey(n.appliedBy[block])
			revision, err := n.registry.NextRevision(key)
			if err != nil {
				panic(err)
			}
			events = append(events, n.event(semantic.KindRemoved, key.Activation.DecisionSequence,
				blockHash(block), revision))
		}
	}
	for _, member := range record.Members {
		block := blockHash(member.Block)
		// The block entity, ordinal 0, then its one body transaction,
		// ordinal 1: both Applied at revision 0 under this decision.
		events = append(events, n.event(semantic.KindApplied, record.Sequence, block, 0))
		tx, err := semantic.NewTransactionDescriptor(semantic.KindApplied, network, producerID, runID,
			record.Sequence, block, blockHash(n.chain.tx[member.Block]), 0, 0, n.sourceRef())
		if err != nil {
			panic(err)
		}
		txBlock, err := tx.Digest()
		if err != nil {
			panic(err)
		}
		events = append(events, registry.EventPlan{Descriptor: tx, Content: txBlock.String()})
	}
	return registry.ApplicationPlan{Events: events}
}

// event derives one public block-entity event of the model: the semantic
// descriptor and the content digest the manifest ledger pins for its
// bytes.
func (n *node) event(kind semantic.Kind, sequence uint64, block semantic.Hash32,
	revision uint64) registry.EventPlan {
	descriptor, err := semantic.NewBlockDescriptor(kind, network, producerID, runID, sequence, block,
		revision, n.sourceRef())
	if err != nil {
		panic(err)
	}
	digest, err := descriptor.Digest()
	if err != nil {
		panic(err)
	}
	return registry.EventPlan{Descriptor: descriptor, Content: digest.String()}
}

// apply commits the application plan of a decided decision to the registry
// and records the applied-activation index of its member blocks.
func (n *node) apply(record decision.IntentRecord, plan registry.ApplicationPlan) registry.ApplyResult {
	result, err := n.registry.Apply(dref(record.Sequence), plan)
	if err != nil {
		panic(err)
	}
	for _, member := range record.Members {
		n.appliedBy[member.Block] = record.Sequence
	}
	return result
}

// cleanup runs the bridge cleanup: the native markers of the decisions the
// registry applied are deleted, as they are needed no longer.
func (n *node) cleanup() {
	if err := n.bridge.Cleanup(n.token); err != nil {
		panic(err)
	}
}

// importBlocks runs the canonical import of a chain of new blocks: one
// IMPORT decision per block, each with its manifest.
func (n *node) importBlocks(blocks ...Block) []*Manifest {
	var manifests []*Manifest
	for _, b := range blocks {
		n.chain.add(b)
		ready := n.corpus(b, false)
		oldHead := n.head()
		pass, err := n.gate.Enter(n.token, "geth.import")
		if err != nil {
			panic(err)
		}
		record, err := pass.Intend(n.bridge, decision.Intent{Kind: decision.Import, OldHead: oldHead,
			NewHead:        decision.Head{Number: b.Number, Hash: b.Hash},
			Members:        []decision.Member{{Observation: ready.Observation, Block: b.Hash, ExecReadyDigest: ready.Digest, ChildRoot: childRoot}},
			EvidenceDigest: intentEvidence(decision.Import, b.Hash), Events: true})
		if err != nil {
			panic(err)
		}
		n.marker(record)
		n.mirrorIntent(record, map[string]string{b.Hash: ready.Digest})
		if _, err := n.bridge.Resolve(n.token, record.Sequence); err != nil {
			panic(err)
		}
		n.settle(record)
		if err := pass.Exit(); err != nil {
			panic(err)
		}
		n.chain.canonical = append(n.chain.canonical, b.Hash)
		manifest, err := n.buildManifest(record, nil)
		if err != nil {
			panic(err)
		}
		manifests = append(manifests, manifest)
	}
	n.cleanup()
	return manifests
}

// sideImport stores a known side block without a head change: one SIDE
// decision retains its corpus and observation for a later known-branch
// switch; it allocates no batch, publishes no event and closes as a
// checkpoint in the registry.
func (n *node) sideImport(b Block) decision.IntentRecord {
	n.chain.add(b)
	ready := n.corpus(b, false)
	head := n.head()
	pass, err := n.gate.Enter(n.token, "geth.insert-without-head")
	if err != nil {
		panic(err)
	}
	record, err := pass.Intend(n.bridge, decision.Intent{Kind: decision.Side, OldHead: head, NewHead: head,
		Members:        []decision.Member{{Observation: ready.Observation, Block: b.Hash, ExecReadyDigest: ready.Digest, ChildRoot: childRoot}},
		EvidenceDigest: intentEvidence(decision.Side, b.Hash), Events: false})
	if err != nil {
		panic(err)
	}
	n.marker(record)
	n.mirrorIntent(record, map[string]string{b.Hash: ready.Digest})
	if _, err := n.bridge.Resolve(n.token, record.Sequence); err != nil {
		panic(err)
	}
	n.settle(record)
	if err := pass.Exit(); err != nil {
		panic(err)
	}
	n.cleanup()
	return record
}

// switchCanonical reorganizes onto a known branch without new execution.
// The corpus of every branch block that has no open observation is rebuilt
// by a native targeted replay of its retained inputs; the registry corpus
// record the earlier decision captured is reused, so the application marks
// the activation speculative. Each block application is one CANONICAL
// decision of its own; the first decision that leaves the old canonical
// path is the reorg decision: one native decision whose manifest replaces
// several blocks, with the complete Removed set of the replaced
// activations in its one batch. It returns the manifests of the walk in
// order.
func (n *node) switchCanonical(newHead string) []*Manifest {
	fork := n.chain.fork(newHead)
	branch := n.chain.branch(fork, newHead)
	replaced := append([]string{}, n.chain.canonical[len(fork):]...)
	var manifests []*Manifest
	for index, block := range branch {
		var member decision.Member
		if obs, ok := n.observationOf(block); ok {
			member = decision.Member{Observation: obs.Key, Block: block, ExecReadyDigest: obs.ExecReady.Digest,
				ChildRoot: childRoot}
		} else {
			ready := n.corpus(Block{Number: n.chain.number[block], Hash: block,
				Parent: n.chain.parent[block], Tx: n.chain.tx[block]}, true)
			member = decision.Member{Observation: ready.Observation, Block: block,
				ExecReadyDigest: ready.Digest, ChildRoot: childRoot}
		}
		oldHead := n.head()
		pass, err := n.gate.Enter(n.token, "geth.set-canonical")
		if err != nil {
			panic(err)
		}
		record, err := pass.Intend(n.bridge, decision.Intent{Kind: decision.Canonical, OldHead: oldHead,
			NewHead:        decision.Head{Number: n.chain.number[block], Hash: block},
			Members:        []decision.Member{member},
			EvidenceDigest: intentEvidence(decision.Canonical, block), Events: true})
		if err != nil {
			panic(err)
		}
		n.marker(record)
		n.mirrorIntent(record, nil)
		if _, err := n.bridge.Resolve(n.token, record.Sequence); err != nil {
			panic(err)
		}
		n.settle(record)
		if err := pass.Exit(); err != nil {
			panic(err)
		}
		// The Removed group is derived before the canonical path moves past
		// the replaced blocks; only the first decision of the walk leaves
		// the old path, so only it replaces activations.
		firstReplaced := append([]string{}, replaced...)
		replaced = nil
		manifest, err := n.buildManifest(record, firstReplaced)
		if err != nil {
			panic(err)
		}
		n.chain.canonical = append(append([]string{}, fork...), branch[:index+1]...)
		manifests = append(manifests, manifest)
	}
	n.cleanup()
	return manifests
}

// intentEvidence is the decision evidence digest of the model: the kind
// and the decided block hash.
func intentEvidence(kind decision.Kind, block string) string {
	sum := []byte(kind)
	sum = append(sum, []byte(block)...)
	hexed := fmt.Sprintf("%x", sum)
	return (hexed + strings.Repeat("0", 64))[:64]
}

// Manifest is the durable manifest of one decision with public events:
// exactly one batch, the full ordered set of semantic descriptors, and the
// READY header written last.
type Manifest struct {
	Decision    uint64
	Batch       semantic.BatchID
	Descriptors []semantic.SemanticDescriptor
	Ready       bool
}

// manifestKey is the store prefix of the manifest ledger of one decision.
func manifestKey(sequence uint64, part string) string {
	return fmt.Sprintf("extractor-native-lifecycle-manifest-v1/%d/%s", sequence, part)
}

type allocationRecord struct {
	Decision uint64 `json:"decision"`
	Batch    string `json:"batch"`
}

type chunkRecord struct {
	Index       uint64   `json:"index"`
	Kind        string   `json:"kind"`
	Descriptors []string `json:"descriptors"`
}

type headerRecord struct {
	Decision uint64 `json:"decision"`
	Batch    string `json:"batch"`
	Count    int    `json:"count"`
}

// buildManifest builds the manifest of a decided intent in durable chunks
// and commits its application to the registry: the batch allocation first
// (exactly one per decision, restored by AdoptBatch on a rebuild), then
// one chunk per descriptor group, then the READY header. The events are
// the registry application plan of the decision, so the manifest and the
// registry commit the same full ordered set. A rebuild from the same
// native marker restores the same batch and the same set; a manifest
// without its header is not READY. replaced names the blocks whose applied
// activations the canonical change replaces.
func (n *node) buildManifest(record decision.IntentRecord, replaced []string) (*Manifest, error) {
	crash := func(part string) error {
		if n.fault != "" && n.fault == part {
			return errCrash
		}
		return nil
	}
	manifest := &Manifest{Decision: record.Sequence}
	if data, ok := n.db.durableString(manifestKey(record.Sequence, "allocation")); ok {
		var stored allocationRecord
		if err := json.Unmarshal([]byte(data), &stored); err != nil {
			return nil, fmt.Errorf("%w: allocation of %d: %v", ErrModel, record.Sequence, err)
		}
		raw, err := hex.DecodeString(stored.Batch)
		if err != nil {
			return nil, fmt.Errorf("%w: allocation batch: %v", ErrModel, err)
		}
		var batch semantic.BatchID
		copy(batch[:], raw)
		if err := n.alloc.AdoptBatch(record.Sequence, batch); err != nil {
			return nil, err
		}
		manifest.Batch = batch
	} else {
		batch, _, err := n.alloc.EnsureBatch(record.Sequence)
		if err != nil {
			return nil, err
		}
		manifest.Batch = batch
		if err := crash("allocation"); err != nil {
			return nil, err
		}
		n.db.putString(manifestKey(record.Sequence, "allocation"),
			string(mustJSON(allocationRecord{Decision: record.Sequence, Batch: batch.String()})))
		if err := n.db.SyncKeyValue(); err != nil {
			return nil, err
		}
	}
	plan := n.planOf(record, replaced)
	for _, event := range plan.Events {
		manifest.Descriptors = append(manifest.Descriptors, event.Descriptor)
	}
	groups := descriptorGroups(plan.Events)
	position := 0
	for _, group := range groups {
		encoded := make([]string, len(group))
		for i, descriptor := range group {
			digest, err := descriptor.Digest()
			if err != nil {
				return nil, err
			}
			encoded[i] = digest.String()
		}
		part := fmt.Sprintf("chunk/%d", position)
		if err := crash(part); err != nil {
			return nil, err
		}
		n.db.putString(manifestKey(record.Sequence, part),
			string(mustJSON(chunkRecord{Index: uint64(position), Kind: group[0].Kind.String(),
				Descriptors: encoded})))
		position++
	}
	if err := n.db.SyncKeyValue(); err != nil {
		return nil, err
	}
	// The READY header, written last: a partial manifest is not READY. The
	// registry application follows it, so a partial manifest applied
	// nothing.
	if err := crash("header"); err != nil {
		return nil, err
	}
	n.db.putString(manifestKey(record.Sequence, "header"),
		string(mustJSON(headerRecord{Decision: record.Sequence, Batch: manifest.Batch.String(),
			Count: len(manifest.Descriptors)})))
	if err := n.db.SyncKeyValue(); err != nil {
		return nil, err
	}
	n.apply(record, plan)
	manifest.Ready = true
	return manifest, nil
}

// descriptorGroups splits the ordered event set of an application into the
// descriptor groups of the manifest chunks: one Removed group, then one
// Applied group per new entity in native order (the block entity and its
// body transactions).
func descriptorGroups(events []registry.EventPlan) [][]semantic.SemanticDescriptor {
	var groups [][]semantic.SemanticDescriptor
	var removed, applied []semantic.SemanticDescriptor
	for _, event := range events {
		switch event.Descriptor.Kind {
		case semantic.KindRemoved:
			removed = append(removed, event.Descriptor)
		case semantic.KindApplied, semantic.KindStatus:
			applied = append(applied, event.Descriptor)
		}
	}
	if len(removed) != 0 {
		groups = append(groups, removed)
	}
	for i := 0; i < len(applied); i += 2 {
		group := applied[i:]
		if len(group) > 2 {
			group = group[:2]
		}
		groups = append(groups, append([]semantic.SemanticDescriptor{}, group...))
	}
	return groups
}

// isReady reports whether the durable manifest of a decision holds its
// READY header.
func (n *node) isReady(sequence uint64) bool {
	_, ok := n.db.durableString(manifestKey(sequence, "header"))
	return ok
}

// setFinalized drives one live finalized setter call through the finality
// owner, mirrors the FINALITY decision into the registry and applies its
// StatusChanged events: one revision from the same activation counter for
// every active activation the new evidence covers, no new activation.
func (n *node) setFinalized(block string) {
	head := n.head()
	pass, err := n.gate.Enter(n.token, "geth.set-finalized")
	if err != nil {
		panic(err)
	}
	change := lifecycle.FinalityChange{Field: lifecycle.Finalized, Caller: "geth.set-finalized",
		Old: noneRef, New: decision.Head{Number: n.chain.number[block], Hash: block}}
	plan, err := n.finality.Set(n.token, pass, n.bridge, head, change)
	if err != nil {
		panic(err)
	}
	if !plan.Noop {
		if err := n.finality.Commit(n.token, plan); err != nil {
			panic(err)
		}
		if _, err := n.finality.Resolve(n.token, n.bridge, plan.Intent.Sequence); err != nil {
			panic(err)
		}
	}
	if err := pass.Exit(); err != nil {
		panic(err)
	}
	if plan.Noop {
		return
	}
	n.mirrorIntent(plan.Intent, nil)
	n.settle(plan.Intent)
	// The StatusChanged events of the coverage the new evidence
	// establishes, committed through the registry under the same FINALITY
	// decision: the applied activations are visited by decision sequence,
	// so the derived events are deterministic.
	var covered []uint64
	for _, sequence := range n.appliedSequences() {
		key := activationKey(sequence)
		record, ok, err := n.registry.Activation(key)
		if err != nil || !ok || record.State != registry.ActivationActive {
			continue
		}
		status, err := n.finality.StatusOf(finality.Ref{Hash: n.chain.blockOf(record.BlockHash)})
		if err != nil || status != finality.StatusFinalized {
			continue
		}
		covered = append(covered, sequence)
	}
	var events []registry.EventPlan
	for _, sequence := range covered {
		key := activationKey(sequence)
		record, _, err := n.registry.Activation(key)
		if err != nil {
			panic(err)
		}
		revision, err := n.registry.NextRevision(key)
		if err != nil {
			panic(err)
		}
		events = append(events, n.event(semantic.KindStatus, sequence, record.BlockHash, revision))
	}
	n.apply(plan.Intent, registry.ApplicationPlan{Events: events})
	n.cleanup()
}

// appliedSequences lists the decision sequences of the applied block
// activations of the model in a deterministic order.
func (n *node) appliedSequences() []uint64 {
	out := make([]uint64, 0, len(n.appliedBy))
	for _, sequence := range n.appliedBy {
		out = append(out, sequence)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// acceptAnchor accepts the GC anchor from the current finalized evidence
// on the recovery GC engine.
func (n *node) acceptAnchor() (recoverygc.AcceptedGcAnchor, error) {
	return n.finality.Accept(n.token)
}

// published returns the published event set of the registry: the evidence
// the E02-03 oracle folds.
func (n *node) published() []registry.PublishedEvent {
	events, err := n.registry.Events(&network)
	if err != nil {
		panic(err)
	}
	return events
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

// DeliveryKind is the publication state of a manifest for the PubAck
// variants of AC-LIF-02-01.
type DeliveryKind string

// Delivery states of the model.
const (
	// DeliveryUnpublished: the manifest is READY but nothing was published.
	DeliveryUnpublished DeliveryKind = "UNPUBLISHED"
	// DeliveryOnePart: one part of the batch was published; the PubAck of
	// the full batch did not happen.
	DeliveryOnePart DeliveryKind = "ONE_PART_PUBLISHED"
	// DeliveryAcked: the PubAck of every part of the batch happened.
	DeliveryAcked DeliveryKind = "PUB_ACKED"
)

// delivery tracks the publication state of every manifest. Publication
// never feeds back into the native lifecycle: Removed is created for a
// READY Applied regardless of this state, and the Applied delivery is not
// guessed from it.
type delivery struct {
	state map[uint64]DeliveryKind
}

func newDelivery() *delivery { return &delivery{state: map[uint64]DeliveryKind{}} }

func (d *delivery) publish(sequence uint64, state DeliveryKind) { d.state[sequence] = state }

func (d *delivery) of(sequence uint64) DeliveryKind {
	if state, ok := d.state[sequence]; ok {
		return state
	}
	return DeliveryUnpublished
}
