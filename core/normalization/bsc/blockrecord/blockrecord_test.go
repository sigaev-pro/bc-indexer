package blockrecord

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	bscblock "github.com/sigaev-pro/bc-indexer/core/normalization/bsc/block"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// render is the value of a decoded node: its kind and its value.
func render(f flatNode) string {
	switch f.kind {
	case codec.ValueUint64:
		return fmt.Sprintf("uint64:%d", f.u64)
	case codec.ValueUint32:
		return fmt.Sprintf("uint32:%d", f.u32)
	case codec.ValueEnum:
		return fmt.Sprintf("enum:%d", f.enum)
	case codec.ValueMessage:
		return fmt.Sprintf("message:%d", f.typeKey)
	case codec.ValueBool:
		return fmt.Sprintf("bool:%t", f.boolean)
	}
	return fmt.Sprintf("%s:%x", f.kind, f.value)
}

// mustBuild builds the event of in or fails the test.
func mustBuild(t *testing.T, in Input, sequence uint64) BuiltEvent {
	t.Helper()
	built, err := BuildBlockEvent(in, eventIDOf(in, sequence))
	if err != nil {
		t.Fatalf("BuildBlockEvent: %v", err)
	}
	return built
}

// readPaths decodes a built event and indexes its tree by field path.
func readPaths(t *testing.T, built BuiltEvent) map[string]flatNode {
	t.Helper()
	header, nodes := readBuild(t, built)
	checkRegistryConformance(t, header, nodes)
	return pathsOf(t, flattenRead(nodes))
}

// blockPath is the path of a field of the payload record.
const blockPath = "Envelope.block_applied."

func TestBuildBlockEventProducesTheReadyDescriptors(t *testing.T) {
	in := validInput()
	built := mustBuild(t, in, 9)
	event := built.Event
	if event.Ordinal != 0 || event.Family != proto.FamilyBlock || event.Kind != proto.KindApplied ||
		event.EventID != eventIDOf(in, 9) {
		t.Fatalf("event descriptor %+v", event)
	}
	if err := proto.ValidateFragments(event, built.Frags); err != nil {
		t.Fatalf("the fragment descriptors do not satisfy the READY rules: %v", err)
	}
	if event.FragmentCount != uint64(len(built.Fragments)) || len(built.Frags) != len(built.Fragments) ||
		event.FragmentCount < codec.MinimumFragmentCount {
		t.Fatalf("%d fragments, %d descriptors, count %d", len(built.Fragments), len(built.Frags),
			event.FragmentCount)
	}
	for i, fragment := range built.Fragments {
		sum := proto.Hash32(sha256.Sum256(fragment))
		if len(fragment) == 0 || len(fragment) > proto.FragmentMaxBytes ||
			built.Frags[i] != (proto.FragmentDescriptor{FragmentIndex: uint64(i),
				EncodedLength: uint64(len(fragment)), FragmentSHA256: sum}) {
			t.Fatalf("fragment %d of %d bytes does not match its descriptor %+v", i, len(fragment), built.Frags[i])
		}
	}
	digest, err := proto.RecordDigest(built.Fragments)
	if err != nil || digest != event.RecordDigest {
		t.Fatalf("record digest %x, descriptor %x, err %v", digest, event.RecordDigest, err)
	}

	d := built.Semantic
	if d.Family != semantic.FamilyBlock || d.Kind != semantic.KindApplied || d.Revision != 0 ||
		d.Activation.ActivationID != (semantic.ActivationID{DecisionSequence: 7, EntityOrdinal: 0}) ||
		d.Activation.BlockHash != semantic.Hash32(hashOf(0x22)) || d.Activation.ProducerID != in.ProducerID ||
		d.Activation.RunID != in.RunID || d.Network.ChainID != semantic.UInt256(in.Network.ChainID) ||
		d.Network.GenesisHash != semantic.Hash32(in.Network.GenesisHash) ||
		d.Source != (semantic.SourceRef{NodeInstanceID: in.NodeInstanceID, WriterEpoch: in.WriterEpoch, SourceSequence: 1}) {
		t.Fatalf("semantic descriptor %+v", d)
	}

	r := built.Record
	if r.Activation != (block.BlockActivationRef{Network: in.Network, BlockHash: hashOf(0x22),
		ProducerID: block.Id16(in.ProducerID), RunID: block.Id16(in.RunID),
		ActivationID: block.ActivationID{DecisionSequence: 7, EntityOrdinal: 0}}) ||
		r.Admission != (block.ObservationRef{ProducerID: block.Id16(in.ProducerID), RunID: block.Id16(in.RunID),
			ObservationSequence: 3}) {
		t.Fatalf("record identity %+v", r)
	}
	m := r.Metadata
	if m.Number != 420 || m.Hash != hashOf(0x22) || m.ParentHash != hashOf(0x21) ||
		m.SourceTransactionCount != 3 || !bytes.Equal(m.Header.SourceRLP, in.Header.SourceRLP) ||
		!bytes.Equal(m.Header.ExtraData, in.Header.ExtraData) ||
		m.Timestamp != block.TimestampFromMilliseconds(1_795_616_202*1000+450) ||
		m.Timestamp.Precision != block.PrecisionMilliseconds || m.Header.Number != 420 ||
		m.Header.GasUsed != 123_456 {
		t.Fatalf("block metadata %+v", m)
	}
	if count, ok := m.Details.Bsc.ParliaSystemTransactionCount.Get(); !ok || count != 1 {
		t.Fatalf("Parlia system transaction count %d present %t", count, ok)
	}
	if r.InitialSnapshot.NativeDecision != (block.NativeDecisionRef{NodeInstanceID: block.Id16(in.NodeInstanceID),
		ProducerID: block.Id16(in.ProducerID), RunID: block.Id16(in.RunID), DecisionSequence: 7}) ||
		r.InitialSnapshot.ObservedExecutionHead != (block.AnchorRef{Height: 420, Hash: hashOf(0x22)}) {
		t.Fatalf("initial snapshot %+v", r.InitialSnapshot)
	}
}

