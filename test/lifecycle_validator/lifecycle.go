// Package lifecycle is the test lifecycle automaton of E02-03
// .lifecycle_validator: it folds reordered Applied, Removed and StatusChanged
// semantic descriptors of one activation into one deterministic record, so
// that reordering, revision conflicts, duplicates and reinclusion each have
// exactly one unambiguous result. The semantic half (lifecycle.go) is the
// state machine over protocol/semantic SemanticDescriptor values; the schema
// half (schema.go) wires the merged protocol/proto layer in front of it:
// lifecycle events arrive as schema messages (a proto Envelope, its READY
// event descriptor and the canonical transport frames of its record
// fragments), are validated there and are folded with the proto
// record_digest as the content discriminator. Neither half is a production
// consumer. The design and the normative anchors are in README.md. The
// package uses only the Go standard library plus protocol/semantic and
// protocol/proto.
package lifecycle

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// ErrUnsetDigest rejects an applied event whose content digest is the unset
// zero digest: every layer in front of the automaton pins a real digest
// (the schema half the proto record_digest, a semantic-only caller the
// descriptor digest), and the zero value is never one.
var ErrUnsetDigest = errors.New("lifecycle: content digest is unset")

// State is the lifecycle state of one activation record, after the state
// names of MESSAGE_CONTRACT section 5. States only advance: an activation
// goes PENDING_APPLIED -> ACTIVE -> REMOVED and never backwards.
type State uint8

// Lifecycle states of one activation record.
const (
	// StatePendingApplied marks evidence or a tombstone whose Applied record
	// has not been observed yet. Nothing about the activation is invented.
	StatePendingApplied State = iota + 1
	// StateActive marks an activation whose Applied revision-0 record is
	// observed. Late evidence may still arrive by revision.
	StateActive
	// StateRemoved is terminal: the tombstone keeps its cancel meaning even
	// for a late Applied, and no event reactivates the activation.
	StateRemoved
)

// String returns the contract name of s.
func (s State) String() string {
	switch s {
	case StatePendingApplied:
		return "PENDING_APPLIED"
	case StateActive:
		return "ACTIVE"
	case StateRemoved:
		return "REMOVED"
	}
	return fmt.Sprintf("State(%d)", uint8(s))
}

// Class is the outcome vocabulary of the automaton. Every applied event gets
// exactly one class; the definitions are in README.md.
type Class uint8

// Outcome classes of one applied event.
const (
	// ClassAccepted means the event advanced the record: a lifecycle state
	// transition and/or new evidence at the revision frontier.
	ClassAccepted Class = iota + 1
	// ClassDuplicate means an idempotent retry: the same descriptor digest is
	// already recorded. The record is unchanged.
	ClassDuplicate
	// ClassConflict means a content clash: the same revision slot or the
	// activation identity is already recorded with a different digest. The
	// record is unchanged; nothing is overwritten silently.
	ClassConflict
	// ClassStale means the event cannot advance the record: its revision is
	// older than the recorded evidence frontier, or the record is already
	// terminal. A stale event with a free revision slot is merged as evidence
	// only; it never replaces newer evidence and never reactivates.
	ClassStale
	// ClassUnknown means the event references an activation with no record:
	// the reference is recorded (pending evidence or a tombstone), and the
	// activation itself is not invented.
	ClassUnknown
)

// String returns the outcome name of c.
func (c Class) String() string {
	switch c {
	case ClassAccepted:
		return "Accepted"
	case ClassDuplicate:
		return "Duplicate"
	case ClassConflict:
		return "Conflict"
	case ClassStale:
		return "Stale"
	case ClassUnknown:
		return "Unknown"
	}
	return fmt.Sprintf("Class(%d)", uint8(c))
}

// ActivationKey identifies one activation across its events: the network,
// the producer and run that observed it, and the decision entity. The block
// hash, the family and the inclusion binding are content of the events, not
// parts of the key: a record accepts only one entity signature, and a clash
// is a Conflict. A re-included entity arrives with a new decision sequence
// (CONTEXT.md, "Inclusion" and "Canonical activation"), so its activations
// are separate records that coexist.
type ActivationKey struct {
	Network    semantic.Network
	ProducerID semantic.ID16
	RunID      semantic.ID16
	Activation semantic.ActivationID
}

