// Package semantic is the manifest semantic identity of E02-01
// .semantic_descriptors: the SemanticDescriptor of one public event and the
// decision-to-batch allocation of a native decision. The semantic manifest
// fixes the event kind, the source provenance and sequence, the activation
// refs and the revisions before DELIVERY_STAGING; delivery attempts never
// change them (CONTROL_WIRE_CONTRACT, "Base encodings and identities"; D79).
// The design and the digest formulas are in README.md. The package imports
// only the Go standard library.
package semantic

import (
	"errors"
	"fmt"
)

// ErrInvalid rejects a malformed descriptor or allocation argument.
var ErrInvalid = errors.New("invalid semantic argument")

// ErrReallocation reports a second, different batch for a decision that
// already has one. One native decision keeps the first batch allocated to
// it; a retry restores the same binding (D79).
var ErrReallocation = errors.New("decision already has another batch")

// ErrIdentity reports a batch whose producer or run does not match the
// allocation that names it.
var ErrIdentity = errors.New("batch identity does not match the decision")

// ErrUnallocated reports a decision that has no batch yet.
var ErrUnallocated = errors.New("decision has no batch")

// Family is the event family of the family registry v1 (family tx=1,
// block=2, balance.system=3, bor.legacy_receipt=4; the last two optional).
type Family uint16

// Event families of registry v1.
const (
	FamilyTx            Family = 1
	FamilyBlock         Family = 2
	FamilyBalanceSystem Family = 3
	FamilyBorLegacy     Family = 4
)

// Valid reports whether f is a registered family value.
func (f Family) Valid() bool {
	switch f {
	case FamilyTx, FamilyBlock, FamilyBalanceSystem, FamilyBorLegacy:
		return true
	}
	return false
}

// String returns the registry name of f.
func (f Family) String() string {
	switch f {
	case FamilyTx:
		return "tx"
	case FamilyBlock:
		return "block"
	case FamilyBalanceSystem:
		return "balance.system"
	case FamilyBorLegacy:
		return "bor.legacy_receipt"
	}
	return fmt.Sprintf("Family(%d)", uint16(f))
}

// Kind is the lifecycle event kind of registry v1 (applied=1, removed=2,
// status=3). Unknown values are errors; there is no OTHER.
type Kind uint16

// Lifecycle event kinds of registry v1.
const (
	KindApplied Kind = 1
	KindRemoved Kind = 2
	KindStatus  Kind = 3
)

// Valid reports whether k is a registered kind value.
func (k Kind) Valid() bool {
	switch k {
	case KindApplied, KindRemoved, KindStatus:
		return true
	}
	return false
}

// String returns the registry name of k.
func (k Kind) String() string {
	switch k {
	case KindApplied:
		return "applied"
	case KindRemoved:
		return "removed"
	case KindStatus:
		return "status"
	}
	return fmt.Sprintf("Kind(%d)", uint16(k))
}

// NetworkFamilyEVM is the only network family of the current contract; the
// network digest formulas encode it as this byte.
const NetworkFamilyEVM byte = 0x01

// ID16 is a 16-byte identity (producer, run, node instance, writer epoch,
// batch issuer). New independent identities come from a cryptographic RNG;
// the zero value is unset and invalid. The text form is 32 lowercase hex
// characters without a 0x prefix.
type ID16 [16]byte

// IsZero reports whether id is the unset zero identity.
func (id ID16) IsZero() bool { return id == ID16{} }

// String returns the 32 lowercase hex characters of id.
func (id ID16) String() string { return hexString(id[:]) }

// Hash32 is a 32-byte hash. The text form is 64 lowercase hex characters
// without a 0x prefix.
type Hash32 [32]byte

// String returns the 64 lowercase hex characters of h.
func (h Hash32) String() string { return hexString(h[:]) }

// UInt256 is an unsigned 256-bit integer in 32 big-endian bytes.
type UInt256 [32]byte

// Network is the network identity of the observation: family EVM with the
// chain ID and the genesis hash. It qualifies every ActivationRef and every
// descriptor digest.
type Network struct {
	ChainID     UInt256
	GenesisHash Hash32
}

// SourceRef is the source provenance of one descriptor: source={node_
// instance_id, writer_epoch, source_sequence} of the logical envelope. The
// source_sequence is a separate durable counter of semantic descriptors; it
// is neither the event_sequence nor a block number.
type SourceRef struct {
	NodeInstanceID ID16
	WriterEpoch    ID16
	SourceSequence uint64
}

