package ingestclient

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/adapters/ingestclient/ingest"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// The transport plan of one batch (step 6 of the call order): the layout
// the reservation budget allows, the frames it produces over the immutable
// attempt data, and the plan payload the producer journal spools.

// planFrame is one transport frame of a plan, in layout order.
type planFrame struct {
	// Entry is the layout entry the frame executes; one entry holds a
	// COMPLETE frame or the PART frames of one fragment.
	Entry proto.LayoutManifestEntry
	// Part is the part index of a PART frame; 0 for COMPLETE.
	Part uint64
	// Encoded are the canonical TransportFrame bytes
	// (codec.EmittedFrame.Encoded).
	Encoded []byte
	// Header holds the exact NATS header bytes of the frame.
	Header []byte
}

// address returns the frame address of the wire: event ordinal, fragment
// index and frame index (0 for COMPLETE, the part index for PART).
func (f *planFrame) address() (ordinal, fragment, frame uint64) {
	return f.Entry.Ordinal, f.Entry.FragmentIndex, f.Part
}

// meta returns the declaration the first chunk of the frame carries, from
// the layout entry.
func (f *planFrame) meta() *ingest.FrameMeta {
	if f.Entry.Form == proto.FormComplete {
		return &ingest.FrameMeta{Form: uint32(proto.FormComplete), Capacity: f.Entry.FragmentTotalBytes,
			PartCount: 1, FragmentTotalBytes: f.Entry.FragmentTotalBytes}
	}
	return &ingest.FrameMeta{Form: uint32(proto.FormPart), Capacity: f.Entry.Capacity,
		PartCount: f.Entry.PartCount, FragmentTotalBytes: f.Entry.FragmentTotalBytes}
}

// limitError carries a frame the transport budget cannot carry as
// LIMIT_INCOMPATIBLE; every other error passes through.
func limitError(err error) error {
	if errors.Is(err, proto.ErrLimitIncompatible) || errors.Is(err, codec.ErrLimitIncompatible) {
		return fmt.Errorf("%w: %v", ErrLimitIncompatible, err)
	}
	return err
}

// layout computes the deterministic transport layout of one batch from the
// reservation budget and the binding: one entry per (event ordinal,
// fragment index) pair, in ordinal and fragment order.
func layout(events []manifestEvent, seed Seed, binding destination.Binding) (*proto.LayoutPlan, error) {
	req := proto.LayoutRequest{Budget: seed.FrameLimit, Stream: binding.Stream,
		Network: binding.Network, PublicationEpoch: seed.IssuerEpoch, SequenceBase: seed.SequenceBase}
	for _, event := range events {
		lengths := make([]uint64, len(event.Fragments))
		for i := range event.Fragments {
			lengths[i] = event.Fragments[i].EncodedLength
		}
		req.Events = append(req.Events, proto.EventFragments{Ordinal: event.Ordinal, FragmentLengths: lengths})
	}
	plan, err := proto.ComputeLayout(req)
	if err != nil {
		return nil, limitError(err)
	}
	return plan, nil
}

