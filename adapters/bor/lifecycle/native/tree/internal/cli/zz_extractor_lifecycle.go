// Extractor patch (E01-02.lifecycle_seams, Bor block): the refusal of manual
// commands that delete or rewrite chaindata on a capture store. The
// preparation adds this file to the accepted Bor tree; see
// adapters/bor/lifecycle/native/README.md.

package cli

import "github.com/ethereum/go-ethereum/ethdb"

// ExtractorManualCommand, if set, refuses a manual command (removedb,
// purge-whitelisted-entries) on a capture store with live pins or decision
// obligations. db is the open store, or nil for a command that removes the
// store at chaindata without opening it. extractor/borlifecycle/wire installs
// it from the shipped bor main.
var ExtractorManualCommand func(command, chaindata string, db ethdb.Database) error

func extractorManualCommand(command, chaindata string, db ethdb.Database) error {
	if ExtractorManualCommand == nil {
		return nil
	}
	return ExtractorManualCommand(command, chaindata, db)
}
