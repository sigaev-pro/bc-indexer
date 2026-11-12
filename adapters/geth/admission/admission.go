// Package admission is the client-independent guard/pin/ADMITTED seam of
// E01-02.admission. It records each native observation from INPUT_PREPARING
// to ADMITTED and EXEC_READY in the native key-value store, and binds the
// EXEC_READY record to the closed consumed-input manifest of E01-02.inputs.
// The design is in README.md. The package imports only the standard library.
package admission

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
	"sync"
)

// Store is the subset of the native key-value store that this package uses.
// Writes are durable only after SyncKeyValue returns nil; before that, a
// power loss may keep any subset of them. After a process crash, unsynced
// writes may still be visible, so Open syncs before it reads.
type Store interface {
	Has(key []byte) (bool, error)
	Get(key []byte) ([]byte, error)
	Put(key, value []byte) error
	SyncKeyValue() error
}

// Pins is the pin ledger of E01-02.pins. The pin ID of an observation is its
// observation key.
type Pins interface {
	// Verify checks that the durable pin id exists, holds parentRoot and is
	// readable, and returns the SHA-256 of its ledger record.
	Verify(id, parentRoot string) (string, error)
	// Release durably removes the pin. It returns nil for an absent pin.
	Release(id string) error
}

// DeleteStore is a Store that can also delete a key. A Delete is durable
// only after SyncKeyValue returns nil, like a Put.
type DeleteStore interface {
	Store
	Delete(key []byte) error
}

// InputPurger deletes the capsule and dependency log records of a closed
// observation (D100); source_inputs.Purge does this. It must return nil when
// nothing is left to delete.
type InputPurger interface {
	Purge(observation string) error
}

// Consumed is the closed consumed-input manifest of E01-02.inputs.
type Consumed struct {
	Observation   string
	CapsuleDigest string
	Count         uint64
	Chain         string
	Digest        string
}

// Inputs is the input capsule and dependency log of E01-02.inputs.
type Inputs interface {
	// CapsuleDigest returns the digest of the sealed static capsule.
	CapsuleDigest(observation string) (string, error)
	// Consumed returns the durable closed consumed-input manifest, or an
	// error if the log is not closed.
	Consumed(observation string) (Consumed, error)
}

// keyPrefix starts every key. Every key is longer than 33 bytes and starts
// with "e", so the native offline pruner never deletes it.
const keyPrefix = "extractor-admission-v1/"

// Domain tags of the digests. Each is followed by one zero byte.
const (
	tagAdmitted  = "extractor-admission-admitted-v1"
	tagExecReady = "extractor-admission-exec-ready-v1"
	tagRejected  = "extractor-admission-rejected-v1"
	tagDecided   = "extractor-admission-decided-v1"
	tagConsumed  = "extractor-source-inputs-consumed-v1"
)

var (
	// ErrInvalid rejects a malformed argument.
	ErrInvalid = errors.New("invalid admission argument")
	// ErrGuard rejects a call without the held observation guard token.
	ErrGuard = errors.New("observation guard is not held")
	// ErrMode rejects a step that the current mode does not allow.
	ErrMode = errors.New("admission mode forbids this step")
	// ErrPending rejects a new admission or the switch to LIVE while an
	// observation is in INPUT_PREPARING or ADMITTED.
	ErrPending = errors.New("an observation is still pending")
	// ErrState rejects a step from the wrong observation state.
	ErrState = errors.New("wrong observation state")
	// ErrUnknown reports an observation key that does not exist.
	ErrUnknown = errors.New("unknown observation")
	// ErrConflict rejects a repeated step with different arguments.
	ErrConflict = errors.New("conflicting admission record")
	// ErrBinding rejects a pin, capsule or consumed manifest that does not
	// match the admitted observation.
	ErrBinding = errors.New("input binding mismatch")
	// ErrCorrupt reports a stored record that fails its format or digest check.
	ErrCorrupt = errors.New("corrupt admission record")
	// ErrFailed reports an admission that stopped after a store error.
	ErrFailed = errors.New("admission stopped after an error")
)

