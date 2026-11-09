// Package capture is the owned capture seam that the Geth, BSC and Bor adapters
// share (E01-02.owned_capture, REQ-CAP-05 and REQ-CAP-06).
//
// One native goroutine, the producer, copies native data into immutable frames.
// One worker goroutine reads the frames in order and releases each one after it
// has stored it. The rules are:
//
//   - A frame holds only value fields and a string. Go strings are immutable,
//     so a worker can never see or change native memory through a frame.
//   - A field is split into BEGIN_FIELD, CHUNK and END_FIELD frames with a
//     sequence, an offset, a length and a SHA-256 digest. No call copies a
//     whole field at once.
//   - All frames that are queued, delivered but not released, or reserved as
//     callback credit share one fixed byte budget.
//   - A producer waits for budget only at a safe point, outside a native
//     callback. Inside a callback every waiting call fails at once with
//     ErrInCallback, and Note never waits: it spends credit that a safe point
//     reserved earlier.
//   - Any producer error stops the capture (fail closed). The worker then
//     receives the same error instead of a partial result.
//
// The package performs no disk or network I/O. Writing frames to a spool is the
// worker's job.
package capture

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	// MaxChunkBytes is the largest chunk payload. It equals the BlobChunk data
	// limit in DELIVERY_STATE_MACHINE.
	MaxChunkBytes = 16384
	// MaxNoteBytes is the largest payload of a callback note.
	MaxNoteBytes = 256
	// FrameOverhead is the budget charged for every frame in addition to its
	// payload. It is at least the in-memory size of a Frame value.
	FrameOverhead = 128
	// MaxBufferBytes limits the budget so the preallocated frame ring stays
	// addressable on every supported platform.
	MaxBufferBytes = 1 << 32
)

// FrameKind identifies the role of a frame in the raw spool.
type FrameKind uint8

const (
	// BeginField starts a field. Length is the total field length.
	BeginField FrameKind = 1
	// Chunk carries one bounded part of a field and the digest of that part.
	Chunk FrameKind = 2
	// EndField closes a field. Length and Digest cover the whole field.
	EndField FrameKind = 3
	// Note carries one small value recorded inside a native callback.
	Note FrameKind = 4
)

var (
	// ErrConfig reports an invalid Config.
	ErrConfig = errors.New("capture: invalid configuration")
	// ErrInCallback reports a waiting call made inside a native callback.
	ErrInCallback = errors.New("capture: a native callback cannot wait or copy a field")
	// ErrNotInCallback reports a callback-only call made outside a callback.
	ErrNotInCallback = errors.New("capture: the call requires an open native callback")
	// ErrNestedCallback reports a callback opened inside another callback.
	ErrNestedCallback = errors.New("capture: native callbacks cannot nest")
	// ErrCredit reports a note that needs more credit than a safe point reserved.
	ErrCredit = errors.New("capture: the note exceeds the credit reserved at a safe point")
	// ErrTooLarge reports a frame or reservation that can never fit the budget.
	ErrTooLarge = errors.New("capture: the request exceeds the capture budget")
	// ErrBusy reports a second producer call while one is still running.
	ErrBusy = errors.New("capture: only one producer call may run at a time")
	// ErrClosed reports a producer call after Close.
	ErrClosed = errors.New("capture: the capture is closed")
	// ErrRelease reports a release of a frame that is not the oldest delivered frame.
	ErrRelease = errors.New("capture: release must name the oldest delivered frame")
)

// ReadFunc copies field bytes starting at offset into dst. It must fill all of
// dst from a native view that stays unchanged while the field is captured, or
// return an error. dst is valid only during the call; the capture copies it
// into an immutable frame before the next call.
type ReadFunc func(offset uint64, dst []byte) error

// Config sets the bounds of one capture.
type Config struct {
	// ChunkBytes is the payload size of a full chunk, from 1 to MaxChunkBytes.
	ChunkBytes int
	// BufferBytes is the byte budget for all held frames and reserved credit.
	// It must fit at least one full chunk or one maximum note with its overhead.
	BufferBytes int64
}

