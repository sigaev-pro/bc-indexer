// Package acceptance is E07, the final product acceptance suite of the
// bc-indexer: the composed verdict of the whole product path — the native
// decision seam, the semantic normalization, the E02 codec, the layout
// planner, the producer WAL, the gateway journal, the publisher engine and
// the NATS boundary — against an independent fixture-derived oracle
// (TASK_DECOMPOSITION E07-02…E07-09; TESTING_CONTRACT "Reader и
// независимая проверка"; IMPLEMENTATION_GATES EVID).
//
// The suite composes only merged E01–E06 modules: core/delivery (the
// durable state machine and its memory stores), gateway/destination and
// gateway/publisher (the verified target and the publish engine),
// protocol/codec (records, manifests, layout and the independent
// FragmentReader), protocol/semantic (the decision→batch allocation of
// D79), adapters/maintenance (the drain/resume mode barrier) and the
// E02-03 lifecycle oracle of test/lifecycle_validator. The native client is
// a deterministic serialized decision journal — the same seam shape the
// E05 composed fault matrix proved — and the NATS server is the in-memory
// JetStream double: the acceptance run is portable and offline, and no
// production consumer is created (REQ-SCP-02, REQ-SCP-06).
//
// Every test keeps the acceptance contract honest: the expected records
// are derived from the chain fixture by a separate oracle path, never by
// the normalizer under test; verdicts compare full identity and content
// sets, not message counts; and a PASS of one network slot never closes
// the others.
package acceptance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// networkSlot is one product family applicability slot: its own persistent
// identities, destination binding, stream, account and chain identity. The
// slots share the acceptance NATS server (shared infrastructure) and never
// a writable store.
type networkSlot struct {
	name   string
	client string
	// optionalFamily is the optional-event family the slot exercises:
	// BSC system scope rides FamilyBalanceSystem, Bor legacy scope rides
	// FamilyBorLegacy (REQ-CAP-09: the native scopes stay separate).
	optionalFamily semantic.Family
	producer       proto.ID16
	run            proto.ID16
	epoch          proto.ID16
	node           proto.ID16
	writer         proto.ID16
	dest           proto.ID16
	stream         string
	prefix         string
	account        string
	chainID        uint64
	genesis        byte
}

// networkSlots lists the three family slots. Every acceptance test runs
// each slot: a PASS of one network never closes the others.
var networkSlots = []networkSlot{
	{name: "ETH", client: "geth", optionalFamily: semantic.FamilyBalanceSystem,
		producer: proto.ID16{0xa1}, run: proto.ID16{0xa2}, epoch: proto.ID16{0xa3},
		node: proto.ID16{0xa4}, writer: proto.ID16{0xa5}, dest: proto.ID16{0xa6},
		stream: "EXTRACTOR_ETH_V3", prefix: "evm.v3.eth", account: "IDX_ETH",
		chainID: 1, genesis: 0xd1},
	{name: "BSC", client: "bsc", optionalFamily: semantic.FamilyBalanceSystem,
		producer: proto.ID16{0xb1}, run: proto.ID16{0xb2}, epoch: proto.ID16{0xb3},
		node: proto.ID16{0xb4}, writer: proto.ID16{0xb5}, dest: proto.ID16{0xb6},
		stream: "EXTRACTOR_BSC_V3", prefix: "evm.v3.bsc", account: "IDX_BSC",
		chainID: 56, genesis: 0xd2},
	{name: "POLY", client: "bor", optionalFamily: semantic.FamilyBorLegacy,
		producer: proto.ID16{0xc1}, run: proto.ID16{0xc2}, epoch: proto.ID16{0xc3},
		node: proto.ID16{0xc4}, writer: proto.ID16{0xc5}, dest: proto.ID16{0xc6},
		stream: "EXTRACTOR_POLY_V3", prefix: "evm.v3.poly", account: "IDX_POLY",
		chainID: 137, genesis: 0xd3},
}

// slotByName returns the slot of a family name.
func slotByName(name string) (networkSlot, error) {
	for _, s := range networkSlots {
		if s.name == name {
			return s, nil
		}
	}
	return networkSlot{}, fmt.Errorf("acceptance: unknown network slot %q", name)
}

