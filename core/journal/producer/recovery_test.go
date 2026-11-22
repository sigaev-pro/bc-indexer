package producer

import (
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// Crash recovery: dropping the Store object and reopening over the same
// storage is exactly the view a restarted process sees after sync commits.
// Every step below reopens mid-flight and re-drives the protocol.

func reopen(t *testing.T, st storage.Storage) *Store {
	t.Helper()
	s, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRecoveryAtEveryTransition(t *testing.T) {
	st := storage.NewMemory()
	s := reopen(t, st)
	if err := s.SetDestinationBinding(testBinding); err != nil {
		t.Fatal(err)
	}

	// After the intent commit: the batch and its decision link survive, and
	// a replayed decision does not allocate a second batch.
	att, _, err := s.AllocateBatch(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	s = reopen(t, st)
	info, err := s.Batch(att.BatchSeq)
	if err != nil || info.State != StateIntent {
		t.Fatalf("reopened INTENT: %+v %v", info, err)
	}
	replayedAtt, replayed, err := s.AllocateBatch(1, 2)
	if err != nil || !replayed || replayedAtt.BatchSeq != att.BatchSeq {
		t.Fatalf("decision replay after reopen: %+v %v %v", replayedAtt, replayed, err)
	}

	// After StartAttempt: the generation and fresh event range survive.
	att, err = s.StartAttempt(att.BatchSeq, 2)
	if err != nil {
		t.Fatal(err)
	}
	s = reopen(t, st)
	info, err = s.Batch(att.BatchSeq)
	if err != nil || info.State != StateStaging || info.Intent.AttemptGeneration != att.Generation ||
		info.Intent.EventSequenceBase != att.EventSequenceBase {
		t.Fatalf("reopened STAGING: %+v %v", info, err)
	}

	// Chunk writes resume across reopen: a partial stream continues at the
	// durable offset without gaps or duplicates.
	manifest := []byte("extractor.manifest.v2\x00payload")
	if err := s.WriteAttemptManifest(att.BatchSeq, att.Generation, 0, manifest[:7]); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, st)
	if err := s.WriteAttemptManifest(att.BatchSeq, att.Generation, 7, manifest[7:]); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAttemptData(att.BatchSeq, att.Generation, 0, []byte("data")); err != nil {
		t.Fatal(err)
	}

	// After the READY closing commit: digest, checkpoint and immutability
	// survive; the identical commit is idempotent after reopen.
	ready, err := s.CommitReady(att.BatchSeq, att.Generation, uint64(len(manifest)), []byte("cp"))
	if err != nil {
		t.Fatal(err)
	}
	s = reopen(t, st)
	again, err := s.CommitReady(att.BatchSeq, att.Generation, uint64(len(manifest)), []byte("cp"))
	if err != nil || again != ready {
		t.Fatalf("CommitReady replay after reopen: %+v %v (want %+v)", again, err, ready)
	}
	if _, err := s.StartAttempt(att.BatchSeq, 2); !errors.Is(err, ErrStateConflict) {
		t.Fatal("post-READY immutability must survive reopen")
	}
	cp, err := s.Checkpoint(att.BatchSeq)
	if err != nil || string(cp) != "cp" {
		t.Fatalf("checkpoint after reopen: %q %v", cp, err)
	}

	// After BeginPlan: the plan generation and seed survive; chunk writes
	// continue and Seal closes on the reopened store.
	gen, planDigest, err := s.BeginPlan(att.BatchSeq, []byte("seed"), [32]byte{5}, []byte("h"))
	if err != nil {
		t.Fatal(err)
	}
	s = reopen(t, st)
	gen2, planDigest2, err := s.BeginPlan(att.BatchSeq, []byte("seed"), [32]byte{5}, []byte("h"))
	if err != nil || gen2 != gen || planDigest2 != planDigest {
		t.Fatalf("plan handoff replay after reopen: gen=%d/%d digest match=%v err=%v", gen2, gen, planDigest2 == planDigest, err)
	}
	if err := s.WritePlanChunk(att.BatchSeq, gen, 0, []byte("frame-one")); err != nil {
		t.Fatal(err)
	}
	if err := s.WritePlanChunk(att.BatchSeq, gen, 9, []byte("frame-two")); err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal(att.BatchSeq, gen, 18)
	if err != nil {
		t.Fatal(err)
	}

	// After SEALED: resealing is idempotent and replans conflict.
	s = reopen(t, st)
	if sealed2, err := s.Seal(att.BatchSeq, gen, 18); err != nil || sealed2 != sealed {
		t.Fatalf("seal replay after reopen: %+v %v", sealed2, err)
	}
	if _, _, err := s.BeginPlan(att.BatchSeq, []byte("other-seed"), [32]byte{6}, nil); !errors.Is(err, ErrImmutableConflict) {
		t.Fatal("post-SEALED replan conflict must survive reopen")
	}

	// After OnIngestAck: the outbox and ACKED_CLEANUP state survive, and a
	// replayed identical ack stays a no-op.
	ack := IngestAck{
		ManifestDigest: ready.ManifestDigest,
		PlanDigest:     sealed.PlanDigest,
		LayoutDigest:   sealed.LayoutDigest,
		BindingDigest:  testBinding,
		IssuerEpoch:    [16]byte{3},
	}
	if err := s.OnIngestAck(att.BatchSeq, ack); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, st)
	if err := s.OnIngestAck(att.BatchSeq, ack); err != nil {
		t.Fatalf("ack replay after reopen: %v", err)
	}
	entries, err := s.Outbox()
	if err != nil || len(entries) != 1 || entries[0].Seq != att.BatchSeq || entries[0].Ack != ack {
		t.Fatalf("outbox after reopen: %+v %v", entries, err)
	}

	// Cleanup resumes from the durable remaining keys across reopen.
	done, err := s.Cleanup(att.BatchSeq, 1)
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("one bounded step cannot finish this payload")
	}
	s = reopen(t, st)
	for {
		done, err = s.Cleanup(att.BatchSeq, 2)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
	}

	// After OnReleaseAck: the retired prefix and the empty outbox survive.
	if err := s.OnReleaseAck(att.BatchSeq); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, st)
	if s.RetiredPrefix() != att.BatchSeq {
		t.Fatalf("retired prefix after reopen = %d, want %d", s.RetiredPrefix(), att.BatchSeq)
	}
	entries, err = s.Outbox()
	if err != nil || len(entries) != 0 {
		t.Fatalf("outbox must be empty after retire: %+v %v", entries, err)
	}
	if err := s.OnReleaseAck(att.BatchSeq); err != nil {
		t.Fatal("replayed release ack for a retired prefix must stay a success")
	}
	info, err = s.Batch(att.BatchSeq)
	if err != nil || info.State != StateRetired {
		t.Fatalf("retired state after reopen: %+v %v", info, err)
	}
}

