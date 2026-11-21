package balance

import "testing"

// Cause equality is the identity of the causing native operation boundary
// (task E02-05: "cause equality"). Two actions share an execution_order only
// through the exact ExecutionAction cause, so equality must distinguish
// every registry branch and every reference exactly.
func TestCauseEquality(t *testing.T) {
	frame5 := FrameCause(5)
	if !frame5.Equal(FrameCause(5)) {
		t.Fatal("Frame(5) must equal Frame(5)")
	}
	if frame5.Equal(FrameCause(6)) {
		t.Fatal("Frame(5) must not equal Frame(6)")
	}

	exec7 := ExecutionActionCause(7)
	if !exec7.Equal(ExecutionActionCause(7)) {
		t.Fatal("ExecutionAction(7) must equal ExecutionAction(7)")
	}
	if exec7.Equal(ExecutionActionCause(8)) {
		t.Fatal("ExecutionAction(7) must not equal ExecutionAction(8)")
	}
	if frame5.Equal(exec7) {
		t.Fatal("different cause branches must never be equal")
	}

	purchase0 := ProtocolCause(OpGasPurchase, 0)
	if !purchase0.Equal(ProtocolCause(OpGasPurchase, 0)) {
		t.Fatal("TransactionProtocol{GAS_PURCHASE,0} must equal itself")
	}
	if purchase0.Equal(ProtocolCause(OpGasPurchase, 1)) {
		t.Fatal("different operation_index means a different invocation")
	}
	if purchase0.Equal(ProtocolCause(OpGasReturn, 0)) {
		t.Fatal("different registry operations are different causes")
	}

	burn := ProtocolCause(OpSelfDestructFinalizationBurn, 3, 9, 11)
	if !burn.Equal(ProtocolCause(OpSelfDestructFinalizationBurn, 3, 9, 11)) {
		t.Fatal("related action lists with the same ids must be equal")
	}
	if burn.Equal(ProtocolCause(OpSelfDestructFinalizationBurn, 3, 11, 9)) {
		t.Fatal("related action lists are ordered and must not be reordered")
	}
	if burn.Equal(ProtocolCause(OpSelfDestructFinalizationBurn, 3)) {
		t.Fatal("an absent and a present related action list differ")
	}
	if !ProtocolCause(OpGasPurchase, 0).Equal(ProtocolCause(OpGasPurchase, 0)) {
		t.Fatal("nil and empty related lists are the same absence")
	}

	var unspecified Cause
	if unspecified.Equal(frame5) || frame5.Equal(unspecified) {
		t.Fatal("an unspecified cause equals nothing")
	}
	if unspecified.Equal(Cause{}) {
		t.Fatal("an unspecified cause is not even an identity")
	}
}

func TestCauseStringUsesRegistryNames(t *testing.T) {
	cases := map[string]string{
		FrameCause(3).String():                          "Frame(3)",
		ExecutionActionCause(4).String():                "ExecutionAction(4)",
		ProtocolCause(OpGasPurchase, 0).String():        "TransactionProtocol{GAS_PURCHASE, 0, []}",
		ProtocolCause(OpFeeRecipientCredit, 2).String(): "TransactionProtocol{FEE_RECIPIENT_CREDIT, 2, []}",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("cause string %q != %q", got, want)
		}
	}
	if OpBSCSystemExecution.String() != "BSC_SYSTEM_EXECUTION" {
		t.Fatalf("operation registry name changed: %s", OpBSCSystemExecution)
	}
	if OpBorStateSyncExecution.String() != "BOR_STATE_SYNC_EXECUTION" {
		t.Fatalf("operation registry name changed: %s", OpBorStateSyncExecution)
	}
	if EngineNativeProtocol.String() != "NATIVE_PROTOCOL" {
		t.Fatalf("engine registry name changed: %s", EngineNativeProtocol)
	}
	if KindSelfDestructTransfer.String() != "SELFDESTRUCT_TRANSFER" {
		t.Fatalf("kind registry name changed: %s", KindSelfDestructTransfer)
	}
	if AssetNative.String() != "NATIVE" {
		t.Fatalf("asset registry name changed: %s", AssetNative)
	}
}
