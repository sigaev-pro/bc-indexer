// Package native_concurrency is E05-04.concurrent_fencing: the race matrix
// of native decisions, old workers, sessions and late PubAck answers
// (TASK_DECOMPOSITION E05; DELIVERY_STATE_MACHINE, "Fencing" and the
// transition table; CONTROL_WIRE_CONTRACT, "Служебные запросы и ответы").
//
// Every race drives the composed E03 stack — the producer store (the WAL),
// the gateway journal, the publisher engine and the in-memory NATS — plus
// the E02 seams the races cross: the decision-to-batch allocation of
// protocol/semantic (one native decision, one batch, idempotent marker
// replays) and the lifecycle automaton of test/lifecycle_validator (the
// Removed/Status ordering oracle). The tests run the racing parties on real
// goroutines and rely on `go test -race` for the interleaving: every
// assertion is an invariant that must hold for all interleavings — no
// second action for one decision, no resurrected released state, the live
// admission generation untouched by late sealed-generation RPCs.
//
// The engine is COMMON; the three network slots ETH/BSC/POLY are separate
// applicability identities of the same mechanism (their own producer, run,
// gateway epoch, binding, stream and subjects), and every test runs each
// slot: a PASS of one network never closes the others.
package native_concurrency

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/core/delivery/maintenance"
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

// slots lists the three applicability slots of the race matrix.
var slots = []networkSlot{
	{name: "ETH", producer: proto.ID16{0x11}, run: proto.ID16{0x12}, epoch: proto.ID16{0x13},
		node: proto.ID16{0x14}, writer: proto.ID16{0x15}, dest: proto.ID16{0x16},
		stream: "EXTRACTOR_ETH_V3", prefix: "evm.v3.eth", account: "IDX_ETH",
		chainID: 1, genesis: 0xe1},
	{name: "BSC", producer: proto.ID16{0x21}, run: proto.ID16{0x22}, epoch: proto.ID16{0x23},
		node: proto.ID16{0x24}, writer: proto.ID16{0x25}, dest: proto.ID16{0x26},
		stream: "EXTRACTOR_BSC_V3", prefix: "evm.v3.bsc", account: "IDX_BSC",
		chainID: 56, genesis: 0xe2},
	{name: "POLY", producer: proto.ID16{0x31}, run: proto.ID16{0x32}, epoch: proto.ID16{0x33},
		node: proto.ID16{0x34}, writer: proto.ID16{0x35}, dest: proto.ID16{0x36},
		stream: "EXTRACTOR_POLY_V3", prefix: "evm.v3.poly", account: "IDX_POLY",
		chainID: 137, genesis: 0xe3},
}

