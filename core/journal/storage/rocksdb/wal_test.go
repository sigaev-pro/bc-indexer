package rocksdb

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// The store sets the engine's log recovery mode to
// kTolerateCorruptedTailRecords. The engine's default, point-in-time
// recovery, stops at the first damage in the write-ahead log and opens the
// store without the records after it, and without an error. The mode set
// here keeps the one thing a crash needs, an incomplete last record, and
// refuses everything else (REC-T11: corruption is an explicit error, never a
// silent skip; research/ROCKSDB_STACK: automatic repair that drops records is
// not recovery).
//
// The rule, as the engine implements it (db_impl_open.cc, the status
// handling of RecoverLogFiles):
//   - a record that a crash left incomplete at the end of the log is not
//     reported as damage. It is dropped and every record before it stays.
//   - any reported damage, in the middle or at the end, fails the open.
//
// The stores here are written without a sync per write: the tests are about
// what the engine reads back from the log file, which holds every returned
// write with or without the sync.

const (
	logBatches      = 1500
	logKeysPerBatch = 4
	logValueBytes   = 215
)

func logKey(i, j int) []byte {
	return []byte(fmt.Sprintf("batch/%05d/chunk/%d", i, j))
}

func logValue(i, j int) []byte {
	return []byte(fmt.Sprintf("payload-%d-%d-%s", i, j, strings.Repeat("x", logValueBytes-32)))
}