func TestRecoveryModeAndAdmissionSurviveReopen(t *testing.T) {
	st := storage.NewMemory()
	s := reopen(t, st)
	if err := s.SetDrain(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDrained(); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, st)
	if md := s.ModeInfo(); md.Mode != ModeDRAINED {
		t.Fatalf("DRAINED must survive restart without auto-LIVE, got %v", md.Mode)
	}
	gen, err := s.ResumeToLive()
	if err != nil || gen != 2 {
		t.Fatalf("resume after reopen: gen=%d err=%v", gen, err)
	}
	s = reopen(t, st)
	if md := s.ModeInfo(); md.Mode != ModeLIVE || md.AdmissionGeneration != 2 {
		t.Fatalf("resumed mode after reopen: %+v", md)
	}
	// A crash between the mode commit and anything else continues the same
	// generation; it never bumps twice.
	gen2, err := s.ResumeToLive()
	if err == nil || gen2 != 0 {
		t.Fatal("LIVE cannot resume again")
	}
}

func TestRecoveryAttemptRetryKeepsSemanticIdentity(t *testing.T) {
	st := storage.NewMemory()
	s := reopen(t, st)
	if err := s.SetDestinationBinding(testBinding); err != nil {
		t.Fatal(err)
	}
	att, _, err := s.AllocateBatch(7, 1)
	if err != nil {
		t.Fatal(err)
	}

	// First attempt stalls mid-assembly, the process restarts, and the
	// assembly retries: same batch, new attempt generation, fresh event
	// sequences, no Publish ever happened.
	first, err := s.StartAttempt(att.BatchSeq, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAttemptManifest(att.BatchSeq, first.Generation, 0, []byte("broken")); err != nil {
		t.Fatal(err)
	}
	s = reopen(t, st)
	second, err := s.StartAttempt(att.BatchSeq, 1)
	if err != nil {
		t.Fatal(err)
	}
	if second.BatchSeq != first.BatchSeq || second.Generation != first.Generation+1 ||
		second.EventSequenceBase <= first.EventSequenceBase {
		t.Fatalf("retry identity: first=%+v second=%+v", first, second)
	}
	if err := s.WriteAttemptManifest(att.BatchSeq, second.Generation, 0, []byte("extractor.manifest.v2\x00")); err != nil {
		t.Fatal(err)
	}
	ready, err := s.CommitReady(att.BatchSeq, second.Generation, 22, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ready.ManifestDigest != sha256.Sum256([]byte("extractor.manifest.v2\x00")) {
		t.Fatal("ready digest must be over the winning attempt's manifest bytes")
	}
	// The decision replay still finds the same batch after the full retry.
	replayedAtt, replayed, err := s.AllocateBatch(7, 1)
	if err != nil || !replayed || replayedAtt.BatchSeq != att.BatchSeq {
		t.Fatalf("decision replay after retry: %+v %v %v", replayedAtt, replayed, err)
	}
}
