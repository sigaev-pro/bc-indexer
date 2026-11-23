// Package eth is the ETH scenario model of E05-02.reorg
// (test/native_lifecycle): it drives the real admission, decision bridge,
// lifecycle gate and finality owner of the assembled extractor module over
// an in-memory native store, derives the semantic descriptors of each
// native decision (Applied of the new entities, Removed of the replaced
// activations, StatusChanged of the finality-covered ones), builds the
// decision manifest in durable chunks with exactly one batch allocation per
// decision, and folds the delivered descriptors through the E02-03
// lifecycle oracle. It proves the reorg, reapply, known-branch, reordered
// and removed-before-PubAck evidence of REQ-CAP-08 (AC-CAP-08-02) and
// REQ-LIF-02 (AC-LIF-02-01) for ETH. It is a test model, not a production
// consumer; the design is in ../README.md.
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

// activation is one applied activation of the model registry: the entity
// identity, its block and its lifecycle revision counter.
type activation struct {
	id           semantic.ActivationID
	block        string
	nextRevision uint64
	removed      bool
	// removal is the Removed revision the activation received, with the
	// decision that allocated it; a manifest rebuild from the same native
	// marker derives the same revision instead of a second one.
	removal *removal
}

// removal is one allocated Removed revision of an activation.
type removal struct {
	decision uint64
	revision uint64
}

// registry maps the applied activations by block hash and by identity.
type registry struct {
	byBlock map[string]*activation
	byID    map[semantic.ActivationID]*activation
	source  uint64
}

func newRegistry() *registry {
	return &registry{byBlock: map[string]*activation{}, byID: map[semantic.ActivationID]*activation{}}
}

// apply records the Applied activation of the block entity of a decision.
// The same decision replaying the same block is the same activation: a
// manifest rebuild creates no second activation and resets no revision.
func (r *registry) apply(decisionSequence uint64, block string) *activation {
	id := semantic.ActivationID{DecisionSequence: decisionSequence, EntityOrdinal: 0}
	if a, ok := r.byID[id]; ok {
		return a
	}
	a := &activation{id: id, block: block, nextRevision: 1}
	r.byBlock[block] = a
	r.byID[id] = a
	return a
}

// remove allocates the Removed revision of a still-applied activation, or
// replays the one the same decision already allocated.
func (r *registry) remove(a *activation, decisionSequence uint64) (uint64, bool) {
	if a.removal != nil {
		if a.removal.decision == decisionSequence {
			return a.removal.revision, true
		}
		return 0, false
	}
	rev := a.nextRevision
	a.nextRevision++
	a.removed = true
	a.removal = &removal{decision: decisionSequence, revision: rev}
	return rev, true
}

// status allocates the next StatusChanged revision of an activation.
func (r *registry) status(a *activation) uint64 {
	rev := a.nextRevision
	a.nextRevision++
	return rev
}

func (r *registry) sourceRef() semantic.SourceRef {
	r.source++
	return semantic.SourceRef{NodeInstanceID: nodeID, WriterEpoch: writerID, SourceSequence: r.source}
}

// sorted returns the applied activations in a deterministic order.
func (r *registry) sorted() []*activation {
	out := make([]*activation, 0, len(r.byID))
	for _, a := range r.byID {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].id.DecisionSequence != out[j].id.DecisionSequence {
			return out[i].id.DecisionSequence < out[j].id.DecisionSequence
		}
		return out[i].id.EntityOrdinal < out[j].id.EntityOrdinal
	})
	return out
}

// node is one live producer run of the model: the real admission, decision
// bridge, lifecycle gate and finality owner, plus the manifest ledger, the
// activation registry and the block tree.
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
	alloc    *semantic.Allocator
	registry *registry
	// replayed blocks: their corpus was rebuilt by targeted replay
	// without a new execution callback.
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
		chain:  newChain(genesisHash), registry: newRegistry(), replayed: map[string]bool{}}
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
	bridge, err := decision.Open(n.token, db, adm, storeID, n.roots, noMirror{})
	if err != nil {
		panic(err)
	}
	gate, err := lifecycle.OpenMap(n.token, db, adm, storeID, "geth", false, callerMap())
	if err != nil {
		panic(err)
	}
	tracker, err := finality.Open(n.token, db, adm, storeID, n.chain.isAncestorOrSelf)
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
	n.adm, n.bridge, n.gate, n.finality, n.alloc = adm, bridge, gate, tracker, alloc
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
			Members:        []decision.Member{{Observation: ready.Observation, ExecReadyDigest: ready.Digest, ChildRoot: childRoot}},
			EvidenceDigest: intentEvidence(decision.Import, b.Hash), Events: true})
		if err != nil {
			panic(err)
		}
		n.marker(record)
		if _, err := n.bridge.Resolve(n.token, record.Sequence); err != nil {
			panic(err)
		}
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
	return manifests
}

// sideImport stores a known side block without a head change: one SIDE
// decision retains its corpus and observation for a later known-branch
// switch; it allocates no batch and has no public events.
func (n *node) sideImport(b Block) decision.IntentRecord {
	n.chain.add(b)
	ready := n.corpus(b, false)
	head := n.head()
	pass, err := n.gate.Enter(n.token, "geth.insert-without-head")
	if err != nil {
		panic(err)
	}
	record, err := pass.Intend(n.bridge, decision.Intent{Kind: decision.Side, OldHead: head, NewHead: head,
		Members:        []decision.Member{{Observation: ready.Observation, ExecReadyDigest: ready.Digest, ChildRoot: childRoot}},
		EvidenceDigest: intentEvidence(decision.Side, b.Hash), Events: false})
	if err != nil {
		panic(err)
	}
	n.marker(record)
	if _, err := n.bridge.Resolve(n.token, record.Sequence); err != nil {
		panic(err)
	}
	if err := pass.Exit(); err != nil {
		panic(err)
	}
	return record
}

