// The independent reader of the acceptance suite: it reads the stored
// JetStream messages of one slot's stream, decodes every TransportFrame,
// reassembles each record with the E02 FragmentReader (the bounded reader
// that verifies wrapper identity, part continuity, fragment digests and
// the record digest), rebuilds the node tree with the codec read side and
// compares the full expected/actual identity and content sets against the
// fixture-derived oracle (TESTING_CONTRACT "Reader и независимая
// проверка"; REQ-DEV-02). Equal counts are never a verdict: missing,
// unexpected and content mismatches are reported separately.
package acceptance

import (
	"fmt"
	"sort"

	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// expectedRecord is one oracle record: the event identity, wire family and
// kind, route and node tree the fixture fixes — derived from the chain
// fixture directly, never through the normalizer under test.
type expectedRecord struct {
	eventID proto.EventID
	family  proto.Family
	kind    proto.Kind
	subject string
	nodes   []codec.TreeNode
}

// readRecord is one record the reader rebuilt from the stored transport
// frames of a stream.
type readRecord struct {
	eventID proto.EventID
	subject string
	header  codec.RecordHeader
	nodes   []codec.ReadNode
	frames  int
}

// oracleRecords derives the full expected record set of a source: every
// admitted decision's events, enumerated block first, then the body
// transactions in native order, then the optional-family events. The
// derivation walks the chain fixture, not the semantic descriptors.
func oracleRecords(slot networkSlot, source *nativeSource) (map[proto.EventID]expectedRecord, error) {
	expected := make(map[proto.EventID]expectedRecord)
	for _, d := range source.decided() {
		block := d.block
		if d.kind != semantic.KindApplied {
			target, ok := source.decision(d.target)
			if !ok {
				return nil, fmt.Errorf("oracle: decision %d targets unadmitted %d", d.sequence, d.target)
			}
			block = target.block
		}
		family, kind := protoFamilyOf(semantic.FamilyBlock), protoKindOf(d.kind)
		ordinal := uint64(0)
		add := func(nodes []codec.TreeNode, fam proto.Family) error {
			eventID := proto.NewEventID(slot.producer, slot.run, eventSequence(d, ordinal))
			subject, err := subjectOf(slot, fam, kind)
			if err != nil {
				return err
			}
			expected[eventID] = expectedRecord{eventID: eventID, family: fam, kind: kind,
				subject: subject, nodes: nodes}
			ordinal++
			return nil
		}
		if err := add(blockNodes(block), family); err != nil {
			return nil, err
		}
		for _, tx := range block.txs {
			if err := add(txNodes(tx), proto.FamilyTx); err != nil {
				return nil, err
			}
		}
		for j := uint64(0); j < uint64(block.balances); j++ {
			if err := add(optionalNodes(j), protoFamilyOf(slot.optionalFamily)); err != nil {
				return nil, err
			}
		}
	}
	return expected, nil
}

// subjectOf routes one (family, kind) pair of a slot prefix.
func subjectOf(slot networkSlot, family proto.Family, kind proto.Kind) (string, error) {
	return destination.Subject(slot.prefix, family, kind)
}

// streamReport is the full comparison result of one stream: separately
// reported missing, unexpected and content mismatches, plus the rebuilt
// records for the suites that inspect them further.
type streamReport struct {
	records    map[proto.EventID]*readRecord
	order      []proto.EventID
	missing    []string
	unexpected []string
	mismatch   []string
	decoded    int // stored messages whose frame decoded and validated
}

// empty reports a clean comparison: no missing, no unexpected, no content
// mismatch, and at least one decoded frame.
func (r streamReport) empty() bool {
	return len(r.missing) == 0 && len(r.unexpected) == 0 && len(r.mismatch) == 0 && r.decoded > 0
}

// readStream reads every stored message of a slot's stream, decodes and
// validates each TransportFrame, and reassembles every record with the
// bounded FragmentReader. Any decode, reassembly or digest failure is
// fatal: the reader never accepts a malformed or partial record.
func readStream(t testingT, server *publisher.MemoryJetStream, slot networkSlot) streamReport {
	t.Helper()
	report := streamReport{records: make(map[proto.EventID]*readRecord)}
	grouped := make(map[proto.EventID][]publisher.MemoryMessage)
	for _, msg := range server.Messages() {
		if msg.Stream != slot.stream {
			continue
		}
		frame, err := proto.DecodeTransportFrame(msg.Payload)
		if err != nil {
			t.Fatalf("slot %s: message %s does not decode: %v", slot.name, msg.MsgID, err)
		}
		if err := frame.Validate(); err != nil {
			t.Fatalf("slot %s: frame of %s is invalid: %v", slot.name, msg.MsgID, err)
		}
		report.decoded++
		if _, seen := report.records[frame.EventID]; !seen {
			report.records[frame.EventID] = &readRecord{eventID: frame.EventID, subject: msg.Subject}
			report.order = append(report.order, frame.EventID)
		}
		grouped[frame.EventID] = append(grouped[frame.EventID], msg)
	}
	for eventID, msgs := range grouped {
		reader := codec.NewFragmentReader(codec.ReaderOptions{})
		for _, msg := range msgs {
			if err := reader.Add(msg.Payload); err != nil {
				t.Fatalf("slot %s: record %x rejects its frame %s: %v", slot.name, eventID, msg.MsgID, err)
			}
		}
		fragments, err := reader.Finish()
		if err != nil {
			t.Fatalf("slot %s: record %x does not complete: %v", slot.name, eventID, err)
		}
		header, nodes, err := codec.ReadRecord(fragments)
		if err != nil {
			t.Fatalf("slot %s: record %x fails the read-side invariants: %v", slot.name, eventID, err)
		}
		rec := report.records[eventID]
		rec.header, rec.nodes, rec.frames = *header, nodes, len(msgs)
	}
	return report
}

// compareRecords compares the rebuilt records against an expected set:
// full identity sets and full content, never counts. Content covers the
// wire family and kind of the header, the route and the node tree.
func compareRecords(slot networkSlot, expected map[proto.EventID]expectedRecord, report streamReport) streamReport {
	for _, id := range report.order {
		if _, ok := expected[id]; !ok {
			report.unexpected = append(report.unexpected, fmt.Sprintf("%x", id))
			continue
		}
		want := expected[id]
		got := report.records[id]
		if got.header.EventFamily != uint32(want.family) || got.header.EventKind != uint32(want.kind) {
			report.mismatch = append(report.mismatch, fmt.Sprintf("event %x: family/kind %d/%d, want %d/%d",
				id, got.header.EventFamily, got.header.EventKind, want.family, want.kind))
			continue
		}
		if got.subject != want.subject {
			report.mismatch = append(report.mismatch, fmt.Sprintf("event %x: subject %q, want %q",
				id, got.subject, want.subject))
			continue
		}
		if !treesMatch(want.nodes, got.nodes) {
			report.mismatch = append(report.mismatch, fmt.Sprintf("event %x: record tree differs from the fixture",
				id))
		}
	}
	for id := range expected {
		if _, ok := report.records[id]; !ok {
			report.missing = append(report.missing, fmt.Sprintf("%x", id))
		}
	}
	sort.Strings(report.missing)
	sort.Strings(report.unexpected)
	sort.Strings(report.mismatch)
	return report
}

// verifyStream reads a slot's stream and compares it against the oracle
// derived from its source. It returns the report without failing, so the
// suites can assert their own verdicts on the diff; positive paths call
// report.mustBeClean.
func verifyStream(t testingT, server *publisher.MemoryJetStream, slot networkSlot, source *nativeSource) streamReport {
	t.Helper()
	expected, err := oracleRecords(slot, source)
	if err != nil {
		t.Fatalf("slot %s: oracle: %v", slot.name, err)
	}
	return compareRecords(slot, expected, readStream(t, server, slot))
}

// mustBeClean fails the test unless the report is a clean full match.
func (r streamReport) mustBeClean(t testingT, slot networkSlot) {
	t.Helper()
	if r.empty() {
		return
	}
	t.Fatalf("slot %s: stream diff — missing %v, unexpected %v, mismatch %v (%d frames decoded)",
		slot.name, r.missing, r.unexpected, r.mismatch, r.decoded)
}

// treesMatch compares an expected TreeNode tree against a decoded ReadNode
// tree: kind, placement, container facts and values must agree field by
// field.
func treesMatch(want []codec.TreeNode, got []codec.ReadNode) bool {
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		w, g := want[i], got[i]
		if w.Kind != g.Kind || w.TypeKey != g.TypeKey || w.ChildCount != g.ChildCount {
			return false
		}
		if w.HasFieldTag != g.HasFieldTag || w.FieldTag != g.FieldTag {
			return false
		}
		if w.IsElement != g.IsElement || w.ElementIndex != g.ElementIndex {
			return false
		}
		switch w.Kind {
		case codec.ValueBool:
			if w.Bool != g.Bool {
				return false
			}
		case codec.ValueUint32:
			if w.Uint32 != g.Uint32 {
				return false
			}
		case codec.ValueUint64:
			if w.Uint64 != g.Uint64 {
				return false
			}
		case codec.ValueEnum:
			if w.Enum != g.Enum {
				return false
			}
		case codec.ValueBytes, codec.ValueUTF8:
			if string(w.Value) != string(g.Value) {
				return false
			}
		}
	}
	return true
}

