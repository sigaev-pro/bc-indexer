package decider

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
)

// store is a synced in-memory native store; durability is the shared cores'
// own test subject. history keeps every value ever written.
type store struct {
	data    map[string][]byte
	history map[string][]byte
}

func (s *store) Has(key []byte) (bool, error) { _, ok := s.data[string(key)]; return ok, nil }
func (s *store) Get(key []byte) ([]byte, error) {
	value, ok := s.data[string(key)]
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return bytes.Clone(value), nil
}
func (s *store) Put(key, value []byte) error {
	s.data[string(key)], s.history[string(key)] = bytes.Clone(value), bytes.Clone(value)
	return nil
}
func (s *store) Delete(key []byte) error { delete(s.data, string(key)); return nil }
func (s *store) SyncKeyValue() error     { return nil }

type pins struct{ roots map[string]string }

func (p *pins) Verify(id, root string) (string, error) {
	if p.roots[id] != root {
		return "", errors.New("no such pin")
	}
	return hexOf("pin " + id), nil
}
func (p *pins) Release(id string) error { delete(p.roots, id); return nil }

type inputs struct {
	capsules map[string]string
	consumed map[string]admission.Consumed
}

func (i *inputs) CapsuleDigest(id string) (string, error) {
	if digest, ok := i.capsules[id]; ok {
		return digest, nil
	}
	return "", errors.New("capsule is not sealed")
}
func (i *inputs) Consumed(id string) (admission.Consumed, error) {
	if manifest, ok := i.consumed[id]; ok {
		return manifest, nil
	}
	return admission.Consumed{}, errors.New("log is not closed")
}

type roots struct{}

func (roots) Durable(string) (bool, error) { return true, nil }

type mirror struct{}

func (mirror) Applied(uint64, string) (bool, error) { return false, nil }

func hexOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func blockHash(number uint64) string { return hexOf(fmt.Sprint("block ", number)) }
func childRoot(number uint64) string { return hexOf(fmt.Sprint("root ", number)) }

func head(number uint64) decision.Head { return decision.Head{Number: number, Hash: blockHash(number)} }

func candidate(number uint64) admission.Candidate {
	return admission.Candidate{Number: number, Hash: blockHash(number), ParentHash: blockHash(number - 1),
		ParentRoot: childRoot(number - 1)}
}

// importOf is the IMPORT of block number onto its parent.
func importOf(number uint64) Transition {
	return Transition{Kind: decision.Import, Block: head(number), ChildRoot: childRoot(number), OldHead: head(number - 1),
		NewHead: head(number), LocalTD: fmt.Sprint(number - 1), ExternTD: fmt.Sprint(number)}
}

// native is the fake native side. Complete plays the import path: the
// transition of the block, Intend (recovering), then the native refusal and
// Reject, or the write with the marker and Resolve.
type native struct {
	f         *fixture
	stored    map[string]bool
	storedErr error
	refuse    error
	fail      error
	skip      bool
	kind      decision.Kind
	// other, if set, makes Complete settle that earlier decision instead.
	other     *uint64
	completed []string
}

func (n *native) Stored(block decision.Head) (bool, error) { return n.stored[block.Hash], n.storedErr }

func (n *native) Complete(t *admission.Token, obs admission.Observation) error {
	n.completed = append(n.completed, obs.Key)
	if n.fail != nil {
		return n.fail
	}
	if n.skip {
		return nil
	}
	if n.other != nil {
		return n.f.d.Resolve(t, *n.other)
	}
	tr := importOf(obs.Candidate.Number)
	if n.kind != "" {
		tr.Kind = n.kind
	}
	rec, err := n.f.d.Intend(t, tr, true)
	if err != nil {
		return err
	}
	if n.refuse != nil {
		return n.f.d.Reject(t, rec, tr.Block, n.refuse)
	}
	n.f.write(rec)
	n.stored[tr.Block.Hash] = true
	return n.f.d.Resolve(t, rec.Sequence)
}

