package lifecycle_test

import (
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
	lifecycle "github.com/sigaev-pro/bc-indexer/test/lifecycle_validator"
)

// Fixtures: one network, one producer and run, one node instance and writer
// epoch. Distinct byte patterns keep every identity unambiguous.
var (
	net1       = semantic.Network{ChainID: u256(1), GenesisHash: h32(1)}
	producer1  = id16(0x11)
	run1       = id16(0x22)
	node1      = id16(0x33)
	epoch1     = id16(0x44)
	txHash     = h32(0xA7)
	producer2  = id16(0x55)
	run2       = id16(0x66)
	net2       = semantic.Network{ChainID: u256(2), GenesisHash: h32(2)}
	decisionTx = uint64(100)
)

func u256(b byte) semantic.UInt256 {
	var v semantic.UInt256
	v[31] = b
	return v
}

func h32(b byte) semantic.Hash32 {
	var v semantic.Hash32
	for i := range v {
		v[i] = b
	}
	return v
}

func id16(b byte) semantic.ID16 {
	var v semantic.ID16
	for i := range v {
		v[i] = b
	}
	return v
}

func source(seq uint64) semantic.SourceRef {
	return semantic.SourceRef{NodeInstanceID: node1, WriterEpoch: epoch1, SourceSequence: seq}
}

// txEvent builds one transaction-entity event of one activation: decision
// decisionTx, body index 0 (entity ordinal 1), a fixed transaction hash and
// the given block byte.
func txEvent(kind semantic.Kind, rev, seq, decision uint64, block byte) semantic.SemanticDescriptor {
	d, err := semantic.NewTransactionDescriptor(kind, net1, producer1, run1, decision,
		h32(block), txHash, 0, rev, source(seq))
	if err != nil {
		panic(err)
	}
	return d
}

func txApplied(seq, decision uint64, block byte) semantic.SemanticDescriptor {
	return txEvent(semantic.KindApplied, 0, seq, decision, block)
}

func txStatus(rev, seq, decision uint64, block byte) semantic.SemanticDescriptor {
	return txEvent(semantic.KindStatus, rev, seq, decision, block)
}

func txRemoved(rev, seq, decision uint64, block byte) semantic.SemanticDescriptor {
	return txEvent(semantic.KindRemoved, rev, seq, decision, block)
}

// blockEvent builds one block-entity event (entity ordinal 0, no inclusion
// binding).
func blockEvent(kind semantic.Kind, rev, seq, decision uint64, block byte) semantic.SemanticDescriptor {
	d, err := semantic.NewBlockDescriptor(kind, net1, producer1, run1, decision,
		h32(block), rev, source(seq))
	if err != nil {
		panic(err)
	}
	return d
}

// optionalEvent builds one optional-entity event (family balance.system or
// bor.legacy_receipt) with the entity ordinal derived from the body count.
func optionalEvent(family semantic.Family, kind semantic.Kind, rev, seq, decision uint64,
	block byte, bodyCount, nativeOrder uint64) semantic.SemanticDescriptor {
	d, err := semantic.NewOptionalDescriptor(family, kind, net1, producer1, run1, decision,
		h32(block), bodyCount, nativeOrder, rev, source(seq))
	if err != nil {
		panic(err)
	}
	return d
}

func mustApply(t *testing.T, a *lifecycle.Automaton, d semantic.SemanticDescriptor) lifecycle.Result {
	t.Helper()
	res, err := a.Apply(d, digestOf(t, d))
	if err != nil {
		t.Fatalf("apply returned an unexpected error: %v", err)
	}
	return res
}

func mustRecord(t *testing.T, a *lifecycle.Automaton, key lifecycle.ActivationKey) lifecycle.Record {
	t.Helper()
	rec, ok := a.Record(key)
	if !ok {
		t.Fatalf("no record for key %v", key)
	}
	return rec
}

func digestOf(t *testing.T, d semantic.SemanticDescriptor) lifecycle.Digest {
	t.Helper()
	digest, err := d.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return lifecycle.Digest(digest)
}

func recordsEqual(left, right map[lifecycle.ActivationKey]lifecycle.Record) bool {
	if len(left) != len(right) {
		return false
	}
	for key, rec := range left {
		other, ok := right[key]
		if !ok || !rec.Equal(other) {
			return false
		}
	}
	return true
}

func dumpRecords(records map[lifecycle.ActivationKey]lifecycle.Record) string {
	keys := make([]string, 0, len(records))
	byKey := make(map[string]string, len(records))
	for key, rec := range records {
		text := fmt.Sprintf("%v -> %s", key, rec)
		keys = append(keys, text)
		byKey[text] = text
	}
	sort.Strings(keys)
	out := "{"
	for i, key := range keys {
		if i > 0 {
			out += "; "
		}
		out += key
	}
	return out + "}"
}

// permutations returns all n! index permutations in the deterministic order
// of Heap's algorithm.
func permutations(n int) [][]int {
	var out [][]int
	a := make([]int, n)
	for i := range a {
		a[i] = i
	}
	var generate func(k int)
	generate = func(k int) {
		if k == 1 {
			out = append(out, append([]int(nil), a...))
			return
		}
		for i := 0; i < k; i++ {
			generate(k - 1)
			if k%2 == 0 {
				a[i], a[k-1] = a[k-1], a[i]
			} else {
				a[0], a[k-1] = a[k-1], a[0]
			}
		}
	}
	generate(n)
	return out
}

