package bscdecision

// The decision evidence of a BSC block: the native fork choice and the
// Parlia finality result after EXEC_READY (D100, D99 item 2). Both are read
// through an evidence reader that serves only the capsule window, the block
// itself and the genesis; a read of any other header fails closed, so the
// block gets no decision (its observation stays EXEC_READY without one).
// The native finality paths (safe and finalized setters, VotePool) stay with
// E05-01.bsc_finality.

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
	"github.com/ethereum/go-ethereum/consensus/parlia"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/extractor/bsc"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/params"
)

// EvidenceTag is the domain tag of the decision evidence digest.
const EvidenceTag = "extractor-bsc-decision-evidence-v1"

var (
	// ErrFinality refuses a decision whose finality result is unavailable or
	// needs a header outside the capsule window.
	ErrFinality = errors.New("BSC finality result is outside the capsule window")
	// ErrEngine reports a consensus engine that is not Parlia.
	ErrEngine = errors.New("consensus engine is not Parlia")
)

// Evidence is the decision evidence of one block.
type Evidence struct {
	Kind      decision.Kind
	OldHead   decision.Head
	NewHead   decision.Head
	Justified decision.Head
	Finalized decision.Head
	Digest    string
	finalized *types.Header
}

func head(h *types.Header) decision.Head {
	return decision.Head{Number: h.Number.Uint64(), Hash: hex.EncodeToString(h.Hash().Bytes())}
}

func u64(value uint64) []byte { return binary.BigEndian.AppendUint64(nil, value) }

func headBytes(h decision.Head) []byte {
	raw, _ := hex.DecodeString(h.Hash)
	return append(u64(h.Number), raw...)
}

// EvidenceDigest is SHA-256 of the tag and a zero byte, str(kind), then the
// old head, the new head, the justified block and the finalized block, each
// as u64(number) || hash.
func EvidenceDigest(e Evidence) string {
	data := append([]byte(EvidenceTag), 0)
	data = append(data, u64(uint64(len(e.Kind)))...)
	data = append(data, e.Kind...)
	for _, h := range []decision.Head{e.OldHead, e.NewHead, e.Justified, e.Finalized} {
		data = append(data, headBytes(h)...)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (e Evidence) event(observation string) bsc.Event {
	return bsc.Event{"event": "evidence", "observation": observation, "kind": string(e.Kind),
		"old_head": e.OldHead, "new_head": e.NewHead, "justified": e.Justified, "finalized": e.Finalized,
		"digest": e.Digest}
}

// Finality is the part of Parlia that the evidence reads.
type Finality interface {
	GetJustifiedNumberAndHash(chain consensus.ChainHeaderReader, headers []*types.Header) (uint64, common.Hash, error)
	GetFinalizedHeader(chain consensus.ChainHeaderReader, header *types.Header) *types.Header
}

// DecisionEvidence returns the evidence of the decision about block on top
// of head, after EXEC_READY of the observation key: canonical is the native
// fork choice (IMPORT, or SIDE when the block does not become the head).
func DecisionEvidence(chain *core.BlockChain, seam *bsc.Seam, key string, block *types.Block, current *types.Header,
	canonical bool) (Evidence, error) {
	engine, ok := chain.Engine().(*parlia.Parlia)
	if !ok {
		return Evidence{}, ErrEngine
	}
	static, err := seam.Static(key)
	if err != nil {
		return Evidence{}, err
	}
	if static.Block.Hash() != block.Hash() {
		return Evidence{}, fmt.Errorf("%w: capsule of %s holds another block", decision.ErrBinding, key)
	}
	reader := NewEvidenceReader(chain, static, block.Header(), seam.Genesis())
	return Evaluate(engine, reader, block.Header(), current, canonical)
}

// Evaluate reads the justified and finalized blocks of header through the
// evidence reader and returns the evidence. A read outside the reader's
// headers, or a missing finality result, is ErrFinality.
func Evaluate(engine Finality, reader *EvidenceReader, header, current *types.Header, canonical bool) (Evidence,
	error) {
	e := Evidence{Kind: decision.Side, OldHead: head(current), NewHead: head(current)}
	if canonical {
		e.Kind, e.NewHead = decision.Import, head(header)
	}
	number, hash, err := engine.GetJustifiedNumberAndHash(reader, []*types.Header{header})
	if err != nil {
		return Evidence{}, fmt.Errorf("%w: justified block: %w", ErrFinality, err)
	}
	e.Justified = decision.Head{Number: number, Hash: hex.EncodeToString(hash.Bytes())}
	finalized := engine.GetFinalizedHeader(reader, header)
	// The reader keeps its first refusal: a refused read in either call
	// fails closed, even when the call still returned a result.
	if refused := reader.Err(); refused != nil {
		return Evidence{}, fmt.Errorf("%w: %w", ErrFinality, refused)
	}
	if finalized == nil {
		return Evidence{}, fmt.Errorf("%w: no finalized block for %d", ErrFinality, header.Number.Uint64())
	}
	e.Finalized, e.finalized = head(finalized), finalized
	e.Digest = EvidenceDigest(e)
	return e, nil
}

// EvidenceReader is the chain reader of the decision evidence. It serves the
// parent and the ancestor window of the capsule, the block itself and the
// genesis; any other header read, total difficulty or verified-block query
// is refused and recorded. Parlia snapshots of a served header are computed
// by the native lookup (memory cache, stored checkpoint, header walk), which
// also sees the block, because the block is not written yet. These lookups
// are after EXEC_READY and are not logged: the evidence digest binds their
// result.
type EvidenceReader struct {
	native  core.ExtractorReader
	block   *types.Header
	genesis *types.Header
	current *types.Header
	byHash  map[common.Hash]*types.Header

	mu  sync.Mutex
	err error
}

// NewEvidenceReader returns the evidence reader of block over its capsule.
func NewEvidenceReader(native core.ExtractorReader, static bsc.Static, block, genesis *types.Header) *EvidenceReader {
	r := &EvidenceReader{native: native, block: block, genesis: genesis, current: static.Parent,
		byHash: map[common.Hash]*types.Header{}}
	for _, header := range append([]*types.Header{static.Parent, block, genesis}, static.Ancestors...) {
		r.byHash[header.Hash()] = header
	}
	return r
}

// Err returns the first refused read, if any.
func (r *EvidenceReader) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *EvidenceReader) refuse(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = err
	}
}

