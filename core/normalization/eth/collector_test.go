package eth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sigaev-pro/bc-indexer/core/capture"
)

// memStore is the bounded byte spool of the tests: it counts open writers
// and total puts, and its Get serves the stored bytes.
type memStore struct {
	mu          sync.Mutex
	blobs       map[string][]byte
	openWriters int
	peakOpen    int
	puts        int
	failNextPut bool
}

type memStoreWriter struct {
	store *memStore
	key   string
	buf   []byte
}

func (w *memStoreWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}
func (w *memStoreWriter) Close() error {
	w.store.mu.Lock()
	defer w.store.mu.Unlock()
	w.store.blobs[w.key] = w.buf
	w.store.openWriters--
	return nil
}

func newMemStore() *memStore { return &memStore{blobs: map[string][]byte{}} }

func (s *memStore) Put(key []byte, size int64) (io.WriteCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNextPut {
		s.failNextPut = false
		return nil, errors.New("store: put failed")
	}
	s.puts++
	s.openWriters++
	if s.openWriters > s.peakOpen {
		s.peakOpen = s.openWriters
	}
	return &memStoreWriter{store: s, key: string(key), buf: make([]byte, 0, size)}, nil
}

func (s *memStore) Get(key []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.blobs[string(key)]
	if !ok {
		return nil, errors.New("store: unknown blob")
	}
	return data, nil
}

// funcSink collects imported blocks; the optional delay simulates a slow
// worker.
type funcSink struct {
	mu      sync.Mutex
	blocks  []ImportedBlock
	outside []SystemBalanceRecord
	delay   time.Duration
}

func (s *funcSink) EmitBlock(record ImportedBlock) error {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocks = append(s.blocks, record)
	return nil
}

func (s *funcSink) EmitOutsideTx(block Hash32, record *SystemBalanceRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outside = append(s.outside, *record)
	return nil
}

// emitCorpus writes the corpus blocks through the transport: block 1 with
// the transfer and nested-revert transactions, block 2 empty.
func emitCorpus(ctx context.Context, transport *Transport) error {
	if err := transport.EmitBlock(ctx, corpusBlockObservation(1), corpusOutsideObservations()); err != nil {
		return err
	}
	for _, obs := range []TxObservation{corpusTransferTx(), corpusNestedRevertTx()} {
		if err := transport.EmitTx(ctx, obs, nil); err != nil {
			return err
		}
	}
	if err := transport.EmitBlock(ctx, corpusBlockObservation(2), corpusOutsideObservations()); err != nil {
		return err
	}
	return transport.Close()
}

// corpusOutsideObservations is the block reward of the smoke corpus: 2 ETH
// to the coinbase per block (expected/eth.json, outside_transaction).
func corpusOutsideObservations() []OutsideTxObservation {
	return []OutsideTxObservation{{
		Kind:        OptionalBlockReward,
		NativeOrder: 0,
		Effects: []SystemBalanceEffect{{
			Address: corpusCoinbase,
			Before:  u256("2000261305250000000"),
			After:   u256("4000261305250000000"),
		}},
	}}
}

func newCapturer() *capture.Capturer {
	c, err := capture.New(capture.Config{ChunkBytes: 512, BufferBytes: 128 * 1024})
	if err != nil {
		panic(err)
	}
	return c
}

func TestCollectorFullImportPathKeepsNativeResults(t *testing.T) {
	capturer := newCapturer()
	transport := NewTransport(capturer)
	store := newMemStore()
	sink := &funcSink{}
	collector, err := NewCollector(capturer, store, sink, DefaultScopeDeclaration())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := emitCorpus(ctx, transport); err != nil {
		t.Fatal(err)
	}
	if err := collector.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sink.blocks) != 2 {
		t.Fatalf("imported blocks = %d, want 2", len(sink.blocks))
	}
	// The native results are unchanged: the imported records are
	// deep-equal to the direct assembly of the same observations.
	direct0, err := AssembleTx(corpusTransferTx())
	if err != nil {
		t.Fatal(err)
	}
	direct1, err := AssembleTx(corpusNestedRevertTx())
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.blocks[0].Records) != 2 {
		t.Fatalf("block 1 records = %d, want 2", len(sink.blocks[0].Records))
	}
	if !reflect.DeepEqual(*sink.blocks[0].Records[0], *direct0) {
		t.Fatal("the imported transfer record differs from the direct assembly")
	}
	if !reflect.DeepEqual(*sink.blocks[0].Records[1], *direct1) {
		t.Fatal("the imported nested-revert record differs from the direct assembly")
	}
	// The empty block stays an exact empty import.
	if len(sink.blocks[1].Records) != 0 || sink.blocks[1].Observation.TransactionCount != 0 {
		t.Fatalf("block 2 = %+v", sink.blocks[1].Observation)
	}
	// The D17 default OFF declaration kept the block rewards out.
	if len(sink.outside) != 0 {
		t.Fatalf("outside-tx records = %d, want 0 with the default OFF declaration", len(sink.outside))
	}
}

