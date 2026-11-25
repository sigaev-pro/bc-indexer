// The closed logical schema registry of schema_version 2: the message type
// keys, field tags and enum values a typed record tree may name (D73,
// MESSAGE_CONTRACT section 6.1).
//
// The fragment codec of protocol/codec verifies the structure of a record
// tree: continuous node ids, parents before children, ascending field tags
// under a message, element indices under a list, and child counts that match
// the children. It does not decide whether a type key, a field tag or an
// enum value exists. This registry does, and it is closed: a value outside
// it is an error, never a passthrough.
//
// The first version registers one path and nothing else: the Envelope, its
// own messages, and the block family payload variant block_applied with
// every message block.BlockRecord reaches. A later family extends the
// registry as a versioned change; nothing registered here is renumbered.
//
// The table is static source. The builder functions of this file refuse an
// invalid entry (a zero or repeated key, tag or value; a dangling type or
// enum reference; a field shape that contradicts its kind) by panicking with
// the reason: that is a programming error of this file, not an input a
// caller can produce. The tests build broken tables on purpose to reach
// each refusal.
package proto

import (
	"cmp"
	"fmt"
	"slices"
	"sync"

	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
)

// SchemaVersion is the logical schema version the registry describes: 2.
const SchemaVersion uint32 = 2

// Message type keys of the common Envelope path. Keys 1 to 9 are reserved
// for this path.
const (
	TypeEnvelope        uint32 = 1
	TypeNetworkIdentity uint32 = 2
	TypeProfileRef      uint32 = 3
	TypeSourceRef       uint32 = 4
)

// Message type keys of the block family. Keys 10 to 69 are reserved for
// this family; a later family takes a range of its own above 69. Key 9001
// is never reused: it is the demo key of the reference tool
// cmd/extractor-rpc.
const (
	TypeBlockApplied          uint32 = 10
	TypeBlockActivationRef    uint32 = 11
	TypeActivationID          uint32 = 12
	TypeObservationRef        uint32 = 13
	TypeBlockMetadata         uint32 = 14
	TypeTimestamp             uint32 = 15
	TypeBlockHeader           uint32 = 16
	TypeFamilyDetails         uint32 = 17
	TypeEthBlockDetails       uint32 = 18
	TypeBscBlockDetails       uint32 = 19
	TypeParliaVoteSnapshot    uint32 = 20
	TypeBorBlockDetails       uint32 = 21
	TypeHeimdallCheckpointRef uint32 = 22
	TypeLifecycleSnapshot     uint32 = 23
	TypeNativeDecisionRef     uint32 = 24
	TypeAnchorRef             uint32 = 25
	TypeFinalityMarkers       uint32 = 26
	TypeEthereumMarkers       uint32 = 27
	TypeBscMarkers            uint32 = 28
	TypeAttestedSnapshot      uint32 = 29
	TypePolygonMarkers        uint32 = 30
	TypeReferenceObservation  uint32 = 31
)

// PayloadTagBase opens the tags of the payload oneof of the Envelope. A
// variant tag is PayloadTagBase plus the registry v1 variant number of
// protocol/profiles, so a tag never collides with the fields 1 to 99 of the
// Envelope and can be read from its variant number. Only block_applied is
// registered; every other variant is an unknown field tag until a
// versioned change registers it with its message.
const PayloadTagBase uint32 = 100

// PayloadTagBlockApplied is the tag of the registered payload variant.
const PayloadTagBlockApplied uint32 = PayloadTagBase + uint32(profiles.VariantBlockApplied)

// PayloadBlockApplied is the field name of the registered payload variant.
const PayloadBlockApplied = "block_applied"

// CaptureModeLive is the only registered capture mode. LIVE=1 is the mode
// of every event the first profile publishes; SYNC and BACKFILL are
// deferred and are not registered values.
const CaptureModeLive uint32 = 1

// Lengths of the fixed bytes fields of the registry, in bytes.
const (
	sizeEventID   uint32 = 40
	sizeID16      uint32 = 16
	sizeHash32    uint32 = 32
	sizeAddress20 uint32 = 20
	sizeUInt256   uint32 = 32
	sizeBloom     uint32 = 256
	sizeNonce     uint32 = 8
)

