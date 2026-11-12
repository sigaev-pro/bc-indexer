// Extractor patch (E01-02.decision_bridge, Bor block): the native decision
// seam of the product import path. The preparation adds this file to the
// accepted Bor tree; see adapters/bor/decision/native/README.md.

package core

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/downloader/whitelist"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

// Decision kinds of the import path (the shared decision package's Kind).
const (
	// ExtractorImport moves the head onto the imported block, a child of the head.
	ExtractorImport = "IMPORT"
	// ExtractorSide stores the imported block without a head change.
	ExtractorSide = "SIDE"
	// ExtractorCanonical makes the imported block the head of another branch.
	ExtractorCanonical = "CANONICAL"
)

// ErrExtractorDecision reports a native write that does not match its
// transition intent.
var ErrExtractorDecision = errors.New("extractor decision: native write does not match the intent")

// ExtractorTransition is the native transition that the import of one
// executed block proposes, predicted with the native fork choice before the
// write.
type ExtractorTransition struct {
	Kind  string
	Block *types.Block
	// Branch lists, for CANONICAL, the stored blocks of the new branch below
	// Block and above the common ancestor with the canonical chain, in
	// ascending order: known side blocks that become canonical with Block.
	Branch   []*types.Header
	OldHead  *types.Header
	NewHead  *types.Header
	LocalTD  *big.Int
	ExternTD *big.Int
}

// ExtractorDecider records the transition intent of an executed block. The
// import path calls Intend after the child-root barrier and before any native
// mutation of the block.
type ExtractorDecider interface {
	Intend(t ExtractorTransition) (ExtractorIntent, error)
}

// ExtractorIntent is one recorded transition intent. The import path calls
// exactly one of Resolve (after the native batch with the marker is synced),
// Reject (on a native refusal before any write of the block) and Release
// (on any other failure, which leaves the intent unresolved for recovery).
type ExtractorIntent interface {
	// Marker returns the native decision marker: key and value.
	Marker() (key, value []byte)
	Resolve() error
	Reject(nativeErr error) error
	Release() error
}

// ExtractorDecisionStep, if set, runs at the named steps of a decision on
// the import path (the probe's crash points): "child-root" after the
// child-root barrier, "native-rejected" after a native refusal and before
// Reject, "block-batch" after the block batch and before the final head
// batch, "marker" after the final batch with the marker is synced and before
// Resolve.
var ExtractorDecisionStep func(step string, block common.Hash)

func extractorStep(step string, block *types.Block) {
	if ExtractorDecisionStep != nil {
		ExtractorDecisionStep(step, block.Hash())
	}
}

// extractorDeciders maps a chain to its decider. A package map keeps the
// BlockChain struct unchanged.
var extractorDeciders sync.Map

// SetExtractorDecider installs the decider of bc; nil removes it.
func (bc *BlockChain) SetExtractorDecider(decider ExtractorDecider) {
	if decider == nil {
		extractorDeciders.Delete(bc)
		return
	}
	extractorDeciders.Store(bc, decider)
}

func (bc *BlockChain) extractorDecider() ExtractorDecider {
	if value, ok := extractorDeciders.Load(bc); ok {
		return value.(ExtractorDecider)
	}
	return nil
}

// extractorMarker is the marker that the final authoritative batch of one
// decision carries: the head batch of the block for IMPORT and CANONICAL,
// the block batch for SIDE.
type extractorMarker struct {
	block   common.Hash
	head    bool
	key     []byte
	value   []byte
	written bool
}

var extractorMarkers sync.Map

// extractorMarkBatch adds the armed decision marker to batch when batch is
// the final authoritative batch of the decided block: its head batch (head)
// or its block batch. Only the import path arms a marker, with the chain
// lock held.
func (bc *BlockChain) extractorMarkBatch(batch ethdb.KeyValueWriter, block *types.Block, head bool) {
	value, ok := extractorMarkers.Load(bc)
	if !ok {
		return
	}
	m := value.(*extractorMarker)
	if m.written || m.block != block.Hash() || m.head != head {
		return
	}
	if head {
		// The block batch is written; the head batch is not.
		extractorStep("block-batch", block)
	}
	if err := batch.Put(m.key, m.value); err != nil {
		log.Crit("Failed to add the extractor decision marker", "err", err)
	}
	m.written = true
}

// extractorCommits holds the state commit of the child-root barrier until
// writeBlockWithState asks for it (edit decision-commit-once), so the
// StateDB is committed once.
var extractorCommits sync.Map

