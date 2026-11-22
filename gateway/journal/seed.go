package journal

import (
	"crypto/rand"
	"crypto/sha256"
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
// plan_digest = SHA256(seed). The seed is immutable and is returned
// byte-for-byte on every retry.
const (
	seedDomain   = "extractor.reservation.v2"
	seedBodyLen  = 16 + 16 + 40 + 32 + 8 + 8 + 32 + 8 + 4*5
	seedTotalLen = len(seedDomain) + 1 + seedBodyLen
)

// SeedLayout is the parsed identity of a reservation seed.
type SeedLayout struct {
	ReservationID  [16]byte
	IssuerEpoch    [16]byte
	Batch          BatchID
	ManifestDigest [32]byte
	EventCount     uint64
	SequenceBase   uint64
	BindingDigest  [32]byte
	FrameLimit     uint64
}

// BuildSeed assembles the seed bytes for a newly allocated reservation.
func BuildSeed(l SeedLayout) []byte {
	b := make([]byte, 0, seedTotalLen)
	b = append(b, seedDomain...)
	b = append(b, 0)
	b = append(b, l.ReservationID[:]...)
	b = append(b, l.IssuerEpoch[:]...)
	batch := l.Batch.Bytes()
	b = append(b, batch[:]...)
	b = append(b, l.ManifestDigest[:]...)
	b = append(b, u64be(l.EventCount)...)
	b = append(b, u64be(l.SequenceBase)...)
	b = append(b, l.BindingDigest[:]...)
	b = append(b, u64be(l.FrameLimit)...)
	b = append(b, u32be(TransportVersion)...)
	b = append(b, u32be(LayoutVersion)...)
	b = append(b, u32be(HeaderVersion)...)
	b = append(b, u32be(RecordFormat)...)
	b = append(b, u32be(ManifestVersion)...)
	return b
}

// ParseSeed decodes and structurally validates a seed. Version fields must
// equal the pinned v2 values; the caller checks the identity fields against
// its own durable state.
func ParseSeed(seed []byte) (SeedLayout, error) {
	if len(seed) != seedTotalLen {
		return SeedLayout{}, ErrInvalidSeed
	}
	p := 0
	if string(seed[:len(seedDomain)]) != seedDomain || seed[len(seedDomain)] != 0 {
		return SeedLayout{}, ErrInvalidSeed
	}
	p = len(seedDomain) + 1
	take := func(n int) []byte {
		v := seed[p : p+n]
		p += n
		return v
	}
	var l SeedLayout
	copy(l.ReservationID[:], take(16))
	copy(l.IssuerEpoch[:], take(16))
	var batch [40]byte
	copy(batch[:], take(40))
	copy(l.Batch.ProducerID[:], batch[:16])
	copy(l.Batch.RunID[:], batch[16:32])
	l.Batch.Seq = beU64(batch[32:])
	copy(l.ManifestDigest[:], take(32))
	l.EventCount = beU64(take(8))
	l.SequenceBase = beU64(take(8))
	copy(l.BindingDigest[:], take(32))
	l.FrameLimit = beU64(take(8))
	if beU32(take(4)) != TransportVersion || beU32(take(4)) != LayoutVersion ||
		beU32(take(4)) != HeaderVersion || beU32(take(4)) != RecordFormat ||
		beU32(take(4)) != ManifestVersion {
		return SeedLayout{}, ErrInvalidSeed
	}
	return l, nil
}

// PlanDigest returns plan_digest = SHA256(seed bytes).
func PlanDigest(seed []byte) [32]byte { return sha256.Sum256(seed) }

// newReservationID generates one independent identity with crypto RNG.
func newReservationID() ([16]byte, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return id, err
	}
	return id, nil
}
