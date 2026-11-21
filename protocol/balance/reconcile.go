package balance

import (
	"fmt"
	"math/big"
)

// ReconcileBoundary is the fixture oracle of CURRENT_SEMANTICS D62: the sum
// of committed address effects must agree with the transaction-boundary
// balance points of every touched account. before and after carry the
// observed AccountChange balance points (balance_before / balance_after).
//
// Rules:
//
//   - Only committed effects count. A rolled-back attempt keeps its points
//     but does not move the boundary balance.
//   - Every address touched by a committed effect must have a before point:
//     a missing observation is unknown data, never an implicit zero.
//   - Every address present in the maps must be explained: its final value
//     must equal its before point plus the sum of its committed effects.
//
// This is a test oracle over retained fixtures, not a production balance
// completeness verifier.
func ReconcileBoundary(scopes []RollbackScope, actions []BalanceAction, before, after map[Address20]UInt256) error {
	rolledBack := make(map[uint64]bool, len(scopes))
	for _, s := range scopes {
		rolledBack[s.ScopeID] = s.RolledBack
	}

	type account struct {
		base    *big.Int
		running *big.Int
	}
	accounts := make(map[Address20]*account)

	ensure := func(addr Address20) (*account, error) {
		if acc, ok := accounts[addr]; ok {
			return acc, nil
		}
		base, ok := before[addr]
		if !ok {
			return nil, fmt.Errorf("address %x: %w", addr, ErrBoundaryBaselineMissing)
		}
		acc := &account{base: base.Big(), running: new(big.Int)}
		accounts[addr] = acc
		return acc, nil
	}

	for i := range actions {
		a := &actions[i]
		if !a.EffectsCommitted {
			continue
		}
		if a.RollbackScope != nil && rolledBack[*a.RollbackScope] {
			return fmt.Errorf("action %d: %w", a.ActionIndex, ErrCommittedInRolledBackScope)
		}
		for _, e := range a.Effects {
			acc, err := ensure(e.Address)
			if err != nil {
				return fmt.Errorf("action %d effect %d: %w", a.ActionIndex, e.EffectIndex, err)
			}
			sign, mag := e.Delta()
			switch sign {
			case 1:
				acc.running.Add(acc.running, mag.Big())
			case -1:
				acc.running.Sub(acc.running, mag.Big())
			}
		}
	}

	// Every mapped address must reconcile; also reject after-only addresses
	// whose change no committed action explains.
	visited := make(map[Address20]bool, len(before)+len(after))
	for addr := range before {
		visited[addr] = true
	}
	for addr := range after {
		visited[addr] = true
	}
	for addr := range accounts {
		visited[addr] = true
	}

	for addr := range visited {
		finalPoint, ok := after[addr]
		if !ok {
			return fmt.Errorf("address %x: %w", addr, ErrBoundaryAfterMissing)
		}
		acc, ok := accounts[addr]
		if !ok {
			base, hasBase := before[addr]
			if !hasBase {
				return fmt.Errorf("address %x: %w", addr, ErrBoundaryUnexplained)
			}
			if base != finalPoint {
				return fmt.Errorf("address %x: %s -> %s: %w", addr, base.Decimal(), finalPoint.Decimal(), ErrBoundaryUnexplained)
			}
			continue
		}
		expected := new(big.Int).Add(acc.base, acc.running)
		if expected.Cmp(finalPoint.Big()) != 0 {
			return fmt.Errorf("address %x: committed effects lead to %s but the after point is %s: %w",
				addr, expected.String(), finalPoint.Decimal(), ErrBoundaryMismatch)
		}
	}
	return nil
}
