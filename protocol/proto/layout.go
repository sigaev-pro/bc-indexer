// The transport layout v2 planner: for every (event ordinal,
// fragment_index) pair it fixes the form, the part capacity and the part
// count from the frame budget F, with checked arithmetic and deterministic
// output, and it computes the layout manifest digest
// (CONTROL_WIRE_CONTRACT, "TransportFrame layout v2", steps 1-4).
package proto

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
)

// LayoutManifestEntrySize is the encoded size of one layout manifest entry:
// ordinal u64be, fragment_index u64be, form 1 byte, capacity u64be,
// part_count u64be, fragment_total_bytes u64be.
const LayoutManifestEntrySize = 8 + 8 + 1 + 8 + 8 + 8

// LayoutManifestEntry is one (event ordinal, fragment_index) plan decision.
// A COMPLETE entry carries capacity=fragment_total_bytes and part_count=1.
type LayoutManifestEntry struct {
	Ordinal            uint64
	FragmentIndex      uint64
	Form               Form
	Capacity           uint64
	PartCount          uint64
	FragmentTotalBytes uint64
}

// Validate rejects an entry whose form is unknown, whose total is outside
// the fragment bound, or whose capacity and part count do not match the
// layout arithmetic: COMPLETE covers the fragment whole, PART counts
// ceil(total/capacity) parts of at most capacity bytes.
func (e LayoutManifestEntry) Validate() error {
	if !e.Form.Valid() {
		return fmt.Errorf("%w: form %d is not COMPLETE or PART", ErrClosedRegistry, byte(e.Form))
	}
	if e.FragmentTotalBytes == 0 || e.FragmentTotalBytes > FragmentMaxBytes {
		return fmt.Errorf("%w: fragment total %d outside 1..%d",
			ErrInvalidValue, e.FragmentTotalBytes, FragmentMaxBytes)
	}
	switch e.Form {
	case FormComplete:
		if e.Capacity != e.FragmentTotalBytes {
			return fmt.Errorf("%w: COMPLETE capacity %d, want fragment total %d",
				ErrInvalidValue, e.Capacity, e.FragmentTotalBytes)
		}
		if e.PartCount != 1 {
			return fmt.Errorf("%w: COMPLETE part count %d, want 1", ErrInvalidValue, e.PartCount)
		}
	case FormPart:
		if e.Capacity == 0 || e.Capacity > e.FragmentTotalBytes {
			return fmt.Errorf("%w: PART capacity %d outside 1..%d",
				ErrInvalidValue, e.Capacity, e.FragmentTotalBytes)
		}
		want, err := ceilDiv(e.FragmentTotalBytes, e.Capacity)
		if err != nil {
			return err
		}
		if e.PartCount != want {
			return fmt.Errorf("%w: PART part count %d, want ceil(%d/%d)=%d",
				ErrInvalidValue, e.PartCount, e.FragmentTotalBytes, e.Capacity, want)
		}
	}
	return nil
}

// AppendEncoded appends the fixed-order form of the entry: u64be fields in
// table order with the form as one byte between fragment_index and capacity.
func (e LayoutManifestEntry) AppendEncoded(b []byte) []byte {
	b = u64be(b, e.Ordinal)
	b = u64be(b, e.FragmentIndex)
	b = append(b, byte(e.Form))
	b = u64be(b, e.Capacity)
	b = u64be(b, e.PartCount)
	return u64be(b, e.FragmentTotalBytes)
}

// Encode returns the fixed-order encoded entry.
func (e LayoutManifestEntry) Encode() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return e.AppendEncoded(make([]byte, 0, LayoutManifestEntrySize)), nil
}

// DecodeLayoutManifestEntry parses exactly one fixed-order entry.
func DecodeLayoutManifestEntry(b []byte) (LayoutManifestEntry, error) {
	if len(b) != LayoutManifestEntrySize {
		return LayoutManifestEntry{}, fmt.Errorf("%w: layout entry length %d, want %d",
			ErrInvalidValue, len(b), LayoutManifestEntrySize)
	}
	var e LayoutManifestEntry
	e.Ordinal = beUint64(b[0:8])
	e.FragmentIndex = beUint64(b[8:16])
	e.Form = Form(b[16])
	e.Capacity = beUint64(b[17:25])
	e.PartCount = beUint64(b[25:33])
	e.FragmentTotalBytes = beUint64(b[33:41])
	if err := e.Validate(); err != nil {
		return LayoutManifestEntry{}, err
	}
	return e, nil
}

