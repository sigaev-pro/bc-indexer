package eth

import (
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// Transaction record errors.
var (
	// ErrRecordShape reports a structurally invalid transaction record.
	ErrRecordShape = errors.New("eth: invalid transaction record")
)

// CaptureStatus is the capture status of a transaction record: CAPTURED for
// a complete capture, NOT_APPLICABLE where the profile has nothing to
// capture. A known capture error is a local fatal condition before READY,
// never a public successful record with empty mandatory details.
type CaptureStatus uint8

// Capture statuses.
const (
	CaptureUnknown CaptureStatus = iota
	CaptureCaptured
	CaptureNotApplicable
)

// String returns the contract name of the status.
func (s CaptureStatus) String() string {
	switch s {
	case CaptureCaptured:
		return "CAPTURED"
	case CaptureNotApplicable:
		return "NOT_APPLICABLE"
	}
	return "CAPTURE_UNKNOWN"
}

// RawEncodingKind is the raw payload encoding of a transaction.
type RawEncodingKind uint8

// Raw encoding kinds.
const (
	// RawLegacyRLP is the EVM legacy RLP encoding.
	RawLegacyRLP RawEncodingKind = iota + 1
	// RawEIP2718Envelope is the typed transaction envelope.
	RawEIP2718Envelope
)

// String returns the contract name of the kind.
func (k RawEncodingKind) String() string {
	switch k {
	case RawLegacyRLP:
		return "EVM_LEGACY_RLP"
	case RawEIP2718Envelope:
		return "EIP2718_ENVELOPE"
	}
	return "RAW_ENCODING_INVALID"
}

// RawEncoding is one retained source byte encoding of the transaction,
// never transcoded.
type RawEncoding struct {
	Kind  RawEncodingKind
	Bytes []byte
}

// Provenance names where an execution projection value came from.
type Provenance uint8

// Provenance values.
const (
	// ProvenanceEncoded means the value is present in the signed payload.
	ProvenanceEncoded Provenance = iota + 1
	// ProvenanceNativeDerived means the native execution derived it.
	ProvenanceNativeDerived
	// ProvenanceNotApplicable means the field has no native counterpart
	// for this payload variant; it is never filled with an invented zero.
	ProvenanceNotApplicable
)

// String returns the contract name.
func (p Provenance) String() string {
	switch p {
	case ProvenanceEncoded:
		return "ENCODED"
	case ProvenanceNativeDerived:
		return "NATIVE_DERIVED"
	case ProvenanceNotApplicable:
		return "NOT_APPLICABLE"
	}
	return "PROVENANCE_INVALID"
}

// Effective is a projection value with its provenance.
type Effective[T any] struct {
	Value      T
	Provenance Provenance
}

// EvmMessage is the execution projection of an EVM transaction: the
// effective sender, target, value, input, nonce, gas limit and effective
// gas price. Original caps, access lists, blob hashes, authorizations and
// the signature stay in the native payload; this projection never
// overwrites the source fields.
type EvmMessage struct {
	Sender            Effective[Address20]
	Target            Optional[Effective[Address20]]
	Value             Effective[UInt256]
	Input             Effective[[]byte]
	Nonce             Effective[uint64]
	GasLimit          uint64
	EffectiveGasPrice UInt256
}

// Inclusion binds a transaction to its native inclusion: the body position
// and the native transaction hash.
type Inclusion struct {
	NativePosition  uint64
	TransactionHash Hash32
}

// TransactionRecord is one normalized transaction of one native block
// decision (MESSAGE_CONTRACT section 3): inclusion, native payload
// identity, raw encodings, execution projection, receipt, calls, execution
// actions, balance actions, rollback scopes, the boundary state diff and
// the source authorization tuples.
type TransactionRecord struct {
	Inclusion             Inclusion
	NativeType            uint32
	RawEncodings          []RawEncoding
	Projection            EvmMessage
	Receipt               *Receipt
	Calls                 []CallFrame
	ExecActions           []ExecutionAction
	Actions               []balance.BalanceAction
	RollbackScope         []RollbackScope
	StateChanges          StateDiff
	Authorizations        []AuthorizationTuple
	AuthorizationOutcomes []AuthorizationTupleOutcome
	// CaptureStatus closes the record: CAPTURED only with every mandatory
	// detail present.
	CaptureStatus CaptureStatus
}

// Validate checks the whole record against the shared contracts: the call
// list, the single execution_order counter across calls, execution actions
// and balance actions with the one allowed equality, the rollback-scope
// tree, every cause reference, the fee-summary aggregate and the receipt
// shape.
func (r *TransactionRecord) Validate() error {
	var errs []error
	if r.CaptureStatus != CaptureCaptured && r.CaptureStatus != CaptureNotApplicable {
		errs = append(errs, fmt.Errorf("%w: capture status %s", ErrRecordShape, r.CaptureStatus))
	}
	if r.Receipt == nil {
		errs = append(errs, fmt.Errorf("%w: no receipt", ErrRecordShape))
	} else if err := r.Receipt.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := ValidateCalls(r.Calls); err != nil {
		errs = append(errs, err)
	}
	if len(r.RawEncodings) == 0 {
		errs = append(errs, fmt.Errorf("%w: no raw encodings", ErrRecordShape))
	}
	execRefs := make([]balance.ExecutionActionRef, len(r.ExecActions))
	for i := range r.ExecActions {
		execRefs[i] = balance.ExecutionActionRef{
			ExecutionActionID: r.ExecActions[i].ExecutionActionID,
			ExecutionOrder:    r.ExecActions[i].ExecutionOrder,
		}
		if r.ExecActions[i].Payload.SelfDestruct == nil {
			errs = append(errs, fmt.Errorf("%w: execution action %d has no payload",
				ErrRecordShape, r.ExecActions[i].ExecutionActionID))
		}
	}
	txBalances := balance.TxBalances{
		Scopes:           r.RollbackScope,
		Actions:          r.Actions,
		Frames:           FrameRefs(r.Calls),
		ExecutionActions: execRefs,
		FeeSummary:       r.FeeSummary(),
	}
	if err := balance.Validate(txBalances); err != nil {
		errs = append(errs, err)
	}
	if err := r.validateFrameReferences(); err != nil {
		errs = append(errs, err)
	}
	if err := ValidateAuthorizationOutcomes(r.Authorizations, r.AuthorizationOutcomes, &r.StateChanges); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// FeeSummary returns the receipt fee summary when present.
func (r *TransactionRecord) FeeSummary() *balance.FeeSummary {
	if r.Receipt == nil {
		return nil
	}
	return r.Receipt.FeeSummary
}

// validateFrameReferences checks that execution actions and scope owners
// reference existing frames, and that a selfdestruct action's frame
// exists.
func (r *TransactionRecord) validateFrameReferences() error {
	var errs []error
	frames := make(map[uint64]bool, len(r.Calls))
	for i := range r.Calls {
		frames[r.Calls[i].FrameID] = true
	}
	for i := range r.ExecActions {
		if !frames[r.ExecActions[i].FrameID] {
			errs = append(errs, fmt.Errorf("%w: execution action %d references frame %d: %w",
				ErrRecordShape, r.ExecActions[i].ExecutionActionID, r.ExecActions[i].FrameID, balance.ErrCauseRefUnknown))
		}
	}
	for i := range r.RollbackScope {
		if id := r.RollbackScope[i].OwnerFrameID; id != nil && !frames[*id] {
			errs = append(errs, fmt.Errorf("%w: scope %d references frame %d: %w",
				ErrRecordShape, r.RollbackScope[i].ScopeID, *id, balance.ErrCauseRefUnknown))
		}
	}
	return errors.Join(errs...)
}

// TxBalances returns the balance view of the record for the shared
// validator and the fixture reconciliation oracle.
func (r *TransactionRecord) TxBalances() balance.TxBalances {
	execRefs := make([]balance.ExecutionActionRef, len(r.ExecActions))
	for i := range r.ExecActions {
		execRefs[i] = balance.ExecutionActionRef{
			ExecutionActionID: r.ExecActions[i].ExecutionActionID,
			ExecutionOrder:    r.ExecActions[i].ExecutionOrder,
		}
	}
	return balance.TxBalances{
		Scopes:           r.RollbackScope,
		Actions:          r.Actions,
		Frames:           FrameRefs(r.Calls),
		ExecutionActions: execRefs,
		FeeSummary:       r.FeeSummary(),
	}
}

// Reconcile runs the fixture oracle of CURRENT_SEMANTICS D62: the sum of
// committed address effects must agree with the transaction-boundary
// balance points of the state diff.
func (r *TransactionRecord) Reconcile() error {
	before, after := r.StateChanges.BoundaryBalances()
	return balance.ReconcileBoundary(r.RollbackScope, r.Actions, before, after)
}

// TxDescriptor builds the manifest semantic descriptor of the
// TransactionApplied event of this record (family tx, entity ordinal body
// index plus one).
func (r *TransactionRecord) TxDescriptor(kind semantic.Kind, net semantic.Network, producer, run semantic.ID16,
	decisionSequence uint64, block Hash32, revision uint64, source semantic.SourceRef) (semantic.SemanticDescriptor, error) {
	return semantic.NewTransactionDescriptor(kind, net, producer, run, decisionSequence,
		semantic.Hash32(block), semantic.Hash32(r.Inclusion.TransactionHash), r.Inclusion.NativePosition, revision, source)
}
