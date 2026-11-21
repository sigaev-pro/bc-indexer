// Shared fixtures of the schema-layer tests. The expected bytes and digests
// embedded in the test files come from an independent Python implementation
// of the same normative tables (hashlib and base64 only), following the
// discipline of the sibling phase-1 packages: neither implementation reads
// the other's code.
package proto

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// golden identities (hex from the Python generator).
var (
	goldenProducerHex = "00112233445566778899aabbccddeeff"
	goldenRunHex      = "0f0e0d0c0b0a09080706050403020100"
	goldenEpochHex    = "deadbeefcafebabe0123456789abcdef"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex fixture: %v", err)
	}
	return b
}

func goldenProducer(t *testing.T) ID16 {
	t.Helper()
	var id ID16
	copy(id[:], mustHex(t, goldenProducerHex))
	return id
}

func goldenRun(t *testing.T) ID16 {
	t.Helper()
	var id ID16
	copy(id[:], mustHex(t, goldenRunHex))
	return id
}

func goldenEpoch(t *testing.T) ID16 {
	t.Helper()
	var id ID16
	copy(id[:], mustHex(t, goldenEpochHex))
	return id
}

func goldenEventID(t *testing.T) EventID {
	t.Helper()
	return NewEventID(goldenProducer(t), goldenRun(t), 7)
}

func goldenNetwork(t *testing.T) NetworkIdentity {
	t.Helper()
	var net NetworkIdentity
	net.Family = NetworkFamilyEVM
	chain := sha256.Sum256([]byte("proto-golden-chain-id"))
	genesis := sha256.Sum256([]byte("proto-golden-genesis"))
	copy(net.ChainID[:], chain[:])
	copy(net.GenesisHash[:], genesis[:])
	return net
}

func digest32(t *testing.T, seed string) Hash32 {
	t.Helper()
	var out Hash32
	sum := sha256.Sum256([]byte(seed))
	copy(out[:], sum[:])
	return out
}

func dataPattern(t *testing.T, alphabet string, n int) []byte {
	t.Helper()
	out := make([]byte, n)
	for i := range out {
		out[i] = alphabet[i%len(alphabet)]
	}
	return out
}

func counterBytes(seed byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i)%251 + seed
	}
	return out
}

// modBytes mirrors the Python generator: byte(i % mod) for i in 0..n-1.
func modBytes(mod byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i) % mod
	}
	return out
}

// goldenCompleteFrame is the golden COMPLETE frame of the Python generator:
// chain sequence 424242, fragment 1 of 3, 100 data bytes.
func goldenCompleteFrame(t *testing.T) *TransportFrame {
	t.Helper()
	return &TransportFrame{
		TransportVersion:    TransportVersion,
		Network:             goldenNetwork(t),
		EventID:             goldenEventID(t),
		RecordDigest:        digest32(t, "proto-golden-record"),
		PublicationEpoch:    goldenEpoch(t),
		ChainSequence:       424242,
		Complete:            &CompleteFragment{FragmentBytes: dataPattern(t, "0123456789abcdef", 100)},
		FragmentIndex:       1,
		FragmentCount:       3,
		FragmentDigest:      digest32(t, "proto-golden-fragment"),
		RecordFormatVersion: RecordFormatVersion,
	}
}

// goldenPartFrame is the golden PART frame: part 0 of 37 of a 4096-byte
// fragment, 100 data bytes.
func goldenPartFrame(t *testing.T) *TransportFrame {
	t.Helper()
	return &TransportFrame{
		TransportVersion: TransportVersion,
		Network:          goldenNetwork(t),
		EventID:          goldenEventID(t),
		RecordDigest:     digest32(t, "proto-golden-record"),
		PublicationEpoch: goldenEpoch(t),
		ChainSequence:    424242,
		Part: &FragmentPart{PartIndex: 0, PartCount: 37, FragmentTotalBytes: 4096,
			Data: dataPattern(t, "fedcba9876543210", 100)},
		FragmentIndex:       1,
		FragmentCount:       3,
		FragmentDigest:      digest32(t, "proto-golden-fragment"),
		RecordFormatVersion: RecordFormatVersion,
	}
}

