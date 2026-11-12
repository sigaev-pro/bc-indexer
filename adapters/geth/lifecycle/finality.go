package lifecycle

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/extractor/decision"
)

// Domain tags of the finality digests, each followed by one zero byte.
const (
	tagFinality    = "extractor-lifecycle-finality-v1"
	tagAttestation = "extractor-lifecycle-attestation-v1"
)

// ZeroHash is the zero block hash. As a new finality reference it means "no
// change" (Engine API forkchoice); as an old one it means "none yet".
var ZeroHash = strings.Repeat("0", 64)

var (
	// ErrNoop reports a finality change that changes nothing: a zero new
	// hash or the same reference. No setter runs and no intent is made.
	ErrNoop = errors.New("finality change is a no-op")
	// ErrSource reports a finality evidence read that failed. It is never
	// replaced by the genesis fallback or by empty evidence.
	ErrSource = errors.New("finality evidence source error")
)

// Field is a native finality reference.
type Field string

// Fields.
const (
	Safe      Field = "SAFE"
	Finalized Field = "FINALIZED"
)

// FinalityChange is one native finality setter call: the reference it sets,
// the mapped caller that runs it, and the old and new blocks.
type FinalityChange struct {
	Field  Field
	Caller string
	Old    decision.Head
	New    decision.Head
}

// FinalityDigest returns the evidence digest of one finality setter call.
// A zero new hash or an unchanged reference is ErrNoop: the old native
// reference stays and nothing is declared final.
func FinalityDigest(c FinalityChange) (string, error) {
	if c.Field != Safe && c.Field != Finalized {
		return "", fmt.Errorf("%w: finality field %q", ErrInvalid, c.Field)
	}
	if !idPattern.MatchString(c.Caller) {
		return "", fmt.Errorf("%w: caller %q", ErrInvalid, c.Caller)
	}
	old, err := headBytes(c.Old)
	if err != nil {
		return "", err
	}
	if _, err := rawHash(c.New.Hash); err != nil {
		return "", err
	}
	if c.New.Hash == ZeroHash {
		return "", fmt.Errorf("%w: zero %s hash", ErrNoop, c.Field)
	}
	if c.New == c.Old {
		return "", fmt.Errorf("%w: %s unchanged", ErrNoop, c.Field)
	}
	next, _ := headBytes(c.New)
	return digestOf(tagFinality, str(string(c.Field)), str(c.Caller), old, next), nil
}

// Finality records the FINALITY intent of one native setter call, before
// the setter mutates native state. Each setter call is its own decision, so
// a setter that ran stays decided even if a later step of the same native
// request fails, and a setter that did not run makes no decision. The head
// is the unchanged native head.
func (p *Pass) Finality(bridge Intender, head decision.Head, c FinalityChange, events bool) (decision.IntentRecord,
	error) {
	if c.Caller != p.caller.ID {
		return decision.IntentRecord{}, fmt.Errorf("%w: change of %q in the pass of %s", ErrInvalid, c.Caller,
			p.caller.ID)
	}
	digest, err := FinalityDigest(c)
	if err != nil {
		return decision.IntentRecord{}, err
	}
	return p.Intend(bridge, decision.Intent{Kind: decision.Finality, OldHead: head, NewHead: head,
		EvidenceDigest: digest, Events: events})
}

// Attestation is the vote attestation of a BSC Parlia snapshot: the source
// (finalized) and target (justified) blocks.
type Attestation struct {
	SourceNumber uint64
	SourceHash   string
	TargetNumber uint64
	TargetHash   string
}

// AttestationStatus is the provenance of attestation evidence.
type AttestationStatus string

// Attestation statuses.
const (
	// Attested evidence comes from the snapshot attestation.
	Attested AttestationStatus = "ATTESTED"
	// GenesisFallback is a correct snapshot without an attestation: the
	// justified and finalized blocks are the real genesis block. It is not
	// a source error and not an attestation.
	GenesisFallback AttestationStatus = "GENESIS_FALLBACK"
)

// AttestationEvidence is the result of an error-aware attestation read.
type AttestationEvidence struct {
	Status    AttestationStatus
	Justified decision.Head
	Finalized decision.Head
	Digest    string
}

func nonZero(h decision.Head) ([]byte, error) {
	b, err := headBytes(h)
	if err != nil {
		return nil, err
	}
	if h.Hash == ZeroHash {
		return nil, fmt.Errorf("%w: zero hash", ErrInvalid)
	}
	return b, nil
}

// ReadAttestation reads the attestation evidence of a block without losing
// errors. snapshot returns the attestation of the block's snapshot, nil for
// a correct snapshot without one, or the lookup error; genesis returns the
// genesis block hash or its lookup error. A lookup error, or a malformed or
// zero hash, is ErrSource; it is never turned into the genesis fallback or
// empty evidence.
func ReadAttestation(snapshot func() (*Attestation, error), genesis func() (string, error)) (AttestationEvidence,
	error) {
	if snapshot == nil || genesis == nil {
		return AttestationEvidence{}, fmt.Errorf("%w: missing reader", ErrInvalid)
	}
	att, err := snapshot()
	if err != nil {
		return AttestationEvidence{}, fmt.Errorf("%w: snapshot: %w", ErrSource, err)
	}
	var e AttestationEvidence
	if att == nil {
		hash, err := genesis()
		if err != nil {
			return AttestationEvidence{}, fmt.Errorf("%w: genesis: %w", ErrSource, err)
		}
		g := decision.Head{Number: 0, Hash: hash}
		e = AttestationEvidence{Status: GenesisFallback, Justified: g, Finalized: g}
	} else {
		e = AttestationEvidence{Status: Attested,
			Justified: decision.Head{Number: att.TargetNumber, Hash: att.TargetHash},
			Finalized: decision.Head{Number: att.SourceNumber, Hash: att.SourceHash}}
		if att.SourceNumber > att.TargetNumber {
			return AttestationEvidence{}, fmt.Errorf("%w: source %d above target %d", ErrSource, att.SourceNumber,
				att.TargetNumber)
		}
	}
	justified, err := nonZero(e.Justified)
	if err != nil {
		return AttestationEvidence{}, fmt.Errorf("%w: justified: %w", ErrSource, err)
	}
	finalized, err := nonZero(e.Finalized)
	if err != nil {
		return AttestationEvidence{}, fmt.Errorf("%w: finalized: %w", ErrSource, err)
	}
	e.Digest = digestOf(tagAttestation, str(string(e.Status)), justified, finalized)
	return e, nil
}
