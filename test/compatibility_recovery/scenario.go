// Package compatibility_recovery is E05-03.upgrade (#57, stage E05): the
// old-build drain / compatible replay / rollback matrix with retained
// lifecycle metadata, and the prewrite rejection of incompatible
// persistent store formats (AC-DLV-14-02; DELIVERY_STATE_MACHINE "Errors,
// ENOSPC, совместимость" and "Drain и cutover"; UPGRADE-01). The matrix
// composes the version policy of protocol/compatibility with a real
// drain driven by the controller of adapters/maintenance over the memory
// stores of core/delivery, so every verdict lands on an actual store
// image, not on an abstract snapshot.
//
// The package uses the Go standard library and the repository-local
// modules; there is no NATS, no RocksDB and no native binary. The
// PubAck accounting of a finished batch is driven straight through the
// store seams, which is the same durable boundary the publisher engine
// drives.
package compatibility_recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"testing"

	native "github.com/sigaev-pro/bc-indexer/adapters/maintenance"
	"github.com/sigaev-pro/bc-indexer/core/delivery"
	dmaintenance "github.com/sigaev-pro/bc-indexer/core/delivery/maintenance"
	"github.com/sigaev-pro/bc-indexer/core/delivery/release"
	"github.com/sigaev-pro/bc-indexer/protocol/compatibility"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// wireKinds pairs each pinned wire/plan version of the v2 contract with
// the hypothetical successor a future build writes while claiming to
// read the pinned one. The matrix runs the same verdicts for every
// preflight kind of the wire/plan family.
var wireKinds = []struct {
	pinned    compatibility.VersionID
	successor compatibility.VersionID
}{
	{compatibility.TransportV2.ID(), "transport/3"},
	{compatibility.RecordFormatV1.ID(), "record_format/2"},
	{compatibility.LayoutV2.ID(), "layout/3"},
	{compatibility.HeaderV2.ID(), "header/3"},
	{compatibility.ControlV2.ID(), "control/3"},
	{compatibility.ManifestV2.ID(), "manifest/3"},
}

// The persistent store format versions of the matrix: no concrete
// version is registered by protocol/compatibility (the store format is
// owned outside the wire contract), so the matrix declares its own pair
// the way the store owner would.
const (
	storeV1 compatibility.VersionID = "extractor-store/1"
	storeV2 compatibility.VersionID = "extractor-store/2"
)

// build is one extractor build: the preflight versions it declares — its
// current wire/plan version with the earlier versions it can read and
// replay, and its persistent store format — wired into one policy per
// family. A build answers every version decision through its policies;
// it holds no store of its own.
type build struct {
	name     string
	wire     compatibility.VersionID
	store    compatibility.VersionID
	wirePol  *compatibility.Policy
	storePol *compatibility.Policy
}

// mustVersion builds a concrete version or panics on programmer error.
func mustVersion(id compatibility.VersionID, reads []compatibility.VersionID, writes bool) compatibility.ProtocolVersion {
	v, err := compatibility.NewProtocolVersion(id, reads, writes)
	if err != nil {
		panic(err)
	}
	return v
}

// newBuild wires the policies of one build. reads are the earlier wire
// versions the build claims to read and replay; readOnly are wire
// versions registered without any claim, the way an old build ships with
// a newer version it can diagnose but neither reads nor writes;
// knownStores are the persistent store formats registered for read-only
// diagnosis, which is how the store-format rejections name the pair
// instead of an unknown version.
func newBuild(name string, wire, store compatibility.VersionID, reads, readOnly, knownStores []compatibility.VersionID) *build {
	wireM, err := compatibility.NewMatrix(mustVersion(wire, reads, true))
	if err != nil {
		panic(err)
	}
	for _, old := range reads {
		if err := wireM.Register(mustVersion(old, nil, false)); err != nil {
			panic(err)
		}
	}
	for _, known := range readOnly {
		if err := wireM.Register(mustVersion(known, nil, false)); err != nil {
			panic(err)
		}
	}
	storeM, err := compatibility.NewMatrix(mustVersion(store, nil, true))
	if err != nil {
		panic(err)
	}
	for _, other := range knownStores {
		if other == store {
			continue
		}
		if err := storeM.Register(mustVersion(other, nil, false)); err != nil {
			panic(err)
		}
	}
	return &build{name: name, wire: wire, store: store,
		wirePol: compatibility.NewPolicy(wireM), storePol: compatibility.NewPolicy(storeM)}
}

