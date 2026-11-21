// The typed tree model of D73 and its preorder walker. A record's content
// is a tree of schema-validated nodes: the root is the logical Envelope
// message, every node sits at a path of field tags and element indices, and
// bytes or UTF-8 leaves reference their content as blobs. The walker checks
// the structural rules both directions share: continuous node ids from zero
// in depth-first preorder, parents before children, message children in
// ascending field tags without element indices, list children in element
// index order without field tags, and declared child counts that match the
// actual children (MESSAGE_CONTRACT section 6.1, tree rules 1..2).
package codec

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// TreeLimits bound the tree shape this in-memory codec accepts. The D73
// contract itself allows unlimited nesting verified through disk-backed
// indexes; this codec processes a tree in memory, so it pins depth and
// breadth to bounds derived from the fragment size — the deepest path or
// widest child index, materialized as one u64 node id per level or child,
// fits inside a single 65536-byte fragment. The derivation keeps the
// walker's verification state proportional to one fragment, not to the
// record.
type TreeLimits struct {
	// MaxDepth is the greatest root-to-leaf depth (the root is depth 1).
	MaxDepth uint64
	// MaxChildrenPerNode is the greatest declared child count of one
	// message or list node.
	MaxChildrenPerNode uint64
}

// DefaultTreeLimits returns the fragment-derived limits:
// proto.FragmentMaxBytes / 8 for both depth and breadth.
func DefaultTreeLimits() TreeLimits {
	return TreeLimits{
		MaxDepth:           proto.FragmentMaxBytes / 8,
		MaxChildrenPerNode: proto.FragmentMaxBytes / 8,
	}
}

// Validate rejects a zero limit.
func (l TreeLimits) Validate() error {
	if l.MaxDepth == 0 {
		return fmt.Errorf("%w: tree depth limit is zero", ErrInvalidValue)
	}
	if l.MaxChildrenPerNode == 0 {
		return fmt.Errorf("%w: tree child limit is zero", ErrInvalidValue)
	}
	return nil
}

// TreeNode is one schema-validated node of the logical record tree, supplied
// in depth-first preorder. Exactly one value field applies per Kind; FieldTag
// or ElementIndex carries the node's path component — a field tag for
// children of a message node, an element index for children of a list node.
// The registry membership of field tags, type keys and enum values is the
// schema layer's check; this codec verifies structure and bounds.
type TreeNode struct {
	FieldTag     uint32
	HasFieldTag  bool
	ElementIndex uint64
	IsElement    bool
	Kind         ValueKind
	// TypeKey is the registry type key of a message node.
	TypeKey uint32
	// ChildCount is the direct child count of a message node or the
	// element count of a list node; it must be zero for leaves.
	ChildCount uint64
	Bool       bool
	Uint32     uint32
	Uint64     uint64
	Enum       uint32
	// Value is the content of a bytes or UTF-8 node; it may be empty and
	// may be arbitrarily large — large values split into blob chunks.
	Value []byte
}

// nodeCheck is the walker-facing projection of one node: the placement the
// encoder derived or the decoder read, plus the container facts.
type nodeCheck struct {
	nodeID        uint64
	hasParent     bool
	parentID      uint64
	hasFieldTag   bool
	fieldTag      uint32
	isElement     bool
	elementIndex  uint64
	isContainer   bool
	isMessage     bool
	containerKind ValueKind // ValueMessage or ValueList when isContainer
	childCount    uint64
}

// walkFrame is the open state of one container node on the walker stack.
type walkFrame struct {
	nodeID             uint64
	isMessage          bool
	remaining          uint64
	lastFieldTag       uint32
	expectedElementIdx uint64
}

// preorderWalker validates the preorder stream and derives each node's
// parent from the open containers.
type preorderWalker struct {
	limits                TreeLimits
	stack                 []walkFrame
	nextNodeID            uint64
	depth                 uint64
	requireDeclaredParent bool
}

