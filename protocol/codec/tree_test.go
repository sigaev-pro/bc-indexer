// Tests of the TreeCodec: build/read round-trips with blob splitting and
// multi-fragment items, determinism, cross-checks against the READY
// descriptors of protocol/proto, and the structural rejections of both
// directions (tree rules, blob chunk rules, seal and header totals, packing
// maximality).
package codec

import (
	"bytes"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

func TestTreeRoundTripSample(t *testing.T) {
	fragments := buildSampleFragments(t)
	header, nodes, err := ReadRecord(fragments)
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	want := sampleTree()
	if int(header.NodeCount) != len(want) {
		t.Fatalf("node count %d, want %d", header.NodeCount, len(want))
	}
	if len(nodes) != len(want) {
		t.Fatalf("read %d nodes, want %d", len(nodes), len(want))
	}
	// Every node's kind, placement and value must survive.
	for i := range want {
		got := nodes[i]
		if got.Kind != want[i].Kind {
			t.Fatalf("node %d kind %s, want %s", i, got.Kind, want[i].Kind)
		}
		if got.HasFieldTag != want[i].HasFieldTag || got.FieldTag != want[i].FieldTag {
			t.Fatalf("node %d field tag mismatch: %+v", i, got)
		}
		if got.IsElement != want[i].IsElement || got.ElementIndex != want[i].ElementIndex {
			t.Fatalf("node %d element placement mismatch: %+v", i, got)
		}
		if got.TypeKey != want[i].TypeKey || got.ChildCount != want[i].ChildCount {
			t.Fatalf("node %d container facts mismatch: %+v", i, got)
		}
		switch want[i].Kind {
		case ValueBool:
			if got.Bool != want[i].Bool {
				t.Fatalf("node %d bool mismatch", i)
			}
		case ValueUint32:
			if got.Uint32 != want[i].Uint32 {
				t.Fatalf("node %d uint32 mismatch", i)
			}
		case ValueUint64:
			if got.Uint64 != want[i].Uint64 {
				t.Fatalf("node %d uint64 mismatch", i)
			}
		case ValueEnum:
			if got.Enum != want[i].Enum {
				t.Fatalf("node %d enum mismatch", i)
			}
		case ValueBytes, ValueUTF8:
			if !bytes.Equal(got.Value, want[i].Value) {
				t.Fatalf("node %d value mismatch: %d vs %d bytes", i, len(got.Value), len(want[i].Value))
			}
		}
	}
	// Parent links: node 0 is the root, node 1 its first child, and so on.
	if nodes[0].HasParent {
		t.Fatal("root carries a parent")
	}
	if !nodes[1].HasParent || nodes[1].ParentID != 0 || nodes[1].FieldTag != 1 {
		t.Fatalf("node 1 placement: %+v", nodes[1])
	}
	if !nodes[6].HasParent || nodes[6].ParentID != 5 || nodes[6].ElementIndex != 0 {
		t.Fatalf("node 6 placement: %+v", nodes[6])
	}
}

// readBackToTree projects ReadNodes back into builder TreeNodes.
func readBackToTree(nodes []ReadNode) []TreeNode {
	out := make([]TreeNode, len(nodes))
	for i := range nodes {
		out[i] = TreeNode{FieldTag: nodes[i].FieldTag, HasFieldTag: nodes[i].HasFieldTag,
			ElementIndex: nodes[i].ElementIndex, IsElement: nodes[i].IsElement,
			Kind: nodes[i].Kind, TypeKey: nodes[i].TypeKey, ChildCount: nodes[i].ChildCount,
			Bool: nodes[i].Bool, Uint32: nodes[i].Uint32, Uint64: nodes[i].Uint64,
			Enum: nodes[i].Enum, Value: nodes[i].Value}
	}
	return out
}

func TestTreeRebuildIsByteIdentical(t *testing.T) {
	fragments := buildSampleFragments(t)
	_, nodes, err := ReadRecord(fragments)
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	rebuilt := buildTreeFragments(t, readBackToTree(nodes))
	if len(rebuilt) != len(fragments) {
		t.Fatalf("rebuild produced %d fragments, want %d", len(rebuilt), len(fragments))
	}
	for i := range fragments {
		if !bytes.Equal(fragments[i], rebuilt[i]) {
			t.Fatalf("fragment %d differs after read-rebuild", i)
		}
	}
}

func TestTreeBuildDeterministic(t *testing.T) {
	first := buildSampleFragments(t)
	second := buildSampleFragments(t)
	if len(first) != len(second) {
		t.Fatal("two builds produced different fragment counts")
	}
	for i := range first {
		if !bytes.Equal(first[i], second[i]) {
			t.Fatalf("fragment %d differs between builds", i)
		}
	}
}

func TestTreeBlobChunkSplitting(t *testing.T) {
	cases := []int{1, BlobChunkMaxData - 1, BlobChunkMaxData, BlobChunkMaxData + 1, 3 * BlobChunkMaxData, 3*BlobChunkMaxData + 17}
	for _, size := range cases {
		fragments := buildTreeFragments(t, bigBlobTree(size))
		_, nodes, err := ReadRecord(fragments)
		if err != nil {
			t.Fatalf("blob %d: %v", size, err)
		}
		if got := len(nodes[1].Value); got != size {
			t.Fatalf("blob %d reassembled to %d bytes", size, got)
		}
		if !bytes.Equal(nodes[1].Value, testPattern(0x5A, size)) {
			t.Fatalf("blob %d content differs", size)
		}
	}
}

func TestTreeUTF8RuneSplitAcrossChunkBoundary(t *testing.T) {
	// One four-byte rune straddling the 16384-byte chunk boundary.
	value := testPatternValidUTF8(t, BlobChunkMaxData-2)
	value = append(value, 0xF0, 0x9F, 0x9C, 0x9B) // "🜛" starts in chunk 0, ends in chunk 1
	value = append(value, testPatternValidUTF8(t, 100)...)
	nodes := []TreeNode{
		{Kind: ValueMessage, TypeKey: 100, ChildCount: 1},
		{Kind: ValueUTF8, HasFieldTag: true, FieldTag: 1, Value: value},
	}
	fragments := buildTreeFragments(t, nodes)
	_, read, err := ReadRecord(fragments)
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if !bytes.Equal(read[1].Value, value) {
		t.Fatal("UTF-8 value with boundary rune did not round-trip")
	}
}

// testPatternValidUTF8 builds n ASCII bytes.
func testPatternValidUTF8(t *testing.T, n int) []byte {
	t.Helper()
	out := make([]byte, n)
	for i := range out {
		out[i] = byte('a' + i%26)
	}
	return out
}

func TestTreeManyNodesSpanItemsFragments(t *testing.T) {
	fragments := buildTreeFragments(t, manyNodesTree(5000))
	decoded := decodeAll(t, fragments)
	itemsFragments := 0
	for i, fragment := range decoded {
		if fragment.Payload == PayloadItems {
			itemsFragments++
		}
		if len(fragments[i]) > proto.FragmentMaxBytes {
			t.Fatal("fragment exceeds the bound")
		}
	}
	if itemsFragments < 2 {
		t.Fatalf("5000 nodes packed into %d items fragments, want several", itemsFragments)
	}
	header, nodes, err := ReadRecord(fragments)
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if int(header.NodeCount) != 5002 || len(nodes) != 5002 {
		t.Fatalf("node totals: header %d, nodes %d", header.NodeCount, len(nodes))
	}
}

func TestTreeBlobsSpanBlobsFragments(t *testing.T) {
	// Three blobs of two chunks each force at least two blobs fragments.
	nodes := []TreeNode{
		{Kind: ValueMessage, TypeKey: 100, ChildCount: 3},
		{Kind: ValueBytes, HasFieldTag: true, FieldTag: 1, Value: testPattern(1, 2*BlobChunkMaxData)},
		{Kind: ValueBytes, HasFieldTag: true, FieldTag: 2, Value: testPattern(2, 2*BlobChunkMaxData)},
		{Kind: ValueBytes, HasFieldTag: true, FieldTag: 3, Value: testPattern(3, 2*BlobChunkMaxData)},
	}
	fragments := buildTreeFragments(t, nodes)
	decoded := decodeAll(t, fragments)
	blobsFragments := 0
	for _, fragment := range decoded {
		if fragment.Payload == PayloadBlobs {
			blobsFragments++
		}
	}
	if blobsFragments < 2 {
		t.Fatalf("three 32 KiB blobs packed into %d blobs fragments, want several", blobsFragments)
	}
	if _, _, err := ReadRecord(fragments); err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
}

func TestTreeEmptyBlobsProduceNoBlobsFragments(t *testing.T) {
	nodes := []TreeNode{
		{Kind: ValueMessage, TypeKey: 100, ChildCount: 2},
		{Kind: ValueBytes, HasFieldTag: true, FieldTag: 1},
		{Kind: ValueUTF8, HasFieldTag: true, FieldTag: 2},
	}
	fragments := buildTreeFragments(t, nodes)
	if len(fragments) != MinimumFragmentCount {
		t.Fatalf("%d fragments, want the header/items/seal minimum", len(fragments))
	}
	decoded := decodeAll(t, fragments)
	for _, fragment := range decoded {
		if fragment.Payload == PayloadBlobs {
			t.Fatal("empty blobs produced a blobs fragment")
		}
	}
	if _, read, err := ReadRecord(fragments); err != nil {
		t.Fatalf("ReadRecord: %v", err)
	} else if len(read[1].Value) != 0 || len(read[2].Value) != 0 {
		t.Fatal("empty blobs reassembled bytes")
	}
}

func TestTreeDescriptorCrossCheckWithProto(t *testing.T) {
	fragments := buildSampleFragments(t)
	result := sampleBuildResult(t, sampleTree())
	event, frags, err := proto.BuildEventDescriptor(0, testEventID(7), proto.FamilyTx,
		proto.KindApplied, fragments)
	if err != nil {
		t.Fatalf("BuildEventDescriptor: %v", err)
	}
	if event.RecordDigest != result.RecordDigest {
		t.Fatal("record digest differs from proto.RecordDigest")
	}
	if event.RecordLength != result.RecordLength {
		t.Fatalf("record length %d, want %d", event.RecordLength, result.RecordLength)
	}
	if event.FragmentCount != result.FragmentCount {
		t.Fatalf("fragment count %d, want %d", event.FragmentCount, result.FragmentCount)
	}
	for i, frag := range frags {
		if frag.EncodedLength != uint64(len(fragments[i])) {
			t.Fatalf("fragment %d encoded length %d, want %d", i, frag.EncodedLength, len(fragments[i]))
		}
	}
}

// sampleBuildResult builds a tree and returns its summary.
func sampleBuildResult(t *testing.T, nodes []TreeNode) TreeBuildResult {
	t.Helper()
	result, err := BuildRecord(testEventID(7), testNetwork(), proto.FamilyTx,
		proto.KindApplied, 100, nodes, func(index uint64, fragment []byte) error { return nil })
	if err != nil {
		t.Fatalf("BuildRecord: %v", err)
	}
	return result
}

func TestTreeBuildRejections(t *testing.T) {
	sink := func(index uint64, fragment []byte) error { return nil }
	cases := []struct {
		name  string
		nodes []TreeNode
		want  error
	}{
		{"no nodes", nil, ErrTree},
		{"root is not a message", []TreeNode{{Kind: ValueBool, Bool: true}}, ErrTree},
		{"root carries a field tag", []TreeNode{{Kind: ValueMessage, TypeKey: 100,
			ChildCount: 0, HasFieldTag: true, FieldTag: 1}}, ErrTree},
		{"leaf with children", []TreeNode{
			{Kind: ValueMessage, TypeKey: 100, ChildCount: 1},
			{Kind: ValueBool, HasFieldTag: true, FieldTag: 1, Bool: true, ChildCount: 2},
		}, ErrTree},
		{"dangling child count", []TreeNode{
			{Kind: ValueMessage, TypeKey: 100, ChildCount: 2},
			{Kind: ValueBool, HasFieldTag: true, FieldTag: 1, Bool: true},
		}, ErrTree},
		{"extra node beyond declared count", []TreeNode{
			{Kind: ValueMessage, TypeKey: 100, ChildCount: 1},
			{Kind: ValueBool, HasFieldTag: true, FieldTag: 1, Bool: true},
			{Kind: ValueBool, HasFieldTag: true, FieldTag: 2, Bool: true},
		}, ErrTree},
		{"field tags do not ascend", []TreeNode{
			{Kind: ValueMessage, TypeKey: 100, ChildCount: 2},
			{Kind: ValueBool, HasFieldTag: true, FieldTag: 5, Bool: true},
			{Kind: ValueBool, HasFieldTag: true, FieldTag: 5, Bool: true},
		}, ErrTree},
		{"element index gap", []TreeNode{
			{Kind: ValueMessage, TypeKey: 100, ChildCount: 1},
			{Kind: ValueList, HasFieldTag: true, FieldTag: 1, ChildCount: 2},
			{Kind: ValueUint64, IsElement: true, ElementIndex: 0},
			{Kind: ValueUint64, IsElement: true, ElementIndex: 2},
		}, ErrTree},
		{"list child with field tag", []TreeNode{
			{Kind: ValueMessage, TypeKey: 100, ChildCount: 1},
			{Kind: ValueList, HasFieldTag: true, FieldTag: 1, ChildCount: 1},
			{Kind: ValueUint64, IsElement: true, ElementIndex: 0, HasFieldTag: true, FieldTag: 3},
		}, ErrTree},
		{"message child with element index", []TreeNode{
			{Kind: ValueMessage, TypeKey: 100, ChildCount: 1},
			{Kind: ValueUint64, HasFieldTag: true, FieldTag: 1, IsElement: true, ElementIndex: 0},
		}, ErrTree},
		{"node carries tag and element", []TreeNode{
			{Kind: ValueMessage, TypeKey: 100, ChildCount: 1},
			{Kind: ValueUint64, HasFieldTag: true, FieldTag: 1, IsElement: true, ElementIndex: 0},
		}, ErrTree},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildRecord(testEventID(1), testNetwork(), proto.FamilyTx,
				proto.KindApplied, 100, tc.nodes, sink)
			assertSentinel(t, tc.want, err)
		})
	}
}

