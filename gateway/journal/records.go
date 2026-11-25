package journal

import "errors"

// Record encoding: fixed binary layouts with unsigned big endian integers
// and a leading version byte. Corrupt records are a fatal store error.

const recordVersion = 1

var errCorruptRecord = errors.New("gateway journal: corrupt record")

type encoder struct{ b []byte }

func (e *encoder) u8(v uint8)   { e.b = append(e.b, v) }
func (e *encoder) u64(v uint64) { e.b = append(e.b, u64be(v)...) }
func (e *encoder) raw(v []byte) { e.b = append(e.b, v...) }

type decoder struct {
	b   []byte
	err error
}

func newDecoder(b []byte) *decoder {
	d := &decoder{b: b}
	if len(b) == 0 || b[0] != recordVersion {
		d.err = errCorruptRecord
		return d
	}
	d.b = b[1:]
	return d
}

func (d *decoder) u8() uint8 {
	if d.err != nil || len(d.b) < 1 {
		d.err = errCorruptRecord
		return 0
	}
	v := d.b[0]
	d.b = d.b[1:]
	return v
}

func (d *decoder) u64() uint64 {
	if d.err != nil || len(d.b) < 8 {
		d.err = errCorruptRecord
		return 0
	}
	v := beU64(d.b)
	d.b = d.b[8:]
	return v
}

func (d *decoder) raw(n int) []byte {
	if d.err != nil || len(d.b) < n {
		d.err = errCorruptRecord
		return nil
	}
	v := append([]byte(nil), d.b[:n]...)
	d.b = d.b[n:]
	return v
}

func (d *decoder) done() error {
	if d.err != nil {
		return d.err
	}
	if len(d.b) != 0 {
		return errCorruptRecord
	}
	return nil
}

// Counters record.
func encodeCounters(v Counters) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.u64(v.SequenceNext)
	e.u64(v.ReservationNext)
	return e.b
}

func decodeCounters(b []byte) (Counters, error) {
	d := newDecoder(b)
	v := Counters{SequenceNext: d.u64(), ReservationNext: d.u64()}
	return v, d.done()
}

// PrepareMeta binds the arriving manifest chunks to a manifest digest,
// event count and total length.
type PrepareMeta struct {
	ManifestDigest [32]byte
	EventCount     uint64
	Total          uint64
	Import         bool
}

func encodePrepareMeta(v PrepareMeta) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.raw(v.ManifestDigest[:])
	e.u64(v.EventCount)
	e.u64(v.Total)
	if v.Import {
		e.u8(1)
	} else {
		e.u8(0)
	}
	return e.b
}

func decodePrepareMeta(b []byte) (PrepareMeta, error) {
	d := newDecoder(b)
	v := PrepareMeta{}
	copy(v.ManifestDigest[:], d.raw(32))
	v.EventCount = d.u64()
	v.Total = d.u64()
	flag := d.u8()
	if d.err == nil && flag > 1 {
		return v, errCorruptRecord
	}
	v.Import = flag == 1
	return v, d.done()
}

// Reservation is the committed reservation of one batch: the exact seed
// bytes (returned byte-for-byte on retry) and its parsed identity.
type Reservation struct {
	Seed           []byte
	ReservationID  [16]byte
	IssuerEpoch    [16]byte
	ManifestDigest [32]byte
	EventCount     uint64
	SequenceBase   uint64
	BindingDigest  [32]byte
	FrameLimit     uint64
	Import         bool
}

func encodeReservation(v Reservation) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.u64(uint64(len(v.Seed)))
	e.raw(v.Seed)
	e.raw(v.ReservationID[:])
	e.raw(v.IssuerEpoch[:])
	e.raw(v.ManifestDigest[:])
	e.u64(v.EventCount)
	e.u64(v.SequenceBase)
	e.raw(v.BindingDigest[:])
	e.u64(v.FrameLimit)
	if v.Import {
		e.u8(1)
	} else {
		e.u8(0)
	}
	return e.b
}

func decodeReservation(b []byte) (Reservation, error) {
	d := newDecoder(b)
	v := Reservation{}
	n := d.u64()
	if d.err == nil && n > 1<<20 {
		return v, errCorruptRecord
	}
	v.Seed = d.raw(int(n))
	copy(v.ReservationID[:], d.raw(16))
	copy(v.IssuerEpoch[:], d.raw(16))
	copy(v.ManifestDigest[:], d.raw(32))
	v.EventCount = d.u64()
	v.SequenceBase = d.u64()
	copy(v.BindingDigest[:], d.raw(32))
	v.FrameLimit = d.u64()
	flag := d.u8()
	if d.err == nil && flag > 1 {
		return v, errCorruptRecord
	}
	v.Import = flag == 1
	return v, d.done()
}

