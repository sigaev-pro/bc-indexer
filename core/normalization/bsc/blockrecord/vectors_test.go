package blockrecord

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	bscblock "github.com/sigaev-pro/bc-indexer/core/normalization/bsc/block"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// vectorsPath is the golden vectors of the package, relative to it.
const vectorsPath = "../../../../test/harness/fixtures/blockrecord/expected.json"

// vectorsFile is expected.json: one fixed Input and what the independent
// generator computes from it.
type vectorsFile struct {
	SchemaVersion uint64 `json:"schema_version"`
	Description   string `json:"description"`
	Fixture       struct {
		Endpoint      string `json:"endpoint"`
		AcquiredAt    string `json:"acquired_at"`
		BlockNumber   uint64 `json:"block_number"`
		BlockHash     string `json:"block_hash"`
		GenesisHash   string `json:"genesis_hash"`
		HeaderRLPHash string `json:"header_rlp_sha256"`
	} `json:"fixture"`
	Registry struct {
		SchemaVersion uint32 `json:"schema_version"`
		Messages      map[string]struct {
			Name   string            `json:"name"`
			Fields []json.RawMessage `json:"fields"`
		} `json:"messages"`
		Enums   map[string]map[string]json.Number `json:"enums"`
		Profile map[string]string                 `json:"profile"`
	} `json:"registry"`
	Input struct {
		Network struct {
			ChainID     string `json:"chain_id"`
			GenesisHash string `json:"genesis_hash"`
		} `json:"network"`
		NodeInstanceID      string `json:"node_instance_id"`
		ProducerID          string `json:"producer_id"`
		RunID               string `json:"run_id"`
		WriterEpoch         string `json:"writer_epoch"`
		SourceSequence      uint64 `json:"source_sequence"`
		DecisionSequence    uint64 `json:"decision_sequence"`
		ObservationSequence uint64 `json:"observation_sequence"`
		Header              struct {
			Hash                  string  `json:"hash"`
			ParentHash            string  `json:"parent_hash"`
			UncleHash             string  `json:"uncle_hash"`
			Beneficiary           string  `json:"beneficiary"`
			StateRoot             string  `json:"state_root"`
			TransactionsRoot      string  `json:"transactions_root"`
			ReceiptsRoot          string  `json:"receipts_root"`
			LogsBloom             string  `json:"logs_bloom"`
			Difficulty            string  `json:"difficulty"`
			Number                uint64  `json:"number"`
			GasLimit              uint64  `json:"gas_limit"`
			GasUsed               uint64  `json:"gas_used"`
			Time                  uint64  `json:"time"`
			ExtraData             string  `json:"extra_data"`
			MixDigest             string  `json:"mix_digest"`
			Nonce                 string  `json:"nonce"`
			SourceRLP             string  `json:"source_rlp"`
			BaseFee               *string `json:"base_fee"`
			WithdrawalsRoot       *string `json:"withdrawals_root"`
			BlobGasUsed           *uint64 `json:"blob_gas_used"`
			ExcessBlobGas         *uint64 `json:"excess_blob_gas"`
			ParentBeaconBlockRoot *string `json:"parent_beacon_block_root"`
			RequestsHash          *string `json:"requests_hash"`
		} `json:"header"`
		TxCount       uint64     `json:"tx_count"`
		SystemTxCount *uint64    `json:"system_tx_count"`
		OldHead       vectorHead `json:"old_head"`
		NewHead       vectorHead `json:"new_head"`
		Justified     vectorHead `json:"justified"`
		Finalized     vectorHead `json:"finalized"`
		Attestation   struct {
			Status    string     `json:"status"`
			Justified vectorHead `json:"justified"`
			Finalized vectorHead `json:"finalized"`
			Digest    string     `json:"digest"`
		} `json:"attestation"`
	} `json:"input"`
	EventSequence uint64 `json:"event_sequence"`
	Expected      struct {
		SemanticDigest         string `json:"semantic_digest"`
		NodeCount              uint64 `json:"node_count"`
		BlobCount              uint64 `json:"blob_count"`
		TotalBlobBytes         uint64 `json:"total_blob_bytes"`
		FragmentCount          uint64 `json:"fragment_count"`
		RecordLength           uint64 `json:"record_length"`
		ContentBytes           uint64 `json:"content_bytes"`
		RecordDigest           string `json:"record_digest"`
		FragmentManifestDigest string `json:"fragment_manifest_digest"`
		Fragments              []struct {
			Index  uint64 `json:"index"`
			Length uint64 `json:"length"`
			SHA256 string `json:"sha256"`
		} `json:"fragments"`
		ManifestDigest string `json:"manifest_digest"`
	} `json:"expected"`
}

