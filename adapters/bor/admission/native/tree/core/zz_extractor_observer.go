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
)

// ExtractorObserver opens one observation per imported block. Observe runs
// before ProcessBlock and must return only after the closing ADMITTED sync.
type ExtractorObserver interface {
	Observe(block *types.Block, parent *types.Header) (ExtractorObservation, error)
}

// ExtractorObservation receives the native result of ProcessBlock: the
// receipts, the gas used and the execution and validation error.
type ExtractorObservation interface {
	Done(block *types.Block, receipts types.Receipts, usedGas uint64, err error) error
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
// is installed, so no header verification of a later block runs inside the
// observation window of an earlier one. It keeps the native result shape:
// the index of the failing block, or the batch length.
func (bc *BlockChain) extractorInsertEach(chain types.Blocks, makeWitness bool, witnesses []*stateless.Witness) (int, error) {
	for i := range chain {
		var one []*stateless.Witness
		if witnesses != nil {
			one = witnesses[i : i+1]
		}
		n, err := bc.InsertChainWithWitnesses(chain[i:i+1], makeWitness, one)
		if err != nil {
			return i + n, err
		}
	}
	return len(chain), nil
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
	if doneErr := observation.Done(block, receipts, usedGas, err); doneErr != nil {
		err = doneErr
	}
	return receipts, logs, usedGas, statedb, vtime, err
}