// The registered profile reference of the BSC block event: the five strings
// of ProfileRef (MESSAGE_CONTRACT section 2). Each names one versioned
// input of the record and changes only with that input.
const (
	// ProfileNetworkBscTestnet is the network profile: the BSC Chapel
	// testnet.
	ProfileNetworkBscTestnet = "bsc-testnet"
	// ProfileClientCommitBsc is the client commit of the pinned BSC client
	// the adapter and the fork rules were written for: BSC v1.8.2.
	ProfileClientCommitBsc = "54afaff4da31ac9e49ba646a4f65104edac5a2f3"
	// ProfileAdapterVersionBsc names the version of the BSC capture adapter
	// whose facts the record carries.
	ProfileAdapterVersionBsc = "bsc-adapter-v1"
	// ProfileForkRulesVersionBsc names the fork rules of the pinned client
	// (the fork schedule of the commit above). The block event builder
	// derives no fork rule; the optional header fields follow native
	// presence.
	ProfileForkRulesVersionBsc = "bsc-fork-rules-v1"
	// ProfileNormalizationVersionBsc names the normalization profile of the
	// BSC block event.
	ProfileNormalizationVersionBsc = "bsc-normalization-v1"
	// chainIDBscTestnet is the chain id of the network the profile is
	// registered for.
	chainIDBscTestnet uint64 = 97
)

// FieldKind names the value kinds of the D73 tree. They are the value kinds
// of codec.TreeNode.
type FieldKind uint8

// Field kinds. Zero is not a kind.
const (
	KindMessage FieldKind = iota + 1
	KindList
	KindBool
	KindUint32
	KindUint64
	KindEnum
	KindBytes
	KindUTF8
)

// Valid reports whether k is a registered field kind.
func (k FieldKind) Valid() bool { return k >= KindMessage && k <= KindUTF8 }

// String returns the kind name of k.
func (k FieldKind) String() string {
	switch k {
	case KindMessage:
		return "message"
	case KindList:
		return "list"
	case KindBool:
		return "bool"
	case KindUint32:
		return "uint32"
	case KindUint64:
		return "uint64"
	case KindEnum:
		return "enum"
	case KindBytes:
		return "bytes"
	case KindUTF8:
		return "utf8"
	}
	return fmt.Sprintf("FieldKind(%d)", uint8(k))
}

// MessageType is one message of the logical schema. Fields are in ascending
// tag order.
type MessageType struct {
	Key    uint32
	Name   string
	Fields []FieldType
}

// FieldType is one field of a message.
//
// The presence rules of a field, which the mapping of a typed model to tree
// nodes follows and a reader can check:
//
//   - a plain mandatory field (Optional false, Oneof empty) carries exactly
//     one node;
//   - an optional field (Optional true) carries one node or none: a present
//     zero stays a present node, and an absent value is never a zero node;
//   - a oneof variant (Oneof set) belongs to that group of its message, and
//     exactly one variant of the group carries a node.
type FieldType struct {
	Tag  uint32
	Name string
	Kind FieldKind
	// Type is the message type key of a message field or of the element of
	// a list; 0 for every other kind.
	Type uint32
	// Optional marks a field that is a present node or no node.
	Optional bool
	// Enum is the enum type name of an enum field; empty for every other
	// kind.
	Enum string
	// ByteLen is the exact length of a Bytes field; 0 for variable bytes
	// and for every other kind.
	ByteLen uint32
	// Oneof names the oneof group this field is a variant of; empty for a
	// plain field.
	Oneof string
}

// ProfileRef is the profile reference of one Envelope: the five strings of
// MESSAGE_CONTRACT section 2.
type ProfileRef struct {
	NetworkProfile       string
	ClientCommit         string
	AdapterVersion       string
	ForkRulesVersion     string
	NormalizationVersion string
}

// LogicalSchema is one closed version of the logical schema. A schema is
// built by the table of this file and never changes afterwards; every
// lookup returns copies, so a caller cannot alter the registry.
type LogicalSchema struct {
	profiles map[[32]byte]ProfileRef
	messages map[uint32]storedMessage
	enums    map[string]storedEnum
}

// storedMessage holds one message: its fields in ascending tag order and
// the two indexes over the same values.
type storedMessage struct {
	name   string
	fields []FieldType
	byName map[string]uint32
	byTag  map[uint32]FieldType
}

// storedEnum holds the name of every value of one enum.
type storedEnum struct {
	names map[uint32]string
}

