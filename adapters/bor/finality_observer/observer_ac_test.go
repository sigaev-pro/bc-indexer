package finalityobserver

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// newTestFixture builds the observer over the shared fixture: a synced
// supporting stack, a stored checkpoint covering Bor blocks 20001..30000
// and a canonical Bor chain of 40 blocks.
func newTestFixture(t *testing.T) (*Observer, *MemoryStore, *fakeHeimdall, *fakeL1, *fakeAncestry) {
	t.Helper()
	// One canonical Bor chain high enough for the checkpoint range
	// 20001..30000 and the head above it: each block's parent is the
	// previous one.
	const chainTop = 30010
	chain := &borChain{heights: map[uint64]block.Hash32{}, parents: map[block.Hash32]block.Hash32{}}
	for i := 1; i <= chainTop; i++ {
		h := mustHash(fmtHash(i))
		chain.heights[uint64(i)] = h
		if i > 1 {
			chain.parents[h] = mustHash(fmtHash(i - 1))
		}
	}
	heimdall := &fakeHeimdall{
		storeID:          mustID("heimdall-store"),
		identity:         testHeimdallIdentity,
		chain:            ChainParams{RootChainAddress: testRootChain},
		checkpointParams: CheckpointParams{ChildBlockInterval: testInterval},
		checkpoint: SourceCheckpoint{
			CheckpointID:     block.Some(testCheckpointID),
			Proposer:         testProposer,
			Start:            testBorStart,
			End:              testBorEnd,
			Timestamp:        1234,
			RootHash:         testCheckpointRt,
			State:            CheckpointStoredAcknowledged,
			LocalVerifiedEnd: block.None[block.AnchorRef](),
		},
		checkpointResp: Response{Bytes: []byte(`{"checkpoint":"full"}`), RequestSequence: 7},
		milestone: SourceMilestone{
			MilestoneID:     "ms-2",
			Proposer:        testProposer,
			Start:           29501,
			End:             29900,
			Timestamp:       1200,
			TotalDifficulty: 99,
			EndHash:         chain.heights[29900],
			Outcome:         MilestoneLocallyVerified,
		},
		milestoneResp: Response{Bytes: []byte(`{"milestone":"full"}`), RequestSequence: 6},
	}
	l1 := &fakeL1{
		nodeID:  mustID("l1-node"),
		network: testL1Network,
		finalized: block.AnchorRef{
			Height: 500,
			Hash:   mustHash("l1-finalized-500"),
		},
		result: headerBlockResult(testBorStart, testBorEnd, 42, testCheckpointRt, testProposer),
	}
	ancestry := &fakeAncestry{chain: chain}
	sources, err := ParseSources(testSources(""))
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	observer, err := NewObserver(store, sources, heimdall, l1, ancestry)
	if err != nil {
		t.Fatal(err)
	}
	return observer, store, heimdall, l1, ancestry
}

func fmtHash(i int) string {
	return fmt.Sprintf("bor-block-%d", i)
}

// fixtureHead is the observed execution head of the fixture: above the
// checkpoint end, on the canonical chain.
func fixtureHead() block.AnchorRef {
	return block.AnchorRef{Height: 30005, Hash: mustHash(fmtHash(30005))}
}

func testDecision(seq uint64) block.NativeDecisionRef {
	return block.NativeDecisionRef{
		NodeInstanceID:   mustID("node-instance"),
		ProducerID:       mustID("producer"),
		RunID:            mustID("run"),
		DecisionSequence: seq,
	}
}

