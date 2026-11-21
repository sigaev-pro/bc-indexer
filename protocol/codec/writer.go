// The FragmentWriter: it carries one bounded RecordFragment over the
// transport, whole as a CompleteFragment or in capacity-sized FragmentPart
// pieces, following a layout plan entry of protocol/proto. The plan — the
// form, the part capacity and the part count for every (event ordinal,
// fragment_index) pair — always comes from proto.ComputeLayout; this writer
// never re-implements the planning arithmetic, it executes a saved entry and
// verifies every emitted frame against the exact encoded size plus the NATS
// header bytes before handing it to the sink (CONTROL_WIRE_CONTRACT,
// "TransportFrame layout v2", steps 3..4).
//
// Memory is bounded by the chunk size, not by the record: the slice-based
// Write reuses the caller's payload without copying it into a second buffer,
// and WriteFromReader reads one part at a time into one reused chunk buffer
// (D76 streaming writer pattern).
package codec

import (
	"crypto/sha256"
	"fmt"
	"io"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// FrameSink receives one emitted transport frame with everything the publish
// path needs: the decoded frame, its canonical bytes, the exact NATS message
// id and header bytes, and the exact sizes. The Encoded and NATSHeader byte
// slices are freshly allocated per frame; the frame's Part.Data may alias
// the writer's reused chunk buffer and is valid only until the sink returns.
type FrameSink func(ef *EmittedFrame) error

// EmittedFrame is one ready-to-publish transport frame.
type EmittedFrame struct {
	// Entry is the layout plan decision the frame executes.
	Entry proto.LayoutManifestEntry
	// PartIndex is 0 for a COMPLETE frame.
	PartIndex uint64
	// Frame is the decoded wrapper.
	Frame *proto.TransportFrame
	// Encoded are the canonical wrapper bytes.
	Encoded []byte
	// EncodedSize is len(Encoded); EncodedSize plus HeaderSize is at or
	// below the plan budget.
	EncodedSize uint64
	// HeaderSize is len(NATSHeader).
	HeaderSize uint64
	// MessageID is the exact NATS message id of the frame.
	MessageID string
	// NATSHeader are the exact header bytes of the frame.
	NATSHeader []byte
}

// WriterConfig carries the record-level wrapper identity every frame of one
// record shares: network, event id, record digest, publication epoch and the
// fragment count.
type WriterConfig struct {
	Network          proto.NetworkIdentity
	EventID          proto.EventID
	RecordDigest     proto.Hash32
	PublicationEpoch proto.ID16
	FragmentCount    uint64
}

// FragmentWriter executes layout plan entries of one record.
type FragmentWriter struct {
	plan          *proto.LayoutPlan
	cfg           WriterConfig
	networkDigest proto.Hash32
}

// NewFragmentWriter validates the record identity and binds the writer to
// one saved layout plan. Every entry passed to Write must be an entry of
// that plan, so the form of a fragment cannot change between attempts.
func NewFragmentWriter(plan *proto.LayoutPlan, cfg WriterConfig) (*FragmentWriter, error) {
	if plan == nil {
		return nil, fmt.Errorf("%w: layout plan is nil", ErrInvalidValue)
	}
	if plan.Budget == 0 || plan.Stream == "" {
		return nil, fmt.Errorf("%w: layout plan carries no budget or stream", ErrInvalidValue)
	}
	if len(plan.Entries) == 0 {
		return nil, fmt.Errorf("%w: layout plan has no entries", ErrInvalidValue)
	}
	if err := cfg.Network.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrClosedRegistry, err)
	}
	if err := cfg.EventID.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidValue, err)
	}
	if cfg.PublicationEpoch.IsZero() {
		return nil, fmt.Errorf("%w: publication epoch is zero", ErrInvalidValue)
	}
	if cfg.FragmentCount < MinimumFragmentCount {
		return nil, fmt.Errorf("%w: fragment count %d below the minimum %d",
			ErrFragments, cfg.FragmentCount, MinimumFragmentCount)
	}
	networkDigest, err := cfg.Network.Digest()
	if err != nil {
		return nil, err
	}
	return &FragmentWriter{plan: plan, cfg: cfg, networkDigest: networkDigest}, nil
}

