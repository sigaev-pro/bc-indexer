// Package receipt is the receipt area of the BSC normalization module
// (E04-01.receipts).
//
// It owns the typed outcome of one BSC body transaction, the D75 source
// receipt encoding selection (LEGACY_CONSENSUS_RLP or
// TYPED_CONSENSUS_ENVELOPE, never a reconstruction from normalized fields),
// the fee summary aggregate, and the BSC receipt network details: an
// explicitly present, structurally empty BscReceiptDetails variant, per
// D75, for ordinary and system transactions alike.
//
// Normative anchors:
//
//   - .project/docs/MESSAGE_CONTRACT.md, section 3.1 (D75-D76 source
//     receipt encoding) and section 3.2 (receipt network details
//     applicability: "BSC, including system tx | BscReceiptDetails — an
//     explicitly present empty typed variant; the common gas/log/fee fields
//     stay in Receipt")
//   - .project/docs/CURRENT_SEMANTICS.md, "Balance actions and commission
//     D62" (fee summary)
//   - .project/docs/TRANSACTION_TYPE_REGISTRY.md: body receipt type follows
//     the actual native type; BSC checks it against the actually included
//     transaction after the actualTx substitution, and no invented BSC
//     system receipt type exists
package receipt

import (
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
)

// Sentinel errors of the receipt validator.
var (
	ErrNoOutcome        = errors.New("bsc/receipt: the receipt carries exactly one native outcome")
	ErrNoEncoding       = errors.New("bsc/receipt: a body receipt carries exactly one source consensus encoding")
	ErrNoDetails        = errors.New("bsc/receipt: BscReceiptDetails is a mandatory explicitly present variant")
	ErrNativeTypeRange  = errors.New("bsc/receipt: native receipt type outside the closed BSC body registry")
	ErrEncodingMismatch = errors.New("bsc/receipt: encoding kind does not match the native receipt type")
	ErrNoFeeSummary     = errors.New("bsc/receipt: a BSC body receipt carries the fee summary aggregate")
	ErrEmptyEncoding    = errors.New("bsc/receipt: consensus encoding bytes are mandatory and never truncated")
	ErrGasUsedRange     = errors.New("bsc/receipt: gas_used exceeds cumulative_gas_used")
)

// BSCNativeTypes lists the native transaction types of the closed BSC body
// registry (TRANSACTION_TYPE_REGISTRY, standard EVM variants 0x00-0x04).
// The Parlia system classification is not a type: it preserves the actual
// wire type, so a system receipt also uses one of these values.
var BSCNativeTypes = map[uint32]bool{0: true, 1: true, 2: true, 3: true, 4: true}

// BscReceiptDetails is the D75 network-details variant of the BSC family:
// explicitly present and structurally empty. The common gas, log, fee and
// outcome fields are Receipt fields; the variant exists so that the closed
// oneof stays exhaustive and an empty variant is never confused with a
// missing Receipt.
type BscReceiptDetails struct{}

// Validate always passes: the variant is present and empty by definition.
// Its presence is what the record checks.
func (BscReceiptDetails) Validate() error { return nil }

// Log is one committed receipt log. The receipt carries only committed
// logs; reverted log attempts never enter this list.
type Log struct {
	Address balance.Address20
	Topics  []block.Hash32
	Data    []byte
	// TxLogIndex is the log index inside the transaction.
	TxLogIndex uint64
	// BlockLogIndex is the log index inside the block.
	BlockLogIndex uint64
}

// ExecutionResult is the execution result of the receipt: SUCCESS, REVERT
// or FAILURE. It is derived from the native execution, not guessed from a
// pre-status state root.
type ExecutionResult uint8

// Execution results. Zero is not a valid result.
const (
	ResultInvalid ExecutionResult = iota
	ResultSuccess
	ResultRevert
	ResultFailure
)

var resultNames = [...]string{"INVALID", "SUCCESS", "REVERT", "FAILURE"}

// String returns the contract name of r.
func (r ExecutionResult) String() string {
	if r == 0 || int(r) >= len(resultNames) {
		return fmt.Sprintf("RESULT_INVALID(%d)", uint8(r))
	}
	return resultNames[r]
}

// Encoding is the D75 source receipt encoding of one body receipt: the
// exact bytes of the pinned native consensus codec, with the closed kind
// and the native type the table prescribes.
type Encoding struct {
	// Kind is 1 (LEGACY_CONSENSUS_RLP) or 2 (TYPED_CONSENSUS_ENVELOPE).
	Kind profiles.ReceiptEncodingKind
	// NativeType is the actual native receipt type: present and 0 for kind
	// 1, present and 1..127 for kind 2. There is no invented BSC system
	// type; a Parlia system receipt keeps the type of its actual wire
	// transaction after the actualTx substitution.
	NativeType uint32
	// Bytes are the exact native consensus bytes. They are spooled from
	// the source, never rebuilt from the typed fields.
	Bytes []byte
}