// AC-LIF-07-02: with consistent supporting identities, checkpoint and
// finalized L1 F, the exact hash-pinned RootChain read and the native range
// verification produce verified contract fields, root and end ancestry
// bound to F and the durable anchor; latest/safe never substitute F.
func TestObserveL1VerifiesEvidenceBoundToF(t *testing.T) {
	observer, store, _, l1, _ := newTestFixture(t)
	head := fixtureHead()

	obs, err := observer.ObserveL1(testDecision(1), head, block.OriginNativeLive)
	if err != nil {
		t.Fatal(err)
	}
	store.Sync()

	ev, ok := obs.Evidence.L1Checkpoint.Get()
	if !ok {
		t.Fatal("l1 checkpoint evidence must be known")
	}
	if ev.FinalizedL1Anchor.Hash != mustHash("l1-finalized-500") {
		t.Fatalf("evidence bound to %s, want the finalized F", ev.FinalizedL1Anchor.Hash)
	}
	if ev.Verification.CallBlockHash != ev.FinalizedL1Anchor.Hash {
		t.Fatal("verification call block hash must be F")
	}
	// The read is exactly the checked header block id at F.
	id, err := HeaderBlockID(testCheckpointID, testInterval)
	if err != nil {
		t.Fatal(err)
	}
	if ev.HeaderBlockID != id {
		t.Fatalf("header block id %s, want %s", ev.HeaderBlockID, id)
	}
	calls := l1.calls()
	if len(calls) != 1 {
		t.Fatalf("header blocks called %d times, want 1", len(calls))
	}
	if !bytes.Equal(calls[0].calldata, HeaderBlocksCalldata(id)) {
		t.Fatal("calldata must be the closed selector and the u256 id")
	}
	if calls[0].at.Hash != mustHash("l1-finalized-500") {
		t.Fatal("the read must run at the finalized F, never latest/safe")
	}
	if ev.CheckpointRoot != testCheckpointRt {
		t.Fatal("checkpoint root must be cross-checked")
	}
	if ev.BorEndRef.Hash == testCheckpointRt {
		t.Fatal("the checkpoint root is a range commitment, never the end block hash")
	}
	if ev.BorRangeDigest != BorRangeDigest(ev.BorStart, ev.BorEnd, ev.BorEndRef) {
		t.Fatal("bor range binding digest must bind the verified range")
	}
	frontier, ok, err := store.Frontier()
	if err != nil || !ok {
		t.Fatalf("frontier saved: ok=%v err=%v", ok, err)
	}
	if frontier.BorEndRef != ev.BorEndRef || frontier.FinalizedL1Anchor != ev.FinalizedL1Anchor {
		t.Fatal("verified frontier must equal the accepted evidence")
	}
}

