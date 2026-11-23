package registry

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// Fixtures: one network, one node instance, writer epoch and producer, with
// distinct byte patterns so every identity stays unambiguous.
var (
	net1      = semantic.Network{ChainID: u256(1), GenesisHash: h32(1)}
	otherNet  = semantic.Network{ChainID: u256(2), GenesisHash: h32(2)}
	node1     = id16(0x33)
	producer1 = id16(0x11)
	run1      = id16(0x22)
	epoch1    = id16(0x44)
	blockA    = h32(0xA1)
	blockB    = h32(0xA2)
	blockC    = h32(0xA3)
	txHash    = h32(0xA7)
)

func u256(n byte) semantic.UInt256 {
	var out semantic.UInt256
	out[31] = n
	return out
}

func h32(n byte) semantic.Hash32 {
	var out semantic.Hash32
	for i := range out {
		out[i] = n + byte(i)
	}
	return out
}

func id16(n byte) semantic.ID16 {
	var out semantic.ID16
	for i := range out {
		out[i] = n + byte(i)
	}
	return out
}

func dref(run semantic.ID16, sequence uint64) NativeDecisionRef {
	return NativeDecisionRef{NodeInstanceID: node1, ProducerID: producer1, RunID: run,
		DecisionSequence: sequence}
}

func source(sequence uint64) semantic.SourceRef {
	return semantic.SourceRef{NodeInstanceID: node1, WriterEpoch: epoch1, SourceSequence: sequence}
}

func mustDescriptor(run semantic.ID16, kind semantic.Kind, block semantic.Hash32, decision,
	revision, sourceSequence uint64) semantic.SemanticDescriptor {
	d, err := semantic.NewBlockDescriptor(kind, net1, producer1, run, decision, block, revision,
		source(sourceSequence))
	if err != nil {
		panic(err)
	}
	return d
}

// entityEvent builds one block-entity event whose descriptor targets the
// given decision entity.
func entityEvent(run semantic.ID16, kind semantic.Kind, block semantic.Hash32, decision, revision,
	sourceSequence uint64) EventPlan {
	d := mustDescriptor(run, kind, block, decision, revision, sourceSequence)
	digest, err := d.Digest()
	if err != nil {
		panic(err)
	}
	return EventPlan{Descriptor: d, Content: digest.String()}
}

// txEntityEvent builds one transaction-entity event of a decision.
func txEntityEvent(run semantic.ID16, kind semantic.Kind, block, tx semantic.Hash32, decision,
	bodyIndex, revision, sourceSequence uint64) EventPlan {
	d, err := semantic.NewTransactionDescriptor(kind, net1, producer1, run, decision, block, tx,
		bodyIndex, revision, source(sourceSequence))
	if err != nil {
		panic(err)
	}
	digest, err := d.Digest()
	if err != nil {
		panic(err)
	}
	return EventPlan{Descriptor: d, Content: digest.String()}
}

func corpusDigestOf(block semantic.Hash32) string { return h32(block[0] ^ 0x5A).String() }

func intentDigestOf(sequence uint64) string { return h32(byte(sequence) ^ 0xA5).String() }

func blockKeyOf(run semantic.ID16, sequence uint64) ActivationKey {
	return ActivationKey{Network: net1, ProducerID: producer1, RunID: run,
		Activation: semantic.ActivationID{DecisionSequence: sequence, EntityOrdinal: 0}}
}

func open(t *testing.T, db *memStore) *Registry {
	t.Helper()
	r, _, err := Open(db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return r
}

func eventLines(t *testing.T, r *Registry) []string {
	t.Helper()
	events, err := r.Events(&net1)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, fmt.Sprintf("%s %s %d", ev.Descriptor.Kind, ev.Descriptor.Activation.BlockHash,
			ev.Descriptor.Revision))
	}
	return out
}

// settleCanonical intends a decision of the kind, settles it as decided and
// returns its reference.
func settleCanonical(t *testing.T, r *Registry, run semantic.ID16, sequence uint64, kind DecisionKind,
) NativeDecisionRef {
	t.Helper()
	ref := dref(run, sequence)
	if err := r.Intend(ref, net1, kind, intentDigestOf(sequence)); err != nil {
		t.Fatalf("intend %s: %v", ref, err)
	}
	if err := r.SettleDecided(ref); err != nil {
		t.Fatalf("settle %s: %v", ref, err)
	}
	return ref
}

