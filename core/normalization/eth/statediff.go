package eth

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
)

// State diff errors.
var (
	// ErrStateDiffShape reports a structurally invalid account observation.
	ErrStateDiffShape = errors.New("eth: invalid state diff")
)

// StorageChange is one transaction-boundary storage change of one account
// (MESSAGE_CONTRACT section 3): the raw key and the tx initial and final
// values. The stored list is sorted by raw key bytes for deterministic
// normalization, and each key appears exactly once.
type StorageChange struct {
	Key    Hash32
	Before Hash32
	After  Hash32
}

// AccountChange is the final transaction-boundary change of one touched
// account: existence before and after the boundary, the balance, nonce and
// code-hash points, the code bytes when the code changed, and the storage
// view. Account absence is expressed by the exists flags, never by an
// empty code or a zero balance read as absence.
//
// The whole diff is the committed tx result only: effects that a rollback
// cancelled do not appear, and fees and the final nonce the native kept on
// a root revert do appear (D74 rule 7).
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
	// CodeBefore and CodeAfter are present exactly when the code changed;
	// an unchanged code is not repeated as bytes.
	CodeBefore []byte
	CodeAfter  []byte
	// StorageReset marks that native deleted the account's whole previous
	// storage without enumerating every key (selfdestruct deletion or a
	// recreation reset). Writes after the reset are listed explicitly in
	// StorageChanges.
	StorageReset bool
	// StorageChanges is sorted by raw key bytes; every key appears once
	// with the tx initial and final values.
	StorageChanges []StorageChange
}

// StateDiff is the committed transaction-boundary state diff of one
// transaction, ordered by raw address bytes.
type StateDiff struct {
	Accounts []AccountChange
}

// AccountPoint is one observed account state at a transaction boundary:
// existence, balance, nonce, code hash, code bytes and the observed
// storage slots.
type AccountPoint struct {
	Exists   bool
	Balance  UInt256
	Nonce    uint64
	CodeHash Hash32
	Code     []byte
	// Storage carries the observed slots of this observation. Only slots
	// the observer read appear; a slot absent from both points is not a
	// change.
	Storage map[Hash32]Hash32
}

// AccountObservation is the before/after pair of one touched account. The
// observer records StorageReset when native deleted the previous storage
// without enumerating keys.
type AccountObservation struct {
	Address      Address20
	Before       AccountPoint
	After        AccountPoint
	StorageReset bool
}

// BuildStateDiff normalizes the observed transaction-boundary points into
// the stored diff:
//
//   - accounts are ordered by raw address bytes; a duplicate address is an
//     error;
//   - storage changes keep only keys whose value differs between the two
//     boundary points, one entry per key with the tx initial and final
//     values, sorted by raw key bytes;
//   - the code bytes are stored exactly when the code hash changed;
//   - an account whose boundary state is identical (existence, balance,
//     nonce, code hash and storage) gets no row: the diff is the final
//     state view. A balance of 100 -> 110 -> 100 inside one transaction
//     leaves no diff row; the two movements stay in the balance actions
//     (BALANCE_MODEL, "Сложные случаи"). An account created and deleted
//     inside one transaction with identical final state also leaves no
//     row; the creation attempt and the deletion live in the calls and
//     execution actions.
//
// Rollback correctness is inherited from the boundary points themselves:
// the observer takes the points at the transaction boundary, so a
// transient execution write that a revert cancelled (for example slot 0 of
// the corpus revert contract, which stores 1 and then reverts, leaving the
// slot zero before and after) never enters the diff, and fees or the final
// nonce that native kept on a root revert do enter it.
func BuildStateDiff(observations []AccountObservation) (StateDiff, error) {
	out := StateDiff{Accounts: make([]AccountChange, 0, len(observations))}
	seen := make(map[Address20]bool, len(observations))
	for i := range observations {
		o := &observations[i]
		if seen[o.Address] {
			return StateDiff{}, fmt.Errorf("%w: duplicate address %x", ErrStateDiffShape, o.Address)
		}
		seen[o.Address] = true
		change := AccountChange{
			Address:        o.Address,
			ExistedBefore:  o.Before.Exists,
			ExistsAfter:    o.After.Exists,
			BalanceBefore:  o.Before.Balance,
			BalanceAfter:   o.After.Balance,
			NonceBefore:    o.Before.Nonce,
			NonceAfter:     o.After.Nonce,
			CodeHashBefore: o.Before.CodeHash,
			CodeHashAfter:  o.After.CodeHash,
			StorageReset:   o.StorageReset,
		}
		if o.Before.CodeHash != o.After.CodeHash {
			change.CodeBefore = o.Before.Code
			change.CodeAfter = o.After.Code
		}
		keys := make([]Hash32, 0, len(o.Before.Storage)+len(o.After.Storage))
		visited := make(map[Hash32]bool, len(o.Before.Storage)+len(o.After.Storage))
		for k := range o.Before.Storage {
			if !visited[k] {
				visited[k] = true
				keys = append(keys, k)
			}
		}
		for k := range o.After.Storage {
			if !visited[k] {
				visited[k] = true
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })
		// The reset marker covers the removal of the whole previous
		// storage without enumerating keys; only the writes that landed
		// after the reset stay explicit (CURRENT_SEMANTICS). Without a
		// reset, every differing key gets its own row.
		for _, k := range keys {
			before, inBefore := o.Before.Storage[k]
			after, inAfter := o.After.Storage[k]
			if o.StorageReset && !inAfter {
				continue
			}
			switch {
			case inBefore && inAfter && before == after:
				continue
			case !inBefore && !inAfter:
				continue
			}
			change.StorageChanges = append(change.StorageChanges, StorageChange{Key: k, Before: before, After: after})
		}
		if accountUnchanged(&change) {
			continue
		}
		out.Accounts = append(out.Accounts, change)
	}
	sort.Slice(out.Accounts, func(i, j int) bool {
		return bytes.Compare(out.Accounts[i].Address[:], out.Accounts[j].Address[:]) < 0
	})
	if err := out.Validate(); err != nil {
		return StateDiff{}, err
	}
	return out, nil
}

