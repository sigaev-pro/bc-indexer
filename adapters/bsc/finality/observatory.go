package finality

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	recoverygc "github.com/sigaev-pro/bc-indexer/adapters/recovery_gc"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"

	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
)

// Observatory is the durable finality evidence store and the GC anchor gate
// of one BSC producer run. It keeps the early vote evidence and the header
// observations as separate durable records, serves a stored early record
// after a restart only with RETAINED_DURABLE provenance, and accepts the GC
// anchor only from the attested snapshot source of a header observation on
// the canonical execution ancestry. The anchor is monotonic by ancestry: an
// equal or extending candidate advances it, a lower candidate on the same
// chain is stale and keeps it, and an incomparable candidate — a different
// hash at the anchor height or an incompatible branch — is a proven
// regression below the accepted anchor and stops the observatory. Release of
// pins and the remaining GC conditions belong to E05-01.activation_gc.
type Observatory struct {
	mu       sync.Mutex
	adm      lifecycle.Checker
	db       admission.Store
	base     string
	storeID  string
	producer string
	id       RunIdentity
	gc       Anchorer
	early    *earlyRecord
	err      error
}

// earlyRecord is the durable early vote evidence spool: the typed evidence,
// the digest of the owned vote snapshot it was computed from, and the digest
// of the record.
type earlyRecord struct {
	NativeStoreID string            `json:"native_store_id"`
	Producer      string            `json:"producer"`
	Evidence      EarlyVoteEvidence `json:"evidence"`
	VotesDigest   string            `json:"votes_digest"`
	Digest        string            `json:"digest"`
}

// RunIdentity is the public semantic identity of the producer run that owns
// the observatory: the network it observes and the node instance, producer
// and run identities of the NativeDecisionRef of CURRENT_SEMANTICS. The
// observatory addresses the GC anchor owner and its finality observations
// through it.
type RunIdentity struct {
	Network  semantic.Network
	Node     semantic.ID16
	Producer semantic.ID16
	Run      semantic.ID16
}

// Validate checks the identity: every part must be set.
func (id RunIdentity) Validate() error {
	if id.Network.GenesisHash == (semantic.Hash32{}) {
		return fmt.Errorf("%w: unset network genesis of the run identity", ErrInvalid)
	}
	if id.Node.IsZero() || id.Producer.IsZero() || id.Run.IsZero() {
		return fmt.Errorf("%w: unset node, producer or run of the run identity", ErrInvalid)
	}
	return nil
}

// AnchorProfile is the profile reference the observatory vouches for the GC
// anchor evidence it observes: the attested snapshot header evidence of
// this package at its current digest domain.
const AnchorProfile = "adapters/bsc/finality-v1"

// Anchorer owns the durable GC anchor of the network: the recovery GC
// engine of E05-01.activation_gc, whose AcceptedGcAnchor is the shared
// anchor contract. Acceptance is the engine's own sync decision over
// covering candidates.
type Anchorer interface {
	// Anchor returns the accepted anchor of the network.
	Anchor(net semantic.Network) (recoverygc.AcceptedGcAnchor, bool)
	// ObserveFinality records one finality observation of the network.
	ObserveFinality(recoverygc.FinalityObservation) (recoverygc.ObservationResult, error)
	// CommitAnchor accepts the observed candidate of the network.
	CommitAnchor(net semantic.Network) (recoverygc.AcceptedGcAnchor, error)
}

// hashOfHead decodes the hash of one head into the shared Hash32 model.
func hashOfHead(h decision.Head) (semantic.Hash32, error) {
	raw, err := rawHash(h.Hash)
	if err != nil {
		return semantic.Hash32{}, err
	}
	var out semantic.Hash32
	copy(out[:], raw)
	return out, nil
}

// headOfAnchor converts the anchor block of the shared GC contract into the
// head model of this package.
func headOfAnchor(a recoverygc.AnchorRef) (decision.Head, error) {
	if a.Hash == (semantic.Hash32{}) {
		return decision.Head{}, fmt.Errorf("%w: unset anchor hash", ErrInvalid)
	}
	return decision.Head{Number: a.Height, Hash: a.Hash.String()}, nil
}

