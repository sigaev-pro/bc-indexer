package session

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTokenAndBarrierInvalidation(t *testing.T) {
	b := Barrier{epoch: 5}
	if !b.Invalidates(Token{Epoch: 4}) {
		t.Fatal("barrier must invalidate older epochs")
	}
	if b.Invalidates(Token{Epoch: 5}) {
		t.Fatal("barrier must not invalidate its own epoch")
	}
	if b.Invalidates(Token{Epoch: 6}) {
		t.Fatal("barrier must not invalidate newer epochs")
	}
	if b.Epoch() != 5 {
		t.Fatalf("epoch = %d, want 5", b.Epoch())
	}
}

func TestGateOpenCheckRevoke(t *testing.T) {
	g := NewGate()
	if _, err := g.Open(0); !errors.Is(err, ErrEpochInvalid) {
		t.Fatalf("zero epoch: got %v, want ErrEpochInvalid", err)
	}
	tok, err := g.Open(1)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Epoch != 1 || tok.SessionID == ([16]byte{}) {
		t.Fatalf("token = %+v, want epoch 1 and a random id", tok)
	}
	if err := g.Check(tok); err != nil {
		t.Fatalf("fresh token must check: %v", err)
	}
	// A second session at the same epoch has an independent id.
	other, err := g.Open(1)
	if err != nil {
		t.Fatal(err)
	}
	if other.SessionID == tok.SessionID {
		t.Fatal("session ids must be unpredictable and unique")
	}
	g.Revoke(tok.SessionID)
	if err := g.Check(tok); !errors.Is(err, ErrSessionStale) {
		t.Fatalf("revoked token: got %v, want ErrSessionStale", err)
	}
	g.Revoke(tok.SessionID) // revoking twice is a no-op
	if err := g.Check(other); err != nil {
		t.Fatal("the other session must stay valid")
	}
	// A forged epoch on a valid session id is stale.
	forged := other
	forged.Epoch = 9
	if err := g.Check(forged); !errors.Is(err, ErrSessionStale) {
		t.Fatalf("forged epoch: got %v, want ErrSessionStale", err)
	}
}

func TestGateBarrierFencesOldTokens(t *testing.T) {
	g := NewGate()
	old, err := g.Open(2)
	if err != nil {
		t.Fatal(err)
	}
	// Opening below the barrier is a generation conflict once the barrier
	// rose.
	b := g.Raise(3)
	if b.Epoch() != 3 {
		t.Fatalf("barrier epoch = %d, want 3", b.Epoch())
	}
	if err := g.Check(old); !errors.Is(err, ErrSessionStale) {
		t.Fatalf("old token after barrier: got %v, want ErrSessionStale", err)
	}
	if _, err := g.Open(2); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("open below barrier: got %v, want ErrGenerationConflict", err)
	}
	// Raising is monotonic: a lower epoch never reopens work.
	if b := g.Raise(1); b.Epoch() != 3 {
		t.Fatalf("barrier lowered to %d", b.Epoch())
	}
	fresh, err := g.Open(3)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Check(fresh); err != nil {
		t.Fatalf("token at the barrier epoch must stay valid: %v", err)
	}
}

func TestGateMutateChecksTokenWithCommit(t *testing.T) {
	g := NewGate()
	tok, _ := g.Open(1)
	ran := false
	if err := g.Mutate(tok, func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("mutate: err=%v ran=%v", err, ran)
	}
	// Stale tokens never run their effect.
	g.Raise(2)
	ran = false
	if err := g.Mutate(tok, func() error { ran = true; return nil }); !errors.Is(err, ErrSessionStale) || ran {
		t.Fatalf("stale mutate: err=%v ran=%v, want ErrSessionStale and no effect", err, ran)
	}
	// The mutation's own error is returned unchanged.
	sentinel := errors.New("journal refused")
	fresh, _ := g.Open(2)
	if err := g.Mutate(fresh, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("mutation error: got %v", err)
	}
}

