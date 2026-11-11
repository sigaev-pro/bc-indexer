// Extractor patch (E01-02.admission, Bor block): the observation window gate
// (D98, D100 item 1). The preparation adds this file to the accepted Bor
// tree; see adapters/bor/admission/native/README.md.

package bor

import (
	"bytes"
	"fmt"
	"runtime"
	"strconv"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// extractorGate is the observation window gate. While a window is open, only
// the import of the observed block runs the gated engine entry points; every
// other caller waits at the gate until the window closes, so no Heimdall
// read of another component enters the observation's dependency log and none
// of them fills the caches of the window. A window opens only after every
// gated call in progress has returned, so no read started before it can land
// inside it.
//
// The import is the goroutine that opened the window (the capsule header
// check runs there) and every goroutine inside Finalize of the observed
// block: native ProcessBlock runs its processors on goroutines of their own,
// so Finalize is recognized by the block it finalizes. Calls nested in an
// admitted call pass too.
//
// The gated entry points are every exported Bor and API method that can
// reach a counted Heimdall read: header verification, Prepare, Finalize and
// the assembly variants, Seal, IsAuthorizedSigner, CalcDifficulty,
// CommitStates, FetchAndCommitSpan, and every method of the bor RPC API
// (zz_extractor_gate_test.go checks the list against the source). The
// window owner passes its own gated calls (Finalize of the observed block).
// A goroutine already inside a gated call passes nested ones, so the gate
// never waits for itself.
type extractorGateState struct {
	mu        sync.Mutex
	changed   *sync.Cond
	owner     uint64         // goroutine of the open window; 0 if none
	block     common.Hash    // the observed block of the open window
	opening   bool           // a window waits for the gated calls in progress
	holders   map[uint64]int // goroutines inside gated calls, by nesting depth
	importers map[uint64]int // goroutines inside Finalize of the observed block
	waiting   int            // goroutines waiting for a window to close
}

func newExtractorGate() *extractorGateState {
	g := &extractorGateState{holders: map[uint64]int{}, importers: map[uint64]int{}}
	g.changed = sync.NewCond(&g.mu)
	return g
}

var extractorWindow = newExtractorGate()

// extractorGoroutine returns the ID of the calling goroutine, the first line
// of its stack trace ("goroutine <id> [...").
func extractorGoroutine() uint64 {
	var buf [64]byte
	fields := bytes.Fields(buf[:runtime.Stack(buf[:], false)])
	if len(fields) < 2 || string(fields[0]) != "goroutine" {
		panic(fmt.Sprintf("extractor window gate: unexpected stack header %q", buf[:]))
	}
	id, err := strconv.ParseUint(string(fields[1]), 10, 64)
	if err != nil || id == 0 {
		panic(fmt.Sprintf("extractor window gate: goroutine ID %q", fields[1]))
	}
	return id
}

// enter admits goroutine id to a gated call and returns the release function.
func (g *extractorGateState) enter(id uint64) func() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if id == g.owner || g.importers[id] != 0 {
		return func() {}
	}
	if g.holders[id] == 0 && (g.owner != 0 || g.opening) {
		g.waiting++
		for g.owner != 0 || g.opening {
			g.changed.Wait()
		}
		g.waiting--
	}
	g.holders[id]++
	return func() { g.leave(id) }
}

// enterBlock admits goroutine id to Finalize of block. Finalize of the
// observed block of the open window is the import and passes; any other call
// enters like every gated call.
func (g *extractorGateState) enterBlock(id uint64, block common.Hash) func() {
	g.mu.Lock()
	if g.owner != 0 && block == g.block && g.holders[id] == 0 {
		g.importers[id]++
		g.mu.Unlock()
		return func() { g.leaveImport(id) }
	}
	g.mu.Unlock()
	return g.enter(id)
}

func (g *extractorGateState) leaveImport(id uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.importers[id]--; g.importers[id] == 0 {
		delete(g.importers, id)
	}
}

func (g *extractorGateState) leave(id uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.holders[id]--; g.holders[id] == 0 {
		delete(g.holders, id)
	}
	g.changed.Broadcast()
}

// open makes goroutine id the owner of a window for block once every gated
// call in progress has returned. A window opened from inside a gated call
// would wait for itself, so it panics instead.
func (g *extractorGateState) open(id uint64, block common.Hash) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.holders[id] != 0 || g.importers[id] != 0 || g.owner == id {
		panic("extractor window gate: window opened inside a gated call")
	}
	for g.owner != 0 || g.opening {
		g.changed.Wait()
	}
	g.opening = true
	for len(g.holders) != 0 {
		g.changed.Wait()
	}
	g.opening, g.owner, g.block = false, id, block
	g.changed.Broadcast()
}

func (g *extractorGateState) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.owner == 0 {
		panic("extractor window gate: no open window")
	}
	g.owner, g.block = 0, common.Hash{}
	g.changed.Broadcast()
}

func (g *extractorGateState) waiters() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.waiting
}

// extractorGate passes the calling goroutine through the window gate and
// returns the release function. The patched entry points call it first.
func (c *Bor) extractorGate() func() {
	return extractorWindow.enter(extractorGoroutine())
}

// extractorBlockGate is the gate of Finalize: Finalize of the observed block
// is the import and passes.
func (c *Bor) extractorBlockGate(header *types.Header) func() {
	return extractorWindow.enterBlock(extractorGoroutine(), header.Hash())
}

// ExtractorOpenWindow starts the observation window of block, owned by the
// calling goroutine: it waits for the gated calls in progress to return,
// holds back every gated call other than the import of block, and drops the
// cached Heimdall data and snapshots.
func (c *Bor) ExtractorOpenWindow(block common.Hash) {
	extractorWindow.open(extractorGoroutine(), block)
	c.ExtractorPurgeInputs()
}

// ExtractorCloseWindow ends the window that ExtractorOpenWindow started and
// lets the held-back calls continue.
func (c *Bor) ExtractorCloseWindow() { extractorWindow.close() }

// ExtractorGateWaiting returns the number of goroutines that wait at the gate
// for the open window to close.
func ExtractorGateWaiting() int { return extractorWindow.waiters() }
