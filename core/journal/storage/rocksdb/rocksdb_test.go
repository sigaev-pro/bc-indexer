package rocksdb

// The scenarios in this package's tests are the ones of
// ROCKSDB_DELIVERY_PROTOCOL (REC-T01-17) and LIVE_CONFIG_SPEC ("State and
// startup") that apply at the storage layer: what survives a restart, that a
// batch is all or nothing, that an applied record is never rolled back, the
// exclusive open, and the refusal of a directory that is not a compatible
// store. The delivery transitions themselves belong to
// core/journal/producer and gateway/journal.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage/storagetest"
)

// openTest opens the store in dir with every write synced, and closes it
// when the test ends.
func openTest(t *testing.T, dir string) storage.Storage {
	t.Helper()
	return openWith(t, dir, Options{})
}

// openFast opens the store without a sync per write. Tests use it where the
// sync is not what is under test; a caller outside this package cannot.
func openFast(t *testing.T, dir string) storage.Storage {
	t.Helper()
	return openWith(t, dir, Options{sync: syncNever})
}

func openWith(t *testing.T, dir string, opts Options) storage.Storage {
	t.Helper()
	st, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	t.Cleanup(func() { Close(st) })
	return st
}

type pair struct{ key, value string }

// dump returns every pair of the store in ascending key order.
func dump(t *testing.T, st storage.Storage) []pair {
	t.Helper()
	var out []pair
	err := st.Iterate(nil, nil, func(k, v []byte) error {
		out = append(out, pair{string(k), string(v)})
		return nil
	})
	if err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return out
}

func mustGet(t *testing.T, st storage.Storage, key string) string {
	t.Helper()
	v, err := st.Get([]byte(key))
	if err != nil {
		t.Fatalf("get %q: %v", key, err)
	}
	return string(v)
}

func wantErr(t *testing.T, err error, target error, what string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: got %v, want an error that wraps %v", what, err, target)
	}
}

func wantAbsent(t *testing.T, st storage.Storage, key string) {
	t.Helper()
	if _, err := st.Get([]byte(key)); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("key %q is present or unreadable: %v", key, err)
	}
}

// snapshotDir returns the content of every file under dir, by path relative
// to dir. A symbolic link is recorded as its target and not followed. A true
// return from skip leaves a file out.
func snapshotDir(t *testing.T, dir string, skip func(rel string) bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if skip != nil && skip(rel) {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[rel] = "link to " + target
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return out
}

// notInfoLog leaves out the engine's informational log, which a failed open
// of our own store may append to. It holds no data.
func notInfoLog(rel string) bool {
	return rel == "LOG" || strings.HasPrefix(rel, "LOG.old.")
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// filesNamed returns the regular files directly in dir whose name has the
// given prefix and suffix. An empty one matches every name.
func filesNamed(t *testing.T, dir, prefix, suffix string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		if strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), suffix) {
			out = append(out, entry.Name())
		}
	}
	return out
}

// onlyFile returns dir's single file with the given prefix and suffix.
func onlyFile(t *testing.T, dir, prefix, suffix string) string {
	t.Helper()
	names := filesNamed(t, dir, prefix, suffix)
	if len(names) != 1 {
		t.Fatalf("expected exactly one file %s*%s in %s, found %v", prefix, suffix, dir, names)
	}
	return filepath.Join(dir, names[0])
}

func TestConformance(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) storage.Storage {
		return openTest(t, filepath.Join(t.TempDir(), "wal"))
	})
}

// The zero value of Options is the durable setting. Nothing outside this
// package can ask for less.
func TestOptionsSyncEveryWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	st := openTest(t, dir)

	s := st.(*store)
	if !s.wo.IsSync() {
		t.Fatal("Options{} must sync writes")
	}
	if s.wo.IsDisableWAL() {
		t.Fatal("Options{} must keep the write-ahead log on")
	}

	// A write goes to the write-ahead log: the log file exists and holds the
	// record before any flush of the memtable is asked for.
	value := bytes.Repeat([]byte("wal-enabled "), 64)
	if err := st.Put([]byte("wal/probe"), value); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(onlyFile(t, dir, "", ".log")); err != nil || info.Size() == 0 {
		t.Fatalf("no non-empty write-ahead log in %s after a synced Put: %v", dir, err)
	}

	// The unsynced mode keeps the log on and skips only the sync.
	fast := openFast(t, filepath.Join(t.TempDir(), "wal")).(*store)
	if fast.wo.IsSync() {
		t.Fatal("the unsynced mode must not sync writes")
	}
	if fast.wo.IsDisableWAL() {
		t.Fatal("the unsynced mode must keep the write-ahead log on")
	}
}