// schemaV2 builds the registry of schema_version 2 at its first use and
// returns the same instance afterwards.
var schemaV2 = sync.OnceValue(buildSchemaV2)

// LogicalSchemaV2 returns the closed registry of schema_version 2. The
// result is shared and read-only.
func LogicalSchemaV2() *LogicalSchema { return schemaV2() }

// MessageType returns the message of key, or ok=false when key is not a
// registered type key.
func (s *LogicalSchema) MessageType(key uint32) (MessageType, bool) {
	m, ok := s.messages[key]
	if !ok {
		return MessageType{}, false
	}
	return MessageType{Key: key, Name: m.name, Fields: slices.Clone(m.fields)}, true
}

// Field returns the field tag of the message typeKey, or ok=false when the
// type key or the field tag is not registered. An unregistered type key
// reads as a message without indexes, so it misses on every tag.
func (s *LogicalSchema) Field(typeKey uint32, tag uint32) (FieldType, bool) {
	f, ok := s.messages[typeKey].byTag[tag]
	return f, ok
}

// FieldOf returns the field name of the message typeKey, or ok=false when
// the type key or the field name is not registered. A producer that maps a
// typed model by field name resolves each tag through it.
func (s *LogicalSchema) FieldOf(typeKey uint32, name string) (FieldType, bool) {
	tag, ok := s.messages[typeKey].byName[name]
	if !ok {
		return FieldType{}, false
	}
	return s.messages[typeKey].byTag[tag], true
}

// EnumValue returns the registered name of value of the enum, or ok=false
// when the enum type name or the value is not registered.
func (s *LogicalSchema) EnumValue(enum string, value uint32) (name string, ok bool) {
	name, ok = s.enums[enum].names[value]
	return name, ok
}

// ProfileForNetwork returns the profile registered for the network with
// chain id chainID, or ok=false when no profile is registered for it.
func (s *LogicalSchema) ProfileForNetwork(chainID [32]byte) (ProfileRef, bool) {
	ref, ok := s.profiles[chainID]
	return ref, ok
}

// ------------------------------------------------------------------ table

