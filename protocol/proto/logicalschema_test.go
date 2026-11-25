package proto

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
)

// schemaKeys lists every message type key the registry registers, by
// scanning the keys below the bound.
func schemaKeys(t *testing.T, s *LogicalSchema) []uint32 {
	t.Helper()
	var keys []uint32
	for key := uint32(1); key <= 1<<16; key++ {
		if _, ok := s.MessageType(key); ok {
			keys = append(keys, key)
		}
	}
	return keys
}

// schemaReachable lists every message type key reachable from root through
// message fields, list elements and oneof variants. A reachable key that is
// not registered fails the test.
func schemaReachable(t *testing.T, s *LogicalSchema, root uint32) map[uint32]bool {
	t.Helper()
	reachable := make(map[uint32]bool)
	queue := []uint32{root}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if reachable[key] {
			continue
		}
		m, ok := s.MessageType(key)
		if !ok {
			t.Fatalf("type key %d is reachable from %d but not registered", key, root)
		}
		reachable[key] = true
		for _, f := range m.Fields {
			if f.Kind == KindMessage || f.Kind == KindList {
				queue = append(queue, f.Type)
			}
		}
	}
	return reachable
}

// schemaFieldList renders the fields of one message as "name=shape" words:
// the kind, the element or message type key, the enum name, the exact byte
// length, a trailing ? for an optional field and {group} for a oneof
// variant.
func schemaFieldList(t *testing.T, s *LogicalSchema, key uint32) []string {
	t.Helper()
	m, ok := s.MessageType(key)
	if !ok {
		t.Fatalf("type key %d is not registered", key)
	}
	out := make([]string, 0, len(m.Fields))
	for _, f := range m.Fields {
		var shape string
		switch f.Kind {
		case KindMessage:
			shape = fmt.Sprintf("message(%d)", f.Type)
		case KindList:
			shape = fmt.Sprintf("list(%d)", f.Type)
		case KindEnum:
			shape = fmt.Sprintf("enum(%s)", f.Enum)
		case KindBytes:
			shape = "bytes"
			if f.ByteLen != 0 {
				shape = fmt.Sprintf("bytes%d", f.ByteLen)
			}
		default:
			shape = f.Kind.String()
		}
		if f.Optional {
			shape += "?"
		}
		if f.Oneof != "" {
			shape += "{" + f.Oneof + "}"
		}
		out = append(out, f.Name+"="+shape)
	}
	return out
}

// schemaPanics runs build, which must panic with a message holding want.
func schemaPanics(t *testing.T, want string, build func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("the invalid table was accepted; want a panic naming %q", want)
		}
		message, ok := r.(string)
		if !ok || !strings.Contains(message, want) ||
			!strings.HasPrefix(message, "proto: invalid logical schema table: ") {
			t.Fatalf("panic %v does not name %q", r, want)
		}
	}()
	build()
}

// The table of schema_version 2 is valid: it builds without a refusal. This
// is the first test that touches the registry, so a broken table fails here
// by name.
func TestSchemaV2TableIsValid(t *testing.T) {
	s := buildSchemaV2()
	if s == nil {
		t.Fatal("the registry of schema_version 2 is nil")
	}
	if LogicalSchemaV2() != LogicalSchemaV2() {
		t.Fatal("LogicalSchemaV2 returns a new registry on every call")
	}
	if SchemaVersion != 2 {
		t.Fatalf("schema version %d, want 2", SchemaVersion)
	}
}

// The type keys are wire numbers: each message keeps the key registered for
// it, written here as literals.
func TestSchemaTypeKeysArePinned(t *testing.T) {
	want := map[string]uint32{
		"Envelope": 1, "NetworkIdentity": 2, "ProfileRef": 3, "SourceRef": 4,
		"BlockApplied": 10, "BlockActivationRef": 11, "ActivationID": 12, "ObservationRef": 13,
		"BlockMetadata": 14, "Timestamp": 15, "BlockHeader": 16, "FamilyDetails": 17,
		"EthBlockDetails": 18, "BscBlockDetails": 19, "ParliaVoteSnapshot": 20, "BorBlockDetails": 21,
		"HeimdallCheckpointRef": 22, "LifecycleSnapshot": 23, "NativeDecisionRef": 24, "AnchorRef": 25,
		"FinalityMarkers": 26, "EthereumMarkers": 27, "BscMarkers": 28, "AttestedSnapshot": 29,
		"PolygonMarkers": 30, "ReferenceObservation": 31,
	}
	if TypeEnvelope != 1 || TypeSourceRef != 4 || TypeBlockApplied != 10 || TypeReferenceObservation != 31 {
		t.Fatalf("type key constants moved: %d %d %d %d", TypeEnvelope, TypeSourceRef, TypeBlockApplied,
			TypeReferenceObservation)
	}
	s := LogicalSchemaV2()
	got := make(map[string]uint32, len(want))
	for _, key := range schemaKeys(t, s) {
		m, _ := s.MessageType(key)
		got[m.Name] = key
	}
	if len(got) != len(want) {
		t.Fatalf("the registry names %d messages, want %d", len(got), len(want))
	}
	for name, key := range want {
		if got[name] != key {
			t.Fatalf("message %s has type key %d, want %d", name, got[name], key)
		}
	}
}

