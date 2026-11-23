package eth

import (
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// Block record errors.
var (
	// ErrBlockShape reports a structurally invalid block observation.
	ErrBlockShape = errors.New("eth: invalid block observation")
)

// NativeHeader is the observed native Geth header of one block. Common
// fields follow the pinned Header of go-ethereum 1df30b18; the optional
// fork fields carry their own presence decided by the native header and
// fork, and a field absent from the native source is never filled with a
// zero value. SourceRLP is the exact native header encoding; the block
// hash comes from the native source and this package re-verifies nothing.
type NativeHeader struct {
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
	Time             uint64
	ExtraData        []byte
	MixDigest        Hash32
	Nonce            [8]byte

	// Optional fork fields; presence follows the native header.
	BaseFee               Optional[UInt256]
	WithdrawalsRoot       Optional[Hash32]
	BlobGasUsed           Optional[uint64]
	ExcessBlobGas         Optional[uint64]
	ParentBeaconBlockRoot Optional[Hash32]
	RequestsHash          Optional[Hash32]

	// Geth-only extensions that live in the ETH family details, not in the
	// common header.
	BlockAccessListHash Optional[Hash32]
	SlotNumber          Optional[uint64]

	// SourceRLP is mandatory for the current EVM profiles.
	SourceRLP []byte
}

// BlockObservation is one admitted native block observation: the block
// identity and the native body representation count. An empty block is a
// first-class observation with count 0.
type BlockObservation struct {
	Hash Hash32
	// Number is the native block number; it must equal Header.Number.
	Number uint64
	Header NativeHeader
	// TransactionCount is the observed count of the native body
	// representation. It is an observation, never a completeness claim.
	TransactionCount uint64
}

// BlockIdentity is the semantic identity input of the block record: the
// network, the producer and run that observed it and the native decision
// sequence that produced the activation.
type BlockIdentity struct {
	Network      block.NetworkIdentity
	ProducerID   semantic.ID16
	RunID        semantic.ID16
	DecisionSequ uint64
	// Source is the source provenance of the descriptor.
	Source semantic.SourceRef
}

// BuildBlockRecord projects one native ETH block observation into the
// native BlockApplied record: the typed block.BlockRecord with the header
// at source timestamp precision (ETH: whole seconds, nanos 0, precision
// SECONDS), the observed source transaction count and the closed ETH
// family details, plus the manifest semantic descriptor of the BlockApplied
// event (family block, entity ordinal 0, revision 0).
//
// Empty and nonempty blocks are exact in the same way: the count field is
// the observed value (0 for an empty block, never an absence), the header
// fields follow native presence, and the timestamp keeps the source
// precision without conversion.
func BuildBlockRecord(id BlockIdentity, obs BlockObservation, admission block.ObservationRef, snapshot block.LifecycleSnapshot) (block.BlockRecord, semantic.SemanticDescriptor, error) {
	h := block.Header{
		ParentHash:       block.Hash32(obs.Header.ParentHash),
		UncleHash:        block.Hash32(obs.Header.UncleHash),
		Beneficiary:      block.Address20(obs.Header.Beneficiary),
		StateRoot:        block.Hash32(obs.Header.StateRoot),
		TransactionsRoot: block.Hash32(obs.Header.TransactionsRoot),
		ReceiptsRoot:     block.Hash32(obs.Header.ReceiptsRoot),
		LogsBloom:        block.Bloom(obs.Header.LogsBloom),
		Difficulty:       block.UInt256(obs.Header.Difficulty),
		Number:           obs.Header.Number,
		GasLimit:         obs.Header.GasLimit,
		GasUsed:          obs.Header.GasUsed,
		TimestampSeconds: obs.Header.Time,
		ExtraData:        obs.Header.ExtraData,
		MixDigest:        block.Hash32(obs.Header.MixDigest),
		Nonce:            block.Nonce(obs.Header.Nonce),
		SourceRLP:        obs.Header.SourceRLP,
	}
	if v, ok := obs.Header.BaseFee.Get(); ok {
		h.BaseFee = block.Some(block.UInt256(v))
	}
	if v, ok := obs.Header.WithdrawalsRoot.Get(); ok {
		h.WithdrawalsRoot = block.Some(block.Hash32(v))
	}
	if v, ok := obs.Header.BlobGasUsed.Get(); ok {
		h.BlobGasUsed = block.Some(v)
	}
	if v, ok := obs.Header.ExcessBlobGas.Get(); ok {
		h.ExcessBlobGas = block.Some(v)
	}
	if v, ok := obs.Header.ParentBeaconBlockRoot.Get(); ok {
		h.ParentBeaconBlockRoot = block.Some(block.Hash32(v))
	}
	if v, ok := obs.Header.RequestsHash.Get(); ok {
		h.RequestsHash = block.Some(block.Hash32(v))
	}
	details := block.EthBlockDetails{}
	if v, ok := obs.Header.BlockAccessListHash.Get(); ok {
		details.BlockAccessListHash = block.Some(block.Hash32(v))
	}
	if v, ok := obs.Header.SlotNumber.Get(); ok {
		details.SlotNumber = block.Some(v)
	}
	metadata := block.BlockMetadata{
		Number:                 obs.Number,
		Hash:                   block.Hash32(obs.Hash),
		ParentHash:             block.Hash32(obs.Header.ParentHash),
		Timestamp:              block.TimestampFromSeconds(obs.Header.Time),
		SourceTransactionCount: obs.TransactionCount,
		Header:                 h,
		Details:                block.FamilyDetails{Eth: &details},
	}
	if err := metadata.Validate(); err != nil {
		return block.BlockRecord{}, semantic.SemanticDescriptor{}, fmt.Errorf("%w: %v", ErrBlockShape, err)
	}
	if metadata.Hash.IsZero() {
		return block.BlockRecord{}, semantic.SemanticDescriptor{}, fmt.Errorf("%w: zero block hash", ErrBlockShape)
	}
	record := block.BlockRecord{
		Activation: block.BlockActivationRef{
			Network:      id.Network,
			BlockHash:    block.Hash32(obs.Hash),
			ProducerID:   block.Id16(id.ProducerID),
			RunID:        block.Id16(id.RunID),
			ActivationID: block.ActivationID{DecisionSequence: id.DecisionSequ, EntityOrdinal: 0},
		},
		Admission:       admission,
		Metadata:        metadata,
		InitialSnapshot: snapshot,
	}
	if err := record.Validate(); err != nil {
		return block.BlockRecord{}, semantic.SemanticDescriptor{}, fmt.Errorf("%w: %v", ErrBlockShape, err)
	}
	descriptor, err := semantic.NewBlockDescriptor(semantic.KindApplied,
		semantic.Network{ChainID: semantic.UInt256(id.Network.ChainID),
			GenesisHash: semantic.Hash32(id.Network.GenesisHash)},
		id.ProducerID, id.RunID, id.DecisionSequ, semantic.Hash32(obs.Hash), 0, id.Source)
	if err != nil {
		return block.BlockRecord{}, semantic.SemanticDescriptor{}, err
	}
	return record, descriptor, nil
}
