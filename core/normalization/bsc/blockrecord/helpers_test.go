package blockrecord

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
	"unicode/utf8"

	bscblock "github.com/sigaev-pro/bc-indexer/core/normalization/bsc/block"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// hashOf returns a hash whose bytes are seed, seed+1, seed+2 and so on; it
// is never the zero hash.
func hashOf(seed byte) block.Hash32 {
	var h block.Hash32
	for i := range h {
		h[i] = seed + byte(i)
	}
	return h
}

func idOf(seed byte) semantic.ID16 {
	var id semantic.ID16
	for i := range id {
		id[i] = seed + byte(i)
	}
	return id
}

func addressOf(seed byte) block.Address20 {
	var a block.Address20
	for i := range a {
		a[i] = seed + byte(i)
	}
	return a
}

func uint64Of(v uint64) *uint64 { return &v }

// validInput is one complete decided import of a block above the genesis,
// with every optional header field present (three of them as present zeros)
// and an attested snapshot.
func validInput() Input {
	var chainID block.UInt256
	chainID[31] = 97
	mix := hashOf(0x45)
	mix[24], mix[25], mix[26], mix[27] = 0, 0, 0, 0
	mix[28], mix[29], mix[30], mix[31] = 0, 0, 0x01, 0xC2
	var emptyTrieRoot block.Hash32
	copy(emptyTrieRoot[:], []byte{0x56, 0xe8, 0x1f, 0x17, 0x1b, 0xcc, 0x55, 0xa6, 0xff, 0x83, 0x45, 0xe6,
		0x92, 0xc0, 0xf8, 0x6e, 0x5b, 0x48, 0xe0, 0x1b, 0x99, 0x6c, 0xad, 0xc0, 0x01, 0x62, 0x2f, 0xb5,
		0xe3, 0x63, 0xb4, 0x21})
	var sha256Empty block.Hash32
	copy(sha256Empty[:], []byte{0xe3, 0xb0, 0xc4, 0x42, 0x98, 0xfc, 0x1c, 0x14, 0x9a, 0xfb, 0xf4, 0xc8, 0x99,
		0x6f, 0xb9, 0x24, 0x27, 0xae, 0x41, 0xe4, 0x64, 0x9b, 0x93, 0x4c, 0xa4, 0x95,
		0x99, 0x1b, 0x78, 0x52, 0xb8, 0x55})
	header := bscblock.SourceHeader{
		Hash:             hashOf(0x22),
		ParentHash:       hashOf(0x21),
		UncleHash:        block.Hash32{0x1d},
		Beneficiary:      addressOf(0x40),
		StateRoot:        hashOf(0x41),
		TransactionsRoot: hashOf(0x42),
		ReceiptsRoot:     hashOf(0x43),
		LogsBloom:        block.Bloom{0x11, 0x22, 0x33},
		Difficulty:       block.UInt256{0x02},
		Number:           420,
		GasLimit:         100_000_000,
		GasUsed:          123_456,
		Time:             1_795_616_202,
		ExtraData:        []byte("BSC fixture extra data: 9db76a1c62"),
		MixDigest:        mix,
		Nonce:            block.Nonce{0, 0, 0, 0, 0, 0, 0, 1},
		SourceRLP:        bytes.Repeat([]byte{0xf9, 0x01, 0x5c, 0xa0}, 64),
	}
	header.BaseFee = &block.UInt256{}
	header.WithdrawalsRoot = &emptyTrieRoot
	header.BlobGasUsed = uint64Of(0)
	header.ExcessBlobGas = uint64Of(0)
	parentBeacon := block.Hash32{}
	header.ParentBeaconBlockRoot = &parentBeacon
	header.RequestsHash = &sha256Empty
	return Input{
		Network:             block.NetworkIdentity{Family: "EVM", ChainID: chainID, GenesisHash: hashOf(0x01)},
		NodeInstanceID:      idOf(0x11),
		ProducerID:          idOf(0x12),
		RunID:               idOf(0x13),
		WriterEpoch:         idOf(0x14),
		SourceSequence:      1,
		DecisionSequence:    7,
		ObservationSequence: 3,
		Header:              header,
		TxCount:             3,
		SystemTxCount:       uint64Of(1),
		OldHead:             Head{Number: 419, Hash: hashOf(0x21)},
		NewHead:             Head{Number: 420, Hash: hashOf(0x22)},
		Justified:           Head{Number: 419, Hash: hashOf(0x21)},
		Finalized:           Head{Number: 418, Hash: hashOf(0x23)},
		Attestation: Attestation{Status: StatusAttested, Justified: Head{Number: 419, Hash: hashOf(0x21)},
			Finalized: Head{Number: 418, Hash: hashOf(0x23)}, Digest: hashOf(0x50).String()},
	}
}

