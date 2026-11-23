package poly

import (
	"bytes"
	"fmt"
)

// NativeProtocolOperation is the closed operation registry of the
// NativeProtocolExecution projection. The first profile has exactly one
// entry; the balance cause registry names the same path
// BOR_STATE_SYNC_EXECUTION (protocol/balance).
type NativeProtocolOperation uint8

const (
	// NativeOpUnspecified is the invalid zero value.
	NativeOpUnspecified NativeProtocolOperation = iota
	// NativeOpBorStateSync is the Bor state-sync execution
	// (MESSAGE_CONTRACT: operation=BOR_STATE_SYNC).
	NativeOpBorStateSync
)

// String returns the contract name of the operation.
func (o NativeProtocolOperation) String() string {
	switch o {
	case NativeOpBorStateSync:
		return "BOR_STATE_SYNC"
	default:
		return "NATIVE_OPERATION_INVALID"
	}
}

// NativeProtocolExecution is the typed execution projection of a native
// protocol transaction: fields the native protocol does not define (sender,
// nonce, signature) are absent, never zero-filled fabrications.
type NativeProtocolExecution struct {
	Operation   NativeProtocolOperation
	RecordCount uint64
}

// StateSyncItem is one decoded body item of the StateSync transaction: the
// first four clerk.EventRecord fields of the pinned native encoding.
type StateSyncItem struct {
	ID       uint64
	Contract Address20
	Data     []byte
	// L1TxHash is the L1 transaction hash of the Heimdall event; it is
	// source data and never substitutes for the Polygon inclusion hash.
	L1TxHash Hash32
}

// decodeStateSyncWire decodes the exact native envelope: one 0x7f byte and
// the RLP list of four-field items. Malformed framing, non-minimal integers
// or wrong field widths are explicit errors; the decoder never repairs or
// re-encodes source bytes.
func decodeStateSyncWire(wire []byte) ([]StateSyncItem, error) {
	if len(wire) == 0 || wire[0] != StateSyncTxType {
		return nil, fmt.Errorf("%w: missing 0x7f envelope byte", ErrStateSyncMismatch)
	}
	rows, err := rlpList(wire[1:])
	if err != nil {
		return nil, fmt.Errorf("%w: body framing: %v", ErrStateSyncMismatch, err)
	}
	items := make([]StateSyncItem, 0, len(rows))
	for i, row := range rows {
		if !row.IsList {
			return nil, fmt.Errorf("%w: item %d is not a list", ErrStateSyncMismatch, i)
		}
		fields, err := rlpListContent(row.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: item %d framing: %v", ErrStateSyncMismatch, i, err)
		}
		if len(fields) != 4 {
			return nil, fmt.Errorf("%w: item %d has %d fields, want 4", ErrStateSyncMismatch, i, len(fields))
		}
		id, err := rlpDecodeUint(fields[0])
		if err != nil {
			return nil, fmt.Errorf("%w: item %d id: %v", ErrStateSyncMismatch, i, err)
		}
		if fields[1].IsList || len(fields[1].Bytes) != 20 {
			return nil, fmt.Errorf("%w: item %d contract is not 20 bytes", ErrStateSyncMismatch, i)
		}
		if fields[2].IsList {
			return nil, fmt.Errorf("%w: item %d data is not a byte string", ErrStateSyncMismatch, i)
		}
		if fields[3].IsList || len(fields[3].Bytes) != 32 {
			return nil, fmt.Errorf("%w: item %d L1 hash is not 32 bytes", ErrStateSyncMismatch, i)
		}
		item := StateSyncItem{ID: id, Data: append([]byte(nil), fields[2].Bytes...)}
		copy(item.Contract[:], fields[1].Bytes)
		copy(item.L1TxHash[:], fields[3].Bytes)
		items = append(items, item)
	}
	return items, nil
}

// commitCallInput rebuilds the receiver commit call payload of the pinned
// native path: commitState(uint256, bytes) with selector 0x19494a17, the
// record cutoff time and the RLP of the full six-field clerk event record.
func commitCallInput(recordTime uint64, event ClerkEventCapture) []byte {
	eventRLP := rlpEncodeList(
		rlpEncodeUint(event.ID),
		rlpEncodeBytes(event.Contract[:]),
		rlpEncodeBytes(event.Data),
		rlpEncodeBytes(event.TxHash[:]),
		rlpEncodeUint(event.LogIndex),
		rlpEncodeBytes([]byte(event.ChainID)),
	)
	return abiUint256Bytes(recordTime, eventRLP)
}

