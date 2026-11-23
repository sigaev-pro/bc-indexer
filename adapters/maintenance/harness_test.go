// The composed test harness of the drain mode: one producer store, one
// gateway store, a session fence, the delivery maintenance engine over a
// memory target verifier, and the driver that takes one batch from an
// allocated intent to a retired sequence without any NATS dependency —
// the PubAck accounting is driven straight through the store seams, which
// is the same durable boundary the publisher engine drives.
package maintenance

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	dmaintenance "github.com/sigaev-pro/bc-indexer/core/delivery/maintenance"
	"github.com/sigaev-pro/bc-indexer/core/delivery/release"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// errUnknownTarget and errFailedTarget are the two verifier answers the
// switch path must distinguish: an unknown desired digest and a digest
// whose online check fails.
var (
	errUnknownTarget = errors.New("harness: no desired binding for the digest")
	errFailedTarget  = errors.New("harness: target check failed")
)

// verifier is the memory target verifier of the harness: every known
// desired digest verifies, and the harness can fail selected digests to
// prove the switch path re-checks the target.
type verifier struct {
	known map[proto.Hash32]bool
	fail  map[proto.Hash32]bool
}

// VerifyTarget answers the D77 check from the memory table.
func (v *verifier) VerifyTarget(_ context.Context, digest proto.Hash32) error {
	if !v.known[digest] {
		return fmt.Errorf("%w: %v", errUnknownTarget, digest)
	}
	if v.fail[digest] {
		return fmt.Errorf("%w: %v", errFailedTarget, digest)
	}
	return nil
}

// harness is one composed drain stack of one applicability slot.
type harness struct {
	slot     ClientSlot
	producer proto.ID16
	run      proto.ID16
	epoch    proto.ID16
	oldBind  proto.Hash32
	newBind  proto.Hash32
	stream   string
	prod     *delivery.MemoryProducer
	gw       *delivery.MemoryGateway
	verify   *verifier
	engine   *dmaintenance.Engine
	frontier *MemoryFrontier
	pins     *PinLedger
	ctrl     *Controller
	// delivered counts the batches delivered under each binding digest,
	// which is how the cutover test proves the ACKed history never moves
	// to the new destination.
	delivered map[proto.Hash32]int
}

// newHarness builds a fresh stack of the slot with one open session and
// one retired warm-up batch, so every scenario starts from a non-empty
// delivery frontier.
func newHarness(t *testing.T, slot ClientSlot) *harness {
	t.Helper()
	fam := byte(slot.Family)
	h := &harness{
		slot:      slot,
		producer:  proto.ID16{fam, 1},
		run:       proto.ID16{fam, 2},
		epoch:     proto.ID16{fam, 3},
		oldBind:   proto.Hash32{fam, 0xaa},
		newBind:   proto.Hash32{fam, 0xbb},
		stream:    "EXTRACTOR_" + slot.Client,
		verify:    &verifier{known: make(map[proto.Hash32]bool), fail: make(map[proto.Hash32]bool)},
		frontier:  NewMemoryFrontier(),
		pins:      NewPinLedger(),
		delivered: make(map[proto.Hash32]int),
	}
	h.verify.known[h.oldBind] = true
	h.verify.known[h.newBind] = true

	var err error
	h.prod, err = delivery.NewMemoryProducer(h.producer, h.run)
	if err != nil {
		t.Fatalf("NewMemoryProducer: %v", err)
	}
	h.gw, err = delivery.NewMemoryGateway(h.epoch)
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := h.gw.SetActiveBinding(1, h.oldBind); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	if err := h.gw.OpenSession(h.fence(delivery.SessionNormal, 1)); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	h.engine, err = dmaintenance.New(h.prod, h.gw, h.verify)
	if err != nil {
		t.Fatalf("maintenance engine: %v", err)
	}
	h.ctrl, err = Open(slot, h.prod, h.frontier, h.pins)
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	h.runBatch(t, h.fence(delivery.SessionNormal, 1))
	return h
}

// fence builds the session fence of the harness run.
func (h *harness) fence(kind delivery.SessionKind, admission uint64) delivery.Fence {
	return delivery.Fence{SessionToken: []byte("drain-session"), Producer: h.producer, Run: h.run,
		ConnectionGeneration: 1, AdmissionGeneration: admission,
		BindingDigest: h.oldBind, Kind: kind}
}

// dataFence builds a data-session fence for the given binding digest and
// admission generation: after a switch and a resume, new batches run in
// the new binding of the new admission generation.
func (h *harness) dataFence(binding proto.Hash32, admission uint64) delivery.Fence {
	f := h.fence(delivery.SessionNormal, admission)
	f.BindingDigest = binding
	return f
}

// manifestOf builds the manifest half of one batch identity under binding.
func (h *harness) manifestOf(seq uint64, binding proto.Hash32) delivery.BatchIdentity {
	return delivery.BatchIdentity{
		Batch:          delivery.BatchID{Producer: h.producer, Run: h.run, Seq: seq},
		ManifestDigest: proto.Hash32{byte(seq), 1},
		BindingDigest:  binding,
	}
}

