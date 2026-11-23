package poly

import (
	"bytes"
	"fmt"
	"reflect"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// RawEncodingKind is the closed raw envelope encoding registry of
// MESSAGE_CONTRACT section 3.
type RawEncodingKind uint8

const (
	// RawEncodingUnspecified is the invalid zero value.
	RawEncodingUnspecified RawEncodingKind = iota
	// RawEncodingEvmLegacyRLP is the legacy signed transaction RLP.
	RawEncodingEvmLegacyRLP
	// RawEncodingEip2718Envelope is the typed transaction envelope.
	RawEncodingEip2718Envelope
	// RawEncodingBorStateSyncEnvelope is the Bor StateSync body envelope.
	RawEncodingBorStateSyncEnvelope
)

// String returns the contract name of the kind.
func (k RawEncodingKind) String() string {
	switch k {
	case RawEncodingEvmLegacyRLP:
		return "EVM_LEGACY_RLP"
	case RawEncodingEip2718Envelope:
		return "EIP2718_ENVELOPE"
	case RawEncodingBorStateSyncEnvelope:
		return "BOR_STATE_SYNC_ENVELOPE"
	default:
		return "RAW_ENCODING_INVALID"
	}
}

// RawEncoding is one raw source encoding of the transaction: the exact
// source bytes, never re-encoded.
type RawEncoding struct {
	Kind  RawEncodingKind
	Bytes []byte
}

// Effective wraps one projected execution field with its provenance.
type Effective[T any] struct {
	Value      T
	Provenance ProjectionProvenance
}

// EvmMessage is the typed execution projection of an ordinary transaction.
// The original caps, access lists, blob and authorization fields stay in
// the native payload; this projection carries the effective message only.
type EvmMessage struct {
	Sender            Effective[Address20]
	Target            *Effective[Address20]
	Value             Effective[UInt256]
	Input             Effective[[]byte]
	Nonce             Effective[uint64]
	GasLimit          uint64
	EffectiveGasPrice UInt256
}

// Projection is the closed execution projection union: an EVM message or a
// native protocol execution.
type Projection struct {
	Evm            *EvmMessage
	NativeProtocol *NativeProtocolExecution
}

// StateSyncDetails carries the normalized Polygon-specific view of a body
// StateSync transaction.
type StateSyncDetails struct {
	// Items are the decoded body records in native order.
	Items []StateSyncItem
	// Receiver is the state receiver the synthetic root frame targeted.
	Receiver Address20
	// RecordTime is the observed cutoff time of the event selection.
	RecordTime uint64
}

// TransactionRecord is the normalized Polygon transaction record of one
// included body transaction: the inclusion binding, the native payload
// view, the execution projection, the receipt with its Polygon network
// details, the calls, the execution actions, the causal balance actions,
// the rollback scopes, the transaction-boundary state diff and the closed
// authorization profile, all under the shared D74 execution order.
type TransactionRecord struct {
	// BlockHash binds the record to one branch; the position is the native
	// body index, equal to the execution operation_index.
	BlockHash Hash32
	Position  uint64
	// TransactionHash is the native transaction hash of the source.
	TransactionHash  Hash32
	NativeType       uint8
	Projection       Projection
	RawEncoding      RawEncoding
	Receipt          *Receipt
	Calls            []CallFrame
	ExecutionActions []ExecutionAction
	Actions          []balance.BalanceAction
	Scopes           []balance.RollbackScope
	StateChanges     []AccountChange
	Authorization    AuthorizationProfile
	// StateSync is present exactly for the body StateSync transaction.
	StateSync *StateSyncDetails
}

// NormalizeTransaction normalizes one captured body transaction into the
// typed record and runs every contract check of this family: the closed
// type registry, the raw encoding kind, the D81 create address rules, the
// shared D74 order, the rollback scope tree, the D62 action registry, the
// D75 receipt encoding and the closed authorization profile. A known error
// is returned instead of a successful record with empty mandatory details.
func NormalizeTransaction(tx *TxCapture) (*TransactionRecord, error) {
	if len(tx.Wire) == 0 {
		return nil, fmt.Errorf("empty wire: %w", ErrCapture)
	}
	if Keccak256(tx.Wire) != tx.ObservedTxHash {
		return nil, fmt.Errorf("observed hash %s does not match the wire: %w", tx.ObservedTxHash, ErrCapture)
	}
	// The closed body type registry of the current Polygon profile: 0, 1,
	// 2 and 4 are applicable ordinary types (the E01-05 fixture proved
	// their native imports); type 3 needs a native applicability proof the
	// profile does not have; 127 is the StateSync body type.
	switch tx.NativeType {
	case 0, 1, 2, 4, StateSyncTxType:
	default:
		return nil, fmt.Errorf("%w: native type %d", ErrTypeNotApplicable, tx.NativeType)
	}
	if tx.NativeType == StateSyncTxType && tx.Wire[0] != StateSyncTxType {
		return nil, fmt.Errorf("0x7f type with a mismatched envelope: %w", ErrCapture)
	}
	if tx.NativeType != StateSyncTxType && tx.Wire[0] == StateSyncTxType {
		return nil, fmt.Errorf("0x7f envelope with a mismatched type: %w", ErrCapture)
	}

	authorization, err := normalizeAuthorization(tx)
	if err != nil {
		return nil, err
	}

	orders, err := allocateOrders(tx)
	if err != nil {
		return nil, err
	}
	scopes, scopeOfRow, err := buildScopes(tx)
	if err != nil {
		return nil, err
	}
	calls, err := normalizeCalls(tx, orders)
	if err != nil {
		return nil, err
	}
	selfDestructs, err := normalizeSelfDestructs(tx, orders)
	if err != nil {
		return nil, err
	}
	actions, err := normalizeBalanceActions(tx, orders, scopeOfRow)
	if err != nil {
		return nil, err
	}
	receipt, err := buildReceipt(tx)
	if err != nil {
		return nil, err
	}
	stateChanges, err := normalizeStateDiff(tx)
	if err != nil {
		return nil, err
	}

	record := &TransactionRecord{
		BlockHash:        Hash32{}, // bound by NormalizeBlock
		Position:         tx.Position,
		TransactionHash:  tx.ObservedTxHash,
		NativeType:       tx.NativeType,
		Calls:            calls,
		ExecutionActions: selfDestructs,
		Actions:          actions,
		Scopes:           scopes,
		StateChanges:     stateChanges,
		Authorization:    authorization,
	}

	if tx.StateSync != nil || tx.NativeType == StateSyncTxType {
		if tx.NativeType != StateSyncTxType || tx.StateSync == nil {
			return nil, fmt.Errorf("state-sync capture and type disagree: %w", ErrCapture)
		}
		projection, items, err := normalizeStateSync(tx, calls)
		if err != nil {
			return nil, err
		}
		record.Projection = Projection{NativeProtocol: projection}
		record.RawEncoding = RawEncoding{Kind: RawEncodingBorStateSyncEnvelope, Bytes: cloneBytes(tx.Wire)}
		record.StateSync = &StateSyncDetails{
			Items:      items,
			Receiver:   tx.StateSync.Receiver,
			RecordTime: tx.StateSync.RecordTime,
		}
	} else {
		// An ordinary transaction: a native sender and an EVM message
		// projection are mandatory; no field the protocol does not define
		// is fabricated.
		if tx.Sender == nil {
			return nil, fmt.Errorf("ordinary transaction without a native sender: %w", ErrCapture)
		}
		if tx.Message == nil {
			return nil, fmt.Errorf("ordinary transaction without an EVM message projection: %w", ErrCapture)
		}
		if tx.Message.SenderProvenance == ProvenanceUnspecified ||
			tx.Message.ValueProvenance == ProvenanceUnspecified ||
			tx.Message.InputProvenance == ProvenanceUnspecified ||
			tx.Message.NonceProvenance == ProvenanceUnspecified {
			return nil, fmt.Errorf("message projection without provenance: %w", ErrCapture)
		}
		msg := &EvmMessage{
			Sender: Effective[Address20]{
				Value:      tx.Message.Sender,
				Provenance: tx.Message.SenderProvenance,
			},
			Value: Effective[UInt256]{
				Value:      tx.Message.Value,
				Provenance: tx.Message.ValueProvenance,
			},
			Input: Effective[[]byte]{
				Value:      tx.Message.Input,
				Provenance: tx.Message.InputProvenance,
			},
			Nonce: Effective[uint64]{
				Value:      tx.Message.Nonce,
				Provenance: tx.Message.NonceProvenance,
			},
			GasLimit:          tx.Message.GasLimit,
			EffectiveGasPrice: tx.Message.EffectiveGasPrice,
		}
		if tx.Message.Target != nil {
			if tx.Message.TargetProvenance == ProvenanceUnspecified {
				return nil, fmt.Errorf("target projection without provenance: %w", ErrCapture)
			}
			msg.Target = &Effective[Address20]{
				Value:      *tx.Message.Target,
				Provenance: tx.Message.TargetProvenance,
			}
		}
		if msg.Sender.Value != *tx.Sender {
			return nil, fmt.Errorf("projection sender differs from the native sender: %w", ErrCapture)
		}
		record.Projection = Projection{Evm: msg}
		if tx.NativeType == 0 {
			record.RawEncoding = RawEncoding{Kind: RawEncodingEvmLegacyRLP, Bytes: cloneBytes(tx.Wire)}
		} else {
			record.RawEncoding = RawEncoding{Kind: RawEncodingEip2718Envelope, Bytes: cloneBytes(tx.Wire)}
		}
		// The root frame of an ordinary transaction starts from the
		// projected sender.
		if len(calls) == 0 || calls[0].CallerAddress == nil || *calls[0].CallerAddress != *tx.Sender {
			return nil, fmt.Errorf("root frame caller differs from the native sender: %w", ErrCapture)
		}
	}

	if err := verifyReceiptConsensus(receipt, tx.NativeType); err != nil {
		return nil, err
	}
	feeSummary, err := buildFeeSummary(actions)
	if err != nil {
		return nil, err
	}
	receipt.FeeSummary = feeSummary
	record.Receipt = receipt

	// The full closed D74/D62 validation of the balance view.
	tx0 := balance.TxBalances{
		Scopes:           scopes,
		Actions:          actions,
		Frames:           make([]balance.FrameRef, len(calls)),
		ExecutionActions: make([]balance.ExecutionActionRef, len(selfDestructs)),
		FeeSummary:       feeSummary,
	}
	for i := range calls {
		tx0.Frames[i] = balance.FrameRef{FrameID: calls[i].FrameID, ExecutionOrder: calls[i].ExecutionOrder}
	}
	for i := range selfDestructs {
		tx0.ExecutionActions[i] = balance.ExecutionActionRef{
			ExecutionActionID: selfDestructs[i].ExecutionActionID,
			ExecutionOrder:    selfDestructs[i].ExecutionOrder,
		}
	}
	if err := balance.Validate(tx0); err != nil {
		return nil, fmt.Errorf("balance view: %w", err)
	}
	return record, nil
}

// cloneBytes returns an owned copy of a source byte slice. The published
// record never aliases capture memory: after normalization the record's
// bytes are the record, and later capture reuse cannot rewrite them.
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}

