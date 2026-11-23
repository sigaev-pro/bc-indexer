// Package authorization is the EIP-7702 authorization-tuple sub-package of
// the BSC normalization module (E04-01.authorization).
//
// The current BSC profile is closed and empty: EIP-7702 set-code
// transactions are not active on the current BSC fork schedule (the Jenner
// fork that introduces them stays inactive in the pinned profile), so a
// BSC TransactionRecord never carries authorization tuples. The package
// still exists, with the full source tuple shape, so that a future fork
// activation is a versioned registry change that fills the profile in one
// place instead of an ad-hoc field.
//
// Normative anchors:
//
//   - .project/docs/TASK_DECOMPOSITION.md, E04-01.authorization: original
//     authorization tuples, including invalid and duplicate ones, are kept
//     where the native profile applies them
//   - .project/docs/TRANSACTION_TYPE_REGISTRY.md, "Standard EVM variants":
//     a type in the client source does not prove its activation on the
//     current fork; the fork validator is mandatory
//   - .project/docs/MESSAGE_CONTRACT.md, section 3: fields absent by the
//     native model are never filled with invented zero values
package authorization

import (
	"errors"
	"fmt"
)

// ErrNotApplicable reports authorization tuples observed under a profile
// that does not activate them. The containing native transaction is not
// rejected by this fact; publishing its tuples as normalized BSC data is.
var ErrNotApplicable = errors.New("bsc/authorization: authorization tuples are not applicable in the current BSC profile")

// Profile is the closed authorization profile of one BSC normalization
// profile revision. The zero value is the current empty profile.
type Profile struct{}

// Current returns the closed empty profile of the current BSC profile
// revision. EIP-7702 tuples stay outside the published record until a
// versioned registry update activates them.
func Current() Profile { return Profile{} }

// Active reports whether the profile accepts authorization tuples. The
// current BSC profile does not.
func (Profile) Active() bool { return false }

// Tuple is the source shape of one authorization tuple, preserved verbatim
// for the future registry: the signed authority designation with its
// original signature fields. A tuple keeps invalid parity, an unverifiable
// signature or a duplicate entry exactly as the source carries it; the
// native transaction may skip a tuple and continue, which is why the tuple
// list is data, not a validation verdict.
type Tuple struct {
	// ChainID is the tuple chain id, unsigned 256-bit.
	ChainID [32]byte
	// Address is the designated code address.
	Address [20]byte
	// Nonce is the authority nonce the tuple designates.
	Nonce uint64
	// Parity is the source y-parity byte, kept even when it is not 0 or 1.
	Parity byte
	// R and S are the source signature scalars, kept even when the
	// signature does not recover.
	R [32]byte
	S [32]byte
}

// Accept validates the observed tuple list of one native transaction
// against the profile. Under the current BSC profile every non-empty list
// is an explicit normalization error: the tuples exist on the wire, but the
// current fork rules never apply them, and silently publishing or silently
// dropping them would both misstate the native semantics. An empty list is
// the only accepted value and produces no tuples.
func (Profile) Accept(tuples []Tuple) ([]Tuple, error) {
	if len(tuples) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("%w: %d tuples", ErrNotApplicable, len(tuples))
}