// sealedOf completes a manifest identity with the plan half the gateway
// reservation fixes.
func (h *harness) sealedOf(manifest delivery.BatchIdentity) delivery.BatchIdentity {
	id := manifest
	id.PlanDigest = proto.Hash32{byte(manifest.Batch.Seq), 2}
	id.LayoutManifestDigest = proto.Hash32{byte(manifest.Batch.Seq), 3}
	id.IssuerStoreEpoch = h.epoch
	return id
}

// runBatch drives the next batch from an allocated intent to a retired
// sequence under the fence's binding: READY, SEALED, CLOSED, fully
// PubAcked, NATS_ACKed, cleaned up, released and retired. It returns the
// sequence.
func (h *harness) runBatch(t *testing.T, fence delivery.Fence) uint64 {
	t.Helper()
	binding := fence.BindingDigest
	seq, err := h.prod.AllocateIntent()
	if err != nil {
		t.Fatalf("AllocateIntent: %v", err)
	}
	if err := h.prod.StageChunk(seq, "chunk-1"); err != nil {
		t.Fatalf("StageChunk: %v", err)
	}
	manifest := h.manifestOf(seq, binding)
	if err := h.prod.CommitReady(seq, manifest); err != nil {
		t.Fatalf("CommitReady: %v", err)
	}
	keys := []delivery.FrameKey{
		{Ordinal: 0, Fragment: 0, Complete: true},
		{Ordinal: 1, Fragment: 0, Complete: true},
	}
	id := h.sealedOf(manifest)
	plan := release.NewSealedPlan(id, 1000+seq, h.stream, keys)
	if err := h.prod.CommitSealed(seq, plan); err != nil {
		t.Fatalf("CommitSealed: %v", err)
	}
	if err := h.gw.ImportClosed(fence, plan, false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	for _, key := range keys {
		if err := h.gw.RecordPubAck(fence, id.Batch, id.PlanDigest, key); err != nil {
			t.Fatalf("RecordPubAck: %v", err)
		}
	}
	ack, err := h.gw.MarkNATSACKed(fence, id)
	if err != nil {
		t.Fatalf("MarkNATSACKed: %v", err)
	}
	if err := release.OnIngestAck(h.prod, seq, ack); err != nil {
		t.Fatalf("OnIngestAck: %v", err)
	}
	if remains, err := release.CleanupPayload(h.prod, seq, 10); err != nil || remains {
		t.Fatalf("CleanupPayload: remains=%v err=%v", remains, err)
	}
	releaseAck, err := release.Release(h.gw, fence, id.Batch, 10)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := release.OnReleaseAck(h.prod, releaseAck); err != nil {
		t.Fatalf("OnReleaseAck: %v", err)
	}
	if _, ok := h.prod.Batch(seq); ok {
		t.Fatalf("batch %d is not retired", seq)
	}
	h.delivered[binding]++
	return seq
}

// enterDrain starts the drain of the harness: the startup decision, the
// durable DRAIN entry and the admission-closed assertions of the mode.
func (h *harness) enterDrain(t *testing.T) {
	t.Helper()
	action, err := Startup(FlagDrain, h.prod.Mode())
	if err != nil {
		t.Fatalf("Startup: %v", err)
	}
	if action != ActionDrain {
		t.Fatalf("Startup returned %s, want DRAIN", action)
	}
	if err := h.ctrl.EnterDrain(); err != nil {
		t.Fatalf("EnterDrain: %v", err)
	}
	if mode := h.prod.Mode(); mode != delivery.ModeDrain {
		t.Fatalf("mode is %s, want DRAIN", mode)
	}
}

// refuseNewWork asserts the mode barrier: no new admissions, no new polls.
func (h *harness) refuseNewWork(t *testing.T, sequence uint64) {
	t.Helper()
	if err := h.ctrl.Admit(LiveCallback{Kind: CallbackNewBlock, Sequence: sequence}); err == nil {
		t.Fatalf("new-block callback %d was admitted during the drain", sequence)
	} else if !isAdmissionClosed(err) {
		t.Fatalf("admission refusal of %d: %v", sequence, err)
	}
	if err := h.ctrl.Admit(LiveCallback{Kind: CallbackFinality, Sequence: sequence}); err == nil {
		t.Fatalf("finality callback %d was admitted during the drain", sequence)
	}
	for _, tick := range []PollTick{{Kind: PollGC}, {Kind: PollObserver}} {
		if err := h.ctrl.Poll(tick); err == nil {
			t.Fatalf("%s poll started during the drain", tick.Kind)
		} else if !isAdmissionClosed(err) {
			t.Fatalf("poll refusal: %v", err)
		}
	}
}

// isAdmissionClosed reports the sentinel of the closed admission gate.
func isAdmissionClosed(err error) bool { return errors.Is(err, ErrAdmissionClosed) }