// Every type key and field tag of the registry is positive, and no key,
// tag, message name or field name is registered twice.
func TestSchemaKeysAndTagsArePositiveAndUnique(t *testing.T) {
	s := LogicalSchemaV2()
	keys := schemaKeys(t, s)
	if len(keys) == 0 {
		t.Fatal("the registry is empty")
	}
	if !slices.IsSorted(keys) || len(slices.Compact(slices.Clone(keys))) != len(keys) {
		t.Fatalf("type keys are not sorted and unique: %v", keys)
	}
	names := make(map[string]uint32)
	for _, key := range keys {
		m, ok := s.MessageType(key)
		if !ok || m.Key != key || m.Name == "" {
			t.Fatalf("type key %d: message %v ok %v", key, m, ok)
		}
		if other, dup := names[m.Name]; dup {
			t.Fatalf("message name %s is registered for type keys %d and %d", m.Name, other, key)
		}
		names[m.Name] = key
		if len(m.Fields) == 0 {
			t.Fatalf("message %s has no fields", m.Name)
		}
		tags := make([]uint32, 0, len(m.Fields))
		fieldNames := make(map[string]bool, len(m.Fields))
		for _, f := range m.Fields {
			tags = append(tags, f.Tag)
			if fieldNames[f.Name] {
				t.Fatalf("message %s registers field name %s twice", m.Name, f.Name)
			}
			fieldNames[f.Name] = true
		}
		if !slices.IsSorted(tags) || tags[0] == 0 || len(slices.Compact(slices.Clone(tags))) != len(tags) {
			t.Fatalf("message %s tags are not positive, ascending and unique: %v", m.Name, tags)
		}
		for _, tag := range tags {
			f, ok := s.Field(key, tag)
			if !ok || f.Tag != tag || f.Name == "" {
				t.Fatalf("message %s tag %d: field %v ok %v", m.Name, tag, f, ok)
			}
		}
		// Tag 0 is never a field.
		if _, ok := s.Field(key, 0); ok {
			t.Fatalf("message %s registers tag 0", m.Name)
		}
	}
}

// The registry holds exactly the path of the payload variant block_applied:
// every message it reaches is registered, and nothing else is.
func TestSchemaRegistersExactlyTheBlockAppliedPath(t *testing.T) {
	s := LogicalSchemaV2()
	payload, ok := s.FieldOf(TypeEnvelope, PayloadBlockApplied)
	if !ok || payload.Kind != KindMessage {
		t.Fatalf("the Envelope has no message payload variant %s: %+v ok %v", PayloadBlockApplied, payload, ok)
	}
	if payload.Tag != PayloadTagBlockApplied || payload.Oneof != "payload" {
		t.Fatalf("payload variant %+v", payload)
	}
	variant := schemaReachable(t, s, payload.Type)
	if !variant[TypeBlockApplied] || !variant[TypeBlockHeader] || !variant[TypeBscMarkers] ||
		!variant[TypeAttestedSnapshot] || !variant[TypeReferenceObservation] || !variant[TypeAnchorRef] {
		t.Fatal("the block record does not reach its header or finality markers")
	}
	envelope := schemaReachable(t, s, TypeEnvelope)
	for key := range variant {
		if !envelope[key] {
			t.Fatalf("type key %d is reachable from block_applied but not from the Envelope", key)
		}
	}
	registered := make(map[uint32]bool)
	for _, key := range schemaKeys(t, s) {
		registered[key] = true
	}
	if len(registered) != len(envelope) {
		t.Fatalf("the registry holds %d messages, the Envelope path reaches %d", len(registered), len(envelope))
	}
	for key := range envelope {
		if !registered[key] {
			t.Fatalf("type key %d is reachable but not registered", key)
		}
	}
	// The family variants the typed model reaches are part of the path.
	for _, key := range []uint32{TypeEthBlockDetails, TypeBorBlockDetails, TypeHeimdallCheckpointRef,
		TypeEthereumMarkers, TypePolygonMarkers, TypeParliaVoteSnapshot} {
		if !variant[key] {
			t.Fatalf("type key %d of the block record is not registered", key)
		}
	}
	// The demo key of the reference tool is not reused.
	if registered[9001] {
		t.Fatal("type key 9001 is registered")
	}
}

