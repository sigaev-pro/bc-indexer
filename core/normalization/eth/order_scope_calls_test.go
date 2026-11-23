package eth

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

func TestOrderAllocatesFromOne(t *testing.T) {
	o := NewOrder()
	for want := uint64(1); want <= 3; want++ {
		got, err := o.Allocate()
		if err != nil || got != want {
			t.Fatalf("Allocate() = %d, %v; want %d", got, err, want)
		}
	}
	if o.Last() != 3 {
		t.Fatalf("Last() = %d, want 3", o.Last())
	}
	if o.Reuse() != 3 {
		t.Fatalf("Reuse() = %d, want the last allocated 3", o.Reuse())
	}
	if next, _ := func() (uint64, error) { return o.Allocate() }(); next != 4 {
		t.Fatalf("allocation after reuse continued incorrectly: %d", next)
	}
}

func TestOrderOverflowIsExplicit(t *testing.T) {
	o := NewOrder()
	o.next = ^uint64(0)
	if _, err := o.Allocate(); !errors.Is(err, ErrOrderExhausted) {
		t.Fatalf("Allocate() error = %v, want ErrOrderExhausted", err)
	}
}

func TestScopeTreeBoundaryAndPropagation(t *testing.T) {
	b := NewScopeBuilder()
	if got := b.scopes[0].ScopeID; got != 0 {
		t.Fatalf("scope 0 missing: %d", got)
	}
	root, err := b.Enter(0, uint64Ptr(0))
	if err != nil || root != 1 {
		t.Fatalf("root scope = %d, %v; want 1", root, err)
	}
	child, err := b.Enter(root, uint64Ptr(1))
	if err != nil || child != 2 {
		t.Fatalf("child scope = %d, %v; want 2", child, err)
	}
	grandChild, err := b.Enter(child, nil)
	if err != nil || grandChild != 3 {
		t.Fatalf("grandchild scope = %d, %v; want 3", grandChild, err)
	}
	later, err := b.Enter(0, nil)
	if err != nil || later != 4 {
		t.Fatalf("later scope = %d, %v; want 4", later, err)
	}
	// The caught child revert (scope 2) never marks the root or the
	// independent later scope.
	if err := b.MarkRolledBack(child); err != nil {
		t.Fatal(err)
	}
	scopes, err := b.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[uint64]RollbackScope{}
	for _, s := range scopes {
		byID[s.ScopeID] = s
	}
	if byID[0].RolledBack {
		t.Error("scope 0 is never rolled back for an included transaction")
	}
	if !byID[2].RolledBack {
		t.Error("scope 2 must keep its direct rollback")
	}
	if !byID[3].RolledBack {
		t.Error("the grandchild inside the reverted child scope is cancelled with it")
	}
	if byID[1].RolledBack {
		t.Error("the root scope survives the caught child revert")
	}
	if byID[4].RolledBack {
		t.Error("an independent later scope is unaffected by the child revert")
	}
	// A repeated native snapshot after discard is a new creation.
	again, err := b.Enter(0, nil)
	if err != nil || again != 5 {
		t.Fatalf("re-entered scope = %d, %v; want a fresh id 5", again, err)
	}
}

func TestScopeTreeAncestorRollbackPropagates(t *testing.T) {
	b := NewScopeBuilder()
	root, _ := b.Enter(0, uint64Ptr(0))     // 1
	child, _ := b.Enter(root, uint64Ptr(1)) // 2
	_, _ = b.Enter(child, uint64Ptr(1))     // 3: completed successfully
	if err := b.MarkRolledBack(root); err != nil {
		t.Fatal(err)
	}
	scopes, err := b.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	// The successful child and grandchild do not survive the ancestor
	// rollback (D74 rule 4), while scope 0 stays.
	want := map[uint64]bool{0: false, 1: true, 2: true, 3: true}
	for _, s := range scopes {
		if s.RolledBack != want[s.ScopeID] {
			t.Errorf("scope %d rolled_back = %v, want %v", s.ScopeID, s.RolledBack, want[s.ScopeID])
		}
	}
}

