package storage_test

import (
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage/storagetest"
)

// The store behavior of the memory implementation is the conformance suite in
// storagetest, which every storage.Storage implementation runs. The durable
// implementation is the separate module core/journal/storage/rocksdb.

func newMemoryStore(*testing.T) storage.Storage { return storage.NewMemory() }

func TestMemoryConformance(t *testing.T) {
	storagetest.Run(t, newMemoryStore)
}

func TestBatchAccounting(t *testing.T) {
	b := storage.NewBatch()
	if b.Len() != 0 || b.Size() != 0 {
		t.Fatalf("empty batch: len=%d size=%d, want 0 0", b.Len(), b.Size())
	}
	b.Put([]byte("key"), []byte("value"))
	b.Delete([]byte("gone"))
	want := len("key") + len("value") + storage.BatchOpOverhead + len("gone") + storage.BatchOpOverhead
	if b.Size() != want {
		t.Fatalf("batch size %d, want %d", b.Size(), want)
	}
	b.Reset()
	if b.Len() != 0 || b.Size() != 0 {
		t.Fatalf("reset batch: len=%d size=%d, want 0 0", b.Len(), b.Size())
	}
}

func TestBatchCopiesInputs(t *testing.T) {
	m := storage.NewMemory()
	key := []byte("k")
	val := []byte("v")
	b := storage.NewBatch()
	b.Put(key, val)
	key[0] = 'x'
	val[0] = 'w'
	if err := m.Apply(b); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get([]byte("k")); err != nil {
		t.Fatalf("buffered key was aliased by later mutation: %v", err)
	}
}

func TestBatchVisitReportsOperationsInOrder(t *testing.T) {
	b := storage.NewBatch()
	if n := visitAll(b); n != 0 {
		t.Fatalf("empty batch reported %d operations, want 0", n)
	}
	b.Put([]byte("a"), []byte("1"))
	b.Delete([]byte("b"))
	b.Put([]byte("c"), nil)
	b.Delete([]byte("a"))

	var kinds []storage.OpKind
	var keys, values []string
	b.Visit(func(op storage.Op) bool {
		kinds = append(kinds, op.Kind)
		keys = append(keys, string(op.Key))
		values = append(values, string(op.Value))
		return true
	})
	wantKinds := []storage.OpKind{storage.OpPut, storage.OpDelete, storage.OpPut, storage.OpDelete}
	for i, kind := range wantKinds {
		if kinds[i] != kind {
			t.Fatalf("operation %d kind = %d, want %d", i, kinds[i], kind)
		}
	}
	// A delete reports no value; a put of a nil value reports an empty one.
	if want := []string{"a", "b", "c", "a"}; !equalStrings(keys, want) {
		t.Fatalf("keys = %q, want %q", keys, want)
	}
	if want := []string{"1", "", "", ""}; !equalStrings(values, want) {
		t.Fatalf("values = %q, want %q", values, want)
	}

	// Reset empties the batch but leaves the slices an earlier Visit copied.
	b.Reset()
	if visitAll(b) != 0 {
		t.Fatal("Reset must empty the batch")
	}
	if want := []string{"a", "b", "c", "a"}; !equalStrings(keys, want) {
		t.Fatalf("Reset changed the slices reported by Visit: %q", keys)
	}

	// Returning false stops the walk and does not consume the batch.
	b.Put([]byte("first"), []byte("1"))
	b.Put([]byte("second"), []byte("2"))
	seen := 0
	b.Visit(func(op storage.Op) bool {
		seen++
		return false
	})
	if seen != 1 {
		t.Fatalf("Visit ran the function %d times after a false result, want 1", seen)
	}
	if b.Len() != 2 {
		t.Fatalf("a stopped Visit must not consume the batch: len = %d, want 2", b.Len())
	}
}

func visitAll(b *storage.Batch) (n int) {
	b.Visit(func(storage.Op) bool {
		n++
		return true
	})
	return n
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