// The registry, message by message: every type key, field name, field tag,
// kind, type reference, enum name, byte length and presence rule. A change
// to any of them is a versioned change of the registry.
func TestSchemaTable(t *testing.T) {
	s := LogicalSchemaV2()
	want := map[uint32]string{
		TypeEnvelope: `schema_version=uint32 network=message(2) profile_ref=message(3) event_id=bytes40
			producer_id=bytes16 run_id=bytes16 source=message(4) capture_mode=enum(CaptureMode)
			block_applied=message(10){payload}`,
		TypeNetworkIdentity: `family=enum(NetworkFamily) chain_id=bytes32 genesis_hash=bytes32`,
		TypeProfileRef: `network_profile=utf8 client_commit=utf8 adapter_version=utf8
			fork_rules_version=utf8 normalization_version=utf8`,
		TypeSourceRef: `node_instance_id=bytes16 writer_epoch=bytes16 source_sequence=uint64`,

		TypeBlockApplied: `activation=message(11) admission=message(13) metadata=message(14)
			initial_snapshot=message(23)`,
		TypeBlockActivationRef: `network=message(2) block_hash=bytes32 producer_id=bytes16 run_id=bytes16
			activation_id=message(12)`,
		TypeActivationID:   `decision_sequence=uint64 entity_ordinal=uint64`,
		TypeObservationRef: `producer_id=bytes16 run_id=bytes16 observation_sequence=uint64`,
		TypeBlockMetadata: `number=uint64 hash=bytes32 parent_hash=bytes32 timestamp=message(15)
			source_transaction_count=uint64 header=message(16) details=message(17)`,
		TypeTimestamp: `seconds=uint64 nanos=uint32 precision=enum(TimestampPrecision)`,
		TypeBlockHeader: `parent_hash=bytes32 uncle_hash=bytes32 beneficiary=bytes20 state_root=bytes32
			transactions_root=bytes32 receipts_root=bytes32 logs_bloom=bytes256 difficulty=bytes32
			number=uint64 gas_limit=uint64 gas_used=uint64 timestamp_seconds=uint64 extra_data=bytes
			mix_digest=bytes32 nonce=bytes8 base_fee=bytes32? withdrawals_root=bytes32?
			blob_gas_used=uint64? excess_blob_gas=uint64? parent_beacon_block_root=bytes32?
			requests_hash=bytes32? source_rlp=bytes`,
		TypeFamilyDetails:      `eth=message(18){family} bsc=message(19){family} bor=message(21){family}`,
		TypeEthBlockDetails:    `block_access_list_hash=bytes32? slot_number=uint64?`,
		TypeBscBlockDetails:    `parlia_system_transaction_count=uint64? vote_snapshot=message(20)?`,
		TypeParliaVoteSnapshot: `justified=message(25) finalized=message(25)`,
		TypeBorBlockDetails: `sprint_length=uint64? sprint_end=bool? side_chain_block=bool?
			state_sync_count=uint64? state_sync_mode=enum(StateSyncMode)?
			heimdall_checkpoints=list(22)`,
		TypeHeimdallCheckpointRef: `kind=enum(CheckpointKind) end=message(25) checkpoint_id=uint64?`,
		TypeLifecycleSnapshot: `native_decision=message(24) observed_execution_head=message(25)
			markers=message(26)`,
		TypeNativeDecisionRef: `node_instance_id=bytes16 producer_id=bytes16 run_id=bytes16
			decision_sequence=uint64`,
		TypeAnchorRef:        `height=uint64 hash=bytes32`,
		TypeFinalityMarkers:  `eth=message(27){family} bsc=message(28){family} polygon=message(30){family}`,
		TypeEthereumMarkers:  `safe=message(31) finalized=message(31) safe_basis=enum(EthereumSafeBasis)`,
		TypeBscMarkers:       `attested=message(29)? effective_finalized=message(31) effective_basis=enum(BscEffectiveBasis)`,
		TypeAttestedSnapshot: `snapshot_head=message(25) justified=message(25) finalized=message(25)`,
		TypePolygonMarkers:   `milestone=message(31) checkpoint=message(31) native_finalized=message(31) l1_checkpoint=message(31)`,
		TypeReferenceObservation: `kind=enum(ReferenceKind) anchor=message(25)? origin=enum(EvidenceOrigin)?
			reason=enum(UnavailableReason)?`,
	}
	names := map[uint32]string{
		TypeEnvelope: "Envelope", TypeNetworkIdentity: "NetworkIdentity", TypeProfileRef: "ProfileRef",
		TypeSourceRef: "SourceRef", TypeBlockApplied: "BlockApplied",
		TypeBlockActivationRef: "BlockActivationRef", TypeActivationID: "ActivationID",
		TypeObservationRef: "ObservationRef", TypeBlockMetadata: "BlockMetadata", TypeTimestamp: "Timestamp",
		TypeBlockHeader: "BlockHeader", TypeFamilyDetails: "FamilyDetails",
		TypeEthBlockDetails: "EthBlockDetails", TypeBscBlockDetails: "BscBlockDetails",
		TypeParliaVoteSnapshot: "ParliaVoteSnapshot", TypeBorBlockDetails: "BorBlockDetails",
		TypeHeimdallCheckpointRef: "HeimdallCheckpointRef", TypeLifecycleSnapshot: "LifecycleSnapshot",
		TypeNativeDecisionRef: "NativeDecisionRef", TypeAnchorRef: "AnchorRef",
		TypeFinalityMarkers: "FinalityMarkers", TypeEthereumMarkers: "EthereumMarkers",
		TypeBscMarkers: "BscMarkers", TypeAttestedSnapshot: "AttestedSnapshot",
		TypePolygonMarkers: "PolygonMarkers", TypeReferenceObservation: "ReferenceObservation",
	}
	keys := schemaKeys(t, s)
	if len(keys) != len(want) || len(names) != len(want) {
		t.Fatalf("the registry holds %d messages, the expected table %d with %d names", len(keys), len(want),
			len(names))
	}
	for key, table := range want {
		m, ok := s.MessageType(key)
		if !ok {
			t.Fatalf("type key %d is not registered", key)
		}
		if m.Name != names[key] {
			t.Fatalf("type key %d is named %s, want %s", key, m.Name, names[key])
		}
		expected := strings.Fields(table)
		if got := schemaFieldList(t, s, key); !slices.Equal(got, expected) {
			t.Fatalf("%s fields:\n got %v\nwant %v", m.Name, got, expected)
		}
		for i, f := range m.Fields {
			if key != TypeEnvelope && f.Tag != uint32(i+1) {
				t.Fatalf("%s field %s has tag %d, want %d", m.Name, f.Name, f.Tag, i+1)
			}
		}
	}
}