// buildsOf returns the old and new build of one wire kind: the old build
// writes the pinned version and the first store format; the new build
// writes the successor and the second store format while claiming to
// read the pinned version. Both register the other's store format for
// read-only diagnosis.
func buildsOf(pinned, successor compatibility.VersionID) (*build, *build) {
	oldB := newBuild("old", pinned, storeV1, nil, nil, []compatibility.VersionID{storeV2})
	newB := newBuild("new", successor, storeV2, []compatibility.VersionID{pinned}, nil,
		[]compatibility.VersionID{storeV1})
	return oldB, newB
}

// verifier is the memory target verifier of the scenario: every digest
// the scenario names verifies.
type verifier struct{ known map[proto.Hash32]bool }

// VerifyTarget answers the D77 check from the memory table.
func (v *verifier) VerifyTarget(_ context.Context, digest proto.Hash32) error {
	if !v.known[digest] {
		return fmt.Errorf("no desired binding for %v", digest)
	}
	return nil
}

// scenario is one durable store image under test: the composed memory
// stores, the drain controller and maintenance engine, the retained
// lifecycle metadata (saved activation references and future-GC pins),
// and the version facts of the pending records. The wire and store
// versions name the format the records were last written under; a build
// that writes changes them only after its preflight passes.
type scenario struct {
	t          *testing.T
	slot       native.ClientSlot
	prod       *delivery.MemoryProducer
	gw         *delivery.MemoryGateway
	engine     *dmaintenance.Engine
	ctrl       *native.Controller
	frontier   *native.MemoryFrontier
	pins       *native.PinLedger
	lifecycles *native.Lifecycles
	fence      delivery.Fence
	producer   proto.ID16
	run        proto.ID16
	binding    proto.Hash32
	stream     string
	wire       compatibility.VersionID
	storeFmt   compatibility.VersionID
	nextSeq    uint64
	pending    map[uint64]compatibility.PendingRecord
}

// newScenario composes a fresh store image of the slot whose records are
// written in the wire and store versions of the given build.
func newScenario(t *testing.T, slot native.ClientSlot, b *build) *scenario {
	t.Helper()
	s := &scenario{t: t, slot: slot, wire: b.wire, storeFmt: b.store,
		frontier: native.NewMemoryFrontier(), pins: native.NewPinLedger(),
		lifecycles: native.NewLifecycles(), nextSeq: 1,
		pending: make(map[uint64]compatibility.PendingRecord)}
	fam := byte(slot.Family)
	s.producer, s.run = proto.ID16{fam, 1}, proto.ID16{fam, 2}
	s.binding = proto.Hash32{fam, 0xaa}
	s.stream = "EXTRACTOR_" + slot.Client
	verify := &verifier{known: map[proto.Hash32]bool{s.binding: true}}
	s.fence = delivery.Fence{SessionToken: []byte("compat-session"), Producer: s.producer, Run: s.run,
		ConnectionGeneration: 1, AdmissionGeneration: 1, BindingDigest: s.binding}

	var err error
	s.prod, err = delivery.NewMemoryProducer(s.producer, s.run)
	if err != nil {
		t.Fatalf("NewMemoryProducer: %v", err)
	}
	s.gw, err = delivery.NewMemoryGateway(proto.ID16{fam, 3})
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := s.gw.SetActiveBinding(1, s.binding); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	if err := s.gw.OpenSession(s.fence); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	s.engine, err = dmaintenance.New(s.prod, s.gw, verify)
	if err != nil {
		t.Fatalf("maintenance engine: %v", err)
	}
	s.ctrl, err = native.Open(slot, s.prod, s.frontier, s.pins)
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	return s
}

// manifestOf builds the manifest half of one batch identity.
func (s *scenario) manifestOf(seq uint64) delivery.BatchIdentity {
	return delivery.BatchIdentity{
		Batch:          delivery.BatchID{Producer: s.producer, Run: s.run, Seq: seq},
		ManifestDigest: proto.Hash32{byte(seq), 1},
		BindingDigest:  s.binding,
	}
}

