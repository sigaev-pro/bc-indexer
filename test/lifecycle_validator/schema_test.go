package lifecycle_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
	lifecycle "github.com/sigaev-pro/bc-indexer/test/lifecycle_validator"
)

// Schema fixtures: the transport identities of the semantic fixtures (one
// network, one publication epoch), so every event of one activation carries
// one producer, run, network and epoch across its envelope and frames.

var (
	schemaNet   = protoNetworkOf(net1)
	schemaEpoch = proto.ID16(epoch1)
)

// protoNetworkOf mirrors a semantic network as the frame NetworkIdentity:
// the one registered EVM family with the same chain id and genesis hash.
func protoNetworkOf(net semantic.Network) proto.NetworkIdentity {
	var out proto.NetworkIdentity
	out.Family = proto.NetworkFamilyEVM
	copy(out.ChainID[:], net.ChainID[:])
	copy(out.GenesisHash[:], net.GenesisHash[:])
	return out
}

// patternBytes returns n deterministic non-empty payload bytes.
func patternBytes(seed byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i)%251 + seed
	}
	return out
}

// schemaFragments returns the canonical payload fragments of one event:
// a header, one items fragment and the seal (the minimum record shape).
func schemaFragments(seed byte) [][]byte {
	return [][]byte{patternBytes(seed, 48), patternBytes(seed+1, 120), patternBytes(seed+2, 12)}
}

// variantOf returns the closed payload variant of a family/kind pair,
// registry v1: families group three kinds in order (tx 1/2/3, block 4/5/6,
// balance.system 7/8/9, bor.legacy_receipt 10/11/12).
func variantOf(f semantic.Family, k semantic.Kind) (profiles.PayloadVariant, error) {
	if !f.Valid() || !k.Valid() {
		return 0, errors.New("schema fixture: family or kind outside registry v1")
	}
	return profiles.PayloadVariant(uint8(f-1)*3 + uint8(k)), nil
}

// profileForFamily picks the network profile whose closed variant set
// admits the family: balance.system needs BSC or Bor, bor.legacy_receipt
// needs Bor.
func profileForFamily(f semantic.Family) profiles.Family {
	switch f {
	case semantic.FamilyBorLegacy:
		return profiles.FamilyBor
	case semantic.FamilyBalanceSystem:
		return profiles.FamilyBsc
	}
	return profiles.FamilyEth
}

// schemaEnvelope builds the proto Envelope of one semantic descriptor: the
// closed payload variant of its family and kind, synthetic payload
// fragments seeded by the event content, and the public event id of its
// producer and run.
func schemaEnvelope(t *testing.T, d semantic.SemanticDescriptor, eventSequence, seed uint64) *proto.Envelope {
	t.Helper()
	variant, err := variantOf(d.Family, d.Kind)
	if err != nil {
		t.Fatalf("variant fixture: %v", err)
	}
	return &proto.Envelope{
		EventID:  proto.NewEventID(proto.ID16(d.Activation.ProducerID), proto.ID16(d.Activation.RunID), eventSequence),
		Profile:  profileForFamily(d.Family),
		Semantic: d,
		Payload:  proto.Payload{Variant: variant, Fragments: schemaFragments(byte(seed))},
	}
}

// schemaEvent assembles one validated SchemaEvent of the descriptor at the
// given batch ordinal. The seed varies the payload content: two events
// built with the same seed carry identical record bytes, two with different
// seeds carry different bytes.
func schemaEvent(t *testing.T, ordinal uint64, d semantic.SemanticDescriptor, seed byte) lifecycle.SchemaEvent {
	t.Helper()
	event, err := lifecycle.NewSchemaEvent(ordinal, 1, schemaEnvelope(t, d, ordinal+1, uint64(seed)),
		schemaNet, schemaEpoch)
	if err != nil {
		t.Fatalf("schema event fixture: %v", err)
	}
	return event
}

func mustSchemaApply(t *testing.T, s *lifecycle.SchemaAutomaton, e lifecycle.SchemaEvent) lifecycle.Result {
	t.Helper()
	res, err := s.Apply(e)
	if err != nil {
		t.Fatalf("schema apply returned an unexpected error: %v", err)
	}
	return res
}

func schemaRecord(t *testing.T, s *lifecycle.SchemaAutomaton, key lifecycle.ActivationKey) lifecycle.Record {
	t.Helper()
	rec, ok := s.Record(key)
	if !ok {
		t.Fatalf("no record for key %v", key)
	}
	return rec
}

// reframe decodes one canonical frame, applies the mutation and re-encodes,
// so a mutated fixture stays canonically encoded and only its values break.
func reframe(t *testing.T, encoded []byte, mutate func(*proto.TransportFrame)) []byte {
	t.Helper()
	frame, err := proto.DecodeTransportFrame(encoded)
	if err != nil {
		t.Fatalf("frame fixture: %v", err)
	}
	mutate(frame)
	out, err := frame.Encode()
	if err != nil {
		t.Fatalf("frame re-encode: %v", err)
	}
	return out
}

