package session

import (
	"errors"
	"testing"
)

// batchN is the id of the n-th released batch of a test.
func batchN(n int) [40]byte {
	var b [40]byte
	b[39] = byte(n)
	b[38] = byte(n >> 8)
	b[37] = byte(n >> 16)
	return b
}

// refused reports whether the gate rejects a callback of the current
// generation for one batch.
func refused(p *PublisherGate, batch [40]byte) bool {
	err := p.Apply(Callback{Batch: batch, Generation: p.Generation()}, func() error { return nil })
	if errors.Is(err, ErrBatchReleased) {
		return true
	}
	if err != nil {
		panic("unexpected callback error: " + err.Error())
	}
	return false
}

// The per-batch release barriers are bounded: the gate keeps the most
// recent ones and forgets the oldest beyond the bound, however long the
// gateway runs.
func TestPublisherReleaseBarriersAreBounded(t *testing.T) {
	p := NewPublisherGate()
	for n := 0; n < maxReleasedBatches; n++ {
		p.Release(batchN(n))
	}
	if len(p.released) != maxReleasedBatches {
		t.Fatalf("the gate remembers %d releases, want %d", len(p.released), maxReleasedBatches)
	}
	// One more release forgets the oldest; every other barrier stands.
	p.Release(batchN(maxReleasedBatches))
	if len(p.released) != maxReleasedBatches {
		t.Fatalf("the gate remembers %d releases after the bound, want %d", len(p.released), maxReleasedBatches)
	}
	if refused(p, batchN(0)) {
		t.Fatal("the oldest release was not forgotten")
	}
	for n := 1; n <= maxReleasedBatches; n++ {
		if !refused(p, batchN(n)) {
			t.Fatalf("release %d was forgotten, want remembered", n)
		}
	}
	// The ring wraps: after many more releases the latest max are remembered
	// and nothing else.
	for n := maxReleasedBatches + 1; n < 3*maxReleasedBatches; n++ {
		p.Release(batchN(n))
	}
	if len(p.released) != maxReleasedBatches {
		t.Fatalf("the gate remembers %d releases after wrapping, want %d", len(p.released), maxReleasedBatches)
	}
	for n := 0; n < 2*maxReleasedBatches; n++ {
		if refused(p, batchN(n)) {
			t.Fatalf("release %d is still remembered, want forgotten", n)
		}
	}
	for n := 2 * maxReleasedBatches; n < 3*maxReleasedBatches; n++ {
		if !refused(p, batchN(n)) {
			t.Fatalf("release %d was forgotten, want remembered", n)
		}
	}
}

// A repeated Release of one batch puts its barrier once and takes one slot,
// so it does not push another barrier out.
func TestPublisherRepeatedReleaseTakesOneSlot(t *testing.T) {
	p := NewPublisherGate()
	p.Release(batchN(1))
	for n := 0; n < maxReleasedBatches-1; n++ {
		p.Release(batchN(100 + n))
	}
	if !refused(p, batchN(1)) {
		t.Fatal("the first release was forgotten")
	}
	// Repeating it changes nothing: the barrier is already in place.
	p.Release(batchN(1))
	if len(p.released) != maxReleasedBatches {
		t.Fatalf("a repeated release took a second slot: %d releases", len(p.released))
	}
	if !refused(p, batchN(100)) {
		t.Fatal("the oldest other release was forgotten by a repeated release")
	}
	// The next new release forgets the oldest, which is batch 1.
	p.Release(batchN(5000))
	if refused(p, batchN(1)) {
		t.Fatal("the oldest release was not forgotten")
	}
	if !refused(p, batchN(5000)) || !refused(p, batchN(101)) {
		t.Fatal("a remembered release was forgotten")
	}
}
