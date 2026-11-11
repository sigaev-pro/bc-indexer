package bsc

// The backend of the ethAPI that Parlia receives for its system-contract
// calls. On the import path Parlia calls the validator, stake and
// turn-length contracts in Finalize (epoch blocks, Bohr turn-length checks,
// Feynman breathe blocks), each time on the parent state, addressed by the
// parent hash. The calls run on the native state, as the block's own
// execution does; the parent pin keeps that state. While an observation is
// attached, the backend refuses a call on any other block, so the import
// cannot read a state that the observation did not pin.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

// ErrSystemCallBlock refuses a system-contract call on a block other than
// the observed parent.
var ErrSystemCallBlock = errors.New("system-contract call outside the observed parent")

// SystemCallBackend is the ethapi.Backend of the system-contract calls. Only
// the methods that a call uses are implemented; the others are nil.
type SystemCallBackend struct {
	ethapi.Backend
	chain *core.BlockChain
	seam  *Seam
}

// NewSystemCallAPI returns the ethAPI to pass to parlia.New and its backend.
// Bind the backend to the chain and the seam once they exist.
func NewSystemCallAPI() (*ethapi.BlockChainAPI, *SystemCallBackend) {
	backend := &SystemCallBackend{}
	return ethapi.NewBlockChainAPI(backend), backend
}

// Bind sets the chain and the seam.
func (b *SystemCallBackend) Bind(chain *core.BlockChain, seam *Seam) { b.chain, b.seam = chain, seam }

// StateAndHeaderByNumberOrHash implements ethapi.Backend for a block hash.
func (b *SystemCallBackend) StateAndHeaderByNumberOrHash(_ context.Context,
	target rpc.BlockNumberOrHash) (*state.StateDB, *types.Header, error) {
	hash, ok := target.Hash()
	if !ok {
		return nil, nil, fmt.Errorf("%w: call by number", ErrSystemCallBlock)
	}
	if b.seam != nil {
		if o := b.seam.active.Load(); o != nil && o.reader.parent.Hash() != hash {
			err := fmt.Errorf("%w: %x, parent %x", ErrSystemCallBlock, hash, o.reader.parent.Hash())
			o.reader.refuse(err)
			return nil, nil, err
		}
	}
	header := b.chain.GetHeaderByHash(hash)
	if header == nil {
		return nil, nil, fmt.Errorf("%w: unknown block %x", ErrSystemCallBlock, hash)
	}
	statedb, err := b.chain.StateAt(header)
	if err != nil {
		return nil, nil, err
	}
	return statedb, header, nil
}

// ChainConfig implements ethapi.Backend.
func (b *SystemCallBackend) ChainConfig() *params.ChainConfig { return b.chain.Config() }

// RPCGasCap implements ethapi.Backend: no cap (Parlia sets its own gas).
func (b *SystemCallBackend) RPCGasCap() uint64 { return 0 }

// RPCEVMTimeout implements ethapi.Backend: no timeout, so a call never ends
// on the wall clock.
func (b *SystemCallBackend) RPCEVMTimeout() time.Duration { return 0 }

// Engine implements ethapi.ChainContextBackend.
func (b *SystemCallBackend) Engine() consensus.Engine { return b.chain.Engine() }

// CurrentHeader implements ethapi.ChainContextBackend.
func (b *SystemCallBackend) CurrentHeader() *types.Header { return b.chain.CurrentHeader() }

// HeaderByHash implements ethapi.ChainContextBackend.
func (b *SystemCallBackend) HeaderByHash(_ context.Context, hash common.Hash) (*types.Header, error) {
	return b.chain.GetHeaderByHash(hash), nil
}

// HeaderByNumber implements ethapi.ChainContextBackend.
func (b *SystemCallBackend) HeaderByNumber(_ context.Context, number rpc.BlockNumber) (*types.Header, error) {
	if number < 0 {
		return b.chain.CurrentHeader(), nil
	}
	return b.chain.GetHeaderByNumber(uint64(number)), nil
}
