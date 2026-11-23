package poly

import (
	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// This file defines the capture side of the seam: the typed native
// observations the Bor adapter produces from its hooks and readers. The
// types mirror what the E01 evidence actually observed (live transaction
// rows, OnEnter/OnExit frames, snapshot bookkeeping, balance mutations,
// receipts, per-transaction state boundaries), not a re-implementation of
// native execution. Every field is source data; the normalizer derives all
// normalized identities, orders and scopes and never overwrites a source
// value with a derived one.

// ForkState is the observed fork applicability of the profile at one block.
// The values come from the pinned chain configuration, not from the
// transaction envelope.
type ForkState struct {
	// MadhugiriBlock is the configured Madhugiri height. The body StateSync
	// transaction type 0x7f is applicable at blocks >= MadhugiriBlock.
	MadhugiriBlock uint64
	// MadhugiriActive marks that Madhugiri is scheduled at all (the profile
	// of a pre-Madhugiri-only network leaves it false and zero).
	MadhugiriActive bool
	// ChainID is the native chain identity the StateSync event records must
	// carry ("15001" in the fixtures, "137" on Mainnet).
	ChainID string
}

// StateSyncAt reports whether the body StateSync transaction type is
// applicable at the given block.
func (f ForkState) StateSyncAt(block uint64) bool {
	return f.MadhugiriActive && block >= f.MadhugiriBlock
}

// FrameKind is the closed CallFrame kind registry of MESSAGE_CONTRACT
// section 3.
type FrameKind uint8

const (
	// FrameUnspecified is the invalid zero value.
	FrameUnspecified FrameKind = iota
	// FrameCall is the EVM CALL opcode path.
	FrameCall
	// FrameCallCode is the EVM CALLCODE opcode path.
	FrameCallCode
	// FrameDelegateCall is the EVM DELEGATECALL opcode path.
	FrameDelegateCall
	// FrameStaticCall is the EVM STATICCALL opcode path.
	FrameStaticCall
	// FrameCreate is the EVM CREATE opcode path.
	FrameCreate
	// FrameCreate2 is the EVM CREATE2 opcode path.
	FrameCreate2
	// FrameNativeProtocol is a registered native protocol execution
	// boundary; in this family the synthetic root frame of a body StateSync
	// transaction.
	FrameNativeProtocol
)

// String returns the contract name of the kind.
func (k FrameKind) String() string {
	switch k {
	case FrameCall:
		return "CALL"
	case FrameCallCode:
		return "CALLCODE"
	case FrameDelegateCall:
		return "DELEGATECALL"
	case FrameStaticCall:
		return "STATICCALL"
	case FrameCreate:
		return "CREATE"
	case FrameCreate2:
		return "CREATE2"
	case FrameNativeProtocol:
		return "NATIVE_PROTOCOL"
	default:
		return "FRAME_INVALID"
	}
}

// Outcome is the closed frame execution outcome.
type Outcome uint8

const (
	// OutcomeUnspecified is the invalid zero value.
	OutcomeUnspecified Outcome = iota
	// OutcomeSuccess is a locally successful execution.
	OutcomeSuccess
	// OutcomeRevert is an execution undone by REVERT.
	OutcomeRevert
	// OutcomeFailure is an execution undone by an error.
	OutcomeFailure
)

// String returns the contract name of the outcome.
func (o Outcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "SUCCESS"
	case OutcomeRevert:
		return "REVERT"
	case OutcomeFailure:
		return "FAILURE"
	default:
		return "OUTCOME_INVALID"
	}
}

