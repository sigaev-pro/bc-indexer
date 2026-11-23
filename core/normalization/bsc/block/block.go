// Package block is the block-record area of the BSC normalization module
// (E04-01.block_record).
//
// It projects one admitted native BSC block into the typed BlockRecord
// view of protocol/block: the common header with per-fork optional
// presence, the exact source RLP, the observed source transaction count
// (zero for an empty block), the BSC family details with the observed
// Parlia system-transaction count, and the source-precision timestamp.
//
// BSC timestamp rule: the pinned client computes MilliTimestamp as
// Time*1000 + low_u64(MixDigest); the projection keeps that native rule
// with checked arithmetic and stores seconds=ms/1000,
// nanos=(ms%1000)*1e6, precision=MILLISECONDS. The raw header Time and
// MixDigest stay unchanged in the header; the millisecond derivation is a
// BSC rule, not an interpretation of MixDigest as a timestamp in the other
// families.
//
// Normative anchors:
//
//   - .project/docs/CURRENT_SEMANTICS.md, "Block, calls and account
//     changes" (BlockRef.Timestamp, header fields, empty block)
//   - .project/docs/MESSAGE_CONTRACT.md, section 4 (BlockApplied)
//   - pinned BSC core/types/block.go MilliTimestamp (commit 54afaff4,
//     cited by adapters/bsc/type_mapping/paths.json)
package block

