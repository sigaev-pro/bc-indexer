# codec — E02-02.codec (#27)

Owner task: E02-02.codec (#27, phase 2): bounded typed RecordFragment/tree/blob
codec with streaming manifests and exact transport layout. See
.project/docs/TASK_DECOMPOSITION.md and
.project/evidence/E02-parallel-plan-20261121.md.

Stdlib-only Go module (plus the repository-local `protocol/proto` schema
layer), no I/O, no native runs. Normative anchors: MESSAGE_CONTRACT sections
2, 6 and 6.1 (D73 typed fragment format, tree rules, blob rules, record
digest), CONTROL_WIRE_CONTRACT "READY manifest and reservation" and
"TransportFrame layout v2" (manifest bytes, layout steps 1–4, Nats-Msg-Id and
header bytes), CONTEXT "Транспортная часть / FragmentPart".

## Why this package exists (deep module)

One record — one logical Envelope — is never serialized as a single Protobuf
message (D73). This package owns the bounded typed representation that
replaces it and the transport execution of a saved layout plan. The seam is
four constructors plus a builder; callers — the E03 delivery path and the E04
normalization layers — learn one interface. Behind the seam this package
hides:

- the pinned D73 field tables of RecordFragment, RecordHeader, TypedItems,
  TypedItem, MessageValue, ListValue, BlobRef, BlobChunks, BlobChunk and
  RecordSeal with their canonical encoding rules (ascending tags, minimal
  unsigned varints, no unknown or duplicate singular fields, every named
  scalar present, repeated fields only in the given order);
- the exact fragment-size arithmetic that packs the maximal non-empty prefix
  of the item and chunk streams into 65536-byte fragments;
- the typed tree walker with its depth-first preorder, parent, field tag,
  element index and child-count rules, shared by the write and read
  directions;
- the blob chunk rules (blob_id = node_id, offsets continuous from zero,
  chunk size min(16384, remaining), independent per-blob digests) and the
  incremental UTF-8 validation that survives chunk boundaries splitting a
  rune;
- the part split of one fragment across TransportFrames, executed strictly
  through `proto.LayoutManifestEntry.PartBounds` and the exact
  `EncodedFrameSize` + NATS header budget check — the layout planner itself
  is always `proto.ComputeLayout`, never re-implemented here;
- the "extractor.manifest.v2" batch manifest bytes with their digest and a
  bounded chunker that may split one fixed-size descriptor record.

The deletion test: without this package, every producer and consumer of
fragment streams would re-implement the D73 tables, the packing arithmetic
and the assembly state machine, and they would drift.

## Package contents

| File | Contents |
|---|---|
| errors.go | Named sentinel errors; every rejection wraps one and names the offending field, node, fragment or frame |
| wire.go | Canonical wire primitives: minimal unsigned varints, length-delimited fields, field tracking, the walk rules with consecutive repeats for repeated fields |
| utf8.go | Incremental UTF-8 scanner carrying a partial rune across chunk boundaries, no normalization |
| fragment.go | The D73 message tables: types, canonical encode/decode, exact encoded sizes, the 1..65536 fragment bound |
| tree.go | TreeNode model, TreeLimits, the preorder walker shared by both directions |
| treewrite.go | TreeCodec write side: BuildRecord streams header, packed items fragments, packed blobs fragments and seal through a sink; streaming record digest |
| treeread.go | TreeCodec read side: ReadRecord verifies the whole fragment sequence and rebuilds the tree with reassembled values |
| writer.go | FragmentWriter: one layout entry plus fragment payload in, canonical TransportFrame bytes with message id and exact NATS header bytes out; slice (zero-copy) and reader-driven (chunk-buffer) forms |
| reader.go | FragmentReader: frames in order in, verified fragment bytes out; identical redeliveries idempotent, conflicting ones rejected |
| manifest.go | ManifestBuilder: batch manifest bytes, digest, bounded chunker, layout composition through protocol/proto |

## Design decisions

1. **The fragment stream is the record.** `BuildRecord` takes the
   schema-validated tree in depth-first preorder and emits exactly one
   header fragment, the maximal non-empty TypedItems fragments, the
   BlobChunks fragments of all non-empty blobs and exactly one seal. The
   header opens the record with the final fragment count, so the packing is
   first simulated (deterministically, exact size arithmetic, no bytes
   built) and then replayed for emission; both passes share the same code
   path sizes. `ReadRecord` checks everything the producer promised:
   fragment order and indices, header/seal counts and totals, the seal
   record digest over the content fragments, packing maximality between
   consecutive fragments of one stream, tree structure, blob chunk rules,
   per-blob digests, incremental UTF-8 and the header's node/blob totals.
2. **Memory is bounded by the fragment and chunk sizes, not by the record.**
   The tree is the caller's model; the codec never materializes a second
   full copy of the record bytes. The emit path reuses one fragment-sized
   buffer; `FragmentWriter.Write` slices the caller's payload without
   copying; `WriteFromReader` reads one part capacity at a time into one
   reused chunk buffer (the D76 streaming writer pattern). The tests verify
   this twice: reads interleave with emissions and never exceed the part
   capacity, and the peak heap stays under 8 MiB while an 11.5 MiB record
   streams through (allocation totals stay near the emitted bytes; the
   residual per-frame cost is protocol/proto's transient inner message
   during the canonical wrapper encode, itself bounded by the frame budget).
3. **Every frame of one fragment carries the digest of the whole fragment.**
   A single-pass stream cannot know that digest before it is read, so the
   streaming form of the writer takes the digest from the READY descriptor
   the plan was built from and verifies the streamed bytes against it — the
   pre-SEALED per-frame size/digest check of layout step 4 at codec level.
   The slice form computes it.
4. **The layout comes from protocol/proto, always.** The writer executes a
   saved `LayoutManifestEntry` (form, capacity, part count) and verifies the
   entry belongs to the bound plan, so the form of a fragment cannot change
   between attempts. Before each frame is handed over, the exact
   `EncodedFrameSize` plus `NatsHeaderSize` is checked against the plan
   budget; a frame that cannot fit is `ErrLimitIncompatible` — nothing is
   truncated or replanned (LAYOUT-01 semantics).
5. **Assembly is order-strict, duplicate-tolerant.** The at-least-once
   contract requires identical redelivered frames to stay idempotent and
   conflicting ones to fail. The reader keeps a per-(fragment, part) digest
   of everything it accepted: an identical repeat is a no-op, a different
   repeat is `ErrDuplicate`, a gap or an unknown regression is
   `ErrReordered`. Fragment digests are verified per assembled fragment; the
   record digest over the content fragments is verified incrementally, so an
   `OnFragment` streaming consumer needs no retained bytes.
6. **The manifest is composed, not redefined.** Event and fragment
   descriptors, their digests and the layout planner all come from
   protocol/proto. ManifestBuilder adds exactly what the contract fixes at
   batch level: the `extractor.manifest.v2` byte stream (domain, zero byte,
   count, descriptors in ordinal order), its SHA-256, ordinal continuity
   from zero, unique event ids, the descriptor byte total a BeginPrepare
   carries, and a chunker whose boundaries may split one descriptor
   encoding. The delivery RPC flow around those chunks is E03.
7. **Registries stay closed, versions stay pinned.** Family, kind and form
   values come from proto's registry v1; record_format_version=1 and
   schema_version=2 are the D73 pins; unknown values are named errors with
   no OTHER and no fallback. Field tags, type keys and enum values must be
   positive; their registry membership is the schema layer's check and
   stays open here (see open questions).

## Reproducibility

Same inputs, byte-identical outputs, everywhere:

- `BuildRecord` is a pure function of its tree: building twice, and building
  after a full read, produce byte-identical fragments (tested).
- The emitted record digest equals `proto.RecordDigest` over the collected
  fragments and the `BuildEventDescriptor` totals — an independent
  implementation of the same formula — agree (tested).
- `ComputeLayout` determinism is proto's; this package's layout composition
  recomputes identical entries and digests (tested).
- Two ManifestBuilders fed the same descriptors produce identical bytes and
  digests; chunks reassemble to the exact manifest bytes for every tested
  chunk size (tested).

## Checks

`go test ./...` (also with `-race -cover`) is portable and offline: 187
cases pass, 87.8% statement coverage, gofmt and go vet clean.

## Open questions (recorded, not guessed)

1. **Registry membership of field tags, type keys and enum values.** D73
   pins the numeric registry in E02-02.schemas; the logical schema registry
   tables have not merged yet. This codec validates structure (positivity,
   ordering, counts) and treats membership as the schema layer's check. When
   the registry lands, its validation belongs behind the proto seam, not
   here. The registry has landed: see "Manifest reader and logical schema
   registry" below.
2. **Reader is layout-agnostic.** The FragmentReader verifies a
   self-consistent frame stream — identity, order, digests, totals — but not
   that each fragment's form matches a saved plan entry (a COMPLETE delivery
   of a planned-PART fragment assembles fine at this level). Plan
   conformance at ingest is E03's BeginPublish check; the writer side
   already enforces it before emission.
