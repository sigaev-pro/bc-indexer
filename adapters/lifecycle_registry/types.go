// Package registry is the durable activation and revision registry with the
// native decision mirror of E05-01.registry: per network it keeps the
// activation records and their revisions that native decisions have applied,
// mirrors the native decision states (INTENDED, NATIVE_DECIDED,
// NATIVE_REJECTED) before them, and publishes the semantic events of a
// decision only after the whole decision is applied. Speculative capture
// corpus never becomes Applied by itself, a READY Applied activation stays
// cancellable, and an interrupted application is completed atomically by
// reconciliation after a restart. The design is in README.md. The package
// imports only the Go standard library and protocol/semantic.
package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// Errors of the registry. Every error that reports damaged storage wraps
// ErrCorrupt; errors that report a rule violation name the rule.
var (
	// ErrInvalid rejects a malformed argument.
	ErrInvalid = errors.New("invalid registry argument")
	// ErrCorrupt reports a damaged, missing or contradictory record.
	ErrCorrupt = errors.New("corrupt registry record")
	// ErrDecisionConflict reports a repeated decision reference with a
	// different intent.
	ErrDecisionConflict = errors.New("decision already has another intent")
	// ErrDecisionOrder reports a decision sequence that does not follow the
	// sequences the mirror already holds for the producer run.
	ErrDecisionOrder = errors.New("decision sequence out of order")
	// ErrDecisionState reports a transition the current decision state does
	// not allow.
	ErrDecisionState = errors.New("decision state forbids this transition")
	// ErrUnknownDecision reports a decision the mirror does not hold.
	ErrUnknownDecision = errors.New("unknown decision")
	// ErrUnknownActivation reports an activation the registry does not hold.
	ErrUnknownActivation = errors.New("unknown activation")
	// ErrNotDecided reports an application of a decision that native has not
	// decided yet.
	ErrNotDecided = errors.New("decision is not NATIVE_DECIDED")
	// ErrRejected reports an application of a decision that native rejected:
	// its speculative corpus never becomes Applied (AC-LIF-01-01).
	ErrRejected = errors.New("decision is NATIVE_REJECTED")
	// ErrNotCanonical reports an application of a decision that did not
	// accept its branch as canonical: a SIDE decision stores a branch without
	// a head change, so it carries no public Applied events.
	ErrNotCanonical = errors.New("decision did not accept the branch as canonical")
	// ErrAlreadyApplied reports a second, different application of a decision
	// that the applied-decision frontier already closed (AC-CAP-07-05).
	ErrAlreadyApplied = errors.New("decision already has another application")
	// ErrTerminalActivation reports an event on a terminal activation: the
	// writer creates no new Status for a removed activation.
	ErrTerminalActivation = errors.New("activation is terminal")
	// ErrRevision reports an event revision that is not the next revision of
	// the activation counter.
	ErrRevision = errors.New("event revision is not the activation frontier")
	// ErrPlanConflict reports an application plan that breaks a plan rule.
	ErrPlanConflict = errors.New("application plan conflict")
	// ErrCorpusConflict reports a second, different corpus for one block.
	ErrCorpusConflict = errors.New("block already has another corpus")
	// ErrUnknownReplayTarget reports a targeted replay of a block that no
	// deferred canonical application needs: the unknown downtime range is not
	// scanned (AC-LIF-01-02).
	ErrUnknownReplayTarget = errors.New("replay target is not required by a deferred application")
	// ErrActivationState reports an operation the activation state forbids.
	ErrActivationState = errors.New("activation state forbids this operation")
)

// CorpusReplayError reports an application deferred because the corpus of
// its Applied events is missing (AC-LIF-01-02): the caller must create the
// corpus through a targeted replay of the pinned inputs of exactly these
// blocks before the application can complete.
type CorpusReplayError struct {
	Decision NativeDecisionRef
	Blocks   []semantic.Hash32
}

// Error returns the plain sentence of the deferral.
func (e *CorpusReplayError) Error() string {
	blocks := make([]string, len(e.Blocks))
	for i, b := range e.Blocks {
		blocks[i] = b.String()
	}
	return fmt.Sprintf("application of %s is deferred until a targeted replay creates the corpus of %v",
		e.Decision.String(), blocks)
}

// IsCorpusReplay reports whether err is a corpus deferral.
func IsCorpusReplay(err error) bool {
	var replay *CorpusReplayError
	return errors.As(err, &replay)
}

// DecisionKind is the kind of a native decision, as the E01-02 decision
// bridge records it. The registry is client-independent, so the kinds are
// the shared native vocabulary.
type DecisionKind string

// Decision kinds.
const (
	// KindImport moves the head over newly executed blocks.
	KindImport DecisionKind = "IMPORT"
	// KindSide stores newly executed blocks without a head change: its
	// corpus is speculative and never becomes Applied by itself.
	KindSide DecisionKind = "SIDE"
	// KindCanonical changes the canonical chain, applying new activations
	// and removing old ones.
	KindCanonical DecisionKind = "CANONICAL"
	// KindFinality changes the native safe or finalized block; it only
	// carries StatusChanged events of already applied activations.
	KindFinality DecisionKind = "FINALITY"
)

