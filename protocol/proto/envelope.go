// The envelope-level validator: the composition seam of the schema layer.
// One call lines up the four phase-1 models behind it — the semantic
// identity (protocol/semantic), the closed payload-variant profile
// (protocol/profiles), the block record view (protocol/block) and the
// balance view (protocol/balance) — validates their agreement with the
// payload fragments and returns the READY descriptors of the event.
//
// MESSAGE_CONTRACT section 2 (D73): the Envelope is a model, not one
// serialized Protobuf message. This layer never assembles envelope bytes;
// the RecordFragment codec of E02-02.codec builds them from the validated
// parts.
package proto

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// Payload is the payload half of one public event at this layer: the closed
// registry variant, the canonical fragment bytes of the record and the
// optional typed views of the phase-1 models the family prescribes. The
// full payload types of every family are E04's scope; E02-02.codec produces
// the real fragment bytes. The typed views are the deepest checks this
// layer can make without them.
type Payload struct {
	// Variant is the payload variant of the closed registry; it must be
	// exactly the family/kind variant of the semantic descriptor.
	Variant profiles.PayloadVariant
	// Fragments are the canonical fragment bytes of the record in index
	// order, the final seal fragment included; record_length,
	// record_digest and the fragment digests are computed over them.
	Fragments [][]byte
	// BlockRecord is the typed block view of a block-family Applied event
	// (E02-08). Present only for family block, kind applied; its
	// activation identity must agree with the semantic descriptor.
	BlockRecord *block.BlockRecord
	// BalanceView is the typed balance view of a tx-family event
	// (E02-05). Present only for family tx; it must pass the structural
	// balance validation.
	BalanceView *balance.TxBalances
}

// Envelope is one public event at the schema seam: its public event id, the
// network profile it is published under, the semantic identity fixed before
// DELIVERY_STAGING and the payload. A delivery attempt changes none of the
// semantic parts.
type Envelope struct {
	EventID EventID
	// Profile is the network profile family (ETH, BSC, Bor) the event is
	// published under; it gates the optional event families through the
	// closed profiles registry.
	Profile  profiles.Family
	Semantic semantic.SemanticDescriptor
	Payload  Payload
}

// ValidateEnvelope composes the four phase-1 models into one verdict and
// the READY descriptors of the event. It checks, in order:
//
//   - the semantic descriptor passes the phase-1 identity validation;
//   - the event id carries the same producer and run as the semantic
//     activation;
//   - family and kind are registry v1 values and the payload variant is
//     exactly their closed family/kind variant, admitted by the network
//     profile (optional families need the declared profile feature);
//   - the fragment bytes satisfy the record shape (header/items/seal, the
//     1..65536 bound) and the typed views agree with the descriptor:
//     a BlockRecord only for a block Applied event, with matching
//     activation identity, and a BalanceView only for a tx event, passing
//     the structural balance validation;
//   - record_length, record_digest, the fragment digests and the fragment
//     manifest digest are computed over the payload fragments and returned
//     as READY descriptors that satisfy ValidateFragments.
//
// Every rejection names its cause. The same envelope always validates to
// the same descriptors.
func ValidateEnvelope(ordinal uint64, env *Envelope) (EventDescriptor, []FragmentDescriptor, error) {
	if env == nil {
		return EventDescriptor{}, nil, fmt.Errorf("%w: envelope is nil", ErrInvalidValue)
	}
	if err := env.Semantic.Validate(); err != nil {
		return EventDescriptor{}, nil, fmt.Errorf("%w: %w", ErrSemantic, err)
	}
	family, err := familyFromSemantic(env.Semantic.Family)
	if err != nil {
		return EventDescriptor{}, nil, err
	}
	kind, err := kindFromSemantic(env.Semantic.Kind)
	if err != nil {
		return EventDescriptor{}, nil, err
	}
	if err := env.EventID.Validate(); err != nil {
		return EventDescriptor{}, nil, err
	}
	if env.EventID.ProducerID() != ID16(env.Semantic.Activation.ProducerID) {
		return EventDescriptor{}, nil, fmt.Errorf("%w: event id producer differs from the semantic activation producer",
			ErrIdentityMismatch)
	}
	if env.EventID.RunID() != ID16(env.Semantic.Activation.RunID) {
		return EventDescriptor{}, nil, fmt.Errorf("%w: event id run differs from the semantic activation run",
			ErrIdentityMismatch)
	}
	if err := validateVariantAgainstProfile(env.Profile, env.Payload.Variant, family, kind); err != nil {
		return EventDescriptor{}, nil, err
	}
	if err := validateTypedViews(env, family, kind); err != nil {
		return EventDescriptor{}, nil, err
	}
	event, frags, err := BuildEventDescriptor(ordinal, env.EventID, family, kind, env.Payload.Fragments)
	if err != nil {
		return EventDescriptor{}, nil, err
	}
	return event, frags, nil
}

