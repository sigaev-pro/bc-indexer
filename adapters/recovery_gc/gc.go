// Package recoverygc is the recovery garbage-collection decision core of
// E05-01.activation_gc: per network it accepts exactly one durable
// irreversible GC anchor from finality evidence, evaluates every retained
// entity against that anchor, proven ancestry and the absence of pending
// work, and releases entities and pins through a restartable two-phase
// commit. Without an accepted anchor every pin is retained, a delayed stale
// observation never demotes the anchor and never stops the engine, and a
// proven regression of a new authoritative canonical decision below the
// accepted anchor is fatal. The design is in README.md. The package imports
// only the Go standard library and protocol/semantic.
package recoverygc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// keyPrefix starts every key. Every key is longer than 33 bytes and starts
// with "e", so a native key-value store that hosts it never deletes it in an
// offline prune (the E01-02.pins ledger convention).
const keyPrefix = "extractor-recovery-gc-v1/"

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Errors of the engine. A fatal regression stops the engine and every later
// call; nothing else does.
var (
	// ErrInvalid rejects a malformed argument.
	ErrInvalid = errors.New("invalid recovery_gc argument")
	// ErrCorrupt reports a damaged, missing or contradictory record.
	ErrCorrupt = errors.New("corrupt recovery_gc record")
	// ErrStopped reports that the engine halted after a fatal anchor
	// regression (D57); it wraps the fatal error.
	ErrStopped = errors.New("engine stopped after a fatal anchor regression")
	// ErrFatalRegression is the sentinel of every proven regression below
	// an accepted anchor.
	ErrFatalRegression = errors.New("fatal GC anchor regression")
	// ErrAncestryUnknown reports an authoritative decision whose ancestry
	// evidence is unavailable: the decision is held until refreshed, and no
	// release is authorized by it. It is never fatal.
	ErrAncestryUnknown = errors.New("ancestry evidence unavailable")
	// ErrNoCandidate reports an anchor commit without an observed
	// candidate.
	ErrNoCandidate = errors.New("no anchor candidate for this network")
	// ErrCandidateNotCovering reports a candidate that does not provably
	// contain the accepted anchor. The candidate is dropped and the anchor
	// stands; this is a refresh, not a regression.
	ErrCandidateNotCovering = errors.New("anchor candidate does not cover the accepted anchor")
	// ErrNotEligible reports a release plan with an entity the evaluation
	// retains.
	ErrNotEligible = errors.New("release plan retains an entity")
)

// FatalRegressionError reports a new authoritative canonical decision that
// provably does not contain the durable irreversible anchor of its network:
// its head is below the accepted anchor, or the canonical ancestry at the
// anchor height carries a different hash. The native owner stops before
// READY or publication of the conflicting decision and before any GC by it
// (AC-LIF-08-05); completed releases and delivery state are not rolled
// back, and already-sent bytes are not recalled.
type FatalRegressionError struct {
	Network   semantic.Network
	Accepted  AnchorRef
	Conflict  AnchorRef
	Reason    string
	RecordKey string
}

// Error returns the plain sentence of the regression.
func (e *FatalRegressionError) Error() string {
	return fmt.Sprintf("authoritative canonical decision at %s does not contain the accepted anchor %s: %s",
		e.Conflict, e.Accepted, e.Reason)
}

// Unwrap reports the regression sentinel.
func (e *FatalRegressionError) Unwrap() error { return ErrFatalRegression }

// AnchorRef is the AnchorRef of CURRENT_SEMANTICS: a block height and its
// hash. The number alone is never evidence in either direction.
type AnchorRef struct {
	Height uint64          `json:"height"`
	Hash   semantic.Hash32 `json:"hash"`
}

// Validate checks the reference: the hash must be set; the all-zero hash is
// the unset value, not genesis (CURRENT_SEMANTICS).
func (a AnchorRef) Validate() error {
	if a.Hash == (semantic.Hash32{}) {
		return fmt.Errorf("%w: unset anchor hash", ErrInvalid)
	}
	return nil
}

// String returns a stable one-line name of the reference.
func (a AnchorRef) String() string {
	return fmt.Sprintf("%d/%s", a.Height, a.Hash)
}