// Frame is one immutable record of the raw spool. Its payload is a string, so
// the frame owns its bytes and shares no memory with the native side.
type Frame struct {
	Sequence uint64
	Field    uint64
	Offset   uint64
	Length   uint64
	Digest   [sha256.Size]byte
	Tag      uint32
	Kind     FrameKind
	data     string
}

// Data returns a new copy of the frame payload.
func (f Frame) Data() []byte { return []byte(f.data) }

// DataLen returns the payload length without copying it.
func (f Frame) DataLen() int { return len(f.data) }

// WriteTo writes the payload to w without an intermediate copy.
func (f Frame) WriteTo(w io.Writer) (int64, error) {
	n, err := io.WriteString(w, f.data)
	return int64(n), err
}

func (f Frame) cost() int64 { return FrameOverhead + int64(len(f.data)) }

// Stats is a snapshot of the capture accounting.
type Stats struct {
	// Held is the budget in use: queued, delivered and unreleased frames plus credit.
	Held int64
	// PeakHeld is the largest Held value observed.
	PeakHeld int64
	// Credit is the unused callback credit reserved at safe points.
	Credit int64
	// Queued counts frames not yet delivered to the worker.
	Queued int
	// Delivered counts frames delivered to the worker and not yet released.
	Delivered int
	// Frames counts all frames created.
	Frames uint64
	// Waits counts producer calls that had to wait for budget.
	Waits uint64
	// ProducerWaiting reports whether a producer call is waiting now.
	ProducerWaiting bool
	// InCallback reports whether a native callback is open now.
	InCallback bool
}

// Capturer is one owned capture between a native producer and one worker.
type Capturer struct {
	chunkBytes  int
	bufferBytes int64
	scratch     []byte

	mu         sync.Mutex
	changed    chan struct{}
	ring       []Frame
	head       int // oldest unreleased frame
	next       int // next frame to deliver
	count      int // frames in the ring
	delivered  int // delivered and not yet released
	held       int64
	credit     int64
	sequence   uint64
	err        error
	closed     bool
	busy       bool
	inCallback bool
	stats      Stats
}

// validate checks cfg without allocating anything.
func validate(cfg Config) error {
	if cfg.ChunkBytes < 1 || cfg.ChunkBytes > MaxChunkBytes {
		return fmt.Errorf("%w: ChunkBytes %d is outside 1..%d", ErrConfig, cfg.ChunkBytes, MaxChunkBytes)
	}
	least := int64(FrameOverhead + max(cfg.ChunkBytes, MaxNoteBytes))
	if cfg.BufferBytes < least || cfg.BufferBytes > MaxBufferBytes {
		return fmt.Errorf("%w: BufferBytes %d is outside %d..%d", ErrConfig, cfg.BufferBytes, least, int64(MaxBufferBytes))
	}
	return nil
}

// New validates cfg and preallocates the bounded frame ring.
func New(cfg Config) (*Capturer, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}
	return &Capturer{
		chunkBytes:  cfg.ChunkBytes,
		bufferBytes: cfg.BufferBytes,
		scratch:     make([]byte, cfg.ChunkBytes),
		changed:     make(chan struct{}),
		// Every frame costs at least FrameOverhead, so the budget bounds the count.
		ring: make([]Frame, cfg.BufferBytes/FrameOverhead),
	}, nil
}

// notify wakes every waiter. The caller holds c.mu.
func (c *Capturer) notify() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// fail stops the capture with err unless it already stopped. The caller holds c.mu.
func (c *Capturer) fail(err error) error {
	if c.err == nil {
		c.err = err
		// The worker gets the error, so queued frames are no longer needed.
		for c.count > 0 {
			c.ring[c.head] = Frame{}
			c.head = (c.head + 1) % len(c.ring)
			c.count--
		}
		c.next = c.head
		c.delivered = 0
		c.held = 0
		c.credit = 0
		c.notify()
	}
	return c.err
}

