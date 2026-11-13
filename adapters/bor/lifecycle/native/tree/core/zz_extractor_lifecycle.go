// Extractor patch (E01-02.lifecycle_seams, Bor block): the native side of the
// lifecycle gate. The preparation adds this file to the accepted Bor tree; see
// adapters/bor/lifecycle/native/README.md.

package core

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

// ErrExtractorRefused reports a native path that the capture profile refuses.
var ErrExtractorRefused = errors.New("extractor lifecycle: refused on a capture node")

// ExtractorLifecycle is the lifecycle gate of a capture chain
// (extractor/borlifecycle implements it). Only a capture node installs one; a
// chain without it keeps the native behavior.
type ExtractorLifecycle interface {
	// Enter opens a pass of the mapped caller id. chain reports that the
	// caller holds chainmu: then a pass that a chain-lock holder already
	// opened lends its guard token, else the guard is acquired. A DECISION
	// caller nested in a DECISION pass with a CANONICAL intent is covered by
	// that decision and opens no pass.
	Enter(id string, chain bool) (ExtractorPass, error)
	// Refuse enters a REFUSE caller, which the gate refuses and counts.
	Refuse(id string, chain bool) error
	// RefuseOutside counts the refusal of a REFUSE caller on a path where
	// the guard may already be held by an observation, and refuses it.
	RefuseOutside(id string) error
	// Require is the write choke-point check of an effect (EXECUTION,
	// CANONICAL, FINALITY).
	Require(effect string) error
	// Held returns the guard token that the current chain-lock holder lends,
	// or nil. Only code that holds chainmu may call it.
	Held() any
	// Floor returns the number of the live boundary H, if the run has one.
	Floor() (uint64, bool)
}

// ExtractorPass is one open pass of a mapped caller.
type ExtractorPass interface {
	// Covered reports that an outer decision covers this call.
	Covered() bool
	// Canonical records the CANONICAL intent of a head change from old to
	// next before the native mutation.
	Canonical(operation string, old, next *types.Header) error
	// Decide checks that the native head is the intent's new head, writes
	// the decision marker in a batch of its own, syncs and resolves the open
	// intent. Without an open intent it does nothing.
	Decide() error
	// Exit closes the pass and releases the guard it acquired. An open
	// intent stays unresolved for the startup reconcile.
	Exit() error
}

var extractorLifecycles sync.Map

// SetExtractorLifecycle installs the lifecycle gate of bc; nil removes it.
func (bc *BlockChain) SetExtractorLifecycle(l ExtractorLifecycle) {
	if l == nil {
		extractorLifecycles.Delete(bc)
		return
	}
	extractorLifecycles.Store(bc, l)
}

func (bc *BlockChain) extractorLifecycle() ExtractorLifecycle {
	if value, ok := extractorLifecycles.Load(bc); ok {
		return value.(ExtractorLifecycle)
	}
	return nil
}

// ExtractorLifecycleInstalled reports whether bc is a capture chain.
func (bc *BlockChain) ExtractorLifecycleInstalled() bool { return bc.extractorLifecycle() != nil }

// ExtractorHeld returns the guard token lent by the chain-lock holder, or nil.
// The decision bridge's live decider calls it from the import path, which
// holds chainmu.
func (bc *BlockChain) ExtractorHeld() any {
	if l := bc.extractorLifecycle(); l != nil {
		return l.Held()
	}
	return nil
}

// extractorRequire is the choke-point check of an effect on bc.
func (bc *BlockChain) extractorRequire(effect string) error {
	if l := bc.extractorLifecycle(); l != nil {
		return l.Require(effect)
	}
	return nil
}

// extractorRefuse refuses a REFUSE caller on a capture chain.
func (bc *BlockChain) extractorRefuse(id string, chain bool) error {
	if l := bc.extractorLifecycle(); l != nil {
		return l.Refuse(id, chain)
	}
	return nil
}

// extractorRefuseOutside refuses a REFUSE caller without the guard. It
// returns true when bc is a capture chain.
func (bc *BlockChain) extractorRefuseOutside(id string) bool {
	if l := bc.extractorLifecycle(); l != nil {
		_ = l.RefuseOutside(id)
		return true
	}
	return false
}