// accountUnchanged reports whether the boundary state of one account is
// identical, so the final diff carries no row for it.
func accountUnchanged(c *AccountChange) bool {
	return c.ExistedBefore == c.ExistsAfter &&
		c.BalanceBefore.Equal(c.BalanceAfter) &&
		c.NonceBefore == c.NonceAfter &&
		c.CodeHashBefore == c.CodeHashAfter &&
		len(c.StorageChanges) == 0
}

// Validate checks the stored diff: address order without duplicates,
// storage key order with unique keys, and the code-presence rule.
func (d *StateDiff) Validate() error {
	for i := range d.Accounts {
		a := &d.Accounts[i]
		if i > 0 && bytes.Compare(d.Accounts[i-1].Address[:], a.Address[:]) >= 0 {
			return fmt.Errorf("%w: accounts not sorted by raw address", ErrStateDiffShape)
		}
		codeChanged := a.CodeHashBefore != a.CodeHashAfter
		if codeChanged && (a.CodeBefore == nil || a.CodeAfter == nil) {
			return fmt.Errorf("%w: code bytes missing on a code change of %x", ErrStateDiffShape, a.Address)
		}
		if !codeChanged && (a.CodeBefore != nil || a.CodeAfter != nil) {
			return fmt.Errorf("%w: code bytes present without a code change of %x", ErrStateDiffShape, a.Address)
		}
		for j := range a.StorageChanges {
			if j > 0 && bytes.Compare(a.StorageChanges[j-1].Key[:], a.StorageChanges[j].Key[:]) >= 0 {
				return fmt.Errorf("%w: storage keys of %x not sorted or duplicated", ErrStateDiffShape, a.Address)
			}
			if a.StorageChanges[j].Before == a.StorageChanges[j].After {
				return fmt.Errorf("%w: no-op storage change of %x", ErrStateDiffShape, a.Address)
			}
		}
		if !a.ExistsAfter && len(a.StorageChanges) > 0 {
			// A deleted account has no final storage; writes after a reset
			// belong to an account that exists after.
			return fmt.Errorf("%w: storage changes on an account absent after the boundary", ErrStateDiffShape)
		}
	}
	return nil
}

// CodeOf implements the authorization outcome cross-check view: the code
// bytes of one address after the boundary. ok reports whether the account
// exists after; a nil code with ok means the code did not change (or is
// the empty code), which no applied designation can match.
func (d *StateDiff) CodeOf(addr Address20) ([]byte, bool) {
	for i := range d.Accounts {
		if d.Accounts[i].Address != addr {
			continue
		}
		if !d.Accounts[i].ExistsAfter {
			return nil, false
		}
		return d.Accounts[i].CodeAfter, true
	}
	return nil, false
}

// BoundaryBalances returns the balance points of every account of the
// diff, for the fixture reconciliation oracle balance.ReconcileBoundary.
func (d *StateDiff) BoundaryBalances() (before, after map[Address20]UInt256) {
	before = make(map[Address20]UInt256, len(d.Accounts))
	after = make(map[Address20]UInt256, len(d.Accounts))
	for i := range d.Accounts {
		a := &d.Accounts[i]
		before[a.Address] = a.BalanceBefore
		after[a.Address] = a.BalanceAfter
	}
	return before, after
}

// Find returns the change of one address.
func (d *StateDiff) Find(addr Address20) (AccountChange, bool) {
	for i := range d.Accounts {
		if d.Accounts[i].Address == addr {
			return d.Accounts[i], true
		}
	}
	return AccountChange{}, false
}
