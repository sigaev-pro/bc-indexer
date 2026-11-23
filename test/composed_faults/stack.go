// Package composed_faults is E05-03.full_crash_matrix: the combined
// native→WAL→Gateway→NATS crash, reorg, finality and restart proof,
// including the ordered cleanup and the recovery GC
// (TASK_DECOMPOSITION E05; DELIVERY_STATE_MACHINE; CONTROL_WIRE_CONTRACT;
// NATIVE_CAPTURE_PROTOCOL REC-T01). Only this matrix composes the native
// decision seam with the whole delivery stack; the delivery-only matrix of
// test/delivery_faults (E03-05) stays a separate, narrower evidence.
//
// The native side is the same serialized decision journal the concurrency
// matrix uses: the durable applied-decision frontier, the decision-to-batch
// allocation of D79 (protocol/semantic) and the semantic manifest each
// decision fixes before DELIVERY_STAGING. The WAL is the producer store,
// the Gateway the gateway journal, NATS the in-memory JetStream of the
// E03-03 publisher. A crash is the memory oracle's cut point — Reopen
// discards exactly the RAM overlays (staged chunks, open sessions) while
// every interface mutation, being one atomic sync commit, survives — which
// is the power-loss variant of the delivery stores; the lifecycle mirror of
// the restart is folded through the E02-03 automaton (test/lifecycle_
// validator), the independent semantic oracle.
//
// The engine is COMMON; ETH/BSC/POLY are separate applicability identities
// of the same mechanism, and every test runs each slot: a PASS of one
// network never closes the others.
package composed_faults

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	derrors "github.com/sigaev-pro/bc-indexer/core/delivery/errors"
	"github.com/sigaev-pro/bc-indexer/core/delivery/release"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// networkSlot is one applicability slot: its own persistent identities,
// destination binding, stream and network identity. The slots share the
// delivery mechanism, never a writable store.
type networkSlot struct {
	name     string
	producer proto.ID16
	run      proto.ID16
	epoch    proto.ID16
	node     proto.ID16
	writer   proto.ID16
	dest     proto.ID16
	stream   string
	prefix   string
	account  string
	chainID  uint64
	genesis  byte
}

// slots lists the three applicability slots of the composed matrix.
var slots = []networkSlot{
	{name: "ETH", producer: proto.ID16{0xa1}, run: proto.ID16{0xa2}, epoch: proto.ID16{0xa3},
		node: proto.ID16{0xa4}, writer: proto.ID16{0xa5}, dest: proto.ID16{0xa6},
		stream: "EXTRACTOR_ETH_V3", prefix: "evm.v3.eth", account: "IDX_ETH",
		chainID: 1, genesis: 0xd1},
	{name: "BSC", producer: proto.ID16{0xb1}, run: proto.ID16{0xb2}, epoch: proto.ID16{0xb3},
		node: proto.ID16{0xb4}, writer: proto.ID16{0xb5}, dest: proto.ID16{0xb6},
		stream: "EXTRACTOR_BSC_V3", prefix: "evm.v3.bsc", account: "IDX_BSC",
		chainID: 56, genesis: 0xd2},
	{name: "POLY", producer: proto.ID16{0xc1}, run: proto.ID16{0xc2}, epoch: proto.ID16{0xc3},
		node: proto.ID16{0xc4}, writer: proto.ID16{0xc5}, dest: proto.ID16{0xc6},
		stream: "EXTRACTOR_POLY_V3", prefix: "evm.v3.poly", account: "IDX_POLY",
		chainID: 137, genesis: 0xd3},
}

// withIDs copies a slot under new producer and run identities (the fresh
// adapter store after a producer store loss keeps the network identity).
func (s networkSlot) withIDs(producer, run proto.ID16) networkSlot {
	out := s
	out.producer, out.run = producer, run
	return out
}

// decisionSpec fixes one native decision before the run: the decision
// sequence that allocates the batch, the activation the decision touches
// (equal to sequence for an application), the lifecycle kind and revision,
// and the body transactions an applied block publishes. A Removed or
// StatusChanged decision touches every entity of the target application.
// Everything is derived deterministically, so a recovery pass rebuilds the
// identical manifest.
type decisionSpec struct {
	sequence uint64
	target   uint64
	block    proto.Hash32
	kind     semantic.Kind
	revision uint64
	txs      []proto.Hash32
}

// appliedBlock fixes the spec of an applied-block decision with n body
// transactions.
func appliedBlock(sequence uint64, block proto.Hash32, n int) decisionSpec {
	txs := make([]proto.Hash32, n)
	for i := range txs {
		txs[i] = txHash(sequence, i)
	}
	return decisionSpec{sequence: sequence, target: sequence,
		block: block, kind: semantic.KindApplied, revision: 0, txs: txs}
}