// The registry type keys of the acceptance records. They fix the wire
// shape of the three record families the fixture emits; field 4 of the
// block record is the optional native header metadata (present only when
// the block carries it — presence, never a synthetic zero).
const (
	typeKeyBlock    uint32 = 9101
	typeKeyTx       uint32 = 9102
	typeKeyOptional uint32 = 9103
)

// acceptanceBudget is transport.max_frame_bytes of the acceptance
// topology: below the ~16 KiB encoded size of one blob-chunk fragment, so
// the large payloads of the fixture split into PART frames while the small
// header/items/seal fragments stay COMPLETE.
const acceptanceBudget = 8 * 1024

// fixtureTx is one body transaction of a fixture block.
type fixtureTx struct {
	hash proto.Hash32
	// payload is the record payload size: every third transaction is large
	// enough to force PART frames on the transport.
	payload int
}

// blockFixture is one block of the native chain fixture. It is the single
// source both the normalizer path and the independent oracle read; the
// oracle derives the expected records from it directly.
type blockFixture struct {
	height   uint64
	hash     proto.Hash32
	txs      []fixtureTx
	balances int
	metadata []byte
}

// fixtureBlock derives the deterministic canonical block of a height with
// the given body size, optional-family event count and empty metadata.
func fixtureBlock(height uint64, txCount, balances int) blockFixture {
	b := blockFixture{height: height, balances: balances}
	b.hash[0], b.hash[1] = 0xc7, byte(height)
	for i := 0; i < txCount; i++ {
		b.txs = append(b.txs, fixtureTx{hash: fixtureTxHash(height, i), payload: txPayloadSize(i)})
	}
	return b
}

// emptyFixtureBlock derives the deterministic empty block of a height: no
// transactions, no optional events, no metadata — the REQ-DAT-10 empty
// block that still publishes.
func emptyFixtureBlock(height uint64) blockFixture {
	b := fixtureBlock(height, 0, 0)
	b.hash[0] = 0xe0
	return b
}

// reorgFixtureBlock derives the deterministic replacement block of a
// height: a different hash and a different body.
func reorgFixtureBlock(height uint64, txCount, balances int) blockFixture {
	b := fixtureBlock(height, txCount, balances)
	b.hash[0] = 0x99
	return b
}

// systemFixtureBlock derives a block whose header carries native system
// metadata (a consensus extra field), exercising the optional presence
// rule of the block record: the field exists only when the source had it.
func systemFixtureBlock(height uint64, txCount int) blockFixture {
	b := fixtureBlock(height, txCount, 0)
	b.hash[0] = 0x55
	b.metadata = []byte(fmt.Sprintf("system-header-%d", height))
	return b
}

// fixtureTxHash derives the deterministic hash of body transaction i of a
// height.
func fixtureTxHash(height uint64, i int) proto.Hash32 {
	var h proto.Hash32
	h[0], h[1], h[2] = 0x57, byte(height), byte(i)
	return h
}

// txPayloadSize fixes the payload size of body transaction i: every third
// transaction carries a 24 KiB blob that splits into PART frames.
func txPayloadSize(i int) int {
	if i%3 == 0 {
		return 24 * 1024
	}
	return 512
}

// fixturePayload derives the deterministic payload bytes of a transaction
// record from its size.
func fixturePayload(size int) []byte {
	value := make([]byte, size)
	for i := range value {
		value[i] = byte(i%251 + 1)
	}
	return value
}

// The named boundary errors of the native source. They are the observable
// product boundary, not test shortcuts: a block outside the live capture
// window produces no data (REQ-SCP-03), and a known height needs an
// explicit reorg decision, never a silent second application.
var (
	// ErrOutsideWindow refuses a block height outside the live capture
	// window of the source: initial-sync history and heights beyond the
	// current window are never exported.
	ErrOutsideWindow = errors.New("acceptance: block outside the live capture window")

	// ErrKnownHeight refuses a second application of an already canonical
	// height: only a reorg decision rewrites it.
	ErrKnownHeight = errors.New("acceptance: height is already canonical")

	// ErrNoActivation refuses a lifecycle decision on a height that has no
	// applied activation.
	ErrNoActivation = errors.New("acceptance: height has no applied activation")
)

