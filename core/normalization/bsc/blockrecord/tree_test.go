package blockrecord

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// A mapping the registry does not name cannot build a tree: each mismatch
// is a named error, and no node is returned.
func TestTreeWriterRefusesWhatTheRegistryDoesNotName(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		wraps        error
		write        func(*treeWriter)
	}{
		{"unknown field", `message Envelope has no field "profile"`, proto.ErrClosedRegistry,
			func(w *treeWriter) { w.utf8("profile", "x") }},
		{"field of another kind", "field schema_version is uint32 in the registry and was mapped as utf8",
			proto.ErrInvalidValue, func(w *treeWriter) { w.utf8("schema_version", "2") }},
		{"scalar mapped as a message", "field schema_version is uint32 in the registry and was mapped as message",
			proto.ErrInvalidValue, func(w *treeWriter) { w.push("schema_version") }},
		{"message mapped as a scalar", "field network is message in the registry and was mapped as uint64",
			proto.ErrInvalidValue, func(w *treeWriter) { w.uint64("network", 1) }},
		{"bytes of another length", "field producer_id holds 15 bytes, want 16", proto.ErrInvalidValue,
			func(w *treeWriter) { w.bytes("producer_id", make([]byte, 15)) }},
		{"unregistered enum value", "value 2 is not registered in enum CaptureMode (field capture_mode)",
			proto.ErrClosedRegistry, func(w *treeWriter) { w.enum("capture_mode", 2) }},
		{"unclosed message", "1 messages of the record tree were not closed", proto.ErrInvalidValue,
			func(w *treeWriter) { w.push("source") }},
		{"nested field of another kind", "field chain_id is bytes in the registry and was mapped as message",
			proto.ErrInvalidValue, func(w *treeWriter) { w.push("network"); w.push("chain_id") }},
		{"unregistered enum value of a nested message",
			"value 7 is not registered in enum NetworkFamily (field family)", proto.ErrClosedRegistry,
			func(w *treeWriter) { w.push("network"); w.enum("family", 7); w.pop() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newTreeWriter(proto.LogicalSchemaV2(), proto.TypeEnvelope)
			tc.write(w)
			nodes, err := w.finish()
			if err == nil || !errors.Is(err, tc.wraps) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("error %v, want %v naming %q", err, tc.wraps, tc.reason)
			}
			if nodes != nil {
				t.Fatalf("a failed mapping returned %d nodes", len(nodes))
			}
		})
	}
}

// The first failure stops the mapping: later calls append nothing.
func TestTreeWriterStopsAtTheFirstFailure(t *testing.T) {
	w := newTreeWriter(proto.LogicalSchemaV2(), proto.TypeEnvelope)
	w.uint32("schema_version", 2)
	w.utf8("event_id", "x")
	w.uint64("run_id", 1)
	w.push("source")
	if len(w.nodes) != 2 || len(w.open) != 1 {
		t.Fatalf("%d nodes and %d open messages after the failure", len(w.nodes), len(w.open))
	}
	if _, err := w.finish(); err == nil || !strings.Contains(err.Error(), "field event_id") {
		t.Fatalf("finish returned %v", err)
	}
}

// A valid mapping produces the nodes with the tags and type keys of the
// registry.
func TestTreeWriterBuildsTheNodesOfTheRegistry(t *testing.T) {
	in := validInput()
	id := eventIDOf(in, 9)
	w := newTreeWriter(proto.LogicalSchemaV2(), proto.TypeEnvelope)
	w.uint32("schema_version", 2)
	w.push("network")
	writeNetwork(w, in.Network)
	w.pop()
	w.bytes("event_id", id[:])
	w.bytes("producer_id", in.ProducerID[:])
	w.bytes("run_id", in.RunID[:])
	w.push("source")
	w.bytes("node_instance_id", in.NodeInstanceID[:])
	w.bytes("writer_epoch", in.WriterEpoch[:])
	w.uint64("source_sequence", 1)
	w.pop()
	w.enum("capture_mode", proto.CaptureModeLive)
	w.push(proto.PayloadBlockApplied)
	w.pop()
	nodes, err := w.finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	want := []codec.TreeNode{
		{Kind: codec.ValueMessage, TypeKey: proto.TypeEnvelope, ChildCount: 8},
		{FieldTag: 1, HasFieldTag: true, Kind: codec.ValueUint32, Uint32: 2},
		{FieldTag: 2, HasFieldTag: true, Kind: codec.ValueMessage, TypeKey: proto.TypeNetworkIdentity, ChildCount: 3},
		{FieldTag: 1, HasFieldTag: true, Kind: codec.ValueEnum, Enum: 1},
		{FieldTag: 2, HasFieldTag: true, Kind: codec.ValueBytes, Value: in.Network.ChainID[:]},
		{FieldTag: 3, HasFieldTag: true, Kind: codec.ValueBytes, Value: in.Network.GenesisHash[:]},
		{FieldTag: 4, HasFieldTag: true, Kind: codec.ValueBytes, Value: id[:]},
		{FieldTag: 5, HasFieldTag: true, Kind: codec.ValueBytes, Value: in.ProducerID[:]},
		{FieldTag: 6, HasFieldTag: true, Kind: codec.ValueBytes, Value: in.RunID[:]},
		{FieldTag: 7, HasFieldTag: true, Kind: codec.ValueMessage, TypeKey: proto.TypeSourceRef, ChildCount: 3},
		{FieldTag: 1, HasFieldTag: true, Kind: codec.ValueBytes, Value: in.NodeInstanceID[:]},
		{FieldTag: 2, HasFieldTag: true, Kind: codec.ValueBytes, Value: in.WriterEpoch[:]},
		{FieldTag: 3, HasFieldTag: true, Kind: codec.ValueUint64, Uint64: 1},
		{FieldTag: 8, HasFieldTag: true, Kind: codec.ValueEnum, Enum: proto.CaptureModeLive},
		{FieldTag: proto.PayloadTagBlockApplied, HasFieldTag: true, Kind: codec.ValueMessage, TypeKey: proto.TypeBlockApplied},
	}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("nodes %+v", nodes)
	}
	// The nodes are a well-formed record tree.
	if _, err := codec.BuildRecord(id, testNetwork(in), proto.FamilyBlock, proto.KindApplied, proto.TypeEnvelope,
		nodes, func(uint64, []byte) error { return nil }); err != nil {
		t.Fatalf("BuildRecord: %v", err)
	}
}

