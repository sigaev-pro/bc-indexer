// Package finality is the typed BSC finality evidence seam of
// E05-01.bsc_finality (REQ-LIF-06, AC-LIF-06-01 to 06-03). BSC has two
// different native finality surfaces: the early vote-only result that
// Parlia's GetFinalizedHeader derives from the in-memory VotePool quorum
// (observed at NotifyFinalized), and the header-derived snapshot attestation
// (VoteData Source/Target merged by the native transition logic). This
// package keeps them separate: each has its own typed record, digest and
// provenance, the projection reports a header status and an early status per
// activation instead of one unconditional finalized flag, and the GC anchor
// accepts only the contract-established header Source of an AttestedSnapshot
// on the canonical execution ancestry. The design is in README.md. The
// package imports the standard library and the shared admission, decision
// and lifecycle packages.
package finality

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"regexp"

	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
)

// Domain tags of the digests, each followed by one zero byte. The accepted
// GC anchor is the recovery GC engine's own record; this package carries
// only the header and early evidence digests.
const (
	tagHeader = "extractor-bsc-finality-header-v1"
	tagEarly  = "extractor-bsc-finality-early-v1"
)

// keyPrefix starts every key of this package. Every key is longer than 33
// bytes and starts with "e", so the native offline pruner never deletes it.
const keyPrefix = "extractor-bsc-finality-v1/"