// KeyOf returns the activation key of a descriptor.
func KeyOf(d semantic.SemanticDescriptor) ActivationKey {
	return ActivationKey{Network: d.Network, ProducerID: d.Activation.ProducerID,
		RunID: d.Activation.RunID, Activation: d.Activation.ActivationID}
}

// EntitySignature is the activation content that the first accepted event of
// a record fixes: the event family, the block hash of the activation and the
// inclusion binding. Every later event of the same activation key must carry
// the same signature; a different one is an identity clash (Conflict).
type EntitySignature struct {
	Family      semantic.Family
	BlockHash   semantic.Hash32
	HasTx       bool
	Transaction semantic.TransactionRef
}

// EntitySignatureOf returns the entity signature of a descriptor.
func EntitySignatureOf(d semantic.SemanticDescriptor) EntitySignature {
	sig := EntitySignature{Family: d.Family, BlockHash: d.Activation.BlockHash}
	if d.Activation.Transaction != nil {
		sig.HasTx = true
		sig.Transaction = *d.Activation.Transaction
	}
	return sig
}

// Digest is the content discriminator of one applied event: the digest the
// layer in front of the automaton pins for the event bytes. The schema half
// supplies the proto record_digest computed over the record's fragment bytes
// (README, open question 4, resolved); a semantic-only caller supplies the
// descriptor digest. The same digest at one revision is an idempotent retry
// (Duplicate); a different digest at an occupied revision is a Conflict.
type Digest [32]byte

// IsZero reports whether d is the unset zero digest.
func (d Digest) IsZero() bool { return d == Digest{} }

// String returns the 64 lowercase hex characters of d.
func (d Digest) String() string { return hex.EncodeToString(d[:]) }

// Record is the accumulated state of one activation. It is a value type:
// every transition stores a fresh copy, and the maps and pointers of a
// returned record are never shared with the automaton. Evidence maps each
// lifecycle revision to the content digest of the one event recorded at it;
// slot 0 holds the Applied event, which is also kept in Origin. MaxRevision
// is the highest recorded evidence revision.
type Record struct {
	State  State
	Entity EntitySignature
	Origin *semantic.SemanticDescriptor
	// RemovedRevision is the lifecycle revision of the event that
	// terminalized the record; it is meaningful only in StateRemoved and
	// never changes afterwards, not even when later evidence merges into
	// free slots above it. The schema half uses it to separate a
	// StatusChanged written before the removal (revision below it, a
	// legitimate late delivery) from the writer-invalid one written after
	// it (revision above it: one monotonic counter per activation).
	RemovedRevision uint64
	Evidence        map[uint64]Digest
	MaxRevision     uint64
}

// cloneDescriptor returns a deep copy of d.
func cloneDescriptor(d semantic.SemanticDescriptor) semantic.SemanticDescriptor {
	out := d
	if d.Activation.Transaction != nil {
		tx := *d.Activation.Transaction
		out.Activation.Transaction = &tx
	}
	return out
}

// Clone returns a deep copy of the record.
func (r Record) Clone() Record {
	out := r
	out.Evidence = make(map[uint64]Digest, len(r.Evidence)+1)
	for rev, digest := range r.Evidence {
		out.Evidence[rev] = digest
	}
	if r.Origin != nil {
		origin := cloneDescriptor(*r.Origin)
		out.Origin = &origin
	}
	return out
}

// Equal reports whether two records carry the same lifecycle content: the
// same state, entity signature, terminal revision, applied origin (by
// digest), evidence slots and revision frontier.
func (r Record) Equal(other Record) bool {
	if r.State != other.State || r.Entity != other.Entity || r.MaxRevision != other.MaxRevision ||
		r.RemovedRevision != other.RemovedRevision {
		return false
	}
	if (r.Origin == nil) != (other.Origin == nil) {
		return false
	}
	if r.Origin != nil {
		left, err := r.Origin.Digest()
		if err != nil {
			return false
		}
		right, err := other.Origin.Digest()
		if err != nil {
			return false
		}
		if left != right {
			return false
		}
	}
	if len(r.Evidence) != len(other.Evidence) {
		return false
	}
	for rev, digest := range r.Evidence {
		otherDigest, ok := other.Evidence[rev]
		if !ok || otherDigest != digest {
			return false
		}
	}
	return true
}

