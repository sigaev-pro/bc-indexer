// The typed RecordFragment format of D73 (MESSAGE_CONTRACT section 6.1): one
// bounded Protobuf message of up to 65536 bytes carrying exactly one of four
// payloads — a RecordHeader, TypedItems (schema-validated tree nodes),
// BlobChunks (content bytes) or a RecordSeal. The pinned field tables and the
// canonical encoding rules (ascending tags, minimal unsigned varints,
// length-delimited fields, no unknown or duplicate singular fields, repeated
// fields in the given order) are fixed by the contract; this file implements
// them with exact encoded-size arithmetic so packing can plan fragments
// without building them first.
package codec

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// Version and bound pins of the D73 typed fragment format.
const (
	// SchemaVersion is the logical schema version of the record tree
	// (D73: logical schema_version=2).
	SchemaVersion uint32 = 2

	// MinimumFragmentCount is the least fragment count of a valid record:
	// exactly one header, at least one TypedItems fragment and exactly one
	// seal.
	MinimumFragmentCount = 3

	// BlobChunkMaxData bounds the data field of one BlobChunk
	// (D73 table: 1..16384 bytes).
	BlobChunkMaxData = 16384
)

// RecordFragment payload field numbers of the pinned table (the oneof).
const (
	fieldHeaderTag = 4
	fieldItemsTag  = 5
	fieldBlobsTag  = 6
	fieldSealTag   = 7
)

// FragmentPayload identifies which oneof payload one RecordFragment carries.
type FragmentPayload uint8

// The four payload alternatives of a RecordFragment.
const (
	PayloadNone FragmentPayload = iota
	PayloadHeader
	PayloadItems
	PayloadBlobs
	PayloadSeal
)

// String returns the payload name of p.
func (p FragmentPayload) String() string {
	switch p {
	case PayloadNone:
		return "none"
	case PayloadHeader:
		return "header"
	case PayloadItems:
		return "items"
	case PayloadBlobs:
		return "blobs"
	case PayloadSeal:
		return "seal"
	}
	return fmt.Sprintf("FragmentPayload(%d)", uint8(p))
}

// familyFromU32 maps a header event_family value onto the closed registry of
// protocol/proto. Unknown values are errors; there is no OTHER.
func familyFromU32(v uint32) (proto.Family, error) {
	f := proto.Family(v)
	if !f.Valid() {
		return 0, fmt.Errorf("%w: event family %d is not in registry v1", ErrClosedRegistry, v)
	}
	return f, nil
}

// kindFromU32 maps a header event_kind value onto the closed registry.
func kindFromU32(v uint32) (proto.Kind, error) {
	k := proto.Kind(v)
	if !k.Valid() {
		return 0, fmt.Errorf("%w: event kind %d is not in registry v1", ErrClosedRegistry, v)
	}
	return k, nil
}

// appendNetwork appends the pinned NetworkIdentity table (family enum,
// chain id, genesis hash), the same three fields protocol/proto wraps into
// its TransportFrame.
func appendNetwork(b []byte, n proto.NetworkIdentity) []byte {
	b = appendVarintField(b, 1, uint64(n.Family))
	b = appendBytesField(b, 2, n.ChainID[:])
	return appendBytesField(b, 3, n.GenesisHash[:])
}

// decodeNetwork parses the pinned NetworkIdentity table.
func decodeNetwork(b []byte) (proto.NetworkIdentity, error) {
	r := &wireReader{b: b}
	var net proto.NetworkIdentity
	seen, err := walkFields("NetworkIdentity", r, 1, 3, nil, func(field uint32, wire byte, r *wireReader) error {
		switch field {
		case 1:
			if wire != wireVarint {
				return fmt.Errorf("%w: NetworkIdentity family wire type %d", ErrMalformed, wire)
			}
			v, err := r.readVarint()
			if err != nil {
				return err
			}
			if proto.NetworkFamily(v) != proto.NetworkFamilyEVM {
				return fmt.Errorf("%w: network family %d is not EVM=1", ErrClosedRegistry, v)
			}
			net.Family = proto.NetworkFamily(v)
			return nil
		default:
			buf, err := r.readFixedBytes(wire, field, 32, "network identity bytes")
			if err != nil {
				return err
			}
			if field == 2 {
				copy(net.ChainID[:], buf)
			} else {
				copy(net.GenesisHash[:], buf)
			}
			return nil
		}
	})
	if err != nil {
		return proto.NetworkIdentity{}, err
	}
	for _, f := range []uint32{1, 2, 3} {
		if err := seen.require(f, "NetworkIdentity"); err != nil {
			return proto.NetworkIdentity{}, err
		}
	}
	return net, nil
}