// vectorHead is one anchor of the vectors.
type vectorHead struct {
	Number uint64 `json:"number"`
	Hash   string `json:"hash"`
}

func (h vectorHead) head(t *testing.T) Head {
	t.Helper()
	return Head{Number: h.Number, Hash: mustHash32(t, h.Hash)}
}

func loadVectors(t *testing.T) *vectorsFile {
	t.Helper()
	data, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("golden vectors: %v", err)
	}
	var v vectorsFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("golden vectors: %v", err)
	}
	return &v
}

// mustBytes decodes a hex value of the vectors, with or without 0x.
func mustBytes(name, value string) []byte {
	raw, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	if err != nil {
		panic(err)
	}
	return raw
}

func mustHash32(t *testing.T, value string) [32]byte {
	t.Helper()
	raw := mustBytes("hash", value)
	if len(raw) != 32 {
		t.Fatalf("hash %s holds %d bytes", value, len(raw))
	}
	return [32]byte(raw)
}

func mustID16(t *testing.T, name, value string) semantic.ID16 {
	t.Helper()
	raw := mustBytes(name, value)
	if len(raw) != 16 {
		t.Fatalf("%s %s holds %d bytes", name, value, len(raw))
	}
	return semantic.ID16(raw)
}

// fixed decodes a fixed-width hex value into out.
func fixed(out []byte, value string) {
	raw := mustBytes("", value)
	if len(raw) != len(out) {
		panic(fmt.Sprintf("%s holds %d bytes, want %d", value, len(raw), len(out)))
	}
	copy(out, raw)
}