func TestOpenRejectsBadInput(t *testing.T) {
	if _, err := Open("", Options{}); err == nil {
		t.Fatal("an empty directory path must be refused")
	}
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(notDir, Options{})
	wantErr(t, err, ErrForeign, "a path that is a file")
}

func TestOpenCreatesDirectoryAndReopens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing", "nested", "wal")
	st := openTest(t, dir)

	wantMarker := fmt.Sprintf("%s\nformat_version=%d\nengine=%s\n", formatMagic, formatVersion, engineName)
	marker, err := os.ReadFile(filepath.Join(dir, formatFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(marker) != wantMarker {
		t.Fatalf("marker = %q, want %q", marker, wantMarker)
	}
	if _, err := os.Stat(filepath.Join(dir, currentFile)); err != nil {
		t.Fatalf("the engine did not create the database: %v", err)
	}

	b := storage.NewBatch()
	b.Put([]byte("batch/1/state"), []byte{3})
	b.Delete([]byte("gone"))
	if err := st.Apply(b); err != nil {
		t.Fatal(err)
	}
	if err := st.Put([]byte("meta/counters"), []byte("counters")); err != nil {
		t.Fatal(err)
	}
	before := dump(t, st)
	if err := Close(st); err != nil {
		t.Fatal(err)
	}

	reopened := openTest(t, dir)
	if after := dump(t, reopened); !reflect.DeepEqual(after, before) {
		t.Fatalf("state changed across close and reopen:\n before %v\n after  %v", before, after)
	}
	for _, leftover := range []string{formatTempFile} {
		if _, err := os.Stat(filepath.Join(dir, leftover)); !os.IsNotExist(err) {
			t.Fatalf("%s must not remain: %v", leftover, err)
		}
	}
}

// REC-T02 at the storage layer: after a restart the stored bytes are exactly
// what was written, including empty values, high bytes, and deletions.
func TestReopenRestoresWritesBatchesAndDeletes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	st := openTest(t, dir)

	large := bytes.Repeat([]byte{0x00, 0xFF, 0x80, 0x7F, 'x'}, 16384) // 80 KiB
	chunkKey := func(i int) string {
		return fmt.Sprintf("batch/7/attempt/1/data/%08d", i*len(large))
	}
	const chunks = 12
	for i := 0; i < chunks; i++ {
		if err := st.Put([]byte(chunkKey(i)), large); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Put([]byte("empty/value"), nil); err != nil {
		t.Fatal(err)
	}
	if err := st.Put([]byte{0xFF, 0x00, 0xFE}, []byte{0x01, 0x00, 0x02}); err != nil {
		t.Fatal(err)
	}
	if err := st.Put([]byte("doomed"), []byte("first")); err != nil {
		t.Fatal(err)
	}
	b := storage.NewBatch()
	b.Put([]byte("batch/7/state"), []byte("READY"))
	b.Put([]byte("batch/7/ready"), []byte("manifest-digest"))
	b.Delete([]byte("doomed"))
	if err := st.Apply(b); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete([]byte("batch/7/ready")); err != nil {
		t.Fatal(err)
	}
	wantAbsent(t, st, "doomed")
	before := dump(t, st)
	// The chunks, the empty value, the high-byte key and the batch state.
	if want := chunks + 3; len(before) != want {
		t.Fatalf("store holds %d pairs, want %d", len(before), want)
	}
	if err := Close(st); err != nil {
		t.Fatal(err)
	}

	reopened := openTest(t, dir)
	if after := dump(t, reopened); !reflect.DeepEqual(after, before) {
		t.Fatal("state changed across close and reopen")
	}
	for _, key := range []string{"doomed", "batch/7/ready"} {
		wantAbsent(t, reopened, key)
	}
	got, err := reopened.Get([]byte(chunkKey(5)))
	if err != nil || !bytes.Equal(got, large) {
		t.Fatalf("large value changed across reopen: %d bytes, err %v", len(got), err)
	}
	// A range walk after reopen is ordered and bounded like before.
	var keys []string
	if err := reopened.Iterate([]byte("batch/7/attempt/"), []byte("batch/7/ready"), func(k, v []byte) error {
		keys = append(keys, string(k))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(keys) != chunks || keys[0] != chunkKey(0) || keys[chunks-1] != chunkKey(chunks-1) {
		t.Fatalf("range walk after reopen got %d keys, first %q, last %q", len(keys), keys[0], keys[len(keys)-1])
	}
}

// A batch that Apply reported is whole after a restart, and a batch that was
// refused left nothing.
func TestBatchIsWholeAfterRestart(t *testing.T) {
	const batches, keysPerBatch = 300, 3
	dir := filepath.Join(t.TempDir(), "wal")
	st := openFast(t, dir)
	key := func(i, j int) string { return fmt.Sprintf("batch/%06d/chunk/%d", i, j) }
	value := func(i, j int) string { return fmt.Sprintf("value-%d-%d", i, j) }

	for i := 0; i < batches; i++ {
		b := storage.NewBatch()
		for j := 0; j < keysPerBatch; j++ {
			b.Put([]byte(key(i, j)), []byte(value(i, j)))
		}
		// Every batch also moves one shared key, like a closing commit that
		// advances a checkpoint next to its payload.
		b.Put([]byte("meta/checkpoint"), []byte(strconv.Itoa(i)))
		if err := st.Apply(b); err != nil {
			t.Fatal(err)
		}
	}
	tooBig := storage.NewBatch()
	tooBig.Put([]byte("too/big"), make([]byte, storage.MaxBatchBytes))
	wantErr(t, st.Apply(tooBig), storage.ErrBatchTooLarge, "oversized batch")
	if err := Close(st); err != nil {
		t.Fatal(err)
	}

	reopened := openTest(t, dir)
	for i := 0; i < batches; i++ {
		for j := 0; j < keysPerBatch; j++ {
			if got := mustGet(t, reopened, key(i, j)); got != value(i, j) {
				t.Fatalf("batch %d key %d = %q after reopen, want %q", i, j, got, value(i, j))
			}
		}
	}
	if got := mustGet(t, reopened, "meta/checkpoint"); got != strconv.Itoa(batches-1) {
		t.Fatalf("checkpoint = %q after reopen, want %d", got, batches-1)
	}
	wantAbsent(t, reopened, "too/big")
}

func TestClosedStoreRefusesOperations(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	st := openTest(t, dir)
	if err := st.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := Close(st); err != nil {
		t.Fatal(err)
	}
	if err := Close(st); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	_, err := st.Get([]byte("k"))
	wantErr(t, err, ErrClosed, "Get after Close")
	wantErr(t, st.Put([]byte("k"), []byte("v")), ErrClosed, "Put after Close")
	wantErr(t, st.Delete([]byte("k")), ErrClosed, "Delete after Close")
	b := storage.NewBatch()
	b.Put([]byte("k"), []byte("v"))
	wantErr(t, st.Apply(b), ErrClosed, "Apply after Close")
	wantErr(t, st.Iterate(nil, nil, func(k, v []byte) error { return nil }), ErrClosed, "Iterate after Close")

	// Close released the lock: the store opens again with its state.
	if got := mustGet(t, openTest(t, dir), "k"); got != "v" {
		t.Fatalf("state lost after Close: %q", got)
	}
}

// One store used from several goroutines at once, under the race detector.
func TestConcurrentReadersAndWriters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	st := openFast(t, dir)
	const writers, readers, ops = 4, 4, 150
	seed := storage.NewBatch()
	for i := 0; i < 64; i++ {
		seed.Put([]byte(fmt.Sprintf("seed/%03d", i)), []byte("seed"))
	}
	if err := st.Apply(seed); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, (writers+readers)*ops)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				key := []byte(fmt.Sprintf("writer/%d/%05d", w, i))
				var err error
				switch i % 4 {
				case 0:
					err = st.Put(key, []byte("put"))
				case 1:
					b := storage.NewBatch()
					b.Put(key, []byte("batch"))
					b.Put([]byte(fmt.Sprintf("writer/%d/shared", w)), []byte(strconv.Itoa(i)))
					err = st.Apply(b)
				case 2:
					err = st.Delete(key)
				default:
					_, err = st.Get(key)
					if errors.Is(err, storage.ErrNotFound) {
						err = nil
					}
				}
				if err != nil {
					errs <- fmt.Errorf("writer %d op %d: %w", w, i, err)
					return
				}
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				var seen int
				err := st.Iterate([]byte("seed/"), []byte("seed0"), func(k, v []byte) error {
					seen++
					if !bytes.HasPrefix(k, []byte("seed/")) {
						return fmt.Errorf("key %q outside the range", k)
					}
					return nil
				})
				// The range holds 64 stable keys whatever the writers do.
				if err == nil && seen != 64 {
					err = fmt.Errorf("walk %d of reader %d saw %d keys, want 64", i, r, seen)
				}
				if err != nil {
					errs <- fmt.Errorf("reader %d: %w", r, err)
					return
				}
			}
		}(r)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	// Every write that returned is in the store after a restart. The last
	// batch of a writer is op 149, which is an Apply, so the shared key holds
	// that op's number.
	if err := Close(st); err != nil {
		t.Fatal(err)
	}
	reopened := openTest(t, dir)
	lastBatch := ops - 1
	for lastBatch%4 != 1 {
		lastBatch--
	}
	for w := 0; w < writers; w++ {
		if got := mustGet(t, reopened, fmt.Sprintf("writer/%d/shared", w)); got != strconv.Itoa(lastBatch) {
			t.Fatalf("writer %d shared key = %q after reopen, want %d", w, got, lastBatch)
		}
	}
}