// RecordHeader is the first fragment of a record: the logical schema
// versions, the network identity, the event registry values, the root type
// key and the totals the seal and the readers verify against.
type RecordHeader struct {
	SchemaVersion  uint32
	Network        proto.NetworkIdentity
	EventFamily    uint32
	EventKind      uint32
	RootType       uint32
	NodeCount      uint64
	BlobCount      uint64
	TotalBlobBytes uint64
	FragmentCount  uint64
}

// Validate rejects a header with wrong versions, registry values or
// impossible totals.
func (h *RecordHeader) Validate() error {
	if h.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: schema version %d, want %d", ErrVersionUnsupported, h.SchemaVersion, SchemaVersion)
	}
	if err := h.Network.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrClosedRegistry, err)
	}
	if _, err := familyFromU32(h.EventFamily); err != nil {
		return err
	}
	if _, err := kindFromU32(h.EventKind); err != nil {
		return err
	}
	if h.RootType == 0 {
		return fmt.Errorf("%w: root type key 0, type keys are positive", ErrInvalidValue)
	}
	if h.NodeCount == 0 {
		return fmt.Errorf("%w: node count 0, the root node always exists", ErrInvalidValue)
	}
	if h.FragmentCount < MinimumFragmentCount {
		return fmt.Errorf("%w: fragment count %d below the header/items/seal minimum %d",
			ErrFragments, h.FragmentCount, MinimumFragmentCount)
	}
	return nil
}

func appendRecordHeader(b []byte, h *RecordHeader) []byte {
	b = appendVarintField(b, 1, uint64(h.SchemaVersion))
	b = appendBytesField(b, 2, appendNetwork(nil, h.Network))
	b = appendVarintField(b, 3, uint64(h.EventFamily))
	b = appendVarintField(b, 4, uint64(h.EventKind))
	b = appendVarintField(b, 5, uint64(h.RootType))
	b = appendVarintField(b, 6, h.NodeCount)
	b = appendVarintField(b, 7, h.BlobCount)
	b = appendVarintField(b, 8, h.TotalBlobBytes)
	return appendVarintField(b, 9, h.FragmentCount)
}

// recordHeaderSize is the exact encoded size of h.
func recordHeaderSize(h *RecordHeader) uint64 {
	net := 1 + uint64(varintLen(uint64(h.Network.Family))) + (1 + 1 + 32) + (1 + 1 + 32)
	return 1 + uint64(varintLen(uint64(h.SchemaVersion))) +
		1 + uint64(varintLen(net)) + net +
		1 + uint64(varintLen(uint64(h.EventFamily))) +
		1 + uint64(varintLen(uint64(h.EventKind))) +
		1 + uint64(varintLen(uint64(h.RootType))) +
		1 + uint64(varintLen(h.NodeCount)) +
		1 + uint64(varintLen(h.BlobCount)) +
		1 + uint64(varintLen(h.TotalBlobBytes)) +
		1 + uint64(varintLen(h.FragmentCount))
}

func decodeRecordHeader(b []byte) (*RecordHeader, error) {
	r := &wireReader{b: b}
	var h RecordHeader
	seen, err := walkFields("RecordHeader", r, 1, 9, nil, func(field uint32, wire byte, r *wireReader) error {
		switch field {
		case 1, 3, 4, 5:
			v, err := readU32Field(r, wire, "RecordHeader", field)
			if err != nil {
				return err
			}
			switch field {
			case 1:
				h.SchemaVersion = v
			case 3:
				h.EventFamily = v
			case 4:
				h.EventKind = v
			case 5:
				h.RootType = v
			}
			return nil
		case 2:
			if wire != wireBytes {
				return fmt.Errorf("%w: RecordHeader network wire type %d", ErrMalformed, wire)
			}
			msg, err := r.readBytes()
			if err != nil {
				return err
			}
			net, err := decodeNetwork(msg)
			if err != nil {
				return err
			}
			h.Network = net
			return nil
		default: // fields 6..9
			v, err := readU64Field(r, wire, "RecordHeader", field)
			if err != nil {
				return err
			}
			switch field {
			case 6:
				h.NodeCount = v
			case 7:
				h.BlobCount = v
			case 8:
				h.TotalBlobBytes = v
			case 9:
				h.FragmentCount = v
			}
			return nil
		}
	})
	if err != nil {
		return nil, err
	}
	for _, f := range []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9} {
		if err := seen.require(f, "RecordHeader"); err != nil {
			return nil, err
		}
	}
	if err := h.Validate(); err != nil {
		return nil, err
	}
	return &h, nil
}