// lcg is a fixed linear congruential generator: the shuffle stream is
// identical on every platform and every run.
type lcg struct{ state uint64 }

func (l *lcg) next() uint64 {
	l.state = l.state*6364136223846793005 + 1442695040888963407
	return l.state >> 33
}

func (l *lcg) permutation(n int) []int {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	for i := n - 1; i > 0; i-- {
		j := int(l.next() % uint64(i+1))
		p[i], p[j] = p[j], p[i]
	}
	return p
}

func identity(n int) []int {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return p
}

func reversed(n int) []int {
	p := identity(n)
	for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
		p[i], p[j] = p[j], p[i]
	}
	return p
}

func runOrdered(t *testing.T, events []semantic.SemanticDescriptor,
	order []int) map[lifecycle.ActivationKey]lifecycle.Record {
	t.Helper()
	a := lifecycle.New()
	for _, i := range order {
		mustApply(t, a, events[i])
	}
	return a.Records()
}

// TestContractTableRows walks every row of the lifecycle state table of
// MESSAGE_CONTRACT section 5 and checks the outcome class, the resulting
// state, the applied origin and the evidence frontier.
func TestContractTableRows(t *testing.T) {
	rows := []struct {
		name       string
		setup      []semantic.SemanticDescriptor
		event      semantic.SemanticDescriptor
		wantClass  lifecycle.Class
		wantState  lifecycle.State
		wantOrigin bool
		wantMax    uint64
	}{
		{
			name: "absent applied becomes active", event: txApplied(1, decisionTx, 1),
			wantClass: lifecycle.ClassAccepted, wantState: lifecycle.StateActive, wantOrigin: true, wantMax: 0,
		},
		{
			name: "absent status keeps pending evidence", event: txStatus(3, 2, decisionTx, 1),
			wantClass: lifecycle.ClassUnknown, wantState: lifecycle.StatePendingApplied, wantMax: 3,
		},
		{
			name: "absent removed keeps tombstone", event: txRemoved(5, 3, decisionTx, 1),
			wantClass: lifecycle.ClassUnknown, wantState: lifecycle.StateRemoved, wantMax: 5,
		},
		{
			name:  "pending applied becomes active with kept evidence",
			setup: []semantic.SemanticDescriptor{txStatus(4, 2, decisionTx, 1)}, event: txApplied(1, decisionTx, 1),
			wantClass: lifecycle.ClassAccepted, wantState: lifecycle.StateActive, wantOrigin: true, wantMax: 4,
		},
		{
			name:  "pending higher status replaces the snapshot",
			setup: []semantic.SemanticDescriptor{txStatus(3, 2, decisionTx, 1)}, event: txStatus(9, 3, decisionTx, 1),
			wantClass: lifecycle.ClassAccepted, wantState: lifecycle.StatePendingApplied, wantMax: 9,
		},
		{
			name:  "pending removed terminalizes",
			setup: []semantic.SemanticDescriptor{txStatus(3, 2, decisionTx, 1)}, event: txRemoved(6, 3, decisionTx, 1),
			wantClass: lifecycle.ClassAccepted, wantState: lifecycle.StateRemoved, wantMax: 6,
		},
		{
			name:  "active applied retry is the same fact",
			setup: []semantic.SemanticDescriptor{txApplied(1, decisionTx, 1)}, event: txApplied(1, decisionTx, 1),
			wantClass: lifecycle.ClassDuplicate, wantState: lifecycle.StateActive, wantOrigin: true, wantMax: 0,
		},
		{
			name:      "active higher status is new evidence",
			setup:     []semantic.SemanticDescriptor{txApplied(1, decisionTx, 1), txStatus(2, 2, decisionTx, 1)},
			event:     txStatus(5, 3, decisionTx, 1),
			wantClass: lifecycle.ClassAccepted, wantState: lifecycle.StateActive, wantOrigin: true, wantMax: 5,
		},
		{
			name:  "active removed is terminal",
			setup: []semantic.SemanticDescriptor{txApplied(1, decisionTx, 1)}, event: txRemoved(4, 2, decisionTx, 1),
			wantClass: lifecycle.ClassAccepted, wantState: lifecycle.StateRemoved, wantOrigin: true, wantMax: 4,
		},
		{
			name:      "removed redelivered applied stays removed",
			setup:     []semantic.SemanticDescriptor{txApplied(1, decisionTx, 1), txRemoved(4, 2, decisionTx, 1)},
			event:     txApplied(1, decisionTx, 1),
			wantClass: lifecycle.ClassDuplicate, wantState: lifecycle.StateRemoved, wantOrigin: true, wantMax: 4,
		},
		{
			name:  "removed before applied keeps the tombstone",
			setup: []semantic.SemanticDescriptor{txRemoved(4, 1, decisionTx, 1)}, event: txApplied(2, decisionTx, 1),
			wantClass: lifecycle.ClassStale, wantState: lifecycle.StateRemoved, wantOrigin: true, wantMax: 4,
		},
		{
			name:      "removed late status does not reactivate",
			setup:     []semantic.SemanticDescriptor{txApplied(1, decisionTx, 1), txRemoved(6, 2, decisionTx, 1)},
			event:     txStatus(2, 3, decisionTx, 1),
			wantClass: lifecycle.ClassStale, wantState: lifecycle.StateRemoved, wantOrigin: true, wantMax: 6,
		},
		{
			name:  "pending same-revision retry is idempotent",
			setup: []semantic.SemanticDescriptor{txStatus(3, 2, decisionTx, 1)}, event: txStatus(3, 2, decisionTx, 1),
			wantClass: lifecycle.ClassDuplicate, wantState: lifecycle.StatePendingApplied, wantMax: 3,
		},
		{
			name:  "pending same-revision different digest conflicts",
			setup: []semantic.SemanticDescriptor{txStatus(3, 2, decisionTx, 1)}, event: txStatus(3, 3, decisionTx, 1),
			wantClass: lifecycle.ClassConflict, wantState: lifecycle.StatePendingApplied, wantMax: 3,
		},
		{
			name:      "active same-revision different digest conflicts",
			setup:     []semantic.SemanticDescriptor{txApplied(1, decisionTx, 1), txStatus(5, 2, decisionTx, 1)},
			event:     txStatus(5, 3, decisionTx, 1),
			wantClass: lifecycle.ClassConflict, wantState: lifecycle.StateActive, wantOrigin: true, wantMax: 5,
		},
		{
			name:      "active older status is stale",
			setup:     []semantic.SemanticDescriptor{txApplied(1, decisionTx, 1), txStatus(5, 2, decisionTx, 1)},
			event:     txStatus(2, 3, decisionTx, 1),
			wantClass: lifecycle.ClassStale, wantState: lifecycle.StateActive, wantOrigin: true, wantMax: 5,
		},
		{
			name: "block entity applied becomes active", event: blockEvent(semantic.KindApplied, 0, 1, 50, 1),
			wantClass: lifecycle.ClassAccepted, wantState: lifecycle.StateActive, wantOrigin: true, wantMax: 0,
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			a := lifecycle.New()
			for _, setup := range row.setup {
				mustApply(t, a, setup)
			}
			res := mustApply(t, a, row.event)
			if res.Class != row.wantClass {
				t.Errorf("class = %s, want %s (reason: %s)", res.Class, row.wantClass, res.Reason)
			}
			if res.State != row.wantState {
				t.Errorf("resulting state = %s, want %s", res.State, row.wantState)
			}
			rec := mustRecord(t, a, lifecycle.KeyOf(row.event))
			if rec.State != row.wantState {
				t.Errorf("record state = %s, want %s", rec.State, row.wantState)
			}
			if (rec.Origin != nil) != row.wantOrigin {
				t.Errorf("origin observed = %v, want %v", rec.Origin != nil, row.wantOrigin)
			}
			if rec.MaxRevision != row.wantMax {
				t.Errorf("max revision = %d, want %d", rec.MaxRevision, row.wantMax)
			}
		})
	}
}