// lifecycleOf fixes the spec of a Removed or StatusChanged decision on an
// earlier applying decision: it covers every entity of that application.
func lifecycleOf(sequence, target uint64, block proto.Hash32, kind semantic.Kind, revision uint64) decisionSpec {
	return decisionSpec{sequence: sequence, target: target,
		block: block, kind: kind, revision: revision}
}

// blockHash derives the deterministic block hash of one decision.
func blockHash(sequence uint64) proto.Hash32 {
	var h proto.Hash32
	h[0], h[1] = 0xc7, byte(sequence)
	return h
}

// txHash derives the deterministic hash of body transaction i of one
// decision.
func txHash(sequence uint64, i int) proto.Hash32 {
	var h proto.Hash32
	h[0], h[1], h[2] = 0x57, byte(sequence), byte(i)
	return h
}

// semID and semHash convert the proto identity arrays onto the semantic
// package's identical underlying arrays.
func semID(id proto.ID16) semantic.ID16      { return semantic.ID16(id) }
func semHash(h proto.Hash32) semantic.Hash32 { return semantic.Hash32(h) }

// semNet builds the semantic network identity of a slot.
func semNet(s networkSlot) semantic.Network {
	var chain semantic.UInt256
	chain[31] = byte(s.chainID)
	var genesis semantic.Hash32
	genesis[0] = s.genesis
	return semantic.Network{ChainID: chain, GenesisHash: genesis}
}

// decisionJournal is the serialized native-decision seam of one producer
// run: the durable applied-decision frontier, the decision-to-batch
// allocation (D79) and the semantic manifest each decision fixes before
// DELIVERY_STAGING. One mutex models the single serialized writer of the
// real store; the whole marker-then-READY drive of one decision is one
// serialized step.
type decisionJournal struct {
	mu        sync.Mutex
	slot      networkSlot
	allocator *semantic.Allocator
	// batches mirrors the durable decision-to-batch binding (the marker).
	batches map[uint64]uint64
	// specs keeps the admitted decision specs: the mirror a lifecycle
	// decision reads to enumerate the entities of its target application.
	specs map[uint64]decisionSpec
	// head is the highest admitted decision sequence (the native head
	// marker H of the fixture); blockHead is the highest applied block —
	// a finality-only decision advances the decision head but never the
	// block head.
	head      uint64
	blockHead uint64
}

func newJournal(s networkSlot) *decisionJournal {
	allocator, err := semantic.NewAllocator(semID(s.producer), semID(s.run))
	if err != nil {
		panic(err)
	}
	return &decisionJournal{slot: s, allocator: allocator,
		batches: make(map[uint64]uint64), specs: make(map[uint64]decisionSpec)}
}

