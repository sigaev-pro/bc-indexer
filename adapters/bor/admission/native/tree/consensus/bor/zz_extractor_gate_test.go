// Extractor patch test (E01-02.admission, Bor block): the observation window
// gate bound to the Bor engine (D100 item 1).

package bor

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
)

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
	// The probe's test hook into the native lookup enters like any caller.
	hook, err := parser.ParseFile(fset, "zz_extractor_gate.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	hooked := false
	for _, decl := range hook.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "ExtractorFetchSpanRange" {
			hooked = len(fn.Body.List) > 0 && gateCall(fn.Body.List[0], "c")
		}
	}
	if !hooked {
		t.Errorf("ExtractorFetchSpanRange does not pass the gate first")
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

// TestExtractorSpanRangeFetchIsCountedFromTheStart drives the native lookup
// with an expired context, as waitForNewSpan does after its timeout: the
// lookup returns at once, the detached fetch it started is already counted,
// and a window cannot finish opening until its result is delivered.
func TestExtractorSpanRangeFetchIsCountedFromTheStart(t *testing.T) {
	saved := extractorWindow
	extractorWindow = newExtractorGate()
	defer func() { extractorWindow = saved }()
	client := newCountingClient(lookupBase, 300*time.Millisecond)
	client.entered = make(chan struct{})
	store := NewSpanStore(client, nil, "1", big.NewInt(0))
	defer store.Close()
	store.stopPollLoop()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.spanRangeById(ctx, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("lookup with an expired context: %v", err)
	}
	extractorWindow.mu.Lock()
	counted := extractorWindow.fetches
	extractorWindow.mu.Unlock()
	if counted != 1 {
		t.Fatalf("%d fetches counted when the lookup returned", counted)
	}
	drained := make(chan struct{})
	go func() {
		extractorWindow.drainFetches()
		close(drained)
	}()
	<-client.entered
	if isClosed(drained) {
		t.Fatal("the window did not wait for the detached fetch")
	}
	waitFor(t, "the detached fetch", func() bool { return isClosed(drained) })
	if client.callsFor(2) != 1 {
		t.Fatalf("span 2 fetched %d times", client.callsFor(2))
	}
}

// TestExtractorSpanRangeFetchCountPrecedesDoChan checks the patched source:
// spanRangeById counts the fetch before its singleflight DoChan.
func TestExtractorSpanRangeFetchCountPrecedesDoChan(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "span_store_lookup.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "spanRangeById" {
			continue
		}
		track, do := -1, -1
		for i, stmt := range fn.Body.List {
			ast.Inspect(stmt, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "DoChan" && do < 0 {
					do = i
				}
				if id, ok := n.(*ast.Ident); ok && id.Name == "extractorTrackFetch" && track < 0 {
					track = i
				}
				return true
			})
		}
		if track < 0 || do < 0 || track >= do {
			t.Fatalf("spanRangeById counts the fetch at statement %d and calls DoChan at %d", track, do)
		}
		return
	}
	t.Fatal("spanRangeById not found")
}
