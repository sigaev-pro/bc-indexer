// Extractor patch test (E01-02.admission, Bor block): the state of the
// observation window gate (D100 item 1). Standard library only, so the
// mutants of gate.mutants.json run it outside the Bor graph.

package bor

import (
	"testing"
	"time"
)

var (
	observedBlock = [32]byte{0x0a}
	otherBlock    = [32]byte{0x0b}
)

// enterFrom enters g from a new goroutine and reports on the returned
// channel once it is admitted; the goroutine leaves when release closes.
func enterFrom(g *extractorGateState, release <-chan struct{}) <-chan struct{} {
	admitted := make(chan struct{})
	go func() {
		leave := g.enter(extractorGoroutine())
		close(admitted)
		<-release
		leave()
	}()
	return admitted
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// within runs f on another goroutine and fails the test if f does not return
// in time; it returns the value f panicked with, if any.
func within(t *testing.T, what string, f func()) (panicked any) {
	t.Helper()
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		f()
	}()
	select {
	case panicked = <-done:
		return panicked
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return", what)
		return nil
	}
}

func TestExtractorGoroutineIDs(t *testing.T) {
	var id uint64
	if p := within(t, "reading the goroutine ID", func() { id = extractorGoroutine() }); p != nil {
		t.Fatalf("reading the goroutine ID panicked: %v", p)
	}
	id = extractorGoroutine()
	if id == 0 || extractorGoroutine() != id {
		t.Fatalf("goroutine ID %d is not stable", id)
	}
	other := make(chan uint64)
	go func() { other <- extractorGoroutine() }()
	if got := <-other; got == id || got == 0 {
		t.Fatalf("another goroutine has ID %d, this one %d", got, id)
	}
}

func TestExtractorGateOwnerPassesOthersWait(t *testing.T) {
	g := newExtractorGate()
	owner := extractorGoroutine()
	g.open(owner, observedBlock)
	// The owner passes its own gated calls, nested too, without waiting.
	within(t, "a gated call of the owner", func() {
		leave := g.enter(owner)
		g.enter(owner)()
		leave()
	})
	release := make(chan struct{})
	defer close(release)
	admitted := enterFrom(g, release)
	waitFor(t, "a waiting caller", func() bool { return g.waiters() == 1 })
	time.Sleep(20 * time.Millisecond)
	if isClosed(admitted) {
		t.Fatal("another goroutine entered while the window was open")
	}
	g.close(owner)
	waitFor(t, "the held-back caller", func() bool { return isClosed(admitted) })
	if g.waiters() != 0 {
		t.Fatalf("%d waiters after the window closed", g.waiters())
	}
}

func TestExtractorGateOpenWaitsForGatedCalls(t *testing.T) {
	g := newExtractorGate()
	releaseFirst := make(chan struct{})
	first := enterFrom(g, releaseFirst)
	waitFor(t, "the first caller", func() bool { return isClosed(first) })

	opened := make(chan struct{})
	var opener uint64
	go func() {
		opener = extractorGoroutine()
		g.open(opener, observedBlock)
		close(opened)
	}()
	waitFor(t, "the window to start opening", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.opening
	})
	// A new caller waits while the window opens.
	releaseLate := make(chan struct{})
	late := enterFrom(g, releaseLate)
	waitFor(t, "the late caller to wait", func() bool { return g.waiters() == 1 })
	time.Sleep(20 * time.Millisecond)
	if isClosed(opened) || isClosed(late) {
		t.Fatalf("opened %t, late caller admitted %t while a gated call was in progress", isClosed(opened), isClosed(late))
	}
	close(releaseFirst)
	waitFor(t, "the window", func() bool { return isClosed(opened) })
	time.Sleep(20 * time.Millisecond)
	if isClosed(late) {
		t.Fatal("the late caller entered the open window")
	}
	g.close(opener)
	waitFor(t, "the late caller", func() bool { return isClosed(late) })
	close(releaseLate)
}

func TestExtractorGateNestedCallsDoNotWaitForAnOpeningWindow(t *testing.T) {
	g := newExtractorGate()
	nested := make(chan struct{})
	release := make(chan struct{})
	go func() {
		id := extractorGoroutine()
		leave := g.enter(id)
		<-release
		// A window now waits for this call; the nested call must pass.
		g.enter(id)()
		close(nested)
		leave()
	}()
	waitFor(t, "the outer call", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return len(g.holders) == 1
	})
	opened := make(chan struct{})
	var opener uint64
	go func() {
		opener = extractorGoroutine()
		g.open(opener, observedBlock)
		close(opened)
	}()
	waitFor(t, "the window to start opening", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.opening
	})
	close(release)
	waitFor(t, "the nested call and the window", func() bool { return isClosed(nested) && isClosed(opened) })
	g.close(opener)
}