// readU32Field reads one varint field whose value must fit u32.
func readU32Field(r *wireReader, wire byte, name string, field uint32) (uint32, error) {
	v, err := readU64Field(r, wire, name, field)
	if err != nil {
		return 0, err
	}
	if v > 0xFFFFFFFF {
		return 0, fmt.Errorf("%w: %s field %d value %d above u32", ErrInvalidValue, name, field, v)
	}
	return uint32(v), nil
}

// readU64Field reads one varint field of the pinned tables.
func readU64Field(r *wireReader, wire byte, name string, field uint32) (uint64, error) {
	if wire != wireVarint {
		return 0, fmt.Errorf("%w: %s field %d wire type %d", ErrMalformed, name, field, wire)
	}
	return r.readVarint()
}

// MessageValue is the value of one message node: its registry type key and
// the number of direct children.
type MessageValue struct {
	TypeKey          uint32
	DirectChildCount uint64
}

// ListValue is the value of one list node: its element count.
type ListValue struct {
	Count uint64
}

// BlobRef is the reference of one bytes or UTF-8 node: the blob id (the
// node id), the exact length and the SHA-256 of the content.
type BlobRef struct {
	BlobID uint64
	Length uint64
	SHA256 proto.Hash32
}

// ValueKind is the closed value kind of one typed node (the TypedItem oneof
// fields 5..12).
type ValueKind uint8

// The value kinds of the pinned TypedItem table.
const (
	ValueNone ValueKind = iota
	ValueMessage
	ValueList
	ValueBool
	ValueUint32
	ValueUint64
	ValueEnum
	ValueBytes
	ValueUTF8
)

// String returns the kind name of k.
func (k ValueKind) String() string {
	switch k {
	case ValueNone:
		return "none"
	case ValueMessage:
		return "message"
	case ValueList:
		return "list"
	case ValueBool:
		return "bool"
	case ValueUint32:
		return "uint32"
	case ValueUint64:
		return "uint64"
	case ValueEnum:
		return "enum"
	case ValueBytes:
		return "bytes"
	case ValueUTF8:
		return "utf8"
	}
	return fmt.Sprintf("ValueKind(%d)", uint8(k))
}

// TypedItem is one schema-validated tree node: its position in the tree
// (node id, optional parent, optional field tag or element index) and one
// value kind. Bytes and UTF-8 nodes reference their content through a
// BlobRef; the content itself travels in BlobChunks fragments.
type TypedItem struct {
	NodeID uint64
	// ParentID is present for every node except the root.
	HasParentID bool
	ParentID    uint64
	// FieldTag is present for children of a message node.
	HasFieldTag bool
	FieldTag    uint32
	// ElementIndex is present for children of a list node.
	HasElementIndex bool
	ElementIndex    uint64
	// Kind selects the oneof value; exactly one of the fields below it
	// fills.
	Kind        ValueKind
	Message     MessageValue
	List        ListValue
	BoolValue   bool
	Uint32Value uint32
	Uint64Value uint64
	EnumValue   uint32
	BytesValue  BlobRef
	UTF8Value   BlobRef
}

// Validate rejects item-level shape violations: an unset value kind, a zero
// field tag or type key, or a scalar leaf carrying structure fields.
// Tree-level rules (parent, order, counts) belong to the tree walker.
func (it *TypedItem) Validate() error {
	if it.Kind == ValueNone {
		return fmt.Errorf("%w: typed item %d has no value kind", ErrMissingField, it.NodeID)
	}
	if it.HasFieldTag && it.FieldTag == 0 {
		return fmt.Errorf("%w: node %d field tag 0, field tags are positive", ErrInvalidValue, it.NodeID)
	}
	if it.Kind == ValueMessage && it.Message.TypeKey == 0 {
		return fmt.Errorf("%w: node %d type key 0, type keys are positive", ErrInvalidValue, it.NodeID)
	}
	return nil
}

func appendBlobRef(b []byte, ref *BlobRef) []byte {
	b = appendVarintField(b, 1, ref.BlobID)
	b = appendVarintField(b, 2, ref.Length)
	return appendBytesField(b, 3, ref.SHA256[:])
}

// blobRefSize is the exact encoded size of ref.
func blobRefSize(ref *BlobRef) uint64 {
	return 1 + uint64(varintLen(ref.BlobID)) +
		1 + uint64(varintLen(ref.Length)) +
		1 + 1 + 32
}