// AC-LIF-07-03: with ready tx/status and the L1 source offline, syncing, or
// the state at F pruned, the GC lookup returns the matching Unavailable
// variant, keeps the accepted anchor and the publication markers continue.
func TestObserveL1UnavailableVariants(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason block.UnavailableReason
		set    func(*fakeHeimdall, *fakeL1)
	}{
		{"l1 offline", block.UnavailableSourceOffline, func(_ *fakeHeimdall, l1 *fakeL1) {
			l1.setFinErr(Unavailable(block.UnavailableSourceOffline))
		}},
		{"l1 syncing", block.UnavailableSourceSyncing, func(_ *fakeHeimdall, l1 *fakeL1) {
			l1.setFinErr(Unavailable(block.UnavailableSourceSyncing))
		}},
		{"heimdall offline", block.UnavailableSourceOffline, func(h *fakeHeimdall, _ *fakeL1) {
			h.setCheckpointErr(Unavailable(block.UnavailableSourceOffline))
		}},
		{"pruned state at F", block.UnavailableStateUnavailable, func(_ *fakeHeimdall, l1 *fakeL1) {
			l1.setHeaderErr(Unavailable(block.UnavailableStateUnavailable))
		}},
		{"checkpoint pending", block.UnavailableCheckpointPending, func(h *fakeHeimdall, _ *fakeL1) {
			cp := h.checkpoint
			cp.CheckpointID = block.None[uint64]()
			h.setCheckpoint(cp)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observer, store, heimdall, l1, _ := newTestFixture(t)
			head := fixtureHead()
			// One accepted anchor exists before the outage.
			accepted, err := observer.ObserveL1(testDecision(1), head, block.OriginNativeLive)
			if err != nil {
				t.Fatal(err)
			}
			store.Sync()
			acceptedEv, ok := accepted.Evidence.L1Checkpoint.Get()
			if !ok {
				t.Fatal("setup: first lookup must be known")
			}
			// The checkpoint advances to a newer range; the outage must
			// not invent an anchor for it.
			tc.set(heimdall, l1)

			obs, err := observer.ObserveL1(testDecision(2), head, block.OriginNativeLive)
			if err != nil {
				t.Fatal(err)
			}
			if obs.Evidence.L1Checkpoint.Kind != EvidenceUnavailable {
				t.Fatalf("l1 evidence %v, want UNAVAILABLE", obs.Evidence.L1Checkpoint.Kind)
			}
			store.Sync()
			if obs.Evidence.L1Checkpoint.Reason != tc.reason {
				t.Fatalf("unavailable reason %s, want %s", obs.Evidence.L1Checkpoint.Reason, tc.reason)
			}
			// GC waits: the accepted anchor is not erased.
			frontier, ok, err := store.Frontier()
			if err != nil || !ok || frontier != acceptedEv {
				t.Fatalf("accepted anchor must stay: ok=%v frontier=%v", ok, frontier == acceptedEv)
			}
			// Publication continues: the markers carry the explicit
			// availability and the native projection still works.
			markers, err := observer.Markers()
			if err != nil {
				t.Fatal(err)
			}
			if markers.L1Checkpoint.Kind != block.ReferenceUnavailable {
				t.Fatalf("l1 marker %v, want UNAVAILABLE", markers.L1Checkpoint.Kind)
			}
			native, err := observer.ObserveNative(NativeInput{Head: head, Decision: testDecision(3), Origin: block.OriginNativeLive})
			if err != nil {
				t.Fatal(err)
			}
			// Carried forward: still unavailable, and the retained anchor
			// of the frontier stays accepted for GC.
			if native.Evidence.L1Checkpoint.Kind != EvidenceUnavailable {
				t.Fatalf("carried l1 evidence %v, want UNAVAILABLE", native.Evidence.L1Checkpoint.Kind)
			}
		})
	}
}

