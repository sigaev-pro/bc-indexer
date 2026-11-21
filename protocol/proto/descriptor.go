// The READY manifest descriptor layer: the event descriptor v2, its
// fragment sub-descriptors, their fixed-order big-endian encoding and the
// record and fragment-manifest digests (CONTROL_WIRE_CONTRACT, "READY manifest
// and reservation"; MESSAGE_CONTRACT section 6.1, D73). The full chunked
// manifest stream ("extractor.manifest.v2") belongs to the delivery
// protocol; this layer produces and validates one event's descriptors.
package proto

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// Digest domains, each followed by one zero byte in the preimage.
const (
	recordDigestDomain = "extractor.record.v1"
	fragmentDomain     = "extractor.fragments.v1"
	layoutDigestDomain = "extractor.layout.v2"
)

// minimumFragmentCount is the least fragment count of a valid record:
// exactly one header, at least one non-empty items or blobs fragment, and
// exactly one seal (MESSAGE_CONTRACT section 6.1).
const minimumFragmentCount = 3

// Fixed encoding sizes of the descriptor records.
const (
	// EventDescriptorSize is the encoded size of one event descriptor.
	EventDescriptorSize = 8 + 40 + 2 + 2 + 8 + 32 + 8 + 32
	// FragmentDescriptorSize is the encoded size of one fragment descriptor.
	FragmentDescriptorSize = 8 + 8 + 32
)

// FragmentDescriptor is one fragment sub-descriptor of the READY manifest:
// {fragment_index, encoded_length, fragment_sha256}.
type FragmentDescriptor struct {
	FragmentIndex  uint64
	EncodedLength  uint64
	FragmentSHA256 Hash32
}

// Validate rejects a length outside the 1..65536 fragment bound.
func (d FragmentDescriptor) Validate() error {
	if d.EncodedLength == 0 || d.EncodedLength > FragmentMaxBytes {
		return fmt.Errorf("%w: fragment %d encoded length %d outside 1..%d",
			ErrInvalidValue, d.FragmentIndex, d.EncodedLength, FragmentMaxBytes)
	}
	return nil
}

// AppendEncoded appends the fixed-order big-endian form of d:
// fragment_index u64be, encoded_length u64be, fragment_sha256 raw 32 bytes.
func (d FragmentDescriptor) AppendEncoded(b []byte) []byte {
	b = u64be(b, d.FragmentIndex)
	b = u64be(b, d.EncodedLength)
	return append(b, d.FragmentSHA256[:]...)
}

// Encode returns the fixed-order big-endian form of d.
func (d FragmentDescriptor) Encode() []byte {
	return d.AppendEncoded(make([]byte, 0, FragmentDescriptorSize))
}

// DecodeFragmentDescriptor parses exactly one fixed-order fragment
// descriptor; both wrong length and wrong bytes are named errors.
func DecodeFragmentDescriptor(b []byte) (FragmentDescriptor, error) {
	if len(b) != FragmentDescriptorSize {
		return FragmentDescriptor{}, fmt.Errorf("%w: fragment descriptor length %d, want %d",
			ErrInvalidValue, len(b), FragmentDescriptorSize)
	}
	var d FragmentDescriptor
	d.FragmentIndex = beUint64(b[0:8])
	d.EncodedLength = beUint64(b[8:16])
	copy(d.FragmentSHA256[:], b[16:48])
	if err := d.Validate(); err != nil {
		return FragmentDescriptor{}, err
	}
	return d, nil
}

// EventDescriptor is the event descriptor v2 of the READY manifest:
// {ordinal, event_id 40 bytes, event_family u16, event_kind u16,
// record_length u64, record_digest Hash32, fragment_count u64,
// fragment_manifest_digest Hash32}.
type EventDescriptor struct {
	Ordinal                uint64
	EventID                EventID
	Family                 Family
	Kind                   Kind
	RecordLength           uint64
	RecordDigest           Hash32
	FragmentCount          uint64
	FragmentManifestDigest Hash32
}