type fixture struct {
	t      *testing.T
	db     *store
	pins   *pins
	inputs *inputs
	token  *admission.Token
	adm    *admission.Admission
	b      *decision.Bridge
	d      *Decider
	native *native
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, db: &store{data: map[string][]byte{}, history: map[string][]byte{}}, pins: &pins{roots: map[string]string{}},
		inputs: &inputs{capsules: map[string]string{}, consumed: map[string]admission.Consumed{}},
		token:  admission.NewGuard().Acquire()}
	f.native = &native{f: f, stored: map[string]bool{}}
	f.reopen()
	if err := f.b.GoLive(f.token); err != nil {
		t.Fatalf("go live: %v", err)
	}
	return f
}

func producer() admission.Producer { return admission.Producer{ProducerID: "bor", RunID: "run-1"} }

// reopen opens the admission (RECOVERY_ONLY), the bridge and a new decider
// over the same store, as a restart does.
func (f *fixture) reopen() {
	f.t.Helper()
	adm, err := admission.Open(f.token, f.db, producer(), f.pins, f.inputs)
	if err != nil {
		f.t.Fatalf("open admission: %v", err)
	}
	b, err := decision.Open(f.token, f.db, adm, "store-1", roots{}, mirror{})
	if err != nil {
		f.t.Fatalf("open bridge: %v", err)
	}
	f.adm, f.b = adm, b
	f.d = &Decider{Admission: adm, Bridge: b, Native: f.native}
}

// execReady runs one observation of block number to EXEC_READY.
func (f *fixture) execReady(number uint64) admission.Observation {
	f.t.Helper()
	c := candidate(number)
	obs, err := f.adm.Begin(f.token, c)
	if err != nil {
		f.t.Fatalf("begin: %v", err)
	}
	capsule := hexOf("capsule " + obs.Key)
	f.pins.roots[obs.Key], f.inputs.capsules[obs.Key] = c.ParentRoot, capsule
	if _, err := f.adm.Admit(f.token, obs.Key); err != nil {
		f.t.Fatalf("admit: %v", err)
	}
	chain := hexOf("chain " + obs.Key)
	consumed, err := admission.ConsumedDigest(capsule, 1, chain)
	if err != nil {
		f.t.Fatal(err)
	}
	f.inputs.consumed[obs.Key] = admission.Consumed{Observation: obs.Key, CapsuleDigest: capsule, Count: 1, Chain: chain,
		Digest: consumed}
	if _, err := f.adm.ExecReady(f.token, obs.Key, hexOf(fmt.Sprint("result ", number))); err != nil {
		f.t.Fatalf("exec ready: %v", err)
	}
	return f.observation(obs.Key)
}

func (f *fixture) observation(key string) admission.Observation {
	f.t.Helper()
	observations, err := f.adm.Observations(f.token)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, obs := range observations {
		if obs.Key == key {
			return obs
		}
	}
	return admission.Observation{Key: key}
}

// write is the native write: the final batch with the marker, synced.
func (f *fixture) write(rec decision.IntentRecord) {
	f.t.Helper()
	value, err := decision.MarkerValue(rec)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.Put(decision.MarkerKey(producer(), rec.Sequence), value); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) intend(tr Transition) decision.IntentRecord {
	f.t.Helper()
	rec, err := f.d.Intend(f.token, tr, false)
	if err != nil {
		f.t.Fatalf("intend: %v", err)
	}
	return rec
}

func (f *fixture) decisions() []decision.Decision {
	f.t.Helper()
	list, err := f.b.Decisions(f.token)
	if err != nil {
		f.t.Fatal(err)
	}
	return list
}

func (f *fixture) mode() admission.Mode {
	f.t.Helper()
	mode, err := f.adm.Mode(f.token)
	if err != nil {
		f.t.Fatal(err)
	}
	return mode
}

func (f *fixture) has(key []byte) bool { _, ok := f.db.data[string(key)]; return ok }

// field and expected digests, written again from the README formulas.
func lengthPrefixed(value string) []byte {
	return append(binary.BigEndian.AppendUint64(nil, uint64(len(value))), value...)
}

