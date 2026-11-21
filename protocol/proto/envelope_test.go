package proto

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// TestEnvelopeValidPerFamily: one interface validates a synthetic payload of
// every registry family and returns READY descriptors computed over the
// payload fragments.
func TestEnvelopeValidPerFamily(t *testing.T) {
	for _, family := range []Family{FamilyTx, FamilyBlock, FamilyBalanceSystem, FamilyBorLegacy} {
		for _, kind := range []Kind{KindApplied, KindRemoved, KindStatus} {
			env := envelopeFixture(t, family, kind)
			event, frags, err := ValidateEnvelope(11, env)
			if err != nil {
				t.Fatalf("%s/%s: %v", family, kind, err)
			}
			if event.Family != family || event.Kind != kind {
				t.Fatalf("%s/%s: descriptor carries %s/%s", family, kind, event.Family, event.Kind)
			}
			if event.Ordinal != 11 || event.EventID != env.EventID {
				t.Fatalf("%s/%s: descriptor identity changed", family, kind)
			}
			// The descriptor matches an independent rebuild over the same
			// fragments: family/kind agreement and digests over the payload
			// canonical encoding.
			direct, directFrags, err := BuildEventDescriptor(11, env.EventID, family, kind,
				env.Payload.Fragments)
			if err != nil {
				t.Fatalf("%s/%s rebuild: %v", family, kind, err)
			}
			if event != direct || len(frags) != len(directFrags) {
				t.Fatalf("%s/%s: envelope descriptors differ from the direct build", family, kind)
			}
			for i := range frags {
				if frags[i] != directFrags[i] {
					t.Fatalf("%s/%s: fragment %d differs", family, kind, i)
				}
			}
			var wantVariant profiles.PayloadVariant = profiles.PayloadVariant(
				uint8(family-1)*3 + uint8(kind))
			if env.Payload.Variant != wantVariant {
				t.Fatalf("%s/%s: variant %d, want %d", family, kind, env.Payload.Variant, wantVariant)
			}
		}
	}
}

func TestEnvelopeVariantMismatch(t *testing.T) {
	env := envelopeFixture(t, FamilyTx, KindApplied)
	env.Payload.Variant = profiles.VariantBlockApplied
	_, _, err := ValidateEnvelope(0, env)
	if !errors.Is(err, ErrVariantMismatch) {
		t.Fatalf("error %v does not carry ErrVariantMismatch", err)
	}
	env.Payload.Variant = profiles.VariantInvalid
	if _, _, err := ValidateEnvelope(0, env); !errors.Is(err, ErrVariantMismatch) {
		t.Fatalf("invalid variant error %v", err)
	}
}

// TestEnvelopeProfileGatesOptionalFamilies: balance.system and
// bor.legacy_receipt events exist only under a profile that declares them
// (MESSAGE_CONTRACT section 7); the closed registry rejects the rest.
func TestEnvelopeProfileGatesOptionalFamilies(t *testing.T) {
	balanceEnv := envelopeFixture(t, FamilyBalanceSystem, KindApplied)
	balanceEnv.Profile = profiles.FamilyEth
	if _, _, err := ValidateEnvelope(0, balanceEnv); !errors.Is(err, ErrClosedRegistry) {
		t.Fatalf("balance.system under the ETH profile error %v", err)
	}
	borEnv := envelopeFixture(t, FamilyBorLegacy, KindApplied)
	borEnv.Profile = profiles.FamilyBsc
	if _, _, err := ValidateEnvelope(0, borEnv); !errors.Is(err, ErrClosedRegistry) {
		t.Fatalf("bor.legacy_receipt under the BSC profile error %v", err)
	}
	// The declared profiles admit them.
	if _, _, err := ValidateEnvelope(0, envelopeFixture(t, FamilyBalanceSystem, KindApplied)); err != nil {
		t.Fatalf("balance.system under BSC: %v", err)
	}
	if _, _, err := ValidateEnvelope(0, envelopeFixture(t, FamilyBorLegacy, KindApplied)); err != nil {
		t.Fatalf("bor.legacy_receipt under Bor: %v", err)
	}
	// An unknown profile family is a closed-registry error.
	unknown := envelopeFixture(t, FamilyTx, KindApplied)
	unknown.Profile = profiles.Family(9)
	if _, _, err := ValidateEnvelope(0, unknown); !errors.Is(err, ErrClosedRegistry) {
		t.Fatalf("unknown profile error %v", err)
	}
}