func TestCollectorLargeFieldAndBoundedMemory(t *testing.T) {
	// A receipt with a 5 MiB log payload and the D76 spool form of its
	// consensus bytes: everything above the inline threshold streams
	// through the capture frames into the store.
	big := make([]byte, 5*1024*1024)
	for i := range big {
		big[i] = byte(i % 251)
	}
	obs := corpusTransferTx()
	obs.Receipt.Logs = []Log{{Address: corpusParent, Topics: []Hash32{valueHash(0xa1)}, Data: big}}
	obs.Receipt.NetworkDetails = NetworkDetails{Eth: &EthReceiptDetails{}}
	source, err := SourceEncoding(&obs.Receipt, 0)
	if err != nil {
		t.Fatal(err)
	}

	capturer := newCapturer()
	transport := NewTransport(capturer)
	store := newMemStore()
	sink := &funcSink{}
	collector, err := NewCollector(capturer, store, sink, DefaultScopeDeclaration())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// The 5 MiB field cannot fit the 128 KiB budget: the producer streams
	// it while the collector consumes, which is exactly the bounded
	// backpressure under test.
	emitDone := make(chan error, 1)
	go func() {
		if err := transport.EmitBlock(ctx, corpusBlockObservation(1), nil); err != nil {
			emitDone <- err
			return
		}
		if err := transport.EmitTx(ctx, obs, source.Bytes); err != nil {
			emitDone <- err
			return
		}
		emitDone <- transport.Close()
	}()
	if err := collector.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-emitDone; err != nil {
		t.Fatal(err)
	}
	record := sink.blocks[0].Records[0]
	if !bytes.Equal(record.Receipt.Logs[0].Data, big) {
		t.Fatal("the large log data must arrive unchanged")
	}
	retained, ok := record.Receipt.Source.Get()
	if !ok || !bytes.Equal(retained.Bytes, source.Bytes) {
		t.Fatal("the retained D76 source bytes must equal the producer's streaming bytes")
	}
	// The collector's memory is bounded: the capture budget held the
	// in-flight frames and never exceeded it, and at most one store
	// writer was open at a time (one field in flight).
	stats := capturer.Stats()
	if stats.PeakHeld > 128*1024 {
		t.Fatalf("peak held = %d, want at most the 128 KiB budget", stats.PeakHeld)
	}
	if store.peakOpen != 1 {
		t.Fatalf("peak open store writers = %d, want 1", store.peakOpen)
	}
}

func TestCollectorSlowWorkerBoundsProducer(t *testing.T) {
	capturer := newCapturer()
	transport := NewTransport(capturer)
	store := newMemStore()
	sink := &funcSink{delay: 30 * time.Millisecond}
	collector, err := NewCollector(capturer, store, sink, DefaultScopeDeclaration())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	done := make(chan error, 2)
	go func() {
		done <- emitCorpus(ctx, transport)
	}()
	go func() { done <- collector.Run(ctx) }()
	var runErr error
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil && runErr == nil {
				runErr = err
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the import did not finish")
		}
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	stats := capturer.Stats()
	if stats.PeakHeld > 128*1024 {
		t.Fatalf("peak held = %d, want at most the budget despite the slow worker", stats.PeakHeld)
	}
	if stats.Waits == 0 {
		t.Log("note: the producer never had to wait; the sink delay was fully absorbed")
	}
	if len(sink.blocks) != 2 {
		t.Fatalf("imported blocks = %d, want 2", len(sink.blocks))
	}
}