// evidenceRecord is the last observed full evidence, kept for the restart
// refresh and the reconciliation of the next start.
type evidenceRecord struct {
	NativeStoreID string   `json:"native_store_id"`
	Producer      string   `json:"producer"`
	Evidence      Evidence `json:"evidence"`
	Digest        string   `json:"digest"`
}

// AnchorChange is what one header observation did to the GC anchor.
type AnchorChange string

// Anchor changes.
const (
	// Accepted means a new anchor was accepted (there was none).
	Accepted AnchorChange = "ACCEPTED"
	// Advanced means the anchor moved to a descendant of the accepted one.
	Advanced AnchorChange = "ADVANCED"
	// Unchanged means the candidate equals the accepted anchor.
	Unchanged AnchorChange = "UNCHANGED"
	// Stale means the candidate is on the accepted chain below the anchor;
	// the anchor stays and nothing is written.
	Stale AnchorChange = "STALE"
	// Refused means the observation gives no anchor candidate: a genesis
	// fallback, or a header or finalized block on a side branch. Nothing is
	// written and this is not a regression.
	Refused AnchorChange = "REFUSED"
)

// AnchorResult is the outcome of one header observation.
type AnchorResult struct {
	// Change is what happened to the GC anchor.
	Change AnchorChange
	// Anchor is the accepted anchor after the observation; the zero value
	// when none is accepted.
	Anchor decision.Head
	// HeaderDigest is the evidence digest of the observed header snapshot.
	HeaderDigest string
}

// ErrRegression reports a proven regression below the accepted GC anchor: a
// new authoritative header-derived finalized that does not contain the
// accepted anchor, or an execution head that no longer covers it. The
// observatory stops; a lost VotePool, a missing header or a stale callback
// is never this error.
var ErrRegression = fmt.Errorf("%w: a proven regression below the accepted BSC GC anchor",
	lifecycle.ErrInvariant)

// Open opens the observatory of the producer run of adm on db. It loads the
// stored records; a stored early record is served with RETAINED_DURABLE
// provenance, never as a fresh VotePool result. A record of another store or
// producer, a malformed record or a wrong digest is refused. id addresses
// the network and the native decision references of the run, and gc owns
// the durable GC anchor of the network (the recovery GC engine).
func Open(t *admission.Token, db admission.Store, adm lifecycle.Checker, storeID string,
	id RunIdentity, gc Anchorer) (*Observatory, error) {
	if adm == nil || db == nil {
		return nil, fmt.Errorf("%w: missing admission or store", ErrInvalid)
	}
	if err := adm.Check(t); err != nil {
		return nil, err
	}
	if !storeIDPattern.MatchString(storeID) {
		return nil, fmt.Errorf("%w: native store ID %q", ErrInvalid, storeID)
	}
	if err := id.Validate(); err != nil {
		return nil, err
	}
	if gc == nil {
		return nil, fmt.Errorf("%w: missing GC anchor owner", ErrInvalid)
	}
	p := adm.Producer()
	o := &Observatory{adm: adm, db: db, storeID: storeID,
		base:     keyPrefix + p.ProducerID + "." + p.RunID + "/",
		producer: p.ProducerID + "." + p.RunID, id: id, gc: gc}
	if err := db.SyncKeyValue(); err != nil {
		return nil, err
	}
	if err := o.load(); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *Observatory) key(name string) []byte { return []byte(o.base + name) }

// load reads the stored records and checks their digests and bindings.
func (o *Observatory) load() error {
	early, ok, err := readRecord[earlyRecord](o.db, o.key("early"))
	if err != nil {
		return err
	}
	if ok {
		if early.NativeStoreID != o.storeID || early.Producer != o.producer {
			return fmt.Errorf("%w: early record of %s %s", ErrInvariant, early.NativeStoreID, early.Producer)
		}
		if err := verifyEarly(early); err != nil {
			return err
		}
		o.early = early
	}
	return nil
}