// codec.ReadRecord returns the tree that was built.
func TestBuildBlockEventRoundTripThroughReadRecord(t *testing.T) {
	in := validInput()
	built := mustBuild(t, in, 9)
	profile, ok := proto.LogicalSchemaV2().ProfileForNetwork(in.Network.ChainID)
	if !ok {
		t.Fatal("the network of the input has no profile")
	}
	nodes, err := envelopeTree(in, eventIDOf(in, 9), profile, &built.Record)
	if err != nil {
		t.Fatalf("envelopeTree: %v", err)
	}
	header, read := readBuild(t, built)
	if header.RootType != proto.TypeEnvelope || header.EventFamily != uint32(proto.FamilyBlock) ||
		header.EventKind != uint32(proto.KindApplied) || header.SchemaVersion != proto.SchemaVersion ||
		header.Network.ChainID != in.Network.ChainID || header.Network.GenesisHash != in.Network.GenesisHash ||
		header.NodeCount != uint64(len(nodes)) || header.FragmentCount != uint64(len(built.Fragments)) {
		t.Fatalf("record header %+v", header)
	}
	if err := samePaths(builtPaths(t, nodes), pathsOf(t, flattenRead(read))); err != nil {
		t.Fatal(err)
	}
}

// Every type key, field tag and enum value of the built tree is in the
// registry, and the tree holds exactly the nodes of the input.
func TestBuildBlockEventTreeFollowsTheRegistry(t *testing.T) {
	in := validInput()
	paths := readPaths(t, mustBuild(t, in, 9))
	id := eventIDOf(in, 9)
	want := map[string]string{
		"Envelope.schema_version":                                                               "uint32:2",
		"Envelope.network.family":                                                               "enum:1",
		"Envelope.network.chain_id":                                                             "bytes:" + hexText(in.Network.ChainID[:]),
		"Envelope.network.genesis_hash":                                                         "bytes:" + hashOf(0x01).String(),
		"Envelope.profile_ref.network_profile":                                                  "utf8:6273632d746573746e6574",
		"Envelope.profile_ref.client_commit":                                                    "utf8:" + hexText([]byte("54afaff4da31ac9e49ba646a4f65104edac5a2f3")),
		"Envelope.profile_ref.adapter_version":                                                  "utf8:" + hexText([]byte("bsc-adapter-v1")),
		"Envelope.profile_ref.fork_rules_version":                                               "utf8:" + hexText([]byte("bsc-fork-rules-v1")),
		"Envelope.profile_ref.normalization_version":                                            "utf8:" + hexText([]byte("bsc-normalization-v1")),
		"Envelope.event_id":                                                                     "bytes:" + hexText(id[:]),
		"Envelope.producer_id":                                                                  "bytes:" + hexText(in.ProducerID[:]),
		"Envelope.run_id":                                                                       "bytes:" + hexText(in.RunID[:]),
		"Envelope.source.node_instance_id":                                                      "bytes:" + hexText(in.NodeInstanceID[:]),
		"Envelope.source.writer_epoch":                                                          "bytes:" + hexText(in.WriterEpoch[:]),
		"Envelope.source.source_sequence":                                                       "uint64:1",
		"Envelope.capture_mode":                                                                 "enum:1",
		"Envelope.block_applied.activation.network.family":                                      "enum:1",
		"Envelope.block_applied.activation.block_hash":                                          "bytes:" + hashOf(0x22).String(),
		"Envelope.block_applied.activation.activation_id.decision_sequence":                     "uint64:7",
		"Envelope.block_applied.activation.activation_id.entity_ordinal":                        "uint64:0",
		"Envelope.block_applied.admission.observation_sequence":                                 "uint64:3",
		"Envelope.block_applied.metadata.number":                                                "uint64:420",
		"Envelope.block_applied.metadata.timestamp.seconds":                                     "uint64:1795616202",
		"Envelope.block_applied.metadata.timestamp.nanos":                                       "uint32:450000000",
		"Envelope.block_applied.metadata.timestamp.precision":                                   "enum:2",
		"Envelope.block_applied.metadata.source_transaction_count":                              "uint64:3",
		"Envelope.block_applied.metadata.header.number":                                         "uint64:420",
		"Envelope.block_applied.metadata.header.gas_used":                                       "uint64:123456",
		"Envelope.block_applied.metadata.header.timestamp_seconds":                              "uint64:1795616202",
		"Envelope.block_applied.metadata.header.mix_digest":                                     "bytes:" + hexText(in.Header.MixDigest[:]),
		"Envelope.block_applied.metadata.header.extra_data":                                     "bytes:" + hexText(in.Header.ExtraData),
		"Envelope.block_applied.metadata.header.source_rlp":                                     "bytes:" + hexText(in.Header.SourceRLP),
		"Envelope.block_applied.metadata.details.bsc.parlia_system_transaction_count":           "uint64:1",
		"Envelope.block_applied.initial_snapshot.native_decision.decision_sequence":             "uint64:7",
		"Envelope.block_applied.initial_snapshot.observed_execution_head.height":                "uint64:420",
		"Envelope.block_applied.initial_snapshot.observed_execution_head.hash":                  "bytes:" + hashOf(0x22).String(),
		"Envelope.block_applied.initial_snapshot.markers.bsc.attested.snapshot_head.height":     "uint64:420",
		"Envelope.block_applied.initial_snapshot.markers.bsc.attested.justified.height":         "uint64:419",
		"Envelope.block_applied.initial_snapshot.markers.bsc.attested.finalized.height":         "uint64:418",
		"Envelope.block_applied.initial_snapshot.markers.bsc.effective_finalized.kind":          "enum:1",
		"Envelope.block_applied.initial_snapshot.markers.bsc.effective_finalized.anchor.height": "uint64:418",
		"Envelope.block_applied.initial_snapshot.markers.bsc.effective_finalized.origin":        "enum:1",
		"Envelope.block_applied.initial_snapshot.markers.bsc.effective_basis":                   "enum:1",
		// Present zeros stay present; the zero hashes and counts are not
		// absences.
		"Envelope.block_applied.metadata.header.base_fee":                 "bytes:" + strings.Repeat("00", 32),
		"Envelope.block_applied.metadata.header.blob_gas_used":            "uint64:0",
		"Envelope.block_applied.metadata.header.excess_blob_gas":          "uint64:0",
		"Envelope.block_applied.metadata.header.parent_beacon_block_root": "bytes:" + strings.Repeat("00", 32),
		"Envelope.block_applied.activation.network.genesis_hash":          "bytes:" + hashOf(0x01).String(),
	}
	for path, expected := range want {
		node, ok := paths[path]
		if !ok {
			t.Fatalf("%s is absent from the tree", path)
		}
		if got := render(node); got != expected {
			t.Fatalf("%s is %s, want %s", path, got, expected)
		}
	}
	if _, ok := paths["Envelope.block_applied.metadata.details.eth"]; ok {
		t.Fatal("the tree carries the Eth family details")
	}
	if _, ok := paths["Envelope.block_applied.initial_snapshot.markers.bsc.attested.justified.hash"]; !ok {
		t.Fatal("the attested justified anchor has no hash")
	}
}