// ChainSequence computes the chain_sequence of one event ordinal from the
// reservation sequence base with checked addition.
func ChainSequence(sequenceBase, ordinal uint64) (uint64, error) {
	seq := sequenceBase + ordinal
	if seq < sequenceBase {
		return 0, fmt.Errorf("%w: sequence base %d + ordinal %d", ErrOverflow, sequenceBase, ordinal)
	}
	return seq, nil
}

// sha256Of hashes bytes into the fixed 32-byte digest type.
func sha256Of(b []byte) proto.Hash32 {
	return proto.Hash32(sha256.Sum256(b))
}

// Write splits payload — the exact bytes of one serialized fragment — into
// the frames the entry prescribes and passes each to sink. The payload is
// never copied into a second buffer; the frames reference its slices
// directly, so it must stay unmodified until the sink returned.
func (w *FragmentWriter) Write(entry proto.LayoutManifestEntry, chainSequence uint64,
	payload []byte, sink FrameSink) error {
	if uint64(len(payload)) != entry.FragmentTotalBytes {
		return fmt.Errorf("%w: payload of %d bytes, layout entry plans %d",
			ErrInvalidValue, len(payload), entry.FragmentTotalBytes)
	}
	return w.writeParts(entry, chainSequence, payload, sink)
}

// WriteFromReader streams the fragment payload from r, reading at most one
// part capacity of bytes at a time into one reused chunk buffer, and emits
// the frames the entry prescribes. Every frame of one fragment carries the
// digest of the whole fragment, which a single-pass stream cannot know
// before it is read, so the digest comes in with the call — it is the
// fragment digest of the READY descriptor the saved plan was built from.
// The writer hashes the streamed bytes and verifies them against it before
// finishing (the pre-SEALED per-frame digest check). Exactly
// entry.FragmentTotalBytes bytes are consumed; a shorter stream is a named
// truncation error.
func (w *FragmentWriter) WriteFromReader(entry proto.LayoutManifestEntry, chainSequence uint64,
	expectedDigest proto.Hash32, r io.Reader, sink FrameSink) error {
	if err := w.checkEntry(entry, chainSequence); err != nil {
		return err
	}
	if entry.Form == proto.FormComplete {
		buf := make([]byte, entry.FragmentTotalBytes)
		if _, err := io.ReadFull(r, buf); err != nil {
			return fmt.Errorf("%w: fragment %d payload: %w", ErrTruncated, entry.FragmentIndex, err)
		}
		return w.emitVerified(entry, chainSequence, expectedDigest, buf, sink)
	}
	chunk := make([]byte, entry.Capacity)
	fragmentHash := sha256.New()
	var consumed uint64
	for part := uint64(0); part < entry.PartCount; part++ {
		start, end, err := entry.PartBounds(part)
		if err != nil {
			return err
		}
		want := end - start
		if _, err := io.ReadFull(r, chunk[:want]); err != nil {
			return fmt.Errorf("%w: fragment %d part %d: %w", ErrTruncated, entry.FragmentIndex, part, err)
		}
		fragmentHash.Write(chunk[:want])
		consumed, err = addCount(consumed, want)
		if err != nil {
			return err
		}
		if err := w.emit(entry, chainSequence, part, chunk[:want], expectedDigest[:], sink); err != nil {
			return err
		}
	}
	if consumed != entry.FragmentTotalBytes {
		return fmt.Errorf("%w: consumed %d bytes, layout entry plans %d",
			ErrInvalidValue, consumed, entry.FragmentTotalBytes)
	}
	return w.verifyStreamedDigest(entry, fragmentHash.Sum(nil), expectedDigest)
}

// emitVerified emits one COMPLETE frame whose payload was streamed whole and
// verifies its digest against the expected descriptor digest.
func (w *FragmentWriter) emitVerified(entry proto.LayoutManifestEntry, chainSequence uint64,
	expectedDigest proto.Hash32, payload []byte, sink FrameSink) error {
	fragmentHash := sha256.New()
	fragmentHash.Write(payload)
	if err := w.verifyStreamedDigest(entry, fragmentHash.Sum(nil), expectedDigest); err != nil {
		return err
	}
	return w.emit(entry, chainSequence, 0, payload, expectedDigest[:], sink)
}

// verifyStreamedDigest is the pre-SEALED digest check of contract step 4:
// streamed bytes must hash to the digest the plan was built from.
func (w *FragmentWriter) verifyStreamedDigest(entry proto.LayoutManifestEntry,
	actual []byte, expected proto.Hash32) error {
	var got proto.Hash32
	copy(got[:], actual)
	if got != expected {
		return fmt.Errorf("%w: fragment %d streams to a different digest than the plan entry",
			ErrDigestMismatch, entry.FragmentIndex)
	}
	return nil
}

