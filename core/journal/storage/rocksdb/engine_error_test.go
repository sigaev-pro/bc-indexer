package rocksdb

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// REC-T11 and DELIVERY_STATE_MACHINE ("Errors, ENOSPC, compatibility"): a
// physical write error is an explicit error. WAL_SAVED is never declared
// without a durable write, so a write the engine cannot persist must come
// back as an error from Put, Delete and Apply.
//
// The error is the engine's own. The store's directory is made read-only
// after the open, and the write buffer is small (1 MiB): when the buffer
// fills, the engine has to create its next log file in that directory,
// cannot, and refuses the write.
func TestWriteErrorsAreReturned(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	testWriteBufferBytes = 1 << 20
	st := openFast(t, dir)
	testWriteBufferBytes = 0

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	probe := filepath.Join(dir, "not-writable-probe")
	if err := os.WriteFile(probe, nil, 0o644); err == nil {
		os.Remove(probe)
		t.Skip("the directory stays writable for this user, so the engine cannot be stopped this way")
	}

	value := make([]byte, 32<<10)
	acknowledged, failed := 0, -1
	var first error
	for i := 0; i < 256; i++ {
		err := st.Put([]byte(fmt.Sprintf("k%04d", i)), value)
		if err != nil {
			failed, first = i, err
			break
		}
		acknowledged++
	}
	if first == nil {
		t.Fatalf("no write failed, although the engine cannot create its next log file (%d writes)", acknowledged)
	}
	t.Logf("write %d returned: %v", failed, first)

	// Every kind of write reports it.
	if err := st.Delete([]byte("k0000")); err == nil {
		t.Error("Delete returned nil after the engine stopped persisting writes")
	} else {
		t.Logf("Delete returned: %v", err)
	}
	b := storage.NewBatch()
	b.Put([]byte("batch/1"), []byte("value"))
	if err := st.Apply(b); err == nil {
		t.Error("Apply returned nil after the engine stopped persisting writes")
	} else {
		t.Logf("Apply returned: %v", err)
	}

	// Nothing was acknowledged that is not there. Every write that returned
	// is in the store after a restart, and the write that failed is not.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Close(st); err != nil {
		t.Fatal(err)
	}
	reopened := openFast(t, dir)
	for i := 0; i < acknowledged; i++ {
		if got, err := reopened.Get([]byte(fmt.Sprintf("k%04d", i))); err != nil || len(got) != len(value) {
			t.Fatalf("write %d returned before the error and is not in the store after the restart: %d bytes, %v", i, len(got), err)
		}
	}
	for _, key := range []string{fmt.Sprintf("k%04d", failed), "batch/1"} {
		if _, err := reopened.Get([]byte(key)); err == nil {
			t.Fatalf("key %q is in the store although its write returned an error", key)
		}
	}
}