// switchCanonical reorganizes onto a known branch without new execution.
// The corpus of every branch block that has no open observation is rebuilt
// by targeted replay of its retained inputs. Each block application is one
// CANONICAL decision of its own (the contract gives one block entity,
// ordinal 0, to a decision); the first decision that leaves the old
// canonical path is the reorg decision: one native decision whose manifest
// replaces several blocks, with the complete Removed set of the replaced
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
			member = decision.Member{Observation: obs.Key, ExecReadyDigest: obs.ExecReady.Digest,
				ChildRoot: childRoot}
		} else {
			ready := n.corpus(Block{Number: n.chain.number[block], Hash: block,
				Parent: n.chain.parent[block], Tx: n.chain.tx[block]}, true)
			member = decision.Member{Observation: ready.Observation, ExecReadyDigest: ready.Digest,
				ChildRoot: childRoot}
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
		if _, err := n.bridge.Resolve(n.token, record.Sequence); err != nil {
			panic(err)
		}
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

// buildManifest builds the manifest of a decided intent in durable chunks:
// the batch allocation first (exactly one per decision, restored by
// AdoptBatch on a rebuild), then one chunk per descriptor group, then the
// READY header. A rebuild from the same native marker restores the same
// batch and the same full ordered set; a manifest without its header is
// not READY. replaced names the blocks whose applied activations the
// canonical change replaces.
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
	groups := n.descriptorGroups(record, replaced)
	position := 0
	for _, group := range groups {
		encoded := make([]string, len(group))
		for i, descriptor := range group {
			manifest.Descriptors = append(manifest.Descriptors, descriptor)
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
	// The READY header, written last: a partial manifest is not READY.
	if err := crash("header"); err != nil {
		return nil, err
	}
	n.db.putString(manifestKey(record.Sequence, "header"),
		string(mustJSON(headerRecord{Decision: record.Sequence, Batch: manifest.Batch.String(),
			Count: len(manifest.Descriptors)})))
	if err := n.db.SyncKeyValue(); err != nil {
		return nil, err
	}
	manifest.Ready = true
	return manifest, nil
}

// isReady reports whether the durable manifest of a decision holds its
// READY header.
func (n *node) isReady(sequence uint64) bool {
	_, ok := n.db.durableString(manifestKey(sequence, "header"))
	return ok
}

// descriptorGroups derives the full ordered descriptor set of a decision:
// one Removed group for the activations its canonical change replaces,
// then one Applied group per new entity in native order (the block entity,
// then its body transactions).
func (n *node) descriptorGroups(record decision.IntentRecord, replaced []string) [][]semantic.SemanticDescriptor {
	var groups [][]semantic.SemanticDescriptor
	if record.Kind == decision.Canonical {
		var removed []semantic.SemanticDescriptor
		for _, block := range replaced {
			a, ok := n.registry.byBlock[block]
			if !ok {
				continue
			}
			revision, mine := n.registry.remove(a, record.Sequence)
			if !mine {
				continue
			}
			d, err := semantic.NewBlockDescriptor(semantic.KindRemoved, network, producerID, runID,
				a.id.DecisionSequence, blockHash(block), revision, n.registry.sourceRef())
			if err != nil {
				panic(err)
			}
			removed = append(removed, d)
		}
		if len(removed) != 0 {
			groups = append(groups, removed)
		}
	}
	for _, member := range record.Members {
		block := member.Block
		n.registry.apply(record.Sequence, block)
		blockDescriptor, err := semantic.NewBlockDescriptor(semantic.KindApplied, network, producerID,
			runID, record.Sequence, blockHash(block), 0, n.registry.sourceRef())
		if err != nil {
			panic(err)
		}
		tx, err := semantic.NewTransactionDescriptor(semantic.KindApplied, network, producerID, runID,
			record.Sequence, blockHash(block), blockHash(n.chain.tx[block]), 0, 0, n.registry.sourceRef())
		if err != nil {
			panic(err)
		}
		groups = append(groups, []semantic.SemanticDescriptor{blockDescriptor, tx})
	}
	return groups
}

// statusDescriptors derives the StatusChanged descriptors of the applied
// activations that the current finality evidence covers: one revision from
// the same activation counter, no new activation.
func (n *node) statusDescriptors() []semantic.SemanticDescriptor {
	var out []semantic.SemanticDescriptor
	for _, a := range n.registry.sorted() {
		if a.removed {
			continue
		}
		status, err := n.finality.StatusOf(finality.Ref{Hash: a.block})
		if err != nil || status != finality.StatusFinalized {
			continue
		}
		d, err := semantic.NewBlockDescriptor(semantic.KindStatus, network, producerID, runID,
			a.id.DecisionSequence, blockHash(a.block), n.registry.status(a), n.registry.sourceRef())
		if err != nil {
			panic(err)
		}
		out = append(out, d)
	}
	return out
}

// setFinalized drives one live finalized setter call through the finality
// owner.
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