// descriptorsLocked builds the full ordered descriptor set of a decision:
// the block entity first, then the body transactions in native order (an
// applied block), or the Removed/Status events of every entity of the
// touched application. The derivation is a pure function of the spec and
// the admitted target spec, so a recovery pass rebuilds the identical set.
// It reads the journal's admitted specs and therefore requires the lock.
func (j *decisionJournal) descriptorsLocked(spec decisionSpec) ([]semantic.SemanticDescriptor, error) {
	net := semNet(j.slot)
	producer, run := semID(j.slot.producer), semID(j.slot.run)
	node, writer := semID(j.slot.node), semID(j.slot.writer)
	source := func(ordinal uint64) semantic.SourceRef {
		return semantic.SourceRef{NodeInstanceID: node, WriterEpoch: writer,
			SourceSequence: spec.sequence*1000 + ordinal + 1}
	}
	if spec.kind == semantic.KindApplied {
		if spec.target != spec.sequence {
			return nil, fmt.Errorf("an applied decision targets its own activation")
		}
		out := make([]semantic.SemanticDescriptor, 0, 1+len(spec.txs))
		block, err := semantic.NewBlockDescriptor(spec.kind, net, producer, run,
			spec.sequence, semHash(spec.block), 0, source(0))
		if err != nil {
			return nil, err
		}
		out = append(out, block)
		for i, tx := range spec.txs {
			d, err := semantic.NewTransactionDescriptor(spec.kind, net, producer, run,
				spec.sequence, semHash(spec.block), semHash(tx), uint64(i), 0,
				source(uint64(i)+1))
			if err != nil {
				return nil, err
			}
			out = append(out, d)
		}
		return out, nil
	}
	// A Removed/Status decision touches the entities of an earlier applying
	// decision and publishes no new block body.
	if spec.target == 0 || spec.target >= spec.sequence {
		return nil, fmt.Errorf("lifecycle decision %d targets activation %d",
			spec.sequence, spec.target)
	}
	target, ok := j.specs[spec.target]
	if !ok {
		return nil, fmt.Errorf("lifecycle decision %d targets unadmitted decision %d",
			spec.sequence, spec.target)
	}
	if target.block != spec.block {
		return nil, fmt.Errorf("lifecycle decision %d names block %v, target applied %v",
			spec.sequence, spec.block, target.block)
	}
	out := make([]semantic.SemanticDescriptor, 0, 1+len(target.txs))
	block, err := semantic.NewBlockDescriptor(spec.kind, net, producer, run,
		spec.target, semHash(spec.block), spec.revision, source(0))
	if err != nil {
		return nil, err
	}
	out = append(out, block)
	for i, tx := range target.txs {
		d, err := semantic.NewTransactionDescriptor(spec.kind, net, producer, run,
			spec.target, semHash(spec.block), semHash(tx), uint64(i), spec.revision,
			source(uint64(i)+1))
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// manifestDigestLocked binds the full ordered descriptor set of the
// decision: SHA-256 over the domain tag, one zero byte and each descriptor
// digest in ordinal order. It requires the journal lock.
func (j *decisionJournal) manifestDigestLocked(spec decisionSpec) (proto.Hash32, error) {
	descs, err := j.descriptorsLocked(spec)
	if err != nil {
		return proto.Hash32{}, err
	}
	h := sha256.New()
	h.Write([]byte("composed_faults.manifest.v1"))
	h.Write([]byte{0})
	for _, d := range descs {
		dg, err := d.Digest()
		if err != nil {
			return proto.Hash32{}, err
		}
		h.Write(dg[:])
	}
	var out proto.Hash32
	h.Sum(out[:0])
	return out, nil
}

// markLocked commits the decision marker under the lock: validation first,
// then the allocation and the binding. A repeated marker returns the same
// batch without advancing the counter.
func (j *decisionJournal) markLocked(spec decisionSpec) (uint64, bool, error) {
	if _, err := j.descriptorsLocked(spec); err != nil {
		return 0, false, err
	}
	batch, fresh, err := j.allocator.EnsureBatch(spec.sequence)
	if err != nil {
		return 0, false, err
	}
	_, _, seq := batch.Decode()
	if _, held := j.batches[spec.sequence]; !held {
		j.batches[spec.sequence] = seq
		j.specs[spec.sequence] = spec
		if spec.sequence > j.head {
			j.head = spec.sequence
		}
		if spec.kind == semantic.KindApplied && spec.sequence > j.blockHead {
			j.blockHead = spec.sequence
		}
	}
	return seq, fresh, nil
}

// mark commits the decision marker (the durable native marker with the
// atomic decision-to-batch allocation) and nothing else — the cut point a
// crash between the marker and the adapter mirror uses.
func (j *decisionJournal) mark(spec decisionSpec) (uint64, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.markLocked(spec)
}

// descriptorsOf returns the descriptor set of a decision under the lock;
// the returned slice is fresh, so concurrent callers never share state
// with the serialized writer.
func (j *decisionJournal) descriptorsOf(spec decisionSpec) ([]semantic.SemanticDescriptor, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.descriptorsLocked(spec)
}

// binding returns the batch sequence a decision's marker bound, when it
// has one.
func (j *decisionJournal) binding(sequence uint64) (uint64, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	seq, ok := j.batches[sequence]
	return seq, ok
}

// headOf returns the current admitted decision head.
func (j *decisionJournal) headOf() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.head
}

// blockHeadOf returns the highest applied block: the native chain head H
// of the fixture. A finality-only decision never moves it.
func (j *decisionJournal) blockHeadOf() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.blockHead
}

// pinSet models the recovery-GC pin set of E05-01.activation_gc: an
// application's pins survive until an accepted finality anchor covers it
// or its lifecycle record is terminal, and every batch that carries its
// events is retired. Neither an ACK, a height nor a TTL alone releases a
// pin.
type pinSet struct {
	mu     sync.Mutex
	pins   map[uint64]bool
	anchor uint64
}

func newPinSet() *pinSet { return &pinSet{pins: make(map[uint64]bool)} }

// pin holds the pins of an admitted application.
func (p *pinSet) pin(sequence uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pins[sequence] = true
}

// pinned reports whether an application's pins are still held.
func (p *pinSet) pinned(sequence uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pins[sequence]
}