// decisionSpec fixes one native decision before the run: its sequence, its
// kind, the revision a lifecycle decision carries, the activation it
// targets (its own sequence for an application) and the block content. A
// Removed or StatusChanged decision touches every entity of the target
// application. Everything is derived deterministically, so a recovery pass
// rebuilds the identical descriptor set and the identical transport bytes.
type decisionSpec struct {
	sequence uint64
	kind     semantic.Kind
	revision uint64
	target   uint64
	block    blockFixture
}

// nativeSource is the serialized native-decision seam of one producer run:
// the durable decision journal with its capture window, the
// decision-to-batch allocation (D79) and the highest admitted decision and
// block heads. One mutex models the single serialized writer of the real
// store. The source survives process crashes: only the adapter processes
// (the producer WAL and the gateway journal) lose their RAM overlays.
type nativeSource struct {
	mu        sync.Mutex
	slot      networkSlot
	allocator *semantic.Allocator
	// windowStart/windowEnd fix the live capture window [start, end]: only
	// new live H+1 blocks inside it are admitted (REQ-SCP-03).
	windowStart uint64
	windowEnd   uint64
	decisions   map[uint64]decisionSpec
	byHeight    map[uint64]uint64
	// nextRevision is the next lifecycle revision per target activation.
	nextRevision map[uint64]uint64
	head         uint64
	blockHead    uint64
}

// newNativeSource builds the source of one slot with a capture window.
func newNativeSource(slot networkSlot, windowStart, windowEnd uint64) *nativeSource {
	allocator, err := semantic.NewAllocator(semantic.ID16(slot.producer), semantic.ID16(slot.run))
	if err != nil {
		panic(err)
	}
	return &nativeSource{slot: slot, allocator: allocator,
		windowStart: windowStart, windowEnd: windowEnd,
		decisions: make(map[uint64]decisionSpec), byHeight: make(map[uint64]uint64),
		nextRevision: make(map[uint64]uint64)}
}

// inWindow reports whether a height lies inside the live capture window.
func (s *nativeSource) inWindow(height uint64) bool {
	return height >= s.windowStart && height <= s.windowEnd
}

// setWindow moves the live capture window forward. A gap between the old
// and the new window is a downtime range the product does not export; the
// boundary is recorded without inventing data for the gap (REQ-SCP-03).
func (s *nativeSource) setWindow(start, end uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if start < s.windowStart || end < start {
		panic("acceptance: the capture window never moves back")
	}
	s.windowStart, s.windowEnd = start, end
}

// windowOf returns the current capture window.
func (s *nativeSource) windowOf() (uint64, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.windowStart, s.windowEnd
}

// nextSequenceLocked allocates the next decision sequence.
func (s *nativeSource) nextSequenceLocked() uint64 {
	s.head++
	return s.head
}

// commitLocked stores an admitted decision and updates the heads.
func (s *nativeSource) commitLocked(d decisionSpec) {
	s.decisions[d.sequence] = d
	if d.kind == semantic.KindApplied && d.block.height > s.blockHead {
		s.blockHead = d.block.height
	}
}

// admitLive offers one new live H+1 block to the source. A height outside
// the capture window is refused with ErrOutsideWindow — no obligation, no
// batch, no public event exists for it. A known height is refused with
// ErrKnownHeight.
func (s *nativeSource) admitLive(b blockFixture) (decisionSpec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.inWindow(b.height) {
		return decisionSpec{}, fmt.Errorf("%w: height %d, window [%d, %d]",
			ErrOutsideWindow, b.height, s.windowStart, s.windowEnd)
	}
	if _, held := s.byHeight[b.height]; held {
		return decisionSpec{}, fmt.Errorf("%w: height %d", ErrKnownHeight, b.height)
	}
	d := decisionSpec{sequence: s.nextSequenceLocked(), kind: semantic.KindApplied,
		target: s.head, block: b}
	d.target = d.sequence
	s.commitLocked(d)
	s.byHeight[b.height] = d.sequence
	return d, nil
}

