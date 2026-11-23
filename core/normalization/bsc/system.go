package bsc

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// SystemCallKind is the registered semantic kind of one Parlia system
// contract call. The registry covers the system calls observed in the
// retained E01 evidence; an unregistered selector keeps its real effects
// under ParliaSystem and simply has no independent distribution
// expectation.
type SystemCallKind uint8

// System call kinds.
const (
	// SystemCallInvalid is the unset value.
	SystemCallInvalid SystemCallKind = iota
	// SystemCallInitializeLegacy is the legacy init() round of the
	// validator-family contracts (selector e1c7392a).
	SystemCallInitializeLegacy
	// SystemCallInitializeFeynman is the Feynman initializeContracts
	// round (selector 8129fc1c) covering the StakeHub family.
	SystemCallInitializeFeynman
	// SystemCallDistributeFees is the deposit call that distributes the
	// swept block fee (selector f340fa01): reward fraction to the system
	// reward contract, burn fraction to the burn address, the rest to the
	// validator set contract income.
	SystemCallDistributeFees
	// SystemCallUpdateValidatorSet is the election-time validator set
	// update (selector 1e4c1524): the validator set contract balance moves
	// to the reward contract (the dusk transfer).
	SystemCallUpdateValidatorSet
	// SystemCallUnregistered is a system call whose selector is not in
	// the retained evidence registry (slash paths, direct StakeHub
	// interactions). Its real effects are kept; no economic expectation is
	// invented for it.
	SystemCallUnregistered
)

var systemCallNames = [...]string{
	"INVALID", "INITIALIZE_LEGACY", "INITIALIZE_FEYNMAN", "DISTRIBUTE_FEES",
	"UPDATE_VALIDATOR_SET", "UNREGISTERED",
}

// String returns the registry name of k.
func (k SystemCallKind) String() string {
	if k == 0 || int(k) >= len(systemCallNames) {
		return fmt.Sprintf("SYSTEM_CALL_INVALID(%d)", uint8(k))
	}
	return systemCallNames[k]
}

// The selector bytes of the registered system calls, from the retained
// full-import oracle (test/type_paths/bsc/full_import/judge.py: INIT
// e1c7392a, INITIALIZE 8129fc1c, DEPOSIT f340fa01, ELECTION 1e4c1524).
var (
	selectorInitializeLegacy   = [4]byte{0xe1, 0xc7, 0x39, 0x2a}
	selectorInitializeFeynman  = [4]byte{0x81, 0x29, 0xfc, 0x1c}
	selectorDistributeFees     = [4]byte{0xf3, 0x40, 0xfa, 0x01}
	selectorUpdateValidatorSet = [4]byte{0x1e, 0x4c, 0x15, 0x24}
)

// ClassifySystemCall resolves the registered kind of a system call from
// its calldata selector. Input shorter than four bytes is unregistered:
// no error, because the native engine executed it and the effects are
// kept; only the independent expectation is unavailable.
func ClassifySystemCall(input []byte) SystemCallKind {
	if len(input) < 4 {
		return SystemCallUnregistered
	}
	var selector [4]byte
	copy(selector[:], input[:4])
	switch selector {
	case selectorInitializeLegacy:
		return SystemCallInitializeLegacy
	case selectorInitializeFeynman:
		return SystemCallInitializeFeynman
	case selectorDistributeFees:
		return SystemCallDistributeFees
	case selectorUpdateValidatorSet:
		return SystemCallUpdateValidatorSet
	default:
		return SystemCallUnregistered
	}
}

// Distribution is the fee-distribution model of the deposit system call.
// The fractions live in validator-set contract storage (slots 6/1000,
// 15/625, 20/1 in the retained oracle); the defaults are the deployed
// fixture values, and the checker takes the observed values so a changed
// contract state changes the expectation.
type Distribution struct {
	// RewardNumerator over Denominator is the system-reward fraction.
	RewardNumerator uint64
	// BurnNumerator over Denominator is the burn fraction.
	BurnNumerator uint64
	// Denominator is the common denominator.
	Denominator uint64
}

