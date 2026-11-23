package finalityobserver

import (
	"crypto/sha256"
	"encoding/binary"
	"io"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// Canonical digest rules of this package, following the lifecycle digest
// convention: SHA-256 of the domain tag, one zero byte, then the fields; a
// string is its 8-byte big-endian length and its bytes, a number is 8 bytes
// big-endian, a hash is its 32 raw bytes, an anchor is its height then its
// hash. Every digest is computed over exact observed bytes or over the
// tagged fields listed below; none of them is a block hash.

const (
	tagParams    = "extractor-polygon-params-v1"
	tagWhitelist = "extractor-polygon-whitelist-v1"
	tagBorRange  = "extractor-polygon-bor-range-v1"
)

// DigestBytes is the SHA-256 of exact bytes: the response digest of one
// source observation and the calldata/result digests of the L1 read.
func DigestBytes(b []byte) block.Hash32 {
	return block.Hash32(sha256.Sum256(b))
}

// ParamsDigest binds the checked RootChain parameters of one L1
// verification: the RootChain contract address and the child chain block
// interval, read before and re-read after the L1 read.
func ParamsDigest(rootChainAddress block.Address20, childBlockInterval uint64) block.Hash32 {
	h := sha256.New()
	h.Write([]byte(tagParams))
	h.Write([]byte{0})
	h.Write(rootChainAddress[:])
	var interval [8]byte
	binary.BigEndian.PutUint64(interval[:], childBlockInterval)
	h.Write(interval[:])
	return block.Hash32(h.Sum(nil))
}

// WhitelistAncestryDigest binds one accepted whitelist to its kind, anchor
// and native decision. A whitelist is accepted only after the canonical
// match; the digest records exactly that binding.
func WhitelistAncestryDigest(kind WhitelistKind, anchor block.AnchorRef, decision block.NativeDecisionRef) block.Hash32 {
	h := sha256.New()
	h.Write([]byte(tagWhitelist))
	h.Write([]byte{0})
	var k [8]byte
	binary.BigEndian.PutUint64(k[:], uint64(kind))
	h.Write(k[:])
	writeAnchor(h, anchor)
	writeID16(h, decision.NodeInstanceID)
	writeID16(h, decision.ProducerID)
	writeID16(h, decision.RunID)
	var seq [8]byte
	binary.BigEndian.PutUint64(seq[:], decision.DecisionSequence)
	h.Write(seq[:])
	return block.Hash32(h.Sum(nil))
}

// BorRangeDigest binds the Bor range of one L1 checkpoint evidence: the
// start and end heights and the verified end anchor of the continuous
// canonical range.
func BorRangeDigest(start, end uint64, endRef block.AnchorRef) block.Hash32 {
	h := sha256.New()
	h.Write([]byte(tagBorRange))
	h.Write([]byte{0})
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], start)
	h.Write(b[:])
	binary.BigEndian.PutUint64(b[:], end)
	h.Write(b[:])
	writeAnchor(h, endRef)
	return block.Hash32(h.Sum(nil))
}

func writeAnchor(h io.Writer, a block.AnchorRef) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], a.Height)
	h.Write(b[:])
	h.Write(a.Hash[:])
}

func writeID16(h io.Writer, id block.Id16) {
	h.Write(id[:])
}