// importPlan is the application of an IMPORT decision over one block: a
// block entity and one transaction entity.
func importPlan(run semantic.ID16, sequence uint64, block semantic.Hash32) ApplicationPlan {
	return ApplicationPlan{Events: []EventPlan{
		entityEvent(run, semantic.KindApplied, block, sequence, 0, 10*sequence+1),
		txEntityEvent(run, semantic.KindApplied, block, txHash, sequence, 0, 0, 10*sequence+2),
	}}
}

// applyBlock captures the corpus of one block and applies an IMPORT decision
// over it.
func applyBlock(t *testing.T, r *Registry, run semantic.ID16, sequence uint64,
	block semantic.Hash32) ApplyResult {
	t.Helper()
	ref := settleCanonical(t, r, run, sequence, KindImport)
	if err := r.CaptureCorpus(ref, block, corpusDigestOf(block)); err != nil {
		t.Fatalf("capture corpus of %s: %v", block, err)
	}
	result, err := r.Apply(ref, importPlan(run, sequence, block))
	if err != nil {
		t.Fatalf("apply %s: %v", ref, err)
	}
	return result
}

func activationRecords(snapshot []string) int {
	count := 0
	for _, line := range snapshot {
		if strings.HasPrefix(line, keyPrefix+"activation/") {
			count++
		}
	}
	return count
}

func TestDecisionMirrorTransitions(t *testing.T) {
	r := open(t, newStore())
	ref := dref(run1, 0)
	if err := r.Intend(ref, net1, KindImport, intentDigestOf(0)); err != nil {
		t.Fatalf("intend: %v", err)
	}
	// A repeated identical intent is idempotent; a different one conflicts.
	if err := r.Intend(ref, net1, KindImport, intentDigestOf(0)); err != nil {
		t.Fatalf("repeat intend: %v", err)
	}
	if err := r.Intend(ref, net1, KindCanonical, intentDigestOf(9)); err == nil {
		t.Fatal("conflicting intent accepted")
	}
	if err := r.Intend(ref, otherNet, KindImport, intentDigestOf(0)); err == nil {
		t.Fatal("second network of one run accepted")
	}
	next := dref(run1, 1)
	if err := r.Intend(next, net1, KindImport, intentDigestOf(1)); err != nil {
		t.Fatalf("intend next: %v", err)
	}
	if err := r.Intend(dref(run1, 0), net1, KindFinality, intentDigestOf(7)); err == nil {
		t.Fatal("sequence out of order accepted")
	}
	if err := r.SettleDecided(ref); err != nil {
		t.Fatalf("settle decided: %v", err)
	}
	if err := r.SettleDecided(ref); err != nil {
		t.Fatalf("repeat settle decided: %v", err)
	}
	if err := r.SettleRejected(ref); err == nil {
		t.Fatal("rejected a decided decision")
	}
	if err := r.SettleDecided(dref(run1, 9)); err == nil {
		t.Fatal("settled an unknown decision")
	}
	if _, ok, err := r.Decision(dref(run1, 9)); err != nil || ok {
		t.Fatalf("unknown decision: %v %v", ok, err)
	}
}

func TestApplyPublishesWholeDecision(t *testing.T) {
	r := open(t, newStore())
	result := applyBlock(t, r, run1, 0, blockA)
	if len(result.Applied) != 2 {
		t.Fatalf("applied %v, want the block and the tx entity", result.Applied)
	}
	if events := eventLines(t, r); len(events) != 2 {
		t.Fatalf("events %v, want 2", events)
	}
	block, ok, err := r.Activation(blockKeyOf(run1, 0))
	if err != nil || !ok {
		t.Fatalf("block activation: %v %v", ok, err)
	}
	if block.State != ActivationActive || block.MaxRevision != 0 {
		t.Fatalf("block record is %+v", block)
	}
	if block.CorpusDigest != corpusDigestOf(blockA) {
		t.Fatalf("corpus digest %s", block.CorpusDigest)
	}
	if block.Speculative {
		t.Fatal("corpus captured under the applying decision marked speculative")
	}
}