func decodeBlobRef(b []byte) (BlobRef, error) {
	r := &wireReader{b: b}
	var ref BlobRef
	seen, err := walkFields("BlobRef", r, 1, 3, nil, func(field uint32, wire byte, r *wireReader) error {
		switch field {
		case 1, 2:
			v, err := readU64Field(r, wire, "BlobRef", field)
			if err != nil {
				return err
			}
			if field == 1 {
				ref.BlobID = v
			} else {
				ref.Length = v
			}
			return nil
		default:
			buf, err := r.readFixedBytes(wire, field, 32, "blob sha256")
			if err != nil {
				return err
			}
			copy(ref.SHA256[:], buf)
			return nil
		}
	})
	if err != nil {
		return BlobRef{}, err
	}
	for _, f := range []uint32{1, 2, 3} {
		if err := seen.require(f, "BlobRef"); err != nil {
			return BlobRef{}, err
		}
	}
	return ref, nil
}

func appendTypedItem(b []byte, it *TypedItem) []byte {
	b = appendVarintField(b, 1, it.NodeID)
	if it.HasParentID {
		b = appendVarintField(b, 2, it.ParentID)
	}
	if it.HasFieldTag {
		b = appendVarintField(b, 3, uint64(it.FieldTag))
	}
	if it.HasElementIndex {
		b = appendVarintField(b, 4, it.ElementIndex)
	}
	switch it.Kind {
	case ValueMessage:
		inner := appendVarintField(nil, 1, uint64(it.Message.TypeKey))
		inner = appendVarintField(inner, 2, it.Message.DirectChildCount)
		b = appendBytesField(b, 5, inner)
	case ValueList:
		inner := appendVarintField(nil, 1, it.List.Count)
		b = appendBytesField(b, 6, inner)
	case ValueBool:
		v := uint64(0)
		if it.BoolValue {
			v = 1
		}
		b = appendVarintField(b, 7, v)
	case ValueUint32:
		b = appendVarintField(b, 8, uint64(it.Uint32Value))
	case ValueUint64:
		b = appendVarintField(b, 9, it.Uint64Value)
	case ValueEnum:
		b = appendVarintField(b, 10, uint64(it.EnumValue))
	case ValueBytes:
		b = appendBytesField(b, 11, appendBlobRef(nil, &it.BytesValue))
	case ValueUTF8:
		b = appendBytesField(b, 12, appendBlobRef(nil, &it.UTF8Value))
	}
	return b
}

// typedItemSize is the exact encoded size of it, computed without building
// the intermediate oneof message bytes; it always equals
// len(appendTypedItem(nil, it)).
func typedItemSize(it *TypedItem) uint64 {
	size := 1 + uint64(varintLen(it.NodeID))
	if it.HasParentID {
		size += 1 + uint64(varintLen(it.ParentID))
	}
	if it.HasFieldTag {
		size += 1 + uint64(varintLen(uint64(it.FieldTag)))
	}
	if it.HasElementIndex {
		size += 1 + uint64(varintLen(it.ElementIndex))
	}
	switch it.Kind {
	case ValueMessage:
		inner := 1 + uint64(varintLen(uint64(it.Message.TypeKey))) +
			1 + uint64(varintLen(it.Message.DirectChildCount))
		size += 1 + uint64(varintLen(inner)) + inner
	case ValueList:
		inner := 1 + uint64(varintLen(it.List.Count))
		size += 1 + uint64(varintLen(inner)) + inner
	case ValueBool, ValueUint32, ValueUint64, ValueEnum:
		var v uint64
		switch it.Kind {
		case ValueBool:
			if it.BoolValue {
				v = 1
			}
		case ValueUint32:
			v = uint64(it.Uint32Value)
		case ValueUint64:
			v = it.Uint64Value
		case ValueEnum:
			v = uint64(it.EnumValue)
		}
		size += 1 + uint64(varintLen(v))
	case ValueBytes:
		inner := blobRefSize(&it.BytesValue)
		size += 1 + uint64(varintLen(inner)) + inner
	case ValueUTF8:
		inner := blobRefSize(&it.UTF8Value)
		size += 1 + uint64(varintLen(inner)) + inner
	}
	return size
}

