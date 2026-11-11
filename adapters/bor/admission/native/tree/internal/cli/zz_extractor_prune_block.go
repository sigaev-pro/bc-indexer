// Extractor patch (E01-02.admission, Bor block): the pin ledger guard of
// `bor snapshot prune-block` (D100 item 1). The preparation adds this file to
// the accepted Bor tree; see adapters/bor/admission/native/README.md.

package cli

import (
	"fmt"

	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/node"
)

// extractorRequirePruneBlock refuses ancient block pruning while a pin of the
// pin ledger is live: the pruner deletes every frozen block below the
// reserved amount, pinned headers included. It runs before every path of the
// command (a new prune, the resumption of an interrupted one, and the
// replacement of the ancient directory). It opens the store read-only with
// freezing disabled and the ancient directory resolved as the node resolves
// it (OpenDatabaseWithOptions; the native OpenDatabaseWithFreezer of this
// source drops its ancient argument and cannot open a store whose freezer
// holds blocks). The node instance lock that the command holds keeps a
// native node from adding a pin between the check and the prune. A ledger
// that cannot be read refuses too.
func extractorRequirePruneBlock(stack *node.Node, cache, handles int, ancient string) error {
	db, err := stack.OpenDatabaseWithOptions(chaindataPath, node.DatabaseOptions{AncientsDirectory: ancient,
		Cache: cache, Handles: handles, ReadOnly: true, DisableFreeze: true})
	if err != nil {
		return fmt.Errorf("guarded block pruning: open the pin ledger: %w", err)
	}
	defer db.Close()
	if err := stateprovider.RequireNoLivePins(db); err != nil {
		return fmt.Errorf("guarded block pruning: %w", err)
	}
	return nil
}
