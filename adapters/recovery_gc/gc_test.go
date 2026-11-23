package recoverygc

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// errCrash reports the simulated power loss of a store that stops syncing.
var errCrash = errors.New("simulated power loss")

// memStore is the memory-backed durable store of the tests, with the
// durability contract of the Store seam.
type memStore struct {
	mu      sync.Mutex
	durable map[string][]byte
	pending []write
	syncs   int
	failAt  int
}

type write struct {
	key   string
	value []byte
}

func newStore() *memStore { return &memStore{durable: map[string][]byte{}} }

func (s *memStore) view(key string) ([]byte, bool) {
	for i := len(s.pending) - 1; i >= 0; i-- {
		if s.pending[i].key == key {
			return s.pending[i].value, true
		}
	}
	value, ok := s.durable[key]
	return value, ok
}

func (s *memStore) Has(key []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.view(string(key))
	return ok, nil
}

func (s *memStore) Get(key []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.view(string(key))
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return bytes.Clone(value), nil
}

func (s *memStore) Put(key, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, write{key: string(key), value: bytes.Clone(value)})
	return nil
}

func (s *memStore) SyncKeyValue() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncs++
	if s.failAt != 0 && s.syncs > s.failAt {
		return errCrash
	}
	for _, w := range s.pending {
		s.durable[w.key] = w.value
	}
	s.pending = nil
	return nil
}

func (s *memStore) Range(prefix []byte, visit func(key, value []byte) error) error {
	s.mu.Lock()
	keys := make([]string, 0, len(s.durable)+len(s.pending))
	merged := map[string][]byte{}
	for key, value := range s.durable {
		if bytes.HasPrefix([]byte(key), prefix) {
			keys = append(keys, key)
			merged[key] = value
		}
	}
	for _, w := range s.pending {
		if bytes.HasPrefix([]byte(w.key), prefix) {
			if _, seen := merged[w.key]; !seen {
				keys = append(keys, w.key)
			}
			merged[w.key] = w.value
		}
	}
	s.mu.Unlock()
	sort.Strings(keys)
	for _, key := range keys {
		if err := visit([]byte(key), bytes.Clone(merged[key])); err != nil {
			return err
		}
	}
	return nil
}

// powerLoss returns the store a restart would see.
func (s *memStore) powerLoss(keep uint) *memStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &memStore{durable: maps.Clone(s.durable)}
	for i, w := range s.pending {
		if keep&(1<<i) != 0 {
			out.durable[w.key] = w.value
		}
	}
	return out
}

func (s *memStore) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := slices.Collect(maps.Keys(s.durable))
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, fmt.Sprintf("%s=%s", key, s.durable[key]))
	}
	return out
}

// chainOracle proves ancestry over one pinned canonical chain; unknownFor
// forces the unknown verdict for selected pairs.
type chainOracle struct {
	chain      []AnchorRef
	unknownFor func(ancestor, descendant AnchorRef) bool
}

func (o *chainOracle) Contains(ancestor, descendant AnchorRef) Verdict {
	if o.unknownFor != nil && o.unknownFor(ancestor, descendant) {
		return VerdictUnknown
	}
	if descendant.Height >= uint64(len(o.chain)) ||
		o.chain[descendant.Height].Hash != descendant.Hash {
		return VerdictDivergent
	}
	if ancestor.Height > descendant.Height || ancestor.Height >= uint64(len(o.chain)) {
		return VerdictDivergent
	}
	if o.chain[ancestor.Height].Hash == ancestor.Hash {
		return VerdictContains
	}
	return VerdictDivergent
}

// pendingMap is a PendingOracle over one map.
type pendingMap struct {
	mu      sync.Mutex
	pending map[EntityID]PendingReport
}

func (p *pendingMap) Pending(entity EntityID) (PendingReport, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pending[entity], nil
}

// fakeReleaser counts the effective entity drops.
type fakeReleaser struct {
	mu          sync.Mutex
	held        map[EntityID]bool
	transitions int
	fail        error
}

func newReleaser() *fakeReleaser { return &fakeReleaser{held: map[EntityID]bool{}} }

func (f *fakeReleaser) ReleaseEntity(id EntityID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	if f.held[id] {
		return nil
	}
	f.held[id] = true
	f.transitions++
	return nil
}

