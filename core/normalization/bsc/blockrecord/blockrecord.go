// Package blockrecord builds the public BlockApplied event of one decided
// BSC block import (C2 of the live capture plan): the typed block.BlockRecord
// with its BSC finality markers, the semantic descriptor fixed before
// DELIVERY_STAGING, the canonical RecordFragment bytes of the logical
// Envelope, and the READY descriptors the manifest carries.
//
// BuildBlockEvent is a pure function of its input and the event id. It
// reads nothing: every fact arrives as an owned value in Input, captured by
// the collector on the native goroutine. A missing or invalid fact is a
// named error before any byte is produced; an absent fact is never filled
// with a zero.
//
// The Envelope tree is the one of MESSAGE_CONTRACT section 2. Its type keys
// and field tags come from the logical schema registry of protocol/proto;
// the mapping of the typed model to tree nodes goes through the registry
// field by field (tree.go), so a node the registry does not name cannot be
// built.
//
// The package imports core/normalization/bsc/block, protocol/block,
// protocol/semantic, protocol/proto and protocol/codec, and the standard
// library only.
package blockrecord

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"

	bscblock "github.com/sigaev-pro/bc-indexer/core/normalization/bsc/block"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// The two statuses of the lifecycle attestation read of one import
// (lifecycle.AttestationStatus of the native tree).
const (
	// StatusAttested is a snapshot with a vote attestation.
	StatusAttested = "ATTESTED"
	// StatusGenesisFallback is a correct snapshot without an attestation:
	// its justified and finalized blocks are the real genesis block.
	StatusGenesisFallback = "GENESIS_FALLBACK"
)

// Head is one native anchor as the decision evidence reports it.
type Head struct {
	Number uint64
	Hash   [32]byte
}

// Input is one decided BSC block import as owned values, captured on the
// native goroutine.
type Input struct {
	// Network and identities.
	// Network is the network identity ("EVM", chain id, genesis hash),
	// read from the native store.
	Network block.NetworkIdentity
	// NodeInstanceID is the native store identity
	// (stateprovider.StoreIdentity.StoreID).
	NodeInstanceID semantic.ID16
	// ProducerID and RunID are the producer journal identity.
	ProducerID, RunID semantic.ID16
	// WriterEpoch is new for every native process.
	WriterEpoch semantic.ID16
	// SourceSequence is the durable semantic descriptor counter, from 1.
	SourceSequence uint64
	// DecisionSequence is the sequence of the NATIVE_DECIDED IMPORT
	// decision.
	DecisionSequence uint64
	// ObservationSequence is the admission sequence of the EXEC_READY
	// observation of the block.
	ObservationSequence uint64

	// Block facts.
	// Header is the decided native header; SourceRLP is the native header
	// encoding.
	Header bscblock.SourceHeader
	// TxCount is the transaction count of the native body.
	TxCount uint64
	// SystemTxCount is the observed count of Parlia system transactions;
	// nil when it was not observed.
	SystemTxCount *uint64

	// What the decision step observed.
	// OldHead and NewHead are evidence.OldHead and evidence.NewHead.
	OldHead, NewHead Head
	// Justified and Finalized are evidence.Justified and
	// evidence.Finalized: the effective finality result of the decision.
	Justified, Finalized Head
	// Attestation is the result of the lifecycle attestation read of this
	// import.
	Attestation Attestation
}

// Attestation is the result of the lifecycle attestation read of one
// import (lifecycle.AttestationEvidence).
type Attestation struct {
	// Status is StatusAttested or StatusGenesisFallback.
	Status string
	// Justified and Finalized are the attested anchors: the target and the
	// source of the snapshot vote attestation, or both the genesis block
	// under the fallback.
	Justified, Finalized Head
	// Digest is the evidence digest of the attestation read: 64 lowercase
	// hex digits.
	Digest string
}