// writeLogOnlyStore fills a store with batches and closes it. The data is
// small enough to stay in the write-ahead log: no table file is written. It
// returns the log file and the size the log had before the last batch.
func writeLogOnlyStore(t *testing.T, dir string) (wal string, sizeBeforeLast int64) {
	t.Helper()
	st := openFast(t, dir)
	for i := 0; i < logBatches; i++ {
		if i == logBatches-1 {
			info, err := os.Stat(onlyFile(t, dir, "", ".log"))
			if err != nil {
				t.Fatal(err)
			}
			sizeBeforeLast = info.Size()
		}
		b := storage.NewBatch()
		for j := 0; j < logKeysPerBatch; j++ {
			b.Put(logKey(i, j), logValue(i, j))
		}
		if err := st.Apply(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := Close(st); err != nil {
		t.Fatal(err)
	}
	if tables := filesNamed(t, dir, "", ".sst"); len(tables) != 0 {
		t.Fatalf("the data reached a table file, so this store no longer tests the log: %v", tables)
	}
	return onlyFile(t, dir, "", ".log"), sizeBeforeLast
}

// countLogBatches returns, per batch, how many of its keys the store holds.
// A key with another value than the one written fails the test.
func countLogBatches(t *testing.T, st storage.Storage) map[int]int {
	t.Helper()
	counts := map[int]int{}
	err := st.Iterate([]byte("batch/"), []byte("batch0"), func(k, v []byte) error {
		var i, j int
		if _, err := fmt.Sscanf(string(k), "batch/%5d/chunk/%d", &i, &j); err != nil {
			return err
		}
		if !bytes.Equal(v, logValue(i, j)) {
			return fmt.Errorf("key %q holds another value than the one written", k)
		}
		counts[i]++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

// assertWhole checks that every batch in [from, to) is whole and that no
// batch in the store is partly there.
func assertWhole(t *testing.T, counts map[int]int, from, to int) {
	t.Helper()
	for i, n := range counts {
		if n != 0 && n != logKeysPerBatch {
			t.Fatalf("batch %d is partly visible: %d of %d keys", i, n, logKeysPerBatch)
		}
	}
	for i := from; i < to; i++ {
		if counts[i] != logKeysPerBatch {
			t.Fatalf("batch %d holds %d of %d keys, want all", i, counts[i], logKeysPerBatch)
		}
	}
}

// overwrite replaces length bytes of the file at offset with a fixed byte.
func overwrite(t *testing.T, path string, offset int64, length int, filler byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{filler}, length), offset); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// Damage in the middle of the log: the open fails and the log is left as it
// was. Nothing is dropped and nothing is repaired.
func TestDamagedLogIsRefusedAndLeftAlone(t *testing.T) {
	for _, damage := range []struct {
		name   string
		offset func(size int64) int64
		filler byte
	}{
		{"zeros in the middle", func(size int64) int64 { return size / 2 }, 0x00},
		{"ones in the middle", func(size int64) int64 { return size / 3 }, 0xFF},
	} {
		t.Run(damage.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "wal")
			wal, _ := writeLogOnlyStore(t, dir)
			info, err := os.Stat(wal)
			if err != nil {
				t.Fatal(err)
			}
			const damagedBytes = 4096
			offset := damage.offset(info.Size())
			if offset+damagedBytes >= info.Size()-2*logKeysPerBatch*(logValueBytes+32) {
				t.Fatalf("the damage would reach the last records: offset %d in a %d byte log", offset, info.Size())
			}
			overwrite(t, wal, offset, damagedBytes, damage.filler)
			damaged := readFile(t, wal)

			st, err := Open(dir, Options{})
			if st != nil {
				Close(st)
				t.Fatal("Open returned a store although the log is damaged in the middle")
			}
			wantErr(t, err, ErrOpen, "open of a store whose log is damaged in the middle")
			if !strings.Contains(err.Error(), "Corruption") {
				t.Fatalf("the error does not carry the engine's corruption status: %v", err)
			}
			if !bytes.Equal(readFile(t, wal), damaged) {
				t.Fatal("the refused open changed the log")
			}
		})
	}
}

// A crash that cut the last record leaves it incomplete. The store opens,
// the incomplete batch is gone, and every batch before it is whole.
func TestIncompleteLastLogRecordIsDropped(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	wal, sizeBeforeLast := writeLogOnlyStore(t, dir)
	info, err := os.Stat(wal)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= sizeBeforeLast {
		t.Fatalf("the last batch wrote nothing to the log: size %d, before %d", info.Size(), sizeBeforeLast)
	}
	// Cut the last write in half.
	cut := sizeBeforeLast + (info.Size()-sizeBeforeLast)/2
	if err := os.Truncate(wal, cut); err != nil {
		t.Fatal(err)
	}

	st := openTest(t, dir)
	counts := countLogBatches(t, st)
	assertWhole(t, counts, 0, logBatches-1)
	if counts[logBatches-1] != 0 {
		t.Fatalf("the incomplete last batch came back with %d of %d keys", counts[logBatches-1], logKeysPerBatch)
	}
	// The store is usable: the next batch goes in and stays.
	b := storage.NewBatch()
	for j := 0; j < logKeysPerBatch; j++ {
		b.Put(logKey(logBatches, j), logValue(logBatches, j))
	}
	if err := st.Apply(b); err != nil {
		t.Fatal(err)
	}
	if err := Close(st); err != nil {
		t.Fatal(err)
	}
	counts = countLogBatches(t, openTest(t, dir))
	assertWhole(t, counts, 0, logBatches-1)
	if counts[logBatches] != logKeysPerBatch {
		t.Fatalf("the batch written after the recovery is not whole: %d keys", counts[logBatches])
	}
}

// A last record that is whole in length but damaged in content is not a torn
// write. The engine reports it, and the open fails rather than guess.
func TestDamagedLastLogRecordIsRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	wal, sizeBeforeLast := writeLogOnlyStore(t, dir)
	// The record header is 7 bytes; damage starts inside its payload.
	overwrite(t, wal, sizeBeforeLast+8, 64, 0xA5)
	damaged := readFile(t, wal)

	st, err := Open(dir, Options{})
	if st != nil {
		Close(st)
		t.Fatal("Open returned a store although the last record is damaged")
	}
	wantErr(t, err, ErrOpen, "open of a store whose last record is damaged")
	if !strings.Contains(err.Error(), "Corruption") {
		t.Fatalf("the error does not carry the engine's corruption status: %v", err)
	}
	if !bytes.Equal(readFile(t, wal), damaged) {
		t.Fatal("the refused open changed the log")
	}
}
