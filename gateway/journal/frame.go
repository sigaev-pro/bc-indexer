package journal

// Frame accounting. A frame stream is the canonical encoding of one
// TransportFrame v2: the wrapper plus the data it carries
// (CONTROL_WIRE_CONTRACT, "TransportFrame layout v2": the RPC byte offset
// refers to one TransportFrame, not to the logical record). The journal
// does not count data bytes. It fixes the exact encoded size of every
// declared frame when the frame is declared, from three things it holds:
//
//   - the reservation, for the chain sequence of the event (its sequence
//     base plus the ordinal);
//   - the stored manifest, for the fragment count of the event, a wrapper
//     field whose varint length varies;
//   - the declared layout entry, for the form, the part count, the part
//     index and the data length of the frame.
//
// protocol/proto owns the size formula (TransportFrame.EncodedFrameSize);
// the journal asks for the size and never repeats the arithmetic. Every
// later rule of the frame stream works on that size: a chunk may not
// carry bytes past it, and a frame is complete when its stream covers it.

import "github.com/sigaev-pro/bc-indexer/protocol/proto"

// manifestDomain opens the manifest bytes of a batch
// ("extractor.manifest.v2", a zero byte, then the event count).
const manifestDomain = "extractor.manifest.v2"

// The identity fields of the wrapper have a fixed encoded size, so the
// frame shape that is measured carries any valid value for them. Only the
// scalars and the data length decide the size.
var (
	shapeEventID          = proto.NewEventID(proto.ID16{1}, proto.ID16{1}, 0)
	shapeNetwork          = proto.NetworkIdentity{Family: proto.NetworkFamilyEVM}
	shapePublicationEpoch = proto.ID16{1}
)

// frameEncodedSize returns the exact size of the canonical encoding of one
// frame of a declared layout entry: the wrapper plus the data the frame
// carries. scratch is the journal's buffer for the data length probe; it is
// grown once and reused.
func frameEncodedSize(scratch *[]byte, sequenceBase uint64, entry LayoutEntry, fragmentCount, frame uint64) (uint64, error) {
	data := entry.FrameData(frame)
	if data == 0 || data > proto.FragmentMaxBytes {
		return 0, ErrStateConflict // a frame carries 1..65536 data bytes
	}
	sequence := sequenceBase + entry.Ordinal
	if sequence < sequenceBase {
		return 0, ErrCounterOverflow
	}
	if cap(*scratch) < int(data) {
		*scratch = make([]byte, proto.FragmentMaxBytes)
	}
	payload := (*scratch)[:data]
	shape := proto.TransportFrame{
		TransportVersion:    proto.TransportVersion,
		Network:             shapeNetwork,
		EventID:             shapeEventID,
		PublicationEpoch:    shapePublicationEpoch,
		ChainSequence:       sequence,
		FragmentIndex:       entry.FragmentIndex,
		FragmentCount:       fragmentCount,
		RecordFormatVersion: proto.RecordFormatVersion,
	}
	if entry.Form == 0 {
		shape.Complete = &proto.CompleteFragment{FragmentBytes: payload}
	} else {
		shape.Part = &proto.FragmentPart{PartIndex: frame, PartCount: entry.PartCount,
			FragmentTotalBytes: entry.FragmentTotal, Data: payload}
	}
	size, err := shape.EncodedFrameSize()
	if err != nil {
		// The shape is valid for every reservation whose sequence base is a
		// counter; anything else cannot be the reservation of this batch.
		return 0, ErrInvalidSeed
	}
	return size, nil
}

// encodedFrameSize is frameEncodedSize for one frame of one batch, with the
// sequence base and the fragment count read from the batch's own records.
// The caller holds j.mu.
func (j *Journal) encodedFrameSize(id BatchID, entry LayoutEntry, frame uint64) (uint64, error) {
	res, err := j.readReservation(id)
	if err != nil {
		return 0, err
	}
	count, err := j.fragmentCount(id, entry.Ordinal)
	if err != nil {
		return 0, err
	}
	return frameEncodedSize(&j.frameScratch, res.SequenceBase, entry, count, frame)
}

// fragmentCount returns the fragment count of one event of the manifest of
// a batch. The caller holds j.mu.
func (j *Journal) fragmentCount(id BatchID, ordinal uint64) (uint64, error) {
	counts, err := j.fragmentCounts(id)
	if err != nil {
		return 0, err
	}
	if ordinal >= uint64(len(counts)) {
		return 0, ErrManifestMismatch // the event is not in the manifest
	}
	return counts[ordinal], nil
}

