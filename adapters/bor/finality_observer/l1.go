package finalityobserver

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// ErrMalformed rejects a malformed RootChain result: a decode that is not
// the exact static shape, or a field that cannot be a header block. This is
// an explicit source error, never an Unavailable-success.
var ErrMalformed = errors.New("finality_observer: malformed source result")

// HeaderBlocksSelector is the closed function selector of
// RootChain.headerBlocks(uint256): 0x41539d4a (keccak of the signature's
// first four bytes, pinned by the Polygon research note).
var HeaderBlocksSelector = [4]byte{0x41, 0x53, 0x9d, 0x4a}

// HeaderBlockWordCount is the exact word count of the static
// HeaderBlock{return start,end,createdAt,root,proposer} result: five
// 32-byte words, nothing else.
const HeaderBlockWordCount = 5

// HeaderBlockID computes the checked header block id:
// checkpoint_id * child_chain_block_interval as a UInt256. An overflow is
// an explicit error, never a wrapped value.
func HeaderBlockID(checkpointID, childBlockInterval uint64) (block.UInt256, error) {
	if checkpointID == 0 || childBlockInterval == 0 {
		return block.UInt256{}, fmt.Errorf("%w: zero checkpoint id or interval", ErrMalformed)
	}
	product := new(big.Int).Mul(new(big.Int).SetUint64(checkpointID), new(big.Int).SetUint64(childBlockInterval))
	if product.BitLen() > 256 {
		return block.UInt256{}, fmt.Errorf("%w: header block id overflow", ErrMalformed)
	}
	var out block.UInt256
	product.FillBytes(out[:])
	return out, nil
}

// HeaderBlocksCalldata builds the exact calldata of the hash-pinned read:
// the closed selector and the 32-byte big-endian header block id.
func HeaderBlocksCalldata(headerBlockID block.UInt256) []byte {
	out := make([]byte, 4+32)
	copy(out, HeaderBlocksSelector[:])
	copy(out[4:], headerBlockID[:])
	return out
}

// HeaderBlockResult is the strictly decoded HeaderBlock result.
type HeaderBlockResult struct {
	Start     uint64
	End       uint64
	CreatedAt block.UInt256
	Root      block.Hash32
	Proposer  block.Address20
}

// DecodeHeaderBlockResult strictly decodes the exact result bytes of the
// RootChain.headerBlocks read: exactly five 32-byte words — start, end,
// createdAt, root, proposer — with start and end as full u64 values in
// their low 8 bytes and the proposer as a 20-byte address in the low bytes
// of its word. Anything else — a short or long result, a dirty upper half
// of the u64 words, a range with end below start — is ErrMalformed.
func DecodeHeaderBlockResult(raw []byte) (HeaderBlockResult, error) {
	if len(raw) != HeaderBlockWordCount*32 {
		return HeaderBlockResult{}, fmt.Errorf("%w: result length %d, want %d", ErrMalformed, len(raw), HeaderBlockWordCount*32)
	}
	word := func(i int) []byte { return raw[i*32 : (i+1)*32] }
	start, err := wordU64(word(0))
	if err != nil {
		return HeaderBlockResult{}, err
	}
	end, err := wordU64(word(1))
	if err != nil {
		return HeaderBlockResult{}, err
	}
	if end < start {
		return HeaderBlockResult{}, fmt.Errorf("%w: end %d below start %d", ErrMalformed, end, start)
	}
	var out HeaderBlockResult
	out.Start, out.End = start, end
	copy(out.CreatedAt[:], word(2))
	copy(out.Root[:], word(3))
	if err := addressFromWord(word(4), &out.Proposer); err != nil {
		return HeaderBlockResult{}, err
	}
	if out.Root.IsZero() {
		// A zero root is the never-written header block: the contract
		// shape exists but the checkpoint was never committed there. For
		// a verified binding that is a malformed result, not an anchor.
		return HeaderBlockResult{}, fmt.Errorf("%w: zero root", ErrMalformed)
	}
	if out.Proposer.IsZero() {
		return HeaderBlockResult{}, fmt.Errorf("%w: zero proposer", ErrMalformed)
	}
	return out, nil
}

// wordU64 decodes a full u64 in the low 8 bytes of one 32-byte word; the
// upper 24 bytes must be zero.
func wordU64(w []byte) (uint64, error) {
	for _, b := range w[:24] {
		if b != 0 {
			return 0, fmt.Errorf("%w: dirty upper bytes of a u64 word", ErrMalformed)
		}
	}
	var v uint64
	for _, b := range w[24:] {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

// addressFromWord decodes a 20-byte address in the low bytes of one
// 32-byte word; the upper 12 bytes must be zero.
func addressFromWord(w []byte, out *block.Address20) error {
	for _, b := range w[:12] {
		if b != 0 {
			return fmt.Errorf("%w: dirty upper bytes of an address word", ErrMalformed)
		}
	}
	copy(out[:], w[12:])
	return nil
}