// AC-LIF-07-04: with the binding known, a substituted chain, contract,
// params or a malformed RootChain result is an explicit source/identity
// error, never an Unavailable-success and never an accepted anchor.
func TestObserveL1IdentityAndMalformedErrors(t *testing.T) {
	head := fixtureHead()
	for _, tc := range []struct {
		name string
		set  func(*fakeHeimdall, *fakeL1)
		want error
	}{
		{"wrong l1 network", func(_ *fakeHeimdall, l1 *fakeL1) {
			l1.network = block.NetworkIdentity{Family: "EVM", ChainID: block.UInt256(mustHash("other-chain")), GenesisHash: mustHash("other-genesis")}
		}, ErrIdentity},
		{"wrong heimdall identity", func(h *fakeHeimdall, _ *fakeL1) {
			h.identity = HeimdallIdentity{ChainID: "heimdall-other", GenesisDocumentSHA256: mustHash("other")}
		}, ErrIdentity},
		{"root mismatch", func(_ *fakeHeimdall, l1 *fakeL1) {
			l1.setResult(headerBlockResult(testBorStart, testBorEnd, 42, mustHash("wrong-root"), testProposer))
		}, ErrIdentity},
		{"range mismatch", func(_ *fakeHeimdall, l1 *fakeL1) {
			l1.setResult(headerBlockResult(testBorStart, testBorEnd+1, 42, testCheckpointRt, testProposer))
		}, ErrIdentity},
		{"proposer mismatch", func(_ *fakeHeimdall, l1 *fakeL1) {
			l1.setResult(headerBlockResult(testBorStart, testBorEnd, 42, testCheckpointRt, mustAddress("wrong-proposer")))
		}, ErrIdentity},
		{"malformed short result", func(_ *fakeHeimdall, l1 *fakeL1) {
			l1.setResult([]byte{1, 2, 3})
		}, ErrMalformed},
		{"malformed long result", func(_ *fakeHeimdall, l1 *fakeL1) {
			l1.setResult(make([]byte, HeaderBlockWordCount*32+32))
		}, ErrMalformed},
		{"malformed dirty u64", func(_ *fakeHeimdall, l1 *fakeL1) {
			raw := headerBlockResult(testBorStart, testBorEnd, 42, testCheckpointRt, testProposer)
			raw[0] = 1
			l1.setResult(raw)
		}, ErrMalformed},
		{"params drift during read", func(h *fakeHeimdall, l1 *fakeL1) {
			l1.onHeader = func() { h.checkpointParams.ChildBlockInterval = testInterval + 1 }
		}, ErrIdentity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observer, store, heimdall, l1, _ := newTestFixture(t)
			tc.set(heimdall, l1)
			_, err := observer.ObserveL1(testDecision(1), head, block.OriginNativeLive)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error %v, want %v", err, tc.want)
			}
			// No observation was appended and no anchor was accepted.
			observations, err := store.Observations()
			if err != nil {
				t.Fatal(err)
			}
			if len(observations) != 0 {
				t.Fatalf("%d observations after a hard error, want 0", len(observations))
			}
			if _, ok, _ := store.Frontier(); ok {
				t.Fatal("no anchor may be accepted from a rejected source")
			}
		})
	}
}