// BuiltEvent is one public event with its READY descriptors.
type BuiltEvent struct {
	// Semantic is fixed before DELIVERY_STAGING: a delivery attempt
	// changes none of it.
	Semantic semantic.SemanticDescriptor
	// Record is the typed payload, validated.
	Record block.BlockRecord
	// Fragments are the canonical RecordFragment bytes in index order, the
	// seal included.
	Fragments [][]byte
	// Event is the READY event descriptor.
	Event proto.EventDescriptor
	// Frags holds one fragment descriptor per fragment: the encoded length
	// and the SHA-256.
	Frags []proto.FragmentDescriptor
}

// BuildBlockEvent builds the BlockApplied event of one decided block
// import: family block, kind applied, entity ordinal 0, revision 0. eventID
// is producer_id16 || run_id16 || event_sequence and must carry the
// producer and the run of in. The result is a pure function of (in,
// eventID).
//
// A missing or invalid mandatory fact is a rejection that wraps
// block.ErrInvalidValue or proto.ErrInvalidValue and names the fact. The
// fragments own their bytes; the record shares the header bytes of in.
func BuildBlockEvent(in Input, eventID proto.EventID) (BuiltEvent, error) {
	if err := validate(in, eventID); err != nil {
		return BuiltEvent{}, err
	}
	// The profile reference is registered per network: a network without a
	// profile gets no record under a borrowed profile.
	profile, ok := proto.LogicalSchemaV2().ProfileForNetwork(in.Network.ChainID)
	if !ok {
		return BuiltEvent{}, fmt.Errorf("%w: %w: no profile is registered for chain id %x",
			proto.ErrInvalidValue, proto.ErrClosedRegistry, in.Network.ChainID)
	}
	metadata, err := bscblock.Project(in.Header, in.TxCount,
		bscblock.ParliaFacts{SystemTransactionCount: in.SystemTxCount})
	if err != nil {
		return BuiltEvent{}, fmt.Errorf("%w: block metadata: %w", block.ErrInvalidValue, err)
	}
	markers, err := markers(in)
	if err != nil {
		return BuiltEvent{}, err
	}
	record := block.BlockRecord{
		Activation: block.BlockActivationRef{
			Network:      in.Network,
			BlockHash:    in.Header.Hash,
			ProducerID:   block.Id16(in.ProducerID),
			RunID:        block.Id16(in.RunID),
			ActivationID: block.ActivationID{DecisionSequence: in.DecisionSequence, EntityOrdinal: 0},
		},
		Admission: block.ObservationRef{ProducerID: block.Id16(in.ProducerID),
			RunID: block.Id16(in.RunID), ObservationSequence: in.ObservationSequence},
		Metadata: metadata,
		InitialSnapshot: block.LifecycleSnapshot{
			NativeDecision: block.NativeDecisionRef{NodeInstanceID: block.Id16(in.NodeInstanceID),
				ProducerID: block.Id16(in.ProducerID), RunID: block.Id16(in.RunID),
				DecisionSequence: in.DecisionSequence},
			ObservedExecutionHead: anchor(in.NewHead),
			Markers:               block.FinalityMarkers{Bsc: &markers},
		},
	}
	if err := record.Validate(); err != nil {
		return BuiltEvent{}, fmt.Errorf("%w: block record: %w", block.ErrInvalidValue, err)
	}
	descriptor, err := semantic.NewBlockDescriptor(semantic.KindApplied,
		semantic.Network{ChainID: semantic.UInt256(in.Network.ChainID),
			GenesisHash: semantic.Hash32(in.Network.GenesisHash)},
		in.ProducerID, in.RunID, in.DecisionSequence, semantic.Hash32(in.Header.Hash), 0,
		semantic.SourceRef{NodeInstanceID: in.NodeInstanceID, WriterEpoch: in.WriterEpoch,
			SourceSequence: in.SourceSequence})
	if err != nil {
		return BuiltEvent{}, fmt.Errorf("%w: semantic descriptor: %w", proto.ErrInvalidValue, err)
	}
	nodes, err := envelopeTree(in, eventID, profile, &record)
	if err != nil {
		return BuiltEvent{}, err
	}
	network := proto.NetworkIdentity{Family: proto.NetworkFamilyEVM,
		ChainID: in.Network.ChainID, GenesisHash: in.Network.GenesisHash}
	var fragments [][]byte
	if _, err := codec.BuildRecord(eventID, network, proto.FamilyBlock, proto.KindApplied, proto.TypeEnvelope,
		nodes, func(index uint64, fragment []byte) error {
			fragments = append(fragments, bytes.Clone(fragment))
			return nil
		}); err != nil {
		return BuiltEvent{}, err
	}
	event, frags, err := proto.BuildEventDescriptor(0, eventID, proto.FamilyBlock, proto.KindApplied, fragments)
	if err != nil {
		return BuiltEvent{}, err
	}
	return BuiltEvent{Semantic: descriptor, Record: record, Fragments: fragments, Event: event, Frags: frags},
		nil
}