// validateVariantAgainstProfile closes the family/kind/variant triangle:
// the variant must be the exact family/kind entry of registry v1 and must
// be admitted by the network profile (MESSAGE_CONTRACT section 7: optional
// families only with the declared profile feature).
func validateVariantAgainstProfile(profileFamily profiles.Family,
	variant profiles.PayloadVariant, family Family, kind Kind) error {
	want, err := variantFor(family, kind)
	if err != nil {
		return err
	}
	if variant != want {
		return fmt.Errorf("%w: variant %d, want %d for family %s kind %s",
			ErrVariantMismatch, uint8(variant), uint8(want), family, kind)
	}
	profile, err := profiles.Default(profileFamily)
	if err != nil {
		return fmt.Errorf("%w: profile: %s", ErrClosedRegistry, err)
	}
	if err := profile.ValidateVariant(variant); err != nil {
		return fmt.Errorf("%w: profile %s: %s", ErrClosedRegistry, profileFamily, err)
	}
	return nil
}

// validateTypedViews lines the optional phase-1 payload views up with the
// semantic descriptor: placement first (a view of another family or kind is
// a named conflict, not a reinterpretation), then identity and structural
// agreement.
func validateTypedViews(env *Envelope, family Family, kind Kind) error {
	if env.Payload.BlockRecord != nil {
		if family != FamilyBlock {
			return fmt.Errorf("%w: block record payload on family %s", ErrConflict, family)
		}
		if kind != KindApplied {
			return fmt.Errorf("%w: block record payload on kind %s, a BlockRecord is the initial applied record",
				ErrConflict, kind)
		}
		if err := validateBlockRecord(env.Payload.BlockRecord, env.Semantic); err != nil {
			return err
		}
	}
	if env.Payload.BalanceView != nil {
		if family != FamilyTx {
			return fmt.Errorf("%w: balance view payload on family %s", ErrConflict, family)
		}
		if err := balance.Validate(*env.Payload.BalanceView); err != nil {
			return fmt.Errorf("%w: balance view: %w", ErrPayload, err)
		}
	}
	return nil
}

// validateBlockRecord checks the phase-1 block record against the semantic
// descriptor: structural validity, then the shared activation identity
// (network, block hash, producer, run, decision sequence and the block
// entity ordinal 0).
func validateBlockRecord(record *block.BlockRecord, descriptor semantic.SemanticDescriptor) error {
	if err := record.Validate(); err != nil {
		return fmt.Errorf("%w: block record: %w", ErrPayload, err)
	}
	activation := record.Activation
	if activation.BlockHash != block.Hash32(descriptor.Activation.BlockHash) {
		return fmt.Errorf("%w: block record hash differs from the descriptor block hash",
			ErrIdentityMismatch)
	}
	if activation.ProducerID != block.Id16(descriptor.Activation.ProducerID) {
		return fmt.Errorf("%w: block record producer differs from the descriptor producer",
			ErrIdentityMismatch)
	}
	if activation.RunID != block.Id16(descriptor.Activation.RunID) {
		return fmt.Errorf("%w: block record run differs from the descriptor run",
			ErrIdentityMismatch)
	}
	if activation.ActivationID.DecisionSequence != descriptor.Activation.ActivationID.DecisionSequence {
		return fmt.Errorf("%w: block record decision sequence %d differs from the descriptor decision sequence %d",
			ErrIdentityMismatch, activation.ActivationID.DecisionSequence,
			descriptor.Activation.ActivationID.DecisionSequence)
	}
	if activation.ActivationID.EntityOrdinal != 0 {
		return fmt.Errorf("%w: block record entity ordinal %d, the block entity is ordinal 0",
			ErrIdentityMismatch, activation.ActivationID.EntityOrdinal)
	}
	var chainID, genesis [32]byte
	copy(chainID[:], activation.Network.ChainID[:])
	copy(genesis[:], activation.Network.GenesisHash[:])
	if chainID != [32]byte(descriptor.Network.ChainID) || genesis != [32]byte(descriptor.Network.GenesisHash) {
		return fmt.Errorf("%w: block record network differs from the descriptor network",
			ErrIdentityMismatch)
	}
	return nil
}
