package poly

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// FinalityMarker is the block-record finality section of the Polygon
// family: the Heimdall milestone and checkpoint references observed with
// this block. A reference is an observation, not a coverage claim: block
// ancestry coverage is decided only against the evidence schema of the
// lifecycle owner (CURRENT_SEMANTICS D63), and an empty list means no
// covering reference was observed, not that none exists.
type FinalityMarker struct {
	HeimdallCheckpoints []block.HeimdallCheckpointRef
}

// BlockRecord is the normalized Polygon block record: the block metadata
// with the Bor family context, the finality marker, the normalized body
// transactions and, when the optional scope is enabled and the source has
// one, the separate legacy synthetic receipt record. The legacy record
// never increases the body transaction count.
type BlockRecord struct {
	Metadata      block.BlockMetadata
	Finality      FinalityMarker
	Transactions  []*TransactionRecord
	LegacyReceipt *BorLegacyReceiptRecord
}

// NormalizeBlock normalizes one captured Bor block: it builds the block
// metadata with the Bor family details (sprint and side-chain context,
// StateSync count and mode, Heimdall references), normalizes every body
// transaction, and enforces the block-level coherence rules: the StateSync
// carrier split around Madhugiri, the sprint boundary of the system work,
// the header gas rollup and the body inventory.
func NormalizeBlock(cap *BlockCapture, opts Options) (*BlockRecord, error) {
	if cap.Header.Number != cap.Number {
		return nil, fmt.Errorf("block number %d but header number %d: %w", cap.Number, cap.Header.Number, ErrBlock)
	}
	if cap.Header.ParentHash != cap.ParentHash {
		return nil, fmt.Errorf("block parent hash differs from header parent hash: %w", ErrBlock)
	}
	if cap.Header.TimestampSeconds != cap.TimeSeconds {
		return nil, fmt.Errorf("block time differs from header time: %w", ErrBlock)
	}

	metadata := block.BlockMetadata{
		Number:                 cap.Number,
		Hash:                   cap.Hash,
		ParentHash:             cap.ParentHash,
		Timestamp:              block.TimestampFromSeconds(cap.TimeSeconds),
		SourceTransactionCount: uint64(len(cap.Transactions)),
		Header:                 buildHeader(cap.Header),
	}

	details := &block.BorBlockDetails{}
	if cap.SprintLength > 0 {
		details.SprintLength = block.Some(cap.SprintLength)
		details.SprintEnd = block.Some(cap.sprintBoundary())
	}
	details.SideChainBlock = block.Some(cap.SideChain)
	details.StateSyncCount = block.Some(cap.StateSyncRows)

	// The carrier split of D17/Madhugiri: before the gate the system work
	// is a separate end-of-block commit (the optional legacy record); from
	// the gate on it is the final body transaction. The two carriers never
	// mix and never double the same events.
	stateSyncBody := false
	for i := range cap.Transactions {
		tx := &cap.Transactions[i]
		if tx.NativeType != StateSyncTxType {
			continue
		}
		if stateSyncBody {
			return nil, fmt.Errorf("two body state-sync transactions: %w", ErrBlock)
		}
		stateSyncBody = true
		if !cap.Forks.StateSyncAt(cap.Number) {
			return nil, fmt.Errorf("%w: 0x7f body transaction at block %d before Madhugiri block %d: %w",
				ErrBlock, cap.Number, cap.Forks.MadhugiriBlock, ErrTypeNotApplicable)
		}
		if uint64(i) != uint64(len(cap.Transactions)-1) {
			return nil, fmt.Errorf("%w: 0x7f body transaction at non-final position %d: %w",
				ErrBlock, i, ErrStateSyncMismatch)
		}
		if cap.StateSyncRows != uint64(len(tx.StateSync.Records)) {
			return nil, fmt.Errorf("%w: block observed %d state-sync rows, body carries %d: %w",
				ErrBlock, cap.StateSyncRows, len(tx.StateSync.Records), ErrStateSyncMismatch)
		}
		details.StateSyncMode = block.Some(block.StateSyncBodyTransaction)
	}
	if !stateSyncBody && cap.StateSyncRows > 0 {
		if cap.Forks.StateSyncAt(cap.Number) {
			return nil, fmt.Errorf("%w: %d state-sync rows without the final body transaction: %w",
				ErrBlock, cap.StateSyncRows, ErrStateSyncMismatch)
		}
		details.StateSyncMode = block.Some(block.StateSyncEndOfBlockCommit)
	}

	// The system work commits at the sprint boundary: rows require the
	// boundary flag, and a known sprint length must agree with it.
	if cap.StateSyncRows > 0 && cap.SprintLength > 0 && !cap.sprintBoundary() {
		return nil, fmt.Errorf("%w: %d state-sync rows at block %d off the sprint boundary %d: %w",
			ErrBlock, cap.StateSyncRows, cap.Number, cap.SprintLength, ErrCapture)
	}

	// Heimdall references of the block record and its finality marker.
	refs := make([]block.HeimdallCheckpointRef, 0, len(cap.HeimdallCheckpoints))
	for _, ref := range cap.HeimdallCheckpoints {
		anchor := block.AnchorRef{Height: ref.EndHeight, Hash: ref.EndHash}
		kind := block.CheckpointMilestone
		if ref.Kind == HeimdallCheckpoint {
			kind = block.CheckpointArea
		}
		row := block.HeimdallCheckpointRef{Kind: kind, End: anchor}
		if ref.CheckpointID != nil {
			row.CheckpointID = block.Some(*ref.CheckpointID)
		}
		refs = append(refs, row)
	}
	details.HeimdallCheckpoints = refs
	metadata.Details = block.FamilyDetails{Bor: details}

	record := &BlockRecord{
		Metadata:     metadata,
		Finality:     FinalityMarker{HeimdallCheckpoints: refs},
		Transactions: make([]*TransactionRecord, 0, len(cap.Transactions)),
	}

	for i := range cap.Transactions {
		tx := &cap.Transactions[i]
		tx.BlockNumber = cap.Number
		tx.Forks = cap.Forks
		if tx.BodyTransactionCount == 0 {
			tx.BodyTransactionCount = uint64(len(cap.Transactions))
		}
		normalized, err := NormalizeTransaction(tx)
		if err != nil {
			return nil, fmt.Errorf("transaction %d: %w", i, err)
		}
		normalized.BlockHash = cap.Hash
		record.Transactions = append(record.Transactions, normalized)
	}

	// The header gas rollup: the last body receipt carries the header total.
	if len(record.Transactions) > 0 {
		last := record.Transactions[len(record.Transactions)-1]
		if last.Receipt != nil && last.Receipt.CumulativeGasUsed != cap.Header.GasUsed {
			return nil, fmt.Errorf("%w: header gas %d differs from the last receipt cumulative %d: %w",
				ErrBlock, cap.Header.GasUsed, last.Receipt.CumulativeGasUsed, ErrCapture)
		}
	}

	// The optional legacy record: pre-Madhugiri only, never alongside the
	// body carrier, and never counted in the body inventory. With the
	// optional scope OFF (the default) the record is simply not published;
	// refusing the extension never blocks the mandatory transaction scope.
	if cap.LegacyReceipt != nil {
		if stateSyncBody {
			return nil, fmt.Errorf("%w: legacy receipt alongside the body state-sync transaction: %w",
				ErrBlock, ErrStateSyncMismatch)
		}
		if opts.LegacyReceipt {
			legacy, err := NormalizeLegacyReceipt(cap.LegacyReceipt, opts)
			if err != nil {
				return nil, err
			}
			if legacy != nil {
				if legacy.BlockNumber != cap.Number || legacy.BlockHash != cap.Hash {
					return nil, fmt.Errorf("%w: legacy receipt bound to another block: %w", ErrBlock, ErrLegacySource)
				}
				if legacy.SyntheticReceiptIndex != uint64(len(cap.Transactions)) {
					return nil, fmt.Errorf("%w: legacy synthetic index %d differs from the body count %d: %w",
						ErrBlock, legacy.SyntheticReceiptIndex, len(cap.Transactions), ErrLegacySource)
				}
				if legacy.SourceReceipt.CumulativeGasUsed != cap.Header.GasUsed {
					return nil, fmt.Errorf("%w: legacy cumulative gas differs from the header total: %w",
						ErrBlock, ErrLegacySource)
				}
				record.LegacyReceipt = legacy
			}
		}
	}

	if err := metadata.Validate(); err != nil {
		return nil, fmt.Errorf("block metadata: %w", err)
	}
	return record, nil
}

