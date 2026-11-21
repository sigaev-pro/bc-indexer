// The FragmentReader: it reassembles one record's fragment bytes from the
// transport frames taken in publication order. It verifies the shared
// wrapper identity of every frame, the part continuity inside every
// fragment, the fragment index continuity across the record, the assembled
// total lengths, every fragment digest and the record digest over the
// content fragments. Rejection is specific: truncation, reordering,
// conflicting duplicates and digest mismatches are named errors, while an
// identical redelivered frame stays idempotent, as the at-least-once
// contract requires (MESSAGE_CONTRACT sections 6 and 6.1: a conflicting
// repeat of the same fragment/part index is an error; an identical repeat is
// idempotent).
package codec

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// ReaderOptions tunes a FragmentReader.
type ReaderOptions struct {
	// OnFragment, when set, receives every completed fragment in index
	// order together with its index. The reader then does not retain the
	// fragment bytes, so memory stays bounded by one fragment plus the
	// per-part digests; Finish still verifies the record digest
	// incrementally. The slice is valid only inside the call.
	OnFragment func(index uint64, fragment []byte) error
}

// partKey identifies one transport part of the record stream.
type partKey struct {
	fragment uint64
	part     uint64
}

// fragmentAssembly collects the parts of the fragment currently in progress.
type fragmentAssembly struct {
	index          uint64
	partCount      uint64
	total          uint64
	nextPart       uint64
	partCapacity   uint64 // data length of part 0; uniform for non-final parts
	assembled      []byte
	receivedBytes  uint64
	fragmentDigest proto.Hash32
}

// FragmentReader reassembles the fragment stream of one record.
type FragmentReader struct {
	opts ReaderOptions

	haveIdentity  bool
	network       proto.NetworkIdentity
	eventID       proto.EventID
	recordDigest  proto.Hash32
	epoch         proto.ID16
	chainSequence uint64
	fragmentCount uint64

	nextFragment uint64 // first fragment index not yet completed
	current      *fragmentAssembly
	completed    uint64

	recordHash   *recordDigestStream
	contentBytes uint64
	retained     [][]byte // stays nil when OnFragment streams
	partDigests  map[partKey]proto.Hash32
}

// NewFragmentReader returns a reader for the frames of one record.
func NewFragmentReader(opts ReaderOptions) *FragmentReader {
	return &FragmentReader{opts: opts, recordHash: newRecordDigest(),
		partDigests: make(map[partKey]proto.Hash32)}
}

// Add takes one canonical encoded transport frame in publication order.
// Identical redeliveries of an already accepted part are idempotent; any
// gap, regression with different bytes, identity change or form change of
// one fragment is a named error.
func (r *FragmentReader) Add(encoded []byte) error {
	frame, err := proto.DecodeTransportFrame(encoded)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if err := r.checkIdentity(frame); err != nil {
		return err
	}
	if frame.FragmentIndex > r.nextFragment {
		return fmt.Errorf("%w: fragment %d arrives before fragment %d completed",
			ErrReordered, frame.FragmentIndex, r.nextFragment)
	}
	if frame.FragmentIndex < r.nextFragment {
		return r.handleRedelivery(frame)
	}
	// Inside the fragment under assembly, an already-received part index is
	// a redelivery; the next part index continues the assembly.
	if r.current != nil {
		if frame.Complete != nil {
			return fmt.Errorf("%w: COMPLETE frame for fragment %d while its parts are open",
				ErrConflict, frame.FragmentIndex)
		}
		if frame.Part.PartIndex < r.current.nextPart {
			return r.handleRedelivery(frame)
		}
	}
	if frame.Complete != nil {
		return r.acceptComplete(frame)
	}
	return r.acceptPart(frame)
}

