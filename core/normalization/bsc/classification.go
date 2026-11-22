package bsc

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// Classification is the typed BSC classification of one body transaction:
// a classification over the actual standard wire type, never a new type
// id. The wire type, signature, nonce and original payload are preserved
// unchanged.
type Classification uint8

// Classifications. Zero is not a valid classification.
const (
	// ClassificationInvalid is the unset value.
	ClassificationInvalid Classification = iota
	// ClassificationOrdinary is an ordinary transaction.
	ClassificationOrdinary
	// ClassificationParliaSystem is a Parlia system transaction: a native
	// system-contract call the Parlia engine assembled into the block
	// body (validator-set updates, fee distribution, initializers).
	ClassificationParliaSystem
)

var classificationNames = [...]string{"INVALID", "Ordinary", "ParliaSystem"}

// String returns the contract name of c.
func (c Classification) String() string {
	if c == 0 || int(c) >= len(classificationNames) {
		return fmt.Sprintf("CLASSIFICATION_INVALID(%d)", uint8(c))
	}
	return classificationNames[c]
}

// ErrUnclassified reports a classification attempt whose native facts do
// not resolve: a sender recovery error propagates, it is never turned into
// an Ordinary verdict.
var ErrUnclassified = errors.New("bsc: the native classification facts do not resolve")

// Classifier applies the pinned BSC system-transaction criterion: the
// recipient is a system contract, the effective gas price is zero, and the
// recovered sender equals the block coinbase. All three must hold; a
// zero-fee transaction alone, or a call of a system address alone, is
// never ParliaSystem.
//
// Anchor: adapters/bsc/type_mapping/paths.json, "classification" — "System
// recipient AND EffectiveGasPriceForBSC().Sign() == 0 AND native recovered
// sender == header.Coinbase; sender recovery errors propagate. The
// classification preserves the actual wire type and payload; it does not
// assign a new type ID."
type Classifier struct {
	// SystemContracts is the system-contract set of the pinned profile at
	// the classified block. DefaultSystemContracts returns the retained
	// modern fixture set; the profile owns the per-fork set.
	SystemContracts map[balance.Address20]bool
}

// DefaultSystemContracts returns the system-contract set of the retained
// modern BSC fixture (test/type_paths/bsc/full_import/native_test.go,
// modernCodes): the validator set, slash indicator, system reward, light
// client, token hub, relayer contracts, cross-chain components, the
// staking and StakeHub family, the governor family and the token recover
// portal.
func DefaultSystemContracts() map[balance.Address20]bool {
	set := make(map[balance.Address20]bool)
	for _, hex := range []string{
		"1000", "1001", "1002", "1003", "1004", "1005", "1006", "1007", "1008",
		"2000", "2001", "2002", "2003", "2004", "2005", "2006",
		"3000",
	} {
		set[addressFromWord(hex)] = true
	}
	return set
}

func addressFromWord(word string) balance.Address20 {
	var out balance.Address20
	padded := strings.Repeat("0", 40-len(word)) + word
	for i := 0; i < 20; i++ {
		hi := hexDigit(padded[2*i])
		lo := hexDigit(padded[2*i+1])
		out[i] = hi<<4 | lo
	}
	return out
}

func hexDigit(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 0
	}
}

// Classify classifies one observed envelope of one block whose coinbase is
// given. SenderRecoveryError marks an envelope whose native sender
// recovery failed: the error propagates, because a failed recovery can be
// neither Ordinary nor ParliaSystem.
func (c Classifier) Classify(env EnvelopeFacts, coinbase balance.Address20) (Classification, error) {
	if env.SenderRecoveryError {
		return ClassificationInvalid, fmt.Errorf("%w: position %d", ErrUnclassified, env.Position)
	}
	if env.Target != nil &&
		c.SystemContracts[*env.Target] &&
		env.EffectiveGasPrice.IsZero() &&
		env.Sender == coinbase {
		return ClassificationParliaSystem, nil
	}
	return ClassificationOrdinary, nil
}
