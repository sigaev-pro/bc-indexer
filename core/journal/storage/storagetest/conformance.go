// Package storagetest holds the conformance suite of the journal storage seam.
// Every implementation of storage.Storage runs the same cases, so a store is
// known to hold the seam contract by the same evidence as the memory store.
//
// An implementation's tests call Run with a function that returns a fresh,
// empty store. The durable implementation lives in the separate module
// core/journal/storage/rocksdb and runs this suite against the real engine.
//
// This package imports testing, so only test code should import it.
package storagetest

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// NewStore returns a fresh, empty store for one conformance case.
type NewStore func(t *testing.T) storage.Storage

// Run runs every conformance case against a store from newStore.
func Run(t *testing.T, newStore NewStore) {
	t.Helper()
	cases := []struct {
		name string
		run  func(t *testing.T, st storage.Storage)
	}{
		{"GetPutDelete", GetPutDelete},
		{"ApplyIsAtomic", ApplyIsAtomic},
		{"ApplyVisibility", ApplyVisibility},
		{"ApplyDeletesExistingKeys", ApplyDeletesExistingKeys},
		{"BatchOrderIsCommitOrder", BatchOrderIsCommitOrder},
		{"PutOverwritesExistingKey", PutOverwritesExistingKey},
		{"IterateOrderAndBounds", IterateOrderAndBounds},
		{"IterateHandsOutStoredValues", IterateHandsOutStoredValues},
		{"IterateStopAndError", IterateStopAndError},
		{"IterateSnapshotIsStable", IterateSnapshotIsStable},
		{"InputBytesAreCopied", InputBytesAreCopied},
		{"EmptyKeysAndValues", EmptyKeysAndValues},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.run(t, newStore(t))
		})
	}
}