// acceptAnchor accepts a finality anchor: the highest decision sequence
// whose activations the authoritative finality evidence covers.
func (p *pinSet) acceptAnchor(sequence uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if sequence > p.anchor {
		p.anchor = sequence
	}
}

// sweep releases exactly the pins the GC rule allows: the application's
// batches are all retired and its activation is terminal or below the
// accepted anchor. It returns the released sequences.
func (p *pinSet) sweep(retiredPrefix uint64, batchOf map[uint64]uint64, terminal map[uint64]bool) []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	var released []uint64
	for sequence := range p.pins {
		batch, ok := batchOf[sequence]
		if !ok || batch > retiredPrefix {
			continue // pending delivery alone holds the pin
		}
		if !terminal[sequence] && sequence > p.anchor {
			continue // neither terminal nor anchor-covered
		}
		delete(p.pins, sequence)
		released = append(released, sequence)
	}
	return released
}

// stack is one composed network stack: the decision journal (the native
// seam), the producer store (the WAL, possibly behind the ENOSPC wrapper),
// the gateway journal, the in-memory NATS, the publisher engine and the
// recovery-GC pin set.
type stack struct {
	slot         networkSlot
	journal      *decisionJournal
	mem          *delivery.MemoryProducer
	prod         delivery.ProducerStore
	gw           *delivery.MemoryGateway
	server       *publisher.MemoryJetStream
	engine       *publisher.Engine
	target       destination.VerifiedTarget
	binding      destination.Binding
	digestOfBind proto.Hash32
	pins         *pinSet
}

// slotBinding builds the destination binding of a slot.
func slotBinding(s networkSlot) destination.Binding {
	b := destination.Binding{
		DestinationID: s.dest, Generation: 1,
		Network:         proto.NetworkIdentity{Family: proto.NetworkFamilyEVM},
		ExpectedAccount: s.account, Stream: s.stream, SubjectPrefix: s.prefix,
	}
	b.Network.ChainID[31] = byte(s.chainID)
	b.Network.GenesisHash[0] = s.genesis
	digest, err := b.DescriptorDigestComputed()
	if err != nil {
		panic(err)
	}
	b.DescriptorDigest = digest
	return b
}

// newStack builds a fresh stack of one slot with its own NATS server and
// one ordinary session of admission generation 1 open.
func newStack(s networkSlot) *stack {
	server, err := publisher.NewMemoryJetStream(publisher.StreamConfig{
		Name: s.stream, Subjects: []string{s.prefix + ".>"}, MaxMsgSize: 1 << 20,
	})
	if err != nil {
		panic(err)
	}
	return newStackOn(server, s)
}

// newStackOn builds a stack of one slot over a shared NATS server (the
// common NATS of the deployment: shared infrastructure, never a shared
// journal).
func newStackOn(server *publisher.MemoryJetStream, s networkSlot) *stack {
	binding := slotBinding(s)
	mem, err := delivery.NewMemoryProducer(s.producer, s.run)
	if err != nil {
		panic(err)
	}
	gw, err := delivery.NewMemoryGateway(s.epoch)
	if err != nil {
		panic(err)
	}
	if err := gw.SetActiveBinding(1, binding.DescriptorDigest); err != nil {
		panic(err)
	}
	h := &stack{slot: s, journal: newJournal(s), mem: mem, prod: mem, gw: gw,
		server: server, binding: binding, digestOfBind: binding.DescriptorDigest,
		pins: newPinSet()}
	h.target = h.newTarget()
	if err := gw.OpenSession(h.fence(1)); err != nil {
		panic(err)
	}
	engine, err := publisher.New(server, gw)
	if err != nil {
		panic(err)
	}
	h.engine = engine
	return h
}

// newTarget verifies the destination over a memory remote and returns the
// verified target of the slot's binding.
func (h *stack) newTarget() destination.VerifiedTarget {
	binding := h.binding
	networkDigest, _ := binding.NetworkDigest()
	stream := destination.StreamInfo{
		Name: binding.Stream, Subjects: []string{binding.SubjectPrefix + ".>"},
		Metadata: map[string]string{
			destination.MetadataNetworkDigest: hexOf(networkDigest[:]),
			destination.MetadataDestinationID: hexOf(binding.DestinationID[:]),
		},
		MaxMsgSize: 1 << 20,
	}
	remote, err := destination.NewMemoryRemote(binding.ExpectedAccount, stream)
	if err != nil {
		panic(err)
	}
	subjects := h.routeSubjects()
	report := destination.Check(context.Background(), remote, binding, subjects, 1<<20)
	if !report.OK() {
		panic(fmt.Sprintf("slot %s destination check failed: %+v", h.slot.name, report))
	}
	target, err := destination.NewVerifiedTarget(binding, subjects, 1<<20)
	if err != nil {
		panic(err)
	}
	return target
}