// PartBounds returns the byte range [start, end) of part partIndex inside
// the fragment: part i carries bytes [i*capacity, min((i+1)*capacity,
// total)). All length and offset arithmetic is checked; overflow is
// rejected, never truncated.
func (e LayoutManifestEntry) PartBounds(partIndex uint64) (start, end uint64, err error) {
	if err := e.Validate(); err != nil {
		return 0, 0, err
	}
	if partIndex >= e.PartCount {
		return 0, 0, fmt.Errorf("%w: part index %d outside count %d",
			ErrInvalidValue, partIndex, e.PartCount)
	}
	if e.Form == FormComplete {
		return 0, e.FragmentTotalBytes, nil
	}
	start, err = mulUint64(partIndex, e.Capacity)
	if err != nil {
		return 0, 0, err
	}
	end, err = addUint64(start, e.Capacity)
	if err != nil {
		return 0, 0, err
	}
	if end > e.FragmentTotalBytes {
		end = e.FragmentTotalBytes
	}
	return start, end, nil
}

// EventFragments is the layout input of one event: its ordinal and the
// encoded lengths of its fragments in index order.
type EventFragments struct {
	Ordinal         uint64
	FragmentLengths []uint64
}

// LayoutRequest is one deterministic planning run over a closed batch: the
// frame budget F, the NATS stream name that fixes the header size, the
// wrapper identities that fix the frame sizes, the reservation sequence
// base and the events in ordinal order.
type LayoutRequest struct {
	// Budget is F = transport.max_frame_bytes: the exact encoded frame
	// size plus the NATS header bytes must fit inside it.
	Budget uint64
	// Stream is the stream name of the durable binding; the header size
	// is fixed by the message-id tuple length and the stream name length.
	Stream string
	// Network is the frame network identity of the batch.
	Network NetworkIdentity
	// PublicationEpoch is the publication epoch of the batch.
	PublicationEpoch ID16
	// SequenceBase is the reservation sequence base: the chain_sequence
	// of event ordinal o is SequenceBase + o, with checked addition.
	SequenceBase uint64
	// Events are the events of the batch in ordinal order; ordinals are
	// continuous from 0.
	Events []EventFragments
}

// LayoutPlan is the deterministic result of one planning run: one entry per
// (event ordinal, fragment_index) pair in ordinal/fragment order.
type LayoutPlan struct {
	// Budget and Stream echo the request inputs the plan was computed for,
	// so a saved plan can be checked against the budget it came from.
	Budget            uint64
	Stream            string
	StreamHeaderBytes uint64
	Entries           []LayoutManifestEntry
}

// ComputeLayout plans the transport form of every (event ordinal,
// fragment_index) pair of a closed batch, in this order per pair:
//
//  1. If the exact COMPLETE frame with its actual metadata plus the NATS
//     header bytes fits the budget F, the plan is COMPLETE.
//  2. Otherwise the largest capacity 1 <= C <= fragment_total_bytes for
//     which a PART frame with data length C, part_index=part_count=U64Max
//     (the conservative maximal varint overhead) and the actual fragment
//     metadata fits F. The maximal indices avoid the circular dependency of
//     count on size.
//  3. PartCount is ceil(total/C); part i carries the bytes
//     [i*C, min((i+1)*C, total)). All arithmetic is checked.
//
// If no capacity C >= 1 fits, the planner reports ErrLimitIncompatible
// (LAYOUT-01: C below 1 keeps the batch READY instead of replanning it).
// The same request always produces the same plan.
func ComputeLayout(req LayoutRequest) (*LayoutPlan, error) {
	if req.Budget == 0 {
		return nil, fmt.Errorf("%w: frame budget is zero", ErrInvalidValue)
	}
	header, err := NatsHeaderSize(req.Stream)
	if err != nil {
		return nil, err
	}
	if err := req.Network.Validate(); err != nil {
		return nil, err
	}
	if req.PublicationEpoch.IsZero() {
		return nil, fmt.Errorf("%w: publication epoch is zero", ErrInvalidValue)
	}
	if len(req.Events) == 0 {
		return nil, fmt.Errorf("%w: layout of an empty batch", ErrInvalidValue)
	}
	plan := &LayoutPlan{Budget: req.Budget, Stream: req.Stream, StreamHeaderBytes: header}
	var entryCount uint64
	for eventIndex := range req.Events {
		event := req.Events[eventIndex]
		if eventIndex == 0 {
			if event.Ordinal != 0 {
				return nil, fmt.Errorf("%w: first event ordinal %d, want 0",
					ErrInvalidValue, event.Ordinal)
			}
		} else {
			want, err := addUint64(req.Events[eventIndex-1].Ordinal, 1)
			if err != nil {
				return nil, fmt.Errorf("%w: event ordinal after %d: %w", ErrOverflow,
					req.Events[eventIndex-1].Ordinal, err)
			}
			if event.Ordinal != want {
				return nil, fmt.Errorf("%w: event ordinal %d breaks continuity, want %d",
					ErrInvalidValue, event.Ordinal, want)
			}
		}
		if len(event.FragmentLengths) == 0 {
			return nil, fmt.Errorf("%w: event ordinal %d has no fragments",
				ErrInvalidValue, event.Ordinal)
		}
		chainSequence, err := addUint64(req.SequenceBase, event.Ordinal)
		if err != nil {
			return nil, fmt.Errorf("%w: chain sequence of event ordinal %d: %w",
				ErrOverflow, event.Ordinal, err)
		}
		for fragIndex, total := range event.FragmentLengths {
			entry, err := planOnePair(req, header, chainSequence,
				event.Ordinal, uint64(fragIndex), uint64(len(event.FragmentLengths)), total)
			if err != nil {
				return nil, err
			}
			plan.Entries = append(plan.Entries, entry)
			entryCount, err = addUint64(entryCount, 1)
			if err != nil {
				return nil, fmt.Errorf("%w: layout entry count: %w", ErrOverflow, err)
			}
		}
	}
	return plan, nil
}