func TestTreeBuildRejectsRootTypeMismatch(t *testing.T) {
	nodes := []TreeNode{{Kind: ValueMessage, TypeKey: 999}}
	_, err := BuildRecord(testEventID(1), testNetwork(), proto.FamilyTx,
		proto.KindApplied, 100, nodes, func(index uint64, fragment []byte) error { return nil })
	assertSentinel(t, ErrTree, err)
}

func TestTreeBuildRejectsInvalidInputs(t *testing.T) {
	nodes := []TreeNode{{Kind: ValueMessage, TypeKey: 100}}
	sink := func(index uint64, fragment []byte) error { return nil }
	cases := []struct {
		name string
		want error
		run  func() error
	}{
		{"zero event id", ErrInvalidValue, func() error {
			_, err := BuildRecord(proto.EventID{}, testNetwork(), proto.FamilyTx,
				proto.KindApplied, 100, nodes, sink)
			return err
		}},
		{"unknown network family", ErrClosedRegistry, func() error {
			network := testNetwork()
			network.Family = proto.NetworkFamily(4)
			_, err := BuildRecord(testEventID(1), network, proto.FamilyTx,
				proto.KindApplied, 100, nodes, sink)
			return err
		}},
		{"unknown family", ErrClosedRegistry, func() error {
			_, err := BuildRecord(testEventID(1), testNetwork(), proto.Family(9),
				proto.KindApplied, 100, nodes, sink)
			return err
		}},
		{"unknown kind", ErrClosedRegistry, func() error {
			_, err := BuildRecord(testEventID(1), testNetwork(), proto.FamilyTx,
				proto.Kind(0), 100, nodes, sink)
			return err
		}},
		{"zero root type", ErrInvalidValue, func() error {
			_, err := BuildRecord(testEventID(1), testNetwork(), proto.FamilyTx,
				proto.KindApplied, 0, nodes, sink)
			return err
		}},
		{"nil sink", ErrInvalidValue, func() error {
			_, err := BuildRecord(testEventID(1), testNetwork(), proto.FamilyTx,
				proto.KindApplied, 100, nodes, nil)
			return err
		}},
		{"invalid utf8", ErrUTF8, func() error {
			bad := []TreeNode{{Kind: ValueMessage, TypeKey: 100, ChildCount: 1},
				{Kind: ValueUTF8, HasFieldTag: true, FieldTag: 1, Value: []byte{0xC2}}}
			_, err := BuildRecord(testEventID(1), testNetwork(), proto.FamilyTx,
				proto.KindApplied, 100, bad, sink)
			return err
		}},
		{"zero tree limits", ErrInvalidValue, func() error {
			_, err := BuildRecordWithLimits(testEventID(1), testNetwork(), proto.FamilyTx,
				proto.KindApplied, 100, nodes, TreeLimits{}, sink)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSentinel(t, tc.want, tc.run())
		})
	}
}