// NativeDecisionRef identifies the native decision that accepted evidence.
// It is the NativeDecisionRef of CURRENT_SEMANTICS over the public ID16
// identities, byte-identical in shape to the reference type of
// adapters/lifecycle_registry.
type NativeDecisionRef struct {
	NodeInstanceID   semantic.ID16 `json:"node_instance_id"`
	ProducerID       semantic.ID16 `json:"producer_id"`
	RunID            semantic.ID16 `json:"run_id"`
	DecisionSequence uint64        `json:"decision_sequence"`
}

// Validate checks the reference: every identity must be set.
func (r NativeDecisionRef) Validate() error {
	if r.NodeInstanceID.IsZero() {
		return fmt.Errorf("%w: unset node instance of a decision", ErrInvalid)
	}
	if r.ProducerID.IsZero() {
		return fmt.Errorf("%w: unset producer of a decision", ErrInvalid)
	}
	if r.RunID.IsZero() {
		return fmt.Errorf("%w: unset run of a decision", ErrInvalid)
	}
	return nil
}

// String returns a stable one-line name of the reference.
func (r NativeDecisionRef) String() string {
	return fmt.Sprintf("%s.%s.%s#%d", r.NodeInstanceID, r.ProducerID, r.RunID, r.DecisionSequence)
}

// Verdict is the three-valued result of an ancestry check: proven
// containment, proven divergence, or unknown evidence.
type Verdict uint8

// Verdicts.
const (
	// VerdictUnknown means the ancestry evidence is unavailable: the caller
	// refreshes or retries, GC is blocked, and nothing is fatal.
	VerdictUnknown Verdict = iota
	// VerdictContains means ancestor is proven to be an ancestor-or-self of
	// descendant.
	VerdictContains
	// VerdictDivergent means ancestor is proven not to be an
	// ancestor-or-self of descendant.
	VerdictDivergent
)

// String returns the verdict name.
func (v Verdict) String() string {
	switch v {
	case VerdictContains:
		return "CONTAINS"
	case VerdictDivergent:
		return "DIVERGENT"
	}
	return "UNKNOWN"
}

// AncestryOracle proves ancestry against the pinned canonical ancestry the
// caller holds. It never guesses: unavailable evidence is VerdictUnknown.
type AncestryOracle interface {
	// Contains reports whether ancestor is an ancestor-or-self of
	// descendant, proven not to be, or unknown.
	Contains(ancestor, descendant AnchorRef) Verdict
}

// PendingReport lists the pending work that still references one entity:
// capture, transition, replay or delivery. An acknowledgement, a block
// height or a TTL is not pending work and has no representation here.
type PendingReport struct {
	Capture    bool `json:"capture"`
	Transition bool `json:"transition"`
	Replay     bool `json:"replay"`
	Delivery   bool `json:"delivery"`
}

// Any reports whether any work is pending.
func (p PendingReport) Any() bool { return p.Capture || p.Transition || p.Replay || p.Delivery }

// String lists the pending kinds in a fixed order.
func (p PendingReport) String() string {
	kinds := []string{}
	for _, kind := range []struct {
		name string
		set  bool
	}{{"capture", p.Capture}, {"transition", p.Transition}, {"replay", p.Replay},
		{"delivery", p.Delivery}} {
		if kind.set {
			kinds = append(kinds, kind.name)
		}
	}
	return strings.Join(kinds, ",")
}

// PendingOracle reports the pending work of one entity. The delivery half
// comes from the E03-01 cleanup status; the capture, transition and replay
// halves come from the native owner.
type PendingOracle interface {
	Pending(entity EntityID) (PendingReport, error)
}

// Releaser drops the retained state of one entity; it is idempotent, so a
// restart that repeats a completed release applies nothing twice.
type Releaser interface {
	ReleaseEntity(id EntityID) error
}

// Pins releases durable pins; it is idempotent.
type Pins interface {
	Release(id string) error
}