// TestSchemaRoundTripContractTable walks the lifecycle rows of
// MESSAGE_CONTRACT section 5 through the schema seam: each event is
// validated as a schema message and folded, and the outcome and record
// match the semantic-half table.
func TestSchemaRoundTripContractTable(t *testing.T) {
	type step struct {
		d    semantic.SemanticDescriptor
		seed byte
	}
	rows := []struct {
		name           string
		steps          []step
		wantClass      lifecycle.Class
		wantState      lifecycle.State
		wantOrigin     bool
		wantMax        uint64
		wantRemovedRev uint64
	}{
		{
			name: "absent applied becomes active",
			steps: []step{
				{txApplied(1, decisionTx, 1), 0x11},
			},
			wantClass: lifecycle.ClassAccepted, wantState: lifecycle.StateActive, wantOrigin: true, wantMax: 0,
		},
		{
			name: "absent status keeps pending evidence",
			steps: []step{
				{txStatus(3, 2, decisionTx, 1), 0x12},
			},
			wantClass: lifecycle.ClassUnknown, wantState: lifecycle.StatePendingApplied, wantMax: 3,
		},
		{
			name: "absent removed keeps the tombstone",
			steps: []step{
				{txRemoved(5, 3, decisionTx, 1), 0x13},
			},
			wantClass: lifecycle.ClassUnknown, wantState: lifecycle.StateRemoved, wantMax: 5, wantRemovedRev: 5,
		},
		{
			name: "pending applied activates with kept evidence",
			steps: []step{
				{txStatus(4, 2, decisionTx, 1), 0x14},
				{txApplied(1, decisionTx, 1), 0x15},
			},
			wantClass: lifecycle.ClassAccepted, wantState: lifecycle.StateActive, wantOrigin: true, wantMax: 4,
		},
		{
			name: "pending higher status replaces the snapshot",
			steps: []step{
				{txStatus(3, 2, decisionTx, 1), 0x16},
				{txStatus(9, 3, decisionTx, 1), 0x17},
			},
			wantClass: lifecycle.ClassAccepted, wantState: lifecycle.StatePendingApplied, wantMax: 9,
		},
		{
			name: "pending removed terminalizes",
			steps: []step{
				{txStatus(3, 2, decisionTx, 1), 0x18},
				{txRemoved(6, 3, decisionTx, 1), 0x19},
			},
			wantClass: lifecycle.ClassAccepted, wantState: lifecycle.StateRemoved, wantMax: 6, wantRemovedRev: 6,
		},
		{
			name: "active applied retry is the same fact",
			steps: []step{
				{txApplied(1, decisionTx, 1), 0x1A},
				{txApplied(1, decisionTx, 1), 0x1A},
			},
			wantClass: lifecycle.ClassDuplicate, wantState: lifecycle.StateActive, wantOrigin: true, wantMax: 0,
		},
		{
			name: "active higher status is new evidence",
			steps: []step{
				{txApplied(1, decisionTx, 1), 0x1B},
				{txStatus(2, 2, decisionTx, 1), 0x1C},
				{txStatus(5, 3, decisionTx, 1), 0x1D},
			},
			wantClass: lifecycle.ClassAccepted, wantState: lifecycle.StateActive, wantOrigin: true, wantMax: 5,
		},
		{
			name: "active removed is terminal",
			steps: []step{
				{txApplied(1, decisionTx, 1), 0x1E},
				{txRemoved(4, 2, decisionTx, 1), 0x1F},
			},
			wantClass: lifecycle.ClassAccepted, wantState: lifecycle.StateRemoved, wantOrigin: true, wantMax: 4, wantRemovedRev: 4,
		},
		{
			name: "removed redelivered applied stays removed",
			steps: []step{
				{txApplied(1, decisionTx, 1), 0x20},
				{txRemoved(4, 2, decisionTx, 1), 0x21},
				{txApplied(1, decisionTx, 1), 0x20},
			},
			wantClass: lifecycle.ClassDuplicate, wantState: lifecycle.StateRemoved, wantOrigin: true, wantMax: 4, wantRemovedRev: 4,
		},
		{
			name: "removed before applied keeps the tombstone",
			steps: []step{
				{txRemoved(4, 1, decisionTx, 1), 0x22},
				{txApplied(2, decisionTx, 1), 0x23},
			},
			wantClass: lifecycle.ClassStale, wantState: lifecycle.StateRemoved, wantOrigin: true, wantMax: 4, wantRemovedRev: 4,
		},
		{
			name: "removed late status below the removed revision is stale evidence",
			steps: []step{
				{txApplied(1, decisionTx, 1), 0x24},
				{txRemoved(6, 2, decisionTx, 1), 0x25},
				{txStatus(2, 3, decisionTx, 1), 0x26},
			},
			wantClass: lifecycle.ClassStale, wantState: lifecycle.StateRemoved, wantOrigin: true, wantMax: 6, wantRemovedRev: 6,
		},
		{
			name: "same revision with different bytes conflicts",
			steps: []step{
				{txStatus(3, 2, decisionTx, 1), 0x27},
				{txStatus(3, 3, decisionTx, 1), 0x28},
			},
			wantClass: lifecycle.ClassConflict, wantState: lifecycle.StatePendingApplied, wantMax: 3,
		},
		{
			name: "block entity applied becomes active",
			steps: []step{
				{blockEvent(semantic.KindApplied, 0, 1, 50, 1), 0x29},
			},
			wantClass: lifecycle.ClassAccepted, wantState: lifecycle.StateActive, wantOrigin: true, wantMax: 0,
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			s := lifecycle.NewSchema()
			var res lifecycle.Result
			for i, st := range row.steps {
				res = mustSchemaApply(t, s, schemaEvent(t, uint64(i), st.d, st.seed))
			}
			if res.Class != row.wantClass {
				t.Errorf("class = %s, want %s (reason: %s)", res.Class, row.wantClass, res.Reason)
			}
			if res.State != row.wantState {
				t.Errorf("resulting state = %s, want %s", res.State, row.wantState)
			}
			rec := schemaRecord(t, s, lifecycle.KeyOf(row.steps[0].d))
			if rec.State != row.wantState {
				t.Errorf("record state = %s, want %s", rec.State, row.wantState)
			}
			if (rec.Origin != nil) != row.wantOrigin {
				t.Errorf("origin observed = %v, want %v", rec.Origin != nil, row.wantOrigin)
			}
			if rec.MaxRevision != row.wantMax {
				t.Errorf("max revision = %d, want %d", rec.MaxRevision, row.wantMax)
			}
			if rec.State == lifecycle.StateRemoved && rec.RemovedRevision != row.wantRemovedRev {
				t.Errorf("removed revision = %d, want %d", rec.RemovedRevision, row.wantRemovedRev)
			}
		})
	}
}