func strictDecode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

func readRecord[T any](db admission.Store, key []byte) (*T, bool, error) {
	ok, err := db.Has(key)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	data, err := db.Get(key)
	if err != nil {
		return nil, false, err
	}
	var value T
	if err := strictDecode(data, &value); err != nil {
		return nil, false, fmt.Errorf("%w: %s: %v", ErrCorrupt, key, err)
	}
	return &value, true, nil
}

func verifyEarly(e *earlyRecord) error {
	if _, err := rawHash(e.VotesDigest); err != nil {
		return fmt.Errorf("%w: early record: %v", ErrCorrupt, err)
	}
	if digest := digestOf(tagEarly, str(e.Evidence.EvidenceDigest), str(e.VotesDigest)); digest == e.Digest {
		return nil
	}
	return fmt.Errorf("%w: early record digest", ErrCorrupt)
}

func (o *Observatory) enter(t *admission.Token) error {
	if err := o.adm.Check(t); err != nil {
		return err
	}
	if o.err != nil {
		return fmt.Errorf("%w: %v", ErrFailed, o.err)
	}
	return nil
}

func (o *Observatory) fail(err error) error {
	if o.err == nil {
		o.err = err
	}
	return err
}

// sha256Sum returns the hex SHA-256 of data.
func sha256Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (o *Observatory) put(key []byte, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := o.db.Put(key, data); err != nil {
		return o.fail(err)
	}
	if err := o.db.SyncKeyValue(); err != nil {
		return o.fail(err)
	}
	return nil
}

// Early returns the stored early vote evidence. After a restart the evidence
// of an earlier process is served only with RETAINED_DURABLE provenance: it
// is retained evidence, not a fresh VotePool result. It returns nil when no
// early evidence is stored.
func (o *Observatory) Early(t *admission.Token) *EarlyVoteEvidence {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.enter(t); err != nil {
		return nil
	}
	if o.early == nil {
		return nil
	}
	retained := o.early.Evidence
	retained.Origin = RetainedDurable
	return &retained
}

// Anchor returns the accepted GC anchor record of the network from the
// anchor owner, if one is accepted.
func (o *Observatory) Anchor(t *admission.Token) (recoverygc.AcceptedGcAnchor, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.enter(t); err != nil {
		return recoverygc.AcceptedGcAnchor{}, false
	}
	return o.gc.Anchor(o.id.Network)
}

// ObserveEarly stores one early vote observation made by this process. The
// origin must be NATIVE_LIVE or NATIVE_RESTART; a RETAINED_DURABLE origin is
// never a fresh observation. The evidence digest must equal EarlyDigest of
// the evidence and the owned snapshot of the verified vote envelopes and
// validator context, so the record binds the spool it was computed from. The
// record and its sync are one unit: a power loss before the sync drops the
// observation, which loses only transient early evidence and never lowers a
// durable anchor.
func (o *Observatory) ObserveEarly(t *admission.Token, e EarlyVoteEvidence, votes []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.enter(t); err != nil {
		return err
	}
	if err := e.valid(); err != nil {
		return err
	}
	if e.Origin != NativeLive && e.Origin != NativeRestart {
		return fmt.Errorf("%w: a fresh early observation has origin %q", ErrInvalid, e.Origin)
	}
	digest, err := EarlyDigest(e, votes)
	if err != nil {
		return err
	}
	if e.EvidenceDigest != digest {
		return fmt.Errorf("%w: early evidence digest %s, want %s", ErrConflict, e.EvidenceDigest, digest)
	}
	voteDigest := sha256Sum(votes)
	record := &earlyRecord{NativeStoreID: o.storeID, Producer: o.producer, Evidence: e, VotesDigest: voteDigest}
	record.Digest = digestOf(tagEarly, str(e.EvidenceDigest), str(voteDigest))
	if err := o.put(o.key("early"), record); err != nil {
		return err
	}
	o.early = record
	return nil
}