// Abort stops the capture with err. Waiting producer and worker calls return err.
func (c *Capturer) Abort(err error) {
	if err == nil {
		err = errors.New("capture: aborted")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail(err)
}

// Close ends production. The worker receives io.EOF after the last frame.
// Unused credit returns to the budget.
func (c *Capturer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if c.busy {
		return c.fail(ErrBusy)
	}
	if c.inCallback {
		return c.fail(ErrInCallback)
	}
	c.closed = true
	c.held -= c.credit
	c.credit = 0
	c.notify()
	return nil
}

// EnterCallback marks the start of a native callback. Until ExitCallback, every
// waiting call fails at once with ErrInCallback.
func (c *Capturer) EnterCallback() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if c.inCallback {
		return c.fail(ErrNestedCallback)
	}
	c.inCallback = true
	return nil
}

// ExitCallback marks the end of a native callback.
func (c *Capturer) ExitCallback() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if !c.inCallback {
		return c.fail(ErrNotInCallback)
	}
	c.inCallback = false
	return nil
}

// startProducer checks that a waiting producer call may begin. The caller holds c.mu.
func (c *Capturer) startProducer() error {
	switch {
	case c.err != nil:
		return c.err
	case c.inCallback:
		return c.fail(ErrInCallback)
	case c.busy:
		return c.fail(ErrBusy)
	case c.closed:
		return c.fail(ErrClosed)
	}
	c.busy = true
	return nil
}

// waitFor waits at a safe point until amount fits the budget. It returns with
// c.mu held and the amount added to c.held. The caller holds c.mu.
func (c *Capturer) waitFor(ctx context.Context, amount int64) error {
	if amount > c.bufferBytes {
		return c.fail(ErrTooLarge)
	}
	counted := false
	for c.held+amount > c.bufferBytes {
		if c.count == 0 {
			// Only reserved credit is held, and no worker release can free it.
			return c.fail(ErrTooLarge)
		}
		if !counted {
			c.stats.Waits++
			counted = true
		}
		c.stats.ProducerWaiting = true
		changed := c.changed
		c.mu.Unlock()
		var cause error
		select {
		case <-changed:
		case <-ctx.Done():
			cause = ctx.Err()
		}
		c.mu.Lock()
		c.stats.ProducerWaiting = false
		if c.err != nil {
			return c.err
		}
		if cause != nil {
			return c.fail(cause)
		}
	}
	c.held += amount
	c.stats.PeakHeld = max(c.stats.PeakHeld, c.held)
	return nil
}

// push appends a frame whose cost is already in c.held. The caller holds c.mu.
func (c *Capturer) push(f Frame) {
	c.sequence++
	f.Sequence = c.sequence
	if f.Kind == BeginField {
		f.Field = f.Sequence
	}
	c.ring[(c.head+c.count)%len(c.ring)] = f
	c.count++
	c.stats.Frames++
	c.notify()
}

// Reserve waits at a safe point until n bytes of callback credit fit the budget.
func (c *Capturer) Reserve(ctx context.Context, n int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.startProducer(); err != nil {
		return err
	}
	defer func() { c.busy = false }()
	if n < 1 {
		return c.fail(fmt.Errorf("%w: reservation %d is not positive", ErrConfig, n))
	}
	if err := c.waitFor(ctx, n); err != nil {
		return err
	}
	c.credit += n
	return nil
}

// Note records a small value inside a native callback. It never waits: the
// frame spends credit that a safe point reserved with Reserve.
func (c *Capturer) Note(tag uint32, value []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.err != nil:
		return c.err
	case !c.inCallback:
		return c.fail(ErrNotInCallback)
	case c.closed:
		return c.fail(ErrClosed)
	case len(value) > MaxNoteBytes:
		return c.fail(ErrTooLarge)
	}
	f := Frame{Kind: Note, Tag: tag, Length: uint64(len(value)), Digest: sha256.Sum256(value), data: string(value)}
	if f.cost() > c.credit {
		return c.fail(ErrCredit)
	}
	c.credit -= f.cost()
	c.push(f)
	return nil
}