// extractorVias holds, per chain, the mapped caller whose nested import the
// product import path is running (recoverAncestors, insertSideChain). It is
// set and read only with chainmu held.
var extractorVias sync.Map

func (bc *BlockChain) extractorVia(id string) string {
	if value, ok := extractorVias.Load(bc); ok {
		return value.(string)
	}
	return id
}

func (bc *BlockChain) extractorWithVia(id string) func() {
	if _, ok := extractorVias.Load(bc); ok {
		return func() {}
	}
	extractorVias.Store(bc, id)
	return func() { extractorVias.Delete(bc) }
}

// extractorDecision is a DECISION pass on the native side; its zero value
// (no capture chain) does nothing.
type extractorDecision struct {
	pass ExtractorPass
}

// extractorBegin opens the pass of a DECISION caller that holds chainmu and
// records the CANONICAL intent of the head change to next.
func (bc *BlockChain) extractorBegin(id, operation string, next *types.Header) (extractorDecision, error) {
	l := bc.extractorLifecycle()
	if l == nil {
		return extractorDecision{}, nil
	}
	pass, err := l.Enter(id, true)
	if err != nil {
		return extractorDecision{}, err
	}
	if !pass.Covered() {
		if err := pass.Canonical(operation, bc.CurrentBlock(), next); err != nil {
			return extractorDecision{}, errors.Join(err, pass.Exit())
		}
	}
	return extractorDecision{pass: pass}, nil
}

func (d extractorDecision) decide() error {
	if d.pass == nil {
		return nil
	}
	return d.pass.Decide()
}

func (d extractorDecision) end() error {
	if d.pass == nil {
		return nil
	}
	return d.pass.Exit()
}

// extractorWriteDecided runs the decision path of one executed block
// (E01-02.decision_bridge) inside one pass of the import caller: the
// child-root barrier, the intent, the native write with its marker and the
// resolve hold one guard token, which the live decider borrows. The caller is
// bor.import, or the caller whose nested import this is.
func (bc *BlockChain) extractorWriteDecided(decider ExtractorDecider, block *types.Block, receipts []*types.Receipt,
	logs []*types.Log, statedb *state.StateDB, setHead bool, from *types.Header) (WriteStatus, time.Duration, error) {
	l := bc.extractorLifecycle()
	if l == nil {
		return bc.extractorNativeWriteDecided(decider, block, receipts, logs, statedb, setHead, from)
	}
	pass, err := l.Enter(bc.extractorVia("bor.import"), true)
	if err != nil {
		return NonStatTy, 0, err
	}
	status, elapsed, err := bc.extractorNativeWriteDecided(decider, block, receipts, logs, statedb, setHead, from)
	return status, elapsed, errors.Join(err, pass.Exit())
}

// writeKnownBlock activates a stored block with its state (known branch) as
// one CANONICAL decision of bor.known-block. The bridge binds every block of
// the new branch above the common ancestor to its retained EXEC_READY
// observation; a block without one refuses the intent, so the activation
// fails closed (never a silent activation).
func (bc *BlockChain) writeKnownBlock(block *types.Block) error {
	d, err := bc.extractorBegin("bor.known-block", "known-block", block.Header())
	if err != nil {
		return err
	}
	if err := bc.extractorNativeWriteKnownBlock(block); err != nil {
		return errors.Join(err, d.end())
	}
	return errors.Join(d.decide(), d.end())
}

// recoverAncestors runs the native re-execution of a pruned branch; its
// nested imports enter the gate as bor.recover-ancestors.
func (bc *BlockChain) recoverAncestors(block *types.Block, makeWitness bool) (common.Hash, error) {
	defer bc.extractorWithVia("bor.recover-ancestors")()
	return bc.extractorNativeRecoverAncestors(block, makeWitness)
}

// insertSideChain runs the native side-chain import; the re-import of its
// stateless blocks enters the gate as bor.side-chain.
func (bc *BlockChain) insertSideChain(block *types.Block, it *insertIterator, makeWitness bool) (*stateless.Witness, int, error) {
	defer bc.extractorWithVia("bor.side-chain")()
	return bc.extractorNativeInsertSideChain(block, it, makeWitness)
}