// routeSubjects lists the six fixed routes of the binding.
func (h *stack) routeSubjects() []string {
	var subjects []string
	for _, family := range []proto.Family{proto.FamilyTx, proto.FamilyBlock} {
		for _, kind := range []proto.Kind{proto.KindApplied, proto.KindRemoved, proto.KindStatus} {
			s, err := destination.Subject(h.slot.prefix, family, kind)
			if err != nil {
				panic(err)
			}
			subjects = append(subjects, s)
		}
	}
	return subjects
}

// fence builds the ordinary session fence of the stack at an admission
// generation.
func (h *stack) fence(admission uint64) delivery.Fence {
	return delivery.Fence{SessionToken: []byte("composed-token"),
		Producer: h.slot.producer, Run: h.slot.run,
		ConnectionGeneration: 1, AdmissionGeneration: admission,
		BindingDigest: h.digestOfBind, Kind: delivery.SessionNormal}
}

// sealedIdentity completes the manifest identity with the plan half the
// gateway reservation fixes. The derivation is deterministic per batch
// sequence and gateway epoch, so a recovery pass over a surviving gateway
// rebuilds the identical plan identity.
func (h *stack) sealedIdentity(manifest delivery.BatchIdentity) delivery.BatchIdentity {
	out := manifest
	out.PlanDigest = proto.Hash32{byte(manifest.Batch.Seq), 2}
	out.LayoutManifestDigest = proto.Hash32{byte(manifest.Batch.Seq), 3}
	out.IssuerStoreEpoch = h.gw.StoreEpoch()
	return out
}

// protoFamilyOf maps a semantic family onto the wire registry value.
func protoFamilyOf(f semantic.Family) proto.Family {
	switch f {
	case semantic.FamilyBlock:
		return proto.FamilyBlock
	case semantic.FamilyBalanceSystem:
		return proto.FamilyBalanceSystem
	case semantic.FamilyBorLegacy:
		return proto.FamilyBorLegacy
	default:
		return proto.FamilyTx
	}
}

// protoKindOf maps a semantic kind onto the wire registry value.
func protoKindOf(k semantic.Kind) proto.Kind {
	switch k {
	case semantic.KindRemoved:
		return proto.KindRemoved
	case semantic.KindStatus:
		return proto.KindStatus
	default:
		return proto.KindApplied
	}
}

// frames builds one COMPLETE frame per descriptor of the decision in
// ordinal order; every retry publishes identical bytes.
func (h *stack) frames(spec decisionSpec) ([]publisher.Frame, error) {
	descs, err := h.journal.descriptorsOf(spec)
	if err != nil {
		return nil, err
	}
	networkDigest, err := h.binding.NetworkDigest()
	if err != nil {
		return nil, err
	}
	frames := make([]publisher.Frame, 0, len(descs))
	for i, d := range descs {
		eventID := proto.NewEventID(h.slot.producer, h.slot.run, spec.sequence*100+uint64(i))
		msgID, err := proto.NatsMsgID(networkDigest, eventID, 0, proto.FormComplete, 0)
		if err != nil {
			return nil, err
		}
		header, err := proto.NatsHeader(msgID, h.slot.stream)
		if err != nil {
			return nil, err
		}
		subject, err := destination.Subject(h.slot.prefix, protoFamilyOf(d.Family), protoKindOf(d.Kind))
		if err != nil {
			return nil, err
		}
		dg, err := d.Digest()
		if err != nil {
			return nil, err
		}
		frames = append(frames, publisher.Frame{
			Key:     delivery.FrameKey{Ordinal: uint64(i), Fragment: 0, Complete: true},
			Subject: subject, MessageID: msgID, Header: header,
			Payload: append([]byte(fmt.Sprintf("faults-%d-%d|", spec.sequence, i)), dg[:]...),
		})
	}
	return frames, nil
}

// keysOf extracts the accounting keys of frames in order.
func keysOf(frames []publisher.Frame) []delivery.FrameKey {
	keys := make([]delivery.FrameKey, len(frames))
	for i := range frames {
		keys[i] = frames[i].Key
	}
	return keys
}