func TestExtractorGateMisuse(t *testing.T) {
	expectPanic := func(name string, f func()) {
		t.Helper()
		if within(t, name, f) == nil {
			t.Fatalf("%s did not panic", name)
		}
	}
	g := newExtractorGate()
	id := extractorGoroutine()
	expectPanic("close without a window", func() { g.close(id) })
	leave := g.enter(id)
	expectPanic("open inside a gated call", func() { g.open(id, observedBlock) })
	leave()
	g.open(id, observedBlock)
	expectPanic("a second open by the owner", func() { g.open(id, observedBlock) })
	expectPanic("close by another goroutine", func() { g.close(id + 1) })
	g.close(id)
	g2 := newExtractorGate()
	other := make(chan struct{})
	var owner2 uint64
	go func() {
		defer close(other)
		owner2 = extractorGoroutine()
		g2.open(owner2, observedBlock)
	}()
	<-other
	var leaveImport func()
	within(t, "Finalize of the observed block by the import", func() { leaveImport = g2.enterBlock(id, owner2, observedBlock) })
	expectPanic("open inside the import", func() { g2.open(id, otherBlock) })
	leaveImport()
	g2.close(owner2)
}

func TestExtractorCreator(t *testing.T) {
	parent := extractorGoroutine()
	got := make(chan uint64)
	go func() { got <- extractorCreator() }()
	if creator := <-got; creator != parent {
		t.Fatalf("creator %d, expected %d", creator, parent)
	}
	// A goroutine of a goroutine names its own creator, not the grandparent.
	go func() {
		child := extractorGoroutine()
		inner := make(chan uint64)
		go func() { inner <- extractorCreator() }()
		got <- child - (<-inner) // 0 when the inner creator is the child
	}()
	if diff := <-got; diff != 0 {
		t.Fatalf("grandchild creator differs from its parent by %d", diff)
	}
}

func TestExtractorGateWaitsForDetachedFetches(t *testing.T) {
	g := newExtractorGate()
	done := g.trackFetch()
	drained := make(chan struct{})
	go func() {
		g.drainFetches()
		close(drained)
	}()
	time.Sleep(20 * time.Millisecond)
	if isClosed(drained) {
		t.Fatal("the window did not wait for a detached fetch")
	}
	done()
	waitFor(t, "the drain", func() bool { return isClosed(drained) })
	g.drainFetches()
}

func TestExtractorGateFinalizeOfTheObservedBlockIsTheImport(t *testing.T) {
	g := newExtractorGate()
	owner := extractorGoroutine()
	g.open(owner, observedBlock)
	// Native ProcessBlock finalizes on a goroutine of its own.
	imported := make(chan struct{})
	release := make(chan struct{})
	go func() {
		id := extractorGoroutine()
		leave := g.enterBlock(id, owner, observedBlock)
		// CommitStates and FetchAndCommitSpan nested in Finalize pass too.
		g.enter(id)()
		close(imported)
		<-release
		leave()
	}()
	waitFor(t, "the import's Finalize", func() bool { return isClosed(imported) })
	// Finalize of another block, outside the import, waits.
	other := make(chan struct{})
	go func() {
		leave := g.enterBlock(extractorGoroutine(), owner, otherBlock)
		close(other)
		leave()
	}()
	// Finalize of the observed block on a goroutine that the owner did not
	// start (state regeneration for tracing) waits too.
	foreign := make(chan struct{})
	go func() {
		leave := g.enterBlock(extractorGoroutine(), owner+1000, observedBlock)
		close(foreign)
		leave()
	}()
	waitFor(t, "the other Finalize calls to wait", func() bool { return g.waiters() == 2 })
	time.Sleep(20 * time.Millisecond)
	if isClosed(other) || isClosed(foreign) {
		t.Fatalf("a non-import Finalize ran inside the window: other block %t, foreign goroutine %t",
			isClosed(other), isClosed(foreign))
	}
	close(release)
	waitFor(t, "the import to leave", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return len(g.importers) == 0
	})
	// Once the import left, its goroutine is no longer the import.
	g.close(owner)
	waitFor(t, "the other Finalize calls", func() bool { return isClosed(other) && isClosed(foreign) })
	// Without a window, Finalize of that block enters like any gated call.
	id := extractorGoroutine()
	leave := g.enterBlock(id, owner, observedBlock)
	g.mu.Lock()
	holders, importers := g.holders[id], len(g.importers)
	g.mu.Unlock()
	leave()
	if holders != 1 || importers != 0 {
		t.Fatalf("Finalize without a window: holders %d importers %d", holders, importers)
	}
}

func TestExtractorGateOneWindowAtATime(t *testing.T) {
	g := newExtractorGate()
	first := extractorGoroutine()
	g.open(first, observedBlock)
	second := make(chan struct{})
	var opener uint64
	go func() {
		opener = extractorGoroutine()
		g.open(opener, otherBlock)
		close(second)
	}()
	time.Sleep(20 * time.Millisecond)
	if isClosed(second) {
		t.Fatal("a second window opened while the first was open")
	}
	g.close(first)
	waitFor(t, "the second window", func() bool { return isClosed(second) })
	g.mu.Lock()
	block := g.block
	g.mu.Unlock()
	if block != otherBlock {
		t.Fatalf("the second window observes %x", block)
	}
	g.close(opener)
}