// checkIdentity captures the shared wrapper identity from the first frame
// and rejects any later disagreement.
func (r *FragmentReader) checkIdentity(frame *proto.TransportFrame) error {
	if !r.haveIdentity {
		r.network = frame.Network
		r.eventID = frame.EventID
		r.recordDigest = frame.RecordDigest
		r.epoch = frame.PublicationEpoch
		r.chainSequence = frame.ChainSequence
		r.fragmentCount = frame.FragmentCount
		if r.fragmentCount < MinimumFragmentCount {
			return fmt.Errorf("%w: fragment count %d below the minimum %d",
				ErrFragments, r.fragmentCount, MinimumFragmentCount)
		}
		r.haveIdentity = true
		return nil
	}
	switch {
	case frame.Network != r.network || frame.EventID != r.eventID ||
		frame.RecordDigest != r.recordDigest || frame.PublicationEpoch != r.epoch ||
		frame.ChainSequence != r.chainSequence:
		return fmt.Errorf("%w: frame of fragment %d names a different identity",
			ErrIdentityMismatch, frame.FragmentIndex)
	case frame.FragmentCount != r.fragmentCount:
		return fmt.Errorf("%w: frame of fragment %d carries fragment count %d, want %d",
			ErrConflict, frame.FragmentIndex, frame.FragmentCount, r.fragmentCount)
	}
	return nil
}

// handleRedelivery classifies a frame for a part the reader already
// accepted: an identical repeat is idempotent, different bytes are a named
// conflict, an unknown combination is an order violation.
func (r *FragmentReader) handleRedelivery(frame *proto.TransportFrame) error {
	var data []byte
	partIndex := uint64(0)
	if frame.Complete != nil {
		data = frame.Complete.FragmentBytes
	} else {
		data = frame.Part.Data
		partIndex = frame.Part.PartIndex
	}
	key := partKey{fragment: frame.FragmentIndex, part: partIndex}
	digest := sha256Of(data)
	if known, ok := r.partDigests[key]; ok {
		if known != digest {
			return fmt.Errorf("%w: fragment %d part %d redelivered with different bytes",
				ErrDuplicate, frame.FragmentIndex, partIndex)
		}
		return nil // identical repeat: idempotent
	}
	return fmt.Errorf("%w: fragment %d part %d regresses below the assembly cursor",
		ErrReordered, frame.FragmentIndex, partIndex)
}

// acceptComplete verifies and stores one whole fragment.
func (r *FragmentReader) acceptComplete(frame *proto.TransportFrame) error {
	data := frame.Complete.FragmentBytes
	if r.current != nil {
		return fmt.Errorf("%w: COMPLETE of fragment %d while parts of fragment %d are open",
			ErrReordered, frame.FragmentIndex, r.current.index)
	}
	digest := sha256Of(data)
	if digest != frame.FragmentDigest {
		return fmt.Errorf("%w: fragment %d bytes differ from the frame digest",
			ErrDigestMismatch, frame.FragmentIndex)
	}
	r.partDigests[partKey{fragment: frame.FragmentIndex}] = digest
	return r.completeFragment(frame.FragmentIndex, data)
}

