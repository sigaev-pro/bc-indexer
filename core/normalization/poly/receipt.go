package poly

import (
	"bytes"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
)

// Log is one committed receipt log with its inclusion indexes.
type Log struct {
	Address             Address20
	Topics              []Hash32
	Data                []byte
	TransactionLogIndex uint64
	BlockLogIndex       uint64
}

// SourceReceiptEncoding is the D75 source encoding of the receipt: kind,
// mandatory native_type presence by the closed table, and the exact native
// consensus bytes.
type SourceReceiptEncoding struct {
	Kind       profiles.ReceiptEncodingKind
	NativeType *uint32
	Bytes      []byte
}

// BorReceiptDetails is the closed Polygon network-details variant of the
// receipt (D75): the source log provenance. An ordinary body transaction
// carries the variant with a present, empty list.
type BorReceiptDetails struct {
	SourceLogScopes []SourceLogScope
}

// Receipt is the typed outcome, fee and network view of one body
// transaction. Source values (pre-status roots, blooms, gas fields by
// applicability) are kept as the native source produced them; consensus
// bytes and typed fields are two representations of one receipt, checked
// against each other and never substituted.
type Receipt struct {
	// Status is the post-Byzantium status flag; a nil Status with a
	// PostStateRoot is a pre-status source root.
	Status        *bool
	PostStateRoot *Hash32
	Result        ExecutionResult
	// CumulativeGasUsed is mandatory; GasUsed is applicable presence.
	CumulativeGasUsed uint64
	GasUsed           *uint64
	EffectiveGasPrice *UInt256
	BlobGasUsed       *uint64
	BlobGasPrice      *uint64
	Logs              []Log
	LogsBloom         Bloom
	SourceEncoding    SourceReceiptEncoding
	NetworkDetails    BorReceiptDetails
	// FeeSummary is the aggregate of the fee actions; it is never a second
	// monetary event.
	FeeSummary *balance.FeeSummary
}

// buildReceipt normalizes the captured receipt: it selects the D75 encoding
// kind from the observed native type, decodes the consensus framing
// strictly, checks the common consensus fields against the typed values,
// and validates the BorReceiptDetails provenance.
func buildReceipt(tx *TxCapture) (*Receipt, error) {
	rc := &tx.Receipt
	if rc.Result == ResultUnspecified {
		return nil, fmt.Errorf("receipt result is unspecified: %w", ErrCapture)
	}
	if rc.Status == nil && rc.PostStateRoot == nil {
		return nil, fmt.Errorf("receipt has neither a status nor a pre-status root: %w", ErrCapture)
	}
	if rc.Status != nil && rc.PostStateRoot != nil {
		return nil, fmt.Errorf("receipt carries both a status and a pre-status root: %w", ErrCapture)
	}

	out := &Receipt{
		Status:            rc.Status,
		PostStateRoot:     rc.PostStateRoot,
		Result:            rc.Result,
		CumulativeGasUsed: rc.CumulativeGasUsed,
		GasUsed:           rc.GasUsed,
		EffectiveGasPrice: rc.EffectiveGasPrice,
		BlobGasUsed:       rc.BlobGasUsed,
		BlobGasPrice:      rc.BlobGasPrice,
		LogsBloom:         rc.LogsBloom,
	}
	for _, l := range rc.Logs {
		out.Logs = append(out.Logs, Log{
			Address:             l.Address,
			Topics:              l.Topics,
			Data:                l.Data,
			TransactionLogIndex: l.TransactionLogIndex,
			BlockLogIndex:       l.BlockLogIndex,
		})
	}

	kind, nativeType, err := receiptEncodingFor(tx)
	if err != nil {
		return nil, err
	}
	out.SourceEncoding = SourceReceiptEncoding{Kind: kind, NativeType: nativeType, Bytes: cloneBytes(rc.Consensus)}
	scopes, err := validateSourceLogScopes(rc.Logs, rc.LogScopes, tx.StateSync != nil)
	if err != nil {
		return nil, err
	}
	out.NetworkDetails = BorReceiptDetails{SourceLogScopes: scopes}
	return out, nil
}

// receiptEncodingFor selects the closed D75 row of the observed native
// type: kind 1 with native_type 0 for a legacy receipt, kind 2 with the
// observed 1..127 type for a typed receipt (the StateSync body receipt uses
// native_type 127).
func receiptEncodingFor(tx *TxCapture) (profiles.ReceiptEncodingKind, *uint32, error) {
	profile, err := profiles.Default(profiles.FamilyBor)
	if err != nil {
		return 0, nil, err
	}
	nativeType := uint32(tx.NativeType)
	enc := profiles.ReceiptEncoding{NativeType: &nativeType}
	if tx.NativeType == 0 {
		enc.Kind = profiles.KindLegacyConsensusRLP
	} else {
		enc.Kind = profiles.KindTypedConsensusEnvelope
	}
	if err := profile.ValidateReceiptEncoding(enc, true); err != nil {
		return 0, nil, fmt.Errorf("%w: %v", ErrTypeNotApplicable, err)
	}
	return enc.Kind, enc.NativeType, nil
}

