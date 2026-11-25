package rocksdb

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// These tests need a store that holds a table file. With the engine's
// default write buffer that takes 64 MiB of writes. The test hook shrinks
// the buffer to 4 MiB, so a few MiB are enough. A store outside tests keeps
// the default (TestEngineSettingsMatchTheBaseline).

func tableKey(i int) []byte { return []byte(fmt.Sprintf("k%06d", i)) }

// writeThroughAFlush fills a store past its write buffer, so that the engine
// flushes part of the data to a table file, and closes it. The values do not
// compress. The store holds exactly one table file.
func writeThroughAFlush(t *testing.T, dir string, keys, valueBytes int) {
	t.Helper()
	testWriteBufferBytes = testBufferBytes
	st := openFast(t, dir)
	testWriteBufferBytes = 0
	b := storage.NewBatch()
	for i := 0; i < keys; i++ {
		b.Put(tableKey(i), crashValue(i, 7, valueBytes))
		if b.Len() == 8 {
			if err := st.Apply(b); err != nil {
				t.Fatal(err)
			}
			b = storage.NewBatch()
		}
	}
	if b.Len() > 0 {
		if err := st.Apply(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := Close(st); err != nil {
		t.Fatal(err)
	}
	onlyFile(t, dir, "", ".sst")
}

// A store big enough to hold table files reopens with the same state. The
// other restart tests are small enough that the engine recovers them from the
// write-ahead log alone; a real store also keeps data in table files.
func TestReopenAfterFlushToTableFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	const keys = 300
	// 300 values of 16 KiB are 4.7 MiB, past the 4 MiB buffer.
	writeThroughAFlush(t, dir, keys, 16<<10)

	st := openFast(t, dir)
	corners := storage.NewBatch()
	corners.Put([]byte("flush/empty-value"), nil)
	corners.Put([]byte{}, []byte("empty-key"))
	corners.Put([]byte{0xFF, 0x00}, []byte{0x00, 0xFF})
	corners.Put([]byte("flush/first"), []byte("written before the bulk data"))
	if err := st.Apply(corners); err != nil {
		t.Fatal(err)
	}
	before := dump(t, st)
	if len(before) != keys+4 {
		t.Fatalf("the store holds %d pairs, want %d", len(before), keys+4)
	}
	if err := Close(st); err != nil {
		t.Fatal(err)
	}

	reopened := openFast(t, dir)
	if after := dump(t, reopened); !reflect.DeepEqual(after, before) {
		t.Fatalf("state changed across close and reopen: %d pairs before, %d after", len(before), len(after))
	}
}

// A damaged block of a table file is an error, never the silent end of a
// walk. The iterator reports Valid() = false after a read error, exactly as
// it does at the end of the range; only its error tells the two apart. The
// footer and the index of the file are at its end and stay intact here, so
// the store opens and the damage shows when the block is read.
func TestIterateReportsADamagedTableBlock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	const keys, valueBytes = 6000, 1024
	// 6 MiB: about 4 MiB go to the table file, the rest stays in the log.
	writeThroughAFlush(t, dir, keys, valueBytes)
	table := onlyFile(t, dir, "", ".sst")
	info, err := os.Stat(table)
	if err != nil {
		t.Fatal(err)
	}
	overwrite(t, table, info.Size()/3, 4096, 0xA5)

	st := openFast(t, dir)
	seen := 0
	err = st.Iterate(nil, nil, func(k, v []byte) error {
		var i int
		if _, err := fmt.Sscanf(string(k), "k%6d", &i); err != nil {
			return err
		}
		if !bytes.Equal(v, crashValue(i, 7, valueBytes)) {
			return fmt.Errorf("key %q holds another value than the one written", k)
		}
		seen++
		return nil
	})
	if err == nil {
		t.Fatalf("Iterate returned no error over a damaged table file; it visited %d of %d keys", seen, keys)
	}
	if !strings.Contains(err.Error(), "Corruption") {
		t.Fatalf("the error does not carry the engine's corruption status: %v", err)
	}
	// A read of a key in the damaged block is an error as well, not a
	// missing key and not another value.
	corrupt := 0
	for i := keys / 6; i < keys/2; i++ {
		v, err := st.Get(tableKey(i))
		switch {
		case err != nil:
			if errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("key %d reads as missing in a damaged table file", i)
			}
			corrupt++
		case !bytes.Equal(v, crashValue(i, 7, valueBytes)):
			t.Fatalf("key %d holds another value than the one written", i)
		}
	}
	if corrupt == 0 {
		t.Fatal("Get returned no error for any key around the damaged block")
	}
}