// extractorSetHeads holds the open set-head decision of a chain while
// setHeadBeyondRoot runs; set and read only with chainmu held.
var extractorSetHeads sync.Map

// extractorSetHeadLocked is setHeadBeyondRoot with chainmu held: one
// CANONICAL decision of bor.set-head for the rewind to the predicted head.
// The startup repair runs before the gate is installed and stays native.
func (bc *BlockChain) extractorSetHeadLocked(head uint64, time uint64, root common.Hash, repair bool) (uint64, error) {
	l := bc.extractorLifecycle()
	if l == nil {
		return bc.extractorNativeSetHeadLocked(head, time, root, repair)
	}
	target, err := bc.extractorRewindCheck(l, head, time)
	if err != nil {
		return 0, err
	}
	d, err := bc.extractorBegin("bor.set-head", "set-head", target)
	if err != nil {
		return 0, err
	}
	extractorSetHeads.Store(bc, d)
	number, err := bc.extractorNativeSetHeadLocked(head, time, root, repair)
	extractorSetHeads.Delete(bc)
	if err != nil {
		return number, errors.Join(err, d.end())
	}
	return number, errors.Join(d.decide(), d.end())
}

// extractorRewindCheck predicts the head of a rewind and refuses, before any
// intent or mutation, a rewind the decision could not match: no canonical
// target, a target below the live boundary H, or a target without its state
// (the native rewind would go further back than the intent).
func (bc *BlockChain) extractorRewindCheck(l ExtractorLifecycle, head uint64, time uint64) (*types.Header, error) {
	target := bc.extractorRewindHeader(head, time)
	if target == nil {
		return nil, fmt.Errorf("%w: no canonical header for the rewind to %d (time %d)", ErrExtractorRefused, head, time)
	}
	if floor, ok := l.Floor(); ok && target.Number.Uint64() < floor {
		return nil, fmt.Errorf("%w: rewind to %d is below the live boundary %d", ErrExtractorRefused,
			target.Number.Uint64(), floor)
	}
	if !bc.HasState(target.Root) {
		return nil, fmt.Errorf("%w: rewind target %d has no state", ErrExtractorRefused, target.Number.Uint64())
	}
	return target, nil
}

// extractorRewindHeader predicts the head of a rewind: the canonical header
// at head, or for a timestamp the newest canonical header not after it. The
// decision checks the native result against it.
func (bc *BlockChain) extractorRewindHeader(head uint64, time uint64) *types.Header {
	current := bc.CurrentBlock()
	if time == 0 {
		if head >= current.Number.Uint64() {
			return current
		}
		return bc.GetHeaderByNumber(head)
	}
	for h := current; h != nil; h = bc.GetHeader(h.ParentHash, h.Number.Uint64()-1) {
		if h.Time <= time || h.Number.Uint64() == 0 {
			return h
		}
	}
	return nil
}

// extractorSetHeadDecide decides the canonical part of the open set-head
// decision before setHeadBeyondRoot clears the finality references, so each
// cleared reference is a FINALITY decision of its own.
func (bc *BlockChain) extractorSetHeadDecide() error {
	value, ok := extractorSetHeads.Load(bc)
	if !ok {
		return nil
	}
	return value.(extractorDecision).decide()
}

