package native

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
)

// mirror answers Applied with a fixed result and records every query.
type mirror struct {
	applied bool
	asked   []uint64
}

func (m *mirror) Applied(sequence uint64, _ string) (bool, error) {
	m.asked = append(m.asked, sequence)
	return m.applied, nil
}

// crashed is the panic value of a simulated process crash; the in-memory
// store keeps every write, as after a process crash.
type crashed struct{ point string }

// crashAt runs fn and reports whether it stopped with the simulated crash.
func crashAt(t *testing.T, point string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != (crashed{point}) {
			t.Fatalf("want a crash at %s, got %v", point, r)
		}
	}()
	fn()
}

func attachDecisions(t *testing.T, chain *core.BlockChain, db ethdb.Database, f fixture, opts Options,
	dopts DecisionOptions) (*Decisions, *[]string) {
	t.Helper()
	events := &[]string{}
	opts.Trace = func(event string, fields map[string]any) {
		if event != "lookup" {
			*events = append(*events, event)
		}
	}
	if opts.RunID == "" {
		opts.RunID = "r1"
	}
	d, err := AttachDecisions(chain, db, f.config(), opts, dopts)
	if err != nil {
		t.Fatal(err)
	}
	return d, events
}

func decisions(t *testing.T, d *Decisions) []decision.Decision {
	t.Helper()
	bridge, token := d.Bridge()
	defer token.Release()
	list, err := bridge.Decisions(token)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func counter(t *testing.T, db ethdb.Database, prefix string) string {
	t.Helper()
	value, err := db.Get([]byte(prefix + "geth-test.r1/counter"))
	if err != nil {
		t.Fatal(err)
	}
	return string(value)
}

func markers(t *testing.T, db ethdb.Database) []string {
	t.Helper()
	it := db.NewIterator([]byte("extractor-decision-marker-v1/"), nil)
	defer it.Release()
	keys := []string{}
	for it.Next() {
		keys = append(keys, string(it.Key()))
	}
	return keys
}

func TestUnitDecisionImportPath(t *testing.T) {
	f := newFixture(t, 5)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	if _, err := chain.InsertChain(f.blocks[:3]); err != nil {
		t.Fatal(err)
	}
	m := &mirror{}
	d, events := attachDecisions(t, chain, db, f, Options{}, DecisionOptions{Mirror: m})
	if _, err := chain.InsertChain(f.blocks[3:]); err != nil {
		t.Fatal(err)
	}
	if head := chain.CurrentBlock(); head.Hash() != f.blocks[4].Hash() {
		t.Fatalf("head %d", head.Number)
	}
	// Both observations are closed by their IMPORT decisions: no pin, no
	// listed observation; the child roots are on disk.
	if list := observations(t, d.Admission()); len(list) != 0 {
		t.Fatalf("observations stay open: %+v", list)
	}
	if ids := livePins(t, db); len(ids) != 0 {
		t.Fatalf("pins stay live: %v", ids)
	}
	for _, block := range f.blocks[3:] {
		if !rawdb.HasLegacyTrieNode(db, block.Root()) {
			t.Fatalf("child root of block %d is not on disk", block.NumberU64())
		}
	}
	list := decisions(t, d)
	if len(list) != 2 || list[0].Intent.Kind != decision.Import || list[1].Intent.Kind != decision.Import ||
		!list[0].Marker || list[0].Decided == nil || list[0].Decided.Delivery != decision.Batch {
		t.Fatalf("decisions: %+v", list)
	}
	if list[0].Intent.Members[0].Observation != "geth-test.r1.0000000000000000" ||
		list[0].Intent.Members[0].ChildRoot != hexHash(f.blocks[3].Root()) ||
		list[0].Intent.OldHead.Hash != hexHash(f.blocks[2].Hash()) || list[0].Intent.NewHead.Hash != hexHash(f.blocks[3].Hash()) {
		t.Fatalf("intent 0: %+v", list[0].Intent)
	}
	// The marker holds exactly the intent bytes.
	value, err := db.Get(decision.MarkerKey(d.Admission().Producer(), 1))
	want, _ := decision.MarkerValue(list[1].Intent)
	if err != nil || string(value) != string(want) {
		t.Fatalf("marker of decision 1: %s %v", value, err)
	}
	wantEvents := strings.Join([]string{"opened", "bridge_opened", "live"}, ",") + "," +
		strings.Repeat("begin,pinned,sealed,admitted,exec_ready,committed,intent,mutated,decided,", 2)
	if got := strings.Join(*events, ",") + ","; got != wantEvents {
		t.Fatalf("events:\n got %s\nwant %s", got, wantEvents)
	}
	// The mirror applies both: Cleanup removes the markers.
	m.applied = true
	bridge, token := d.Bridge()
	if err := bridge.Cleanup(token); err != nil {
		t.Fatal(err)
	}
	token.Release()
	if keys := markers(t, db); len(keys) != 0 {
		t.Fatalf("markers stay: %v", keys)
	}
	// A block already in the chain opens no new observation and no decision.
	admitted, decided := counter(t, db, "extractor-admission-v1/"), counter(t, db, "extractor-decision-v1/")
	if _, err := chain.InsertChain(f.blocks[3:]); err != nil {
		t.Fatal(err)
	}
	if counter(t, db, "extractor-admission-v1/") != admitted || counter(t, db, "extractor-decision-v1/") != decided {
		t.Fatal("a known block opened an observation or a decision")
	}
	chain.Stop()
}

func TestUnitKnownBlockOpensNoObservation(t *testing.T) {
	f := newFixture(t, 5)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	defer chain.Stop()
	if _, err := chain.InsertChain(f.blocks[:4]); err != nil {
		t.Fatal(err)
	}
	d, _ := attachDecisions(t, chain, db, f, Options{}, DecisionOptions{})
	// Begin of a block that is stored with its state, as when Geth re-executes
	// a known block for a missing snapshot layer.
	unlock := d.Admission().Lock()
	_, err := d.Admission().Begin(f.blocks[3], f.blocks[2].Root())
	unlock()
	if !errors.Is(err, ErrKnownBlock) {
		t.Fatalf("Begin of a stored block: %v", err)
	}
	if list := observations(t, d.Admission()); len(list) != 0 {
		t.Fatalf("observations: %+v", list)
	}
}

func TestUnitDecisionSideThenCanonical(t *testing.T) {
	f := newFixture(t, 5)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	defer chain.Stop()
	if _, err := chain.InsertChain(f.blocks[:3]); err != nil {
		t.Fatal(err)
	}
	d, _ := attachDecisions(t, chain, db, f, Options{}, DecisionOptions{})
	if _, err := chain.InsertBlockWithoutSetHead(context.Background(), f.blocks[3], false); err != nil {
		t.Fatal(err)
	}
	list := decisions(t, d)
	if len(list) != 1 || list[0].Intent.Kind != decision.Side || list[0].Decided == nil ||
		list[0].Decided.Delivery != decision.Checkpoint || list[0].Marker || list[0].Intent.Events {
		t.Fatalf("SIDE decision: %+v", list)
	}
	// The SIDE decision retains its member with its pin.
	if ids := livePins(t, db); len(ids) != 1 {
		t.Fatalf("pins: %v", ids)
	}
	if chain.CurrentBlock().Hash() != f.blocks[2].Hash() {
		t.Fatal("a SIDE write moved the head")
	}
	// The child names the retained side block: one CANONICAL decision closes
	// both.
	if _, err := chain.InsertChain(f.blocks[4:]); err != nil {
		t.Fatal(err)
	}
	list = decisions(t, d)
	if len(list) != 1 || list[0].Intent.Kind != decision.Canonical || len(list[0].Intent.Members) != 2 ||
		list[0].Intent.Members[0].Observation != "geth-test.r1.0000000000000001" ||
		list[0].Intent.Members[1].Observation != "geth-test.r1.0000000000000000" {
		t.Fatalf("CANONICAL decision: %+v", list)
	}
	if ids := livePins(t, db); len(ids) != 0 {
		t.Fatalf("pins: %v", ids)
	}
	if obs := observations(t, d.Admission()); len(obs) != 0 {
		t.Fatalf("observations: %+v", obs)
	}
	if chain.CurrentBlock().Hash() != f.blocks[4].Hash() {
		t.Fatal("head")
	}
}

func TestUnitDecisionRejection(t *testing.T) {
	f := newFixture(t, 5)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	defer chain.Stop()
	if _, err := chain.InsertChain(f.blocks[:4]); err != nil {
		t.Fatal(err)
	}
	refusal := errors.New("native refusal fixture")
	m := &mirror{applied: true}
	d, _ := attachDecisions(t, chain, db, f, Options{}, DecisionOptions{Mirror: m,
		Outcome: func(*types.Block) error { return refusal }})
	if _, err := chain.InsertChain(f.blocks[4:]); !errors.Is(err, refusal) {
		t.Fatalf("refused import: %v", err)
	}
	if chain.CurrentBlock().Hash() != f.blocks[3].Hash() || chain.HasBlock(f.blocks[4].Hash(), 5) ||
		rawdb.ReadBadBlock(db, f.blocks[4].Hash()) == nil {
		t.Fatal("the refused block was stored or not recorded as bad")
	}
	if list := decisions(t, d); len(list) != 0 {
		t.Fatalf("a rejected intent stays listed: %+v", list)
	}
	if ids := livePins(t, db); len(ids) != 0 {
		t.Fatalf("pins: %v", ids)
	}
	if keys := markers(t, db); len(keys) != 0 || len(m.asked) != 0 {
		t.Fatalf("markers %v, mirror asked %v", keys, m.asked)
	}
}

func TestUnitRecoveryRejectsFromBadBlock(t *testing.T) {
	f := newFixture(t, 5)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	if _, err := chain.InsertChain(f.blocks[:4]); err != nil {
		t.Fatal(err)
	}
	// The native refusal is durable, the rejection of the intent is not.
	store := &failingStore{Database: db, suffix: "/rejected"}
	refusal := errors.New("native refusal fixture")
	attachDecisions(t, chain, db, f, Options{Store: store}, DecisionOptions{
		Outcome: func(*types.Block) error { return refusal }})
	if _, err := chain.InsertChain(f.blocks[4:]); err == nil {
		t.Fatal("the import passed")
	}
	chain = newChain(t, db, f)
	defer chain.Stop()
	m := &mirror{applied: true}
	d, events := attachDecisions(t, chain, db, f, Options{}, DecisionOptions{Mirror: m})
	if got := strings.Join(*events, ","); got != "opened,bridge_opened,settling,intent_rejected,live" {
		t.Fatalf("events %s", got)
	}
	if list := decisions(t, d); len(list) != 0 || len(m.asked) != 0 {
		t.Fatalf("decisions %+v, mirror asked %v", list, m.asked)
	}
	rejected, err := db.Get([]byte("extractor-decision-v1/geth-test.r1/0000000000000000/rejected"))
	if err == nil {
		t.Fatalf("the done rejection was not pruned: %s", rejected)
	}
	if chain.CurrentBlock().Hash() != f.blocks[3].Hash() || len(livePins(t, db)) != 0 {
		t.Fatal("head or pins after the recovered rejection")
	}
}

func TestUnitRecoveryCompletesTheSavedIntent(t *testing.T) {
	for _, point := range []string{"intent", "mutated", "marker"} {
		t.Run(point, func(t *testing.T) {
			f := newFixture(t, 5)
			db := rawdb.NewMemoryDatabase()
			chain := newChain(t, db, f)
			if _, err := chain.InsertChain(f.blocks[:3]); err != nil {
				t.Fatal(err)
			}
			dopts := DecisionOptions{}
			opts := Options{}
			switch point {
			case "intent":
				// After the intent sync, before anything native.
				dopts.Outcome = func(*types.Block) error { panic(crashed{point}) }
			case "mutated":
				// After the block batch, before the head batch with the marker.
				dopts.Point = func(name string) {
					if name == "mutated" {
						panic(crashed{point})
					}
				}
			case "marker":
				// After the head batch with the marker, before the decided record.
				opts.Store = &failingStore{Database: db, suffix: "/decided"}
			}
			attachDecisions(t, chain, db, f, opts, dopts)
			if point == "marker" {
				if _, err := chain.InsertChain(f.blocks[3:4]); err == nil {
					t.Fatal("the import passed a failed decided write")
				}
			} else {
				crashAt(t, point, func() { _, _ = chain.InsertChain(f.blocks[3:4]) })
			}
			// The intent is durable and unresolved.
			saved, err := db.Get([]byte("extractor-decision-v1/geth-test.r1/0000000000000000/intent"))
			if err != nil {
				t.Fatal(err)
			}
			if ok, _ := db.Has([]byte("extractor-decision-v1/geth-test.r1/0000000000000000/decided")); ok {
				t.Fatal("decided before recovery")
			}
			chain = newChain(t, db, f)
			defer chain.Stop()
			d, events := attachDecisions(t, chain, db, f, Options{}, DecisionOptions{})
			after := decisions(t, d)
			if len(after) != 1 || after[0].State != decision.Decided || !strings.Contains(string(saved), after[0].Intent.Digest) {
				t.Fatalf("after recovery: %+v", after)
			}
			if chain.CurrentBlock().Hash() != f.blocks[3].Hash() {
				t.Fatal("recovery did not complete the saved head transition")
			}
			want := map[string]string{"intent": "settling,replaying,replayed,committed,intent,mutated,decided",
				"mutated": "settling,decided", "marker": "settling,decided"}[point]
			if got := strings.Join(*events, ","); got != "opened,bridge_opened,"+want+",live" {
				t.Fatalf("events %s", got)
			}
			if obs := observations(t, d.Admission()); len(obs) != 0 {
				t.Fatalf("observations: %+v", obs)
			}
			// The completed block is in the chain: importing it again opens
			// nothing.
			if _, err := chain.InsertChain(f.blocks[3:4]); err != nil && !errors.Is(err, ErrKnownBlock) {
				t.Fatal(err)
			}
			if counter(t, db, "extractor-admission-v1/") != `{"next":1}` ||
				counter(t, db, "extractor-decision-v1/") != `{"next":1}` {
				t.Fatal("a known block opened an observation or a decision")
			}
		})
	}
}

func TestUnitCanonicalMarkerIsInTheFinalHeadBatch(t *testing.T) {
	f := newFixture(t, 5)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	if _, err := chain.InsertChain(f.blocks[:3]); err != nil {
		t.Fatal(err)
	}
	attachDecisions(t, chain, db, f, Options{}, DecisionOptions{Point: func(name string) {
		if name == "intermediate" {
			panic(crashed{"intermediate"})
		}
	}})
	if _, err := chain.InsertBlockWithoutSetHead(context.Background(), f.blocks[3], false); err != nil {
		t.Fatal(err)
	}
	// reorg writes the head batch of the side block 4 first; the crash comes
	// after it, before the head batch of 5.
	crashAt(t, "intermediate", func() { _, _ = chain.InsertChain(f.blocks[4:]) })
	if keys := markers(t, db); len(keys) != 0 {
		t.Fatalf("the marker was written before the final head batch: %v", keys)
	}
	if rawdb.ReadHeadBlockHash(db) != f.blocks[3].Hash() {
		t.Fatal("the crash did not come after the intermediate head batch")
	}
	chain = newChain(t, db, f)
	defer chain.Stop()
	d, events := attachDecisions(t, chain, db, f, Options{}, DecisionOptions{})
	if got := strings.Join(*events, ","); got != "opened,bridge_opened,settling,decided,live" {
		t.Fatalf("events %s", got)
	}
	list := decisions(t, d)
	if len(list) != 1 || list[0].Intent.Kind != decision.Canonical || list[0].Decided == nil {
		t.Fatalf("decisions: %+v", list)
	}
	if chain.CurrentBlock().Hash() != f.blocks[4].Hash() || len(livePins(t, db)) != 0 {
		t.Fatal("recovery did not complete the CANONICAL head change")
	}
}

func TestUnitRecoveryReplaysAVolatileChildRoot(t *testing.T) {
	f := newFixture(t, 5)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	if _, err := chain.InsertChain(f.blocks[:3]); err != nil {
		t.Fatal(err)
	}
	// EXEC_READY is durable, the child state is not.
	attachDecisions(t, chain, db, f, Options{}, DecisionOptions{Point: func(name string) {
		if name == "committed" {
			panic(crashed{"committed"})
		}
	}})
	crashAt(t, "committed", func() { _, _ = chain.InsertChain(f.blocks[3:4]) })
	if rawdb.HasLegacyTrieNode(db, f.blocks[3].Root()) {
		t.Fatal("the child root reached the disk before the barrier")
	}
	ready, err := db.Get([]byte("extractor-admission-v1/geth-test.r1/0000000000000000/exec-ready"))
	if err != nil {
		t.Fatal(err)
	}
	chain = newChain(t, db, f)
	defer chain.Stop()
	d, events := attachDecisions(t, chain, db, f, Options{}, DecisionOptions{})
	if got := strings.Join(*events, ","); got != "opened,bridge_opened,replaying,replayed,committed,intent,mutated,decided,live" {
		t.Fatalf("events %s", got)
	}
	if !rawdb.HasLegacyTrieNode(db, f.blocks[3].Root()) {
		t.Fatal("replay did not make the child root durable")
	}
	list := decisions(t, d)
	if len(list) != 1 || list[0].Intent.Kind != decision.Side {
		t.Fatalf("decisions: %+v", list)
	}
	again, _ := db.Get([]byte("extractor-admission-v1/geth-test.r1/0000000000000000/exec-ready"))
	if string(again) != string(ready) {
		t.Fatal("replay replaced the EXEC_READY record")
	}
	// The child of the replayed block makes it canonical with one decision.
	if _, err := chain.InsertChain(f.blocks[4:]); err != nil {
		t.Fatal(err)
	}
	if chain.CurrentBlock().Hash() != f.blocks[4].Hash() || len(livePins(t, db)) != 0 {
		t.Fatal("the CANONICAL decision did not close the replayed block")
	}
}

func TestUnitDeferredWriteBeforeTheBridge(t *testing.T) {
	f := newFixture(t, 5)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	if _, err := chain.InsertChain(f.blocks[:3]); err != nil {
		t.Fatal(err)
	}
	// The EXEC_READY write fails: the obligation stays ADMITTED.
	attachDecisions(t, chain, db, f, Options{Store: &failingStore{Database: db, suffix: "/exec-ready"}},
		DecisionOptions{})
	if _, err := chain.InsertChain(f.blocks[3:4]); err == nil {
		t.Fatal("the import passed")
	}
	chain = newChain(t, db, f)
	defer chain.Stop()
	d, events := attachDecisions(t, chain, db, f, Options{}, DecisionOptions{})
	want := "opened,recovering,resumed,exec_ready,deferred,recovered,bridge_opened,replaying,replayed,committed,intent,mutated,decided,live"
	if got := strings.Join(*events, ","); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	if list := decisions(t, d); len(list) != 1 || list[0].Intent.Kind != decision.Side {
		t.Fatalf("decisions: %+v", list)
	}
}

func TestUnitLegacyStoreIsRefused(t *testing.T) {
	f := newFixture(t, 5)
	db := rawdb.NewMemoryDatabase()
	chain := newChain(t, db, f)
	if _, err := chain.InsertChain(f.blocks[:3]); err != nil {
		t.Fatal(err)
	}
	if _, err := Attach(chain, db, f.config(), Options{RunID: "r1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := chain.InsertChain(f.blocks[3:4]); err != nil {
		t.Fatal(err)
	}
	chain.Stop()
	chain = newChain(t, db, f)
	defer chain.Stop()
	if _, err := AttachDecisions(chain, db, f.config(), Options{RunID: "r1"}, DecisionOptions{}); !errors.Is(err,
		ErrDecisionRecovery) {
		t.Fatalf("a store from before the decision bridge: %v", err)
	}
}

func TestUnitRootsAndEvidence(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	root := common.HexToHash("0x11")
	if ok, err := (Roots{DB: db}).Durable(hexHash(root)); ok || err != nil {
		t.Fatalf("absent root: %v %v", ok, err)
	}
	rawdb.WriteLegacyTrieNode(db, root, []byte{1})
	if ok, err := (Roots{DB: db}).Durable(hexHash(root)); !ok || err != nil {
		t.Fatalf("stored root: %v %v", ok, err)
	}
	if ok, err := (Roots{DB: db}).Durable(hexHash(types.EmptyRootHash)); !ok || err != nil {
		t.Fatalf("empty root: %v %v", ok, err)
	}
	if _, err := (Roots{DB: db}).Durable("zz"); err == nil {
		t.Fatal("malformed root")
	}
	block := types.NewBlockWithHeader(&types.Header{Number: common.Big1})
	if IntentEvidence(decision.Import, block, root) == IntentEvidence(decision.Side, block, root) ||
		RefusalEvidence(block.Hash()) == RefusalEvidence(root) {
		t.Fatal("evidence digests do not bind their inputs")
	}
	_ = admission.ExecReady
}
