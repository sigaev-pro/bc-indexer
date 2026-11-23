// Package finality is the Ethereum finality owner of E05-01.eth_finality.
// It turns the native safe and finalized references of Geth into typed
// observations with provenance (NATIVE_LIVE, NATIVE_RESTART or
// RETAINED_DURABLE), drives one FINALITY decision per native setter call,
// restart or clear through the lifecycle gate and the decision bridge,
// refreshes the evidence at startup without a new head event, and owns the
// durable GC anchor: an accepted anchor derives only from an authoritative
// finalized observation on the canonical execution ancestry, never from
// safe, and a new authoritative SetFinalized below the accepted anchor or
// with an incompatible hash stops the native owner before any READY,
// publication or GC of the conflicting decision. The design is in
// README.md. The package imports only the standard library and the shared
// admission, decision and lifecycle packages.
package finality

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
)

// keyPrefix starts every key. Every key is longer than 33 bytes and starts
// with "e", so the native offline pruner never deletes it.
const keyPrefix = "extractor-finality-v1/"

// Domain tags of the digests, each followed by one zero byte.
const (
	tagObservation = "extractor-finality-observation-v1"
	tagRestart     = "extractor-finality-restart-v1"
	tagAnchor      = "extractor-finality-anchor-v1"
)

// fields are the native finality fields, in the fixed order the tracker
// uses for deterministic batches.
var fields = []lifecycle.Field{lifecycle.Safe, lifecycle.Finalized}

var (
	// ErrInvalid rejects a malformed argument.
	ErrInvalid = errors.New("invalid finality argument")
	// ErrCorrupt reports a damaged or contradictory durable record.
	ErrCorrupt = errors.New("corrupt finality record")
	// ErrInvariant reports a durable record of another store or producer.
	ErrInvariant = errors.New("source invariant error")
	// ErrSource reports a finality evidence read that failed: a native
	// pointer lookup or an ancestry lookup error. It is never replaced by
	// Unknown evidence and never moves the anchor.
	ErrSource = errors.New("finality evidence source error")
	// ErrState rejects a step that does not apply to the tracker state, for
	// example an anchor from an unknown finalized reference.
	ErrState = errors.New("wrong finality state")
	// ErrRegression refuses a GC anchor candidate that is neither the
	// accepted anchor nor its descendant. The accepted anchor stays; the
	// refusal itself is not a proven finality regression.
	ErrRegression = errors.New("anchor candidate regresses below the accepted anchor")
	// ErrFatal reports a proven fatal finality regression: a new
	// authoritative finalized decision below the accepted irreversible
	// anchor or with an incompatible hash, or a rewind that clears the
	// anchored finalized reference. It stops the native owner; it is never
	// a stale-callback dismissal.
	ErrFatal = errors.New("fatal finality regression")
	// ErrFailed reports a tracker that stopped after an error.
	ErrFailed = errors.New("finality tracker stopped after an error")
)

