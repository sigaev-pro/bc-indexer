// Canonical encodings and deterministic digests of the semantic identity.
// Every digest is SHA-256 over a domain tag, one zero byte and the fields
// in a fixed order, with unsigned big-endian integers and raw identity
// bytes; the formulas are normative in README.md.
package semantic

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// tagDescriptor and tagAllocation are the digest domain tags. Each is
// followed by one zero byte.
const (
	tagDescriptor = "extractor.semantic.v1"
	tagAllocation = "extractor.allocation.v1"
)

func u16(value uint16) []byte {
	var out [2]byte
	binary.BigEndian.PutUint16(out[:], value)
	return out[:]
}

func u64(value uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, value)
}

func hexString(b []byte) string { return hex.EncodeToString(b) }

// digestOf joins the parts and returns their SHA-256.
func digestOf(parts ...[]byte) Hash32 {
	var out Hash32
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	h.Sum(out[:0:32])
	return out
}

// Digest returns the deterministic digest of the descriptor: SHA-256 over
//
//	UTF8("extractor.semantic.v1") || 0x00 ||
//	u16be(family) || u16be(kind) ||
//	0x01 || chain_id32 || genesis32 ||
//	producer16 || run16 ||
//	u64be(decision_sequence) || u64be(entity_ordinal) || block32 ||
//	entity || [u64be(native_position) || tx32] ||
//	u64be(revision) ||
//	node_instance16 || writer_epoch16 || u64be(source_sequence)
//
// where entity is 0 for the block entity, 1 for a transaction entity
// (followed by its inclusion binding) and 2 for an optional entity. The
// digest covers only semantic identity; delivery attempts and public event
// IDs are absent, so it is stable across retries.
func (d SemanticDescriptor) Digest() (Hash32, error) {
	if err := d.Validate(); err != nil {
		return Hash32{}, err
	}
	parts := [][]byte{[]byte(tagDescriptor), {0}, u16(uint16(d.Family)), u16(uint16(d.Kind)),
		{NetworkFamilyEVM}, d.Network.ChainID[:], d.Network.GenesisHash[:],
		d.Activation.ProducerID[:], d.Activation.RunID[:],
		u64(d.Activation.ActivationID.DecisionSequence), u64(d.Activation.ActivationID.EntityOrdinal),
		d.Activation.BlockHash[:], {entityOf(d)}}
	if d.Activation.Transaction != nil {
		parts = append(parts, u64(d.Activation.Transaction.NativePosition),
			d.Activation.Transaction.TransactionHash[:])
	}
	parts = append(parts, u64(d.Revision),
		d.Source.NodeInstanceID[:], d.Source.WriterEpoch[:], u64(d.Source.SourceSequence))
	return digestOf(parts...), nil
}
