// The TreeCodec write side: it turns one schema-validated node tree in
// depth-first preorder into the canonical fragment stream of D73 — exactly
// one header fragment, TypedItems fragments holding the maximal non-empty
// prefix of the node stream that fits the 65536-byte bound, BlobChunks
// fragments holding the blob content stream packed the same way, and exactly
// one seal fragment. The stream is emitted through a sink piece by piece; the
// codec buffers at most one fragment, never a second full copy of the record
// (MESSAGE_CONTRACT section 6.1, producer rules).
package codec

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// recordDigestDomain is the digest domain of the record digest; the domain
// bytes are followed by one zero byte in the preimage.
const recordDigestDomain = "extractor.record.v1"

// FragmentSink receives the canonical fragment bytes of one record in index
// order. The slice is valid until the sink returns; a sink that keeps a
// fragment must copy it.
type FragmentSink func(index uint64, fragment []byte) error

// TreeBuildResult summarizes one built record: the totals the header
// carries, the record length and the record digest of the content
// fragments. It matches what proto.BuildEventDescriptor computes over the
// collected fragments.
type TreeBuildResult struct {
	FragmentCount  uint64
	RecordLength   uint64
	ContentBytes   uint64
	RecordDigest   proto.Hash32
	NodeCount      uint64
	BlobCount      uint64
	TotalBlobBytes uint64
}

// BuildRecord encodes one logical record into its fragment stream. family
// and kind are the registry v1 values of the event; rootType is the registry
// type key of the root message node; nodes hold the tree in depth-first
// preorder. Every fragment is passed to sink in index order; nothing is
// emitted when validation fails. The result digest equals
// proto.RecordDigest over the emitted fragments, the seal excluded.
func BuildRecord(eventID proto.EventID, network proto.NetworkIdentity,
	family proto.Family, kind proto.Kind, rootType uint32,
	nodes []TreeNode, sink FragmentSink) (TreeBuildResult, error) {
	return BuildRecordWithLimits(eventID, network, family, kind, rootType,
		nodes, DefaultTreeLimits(), sink)
}