func TestEnvelopeEventIDMismatch(t *testing.T) {
	env := envelopeFixture(t, FamilyBlock, KindApplied)
	other := env.EventID
	other[0] ^= 0xFF // a different producer
	env.EventID = other
	if _, _, err := ValidateEnvelope(0, env); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("producer mismatch error %v", err)
	}
	env = envelopeFixture(t, FamilyBlock, KindApplied)
	other = env.EventID
	other[16] ^= 0xFF // a different run
	env.EventID = other
	if _, _, err := ValidateEnvelope(0, env); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("run mismatch error %v", err)
	}
	env = envelopeFixture(t, FamilyBlock, KindApplied)
	env.EventID = EventID{}
	if _, _, err := ValidateEnvelope(0, env); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("zero event id error %v", err)
	}
}

func TestEnvelopeSemanticInvalid(t *testing.T) {
	env := envelopeFixture(t, FamilyTx, KindApplied)
	env.Semantic.Revision = 5 // Applied carries revision 0
	_, _, err := ValidateEnvelope(0, env)
	if !errors.Is(err, ErrSemantic) || !errors.Is(err, semantic.ErrInvalid) {
		t.Fatalf("error %v does not carry ErrSemantic and semantic.ErrInvalid", err)
	}
}

func TestEnvelopeFragmentShape(t *testing.T) {
	env := envelopeFixture(t, FamilyTx, KindApplied)
	env.Payload.Fragments = env.Payload.Fragments[:2]
	if _, _, err := ValidateEnvelope(0, env); !errors.Is(err, ErrFragments) {
		t.Fatalf("two fragments error %v", err)
	}
	env = envelopeFixture(t, FamilyTx, KindApplied)
	env.Payload.Fragments[1] = nil
	if _, _, err := ValidateEnvelope(0, env); !errors.Is(err, ErrFragments) {
		t.Fatalf("empty fragment error %v", err)
	}
	env = envelopeFixture(t, FamilyTx, KindApplied)
	env.Payload.Fragments = append(env.Payload.Fragments, make([]byte, FragmentMaxBytes+1))
	env.Payload.Variant = profiles.VariantTransactionApplied
	if _, _, err := ValidateEnvelope(0, env); !errors.Is(err, ErrFragments) {
		t.Fatalf("oversize fragment error %v", err)
	}
}

