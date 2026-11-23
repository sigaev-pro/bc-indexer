package registry

import (
	"encoding/hex"
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
	lifecycle "github.com/sigaev-pro/bc-indexer/test/lifecycle_validator"
)

// digest32 decodes the hex content digest of an event into the Digest type
// of the E02-03 test oracle.
func digest32(value string) lifecycle.Digest {
	var out lifecycle.Digest
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != len(out) {
		panic("bad content digest")
	}
	copy(out[:], raw)
	return out
}

// scenario builds the event stream of one producer run through the
// registry: a speculative SIDE execution that publishes nothing, an import,
// a finality status, a canonical reorganization over a retained speculative
// corpus, and a re-inclusion of the removed block.
func scenario(t *testing.T, r *Registry) {
	t.Helper()
	side := settleCanonical(t, r, run1, 0, KindSide)
	if err := r.CaptureCorpus(side, blockB, corpusDigestOf(blockB)); err != nil {
		t.Fatalf("capture speculative corpus: %v", err)
	}
	applyBlock(t, r, run1, 1, blockA)

	finality := settleCanonical(t, r, run1, 2, KindFinality)
	status := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindStatus, blockA, 1, 1, 22)}}
	if _, err := r.Apply(finality, status); err != nil {
		t.Fatalf("finality status: %v", err)
	}

	reorg := settleCanonical(t, r, run1, 3, KindCanonical)
	reorgPlan := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindRemoved, blockA, 1, 2, 31),
		txEntityEvent(run1, semantic.KindRemoved, blockA, txHash, 1, 0, 1, 32),
		entityEvent(run1, semantic.KindApplied, blockB, 3, 0, 33)}}
	if _, err := r.Apply(reorg, reorgPlan); err != nil {
		t.Fatalf("reorg: %v", err)
	}

	again := settleCanonical(t, r, run1, 4, KindCanonical)
	againPlan := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindRemoved, blockB, 3, 1, 41),
		entityEvent(run1, semantic.KindApplied, blockA, 4, 0, 42)}}
	if _, err := r.Apply(again, againPlan); err != nil {
		t.Fatalf("re-inclusion: %v", err)
	}
}

// foldAll feeds the events into a fresh automaton of the E02-03 oracle.
func foldAll(t *testing.T, events []PublishedEvent) *lifecycle.Automaton {
	t.Helper()
	a := lifecycle.New()
	for _, ev := range events {
		if _, err := a.Apply(ev.Descriptor, digest32(ev.Content)); err != nil {
			t.Fatalf("oracle rejected %s: %v", ev.Descriptor.Kind, err)
		}
	}
	return a
}

func dumpAutomaton(a *lifecycle.Automaton) []string {
	records := a.Records()
	lines := make([]string, 0, len(records))
	for key, rec := range records {
		lines = append(lines, fmt.Sprintf("%+v -> %s", key, rec.String()))
	}
	sort.Strings(lines)
	return lines
}

// TestValidatorOracleConvergence is the E02-03 dependency check: the events
// the registry publishes fold through the semantic lifecycle oracle into one
// unambiguous record set in every delivery order, Removed stays terminal,
// and the speculative SIDE execution contributes no event at all.
func TestValidatorOracleConvergence(t *testing.T) {
	r := open(t, newStore())
	scenario(t, r)
	events, err := r.Events(&net1)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 8 {
		t.Fatalf("events %d, want 8", len(events))
	}
	for _, ev := range events {
		if ev.Descriptor.Activation.ActivationID.DecisionSequence == 0 {
			t.Fatalf("speculative SIDE decision published %s", ev.Descriptor.Kind)
		}
	}

	reference := foldAll(t, events)
	want := dumpAutomaton(reference)

	random := rand.New(rand.NewSource(1))
	shuffled := false
	for round := 0; round < 100; round++ {
		order := random.Perm(len(events))
		if round == 1 {
			shuffled = true
		}
		reordered := make([]PublishedEvent, len(events))
		for i, j := range order {
			reordered[i] = events[j]
		}
		if got := dumpAutomaton(foldAll(t, reordered)); !sort.StringsAreSorted(got) || !equalLines(got, want) {
			t.Fatalf("round %d: fold diverged:\n got %v\nwant %v", round, got, want)
		}
	}
	if !shuffled {
		t.Fatal("no shuffle ran")
	}

	// The terminal and re-included records are what the contract names.
	expected := []struct {
		event   EventPlan
		state   lifecycle.State
		removed uint64
	}{
		{entityEvent(run1, semantic.KindRemoved, blockA, 1, 2, 31), lifecycle.StateRemoved, 2},
		{txEntityEvent(run1, semantic.KindRemoved, blockA, txHash, 1, 0, 1, 32), lifecycle.StateRemoved, 1},
		{entityEvent(run1, semantic.KindRemoved, blockB, 3, 1, 41), lifecycle.StateRemoved, 1},
		{entityEvent(run1, semantic.KindApplied, blockA, 4, 0, 42), lifecycle.StateActive, 0},
	}
	for _, want := range expected {
		rec, ok := reference.Record(lifecycle.KeyOf(want.event.Descriptor))
		if !ok {
			t.Fatalf("oracle has no record of %s", want.event.Descriptor.Kind)
		}
		if rec.State != want.state || rec.RemovedRevision != want.removed {
			t.Fatalf("record is %s, want %s removed=%d", rec.State, want.state, want.removed)
		}
	}
	if reference.Len() != 4 {
		t.Fatalf("oracle holds %d records, want 4", reference.Len())
	}
}

func equalLines(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