// NewEncoding selects the closed encoding row for the observed native
// receipt type. It never recodes: bytes are the source consensus bytes.
func NewEncoding(nativeType uint32, consensusBytes []byte) (Encoding, error) {
	if !BSCNativeTypes[nativeType] {
		return Encoding{}, fmt.Errorf("%w: native type %d", ErrNativeTypeRange, nativeType)
	}
	if len(consensusBytes) == 0 {
		return Encoding{}, ErrEmptyEncoding
	}
	enc := Encoding{NativeType: nativeType, Bytes: consensusBytes}
	if nativeType == 0 {
		enc.Kind = profiles.KindLegacyConsensusRLP
	} else {
		enc.Kind = profiles.KindTypedConsensusEnvelope
	}
	profile, err := profiles.Default(profiles.FamilyBsc)
	if err != nil {
		return Encoding{}, err
	}
	if err := profile.ValidateReceiptEncoding(profiles.ReceiptEncoding{Kind: enc.Kind, NativeType: &enc.NativeType}, true); err != nil {
		return Encoding{}, fmt.Errorf("%w: %v", ErrEncodingMismatch, err)
	}
	return enc, nil
}

// Receipt is the typed receipt of one BSC body transaction.
type Receipt struct {
	// Status is the native status outcome, when the receipt carries one.
	Status *bool
	// PreStatusStateRoot is the pre-status state-root outcome. BSC blocks
	// after the Byzantium-family rules always carry a status; the field
	// exists so a pre-status source stays representable without inventing
	// an RPC-style status.
	PreStatusStateRoot *block.Hash32
	// Result is the execution result SUCCESS | REVERT | FAILURE.
	Result ExecutionResult
	// CumulativeGasUsed is the block-cumulative gas after this receipt.
	CumulativeGasUsed uint64
	// GasUsed is the gas of this one transaction, when applicable.
	GasUsed *uint64
	// EffectiveGasPrice is the effective gas price of the execution. For a
	// Parlia system transaction it is an explicit zero, not an absent
	// field.
	EffectiveGasPrice balance.UInt256
	// BlobGasUsed and BlobGasPrice carry their own presence; they apply
	// only to blob transactions.
	BlobGasUsed  *uint64
	BlobGasPrice *balance.UInt256
	// Logs are the committed logs of the transaction.
	Logs []Log
	// LogsBloom is the receipt bloom of the committed logs.
	LogsBloom [256]byte
	// Encoding is the source consensus encoding: exactly one variant.
	Encoding Encoding
	// FeeSummary aggregates the fee actions of the transaction. A
	// zero-fee system transaction stores applicable explicit zeros with an
	// absent payer; the summary is an aggregate, never a second movement.
	FeeSummary balance.FeeSummary
	// Details is the mandatory, explicitly present BSC network-details
	// variant. It is structurally empty per D75.
	Details *BscReceiptDetails
}

// Validate checks the closed receipt shape: exactly one native outcome,
// exactly one source encoding selected by the actual native type, the
// mandatory BSC details variant, the fee summary presence, and the gas
// consistency.
func (r *Receipt) Validate() error {
	var errs []error
	outcomes := 0
	if r.Status != nil {
		outcomes++
	}
	if r.PreStatusStateRoot != nil {
		outcomes++
	}
	if outcomes != 1 {
		errs = append(errs, ErrNoOutcome)
	}
	if r.Details == nil {
		errs = append(errs, ErrNoDetails)
	}
	if r.Result == ResultInvalid {
		errs = append(errs, fmt.Errorf("result invalid: %w", ErrNoOutcome))
	}
	if r.Encoding.Bytes == nil {
		errs = append(errs, ErrNoEncoding)
	} else {
		if !BSCNativeTypes[r.Encoding.NativeType] {
			errs = append(errs, fmt.Errorf("type %d: %w", r.Encoding.NativeType, ErrNativeTypeRange))
		}
		want := profiles.KindLegacyConsensusRLP
		if r.Encoding.NativeType != 0 {
			want = profiles.KindTypedConsensusEnvelope
		}
		if r.Encoding.Kind != want {
			errs = append(errs, fmt.Errorf("kind %d for type %d: %w", uint8(r.Encoding.Kind), r.Encoding.NativeType, ErrEncodingMismatch))
		}
		if len(r.Encoding.Bytes) == 0 {
			errs = append(errs, ErrEmptyEncoding)
		}
	}
	if r.GasUsed != nil && *r.GasUsed > r.CumulativeGasUsed {
		errs = append(errs, ErrGasUsedRange)
	}
	return errors.Join(errs...)
}

// ZeroFeeSummary returns the fee summary of a zero-fee Parlia system
// transaction: applicable explicit zeros and an absent payer. The gas
// price is zero, so no purchase or return movement exists to aggregate.
func ZeroFeeSummary() balance.FeeSummary { return balance.FeeSummary{} }