// FrameCapture is one observed execution frame (native OnEnter/OnExit pair).
type FrameCapture struct {
	// Entry is the capture-observed position of the frame entry in the
	// synchronous native chronology of the transaction. The normalizer maps
	// it onto the shared D74 execution_order; it is not a published field.
	Entry uint64
	// Parent is the index of the parent frame in TxCapture.Frames, or -1
	// for the root frame of the transaction.
	Parent int
	Depth  uint64
	Engine balance.Engine
	Kind   FrameKind
	// CallerAddress, ContextAddress, CodeAddress, MsgSender and Target are
	// the distinct native address roles; none of them may substitute for
	// another under DELEGATECALL or delegated code.
	CallerAddress  *Address20
	ContextAddress *Address20
	CodeAddress    *Address20
	MsgSender      *Address20
	Target         *Address20
	Value          *UInt256
	Input          []byte
	Output         []byte
	Gas            *uint64
	GasUsed        *uint64
	Outcome        Outcome
	// Committed is the final applicability of this frame's effects after
	// ancestor rollback: a successful child inside a reverted scope is not
	// committed.
	Committed bool
	// ErrorCode is the optional native diagnostic in the profile namespace.
	ErrorCode *string
	// RevertData is the REVERT return payload, kept separately from output.
	RevertData []byte
	// AttemptedCreateAddress is the address of the CREATE attempt as the
	// native computation determined it (D81): present even when the attempt
	// later failed, and never derived from a zero check of a returned
	// address.
	AttemptedCreateAddress *Address20
	// CreatedAddress is present only on a locally successful creation.
	CreatedAddress *Address20
}

// SelfDestructCapture is one observed SELFDESTRUCT opcode.
type SelfDestructCapture struct {
	// Sequence is the capture-observed position of the opcode in the
	// synchronous native chronology.
	Sequence uint64
	// Frame is the index of the frame that executed the opcode.
	Frame           int
	ContractAddress Address20
	Beneficiary     Address20
	BalanceAtEntry  UInt256
	// CreatedSameTx reports the fork eligibility fact: the contract was
	// created in the same transaction, so EIP-6780 keeps full deletion
	// semantics available.
	CreatedSameTx bool
	// Deleted is the observed final deletion result after rollback and
	// finalization.
	Deleted bool
	// Committed is the final applicability of the opcode's monetary
	// effects.
	Committed bool
	// BurnedAtFinalization reports that finalization destroyed the balance
	// (self-beneficiary deletion): the burn is a separate operation with its
	// own order, linked through RelatedExecutionActions.
	BurnedAtFinalization bool
}

// OpCauseInput selects the cause branch of one captured monetary operation.
type OpCauseInput uint8

const (
	// OpCauseUnspecified is the invalid zero value.
	OpCauseUnspecified OpCauseInput = iota
	// OpCauseFrame points at TxCapture.Frames[Frame].
	OpCauseFrame
	// OpCauseSelfDestruct points at TxCapture.SelfDestructs[SelfDestruct].
	OpCauseSelfDestruct
	// OpCauseProtocol points at a closed registry protocol operation.
	OpCauseProtocol
)

// ProtocolOperation mirrors the closed registry of balance.ProtocolOperation
// for capture input; the normalizer maps it and assigns the tx-local
// operation index.
type ProtocolOperation = balance.ProtocolOperation

// OpCapture is one observed native monetary operation invocation. One
// transfer invocation carries both of its sides in Effects; the normalizer
// never splits or merges invocations by amount proximity.
type OpCapture struct {
	// Sequence is the capture-observed position of the operation in the
	// synchronous native chronology.
	Sequence uint64
	Cause    OpCauseInput
	// Frame indexes TxCapture.Frames for OpCauseFrame.
	Frame int
	// SelfDestruct indexes TxCapture.SelfDestructs for OpCauseSelfDestruct.
	SelfDestruct int
	// Operation is the registry operation for OpCauseProtocol.
	Operation ProtocolOperation
	From, To  *Address20
	Amount    UInt256
	Effects   []EffectCapture
	// Committed is the final applicability of the effects after rollback.
	Committed bool
	// Snapshot is the index of the observed snapshot row in
	// TxCapture.Snapshots the operation ran in; -1 means the transaction
	// boundary (outside every nested snapshot).
	Snapshot int
	// Related links a finalization burn to the SELFDESTRUCT rows it
	// finalizes (indexes into TxCapture.SelfDestructs).
	Related []int
}

// EffectCapture is one observed balance mutation of one operation.
type EffectCapture struct {
	Address    Address20
	Before     UInt256
	After      UInt256
	ReasonCode uint32
}

