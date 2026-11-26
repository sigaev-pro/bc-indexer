// The mapping of the typed model onto the Envelope tree of the logical
// schema registry (C6 of the live capture plan): one decided block import
// and its built BlockRecord become the nodes of the logical Envelope of
// MESSAGE_CONTRACT section 2, in depth-first preorder.
//
// Every field goes through the registry by name: the tag, the kind, the
// message type key, the enum and the exact byte length of a node come from
// the registry, and a node is appended only when the registry names the
// field with the kind the mapping uses. A mapping and a registry that
// disagree cannot build a tree.
package blockrecord

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// treeWriter appends the nodes of one record tree in depth-first preorder
// and resolves every field through the logical schema registry. The first
// failure stops the mapping; later calls do nothing.
type treeWriter struct {
	schema *proto.LogicalSchema
	nodes  []codec.TreeNode
	// open holds the message nodes that still take children, the root
	// first and the innermost message last.
	open []openMessage
	err  error
}

// openMessage is one message node of the tree that takes children.
type openMessage struct {
	index   int
	typeKey uint32
}

// newTreeWriter returns a writer whose tree is one open root message.
func newTreeWriter(schema *proto.LogicalSchema, root uint32) *treeWriter {
	w := &treeWriter{schema: schema, nodes: make([]codec.TreeNode, 0, 128),
		open: make([]openMessage, 0, 8)}
	w.nodes = append(w.nodes, codec.TreeNode{Kind: codec.ValueMessage, TypeKey: root})
	w.open = append(w.open, openMessage{index: 0, typeKey: root})
	return w
}

// finish returns the nodes of the closed tree.
func (w *treeWriter) finish() ([]codec.TreeNode, error) {
	if w.err != nil {
		return nil, w.err
	}
	if len(w.open) != 1 {
		return nil, fmt.Errorf("%w: %d messages of the record tree were not closed", proto.ErrInvalidValue,
			len(w.open)-1)
	}
	return w.nodes, nil
}

// fail records the first failure of the mapping.
func (w *treeWriter) fail(sentinel error, format string, args ...any) {
	w.err = fmt.Errorf("%w: %s", sentinel, fmt.Sprintf(format, args...))
}

// lookup returns the field name of the innermost open message and checks
// that the registry gives it the value kind kind.
func (w *treeWriter) lookup(name string, kind codec.ValueKind) (proto.FieldType, bool) {
	if w.err != nil {
		return proto.FieldType{}, false
	}
	parent := w.open[len(w.open)-1]
	f, ok := w.schema.FieldOf(parent.typeKey, name)
	if !ok {
		message, _ := w.schema.MessageType(parent.typeKey)
		w.fail(proto.ErrClosedRegistry, "message %s has no field %q", message.Name, name)
		return proto.FieldType{}, false
	}
	if valueKind(f.Kind) != kind {
		w.fail(proto.ErrInvalidValue, "field %s is %s in the registry and was mapped as %s", name, f.Kind, kind)
		return proto.FieldType{}, false
	}
	return f, true
}

// appendNode appends one child of the innermost open message.
func (w *treeWriter) appendNode(node codec.TreeNode) {
	w.nodes = append(w.nodes, node)
	w.nodes[w.open[len(w.open)-1].index].ChildCount++
}

// push opens the message field name of the innermost open message. Its
// children follow until pop.
func (w *treeWriter) push(name string) {
	f, ok := w.lookup(name, codec.ValueMessage)
	if !ok {
		return
	}
	w.appendNode(codec.TreeNode{FieldTag: f.Tag, HasFieldTag: true, Kind: codec.ValueMessage, TypeKey: f.Type})
	w.open = append(w.open, openMessage{index: len(w.nodes) - 1, typeKey: f.Type})
}

// pop closes the innermost open message.
func (w *treeWriter) pop() {
	w.open = w.open[:len(w.open)-1]
}

func (w *treeWriter) uint64(name string, value uint64) {
	if f, ok := w.lookup(name, codec.ValueUint64); ok {
		w.appendNode(codec.TreeNode{FieldTag: f.Tag, HasFieldTag: true, Kind: codec.ValueUint64, Uint64: value})
	}
}

func (w *treeWriter) uint32(name string, value uint32) {
	if f, ok := w.lookup(name, codec.ValueUint32); ok {
		w.appendNode(codec.TreeNode{FieldTag: f.Tag, HasFieldTag: true, Kind: codec.ValueUint32, Uint32: value})
	}
}

// bytes appends a Bytes node: of the exact length the registry names, or of
// any length for a variable field.
func (w *treeWriter) bytes(name string, value []byte) {
	f, ok := w.lookup(name, codec.ValueBytes)
	if !ok {
		return
	}
	if f.ByteLen != 0 && uint64(len(value)) != uint64(f.ByteLen) {
		w.fail(proto.ErrInvalidValue, "field %s holds %d bytes, want %d", name, len(value), f.ByteLen)
		return
	}
	w.appendNode(codec.TreeNode{FieldTag: f.Tag, HasFieldTag: true, Kind: codec.ValueBytes, Value: value})
}