// txDeepEqual compares two transaction records field by field, including
// every logical identity, order and scope. It backs the replay checks.
func txDeepEqual(a, b *TransactionRecord) error {
	if a.BlockHash != b.BlockHash || a.Position != b.Position || a.TransactionHash != b.TransactionHash ||
		a.NativeType != b.NativeType {
		return fmt.Errorf("transaction identity differs at position %d", a.Position)
	}
	if a.RawEncoding.Kind != b.RawEncoding.Kind || !bytes.Equal(a.RawEncoding.Bytes, b.RawEncoding.Bytes) {
		return fmt.Errorf("raw encoding differs at position %d", a.Position)
	}
	if len(a.Calls) != len(b.Calls) || len(a.ExecutionActions) != len(b.ExecutionActions) ||
		len(a.Actions) != len(b.Actions) || len(a.Scopes) != len(b.Scopes) ||
		len(a.StateChanges) != len(b.StateChanges) {
		return fmt.Errorf("list sizes differ at position %d", a.Position)
	}
	for i := range a.Calls {
		if !reflect.DeepEqual(a.Calls[i], b.Calls[i]) {
			return fmt.Errorf("call %d differs at position %d", i, a.Position)
		}
	}
	for i := range a.ExecutionActions {
		if !reflect.DeepEqual(a.ExecutionActions[i], b.ExecutionActions[i]) {
			return fmt.Errorf("execution action %d differs at position %d", i, a.Position)
		}
	}
	for i := range a.Actions {
		if !actionsEqual(&a.Actions[i], &b.Actions[i]) {
			return fmt.Errorf("action %d differs at position %d", i, a.Position)
		}
	}
	for i := range a.Scopes {
		if !scopesEqual(&a.Scopes[i], &b.Scopes[i]) {
			return fmt.Errorf("scope %d differs at position %d", i, a.Position)
		}
	}
	for i := range a.StateChanges {
		if !reflect.DeepEqual(a.StateChanges[i], b.StateChanges[i]) {
			return fmt.Errorf("state change %d differs at position %d", i, a.Position)
		}
	}
	if err := receiptsEqual(a.Receipt, b.Receipt); err != nil {
		return fmt.Errorf("receipt differs at position %d: %v", a.Position, err)
	}
	if (a.StateSync == nil) != (b.StateSync == nil) {
		return fmt.Errorf("state-sync presence differs at position %d", a.Position)
	}
	if a.StateSync != nil && !reflect.DeepEqual(a.StateSync, b.StateSync) {
		return fmt.Errorf("state-sync details differ at position %d", a.Position)
	}
	return nil
}