// fakePins counts the effective pin releases.
type fakePins struct {
	mu       sync.Mutex
	held     map[string]bool
	releases int
	fail     error
}

func newPins() *fakePins { return &fakePins{held: map[string]bool{}} }

func (f *fakePins) Release(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	if !f.held[id] {
		return nil
	}
	delete(f.held, id)
	f.releases++
	return nil
}

func (f *fakePins) hold(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held[id] = true
}

// Fixtures.
var (
	gcNet      = semantic.Network{ChainID: gcU256(1), GenesisHash: gcH32(1)}
	gcNode     = gcID16(0x33)
	gcProducer = gcID16(0x11)
	gcRun      = gcID16(0x22)
)

func gcU256(n byte) semantic.UInt256 {
	var out semantic.UInt256
	out[31] = n
	return out
}

func gcH32(n byte) semantic.Hash32 {
	var out semantic.Hash32
	for i := range out {
		out[i] = n + byte(i)
	}
	return out
}

func gcID16(n byte) semantic.ID16 {
	var out semantic.ID16
	for i := range out {
		out[i] = n + byte(i)
	}
	return out
}

func gcRef(sequence uint64) NativeDecisionRef {
	return NativeDecisionRef{NodeInstanceID: gcNode, ProducerID: gcProducer, RunID: gcRun,
		DecisionSequence: sequence}
}

// chain builds a canonical chain whose block at height i hashes from seed.
func chain(length int) []AnchorRef {
	out := make([]AnchorRef, length)
	for i := range out {
		out[i] = AnchorRef{Height: uint64(i), Hash: gcH32(byte(0xB0 + i))}
	}
	return out
}

func blockAt(height int) AnchorRef {
	return AnchorRef{Height: uint64(height), Hash: gcH32(byte(0xB0 + height))}
}

func offChain(height int) AnchorRef {
	return AnchorRef{Height: uint64(height), Hash: gcH32(byte(0xE0 + height))}
}

func entityOf(sequence, ordinal uint64, block AnchorRef, branch Branch, applied bool,
	pins ...string) Entity {
	return Entity{ID: EntityID{Network: gcNet, ProducerID: gcProducer, RunID: gcRun,
		DecisionSequence: sequence, EntityOrdinal: ordinal}, Block: block, Branch: branch,
		Applied: applied, Pins: pins}
}

func evidenceDigest(seed byte) string { return gcH32(seed).String() }

type harness struct {
	db       *memStore
	oracle   *chainOracle
	pending  *pendingMap
	releaser *fakeReleaser
	pins     *fakePins
}

func newHarness(t *testing.T, chainLength int) *harness {
	t.Helper()
	h := &harness{db: newStore(), oracle: &chainOracle{chain: chain(chainLength)},
		pending: &pendingMap{pending: map[EntityID]PendingReport{}}, releaser: newReleaser(),
		pins: newPins()}
	h.pins.hold("P0")
	h.pins.hold("P1")
	h.pins.hold("P2")
	return h
}

func (h *harness) engine(t *testing.T) (*Engine, error) {
	t.Helper()
	e, _, err := Open(h.db, h.oracle, h.pending, h.releaser, h.pins)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func openEngine(t *testing.T, h *harness) *Engine {
	t.Helper()
	e, err := h.engine(t)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return e
}

// commitAnchorAt observes and commits the anchor at one height.
func commitAnchorAt(t *testing.T, e *Engine, height int) AcceptedGcAnchor {
	t.Helper()
	result, err := e.ObserveFinality(FinalityObservation{Network: gcNet, Decision: gcRef(uint64(height)),
		Anchor: blockAt(height), EvidenceDigest: evidenceDigest(byte(height)),
		ProfileRef: "eth-mainnet"})
	if err != nil {
		t.Fatalf("observe %d: %v", height, err)
	}
	if result != ObservationCandidate {
		t.Fatalf("observe %d: %s", height, result)
	}
	anchor, err := e.CommitAnchor(gcNet)
	if err != nil {
		t.Fatalf("commit %d: %v", height, err)
	}
	return anchor
}

func decisionsOf(t *testing.T, e *Engine, entities ...Entity) []Decision {
	t.Helper()
	decisions, err := e.Evaluate(entities)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	return decisions
}

func reasonLines(decisions []Decision) []string {
	out := make([]string, len(decisions))
	for i, d := range decisions {
		out[i] = string(d.Reason)
	}
	return out
}

func TestNoAnchorRetainsEverything(t *testing.T) {
	h := newHarness(t, 20)
	e := openEngine(t, h)
	entities := []Entity{
		entityOf(1, 0, blockAt(5), BranchCanonical, true, "P0"),
		entityOf(2, 0, offChain(6), BranchNoncanonical, false, "P1"),
	}
	decisions := decisionsOf(t, e, entities...)
	if !slices.Equal(reasonLines(decisions), []string{string(ReasonRetainNoAnchor),
		string(ReasonRetainNoAnchor)}) {
		t.Fatalf("reasons %v", reasonLines(decisions))
	}
	if _, err := e.Release(entities, nil); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("release without anchor: %v", err)
	}
	if h.pins.releases != 0 {
		t.Fatalf("released %d pins without an anchor", h.pins.releases)
	}
	if h.releaser.transitions != 0 {
		t.Fatalf("dropped %d entities without an anchor", h.releaser.transitions)
	}
}