func expectedDigest(tag string, values ...string) string {
	data := append([]byte(tag), 0)
	for _, value := range values {
		data = append(data, lengthPrefixed(value)...)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestEvidenceAndRejectionDigests(t *testing.T) {
	tr := Transition{Kind: decision.Canonical, Block: head(7), ChildRoot: childRoot(7), OldHead: head(9),
		NewHead: head(7), LocalTD: "18", ExternTD: "19"}
	want := expectedDigest("extractor-bor-decision-evidence-v1", "CANONICAL", "7", blockHash(7), childRoot(7), "9",
		blockHash(9), "7", blockHash(7), "18", "19")
	if got := EvidenceDigest(tr); got != want {
		t.Fatalf("evidence digest %s, want %s", got, want)
	}
	// Every field takes part.
	for i, changed := range []Transition{
		{Kind: decision.Import, Block: tr.Block, ChildRoot: tr.ChildRoot, OldHead: tr.OldHead, NewHead: tr.NewHead, LocalTD: "18", ExternTD: "19"},
		{Kind: tr.Kind, Block: head(8), ChildRoot: tr.ChildRoot, OldHead: tr.OldHead, NewHead: tr.NewHead, LocalTD: "18", ExternTD: "19"},
		{Kind: tr.Kind, Block: tr.Block, ChildRoot: childRoot(8), OldHead: tr.OldHead, NewHead: tr.NewHead, LocalTD: "18", ExternTD: "19"},
		{Kind: tr.Kind, Block: tr.Block, ChildRoot: tr.ChildRoot, OldHead: head(8), NewHead: tr.NewHead, LocalTD: "18", ExternTD: "19"},
		{Kind: tr.Kind, Block: tr.Block, ChildRoot: tr.ChildRoot, OldHead: tr.OldHead, NewHead: head(8), LocalTD: "18", ExternTD: "19"},
		{Kind: tr.Kind, Block: tr.Block, ChildRoot: tr.ChildRoot, OldHead: tr.OldHead, NewHead: tr.NewHead, LocalTD: "17", ExternTD: "19"},
		{Kind: tr.Kind, Block: tr.Block, ChildRoot: tr.ChildRoot, OldHead: tr.OldHead, NewHead: tr.NewHead, LocalTD: "18", ExternTD: "20"},
	} {
		if EvidenceDigest(changed) == want {
			t.Fatalf("change %d does not change the evidence digest", i)
		}
	}
	intent := hexOf("intent")
	if got, want := RejectionDigest(intent, "milestone mismatch"),
		expectedDigest("extractor-bor-decision-rejection-v1", intent, "milestone mismatch"); got != want {
		t.Fatalf("rejection digest %s, want %s", got, want)
	}
}

func TestIntendBindsTheAuthoritativeObservation(t *testing.T) {
	f := newFixture(t)
	first := f.execReady(3)
	second := f.execReady(3)
	rec := f.intend(importOf(3))
	want := decision.Member{Observation: first.Key, Block: blockHash(3), ExecReadyDigest: first.ExecReady.Digest,
		ChildRoot: childRoot(3)}
	if len(rec.Members) != 1 || rec.Members[0] != want {
		t.Fatalf("members %+v, want the first observation %+v", rec.Members, want)
	}
	if rec.Kind != decision.Import || !rec.Events || rec.OldHead != head(2) || rec.NewHead != head(3) ||
		rec.EvidenceDigest != EvidenceDigest(importOf(3)) {
		t.Fatalf("intent %+v does not carry the transition", rec)
	}
	f.write(rec)
	if err := f.d.Resolve(f.token, rec.Sequence); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{first.Key, second.Key} {
		if obs := f.observation(key); obs.State != "" {
			t.Fatalf("%s is still %s after the decision", key, obs.State)
		}
	}
}

func TestIntendSideHasNoEvents(t *testing.T) {
	f := newFixture(t)
	f.execReady(3)
	tr := importOf(3)
	tr.Kind, tr.NewHead = decision.Side, tr.OldHead
	if rec := f.intend(tr); rec.Events {
		t.Fatal("a SIDE intent has public events")
	}
}

func TestIntendWithoutObservation(t *testing.T) {
	f := newFixture(t)
	f.execReady(3)
	for _, tr := range []Transition{importOf(4),
		{Kind: decision.Import, Block: decision.Head{Number: 4, Hash: blockHash(3)}, ChildRoot: childRoot(3),
			OldHead: head(2), NewHead: head(3), LocalTD: "2", ExternTD: "3"},
		{Kind: decision.Import, Block: decision.Head{Number: 3, Hash: blockHash(4)}, ChildRoot: childRoot(3),
			OldHead: head(2), NewHead: head(3), LocalTD: "2", ExternTD: "3"}} {
		if _, err := f.d.Intend(f.token, tr, false); !errors.Is(err, ErrNoObservation) {
			t.Fatalf("intend of block %d without its observation: %v", tr.Block.Number, err)
		}
	}
	// A block whose observation is only ADMITTED has none either.
	c := candidate(5)
	obs, err := f.adm.Begin(f.token, c)
	if err != nil {
		t.Fatal(err)
	}
	f.pins.roots[obs.Key], f.inputs.capsules[obs.Key] = c.ParentRoot, hexOf("capsule")
	if _, err := f.adm.Admit(f.token, obs.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Intend(f.token, importOf(5), false); !errors.Is(err, ErrNoObservation) {
		t.Fatalf("intend of an ADMITTED block: %v", err)
	}
}

func TestIntendWhileRecoveringReturnsTheSavedIntent(t *testing.T) {
	f := newFixture(t)
	f.execReady(3)
	saved := f.intend(importOf(3))
	if _, err := f.d.Intend(f.token, importOf(3), false); !errors.Is(err, decision.ErrPending) {
		t.Fatalf("a second live intent: %v", err)
	}
	again, err := f.d.Intend(f.token, importOf(3), true)
	if err != nil || again.Digest != saved.Digest || again.Sequence != saved.Sequence {
		t.Fatalf("recovering intent %+v, %v; want the saved %+v", again, err, saved)
	}
	for name, change := range map[string]func(*Transition){
		"kind":     func(tr *Transition) { tr.Kind, tr.NewHead = decision.Side, tr.OldHead },
		"old head": func(tr *Transition) { tr.OldHead = head(1) },
		"new head": func(tr *Transition) { tr.NewHead = head(4) },
		"evidence": func(tr *Transition) { tr.LocalTD = "1" },
		"root":     func(tr *Transition) { tr.ChildRoot = childRoot(4) },
	} {
		tr := importOf(3)
		change(&tr)
		if _, err := f.d.Intend(f.token, tr, true); !errors.Is(err, decision.ErrInvariant) {
			t.Fatalf("recovering intent with another %s: %v", name, err)
		}
	}
	// The saved intent of another block is not this transition either.
	f.execReady(4)
	if _, err := f.d.Intend(f.token, importOf(4), true); !errors.Is(err, decision.ErrInvariant) {
		t.Fatalf("recovering intent of another block: %v", err)
	}
	if got := len(f.decisions()); got != 1 {
		t.Fatalf("%d decisions after the recovering intents, want 1", got)
	}
}

func TestMatchesComparesEveryField(t *testing.T) {
	member := decision.Member{Observation: "o", Block: blockHash(3), ExecReadyDigest: hexOf("r"), ChildRoot: childRoot(3)}
	in := decision.Intent{Kind: decision.Import, OldHead: head(2), NewHead: head(3), Members: []decision.Member{member},
		EvidenceDigest: hexOf("e"), Events: true}
	saved := decision.IntentRecord{Kind: in.Kind, OldHead: in.OldHead, NewHead: in.NewHead,
		Members: []decision.Member{member}, EvidenceDigest: in.EvidenceDigest, Events: true}
	if !matches(saved, in) {
		t.Fatal("equal intents do not match")
	}
	other := member
	other.ExecReadyDigest = hexOf("other")
	for name, change := range map[string]func(*decision.IntentRecord){
		"kind":     func(r *decision.IntentRecord) { r.Kind = decision.Canonical },
		"old head": func(r *decision.IntentRecord) { r.OldHead = head(1) },
		"new head": func(r *decision.IntentRecord) { r.NewHead = head(4) },
		"events":   func(r *decision.IntentRecord) { r.Events = false },
		"members":  func(r *decision.IntentRecord) { r.Members = append(r.Members, member) },
		"member":   func(r *decision.IntentRecord) { r.Members = []decision.Member{other} },
		"evidence": func(r *decision.IntentRecord) { r.EvidenceDigest = hexOf("x") },
	} {
		changed := saved
		changed.Members = slices.Clone(saved.Members)
		change(&changed)
		if matches(changed, in) {
			t.Fatalf("intents with another %s match", name)
		}
	}
}

func TestRejectRecordsTheNativeRejection(t *testing.T) {
	f := newFixture(t)
	obs := f.execReady(3)
	rec := f.intend(importOf(3))
	if err := f.d.Reject(f.token, rec, head(3), nil); !errors.Is(err, decision.ErrInvalid) {
		t.Fatalf("reject without a native error: %v", err)
	}
	f.native.stored[blockHash(3)] = true
	if err := f.d.Reject(f.token, rec, head(3), errors.New("refused")); !errors.Is(err, decision.ErrInvariant) {
		t.Fatalf("reject of a stored block: %v", err)
	}
	f.native.stored[blockHash(3)] = false
	f.native.storedErr = errors.New("store read failed")
	if err := f.d.Reject(f.token, rec, head(3), errors.New("refused")); err == nil ||
		!strings.Contains(err.Error(), "store read failed") {
		t.Fatalf("reject with a failing store read: %v", err)
	}
	f.native.storedErr = nil
	if err := f.d.Reject(f.token, rec, head(3), errors.New("refused")); err != nil {
		t.Fatal(err)
	}
	if obs := f.observation(obs.Key); obs.State != "" {
		t.Fatalf("the rejected member is still %s", obs.State)
	}
	if len(f.decisions()) != 0 || f.has(decision.MarkerKey(producer(), rec.Sequence)) {
		t.Fatal("a rejected intent left a decision or a marker")
	}
	// The rejection record binds the native error text.
	var got decision.RejectedRecord
	key := fmt.Sprintf("extractor-decision-v1/bor.run-1/%016x/rejected", rec.Sequence)
	if err := json.Unmarshal(f.db.history[key], &got); err != nil || got.EvidenceDigest != RejectionDigest(rec.Digest, "refused") {
		t.Fatalf("rejected record %+v, %v", got, err)
	}
}

func TestResolveRemovesTheSideMarker(t *testing.T) {
	f := newFixture(t)
	f.execReady(3)
	tr := importOf(3)
	tr.Kind, tr.NewHead = decision.Side, tr.OldHead
	side := f.intend(tr)
	if err := f.d.Resolve(f.token, side.Sequence); !errors.Is(err, decision.ErrNoMarker) {
		t.Fatalf("resolve without the marker: %v", err)
	}
	f.write(side)
	if err := f.d.Resolve(f.token, side.Sequence); err != nil {
		t.Fatal(err)
	}
	if f.has(decision.MarkerKey(producer(), side.Sequence)) {
		t.Fatal("the marker of a SIDE decision stayed after Resolve")
	}
	f.execReady(4)
	imported := f.intend(importOf(4))
	f.write(imported)
	if err := f.d.Resolve(f.token, imported.Sequence); err != nil {
		t.Fatal(err)
	}
	if !f.has(decision.MarkerKey(producer(), imported.Sequence)) {
		t.Fatal("the marker of a BATCH decision was removed before the mirror applied it")
	}
}

func TestRecoverResolvesAnIntentWithItsMarker(t *testing.T) {
	f := newFixture(t)
	f.execReady(3)
	rec := f.intend(importOf(3))
	f.write(rec)
	f.reopen()
	report, err := f.d.Recover(f.token)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(report.Resolved, []uint64{rec.Sequence}) || len(report.Completed)+len(report.Rejected)+
		len(report.Imported) != 0 || len(f.native.completed) != 0 {
		t.Fatalf("report %+v, completions %v", report, f.native.completed)
	}
	if f.mode() != admission.Live {
		t.Fatal("recovery did not go live")
	}
}

func TestRecoverCompletesTheSavedTransition(t *testing.T) {
	f := newFixture(t)
	obs := f.execReady(3)
	rec := f.intend(importOf(3))
	f.reopen()
	report, err := f.d.Recover(f.token)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(report.Completed, []uint64{rec.Sequence}) || !slices.Equal(f.native.completed, []string{obs.Key}) ||
		len(report.Imported) != 0 {
		t.Fatalf("report %+v, completions %v", report, f.native.completed)
	}
	list := f.decisions()
	if len(list) != 1 || list[0].Sequence != rec.Sequence || list[0].State != decision.Decided ||
		list[0].Intent.Digest != rec.Digest {
		t.Fatalf("decisions %+v, want the saved intent decided", list)
	}
	if f.mode() != admission.Live {
		t.Fatal("recovery did not go live")
	}
}

func TestRecoverRecordsTheNativeRejection(t *testing.T) {
	f := newFixture(t)
	f.execReady(3)
	rec := f.intend(importOf(3))
	f.reopen()
	f.native.refuse = errors.New("milestone mismatch")
	report, err := f.d.Recover(f.token)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(report.Rejected, []uint64{rec.Sequence}) || len(report.Completed) != 0 || len(f.decisions()) != 0 {
		t.Fatalf("report %+v, decisions %+v", report, f.decisions())
	}
	if f.has(decision.MarkerKey(producer(), rec.Sequence)) || f.mode() != admission.Live {
		t.Fatal("a recovered rejection left a marker or stayed in RECOVERY_ONLY")
	}
}

func TestRecoverStopsWhenNativeRecoveryDoesNotSettle(t *testing.T) {
	for name, set := range map[string]func(*native){
		"unsettled": func(n *native) { n.skip = true },
		"failure":   func(n *native) { n.fail = errors.New("replay differs") },
		"other":     func(n *native) { n.kind = decision.Side },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.execReady(3)
			f.intend(importOf(3))
			f.reopen()
			set(f.native)
			_, err := f.d.Recover(f.token)
			if err == nil || (f.native.fail != nil && !errors.Is(err, f.native.fail)) {
				t.Fatalf("recovery: %v", err)
			}
			if f.mode() == admission.Live {
				t.Fatal("recovery went live with an unsettled intent")
			}
		})
	}
}

func TestRecoverRefusesAnIntentWithTwoMembers(t *testing.T) {
	f := newFixture(t)
	a, b := f.execReady(3), f.execReady(4)
	in := decision.Intent{Kind: decision.Import, OldHead: head(2), NewHead: head(4), EvidenceDigest: hexOf("e"),
		Events: true, Members: []decision.Member{
			{Observation: a.Key, ExecReadyDigest: a.ExecReady.Digest, ChildRoot: childRoot(3)},
			{Observation: b.Key, ExecReadyDigest: b.ExecReady.Digest, ChildRoot: childRoot(4)}}}
	if _, err := f.b.Intend(f.token, in); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	if _, err := f.d.Recover(f.token); !errors.Is(err, decision.ErrInvariant) {
		t.Fatalf("recovery of a two-member intent: %v", err)
	}
	if len(f.native.completed) != 0 {
		t.Fatal("recovery completed a member of a two-member intent")
	}
}

func TestRecoverImportsEveryUndecidedObservation(t *testing.T) {
	f := newFixture(t)
	first := f.execReady(3)
	second := f.execReady(4)
	f.reopen()
	report, err := f.d.Recover(f.token)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.native.completed, []string{first.Key, second.Key}) || len(report.Imported) != 2 ||
		report.Imported[0] != (Imported{Observation: first.Key, Sequence: 0, Outcome: Decided}) ||
		report.Imported[1] != (Imported{Observation: second.Key, Sequence: 1, Outcome: Decided}) {
		t.Fatalf("report %+v, completions %v", report, f.native.completed)
	}
	if f.mode() != admission.Live {
		t.Fatal("recovery did not go live")
	}
}