// GetPutDelete checks missing keys, stored values, returned copies, and that
// deleting a missing key is not an error.
func GetPutDelete(t *testing.T, st storage.Storage) {
	t.Helper()
	if _, err := st.Get([]byte("missing")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get of missing key: got %v, want ErrNotFound", err)
	}
	if err := st.Put([]byte("a"), []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	v, err := st.Get([]byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v, []byte{1, 2, 3}) {
		t.Fatalf("Get returned %v, want [1 2 3]", v)
	}
	// The returned value is a copy: writing through it must not corrupt the
	// store.
	v[0] = 0xFF
	again, _ := st.Get([]byte("a"))
	if again[0] != 1 {
		t.Fatalf("Get did not copy: stored value mutated to %v", again)
	}
	if err := st.Delete([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete([]byte("a")); err != nil {
		t.Fatalf("deleting a missing key must not error, got %v", err)
	}
	if _, err := st.Get([]byte("a")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("key still present after delete: %v", err)
	}
}

// ApplyIsAtomic checks that an oversized batch is rejected before any of its
// operations becomes visible.
func ApplyIsAtomic(t *testing.T, st storage.Storage) {
	t.Helper()
	big := make([]byte, storage.MaxBatchBytes)
	ok := storage.NewBatch()
	ok.Put([]byte("k1"), []byte("v1"))
	ok.Delete([]byte("k2"))
	if err := st.Apply(ok); err != nil {
		t.Fatal(err)
	}

	b := storage.NewBatch()
	b.Put([]byte("k1"), []byte("overwritten"))
	b.Delete([]byte("k1"))
	// One operation pushes the batch over the bound; nothing may be applied.
	b.Put([]byte("big"), big)
	if err := st.Apply(b); !errors.Is(err, storage.ErrBatchTooLarge) {
		t.Fatalf("oversized batch: got %v, want ErrBatchTooLarge", err)
	}
	v, err := st.Get([]byte("k1"))
	if err != nil || string(v) != "v1" {
		t.Fatalf("rejected batch leaked a mutation: k1=%q err=%v", v, err)
	}
	if _, err := st.Get([]byte("big")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("oversized batch wrote its payload: %v", err)
	}
}

// ApplyVisibility checks that every operation of an applied batch is visible
// once Apply returns.
func ApplyVisibility(t *testing.T, st storage.Storage) {
	t.Helper()
	b := storage.NewBatch()
	b.Put([]byte("x"), []byte("1"))
	b.Put([]byte("y"), []byte("2"))
	if err := st.Apply(b); err != nil {
		t.Fatal(err)
	}
	// Every operation of an applied batch is visible at once; there is no
	// partial state after Apply returns.
	for _, k := range []string{"x", "y"} {
		if _, err := st.Get([]byte(k)); err != nil {
			t.Fatalf("key %q missing after Apply: %v", k, err)
		}
	}
}

// IterateOrderAndBounds checks unsigned byte order and the half-open range
// [lo, hi).
func IterateOrderAndBounds(t *testing.T, st storage.Storage) {
	t.Helper()
	keys := []string{"b/1", "b/2", "b/10", "a", "c"}
	for _, k := range keys {
		if err := st.Put([]byte(k), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	if err := st.Iterate([]byte("b/"), []byte("b0"), func(k, v []byte) error {
		got = append(got, string(k))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Unsigned byte order: "b/1" < "b/10" < "b/2".
	want := []string{"b/1", "b/10", "b/2"}
	if len(got) != len(want) {
		t.Fatalf("prefix iterate got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("prefix iterate got %v, want %v", got, want)
		}
	}

	got = got[:0]
	if err := st.Iterate([]byte("b/1"), []byte("b/2"), func(k, v []byte) error {
		got = append(got, string(k))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// "b/10" sorts before "b/2" in unsigned byte order, so the half-open
	// range [b/1,b/2) holds both "b/1" and "b/10".
	if len(got) != 2 || got[0] != "b/1" || got[1] != "b/10" {
		t.Fatalf("half-open range [b/1,b/2) got %v, want [b/1 b/10]", got)
	}
}

// IterateStopAndError checks that ErrStopIterate ends the walk without an
// error and that any other visit error is returned.
func IterateStopAndError(t *testing.T, st storage.Storage) {
	t.Helper()
	for _, k := range []string{"a", "b", "c"} {
		if err := st.Put([]byte(k), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	if err := st.Iterate(nil, nil, func(k, v []byte) error {
		count++
		return storage.ErrStopIterate
	}); err != nil {
		t.Fatalf("ErrStopIterate must be swallowed, got %v", err)
	}
	if count != 1 {
		t.Fatalf("visit ran %d times after stop, want 1", count)
	}
	sentinel := errors.New("boom")
	if err := st.Iterate(nil, nil, func(k, v []byte) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("visit error must be returned, got %v", err)
	}
}

// IterateSnapshotIsStable checks that a write made during a visit neither
// disturbs the remaining pairs nor deadlocks.
func IterateSnapshotIsStable(t *testing.T, st storage.Storage) {
	t.Helper()
	for _, k := range []string{"a", "b", "c"} {
		if err := st.Put([]byte(k), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	seen := 0
	err := st.Iterate(nil, nil, func(k, v []byte) error {
		seen++
		// Mutating during a visit must not disturb the remaining pairs and
		// must not deadlock.
		return st.Put([]byte("z"), []byte("new"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != 3 {
		t.Fatalf("visit saw %d pairs, want 3 (snapshot must be stable)", seen)
	}
	if _, err := st.Get([]byte("z")); err != nil {
		t.Fatalf("write during visit lost: %v", err)
	}
}

// InputBytesAreCopied checks that a store keeps what was written even when
// the caller reuses its buffers after that.
func InputBytesAreCopied(t *testing.T, st storage.Storage) {
	t.Helper()
	key := []byte("k")
	val := []byte("v")
	b := storage.NewBatch()
	b.Put(key, val)
	key[0] = 'x'
	val[0] = 'w'
	if err := st.Apply(b); err != nil {
		t.Fatal(err)
	}
	if got, err := st.Get([]byte("k")); err != nil || string(got) != "v" {
		t.Fatalf("buffered key was aliased by later mutation: %q %v", got, err)
	}
	pkey := []byte("p")
	pval := []byte("q")
	if err := st.Put(pkey, pval); err != nil {
		t.Fatal(err)
	}
	pkey[0] = 'x'
	pval[0] = 'w'
	if got, err := st.Get([]byte("p")); err != nil || string(got) != "q" {
		t.Fatalf("Put aliased the caller's buffers: %q %v", got, err)
	}
}

// EmptyKeysAndValues checks the corners of the key space: an empty value, an
// empty key, and keys with high bytes that sort after all ASCII.
func EmptyKeysAndValues(t *testing.T, st storage.Storage) {
	t.Helper()
	if err := st.Put([]byte("empty"), nil); err != nil {
		t.Fatal(err)
	}
	if got, err := st.Get([]byte("empty")); err != nil || len(got) != 0 {
		t.Fatalf("empty value: got %q %v, want empty and no error", got, err)
	}
	if err := st.Put([]byte{}, []byte("root")); err != nil {
		t.Fatal(err)
	}
	if err := st.Put([]byte{0xFF, 0xFE}, []byte("high")); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := st.Iterate(nil, nil, func(k, v []byte) error {
		got = append(got, string(k)+"="+string(v))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"=root", "empty=", string([]byte{0xFF, 0xFE}) + "=high"}
	if len(got) != len(want) {
		t.Fatalf("full iterate got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("full iterate got %q, want %q", got, want)
		}
	}
	// An empty key is inside [nil, nil) only because an empty hi means "to
	// the end"; an empty lo is not a lower bound.
	if err := st.Iterate(nil, []byte{1}, func(k, v []byte) error {
		if len(k) != 0 {
			t.Fatalf("key %q is not below the bound", k)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// ApplyDeletesExistingKeys checks that Apply carries out the deletions of a
// batch on keys that are there, next to its writes. ApplyIsAtomic deletes a
// key that never existed, which a store that ignores deletions would pass.
// The journals clean up payload with batches that delete existing keys
// (the Cleanup call of core/journal/producer, the Release step of
// gateway/journal).
func ApplyDeletesExistingKeys(t *testing.T, st storage.Storage) {
	t.Helper()
	for _, k := range []string{"k1", "k2", "k3", "k4"} {
		if err := st.Put([]byte(k), []byte("value of "+k)); err != nil {
			t.Fatal(err)
		}
	}
	b := storage.NewBatch()
	b.Delete([]byte("k1"))
	b.Delete([]byte("k3"))
	b.Put([]byte("k5"), []byte("value of k5"))
	b.Delete([]byte("never-there"))
	if err := st.Apply(b); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"k1", "k3"} {
		if _, err := st.Get([]byte(k)); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("key %q survived a batch that deleted it: %v", k, err)
		}
	}
	for _, k := range []string{"k2", "k4", "k5"} {
		v, err := st.Get([]byte(k))
		if err != nil || string(v) != "value of "+k {
			t.Fatalf("key %q = %q, %v after the batch; want %q", k, v, err, "value of "+k)
		}
	}

	// A batch of nothing but deletions empties the store.
	b = storage.NewBatch()
	for _, k := range []string{"k2", "k4", "k5"} {
		b.Delete([]byte(k))
	}
	if err := st.Apply(b); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := st.Iterate(nil, nil, func(k, v []byte) error {
		left++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d keys survived a batch that deleted every key", left)
	}
}

// BatchOrderIsCommitOrder checks that a batch applies its operations in the
// order given, one key after another. A store that ran every deletion
// before every write would answer both of these the wrong way.
func BatchOrderIsCommitOrder(t *testing.T, st storage.Storage) {
	t.Helper()
	if err := st.Put([]byte("replaced"), []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := st.Put([]byte("kept"), []byte("value")); err != nil {
		t.Fatal(err)
	}
	b := storage.NewBatch()
	// A deletion followed by a write of the same key leaves the new value.
	b.Delete([]byte("replaced"))
	b.Put([]byte("replaced"), []byte("new"))
	// A write followed by a deletion of the same key removes the key.
	b.Put([]byte("removed"), []byte("temporary"))
	b.Delete([]byte("removed"))
	// The same on a key the store has never seen.
	b.Delete([]byte("absent"))
	b.Put([]byte("absent"), []byte("written"))
	b.Put([]byte("gone"), []byte("written"))
	b.Delete([]byte("gone"))
	if err := st.Apply(b); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"replaced": "new", "kept": "value", "absent": "written"}
	for key, value := range want {
		got, err := st.Get([]byte(key))
		if err != nil || string(got) != value {
			t.Fatalf("key %q = %q, %v after the batch, want %q", key, got, err, value)
		}
	}
	for _, key := range []string{"removed", "gone"} {
		if _, err := st.Get([]byte(key)); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("key %q survived a batch that wrote and then deleted it: %v", key, err)
		}
	}
}

// PutOverwritesExistingKey checks that Put replaces the value of a key that
// is already there, and that the old value is gone.
func PutOverwritesExistingKey(t *testing.T, st storage.Storage) {
	t.Helper()
	for _, v := range []string{"first", "second", "third"} {
		if err := st.Put([]byte("k"), []byte(v)); err != nil {
			t.Fatal(err)
		}
		got, err := st.Get([]byte("k"))
		if err != nil || string(got) != v {
			t.Fatalf("Put %q: Get returns %q, %v", v, got, err)
		}
	}
	var pairs []string
	if err := st.Iterate(nil, nil, func(k, v []byte) error {
		pairs = append(pairs, string(k)+"="+string(v))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0] != "k=third" {
		t.Fatalf("the store holds %v, want exactly [k=third]", pairs)
	}
}

// IterateHandsOutStoredValues checks that a walk returns, under each key,
// the value that is stored there. The other walk cases look at the keys
// only.
func IterateHandsOutStoredValues(t *testing.T, st storage.Storage) {
	t.Helper()
	const keys = 20
	key := func(i int) string { return fmt.Sprintf("k%02d", i) }
	value := func(i int) string { return fmt.Sprintf("value of %02d", i*i) }
	for i := 0; i < keys; i++ {
		if err := st.Put([]byte(key(i)), []byte(value(i))); err != nil {
			t.Fatal(err)
		}
	}
	check := func(lo, hi []byte, from, to int) {
		t.Helper()
		var seen []string
		if err := st.Iterate(lo, hi, func(k, v []byte) error {
			seen = append(seen, string(k)+"="+string(v))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var want []string
		for i := from; i < to; i++ {
			want = append(want, key(i)+"="+value(i))
		}
		if len(seen) != len(want) {
			t.Fatalf("walk [%d,%d) saw %d pairs, want %d: %v", from, to, len(seen), len(want), seen)
		}
		for i := range want {
			if seen[i] != want[i] {
				t.Fatalf("pair %d of the walk is %q, want %q", i, seen[i], want[i])
			}
		}
	}
	check(nil, nil, 0, keys)
	check([]byte("k05"), []byte("k12"), 5, 12)
}
