// Package codec is the bounded RecordFragment/tree/blob codec of E02-02.codec
// (#27): it produces and consumes the typed D73 fragment stream of one
// logical record and the transport frames that carry it.
//
// The full Envelope is never serialized as one Protobuf message
// (MESSAGE_CONTRACT sections 2 and 6, D73). A record is a bounded sequence of
// RecordFragment messages — exactly one header, at least one TypedItems
// fragment, optional BlobChunks fragments and exactly one seal — where every
// serialized fragment, wrapper included, is 1..65536 bytes. Large field values
// are BlobRefs in the typed tree; their bytes travel as separate blob chunks,
// so a single large field is split before serialization. FragmentPart splits
// one serialized fragment across transport frames when it does not fit the
// NATS frame budget.
//
// The package is a deep module over protocol/proto. The seam is four types:
//
//   - BuildRecord (TreeCodec write side): schema-validated tree nodes in
//     depth-first preorder plus a fragment sink in, canonical fragment bytes
//     out. The fragment stream is emitted piece by piece; the codec never
//     materializes a second full copy of the record.
//   - ReadRecord (TreeCodec read side): fragment bytes in, the validated tree
//     with reassembled values out, with every structural, blob and seal
//     invariant checked and named.
//   - FragmentWriter: fragment payload plus a protocol/proto layout plan entry
//     in, canonical TransportFrame bytes with the exact NATS message id and
//     header bytes out, within the frame budget, calling the layout planner
//     of protocol/proto instead of re-implementing it.
//   - FragmentReader: transport frames in order in, verified fragment bytes
//     out; truncation, reordering, conflicting duplicates and digest
//     mismatches are named errors, while identical redelivered frames stay
//     idempotent as the at-least-once contract requires.
//
// ManifestBuilder composes the READY descriptors of a closed batch into the
// chunked "extractor.manifest.v2" manifest bytes with their digest, and the
// batch-level transport layout plan through protocol/proto.
//
// Every decoder is closed and versioned: unknown record format or schema
// versions, unknown fields, duplicate or out-of-order tags, non-minimal
// varints, missing named scalars and registry violations are named errors,
// never silent defaults. All length, count and offset arithmetic is checked;
// overflow is rejected, never wrapped. Encodings are reproducible: the same
// input always produces byte-identical fragments, frames and manifests.
//
// The package imports the Go standard library and protocol/proto only.
package codec