func decodeTypedItem(b []byte) (TypedItem, error) {
	r := &wireReader{b: b}
	var it TypedItem
	var valueFields uint8
	seen, err := walkFields("TypedItem", r, 1, 12, nil,
		func(field uint32, wire byte, r *wireReader) error {
			switch field {
			case 1:
				v, err := readU64Field(r, wire, "TypedItem", field)
				if err != nil {
					return err
				}
				it.NodeID = v
				return nil
			case 2:
				v, err := readU64Field(r, wire, "TypedItem", field)
				if err != nil {
					return err
				}
				it.HasParentID = true
				it.ParentID = v
				return nil
			case 3:
				v, err := readU32Field(r, wire, "TypedItem", field)
				if err != nil {
					return err
				}
				it.HasFieldTag = true
				it.FieldTag = v
				return nil
			case 4:
				v, err := readU64Field(r, wire, "TypedItem", field)
				if err != nil {
					return err
				}
				it.HasElementIndex = true
				it.ElementIndex = v
				return nil
			default:
				valueFields++
				switch field {
				case 5:
					if wire != wireBytes {
						return fmt.Errorf("%w: TypedItem message value wire type %d", ErrMalformed, wire)
					}
					msg, err := r.readBytes()
					if err != nil {
						return err
					}
					mv, err := decodeMessageValue(msg)
					if err != nil {
						return err
					}
					it.Kind = ValueMessage
					it.Message = mv
				case 6:
					if wire != wireBytes {
						return fmt.Errorf("%w: TypedItem list value wire type %d", ErrMalformed, wire)
					}
					msg, err := r.readBytes()
					if err != nil {
						return err
					}
					lv, err := decodeListValue(msg)
					if err != nil {
						return err
					}
					it.Kind = ValueList
					it.List = lv
				case 7:
					v, err := readU64Field(r, wire, "TypedItem", field)
					if err != nil {
						return err
					}
					if v > 1 {
						return fmt.Errorf("%w: bool value %d, want 0 or 1", ErrInvalidValue, v)
					}
					it.Kind = ValueBool
					it.BoolValue = v == 1
				case 8:
					v, err := readU32Field(r, wire, "TypedItem", field)
					if err != nil {
						return err
					}
					it.Kind = ValueUint32
					it.Uint32Value = v
				case 9:
					v, err := readU64Field(r, wire, "TypedItem", field)
					if err != nil {
						return err
					}
					it.Kind = ValueUint64
					it.Uint64Value = v
				case 10:
					v, err := readU32Field(r, wire, "TypedItem", field)
					if err != nil {
						return err
					}
					it.Kind = ValueEnum
					it.EnumValue = v
				case 11, 12:
					if wire != wireBytes {
						return fmt.Errorf("%w: TypedItem blob ref wire type %d", ErrMalformed, wire)
					}
					msg, err := r.readBytes()
					if err != nil {
						return err
					}
					ref, err := decodeBlobRef(msg)
					if err != nil {
						return err
					}
					if field == 11 {
						it.Kind = ValueBytes
						it.BytesValue = ref
					} else {
						it.Kind = ValueUTF8
						it.UTF8Value = ref
					}
				}
				return nil
			}
		})
	if err != nil {
		return TypedItem{}, err
	}
	if err := seen.require(1, "TypedItem node_id"); err != nil {
		return TypedItem{}, err
	}
	if valueFields != 1 {
		return TypedItem{}, fmt.Errorf("%w: TypedItem node %d carries %d value fields, want exactly one",
			ErrInvalidValue, it.NodeID, valueFields)
	}
	if err := it.Validate(); err != nil {
		return TypedItem{}, err
	}
	return it, nil
}

func decodeMessageValue(b []byte) (MessageValue, error) {
	r := &wireReader{b: b}
	var mv MessageValue
	seen, err := walkFields("MessageValue", r, 1, 2, nil, func(field uint32, wire byte, r *wireReader) error {
		if field == 1 {
			v, err := readU32Field(r, wire, "MessageValue", field)
			if err != nil {
				return err
			}
			mv.TypeKey = v
			return nil
		}
		v, err := readU64Field(r, wire, "MessageValue", field)
		if err != nil {
			return err
		}
		mv.DirectChildCount = v
		return nil
	})
	if err != nil {
		return MessageValue{}, err
	}
	for _, f := range []uint32{1, 2} {
		if err := seen.require(f, "MessageValue"); err != nil {
			return MessageValue{}, err
		}
	}
	return mv, nil
}

func decodeListValue(b []byte) (ListValue, error) {
	r := &wireReader{b: b}
	var lv ListValue
	seen, err := walkFields("ListValue", r, 1, 1, nil, func(field uint32, wire byte, r *wireReader) error {
		v, err := readU64Field(r, wire, "ListValue", field)
		if err != nil {
			return err
		}
		lv.Count = v
		return nil
	})
	if err != nil {
		return ListValue{}, err
	}
	if err := seen.require(1, "ListValue count"); err != nil {
		return ListValue{}, err
	}
	return lv, nil
}

// BlobChunk is one content chunk of one blob: at most 16384 data bytes at an
// exact offset of the blob.
type BlobChunk struct {
	BlobID uint64
	Offset uint64
	Data   []byte
}

