package eth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"

	"github.com/sigaev-pro/bc-indexer/core/capture"
)

// Import errors.
var (
	// ErrImport reports a malformed or inconsistent import stream.
	ErrImport = errors.New("eth: invalid import stream")
)

// Field tags of the import path. Structured events travel as whole JSON
// fields; large byte blobs (receipt consensus bytes, raw encodings, code,
// log data) travel as TagBytes fields whose payload starts with the 32-byte
// SHA-256 content key the wire event references.
const (
	TagBlockEvent uint32 = 1
	TagTxEvent    uint32 = 2
	TagBytes      uint32 = 3
)

// FrameSource is the worker side of the capture seam. *capture.Capturer
// satisfies it: Next delivers immutable frames in order and Release
// returns the frame's byte budget.
type FrameSource interface {
	Next(ctx context.Context) (capture.Frame, error)
	Release(f capture.Frame) error
}

// ByteStore is the bounded byte spool of large fields: one streaming put
// under a content key and one read-back. The collector streams chunk
// payloads into the store as the frames arrive and verifies the whole-field
// digest at the field end, so it never buffers a whole large field outside
// the record that retains it.
type ByteStore interface {
	Put(key []byte, size int64) (io.WriteCloser, error)
	Get(key []byte) ([]byte, error)
}

// RecordSink receives every completed record of the import path. A slow
// sink slows the worker; the producer then waits inside the capture budget,
// which is the bounded backpressure property E04-04 tests.
type RecordSink interface {
	EmitBlock(record ImportedBlock) error
	EmitOutsideTx(block Hash32, record *SystemBalanceRecord) error
}

// ImportedBlock is one imported block: the block observation and the
// normalized transaction records in body order.
type ImportedBlock struct {
	Observation BlockObservation
	Records     []*TransactionRecord
}

// Collector is the import-path worker (E04-04): it reads the frame stream,
// streams large fields into the byte store, reassembles the wire events and
// assembles the normalized records through the same builders a direct
// caller uses. Its own buffering is bounded: exactly one field is in
// flight, structured events are unmarshalled one at a time, and every
// frame is released as soon as its payload is consumed, so the capture
// budget — not the collector — bounds the in-flight memory. The assembled
// record retains what the contract requires it to retain (raw encodings,
// source receipt bytes); that retention is the record, not import-path
// buffering.
type Collector struct {
	source FrameSource
	store  ByteStore
	sink   RecordSink
	decl   ScopeDeclaration

	// One field in flight.
	tag       uint32
	buf       []byte // structured event accumulation
	inBytes   bool
	key       []byte
	keyFilled int
	writer    io.WriteCloser
	digest    hash.Hash
	written   int64
	size      int64

	block *wireBlockState
}

// NewCollector builds a collector over one frame source. decl is the D17
// scope declaration; the default OFF declaration keeps every outside-tx
// observation out of the public output without touching the tx records.
func NewCollector(source FrameSource, store ByteStore, sink RecordSink, decl ScopeDeclaration) (*Collector, error) {
	if source == nil || sink == nil {
		return nil, fmt.Errorf("%w: nil source or sink", ErrImport)
	}
	return &Collector{source: source, store: store, sink: sink, decl: decl}, nil
}

// Run consumes the stream until EOF or error and flushes the last block.
// A store, digest or assembly error stops the import; the frame being
// processed is still released.
func (c *Collector) Run(ctx context.Context) error {
	for {
		frame, err := c.source.Next(ctx)
		if errors.Is(err, io.EOF) {
			return c.flushBlock()
		}
		if err != nil {
			return err
		}
		err = c.step(frame)
		if relErr := c.source.Release(frame); relErr != nil && err == nil {
			err = relErr
		}
		if err != nil {
			return err
		}
	}
}

// step processes one frame of the in-flight field.
func (c *Collector) step(frame capture.Frame) error {
	switch frame.Kind {
	case capture.BeginField:
		if c.tag != 0 {
			return fmt.Errorf("%w: nested begin", ErrImport)
		}
		c.tag = frame.Tag
		c.buf = c.buf[:0]
		c.inBytes = frame.Tag == TagBytes
		c.key = nil
		c.keyFilled = 0
		c.writer = nil
		c.digest = sha256.New()
		c.written = 0
		c.size = int64(frame.Length)
		if c.inBytes && frame.Length < 32 {
			return fmt.Errorf("%w: bytes field shorter than its key", ErrImport)
		}
		return nil
	case capture.Chunk:
		if c.tag == 0 {
			return fmt.Errorf("%w: chunk outside a field", ErrImport)
		}
		data := frame.Data()
		c.digest.Write(data)
		c.written += int64(len(data))
		if c.written > c.size {
			return fmt.Errorf("%w: field grew past its declared length", ErrImport)
		}
		if c.inBytes {
			return c.streamBytes(data)
		}
		c.buf = append(c.buf, data...)
		return nil
	case capture.EndField:
		if c.tag == 0 {
			return fmt.Errorf("%w: end outside a field", ErrImport)
		}
		var sum [sha256.Size]byte
		c.digest.Sum(sum[:0])
		if sum != frame.Digest {
			return fmt.Errorf("%w: field digest mismatch", ErrImport)
		}
		if c.inBytes {
			if c.writer != nil {
				if err := c.writer.Close(); err != nil {
					return fmt.Errorf("%w: %v", ErrImport, err)
				}
			}
		}
		tag := c.tag
		c.tag = 0
		c.writer = nil
		if tag == TagBytes {
			return nil
		}
		return c.dispatch(tag)
	case capture.Note:
		return fmt.Errorf("%w: notes are not part of the v1 import wire", ErrImport)
	default:
		return fmt.Errorf("%w: frame kind %d", ErrImport, uint8(frame.Kind))
	}
}

