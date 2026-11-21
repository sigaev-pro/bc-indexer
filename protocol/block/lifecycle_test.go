package block

import (
	"errors"
	"reflect"
	"testing"
)

// ethSnapshot builds a minimal valid ETH-family lifecycle snapshot.
func ethSnapshot(t *testing.T, decisionSeq uint64) LifecycleSnapshot {
	t.Helper()
	return LifecycleSnapshot{
		NativeDecision: NativeDecisionRef{
			NodeInstanceID:   mustID16(1),
			ProducerID:       mustID16(2),
			RunID:            mustID16(3),
			DecisionSequence: decisionSeq,
		},
		ObservedExecutionHead: AnchorRef{
			Height: fixtureEthBlock1Number,
			Hash:   mustHash32(t, fixtureEthBlock1Hash),
		},
		Markers: FinalityMarkers{Eth: &EthereumMarkers{
			Safe:      NotObserved(),
			Finalized: NotObserved(),
			SafeBasis: SafeBasisUnset,
		}},
	}
}

// snapshotAtHead returns s with a different observed execution head, to
// build distinct replacement evidence.
func snapshotAtHead(t *testing.T, s LifecycleSnapshot, height uint64) LifecycleSnapshot {
	t.Helper()
	out := s
	out.ObservedExecutionHead = AnchorRef{Height: height, Hash: mustHash32(t, fixtureEthBlock1Hash)}
	return out
}

// ethBlockRecord builds the fixture BlockRecord for one activation of the
// ETH fixture block.
func ethBlockRecord(t *testing.T, decisionSeq uint64) BlockRecord {
	t.Helper()
	meta := ethFixtureMetadata(t)
	return BlockRecord{
		Activation: BlockActivationRef{
			Network: NetworkIdentity{
				Family:      "EVM",
				ChainID:     u256(1),
				GenesisHash: mustHash32(t, fixtureEthGenesis),
			},
			BlockHash:    meta.Hash,
			ProducerID:   mustID16(2),
			RunID:        mustID16(3),
			ActivationID: ActivationID{DecisionSequence: decisionSeq, EntityOrdinal: 0},
		},
		Admission: ObservationRef{
			ProducerID:          mustID16(2),
			RunID:               mustID16(3),
			ObservationSequence: 11,
		},
		Metadata:        meta,
		InitialSnapshot: ethSnapshot(t, decisionSeq),
	}
}

// appliedEvent builds the Applied(revision 0) event of one record.
func appliedEvent(t *testing.T, rec BlockRecord) LifecycleEvent {
	t.Helper()
	return LifecycleEvent{
		Activation: rec.Activation,
		Kind:       EventApplied,
		Snapshot:   rec.InitialSnapshot,
	}
}

// removedEvent builds a Removed event at the given revision.
func removedEvent(t *testing.T, rec BlockRecord, revision uint64) LifecycleEvent {
	t.Helper()
	return LifecycleEvent{
		Activation:     rec.Activation,
		Kind:           EventRemoved,
		Revision:       revision,
		NativeDecision: rec.InitialSnapshot.NativeDecision,
		Reason:         RemovalReasonNativeCanonicalChange,
	}
}