// An unknown type key, field tag, field name, enum value or network is an
// explicit miss, never a passthrough.
func TestSchemaUnknownValuesAreMisses(t *testing.T) {
	s := LogicalSchemaV2()
	for _, key := range []uint32{0, 5, 9, 32, 69, 70, 9001, ^uint32(0)} {
		if _, ok := s.MessageType(key); ok {
			t.Fatalf("MessageType answers unregistered key %d", key)
		}
		if _, ok := s.Field(key, 1); ok {
			t.Fatalf("Field answers unregistered type key %d", key)
		}
		if _, ok := s.FieldOf(key, "kind"); ok {
			t.Fatalf("FieldOf answers unregistered type key %d", key)
		}
	}
	// Fields 1 to 8 and the payload variant 104 are the Envelope; 9 and the
	// other payload variants are not registered.
	for _, tag := range []uint32{0, 9, 10, 99, 100, 101, 103, 105, 106, 107, 112, 113, ^uint32(0)} {
		if _, ok := s.Field(TypeEnvelope, tag); ok {
			t.Fatalf("the Envelope registers unregistered tag %d", tag)
		}
	}
	for _, tag := range []uint32{1, 2, 3, 4, 5, 6, 7, 8, PayloadTagBlockApplied} {
		if _, ok := s.Field(TypeEnvelope, tag); !ok {
			t.Fatalf("the Envelope does not register tag %d", tag)
		}
	}
	// Payload variants that are not registered, and a field of another
	// message, are unknown.
	for _, name := range []string{"", "block_removed", "block_status_changed", "transaction_applied", "hash",
		"BLOCK_APPLIED", "block_applied "} {
		if _, ok := s.FieldOf(TypeEnvelope, name); ok {
			t.Fatalf("the Envelope registers unregistered field %q", name)
		}
	}
	if _, ok := s.FieldOf(TypeBlockHeader, "number"); !ok {
		t.Fatal("BlockHeader has no field number")
	}
	if _, ok := s.FieldOf(TypeBlockHeader, "hash"); ok {
		t.Fatal("BlockHeader answers a field of another message")
	}
	for _, tc := range []struct {
		enum string
		want uint32
	}{
		{"CaptureMode", 0}, {"CaptureMode", 2}, {"CaptureMode", 3}, {"CaptureMode", ^uint32(0)},
		{"NetworkFamily", 0}, {"NetworkFamily", 2},
		{"TimestampPrecision", 0}, {"TimestampPrecision", 4},
		{"StateSyncMode", 0}, {"StateSyncMode", 3},
		{"CheckpointKind", 0}, {"CheckpointKind", 3},
		{"EvidenceOrigin", 0}, {"EvidenceOrigin", 4},
		{"UnavailableReason", 0}, {"UnavailableReason", 5},
		{"ReferenceKind", 0}, {"ReferenceKind", 5},
		{"EthereumSafeBasis", 0}, {"EthereumSafeBasis", 4},
		{"BscEffectiveBasis", 0}, {"BscEffectiveBasis", 5},
		{"NoSuchEnum", 0}, {"NoSuchEnum", 1}, {"", 1}, {"capturemode", 1},
	} {
		if name, ok := s.EnumValue(tc.enum, tc.want); ok || name != "" {
			t.Fatalf("EnumValue(%q, %d) answers %q, %v", tc.enum, tc.want, name, ok)
		}
	}
	// Only the BSC testnet network has a profile.
	if _, ok := s.ProfileForNetwork([32]byte{}); ok {
		t.Fatal("a zero chain id has a profile")
	}
	if _, ok := s.ProfileForNetwork(chainIDBytes(56)); ok {
		t.Fatal("chain id 56 has a registered profile")
	}
}

// A oneof is exactly one child among its variant tags: the variants share
// the group name, none of them is optional, and no other field of the
// message carries the group.
func TestSchemaOneofGroups(t *testing.T) {
	s := LogicalSchemaV2()
	for _, tc := range []struct {
		typeKey  uint32
		group    string
		variants []string
		tags     []uint32
	}{
		{TypeEnvelope, "payload", []string{"block_applied"}, []uint32{PayloadTagBlockApplied}},
		{TypeFamilyDetails, "family", []string{"eth", "bsc", "bor"}, []uint32{1, 2, 3}},
		{TypeFinalityMarkers, "family", []string{"eth", "bsc", "polygon"}, []uint32{1, 2, 3}},
	} {
		m, ok := s.MessageType(tc.typeKey)
		if !ok {
			t.Fatalf("type key %d is not registered", tc.typeKey)
		}
		var names []string
		for _, f := range m.Fields {
			if f.Oneof == tc.group {
				if f.Optional {
					t.Fatalf("%s variant %s is optional", m.Name, f.Name)
				}
				if f.Kind != KindMessage || f.Type == 0 {
					t.Fatalf("%s variant %s is not a message", m.Name, f.Name)
				}
				names = append(names, f.Name)
			} else if f.Oneof != "" {
				t.Fatalf("%s field %s carries unknown oneof group %q", m.Name, f.Name, f.Oneof)
			}
		}
		if !slices.Equal(names, tc.variants) {
			t.Fatalf("oneof %s of %s: variants %v, want %v", tc.group, m.Name, names, tc.variants)
		}
		var tags []uint32
		for _, name := range tc.variants {
			f, ok := s.FieldOf(tc.typeKey, name)
			if !ok {
				t.Fatalf("%s has no field %s", m.Name, name)
			}
			tags = append(tags, f.Tag)
		}
		if !slices.Equal(tags, tc.tags) {
			t.Fatalf("oneof %s of %s: tags %v, want %v", tc.group, m.Name, tags, tc.tags)
		}
	}
	// No other message carries a oneof group.
	for _, key := range schemaKeys(t, s) {
		if key == TypeEnvelope || key == TypeFamilyDetails || key == TypeFinalityMarkers {
			continue
		}
		m, _ := s.MessageType(key)
		for _, f := range m.Fields {
			if f.Oneof != "" {
				t.Fatalf("%s field %s carries oneof group %q", m.Name, f.Name, f.Oneof)
			}
		}
	}
}