func TestTreeBuildDepthAndBreadthLimits(t *testing.T) {
	sink := func(index uint64, fragment []byte) error { return nil }
	// Depth: a chain root -> message -> message..., limits.MaxDepth = 2.
	deep := []TreeNode{
		{Kind: ValueMessage, TypeKey: 100, ChildCount: 1},
		{Kind: ValueMessage, HasFieldTag: true, FieldTag: 1, TypeKey: 101, ChildCount: 1},
		{Kind: ValueBool, HasFieldTag: true, FieldTag: 1, Bool: true},
	}
	limits := TreeLimits{MaxDepth: 2, MaxChildrenPerNode: 4}
	_, err := BuildRecordWithLimits(testEventID(1), testNetwork(), proto.FamilyTx,
		proto.KindApplied, 100, deep, limits, sink)
	assertSentinel(t, ErrTree, err)

	// Breadth: two children under a limit of one.
	wide := []TreeNode{
		{Kind: ValueMessage, TypeKey: 100, ChildCount: 2},
		{Kind: ValueBool, HasFieldTag: true, FieldTag: 1, Bool: true},
		{Kind: ValueBool, HasFieldTag: true, FieldTag: 2, Bool: true},
	}
	limits = TreeLimits{MaxDepth: 8, MaxChildrenPerNode: 1}
	_, err = BuildRecordWithLimits(testEventID(1), testNetwork(), proto.FamilyTx,
		proto.KindApplied, 100, wide, limits, sink)
	assertSentinel(t, ErrTree, err)

	// The same shapes pass under the default limits.
	if _, err := BuildRecord(testEventID(1), testNetwork(), proto.FamilyTx,
		proto.KindApplied, 100, deep, sink); err != nil {
		t.Fatalf("deep tree under default limits: %v", err)
	}
	if _, err := BuildRecord(testEventID(1), testNetwork(), proto.FamilyTx,
		proto.KindApplied, 100, wide, sink); err != nil {
		t.Fatalf("wide tree under default limits: %v", err)
	}
}