func eventIDOf(in Input, sequence uint64) proto.EventID {
	return proto.NewEventID(proto.ID16(in.ProducerID), proto.ID16(in.RunID), sequence)
}

// flatNode is one tree node in a shape shared by built and decoded nodes.
type flatNode struct {
	hasParent bool
	parent    uint64
	hasTag    bool
	tag       uint32
	isElement bool
	element   uint64
	kind      codec.ValueKind
	typeKey   uint32
	children  uint64
	boolean   bool
	u32       uint32
	u64       uint64
	enum      uint32
	value     []byte
}

// String renders a node for a failure message.
func (f flatNode) String() string {
	return fmt.Sprintf("parent %t/%d tag %t/%d element %t/%d kind %s type %d children %d bool %t "+
		"u32 %d u64 %d enum %d value %x", f.hasParent, f.parent, f.hasTag, f.tag, f.isElement, f.element,
		f.kind, f.typeKey, f.children, f.boolean, f.u32, f.u64, f.enum, f.value)
}

func flattenBuilt(nodes []codec.TreeNode) []flatNode {
	out := make([]flatNode, len(nodes))
	// The parent of a built node comes from the open containers.
	type open struct {
		index     int
		remaining uint64
	}
	var stack []open
	for i := range nodes {
		n := &nodes[i]
		for len(stack) > 0 && stack[len(stack)-1].remaining == 0 {
			stack = stack[:len(stack)-1]
		}
		f := flatNode{hasTag: n.HasFieldTag, tag: n.FieldTag, isElement: n.IsElement, element: n.ElementIndex,
			kind: n.Kind, typeKey: n.TypeKey, children: n.ChildCount, boolean: n.Bool, u32: n.Uint32,
			u64: n.Uint64, enum: n.Enum, value: n.Value}
		if len(stack) > 0 {
			f.hasParent, f.parent = true, uint64(stack[len(stack)-1].index)
			stack[len(stack)-1].remaining--
		}
		out[i] = f
		if n.Kind == codec.ValueMessage || n.Kind == codec.ValueList {
			stack = append(stack, open{index: i, remaining: n.ChildCount})
		}
	}
	return out
}

func flattenRead(nodes []codec.ReadNode) []flatNode {
	out := make([]flatNode, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		out[i] = flatNode{hasParent: n.HasParent, parent: n.ParentID, hasTag: n.HasFieldTag, tag: n.FieldTag,
			isElement: n.IsElement, element: n.ElementIndex, kind: n.Kind, typeKey: n.TypeKey,
			children: n.ChildCount, boolean: n.Bool, u32: n.Uint32, u64: n.Uint64, enum: n.Enum, value: n.Value}
	}
	return out
}

// pathsOf indexes a flattened tree by field path: the root carries the
// name of its message, its fields follow as ".name" and a list element as
// "[index]". Every field tag must be registered for its message.
func pathsOf(t *testing.T, flat []flatNode) map[string]flatNode {
	t.Helper()
	s := proto.LogicalSchemaV2()
	paths := make(map[string]flatNode, len(flat))
	byID := make([]string, len(flat))
	for i := range flat {
		n := &flat[i]
		if n.kind == codec.ValueList {
			t.Fatalf("node %d is a list; the block record has no list", i)
		}
		var path string
		if n.hasParent {
			parent := &flat[n.parent]
			if !n.hasTag || n.isElement {
				t.Fatalf("node %d is not a message child", i)
			}
			f, ok := s.Field(parent.typeKey, n.tag)
			if !ok {
				t.Fatalf("node %d uses tag %d, which the registry does not hold for type key %d", i, n.tag,
					parent.typeKey)
			}
			path = byID[n.parent] + "." + f.Name
		} else {
			m, ok := s.MessageType(n.typeKey)
			if !ok {
				t.Fatalf("node %d is the root with unregistered type key %d", i, n.typeKey)
			}
			path = m.Name
		}
		byID[i] = path
		paths[path] = *n
	}
	return paths
}

// builtPaths indexes a built tree by field path.
func builtPaths(t *testing.T, nodes []codec.TreeNode) map[string]flatNode {
	t.Helper()
	return pathsOf(t, flattenBuilt(nodes))
}

// readBuild decodes the fragments of a built event.
func readBuild(t *testing.T, built BuiltEvent) (*codec.RecordHeader, []codec.ReadNode) {
	t.Helper()
	header, nodes, err := codec.ReadRecord(built.Fragments)
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	return header, nodes
}

