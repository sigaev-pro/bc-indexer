// Package borwire installs the retention hooks in the shipped Bor binary:
// cmd/cli/main.go imports it for its side effect. Each hook calls the pin
// ledger guard of extractor/state_provider (E01-02.pins).
//
// Extractor patch (E01-02.admission, Bor block); see
// adapters/bor/admission/native/README.md.
package borwire

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/retention"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
)

func init() {
	retention.RecoverPruning = RecoverPruning
	retention.RequireRewindAllowed = stateprovider.RequireRewindAllowed
	retention.FreezeLimit = stateprovider.FreezeLimit
}

// PendingPrune reports whether datadir holds a state bloom filter of an
// interrupted offline prune, found the way the native pruner finds it
// (core/state/pruner findBloomFilter walks the whole directory and accepts
// "statebloom.<root>.bf.gz").
func PendingPrune(datadir string) (bool, error) {
	found := false
	err := filepath.WalkDir(datadir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if !entry.IsDir() && strings.HasPrefix(name, "statebloom.") && strings.HasSuffix(name, ".bf.gz") {
			found = true
		}
		return nil
	})
	return found, err
}

// RecoverPruning resumes an interrupted offline prune only through the
// guarded state_provider RecoverPruning, which refuses while a pin is live.
// Without a pending prune the native recovery deletes nothing and live pins
// do not stop the node.
func RecoverPruning(datadir string, db ethdb.Database) error {
	pending, err := PendingPrune(datadir)
	if err != nil || !pending {
		return err
	}
	return stateprovider.RecoverPruning(db, datadir)
}