// SnapshotCapture is one observed native snapshot/revert scope creation.
type SnapshotCapture struct {
	// NativeID is the native snapshot identifier as the journal allocated
	// it. A native id may repeat after rollback and discard; each observed
	// creation is a distinct capture row and gets a distinct normalized
	// scope.
	NativeID uint64
	// Parent is the index of the nearest enclosing snapshot row in
	// TxCapture.Snapshots, or -1 when the scope is a direct child of the
	// transaction boundary.
	Parent int
	// OwnerFrame is the index of the frame owning the scope when the link
	// is proven, or -1.
	OwnerFrame int
	// RolledBack is the final cancellation fact, direct or through an
	// ancestor.
	RolledBack bool
}

// LogCapture is one committed receipt log.
type LogCapture struct {
	Address             Address20
	Topics              []Hash32
	Data                []byte
	TransactionLogIndex uint64
	BlockLogIndex       uint64
}

// LogScopeCapture is the native instrumentation attribution of one source
// receipt log (D75); only meaningful for a StateSync receipt.
type LogScopeCapture struct {
	TransactionLogIndex uint64
	Scope               LogScope
}

// ReceiptCapture is the observed native receipt of one body transaction.
type ReceiptCapture struct {
	// Status is the post-Byzantium status flag. A nil Status with a
	// non-nil PostStateRoot is a pre-status source root, which stays source
	// data and is never converted into an RPC status.
	Status        *bool
	PostStateRoot *Hash32
	// Result is the execution result classification.
	Result            ExecutionResult
	CumulativeGasUsed uint64
	GasUsed           *uint64
	EffectiveGasPrice *UInt256
	BlobGasUsed       *uint64
	BlobGasPrice      *uint64
	Logs              []LogCapture
	LogsBloom         Bloom
	// Consensus is the native consensus encoding of the receipt
	// (Receipt.MarshalBinary of the pinned client).
	Consensus []byte
	// LogScopes carries the per-log native attribution of a StateSync
	// receipt.
	LogScopes []LogScopeCapture
}

// ExecutionResult is the closed execution result of the receipt.
type ExecutionResult uint8

const (
	// ResultUnspecified is the invalid zero value.
	ResultUnspecified ExecutionResult = iota
	// ResultSuccess is a successful execution.
	ResultSuccess
	// ResultRevert is an execution undone by REVERT.
	ResultRevert
	// ResultFailure is a failed execution.
	ResultFailure
)

// String returns the contract name of the result.
func (r ExecutionResult) String() string {
	switch r {
	case ResultSuccess:
		return "SUCCESS"
	case ResultRevert:
		return "REVERT"
	case ResultFailure:
		return "FAILURE"
	default:
		return "RESULT_INVALID"
	}
}

// StorageBoundaryCapture is one observed storage slot boundary of one
// account in one transaction.
type StorageBoundaryCapture struct {
	Key           Hash32
	Before, After Hash32
}

// AccountBoundaryCapture is the observed transaction-boundary state of one
// touched account: the points immediately before the first operation and
// after the last operation of the transaction.
type AccountBoundaryCapture struct {
	Address        Address20
	ExistedBefore  bool
	ExistsAfter    bool
	BalanceBefore  UInt256
	BalanceAfter   UInt256
	NonceBefore    uint64
	NonceAfter     uint64
	CodeHashBefore Hash32
	CodeHashAfter  Hash32
	CodeBefore     []byte
	CodeAfter      []byte
	// StorageReset marks that the whole prior storage was deleted without
	// enumerating every key.
	StorageReset bool
	Storage      []StorageBoundaryCapture
}

// AuthorizationTupleCapture is one source authorization tuple of the
// transaction envelope. The current Polygon profile has none (EIP-7702 is
// not active), so any capture row is an explicit error, including invalid
// or duplicate tuples: they are preserved source data that this closed
// profile rejects rather than drops.
type AuthorizationTupleCapture struct {
	ChainID UInt256
	Address Address20
	Nonce   uint64
	YParity uint64
	R, S    UInt256
}