func TestRecoverImportsTheLowestSequenceFirst(t *testing.T) {
	f := newFixture(t)
	first := f.execReady(3)
	second := f.execReady(3)
	f.reopen()
	report, err := f.d.Recover(f.token)
	if err != nil {
		t.Fatal(err)
	}
	// One decision for the block observed twice, over the first observation.
	if !slices.Equal(f.native.completed, []string{first.Key}) || len(report.Imported) != 1 ||
		report.Imported[0].Observation != first.Key {
		t.Fatalf("report %+v, completions %v", report, f.native.completed)
	}
	if obs := f.observation(second.Key); obs.State != "" {
		t.Fatalf("the second observation of the block is still %s", obs.State)
	}
}

func TestRecoverRefusesAStoredBlockWithoutDecision(t *testing.T) {
	f := newFixture(t)
	f.execReady(3)
	f.native.stored[blockHash(3)] = true
	f.reopen()
	if _, err := f.d.Recover(f.token); !errors.Is(err, decision.ErrInvariant) {
		t.Fatalf("recovery of a stored block without decision: %v", err)
	}
	if len(f.native.completed) != 0 || f.mode() == admission.Live {
		t.Fatal("recovery completed or went live")
	}
	f.native.stored[blockHash(3)] = false
	f.native.storedErr = errors.New("store read failed")
	if _, err := f.d.Recover(f.token); err == nil || !strings.Contains(err.Error(), "store read failed") {
		t.Fatalf("recovery with a failing store read: %v", err)
	}
}