func TestPendingWorkRetains(t *testing.T) {
	h := newHarness(t, 20)
	e := openEngine(t, h)
	commitAnchorAt(t, e, 10)
	covered := entityOf(1, 0, blockAt(5), BranchCanonical, true, "P1")
	for _, report := range []PendingReport{
		{Capture: true}, {Transition: true}, {Replay: true}, {Delivery: true},
	} {
		h.pending.pending[covered.ID] = report
		decisions := decisionsOf(t, e, covered)
		if decisions[0].Reason != ReasonRetainPending || decisions[0].Pending != report {
			t.Fatalf("pending %s decision %+v", report, decisions[0])
		}
		if _, err := e.Release([]Entity{covered}, nil); !errors.Is(err, ErrNotEligible) {
			t.Fatalf("released with pending %s: %v", report, err)
		}
		if h.pins.releases != 0 {
			t.Fatalf("pin released with pending %s", report)
		}
	}
	delete(h.pending.pending, covered.ID)
	decisions := decisionsOf(t, e, covered)
	if decisions[0].Reason != ReasonReleaseCovered || !decisions[0].Release {
		t.Fatalf("clean decision %+v", decisions[0])
	}
}

func TestCoverageIsAncestryNotHeight(t *testing.T) {
	h := newHarness(t, 20)
	e := openEngine(t, h)
	commitAnchorAt(t, e, 10)
	// A block below the anchor height without proven ancestry is retained;
	// the height alone never authorizes a cleanup (AC-LIF-08-01).
	h.oracle.unknownFor = func(ancestor, descendant AnchorRef) bool {
		return ancestor.Hash == blockAt(5).Hash || ancestor.Hash == offChain(6).Hash
	}
	unknownCanonical := entityOf(1, 0, blockAt(5), BranchCanonical, true)
	unknownSide := entityOf(2, 0, offChain(6), BranchNoncanonical, false)
	decisions := decisionsOf(t, e, unknownCanonical, unknownSide)
	if !slices.Equal(reasonLines(decisions), []string{string(ReasonRetainAncestryUnknown),
		string(ReasonRetainAncestryUnknown)}) {
		t.Fatalf("reasons %v", reasonLines(decisions))
	}
	// Proven divergence of a canonical block also retains it.
	h.oracle.unknownFor = nil
	divergent := entityOf(3, 0, offChain(5), BranchCanonical, true)
	if got := decisionsOf(t, e, divergent)[0].Reason; got != ReasonRetainNotCovered {
		t.Fatalf("divergent canonical reason %s", got)
	}
}

