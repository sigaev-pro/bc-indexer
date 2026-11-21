// Fixed-size identities of the v2 wire contract: Id16 identities, Hash32
// digests, the 40-byte event id and the frame NetworkIdentity with its
// network digest formula (CONTROL_WIRE_CONTRACT, "Base encodings and
// identities" and "DestinationBinding file").
package proto

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// ID16 is a 16-byte identity (producer, run, publication epoch). The zero
// value is unset and invalid.
type ID16 [16]byte

// IsZero reports whether id is the unset zero identity.
func (id ID16) IsZero() bool { return id == ID16{} }

// Hash32 is a 32-byte SHA-256 digest.
type Hash32 [32]byte

// EventID is the 40-byte public event identity
// producer_id16 || run_id16 || event_sequence_u64be. Different lifecycle
// events of one entity have different event ids; retries keep them.
type EventID [40]byte

// NewEventID builds the event id from its parts. The sequence is an unsigned
// counter in big-endian order.
func NewEventID(producer, run ID16, sequence uint64) EventID {
	var id EventID
	copy(id[:16], producer[:])
	copy(id[16:32], run[:])
	binary.BigEndian.PutUint64(id[32:40], sequence)
	return id
}

// ProducerID returns the producer identity half of the event id.
func (id EventID) ProducerID() ID16 {
	var out ID16
	copy(out[:], id[:16])
	return out
}

// RunID returns the run identity half of the event id.
func (id EventID) RunID() ID16 {
	var out ID16
	copy(out[:], id[16:32])
	return out
}

// Sequence returns the event sequence of the event id.
func (id EventID) Sequence() uint64 { return binary.BigEndian.Uint64(id[32:40]) }

// Validate rejects an event id without assigned producer or run identities.
func (id EventID) Validate() error {
	if id.ProducerID().IsZero() {
		return fmt.Errorf("%w: event id producer is zero", ErrInvalidValue)
	}
	if id.RunID().IsZero() {
		return fmt.Errorf("%w: event id run is zero", ErrInvalidValue)
	}
	return nil
}

// NetworkFamily is the network family enum of the frame NetworkIdentity.
// EVM=1 is the only registered value of the current contract.
type NetworkFamily uint32

// NetworkFamilyEVM encodes ETH, BSC and Polygon: one EVM execution family,
// distinct chain identities.
const NetworkFamilyEVM NetworkFamily = 1

// String returns the registry name of f.
func (f NetworkFamily) String() string {
	if f == NetworkFamilyEVM {
		return "EVM"
	}
	return fmt.Sprintf("NetworkFamily(%d)", uint32(f))
}

// NetworkIdentity is the frame network message: family enum, chain id and
// genesis hash (fields 1..3 of the pinned table).
type NetworkIdentity struct {
	Family      NetworkFamily
	ChainID     [32]byte
	GenesisHash [32]byte
}

// Validate rejects an unknown family enum value.
func (n NetworkIdentity) Validate() error {
	if n.Family != NetworkFamilyEVM {
		return fmt.Errorf("%w: network family %d is not EVM=1", ErrClosedRegistry, uint32(n.Family))
	}
	return nil
}

// networkDigestDomain is the digest domain of the network identity.
const networkDigestDomain = "extractor.network.v1"

// Digest returns the network digest of the binding contract:
//
//	SHA256(UTF8("extractor.network.v1") || 0x00 || 0x01 || chain_id32 || genesis32)
//
// where byte 0x01 is the EVM family encoding. The digest qualifies the
// Nats-Msg-Id tuple and the stream metadata check of D77.
func (n NetworkIdentity) Digest() (Hash32, error) {
	if err := n.Validate(); err != nil {
		return Hash32{}, err
	}
	var out Hash32
	h := sha256.New()
	h.Write([]byte(networkDigestDomain))
	h.Write([]byte{0})
	h.Write([]byte{byte(NetworkFamilyEVM)})
	h.Write(n.ChainID[:])
	h.Write(n.GenesisHash[:])
	h.Sum(out[:0])
	return out, nil
}

// u64be appends v as 8 big-endian bytes.
func u64be(b []byte, v uint64) []byte {
	return binary.BigEndian.AppendUint64(b, v)
}

// u16be appends v as 2 big-endian bytes.
func u16be(b []byte, v uint16) []byte {
	return binary.BigEndian.AppendUint16(b, v)
}

// addUint64 adds two unsigned counters and rejects overflow.
func addUint64(a, b uint64) (uint64, error) {
	sum := a + b
	if sum < a {
		return 0, fmt.Errorf("%w: %d + %d", ErrOverflow, a, b)
	}
	return sum, nil
}

// mulUint64 multiplies two unsigned values and rejects overflow.
func mulUint64(a, b uint64) (uint64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	product := a * b
	if product/b != a {
		return 0, fmt.Errorf("%w: %d * %d", ErrOverflow, a, b)
	}
	return product, nil
}