func TestRecoverStopsOnAFailedImport(t *testing.T) {
	for name, set := range map[string]func(*native){
		"failure":   func(n *native) { n.fail = errors.New("replay differs") },
		"unsettled": func(n *native) { n.skip = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.execReady(3)
			f.reopen()
			set(f.native)
			_, err := f.d.Recover(f.token)
			if err == nil || (f.native.fail != nil && !errors.Is(err, f.native.fail)) {
				t.Fatalf("recovery: %v", err)
			}
			if f.mode() == admission.Live {
				t.Fatal("recovery went live with an undecided observation")
			}
		})
	}
}

func TestRecoverRejectsAnUndecidedObservation(t *testing.T) {
	f := newFixture(t)
	obs := f.execReady(3)
	f.reopen()
	f.native.refuse = errors.New("milestone mismatch")
	report, err := f.d.Recover(f.token)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Imported) != 1 || report.Imported[0] != (Imported{Observation: obs.Key, Sequence: 0, Outcome: Rejected}) {
		t.Fatalf("report %+v", report)
	}
}

func TestRecoverWithNothingToDo(t *testing.T) {
	f := newFixture(t)
	f.reopen()
	report, err := f.d.Recover(f.token)
	if err != nil || len(report.Resolved)+len(report.Completed)+len(report.Rejected)+len(report.Imported) != 0 {
		t.Fatalf("report %+v, %v", report, err)
	}
	if f.mode() != admission.Live {
		t.Fatal("recovery did not go live")
	}
}