// Close must not deadlock against an iteration whose visit function writes,
// and that write must fail fast instead of waiting.
func TestCloseWaitsForAnIterationThatWrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	st := openFast(t, dir)
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if err := st.Put([]byte(k), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}

	iterateDone := make(chan error, 1)
	go func() {
		iterateDone <- st.Iterate(nil, nil, func(k, v []byte) error {
			time.Sleep(3 * time.Millisecond)
			return st.Put([]byte("written-from-visit"), []byte(string(k)))
		})
	}()
	time.Sleep(4 * time.Millisecond) // the walk is inside its visit function

	closed := make(chan error, 1)
	go func() { closed <- Close(st) }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Close did not return while an iteration was writing")
	}
	select {
	case err := <-iterateDone:
		// The walk either finished before Close or got ErrClosed on its next
		// write. Anything else is a failure.
		if err != nil && !errors.Is(err, ErrClosed) {
			t.Fatalf("Iterate returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Iterate did not return after Close")
	}
}

// Close called twice at once, while an iteration is still running, must
// return from both calls.
func TestCloseCalledTwiceConcurrentlyReturns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	st := openFast(t, dir)
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if err := st.Put([]byte(k), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}

	iterateDone := make(chan error, 1)
	go func() {
		iterateDone <- st.Iterate(nil, nil, func(k, v []byte) error {
			time.Sleep(3 * time.Millisecond)
			return nil
		})
	}()
	time.Sleep(4 * time.Millisecond) // the walk is inside its visit function

	closed := make(chan error, 2)
	go func() { closed <- Close(st) }()
	go func() { closed <- Close(st) }()
	for call := 1; call <= 2; call++ {
		select {
		case err := <-closed:
			if err != nil {
				t.Fatalf("Close call %d: %v", call, err)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("Close call %d did not return", call)
		}
	}
	select {
	case err := <-iterateDone:
		if err != nil && !errors.Is(err, ErrClosed) {
			t.Fatalf("Iterate returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Iterate did not return after Close")
	}
}

// A symbolic link to the store directory is the store directory: a volume
// reached through a link opens like any other.
func TestOpenAcceptsSymlinkedDirectory(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "volume", "wal")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link-to-wal")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	// Created through the link, opened again through the real path.
	st := openTest(t, link)
	if err := st.Put([]byte("k"), []byte("through the link")); err != nil {
		t.Fatal(err)
	}
	if err := Close(st); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, openTest(t, real), "k"); got != "through the link" {
		t.Fatalf("got %q", got)
	}

	// A link to a file is still not a directory.
	file := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileLink := filepath.Join(base, "link-to-file")
	if err := os.Symlink(file, fileLink); err != nil {
		t.Fatal(err)
	}
	_, err := Open(fileLink, Options{})
	wantErr(t, err, ErrForeign, "open through a link to a file")
}

// readFile reads a whole file or fails the test.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// writeFile places a file with the usual permissions or fails the test.
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