func TestStaleObservationIsNoFalseFatal(t *testing.T) {
	h := newHarness(t, 20)
	e := openEngine(t, h)
	accepted := commitAnchorAt(t, e, 10)

	// A delayed observation below the anchor is stale: refresh, no fatal,
	// and the anchor is not demoted (AC-LIF-08-03).
	result, err := e.ObserveFinality(FinalityObservation{Network: gcNet, Decision: gcRef(8),
		Anchor: blockAt(8), EvidenceDigest: evidenceDigest(8), ProfileRef: "eth-mainnet"})
	if err != nil || result != ObservationStale {
		t.Fatalf("stale observation: %s %v", result, err)
	}
	// So is an observation of another branch above the anchor.
	result, err = e.ObserveFinality(FinalityObservation{Network: gcNet, Decision: gcRef(12),
		Anchor: offChain(12), EvidenceDigest: evidenceDigest(12), ProfileRef: "eth-mainnet"})
	if err != nil || result != ObservationStale {
		t.Fatalf("divergent observation: %s %v", result, err)
	}
	// Unavailable ancestry evidence records no candidate.
	h.oracle.unknownFor = func(ancestor, descendant AnchorRef) bool { return true }
	result, err = e.ObserveFinality(FinalityObservation{Network: gcNet, Decision: gcRef(15),
		Anchor: blockAt(15), EvidenceDigest: evidenceDigest(15), ProfileRef: "eth-mainnet"})
	if err != nil || result != ObservationUnavailable {
		t.Fatalf("unavailable observation: %s %v", result, err)
	}
	h.oracle.unknownFor = nil

	if anchor, ok := e.Anchor(gcNet); !ok || anchor != accepted {
		t.Fatalf("anchor changed to %+v", anchor)
	}
	// The engine is alive and the anchor still governs cleanup.
	covered := entityOf(1, 0, blockAt(5), BranchCanonical, true)
	if got := decisionsOf(t, e, covered)[0].Reason; got != ReasonReleaseCovered {
		t.Fatalf("covered reason %s", got)
	}
	if err := e.Authoritative(CanonicalDecision{Network: gcNet, Decision: gcRef(1),
		Head: blockAt(12)}); err != nil {
		t.Fatalf("authoritative decision after stale observations: %v", err)
	}
	// An observation without evidence is refused: an outage cannot invent
	// an anchor.
	if _, err := e.ObserveFinality(FinalityObservation{Network: gcNet, Anchor: blockAt(15),
		ProfileRef: "eth-mainnet"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invented anchor: %v", err)
	}
}

func TestUnacceptedCandidateAuthorizesNothing(t *testing.T) {
	h := newHarness(t, 20)
	e := openEngine(t, h)
	accepted := commitAnchorAt(t, e, 10)

	// A computed candidate is in memory only: its acceptance is a separate
	// sync decision, and a power loss before it keeps the previous anchor
	// (AC-LIF-08-04).
	if result, err := e.ObserveFinality(FinalityObservation{Network: gcNet, Decision: gcRef(15),
		Anchor: blockAt(15), EvidenceDigest: evidenceDigest(15), ProfileRef: "eth-mainnet"}); err != nil ||
		result != ObservationCandidate {
		t.Fatalf("candidate: %s %v", result, err)
	}
	restarted := openEngine(t, &harness{db: h.db.powerLoss(0), oracle: h.oracle, pending: h.pending,
		releaser: h.releaser, pins: h.pins})
	if anchor, ok := restarted.Anchor(gcNet); !ok || anchor != accepted {
		t.Fatalf("anchor changed to %+v", anchor)
	}
	above := entityOf(1, 0, blockAt(12), BranchCanonical, true)
	if got := decisionsOf(t, restarted, above)[0].Reason; got != ReasonRetainNotCovered {
		t.Fatalf("unaccepted candidate released: %s", got)
	}

	// After the re-observation and the commit, the same entity releases.
	commitAnchorAt(t, restarted, 15)
	if got := decisionsOf(t, restarted, above)[0].Reason; got != ReasonReleaseCovered {
		t.Fatalf("covered reason after commit: %s", got)
	}

	// An observation of another branch never becomes a candidate, and the
	// engine stays alive.
	if result, err := restarted.ObserveFinality(FinalityObservation{Network: gcNet,
		Decision: gcRef(20), Anchor: offChain(20), EvidenceDigest: evidenceDigest(20),
		ProfileRef: "eth-mainnet"}); err != nil || result != ObservationStale {
		t.Fatalf("divergent observation: %s %v", result, err)
	}
	if _, err := restarted.CommitAnchor(gcNet); !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("stale observation became a candidate: %v", err)
	}
	if _, err := restarted.Evaluate([]Entity{above}); err != nil {
		t.Fatalf("engine stopped by a stale observation: %v", err)
	}
}