func hexText(b []byte) string { return fmt.Sprintf("%x", b) }

// proto.ValidateEnvelope over the built parts returns exactly the READY
// descriptors of the built event.
func TestBuildBlockEventMatchesValidateEnvelope(t *testing.T) {
	in := validInput()
	built := mustBuild(t, in, 9)
	event, frags, err := proto.ValidateEnvelope(0, &proto.Envelope{EventID: eventIDOf(in, 9),
		Profile: profiles.FamilyBsc, Semantic: built.Semantic,
		Payload: proto.Payload{Variant: profiles.VariantBlockApplied, Fragments: built.Fragments,
			BlockRecord: &built.Record}})
	if err != nil {
		t.Fatalf("ValidateEnvelope: %v", err)
	}
	if event != built.Event || !slices.Equal(frags, built.Frags) {
		t.Fatalf("ValidateEnvelope returned %+v, the builder %+v", event, built.Event)
	}
}

// One negative control per mandatory fact: each removed fact is a named
// error that wraps block.ErrInvalidValue or proto.ErrInvalidValue, and no
// event is returned.
func TestBuildBlockEventRejectsMissingFacts(t *testing.T) {
	sameID := func(in Input) proto.EventID { return eventIDOf(in, 9) }
	// A missing fact wraps block.ErrInvalidValue; a wrong event id wraps
	// proto.ErrInvalidValue.
	var fact, identity = block.ErrInvalidValue, proto.ErrInvalidValue
	// The message of a rejection starts with its sentinel and the name of
	// the fact; tail holds the rest of the message where the name is only
	// its start.
	tail := map[string]string{
		"decided head differs from the header":                "differs from the header hash",
		"zero block hash":                                     "differs from the header hash",
		"header number differs from the decided block number": "differs from the header number",
		"parent hash zero":                                    "has a zero parent hash",
		"attestation digest uppercase":                        "not 64 lowercase hex digits",
		"attestation digest long":                             "not 64 lowercase hex digits",
		"attestation digest not hex":                          "not 64 lowercase hex digits",
		"header source rlp empty":                             "source_rlp is mandatory",
		"timestamp overflow":                                  "overflows the millisecond timestamp",
	}
	for _, tc := range []struct {
		name, reason string
		change       func(*Input)
		id           func(Input) proto.EventID
		sentinel     error
	}{
		{"network family", `network family "BSC"`, func(in *Input) { in.Network.Family = "BSC" }, nil, fact},
		{"network chain id zero", "network chain id is zero", func(in *Input) { in.Network.ChainID = block.UInt256{} }, nil, fact},
		{"network genesis hash zero", "network genesis hash is zero", func(in *Input) { in.Network.GenesisHash = block.Hash32{} }, nil, fact},
		{"node instance id zero", "node instance id is zero", func(in *Input) { in.NodeInstanceID = semantic.ID16{} }, nil, fact},
		{"producer id zero", "producer id is zero", func(in *Input) { in.ProducerID = semantic.ID16{} }, nil, fact},
		{"run id zero", "run id is zero", func(in *Input) { in.RunID = semantic.ID16{} }, nil, fact},
		{"writer epoch zero", "writer epoch is zero", func(in *Input) { in.WriterEpoch = semantic.ID16{} }, nil, fact},
		{"source sequence zero", "source sequence 0", func(in *Input) { in.SourceSequence = 0 }, nil, fact},
		{"event id of another producer", "event id carries another producer", nil,
			func(in Input) proto.EventID { return proto.NewEventID(proto.ID16(idOf(0x99)), proto.ID16(in.RunID), 9) }, identity},
		{"event id of another run", "event id carries another run", nil,
			func(in Input) proto.EventID {
				return proto.NewEventID(proto.ID16(in.ProducerID), proto.ID16(idOf(0x99)), 9)
			}, identity},
		{"event id without a producer", "event id producer is zero", nil,
			func(in Input) proto.EventID { return proto.NewEventID(proto.ID16{}, proto.ID16(in.RunID), 9) }, identity},
		{"old head hash zero", "old head hash is zero", func(in *Input) { in.OldHead.Hash = block.Hash32{} }, nil, fact},
		{"new head hash zero", "new head hash is zero", func(in *Input) { in.NewHead.Hash = block.Hash32{} }, nil, fact},
		{"justified hash zero", "justified hash is zero", func(in *Input) { in.Justified.Hash = block.Hash32{} }, nil, fact},
		{"finalized hash zero", "finalized hash is zero", func(in *Input) { in.Finalized.Hash = block.Hash32{} }, nil, fact},
		{"decided head differs from the header", "decided head ",
			func(in *Input) { in.Header.Hash = hashOf(0x77) }, nil, fact},
		{"zero block hash", "decided head ", func(in *Input) { in.Header.Hash = block.Hash32{} }, nil, fact},
		{"header number differs from the decided block number", "decided head ",
			func(in *Input) { in.Header.Number++ }, nil, fact},
		{"attestation status unknown", `attestation status ""`, func(in *Input) { in.Attestation.Status = "" }, nil, fact},
		{"attestation justified hash zero", "attestation justified hash is zero",
			func(in *Input) { in.Attestation.Justified.Hash = block.Hash32{} }, nil, fact},
		{"attestation finalized hash zero", "attestation finalized hash is zero",
			func(in *Input) { in.Attestation.Finalized.Hash = block.Hash32{} }, nil, fact},
		{"attestation digest empty", "attestation digest", func(in *Input) { in.Attestation.Digest = "" }, nil, fact},
		{"attestation digest uppercase", "attestation digest ",
			func(in *Input) { in.Attestation.Digest = strings.ToUpper(in.Attestation.Digest) }, nil, fact},
		{"attestation digest short", "attestation digest", func(in *Input) { in.Attestation.Digest = in.Attestation.Digest[:63] }, nil, fact},
		{"attestation digest long", "attestation digest", func(in *Input) { in.Attestation.Digest += "0" }, nil, fact},
		{"attestation digest not hex", "attestation digest", func(in *Input) { in.Attestation.Digest = strings.Repeat("g", 64) }, nil, fact},
		{"header source rlp empty", "block metadata: ", func(in *Input) { in.Header.SourceRLP = nil }, nil, fact},
		{"timestamp overflow", "block metadata: ", func(in *Input) { in.Header.Time = ^uint64(0) }, nil, fact},
		{"parent hash zero", "block metadata: ", func(in *Input) { in.Header.ParentHash = block.Hash32{} }, nil, fact},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			if tc.change != nil {
				tc.change(&in)
			}
			id := sameID(in)
			if tc.id != nil {
				id = tc.id(in)
			}
			built, err := BuildBlockEvent(in, id)
			if err == nil {
				t.Fatal("the input without the fact was accepted")
			}
			if !reflect.DeepEqual(built, BuiltEvent{}) {
				t.Fatal("a rejected input returned an event")
			}
			if !errors.Is(err, block.ErrInvalidValue) && !errors.Is(err, proto.ErrInvalidValue) {
				t.Fatalf("error %v wraps neither block.ErrInvalidValue nor proto.ErrInvalidValue", err)
			}
			if want := tc.sentinel.Error() + ": " + tc.reason; !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("error %q does not start with %q", err, want)
			}
			if !strings.Contains(err.Error(), tail[tc.name]) {
				t.Fatalf("error %q does not name %q", err, tail[tc.name])
			}
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("error %v does not wrap %v", err, tc.sentinel)
			}
		})
	}
	// The native rejections keep their own sentinels as well.
	in := validInput()
	in.Header.SourceRLP = nil
	if _, err := BuildBlockEvent(in, eventIDOf(in, 9)); !errors.Is(err, bscblock.ErrEmptySourceRLP) {
		t.Fatalf("empty source RLP: %v", err)
	}
	in = validInput()
	in.Header.Time = ^uint64(0)
	if _, err := BuildBlockEvent(in, eventIDOf(in, 9)); !errors.Is(err, bscblock.ErrMilliOverflow) {
		t.Fatalf("timestamp overflow: %v", err)
	}
}

