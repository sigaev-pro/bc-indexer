// Package decision is the client-independent child-root, native-marker and
// mirror seam of E01-02.decision_bridge. It records the transition intent of
// each native decision before the native mutation, recognizes the native
// decision marker that the native patch writes in its final authoritative
// batch, records the NATIVE_DECIDED result with its single delivery
// obligation, and closes the EXEC_READY observations of E01-02.admission
// that the decision resolved. The design is in README.md. The package
// imports only the standard library and the shared admission package.
package decision

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

	"github.com/ethereum/go-ethereum/extractor/admission"
)

// Roots checks the child-root barrier in the native store.
type Roots interface {
	// Durable reports whether the state root is durable in the native store:
	// committed by the native StateDB, flushed through the hash provider and
	// synced with SyncKeyValue.
	Durable(root string) (bool, error)
}

// Mirror is the decision mirror of E05-01.registry.
type Mirror interface {
	// Applied reports whether the mirror durably applied the decision with
	// this NATIVE_DECIDED digest, so that its applied-decision frontier
	// covers it.
	Applied(sequence uint64, decidedDigest string) (bool, error)
}

// Key prefixes. Every key is longer than 33 bytes and starts with "e", so
// the native offline pruner never deletes it.
const (
	keyPrefix    = "extractor-decision-v1/"
	markerPrefix = "extractor-decision-marker-v1/"
)

// Domain tags of the digests. Each is followed by one zero byte.
const (
	tagIntent   = "extractor-decision-intent-v1"
	tagDecided  = "extractor-decision-decided-v1"
	tagRejected = "extractor-decision-rejected-v1"
)

var (
	// ErrInvalid rejects a malformed argument.
	ErrInvalid = errors.New("invalid decision argument")
	// ErrPending refuses a step while a transition intent is unresolved, or
	// GoLive while an EXEC_READY observation has no decision.
	ErrPending = errors.New("a decision is still pending")
	// ErrState rejects a step that does not apply to the decision state.
	ErrState = errors.New("wrong decision state")
	// ErrUnknown reports a decision that is not open.
	ErrUnknown = errors.New("unknown decision")
	// ErrConflict reports a second bridge instance of the same producer run.
	ErrConflict = errors.New("conflicting decision record")
	// ErrBinding rejects a member that is not an open EXEC_READY observation
	// with this EXEC_READY digest.
	ErrBinding = errors.New("decision member binding mismatch")
	// ErrChildRoot rejects a member whose child root is not durable.
	ErrChildRoot = errors.New("child root is not durable")
	// ErrDuplicate rejects a member that is not the authoritative
	// observation of its block.
	ErrDuplicate = errors.New("block already has an authoritative observation")
	// ErrNoMarker reports that the native decision marker is absent. An
	// absent marker is never a decision.
	ErrNoMarker = errors.New("native decision marker is absent")
	// ErrInvariant reports native data that contradicts the bridge records:
	// a marker that differs from its intent, a marker of a rejected intent,
	// or a marker removed before the mirror applied its decision.
	ErrInvariant = errors.New("source invariant error")
	// ErrCorrupt reports a damaged, missing or contradictory record.
	ErrCorrupt = errors.New("corrupt decision record")
	// ErrFailed reports a bridge that stopped after an error.
	ErrFailed = errors.New("decision bridge stopped after an error")
)