// streamBytes routes one chunk of a bytes field: the first 32 bytes are
// the content key, the rest streams into the store writer opened on key
// completion.
func (c *Collector) streamBytes(data []byte) error {
	if c.keyFilled < 32 {
		take := 32 - c.keyFilled
		if take > len(data) {
			take = len(data)
		}
		c.key = append(c.key, data[:take]...)
		c.keyFilled += take
		data = data[take:]
		if c.keyFilled < 32 {
			return nil
		}
		if c.store == nil {
			return fmt.Errorf("%w: bytes field without a store", ErrImport)
		}
		w, err := c.store.Put(c.key, c.size-32)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrImport, err)
		}
		c.writer = w
	}
	if len(data) == 0 {
		return nil
	}
	if c.writer == nil {
		return fmt.Errorf("%w: bytes payload without a key", ErrImport)
	}
	if _, err := c.writer.Write(data); err != nil {
		return fmt.Errorf("%w: %v", ErrImport, err)
	}
	return nil
}

// dispatch finishes one structured event field.
func (c *Collector) dispatch(tag uint32) error {
	switch tag {
	case TagBlockEvent:
		if err := c.flushBlock(); err != nil {
			return err
		}
		var event wireBlock
		if err := jsonUnmarshalStrict(c.buf, &event); err != nil {
			return fmt.Errorf("%w: %v", ErrImport, err)
		}
		// Resolve the header blobs now: the referenced TagBytes fields
		// arrived before this event.
		if _, err := event.observation(c.resolveBlob); err != nil {
			return err
		}
		c.block = &wireBlockState{event: event}
		return nil
	case TagTxEvent:
		if c.block == nil {
			return fmt.Errorf("%w: tx event outside a block", ErrImport)
		}
		var event wireTx
		if err := jsonUnmarshalStrict(c.buf, &event); err != nil {
			return fmt.Errorf("%w: %v", ErrImport, err)
		}
		obs, err := event.observation(c.resolveBlob)
		if err != nil {
			return err
		}
		record, err := AssembleTx(obs)
		if err != nil {
			return err
		}
		c.block.records = append(c.block.records, record)
		return nil
	default:
		return fmt.Errorf("%w: event tag %d", ErrImport, tag)
	}
}

// flushBlock emits the completed block and applies the D17 declaration to
// its outside-tx observations, in native order after the transactions.
func (c *Collector) flushBlock() error {
	if c.block == nil {
		return nil
	}
	state := c.block
	c.block = nil
	observation, err := state.event.observation(c.resolveBlob)
	if err != nil {
		return err
	}
	imported := ImportedBlock{Observation: observation,
		Records: append([]*TransactionRecord(nil), state.records...)}
	if err := c.sink.EmitBlock(imported); err != nil {
		return err
	}
	for _, outside := range state.event.Outside {
		record, err := ApplyOutsideTx(c.decl, outside.observation())
		if err != nil {
			return err
		}
		if record == nil {
			continue
		}
		if err := c.sink.EmitOutsideTx(observation.Hash, record); err != nil {
			return err
		}
	}
	return nil
}

// resolveBlob reads one wire byte value: inline hex directly, a store
// reference through the store with a content-key check.
func (c *Collector) resolveBlob(w wireBytes) ([]byte, error) {
	if w.Key == "" {
		data, err := hex.DecodeString(w.Hex)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrImport, err)
		}
		return data, nil
	}
	key, err := hex.DecodeString(w.Key)
	if err != nil || len(key) != sha256.Size {
		return nil, fmt.Errorf("%w: bad blob key", ErrImport)
	}
	if c.store == nil {
		return nil, fmt.Errorf("%w: referenced blob without a store", ErrImport)
	}
	data, err := c.store.Get(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrImport, err)
	}
	sum := sha256.Sum256(data)
	if string(sum[:]) != string(key) {
		return nil, fmt.Errorf("%w: blob content does not match its key", ErrImport)
	}
	return data, nil
}
