package rocksdb

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linxGnu/grocksdb"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage/storagetest"
)

// The scenarios below are the ones of ROCKSDB_DELIVERY_PROTOCOL (REC-T01-17)
// and LIVE_CONFIG_SPEC ("State and startup") that apply at the storage layer:
// what survives a restart, that a batch is all or nothing, the exclusive
// open, and the refusal of a directory that is not a compatible store. The
// delivery transitions themselves belong to core/journal/producer and
// gateway/journal.

func openTest(t *testing.T, dir string, opts Options) storage.Storage {
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

// snapshotDir returns the content of every file under dir, by path relative
// to dir. A true return from skip leaves a file out.
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

func TestConformance(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) storage.Storage {
		return openTest(t, filepath.Join(t.TempDir(), "wal"), Options{})
	})
}

func TestZeroOptionsAreDurable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	st := openTest(t, dir, Options{})

	// The zero value syncs every write and keeps the write-ahead log on.
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
	logs := 0
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(e.Name(), ".log") && info.Size() > 0 {
			logs++
		}
	}
	if logs == 0 {
		t.Fatalf("no non-empty write-ahead log in %s after a synced Put", dir)
	}

	// SyncNever keeps the log on and skips only the sync.
	fast := openTest(t, filepath.Join(t.TempDir(), "wal"), Options{Sync: SyncNever}).(*store)
	if fast.wo.IsSync() {
		t.Fatal("SyncNever must not sync writes")
	}
	if fast.wo.IsDisableWAL() {
		t.Fatal("SyncNever must keep the write-ahead log on")
	}
}

func TestOpenRejectsBadInput(t *testing.T) {
	if _, err := Open("", Options{}); err == nil {
		t.Fatal("an empty directory path must be refused")
	}
	if _, err := Open(filepath.Join(t.TempDir(), "wal"), Options{Sync: SyncMode(9)}); err == nil {
		t.Fatal("an unknown sync mode must be refused")
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
	st := openTest(t, dir, Options{})

	wantMarker := fmt.Sprintf("%s\nformat_version=%d\nengine=%s\nengine_version=%s\n",
		formatMagic, formatVersion, engineName, engineVersion)
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

	reopened := openTest(t, dir, Options{})
	if after := dump(t, reopened); !reflect.DeepEqual(after, before) {
		t.Fatalf("state changed across close and reopen:\n before %v\n after  %v", before, after)
	}
	if _, err := os.Stat(filepath.Join(dir, formatTempFile)); !os.IsNotExist(err) {
		t.Fatalf("the temporary marker must not remain: %v", err)
	}
}

// REC-T02 at the storage layer: after a restart the stored bytes are exactly
// what was written, including empty values, high bytes, and deletions.
func TestReopenRestoresWritesBatchesAndDeletes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	st := openTest(t, dir, Options{})

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
	if _, err := st.Get([]byte("doomed")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("deleted key still visible: %v", err)
	}
	before := dump(t, st)
	// The chunks, the empty value, the high-byte key and the batch state.
	if want := chunks + 3; len(before) != want {
		t.Fatalf("store holds %d pairs, want %d", len(before), want)
	}
	if err := Close(st); err != nil {
		t.Fatal(err)
	}

	reopened := openTest(t, dir, Options{})
	if after := dump(t, reopened); !reflect.DeepEqual(after, before) {
		t.Fatal("state changed across close and reopen")
	}
	for _, key := range []string{"doomed", "batch/7/ready"} {
		if _, err := reopened.Get([]byte(key)); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("key %q is back after reopen: %v", key, err)
		}
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
// refused left nothing behind.
func TestBatchIsWholeAfterRestart(t *testing.T) {
	const batches, keysPerBatch = 300, 3
	dir := filepath.Join(t.TempDir(), "wal")
	st := openTest(t, dir, Options{})
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

	reopened := openTest(t, dir, Options{})
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
	if _, err := reopened.Get([]byte("too/big")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("the refused batch left a key behind: %v", err)
	}
}

// LIVE_CONFIG_SPEC, "State and startup" step 2: the owner state is opened
// exclusively and a second writer gets an error.
func TestExclusiveOpenRefusesSecondWriter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	first := openTest(t, dir, Options{})
	if err := first.Put([]byte("owner"), []byte("first")); err != nil {
		t.Fatal(err)
	}

	second, err := Open(dir, Options{})
	if second != nil {
		Close(second)
		t.Fatal("the second open returned a store")
	}
	wantErr(t, err, ErrLocked, "second open of a live store")

	// The refused open left the owner working and the state intact.
	if err := first.Put([]byte("after"), []byte("refusal")); err != nil {
		t.Fatalf("owner write after a refused second open: %v", err)
	}
	if got := mustGet(t, first, "owner"); got != "first" {
		t.Fatalf("owner state changed: %q", got)
	}

	// Close releases the directory: the store opens again and the state is
	// what the owner left. (A second process is covered by
	// TestSurvivesProcessKill, which opens the store while the helper holds
	// it.)
	if err := Close(first); err != nil {
		t.Fatal(err)
	}
	next := openTest(t, dir, Options{})
	if got := mustGet(t, next, "after"); got != "refusal" {
		t.Fatalf("state after the owner closed: %q", got)
	}
}

