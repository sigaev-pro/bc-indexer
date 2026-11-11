// Extractor patch test (E01-02.admission, Bor block): the chain freezer
// threshold under the FreezeLimit hook.

package rawdb

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/retention"
	"github.com/ethereum/go-ethereum/params"
)

func TestExtractorFreezeThreshold(t *testing.T) {
	db := NewMemoryDatabase()
	head := common.HexToHash("0x01")
	WriteHeadBlockHash(db, head)
	WriteHeaderNumber(db, head, params.FullImmutabilityThreshold+1000)
	f := &chainFreezer{}
	view := &nofreezedb{KeyValueStore: db}
	saved := retention.FreezeLimit
	defer func() { retention.FreezeLimit = saved }()

	retention.FreezeLimit = nil
	if got, err := f.freezeThreshold(view); err != nil || got != 1000 {
		t.Fatalf("native threshold %d %v", got, err)
	}
	var seen uint64
	var readHead bool
	retention.FreezeLimit = func(full ethdb.Database, limit uint64) (uint64, error) {
		seen = limit
		number, ok := ReadHeaderNumber(full, head)
		readHead = ok && number == params.FullImmutabilityThreshold+1000
		return 400, nil
	}
	if got, err := f.freezeThreshold(view); err != nil || got != 399 || seen != 1001 || !readHead {
		t.Fatalf("pinned limit: threshold %d, limit passed %d, head read %t, %v", got, seen, readHead, err)
	}
	retention.FreezeLimit = func(ethdb.Database, uint64) (uint64, error) { return 5000, nil }
	if got, err := f.freezeThreshold(view); err != nil || got != 1000 {
		t.Fatalf("a higher limit raised the threshold: %d %v", got, err)
	}
	retention.FreezeLimit = func(ethdb.Database, uint64) (uint64, error) { return 1001, nil }
	if got, err := f.freezeThreshold(view); err != nil || got != 1000 {
		t.Fatalf("limit equal to threshold+1: %d %v", got, err)
	}
	retention.FreezeLimit = func(ethdb.Database, uint64) (uint64, error) { return 0, nil }
	if _, err := f.freezeThreshold(view); err == nil {
		t.Fatal("a zero limit froze blocks")
	}
	failure := errors.New("ledger unreadable")
	retention.FreezeLimit = func(ethdb.Database, uint64) (uint64, error) { return 0, failure }
	if _, err := f.freezeThreshold(view); !errors.Is(err, failure) {
		t.Fatalf("ledger error: %v", err)
	}
	if _, err := f.extractorFreezeThreshold(db, 10); !errors.Is(err, errExtractorFreezeView) {
		t.Fatalf("another view: %v", err)
	}
}