// sealedOf completes a manifest identity with the plan half the gateway
// reservation fixes.
func (s *scenario) sealedOf(manifest delivery.BatchIdentity) delivery.BatchIdentity {
	id := manifest
	id.PlanDigest = proto.Hash32{byte(manifest.Batch.Seq), 2}
	id.LayoutManifestDigest = proto.Hash32{byte(manifest.Batch.Seq), 3}
	id.IssuerStoreEpoch = s.gw.StoreEpoch()
	return id
}

// planKeys is the fixed two-frame layout of every scenario batch.
func planKeys() []delivery.FrameKey {
	return []delivery.FrameKey{
		{Ordinal: 0, Fragment: 0, Complete: true},
		{Ordinal: 1, Fragment: 0, Complete: true},
	}
}

// writePending writes one new pending record in the scenario's current
// wire version and drives it to the wanted durable delivery state. It
// returns the sequence.
func (s *scenario) writePending(state delivery.ProducerState) uint64 {
	s.t.Helper()
	seq, err := s.prod.AllocateIntent()
	if err != nil {
		s.t.Fatalf("AllocateIntent: %v", err)
	}
	s.nextSeq = seq + 1
	s.pending[seq] = compatibility.PendingRecord{ID: fmt.Sprintf("batch-%d", seq), Version: s.wire}
	if state == delivery.StateIntent {
		return seq
	}
	if err := s.prod.StageChunk(seq, "chunk-1"); err != nil {
		s.t.Fatalf("StageChunk: %v", err)
	}
	if err := s.prod.CommitReady(seq, s.manifestOf(seq)); err != nil {
		s.t.Fatalf("CommitReady: %v", err)
	}
	if state == delivery.StateReady {
		return seq
	}
	if state != delivery.StateSealed {
		s.t.Fatalf("unsupported pending state %s", state)
	}
	id := s.sealedOf(s.manifestOf(seq))
	if err := s.prod.CommitSealed(seq, release.NewSealedPlan(id, 1000+seq, s.stream, planKeys())); err != nil {
		s.t.Fatalf("CommitSealed: %v", err)
	}
	return seq
}

// finishPending drives one pending record to retired: the unfinished
// half is closed first, then the batch is imported (or its sealed plan
// replayed through IMPORT_SEALED), fully acknowledged, cleaned up,
// released and retired. The pending version fact goes with it.
func (s *scenario) finishPending(seq uint64) {
	s.t.Helper()
	if _, ok := s.pending[seq]; !ok {
		s.t.Fatalf("batch %d is not pending", seq)
	}
	rec, ok := s.prod.Batch(seq)
	if !ok {
		s.t.Fatalf("batch %d has no record", seq)
	}
	var id delivery.BatchIdentity
	switch rec.State {
	case delivery.StateIntent, delivery.StateStaging:
		if err := s.prod.CommitReady(seq, s.manifestOf(seq)); err != nil {
			s.t.Fatalf("CommitReady: %v", err)
		}
		fallthrough
	case delivery.StateReady:
		id = s.sealedOf(s.manifestOf(seq))
		if err := s.prod.CommitSealed(seq, release.NewSealedPlan(id, 1000+seq, s.stream, planKeys())); err != nil {
			s.t.Fatalf("CommitSealed: %v", err)
		}
	case delivery.StateSealed:
		id = rec.Identity
	default:
		s.t.Fatalf("batch %d is %s, already past the pending half", seq, rec.State)
	}
	// The exact durable sealed plan replays: IMPORT_SEALED registers the
	// CLOSED batch with the original issuer epoch and frame layout.
	replayed, err := release.ImportSealed(s.prod, s.gw, s.fence, seq)
	if err != nil {
		s.t.Fatalf("ImportSealed: %v", err)
	}
	if !replayed.SameIdentity(id) {
		s.t.Fatalf("the replayed identity %v differs from the sealed %v", replayed, id)
	}
	for _, key := range planKeys() {
		if err := s.gw.RecordPubAck(s.fence, id.Batch, id.PlanDigest, key); err != nil {
			s.t.Fatalf("RecordPubAck: %v", err)
		}
	}
	ack, err := s.gw.MarkNATSACKed(s.fence, id)
	if err != nil {
		s.t.Fatalf("MarkNATSACKed: %v", err)
	}
	if err := release.OnIngestAck(s.prod, seq, ack); err != nil {
		s.t.Fatalf("OnIngestAck: %v", err)
	}
	if remains, err := release.CleanupPayload(s.prod, seq, 10); err != nil || remains {
		s.t.Fatalf("CleanupPayload: remains=%v err=%v", remains, err)
	}
	releaseAck, err := release.Release(s.gw, s.fence, id.Batch, 10)
	if err != nil {
		s.t.Fatalf("Release: %v", err)
	}
	if err := release.OnReleaseAck(s.prod, releaseAck); err != nil {
		s.t.Fatalf("OnReleaseAck: %v", err)
	}
	delete(s.pending, seq)
}