// Validate rejects a chunk without data or above the chunk data bound.
func (c *BlobChunk) Validate() error {
	if len(c.Data) == 0 {
		return fmt.Errorf("%w: blob %d chunk at %d carries zero data bytes", ErrInvalidValue, c.BlobID, c.Offset)
	}
	if len(c.Data) > BlobChunkMaxData {
		return fmt.Errorf("%w: blob %d chunk at %d carries %d bytes, bound %d",
			ErrInvalidValue, c.BlobID, c.Offset, len(c.Data), BlobChunkMaxData)
	}
	return nil
}

func appendBlobChunk(b []byte, c *BlobChunk) []byte {
	b = appendVarintField(b, 1, c.BlobID)
	b = appendVarintField(b, 2, c.Offset)
	return appendBytesField(b, 3, c.Data)
}

// blobChunkSize is the exact encoded size of c.
func blobChunkSize(c *BlobChunk) uint64 {
	return 1 + uint64(varintLen(c.BlobID)) +
		1 + uint64(varintLen(c.Offset)) +
		1 + uint64(varintLen(uint64(len(c.Data)))) + uint64(len(c.Data))
}

func decodeBlobChunk(b []byte) (BlobChunk, error) {
	r := &wireReader{b: b}
	var c BlobChunk
	seen, err := walkFields("BlobChunk", r, 1, 3, nil, func(field uint32, wire byte, r *wireReader) error {
		switch field {
		case 1, 2:
			v, err := readU64Field(r, wire, "BlobChunk", field)
			if err != nil {
				return err
			}
			if field == 1 {
				c.BlobID = v
			} else {
				c.Offset = v
			}
			return nil
		default:
			if wire != wireBytes {
				return fmt.Errorf("%w: BlobChunk data wire type %d", ErrMalformed, wire)
			}
			data, err := r.readBytes()
			if err != nil {
				return err
			}
			c.Data = data
			return nil
		}
	})
	if err != nil {
		return BlobChunk{}, err
	}
	for _, f := range []uint32{1, 2, 3} {
		if err := seen.require(f, "BlobChunk"); err != nil {
			return BlobChunk{}, err
		}
	}
	if err := c.Validate(); err != nil {
		return BlobChunk{}, err
	}
	return c, nil
}

// RecordSeal is the last fragment of a record: the fragment count, the
// content byte total (every fragment except the seal) and the record digest
// over those content fragments.
type RecordSeal struct {
	FragmentCount uint64
	ContentBytes  uint64
	RecordDigest  proto.Hash32
}

// Validate rejects a seal below the fragment minimum.
func (s *RecordSeal) Validate() error {
	if s.FragmentCount < MinimumFragmentCount {
		return fmt.Errorf("%w: seal fragment count %d below the minimum %d",
			ErrFragments, s.FragmentCount, MinimumFragmentCount)
	}
	return nil
}

func appendRecordSeal(b []byte, s *RecordSeal) []byte {
	b = appendVarintField(b, 1, s.FragmentCount)
	b = appendVarintField(b, 2, s.ContentBytes)
	return appendBytesField(b, 3, s.RecordDigest[:])
}

// recordSealSize is the exact encoded size of s.
func recordSealSize(s *RecordSeal) uint64 {
	return 1 + uint64(varintLen(s.FragmentCount)) +
		1 + uint64(varintLen(s.ContentBytes)) +
		1 + 1 + 32
}

