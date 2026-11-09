package capture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"reflect"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

func newCapturer(t *testing.T, chunk int, buffer int64) *Capturer {
	t.Helper()
	c, err := New(Config{ChunkBytes: chunk, BufferBytes: buffer})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// drain reads and releases every frame until io.EOF or an error.
func drain(t *testing.T, c *Capturer) ([]Frame, error) {
	t.Helper()
	var frames []Frame
	for {
		f, err := c.Next(context.Background())
		if err != nil {
			if errors.Is(err, io.EOF) {
				return frames, nil
			}
			return frames, err
		}
		frames = append(frames, f)
		if err := c.Release(f); err != nil {
			return frames, err
		}
	}
}

// field rebuilds one field from its frames and checks the framing rules.
func field(t *testing.T, frames []Frame) []byte {
	t.Helper()
	if len(frames) < 2 || frames[0].Kind != BeginField || frames[len(frames)-1].Kind != EndField {
		t.Fatalf("field is not framed: %+v", frames)
	}
	begin, end := frames[0], frames[len(frames)-1]
	var out bytes.Buffer
	for i, f := range frames {
		if f.Field != begin.Sequence || f.Tag != begin.Tag {
			t.Fatalf("frame %d has field %d tag %d", i, f.Field, f.Tag)
		}
		if i > 0 && f.Sequence != frames[i-1].Sequence+1 {
			t.Fatalf("sequence gap at frame %d", i)
		}
		if f.Kind == Chunk {
			if f.Offset != uint64(out.Len()) || f.Length != uint64(f.DataLen()) || f.Digest != sha256.Sum256(f.Data()) {
				t.Fatalf("chunk %d has a wrong offset, length or digest", i)
			}
			if _, err := f.WriteTo(&out); err != nil {
				t.Fatal(err)
			}
		}
	}
	if begin.Length != uint64(out.Len()) || end.Length != uint64(out.Len()) || end.Offset != uint64(out.Len()) ||
		end.Digest != sha256.Sum256(out.Bytes()) {
		t.Fatal("field length or digest does not match its chunks")
	}
	return out.Bytes()
}

func pattern(n int, seed byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i*7) + seed
	}
	return out
}

func waitUntil(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestConfigBounds(t *testing.T) {
	least := int64(FrameOverhead + MaxNoteBytes)
	for _, cfg := range []Config{
		{ChunkBytes: 0, BufferBytes: 1 << 20},
		{ChunkBytes: MaxChunkBytes + 1, BufferBytes: 1 << 20},
		{ChunkBytes: 64, BufferBytes: least - 1},
		{ChunkBytes: 4096, BufferBytes: FrameOverhead + 4096 - 1},
		{ChunkBytes: 64, BufferBytes: MaxBufferBytes + 1},
	} {
		if _, err := New(cfg); !errors.Is(err, ErrConfig) {
			t.Fatalf("%+v: got %v, want ErrConfig", cfg, err)
		}
	}
	for _, cfg := range []Config{
		{ChunkBytes: 1, BufferBytes: least},
		{ChunkBytes: MaxChunkBytes, BufferBytes: FrameOverhead + MaxChunkBytes},
	} {
		if _, err := New(cfg); err != nil {
			t.Fatalf("%+v: %v", cfg, err)
		}
	}
}

// TestFrameOwnsNoNativeMemory checks the type graph a worker can reach: only
// value fields and immutable strings, never a slice, map, pointer or function.
func TestFrameOwnsNoNativeMemory(t *testing.T) {
	var check func(reflect.Type)
	check = func(typ reflect.Type) {
		switch typ.Kind() {
		case reflect.Bool, reflect.Int, reflect.Int64, reflect.Uint8, reflect.Uint32, reflect.Uint64, reflect.String:
		case reflect.Array:
			check(typ.Elem())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				check(typ.Field(i).Type)
			}
		default:
			t.Fatalf("worker-visible type %s can alias memory", typ)
		}
	}
	check(reflect.TypeOf(Frame{}))
	check(reflect.TypeOf(Stats{}))
	if size := unsafe.Sizeof(Frame{}); size > FrameOverhead {
		t.Fatalf("Frame is %d bytes, more than FrameOverhead", size)
	}
}

