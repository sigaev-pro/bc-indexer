package poly

import (
	"fmt"
	"sort"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// StorageChange is one storage slot boundary of one transaction: the tx
// initial and final values of one key, exactly once per key.
type StorageChange struct {
	Key    Hash32
	Before Hash32
	After  Hash32
}

// AccountChange is the transaction-boundary diff of one touched account
// (MESSAGE_CONTRACT section 3): existence, balance, nonce, code and storage
// before and after the whole transaction, after rollback. It is a separate
// representation from the actions: the two are checked against each other
// and never summed as independent changes.
type AccountChange struct {
	Address        Address20
	ExistedBefore  bool
	ExistsAfter    bool
	BalanceBefore  UInt256
	BalanceAfter   UInt256
	NonceBefore    uint64
	NonceAfter     uint64
	CodeHashBefore Hash32
	CodeHashAfter  Hash32
	CodeBefore     []byte
	CodeAfter      []byte
	StorageReset   bool
	StorageChanges []StorageChange
}

// normalizeStateDiff builds the deterministic transaction-boundary diff:
// accounts sorted by raw address bytes, storage sorted by raw key bytes,
// one row per key with the tx initial and final values. Existence is kept
// in the exists flags; an absent account is never encoded as empty code,
// and a present zero balance is data, not absence.
func normalizeStateDiff(tx *TxCapture) ([]AccountChange, error) {
	out := make([]AccountChange, len(tx.Accounts))
	for i, acc := range tx.Accounts {
		if acc.ExistsAfter && !acc.ExistedBefore && acc.CodeAfter == nil {
			return nil, fmt.Errorf("account %x exists after without observed code: %w", acc.Address, ErrCapture)
		}
		change := AccountChange{
			Address:        acc.Address,
			ExistedBefore:  acc.ExistedBefore,
			ExistsAfter:    acc.ExistsAfter,
			BalanceBefore:  acc.BalanceBefore,
			BalanceAfter:   acc.BalanceAfter,
			NonceBefore:    acc.NonceBefore,
			NonceAfter:     acc.NonceAfter,
			CodeHashBefore: acc.CodeHashBefore,
			CodeHashAfter:  acc.CodeHashAfter,
			StorageReset:   acc.StorageReset,
		}
		if acc.CodeBefore != nil {
			change.CodeBefore = append([]byte(nil), acc.CodeBefore...)
		}
		if acc.CodeAfter != nil {
			change.CodeAfter = append([]byte(nil), acc.CodeAfter...)
		}
		// Code bytes are mandatory when the code hash changed.
		if acc.CodeHashBefore != acc.CodeHashAfter {
			if acc.CodeBefore == nil || acc.CodeAfter == nil {
				return nil, fmt.Errorf("account %x changed code without both code observations: %w",
					acc.Address, ErrCapture)
			}
		}
		// Storage: one row per key, deterministic order.
		seen := make(map[Hash32]bool, len(acc.Storage))
		for _, slot := range acc.Storage {
			if seen[slot.Key] {
				return nil, fmt.Errorf("account %x repeats storage key %s: %w", acc.Address, slot.Key, ErrCapture)
			}
			seen[slot.Key] = true
			change.StorageChanges = append(change.StorageChanges, StorageChange{
				Key: slot.Key, Before: slot.Before, After: slot.After,
			})
		}
		sort.Slice(change.StorageChanges, func(a, b int) bool {
			return lessHash(change.StorageChanges[a].Key, change.StorageChanges[b].Key)
		})
		out[i] = change
	}
	sort.Slice(out, func(a, b int) bool { return lessAddress(out[a].Address, out[b].Address) })
	for i := 1; i < len(out); i++ {
		if out[i].Address == out[i-1].Address {
			return nil, fmt.Errorf("account %x appears twice: %w", out[i].Address, ErrCapture)
		}
	}
	return out, nil
}

func lessAddress(a, b Address20) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func lessHash(a, b Hash32) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// ReconcileBoundary is the fixture oracle of CURRENT_SEMANTICS D62: the
// committed address effects of the transaction must agree with the
// boundary balance points of every touched account. It is a test oracle
// over retained fixtures, not a production balance completeness verifier.
func (r *TransactionRecord) ReconcileBoundary() error {
	before := make(map[balance.Address20]balance.UInt256, len(r.StateChanges))
	after := make(map[balance.Address20]balance.UInt256, len(r.StateChanges))
	for _, change := range r.StateChanges {
		before[change.Address] = change.BalanceBefore
		after[change.Address] = change.BalanceAfter
	}
	return balance.ReconcileBoundary(r.Scopes, r.Actions, before, after)
}
