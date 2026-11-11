// Extractor patch test (E01-02.admission, Bor block): the observation window
// gate (D100 item 1).

package bor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

var (
	observedBlock = common.HexToHash("0x0a")
	otherBlock    = common.HexToHash("0x0b")
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

func TestExtractorGoroutineIDs(t *testing.T) {
	id := extractorGoroutine()
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
	leave := g.enter(owner)
	g.enter(owner)()
	leave()
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
		defer func() {
			if recover() == nil {
				t.Fatalf("%s did not panic", name)
			}
		}()
		f()
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
	leaveImport := g2.enterBlock(id, owner2, observedBlock)
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

func TestExtractorBlockGateHashesTheHeader(t *testing.T) {
	header := &types.Header{Number: big.NewInt(7)}
	c := &Bor{}
	saved := extractorWindow
	extractorWindow = newExtractorGate()
	defer func() { extractorWindow = saved }()
	owner := extractorGoroutine()
	extractorWindow.open(owner, header.Hash())
	// A goroutine that the owner starts, as ProcessBlock does, is the import.
	done := make(chan struct{})
	go func() {
		c.extractorBlockGate(header)()
		close(done)
	}()
	waitFor(t, "Finalize of the observed header", func() bool { return isClosed(done) })
	extractorWindow.close(owner)
}

// gatedBor lists the exported Bor methods that can reach a counted Heimdall
// read (a span, the latest span, the status or a state sync page) and must
// pass the gate. ungatedBor lists the others; a new native method fails the
// test until it is classified.
var (
	gatedBor = []string{"CalcDifficulty", "CommitStates", "FetchAndCommitSpan", "Finalize", "FinalizeAndAssemble",
		"FinalizeAndAssembleForSimulation", "FinalizeForPipeline", "IsAuthorizedSigner", "Prepare",
		"SealWithStopHook", "VerifyHeader", "VerifyHeaders", "VerifySeal"}
	ungatedBor = []string{
		"APIs", "AssembleBlock", "Author", "Authorize", "Close", "CurrentSigner", "EarliestAnnounceTime",
		"GetCurrentValidators", "GetSpanner", "PurgeCache", "Seal", "SealHash", "SetHeimdallClient", "SetSpanner",
		"SignBytes", "VerifyUncles"}
)

func gateCall(stmt ast.Stmt, receiver string) bool {
	var call *ast.CallExpr
	switch s := stmt.(type) {
	case *ast.DeferStmt:
		call = s.Call
	case *ast.AssignStmt:
		if len(s.Rhs) == 1 {
			call, _ = s.Rhs[0].(*ast.CallExpr)
		}
	}
	if call == nil {
		return false
	}
	if inner, ok := call.Fun.(*ast.CallExpr); ok {
		call = inner
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (selector.Sel.Name != "extractorGate" && selector.Sel.Name != "extractorBlockGate") {
		return false
	}
	var text strings.Builder
	var walk func(ast.Expr)
	walk = func(e ast.Expr) {
		switch x := e.(type) {
		case *ast.Ident:
			text.WriteString(x.Name)
		case *ast.SelectorExpr:
			walk(x.X)
			text.WriteString("." + x.Sel.Name)
		}
	}
	walk(selector.X)
	return text.String() == receiver
}

func TestExtractorGateCoversEveryHeimdallEntryPoint(t *testing.T) {
	fset := token.NewFileSet()
	methods := map[string]map[string]*ast.FuncDecl{"Bor": {}, "API": {}}
	for _, file := range []string{"bor.go", "api.go"} {
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() {
				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			if ident, ok := star.X.(*ast.Ident); ok && methods[ident.Name] != nil {
				methods[ident.Name][fn.Name.Name] = fn
			}
		}
	}
	var listed []string
	listed = append(append(listed, gatedBor...), ungatedBor...)
	sort.Strings(listed)
	var found []string
	for name := range methods["Bor"] {
		found = append(found, name)
	}
	sort.Strings(found)
	if strings.Join(found, ",") != strings.Join(listed, ",") {
		t.Fatalf("exported Bor methods %v differ from the classified ones %v", found, listed)
	}
	for _, name := range gatedBor {
		fn := methods["Bor"][name]
		if name == "VerifyHeaders" {
			// Gated per header inside its verification goroutine.
			gated := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if stmt, ok := n.(ast.Stmt); ok && gateCall(stmt, "c") {
					gated = true
				}
				return true
			})
			if !gated {
				t.Errorf("VerifyHeaders does not pass the gate")
			}
			continue
		}
		if len(fn.Body.List) == 0 || !gateCall(fn.Body.List[0], "c") {
			t.Errorf("Bor.%s does not pass the gate first", name)
		}
		if name == "Finalize" {
			// The import is recognized by the block it finalizes.
			call := fn.Body.List[0].(*ast.DeferStmt).Call.Fun.(*ast.CallExpr)
			if selector := call.Fun.(*ast.SelectorExpr); selector.Sel.Name != "extractorBlockGate" ||
				len(call.Args) != 1 || call.Args[0].(*ast.Ident).Name != "header" {
				t.Errorf("Finalize does not pass the block gate with its header")
			}
		}
	}
	if len(methods["API"]) == 0 {
		t.Fatal("no bor API methods found")
	}
	for name, fn := range methods["API"] {
		if len(fn.Body.List) == 0 || !gateCall(fn.Body.List[0], "api.bor") {
			t.Errorf("API.%s does not pass the gate first", name)
		}
	}
}
