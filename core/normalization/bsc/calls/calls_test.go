package calls

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

var (
	caller    = balance.Address20{0x11}
	target    = balance.Address20{0x22}
	childAddr = balance.Address20{0x33}
	attempt   = balance.Address20{0x44}
)

func rootFrame() Frame {
	callerCopy := caller
	return Frame{
		FrameID: 0, ExecutionOrder: 2, Depth: 0,
		Engine: balance.EngineEVM, Kind: KindCall,
		CallerAddress: &callerCopy, Target: &target,
		Outcome: OutcomeSuccess, EffectsCommitted: true,
	}
}

func validTree() Tree {
	parent := uint64(0)
	grandParent := uint64(1)
	return Tree{Frames: []Frame{
		rootFrame(),
		{
			FrameID: 1, ParentID: &parent, ExecutionOrder: 4, Depth: 1,
			Engine: balance.EngineEVM, Kind: KindCreate,
			CallerAddress:          &caller,
			Outcome:                OutcomeFailure,
			AttemptedCreateAddress: &attempt,
		},
		{
			FrameID: 2, ParentID: &grandParent, ExecutionOrder: 5, Depth: 2,
			Engine: balance.EngineEVM, Kind: KindCall,
			CallerAddress: &caller, Target: &childAddr,
			Outcome: OutcomeSuccess,
		},
	}}
}

func TestValidTreePasses(t *testing.T) {
	if err := validTree().Validate(); err != nil {
		t.Fatalf("valid tree rejected: %v", err)
	}
	refs := validTree().Refs()
	if len(refs) != 3 || refs[0].FrameID != 0 || refs[2].ExecutionOrder != 5 {
		t.Fatalf("unexpected refs view: %+v", refs)
	}
}

func TestRootFrameRules(t *testing.T) {
	noRoot := validTree()
	noRoot.Frames = noRoot.Frames[1:]
	if !errors.Is(noRoot.Validate(), ErrNoRootFrame) {
		t.Fatalf("missing root error = %v", noRoot.Validate())
	}

	parented := validTree()
	p := uint64(1)
	parented.Frames[0].ParentID = &p
	if !errors.Is(parented.Validate(), ErrRootParent) {
		t.Fatalf("root parent error = %v", parented.Validate())
	}

	deep := validTree()
	deep.Frames[0].Depth = 3
	if !errors.Is(deep.Validate(), ErrRootDepth) {
		t.Fatalf("root depth error = %v", deep.Validate())
	}
}

func TestParentOrderAndDepth(t *testing.T) {
	tree := validTree()
	tree.Frames[1].ExecutionOrder = 1 // child entered before its parent
	if !errors.Is(tree.Validate(), ErrParentOrder) {
		t.Fatalf("parent order error = %v", tree.Validate())
	}

	tree = validTree()
	tree.Frames[2].Depth = 5
	if !errors.Is(tree.Validate(), ErrDepthMismatch) {
		t.Fatalf("depth error = %v", tree.Validate())
	}

	tree = validTree()
	missing := uint64(9)
	tree.Frames[2].ParentID = &missing
	if !errors.Is(tree.Validate(), ErrParentUnknown) {
		t.Fatalf("unknown parent error = %v", tree.Validate())
	}
}

func TestOrderRules(t *testing.T) {
	tree := validTree()
	tree.Frames[0].ExecutionOrder = 0
	if !errors.Is(tree.Validate(), ErrOrderZero) {
		t.Fatalf("zero order error = %v", tree.Validate())
	}

	tree = validTree()
	tree.Frames[1].ExecutionOrder = 2 // duplicates the root entry order
	if !errors.Is(tree.Validate(), ErrOrderDuplicate) {
		t.Fatalf("duplicate order error = %v", tree.Validate())
	}

	tree = validTree()
	tree.Frames[1], tree.Frames[2] = tree.Frames[2], tree.Frames[1]
	if !errors.Is(tree.Validate(), ErrListUnsorted) {
		t.Fatalf("unsorted error = %v", tree.Validate())
	}
}

// D81: an attempted creation address stays present after a failed attempt;
// a created address exists only on a locally successful creation, and it
// never appears without its attempt.
func TestD81CreationAddressRules(t *testing.T) {
	tree := validTree()
	if tree.Frames[1].AttemptedCreateAddress == nil || tree.Frames[1].CreatedAddress != nil {
		t.Fatal("fixture: failed creation keeps the attempt and no created address")
	}

	created := validTree()
	createdAddr := attempt
	created.Frames[1].Outcome = OutcomeSuccess
	created.Frames[1].CreatedAddress = &createdAddr
	if err := created.Validate(); err != nil {
		t.Fatalf("successful creation rejected: %v", err)
	}

	noAttempt := created
	noAttempt.Frames[1].AttemptedCreateAddress = nil
	if !errors.Is(noAttempt.Validate(), ErrCreatedWithoutAttempt) {
		t.Fatalf("created without attempt error = %v", noAttempt.Validate())
	}

	failedWithCreated := validTree()
	failedAddr := attempt
	failedWithCreated.Frames[1].CreatedAddress = &failedAddr
	if !errors.Is(failedWithCreated.Validate(), ErrCreatedNotSuccess) {
		t.Fatalf("created on failure error = %v", failedWithCreated.Validate())
	}
}

func TestCommittedRequiresLocalSuccess(t *testing.T) {
	tree := validTree()
	tree.Frames[2].EffectsCommitted = true
	tree.Frames[2].Outcome = OutcomeRevert
	if !errors.Is(tree.Validate(), ErrCommittedNonSuccess) {
		t.Fatalf("committed revert error = %v", tree.Validate())
	}
}

func TestEngineKindPairing(t *testing.T) {
	tree := validTree()
	tree.Frames[0].Engine = balance.EngineNativeProtocol
	if !errors.Is(tree.Validate(), ErrNativeProtocolKind) {
		t.Fatalf("native protocol engine with call kind error = %v", tree.Validate())
	}

	tree = validTree()
	tree.Frames[0].Kind = KindNativeProtocol
	if !errors.Is(tree.Validate(), ErrEvmKind) {
		t.Fatalf("evm engine with native protocol kind error = %v", tree.Validate())
	}

	tree = validTree()
	native := rootFrame()
	native.FrameID = 3
	parent := uint64(0)
	native.ParentID = &parent
	native.ExecutionOrder = 6
	native.Depth = 1
	native.Kind = KindNativeProtocol
	native.Engine = balance.EngineNativeProtocol
	tree.Frames = append(tree.Frames, native)
	if err := tree.Validate(); err != nil {
		t.Fatalf("parlia system frame rejected: %v", err)
	}
}

func TestTargetPresenceRules(t *testing.T) {
	tree := validTree()
	creationTarget := target
	tree.Frames[1].Target = &creationTarget
	if !errors.Is(tree.Validate(), ErrCreateTarget) {
		t.Fatalf("creation with target error = %v", tree.Validate())
	}

	tree = validTree()
	tree.Frames[0].Target = nil
	if !errors.Is(tree.Validate(), ErrCallTargetMissing) {
		t.Fatalf("call without target error = %v", tree.Validate())
	}

	tree = validTree()
	tree.Frames[0].CallerAddress = nil
	if !errors.Is(tree.Validate(), ErrCallerMissing) {
		t.Fatalf("missing caller error = %v", tree.Validate())
	}
}