// Publish binds the receiving phase to one accepted plan.
type Publish struct {
	PlanDigest   [32]byte
	LayoutDigest [32]byte
	EntryCount   uint64
}

func encodePublish(v Publish) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.raw(v.PlanDigest[:])
	e.raw(v.LayoutDigest[:])
	e.u64(v.EntryCount)
	return e.b
}

func decodePublish(b []byte) (Publish, error) {
	d := newDecoder(b)
	v := Publish{}
	copy(v.PlanDigest[:], d.raw(32))
	copy(v.LayoutDigest[:], d.raw(32))
	v.EntryCount = d.u64()
	return v, d.done()
}

// LayoutEntry is one layout manifest entry (CONTROL_WIRE_CONTRACT "Transport
// Frame layout v2"): COMPLETE capacity=fragment_total, part_count=1.
type LayoutEntry struct {
	Ordinal       uint64
	FragmentIndex uint64
	Form          byte // 0 COMPLETE, 1 PART
	Capacity      uint64
	PartCount     uint64
	FragmentTotal uint64
}

// FrameCount is the number of transport frames this entry produces.
func (e LayoutEntry) FrameCount() uint64 {
	if e.Form == 0 {
		return 1
	}
	return e.PartCount
}

// FrameData returns the number of data bytes one frame of this entry
// carries: the whole fragment for COMPLETE, the part's slice for PART. It is
// not the size of the frame stream: a frame stream is the encoded
// TransportFrame, the wrapper plus these bytes (frame.go).
func (e LayoutEntry) FrameData(frame uint64) uint64 {
	if e.Form == 0 {
		return e.FragmentTotal
	}
	remaining := e.FragmentTotal - frame*e.Capacity
	if remaining > e.Capacity {
		return e.Capacity
	}
	return remaining
}

func encodeLayoutEntry(v LayoutEntry) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.u64(v.Ordinal)
	e.u64(v.FragmentIndex)
	e.u8(v.Form)
	e.u64(v.Capacity)
	e.u64(v.PartCount)
	e.u64(v.FragmentTotal)
	return e.b
}

func decodeLayoutEntry(b []byte) (LayoutEntry, error) {
	d := newDecoder(b)
	v := LayoutEntry{
		Ordinal:       d.u64(),
		FragmentIndex: d.u64(),
		Form:          d.u8(),
		Capacity:      d.u64(),
		PartCount:     d.u64(),
		FragmentTotal: d.u64(),
	}
	if d.err == nil && v.Form > 1 {
		return v, errCorruptRecord
	}
	return v, d.done()
}

// Closed is the CLOSED marker: manifest completeness was verified for this
// exact plan identity.
type Closed struct {
	PlanDigest     [32]byte
	ManifestDigest [32]byte
	LayoutDigest   [32]byte
	BindingDigest  [32]byte
}

func encodeClosed(v Closed) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.raw(v.PlanDigest[:])
	e.raw(v.ManifestDigest[:])
	e.raw(v.LayoutDigest[:])
	e.raw(v.BindingDigest[:])
	return e.b
}

func decodeClosed(b []byte) (Closed, error) {
	d := newDecoder(b)
	v := Closed{}
	copy(v.PlanDigest[:], d.raw(32))
	copy(v.ManifestDigest[:], d.raw(32))
	copy(v.LayoutDigest[:], d.raw(32))
	copy(v.BindingDigest[:], d.raw(32))
	return v, d.done()
}

// NatsAcked is the durable NATS_ACKED marker with the plan identity it
// covers.
type NatsAcked struct {
	PlanDigest     [32]byte
	ManifestDigest [32]byte
}

func encodeNatsAcked(v NatsAcked) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.raw(v.PlanDigest[:])
	e.raw(v.ManifestDigest[:])
	return e.b
}

func decodeNatsAcked(b []byte) (NatsAcked, error) {
	d := newDecoder(b)
	v := NatsAcked{}
	copy(v.PlanDigest[:], d.raw(32))
	copy(v.ManifestDigest[:], d.raw(32))
	return v, d.done()
}

// Releasing tracks the bounded RELEASING cleanup.
type Releasing struct {
	Done bool
}

func encodeReleasing(v Releasing) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	if v.Done {
		e.u8(1)
	} else {
		e.u8(0)
	}
	return e.b
}

func decodeReleasing(b []byte) (Releasing, error) {
	d := newDecoder(b)
	flag := d.u8()
	if d.err == nil && flag > 1 {
		return Releasing{}, errCorruptRecord
	}
	return Releasing{Done: flag == 1}, d.done()
}