// ActivationID is the activation identity of one entity inside one native
// decision: the native decision sequence and the entity ordinal. The block
// entity of a decision gets ordinal 0, transaction entities get their body
// index plus one, and optional entities follow after the native body count
// in native order. A new application of a branch has a new decision
// sequence, so two activations of one branch never share an ActivationID.
type ActivationID struct {
	DecisionSequence uint64
	EntityOrdinal    uint64
}

// TransactionRef binds a transaction descriptor to its native inclusion:
// the native body position of the block and the native transaction hash.
type TransactionRef struct {
	NativePosition  uint64
	TransactionHash Hash32
}

// ActivationRef is the activation identity of one descriptor, always
// qualified by the producer and the run that observed it: the
// BlockActivationRef of a block event, or the ActivationRef of a
// transaction event with its inclusion binding.
type ActivationRef struct {
	ProducerID   ID16
	RunID        ID16
	ActivationID ActivationID
	// BlockHash is the block entity of the activation.
	BlockHash Hash32
	// Transaction is the inclusion binding of a transaction descriptor. It
	// is nil for the block entity and for optional entities.
	Transaction *TransactionRef
}

// SemanticDescriptor is the manifest semantic identity of one public event:
// its family and kind, its network, its activation ref, its lifecycle
// revision and its source provenance. The semantic manifest fixes exactly
// these fields before DELIVERY_STAGING. A delivery attempt changes none of
// them; before READY a retry may change only public event IDs and the
// envelope attempt, which this type does not carry.
type SemanticDescriptor struct {
	Family     Family
	Kind       Kind
	Network    Network
	Activation ActivationRef
	// Revision is the lifecycle revision of the activation: Applied carries
	// 0, every StatusChanged carries a positive revision.
	Revision uint64
	Source   SourceRef
}

// entity codes of the canonical encoding.
const (
	entityBlock       byte = 0
	entityTransaction byte = 1
	entityOptional    byte = 2
)

// NewBlockDescriptor returns the block descriptor of the block entity of a
// native decision: family block, entity ordinal 0. See NewDescriptor for
// the shared validation.
func NewBlockDescriptor(kind Kind, net Network, producer, run ID16, decisionSequence uint64,
	block Hash32, revision uint64, source SourceRef) (SemanticDescriptor, error) {
	d := SemanticDescriptor{Family: FamilyBlock, Kind: kind, Network: net,
		Activation: ActivationRef{ProducerID: producer, RunID: run,
			ActivationID: ActivationID{DecisionSequence: decisionSequence, EntityOrdinal: 0},
			BlockHash:    block},
		Revision: revision, Source: source}
	if err := d.Validate(); err != nil {
		return SemanticDescriptor{}, err
	}
	return d, nil
}

// NewTransactionDescriptor returns the transaction descriptor of one body
// transaction of a native decision: family tx, entity ordinal body index
// plus one. bodyIndex is the native body position. See NewDescriptor for
// the shared validation.
func NewTransactionDescriptor(kind Kind, net Network, producer, run ID16, decisionSequence uint64,
	block, tx Hash32, bodyIndex uint64, revision uint64, source SourceRef) (SemanticDescriptor, error) {
	ordinal, err := addUint64(bodyIndex, 1)
	if err != nil {
		return SemanticDescriptor{}, err
	}
	d := SemanticDescriptor{Family: FamilyTx, Kind: kind, Network: net,
		Activation: ActivationRef{ProducerID: producer, RunID: run,
			ActivationID: ActivationID{DecisionSequence: decisionSequence, EntityOrdinal: ordinal},
			BlockHash:    block,
			Transaction:  &TransactionRef{NativePosition: bodyIndex, TransactionHash: tx}},
		Revision: revision, Source: source}
	if err := d.Validate(); err != nil {
		return SemanticDescriptor{}, err
	}
	return d, nil
}

