package poly

import (
	"bytes"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
)

// LegacyOrigin is the closed origin of the optional auxiliary record.
const LegacyOrigin = "BOR_LEGACY_SYNTHETIC_RECEIPT"

// LegacyCoverage is the closed coverage claim of the auxiliary record: it
// publishes the source receipt only and never claims completeness of the
// outside-tx actions or state diff.
const LegacyCoverage = "SOURCE_RECEIPT_ONLY"

// LegacySourceReceipt is the typed view of the stored native receipt: the
// storage values with the derived metadata kept separately. The stored
// status is the writer's success flag; it is not a statement that every
// internal system call succeeded.
type LegacySourceReceipt struct {
	Status            uint8
	CumulativeGasUsed uint64
	Logs              []Log
	Bloom             *Bloom
}

// BorLegacyReceiptRecord is the optional auxiliary record of the
// pre-Madhugiri separate Bor receipt (POLYGON_LEGACY_CAPTURE, E00-15): a
// distinct record type, never a body TransactionRecord, never a fake
// EvmLegacy envelope, and never counted in the body transaction inventory.
type BorLegacyReceiptRecord struct {
	BlockNumber uint64
	BlockHash   Hash32
	Origin      string
	// SourceReceiptHash is the native derived identity, verified against
	// the pinned formula instead of being trusted.
	SourceReceiptHash Hash32
	// SyntheticReceiptIndex equals the ordinary receipt count of the block;
	// it is not a body index.
	SyntheticReceiptIndex uint64
	SourceReceipt         LegacySourceReceipt
	// RawEncoding is the D75 kind 3 storage encoding: the raw storage value
	// of the native reader, never re-encoded consensus bytes.
	RawEncoding SourceReceiptEncoding
	Coverage    string
	// NativeLogPhases is the auxiliary per-log native attribution. Its
	// semantics deliberately differ from the D75 body source_log_scopes:
	// these logs are outside-tx system logs, they do not belong to any body
	// transaction, and the attribution never feeds the body provenance.
	NativeLogPhases []SourceLogScope
}