// Validate rejects a descriptor that breaks the registry or the record
// shape: unknown family or kind, unset event identity, a fragment count
// below the header/items/seal minimum, or a record length that cannot carry
// that many non-empty fragments.
func (d EventDescriptor) Validate() error {
	if !d.Family.Valid() {
		return fmt.Errorf("%w: event family %d is not in registry v1", ErrClosedRegistry, uint16(d.Family))
	}
	if !d.Kind.Valid() {
		return fmt.Errorf("%w: event kind %d is not in registry v1", ErrClosedRegistry, uint16(d.Kind))
	}
	if err := d.EventID.Validate(); err != nil {
		return err
	}
	if d.FragmentCount < minimumFragmentCount {
		return fmt.Errorf("%w: fragment count %d below the header/items/seal minimum %d",
			ErrFragments, d.FragmentCount, minimumFragmentCount)
	}
	if d.RecordLength < d.FragmentCount {
		return fmt.Errorf("%w: record length %d cannot carry %d non-empty fragments",
			ErrFragments, d.RecordLength, d.FragmentCount)
	}
	return nil
}

// AppendEncoded appends the fixed-order big-endian form of the descriptor:
// ordinal, event_id, family u16be, kind u16be, record_length, record_digest,
// fragment_count, fragment_manifest_digest.
func (d EventDescriptor) AppendEncoded(b []byte) []byte {
	b = u64be(b, d.Ordinal)
	b = append(b, d.EventID[:]...)
	b = u16be(b, uint16(d.Family))
	b = u16be(b, uint16(d.Kind))
	b = u64be(b, d.RecordLength)
	b = append(b, d.RecordDigest[:]...)
	b = u64be(b, d.FragmentCount)
	return append(b, d.FragmentManifestDigest[:]...)
}

// Encode returns the fixed-order big-endian form of the descriptor.
func (d EventDescriptor) Encode() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return d.AppendEncoded(make([]byte, 0, EventDescriptorSize)), nil
}

// DecodeEventDescriptor parses exactly one fixed-order event descriptor.
func DecodeEventDescriptor(b []byte) (EventDescriptor, error) {
	if len(b) != EventDescriptorSize {
		return EventDescriptor{}, fmt.Errorf("%w: event descriptor length %d, want %d",
			ErrInvalidValue, len(b), EventDescriptorSize)
	}
	var d EventDescriptor
	d.Ordinal = beUint64(b[0:8])
	copy(d.EventID[:], b[8:48])
	d.Family = Family(beUint16(b[48:50]))
	d.Kind = Kind(beUint16(b[50:52]))
	d.RecordLength = beUint64(b[52:60])
	copy(d.RecordDigest[:], b[60:92])
	d.FragmentCount = beUint64(b[92:100])
	copy(d.FragmentManifestDigest[:], b[100:132])
	if err := d.Validate(); err != nil {
		return EventDescriptor{}, err
	}
	return d, nil
}

// ValidateFragments checks a descriptor's fragment list against the READY
// rules: the count matches, indices are continuous from 0 in ascending
// order, every encoded_length is inside the fragment bound, and the checked
// sum of the lengths equals record_length exactly.
func ValidateFragments(event EventDescriptor, frags []FragmentDescriptor) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if uint64(len(frags)) != event.FragmentCount {
		return fmt.Errorf("%w: %d fragment descriptors for fragment_count %d",
			ErrFragments, len(frags), event.FragmentCount)
	}
	var sum uint64
	for i := range frags {
		d := frags[i]
		if d.FragmentIndex != uint64(i) {
			return fmt.Errorf("%w: fragment index %d at position %d breaks continuity",
				ErrFragments, d.FragmentIndex, i)
		}
		if err := d.Validate(); err != nil {
			return err
		}
		var err error
		sum, err = addUint64(sum, d.EncodedLength)
		if err != nil {
			return fmt.Errorf("%w: encoded lengths of event ordinal %d: %w", ErrFragments, event.Ordinal, err)
		}
	}
	if sum != event.RecordLength {
		return fmt.Errorf("%w: encoded lengths sum %d, record_length %d",
			ErrFragments, sum, event.RecordLength)
	}
	return nil
}

// RecordDigest computes the record digest over the canonical fragment bytes
// in index order, excluding the final seal fragment so the digest cannot
// reference itself (MESSAGE_CONTRACT section 6.1, D73):
//
//	SHA256(UTF8("extractor.record.v1") || 0x00 ||
//	    concat(u64be(index) || u64be(length(B_i)) || B_i) for i in 0..K-2)
//
// fragments must hold at least the header, one content fragment and the
// seal; every fragment must be inside the 1..65536 bound.
func RecordDigest(fragments [][]byte) (Hash32, error) {
	if len(fragments) < minimumFragmentCount {
		return Hash32{}, fmt.Errorf("%w: %d fragments below the minimum %d",
			ErrFragments, len(fragments), minimumFragmentCount)
	}
	for i, frag := range fragments {
		if len(frag) == 0 || len(frag) > FragmentMaxBytes {
			return Hash32{}, fmt.Errorf("%w: fragment %d length %d outside 1..%d",
				ErrFragments, i, len(frag), FragmentMaxBytes)
		}
	}
	var out Hash32
	h := sha256.New()
	h.Write([]byte(recordDigestDomain))
	h.Write([]byte{0})
	var prefix [16]byte
	for i := 0; i < len(fragments)-1; i++ { // excludes the seal
		binary.BigEndian.PutUint64(prefix[0:8], uint64(i))
		binary.BigEndian.PutUint64(prefix[8:16], uint64(len(fragments[i])))
		h.Write(prefix[:])
		h.Write(fragments[i])
	}
	h.Sum(out[:0])
	return out, nil
}

