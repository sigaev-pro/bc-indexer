package poly

// Keccak-256 (the pre-FIPS-256 Keccak with the 0x01 domain padding, not
// SHA3-256). The normalizer needs exactly two native hash derivations from
// the pinned Bor source: the legacy Bor receipt identity
// (types.DeriveBorReceiptHash: keccak256("matic-bor-receipt-" ||
// u64be(block_number) || block_hash)) and the transaction-hash checks of the
// StateSync wire. The Go standard library ships FIPS-202 SHA3 only, so this
// file carries the compact reference permutation instead of a dependency.
// Test vectors come from the independent E01 fixture codec
// (test/type_paths/poly/poly_codec.py), which this file never reads.

const keccakRate = 136 // 1088-bit rate of Keccak-256

var keccakRoundConstants = [24]uint64{
	0x0000000000000001, 0x0000000000008082, 0x800000000000808a,
	0x8000000080008000, 0x000000000000808b, 0x0000000080000001,
	0x8000000080008081, 0x8000000000008009, 0x000000000000008a,
	0x0000000000000088, 0x0000000080008009, 0x000000008000000a,
	0x000000008000808b, 0x800000000000008b, 0x8000000000008089,
	0x8000000000008003, 0x8000000000008002, 0x8000000000000080,
	0x000000000000800a, 0x800000008000000a, 0x8000000080008081,
	0x8000000000008080, 0x0000000080000001, 0x8000000080008008,
}

var keccakRotc = [24]uint{1, 3, 6, 10, 15, 21, 28, 36, 45, 55, 2, 14, 27, 41, 56, 8, 25, 43, 62, 18, 39, 61, 20, 44}

var keccakPiln = [24]int{10, 7, 11, 17, 18, 3, 5, 16, 8, 21, 24, 4, 15, 23, 19, 13, 12, 2, 20, 14, 22, 9, 6, 1}

func rotl64(x uint64, n uint) uint64 { return x<<n | x>>(64-n) }

func keccakF(a *[25]uint64) {
	var bc [5]uint64
	for round := 0; round < 24; round++ {
		// Theta.
		for i := 0; i < 5; i++ {
			bc[i] = a[i] ^ a[i+5] ^ a[i+10] ^ a[i+15] ^ a[i+20]
		}
		for i := 0; i < 5; i++ {
			t := bc[(i+4)%5] ^ rotl64(bc[(i+1)%5], 1)
			for j := 0; j < 25; j += 5 {
				a[j+i] ^= t
			}
		}
		// Rho and Pi.
		t := a[1]
		for i := 0; i < 24; i++ {
			j := keccakPiln[i]
			bc[0] = a[j]
			a[j] = rotl64(t, keccakRotc[i])
			t = bc[0]
		}
		// Chi.
		for j := 0; j < 25; j += 5 {
			for i := 0; i < 5; i++ {
				bc[i] = a[j+i]
			}
			for i := 0; i < 5; i++ {
				a[j+i] ^= (^bc[(i+1)%5]) & bc[(i+2)%5]
			}
		}
		// Iota.
		a[0] ^= keccakRoundConstants[round]
	}
}

// Keccak256 hashes the concatenation of the inputs with Keccak-256.
func Keccak256(parts ...[]byte) Hash32 {
	var st [25]uint64
	var buf [keccakRate]byte
	bufLen := 0
	absorb := func(block []byte) {
		for i := 0; i < keccakRate; i += 8 {
			var lane uint64
			for j := 0; j < 8; j++ {
				lane |= uint64(block[i+j]) << (8 * uint(j))
			}
			st[i/8] ^= lane
		}
		keccakF(&st)
	}
	for _, part := range parts {
		for len(part) > 0 {
			n := copy(buf[bufLen:], part)
			bufLen += n
			part = part[n:]
			if bufLen == keccakRate {
				absorb(buf[:])
				bufLen = 0
			}
		}
	}
	// Legacy Keccak padding: 0x01, zero fill, 0x80 on the final byte.
	for i := bufLen; i < keccakRate; i++ {
		buf[i] = 0
	}
	buf[bufLen] = 0x01
	buf[keccakRate-1] |= 0x80
	absorb(buf[:])

	var out Hash32
	for i := 0; i < 4; i++ {
		for j := 0; j < 8; j++ {
			out[i*8+j] = byte(st[i] >> (8 * uint(j)))
		}
	}
	return out
}

// u64be returns the 8-byte big-endian encoding of v.
func u64be(v uint64) [8]byte {
	var out [8]byte
	for i := 0; i < 8; i++ {
		out[7-i] = byte(v >> (8 * uint(i)))
	}
	return out
}

// DeriveBorReceiptHash computes the native legacy Bor receipt identity of
// types.DeriveBorReceiptHash in the pinned Bor source:
// keccak256("matic-bor-receipt-" || uint64BE(block_number) || block_hash).
func DeriveBorReceiptHash(blockNumber uint64, blockHash Hash32) Hash32 {
	number := u64be(blockNumber)
	return Keccak256([]byte("matic-bor-receipt-"), number[:], blockHash[:])
}
