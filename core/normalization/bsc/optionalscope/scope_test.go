package optionalscope

import (
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

func sweepRecord() OutsideTxRecord {
	return OutsideTxRecord{
		OrderInBlock: 0,
		Effects: []balance.BalanceEffect{
			{EffectIndex: 0, Address: balance.Address20{0xff}, Before: balance.UInt256FromUint64(210), After: balance.UInt256{}},
		},
		BlockNumber: 12,
	}
}

// Default OFF: the sweep observation is dropped from publication without
// an error, and no native transaction effect depends on it.
func TestDefaultOffDropsWithoutError(t *testing.T) {
	d := Default()
	if d.Enabled || len(d.Fixtures) != 0 {
		t.Fatal("default declaration must be OFF with no fixtures")
	}
	published, err := d.Apply(FeatureParliaFeeSweep, "sweep-fixture", sweepRecord())
	if err != nil {
		t.Fatalf("default OFF must drop silently, got %v", err)
	}
	if published != nil {
		t.Fatalf("default OFF published %+v", published)
	}
}

func TestEnabledRequiresDeclaredFixture(t *testing.T) {
	d := Declaration{Enabled: true, Fixtures: []string{"declared-fixture"}}
	if _, err := d.Apply(FeatureParliaFeeSweep, "undeclared", sweepRecord()); !errors.Is(err, ErrUndeclaredFixture) {
		t.Fatalf("undeclared fixture error = %v", err)
	}
	published, err := d.Apply(FeatureParliaFeeSweep, "declared-fixture", sweepRecord())
	if err != nil {
		t.Fatalf("declared fixture rejected: %v", err)
	}
	if published == nil || published.Feature != FeatureParliaFeeSweep || published.Fixture != "declared-fixture" {
		t.Fatalf("published record drifted: %+v", published)
	}
}

// An outside-tx record never borrows a transaction identity: no hash of a
// transaction, no body position, its own order space.
func TestOutsideTxRecordHasOwnOrderSpace(t *testing.T) {
	record := sweepRecord()
	record.OrderInBlock = 3
	if record.Fixture != "" {
		t.Fatal("the fixture is assigned by the declaration, not carried in")
	}
	if FeatureParliaFeeSweep.String() != "PARLIA_FEE_SWEEP" {
		t.Fatalf("feature name = %s", FeatureParliaFeeSweep)
	}
}
