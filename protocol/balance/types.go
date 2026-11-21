// Package balance is the typed model of native monetary actions shared by the
// ETH, BSC and Polygon profiles (E02-05.actions_model, D62 and D74).
//
// The package owns the record side of the model only: the closed cause union,
// the action and effect types, rollback scopes, the receipt fee summary and a
// structural validator over one transaction's balance view. Capture, wire
// field numbers and the RecordFragment codec belong to other tasks.
//
// Normative sources:
//
//   - .project/docs/CURRENT_SEMANTICS.md, sections D74 (execution order and
//     rollback scopes) and D62 (balance actions and fees)
//   - .project/docs/MESSAGE_CONTRACT.md, section 3 (TransactionRecord)
//   - .project/CONTEXT.md, terms "execution_order" and "RollbackScope"
//
// All arithmetic is exact big-endian integer arithmetic; the package never
// uses floats for amounts.
package balance

import (
	"fmt"
	"math/big"
	"strings"
)

// Address20 is a native account address: exactly 20 bytes, not an arbitrary
// string (MESSAGE_CONTRACT section 1, Hash32/Address20 row).
type Address20 [20]byte

// UInt256 is an unsigned 256-bit integer stored as exactly 32 big-endian
// bytes (MESSAGE_CONTRACT section 1, UInt256 row). The zero value is a valid
// explicit zero and is distinct from an absent field.
type UInt256 [32]byte

// Asset identifies what an action moves. The registry is closed for the
// current schema: only the native asset exists.
type Asset uint8

const (
	// AssetUnspecified is the invalid zero value. Zero enum values are never
	// published as normalized data.
	AssetUnspecified Asset = iota
	// AssetNative is the chain's native currency in its minimal unit.
	AssetNative
)

// String returns the registry name of the asset.
func (a Asset) String() string {
	switch a {
	case AssetNative:
		return "NATIVE"
	default:
		return fmt.Sprintf("ASSET_INVALID(%d)", uint8(a))
	}
}

// IsZero reports whether the value is an explicit zero.
func (u UInt256) IsZero() bool {
	for _, b := range u {
		if b != 0 {
			return false
		}
	}
	return true
}

// Equal reports exact value equality.
func (u UInt256) Equal(o UInt256) bool { return u == o }

// Cmp compares two values: -1, 0 or 1.
func (u UInt256) Cmp(o UInt256) int {
	for i := 0; i < 32; i++ {
		switch {
		case u[i] < o[i]:
			return -1
		case u[i] > o[i]:
			return 1
		}
	}
	return 0
}

// Big returns the value as a new big.Int. The caller owns the result.
func (u UInt256) Big() *big.Int { return new(big.Int).SetBytes(u[:]) }

// Decimal returns the plain decimal representation of the value.
func (u UInt256) Decimal() string { return u.Big().String() }

// String is the same as Decimal; kept for the fmt.Stringer interface.
func (u UInt256) String() string { return u.Decimal() }

// UInt256FromBig converts a non-negative big.Int that fits into 256 bits.
func UInt256FromBig(v *big.Int) (UInt256, bool) {
	var out UInt256
	if v == nil || v.Sign() < 0 {
		return out, false
	}
	if v.BitLen() > 256 {
		return out, false
	}
	b := v.Bytes()
	copy(out[32-len(b):], b)
	return out, true
}

// ParseUInt256 parses a plain decimal string with no sign, spaces or
// separators. A leading "+" or "-" is rejected: amounts are unsigned, and a
// negative zero is forbidden by the common Delta256 rule.
func ParseUInt256(s string) (UInt256, error) {
	var out UInt256
	if s == "" || len(s) > 78 {
		return out, fmt.Errorf("balance: invalid UInt256 decimal %q", s)
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return out, fmt.Errorf("balance: invalid UInt256 decimal %q", s)
		}
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return out, fmt.Errorf("balance: invalid UInt256 decimal %q", s)
	}
	out, ok = UInt256FromBig(v)
	if !ok {
		return out, fmt.Errorf("balance: UInt256 decimal %q overflows 256 bits", s)
	}
	return out, nil
}

// MustParseUInt256 parses a literal that is known to be valid. It is meant
// for test and fixture literals and panics on invalid input.
func MustParseUInt256(s string) UInt256 {
	u, err := ParseUInt256(s)
	if err != nil {
		panic(err)
	}
	return u
}

// UInt256FromUint64 converts a small unsigned value.
func UInt256FromUint64(v uint64) UInt256 {
	var out UInt256
	b := new(big.Int).SetUint64(v).Bytes()
	copy(out[32-len(b):], b)
	return out
}

// MulUint64 multiplies the value by a small unsigned factor and fails on
// overflow past 256 bits.
func (u UInt256) MulUint64(f uint64) (UInt256, bool) {
	return UInt256FromBig(new(big.Int).Mul(u.Big(), new(big.Int).SetUint64(f)))
}

// AddChecked returns the exact sum and fails on overflow past 256 bits.
func (u UInt256) AddChecked(o UInt256) (UInt256, error) {
	sum := new(big.Int).Add(u.Big(), o.Big())
	out, ok := UInt256FromBig(sum)
	if !ok {
		return out, fmt.Errorf("balance: UInt256 addition overflow %s + %s", u.Decimal(), o.Decimal())
	}
	return out, nil
}

// SubChecked returns the exact difference and fails when o is greater than u.
func (u UInt256) SubChecked(o UInt256) (UInt256, error) {
	if u.Cmp(o) < 0 {
		return UInt256{}, fmt.Errorf("balance: UInt256 subtraction underflow %s - %s", u.Decimal(), o.Decimal())
	}
	diff := new(big.Int).Sub(u.Big(), o.Big())
	out, _ := UInt256FromBig(diff)
	return out, nil
}

// MustParseAddress parses a 20-byte address literal in hex, with or without
// the 0x prefix. It is meant for test and fixture literals and panics on
// invalid input.
func MustParseAddress(s string) Address20 {
	var out Address20
	hex := strings.TrimPrefix(s, "0x")
	if len(hex) != 40 {
		panic(fmt.Sprintf("balance: invalid Address20 literal %q", s))
	}
	for i := 0; i < 20; i++ {
		hi, ok1 := hexDigit(hex[2*i])
		lo, ok2 := hexDigit(hex[2*i+1])
		if !ok1 || !ok2 {
			panic(fmt.Sprintf("balance: invalid Address20 literal %q", s))
		}
		out[i] = hi<<4 | lo
	}
	return out
}

func hexDigit(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}