// TestReorderExhaustivePermutations applies every permutation of several
// well-formed event sets and requires identical final records.
func TestReorderExhaustivePermutations(t *testing.T) {
	sets := []struct {
		name       string
		events     []semantic.SemanticDescriptor
		wantState  lifecycle.State
		wantOrigin bool
		wantMax    uint64
		wantSlots  []uint64
	}{
		{
			name: "full journey with duplicate delivery",
			events: []semantic.SemanticDescriptor{
				txApplied(1, decisionTx, 1), txApplied(1, decisionTx, 1),
				txStatus(2, 2, decisionTx, 1), txRemoved(5, 3, decisionTx, 1),
			},
			wantState: lifecycle.StateRemoved, wantOrigin: true, wantMax: 5, wantSlots: []uint64{0, 2, 5},
		},
		{
			name: "applied then removed",
			events: []semantic.SemanticDescriptor{
				txApplied(1, decisionTx, 1), txRemoved(4, 2, decisionTx, 1),
			},
			wantState: lifecycle.StateRemoved, wantOrigin: true, wantMax: 4, wantSlots: []uint64{0, 4},
		},
		{
			name: "status then removed without applied",
			events: []semantic.SemanticDescriptor{
				txStatus(3, 1, decisionTx, 1), txRemoved(6, 2, decisionTx, 1),
			},
			wantState: lifecycle.StateRemoved, wantMax: 6, wantSlots: []uint64{3, 6},
		},
		{
			name: "two status revisions",
			events: []semantic.SemanticDescriptor{
				txStatus(2, 1, decisionTx, 1), txStatus(5, 2, decisionTx, 1),
			},
			wantState: lifecycle.StatePendingApplied, wantMax: 5, wantSlots: []uint64{2, 5},
		},
		{
			name: "applied with late evidence",
			events: []semantic.SemanticDescriptor{
				txApplied(1, decisionTx, 1), txStatus(4, 2, decisionTx, 1),
			},
			wantState: lifecycle.StateActive, wantOrigin: true, wantMax: 4, wantSlots: []uint64{0, 4},
		},
		{
			name: "five events",
			events: []semantic.SemanticDescriptor{
				txApplied(1, decisionTx, 1), txStatus(2, 2, decisionTx, 1),
				txStatus(5, 3, decisionTx, 1), txStatus(8, 4, decisionTx, 1),
				txRemoved(9, 5, decisionTx, 1),
			},
			wantState: lifecycle.StateRemoved, wantOrigin: true, wantMax: 9, wantSlots: []uint64{0, 2, 5, 8, 9},
		},
	}
	for _, set := range sets {
		t.Run(set.name, func(t *testing.T) {
			canonical := runOrdered(t, set.events, identity(len(set.events)))
			key := lifecycle.KeyOf(set.events[0])
			rec, ok := canonical[key]
			if !ok {
				t.Fatalf("no record for key %v", key)
			}
			if rec.State != set.wantState {
				t.Errorf("canonical state = %s, want %s", rec.State, set.wantState)
			}
			if (rec.Origin != nil) != set.wantOrigin {
				t.Errorf("canonical origin observed = %v, want %v", rec.Origin != nil, set.wantOrigin)
			}
			if rec.MaxRevision != set.wantMax {
				t.Errorf("canonical max revision = %d, want %d", rec.MaxRevision, set.wantMax)
			}
			if len(rec.Evidence) != len(set.wantSlots) {
				t.Errorf("canonical evidence slots = %d (%s), want %d", len(rec.Evidence), rec, len(set.wantSlots))
			}
			for _, rev := range set.wantSlots {
				if _, ok := rec.Evidence[rev]; !ok {
					t.Errorf("canonical evidence misses revision %d: %s", rev, rec)
				}
			}
			perms := permutations(len(set.events))
			for _, perm := range perms {
				got := runOrdered(t, set.events, perm)
				if !recordsEqual(canonical, got) {
					t.Fatalf("permutation %v produced %s, want %s", perm,
						dumpRecords(got), dumpRecords(canonical))
				}
			}
			if len(perms) != factorial(len(set.events)) {
				t.Errorf("permutation generator returned %d permutations, want %d", len(perms), factorial(len(set.events)))
			}
		})
	}
}