// durable is the policy input of the store image: the wire version of
// its records, the producer mode, the pending count and the highest
// delivery state any pending obligation reached.
func (s *scenario) durable() compatibility.DurableState {
	d := compatibility.DurableState{Version: s.wire, Mode: s.prod.Mode()}
	for seq := uint64(1); seq < s.nextSeq; seq++ {
		if _, pending := s.pending[seq]; !pending {
			continue
		}
		d.Pending++
		rec, ok := s.prod.Batch(seq)
		if !ok {
			s.t.Fatalf("pending batch %d has no record", seq)
		}
		if rec.State > d.MaxDelivery {
			d.MaxDelivery = rec.State
		}
	}
	return d
}

// drain runs the old build's drain of the store image: the startup
// decision, the durable DRAIN entry, the ordered finish of every pending
// obligation, and the seal that fixes the drain point and highwater and
// saves DRAINED.
func (s *scenario) drain(mark byte) delivery.SealAck {
	s.t.Helper()
	action, err := native.Startup(native.FlagDrain, s.prod.Mode())
	if err != nil || action != native.ActionDrain {
		s.t.Fatalf("drain startup: %v %v", action, err)
	}
	if err := s.ctrl.EnterDrain(); err != nil {
		s.t.Fatalf("EnterDrain: %v", err)
	}
	if err := s.ctrl.Admit(native.LiveCallback{Kind: native.CallbackNewBlock,
		Sequence: s.prod.RetiredPrefix() + 1}); err == nil {
		s.t.Fatalf("a live callback was admitted during the drain")
	}
	for _, seq := range s.pendingSequences() {
		s.finishPending(seq)
	}
	if !s.ctrl.Exhausted() {
		s.t.Fatalf("the drain frontier is not exhausted")
	}
	ack, err := s.ctrl.Seal(context.Background(), s.engine, s.fence,
		proto.ID16{byte(s.slot.Family), mark})
	if err != nil {
		s.t.Fatalf("Seal: %v", err)
	}
	if mode := s.prod.Mode(); mode != delivery.ModeDrained {
		s.t.Fatalf("mode is %s after the drain, want DRAINED", mode)
	}
	if ack.Highwater != s.prod.RetiredPrefix() {
		s.t.Fatalf("highwater %d differs from the retired prefix %d", ack.Highwater, s.prod.RetiredPrefix())
	}
	return ack
}

