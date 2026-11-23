package eth

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// Test helpers for fixture literals. fixtureCodeHash is a stand-in hash
// for fixture code identity only: the native adapter owns the real code
// hash of the pinned client; no value here claims to be native keccak
// output.

func uint64Ptr(v uint64) *uint64 { return &v }

func addrPtr(a Address20) *Address20 { return &a }

// patternHash builds a distinct fixture hash from a seed byte.
func patternHash(seed byte) Hash32 {
	var h Hash32
	h[0] = seed
	for i := 1; i < 32; i++ {
		h[i] = seed + byte(i)
	}
	return h
}

// patternID16 builds a distinct 16-byte fixture identity.
func patternID16(seed byte) (out [16]byte) {
	out[0] = seed
	for i := 1; i < 16; i++ {
		out[i] = seed + byte(i)
	}
	return out
}

// blockDecimal renders a block-model UInt256 in decimal.
func blockDecimal(v block.UInt256) string { return UInt256(v).Decimal() }

// hashFromHex parses a full 32-byte hex literal.
func hashFromHex(s string) Hash32 {
	h, err := parseHash(s)
	if err != nil {
		panic(err)
	}
	return h
}

// fixtureCodeHash hashes fixture code bytes for change detection.
func fixtureCodeHash(code []byte) Hash32 {
	sum := sha256.Sum256(code)
	var out Hash32
	copy(out[:], sum[:])
	return out
}

// codeHashOf is the hex form of fixtureCodeHash.
func codeHashOf(codeHex string) Hash32 { return fixtureCodeHash(decodeHexOrEmpty(codeHex)) }

// decodeHexOrEmpty decodes fixture hex; an empty string is an empty code.
func decodeHexOrEmpty(s string) []byte {
	if s == "" {
		return []byte{}
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// slotHash builds the fixture storage key of slot n (raw big-endian).
func slotHash(n byte) Hash32 {
	var h Hash32
	h[31] = n
	return h
}

// valueHash builds the fixture storage value n (raw big-endian).
func valueHash(n byte) Hash32 {
	var h Hash32
	h[31] = n
	return h
}

// value32 is a 32-byte fixture value as a slice.
func value32(n byte) []byte {
	h := valueHash(n)
	return h[:]
}