// buildFrames wraps every fragment of the attempt data in its transport
// frame, following the layout entries in order. Before anything is sealed
// it checks the digest of each fragment against its READY descriptor
// (DELIVERY_STATE_MACHINE, "Prepare и повторные попытки": the producer
// verifies the actual bytes before SEALED). data is the attempt data stream:
// the RecordFragment bytes of every event in ordinal order, each event its
// fragments in index order.
func buildFrames(events []manifestEvent, plan *proto.LayoutPlan, data []byte, seed Seed, binding destination.Binding) ([]planFrame, error) {
	frames := make([]planFrame, 0, len(plan.Entries))
	var pos uint64
	for i := range events {
		event := &events[i]
		writer, err := codec.NewFragmentWriter(plan, codec.WriterConfig{
			Network:          binding.Network,
			EventID:          event.EventID,
			RecordDigest:     event.RecordDigest,
			PublicationEpoch: seed.IssuerEpoch,
			FragmentCount:    uint64(len(event.Fragments)),
		})
		if err != nil {
			return nil, err
		}
		chain, err := codec.ChainSequence(seed.SequenceBase, event.Ordinal)
		if err != nil {
			return nil, err
		}
		for j := range event.Fragments {
			descriptor := event.Fragments[j]
			end := pos + descriptor.EncodedLength
			if end < pos || end > uint64(len(data)) {
				return nil, fmt.Errorf("%w: event %d fragment %d needs %d bytes at %d, the attempt holds %d",
					ErrJournalData, event.Ordinal, j, descriptor.EncodedLength, pos, len(data))
			}
			fragment := data[pos:end]
			if proto.Hash32(sha256.Sum256(fragment)) != descriptor.FragmentSHA256 {
				return nil, fmt.Errorf("%w: event %d fragment %d differs from its READY digest",
					ErrJournalData, event.Ordinal, j)
			}
			pos = end
			if len(frames) >= len(plan.Entries) {
				return nil, fmt.Errorf("%w: layout holds %d entries for more fragments", ErrJournalData, len(plan.Entries))
			}
			entry := plan.Entries[len(frames)]
			if entry.Ordinal != event.Ordinal || entry.FragmentIndex != uint64(j) {
				return nil, fmt.Errorf("%w: layout entry (ordinal %d, fragment %d) is not the next fragment",
					ErrJournalData, entry.Ordinal, entry.FragmentIndex)
			}
			err = writer.Write(entry, chain, fragment, func(ef *codec.EmittedFrame) error {
				frames = append(frames, planFrame{Entry: ef.Entry, Part: ef.PartIndex,
					Encoded: append([]byte(nil), ef.Encoded...), Header: append([]byte(nil), ef.NATSHeader...)})
				return nil
			})
			if err != nil {
				return nil, limitError(err)
			}
		}
	}
	if pos != uint64(len(data)) {
		return nil, fmt.Errorf("%w: %d attempt bytes follow the last fragment", ErrJournalData, uint64(len(data))-pos)
	}
	if len(frames) < len(plan.Entries) {
		return nil, fmt.Errorf("%w: layout holds %d entries, the frames cover %d", ErrJournalData, len(plan.Entries), len(frames))
	}
	return frames, nil
}

// encodePlan renders the plan payload the producer journal spools: one
// frame record per frame, in layout order, each
// u32be(len(header)) || u32be(len(frame)) || header || frame.
func encodePlan(frames []planFrame) []byte {
	var out []byte
	for i := range frames {
		var sizes [8]byte
		binary.BigEndian.PutUint32(sizes[0:4], uint32(len(frames[i].Header)))
		binary.BigEndian.PutUint32(sizes[4:8], uint32(len(frames[i].Encoded)))
		out = append(out, sizes[:]...)
		out = append(out, frames[i].Header...)
		out = append(out, frames[i].Encoded...)
	}
	return out
}

// storedFrame is one frame record of a spooled plan.
type storedFrame struct {
	Header  []byte
	Encoded []byte
}

// decodePlan parses a plan payload back into its frame records, in layout
// order. A truncated record or a length above the payload is corruption of
// the stored plan.
func decodePlan(payload []byte) ([]storedFrame, error) {
	var frames []storedFrame
	for len(payload) > 0 {
		if len(payload) < 8 {
			return nil, fmt.Errorf("%w: plan record of %d bytes, want at least 8", ErrJournalData, len(payload))
		}
		headerLen, frameLen := binary.BigEndian.Uint32(payload[0:4]), binary.BigEndian.Uint32(payload[4:8])
		rest := uint64(len(payload) - 8)
		if need := uint64(headerLen) + uint64(frameLen); need > rest {
			return nil, fmt.Errorf("%w: plan record needs %d bytes, %d follow", ErrJournalData, need, rest)
		}
		record := payload[8 : 8+headerLen+frameLen]
		frames = append(frames, storedFrame{
			Header:  append([]byte(nil), record[:headerLen]...),
			Encoded: append([]byte(nil), record[headerLen:]...),
		})
		payload = payload[8+headerLen+frameLen:]
	}
	return frames, nil
}

// frameCount returns the number of frames one layout entry holds.
func frameCount(entry proto.LayoutManifestEntry) uint64 {
	if entry.Form == proto.FormComplete {
		return 1
	}
	return entry.PartCount
}