// validKind reports whether k is a registered kind.
func validKind(k DecisionKind) bool {
	switch k {
	case KindImport, KindSide, KindCanonical, KindFinality:
		return true
	}
	return false
}

// canonical reports whether a decided decision of this kind accepted its
// branch as canonical and may apply Applied events.
func (k DecisionKind) canonical() bool {
	return k == KindImport || k == KindCanonical
}

// DecisionState is the state of one mirrored decision. StateApplied is the
// applied-decision frontier: the whole application is durable and visible,
// and a repeated marker of the same decision changes nothing.
type DecisionState string

// Decision states.
const (
	// StateIntended records the transition intent before native settles it.
	StateIntended DecisionState = "INTENDED"
	// StateDecided records the settled NATIVE_DECIDED marker.
	StateDecided DecisionState = "NATIVE_DECIDED"
	// StateRejected records the settled NATIVE_REJECTED marker. It is
	// terminal: the speculative corpus of the decision never becomes Applied.
	StateRejected DecisionState = "NATIVE_REJECTED"
	// StateApplied is the applied-decision frontier.
	StateApplied DecisionState = "APPLIED"
)

// ActivationState is the lifecycle state of one activation, after the state
// names of MESSAGE_CONTRACT section 5. States only advance.
type ActivationState string

// Activation states.
const (
	// ActivationPendingApplied marks a tombstone whose Applied record was
	// not applied yet.
	ActivationPendingApplied ActivationState = "PENDING_APPLIED"
	// ActivationActive marks an applied activation. Its Applied event may be
	// READY and acknowledged, and it stays cancellable either way.
	ActivationActive ActivationState = "ACTIVE"
	// ActivationRemoved is terminal: the tombstone keeps its cancel meaning
	// even for a late Applied, and no event reactivates the activation.
	ActivationRemoved ActivationState = "REMOVED"
)

// NativeDecisionRef identifies one native decision: the node instance that
// observed it, the producer run that made it, and the native decision
// sequence. It is the NativeDecisionRef of CURRENT_SEMANTICS over the
// public ID16 identities of protocol/semantic.
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

// ActivationKey identifies one activation across its events: the network,
// the producer and run that observed it, and the decision entity. Two
// activations of one branch never share it, because a new application of a
// branch has a new decision sequence.
type ActivationKey struct {
	Network    semantic.Network
	ProducerID semantic.ID16
	RunID      semantic.ID16
	Activation semantic.ActivationID
}

// activationName returns a stable one-line name of an activation key.
func activationName(key ActivationKey) string {
	return fmt.Sprintf("%s.%s#%d/%d", key.ProducerID, key.RunID, key.Activation.DecisionSequence,
		key.Activation.EntityOrdinal)
}

// EventPlan is one planned public event of an application: its semantic
// descriptor, which carries the kind, the activation reference, the revision
// and the source provenance, and the content digest the layer in front of
// the registry pins for the event bytes.
type EventPlan struct {
	Descriptor semantic.SemanticDescriptor `json:"descriptor"`
	Content    string                      `json:"content"`
}

// ApplicationPlan is the full event set of one decision application. It is
// stored durably before any activation record changes, so reconciliation
// after a power loss can complete the application atomically.
type ApplicationPlan struct {
	Events []EventPlan `json:"events"`
}

// equal reports whether two plans carry the same events in the same order.
func (p ApplicationPlan) equal(other ApplicationPlan) bool {
	if len(p.Events) != len(other.Events) {
		return false
	}
	for i := range p.Events {
		left, err := json.Marshal(p.Events[i])
		if err != nil {
			return false
		}
		right, err := json.Marshal(other.Events[i])
		if err != nil {
			return false
		}
		if string(left) != string(right) {
			return false
		}
	}
	return true
}

// DecisionRecord is the mirrored state of one native decision.
type DecisionRecord struct {
	Ref     NativeDecisionRef `json:"ref"`
	Network semantic.Network  `json:"network"`
	Kind    DecisionKind      `json:"kind"`
	State   DecisionState     `json:"state"`
	// IntentDigest is the evidence digest of the native transition intent.
	IntentDigest string `json:"intent_digest"`
	// Application is the durable application plan. It is present once an
	// Apply call stored it, including while the application is deferred for
	// a targeted corpus replay.
	Application *ApplicationPlan `json:"application,omitempty"`
	// RecordDigest binds every field above; load refuses a mismatch.
	RecordDigest string `json:"record_digest"`
}