// ObserveHeader records one full finality evidence and applies the GC anchor
// gate. The observation is refused with ErrBranch when the native header
// head is not on the canonical execution ancestry of the observed execution
// head: a delayed side-branch or same-height different-hash notification
// finalizes nothing, changes nothing and is not a regression. A genesis
// fallback gives no anchor candidate, and neither does a finalized source
// that is not on the canonical execution ancestry (Refused, no error). An
// attested snapshot source that is canonical advances the
// anchor (Accepted with none, Advanced on a descendant, Unchanged when
// equal); a lower candidate on the accepted chain is Stale and keeps the
// anchor. An execution head that no longer covers the accepted anchor, or an
// incomparable candidate, is a proven regression below the accepted anchor:
// ErrRegression, the observatory stops and the caller must stop the native
// owner before READY or events of the conflicting decision. decisionSequence
// is the sequence of the native decision that observed the snapshot (0 for a
// startup refresh observation).
func (o *Observatory) ObserveHeader(t *admission.Token, e Evidence, anc Ancestry, decisionSequence uint64) (
	AnchorResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.enter(t); err != nil {
		return AnchorResult{}, err
	}
	if anc == nil {
		return AnchorResult{}, fmt.Errorf("%w: missing ancestry", ErrInvalid)
	}
	if err := e.valid(); err != nil {
		return AnchorResult{}, err
	}
	// The header head and the execution head must first agree: the snapshot
	// head must be on the canonical execution ancestry (either side may be
	// briefly ahead, never on another branch).
	snapshotCanonical, err := anc.IsAncestorOrSelf(e.ObservedNativeHeaderHead, e.ObservedExecutionHead)
	if err != nil {
		return AnchorResult{}, fmt.Errorf("%w: header head ancestry: %w", ErrSource, err)
	}
	executionCanonical := false
	if !snapshotCanonical {
		if executionCanonical, err = anc.IsAncestorOrSelf(e.ObservedExecutionHead, e.ObservedNativeHeaderHead); err != nil {
			return AnchorResult{}, fmt.Errorf("%w: execution head ancestry: %w", ErrSource, err)
		}
		if !executionCanonical {
			return AnchorResult{}, fmt.Errorf("%w: header head %s is not on the canonical execution ancestry of %s",
				ErrBranch, e.ObservedNativeHeaderHead.Hash, e.ObservedExecutionHead.Hash)
		}
	}
	// An accepted anchor is irreversible: the canonical execution head must
	// still cover it. A head below or beside the anchor is a proven
	// regression, not a stale observation.
	if accepted, ok := o.gc.Anchor(o.id.Network); ok {
		anchor, err := headOfAnchor(accepted.Anchor)
		if err != nil {
			return AnchorResult{}, err
		}
		covers, err := anc.IsAncestorOrSelf(anchor, e.ObservedExecutionHead)
		if err != nil {
			return AnchorResult{}, fmt.Errorf("%w: anchor ancestry: %w", ErrSource, err)
		}
		if !covers {
			return AnchorResult{}, o.fail(fmt.Errorf("%w: execution head %s does not cover the accepted anchor %s",
				ErrRegression, e.ObservedExecutionHead.Hash, anchor.Hash))
		}
	}
	result := AnchorResult{Change: Refused}
	digest, err := HeaderDigest(e.HeaderFinality)
	if err != nil {
		return AnchorResult{}, err
	}
	result.HeaderDigest = digest
	if e.HeaderFinality.Attested {
		candidate := e.HeaderFinality.Finalized
		canonical, err := anc.IsAncestorOrSelf(candidate, e.ObservedExecutionHead)
		if err != nil {
			return AnchorResult{}, fmt.Errorf("%w: finalized ancestry: %w", ErrSource, err)
		}
		if canonical {
			change, err := o.acceptAnchor(candidate, result.HeaderDigest, decisionSequence, anc)
			if err != nil {
				return AnchorResult{}, err
			}
			result.Change = change
		}
	}
	if accepted, ok := o.gc.Anchor(o.id.Network); ok {
		anchor, err := headOfAnchor(accepted.Anchor)
		if err != nil {
			return AnchorResult{}, err
		}
		result.Anchor = anchor
	}
	// The full evidence record is stored last, after the anchor decision; a
	// power loss between the two keeps the anchor without the newer
	// evidence, which the next refresh rewrites.
	record := &evidenceRecord{NativeStoreID: o.storeID, Producer: o.producer, Evidence: e}
	record.Digest = digestOf(tagHeader, str(o.storeID), str(o.producer), str(result.HeaderDigest))
	if err := o.put(o.key("evidence"), record); err != nil {
		return AnchorResult{}, err
	}
	return result, nil
}

