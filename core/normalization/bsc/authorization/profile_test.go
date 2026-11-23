package authorization

import (
	"errors"
	"testing"
)

func TestCurrentProfileIsClosedAndEmpty(t *testing.T) {
	p := Current()
	if p.Active() {
		t.Fatal("the current BSC authorization profile must be inactive")
	}
	kept, err := p.Accept(nil)
	if err != nil {
		t.Fatalf("empty tuple list rejected: %v", err)
	}
	if len(kept) != 0 {
		t.Fatalf("empty tuple list produced %d tuples", len(kept))
	}
}

func TestNonEmptyTupleListIsExplicitError(t *testing.T) {
	p := Current()
	tuples := []Tuple{{Nonce: 1}, {Nonce: 1}} // a duplicate stays source data
	kept, err := p.Accept(tuples)
	if !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("non-empty tuple list error = %v, want ErrNotApplicable", err)
	}
	if kept != nil {
		t.Fatalf("rejected tuples must not be returned, got %d", len(kept))
	}
}

func TestTupleKeepsSourceShape(t *testing.T) {
	// The tuple type is the future registry shape: signature fields that a
	// verifier would reject (parity 7, zero scalars) stay representable,
	// because the contract stores the source tuple, not a recovery result.
	tuple := Tuple{Parity: 7}
	if tuple.Nonce != 0 || tuple.Address != [20]byte{} || tuple.R != [32]byte{} {
		t.Fatal("zero tuple fields drifted")
	}
}
