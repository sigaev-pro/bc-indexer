package journal

import (
	"crypto/sha256"
	"errors"
	"testing"
)

// closedPlan drives one batch through prepare, publish, close and full
// PubAck accounting.
func closedPlan(t *testing.T, j *Journal, seq uint64) planFixture {
	t.Helper()
	pf := newPlan(t, j, seq, false)
	pf.beginPublish(t, j)
	pf.publishFrames(t, j)
	if err := j.FinalizePublish(pf.id, pf.planDigest, pf.res.ManifestDigest, pf.layout); err != nil {
		t.Fatal(err)
	}
	for _, f := range pf.frames {
		if _, err := j.RecordPubAck(pf.id, pf.planDigest, f.addr); err != nil {
			t.Fatal(err)
		}
	}
	return pf
}

func TestPubAckAccounting(t *testing.T) {
	j, _ := newJournal(t)
	pf := newPlan(t, j, 1, false)

	// PubAck before CLOSED never creates state.
	if _, err := j.RecordPubAck(pf.id, pf.planDigest, pf.frames[0].addr); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("ack before CLOSED: got %v, want ErrStateConflict", err)
	}
	pf.beginPublish(t, j)
	pf.publishFrames(t, j)
	if err := j.FinalizePublish(pf.id, pf.planDigest, pf.res.ManifestDigest, pf.layout); err != nil {
		t.Fatal(err)
	}

	// Unknown frame and wrong plan digest are rejected.
	if _, err := j.RecordPubAck(pf.id, pf.planDigest, FrameAddr{9, 9, 9}); !errors.Is(err, ErrBatchNotFound) {
		t.Fatalf("unknown frame ack: got %v, want ErrBatchNotFound", err)
	}
	if _, err := j.RecordPubAck(pf.id, [32]byte{1}, pf.frames[0].addr); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("wrong plan ack: got %v, want ErrIdentityMismatch", err)
	}

	// A partial acknowledgement never completes the batch.
	allDone, err := j.RecordPubAck(pf.id, pf.planDigest, pf.frames[0].addr)
	if err != nil || allDone {
		t.Fatalf("first ack: allDone=%v err=%v", allDone, err)
	}
	if err := j.MarkNATSACKed(pf.id, pf.planDigest, pf.res.ManifestDigest); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("NATS_ACKED with missing acks: got %v, want ErrStateConflict", err)
	}
	// A duplicate acknowledgement is allowed and changes nothing.
	if allDone, err = j.RecordPubAck(pf.id, pf.planDigest, pf.frames[0].addr); err != nil || allDone {
		t.Fatalf("duplicate ack: allDone=%v err=%v", allDone, err)
	}
	if _, err := j.RecordPubAck(pf.id, pf.planDigest, pf.frames[1].addr); err != nil {
		t.Fatal(err)
	}
	allDone, err = j.RecordPubAck(pf.id, pf.planDigest, pf.frames[2].addr)
	if err != nil || !allDone {
		t.Fatalf("final ack: allDone=%v err=%v", allDone, err)
	}

	if err := j.MarkNATSACKed(pf.id, pf.planDigest, pf.res.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	info, err := j.Batch(pf.id)
	if err != nil || info.State != StateNatsAcked {
		t.Fatalf("state after NATS_ACKED: %+v %v", info, err)
	}
	// Idempotent marker, conflicting identity rejected.
	if err := j.MarkNATSACKed(pf.id, pf.planDigest, pf.res.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	if err := j.MarkNATSACKed(pf.id, pf.planDigest, [32]byte{2}); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("conflicting marker: got %v", err)
	}
	// A late duplicate PubAck stays a no-op.
	if allDone, err := j.RecordPubAck(pf.id, pf.planDigest, pf.frames[0].addr); err != nil || !allDone {
		t.Fatalf("late duplicate ack: allDone=%v err=%v", allDone, err)
	}
}

