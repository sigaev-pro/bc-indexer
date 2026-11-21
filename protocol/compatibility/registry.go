package compatibility

// DefaultRegistry is the standing wiring of the pinned proto encoding
// versions into the capability policy: one matrix per version kind.
//
// The kinds version independently — a future transport v3 does not bump
// the record format — and a matrix tracks exactly one current new-write
// version over one version namespace, so the kinds cannot share a single
// matrix. Every pinned version is the only version of its kind so far:
// each kind's matrix registers it as the current version, and the kind
// has no earlier version to claim to read.
//
// A cross-kind question (can transport/2 read layout/2?) is a category
// error; on any one matrix the other kind's identifier is unknown and
// the decision is ErrUnknownVersion — the safe rejection, never a
// fallback.
//
// The zero DefaultRegistry is not usable; build one with
// NewDefaultRegistry. A DefaultRegistry is safe for concurrent use
// because every field is.
type DefaultRegistry struct {
	// Transport is the matrix of the TransportFrame wrapper versions;
	// current: transport/2.
	Transport *Matrix
	// RecordFormat is the matrix of the RecordFragment format versions;
	// current: record_format/1.
	RecordFormat *Matrix
	// Layout is the matrix of the transport layout versions; current:
	// layout/2.
	Layout *Matrix
	// Header is the matrix of the immutable header contract versions;
	// current: header/2.
	Header *Matrix
	// Control is the matrix of the control wire protocol versions;
	// current: control/2.
	Control *Matrix
	// Manifest is the matrix of the READY manifest versions; current:
	// manifest/2.
	Manifest *Matrix
}

// NewDefaultRegistry builds the default registration. It cannot fail:
// the pinned versions are static, valid and pairwise distinct, so a
// failure here is a programmer mistake and panics like a Must-constructor.
func NewDefaultRegistry() *DefaultRegistry {
	return &DefaultRegistry{
		Transport:    mustMatrix(TransportV2),
		RecordFormat: mustMatrix(RecordFormatV1),
		Layout:       mustMatrix(LayoutV2),
		Header:       mustMatrix(HeaderV2),
		Control:      mustMatrix(ControlV2),
		Manifest:     mustMatrix(ManifestV2),
	}
}

// Versions returns every pinned version of the registry, one
// ProtocolVersion per version constant of the proto registry, in
// registry order. The slice is fresh; callers may register these
// versions into their own matrices.
func (r *DefaultRegistry) Versions() []EncodingVersion {
	return []EncodingVersion{TransportV2, RecordFormatV1, LayoutV2, HeaderV2, ControlV2, ManifestV2}
}

// mustMatrix builds a single-version matrix. The pinned inputs are
// static and valid, so the error path exists only for programmer
// mistakes.
func mustMatrix(v ProtocolVersion) *Matrix {
	m, err := NewMatrix(v)
	if err != nil {
		panic(err)
	}
	return m
}
