// Package proto is the schema layer of E02-02.schemas: versioned structural
// validators with reproducible encodings over the four phase-1 models
// (protocol/semantic, protocol/balance, protocol/block, protocol/profiles).
//
// The package is a deep module. The seam is the Validate/Encode API surface:
// callers (the E02-02.codec RecordFragment codec and the E03 delivery path)
// learn one interface, not four. Behind it this package hides the pinned
// field tables of the v2 transport wrapper (CONTROL_WIRE_CONTRACT, "TransportFrame
// layout v2"), the READY event/fragment descriptor rules ("READY manifest and
// reservation"), the Nats-Msg-Id tuple, the layout v2 planner and the
// descriptor/payload consistency checks. The deletion test: if this package
// were deleted, the four-model composition complexity (family and kind
// registries, canonical wrapper bytes, exact frame sizes, header-fixed
// budgets, checked layout arithmetic) would reappear in every downstream
// consumer. That is why the package earns its keep.
//
// MESSAGE_CONTRACT section 6 (D73) fixes the boundary above this layer: the
// Envelope is a model, not one serialized Protobuf message, and the
// RecordFragment codec belongs to E02-02.codec. This package validates and
// digests fragment bytes; it never interprets their typed content, which is
// E04's scope.
//
// Every decoder is versioned and closed: unknown versions, unknown field
// numbers, duplicate or out-of-order fields, non-minimal varints and unknown
// registry values are named errors, never silent defaults. Encodings are
// reproducible: the same input always produces byte-identical output.
// All arithmetic on lengths, counts and offsets is checked; overflow is
// rejected, never wrapped or truncated.
//
// The package imports only the Go standard library besides the four
// repository-local phase-1 models.
package proto
