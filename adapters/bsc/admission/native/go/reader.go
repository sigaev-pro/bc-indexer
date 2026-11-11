// Package bsc wires the admission core (E01-02.admission) and the source
// inputs core (E01-02.inputs) into the BSC native import path.
//
// The extractor patch set adds the import seam to core.BlockChain; Seam
// implements it. The driver test/harness/bsc_admission.py installs this
// package at extractor/bsc of the accepted BSC source tree, next to the shared
// packages extractor/admission, extractor/source_inputs and
// extractor/state_provider, and applies the patches in ../patches.
package bsc

// The chain reader of one observation. While an observation is attached,
// header verification, Process, Finalize and BLOCKHASH read headers only
// through it (core patch), so every header that the import reads before
// EXEC_READY is a static capsule input: the parent and the BLOCKHASH window.
// A read of any other header stops the observation, which then stays
// ADMITTED: it fails closed and is never recorded as NATIVE_REJECTED.
//
// Parlia snapshots are logged dependency responses (Parlia patch): when the
// engine looks up a snapshot through this reader, the reader logs the
// request (block number and hash) and saves the native lookup's result, the
// snapshot's JSON, before Parlia receives it; a retry receives the saved
// bytes. The native lookup (memory cache, stored checkpoint snapshot, header
// walk with FindAncientHeader at a validator-set switch) runs on the native
// chain, so its own reads are not observation reads, and the response does
// not depend on what the engine has in memory.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
	"github.com/ethereum/go-ethereum/params"
)

// SnapshotTag is the request tag of a logged Parlia snapshot. A request is
// the tag, one zero byte, the block number (8 bytes, big-endian) and the
// block hash.
const SnapshotTag = "bsc-parlia-snapshot-v1"

var (
	// ErrUndeclaredHeader stops an observation whose import read a header
	// that is not a static input of its capsule.
	ErrUndeclaredHeader = errors.New("observation read an undeclared header")
	// ErrUndeclaredRead stops an observation whose import used a chain read
	// that has no static or logged form (total difficulty, verified-block
	// queries).
	ErrUndeclaredRead = errors.New("observation used an undeclared chain read")
	// ErrWiring reports a misuse of the seam.
	ErrWiring = errors.New("BSC seam wiring error")
)

// Event is one trace record of the seam.
type Event map[string]any

// SnapshotRequest returns the logged request of the snapshot of one block.
func SnapshotRequest(number uint64, hash common.Hash) []byte {
	request := append([]byte(SnapshotTag), 0)
	request = binary.BigEndian.AppendUint64(request, number)
	return append(request, hash.Bytes()...)
}

// Reader is the chain reader of one observation. It is safe for concurrent
// use: header verification runs on its own goroutine.
type Reader struct {
	native  core.ExtractorReader
	parent  *types.Header
	genesis *types.Header
	byHash  map[common.Hash]*types.Header

	mu      sync.Mutex
	id      string
	log     *sourceinputs.Log
	err     error
	emit    func(Event)
	fetch   func()
	handoff func(observation string, sequence uint64, response []byte)
}

// newReader returns the reader of an observation over its checked capsule.
func newReader(id string, static Static, genesis *types.Header, log *sourceinputs.Log, emit func(Event),
	fetch func(), handoff func(string, uint64, []byte)) *Reader {
	r := &Reader{parent: static.Parent, genesis: genesis, byHash: map[common.Hash]*types.Header{},
		id: id, log: log, emit: emit, fetch: fetch, handoff: handoff}
	for _, header := range append([]*types.Header{static.Parent}, static.Ancestors...) {
		r.byHash[header.Hash()] = header
	}
	return r
}

// bind sets the native chain; core passes it with every Reader call.
func (r *Reader) bind(native core.ExtractorReader) *Reader {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.native == nil {
		r.native = native
	}
	return r
}

// Err returns the first error that stopped the observation, if any.
func (r *Reader) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *Reader) stop(err error) {
	if r.err == nil {
		r.err = err
		r.emit(Event{"event": "reader-stop", "observation": r.id, "error": err.Error()})
	}
}

