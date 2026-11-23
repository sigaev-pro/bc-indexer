// E07-02.scope (#73): the product boundary audit. The trace boundary runs
// native source → normalization → NATS: no data exists for blocks outside
// the live capture window (initial-sync history and downtime ranges are
// never exported), the published transaction set is exactly the fixture
// set — no invented, no missing — and the source metadata the product
// publishes is proven not to be a completeness claim: the block record
// carries its closed schema fields and nothing that asserts chain
// completeness (TASK_DECOMPOSITION E07-02.scope; REQ-SCP-02, REQ-SCP-03,
// REQ-SCP-06).
package acceptance

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// scopeServer builds a one-stream in-memory NATS for a slot.
func scopeServer(t *testing.T, slot networkSlot) *publisher.MemoryJetStream {
	t.Helper()
	server, err := publisher.NewMemoryJetStream(publisher.StreamConfig{
		Name: slot.stream, Subjects: []string{slot.prefix + ".>"},
		MaxMsgSize: acceptanceBudget,
	})
	if err != nil {
		t.Fatalf("slot %s: server: %v", slot.name, err)
	}
	return server
}

// TestScopeNoDataOutsideCaptureWindow verifies REQ-SCP-03 per slot: a
// history block below the window, a block beyond the window end and a
// downtime gap between two windows produce no obligation, no batch and no
// NATS message, while saved obligations from inside the window complete
// and the new boundary records without a window of invented data.
func TestScopeNoDataOutsideCaptureWindow(t *testing.T) {
	for _, slot := range networkSlots {
		t.Run(slot.name, func(t *testing.T) {
			server := scopeServer(t, slot)
			h := newStack(slot, server, 100, 102)

			// Initial-sync history: the height below the window start is
			// the chain the adapter synced before admission. It exports
			// nothing — only new live H+1 is captured.
			if _, err := h.source.admitLive(fixtureBlock(99, 3, 0)); !errors.Is(err, ErrOutsideWindow) {
				t.Fatalf("history height 99 admitted: %v", err)
			}
			// Beyond the window end: not a live range yet.
			if _, err := h.source.admitLive(fixtureBlock(103, 1, 0)); !errors.Is(err, ErrOutsideWindow) {
				t.Fatalf("height 103 beyond the window admitted: %v", err)
			}

			// Inside the window: one block runs to retirement, the second
			// is left mid-flight at its intent — a saved obligation.
			first, err := h.source.admitLive(fixtureBlock(100, 2, 0))
			if err != nil {
				t.Fatalf("admit 100: %v", err)
			}
			h.deliverDecision(t, server, first)
			second, err := h.source.admitLive(fixtureBlock(101, 3, 0))
			if err != nil {
				t.Fatalf("admit 101: %v", err)
			}
			cutIntent(t, h, second)

			// A downtime gap: the process was down for heights 102–104;
			// the window restarts at 105. The gap heights are refused even
			// after the move, and nothing exports data for them.
			h.crash()
			h.source.setWindow(105, 110)
			for _, missed := range []uint64{102, 104} {
				if _, err := h.source.admitLive(fixtureBlock(missed, 2, 0)); !errors.Is(err, ErrOutsideWindow) {
					t.Fatalf("downtime height %d admitted after the window move: %v", missed, err)
				}
			}

			// The new live range continues at 105, and the recovery replays
			// every admitted obligation in order: the saved intent of 101
			// finishes before the new range publishes.
			if _, err := h.source.admitLive(fixtureBlock(105, 1, 0)); err != nil {
				t.Fatalf("admit 105: %v", err)
			}
			h.deliverAll(t, server)

			// The stream holds exactly the events of 100, 101 and 105:
			// the gap heights left no trace, and the saved obligation
			// completed.
			report := verifyStream(t, server, slot, h.source)
			report.mustBeClean(t, slot)
			heights := blockHeightsOf(t, report)
			want := map[uint64]bool{100: true, 101: true, 105: true}
			if len(heights) != len(want) {
				t.Fatalf("slot %s: published block heights %v, want %v", slot.name, heights, want)
			}
			for height := range want {
				if !heights[height] {
					t.Fatalf("slot %s: height %d missing from the published set %v",
						slot.name, height, heights)
				}
			}
		})
	}
}