// applyDecision drives one native decision to durable READY as one
// serialized step: the marker (validation, allocation, binding), the
// staging chunks and the closing READY commit. A repeated marker replays
// the same batch and identity; the closing commit is idempotent for the
// same manifest and an immutable conflict for a different one. When the
// decision's batch already completed its release, the applied-decision
// frontier answers the former binding and no second application exists.
func (h *stack) applyDecision(spec decisionSpec) (seq uint64, id delivery.BatchIdentity, fresh bool, err error) {
	h.journal.mu.Lock()
	defer h.journal.mu.Unlock()
	if _, err := h.journal.descriptorsLocked(spec); err != nil {
		return 0, delivery.BatchIdentity{}, false, err
	}
	bound, held := h.journal.batches[spec.sequence]
	if held {
		seq = bound
	} else {
		seq, fresh, err = h.journal.markLocked(spec)
		if err != nil {
			return 0, delivery.BatchIdentity{}, false, err
		}
	}
	manifest, err := h.journal.manifestDigestLocked(spec)
	if err != nil {
		return 0, delivery.BatchIdentity{}, false, err
	}
	id = delivery.BatchIdentity{
		Batch:          delivery.BatchID{Producer: h.slot.producer, Run: h.slot.run, Seq: seq},
		ManifestDigest: manifest, BindingDigest: h.digestOfBind,
	}
	rec, ok := h.prod.Batch(seq)
	if !ok {
		if seq <= h.prod.RetiredPrefix() {
			return seq, id, false, nil // the batch is retired; the frontier answers
		}
		allocated, err := h.prod.AllocateIntent()
		if err != nil {
			return 0, delivery.BatchIdentity{}, false, err
		}
		if allocated != seq {
			return 0, delivery.BatchIdentity{}, false, fmt.Errorf(
				"producer allocated sequence %d, the marker bound %d", allocated, seq)
		}
		rec, ok = h.prod.Batch(seq)
		if !ok {
			return 0, delivery.BatchIdentity{}, false, fmt.Errorf("intent of sequence %d vanished", seq)
		}
	}
	if rec.State == delivery.StateIntent || rec.State == delivery.StateStaging {
		if err := h.stageChunk(seq, fmt.Sprintf("decision-%d-a", spec.sequence)); err != nil {
			return 0, delivery.BatchIdentity{}, false, err
		}
		if err := h.stageChunk(seq, fmt.Sprintf("decision-%d-b", spec.sequence)); err != nil {
			return 0, delivery.BatchIdentity{}, false, err
		}
	}
	switch rec.State {
	case delivery.StateIntent, delivery.StateStaging, delivery.StateReady:
		if err := h.prod.CommitReady(seq, id); err != nil {
			return 0, delivery.BatchIdentity{}, false, err
		}
	}
	if spec.kind == semantic.KindApplied {
		h.pins.pin(spec.sequence) // the application's pins are held from admission
	}
	return seq, id, fresh, nil
}

// intentOnly drives the marker and the atomic intent and stops — the first
// durable step a crash can follow.
func (h *stack) intentOnly(t testingT, spec decisionSpec) uint64 {
	t.Helper()
	seq, _, err := h.journal.mark(spec)
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	allocated, err := h.prod.AllocateIntent()
	if err != nil {
		t.Fatalf("AllocateIntent: %v", err)
	}
	if allocated != seq {
		t.Fatalf("allocated %d, marker bound %d", allocated, seq)
	}
	if spec.kind == semantic.KindApplied {
		h.pins.pin(spec.sequence)
	}
	return seq
}

// stageOnly drives the marker, the intent and one staging chunk and stops.
func (h *stack) stageOnly(t testingT, spec decisionSpec) uint64 {
	t.Helper()
	seq := h.intentOnly(t, spec)
	if err := h.stageChunk(seq, fmt.Sprintf("decision-%d-a", spec.sequence)); err != nil {
		t.Fatalf("StageChunk: %v", err)
	}
	return seq
}

// manifestIdentityOf returns the manifest identity of a decision at its
// bound batch sequence.
func (h *stack) manifestIdentityOf(t testingT, spec decisionSpec) (uint64, delivery.BatchIdentity) {
	t.Helper()
	seq, ok := h.journal.binding(spec.sequence)
	if !ok {
		t.Fatalf("decision %d has no binding", spec.sequence)
	}
	h.journal.mu.Lock()
	manifest, err := h.journal.manifestDigestLocked(spec)
	h.journal.mu.Unlock()
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return seq, delivery.BatchIdentity{
		Batch:          delivery.BatchID{Producer: h.slot.producer, Run: h.slot.run, Seq: seq},
		ManifestDigest: manifest, BindingDigest: h.digestOfBind,
	}
}