// sprintBoundary reports the pinned native sprint rule: the system work of
// Bor Finalize runs at the sprint start boundary, IsSprintStart(n, s) =
// n % s == 0 (bor.go of the pinned source), which the block schema records
// as the sprint-end observation of the preceding sprint.
func (c *BlockCapture) sprintBoundary() bool {
	return c.SprintLength > 0 && c.Number > 0 && c.Number%c.SprintLength == 0
}

// buildHeader maps the captured header fields onto the typed common header
// with optional fork fields by presence only.
func buildHeader(h HeaderCapture) block.Header {
	out := block.Header{
		ParentHash:       h.ParentHash,
		UncleHash:        h.UncleHash,
		Beneficiary:      block.Address20(h.Beneficiary),
		StateRoot:        h.StateRoot,
		TransactionsRoot: h.TransactionsRoot,
		ReceiptsRoot:     h.ReceiptsRoot,
		LogsBloom:        h.LogsBloom,
		Difficulty:       block.UInt256(h.Difficulty),
		Number:           h.Number,
		GasLimit:         h.GasLimit,
		GasUsed:          h.GasUsed,
		TimestampSeconds: h.TimestampSeconds,
		ExtraData:        h.ExtraData,
		MixDigest:        h.MixDigest,
		Nonce:            block.Nonce(h.Nonce),
		SourceRLP:        h.SourceRLP,
	}
	if h.BaseFee != nil {
		fee := block.UInt256(*h.BaseFee)
		out.BaseFee = block.Some(fee)
	}
	if h.WithdrawalsRoot != nil {
		out.WithdrawalsRoot = block.Some(block.Hash32(*h.WithdrawalsRoot))
	}
	if h.BlobGasUsed != nil {
		out.BlobGasUsed = block.Some(*h.BlobGasUsed)
	}
	if h.ExcessBlobGas != nil {
		out.ExcessBlobGas = block.Some(*h.ExcessBlobGas)
	}
	if h.ParentBeaconBlockRoot != nil {
		out.ParentBeaconBlockRoot = block.Some(block.Hash32(*h.ParentBeaconBlockRoot))
	}
	if h.RequestsHash != nil {
		out.RequestsHash = block.Some(block.Hash32(*h.RequestsHash))
	}
	return out
}