// StateSyncRecordCapture is one observed committed StateSync record: the
// first four clerk.EventRecord fields the native body encoding carries.
type StateSyncRecordCapture struct {
	ID       uint64
	Contract Address20
	Data     []byte
	// L1TxHash is the L1 transaction hash of the event; it never
	// substitutes for the Polygon inclusion transaction hash.
	L1TxHash Hash32
	// FullEvent is the complete clerk event record when the capture kept
	// it; its presence enables the receiver-call input verification.
	FullEvent *ClerkEventCapture
}

// ClerkEventCapture is the full Heimdall event record the receiver commit
// call receives: six fields, including the log index and chain id the body
// encoding omits.
type ClerkEventCapture struct {
	ID       uint64
	Contract Address20
	Data     []byte
	TxHash   Hash32
	LogIndex uint64
	ChainID  string
}

// StateSyncCapture is the observed StateSync execution of one block: the
// records the native path committed, in native order.
type StateSyncCapture struct {
	// Records are the committed records, ordered by native execution.
	Records []StateSyncRecordCapture
	// Receiver is the observed state receiver contract the synthetic root
	// frame targets.
	Receiver Address20
	// RecordTime is the cutoff time the native path selected the events by
	// (block time minus the configured state-sync delay).
	RecordTime uint64
	// LastStateIDBefore is the last committed event id before this block;
	// committed record ids continue from it.
	LastStateIDBefore uint64
}

// TxCapture is the captured native view of one included body transaction.
type TxCapture struct {
	// Position is the native body index of the transaction.
	Position uint64
	// BodyTransactionCount is the observed body size; the StateSync
	// transaction must occupy the final position.
	BodyTransactionCount uint64
	// NativeType is the observed envelope type byte.
	NativeType uint8
	// Wire is the exact source envelope bytes, never re-encoded.
	Wire []byte
	// ObservedTxHash is the native transaction hash of the source; the
	// normalizer verifies it against the wire instead of trusting either
	// side alone.
	ObservedTxHash Hash32
	// Sender is the native sender. A body StateSync transaction has none:
	// the field stays nil and no sender is fabricated.
	Sender *Address20
	// Message is the typed execution projection of an ordinary transaction;
	// nil exactly for the body StateSync transaction.
	Message *EvmMessageCapture
	// StateSync is set exactly for the body StateSync transaction.
	StateSync *StateSyncCapture

	Frames        []FrameCapture
	SelfDestructs []SelfDestructCapture
	Operations    []OpCapture
	Snapshots     []SnapshotCapture
	Receipt       ReceiptCapture
	Accounts      []AccountBoundaryCapture
	// AuthorizationTuples holds the source authorization tuples of the
	// envelope; the closed Polygon profile requires this list empty.
	AuthorizationTuples []AuthorizationTupleCapture

	// BlockNumber is the inclusion block of the transaction.
	BlockNumber uint64
	// Forks is the observed fork state at that block.
	Forks ForkState
}

// ProjectionProvenance is the D75 provenance of one projected field.
type ProjectionProvenance uint8

const (
	// ProvenanceUnspecified is the invalid zero value.
	ProvenanceUnspecified ProjectionProvenance = iota
	// ProvenanceEncoded means the value came from the signed envelope.
	ProvenanceEncoded
	// ProvenanceNativeDerived means the native path derived the value.
	ProvenanceNativeDerived
)

// EvmMessageCapture is the typed execution projection of an ordinary
// transaction: effective sender, target, value, input, nonce, gas limit and
// effective gas price with their provenance.
type EvmMessageCapture struct {
	Sender            Address20
	SenderProvenance  ProjectionProvenance
	Target            *Address20
	TargetProvenance  ProjectionProvenance
	Value             UInt256
	ValueProvenance   ProjectionProvenance
	Input             []byte
	InputProvenance   ProjectionProvenance
	Nonce             uint64
	NonceProvenance   ProjectionProvenance
	GasLimit          uint64
	EffectiveGasPrice UInt256
}