// planOnePair decides the form of one (ordinal, fragment_index) pair.
func planOnePair(req LayoutRequest, header, chainSequence, ordinal, fragmentIndex,
	fragmentCount, total uint64) (LayoutManifestEntry, error) {
	if total == 0 || total > FragmentMaxBytes {
		return LayoutManifestEntry{}, fmt.Errorf("%w: event ordinal %d fragment %d total %d outside 1..%d",
			ErrInvalidValue, ordinal, fragmentIndex, total, FragmentMaxBytes)
	}
	scalars := frameScalars{chainSequence: chainSequence,
		fragmentIndex: fragmentIndex, fragmentCount: fragmentCount}
	if completeEncodedSize(scalars, total)+header <= req.Budget {
		return LayoutManifestEntry{Ordinal: ordinal, FragmentIndex: fragmentIndex,
			Form: FormComplete, Capacity: total, PartCount: 1, FragmentTotalBytes: total}, nil
	}
	// Conservative PART probe: maximal part_index and part_count varints.
	capacity := largestFittingCapacity(scalars, total, req.Budget-header)
	if capacity == 0 {
		return LayoutManifestEntry{}, fmt.Errorf("%w: event ordinal %d fragment %d of %d bytes fits no part under budget %d",
			ErrLimitIncompatible, ordinal, fragmentIndex, total, req.Budget)
	}
	partCount, err := ceilDiv(total, capacity)
	if err != nil {
		return LayoutManifestEntry{}, err
	}
	return LayoutManifestEntry{Ordinal: ordinal, FragmentIndex: fragmentIndex,
		Form: FormPart, Capacity: capacity, PartCount: partCount, FragmentTotalBytes: total}, nil
}

// largestFittingCapacity returns the largest data length 1..total whose
// conservative PART frame fits avail bytes, or 0 when none fits. The frame
// size grows monotonically with the data length, so a binary search finds
// the boundary without probing every length.
func largestFittingCapacity(scalars frameScalars, total, avail uint64) uint64 {
	probe := func(dataLen uint64) bool {
		return partEncodedSize(scalars, math.MaxUint64, math.MaxUint64, total, dataLen) <= avail
	}
	lo, hi := uint64(1), total
	if !probe(lo) {
		return 0
	}
	for lo < hi {
		mid := lo + (hi-lo+1)/2 // ceil so the loop always advances
		if mid > total {
			mid = total
		}
		if probe(mid) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// LayoutManifestDigest computes
//
//	SHA256(domain "extractor.layout.v2" + zero + plan_digest +
//	    entry_count_u64be + entries)
//
// with the entries in their fixed-order encoding. The entry count is the
// fragment count sum of all events of the batch.
func LayoutManifestDigest(planDigest Hash32, entries []LayoutManifestEntry) (Hash32, error) {
	if uint64(len(entries)) > math.MaxUint32 { // far below u64, checked anyway
		return Hash32{}, fmt.Errorf("%w: %d layout entries", ErrOverflow, len(entries))
	}
	var out Hash32
	h := sha256.New()
	h.Write([]byte(layoutDigestDomain))
	h.Write([]byte{0})
	h.Write(planDigest[:])
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(entries)))
	h.Write(count[:])
	for _, e := range entries {
		encoded, err := e.Encode()
		if err != nil {
			return Hash32{}, err
		}
		h.Write(encoded)
	}
	h.Sum(out[:0])
	return out, nil
}

// ceilDiv returns ceil(a/b) for b > 0 with checked arithmetic.
func ceilDiv(a, b uint64) (uint64, error) {
	if b == 0 {
		return 0, fmt.Errorf("%w: ceilDiv by zero", ErrInvalidValue)
	}
	q := a / b
	if a%b != 0 {
		var err error
		q, err = addUint64(q, 1)
		if err != nil {
			return 0, fmt.Errorf("%w: ceil(%d/%d): %w", ErrOverflow, a, b, err)
		}
	}
	return q, nil
}