// TestEnvelopeBlockRecordComposition: the phase-1 block record lines up
// with the semantic descriptor of a block Applied event, and every
// disagreement is a named error.
func TestEnvelopeBlockRecordComposition(t *testing.T) {
	env := envelopeFixture(t, FamilyBlock, KindApplied)
	env.Payload.BlockRecord = blockRecordFixture(t, env)
	if _, _, err := ValidateEnvelope(0, env); err != nil {
		t.Fatalf("valid block record rejected: %v", err)
	}

	mutate := func(f func(*block.BlockRecord)) error {
		t.Helper()
		e := envelopeFixture(t, FamilyBlock, KindApplied)
		record := blockRecordFixture(t, e)
		f(record)
		e.Payload.BlockRecord = record
		_, _, err := ValidateEnvelope(0, e)
		return err
	}
	if err := mutate(func(r *block.BlockRecord) {
		r.Activation.BlockHash[0] ^= 0xFF
		r.Metadata.Hash = r.Activation.BlockHash // stay internally consistent
	}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("block hash mismatch error %v", err)
	}
	if err := mutate(func(r *block.BlockRecord) { r.Activation.ProducerID[0] ^= 0xFF }); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("producer mismatch error %v", err)
	}
	if err := mutate(func(r *block.BlockRecord) { r.Activation.RunID[0] ^= 0xFF }); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("run mismatch error %v", err)
	}
	if err := mutate(func(r *block.BlockRecord) { r.Activation.ActivationID.DecisionSequence++ }); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("decision sequence mismatch error %v", err)
	}
	if err := mutate(func(r *block.BlockRecord) { r.Activation.Network.ChainID[0] ^= 0xFF }); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("network mismatch error %v", err)
	}
	if err := mutate(func(r *block.BlockRecord) { r.Metadata.Hash[0] ^= 0xFF }); !errors.Is(err, ErrPayload) {
		t.Fatalf("structurally invalid record error %v", err)
	}

	// A block record on another family or kind is a conflict, not a
	// reinterpretation.
	txEnv := envelopeFixture(t, FamilyTx, KindApplied)
	txEnv.Payload.BlockRecord = blockRecordFixture(t, txEnv)
	if _, _, err := ValidateEnvelope(0, txEnv); !errors.Is(err, ErrConflict) {
		t.Fatalf("block record on tx error %v", err)
	}
	statusEnv := envelopeFixture(t, FamilyBlock, KindStatus)
	statusEnv.Payload.BlockRecord = blockRecordFixture(t, statusEnv)
	if _, _, err := ValidateEnvelope(0, statusEnv); !errors.Is(err, ErrConflict) {
		t.Fatalf("block record on status error %v", err)
	}
}

// TestEnvelopeBalanceViewComposition: the phase-1 balance view of a tx event
// passes its structural validation through the same seam.
func TestEnvelopeBalanceViewComposition(t *testing.T) {
	env := envelopeFixture(t, FamilyTx, KindApplied)
	env.Payload.BalanceView = &balance.TxBalances{
		Scopes: []balance.RollbackScope{balance.TxBoundaryScope()},
		Frames: []balance.FrameRef{{FrameID: 1, ExecutionOrder: 1}},
	}
	if _, _, err := ValidateEnvelope(0, env); err != nil {
		t.Fatalf("valid balance view rejected: %v", err)
	}

	invalid := envelopeFixture(t, FamilyTx, KindApplied)
	invalid.Payload.BalanceView = &balance.TxBalances{
		Scopes: []balance.RollbackScope{balance.TxBoundaryScope()},
		// no frames: an executed transaction always has its root frame
	}
	_, _, err := ValidateEnvelope(0, invalid)
	if !errors.Is(err, ErrPayload) || !errors.Is(err, balance.ErrNoFrames) {
		t.Fatalf("error %v does not carry ErrPayload and balance.ErrNoFrames", err)
	}

	misplaced := envelopeFixture(t, FamilyBlock, KindApplied)
	misplaced.Payload.BalanceView = &balance.TxBalances{
		Scopes: []balance.RollbackScope{balance.TxBoundaryScope()},
		Frames: []balance.FrameRef{{FrameID: 1, ExecutionOrder: 1}},
	}
	if _, _, err := ValidateEnvelope(0, misplaced); !errors.Is(err, ErrConflict) {
		t.Fatalf("balance view on block error %v", err)
	}
}

// TestEnvelopeNilAndDeterminism: nil is rejected and repeated validation of
// one envelope returns identical descriptors.
func TestEnvelopeNilAndDeterminism(t *testing.T) {
	if _, _, err := ValidateEnvelope(0, nil); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("nil envelope error %v", err)
	}
	env := envelopeFixture(t, FamilyBlock, KindApplied)
	first, firstFrags, err := ValidateEnvelope(4, env)
	if err != nil {
		t.Fatalf("first validation: %v", err)
	}
	for i := 0; i < 3; i++ {
		event, frags, err := ValidateEnvelope(4, env)
		if err != nil {
			t.Fatalf("repeat %d: %v", i, err)
		}
		if event != first || len(frags) != len(firstFrags) {
			t.Fatalf("repeat %d changed the descriptors", i)
		}
		for j := range frags {
			if frags[j] != firstFrags[j] {
				t.Fatalf("repeat %d changed fragment %d", i, j)
			}
		}
	}
}
