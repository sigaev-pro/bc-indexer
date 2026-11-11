// Extractor patch (E01-02.admission, Bor block): the observation window gate
// bound to the Bor engine (D98, D100 item 1); the gate state is in
// zz_extractor_gate_core.go. The preparation adds this file to the accepted
// Bor tree; see adapters/bor/admission/native/README.md.

package bor

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// extractorGate passes the calling goroutine through the window gate and
// returns the release function. The patched entry points call it first.
func (c *Bor) extractorGate() func() {
	return extractorWindow.enter(extractorGoroutine())
}

// extractorBlockGate is the gate of Finalize: Finalize of the observed block
// by the import passes.
func (c *Bor) extractorBlockGate(header *types.Header) func() {
	return extractorWindow.enterBlock(extractorGoroutine(), extractorCreator(), [32]byte(header.Hash()))
}

// extractorTrackFetch counts a detached span range fetch until the returned
// function runs. The patched spanRangeById counts each lookup before its
// singleflight DoChan and releases it when the fetch result is delivered, to
// the caller or, after the caller left on its context, to a goroutine that
// waits for it; a fetch is therefore counted from before it can start.
func extractorTrackFetch() func() { return extractorWindow.trackFetch() }

// ExtractorFetchSpanRange is test support for the probe: it runs the native
// span range lookup of span id (spanRangeById) through the window gate with
// ctx, as waitForNewSpan does after its timeout. With an expired ctx it
// returns at once, while the detached fetch it started goes on.
func (c *Bor) ExtractorFetchSpanRange(ctx context.Context, id uint64) error {
	defer c.extractorGate()()
	_, err := c.spanStore.spanRangeById(ctx, id)
	return err
}

// ExtractorOpenWindow starts the observation window of block, owned by the
// calling goroutine: it waits for the gated calls in progress to return,
// holds back every gated call other than the import of block, drops the
// cached Heimdall data and snapshots (which stops the poll loop and the
// range warmup), and then waits for the detached span range fetches still in
// flight. Only then may the caller route counted reads to the log.
func (c *Bor) ExtractorOpenWindow(block common.Hash) {
	extractorWindow.open(extractorGoroutine(), [32]byte(block))
	c.ExtractorPurgeInputs()
	extractorWindow.drainFetches()
}

// ExtractorCloseWindow ends the window that the calling goroutine opened and
// lets the held-back calls continue.
func (c *Bor) ExtractorCloseWindow() { extractorWindow.close(extractorGoroutine()) }

// ExtractorGateWaiting returns the number of goroutines that wait at the gate
// for the open window to close.
func ExtractorGateWaiting() int { return extractorWindow.waiters() }