// EntityID identifies one retained entity: the network, the producer run
// that observed it, and the decision entity.
type EntityID struct {
	Network          semantic.Network `json:"network"`
	ProducerID       semantic.ID16    `json:"producer_id"`
	RunID            semantic.ID16    `json:"run_id"`
	DecisionSequence uint64           `json:"decision_sequence"`
	EntityOrdinal    uint64           `json:"entity_ordinal"`
}

// String returns a stable one-line name of the entity.
func (id EntityID) String() string {
	return fmt.Sprintf("%s.%s#%d/%d", id.ProducerID, id.RunID, id.DecisionSequence, id.EntityOrdinal)
}

// key returns the store key suffix of the entity.
func (id EntityID) key() string {
	return hex.EncodeToString(id.Network.GenesisHash[:]) + "/" + id.ProducerID.String() + "/" +
		id.RunID.String() + "/" + strconv.FormatUint(id.DecisionSequence, 10) + "/" +
		strconv.FormatUint(id.EntityOrdinal, 10)
}

// Branch classifies the branch the entity lives on.
type Branch uint8

// Branches.
const (
	// BranchCanonical is the branch the native chain currently holds
	// canonical.
	BranchCanonical Branch = iota + 1
	// BranchNoncanonical is a side branch: speculative or removed.
	BranchNoncanonical
)

// String returns the branch name.
func (b Branch) String() string {
	switch b {
	case BranchCanonical:
		return "CANONICAL"
	case BranchNoncanonical:
		return "NONCANONICAL"
	}
	return fmt.Sprintf("Branch(%d)", uint8(b))
}

// TerminalEvidence is the durable terminal record of a removed entity: the
// lifecycle revision of the Removing event and the decision that made it.
type TerminalEvidence struct {
	Revision uint64            `json:"revision"`
	Decision NativeDecisionRef `json:"decision"`
}

// Entity is one retained entity the engine evaluates: its activation block,
// its branch, whether it was applied, its terminal evidence if removed, and
// the pins held for it.
type Entity struct {
	ID       EntityID          `json:"id"`
	Block    AnchorRef         `json:"block"`
	Branch   Branch            `json:"branch"`
	Applied  bool              `json:"applied"`
	Terminal *TerminalEvidence `json:"terminal,omitempty"`
	Pins     []string          `json:"pins"`
}

// Validate checks the entity.
func (e Entity) Validate() error {
	if e.ID.ProducerID.IsZero() || e.ID.RunID.IsZero() {
		return fmt.Errorf("%w: unset producer or run of %s", ErrInvalid, e.ID)
	}
	if e.ID.Network.GenesisHash == (semantic.Hash32{}) {
		return fmt.Errorf("%w: unset network genesis of %s", ErrInvalid, e.ID)
	}
	if err := e.Block.Validate(); err != nil {
		return err
	}
	if e.Branch != BranchCanonical && e.Branch != BranchNoncanonical {
		return fmt.Errorf("%w: branch %d of %s", ErrInvalid, uint8(e.Branch), e.ID)
	}
	return nil
}

// Reason is the outcome vocabulary of an evaluation.
type Reason string

// Reasons.
const (
	// ReasonReleaseCovered releases a canonical entity proven inside the
	// accepted anchor area.
	ReasonReleaseCovered Reason = "RELEASE_COVERED"
	// ReasonReleaseDivergentBranch releases a noncanonical entity proven
	// divergent from the pinned canonical ancestry.
	ReasonReleaseDivergentBranch Reason = "RELEASE_DIVERGENT_BRANCH"
	// ReasonRetainNoAnchor keeps everything when no anchor is accepted.
	ReasonRetainNoAnchor Reason = "RETAIN_NO_ANCHOR"
	// ReasonRetainPending keeps an entity with pending work.
	ReasonRetainPending Reason = "RETAIN_PENDING"
	// ReasonRetainAncestryUnknown keeps an entity whose ancestry evidence
	// is unavailable.
	ReasonRetainAncestryUnknown Reason = "RETAIN_ANCESTRY_UNKNOWN"
	// ReasonRetainNotCovered keeps a canonical entity not proven inside the
	// anchor area.
	ReasonRetainNotCovered Reason = "RETAIN_NOT_COVERED"
	// ReasonRetainBranchUndiverged keeps a noncanonical entity whose
	// divergence is not proven.
	ReasonRetainBranchUndiverged Reason = "RETAIN_BRANCH_UNDIVERGED"
	// ReasonRetainAppliedNoncanonical keeps an applied noncanonical entity:
	// its removal transition must land first.
	ReasonRetainAppliedNoncanonical Reason = "RETAIN_APPLIED_NONCANONICAL"
)

