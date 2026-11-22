// Package delivery_faults is E03-05.recovery_matrix: the delivery-only
// fault matrix. From a given durable INTENT/READY producer state it drives
// the full composed path — producer store, gateway store, publisher
// engine, memory NATS, release and maintenance engines — injects one
// precise fault (gateway store loss, late RPC, partial cleanup, NATS
// outage) and asserts the expected durable outcome. The oracle
// distinguishes durable state from RAM bytes through the memory stores'
// staging overlays and Reopen cut points.
//
// The matrix starts from a given durable delivery input. Native
// admission, decision interleavings and a producer total-store-loss are
// E05-03.full_crash_matrix territory, not this one; the combined REC-T01
// proof is not assigned here.
package delivery_faults

import (
	"context"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	derrors "github.com/sigaev-pro/bc-indexer/core/delivery/errors"
	"github.com/sigaev-pro/bc-indexer/core/delivery/maintenance"
	"github.com/sigaev-pro/bc-indexer/core/delivery/release"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// The fixed identities of the matrix.
var (
	fProducer = proto.ID16{1}
	fRun      = proto.ID16{2}
	fEpoch    = proto.ID16{3}
	fStream   = "EXTRACTOR_ETH_V3"
	fPrefix   = "evm.v3.eth"
)

// fBindingDigest computes the descriptor digest of the matrix binding.
var fBindingDigest = func() proto.Hash32 {
	b := fBinding()
	return b.DescriptorDigest
}()

// fBinding builds the matrix binding.
func fBinding() destination.Binding {
	b := destination.Binding{
		DestinationID: proto.ID16{7}, Generation: 1,
		Network:         proto.NetworkIdentity{Family: proto.NetworkFamilyEVM},
		ExpectedAccount: "IDX_ETH", Stream: fStream, SubjectPrefix: fPrefix,
	}
	digest, err := b.DescriptorDigestComputed()
	if err != nil {
		panic(err)
	}
	b.DescriptorDigest = digest
	return b
}

// fFence builds the session fence of the matrix.
func fFence(kind delivery.SessionKind, admission uint64) delivery.Fence {
	return delivery.Fence{SessionToken: []byte("fence-token"), Producer: fProducer, Run: fRun,
		ConnectionGeneration: 1, AdmissionGeneration: admission,
		BindingDigest: fBindingDigest, Kind: kind}
}

// fFrames builds n one-frame-per-ordinal frames of one event family/kind.
func fFrames(t interface {
	Fatalf(format string, args ...any)
}, seq uint64, family proto.Family, kind proto.Kind, n int) []publisher.Frame {
	frames := make([]publisher.Frame, 0, n)
	eventID := proto.NewEventID(fProducer, fRun, 5000+seq)
	subject, err := destination.Subject(fPrefix, family, kind)
	if err != nil {
		t.Fatalf("Subject: %v", err)
	}
	for i := 0; i < n; i++ {
		msgID, err := proto.NatsMsgID(proto.Hash32{5}, eventID, uint64(i), proto.FormComplete, 0)
		if err != nil {
			t.Fatalf("NatsMsgID: %v", err)
		}
		header, err := proto.NatsHeader(msgID, fStream)
		if err != nil {
			t.Fatalf("NatsHeader: %v", err)
		}
		payload := []byte(fmt.Sprintf("faults-frame-%d-%d", seq, i))
		frames = append(frames, publisher.Frame{
			Key:     delivery.FrameKey{Ordinal: uint64(i), Fragment: 0, Complete: true},
			Subject: subject, MessageID: msgID, Header: header, Payload: payload,
		})
	}
	return frames
}

// fKeys extracts the accounting keys of frames in order.
func fKeys(frames []publisher.Frame) []delivery.FrameKey {
	keys := make([]delivery.FrameKey, len(frames))
	for i := range frames {
		keys[i] = frames[i].Key
	}
	return keys
}

// fServer builds the in-memory NATS of the matrix.
func fServer() *publisher.MemoryJetStream {
	server, err := publisher.NewMemoryJetStream(publisher.StreamConfig{
		Name: fStream, Subjects: []string{fPrefix + ".>"}, MaxMsgSize: 1 << 20,
	})
	if err != nil {
		panic(err)
	}
	return server
}

// fTarget builds the verified target of the matrix.
func fTarget() destination.VerifiedTarget {
	binding := fBinding()
	networkDigest, _ := binding.NetworkDigest()
	stream := destination.StreamInfo{
		Name: fStream, Subjects: []string{fPrefix + ".>"},
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
	subjects := []string{}
	for _, family := range []proto.Family{proto.FamilyTx, proto.FamilyBlock} {
		for _, kind := range []proto.Kind{proto.KindApplied, proto.KindRemoved, proto.KindStatus} {
			s, err := destination.Subject(fPrefix, family, kind)
			if err != nil {
				panic(err)
			}
			subjects = append(subjects, s)
		}
	}
	if report := destination.Check(context.Background(), remote, binding, subjects, 1<<20); !report.OK() {
		panic(fmt.Sprintf("matrix destination check failed: %+v", report))
	}
	target, err := destination.NewVerifiedTarget(binding, subjects, 1<<20)
	if err != nil {
		panic(err)
	}
	return target
}

// fHex encodes bytes as lowercase hex.
func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// harness is one composed delivery stack: producer store, gateway store,
// NATS server, publisher engine and the classifier with the engine
// sentinels registered.
type harness struct {
	prod     *delivery.MemoryProducer
	gw       *delivery.MemoryGateway
	server   *publisher.MemoryJetStream
	engine   *publisher.Engine
	target   destination.VerifiedTarget
	classify *derrors.Classifier
}

// newHarness builds a fresh stack with one session open.
func newHarness() *harness {
	prod, err := delivery.NewMemoryProducer(fProducer, fRun)
	if err != nil {
		panic(err)
	}
	gw, err := delivery.NewMemoryGateway(fEpoch)
	if err != nil {
		panic(err)
	}
	if err := gw.SetActiveBinding(1, fBindingDigest); err != nil {
		panic(err)
	}
	if err := gw.OpenSession(fFence(delivery.SessionNormal, 1)); err != nil {
		panic(err)
	}
	server := fServer()
	engine, err := publisher.New(server, gw)
	if err != nil {
		panic(err)
	}
	classifier := derrors.NewClassifier()
	// Wiring registers the engine sentinels of both blocks: the transient
	// and configuration outcomes of the publisher and the destination
	// check, and the immutable poison outcomes of the stores.
	for _, pair := range []struct {
		sentinel error
		class    derrors.Class
	}{
		{publisher.ErrTransient, derrors.ClassTransient},
		{publisher.ErrNoStream, derrors.ClassConfigBlocked},
		{publisher.ErrUnexpectedStream, derrors.ClassConfigBlocked},
		{publisher.ErrMsgTooLarge, derrors.ClassConfigBlocked},
		{publisher.ErrBadHeader, derrors.ClassConfigBlocked},
		{destination.ErrUnavailable, derrors.ClassTransient},
		{delivery.ErrImmutableConflict, derrors.ClassPoison},
		{delivery.ErrAlreadyRetired, derrors.ClassPoison},
	} {
		if err := classifier.Register(pair.sentinel, pair.class); err != nil {
			panic(err)
		}
	}
	return &harness{prod: prod, gw: gw, server: server, engine: engine,
		target: fTarget(), classify: classifier}
}

// manifestIdentity builds the manifest half of one sequence identity:
// everything READY fixes. The plan half comes from the reservation of the
// gateway the producer seals against.
func manifestIdentity(seq uint64) delivery.BatchIdentity {
	return delivery.BatchIdentity{
		Batch:          delivery.BatchID{Producer: fProducer, Run: fRun, Seq: seq},
		ManifestDigest: proto.Hash32{byte(seq), 1},
		BindingDigest:  fBindingDigest,
	}
}

// sealedIdentity completes a manifest identity with the plan half one
// gateway reservation fixed.
func sealedIdentity(manifest delivery.BatchIdentity, epoch proto.ID16) delivery.BatchIdentity {
	out := manifest
	out.PlanDigest = proto.Hash32{byte(manifest.Batch.Seq), 2}
	out.LayoutManifestDigest = proto.Hash32{byte(manifest.Batch.Seq), 3}
	out.IssuerStoreEpoch = epoch
	return out
}

// driveToReady takes the next sequence to durable READY (staged RAM
// chunks folded by the closing commit) and returns the manifest identity.
func (h *harness) driveToReady(t fatalf) (uint64, delivery.BatchIdentity) {
	t.Helper()
	seq, err := h.prod.AllocateIntent()
	if err != nil {
		t.Fatalf("AllocateIntent: %v", err)
	}
	if err := h.prod.StageChunk(seq, "chunk-1"); err != nil {
		t.Fatalf("StageChunk: %v", err)
	}
	if err := h.prod.StageChunk(seq, "chunk-2"); err != nil {
		t.Fatalf("StageChunk: %v", err)
	}
	id := manifestIdentity(seq)
	if err := h.prod.CommitReady(seq, id); err != nil {
		t.Fatalf("CommitReady: %v", err)
	}
	return seq, id
}

// driveToSealed takes a READY sequence to durable SEALED against the
// current gateway's reservation (its store epoch issues the plan) and
// returns the sealed identity.
func (h *harness) driveToSealed(t fatalf, seq uint64, manifest delivery.BatchIdentity, keys []delivery.FrameKey) delivery.BatchIdentity {
	t.Helper()
	id := sealedIdentity(manifest, h.gw.StoreEpoch())
	if err := h.prod.CommitSealed(seq, release.NewSealedPlan(id, 1000+seq, fStream, keys)); err != nil {
		t.Fatalf("CommitSealed: %v", err)
	}
	return id
}

// publishClosed registers a CLOSED batch and publishes it to NATS_ACKED,
// returning the IngestAck.
func (h *harness) publishClosed(t fatalf, id delivery.BatchIdentity, frames []publisher.Frame) delivery.IngestAck {
	t.Helper()
	if err := h.gw.ImportClosed(fFence(delivery.SessionNormal, 1),
		release.NewSealedPlan(id, 1000+id.Batch.Seq, fStream, fKeys(frames)), false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	ack, err := h.engine.Publish(fFence(delivery.SessionNormal, 1), h.target,
		publisher.Batch{Identity: id, Frames: frames})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return ack
}

// publishImported publishes a batch whose sealed plan was imported from
// the producer (IMPORT_SEALED): the original issuer epoch stands and no
// new reservation is allocated.
func (h *harness) publishImported(t fatalf, seq uint64) delivery.IngestAck {
	t.Helper()
	id, err := release.ImportSealed(h.prod, h.gw, fFence(delivery.SessionNormal, 1), seq)
	if err != nil {
		t.Fatalf("ImportSealed: %v", err)
	}
	plan, _ := h.prod.SealedPlan(seq)
	batch, ok := h.gw.Batch(fFence(delivery.SessionNormal, 1), id.Batch)
	if !ok || len(batch.FrameKeys) != len(plan.FrameKeys) {
		t.Fatalf("the imported batch carries the wrong frame set")
	}
	// The frames of the sealed plan republish byte-identically: rebuild
	// them from the producer's durable frame keys through the fixed
	// subjects of the binding.
	frames := make([]publisher.Frame, 0, len(plan.FrameKeys))
	for i, key := range plan.FrameKeys {
		eventID := proto.NewEventID(fProducer, fRun, 5000+seq)
		msgID, err := proto.NatsMsgID(proto.Hash32{5}, eventID, key.Fragment, proto.FormComplete, 0)
		if err != nil {
			t.Fatalf("NatsMsgID: %v", err)
		}
		header, err := proto.NatsHeader(msgID, fStream)
		if err != nil {
			t.Fatalf("NatsHeader: %v", err)
		}
		subject, err := destination.Subject(fPrefix, proto.FamilyTx, proto.KindApplied)
		if err != nil {
			t.Fatalf("Subject: %v", err)
		}
		frames = append(frames, publisher.Frame{Key: key, Subject: subject,
			MessageID: msgID, Header: header,
			Payload: []byte(fmt.Sprintf("faults-frame-%d-%d", seq, i))})
	}
	ack, err := h.engine.Publish(fFence(delivery.SessionNormal, 1), h.target,
		publisher.Batch{Identity: id, Frames: frames})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return ack
}

// cleanupProducer applies the IngestAck and deletes the payload.
func (h *harness) cleanupProducer(t fatalf, seq uint64, ack delivery.IngestAck) {
	t.Helper()
	if err := release.OnIngestAck(h.prod, seq, ack); err != nil {
		t.Fatalf("OnIngestAck: %v", err)
	}
	if remains, err := release.CleanupPayload(h.prod, seq, 100); err != nil || remains {
		t.Fatalf("CleanupPayload: remains=%v err=%v", remains, err)
	}
}

// releaseBatch releases one sequence on the gateway and retires it on the
// producer.
func (h *harness) releaseBatch(t fatalf, id delivery.BatchIdentity) {
	t.Helper()
	ack, err := release.Release(h.gw, fFence(delivery.SessionNormal, 1), id.Batch, 100)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := release.OnReleaseAck(h.prod, ack); err != nil {
		t.Fatalf("OnReleaseAck: %v", err)
	}
}

// runOneBatch drives one full sequence: READY, SEALED, CLOSED, published,
// acknowledged, cleaned up, released and retired.
func (h *harness) runOneBatch(t fatalf) delivery.BatchIdentity {
	t.Helper()
	seq, manifest := h.driveToReady(t)
	frames := fFrames(t, seq, proto.FamilyTx, proto.KindApplied, 2)
	id := h.driveToSealed(t, seq, manifest, fKeys(frames))
	ack := h.publishClosed(t, id, frames)
	h.cleanupProducer(t, seq, ack)
	h.releaseBatch(t, id)
	return id
}

// fatalf is the testing.T subset the harness needs.
type fatalf interface {
	Fatalf(format string, args ...any)
	Helper()
}

// destVerifier adapts destination.Check to the maintenance engine's
// TargetVerifier seam, resolving the desired digest through a stored
// desired binding.
type destVerifier struct {
	remote      destination.Remote
	desired     map[proto.Hash32]destination.Binding
	frameBudget uint64
}

// newDestVerifier builds the adapter over a remote.
func newDestVerifier(remote destination.Remote) *destVerifier {
	return &destVerifier{remote: remote,
		desired: make(map[proto.Hash32]destination.Binding), frameBudget: 1 << 20}
}

// add registers one desired binding the verifier can resolve.
func (v *destVerifier) add(b destination.Binding) { v.desired[b.DescriptorDigest] = b }

// VerifyTarget performs the D77 check of the desired binding.
func (v *destVerifier) VerifyTarget(ctx context.Context, digest proto.Hash32) error {
	binding, ok := v.desired[digest]
	if !ok {
		return fmt.Errorf("no desired binding for the digest")
	}
	subjects := []string{}
	for _, family := range []proto.Family{proto.FamilyTx, proto.FamilyBlock} {
		for _, kind := range []proto.Kind{proto.KindApplied, proto.KindRemoved, proto.KindStatus} {
			s, err := destination.Subject(binding.SubjectPrefix, family, kind)
			if err != nil {
				return err
			}
			subjects = append(subjects, s)
		}
	}
	report := destination.Check(ctx, v.remote, binding, subjects, v.frameBudget)
	if report.OK() {
		return nil
	}
	return fmt.Errorf("target check: %s: %v", report.Status, report.Reasons)
}

// maintenanceStack builds a maintenance engine over the harness stores.
func (h *harness) maintenanceStack(v maintenance.TargetVerifier) *maintenance.Engine {
	engine, err := maintenance.New(h.prod, h.gw, v)
	if err != nil {
		panic(err)
	}
	return engine
}