// ActivationRecord is the durable state of one activation.
type ActivationRecord struct {
	Network    semantic.Network      `json:"network"`
	ProducerID semantic.ID16         `json:"producer_id"`
	RunID      semantic.ID16         `json:"run_id"`
	Activation semantic.ActivationID `json:"activation"`
	State      ActivationState       `json:"state"`
	// BlockHash is the activation block the Applied event fixed.
	BlockHash semantic.Hash32 `json:"block_hash"`
	Family    semantic.Family `json:"family"`
	// HasTx marks an inclusion binding; TxPosition and TxHash are its
	// content.
	HasTx      bool            `json:"has_tx"`
	TxPosition uint64          `json:"tx_position"`
	TxHash     semantic.Hash32 `json:"tx_hash"`
	// OriginDigest is the content digest of the Applied event.
	OriginDigest string `json:"origin_digest"`
	// CorpusDigest is the digest of the corpus the Applied event bound.
	CorpusDigest string `json:"corpus_digest"`
	// MaxRevision is the highest recorded evidence revision.
	MaxRevision uint64 `json:"max_revision"`
	// StatusDigest is the content digest of the newest StatusChanged event.
	StatusDigest string `json:"status_digest"`
	// RemovedRevision is the lifecycle revision of the Removing event; it is
	// meaningful only in ActivationRemoved.
	RemovedRevision uint64             `json:"removed_revision"`
	RemovedDecision *NativeDecisionRef `json:"removed_decision,omitempty"`
	// Ready reports whether the Applied event reached the READY publication
	// commit.
	Ready bool `json:"ready"`
	// Acked reports whether the publication was acknowledged. A removal
	// never waits for it.
	Acked bool `json:"acked"`
	// Speculative reports whether the activation corpus was captured before
	// a canonical decision accepted the branch.
	Speculative  bool   `json:"speculative"`
	RecordDigest string `json:"record_digest"`
}

// Key returns the activation key of the record.
func (a ActivationRecord) Key() ActivationKey {
	return ActivationKey{Network: a.Network, ProducerID: a.ProducerID, RunID: a.RunID,
		Activation: a.Activation}
}

// CorpusRecord is the captured corpus of one block: the durable capture
// evidence a canonical application binds. A corpus captured under a
// speculative or rejected decision stays recorded until a later canonical
// decision applies the block or recovery releases it.
type CorpusRecord struct {
	Network    semantic.Network `json:"network"`
	ProducerID semantic.ID16    `json:"producer_id"`
	RunID      semantic.ID16    `json:"run_id"`
	Block      semantic.Hash32  `json:"block"`
	// CorpusDigest is the digest of the captured corpus bytes.
	CorpusDigest string            `json:"corpus_digest"`
	CapturedBy   NativeDecisionRef `json:"captured_by"`
	// ReplayedBy names the canonical decision whose targeted replay produced
	// the corpus; nil for a live capture.
	ReplayedBy   *NativeDecisionRef `json:"replayed_by,omitempty"`
	RecordDigest string             `json:"record_digest"`
}

// digestRecord returns the SHA-256 of the canonical JSON of the record with
// its digest field cleared, over a domain tag and one zero byte. json.Marshal
// of these structs is deterministic: Go writes struct fields in declaration
// order.
func digestRecord(tag string, record any) (string, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(tag), append([]byte{0}, data...)...))
	return hex.EncodeToString(sum[:]), nil
}

// seal computes and stores the record digest of the decision record.
func (d *DecisionRecord) seal() error {
	d.RecordDigest = ""
	digest, err := digestRecord("extractor-registry-decision-v1", d)
	d.RecordDigest = digest
	return err
}

// verify checks the record digest of the decision record.
func (d *DecisionRecord) verify() error {
	stored := d.RecordDigest
	if err := d.seal(); err != nil {
		return err
	}
	if stored != d.RecordDigest {
		return fmt.Errorf("%w: decision %s", ErrCorrupt, d.Ref)
	}
	return nil
}

// seal computes and stores the record digest of the activation record.
func (a *ActivationRecord) seal() error {
	a.RecordDigest = ""
	digest, err := digestRecord("extractor-registry-activation-v1", a)
	a.RecordDigest = digest
	return err
}

// verify checks the record digest of the activation record.
func (a *ActivationRecord) verify() error {
	stored := a.RecordDigest
	if err := a.seal(); err != nil {
		return err
	}
	if stored != a.RecordDigest {
		return fmt.Errorf("%w: activation %s", ErrCorrupt, activationName(a.Key()))
	}
	return nil
}

// seal computes and stores the record digest of the corpus record.
func (c *CorpusRecord) seal() error {
	c.RecordDigest = ""
	digest, err := digestRecord("extractor-registry-corpus-v1", c)
	c.RecordDigest = digest
	return err
}

// verify checks the record digest of the corpus record.
func (c *CorpusRecord) verify() error {
	stored := c.RecordDigest
	if err := c.seal(); err != nil {
		return err
	}
	if stored != c.RecordDigest {
		return fmt.Errorf("%w: corpus of %s", ErrCorrupt, c.Block)
	}
	return nil
}
