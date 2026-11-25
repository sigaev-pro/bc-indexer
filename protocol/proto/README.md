# proto

Owner task: E02-02.schemas (#26, phase 2): reproducible schema bindings and
structural validators over the four phase-1 models. See
.project/docs/TASK_DECOMPOSITION.md and
.project/evidence/E02-parallel-plan-20261121.md.

Stdlib-only Go module (besides the four repository-local phase-1 models),
no I/O, no native runs. Normative anchors: CONTROL_WIRE_CONTRACT ("Base
encodings and identities", "READY manifest and reservation", "TransportFrame
layout v2"), MESSAGE_CONTRACT sections 2 and 6 (D73), D77, D79, LAYOUT-01.

## Why this package exists (deep module)

The schema layer is a deep module: a small interface with a large
implementation hidden behind it. The seam is the Validate/Encode API
surface. Callers — the E02-02.codec RecordFragment codec and the E03
delivery path — learn one interface, not four. Behind the seam this package
hides:

- the pinned field tables of the v2 transport wrapper and their canonical
  encoding rules (ascending tags, minimal varints, no unknown fields or
  duplicates, every named scalar present including index 0);
- the exact `EncodedFrameSize` arithmetic with no approximate overhead;
- the Nats-Msg-Id tuple, its base64url form and the fixed NATS header
  bytes;
- the READY event/fragment descriptor rules with the record and
  fragment-manifest digests;
- the layout v2 planner with checked arithmetic and the conservative
  maximal-varint part probe;
- the family/kind/variant triangle across `protocol/semantic` and
  `protocol/profiles`, plus the optional typed views of `protocol/block`
  and `protocol/balance`.

The deletion test: if this package were deleted, the four-model composition
complexity would reappear in every downstream consumer. That is why it
earns its keep.

## Package contents

| File | Contents |
|---|---|
| wire.go | Canonical wire primitives: minimal unsigned varints, length-delimited fields, field-order/duplicate/missing tracking |
| identity.go | ID16, Hash32, EventID (producer 16 + run 16 + sequence), NetworkIdentity with the extractor.network.v1 digest |
| registry.go | Family/kind registry v1, transport Form, pinned versions, the family/kind to payload-variant table, mapping onto the phase-1 registries |
| frame.go | NetworkIdentity, CompleteFragment, FragmentPart, TransportFrame: canonical v2 Encode/Decode, exact EncodedFrameSize |
| msgid.go | NatsMsgID (89-byte tuple, 123 ASCII bytes), NatsHeader exact bytes, NatsHeaderSize |
| descriptor.go | EventDescriptor, FragmentDescriptor: fixed-order big-endian encoding, RecordDigest (seal excluded), FragmentManifestDigest, BuildEventDescriptor, ValidateFragments |
| layout.go | LayoutManifestEntry (41-byte encoding, PartBounds), LayoutRequest, ComputeLayout, LayoutManifestDigest |
| envelope.go | The composition seam: Envelope, Payload and ValidateEnvelope over semantic + profiles + block + balance |
| errors.go | Named sentinel errors; every rejection wraps one and names the offending field or record |

## Design decisions

1. **The Envelope stays a model (D73/D77 of MESSAGE_CONTRACT section 2 and
   6).** There is no single serialized Envelope message and no monolithic
   fast path. `ValidateEnvelope` composes the models and returns READY
   descriptors computed over the payload fragment bytes; assembling those
   bytes is E02-02.codec's job, and the full payload types of every family
   are E04's scope. The tests use simple synthetic payloads per family.
2. **The wrapper encoding is hand-rolled and canonical by construction.**
   The pinned tables are small and closed, so the encoder emits ascending
   tags with minimal varints directly, and the decoder rejects everything
   the canonical form forbids: unknown fields, duplicates, out-of-order
   tags, non-minimal varints, missing named scalars (an explicit zero is
   present), a doubled or missing oneof, wrong versions, wrong lengths and
   trailing bytes. This is a locally defined canonical encoding for these
   bounded messages, not a claim about canonical Protobuf; arbitrary
   Protobuf is out of scope.
3. **`EncodedFrameSize` is exact, not an estimate.** It sums key, varint,
   length and data sizes per field. The property test asserts it equals
   `len(Encode())` for hundreds of random frames, and the golden vectors
   pin the absolute sizes (316/323/268 bytes for the fixtures).
4. **The layout planner follows the contract steps literally.** COMPLETE
   first at the exact boundary; otherwise the largest capacity C with a
   PART frame that fits under the conservative probe
   (part_index=part_count=U64Max, the widest varints, so the count/size
   circularity is avoided); part count ceil(total/C); every length, offset
   and sum checked with explicit overflow rejection; C below 1 is
   `ErrLimitIncompatible` (LAYOUT-01: the batch stays READY, nothing is
   replanned or truncated). The plan is a pure function of the request, so
   identical requests produce identical entries and digests.
5. **Registries are closed everywhere.** Family (tx=1, block=2,
   balance.system=3, bor.legacy_receipt=4), kind (applied=1, removed=2,
   status=3), form (COMPLETE=0, PART=1) and the payload variants reject
   unknown values with named errors; there is no OTHER and no silent
   default. The variant is not a free choice: it must be exactly the
   family/kind entry of registry v1, and the network profile
   (ETH/BSC/Bor) must admit it — balance.system and bor.legacy_receipt
   events exist only under a profile that declares them.
6. **Typed payload views compose, they do not reinterpret.** A
   `block.BlockRecord` is accepted only for a block-family Applied event
   and must agree with the semantic descriptor on network, block hash,
   producer, run, decision sequence and entity ordinal 0; a
   `balance.TxBalances` view is accepted only for a tx-family event and
   must pass the phase-1 structural validation. A view on the wrong family
   or kind is a named conflict.
7. **Golden vectors come from an independent implementation.** The
   expected frame bytes, digests and message ids were produced by a Python
   implementation of the same normative tables (hashlib and base64 only),
   in the discipline of the sibling phase-1 packages: neither
   implementation reads the other's code.

## Reproducibility

Same inputs, byte-identical outputs, everywhere:

- `TransportFrame.Encode` is a pure function of the frame; the property
  test encodes random frames twice, decodes and re-encodes, and requires
  byte identity plus `EncodedFrameSize == len(Encode())`.
- `ComputeLayout` is a pure function of the request; the determinism test
  recomputes plans and digests repeatedly.
- `ValidateEnvelope` returns identical descriptors for identical
  envelopes.

## Checks

`go test ./...` (also with `-race -cover`) is portable and offline:
102 cases pass, 87.8% statement coverage, gofmt and go vet clean.

## Open questions (recorded, not guessed)

1. **`event_sequence` lower bound.** The base encodings say counters start
   at 1, but no accepted text pins the first `event_sequence` value of an
   event id. This package validates only that the producer and run halves
   of an event id are set; it does not require sequence >= 1.
2. **`chain_sequence >= 1` at frame level.** The base encodings start
   counters at 1 and the reservation allocates a sequence base, so the
   frame validator rejects chain_sequence 0. If a future decision allows
   sequence 0 for any state, this check must move to the delivery layer.
3. **READY manifest stream bytes.** The per-event `fragment_manifest_digest`
   is implemented here; the chunked `extractor.manifest.v2` manifest bytes
   and digest (PrepareChunk/FinalizePrepare) are the delivery protocol's
   territory and are not built by this package. If the codec task needs a
   shared builder, it should live behind this seam in a follow-up.
4. **Part data shape beyond the totals.** The frame layer checks
   `len(data) <= fragment_total_bytes` and the part index bounds, but only
   the layout entry knows the capacity, so "only the final part may be
   shorter than capacity" is enforced through `LayoutManifestEntry`
   arithmetic, not per frame. The pre-SEALED per-frame size/digest check
   (contract step 4) belongs to E03.
5. **Inherited profile question.** `protocol/profiles` records whether
   OptionalBorLegacy should also be enumerated inside the Bor profile's
   receipt list. This package only uses `ValidateVariant`, so the outcome
   does not change anything here; it stays open in profiles.

## Logical schema registry (schema_version 2)

`logicalschema.go` holds the closed registry of the typed record tree of
D73 (MESSAGE_CONTRACT section 6.1): the message type keys, the field tags,
the enum values and the registered profile reference that a record may
name. The codec checks the structure of a tree; the registry says which
type keys, field tags and enum values exist. A value outside it is a miss,
never a passthrough.

The first version registers one path and nothing else: the Envelope, its
own messages, and the block family payload variant `block_applied` with
every message `block.BlockRecord` reaches. A later family extends the
registry as a versioned change and never renumbers what is registered
here.

### Interface

| Name | Meaning |
|---|---|
| `SchemaVersion` | The logical schema version the registry describes: 2 |
| `FieldKind` and `KindMessage`, `KindList`, `KindBool`, `KindUint32`, `KindUint64`, `KindEnum`, `KindBytes`, `KindUTF8` | The value kinds of the D73 tree |
| `MessageType`, `FieldType` | One message and one field of the schema |
| `ProfileRef` | The five strings of the profile reference of an Envelope |
| `LogicalSchemaV2()` | The shared read-only registry of schema_version 2 |
| `MessageType(key)` | The message of a type key |
| `Field(typeKey, tag)` | The field of one tag of one message |
| `FieldOf(typeKey, name)` | The field of one field name of one message |
| `EnumValue(enum, value)` | The registered name of one enum value |
| `ProfileForNetwork(chainID)` | The profile registered for one chain id |
| `Type*` constants | The message type keys |

Every lookup returns copies, so a caller cannot change the registry. The
table is static source: an invalid entry (a zero or repeated key, tag or
value, a dangling type or enum reference) is refused when the table is
built, with the reason. That is a programming error of the file, not an
input a caller can produce; the tests build broken tables on purpose to
reach each refusal.

### Numbers

- Type keys 1 to 9 are reserved for the common Envelope path and 10 to 69
  for the block family. A later family takes a range of its own above 69.
- Key 9001 is never reused: it is the demo key of `cmd/extractor-rpc`.
- Field tags are positive and unique inside a message. They follow the
  field order of the typed model of `protocol/block`.
- The tag of a payload variant is 100 plus the registry v1 variant number
  of `protocol/profiles`, so `block_applied` is 104. Fields 9 to 99 of the
  Envelope stay free for its own fields. Only `block_applied` is
  registered; the other variants are unknown field tags until a versioned
  change registers them with their message.
- Every enum value is positive and equals the value of the typed model of
  `protocol/block`.

### Presence

The mapping of a typed model to tree nodes (C6 of the live capture plan)
follows three rules, which a reader can check against the registry:

1. A plain field carries exactly one node with its field tag.
2. An optional field (`Optional`) carries one node or none: a present zero
   stays a present node, and an absent value is never a zero node.
3. A oneof variant (`Oneof`) belongs to its group, and exactly one variant
   of the group carries a node.

An enum is a `ValueEnum` node with a registered value. `Hash32`,
`Address20`, `UInt256`, `Bloom`, `Nonce`, `Id16` and the event id are
`ValueBytes` nodes of the exact length the registry names; `extra_data`
and `source_rlp` are bytes of variable length; the profile strings are
`ValueUTF8` nodes.

### Messages and fields

| Message | Tag | Field | Shape |
|---|---|---|---|
| Envelope | 1 | `schema_version` | uint32 |
| Envelope | 2 | `network` | message `NetworkIdentity` |
| Envelope | 3 | `profile_ref` | message `ProfileRef` |
| Envelope | 4 | `event_id` | bytes(40) |
| Envelope | 5 | `producer_id` | bytes(16) |
| Envelope | 6 | `run_id` | bytes(16) |
| Envelope | 7 | `source` | message `SourceRef` |
| Envelope | 8 | `capture_mode` | enum `CaptureMode` |
| Envelope | 104 | `block_applied` | message `BlockApplied`, oneof `payload` |
| NetworkIdentity | 1 | `family` | enum `NetworkFamily` |
| NetworkIdentity | 2 | `chain_id` | bytes(32) |
| NetworkIdentity | 3 | `genesis_hash` | bytes(32) |
| ProfileRef | 1 | `network_profile` | utf8 |
| ProfileRef | 2 | `client_commit` | utf8 |
| ProfileRef | 3 | `adapter_version` | utf8 |
| ProfileRef | 4 | `fork_rules_version` | utf8 |
| ProfileRef | 5 | `normalization_version` | utf8 |
| SourceRef | 1 | `node_instance_id` | bytes(16) |
| SourceRef | 2 | `writer_epoch` | bytes(16) |
| SourceRef | 3 | `source_sequence` | uint64 |
| BlockApplied | 1 | `activation` | message `BlockActivationRef` |
| BlockApplied | 2 | `admission` | message `ObservationRef` |
| BlockApplied | 3 | `metadata` | message `BlockMetadata` |
| BlockApplied | 4 | `initial_snapshot` | message `LifecycleSnapshot` |
| BlockActivationRef | 1 | `network` | message `NetworkIdentity` |
| BlockActivationRef | 2 | `block_hash` | bytes(32) |
| BlockActivationRef | 3 | `producer_id` | bytes(16) |
| BlockActivationRef | 4 | `run_id` | bytes(16) |
| BlockActivationRef | 5 | `activation_id` | message `ActivationID` |
| ActivationID | 1 | `decision_sequence` | uint64 |
| ActivationID | 2 | `entity_ordinal` | uint64 |
| ObservationRef | 1 | `producer_id` | bytes(16) |
| ObservationRef | 2 | `run_id` | bytes(16) |
| ObservationRef | 3 | `observation_sequence` | uint64 |
| BlockMetadata | 1 | `number` | uint64 |
| BlockMetadata | 2 | `hash` | bytes(32) |
| BlockMetadata | 3 | `parent_hash` | bytes(32) |
| BlockMetadata | 4 | `timestamp` | message `Timestamp` |
| BlockMetadata | 5 | `source_transaction_count` | uint64 |
| BlockMetadata | 6 | `header` | message `BlockHeader` |
| BlockMetadata | 7 | `details` | message `FamilyDetails` |
| Timestamp | 1 | `seconds` | uint64 |
| Timestamp | 2 | `nanos` | uint32 |
| Timestamp | 3 | `precision` | enum `TimestampPrecision` |
| BlockHeader | 1 | `parent_hash` | bytes(32) |
| BlockHeader | 2 | `uncle_hash` | bytes(32) |
| BlockHeader | 3 | `beneficiary` | bytes(20) |
| BlockHeader | 4 | `state_root` | bytes(32) |
| BlockHeader | 5 | `transactions_root` | bytes(32) |
| BlockHeader | 6 | `receipts_root` | bytes(32) |
| BlockHeader | 7 | `logs_bloom` | bytes(256) |
| BlockHeader | 8 | `difficulty` | bytes(32) |
| BlockHeader | 9 | `number` | uint64 |
| BlockHeader | 10 | `gas_limit` | uint64 |
| BlockHeader | 11 | `gas_used` | uint64 |
| BlockHeader | 12 | `timestamp_seconds` | uint64 |
| BlockHeader | 13 | `extra_data` | bytes |
| BlockHeader | 14 | `mix_digest` | bytes(32) |
| BlockHeader | 15 | `nonce` | bytes(8) |
| BlockHeader | 16 | `base_fee` | bytes(32), optional |
| BlockHeader | 17 | `withdrawals_root` | bytes(32), optional |
| BlockHeader | 18 | `blob_gas_used` | uint64, optional |
| BlockHeader | 19 | `excess_blob_gas` | uint64, optional |
| BlockHeader | 20 | `parent_beacon_block_root` | bytes(32), optional |
| BlockHeader | 21 | `requests_hash` | bytes(32), optional |
| BlockHeader | 22 | `source_rlp` | bytes |
| FamilyDetails | 1 | `eth` | message `EthBlockDetails`, oneof `family` |
| FamilyDetails | 2 | `bsc` | message `BscBlockDetails`, oneof `family` |
| FamilyDetails | 3 | `bor` | message `BorBlockDetails`, oneof `family` |
| EthBlockDetails | 1 | `block_access_list_hash` | bytes(32), optional |
| EthBlockDetails | 2 | `slot_number` | uint64, optional |
| BscBlockDetails | 1 | `parlia_system_transaction_count` | uint64, optional |
| BscBlockDetails | 2 | `vote_snapshot` | message `ParliaVoteSnapshot`, optional |
| ParliaVoteSnapshot | 1 | `justified` | message `AnchorRef` |
| ParliaVoteSnapshot | 2 | `finalized` | message `AnchorRef` |
| BorBlockDetails | 1 | `sprint_length` | uint64, optional |
| BorBlockDetails | 2 | `sprint_end` | bool, optional |
| BorBlockDetails | 3 | `side_chain_block` | bool, optional |
| BorBlockDetails | 4 | `state_sync_count` | uint64, optional |
| BorBlockDetails | 5 | `state_sync_mode` | enum `StateSyncMode`, optional |
| BorBlockDetails | 6 | `heimdall_checkpoints` | list of `HeimdallCheckpointRef` |
| HeimdallCheckpointRef | 1 | `kind` | enum `CheckpointKind` |
| HeimdallCheckpointRef | 2 | `end` | message `AnchorRef` |
| HeimdallCheckpointRef | 3 | `checkpoint_id` | uint64, optional |
| LifecycleSnapshot | 1 | `native_decision` | message `NativeDecisionRef` |
| LifecycleSnapshot | 2 | `observed_execution_head` | message `AnchorRef` |
| LifecycleSnapshot | 3 | `markers` | message `FinalityMarkers` |
| NativeDecisionRef | 1 | `node_instance_id` | bytes(16) |
| NativeDecisionRef | 2 | `producer_id` | bytes(16) |
| NativeDecisionRef | 3 | `run_id` | bytes(16) |
| NativeDecisionRef | 4 | `decision_sequence` | uint64 |
| AnchorRef | 1 | `height` | uint64 |
| AnchorRef | 2 | `hash` | bytes(32) |
| FinalityMarkers | 1 | `eth` | message `EthereumMarkers`, oneof `family` |
| FinalityMarkers | 2 | `bsc` | message `BscMarkers`, oneof `family` |
| FinalityMarkers | 3 | `polygon` | message `PolygonMarkers`, oneof `family` |
| EthereumMarkers | 1 | `safe` | message `ReferenceObservation` |
| EthereumMarkers | 2 | `finalized` | message `ReferenceObservation` |
| EthereumMarkers | 3 | `safe_basis` | enum `EthereumSafeBasis` |
| BscMarkers | 1 | `attested` | message `AttestedSnapshot`, optional |
| BscMarkers | 2 | `effective_finalized` | message `ReferenceObservation` |
| BscMarkers | 3 | `effective_basis` | enum `BscEffectiveBasis` |
| AttestedSnapshot | 1 | `snapshot_head` | message `AnchorRef` |
| AttestedSnapshot | 2 | `justified` | message `AnchorRef` |
| AttestedSnapshot | 3 | `finalized` | message `AnchorRef` |
| PolygonMarkers | 1 | `milestone` | message `ReferenceObservation` |
| PolygonMarkers | 2 | `checkpoint` | message `ReferenceObservation` |
| PolygonMarkers | 3 | `native_finalized` | message `ReferenceObservation` |
| PolygonMarkers | 4 | `l1_checkpoint` | message `ReferenceObservation` |
| ReferenceObservation | 1 | `kind` | enum `ReferenceKind` |
| ReferenceObservation | 2 | `anchor` | message `AnchorRef`, optional |
| ReferenceObservation | 3 | `origin` | enum `EvidenceOrigin`, optional |
| ReferenceObservation | 4 | `reason` | enum `UnavailableReason`, optional |

### Enums

| Enum | Registered values |
|---|---|
| `CaptureMode` | 1 `LIVE` |
| `NetworkFamily` | 1 `EVM` |
| `TimestampPrecision` | 1 `SECONDS`, 2 `MILLISECONDS`, 3 `NANOSECONDS` |
| `StateSyncMode` | 1 `END_OF_BLOCK_COMMIT`, 2 `BODY_TRANSACTION` |
| `CheckpointKind` | 1 `MILESTONE`, 2 `CHECKPOINT` |
| `EvidenceOrigin` | 1 `NATIVE_LIVE`, 2 `NATIVE_RESTART`, 3 `RETAINED_DURABLE` |
| `UnavailableReason` | 1 `SOURCE_OFFLINE`, 2 `SOURCE_SYNCING`, 3 `STATE_UNAVAILABLE`, 4 `CHECKPOINT_PENDING` |
| `ReferenceKind` | 1 `KNOWN`, 2 `NOT_OBSERVED`, 3 `NATIVE_REFERENCE_UNSET`, 4 `UNAVAILABLE` |
| `EthereumSafeBasis` | 1 `CONSENSUS_SETTER`, 2 `STARTUP_FINALIZED_FALLBACK`, 3 `UNSET` |
| `BscEffectiveBasis` | 1 `HEADER_SNAPSHOT`, 2 `VOTE_POOL_QUORUM`, 3 `GENESIS_FALLBACK`, 4 `UNSET` |

### Registered profile

One profile is registered, for the BSC Chapel testnet (chain id 97). The
block event builder writes these five strings into `profile_ref`. Each
names one versioned input of the record and changes only with that input.

| String | Value | Names |
|---|---|---|
| `network_profile` | `bsc-testnet` | The network profile |
| `client_commit` | `54afaff4da31ac9e49ba646a4f65104edac5a2f3` | The pinned BSC client the adapter and the fork rules were written for: BSC v1.8.2 |
| `adapter_version` | `bsc-adapter-v1` | The version of the BSC capture adapter whose facts the record carries |
| `fork_rules_version` | `bsc-fork-rules-v1` | The fork rules of that client commit; the builder derives no fork rule itself |
| `normalization_version` | `bsc-normalization-v1` | The normalization profile of the BSC block event |

### Decisions on points the plan leaves open

1. `FieldType` carries one field more than the contract text lists:
   `Oneof`. Without it a reader cannot tell a oneof variant from a
   mandatory field and cannot check that exactly one variant is present.
2. The payload variant `block_applied` carries the `BlockRecord` itself
   (activation, admission, metadata, initial snapshot) under the message
   name `BlockApplied`. There is no wrapper field around the record.
3. `CaptureMode` registers `LIVE` only. `SYNC` and `BACKFILL` are deferred
   and are not registered values.
4. `FamilyDetails` and `FinalityMarkers` are oneofs without a family name
   field: the variant tag names the family. The `family` string of the
   local JSON encoding of `protocol/block` is not a tree field.
5. In `ReferenceObservation` the fields `anchor`, `origin` and `reason`
   are optional, and the kind decides which of them are present: a known
   reference carries its anchor and origin, an unavailable one its reason,
   and the other kinds carry neither. This is the shape of the local JSON
   encoding of `protocol/block`.
6. `NetworkIdentity.family` is the enum `NetworkFamily` (`EVM` = 1), not a
   string.
7. A list field names the type key of its element message. The block path
   has one list, `heimdall_checkpoints`; a list of scalars would need a
   registry extension.
8. The client commit is the pinned commit of the build, not the
   `native_commit` the node reads from its configuration. The builder has
   no input for the configured value.
9. `bsc-normalization-v1` is the candidate product normalization profile
   ID that D99 item 4 assigns to E04-01.block_record. The capsule item
   `normalization-profile` of the admission packages still says
   `test-only-probe-v1`; aligning it is outside this change.
10. Only the BSC testnet has a registered profile. A mainnet BSC record
    needs its own entry; the builder refuses any other chain id.

### Checks

Portable and offline, in this directory: `go build ./...`, `go vet ./...`,
`gofmt -l .` (no output) and `go test -count=1 -race -cover ./...` pass
with 59 tests, 93 subtests and 92.0% statement coverage.
`python3 -I test/mutate.py protocol/proto/logicalschema.mutants.json
--output <new directory>` runs the 152 single-check mutants of the
registry: the lookups, each refusal of the builder, the field constructors
and the wire numbers the tests pin. Every mutant is killed.