// goldenFragments are the golden record fragments of the Python generator:
// a header fragment, one items fragment and the seal (bytes i%7).
func goldenFragments(t *testing.T) [][]byte {
	t.Helper()
	return [][]byte{
		[]byte("HEADER-FRAGMENT"),
		counterBytes(0, 200),
		modBytes(7, 12),
	}
}

// goldenSemanticNetwork is the semantic.Network matching goldenNetwork.
func goldenSemanticNetwork(t *testing.T) semantic.Network {
	t.Helper()
	net := goldenNetwork(t)
	var out semantic.Network
	copy(out.ChainID[:], net.ChainID[:])
	copy(out.GenesisHash[:], net.GenesisHash[:])
	return out
}

func goldenSource(t *testing.T) semantic.SourceRef {
	t.Helper()
	return semantic.SourceRef{
		NodeInstanceID: semantic.ID16(goldenProducer(t)),
		WriterEpoch:    semantic.ID16(goldenRun(t)),
		SourceSequence: 1,
	}
}

// envelopeFixture builds a valid envelope of the given family and kind with
// synthetic payload fragments. The full payload types of every family are
// E04's scope; three non-empty fragments are the minimal record shape.
func envelopeFixture(t *testing.T, family Family, kind Kind) *Envelope {
	t.Helper()
	producer := goldenProducer(t)
	run := goldenRun(t)
	blockHash := semantic.Hash32(digest32(t, "proto-envelope-block"))
	net := goldenSemanticNetwork(t)
	source := goldenSource(t)
	var descriptor semantic.SemanticDescriptor
	var err error
	revision := uint64(0)
	if kind != KindApplied {
		revision = 1 // Removed and StatusChanged carry a positive revision
	}
	switch family {
	case FamilyTx:
		txHash := semantic.Hash32(digest32(t, "proto-envelope-tx"))
		descriptor, err = semantic.NewTransactionDescriptor(semantic.Kind(kind), net, semantic.ID16(producer), semantic.ID16(run), 9,
			blockHash, txHash, 4, revision, source)
	case FamilyBlock:
		descriptor, err = semantic.NewBlockDescriptor(semantic.Kind(kind), net, semantic.ID16(producer), semantic.ID16(run), 9,
			blockHash, revision, source)
	default:
		descriptor, err = semantic.NewOptionalDescriptor(family2semantic(t, family), semantic.Kind(kind), net,
			semantic.ID16(producer), semantic.ID16(run), 9, blockHash, 5, 0, revision, source)
	}
	if err != nil {
		t.Fatalf("semantic descriptor fixture: %v", err)
	}
	variant, err := variantFor(family, kind)
	if err != nil {
		t.Fatalf("variant fixture: %v", err)
	}
	return &Envelope{
		EventID:  NewEventID(producer, run, 7),
		Profile:  profileForFamily(t, family),
		Semantic: descriptor,
		Payload:  Payload{Variant: variant, Fragments: syntheticFragments(t, family, kind)},
	}
}

func family2semantic(t *testing.T, f Family) semantic.Family {
	t.Helper()
	switch f {
	case FamilyBalanceSystem:
		return semantic.FamilyBalanceSystem
	case FamilyBorLegacy:
		return semantic.FamilyBorLegacy
	}
	t.Fatalf("not an optional family: %s", f)
	return 0
}

// profileForFamily picks a network profile whose closed variant set admits
// the family: balance.system needs BSC or Bor, bor.legacy_receipt needs Bor.
func profileForFamily(t *testing.T, f Family) profiles.Family {
	t.Helper()
	switch f {
	case FamilyBorLegacy:
		return profiles.FamilyBor
	case FamilyBalanceSystem:
		return profiles.FamilyBsc
	}
	return profiles.FamilyEth
}

func syntheticFragments(t *testing.T, family Family, kind Kind) [][]byte {
	t.Helper()
	seed := byte(uint16(family)*16 + uint16(kind))
	return [][]byte{counterBytes(seed, 48), counterBytes(seed+1, 120), counterBytes(seed+2, 20)}
}