func factorial(n int) int {
	out := 1
	for i := 2; i <= n; i++ {
		out *= i
	}
	return out
}

// TestReorderMixedActivations shuffles the interleaved events of three
// activations (one with a duplicate delivery and one reinclusion) and
// requires identical final records for every order.
func TestReorderMixedActivations(t *testing.T) {
	events := []semantic.SemanticDescriptor{
		// Activation 1: decision 100, block 1: applied, status, removed, plus
		// one redelivered applied.
		txApplied(1, 100, 1),
		txStatus(2, 2, 100, 1),
		txRemoved(5, 3, 100, 1),
		txApplied(1, 100, 1),
		// Activation 2: decision 200, block 2: status before applied, then
		// later evidence.
		txStatus(3, 4, 200, 2),
		txApplied(5, 200, 2),
		txStatus(9, 6, 200, 2),
		// Activation 3: reinclusion of the same transaction hash in block 3
		// under a new decision sequence.
		txApplied(7, 300, 3),
	}
	canonical := runOrdered(t, events, identity(len(events)))
	if len(canonical) != 3 {
		t.Fatalf("canonical run produced %d records, want 3: %s", len(canonical), dumpRecords(canonical))
	}
	want := map[uint64]struct {
		state  lifecycle.State
		origin bool
		max    uint64
		slots  int
	}{
		100: {lifecycle.StateRemoved, true, 5, 3},
		200: {lifecycle.StateActive, true, 9, 3},
		300: {lifecycle.StateActive, true, 0, 1},
	}
	for key, rec := range canonical {
		w, ok := want[key.Activation.DecisionSequence]
		if !ok {
			t.Fatalf("unexpected record for key %v", key)
		}
		if rec.State != w.state || (rec.Origin != nil) != w.origin ||
			rec.MaxRevision != w.max || len(rec.Evidence) != w.slots {
			t.Errorf("decision %d record = %s, want state %s origin %v max %d slots %d",
				key.Activation.DecisionSequence, rec, w.state, w.origin, w.max, w.slots)
		}
	}
	orders := [][]int{identity(len(events)), reversed(len(events))}
	shuffler := &lcg{state: 0x20261122}
	for i := 0; i < 500; i++ {
		orders = append(orders, shuffler.permutation(len(events)))
	}
	for i, order := range orders {
		got := runOrdered(t, events, order)
		if !recordsEqual(canonical, got) {
			t.Fatalf("order %d %v produced %s, want %s", i, order,
				dumpRecords(got), dumpRecords(canonical))
		}
	}
}

// TestRevisionConflictNeverOverwrites checks every same-revision clash shape:
// the second event is a Conflict and the record does not change.
func TestRevisionConflictNeverOverwrites(t *testing.T) {
	cases := []struct {
		name  string
		setup []semantic.SemanticDescriptor
		event semantic.SemanticDescriptor
	}{
		{
			name:  "two applied at revision 0",
			setup: []semantic.SemanticDescriptor{txApplied(1, decisionTx, 1)},
			event: txApplied(2, decisionTx, 1),
		},
		{
			name:  "status revision clash",
			setup: []semantic.SemanticDescriptor{txStatus(5, 1, decisionTx, 1)},
			event: txStatus(5, 2, decisionTx, 1),
		},
		{
			name:  "removed versus status at one revision",
			setup: []semantic.SemanticDescriptor{txStatus(5, 1, decisionTx, 1)},
			event: txRemoved(5, 2, decisionTx, 1),
		},
		{
			name:  "applied variant on the merged tombstone origin",
			setup: []semantic.SemanticDescriptor{txRemoved(4, 1, decisionTx, 1), txApplied(2, decisionTx, 1)},
			event: txApplied(3, decisionTx, 1),
		},
		{
			name:  "entity clash by block hash",
			setup: []semantic.SemanticDescriptor{txApplied(1, decisionTx, 1)},
			event: txApplied(2, decisionTx, 2),
		},
		{
			name:  "entity clash by family",
			setup: []semantic.SemanticDescriptor{txStatus(5, 1, 7, 1)},
			event: optionalEvent(semantic.FamilyBalanceSystem, semantic.KindStatus, 5, 2, 7, 1, 0, 0),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := lifecycle.New()
			for _, setup := range tc.setup {
				mustApply(t, a, setup)
			}
			key := lifecycle.KeyOf(tc.event)
			before := mustRecord(t, a, key)
			res := mustApply(t, a, tc.event)
			if res.Class != lifecycle.ClassConflict {
				t.Errorf("class = %s, want Conflict (reason: %s)", res.Class, res.Reason)
			}
			if res.State != before.State {
				t.Errorf("resulting state = %s, want the unchanged %s", res.State, before.State)
			}
			after := mustRecord(t, a, key)
			if !after.Equal(before) {
				t.Errorf("record changed on conflict: got %s, want the unchanged %s", after, before)
			}
		})
	}
}