func TestLifecycleAppliedPendingRemovedTable(t *testing.T) {
	rec := ethBlockRecord(t, 5)

	// ABSENT + Applied(rev 0) -> ACTIVE(A) with the initial snapshot.
	state, err := StartApplied(appliedEvent(t, rec))
	if err != nil {
		t.Fatal(err)
	}
	if state.State != StateActive || state.Revision != 0 {
		t.Fatalf("applied must produce ACTIVE revision 0, got %s rev %d", state.State, state.Revision)
	}

	// ACTIVE + Applied retry with the same evidence -> the same fact.
	again, err := state.Apply(appliedEvent(t, rec))
	if err != nil {
		t.Fatalf("identical applied retry must be idempotent: %v", err)
	}
	if !reflect.DeepEqual(state, again) {
		t.Fatal("identical applied retry changed the state")
	}

	// ACTIVE + Applied retry with different evidence -> conflict.
	conflict := appliedEvent(t, rec)
	conflict.Snapshot = snapshotAtHead(t, rec.InitialSnapshot, 99)
	if _, err := state.Apply(conflict); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("applied retry with different evidence must conflict, got %v", err)
	}

	// ACTIVE + StatusChanged(rev higher) -> ACTIVE with new full evidence.
	changed := LifecycleEvent{
		Activation: rec.Activation,
		Kind:       EventStatusChanged,
		Revision:   3,
		Snapshot:   snapshotAtHead(t, rec.InitialSnapshot, 3),
	}
	updated, err := state.Apply(changed)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != StateActive || updated.Revision != 3 || updated.Snapshot.Equal(state.Snapshot) {
		t.Fatalf("status changed must replace evidence: %+v", updated)
	}

	// Lower revision never replaces a newer one.
	stale := changed
	stale.Revision = 2
	stale.Snapshot = snapshotAtHead(t, rec.InitialSnapshot, 2)
	if _, err := updated.Apply(stale); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale revision must be rejected, got %v", err)
	}

	// Same revision with different content is a conflict, not
	// last-write-wins.
	same := changed
	same.Revision = 3
	same.Snapshot = snapshotAtHead(t, rec.InitialSnapshot, 33)
	if _, err := updated.Apply(same); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("same revision different content must conflict, got %v", err)
	}

	// ACTIVE + Removed(rev higher) -> REMOVED, terminal for the activation.
	removed, err := updated.Apply(removedEvent(t, rec, 4))
	if err != nil {
		t.Fatal(err)
	}
	if removed.State != StateRemoved || removed.Revision != 4 {
		t.Fatalf("removed must produce REMOVED at revision 4, got %+v", removed)
	}

	// REMOVED + any late event -> terminal error, no reactivation.
	for _, ev := range []LifecycleEvent{
		appliedEvent(t, rec),
		{Activation: rec.Activation, Kind: EventStatusChanged, Revision: 5, Snapshot: rec.InitialSnapshot},
		removedEvent(t, rec, 6),
	} {
		if _, err := removed.Apply(ev); !errors.Is(err, ErrTerminal) {
			t.Fatalf("event %s on removed activation must be terminal, got %v", ev.Kind, err)
		}
	}

	// Events of a different activation never mix into this lifecycle.
	other := appliedEvent(t, rec)
	other.Activation.BlockHash = mustHash32(t, fixtureBscBlock1Hash)
	if _, err := state.Apply(other); !errors.Is(err, ErrWrongActivation) {
		t.Fatalf("foreign activation must be rejected, got %v", err)
	}
}

func TestLifecyclePendingApplied(t *testing.T) {
	rec := ethBlockRecord(t, 5)

	// ABSENT + StatusChanged(rev>0) -> PENDING_APPLIED: keep the last full
	// evidence, invent no record.
	late := LifecycleEvent{
		Activation: rec.Activation,
		Kind:       EventStatusChanged,
		Revision:   2,
		Snapshot:   snapshotAtHead(t, rec.InitialSnapshot, 2),
	}
	pending, err := StartPending(late)
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != StatePendingApplied || pending.Revision != 2 {
		t.Fatalf("late status must produce PENDING_APPLIED, got %+v", pending)
	}

	// PENDING_APPLIED + StatusChanged(rev higher) -> replace the snapshot.
	newer := LifecycleEvent{
		Activation: rec.Activation,
		Kind:       EventStatusChanged,
		Revision:   4,
		Snapshot:   snapshotAtHead(t, rec.InitialSnapshot, 4),
	}
	pending2, err := pending.Apply(newer)
	if err != nil {
		t.Fatal(err)
	}
	if pending2.State != StatePendingApplied || pending2.Revision != 4 {
		t.Fatalf("pending replacement mismatch: %+v", pending2)
	}

	// PENDING_APPLIED + Applied(rev 0) -> ACTIVE with the original record
	// plus the already received later evidence; the durable revision stays.
	active, err := pending2.Apply(appliedEvent(t, rec))
	if err != nil {
		t.Fatal(err)
	}
	if active.State != StateActive {
		t.Fatalf("applied after pending must activate, got %s", active.State)
	}
	if active.Revision != 4 {
		t.Fatalf("durable revision must stay at 4, got %d", active.Revision)
	}
	if !active.Snapshot.Equal(pending2.Snapshot) {
		t.Fatal("later evidence must survive the late applied")
	}

	// PENDING_APPLIED + Removed -> REMOVED tombstone.
	tomb, err := pending2.Apply(removedEvent(t, rec, 9))
	if err != nil {
		t.Fatal(err)
	}
	if tomb.State != StateRemoved {
		t.Fatalf("removed on pending must tombstone, got %s", tomb.State)
	}
}