// driveToRetired takes one decision's batch from whatever durable state a
// crash left it in to a retired batch: the idempotent marker/READY replay,
// the seal, the CLOSED registration, the publish to NATS_ACKED (already
// acked frames are skipped), the producer cleanup and the ordered release
// of both sides. Every step is safe to repeat; the loop re-reads the
// durable record after each commit, exactly as a recovering process does.
func (h *stack) driveToRetired(t testingT, spec decisionSpec) delivery.BatchIdentity {
	t.Helper()
	f := h.fence(1)
	seq, manifest, _, err := h.applyDecision(spec)
	if err != nil {
		t.Fatalf("applyDecision: %v", err)
	}
	frames, err := h.frames(spec)
	if err != nil {
		t.Fatalf("frames: %v", err)
	}
	id := h.sealedIdentity(manifest)
	// A gateway batch past the release barrier resumes its bounded cleanup
	// instead of publishing again.
	if gwRec, ok := h.gw.Batch(f, id.Batch); ok && gwRec.State == delivery.StateReleasing {
		releaseAck, err := release.Release(h.gw, f, id.Batch, 100)
		if err != nil {
			t.Fatalf("resume release: %v", err)
		}
		if err := release.OnReleaseAck(h.prod, releaseAck); err != nil {
			t.Fatalf("OnReleaseAck: %v", err)
		}
		return id
	}
	for {
		rec, ok := h.prod.Batch(seq)
		if !ok {
			return id // the batch completed its release
		}
		switch rec.State {
		case delivery.StateReady:
			if err := h.prod.CommitSealed(seq,
				release.NewSealedPlan(id, 1000+seq, h.slot.stream, keysOf(frames))); err != nil {
				t.Fatalf("CommitSealed: %v", err)
			}
		case delivery.StateSealed:
			if err := h.gw.ImportClosed(f,
				release.NewSealedPlan(id, 1000+seq, h.slot.stream, keysOf(frames)), false); err != nil {
				t.Fatalf("ImportClosed: %v", err)
			}
			ack, err := h.engine.Publish(f, h.target, publisher.Batch{Identity: id, Frames: frames})
			if err != nil {
				t.Fatalf("Publish: %v", err)
			}
			if err := release.OnIngestAck(h.prod, seq, ack); err != nil {
				t.Fatalf("OnIngestAck: %v", err)
			}
		case delivery.StateAckedCleanup:
			if rec.HasPayload {
				if remains, err := release.CleanupPayload(h.prod, seq, 100); err != nil || remains {
					t.Fatalf("cleanup: remains=%v err=%v", remains, err)
				}
			}
			releaseAck, err := release.Release(h.gw, f, id.Batch, 100)
			if err != nil {
				t.Fatalf("Release: %v", err)
			}
			if err := release.OnReleaseAck(h.prod, releaseAck); err != nil {
				t.Fatalf("OnReleaseAck: %v", err)
			}
		default:
			t.Fatalf("the replay left batch %d in %s", seq, rec.State)
		}
	}
}

// crash is the memory oracle's cut point: both processes restart on their
// surviving stores, the RAM overlays (staged chunks, open sessions) are
// lost, and the producer re-opens its session.
func (h *stack) crash() {
	h.prod = h.mem // the ENOSPC wrapper, if any, belonged to the dead process
	h.mem.Reopen()
	h.gw.Reopen()
	if err := h.gw.OpenSession(h.fence(1)); err != nil {
		panic(err)
	}
}

// withNewRun returns a stack over the same gateway, NATS server, publisher
// engine and verified target but a fresh producer store and journal under
// new identities — the day-one handshake after a producer store loss. The
// new run's session replaces the old one on the shared gateway.
func (h *stack) withNewRun(producer, run proto.ID16) *stack {
	slot := h.slot.withIDs(producer, run)
	mem, err := delivery.NewMemoryProducer(producer, run)
	if err != nil {
		panic(err)
	}
	out := &stack{slot: slot, journal: newJournal(slot), mem: mem, prod: mem,
		gw: h.gw, server: h.server, engine: h.engine, target: h.target,
		binding: h.binding, digestOfBind: h.digestOfBind, pins: newPinSet()}
	if err := h.gw.OpenSession(out.fence(1)); err != nil {
		panic(err)
	}
	return out
}

// messagesByID returns the slot's stored NATS messages keyed by message id.
func (h *stack) messagesByID() map[string]publisher.MemoryMessage {
	out := make(map[string]publisher.MemoryMessage)
	for _, m := range h.server.Messages() {
		out[m.MsgID] = m
	}
	return out
}