// TestStaleEventsMergeWithoutAdvancing checks the stale paths: evidence is
// merged into free revision slots, but the frontier and the lifecycle state
// never move backwards and a terminal record never reactivates.
func TestStaleEventsMergeWithoutAdvancing(t *testing.T) {
	t.Run("older status records in its slot", func(t *testing.T) {
		a := lifecycle.New()
		mustApply(t, a, txStatus(5, 1, decisionTx, 1))
		res := mustApply(t, a, txStatus(2, 2, decisionTx, 1))
		if res.Class != lifecycle.ClassStale {
			t.Errorf("class = %s, want Stale (reason: %s)", res.Class, res.Reason)
		}
		rec := mustRecord(t, a, lifecycle.KeyOf(txStatus(2, 2, decisionTx, 1)))
		if rec.MaxRevision != 5 {
			t.Errorf("max revision = %d, want 5", rec.MaxRevision)
		}
		if _, ok := rec.Evidence[2]; !ok {
			t.Errorf("stale evidence not recorded in slot 2: %s", rec)
		}
		retry := mustApply(t, a, txStatus(2, 2, decisionTx, 1))
		if retry.Class != lifecycle.ClassDuplicate {
			t.Errorf("retry class = %s, want Duplicate", retry.Class)
		}
	})

	t.Run("late applied merges into the tombstone without reactivation", func(t *testing.T) {
		a := lifecycle.New()
		mustApply(t, a, txRemoved(4, 1, decisionTx, 1))
		applied := txApplied(2, decisionTx, 1)
		res := mustApply(t, a, applied)
		if res.Class != lifecycle.ClassStale {
			t.Errorf("class = %s, want Stale (reason: %s)", res.Class, res.Reason)
		}
		rec := mustRecord(t, a, lifecycle.KeyOf(applied))
		if rec.State != lifecycle.StateRemoved {
			t.Errorf("state = %s, want REMOVED", rec.State)
		}
		if rec.Origin == nil {
			t.Fatal("origin not merged into the tombstone")
		}
		if want := digestOf(t, applied); rec.Evidence[0] != want {
			t.Errorf("origin slot digest = %s, want %s", rec.Evidence[0], want)
		}
		late := mustApply(t, a, txStatus(6, 3, decisionTx, 1))
		if late.Class != lifecycle.ClassStale {
			t.Errorf("late status class = %s, want Stale", late.Class)
		}
		if rec2 := mustRecord(t, a, lifecycle.KeyOf(applied)); rec2.State != lifecycle.StateRemoved {
			t.Errorf("state after late status = %s, want REMOVED", rec2.State)
		}
		variant := mustApply(t, a, txApplied(4, decisionTx, 1))
		if variant.Class != lifecycle.ClassConflict {
			t.Errorf("applied variant class = %s, want Conflict", variant.Class)
		}
	})

	t.Run("newer status on a terminal record is stale evidence", func(t *testing.T) {
		a := lifecycle.New()
		mustApply(t, a, txApplied(1, decisionTx, 1))
		mustApply(t, a, txRemoved(5, 2, decisionTx, 1))
		res := mustApply(t, a, txStatus(7, 3, decisionTx, 1))
		if res.Class != lifecycle.ClassStale {
			t.Errorf("class = %s, want Stale (reason: %s)", res.Class, res.Reason)
		}
		rec := mustRecord(t, a, lifecycle.KeyOf(txApplied(1, decisionTx, 1)))
		if rec.State != lifecycle.StateRemoved {
			t.Errorf("state = %s, want REMOVED", rec.State)
		}
		if rec.MaxRevision != 7 {
			t.Errorf("max revision = %d, want 7", rec.MaxRevision)
		}
		retry := mustApply(t, a, txStatus(7, 3, decisionTx, 1))
		if retry.Class != lifecycle.ClassDuplicate {
			t.Errorf("retry class = %s, want Duplicate", retry.Class)
		}
	})
}

// TestDuplicateIsIdempotent checks that retries of recorded events leave the
// record byte-for-byte unchanged.
func TestDuplicateIsIdempotent(t *testing.T) {
	cases := []struct {
		name  string
		setup []semantic.SemanticDescriptor
		event semantic.SemanticDescriptor
	}{
		{
			name:  "applied retry on active",
			setup: []semantic.SemanticDescriptor{txApplied(1, decisionTx, 1)},
			event: txApplied(1, decisionTx, 1),
		},
		{
			name:  "status frontier retry on pending",
			setup: []semantic.SemanticDescriptor{txStatus(3, 2, decisionTx, 1)},
			event: txStatus(3, 2, decisionTx, 1),
		},
		{
			name:  "removed retry on terminal",
			setup: []semantic.SemanticDescriptor{txApplied(1, decisionTx, 1), txRemoved(4, 2, decisionTx, 1)},
			event: txRemoved(4, 2, decisionTx, 1),
		},
		{
			name:  "retry of superseded stale evidence",
			setup: []semantic.SemanticDescriptor{txStatus(5, 1, decisionTx, 1), txStatus(2, 2, decisionTx, 1)},
			event: txStatus(2, 2, decisionTx, 1),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := lifecycle.New()
			for _, setup := range tc.setup {
				mustApply(t, a, setup)
			}
			key := lifecycle.KeyOf(tc.event)
			before := mustRecord(t, a, key)
			res := mustApply(t, a, tc.event)
			if res.Class != lifecycle.ClassDuplicate {
				t.Errorf("class = %s, want Duplicate (reason: %s)", res.Class, res.Reason)
			}
			after := mustRecord(t, a, key)
			if !after.Equal(before) {
				t.Errorf("record changed on duplicate: got %s, want %s", after, before)
			}
		})
	}
}