// TestSchemaRoundTripFamilies validates and folds one event of every
// registry family and kind: the closed variant triangle and the profile
// gating pass for each, and the outcome matches the unseen-activation rows.
func TestSchemaRoundTripFamilies(t *testing.T) {
	cases := []struct {
		family    semantic.Family
		kind      semantic.Kind
		event     func() semantic.SemanticDescriptor
		wantClass lifecycle.Class
		wantState lifecycle.State
	}{
		{semantic.FamilyTx, semantic.KindApplied, func() semantic.SemanticDescriptor {
			return txApplied(1, 60, 1)
		}, lifecycle.ClassAccepted, lifecycle.StateActive},
		{semantic.FamilyTx, semantic.KindRemoved, func() semantic.SemanticDescriptor {
			return txRemoved(2, 1, 61, 1)
		}, lifecycle.ClassUnknown, lifecycle.StateRemoved},
		{semantic.FamilyTx, semantic.KindStatus, func() semantic.SemanticDescriptor {
			return txStatus(3, 1, 62, 1)
		}, lifecycle.ClassUnknown, lifecycle.StatePendingApplied},
		{semantic.FamilyBlock, semantic.KindApplied, func() semantic.SemanticDescriptor {
			return blockEvent(semantic.KindApplied, 0, 1, 63, 1)
		}, lifecycle.ClassAccepted, lifecycle.StateActive},
		{semantic.FamilyBlock, semantic.KindRemoved, func() semantic.SemanticDescriptor {
			return blockEvent(semantic.KindRemoved, 2, 2, 64, 1)
		}, lifecycle.ClassUnknown, lifecycle.StateRemoved},
		{semantic.FamilyBlock, semantic.KindStatus, func() semantic.SemanticDescriptor {
			return blockEvent(semantic.KindStatus, 3, 3, 65, 1)
		}, lifecycle.ClassUnknown, lifecycle.StatePendingApplied},
		{semantic.FamilyBalanceSystem, semantic.KindApplied, func() semantic.SemanticDescriptor {
			return optionalEvent(semantic.FamilyBalanceSystem, semantic.KindApplied, 0, 1, 66, 1, 0, 0)
		}, lifecycle.ClassAccepted, lifecycle.StateActive},
		{semantic.FamilyBalanceSystem, semantic.KindStatus, func() semantic.SemanticDescriptor {
			return optionalEvent(semantic.FamilyBalanceSystem, semantic.KindStatus, 4, 2, 67, 1, 0, 0)
		}, lifecycle.ClassUnknown, lifecycle.StatePendingApplied},
		{semantic.FamilyBorLegacy, semantic.KindApplied, func() semantic.SemanticDescriptor {
			return optionalEvent(semantic.FamilyBorLegacy, semantic.KindApplied, 0, 1, 68, 1, 0, 0)
		}, lifecycle.ClassAccepted, lifecycle.StateActive},
		{semantic.FamilyBorLegacy, semantic.KindRemoved, func() semantic.SemanticDescriptor {
			return optionalEvent(semantic.FamilyBorLegacy, semantic.KindRemoved, 5, 2, 69, 1, 0, 0)
		}, lifecycle.ClassUnknown, lifecycle.StateRemoved},
	}
	for _, tc := range cases {
		name := tc.family.String() + "/" + tc.kind.String()
		t.Run(name, func(t *testing.T) {
			s := lifecycle.NewSchema()
			res := mustSchemaApply(t, s, schemaEvent(t, 0, tc.event(), 0x30))
			if res.Class != tc.wantClass {
				t.Errorf("class = %s, want %s (reason: %s)", res.Class, tc.wantClass, res.Reason)
			}
			rec := schemaRecord(t, s, lifecycle.KeyOf(tc.event()))
			if rec.State != tc.wantState {
				t.Errorf("record state = %s, want %s", rec.State, tc.wantState)
			}
		})
	}
}

// TestSchemaReorderPermutations delivers every permutation of one
// activation's writer-valid event set through the schema seam — each event
// rebuilt at the delivery-position ordinal, the way a writer emitting that
// order would number it — and requires the identical final record.
func TestSchemaReorderPermutations(t *testing.T) {
	events := []struct {
		d    semantic.SemanticDescriptor
		seed byte
	}{
		{txApplied(1, decisionTx, 1), 0xA1},
		{txStatus(2, 2, decisionTx, 1), 0xA2},
		{txStatus(5, 3, decisionTx, 1), 0xA3},
		{txRemoved(9, 4, decisionTx, 1), 0xA4},
	}
	run := func(t *testing.T, order []int) map[lifecycle.ActivationKey]lifecycle.Record {
		t.Helper()
		s := lifecycle.NewSchema()
		for pos, idx := range order {
			mustSchemaApply(t, s, schemaEvent(t, uint64(pos), events[idx].d, events[idx].seed))
		}
		return s.Records()
	}
	canonical := run(t, identity(len(events)))
	key := lifecycle.KeyOf(events[0].d)
	rec, ok := canonical[key]
	if !ok {
		t.Fatalf("no record for key %v", key)
	}
	if rec.State != lifecycle.StateRemoved || rec.Origin == nil || rec.MaxRevision != 9 ||
		len(rec.Evidence) != 4 || rec.RemovedRevision != 9 {
		t.Fatalf("canonical record = %s, want removed with origin, max 9, 4 slots", rec)
	}
	for _, perm := range permutations(len(events)) {
		got := run(t, perm)
		if !recordsEqual(canonical, got) {
			t.Fatalf("permutation %v produced %s, want %s", perm,
				dumpRecords(got), dumpRecords(canonical))
		}
	}
}

