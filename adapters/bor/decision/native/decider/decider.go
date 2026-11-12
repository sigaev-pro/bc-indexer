// Package decider drives the native decisions of the Bor import path
// through the shared decision package (E01-02.decision_bridge, Bor block):
// the transition intent of each executed block, the native rejection before
// any write of the block, NATIVE_DECIDED after the native marker, and the
// recovery driver that settles every saved intent and completes the native
// import of every EXEC_READY observation without a decision in RECOVERY_ONLY
// before GoLive.
//
// The package imports only the Go standard library and the shared admission
// and decision packages at their native paths, so its mutant table runs in
// the module from `python3 test/harness/bor_decision.py assemble DIR`.
// extractor/bordecision binds it to native Bor.
package decider

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
)

// Domain tags of the digests of this package. Each is followed by one zero byte.
const (
	tagEvidence  = "extractor-bor-decision-evidence-v1"
	tagRejection = "extractor-bor-decision-rejection-v1"
)

var (
	// ErrNoObservation reports an executed block without an open EXEC_READY
	// observation.
	ErrNoObservation = errors.New("no EXEC_READY observation of the block")
	// ErrUnsettled reports a recovery step that left a decision open.
	ErrUnsettled = errors.New("native recovery did not settle the decision")
)

func field(value string) []byte {
	out := make([]byte, 8, 8+len(value))
	length := uint64(len(value))
	for i := 7; i >= 0; i-- {
		out[i] = byte(length)
		length >>= 8
	}
	return append(out, value...)
}