func TestReleaseOrderedAndBounded(t *testing.T) {
	j, _ := newJournal(t)
	first := closedPlan(t, j, 1)
	if err := j.MarkNATSACKed(first.id, first.planDigest, first.res.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	second := closedPlan(t, j, 2)
	if err := j.MarkNATSACKed(second.id, second.planDigest, second.res.ManifestDigest); err != nil {
		t.Fatal(err)
	}

	// Release requires NATS_ACKED: a closed but unacked batch is refused.
	unacked := newPlan(t, j, 3, false)
	unacked.beginPublish(t, j)
	unacked.publishFrames(t, j)
	if err := j.FinalizePublish(unacked.id, unacked.planDigest, unacked.res.ManifestDigest, unacked.layout); err != nil {
		t.Fatal(err)
	}
	if err := j.BeginRelease(unacked.id, unacked.planDigest); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("unacked release: got %v, want ErrStateConflict", err)
	}
	if err := j.BeginRelease(first.id, first.planDigest); err != nil {
		t.Fatal(err)
	}
	if err := j.BeginRelease(first.id, first.planDigest); err != nil {
		t.Fatal(err)
	}
	if err := j.BeginRelease(first.id, [32]byte{3}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("wrong plan release: got %v", err)
	}

	// CompleteRelease before the bounded cleanup finishes is rejected.
	if _, err := j.CompleteRelease(first.id); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("early complete: got %v, want ErrStateConflict", err)
	}
	// Bounded cleanup: one key per call makes several steps necessary.
	for {
		done, err := j.ReleaseCleanup(first.id, 1)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
	}
	prefix, err := j.CompleteRelease(first.id)
	if err != nil || prefix != 1 {
		t.Fatalf("complete release: prefix=%d err=%v", prefix, err)
	}
	info, err := j.Batch(first.id)
	if err != nil || info.State != StateRetired || info.Reservation != nil {
		t.Fatalf("retired batch: %+v %v", info, err)
	}
	// Retiring batch 2 before the gateway finished it is out of order.
	secondPrefix, err := j.CompleteRelease(second.id)
	if !errors.Is(err, ErrStateConflict) || secondPrefix != 1 {
		t.Fatalf("unordered retire: prefix=%d err=%v", secondPrefix, err)
	}
	if err := j.BeginRelease(second.id, second.planDigest); err != nil {
		t.Fatal(err)
	}
	for {
		done, err := j.ReleaseCleanup(second.id, 50)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
	}
	if prefix, err = j.CompleteRelease(second.id); err != nil || prefix != 2 {
		t.Fatalf("second retire: prefix=%d err=%v", prefix, err)
	}
	// The retired prefix is readable for the recovery handshake.
	p, err := j.RetiredPrefix(first.id)
	if err != nil || p != 2 {
		t.Fatalf("retired prefix read: %d %v, want 2", p, err)
	}
}

func TestAlreadyRetiredAfterRelease(t *testing.T) {
	j, _ := newJournal(t)
	pf := closedPlan(t, j, 1)
	if err := j.MarkNATSACKed(pf.id, pf.planDigest, pf.res.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	if err := j.BeginRelease(pf.id, pf.planDigest); err != nil {
		t.Fatal(err)
	}
	// A stale PubAck callback arriving during RELEASING is a tolerated
	// duplicate: it must not resurrect publication state.
	if allDone, err := j.RecordPubAck(pf.id, pf.planDigest, pf.frames[0].addr); err != nil || !allDone {
		t.Fatalf("stale callback during release: allDone=%v err=%v", allDone, err)
	}
	for {
		done, err := j.ReleaseCleanup(pf.id, 100)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
	}
	if _, err := j.CompleteRelease(pf.id); err != nil {
		t.Fatal(err)
	}
	if _, err := j.BeginPrepare(pf.id, sha256.Sum256([]byte("again")), 1, 5, PrepareNormal, nil); !errors.Is(err, ErrAlreadyRetired) {
		t.Fatalf("re-prepare retired batch: got %v, want ErrAlreadyRetired", err)
	}
	if err := j.BeginRelease(pf.id, pf.planDigest); !errors.Is(err, ErrAlreadyRetired) {
		t.Fatalf("re-release retired batch: got %v, want ErrAlreadyRetired", err)
	}
}