// releaseScenario releases one covered applied entity and one covered
// removed entity while a shared pin and a pending delivery entity stay.
func releaseScenario(t *testing.T) (*harness, *Engine, []Entity, ReleaseResult) {
	t.Helper()
	h := newHarness(t, 25)
	e := openEngine(t, h)
	commitAnchorAt(t, e, 20)
	removed := entityOf(1, 0, blockAt(7), BranchCanonical, true, "P0")
	removed.Terminal = &TerminalEvidence{Revision: 1, Decision: gcRef(2)}
	applied := entityOf(3, 0, blockAt(5), BranchCanonical, true, "P0", "P1")
	pendingDelivery := entityOf(4, 0, blockAt(8), BranchCanonical, true, "P0")
	h.pending.pending[pendingDelivery.ID] = PendingReport{Delivery: true}
	entities := []Entity{removed, applied}
	result, err := e.Release(entities, []string{"P0"})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	return h, e, entities, result
}

func TestReleaseRetainsSharedPinsAndPending(t *testing.T) {
	h, _, _, result := releaseScenario(t)
	if len(result.Released) != 2 {
		t.Fatalf("released %v", result.Released)
	}
	if !slices.Equal(result.ReleasedPins, []string{"P1"}) {
		t.Fatalf("released pins %v, want the unshared P1", result.ReleasedPins)
	}
	if !slices.Equal(result.RetainedPins, []string{"P0"}) {
		t.Fatalf("retained pins %v", result.RetainedPins)
	}
	if !h.pins.held["P0"] || h.pins.held["P1"] {
		t.Fatalf("pin state %+v", h.pins.held)
	}
	if h.pins.releases != 1 {
		t.Fatalf("pin releases %d", h.pins.releases)
	}
	// The compact records remain for every released entity.
	tombstones, err := openEngine(t, h).Tombstones()
	if err != nil {
		t.Fatalf("tombstones: %v", err)
	}
	if len(tombstones) != 2 {
		t.Fatalf("tombstones %d", len(tombstones))
	}
	for _, tombstone := range tombstones {
		if tombstone.Anchor != blockAt(20) {
			t.Fatalf("tombstone anchor %s", tombstone.Anchor)
		}
	}
}

func TestReleaseRestartCompletesWithoutDoubleApplication(t *testing.T) {
	// The golden uninterrupted release.
	goldenH, _, _, goldenResult := releaseScenario(t)
	goldenRecords := goldenH.db.snapshot()
	goldenReleases := goldenH.pins.releases
	goldenTransitions := goldenH.releaser.transitions

	// Every crash point of the same release, with every subset of the
	// unsynced writes kept: the restart completes the release exactly once
	// (AC-LIF-08-02).
	totalSyncs := goldenH.db.syncs
	for failAt := 0; failAt <= totalSyncs; failAt++ {
		for keep := uint(0); keep < 2; keep++ {
			h := newHarness(t, 25)
			h.db.failAt = failAt
			e := openEngine(t, h)
			commitAnchorAt(t, e, 20)
			removed := entityOf(1, 0, blockAt(7), BranchCanonical, true, "P0")
			removed.Terminal = &TerminalEvidence{Revision: 1, Decision: gcRef(2)}
			applied := entityOf(3, 0, blockAt(5), BranchCanonical, true, "P0", "P1")
			pendingDelivery := entityOf(4, 0, blockAt(8), BranchCanonical, true, "P0")
			h.pending.pending[pendingDelivery.ID] = PendingReport{Delivery: true}
			_, err := e.Release([]Entity{removed, applied}, []string{"P0"})
			if failAt < totalSyncs && !errors.Is(err, errCrash) && err != nil {
				t.Fatalf("failAt %d: release stopped with %v", failAt, err)
			}
			survived := h.db.powerLoss(keep)
			_, hasRelease := viewOf(survived, keyPrefix+"release/")
			restartedH := &harness{db: survived, oracle: h.oracle, pending: h.pending,
				releaser: h.releaser, pins: h.pins}
			restarted, err := restartedH.engine(t)
			if err != nil {
				t.Fatalf("failAt %d keep %d: reopen %v", failAt, keep, err)
			}
			if hasRelease {
				if !slices.Equal(survived.snapshot(), goldenRecords) {
					t.Fatalf("failAt %d keep %d: records differ from golden", failAt, keep)
				}
				if h.releaser.transitions != goldenTransitions {
					t.Fatalf("failAt %d keep %d: %d entity drops, want %d", failAt, keep,
						h.releaser.transitions, goldenTransitions)
				}
				if h.pins.releases != goldenReleases {
					t.Fatalf("failAt %d keep %d: %d pin releases, want %d", failAt, keep,
						h.pins.releases, goldenReleases)
				}
				// A repeated release of the same plan applies nothing.
				if _, err := restarted.Release([]Entity{removed, applied}, []string{"P0"}); err != nil {
					t.Fatalf("failAt %d keep %d: repeated release: %v", failAt, keep, err)
				}
				if h.releaser.transitions != goldenTransitions || h.pins.releases != goldenReleases {
					t.Fatalf("failAt %d keep %d: repeated release applied twice", failAt, keep)
				}
			} else if h.releaser.transitions != 0 || h.pins.releases != 0 {
				t.Fatalf("failAt %d keep %d: release without a durable intent", failAt, keep)
			}
			_ = goldenResult
		}
	}
}