// Decision is the evaluation of one entity.
type Decision struct {
	Entity  EntityID      `json:"entity"`
	Release bool          `json:"release"`
	Reason  Reason        `json:"reason"`
	Pending PendingReport `json:"pending"`
}

// String returns a deterministic one-line dump of the decision.
func (d Decision) String() string {
	return fmt.Sprintf("%s %s %s", d.Entity, map[bool]string{true: "RELEASE", false: "RETAIN"}[d.Release],
		d.Reason)
}

// AcceptedGcAnchor is the durable irreversible anchor of one network, the
// AcceptedGcAnchor of CURRENT_SEMANTICS: the network-specific finality
// evidence adapter vouches for the provenance; the engine owns the
// monotonic acceptance and the sync boundary.
type AcceptedGcAnchor struct {
	Network        semantic.Network  `json:"network"`
	Anchor         AnchorRef         `json:"anchor"`
	Decision       NativeDecisionRef `json:"decision"`
	EvidenceDigest string            `json:"evidence_digest"`
	ProfileRef     string            `json:"profile_ref"`
	RecordDigest   string            `json:"record_digest"`
}

// Validate checks the anchor record.
func (a AcceptedGcAnchor) Validate() error {
	if a.Network.GenesisHash == (semantic.Hash32{}) {
		return fmt.Errorf("%w: unset network genesis of an anchor", ErrInvalid)
	}
	if err := a.Anchor.Validate(); err != nil {
		return err
	}
	if err := a.Decision.Validate(); err != nil {
		return err
	}
	if !digestPattern.MatchString(a.EvidenceDigest) {
		return fmt.Errorf("%w: anchor evidence digest %q", ErrInvalid, a.EvidenceDigest)
	}
	if a.ProfileRef == "" {
		return fmt.Errorf("%w: anchor profile ref is empty", ErrInvalid)
	}
	return nil
}

// seal computes and stores the record digest.
func (a *AcceptedGcAnchor) seal() error {
	a.RecordDigest = ""
	digest, err := digestRecord("extractor-gc-anchor-v1", a)
	a.RecordDigest = digest
	return err
}

// verify checks the record digest.
func (a *AcceptedGcAnchor) verify() error {
	stored := a.RecordDigest
	if err := a.seal(); err != nil {
		return err
	}
	if stored != a.RecordDigest {
		return fmt.Errorf("%w: anchor %s", ErrCorrupt, a.Anchor)
	}
	return nil
}

// frontierRecord is the durable decision frontier of one network: the last
// authoritative canonical decision the engine accepted.
type frontierRecord struct {
	Network      semantic.Network  `json:"network"`
	Decision     NativeDecisionRef `json:"decision"`
	Head         AnchorRef         `json:"head"`
	RecordDigest string            `json:"record_digest"`
}

func (f *frontierRecord) seal() error {
	f.RecordDigest = ""
	digest, err := digestRecord("extractor-gc-frontier-v1", f)
	f.RecordDigest = digest
	return err
}

func (f *frontierRecord) verify() error {
	stored := f.RecordDigest
	if err := f.seal(); err != nil {
		return err
	}
	if stored != f.RecordDigest {
		return fmt.Errorf("%w: frontier of the decision %s", ErrCorrupt, f.Decision)
	}
	return nil
}

// fatalRecord is the durable halt marker of a proven anchor regression.
type fatalRecord struct {
	Network      semantic.Network `json:"network"`
	Accepted     AnchorRef        `json:"accepted"`
	Conflict     AnchorRef        `json:"conflict"`
	Reason       string           `json:"reason"`
	RecordDigest string           `json:"record_digest"`
}

func (f *fatalRecord) seal() error {
	f.RecordDigest = ""
	digest, err := digestRecord("extractor-gc-fatal-v1", f)
	f.RecordDigest = digest
	return err
}

