package eth

import (
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// Optional scope errors.
var (
	// ErrOptionalScopeDeclaration reports an invalid scope declaration: a
	// declaration that enables nothing, or one that names a record kind
	// this module does not know or did not declare.
	ErrOptionalScopeDeclaration = errors.New("eth: invalid optional scope declaration")
)

// OptionalRecordKind names one typed outside-tx record shape. The v1
// registry for the ETH family has exactly one kind: the block reward
// credit of the smoke corpus fixture (2 ETH to the coinbase per block,
// test/native_corpus/expected/eth.json, "outside_transaction" rows). New
// kinds are a versioned registry change, never an OTHER fallback.
type OptionalRecordKind uint8

// Optional record kinds.
const (
	// OptionalNone is the invalid zero value.
	OptionalNone OptionalRecordKind = iota
	// OptionalBlockReward is the outside-tx block reward credit.
	OptionalBlockReward
)

// String returns the registry name of the kind.
func (k OptionalRecordKind) String() string {
	switch k {
	case OptionalBlockReward:
		return "balance.system.block-reward"
	}
	return "OPTIONAL_INVALID"
}

// ScopeDeclaration is the D17 scope declaration of the outside-tx optional
// extension. The default is OFF: outside-tx observations (block rewards,
// withdrawals and other credits without a transaction) produce no public
// records, and disabling them never loses a native transaction effect —
// the mandatory tx records stay complete and byte-identical either way.
//
// Enabling requires an explicit declaration naming the intended typed
// record kinds. There is no enable-by-accident: an empty declaration is
// invalid, and an observation of a kind the declaration does not name is
// rejected, not silently widened.
type ScopeDeclaration struct {
	enabled bool
	kinds   map[OptionalRecordKind]bool
}

// DefaultScopeDeclaration returns the OFF declaration.
func DefaultScopeDeclaration() ScopeDeclaration {
	return ScopeDeclaration{enabled: false, kinds: map[OptionalRecordKind]bool{}}
}

// EnableOptionalScope builds an ON declaration. kinds must name at least
// one registered kind; duplicates collapse.
func EnableOptionalScope(kinds ...OptionalRecordKind) (ScopeDeclaration, error) {
	if len(kinds) == 0 {
		return ScopeDeclaration{}, fmt.Errorf("%w: enabling requires declared kinds", ErrOptionalScopeDeclaration)
	}
	set := make(map[OptionalRecordKind]bool, len(kinds))
	for _, k := range kinds {
		if k == OptionalNone {
			return ScopeDeclaration{}, fmt.Errorf("%w: kind 0 is INVALID", ErrOptionalScopeDeclaration)
		}
		set[k] = true
	}
	return ScopeDeclaration{enabled: true, kinds: set}, nil
}

// Enabled reports whether the optional scope is ON.
func (d ScopeDeclaration) Enabled() bool { return d.enabled }

// Allows reports whether the declaration names one kind.
func (d ScopeDeclaration) Allows(k OptionalRecordKind) bool {
	return d.enabled && d.kinds[k]
}

// SystemBalanceRecord is the publication unit of one outside-tx native
// operation (BALANCE_MODEL: "Её предлагаемая единица публикации — отдельный
// SystemBalanceRecord"): the declared kind, the observed native effects
// and its own entity ordinal after the body transactions. It never
// invents a transaction hash; the closed v1 TransactionProtocol registry
// has no ETH outside-tx operation, so the record carries the observed
// effect points as-is instead of a fabricated BalanceAction cause.
type SystemBalanceRecord struct {
	Kind OptionalRecordKind
	// Effects are the observed balance points of the operation.
	Effects []SystemBalanceEffect
	// NativeOrder is the operation's position among the block's optional
	// entities, from 0; the descriptor entity ordinal is body count plus
	// one plus this value.
	NativeOrder uint64
}

// SystemBalanceEffect is one observed address effect of an outside-tx
// operation.
type SystemBalanceEffect struct {
	Address Address20
	Before  UInt256
	After   UInt256
}

// OutsideTxObservation is one observed outside-tx operation offered to the
// public output.
type OutsideTxObservation struct {
	Kind    OptionalRecordKind
	Effects []SystemBalanceEffect
	// NativeOrder is the operation position among the block's optional
	// entities.
	NativeOrder uint64
}

// ApplyOutsideTx applies D17 to one outside-tx observation:
//
//   - with the default OFF declaration the observation produces no record
//     (nil, nil): optional data never blocks or alters the mandatory path;
//   - with an ON declaration that names the kind, it produces the typed
//     SystemBalanceRecord;
//   - with an ON declaration that does not name the kind, it is an
//     explicit error: widening the declaration is a decision, not a
//     default.
func ApplyOutsideTx(decl ScopeDeclaration, obs OutsideTxObservation) (*SystemBalanceRecord, error) {
	if !decl.enabled {
		return nil, nil
	}
	if !decl.Allows(obs.Kind) {
		return nil, fmt.Errorf("%w: kind %s is not declared", ErrOptionalScopeDeclaration, obs.Kind)
	}
	return &SystemBalanceRecord{
		Kind:        obs.Kind,
		Effects:     obs.Effects,
		NativeOrder: obs.NativeOrder,
	}, nil
}

// SystemBalanceDescriptor builds the manifest semantic descriptor of one
// optional entity: family balance.system, entity ordinal body count plus
// one plus the native order.
func (s *SystemBalanceRecord) SystemBalanceDescriptor(kind semantic.Kind, net semantic.Network, producer, run semantic.ID16,
	decisionSequence uint64, block Hash32, bodyCount uint64, revision uint64, source semantic.SourceRef) (semantic.SemanticDescriptor, error) {
	return semantic.NewOptionalDescriptor(semantic.FamilyBalanceSystem, kind, net, producer, run,
		decisionSequence, semantic.Hash32(block), bodyCount, s.NativeOrder, revision, source)
}