// CaptureBytes copies src as one field. src must not change during the call.
func (c *Capturer) CaptureBytes(ctx context.Context, tag uint32, src []byte) error {
	return c.CaptureField(ctx, tag, uint64(len(src)), func(offset uint64, dst []byte) error {
		copy(dst, src[offset:])
		return nil
	})
}

// CaptureField copies a field of length bytes through read, one bounded chunk
// at a time. It may wait for budget before each frame, so the caller must be
// at a safe point outside any native callback where the native view is stable.
func (c *Capturer) CaptureField(ctx context.Context, tag uint32, length uint64, read ReadFunc) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.startProducer(); err != nil {
		return err
	}
	defer func() { c.busy = false }()
	if err := c.waitFor(ctx, FrameOverhead); err != nil {
		return err
	}
	c.push(Frame{Kind: BeginField, Tag: tag, Length: length})
	field := c.sequence
	whole := sha256.New()
	for offset := uint64(0); offset < length; {
		size := uint64(c.chunkBytes)
		if rest := length - offset; rest < size {
			size = rest
		}
		if err := c.waitFor(ctx, FrameOverhead+int64(size)); err != nil {
			return err
		}
		dst := c.scratch[:size]
		// The native view is stable at a safe point, so the copy and the
		// digests run without the lock. The string copy makes the chunk owned.
		c.mu.Unlock()
		err := safeRead(read, offset, dst)
		chunk := Frame{Kind: Chunk, Tag: tag, Field: field, Offset: offset, Length: size}
		if err == nil {
			chunk.data = string(dst)
			chunk.Digest = sha256.Sum256(dst)
			whole.Write(dst)
		}
		c.mu.Lock()
		if c.err != nil {
			return c.err
		}
		if err != nil {
			return c.fail(fmt.Errorf("capture: read field at offset %d: %w", offset, err))
		}
		c.push(chunk)
		offset += size
	}
	if err := c.waitFor(ctx, FrameOverhead); err != nil {
		return err
	}
	end := Frame{Kind: EndField, Tag: tag, Field: field, Offset: length, Length: length}
	whole.Sum(end.Digest[:0])
	c.push(end)
	return nil
}

// safeRead calls read and turns a panic into an error, so the capture stops
// closed and its lock state stays consistent.
func safeRead(read ReadFunc, offset uint64, dst []byte) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("capture: ReadFunc panicked: %v", recovered)
		}
	}()
	return read(offset, dst)
}

// Next waits for the next frame. It returns io.EOF after Close and the last
// frame, or the error that stopped the capture.
func (c *Capturer) Next(ctx context.Context) (Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		if c.err != nil {
			return Frame{}, c.err
		}
		if c.delivered < c.count {
			f := c.ring[c.next]
			c.next = (c.next + 1) % len(c.ring)
			c.delivered++
			return f, nil
		}
		if c.closed {
			return Frame{}, io.EOF
		}
		changed := c.changed
		c.mu.Unlock()
		var cause error
		select {
		case <-changed:
		case <-ctx.Done():
			cause = ctx.Err()
		}
		c.mu.Lock()
		if cause != nil {
			return Frame{}, cause
		}
	}
}

// Release returns the budget of f, which must be the oldest delivered frame.
func (c *Capturer) Release(f Frame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if c.delivered == 0 || c.ring[c.head] != f {
		return c.fail(ErrRelease)
	}
	c.held -= f.cost()
	c.ring[c.head] = Frame{}
	c.head = (c.head + 1) % len(c.ring)
	c.count--
	c.delivered--
	c.notify()
	return nil
}

// Stats returns a snapshot of the accounting.
func (c *Capturer) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	s.Held = c.held
	s.Credit = c.credit
	s.Queued = c.count - c.delivered
	s.Delivered = c.delivered
	s.InCallback = c.inCallback
	return s
}

// Err returns the error that stopped the capture, or nil.
func (c *Capturer) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}