func TestSpeculativeCorpusNeverBecomesApplied(t *testing.T) {
	r := open(t, newStore())
	// A speculative execution of a side branch: the corpus exists, but the
	// branch was not accepted as canonical (AC-LIF-01-01).
	side := settleCanonical(t, r, run1, 0, KindSide)
	if err := r.CaptureCorpus(side, blockB, corpusDigestOf(blockB)); err != nil {
		t.Fatalf("capture: %v", err)
	}
	sidePlan := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindApplied, blockB, 0, 0, 1)}}
	if _, err := r.Apply(side, sidePlan); !errors.Is(err, ErrNotCanonical) {
		t.Fatalf("SIDE application: %v", err)
	}

	// A rejected decision never applies: execution success does not
	// substitute a canonical decision.
	rejected := dref(run1, 1)
	if err := r.Intend(rejected, net1, KindImport, intentDigestOf(1)); err != nil {
		t.Fatalf("intend: %v", err)
	}
	if err := r.SettleRejected(rejected); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if err := r.CaptureCorpus(rejected, blockC, corpusDigestOf(blockC)); err != nil {
		t.Fatalf("capture: %v", err)
	}
	rejectedPlan := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindApplied, blockC, 1, 0, 2)}}
	if _, err := r.Apply(rejected, rejectedPlan); !errors.Is(err, ErrRejected) {
		t.Fatalf("rejected application: %v", err)
	}
	if events := eventLines(t, r); len(events) != 0 {
		t.Fatalf("speculative corpus published %v", events)
	}
	if _, ok, err := r.Activation(blockKeyOf(run1, 1)); err != nil || ok {
		t.Fatalf("rejected activation exists: %v %v", ok, err)
	}
	if _, ok, err := r.Corpus(producer1, run1, blockC); err != nil || !ok {
		t.Fatalf("rejected corpus lost: %v %v", ok, err)
	}

	// The known branch becomes canonical later without a new execution: a
	// new decision sequence reuses the retained speculative corpus.
	canonical := settleCanonical(t, r, run1, 2, KindCanonical)
	reuse := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindApplied, blockB, 2, 0, 3)}}
	result, err := r.Apply(canonical, reuse)
	if err != nil {
		t.Fatalf("canonical reuse of the speculative corpus: %v", err)
	}
	if len(result.Applied) != 1 {
		t.Fatalf("applied %v", result.Applied)
	}
	rec, ok, err := r.Activation(blockKeyOf(run1, 2))
	if err != nil || !ok {
		t.Fatalf("activation: %v %v", ok, err)
	}
	if !rec.Speculative {
		t.Fatal("the reused corpus was not recorded as speculative")
	}
}

func TestDeferredApplicationRequiresTargetedReplay(t *testing.T) {
	r := open(t, newStore())
	// The known branch becomes canonical without a new execution callback
	// and the corpus is absent (AC-LIF-01-02).
	ref := settleCanonical(t, r, run1, 0, KindCanonical)
	plan := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindApplied, blockB, 0, 0, 1)}}
	result, err := r.Apply(ref, plan)
	if !IsCorpusReplay(err) {
		t.Fatalf("missing corpus applied anyway: %v", err)
	}
	if !result.Deferred || len(result.Blocks) != 1 || result.Blocks[0] != blockB {
		t.Fatalf("deferral is %+v", result)
	}
	if events := eventLines(t, r); len(events) != 0 {
		t.Fatalf("deferred decision published %v", events)
	}
	d, ok, err := r.Decision(ref)
	if err != nil || !ok || d.State != StateDecided || d.Application == nil {
		t.Fatalf("deferred decision is %+v %v %v", d, ok, err)
	}

	// A later decision cannot jump the deferred one.
	later := settleCanonical(t, r, run1, 1, KindCanonical)
	laterPlan := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindApplied, blockC, 1, 0, 2)}}
	if _, err := r.Apply(later, laterPlan); !errors.Is(err, ErrDecisionOrder) {
		t.Fatalf("jumped the deferred decision: %v", err)
	}

	// The replay is targeted: only the block the deferred application needs.
	if err := r.ReplayCorpus(ref, blockC, corpusDigestOf(blockC)); !errors.Is(err,
		ErrUnknownReplayTarget) {
		t.Fatalf("unneeded replay accepted: %v", err)
	}
	if err := r.ReplayCorpus(dref(run1, 9), blockB, corpusDigestOf(blockB)); !errors.Is(err,
		ErrUnknownDecision) {
		t.Fatalf("unknown replay decision: %v", err)
	}
	other := settleCanonical(t, r, run1, 2, KindImport)
	if err := r.ReplayCorpus(other, blockB, corpusDigestOf(blockB)); !errors.Is(err,
		ErrUnknownReplayTarget) {
		t.Fatalf("replay without a stored application accepted: %v", err)
	}
	if err := r.ReplayCorpus(ref, blockB, corpusDigestOf(blockB)); err != nil {
		t.Fatalf("targeted replay: %v", err)
	}
	corpus, ok, err := r.Corpus(producer1, run1, blockB)
	if err != nil || !ok || corpus.ReplayedBy == nil {
		t.Fatalf("replayed corpus is %+v %v %v", corpus, ok, err)
	}

	// The same application now completes.
	result, err = r.Apply(ref, plan)
	if err != nil {
		t.Fatalf("apply after replay: %v", err)
	}
	if len(result.Applied) != 1 {
		t.Fatalf("applied %v", result.Applied)
	}
	// The replay ran under the canonical decision itself, after it accepted
	// the branch, so the corpus is not speculative.
	rec, ok, err := r.Activation(blockKeyOf(run1, 0))
	if err != nil || !ok || rec.State != ActivationActive || rec.Speculative {
		t.Fatalf("replayed activation is %+v %v %v", rec, ok, err)
	}

	// The later decision follows.
	if err := r.CaptureCorpus(later, blockC, corpusDigestOf(blockC)); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if _, err := r.Apply(later, laterPlan); err != nil {
		t.Fatalf("apply after the deferred decision closed: %v", err)
	}
}