func TestCompleteRequiresExactlyOneSettledDecision(t *testing.T) {
	f := newFixture(t)
	obs := f.execReady(3)
	f.native.skip = true
	if _, _, err := f.d.complete(f.token, obs); !errors.Is(err, ErrUnsettled) {
		t.Fatalf("completion that settled nothing: %v", err)
	}
	f.native.skip = false
	seq, outcome, err := f.d.complete(f.token, obs)
	if err != nil || seq != 0 || outcome != Decided {
		t.Fatalf("completion: %d %s %v", seq, outcome, err)
	}
	// A second completion starts from no settled decision.
	f.native.refuse = errors.New("refused")
	second := f.execReady(4)
	seq, outcome, err = f.d.complete(f.token, second)
	if err != nil || seq != 1 || outcome != Rejected {
		t.Fatalf("second completion: %d %s %v", seq, outcome, err)
	}
}

func TestRecoverRefusesACompletionOfAnotherDecision(t *testing.T) {
	f := newFixture(t)
	f.execReady(3)
	first := f.intend(importOf(3))
	f.write(first)
	if err := f.d.Resolve(f.token, first.Sequence); err != nil {
		t.Fatal(err)
	}
	f.execReady(4)
	f.intend(importOf(4))
	f.reopen()
	other := first.Sequence
	f.native.other = &other
	if _, err := f.d.Recover(f.token); !errors.Is(err, ErrUnsettled) {
		t.Fatalf("recovery that settled another decision: %v", err)
	}
}

func TestRecoverRefusesAnObservationThatStaysUndecided(t *testing.T) {
	f := newFixture(t)
	f.execReady(3)
	first := f.intend(importOf(3))
	f.write(first)
	if err := f.d.Resolve(f.token, first.Sequence); err != nil {
		t.Fatal(err)
	}
	f.execReady(4)
	f.reopen()
	other := first.Sequence
	f.native.other = &other
	done := make(chan error, 1)
	go func() { _, err := f.d.Recover(f.token); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnsettled) {
			t.Fatalf("recovery of an observation that stays undecided: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recovery did not stop on an observation that stays undecided")
	}
}