func TestTreeReadRejectsFragmentSequenceViolations(t *testing.T) {
	base := buildSampleFragments(t)
	cases := []struct {
		name     string
		mutate   func() [][]byte
		want     error
		resealOk bool
	}{
		{"too few fragments", func() [][]byte {
			return base[:2]
		}, ErrFragments, false},
		{"seal not last", func() [][]byte {
			out := append([][]byte{}, base...)
			out[1], out[len(out)-1] = out[len(out)-1], out[1]
			return out
		}, ErrFragments, false},
		{"items after blobs", func() [][]byte {
			// A record with several blobs fragments: swap the items
			// fragment behind the first blobs fragment and renumber,
			// so only the items-after-blobs rule fires.
			out := buildTreeFragments(t, []TreeNode{
				{Kind: ValueMessage, TypeKey: 100, ChildCount: 3},
				{Kind: ValueBytes, HasFieldTag: true, FieldTag: 1, Value: testPattern(1, 2*BlobChunkMaxData)},
				{Kind: ValueBytes, HasFieldTag: true, FieldTag: 2, Value: testPattern(2, 2*BlobChunkMaxData)},
				{Kind: ValueBytes, HasFieldTag: true, FieldTag: 3, Value: testPattern(3, 2*BlobChunkMaxData)},
			})
			itemsAt, blobsAt := -1, -1
			decoded := decodeAll(t, out)
			for i, fragment := range decoded {
				if fragment.Payload == PayloadItems && itemsAt < 0 {
					itemsAt = i
				}
				if fragment.Payload == PayloadBlobs && blobsAt < 0 {
					blobsAt = i
				}
			}
			if itemsAt < 0 || blobsAt < 0 || blobsAt <= itemsAt {
				t.Fatal("fixture lacks separate items and blobs fragments")
			}
			out[itemsAt], out[blobsAt] = out[blobsAt], out[itemsAt]
			for i, raw := range out {
				out[i] = renumberFragment(t, raw, uint64(i))
			}
			return out
		}, ErrFragments, false},
		{"fragment index gap", func() [][]byte {
			out := append([][]byte{}, base...)
			out[1] = renumberFragment(t, out[1], 5)
			return out
		}, ErrFragments, false},
		{"event id mismatch", func() [][]byte {
			out := append([][]byte{}, base...)
			decoded, err := DecodeRecordFragment(out[1])
			if err != nil {
				t.Fatal(err)
			}
			decoded.EventID = testEventID(99)
			encoded, err := decoded.Encode()
			if err != nil {
				t.Fatal(err)
			}
			out[1] = encoded
			return out
		}, ErrIdentityMismatch, false},
		{"seal digest mismatch", func() [][]byte {
			out := append([][]byte{}, base...)
			decoded := decodeAll(t, out)
			decoded[len(decoded)-1].Seal.RecordDigest[0] ^= 0xFF
			encoded, err := decoded[len(decoded)-1].Encode()
			if err != nil {
				t.Fatal(err)
			}
			out[len(out)-1] = encoded
			return out
		}, ErrDigestMismatch, false},
		{"seal content bytes lie", func() [][]byte {
			out := append([][]byte{}, base...)
			decoded := decodeAll(t, out)
			decoded[len(decoded)-1].Seal.ContentBytes++
			encoded, err := decoded[len(decoded)-1].Encode()
			if err != nil {
				t.Fatal(err)
			}
			out[len(out)-1] = encoded
			return out
		}, ErrFragments, false},
		{"seal fragment count lie", func() [][]byte {
			out := append([][]byte{}, base...)
			decoded := decodeAll(t, out)
			decoded[len(decoded)-1].Seal.FragmentCount++
			encoded, err := decoded[len(decoded)-1].Encode()
			if err != nil {
				t.Fatal(err)
			}
			out[len(out)-1] = encoded
			return out
		}, ErrFragments, false},
		{"header node count lie", func() [][]byte {
			out := append([][]byte{}, base...)
			decoded := decodeAll(t, out)
			decoded[0].Header.NodeCount++
			encoded, err := decoded[0].Encode()
			if err != nil {
				t.Fatal(err)
			}
			out[0] = encoded
			return reseal(t, out)
		}, ErrFragments, true},
		{"header blob count lie", func() [][]byte {
			out := append([][]byte{}, base...)
			decoded := decodeAll(t, out)
			decoded[0].Header.BlobCount++
			encoded, err := decoded[0].Encode()
			if err != nil {
				t.Fatal(err)
			}
			out[0] = encoded
			return reseal(t, out)
		}, ErrFragments, true},
		{"header total blob bytes lie", func() [][]byte {
			out := append([][]byte{}, base...)
			decoded := decodeAll(t, out)
			decoded[0].Header.TotalBlobBytes += 7
			encoded, err := decoded[0].Encode()
			if err != nil {
				t.Fatal(err)
			}
			out[0] = encoded
			return reseal(t, out)
		}, ErrFragments, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := tc.mutate()
			_, _, err := ReadRecord(mutated)
			assertSentinel(t, tc.want, err)
		})
	}
}