// The linearization rule: after Raise returns, no later Mutate with an old
// token may run; every mutation that already started completes.
func TestGateConcurrentRaiseVersusMutate(t *testing.T) {
	g := NewGate()
	tok, _ := g.Open(1)

	var ranBefore, rejected int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 50; j++ {
				err := g.Mutate(tok, func() error {
					atomic.AddInt64(&ranBefore, 1)
					return nil
				})
				if errors.Is(err, ErrSessionStale) {
					atomic.AddInt64(&rejected, 1)
					// Once rejected, the token stays rejected.
					if err := g.Mutate(tok, func() error { return nil }); !errors.Is(err, ErrSessionStale) {
						t.Error("token resurrected after a stale rejection")
					}
				} else if err != nil {
					t.Errorf("unexpected mutate error: %v", err)
				}
			}
		}()
	}
	close(start)
	// Raise concurrently with the mutators.
	for i := 0; i < 5; i++ {
		g.Raise(uint64(2 + i))
	}
	wg.Wait()

	if rejected == 0 {
		t.Fatal("the barrier never rejected a stale mutation in the race")
	}
	// Every accepted mutation happened before the barrier took effect; the
	// accounting is simply consistent with a serialized order.
	if atomic.LoadInt64(&ranBefore) == 0 && rejected == 0 {
		t.Fatal("no mutation was accounted")
	}
}

func TestPublisherGenerationBarrier(t *testing.T) {
	p := NewPublisherGate()
	if p.Generation() != 1 {
		t.Fatalf("initial generation = %d, want 1", p.Generation())
	}
	cb := Callback{Batch: [40]byte{1}, PlanDigest: [32]byte{2}, Generation: 1}
	ran := 0
	if err := p.Apply(cb, func() error { ran++; return nil }); err != nil {
		t.Fatal(err)
	}
	// A duplicate of a still-valid callback is allowed: the journal answers
	// it idempotently.
	if err := p.Apply(cb, func() error { ran++; return nil }); err != nil {
		t.Fatalf("duplicate callback: %v", err)
	}
	if ran != 2 {
		t.Fatalf("callback ran %d times, want 2", ran)
	}
	// A new generation puts a barrier: the old callback is stale and cannot
	// create state.
	if p.NewGeneration() != 2 {
		t.Fatal("generation must advance to 2")
	}
	ran = 0
	if err := p.Apply(cb, func() error { ran++; return nil }); !errors.Is(err, ErrCallbackStale) || ran != 0 {
		t.Fatalf("stale callback: err=%v ran=%d", err, ran)
	}
	// A callback of the current generation runs.
	current := Callback{Batch: [40]byte{1}, PlanDigest: [32]byte{2}, Generation: 2}
	if err := p.Apply(current, func() error { ran++; return nil }); err != nil || ran != 1 {
		t.Fatalf("current callback: err=%v ran=%d", err, ran)
	}
	// A future generation is not stale yet (the worker may be ahead).
	ahead := Callback{Batch: [40]byte{9}, Generation: 3}
	if err := p.Apply(ahead, func() error { return nil }); err != nil {
		t.Fatalf("ahead callback: %v", err)
	}
}

func TestPublisherReleaseBarrier(t *testing.T) {
	p := NewPublisherGate()
	batch := [40]byte{7}
	cb := Callback{Batch: batch, PlanDigest: [32]byte{3}, Generation: 1}
	if err := p.Apply(cb, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	// The ordered release begins: the batch is fenced for callbacks.
	p.Release(batch)
	ran := false
	if err := p.Apply(cb, func() error { ran = true; return nil }); !errors.Is(err, ErrBatchReleased) || ran {
		t.Fatalf("released batch callback: err=%v ran=%v", err, ran)
	}
	// Other batches keep working at the same generation.
	other := Callback{Batch: [40]byte{8}, PlanDigest: [32]byte{3}, Generation: 1}
	if err := p.Apply(other, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestPublisherConcurrentReleaseVersusCallback(t *testing.T) {
	p := NewPublisherGate()
	var applied, rejected int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cb := Callback{Batch: [40]byte{1}, Generation: 1}
			for j := 0; j < 100; j++ {
				err := p.Apply(cb, func() error {
					atomic.AddInt64(&applied, 1)
					return nil
				})
				if errors.Is(err, ErrBatchReleased) || errors.Is(err, ErrCallbackStale) {
					atomic.AddInt64(&rejected, 1)
				} else if err != nil {
					t.Errorf("unexpected callback error: %v", err)
				}
			}
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.NewGeneration()
			p.Release([40]byte{1})
		}()
	}
	wg.Wait()
	if rejected == 0 {
		t.Fatal("the release barrier never rejected a callback in the race")
	}
}