func (f *fatalRecord) verify() error {
	stored := f.RecordDigest
	if err := f.seal(); err != nil {
		return err
	}
	if stored != f.RecordDigest {
		return fmt.Errorf("%w: fatal record", ErrCorrupt)
	}
	return nil
}

// digestRecord returns the SHA-256 of the canonical JSON of the record with
// its digest field cleared, over a domain tag and one zero byte.
func digestRecord(tag string, record any) (string, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(tag), append([]byte{0}, data...)...))
	return hex.EncodeToString(sum[:]), nil
}

// Store is the durable key-value store of the engine; it has the durability
// contract of the lifecycle_registry Store.
type Store interface {
	Has(key []byte) (bool, error)
	Get(key []byte) ([]byte, error)
	Put(key, value []byte) error
	SyncKeyValue() error
	Range(prefix []byte, visit func(key, value []byte) error) error
}

func strictDecode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing data")
	}
	return nil
}

func anchorKey(net semantic.Network) []byte {
	return []byte(keyPrefix + "anchor/" + hex.EncodeToString(net.GenesisHash[:]))
}

func frontierKey(net semantic.Network) []byte {
	return []byte(keyPrefix + "frontier/" + hex.EncodeToString(net.GenesisHash[:]))
}

func fatalKey(net semantic.Network) []byte {
	return []byte(keyPrefix + "fatal/" + hex.EncodeToString(net.GenesisHash[:]))
}

// ObservationResult is the outcome of one finality observation.
type ObservationResult string

// Observation results.
const (
	// ObservationCandidate reports a covering observation that became the
	// anchor candidate; CommitAnchor still has to accept it.
	ObservationCandidate ObservationResult = "CANDIDATE"
	// ObservationSame reports the accepted anchor observed again.
	ObservationSame ObservationResult = "SAME_ANCHOR"
	// ObservationStale reports an observation that does not cover the
	// accepted anchor: a refresh outcome, never a fatal one and never a
	// demotion of the anchor.
	ObservationStale ObservationResult = "STALE_OBSERVATION"
	// ObservationUnavailable reports ancestry evidence that is not
	// available; no candidate was recorded.
	ObservationUnavailable ObservationResult = "EVIDENCE_UNAVAILABLE"
)

// FinalityObservation is one network-specific finality evidence
// observation: the proposed anchor with the native decision that produced
// the evidence, its evidence digest and profile. An unavailable source is
// the caller's decision not to observe: an empty digest is refused, so an
// outage cannot invent an anchor.
type FinalityObservation struct {
	Network        semantic.Network
	Decision       NativeDecisionRef
	Anchor         AnchorRef
	EvidenceDigest string
	ProfileRef     string
}

// CanonicalDecision is one new authoritative canonical decision of a
// network with the canonical head it accepted.
type CanonicalDecision struct {
	Network  semantic.Network
	Decision NativeDecisionRef
	Head     AnchorRef
}

// Engine is the recovery garbage-collection decision core. It is safe for
// concurrent use.
type Engine struct {
	mu         sync.Mutex
	db         Store
	oracle     AncestryOracle
	pending    PendingOracle
	releaser   Releaser
	pins       Pins
	anchors    map[semantic.Network]AcceptedGcAnchor
	candidates map[semantic.Network]AcceptedGcAnchor
	frontiers  map[semantic.Network]frontierRecord
	stopped    error
}