// BuildRecordWithLimits is BuildRecord with explicit tree limits.
func BuildRecordWithLimits(eventID proto.EventID, network proto.NetworkIdentity,
	family proto.Family, kind proto.Kind, rootType uint32,
	nodes []TreeNode, limits TreeLimits, sink FragmentSink) (TreeBuildResult, error) {
	if err := eventID.Validate(); err != nil {
		return TreeBuildResult{}, fmt.Errorf("%w: %w", ErrInvalidValue, err)
	}
	if err := network.Validate(); err != nil {
		return TreeBuildResult{}, fmt.Errorf("%w: %w", ErrClosedRegistry, err)
	}
	if !family.Valid() {
		return TreeBuildResult{}, fmt.Errorf("%w: family %d is not in registry v1",
			ErrClosedRegistry, uint16(family))
	}
	if !kind.Valid() {
		return TreeBuildResult{}, fmt.Errorf("%w: kind %d is not in registry v1",
			ErrClosedRegistry, uint16(kind))
	}
	if rootType == 0 {
		return TreeBuildResult{}, fmt.Errorf("%w: root type key 0, type keys are positive", ErrInvalidValue)
	}
	if err := limits.Validate(); err != nil {
		return TreeBuildResult{}, err
	}
	if sink == nil {
		return TreeBuildResult{}, fmt.Errorf("%w: fragment sink is nil", ErrInvalidValue)
	}
	if len(nodes) == 0 {
		return TreeBuildResult{}, fmt.Errorf("%w: record tree has no nodes", ErrTree)
	}

	// Pass one validates the tree and walks out the blob refs. Nothing is
	// emitted before this pass succeeds.
	var blobCount, totalBlobBytes uint64
	blobDigests := make(map[uint64]proto.Hash32)
	walker := &preorderWalker{limits: limits}
	for i := range nodes {
		node := &nodes[i]
		check, err := treeNodeCheck(uint64(i), node)
		if err != nil {
			return TreeBuildResult{}, err
		}
		if _, err := walker.next(check); err != nil {
			return TreeBuildResult{}, err
		}
		if i == 0 && node.TypeKey != rootType {
			return TreeBuildResult{}, fmt.Errorf("%w: root node type key %d differs from the header root type %d",
				ErrTree, node.TypeKey, rootType)
		}
		if node.Kind == ValueBytes || node.Kind == ValueUTF8 {
			if node.Kind == ValueUTF8 {
				var scanner utf8Scanner
				if err := scanner.write(node.Value); err != nil {
					return TreeBuildResult{}, fmt.Errorf("%w: node %d: %w", ErrUTF8, i, err)
				}
				if err := scanner.end(); err != nil {
					return TreeBuildResult{}, fmt.Errorf("%w: node %d: %w", ErrUTF8, i, err)
				}
			}
			blobDigests[uint64(i)] = sha256Of(node.Value)
			blobCount, err = addCount(blobCount, 1)
			if err != nil {
				return TreeBuildResult{}, err
			}
			totalBlobBytes, err = addCount(totalBlobBytes, uint64(len(node.Value)))
			if err != nil {
				return TreeBuildResult{}, err
			}
		}
	}
	if err := walker.done(); err != nil {
		return TreeBuildResult{}, err
	}

	// Pass two counts the fragments without emitting: the header must open
	// the record with the final fragment count before any items fragment
	// exists, so the packing is simulated once (deterministic) and then
	// replayed for emission.
	itemsFragments, err := countItemFragments(nodes, limits)
	if err != nil {
		return TreeBuildResult{}, err
	}
	blobsFragments, err := countBlobFragments(nodes)
	if err != nil {
		return TreeBuildResult{}, err
	}
	fragmentCount := uint64(2) + itemsFragments + blobsFragments

	header := &RecordHeader{SchemaVersion: SchemaVersion, Network: network,
		EventFamily: uint32(family), EventKind: uint32(kind), RootType: rootType,
		NodeCount: uint64(len(nodes)), BlobCount: blobCount, TotalBlobBytes: totalBlobBytes,
		FragmentCount: fragmentCount}

	digest := newRecordDigest()
	var contentBytes, recordLength uint64
	emit := func(index uint64, fragment []byte) error {
		recordLength, err = addCount(recordLength, uint64(len(fragment)))
		if err != nil {
			return fmt.Errorf("%w: record length: %w", ErrOverflow, err)
		}
		if index != fragmentCount-1 {
			digest.fragment(index, fragment)
			contentBytes, err = addCount(contentBytes, uint64(len(fragment)))
			if err != nil {
				return fmt.Errorf("%w: content bytes: %w", ErrOverflow, err)
			}
		}
		return sink(index, fragment)
	}

	if size := fragmentEncodedSize(0, recordHeaderSize(header)); size > proto.FragmentMaxBytes {
		return TreeBuildResult{}, fmt.Errorf("%w: header fragment serializes to %d bytes",
			ErrInvalidValue, size)
	}
	headerFragment := appendFragment(nil, eventID, 0, fieldHeaderTag, appendRecordHeader(nil, header))
	if err := emit(0, headerFragment); err != nil {
		return TreeBuildResult{}, err
	}

	// Pass three replays the packing and emits the content fragments.
	nextIndex := uint64(1)
	nextIndex, err = emitItems(eventID, nodes, limits, blobDigests, nextIndex, emit)
	if err != nil {
		return TreeBuildResult{}, err
	}
	nextIndex, err = emitBlobs(eventID, nodes, nextIndex, emit)
	if err != nil {
		return TreeBuildResult{}, err
	}
	if nextIndex != fragmentCount-1 {
		return TreeBuildResult{}, fmt.Errorf("%w: content fragments stopped at index %d, want %d",
			ErrFragments, nextIndex, fragmentCount-1)
	}

	seal := &RecordSeal{FragmentCount: fragmentCount, ContentBytes: contentBytes,
		RecordDigest: digest.sum()}
	if size := fragmentEncodedSize(fragmentCount-1, recordSealSize(seal)); size > proto.FragmentMaxBytes {
		return TreeBuildResult{}, fmt.Errorf("%w: seal fragment serializes to %d bytes",
			ErrInvalidValue, size)
	}
	sealFragment := appendFragment(nil, eventID, fragmentCount-1, fieldSealTag, appendRecordSeal(nil, seal))
	if err := emit(fragmentCount-1, sealFragment); err != nil {
		return TreeBuildResult{}, err
	}
	return TreeBuildResult{FragmentCount: fragmentCount, RecordLength: recordLength,
		ContentBytes: contentBytes, RecordDigest: digest.sum(),
		NodeCount: uint64(len(nodes)), BlobCount: blobCount, TotalBlobBytes: totalBlobBytes}, nil
}