// The tag of a payload variant is the base plus the registry v1 variant
// number.
func TestSchemaPayloadTagFollowsTheVariantRegistry(t *testing.T) {
	if PayloadTagBase != 100 {
		t.Fatalf("payload tag base %d, want 100", PayloadTagBase)
	}
	if PayloadTagBlockApplied != PayloadTagBase+uint32(profiles.VariantBlockApplied) {
		t.Fatalf("block_applied tag %d, want %d", PayloadTagBlockApplied,
			PayloadTagBase+uint32(profiles.VariantBlockApplied))
	}
	f, ok := LogicalSchemaV2().Field(TypeEnvelope, PayloadTagBlockApplied)
	if !ok || f.Name != PayloadBlockApplied || f.Type != TypeBlockApplied {
		t.Fatalf("tag %d: %+v ok %v", PayloadTagBlockApplied, f, ok)
	}
	if _, ok := LogicalSchemaV2().Field(TypeEnvelope, PayloadTagBase+uint32(profiles.VariantBlockRemoved)); ok {
		t.Fatal("the block_removed variant is registered")
	}
}

// Every enum value of the typed model of protocol/block is registered under
// its contract name, and zero is registered nowhere.
func TestSchemaEnumValuesFollowTheBlockModel(t *testing.T) {
	s := LogicalSchemaV2()
	for _, tc := range []struct {
		enum  string
		value uint32
		name  string
	}{
		{"CaptureMode", CaptureModeLive, "LIVE"},
		{"NetworkFamily", uint32(NetworkFamilyEVM), "EVM"},
		{"TimestampPrecision", uint32(block.PrecisionSeconds), "SECONDS"},
		{"TimestampPrecision", uint32(block.PrecisionMilliseconds), "MILLISECONDS"},
		{"TimestampPrecision", uint32(block.PrecisionNanoseconds), "NANOSECONDS"},
		{"StateSyncMode", uint32(block.StateSyncEndOfBlockCommit), "END_OF_BLOCK_COMMIT"},
		{"StateSyncMode", uint32(block.StateSyncBodyTransaction), "BODY_TRANSACTION"},
		{"CheckpointKind", uint32(block.CheckpointMilestone), "MILESTONE"},
		{"CheckpointKind", uint32(block.CheckpointArea), "CHECKPOINT"},
		{"EvidenceOrigin", uint32(block.OriginNativeLive), "NATIVE_LIVE"},
		{"EvidenceOrigin", uint32(block.OriginNativeRestart), "NATIVE_RESTART"},
		{"EvidenceOrigin", uint32(block.OriginRetainedDurable), "RETAINED_DURABLE"},
		{"UnavailableReason", uint32(block.UnavailableSourceOffline), "SOURCE_OFFLINE"},
		{"UnavailableReason", uint32(block.UnavailableSourceSyncing), "SOURCE_SYNCING"},
		{"UnavailableReason", uint32(block.UnavailableStateUnavailable), "STATE_UNAVAILABLE"},
		{"UnavailableReason", uint32(block.UnavailableCheckpointPending), "CHECKPOINT_PENDING"},
		{"ReferenceKind", uint32(block.ReferenceKnown), "KNOWN"},
		{"ReferenceKind", uint32(block.ReferenceNotObserved), "NOT_OBSERVED"},
		{"ReferenceKind", uint32(block.ReferenceNativeUnset), "NATIVE_REFERENCE_UNSET"},
		{"ReferenceKind", uint32(block.ReferenceUnavailable), "UNAVAILABLE"},
		{"EthereumSafeBasis", uint32(block.SafeBasisConsensusSetter), "CONSENSUS_SETTER"},
		{"EthereumSafeBasis", uint32(block.SafeBasisStartupFinalizedFallback), "STARTUP_FINALIZED_FALLBACK"},
		{"EthereumSafeBasis", uint32(block.SafeBasisUnset), "UNSET"},
		{"BscEffectiveBasis", uint32(block.BscBasisHeaderSnapshot), "HEADER_SNAPSHOT"},
		{"BscEffectiveBasis", uint32(block.BscBasisVotePoolQuorum), "VOTE_POOL_QUORUM"},
		{"BscEffectiveBasis", uint32(block.BscBasisGenesisFallback), "GENESIS_FALLBACK"},
		{"BscEffectiveBasis", uint32(block.BscBasisUnset), "UNSET"},
	} {
		name, ok := s.EnumValue(tc.enum, tc.value)
		if !ok || name != tc.name {
			t.Fatalf("EnumValue(%q, %d) = %q, %v; want %q", tc.enum, tc.value, name, ok, tc.name)
		}
	}
	// The registry holds exactly the ten enums, each named by a field and
	// each holding exactly the values above.
	want := map[string][]uint32{
		"CaptureMode": {1}, "NetworkFamily": {1}, "TimestampPrecision": {1, 2, 3},
		"StateSyncMode": {1, 2}, "CheckpointKind": {1, 2}, "EvidenceOrigin": {1, 2, 3},
		"UnavailableReason": {1, 2, 3, 4}, "ReferenceKind": {1, 2, 3, 4},
		"EthereumSafeBasis": {1, 2, 3}, "BscEffectiveBasis": {1, 2, 3, 4},
	}
	named := make(map[string]bool)
	values := make(map[string][]uint32)
	for _, key := range schemaKeys(t, s) {
		m, _ := s.MessageType(key)
		for _, f := range m.Fields {
			if f.Kind != KindEnum {
				continue
			}
			named[f.Enum] = true
			for value := uint32(1); value <= 8; value++ {
				if _, ok := s.EnumValue(f.Enum, value); ok && !slices.Contains(values[f.Enum], value) {
					values[f.Enum] = append(values[f.Enum], value)
				}
			}
		}
	}
	if len(named) != len(want) {
		t.Fatalf("the registry holds %d enums, want %d: %v", len(named), len(want), named)
	}
	for enum, expected := range want {
		if !named[enum] {
			t.Fatalf("enum %s is registered but no field names it", enum)
		}
		if !slices.Equal(values[enum], expected) {
			t.Fatalf("enum %s holds values %v, want %v", enum, values[enum], expected)
		}
	}
}