var (
	digestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	storeIDPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._:-]{0,127}$`)
)

// Kind is the kind of a native decision.
type Kind string

// Decision kinds.
const (
	// Import moves the head over newly executed blocks.
	Import Kind = "IMPORT"
	// Side stores newly executed blocks without a head change.
	Side Kind = "SIDE"
	// Canonical changes the canonical chain without a new execution, or
	// together with newly executed blocks in a reorganization.
	Canonical Kind = "CANONICAL"
	// Finality changes the native safe or finalized block.
	Finality Kind = "FINALITY"
)

// Delivery is the delivery result of a NATIVE_DECIDED decision.
type Delivery string

// Deliveries. A decision with public events has one delivery batch; a
// decision without them only advances the decision checkpoint.
const (
	Batch      Delivery = "BATCH"
	Checkpoint Delivery = "CHECKPOINT"
)

// State is the state of one decision.
type State string

// Decision states.
const (
	Intended State = "INTENT"
	Decided  State = "NATIVE_DECIDED"
	Rejected State = "NATIVE_REJECTED"
)

// Head is a native head. The hash is 64 lowercase hex digits.
type Head struct {
	Number uint64 `json:"number"`
	Hash   string `json:"hash"`
}

// Member is one EXEC_READY observation that a decision resolves. Block is
// the candidate block hash; Intend fills it from the observation.
type Member struct {
	Observation     string `json:"observation"`
	Block           string `json:"block"`
	ExecReadyDigest string `json:"exec_ready_digest"`
	ChildRoot       string `json:"child_root"`
}

// Intent is a transition intent as the caller proposes it.
type Intent struct {
	Kind           Kind
	OldHead        Head
	NewHead        Head
	Members        []Member
	EvidenceDigest string
	Events         bool
}

// IntentRecord is the durable transition intent. Its JSON bytes are also the
// value of the native decision marker.
type IntentRecord struct {
	NativeStoreID  string   `json:"native_store_id"`
	Producer       string   `json:"producer"`
	Sequence       uint64   `json:"sequence"`
	Kind           Kind     `json:"kind"`
	OldHead        Head     `json:"old_head"`
	NewHead        Head     `json:"new_head"`
	Members        []Member `json:"members"`
	EvidenceDigest string   `json:"evidence_digest"`
	Events         bool     `json:"events"`
	Digest         string   `json:"digest"`
}

// DecidedRecord is the NATIVE_DECIDED record. It holds the single delivery
// result of the decision.
type DecidedRecord struct {
	Sequence     uint64   `json:"sequence"`
	IntentDigest string   `json:"intent_digest"`
	Delivery     Delivery `json:"delivery"`
	Digest       string   `json:"digest"`
}

// RejectedRecord is the native rejection of a transition intent.
type RejectedRecord struct {
	Sequence       uint64 `json:"sequence"`
	IntentDigest   string `json:"intent_digest"`
	EvidenceDigest string `json:"evidence_digest"`
	Digest         string `json:"digest"`
}

// Decision is the state of one decision.
type Decision struct {
	Sequence uint64
	State    State
	Intent   IntentRecord
	Decided  *DecidedRecord
	Rejected *RejectedRecord
	// Marker reports whether the native decision marker is present.
	Marker bool
}

// Obligation is the delivery obligation of one NATIVE_DECIDED decision with
// public events, until the mirror applies it.
type Obligation struct {
	Intent  IntentRecord
	Decided DecidedRecord
}

type counterRecord struct {
	Next uint64 `json:"next"`
}

type lowwaterRecord struct {
	Sequence uint64 `json:"sequence"`
}

type purgedRecord struct {
	Sequence uint64 `json:"sequence"`
}

func u64(value uint64) []byte { return binary.BigEndian.AppendUint64(nil, value) }

func str(value string) []byte { return append(u64(uint64(len(value))), value...) }

func raw(value string) ([]byte, error) {
	if !digestPattern.MatchString(value) {
		return nil, fmt.Errorf("%w: digest %q", ErrInvalid, value)
	}
	out, _ := hex.DecodeString(value)
	return out, nil
}

func digestOf(parts ...[]byte) string {
	sum := sha256.Sum256(bytes.Join(parts, nil))
	return hex.EncodeToString(sum[:])
}

func head(h Head) ([]byte, error) {
	hash, err := raw(h.Hash)
	if err != nil {
		return nil, err
	}
	return append(u64(h.Number), hash...), nil
}

// IntentDigest returns the digest of a transition intent; the Digest field
// of r is not read.
func IntentDigest(r IntentRecord) (string, error) {
	parts := [][]byte{append([]byte(tagIntent), 0), str(r.NativeStoreID), str(r.Producer), u64(r.Sequence),
		str(string(r.Kind))}
	for _, h := range []Head{r.OldHead, r.NewHead} {
		value, err := head(h)
		if err != nil {
			return "", err
		}
		parts = append(parts, value)
	}
	parts = append(parts, u64(uint64(len(r.Members))))
	for _, m := range r.Members {
		parts = append(parts, str(m.Observation))
		for _, value := range []string{m.Block, m.ExecReadyDigest, m.ChildRoot} {
			b, err := raw(value)
			if err != nil {
				return "", err
			}
			parts = append(parts, b)
		}
	}
	evidence, err := raw(r.EvidenceDigest)
	if err != nil {
		return "", err
	}
	events := byte(0)
	if r.Events {
		events = 1
	}
	return digestOf(append(parts, evidence, []byte{events})...), nil
}

// DecidedDigest returns the digest of a NATIVE_DECIDED record.
func DecidedDigest(intentDigest string, delivery Delivery) (string, error) {
	intent, err := raw(intentDigest)
	if err != nil {
		return "", err
	}
	return digestOf(append([]byte(tagDecided), 0), intent, str(string(delivery))), nil
}

// RejectedDigest returns the digest of the rejection of a transition intent.
func RejectedDigest(intentDigest, evidenceDigest string) (string, error) {
	intent, err := raw(intentDigest)
	if err != nil {
		return "", err
	}
	evidence, err := raw(evidenceDigest)
	if err != nil {
		return "", err
	}
	return digestOf(append([]byte(tagRejected), 0), intent, evidence), nil
}

// MarkerKey returns the native store key of the decision marker of a
// sequence. The native patch writes MarkerValue under it in the final
// authoritative native batch of the decision.
func MarkerKey(p admission.Producer, sequence uint64) []byte {
	return []byte(fmt.Sprintf("%s%s.%s/%016x", markerPrefix, p.ProducerID, p.RunID, sequence))
}

// MarkerValue returns the exact bytes of the native decision marker: the
// JSON encoding of the intent record.
func MarkerValue(r IntentRecord) ([]byte, error) {
	return json.Marshal(r)
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

func readRecord(db admission.Store, key []byte, value any) (bool, error) {
	ok, err := db.Has(key)
	if err != nil || !ok {
		return false, err
	}
	data, err := db.Get(key)
	if err != nil {
		return false, err
	}
	if err := strictDecode(data, value); err != nil {
		return false, fmt.Errorf("%w: %s: %v", ErrCorrupt, key, err)
	}
	return true, nil
}

func putRecord(db admission.Store, key []byte, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return db.Put(key, data)
}

// recordKinds are the per-decision record names.
var recordKinds = []string{"intent", "decided", "rejected"}

// pruneBatch bounds the sequences whose records one sync deletes.
const pruneBatch = 256

// Bridge records the decisions of one producer run. It shares the
// observation guard of its admission; every call needs the held token. It is
// not safe for concurrent use.
type Bridge struct {
	db       admission.DeleteStore
	adm      *admission.Admission
	producer admission.Producer
	storeID  string
	base     string
	roots    Roots
	mirror   Mirror
	next     uint64
	lowwater uint64
	purged   uint64
	open     map[uint64]*Decision
	err      error
}

func (b *Bridge) key(suffix string) []byte { return []byte(b.base + suffix) }

func (b *Bridge) recordKey(seq uint64, kind string) []byte {
	return b.key(fmt.Sprintf("%016x/%s", seq, kind))
}

// Open syncs the store and opens the decisions of the producer run of adm.
// It checks every decision above the low-water mark against its records and
// its native marker, finishes the closes of the observations that a decided
// or rejected decision resolved, then advances the low-water mark and
// deletes the records below it. storeID is the native store identity that
// every intent and marker of this store carries.
func Open(t *admission.Token, db admission.DeleteStore, adm *admission.Admission, storeID string, roots Roots,
	mirror Mirror) (*Bridge, error) {
	if adm == nil {
		return nil, fmt.Errorf("%w: missing admission", ErrInvalid)
	}
	if err := adm.Check(t); err != nil {
		return nil, err
	}
	if db == nil || roots == nil || mirror == nil {
		return nil, fmt.Errorf("%w: missing store, roots or mirror", ErrInvalid)
	}
	if !storeIDPattern.MatchString(storeID) {
		return nil, fmt.Errorf("%w: native store ID %q", ErrInvalid, storeID)
	}
	if err := db.SyncKeyValue(); err != nil {
		return nil, err
	}
	p := adm.Producer()
	b := &Bridge{db: db, adm: adm, producer: p, storeID: storeID,
		base: keyPrefix + p.ProducerID + "." + p.RunID + "/", roots: roots, mirror: mirror,
		open: map[uint64]*Decision{}}
	var counter counterRecord
	if _, err := readRecord(db, b.key("counter"), &counter); err != nil {
		return nil, err
	}
	var low lowwaterRecord
	if _, err := readRecord(db, b.key("lowwater"), &low); err != nil {
		return nil, err
	}
	var purged purgedRecord
	if _, err := readRecord(db, b.key("purged"), &purged); err != nil {
		return nil, err
	}
	if low.Sequence > counter.Next || purged.Sequence > low.Sequence {
		return nil, fmt.Errorf("%w: purged %d, low-water %d, counter %d", ErrCorrupt, purged.Sequence,
			low.Sequence, counter.Next)
	}
	b.next, b.lowwater, b.purged = counter.Next, low.Sequence, purged.Sequence
	var resolved []*Decision
	for seq := low.Sequence; seq < counter.Next; seq++ {
		d, err := b.load(seq)
		if err != nil {
			return nil, err
		}
		if d.State == Intended && seq != counter.Next-1 {
			return nil, fmt.Errorf("%w: unresolved intent %d below the counter %d", ErrCorrupt, seq, counter.Next)
		}
		if d.State != Intended {
			resolved = append(resolved, d)
		}
		if d.State == Intended || (d.State == Decided && d.Marker) {
			b.open[seq] = d
		}
	}
	// Finish the closes that a power loss interrupted after the decided or
	// rejected record was synced.
	for _, d := range resolved {
		if err := b.closeMembers(t, d); err != nil {
			return nil, err
		}
	}
	// A NATIVE_DECIDED SIDE decision without marker stays open while one of
	// its members is retained.
	observations, err := adm.Observations(t)
	if err != nil {
		return nil, err
	}
	for _, d := range resolved {
		if d.State == Decided && !d.Marker && retains(d, observations) {
			b.open[d.Sequence] = d
		}
	}
	if err := b.advance(); err != nil {
		return nil, err
	}
	if err := b.prune(); err != nil {
		return nil, err
	}
	return b, nil
}

// load reads and checks the records and the marker of one existing sequence.
func (b *Bridge) load(seq uint64) (*Decision, error) {
	var intent IntentRecord
	ok, err := readRecord(b.db, b.recordKey(seq, "intent"), &intent)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: missing intent %d", ErrCorrupt, seq)
	}
	want, err := IntentDigest(intent)
	if err != nil {
		return nil, fmt.Errorf("%w: intent %d: %v", ErrCorrupt, seq, err)
	}
	if intent.NativeStoreID != b.storeID || intent.Producer != b.producer.ProducerID+"."+b.producer.RunID ||
		intent.Sequence != seq || intent.Digest != want {
		return nil, fmt.Errorf("%w: intent %d", ErrCorrupt, seq)
	}
	d := &Decision{Sequence: seq, State: Intended, Intent: intent}
	marker, err := b.marker(intent)
	if err != nil {
		return nil, err
	}
	d.Marker = marker
	var decided DecidedRecord
	hasDecided, err := readRecord(b.db, b.recordKey(seq, "decided"), &decided)
	if err != nil {
		return nil, err
	}
	var rejected RejectedRecord
	hasRejected, err := readRecord(b.db, b.recordKey(seq, "rejected"), &rejected)
	if err != nil {
		return nil, err
	}
	if hasDecided {
		want, err := DecidedDigest(intent.Digest, delivery(intent))
		if hasRejected || err != nil || decided.Sequence != seq || decided.IntentDigest != intent.Digest ||
			decided.Delivery != delivery(intent) || decided.Digest != want {
			return nil, fmt.Errorf("%w: NATIVE_DECIDED record %d", ErrCorrupt, seq)
		}
		d.State, d.Decided = Decided, &decided
		if !marker && decided.Delivery == Batch {
			applied, err := b.mirror.Applied(seq, decided.Digest)
			if err != nil {
				return nil, err
			}
			if !applied {
				return nil, fmt.Errorf("%w: marker of decision %d removed before the mirror applied it",
					ErrInvariant, seq)
			}
		}
	}
	if hasRejected {
		want, err := RejectedDigest(intent.Digest, rejected.EvidenceDigest)
		if err != nil || rejected.Sequence != seq || rejected.IntentDigest != intent.Digest || rejected.Digest != want {
			return nil, fmt.Errorf("%w: rejected intent record %d", ErrCorrupt, seq)
		}
		if marker {
			return nil, fmt.Errorf("%w: rejected intent %d has a native marker", ErrInvariant, seq)
		}
		d.State, d.Rejected = Rejected, &rejected
	}
	return d, nil
}

// marker reports whether the native marker of an intent is present. A
// marker with other bytes is ErrInvariant.
func (b *Bridge) marker(intent IntentRecord) (bool, error) {
	key := MarkerKey(b.producer, intent.Sequence)
	ok, err := b.db.Has(key)
	if err != nil || !ok {
		return false, err
	}
	data, err := b.db.Get(key)
	if err != nil {
		return false, err
	}
	want, err := MarkerValue(intent)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(data, want) {
		return false, fmt.Errorf("%w: native marker %d differs from its intent", ErrInvariant, intent.Sequence)
	}
	return true, nil
}

// retains reports whether a SIDE decision still retains an open member. A
// SIDE decision keeps its members EXEC_READY, with their pins and inputs,
// because a known side block can later become canonical without a new
// execution (NATIVE_CAPTURE_PROTOCOL, known branch).
func retains(d *Decision, observations []admission.Observation) bool {
	if d.Intent.Kind != Side {
		return false
	}
	for _, m := range d.Intent.Members {
		for _, obs := range observations {
			if obs.Key == m.Observation {
				return true
			}
		}
	}
	return false
}

func delivery(intent IntentRecord) Delivery {
	if intent.Events {
		return Batch
	}
	return Checkpoint
}

func (b *Bridge) enter(t *admission.Token) error {
	if err := b.adm.Check(t); err != nil {
		return err
	}
	if b.err != nil {
		return fmt.Errorf("%w: %v", ErrFailed, b.err)
	}
	return nil
}

func (b *Bridge) fail(err error) error {
	b.err = err
	return err
}

func (b *Bridge) unresolved() *Decision {
	for _, d := range b.open {
		if d.State == Intended {
			return d
		}
	}
	return nil
}

func copyDecision(d *Decision) Decision {
	out := *d
	out.Intent.Members = append([]Member{}, d.Intent.Members...)
	if d.Decided != nil {
		decided := *d.Decided
		out.Decided = &decided
	}
	if d.Rejected != nil {
		rejected := *d.Rejected
		out.Rejected = &rejected
	}
	return out
}

// Decisions lists every open decision in sequence order: an unresolved
// intent, and NATIVE_DECIDED decisions whose native marker is not yet
// removed.
func (b *Bridge) Decisions(t *admission.Token) ([]Decision, error) {
	if err := b.enter(t); err != nil {
		return nil, err
	}
	out := []Decision{}
	for seq := b.lowwater; seq < b.next; seq++ {
		if d, ok := b.open[seq]; ok {
			out = append(out, copyDecision(d))
		}
	}
	return out, nil
}

// Undecided lists the EXEC_READY observations of the admission that no open
// decision names: neither an unresolved intent nor a SIDE decision that
// retains them. Each needs a decision before GoLive.
func (b *Bridge) Undecided(t *admission.Token) ([]admission.Observation, error) {
	if err := b.enter(t); err != nil {
		return nil, err
	}
	observations, err := b.adm.Observations(t)
	if err != nil {
		return nil, err
	}
	named := map[string]bool{}
	for _, d := range b.open {
		for _, m := range d.Intent.Members {
			named[m.Observation] = true
		}
	}
	out := []admission.Observation{}
	for _, obs := range observations {
		if obs.State == admission.ExecReady && !named[obs.Key] {
			out = append(out, obs)
		}
	}
	return out, nil
}

// GoLive switches the admission to LIVE. It is refused while a transition
// intent is unresolved or an EXEC_READY observation has no decision, and
// otherwise follows admission.GoLive.
func (b *Bridge) GoLive(t *admission.Token) error {
	if err := b.enter(t); err != nil {
		return err
	}
	if d := b.unresolved(); d != nil {
		return fmt.Errorf("%w: intent %d is unresolved", ErrPending, d.Sequence)
	}
	undecided, err := b.Undecided(t)
	if err != nil {
		return err
	}
	if len(undecided) != 0 {
		return fmt.Errorf("%w: %s is EXEC_READY without a decision", ErrPending, undecided[0].Key)
	}
	return b.adm.GoLive(t)
}

func validHead(h Head) error {
	if !digestPattern.MatchString(h.Hash) {
		return fmt.Errorf("%w: head hash %q", ErrInvalid, h.Hash)
	}
	return nil
}

func validIntent(in Intent) error {
	if err := validHead(in.OldHead); err != nil {
		return err
	}
	if err := validHead(in.NewHead); err != nil {
		return err
	}
	if !digestPattern.MatchString(in.EvidenceDigest) {
		return fmt.Errorf("%w: evidence digest %q", ErrInvalid, in.EvidenceDigest)
	}
	switch in.Kind {
	case Import:
		if len(in.Members) == 0 {
			return fmt.Errorf("%w: IMPORT without members", ErrInvalid)
		}
	case Side:
		if len(in.Members) == 0 || in.Events || in.OldHead != in.NewHead {
			return fmt.Errorf("%w: SIDE needs members, no events and an unchanged head", ErrInvalid)
		}
	case Canonical:
	case Finality:
		if len(in.Members) != 0 {
			return fmt.Errorf("%w: FINALITY with members", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: kind %q", ErrInvalid, in.Kind)
	}
	for _, m := range in.Members {
		if !digestPattern.MatchString(m.ExecReadyDigest) || !digestPattern.MatchString(m.ChildRoot) {
			return fmt.Errorf("%w: member %q digests", ErrInvalid, m.Observation)
		}
	}
	return nil
}

// Intend records the transition intent of the next decision and syncs it,
// before the native mutation. It writes the intent record, syncs, then
// writes the counter and syncs; the synced counter is the commit point.
//
// Every member must be an open EXEC_READY observation with this EXEC_READY
// digest, the authoritative observation of its block (the lowest open
// EXEC_READY sequence of that block), and its child root must be durable. A
// member block with an open observation that is not EXEC_READY is
// ErrPending: that observation must first reach EXEC_READY or an admission
// exit. Intend is refused while another intent is unresolved.
func (b *Bridge) Intend(t *admission.Token, in Intent) (IntentRecord, error) {
	if err := b.enter(t); err != nil {
		return IntentRecord{}, err
	}
	if err := validIntent(in); err != nil {
		return IntentRecord{}, err
	}
	if d := b.unresolved(); d != nil {
		return IntentRecord{}, fmt.Errorf("%w: intent %d is unresolved", ErrPending, d.Sequence)
	}
	observations, err := b.adm.Observations(t)
	if err != nil {
		return IntentRecord{}, err
	}
	members := []Member{}
	keys, blocks := map[string]bool{}, map[string]bool{}
	for _, m := range in.Members {
		var obs *admission.Observation
		for i := range observations {
			if observations[i].Key == m.Observation {
				obs = &observations[i]
			}
		}
		if obs == nil || obs.State != admission.ExecReady || obs.ExecReady.Digest != m.ExecReadyDigest {
			return IntentRecord{}, fmt.Errorf("%w: %q is not EXEC_READY with digest %s", ErrBinding, m.Observation,
				m.ExecReadyDigest)
		}
		if keys[obs.Key] || blocks[obs.Candidate.Hash] {
			return IntentRecord{}, fmt.Errorf("%w: block %s named twice", ErrDuplicate, obs.Candidate.Hash)
		}
		keys[obs.Key], blocks[obs.Candidate.Hash] = true, true
		for _, other := range observations {
			if other.Candidate.Hash != obs.Candidate.Hash {
				continue
			}
			if other.State != admission.ExecReady {
				return IntentRecord{}, fmt.Errorf("%w: %s of block %s is %s", ErrPending, other.Key,
					obs.Candidate.Hash, other.State)
			}
			if other.Sequence < obs.Sequence {
				return IntentRecord{}, fmt.Errorf("%w: %s is the authoritative observation of block %s",
					ErrDuplicate, other.Key, obs.Candidate.Hash)
			}
		}
		durable, err := b.roots.Durable(m.ChildRoot)
		if err != nil {
			return IntentRecord{}, fmt.Errorf("%w: %s: %w", ErrChildRoot, m.ChildRoot, err)
		}
		if !durable {
			return IntentRecord{}, fmt.Errorf("%w: %s of %s", ErrChildRoot, m.ChildRoot, obs.Key)
		}
		members = append(members, Member{Observation: obs.Key, Block: obs.Candidate.Hash,
			ExecReadyDigest: m.ExecReadyDigest, ChildRoot: m.ChildRoot})
	}
	// Only one Bridge may own a producer run. A durable counter that moved
	// means another instance wrote intents behind this one.
	var counter counterRecord
	if _, err := readRecord(b.db, b.key("counter"), &counter); err != nil {
		return IntentRecord{}, b.fail(err)
	}
	if counter.Next != b.next {
		return IntentRecord{}, b.fail(fmt.Errorf("%w: decision counter is %d, this bridge expects %d", ErrConflict,
			counter.Next, b.next))
	}
	seq := b.next
	// A record beyond the counter can only be an orphan intent of an
	// interrupted Intend; any later record or a marker means damage.
	for _, key := range [][]byte{b.recordKey(seq, "decided"), b.recordKey(seq, "rejected"), MarkerKey(b.producer, seq)} {
		ok, err := b.db.Has(key)
		if err != nil {
			return IntentRecord{}, b.fail(err)
		}
		if ok {
			return IntentRecord{}, b.fail(fmt.Errorf("%w: %s beyond the counter", ErrCorrupt, key))
		}
	}
	record := IntentRecord{NativeStoreID: b.storeID, Producer: b.producer.ProducerID + "." + b.producer.RunID,
		Sequence: seq, Kind: in.Kind, OldHead: in.OldHead, NewHead: in.NewHead, Members: members,
		EvidenceDigest: in.EvidenceDigest, Events: in.Events}
	if record.Digest, err = IntentDigest(record); err != nil {
		return IntentRecord{}, err
	}
	if err := putRecord(b.db, b.recordKey(seq, "intent"), record); err != nil {
		return IntentRecord{}, b.fail(err)
	}
	if err := b.db.SyncKeyValue(); err != nil {
		return IntentRecord{}, b.fail(err)
	}
	if err := putRecord(b.db, b.key("counter"), counterRecord{Next: seq + 1}); err != nil {
		return IntentRecord{}, b.fail(err)
	}
	if err := b.db.SyncKeyValue(); err != nil {
		return IntentRecord{}, b.fail(err)
	}
	b.next = seq + 1
	stored := record
	stored.Members = append([]Member{}, record.Members...)
	b.open[seq] = &Decision{Sequence: seq, State: Intended, Intent: stored}
	return record, nil
}

func (b *Bridge) find(seq uint64) (*Decision, error) {
	d, ok := b.open[seq]
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnknown, seq)
	}
	return d, nil
}

// Resolve records NATIVE_DECIDED after the native marker of an intent is
// durable. It writes the decided record and syncs; that sync is the commit
// point of the decision and of its single delivery obligation. Then it
// closes the resolved observations in the admission. An absent marker is
// ErrNoMarker and writes nothing: only native recovery, which completes the
// saved transition or rejects it, resolves such an intent. Resolving a
// NATIVE_DECIDED decision again returns its record.
func (b *Bridge) Resolve(t *admission.Token, seq uint64) (DecidedRecord, error) {
	if err := b.enter(t); err != nil {
		return DecidedRecord{}, err
	}
	d, err := b.find(seq)
	if err != nil {
		return DecidedRecord{}, err
	}
	if d.State == Decided {
		return *d.Decided, nil
	}
	marker, err := b.marker(d.Intent)
	if err != nil {
		return DecidedRecord{}, b.fail(err)
	}
	if !marker {
		return DecidedRecord{}, fmt.Errorf("%w: decision %d", ErrNoMarker, seq)
	}
	digest, err := DecidedDigest(d.Intent.Digest, delivery(d.Intent))
	if err != nil {
		return DecidedRecord{}, err
	}
	record := DecidedRecord{Sequence: seq, IntentDigest: d.Intent.Digest, Delivery: delivery(d.Intent), Digest: digest}
	if err := putRecord(b.db, b.recordKey(seq, "decided"), record); err != nil {
		return DecidedRecord{}, b.fail(err)
	}
	if err := b.db.SyncKeyValue(); err != nil {
		return DecidedRecord{}, b.fail(err)
	}
	d.State, d.Decided, d.Marker = Decided, &record, true
	if err := b.closeMembers(t, d); err != nil {
		return DecidedRecord{}, b.fail(err)
	}
	return record, nil
}

// RejectIntent records the native rejection of an unresolved intent, with
// the digest of the native rejection evidence, and syncs it. It closes the
// observations the intent named; no NATIVE_DECIDED record, delivery
// obligation or Applied follows. A present native marker means the native
// side accepted the transition and is ErrInvariant.
func (b *Bridge) RejectIntent(t *admission.Token, seq uint64, evidenceDigest string) (RejectedRecord, error) {
	if err := b.enter(t); err != nil {
		return RejectedRecord{}, err
	}
	if !digestPattern.MatchString(evidenceDigest) {
		return RejectedRecord{}, fmt.Errorf("%w: evidence digest %q", ErrInvalid, evidenceDigest)
	}
	d, err := b.find(seq)
	if err != nil {
		return RejectedRecord{}, err
	}
	if d.State != Intended {
		return RejectedRecord{}, fmt.Errorf("%w: RejectIntent of %d in %s", ErrState, seq, d.State)
	}
	marker, err := b.marker(d.Intent)
	if err != nil {
		return RejectedRecord{}, b.fail(err)
	}
	if marker {
		return RejectedRecord{}, fmt.Errorf("%w: intent %d has a native marker", ErrInvariant, seq)
	}
	digest, err := RejectedDigest(d.Intent.Digest, evidenceDigest)
	if err != nil {
		return RejectedRecord{}, err
	}
	record := RejectedRecord{Sequence: seq, IntentDigest: d.Intent.Digest, EvidenceDigest: evidenceDigest,
		Digest: digest}
	if err := putRecord(b.db, b.recordKey(seq, "rejected"), record); err != nil {
		return RejectedRecord{}, b.fail(err)
	}
	if err := b.db.SyncKeyValue(); err != nil {
		return RejectedRecord{}, b.fail(err)
	}
	d.State, d.Rejected = Rejected, &record
	delete(b.open, seq)
	if err := b.closeMembers(t, d); err != nil {
		return RejectedRecord{}, b.fail(err)
	}
	if err := b.advance(); err != nil {
		return RejectedRecord{}, err
	}
	if err := b.prune(); err != nil {
		return RejectedRecord{}, err
	}
	return record, nil
}

// closeMembers closes, through admission.Decide, every open observation that
// a resolved decision covers: its members and every other open EXEC_READY
// observation of a member block, so a block observed twice gets one
// decision. It repeats safely: closed observations are no longer listed.
func (b *Bridge) closeMembers(t *admission.Token, d *Decision) error {
	digest := ""
	if d.Decided != nil {
		if d.Intent.Kind == Side {
			// The side block may become canonical later without a new
			// execution: its observations stay EXEC_READY with their pins
			// and inputs, and the SIDE decision retains them.
			return nil
		}
		digest = d.Decided.Digest
	} else {
		digest = d.Rejected.Digest
	}
	blocks := map[string]bool{}
	for _, m := range d.Intent.Members {
		blocks[m.Block] = true
	}
	observations, err := b.adm.Observations(t)
	if err != nil {
		return err
	}
	for _, obs := range observations {
		if !blocks[obs.Candidate.Hash] {
			continue
		}
		if obs.State != admission.ExecReady {
			return fmt.Errorf("%w: %s of decided block %s is %s", ErrInvariant, obs.Key, obs.Candidate.Hash, obs.State)
		}
		if _, err := b.adm.Decide(t, obs.Key, digest); err != nil {
			return err
		}
	}
	return nil
}

// Obligations lists, in sequence order, the delivery obligation of every
// NATIVE_DECIDED decision with public events that the mirror has not
// applied. Each such decision has exactly one obligation; a decision without
// public events and a rejected intent have none.
func (b *Bridge) Obligations(t *admission.Token) ([]Obligation, error) {
	if err := b.enter(t); err != nil {
		return nil, err
	}
	out := []Obligation{}
	for seq := b.lowwater; seq < b.next; seq++ {
		d, ok := b.open[seq]
		if !ok || d.State != Decided || d.Decided.Delivery != Batch {
			continue
		}
		applied, err := b.mirror.Applied(seq, d.Decided.Digest)
		if err != nil {
			return nil, err
		}
		if !applied {
			c := copyDecision(d)
			out = append(out, Obligation{Intent: c.Intent, Decided: *c.Decided})
		}
	}
	return out, nil
}

// Cleanup removes the native marker of every NATIVE_DECIDED decision that
// needs it no longer: a decision without public events, or one the mirror
// applied. It deletes the markers and syncs in one separate step, then
// advances the low-water mark. After a power loss before that sync the
// markers stay, and the mirror's applied frontier keeps their decisions out
// of Obligations until Cleanup runs again. A SIDE decision stays open
// without its marker while it retains a member; it is done once a later
// decision closed every member.
func (b *Bridge) Cleanup(t *admission.Token) error {
	if err := b.enter(t); err != nil {
		return err
	}
	var done []*Decision
	for seq := b.lowwater; seq < b.next; seq++ {
		d, ok := b.open[seq]
		if !ok || d.State != Decided || !d.Marker {
			continue
		}
		if d.Decided.Delivery == Batch {
			applied, err := b.mirror.Applied(seq, d.Decided.Digest)
			if err != nil {
				return err
			}
			if !applied {
				continue
			}
		}
		if err := b.db.Delete(MarkerKey(b.producer, seq)); err != nil {
			return b.fail(err)
		}
		done = append(done, d)
	}
	if len(done) != 0 {
		if err := b.db.SyncKeyValue(); err != nil {
			return b.fail(err)
		}
		for _, d := range done {
			d.Marker = false
		}
	}
	observations, err := b.adm.Observations(t)
	if err != nil {
		return err
	}
	for seq, d := range b.open {
		if d.State == Decided && !d.Marker && !retains(d, observations) {
			delete(b.open, seq)
		}
	}
	if err := b.advance(); err != nil {
		return err
	}
	return b.prune()
}

// advance moves the low-water mark past the done decisions at its front.
func (b *Bridge) advance() error {
	low := b.lowwater
	for low < b.next {
		if _, open := b.open[low]; open {
			break
		}
		low++
	}
	if low == b.lowwater {
		return nil
	}
	if err := putRecord(b.db, b.key("lowwater"), lowwaterRecord{Sequence: low}); err != nil {
		return b.fail(err)
	}
	if err := b.db.SyncKeyValue(); err != nil {
		return b.fail(err)
	}
	b.lowwater = low
	return nil
}

// prune deletes the records of the decisions below the low-water mark in
// batches of pruneBatch sequences. Each batch syncs its deletions and then
// the advanced purged mark, so a power loss only repeats a batch.
func (b *Bridge) prune() error {
	for b.purged < b.lowwater {
		end := min(b.lowwater, b.purged+pruneBatch)
		for seq := b.purged; seq < end; seq++ {
			for _, kind := range recordKinds {
				if err := b.db.Delete(b.recordKey(seq, kind)); err != nil {
					return b.fail(err)
				}
			}
		}
		if err := b.db.SyncKeyValue(); err != nil {
			return b.fail(err)
		}
		if err := putRecord(b.db, b.key("purged"), purgedRecord{Sequence: end}); err != nil {
			return b.fail(err)
		}
		if err := b.db.SyncKeyValue(); err != nil {
			return b.fail(err)
		}
		b.purged = end
	}
	return nil
}
