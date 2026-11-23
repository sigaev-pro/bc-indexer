package bsc

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

func envelopeFor(target *balance.Address20, price uint64, sender balance.Address20) EnvelopeFacts {
	return EnvelopeFacts{
		Position: 0, NativeType: 0, Wire: []byte{0xd0},
		Sender: sender, SenderProvenance: ProvenanceNativeDerived,
		Target: target, TargetProvenance: ProvenanceNativeDerived,
		Value: u(price), Nonce: 0, GasLimit: 21000, EffectiveGasPrice: u(price),
	}
}

// All three native conditions must hold: system recipient, zero effective
// gas price, recovered sender equal to the coinbase.
func TestClassifierRequiresAllThreeConditions(t *testing.T) {
	c := Classifier{SystemContracts: DefaultSystemContracts()}
	valSet := fixtureValSet
	coinbase := fixtureCoinbase
	other := balance.Address20{0x99}
	user := balance.Address20{0x7e}

	// All three hold: ParliaSystem.
	got, err := c.Classify(envelopeFor(&valSet, 0, coinbase), coinbase)
	if err != nil || got != ClassificationParliaSystem {
		t.Fatalf("full criterion = %s err %v", got, err)
	}

	// Zero price and coinbase sender, but an ordinary recipient.
	ordinaryTarget := fixtureTo
	if got, _ = c.Classify(envelopeFor(&ordinaryTarget, 0, coinbase), coinbase); got != ClassificationOrdinary {
		t.Fatalf("ordinary recipient classified %s", got)
	}

	// System recipient and coinbase sender, but a nonzero price.
	if got, _ = c.Classify(envelopeFor(&valSet, 3, coinbase), coinbase); got != ClassificationOrdinary {
		t.Fatalf("priced call classified %s", got)
	}

	// System recipient and zero price, but not the coinbase.
	if got, _ = c.Classify(envelopeFor(&valSet, 0, user), coinbase); got != ClassificationOrdinary {
		t.Fatalf("foreign zero-fee sender classified %s", got)
	}

	// A creation has no target: never a system call regardless of price.
	if got, _ = c.Classify(envelopeFor(nil, 0, coinbase), coinbase); got != ClassificationOrdinary {
		t.Fatalf("creation classified %s", got)
	}

	// A zero-fee coinbase call of an address outside the profile set.
	if _, ok := c.SystemContracts[other]; ok {
		t.Fatal("fixture hygiene: unexpected system contract")
	}
	if got, _ = c.Classify(envelopeFor(&other, 0, coinbase), coinbase); got != ClassificationOrdinary {
		t.Fatalf("unknown recipient classified %s", got)
	}
}

// A sender recovery error propagates: it can be neither Ordinary nor
// ParliaSystem.
func TestClassifierPropagatesRecoveryError(t *testing.T) {
	c := Classifier{SystemContracts: DefaultSystemContracts()}
	valSet := fixtureValSet
	env := envelopeFor(&valSet, 0, fixtureCoinbase)
	env.SenderRecoveryError = true
	if _, err := c.Classify(env, fixtureCoinbase); !errors.Is(err, ErrUnclassified) {
		t.Fatalf("recovery error = %v", err)
	}
}

func TestDefaultSystemContractsMatchRetainedFixture(t *testing.T) {
	set := DefaultSystemContracts()
	if len(set) != 17 {
		t.Fatalf("the retained modern fixture set has 17 contracts, got %d", len(set))
	}
	for _, addr := range []balance.Address20{fixtureValSet,
		balance.MustParseAddress("0x0000000000000000000000000000000000001001"), fixtureReward,
		balance.MustParseAddress("0x0000000000000000000000000000000000002002"), balance.MustParseAddress("0x0000000000000000000000000000000000003000")} {
		if !set[addr] {
			t.Fatalf("expected system contract %x missing", addr)
		}
	}
}