func TestReorgCreatesNewActivationOldImmutable(t *testing.T) {
	rec := ethBlockRecord(t, 5)
	before, err := StartApplied(appliedEvent(t, rec))
	if err != nil {
		t.Fatal(err)
	}
	// A reorg takes the block away.
	removed, err := before.Apply(removedEvent(t, rec, 1))
	if err != nil {
		t.Fatal(err)
	}
	recSnapshot := rec
	stateSnapshot := removed

	// The same block returns: a new native decision creates a NEW
	// activation record; the old one is never mutated or reactivated.
	returned := ethSnapshot(t, 9)
	newRec, err := NewCanonicalPeriod(removed, rec.Metadata, rec.Admission, returned)
	if err != nil {
		t.Fatal(err)
	}
	if newRec.Activation.ActivationID != (ActivationID{DecisionSequence: 9, EntityOrdinal: 0}) {
		t.Fatalf("new period must carry the new decision sequence, got %+v", newRec.Activation.ActivationID)
	}
	if newRec.Activation.BlockHash != rec.Activation.BlockHash {
		t.Fatal("the new period is for the same block identity")
	}
	if newRec.Activation == rec.Activation {
		t.Fatal("activation references of two periods must differ")
	}
	if newRec.InitialSnapshot.Equal(rec.InitialSnapshot) {
		t.Fatal("the new period carries its own initial snapshot")
	}
	if err := newRec.Validate(); err != nil {
		t.Fatalf("new period record must validate: %v", err)
	}

	// The old record and state are immutable witnesses of the first period.
	if !reflect.DeepEqual(rec, recSnapshot) {
		t.Fatal("the old record changed while building the new period")
	}
	if !reflect.DeepEqual(removed, stateSnapshot) {
		t.Fatal("the old lifecycle state changed while building the new period")
	}
	if _, err := removed.Apply(appliedEvent(t, rec)); !errors.Is(err, ErrTerminal) {
		t.Fatalf("old activation must stay terminal, got %v", err)
	}

	// The new period starts its own lifecycle at revision 0.
	newState, err := StartApplied(appliedEvent(t, newRec))
	if err != nil {
		t.Fatal(err)
	}
	if newState.State != StateActive || newState.Revision != 0 {
		t.Fatalf("new period must start at ACTIVE revision 0, got %+v", newState)
	}

	// Guards: a new period needs a removed old activation and a different
	// native decision.
	if _, err := NewCanonicalPeriod(before, rec.Metadata, rec.Admission, returned); !errors.Is(err, ErrNotRemoved) {
		t.Fatalf("live activation must refuse a new period, got %v", err)
	}
	if _, err := NewCanonicalPeriod(removed, rec.Metadata, rec.Admission, ethSnapshot(t, 5)); !errors.Is(err, ErrSameDecision) {
		t.Fatalf("reused decision must be refused, got %v", err)
	}
}

func TestCountsAreObservationsNotLifecycleInputs(t *testing.T) {
	base := ethBlockRecord(t, 5)
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}

	// Mutating any observed count never changes record validity: no count
	// is a completeness assertion and none carries a minimum.
	variants := []BlockRecord{base}
	v1 := base
	v1.Metadata.SourceTransactionCount = 0 // an empty block shape.
	variants = append(variants, v1)
	v2 := base
	v2.Metadata.SourceTransactionCount = 1 << 40 // an arbitrarily large observation.
	variants = append(variants, v2)
	v3 := base
	v3.Metadata.Details = FamilyDetails{Bsc: &BscBlockDetails{
		ParliaSystemTransactionCount: Some(uint64(3)),
		VoteSnapshot:                 None[ParliaVoteSnapshot](),
	}}
	v3.InitialSnapshot.Markers = FinalityMarkers{Bsc: &BscMarkers{
		EffectiveFinalized: NotObserved(),
		EffectiveBasis:     BscBasisUnset,
	}}
	variants = append(variants, v3)
	v4 := base
	v4.Metadata.Details = FamilyDetails{Bor: &BorBlockDetails{
		StateSyncCount: Some(uint64(2)),
		StateSyncMode:  Some(StateSyncBodyTransaction),
	}}
	v4.InitialSnapshot.Markers = FinalityMarkers{Polygon: &PolygonMarkers{
		Milestone:       NotObserved(),
		Checkpoint:      NotObserved(),
		NativeFinalized: NotObserved(),
		L1Checkpoint:    NotObserved(),
	}}
	variants = append(variants, v4)

	for i, v := range variants {
		if err := v.Validate(); err != nil {
			t.Fatalf("variant %d must validate regardless of counts: %v", i, err)
		}
	}

	// The lifecycle of each variant evolves identically: counts never
	// enter lifecycle validation.
	events := []func(BlockRecord) LifecycleEvent{
		func(r BlockRecord) LifecycleEvent { return appliedEvent(t, r) },
		func(r BlockRecord) LifecycleEvent {
			return LifecycleEvent{Activation: r.Activation, Kind: EventStatusChanged, Revision: 2,
				Snapshot: snapshotAtHead(t, r.InitialSnapshot, 2)}
		},
		func(r BlockRecord) LifecycleEvent { return removedEvent(t, r, 3) },
	}
	var finals []ActivationState
	for _, v := range variants {
		state, err := StartApplied(appliedEvent(t, v))
		if err != nil {
			t.Fatal(err)
		}
		for _, mk := range events[1:] {
			state, err = state.Apply(mk(v))
			if err != nil {
				t.Fatal(err)
			}
		}
		finals = append(finals, state)
	}
	for _, f := range finals[1:] {
		if f.State != finals[0].State || f.Revision != finals[0].Revision {
			t.Fatal("counts leaked into lifecycle validation")
		}
	}

	// Changing a count after the lifecycle progressed leaves the already
	// applied record valid and unchanged as a witness.
	live := variants[0]
	_, err := StartApplied(appliedEvent(t, live))
	if err != nil {
		t.Fatal(err)
	}
	live.Metadata.SourceTransactionCount = 1 << 40
	if err := live.Validate(); err != nil {
		t.Fatalf("mutated count must not invalidate the record: %v", err)
	}
}