// input is the builder input of the vectors.
func (v *vectorsFile) input(t *testing.T) Input {
	t.Helper()
	i := &v.Input
	var chainID, difficulty block.UInt256
	var uncle, stateRoot, transactionsRoot, receiptsRoot, mixDigest [32]byte
	var withdrawals, beacon, requests block.Hash32
	var beneficiary [20]byte
	var bloom block.Bloom
	var nonce block.Nonce
	fixed(chainID[:], i.Network.ChainID)
	fixed(difficulty[:], i.Header.Difficulty)
	fixed(uncle[:], i.Header.UncleHash)
	fixed(stateRoot[:], i.Header.StateRoot)
	fixed(transactionsRoot[:], i.Header.TransactionsRoot)
	fixed(receiptsRoot[:], i.Header.ReceiptsRoot)
	fixed(mixDigest[:], i.Header.MixDigest)
	fixed(beneficiary[:], i.Header.Beneficiary)
	fixed(bloom[:], i.Header.LogsBloom)
	fixed(nonce[:], i.Header.Nonce)
	header := bscblock.SourceHeader{
		Hash:             mustHash32(t, i.Header.Hash),
		ParentHash:       mustHash32(t, i.Header.ParentHash),
		UncleHash:        uncle,
		Beneficiary:      beneficiary,
		StateRoot:        stateRoot,
		TransactionsRoot: transactionsRoot,
		ReceiptsRoot:     receiptsRoot,
		LogsBloom:        bloom,
		Difficulty:       difficulty,
		Number:           i.Header.Number,
		GasLimit:         i.Header.GasLimit,
		GasUsed:          i.Header.GasUsed,
		Time:             i.Header.Time,
		ExtraData:        mustBytes("extra_data", i.Header.ExtraData),
		MixDigest:        mixDigest,
		Nonce:            nonce,
		SourceRLP:        mustBytes("source_rlp", i.Header.SourceRLP),
	}
	if i.Header.BaseFee != nil {
		header.BaseFee = new(block.UInt256)
		fixed(header.BaseFee[:], *i.Header.BaseFee)
	}
	if i.Header.WithdrawalsRoot != nil {
		fixed(withdrawals[:], *i.Header.WithdrawalsRoot)
		header.WithdrawalsRoot = &withdrawals
	}
	if i.Header.BlobGasUsed != nil {
		header.BlobGasUsed = new(uint64)
		*header.BlobGasUsed = *i.Header.BlobGasUsed
	}
	if i.Header.ExcessBlobGas != nil {
		header.ExcessBlobGas = new(uint64)
		*header.ExcessBlobGas = *i.Header.ExcessBlobGas
	}
	if i.Header.ParentBeaconBlockRoot != nil {
		fixed(beacon[:], *i.Header.ParentBeaconBlockRoot)
		header.ParentBeaconBlockRoot = &beacon
	}
	if i.Header.RequestsHash != nil {
		fixed(requests[:], *i.Header.RequestsHash)
		header.RequestsHash = &requests
	}
	return Input{
		Network:             block.NetworkIdentity{Family: "EVM", ChainID: chainID, GenesisHash: mustHash32(t, i.Network.GenesisHash)},
		NodeInstanceID:      mustID16(t, "node_instance_id", i.NodeInstanceID),
		ProducerID:          mustID16(t, "producer_id", i.ProducerID),
		RunID:               mustID16(t, "run_id", i.RunID),
		WriterEpoch:         mustID16(t, "writer_epoch", i.WriterEpoch),
		SourceSequence:      i.SourceSequence,
		DecisionSequence:    i.DecisionSequence,
		ObservationSequence: i.ObservationSequence,
		Header:              header,
		TxCount:             i.TxCount,
		SystemTxCount:       i.SystemTxCount,
		OldHead:             i.OldHead.head(t),
		NewHead:             i.NewHead.head(t),
		Justified:           i.Justified.head(t),
		Finalized:           i.Finalized.head(t),
		Attestation: Attestation{Status: i.Attestation.Status, Justified: i.Attestation.Justified.head(t),
			Finalized: i.Attestation.Finalized.head(t), Digest: i.Attestation.Digest},
	}
}

// rawParts splits one field of the registry of the vectors into its eight
// values as text.
func rawParts(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) != 8 {
		t.Fatalf("registry field %s: %v (%d parts)", raw, err, len(parts))
	}
	out := make([]string, 0, 8)
	for _, part := range parts {
		var text string
		var number json.Number
		var flag bool
		switch {
		case json.Unmarshal(part, &text) == nil:
			out = append(out, text)
		case json.Unmarshal(part, &number) == nil:
			out = append(out, number.String())
		case json.Unmarshal(part, &flag) == nil:
			out = append(out, fmt.Sprintf("%t", flag))
		default:
			t.Fatalf("registry field value %s is neither text, number nor flag", part)
		}
	}
	return out
}