// treeNodeCheck projects one TreeNode into the walker's view and rejects
// model-level violations: a leaf with a child count, a container with a
// value-shaped kind, or placement markers on a root-position node the walker
// itself catches.
func treeNodeCheck(nodeID uint64, node *TreeNode) (*nodeCheck, error) {
	if node.Kind == ValueNone {
		return nil, fmt.Errorf("%w: node %d has no value kind", ErrTree, nodeID)
	}
	isContainer := node.Kind == ValueMessage || node.Kind == ValueList
	if !isContainer && node.ChildCount != 0 {
		return nil, fmt.Errorf("%w: leaf node %d declares %d children",
			ErrTree, nodeID, node.ChildCount)
	}
	if node.HasFieldTag && node.IsElement {
		return nil, fmt.Errorf("%w: node %d carries both a field tag and an element index",
			ErrTree, nodeID)
	}
	return &nodeCheck{nodeID: nodeID,
		hasFieldTag:  node.HasFieldTag,
		fieldTag:     node.FieldTag,
		isElement:    node.IsElement,
		elementIndex: node.ElementIndex,
		isContainer:  isContainer,
		isMessage:    node.Kind == ValueMessage,
		childCount:   node.ChildCount}, nil
}

// buildItem assembles the TypedItem of one node. blobDigests carries the
// pass-one content digests of the bytes and UTF-8 nodes.
func buildItem(nodeID, parent uint64, node *TreeNode, blobDigests map[uint64]proto.Hash32) *TypedItem {
	it := &TypedItem{NodeID: nodeID, Kind: node.Kind}
	if nodeID != 0 {
		it.HasParentID = true
		it.ParentID = parent
	}
	if node.HasFieldTag {
		it.HasFieldTag = true
		it.FieldTag = node.FieldTag
	}
	if node.IsElement {
		it.HasElementIndex = true
		it.ElementIndex = node.ElementIndex
	}
	switch node.Kind {
	case ValueMessage:
		it.Message = MessageValue{TypeKey: node.TypeKey, DirectChildCount: node.ChildCount}
	case ValueList:
		it.List = ListValue{Count: node.ChildCount}
	case ValueBool:
		it.BoolValue = node.Bool
	case ValueUint32:
		it.Uint32Value = node.Uint32
	case ValueUint64:
		it.Uint64Value = node.Uint64
	case ValueEnum:
		it.EnumValue = node.Enum
	case ValueBytes, ValueUTF8:
		ref := BlobRef{BlobID: nodeID, Length: uint64(len(node.Value)), SHA256: blobDigests[nodeID]}
		if node.Kind == ValueBytes {
			it.BytesValue = ref
		} else {
			it.UTF8Value = ref
		}
	}
	return it
}

