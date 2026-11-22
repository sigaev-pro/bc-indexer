package storage

import (
	"bytes"
	"errors"
	"testing"
)

func TestMemoryGetPutDelete(t *testing.T) {
	m := NewMemory()
	if _, err := m.Get([]byte("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get of missing key: got %v, want ErrNotFound", err)
	}
	if err := m.Put([]byte("a"), []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	v, err := m.Get([]byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v, []byte{1, 2, 3}) {
		t.Fatalf("Get returned %v, want [1 2 3]", v)
	}
	// The returned value is a copy: writing through it must not corrupt the
	// store.
	v[0] = 0xFF
	again, _ := m.Get([]byte("a"))
	if again[0] != 1 {
		t.Fatalf("Get did not copy: stored value mutated to %v", again)
	}
	if err := m.Delete([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete([]byte("a")); err != nil {
		t.Fatalf("deleting a missing key must not error, got %v", err)
	}
	if _, err := m.Get([]byte("a")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("key still present after delete: %v", err)
	}
}

func TestMemoryApplyIsAtomic(t *testing.T) {
	m := NewMemory()
	big := make([]byte, MaxBatchBytes)
	ok := NewBatch()
	ok.Put([]byte("k1"), []byte("v1"))
	ok.Delete([]byte("k2"))
	if err := m.Apply(ok); err != nil {
		t.Fatal(err)
	}

	b := NewBatch()
	b.Put([]byte("k1"), []byte("overwritten"))
	b.Delete([]byte("k1"))
	// One operation pushes the batch over the bound; nothing may be applied.
	b.Put([]byte("big"), big)
	if err := m.Apply(b); !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("oversized batch: got %v, want ErrBatchTooLarge", err)
	}
	v, err := m.Get([]byte("k1"))
	if err != nil || string(v) != "v1" {
		t.Fatalf("rejected batch leaked a mutation: k1=%q err=%v", v, err)
	}
	if _, err := m.Get([]byte("big")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oversized batch wrote its payload: %v", err)
	}
}

func TestMemoryApplyVisibility(t *testing.T) {
	m := NewMemory()
	b := NewBatch()
	b.Put([]byte("x"), []byte("1"))
	b.Put([]byte("y"), []byte("2"))
	if err := m.Apply(b); err != nil {
		t.Fatal(err)
	}
	// Every operation of an applied batch is visible at once; there is no
	// partial state after Apply returns.
	for _, k := range []string{"x", "y"} {
		if _, err := m.Get([]byte(k)); err != nil {
			t.Fatalf("key %q missing after Apply: %v", k, err)
		}
	}
}

func TestMemoryIterateOrderAndBounds(t *testing.T) {
	m := NewMemory()
	keys := []string{"b/1", "b/2", "b/10", "a", "c"}
	for _, k := range keys {
		if err := m.Put([]byte(k), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	if err := m.Iterate([]byte("b/"), []byte("b0"), func(k, v []byte) error {
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
	if err := m.Iterate([]byte("b/1"), []byte("b/2"), func(k, v []byte) error {
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

func TestMemoryIterateStopAndError(t *testing.T) {
	m := NewMemory()
	for _, k := range []string{"a", "b", "c"} {
		if err := m.Put([]byte(k), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	if err := m.Iterate(nil, nil, func(k, v []byte) error {
		count++
		return ErrStopIterate
	}); err != nil {
		t.Fatalf("ErrStopIterate must be swallowed, got %v", err)
	}
	if count != 1 {
		t.Fatalf("visit ran %d times after stop, want 1", count)
	}
	sentinel := errors.New("boom")
	if err := m.Iterate(nil, nil, func(k, v []byte) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("visit error must be returned, got %v", err)
	}
}

func TestMemoryIterateSnapshotIsStable(t *testing.T) {
	m := NewMemory()
	for _, k := range []string{"a", "b", "c"} {
		if err := m.Put([]byte(k), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	seen := 0
	err := m.Iterate(nil, nil, func(k, v []byte) error {
		seen++
		// Mutating during a visit must not disturb the remaining pairs and
		// must not deadlock.
		return m.Put([]byte("z"), []byte("new"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != 3 {
		t.Fatalf("visit saw %d pairs, want 3 (snapshot must be stable)", seen)
	}
	if _, err := m.Get([]byte("z")); err != nil {
		t.Fatalf("write during visit lost: %v", err)
	}
}

func TestBatchAccounting(t *testing.T) {
	b := NewBatch()
	if b.Len() != 0 || b.Size() != 0 {
		t.Fatalf("empty batch: len=%d size=%d, want 0 0", b.Len(), b.Size())
	}
	b.Put([]byte("key"), []byte("value"))
	b.Delete([]byte("gone"))
	want := len("key") + len("value") + BatchOpOverhead + len("gone") + BatchOpOverhead
	if b.Size() != want {
		t.Fatalf("batch size %d, want %d", b.Size(), want)
	}
	b.Reset()
	if b.Len() != 0 || b.Size() != 0 {
		t.Fatalf("reset batch: len=%d size=%d, want 0 0", b.Len(), b.Size())
	}
}

func TestBatchCopiesInputs(t *testing.T) {
	m := NewMemory()
	key := []byte("k")
	val := []byte("v")
	b := NewBatch()
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