// acceptAnchor applies the anchor rules to one canonical candidate through
// the anchor owner: the observation and the acceptance stay the engine's
// own sync decisions. The observatory lock is held.
func (o *Observatory) acceptAnchor(candidate decision.Head, headerDigest string,
	decisionSequence uint64, anc Ancestry) (AnchorChange, error) {
	hash, err := hashOfHead(candidate)
	if err != nil {
		return Refused, err
	}
	observation := recoverygc.FinalityObservation{Network: o.id.Network,
		Decision: recoverygc.NativeDecisionRef{NodeInstanceID: o.id.Node, ProducerID: o.id.Producer,
			RunID: o.id.Run, DecisionSequence: decisionSequence},
		Anchor:         recoverygc.AnchorRef{Height: candidate.Number, Hash: hash},
		EvidenceDigest: headerDigest, ProfileRef: AnchorProfile}
	// The accepted anchor of the observation itself decides the change
	// name; the observatory lock serializes the whole observation.
	accepted, has := o.gc.Anchor(o.id.Network)
	result, err := o.gc.ObserveFinality(observation)
	if err != nil {
		return Refused, err
	}
	if has {
		anchor, err := headOfAnchor(accepted.Anchor)
		if err != nil {
			return Refused, err
		}
		switch result {
		case recoverygc.ObservationSame:
			return Unchanged, nil
		case recoverygc.ObservationCandidate:
			committed, err := o.gc.CommitAnchor(o.id.Network)
			if err != nil {
				return Refused, err
			}
			return anchorChange(has, committed, accepted), nil
		case recoverygc.ObservationUnavailable:
			return Refused, fmt.Errorf("%w: ancestry of the candidate %s against the accepted anchor: %w",
				ErrSource, candidate.Hash, recoverygc.ErrAncestryUnknown)
		}
		// A stale observation of the engine: the candidate does not cover
		// the accepted anchor. A lower snapshot on the accepted chain keeps
		// the anchor and is not a regression; an incomparable candidate —
		// a different hash at the anchor height or an incompatible branch,
		// observed by the authoritative header source — is fatal.
		below, err := anc.IsAncestorOrSelf(candidate, anchor)
		if err != nil {
			return Refused, fmt.Errorf("%w: anchor ancestry: %w", ErrSource, err)
		}
		if below {
			return Stale, nil
		}
		return Refused, o.fail(fmt.Errorf("%w: header source %s does not cover the accepted anchor %s",
			ErrRegression, candidate.Hash, anchor.Hash))
	}
	committed, err := o.gc.CommitAnchor(o.id.Network)
	if err != nil {
		return Refused, err
	}
	return anchorChange(has, committed, accepted), nil
}

// anchorChange names what the committed anchor is relative to the
// previously accepted one: a new acceptance with none before, an advance
// over it, or the same anchor observed again.
func anchorChange(had bool, committed, accepted recoverygc.AcceptedGcAnchor) AnchorChange {
	if !had {
		return Accepted
	}
	if committed.Anchor == accepted.Anchor {
		return Unchanged
	}
	return Advanced
}