// countItemFragments simulates the item packing and returns the fragment
// count. The simulation shares the exact size arithmetic with the emitting
// pass, so both produce identical boundaries.
func countItemFragments(nodes []TreeNode, limits TreeLimits) (uint64, error) {
	walker := &preorderWalker{limits: limits}
	innerLen := uint64(0)
	fragments := uint64(0)
	for i := range nodes {
		check, err := treeNodeCheck(uint64(i), &nodes[i])
		if err != nil {
			return 0, err
		}
		parent, err := walker.next(check)
		if err != nil {
			return 0, err
		}
		// Node 0's parent is fictitious here; its item carries no parent.
		if i == 0 {
			parent = 0
		}
		wire := itemWireSize(buildItem(uint64(i), parent, &nodes[i], nil))
		if innerLen == 0 {
			if err := singlePieceFits(fragments+1, wire); err != nil {
				return 0, err
			}
			innerLen = wire
			continue
		}
		if fragmentEncodedSize(fragments+1, innerLen+wire) <= proto.FragmentMaxBytes {
			innerLen += wire
			continue
		}
		fragments++
		if err := singlePieceFits(fragments+1, wire); err != nil {
			return 0, err
		}
		innerLen = wire
	}
	if err := walker.done(); err != nil {
		return 0, err
	}
	if innerLen > 0 {
		fragments++
	}
	return fragments, nil
}

// countBlobFragments simulates the blob chunk packing.
func countBlobFragments(nodes []TreeNode) (uint64, error) {
	innerLen := uint64(0)
	fragments := uint64(0)
	for i := range nodes {
		node := &nodes[i]
		if node.Kind != ValueBytes && node.Kind != ValueUTF8 {
			continue
		}
		for offset := uint64(0); offset < uint64(len(node.Value)); {
			size := uint64(BlobChunkMaxData)
			if remaining := uint64(len(node.Value)) - offset; remaining < size {
				size = remaining
			}
			chunk := BlobChunk{BlobID: uint64(i), Offset: offset, Data: node.Value[offset : offset+size]}
			wire := chunkWireSize(&chunk)
			if innerLen == 0 {
				if err := singlePieceFits(fragments+1, wire); err != nil {
					return 0, err
				}
				innerLen = wire
			} else if fragmentEncodedSize(fragments+1, innerLen+wire) <= proto.FragmentMaxBytes {
				innerLen += wire
			} else {
				fragments++
				if err := singlePieceFits(fragments+1, wire); err != nil {
					return 0, err
				}
				innerLen = wire
			}
			offset += size
		}
	}
	if innerLen > 0 {
		fragments++
	}
	return fragments, nil
}

// singlePieceFits rejects a single item or chunk that cannot fill one
// fragment alone (D73: one TypedItem must fit; chunk data is bounded by
// 16384 so a chunk always fits).
func singlePieceFits(fragmentIndex, wireLen uint64) error {
	if fragmentEncodedSize(fragmentIndex, wireLen) > proto.FragmentMaxBytes {
		return fmt.Errorf("%w: single item of %d wire bytes does not fit the %d fragment bound",
			ErrInvalidValue, wireLen, proto.FragmentMaxBytes)
	}
	return nil
}

// emitItems packs the node items into maximal fragments and emits them
// starting at nextIndex; it returns the next free fragment index.
func emitItems(eventID proto.EventID, nodes []TreeNode, limits TreeLimits,
	blobDigests map[uint64]proto.Hash32, nextIndex uint64,
	emit func(index uint64, fragment []byte) error) (uint64, error) {
	walker := &preorderWalker{limits: limits}
	var inner []byte
	innerLen := uint64(0)
	flush := func() error {
		if innerLen == 0 {
			return nil
		}
		fragment := appendFragment(nil, eventID, nextIndex, fieldItemsTag, inner)
		if len(fragment) > proto.FragmentMaxBytes {
			return fmt.Errorf("%w: items fragment %d serializes to %d bytes",
				ErrInvalidValue, nextIndex, len(fragment))
		}
		if err := emit(nextIndex, fragment); err != nil {
			return err
		}
		nextIndex++
		inner = inner[:0]
		innerLen = 0
		return nil
	}
	for i := range nodes {
		check, err := treeNodeCheck(uint64(i), &nodes[i])
		if err != nil {
			return 0, err
		}
		parent, err := walker.next(check)
		if err != nil {
			return 0, err
		}
		if i == 0 {
			parent = 0
		}
		item := buildItem(uint64(i), parent, &nodes[i], blobDigests)
		wire := itemWireSize(item)
		if innerLen > 0 && fragmentEncodedSize(nextIndex, innerLen+wire) > proto.FragmentMaxBytes {
			if err := flush(); err != nil {
				return 0, err
			}
		}
		if innerLen == 0 {
			if err := singlePieceFits(nextIndex, wire); err != nil {
				return 0, err
			}
		}
		inner = appendBytesField(inner, 1, appendTypedItem(nil, item))
		innerLen += wire
	}
	if err := walker.done(); err != nil {
		return 0, err
	}
	if err := flush(); err != nil {
		return 0, err
	}
	return nextIndex, nil
}