3. **Tree depth and breadth limits are codec-internal.** D73 allows
   unlimited nesting verified through disk-backed indexes. This in-memory
   codec pins depth and breadth to `FragmentMaxBytes / 8` (the deepest path
   or widest child index, materialized as one u64 per level or child, fits
   one fragment) so its verification state stays proportional to one
   fragment. A producer with spooled indexes may raise the limits through
   `BuildRecordWithLimits`/`ReadRecordWithLimits`; whether the wire format
   should ever carry its own bound is undecided.
4. **Part capacity uniformity is inferred.** Without the layout entry, the
   reader enforces that all non-final parts of a fragment carry equal data
   lengths (uniform capacity, only the final part may be shorter). The exact
   capacity check `LayoutManifestEntry` arithmetic provides stays with the
   plan (E03), per proto's open question 4.
5. **`bytes_value` length is unbounded at the item level.** The D73 table
   bounds only the chunk data (1..16384); a BlobRef length may exceed one
   fragment. Nothing in the merged contracts bounds a single field's size,
   and this codec does not invent a bound; the spool-backed producer of E04
   is the intended home of any operational limit.

## Manifest reader and logical schema registry

### Manifest reader

`manifest.go` also reads what `ManifestBuilder.Bytes()` writes.
`DecodeManifest(data)` parses the bytes back into `[]ManifestEvent`, in
ordinal order: per event its ordinal, event id, family, kind, record
length, record digest and its fragment descriptors (encoded length and
SHA-256 per fragment index). The driver of the producer side and the
publisher of the gateway use it to lay out and publish a batch they did
not build.

