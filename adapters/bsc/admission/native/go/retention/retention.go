// Package retention registers the pin ledger guards of E01-02.pins on the
// native rewind and freezer paths of the BSC client (D98 item 2).
//
// The patched cmd/geth imports this package for its side effect, so the
// guards are set by an init function, before main opens any store. The
// native core and core/rawdb packages cannot import the pin ledger (it
// imports eth/ethconfig, which imports core), so the patch set declares
// function variables there and this package fills them. The other two
// guards, PruneState and RecoverPruning, are called directly by the patched
// cmd/geth and eth packages.
package retention

import (
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
)

func init() { Install() }

// Install sets the rewind guard and the freezer limit. It is idempotent.
func Install() {
	core.ExtractorRewindGuard = stateprovider.RequireRewindAllowed
	rawdb.ExtractorFreezeLimit = stateprovider.FreezeLimit
}
