// Shared deterministic fixtures of the codec tests: identities, a sample
// tree exercising every node kind, blob patterns and a reproducible byte
// pattern reader for the streaming and memory tests. No I/O, no native runs.
package codec

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// testEventID builds a valid event id with a fixed producer and run.
func testEventID(sequence uint64) proto.EventID {
	var producer, run proto.ID16
	for i := range producer {
		producer[i] = byte(0x40 + i)
		run[i] = byte(0xC0 - i)
	}
	return proto.NewEventID(producer, run, sequence)
}

// testEpoch is a fixed non-zero publication epoch.
func testEpoch() proto.ID16 {
	var epoch proto.ID16
	for i := range epoch {
		epoch[i] = byte(0x10 + i)
	}
	return epoch
}

// testNetwork returns a valid EVM network identity.
func testNetwork() proto.NetworkIdentity {
	var n proto.NetworkIdentity
	n.Family = proto.NetworkFamilyEVM
	for i := range n.ChainID {
		n.ChainID[i] = byte(i * 3)
		n.GenesisHash[i] = byte(255 - i)
	}
	return n
}

// sampleTree is a tree exercising every value kind, an empty bytes node,
// an empty list, a multi-byte UTF-8 value and nested messages:
//
//	root message (type 100)
//	 +- field 1: message (type 201)
//	 |   +- field 1: bool true
//	 |   +- field 2: uint32
//	 |   +- field 3: bytes (a fixed pattern)
//	 +- field 2: list of three uint64 elements
//	 +- field 3: utf8 with multi-byte runes
//	 +- field 4: enum
//	 +- field 5: uint64
//	 +- field 6: empty bytes
//	 +- field 7: empty list
//	 +- field 8: bytes value with a rune-like invalid-utf8 pattern
func sampleTree() []TreeNode {
	return []TreeNode{
		{Kind: ValueMessage, TypeKey: 100, ChildCount: 8},
		{Kind: ValueMessage, HasFieldTag: true, FieldTag: 1, TypeKey: 201, ChildCount: 3},
		{Kind: ValueBool, HasFieldTag: true, FieldTag: 1, Bool: true},
		{Kind: ValueUint32, HasFieldTag: true, FieldTag: 2, Uint32: 0xDEADBEEF},
		{Kind: ValueBytes, HasFieldTag: true, FieldTag: 3, Value: testPattern(0xAB, 300)},
		{Kind: ValueList, HasFieldTag: true, FieldTag: 2, ChildCount: 3},
		{Kind: ValueUint64, IsElement: true, ElementIndex: 0, Uint64: 1},
		{Kind: ValueUint64, IsElement: true, ElementIndex: 1, Uint64: 1 << 40},
		{Kind: ValueUint64, IsElement: true, ElementIndex: 2, Uint64: ^uint64(0)},
		{Kind: ValueUTF8, HasFieldTag: true, FieldTag: 3, Value: []byte("ledger ✓ αβγ 🜛ᛗ end")},
		{Kind: ValueEnum, HasFieldTag: true, FieldTag: 4, Enum: 2},
		{Kind: ValueUint64, HasFieldTag: true, FieldTag: 5, Uint64: 0},
		{Kind: ValueBytes, HasFieldTag: true, FieldTag: 6, Value: nil},
		{Kind: ValueList, HasFieldTag: true, FieldTag: 7, ChildCount: 0},
		{Kind: ValueBytes, HasFieldTag: true, FieldTag: 8, Value: []byte{0xF0, 0x9F, 0x98, 0x80, 0xFF}},
	}
}

// bigBlobTree is a tree whose single bytes child forces multiple blob
// chunks and multiple blobs fragments.
func bigBlobTree(blobSize int) []TreeNode {
	return []TreeNode{
		{Kind: ValueMessage, TypeKey: 100, ChildCount: 1},
		{Kind: ValueBytes, HasFieldTag: true, FieldTag: 1, Value: testPattern(0x5A, blobSize)},
	}
}

// manyNodesTree returns a tree with count leaves under one list, enough
// items to span several TypedItems fragments.
func manyNodesTree(count int) []TreeNode {
	nodes := make([]TreeNode, 0, count+2)
	nodes = append(nodes,
		TreeNode{Kind: ValueMessage, TypeKey: 100, ChildCount: 1},
		TreeNode{Kind: ValueList, HasFieldTag: true, FieldTag: 1, ChildCount: uint64(count)})
	for i := 0; i < count; i++ {
		nodes = append(nodes, TreeNode{Kind: ValueUint64, IsElement: true,
			ElementIndex: uint64(i), Uint64: uint64(i) * 7919})
	}
	return nodes
}

// testPattern returns n deterministic bytes seeded by seed.
func testPattern(seed byte, n int) []byte {
	out := make([]byte, n)
	state := uint64(seed)<<56 | 0x9E3779B97F4A7C15
	for i := range out {
		state = state*6364136223846793005 + 1442695040888963407
		out[i] = byte(state >> 33)
	}
	return out
}

// patternReader is a reproducible, allocation-free byte stream for the
// streaming and memory-bound tests.
type patternReader struct {
	state     uint64
	remaining int64
}