// expectedMessages builds the expected NATS content of decisions: message
// id to payload, one entry per frame.
func (h *stack) expectedMessages(specs ...decisionSpec) (map[string][]byte, error) {
	out := make(map[string][]byte)
	for _, spec := range specs {
		frames, err := h.frames(spec)
		if err != nil {
			return nil, err
		}
		for _, frame := range frames {
			out[frame.MessageID] = frame.Payload
		}
	}
	return out, nil
}

// assertNATSEquals verifies the stored NATS content of the stack's stream
// against the expected decisions: exactly their messages, byte for byte,
// nothing more.
func (h *stack) assertNATSEquals(t testingT, specs ...decisionSpec) {
	t.Helper()
	expected, err := h.expectedMessages(specs...)
	if err != nil {
		t.Fatalf("expected content: %v", err)
	}
	stored := h.messagesByID()
	for _, m := range stored {
		if m.Stream != h.slot.stream {
			continue
		}
		want, ok := expected[m.MsgID]
		if !ok {
			t.Fatalf("NATS holds unexpected message %s of stream %s", m.MsgID, m.Stream)
		}
		if string(m.Payload) != string(want) {
			t.Fatalf("message %s stored with foreign bytes", m.MsgID)
		}
	}
	for id := range expected {
		if _, ok := stored[id]; !ok {
			t.Fatalf("NATS misses message %s", id)
		}
	}
}

// stagingStore is the producer seam the fault oracle drives besides the
// durable interface: the staged RAM chunks of the chunk transport, which
// the memory double (and its ENOSPC wrapper) expose for the crash cut
// points.
type stagingStore interface {
	delivery.ProducerStore
	StageChunk(seq uint64, chunk string) error
}

// stageChunk stages one RAM chunk through the producer seam.
func (h *stack) stageChunk(seq uint64, chunk string) error {
	store, ok := h.prod.(stagingStore)
	if !ok {
		return fmt.Errorf("the producer store exposes no staging seam")
	}
	return store.StageChunk(seq, chunk)
}

// enospcProducer wraps the memory producer with an ENOSPC fault: while a
// method is armed, its writes fail with the delivery ENOSPC sentinel and
// nothing else changes — no record is deleted, repaired or dropped. The
// wrapper models a full WAL volume; expanding the volume disarms the
// faults, and a compatible build restarts over the same store.
type enospcProducer struct {
	*delivery.MemoryProducer

	mu          sync.Mutex
	failStaging bool
	failReady   bool
	failCleanup bool
	// refused counts the writes ENOSPC refused.
	refused int
}

// newEnospcProducer wraps a memory producer with every fault disarmed.
func newEnospcProducer(mem *delivery.MemoryProducer) *enospcProducer {
	return &enospcProducer{MemoryProducer: mem}
}

// arm arms one fault variant: the staging data write, the closing READY
// service commit or the bounded cleanup service commit.
func (e *enospcProducer) arm(staging, ready, cleanup bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failStaging, e.failReady, e.failCleanup = staging, ready, cleanup
}

// refusedWrites returns the number of refused writes.
func (e *enospcProducer) refusedWrites() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refused
}

// refuse records one refused write.
func (e *enospcProducer) refuse(armed bool) error {
	if !armed {
		return nil
	}
	e.mu.Lock()
	e.refused++
	e.mu.Unlock()
	return derrors.ErrNoSpaceLeft
}

// StageChunk refuses the data write while the staging fault is armed.
func (e *enospcProducer) StageChunk(seq uint64, chunk string) error {
	e.mu.Lock()
	armed := e.failStaging
	e.mu.Unlock()
	if err := e.refuse(armed); err != nil {
		return err
	}
	return e.MemoryProducer.StageChunk(seq, chunk)
}

// CommitReady refuses the service commit while the ready fault is armed.
func (e *enospcProducer) CommitReady(seq uint64, identity delivery.BatchIdentity) error {
	e.mu.Lock()
	armed := e.failReady
	e.mu.Unlock()
	if err := e.refuse(armed); err != nil {
		return err
	}
	return e.MemoryProducer.CommitReady(seq, identity)
}

// CleanupPayload refuses the service commit while the cleanup fault is
// armed.
func (e *enospcProducer) CleanupPayload(seq uint64) (bool, error) {
	e.mu.Lock()
	armed := e.failCleanup
	e.mu.Unlock()
	if err := e.refuse(armed); err != nil {
		return true, err
	}
	return e.MemoryProducer.CleanupPayload(seq)
}

// hexOf encodes bytes as lowercase hex.
func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// testingT is the testing.T subset the stack helpers need.
type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
}