// The shape of every field agrees with its kind.
func TestSchemaFieldShapes(t *testing.T) {
	s := LogicalSchemaV2()
	for _, key := range schemaKeys(t, s) {
		m, _ := s.MessageType(key)
		for _, f := range m.Fields {
			switch f.Kind {
			case KindMessage, KindList:
				if f.Type == 0 {
					t.Fatalf("%s field %s has kind %s without a type key", m.Name, f.Name, f.Kind)
				}
				if _, ok := s.MessageType(f.Type); !ok {
					t.Fatalf("%s field %s names unregistered type key %d", m.Name, f.Name, f.Type)
				}
			default:
				if f.Type != 0 {
					t.Fatalf("%s field %s has kind %s with type key %d", m.Name, f.Name, f.Kind, f.Type)
				}
			}
			if f.Kind == KindEnum {
				if f.Enum == "" {
					t.Fatalf("%s enum field %s names no enum", m.Name, f.Name)
				}
			} else if f.Enum != "" {
				t.Fatalf("%s field %s has kind %s with enum %s", m.Name, f.Name, f.Kind, f.Enum)
			}
			if f.Kind != KindBytes && f.ByteLen != 0 {
				t.Fatalf("%s field %s has kind %s with byte length %d", m.Name, f.Name, f.Kind, f.ByteLen)
			}
		}
	}
}

// The profile of the BSC testnet is registered with its five strings.
func TestSchemaProfileForNetwork(t *testing.T) {
	s := LogicalSchemaV2()
	chainID := chainIDBytes(97)
	if chainID != [32]byte{31: 0x61} {
		t.Fatalf("chain id 97 is %x", chainID)
	}
	ref, ok := s.ProfileForNetwork(chainID)
	if !ok {
		t.Fatal("the BSC testnet profile is not registered")
	}
	want := ProfileRef{NetworkProfile: "bsc-testnet",
		ClientCommit:         "54afaff4da31ac9e49ba646a4f65104edac5a2f3",
		AdapterVersion:       "bsc-adapter-v1",
		ForkRulesVersion:     "bsc-fork-rules-v1",
		NormalizationVersion: "bsc-normalization-v1"}
	if ref != want {
		t.Fatalf("profile %+v, want %+v", ref, want)
	}
	commit, err := hex.DecodeString(ref.ClientCommit)
	if err != nil || len(commit) != 20 || ref.ClientCommit != strings.ToLower(ref.ClientCommit) {
		t.Fatalf("client commit %q is not 40 lowercase hex digits", ref.ClientCommit)
	}
	// A chain id that differs in one byte has no profile.
	other := chainID
	other[0] = 1
	if _, ok := s.ProfileForNetwork(other); ok {
		t.Fatal("a chain id with a set leading byte has the BSC testnet profile")
	}
	other = chainID
	other[31] = 0x62
	if _, ok := s.ProfileForNetwork(other); ok {
		t.Fatal("chain id 98 has the BSC testnet profile")
	}
}

// A chain id is 32 big-endian bytes, also when it needs all eight bytes.
func TestSchemaChainIDBytes(t *testing.T) {
	if got, want := chainIDBytes(97), [32]byte{31: 0x61}; got != want {
		t.Fatalf("chain id 97 is %x, want %x", got, want)
	}
	var want [32]byte
	for i := 0; i < 8; i++ {
		want[24+i] = byte(i + 1)
	}
	if got := chainIDBytes(0x0102030405060708); got != want {
		t.Fatalf("chain id 0x0102030405060708 is %x, want %x", got, want)
	}
	if got := chainIDBytes(0); got != [32]byte{} {
		t.Fatalf("chain id 0 is %x", got)
	}
	// A second profile is found by its own chain id.
	table := newSchemaTable()
	table.profile(0x0102030405060708, ProfileRef{NetworkProfile: "wide", ClientCommit: "c",
		AdapterVersion: "a", ForkRulesVersion: "f", NormalizationVersion: "z"})
	table.profile(1, ProfileRef{NetworkProfile: "one", ClientCommit: "c", AdapterVersion: "a",
		ForkRulesVersion: "f", NormalizationVersion: "z"})
	if ref, ok := table.schema.ProfileForNetwork(want); !ok || ref.NetworkProfile != "wide" {
		t.Fatalf("wide chain id profile %+v ok %v", ref, ok)
	}
	one := [32]byte{31: 1}
	if ref, ok := table.schema.ProfileForNetwork(one); !ok || ref.NetworkProfile != "one" {
		t.Fatalf("chain id 1 profile %+v ok %v", ref, ok)
	}
	if _, ok := table.schema.ProfileForNetwork([32]byte{}); ok {
		t.Fatal("chain id 0 has a profile")
	}
}

// A lookup returns copies: changing a returned field or field list cannot
// change the registry.
func TestSchemaLookupsReturnCopies(t *testing.T) {
	s := LogicalSchemaV2()
	m, _ := s.MessageType(TypeAnchorRef)
	m.Name = "changed"
	m.Fields[0].Name = "changed"
	m.Fields[0].Tag = 99
	again, _ := s.MessageType(TypeAnchorRef)
	if again.Name != "AnchorRef" || again.Fields[0].Name != "height" || again.Fields[0].Tag != 1 {
		t.Fatalf("the registry changed through a returned value: %+v", again)
	}
	f, _ := s.Field(TypeAnchorRef, 1)
	f.Name = "changed"
	f.Kind = KindUTF8
	if again.Fields[0].Name != "height" {
		t.Fatal("Field returned a reference into the registry")
	}
	named, ok := s.FieldOf(TypeAnchorRef, "height")
	if !ok || named.Name != "height" || named.Kind != KindUint64 || named.Tag != 1 {
		t.Fatalf("FieldOf: %+v ok %v", named, ok)
	}
}