func newPatternReader(seed byte, n int64) *patternReader {
	return &patternReader{state: uint64(seed)<<56 | 0xD1B54A32D192ED03, remaining: n}
}

func (p *patternReader) Read(b []byte) (int, error) {
	if p.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(b))
	if n > p.remaining {
		n = p.remaining
	}
	for i := int64(0); i < n; i++ {
		p.state = p.state*6364136223846793005 + 1442695040888963407
		b[i] = byte(p.state >> 33)
	}
	p.remaining -= n
	return int(n), nil
}

// buildSampleFragments builds the sample tree and returns its fragments.
func buildSampleFragments(t *testing.T) [][]byte {
	t.Helper()
	return buildTreeFragments(t, sampleTree())
}

// buildTreeFragments builds a tree into fragment bytes.
func buildTreeFragments(t *testing.T, nodes []TreeNode) [][]byte {
	t.Helper()
	var fragments [][]byte
	result, err := BuildRecord(testEventID(7), testNetwork(), proto.FamilyTx,
		proto.KindApplied, 100, nodes, func(index uint64, fragment []byte) error {
			if index != uint64(len(fragments)) {
				t.Fatalf("fragment index %d at position %d", index, len(fragments))
			}
			fragments = append(fragments, append([]byte(nil), fragment...))
			return nil
		})
	if err != nil {
		t.Fatalf("BuildRecord: %v", err)
	}
	if int(result.FragmentCount) != len(fragments) {
		t.Fatalf("fragment count %d, emitted %d", result.FragmentCount, len(fragments))
	}
	return fragments
}

// reseal recomputes the seal of a mutated fragment list so structural
// checks after the seal digest can be tested in isolation; it mirrors what
// a consistent forger would produce.
func reseal(t *testing.T, fragments [][]byte) [][]byte {
	t.Helper()
	if len(fragments) < MinimumFragmentCount {
		t.Fatalf("reseal: %d fragments", len(fragments))
	}
	contentBytes := uint64(0)
	digest := newRecordDigest()
	for i := 0; i < len(fragments)-1; i++ {
		digest.fragment(uint64(i), fragments[i])
		contentBytes += uint64(len(fragments[i]))
	}
	seal := &RecordSeal{FragmentCount: uint64(len(fragments)), ContentBytes: contentBytes,
		RecordDigest: digest.sum()}
	last, err := DecodeRecordFragment(fragments[len(fragments)-1])
	if err != nil {
		t.Fatalf("reseal decode: %v", err)
	}
	if last.Payload != PayloadSeal {
		t.Fatalf("reseal: last fragment is %s", last.Payload)
	}
	encoded, err := (&RecordFragment{RecordFormatVersion: proto.RecordFormatVersion,
		EventID: last.EventID, FragmentIndex: last.FragmentIndex,
		Payload: PayloadSeal, Seal: seal}).Encode()
	if err != nil {
		t.Fatalf("reseal encode: %v", err)
	}
	fragments[len(fragments)-1] = encoded
	return fragments
}

// resealWithHeader also refreshes the header's fragment count after the
// fragment count changed.
func resealWithHeader(t *testing.T, fragments [][]byte) [][]byte {
	t.Helper()
	header, err := DecodeRecordFragment(fragments[0])
	if err != nil || header.Payload != PayloadHeader {
		t.Fatalf("decode header: %v (%s)", err, header.Payload)
	}
	header.Header.FragmentCount = uint64(len(fragments))
	encoded, err := header.Encode()
	if err != nil {
		t.Fatalf("encode header: %v", err)
	}
	fragments[0] = encoded
	return reseal(t, fragments)
}

// decodeAll decodes every fragment for inspection.
func decodeAll(t *testing.T, fragments [][]byte) []*RecordFragment {
	t.Helper()
	out := make([]*RecordFragment, len(fragments))
	for i, raw := range fragments {
		decoded, err := DecodeRecordFragment(raw)
		if err != nil {
			t.Fatalf("decode fragment %d: %v", i, err)
		}
		out[i] = decoded
	}
	return out
}

// renumberFragment re-encodes one fragment under a different index.
func renumberFragment(t *testing.T, raw []byte, index uint64) []byte {
	t.Helper()
	decoded, err := DecodeRecordFragment(raw)
	if err != nil {
		t.Fatalf("renumber decode: %v", err)
	}
	decoded.FragmentIndex = index
	out, err := decoded.Encode()
	if err != nil {
		t.Fatalf("renumber encode: %v", err)
	}
	return out
}

// blobDigestOf hashes a value like the blob stream does.
func blobDigestOf(b []byte) proto.Hash32 {
	return proto.Hash32(sha256.Sum256(b))
}

// assertSentinel fails unless err wraps want.
func assertSentinel(t *testing.T, want, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %v, got nil", want)
	}
	if !errors.Is(err, want) {
		t.Fatalf("expected error %v, got %v", want, err)
	}
}

// chunksEqual compares two chunk slices by content.
func chunksEqual(a, b []BlobChunk) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].BlobID != b[i].BlobID || a[i].Offset != b[i].Offset ||
			!bytes.Equal(a[i].Data, b[i].Data) {
			return false
		}
	}
	return true
}