// String returns a deterministic one-line dump of the record with the
// evidence revisions in ascending order; a terminal record also names the
// revision that terminalized it.
func (r Record) String() string {
	revisions := make([]uint64, 0, len(r.Evidence))
	for rev := range r.Evidence {
		revisions = append(revisions, rev)
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i] < revisions[j] })
	slots := make([]string, 0, len(revisions))
	for _, rev := range revisions {
		slots = append(slots, fmt.Sprintf("%d:%s", rev, r.Evidence[rev]))
	}
	origin := "-"
	if r.Origin != nil {
		digest, err := r.Origin.Digest()
		if err != nil {
			origin = "invalid"
		} else {
			origin = digest.String()
		}
	}
	state := r.State.String()
	if r.State == StateRemoved {
		state = fmt.Sprintf("%s removed=%d", state, r.RemovedRevision)
	}
	return fmt.Sprintf("%s origin=%s max=%d evidence={%s}", state, origin,
		r.MaxRevision, strings.Join(slots, ","))
}

// Result is the outcome of one applied event.
type Result struct {
	// Class is the outcome of the event.
	Class Class
	// Key is the activation the event belongs to.
	Key ActivationKey
	// State is the record state after the event.
	State State
	// Revision is the lifecycle revision of the event.
	Revision uint64
	// Reason explains the outcome in one plain sentence.
	Reason string
}

// String returns a deterministic one-line dump of the result.
func (r Result) String() string {
	return fmt.Sprintf("%s rev=%d -> %s: %s", r.Class, r.Revision, r.State, r.Reason)
}

// Automaton folds semantic descriptors into activation records. It is
// deterministic and single-threaded: Apply is a pure transition over value
// records, with no goroutines and no wall-clock or map-order inputs. For any
// clash-free event multiset of one activation, every delivery order produces
// the same final record, because the record is a function of the recorded
// set: terminal if any Removed is recorded, otherwise active once the
// Applied origin is recorded, otherwise pending.
type Automaton struct {
	records map[ActivationKey]Record
}

// New returns an empty automaton.
func New() *Automaton {
	return &Automaton{records: make(map[ActivationKey]Record)}
}

// Len returns the number of activation records.
func (a *Automaton) Len() int { return len(a.records) }

// Record returns a copy of the record of one activation key.
func (a *Automaton) Record(key ActivationKey) (Record, bool) {
	rec, ok := a.records[key]
	if !ok {
		return Record{}, false
	}
	return rec.Clone(), true
}

// Records returns deep copies of all activation records.
func (a *Automaton) Records() map[ActivationKey]Record {
	out := make(map[ActivationKey]Record, len(a.records))
	for key, rec := range a.records {
		out[key] = rec.Clone()
	}
	return out
}

// Apply folds one event into the automaton and returns its outcome. The
// content digest is supplied by the layer in front of the automaton: the
// schema half passes the proto record_digest of the validated envelope
// (open question 4, resolved — the discriminator is the record-level
// digest over the event's fragment bytes, not the descriptor digest), and a
// semantic-only caller passes the descriptor digest. A descriptor that
// fails semantic validation, or an unset digest, is a caller error, not an
// outcome: Apply returns the error and changes nothing.
func (a *Automaton) Apply(d semantic.SemanticDescriptor, content Digest) (Result, error) {
	if err := d.Validate(); err != nil {
		return Result{}, err
	}
	if content.IsZero() {
		return Result{}, ErrUnsetDigest
	}
	key := KeyOf(d)
	sig := EntitySignatureOf(d)

	rec, ok := a.records[key]
	if !ok {
		return a.startRecord(key, sig, d, content)
	}
	return a.stepRecord(key, rec, sig, d, content)
}