// buildSchemaV2 is the registry of schema_version 2.
func buildSchemaV2() *LogicalSchema {
	t := newSchemaTable()

	t.profile(chainIDBscTestnet, ProfileRef{
		NetworkProfile:       ProfileNetworkBscTestnet,
		ClientCommit:         ProfileClientCommitBsc,
		AdapterVersion:       ProfileAdapterVersionBsc,
		ForkRulesVersion:     ProfileForkRulesVersionBsc,
		NormalizationVersion: ProfileNormalizationVersionBsc,
	})

	t.enum("CaptureMode", enumEntry{CaptureModeLive, "LIVE"})
	t.enum("NetworkFamily", enumEntry{1, "EVM"})
	t.enum("TimestampPrecision", enumEntry{1, "SECONDS"}, enumEntry{2, "MILLISECONDS"},
		enumEntry{3, "NANOSECONDS"})
	t.enum("StateSyncMode", enumEntry{1, "END_OF_BLOCK_COMMIT"}, enumEntry{2, "BODY_TRANSACTION"})
	t.enum("CheckpointKind", enumEntry{1, "MILESTONE"}, enumEntry{2, "CHECKPOINT"})
	t.enum("EvidenceOrigin", enumEntry{1, "NATIVE_LIVE"}, enumEntry{2, "NATIVE_RESTART"},
		enumEntry{3, "RETAINED_DURABLE"})
	t.enum("UnavailableReason", enumEntry{1, "SOURCE_OFFLINE"}, enumEntry{2, "SOURCE_SYNCING"},
		enumEntry{3, "STATE_UNAVAILABLE"}, enumEntry{4, "CHECKPOINT_PENDING"})
	t.enum("ReferenceKind", enumEntry{1, "KNOWN"}, enumEntry{2, "NOT_OBSERVED"},
		enumEntry{3, "NATIVE_REFERENCE_UNSET"}, enumEntry{4, "UNAVAILABLE"})
	t.enum("EthereumSafeBasis", enumEntry{1, "CONSENSUS_SETTER"}, enumEntry{2, "STARTUP_FINALIZED_FALLBACK"},
		enumEntry{3, "UNSET"})
	t.enum("BscEffectiveBasis", enumEntry{1, "HEADER_SNAPSHOT"}, enumEntry{2, "VOTE_POOL_QUORUM"},
		enumEntry{3, "GENESIS_FALLBACK"}, enumEntry{4, "UNSET"})

	// The common Envelope path.
	t.message(TypeEnvelope, "Envelope",
		t.uint32(1, "schema_version"),
		t.msg(2, "network", TypeNetworkIdentity),
		t.msg(3, "profile_ref", TypeProfileRef),
		t.bytes(4, "event_id", sizeEventID),
		t.bytes(5, "producer_id", sizeID16),
		t.bytes(6, "run_id", sizeID16),
		t.msg(7, "source", TypeSourceRef),
		t.enumField(8, "capture_mode", "CaptureMode"),
		t.variant(t.msg(PayloadTagBlockApplied, PayloadBlockApplied, TypeBlockApplied), "payload"),
	)
	t.message(TypeNetworkIdentity, "NetworkIdentity",
		t.enumField(1, "family", "NetworkFamily"),
		t.bytes(2, "chain_id", sizeUInt256),
		t.bytes(3, "genesis_hash", sizeHash32),
	)
	t.message(TypeProfileRef, "ProfileRef",
		t.utf8(1, "network_profile"),
		t.utf8(2, "client_commit"),
		t.utf8(3, "adapter_version"),
		t.utf8(4, "fork_rules_version"),
		t.utf8(5, "normalization_version"),
	)
	t.message(TypeSourceRef, "SourceRef",
		t.bytes(1, "node_instance_id", sizeID16),
		t.bytes(2, "writer_epoch", sizeID16),
		t.uint64(3, "source_sequence"),
	)

	// The block family. Field tags follow the field order of the typed
	// model of protocol/block.
	t.message(TypeBlockApplied, "BlockApplied",
		t.msg(1, "activation", TypeBlockActivationRef),
		t.msg(2, "admission", TypeObservationRef),
		t.msg(3, "metadata", TypeBlockMetadata),
		t.msg(4, "initial_snapshot", TypeLifecycleSnapshot),
	)
	t.message(TypeBlockActivationRef, "BlockActivationRef",
		t.msg(1, "network", TypeNetworkIdentity),
		t.bytes(2, "block_hash", sizeHash32),
		t.bytes(3, "producer_id", sizeID16),
		t.bytes(4, "run_id", sizeID16),
		t.msg(5, "activation_id", TypeActivationID),
	)
	t.message(TypeActivationID, "ActivationID",
		t.uint64(1, "decision_sequence"),
		t.uint64(2, "entity_ordinal"),
	)
	t.message(TypeObservationRef, "ObservationRef",
		t.bytes(1, "producer_id", sizeID16),
		t.bytes(2, "run_id", sizeID16),
		t.uint64(3, "observation_sequence"),
	)
	t.message(TypeBlockMetadata, "BlockMetadata",
		t.uint64(1, "number"),
		t.bytes(2, "hash", sizeHash32),
		t.bytes(3, "parent_hash", sizeHash32),
		t.msg(4, "timestamp", TypeTimestamp),
		t.uint64(5, "source_transaction_count"),
		t.msg(6, "header", TypeBlockHeader),
		t.msg(7, "details", TypeFamilyDetails),
	)
	t.message(TypeTimestamp, "Timestamp",
		t.uint64(1, "seconds"),
		t.uint32(2, "nanos"),
		t.enumField(3, "precision", "TimestampPrecision"),
	)
	t.message(TypeBlockHeader, "BlockHeader",
		t.bytes(1, "parent_hash", sizeHash32),
		t.bytes(2, "uncle_hash", sizeHash32),
		t.bytes(3, "beneficiary", sizeAddress20),
		t.bytes(4, "state_root", sizeHash32),
		t.bytes(5, "transactions_root", sizeHash32),
		t.bytes(6, "receipts_root", sizeHash32),
		t.bytes(7, "logs_bloom", sizeBloom),
		t.bytes(8, "difficulty", sizeUInt256),
		t.uint64(9, "number"),
		t.uint64(10, "gas_limit"),
		t.uint64(11, "gas_used"),
		t.uint64(12, "timestamp_seconds"),
		t.bytes(13, "extra_data", 0),
		t.bytes(14, "mix_digest", sizeHash32),
		t.bytes(15, "nonce", sizeNonce),
		t.optional(t.bytes(16, "base_fee", sizeUInt256)),
		t.optional(t.bytes(17, "withdrawals_root", sizeHash32)),
		t.optional(t.uint64(18, "blob_gas_used")),
		t.optional(t.uint64(19, "excess_blob_gas")),
		t.optional(t.bytes(20, "parent_beacon_block_root", sizeHash32)),
		t.optional(t.bytes(21, "requests_hash", sizeHash32)),
		t.bytes(22, "source_rlp", 0),
	)
	t.message(TypeFamilyDetails, "FamilyDetails",
		t.variant(t.msg(1, "eth", TypeEthBlockDetails), "family"),
		t.variant(t.msg(2, "bsc", TypeBscBlockDetails), "family"),
		t.variant(t.msg(3, "bor", TypeBorBlockDetails), "family"),
	)
	t.message(TypeEthBlockDetails, "EthBlockDetails",
		t.optional(t.bytes(1, "block_access_list_hash", sizeHash32)),
		t.optional(t.uint64(2, "slot_number")),
	)
	t.message(TypeBscBlockDetails, "BscBlockDetails",
		t.optional(t.uint64(1, "parlia_system_transaction_count")),
		t.optional(t.msg(2, "vote_snapshot", TypeParliaVoteSnapshot)),
	)
	t.message(TypeParliaVoteSnapshot, "ParliaVoteSnapshot",
		t.msg(1, "justified", TypeAnchorRef),
		t.msg(2, "finalized", TypeAnchorRef),
	)
	t.message(TypeBorBlockDetails, "BorBlockDetails",
		t.optional(t.uint64(1, "sprint_length")),
		t.optional(t.boolean(2, "sprint_end")),
		t.optional(t.boolean(3, "side_chain_block")),
		t.optional(t.uint64(4, "state_sync_count")),
		t.optional(t.enumField(5, "state_sync_mode", "StateSyncMode")),
		t.list(6, "heimdall_checkpoints", TypeHeimdallCheckpointRef),
	)
	t.message(TypeHeimdallCheckpointRef, "HeimdallCheckpointRef",
		t.enumField(1, "kind", "CheckpointKind"),
		t.msg(2, "end", TypeAnchorRef),
		t.optional(t.uint64(3, "checkpoint_id")),
	)
	t.message(TypeLifecycleSnapshot, "LifecycleSnapshot",
		t.msg(1, "native_decision", TypeNativeDecisionRef),
		t.msg(2, "observed_execution_head", TypeAnchorRef),
		t.msg(3, "markers", TypeFinalityMarkers),
	)
	t.message(TypeNativeDecisionRef, "NativeDecisionRef",
		t.bytes(1, "node_instance_id", sizeID16),
		t.bytes(2, "producer_id", sizeID16),
		t.bytes(3, "run_id", sizeID16),
		t.uint64(4, "decision_sequence"),
	)
	t.message(TypeAnchorRef, "AnchorRef",
		t.uint64(1, "height"),
		t.bytes(2, "hash", sizeHash32),
	)
	t.message(TypeFinalityMarkers, "FinalityMarkers",
		t.variant(t.msg(1, "eth", TypeEthereumMarkers), "family"),
		t.variant(t.msg(2, "bsc", TypeBscMarkers), "family"),
		t.variant(t.msg(3, "polygon", TypePolygonMarkers), "family"),
	)
	t.message(TypeEthereumMarkers, "EthereumMarkers",
		t.msg(1, "safe", TypeReferenceObservation),
		t.msg(2, "finalized", TypeReferenceObservation),
		t.enumField(3, "safe_basis", "EthereumSafeBasis"),
	)
	t.message(TypeBscMarkers, "BscMarkers",
		t.optional(t.msg(1, "attested", TypeAttestedSnapshot)),
		t.msg(2, "effective_finalized", TypeReferenceObservation),
		t.enumField(3, "effective_basis", "BscEffectiveBasis"),
	)
	t.message(TypeAttestedSnapshot, "AttestedSnapshot",
		t.msg(1, "snapshot_head", TypeAnchorRef),
		t.msg(2, "justified", TypeAnchorRef),
		t.msg(3, "finalized", TypeAnchorRef),
	)
	t.message(TypePolygonMarkers, "PolygonMarkers",
		t.msg(1, "milestone", TypeReferenceObservation),
		t.msg(2, "checkpoint", TypeReferenceObservation),
		t.msg(3, "native_finalized", TypeReferenceObservation),
		t.msg(4, "l1_checkpoint", TypeReferenceObservation),
	)
	// anchor, origin and reason are optional because the kind of the
	// reference decides which of them apply: a known reference carries its
	// anchor and origin, an unavailable one its reason, and the other kinds
	// carry neither.
	t.message(TypeReferenceObservation, "ReferenceObservation",
		t.enumField(1, "kind", "ReferenceKind"),
		t.optional(t.msg(2, "anchor", TypeAnchorRef)),
		t.optional(t.enumField(3, "origin", "EvidenceOrigin")),
		t.optional(t.enumField(4, "reason", "UnavailableReason")),
	)

	t.finish()
	return t.schema
}