// decisionSpec fixes one native decision before any race runs: the decision
// sequence that allocates the batch, the activation the decision touches
// (the applying decision sequence; equal to sequence for an application),
// the lifecycle kind and revision, and the body transactions an applied
// block publishes. A Removed or StatusChanged decision touches every entity
// of the target application (its block and body transactions). Everything
// is derived deterministically from the spec, so a replay after a crash or
// a marker repeat rebuilds the identical manifest.
type decisionSpec struct {
	sequence uint64
	// target is the decision sequence of the touched activation.
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
// Removed and Status carry a positive revision from the same activation
// counter.
func lifecycleOf(sequence, target uint64, block proto.Hash32, kind semantic.Kind, revision uint64) decisionSpec {
	return decisionSpec{sequence: sequence, target: target,
		block: block, kind: kind, revision: revision}
}

// blockHash derives the deterministic block hash of one decision.
func blockHash(sequence uint64) proto.Hash32 {
	var h proto.Hash32
	h[0], h[1] = 0xb1, byte(sequence)
	return h
}

// txHash derives the deterministic hash of body transaction i of one
// decision.
func txHash(sequence uint64, i int) proto.Hash32 {
	var h proto.Hash32
	h[0], h[1], h[2] = 0x77, byte(sequence), byte(i)
	return h
}

// semID and semHash convert the proto identity arrays onto the semantic
// package's identical underlying arrays.
func semID(id proto.ID16) semantic.ID16      { return semantic.ID16(id) }
func semHash(h proto.Hash32) semantic.Hash32 { return semantic.Hash32(h) }
func semNet(s networkSlot) semantic.Network {
	var chain semantic.UInt256
	chain[31] = byte(s.chainID)
	var genesis semantic.Hash32
	genesis[0] = s.genesis
	return semantic.Network{ChainID: chain, GenesisHash: genesis}
}

// decisionJournal is the serialized native-decision seam of one producer
// run: the durable applied-decision frontier, the decision-to-batch
// allocation (D79: one native decision with public events, exactly one
// batch) and the semantic manifest each decision fixes before
// DELIVERY_STAGING. One mutex models the single serialized writer of the
// real store; the whole marker-then-READY drive of one decision is one
// serialized step, so concurrent replays of one marker restore the same
// binding instead of allocating a second batch.
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
	// marker H of the fixture).
	head uint64
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
// touched application (revision above zero, activation refs of the original
// application, no new block body). The derivation is a pure function of
// the spec and the admitted target spec, so a retry or a recovery pass
// rebuilds the identical set. It reads the journal's admitted specs and
// therefore requires the journal lock.
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

// manifestDigest binds the full ordered descriptor set of the decision:
// SHA-256 over the domain tag, one zero byte and each descriptor digest in
// ordinal order. This is the fixture's semantic manifest — what READY
// fixes and what a retry may never change; the transport manifest v2 of
// the wire belongs to the codec and is not needed to race the state
// machine. It requires the journal lock.
func (j *decisionJournal) manifestDigestLocked(spec decisionSpec) (proto.Hash32, error) {
	descs, err := j.descriptorsLocked(spec)
	if err != nil {
		return proto.Hash32{}, err
	}
	h := sha256.New()
	h.Write([]byte("native_concurrency.manifest.v1"))
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

// descriptorsOf returns the descriptor set of a decision under the journal
// lock; the returned slice is fresh, so concurrent callers never share
// state with the serialized writer.
func (j *decisionJournal) descriptorsOf(spec decisionSpec) ([]semantic.SemanticDescriptor, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.descriptorsLocked(spec)
}

// mark commits the decision marker under the serialized writer: it
// validates the descriptors (validation precedes any allocation), asks the
// allocator for the decision's batch and records the binding. A repeated
// marker returns the same batch without advancing the counter. The
// returned fresh flag reports whether this call allocated.
func (j *decisionJournal) mark(spec decisionSpec) (uint64, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.markLocked(spec)
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

// harness is one composed delivery stack of a slot: the producer store
// (the WAL), the gateway journal, the in-memory NATS, the publisher engine
// and the decision journal.
type harness struct {
	slot         networkSlot
	journal      *decisionJournal
	prod         *delivery.MemoryProducer
	gw           *delivery.MemoryGateway
	server       *publisher.MemoryJetStream
	engine       *publisher.Engine
	target       destination.VerifiedTarget
	binding      destination.Binding
	digestOfBind proto.Hash32
}

// newHarness builds a fresh stack of one slot with one ordinary session of
// admission generation 1 open.
func newHarness(s networkSlot) *harness {
	binding := destination.Binding{
		DestinationID: s.dest, Generation: 1,
		Network:         proto.NetworkIdentity{Family: proto.NetworkFamilyEVM},
		ExpectedAccount: s.account, Stream: s.stream, SubjectPrefix: s.prefix,
	}
	binding.Network.ChainID[31] = byte(s.chainID)
	binding.Network.GenesisHash[0] = s.genesis
	digest, err := binding.DescriptorDigestComputed()
	if err != nil {
		panic(err)
	}
	binding.DescriptorDigest = digest

	prod, err := delivery.NewMemoryProducer(s.producer, s.run)
	if err != nil {
		panic(err)
	}
	gw, err := delivery.NewMemoryGateway(s.epoch)
	if err != nil {
		panic(err)
	}
	if err := gw.SetActiveBinding(1, digest); err != nil {
		panic(err)
	}
	h := &harness{slot: s, journal: newJournal(s), prod: prod, gw: gw,
		server: newServer(s), binding: binding, digestOfBind: digest}
	h.target = h.newTarget()
	if err := gw.OpenSession(h.fence(1)); err != nil {
		panic(err)
	}
	engine, err := publisher.New(h.server, gw)
	if err != nil {
		panic(err)
	}
	h.engine = engine
	return h
}

// newServer builds an in-memory NATS holding the slot's stream.
func newServer(s networkSlot) *publisher.MemoryJetStream {
	server, err := publisher.NewMemoryJetStream(publisher.StreamConfig{
		Name: s.stream, Subjects: []string{s.prefix + ".>"}, MaxMsgSize: 1 << 20,
	})
	if err != nil {
		panic(err)
	}
	return server
}

// newTarget verifies the destination over a memory remote and returns the
// verified target of the slot's binding.
func (h *harness) newTarget() destination.VerifiedTarget {
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
func (h *harness) routeSubjects() []string {
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
// generation. The token bytes are constant across generations on purpose:
// the admission generation alone must fence a late RPC of an old seal.
func (h *harness) fence(admission uint64) delivery.Fence {
	return delivery.Fence{SessionToken: []byte("race-token"),
		Producer: h.slot.producer, Run: h.slot.run,
		ConnectionGeneration: 1, AdmissionGeneration: admission,
		BindingDigest: h.digestOfBind, Kind: delivery.SessionNormal}
}

// manifestIdentity builds the manifest half of the batch identity of a
// decision at its allocated batch sequence: everything READY fixes. The
// batch sequence comes from the allocator's own counter (a decision with a
// higher sequence number may still hold the next batch sequence). It takes
// the journal lock, so callers outside applyDecision stay safe against the
// serialized writer.
func (h *harness) manifestIdentity(spec decisionSpec, seq uint64) (delivery.BatchIdentity, error) {
	h.journal.mu.Lock()
	defer h.journal.mu.Unlock()
	manifest, err := h.journal.manifestDigestLocked(spec)
	if err != nil {
		return delivery.BatchIdentity{}, err
	}
	return delivery.BatchIdentity{
		Batch:          delivery.BatchID{Producer: h.slot.producer, Run: h.slot.run, Seq: seq},
		ManifestDigest: manifest, BindingDigest: h.digestOfBind,
	}, nil
}

// sealedIdentity completes a manifest identity with the plan half the
// gateway reservation fixes.
func (h *harness) sealedIdentity(manifest delivery.BatchIdentity) delivery.BatchIdentity {
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
// ordinal order: the event id, message id, exact header bytes and payload
// are pure functions of the spec, so every retry publishes identical bytes.
func (h *harness) frames(spec decisionSpec) ([]publisher.Frame, error) {
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
			Payload: append([]byte(fmt.Sprintf("race-%d-%d|", spec.sequence, i)), dg[:]...),
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
// staging chunks and the closing READY commit with the manifest identity
// derived from the descriptors. A repeated marker replays the same batch
// and the same identity without a second allocation; the producer's READY
// commit is idempotent for the same manifest.
func (h *harness) applyDecision(spec decisionSpec) (seq uint64, id delivery.BatchIdentity, fresh bool, err error) {
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
			// The batch completed its release: the applied-decision
			// frontier answers the marker with the former binding, and no
			// second application exists.
			return seq, id, false, nil
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
		if err := h.prod.StageChunk(seq, fmt.Sprintf("decision-%d-a", spec.sequence)); err != nil {
			return 0, delivery.BatchIdentity{}, false, err
		}
		if err := h.prod.StageChunk(seq, fmt.Sprintf("decision-%d-b", spec.sequence)); err != nil {
			return 0, delivery.BatchIdentity{}, false, err
		}
	}
	switch rec.State {
	case delivery.StateIntent, delivery.StateStaging, delivery.StateReady:
		// The closing commit is idempotent for the same manifest and an
		// immutable conflict for a different one, so a repeated marker with
		// mutated content cannot pass silently.
		if err := h.prod.CommitReady(seq, id); err != nil {
			return 0, delivery.BatchIdentity{}, false, err
		}
	}
	return seq, id, fresh, nil
}

// markLocked is mark without taking the journal lock (applyDecision holds
// it for the whole serialized step).
func (j *decisionJournal) markLocked(spec decisionSpec) (seq uint64, fresh bool, err error) {
	if _, err := j.descriptorsLocked(spec); err != nil {
		return 0, false, err
	}
	batch, fresh, err := j.allocator.EnsureBatch(spec.sequence)
	if err != nil {
		return 0, false, err
	}
	_, _, seq = batch.Decode()
	if _, held := j.batches[spec.sequence]; !held {
		j.batches[spec.sequence] = seq
		j.specs[spec.sequence] = spec
		if spec.sequence > j.head {
			j.head = spec.sequence
		}
	}
	return seq, fresh, nil
}

// seal closes a READY batch into SEALED with the exact transport plan of
// the current gateway's reservation.
func (h *harness) seal(seq uint64, manifest delivery.BatchIdentity, frames []publisher.Frame) (delivery.BatchIdentity, error) {
	id := h.sealedIdentity(manifest)
	err := h.prod.CommitSealed(seq, release.NewSealedPlan(id, 1000+seq, h.slot.stream, keysOf(frames)))
	if err != nil {
		return delivery.BatchIdentity{}, err
	}
	return id, nil
}

// publish registers a batch as CLOSED and publishes it to durable
// NATS_ACKED, returning the IngestAck.
func (h *harness) publish(fence delivery.Fence, id delivery.BatchIdentity, frames []publisher.Frame) (delivery.IngestAck, error) {
	if err := h.gw.ImportClosed(fence,
		release.NewSealedPlan(id, 1000+id.Batch.Seq, h.slot.stream, keysOf(frames)), false); err != nil {
		return delivery.IngestAck{}, err
	}
	return h.engine.Publish(fence, h.target, publisher.Batch{Identity: id, Frames: frames})
}

// finish drives an acknowledged batch through the producer cleanup and the
// ordered release of both sides.
func (h *harness) finish(fence delivery.Fence, seq uint64, ack delivery.IngestAck) error {
	if err := release.OnIngestAck(h.prod, seq, ack); err != nil {
		return err
	}
	if remains, err := release.CleanupPayload(h.prod, seq, 100); err != nil || remains {
		return fmt.Errorf("cleanup: remains=%v err=%v", remains, err)
	}
	releaseAck, err := release.Release(h.gw, fence, ack.Identity.Batch, 100)
	if err != nil {
		return err
	}
	return release.OnReleaseAck(h.prod, releaseAck)
}

// runDecision drives one applied decision through the whole pipeline to a
// retired batch.
func (h *harness) runDecision(fence delivery.Fence, spec decisionSpec) (delivery.BatchIdentity, error) {
	seq, id, _, err := h.applyDecision(spec)
	if err != nil {
		return delivery.BatchIdentity{}, err
	}
	frames, err := h.frames(spec)
	if err != nil {
		return delivery.BatchIdentity{}, err
	}
	sealed, err := h.seal(seq, id, frames)
	if err != nil {
		return delivery.BatchIdentity{}, err
	}
	ack, err := h.publish(fence, sealed, frames)
	if err != nil {
		return delivery.BatchIdentity{}, err
	}
	if err := h.finish(fence, seq, ack); err != nil {
		return delivery.BatchIdentity{}, err
	}
	return sealed, nil
}

// okVerifier is the maintenance engine's target verifier for paths that
// run no destination switch: SealToDrained and Resume verify no target, so
// the verifier admits the active binding and nothing else.
type okVerifier struct{ active proto.Hash32 }

// VerifyTarget admits exactly the active binding digest.
func (v okVerifier) VerifyTarget(ctx context.Context, digest proto.Hash32) error {
	if digest != v.active {
		return fmt.Errorf("no switch runs in the race matrix")
	}
	return nil
}

// maintenanceStack builds the maintenance engine of the stack.
func (h *harness) maintenanceStack() *maintenance.Engine {
	engine, err := maintenance.New(h.prod, h.gw, okVerifier{active: h.digestOfBind})
	if err != nil {
		panic(err)
	}
	return engine
}

// messagesByID returns the slot's stored NATS messages keyed by message id.
func (h *harness) messagesByID() map[string]publisher.MemoryMessage {
	out := make(map[string]publisher.MemoryMessage)
	for _, m := range h.server.Messages() {
		out[m.MsgID] = m
	}
	return out
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