// acceptPart verifies one part and advances the assembly of its fragment.
func (r *FragmentReader) acceptPart(frame *proto.TransportFrame) error {
	part := frame.Part
	if r.current != nil && r.current.index != frame.FragmentIndex {
		return fmt.Errorf("%w: part of fragment %d while parts of fragment %d are open",
			ErrReordered, frame.FragmentIndex, r.current.index)
	}
	if r.current == nil {
		if part.PartIndex != 0 {
			return fmt.Errorf("%w: fragment %d opens at part %d, want 0",
				ErrReordered, frame.FragmentIndex, part.PartIndex)
		}
		if part.PartCount < 1 {
			return fmt.Errorf("%w: fragment %d part count %d", ErrInvalidValue, frame.FragmentIndex, part.PartCount)
		}
		if part.FragmentTotalBytes == 0 || uint64(len(part.Data)) == 0 {
			return fmt.Errorf("%w: fragment %d total %d, part data %d bytes",
				ErrInvalidValue, frame.FragmentIndex, part.FragmentTotalBytes, len(part.Data))
		}
		r.current = &fragmentAssembly{index: frame.FragmentIndex,
			partCount: part.PartCount, total: part.FragmentTotalBytes,
			partCapacity: uint64(len(part.Data)), fragmentDigest: frame.FragmentDigest}
	} else if part.PartIndex != r.current.nextPart {
		return fmt.Errorf("%w: fragment %d part %d arrives, want part %d",
			ErrReordered, frame.FragmentIndex, part.PartIndex, r.current.nextPart)
	} else if part.PartCount != r.current.partCount ||
		part.FragmentTotalBytes != r.current.total {
		return fmt.Errorf("%w: fragment %d part %d changes count or total",
			ErrConflict, frame.FragmentIndex, part.PartIndex)
	} else if frame.FragmentDigest != r.current.fragmentDigest {
		return fmt.Errorf("%w: fragment %d part %d carries a different fragment digest",
			ErrConflict, frame.FragmentIndex, part.PartIndex)
	} else if part.PartIndex+1 < part.PartCount && uint64(len(part.Data)) != r.current.partCapacity {
		return fmt.Errorf("%w: fragment %d part %d carries %d bytes, non-final parts carry %d",
			ErrInvalidValue, frame.FragmentIndex, part.PartIndex, len(part.Data), r.current.partCapacity)
	} else if uint64(len(part.Data)) > r.current.partCapacity {
		return fmt.Errorf("%w: fragment %d final part carries %d bytes above the %d capacity",
			ErrInvalidValue, frame.FragmentIndex, len(part.Data), r.current.partCapacity)
	}
	digest := sha256Of(part.Data)
	r.partDigests[partKey{fragment: frame.FragmentIndex, part: part.PartIndex}] = digest
	r.current.assembled = append(r.current.assembled, part.Data...)
	r.current.receivedBytes += uint64(len(part.Data))
	r.current.nextPart++
	if part.PartIndex+1 == part.PartCount {
		if r.current.receivedBytes != r.current.total {
			return fmt.Errorf("%w: fragment %d assembled %d bytes, total %d",
				ErrInvalidValue, frame.FragmentIndex, r.current.receivedBytes, r.current.total)
		}
		whole := sha256Of(r.current.assembled)
		if whole != r.current.fragmentDigest {
			return fmt.Errorf("%w: fragment %d assembled bytes differ from the frame digest",
				ErrDigestMismatch, frame.FragmentIndex)
		}
		assembled := r.current.assembled
		index := r.current.index
		r.current = nil
		return r.completeFragment(index, assembled)
	}
	return nil
}

// completeFragment records one verified fragment, advances the record
// digest over the content fragments and hands the fragment to the streaming
// consumer when one is set.
func (r *FragmentReader) completeFragment(index uint64, fragment []byte) error {
	if index != r.nextFragment {
		return fmt.Errorf("%w: fragment %d completed, want %d", ErrReordered, index, r.nextFragment)
	}
	if index >= r.fragmentCount {
		return fmt.Errorf("%w: fragment %d exceeds the record fragment count %d",
			ErrInvalidValue, index, r.fragmentCount)
	}
	if index != r.fragmentCount-1 {
		r.recordHash.fragment(index, fragment)
		var err error
		r.contentBytes, err = addCount(r.contentBytes, uint64(len(fragment)))
		if err != nil {
			return err
		}
	}
	r.nextFragment++
	r.completed++
	if r.opts.OnFragment != nil {
		return r.opts.OnFragment(index, fragment)
	}
	if r.retained == nil {
		r.retained = make([][]byte, 0, r.fragmentCount)
	}
	if uint64(len(r.retained)) != index {
		return fmt.Errorf("%w: retained fragment position %d, want %d",
			ErrFragments, len(r.retained), index)
	}
	r.retained = append(r.retained, fragment)
	return nil
}

// Finish verifies that every fragment of the record arrived — header,
// content fragments and seal — and that the record digest over the content
// fragments matches the digest every frame carries. It returns the
// assembled fragment bytes in index order, or nil when a streaming
// OnFragment consumer already received them.
func (r *FragmentReader) Finish() ([][]byte, error) {
	if r.current != nil {
		return nil, fmt.Errorf("%w: fragment %d stopped at part %d of %d",
			ErrTruncated, r.current.index, r.current.nextPart, r.current.partCount)
	}
	if r.completed != r.fragmentCount {
		return nil, fmt.Errorf("%w: %d of %d fragments assembled",
			ErrTruncated, r.completed, r.fragmentCount)
	}
	if r.recordHash.sum() != r.recordDigest {
		return nil, fmt.Errorf("%w: content fragments hash to a different record digest",
			ErrDigestMismatch)
	}
	return r.retained, nil
}