// enumEntry is one named value of an enum of the table.
type enumEntry struct {
	value uint32
	name  string
}

// schemaTable builds one logical schema.
type schemaTable struct {
	schema *LogicalSchema
}

func newSchemaTable() *schemaTable {
	return &schemaTable{schema: &LogicalSchema{
		profiles: make(map[[32]byte]ProfileRef),
		messages: make(map[uint32]storedMessage),
		enums:    make(map[string]storedEnum),
	}}
}

// invalid ends the construction of an invalid table with the reason.
func invalid(format string, args ...any) {
	panic(fmt.Sprintf("proto: invalid logical schema table: "+format, args...))
}

// profile registers ref for the network with chain id chainID.
func (t *schemaTable) profile(chainID uint64, ref ProfileRef) {
	if ref.NetworkProfile == "" || ref.ClientCommit == "" || ref.AdapterVersion == "" ||
		ref.ForkRulesVersion == "" || ref.NormalizationVersion == "" {
		invalid("profile of chain id %d has an empty string", chainID)
	}
	if chainID == 0 {
		invalid("profile %s has chain id 0", ref.NetworkProfile)
	}
	key := chainIDBytes(chainID)
	if _, seen := t.schema.profiles[key]; seen {
		invalid("profile of chain id %d is registered twice", chainID)
	}
	t.schema.profiles[key] = ref
}