// DefaultDistribution returns the deployed fixture distribution:
// reward 625/10000, burn 1000/10000 (ledger.py of the retained import:
// value * 625 // 10000 and value * 1000 // 10000).
func DefaultDistribution() Distribution { return Distribution{625, 1000, 10000} }

// Split returns the reward part, the burn part and the validator-set part
// of value under the distribution, with exact integer floor arithmetic.
func (d Distribution) Split(value balance.UInt256) (reward, burn, rest balance.UInt256, err error) {
	if d.Denominator == 0 {
		return balance.UInt256{}, balance.UInt256{}, balance.UInt256{}, errors.New("bsc: distribution denominator is zero")
	}
	v := value.Big()
	reward = bigToUInt256(new(big.Int).Div(new(big.Int).Mul(v, new(big.Int).SetUint64(d.RewardNumerator)), new(big.Int).SetUint64(d.Denominator)))
	burn = bigToUInt256(new(big.Int).Div(new(big.Int).Mul(v, new(big.Int).SetUint64(d.BurnNumerator)), new(big.Int).SetUint64(d.Denominator)))
	rest = bigToUInt256(new(big.Int).Sub(new(big.Int).Sub(v, reward.Big()), burn.Big()))
	return reward, burn, rest, nil
}

func bigToUInt256(v *big.Int) balance.UInt256 {
	out, ok := balance.UInt256FromBig(v)
	if !ok {
		return balance.UInt256{}
	}
	return out
}

// SystemAccounts names the accounts the independent system-transition
// expectations refer to.
type SystemAccounts struct {
	// Validator is the system sender: the block coinbase whose recovered
	// sender produced the system transaction.
	Validator balance.Address20
	// ValidatorSetContract is the validator set contract (0x1000 family).
	ValidatorSetContract balance.Address20
	// RewardContract is the system reward contract (0x1002 family).
	RewardContract balance.Address20
	// BurnAddress is the burn recipient of the distribution.
	BurnAddress balance.Address20
}

// AccountPoint is one observed account point of a system boundary.
type AccountPoint struct {
	Balance balance.UInt256
	Nonce   uint64
	Exists  bool
}

// Sentinel errors of the independent system-transition check.
var (
	ErrSystemNonce        = errors.New("bsc: a system transaction increments the system sender nonce by exactly one")
	ErrSystemSender       = errors.New("bsc: the system sender balance decreases by exactly the transaction value")
	ErrSystemStatic       = errors.New("bsc: an initializer system transaction moves no funds")
	ErrSystemDeposit      = errors.New("bsc: the deposit distribution does not match the observed points")
	ErrSystemElection     = errors.New("bsc: the election dusk transfer does not match the observed points")
	ErrSystemPointMissing = errors.New("bsc: an expected system account has no observed boundary point")
)