// TestOverlappingSourceChangesAfterCapture models AC-CAP-05-01: the native
// buffer is overwritten after the safe point while the worker is still held.
func TestOverlappingSourceChangesAfterCapture(t *testing.T) {
	c := newCapturer(t, 64, 1<<16)
	memory := pattern(1000, 3)
	want := append([]byte(nil), memory...)
	if err := c.CaptureBytes(context.Background(), 7, memory); err != nil {
		t.Fatal(err)
	}
	copy(memory, pattern(1000, 99)) // the call output overwrites the input region
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	frames, err := drain(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if got := field(t, frames); !bytes.Equal(got, want) {
		t.Fatal("captured bytes changed with the native buffer")
	}
	data := frames[1].Data()
	data[0] ^= 0xff
	if frames[1].Data()[0] == data[0] {
		t.Fatal("Data returned shared memory")
	}
	if len(frames) != 1+16+1 || frames[16].DataLen() != 1000-15*64 {
		t.Fatalf("got %d frames, want BEGIN, 16 chunks and END", len(frames))
	}
}

func TestEmptyField(t *testing.T) {
	c := newCapturer(t, 64, 1<<12)
	if err := c.CaptureBytes(context.Background(), 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	frames, err := drain(t, c)
	if err != nil || len(frames) != 2 {
		t.Fatalf("got %d frames, %v", len(frames), err)
	}
	if got := field(t, frames); len(got) != 0 {
		t.Fatal("empty field has data")
	}
}

// TestLargeFieldStaysInBudget captures a field 1024 times larger than the
// budget through a slow worker. Held bytes never exceed the budget, the live
// heap stays far below the field size, and every byte arrives.
func TestLargeFieldStaysInBudget(t *testing.T) {
	const size = 64 << 20
	const budget = 64 << 10
	c := newCapturer(t, 4096, budget)
	source := func(offset uint64, dst []byte) error {
		for i := range dst {
			dst[i] = byte((offset + uint64(i)) * 13)
		}
		return nil
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	done := make(chan error, 1)
	go func() {
		err := c.CaptureField(context.Background(), 9, size, source)
		if err == nil {
			err = c.Close()
		}
		done <- err
	}()
	digest := sha256.New()
	var total uint64
	var peak uint64
	var end Frame
	for n := 0; ; n++ {
		f, err := c.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if f.Kind == Chunk {
			f.WriteTo(digest)
			total += f.Length
		}
		if f.Kind == EndField {
			end = f
		}
		if n%1024 == 0 {
			time.Sleep(time.Millisecond) // a slow spool writer
			var now runtime.MemStats
			runtime.ReadMemStats(&now)
			peak = max(peak, now.HeapAlloc)
		}
		if err := c.Release(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	stats := c.Stats()
	if total != size || [32]byte(digest.Sum(nil)) != end.Digest {
		t.Fatal("large field lost or changed bytes")
	}
	if stats.PeakHeld > budget || stats.Waits == 0 || stats.Held != 0 {
		t.Fatalf("budget accounting is wrong: %+v", stats)
	}
	if peak > before.HeapAlloc && peak-before.HeapAlloc > size/4 {
		t.Fatalf("live heap grew by %d bytes for a %d byte field", peak-before.HeapAlloc, size)
	}
}

// TestSafePointWaitsForWorker models AC-CAP-06-02: with the worker held, the
// producer stops at a safe point, makes no progress, and resumes on release.
func TestSafePointWaitsForWorker(t *testing.T) {
	c := newCapturer(t, 256, 4*(FrameOverhead+256))
	src := pattern(4096, 5)
	done := make(chan error, 1)
	go func() { done <- c.CaptureBytes(context.Background(), 2, src) }()
	waitUntil(t, "the producer waits", func() bool { return c.Stats().ProducerWaiting })
	paused := c.Stats()
	time.Sleep(20 * time.Millisecond)
	if again := c.Stats(); again.Frames != paused.Frames || !again.ProducerWaiting || again.InCallback {
		t.Fatalf("producer moved while the worker was held: %+v then %+v", paused, again)
	}
	select {
	case err := <-done:
		t.Fatalf("capture finished while the worker was held: %v", err)
	default:
	}
	go func() {
		if err := <-done; err != nil {
			c.Abort(err)
			return
		}
		c.Close()
	}()
	frames, err := drain(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(field(t, frames), src) || c.Stats().PeakHeld > 4*(FrameOverhead+256) {
		t.Fatal("resumed capture is wrong or exceeded the budget")
	}
}

func TestCallbackRules(t *testing.T) {
	ctx := context.Background()
	for name, call := range map[string]func(*Capturer) error{
		"capture": func(c *Capturer) error { return c.CaptureBytes(ctx, 1, []byte{1}) },
		"reserve": func(c *Capturer) error { return c.Reserve(ctx, 1) },
		"close":   func(c *Capturer) error { return c.Close() },
	} {
		c := newCapturer(t, 64, 1<<12)
		if err := c.EnterCallback(); err != nil {
			t.Fatal(err)
		}
		if err := call(c); !errors.Is(err, ErrInCallback) {
			t.Fatalf("%s in a callback: got %v", name, err)
		}
		if _, err := c.Next(ctx); !errors.Is(err, ErrInCallback) {
			t.Fatalf("%s: worker got %v, want the stop error", name, err)
		}
	}
	c := newCapturer(t, 64, 1<<12)
	c.EnterCallback()
	if err := c.EnterCallback(); !errors.Is(err, ErrNestedCallback) {
		t.Fatalf("nested callback: %v", err)
	}
	if err := c.ExitCallback(); !errors.Is(err, ErrNestedCallback) {
		t.Fatalf("a stopped capture must keep its first error: %v", err)
	}
	c = newCapturer(t, 64, 1<<12)
	if err := c.ExitCallback(); !errors.Is(err, ErrNotInCallback) {
		t.Fatalf("exit without enter: %v", err)
	}
	if err := c.EnterCallback(); !errors.Is(err, ErrNotInCallback) {
		t.Fatalf("enter after a stop: %v", err)
	}
}

func TestNoteSpendsReservedCredit(t *testing.T) {
	ctx := context.Background()
	c := newCapturer(t, 64, 1<<12)
	if err := c.Note(1, nil); !errors.Is(err, ErrNotInCallback) {
		t.Fatalf("note outside a callback: %v", err)
	}
	c = newCapturer(t, 64, 1<<12)
	c.EnterCallback()
	if err := c.Note(1, []byte{1}); !errors.Is(err, ErrCredit) {
		t.Fatalf("note without credit: %v", err)
	}
	c = newCapturer(t, 64, 1<<12)
	c.EnterCallback()
	if err := c.Note(1, make([]byte, MaxNoteBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized note: %v", err)
	}

	// The budget is full of frames, yet a note with credit returns at once.
	budget := int64(2*FrameOverhead + 300)
	c = newCapturer(t, 64, budget)
	if err := c.Reserve(ctx, FrameOverhead+40); err != nil {
		t.Fatal(err)
	}
	if err := c.Reserve(ctx, budget); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("reservation above the budget: %v", err)
	}
	c = newCapturer(t, 64, budget)
	c.Reserve(ctx, FrameOverhead+40)
	go c.CaptureBytes(ctx, 3, pattern(512, 1))
	waitUntil(t, "the budget is full", func() bool { return c.Stats().ProducerWaiting })
	if err := c.EnterCallback(); err != nil {
		t.Fatal(err)
	}
	value := []byte("call depth 1")
	if err := c.Note(4, value); err != nil {
		t.Fatalf("note with credit: %v", err)
	}
	value[0] = 'X'
	c.ExitCallback()
	if s := c.Stats(); s.Credit != 40-int64(len(value)) || s.InCallback {
		t.Fatalf("credit not spent: %+v", s)
	}
	var note Frame
	for note.Kind != Note {
		f, err := c.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		c.Release(f)
		note = f
	}
	if string(note.Data()) != "call depth 1" || note.Digest != sha256.Sum256([]byte("call depth 1")) || note.Tag != 4 {
		t.Fatalf("note frame is wrong: %+v", note)
	}
}

func TestReserveRules(t *testing.T) {
	ctx := context.Background()
	c := newCapturer(t, 64, 1<<12)
	if err := c.Reserve(ctx, 0); !errors.Is(err, ErrConfig) {
		t.Fatalf("zero reservation: %v", err)
	}
	c = newCapturer(t, 64, 1<<12)
	c.Reserve(ctx, 1<<11)
	if err := c.Reserve(ctx, 1<<11+1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("credit that can never be freed must fail, not hang: %v", err)
	}
	c = newCapturer(t, 64, 1<<12)
	c.Reserve(ctx, 1<<11)
	if err := c.Close(); err != nil || c.Stats().Held != 0 || c.Stats().Credit != 0 {
		t.Fatalf("close must return unused credit: %v %+v", err, c.Stats())
	}
	c.EnterCallback()
	if err := c.Note(1, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("note after close: %v", err)
	}
}

func TestReserveWaitsForRelease(t *testing.T) {
	ctx := context.Background()
	c := newCapturer(t, 64, FrameOverhead+MaxNoteBytes)
	c.CaptureBytes(ctx, 1, nil) // BEGIN and END fill the budget
	done := make(chan error, 1)
	go func() { done <- c.Reserve(ctx, MaxNoteBytes) }()
	waitUntil(t, "the reservation waits", func() bool { return c.Stats().ProducerWaiting })
	f, _ := c.Next(ctx)
	if err := c.Release(f); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s := c.Stats(); s.Credit != MaxNoteBytes || s.Waits != 1 {
		t.Fatalf("reservation accounting is wrong: %+v", s)
	}
}

func TestOneProducerAtATime(t *testing.T) {
	ctx := context.Background()
	for name, call := range map[string]func(*Capturer) error{
		"reserve": func(c *Capturer) error { return c.Reserve(ctx, 1) },
		"close":   func(c *Capturer) error { return c.Close() },
	} {
		c := newCapturer(t, 64, 1<<12)
		inside, leave := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- c.CaptureField(ctx, 1, 1, func(uint64, []byte) error {
				close(inside)
				<-leave
				return nil
			})
		}()
		<-inside
		if err := call(c); !errors.Is(err, ErrBusy) {
			t.Fatalf("%s during a capture: %v", name, err)
		}
		close(leave)
		if err := <-done; !errors.Is(err, ErrBusy) {
			t.Fatalf("%s: the running capture must see the stop: %v", name, err)
		}
	}
}

func TestClosedCapture(t *testing.T) {
	ctx := context.Background()
	c := newCapturer(t, 64, 1<<12)
	c.Close()
	if err := c.CaptureBytes(ctx, 1, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("capture after close: %v", err)
	}
	if err := c.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("close after a stop: %v", err)
	}
	if c.Err() == nil || !errors.Is(c.Err(), ErrClosed) {
		t.Fatalf("Err: %v", c.Err())
	}
	c = newCapturer(t, 64, 1<<12)
	c.Close()
	if _, err := c.Next(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("next after close: %v", err)
	}
	if c.Err() != nil {
		t.Fatal("a clean close is not an error")
	}
}

func TestAbortWakesWaiters(t *testing.T) {
	ctx := context.Background()
	stop := errors.New("native shutdown")
	c := newCapturer(t, 64, FrameOverhead+MaxNoteBytes)
	done := make(chan error, 1)
	go func() { done <- c.CaptureBytes(ctx, 1, pattern(1000, 1)) }()
	waitUntil(t, "the producer waits", func() bool { return c.Stats().ProducerWaiting })
	worker := make(chan error, 1)
	c.Next(ctx) // hold the delivered BEGIN frame
	go func() {
		_, err := c.Next(ctx)
		worker <- err
	}()
	c.Abort(stop)
	if err := <-done; !errors.Is(err, stop) {
		t.Fatalf("producer: %v", err)
	}
	if err := <-worker; !errors.Is(err, stop) {
		t.Fatalf("worker: %v", err)
	}
	if s := c.Stats(); s.Held != 0 || s.Queued != 0 || s.Delivered != 0 || s.ProducerWaiting {
		t.Fatalf("abort left budget held: %+v", s)
	}
	if err := c.Release(Frame{}); !errors.Is(err, stop) {
		t.Fatalf("release after abort: %v", err)
	}
	c = newCapturer(t, 64, 1<<12)
	c.Abort(nil)
	if c.Err() == nil {
		t.Fatal("Abort(nil) must stop the capture")
	}
}

func TestAbortDuringRead(t *testing.T) {
	stop := errors.New("stop")
	c := newCapturer(t, 64, 1<<12)
	err := c.CaptureField(context.Background(), 1, 10, func(uint64, []byte) error {
		c.Abort(stop)
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("got %v", err)
	}
}

func TestReadErrorStopsCapture(t *testing.T) {
	bad := errors.New("unstable view")
	c := newCapturer(t, 4, 1<<12)
	err := c.CaptureField(context.Background(), 1, 10, func(offset uint64, dst []byte) error {
		if offset == 4 {
			return bad
		}
		return nil
	})
	if !errors.Is(err, bad) {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Next(context.Background()); !errors.Is(err, bad) {
		t.Fatalf("worker must get the read error instead of a partial field: %v", err)
	}
}

func TestContextCancel(t *testing.T) {
	c := newCapturer(t, 64, FrameOverhead+MaxNoteBytes)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.CaptureBytes(ctx, 1, pattern(1000, 1)) }()
	waitUntil(t, "the producer waits", func() bool { return c.Stats().ProducerWaiting })
	c.Next(context.Background())
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("producer: %v", err)
	}
	if !errors.Is(c.Err(), context.Canceled) {
		t.Fatal("a canceled safe-point wait must stop the capture")
	}

	c = newCapturer(t, 64, 1<<12)
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := c.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("worker: %v", err)
	}
	if c.Err() != nil {
		t.Fatal("a canceled worker wait must not stop the capture")
	}
}

func TestReleaseOrder(t *testing.T) {
	ctx := context.Background()
	c := newCapturer(t, 64, 1<<12)
	if err := c.Release(Frame{}); !errors.Is(err, ErrRelease) {
		t.Fatalf("release before delivery: %v", err)
	}
	c = newCapturer(t, 64, 1<<12)
	c.CaptureBytes(ctx, 1, []byte{1})
	first, _ := c.Next(ctx)
	second, _ := c.Next(ctx)
	if err := c.Release(second); !errors.Is(err, ErrRelease) {
		t.Fatalf("out-of-order release: %v", err)
	}
	c = newCapturer(t, 64, 1<<12)
	c.CaptureBytes(ctx, 1, []byte{1})
	f, _ := c.Next(ctx)
	forged := f
	forged.Tag++
	if err := c.Release(forged); !errors.Is(err, ErrRelease) {
		t.Fatalf("release of a changed frame: %v", err)
	}
	_ = first
}

// TestRingWraps runs many small fields through a ring of a few slots.
func TestRingWraps(t *testing.T) {
	ctx := context.Background()
	c := newCapturer(t, 1, FrameOverhead+MaxNoteBytes)
	var want [][]byte
	go func() {
		for i := 0; i < 50; i++ {
			if err := c.CaptureBytes(ctx, uint32(i), []byte{byte(i), byte(i + 1)}); err != nil {
				c.Abort(err)
				return
			}
		}
		c.Close()
	}()
	for i := 0; i < 50; i++ {
		want = append(want, []byte{byte(i), byte(i + 1)})
	}
	frames, err := drain(t, c)
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		got := field(t, frames[i*4:i*4+4])
		if !bytes.Equal(got, want[i]) || frames[i*4].Tag != uint32(i) {
			t.Fatalf("field %d is wrong", i)
		}
	}
	if s := c.Stats(); s.Frames != 200 || s.Held != 0 || s.PeakHeld > FrameOverhead+MaxNoteBytes {
		t.Fatalf("ring accounting is wrong: %+v", s)
	}
}

// TestStoppedAndBlockedFrames covers a stopped capture and a wait that fails
// before the BEGIN_FIELD or END_FIELD frame.
func TestStoppedAndBlockedFrames(t *testing.T) {
	ctx := context.Background()
	stop := errors.New("stop")
	c := newCapturer(t, 64, 1<<12)
	c.Abort(stop)
	if err := c.Reserve(ctx, 1); !errors.Is(err, stop) {
		t.Fatalf("reserve after a stop: %v", err)
	}
	if err := c.Note(1, nil); !errors.Is(err, stop) {
		t.Fatalf("note after a stop: %v", err)
	}
	c = newCapturer(t, 64, 1<<12)
	if err := c.Reserve(ctx, 1<<12+1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("reservation above the budget: %v", err)
	}

	// A full budget makes BEGIN or END wait, and the wait is canceled.
	budget := int64(FrameOverhead + MaxNoteBytes)
	for name, length := range map[string]uint64{"end": 64, "begin": 0} {
		c = newCapturer(t, 64, budget)
		if name == "begin" {
			// An empty field and a credit reservation fill the budget before the next BEGIN.
			if err := c.CaptureBytes(ctx, 1, nil); err != nil {
				t.Fatal(err)
			}
			if err := c.Reserve(ctx, FrameOverhead); err != nil {
				t.Fatal(err)
			}
		}
		waitCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- c.CaptureBytes(waitCtx, 2, pattern(int(length), 2)) }()
		waitUntil(t, name+" waits", func() bool { return c.Stats().ProducerWaiting })
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// TestExactAccounting pins the budget numbers that the memory bound relies on.
func TestExactAccounting(t *testing.T) {
	ctx := context.Background()
	if err := validate(Config{ChunkBytes: 64, BufferBytes: MaxBufferBytes}); err != nil {
		t.Fatalf("the largest budget must be accepted: %v", err)
	}
	c := newCapturer(t, 64, 1<<12)
	if err := c.CaptureBytes(ctx, 1, pattern(10, 1)); err != nil {
		t.Fatal(err)
	}
	want := int64(3*FrameOverhead + 10)
	if s := c.Stats(); s.Held != want || s.PeakHeld != want || s.Queued != 3 || s.Delivered != 0 || s.Frames != 3 {
		t.Fatalf("after one field: %+v", s)
	}
	f, _ := c.Next(ctx)
	if s := c.Stats(); s.Queued != 2 || s.Delivered != 1 {
		t.Fatalf("after one delivery: %+v", s)
	}
	c.Release(f)
	if c.ring[0] != (Frame{}) {
		t.Fatal("a released slot must drop its payload")
	}
	if s := c.Stats(); s.Held != want-FrameOverhead || s.PeakHeld != want {
		t.Fatalf("after one release: %+v", s)
	}

	// A budget-sized reservation and a note that spends exactly its credit both fit.
	c = newCapturer(t, 64, FrameOverhead+MaxNoteBytes)
	if err := c.Reserve(ctx, FrameOverhead+MaxNoteBytes); err != nil {
		t.Fatalf("a reservation equal to the budget: %v", err)
	}
	c.EnterCallback()
	if err := c.Note(5, make([]byte, MaxNoteBytes)); err != nil {
		t.Fatalf("a maximum note that spends exactly its credit: %v", err)
	}
	if s := c.Stats(); s.Credit != 0 || s.Held != FrameOverhead+MaxNoteBytes {
		t.Fatalf("exact credit accounting: %+v", s)
	}

	// A stop returns all credit and clears every slot.
	c = newCapturer(t, 64, 1<<12)
	c.Reserve(ctx, 100)
	c.CaptureBytes(ctx, 1, []byte{1})
	c.Abort(errors.New("stop"))
	if s := c.Stats(); s.Credit != 0 || s.Held != 0 {
		t.Fatalf("after a stop: %+v", s)
	}
	for i, slot := range c.ring {
		if slot != (Frame{}) {
			t.Fatalf("slot %d kept a frame after a stop", i)
		}
	}
}

func TestReadPanicStopsCapture(t *testing.T) {
	c := newCapturer(t, 64, 1<<12)
	err := c.CaptureField(context.Background(), 1, 10, func(uint64, []byte) error { panic("native view moved") })
	if err == nil || !errors.Is(c.Err(), err) {
		t.Fatalf("got %v", err)
	}
	if err := c.Close(); err == nil {
		t.Fatal("a stopped capture cannot close cleanly")
	}
}

// TestWakeupsAndCounts covers a worker woken by Close, one wait that sees
// several releases, and the smallest reservation.
func TestWakeupsAndCounts(t *testing.T) {
	ctx := context.Background()
	c := newCapturer(t, 64, 1<<12)
	next := make(chan error, 1)
	go func() {
		_, err := c.Next(ctx)
		next <- err
	}()
	time.Sleep(10 * time.Millisecond)
	c.Close()
	if err := <-next; !errors.Is(err, io.EOF) {
		t.Fatalf("a waiting worker must see the close: %v", err)
	}

	c = newCapturer(t, 64, FrameOverhead+MaxNoteBytes)
	if err := c.Reserve(ctx, 1); err != nil {
		t.Fatalf("a one-byte reservation: %v", err)
	}
	c = newCapturer(t, 64, FrameOverhead+MaxNoteBytes)
	c.CaptureBytes(ctx, 1, nil)   // BEGIN and END hold 256 bytes
	c.Reserve(ctx, FrameOverhead) // the budget is full
	done := make(chan error, 1)
	go func() { done <- c.Reserve(ctx, 2*FrameOverhead) }()
	waitUntil(t, "the reservation waits", func() bool { return c.Stats().ProducerWaiting })
	begin, _ := c.Next(ctx)
	end, _ := c.Next(ctx)
	c.Release(begin) // not enough yet: the wait continues
	waitUntil(t, "the reservation waits again", func() bool { return c.Stats().ProducerWaiting })
	c.Release(end)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s := c.Stats(); s.Waits != 1 || s.Credit != 3*FrameOverhead {
		t.Fatalf("one waiting call must count once: %+v", s)
	}
}