// next validates one node placement, consumes one child slot of the open
// parent and returns the computed parent id. When w.requireDeclaredParent
// is set (the read direction), a declared parent that differs from the
// computed parent, or a missing declaration on a non-root node, is a named
// error.
func (w *preorderWalker) next(n *nodeCheck) (uint64, error) {
	if n.nodeID != w.nextNodeID {
		return 0, fmt.Errorf("%w: node id %d breaks continuity, want %d",
			ErrTree, n.nodeID, w.nextNodeID)
	}
	// Close every container whose child slots are exhausted.
	for len(w.stack) > 0 && w.stack[len(w.stack)-1].remaining == 0 {
		w.stack = w.stack[:len(w.stack)-1]
		w.depth--
	}
	nodeDepth := uint64(len(w.stack)) + 1
	if nodeDepth > w.limits.MaxDepth {
		return 0, fmt.Errorf("%w: depth %d at node %d exceeds the limit %d",
			ErrTree, nodeDepth, n.nodeID, w.limits.MaxDepth)
	}
	if len(w.stack) == 0 {
		// The only node without a parent is the root message.
		if n.nodeID != 0 {
			return 0, fmt.Errorf("%w: node %d has no open parent", ErrTree, n.nodeID)
		}
		if n.hasParent {
			return 0, fmt.Errorf("%w: root node carries a parent id", ErrTree)
		}
		if n.hasFieldTag || n.isElement {
			return 0, fmt.Errorf("%w: root node carries a field tag or element index", ErrTree)
		}
		if !n.isContainer || !n.isMessage {
			return 0, fmt.Errorf("%w: root node is not a message", ErrTree)
		}
	} else {
		parent := &w.stack[len(w.stack)-1]
		if w.requireDeclaredParent {
			if !n.hasParent {
				return 0, fmt.Errorf("%w: node %d lacks a parent id", ErrTree, n.nodeID)
			}
			if n.parentID != parent.nodeID {
				return 0, fmt.Errorf("%w: node %d declares parent %d, want %d",
					ErrTree, n.nodeID, n.parentID, parent.nodeID)
			}
		}
		if n.parentID >= n.nodeID {
			return 0, fmt.Errorf("%w: node %d parent %d is not before the child",
				ErrTree, n.nodeID, n.parentID)
		}
		if parent.isMessage {
			if n.isElement || n.hasFieldTag == false {
				return 0, fmt.Errorf("%w: message child %d needs a field tag and no element index",
					ErrTree, n.nodeID)
			}
			if n.fieldTag <= parent.lastFieldTag {
				return 0, fmt.Errorf("%w: message child %d field tag %d does not ascend above %d",
					ErrTree, n.nodeID, n.fieldTag, parent.lastFieldTag)
			}
			parent.lastFieldTag = n.fieldTag
		} else {
			if n.hasFieldTag || !n.isElement {
				return 0, fmt.Errorf("%w: list child %d needs an element index and no field tag",
					ErrTree, n.nodeID)
			}
			if n.elementIndex != parent.expectedElementIdx {
				return 0, fmt.Errorf("%w: list child %d element index %d, want %d",
					ErrTree, n.nodeID, n.elementIndex, parent.expectedElementIdx)
			}
			parent.expectedElementIdx++
		}
		parent.remaining--
	}
	computedParent := uint64(0)
	if len(w.stack) > 0 {
		computedParent = w.stack[len(w.stack)-1].nodeID
	}
	if n.isContainer {
		if n.childCount > w.limits.MaxChildrenPerNode {
			return 0, fmt.Errorf("%w: node %d declares %d children, limit %d",
				ErrTree, n.nodeID, n.childCount, w.limits.MaxChildrenPerNode)
		}
		w.stack = append(w.stack, walkFrame{nodeID: n.nodeID, isMessage: n.isMessage,
			remaining: n.childCount, expectedElementIdx: 0})
		w.depth++
	}
	w.nextNodeID++
	return computedParent, nil
}

// done rejects a stream that ends with open containers (declared child
// counts above the actual children). Containers whose slots are exhausted
// close normally.
func (w *preorderWalker) done() error {
	for len(w.stack) > 0 && w.stack[len(w.stack)-1].remaining == 0 {
		w.stack = w.stack[:len(w.stack)-1]
		w.depth--
	}
	if len(w.stack) != 0 {
		return fmt.Errorf("%w: node %d declares %d unmatched children",
			ErrTree, w.stack[len(w.stack)-1].nodeID, w.stack[len(w.stack)-1].remaining)
	}
	return nil
}
