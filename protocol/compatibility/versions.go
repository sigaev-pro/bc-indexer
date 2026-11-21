package compatibility

import "fmt"

// ProtocolVersion is a concrete encoding version wired into this policy:
// the opaque identifier of one pinned version of the E02 wire contract,
// the explicit set of earlier versions it can decode, and whether new
// records may be created in it.
//
// The identifiers below mirror the version constants pinned in
// protocol/proto/registry.go (E02-02.schemas #26: transport_version=2,
// record_format_version=1, layout_version=2, header_version=2, control
// protocol_version=2, manifest_version=2). This module stays
// stdlib-only and does not import the proto module: registry.go is the
// normative source of the pinned values, and each constant there is
// mirrored by exactly one version here. The digest-domain strings of the
// contract (extractor.network.v1, extractor.record.v1,
// extractor.fragments.v1, extractor.layout.v2, extractor.manifest.v2,
// extractor.reservation.v2, extractor.binding.v1) are fixed parts of
// their encodings' bytes, not preflight version fields, and are not
// registered as versions.
type ProtocolVersion struct {
	id        VersionID
	reads     []VersionID
	writesNew bool
}

// NewProtocolVersion builds a concrete version. The identifier must be
// non-empty; every claimed readable version must be non-empty, different
// from the version itself (identity always holds and is not a claim),
// and claimed at most once. Invalid input is rejected, never repaired.
func NewProtocolVersion(id VersionID, reads []VersionID, writesNew bool) (ProtocolVersion, error) {
	if id == "" {
		return ProtocolVersion{}, &ErrInvalidVersion{Reason: "version identifier is empty"}
	}
	seen := make(map[VersionID]bool, len(reads))
	for _, r := range reads {
		if r == "" {
			return ProtocolVersion{}, &ErrInvalidVersion{Reason: fmt.Sprintf("version %q claims to read a version with an empty identifier", id)}
		}
		if r == id {
			return ProtocolVersion{}, &ErrInvalidVersion{Reason: fmt.Sprintf("version %q lists itself as a read claim; identity always holds and is not a claim", id)}
		}
		if seen[r] {
			return ProtocolVersion{}, &ErrInvalidVersion{Reason: fmt.Sprintf("version %q claims to read %q more than once", id, r)}
		}
		seen[r] = true
	}
	return ProtocolVersion{id: id, reads: append([]VersionID(nil), reads...), writesNew: writesNew}, nil
}

// ID returns the opaque, stable identifier of this encoding version.
func (v ProtocolVersion) ID() VersionID { return v.id }

// Reads reports whether a process running this version can decode the
// records and metadata encoded by the version identified by from. A
// version always reads its own records; beyond identity, exactly the
// versions listed at construction are readable. Claims are per pair and
// never transitive.
func (v ProtocolVersion) Reads(from VersionID) bool {
	if from == v.id {
		return true
	}
	for _, r := range v.reads {
		if r == from {
			return true
		}
	}
	return false
}

// WritesNew reports whether new records may be created in this version.
func (v ProtocolVersion) WritesNew() bool { return v.writesNew }

// mustVersion builds a pinned version. The pinned inputs are static and
// valid, so the error path exists only for programmer mistakes.
func mustVersion(id VersionID, reads []VersionID, writesNew bool) ProtocolVersion {
	v, err := NewProtocolVersion(id, reads, writesNew)
	if err != nil {
		panic(err)
	}
	return v
}

// The pinned encoding versions of the v2 wire contract, one per version
// constant of the protocol/proto registry, named after those constants.
// Every pinned version is the first version of its kind: no earlier
// version exists to read, so each read set is empty and identity is the
// only readability. All six belong to the wire/plan family
// (ClaimWirePlan): they format what is written and published, and a
// completed drain closes every obligation they ever formatted. The
// persistent store format family (ClaimStoreFormat) owns no version in
// the proto registry; the durable store's format is a preflight version
// of the store itself, registered by its owner when one exists.
var (
	// TransportV2 mirrors proto's TransportVersion=2: the TransportFrame
	// wrapper version of the canonical encoding v2.
	TransportV2 = mustVersion("transport/2", nil, true)

	// RecordFormatV1 mirrors proto's RecordFormatVersion=1: the
	// RecordFragment format version.
	RecordFormatV1 = mustVersion("record_format/1", nil, true)

	// LayoutV2 mirrors proto's LayoutVersion=2: the transport layout
	// version (COMPLETE/PART planning and the layout manifest).
	LayoutV2 = mustVersion("layout/2", nil, true)

	// HeaderV2 mirrors proto's HeaderVersion=2: the immutable header
	// contract version (Nats-Msg-Id tuple and fixed header bytes).
	HeaderV2 = mustVersion("header/2", nil, true)

	// ControlV2 mirrors proto's ControlVersion=2: the control wire
	// protocol version of the mutating RPCs.
	ControlV2 = mustVersion("control/2", nil, true)

	// ManifestV2 mirrors proto's ManifestVersion=2: the READY manifest
	// version of the reservation seed.
	ManifestV2 = mustVersion("manifest/2", nil, true)
)