func decodeRecordSeal(b []byte) (*RecordSeal, error) {
	r := &wireReader{b: b}
	var s RecordSeal
	seen, err := walkFields("RecordSeal", r, 1, 3, nil, func(field uint32, wire byte, r *wireReader) error {
		switch field {
		case 1, 2:
			v, err := readU64Field(r, wire, "RecordSeal", field)
			if err != nil {
				return err
			}
			if field == 1 {
				s.FragmentCount = v
			} else {
				s.ContentBytes = v
			}
			return nil
		default:
			buf, err := r.readFixedBytes(wire, field, 32, "record digest")
			if err != nil {
				return err
			}
			copy(s.RecordDigest[:], buf)
			return nil
		}
	})
	if err != nil {
		return nil, err
	}
	for _, f := range []uint32{1, 2, 3} {
		if err := seen.require(f, "RecordSeal"); err != nil {
			return nil, err
		}
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// RecordFragment is one serialized fragment of a record: the pinned wrapper
// fields (record format version, event id, fragment index) and exactly one
// payload. The serialized length of the whole message is 1..65536 bytes.
type RecordFragment struct {
	RecordFormatVersion uint32
	EventID             proto.EventID
	FragmentIndex       uint64
	Payload             FragmentPayload
	Header              *RecordHeader
	Items               []TypedItem
	Blobs               []BlobChunk
	Seal                *RecordSeal
}

// Validate rejects a fragment with a wrong version, an unset identity, a
// missing or overfilled payload, or payload-level violations.
func (f *RecordFragment) Validate() error {
	if f.RecordFormatVersion != proto.RecordFormatVersion {
		return fmt.Errorf("%w: record format version %d, want %d",
			ErrVersionUnsupported, f.RecordFormatVersion, proto.RecordFormatVersion)
	}
	if err := f.EventID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidValue, err)
	}
	switch f.Payload {
	case PayloadHeader:
		if f.Header == nil {
			return fmt.Errorf("%w: header payload without a header", ErrMissingField)
		}
		if err := f.Header.Validate(); err != nil {
			return err
		}
	case PayloadItems:
		if len(f.Items) == 0 {
			return fmt.Errorf("%w: items fragment without items", ErrFragments)
		}
		for i := range f.Items {
			if err := f.Items[i].Validate(); err != nil {
				return err
			}
		}
	case PayloadBlobs:
		if len(f.Blobs) == 0 {
			return fmt.Errorf("%w: blobs fragment without chunks", ErrFragments)
		}
		for i := range f.Blobs {
			if err := f.Blobs[i].Validate(); err != nil {
				return err
			}
		}
	case PayloadSeal:
		if f.Seal == nil {
			return fmt.Errorf("%w: seal payload without a seal", ErrMissingField)
		}
		if err := f.Seal.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: no payload set", ErrMissingField)
	}
	return nil
}

// appendFragment wraps already-encoded payload message bytes into the
// canonical fragment form: fields 1..3 plus the payload field tag.
func appendFragment(b []byte, eventID proto.EventID, fragmentIndex uint64, payloadTag uint32, payload []byte) []byte {
	b = appendVarintField(b, 1, uint64(proto.RecordFormatVersion))
	b = appendBytesField(b, 2, eventID[:])
	b = appendVarintField(b, 3, fragmentIndex)
	return appendBytesField(b, payloadTag, payload)
}

// fragmentEncodedSize is the exact serialized size of one fragment whose
// fragment index is fragmentIndex and whose payload message bytes measure
// innerLen: fields 1..3 plus one payload field.
func fragmentEncodedSize(fragmentIndex, innerLen uint64) uint64 {
	return 2 + // field 1: key + one-byte version varint
		42 + // field 2: key + one-byte length + 40-byte event id
		1 + uint64(varintLen(fragmentIndex)) + // field 3
		1 + uint64(varintLen(innerLen)) + innerLen // payload field
}

// itemWireSize is the wire size of one TypedItem inside a TypedItems
// message: key, length prefix and item bytes.
func itemWireSize(it *TypedItem) uint64 {
	itemLen := typedItemSize(it)
	return 1 + uint64(varintLen(itemLen)) + itemLen
}

// chunkWireSize is the wire size of one BlobChunk inside a BlobChunks
// message.
func chunkWireSize(c *BlobChunk) uint64 {
	chunkLen := blobChunkSize(c)
	return 1 + uint64(varintLen(chunkLen)) + chunkLen
}

// Encode returns the canonical serialized fragment.
func (f *RecordFragment) Encode() ([]byte, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	var inner []byte
	var tag uint32
	switch f.Payload {
	case PayloadHeader:
		inner, tag = appendRecordHeader(nil, f.Header), fieldHeaderTag
	case PayloadItems:
		tag = fieldItemsTag
		for i := range f.Items {
			inner = appendBytesField(inner, 1, appendTypedItem(nil, &f.Items[i]))
		}
	case PayloadBlobs:
		tag = fieldBlobsTag
		for i := range f.Blobs {
			inner = appendBytesField(inner, 1, appendBlobChunk(nil, &f.Blobs[i]))
		}
	case PayloadSeal:
		inner, tag = appendRecordSeal(nil, f.Seal), fieldSealTag
	}
	out := appendFragment(nil, f.EventID, f.FragmentIndex, tag, inner)
	if len(out) > proto.FragmentMaxBytes {
		return nil, fmt.Errorf("%w: fragment %d serializes to %d bytes, bound %d",
			ErrInvalidValue, f.FragmentIndex, len(out), proto.FragmentMaxBytes)
	}
	return out, nil
}