func TestForeignDirectoryIsRefused(t *testing.T) {
	plainDatabase := func(t *testing.T, dir string) {
		t.Helper()
		// An engine database that this adapter did not create: it has no
		// marker, so it belongs to somebody else.
		opts := grocksdb.NewDefaultOptions()
		defer opts.Destroy()
		opts.SetCreateIfMissing(true)
		raw, err := grocksdb.OpenDb(opts, dir)
		if err != nil {
			t.Fatal(err)
		}
		wo := grocksdb.NewDefaultWriteOptions()
		defer wo.Destroy()
		if err := raw.Put(wo, []byte("foreign"), []byte("payload")); err != nil {
			t.Fatal(err)
		}
		raw.Close()
	}
	cases := map[string]func(t *testing.T, dir string){
		"unrelated file": func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "nodekey"), []byte("secret"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"native data directory": func(t *testing.T, dir string) {
			for _, name := range []string{"chaindata/000001.sst", "LOCK", "nodekey", "geth/nodekey"} {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte("node data"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		},
		"plain engine database": plainDatabase,
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "occupied")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			setup(t, dir)
			before := snapshotDir(t, dir, nil)

			st, err := Open(dir, Options{})
			if st != nil {
				Close(st)
				t.Fatal("Open returned a store for a directory that is not ours")
			}
			wantErr(t, err, ErrForeign, "open of a directory without our marker")

			// Refused means untouched: no marker, no database, same bytes.
			if after := snapshotDir(t, dir, nil); !reflect.DeepEqual(after, before) {
				t.Fatalf("a refused directory changed:\n before %v\n after  %v", keys(before), keys(after))
			}
		})
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// LIVE_CONFIG_SPEC, "State and startup" step 3 and DELIVERY_STATE_MACHINE
// ("Errors, ENOSPC, compatibility"): an incompatible store is refused before
// any write and is never reinitialized, even when it holds no payload.
func TestIncompatibleStoreIsRefused(t *testing.T) {
	good := fmt.Sprintf("%s\nformat_version=%d\nengine=%s\nengine_version=%s\n",
		formatMagic, formatVersion, engineName, engineVersion)
	cases := map[string]string{
		"newer store format":   strings.Replace(good, "format_version=1", "format_version=2", 1),
		"no format version":    formatMagic + "\nengine=rocksdb\n",
		"another engine":       strings.Replace(good, "engine=rocksdb", "engine=other", 1),
		"another store":        "some other program\nformat_version=1\nengine=rocksdb\n",
		"garbage":              "\x00\x01\x02not a marker at all\n",
		"truncated first line": "bc-indexer journal stor",
	}
	for name, marker := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "wal")
			st := openTest(t, dir, Options{})
			if err := st.Put([]byte("batch/1/data"), []byte("payload that must survive")); err != nil {
				t.Fatal(err)
			}
			if err := Close(st); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, formatFile), []byte(marker), 0o644); err != nil {
				t.Fatal(err)
			}
			before := snapshotDir(t, dir, nil)

			refused, err := Open(dir, Options{})
			if refused != nil {
				Close(refused)
				t.Fatal("Open returned a store for an incompatible marker")
			}
			wantErr(t, err, ErrIncompatible, "open of an incompatible store")

			// The engine was never started on it, so not one byte changed:
			// no new empty database, no rewritten marker.
			if after := snapshotDir(t, dir, nil); !reflect.DeepEqual(after, before) {
				t.Fatalf("a refused store changed:\n before %v\n after  %v", keys(before), keys(after))
			}
		})
	}
}