// TestSchemaMixedActivationShuffles shuffles the interleaved events of two
// activations; each activation's ordinals step in delivery order, and every
// order converges to the same two records.
func TestSchemaMixedActivationShuffles(t *testing.T) {
	type tagged struct {
		d          semantic.SemanticDescriptor
		seed       byte
		activation int
	}
	events := []tagged{
		{txApplied(1, 100, 1), 0xB1, 0},
		{txStatus(2, 2, 100, 1), 0xB2, 0},
		{txRemoved(5, 3, 100, 1), 0xB3, 0},
		{txStatus(3, 4, 200, 2), 0xB4, 1},
		{txApplied(5, 200, 2), 0xB5, 1},
	}
	run := func(t *testing.T, order []int) map[lifecycle.ActivationKey]lifecycle.Record {
		t.Helper()
		s := lifecycle.NewSchema()
		counters := [2]uint64{}
		for _, idx := range order {
			ev := events[idx]
			mustSchemaApply(t, s, schemaEvent(t, counters[ev.activation], ev.d, ev.seed))
			counters[ev.activation]++
		}
		return s.Records()
	}
	canonical := run(t, identity(len(events)))
	if len(canonical) != 2 {
		t.Fatalf("canonical run produced %d records, want 2: %s", len(canonical), dumpRecords(canonical))
	}
	for key, rec := range canonical {
		switch key.Activation.DecisionSequence {
		case 100:
			if rec.State != lifecycle.StateRemoved || rec.Origin == nil || rec.MaxRevision != 5 {
				t.Errorf("decision 100 record = %s, want removed with origin and max 5", rec)
			}
		case 200:
			if rec.State != lifecycle.StateActive || rec.Origin == nil || rec.MaxRevision != 3 {
				t.Errorf("decision 200 record = %s, want active with origin and max 3", rec)
			}
		default:
			t.Fatalf("unexpected record for key %v", key)
		}
	}
	orders := [][]int{identity(len(events)), reversed(len(events))}
	shuffler := &lcg{state: 0x20261123}
	for i := 0; i < 300; i++ {
		orders = append(orders, shuffler.permutation(len(events)))
	}
	for i, order := range orders {
		got := run(t, order)
		if !recordsEqual(canonical, got) {
			t.Fatalf("order %d %v produced %s, want %s", i, order,
				dumpRecords(got), dumpRecords(canonical))
		}
	}
}

// TestSchemaOrdinalContinuity checks the per-activation ordinal ledger: no
// gaps, no duplicates, and a rejected event leaves the ledger and the
// records untouched so the corrected delivery still applies.
func TestSchemaOrdinalContinuity(t *testing.T) {
	t.Run("gap is rejected and the corrected delivery applies", func(t *testing.T) {
		s := lifecycle.NewSchema()
		mustSchemaApply(t, s, schemaEvent(t, 0, txApplied(1, decisionTx, 1), 0xC1))
		mustSchemaApply(t, s, schemaEvent(t, 1, txStatus(2, 2, decisionTx, 1), 0xC2))
		key := lifecycle.KeyOf(txApplied(1, decisionTx, 1))
		before := schemaRecord(t, s, key)

		skipped := schemaEvent(t, 3, txStatus(5, 3, decisionTx, 1), 0xC3)
		if _, err := s.Apply(skipped); !errors.Is(err, lifecycle.ErrOrdinalContinuity) {
			t.Fatalf("gap error = %v, want ErrOrdinalContinuity", err)
		}
		if after := schemaRecord(t, s, key); !after.Equal(before) {
			t.Errorf("gap rejection changed the record: got %s, want %s", after, before)
		}

		mustSchemaApply(t, s, schemaEvent(t, 2, txStatus(5, 3, decisionTx, 1), 0xC3))
		// The rejected event consumed no ordinal: the next delivery at
		// ordinal 3 is a new event and applies cleanly.
		res := mustSchemaApply(t, s, schemaEvent(t, 3, txStatus(6, 4, decisionTx, 1), 0xCE))
		if res.Class != lifecycle.ClassAccepted {
			t.Errorf("ordinal-3 delivery class = %s, want Accepted (reason: %s)", res.Class, res.Reason)
		}
		rec := schemaRecord(t, s, key)
		if rec.MaxRevision != 6 {
			t.Errorf("max revision = %d, want 6", rec.MaxRevision)
		}
	})

	t.Run("duplicate ordinal is rejected", func(t *testing.T) {
		s := lifecycle.NewSchema()
		mustSchemaApply(t, s, schemaEvent(t, 0, txApplied(1, decisionTx, 1), 0xC4))
		mustSchemaApply(t, s, schemaEvent(t, 1, txStatus(2, 2, decisionTx, 1), 0xC5))
		if _, err := s.Apply(schemaEvent(t, 1, txRemoved(3, 3, decisionTx, 1), 0xC6)); !errors.Is(err, lifecycle.ErrOrdinalContinuity) {
			t.Fatalf("duplicate error = %v, want ErrOrdinalContinuity", err)
		}
		res := mustSchemaApply(t, s, schemaEvent(t, 2, txRemoved(3, 3, decisionTx, 1), 0xC6))
		if res.Class != lifecycle.ClassAccepted {
			t.Errorf("corrected delivery class = %s, want Accepted (reason: %s)", res.Class, res.Reason)
		}
	})

	t.Run("regressed ordinal is rejected", func(t *testing.T) {
		s := lifecycle.NewSchema()
		mustSchemaApply(t, s, schemaEvent(t, 0, txApplied(1, decisionTx, 1), 0xC7))
		mustSchemaApply(t, s, schemaEvent(t, 1, txStatus(2, 2, decisionTx, 1), 0xC8))
		if _, err := s.Apply(schemaEvent(t, 0, txRemoved(3, 3, decisionTx, 1), 0xC9)); !errors.Is(err, lifecycle.ErrOrdinalContinuity) {
			t.Fatalf("regression error = %v, want ErrOrdinalContinuity", err)
		}
	})

	t.Run("interleaved activations keep independent ledgers", func(t *testing.T) {
		s := lifecycle.NewSchema()
		first := lifecycle.KeyOf(txApplied(1, 100, 1))
		second := lifecycle.KeyOf(txApplied(2, 200, 2))
		mustSchemaApply(t, s, schemaEvent(t, 0, txApplied(1, 100, 1), 0xCA))
		mustSchemaApply(t, s, schemaEvent(t, 0, txApplied(2, 200, 2), 0xCB))
		mustSchemaApply(t, s, schemaEvent(t, 1, txStatus(2, 3, 100, 1), 0xCC))
		mustSchemaApply(t, s, schemaEvent(t, 1, txStatus(2, 4, 200, 2), 0xCD))
		if _, err := s.Apply(schemaEvent(t, 1, txRemoved(3, 5, 200, 2), 0xCE)); !errors.Is(err, lifecycle.ErrOrdinalContinuity) {
			t.Fatalf("second activation duplicate error = %v, want ErrOrdinalContinuity", err)
		}
		mustSchemaApply(t, s, schemaEvent(t, 2, txRemoved(3, 6, 100, 1), 0xCF))
		if s.Len() != 2 {
			t.Errorf("records = %d, want 2", s.Len())
		}
		if rec := schemaRecord(t, s, first); rec.State != lifecycle.StateRemoved {
			t.Errorf("first activation state = %s, want REMOVED", rec.State)
		}
		if rec := schemaRecord(t, s, second); rec.State != lifecycle.StateActive {
			t.Errorf("second activation state = %s, want ACTIVE", rec.State)
		}
	})
}