// TestUnknownRecordsReferenceWithoutInventing checks the unseen-activation
// path: a StatusChanged or Removed reference is recorded, and no applied
// record is invented for it.
func TestUnknownRecordsReferenceWithoutInventing(t *testing.T) {
	t.Run("status reference", func(t *testing.T) {
		a := lifecycle.New()
		event := txStatus(3, 1, decisionTx, 1)
		res := mustApply(t, a, event)
		if res.Class != lifecycle.ClassUnknown {
			t.Errorf("class = %s, want Unknown (reason: %s)", res.Class, res.Reason)
		}
		rec := mustRecord(t, a, lifecycle.KeyOf(event))
		if rec.State != lifecycle.StatePendingApplied {
			t.Errorf("state = %s, want PENDING_APPLIED", rec.State)
		}
		if rec.Origin != nil {
			t.Error("origin invented for a status-only activation")
		}
		if len(rec.Evidence) != 1 {
			t.Errorf("evidence slots = %d, want 1: %s", len(rec.Evidence), rec)
		}
	})

	t.Run("removed reference keeps the tombstone meaning", func(t *testing.T) {
		a := lifecycle.New()
		removed := txRemoved(5, 1, decisionTx, 1)
		res := mustApply(t, a, removed)
		if res.Class != lifecycle.ClassUnknown {
			t.Errorf("class = %s, want Unknown (reason: %s)", res.Class, res.Reason)
		}
		rec := mustRecord(t, a, lifecycle.KeyOf(removed))
		if rec.State != lifecycle.StateRemoved {
			t.Errorf("state = %s, want REMOVED", rec.State)
		}
		if rec.Origin != nil {
			t.Error("origin invented for a tombstone-only activation")
		}
		applied := txApplied(2, decisionTx, 1)
		late := mustApply(t, a, applied)
		if late.Class != lifecycle.ClassStale {
			t.Errorf("late applied class = %s, want Stale", late.Class)
		}
		rec = mustRecord(t, a, lifecycle.KeyOf(removed))
		if rec.State != lifecycle.StateRemoved {
			t.Errorf("state after the late applied = %s, want REMOVED", rec.State)
		}
		if rec.Origin == nil {
			t.Error("late applied not merged as the tombstone origin")
		}
	})
}

// TestReinclusionCreatesNewActivation checks that a re-included entity gets
// a new activation record while the removed one stays terminal and immutable.
func TestReinclusionCreatesNewActivation(t *testing.T) {
	a := lifecycle.New()
	// First inclusion: transaction txHash in block 1, decision 10.
	first := txApplied(1, 10, 1)
	mustApply(t, a, first)
	mustApply(t, a, txRemoved(4, 2, 10, 1))
	oldKey := lifecycle.KeyOf(first)
	oldRecord := mustRecord(t, a, oldKey)

	// Reinclusion: the same transaction hash in block 2 under a new decision
	// sequence 22 (CONTEXT.md "Inclusion": another inclusion).
	second := txApplied(3, 22, 2)
	res := mustApply(t, a, second)
	if res.Class != lifecycle.ClassAccepted {
		t.Errorf("reinclusion applied class = %s, want Accepted (reason: %s)", res.Class, res.Reason)
	}
	secondKey := lifecycle.KeyOf(second)
	if secondKey == oldKey {
		t.Fatal("reinclusion reused the old activation key")
	}
	if rec := mustRecord(t, a, secondKey); rec.State != lifecycle.StateActive {
		t.Errorf("reinclusion state = %s, want ACTIVE", rec.State)
	}

	// A third inclusion of the same transaction at another body position is
	// another activation too (entity ordinal 2 from body index 1).
	third, err := semantic.NewTransactionDescriptor(semantic.KindApplied, net1, producer1, run1,
		23, h32(3), txHash, 1, 0, source(4))
	if err != nil {
		t.Fatalf("third inclusion event: %v", err)
	}
	thirdKey := lifecycle.KeyOf(third)
	res = mustApply(t, a, third)
	if res.Class != lifecycle.ClassAccepted {
		t.Errorf("third inclusion class = %s, want Accepted", res.Class)
	}
	if thirdKey == oldKey || thirdKey == secondKey {
		t.Fatal("inclusions at different positions must not share keys")
	}
	if a.Len() != 3 {
		t.Errorf("records = %d, want 3", a.Len())
	}

	// Lifecycle churn of the new activations leaves the old record untouched.
	mustApply(t, a, txStatus(6, 5, 22, 2))
	mustApply(t, a, txRemoved(7, 6, 22, 2))
	if rec := mustRecord(t, a, oldKey); !rec.Equal(oldRecord) {
		t.Errorf("removed activation changed after reinclusion: got %s, want %s", rec, oldRecord)
	}
}