// buildForCrash runs the whole intend, settle, capture and apply sequence of
// one block import; with a failing sync it stops at the simulated power
// loss.
func buildForCrash(db *memStore) error {
	r, _, err := Open(db)
	if err != nil {
		return err
	}
	ref := dref(run1, 0)
	if err := r.Intend(ref, net1, KindImport, intentDigestOf(0)); err != nil {
		return err
	}
	if err := r.SettleDecided(ref); err != nil {
		return err
	}
	if err := r.CaptureCorpus(ref, blockA, corpusDigestOf(blockA)); err != nil {
		return err
	}
	_, err = r.Apply(ref, importPlan(run1, 0, blockA))
	return err
}

func TestPowerLossReconciliationCompletesAtomically(t *testing.T) {
	// The golden final state of an uninterrupted application.
	golden := newStore()
	if err := buildForCrash(golden); err != nil {
		t.Fatalf("golden build: %v", err)
	}
	wantRecords := golden.snapshot()
	wantEvents := eventLines(t, open(t, golden))
	totalSyncs := golden.syncs

	// Every crash point of the same sequence, with every subset of the
	// unsynced writes kept, converges: the application is either whole or
	// absent, never partial (AC-CAP-07-04).
	for failAt := 0; failAt <= totalSyncs; failAt++ {
		for keep := uint(0); keep < 2; keep++ {
			db := newStore()
			db.failAt = failAt
			err := buildForCrash(db)
			if failAt != 0 && failAt < totalSyncs && !errors.Is(err, errCrash) {
				t.Fatalf("failAt %d: build stopped with %v", failAt, err)
			}
			survived := db.powerLoss(keep)
			restarted := open(t, survived)
			d, ok, err := restarted.Decision(dref(run1, 0))
			if err != nil || !ok {
				t.Fatalf("failAt %d keep %d: decision %v %v", failAt, keep, ok, err)
			}
			if d.State == StateApplied {
				if !slices.Equal(survived.snapshot(), wantRecords) {
					t.Fatalf("failAt %d keep %d: records differ from golden", failAt, keep)
				}
				if !slices.Equal(eventLines(t, restarted), wantEvents) {
					t.Fatalf("failAt %d keep %d: events differ from golden", failAt, keep)
				}
				continue
			}
			if events := eventLines(t, restarted); len(events) != 0 {
				t.Fatalf("failAt %d keep %d: partial events %v", failAt, keep, events)
			}
			if count := activationRecords(survived.snapshot()); count != 0 {
				t.Fatalf("failAt %d keep %d: %d partial activations", failAt, keep, count)
			}
		}
	}
}