func (w *treeWriter) utf8(name, value string) {
	if f, ok := w.lookup(name, codec.ValueUTF8); ok {
		w.appendNode(codec.TreeNode{FieldTag: f.Tag, HasFieldTag: true, Kind: codec.ValueUTF8, Value: []byte(value)})
	}
}

// enum appends an Enum node of a value the registry holds for the enum of
// the field.
func (w *treeWriter) enum(name string, value uint32) {
	f, ok := w.lookup(name, codec.ValueEnum)
	if !ok {
		return
	}
	if _, ok := w.schema.EnumValue(f.Enum, value); !ok {
		w.fail(proto.ErrClosedRegistry, "value %d is not registered in enum %s (field %s)", value, f.Enum, name)
		return
	}
	w.appendNode(codec.TreeNode{FieldTag: f.Tag, HasFieldTag: true, Kind: codec.ValueEnum, Enum: value})
}

// valueKind is the value kind of the D73 tree that carries a field kind of
// the registry.
func valueKind(kind proto.FieldKind) codec.ValueKind {
	switch kind {
	case proto.KindMessage:
		return codec.ValueMessage
	case proto.KindList:
		return codec.ValueList
	case proto.KindBool:
		return codec.ValueBool
	case proto.KindUint32:
		return codec.ValueUint32
	case proto.KindUint64:
		return codec.ValueUint64
	case proto.KindEnum:
		return codec.ValueEnum
	case proto.KindBytes:
		return codec.ValueBytes
	default:
		return codec.ValueUTF8
	}
}

// envelopeTree maps one built event onto the Envelope tree. The Envelope
// fields come from the input and the profile reference; the payload variant
// block_applied carries the record.
func envelopeTree(in Input, eventID proto.EventID, profile proto.ProfileRef,
	record *block.BlockRecord) ([]codec.TreeNode, error) {
	w := newTreeWriter(proto.LogicalSchemaV2(), proto.TypeEnvelope)
	w.uint32("schema_version", proto.SchemaVersion)

	w.push("network")
	writeNetwork(w, in.Network)
	w.pop()

	w.push("profile_ref")
	w.utf8("network_profile", profile.NetworkProfile)
	w.utf8("client_commit", profile.ClientCommit)
	w.utf8("adapter_version", profile.AdapterVersion)
	w.utf8("fork_rules_version", profile.ForkRulesVersion)
	w.utf8("normalization_version", profile.NormalizationVersion)
	w.pop()

	w.bytes("event_id", eventID[:])
	w.bytes("producer_id", in.ProducerID[:])
	w.bytes("run_id", in.RunID[:])

	w.push("source")
	w.bytes("node_instance_id", in.NodeInstanceID[:])
	w.bytes("writer_epoch", in.WriterEpoch[:])
	w.uint64("source_sequence", in.SourceSequence)
	w.pop()

	w.enum("capture_mode", proto.CaptureModeLive)

	w.push(proto.PayloadBlockApplied)
	writeBlockApplied(w, record)
	w.pop()

	return w.finish()
}

// writeNetwork maps a network identity: the EVM family, the chain id and
// the genesis hash.
func writeNetwork(w *treeWriter, network block.NetworkIdentity) {
	w.enum("family", uint32(proto.NetworkFamilyEVM))
	w.bytes("chain_id", network.ChainID[:])
	w.bytes("genesis_hash", network.GenesisHash[:])
}

// writeBlockApplied maps the BlockRecord: activation, admission, metadata
// and the initial lifecycle snapshot.
func writeBlockApplied(w *treeWriter, record *block.BlockRecord) {
	activation := &record.Activation
	w.push("activation")
	w.push("network")
	writeNetwork(w, activation.Network)
	w.pop()
	w.bytes("block_hash", activation.BlockHash[:])
	w.bytes("producer_id", activation.ProducerID[:])
	w.bytes("run_id", activation.RunID[:])
	w.push("activation_id")
	w.uint64("decision_sequence", activation.ActivationID.DecisionSequence)
	w.uint64("entity_ordinal", activation.ActivationID.EntityOrdinal)
	w.pop()
	w.pop()

	admission := &record.Admission
	w.push("admission")
	w.bytes("producer_id", admission.ProducerID[:])
	w.bytes("run_id", admission.RunID[:])
	w.uint64("observation_sequence", admission.ObservationSequence)
	w.pop()

	w.push("metadata")
	writeMetadata(w, &record.Metadata)
	w.pop()

	w.push("initial_snapshot")
	writeSnapshot(w, &record.InitialSnapshot)
	w.pop()
}

// writeMetadata maps the block metadata: identity, timestamp, the observed
// transaction count, the header and the BSC family details.
func writeMetadata(w *treeWriter, metadata *block.BlockMetadata) {
	w.uint64("number", metadata.Number)
	w.bytes("hash", metadata.Hash[:])
	w.bytes("parent_hash", metadata.ParentHash[:])

	w.push("timestamp")
	w.uint64("seconds", metadata.Timestamp.Seconds)
	w.uint32("nanos", metadata.Timestamp.Nanos)
	w.enum("precision", uint32(metadata.Timestamp.Precision))
	w.pop()

	w.uint64("source_transaction_count", metadata.SourceTransactionCount)

	w.push("header")
	writeHeader(w, &metadata.Header)
	w.pop()

	w.push("details")
	writeBscDetails(w, metadata.Details.Bsc)
	w.pop()
}