// emitBlobs packs the blob chunk stream into maximal fragments and emits
// them; empty blobs contribute references only, never chunks.
func emitBlobs(eventID proto.EventID, nodes []TreeNode, nextIndex uint64,
	emit func(index uint64, fragment []byte) error) (uint64, error) {
	var inner []byte
	innerLen := uint64(0)
	flush := func() error {
		if innerLen == 0 {
			return nil
		}
		fragment := appendFragment(nil, eventID, nextIndex, fieldBlobsTag, inner)
		if len(fragment) > proto.FragmentMaxBytes {
			return fmt.Errorf("%w: blobs fragment %d serializes to %d bytes",
				ErrInvalidValue, nextIndex, len(fragment))
		}
		if err := emit(nextIndex, fragment); err != nil {
			return err
		}
		nextIndex++
		inner = inner[:0]
		innerLen = 0
		return nil
	}
	for i := range nodes {
		node := &nodes[i]
		if node.Kind != ValueBytes && node.Kind != ValueUTF8 {
			continue
		}
		for offset := uint64(0); offset < uint64(len(node.Value)); {
			size := uint64(BlobChunkMaxData)
			if remaining := uint64(len(node.Value)) - offset; remaining < size {
				size = remaining
			}
			chunk := BlobChunk{BlobID: uint64(i), Offset: offset, Data: node.Value[offset : offset+size]}
			wire := chunkWireSize(&chunk)
			if innerLen > 0 && fragmentEncodedSize(nextIndex, innerLen+wire) > proto.FragmentMaxBytes {
				if err := flush(); err != nil {
					return 0, err
				}
			}
			if innerLen == 0 {
				if err := singlePieceFits(nextIndex, wire); err != nil {
					return 0, err
				}
			}
			inner = appendBytesField(inner, 1, appendBlobChunk(nil, &chunk))
			innerLen += wire
			offset += size
		}
	}
	if err := flush(); err != nil {
		return 0, err
	}
	return nextIndex, nil
}

// recordDigestStream incrementally computes the record digest of D73:
//
//	SHA256(UTF8("extractor.record.v1") || 0x00 ||
//	    concat(u64be(index) || u64be(length(B_i)) || B_i))
//
// over the content fragments, the seal excluded. It matches
// proto.RecordDigest over the same fragments.
type recordDigestStream struct {
	h hash.Hash
}

func newRecordDigest() *recordDigestStream {
	d := &recordDigestStream{h: sha256.New()}
	d.h.Write([]byte(recordDigestDomain))
	d.h.Write([]byte{0})
	return d
}

func (d *recordDigestStream) fragment(index uint64, fragment []byte) {
	var prefix [16]byte
	binary.BigEndian.PutUint64(prefix[0:8], index)
	binary.BigEndian.PutUint64(prefix[8:16], uint64(len(fragment)))
	d.h.Write(prefix[:])
	d.h.Write(fragment)
}

func (d *recordDigestStream) sum() proto.Hash32 {
	var out proto.Hash32
	copy(out[:], d.h.Sum(nil))
	return out
}

// addCount adds one to a checked counter.
func addCount(a, b uint64) (uint64, error) {
	sum := a + b
	if sum < a {
		return 0, fmt.Errorf("%w: %d + %d", ErrOverflow, a, b)
	}
	return sum, nil
}