func scopesEqual(a, b *balance.RollbackScope) bool {
	if a.ScopeID != b.ScopeID || a.RolledBack != b.RolledBack {
		return false
	}
	if (a.ParentScopeID == nil) != (b.ParentScopeID == nil) || (a.OwnerFrameID == nil) != (b.OwnerFrameID == nil) {
		return false
	}
	if a.ParentScopeID != nil && *a.ParentScopeID != *b.ParentScopeID {
		return false
	}
	if a.OwnerFrameID != nil && *a.OwnerFrameID != *b.OwnerFrameID {
		return false
	}
	return true
}

func receiptsEqual(a, b *Receipt) error {
	if a == nil || b == nil {
		if a == b {
			return nil
		}
		return fmt.Errorf("nil mismatch")
	}
	if (a.Status == nil) != (b.Status == nil) || (a.PostStateRoot == nil) != (b.PostStateRoot == nil) {
		return fmt.Errorf("outcome presence mismatch")
	}
	if a.Status != nil && *a.Status != *b.Status {
		return fmt.Errorf("status mismatch")
	}
	if a.PostStateRoot != nil && *a.PostStateRoot != *b.PostStateRoot {
		return fmt.Errorf("root mismatch")
	}
	if a.Result != b.Result || a.CumulativeGasUsed != b.CumulativeGasUsed || a.LogsBloom != b.LogsBloom {
		return fmt.Errorf("outcome mismatch")
	}
	if (a.GasUsed == nil) != (b.GasUsed == nil) || (a.EffectiveGasPrice == nil) != (b.EffectiveGasPrice == nil) ||
		(a.BlobGasUsed == nil) != (b.BlobGasUsed == nil) || (a.BlobGasPrice == nil) != (b.BlobGasPrice == nil) {
		return fmt.Errorf("gas field presence mismatch")
	}
	if a.GasUsed != nil && *a.GasUsed != *b.GasUsed {
		return fmt.Errorf("gas used mismatch")
	}
	if a.EffectiveGasPrice != nil && *a.EffectiveGasPrice != *b.EffectiveGasPrice {
		return fmt.Errorf("price mismatch")
	}
	if a.BlobGasUsed != nil && *a.BlobGasUsed != *b.BlobGasUsed {
		return fmt.Errorf("blob gas mismatch")
	}
	if a.BlobGasPrice != nil && *a.BlobGasPrice != *b.BlobGasPrice {
		return fmt.Errorf("blob price mismatch")
	}
	if len(a.Logs) != len(b.Logs) {
		return fmt.Errorf("log count mismatch")
	}
	for i := range a.Logs {
		if !reflect.DeepEqual(a.Logs[i], b.Logs[i]) {
			return fmt.Errorf("log %d mismatch", i)
		}
	}
	if !bytes.Equal(a.SourceEncoding.Bytes, b.SourceEncoding.Bytes) ||
		a.SourceEncoding.Kind != b.SourceEncoding.Kind ||
		(a.SourceEncoding.NativeType == nil) != (b.SourceEncoding.NativeType == nil) ||
		(a.SourceEncoding.NativeType != nil && *a.SourceEncoding.NativeType != *b.SourceEncoding.NativeType) {
		return fmt.Errorf("source encoding mismatch")
	}
	if !reflect.DeepEqual(a.NetworkDetails, b.NetworkDetails) {
		return fmt.Errorf("network details mismatch")
	}
	return nil
}

func actionsEqual(a, b *balance.BalanceAction) bool {
	if a.ActionIndex != b.ActionIndex || a.ExecutionOrder != b.ExecutionOrder ||
		a.Engine != b.Engine || a.Kind != b.Kind || a.Asset != b.Asset ||
		a.EffectsCommitted != b.EffectsCommitted {
		return false
	}
	if !a.Cause.Equal(b.Cause) {
		return false
	}
	if (a.From == nil) != (b.From == nil) || (a.To == nil) != (b.To == nil) ||
		(a.RollbackScope == nil) != (b.RollbackScope == nil) {
		return false
	}
	if a.From != nil && *a.From != *b.From {
		return false
	}
	if a.To != nil && *a.To != *b.To {
		return false
	}
	if a.RollbackScope != nil && *a.RollbackScope != *b.RollbackScope {
		return false
	}
	if a.Amount != b.Amount || len(a.Effects) != len(b.Effects) {
		return false
	}
	for i := range a.Effects {
		if a.Effects[i] != b.Effects[i] {
			return false
		}
	}
	return true
}