func TestTreeReadRejectsTreeViolations(t *testing.T) {
	mutateItems := func(t *testing.T, mutate func(items []TypedItem)) [][]byte {
		out := buildSampleFragments(t)
		decoded := decodeAll(t, out)
		for _, fragment := range decoded {
			if fragment.Payload == PayloadItems {
				mutate(fragment.Items)
				encoded, err := fragment.Encode()
				if err != nil {
					t.Fatal(err)
				}
				// Replace the first items fragment (index 1).
				out[1] = encoded
				break
			}
		}
		return reseal(t, out)
	}
	cases := []struct {
		name   string
		mutate func(items []TypedItem)
	}{
		{"node id gap", func(items []TypedItem) {
			items[2].NodeID = 9
		}},
		{"root with parent", func(items []TypedItem) {
			items[0].HasParentID = true
			items[0].ParentID = 1
		}},
		{"parent mismatch", func(items []TypedItem) {
			items[2].ParentID = 5
		}},
		{"parent after child", func(items []TypedItem) {
			items[2].ParentID = 8
		}},
		{"child count lie", func(items []TypedItem) {
			items[1].Message.DirectChildCount++
		}},
		{"field tag order", func(items []TypedItem) {
			items[2].FieldTag, items[3].FieldTag = items[3].FieldTag, items[2].FieldTag
		}},
		{"element index gap", func(items []TypedItem) {
			items[7].ElementIndex = 5
		}},
		{"blob ref foreign id", func(items []TypedItem) {
			items[4].BytesValue.BlobID = 6
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ReadRecord(mutateItems(t, tc.mutate))
			assertSentinel(t, ErrTree, err)
		})
	}
}