// AC-LIF-07-05: a previous observation of an unavailable state F followed
// by a newer finalized F2 creates a new hash-pinned observation bound to
// F2; the earlier observation and its F are not rewritten.
func TestObserveL1NewerFinalizedCreatesNewObservation(t *testing.T) {
	observer, store, _, l1, _ := newTestFixture(t)
	head := fixtureHead()

	// Pruned state at F: one unavailable observation.
	l1.setHeaderErr(Unavailable(block.UnavailableStateUnavailable))
	first, err := observer.ObserveL1(testDecision(1), head, block.OriginNativeLive)
	if err != nil {
		t.Fatal(err)
	}
	store.Sync()

	// A newer finalized F2 arrives; the state at F2 is available.
	l1.setHeaderErr(nil)
	l1.setFinalized(block.AnchorRef{Height: 640, Hash: mustHash("l1-finalized-640")})
	second, err := observer.ObserveL1(testDecision(2), head, block.OriginNativeLive)
	if err != nil {
		t.Fatal(err)
	}
	store.Sync()

	if first.Evidence.L1Checkpoint.Kind != EvidenceUnavailable {
		t.Fatal("first observation must record the unavailable state")
	}
	secondEv, ok := second.Evidence.L1Checkpoint.Get()
	if !ok {
		t.Fatal("second observation must carry verified evidence")
	}
	if secondEv.FinalizedL1Anchor.Height != 640 {
		t.Fatalf("second observation bound to F2 at %d, want 640", secondEv.FinalizedL1Anchor.Height)
	}
	observations, err := store.Observations()
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 2 {
		t.Fatalf("%d observations, want 2 (append-only stream)", len(observations))
	}
	replayed, err := observer.Resend(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Evidence.L1Checkpoint.Kind != EvidenceUnavailable {
		t.Fatal("the earlier observation must keep its unavailable state")
	}
}

// A candidate whose Bor range conflicts with the verified frontier is a
// source invariant error, and a below-frontier candidate never lowers the
// anchor.
func TestObserveL1FrontierMonotonicity(t *testing.T) {
	head := fixtureHead()

	t.Run("conflict at the verified anchor", func(t *testing.T) {
		observer, store, heimdall, l1, _ := newTestFixture(t)
		if _, err := observer.ObserveL1(testDecision(1), head, block.OriginNativeLive); err != nil {
			t.Fatal(err)
		}
		store.Sync()
		// Same range, different root: a conflicting branch.
		cp := heimdall.checkpoint
		cp.RootHash = mustHash("conflicting-root")
		heimdall.setCheckpoint(cp)
		l1.setResult(headerBlockResult(testBorStart, testBorEnd, 42, mustHash("conflicting-root"), testProposer))
		if _, err := observer.ObserveL1(testDecision(2), head, block.OriginNativeLive); !errors.Is(err, ErrInvariant) {
			t.Fatalf("error %v, want ErrInvariant", err)
		}
	})

	t.Run("candidate below the verified anchor", func(t *testing.T) {
		observer, store, heimdall, l1, _ := newTestFixture(t)
		if _, err := observer.ObserveL1(testDecision(1), head, block.OriginNativeLive); err != nil {
			t.Fatal(err)
		}
		store.Sync()
		// An older checkpoint re-queried: below the verified end.
		cp := heimdall.checkpoint
		cp.CheckpointID = block.Some(uint64(2))
		cp.Start = testBorStart - 10000
		cp.End = testBorEnd - 10000
		heimdall.setCheckpoint(cp)
		l1.setResult(headerBlockResult(cp.Start, cp.End, 42, testCheckpointRt, testProposer))
		if _, err := observer.ObserveL1(testDecision(2), head, block.OriginNativeLive); !errors.Is(err, ErrInvariant) {
			t.Fatalf("error %v, want ErrInvariant", err)
		}
		frontier, ok, _ := store.Frontier()
		if !ok || frontier.BorEnd != testBorEnd {
			t.Fatal("the frontier must not be lowered by a below-anchor candidate")
		}
	})

	t.Run("same anchor newer F is a new observation", func(t *testing.T) {
		observer, store, _, l1, _ := newTestFixture(t)
		if _, err := observer.ObserveL1(testDecision(1), head, block.OriginNativeLive); err != nil {
			t.Fatal(err)
		}
		store.Sync()
		l1.setFinalized(block.AnchorRef{Height: 520, Hash: mustHash("l1-finalized-520")})
		obs, err := observer.ObserveL1(testDecision(2), head, block.OriginNativeLive)
		if err != nil {
			t.Fatal(err)
		}
		store.Sync()
		ev, _ := obs.Evidence.L1Checkpoint.Get()
		if ev.FinalizedL1Anchor.Height != 520 {
			t.Fatal("the same anchor at a newer F must be a new observation")
		}
		observations, _ := store.Observations()
		if len(observations) != 2 {
			t.Fatalf("%d observations, want 2", len(observations))
		}
	})
}

// The finalized L1 anchor F must still be canonical after the read: a
// non-canonical F is an invariant error, not an anchor.
func TestObserveL1FMustStayCanonical(t *testing.T) {
	observer, _, _, l1, _ := newTestFixture(t)
	l1.ancestorErr = errors.New("ancestor lookup failed")
	head := fixtureHead()
	if _, err := observer.ObserveL1(testDecision(1), head, block.OriginNativeLive); err == nil {
		t.Fatal("a failed canonical ancestry check must not produce evidence")
	}
}

// The native Bor verifier failure is a hard error: the root recomputation
// over the continuous canonical range is mandatory before any evidence.
func TestObserveL1NativeVerifierRequired(t *testing.T) {
	observer, store, _, _, ancestry := newTestFixture(t)
	ancestry.verifyErr = errors.New("range not continuous")
	head := fixtureHead()
	if _, err := observer.ObserveL1(testDecision(1), head, block.OriginNativeLive); err == nil {
		t.Fatal("a failed native range verification must not produce evidence")
	}
	if _, ok, _ := store.Frontier(); ok {
		t.Fatal("no frontier may be accepted without the native verification")
	}
}
