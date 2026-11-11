// Package retention holds the hooks through which the shipped Bor binary
// calls the pin ledger guards of E01-02.pins (D98 item 2). Native packages
// (eth, core, core/rawdb) cannot import extractor/state_provider without an
// import cycle, so they call these hooks; extractor/borwire installs them
// from the shipped `bor` main. A nil hook keeps the native behavior, which
// only binaries without the wiring (tests, tools) see.
//
// Extractor patch (E01-02.admission, Bor block); see
// adapters/bor/admission/native/README.md.
package retention

import "github.com/ethereum/go-ethereum/ethdb"

var (
	// RecoverPruning replaces the startup recovery of an interrupted offline
	// prune. An error stops the node.
	RecoverPruning func(datadir string, db ethdb.Database) error
	// RequireRewindAllowed refuses a head rewind that would delete a pinned
	// header (SetHead, debug_setHead).
	RequireRewindAllowed func(db ethdb.Database, newHead uint64) error
	// FreezeLimit returns the highest exclusive block number that the chain
	// freezer may freeze, given its own exclusive limit.
	FreezeLimit func(db ethdb.Database, limit uint64) (uint64, error)
)

// Installed reports whether every hook is set.
func Installed() bool {
	return RecoverPruning != nil && RequireRewindAllowed != nil && FreezeLimit != nil
}
