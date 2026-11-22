package producer

import (
	"errors"
	"fmt"
)

// Record encoding: fixed binary layouts with unsigned big endian integers.
// Every record starts with a one-byte version so a future format change is
// detectable instead of silently misread. Corruption (short records,
// unsupported versions) is a fatal store error, not a soft default.

const recordVersion = 1

var errCorrupt = errors.New("producer journal: corrupt record")

type encoder struct{ b []byte }

func (e *encoder) u8(v uint8)   { e.b = append(e.b, v) }
func (e *encoder) u64(v uint64) { e.b = append(e.b, u64be(v)...) }
func (e *encoder) bytes(v []byte) {
	e.u64(uint64(len(v)))
	e.b = append(e.b, v...)
}
func (e *encoder) hash(h [32]byte) { e.b = append(e.b, h[:]...) }
func (e *encoder) id16(h [16]byte) { e.b = append(e.b, h[:]...) }

type decoder struct {
	b   []byte
	err error
}

func newDecoder(b []byte) *decoder {
	d := &decoder{b: b}
	if len(b) == 0 {
		d.err = errCorrupt
		return d
	}
	if b[0] != recordVersion {
		d.err = fmt.Errorf("%w: record version %d, want %d", errCorrupt, b[0], recordVersion)
		return d
	}
	d.b = b[1:]
	return d
}

func (d *decoder) u8() uint8 {
	if d.err != nil || len(d.b) < 1 {
		d.err = errCorrupt
		return 0
	}
	v := d.b[0]
	d.b = d.b[1:]
	return v
}

func (d *decoder) u64() uint64 {
	if d.err != nil || len(d.b) < 8 {
		d.err = errCorrupt
		return 0
	}
	v := beU64(d.b)
	d.b = d.b[8:]
	return v
}

func (d *decoder) bytes() []byte {
	n := d.u64()
	if d.err != nil {
		return nil
	}
	if uint64(len(d.b)) < n {
		d.err = errCorrupt
		return nil
	}
	v := d.b[:n]
	d.b = d.b[n:]
	return append([]byte(nil), v...)
}

func (d *decoder) hash() (h [32]byte) {
	if d.err != nil || len(d.b) < 32 {
		d.err = errCorrupt
		return h
	}
	copy(h[:], d.b[:32])
	d.b = d.b[32:]
	return h
}

func (d *decoder) id16() (h [16]byte) {
	if d.err != nil || len(d.b) < 16 {
		d.err = errCorrupt
		return h
	}
	copy(h[:], d.b[:16])
	d.b = d.b[16:]
	return h
}

func (d *decoder) done() error {
	if d.err != nil {
		return d.err
	}
	if len(d.b) != 0 {
		return fmt.Errorf("%w: %d trailing bytes", errCorrupt, len(d.b))
	}
	return nil
}

// Identity is the persistent producer/run identity created with the store.
type Identity struct {
	ProducerID [16]byte
	RunID      [16]byte
}

func encodeIdentity(v Identity) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.id16(v.ProducerID)
	e.id16(v.RunID)
	return e.b
}

func decodeIdentity(b []byte) (Identity, error) {
	d := newDecoder(b)
	v := Identity{ProducerID: d.id16(), RunID: d.id16()}
	return v, d.done()
}

// ModeState is the durable producer mode and admission generation
// (DELIVERY_STATE_MACHINE "Режим producer", drain and cutover D54/D57/D59).
type ModeState struct {
	Mode                Mode
	AdmissionGeneration uint64
}

func encodeMode(v ModeState) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.u8(uint8(v.Mode))
	e.u64(v.AdmissionGeneration)
	return e.b
}

func decodeMode(b []byte) (ModeState, error) {
	d := newDecoder(b)
	v := ModeState{Mode: Mode(d.u8()), AdmissionGeneration: d.u64()}
	if d.err == nil && v.Mode > ModeBLOCKED {
		return v, fmt.Errorf("%w: unknown mode %d", errCorrupt, v.Mode)
	}
	return v, d.done()
}

// Counters holds the monotonic allocation counters. Values start at 1; zero
// is only the empty frontier (CONTROL_WIRE_CONTRACT "Базовые кодирования").
type Counters struct {
	BatchSeqNext      uint64
	EventSequenceNext uint64
}

func encodeCounters(v Counters) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.u64(v.BatchSeqNext)
	e.u64(v.EventSequenceNext)
	return e.b
}

func decodeCounters(b []byte) (Counters, error) {
	d := newDecoder(b)
	v := Counters{BatchSeqNext: d.u64(), EventSequenceNext: d.u64()}
	return v, d.done()
}

