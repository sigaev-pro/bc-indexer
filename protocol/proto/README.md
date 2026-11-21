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