import (
	"errors"
	"fmt"
	"math"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// Sentinel errors of the projection.
var (
	ErrMilliOverflow  = errors.New("bsc/block: header time overflows the millisecond timestamp")
	ErrEmptySourceRLP = errors.New("bsc/block: header source_rlp is mandatory for the BSC profile")
)

// SourceHeader is the typed native header of one pinned BSC block. Common
// fields are always present; optional fork fields carry pointer presence:
// a present zero (for example a pre-London base fee 0) stays a present
// value, and an absent field is never filled with a zero.
type SourceHeader struct {
	// Hash is the native block hash from the source. The projection keeps
	// it as given and never recomputes a whole-block hash.
	Hash             block.Hash32
	ParentHash       block.Hash32
	UncleHash        block.Hash32
	Beneficiary      block.Address20
	StateRoot        block.Hash32
	TransactionsRoot block.Hash32
	ReceiptsRoot     block.Hash32
	LogsBloom        block.Bloom
	Difficulty       block.UInt256
	Number           uint64
	GasLimit         uint64
	GasUsed          uint64
	Time             uint64
	ExtraData        []byte
	MixDigest        block.Hash32
	Nonce            block.Nonce

	// Optional fork fields, by native presence.
	BaseFee               *block.UInt256
	WithdrawalsRoot       *block.Hash32
	BlobGasUsed           *uint64
	ExcessBlobGas         *uint64
	ParentBeaconBlockRoot *block.Hash32
	RequestsHash          *block.Hash32

	// SourceRLP is the exact native header encoding.
	SourceRLP []byte
}

// MilliTimestamp computes the pinned native BSC millisecond timestamp
// Time*1000 + low_u64(MixDigest) with checked arithmetic. Mixing the
// millisecond remainder into the low bits of MixDigest is the native BSC
// header rule (Lorentz and later); before that fork the remainder is zero
// and the rule still holds.
func MilliTimestamp(timeSeconds uint64, mixDigest block.Hash32) (uint64, error) {
	const millisPerSecond = 1000
	if timeSeconds > (1<<64-1)/millisPerSecond {
		return 0, fmt.Errorf("%w: time %d", ErrMilliOverflow, timeSeconds)
	}
	ms := timeSeconds * millisPerSecond
	// low_u64 of the mix digest, big endian in the native header bytes.
	low := uint64(mixDigest[24])<<56 | uint64(mixDigest[25])<<48 | uint64(mixDigest[26])<<40 | uint64(mixDigest[27])<<32 |
		uint64(mixDigest[28])<<24 | uint64(mixDigest[29])<<16 | uint64(mixDigest[30])<<8 | uint64(mixDigest[31])
	if ms > math.MaxUint64-low {
		return 0, fmt.Errorf("%w: time %d mix %x", ErrMilliOverflow, timeSeconds, mixDigest)
	}
	return ms + low, nil
}

// Timestamp projects the native header time into the source-precision
// BlockRef timestamp: seconds=ms/1000, nanos=(ms%1000)*1e6, precision
// MILLISECONDS.
func Timestamp(timeSeconds uint64, mixDigest block.Hash32) (block.Timestamp, error) {
	ms, err := MilliTimestamp(timeSeconds, mixDigest)
	if err != nil {
		return block.Timestamp{}, err
	}
	return block.TimestampFromMilliseconds(ms), nil
}

// ParliaFacts are the BSC family observations of one block import.
type ParliaFacts struct {
	// SystemTransactionCount is the observed count of Parlia-classified
	// native system transactions in this block body. It is an observation
	// of the source classification: never a completeness claim for the
	// block, and never a substitute for the per-transaction
	// classifications. Zero is a valid observed value.
	SystemTransactionCount *uint64
}

// Project builds the typed block metadata of one BSC block. txCount is
// the observed source transaction count of the native body
// representation: zero is the observed empty block.
func Project(source SourceHeader, txCount uint64, facts ParliaFacts) (block.BlockMetadata, error) {
	if len(source.SourceRLP) == 0 {
		return block.BlockMetadata{}, ErrEmptySourceRLP
	}
	if source.Hash.IsZero() {
		return block.BlockMetadata{}, block.ErrInvalidValue
	}
	timestamp, err := Timestamp(source.Time, source.MixDigest)
	if err != nil {
		return block.BlockMetadata{}, err
	}
	header := block.Header{
		ParentHash:       source.ParentHash,
		UncleHash:        source.UncleHash,
		Beneficiary:      source.Beneficiary,
		StateRoot:        source.StateRoot,
		TransactionsRoot: source.TransactionsRoot,
		ReceiptsRoot:     source.ReceiptsRoot,
		LogsBloom:        source.LogsBloom,
		Difficulty:       source.Difficulty,
		Number:           source.Number,
		GasLimit:         source.GasLimit,
		GasUsed:          source.GasUsed,
		TimestampSeconds: source.Time,
		ExtraData:        source.ExtraData,
		MixDigest:        source.MixDigest,
		Nonce:            source.Nonce,
		SourceRLP:        source.SourceRLP,
	}
	if source.BaseFee != nil {
		header.BaseFee = block.Some(*source.BaseFee)
	}
	if source.WithdrawalsRoot != nil {
		header.WithdrawalsRoot = block.Some(*source.WithdrawalsRoot)
	}
	if source.BlobGasUsed != nil {
		header.BlobGasUsed = block.Some(*source.BlobGasUsed)
	}
	if source.ExcessBlobGas != nil {
		header.ExcessBlobGas = block.Some(*source.ExcessBlobGas)
	}
	if source.ParentBeaconBlockRoot != nil {
		header.ParentBeaconBlockRoot = block.Some(*source.ParentBeaconBlockRoot)
	}
	if source.RequestsHash != nil {
		header.RequestsHash = block.Some(*source.RequestsHash)
	}
	details := block.BscBlockDetails{}
	if facts.SystemTransactionCount != nil {
		details.ParliaSystemTransactionCount = block.Some(*facts.SystemTransactionCount)
	}
	metadata := block.BlockMetadata{
		Number:                 source.Number,
		Hash:                   source.Hash,
		ParentHash:             source.ParentHash,
		Timestamp:              timestamp,
		SourceTransactionCount: txCount,
		Header:                 header,
		Details:                block.FamilyDetails{Bsc: &details},
	}
	if err := metadata.Validate(); err != nil {
		return block.BlockMetadata{}, err
	}
	return metadata, nil
}