// TestScopeNoInventedTransactions verifies REQ-SCP-04 from the boundary
// side: the published transaction set is exactly the fixture set — full
// identity comparison, never counts — and the comparator itself is proven
// awake both ways: an expected record no source produced is reported
// missing, and a stored record dropped from the expectation is reported
// unexpected (TC09 oracle sanity; a comparator that cannot fail proves
// nothing).
func TestScopeNoInventedTransactions(t *testing.T) {
	for _, slot := range networkSlots {
		t.Run(slot.name, func(t *testing.T) {
			server := scopeServer(t, slot)
			h := newStack(slot, server, 200, 210)
			first, err := h.source.admitLive(fixtureBlock(200, 3, 0))
			if err != nil {
				t.Fatalf("admit 200: %v", err)
			}
			h.deliverDecision(t, server, first)
			second, err := h.source.admitLive(fixtureBlock(201, 2, 1))
			if err != nil {
				t.Fatalf("admit 201: %v", err)
			}
			h.deliverDecision(t, server, second)

			// The exact-set verdict first: no invented, no missing
			// records of any family.
			verifyStream(t, server, slot, h.source).mustBeClean(t, slot)

			// The published transaction identities are exactly the
			// fixture transaction hashes of the two blocks.
			stored := readStream(t, server, slot)
			published := txIdentities(t, stored)
			for _, block := range []blockFixture{first.block, second.block} {
				for _, tx := range block.txs {
					if !published[txIdentityKey(tx.hash)] {
						t.Fatalf("slot %s: fixture transaction %x of height %d has no record",
							slot.name, tx.hash, block.height)
					}
				}
			}
			if got := len(published); got != len(first.block.txs)+len(second.block.txs) {
				t.Fatalf("slot %s: %d transaction records published, want exactly %d",
					slot.name, got, len(first.block.txs)+len(second.block.txs))
			}

			expected, err := oracleRecords(slot, h.source)
			if err != nil {
				t.Fatalf("oracle: %v", err)
			}

			// Missing-side control: expect a phantom transaction no source
			// admitted — the comparator must report it missing.
			phantom := proto.NewEventID(slot.producer, slot.run, 999999)
			withPhantom := copyExpected(expected)
			withPhantom[phantom] = expectedRecord{eventID: phantom, family: proto.FamilyTx,
				kind: proto.KindApplied}
			if diff := compareRecords(slot, withPhantom, stored); len(diff.missing) != 1 {
				t.Fatalf("slot %s: a phantom expected record was not reported missing: %v",
					slot.name, diff.missing)
			}

			// Unexpected-side control: drop the expectation of one stored
			// record — the comparator must report it unexpected, proving a
			// real invented record could never pass silently.
			dropped := copyExpected(expected)
			var droppedID proto.EventID
			for id, rec := range expected {
				if rec.family == proto.FamilyTx {
					droppedID = id
					delete(dropped, id)
					break
				}
			}
			diff := compareRecords(slot, dropped, stored)
			if len(diff.unexpected) != 1 || len(diff.missing) != 0 || len(diff.mismatch) != 0 {
				t.Fatalf("slot %s: dropping stored record %x produced missing=%v unexpected=%v mismatch=%v",
					slot.name, droppedID, diff.missing, diff.unexpected, diff.mismatch)
			}
		})
	}
}

