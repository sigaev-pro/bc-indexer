// The TreeCodec read side: it verifies one complete fragment sequence
// against every D73 invariant and rebuilds the node tree with reassembled
// values. The checks mirror the producer rules: fragment order and counts,
// tree preorder structure, blob chunk continuity with independent per-blob
// digests, incremental UTF-8 validation without normalization, header totals
// and the seal digest over the content fragments. A record is valid only
// after all fragments and invariants check out; receiving the seal alone
// proves nothing (MESSAGE_CONTRACT section 6.1, validation rules).
package codec

import (
	"crypto/sha256"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// ReadNode is one decoded tree node with its value reassembled from the blob
// chunk stream. The fields mirror TreeNode; parents come from the encoded
// items.
type ReadNode struct {
	HasParent    bool
	ParentID     uint64
	HasFieldTag  bool
	FieldTag     uint32
	IsElement    bool
	ElementIndex uint64
	Kind         ValueKind
	TypeKey      uint32
	ChildCount   uint64
	Bool         bool
	Uint32       uint32
	Uint64       uint64
	Enum         uint32
	// Value holds the reassembled content of a bytes or UTF-8 node,
	// including an empty byte for an empty blob.
	Value []byte
}

// ReadRecord verifies a full fragment sequence and returns the record header
// and the node tree in depth-first preorder with reassembled values. The
// values together measure the blob total of the record; callers that stream
// should consume fragments incrementally at the transport layer instead.
// ReadRecord accepts the default tree limits; ReadRecordWithLimits accepts
// the limits a producer used.
func ReadRecord(fragments [][]byte) (*RecordHeader, []ReadNode, error) {
	return ReadRecordWithLimits(fragments, DefaultTreeLimits())
}

// ReadRecordWithLimits is ReadRecord with explicit tree limits.
func ReadRecordWithLimits(fragments [][]byte, limits TreeLimits) (*RecordHeader, []ReadNode, error) {
	if err := limits.Validate(); err != nil {
		return nil, nil, err
	}
	if len(fragments) < MinimumFragmentCount {
		return nil, nil, fmt.Errorf("%w: %d fragments below the header/items/seal minimum %d",
			ErrFragments, len(fragments), MinimumFragmentCount)
	}
	var items []TypedItem
	var chunks []BlobChunk
	type packedFragment struct {
		index     uint64
		innerLen  uint64
		firstWire uint64
	}
	var itemsFrags, blobsFrags []packedFragment
	var header *RecordHeader
	var seal *RecordSeal
	var eventID proto.EventID

	for position, raw := range fragments {
		decoded, err := DecodeRecordFragment(raw)
		if err != nil {
			return nil, nil, err
		}
		if position == 0 {
			eventID = decoded.EventID
		} else if decoded.EventID != eventID {
			return nil, nil, fmt.Errorf("%w: fragment %d names a different event id",
				ErrIdentityMismatch, position)
		}
		if decoded.FragmentIndex != uint64(position) {
			return nil, nil, fmt.Errorf("%w: fragment at position %d carries index %d",
				ErrFragments, position, decoded.FragmentIndex)
		}
		switch decoded.Payload {
		case PayloadHeader:
			if position != 0 {
				return nil, nil, fmt.Errorf("%w: header fragment at position %d", ErrFragments, position)
			}
			header = decoded.Header
		case PayloadSeal:
			if position != len(fragments)-1 {
				return nil, nil, fmt.Errorf("%w: seal fragment at position %d of %d",
					ErrFragments, position, len(fragments))
			}
			seal = decoded.Seal
		case PayloadItems:
			if position == 0 || position == len(fragments)-1 {
				return nil, nil, fmt.Errorf("%w: items fragment at position %d", ErrFragments, position)
			}
			if len(blobsFrags) > 0 {
				return nil, nil, fmt.Errorf("%w: items fragment at position %d follows blobs fragments",
					ErrFragments, position)
			}
			pf := packedFragment{index: uint64(position)}
			for i := range decoded.Items {
				pf.innerLen += itemWireSize(&decoded.Items[i])
			}
			pf.firstWire = itemWireSize(&decoded.Items[0])
			items = append(items, decoded.Items...)
			itemsFrags = append(itemsFrags, pf)
		case PayloadBlobs:
			if position == 0 || position == len(fragments)-1 {
				return nil, nil, fmt.Errorf("%w: blobs fragment at position %d", ErrFragments, position)
			}
			pf := packedFragment{index: uint64(position)}
			for i := range decoded.Blobs {
				pf.innerLen += chunkWireSize(&decoded.Blobs[i])
			}
			pf.firstWire = chunkWireSize(&decoded.Blobs[0])
			chunks = append(chunks, decoded.Blobs...)
			blobsFrags = append(blobsFrags, pf)
		}
	}
	if header == nil || seal == nil {
		return nil, nil, fmt.Errorf("%w: record lacks a header or seal fragment", ErrFragments)
	}
	if len(itemsFrags) == 0 {
		return nil, nil, fmt.Errorf("%w: record lacks an items fragment", ErrFragments)
	}

	// Counts and totals: header and seal carry the same fragment count K,
	// and the seal content total matches the content fragment bytes.
	if seal.FragmentCount != uint64(len(fragments)) {
		return nil, nil, fmt.Errorf("%w: seal fragment count %d, want %d",
			ErrFragments, seal.FragmentCount, len(fragments))
	}
	if header.FragmentCount != uint64(len(fragments)) {
		return nil, nil, fmt.Errorf("%w: header fragment count %d, want %d",
			ErrFragments, header.FragmentCount, len(fragments))
	}
	digest := newRecordDigest()
	var contentBytes uint64
	for i := 0; i < len(fragments)-1; i++ {
		digest.fragment(uint64(i), fragments[i])
		var err error
		contentBytes, err = addCount(contentBytes, uint64(len(fragments[i])))
		if err != nil {
			return nil, nil, err
		}
	}
	if seal.ContentBytes != contentBytes {
		return nil, nil, fmt.Errorf("%w: seal content bytes %d, want %d",
			ErrFragments, seal.ContentBytes, contentBytes)
	}
	if seal.RecordDigest != digest.sum() {
		return nil, nil, fmt.Errorf("%w: seal record digest differs from the digest over the %d content fragments",
			ErrDigestMismatch, len(fragments)-1)
	}

	// Packing maximality: every items or blobs fragment except the last of
	// its stream holds the largest prefix that fits the fragment bound.
	for i := 0; i+1 < len(itemsFrags); i++ {
		a, b := itemsFrags[i], itemsFrags[i+1]
		if fragmentEncodedSize(a.index, a.innerLen+b.firstWire) <= proto.FragmentMaxBytes {
			return nil, nil, fmt.Errorf("%w: items fragment %d is not packed maximally",
				ErrFragments, a.index)
		}
	}
	for i := 0; i+1 < len(blobsFrags); i++ {
		a, b := blobsFrags[i], blobsFrags[i+1]
		if fragmentEncodedSize(a.index, a.innerLen+b.firstWire) <= proto.FragmentMaxBytes {
			return nil, nil, fmt.Errorf("%w: blobs fragment %d is not packed maximally",
				ErrFragments, a.index)
		}
	}

	nodes, blobRefs, err := walkItems(items, header, limits)
	if err != nil {
		return nil, nil, err
	}
	if err := applyChunks(nodes, blobRefs, chunks); err != nil {
		return nil, nil, err
	}

	// Header totals.
	if header.NodeCount != uint64(len(items)) {
		return nil, nil, fmt.Errorf("%w: header node count %d, want %d",
			ErrFragments, header.NodeCount, len(items))
	}
	var blobCount, totalBlobBytes uint64
	for _, ref := range blobRefs {
		blobCount, err = addCount(blobCount, 1)
		if err != nil {
			return nil, nil, err
		}
		totalBlobBytes, err = addCount(totalBlobBytes, ref.Length)
		if err != nil {
			return nil, nil, err
		}
	}
	if header.BlobCount != blobCount {
		return nil, nil, fmt.Errorf("%w: header blob count %d, want %d",
			ErrFragments, header.BlobCount, blobCount)
	}
	if header.TotalBlobBytes != totalBlobBytes {
		return nil, nil, fmt.Errorf("%w: header total blob bytes %d, want %d",
			ErrFragments, header.TotalBlobBytes, totalBlobBytes)
	}
	return header, nodes, nil
}

// walkItems validates the typed tree structure of the decoded items and
// projects them into ReadNodes; it also returns the blob references of the
// bytes and UTF-8 nodes in node order.
func walkItems(items []TypedItem, header *RecordHeader, limits TreeLimits) ([]ReadNode, []BlobRef, error) {
	walker := &preorderWalker{limits: limits, requireDeclaredParent: true}
	nodes := make([]ReadNode, 0, len(items))
	var blobRefs []BlobRef
	for i := range items {
		it := &items[i]
		var childCount uint64
		isContainer := it.Kind == ValueMessage || it.Kind == ValueList
		if it.Kind == ValueMessage {
			childCount = it.Message.DirectChildCount
		} else if it.Kind == ValueList {
			childCount = it.List.Count
		}
		if _, err := walker.next(&nodeCheck{
			nodeID:       it.NodeID,
			hasParent:    it.HasParentID,
			parentID:     it.ParentID,
			hasFieldTag:  it.HasFieldTag,
			fieldTag:     it.FieldTag,
			isElement:    it.HasElementIndex,
			elementIndex: it.ElementIndex,
			isContainer:  isContainer,
			isMessage:    it.Kind == ValueMessage,
			childCount:   childCount,
		}); err != nil {
			return nil, nil, err
		}
		node := ReadNode{HasParent: it.HasParentID, ParentID: it.ParentID,
			HasFieldTag: it.HasFieldTag, FieldTag: it.FieldTag,
			IsElement: it.HasElementIndex, ElementIndex: it.ElementIndex,
			Kind: it.Kind, ChildCount: childCount,
			Bool: it.BoolValue, Uint32: it.Uint32Value, Uint64: it.Uint64Value,
			Enum: it.EnumValue}
		switch it.Kind {
		case ValueMessage:
			node.TypeKey = it.Message.TypeKey
			if i == 0 && it.Message.TypeKey != header.RootType {
				return nil, nil, fmt.Errorf("%w: root node type key %d differs from the header root type %d",
					ErrTree, it.Message.TypeKey, header.RootType)
			}
		case ValueBytes, ValueUTF8:
			ref := it.BytesValue
			if it.Kind == ValueUTF8 {
				ref = it.UTF8Value
			}
			if ref.BlobID != it.NodeID {
				return nil, nil, fmt.Errorf("%w: node %d references blob %d, want its own id",
					ErrTree, it.NodeID, ref.BlobID)
			}
			blobRefs = append(blobRefs, ref)
		}
		nodes = append(nodes, node)
	}
	if err := walker.done(); err != nil {
		return nil, nil, err
	}
	return nodes, blobRefs, nil
}

// applyChunks verifies the blob chunk stream against the blob references in
// node order and reassembles the values with independent per-blob digests
// and incremental UTF-8 validation.
func applyChunks(nodes []ReadNode, blobRefs []BlobRef, chunks []BlobChunk) error {
	if len(blobRefs) == 0 && len(chunks) > 0 {
		return fmt.Errorf("%w: %d chunks for a record without blobs", ErrBlobs, len(chunks))
	}
	chunkPos := 0
	for _, ref := range blobRefs {
		node := &nodes[ref.BlobID]
		value := make([]byte, 0, ref.Length)
		blobHash := sha256.New()
		var scanner utf8Scanner
		for offset := uint64(0); offset < ref.Length; {
			wantSize := uint64(BlobChunkMaxData)
			if remaining := ref.Length - offset; remaining < wantSize {
				wantSize = remaining
			}
			if chunkPos >= len(chunks) {
				return fmt.Errorf("%w: blob %d ends at offset %d of %d, chunks exhausted",
					ErrTruncated, ref.BlobID, offset, ref.Length)
			}
			chunk := &chunks[chunkPos]
			if chunk.BlobID != ref.BlobID {
				return fmt.Errorf("%w: chunk of blob %d arrives while blob %d is open at offset %d",
					ErrBlobs, chunk.BlobID, ref.BlobID, offset)
			}
			if chunk.Offset != offset {
				return fmt.Errorf("%w: blob %d chunk offset %d, want %d",
					ErrBlobs, ref.BlobID, chunk.Offset, offset)
			}
			if uint64(len(chunk.Data)) != wantSize {
				return fmt.Errorf("%w: blob %d chunk at %d carries %d bytes, want min(%d, remaining %d)=%d",
					ErrBlobs, ref.BlobID, chunk.Offset, len(chunk.Data), BlobChunkMaxData,
					ref.Length-offset, wantSize)
			}
			value = append(value, chunk.Data...)
			blobHash.Write(chunk.Data)
			if node.Kind == ValueUTF8 {
				if err := scanner.write(chunk.Data); err != nil {
					return fmt.Errorf("%w: blob %d: %w", ErrUTF8, ref.BlobID, err)
				}
			}
			offset += wantSize
			chunkPos++
		}
		if node.Kind == ValueUTF8 {
			if err := scanner.end(); err != nil {
				return fmt.Errorf("%w: blob %d: %w", ErrUTF8, ref.BlobID, err)
			}
		}
		var digest proto.Hash32
		copy(digest[:], blobHash.Sum(nil))
		if digest != ref.SHA256 {
			return fmt.Errorf("%w: blob %d of %d bytes differs from its BlobRef digest",
				ErrDigestMismatch, ref.BlobID, ref.Length)
		}
		if ref.Length == 0 && len(value) != 0 {
			return fmt.Errorf("%w: empty blob %d reassembled %d bytes", ErrBlobs, ref.BlobID, len(value))
		}
		node.Value = value
	}
	if chunkPos != len(chunks) {
		return fmt.Errorf("%w: %d chunks after the last blob", ErrBlobs, len(chunks)-chunkPos)
	}
	return nil
}