// Optional fork fields follow native pointer presence: each one is a node
// when the native header carried it, a present zero included, and no node
// otherwise.
func TestBuildBlockEventHeaderOptionalsFollowNativePresence(t *testing.T) {
	in := validInput()
	paths := readPaths(t, mustBuild(t, in, 1))
	all := map[string]string{
		"base_fee":                 "bytes:" + strings.Repeat("00", 32),
		"withdrawals_root":         "bytes:56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		"blob_gas_used":            "uint64:0",
		"excess_blob_gas":          "uint64:0",
		"parent_beacon_block_root": "bytes:" + strings.Repeat("00", 32),
		"requests_hash":            "bytes:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}
	header := blockPath + "metadata.header."
	for name, expected := range all {
		if got, ok := paths[header+name]; !ok || render(got) != expected {
			t.Fatalf("%s is %s present %t, want %s", name, render(got), ok, expected)
		}
	}
	drop := map[string]func(*bscblock.SourceHeader){
		"base_fee":                 func(h *bscblock.SourceHeader) { h.BaseFee = nil },
		"withdrawals_root":         func(h *bscblock.SourceHeader) { h.WithdrawalsRoot = nil },
		"blob_gas_used":            func(h *bscblock.SourceHeader) { h.BlobGasUsed = nil },
		"excess_blob_gas":          func(h *bscblock.SourceHeader) { h.ExcessBlobGas = nil },
		"parent_beacon_block_root": func(h *bscblock.SourceHeader) { h.ParentBeaconBlockRoot = nil },
		"requests_hash":            func(h *bscblock.SourceHeader) { h.RequestsHash = nil },
	}
	for name, remove := range drop {
		in := validInput()
		remove(&in.Header)
		paths := readPaths(t, mustBuild(t, in, 1))
		if _, ok := paths[header+name]; ok {
			t.Fatalf("%s is a node although the native header did not carry it", name)
		}
		for other := range all {
			if other != name {
				if _, ok := paths[header+other]; !ok {
					t.Fatalf("%s is absent after %s was removed", other, name)
				}
			}
		}
	}
	// None carried: none present.
	in = validInput()
	in.Header.BaseFee, in.Header.WithdrawalsRoot = nil, nil
	in.Header.BlobGasUsed, in.Header.ExcessBlobGas = nil, nil
	in.Header.ParentBeaconBlockRoot, in.Header.RequestsHash = nil, nil
	paths = readPaths(t, mustBuild(t, in, 1))
	for name := range all {
		if _, ok := paths[header+name]; ok {
			t.Fatalf("%s is a node without a native field", name)
		}
	}
	// Values other than zero are carried as they are.
	in = validInput()
	in.Header.BlobGasUsed = uint64Of(131_072)
	baseFee := block.UInt256{31: 7}
	in.Header.BaseFee = &baseFee
	paths = readPaths(t, mustBuild(t, in, 1))
	if got := render(paths[header+"blob_gas_used"]); got != "uint64:131072" {
		t.Fatalf("blob_gas_used is %s", got)
	}
	if got := render(paths[header+"base_fee"]); got != "bytes:"+strings.Repeat("00", 31)+"07" {
		t.Fatalf("base_fee is %s", got)
	}
}

// The Parlia system transaction count is a node only when it was observed:
// nil gives an absent node, never zero.
func TestBuildBlockEventSystemTxCountAbsentNeverZero(t *testing.T) {
	path := blockPath + "metadata.details.bsc.parlia_system_transaction_count"
	in := validInput()
	in.SystemTxCount = nil
	paths := readPaths(t, mustBuild(t, in, 1))
	if _, ok := paths[path]; ok {
		t.Fatal("an unobserved system transaction count became a node")
	}
	// The family details node still exists, with no child.
	if node, ok := paths[blockPath+"metadata.details.bsc"]; !ok || node.children != 0 {
		t.Fatalf("the BSC details node is %+v", node)
	}
	in.SystemTxCount = uint64Of(0)
	if got := render(readPaths(t, mustBuild(t, in, 1))[path]); got != "uint64:0" {
		t.Fatalf("an observed zero became %s", got)
	}
}

// The same input and event id build twice to identical bytes, and the
// semantic digest is the same across attempts, which change only the event
// id.
func TestBuildBlockEventIsDeterministic(t *testing.T) {
	in := validInput()
	first := mustBuild(t, in, 9)
	second := mustBuild(t, in, 9)
	if !reflectEqual(first.Fragments, second.Fragments) || first.Event != second.Event {
		t.Fatal("two builds of the same input differ")
	}
	digest, err := first.Semantic.Digest()
	if err != nil {
		t.Fatal(err)
	}
	retry := mustBuild(t, in, 10)
	if retry.Semantic != first.Semantic {
		t.Fatal("a new event id changed the semantic descriptor")
	}
	// The record digest covers the event id, so a retry builds other bytes;
	// the semantic identity does not move.
	if reflectEqual(first.Fragments, retry.Fragments) {
		t.Fatal("another event id built identical fragments")
	}
	if again, err := retry.Semantic.Digest(); err != nil || again != digest {
		t.Fatalf("semantic digest %x across attempts, want %x (%v)", again, digest, err)
	}
}

// reflectEqual compares fragment lists byte for byte.
func reflectEqual(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// A network without a registered profile gets no record.
func TestBuildBlockEventRefusesAnUnregisteredNetwork(t *testing.T) {
	in := validInput()
	in.Network.ChainID[31] = 56
	_, err := BuildBlockEvent(in, eventIDOf(in, 9))
	if !errors.Is(err, proto.ErrClosedRegistry) || !errors.Is(err, proto.ErrInvalidValue) {
		t.Fatalf("chain id 56: %v", err)
	}
}

// An empty block is a first-class record, and the sequences that start at
// zero in the native tree are values.
func TestBuildBlockEventAcceptsAnEmptyBlock(t *testing.T) {
	in := validInput()
	in.TxCount = 0
	in.SystemTxCount = uint64Of(0)
	in.DecisionSequence = 0
	in.ObservationSequence = 0
	in.Header.GasUsed = 0
	paths := readPaths(t, mustBuild(t, in, 1))
	for path, want := range map[string]string{
		blockPath + "metadata.source_transaction_count":                     "uint64:0",
		blockPath + "activation.activation_id.decision_sequence":            "uint64:0",
		blockPath + "admission.observation_sequence":                        "uint64:0",
		blockPath + "initial_snapshot.markers.bsc.effective_finalized.kind": "enum:1",
	} {
		if got := render(paths[path]); got != want {
			t.Fatalf("%s is %s, want %s", path, got, want)
		}
	}
}

// The sequences of the input reach the semantic descriptor and the tree as
// they are.
func TestBuildBlockEventCarriesTheSequences(t *testing.T) {
	in := validInput()
	in.SourceSequence, in.DecisionSequence, in.ObservationSequence = 9, 21, 5
	built := mustBuild(t, in, 1)
	d := built.Semantic
	if d.Source.SourceSequence != 9 || d.Activation.ActivationID.DecisionSequence != 21 {
		t.Fatalf("semantic descriptor carries source sequence %d and decision sequence %d, want 9 and 21",
			d.Source.SourceSequence, d.Activation.ActivationID.DecisionSequence)
	}
	paths := readPaths(t, built)
	for path, want := range map[string]string{
		"Envelope.source.source_sequence":                                "uint64:9",
		blockPath + "activation.activation_id.decision_sequence":         "uint64:21",
		blockPath + "admission.observation_sequence":                     "uint64:5",
		blockPath + "initial_snapshot.native_decision.decision_sequence": "uint64:21",
		blockPath + "initial_snapshot.observed_execution_head.height":    "uint64:420",
	} {
		if got := render(paths[path]); got != want {
			t.Fatalf("%s is %s, want %s", path, got, want)
		}
	}
}

// The fragments own their bytes; the record shares the header bytes of the
// input.
func TestBuildBlockEventSharesHeaderBytesWithTheRecord(t *testing.T) {
	in := validInput()
	built := mustBuild(t, in, 9)
	in.Header.ExtraData[0] ^= 0xFF
	if !bytes.Equal(built.Record.Metadata.Header.ExtraData, in.Header.ExtraData) {
		t.Fatal("the record does not share the header bytes of the input")
	}
	// The fragments were built before the change and keep the old bytes.
	paths := readPaths(t, built)
	if got := render(paths[blockPath+"metadata.header.extra_data"]); got != "bytes:"+hexText(validInput().Header.ExtraData) {
		t.Fatalf("the fragments followed a later change of the input: %s", got)
	}
}