// ExpectSystemTransition independently checks the observed before/after
// points of one Parlia system transaction against the transition the
// retained native evidence establishes:
//
//   - every system transaction: the sender nonce rises by exactly one and
//     the sender balance falls by exactly the transaction value
//   - the initializer rounds (legacy init and Feynman initialize) move no
//     funds: every observed balance point is unchanged
//   - the deposit distributes value by the observed fractions: reward
//     contract +reward, burn address +burn, validator set contract +rest
//   - the validator-set update moves the whole validator set contract
//     balance to the reward contract (the dusk transfer)
//   - an unregistered call keeps its real effects; only the generic sender
//     rules apply
//
// Anchors: test/type_paths/bsc/full_import/ledger.py (BalanceLedger.apply)
// and judge.py (check_system), the independently expected nonce, balance
// and storage transitions of the retained private import.
func ExpectSystemTransition(kind SystemCallKind, sender balance.Address20, value balance.UInt256,
	accounts SystemAccounts, distribution Distribution, before, after map[balance.Address20]AccountPoint) error {
	point := func(points map[balance.Address20]AccountPoint, addr balance.Address20) (AccountPoint, error) {
		p, ok := points[addr]
		if !ok {
			return AccountPoint{}, fmt.Errorf("%w: %x", ErrSystemPointMissing, addr)
		}
		return p, nil
	}
	senderBefore, err := point(before, sender)
	if err != nil {
		return err
	}
	senderAfter, err := point(after, sender)
	if err != nil {
		return err
	}
	if senderAfter.Nonce != senderBefore.Nonce+1 {
		return fmt.Errorf("%w: %d -> %d", ErrSystemNonce, senderBefore.Nonce, senderAfter.Nonce)
	}
	want, uerr := senderBefore.Balance.SubChecked(value)
	if uerr != nil {
		return fmt.Errorf("%w: underflow", ErrSystemSender)
	}
	if senderAfter.Balance != want {
		return fmt.Errorf("%w: %s -> %s with value %s", ErrSystemSender,
			senderBefore.Balance.Decimal(), senderAfter.Balance.Decimal(), value.Decimal())
	}

	switch kind {
	case SystemCallInitializeLegacy, SystemCallInitializeFeynman:
		for addr, p := range before {
			q, ok := after[addr]
			if !ok {
				return fmt.Errorf("%w: %x", ErrSystemPointMissing, addr)
			}
			if p.Balance != q.Balance {
				return fmt.Errorf("%w: %x moved %s -> %s", ErrSystemStatic, addr,
					p.Balance.Decimal(), q.Balance.Decimal())
			}
		}
		return nil

	case SystemCallDistributeFees:
		reward, burn, rest, derr := distribution.Split(value)
		if derr != nil {
			return derr
		}
		for _, side := range []struct {
			addr  balance.Address20
			delta balance.UInt256
			name  string
		}{
			{accounts.ValidatorSetContract, rest, "validator set contract"},
			{accounts.RewardContract, reward, "reward contract"},
			{accounts.BurnAddress, burn, "burn address"},
		} {
			p, err := point(before, side.addr)
			if err != nil {
				return err
			}
			q, err := point(after, side.addr)
			if err != nil {
				return err
			}
			want, aerr := p.Balance.AddChecked(side.delta)
			if aerr != nil {
				return fmt.Errorf("%w: %s overflow", ErrSystemDeposit, side.name)
			}
			if q.Balance != want {
				return fmt.Errorf("%w: %s %s -> %s, want %s", ErrSystemDeposit, side.name,
					p.Balance.Decimal(), q.Balance.Decimal(), want.Decimal())
			}
		}
		return nil

	case SystemCallUpdateValidatorSet:
		setBefore, err := point(before, accounts.ValidatorSetContract)
		if err != nil {
			return err
		}
		setAfter, err := point(after, accounts.ValidatorSetContract)
		if err != nil {
			return err
		}
		rewardBefore, err := point(before, accounts.RewardContract)
		if err != nil {
			return err
		}
		rewardAfter, err := point(after, accounts.RewardContract)
		if err != nil {
			return err
		}
		if !setAfter.Balance.IsZero() {
			return fmt.Errorf("%w: validator set contract keeps %s", ErrSystemElection, setAfter.Balance.Decimal())
		}
		want, aerr := rewardBefore.Balance.AddChecked(setBefore.Balance)
		if aerr != nil {
			return fmt.Errorf("%w: reward overflow", ErrSystemElection)
		}
		if rewardAfter.Balance != want {
			return fmt.Errorf("%w: reward %s -> %s, want %s", ErrSystemElection,
				rewardBefore.Balance.Decimal(), rewardAfter.Balance.Decimal(), want.Decimal())
		}
		return nil

	default:
		return nil
	}
}
