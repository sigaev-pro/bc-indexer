// Package optionalscope is the optional-scope declaration area of the BSC
// normalization module (E04-02.optional_scope).
//
// The default is OFF: the published record carries exactly the native
// transaction effects, and no outside-transaction observation is published
// as a system record. Turning the scope on requires a declared typed
// fixture; an undeclared outside-tx record is never published. The BSC
// outside-tx operation of the current profiles is the Parlia fee sweep:
// immediately before the deposit system transaction's boundary, native
// Parlia moves the accumulated fee balance of the system address to the
// block validator, outside every transaction. The sweep is real native
// behavior, which is why it is declared here instead of being silently
// dropped or silently published.
//
// Normative anchors:
//
//   - .project/docs/TASK_DECOMPOSITION.md, E04-02.optional_scope: default
//     OFF without losing native tx effects; the enabled outside-tx branch
//     exists only with declared typed fixtures
//   - .project/docs/MESSAGE_CONTRACT.md, section 7: optional families are
//     allowed only with the declared profile feature
//   - test/type_paths/bsc/full_import/ledger.py: the sweep happens
//     immediately before the deposit transaction's OnTxStart boundary,
//     outside that transaction
package optionalscope

import (
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// Sentinel errors of the declaration.
var ErrUndeclaredFixture = errors.New("bsc/optionalscope: an outside-tx record requires a declared typed fixture")

// Feature names the one outside-tx operation of the current BSC profiles.
type Feature uint8

// Features. Zero is not a valid feature.
const (
	// FeatureParliaFeeSweep is the Parlia SystemAddress-to-validator fee
	// sweep before a deposit system transaction.
	FeatureParliaFeeSweep Feature = iota + 1
)

var featureNames = [...]string{"PARLIA_FEE_SWEEP"}

// String returns the registry name of f.
func (f Feature) String() string {
	if f == 0 || int(f) > len(featureNames) {
		return fmt.Sprintf("FEATURE_INVALID(%d)", uint8(f))
	}
	return featureNames[f-1]
}

// Declaration is the outside-tx scope declaration of one BSC profile: the
// enabled flag and the declared typed fixtures that may publish
// outside-tx records when it is on.
type Declaration struct {
	// Enabled is false by default. Default OFF loses no native transaction
	// effect: transaction records stay complete, and no completeness of
	// the whole native balance history is claimed.
	Enabled bool
	// Fixtures lists the declared typed fixtures allowed to publish
	// outside-tx records while Enabled is true.
	Fixtures []string
}

// Default returns the default declaration: OFF, no fixtures.
func Default() Declaration { return Declaration{} }

// Declares reports whether fixture is declared.
func (d Declaration) Declares(fixture string) bool {
	for _, name := range d.Fixtures {
		if name == fixture {
			return true
		}
	}
	return false
}

// OutsideTxRecord is one outside-tx system record of the enabled scope.
// It never borrows a transaction identity: no transaction hash, no
// inclusion position, no tx execution order. Its order space is its own,
// defined by the declared fixture before the profile becomes supported.
type OutsideTxRecord struct {
	// Feature is the outside-tx operation.
	Feature Feature
	// Fixture is the declared typed fixture that publishes this record.
	Fixture string
	// OrderInBlock is the fixture-defined position of the operation
	// inside its block, in the fixture's own order space. It is never a
	// body position of a transaction.
	OrderInBlock uint64
	// Effects are the actual native balance effects of the operation.
	Effects []balance.BalanceEffect
	// BlockNumber and BlockHash bind the record to its block without
	// inventing a transaction inclusion.
	BlockNumber uint64
	BlockHash   [32]byte
}

// Apply decides one observed outside-tx operation against the
// declaration. With the scope disabled the observation is dropped from
// publication and the transaction records stay exactly as they are; with
// the scope enabled the fixture must be declared, and the record is
// published in the fixture's own order space. A disabled scope never
// returns a record and never reports an error: dropping is the declared
// behavior, not a failure.
func (d Declaration) Apply(feature Feature, fixture string, record OutsideTxRecord) (*OutsideTxRecord, error) {
	if !d.Enabled {
		return nil, nil
	}
	if !d.Declares(fixture) {
		return nil, fmt.Errorf("%w: %s", ErrUndeclaredFixture, fixture)
	}
	record.Feature = feature
	record.Fixture = fixture
	return &record, nil
}
