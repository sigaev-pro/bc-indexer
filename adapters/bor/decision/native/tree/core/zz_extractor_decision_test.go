// Extractor patch test (E01-02.decision_bridge, Bor block): the decision
// seam of the import path.

package core

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/downloader/whitelist"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/params"
)

// testDecider checks the protocol order at each step and records it.
type testDecider struct {
	t        *testing.T
	bc       *BlockChain
	db       ethdb.Database
	intents  []ExtractorTransition
	events   []string
	intend   func(ExtractorTransition) error
	resolved []common.Hash
}

type testIntent struct {
	d     *testDecider
	t     ExtractorTransition
	key   []byte
	value []byte
}

func (d *testDecider) Intend(t ExtractorTransition) (ExtractorIntent, error) {
	d.intents = append(d.intents, t)
	d.events = append(d.events, "intend "+t.Kind)
	number := t.Block.NumberU64()
	// The child root is durable; nothing of the block is written; the head
	// is the old head.
	if durable, err := d.bc.ExtractorRootDurable(t.Block.Root()); err != nil || !durable {
		d.t.Errorf("block %d: child root not durable at Intend (%v)", number, err)
	}
	if d.bc.HasBlock(t.Block.Hash(), number) || d.bc.GetTd(t.Block.Hash(), number) != nil {
		d.t.Errorf("block %d is stored before its intent", number)
	}
	if d.bc.CurrentBlock().Hash() != t.OldHead.Hash() {
		d.t.Errorf("block %d: head moved before its intent", number)
	}
	if d.intend != nil {
		if err := d.intend(t); err != nil {
			return nil, err
		}
	}
	key := []byte(fmt.Sprintf("extractor-decision-marker-v1/test/%016x", len(d.intents)))
	return &testIntent{d: d, t: t, key: key, value: []byte("intent " + t.Block.Hash().Hex())}, nil
}

func (i *testIntent) Marker() ([]byte, []byte) { return i.key, i.value }

func (i *testIntent) Resolve() error {
	i.d.events = append(i.d.events, "resolve")
	value, err := i.d.db.Get(i.key)
	if err != nil || string(value) != string(i.value) {
		return fmt.Errorf("marker of block %d is %q, %v", i.t.Block.NumberU64(), value, err)
	}
	if i.t.Kind != ExtractorSide && i.d.bc.CurrentBlock().Hash() != i.t.Block.Hash() {
		return fmt.Errorf("%s of block %d resolved before the head moved", i.t.Kind, i.t.Block.NumberU64())
	}
	i.d.resolved = append(i.d.resolved, i.t.Block.Hash())
	return nil
}

func (i *testIntent) Reject(nativeErr error) error {
	i.d.events = append(i.d.events, "reject "+nativeErr.Error())
	return nil
}

func (i *testIntent) Release() error {
	i.d.events = append(i.d.events, "release")
	return nil
}

type decisionChain struct {
	bc      *BlockChain
	db      ethdb.Database
	checker *whitelist.Service
	genesis *Genesis
	engine  *ethash.Ethash
}

func newDecisionChain(t *testing.T) *decisionChain {
	t.Helper()
	genesis := &Genesis{Config: params.AllEthashProtocolChanges, BaseFee: big.NewInt(params.InitialBaseFee)}
	db := rawdb.NewMemoryDatabase()
	checker := whitelist.NewService(db, false, 0)
	config := DefaultConfig()
	config.ArchiveMode, config.StateScheme, config.Checker = true, rawdb.HashScheme, checker
	engine := ethash.NewFaker()
	bc, err := NewBlockChain(db, genesis, engine, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bc.Stop)
	return &decisionChain{bc: bc, db: db, checker: checker, genesis: genesis, engine: engine}
}

// blocks generates n blocks on genesis that pay coinbase, so each block
// changes the state.
func (c *decisionChain) blocks(n int, coinbase byte) []*types.Block {
	_, blocks, _ := GenerateChainWithGenesis(c.genesis, c.engine, n, func(_ int, b *BlockGen) {
		b.SetCoinbase(common.Address{coinbase})
	})
	return blocks
}

