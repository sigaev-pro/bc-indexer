package ingestclient

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// The manifest reader of the plan step. The driver parses the stored
// manifest bytes (the bytes of codec.ManifestBuilder.Bytes) back into the
// events and fragment descriptors the transport plan is built from.
//
// DECISION (contract reference: CONTROL_WIRE_CONTRACT, "READY manifest and
// reservation"): the manifest reader of the codec, codec.DecodeManifest,
// belongs to the schema registry block and is not merged at the commit this
// file was written against. The driver must not wait for it, so every
// manifest parse sits behind the one function decodeManifest below, built on
// the descriptor decoders of protocol/proto. When codec.DecodeManifest is
// merged, decodeManifest becomes a call to it and the walker here is
// deleted; nothing else in the driver changes.

// manifestEvent is one event of a decoded manifest, in the shape of
// codec.ManifestEvent: the identity the frame wrapper carries and the
// fragments of the event in index order.
type manifestEvent struct {
	Ordinal      uint64
	EventID      proto.EventID
	Family, Kind uint16
	RecordLength uint64
	RecordDigest proto.Hash32
	Fragments    []proto.FragmentDescriptor
}

// manifestHeaderBytes is the fixed part of the manifest stream before the
// first descriptor: the domain, the zero byte and the event count.
const manifestHeaderBytes = uint64(len(codec.ManifestDomain) + 1 + 8)

// ErrManifest rejects stored manifest bytes that are not the
// "extractor.manifest.v2" stream of a well-formed batch.
var errManifest = fmt.Errorf("%w: invalid manifest bytes", ErrJournalData)

// decodeManifest parses the bytes of codec.ManifestBuilder.Bytes() back into
// its events. It accepts exactly that stream: the domain, the zero byte, the
// event count, then per event its descriptor and its fragment descriptors in
// index order, with nothing after the last one.
func decodeManifest(data []byte) ([]manifestEvent, error) {
	if len(data) < int(manifestHeaderBytes) ||
		string(data[:len(codec.ManifestDomain)]) != codec.ManifestDomain ||
		data[len(codec.ManifestDomain)] != 0 {
		return nil, fmt.Errorf("%w: domain or zero byte differs", errManifest)
	}
	count := binary.BigEndian.Uint64(data[len(codec.ManifestDomain)+1:])
	if count == 0 {
		return nil, fmt.Errorf("%w: an empty public batch produces no manifest", errManifest)
	}
	// The event count cannot exceed what the remaining bytes can hold; the
	// check keeps the allocation below bounded by the input.
	if count > uint64(len(data)-int(manifestHeaderBytes))/proto.EventDescriptorSize {
		return nil, fmt.Errorf("%w: event count %d, bytes for fewer", errManifest, count)
	}
	events := make([]manifestEvent, 0, count)
	p := int(manifestHeaderBytes)
	seen := make(map[proto.EventID]struct{}, count)
	for i := uint64(0); i < count; i++ {
		if len(data)-p < proto.EventDescriptorSize {
			return nil, fmt.Errorf("%w: event %d has no descriptor", errManifest, i)
		}
		event, err := proto.DecodeEventDescriptor(data[p : p+proto.EventDescriptorSize])
		if err != nil {
			return nil, fmt.Errorf("%w: event %d: %v", errManifest, i, err)
		}
		p += proto.EventDescriptorSize
		if event.Ordinal != i {
			return nil, fmt.Errorf("%w: event ordinal %d at position %d breaks continuity", errManifest, event.Ordinal, i)
		}
		if _, dup := seen[event.EventID]; dup {
			return nil, fmt.Errorf("%w: event id repeats in the batch", errManifest)
		}
		seen[event.EventID] = struct{}{}
		frags := make([]proto.FragmentDescriptor, event.FragmentCount)
		for j := range frags {
			if len(data)-p < proto.FragmentDescriptorSize {
				return nil, fmt.Errorf("%w: event %d fragment %d has no descriptor", errManifest, i, j)
			}
			frag, err := proto.DecodeFragmentDescriptor(data[p : p+proto.FragmentDescriptorSize])
			if err != nil {
				return nil, fmt.Errorf("%w: event %d fragment %d: %v", errManifest, i, j, err)
			}
			p += proto.FragmentDescriptorSize
			frags[j] = frag
		}
		if err := proto.ValidateFragments(event, frags); err != nil {
			return nil, fmt.Errorf("%w: event %d: %v", errManifest, i, err)
		}
		events = append(events, manifestEvent{
			Ordinal:      event.Ordinal,
			EventID:      event.EventID,
			Family:       uint16(event.Family),
			Kind:         uint16(event.Kind),
			RecordLength: event.RecordLength,
			RecordDigest: event.RecordDigest,
			Fragments:    frags,
		})
	}
	if p != len(data) {
		return nil, fmt.Errorf("%w: %d trailing bytes", errManifest, len(data)-p)
	}
	return events, nil
}

// verifyManifest checks the manifest bytes against the READY marker of their
// batch: the length and the SHA-256 must equal the marker, and the event
// count must equal the descriptors the stream holds.
func verifyManifest(data []byte, ready producer.Ready) error {
	if uint64(len(data)) != ready.ManifestLength {
		return fmt.Errorf("%w: manifest of %d bytes, READY marker names %d", ErrJournalData, len(data), ready.ManifestLength)
	}
	digest := proto.Hash32(sha256.Sum256(data))
	if digest != ready.ManifestDigest {
		return fmt.Errorf("%w: manifest digest %x, READY marker names %x", ErrJournalData, digest, ready.ManifestDigest)
	}
	events, err := decodeManifest(data)
	if err != nil {
		return err
	}
	if uint64(len(events)) != ready.EventCount {
		return fmt.Errorf("%w: manifest holds %d events, READY marker names %d", ErrJournalData, len(events), ready.EventCount)
	}
	return nil
}