func TestMarkerRepeatAfterRestartSuppressesSecondApplication(t *testing.T) {
	db := newStore()
	applyBlock(t, open(t, db), run1, 0, blockA)
	before := eventLines(t, open(t, db))
	restarted := open(t, db.powerLoss(0))

	// The native decision marker repeats after its cleanup was interrupted
	// (AC-CAP-07-05): every step is idempotent.
	ref := dref(run1, 0)
	if err := restarted.Intend(ref, net1, KindImport, intentDigestOf(0)); err != nil {
		t.Fatalf("repeat intend: %v", err)
	}
	if err := restarted.SettleDecided(ref); err != nil {
		t.Fatalf("repeat settle: %v", err)
	}
	result, err := restarted.Apply(ref, importPlan(run1, 0, blockA))
	if err != nil {
		t.Fatalf("repeat apply: %v", err)
	}
	if !result.Duplicate || len(result.Applied) != 0 {
		t.Fatalf("second application happened: %+v", result)
	}
	if after := eventLines(t, restarted); !slices.Equal(after, before) {
		t.Fatalf("events changed: %v, want %v", after, before)
	}
	if revision, err := restarted.NextRevision(blockKeyOf(run1, 0)); err != nil || revision != 1 {
		t.Fatalf("revision frontier %d %v", revision, err)
	}

	// A different plan of an applied decision is refused.
	different := importPlan(run1, 0, blockA)
	different.Events[0].Content = corpusDigestOf(blockC)
	if _, err := restarted.Apply(ref, different); !errors.Is(err, ErrAlreadyApplied) {
		t.Fatalf("different plan accepted: %v", err)
	}
	if after := eventLines(t, restarted); !slices.Equal(after, before) {
		t.Fatalf("events changed: %v, want %v", after, before)
	}
}

func TestReadyAppliedStaysCancellable(t *testing.T) {
	r := open(t, newStore())
	applyBlock(t, r, run1, 0, blockA)
	key := blockKeyOf(run1, 0)
	if err := r.MarkReady(key); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if err := r.MarkAcked(key); err != nil {
		t.Fatalf("mark acked: %v", err)
	}

	// A canonical decision removes the READY, acknowledged activation: a
	// removal never waits for the publication or its acknowledgement.
	removing := settleCanonical(t, r, run1, 1, KindCanonical)
	removal := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindRemoved, blockA, 0, 1, 21)}}
	result, err := r.Apply(removing, removal)
	if err != nil {
		t.Fatalf("remove ready applied: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != key {
		t.Fatalf("removed %v", result.Removed)
	}
	rec, ok, err := r.Activation(key)
	if err != nil || !ok {
		t.Fatalf("activation: %v %v", ok, err)
	}
	if rec.State != ActivationRemoved || rec.RemovedRevision != 1 || rec.RemovedDecision == nil ||
		*rec.RemovedDecision != removing || !rec.Ready || !rec.Acked {
		t.Fatalf("removed record is %+v", rec)
	}
	if _, err := r.NextRevision(key); !errors.Is(err, ErrTerminalActivation) {
		t.Fatalf("terminal revision allocated: %v", err)
	}
	if err := r.MarkReady(key); !errors.Is(err, ErrActivationState) {
		t.Fatalf("ready on terminal activation: %v", err)
	}

	// The same block returns as a new activation with a new decision
	// sequence; the removed record stays terminal.
	reapply := settleCanonical(t, r, run1, 2, KindCanonical)
	reapplyPlan := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindApplied, blockA, 2, 0, 41)}}
	if _, err := r.Apply(reapply, reapplyPlan); err != nil {
		t.Fatalf("reapply: %v", err)
	}
	fresh, ok, err := r.Activation(blockKeyOf(run1, 2))
	if err != nil || !ok || fresh.State != ActivationActive {
		t.Fatalf("reapplied activation is %+v %v %v", fresh, ok, err)
	}
	old, _, _ := r.Activation(key)
	if old.State != ActivationRemoved {
		t.Fatalf("removed activation reactivated: %+v", old)
	}

	// A removal before READY works the same way.
	applyBlock(t, r, run1, 3, blockB)
	notReady := blockKeyOf(run1, 3)
	removing2 := settleCanonical(t, r, run1, 4, KindCanonical)
	removal2 := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindRemoved, blockB, 3, 1, 51)}}
	if _, err := r.Apply(removing2, removal2); err != nil {
		t.Fatalf("remove before ready: %v", err)
	}
	rec2, _, _ := r.Activation(notReady)
	if rec2.State != ActivationRemoved || rec2.Ready {
		t.Fatalf("unready removal is %+v", rec2)
	}

	// The writer creates no new Status for a terminal activation.
	finality := settleCanonical(t, r, run1, 5, KindFinality)
	status := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindStatus, blockA, 0, 2, 31)}}
	if _, err := r.Apply(finality, status); !errors.Is(err, ErrTerminalActivation) {
		t.Fatalf("status on terminal activation: %v", err)
	}
}

