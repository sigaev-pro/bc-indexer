// Extractor patch (E01-02.lifecycle_seams, Bor block): the lifecycle passes of
// the whitelist (milestone, checkpoint and milestone lock). The preparation
// adds this file to the accepted Bor tree; see
// adapters/bor/lifecycle/native/README.md.

package whitelist

import (
	"sync"

	"github.com/ethereum/go-ethereum/log"
)

// ExtractorEnter, if set, opens the lifecycle pass of a mapped whitelist
// caller and returns its exit. It takes the observation guard, so a patched
// method calls it before it takes the whitelist finality lock (lock order:
// guard, then the finality lock). extractor/borlifecycle sets it for the one
// capture chain of the process; a nil hook keeps the native behavior.
var ExtractorEnter func(id string) (exit func() error, err error)

// extractorEnter opens the pass of id, or stops the process when the gate
// refuses it: the whitelist change fails closed.
func extractorEnter(id string) func() {
	if ExtractorEnter == nil {
		return func() {}
	}
	exit, err := ExtractorEnter(id)
	if err != nil {
		log.Crit("Extractor lifecycle refused a whitelist change", "caller", id, "err", err)
	}
	return func() {
		if err := exit(); err != nil {
			log.Crit("Extractor lifecycle could not close a whitelist pass", "caller", id, "err", err)
		}
	}
}

// extractorVotes holds the pass that LockMutex opened until UnlockMutex
// closes it: the vote keeps the finality lock across both calls.
var extractorVotes sync.Map

func (m *milestone) extractorVoteEnter() {
	extractorVotes.Store(m, extractorEnter("bor.milestone-lock"))
}

func (m *milestone) extractorVoteExit() {
	if exit, ok := extractorVotes.LoadAndDelete(m); ok {
		exit.(func())()
	}
}