// Intent is the durable delivery intent: batch_seq is allocated atomically
// with this record, so the absence of an intent means the absence of an
// allocated batch.
type Intent struct {
	Decision            uint64 // native decision sequence the batch belongs to
	AttemptGeneration   uint64 // current attempt generation, starts at 1
	EventSequenceBase   uint64 // base of the current attempt's event sequence range
	EventCount          uint64
	AdmissionGeneration uint64
}

func encodeIntent(v Intent) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.u64(v.Decision)
	e.u64(v.AttemptGeneration)
	e.u64(v.EventSequenceBase)
	e.u64(v.EventCount)
	e.u64(v.AdmissionGeneration)
	return e.b
}

func decodeIntent(b []byte) (Intent, error) {
	d := newDecoder(b)
	v := Intent{
		Decision:            d.u64(),
		AttemptGeneration:   d.u64(),
		EventSequenceBase:   d.u64(),
		EventCount:          d.u64(),
		AdmissionGeneration: d.u64(),
	}
	return v, d.done()
}

// Ready is the closing READY marker: it binds the manifest digest, the event
// count and the winning attempt generation atomically with the state switch
// and checkpoint.
type Ready struct {
	ManifestDigest [32]byte
	ManifestLength uint64
	EventCount     uint64
	Attempt        uint64
}

func encodeReady(v Ready) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.hash(v.ManifestDigest)
	e.u64(v.ManifestLength)
	e.u64(v.EventCount)
	e.u64(v.Attempt)
	return e.b
}

func decodeReady(b []byte) (Ready, error) {
	d := newDecoder(b)
	v := Ready{ManifestDigest: d.hash(), ManifestLength: d.u64(), EventCount: d.u64(), Attempt: d.u64()}
	return v, d.done()
}

// PlanCurrent names the current (possibly abandoned) plan generation.
type PlanCurrent struct {
	Generation uint64
	Abandoned  bool
}

func encodePlanCurrent(v PlanCurrent) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.u64(v.Generation)
	if v.Abandoned {
		e.u8(1)
	} else {
		e.u8(0)
	}
	return e.b
}

func decodePlanCurrent(b []byte) (PlanCurrent, error) {
	d := newDecoder(b)
	v := PlanCurrent{Generation: d.u64()}
	flag := d.u8()
	if d.err == nil && flag > 1 {
		return v, fmt.Errorf("%w: plan flag %d", errCorrupt, flag)
	}
	v.Abandoned = flag == 1
	return v, d.done()
}

// PlanHeader is the per-generation plan header: the reservation seed bytes
// (whose SHA-256 is the plan digest), the layout manifest digest and the
// caller's header bytes.
type PlanHeader struct {
	Seed         []byte
	LayoutDigest [32]byte
	Header       []byte
}

func encodePlanHeader(v PlanHeader) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.bytes(v.Seed)
	e.hash(v.LayoutDigest)
	e.bytes(v.Header)
	return e.b
}

func decodePlanHeader(b []byte) (PlanHeader, error) {
	d := newDecoder(b)
	v := PlanHeader{Seed: d.bytes(), LayoutDigest: d.hash(), Header: d.bytes()}
	return v, d.done()
}

// Sealed is the closing SEALED marker: after it the reservation, layout,
// headers and TransportFrame bytes are immutable.
type Sealed struct {
	PlanDigest   [32]byte
	LayoutDigest [32]byte
	Generation   uint64
	TotalBytes   uint64
}

func encodeSealed(v Sealed) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.hash(v.PlanDigest)
	e.hash(v.LayoutDigest)
	e.u64(v.Generation)
	e.u64(v.TotalBytes)
	return e.b
}

func decodeSealed(b []byte) (Sealed, error) {
	d := newDecoder(b)
	v := Sealed{PlanDigest: d.hash(), LayoutDigest: d.hash(), Generation: d.u64(), TotalBytes: d.u64()}
	return v, d.done()
}

// Cleanup tracks the bounded ACKED_CLEANUP payload deletion.
type Cleanup struct {
	Done bool
}

func encodeCleanup(v Cleanup) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	if v.Done {
		e.u8(1)
	} else {
		e.u8(0)
	}
	return e.b
}

func decodeCleanup(b []byte) (Cleanup, error) {
	d := newDecoder(b)
	flag := d.u8()
	if d.err == nil && flag > 1 {
		return Cleanup{}, fmt.Errorf("%w: cleanup flag %d", errCorrupt, flag)
	}
	return Cleanup{Done: flag == 1}, d.done()
}