// checkRegistryConformance verifies a decoded tree against the registry:
// the root type key, every type key, field tag and enum value, the kind and
// the byte length of every node, every mandatory field present exactly
// once, no field present twice, and exactly one variant of every oneof.
func checkRegistryConformance(t *testing.T, header *codec.RecordHeader, nodes []codec.ReadNode) {
	t.Helper()
	s := proto.LogicalSchemaV2()
	if nodes[0].Kind != codec.ValueMessage || nodes[0].TypeKey != header.RootType || nodes[0].HasParent {
		t.Fatalf("root node %+v, header root type %d", nodes[0], header.RootType)
	}
	for i := range nodes {
		n := &nodes[i]
		if n.Kind != codec.ValueMessage {
			continue
		}
		m, ok := s.MessageType(n.TypeKey)
		if !ok {
			t.Fatalf("node %d uses unregistered type key %d", i, n.TypeKey)
		}
		present := make(map[uint32]int, len(m.Fields))
		for j := range nodes {
			c := &nodes[j]
			if !c.HasParent || c.ParentID != uint64(i) {
				continue
			}
			f, ok := s.Field(n.TypeKey, c.FieldTag)
			if !ok {
				t.Fatalf("node %d is a child of %s with unregistered tag %d", j, m.Name, c.FieldTag)
			}
			if valueKind(f.Kind) != c.Kind {
				t.Fatalf("%s.%s is %s in the registry and %s on the wire", m.Name, f.Name, f.Kind, c.Kind)
			}
			switch c.Kind {
			case codec.ValueMessage:
				if c.TypeKey != f.Type {
					t.Fatalf("%s.%s carries type key %d, want %d", m.Name, f.Name, c.TypeKey, f.Type)
				}
			case codec.ValueEnum:
				if _, ok := s.EnumValue(f.Enum, c.Enum); !ok {
					t.Fatalf("%s.%s carries unregistered value %d of enum %s", m.Name, f.Name, c.Enum, f.Enum)
				}
			case codec.ValueBytes:
				if f.ByteLen != 0 && uint64(len(c.Value)) != uint64(f.ByteLen) {
					t.Fatalf("%s.%s holds %d bytes, want %d", m.Name, f.Name, len(c.Value), f.ByteLen)
				}
			case codec.ValueUTF8:
				if !utf8.Valid(c.Value) {
					t.Fatalf("%s.%s is not valid UTF-8", m.Name, f.Name)
				}
			}
			present[c.FieldTag]++
		}
		groups := make(map[string][]uint32)
		for _, f := range m.Fields {
			count := present[f.Tag]
			switch {
			case f.Oneof != "":
				groups[f.Oneof] = append(groups[f.Oneof], f.Tag)
			case f.Optional:
				if count > 1 {
					t.Fatalf("%s.%s is present %d times", m.Name, f.Name, count)
				}
			default:
				if count != 1 {
					t.Fatalf("%s.%s is mandatory and present %d times", m.Name, f.Name, count)
				}
			}
		}
		for group, tags := range groups {
			total := 0
			for _, tag := range tags {
				total += present[tag]
			}
			if total != 1 {
				t.Fatalf("oneof %s of %s has %d variants present, want exactly one", group, m.Name, total)
			}
		}
	}
}

// samePaths compares two path indexes node by node.
func samePaths(a, b map[string]flatNode) error {
	aKeys, bKeys := make([]string, 0, len(a)), make([]string, 0, len(b))
	for path := range a {
		aKeys = append(aKeys, path)
	}
	for path := range b {
		bKeys = append(bKeys, path)
	}
	slices.Sort(aKeys)
	slices.Sort(bKeys)
	if !slices.Equal(aKeys, bKeys) {
		return fmt.Errorf("the trees hold different paths:\n built %v\n read  %v", aKeys, bKeys)
	}
	for _, path := range aKeys {
		x, y := a[path], b[path]
		if x.hasParent != y.hasParent || x.parent != y.parent || x.hasTag != y.hasTag || x.tag != y.tag ||
			x.isElement != y.isElement || x.element != y.element || x.kind != y.kind || x.typeKey != y.typeKey ||
			x.children != y.children || x.boolean != y.boolean || x.u32 != y.u32 || x.u64 != y.u64 ||
			x.enum != y.enum || !bytes.Equal(x.value, y.value) {
			return fmt.Errorf("node %q differs:\n built %s\n read  %s", path, x, y)
		}
	}
	return nil
}
