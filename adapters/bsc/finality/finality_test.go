package finality

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	recoverygc "github.com/sigaev-pro/bc-indexer/adapters/recovery_gc"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"

	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
)

// vectorFile holds the independent expected values. `python3
// test/harness/bsc_finality.py vectors` computes them from the README
// formulas; test/harness/test_bsc_finality.py checks that the file matches.
type vectorFile struct {
	StoreID        string        `json:"store_id"`
	Producer       string        `json:"producer"`
	Run            string        `json:"run"`
	Genesis        decision.Head `json:"genesis"`
	SnapshotHead   decision.Head `json:"snapshot_head"`
	Justified      decision.Head `json:"justified"`
	Finalized      decision.Head `json:"finalized"`
	FallbackHead   decision.Head `json:"fallback_head"`
	AttestedDigest string        `json:"attested_digest"`
	FallbackDigest string        `json:"fallback_digest"`
	Early          struct {
		Origin     string        `json:"origin"`
		Evaluation decision.Head `json:"evaluation"`
		Source     decision.Head `json:"source"`
		Target     decision.Head `json:"target"`
		Candidate  decision.Head `json:"candidate"`
		Validators decision.Head `json:"validators"`
		Count      uint64        `json:"count"`
		Quorum     uint64        `json:"quorum"`
		Matching   uint64        `json:"matching"`
		Votes      string        `json:"votes"`
	} `json:"early"`
	EarlyDigest string `json:"early_digest"`
	Sequence    uint64 `json:"sequence"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	data, err := os.ReadFile("vectors.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v vectorFile
	if err := strictDecode(data, &v); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	return v
}

var errCrash = errors.New("injected power loss")

type write struct {
	key     string
	value   []byte
	deleted bool
}

// memStore models the native key-value store. Put and Delete stay pending
// until SyncKeyValue makes them durable; powerLoss drops every pending
// write.
type memStore struct {
	durable map[string][]byte
	pending []write
	ops     int
	crashAt int
	syncErr error
	syncs   int
}

func newStore() *memStore { return &memStore{durable: map[string][]byte{}} }

func (s *memStore) view(key string) ([]byte, bool) {
	for i := len(s.pending) - 1; i >= 0; i-- {
		if s.pending[i].key == key {
			return s.pending[i].value, !s.pending[i].deleted
		}
	}
	value, ok := s.durable[key]
	return value, ok
}

func (s *memStore) Has(key []byte) (bool, error) {
	_, ok := s.view(string(key))
	return ok, nil
}

func (s *memStore) Get(key []byte) ([]byte, error) {
	value, ok := s.view(string(key))
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return bytes.Clone(value), nil
}

func (s *memStore) Put(key, value []byte) error {
	s.ops++
	if s.crashAt != 0 && s.ops >= s.crashAt {
		return errCrash
	}
	s.pending = append(s.pending, write{key: string(key), value: bytes.Clone(value)})
	return nil
}

func (s *memStore) Delete(key []byte) error {
	s.ops++
	if s.crashAt != 0 && s.ops >= s.crashAt {
		return errCrash
	}
	s.pending = append(s.pending, write{key: string(key), deleted: true})
	return nil
}

func (s *memStore) SyncKeyValue() error {
	if s.syncErr != nil {
		return s.syncErr
	}
	s.ops++
	if s.crashAt != 0 && s.ops >= s.crashAt {
		return errCrash
	}
	for _, w := range s.pending {
		if w.deleted {
			delete(s.durable, w.key)
		} else {
			s.durable[w.key] = w.value
		}
	}
	s.pending = nil
	s.syncs++
	return nil
}

// Range visits every visible key that starts with prefix, in ascending
// byte order, with the value visible through unsynced writes; a pending
// delete hides the key. The recovery GC engine reads its records through
// it.
func (s *memStore) Range(prefix []byte, visit func(key, value []byte) error) error {
	view := map[string][]byte{}
	for key, value := range s.durable {
		if strings.HasPrefix(key, string(prefix)) {
			view[key] = value
		}
	}
	for _, w := range s.pending {
		if !strings.HasPrefix(w.key, string(prefix)) {
			continue
		}
		if w.deleted {
			delete(view, w.key)
		} else {
			view[w.key] = w.value
		}
	}
	for _, key := range slices.Sorted(maps.Keys(view)) {
		if err := visit([]byte(key), view[key]); err != nil {
			return err
		}
	}
	return nil
}

// powerLoss drops every pending write and returns the reopened store.
func (s *memStore) powerLoss() *memStore {
	out := &memStore{durable: map[string][]byte{}}
	for key, value := range s.durable {
		out.durable[key] = value
	}
	return out
}

// chain is an in-memory canonical chain oracle.
type chain struct {
	numbers map[string]uint64
	parents map[string]string
}

func newChain() *chain { return &chain{numbers: map[string]uint64{}, parents: map[string]string{}} }

func (c *chain) add(number uint64, hash, parent string) *chain {
	c.numbers[hash] = number
	c.parents[hash] = parent
	return c
}

func (c *chain) IsAncestorOrSelf(block, anchor decision.Head) (bool, error) {
	current := anchor
	for {
		if current.Hash == block.Hash {
			return current.Number == block.Number, nil
		}
		parent, ok := c.parents[current.Hash]
		if !ok {
			// The root of the walk: the chain above it is unknown, and block
			// was not found below it.
			return false, nil
		}
		current = decision.Head{Number: c.numbers[parent], Hash: parent}
	}
}

// errChain fails every ancestry lookup.
type errChain struct{ err error }

func (c errChain) IsAncestorOrSelf(decision.Head, decision.Head) (bool, error) { return false, c.err }

// The semantic identity of the fixture run: the network and the ID16
// identities of the GC anchor contract.
var (
	gcNetwork  = semantic.Network{ChainID: semantic.UInt256{1}, GenesisHash: seedHash32("bsc-gc-genesis")}
	gcNode     = semantic.ID16{0x11}
	gcProducer = semantic.ID16{0x22}
	gcRun      = semantic.ID16{0x33}
)

// seedHash32 derives a fixed Hash32 of a seed: the seed bytes in a 32-byte
// value.
func seedHash32(seed string) semantic.Hash32 {
	var out semantic.Hash32
	copy(out[:], []byte(seed))
	return out
}

// gcOracle adapts a test chain into the ancestry oracle of the recovery GC
// engine: a nil chain has unknown ancestry, and a lookup failure is
// unknown evidence, never a guess.
type gcOracle struct{ chain *chain }

func (o gcOracle) Contains(ancestor, descendant recoverygc.AnchorRef) recoverygc.Verdict {
	if o.chain == nil {
		return recoverygc.VerdictUnknown
	}
	ok, err := o.chain.IsAncestorOrSelf(
		decision.Head{Number: ancestor.Height, Hash: ancestor.Hash.String()},
		decision.Head{Number: descendant.Height, Hash: descendant.Hash.String()})
	if err != nil {
		return recoverygc.VerdictUnknown
	}
	if ok {
		return recoverygc.VerdictContains
	}
	return recoverygc.VerdictDivergent
}

// The inert GC seams of the fixture: these tests release nothing.
type gcNothingPending struct{}

func (gcNothingPending) Pending(recoverygc.EntityID) (recoverygc.PendingReport, error) {
	return recoverygc.PendingReport{}, nil
}

type gcReleaser struct{}

func (gcReleaser) ReleaseEntity(recoverygc.EntityID) error { return nil }

type gcPins struct{}

func (gcPins) Release(string) error { return nil }

// openEngine opens the recovery GC engine of the fixture over db with the
// given chain as its ancestry oracle.
func openEngine(t *testing.T, db *memStore, anc *chain) *recoverygc.Engine {
	t.Helper()
	engine, _, err := recoverygc.Open(db, gcOracle{chain: anc}, gcNothingPending{}, gcReleaser{}, gcPins{})
	if err != nil {
		t.Fatalf("recoverygc.Open: %v", err)
	}
	return engine
}

// anchorHead converts the anchor block of the shared GC record into the
// head model of the tests.
func anchorHead(t *testing.T, a recoverygc.AcceptedGcAnchor) decision.Head {
	t.Helper()
	h, err := headOfAnchor(a.Anchor)
	if err != nil {
		t.Fatalf("anchor head: %v", err)
	}
	return h
}

// headOfAnchorUnchecked names the anchor block without the error path of
// a validated record.
func headOfAnchorUnchecked(a recoverygc.AcceptedGcAnchor) decision.Head {
	return decision.Head{Number: a.Anchor.Height, Hash: a.Anchor.Hash.String()}
}

// fixture is one producer run with its store, admission and observatory.
type fixture struct {
	v     vectorFile
	db    *memStore
	token *admission.Token
	adm   *admission.Admission
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	v := loadVectors(t)
	db := newStore()
	token := admission.NewGuard().Acquire()
	adm, err := admission.Open(token, db, admission.Producer{ProducerID: v.Producer, RunID: v.Run}, noPins{},
		noInputs{})
	if err != nil {
		t.Fatalf("admission.Open: %v", err)
	}
	return &fixture{v: v, db: db, token: token, adm: adm}
}

// open opens the observatory over db with the recovery GC engine of the
// run; anc is the chain the engine proves ancestry on (nil: unknown).
func (f *fixture) open(t *testing.T, db *memStore, anc *chain) *Observatory {
	t.Helper()
	o, err := Open(f.token, db, f.adm, f.v.StoreID,
		RunIdentity{Network: gcNetwork, Node: gcNode, Producer: gcProducer, Run: gcRun},
		openEngine(t, db, anc))
	if err != nil {
		t.Fatalf("observatory open: %v", err)
	}
	return o
}

// noPins and noInputs fail every call: no observation is admitted here.
type noPins struct{}

func (noPins) Verify(string, string) (string, error) { return "", errors.New("no pins") }
func (noPins) Release(string) error                  { return errors.New("no pins") }

type noInputs struct{}

func (noInputs) CapsuleDigest(string) (string, error) { return "", errors.New("no inputs") }
func (noInputs) Consumed(string) (admission.Consumed, error) {
	return admission.Consumed{}, errors.New("no inputs")
}

func hash(seed string) string {
	var out strings.Builder
	for _, b := range []byte(seed) {
		fmt.Fprintf(&out, "%02x", b)
	}
	return (out.String() + strings.Repeat("0", 64))[:63] + "1"
}

func head(number uint64, seed string) decision.Head {
	return decision.Head{Number: number, Hash: hash(seed)}
}

func mustErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error %v, want %v", err, want)
	}
}

// canonicalChain builds the chain of the tests: genesis G, the canonical
// branch A1..A7 and the side branch S3..S6 forking at A2.
func canonicalChain() (heads map[string]decision.Head, anc *chain) {
	heads = map[string]decision.Head{"G": head(0, "genesis")}
	anc = newChain().add(0, heads["G"].Hash, "")
	parent := heads["G"].Hash
	for i := 1; i <= 7; i++ {
		name := fmt.Sprintf("A%d", i)
		heads[name] = head(uint64(i), name)
		anc.add(uint64(i), heads[name].Hash, parent)
		parent = heads[name].Hash
	}
	parent = heads["A2"].Hash
	for i := 3; i <= 6; i++ {
		name := fmt.Sprintf("S%d", i)
		heads[name] = head(uint64(i), name)
		anc.add(uint64(i), heads[name].Hash, parent)
		parent = heads[name].Hash
	}
	return heads, anc
}

// attestedEvidence builds a valid evidence over the attested snapshot
// (justified, finalized) observed at execution head, with the effective
// finalized equal to the snapshot source.
func attestedEvidence(execHead decision.Head, hf HeaderFinality) Evidence {
	return Evidence{ObservedExecutionHead: execHead, ObservedNativeHeaderHead: hf.SnapshotHead,
		HeaderFinality: hf, NativeEffectiveFinalized: KnownReference(hf.Finalized, NativeLive),
		EffectiveBasis: BasisHeaderSnapshot}
}

func TestVectors(t *testing.T) {
	v := loadVectors(t)
	attested := AttestedSnapshot(v.SnapshotHead, v.Justified, v.Finalized)
	if got, err := HeaderDigest(attested); err != nil || got != v.AttestedDigest {
		t.Fatalf("attested digest %s %v, want %s", got, err, v.AttestedDigest)
	}
	fallback := GenesisFallback(v.FallbackHead, v.Genesis, NoAttestation)
	if got, err := HeaderDigest(fallback); err != nil || got != v.FallbackDigest {
		t.Fatalf("fallback digest %s %v, want %s", got, err, v.FallbackDigest)
	}
	early := EarlyVoteEvidence{Origin: Origin(v.Early.Origin), EvaluationHead: v.Early.Evaluation,
		SourceJustified: v.Early.Source, VoteTarget: v.Early.Target, FinalizedCandidate: v.Early.Candidate,
		ValidatorSnapshotParent: v.Early.Validators, ValidatorCount: v.Early.Count, QuorumCount: v.Early.Quorum,
		MatchingVoteCount: v.Early.Matching, EvidenceDigest: v.EarlyDigest}
	if got, err := EarlyDigest(early, []byte(v.Early.Votes)); err != nil || got != v.EarlyDigest {
		t.Fatalf("early digest %s %v, want %s", got, err, v.EarlyDigest)
	}
	// The accepted anchor of the recovery GC engine: the shared
	// AcceptedGcAnchor record, checked through the observatory.
	heads, anc := canonicalChain()
	f := newFixture(t)
	o := f.open(t, f.db, anc)
	execution := head(9, "execution-9")
	snapshot := head(9, "execution-9")
	anc.add(9, execution.Hash, heads["A7"].Hash)
	evidence := attestedEvidence(execution, AttestedSnapshot(snapshot, heads["A6"], heads["A5"]))
	if _, err := o.ObserveHeader(f.token, evidence, anc, v.Sequence); err != nil {
		t.Fatalf("observe: %v", err)
	}
	stored, ok := o.Anchor(f.token)
	if !ok {
		t.Fatal("the observation accepted no anchor")
	}
	if err := stored.Validate(); err != nil {
		t.Fatalf("the accepted anchor record is invalid: %v", err)
	}
	if headOfAnchorUnchecked(stored) != heads["A5"] || stored.Decision.DecisionSequence != v.Sequence {
		t.Fatalf("anchor record %+v", stored)
	}
	if stored.EvidenceDigest == "" || stored.RecordDigest == "" {
		t.Fatalf("anchor record binds no evidence: %+v", stored)
	}
}

func TestEarlyValidation(t *testing.T) {
	v := loadVectors(t)
	valid := func() EarlyVoteEvidence {
		return EarlyVoteEvidence{Origin: NativeLive, EvaluationHead: v.Early.Evaluation,
			SourceJustified: v.Early.Source, VoteTarget: v.Early.Target,
			FinalizedCandidate: v.Early.Candidate, ValidatorSnapshotParent: v.Early.Validators,
			ValidatorCount: v.Early.Count, QuorumCount: v.Early.Quorum, MatchingVoteCount: v.Early.Matching}
	}
	cases := []struct {
		name   string
		change func(*EarlyVoteEvidence)
	}{
		{"quorum below two thirds", func(e *EarlyVoteEvidence) { e.QuorumCount = 13 }},
		{"quorum above two thirds", func(e *EarlyVoteEvidence) { e.QuorumCount = 15 }},
		{"matching below quorum", func(e *EarlyVoteEvidence) { e.MatchingVoteCount = e.QuorumCount - 1 }},
		{"matching above validators", func(e *EarlyVoteEvidence) { e.MatchingVoteCount = 22 }},
		{"empty validator set", func(e *EarlyVoteEvidence) { e.ValidatorCount, e.QuorumCount = 0, 0 }},
		{"candidate above source", func(e *EarlyVoteEvidence) { e.SourceJustified = head(1, "x") }},
		{"target above evaluation", func(e *EarlyVoteEvidence) { e.VoteTarget.Number = 10 }},
		{"zero hash", func(e *EarlyVoteEvidence) { e.VoteTarget.Hash = lifecycle.ZeroHash }},
		{"unknown origin", func(e *EarlyVoteEvidence) { e.Origin = "NATIVE_GUESS" }},
	}
	for _, c := range cases {
		e := valid()
		c.change(&e)
		if err := e.valid(); err == nil {
			t.Fatalf("%s: accepted %+v", c.name, e)
		}
		if _, err := EarlyDigest(e, []byte("votes")); err == nil {
			t.Fatalf("%s: digest accepted", c.name)
		}
	}
	if err := valid().valid(); err != nil {
		t.Fatalf("valid early evidence: %v", err)
	}
	// A retained origin is a valid shape (a stored record after a restart);
	// only ObserveEarly refuses it as a fresh observation.
	retained := valid()
	retained.Origin = RetainedDurable
	if err := retained.valid(); err != nil {
		t.Fatalf("retained shape: %v", err)
	}
}

func TestEvidenceBasisValidation(t *testing.T) {
	heads, _ := canonicalChain()
	hf := AttestedSnapshot(heads["A7"], heads["A6"], heads["A5"])
	early := &EarlyVoteEvidence{Origin: NativeLive, EvaluationHead: heads["A7"],
		SourceJustified: heads["A6"], VoteTarget: heads["A7"], FinalizedCandidate: heads["A5"],
		ValidatorSnapshotParent: heads["A6"], ValidatorCount: 4, QuorumCount: quorumOf(4),
		MatchingVoteCount: quorumOf(4)}
	cases := []struct {
		name string
		e    Evidence
	}{
		{"known reference without basis", Evidence{ObservedExecutionHead: heads["A7"],
			ObservedNativeHeaderHead: heads["A7"], HeaderFinality: hf,
			NativeEffectiveFinalized: KnownReference(heads["A5"], NativeLive)}},
		{"basis unset with known reference", Evidence{ObservedExecutionHead: heads["A7"],
			ObservedNativeHeaderHead: heads["A7"], HeaderFinality: hf,
			NativeEffectiveFinalized: KnownReference(heads["A5"], NativeLive), EffectiveBasis: BasisUnset}},
		{"header basis without attestation", Evidence{ObservedExecutionHead: heads["A3"],
			ObservedNativeHeaderHead: heads["A3"], HeaderFinality: GenesisFallback(heads["A3"], heads["G"],
				NoAttestation), NativeEffectiveFinalized: KnownReference(heads["A5"], NativeLive),
			EffectiveBasis: BasisHeaderSnapshot}},
		{"header basis with vote result", Evidence{ObservedExecutionHead: heads["A7"],
			ObservedNativeHeaderHead: heads["A7"], HeaderFinality: hf,
			NativeEffectiveFinalized: KnownReference(heads["A6"], NativeLive),
			EffectiveBasis:           BasisHeaderSnapshot}},
		{"genesis basis without fallback", Evidence{ObservedExecutionHead: heads["A7"],
			ObservedNativeHeaderHead: heads["A7"], HeaderFinality: hf,
			NativeEffectiveFinalized: KnownReference(heads["G"], NativeLive),
			EffectiveBasis:           BasisGenesisFallback}},
		{"vote basis without early evidence", Evidence{ObservedExecutionHead: heads["A7"],
			ObservedNativeHeaderHead: heads["A7"], HeaderFinality: hf,
			NativeEffectiveFinalized: KnownReference(heads["A6"], NativeLive),
			EffectiveBasis:           BasisVotePoolQuorum}},
		{"vote basis with other candidate", Evidence{ObservedExecutionHead: heads["A7"],
			ObservedNativeHeaderHead: heads["A7"], HeaderFinality: hf,
			NativeEffectiveFinalized: KnownReference(heads["A6"], NativeLive),
			EffectiveBasis:           BasisVotePoolQuorum, Early: early}},
		{"unknown basis", Evidence{ObservedExecutionHead: heads["A7"], ObservedNativeHeaderHead: heads["A7"],
			HeaderFinality: hf, NativeEffectiveFinalized: KnownReference(heads["A5"], NativeLive),
			EffectiveBasis: "MAYBE"}},
	}
	for _, c := range cases {
		if _, _, err := Project(c.e, heads["A1"], &errChain{}); err == nil {
			t.Fatalf("%s: accepted", c.name)
		}
	}
	vote := Evidence{ObservedExecutionHead: heads["A7"], ObservedNativeHeaderHead: heads["A7"],
		HeaderFinality: hf, NativeEffectiveFinalized: KnownReference(heads["A5"], NativeLive),
		EffectiveBasis: BasisVotePoolQuorum, Early: early}
	if err := vote.valid(); err != nil {
		t.Fatalf("vote evidence: %v", err)
	}
	unknown := Evidence{ObservedExecutionHead: heads["A7"], ObservedNativeHeaderHead: heads["A7"],
		HeaderFinality: hf, NativeEffectiveFinalized: UnknownReference(NotObserved), EffectiveBasis: BasisUnset}
	if err := unknown.valid(); err != nil {
		t.Fatalf("unknown effective reference: %v", err)
	}
}

func TestProjectSeparateStatuses(t *testing.T) {
	heads, anc := canonicalChain()
	execution := heads["A7"]
	attested := attestedEvidence(execution, AttestedSnapshot(heads["A7"], heads["A6"], heads["A5"]))
	cases := []struct {
		name       string
		e          Evidence
		activation string
		header     HeaderStatus
		early      EarlyStatus
	}{
		{"finalized source covers", attested, "A3", FinalizedHeaderAttested, EarlyNotObserved},
		{"justified covers, source does not", attested, "A6", Justified, EarlyNotObserved},
		{"head itself unfinalized", attested, "A7", CanonicalUnfinalized, EarlyNotObserved},
		{"genesis activation", attested, "G", FinalizedHeaderAttested, EarlyNotObserved},
	}
	for _, c := range cases {
		h, e, err := Project(c.e, heads[c.activation], anc)
		if err != nil || h != c.header || e != c.early {
			t.Fatalf("%s: %s %s %v, want %s %s", c.name, h, e, err, c.header, c.early)
		}
	}
	// An early quorum above the snapshot source keeps the two statuses
	// separate: the activation is early-finalized but not header-attested.
	early := &EarlyVoteEvidence{Origin: NativeLive, EvaluationHead: heads["A7"],
		SourceJustified: heads["A6"], VoteTarget: heads["A7"], FinalizedCandidate: heads["A6"],
		ValidatorSnapshotParent: heads["A6"], ValidatorCount: 4, QuorumCount: quorumOf(4),
		MatchingVoteCount: quorumOf(4)}
	withEarly := attested
	withEarly.Early = early
	h, e, err := Project(withEarly, heads["A6"], anc)
	if err != nil || h != Justified || e != VoteFinalizedObserved {
		t.Fatalf("early above source: %s %s %v, want %s %s", h, e, err, Justified, VoteFinalizedObserved)
	}
	h, e, err = Project(withEarly, heads["A7"], anc)
	if err != nil || h != CanonicalUnfinalized || e != NotCovered {
		t.Fatalf("early not covering: %s %s %v", h, e, err)
	}
	// A side-branch early observation covers nothing: its finalized
	// candidate is not on the canonical execution ancestry, even though the
	// activation is an ancestor of the fork point below it.
	sideEarly := *early
	sideEarly.FinalizedCandidate = heads["S6"]
	sideEarly.VoteTarget = heads["S6"]
	sideEarly.EvaluationHead = heads["S6"]
	withSide := attested
	withSide.Early = &sideEarly
	h, e, err = Project(withSide, heads["A5"], anc)
	if err != nil || h != FinalizedHeaderAttested || e != NotCovered {
		t.Fatalf("side-branch early: %s %s %v, want early %s", h, e, err, NotCovered)
	}
	// A snapshot whose finalized block is on the side branch finalizes
	// nothing for a canonical activation.
	sideHeader := attestedEvidence(heads["S6"], AttestedSnapshot(heads["S6"], heads["S5"], heads["S4"]))
	h, e, err = Project(sideHeader, heads["A3"], anc)
	if err != nil || h != CanonicalUnfinalized || e != EarlyNotObserved {
		t.Fatalf("side-branch header: %s %s %v, want %s", h, e, err, CanonicalUnfinalized)
	}
	// The genesis fallback: an activation above genesis has no attested
	// finality; genesis itself is the fallback's finalized reference.
	fallback := Evidence{ObservedExecutionHead: heads["A3"], ObservedNativeHeaderHead: heads["A3"],
		HeaderFinality:           GenesisFallback(heads["A3"], heads["G"], NoAttestation),
		NativeEffectiveFinalized: KnownReference(heads["G"], NativeLive),
		EffectiveBasis:           BasisGenesisFallback}
	h, e, err = Project(fallback, heads["A1"], anc)
	if err != nil || h != NoAttestedFinality || e != EarlyNotObserved {
		t.Fatalf("fallback above genesis: %s %s %v, want %s", h, e, err, NoAttestedFinality)
	}
	h, e, err = Project(fallback, heads["G"], anc)
	if err != nil || h != FinalizedHeaderAttested || e != EarlyNotObserved {
		t.Fatalf("fallback genesis: %s %s %v, want %s", h, e, err, FinalizedHeaderAttested)
	}
	// An ancestry lookup error is a source error, never a status.
	if _, _, err := Project(attested, heads["A3"], errChain{err: errCrash}); !errors.Is(err, ErrSource) {
		t.Fatalf("ancestry error: %v", err)
	}
}

func TestObservatoryAcceptsOnlyHeaderSource(t *testing.T) {
	heads, anc := canonicalChain()
	f := newFixture(t)
	o := f.open(t, f.db, anc)
	// A genesis fallback gives no anchor.
	fallback := Evidence{ObservedExecutionHead: heads["A3"], ObservedNativeHeaderHead: heads["A3"],
		HeaderFinality:           GenesisFallback(heads["A3"], heads["G"], NoAttestation),
		NativeEffectiveFinalized: KnownReference(heads["G"], NativeLive),
		EffectiveBasis:           BasisGenesisFallback}
	result, err := o.ObserveHeader(f.token, fallback, anc, 1)
	if err != nil || result.Change != Refused {
		t.Fatalf("fallback: %+v %v", result, err)
	}
	if _, ok := o.Anchor(f.token); ok {
		t.Fatal("a genesis fallback accepted an anchor")
	}
	// The native effective finalized result with a VotePool quorum basis is
	// never an anchor: only the attested snapshot source is.
	early := EarlyVoteEvidence{Origin: NativeLive, EvaluationHead: heads["A7"],
		SourceJustified: heads["A6"], VoteTarget: heads["A7"], FinalizedCandidate: heads["A5"],
		ValidatorSnapshotParent: heads["A6"], ValidatorCount: 4, QuorumCount: quorumOf(4),
		MatchingVoteCount: quorumOf(4)}
	early.EvidenceDigest, err = EarlyDigest(early, []byte("votes A5"))
	if err != nil {
		t.Fatal(err)
	}
	if err := o.ObserveEarly(f.token, early, []byte("votes A5")); err != nil {
		t.Fatalf("observe early: %v", err)
	}
	voteEffective := Evidence{ObservedExecutionHead: heads["A7"], ObservedNativeHeaderHead: heads["A7"],
		HeaderFinality:           AttestedSnapshot(heads["A7"], heads["A6"], heads["A3"]),
		NativeEffectiveFinalized: KnownReference(heads["A5"], NativeLive),
		EffectiveBasis:           BasisVotePoolQuorum, Early: &early}
	result, err = o.ObserveHeader(f.token, voteEffective, anc, 2)
	if err != nil || result.Change != Accepted || result.Anchor != heads["A3"] {
		t.Fatalf("vote-effective observation: %+v %v", result, err)
	}
	// The early status covers A4 above the anchor; the header status of A4
	// is only justified (the snapshot source A3 does not cover it), and the
	// GC anchor stays at the header source A3 either way.
	h, e, err := Project(voteEffective, heads["A4"], anc)
	if err != nil || h != Justified || e != VoteFinalizedObserved {
		t.Fatalf("projection: %s %s %v, want %s %s", h, e, err, Justified, VoteFinalizedObserved)
	}
	anchor, ok := o.Anchor(f.token)
	if !ok || anchorHead(t, anchor) != heads["A3"] {
		t.Fatalf("anchor %+v %v", anchor, ok)
	}
	// A finalized block of the snapshot that is not on the canonical
	// execution ancestry gives no anchor candidate: Refused, the anchor
	// stays and this is not an error.
	sideFinalized := attestedEvidence(heads["A7"], AttestedSnapshot(heads["A7"], heads["A6"], heads["S6"]))
	result, err = o.ObserveHeader(f.token, sideFinalized, anc, 3)
	if err != nil || result.Change != Refused || result.Anchor != heads["A3"] {
		t.Fatalf("side-branch finalized: %+v %v", result, err)
	}
	// A header head on the side branch is refused before anything else: a
	// delayed side-branch callback after the canonical branch settled on A.
	sideHead := attestedEvidence(heads["A7"], AttestedSnapshot(heads["S6"], heads["S5"], heads["S4"]))
	if _, err := o.ObserveHeader(f.token, sideHead, anc, 4); !errors.Is(err, ErrBranch) {
		t.Fatalf("side-branch header head: %v", err)
	}
	if anchor, ok = o.Anchor(f.token); !ok || anchorHead(t, anchor) != heads["A3"] {
		t.Fatalf("anchor after side branch: %+v %v", anchor, ok)
	}
}

func TestAnchorMonotonicity(t *testing.T) {
	heads, anc := canonicalChain()
	f := newFixture(t)
	o := f.open(t, f.db, anc)
	observe := func(executed decision.Head, finalized decision.Head, sequence uint64) AnchorResult {
		t.Helper()
		result, err := o.ObserveHeader(f.token,
			attestedEvidence(executed, AttestedSnapshot(executed, executed, finalized)), anc, sequence)
		if err != nil {
			t.Fatalf("observe %s: %v", finalized.Hash, err)
		}
		return result
	}
	if result := observe(heads["A5"], heads["A3"], 1); result.Change != Accepted {
		t.Fatalf("first anchor: %+v", result)
	}
	if result := observe(heads["A6"], heads["A4"], 2); result.Change != Advanced || result.Anchor != heads["A4"] {
		t.Fatalf("advance: %+v", result)
	}
	if result := observe(heads["A6"], heads["A4"], 3); result.Change != Unchanged {
		t.Fatalf("repeat: %+v", result)
	}
	if result := observe(heads["A6"], heads["A3"], 4); result.Change != Stale || result.Anchor != heads["A4"] {
		t.Fatalf("stale: %+v", result)
	}
	// A same-height different-hash finalized of the authoritative header
	// source does not contain the accepted anchor: a proven regression.
	if _, err := o.ObserveHeader(f.token,
		attestedEvidence(heads["S6"], AttestedSnapshot(heads["S6"], heads["S5"], heads["S4"])), anc, 5); !errors.Is(
		err, ErrRegression) {
		t.Fatalf("incomparable candidate: %v", err)
	}
	// The observatory stopped after the regression.
	if _, err := o.ObserveHeader(f.token,
		attestedEvidence(heads["A7"], AttestedSnapshot(heads["A7"], heads["A6"], heads["A5"])), anc,
		6); !errors.Is(err, ErrFailed) {
		t.Fatalf("after the regression: %v", err)
	}
	if early := o.Early(f.token); early != nil {
		t.Fatalf("stopped observatory serves early evidence %+v", early)
	}
}

func TestExecutionHeadRegressionIsFatal(t *testing.T) {
	heads, anc := canonicalChain()
	f := newFixture(t)
	o := f.open(t, f.db, anc)
	if _, err := o.ObserveHeader(f.token,
		attestedEvidence(heads["A5"], AttestedSnapshot(heads["A5"], heads["A5"], heads["A5"])), anc, 1); err != nil {
		t.Fatal(err)
	}
	// A canonical head below the accepted anchor: the rewind removed a
	// finalized block, which is a proven regression, not a stale snapshot.
	lower := attestedEvidence(heads["A3"], AttestedSnapshot(heads["A3"], heads["A3"], heads["A3"]))
	if _, err := o.ObserveHeader(f.token, lower, anc, 2); !errors.Is(err, ErrRegression) {
		t.Fatalf("head below anchor: %v", err)
	}
	// A canonical head on the side branch abandons the accepted anchor the
	// same way: the head itself was read under the guard, so this is a new
	// authoritative decision, not a stale callback.
	f2 := newFixture(t)
	o2 := f2.open(t, f2.db, nil)
	if _, err := o2.ObserveHeader(f2.token,
		attestedEvidence(heads["A5"], AttestedSnapshot(heads["A5"], heads["A5"], heads["A5"])), anc, 1); err != nil {
		t.Fatal(err)
	}
	sideHead := attestedEvidence(heads["S6"], AttestedSnapshot(heads["S6"], heads["S5"], heads["S4"]))
	if _, err := o2.ObserveHeader(f2.token, sideHead, anc, 2); !errors.Is(err, ErrRegression) {
		t.Fatalf("side head: %v", err)
	}
	// A delayed side-branch callback while the canonical head still covers
	// the anchor is only a refused branch, not a regression.
	f3 := newFixture(t)
	o3 := f3.open(t, f3.db, nil)
	if _, err := o3.ObserveHeader(f3.token,
		attestedEvidence(heads["A5"], AttestedSnapshot(heads["A5"], heads["A5"], heads["A5"])), anc, 1); err != nil {
		t.Fatal(err)
	}
	delayed := attestedEvidence(heads["A5"], AttestedSnapshot(heads["S6"], heads["S5"], heads["S4"]))
	if _, err := o3.ObserveHeader(f3.token, delayed, anc, 2); !errors.Is(err, ErrBranch) {
		t.Fatalf("delayed side branch: %v", err)
	}
	if anchor, ok := o3.Anchor(f3.token); !ok || anchorHead(t, anchor) != heads["A5"] {
		t.Fatalf("anchor after the delayed callback: %+v %v", anchor, ok)
	}
}

func TestRestartRetainsEarlyEvidence(t *testing.T) {
	heads, anc := canonicalChain()
	v := loadVectors(t)
	f := newFixture(t)
	db := newStore()
	o := f.open(t, db, anc)
	early := EarlyVoteEvidence{Origin: NativeLive, EvaluationHead: heads["A7"],
		SourceJustified: heads["A6"], VoteTarget: heads["A7"], FinalizedCandidate: heads["A5"],
		ValidatorSnapshotParent: heads["A6"], ValidatorCount: 21, QuorumCount: quorumOf(21),
		MatchingVoteCount: v.Early.Matching}
	digest, err := EarlyDigest(early, []byte(v.Early.Votes))
	if err != nil {
		t.Fatal(err)
	}
	early.EvidenceDigest = digest
	if err := o.ObserveEarly(f.token, early, []byte(v.Early.Votes)); err != nil {
		t.Fatalf("observe early: %v", err)
	}
	if _, err := o.ObserveHeader(f.token,
		attestedEvidence(heads["A5"], AttestedSnapshot(heads["A5"], heads["A5"], heads["A3"])), anc, 1); err != nil {
		t.Fatal(err)
	}
	// A restart with an empty VotePool: the same store, a new observatory.
	restarted := f.open(t, db, anc)
	retained := restarted.Early(f.token)
	if retained == nil {
		t.Fatal("the early evidence was lost")
	}
	if retained.Origin != RetainedDurable {
		t.Fatalf("retained origin %s, want %s", retained.Origin, RetainedDurable)
	}
	if retained.EvidenceDigest != digest || retained.FinalizedCandidate != heads["A5"] {
		t.Fatalf("retained evidence changed: %+v", retained)
	}
	anchor, ok := restarted.Anchor(f.token)
	if !ok || anchorHead(t, anchor) != heads["A3"] {
		t.Fatalf("anchor after restart: %+v %v", anchor, ok)
	}
	// A lower stale snapshot after the restart does not lower the durable
	// anchor, and a lost early observation never does.
	result, err := restarted.ObserveHeader(f.token,
		attestedEvidence(heads["A4"], AttestedSnapshot(heads["A4"], heads["A4"], heads["A2"])), anc, 2)
	if err != nil || result.Change != Stale || result.Anchor != heads["A3"] {
		t.Fatalf("stale after restart: %+v %v", result, err)
	}
	if early := restarted.Early(f.token); early.Origin != RetainedDurable {
		t.Fatalf("early origin %s", early.Origin)
	}
	// A fresh observation with a retained origin is refused.
	retainedFresh := *retained
	retainedFresh.EvidenceDigest, _ = EarlyDigest(retainedFresh, []byte("votes"))
	if err := restarted.ObserveEarly(f.token, retainedFresh, []byte("votes")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("retained fresh observation: %v", err)
	}
	// A startup refresh observation replaces the record and is served
	// retained again.
	refresh := early
	refresh.Origin = NativeRestart
	refresh.EvidenceDigest, err = EarlyDigest(refresh, []byte(v.Early.Votes))
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.ObserveEarly(f.token, refresh, []byte(v.Early.Votes)); err != nil {
		t.Fatalf("startup refresh: %v", err)
	}
	if early := restarted.Early(f.token); early.Origin != RetainedDurable || early.EvidenceDigest != refresh.EvidenceDigest {
		t.Fatalf("refreshed evidence: %+v", early)
	}
}

func TestEarlyRecordIntegrity(t *testing.T) {
	heads, _ := canonicalChain()
	f := newFixture(t)
	o := f.open(t, f.db, nil)
	early := EarlyVoteEvidence{Origin: NativeLive, EvaluationHead: heads["A7"],
		SourceJustified: heads["A6"], VoteTarget: heads["A7"], FinalizedCandidate: heads["A5"],
		ValidatorSnapshotParent: heads["A6"], ValidatorCount: 4, QuorumCount: quorumOf(4),
		MatchingVoteCount: quorumOf(4)}
	digest, err := EarlyDigest(early, []byte("votes"))
	if err != nil {
		t.Fatal(err)
	}
	// A declared digest that does not bind the owned vote snapshot is a
	// conflict, and nothing is stored.
	other := early
	other.EvidenceDigest = digest
	if err := o.ObserveEarly(f.token, other, []byte("other votes")); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if early := o.Early(f.token); early != nil {
		t.Fatalf("conflict stored a record: %+v", early)
	}
	if err := o.ObserveEarly(f.token, other, []byte("votes")); err != nil {
		t.Fatalf("observe: %v", err)
	}
	// A power loss before the sync drops the record: only transient early
	// evidence is lost, and no anchor existed to lower.
	loss := newStore()
	f2 := newFixture(t)
	o2 := f2.open(t, loss, nil)
	loss.crashAt = 1
	if err := o2.ObserveEarly(f2.token, other, []byte("votes")); !errors.Is(err, errCrash) {
		t.Fatalf("power loss: %v", err)
	}
	if early := f2.open(t, loss.powerLoss(), nil).Early(f2.token); early != nil {
		t.Fatalf("unsynced record survived: %+v", early)
	}
	// A corrupted record is refused at open.
	key := []byte(keyPrefix + f.v.Producer + "." + f.v.Run + "/early")
	data, err := json.Marshal(&earlyRecord{NativeStoreID: f.v.StoreID, Producer: f.v.Producer + "." + f.v.Run,
		Evidence: other, VotesDigest: hash("votes"), Digest: hash("wrong")})
	if err != nil {
		t.Fatal(err)
	}
	f.db.durable[string(key)] = data
	if _, err := Open(f.token, f.db, f.adm, f.v.StoreID,
		RunIdentity{Network: gcNetwork, Node: gcNode, Producer: gcProducer, Run: gcRun},
		openEngine(t, f.db, nil)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt record: %v", err)
	}
	// A record of another store is an invariant error.
	f.db.durable[string(key)] = mustJSON(t, &earlyRecord{NativeStoreID: "other-store",
		Producer: f.v.Producer + "." + f.v.Run, Evidence: other, VotesDigest: hash("votes"),
		Digest: hash("digest")})
	if _, err := Open(f.token, f.db, f.adm, f.v.StoreID,
		RunIdentity{Network: gcNetwork, Node: gcNode, Producer: gcProducer, Run: gcRun},
		openEngine(t, f.db, nil)); !errors.Is(err, ErrInvariant) {
		t.Fatalf("foreign record: %v", err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestOpenValidatesArguments(t *testing.T) {
	f := newFixture(t)
	if _, err := Open(f.token, f.db, nil, f.v.StoreID,
		RunIdentity{Network: gcNetwork, Node: gcNode, Producer: gcProducer, Run: gcRun},
		openEngine(t, f.db, nil)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing admission: %v", err)
	}
	if _, err := Open(f.token, f.db, f.adm, "bad id",
		RunIdentity{Network: gcNetwork, Node: gcNode, Producer: gcProducer, Run: gcRun},
		openEngine(t, f.db, nil)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("store id: %v", err)
	}
	guard := admission.NewGuard()
	if _, err := Open(guard.Acquire(), f.db, f.adm, f.v.StoreID,
		RunIdentity{Network: gcNetwork, Node: gcNode, Producer: gcProducer, Run: gcRun},
		openEngine(t, f.db, nil)); err == nil {
		t.Fatal("another guard token was accepted")
	}
}

func TestConcurrentObservations(t *testing.T) {
	heads, anc := canonicalChain()
	f := newFixture(t)
	o := f.open(t, f.db, anc)
	evidence := attestedEvidence(heads["A5"], AttestedSnapshot(heads["A5"], heads["A5"], heads["A3"]))
	var group sync.WaitGroup
	changes := make([]AnchorChange, 4)
	for i := range changes {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			result, err := o.ObserveHeader(f.token, evidence, anc, uint64(i+1))
			if err == nil {
				changes[i] = result.Change
			}
		}(i)
	}
	group.Wait()
	accepted, repeated := 0, 0
	for _, change := range changes {
		switch change {
		case Accepted:
			accepted++
		case Unchanged:
			repeated++
		default:
			t.Fatalf("unexpected change %s", change)
		}
	}
	if accepted != 1 || repeated != 3 {
		t.Fatalf("accepted %d, repeated %d", accepted, repeated)
	}
	if anchor, ok := o.Anchor(f.token); !ok || anchorHead(t, anchor) != heads["A3"] {
		t.Fatalf("anchor %+v %v", anchor, ok)
	}
}