// TestSchemaWriterInvalidPostTerminalStatus resolves open question 3: a
// StatusChanged created after the terminal event (revision above the
// recorded Removed revision) is a distinct schema-level error class, not
// stale-recorded evidence; everything the writer may still emit keeps its
// semantic-half outcome.
func TestSchemaWriterInvalidPostTerminalStatus(t *testing.T) {
	t.Run("status above the removed revision is writer-invalid", func(t *testing.T) {
		s := lifecycle.NewSchema()
		mustSchemaApply(t, s, schemaEvent(t, 0, txApplied(1, decisionTx, 1), 0xD1))
		mustSchemaApply(t, s, schemaEvent(t, 1, txRemoved(5, 2, decisionTx, 1), 0xD2))
		key := lifecycle.KeyOf(txApplied(1, decisionTx, 1))
		before := schemaRecord(t, s, key)
		if before.RemovedRevision != 5 {
			t.Fatalf("removed revision = %d, want 5", before.RemovedRevision)
		}

		if _, err := s.Apply(schemaEvent(t, 2, txStatus(7, 3, decisionTx, 1), 0xD3)); !errors.Is(err, lifecycle.ErrWriterInvalid) {
			t.Fatalf("post-terminal status error = %v, want ErrWriterInvalid", err)
		}
		after := schemaRecord(t, s, key)
		if !after.Equal(before) {
			t.Errorf("writer-invalid rejection changed the record: got %s, want %s", after, before)
		}
		if _, polluted := after.Evidence[7]; polluted {
			t.Error("writer-invalid status was stale-recorded into slot 7")
		}

		// The rejection consumed no ordinal: the next delivery at ordinal 2
		// still applies.
		res := mustSchemaApply(t, s, schemaEvent(t, 2, txRemoved(6, 4, decisionTx, 1), 0xD4))
		if res.Class != lifecycle.ClassStale {
			t.Errorf("second removed class = %s, want Stale (reason: %s)", res.Class, res.Reason)
		}
	})

	t.Run("status below the removed revision is late evidence", func(t *testing.T) {
		s := lifecycle.NewSchema()
		mustSchemaApply(t, s, schemaEvent(t, 0, txApplied(1, decisionTx, 1), 0xD5))
		mustSchemaApply(t, s, schemaEvent(t, 1, txRemoved(6, 2, decisionTx, 1), 0xD6))
		res := mustSchemaApply(t, s, schemaEvent(t, 2, txStatus(2, 3, decisionTx, 1), 0xD7))
		if res.Class != lifecycle.ClassStale {
			t.Errorf("late pre-removal status class = %s, want Stale (reason: %s)", res.Class, res.Reason)
		}
		key := lifecycle.KeyOf(txApplied(1, decisionTx, 1))
		rec := schemaRecord(t, s, key)
		if _, ok := rec.Evidence[2]; !ok {
			t.Errorf("late pre-removal status not recorded in slot 2: %s", rec)
		}
		if rec.MaxRevision != 6 {
			t.Errorf("max revision = %d, want 6", rec.MaxRevision)
		}
	})

	t.Run("status at the removed revision is a revision clash", func(t *testing.T) {
		s := lifecycle.NewSchema()
		mustSchemaApply(t, s, schemaEvent(t, 0, txApplied(1, decisionTx, 1), 0xD8))
		mustSchemaApply(t, s, schemaEvent(t, 1, txRemoved(5, 2, decisionTx, 1), 0xD9))
		res := mustSchemaApply(t, s, schemaEvent(t, 2, txStatus(5, 3, decisionTx, 1), 0xDA))
		if res.Class != lifecycle.ClassConflict {
			t.Errorf("removed-slot clash class = %s, want Conflict (reason: %s)", res.Class, res.Reason)
		}
	})

	t.Run("redelivered pre-removal status stays idempotent", func(t *testing.T) {
		s := lifecycle.NewSchema()
		mustSchemaApply(t, s, schemaEvent(t, 0, txStatus(2, 1, decisionTx, 1), 0xDB))
		mustSchemaApply(t, s, schemaEvent(t, 1, txRemoved(5, 2, decisionTx, 1), 0xDC))
		res := mustSchemaApply(t, s, schemaEvent(t, 2, txStatus(2, 1, decisionTx, 1), 0xDB))
		if res.Class != lifecycle.ClassDuplicate {
			t.Errorf("redelivered pre-removal status class = %s, want Duplicate (reason: %s)", res.Class, res.Reason)
		}
	})

	t.Run("late applied merges as the tombstone origin", func(t *testing.T) {
		s := lifecycle.NewSchema()
		mustSchemaApply(t, s, schemaEvent(t, 0, txRemoved(5, 1, decisionTx, 1), 0xDD))
		res := mustSchemaApply(t, s, schemaEvent(t, 1, txApplied(2, decisionTx, 1), 0xDE))
		if res.Class != lifecycle.ClassStale {
			t.Errorf("late applied class = %s, want Stale (reason: %s)", res.Class, res.Reason)
		}
		rec := schemaRecord(t, s, lifecycle.KeyOf(txRemoved(5, 1, decisionTx, 1)))
		if rec.State != lifecycle.StateRemoved || rec.Origin == nil {
			t.Errorf("tombstone after late applied = %s, want removed with the merged origin", rec)
		}
	})

	t.Run("the semantic half alone still stale-records it", func(t *testing.T) {
		a := lifecycle.New()
		mustApply(t, a, txApplied(1, decisionTx, 1))
		mustApply(t, a, txRemoved(5, 2, decisionTx, 1))
		res := mustApply(t, a, txStatus(7, 3, decisionTx, 1))
		if res.Class != lifecycle.ClassStale {
			t.Errorf("semantic-half class = %s, want Stale: the writer-invalid error class is the schema half's contribution",
				res.Class)
		}
	})
}