type extractorCommit struct {
	root   common.Hash
	update any
}

// extractorCommitBefore commits statedb for the child-root barrier and keeps
// the result for the native write.
func extractorCommitBefore[U any](statedb *state.StateDB, commit func(uint64, bool, bool) (common.Hash, U, error),
	block uint64, deleteEmpty, noStorageWiping bool) (common.Hash, error) {
	root, update, err := commit(block, deleteEmpty, noStorageWiping)
	if err != nil {
		return root, err
	}
	extractorCommits.Store(statedb, extractorCommit{root: root, update: update})
	return root, nil
}

// extractorCommitOnce is the state commit of writeBlockWithState: the saved
// barrier commit of statedb if there is one, else the native commit.
func extractorCommitOnce[U any](statedb *state.StateDB, commit func(uint64, bool, bool) (common.Hash, U, error),
	block uint64, deleteEmpty, noStorageWiping bool) (common.Hash, U, error) {
	if saved, ok := extractorCommits.LoadAndDelete(statedb); ok {
		c := saved.(extractorCommit)
		return c.root, c.update.(U), nil
	}
	return commit(block, deleteEmpty, noStorageWiping)
}

// ExtractorRootDurable reports whether state root is durable in the native
// store: it syncs the key-value store and checks the root node of the hash
// scheme. The hash trie database commits children before their parent, so a
// durable root node has a durable trie below it.
func (bc *BlockChain) ExtractorRootDurable(root common.Hash) (bool, error) {
	if root == types.EmptyRootHash {
		return true, nil
	}
	if err := bc.db.SyncKeyValue(); err != nil {
		return false, err
	}
	return rawdb.HasLegacyTrieNode(bc.db, root), nil
}

// extractorTD serves the total difficulty of the block being decided before
// the native write stores it, so the native fork choice predicts the write.
type extractorTD struct {
	bc     *BlockChain
	hash   common.Hash
	number uint64
	td     *big.Int
}

func (t extractorTD) Config() *params.ChainConfig { return t.bc.Config() }

func (t extractorTD) GetTd(hash common.Hash, number uint64) *big.Int {
	if hash == t.hash && number == t.number {
		return t.td
	}
	return t.bc.GetTd(hash, number)
}

// extractorTransition predicts the native write of block: SIDE without a
// head change (setHead false, or the native fork choice keeps the head),
// IMPORT onto a child of the head, CANONICAL onto another branch.
func (bc *BlockChain) extractorTransition(block *types.Block, setHead bool) (ExtractorTransition, error) {
	current := bc.CurrentBlock()
	ptd := bc.GetTd(block.ParentHash(), block.NumberU64()-1)
	localTD := bc.GetTd(current.Hash(), current.Number.Uint64())
	if ptd == nil || localTD == nil {
		return ExtractorTransition{}, consensus.ErrUnknownAncestor
	}
	externTD := new(big.Int).Add(block.Difficulty(), ptd)
	t := ExtractorTransition{Kind: ExtractorSide, Block: block, OldHead: current, NewHead: current, LocalTD: localTD,
		ExternTD: externTD}
	if !setHead {
		return t, nil
	}
	fork := &ForkChoice{chain: extractorTD{bc: bc, hash: block.Hash(), number: block.NumberU64(), td: externTD},
		preserve: bc.forker.preserve}
	reorg, err := fork.ReorgNeeded(current, block.Header())
	if err != nil || !reorg {
		return t, err
	}
	t.NewHead = block.Header()
	if block.ParentHash() == current.Hash() {
		t.Kind = ExtractorImport
		return t, nil
	}
	t.Kind = ExtractorCanonical
	for header := bc.GetHeader(block.ParentHash(), block.NumberU64()-1); ; {
		if header == nil {
			return ExtractorTransition{}, consensus.ErrUnknownAncestor
		}
		if bc.GetCanonicalHash(header.Number.Uint64()) == header.Hash() {
			break
		}
		t.Branch = append([]*types.Header{header}, t.Branch...)
		header = bc.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	}
	return t, nil
}