// writeParts executes an entry over an in-memory payload.
func (w *FragmentWriter) writeParts(entry proto.LayoutManifestEntry, chainSequence uint64,
	payload []byte, sink FrameSink) error {
	if err := w.checkEntry(entry, chainSequence); err != nil {
		return err
	}
	fragmentHash := sha256.New()
	fragmentHash.Write(payload)
	digest := fragmentHash.Sum(nil)
	if entry.Form == proto.FormComplete {
		return w.emit(entry, chainSequence, 0, payload, digest, sink)
	}
	for part := uint64(0); part < entry.PartCount; part++ {
		start, end, err := entry.PartBounds(part)
		if err != nil {
			return err
		}
		if err := w.emit(entry, chainSequence, part, payload[start:end], digest, sink); err != nil {
			return err
		}
	}
	return nil
}

// checkEntry validates the entry, its chain sequence and its membership in
// the saved plan.
func (w *FragmentWriter) checkEntry(entry proto.LayoutManifestEntry, chainSequence uint64) error {
	if err := entry.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidValue, err)
	}
	if chainSequence == 0 {
		return fmt.Errorf("%w: chain sequence 0, counters start at 1", ErrInvalidValue)
	}
	for _, planEntry := range w.plan.Entries {
		if planEntry == entry {
			return nil
		}
	}
	return fmt.Errorf("%w: layout entry (ordinal %d, fragment %d) is not part of the saved plan",
		ErrConflict, entry.Ordinal, entry.FragmentIndex)
}

// emit builds, size-checks and hands over one frame. The exact frame size
// plus the exact NATS header bytes must fit the plan budget; a frame that
// cannot fit is the codec-level LIMIT_INCOMPATIBLE rejection — nothing is
// truncated or replanned here.
func (w *FragmentWriter) emit(entry proto.LayoutManifestEntry, chainSequence, partIndex uint64,
	data, fragmentDigest []byte, sink FrameSink) error {
	var digest proto.Hash32
	copy(digest[:], fragmentDigest)
	frame := &proto.TransportFrame{
		TransportVersion:    proto.TransportVersion,
		Network:             w.cfg.Network,
		EventID:             w.cfg.EventID,
		RecordDigest:        w.cfg.RecordDigest,
		PublicationEpoch:    w.cfg.PublicationEpoch,
		ChainSequence:       chainSequence,
		FragmentIndex:       entry.FragmentIndex,
		FragmentCount:       w.cfg.FragmentCount,
		FragmentDigest:      digest,
		RecordFormatVersion: proto.RecordFormatVersion,
	}
	if entry.Form == proto.FormComplete {
		frame.Complete = &proto.CompleteFragment{FragmentBytes: data}
	} else {
		frame.Part = &proto.FragmentPart{PartIndex: partIndex, PartCount: entry.PartCount,
			FragmentTotalBytes: entry.FragmentTotalBytes, Data: data}
	}
	encodedSize, err := frame.EncodedFrameSize()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidValue, err)
	}
	headerSize, err := proto.NatsHeaderSize(w.plan.Stream)
	if err != nil {
		return err
	}
	if encodedSize+headerSize > w.plan.Budget {
		return fmt.Errorf("%w: frame of ordinal %d fragment %d part %d needs %d bytes, budget %d",
			ErrLimitIncompatible, entry.Ordinal, entry.FragmentIndex, partIndex,
			encodedSize+headerSize, w.plan.Budget)
	}
	encoded, err := frame.Encode()
	if err != nil {
		return err
	}
	form := proto.FormComplete
	if entry.Form == proto.FormPart {
		form = proto.FormPart
	}
	messageID, err := proto.NatsMsgID(w.networkDigest, w.cfg.EventID, entry.FragmentIndex, form, partIndex)
	if err != nil {
		return err
	}
	header, err := proto.NatsHeader(messageID, w.plan.Stream)
	if err != nil {
		return err
	}
	return sink(&EmittedFrame{Entry: entry, PartIndex: partIndex, Frame: frame,
		Encoded: encoded, EncodedSize: encodedSize, HeaderSize: headerSize,
		MessageID: messageID, NATSHeader: header})
}