// TestSchemaRecordDigestDiscriminator resolves open question 4: the content
// discriminator inside the fold is the record-level record_digest from the
// proto descriptor. Conflict detection still works on differing bytes, and
// identical record bytes are one fact (Duplicate) even when the descriptors
// differ.
func TestSchemaRecordDigestDiscriminator(t *testing.T) {
	t.Run("conflict on differing bytes at one revision", func(t *testing.T) {
		s := lifecycle.NewSchema()
		mustSchemaApply(t, s, schemaEvent(t, 0, txStatus(5, 2, decisionTx, 1), 0xE1))
		key := lifecycle.KeyOf(txStatus(5, 2, decisionTx, 1))
		before := schemaRecord(t, s, key)
		res := mustSchemaApply(t, s, schemaEvent(t, 1, txStatus(5, 3, decisionTx, 1), 0xE2))
		if res.Class != lifecycle.ClassConflict {
			t.Errorf("class = %s, want Conflict (reason: %s)", res.Class, res.Reason)
		}
		if after := schemaRecord(t, s, key); !after.Equal(before) {
			t.Errorf("conflict changed the record: got %s, want %s", after, before)
		}
	})

	t.Run("identical bytes redelivered are idempotent", func(t *testing.T) {
		s := lifecycle.NewSchema()
		mustSchemaApply(t, s, schemaEvent(t, 0, txStatus(5, 2, decisionTx, 1), 0xE3))
		res := mustSchemaApply(t, s, schemaEvent(t, 1, txStatus(5, 2, decisionTx, 1), 0xE3))
		if res.Class != lifecycle.ClassDuplicate {
			t.Errorf("class = %s, want Duplicate (reason: %s)", res.Class, res.Reason)
		}
	})

	t.Run("same bytes with different provenance are one fact", func(t *testing.T) {
		first := txStatus(5, 2, decisionTx, 1)
		second := txStatus(5, 3, decisionTx, 1) // same revision, different source sequence
		left := digestOf(t, first)
		right := digestOf(t, second)
		if left == right {
			t.Fatal("fixture: the two descriptors carry the same descriptor digest")
		}
		s := lifecycle.NewSchema()
		ev1 := schemaEvent(t, 0, first, 0xE4)
		ev2 := schemaEvent(t, 1, second, 0xE4) // identical payload bytes
		if ev1.Descriptor.RecordDigest != ev2.Descriptor.RecordDigest {
			t.Fatal("fixture: identical payload bytes produced different record digests")
		}
		mustSchemaApply(t, s, ev1)
		res := mustSchemaApply(t, s, ev2)
		if res.Class != lifecycle.ClassDuplicate {
			t.Errorf("class = %s, want Duplicate: same revision and same record bytes are one fact, not a conflict (reason: %s)",
				res.Class, res.Reason)
		}
	})

	t.Run("evidence slots hold the record digest", func(t *testing.T) {
		s := lifecycle.NewSchema()
		applied := schemaEvent(t, 0, txApplied(1, decisionTx, 1), 0xE5)
		status := schemaEvent(t, 1, txStatus(2, 2, decisionTx, 1), 0xE6)
		mustSchemaApply(t, s, applied)
		mustSchemaApply(t, s, status)
		rec := schemaRecord(t, s, lifecycle.KeyOf(txApplied(1, decisionTx, 1)))
		if want := lifecycle.Digest(applied.Descriptor.RecordDigest); rec.Evidence[0] != want {
			t.Errorf("slot 0 digest = %s, want the record digest %s", rec.Evidence[0], want)
		}
		if want := lifecycle.Digest(status.Descriptor.RecordDigest); rec.Evidence[2] != want {
			t.Errorf("slot 2 digest = %s, want the record digest %s", rec.Evidence[2], want)
		}
	})

	t.Run("unset content digest is a caller error", func(t *testing.T) {
		a := lifecycle.New()
		if _, err := a.Apply(txApplied(1, decisionTx, 1), lifecycle.Digest{}); !errors.Is(err, lifecycle.ErrUnsetDigest) {
			t.Fatalf("error = %v, want ErrUnsetDigest", err)
		}
		if a.Len() != 0 {
			t.Errorf("records = %d, want 0", a.Len())
		}
	})
}