// startRecord records the first event of an unseen activation. An Applied
// starts ACTIVE with its original revision-0 snapshot; a StatusChanged or a
// Removed only references an activation that has no record yet, so the
// reference is recorded and the activation itself is not invented.
func (a *Automaton) startRecord(key ActivationKey, sig EntitySignature,
	d semantic.SemanticDescriptor, content Digest) (Result, error) {
	rec := Record{Entity: sig, Evidence: make(map[uint64]Digest, 1)}
	var class Class
	var reason string
	switch d.Kind {
	case semantic.KindApplied:
		rec.State = StateActive
		origin := cloneDescriptor(d)
		rec.Origin = &origin
		rec.Evidence[0] = content
		class = ClassAccepted
		reason = "applied creates an active activation with its original revision-0 snapshot"
	case semantic.KindStatus:
		rec.State = StatePendingApplied
		rec.Evidence[d.Revision] = content
		rec.MaxRevision = d.Revision
		class = ClassUnknown
		reason = "status references an unseen activation: evidence recorded by revision, no applied record invented"
	default:
		rec.State = StateRemoved
		rec.RemovedRevision = d.Revision
		rec.Evidence[d.Revision] = content
		rec.MaxRevision = d.Revision
		class = ClassUnknown
		reason = "removed references an unseen activation: tombstone recorded, no applied record invented"
	}
	a.records[key] = rec
	return Result{Class: class, Key: key, State: rec.State, Revision: d.Revision, Reason: reason}, nil
}

// stepRecord folds an event into an existing record. The checks run in a
// fixed order: idempotent retry, identity clash, revision clash, then the
// kind and state rules. Rejected events (Duplicate, Conflict) leave the
// stored record unchanged.
func (a *Automaton) stepRecord(key ActivationKey, rec Record, sig EntitySignature,
	d semantic.SemanticDescriptor, content Digest) (Result, error) {
	if rec.Evidence[d.Revision] == content {
		return Result{Class: ClassDuplicate, Key: key, State: rec.State, Revision: d.Revision,
			Reason: fmt.Sprintf("idempotent retry of the event already recorded at revision %d", d.Revision)}, nil
	}
	if rec.Entity != sig {
		return Result{Class: ClassConflict, Key: key, State: rec.State, Revision: d.Revision,
			Reason: "activation identity clash: family, block hash or inclusion binding differs from the recorded entity"}, nil
	}
	if _, occupied := rec.Evidence[d.Revision]; occupied {
		return Result{Class: ClassConflict, Key: key, State: rec.State, Revision: d.Revision,
			Reason: fmt.Sprintf("revision %d is already recorded with a different digest; silent overwrite is forbidden", d.Revision)}, nil
	}

	next := rec.Clone()
	var class Class
	var reason string
	switch d.Kind {
	case semantic.KindApplied:
		// Slot 0 is free, so the origin was not observed yet. A late Applied
		// on a terminal record merges as the cancelled origin without
		// reactivating it; on a pending record it activates the activation.
		origin := cloneDescriptor(d)
		next.Origin = &origin
		next.Evidence[0] = content
		if rec.State == StateRemoved {
			class = ClassStale
			reason = "late applied merges as the origin of the removed activation; the tombstone keeps its cancel meaning and the terminal state"
		} else {
			next.State = StateActive
			class = ClassAccepted
			reason = "applied activates the pending activation with its original record plus the evidence already received"
		}
	case semantic.KindStatus:
		next.Evidence[d.Revision] = content
		if d.Revision > rec.MaxRevision {
			next.MaxRevision = d.Revision
		}
		switch {
		case rec.State == StateRemoved:
			class = ClassStale
			reason = "late status evidence on the terminal activation: recorded in its free revision slot without reactivation"
		case d.Revision > rec.MaxRevision:
			class = ClassAccepted
			reason = fmt.Sprintf("status revision %d replaces the snapshot as the new evidence frontier", d.Revision)
		default:
			class = ClassStale
			reason = fmt.Sprintf("status revision %d is older than the recorded evidence revision %d; the newer evidence stays", d.Revision, rec.MaxRevision)
		}
	default:
		// Removed is terminal regardless of delivery order: it terminalizes
		// a pending or active record at any free revision slot, and a second
		// Removed on a terminal record only adds late evidence.
		next.Evidence[d.Revision] = content
		if d.Revision > rec.MaxRevision {
			next.MaxRevision = d.Revision
		}
		if rec.State == StateRemoved {
			class = ClassStale
			reason = "late removed evidence on the terminal activation: recorded in its free revision slot, the record stays removed"
		} else {
			next.State = StateRemoved
			next.RemovedRevision = d.Revision
			class = ClassAccepted
			reason = "removed terminalizes the activation; the tombstone keeps its cancel meaning for a late applied"
		}
	}
	a.records[key] = next
	return Result{Class: class, Key: key, State: next.State, Revision: d.Revision, Reason: reason}, nil
}