func (r *Reader) refuse(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stop(err)
}

// Config implements consensus.ChainHeaderReader.
func (r *Reader) Config() *params.ChainConfig { return r.native.Config() }

// Engine implements core.ChainContext.
func (r *Reader) Engine() consensus.Engine { return r.native.Engine() }

// CurrentHeader implements consensus.ChainHeaderReader: during the import
// the head is the parent (Begin requires it).
func (r *Reader) CurrentHeader() *types.Header { return r.parent }

// GenesisHeader implements consensus.ChainHeaderReader: the genesis is a
// static input (genesis-json).
func (r *Reader) GenesisHeader() *types.Header { return r.genesis }

// GetHeader implements consensus.ChainHeaderReader.
func (r *Reader) GetHeader(hash common.Hash, number uint64) *types.Header {
	if header := r.byHash[hash]; header != nil && header.Number.Uint64() == number {
		return header
	}
	r.refuse(fmt.Errorf("%w: %d %x", ErrUndeclaredHeader, number, hash))
	return nil
}

// GetHeaderByHash implements consensus.ChainHeaderReader.
func (r *Reader) GetHeaderByHash(hash common.Hash) *types.Header {
	if header := r.byHash[hash]; header != nil {
		return header
	}
	r.refuse(fmt.Errorf("%w: %x", ErrUndeclaredHeader, hash))
	return nil
}

// GetHeaderByNumber implements consensus.ChainHeaderReader.
func (r *Reader) GetHeaderByNumber(number uint64) *types.Header {
	for _, header := range r.byHash {
		if header.Number.Uint64() == number {
			return header
		}
	}
	r.refuse(fmt.Errorf("%w: number %d", ErrUndeclaredHeader, number))
	return nil
}

// GetTd implements consensus.ChainHeaderReader; total difficulty is not an
// execution input.
func (r *Reader) GetTd(hash common.Hash, number uint64) *big.Int {
	r.refuse(fmt.Errorf("%w: GetTd %d", ErrUndeclaredRead, number))
	return nil
}

// GetHighestVerifiedHeader implements consensus.ChainHeaderReader.
func (r *Reader) GetHighestVerifiedHeader() *types.Header {
	r.refuse(fmt.Errorf("%w: GetHighestVerifiedHeader", ErrUndeclaredRead))
	return nil
}

// GetVerifiedBlockByHash implements consensus.ChainHeaderReader.
func (r *Reader) GetVerifiedBlockByHash(hash common.Hash) *types.Header {
	r.refuse(fmt.Errorf("%w: GetVerifiedBlockByHash", ErrUndeclaredRead))
	return nil
}

// ExtractorSnapshot implements parlia.ExtractorSnapshotSource. Only the
// snapshot of a capsule header may be requested.
func (r *Reader) ExtractorSnapshot(number uint64, hash common.Hash,
	compute func(native consensus.ChainHeaderReader) ([]byte, error)) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	if header := r.byHash[hash]; header == nil || header.Number.Uint64() != number {
		err := fmt.Errorf("%w: snapshot of %d %x", ErrUndeclaredHeader, number, hash)
		r.stop(err)
		return nil, err
	}
	request := SnapshotRequest(number, hash)
	response, err := r.log.Next(request, func() ([]byte, error) {
		r.fetch()
		r.emit(Event{"event": "fetch", "observation": r.id, "tag": SnapshotTag, "number": number})
		return compute(r.native)
	})
	if err != nil {
		// Any error ends the observation, including a native lookup error:
		// it is an input failure, not a native decision about the block.
		r.stop(err)
		return nil, err
	}
	sequence := r.log.Cursor() - 1
	digest := sha256.Sum256(response)
	r.emit(Event{"event": "handoff", "observation": r.id, "sequence": sequence,
		"request_hex": hex.EncodeToString(request), "response_hex": hex.EncodeToString(response),
		"response_sha256": hex.EncodeToString(digest[:])})
	if r.handoff != nil {
		r.handoff(r.id, sequence, response)
	}
	return response, nil
}