func TestTreeReadRejectsRootTypeMismatch(t *testing.T) {
	out := buildSampleFragments(t)
	decoded := decodeAll(t, out)
	decoded[0].Header.RootType = 555
	encoded, err := decoded[0].Encode()
	if err != nil {
		t.Fatal(err)
	}
	out[0] = encoded
	_, _, err = ReadRecord(reseal(t, out))
	assertSentinel(t, ErrTree, err)
}

func TestTreeReadRejectsBlobViolations(t *testing.T) {
	// A record with one 2-chunk blob keeps the blob stream inspectable.
	base := buildTreeFragments(t, bigBlobTree(2*BlobChunkMaxData+50))
	mutateBlobs := func(t *testing.T, mutate func(chunks []BlobChunk) []BlobChunk) [][]byte {
		out := append([][]byte{}, base...)
		decoded := decodeAll(t, out)
		for i, fragment := range decoded {
			if fragment.Payload == PayloadBlobs {
				fragment.Blobs = mutate(fragment.Blobs)
				encoded, err := fragment.Encode()
				if err != nil {
					t.Fatal(err)
				}
				out[i] = encoded
				break
			}
		}
		return reseal(t, out)
	}
	cases := []struct {
		name   string
		want   error
		mutate func(chunks []BlobChunk) []BlobChunk
	}{
		{"offset gap", ErrBlobs, func(chunks []BlobChunk) []BlobChunk {
			chunks[1].Offset += 8
			return chunks
		}},
		{"unknown blob id", ErrBlobs, func(chunks []BlobChunk) []BlobChunk {
			chunks[1].BlobID = 9
			return chunks
		}},
		{"wrong chunk size", ErrBlobs, func(chunks []BlobChunk) []BlobChunk {
			chunks[0].Data = chunks[0].Data[:len(chunks[0].Data)-1]
			return chunks
		}},
		{"extra chunk", ErrBlobs, func(chunks []BlobChunk) []BlobChunk {
			return append(chunks, BlobChunk{BlobID: 1, Offset: 2*BlobChunkMaxData + 50,
				Data: []byte{1}})
		}},
		{"duplicate first chunk", ErrBlobs, func(chunks []BlobChunk) []BlobChunk {
			chunks[1] = chunks[0]
			return chunks
		}},
		{"digest mismatch", ErrDigestMismatch, func(chunks []BlobChunk) []BlobChunk {
			chunks[0].Data[0] ^= 0xFF
			return chunks
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ReadRecord(mutateBlobs(t, tc.mutate))
			assertSentinel(t, tc.want, err)
		})
	}
}