// TestTerminalImmutabilityAfterRemoved applies a battery of post-terminal
// events and requires the removed record to keep its state, origin, entity
// and already-recorded slots.
func TestTerminalImmutabilityAfterRemoved(t *testing.T) {
	a := lifecycle.New()
	applied := txApplied(1, decisionTx, 1)
	mustApply(t, a, applied)
	mustApply(t, a, txStatus(2, 2, decisionTx, 1))
	mustApply(t, a, txRemoved(5, 3, decisionTx, 1))
	key := lifecycle.KeyOf(applied)
	snapshot := mustRecord(t, a, key)
	originDigest := digestOf(t, applied)

	battery := []struct {
		name  string
		event semantic.SemanticDescriptor
		want  lifecycle.Class
	}{
		{"applied redelivery", txApplied(1, decisionTx, 1), lifecycle.ClassDuplicate},
		{"applied variant", txApplied(9, decisionTx, 1), lifecycle.ClassConflict},
		{"older status", txStatus(1, 4, decisionTx, 1), lifecycle.ClassStale},
		{"newer status", txStatus(7, 5, decisionTx, 1), lifecycle.ClassStale},
		{"second removed", txRemoved(6, 6, decisionTx, 1), lifecycle.ClassStale},
		{"removed redelivery", txRemoved(5, 3, decisionTx, 1), lifecycle.ClassDuplicate},
		{"status variant of a recorded slot", txStatus(2, 8, decisionTx, 1), lifecycle.ClassConflict},
	}
	for _, step := range battery {
		t.Run(step.name, func(t *testing.T) {
			res := mustApply(t, a, step.event)
			if res.Class != step.want {
				t.Errorf("class = %s, want %s (reason: %s)", res.Class, step.want, res.Reason)
			}
			rec := mustRecord(t, a, key)
			if rec.State != lifecycle.StateRemoved {
				t.Errorf("state = %s, want REMOVED", rec.State)
			}
			if rec.Origin == nil || rec.Evidence[0] != originDigest {
				t.Error("applied origin changed after removal")
			}
			if rec.Entity != snapshot.Entity {
				t.Error("entity signature changed after removal")
			}
			for rev, digest := range snapshot.Evidence {
				if got, ok := rec.Evidence[rev]; !ok || got != digest {
					t.Errorf("recorded slot %d changed: got %s, want %s", rev, got, digest)
				}
			}
		})
	}
	final := mustRecord(t, a, key)
	if final.MaxRevision != 7 {
		t.Errorf("final max revision = %d, want 7", final.MaxRevision)
	}
	wantSlots := []uint64{0, 1, 2, 5, 6, 7}
	if len(final.Evidence) != len(wantSlots) {
		t.Errorf("final evidence slots = %d, want %d: %s", len(final.Evidence), len(wantSlots), final)
	}
	for _, rev := range wantSlots {
		if _, ok := final.Evidence[rev]; !ok {
			t.Errorf("final evidence misses revision %d: %s", rev, final)
		}
	}
}

// TestIndependentObservationsDoNotMix checks the contract note that
// independent producers, runs and networks keep separate activation domains.
func TestIndependentObservationsDoNotMix(t *testing.T) {
	a := lifecycle.New()
	first := txApplied(1, decisionTx, 1)
	mustApply(t, a, first)

	// Another producer and run observing the same inclusion.
	otherRun, err := semantic.NewTransactionDescriptor(semantic.KindApplied, net1, producer2, run2,
		decisionTx, h32(1), txHash, 0, 0, source(2))
	if err != nil {
		t.Fatalf("other run event: %v", err)
	}
	res := mustApply(t, a, otherRun)
	if res.Class != lifecycle.ClassAccepted {
		t.Errorf("other run class = %s, want Accepted", res.Class)
	}
	if a.Len() != 2 {
		t.Errorf("records = %d, want 2", a.Len())
	}

	// The same producer and run on another network.
	otherNet, err := semantic.NewTransactionDescriptor(semantic.KindApplied, net2, producer1, run1,
		decisionTx, h32(1), txHash, 0, 0, source(3))
	if err != nil {
		t.Fatalf("other network event: %v", err)
	}
	res = mustApply(t, a, otherNet)
	if res.Class != lifecycle.ClassAccepted {
		t.Errorf("other network class = %s, want Accepted", res.Class)
	}
	if a.Len() != 3 {
		t.Errorf("records = %d, want 3", a.Len())
	}
	if key := lifecycle.KeyOf(first); key == lifecycle.KeyOf(otherRun) || key == lifecycle.KeyOf(otherNet) {
		t.Error("independent observations must not share one activation key")
	}
}