// The golden vectors of generate.py: one fixed Input of a real Chapel block
// and the digests the independent implementation computes from it.
func TestGoldenVectors(t *testing.T) {
	v := loadVectors(t)
	in := v.input(t)
	// The fixture is the stored block, with its exact native header
	// encoding and its genesis.
	// block.Hash32 and semantic.Hash32 name themselves with String, which
	// the %x verb would encode again; their text is compared as text.
	if "0x"+in.Header.Hash.String() != v.Fixture.BlockHash || in.Header.Number != v.Fixture.BlockNumber {
		t.Fatalf("the fixture block is %d %s", in.Header.Number, in.Header.Hash)
	}
	if sum := sha256.Sum256(in.Header.SourceRLP); fmt.Sprintf("%x", sum) != v.Fixture.HeaderRLPHash {
		t.Fatal("the header encoding differs from the stored one")
	}
	if "0x"+in.Network.GenesisHash.String() != v.Fixture.GenesisHash {
		t.Fatal("the genesis hash differs from the stored one")
	}
	// The millisecond rule of the pinned client holds for this header.
	milliseconds, err := bscblock.MilliTimestamp(in.Header.Time, in.Header.MixDigest)
	if err != nil {
		t.Fatal(err)
	}
	if low := binary.BigEndian.Uint64(in.Header.MixDigest[24:]); milliseconds != in.Header.Time*1000+low {
		t.Fatalf("the header carries %d milliseconds, want time*1000 + low_u64(mix digest)", milliseconds)
	}

	id := proto.NewEventID(proto.ID16(in.ProducerID), proto.ID16(in.RunID), v.EventSequence)
	built, err := BuildBlockEvent(in, id)
	if err != nil {
		t.Fatalf("BuildBlockEvent: %v", err)
	}
	want := v.Expected
	digest, err := built.Semantic.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digest != mustHash32(t, want.SemanticDigest) {
		t.Fatalf("semantic digest %s, want %s", digest, want.SemanticDigest)
	}
	header, nodes, err := codec.ReadRecord(built.Fragments)
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if uint64(len(nodes)) != want.NodeCount || header.NodeCount != want.NodeCount ||
		header.BlobCount != want.BlobCount || header.TotalBlobBytes != want.TotalBlobBytes {
		t.Fatalf("tree of %d nodes (%d in the header), %d blobs, %d blob bytes; want %d, %d, %d, %d",
			len(nodes), header.NodeCount, header.BlobCount, header.TotalBlobBytes, want.NodeCount,
			want.NodeCount, want.BlobCount, want.TotalBlobBytes)
	}
	if uint64(len(built.Fragments)) != want.FragmentCount || uint64(len(built.Frags)) != want.FragmentCount ||
		uint64(len(want.Fragments)) != want.FragmentCount {
		t.Fatalf("%d fragments, want %d", len(built.Fragments), want.FragmentCount)
	}
	for i, fragment := range want.Fragments {
		sum := proto.Hash32(sha256.Sum256(built.Fragments[i]))
		if fragment.Index != uint64(i) || uint64(len(built.Fragments[i])) != fragment.Length ||
			fmt.Sprintf("%x", sum) != fragment.SHA256 ||
			built.Frags[i] != (proto.FragmentDescriptor{FragmentIndex: uint64(i), EncodedLength: fragment.Length,
				FragmentSHA256: sum}) {
			t.Fatalf("fragment %d is %d bytes with digest %x, want %d bytes with digest %s", i,
				len(built.Fragments[i]), sum, fragment.Length, fragment.SHA256)
		}
	}
	event := built.Event
	if event.RecordLength != want.RecordLength || event.RecordDigest != mustHash32(t, want.RecordDigest) ||
		event.FragmentCount != want.FragmentCount ||
		event.FragmentManifestDigest != mustHash32(t, want.FragmentManifestDigest) ||
		event.Ordinal != 0 || event.Family != proto.FamilyBlock || event.Kind != proto.KindApplied {
		t.Fatalf("event descriptor %+v; record digest %s, want %s; fragment manifest digest %s, want %s", event,
			event.RecordDigest, want.RecordDigest, event.FragmentManifestDigest, want.FragmentManifestDigest)
	}
	// The seal is the last fragment; the content bytes are all the others.
	seal := built.Fragments[len(built.Fragments)-1]
	if event.RecordLength-uint64(len(seal)) != want.ContentBytes {
		t.Fatalf("content bytes %d, want %d", event.RecordLength-uint64(len(seal)), want.ContentBytes)
	}
	// The event feeds the manifest builder, and the one-event manifest
	// hashes to the digest of the generator.
	builder := codec.NewManifestBuilder()
	if err := builder.AddEvent(event, built.Frags); err != nil {
		t.Fatalf("AddEvent: %v", err)
	}
	manifest, err := builder.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(manifest); fmt.Sprintf("%x", sum) != want.ManifestDigest {
		t.Fatalf("manifest digest %x, want %s", sum, want.ManifestDigest)
	}
	if uint64(len(manifest)) != codec.ManifestHeaderBytes+proto.EventDescriptorSize+
		want.FragmentCount*proto.FragmentDescriptorSize {
		t.Fatalf("manifest of %d bytes", len(manifest))
	}
}