// fragmentCounts returns the fragment count of every event of one batch by
// ordinal, read from its stored manifest. Every frame declaration needs one
// count, so the manifest is read once per batch and the result is kept in
// memory until the batch is closed or retired. The caller holds j.mu.
func (j *Journal) fragmentCounts(id BatchID) ([]uint64, error) {
	if counts, ok := j.manifests[id]; ok {
		return counts, nil
	}
	w := &manifestWalker{}
	var walkErr error
	// hashChunks feeds the stored chunks in offset order and refuses a hole.
	if err := hashChunks(j.st, batchKey(id, "prepare/"), func(p []byte) {
		if walkErr == nil {
			walkErr = w.feed(p)
		}
	}); err != nil {
		return nil, err
	}
	if walkErr != nil {
		return nil, walkErr
	}
	counts, err := w.finish()
	if err != nil {
		return nil, err
	}
	if j.manifests == nil {
		j.manifests = make(map[BatchID][]uint64)
	}
	j.manifests[id] = counts
	return counts, nil
}

// forgetManifest drops the kept fragment counts of one batch, once no frame
// of it can be declared anymore. The caller holds j.mu.
func (j *Journal) forgetManifest(id BatchID) {
	delete(j.manifests, id)
}

// manifestWalker reads the manifest bytes piece by piece and keeps the
// fragment count of every event by ordinal. The manifest is the domain, a
// zero byte and the event count, then per event its fixed-size descriptor
// followed by that many fixed-size fragment descriptors. The walker holds
// one descriptor at a time and passes over the fragment descriptors without
// keeping them, so its memory does not grow with the manifest; only the
// result does, with one word per event.
type manifestWalker struct {
	// pending collects the bytes of the record being read.
	pending []byte
	// counts is the fragment count of every event read so far, by ordinal.
	counts []uint64
	// events is the event count of the header.
	events uint64
	// skip is the number of fragment descriptor bytes still to pass over.
	skip uint64
	// stage is 0 for the header, 1 for an event descriptor and 2 once the
	// last event was read.
	stage uint8
}

// stages of the manifest walker.
const (
	manifestHeader uint8 = iota
	manifestEvent
	manifestEnd
)

// feed takes the next piece of the manifest bytes, of any length: a chunk
// boundary may fall inside any record.
func (w *manifestWalker) feed(p []byte) error {
	for len(p) > 0 {
		if w.skip > 0 {
			n := len(p)
			if uint64(n) > w.skip {
				n = int(w.skip)
			}
			p = p[n:]
			w.skip -= uint64(n)
			continue
		}
		if w.stage == manifestEnd {
			return ErrManifestMismatch // bytes follow the last event
		}
		size := len(manifestDomain) + 1 + 8
		if w.stage == manifestEvent {
			size = proto.EventDescriptorSize
		}
		n := size - len(w.pending)
		if n > len(p) {
			n = len(p)
		}
		w.pending = append(w.pending, p[:n]...)
		p = p[n:]
		if len(w.pending) < size {
			return nil // the record continues in a later piece
		}
		if err := w.record(); err != nil {
			return err
		}
	}
	return nil
}

// record handles one complete record and clears the pending bytes.
func (w *manifestWalker) record() error {
	defer func() { w.pending = w.pending[:0] }()
	if w.stage == manifestHeader {
		if string(w.pending[:len(manifestDomain)]) != manifestDomain || w.pending[len(manifestDomain)] != 0 {
			return ErrManifestMismatch
		}
		w.events = beU64(w.pending[len(manifestDomain)+1:])
		if w.events == 0 {
			return ErrManifestMismatch
		}
		w.stage = manifestEvent
		return nil
	}
	event, err := proto.DecodeEventDescriptor(w.pending)
	if err != nil || event.Ordinal != uint64(len(w.counts)) {
		return ErrManifestMismatch // not a descriptor, or the ordinals break
	}
	if event.FragmentCount > ^uint64(0)/proto.FragmentDescriptorSize {
		return ErrManifestMismatch
	}
	w.counts = append(w.counts, event.FragmentCount)
	w.skip = event.FragmentCount * proto.FragmentDescriptorSize
	if uint64(len(w.counts)) == w.events {
		w.stage = manifestEnd
	}
	return nil
}

// finish returns the fragment counts once every byte of the manifest was
// read. A manifest that ends before its last event is not complete.
func (w *manifestWalker) finish() ([]uint64, error) {
	if w.stage != manifestEnd || w.skip != 0 {
		return nil, ErrManifestMismatch
	}
	return w.counts, nil
}
