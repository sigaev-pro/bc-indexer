package state

import (
	"bytes"
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

func addr(b byte) balance.Address20 {
	var out balance.Address20
	out[0] = b
	return out
}

func key(b byte) block.Hash32 {
	var out block.Hash32
	out[0] = b
	return out
}

func u256(v uint64) balance.UInt256 { return balance.UInt256FromUint64(v) }

func TestNormalizeSortsAccountsAndKeys(t *testing.T) {
	raw := []AccountChange{
		{Address: addr(0x02), ExistedBefore: true, ExistsAfter: true, Storage: []StorageChange{
			{Key: key(0x09), Before: key(0), After: key(1)},
			{Key: key(0x04), Before: key(0), After: key(1)},
		}},
		{Address: addr(0x01), ExistedBefore: true, ExistsAfter: true},
	}
	diff, err := Normalize(raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if diff.Accounts[0].Address != addr(0x01) || diff.Accounts[1].Address != addr(0x02) {
		t.Fatalf("accounts not sorted by raw address: %v", diff.Accounts)
	}
	storage := diff.Accounts[1].Storage
	if storage[0].Key != key(0x04) || storage[1].Key != key(0x09) {
		t.Fatalf("storage not sorted by raw key: %v", storage)
	}
	if err := diff.Validate(); err != nil {
		t.Fatalf("normalized diff rejected: %v", err)
	}
}

// Each key appears once with the transaction-initial and transaction-final
// value; intermediate values collapse.
func TestRepeatedKeysCollapseToInitialFinal(t *testing.T) {
	raw := []AccountChange{{
		Address: addr(0x01), ExistedBefore: true, ExistsAfter: true,
		Storage: []StorageChange{
			{Key: key(0x07), Before: block.Hash32{0x01}, After: block.Hash32{0x02}},
			{Key: key(0x07), Before: block.Hash32{0x02}, After: block.Hash32{0x03}},
			{Key: key(0x07), Before: block.Hash32{0x03}, After: block.Hash32{0x04}},
		},
	}}
	diff, err := Normalize(raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(diff.Accounts[0].Storage) != 1 {
		t.Fatalf("want one collapsed entry, got %d", len(diff.Accounts[0].Storage))
	}
	entry := diff.Accounts[0].Storage[0]
	if entry.Before != (block.Hash32{0x01}) || entry.After != (block.Hash32{0x04}) {
		t.Fatalf("collapsed entry lost its tx-initial/final values: %+v", entry)
	}
}

// A storage reset removes the enumeration duty for cleared keys; explicit
// writes after the reset survive.
func TestStorageResetKeepsOnlyExplicitWrites(t *testing.T) {
	raw := []AccountChange{{
		Address: addr(0x01), ExistedBefore: true, ExistsAfter: true, StorageReset: true,
		Storage: []StorageChange{
			{Key: key(0x01), Before: block.Hash32{0xaa}, After: block.Hash32{}},     // cleared by reset
			{Key: key(0x02), Before: block.Hash32{0xbb}, After: block.Hash32{0xcc}}, // rewritten after reset
		},
	}}
	diff, err := Normalize(raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	storage := diff.Accounts[0].Storage
	if len(storage) != 1 || storage[0].Key != key(0x02) {
		t.Fatalf("reset must keep only the explicit post-reset write: %+v", storage)
	}
	if !diff.Accounts[0].StorageReset {
		t.Fatal("reset flag lost")
	}
}

func TestDuplicateAccountIsError(t *testing.T) {
	raw := []AccountChange{
		{Address: addr(0x01), ExistedBefore: true, ExistsAfter: true},
		{Address: addr(0x01), ExistedBefore: true, ExistsAfter: true},
	}
	if _, err := Normalize(raw); !errors.Is(err, ErrAccountDuplicate) {
		t.Fatalf("duplicate account error = %v", err)
	}
}

func TestValidateRejectsUnsortedAndDuplicateKeys(t *testing.T) {
	diff := Diff{Accounts: []AccountChange{{
		Address: addr(0x01), ExistedBefore: true, ExistsAfter: true,
		Storage: []StorageChange{
			{Key: key(0x05)},
			{Key: key(0x05)},
		},
	}}}
	if !errors.Is(diff.Validate(), ErrStorageDuplicate) {
		t.Fatalf("duplicate key error = %v", diff.Validate())
	}

	diff = Diff{Accounts: []AccountChange{{
		Address: addr(0x01), ExistedBefore: true, ExistsAfter: true,
		Storage: []StorageChange{
			{Key: key(0x06)},
			{Key: key(0x05)},
		},
	}}}
	if !errors.Is(diff.Validate(), ErrStorageUnsorted) {
		t.Fatalf("unsorted key error = %v", diff.Validate())
	}
}

func TestValidateRejectsUnsortedAccounts(t *testing.T) {
	diff := Diff{Accounts: []AccountChange{
		{Address: addr(0x02), ExistedBefore: true, ExistsAfter: true},
		{Address: addr(0x01), ExistedBefore: true, ExistsAfter: true},
	}}
	if !errors.Is(diff.Validate(), ErrAccountUnsorted) {
		t.Fatalf("unsorted account error = %v", diff.Validate())
	}
}

func TestCodeBytesPresence(t *testing.T) {
	changed := block.Hash32{0x01}
	diff := Diff{Accounts: []AccountChange{{
		Address: addr(0x01), ExistedBefore: true, ExistsAfter: true,
		CodeHashBefore: block.Hash32{}, CodeHashAfter: changed,
	}}}
	if !errors.Is(diff.Validate(), ErrCodeBytesMissing) {
		t.Fatalf("missing code bytes error = %v", diff.Validate())
	}

	diff.Accounts[0].CodeBefore = []byte{}
	diff.Accounts[0].CodeAfter = []byte{0x60}
	if err := diff.Validate(); err != nil {
		t.Fatalf("code change with bytes rejected: %v", err)
	}

	diff.Accounts[0].CodeHashAfter = block.Hash32{}
	if !errors.Is(diff.Validate(), ErrCodeBytesExtra) {
		t.Fatalf("unchanged code with bytes error = %v", diff.Validate())
	}
}

func TestAbsentSidePoints(t *testing.T) {
	// A destroyed account: absent after, empty points, and the native
	// empty-code hash stays a present observation.
	diff := Diff{Accounts: []AccountChange{{
		Address: addr(0x01), ExistedBefore: true, ExistsAfter: false,
		CodeHashBefore: block.Hash32{0x01}, CodeHashAfter: block.Hash32{0xc5},
		CodeBefore: []byte{0x60}, CodeAfter: []byte{},
	}}}
	if err := diff.Validate(); err != nil {
		t.Fatalf("destroyed account rejected: %v", err)
	}

	diff.Accounts[0].BalanceAfter = u256(5)
	if !errors.Is(diff.Validate(), ErrAbsentWithBalance) {
		t.Fatalf("absent side with balance error = %v", diff.Validate())
	}
}

func TestBoundaryPoints(t *testing.T) {
	diff := Diff{Accounts: []AccountChange{
		{Address: addr(0x01), ExistedBefore: true, ExistsAfter: true, BalanceBefore: u256(10), BalanceAfter: u256(7)},
	}}
	if diff.BeforePoints()[addr(0x01)] != u256(10) || diff.AfterPoints()[addr(0x01)] != u256(7) {
		t.Fatal("boundary points drifted")
	}
	first := addr(0x01)
	if !bytes.Equal(diff.Accounts[0].Address[:], first[:]) {
		t.Fatal("address bytes drifted")
	}
}
