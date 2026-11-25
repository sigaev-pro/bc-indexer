package ingest

import (
	"context"
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/gateway/session"
)

// Each test of this file fails when exactly one control of the RPC layer is
// deleted. A control whose removal changes nothing that a test can see is
// either removed or covered here.

// The seal of the last representable generation is refused: no next
// generation exists, so that seal could never be resumed. The rule that the
// sealed generation is the session's or the one below it does not catch it,
// because the session may run in that generation too.
func TestSealOfLastGenerationIsRefused(t *testing.T) {
	h := newHarness(t)
	last := ^uint64(0)
	s := h.openSessionAt(t, last, 1)
	resp, err := h.client.SealGeneration(context.Background(), &SealGenerationRequest{
		Header: s.header(), BindingGeneration: 1, AdmissionGeneration: last, DrainId: testDrainID, BatchHighwater: 0})
	wantCode(t, "seal of the last generation", wireErr(t, resp.GetError(), err), ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
	if _, sealed, err := h.j.SealedGeneration(jid(0)); err != nil || sealed {
		t.Fatalf("a refused seal left a record: sealed=%v %v", sealed, err)
	}
	// The generation below the last one still has a next one, so it seals.
	under := h.openSessionAt(t, last-1, 2)
	sealOverWire(t, h, under.header, 1, last-1, 0)
}

// The retry of a seal or of a Resume comes from the generation it names or
// from the next one. A session of any other generation is refused, although
// the durable receipt would answer it: a retry never crosses generations.
func TestSealAndResumeRetryNeedTheirGenerations(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	s := h.openSessionAt(t, 1, 1)
	seal := sealOverWire(t, h, s.header, 1, 1, 0)
	next := h.openSessionAt(t, 2, 2)
	if resumed, e := resumeOverWire(t, h, next.header, seal, resumeFive, testBinding, 2); e != nil {
		t.Fatalf("resume: %s %s", e.GetCode(), e.GetDetail())
	} else if resumed.GetAck() == nil {
		t.Fatal("resume answered no ack")
	}

	// Generation 5 is neither the sealed one nor the next one.
	stranger := h.openSessionAt(t, 5, 2)
	sealRetry, err := h.client.SealGeneration(ctx, &SealGenerationRequest{
		Header: stranger.header(), BindingGeneration: 1, AdmissionGeneration: 1, DrainId: testDrainID, BatchHighwater: 0})
	wantCode(t, "seal retry from another generation", wireErr(t, sealRetry.GetError(), err),
		ErrorCode_GENERATION_CONFLICT, RetryAction_RECONNECT)
	_, e := resumeOverWire(t, h, stranger.header, seal, resumeFive, testBinding, 2)
	wantCode(t, "resume retry from another generation", e, ErrorCode_GENERATION_CONFLICT, RetryAction_RECONNECT)
}

// A seal raises the publisher barrier: a callback of the publisher
// generation before it is stale afterwards, and a refused seal raises
// nothing.
func TestSealRaisesPublisherBarrier(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	s := h.openSessionAt(t, 1, 1)
	gate := h.server.PublisherGate()
	before := gate.Generation()
	callback := session.Callback{Batch: jid(1).Bytes(), Generation: before}
	if err := gate.Apply(callback, func() error { return nil }); err != nil {
		t.Fatalf("a callback before the seal: %v", err)
	}
	refused, err := h.client.SealGeneration(ctx, &SealGenerationRequest{
		Header: s.header(), BindingGeneration: 1, AdmissionGeneration: 1, DrainId: make([]byte, 16), BatchHighwater: 0})
	wantCode(t, "seal without a drain id", wireErr(t, refused.GetError(), err), ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
	if gate.Generation() != before {
		t.Fatalf("a refused seal raised the publisher generation to %d", gate.Generation())
	}
	sealOverWire(t, h, s.header, 1, 1, 0)
	if gate.Generation() != before+1 {
		t.Fatalf("publisher generation after the seal = %d, want %d", gate.Generation(), before+1)
	}
	if err := gate.Apply(callback, func() error { return nil }); !errors.Is(err, session.ErrCallbackStale) {
		t.Fatalf("a callback of the generation before the seal: %v, want ErrCallbackStale", err)
	}
}

// The producer assertions of a switch commit and of a recovery release must
// be present, and an assertion must name the switch of its request. Without
// these controls a missing message reads as an unsupported schema version,
// and an assertion for another switch would be checked against this one.
func TestSwitchAndReleaseAssertionsAreRequired(t *testing.T) {
	h := newSwitchHarness(t)
	ctx := context.Background()
	active := activeSpec.digest()
	a := h.openSessionBound(t, 1, 1, active)
	manifest, plan, layout := sha32([]byte("manifest")), sha32([]byte("plan")), sha32([]byte("layout"))
	ack := ackProto(2, as32(manifest), as32(plan), as32(layout), active[:])

	// RecoveryRelease: the assertion and the ack are both required.
	missing, err := h.client.RecoveryRelease(ctx, &RecoveryReleaseRequest{Header: a.header(), Batch: batchMsg(2), IngestAck: ack})
	wantCode(t, "recovery release without an assertion", wireErr(t, missing.GetError(), err),
		ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
	missing, err = h.client.RecoveryRelease(ctx, &RecoveryReleaseRequest{Header: a.header(), Batch: batchMsg(2),
		Assertion: outboxProto(2, as32(plan), active[:])})
	wantCode(t, "recovery release without an ack", wireErr(t, missing.GetError(), err),
		ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)

	// CommitSwitch: the assertion is required and names the switch.
	receipt := &SwitchPrepared{SwitchId: switchID, OldBindingDigest: active[:], NewBindingDigest: sha32([]byte("new")),
		DrainId: testDrainID, AdmissionGeneration: 1, IssuerStoreEpoch: epochOf(h)}
	commit, err := h.client.CommitSwitch(ctx, &CommitSwitchRequest{Header: a.header(), SwitchId: switchID, Prepared: receipt})
	wantCode(t, "switch commit without an assertion", wireErr(t, commit.GetError(), err),
		ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
	commit, err = h.client.CommitSwitch(ctx, &CommitSwitchRequest{Header: a.header(), SwitchId: switchID, Prepared: receipt,
		Assertion: &BindingCommitAssertion{SchemaVersion: 1, SwitchId: otherSwitchID,
			BindingDigest: sha32([]byte("new")), BindingGeneration: 4}})
	wantCode(t, "switch commit with an assertion of another switch", wireErr(t, commit.GetError(), err),
		ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF)
}