// blockRecordFixture builds the minimal valid phase-1 BlockRecord of the
// golden block-family Applied envelope: matching activation identity, an
// ETH header with source_rlp and ETH finality markers.
func blockRecordFixture(t *testing.T, env *Envelope) *block.BlockRecord {
	t.Helper()
	blockHash := block.Hash32(env.Semantic.Activation.BlockHash)
	parent := block.Hash32(digest32(t, "proto-envelope-parent"))
	producer := block.Id16(env.Semantic.Activation.ProducerID)
	run := block.Id16(env.Semantic.Activation.RunID)
	var chain block.UInt256
	var genesis block.Hash32
	copy(chain[:], env.Semantic.Network.ChainID[:])
	copy(genesis[:], env.Semantic.Network.GenesisHash[:])
	instance := block.Id16(env.Semantic.Source.NodeInstanceID)
	return &block.BlockRecord{
		Activation: block.BlockActivationRef{
			Network:      block.NetworkIdentity{Family: "EVM", ChainID: chain, GenesisHash: genesis},
			BlockHash:    blockHash,
			ProducerID:   producer,
			RunID:        run,
			ActivationID: block.ActivationID{DecisionSequence: env.Semantic.Activation.ActivationID.DecisionSequence},
		},
		Admission: block.ObservationRef{ProducerID: producer, RunID: run, ObservationSequence: 1},
		Metadata: block.BlockMetadata{
			Number:     5,
			Hash:       blockHash,
			ParentHash: parent,
			Timestamp:  block.TimestampFromSeconds(1700000000),
			Header:     block.Header{ParentHash: parent, Number: 5, SourceRLP: []byte{1, 2, 3}},
			Details:    block.FamilyDetails{Eth: &block.EthBlockDetails{}},
		},
		InitialSnapshot: block.LifecycleSnapshot{
			NativeDecision:        block.NativeDecisionRef{NodeInstanceID: instance, ProducerID: producer, RunID: run, DecisionSequence: env.Semantic.Activation.ActivationID.DecisionSequence},
			ObservedExecutionHead: block.AnchorRef{Height: 5, Hash: parent},
			Markers: block.FinalityMarkers{Eth: &block.EthereumMarkers{
				Safe: block.NotObserved(), Finalized: block.NotObserved(), SafeBasis: block.SafeBasisUnset}},
		},
	}
}

// cut removes the first occurrence of marker from b.
func cut(t *testing.T, b []byte, markerHex string) []byte {
	t.Helper()
	marker := mustHex(t, markerHex)
	at := bytes.Index(b, marker)
	if at < 0 {
		t.Fatalf("marker %s not found", markerHex)
	}
	out := make([]byte, 0, len(b)-len(marker))
	out = append(out, b[:at]...)
	return append(out, b[at+len(marker):]...)
}

// splice replaces the first occurrence of marker with replacement.
func splice(t *testing.T, b []byte, markerHex, replacementHex string) []byte {
	t.Helper()
	marker := mustHex(t, markerHex)
	at := bytes.Index(b, marker)
	if at < 0 {
		t.Fatalf("marker %s not found", markerHex)
	}
	replacement := mustHex(t, replacementHex)
	out := make([]byte, 0, len(b)-len(marker)+len(replacement))
	out = append(out, b[:at]...)
	out = append(out, replacement...)
	return append(out, b[at+len(marker):]...)
}

// insert puts addition after the first occurrence of marker.
func insert(t *testing.T, b []byte, markerHex, additionHex string) []byte {
	t.Helper()
	marker := mustHex(t, markerHex)
	at := bytes.Index(b, marker)
	if at < 0 {
		t.Fatalf("marker %s not found", markerHex)
	}
	return insertAt(t, b, at+len(marker), additionHex)
}

// insertBefore puts addition right before the first occurrence of marker.
func insertBefore(t *testing.T, b []byte, markerHex, additionHex string) []byte {
	t.Helper()
	marker := mustHex(t, markerHex)
	at := bytes.Index(b, marker)
	if at < 0 {
		t.Fatalf("marker %s not found", markerHex)
	}
	return insertAt(t, b, at, additionHex)
}

func insertAt(t *testing.T, b []byte, at int, additionHex string) []byte {
	t.Helper()
	addition := mustHex(t, additionHex)
	out := make([]byte, 0, len(b)+len(addition))
	out = append(out, b[:at]...)
	out = append(out, addition...)
	return append(out, b[at:]...)
}