// messagesOf returns the stored messages of a slot's stream in sequence
// order (the physical evidence, duplicates retained by identity).
func messagesOf(server *publisher.MemoryJetStream, slot networkSlot) []publisher.MemoryMessage {
	var out []publisher.MemoryMessage
	for _, msg := range server.Messages() {
		if msg.Stream == slot.stream {
			out = append(out, msg)
		}
	}
	return out
}

// snapshotMessages captures the byte-exact stored content of a slot's
// stream: message id to payload, the comparison base of the isolation
// suite.
func snapshotMessages(server *publisher.MemoryJetStream, slot networkSlot) map[string][]byte {
	out := make(map[string][]byte)
	for _, msg := range messagesOf(server, slot) {
		out[msg.MsgID] = append([]byte(nil), msg.Payload...)
	}
	return out
}

// assertSnapshotUnchanged verifies a slot's stream still holds exactly the
// snapshotted messages, byte for byte.
func assertSnapshotUnchanged(t testingT, slot networkSlot, server *publisher.MemoryJetStream, before map[string][]byte) {
	t.Helper()
	current := snapshotMessages(server, slot)
	for id, payload := range before {
		got, ok := current[id]
		if !ok {
			t.Fatalf("slot %s: message %s vanished from its stream", slot.name, id)
		}
		if string(got) != string(payload) {
			t.Fatalf("slot %s: message %s changed its bytes", slot.name, id)
		}
	}
	for id := range current {
		if _, ok := before[id]; !ok {
			t.Fatalf("slot %s: message %s appeared without its network acting", slot.name, id)
		}
	}
}
