// The ManifestBuilder composes the READY descriptors of one closed decision
// batch into the chunked manifest bytes of the "extractor.manifest.v2"
// domain and the batch-level transport layout plan.
//
// Manifest bytes (CONTROL_WIRE_CONTRACT, "READY manifest and reservation"):
//
//	UTF8("extractor.manifest.v2") || 0x00 || event_count_u64be ||
//	    concat(event_descriptor || its_fragment_descriptors)
//
// in ordinal order, every descriptor in its fixed-order big-endian encoding
// from protocol/proto. The manifest digest is SHA-256 of these bytes.
// Ordinals are continuous from zero, event ids are unique and an empty
// public batch produces no manifest. The descriptor stream is emitted and
// chunked piece by piece — a chunk boundary may fall inside one fixed-size
// descriptor record — so no full manifest buffer is required; the delivery
// RPC flow around it (PrepareChunk/FinalizePrepare) is E03 territory.
package codec

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// ManifestDomain opens the manifest bytes of a closed batch.
const ManifestDomain = "extractor.manifest.v2"

// manifestEvent is one event of the batch with its fragment descriptors.
type manifestEvent struct {
	event proto.EventDescriptor
	frags []proto.FragmentDescriptor
}

// ManifestBuilder accumulates the READY descriptors of one batch in ordinal
// order and produces the manifest bytes, digest, chunks and the batch
// layout. The same descriptors always produce byte-identical output.
type ManifestBuilder struct {
	events          []manifestEvent
	seenIDs         map[proto.EventID]struct{}
	descriptorBytes uint64
}

// NewManifestBuilder returns an empty builder.
func NewManifestBuilder() *ManifestBuilder {
	return &ManifestBuilder{seenIDs: make(map[proto.EventID]struct{})}
}

// AddEvent appends one event's descriptors in ordinal order. The event
// descriptor must pass proto.ValidateFragments with its fragment
// descriptors; ordinals must be continuous from zero and event ids unique.
func (b *ManifestBuilder) AddEvent(event proto.EventDescriptor, frags []proto.FragmentDescriptor) error {
	if err := proto.ValidateFragments(event, frags); err != nil {
		return fmt.Errorf("%w: %w", ErrFragments, err)
	}
	if n := len(b.events); n > 0 {
		want, err := addCount(b.events[n-1].event.Ordinal, 1)
		if err != nil {
			return err
		}
		if event.Ordinal != want {
			return fmt.Errorf("%w: event ordinal %d breaks continuity, want %d",
				ErrFragments, event.Ordinal, want)
		}
	}
	if _, seen := b.seenIDs[event.EventID]; seen {
		return fmt.Errorf("%w: event id repeats in the batch", ErrFragments)
	}
	b.seenIDs[event.EventID] = struct{}{}
	b.events = append(b.events, manifestEvent{event: event, frags: frags})
	count, err := addCount(uint64(proto.EventDescriptorSize),
		uint64(len(frags))*uint64(proto.FragmentDescriptorSize))
	if err != nil {
		return err
	}
	b.descriptorBytes, err = addCount(b.descriptorBytes, count)
	if err != nil {
		return err
	}
	return nil
}

// EventCount returns the number of events added.
func (b *ManifestBuilder) EventCount() uint64 { return uint64(len(b.events)) }

// DescriptorBytesTotal returns the checked sum of the descriptor encodings —
// the descriptor_bytes_total a BeginPrepare carries.
func (b *ManifestBuilder) DescriptorBytesTotal() uint64 { return b.descriptorBytes }

// forEachPiece streams the manifest bytes piece by piece: the domain, the
// zero byte, the event count, then every descriptor encoding in ordinal
// order. Pieces never exceed one descriptor encoding, so a consumer with a
// bounded buffer stays bounded.
func (b *ManifestBuilder) forEachPiece(fn func(piece []byte) error) error {
	if len(b.events) == 0 {
		return fmt.Errorf("%w: an empty public batch produces no manifest", ErrFragments)
	}
	if err := fn([]byte(ManifestDomain)); err != nil {
		return err
	}
	if err := fn([]byte{0}); err != nil {
		return err
	}
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(b.events)))
	if err := fn(count[:]); err != nil {
		return err
	}
	for i := range b.events {
		encoded, err := b.events[i].event.Encode()
		if err != nil {
			return err
		}
		if err := fn(encoded); err != nil {
			return err
		}
		for j := range b.events[i].frags {
			if err := fn(b.events[i].frags[j].Encode()); err != nil {
				return err
			}
		}
	}
	return nil
}