// validate rejects a missing or invalid mandatory fact of the input. The
// decision sequence and the observation sequence start at 0 in the native
// tree, so zero is a value for them and is not checked.
func validate(in Input, eventID proto.EventID) error {
	if err := in.Network.Validate(); err != nil {
		return err
	}
	if in.NodeInstanceID.IsZero() {
		return fmt.Errorf("%w: node instance id is zero", block.ErrInvalidValue)
	}
	if in.ProducerID.IsZero() {
		return fmt.Errorf("%w: producer id is zero", block.ErrInvalidValue)
	}
	if in.RunID.IsZero() {
		return fmt.Errorf("%w: run id is zero", block.ErrInvalidValue)
	}
	if in.WriterEpoch.IsZero() {
		return fmt.Errorf("%w: writer epoch is zero", block.ErrInvalidValue)
	}
	if in.SourceSequence == 0 {
		return fmt.Errorf("%w: source sequence 0", block.ErrInvalidValue)
	}
	if err := eventID.Validate(); err != nil {
		return err
	}
	if eventID.ProducerID() != proto.ID16(in.ProducerID) {
		return fmt.Errorf("%w: event id carries another producer than the input", proto.ErrInvalidValue)
	}
	if eventID.RunID() != proto.ID16(in.RunID) {
		return fmt.Errorf("%w: event id carries another run than the input", proto.ErrInvalidValue)
	}
	// The decision evidence.
	if in.OldHead.Hash == ([32]byte{}) {
		return fmt.Errorf("%w: old head hash is zero", block.ErrInvalidValue)
	}
	if in.NewHead.Hash == ([32]byte{}) {
		return fmt.Errorf("%w: new head hash is zero", block.ErrInvalidValue)
	}
	if in.Justified.Hash == ([32]byte{}) {
		return fmt.Errorf("%w: justified hash is zero", block.ErrInvalidValue)
	}
	if in.Finalized.Hash == ([32]byte{}) {
		return fmt.Errorf("%w: finalized hash is zero", block.ErrInvalidValue)
	}
	// The decided block is the header.
	if in.NewHead.Hash != in.Header.Hash {
		return fmt.Errorf("%w: decided head %x differs from the header hash %x", block.ErrInvalidValue,
			in.NewHead.Hash, in.Header.Hash)
	}
	if in.NewHead.Number != in.Header.Number {
		return fmt.Errorf("%w: decided head number %d differs from the header number %d", block.ErrInvalidValue,
			in.NewHead.Number, in.Header.Number)
	}
	// The attestation read.
	return validateAttestation(in.Attestation)
}

// validateAttestation rejects an attestation read without its anchors or
// without its digest. Its status is checked where the markers are chosen.
func validateAttestation(a Attestation) error {
	if a.Justified.Hash == ([32]byte{}) {
		return fmt.Errorf("%w: attestation justified hash is zero", block.ErrInvalidValue)
	}
	if a.Finalized.Hash == ([32]byte{}) {
		return fmt.Errorf("%w: attestation finalized hash is zero", block.ErrInvalidValue)
	}
	if !isDigest(a.Digest) {
		return fmt.Errorf("%w: attestation digest %q is not 64 lowercase hex digits", block.ErrInvalidValue, a.Digest)
	}
	return nil
}

// isDigest reports whether s is 64 lowercase hex digits.
func isDigest(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// anchor is the anchor reference of one head.
func anchor(h Head) block.AnchorRef {
	return block.AnchorRef{Height: h.Number, Hash: h.Hash}
}
