package eth

import (
	"bytes"
	"errors"
	"fmt"
)

// Authorization errors.
var (
	// ErrAuthorizationShape reports a structurally invalid tuple. Structural
	// checks never judge a tuple economically valid or invalid: an invalid
	// or duplicate tuple is preserved source data.
	ErrAuthorizationShape = errors.New("eth: invalid authorization tuple")
	// ErrExternalValidation reports an invalid external transaction
	// envelope. The check is deliberately independent of the authorization
	// list: validation of the external tx never filters tuples.
	ErrExternalValidation = errors.New("eth: invalid external transaction")
)

// AuthorizationTuple is one source authorization tuple of an EIP-7702
// transaction, preserved exactly as the source carried it: the chain id,
// the designated authority address, the authority nonce, the signature
// parity and the r/s values. Tuples the native execution rejected (wrong
// chain id, bad signature, already-consumed or out-of-order nonce) and
// duplicate tuples stay in the list in source order; filtering happens
// only in the per-tuple observed outcome, never in the source list.
type AuthorizationTuple struct {
	// ChainID is the source chain id field of the tuple. A tuple with a
	// chain id that does not match the network is still source data.
	ChainID Optional[UInt256]
	// Address is the designated delegation target.
	Address Address20
	// Nonce is the authority nonce the tuple expects.
	Nonce uint64
	// YParity is the observed signature parity value.
	YParity uint64
	// R and S are the signature words, exactly 32 bytes each.
	R [32]byte
	S [32]byte
}

// NormalizeAuthorizationList accepts the observed source tuples of one
// transaction and returns the retained list: a defensive copy in source
// order, with duplicates preserved. The tuple type is already fixed-width,
// so there is no width normalization to do; economic validity (wrong chain
// id, bad signature, consumed or out-of-order nonce) is an execution fact
// of native, not a reason to filter source data. The normalization error
// return is the seam where future structural checks of new tuple variants
// belong; v1 tuples always pass.
func NormalizeAuthorizationList(tuples []AuthorizationTuple) ([]AuthorizationTuple, error) {
	out := make([]AuthorizationTuple, len(tuples))
	copy(out, tuples)
	return out, nil
}

// ExternalEnvelope is the external transaction envelope view used by
// ValidateExternalEnvelope. It deliberately excludes the authorization
// list: the function cannot see the tuples, so envelope validation can
// never filter them (E04-01.authorization).
type ExternalEnvelope struct {
	// Sender is the recovered sender of the outer signature.
	Sender Address20
	// Nonce is the outer transaction nonce.
	Nonce uint64
	// ExpectedSender and ExpectedNonce, when present, are the values the
	// caller demands of the envelope (for example the pool or block
	// builder view).
	ExpectedSender Optional[Address20]
	ExpectedNonce  Optional[uint64]
	// ChainID is the envelope chain id; ExpectedChainID must match it when
	// present.
	ChainID         UInt256
	ExpectedChainID Optional[UInt256]
	// NativeType is the envelope type byte.
	NativeType uint32
	// GasLimit must be positive and the signature present for an executed
	// transaction.
	GasLimit        uint64
	SignatureSigned bool
}

// ValidateExternalEnvelope validates the external transaction envelope:
// sender and nonce expectations, chain id binding, a usable gas limit and a
// present outer signature. It receives no authorization list, so an
// envelope failure can never remove a tuple from the source list, and a
// tuple failure can never reject the envelope.
func ValidateExternalEnvelope(e ExternalEnvelope) error {
	if e.GasLimit == 0 {
		return fmt.Errorf("%w: zero gas limit", ErrExternalValidation)
	}
	if !e.SignatureSigned {
		return fmt.Errorf("%w: missing outer signature", ErrExternalValidation)
	}
	if want, ok := e.ExpectedSender.Get(); ok && want != e.Sender {
		return fmt.Errorf("%w: sender mismatch", ErrExternalValidation)
	}
	if want, ok := e.ExpectedNonce.Get(); ok && want != e.Nonce {
		return fmt.Errorf("%w: nonce mismatch", ErrExternalValidation)
	}
	if want, ok := e.ExpectedChainID.Get(); ok && !want.Equal(e.ChainID) {
		return fmt.Errorf("%w: chain id mismatch", ErrExternalValidation)
	}
	return nil
}

// AuthorizationTupleOutcome is the observed native per-tuple outcome. It is
// recorded from execution, never derived from the tuple bytes: an applied
// tuple set the authority code and consumed its nonce; an invalid or
// duplicate tuple kept both untouched.
type AuthorizationTupleOutcome struct {
	// TupleIndex points at the source tuple this outcome belongs to.
	TupleIndex uint64
	// Applied reports whether native executed the delegation.
	Applied bool
	// Authority is the recovered authority account of the signature.
	Authority Address20
	// NonceAfter is the authority nonce after the transaction boundary.
	NonceAfter uint64
}

// DesignationCode returns the EIP-7702 designation code the native sets on
// an authority: the 0xef0100 marker followed by the designated address
// (test/type_paths/eth/ORACLE.md, type 4: "its code is ef0100 followed by
// the delegate address ... even when the second transaction's EVM execution
// reverts").
func DesignationCode(delegate Address20) []byte {
	out := make([]byte, 0, 23)
	out = append(out, 0xef, 0x01, 0x00)
	out = append(out, delegate[:]...)
	return out
}

// accountCodeView is the transaction-boundary account view the outcome
// cross-check needs: the code bytes of one address, when observed.
type accountCodeView interface {
	CodeOf(addr Address20) ([]byte, bool)
}

// ValidateAuthorizationOutcomes reports one error per violation, joined.
func ValidateAuthorizationOutcomes(tuples []AuthorizationTuple, outcomes []AuthorizationTupleOutcome, accounts accountCodeView) error {
	var errs []error
	seen := make(map[uint64]bool, len(outcomes))
	for i := range outcomes {
		o := &outcomes[i]
		if o.TupleIndex >= uint64(len(tuples)) {
			errs = append(errs, fmt.Errorf("%w: outcome %d names tuple %d of %d",
				ErrAuthorizationShape, i, o.TupleIndex, len(tuples)))
			continue
		}
		if seen[o.TupleIndex] {
			errs = append(errs, fmt.Errorf("%w: two outcomes for tuple %d", ErrAuthorizationShape, o.TupleIndex))
			continue
		}
		seen[o.TupleIndex] = true
		tuple := tuples[o.TupleIndex]
		code, ok := accounts.CodeOf(o.Authority)
		if !ok {
			errs = append(errs, fmt.Errorf("%w: outcome %d authority has no account view",
				ErrAuthorizationShape, i))
			continue
		}
		if o.Applied {
			if want := DesignationCode(tuple.Address); !bytes.Equal(code, want) {
				errs = append(errs, fmt.Errorf("%w: applied tuple %d left code %x, want designation %x",
					ErrAuthorizationShape, o.TupleIndex, code, want))
			}
		}
	}
	return errors.Join(errs...)
}