// NewOptionalDescriptor returns the descriptor of an optional entity that
// follows the native body transactions in native order: family
// balance.system or bor.legacy_receipt, entity ordinal bodyCount plus one
// plus nativeOrder. See NewDescriptor for the shared validation.
func NewOptionalDescriptor(family Family, kind Kind, net Network, producer, run ID16,
	decisionSequence uint64, block Hash32, bodyCount, nativeOrder uint64,
	revision uint64, source SourceRef) (SemanticDescriptor, error) {
	if family != FamilyBalanceSystem && family != FamilyBorLegacy {
		return SemanticDescriptor{}, fmt.Errorf("%w: optional entity of family %s", ErrInvalid, family)
	}
	ordinal, err := addUint64(bodyCount, 1)
	if err != nil {
		return SemanticDescriptor{}, err
	}
	ordinal, err = addUint64(ordinal, nativeOrder)
	if err != nil {
		return SemanticDescriptor{}, err
	}
	d := SemanticDescriptor{Family: family, Kind: kind, Network: net,
		Activation: ActivationRef{ProducerID: producer, RunID: run,
			ActivationID: ActivationID{DecisionSequence: decisionSequence, EntityOrdinal: ordinal},
			BlockHash:    block},
		Revision: revision, Source: source}
	if err := d.Validate(); err != nil {
		return SemanticDescriptor{}, err
	}
	return d, nil
}

// Validate checks the descriptor against the identity contract. It reports
// ErrInvalid with the offending field.
func (d SemanticDescriptor) Validate() error {
	if !d.Family.Valid() {
		return fmt.Errorf("%w: family %d", ErrInvalid, uint16(d.Family))
	}
	if !d.Kind.Valid() {
		return fmt.Errorf("%w: kind %d", ErrInvalid, uint16(d.Kind))
	}
	if d.Activation.ProducerID.IsZero() {
		return fmt.Errorf("%w: unset producer", ErrInvalid)
	}
	if d.Activation.RunID.IsZero() {
		return fmt.Errorf("%w: unset run", ErrInvalid)
	}
	entity := entityOf(d)
	switch d.Family {
	case FamilyTx:
		if d.Activation.Transaction == nil || entity != entityTransaction {
			return fmt.Errorf("%w: tx family without an inclusion binding", ErrInvalid)
		}
		if d.Activation.ActivationID.EntityOrdinal == 0 {
			return fmt.Errorf("%w: transaction entity ordinal 0 belongs to the block", ErrInvalid)
		}
	case FamilyBlock:
		if d.Activation.Transaction != nil || entity != entityBlock {
			return fmt.Errorf("%w: block family with an inclusion binding", ErrInvalid)
		}
		if d.Activation.ActivationID.EntityOrdinal != 0 {
			return fmt.Errorf("%w: block entity ordinal %d", ErrInvalid, d.Activation.ActivationID.EntityOrdinal)
		}
	default:
		if d.Activation.Transaction != nil || entity != entityOptional {
			return fmt.Errorf("%w: optional family with an inclusion binding", ErrInvalid)
		}
		if d.Activation.ActivationID.EntityOrdinal == 0 {
			return fmt.Errorf("%w: optional entity ordinal 0 belongs to the block", ErrInvalid)
		}
	}
	switch d.Kind {
	case KindApplied:
		if d.Revision != 0 {
			return fmt.Errorf("%w: Applied revision %d, not 0", ErrInvalid, d.Revision)
		}
	case KindStatus:
		if d.Revision == 0 {
			return fmt.Errorf("%w: StatusChanged revision 0", ErrInvalid)
		}
	case KindRemoved:
		// The contract fixes the revision of Applied and StatusChanged
		// events; it does not state a revision rule for Removed. See the
		// open question in README.md.
	}
	if d.Source.NodeInstanceID.IsZero() {
		return fmt.Errorf("%w: unset node instance", ErrInvalid)
	}
	if d.Source.WriterEpoch.IsZero() {
		return fmt.Errorf("%w: unset writer epoch", ErrInvalid)
	}
	if d.Source.SourceSequence == 0 {
		return fmt.Errorf("%w: source sequence 0", ErrInvalid)
	}
	return nil
}

// entityOf returns the entity code of the descriptor encoding.
func entityOf(d SemanticDescriptor) byte {
	if d.Activation.Transaction == nil {
		if d.Family == FamilyBlock {
			return entityBlock
		}
		return entityOptional
	}
	return entityTransaction
}

// addUint64 adds two unsigned values and reports ErrInvalid on overflow.
// Counters are checked: overflow is an explicit error, never a wrap.
func addUint64(a, b uint64) (uint64, error) {
	sum := a + b
	if sum < a {
		return 0, fmt.Errorf("%w: counter overflow at %d + %d", ErrInvalid, a, b)
	}
	return sum, nil
}
