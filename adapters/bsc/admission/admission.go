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

// Observation states. Later states belong to E01-02.decision_bridge.
const (
	InputPreparing State = "INPUT_PREPARING"
	Admitted       State = "ADMITTED"
	ExecReady      State = "EXEC_READY"
	Abandoned      State = "ABANDONED"
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
	if t == nil || t.guard != g {
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
	guard    *Guard
	owner    string
	base     string
	pins     Pins
	inputs   Inputs
	mode     Mode
	next     uint64
	lowwater uint64
	open     map[uint64]*Observation
	err      error
}

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

// Open syncs the store, reads every non-terminal observation and checks its
// records. It starts in RECOVERY_ONLY.
func Open(t *Token, db Store, p Producer, pins Pins, inputs Inputs) (*Admission, error) {
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
	a := &Admission{db: db, guard: t.guard, owner: owner, base: keyPrefix + owner + "/",
		pins: pins, inputs: inputs, mode: RecoveryOnly, open: map[uint64]*Observation{}}
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
	a.next, a.lowwater = counter.Next, low.Sequence
	for seq := low.Sequence; seq < counter.Next; seq++ {
		obs, err := a.load(seq)
		if err != nil {
			return nil, err
		}
		if obs.State != Abandoned {
			a.open[seq] = obs
		}
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
	if hasAbandoned {
		if hasAdmitted || hasReady || abandoned != (abandonedRecord{Observation: key, Sequence: seq}) {
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
	return obs, nil
}

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
func (a *Admission) Mode() Mode { return a.mode }

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
	seq := a.next
	key := a.observationKey(seq)
	// A record beyond the counter can only be an orphan INPUT_PREPARING of an
	// interrupted Begin; any later record there means the store is damaged.
	for _, kind := range []string{"admitted", "exec-ready", "abandoned"} {
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
		return AdmittedRecord{}, fmt.Errorf("%w: pin of %s: %v", ErrBinding, key, err)
	}
	capsuleDigest, err := a.inputs.CapsuleDigest(key)
	if err != nil {
		return AdmittedRecord{}, fmt.Errorf("%w: capsule of %s: %v", ErrBinding, key, err)
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
		return ExecReadyRecord{}, fmt.Errorf("%w: pin of %s: %v", ErrBinding, key, err)
	}
	if pinDigest != admitted.PinDigest {
		return ExecReadyRecord{}, fmt.Errorf("%w: pin of %s changed after admission", ErrBinding, key)
	}
	capsuleDigest, err := a.inputs.CapsuleDigest(key)
	if err != nil {
		return ExecReadyRecord{}, fmt.Errorf("%w: capsule of %s: %v", ErrBinding, key, err)
	}
	if capsuleDigest != admitted.CapsuleDigest {
		return ExecReadyRecord{}, fmt.Errorf("%w: capsule of %s changed after admission", ErrBinding, key)
	}
	consumed, err := a.inputs.Consumed(key)
	if err != nil {
		return ExecReadyRecord{}, fmt.Errorf("%w: consumed manifest of %s: %v", ErrBinding, key, err)
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

// Abandon cleans an INPUT_PREPARING observation: it releases the pin, writes
// the ABANDONED record and syncs, then advances the low-water mark past
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
	if err := a.pins.Release(key); err != nil {
		return a.fail(err)
	}
	if err := putRecord(a.db, a.recordKey(obs.Sequence, "abandoned"),
		abandonedRecord{Observation: key, Sequence: obs.Sequence}); err != nil {
		return a.fail(err)
	}
	if err := a.db.SyncKeyValue(); err != nil {
		return a.fail(err)
	}
	delete(a.open, obs.Sequence)
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