func TestRevisionFrontier(t *testing.T) {
	r := open(t, newStore())
	applyBlock(t, r, run1, 0, blockA)
	key := blockKeyOf(run1, 0)
	if revision, err := r.NextRevision(key); err != nil || revision != 1 {
		t.Fatalf("next revision %d %v", revision, err)
	}

	// A revision above the frontier is refused: the counter allocates one
	// revision at a time.
	first := settleCanonical(t, r, run1, 1, KindFinality)
	skip := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindStatus, blockA, 0, 2, 31)}}
	if _, err := r.Apply(first, skip); !errors.Is(err, ErrRevision) {
		t.Fatalf("skipped revision accepted: %v", err)
	}
	frontier := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindStatus, blockA, 0, 1, 32)}}
	if _, err := r.Apply(first, frontier); err != nil {
		t.Fatalf("frontier status: %v", err)
	}
	if revision, err := r.NextRevision(key); err != nil || revision != 2 {
		t.Fatalf("next revision %d %v", revision, err)
	}

	// A stale revision is refused; so is an unknown activation and a kind
	// the decision may not apply.
	second := settleCanonical(t, r, run1, 2, KindFinality)
	stale := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindStatus, blockA, 0, 1, 33)}}
	if _, err := r.Apply(second, stale); !errors.Is(err, ErrRevision) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	unknown := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindStatus, blockC, 9, 1, 34)}}
	if _, err := r.Apply(second, unknown); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("unknown activation accepted: %v", err)
	}
	wrongKind := ApplicationPlan{Events: []EventPlan{
		entityEvent(run1, semantic.KindApplied, blockB, 2, 0, 35)}}
	if _, err := r.Apply(second, wrongKind); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("finality applied an Applied event: %v", err)
	}
}

func TestCorruptRecordRejected(t *testing.T) {
	db := newStore()
	applyBlock(t, open(t, db), run1, 0, blockA)
	for key, value := range db.durable {
		tampered := slices.Clone(value)
		tampered[len(tampered)-1] ^= 0xFF
		db.durable[key] = tampered
		break
	}
	if _, _, err := Open(db); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered record accepted: %v", err)
	}

	stray := newStore()
	applyBlock(t, open(t, stray), run1, 0, blockA)
	if err := stray.Put([]byte(keyPrefix+"stray"), []byte("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := stray.SyncKeyValue(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, _, err := Open(stray); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("stray key accepted: %v", err)
	}

	truncated := newStore()
	applyBlock(t, open(t, truncated), run1, 0, blockA)
	decKey := decisionKey(producer1, run1, 0)
	truncated.durable[string(decKey)] = truncated.durable[string(decKey)][:3]
	if _, _, err := Open(truncated); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("truncated record accepted: %v", err)
	}
}

func TestConcurrentRuns(t *testing.T) {
	db := newStore()
	r := open(t, db)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		run := id16(byte(0x60 + i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref := dref(run, 0)
			if err := r.Intend(ref, net1, KindImport, intentDigestOf(0)); err != nil {
				t.Errorf("intend %s: %v", ref, err)
				return
			}
			if err := r.SettleDecided(ref); err != nil {
				t.Errorf("settle %s: %v", ref, err)
				return
			}
			if err := r.CaptureCorpus(ref, blockA, corpusDigestOf(blockA)); err != nil {
				t.Errorf("capture %s: %v", ref, err)
				return
			}
			if _, err := r.Apply(ref, importPlan(run, 0, blockA)); err != nil {
				t.Errorf("apply %s: %v", ref, err)
				return
			}
			if err := r.MarkReady(blockKeyOf(run, 0)); err != nil {
				t.Errorf("ready %s: %v", ref, err)
				return
			}
		}()
	}
	wg.Wait()
	events, err := r.Events(nil)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 16 {
		t.Fatalf("events %d, want 16", len(events))
	}
	for i := 0; i < 8; i++ {
		run := id16(byte(0x60 + i))
		rec, ok, err := r.Activation(blockKeyOf(run, 0))
		if err != nil || !ok || !rec.Ready {
			t.Fatalf("run %d activation %+v %v %v", i, rec, ok, err)
		}
	}
}
