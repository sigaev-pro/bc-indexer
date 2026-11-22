// Package state is the transaction-boundary state-diff area of the BSC
// normalization module (E04-03.state_diff).
//
// One Diff is the committed, transaction-boundary view of the accounts and
// storage the transaction touched: existence, balance, nonce, code and
// storage before and after the transaction, after rollback. It is a
// separate representation from the causal balance actions; the two are
// reconciled by the fixture oracle, never summed as independent changes.
//
// Normative anchors:
//
//   - .project/docs/CURRENT_SEMANTICS.md, "Block, calls and account
//     changes": AccountChange carries code_hash_before/code_hash_after;
//     before/after code bytes are mandatory when code changed; absence of
//     an account is expressed by the exists flags, not by empty code;
//     storage changes are sorted by raw key bytes with one entry per key
//     holding the tx initial/final value; storage_reset marks the removal
//     of all previous storage without enumerating every cleared key, and
//     writes after the reset stay explicit; the overall diff is only the
//     committed tx result
//   - .project/docs/MESSAGE_CONTRACT.md, section 3, AccountChange and
//     StorageChange rows
package state

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// Sentinel errors of the diff validator.
var (
	ErrAccountUnsorted   = errors.New("bsc/state: accounts must be stored in ascending raw address order")
	ErrAccountDuplicate  = errors.New("bsc/state: duplicate account address in the raw boundary rows")
	ErrStorageUnsorted   = errors.New("bsc/state: storage changes must be stored in ascending raw key order")
	ErrStorageDuplicate  = errors.New("bsc/state: each storage key appears exactly once")
	ErrCodeBytesMissing  = errors.New("bsc/state: a code change requires the before and after code bytes")
	ErrCodeBytesExtra    = errors.New("bsc/state: unchanged code carries no code bytes")
	ErrAbsentWithBalance = errors.New("bsc/state: an absent account side cannot hold balance or nonce")
)

// StorageChange is one storage key of the transaction boundary: the
// transaction-initial and transaction-final value of that key.
type StorageChange struct {
	Key    block.Hash32
	Before block.Hash32
	After  block.Hash32
}

// AccountChange is one touched account of the transaction boundary.
type AccountChange struct {
	// Address is the account.
	Address balance.Address20
	// ExistedBefore and ExistsAfter express account existence separately
	// from any empty field: absence is never encoded as empty code or a
	// zero balance.
	ExistedBefore bool
	ExistsAfter   bool
	// BalanceBefore and BalanceAfter are the boundary balance points.
	BalanceBefore balance.UInt256
	BalanceAfter  balance.UInt256
	// NonceBefore and NonceAfter are the boundary nonce points.
	NonceBefore uint64
	NonceAfter  uint64
	// CodeHashBefore and CodeHashAfter are the boundary code hashes.
	CodeHashBefore block.Hash32
	CodeHashAfter  block.Hash32
	// CodeBefore and CodeAfter are mandatory exactly when the code hash
	// changed; they are the full code bytes, never truncated.
	CodeBefore []byte
	CodeAfter  []byte
	// StorageReset marks that the whole previous storage was removed
	// without enumerating every cleared key. Writes after the reset stay
	// explicit entries.
	StorageReset bool
	// Storage holds the raw storage observations of the transaction:
	// repeated keys with intermediate values are allowed here. Normalize
	// collapses them.
	Storage []StorageChange
}

// Diff is the committed transaction-boundary state diff of one
// transaction, normalized: accounts in ascending raw address order,
// storage in ascending raw key order with one entry per key.
type Diff struct {
	Accounts []AccountChange
}

// Normalize builds the normalized committed diff from raw boundary
// observations: one row per touched account, storage rows in observation
// order with possible repeated keys. The normalized storage entry keeps
// the transaction-initial and transaction-final value of its key. The
// input order does not survive: normalization is by raw bytes, which is
// what makes the record deterministic across capture recoveries.
func Normalize(raw []AccountChange) (Diff, error) {
	out := Diff{Accounts: make([]AccountChange, 0, len(raw))}
	seen := make(map[balance.Address20]bool, len(raw))
	for i := range raw {
		account := raw[i]
		if seen[account.Address] {
			return Diff{}, fmt.Errorf("account %x: %w", account.Address, ErrAccountDuplicate)
		}
		seen[account.Address] = true
		collapsed, err := collapseStorage(account)
		if err != nil {
			return Diff{}, fmt.Errorf("account %x: %w", account.Address, err)
		}
		out.Accounts = append(out.Accounts, collapsed)
	}
	sort.Slice(out.Accounts, func(i, j int) bool {
		return bytes.Compare(out.Accounts[i].Address[:], out.Accounts[j].Address[:]) < 0
	})
	return out, nil
}

