// Extractor patch (E01-02.admission, Bor block): the per-block observation
// seam of the product import path. The preparation adds this file to the
// accepted Bor tree; see adapters/bor/admission/native/README.md.

package core

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/extractor/retention"
)

// ExtractorObserver opens one observation per imported block. Observe runs
// before ProcessBlock and must return only after the closing ADMITTED sync.
type ExtractorObserver interface {
	Observe(block *types.Block, parent *types.Header) (ExtractorObservation, error)
}

// ExtractorObservation receives the native result of ProcessBlock. Done gets
// the receipts, the gas used and the execution and validation error. Fail
// gets an error that says nothing about the block (local state that native
// code could not read); the obligation then stays open.
type ExtractorObservation interface {
	Done(block *types.Block, receipts types.Receipts, usedGas uint64, err error) error
	Fail(err error) error
}

// extractorObservers maps a chain to its observer. A package map keeps the
// BlockChain struct unchanged.
var extractorObservers sync.Map

// SetExtractorObserver installs the observer of bc; nil removes it.
func (bc *BlockChain) SetExtractorObserver(observer ExtractorObserver) {
	if observer == nil {
		extractorObservers.Delete(bc)
		return
	}
	extractorObservers.Store(bc, observer)
}

func (bc *BlockChain) extractorObserver() ExtractorObserver {
	if value, ok := extractorObservers.Load(bc); ok {
		return value.(ExtractorObserver)
	}
	return nil
}

// extractorInsertEach imports a batch one block at a time while an observer
// is installed, so the header verification of a later block in the batch
// never runs inside the observation window of an earlier one. Every batch
// import passes here, the side-chain re-import too. It keeps the native
// result shape: the index of the failing block, or the batch length.
func (bc *BlockChain) extractorInsertEach(chain types.Blocks, setHead bool, makeWitness bool, witnesses []*stateless.Witness) (*stateless.Witness, int, error) {
	var witness *stateless.Witness
	for i := range chain {
		var one []*stateless.Witness
		if witnesses != nil {
			one = witnesses[i : i+1]
		}
		var n int
		var err error
		witness, n, err = bc.insertChainWithWitnesses(chain[i:i+1], setHead, makeWitness, one)
		if err != nil {
			return witness, i + n, err
		}
	}
	return witness, len(chain), nil
}

// extractorProcess runs one block inside its observation. Preconfirmed
// results are never used: the block is executed by ProcessBlock after the
// closing ADMITTED sync.
func (bc *BlockChain) extractorProcess(observer ExtractorObserver, block *types.Block, parent *types.Header, witness *stateless.Witness, interrupt *atomic.Bool, pipeOpts *PipelineImportOpts) (types.Receipts, []*types.Log, uint64, *state.StateDB, time.Duration, error) {
	observation, err := observer.Observe(block, parent)
	if err != nil {
		return nil, nil, 0, nil, 0, err
	}
	receipts, logs, usedGas, statedb, vtime, err := bc.ProcessBlock(block, parent, witness, interrupt, pipeOpts)
	var closeErr error
	if err != nil && ExtractorLocalStateUnavailable(err) {
		closeErr = observation.Fail(err)
	} else {
		closeErr = observation.Done(block, receipts, usedGas, err)
	}
	if closeErr != nil {
		err = closeErr
	}
	return receipts, logs, usedGas, statedb, vtime, err
}

// extractorRequireRewind refuses a head rewind to head that would delete a
// pinned header (D98 item 2).
func (bc *BlockChain) extractorRequireRewind(head uint64) error {
	if retention.RequireRewindAllowed == nil {
		return nil
	}
	return retention.RequireRewindAllowed(bc.db, head)
}

// extractorRewindTarget returns the number of the newest canonical header at
// or below timestamp, the head that SetHeadWithTimestamp rewinds to.
func (bc *BlockChain) extractorRewindTarget(timestamp uint64) uint64 {
	header := bc.CurrentBlock()
	for header != nil && header.Number.Uint64() > 0 && header.Time > timestamp {
		header = bc.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	}
	if header == nil {
		return 0
	}
	return header.Number.Uint64()
}

// ExtractorLocalStateUnavailable reports whether err from ProcessBlock means
// that native code could not read local state (a missing or unreadable trie
// node), which says nothing about the block.
func ExtractorLocalStateUnavailable(err error) bool { return isLocalStateUnavailable(err) }