func TestTreeReadBlobErrorKinds(t *testing.T) {
	base := buildTreeFragments(t, bigBlobTree(2*BlobChunkMaxData+50))
	out := append([][]byte{}, base...)
	decoded := decodeAll(t, out)
	for i, fragment := range decoded {
		if fragment.Payload == PayloadBlobs {
			// Drop the final chunk: the blob stream ends mid-blob.
			fragment.Blobs = fragment.Blobs[:len(fragment.Blobs)-1]
			encoded, err := fragment.Encode()
			if err != nil {
				t.Fatal(err)
			}
			out[i] = encoded
			break
		}
	}
	_, _, err := ReadRecord(reseal(t, out))
	assertSentinel(t, ErrTruncated, err)
}

func TestTreeReadRejectsNonMaximalItemsPacking(t *testing.T) {
	fragments := buildTreeFragments(t, manyNodesTree(5000))
	decoded := decodeAll(t, fragments)
	// Find the first items fragment and split its items in two halves.
	for i, fragment := range decoded {
		if fragment.Payload != PayloadItems {
			continue
		}
		items := fragment.Items
		if len(items) < 4 {
			t.Fatal("items fragment too small to split")
		}
		half := len(items) / 2
		first := &RecordFragment{RecordFormatVersion: proto.RecordFormatVersion,
			EventID: fragment.EventID, FragmentIndex: fragment.FragmentIndex,
			Payload: PayloadItems, Items: items[:half]}
		second := &RecordFragment{RecordFormatVersion: proto.RecordFormatVersion,
			EventID: fragment.EventID, FragmentIndex: fragment.FragmentIndex + 1,
			Payload: PayloadItems, Items: items[half:]}
		firstBytes, err := first.Encode()
		if err != nil {
			t.Fatal(err)
		}
		secondBytes, err := second.Encode()
		if err != nil {
			t.Fatal(err)
		}
		out := append([][]byte{}, fragments[:i]...)
		out = append(out, firstBytes, secondBytes)
		for _, raw := range fragments[i+1:] {
			decodedFragment, err := DecodeRecordFragment(raw)
			if err != nil {
				t.Fatal(err)
			}
			decodedFragment.FragmentIndex++
			reencoded, err := decodedFragment.Encode()
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, reencoded)
		}
		_, _, err = ReadRecord(resealWithHeader(t, out))
		assertSentinel(t, ErrFragments, err)
		return
	}
	t.Fatal("no items fragment found")
}
