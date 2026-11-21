package balance

import (
	"testing"
)

func TestParseUInt256RoundTrip(t *testing.T) {
	cases := []string{
		"0",
		"1",
		"42",
		"10000000000000000000",
		"115792089237316195423570985008687907853269984665640564039457584007913129639935", // 2^256-1
	}
	for _, c := range cases {
		u, err := ParseUInt256(c)
		if err != nil {
			t.Fatalf("ParseUInt256(%q): %v", c, err)
		}
		if got := u.Decimal(); got != c {
			t.Fatalf("round trip of %q gave %q", c, got)
		}
	}
}

func TestParseUInt256Rejects(t *testing.T) {
	bad := []string{
		"",
		"-1",
		"+1",
		"1 ",
		" 1",
		"0x10",
		"1_000",
		"115792089237316195423570985008687907853269984665640564039457584007913129639936",   // 2^256
		"99999999999999999999999999999999999999999999999999999999999999999999999999999999", // > 78 digits
	}
	for _, c := range bad {
		if _, err := ParseUInt256(c); err == nil {
			t.Fatalf("ParseUInt256(%q) unexpectedly succeeded", c)
		}
	}
}

func TestUInt256CheckedArithmetic(t *testing.T) {
	max := MustParseUInt256("115792089237316195423570985008687907853269984665640564039457584007913129639935")
	one := UInt256FromUint64(1)

	if _, err := max.AddChecked(one); err == nil {
		t.Fatal("addition past 2^256-1 must fail")
	}
	sum, err := MustParseUInt256("1000000000000000000").AddChecked(MustParseUInt256("1"))
	if err != nil || sum.Decimal() != "1000000000000000001" {
		t.Fatalf("exact addition failed: %s %v", sum.Decimal(), err)
	}
	if _, err := UInt256FromUint64(5).SubChecked(UInt256FromUint64(6)); err == nil {
		t.Fatal("subtraction underflow must fail")
	}
	diff, err := MustParseUInt256("10000000000000000000000000005").SubChecked(UInt256FromUint64(5))
	if err != nil || diff.Decimal() != "10000000000000000000000000000" {
		t.Fatalf("exact subtraction failed: %s %v", diff.Decimal(), err)
	}
	mul, ok := MustParseUInt256("2000000000").MulUint64(21000)
	if !ok || mul.Decimal() != "42000000000000" {
		t.Fatalf("exact multiplication failed: %s %v", mul.Decimal(), ok)
	}
	if v := UInt256FromUint64(0); !v.IsZero() {
		t.Fatal("zero value must report IsZero")
	}
}

func TestMustParseAddress(t *testing.T) {
	a := MustParseAddress("7e5f4552091a69125d5dfcb7b8c2659029395bdf")
	b := MustParseAddress("0x7e5f4552091a69125d5dfcb7b8c2659029395bdf")
	if a != b {
		t.Fatal("0x prefix must not change the parsed address")
	}
	if a[0] != 0x7e || a[19] != 0xdf {
		t.Fatalf("wrong bytes: %x", a)
	}
}

func TestMustParseAddressPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("invalid literal must panic")
		}
	}()
	MustParseAddress("7e5f")
}

func TestDelta(t *testing.T) {
	e := BalanceEffect{
		Before: MustParseUInt256("10"),
		After:  MustParseUInt256("25"),
	}
	sign, mag := e.Delta()
	if sign != 1 || mag.Decimal() != "15" {
		t.Fatalf("delta of +15 gave %d %s", sign, mag.Decimal())
	}
	e.Before, e.After = e.After, e.Before
	sign, mag = e.Delta()
	if sign != -1 || mag.Decimal() != "15" {
		t.Fatalf("delta of -15 gave %d %s", sign, mag.Decimal())
	}
	e.After = e.Before
	sign, mag = e.Delta()
	if sign != 0 || !mag.IsZero() {
		t.Fatalf("zero delta gave %d %s", sign, mag.Decimal())
	}
}