// reorgAt replaces the canonical block of a height: the source emits the
// Removed decision of the old activation (every entity of the application,
// revision-allocated) and the Applied decision of the replacement block.
// The two decisions are whole: neither is cut at a block boundary
// (REQ-LIF-02, REQ-DLV-02).
func (s *nativeSource) reorgAt(height uint64, replacement blockFixture) ([]decisionSpec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.inWindow(height) || !s.inWindow(replacement.height) {
		return nil, fmt.Errorf("%w: reorg of height %d", ErrOutsideWindow, height)
	}
	if replacement.height != height {
		return nil, fmt.Errorf("acceptance: reorg of height %d names replacement height %d",
			height, replacement.height)
	}
	target, ok := s.byHeight[height]
	if !ok {
		return nil, fmt.Errorf("%w: height %d", ErrNoActivation, height)
	}
	removed := decisionSpec{sequence: s.nextSequenceLocked(), kind: semantic.KindRemoved,
		revision: s.allocateRevisionLocked(target), target: target, block: s.decisions[target].block}
	s.commitLocked(removed)
	applied := decisionSpec{sequence: s.nextSequenceLocked(), kind: semantic.KindApplied,
		target: s.head, block: replacement}
	applied.target = applied.sequence
	s.commitLocked(applied)
	s.byHeight[height] = applied.sequence
	return []decisionSpec{removed, applied}, nil
}

// finalizeAt emits a finality-only StatusChanged decision on the applied
// activation of a height: it advances the decision head, never the block
// head (REQ-LIF-04).
func (s *nativeSource) finalizeAt(height uint64) (decisionSpec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target, ok := s.byHeight[height]
	if !ok {
		return decisionSpec{}, fmt.Errorf("%w: height %d", ErrNoActivation, height)
	}
	d := decisionSpec{sequence: s.nextSequenceLocked(), kind: semantic.KindStatus,
		revision: s.allocateRevisionLocked(target), target: target,
		block: s.decisions[target].block}
	s.commitLocked(d)
	return d, nil
}

// allocateRevisionLocked returns the next lifecycle revision of a target
// activation: Removed and StatusChanged share one allocation counter, so
// revisions are dense and never reused.
func (s *nativeSource) allocateRevisionLocked(target uint64) uint64 {
	s.nextRevision[target]++
	return s.nextRevision[target]
}

// decision returns an admitted decision by sequence.
func (s *nativeSource) decision(sequence uint64) (decisionSpec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.decisions[sequence]
	return d, ok
}

// decided returns all admitted decisions in sequence order — the replay
// order of the recovery path and the oracle walk order.
func (s *nativeSource) decided() []decisionSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]decisionSpec, 0, len(s.decisions))
	for _, d := range s.decisions {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].sequence < out[j].sequence })
	return out
}

// heads returns the admitted decision head and the applied block head.
func (s *nativeSource) heads() (decision, block uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.head, s.blockHead
}