// NormalizeLegacyReceipt normalizes the observed pre-Madhugiri separate Bor
// receipt into the optional auxiliary record. The optional scope must be
// enabled with declared fixtures; the source states stay distinct: absent
// (no record), present (a record) and corrupt (an explicit error, never an
// empty successful record).
func NormalizeLegacyReceipt(cap *LegacyReceiptCapture, opts Options) (*BorLegacyReceiptRecord, error) {
	if !opts.LegacyReceipt {
		return nil, fmt.Errorf("%w: origin %s", ErrLegacyDisabled, LegacyOrigin)
	}
	if len(opts.DeclaredFixtures) == 0 {
		return nil, ErrLegacyNotFixtured
	}
	if cap == nil {
		return nil, nil
	}
	if !cap.Present {
		return nil, nil
	}
	if cap.DecodeErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrLegacySource, cap.DecodeErr)
	}

	// The native writer stores a record only when additional logs exist; a
	// present record without logs is a source inconsistency.
	if len(cap.Logs) == 0 {
		return nil, fmt.Errorf("%w: present record without logs", ErrLegacySource)
	}

	// The raw storage encoding: kind 3, no native_type, exact storage bytes.
	profile, err := profiles.Default(profiles.FamilyBor)
	if err != nil {
		return nil, err
	}
	enc := profiles.OptionalBorLegacy()
	if err := profile.ValidateReceiptEncoding(enc, false); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLegacySource, err)
	}

	// The storage RLP decodes as the storage consensus subset: status,
	// cumulative gas and logs; the stored bloom is derived metadata and is
	// not part of the storage value.
	rows, err := rlpList(cap.StorageRLP)
	if err != nil {
		return nil, fmt.Errorf("%w: storage framing: %v", ErrLegacySource, err)
	}
	if len(rows) != 3 {
		return nil, fmt.Errorf("%w: storage receipt has %d fields, want 3", ErrLegacySource, len(rows))
	}
	if rows[0].IsList || len(rows[0].Bytes) != 1 || rows[0].Bytes[0] != cap.Status {
		return nil, fmt.Errorf("%w: storage status differs from the observed status", ErrLegacySource)
	}
	if cap.Status != 1 {
		return nil, fmt.Errorf("%w: stored status %d is not the writer success flag",
			ErrLegacySource, cap.Status)
	}
	cumulative, err := rlpDecodeUint(rows[1])
	if err != nil || cumulative != cap.CumulativeGasUsed {
		return nil, fmt.Errorf("%w: storage cumulative gas differs from the observed value", ErrLegacySource)
	}
	encodedLogs, err := rlpListContent(rows[2].Bytes)
	if err != nil || !rows[2].IsList || len(encodedLogs) != len(cap.Logs) {
		return nil, fmt.Errorf("%w: storage log count differs from the observed logs", ErrLegacySource)
	}
	for i, encoded := range encodedLogs {
		parts, err := rlpListContent(encoded.Bytes)
		if err != nil || !encoded.IsList || len(parts) != 3 {
			return nil, fmt.Errorf("%w: storage log %d framing", ErrLegacySource, i)
		}
		if parts[0].IsList || len(parts[0].Bytes) != 20 || Address20(parts[0].Bytes) != cap.Logs[i].Address {
			return nil, fmt.Errorf("%w: storage log %d address differs", ErrLegacySource, i)
		}
		if !bytes.Equal(parts[2].Bytes, cap.Logs[i].Data) {
			return nil, fmt.Errorf("%w: storage log %d data differs", ErrLegacySource, i)
		}
	}

	// The derived identity follows the pinned native formula; an observed
	// hash that does not match is rejected, not adopted.
	wantHash := DeriveBorReceiptHash(cap.BlockNumber, cap.BlockHash)
	if cap.ObservedHash != wantHash {
		return nil, fmt.Errorf("%w: observed hash %s does not match the derived identity %s",
			ErrLegacySource, cap.ObservedHash, wantHash)
	}

	// The auxiliary per-log phases keep their own semantics; when present
	// they describe every log once, in order.
	phases := make([]SourceLogScope, 0, len(cap.LogPhases))
	if len(cap.LogPhases) > 0 {
		if len(cap.LogPhases) != len(cap.Logs) {
			return nil, fmt.Errorf("%w: %d phases for %d logs", ErrLegacySource, len(cap.LogPhases), len(cap.Logs))
		}
		for i, phase := range cap.LogPhases {
			if phase.TransactionLogIndex != uint64(i) || phase.Phase == LogScopeInvalid {
				return nil, fmt.Errorf("%w: phase %d is malformed", ErrLegacySource, i)
			}
			phases = append(phases, SourceLogScope{TransactionLogIndex: phase.TransactionLogIndex, Scope: phase.Phase})
		}
	}

	record := &BorLegacyReceiptRecord{
		BlockNumber:           cap.BlockNumber,
		BlockHash:             cap.BlockHash,
		Origin:                LegacyOrigin,
		SourceReceiptHash:     wantHash,
		SyntheticReceiptIndex: cap.OrdinaryReceiptCount,
		RawEncoding:           SourceReceiptEncoding{Kind: profiles.KindBorLegacyStorageRLP, Bytes: cloneBytes(cap.StorageRLP)},
		Coverage:              LegacyCoverage,
		NativeLogPhases:       phases,
	}
	record.SourceReceipt.Status = cap.Status
	record.SourceReceipt.CumulativeGasUsed = cap.CumulativeGasUsed
	for _, l := range cap.Logs {
		record.SourceReceipt.Logs = append(record.SourceReceipt.Logs, Log{
			Address:             l.Address,
			Topics:              l.Topics,
			Data:                l.Data,
			TransactionLogIndex: l.TransactionLogIndex,
			BlockLogIndex:       l.BlockLogIndex,
		})
	}
	record.SourceReceipt.Bloom = cap.Bloom
	return record, nil
}
