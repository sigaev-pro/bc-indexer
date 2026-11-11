// Extractor patch (E01-02.admission, Bor block): the chain freezer
// threshold passes the pin ledger guard FreezeLimit (D98 item 2). The
// preparation adds this file to the accepted Bor tree; see
// adapters/bor/admission/native/README.md.

package rawdb

import (
	"errors"

	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/retention"
)

var errExtractorFreezeView = errors.New("extractor freeze limit needs the freezer key-value view")

// extractorFreezeThreshold lowers the inclusive native threshold to the pin
// ledger limit. FreezeLimit takes and returns exclusive limits, so the
// result is min(threshold, limit-1). A limit of 0 has no inclusive
// threshold, so it is an error and the freezer freezes nothing.
// FreezeLimit reads canonical hashes and headers that may already be frozen,
// so it reads through the key-value store together with this freezer.
func (f *chainFreezer) extractorFreezeThreshold(db ethdb.KeyValueReader, threshold uint64) (uint64, error) {
	if retention.FreezeLimit == nil {
		return threshold, nil
	}
	kv, ok := db.(*nofreezedb)
	if !ok {
		return 0, errExtractorFreezeView
	}
	limit, err := retention.FreezeLimit(&freezerdb{KeyValueStore: kv.KeyValueStore, chainFreezer: f}, threshold+1)
	if err != nil {
		return 0, err
	}
	if limit == 0 {
		return 0, errors.New("freezing threshold is not available: the pin ledger limit is 0")
	}
	return min(threshold, limit-1), nil
}