// eventSequence derives the deterministic event-id sequence of one entity
// ordinal of a decision: the identity a recovery pass rebuilds identically
// (REQ-CAP-08).
func eventSequence(d decisionSpec, ordinal uint64) uint64 {
	return d.sequence*1000 + ordinal
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

// protoNet builds the wire network identity of a slot.
func protoNet(s networkSlot) proto.NetworkIdentity {
	n := proto.NetworkIdentity{Family: proto.NetworkFamilyEVM}
	n.ChainID[31] = byte(s.chainID)
	n.GenesisHash[0] = s.genesis
	return n
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

// acceptanceStack is one composed acceptance stack of a slot: the native
// source (the decision seam), the producer store (the WAL), the gateway
// journal, the in-memory NATS server (its own or the shared acceptance
// server) and the verified target. The engine is rebuilt on demand so a
// fault-instrumented connection can replace it.
type acceptanceStack struct {
	slot          networkSlot
	source        *nativeSource
	mem           *delivery.MemoryProducer
	prod          delivery.ProducerStore
	gw            *delivery.MemoryGateway
	server        *publisher.MemoryJetStream
	target        destination.VerifiedTarget
	binding       destination.Binding
	bindingDigest proto.Hash32
	planSequence  uint64
}

// slotBinding builds the destination binding of a slot.
func slotBinding(s networkSlot) destination.Binding {
	b := destination.Binding{
		DestinationID: s.dest, Generation: 1,
		Network:         protoNet(s),
		ExpectedAccount: s.account, Stream: s.stream, SubjectPrefix: s.prefix,
	}
	digest, err := b.DescriptorDigestComputed()
	if err != nil {
		panic(err)
	}
	b.DescriptorDigest = digest
	return b
}

// newStack builds a fresh stack of one slot over a NATS server with its
// own capture window, one ordinary session open and the destination
// verified against the memory remote.
func newStack(s networkSlot, server *publisher.MemoryJetStream, windowStart, windowEnd uint64) *acceptanceStack {
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
	h := &acceptanceStack{slot: s, source: newNativeSource(s, windowStart, windowEnd),
		mem: mem, prod: mem, gw: gw, server: server,
		binding: binding, bindingDigest: binding.DescriptorDigest}
	h.target = h.verifyTarget()
	if err := gw.OpenSession(h.fence(1)); err != nil {
		panic(err)
	}
	return h
}

// verifyTarget checks the destination over a memory remote modeled on the
// server's stream configuration and returns the verified target.
func (h *acceptanceStack) verifyTarget() destination.VerifiedTarget {
	networkDigest, err := h.binding.NetworkDigest()
	if err != nil {
		panic(err)
	}
	info := destination.StreamInfo{
		Name: h.binding.Stream, Subjects: []string{h.binding.SubjectPrefix + ".>"},
		Metadata: map[string]string{
			destination.MetadataNetworkDigest: hexOf(networkDigest[:]),
			destination.MetadataDestinationID: hexOf(h.binding.DestinationID[:]),
		},
		MaxMsgSize: acceptanceBudget,
	}
	remote, err := destination.NewMemoryRemote(h.binding.ExpectedAccount, info)
	if err != nil {
		panic(err)
	}
	subjects := h.routeSubjects()
	report := destination.Check(context.Background(), remote, h.binding, subjects, acceptanceBudget)
	if !report.OK() {
		panic(fmt.Sprintf("slot %s destination check failed: %+v", h.slot.name, report))
	}
	target, err := destination.NewVerifiedTarget(h.binding, subjects, acceptanceBudget)
	if err != nil {
		panic(err)
	}
	return target
}

// routeSubjects lists the fixed routes of the binding: the six tx/block
// routes every slot publishes plus the optional-family routes of the
// slot's native scope (BSC balance_system, Bor legacy_receipt).
func (h *acceptanceStack) routeSubjects() []string {
	families := []proto.Family{proto.FamilyTx, proto.FamilyBlock}
	if optional := protoFamilyOf(h.slot.optionalFamily); optional != proto.FamilyTx {
		families = append(families, optional)
	}
	var subjects []string
	for _, family := range families {
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
func (h *acceptanceStack) fence(admission uint64) delivery.Fence {
	return delivery.Fence{SessionToken: []byte("acceptance-" + h.slot.name),
		Producer: h.slot.producer, Run: h.slot.run,
		ConnectionGeneration: 1, AdmissionGeneration: admission,
		BindingDigest: h.bindingDigest, Kind: delivery.SessionNormal}
}

// engine builds a publish engine over the stack's server and journal.
func (h *acceptanceStack) engine(conn publisher.Conn) *publisher.Engine {
	engine, err := publisher.New(conn, h.gw)
	if err != nil {
		panic(err)
	}
	return engine
}

// crash is the memory oracle's cut point: both adapter processes restart
// on their surviving stores, the RAM overlays (staged chunks, open
// sessions) are lost, and the producer re-opens its session. The native
// source — the durable decision journal — survives unchanged.
func (h *acceptanceStack) crash() {
	h.prod = h.mem
	h.mem.Reopen()
	h.gw.Reopen()
	if err := h.gw.OpenSession(h.fence(1)); err != nil {
		panic(err)
	}
}

// stop models an operator stop of one network's adapter pair: the stack is
// left un-reopened after a crash, so it delivers nothing until an explicit
// restart.
func (h *acceptanceStack) stop() {
	h.prod = h.mem
	h.mem.Reopen()
	h.gw.Reopen()
}

// restart models the operator start of the stopped pair.
func (h *acceptanceStack) restart() {
	if err := h.gw.OpenSession(h.fence(1)); err != nil {
		panic(err)
	}
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