func digest(tag string, values ...string) string {
	h := sha256.New()
	h.Write(append([]byte(tag), 0))
	for _, value := range values {
		h.Write(field(value))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Transition is the native transition that the import of one executed block
// proposes, predicted by the native fork choice before the write.
type Transition struct {
	Kind decision.Kind
	// Block is the executed block; ChildRoot its state root (64 lowercase
	// hex digits), durable after the child-root barrier.
	Block     decision.Head
	ChildRoot string
	OldHead   decision.Head
	NewHead   decision.Head
	// LocalTD and ExternTD are the total difficulties of the old head and of
	// the block (decimal), the inputs of the native fork choice.
	LocalTD  string
	ExternTD string
}

// EvidenceDigest is the digest of the native evidence of a transition: the
// kind, the block, its child root, the old and new heads and the fork choice
// inputs.
func EvidenceDigest(t Transition) string {
	return digest(tagEvidence, string(t.Kind), strconv.FormatUint(t.Block.Number, 10), t.Block.Hash, t.ChildRoot,
		strconv.FormatUint(t.OldHead.Number, 10), t.OldHead.Hash, strconv.FormatUint(t.NewHead.Number, 10),
		t.NewHead.Hash, t.LocalTD, t.ExternTD)
}

// RejectionDigest is the digest of the native rejection evidence of an
// intent: the intent digest and the native error text.
func RejectionDigest(intentDigest, nativeError string) string {
	return digest(tagRejection, intentDigest, nativeError)
}

// Native is the native side of the decisions.
type Native interface {
	// Stored reports whether the block (header and body) is stored natively.
	Stored(block decision.Head) (bool, error)
	// Complete replays the EXEC_READY observation obs from its saved inputs
	// and completes the native import of its block through the decision
	// path of the import, which calls Intend (recovering) and then Resolve
	// or Reject.
	Complete(t *admission.Token, obs admission.Observation) error
}

// Outcome is how a decision of this decider ended.
type Outcome string

// Outcomes.
const (
	Decided  Outcome = "NATIVE_DECIDED"
	Rejected Outcome = "NATIVE_REJECTED"
)

// Decider records the native decisions of one admission. Every method runs
// under the observation guard; the caller passes its token.
type Decider struct {
	Admission *admission.Admission
	Bridge    *decision.Bridge
	Native    Native
	outcomes  map[uint64]Outcome
}

func (d *Decider) record(seq uint64, outcome Outcome) {
	if d.outcomes == nil {
		d.outcomes = map[uint64]Outcome{}
	}
	d.outcomes[seq] = outcome
}

// authoritative returns the open EXEC_READY observation of block with the
// lowest sequence.
func (d *Decider) authoritative(t *admission.Token, block decision.Head) (admission.Observation, error) {
	observations, err := d.Admission.Observations(t)
	if err != nil {
		return admission.Observation{}, err
	}
	var found *admission.Observation
	for i := range observations {
		obs := &observations[i]
		if obs.State != admission.ExecReady || obs.Candidate.Hash != block.Hash || obs.Candidate.Number != block.Number {
			continue
		}
		if found == nil || obs.Sequence < found.Sequence {
			found = obs
		}
	}
	if found == nil {
		return admission.Observation{}, fmt.Errorf("%w: block %d %s", ErrNoObservation, block.Number, block.Hash)
	}
	return *found, nil
}

// intent builds the transition intent of t over the authoritative
// observation of its block. Only IMPORT and CANONICAL have public events.
func (d *Decider) intent(t *admission.Token, tr Transition) (decision.Intent, error) {
	obs, err := d.authoritative(t, tr.Block)
	if err != nil {
		return decision.Intent{}, err
	}
	return decision.Intent{Kind: tr.Kind, OldHead: tr.OldHead, NewHead: tr.NewHead,
		Members: []decision.Member{{Observation: obs.Key, Block: obs.Candidate.Hash,
			ExecReadyDigest: obs.ExecReady.Digest, ChildRoot: tr.ChildRoot}},
		EvidenceDigest: EvidenceDigest(tr), Events: tr.Kind != decision.Side}, nil
}

func (d *Decider) unresolved(t *admission.Token) (*decision.Decision, error) {
	decisions, err := d.Bridge.Decisions(t)
	if err != nil {
		return nil, err
	}
	for i := range decisions {
		if decisions[i].State == decision.Intended {
			return &decisions[i], nil
		}
	}
	return nil, nil
}

// matches reports whether the saved intent record is exactly the intent in.
func matches(saved decision.IntentRecord, in decision.Intent) bool {
	if saved.Kind != in.Kind || saved.OldHead != in.OldHead || saved.NewHead != in.NewHead ||
		saved.EvidenceDigest != in.EvidenceDigest || saved.Events != in.Events || len(saved.Members) != len(in.Members) {
		return false
	}
	for i, m := range in.Members {
		if saved.Members[i] != m {
			return false
		}
	}
	return true
}

// Intend records the transition intent of an executed block after its
// child-root barrier. While recovering, a saved unresolved intent is
// returned instead if it is exactly this transition: the recovery completes
// the saved transition and no other. A saved intent of another transition
// is a source invariant error.
func (d *Decider) Intend(t *admission.Token, tr Transition, recovering bool) (decision.IntentRecord, error) {
	in, err := d.intent(t, tr)
	if err != nil {
		return decision.IntentRecord{}, err
	}
	if recovering {
		saved, err := d.unresolved(t)
		if err != nil {
			return decision.IntentRecord{}, err
		}
		if saved != nil {
			if !matches(saved.Intent, in) {
				return decision.IntentRecord{}, fmt.Errorf("%w: intent %d is not the native transition of block %d",
					decision.ErrInvariant, saved.Sequence, tr.Block.Number)
			}
			return saved.Intent, nil
		}
	}
	return d.Bridge.Intend(t, in)
}

// Resolve records NATIVE_DECIDED after the native batch with the marker is
// synced, then removes the markers that no longer need to stay.
func (d *Decider) Resolve(t *admission.Token, seq uint64) error {
	if _, err := d.Bridge.Resolve(t, seq); err != nil {
		return err
	}
	d.record(seq, Decided)
	return d.Bridge.Cleanup(t)
}

// Reject records the native rejection of intent rec of block. A rejected
// intent means the native side kept none of its blocks: a block that is
// stored natively is a source invariant error instead.
func (d *Decider) Reject(t *admission.Token, rec decision.IntentRecord, block decision.Head, nativeErr error) error {
	if nativeErr == nil {
		return fmt.Errorf("%w: rejection of intent %d without a native error", decision.ErrInvalid, rec.Sequence)
	}
	stored, err := d.Native.Stored(block)
	if err != nil {
		return err
	}
	if stored {
		return fmt.Errorf("%w: intent %d was refused but block %d is stored", decision.ErrInvariant, rec.Sequence,
			block.Number)
	}
	if _, err := d.Bridge.RejectIntent(t, rec.Sequence, RejectionDigest(rec.Digest, nativeErr.Error())); err != nil {
		return err
	}
	d.record(rec.Sequence, Rejected)
	return nil
}

// Report lists what the recovery driver did.
type Report struct {
	// Resolved lists the intents whose marker was present.
	Resolved []uint64 `json:"resolved"`
	// Completed lists the intents whose saved transition native recovery
	// completed; Rejected those that native recovery refused.
	Completed []uint64 `json:"completed"`
	Rejected  []uint64 `json:"rejected"`
	// Imported lists the EXEC_READY observations without a decision whose
	// native import recovery completed, with the decision sequence.
	Imported []Imported `json:"imported"`
}

// Imported is one EXEC_READY observation whose native import recovery
// completed.
type Imported struct {
	Observation string  `json:"observation"`
	Sequence    uint64  `json:"sequence"`
	Outcome     Outcome `json:"outcome"`
}

// complete runs the native completion of obs and returns the one decision
// it settled.
func (d *Decider) complete(t *admission.Token, obs admission.Observation) (uint64, Outcome, error) {
	d.outcomes = nil
	if err := d.Native.Complete(t, obs); err != nil {
		return 0, "", err
	}
	if len(d.outcomes) != 1 {
		return 0, "", fmt.Errorf("%w: %d decisions settled for %s", ErrUnsettled, len(d.outcomes), obs.Key)
	}
	for seq, outcome := range d.outcomes {
		return seq, outcome, nil
	}
	return 0, "", nil
}

// Recover runs in RECOVERY_ONLY after the admission recovery and
// decision.Open. It settles the saved unresolved intent: with its marker it
// resolves it; without, native recovery completes exactly the saved
// transition (which writes the marker) or records its native rejection, and
// anything else stops with an error. Then it completes the native import of
// every EXEC_READY observation without a decision, from its saved inputs, in
// sequence order; a block that is already stored without a decision is a
// source invariant error. Finally it switches to LIVE (decision GoLive).
func (d *Decider) Recover(t *admission.Token) (Report, error) {
	report := Report{Resolved: []uint64{}, Completed: []uint64{}, Rejected: []uint64{}, Imported: []Imported{}}
	saved, err := d.unresolved(t)
	if err != nil {
		return report, err
	}
	if saved != nil {
		seq := saved.Sequence
		if saved.Marker {
			if err := d.Resolve(t, seq); err != nil {
				return report, err
			}
			report.Resolved = append(report.Resolved, seq)
		} else {
			if len(saved.Intent.Members) != 1 {
				return report, fmt.Errorf("%w: intent %d has %d members", decision.ErrInvariant, seq,
					len(saved.Intent.Members))
			}
			obs, err := d.observation(t, saved.Intent.Members[0].Observation)
			if err != nil {
				return report, err
			}
			settled, outcome, err := d.complete(t, obs)
			if err != nil {
				return report, err
			}
			if settled != seq {
				return report, fmt.Errorf("%w: intent %d, settled %d", ErrUnsettled, seq, settled)
			}
			if outcome == Decided {
				report.Completed = append(report.Completed, seq)
			} else {
				report.Rejected = append(report.Rejected, seq)
			}
		}
	}
	seen := map[string]bool{}
	for {
		undecided, err := d.Bridge.Undecided(t)
		if err != nil {
			return report, err
		}
		if len(undecided) == 0 {
			break
		}
		// Undecided keeps the admission's sequence order.
		obs := undecided[0]
		if seen[obs.Key] {
			return report, fmt.Errorf("%w: %s is still undecided after its completion", ErrUnsettled, obs.Key)
		}
		seen[obs.Key] = true
		stored, err := d.Native.Stored(decision.Head{Number: obs.Candidate.Number, Hash: obs.Candidate.Hash})
		if err != nil {
			return report, err
		}
		if stored {
			return report, fmt.Errorf("%w: block %d of %s is stored without a decision", decision.ErrInvariant,
				obs.Candidate.Number, obs.Key)
		}
		seq, outcome, err := d.complete(t, obs)
		if err != nil {
			return report, err
		}
		report.Imported = append(report.Imported, Imported{Observation: obs.Key, Sequence: seq, Outcome: outcome})
	}
	return report, d.Bridge.GoLive(t)
}

func (d *Decider) observation(t *admission.Token, key string) (admission.Observation, error) {
	observations, err := d.Admission.Observations(t)
	if err != nil {
		return admission.Observation{}, err
	}
	for _, obs := range observations {
		if obs.Key == key {
			if obs.State != admission.ExecReady {
				return admission.Observation{}, fmt.Errorf("%w: member %s is %s", decision.ErrInvariant, key, obs.State)
			}
			return obs, nil
		}
	}
	return admission.Observation{}, fmt.Errorf("%w: member %s is not open", decision.ErrInvariant, key)
}