// onStateReceiveInput rebuilds the target call payload:
// onStateReceive(uint256, bytes) with selector 0x26c53bea, the record id
// and the record data.
func onStateReceiveInput(id uint64, data []byte) []byte {
	return append(
		append([]byte{0x26, 0xc5, 0x3b, 0xea}, makeU256(id)...),
		abiBytesTail(data)...,
	)
}

func abiUint256Bytes(number uint64, data []byte) []byte {
	out := append([]byte{0x19, 0x49, 0x4a, 0x17}, makeU256(number)...)
	return append(out, abiBytesTail(data)...)
}

func abiBytesTail(data []byte) []byte {
	tail := make([]byte, 0, 64+len(data)+(-len(data)%32))
	offset := makeU256(64)
	tail = append(tail, offset...)
	tail = append(tail, makeU256(uint64(len(data)))...)
	tail = append(tail, data...)
	pad := -len(data) % 32
	if pad < 0 {
		pad += 32
	}
	return append(tail, make([]byte, pad)...)
}

func makeU256(v uint64) []byte {
	out := make([]byte, 32)
	for i := 0; i < 8; i++ {
		out[31-i] = byte(v >> (8 * uint(i)))
	}
	return out
}

// normalizeStateSync validates the body StateSync transaction against the
// pinned native semantics: the wire matches the committed records, the ids
// continue the observed last id, the fork gate is active, the position is
// final, the synthetic root frame and the receiver call structure hold, and
// the receiver call payloads re-derive from the source records.
func normalizeStateSync(tx *TxCapture, calls []CallFrame) (*NativeProtocolExecution, []StateSyncItem, error) {
	if tx.StateSync == nil {
		return nil, nil, fmt.Errorf("state-sync capture is missing: %w", ErrCapture)
	}
	if !tx.Forks.StateSyncAt(tx.BlockNumber) {
		return nil, nil, fmt.Errorf("%w: 0x7f body transaction before Madhugiri block %d",
			ErrTypeNotApplicable, tx.Forks.MadhugiriBlock)
	}
	if tx.Sender != nil {
		return nil, nil, fmt.Errorf("state-sync transaction carries a fabricated sender: %w", ErrCapture)
	}
	if tx.Message != nil {
		return nil, nil, fmt.Errorf("state-sync transaction carries an EVM message projection: %w", ErrCapture)
	}
	if tx.BodyTransactionCount == 0 || tx.Position != tx.BodyTransactionCount-1 {
		return nil, nil, fmt.Errorf("%w: state-sync transaction at body position %d of %d is not final",
			ErrStateSyncMismatch, tx.Position, tx.BodyTransactionCount)
	}
	if len(tx.AuthorizationTuples) != 0 {
		return nil, nil, fmt.Errorf("%w: %d tuples on a state-sync transaction",
			ErrAuthorization, len(tx.AuthorizationTuples))
	}

	items, err := decodeStateSyncWire(tx.Wire)
	if err != nil {
		return nil, nil, err
	}
	if len(items) != len(tx.StateSync.Records) {
		return nil, nil, fmt.Errorf("%w: body carries %d records, capture observed %d",
			ErrStateSyncMismatch, len(items), len(tx.StateSync.Records))
	}
	for i := range items {
		record := tx.StateSync.Records[i]
		if items[i].ID != record.ID || items[i].Contract != record.Contract ||
			!bytes.Equal(items[i].Data, record.Data) || items[i].L1TxHash != record.L1TxHash {
			return nil, nil, fmt.Errorf("%w: body record %d differs from the captured record", ErrStateSyncMismatch, i)
		}
		if items[i].ID != tx.StateSync.LastStateIDBefore+uint64(i)+1 {
			return nil, nil, fmt.Errorf("%w: record %d id %d does not continue last id %d",
				ErrStateSyncMismatch, i, items[i].ID, tx.StateSync.LastStateIDBefore)
		}
		if tx.StateSync.Receiver == (Address20{}) {
			return nil, nil, fmt.Errorf("state-sync capture has no receiver: %w", ErrCapture)
		}
	}

	// The synthetic root frame: the registered native protocol boundary
	// from the system caller to the state receiver.
	if len(calls) == 0 {
		return nil, nil, fmt.Errorf("state-sync transaction has no root frame: %w", ErrCapture)
	}
	root := calls[0]
	if root.Kind != FrameNativeProtocol || root.Engine != engineOf(FrameNativeProtocol) {
		return nil, nil, fmt.Errorf("state-sync root frame is not the native protocol boundary: %w", ErrCapture)
	}
	if root.CallerAddress == nil || *root.CallerAddress != SystemCallerAddress {
		return nil, nil, fmt.Errorf("state-sync root frame caller is not the system caller: %w", ErrCapture)
	}
	if root.Target == nil || *root.Target != tx.StateSync.Receiver {
		return nil, nil, fmt.Errorf("state-sync root frame does not target the state receiver: %w", ErrCapture)
	}
	if len(root.Input) != 0 || root.Value != nil || root.Outcome != OutcomeSuccess || !root.EffectsCommitted {
		return nil, nil, fmt.Errorf("state-sync root frame shape differs from the native boundary: %w", ErrCapture)
	}

	// The receiver commits: one depth-1 fan per record, in record order,
	// each with the onStateReceive call of its record nested below it.
	rest := calls[1:]
	for _, record := range tx.StateSync.Records {
		if len(rest) == 0 {
			return nil, nil, fmt.Errorf("state-sync record %d has no receiver commit frame: %w",
				record.ID, ErrCapture)
		}
		commit := rest[0]
		rest = rest[1:]
		if commit.Depth != 1 || commit.Kind != FrameCall || commit.Engine != engineOf(FrameCall) {
			return nil, nil, fmt.Errorf("state-sync record %d commit frame shape differs: %w", record.ID, ErrCapture)
		}
		if commit.CallerAddress == nil || *commit.CallerAddress != SystemCallerAddress ||
			commit.Target == nil || *commit.Target != tx.StateSync.Receiver {
			return nil, nil, fmt.Errorf("state-sync record %d commit frame identity differs: %w", record.ID, ErrCapture)
		}
		if record.FullEvent != nil {
			if want := commitCallInput(tx.StateSync.RecordTime, *record.FullEvent); !bytes.Equal(commit.Input, want) {
				return nil, nil, fmt.Errorf("state-sync record %d commit payload differs from the clerk event: %w",
					record.ID, ErrCapture)
			}
		}
		// The nested calls of this commit run at depth >= 2 until the next
		// depth-1 frame; the first nested call is the record's
		// onStateReceive invocation. A caught revert inside is source data:
		// it never turns the outer receipt into a failure.
		if len(rest) == 0 || rest[0].Depth < 2 {
			return nil, nil, fmt.Errorf("state-sync record %d has no nested target call: %w", record.ID, ErrCapture)
		}
		inner := rest[0]
		rest = rest[1:]
		if inner.CallerAddress == nil || *inner.CallerAddress != tx.StateSync.Receiver ||
			inner.Target == nil || *inner.Target != record.Contract {
			return nil, nil, fmt.Errorf("state-sync record %d target call identity differs: %w", record.ID, ErrCapture)
		}
		if want := onStateReceiveInput(record.ID, record.Data); !bytes.Equal(inner.Input, want) {
			return nil, nil, fmt.Errorf("state-sync record %d target call payload differs: %w", record.ID, ErrCapture)
		}
		for len(rest) > 0 && rest[0].Depth >= 2 {
			rest = rest[1:]
		}
	}
	if len(rest) != 0 {
		return nil, nil, fmt.Errorf("state-sync frames remain after the last record: %w", ErrCapture)
	}

	// The native receipt: status success, no gas, no fee, price absent.
	rc := &tx.Receipt
	if rc.Status == nil || !*rc.Status {
		return nil, nil, fmt.Errorf("state-sync receipt status is not success: %w", ErrCapture)
	}
	if rc.GasUsed != nil && *rc.GasUsed != 0 {
		return nil, nil, fmt.Errorf("state-sync receipt used gas %d: %w", *rc.GasUsed, ErrCapture)
	}
	if rc.EffectiveGasPrice != nil {
		return nil, nil, fmt.Errorf("state-sync receipt carries an effective gas price: %w", ErrCapture)
	}
	if rc.BlobGasUsed != nil || rc.BlobGasPrice != nil {
		return nil, nil, fmt.Errorf("state-sync receipt carries blob gas fields: %w", ErrCapture)
	}

	projection := &NativeProtocolExecution{
		Operation:   NativeOpBorStateSync,
		RecordCount: uint64(len(items)),
	}
	return projection, items, nil
}

// engineOf returns the closed engine of a frame kind.
func engineOf(kind FrameKind) engineType {
	if kind == FrameNativeProtocol {
		return engineNativeProtocol
	}
	return engineEVM
}