// checkPlan verifies the spooled plan of a sealed batch against what its
// manifest, its reservation and the binding determine, and answers the
// frames to publish in layout order. After SEALED the plan is immutable, so
// the check reads it and changes nothing: every frame record must be the
// canonical TransportFrame of its fragment under the reservation, with the
// exact NATS header bytes, inside the frame budget.
func checkPlan(stored []storedFrame, events []manifestEvent, plan *proto.LayoutPlan,
	seed Seed, binding destination.Binding) ([]planFrame, error) {
	networkDigest, err := binding.Network.Digest()
	if err != nil {
		return nil, err
	}
	frames := make([]planFrame, 0, len(stored))
	entryIndex, storedIndex := 0, 0
	for i := range events {
		event := &events[i]
		chain, err := codec.ChainSequence(seed.SequenceBase, event.Ordinal)
		if err != nil {
			return nil, err
		}
		for j := range event.Fragments {
			if entryIndex >= len(plan.Entries) {
				return nil, fmt.Errorf("%w: plan holds no entry for event %d fragment %d", ErrJournalData, event.Ordinal, j)
			}
			entry := plan.Entries[entryIndex]
			entryIndex++
			for part := uint64(0); part < frameCount(entry); part++ {
				if storedIndex >= len(stored) {
					return nil, fmt.Errorf("%w: plan holds %d frames, the layout needs more", ErrJournalData, len(stored))
				}
				frame := planFrame{Entry: entry, Part: part,
					Encoded: stored[storedIndex].Encoded, Header: stored[storedIndex].Header}
				storedIndex++
				if err := checkFrame(&frame, event, j, chain, networkDigest, binding, seed, seed.FrameLimit); err != nil {
					return nil, fmt.Errorf("%w: frame %d: %v", ErrJournalData, storedIndex-1, err)
				}
				frames = append(frames, frame)
			}
		}
	}
	if entryIndex != len(plan.Entries) || storedIndex != len(stored) {
		return nil, fmt.Errorf("%w: plan holds %d frames for %d of %d layout entries",
			ErrJournalData, len(stored), entryIndex, len(plan.Entries))
	}
	return frames, nil
}

// checkFrame requires one spooled frame to be the canonical frame of its
// fragment: the wrapper identity, the form of its layout entry and the NATS
// header bytes.
func checkFrame(frame *planFrame, event *manifestEvent, fragmentIndex int, chain uint64,
	networkDigest proto.Hash32, binding destination.Binding, epoch Seed, frameLimit uint64) error {
	decoded, err := proto.DecodeTransportFrame(frame.Encoded)
	if err != nil {
		return err
	}
	descriptor := event.Fragments[fragmentIndex]
	switch {
	case decoded.EventID != event.EventID:
		return errors.New("another event id than the manifest")
	case decoded.RecordDigest != event.RecordDigest:
		return errors.New("another record digest than the manifest")
	case decoded.PublicationEpoch != epoch.IssuerEpoch:
		return errors.New("another publication epoch than the reservation")
	case decoded.ChainSequence != chain:
		return errors.New("another chain sequence than the reservation")
	case decoded.FragmentIndex != uint64(fragmentIndex):
		return errors.New("another fragment index than its position")
	case decoded.FragmentCount != uint64(len(event.Fragments)):
		return errors.New("another fragment count than the manifest")
	case decoded.FragmentDigest != descriptor.FragmentSHA256:
		return errors.New("another fragment digest than the READY descriptor")
	case decoded.Network != binding.Network:
		return errors.New("another network than the binding")
	}
	if frame.Entry.Form == proto.FormComplete {
		frame.Part = 0
		if decoded.Complete == nil || uint64(len(decoded.Complete.FragmentBytes)) != frame.Entry.FragmentTotalBytes {
			return errors.New("not the COMPLETE frame of its layout entry")
		}
	} else if decoded.Part == nil || decoded.Part.PartIndex != frame.Part ||
		decoded.Part.PartCount != frame.Entry.PartCount ||
		decoded.Part.FragmentTotalBytes != frame.Entry.FragmentTotalBytes {
		return errors.New("not the PART frame of its layout entry")
	}
	size, err := decoded.EncodedFrameSize()
	if err != nil {
		return err
	}
	if size+uint64(len(frame.Header)) > frameLimit {
		return fmt.Errorf("%d bytes with the header, budget %d", size+uint64(len(frame.Header)), frameLimit)
	}
	messageID, err := proto.NatsMsgID(networkDigest, event.EventID, uint64(fragmentIndex), frame.Entry.Form, frame.Part)
	if err != nil {
		return err
	}
	header, err := proto.NatsHeader(messageID, binding.Stream)
	if err != nil {
		return err
	}
	if !bytes.Equal(header, frame.Header) {
		return errors.New("other NATS header bytes than the binding gives")
	}
	return nil
}