// pendingSequences returns the pending sequences in order.
func (s *scenario) pendingSequences() []uint64 {
	out := make([]uint64, 0, len(s.pending))
	for seq := range s.pending {
		out = append(out, seq)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// fingerprint digests the whole visible store image: both delivery
// stores with every maintenance record, the saved obligations, the
// retained pins and activation references, and the pending version
// facts. A rejected prewrite leaves it unchanged — that is the oracle
// for "nothing was written".
func (s *scenario) fingerprint() string {
	switchID, switchDigest := switchOf(s)
	bindingDigest, bindingGen := bindingOf(s)
	image := fmt.Sprintf("prod=%v/%v mode=%s retired=%d drain=%v seal=%v switch=%v/%v binding=%v/%d resume=%v ack=%v\n",
		s.producer, s.run, s.prod.Mode(), s.prod.RetiredPrefix(), drainPointOf(s), sealOf(s),
		switchID, switchDigest, bindingDigest, bindingGen, resumeOf(s), resumeAckOf(s))
	for seq := uint64(1); seq < s.nextSeq; seq++ {
		rec, ok := s.prod.Batch(seq)
		if ok {
			image += fmt.Sprintf("p%d=%s/%v/%t/%t/%t/%v;", seq, rec.State, rec.Identity,
				rec.HasIdentity, rec.HasPayload, rec.Outbox, rec.Ack)
		}
		plan, ok := s.prod.SealedPlan(seq)
		if ok {
			image += fmt.Sprintf("plan%d=%v/%d/%s/%v;", seq, plan.Identity, plan.SequenceBase,
				plan.Stream, plan.FrameKeys)
		}
		if chunks := s.prod.StagedChunks(seq); len(chunks) > 0 {
			image += fmt.Sprintf("staged%d=%v;", seq, chunks)
		}
		image += fmt.Sprintf("pay%d=%d;", seq, s.prod.PayloadChunkCount(seq))
		if _, pending := s.pending[seq]; pending {
			image += fmt.Sprintf("ver%d=%s;", seq, s.pending[seq].Version)
		}
		gwRec, ok := s.gw.Batch(s.fence, delivery.BatchID{Producer: s.producer, Run: s.run, Seq: seq})
		if ok {
			image += fmt.Sprintf("g%d=%s/%v/%d;", seq, gwRec.State, gwRec.Identity, gwRec.ReleasingCursor)
		}
	}
	activeGen, activeDigest := s.gw.ActiveBinding()
	gwPhase, gwPrepared := gwSwitchOf(s)
	image += fmt.Sprintf("gw=%v/%d/%d/%v epoch=%v switch=%s/%v sealed=%v resumed=%v\n",
		s.binding, s.gw.RetiredPrefix(s.producer, s.run), activeGen, activeDigest, s.gw.StoreEpoch(),
		gwPhase, gwPrepared, gwSealedOf(s), gwResumeOf(s))
	for _, o := range s.frontier.Obligations() {
		image += fmt.Sprintf("ob=%d/%s/%d;", o.Sequence, o.Stage, o.Batch)
	}
	for _, p := range s.pins.Pins() {
		image += fmt.Sprintf("pin=%s/%s;", p.ID, p.Reason)
	}
	for _, id := range s.lifecycles.IDs() {
		a, _ := s.lifecycles.Get(id)
		image += fmt.Sprintf("act=%s/%v/%d;", id, a.Applied.Binding, a.Applied.Batch)
	}
	sum := sha256.Sum256([]byte(image))
	return hex.EncodeToString(sum[:])
}

// The maintenance accessors gathered for the fingerprint.
func drainPointOf(s *scenario) delivery.SealAck {
	id, high, ok := s.prod.DrainPoint()
	if !ok {
		return delivery.SealAck{}
	}
	return delivery.SealAck{DrainID: id, Highwater: high}
}

func sealOf(s *scenario) delivery.SealAck {
	ack, ok := s.prod.SealAckSaved()
	if !ok {
		return delivery.SealAck{}
	}
	return ack
}

func switchOf(s *scenario) (proto.ID16, proto.Hash32) {
	id, digest, _ := s.prod.SwitchIntentSaved()
	return id, digest
}

func bindingOf(s *scenario) (proto.Hash32, uint64) {
	digest, gen, _ := s.prod.CommittedBinding()
	return digest, gen
}

func resumeOf(s *scenario) delivery.ResumeIntent {
	intent, ok := s.prod.ResumeIntentSaved()
	if !ok {
		return delivery.ResumeIntent{}
	}
	return intent
}

func resumeAckOf(s *scenario) delivery.ResumeAck {
	ack, ok := s.prod.ResumeAckSaved()
	if !ok {
		return delivery.ResumeAck{}
	}
	return ack
}

func gwSwitchOf(s *scenario) (delivery.SwitchPhase, delivery.SwitchPrepared) {
	return s.gw.SwitchState()
}

func gwSealedOf(s *scenario) delivery.SealAck {
	ack, ok := s.gw.SealedGeneration()
	if !ok {
		return delivery.SealAck{}
	}
	return ack
}

func gwResumeOf(s *scenario) delivery.ResumeAck {
	ack, ok := s.gw.ResumeState()
	if !ok {
		return delivery.ResumeAck{}
	}
	return ack
}