// A database the engine cannot open is an error, not a fresh empty store.
// The data files are left as they were. The error is ErrOpen, not
// ErrIncompatible: the store is ours and of this format, so a caller maps it
// to a disk error and not to an incompatible state.
func TestDamagedDatabaseIsRefusedNotReinitialized(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	st := openTest(t, dir, Options{})
	for i := 0; i < 20; i++ {
		if err := st.Put([]byte(fmt.Sprintf("batch/%d", i)), []byte("pending payload")); err != nil {
			t.Fatal(err)
		}
	}
	if err := Close(st); err != nil {
		t.Fatal(err)
	}

	// Point the database at a manifest that does not exist.
	current := filepath.Join(dir, currentFile)
	if err := os.WriteFile(current, []byte("MANIFEST-999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotDir(t, dir, notInfoLog)

	refused, err := Open(dir, Options{})
	if refused != nil {
		Close(refused)
		t.Fatal("Open returned a store for a damaged database")
	}
	wantErr(t, err, ErrOpen, "open of a damaged database")
	if errors.Is(err, ErrIncompatible) || errors.Is(err, ErrForeign) || errors.Is(err, ErrLocked) {
		t.Fatalf("a damaged database of this format must not report a foreign, incompatible or locked store: %v", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("the error does not name the directory: %v", err)
	}

	// Nothing was repaired or replaced: every file outside the engine's
	// informational log holds the same bytes.
	if after := snapshotDir(t, dir, notInfoLog); !reflect.DeepEqual(after, before) {
		t.Fatalf("a refused damaged database changed:\n before %v\n after  %v", keys(before), keys(after))
	}
}

// The first creation writes the marker and then lets the engine create the
// database. A crash in between leaves the marker and a few engine files
// without a database. That directory has never held a record, so the next
// open finishes the creation.
func TestInterruptedFirstCreation(t *testing.T) {
	markerOnly := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "archive"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeFormat(dir); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"LOCK", "IDENTITY", "LOG", "LOG.old.1730000000000000"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("leftover"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("resumes", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "wal")
		markerOnly(t, dir)
		st := openTest(t, dir, Options{})
		if _, err := os.Stat(filepath.Join(dir, currentFile)); err != nil {
			t.Fatalf("the database was not created: %v", err)
		}
		if err := st.Put([]byte("k"), []byte("v")); err != nil {
			t.Fatal(err)
		}
		if got := mustGet(t, st, "k"); got != "v" {
			t.Fatalf("got %q", got)
		}
	})

	// Database files without the engine's marker of a completed database are
	// ambiguous: an interrupted creation or a damaged store. Neither is
	// written over.
	for _, name := range []string{"MANIFEST-000005", "000003.log", "000004.sst"} {
		t.Run("refuses "+name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "wal")
			markerOnly(t, dir)
			if err := os.WriteFile(filepath.Join(dir, name), []byte("unknown"), 0o644); err != nil {
				t.Fatal(err)
			}
			before := snapshotDir(t, dir, nil)
			st, err := Open(dir, Options{})
			if st != nil {
				Close(st)
				t.Fatal("Open returned a store")
			}
			wantErr(t, err, ErrIncompatible, "open over a database file without "+currentFile)
			if after := snapshotDir(t, dir, nil); !reflect.DeepEqual(after, before) {
				t.Fatal("the refused directory changed")
			}
		})
	}
}

func TestClosedStoreRefusesOperations(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	st := openTest(t, dir, Options{})
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

	// The directory lock is released: the store opens again with its state.
	if got := mustGet(t, openTest(t, dir, Options{}), "k"); got != "v" {
		t.Fatalf("state lost after Close: %q", got)
	}
}

