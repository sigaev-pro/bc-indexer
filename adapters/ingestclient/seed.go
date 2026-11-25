package ingestclient

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// The reservation seed binary v2 of CONTROL_WIRE_CONTRACT ("READY manifest
// and reservation"):
//
//	domain "extractor.reservation.v2" || 0x00
//	|| reservation_id16 || issuer_store_epoch16 || batch_id40
//	|| manifest_digest32 || event_count_u64 || sequence_base_u64
//	|| binding_digest32 || frame_limit_u64
//	|| transport_version_u32 || layout_version_u32 || header_version_u32
//	|| record_format_version_u32 || manifest_version_u32
//
// plan_digest = SHA-256(seed). The gateway allocates the seed once per
// (batch, manifest digest) and returns it byte for byte on every retry; the
// producer parses it and checks it against its own records before it builds
// a plan on it. This is the producer-side copy of the parser of the gateway
// journal (gateway/journal/seed.go); the two read the same bytes.
const (
	// SeedDomain opens the seed.
	SeedDomain = "extractor.reservation.v2"
	// seedBodyLen is the seed without the domain and its zero byte.
	seedBodyLen = 16 + 16 + 40 + 32 + 8 + 8 + 32 + 8 + 4*5
	// SeedLength is the exact length of a seed.
	SeedLength = len(SeedDomain) + 1 + seedBodyLen
)

// Seed is the parsed identity of a reservation seed.
type Seed struct {
	ReservationID  proto.ID16
	IssuerEpoch    proto.ID16
	ProducerID     proto.ID16
	RunID          proto.ID16
	BatchSeq       uint64
	ManifestDigest proto.Hash32
	EventCount     uint64
	SequenceBase   uint64
	BindingDigest  proto.Hash32
	FrameLimit     uint64

	// raw is the seed as the gateway answered it; the plan header and the
	// publish requests carry these bytes.
	raw []byte
}

// PlanDigest returns plan_digest = SHA-256(seed bytes).
func PlanDigest(seed []byte) proto.Hash32 {
	return proto.Hash32(sha256.Sum256(seed))
}

// ParseSeed decodes and structurally validates a seed: the length, the
// domain, the zero byte and the five pinned versions. The identity fields
// are returned as they are; the caller checks them against its durable
// records.
func ParseSeed(seed []byte) (Seed, error) {
	if len(seed) != SeedLength {
		return Seed{}, fmt.Errorf("%w: length %d, want %d", ErrInvalidSeed, len(seed), SeedLength)
	}
	if string(seed[:len(SeedDomain)]) != SeedDomain || seed[len(SeedDomain)] != 0 {
		return Seed{}, fmt.Errorf("%w: domain or zero byte differs", ErrInvalidSeed)
	}
	p := len(SeedDomain) + 1
	take := func(n int) []byte {
		v := seed[p : p+n]
		p += n
		return v
	}
	var s Seed
	copy(s.ReservationID[:], take(16))
	copy(s.IssuerEpoch[:], take(16))
	copy(s.ProducerID[:], take(16))
	copy(s.RunID[:], take(16))
	s.BatchSeq = binary.BigEndian.Uint64(take(8))
	copy(s.ManifestDigest[:], take(32))
	s.EventCount = binary.BigEndian.Uint64(take(8))
	s.SequenceBase = binary.BigEndian.Uint64(take(8))
	copy(s.BindingDigest[:], take(32))
	s.FrameLimit = binary.BigEndian.Uint64(take(8))
	versions := []struct {
		name string
		want uint32
	}{
		{"transport", proto.TransportVersion},
		{"layout", proto.LayoutVersion},
		{"header", proto.HeaderVersion},
		{"record_format", proto.RecordFormatVersion},
		{"manifest", proto.ManifestVersion},
	}
	for _, v := range versions {
		if got := binary.BigEndian.Uint32(take(4)); got != v.want {
			return Seed{}, fmt.Errorf("%w: %s version %d, want %d", ErrInvalidSeed, v.name, got, v.want)
		}
	}
	s.raw = append([]byte(nil), seed...)
	return s, nil
}

// checkSeed verifies the identity fields of a seed against the producer's
// records and that the plan digest the gateway answered is the digest of
// these bytes: batch id, manifest digest, event count and binding digest
// must equal the producer's, and SHA-256(seed) must equal planDigest.
func checkSeed(seed []byte, planDigest []byte, seq uint64, ready producer.Ready,
	id producer.Identity, binding proto.Hash32) (Seed, error) {
	s, err := ParseSeed(seed)
	if err != nil {
		return Seed{}, err
	}
	if len(planDigest) != 32 {
		return Seed{}, fmt.Errorf("%w: plan digest of %d bytes, want 32", ErrProtocol, len(planDigest))
	}
	var wantPlan proto.Hash32
	copy(wantPlan[:], planDigest)
	if PlanDigest(seed) != wantPlan {
		return Seed{}, fmt.Errorf("%w: batch %d: plan digest %x is not the digest of the answered seed",
			ErrBlocked, seq, wantPlan)
	}
	mismatch := ""
	switch {
	case s.ProducerID != id.ProducerID || s.RunID != id.RunID || s.BatchSeq != seq:
		mismatch = "batch id"
	case s.ManifestDigest != ready.ManifestDigest:
		mismatch = "manifest digest"
	case s.EventCount != ready.EventCount:
		mismatch = "event count"
	case s.BindingDigest != binding:
		mismatch = "binding digest"
	case s.FrameLimit == 0:
		mismatch = "frame limit"
	}
	if mismatch != "" {
		return Seed{}, fmt.Errorf("%w: batch %d: the reservation names another %s",
			ErrBlocked, seq, mismatch)
	}
	return s, nil
}