// TestInvalidDescriptorsAreInputErrors checks that descriptors rejected by
// protocol/semantic are caller errors, not outcomes, and change nothing.
func TestInvalidDescriptorsAreInputErrors(t *testing.T) {
	broken := []struct {
		name  string
		event semantic.SemanticDescriptor
	}{
		{"applied with revision", func() semantic.SemanticDescriptor {
			d := txApplied(1, decisionTx, 1)
			d.Revision = 1
			return d
		}()},
		{"status without revision", func() semantic.SemanticDescriptor {
			d := txStatus(3, 2, decisionTx, 1)
			d.Revision = 0
			return d
		}()},
		{"removed without revision", func() semantic.SemanticDescriptor {
			d := txRemoved(5, 3, decisionTx, 1)
			d.Revision = 0
			return d
		}()},
		{"unset producer", func() semantic.SemanticDescriptor {
			d := txApplied(1, decisionTx, 1)
			d.Activation.ProducerID = semantic.ID16{}
			return d
		}()},
		{"unset source sequence", func() semantic.SemanticDescriptor {
			d := txApplied(1, decisionTx, 1)
			d.Source.SourceSequence = 0
			return d
		}()},
		{"transaction without inclusion binding", func() semantic.SemanticDescriptor {
			d := txApplied(1, decisionTx, 1)
			d.Activation.Transaction = nil
			return d
		}()},
	}
	for _, tc := range broken {
		t.Run(tc.name, func(t *testing.T) {
			a := lifecycle.New()
			// The digest argument is irrelevant here: the descriptor itself
			// is invalid, so any non-zero placeholder content digest does.
			_, err := a.Apply(tc.event, lifecycle.Digest{0x01})
			if err == nil {
				t.Fatal("apply accepted an invalid descriptor")
			}
			if !errors.Is(err, semantic.ErrInvalid) {
				t.Errorf("error = %v, want a semantic.ErrInvalid chain", err)
			}
			if a.Len() != 0 {
				t.Errorf("records = %d, want 0", a.Len())
			}
		})
	}
}

// TestRecordValueSemantics checks that returned records are detached copies
// and pins the dump formats used by failure reports.
func TestRecordValueSemantics(t *testing.T) {
	a := lifecycle.New()
	applied := txApplied(1, decisionTx, 1)
	res := mustApply(t, a, applied)
	key := lifecycle.KeyOf(applied)

	if _, ok := a.Record(lifecycle.ActivationKey{Network: net2}); ok {
		t.Error("Record returned a record for an unseen key")
	}

	// Mutating a returned record must not touch the automaton.
	got := mustRecord(t, a, key)
	got.Evidence[9] = lifecycle.Digest{}
	got.Origin = nil
	got.State = lifecycle.StatePendingApplied
	fresh := mustRecord(t, a, key)
	if fresh.State != lifecycle.StateActive || fresh.Origin == nil {
		t.Errorf("stored record changed through a returned copy: %s", fresh)
	}
	if _, polluted := fresh.Evidence[9]; polluted {
		t.Error("returned evidence map aliases the stored map")
	}
	all := a.Records()
	all[key].Evidence[9] = lifecycle.Digest{}
	if _, polluted := mustRecord(t, a, key).Evidence[9]; polluted {
		t.Error("Records returned maps that alias the stored records")
	}

	// Equal separates records that differ in state, origin, entity or slots.
	pending := txStatus(3, 2, decisionTx, 1)
	other := lifecycle.New()
	mustApply(t, other, pending)
	otherRec := mustRecord(t, other, lifecycle.KeyOf(pending))
	if fresh.Equal(otherRec) {
		t.Error("records of different states compared equal")
	}
	held := fresh.Clone()
	held.Origin = nil
	if fresh.Equal(held) {
		t.Error("records with different origins compared equal")
	}
	held = fresh.Clone()
	held.MaxRevision = 8
	if fresh.Equal(held) {
		t.Error("records with different frontiers compared equal")
	}
	held = fresh.Clone()
	held.Entity.BlockHash = h32(9)
	if fresh.Equal(held) {
		t.Error("records with different entities compared equal")
	}
	held = fresh.Clone()
	held.Evidence[3] = lifecycle.Digest{}
	if fresh.Equal(held) {
		t.Error("records with different evidence slots compared equal")
	}

	// The dump formats are stable for reports.
	digest := digestOf(t, applied)
	wantRecord := fmt.Sprintf("ACTIVE origin=%s max=0 evidence={0:%s}", digest, digest)
	if got := fresh.String(); got != wantRecord {
		t.Errorf("record dump = %q, want %q", got, wantRecord)
	}
	wantResult := fmt.Sprintf("Accepted rev=0 -> ACTIVE: %s", res.Reason)
	if got := res.String(); got != wantResult {
		t.Errorf("result dump = %q, want %q", got, wantResult)
	}
	pendingRec := mustRecord(t, other, lifecycle.KeyOf(pending))
	wantPending := fmt.Sprintf("PENDING_APPLIED origin=- max=3 evidence={3:%s}",
		digestOf(t, pending))
	if got := pendingRec.String(); got != wantPending {
		t.Errorf("pending record dump = %q, want %q", got, wantPending)
	}
}

// TestOutcomeVocabularyNamesAreStable pins the names used by reports.
func TestOutcomeVocabularyNamesAreStable(t *testing.T) {
	classes := map[lifecycle.Class]string{
		lifecycle.ClassAccepted:  "Accepted",
		lifecycle.ClassDuplicate: "Duplicate",
		lifecycle.ClassConflict:  "Conflict",
		lifecycle.ClassStale:     "Stale",
		lifecycle.ClassUnknown:   "Unknown",
	}
	for class, want := range classes {
		if got := class.String(); got != want {
			t.Errorf("class name = %q, want %q", got, want)
		}
	}
	states := map[lifecycle.State]string{
		lifecycle.StatePendingApplied: "PENDING_APPLIED",
		lifecycle.StateActive:         "ACTIVE",
		lifecycle.StateRemoved:        "REMOVED",
	}
	for state, want := range states {
		if got := state.String(); got != want {
			t.Errorf("state name = %q, want %q", got, want)
		}
	}
}