// Digest returns the SHA-256 of the manifest bytes, computed streaming.
func (b *ManifestBuilder) Digest() (proto.Hash32, error) {
	var out proto.Hash32
	if len(b.events) == 0 {
		return out, fmt.Errorf("%w: an empty public batch produces no manifest", ErrFragments)
	}
	h := sha256.New()
	err := b.forEachPiece(func(piece []byte) error {
		_, err := h.Write(piece)
		return err
	})
	if err != nil {
		return out, err
	}
	copy(out[:], h.Sum(nil))
	return out, nil
}

// Bytes returns the full manifest bytes. Prefer Digest or Chunks for large
// batches; this convenience materializes the whole stream.
func (b *ManifestBuilder) Bytes() ([]byte, error) {
	var out []byte
	err := b.forEachPiece(func(piece []byte) error {
		out = append(out, piece...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ManifestChunk is one bounded piece of the manifest byte stream: its batch
// offset, its SHA-256 and its bytes — the bytes a PrepareChunk carries with
// its offset and chunk digest.
type ManifestChunk struct {
	Offset uint64
	SHA256 proto.Hash32
	Bytes  []byte
}

// ManifestChunker yields the manifest bytes in bounded chunks. A chunk
// boundary may fall inside one fixed-size descriptor encoding.
type ManifestChunker struct {
	pieces [][]byte
	total  uint64
	max    uint64
	offset uint64
}

// Chunks returns a chunker over the manifest bytes with chunks of at most
// max bytes. The chunker slices the deterministic manifest stream by byte
// offset; each byte is copied exactly once and no chunk exceeds max.
func (b *ManifestBuilder) Chunks(max uint64) (*ManifestChunker, error) {
	if len(b.events) == 0 {
		return nil, fmt.Errorf("%w: an empty public batch produces no manifest", ErrFragments)
	}
	if max < uint64(proto.EventDescriptorSize) {
		return nil, fmt.Errorf("%w: chunk size %d below the %d-byte event descriptor encoding",
			ErrInvalidValue, max, proto.EventDescriptorSize)
	}
	c := &ManifestChunker{max: max, pieces: make([][]byte, 0, 3+2*len(b.events))}
	var total uint64
	err := b.forEachPiece(func(piece []byte) error {
		c.pieces = append(c.pieces, piece)
		var addErr error
		total, addErr = addCount(total, uint64(len(piece)))
		return addErr
	})
	if err != nil {
		return nil, err
	}
	c.total = total
	return c, nil
}

// Next returns the next chunk, or ok=false at the end of the stream.
func (c *ManifestChunker) Next() (ManifestChunk, bool, error) {
	if c.offset >= c.total {
		return ManifestChunk{}, false, nil
	}
	end := c.offset + c.max
	if end < c.offset || end > c.total {
		end = c.total
	}
	chunk := ManifestChunk{Offset: c.offset, Bytes: make([]byte, 0, end-c.offset)}
	// Walk the pieces over the byte range [offset, end).
	position := uint64(0)
	for _, piece := range c.pieces {
		pieceStart, pieceEnd := position, position+uint64(len(piece))
		if pieceEnd <= c.offset {
			position = pieceEnd
			continue
		}
		if pieceStart >= end {
			break
		}
		from := c.offset
		if pieceStart > from {
			from = pieceStart
		}
		to := end
		if pieceEnd < to {
			to = pieceEnd
		}
		chunk.Bytes = append(chunk.Bytes, piece[from-position:to-position]...)
		position = pieceEnd
	}
	chunk.SHA256 = sha256Of(chunk.Bytes)
	c.offset = end
	return chunk, true, nil
}

// ManifestHeaderBytes is the fixed part of the manifest stream before the
// first descriptor: domain, zero byte, event count. The domain holds 21
// bytes, so the header holds 30. The descriptor bytes total of a manifest
// is its length minus this value.
const ManifestHeaderBytes = uint64(len(ManifestDomain) + 1 + 8)

// ManifestEvent is one event of a decoded manifest: its event descriptor
// and, in fragment index order, its fragment descriptors (encoded length
// and SHA-256 per fragment).
type ManifestEvent struct {
	Ordinal      uint64
	EventID      proto.EventID
	Family, Kind uint16
	RecordLength uint64
	RecordDigest proto.Hash32
	Fragments    []proto.FragmentDescriptor
}

// DecodeManifest parses the bytes of ManifestBuilder.Bytes() back into its
// events, in ordinal order. It accepts only what the builder produces:
//
//   - the domain, the zero byte and an event count of at least one: an
//     empty public batch produces no manifest;
//   - for each event, its event descriptor and exactly its fragment_count
//     fragment descriptors, each passing the descriptor rules of
//     protocol/proto, with indices continuous from zero and lengths that
//     sum to record_length;
//   - ordinals continuous from zero and event ids unique in the batch;
//   - no byte after the last descriptor.
//
// Every rejection wraps one named sentinel: ErrInvalidValue for a wrong
// domain or a bad descriptor, ErrMalformed for a missing zero byte or for
// bytes after the last event that form no event, ErrTruncated for a stream
// that ends inside the header or a descriptor, and ErrFragments when the
// event count does not match the events the stream holds or a manifest
// rule is broken.
func DecodeManifest(data []byte) ([]ManifestEvent, error) {
	if uint64(len(data)) < uint64(len(ManifestDomain)) {
		return nil, fmt.Errorf("%w: manifest of %d bytes is shorter than the %d-byte domain",
			ErrTruncated, len(data), len(ManifestDomain))
	}
	if !bytes.HasPrefix(data, []byte(ManifestDomain)) {
		return nil, fmt.Errorf("%w: manifest domain %q, want %q", ErrInvalidValue,
			data[:len(ManifestDomain)], ManifestDomain)
	}
	if uint64(len(data)) < ManifestHeaderBytes {
		return nil, fmt.Errorf("%w: manifest of %d bytes ends inside the %d-byte header",
			ErrTruncated, len(data), ManifestHeaderBytes)
	}
	if data[len(ManifestDomain)] != 0 {
		return nil, fmt.Errorf("%w: byte after the manifest domain is %d, want 0",
			ErrMalformed, data[len(ManifestDomain)])
	}
	count := binary.BigEndian.Uint64(data[ManifestHeaderBytes-8 : ManifestHeaderBytes])
	if count == 0 {
		return nil, fmt.Errorf("%w: manifest of %d bytes declares zero events; an empty public batch produces no manifest",
			ErrFragments, len(data))
	}
	// An event takes at least its descriptor, so the stream length bounds
	// the events it can hold; the declared count is not trusted with an
	// allocation.
	events := make([]ManifestEvent, 0, uint64(len(data)/proto.EventDescriptorSize))
	seen := make(map[proto.EventID]struct{}, cap(events))
	pos := ManifestHeaderBytes
	for uint64(len(events)) < count {
		event, next, err := readManifestEvent(data, pos, uint64(len(events)))
		if err != nil {
			return nil, err
		}
		if event.Ordinal != uint64(len(events)) {
			return nil, fmt.Errorf("%w: event %d carries ordinal %d", ErrFragments, len(events), event.Ordinal)
		}
		if _, dup := seen[event.EventID]; dup {
			return nil, fmt.Errorf("%w: event id repeats in the batch at ordinal %d", ErrFragments, event.Ordinal)
		}
		seen[event.EventID] = struct{}{}
		events = append(events, event)
		pos = next
	}
	if pos != uint64(len(data)) {
		// Bytes after the declared events are one more event the count
		// does not name, or bytes that form no event.
		if _, _, err := readManifestEvent(data, pos, count); err == nil {
			return nil, fmt.Errorf("%w: manifest declares %d events and holds more", ErrFragments, count)
		}
		return nil, fmt.Errorf("%w: %d bytes after the last descriptor of %d events",
			ErrMalformed, uint64(len(data))-pos, count)
	}
	return events, nil
}

// readManifestEvent parses the event at pos, its event descriptor and then
// its fragment descriptors, and returns the position after them. index is
// the position of the event in the stream; it only names the event in an
// error.
func readManifestEvent(data []byte, pos, index uint64) (ManifestEvent, uint64, error) {
	end := uint64(len(data))
	if end-pos < uint64(proto.EventDescriptorSize) {
		if end == pos {
			return ManifestEvent{}, 0, fmt.Errorf("%w: manifest ends after %d events", ErrFragments, index)
		}
		return ManifestEvent{}, 0, fmt.Errorf("%w: event descriptor %d holds %d bytes, want %d",
			ErrTruncated, index, end-pos, proto.EventDescriptorSize)
	}
	descriptor, err := proto.DecodeEventDescriptor(data[pos : pos+uint64(proto.EventDescriptorSize)])
	if err != nil {
		return ManifestEvent{}, 0, fmt.Errorf("%w: event descriptor %d: %w", ErrInvalidValue, index, err)
	}
	pos += uint64(proto.EventDescriptorSize)
	// The fragment count is compared with the bytes left before anything
	// is allocated for it.
	if descriptor.FragmentCount > (end-pos)/uint64(proto.FragmentDescriptorSize) {
		return ManifestEvent{}, 0, fmt.Errorf("%w: event %d declares %d fragment descriptors and the manifest holds bytes for %d",
			ErrTruncated, index, descriptor.FragmentCount, (end-pos)/uint64(proto.FragmentDescriptorSize))
	}
	frags := make([]proto.FragmentDescriptor, descriptor.FragmentCount)
	for i := range frags {
		frag, err := proto.DecodeFragmentDescriptor(data[pos : pos+uint64(proto.FragmentDescriptorSize)])
		if err != nil {
			return ManifestEvent{}, 0, fmt.Errorf("%w: fragment descriptor %d of event %d: %w",
				ErrInvalidValue, i, index, err)
		}
		frags[i] = frag
		pos += uint64(proto.FragmentDescriptorSize)
	}
	if err := proto.ValidateFragments(descriptor, frags); err != nil {
		return ManifestEvent{}, 0, fmt.Errorf("%w: fragments of event %d: %w", ErrInvalidValue, index, err)
	}
	return ManifestEvent{Ordinal: descriptor.Ordinal, EventID: descriptor.EventID,
		Family: uint16(descriptor.Family), Kind: uint16(descriptor.Kind),
		RecordLength: descriptor.RecordLength, RecordDigest: descriptor.RecordDigest,
		Fragments: frags}, pos, nil
}

// LayoutRequest fills a proto.LayoutRequest for this batch: base supplies
// the budget, stream, network, epoch and sequence base; the events with
// their fragment lengths come from the added descriptors in ordinal order.
func (b *ManifestBuilder) LayoutRequest(base proto.LayoutRequest) (proto.LayoutRequest, error) {
	if len(base.Events) != 0 {
		return proto.LayoutRequest{}, fmt.Errorf("%w: layout request already carries events",
			ErrInvalidValue)
	}
	if len(b.events) == 0 {
		return proto.LayoutRequest{}, fmt.Errorf("%w: layout of an empty batch", ErrInvalidValue)
	}
	out := base
	out.Events = make([]proto.EventFragments, len(b.events))
	for i := range b.events {
		lengths := make([]uint64, len(b.events[i].frags))
		for j := range b.events[i].frags {
			lengths[j] = b.events[i].frags[j].EncodedLength
		}
		out.Events[i] = proto.EventFragments{Ordinal: b.events[i].event.Ordinal,
			FragmentLengths: lengths}
	}
	return out, nil
}

// BuildLayout plans the transport of the whole batch through
// proto.ComputeLayout and returns the layout manifest entries in
// ordinal/fragment order together with the layout manifest digest over
// them. planDigest is the reservation plan digest of the batch (E03
// territory); the digest formula itself is protocol/proto's.
func (b *ManifestBuilder) BuildLayout(base proto.LayoutRequest,
	planDigest proto.Hash32) ([]proto.LayoutManifestEntry, proto.Hash32, error) {
	req, err := b.LayoutRequest(base)
	if err != nil {
		return nil, proto.Hash32{}, err
	}
	plan, err := proto.ComputeLayout(req)
	if err != nil {
		return nil, proto.Hash32{}, err
	}
	digest, err := proto.LayoutManifestDigest(planDigest, plan.Entries)
	if err != nil {
		return nil, proto.Hash32{}, err
	}
	return plan.Entries, digest, nil
}