// numberValue is one enum value of the vectors.
func numberValue(t *testing.T, enum string, number json.Number) uint32 {
	t.Helper()
	value, err := number.Int64()
	if err != nil || value < 1 || value > math.MaxUint32 {
		t.Fatalf("enum %s holds value %s: %v", enum, number, err)
	}
	return uint32(value)
}

// The registry of protocol/proto is the registry the generator restates.
func TestGoldenRegistry(t *testing.T) {
	v := loadVectors(t)
	s := proto.LogicalSchemaV2()
	if v.Registry.SchemaVersion != proto.SchemaVersion {
		t.Fatalf("schema version %d, want %d", v.Registry.SchemaVersion, proto.SchemaVersion)
	}
	registered := 0
	for key := uint32(1); key <= 1<<16; key++ {
		if _, ok := s.MessageType(key); ok {
			registered++
		}
	}
	if registered != len(v.Registry.Messages) {
		t.Fatalf("the registry holds %d messages, the vectors %d", registered, len(v.Registry.Messages))
	}
	for text, want := range v.Registry.Messages {
		value, err := strconv.ParseUint(text, 10, 32)
		if err != nil {
			t.Fatalf("type key %q: %v", text, err)
		}
		m, ok := s.MessageType(uint32(value))
		if !ok {
			t.Fatalf("type key %s of the vectors is not registered", text)
		}
		if m.Name != want.Name || len(m.Fields) != len(want.Fields) {
			t.Fatalf("type key %s is %s with %d fields, want %s with %d fields", text, m.Name,
				len(m.Fields), want.Name, len(want.Fields))
		}
		for i, raw := range want.Fields {
			parts := rawParts(t, raw)
			f := m.Fields[i]
			got := []string{strconv.Itoa(int(f.Tag)), f.Name, f.Kind.String(), strconv.Itoa(int(f.Type)),
				fmt.Sprintf("%t", f.Optional), f.Enum, strconv.Itoa(int(f.ByteLen)), f.Oneof}
			if !slices.Equal(got, parts) {
				t.Fatalf("%s field %d is %v, want %v", m.Name, i, got, parts)
			}
		}
	}
	if len(v.Registry.Enums) == 0 {
		t.Fatal("the vectors hold no enums")
	}
	for name, values := range v.Registry.Enums {
		count := 0
		for value := uint32(1); value <= 16; value++ {
			if got, ok := s.EnumValue(name, value); ok {
				count++
				var want string
				for candidate, number := range values {
					if numberValue(t, name, number) == value {
						want = candidate
					}
				}
				if got != want {
					t.Fatalf("enum %s value %d is %s, want %s", name, value, got, want)
				}
			}
		}
		if count != len(values) {
			t.Fatalf("enum %s holds %d values, the vectors %d", name, count, len(values))
		}
	}
	var chainID [32]byte
	fixed(chainID[:], v.Registry.Profile["chain_id"])
	ref, ok := s.ProfileForNetwork(chainID)
	if !ok || ref.NetworkProfile != v.Registry.Profile["network_profile"] ||
		ref.ClientCommit != v.Registry.Profile["client_commit"] ||
		ref.AdapterVersion != v.Registry.Profile["adapter_version"] ||
		ref.ForkRulesVersion != v.Registry.Profile["fork_rules_version"] ||
		ref.NormalizationVersion != v.Registry.Profile["normalization_version"] {
		t.Fatalf("profile %+v ok %t, want %v", ref, ok, v.Registry.Profile)
	}
}
