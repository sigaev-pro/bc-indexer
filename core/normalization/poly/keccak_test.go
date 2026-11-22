package poly

import "testing"

// The expected digests come from the independent E01 fixture codec
// (test/type_paths/poly/poly_codec.py), executed separately; this file
// never reads that code.

func TestKeccak256Vectors(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"empty", []byte{}, "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"},
		{"abc", []byte("abc"), "4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45"},
	}
	for _, c := range cases {
		got := Keccak256(c.data)
		if got.String() != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestKeccak256MultiPartMatchesConcat(t *testing.T) {
	a := []byte("matic-bor-receipt-")
	b := []byte{42, 0, 0, 0, 0, 0, 0, 0}
	c := make([]byte, 32)
	for i := range c {
		c[i] = byte(i)
	}
	joined := Keccak256(append(append(append([]byte{}, a...), b...), c...))
	split := Keccak256(a, b, c)
	if joined != split {
		t.Fatalf("multipart digest differs: %s vs %s", joined, split)
	}
}

func TestDeriveBorReceiptHash(t *testing.T) {
	// Independent vector generated with the E01 fixture codec:
	// keccak256("matic-bor-receipt-" || u64be(42) || 0x00..1f).
	var blockHash Hash32
	for i := range blockHash {
		blockHash[i] = byte(i)
	}
	got := DeriveBorReceiptHash(42, blockHash)
	const want = "a6a0fe9e1604481727a8c6293361ece9e4a9ffdc1135d148e21e11dc9510e11b"
	if got.String() != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