func TestEventShapeValidation(t *testing.T) {
	rec := ethBlockRecord(t, 5)

	applied := appliedEvent(t, rec)
	if err := applied.Validate(); err != nil {
		t.Fatal(err)
	}
	badRev := applied
	badRev.Revision = 2
	if err := badRev.Validate(); err == nil {
		t.Fatal("applied must carry revision 0")
	}
	noSnapshot := LifecycleEvent{Activation: rec.Activation, Kind: EventStatusChanged, Revision: 1}
	if err := noSnapshot.Validate(); err == nil {
		t.Fatal("status changed must carry a full snapshot")
	}
	badRemoved := LifecycleEvent{
		Activation:     rec.Activation,
		Kind:           EventRemoved,
		Revision:       1,
		NativeDecision: rec.InitialSnapshot.NativeDecision,
	}
	if err := badRemoved.Validate(); err == nil {
		t.Fatal("removed must carry a reason")
	}
	if err := (LifecycleEvent{Activation: rec.Activation, Kind: EventRemoved, Revision: 0,
		NativeDecision: rec.InitialSnapshot.NativeDecision, Reason: RemovalReasonNativeCanonicalChange}).Validate(); err == nil {
		t.Fatal("removed must carry a positive revision")
	}
}

func TestSnapshotMarkerClosure(t *testing.T) {
	rec := ethBlockRecord(t, 5)

	// No marker variant is invalid.
	noMarkers := rec.InitialSnapshot
	noMarkers.Markers = FinalityMarkers{}
	if err := noMarkers.Validate(); err == nil {
		t.Fatal("snapshot without markers must be rejected")
	}
	noMarkerRec := BlockRecord{Activation: rec.Activation, Admission: rec.Admission,
		Metadata: rec.Metadata, InitialSnapshot: noMarkers}
	if err := noMarkerRec.Validate(); err == nil {
		t.Fatal("record with marker-less snapshot must be rejected")
	}

	// A BSC marker set on an ETH record is a family mismatch.
	mixed := rec
	mixed.InitialSnapshot.Markers = FinalityMarkers{Bsc: &BscMarkers{
		EffectiveFinalized: NotObserved(),
		EffectiveBasis:     BscBasisUnset,
	}}
	if err := mixed.Validate(); err == nil {
		t.Fatal("marker family must match the metadata family")
	}

	// The zero decision reference is invalid: snapshots always cite a
	// native decision.
	zeroDecision := rec.InitialSnapshot
	zeroDecision.NativeDecision = NativeDecisionRef{}
	if err := zeroDecision.Validate(); err == nil {
		t.Fatal("zero decision reference must be rejected")
	}
}

func TestBlockRecordRoundTrip(t *testing.T) {
	rec := ethBlockRecord(t, 5)
	if err := rec.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var back BlockRecord
	if err := Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rec, back) {
		t.Fatal("block record round trip changed the record")
	}
	if err := back.Validate(); err != nil {
		t.Fatal(err)
	}
}