// collapseStorage collapses repeated storage keys into one entry holding
// the first observed before value and the last observed after value, and
// drops entries that a storage reset made unrepresentable: after a reset
// the cleared keys are not enumerated, so only keys whose final value
// differs from their initial value survive as explicit writes.
func collapseStorage(account AccountChange) (AccountChange, error) {
	first := make(map[block.Hash32]block.Hash32, len(account.Storage))
	last := make(map[block.Hash32]block.Hash32, len(account.Storage))
	for _, change := range account.Storage {
		if _, ok := first[change.Key]; !ok {
			first[change.Key] = change.Before
		}
		last[change.Key] = change.After
	}
	keys := make([]block.Hash32, 0, len(first))
	for key := range first {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })
	storage := make([]StorageChange, 0, len(keys))
	for _, key := range keys {
		change := StorageChange{Key: key, Before: first[key], After: last[key]}
		if account.StorageReset && change.After == (block.Hash32{}) {
			// A key the reset cleared and nothing rewrote carries no
			// explicit entry: the reset flag already states its removal,
			// and a post-reset write of zero is the same observed state.
			continue
		}
		storage = append(storage, change)
	}
	account.Storage = storage
	return account, nil
}

// Validate checks the normalized shape: ascending unique accounts,
// ascending unique storage keys, code bytes exactly when the code hash
// changed, and consistent existence points on each side.
func (d Diff) Validate() error {
	var errs []error
	for i := range d.Accounts {
		a := &d.Accounts[i]
		if i > 0 {
			switch bytes.Compare(a.Address[:], d.Accounts[i-1].Address[:]) {
			case 0:
				errs = append(errs, fmt.Errorf("account %x: %w", a.Address, ErrAccountDuplicate))
			case -1:
				errs = append(errs, fmt.Errorf("account %x: %w", a.Address, ErrAccountUnsorted))
			}
		}
		codeChanged := a.CodeHashBefore != a.CodeHashAfter
		switch {
		case codeChanged && (a.CodeBefore == nil || a.CodeAfter == nil):
			errs = append(errs, fmt.Errorf("account %x: %w", a.Address, ErrCodeBytesMissing))
		case !codeChanged && (a.CodeBefore != nil || a.CodeAfter != nil):
			errs = append(errs, fmt.Errorf("account %x: %w", a.Address, ErrCodeBytesExtra))
		}
		if !a.ExistedBefore {
			if err := checkAbsentSide(a.BalanceBefore, a.NonceBefore); err != nil {
				errs = append(errs, fmt.Errorf("account %x existed_before=false: %w", a.Address, err))
			}
		}
		if !a.ExistsAfter {
			if err := checkAbsentSide(a.BalanceAfter, a.NonceAfter); err != nil {
				errs = append(errs, fmt.Errorf("account %x exists_after=false: %w", a.Address, err))
			}
		}
		for j := range a.Storage {
			key := a.Storage[j].Key
			if j > 0 {
				switch bytes.Compare(key[:], a.Storage[j-1].Key[:]) {
				case 0:
					errs = append(errs, fmt.Errorf("account %x key %x: %w", a.Address, key, ErrStorageDuplicate))
				case -1:
					errs = append(errs, fmt.Errorf("account %x key %x: %w", a.Address, key, ErrStorageUnsorted))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// checkAbsentSide checks that a nonexistent account side carries the
// absent points: no balance and no nonce. The code hash is not checked: a
// destroyed account keeps the native empty-code hash, which is a present
// observation, and the exists flags alone carry the absence fact. A
// nonexistent side with a balance or a nonce would encode a present
// account as absent.
func checkAbsentSide(amount balance.UInt256, nonce uint64) error {
	if !amount.IsZero() || nonce != 0 {
		return ErrAbsentWithBalance
	}
	return nil
}

// BeforePoints returns the boundary before points of the diff, for the
// fixture reconciliation oracle.
func (d Diff) BeforePoints() map[balance.Address20]balance.UInt256 {
	out := make(map[balance.Address20]balance.UInt256, len(d.Accounts))
	for i := range d.Accounts {
		out[d.Accounts[i].Address] = d.Accounts[i].BalanceBefore
	}
	return out
}

// AfterPoints returns the boundary after points of the diff, for the
// fixture reconciliation oracle.
func (d Diff) AfterPoints() map[balance.Address20]balance.UInt256 {
	out := make(map[balance.Address20]balance.UInt256, len(d.Accounts))
	for i := range d.Accounts {
		out[d.Accounts[i].Address] = d.Accounts[i].BalanceAfter
	}
	return out
}