// TestScopeMetadataIsNotACompletenessClaim verifies REQ-SCP-06 per slot:
// the block record carries the native header metadata fields of its
// closed schema — height, transaction count, hash, optional source extras
// — and nothing else. No published field asserts chain completeness,
// archive coverage, downstream delivery or history: the product's own
// metadata is a per-block record, never a completeness claim, and a
// PubAck or a fixture PASS never denotes one either.
func TestScopeMetadataIsNotACompletenessClaim(t *testing.T) {
	for _, slot := range networkSlots {
		t.Run(slot.name, func(t *testing.T) {
			server := scopeServer(t, slot)
			h := newStack(slot, server, 300, 310)
			plain, err := h.source.admitLive(fixtureBlock(300, 2, 0))
			if err != nil {
				t.Fatalf("admit 300: %v", err)
			}
			system, err := h.source.admitLive(systemFixtureBlock(301, 1))
			if err != nil {
				t.Fatalf("admit 301: %v", err)
			}
			h.deliverDecision(t, server, plain)
			h.deliverDecision(t, server, system)
			report := verifyStream(t, server, slot, h.source)
			report.mustBeClean(t, slot)

			for _, check := range []struct {
				label  string
				d      decisionSpec
				fields uint64
			}{
				{"plain block", plain, 3},
				{"system block", system, 4}, // the optional metadata field is present
			} {
				eventID := proto.NewEventID(slot.producer, slot.run, eventSequence(check.d, 0))
				rec, ok := report.records[eventID]
				if !ok {
					t.Fatalf("slot %s: the %s record is absent", slot.name, check.label)
				}
				if len(rec.nodes) == 0 || rec.nodes[0].Kind != codec.ValueMessage {
					t.Fatalf("slot %s: the %s record has no message root", slot.name, check.label)
				}
				root := rec.nodes[0]
				if root.ChildCount != check.fields {
					t.Fatalf("slot %s: the %s record carries %d fields, want exactly %d",
						slot.name, check.label, root.ChildCount, check.fields)
				}
				// The closed field set of the block schema: height,
				// transaction count, hash, optional source metadata. Any
				// other field — a completeness flag, a running total, a
				// coverage marker — is outside the product boundary.
				seen := map[uint32]bool{}
				for _, node := range rec.nodes[1:] {
					if !node.HasFieldTag {
						t.Fatalf("slot %s: the %s record carries a non-field child",
							slot.name, check.label)
					}
					if node.FieldTag < 1 || node.FieldTag > 4 {
						t.Fatalf("slot %s: the %s record carries field %d outside the closed block schema",
							slot.name, check.label, node.FieldTag)
					}
					seen[node.FieldTag] = true
				}
				if !seen[1] || !seen[2] || !seen[3] {
					t.Fatalf("slot %s: the %s record lost a mandatory field", slot.name, check.label)
				}
				if check.fields == 3 && seen[4] {
					t.Fatalf("slot %s: a block without source metadata invented field 4", slot.name)
				}
			}
		})
	}
}

// copyExpected copies an expected record set for a mutation control.
func copyExpected(expected map[proto.EventID]expectedRecord) map[proto.EventID]expectedRecord {
	out := make(map[proto.EventID]expectedRecord, len(expected))
	for id, rec := range expected {
		out[id] = rec
	}
	return out
}

// blockHeightsOf extracts the block heights of the published block
// records of a report.
func blockHeightsOf(t testingT, report streamReport) map[uint64]bool {
	t.Helper()
	heights := make(map[uint64]bool)
	for _, rec := range report.records {
		if rec.header.EventFamily != uint32(proto.FamilyBlock) {
			continue
		}
		for _, node := range rec.nodes[1:] {
			if node.HasFieldTag && node.FieldTag == 1 && node.Kind == codec.ValueUint64 {
				heights[node.Uint64] = true
			}
		}
	}
	return heights
}

// txIdentityKey keys a transaction hash for the exact-set comparison.
func txIdentityKey(hash proto.Hash32) string { return string(hash[:]) }

// txIdentities collects the transaction hashes of every published
// transaction record of a report.
func txIdentities(t testingT, report streamReport) map[string]bool {
	t.Helper()
	out := make(map[string]bool)
	for _, rec := range report.records {
		if rec.header.EventFamily != uint32(proto.FamilyTx) {
			continue
		}
		for _, node := range rec.nodes[1:] {
			if node.HasFieldTag && node.FieldTag == 1 && node.Kind == codec.ValueBytes && len(node.Value) == 32 {
				out[txIdentityKey(proto.Hash32(node.Value))] = true
			}
		}
	}
	return out
}