var (
	partPattern   = regexp.MustCompile(`^[0-9a-z][0-9a-z-]{0,39}$`)
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// State is the admission state of one observation.
type State string

// Observation states. Decided is the close of an EXEC_READY observation by
// E01-02.decision_bridge.
const (
	InputPreparing State = "INPUT_PREPARING"
	Admitted       State = "ADMITTED"
	ExecReady      State = "EXEC_READY"
	Abandoned      State = "ABANDONED"
	Rejected       State = "NATIVE_REJECTED"
	Decided        State = "DECIDED"
)

// Mode is the admission mode of the node.
type Mode string

// Modes. Open starts in RecoveryOnly.
const (
	RecoveryOnly Mode = "RECOVERY_ONLY"
	Live         Mode = "LIVE"
)

// Producer identifies the producer run that owns the observations.
type Producer struct {
	ProducerID string
	RunID      string
}

// Candidate is the native block of an observation. Hashes are 64 lowercase
// hex digits.
type Candidate struct {
	Number     uint64 `json:"number"`
	Hash       string `json:"hash"`
	ParentHash string `json:"parent_hash"`
	ParentRoot string `json:"parent_root"`
}

// Preparing is the INPUT_PREPARING record.
type Preparing struct {
	Observation string    `json:"observation"`
	Sequence    uint64    `json:"sequence"`
	Candidate   Candidate `json:"candidate"`
}

// AdmittedRecord is the ADMITTED record, written by the closing sync.
type AdmittedRecord struct {
	Observation   string    `json:"observation"`
	Sequence      uint64    `json:"sequence"`
	Candidate     Candidate `json:"candidate"`
	PinDigest     string    `json:"pin_digest"`
	CapsuleDigest string    `json:"capsule_digest"`
	Digest        string    `json:"digest"`
}

// ExecReadyRecord is the EXEC_READY record. It binds the admitted
// observation, the closed consumed-input manifest and the record corpus.
type ExecReadyRecord struct {
	Observation    string `json:"observation"`
	Sequence       uint64 `json:"sequence"`
	AdmittedDigest string `json:"admitted_digest"`
	ConsumedDigest string `json:"consumed_digest"`
	ExecDigest     string `json:"exec_digest"`
	Digest         string `json:"digest"`
}

// RejectedRecord is the NATIVE_REJECTED record of an admitted observation
// that native validation refused. It binds the admitted observation and the
// digest of the native rejection evidence.
type RejectedRecord struct {
	Observation    string `json:"observation"`
	Sequence       uint64 `json:"sequence"`
	AdmittedDigest string `json:"admitted_digest"`
	EvidenceDigest string `json:"evidence_digest"`
	Digest         string `json:"digest"`
}

// DecidedRecord closes an EXEC_READY observation. It binds the EXEC_READY
// record and the digest of the decision bridge record that resolved it.
type DecidedRecord struct {
	Observation     string `json:"observation"`
	Sequence        uint64 `json:"sequence"`
	ExecReadyDigest string `json:"exec_ready_digest"`
	DecisionDigest  string `json:"decision_digest"`
	Digest          string `json:"digest"`
}

type abandonedRecord struct {
	Observation string `json:"observation"`
	Sequence    uint64 `json:"sequence"`
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

// Observation is the state of one observation.
type Observation struct {
	Key       string
	Sequence  uint64
	State     State
	Candidate Candidate
	Admitted  *AdmittedRecord
	ExecReady *ExecReadyRecord
}

// Guard is the one observation guard of a node. The lock order is the guard,
// then native locks. A nested native call passes the token it received and
// never acquires the guard again.
type Guard struct {
	lock    sync.Mutex
	state   sync.Mutex
	current *Token
}

// Token proves that its holder holds the guard.
type Token struct {
	guard *Guard
}

// NewGuard returns a free guard.
func NewGuard() *Guard { return &Guard{} }

// Acquire blocks until the guard is free and returns its token.
func (g *Guard) Acquire() *Token {
	g.lock.Lock()
	token := &Token{guard: g}
	g.state.Lock()
	g.current = token
	g.state.Unlock()
	return token
}

// Release frees the guard. Releasing a token that no longer holds it is
// ErrGuard.
func (t *Token) Release() error {
	if t == nil || t.guard == nil {
		return ErrGuard
	}
	g := t.guard
	g.state.Lock()
	if g.current != t {
		g.state.Unlock()
		return ErrGuard
	}
	g.current = nil
	g.state.Unlock()
	g.lock.Unlock()
	return nil
}

func (g *Guard) check(t *Token) error {
	if t == nil {
		return ErrGuard
	}
	g.state.Lock()
	defer g.state.Unlock()
	if g.current != t {
		return ErrGuard
	}
	return nil
}

func sum(data []byte) [32]byte { return sha256.Sum256(data) }

func u64(value uint64) []byte { return binary.BigEndian.AppendUint64(nil, value) }

func tagged(tag string) []byte { return append([]byte(tag), 0) }

func str(value string) []byte { return append(u64(uint64(len(value))), value...) }

func decodeDigest(value string) ([32]byte, error) {
	var out [32]byte
	if !digestPattern.MatchString(value) {
		return out, fmt.Errorf("%w: digest %q", ErrCorrupt, value)
	}
	_, err := hex.Decode(out[:], []byte(value))
	return out, err
}

func digestOf(parts ...[]byte) string {
	digest := sum(bytes.Join(parts, nil))
	return hex.EncodeToString(digest[:])
}

func hashes(values ...string) ([][]byte, error) {
	out := make([][]byte, 0, len(values))
	for _, value := range values {
		raw, err := decodeDigest(value)
		if err != nil {
			return nil, err
		}
		out = append(out, raw[:])
	}
	return out, nil
}

func validCandidate(c Candidate) error {
	for _, value := range []string{c.Hash, c.ParentHash, c.ParentRoot} {
		if !digestPattern.MatchString(value) {
			return fmt.Errorf("%w: candidate hash %q", ErrInvalid, value)
		}
	}
	return nil
}

// AdmittedDigest returns the digest of an ADMITTED record.
func AdmittedDigest(key string, sequence uint64, c Candidate, pinDigest, capsuleDigest string) (string, error) {
	raw, err := hashes(c.Hash, c.ParentHash, c.ParentRoot, pinDigest, capsuleDigest)
	if err != nil {
		return "", err
	}
	return digestOf(tagged(tagAdmitted), str(key), u64(sequence), u64(c.Number),
		raw[0], raw[1], raw[2], raw[3], raw[4]), nil
}

// ConsumedDigest recomputes the consumed-manifest digest of E01-02.inputs.
func ConsumedDigest(capsuleDigest string, count uint64, chain string) (string, error) {
	raw, err := hashes(capsuleDigest, chain)
	if err != nil {
		return "", err
	}
	return digestOf(tagged(tagConsumed), raw[0], u64(count), raw[1]), nil
}

// ExecReadyDigest returns the digest of an EXEC_READY record.
func ExecReadyDigest(admittedDigest, consumedDigest, execDigest string) (string, error) {
	raw, err := hashes(admittedDigest, consumedDigest, execDigest)
	if err != nil {
		return "", err
	}
	return digestOf(tagged(tagExecReady), raw[0], raw[1], raw[2]), nil
}

// DecidedDigest returns the digest of the close of an EXEC_READY observation.
func DecidedDigest(execReadyDigest, decisionDigest string) (string, error) {
	raw, err := hashes(execReadyDigest, decisionDigest)
	if err != nil {
		return "", err
	}
	return digestOf(tagged(tagDecided), raw[0], raw[1]), nil
}

// RejectedDigest returns the digest of a NATIVE_REJECTED record.
func RejectedDigest(admittedDigest, evidenceDigest string) (string, error) {
	raw, err := hashes(admittedDigest, evidenceDigest)
	if err != nil {
		return "", err
	}
	return digestOf(tagged(tagRejected), raw[0], raw[1]), nil
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

func readRecord(db Store, key []byte, value any) (bool, error) {
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

func putRecord(db Store, key []byte, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return db.Put(key, data)
}

// Admission records the observations of one producer run. It is not safe
// for concurrent use; every call needs the held guard token.
type Admission struct {
	db       Store
	deleter  DeleteStore
	cleanup  InputPurger
	guard    *Guard
	producer Producer
	owner    string
	base     string
	pins     Pins
	inputs   Inputs
	mode     Mode
	next     uint64
	lowwater uint64
	purged   uint64
	open     map[uint64]*Observation
	err      error
}

// recordKinds are the per-observation record names, in protocol order.
var recordKinds = []string{"preparing", "admitted", "exec-ready", "abandoned", "rejected", "decided"}

// pruneBatch bounds the sequences whose records one sync deletes.
const pruneBatch = 256

// ObservationKey returns the observation key of a sequence. It is also the
// pin ID and the source_inputs ID of the observation.
func ObservationKey(p Producer, sequence uint64) string {
	return fmt.Sprintf("%s.%s.%016x", p.ProducerID, p.RunID, sequence)
}

func (a *Admission) key(suffix string) []byte { return []byte(a.base + suffix) }

func (a *Admission) recordKey(seq uint64, kind string) []byte {
	return a.key(fmt.Sprintf("%016x/%s", seq, kind))
}

func (a *Admission) observationKey(seq uint64) string { return fmt.Sprintf("%s.%016x", a.owner, seq) }

// Open syncs the store, reads every observation above the low-water mark and
// checks its records. It releases the pin of every closed observation there
// again, which finishes an interrupted close, and advances the low-water
// mark. It starts in RECOVERY_ONLY. An Admission from Open deletes no
// records; see OpenWithCleanup.
func Open(t *Token, db Store, p Producer, pins Pins, inputs Inputs) (*Admission, error) {
	return open(t, db, nil, p, pins, inputs, nil)
}

// OpenWithCleanup is Open for an Admission that also deletes the records of
// closed observations (D100). After the pin of a closed observation is
// released, cleanup.Purge deletes its capsule and dependency log; after the
// low-water mark passes it, its own records under this package are deleted.
// Open repeats both for closes that a power loss interrupted.
func OpenWithCleanup(t *Token, db DeleteStore, p Producer, pins Pins, inputs Inputs, cleanup InputPurger) (*Admission, error) {
	if db == nil || cleanup == nil {
		return nil, fmt.Errorf("%w: missing store or cleanup", ErrInvalid)
	}
	return open(t, db, db, p, pins, inputs, cleanup)
}

func open(t *Token, db Store, deleter DeleteStore, p Producer, pins Pins, inputs Inputs, cleanup InputPurger) (*Admission, error) {
	if t == nil || t.guard == nil {
		return nil, ErrGuard
	}
	if err := t.guard.check(t); err != nil {
		return nil, err
	}
	if db == nil || pins == nil || inputs == nil {
		return nil, fmt.Errorf("%w: missing store, pins or inputs", ErrInvalid)
	}
	if !partPattern.MatchString(p.ProducerID) || !partPattern.MatchString(p.RunID) {
		return nil, fmt.Errorf("%w: producer %q run %q", ErrInvalid, p.ProducerID, p.RunID)
	}
	// After a process crash, unsynced writes may be visible. Sync them first,
	// so nothing read below can be lost later.
	if err := db.SyncKeyValue(); err != nil {
		return nil, err
	}
	owner := p.ProducerID + "." + p.RunID
	a := &Admission{db: db, deleter: deleter, cleanup: cleanup, guard: t.guard, producer: p, owner: owner,
		base: keyPrefix + owner + "/", pins: pins, inputs: inputs, mode: RecoveryOnly, open: map[uint64]*Observation{}}
	var counter counterRecord
	if _, err := readRecord(db, a.key("counter"), &counter); err != nil {
		return nil, err
	}
	var low lowwaterRecord
	if _, err := readRecord(db, a.key("lowwater"), &low); err != nil {
		return nil, err
	}
	if low.Sequence > counter.Next {
		return nil, fmt.Errorf("%w: low-water %d above counter %d", ErrCorrupt, low.Sequence, counter.Next)
	}
	var purged purgedRecord
	if _, err := readRecord(db, a.key("purged"), &purged); err != nil {
		return nil, err
	}
	if purged.Sequence > low.Sequence {
		return nil, fmt.Errorf("%w: purged mark %d above low-water %d", ErrCorrupt, purged.Sequence, low.Sequence)
	}
	a.next, a.lowwater, a.purged = counter.Next, low.Sequence, purged.Sequence
	var closed []*Observation
	for seq := low.Sequence; seq < counter.Next; seq++ {
		obs, err := a.load(seq)
		if err != nil {
			return nil, err
		}
		if obs.State == Abandoned || obs.State == Rejected || obs.State == Decided {
			closed = append(closed, obs)
		} else {
			a.open[seq] = obs
		}
	}
	// Finish closes that a power loss or a release error interrupted after
	// the terminal record was synced: release the pin again, delete the
	// inputs again, then advance the low-water mark and delete the records
	// below it.
	for _, obs := range closed {
		if err := a.release(obs); err != nil {
			return nil, err
		}
	}
	if err := a.advance(); err != nil {
		return nil, err
	}
	if err := a.prune(); err != nil {
		return nil, err
	}
	return a, nil
}

// load reads and checks the records of one existing sequence.
func (a *Admission) load(seq uint64) (*Observation, error) {
	key := a.observationKey(seq)
	var prep Preparing
	ok, err := readRecord(a.db, a.recordKey(seq, "preparing"), &prep)
	if err != nil {
		return nil, err
	}
	if !ok || prep.Observation != key || prep.Sequence != seq || validCandidate(prep.Candidate) != nil {
		return nil, fmt.Errorf("%w: INPUT_PREPARING record of %s", ErrCorrupt, key)
	}
	obs := &Observation{Key: key, Sequence: seq, State: InputPreparing, Candidate: prep.Candidate}
	var admitted AdmittedRecord
	hasAdmitted, err := readRecord(a.db, a.recordKey(seq, "admitted"), &admitted)
	if err != nil {
		return nil, err
	}
	var abandoned abandonedRecord
	hasAbandoned, err := readRecord(a.db, a.recordKey(seq, "abandoned"), &abandoned)
	if err != nil {
		return nil, err
	}
	var ready ExecReadyRecord
	hasReady, err := readRecord(a.db, a.recordKey(seq, "exec-ready"), &ready)
	if err != nil {
		return nil, err
	}
	var rejected RejectedRecord
	hasRejected, err := readRecord(a.db, a.recordKey(seq, "rejected"), &rejected)
	if err != nil {
		return nil, err
	}
	var decided DecidedRecord
	hasDecided, err := readRecord(a.db, a.recordKey(seq, "decided"), &decided)
	if err != nil {
		return nil, err
	}
	if hasAbandoned {
		if hasAdmitted || hasReady || hasRejected || hasDecided ||
			abandoned != (abandonedRecord{Observation: key, Sequence: seq}) {
			return nil, fmt.Errorf("%w: ABANDONED record of %s", ErrCorrupt, key)
		}
		obs.State = Abandoned
		return obs, nil
	}
	if hasAdmitted {
		want, err := AdmittedDigest(key, seq, prep.Candidate, admitted.PinDigest, admitted.CapsuleDigest)
		if err != nil || admitted.Observation != key || admitted.Sequence != seq ||
			admitted.Candidate != prep.Candidate || admitted.Digest != want {
			return nil, fmt.Errorf("%w: ADMITTED record of %s", ErrCorrupt, key)
		}
		obs.State, obs.Admitted = Admitted, &admitted
	}
	if hasRejected {
		want, err := RejectedDigest(admitted.Digest, rejected.EvidenceDigest)
		if !hasAdmitted || hasReady || hasDecided || err != nil || rejected.Observation != key || rejected.Sequence != seq ||
			rejected.AdmittedDigest != admitted.Digest || rejected.Digest != want {
			return nil, fmt.Errorf("%w: NATIVE_REJECTED record of %s", ErrCorrupt, key)
		}
		obs.State = Rejected
		return obs, nil
	}
	if hasReady {
		if !hasAdmitted {
			return nil, fmt.Errorf("%w: EXEC_READY without ADMITTED for %s", ErrCorrupt, key)
		}
		want, err := ExecReadyDigest(admitted.Digest, ready.ConsumedDigest, ready.ExecDigest)
		if err != nil || ready.Observation != key || ready.Sequence != seq ||
			ready.AdmittedDigest != admitted.Digest || ready.Digest != want {
			return nil, fmt.Errorf("%w: EXEC_READY record of %s", ErrCorrupt, key)
		}
		obs.State, obs.ExecReady = ExecReady, &ready
	}
	if hasDecided {
		if !hasReady {
			return nil, fmt.Errorf("%w: DECIDED without EXEC_READY for %s", ErrCorrupt, key)
		}
		want, err := DecidedDigest(ready.Digest, decided.DecisionDigest)
		if err != nil || decided.Observation != key || decided.Sequence != seq ||
			decided.ExecReadyDigest != ready.Digest || decided.Digest != want {
			return nil, fmt.Errorf("%w: DECIDED record of %s", ErrCorrupt, key)
		}
		obs.State = Decided
	}
	return obs, nil
}

// Check returns nil if t holds the guard of this admission and the admission
// has not stopped after an error. The decision bridge, which shares the
// guard, uses it.
func (a *Admission) Check(t *Token) error { return a.enter(t) }

// Producer returns the producer run of this admission.
func (a *Admission) Producer() Producer { return a.producer }

func (a *Admission) enter(t *Token) error {
	if err := a.guard.check(t); err != nil {
		return err
	}
	if a.err != nil {
		return fmt.Errorf("%w: %v", ErrFailed, a.err)
	}
	return nil
}

func (a *Admission) fail(err error) error {
	a.err = err
	return err
}

func (a *Admission) pending() *Observation {
	var first *Observation
	for _, obs := range a.open {
		if (obs.State == InputPreparing || obs.State == Admitted) && (first == nil || obs.Sequence < first.Sequence) {
			first = obs
		}
	}
	return first
}

func (a *Admission) find(key string) (*Observation, error) {
	for _, obs := range a.open {
		if obs.Key == key {
			return obs, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknown, key)
}

func copyObservation(obs *Observation) Observation {
	out := *obs
	if obs.Admitted != nil {
		admitted := *obs.Admitted
		out.Admitted = &admitted
	}
	if obs.ExecReady != nil {
		ready := *obs.ExecReady
		out.ExecReady = &ready
	}
	return out
}

// Mode returns the current mode.
func (a *Admission) Mode(t *Token) (Mode, error) {
	if err := a.enter(t); err != nil {
		return "", err
	}
	return a.mode, nil
}

// Observations lists every non-terminal observation in sequence order.
func (a *Admission) Observations(t *Token) ([]Observation, error) {
	if err := a.enter(t); err != nil {
		return nil, err
	}
	out := []Observation{}
	for seq := a.lowwater; seq < a.next; seq++ {
		if obs, ok := a.open[seq]; ok {
			out = append(out, copyObservation(obs))
		}
	}
	return out, nil
}

// GoLive switches to LIVE. It is refused while an observation is in
// INPUT_PREPARING or ADMITTED.
func (a *Admission) GoLive(t *Token) error {
	if err := a.enter(t); err != nil {
		return err
	}
	if obs := a.pending(); obs != nil {
		return fmt.Errorf("%w: %s is %s", ErrPending, obs.Key, obs.State)
	}
	a.mode = Live
	return nil
}

// Begin allocates the next observation in INPUT_PREPARING. It writes the
// record, syncs, then writes the counter and syncs; the synced counter is the
// commit point. It is refused in RECOVERY_ONLY and while another observation
// is pending.
func (a *Admission) Begin(t *Token, c Candidate) (Observation, error) {
	if err := a.enter(t); err != nil {
		return Observation{}, err
	}
	if a.mode != Live {
		return Observation{}, fmt.Errorf("%w: Begin in %s", ErrMode, a.mode)
	}
	if err := validCandidate(c); err != nil {
		return Observation{}, err
	}
	if obs := a.pending(); obs != nil {
		return Observation{}, fmt.Errorf("%w: %s is %s", ErrPending, obs.Key, obs.State)
	}
	// Only one Admission may own a producer run. A durable counter that moved
	// means another instance admitted observations behind this one.
	var counter counterRecord
	if _, err := readRecord(a.db, a.key("counter"), &counter); err != nil {
		return Observation{}, a.fail(err)
	}
	if counter.Next != a.next {
		return Observation{}, a.fail(fmt.Errorf("%w: counter of %s is %d, this admission expects %d",
			ErrConflict, a.owner, counter.Next, a.next))
	}
	seq := a.next
	key := a.observationKey(seq)
	// A record beyond the counter can only be an orphan INPUT_PREPARING of an
	// interrupted Begin; any later record there means the store is damaged.
	for _, kind := range recordKinds[1:] {
		ok, err := a.db.Has(a.recordKey(seq, kind))
		if err != nil {
			return Observation{}, a.fail(err)
		}
		if ok {
			return Observation{}, a.fail(fmt.Errorf("%w: %s record beyond the counter for %s", ErrCorrupt, kind, key))
		}
	}
	prep := Preparing{Observation: key, Sequence: seq, Candidate: c}
	if err := putRecord(a.db, a.recordKey(seq, "preparing"), prep); err != nil {
		return Observation{}, a.fail(err)
	}
	if err := a.db.SyncKeyValue(); err != nil {
		return Observation{}, a.fail(err)
	}
	if err := putRecord(a.db, a.key("counter"), counterRecord{Next: seq + 1}); err != nil {
		return Observation{}, a.fail(err)
	}
	if err := a.db.SyncKeyValue(); err != nil {
		return Observation{}, a.fail(err)
	}
	obs := &Observation{Key: key, Sequence: seq, State: InputPreparing, Candidate: c}
	a.next = seq + 1
	a.open[seq] = obs
	return copyObservation(obs), nil
}

// Admit writes the closing ADMITTED record after the pin and the sealed
// capsule verify, and syncs it. From then on the obligation is discoverable,
// and only then may capture hooks or native execution run. Admitting an
// ADMITTED observation again returns its record if the pin and capsule still
// match.
func (a *Admission) Admit(t *Token, key string) (AdmittedRecord, error) {
	if err := a.enter(t); err != nil {
		return AdmittedRecord{}, err
	}
	if a.mode != Live {
		return AdmittedRecord{}, fmt.Errorf("%w: Admit in %s", ErrMode, a.mode)
	}
	obs, err := a.find(key)
	if err != nil {
		return AdmittedRecord{}, err
	}
	if obs.State != InputPreparing && obs.State != Admitted {
		return AdmittedRecord{}, fmt.Errorf("%w: Admit of %s in %s", ErrState, key, obs.State)
	}
	pinDigest, err := a.pins.Verify(key, obs.Candidate.ParentRoot)
	if err != nil {
		return AdmittedRecord{}, fmt.Errorf("%w: pin of %s: %w", ErrBinding, key, err)
	}
	capsuleDigest, err := a.inputs.CapsuleDigest(key)
	if err != nil {
		return AdmittedRecord{}, fmt.Errorf("%w: capsule of %s: %w", ErrBinding, key, err)
	}
	digest, err := AdmittedDigest(key, obs.Sequence, obs.Candidate, pinDigest, capsuleDigest)
	if err != nil {
		return AdmittedRecord{}, fmt.Errorf("%w: %v", ErrBinding, err)
	}
	record := AdmittedRecord{Observation: key, Sequence: obs.Sequence, Candidate: obs.Candidate,
		PinDigest: pinDigest, CapsuleDigest: capsuleDigest, Digest: digest}
	if obs.State == Admitted {
		if *obs.Admitted != record {
			return AdmittedRecord{}, fmt.Errorf("%w: ADMITTED %s", ErrConflict, key)
		}
		return record, nil
	}
	if err := putRecord(a.db, a.recordKey(obs.Sequence, "admitted"), record); err != nil {
		return AdmittedRecord{}, a.fail(err)
	}
	if err := a.db.SyncKeyValue(); err != nil {
		return AdmittedRecord{}, a.fail(err)
	}
	obs.State, obs.Admitted = Admitted, &record
	return record, nil
}

// ExecReady writes the EXEC_READY record of an admitted observation after
// native execution closed its dependency log, and syncs it. The pin and the
// capsule must still match the ADMITTED record, and the closed consumed
// manifest must belong to this observation and capsule, with a digest that
// recomputes. execDigest is the digest of the immutable record corpus. It is
// allowed in RECOVERY_ONLY, which completes saved obligations.
func (a *Admission) ExecReady(t *Token, key, execDigest string) (ExecReadyRecord, error) {
	if err := a.enter(t); err != nil {
		return ExecReadyRecord{}, err
	}
	if !digestPattern.MatchString(execDigest) {
		return ExecReadyRecord{}, fmt.Errorf("%w: exec digest %q", ErrInvalid, execDigest)
	}
	obs, err := a.find(key)
	if err != nil {
		return ExecReadyRecord{}, err
	}
	if obs.State != Admitted && obs.State != ExecReady {
		return ExecReadyRecord{}, fmt.Errorf("%w: ExecReady of %s in %s", ErrState, key, obs.State)
	}
	admitted := obs.Admitted
	pinDigest, err := a.pins.Verify(key, admitted.Candidate.ParentRoot)
	if err != nil {
		return ExecReadyRecord{}, fmt.Errorf("%w: pin of %s: %w", ErrBinding, key, err)
	}
	if pinDigest != admitted.PinDigest {
		return ExecReadyRecord{}, fmt.Errorf("%w: pin of %s changed after admission", ErrBinding, key)
	}
	capsuleDigest, err := a.inputs.CapsuleDigest(key)
	if err != nil {
		return ExecReadyRecord{}, fmt.Errorf("%w: capsule of %s: %w", ErrBinding, key, err)
	}
	if capsuleDigest != admitted.CapsuleDigest {
		return ExecReadyRecord{}, fmt.Errorf("%w: capsule of %s changed after admission", ErrBinding, key)
	}
	consumed, err := a.inputs.Consumed(key)
	if err != nil {
		return ExecReadyRecord{}, fmt.Errorf("%w: consumed manifest of %s: %w", ErrBinding, key, err)
	}
	want, err := ConsumedDigest(admitted.CapsuleDigest, consumed.Count, consumed.Chain)
	if err != nil || consumed.Observation != key || consumed.CapsuleDigest != admitted.CapsuleDigest ||
		consumed.Digest != want {
		return ExecReadyRecord{}, fmt.Errorf("%w: consumed manifest of %s does not bind its capsule", ErrBinding, key)
	}
	digest, err := ExecReadyDigest(admitted.Digest, consumed.Digest, execDigest)
	if err != nil {
		return ExecReadyRecord{}, err
	}
	record := ExecReadyRecord{Observation: key, Sequence: obs.Sequence, AdmittedDigest: admitted.Digest,
		ConsumedDigest: consumed.Digest, ExecDigest: execDigest, Digest: digest}
	if obs.State == ExecReady {
		if *obs.ExecReady != record {
			return ExecReadyRecord{}, fmt.Errorf("%w: EXEC_READY %s", ErrConflict, key)
		}
		return record, nil
	}
	if err := putRecord(a.db, a.recordKey(obs.Sequence, "exec-ready"), record); err != nil {
		return ExecReadyRecord{}, a.fail(err)
	}
	if err := a.db.SyncKeyValue(); err != nil {
		return ExecReadyRecord{}, a.fail(err)
	}
	obs.State, obs.ExecReady = ExecReady, &record
	return record, nil
}

// Abandon cleans an INPUT_PREPARING observation: it writes the ABANDONED
// record and syncs, releases the pin, then advances the low-water mark past
// terminal sequences. An ADMITTED or later observation is never abandoned.
func (a *Admission) Abandon(t *Token, key string) error {
	if err := a.enter(t); err != nil {
		return err
	}
	obs, err := a.find(key)
	if err != nil {
		return err
	}
	if obs.State != InputPreparing {
		return fmt.Errorf("%w: Abandon of %s in %s", ErrState, key, obs.State)
	}
	return a.terminate(obs, "abandoned", abandonedRecord{Observation: key, Sequence: obs.Sequence})
}

// Reject closes an ADMITTED observation that native validation refused
// (NATIVE_REJECTED). evidenceDigest is the digest of the native rejection
// evidence. It writes the record and syncs, releases the pin, then advances
// the low-water mark. It is allowed in RECOVERY_ONLY. An EXEC_READY
// observation passed native validation and is closed by the decision bridge.
func (a *Admission) Reject(t *Token, key, evidenceDigest string) (RejectedRecord, error) {
	if err := a.enter(t); err != nil {
		return RejectedRecord{}, err
	}
	if !digestPattern.MatchString(evidenceDigest) {
		return RejectedRecord{}, fmt.Errorf("%w: evidence digest %q", ErrInvalid, evidenceDigest)
	}
	obs, err := a.find(key)
	if err != nil {
		return RejectedRecord{}, err
	}
	if obs.State != Admitted {
		return RejectedRecord{}, fmt.Errorf("%w: Reject of %s in %s", ErrState, key, obs.State)
	}
	digest, err := RejectedDigest(obs.Admitted.Digest, evidenceDigest)
	if err != nil {
		return RejectedRecord{}, err
	}
	record := RejectedRecord{Observation: key, Sequence: obs.Sequence, AdmittedDigest: obs.Admitted.Digest,
		EvidenceDigest: evidenceDigest, Digest: digest}
	if err := a.terminate(obs, "rejected", record); err != nil {
		return RejectedRecord{}, err
	}
	return record, nil
}

// Decide closes an EXEC_READY observation after the decision bridge resolved
// it (D100). decisionDigest is the digest of the bridge record that resolved
// the observation: a NATIVE_DECIDED record, or the rejection of its
// transition intent. Decide writes the DECIDED record and syncs, releases the
// pin, then advances the low-water mark. It works in either mode. Only the
// decision bridge calls it, after its own record is durable.
func (a *Admission) Decide(t *Token, key, decisionDigest string) (DecidedRecord, error) {
	if err := a.enter(t); err != nil {
		return DecidedRecord{}, err
	}
	if !digestPattern.MatchString(decisionDigest) {
		return DecidedRecord{}, fmt.Errorf("%w: decision digest %q", ErrInvalid, decisionDigest)
	}
	obs, err := a.find(key)
	if err != nil {
		return DecidedRecord{}, err
	}
	if obs.State != ExecReady {
		return DecidedRecord{}, fmt.Errorf("%w: Decide of %s in %s", ErrState, key, obs.State)
	}
	digest, err := DecidedDigest(obs.ExecReady.Digest, decisionDigest)
	if err != nil {
		return DecidedRecord{}, err
	}
	record := DecidedRecord{Observation: key, Sequence: obs.Sequence, ExecReadyDigest: obs.ExecReady.Digest,
		DecisionDigest: decisionDigest, Digest: digest}
	if err := a.terminate(obs, "decided", record); err != nil {
		return DecidedRecord{}, err
	}
	return record, nil
}

// terminate writes the terminal record of an observation and syncs it, then
// releases the pin, then advances the low-water mark. The record comes first:
// an ADMITTED obligation keeps its pin until its closing record is durable
// (NATIVE_CAPTURE_PROTOCOL). After a power loss or a release error between
// the record and the release, Open releases the pin again; Release of an
// absent pin returns nil.
func (a *Admission) terminate(obs *Observation, kind string, record any) error {
	if err := putRecord(a.db, a.recordKey(obs.Sequence, kind), record); err != nil {
		return a.fail(err)
	}
	if err := a.db.SyncKeyValue(); err != nil {
		return a.fail(err)
	}
	delete(a.open, obs.Sequence)
	if err := a.release(obs); err != nil {
		return a.fail(err)
	}
	if err := a.advance(); err != nil {
		return err
	}
	return a.prune()
}

// release releases the pin of a closed observation and then, with cleanup,
// deletes its inputs. Both return nil when nothing is left.
func (a *Admission) release(obs *Observation) error {
	if err := a.pins.Release(obs.Key); err != nil {
		return err
	}
	if a.cleanup == nil {
		return nil
	}
	return a.cleanup.Purge(obs.Key)
}

// advance moves the low-water mark past the closed sequences at its front.
// Every closed sequence above the mark has had its pin released.
func (a *Admission) advance() error {
	low := a.lowwater
	for low < a.next {
		if _, open := a.open[low]; open {
			break
		}
		low++
	}
	if low == a.lowwater {
		return nil
	}
	if err := putRecord(a.db, a.key("lowwater"), lowwaterRecord{Sequence: low}); err != nil {
		return a.fail(err)
	}
	if err := a.db.SyncKeyValue(); err != nil {
		return a.fail(err)
	}
	a.lowwater = low
	return nil
}

// prune deletes the records of the sequences below the low-water mark, which
// are never read again, in batches of pruneBatch sequences. Each batch syncs
// its deletions and then the advanced purged mark, so a power loss only
// repeats a batch. It does nothing without cleanup.
func (a *Admission) prune() error {
	for a.deleter != nil && a.purged < a.lowwater {
		end := min(a.lowwater, a.purged+pruneBatch)
		for seq := a.purged; seq < end; seq++ {
			for _, kind := range recordKinds {
				if err := a.deleter.Delete(a.recordKey(seq, kind)); err != nil {
					return a.fail(err)
				}
			}
		}
		if err := a.db.SyncKeyValue(); err != nil {
			return a.fail(err)
		}
		if err := putRecord(a.db, a.key("purged"), purgedRecord{Sequence: end}); err != nil {
			return a.fail(err)
		}
		if err := a.db.SyncKeyValue(); err != nil {
			return a.fail(err)
		}
		a.purged = end
	}
	return nil
}