// enum registers one enum with its named values.
func (t *schemaTable) enum(name string, entries ...enumEntry) {
	if name == "" {
		invalid("enum without a name")
	}
	if len(entries) == 0 {
		invalid("enum %s without values", name)
	}
	if _, seen := t.schema.enums[name]; seen {
		invalid("enum %s is registered twice", name)
	}
	stored := storedEnum{names: make(map[uint32]string, len(entries))}
	seen := make(map[string]uint32, len(entries))
	for _, e := range entries {
		if e.value == 0 {
			invalid("enum %s has value 0 (%s); zero is not a registered value", name, e.name)
		}
		if e.name == "" {
			invalid("enum %s value %d has no name", name, e.value)
		}
		if other, dup := seen[e.name]; dup {
			invalid("enum %s name %s is registered for values %d and %d", name, e.name, other, e.value)
		}
		if _, dup := stored.names[e.value]; dup {
			invalid("enum %s value %d is registered twice", name, e.value)
		}
		seen[e.name] = e.value
		stored.names[e.value] = e.name
	}
	t.schema.enums[name] = stored
}

// message registers one message with its fields.
func (t *schemaTable) message(key uint32, name string, fields ...FieldType) {
	if key == 0 {
		invalid("message %s has type key 0", name)
	}
	if name == "" {
		invalid("message %d has no name", key)
	}
	if len(fields) == 0 {
		invalid("message %s has no fields", name)
	}
	for other, m := range t.schema.messages {
		if m.name == name {
			invalid("message name %s is registered for type keys %d and %d", name, other, key)
		}
	}
	if _, seen := t.schema.messages[key]; seen {
		invalid("type key %d is registered twice", key)
	}
	m := storedMessage{name: name,
		fields: sortedFields(fields),
		byName: make(map[string]uint32, len(fields)),
		byTag:  make(map[uint32]FieldType, len(fields))}
	for _, f := range fields {
		if f.Tag == 0 {
			invalid("message %s field %s has tag 0", name, f.Name)
		}
		if f.Name == "" {
			invalid("message %s has an unnamed field with tag %d", name, f.Tag)
		}
		if !f.Kind.Valid() {
			invalid("message %s field %s has kind %d", name, f.Name, uint8(f.Kind))
		}
		if _, dup := m.byTag[f.Tag]; dup {
			invalid("message %s tag %d is registered twice", name, f.Tag)
		}
		if other, dup := m.byName[f.Name]; dup {
			invalid("message %s name %s is registered for tags %d and %d", name, f.Name, other, f.Tag)
		}
		if f.Oneof != "" && f.Optional {
			invalid("message %s field %s is a oneof variant and optional; a variant is present by its group",
				name, f.Name)
		}
		m.byTag[f.Tag] = f
		m.byName[f.Name] = f.Tag
	}
	t.schema.messages[key] = m
}

