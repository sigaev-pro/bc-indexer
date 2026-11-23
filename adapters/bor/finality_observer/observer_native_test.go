package finalityobserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// AC-LIF-07-01: a persisted milestone/checkpoint whitelist without the
// saved full source response publishes as the narrower Known whitelist
// with an absent source_evidence and no invented digest; the checkpoint
// root never substitutes a block hash.
func TestNarrowerWhitelistWithoutSourceResponse(t *testing.T) {
	observer, store, _, _, _ := newTestFixture(t)
	head := fixtureHead()
	anchor := block.AnchorRef{Height: 29900, Hash: mustHash(fmtHash(29900))}

	obs, err := observer.ObserveNative(NativeInput{
		Head:     head,
		Decision: testDecision(1),
		Origin:   block.OriginNativeRestart,
		// A checkpoint status without its retained response: no
		// checkpoint evidence is invented.
		Checkpoint: block.Some(SourceCheckpoint{
			CheckpointID: block.Some(testCheckpointID),
			Proposer:     testProposer,
			Start:        testBorStart,
			End:          testBorEnd,
			RootHash:     testCheckpointRt,
			State:        CheckpointStoredAcknowledged,
		}),
		MilestoneWhitelist: block.Some(RetainedWhitelist{
			Kind:     WhitelistMilestone,
			Anchor:   anchor,
			Decision: testDecision(1),
			Request:  Request{Operation: OpMilestoneLatest},
			// No Response: the narrower retained form.
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	store.Sync()

	w, ok := obs.Evidence.MilestoneWhitelist.Get()
	if !ok {
		t.Fatal("the whitelist must publish as Known")
	}
	if !w.Narrower() {
		t.Fatal("without the saved response the whitelist is the narrower form")
	}
	if _, hasSource := w.SourceEvidence.Get(); hasSource {
		t.Fatal("the narrower form must not carry a source observation ref")
	}
	// No checkpoint evidence was invented from the response-less status.
	if obs.Evidence.Checkpoint.Kind != EvidenceNotObserved {
		t.Fatalf("checkpoint evidence %v, want NOT_OBSERVED", obs.Evidence.Checkpoint.Kind)
	}
	// The native finalized projection still works off the whitelist.
	f, ok := obs.Evidence.NativeFinalized.Get()
	if !ok || f.Basis != BasisMilestoneWhitelist || f.Anchor != anchor {
		t.Fatalf("native finalized %v, want milestone whitelist at %s", f, anchor.Hash)
	}
	// The marker anchor is the whitelist anchor, never the checkpoint root.
	markers, err := observer.Markers()
	if err != nil {
		t.Fatal(err)
	}
	if markers.Milestone.Kind != block.ReferenceKnown || markers.Milestone.Anchor != anchor {
		t.Fatalf("milestone marker %v, want KNOWN at the whitelist anchor", markers.Milestone)
	}
	// The checkpoint marker without a locally verified end stays
	// NotObserved: the root is a range commitment, not a block hash.
	if markers.Checkpoint.Kind != block.ReferenceNotObserved {
		t.Fatalf("checkpoint marker %v, want NOT_OBSERVED", markers.Checkpoint.Kind)
	}
}

// A whitelist is accepted only after the canonical match: an anchor off
// the canonical chain of the observed head is an invariant error.
func TestWhitelistRequiresCanonicalMatch(t *testing.T) {
	observer, _, _, _, _ := newTestFixture(t)
	head := fixtureHead()
	_, err := observer.ObserveNative(NativeInput{
		Head:     head,
		Decision: testDecision(1),
		Origin:   block.OriginNativeLive,
		MilestoneWhitelist: block.Some(RetainedWhitelist{
			Kind:     WhitelistMilestone,
			Anchor:   block.AnchorRef{Height: 30, Hash: mustHash("side-branch-30")},
			Decision: testDecision(1),
			Request:  Request{Operation: OpMilestoneLatest},
		}),
	})
	if !errors.Is(err, ErrInvariant) {
		t.Fatalf("error %v, want ErrInvariant", err)
	}
}

// AC-LIF-07-06: with the native milestone and the checkpoint fallback both
// covering the activation and a durable L1 anchor retained, a native
// milestone purge moves the native projection to the checkpoint fallback
// and does not delete the retained L1 GC anchor.
func TestMilestonePurgeFallsBackToCheckpoint(t *testing.T) {
	observer, store, _, _, _ := newTestFixture(t)
	head := fixtureHead()
	milestoneAnchor := block.AnchorRef{Height: 29900, Hash: mustHash(fmtHash(29900))}
	checkpointAnchor := block.AnchorRef{Height: 29800, Hash: mustHash(fmtHash(29800))}

	// A durable L1 anchor exists first.
	if _, err := observer.ObserveL1(testDecision(1), head, block.OriginNativeLive); err != nil {
		t.Fatal(err)
	}
	store.Sync()
	frontier, ok, _ := store.Frontier()
	if !ok {
		t.Fatal("setup: the L1 anchor must be accepted")
	}

	obs, err := observer.ObserveNative(NativeInput{
		Head:     head,
		Decision: testDecision(2),
		Origin:   block.OriginNativeLive,
		Milestone: block.Some(SourceMilestone{
			MilestoneID: "ms-2", Proposer: testProposer,
			Start: 29501, End: 29900, Timestamp: 1200, TotalDifficulty: 9,
			EndHash: milestoneAnchor.Hash, Outcome: MilestoneLocallyVerified,
		}),
		MilestoneResponse: block.Some(Response{Bytes: []byte(`{"m":1}`), RequestSequence: 4}),
		MilestoneWhitelist: block.Some(RetainedWhitelist{
			Kind: WhitelistMilestone, Anchor: milestoneAnchor, Decision: testDecision(2),
			Request: Request{Operation: OpMilestoneLatest},
		}),
		CheckpointWhitelist: block.Some(RetainedWhitelist{
			Kind: WhitelistCheckpoint, Anchor: checkpointAnchor, Decision: testDecision(2),
			Request: Request{Operation: OpCheckpointLatest},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := obs.Evidence.NativeFinalized.Get(); !ok || f.Basis != BasisMilestoneWhitelist {
		t.Fatal("native finalized must choose the milestone whitelist first")
	}
	store.Sync()

	// The milestone purge: the checkpoint fallback takes over, the L1
	// evidence is carried and the frontier is untouched.
	purged, err := observer.ObserveNative(NativeInput{
		Head:            head,
		Decision:        testDecision(3),
		Origin:          block.OriginNativeLive,
		MilestonePurged: true,
		CheckpointWhitelist: block.Some(RetainedWhitelist{
			Kind: WhitelistCheckpoint, Anchor: checkpointAnchor, Decision: testDecision(3),
			Request: Request{Operation: OpCheckpointLatest},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if purged.Evidence.MilestoneWhitelist.Kind != EvidenceNotObserved {
		t.Fatal("a purged milestone whitelist must not stay")
	}
	f, ok := purged.Evidence.NativeFinalized.Get()
	if !ok || f.Basis != BasisCheckpointWhitelist || f.Anchor != checkpointAnchor {
		t.Fatalf("native finalized %v, want the checkpoint fallback", f)
	}
	retained, ok := purged.Evidence.L1Checkpoint.Get()
	if !ok || retained.BorEndRef != frontier.BorEndRef {
		t.Fatal("the retained L1 GC anchor must survive a milestone purge")
	}
	if purged.Evidence.L1Checkpoint.Origin != block.OriginRetainedDurable {
		t.Fatal("carried L1 evidence is RETAINED_DURABLE")
	}
	after, ok, _ := store.Frontier()
	if !ok || after != frontier {
		t.Fatal("the frontier must be unchanged by the purge")
	}
}

// The publication path never waits for the GC source: with the L1 seam
// erroring on every network method, the native observation and the
// markers still complete.
func TestPublicationDoesNotWaitForGCSource(t *testing.T) {
	observer, _, _, l1, _ := newTestFixture(t)
	l1.setFinErr(Unavailable(block.UnavailableSourceOffline))
	l1.setHeaderErr(Unavailable(block.UnavailableSourceOffline))
	head := fixtureHead()

	obs, err := observer.ObserveNative(NativeInput{
		Head:     head,
		Decision: testDecision(1),
		Origin:   block.OriginNativeLive,
		Milestone: block.Some(SourceMilestone{
			MilestoneID: "ms-1", Proposer: testProposer,
			Start: 29501, End: 29900, Timestamp: 1200, TotalDifficulty: 9,
			EndHash: mustHash(fmtHash(29900)), Outcome: MilestoneObserved,
		}),
		MilestoneResponse: block.Some(Response{Bytes: []byte(`{"m":1}`), RequestSequence: 4}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Evidence.Milestone.Kind != EvidenceKnown {
		t.Fatal("native milestone evidence must not wait for the L1 source")
	}
	if calls := l1.calls(); len(calls) != 0 {
		t.Fatalf("the native observation made %d L1 reads, want 0", len(calls))
	}
	if _, err := observer.Markers(); err != nil {
		t.Fatalf("markers must not wait for the GC source: %v", err)
	}
	// The GC lookup itself returns the explicit availability.
	gc, err := observer.ObserveL1(testDecision(2), head, block.OriginNativeLive)
	if err != nil {
		t.Fatal(err)
	}
	if gc.Evidence.L1Checkpoint.Kind != EvidenceUnavailable || gc.Evidence.L1Checkpoint.Reason != block.UnavailableSourceOffline {
		t.Fatalf("gc lookup %v, want UNAVAILABLE/SOURCE_OFFLINE", gc.Evidence.L1Checkpoint)
	}
}

// AC-LIF-07-08: observation O1 pinned sources file S1; the configuration
// is replaced by S2 (including a whitespace-only variant); the new
// observation O2 binds SHA256(bytes S2) while O1 keeps SHA256(bytes S1);
// equal deployment IDs never substitute the file digest.
func TestSourcesReplacementBindsNewObservationsOnly(t *testing.T) {
	s1, err := ParseSources(testSources(""))
	if err != nil {
		t.Fatal(err)
	}
	s2Bytes := testSources("  ") // whitespace-only change
	s2, err := ParseSources(s2Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if s1.Digest == s2.Digest {
		t.Fatal("a whitespace-only change must change the D80 digest")
	}
	if s1.L1.DeploymentID != s2.L1.DeploymentID || s1.Heimdall.DeploymentID != s2.Heimdall.DeploymentID {
		t.Fatal("the fixture must keep the deployment IDs equal")
	}

	store := NewMemoryStore()
	heimdall, l1, ancestry := fixtureSeams(t)
	observer, err := NewObserver(store, s1, heimdall, l1, ancestry)
	if err != nil {
		t.Fatal(err)
	}
	head := fixtureHead()
	o1, err := observer.ObserveL1(testDecision(1), head, block.OriginNativeLive)
	if err != nil {
		t.Fatal(err)
	}
	store.Sync()
	if o1.Binding.SourcesBinding != s1.Digest {
		t.Fatal("O1 must bind the digest of S1")
	}
	if ev, ok := o1.Evidence.L1Checkpoint.Get(); !ok || ev.SourcesBinding != s1.Digest {
		t.Fatal("the L1Checkpoint evidence of O1 must bind S1")
	}

	if err := observer.ReplaceSources(s2Bytes); err != nil {
		t.Fatal(err)
	}
	o2, err := observer.ObserveL1(testDecision(2), head, block.OriginNativeLive)
	if err != nil {
		t.Fatal(err)
	}
	store.Sync()
	if o2.Binding.SourcesBinding != s2.Digest {
		t.Fatal("O2 must bind the digest of S2")
	}
	if ev, ok := o2.Evidence.L1Checkpoint.Get(); !ok || ev.SourcesBinding != s2.Digest {
		t.Fatal("the L1Checkpoint evidence of O2 must bind S2")
	}
	// O1 keeps S1 although the deployment IDs are equal.
	replayed, err := observer.Resend(o1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Binding.SourcesBinding != s1.Digest {
		t.Fatal("O1 keeps the digest of S1 after the replacement")
	}
}

// AC-LIF-07-09: durable O1 evidence/READY/SEALED with digest S1 while the
// current sources_file is already S2: restart/recovery resends O1 without
// a new source lookup, using the saved digest S1, and the evidence bytes
// stay byte-identical; the current file never rewrites the provenance.
func TestReplayUsesSavedDigestAndBytes(t *testing.T) {
	s1Bytes := testSources("")
	s2Bytes := testSources("  ")
	s1, err := ParseSources(s1Bytes)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := ParseSources(s2Bytes)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	heimdall, l1, ancestry := fixtureSeams(t)
	observer, err := NewObserver(store, s1, heimdall, l1, ancestry)
	if err != nil {
		t.Fatal(err)
	}
	head := fixtureHead()
	o1, err := observer.ObserveL1(testDecision(1), head, block.OriginNativeLive)
	if err != nil {
		t.Fatal(err)
	}
	// The durable commit covers exactly O1.
	store.Sync()
	// O2 binds S2 but crashes before its sync: the unsynced write dies.
	if err := observer.ReplaceSources(s2Bytes); err != nil {
		t.Fatal(err)
	}
	o2, err := observer.ObserveL1(testDecision(2), head, block.OriginNativeLive)
	if err != nil {
		t.Fatal(err)
	}
	if o2.Binding.SourcesBinding != s2.Digest {
		t.Fatal("O2 (before the crash) binds S2")
	}
	store.Crash()

	// Restart: a new observer over the same durable store, current file S2.
	restarted, err := NewObserver(store, s2, heimdall, l1, ancestry)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.SourcesDigest() != s2.Digest {
		t.Fatal("the current digest after restart is S2")
	}
	replayed, err := restarted.Resend(o1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Binding.SourcesBinding != s1.Digest {
		t.Fatalf("replay must use the saved digest S1, got %s", replayed.Binding.SourcesBinding)
	}
	original, err := o1.EvidenceBytes()
	if err != nil {
		t.Fatal(err)
	}
	again, err := replayed.EvidenceBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, again) {
		t.Fatal("the replayed evidence bytes must be byte-identical")
	}
	// The current file never rewrites the provenance of the stored
	// observations.
	observations, err := store.Observations()
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 || observations[0].ID != o1.ID {
		t.Fatalf("the crashed O2 must not be durable; got %d observations", len(observations))
	}
	for _, obs := range observations {
		if obs.Binding.SourcesBinding == s2.Digest {
			t.Fatal("no durable observation may be rebound to S2")
		}
	}
}

// Power loss before the durable commit loses exactly the unsynced
// observations; the next observation binds the then-current digest.
func TestCrashBeforeSyncLosesUnsyncedObservation(t *testing.T) {
	s1, err := ParseSources(testSources(""))
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	heimdall, l1, ancestry := fixtureSeams(t)
	observer, err := NewObserver(store, s1, heimdall, l1, ancestry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := observer.ObserveL1(testDecision(1), fixtureHead(), block.OriginNativeLive); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Frontier(); ok {
		t.Fatal("an unsynced frontier is not durable")
	}
	store.Crash()
	observations, err := store.Observations()
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 0 {
		t.Fatalf("%d observations after the crash, want 0", len(observations))
	}
}

// The projection coverage follows the ancestry of the exact activation
// hash, never the height alone; without a covering anchor no coverage is
// assigned.
func TestProjectionCoverage(t *testing.T) {
	observer, store, _, _, _ := newTestFixture(t)
	head := fixtureHead()
	milestoneAnchor := block.AnchorRef{Height: 29900, Hash: mustHash(fmtHash(29900))}

	// No observation yet: no coverage at all.
	p, err := observer.Projection(block.AnchorRef{Height: 25000, Hash: mustHash(fmtHash(25000))})
	if err != nil {
		t.Fatal(err)
	}
	if p.Native != CanonicalNoNativeFinality || p.L1 != L1NotVerified {
		t.Fatalf("empty stream projection %v/%v", p.Native, p.L1)
	}

	// Milestone whitelist at 30, checkpoint fallback at 28, L1 frontier
	// at 30000.
	if _, err := observer.ObserveL1(testDecision(1), head, block.OriginNativeLive); err != nil {
		t.Fatal(err)
	}
	if _, err := observer.ObserveNative(NativeInput{
		Head:     head,
		Decision: testDecision(2),
		Origin:   block.OriginNativeLive,
		MilestoneWhitelist: block.Some(RetainedWhitelist{
			Kind: WhitelistMilestone, Anchor: milestoneAnchor, Decision: testDecision(2),
			Request: Request{Operation: OpMilestoneLatest},
		}),
	}); err != nil {
		t.Fatal(err)
	}
	store.Sync()

	covered := block.AnchorRef{Height: 25000, Hash: mustHash(fmtHash(25000))}
	if p, _ := observer.Projection(covered); p.Native != NativeMilestoneCovered {
		t.Fatalf("projection %v, want NATIVE_MILESTONE_COVERED", p.Native)
	}
	if p, _ := observer.Projection(covered); p.L1 != L1CheckpointCovered {
		t.Fatalf("projection %v, want L1_CHECKPOINT_COVERED below the frontier end", p.L1)
	}
	// A same-height different hash is not covered: the ancestry uses the
	// exact activation hash, never the height alone.
	forked := block.AnchorRef{Height: 25000, Hash: mustHash("fork-25000")}
	if p, _ := observer.Projection(forked); p.Native != CanonicalNoNativeFinality || p.L1 != L1NotVerified {
		t.Fatalf("projection %v/%v for a fork hash, want no coverage", p.Native, p.L1)
	}
}

// The evidence JSON round-trips: kind-dependent shapes survive encode and
// decode, and an unknown state never decodes with an invented value.
func TestEvidenceJSONRoundTrip(t *testing.T) {
	observer, store, _, _, _ := newTestFixture(t)
	if _, err := observer.ObserveL1(testDecision(1), fixtureHead(), block.OriginNativeLive); err != nil {
		t.Fatal(err)
	}
	store.Sync()
	observations, err := store.Observations()
	if err != nil || len(observations) != 1 {
		t.Fatalf("observations: %v %d", err, len(observations))
	}
	raw, err := json.Marshal(observations[0])
	if err != nil {
		t.Fatal(err)
	}
	var back Observation
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Equal(observations[0]) {
		t.Fatal("the observation must round-trip to equal evidence bytes")
	}

	// An UNAVAILABLE marker shape with a value is rejected.
	bad := []byte(`{"kind":"UNAVAILABLE","value":{"x":1},"reason":"SOURCE_OFFLINE"}`)
	var ev Evidence[L1Checkpoint]
	if err := json.Unmarshal(bad, &ev); err == nil {
		t.Fatal("an unavailable evidence with a value must be rejected")
	}
}

// fixtureSeams builds the plain source seams without the observer, for the
// replacement tests that own their sources files.
func fixtureSeams(t *testing.T) (*fakeHeimdall, *fakeL1, *fakeAncestry) {
	t.Helper()
	observer, _, heimdall, l1, ancestry := newTestFixture(t)
	_ = observer
	return heimdall, l1, ancestry
}