var (
	digestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	storeIDPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._:-]{0,127}$`)
)

// Origin is the provenance of an observation (EvidenceOrigin of
// CURRENT_SEMANTICS).
type Origin string

// Observation origins.
const (
	// OriginLive observed the reference in a live native consensus setter
	// call of this run.
	OriginLive Origin = "NATIVE_LIVE"
	// OriginRestart observed the reference at startup: the native startup
	// restore, refreshed by the recovery driver inside its startup pass.
	OriginRestart Origin = "NATIVE_RESTART"
	// OriginRetained marks previously saved evidence that a lower or
	// unknown current observation did not destroy. Retained evidence is
	// never presented as a fresh native read.
	OriginRetained Origin = "RETAINED_DURABLE"
)

// Reason is why a reference is Unknown.
type Reason string

// Unknown reasons.
const (
	// NotObserved means the reference was never observed.
	NotObserved Reason = "NOT_OBSERVED"
	// ReferenceUnset means a real native write unset the reference
	// (SetSafe or SetFinalized with nil in a rewind): the native reference
	// holds none, which is not the Engine API zero-hash no-op.
	ReferenceUnset Reason = "NATIVE_REFERENCE_UNSET"
)

// Basis is the safe_basis of the Ethereum evidence: how the current safe
// reference was established.
type Basis string

// Safe bases.
const (
	// ConsensusSetter: the live consensus setter set safe (forkchoice).
	ConsensusSetter Basis = "CONSENSUS_SETTER"
	// StartupFinalizedFallback: startup restored safe from the persisted
	// finalized header, so safe may be lower than a previous live safe.
	StartupFinalizedFallback Basis = "STARTUP_FINALIZED_FALLBACK"
	// UnsetBasis: no basis is established (safe Unknown, or a field that
	// carries no basis).
	UnsetBasis Basis = "UNSET"
)

// Ref is one block reference (AnchorRef of CURRENT_SEMANTICS): a height
// and a 64-digit lowercase hash. The all-zero hash never encodes a known
// block.
type Ref struct {
	Height uint64 `json:"height"`
	Hash   string `json:"hash"`
}

// Observation is one typed finality reference: Known with its block and
// origin, or Unknown with its reason.
type Observation struct {
	// Known reports whether the reference holds a block.
	Known bool
	// Block is the observed block; meaningful when Known.
	Block Ref
	// Origin is the provenance of the observation; meaningful when Known.
	Origin Origin
	// Reason is why the reference is Unknown; meaningful when !Known.
	Reason Reason
}

// KnownObservation returns a Known observation of block with origin.
func KnownObservation(block Ref, origin Origin) Observation {
	return Observation{Known: true, Block: block, Origin: origin}
}

// UnknownObservation returns an Unknown observation with reason.
func UnknownObservation(reason Reason) Observation {
	return Observation{Reason: reason}
}

// Evidence is the EthereumEvidence snapshot: the common observed execution
// head plus the typed safe and finalized references and the safe basis.
type Evidence struct {
	Head      Ref
	Safe      Observation
	Finalized Observation
	Basis     Basis
}

// Status is the per-activation finality projection of the evidence.
type Status string

// Projection statuses of CURRENT_SEMANTICS (Ethereum evidence).
const (
	// StatusFinalized: known finalized covers the activation by ancestry.
	StatusFinalized Status = "ETH_FINALIZED"
	// StatusSafe: known safe covers the activation, finalized does not.
	StatusSafe Status = "ETH_SAFE"
	// StatusUnfinalized: references are known but none covers the
	// activation.
	StatusUnfinalized Status = "ETH_CANONICAL_UNFINALIZED"
	// StatusUnknown: the necessary references are unknown.
	StatusUnknown Status = "ETH_FINALITY_UNKNOWN"
)

// Ancestry reports whether ancestor is block itself or an ancestor of
// block on the canonical chain. A lookup error must be returned, never
// reported as false: the projection and the anchor rules never turn an
// unavailable header into negative or Unknown evidence.
type Ancestry func(ancestor, block Ref) (bool, error)

// StatusOf projects the evidence onto one activation block. Known finalized
// covering the activation by ancestry wins; otherwise known safe covering
// it; known references without coverage leave the activation unfinalized;
// with no known reference at all the projection is unknown. An unknown safe
// never cancels a known finalized.
func StatusOf(activation Ref, ev Evidence, anc Ancestry) (Status, error) {
	if anc == nil {
		return StatusUnknown, fmt.Errorf("%w: missing ancestry oracle", ErrInvalid)
	}
	if ev.Finalized.Known {
		covers, err := anc(activation, ev.Finalized.Block)
		if err != nil {
			return StatusUnknown, err
		}
		if covers {
			return StatusFinalized, nil
		}
	}
	if ev.Safe.Known {
		covers, err := anc(activation, ev.Safe.Block)
		if err != nil {
			return StatusUnknown, err
		}
		if covers {
			return StatusSafe, nil
		}
	}
	if ev.Finalized.Known || ev.Safe.Known {
		return StatusUnfinalized, nil
	}
	return StatusUnknown, nil
}

// DecisionRef identifies the FINALITY decision that produced an
// observation: the producer run and the decision sequence.
type DecisionRef struct {
	Producer string `json:"producer"`
	Sequence uint64 `json:"sequence"`
}

// Anchor is the accepted GC anchor: an irreversible finalized block, the
// digest of the finalized observation record it derives from, and the
// authoritative FINALITY decision that produced that observation.
type Anchor struct {
	Block          Ref         `json:"block"`
	EvidenceDigest string      `json:"evidence_digest"`
	Decision       DecisionRef `json:"decision"`
	Digest         string      `json:"digest"`
}

// observationRecord is the durable typed observation of one field.
type observationRecord struct {
	NativeStoreID string          `json:"native_store_id"`
	Producer      string          `json:"producer"`
	Field         lifecycle.Field `json:"field"`
	Known         bool            `json:"known"`
	Block         Ref             `json:"block"`
	Origin        Origin          `json:"origin"`
	Basis         Basis           `json:"basis"`
	Reason        Reason          `json:"reason"`
	Decision      *DecisionRef    `json:"decision,omitempty"`
	Digest        string          `json:"digest"`
}

// anchorRecord is the durable accepted anchor with its store identity.
type anchorRecord struct {
	NativeStoreID  string      `json:"native_store_id"`
	Producer       string      `json:"producer"`
	Block          Ref         `json:"block"`
	EvidenceDigest string      `json:"evidence_digest"`
	Decision       DecisionRef `json:"decision"`
	Digest         string      `json:"digest"`
}

func u64(value uint64) []byte { return binary.BigEndian.AppendUint64(nil, value) }

func str(value string) []byte { return append(u64(uint64(len(value))), value...) }

func digestOf(tag string, parts ...[]byte) string {
	h := sha256.New()
	h.Write(append([]byte(tag), 0))
	for _, part := range parts {
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func rawHash(value string) ([]byte, error) {
	if !digestPattern.MatchString(value) {
		return nil, fmt.Errorf("%w: hash %q", ErrInvalid, value)
	}
	return hex.DecodeString(value)
}

func refBytes(r Ref) ([]byte, error) {
	hash, err := rawHash(r.Hash)
	if err != nil {
		return nil, err
	}
	return append(u64(r.Height), hash...), nil
}

func headBytes(h decision.Head) ([]byte, error) {
	hash, err := rawHash(h.Hash)
	if err != nil {
		return nil, err
	}
	return append(u64(h.Number), hash...), nil
}

func decisionBytes(d *DecisionRef) []byte {
	if d == nil {
		return []byte{0}
	}
	return append([]byte{1}, append(str(d.Producer), u64(d.Sequence)...)...)
}

// ObservationDigest returns the evidence digest of one typed observation
// record: the field, the known branch (block, origin and basis, or the
// unknown reason) and the FINALITY decision it derives from.
func ObservationDigest(r observationRecord) (string, error) {
	if r.Field != lifecycle.Safe && r.Field != lifecycle.Finalized {
		return "", fmt.Errorf("%w: finality field %q", ErrInvalid, r.Field)
	}
	parts := [][]byte{str(string(r.Field))}
	if r.Known {
		block, err := refBytes(r.Block)
		if err != nil {
			return "", err
		}
		basis := UnsetBasis
		if r.Field == lifecycle.Safe {
			basis = r.Basis
		}
		parts = append(parts, []byte{1}, block, str(string(r.Origin)), str(string(basis)))
	} else {
		parts = append(parts, []byte{0}, str(string(r.Reason)))
	}
	parts = append(parts, decisionBytes(r.Decision))
	return digestOf(tagObservation, parts...), nil
}

// AnchorDigest returns the digest of an accepted anchor record.
func AnchorDigest(storeID string, a anchorRecord) (string, error) {
	block, err := refBytes(a.Block)
	if err != nil {
		return "", err
	}
	evidence, err := rawHash(a.EvidenceDigest)
	if err != nil {
		return "", err
	}
	return digestOf(tagAnchor, str(storeID), str(a.Producer), block, evidence,
		decisionBytes(&a.Decision)), nil
}

// RestartDigest is the evidence digest of one startup reconcile decision:
// the field, the startup-observed block and the unchanged head read under
// the guard.
func RestartDigest(field lifecycle.Field, block, head decision.Head) (string, error) {
	if field != lifecycle.Safe && field != lifecycle.Finalized {
		return "", fmt.Errorf("%w: finality field %q", ErrInvalid, field)
	}
	b, err := refBytes(Ref{Height: block.Number, Hash: block.Hash})
	if err != nil {
		return "", err
	}
	h, err := headBytes(head)
	if err != nil {
		return "", err
	}
	return digestOf(tagRestart, str(string(field)), b, h), nil
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

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

// fieldState is the durable state of one field: the current record and the
// retained record that a lower or unknown current observation kept.
type fieldState struct {
	current  *observationRecord
	retained *observationRecord
}

// Planned is one planned typed observation change of Set, Clear or
// Startup: the FINALITY intent recorded before the native mutation, and
// the next observation record. Commit makes it durable with the native
// marker.
type Planned struct {
	// Noop reports a no-op change: a zero new hash or an unchanged
	// reference. No setter decision runs and no record changes; the pass
	// may still rewrite an unchanged reference without a marker.
	Noop bool
	// Intent is the FINALITY intent of the change; unset when Noop.
	Intent decision.IntentRecord
	// Next is the next observation record; unset when Noop.
	Next observationRecord
}

// update is one deduplicated field change of a Commit batch.
type update struct {
	intent   decision.IntentRecord
	next     observationRecord
	retained *observationRecord
}

// retainedFrom returns the retained record that committing next keeps for
// the previous current record: previous Known evidence that the next
// record lowers or unsets is not destroyed, and keeps its block and
// decision binding with RETAINED_DURABLE provenance and the matching
// digest. An equal or higher next reference, or an unknown previous one,
// retains nothing.
func retainedFrom(prev, next *observationRecord) *observationRecord {
	if prev == nil || !prev.Known {
		return nil
	}
	if next.Known && next.Block.Height >= prev.Block.Height {
		return nil
	}
	kept := *prev
	kept.Origin = OriginRetained
	digest, err := ObservationDigest(kept)
	if err != nil {
		return nil
	}
	kept.Digest = digest
	return &kept
}

// Tracker owns the typed Ethereum finality evidence and the GC anchor of
// one producer run. Its calls need the held observation guard token; the
// native patches call Set inside the pass of the mapped finality caller
// before the native setter mutates state, and Commit in the final
// authoritative batch of the change. A Tracker is not safe for concurrent
// use.
type Tracker struct {
	db       admission.DeleteStore
	adm      lifecycle.Checker
	producer admission.Producer
	owner    string
	storeID  string
	anc      Ancestry
	head     decision.Head
	fields   map[lifecycle.Field]*fieldState
	anchor   *anchorRecord
	err      error
}

func (tr *Tracker) key(suffix string) []byte {
	return []byte(keyPrefix + tr.owner + "/" + suffix)
}

func (tr *Tracker) enter(t *admission.Token) error {
	if err := tr.adm.Check(t); err != nil {
		return err
	}
	if tr.err != nil {
		return fmt.Errorf("%w: %v", ErrFailed, tr.err)
	}
	return nil
}

func (tr *Tracker) stop(err error) error {
	tr.err = err
	return err
}

func validRef(r Ref, zeroOK bool) error {
	if !zeroOK && r.Hash == lifecycle.ZeroHash {
		return fmt.Errorf("%w: zero hash", ErrInvalid)
	}
	if !digestPattern.MatchString(r.Hash) {
		return fmt.Errorf("%w: hash %q", ErrInvalid, r.Hash)
	}
	return nil
}

// loadRecord reads one durable record, or nil when its key holds none.
func (tr *Tracker) loadRecord(key []byte, value any) (bool, error) {
	ok, err := tr.db.Has(key)
	if err != nil || !ok {
		return false, err
	}
	data, err := tr.db.Get(key)
	if err != nil {
		return false, err
	}
	if err := strictDecode(data, value); err != nil {
		return false, fmt.Errorf("%w: %s: %v", ErrCorrupt, key, err)
	}
	return true, nil
}

func (tr *Tracker) checkObservation(r *observationRecord, retained bool) error {
	if r.NativeStoreID != tr.storeID || r.Producer != tr.owner {
		return fmt.Errorf("%w: observation of %s %s, not %s %s", ErrInvariant, r.NativeStoreID, r.Producer,
			tr.storeID, tr.owner)
	}
	digest, err := ObservationDigest(*r)
	if err != nil || digest != r.Digest {
		return fmt.Errorf("%w: observation digest of %s", ErrCorrupt, r.Field)
	}
	if r.Known {
		if validRef(r.Block, false) != nil {
			return fmt.Errorf("%w: observation block of %s", ErrCorrupt, r.Field)
		}
		if retained != (r.Origin == OriginRetained) {
			return fmt.Errorf("%w: %s observation origin %s", ErrCorrupt, r.Field, r.Origin)
		}
	} else if r.Reason != NotObserved && r.Reason != ReferenceUnset {
		return fmt.Errorf("%w: unknown reason of %s", ErrCorrupt, r.Field)
	}
	if r.Field == lifecycle.Safe && r.Known {
		switch r.Basis {
		case ConsensusSetter, StartupFinalizedFallback:
		default:
			return fmt.Errorf("%w: safe basis %q", ErrCorrupt, r.Basis)
		}
	}
	return nil
}

// Open syncs the store and opens the tracker of the producer run of adm.
// It loads the durable observation and anchor records; until a startup
// refresh or a live observation re-establishes them, the loaded records
// are retained evidence, never a fresh native read. anc resolves ancestry
// for the anchor rules and the projection.
func Open(t *admission.Token, db admission.DeleteStore, adm lifecycle.Checker, storeID string,
	anc Ancestry) (*Tracker, error) {
	if adm == nil || db == nil {
		return nil, fmt.Errorf("%w: missing admission or store", ErrInvalid)
	}
	if err := adm.Check(t); err != nil {
		return nil, err
	}
	if !storeIDPattern.MatchString(storeID) {
		return nil, fmt.Errorf("%w: native store ID %q", ErrInvalid, storeID)
	}
	if anc == nil {
		return nil, fmt.Errorf("%w: missing ancestry oracle", ErrInvalid)
	}
	if err := db.SyncKeyValue(); err != nil {
		return nil, err
	}
	p := adm.Producer()
	tr := &Tracker{db: db, adm: adm, producer: p, owner: p.ProducerID + "." + p.RunID, storeID: storeID,
		anc: anc, fields: map[lifecycle.Field]*fieldState{}}
	for _, field := range fields {
		state := &fieldState{}
		var record observationRecord
		found, err := tr.loadRecord(tr.key(fieldKey(field)), &record)
		if err != nil {
			return nil, err
		}
		if found {
			if err := tr.checkObservation(&record, false); err != nil {
				return nil, err
			}
			if record.Field != field {
				return nil, fmt.Errorf("%w: %s record holds field %s", ErrCorrupt, field, record.Field)
			}
			state.current = &record
		}
		var retained observationRecord
		found, err = tr.loadRecord(tr.key(fieldKey(field)+"/retained"), &retained)
		if err != nil {
			return nil, err
		}
		if found {
			if err := tr.checkObservation(&retained, true); err != nil {
				return nil, err
			}
			if retained.Field != field {
				return nil, fmt.Errorf("%w: retained %s record", ErrCorrupt, field)
			}
			state.retained = &retained
		}
		tr.fields[field] = state
	}
	var anchor anchorRecord
	found, err := tr.loadRecord(tr.key("anchor"), &anchor)
	if err != nil {
		return nil, err
	}
	if found {
		if anchor.NativeStoreID != tr.storeID || anchor.Producer != tr.owner {
			return nil, fmt.Errorf("%w: anchor of %s %s, not %s %s", ErrInvariant, anchor.NativeStoreID,
				anchor.Producer, tr.storeID, tr.owner)
		}
		digest, err := AnchorDigest(tr.storeID, anchor)
		if err != nil || digest != anchor.Digest || validRef(anchor.Block, false) != nil ||
			!digestPattern.MatchString(anchor.EvidenceDigest) {
			return nil, fmt.Errorf("%w: anchor record", ErrCorrupt)
		}
		tr.anchor = &anchor
	}
	return tr, nil
}

// fieldKey is the store suffix of one field.
func fieldKey(field lifecycle.Field) string { return strings.ToLower(string(field)) }

// fatalBelowAnchor reports whether block conflicts with the accepted
// anchor: it is neither the anchor block nor its descendant. A lookup error
// is ErrSource and stops nothing.
func (tr *Tracker) fatalBelowAnchor(block Ref) (bool, error) {
	if tr.anchor == nil {
		return false, nil
	}
	if tr.anchor.Block == block {
		return false, nil
	}
	descendant, err := tr.anc(tr.anchor.Block, block)
	if err != nil {
		return false, fmt.Errorf("%w: ancestry of %s against anchor %s: %w", ErrSource, block.Hash,
			tr.anchor.Block.Hash, err)
	}
	return !descendant, nil
}

// Set plans the typed observation of one live native finality setter call:
// it records one FINALITY decision through the pass of the mapped caller,
// before the native setter mutates state. A zero new hash or an unchanged
// reference is a no-op: no decision runs, nothing is declared final, and
// the old native reference stays. A FINALIZED change below the accepted
// anchor, or with an incompatible hash, is a proven fatal regression: the
// tracker stops before any evidence record, marker or GC of the conflicting
// decision. head is the unchanged native head; the change may arrive
// without a new block.
func (tr *Tracker) Set(t *admission.Token, pass *lifecycle.Pass, bridge lifecycle.Intender,
	head decision.Head, c lifecycle.FinalityChange) (Planned, error) {
	if err := tr.enter(t); err != nil {
		return Planned{}, err
	}
	if pass == nil || bridge == nil {
		return Planned{}, fmt.Errorf("%w: missing pass or bridge", ErrInvalid)
	}
	if c.Field != lifecycle.Safe && c.Field != lifecycle.Finalized {
		return Planned{}, fmt.Errorf("%w: finality field %q", ErrInvalid, c.Field)
	}
	if err := validRef(Ref{Height: c.New.Number, Hash: c.New.Hash}, true); err != nil {
		return Planned{}, err
	}
	if c.Field == lifecycle.Finalized && c.New.Hash != lifecycle.ZeroHash {
		conflict, err := tr.fatalBelowAnchor(Ref{Height: c.New.Number, Hash: c.New.Hash})
		if err != nil {
			return Planned{}, err
		}
		if conflict {
			return Planned{}, tr.stop(fmt.Errorf("%w: authoritative SetFinalized %d %s conflicts with anchor %d %s",
				ErrFatal, c.New.Number, c.New.Hash, tr.anchor.Block.Height, tr.anchor.Block.Hash))
		}
	}
	record, err := pass.Finality(bridge, head, c, false)
	if err != nil {
		if errors.Is(err, lifecycle.ErrNoop) {
			return Planned{Noop: true}, nil
		}
		return Planned{}, err
	}
	basis := UnsetBasis
	if c.Field == lifecycle.Safe {
		basis = ConsensusSetter
	}
	next := observationRecord{NativeStoreID: tr.storeID, Producer: tr.owner, Field: c.Field, Known: true,
		Block:  Ref{Height: c.New.Number, Hash: c.New.Hash},
		Origin: OriginLive, Basis: basis,
		Decision: &DecisionRef{Producer: tr.owner, Sequence: record.Sequence}}
	next.Digest, err = ObservationDigest(next)
	if err != nil {
		return Planned{}, err
	}
	return Planned{Intent: record, Next: next}, nil
}

// Clear plans the typed observation of one native rewind clear of a field
// (SetSafe or SetFinalized with nil): a real native write, recorded as one
// FINALITY decision with the caller's clear-evidence digest, after which
// the reference is Unknown because the native reference holds none. This
// is not the Engine API zero-hash no-op. Clearing FINALIZED while an anchor
// is accepted is fatal: the rewind deleted the anchored finalized ancestry.
func (tr *Tracker) Clear(t *admission.Token, pass *lifecycle.Pass, bridge lifecycle.Intender,
	head decision.Head, field lifecycle.Field, evidenceDigest string) (Planned, error) {
	if err := tr.enter(t); err != nil {
		return Planned{}, err
	}
	if pass == nil || bridge == nil {
		return Planned{}, fmt.Errorf("%w: missing pass or bridge", ErrInvalid)
	}
	if field != lifecycle.Safe && field != lifecycle.Finalized {
		return Planned{}, fmt.Errorf("%w: finality field %q", ErrInvalid, field)
	}
	if !digestPattern.MatchString(evidenceDigest) {
		return Planned{}, fmt.Errorf("%w: clear evidence digest %q", ErrInvalid, evidenceDigest)
	}
	if field == lifecycle.Finalized && tr.anchor != nil {
		return Planned{}, tr.stop(fmt.Errorf("%w: rewind cleared the anchored finalized reference %d %s",
			ErrFatal, tr.anchor.Block.Height, tr.anchor.Block.Hash))
	}
	record, err := pass.Intend(bridge, decision.Intent{Kind: decision.Finality, OldHead: head, NewHead: head,
		EvidenceDigest: evidenceDigest, Events: false})
	if err != nil {
		return Planned{}, err
	}
	next := observationRecord{NativeStoreID: tr.storeID, Producer: tr.owner, Field: field,
		Reason: ReferenceUnset, Basis: UnsetBasis,
		Decision: &DecisionRef{Producer: tr.owner, Sequence: record.Sequence}}
	next.Digest, err = ObservationDigest(next)
	if err != nil {
		return Planned{}, err
	}
	return Planned{Intent: record, Next: next}, nil
}

// Startup refreshes the typed evidence after a restart, inside the pass of
// the startup reconcile caller, without a new head event. finalized reads
// the persisted native finalized reference under the guard: known=false
// means the native store holds none, an error is a source error and never
// Unknown evidence. Native startup restores safe from the found finalized
// header, so the refreshed safe observation carries the
// STARTUP_FINALIZED_FALLBACK basis with NATIVE_RESTART origin even when it
// is lower than the previous live safe: that is not fatal and never a GC
// basis. A reference unchanged by the refresh stays as it is; replaying a
// saved decision creates no new decision.
//
// One call plans at most the next change of the refresh, in the order
// finalized then safe, because the decision bridge refuses a second intent
// while the first is unresolved. The recovery driver commits and resolves
// each returned plan and calls Startup again; more=false reports that the
// refresh is complete. A startup-observed finalized that conflicts with the
// accepted anchor is fatal (an operator rewind or a damaged native store
// below the irreversible anchor).
func (tr *Tracker) Startup(t *admission.Token, pass *lifecycle.Pass, bridge lifecycle.Intender,
	head decision.Head, finalized func() (decision.Head, bool, error)) (Planned, bool, error) {
	if err := tr.enter(t); err != nil {
		return Planned{}, false, err
	}
	if pass == nil || bridge == nil || finalized == nil {
		return Planned{}, false, fmt.Errorf("%w: missing pass, bridge or finalized reader", ErrInvalid)
	}
	block, known, err := finalized()
	if err != nil {
		return Planned{}, false, fmt.Errorf("%w: finalized reference: %w", ErrSource, err)
	}
	if known && validRef(Ref{Height: block.Number, Hash: block.Hash}, false) != nil {
		return Planned{}, false, fmt.Errorf("%w: finalized reference %d %s", ErrInvalid, block.Number, block.Hash)
	}
	tr.head = head
	if !known {
		return Planned{}, false, nil
	}
	conflict, err := tr.fatalBelowAnchor(Ref{Height: block.Number, Hash: block.Hash})
	if err != nil {
		return Planned{}, false, err
	}
	if conflict {
		return Planned{}, false, tr.stop(fmt.Errorf("%w: startup finalized %d %s conflicts with anchor %d %s",
			ErrFatal, block.Number, block.Hash, tr.anchor.Block.Height, tr.anchor.Block.Hash))
	}
	// Finalized: a refresh that found the same reference replays the saved
	// decision; only a changed reference makes a new decision.
	finState := tr.fields[lifecycle.Finalized]
	if finState.current == nil || !finState.current.Known || finState.current.Block != (Ref{Height: block.Number,
		Hash: block.Hash}) {
		next := observationRecord{NativeStoreID: tr.storeID, Producer: tr.owner, Field: lifecycle.Finalized,
			Known: true, Block: Ref{Height: block.Number, Hash: block.Hash}, Origin: OriginRestart,
			Basis: UnsetBasis}
		return tr.startupPlan(pass, bridge, head, block, next)
	}
	// Safe: startup restored it from the finalized header. A safe record
	// that already is that fallback replays the saved decision; a live or
	// higher previous safe becomes retained evidence in Commit.
	safeState := tr.fields[lifecycle.Safe]
	fallback := Ref{Height: block.Number, Hash: block.Hash}
	if safeState.current == nil || !safeState.current.Known || safeState.current.Basis != StartupFinalizedFallback ||
		safeState.current.Block != fallback {
		next := observationRecord{NativeStoreID: tr.storeID, Producer: tr.owner, Field: lifecycle.Safe,
			Known: true, Block: fallback, Origin: OriginRestart, Basis: StartupFinalizedFallback}
		return tr.startupPlan(pass, bridge, head, block, next)
	}
	return Planned{}, false, nil
}

// startupPlan records the FINALITY intent of one startup reconcile change
// with its restart evidence digest.
func (tr *Tracker) startupPlan(pass *lifecycle.Pass, bridge lifecycle.Intender, head decision.Head,
	block decision.Head, next observationRecord) (Planned, bool, error) {
	digest, err := RestartDigest(next.Field, block, head)
	if err != nil {
		return Planned{}, false, err
	}
	record, err := pass.Intend(bridge, decision.Intent{Kind: decision.Finality, OldHead: head, NewHead: head,
		EvidenceDigest: digest, Events: false})
	if err != nil {
		return Planned{}, false, err
	}
	next.Decision = &DecisionRef{Producer: tr.owner, Sequence: record.Sequence}
	next.Digest, err = ObservationDigest(next)
	if err != nil {
		return Planned{}, false, err
	}
	return Planned{Intent: record, Next: next}, true, nil
}

// Commit makes one or more planned changes durable in the final
// authoritative batch of the change: the typed observation records, the
// retained records they keep, and the native decision markers, in one
// store sync. A failed or interrupted commit changes nothing; the caller
// replays the plan after recovery.
func (tr *Tracker) Commit(t *admission.Token, plans ...Planned) error {
	if err := tr.enter(t); err != nil {
		return err
	}
	updates := map[lifecycle.Field]*update{}
	for i := range plans {
		p := &plans[i]
		if p.Noop {
			return fmt.Errorf("%w: commit of a no-op change", ErrInvalid)
		}
		if !containsField(p.Next.Field) {
			return fmt.Errorf("%w: planned field %q", ErrInvalid, p.Next.Field)
		}
		if p.Next.Field == lifecycle.Safe && p.Next.Known {
			switch p.Next.Basis {
			case ConsensusSetter, StartupFinalizedFallback:
			default:
				return fmt.Errorf("%w: planned safe basis %q", ErrInvalid, p.Next.Basis)
			}
		}
		if _, err := decision.MarkerValue(p.Intent); err != nil {
			return err
		}
		if u, ok := updates[p.Next.Field]; ok {
			if u.next.Digest != p.Next.Digest {
				return fmt.Errorf("%w: two different plans for %s", ErrInvalid, p.Next.Field)
			}
			continue
		}
		updates[p.Next.Field] = &update{intent: p.Intent, next: p.Next}
	}
	// Compute the retained evidence of every change before the first
	// write, so one batch carries the current record, the marker, and the
	// record that a lower or unknown observation keeps.
	for _, field := range fields {
		u, ok := updates[field]
		if !ok {
			continue
		}
		next := u.next
		u.retained = retainedFrom(tr.fields[field].current, &next)
	}
	for _, field := range fields {
		u, ok := updates[field]
		if !ok {
			continue
		}
		if err := tr.db.Put(tr.key(fieldKey(field)), mustJSON(u.next)); err != nil {
			return err
		}
		markerKey := decision.MarkerKey(tr.producer, u.next.Decision.Sequence)
		marker, err := decision.MarkerValue(u.intent)
		if err != nil {
			return err
		}
		if err := tr.db.Put(markerKey, marker); err != nil {
			return err
		}
		retainedKey := tr.key(fieldKey(field) + "/retained")
		if u.retained != nil {
			if err := tr.db.Put(retainedKey, mustJSON(*u.retained)); err != nil {
				return err
			}
		} else if tr.fields[field].retained != nil {
			if err := tr.db.Delete(retainedKey); err != nil {
				return err
			}
		}
	}
	if err := tr.db.SyncKeyValue(); err != nil {
		return err
	}
	// The batch is durable; the in-memory state follows it.
	for _, field := range fields {
		u, ok := updates[field]
		if !ok {
			continue
		}
		state := tr.fields[field]
		state.retained = u.retained
		next := u.next
		state.current = &next
		tr.head = u.intent.OldHead
	}
	return nil
}

func containsField(field lifecycle.Field) bool {
	for _, f := range fields {
		if f == field {
			return true
		}
	}
	return false
}

// Resolver resolves a committed FINALITY decision; *decision.Bridge
// implements it.
type Resolver interface {
	Resolve(t *admission.Token, seq uint64) (decision.DecidedRecord, error)
}

// Resolve records NATIVE_DECIDED of one committed change through the
// bridge. The bridge itself refuses a change whose native marker is not
// durable; the tracker keeps no separate commitment state, so recovery
// after a power loss resolves the same way.
func (tr *Tracker) Resolve(t *admission.Token, bridge Resolver, seq uint64) (decision.DecidedRecord, error) {
	if err := tr.enter(t); err != nil {
		return decision.DecidedRecord{}, err
	}
	if bridge == nil {
		return decision.DecidedRecord{}, fmt.Errorf("%w: missing bridge", ErrInvalid)
	}
	return bridge.Resolve(t, seq)
}

// Evidence returns the current typed evidence snapshot. Until a startup
// refresh or a live observation established the head it is ErrState.
func (tr *Tracker) Evidence() (Evidence, error) {
	if tr.head == (decision.Head{}) {
		return Evidence{}, fmt.Errorf("%w: no observed head", ErrState)
	}
	ev := Evidence{Head: Ref{Height: tr.head.Number, Hash: tr.head.Hash}, Basis: UnsetBasis,
		Safe: UnknownObservation(NotObserved), Finalized: UnknownObservation(NotObserved)}
	if state := tr.fields[lifecycle.Safe]; state.current != nil {
		ev.Safe = observationOf(*state.current)
		if state.current.Known {
			ev.Basis = state.current.Basis
		}
	}
	if state := tr.fields[lifecycle.Finalized]; state.current != nil {
		ev.Finalized = observationOf(*state.current)
	}
	return ev, nil
}

// Retained returns the retained evidence of one field: previously saved
// evidence that a lower or unknown current observation kept, with
// RETAINED_DURABLE provenance. It is never a fresh native read.
func (tr *Tracker) Retained(field lifecycle.Field) (Observation, bool) {
	state, ok := tr.fields[field]
	if !ok || state.retained == nil {
		return Observation{}, false
	}
	return observationOf(*state.retained), true
}

func observationOf(r observationRecord) Observation {
	if r.Known {
		return KnownObservation(r.Block, r.Origin)
	}
	return UnknownObservation(r.Reason)
}

// StatusOf projects the current evidence onto one activation block.
func (tr *Tracker) StatusOf(activation Ref) (Status, error) {
	ev, err := tr.Evidence()
	if err != nil {
		return StatusUnknown, err
	}
	return StatusOf(activation, ev, tr.anc)
}

// Anchor returns the accepted GC anchor.
func (tr *Tracker) Anchor() (Anchor, bool) {
	if tr.anchor == nil {
		return Anchor{}, false
	}
	return anchorOf(*tr.anchor), true
}

func anchorOf(r anchorRecord) Anchor {
	return Anchor{Block: r.Block, EvidenceDigest: r.EvidenceDigest, Decision: r.Decision, Digest: r.Digest}
}

// Accept accepts the GC anchor from the current durable finalized
// observation. The candidate is always the finalized reference itself:
// safe is never an anchor source. Acceptance requires the finalized block
// on the canonical execution ancestry of the observed head, the native
// decision marker of its FINALITY decision still durable (the
// authoritative evidence), and ancestry monotonicity: the candidate must
// be the accepted anchor or its descendant. Accepting the same anchor
// again is idempotent. Acceptance and the later release of refs and pins
// are separate sync decisions; a payload ACK is not GC.
func (tr *Tracker) Accept(t *admission.Token) (Anchor, error) {
	if err := tr.enter(t); err != nil {
		return Anchor{}, err
	}
	state := tr.fields[lifecycle.Finalized]
	if state.current == nil || !state.current.Known {
		return Anchor{}, fmt.Errorf("%w: finalized reference is unknown", ErrState)
	}
	fin := *state.current
	if fin.Decision == nil {
		return Anchor{}, fmt.Errorf("%w: finalized observation has no decision", ErrState)
	}
	// The record-level monotonicity needs no observed head: the candidate
	// must be the accepted anchor or its descendant.
	if tr.anchor != nil {
		if tr.anchor.Block == fin.Block {
			return anchorOf(*tr.anchor), nil
		}
		descendant, err := tr.anc(tr.anchor.Block, fin.Block)
		if err != nil {
			return Anchor{}, fmt.Errorf("%w: ancestry of candidate %s: %w", ErrSource, fin.Block.Hash, err)
		}
		if !descendant {
			return Anchor{}, fmt.Errorf("%w: candidate %d %s is below the accepted anchor %d %s", ErrRegression,
				fin.Block.Height, fin.Block.Hash, tr.anchor.Block.Height, tr.anchor.Block.Hash)
		}
	}
	if tr.head == (decision.Head{}) {
		return Anchor{}, fmt.Errorf("%w: no observed head", ErrState)
	}
	onCanonical, err := tr.anc(fin.Block, Ref{Height: tr.head.Number, Hash: tr.head.Hash})
	if err != nil {
		return Anchor{}, fmt.Errorf("%w: ancestry of finalized %s: %w", ErrSource, fin.Block.Hash, err)
	}
	if !onCanonical {
		return Anchor{}, fmt.Errorf("%w: finalized %d %s is not on the canonical execution ancestry", ErrState,
			fin.Block.Height, fin.Block.Hash)
	}
	marker, err := tr.db.Has(decision.MarkerKey(tr.producer, fin.Decision.Sequence))
	if err != nil {
		return Anchor{}, err
	}
	if !marker {
		return Anchor{}, fmt.Errorf("%w: finalized decision %d has no durable marker", ErrState,
			fin.Decision.Sequence)
	}
	record := anchorRecord{NativeStoreID: tr.storeID, Producer: tr.owner, Block: fin.Block,
		EvidenceDigest: fin.Digest, Decision: *fin.Decision}
	digest, err := AnchorDigest(tr.storeID, record)
	if err != nil {
		return Anchor{}, err
	}
	record.Digest = digest
	if err := tr.db.Put(tr.key("anchor"), mustJSON(record)); err != nil {
		return Anchor{}, err
	}
	if err := tr.db.SyncKeyValue(); err != nil {
		return Anchor{}, err
	}
	tr.anchor = &record
	return anchorOf(record), nil
}

// Stopped reports whether the tracker stopped after an error, and the error
// itself.
func (tr *Tracker) Stopped() (bool, error) {
	if tr.err == nil {
		return false, nil
	}
	return true, tr.err
}