func TestSchemaFieldKindNames(t *testing.T) {
	for kind, want := range map[FieldKind]string{
		KindMessage: "message", KindList: "list", KindBool: "bool", KindUint32: "uint32",
		KindUint64: "uint64", KindEnum: "enum", KindBytes: "bytes", KindUTF8: "utf8",
	} {
		if !kind.Valid() || kind.String() != want {
			t.Fatalf("kind %d: valid %v name %q, want %q", uint8(kind), kind.Valid(), kind.String(), want)
		}
	}
	for _, kind := range []FieldKind{0, 9, 255} {
		if kind.Valid() {
			t.Fatalf("kind %d is valid", uint8(kind))
		}
		if got, want := kind.String(), fmt.Sprintf("FieldKind(%d)", uint8(kind)); got != want {
			t.Fatalf("kind %d name %q, want %q", uint8(kind), got, want)
		}
	}
}

// The refusal of an invalid table: each check of the builder turns one
// broken table into a panic that names the cause, and a valid table of
// every field shape is accepted.
func TestSchemaTableRefusesInvalidEntries(t *testing.T) {
	// constructors only builds field values; every case registers into its
	// own table.
	constructors := newSchemaTable()
	// valid registers one enum and one message, so a finish check has a
	// table to resolve against.
	valid := func(table *schemaTable) {
		table.enum("E", enumEntry{1, "ONE"})
		table.message(1, "One", constructors.uint64(1, "value"))
	}
	complete := ProfileRef{NetworkProfile: "n", ClientCommit: "c", AdapterVersion: "a",
		ForkRulesVersion: "f", NormalizationVersion: "z"}

	for _, empty := range []string{"NetworkProfile", "ClientCommit", "AdapterVersion", "ForkRulesVersion",
		"NormalizationVersion"} {
		tc := struct {
			empty string
		}{empty}
		t.Run("profile without "+tc.empty, func(t *testing.T) {
			ref := complete
			switch tc.empty {
			case "NetworkProfile":
				ref.NetworkProfile = ""
			case "ClientCommit":
				ref.ClientCommit = ""
			case "AdapterVersion":
				ref.AdapterVersion = ""
			case "ForkRulesVersion":
				ref.ForkRulesVersion = ""
			case "NormalizationVersion":
				ref.NormalizationVersion = ""
			}
			tt := newSchemaTable()
			schemaPanics(t, "profile of chain id 7 has an empty string", func() { tt.profile(7, ref) })
		})
	}
	t.Run("profile with chain id zero", func(t *testing.T) {
		tt := newSchemaTable()
		schemaPanics(t, "profile n has chain id 0", func() { tt.profile(0, complete) })
	})
	t.Run("profile registered twice", func(t *testing.T) {
		tt := newSchemaTable()
		tt.profile(7, complete)
		schemaPanics(t, "profile of chain id 7 is registered twice", func() { tt.profile(7, complete) })
	})
	t.Run("enum without a name", func(t *testing.T) {
		schemaPanics(t, "enum without a name", func() { newSchemaTable().enum("", enumEntry{1, "ONE"}) })
	})
	t.Run("enum without values", func(t *testing.T) {
		schemaPanics(t, "enum E without values", func() { newSchemaTable().enum("E") })
	})
	t.Run("enum registered twice", func(t *testing.T) {
		tt := newSchemaTable()
		tt.enum("E", enumEntry{1, "ONE"})
		schemaPanics(t, "enum E is registered twice", func() { tt.enum("E", enumEntry{2, "TWO"}) })
	})
	t.Run("enum value zero", func(t *testing.T) {
		schemaPanics(t, "enum E has value 0 (ZERO)", func() {
			newSchemaTable().enum("E", enumEntry{1, "ONE"}, enumEntry{0, "ZERO"})
		})
	})
	t.Run("enum value without a name", func(t *testing.T) {
		schemaPanics(t, "enum E value 2 has no name", func() {
			newSchemaTable().enum("E", enumEntry{1, "ONE"}, enumEntry{2, ""})
		})
	})
	t.Run("enum name registered twice", func(t *testing.T) {
		schemaPanics(t, "enum E name ONE is registered for values 1 and 2", func() {
			newSchemaTable().enum("E", enumEntry{1, "ONE"}, enumEntry{2, "ONE"})
		})
	})
	t.Run("enum value registered twice", func(t *testing.T) {
		schemaPanics(t, "enum E value 1 is registered twice", func() {
			newSchemaTable().enum("E", enumEntry{1, "ONE"}, enumEntry{1, "UNO"})
		})
	})
	t.Run("message with type key zero", func(t *testing.T) {
		schemaPanics(t, "message One has type key 0", func() {
			newSchemaTable().message(0, "One", constructors.uint64(1, "value"))
		})
	})
	t.Run("message without a name", func(t *testing.T) {
		schemaPanics(t, "message 1 has no name", func() {
			newSchemaTable().message(1, "", constructors.uint64(1, "value"))
		})
	})
	t.Run("message without fields", func(t *testing.T) {
		schemaPanics(t, "message One has no fields", func() { newSchemaTable().message(1, "One") })
	})
	t.Run("message name registered twice", func(t *testing.T) {
		tt := newSchemaTable()
		tt.message(1, "One", constructors.uint64(1, "value"))
		schemaPanics(t, "message name One is registered for type keys 1 and 2", func() {
			tt.message(2, "One", constructors.uint64(1, "value"))
		})
	})
	t.Run("type key registered twice", func(t *testing.T) {
		tt := newSchemaTable()
		tt.message(1, "One", constructors.uint64(1, "value"))
		schemaPanics(t, "type key 1 is registered twice", func() {
			tt.message(1, "Two", constructors.uint64(1, "value"))
		})
	})
	t.Run("field with tag zero", func(t *testing.T) {
		schemaPanics(t, "message One field value has tag 0", func() {
			newSchemaTable().message(1, "One", constructors.uint64(0, "value"))
		})
	})
	t.Run("field without a name", func(t *testing.T) {
		schemaPanics(t, "message One has an unnamed field with tag 1", func() {
			newSchemaTable().message(1, "One", constructors.uint64(1, ""))
		})
	})
	for _, kind := range []FieldKind{0, 9, 255} {
		t.Run(fmt.Sprintf("field with kind %d", uint8(kind)), func(t *testing.T) {
			f := FieldType{Tag: 1, Name: "value", Kind: kind}
			schemaPanics(t, fmt.Sprintf("message One field value has kind %d", uint8(kind)), func() {
				newSchemaTable().message(1, "One", f)
			})
		})
	}
	t.Run("field tag registered twice", func(t *testing.T) {
		schemaPanics(t, "message One tag 1 is registered twice", func() {
			newSchemaTable().message(1, "One", constructors.uint64(1, "value"), constructors.uint64(1, "other"))
		})
	})
	t.Run("field name registered twice", func(t *testing.T) {
		schemaPanics(t, "message One name value is registered for tags 1 and 2", func() {
			newSchemaTable().message(1, "One", constructors.uint64(1, "value"), constructors.uint64(2, "value"))
		})
	})
	t.Run("oneof variant flagged optional", func(t *testing.T) {
		tt := newSchemaTable()
		schemaPanics(t, "message One field choice is a oneof variant and optional", func() {
			tt.message(1, "One", tt.variant(tt.optional(tt.uint64(1, "choice")), "group"))
		})
	})
	t.Run("message field names an unregistered type key", func(t *testing.T) {
		tt := newSchemaTable()
		valid(tt)
		tt.message(2, "Two", tt.msg(1, "one", 1), tt.msg(2, "missing", 3))
		schemaPanics(t, "message Two field missing refers to unregistered type key 3", tt.finish)
	})
	t.Run("list names an unregistered element type key", func(t *testing.T) {
		tt := newSchemaTable()
		valid(tt)
		tt.message(2, "Two", tt.list(1, "items", 9))
		schemaPanics(t, "message Two field items refers to unregistered type key 9", tt.finish)
	})
	t.Run("scalar field carries a type key", func(t *testing.T) {
		tt := newSchemaTable()
		valid(tt)
		tt.message(2, "Two", FieldType{Tag: 1, Name: "value", Kind: KindUint64, Type: 1})
		schemaPanics(t, "message Two field value has kind uint64 and type key 1", tt.finish)
	})
	t.Run("enum field names an unregistered enum", func(t *testing.T) {
		tt := newSchemaTable()
		valid(tt)
		tt.message(2, "Two", tt.enumField(1, "value", "Missing"))
		schemaPanics(t, "message Two field value refers to unregistered enum Missing", tt.finish)
	})
	t.Run("field of another kind carries an enum", func(t *testing.T) {
		tt := newSchemaTable()
		valid(tt)
		tt.message(2, "Two", FieldType{Tag: 1, Name: "value", Kind: KindUint64, Enum: "E"})
		schemaPanics(t, "message Two field value has kind uint64 and enum E", tt.finish)
	})
	t.Run("field of another kind carries a byte length", func(t *testing.T) {
		tt := newSchemaTable()
		valid(tt)
		tt.message(2, "Two", FieldType{Tag: 1, Name: "value", Kind: KindUint64, ByteLen: 4})
		schemaPanics(t, "message Two field value has kind uint64 and byte length 4", tt.finish)
	})
	t.Run("a valid table of every field shape finishes in tag order", func(t *testing.T) {
		tt := newSchemaTable()
		tt.enum("E", enumEntry{1, "ONE"})
		tt.profile(97, complete)
		// Declared out of tag order on purpose: the registry lists fields
		// by tag.
		tt.message(1, "One",
			tt.uint64(3, "third"),
			tt.uint32(4, "fourth"),
			tt.boolean(5, "fifth"),
			tt.utf8(6, "sixth"),
			tt.optional(tt.bytes(2, "second", 4)),
			tt.optional(tt.msg(8, "eighth", 2)),
			tt.list(9, "items", 2),
			tt.optional(tt.enumField(10, "tenth", "E")),
			tt.bytes(11, "eleventh", 0))
		tt.message(2, "Two",
			tt.variant(tt.msg(1, "first", 1), "group"),
			tt.variant(tt.msg(2, "second", 1), "group"),
			tt.enumField(3, "kind", "E"))
		tt.finish()
		m, ok := tt.schema.MessageType(1)
		if !ok || len(m.Fields) != 9 {
			t.Fatalf("message %v ok %v", m, ok)
		}
		for i, f := range m.Fields {
			if want := []uint32{2, 3, 4, 5, 6, 8, 9, 10, 11}; f.Tag != want[i] {
				t.Fatalf("field %d is %+v, want tag %d", i, f, want[i])
			}
		}
		want := []string{"second=bytes4?", "third=uint64", "fourth=uint32", "fifth=bool", "sixth=utf8",
			"eighth=message(2)?", "items=list(2)", "tenth=enum(E)?", "eleventh=bytes"}
		if got := schemaFieldList(t, tt.schema, 1); !slices.Equal(got, want) {
			t.Fatalf("fields %v, want %v", got, want)
		}
		two, _ := tt.schema.MessageType(2)
		if two.Fields[0].Oneof != "group" || two.Fields[1].Oneof != "group" || two.Fields[2].Oneof != "" ||
			two.Fields[0].Optional {
			t.Fatalf("oneof fields %+v", two.Fields)
		}
	})
}