// A finality reference carries the fields its kind allows and no other.
func TestTreeWriterReferenceKinds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ref   block.ReferenceObservation
		wants map[string]string
	}{
		{"known", block.Known(block.AnchorRef{Height: 5, Hash: hashOf(1)}, block.OriginNativeRestart),
			map[string]string{"kind": "enum:1", "anchor.height": "uint64:5",
				"anchor.hash": "bytes:" + hashOf(1).String(), "origin": "enum:2"}},
		{"unavailable", block.ReferenceObservation{Kind: block.ReferenceUnavailable,
			Reason: block.UnavailableSourceOffline}, map[string]string{"kind": "enum:4", "reason": "enum:1"}},
		{"not observed", block.NotObserved(), map[string]string{"kind": "enum:2"}},
		{"native reference unset", block.ReferenceObservation{Kind: block.ReferenceNativeUnset},
			map[string]string{"kind": "enum:3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newTreeWriter(proto.LogicalSchemaV2(), proto.TypeBscMarkers)
			writeReference(w, "effective_finalized", tc.ref)
			nodes, err := w.finish()
			if err != nil {
				t.Fatalf("finish: %v", err)
			}
			paths := builtPaths(t, nodes)
			base := "BscMarkers.effective_finalized."
			var got []string
			for path, node := range paths {
				// Leaves only: the anchor is a message of two fields.
				if strings.HasPrefix(path, base) && node.kind != codec.ValueMessage {
					got = append(got, strings.TrimPrefix(path, base))
				}
			}
			var want []string
			for name := range tc.wants {
				want = append(want, name)
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("fields %v, want %v", got, want)
			}
			for name, expected := range tc.wants {
				if got := render(paths[base+name]); got != expected {
					t.Fatalf("%s is %s, want %s", name, got, expected)
				}
			}
		})
	}
}

// The BSC details carry the vote snapshot when the typed model has one. The
// builder of the block event never sets it; the mapping is complete anyway.
func TestTreeWriterBscDetails(t *testing.T) {
	w := newTreeWriter(proto.LogicalSchemaV2(), proto.TypeFamilyDetails)
	writeBscDetails(w, &block.BscBlockDetails{
		ParliaSystemTransactionCount: block.Some(uint64(2)),
		VoteSnapshot: block.Some(block.ParliaVoteSnapshot{Justified: block.AnchorRef{Height: 9, Hash: hashOf(1)},
			Finalized: block.AnchorRef{Height: 8, Hash: hashOf(2)}}),
	})
	nodes, err := w.finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	paths := builtPaths(t, nodes)
	base := "FamilyDetails.bsc."
	for path, expected := range map[string]string{
		base + "parlia_system_transaction_count": "uint64:2",
		base + "vote_snapshot.justified.height":  "uint64:9",
		base + "vote_snapshot.justified.hash":    "bytes:" + hashOf(1).String(),
		base + "vote_snapshot.finalized.height":  "uint64:8",
	} {
		if got := render(paths[path]); got != expected {
			t.Fatalf("%s is %s, want %s", path, got, expected)
		}
	}
	if len(nodes) != 10 {
		t.Fatalf("%d nodes", len(nodes))
	}
}

// Every field kind of the registry has its value kind of the tree.
func TestValueKindFollowsTheFieldKinds(t *testing.T) {
	for kind, want := range map[proto.FieldKind]codec.ValueKind{
		proto.KindMessage: codec.ValueMessage, proto.KindList: codec.ValueList, proto.KindBool: codec.ValueBool,
		proto.KindUint32: codec.ValueUint32, proto.KindUint64: codec.ValueUint64, proto.KindEnum: codec.ValueEnum,
		proto.KindBytes: codec.ValueBytes, proto.KindUTF8: codec.ValueUTF8,
	} {
		if got := valueKind(kind); got != want {
			t.Fatalf("field kind %s is %s, want %s", kind, got, want)
		}
	}
}

// testNetwork is the frame network identity of an input.
func testNetwork(in Input) proto.NetworkIdentity {
	return proto.NetworkIdentity{Family: proto.NetworkFamilyEVM, ChainID: in.Network.ChainID,
		GenesisHash: in.Network.GenesisHash}
}