// Open loads the durable state, refuses a store that halted on a proven
// anchor regression, and completes the releases a power loss interrupted
// (AC-LIF-08-02). It returns the number of releases the restart completed.
func Open(db Store, oracle AncestryOracle, pending PendingOracle, releaser Releaser,
	pins Pins) (*Engine, int, error) {
	if db == nil || oracle == nil || pending == nil || releaser == nil || pins == nil {
		return nil, 0, fmt.Errorf("%w: missing seam", ErrInvalid)
	}
	e := &Engine{db: db, oracle: oracle, pending: pending, releaser: releaser, pins: pins,
		anchors:    map[semantic.Network]AcceptedGcAnchor{},
		candidates: map[semantic.Network]AcceptedGcAnchor{},
		frontiers:  map[semantic.Network]frontierRecord{}}
	err := db.Range([]byte(keyPrefix), func(key, value []byte) error {
		switch {
		case bytes.HasPrefix(key, []byte(keyPrefix+"anchor/")):
			var a AcceptedGcAnchor
			if err := strictDecode(value, &a); err != nil {
				return err
			}
			if err := a.verify(); err != nil {
				return err
			}
			if string(key) != string(anchorKey(a.Network)) {
				return fmt.Errorf("anchor of %s filed under %s", a.Network, key)
			}
			e.anchors[a.Network] = a
			return nil
		case bytes.HasPrefix(key, []byte(keyPrefix+"frontier/")):
			var f frontierRecord
			if err := strictDecode(value, &f); err != nil {
				return err
			}
			if err := f.verify(); err != nil {
				return err
			}
			e.frontiers[f.Network] = f
			return nil
		case bytes.HasPrefix(key, []byte(keyPrefix+"fatal/")):
			var f fatalRecord
			if err := strictDecode(value, &f); err != nil {
				return err
			}
			if err := f.verify(); err != nil {
				return err
			}
			fatal := &FatalRegressionError{Network: f.Network, Accepted: f.Accepted, Conflict: f.Conflict,
				Reason: f.Reason, RecordKey: string(key)}
			return fatal
		case bytes.HasPrefix(key, []byte(keyPrefix+"release/")):
			return nil
		case bytes.HasPrefix(key, []byte(keyPrefix+"tombstone/")):
			var c compactRecord
			if err := strictDecode(value, &c); err != nil {
				return err
			}
			if err := c.verify(); err != nil {
				return err
			}
			return nil
		}
		return fmt.Errorf("%w: unexpected key %s", ErrCorrupt, key)
	})
	if err != nil {
		var fatal *FatalRegressionError
		if errors.As(err, &fatal) {
			return nil, 0, fmt.Errorf("%w: %w", ErrStopped, fatal)
		}
		return nil, 0, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	completed, err := e.reconcile()
	if err != nil {
		return nil, 0, err
	}
	return e, completed, nil
}

// Anchor returns the accepted anchor of one network.
func (e *Engine) Anchor(net semantic.Network) (AcceptedGcAnchor, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.anchors[net]
	return a, ok
}

// ObserveFinality records one finality observation. A covering observation
// becomes the anchor candidate; an observation that does not cover the
// accepted anchor is stale and changes nothing: it is refreshed later and
// never demotes the anchor and never stops the engine (AC-LIF-08-03).
func (e *Engine) ObserveFinality(obs FinalityObservation) (ObservationResult, error) {
	if obs.Network.GenesisHash == (semantic.Hash32{}) {
		return "", fmt.Errorf("%w: unset network genesis", ErrInvalid)
	}
	if err := obs.Decision.Validate(); err != nil {
		return "", err
	}
	if err := obs.Anchor.Validate(); err != nil {
		return "", err
	}
	// A source outage must not invent an anchor: the caller observes
	// nothing, and an observation without evidence is refused.
	if !digestPattern.MatchString(obs.EvidenceDigest) {
		return "", fmt.Errorf("%w: observation evidence digest %q", ErrInvalid, obs.EvidenceDigest)
	}
	if obs.ProfileRef == "" {
		return "", fmt.Errorf("%w: observation profile ref is empty", ErrInvalid)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.live(); err != nil {
		return "", err
	}
	accepted, has := e.anchors[obs.Network]
	if has && accepted.Anchor == obs.Anchor {
		return ObservationSame, nil
	}
	if has {
		switch e.oracle.Contains(accepted.Anchor, obs.Anchor) {
		case VerdictContains:
			candidate := AcceptedGcAnchor{Network: obs.Network, Decision: obs.Decision,
				Anchor: obs.Anchor, EvidenceDigest: obs.EvidenceDigest, ProfileRef: obs.ProfileRef}
			if err := candidate.Validate(); err != nil {
				return "", err
			}
			e.candidates[obs.Network] = candidate
			return ObservationCandidate, nil
		case VerdictDivergent:
			return ObservationStale, nil
		default:
			return ObservationUnavailable, nil
		}
	}
	candidate := AcceptedGcAnchor{Network: obs.Network, Decision: obs.Decision, Anchor: obs.Anchor,
		EvidenceDigest: obs.EvidenceDigest, ProfileRef: obs.ProfileRef}
	if err := candidate.Validate(); err != nil {
		return "", err
	}
	e.candidates[obs.Network] = candidate
	return ObservationCandidate, nil
}

// CommitAnchor accepts the observed candidate of one network: the anchor
// acceptance is its own sync decision, so a power loss before it keeps the
// previous anchor and the candidate authorizes nothing (AC-LIF-08-04). The
// candidate must provably contain the accepted anchor, or the frontier head
// when this is the first anchor; a non-covering candidate is refused and
// dropped without stopping the engine.
func (e *Engine) CommitAnchor(net semantic.Network) (AcceptedGcAnchor, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.live(); err != nil {
		return AcceptedGcAnchor{}, err
	}
	candidate, ok := e.candidates[net]
	if !ok {
		return AcceptedGcAnchor{}, fmt.Errorf("%w: %s", ErrNoCandidate, net)
	}
	delete(e.candidates, net)
	if accepted, has := e.anchors[net]; has {
		if accepted.Anchor == candidate.Anchor {
			return accepted, nil
		}
		if e.oracle.Contains(accepted.Anchor, candidate.Anchor) != VerdictContains {
			return AcceptedGcAnchor{}, fmt.Errorf("%w: %s does not contain %s", ErrCandidateNotCovering,
				candidate.Anchor, accepted.Anchor)
		}
	} else if frontier, has := e.frontiers[net]; has {
		// The first anchor must sit on the canonical chain the engine
		// already accepted decisions for.
		if e.oracle.Contains(candidate.Anchor, frontier.Head) != VerdictContains {
			return AcceptedGcAnchor{}, fmt.Errorf("%w: %s is not on the accepted canonical chain",
				ErrCandidateNotCovering, candidate.Anchor)
		}
	}
	if err := candidate.seal(); err != nil {
		return AcceptedGcAnchor{}, err
	}
	data, err := json.Marshal(candidate)
	if err != nil {
		return AcceptedGcAnchor{}, err
	}
	if err := e.db.Put(anchorKey(net), data); err != nil {
		return AcceptedGcAnchor{}, err
	}
	if err := e.db.SyncKeyValue(); err != nil {
		return AcceptedGcAnchor{}, err
	}
	e.anchors[net] = candidate
	return candidate, nil
}

// Authoritative records one new authoritative canonical decision. A
// decision whose head provably does not contain the accepted anchor is a
// fatal regression: the error is returned, the engine halts, the decision
// never reaches the durable frontier and no GC runs by it (AC-LIF-08-05).
// Unavailable ancestry evidence holds the decision without stopping the
// engine.
func (e *Engine) Authoritative(d CanonicalDecision) error {
	if d.Network.GenesisHash == (semantic.Hash32{}) {
		return fmt.Errorf("%w: unset network genesis", ErrInvalid)
	}
	if err := d.Decision.Validate(); err != nil {
		return err
	}
	if err := d.Head.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.live(); err != nil {
		return err
	}
	if accepted, has := e.anchors[d.Network]; has {
		if d.Head.Height < accepted.Anchor.Height {
			return e.fatal(d.Network, accepted.Anchor, d.Head,
				fmt.Sprintf("head height %d is below the accepted anchor height %d", d.Head.Height,
					accepted.Anchor.Height))
		}
		switch e.oracle.Contains(accepted.Anchor, d.Head) {
		case VerdictDivergent:
			return e.fatal(d.Network, accepted.Anchor, d.Head,
				fmt.Sprintf("the canonical ancestry at height %d does not carry the accepted anchor hash",
					accepted.Anchor.Height))
		case VerdictUnknown:
			return fmt.Errorf("%w: decision %s head %s", ErrAncestryUnknown, d.Decision, d.Head)
		}
	}
	frontier := frontierRecord{Network: d.Network, Decision: d.Decision, Head: d.Head}
	if err := frontier.seal(); err != nil {
		return err
	}
	data, err := json.Marshal(frontier)
	if err != nil {
		return err
	}
	if err := e.db.Put(frontierKey(d.Network), data); err != nil {
		return err
	}
	if err := e.db.SyncKeyValue(); err != nil {
		return err
	}
	e.frontiers[d.Network] = frontier
	return nil
}

// fatal records the proven regression durably and halts the engine.
func (e *Engine) fatal(net semantic.Network, accepted, conflict AnchorRef, reason string) error {
	record := fatalRecord{Network: net, Accepted: accepted, Conflict: conflict, Reason: reason}
	if err := record.seal(); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := e.db.Put(fatalKey(net), data); err != nil {
		return err
	}
	if err := e.db.SyncKeyValue(); err != nil {
		return err
	}
	fatal := &FatalRegressionError{Network: net, Accepted: accepted, Conflict: conflict, Reason: reason,
		RecordKey: string(fatalKey(net))}
	e.stopped = fmt.Errorf("%w: %w", ErrStopped, fatal)
	return fatal
}

// live reports ErrStopped once the engine halted.
func (e *Engine) live() error {
	if e.stopped != nil {
		return e.stopped
	}
	return nil
}

// Evaluate evaluates every entity against the accepted anchor of its
// network, proven ancestry and the absence of pending work. Without an
// anchor everything is retained (AC-LIF-08-01); pending work retains the
// entity whatever the anchor covers; coverage is proven ancestry, never a
// block height; a noncanonical entity needs proven divergence from the
// pinned canonical ancestry.
func (e *Engine) Evaluate(entities []Entity) ([]Decision, error) {
	for _, entity := range entities {
		if err := entity.Validate(); err != nil {
			return nil, err
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.live(); err != nil {
		return nil, err
	}
	out := make([]Decision, 0, len(entities))
	for _, entity := range entities {
		decision, err := e.evaluate(entity)
		if err != nil {
			return nil, err
		}
		out = append(out, decision)
	}
	return out, nil
}

func (e *Engine) evaluate(entity Entity) (Decision, error) {
	accepted, has := e.anchors[entity.ID.Network]
	if !has {
		return Decision{Entity: entity.ID, Reason: ReasonRetainNoAnchor}, nil
	}
	pending, err := e.pending.Pending(entity.ID)
	if err != nil {
		return Decision{}, err
	}
	if pending.Any() {
		return Decision{Entity: entity.ID, Reason: ReasonRetainPending, Pending: pending}, nil
	}
	if entity.Branch == BranchCanonical {
		switch e.oracle.Contains(entity.Block, accepted.Anchor) {
		case VerdictContains:
			return Decision{Entity: entity.ID, Release: true, Reason: ReasonReleaseCovered}, nil
		case VerdictDivergent:
			return Decision{Entity: entity.ID, Reason: ReasonRetainNotCovered}, nil
		default:
			return Decision{Entity: entity.ID, Reason: ReasonRetainAncestryUnknown}, nil
		}
	}
	if entity.Applied {
		return Decision{Entity: entity.ID, Reason: ReasonRetainAppliedNoncanonical}, nil
	}
	switch e.oracle.Contains(entity.Block, accepted.Anchor) {
	case VerdictDivergent:
		return Decision{Entity: entity.ID, Release: true, Reason: ReasonReleaseDivergentBranch}, nil
	case VerdictContains:
		return Decision{Entity: entity.ID, Reason: ReasonRetainBranchUndiverged}, nil
	default:
		return Decision{Entity: entity.ID, Reason: ReasonRetainAncestryUnknown}, nil
	}
}

// Tombstones returns the compact records the releases of one network left.
func (e *Engine) Tombstones() ([]compactRecord, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []compactRecord{}
	err := e.db.Range([]byte(keyPrefix+"tombstone/"), func(key, value []byte) error {
		var c compactRecord
		if err := strictDecode(value, &c); err != nil {
			return err
		}
		if err := c.verify(); err != nil {
			return err
		}
		out = append(out, c)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Entity.key() < out[j].Entity.key() })
	return out, nil
}