`ManifestHeaderBytes` is the fixed part of the stream before the first
descriptor: the 21-byte domain, the zero byte and the 8-byte event count.
It holds **30 bytes**. The live capture plan writes "32" in the comments
of contracts C1, C2 and C4 (`ManifestLength - 32`); that number is wrong
by two. The descriptor byte total of a manifest is its length minus
`codec.ManifestHeaderBytes`, never a literal.

The reader accepts only what the builder produces:

| Condition | Sentinel |
|---|---|
| The stream is shorter than the domain or ends inside the header | `ErrTruncated` |
| The domain differs | `ErrInvalidValue` |
| The byte after the domain is not zero | `ErrMalformed` |
| The event count is zero, with or without descriptors | `ErrFragments` |
| The stream ends inside an event descriptor or a fragment descriptor | `ErrTruncated` |
| An event descriptor or a fragment descriptor breaks a rule of protocol/proto (bad length, unknown family, unset event id, index gap, lengths that do not sum to the record length) | `ErrInvalidValue`, wrapping the error of protocol/proto |
| The stream ends at an event boundary before the declared count, or holds one more complete event after it | `ErrFragments` |
| Ordinals are not continuous from zero, or an event id repeats | `ErrFragments` |
| Bytes after the last event form no event | `ErrMalformed` |

On a rejection the result is nil, never a partial list. The declared event
count and fragment counts are compared with the bytes of the stream before
anything is allocated for them.

Decisions on points the plan leaves open:

1. An event count of zero is always rejected. The contract says an empty
   public batch produces no manifest, so no valid manifest declares zero
   events.
2. The reader also enforces the two batch rules the builder enforces:
   ordinals continuous from zero and unique event ids. A manifest that
   breaks them was not produced by the builder, and the layout planner
   rejects it as well.
3. Bytes after the declared events are one more event when they parse as
   one (the count does not match, `ErrFragments`); otherwise they are
   trailing bytes (`ErrMalformed`).

### Logical schema registry

The logical schema registry of schema_version 2 lives in
`logicalschema.go` of protocol/proto: the message type keys, field tags
and enum values a typed tree may name, the presence rules of a field, and
the registered profile reference. This codec still verifies structure
only. `BuildRecord` takes the root type key from the caller. A producer
that maps a typed model by field name resolves each tag through `FieldOf`
of the registry; a reader names nodes through `MessageType`, `Field` and
`EnumValue` and treats every miss as an error.

### Checks

The reader is covered in `manifest_test.go`: the bytes of the builder for
1, 2 and 50 events decode to the same descriptors and rebuild the same
bytes, chunks of every allowed size reassemble and decode, and each
rejection of the table above has a case that checks its sentinel and its
reason.