// viewOf reports whether any key with the prefix is visible in the store.
func viewOf(s *memStore, prefix string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.durable {
		if bytes.HasPrefix([]byte(key), []byte(prefix)) {
			return key, true
		}
	}
	return "", false
}

func TestFatalRegressionStopsEngine(t *testing.T) {
	h, _, _, _ := releaseScenario(t)
	transitions := h.releaser.transitions
	releases := h.pins.releases

	// A new authoritative canonical decision below the accepted anchor is
	// a proven fatal regression (AC-LIF-08-05).
	stopped := openEngine(t, h)
	err := stopped.Authoritative(CanonicalDecision{Network: gcNet, Decision: gcRef(5),
		Head: blockAt(9)})
	var fatal *FatalRegressionError
	if !errors.As(err, &fatal) || !errors.Is(err, ErrFatalRegression) {
		t.Fatalf("below-anchor decision: %v", err)
	}

	// The engine halts: no evaluation, release or observation runs, and no
	// GC happens by the conflicting decision.
	if _, err := stopped.Evaluate([]Entity{entityOf(9, 0, blockAt(5), BranchCanonical, true)}); !errors.Is(
		err, ErrStopped) {
		t.Fatalf("evaluated after fatal: %v", err)
	}
	if _, err := stopped.ObserveFinality(FinalityObservation{Network: gcNet, Decision: gcRef(25),
		Anchor: blockAt(25), EvidenceDigest: evidenceDigest(25), ProfileRef: "eth-mainnet"}); !errors.Is(
		err, ErrStopped) {
		t.Fatalf("observed after fatal: %v", err)
	}
	if _, err := stopped.Release([]Entity{entityOf(9, 0, blockAt(5), BranchCanonical, true)},
		nil); !errors.Is(err, ErrStopped) {
		t.Fatalf("released after fatal: %v", err)
	}
	if h.releaser.transitions != transitions || h.pins.releases != releases {
		t.Fatal("the fatal regression changed completed releases")
	}

	// A restart refuses the store: the native owner stays stopped (D57).
	if _, _, err := Open(h.db, h.oracle, h.pending, h.releaser, h.pins); !errors.Is(err, ErrStopped) ||
		!errors.Is(err, ErrFatalRegression) {
		t.Fatalf("reopened a fatal store: %v", err)
	}
	if h.releaser.transitions != transitions || h.pins.releases != releases {
		t.Fatal("the failed restart changed completed releases")
	}

	// A divergent head above the anchor height is fatal the same way.
	other := newHarness(t, 25)
	otherE := openEngine(t, other)
	commitAnchorAt(t, otherE, 10)
	err = otherE.Authoritative(CanonicalDecision{Network: gcNet, Decision: gcRef(1),
		Head: offChain(12)})
	if !errors.As(err, &fatal) {
		t.Fatalf("divergent-head decision: %v", err)
	}
}