func TestCollectorReplayRestoresSameIdentities(t *testing.T) {
	// A store failure mid-import stops the run; a replay of the same
	// observation produces the identical records (the crash-recovery
	// property of test/native_faults/eth: logical identities are
	// functions of the observation, not of the counters of a dead run).
	run := func(fail bool) ([]*TransactionRecord, error) {
		capturer := newCapturer()
		transport := NewTransport(capturer)
		store := newMemStore()
		store.failNextPut = fail
		// A large header extra data forces a blob put, so the failing
		// store actually stops the producer.
		block := corpusBlockObservation(1)
		block.Header.ExtraData = make([]byte, 1000)
		sink := &funcSink{}
		collector, err := NewCollector(capturer, store, sink, DefaultScopeDeclaration())
		if err != nil {
			return nil, err
		}
		ctx := context.Background()
		if err := transport.EmitBlock(ctx, block, corpusOutsideObservations()); err != nil {
			return nil, err
		}
		for _, txObs := range []TxObservation{corpusTransferTx(), corpusNestedRevertTx()} {
			if err := transport.EmitTx(ctx, txObs, nil); err != nil {
				return nil, err
			}
		}
		if err := transport.EmitBlock(ctx, corpusBlockObservation(2), corpusOutsideObservations()); err != nil {
			return nil, err
		}
		if err := transport.Close(); err != nil {
			return nil, err
		}
		err = collector.Run(ctx)
		if err != nil {
			return nil, err
		}
		var records []*TransactionRecord
		for _, b := range sink.blocks {
			records = append(records, b.Records...)
		}
		return records, nil
	}
	if _, err := run(true); err == nil {
		t.Fatal("a store failure must stop the import")
	}
	replayed, err := run(false)
	if err != nil {
		t.Fatal(err)
	}
	again, err := run(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 2 {
		t.Fatalf("replayed records = %d, want the 2 corpus transactions", len(replayed))
	}
	if !reflect.DeepEqual(replayed, again) {
		t.Fatal("two replays of the same observation must produce identical records")
	}
	direct0, _ := AssembleTx(corpusTransferTx())
	direct1, _ := AssembleTx(corpusNestedRevertTx())
	if !reflect.DeepEqual(replayed[0], direct0) || !reflect.DeepEqual(replayed[1], direct1) {
		t.Fatal("the replayed records must match the direct assembly: same ids, orders and scopes")
	}
}

// corruptingStore serves every stored blob with its first byte flipped:
// the collector's content-key check must reject the tampered read.
type corruptingStore struct {
	inner *memStore
}

func (s *corruptingStore) Put(key []byte, size int64) (io.WriteCloser, error) {
	return s.inner.Put(key, size)
}

func (s *corruptingStore) Get(key []byte) ([]byte, error) {
	data, err := s.inner.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) > 0 {
		tampered := append([]byte{}, data...)
		tampered[0] ^= 0xff
		return tampered, nil
	}
	return data, nil
}

func TestCollectorRejectsCorruptedBlob(t *testing.T) {
	capturer := newCapturer()
	transport := NewTransport(capturer)
	mem := newMemStore()
	store := &corruptingStore{inner: mem}
	sink := &funcSink{}
	collector, err := NewCollector(capturer, store, sink, DefaultScopeDeclaration())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := transport.EmitBlock(ctx, corpusBlockObservation(1), nil); err != nil {
		t.Fatal(err)
	}
	// A raw encoding above the inline threshold becomes a stored blob.
	obs := corpusTransferTx()
	obs.RawEncodings[0].Bytes = make([]byte, 1000)
	if err := transport.EmitTx(ctx, obs, nil); err != nil {
		t.Fatal(err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if err := collector.Run(ctx); err == nil {
		t.Fatal("a blob that does not match its content key must fail the import")
	}
}

func TestCollectorRejectsDigestMismatch(t *testing.T) {
	// Hand-craft a frame stream whose end digest disagrees.
	capturer := newCapturer()
	if err := capturer.CaptureBytes(context.Background(), TagBlockEvent, []byte(`{"hash":"`+hashHex(patternHash(1))+`"}`)); err != nil {
		t.Fatal(err)
	}
	if err := capturer.Close(); err != nil {
		t.Fatal(err)
	}
	// Swap the digest of the stored end frame by replaying through a
	// wrapper source that flips the digest.
	real := &directSource{capturer: capturer}
	source := &digestFlippingSource{inner: real}
	sink := &funcSink{}
	collector, err := NewCollector(source, newMemStore(), sink, DefaultScopeDeclaration())
	if err != nil {
		t.Fatal(err)
	}
	if err := collector.Run(context.Background()); err == nil {
		t.Fatal("a mismatching field digest must fail the import")
	}
}

// directSource adapts a Capturer that already holds frames.
type directSource struct {
	capturer *capture.Capturer
}

func (d *directSource) Next(ctx context.Context) (capture.Frame, error) {
	return d.capturer.Next(ctx)
}
func (d *directSource) Release(f capture.Frame) error { return d.capturer.Release(f) }

// digestFlippingSource corrupts the digest of every EndField.
type digestFlippingSource struct {
	inner FrameSource
}

func (d *digestFlippingSource) Next(ctx context.Context) (capture.Frame, error) {
	f, err := d.inner.Next(ctx)
	if err == nil && f.Kind == capture.EndField {
		f.Digest[0] ^= 0xff
	}
	return f, err
}
func (d *digestFlippingSource) Release(f capture.Frame) error { return d.inner.Release(f) }