var (
	digestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	storeIDPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._:-]{0,127}$`)
)

var (
	// ErrInvalid rejects a malformed argument.
	ErrInvalid = fmt.Errorf("%w: invalid BSC finality argument", lifecycle.ErrInvalid)
	// ErrSource reports a finality evidence read or ancestry lookup that
	// failed. It is never replaced by the genesis fallback, by unknown
	// references or by empty evidence.
	ErrSource = lifecycle.ErrSource
	// ErrBranch refuses a finality observation whose header or finalized
	// block is not on the canonical execution ancestry: a delayed
	// side-branch or same-height different-hash notification. It changes
	// nothing and is not a regression.
	ErrBranch = fmt.Errorf("%w: side-branch finality observation", lifecycle.ErrInvalid)
	// ErrConflict rejects a repeated observation with different evidence.
	ErrConflict = fmt.Errorf("%w: conflicting BSC finality record", lifecycle.ErrInvalid)
	// ErrCorrupt reports a damaged record.
	ErrCorrupt = lifecycle.ErrCorrupt
	// ErrInvariant reports native data that contradicts the durable records:
	// a proven regression below the accepted anchor.
	ErrInvariant = lifecycle.ErrInvariant
	// ErrFailed reports an observatory that stopped after an error.
	ErrFailed = fmt.Errorf("%w: BSC finality observatory stopped after an error", lifecycle.ErrFailed)
)

// Origin is the provenance of an observation: NATIVE_LIVE was read from the
// live native node in this process, NATIVE_RESTART during the startup
// refresh without a new head event, and RETAINED_DURABLE is a record of an
// earlier process served after a restart. A stored early observation is
// never served as a fresh VotePool result after a restart.
type Origin string

// Origins.
const (
	NativeLive      Origin = "NATIVE_LIVE"
	NativeRestart   Origin = "NATIVE_RESTART"
	RetainedDurable Origin = "RETAINED_DURABLE"
)

// UnknownReason is why a reference observation is unknown.
type UnknownReason string

// Unknown reasons.
const (
	// NotObserved means no observation was made.
	NotObserved UnknownReason = "NOT_OBSERVED"
	// NativeReferenceUnset means the native reference itself is unset.
	NativeReferenceUnset UnknownReason = "NATIVE_REFERENCE_UNSET"
)

// Reference is one native reference observation: a Known block with its
// origin, or Unknown with its reason. The zero hash never encodes a known
// block; a real genesis has its source hash.
type Reference struct {
	Known  bool          `json:"known"`
	Head   decision.Head `json:"head"`
	Origin Origin        `json:"origin"`
	Reason UnknownReason `json:"reason"`
}

// KnownReference returns the Known observation of head with origin.
func KnownReference(head decision.Head, origin Origin) Reference {
	return Reference{Known: true, Head: head, Origin: origin}
}

// UnknownReference returns the Unknown observation with reason.
func UnknownReference(reason UnknownReason) Reference {
	return Reference{Reason: reason}
}

// valid checks one reference against its shape rules.
func (r Reference) valid() error {
	if r.Known {
		if r.Reason != "" {
			return fmt.Errorf("%w: a known reference carries a reason", ErrInvalid)
		}
		if !isOrigin(r.Origin) {
			return fmt.Errorf("%w: origin %q", ErrInvalid, r.Origin)
		}
		return validHead(r.Head)
	}
	if r.Head.Number != 0 || r.Head.Hash != "" {
		return fmt.Errorf("%w: an unknown reference carries a head", ErrInvalid)
	}
	if !isUnknownReason(r.Reason) {
		return fmt.Errorf("%w: unknown reason %q", ErrInvalid, r.Reason)
	}
	return nil
}

func isOrigin(o Origin) bool {
	return o == NativeLive || o == NativeRestart || o == RetainedDurable
}

func isUnknownReason(r UnknownReason) bool {
	return r == NotObserved || r == NativeReferenceUnset
}

// FallbackReason is why a correct header snapshot carries no attestation.
type FallbackReason string

// Fallback reasons.
const (
	// NoAttestation means the snapshot is correct but has no vote
	// attestation: the justified and finalized blocks are the real genesis.
	NoAttestation FallbackReason = "NO_ATTESTATION"
	// PrePlato means the fork has not activated vote attestations yet.
	PrePlato FallbackReason = "PRE_PLATO"
)

func isFallbackReason(r FallbackReason) bool {
	return r == NoAttestation || r == PrePlato
}

// HeaderFinality is the header-derived snapshot finality: an
// AttestedSnapshot (the merged native VoteData, where the target is the
// justified block and the source the finalized block) or a GenesisFallback.
// It is a snapshot projection of the native transition logic, never a single
// signed header attestation.
type HeaderFinality struct {
	// Attested selects the AttestedSnapshot variant; otherwise this is a
	// GenesisFallback.
	Attested bool `json:"attested"`
	// SnapshotHead is the header whose snapshot produced this evidence.
	SnapshotHead decision.Head `json:"snapshot_head"`
	// Justified is the attestation target. Attested only.
	Justified decision.Head `json:"justified"`
	// Finalized is the attestation source: the only block this package ever
	// offers as a GC anchor. Attested only.
	Finalized decision.Head `json:"finalized"`
	// Genesis is the real genesis block of the fallback. Fallback only.
	Genesis decision.Head `json:"genesis"`
	// Reason is the fallback reason. Fallback only.
	Reason FallbackReason `json:"reason"`
}

// AttestedSnapshot returns the header finality of an attested snapshot.
func AttestedSnapshot(snapshotHead, justified, finalized decision.Head) HeaderFinality {
	return HeaderFinality{Attested: true, SnapshotHead: snapshotHead, Justified: justified, Finalized: finalized}
}

// GenesisFallback returns the header finality of a correct snapshot without
// an attestation, where the justified and finalized blocks are the real
// genesis block.
func GenesisFallback(snapshotHead, genesis decision.Head, reason FallbackReason) HeaderFinality {
	return HeaderFinality{SnapshotHead: snapshotHead, Genesis: genesis, Reason: reason}
}

// valid checks one header finality against its shape rules.
func (h HeaderFinality) valid() error {
	if err := validHead(h.SnapshotHead); err != nil {
		return err
	}
	if h.Attested {
		if h.Genesis.Number != 0 || h.Genesis.Hash != "" || h.Reason != "" {
			return fmt.Errorf("%w: an attested snapshot carries fallback fields", ErrInvalid)
		}
		if err := validHead(h.Justified); err != nil {
			return fmt.Errorf("%w: justified: %w", ErrInvalid, err)
		}
		if err := validHead(h.Finalized); err != nil {
			return fmt.Errorf("%w: finalized: %w", ErrInvalid, err)
		}
		if h.Finalized.Number > h.Justified.Number || h.Justified.Number > h.SnapshotHead.Number {
			return fmt.Errorf("%w: finalized %d, justified %d, snapshot %d", ErrInvalid, h.Finalized.Number,
				h.Justified.Number, h.SnapshotHead.Number)
		}
		return nil
	}
	if h.Justified.Number != 0 || h.Justified.Hash != "" || h.Finalized.Number != 0 || h.Finalized.Hash != "" {
		return fmt.Errorf("%w: a fallback carries attested fields", ErrInvalid)
	}
	if !isFallbackReason(h.Reason) {
		return fmt.Errorf("%w: fallback reason %q", ErrInvalid, h.Reason)
	}
	if err := validHead(h.Genesis); err != nil {
		return fmt.Errorf("%w: genesis: %w", ErrInvalid, err)
	}
	if h.Genesis.Number != 0 {
		return fmt.Errorf("%w: genesis number %d", ErrInvalid, h.Genesis.Number)
	}
	return nil
}

// EffectiveBasis is the basis of the native effective finalized result.
type EffectiveBasis string

// Bases.
const (
	// BasisHeaderSnapshot means the effective finalized equals the attested
	// snapshot source.
	BasisHeaderSnapshot EffectiveBasis = "HEADER_SNAPSHOT"
	// BasisVotePoolQuorum means the native effective finalized was advanced
	// by a VotePool quorum that is not yet included in headers.
	BasisVotePoolQuorum EffectiveBasis = "VOTE_POOL_QUORUM"
	// BasisGenesisFallback means the effective finalized is the genesis
	// fallback of a snapshot without an attestation.
	BasisGenesisFallback EffectiveBasis = "GENESIS_FALLBACK"
	// BasisUnset means no effective basis applies (the reference is
	// unknown).
	BasisUnset EffectiveBasis = "UNSET"
)

func isBasis(b EffectiveBasis) bool {
	switch b {
	case BasisHeaderSnapshot, BasisVotePoolQuorum, BasisGenesisFallback, BasisUnset:
		return true
	}
	return false
}

// EarlyVoteEvidence is the early vote-only finality observation: what the
// native GetFinalizedHeader advance decided from the VotePool quorum, saved
// as an owned snapshot of the actually verified vote envelopes and the
// validator snapshot before publication. The digest binds that record; it is
// not an independently verifiable consensus certificate.
type EarlyVoteEvidence struct {
	Origin                  Origin        `json:"origin"`
	EvaluationHead          decision.Head `json:"evaluation_head"`
	SourceJustified         decision.Head `json:"source_justified"`
	VoteTarget              decision.Head `json:"vote_target"`
	FinalizedCandidate      decision.Head `json:"finalized_candidate"`
	ValidatorSnapshotParent decision.Head `json:"validator_snapshot_parent"`
	ValidatorCount          uint64        `json:"validator_count"`
	QuorumCount             uint64        `json:"quorum_count"`
	MatchingVoteCount       uint64        `json:"matching_vote_count"`
	// EvidenceDigest binds the owned vote snapshot; EarlyDigest computes it.
	EvidenceDigest string `json:"evidence_digest"`
}

// valid checks the shape rules of an early observation. Any origin is a
// valid shape here: a stored record is legitimately RETAINED_DURABLE after a
// restart; only ObserveEarly refuses a retained origin as a fresh
// observation of this process.
func (e EarlyVoteEvidence) valid() error {
	if !isOrigin(e.Origin) {
		return fmt.Errorf("%w: origin %q", ErrInvalid, e.Origin)
	}
	for _, h := range []decision.Head{e.EvaluationHead, e.SourceJustified, e.VoteTarget, e.FinalizedCandidate,
		e.ValidatorSnapshotParent} {
		if err := validHead(h); err != nil {
			return err
		}
	}
	if e.ValidatorCount == 0 {
		return fmt.Errorf("%w: an empty validator set", ErrInvalid)
	}
	if want := quorumOf(e.ValidatorCount); e.QuorumCount != want {
		return fmt.Errorf("%w: quorum %d of %d validators, want %d", ErrInvalid, e.QuorumCount, e.ValidatorCount,
			want)
	}
	if e.MatchingVoteCount < e.QuorumCount || e.MatchingVoteCount > e.ValidatorCount {
		return fmt.Errorf("%w: %d matching votes, quorum %d of %d", ErrInvalid, e.MatchingVoteCount, e.QuorumCount,
			e.ValidatorCount)
	}
	if e.FinalizedCandidate.Number > e.SourceJustified.Number || e.SourceJustified.Number > e.VoteTarget.Number {
		return fmt.Errorf("%w: candidate %d, source %d, target %d", ErrInvalid, e.FinalizedCandidate.Number,
			e.SourceJustified.Number, e.VoteTarget.Number)
	}
	if e.VoteTarget.Number > e.EvaluationHead.Number {
		return fmt.Errorf("%w: target %d above the evaluation head %d", ErrInvalid, e.VoteTarget.Number,
			e.EvaluationHead.Number)
	}
	return nil
}

// quorumOf returns the BSC vote quorum of n validators: ceil(2*n/3).
func quorumOf(n uint64) uint64 {
	q := (2*n + 2) / 3
	if q < 2 {
		q = 2
	}
	return q
}

// Evidence is the typed finality evidence of one observation: the observed
// heads, the header-derived snapshot finality, the native effective
// finalized result with its basis, and the early observation if one was
// made. The native effective result may be ahead of the header snapshot
// (VotePool quorum); the two surfaces are never collapsed.
type Evidence struct {
	// ObservedExecutionHead is the common observed execution head.
	ObservedExecutionHead decision.Head `json:"observed_execution_head"`
	// ObservedNativeHeaderHead is the observed native header head; it may
	// differ from the execution head and is reconciled first.
	ObservedNativeHeaderHead decision.Head  `json:"observed_native_header_head"`
	HeaderFinality           HeaderFinality `json:"header_finality"`
	NativeEffectiveFinalized Reference      `json:"native_effective_finalized"`
	EffectiveBasis           EffectiveBasis `json:"effective_basis"`
	// Early is nil when no early observation was made.
	Early *EarlyVoteEvidence `json:"early_observation"`
}

// valid checks the whole evidence against its shape rules, including the
// agreement between the effective basis, the header snapshot and the early
// observation.
func (e Evidence) valid() error {
	if err := validHead(e.ObservedExecutionHead); err != nil {
		return fmt.Errorf("%w: execution head: %w", ErrInvalid, err)
	}
	if err := validHead(e.ObservedNativeHeaderHead); err != nil {
		return fmt.Errorf("%w: header head: %w", ErrInvalid, err)
	}
	if err := e.HeaderFinality.valid(); err != nil {
		return err
	}
	if err := e.NativeEffectiveFinalized.valid(); err != nil {
		return err
	}
	if !isBasis(e.EffectiveBasis) {
		return fmt.Errorf("%w: effective basis %q", ErrInvalid, e.EffectiveBasis)
	}
	switch e.EffectiveBasis {
	case BasisUnset:
		if e.NativeEffectiveFinalized.Known {
			return fmt.Errorf("%w: a known effective reference with basis %s", ErrInvalid, BasisUnset)
		}
	case BasisHeaderSnapshot:
		if !e.HeaderFinality.Attested || !e.NativeEffectiveFinalized.Known ||
			e.NativeEffectiveFinalized.Head != e.HeaderFinality.Finalized {
			return fmt.Errorf("%w: basis %s without the attested snapshot source", ErrInvalid, BasisHeaderSnapshot)
		}
	case BasisGenesisFallback:
		if e.HeaderFinality.Attested || !e.NativeEffectiveFinalized.Known ||
			e.NativeEffectiveFinalized.Head != e.HeaderFinality.Genesis {
			return fmt.Errorf("%w: basis %s without the genesis fallback", ErrInvalid, BasisGenesisFallback)
		}
	case BasisVotePoolQuorum:
		if !e.NativeEffectiveFinalized.Known || e.Early == nil ||
			e.Early.FinalizedCandidate != e.NativeEffectiveFinalized.Head {
			return fmt.Errorf("%w: basis %s without its early evidence", ErrInvalid, BasisVotePoolQuorum)
		}
	}
	if e.Early != nil {
		if err := e.Early.valid(); err != nil {
			return err
		}
	}
	return nil
}

// HeaderStatus is the header-derived status of one activation. It is never
// merged with the early status into one unconditional finalized flag.
type HeaderStatus string

// Header statuses.
const (
	// FinalizedHeaderAttested means the attested snapshot source covers the
	// activation on the canonical execution ancestry.
	FinalizedHeaderAttested HeaderStatus = "FINALIZED_HEADER_ATTESTED"
	// Justified means the attested target covers the activation while the
	// source does not.
	Justified HeaderStatus = "JUSTIFIED"
	// NoAttestedFinality means the snapshot has no attestation and the
	// activation is above the genesis fallback.
	NoAttestedFinality HeaderStatus = "NO_ATTESTED_FINALITY"
	// CanonicalUnfinalized means the known snapshot does not cover the
	// activation.
	CanonicalUnfinalized HeaderStatus = "CANONICAL_UNFINALIZED"
)

// EarlyStatus is the early vote status of one activation.
type EarlyStatus string

// Early statuses.
const (
	// VoteFinalizedObserved means an early quorum covers the activation on
	// the canonical execution ancestry.
	VoteFinalizedObserved EarlyStatus = "VOTE_FINALIZED_OBSERVED"
	// NotCovered means an early observation exists but does not cover the
	// activation, or was made on a side branch.
	NotCovered EarlyStatus = "NOT_COVERED"
	// EarlyNotObserved means no early observation was made.
	EarlyNotObserved EarlyStatus = "NOT_OBSERVED"
)

// Ancestry resolves block ancestry. The BSC client block walks the canonical
// headers; a lookup error is returned, never a silent false.
type Ancestry interface {
	// IsAncestorOrSelf reports whether block is an ancestor of anchor or
	// equals it.
	IsAncestorOrSelf(block, anchor decision.Head) (bool, error)
}

// covered reports whether evidence block e covers the activation a: a is an
// ancestor of or equals e, and e itself is on the canonical execution
// ancestry of head, so a side-branch evidence block never covers a canonical
// activation.
func covered(a, e, head decision.Head, anc Ancestry) (bool, error) {
	canonical, err := anc.IsAncestorOrSelf(e, head)
	if err != nil {
		return false, fmt.Errorf("%w: ancestry of %s: %w", ErrSource, e.Hash, err)
	}
	if !canonical {
		return false, nil
	}
	onChain, err := anc.IsAncestorOrSelf(a, e)
	if err != nil {
		return false, fmt.Errorf("%w: ancestry of %s: %w", ErrSource, a.Hash, err)
	}
	return onChain, nil
}

// Project computes the separate header and early statuses of one activation
// from the evidence. The statuses are a semantic projection of typed
// evidence, not one linear finalized flag; an early quorum never turns into
// a header-attested status, and a side-branch observation covers nothing.
func Project(e Evidence, activation decision.Head, anc Ancestry) (HeaderStatus, EarlyStatus, error) {
	if anc == nil {
		return "", "", fmt.Errorf("%w: missing ancestry", ErrInvalid)
	}
	if err := e.valid(); err != nil {
		return "", "", err
	}
	if err := validHead(activation); err != nil {
		return "", "", fmt.Errorf("%w: activation: %w", ErrInvalid, err)
	}
	head := e.ObservedExecutionHead
	var headerStatus HeaderStatus
	if e.HeaderFinality.Attested {
		finalized, err := covered(activation, e.HeaderFinality.Finalized, head, anc)
		if err != nil {
			return "", "", err
		}
		justified := false
		if !finalized {
			justified, err = covered(activation, e.HeaderFinality.Justified, head, anc)
			if err != nil {
				return "", "", err
			}
		}
		switch {
		case finalized:
			headerStatus = FinalizedHeaderAttested
		case justified:
			headerStatus = Justified
		default:
			headerStatus = CanonicalUnfinalized
		}
	} else {
		genesis, err := covered(activation, e.HeaderFinality.Genesis, head, anc)
		if err != nil {
			return "", "", err
		}
		if genesis {
			headerStatus = FinalizedHeaderAttested
		} else {
			headerStatus = NoAttestedFinality
		}
	}
	earlyStatus := EarlyNotObserved
	if e.Early != nil {
		vote, err := covered(activation, e.Early.FinalizedCandidate, head, anc)
		if err != nil {
			return "", "", err
		}
		if vote {
			earlyStatus = VoteFinalizedObserved
		} else {
			earlyStatus = NotCovered
		}
	}
	return headerStatus, earlyStatus, nil
}

// ---------------------------------------------------------------- digests

func u64(value uint64) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, value)
	return out
}

func str(value string) []byte { return append(u64(uint64(len(value))), value...) }

func rawHash(value string) ([]byte, error) {
	if !digestPattern.MatchString(value) {
		return nil, fmt.Errorf("%w: hash %q", ErrInvalid, value)
	}
	out, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("%w: hash %q: %v", ErrInvalid, value, err)
	}
	return out, nil
}

func headBytes(h decision.Head) ([]byte, error) {
	hash, err := rawHash(h.Hash)
	if err != nil {
		return nil, err
	}
	return append(u64(h.Number), hash...), nil
}

func nonZeroHead(h decision.Head, what string) ([]byte, error) {
	if h.Hash == lifecycle.ZeroHash {
		return nil, fmt.Errorf("%w: zero %s hash", ErrInvalid, what)
	}
	return headBytes(h)
}

func digestOf(tag string, parts ...[]byte) string {
	h := sha256.New()
	h.Write(append([]byte(tag), 0))
	for _, part := range parts {
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// validHead checks one known block reference: a 64-digit lowercase hash that
// is not the zero hash.
func validHead(h decision.Head) error {
	if h.Hash == lifecycle.ZeroHash {
		return fmt.Errorf("%w: zero block hash", ErrInvalid)
	}
	_, err := rawHash(h.Hash)
	return err
}

// HeaderDigest returns the evidence digest of one header snapshot: an
// attested snapshot binds its head, justified and finalized blocks; a
// genesis fallback binds its head, reason and the real genesis hash.
func HeaderDigest(h HeaderFinality) (string, error) {
	if err := h.valid(); err != nil {
		return "", err
	}
	head, _ := headBytes(h.SnapshotHead)
	if h.Attested {
		justified, _ := headBytes(h.Justified)
		finalized, _ := headBytes(h.Finalized)
		return digestOf(tagHeader, str("ATTESTED"), head, justified, finalized), nil
	}
	genesis, _ := headBytes(h.Genesis)
	return digestOf(tagHeader, str("GENESIS_FALLBACK"), head, str(string(h.Reason)), genesis, genesis), nil
}

// EarlyDigest returns the evidence digest of one early observation and the
// owned snapshot of the verified vote envelopes and validator context it was
// computed from. The digest binds the record; it is not a consensus
// certificate.
func EarlyDigest(e EarlyVoteEvidence, votes []byte) (string, error) {
	if err := e.valid(); err != nil {
		return "", err
	}
	voteDigest := sha256.Sum256(votes)
	evaluation, _ := headBytes(e.EvaluationHead)
	source, _ := headBytes(e.SourceJustified)
	target, _ := headBytes(e.VoteTarget)
	candidate, _ := headBytes(e.FinalizedCandidate)
	validators, _ := headBytes(e.ValidatorSnapshotParent)
	return digestOf(tagEarly, str(string(e.Origin)), evaluation, source, target, candidate, validators,
		u64(e.ValidatorCount), u64(e.QuorumCount), u64(e.MatchingVoteCount), voteDigest[:]), nil
}