// ExtractorReorgToFinalized is the milestone reorganization of a capture
// chain (reorgToFinalized): the rewind to rewindTo, the import of the
// canonical chain and its activation, under one chain lock, so no other
// caller runs between the steps. The rewind is one CANONICAL decision of
// bor.milestone-reorg. The native rewind deletes every block above rewindTo,
// the stored side blocks too, so the blocks of chain are then executed again
// through the import path, each with its own observation and decision, and
// SetCanonical of the last block is a decision of bor.set-canonical. The guard
// is not held across the import, whose observations take it themselves.
// moved reports that the decided rewind ran, as native reorgToFinalized
// reports a successful rewind even when the insert that follows fails.
func (bc *BlockChain) ExtractorReorgToFinalized(rewindTo uint64, chain []*types.Block, makeWitness bool) (moved bool,
	err error) {
	if len(chain) == 0 {
		return false, fmt.Errorf("%w: milestone reorganization without a chain", ErrExtractorRefused)
	}
	for i, block := range chain {
		if i > 0 && block.ParentHash() != chain[i-1].Hash() {
			return false, fmt.Errorf("%w: milestone chain is not linked at %d", ErrExtractorRefused, block.NumberU64())
		}
	}
	if err := bc.extractorRequireRewind(rewindTo); err != nil {
		return false, err
	}
	if !bc.chainmu.TryLock() {
		return false, errChainStopped
	}
	defer bc.chainmu.Unlock()
	target, err := bc.extractorRewindCheck(bc.extractorLifecycle(), rewindTo, 0)
	if err != nil {
		return false, err
	}
	d, err := bc.extractorBegin("bor.milestone-reorg", "milestone-rewind", target)
	if err != nil {
		return false, err
	}
	if _, err := bc.extractorSetHeadLocked(rewindTo, 0, common.Hash{}, false); err != nil {
		return false, errors.Join(err, d.end())
	}
	if err := errors.Join(d.decide(), d.end()); err != nil {
		return false, err
	}
	bc.chainHeadFeed.Send(ChainHeadEvent{Header: bc.CurrentBlock()})
	if _, _, err := bc.insertChain(chain, true, makeWitness); err != nil {
		return true, err
	}
	_, err = bc.extractorNativeSetCanonicalLocked(chain[len(chain)-1])
	return true, err
}

// ExtractorWriteKnownBlock activates a stored block with its state through
// writeKnownBlock, with chainmu held. On a capture chain the native entries
// do not reach writeKnownBlock: the import path inserts one block per batch,
// and the native left-trim of insertChain replaces the known-block error with
// the fork-choice result, so a known block that needs a reorganization is
// executed again under a new observation. Test only: the lifecycle probe
// calls this hook to exercise the bor.known-block decision of the patched
// function; no product path calls it.
func (bc *BlockChain) ExtractorWriteKnownBlock(block *types.Block) error {
	if !bc.HasBlockAndState(block.Hash(), block.NumberU64()) {
		return fmt.Errorf("%w: block %d is not stored with its state", ErrExtractorRefused, block.NumberU64())
	}
	if !bc.chainmu.TryLock() {
		return errChainStopped
	}
	defer bc.chainmu.Unlock()
	return bc.writeKnownBlock(block)
}

// WriteBlockAndSetHead is refused on a capture chain: self-sealed blocks are
// written without an observation or decision (D101, bor.seal-write).
func (bc *BlockChain) WriteBlockAndSetHead(block *types.Block, receipts []*types.Receipt, logs []*types.Log, state *state.StateDB, emitHeadEvent bool) (status WriteStatus, err error) {
	if err := bc.extractorRefuse("bor.seal-write", false); err != nil {
		return NonStatTy, err
	}
	return bc.extractorNativeWriteBlockAndSetHead(block, receipts, logs, state, emitHeadEvent)
}

// InsertBlockWithoutSetHead is the Engine API import, refused on a capture
// chain (bor.engine-api).
func (bc *BlockChain) InsertBlockWithoutSetHead(block *types.Block, makeWitness bool) (*stateless.Witness, error) {
	if err := bc.extractorRefuse("bor.engine-api", false); err != nil {
		return nil, err
	}
	return bc.extractorNativeInsertBlockWithoutSetHead(block, makeWitness)
}

// InsertReceiptChain is the snap-sync body and receipt import, refused on a
// capture chain (bor.snap-sync).
func (bc *BlockChain) InsertReceiptChain(blockChain types.Blocks, receiptChain []rlp.RawValue, ancientLimit uint64) (int, error) {
	if err := bc.extractorRefuse("bor.snap-sync", false); err != nil {
		return 0, err
	}
	return bc.extractorNativeInsertReceiptChain(blockChain, receiptChain, ancientLimit)
}

// InsertChainStateless is the stateless import (sequential and parallel),
// refused on a capture chain (bor.stateless).
func (bc *BlockChain) InsertChainStateless(chain types.Blocks, witnesses []*stateless.Witness) (int, error) {
	if err := bc.extractorRefuse("bor.stateless", false); err != nil {
		return 0, err
	}
	return bc.extractorNativeInsertChainStateless(chain, witnesses)
}