// FragmentManifestDigest computes
//
//	SHA256(UTF8("extractor.fragments.v1") || 0x00 || event_id40 ||
//	    fragment_count_u64be || fragment_descriptors_in_index_order)
//
// The preimage never includes the digest itself, so the computation is not
// self-referential.
func FragmentManifestDigest(eventID EventID, frags []FragmentDescriptor) (Hash32, error) {
	if err := eventID.Validate(); err != nil {
		return Hash32{}, err
	}
	if len(frags) == 0 {
		return Hash32{}, fmt.Errorf("%w: empty fragment descriptor list", ErrFragments)
	}
	for i := range frags {
		if frags[i].FragmentIndex != uint64(i) {
			return Hash32{}, fmt.Errorf("%w: fragment index %d at position %d breaks continuity",
				ErrFragments, frags[i].FragmentIndex, i)
		}
		if err := frags[i].Validate(); err != nil {
			return Hash32{}, err
		}
	}
	var out Hash32
	h := sha256.New()
	h.Write([]byte(fragmentDomain))
	h.Write([]byte{0})
	h.Write(eventID[:])
	var count [8]byte
	u64be(count[:0], uint64(len(frags)))
	h.Write(count[:])
	for _, d := range frags {
		h.Write(d.Encode())
	}
	h.Sum(out[:0])
	return out, nil
}

// BuildEventDescriptor assembles one event's READY descriptors from its
// identity, registry values and canonical fragment bytes: it computes every
// fragment digest, the checked record_length over all fragments (the seal
// included), the record digest over the content fragments (the seal
// excluded) and the fragment manifest digest. The result satisfies
// ValidateFragments by construction; the input fragments themselves are
// checked against the same bounds first.
func BuildEventDescriptor(ordinal uint64, eventID EventID, family Family, kind Kind,
	fragments [][]byte) (EventDescriptor, []FragmentDescriptor, error) {
	if !family.Valid() {
		return EventDescriptor{}, nil, fmt.Errorf("%w: event family %d is not in registry v1",
			ErrClosedRegistry, uint16(family))
	}
	if !kind.Valid() {
		return EventDescriptor{}, nil, fmt.Errorf("%w: event kind %d is not in registry v1",
			ErrClosedRegistry, uint16(kind))
	}
	recordDigest, err := RecordDigest(fragments)
	if err != nil {
		return EventDescriptor{}, nil, err
	}
	frags := make([]FragmentDescriptor, len(fragments))
	var recordLength uint64
	for i, frag := range fragments {
		var sha Hash32
		sum := sha256.Sum256(frag)
		copy(sha[:], sum[:])
		frags[i] = FragmentDescriptor{FragmentIndex: uint64(i),
			EncodedLength: uint64(len(frag)), FragmentSHA256: sha}
		recordLength, err = addUint64(recordLength, uint64(len(frag)))
		if err != nil {
			return EventDescriptor{}, nil, fmt.Errorf("%w: record length of event ordinal %d: %w",
				ErrOverflow, ordinal, err)
		}
	}
	manifestDigest, err := FragmentManifestDigest(eventID, frags)
	if err != nil {
		return EventDescriptor{}, nil, err
	}
	event := EventDescriptor{Ordinal: ordinal, EventID: eventID, Family: family, Kind: kind,
		RecordLength: recordLength, RecordDigest: recordDigest,
		FragmentCount: uint64(len(fragments)), FragmentManifestDigest: manifestDigest}
	if err := ValidateFragments(event, frags); err != nil {
		return EventDescriptor{}, nil, err
	}
	return event, frags, nil
}

// beUint64 decodes 8 big-endian bytes.
func beUint64(b []byte) uint64 {
	return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
}

// beUint16 decodes 2 big-endian bytes.
func beUint16(b []byte) uint16 {
	return uint16(b[0])<<8 | uint16(b[1])
}