func (c *decisionChain) decider(t *testing.T) *testDecider {
	d := &testDecider{t: t, bc: c.bc, db: c.db}
	c.bc.SetExtractorDecider(d)
	return d
}

func recordSteps(t *testing.T) *[]string {
	steps := []string{}
	ExtractorDecisionStep = func(step string, block common.Hash) { steps = append(steps, step) }
	t.Cleanup(func() { ExtractorDecisionStep = nil })
	return &steps
}

func TestExtractorDecisionImportOrder(t *testing.T) {
	c := newDecisionChain(t)
	d := c.decider(t)
	steps := recordSteps(t)
	blocks := c.blocks(3, 0x01)
	for i, block := range blocks {
		if rawdb.HasLegacyTrieNode(c.db, block.Root()) {
			t.Fatalf("the state of block %d exists before its import", i+1)
		}
		if _, err := c.bc.InsertChain(types.Blocks{block}, false); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.intents) != 3 {
		t.Fatalf("%d intents, want 3", len(d.intents))
	}
	for i, tr := range d.intents {
		parent := c.bc.GetHeaderByNumber(uint64(i))
		if tr.Kind != ExtractorImport || tr.Block.Hash() != blocks[i].Hash() || tr.OldHead.Hash() != parent.Hash() ||
			tr.NewHead.Hash() != blocks[i].Hash() || tr.ExternTD.Cmp(new(big.Int).Add(tr.LocalTD, blocks[i].Difficulty())) != 0 {
			t.Fatalf("intent %d is %s %d over %d", i, tr.Kind, tr.Block.NumberU64(), tr.OldHead.Number)
		}
	}
	if want := slices.Repeat([]string{"child-root", "block-batch", "marker"}, 3); !slices.Equal(*steps, want) {
		t.Fatalf("steps %v, want %v", *steps, want)
	}
	if !slices.Equal(d.events, slices.Repeat([]string{"intend IMPORT", "resolve"}, 3)) {
		t.Fatalf("events %v", d.events)
	}
	if c.bc.CurrentBlock().Hash() != blocks[2].Hash() {
		t.Fatal("the head did not move to the last block")
	}
}

