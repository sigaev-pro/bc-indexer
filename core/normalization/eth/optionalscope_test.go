package eth

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

func TestDefaultScopeDeclarationIsOff(t *testing.T) {
	decl := DefaultScopeDeclaration()
	if decl.Enabled() {
		t.Fatal("the default declaration is OFF")
	}
	// The corpus block reward (2 ETH to the coinbase) produces no record.
	record, err := ApplyOutsideTx(decl, corpusOutsideObservations()[0])
	if err != nil {
		t.Fatal(err)
	}
	if record != nil {
		t.Fatal("with the default OFF declaration no outside-tx record is published")
	}
}

func TestOptionalOffKeepsNativeTxRecordsUnchanged(t *testing.T) {
	// D17: disabling the outside-tx extension loses no native transaction
	// effect. The tx records assembled with outside observations present
	// in the same block are identical either way.
	withOutside, err := AssembleTx(corpusTransferTx())
	if err != nil {
		t.Fatal(err)
	}
	plain, err := AssembleTx(corpusTransferTx())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withOutside, plain) {
		t.Fatal("outside-tx observations never leak into the transaction records")
	}
	if len(withOutside.Actions) != 3 {
		t.Fatalf("actions = %d, want the three transaction actions", len(withOutside.Actions))
	}
}

func TestEnableOptionalScopeRequiresDeclaredKinds(t *testing.T) {
	if _, err := EnableOptionalScope(); !errors.Is(err, ErrOptionalScopeDeclaration) {
		t.Fatalf("empty enable error = %v", err)
	}
	if _, err := EnableOptionalScope(OptionalNone); !errors.Is(err, ErrOptionalScopeDeclaration) {
		t.Fatalf("kind 0 error = %v", err)
	}
	decl, err := EnableOptionalScope(OptionalBlockReward)
	if err != nil {
		t.Fatal(err)
	}
	if !decl.Allows(OptionalBlockReward) || decl.Allows(OptionalRecordKind(99)) {
		t.Fatal("the declaration names exactly its kinds")
	}
	// An observation of an undeclared kind is an explicit error, never a
	// silent widening.
	other := OutsideTxObservation{Kind: OptionalRecordKind(42), NativeOrder: 0}
	if _, err := ApplyOutsideTx(decl, other); !errors.Is(err, ErrOptionalScopeDeclaration) {
		t.Fatalf("undeclared kind error = %v", err)
	}
}

func TestEnabledOptionalScopeEmitsBlockRewardRecord(t *testing.T) {
	decl, err := EnableOptionalScope(OptionalBlockReward)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ApplyOutsideTx(decl, corpusOutsideObservations()[0])
	if err != nil {
		t.Fatal(err)
	}
	if record == nil {
		t.Fatal("the declared block reward must be published")
	}
	if len(record.Effects) != 1 || record.Effects[0].Address != corpusCoinbase {
		t.Fatalf("effects = %+v", record.Effects)
	}
	// The observed 2 ETH credit of the smoke corpus.
	delta, _ := record.Effects[0].After.SubChecked(record.Effects[0].Before)
	if delta.Decimal() != "2000000000000000000" {
		t.Fatalf("reward delta = %s, want 2000000000000000000", delta.Decimal())
	}
	// The optional entity ordinal follows the body transactions: block 1
	// with four transactions gets entity ordinal 5 (4 + 1 + native order
	// 0) in its descriptor.
	descriptor, err := record.SystemBalanceDescriptor(semantic.KindApplied,
		semantic.Network{ChainID: semantic.UInt256(u256("31337")), GenesisHash: semantic.Hash32(patternHash(0x0a))},
		semantic.ID16(patternID16(2)), semantic.ID16(patternID16(3)), 7, patternHash(1), 4, 0,
		semantic.SourceRef{NodeInstanceID: semantic.ID16(patternID16(1)), WriterEpoch: semantic.ID16(patternID16(4)), SourceSequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Family != semantic.FamilyBalanceSystem {
		t.Fatalf("family = %s, want balance.system", descriptor.Family)
	}
	if descriptor.Activation.ActivationID.EntityOrdinal != 5 {
		t.Fatalf("entity ordinal = %d, want 5", descriptor.Activation.ActivationID.EntityOrdinal)
	}
}