// TestSchemaValidateRejectsBrokenEvents feeds corrupted schema events and
// requires the named error class, with the automaton left untouched.
func TestSchemaValidateRejectsBrokenEvents(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(*lifecycle.SchemaEvent)
		want    error
	}{
		{
			name: "trailing bytes on a frame",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Frames[2] = append(append([]byte(nil), e.Frames[2]...), 0x68, 0x00)
			},
			want: proto.ErrUnknownField,
		},
		{
			name: "truncated frame",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Frames[2] = e.Frames[2][:len(e.Frames[2])-2]
			},
			want: proto.ErrMissingField,
		},
		{
			name: "lying record digest in the descriptor",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Descriptor.RecordDigest[0] ^= 0xFF
			},
			want: lifecycle.ErrDescriptorMismatch,
		},
		{
			name: "lying fragment count in the descriptor",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Descriptor.FragmentCount++
			},
			want: lifecycle.ErrDescriptorMismatch,
		},
		{
			name: "tampered payload fragment",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Envelope.Payload.Fragments[1][0] ^= 0xFF
			},
			want: lifecycle.ErrDescriptorMismatch,
		},
		{
			name: "wrong payload variant",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Envelope.Payload.Variant = profiles.VariantBlockApplied
			},
			want: proto.ErrVariantMismatch,
		},
		{
			name: "invalid semantic revision",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Envelope.Semantic.Revision = 1 // Applied carries revision 0
			},
			want: proto.ErrSemantic,
		},
		{
			name: "event id from another producer",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Envelope.EventID[0] ^= 0xFF
			},
			want: proto.ErrIdentityMismatch,
		},
		{
			name: "swapped frames",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Frames[0], e.Frames[1] = e.Frames[1], e.Frames[0]
			},
			want: lifecycle.ErrFrameMismatch,
		},
		{
			name: "missing frame",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Frames = e.Frames[:2]
			},
			want: lifecycle.ErrFrameMismatch,
		},
		{
			name: "frame with other fragment bytes",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Frames[1] = reframe(t, e.Frames[1], func(f *proto.TransportFrame) {
					f.Complete.FragmentBytes[0] ^= 0xFF
				})
			},
			want: lifecycle.ErrFrameMismatch,
		},
		{
			name: "frame from another network",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Frames[0] = reframe(t, e.Frames[0], func(f *proto.TransportFrame) {
					f.Network.ChainID[0] ^= 0xFF
				})
			},
			want: lifecycle.ErrFrameMismatch,
		},
		{
			name: "frame with another record digest",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Frames[2] = reframe(t, e.Frames[2], func(f *proto.TransportFrame) {
					f.RecordDigest[0] ^= 0xFF
				})
			},
			want: lifecycle.ErrFrameMismatch,
		},
		{
			name: "frame with another event id",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Frames[1] = reframe(t, e.Frames[1], func(f *proto.TransportFrame) {
					f.EventID[0] ^= 0xFF
				})
			},
			want: lifecycle.ErrFrameMismatch,
		},
		{
			name: "frame with another fragment count",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Frames[1] = reframe(t, e.Frames[1], func(f *proto.TransportFrame) {
					f.FragmentCount++
				})
			},
			want: lifecycle.ErrFrameMismatch,
		},
		{
			name: "part form instead of a complete fragment",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Frames[2] = reframe(t, e.Frames[2], func(f *proto.TransportFrame) {
					data := f.Complete.FragmentBytes
					f.Complete = nil
					f.Part = &proto.FragmentPart{PartIndex: 0, PartCount: 1,
						FragmentTotalBytes: uint64(len(data)), Data: data}
				})
			},
			want: lifecycle.ErrFrameMismatch,
		},
		{
			name: "frames not sharing one chain sequence",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Frames[2] = reframe(t, e.Frames[2], func(f *proto.TransportFrame) {
					f.ChainSequence++
				})
			},
			want: lifecycle.ErrFrameMismatch,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broken := schemaEvent(t, 0, txApplied(1, decisionTx, 1), 0xF0)
			tc.corrupt(&broken)
			if _, err := broken.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("validate error = %v, want %v", err, tc.want)
			}
			s := lifecycle.NewSchema()
			if _, err := s.Apply(broken); !errors.Is(err, tc.want) {
				t.Fatalf("apply error = %v, want %v", err, tc.want)
			}
			if s.Len() != 0 {
				t.Errorf("records = %d, want 0 after the rejection", s.Len())
			}
		})
	}

	t.Run("chain sequence not above the ordinal", func(t *testing.T) {
		broken := schemaEvent(t, 5, txApplied(1, decisionTx, 1), 0xF1)
		broken.Frames[0] = reframe(t, broken.Frames[0], func(f *proto.TransportFrame) {
			f.ChainSequence = 5 // sequence base 0: chain_sequence = base + ordinal
		})
		if _, err := broken.Validate(); !errors.Is(err, lifecycle.ErrFrameMismatch) {
			t.Fatalf("validate error = %v, want ErrFrameMismatch", err)
		}
	})
}

// TestDescriptorMismatchNamesTheField pins that a lying descriptor is
// rejected with the offending field named, for every descriptor field the
// envelope recomputation can contradict.
func TestDescriptorMismatchNamesTheField(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(*lifecycle.SchemaEvent)
		reason  string
	}{
		{
			name: "event id",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Descriptor.EventID[0] ^= 0xFF
			},
			reason: "event_id",
		},
		{
			name: "family",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Descriptor.Family = proto.FamilyBlock
			},
			reason: "family",
		},
		{
			name: "kind",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Descriptor.Kind = proto.KindRemoved
			},
			reason: "kind",
		},
		{
			name: "record length",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Descriptor.RecordLength++
			},
			reason: "record_length",
		},
		{
			name: "manifest digest",
			corrupt: func(e *lifecycle.SchemaEvent) {
				e.Descriptor.FragmentManifestDigest[0] ^= 0xFF
			},
			reason: "fragment_manifest_digest",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broken := schemaEvent(t, 0, txApplied(1, decisionTx, 1), 0xF2)
			tc.corrupt(&broken)
			_, err := broken.Validate()
			if !errors.Is(err, lifecycle.ErrDescriptorMismatch) {
				t.Fatalf("validate error = %v, want ErrDescriptorMismatch", err)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("error %q does not name the offending field %q", err, tc.reason)
			}
		})
	}
}

