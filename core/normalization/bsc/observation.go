package bsc

import (
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/authorization"
	bscbalance "github.com/sigaev-pro/bc-indexer/core/normalization/bsc/balance"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/calls"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/receipt"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/state"
	"github.com/sigaev-pro/bc-indexer/protocol/balance"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// Provenance records where a projected execution value came from: the
// encoded source fields, or a native derivation on top of them. It never
// overwrites the original native fields.
type Provenance uint8

// Provenance values.
const (
	// ProvenanceInvalid is the unset value.
	ProvenanceInvalid Provenance = iota
	// ProvenanceEncoded means the value is the encoded source field.
	ProvenanceEncoded
	// ProvenanceNativeDerived means the value was derived by the native
	// execution path (for example the effective gas price of a dynamic-fee
	// transaction at this block).
	ProvenanceNativeDerived
	// ProvenanceNotApplicable means the native protocol has no such field
	// at all; it is never filled with an invented zero.
	ProvenanceNotApplicable
)

// EnvelopeFacts are the observed envelope facts of one included body
// transaction: the effective typed message with its provenance, plus the
// raw native wire type and encoding, both preserved unchanged.
type EnvelopeFacts struct {
	// Position is the native body position of the transaction.
	Position uint64
	// NativeType is the actual wire type (0..4 for the closed BSC body
	// registry).
	NativeType uint8
	// Wire is the raw source encoding: EVM_LEGACY_RLP or
	// EIP2718_ENVELOPE bytes, never re-encoded.
	Wire []byte
	// TransactionHash is the native transaction hash.
	TransactionHash block.Hash32

	// Sender is the recovered native sender; SenderRecoveryError marks a
	// failed recovery, which propagates instead of a classification.
	Sender              balance.Address20
	SenderProvenance    Provenance
	SenderRecoveryError bool

	// Target is the message target for calls; nil for creations.
	Target           *balance.Address20
	TargetProvenance Provenance
	// Value, Input, Nonce and GasLimit are the effective message fields.
	Value    balance.UInt256
	Input    []byte
	Nonce    uint64
	GasLimit uint64
	// EffectiveGasPrice is the effective gas price at this block. A Parlia
	// system transaction carries an explicit zero here.
	EffectiveGasPrice balance.UInt256

	// Authorizations holds the observed authorization tuples of the wire
	// envelope, verbatim. The current BSC profile accepts none.
	Authorizations []authorization.Tuple
}

// SnapshotKind is the lifecycle step of one observed native snapshot.
type SnapshotKind uint8

// Snapshot kinds.
const (
	// SnapshotCreate is the creation of a native snapshot scope.
	SnapshotCreate SnapshotKind = iota + 1
	// SnapshotRevert is the rollback of a still-open native snapshot.
	SnapshotRevert
	// SnapshotEnd is a snapshot that ended without a revert: the scope
	// stays published for later ancestor-rollback checks, and later
	// operations belong to its parent again.
	SnapshotEnd
)

// FrameExitFacts are the bookkeeping facts of one frame exit. A frame exit
// allocates no execution_order; it only records the native scope that was
// active when the frame finished, which is what the final
// effects_committed of the frame is resolved against.
type FrameExitFacts struct {
	// FrameIndex is the index of the exiting frame in entry order.
	FrameIndex uint64
	// ExitNativeSnapshot is the native snapshot id active at the exit, or
	// 0 at the transaction boundary.
	ExitNativeSnapshot uint64
}

// SelfDestructFacts are the observed facts of one SELFDESTRUCT opcode
// execution: the execution-action facts and, attached, the monetary
// projection group of the same native operation.
type SelfDestructFacts struct {
	// FrameIndex is the index of the executing frame in entry order.
	FrameIndex uint64
	// Contract, Beneficiary and BalanceAtEntry are the opcode facts.
	Contract       balance.Address20
	Beneficiary    balance.Address20
	BalanceAtEntry balance.UInt256
	// DeletionEligible records the fork-rule eligibility observed by the
	// native execution (EIP-6780 semantics under the pinned BSC forks).
	DeletionEligible bool
	// Projection carries the observed monetary mutations of this same
	// operation. It shares the SELFDESTRUCT execution order through the
	// exact cause; nil when the balance at entry was zero.
	Projection *bscbalance.Group
}

// FinalizationBurnFacts are the observed facts of one
// SELFDESTRUCT_FINALIZATION_BURN: a separate operation at its own
// position, with its own order, that references the execution actions it
// finalizes.
type FinalizationBurnFacts struct {
	// RelatedExecutionActions are the execution-action ordinals (1-based,
	// in observation order) this burn finalizes.
	RelatedExecutionActions []uint64
	// Group is the observed burn debit group.
	Group bscbalance.Group
}

// Event is one observed native operation boundary of a transaction, in
// native order. Exactly one field is set. The normalizer allocates the
// single tx-local execution_order over the order-bearing kinds: frame
// entries, mutation groups, SELFDESTRUCT opcodes (with their attached
// projection sharing the order), finalization burns and protocol fee
// operations. Snapshot lifecycle events and frame exits are bookkeeping
// and allocate no order.
type Event struct {
	// Snapshot is a native snapshot lifecycle step.
	Snapshot *SnapshotEvent
	// FrameEntry is the entry of one execution frame. The frame carries
	// raw facts; the normalizer assigns the frame id and the entry order.
	FrameEntry *calls.Frame
	// FrameExit is the exit of one execution frame (bookkeeping).
	FrameExit *FrameExitFacts
	// Group is one observed native monetary operation.
	Group *bscbalance.Group
	// SelfDestruct is one SELFDESTRUCT opcode execution with its monetary
	// projection.
	SelfDestruct *SelfDestructFacts
	// FinalizationBurn is one post-execution finalization burn.
	FinalizationBurn *FinalizationBurnFacts
}

// SnapshotEvent is one observed native snapshot lifecycle step. NativeID
// is the native snapshot identifier; a repeated id after a rollback or an
// end creates a new scope.
type SnapshotEvent struct {
	NativeID uint64
	Kind     SnapshotKind
	// OwnerFrameIndex is the proven owning frame (index in entry order)
	// of the created scope, when the capture proves the link.
	OwnerFrameIndex *uint64
}

// ReceiptFacts are the observed receipt facts of one body transaction.
type ReceiptFacts struct {
	Status             *bool
	PreStatusStateRoot *block.Hash32
	Result             receipt.ExecutionResult
	CumulativeGasUsed  uint64
	GasUsed            *uint64
	EffectiveGasPrice  balance.UInt256
	BlobGasUsed        *uint64
	BlobGasPrice       *balance.UInt256
	Logs               []receipt.Log
	LogsBloom          [256]byte
	// NativeReceiptType is the type of the actual native receipt, checked
	// against the wire type of the included transaction after the BSC
	// actualTx substitution. ConsensusBytes are the exact native consensus
	// bytes (D75-D76): spooled from the source, never rebuilt.
	NativeReceiptType uint32
	ConsensusBytes    []byte
}

// Observation is the captured observation of one included BSC body
// transaction: the envelope facts, the ordered native event stream, the
// receipt facts and the raw account-boundary rows. This is the seam the
// E01 capture spool maps onto; every value is an owned copy, so the
// normalizer shares no memory with the native side.
type Observation struct {
	// Envelope holds the envelope facts of the transaction.
	Envelope EnvelopeFacts
	// Coinbase is the beneficiary of the executing block; the Parlia
	// classification compares the recovered sender against it.
	Coinbase balance.Address20
	// Events is the ordered native event stream of the execution.
	Events []Event
	// Receipt holds the receipt facts.
	Receipt ReceiptFacts
	// Boundary holds the raw transaction-boundary account rows, one per
	// touched account, storage rows in observation order.
	Boundary []state.AccountChange
	// Distribution is the observed fee-distribution model of the validator
	// set contract storage for the deposit expectation. Zero uses the
	// deployed fixture default.
	Distribution Distribution
	// SystemAccounts names the accounts of the independent system
	// transition expectations. Required for ParliaSystem transactions.
	SystemAccounts SystemAccounts
}