// BlockCapture is the captured native view of one block.
type BlockCapture struct {
	Number     uint64
	Hash       Hash32
	ParentHash Hash32
	// TimeSeconds is header.Time; the Bor timestamp precision is seconds.
	TimeSeconds uint64
	// Header carries the typed common header fields with the source RLP.
	Header HeaderCapture

	// Transactions are the body transactions in native order, including a
	// body StateSync transaction at the final position when present.
	Transactions []TxCapture

	// Bor context observations.
	SprintLength uint64
	SprintEnd    bool
	SideChain    bool
	// StateSyncRows is the observed count of StateSync records committed
	// with this block (events, not transactions).
	StateSyncRows uint64
	// LegacyReceipt is the pre-Madhugiri separate storage receipt
	// observation, present only when the source store has one.
	LegacyReceipt *LegacyReceiptCapture
	// HeimdallCheckpoints are the covering milestone/checkpoint references
	// observed with this block.
	HeimdallCheckpoints []HeimdallRefCapture

	// Forks is the observed fork state at this block.
	Forks ForkState
}

// HeaderCapture carries the typed common header fields the block record
// needs, with optional fork fields by presence.
type HeaderCapture struct {
	ParentHash       Hash32
	UncleHash        Hash32
	Beneficiary      Address20
	StateRoot        Hash32
	TransactionsRoot Hash32
	ReceiptsRoot     Hash32
	LogsBloom        Bloom
	Difficulty       UInt256
	Number           uint64
	GasLimit         uint64
	GasUsed          uint64
	TimestampSeconds uint64
	ExtraData        []byte
	MixDigest        Hash32
	Nonce            [8]byte

	BaseFee               *UInt256
	WithdrawalsRoot       *Hash32
	BlobGasUsed           *uint64
	ExcessBlobGas         *uint64
	ParentBeaconBlockRoot *Hash32
	RequestsHash          *Hash32

	// SourceRLP is the exact native header encoding.
	SourceRLP []byte
}

// HeimdallRefCapture is one observed covering Heimdall reference.
type HeimdallRefCapture struct {
	Kind         HeimdallKind
	EndHeight    uint64
	EndHash      Hash32
	CheckpointID *uint64
}

// HeimdallKind distinguishes the two Heimdall finality sources.
type HeimdallKind uint8

const (
	// HeimdallMilestone is a milestone; its hash is the Bor end block hash.
	HeimdallMilestone HeimdallKind = iota + 1
	// HeimdallCheckpoint is a checkpoint; its root is a range commitment.
	HeimdallCheckpoint
)

// LegacyReceiptCapture is the observed pre-Madhugiri separate Bor receipt
// from the native storage reader. Present, DecodeErr and Bytes keep the
// three source states distinct (record exists / no record / corrupt
// record); a corrupt read is never converted into absent data.
type LegacyReceiptCapture struct {
	BlockNumber uint64
	BlockHash   Hash32
	// Present reports that the native store holds a record.
	Present bool
	// DecodeErr carries a native decode failure of a present record.
	DecodeErr error
	// StorageRLP is the raw storage value of the native reader.
	StorageRLP []byte
	// ObservedHash is the derived identity the native reader returned.
	ObservedHash Hash32
	// OrdinaryReceiptCount is the number of ordinary receipts of the block;
	// the synthetic receipt index equals it.
	OrdinaryReceiptCount uint64
	// Status is the stored status flag (the writer stores success).
	Status uint8
	// CumulativeGasUsed is copied from the last ordinary receipt when one
	// exists.
	CumulativeGasUsed uint64
	// Logs are the additional state-sync logs beyond the ordinary
	// transaction logs.
	Logs []LogCapture
	// Bloom is the derived bloom when the capture kept it.
	Bloom *Bloom
	// LogPhases is the auxiliary per-log native attribution; its semantics
	// differ from the body source_log_scopes of D75.
	LogPhases []LogPhaseCapture
}

// LogPhaseCapture is one auxiliary per-log phase attribution of the legacy
// record.
type LogPhaseCapture struct {
	TransactionLogIndex uint64
	Phase               LogScope
}