// verifyReceiptConsensus decodes the consensus framing of the source
// encoding strictly and checks the common consensus fields (status or
// pre-status root, cumulative gas, bloom and logs) against the typed
// receipt. A framing violation or a mismatch is an explicit normalization
// error, never a silent re-encoding.
func verifyReceiptConsensus(r *Receipt, nativeType uint8) error {
	payload := r.SourceEncoding.Bytes
	if len(payload) == 0 {
		return fmt.Errorf("receipt consensus bytes are empty: %w", ErrCapture)
	}
	if r.SourceEncoding.Kind == profiles.KindTypedConsensusEnvelope {
		if payload[0] != nativeType {
			return fmt.Errorf("receipt consensus type byte %d differs from native type %d: %w",
				payload[0], nativeType, ErrCapture)
		}
		payload = payload[1:]
	} else if payload[0] == StateSyncTxType {
		return fmt.Errorf("state-sync type byte in a kind 1 receipt: %w", ErrCapture)
	}
	fields, err := rlpList(payload)
	if err != nil {
		return fmt.Errorf("receipt consensus framing: %w", err)
	}
	if len(fields) != 4 {
		return fmt.Errorf("receipt consensus has %d fields, want 4: %w", len(fields), ErrCapture)
	}
	statusField, cumulativeField, bloomField, logsField := fields[0], fields[1], fields[2], fields[3]

	// Status or pre-status root.
	if r.Status != nil {
		want := byte(0)
		if *r.Status {
			want = 1
		}
		if statusField.IsList || len(statusField.Bytes) != 1 || statusField.Bytes[0] != want {
			return fmt.Errorf("receipt consensus status differs from the typed status: %w", ErrCapture)
		}
	} else {
		if statusField.IsList || len(statusField.Bytes) != 32 {
			return fmt.Errorf("receipt consensus pre-status root is not 32 bytes: %w", ErrCapture)
		}
		var root Hash32
		copy(root[:], statusField.Bytes)
		if r.PostStateRoot == nil || root != *r.PostStateRoot {
			return fmt.Errorf("receipt consensus root differs from the typed root: %w", ErrCapture)
		}
	}

	// Cumulative gas.
	cumulative, err := rlpDecodeUint(cumulativeField)
	if err != nil || cumulative != r.CumulativeGasUsed {
		return fmt.Errorf("receipt consensus cumulative gas differs from the typed value: %w", ErrCapture)
	}

	// Bloom.
	if bloomField.IsList || len(bloomField.Bytes) != 256 {
		return fmt.Errorf("receipt consensus bloom is not 256 bytes: %w", ErrCapture)
	}
	if Bloom(bloomField.Bytes) != r.LogsBloom {
		return fmt.Errorf("receipt consensus bloom differs from the typed bloom: %w", ErrCapture)
	}

	// Logs: committed source logs only, in order.
	if !logsField.IsList {
		return fmt.Errorf("receipt consensus logs are not a list: %w", ErrCapture)
	}
	encodedLogs, err := rlpListContent(logsField.Bytes)
	if err != nil {
		return fmt.Errorf("receipt consensus logs framing: %w", err)
	}
	if len(encodedLogs) != len(r.Logs) {
		return fmt.Errorf("receipt consensus has %d logs, typed receipt has %d: %w",
			len(encodedLogs), len(r.Logs), ErrCapture)
	}
	for i, encoded := range encodedLogs {
		parts, err := rlpListContent(encoded.Bytes)
		if err != nil || !encoded.IsList || len(parts) != 3 {
			return fmt.Errorf("receipt consensus log %d framing: %w", i, ErrCapture)
		}
		if parts[0].IsList || len(parts[0].Bytes) != 20 || Address20(parts[0].Bytes) != r.Logs[i].Address {
			return fmt.Errorf("receipt consensus log %d address differs: %w", i, ErrCapture)
		}
		topics, err := rlpListContent(parts[1].Bytes)
		if err != nil || !parts[1].IsList || len(topics) != len(r.Logs[i].Topics) {
			return fmt.Errorf("receipt consensus log %d topics differ: %w", i, ErrCapture)
		}
		for j, topic := range topics {
			if topic.IsList || len(topic.Bytes) != 32 || Hash32(topic.Bytes) != r.Logs[i].Topics[j] {
				return fmt.Errorf("receipt consensus log %d topic %d differs: %w", i, j, ErrCapture)
			}
		}
		if parts[2].IsList || !bytes.Equal(parts[2].Bytes, r.Logs[i].Data) {
			return fmt.Errorf("receipt consensus log %d data differs: %w", i, ErrCapture)
		}
	}
	return nil
}
