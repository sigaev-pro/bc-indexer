package poly

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// The record model reuses the closed protocol value types through aliases so
// the transaction model and the protocol models stay one type system:
// hashes, addresses and amounts are never redefined or converted.
type (
	// Hash32 is a 32-byte native hash (protocol/block).
	Hash32 = block.Hash32
	// Bloom is the 256-byte logs bloom (protocol/block).
	Bloom = block.Bloom
	// Address20 is a native account address (protocol/balance).
	Address20 = balance.Address20
	// UInt256 is an unsigned 256-bit integer (protocol/balance).
	UInt256 = balance.UInt256
)

// SystemCallerAddress is the caller Bor uses for native system execution:
// types.SystemAddress, 0xfffffffffffffffffffffffffffffffffffffffe. A body
// StateSync transaction is executed from this address; the address proves
// the native system path, not a transaction signature, and no sender is
// fabricated for the normalized record.
var SystemCallerAddress = mustAddress("0xfffffffffffffffffffffffffffffffffffffffe")

// StateReceiverAddress is the genesis StateReceiver contract
// (0x0000000000000000000000000000000000001001 in the pinned Bor genesis and
// on Polygon Mainnet). The synthetic root frame of a body StateSync
// transaction targets it.
var StateReceiverAddress = mustAddress("0x0000000000000000000000000000000000001001")

// BurnAddress is the Polygon base-fee burn recipient, the zero address. The
// burn is a family-defined FEE_RECIPIENT_CREDIT recipient (CURRENT_SEMANTICS
// D62), not a mint or a missing recipient.
var BurnAddress Address20

// StateSyncTxType is the Bor body StateSync transaction type byte
// (types.StateSyncTxType).
const StateSyncTxType uint8 = 0x7f

// MadhugiriMainnetBlock is the pinned Polygon Mainnet Madhugiri height. The
// gate is a chain-config fact, not a date; profiles pass their configured
// height and this constant is provenance for the mainnet profile.
const MadhugiriMainnetBlock uint64 = 80084800

// StateSyncEventLogTopic is the StateSync log topic of the pinned genesis
// receiver (keccak of the event signature). It is evidence provenance from
// the E01 corpus; the normalizer does not require a StateSync log.
var StateSyncEventLogTopic = mustHash("000000000000000000000000000000000000000000000000000000000000005e")

func mustAddress(hex string) Address20 {
	var out Address20
	if len(hex) != 42 || hex[:2] != "0x" {
		panic(fmt.Sprintf("poly: invalid address literal %q", hex))
	}
	for i := 0; i < 20; i++ {
		hi, ok1 := hexNibble(hex[2+2*i])
		lo, ok2 := hexNibble(hex[2+2*i+1])
		if !ok1 || !ok2 {
			panic(fmt.Sprintf("poly: invalid address literal %q", hex))
		}
		out[i] = hi<<4 | lo
	}
	return out
}

func mustHash(hex string) Hash32 {
	if len(hex) == 66 && hex[:2] == "0x" {
		hex = hex[2:]
	}
	var out Hash32
	if len(hex) != 64 {
		panic(fmt.Sprintf("poly: invalid hash literal %q", hex))
	}
	for i := 0; i < 32; i++ {
		hi, ok1 := hexNibble(hex[2*i])
		lo, ok2 := hexNibble(hex[2*i+1])
		if !ok1 || !ok2 {
			panic(fmt.Sprintf("poly: invalid hash literal %q", hex))
		}
		out[i] = hi<<4 | lo
	}
	return out
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	default:
		return 0, false
	}
}

func addrPtr(a Address20) *Address20 { return &a }

func hashPtr(h Hash32) *Hash32 { return &h }

func u64Ptr(v uint64) *uint64 { return &v }

// engineType aliases the closed engine registry of protocol/balance for the
// frame model.
type engineType = balance.Engine

const (
	engineEVM            = balance.EngineEVM
	engineNativeProtocol = balance.EngineNativeProtocol
)