// extractorWriteDecided is the native whitelist check and the native write
// of an executed block on the import path with a decider (edit
// decision-import-write), in protocol order:
//
//  1. Child-root barrier: the StateDB commit, the flush through the hash trie
//     database (triedb.Commit of the root) and SyncKeyValue.
//  2. Intend with the transition predicted by the native fork choice.
//  3. The native milestone and checkpoint check (ValidateReorg). A refusal is
//     a native rejection: nothing of the block was written; Reject.
//  4. The native write. Its final authoritative batch (the head batch, or
//     the block batch of a SIDE decision) carries the marker; then
//     SyncKeyValue and Resolve.
//
// It returns the native write status and the duration of the native check.
func (bc *BlockChain) extractorWriteDecided(decider ExtractorDecider, block *types.Block, receipts []*types.Receipt,
	logs []*types.Log, statedb *state.StateDB, setHead bool) (WriteStatus, time.Duration, error) {
	defer extractorCommits.Delete(statedb)
	root, err := extractorCommitBefore(statedb, statedb.CommitWithUpdate, block.NumberU64(),
		bc.chainConfig.IsEIP158(block.Number()), bc.chainConfig.IsCancun(block.Number()))
	if err != nil {
		return NonStatTy, 0, err
	}
	if root != block.Root() {
		return NonStatTy, 0, fmt.Errorf("%w: state root %x of block %d is not the header root %x", ErrExtractorDecision,
			root, block.NumberU64(), block.Root())
	}
	if err := bc.triedb.Commit(root, false); err != nil {
		return NonStatTy, 0, err
	}
	if err := bc.db.SyncKeyValue(); err != nil {
		return NonStatTy, 0, err
	}
	extractorStep("child-root", block)
	t, err := bc.extractorTransition(block, setHead)
	if err != nil {
		return NonStatTy, 0, err
	}
	intent, err := decider.Intend(t)
	if err != nil {
		return NonStatTy, 0, err
	}
	start := time.Now()
	isValid, err := bc.forker.ValidateReorg(block.Header(), []*types.Header{block.Header()})
	elapsed := time.Since(start)
	if err == nil && !isValid {
		err = whitelist.ErrMismatch
	}
	if err != nil {
		extractorStep("native-rejected", block)
		return NonStatTy, elapsed, errors.Join(err, intent.Reject(err))
	}
	key, value := intent.Marker()
	marker := &extractorMarker{block: block.Hash(), head: t.Kind != ExtractorSide, key: key, value: value}
	extractorMarkers.Store(bc, marker)
	var status WriteStatus
	if !setHead {
		_, err = bc.writeBlockWithState(block, receipts, logs, statedb)
	} else {
		status, err = bc.writeBlockAndSetHead(block, receipts, logs, statedb, false, false)
	}
	extractorMarkers.Delete(bc)
	if err == nil && !marker.written {
		err = fmt.Errorf("%w: the native write of block %d carried no marker", ErrExtractorDecision, block.NumberU64())
	}
	if err == nil && setHead && (status == CanonStatTy) != (t.Kind != ExtractorSide) {
		err = fmt.Errorf("%w: native status %d for a %s intent", ErrExtractorDecision, status, t.Kind)
	}
	if err == nil {
		err = bc.db.SyncKeyValue()
	}
	if err != nil {
		return status, elapsed, errors.Join(err, intent.Release())
	}
	extractorStep("marker", block)
	return status, elapsed, intent.Resolve()
}

// ExtractorRecovery is the native side of the decision recovery driver. It
// exists only inside ExtractorRecover, with the chain lock held.
type ExtractorRecovery struct {
	bc *BlockChain
}

// ExtractorRecover runs fn with the native chain lock held, so the recovery
// driver takes the chain lock before the observation guard, as the import
// path does, and no import runs while it completes saved decisions.
func (bc *BlockChain) ExtractorRecover(fn func(*ExtractorRecovery) error) error {
	if !bc.chainmu.TryLock() {
		return errChainStopped
	}
	defer bc.chainmu.Unlock()
	return fn(&ExtractorRecovery{bc: bc})
}

// Head returns the current native head.
func (r *ExtractorRecovery) Head() *types.Header { return r.bc.CurrentBlock() }

// Stored reports whether the block (header and body) is stored natively.
func (r *ExtractorRecovery) Stored(hash common.Hash, number uint64) bool {
	return r.bc.HasBlock(hash, number)
}

// Import completes the native import of an executed block from recovery
// through the decision path of the import path (extractorWriteDecided).
func (r *ExtractorRecovery) Import(decider ExtractorDecider, block *types.Block, receipts types.Receipts, logs []*types.Log,
	statedb *state.StateDB) error {
	_, _, err := r.bc.extractorWriteDecided(decider, block, receipts, logs, statedb, true)
	return err
}