func TestScopeTreeRejectsUnknownParentAndBoundaryMark(t *testing.T) {
	b := NewScopeBuilder()
	if _, err := b.Enter(9, nil); !errors.Is(err, ErrScopeUnknown) {
		t.Fatalf("unknown parent error = %v", err)
	}
	if err := b.MarkRolledBack(0); !errors.Is(err, ErrScopeBoundary) {
		t.Fatalf("boundary mark error = %v", err)
	}
	if err := b.MarkRolledBack(7); !errors.Is(err, ErrScopeUnknown) {
		t.Fatalf("unknown mark error = %v", err)
	}
}

func TestBuildCallsAssignsIdsOrdersDepths(t *testing.T) {
	order := NewOrder()
	parent := addrPtr(corpusParent)
	inputs := []FrameInput{
		{Kind: KindCall, Engine: balance.EngineEVM, LocalOutcome: ResultSuccess,
			Caller: &corpusSender, Context: parent, Code: parent, MsgSender: &corpusSender, Target: parent},
		{ParentIndex: 0, Kind: KindDelegateCall, Engine: balance.EngineEVM, LocalOutcome: ResultSuccess,
			Caller: parent, Context: parent, Code: addrPtr(corpusChild), MsgSender: &corpusSender, Target: addrPtr(corpusChild)},
	}
	frames, err := BuildCalls(order, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if frames[0].FrameID != 0 || frames[0].Depth != 0 || frames[0].ParentID.Present() {
		t.Errorf("root frame shape wrong: %+v", frames[0])
	}
	if frames[1].ParentID.MustGet() != 0 || frames[1].Depth != 1 {
		t.Errorf("child frame shape wrong: %+v", frames[1])
	}
	if frames[0].ExecutionOrder != 1 || frames[1].ExecutionOrder != 2 {
		t.Errorf("orders = %d, %d; want 1, 2", frames[0].ExecutionOrder, frames[1].ExecutionOrder)
	}
	// DELEGATECALL keeps the code address and the message sender separate
	// from the context and the caller.
	if *frames[1].CodeAddress != corpusChild || *frames[1].MsgSender != corpusSender || *frames[1].ContextAddress != corpusParent {
		t.Errorf("delegatecall address roles substituted: %+v", frames[1])
	}
}

func TestBuildCallsD81AttemptedVersusCreated(t *testing.T) {
	created := addrPtr(corpusParent)
	// A failed creation keeps the attempted address.
	frames, err := BuildCalls(NewOrder(), []FrameInput{{
		Kind: KindCreate2, Engine: balance.EngineEVM, LocalOutcome: ResultFailure,
		AttemptedCreate: created, ErrorCode: Some("err: insufficient gas for deployment"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if frames[0].AttemptedCreateAddress == nil || *frames[0].AttemptedCreateAddress != corpusParent {
		t.Fatal("the attempted address of a failed creation must survive")
	}
	if frames[0].CreatedAddress != nil {
		t.Fatal("a failed creation has no created address")
	}
	// A created address without local success is rejected.
	if _, err := BuildCalls(NewOrder(), []FrameInput{{
		Kind: KindCreate, Engine: balance.EngineEVM, LocalOutcome: ResultRevert, CreatedCreate: created,
	}}); !errors.Is(err, ErrCreateAddress) {
		t.Fatalf("created without success error = %v", err)
	}
	// A created address on a non-creation kind is rejected.
	if _, err := BuildCalls(NewOrder(), []FrameInput{{
		Kind: KindCall, Engine: balance.EngineEVM, LocalOutcome: ResultSuccess, CreatedCreate: created,
	}}); !errors.Is(err, ErrCreateAddress) {
		t.Fatalf("created on a call error = %v", err)
	}
	// An attempted address after an erroring constructor is kept and does
	// not prove local success (D81); a zero attempted address stays as
	// observed instead of being derived from a returned value.
	zero := addrPtr(Address20{})
	frames, err = BuildCalls(NewOrder(), []FrameInput{{
		Kind: KindCreate, Engine: balance.EngineEVM, LocalOutcome: ResultFailure, AttemptedCreate: zero,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if frames[0].AttemptedCreateAddress == nil || *frames[0].AttemptedCreateAddress != (Address20{}) {
		t.Fatal("the observed zero attempted address is preserved, not dropped or invented")
	}
}

func TestApplyCommitViewAncestorRollbackCancelsLocalSuccess(t *testing.T) {
	order := NewOrder()
	frames, err := BuildCalls(order, []FrameInput{
		{Kind: KindCall, Engine: balance.EngineEVM, LocalOutcome: ResultSuccess, Context: addrPtr(corpusParent)},
		{ParentIndex: 0, Kind: KindCall, Engine: balance.EngineEVM, LocalOutcome: ResultSuccess, Context: addrPtr(corpusChild)},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := NewScopeBuilder()
	root, _ := b.Enter(0, uint64Ptr(0))
	childScope, _ := b.Enter(root, uint64Ptr(1))
	if err := b.MarkRolledBack(root); err != nil {
		t.Fatal(err)
	}
	scopes, _ := b.Finalize()
	committed, err := ApplyCommitView(frames, scopes)
	if err != nil {
		t.Fatal(err)
	}
	if committed[0].EffectsCommitted {
		t.Error("the root frame's local success does not survive its own scope rollback")
	}
	if committed[1].EffectsCommitted {
		t.Error("a successful child does not survive an ancestor rollback")
	}
	if committed[0].Outcome != ResultSuccess || committed[1].Outcome != ResultSuccess {
		t.Error("the commit view must not rewrite the local outcomes")
	}
	// Without any rollback both stay committed.
	b2 := NewScopeBuilder()
	r2, _ := b2.Enter(0, uint64Ptr(0))
	_, _ = b2.Enter(r2, uint64Ptr(1))
	scopes2, _ := b2.Finalize()
	committed2, _ := ApplyCommitView(frames, scopes2)
	if !committed2[0].EffectsCommitted || !committed2[1].EffectsCommitted {
		t.Error("unscoped successful frames stay committed")
	}
	// A caught child revert alone does not cancel the parent.
	b3 := NewScopeBuilder()
	r3, _ := b3.Enter(0, uint64Ptr(0))
	c3, _ := b3.Enter(r3, uint64Ptr(1))
	_ = b3.MarkRolledBack(c3)
	scopes3, _ := b3.Finalize()
	if !RolledBack(scopes3, childScope) {
		t.Fatal("setup error")
	}
	committed3, _ := ApplyCommitView(frames, scopes3)
	if !committed3[0].EffectsCommitted {
		t.Error("a caught child revert must not cancel the parent")
	}
	if committed3[1].EffectsCommitted {
		t.Error("the rolled back child stays uncommitted")
	}
}

func TestValidateCallsRejectsBrokenShapes(t *testing.T) {
	good, err := BuildCalls(NewOrder(), []FrameInput{
		{Kind: KindCall, Engine: balance.EngineEVM, LocalOutcome: ResultSuccess},
		{ParentIndex: 0, Kind: KindCall, Engine: balance.EngineEVM, LocalOutcome: ResultRevert},
	})
	if err != nil {
		t.Fatal(err)
	}
	bad := append([]CallFrame{}, good...)
	bad[1].ExecutionOrder = bad[0].ExecutionOrder
	if err := ValidateCalls(bad); err == nil {
		t.Error("duplicate frame orders must be rejected")
	}
	bad2 := append([]CallFrame{}, good...)
	bad2[0].Depth = 3
	if err := ValidateCalls(bad2); err == nil {
		t.Error("a nonzero root depth must be rejected")
	}
	bad3 := append([]CallFrame{}, good...)
	bad3[1].ParentID = None[uint64]()
	if err := ValidateCalls(bad3); err == nil {
		t.Error("a child without a parent reference must be rejected")
	}
	bad4 := append([]CallFrame{}, good...)
	bad4[0].Outcome = ResultSuccess
	code := "execution reverted"
	bad4[0].ErrorCode = Some(code)
	if err := ValidateCalls(bad4); err == nil {
		t.Error("an error code on SUCCESS must be rejected")
	}
	if err := ValidateCalls(nil); err == nil {
		t.Error("an empty call list must be rejected")
	}
}
