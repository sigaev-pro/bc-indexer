// Extractor patch (E01-02.lifecycle_seams, Bor block): the milestone
// reorganization of a capture node. The preparation adds this file to the
// accepted Bor tree; see adapters/bor/lifecycle/native/README.md.

package eth

import (
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// extractorReorgToFinalized is reorgToFinalized on a capture chain: the
// rewind and the activation of the stored canonical chain as one decision of
// bor.milestone-reorg under one chain lock (core.ExtractorReorgToFinalized).
// It reports whether the head moved; a refusal moves nothing.
func extractorReorgToFinalized(eth *Ethereum, head uint64, rewindTo uint64, canonicalChain []*types.Block) bool {
	eth.handler.downloader.Cancel()
	if err := eth.blockchain.ExtractorReorgToFinalized(rewindTo, canonicalChain); err != nil {
		log.Error("Extractor milestone reorganization failed closed", "head", head, "rewindTo", rewindTo, "err", err)
		return false
	}
	rewindLengthMeter.Mark(int64(head - rewindTo))
	return true
}

// ExtractorReorgToFinalized runs the native reorgToFinalized of eth. The
// lifecycle probe calls it to exercise the milestone reorganization of the
// whitelist verifier without a Heimdall milestone conflict.
func ExtractorReorgToFinalized(eth *Ethereum, head uint64, rewindTo uint64, canonicalChain []*types.Block) bool {
	return reorgToFinalized(eth, head, rewindTo, canonicalChain)
}