// TestNewSchemaEventRejectsBadInputs pins the writer-side assembly checks.
func TestNewSchemaEventRejectsBadInputs(t *testing.T) {
	env := func() *proto.Envelope { return schemaEnvelope(t, txApplied(1, decisionTx, 1), 1, 0x40) }

	t.Run("nil envelope", func(t *testing.T) {
		if _, err := lifecycle.NewSchemaEvent(0, 1, nil, schemaNet, schemaEpoch); !errors.Is(err, proto.ErrInvalidValue) {
			t.Fatalf("error = %v, want proto.ErrInvalidValue", err)
		}
	})
	t.Run("sequence base zero", func(t *testing.T) {
		if _, err := lifecycle.NewSchemaEvent(0, 0, env(), schemaNet, schemaEpoch); err == nil {
			t.Fatal("sequence base 0 was accepted")
		}
	})
	t.Run("sequence overflow", func(t *testing.T) {
		if _, err := lifecycle.NewSchemaEvent(1, math.MaxUint64, env(), schemaNet, schemaEpoch); err == nil {
			t.Fatal("sequence overflow was accepted")
		}
	})
	t.Run("network mismatch", func(t *testing.T) {
		other := schemaNet
		other.GenesisHash[0] ^= 0xFF
		if _, err := lifecycle.NewSchemaEvent(0, 1, env(), other, schemaEpoch); !errors.Is(err, lifecycle.ErrFrameMismatch) {
			t.Fatalf("error = %v, want ErrFrameMismatch", err)
		}
	})
	t.Run("network family outside the registry", func(t *testing.T) {
		other := schemaNet
		other.Family = proto.NetworkFamily(2)
		if _, err := lifecycle.NewSchemaEvent(0, 1, env(), other, schemaEpoch); !errors.Is(err, proto.ErrClosedRegistry) {
			t.Fatalf("error = %v, want proto.ErrClosedRegistry", err)
		}
	})
	t.Run("fragment count below the record minimum", func(t *testing.T) {
		short := env()
		short.Payload.Fragments = short.Payload.Fragments[:2]
		if _, err := lifecycle.NewSchemaEvent(0, 1, short, schemaNet, schemaEpoch); !errors.Is(err, proto.ErrFragments) {
			t.Fatalf("error = %v, want proto.ErrFragments", err)
		}
	})
	t.Run("zero publication epoch", func(t *testing.T) {
		if _, err := lifecycle.NewSchemaEvent(0, 1, env(), schemaNet, proto.ID16{}); err == nil {
			t.Fatal("zero publication epoch was accepted")
		}
	})
}

// TestSchemaValidateReturnsVerifiedDescriptor pins the Validate contract:
// the returned descriptor is the envelope's semantic identity as a detached
// copy, and the same event validates to the same descriptor.
func TestSchemaValidateReturnsVerifiedDescriptor(t *testing.T) {
	d := txApplied(1, decisionTx, 1)
	event := schemaEvent(t, 4, d, 0x50)

	got, err := event.Validate()
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if want := digestOf(t, d); lifecycle.Digest(mustDigest(t, got)) != want {
		t.Errorf("validated descriptor digest differs from the envelope descriptor")
	}

	// The returned descriptor is detached: mutating its inclusion binding
	// does not reach the event's envelope.
	got.Activation.Transaction.NativePosition = 99
	if event.Envelope.Semantic.Activation.Transaction.NativePosition != 0 {
		t.Error("Validate returned a descriptor that aliases the envelope")
	}

	again, err := event.Validate()
	if err != nil {
		t.Fatalf("second validate: %v", err)
	}
	if lifecycle.Digest(mustDigest(t, again)) != digestOf(t, d) {
		t.Error("Validate is not deterministic")
	}
}

// TestSchemaAutomatonComposesInner pins the delegation surface of the
// schema automaton over the semantic-half state machine.
func TestSchemaAutomatonComposesInner(t *testing.T) {
	s := lifecycle.NewSchema()
	if s.Inner() == nil {
		t.Fatal("Inner returned nil")
	}
	if s.Len() != 0 {
		t.Errorf("records = %d, want 0", s.Len())
	}
	event := schemaEvent(t, 0, txApplied(1, decisionTx, 1), 0x60)
	mustSchemaApply(t, s, event)
	key := lifecycle.KeyOf(txApplied(1, decisionTx, 1))
	if s.Len() != 1 {
		t.Errorf("records = %d, want 1", s.Len())
	}
	if _, ok := s.Record(lifecycle.ActivationKey{Network: net2}); ok {
		t.Error("Record returned a record for an unseen key")
	}
	records := s.Records()
	if len(records) != 1 {
		t.Fatalf("Records returned %d records, want 1", len(records))
	}
	if want := lifecycle.Digest(event.Descriptor.RecordDigest); records[key].Evidence[0] != want {
		t.Errorf("slot 0 digest = %s, want the record digest %s", records[key].Evidence[0], want)
	}
}

// mustDigest returns the descriptor digest of an already validated
// descriptor, for equality checks against fixture digests.
func mustDigest(t *testing.T, d semantic.SemanticDescriptor) semantic.Hash32 {
	t.Helper()
	digest, err := d.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return digest
}