// writeHeader maps the typed header. The common fields are always present;
// each optional fork field is a node exactly when the native header carried
// it, and a present zero stays a present node.
func writeHeader(w *treeWriter, header *block.Header) {
	w.bytes("parent_hash", header.ParentHash[:])
	w.bytes("uncle_hash", header.UncleHash[:])
	w.bytes("beneficiary", header.Beneficiary[:])
	w.bytes("state_root", header.StateRoot[:])
	w.bytes("transactions_root", header.TransactionsRoot[:])
	w.bytes("receipts_root", header.ReceiptsRoot[:])
	w.bytes("logs_bloom", header.LogsBloom[:])
	w.bytes("difficulty", header.Difficulty[:])
	w.uint64("number", header.Number)
	w.uint64("gas_limit", header.GasLimit)
	w.uint64("gas_used", header.GasUsed)
	w.uint64("timestamp_seconds", header.TimestampSeconds)
	w.bytes("extra_data", header.ExtraData)
	w.bytes("mix_digest", header.MixDigest[:])
	w.bytes("nonce", header.Nonce[:])
	if v, ok := header.BaseFee.Get(); ok {
		w.bytes("base_fee", v[:])
	}
	if v, ok := header.WithdrawalsRoot.Get(); ok {
		w.bytes("withdrawals_root", v[:])
	}
	if v, ok := header.BlobGasUsed.Get(); ok {
		w.uint64("blob_gas_used", v)
	}
	if v, ok := header.ExcessBlobGas.Get(); ok {
		w.uint64("excess_blob_gas", v)
	}
	if v, ok := header.ParentBeaconBlockRoot.Get(); ok {
		w.bytes("parent_beacon_block_root", v[:])
	}
	if v, ok := header.RequestsHash.Get(); ok {
		w.bytes("requests_hash", v[:])
	}
	w.bytes("source_rlp", header.SourceRLP)
}

// writeBscDetails maps the BSC family details: the observed Parlia system
// transaction count and the vote snapshot, each a node only when observed.
func writeBscDetails(w *treeWriter, details *block.BscBlockDetails) {
	w.push("bsc")
	if v, ok := details.ParliaSystemTransactionCount.Get(); ok {
		w.uint64("parlia_system_transaction_count", v)
	}
	if v, ok := details.VoteSnapshot.Get(); ok {
		w.push("vote_snapshot")
		writeAnchor(w, "justified", v.Justified)
		writeAnchor(w, "finalized", v.Finalized)
		w.pop()
	}
	w.pop()
}

// writeSnapshot maps the initial lifecycle snapshot: the native decision,
// the observed execution head and the BSC finality markers.
func writeSnapshot(w *treeWriter, snapshot *block.LifecycleSnapshot) {
	decision := &snapshot.NativeDecision
	w.push("native_decision")
	w.bytes("node_instance_id", decision.NodeInstanceID[:])
	w.bytes("producer_id", decision.ProducerID[:])
	w.bytes("run_id", decision.RunID[:])
	w.uint64("decision_sequence", decision.DecisionSequence)
	w.pop()

	writeAnchor(w, "observed_execution_head", snapshot.ObservedExecutionHead)

	w.push("markers")
	writeBscMarkers(w, snapshot.Markers.Bsc)
	w.pop()
}

// writeAnchor maps one anchor: its height and its hash.
func writeAnchor(w *treeWriter, name string, ref block.AnchorRef) {
	w.push(name)
	w.uint64("height", ref.Height)
	w.bytes("hash", ref.Hash[:])
	w.pop()
}

// writeBscMarkers maps the BSC finality markers: the attested snapshot when
// one was observed, the effective finalized reference and its basis.
func writeBscMarkers(w *treeWriter, markers *block.BscMarkers) {
	w.push("bsc")
	if v, ok := markers.Attested.Get(); ok {
		w.push("attested")
		writeAnchor(w, "snapshot_head", v.SnapshotHead)
		writeAnchor(w, "justified", v.Justified)
		writeAnchor(w, "finalized", v.Finalized)
		w.pop()
	}
	writeReference(w, "effective_finalized", markers.EffectiveFinalized)
	w.enum("effective_basis", uint32(markers.EffectiveBasis))
	w.pop()
}

// writeReference maps one finality reference. Its kind decides which fields
// are present, as in the typed model: a known reference carries its anchor
// and its origin, an unavailable one its reason, and the other kinds carry
// neither.
func writeReference(w *treeWriter, name string, ref block.ReferenceObservation) {
	w.push(name)
	w.enum("kind", uint32(ref.Kind))
	if ref.Kind == block.ReferenceKnown {
		writeAnchor(w, "anchor", ref.Anchor)
		w.enum("origin", uint32(ref.Origin))
	}
	if ref.Kind == block.ReferenceUnavailable {
		w.enum("reason", uint32(ref.Reason))
	}
	w.pop()
}