// Config implements consensus.ChainHeaderReader.
func (r *EvidenceReader) Config() *params.ChainConfig { return r.native.Config() }

// CurrentHeader implements consensus.ChainHeaderReader: the head before the
// decision, the parent of the block.
func (r *EvidenceReader) CurrentHeader() *types.Header { return r.current }

// GenesisHeader implements consensus.ChainHeaderReader.
func (r *EvidenceReader) GenesisHeader() *types.Header { return r.genesis }

// GetHeader implements consensus.ChainHeaderReader.
func (r *EvidenceReader) GetHeader(hash common.Hash, number uint64) *types.Header {
	if header := r.byHash[hash]; header != nil && header.Number.Uint64() == number {
		return header
	}
	r.refuse(fmt.Errorf("%w: %d %x", bsc.ErrUndeclaredHeader, number, hash))
	return nil
}

// GetHeaderByHash implements consensus.ChainHeaderReader.
func (r *EvidenceReader) GetHeaderByHash(hash common.Hash) *types.Header {
	if header := r.byHash[hash]; header != nil {
		return header
	}
	r.refuse(fmt.Errorf("%w: %x", bsc.ErrUndeclaredHeader, hash))
	return nil
}

// GetHeaderByNumber implements consensus.ChainHeaderReader.
func (r *EvidenceReader) GetHeaderByNumber(number uint64) *types.Header {
	for _, header := range r.byHash {
		if header.Number.Uint64() == number {
			return header
		}
	}
	r.refuse(fmt.Errorf("%w: number %d", bsc.ErrUndeclaredHeader, number))
	return nil
}

// GetTd implements consensus.ChainHeaderReader; it is refused.
func (r *EvidenceReader) GetTd(hash common.Hash, number uint64) *big.Int {
	r.refuse(fmt.Errorf("%w: GetTd %d", bsc.ErrUndeclaredRead, number))
	return nil
}

// GetHighestVerifiedHeader implements consensus.ChainHeaderReader; it is
// refused.
func (r *EvidenceReader) GetHighestVerifiedHeader() *types.Header {
	r.refuse(fmt.Errorf("%w: GetHighestVerifiedHeader", bsc.ErrUndeclaredRead))
	return nil
}

// GetVerifiedBlockByHash implements consensus.ChainHeaderReader; it is
// refused.
func (r *EvidenceReader) GetVerifiedBlockByHash(hash common.Hash) *types.Header {
	r.refuse(fmt.Errorf("%w: GetVerifiedBlockByHash", bsc.ErrUndeclaredRead))
	return nil
}

// ExtractorSnapshot implements parlia.ExtractorSnapshotSource for the
// headers the reader serves.
func (r *EvidenceReader) ExtractorSnapshot(number uint64, hash common.Hash,
	compute func(native consensus.ChainHeaderReader) ([]byte, error)) ([]byte, error) {
	if header := r.byHash[hash]; header == nil || header.Number.Uint64() != number {
		err := fmt.Errorf("%w: snapshot of %d %x", bsc.ErrUndeclaredHeader, number, hash)
		r.refuse(err)
		return nil, err
	}
	return compute(withBlock{ExtractorReader: r.native, block: r.block})
}

// withBlock is the native reader with the block that is not written yet.
type withBlock struct {
	core.ExtractorReader
	block *types.Header
}

func (w withBlock) GetHeader(hash common.Hash, number uint64) *types.Header {
	if hash == w.block.Hash() && number == w.block.Number.Uint64() {
		return w.block
	}
	return w.ExtractorReader.GetHeader(hash, number)
}

func (w withBlock) GetHeaderByHash(hash common.Hash) *types.Header {
	if hash == w.block.Hash() {
		return w.block
	}
	return w.ExtractorReader.GetHeaderByHash(hash)
}