func TestUnknownAncestryHoldsDecisionWithoutFatal(t *testing.T) {
	h := newHarness(t, 25)
	e := openEngine(t, h)
	commitAnchorAt(t, e, 10)
	h.oracle.unknownFor = func(ancestor, descendant AnchorRef) bool { return true }
	err := e.Authoritative(CanonicalDecision{Network: gcNet, Decision: gcRef(1), Head: blockAt(12)})
	if !errors.Is(err, ErrAncestryUnknown) {
		t.Fatalf("unknown ancestry: %v", err)
	}
	// The engine stays alive and the decision retries with refreshed
	// evidence.
	h.oracle.unknownFor = nil
	if err := e.Authoritative(CanonicalDecision{Network: gcNet, Decision: gcRef(1),
		Head: blockAt(12)}); err != nil {
		t.Fatalf("retry after refresh: %v", err)
	}
}

func TestAnchorMonotonicAndFirstOnAcceptedChain(t *testing.T) {
	h := newHarness(t, 25)
	e := openEngine(t, h)
	// Decisions land before any anchor exists.
	if err := e.Authoritative(CanonicalDecision{Network: gcNet, Decision: gcRef(1),
		Head: blockAt(10)}); err != nil {
		t.Fatalf("authoritative without anchor: %v", err)
	}
	// The first anchor must sit on the accepted canonical chain.
	if _, err := e.ObserveFinality(FinalityObservation{Network: gcNet, Decision: gcRef(12),
		Anchor: offChain(12), EvidenceDigest: evidenceDigest(12), ProfileRef: "eth-mainnet"}); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if _, err := e.CommitAnchor(gcNet); !errors.Is(err, ErrCandidateNotCovering) {
		t.Fatalf("off-chain first anchor: %v", err)
	}
	first := commitAnchorAt(t, e, 8)
	if _, err := e.CommitAnchor(gcNet); !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("repeat commit: %v", err)
	}

	// Observing the same anchor again changes nothing.
	if result, err := e.ObserveFinality(FinalityObservation{Network: gcNet, Decision: gcRef(8),
		Anchor: first.Anchor, EvidenceDigest: evidenceDigest(8), ProfileRef: "eth-mainnet"}); err != nil ||
		result != ObservationSame {
		t.Fatalf("same anchor: %s %v", result, err)
	}
	// The anchor only moves up a covering candidate.
	commitAnchorAt(t, e, 15)
	if anchor, _ := e.Anchor(gcNet); anchor.Anchor != blockAt(15) {
		t.Fatalf("anchor %s", anchor.Anchor)
	}
	// A covering observation below the accepted anchor is stale.
	if result, err := e.ObserveFinality(FinalityObservation{Network: gcNet, Decision: gcRef(12),
		Anchor: blockAt(12), EvidenceDigest: evidenceDigest(12), ProfileRef: "eth-mainnet"}); err != nil ||
		result != ObservationStale {
		t.Fatalf("lower covering observation: %s %v", result, err)
	}
	if anchor, _ := e.Anchor(gcNet); anchor.Anchor != blockAt(15) {
		t.Fatalf("anchor demoted to %s", anchor.Anchor)
	}
}

func TestCorruptAnchorRejected(t *testing.T) {
	h := newHarness(t, 25)
	e := openEngine(t, h)
	commitAnchorAt(t, e, 10)
	for key, value := range h.db.durable {
		tampered := slices.Clone(value)
		tampered[len(tampered)-1] ^= 0xFF
		h.db.durable[key] = tampered
		break
	}
	if _, _, err := Open(h.db, h.oracle, h.pending, h.releaser, h.pins); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered store accepted: %v", err)
	}
}

func TestConcurrentEvaluation(t *testing.T) {
	h := newHarness(t, 25)
	e := openEngine(t, h)
	commitAnchorAt(t, e, 20)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			entity := entityOf(uint64(i), 0, blockAt(i%10), BranchCanonical, true, "P0")
			if _, err := e.Evaluate([]Entity{entity}); err != nil {
				t.Errorf("evaluate %d: %v", i, err)
			}
			if _, err := e.ObserveFinality(FinalityObservation{Network: gcNet,
				Decision: gcRef(uint64(20 + i%3)), Anchor: blockAt(20 + i%3),
				EvidenceDigest: evidenceDigest(byte(20 + i%3)), ProfileRef: "eth-mainnet"}); err != nil {
				t.Errorf("observe %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
}