// finish checks what a single entry cannot show: every type and enum
// reference resolves and the shape of a field agrees with its kind.
func (t *schemaTable) finish() {
	for _, m := range t.schema.messages {
		for _, f := range m.fields {
			switch f.Kind {
			case KindMessage, KindList:
				if _, ok := t.schema.messages[f.Type]; !ok {
					invalid("message %s field %s refers to unregistered type key %d", m.name, f.Name, f.Type)
				}
			default:
				if f.Type != 0 {
					invalid("message %s field %s has kind %s and type key %d", m.name, f.Name, f.Kind, f.Type)
				}
			}
			if f.Kind == KindEnum {
				if _, ok := t.schema.enums[f.Enum]; !ok {
					invalid("message %s field %s refers to unregistered enum %s", m.name, f.Name, f.Enum)
				}
			} else if f.Enum != "" {
				invalid("message %s field %s has kind %s and enum %s", m.name, f.Name, f.Kind, f.Enum)
			}
			if f.Kind != KindBytes && f.ByteLen != 0 {
				invalid("message %s field %s has kind %s and byte length %d", m.name, f.Name, f.Kind, f.ByteLen)
			}
		}
	}
}

// sortedFields returns fields in ascending tag order. It sorts the variadic
// slice of message in place; no caller holds that slice.
func sortedFields(fields []FieldType) []FieldType {
	slices.SortFunc(fields, func(a, b FieldType) int { return cmp.Compare(a.Tag, b.Tag) })
	return fields
}

// chainIDBytes is chainID as the 32 big-endian bytes of a UInt256 chain id.
func chainIDBytes(chainID uint64) [32]byte {
	var out [32]byte
	for i := 0; i < 8; i++ {
		out[31-i] = byte(chainID >> (8 * i))
	}
	return out
}

// Field constructors of the table.

func (t *schemaTable) uint32(tag uint32, name string) FieldType {
	return FieldType{Tag: tag, Name: name, Kind: KindUint32}
}

func (t *schemaTable) uint64(tag uint32, name string) FieldType {
	return FieldType{Tag: tag, Name: name, Kind: KindUint64}
}

func (t *schemaTable) boolean(tag uint32, name string) FieldType {
	return FieldType{Tag: tag, Name: name, Kind: KindBool}
}

func (t *schemaTable) msg(tag uint32, name string, typeKey uint32) FieldType {
	return FieldType{Tag: tag, Name: name, Kind: KindMessage, Type: typeKey}
}

func (t *schemaTable) list(tag uint32, name string, elementKey uint32) FieldType {
	return FieldType{Tag: tag, Name: name, Kind: KindList, Type: elementKey}
}

// bytes is a Bytes field of exactly length bytes; length 0 is a variable
// length.
func (t *schemaTable) bytes(tag uint32, name string, length uint32) FieldType {
	return FieldType{Tag: tag, Name: name, Kind: KindBytes, ByteLen: length}
}

func (t *schemaTable) utf8(tag uint32, name string) FieldType {
	return FieldType{Tag: tag, Name: name, Kind: KindUTF8}
}

func (t *schemaTable) enumField(tag uint32, name, enum string) FieldType {
	return FieldType{Tag: tag, Name: name, Kind: KindEnum, Enum: enum}
}

func (t *schemaTable) optional(f FieldType) FieldType {
	f.Optional = true
	return f
}

func (t *schemaTable) variant(f FieldType, group string) FieldType {
	f.Oneof = group
	return f
}