// DecodeRecordFragment parses canonical fragment bytes. It rejects every
// canonical-encoding violation (unknown fields, duplicates, out-of-order
// tags, non-minimal varints, missing named scalars, a doubled or missing
// oneof, wrong versions, wrong lengths, trailing bytes) and any input
// outside the 1..65536 fragment bound.
func DecodeRecordFragment(b []byte) (*RecordFragment, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("%w: fragment is empty", ErrInvalidValue)
	}
	if len(b) > proto.FragmentMaxBytes {
		return nil, fmt.Errorf("%w: fragment of %d bytes exceeds the %d bound",
			ErrInvalidValue, len(b), proto.FragmentMaxBytes)
	}
	r := &wireReader{b: b}
	var f RecordFragment
	var payloadFields uint8
	seen, err := walkFields("RecordFragment", r, 1, fieldSealTag,
		nil, func(field uint32, wire byte, r *wireReader) error {
			switch field {
			case 1:
				v, err := readU32Field(r, wire, "RecordFragment", field)
				if err != nil {
					return err
				}
				if v != proto.RecordFormatVersion {
					return fmt.Errorf("%w: record format version %d, want %d",
						ErrVersionUnsupported, v, proto.RecordFormatVersion)
				}
				f.RecordFormatVersion = v
				return nil
			case 2:
				buf, err := r.readFixedBytes(wire, field, 40, "event_id")
				if err != nil {
					return err
				}
				copy(f.EventID[:], buf)
				return nil
			case 3:
				v, err := readU64Field(r, wire, "RecordFragment", field)
				if err != nil {
					return err
				}
				f.FragmentIndex = v
				return nil
			default:
				payloadFields++
				if wire != wireBytes {
					return fmt.Errorf("%w: RecordFragment payload field %d wire type %d", ErrMalformed, field, wire)
				}
				msg, err := r.readBytes()
				if err != nil {
					return err
				}
				switch field {
				case fieldHeaderTag:
					h, err := decodeRecordHeader(msg)
					if err != nil {
						return err
					}
					f.Payload = PayloadHeader
					f.Header = h
				case fieldItemsTag:
					items, err := decodeTypedItems(msg)
					if err != nil {
						return err
					}
					f.Payload = PayloadItems
					f.Items = items
				case fieldBlobsTag:
					chunks, err := decodeBlobChunks(msg)
					if err != nil {
						return err
					}
					f.Payload = PayloadBlobs
					f.Blobs = chunks
				case fieldSealTag:
					s, err := decodeRecordSeal(msg)
					if err != nil {
						return err
					}
					f.Payload = PayloadSeal
					f.Seal = s
				}
				return nil
			}
		})
	if err != nil {
		return nil, err
	}
	for _, field := range []uint32{1, 2, 3} {
		if err := seen.require(field, "RecordFragment"); err != nil {
			return nil, err
		}
	}
	if payloadFields == 0 {
		return nil, fmt.Errorf("%w: fragment %d carries no payload (oneof fields 4..7)",
			ErrMissingField, f.FragmentIndex)
	}
	if payloadFields > 1 {
		return nil, fmt.Errorf("%w: fragment %d carries %d payload fields, want exactly one",
			ErrInvalidValue, f.FragmentIndex, payloadFields)
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// decodeTypedItems parses the repeated field of one TypedItems message.
func decodeTypedItems(b []byte) ([]TypedItem, error) {
	r := &wireReader{b: b}
	var items []TypedItem
	_, err := walkFields("TypedItems", r, 1, 1, map[uint32]bool{1: true},
		func(field uint32, wire byte, r *wireReader) error {
			if wire != wireBytes {
				return fmt.Errorf("%w: TypedItems item wire type %d", ErrMalformed, wire)
			}
			msg, err := r.readBytes()
			if err != nil {
				return err
			}
			item, err := decodeTypedItem(msg)
			if err != nil {
				return err
			}
			items = append(items, item)
			return nil
		})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%w: TypedItems message without items", ErrFragments)
	}
	return items, nil
}

// decodeBlobChunks parses the repeated field of one BlobChunks message.
func decodeBlobChunks(b []byte) ([]BlobChunk, error) {
	r := &wireReader{b: b}
	var chunks []BlobChunk
	_, err := walkFields("BlobChunks", r, 1, 1, map[uint32]bool{1: true},
		func(field uint32, wire byte, r *wireReader) error {
			if wire != wireBytes {
				return fmt.Errorf("%w: BlobChunks chunk wire type %d", ErrMalformed, wire)
			}
			msg, err := r.readBytes()
			if err != nil {
				return err
			}
			chunk, err := decodeBlobChunk(msg)
			if err != nil {
				return err
			}
			chunks = append(chunks, chunk)
			return nil
		})
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("%w: BlobChunks message without chunks", ErrFragments)
	}
	return chunks, nil
}