func TestExtractorDecisionSideAndCanonical(t *testing.T) {
	c := newDecisionChain(t)
	canon, side := c.blocks(2, 0x01), c.blocks(3, 0x02)
	if _, err := c.bc.InsertChain(canon, false); err != nil {
		t.Fatal(err)
	}
	d := c.decider(t)
	recorded := recordSteps(t)
	for _, block := range side {
		if _, err := c.bc.InsertChain(types.Blocks{block}, false); err != nil {
			t.Fatal(err)
		}
	}
	kinds := []string{}
	for _, tr := range d.intents {
		kinds = append(kinds, tr.Kind)
	}
	// side[0] does not outweigh the head. side[1] ties with canon[1] at
	// height 2: the native tie-break takes it when the head hash is lower.
	// side[2] then extends the head (IMPORT) or outweighs it from another
	// branch (CANONICAL).
	tieTaken := bytes.Compare(canon[1].Hash().Bytes(), side[1].Hash().Bytes()) < 0
	want := []string{ExtractorSide, ExtractorSide, ExtractorCanonical}
	steps := []string{"child-root", "marker", "child-root", "marker", "child-root", "block-batch", "marker"}
	if tieTaken {
		want = []string{ExtractorSide, ExtractorCanonical, ExtractorImport}
		steps = []string{"child-root", "marker", "child-root", "block-batch", "marker", "child-root", "block-batch", "marker"}
	}
	if !slices.Equal(kinds, want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
	for i, tr := range d.intents {
		if tr.Kind != ExtractorCanonical && len(tr.Branch) != 0 {
			t.Fatalf("%s intent %d has a branch", tr.Kind, i)
		}
		switch tr.Kind {
		case ExtractorSide:
			if tr.OldHead.Hash() != tr.NewHead.Hash() {
				t.Fatalf("SIDE intent %d moves the head", i)
			}
		case ExtractorImport:
			if tr.Block.ParentHash() != tr.OldHead.Hash() || tr.NewHead.Hash() != tr.Block.Hash() {
				t.Fatalf("IMPORT intent %d does not extend the head", i)
			}
		case ExtractorCanonical:
			if tr.Block.ParentHash() == tr.OldHead.Hash() || tr.NewHead.Hash() != tr.Block.Hash() {
				t.Fatalf("CANONICAL intent %d does not move the head onto another branch", i)
			}
			// The branch is the stored side blocks below the block, down to
			// the common ancestor (genesis), ascending.
			branch := []common.Hash{}
			for _, h := range tr.Branch {
				branch = append(branch, h.Hash())
			}
			want := []common.Hash{}
			for _, b := range side[:tr.Block.NumberU64()-1] {
				want = append(want, b.Hash())
			}
			if !slices.Equal(branch, want) {
				t.Fatalf("CANONICAL intent %d has branch %x, want %x", i, branch, want)
			}
		}
	}
	// A SIDE marker is in the block batch, the others in the final head batch.
	if !slices.Equal(*recorded, steps) {
		t.Fatalf("steps %v, want %v", *recorded, steps)
	}
	if len(d.resolved) != 3 || c.bc.CurrentBlock().Hash() != side[2].Hash() {
		t.Fatalf("resolved %d, head %x", len(d.resolved), c.bc.CurrentBlock().Hash())
	}
}

func TestExtractorDecisionNativeRejection(t *testing.T) {
	c := newDecisionChain(t)
	d := c.decider(t)
	steps := recordSteps(t)
	blocks := c.blocks(2, 0x01)
	if _, err := c.bc.InsertChain(blocks[:1], false); err != nil {
		t.Fatal(err)
	}
	// A milestone that conflicts with block 2 arrives during its import,
	// after its execution: the native check refuses the write.
	d.intend = func(tr ExtractorTransition) error {
		if tr.Block.NumberU64() == 2 {
			c.checker.ProcessMilestone(2, common.Hash{0xff})
		}
		return nil
	}
	_, err := c.bc.InsertChain(blocks[1:], false)
	if !errors.Is(err, whitelist.ErrMismatch) {
		t.Fatalf("import with a conflicting milestone: %v", err)
	}
	if want := []string{"intend IMPORT", "resolve", "intend IMPORT", "reject " + whitelist.ErrMismatch.Error()}; !slices.Equal(d.events, want) {
		t.Fatalf("events %v, want %v", d.events, want)
	}
	if want := []string{"child-root", "block-batch", "marker", "child-root", "native-rejected"}; !slices.Equal(*steps, want) {
		t.Fatalf("steps %v, want %v", *steps, want)
	}
	if c.bc.HasBlock(blocks[1].Hash(), 2) || c.bc.CurrentBlock().Hash() != blocks[0].Hash() {
		t.Fatal("the refused block was written")
	}
	if has, _ := c.db.Has([]byte(fmt.Sprintf("extractor-decision-marker-v1/test/%016x", 2))); has {
		t.Fatal("the refused intent has a marker")
	}
}

func TestExtractorDecisionIntendFailureWritesNothing(t *testing.T) {
	c := newDecisionChain(t)
	d := c.decider(t)
	refused := errors.New("no EXEC_READY observation")
	d.intend = func(ExtractorTransition) error { return refused }
	blocks := c.blocks(1, 0x01)
	if _, err := c.bc.InsertChain(blocks, false); !errors.Is(err, refused) {
		t.Fatalf("import with a refused intent: %v", err)
	}
	if c.bc.HasBlock(blocks[0].Hash(), 1) || c.bc.CurrentBlock().Number.Uint64() != 0 {
		t.Fatal("a block without an intent was written")
	}
	// The child state of the barrier stays; a retry imports the block.
	d.intend = nil
	if _, err := c.bc.InsertChain(blocks, false); err != nil || c.bc.CurrentBlock().Hash() != blocks[0].Hash() {
		t.Fatalf("retry: %v", err)
	}
}

func TestExtractorDecisionWithoutDeciderIsNative(t *testing.T) {
	c := newDecisionChain(t)
	steps := recordSteps(t)
	blocks := c.blocks(2, 0x01)
	if _, err := c.bc.InsertChain(blocks, false); err != nil || c.bc.CurrentBlock().Hash() != blocks[1].Hash() {
		t.Fatalf("native import: %v", err)
	}
	if len(*steps) != 0 {
		t.Fatalf("decision steps without a decider: %v", *steps)
	}
}

func TestExtractorRecoverImportsUnderTheChainLock(t *testing.T) {
	c := newDecisionChain(t)
	blocks := c.blocks(1, 0x01)
	d := &testDecider{t: t, bc: c.bc, db: c.db}
	err := c.bc.ExtractorRecover(func(r *ExtractorRecovery) error {
		// TryLock waits for the lock; TryLockNow does not.
		if c.bc.chainmu.TryLockNow() {
			c.bc.chainmu.Unlock()
			return errors.New("the chain lock is not held")
		}
		if r.Head().Number.Uint64() != 0 || r.Stored(blocks[0].Hash(), 1) {
			return errors.New("unexpected native state before the import")
		}
		parent := c.bc.GetHeaderByNumber(0)
		receipts, logs, _, statedb, _, err := c.bc.ProcessBlock(blocks[0], parent, nil, nil, nil)
		if err != nil {
			return err
		}
		if err := r.Import(d, blocks[0], receipts, logs, statedb); err != nil {
			return err
		}
		if !r.Stored(blocks[0].Hash(), 1) || r.Head().Hash() != blocks[0].Hash() {
			return errors.New("recovery did not import the block")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.events, []string{"intend IMPORT", "resolve"}) {
		t.Fatalf("events %v", d.events)
	}
	// Outside ExtractorRecover the lock is free again.
	if !c.bc.chainmu.TryLockNow() {
		t.Fatal("ExtractorRecover kept the chain lock")
	}
	c.bc.chainmu.Unlock()
}

func TestExtractorMarkBatchOnlyMarksTheArmedBatch(t *testing.T) {
	c := newDecisionChain(t)
	block := c.blocks(1, 0x01)[0]
	other := c.blocks(1, 0x02)[0]
	marker := &extractorMarker{block: block.Hash(), head: true, key: []byte("k"), value: []byte("v")}
	extractorMarkers.Store(c.bc, marker)
	defer extractorMarkers.Delete(c.bc)
	batch := c.db.NewBatch()
	c.bc.extractorMarkBatch(batch, other, true)
	c.bc.extractorMarkBatch(batch, block, false)
	if batch.ValueSize() != 0 || marker.written {
		t.Fatal("the marker was added to another batch")
	}
	c.bc.extractorMarkBatch(batch, block, true)
	c.bc.extractorMarkBatch(batch, block, true)
	if batch.ValueSize() != 2 || !marker.written {
		t.Fatalf("the head batch holds %d bytes", batch.ValueSize())
	}
}

func TestExtractorCommitOnceUsesTheBarrierCommit(t *testing.T) {
	c := newDecisionChain(t)
	statedb, err := c.bc.StateAt(c.bc.Genesis().Root())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	commit := func(uint64, bool, bool) (common.Hash, int, error) {
		calls++
		return common.Hash{byte(calls)}, calls, nil
	}
	if _, err := extractorCommitBefore(statedb, commit, 1, true, true); err != nil {
		t.Fatal(err)
	}
	root, update, err := extractorCommitOnce(statedb, commit, 1, true, true)
	if err != nil || calls != 1 || root != (common.Hash{1}) || update != 1 {
		t.Fatalf("saved commit: %x %d %v after %d calls", root, update, err, calls)
	}
	root, update, err = extractorCommitOnce(statedb, commit, 1, true, true)
	if err != nil || calls != 2 || root != (common.Hash{2}) || update != 2 {
		t.Fatalf("native commit: %x %d %v after %d calls", root, update, err, calls)
	}
}

func TestExtractorRootDurable(t *testing.T) {
	c := newDecisionChain(t)
	for root, want := range map[common.Hash]bool{types.EmptyRootHash: true, c.bc.Genesis().Root(): true,
		{0x01}: false} {
		if got, err := c.bc.ExtractorRootDurable(root); err != nil || got != want {
			t.Fatalf("root %x durable %v, %v; want %v", root, got, err, want)
		}
	}
}