// One store used from several goroutines at once, under the race detector.
func TestConcurrentReadersAndWriters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	st := openTest(t, dir, Options{})
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
	reopened := openTest(t, dir, Options{})
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
	st := openTest(t, dir, Options{})
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
	st := openTest(t, dir, Options{})
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
	st := openTest(t, link, Options{})
	if err := st.Put([]byte("k"), []byte("through the link")); err != nil {
		t.Fatal(err)
	}
	if err := Close(st); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, openTest(t, real, Options{}), "k"); got != "through the link" {
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

// digestPairs returns one line per pair of the store: the key and the SHA-256
// of the value. It compares a large store without holding two copies of its
// values.
func digestPairs(t *testing.T, st storage.Storage) []string {
	t.Helper()
	var out []string
	err := st.Iterate(nil, nil, func(k, v []byte) error {
		sum := sha256.Sum256(v)
		out = append(out, string(k)+" "+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A store big enough to hold table files reopens with the same state. The
// other restart tests are small enough that the engine recovers them from the
// write-ahead log alone; a real store also keeps data in table files.
func TestReopenAfterFlushToTableFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	// SyncNever keeps the test quick. It is about where the data lands, not
	// about the sync.
	st := openTest(t, dir, Options{Sync: SyncNever})

	first := storage.NewBatch()
	first.Put([]byte("flush/empty-value"), nil)
	first.Put([]byte{}, []byte("empty-key"))
	first.Put([]byte{0xFF, 0x00}, []byte{0x00, 0xFF})
	first.Put([]byte("flush/first"), []byte("written before the bulk data"))
	if err := st.Apply(first); err != nil {
		t.Fatal(err)
	}

	// The engine's default write buffer is 64 MiB. This writes past it, so
	// the engine flushes the memtable to a table file.
	chunk := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64 KiB
	const chunks = 1200                                     // 75 MiB
	for i := 0; i < chunks; i++ {
		if err := st.Put([]byte(fmt.Sprintf("bulk/%05d", i)), chunk); err != nil {
			t.Fatal(err)
		}
	}
	last := storage.NewBatch()
	last.Put([]byte("flush/last"), []byte("written after the bulk data"))
	last.Delete([]byte("bulk/00000"))
	if err := st.Apply(last); err != nil {
		t.Fatal(err)
	}
	before := digestPairs(t, st)
	if want := chunks + 4; len(before) != want {
		t.Fatalf("store holds %d pairs, want %d", len(before), want)
	}
	if err := Close(st); err != nil {
		t.Fatal(err)
	}

	// The flush happened: the directory holds table files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	tables := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sst") {
			tables++
		}
	}
	if tables == 0 {
		t.Fatalf("no table file in %s: the bulk data did not reach the flush", dir)
	}

	reopened := openTest(t, dir, Options{})
	after := digestPairs(t, reopened)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("state changed across close and reopen: %d pairs before, %d after", len(before), len(after))
	}
	// The corner pairs come back from wherever the engine keeps them.
	if got, err := reopened.Get([]byte("flush/empty-value")); err != nil || len(got) != 0 {
		t.Fatalf("empty value after reopen: %q %v", got, err)
	}
	if got := mustGet(t, reopened, ""); got != "empty-key" {
		t.Fatalf("empty key after reopen: %q", got)
	}
	if _, err := reopened.Get([]byte("bulk/00000")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("deleted key is back after reopen: %v", err)
	}
}

// The helper process of TestSurvivesProcessKill. It is started by that test
// with BCI_ROCKSDB_CRASH_HELPER=1 and is killed while it commits batches.
func TestCrashHelperProcess(t *testing.T) {
	if os.Getenv("BCI_ROCKSDB_CRASH_HELPER") != "1" {
		t.Skip("helper process for TestSurvivesProcessKill")
	}
	mode, err := strconv.Atoi(os.Getenv("BCI_ROCKSDB_CRASH_SYNC"))
	if err != nil {
		fmt.Println("BADSYNC")
		os.Exit(2)
	}
	st, err := Open(os.Getenv("BCI_ROCKSDB_CRASH_DIR"), Options{Sync: SyncMode(mode)})
	if err != nil {
		fmt.Println("OPENFAIL " + err.Error())
		os.Exit(3)
	}
	for i := 0; ; i++ {
		b := storage.NewBatch()
		for j := 0; j < crashKeysPerBatch; j++ {
			b.Put(crashKey(i, j), crashValue(i, j))
		}
		if err := st.Apply(b); err != nil {
			fmt.Println("APPLYFAIL " + err.Error())
			os.Exit(4)
		}
		// This line goes out only after Apply returned, so the parent knows
		// the batch is one the store has promised to keep.
		fmt.Println(i)
	}
}

const (
	crashKeysPerBatch = 4
	crashKillAfter    = 200
	crashBatchLimit   = 100000
)

func crashKey(i, j int) []byte { return []byte(fmt.Sprintf("batch/%07d/chunk/%d", i, j)) }

func crashValue(i, j int) []byte {
	return []byte(fmt.Sprintf("payload-%d-%d-%s", i, j, strings.Repeat("x", 200)))
}

// LIVE_CONFIG_SPEC ("Validation and shutdown"): SIGKILL is handled by the next
// start as a crash. A helper process commits batches one by one and is killed
// without warning. After the kill every batch it had reported is whole, and
// no batch is partly there.
func TestSurvivesProcessKill(t *testing.T) {
	for _, mode := range []SyncMode{SyncAlways, SyncNever} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "wal")
			cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelperProcess$")
			cmd.Env = append(os.Environ(),
				"BCI_ROCKSDB_CRASH_HELPER=1",
				"BCI_ROCKSDB_CRASH_DIR="+dir,
				"BCI_ROCKSDB_CRASH_SYNC="+strconv.Itoa(int(mode)),
			)
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Wait()

			// Read until the helper reported enough batches, then kill it
			// while it is committing the next ones.
			lines := make(chan string)
			stop := make(chan struct{})
			go func() {
				scanner := bufio.NewScanner(out)
				for scanner.Scan() {
					select {
					case lines <- scanner.Text():
					case <-stop:
						return
					}
				}
				close(lines)
			}()
			reported := -1
			for reported+1 < crashKillAfter {
				select {
				case line, ok := <-lines:
					if !ok {
						t.Fatalf("helper stopped after %d batches", reported+1)
					}
					if n, err := strconv.Atoi(line); err == nil {
						reported = n
						continue
					}
					if strings.HasPrefix(line, "OPENFAIL") || strings.HasPrefix(line, "APPLYFAIL") || line == "BADSYNC" {
						t.Fatalf("helper failed: %s", line)
					}
				case <-time.After(60 * time.Second):
					t.Fatalf("helper reported only %d batches in 60 seconds", reported+1)
				}
			}
			promised := reported + 1 // batches 0..reported had returned

			// The helper is another process that holds the store, so this
			// process is refused until it exits.
			if other, err := Open(dir, Options{Sync: mode}); other != nil || !errors.Is(err, ErrLocked) {
				Close(other)
				t.Fatalf("open while the helper holds the store: got store %v, err %v; want ErrLocked", other != nil, err)
			}

			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			close(stop)
			if err := cmd.Wait(); err == nil {
				t.Fatal("the helper exited by itself instead of by the kill")
			}
			t.Logf("killed after %d returned batches", promised)

			st := openTest(t, dir, Options{Sync: mode})
			counts := map[int]int{}
			wrong := 0
			var highest int
			err = st.Iterate([]byte("batch/"), []byte("batch0"), func(k, v []byte) error {
				var i, j int
				if _, err := fmt.Sscanf(string(k), "batch/%7d/chunk/%d", &i, &j); err != nil {
					return fmt.Errorf("key %q", k)
				}
				if !bytes.Equal(v, crashValue(i, j)) {
					wrong++
				}
				counts[i]++
				if i > highest {
					highest = i
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if wrong != 0 {
				t.Fatalf("%d values differ from what was written", wrong)
			}
			for i := 0; i <= highest; i++ {
				n := counts[i]
				// A batch is whole or absent. Batches the helper had reported
				// must be whole.
				if n != 0 && n != crashKeysPerBatch {
					t.Fatalf("batch %d is partly visible after SIGKILL: %d of %d keys", i, n, crashKeysPerBatch)
				}
				if i < promised && n != crashKeysPerBatch {
					t.Fatalf("batch %d returned before the kill but has %d of %d keys after it", i, n, crashKeysPerBatch)
				}
			}
			if highest > crashBatchLimit {
				t.Fatalf("unexpected batch index %d", highest)
			}
		})
	}
}
