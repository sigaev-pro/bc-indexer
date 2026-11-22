// Package nats_smoke is E03-11.block_lifecycle: the NATS smoke corpus of
// the delivery path. It builds a minimal independent corpus — a block
// event (including an empty block), transaction events and lifecycle
// removed/status events — with the E02 codec, plans their transport with
// the layout v2 planner, publishes them through the E03 publisher engine
// into the in-memory JetStream, and verifies every transport frame: the
// parts of a record reassemble into the exact fragment bytes, the PubAck
// of every frame composes one NATS_ACKED, and a retry republishes
// identical bytes that the stream dedups.
//
// This is a smoke corpus, not a production verifier: full blockchain
// semantic coverage belongs to E04 and the acceptance reader of E07. The
// memory server is a transport double, not a NATS deployment.
package nats_smoke

import (
	"crypto/sha256"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// The fixed identities of the corpus: one ETH-like network, one producer
// run, one stream and one binding generation.
var (
	smokeNetwork  = proto.NetworkIdentity{Family: proto.NetworkFamilyEVM}
	smokeProducer = proto.ID16{1}
	smokeRun      = proto.ID16{2}
	smokeEpoch    = proto.ID16{3}
	smokeStream   = "EXTRACTOR_ETH_V3"
	smokePrefix   = "evm.v3.eth"
	// smokeBudget is transport.max_frame_bytes: below the ~16 KiB encoded
	// size of one blob-chunk fragment, so the large blob of the corpus
	// splits into PART frames, while the small header/items/seal fragments
	// stay COMPLETE.
	smokeBudget = uint64(8 * 1024)
	// smokeRootType is the registry type key of the corpus root message.
	smokeRootType = uint32(9001)
)

// smokeBinding builds the destination binding of the corpus.
func smokeBinding() (destination.Binding, error) {
	b := destination.Binding{
		DestinationID: proto.ID16{7}, Generation: 1, Network: smokeNetwork,
		ExpectedAccount: "IDX_ETH", Stream: smokeStream, SubjectPrefix: smokePrefix,
	}
	digest, err := b.DescriptorDigestComputed()
	if err != nil {
		return destination.Binding{}, err
	}
	b.DescriptorDigest = digest
	return b, nil
}

// corpusEvent is one event of the smoke corpus.
type corpusEvent struct {
	family proto.Family
	kind   proto.Kind
	// nodes is the record tree of the event.
	nodes []codec.TreeNode
	// fragments holds the canonical fragment bytes in index order.
	fragments [][]byte
	// descriptor is the READY descriptor of the event.
	descriptor proto.EventDescriptor
	// frags are the fragment descriptors of the event.
	frags []proto.FragmentDescriptor
}

// smokeCorpus returns the minimal corpus: an empty block event, a
// transaction applied event with a large blob (PART frames), a removed
// event and a status event (lifecycle).
func smokeCorpus(producer, run proto.ID16, sequenceBase uint64) ([]*corpusEvent, error) {
	definitions := []struct {
		name   string
		family proto.Family
		kind   proto.Kind
		blob   int
		empty  bool
	}{
		{"empty block applied", proto.FamilyBlock, proto.KindApplied, 0, true},
		{"transaction applied", proto.FamilyTx, proto.KindApplied, 40 * 1024, false},
		{"transaction removed", proto.FamilyTx, proto.KindRemoved, 0, false},
		{"transaction status", proto.FamilyTx, proto.KindStatus, 0, false},
	}
	events := make([]*corpusEvent, 0, len(definitions))
	for ordinal, def := range definitions {
		eventID := proto.NewEventID(producer, run, sequenceBase+uint64(ordinal))
		nodes := corpusTree(def.blob, def.empty)
		event := &corpusEvent{family: def.family, kind: def.kind, nodes: nodes}
		result, err := codec.BuildRecord(eventID, smokeNetwork, def.family, def.kind,
			smokeRootType, nodes, func(_ uint64, fragment []byte) error {
				event.fragments = append(event.fragments, append([]byte(nil), fragment...))
				return nil
			})
		if err != nil {
			return nil, fmt.Errorf("corpus event %s: %w", def.name, err)
		}
		descriptor, frags, err := proto.BuildEventDescriptor(uint64(ordinal), eventID,
			def.family, def.kind, event.fragments)
		if err != nil {
			return nil, fmt.Errorf("descriptor of %s: %w", def.name, err)
		}
		if descriptor.RecordDigest != result.RecordDigest {
			return nil, fmt.Errorf("corpus event %s: codec and schema digests disagree", def.name)
		}
		event.descriptor = descriptor
		event.frags = frags
		events = append(events, event)
	}
	return events, nil
}

// corpusTree builds the record tree of one corpus event: a root message
// with a number field and a bytes field. The empty block carries an empty
// bytes value (BlobRef length 0); the applied transaction carries a large
// blob that splits into blob chunks and PART frames.
func corpusTree(blob int, empty bool) []codec.TreeNode {
	value := []byte{}
	if !empty && blob > 0 {
		value = make([]byte, blob)
		for i := range value {
			value[i] = byte(i % 251)
		}
	}
	root := codec.TreeNode{Kind: codec.ValueMessage, TypeKey: smokeRootType, ChildCount: 2}
	height := codec.TreeNode{Kind: codec.ValueUint64, HasFieldTag: true, FieldTag: 1, Uint64: 19_000_000}
	payload := codec.TreeNode{Kind: codec.ValueBytes, HasFieldTag: true, FieldTag: 2, Value: value}
	return []codec.TreeNode{root, height, payload}
}

// plannedCorpus is the transport-planned corpus of one batch.
type plannedCorpus struct {
	identity delivery.BatchIdentity
	events   []*corpusEvent
	entries  []proto.LayoutManifestEntry
	frames   []publisher.Frame
	// reassembly collects, per (ordinal, fragment), the part payloads the
	// publisher emitted, so the test can verify the parts reassemble into
	// the exact fragment bytes.
	reassembly map[delivery.FrameKey][]byte
}

// planCorpus builds the manifest, the layout and the exact frames of the
// corpus batch: the producer-side shape the E03 flow seals and the
// gateway republishes without transformation.
func planCorpus(events []*corpusEvent, sequenceBase uint64, planDigest proto.Hash32) (*plannedCorpus, error) {
	builder := codec.NewManifestBuilder()
	for _, event := range events {
		if err := builder.AddEvent(event.descriptor, event.frags); err != nil {
			return nil, err
		}
	}
	manifestDigest, err := builder.Digest()
	if err != nil {
		return nil, err
	}
	layoutDigestInput := proto.LayoutRequest{
		Budget: smokeBudget, Stream: smokeStream, Network: smokeNetwork,
		PublicationEpoch: smokeEpoch, SequenceBase: sequenceBase,
	}
	entries, layoutDigest, err := builder.BuildLayout(layoutDigestInput, planDigest)
	if err != nil {
		return nil, err
	}
	identity := delivery.BatchIdentity{
		Batch:                delivery.BatchID{Producer: smokeProducer, Run: smokeRun, Seq: 1},
		ManifestDigest:       manifestDigest,
		PlanDigest:           planDigest,
		LayoutManifestDigest: layoutDigest,
		BindingDigest:        proto.Hash32{}, // set by the caller
		IssuerStoreEpoch:     smokeEpoch,
	}
	planned := &plannedCorpus{identity: identity, events: events, entries: entries,
		reassembly: make(map[delivery.FrameKey][]byte)}
	for _, event := range events {
		subject, err := destination.Subject(smokePrefix, event.family, event.kind)
		if err != nil {
			return nil, err
		}
		eventEntries := entriesOfOrdinal(entries, event.descriptor.Ordinal)
		subPlan := &proto.LayoutPlan{Budget: smokeBudget, Stream: smokeStream, Entries: eventEntries}
		writer, err := codec.NewFragmentWriter(subPlan, codec.WriterConfig{
			Network: smokeNetwork, EventID: event.descriptor.EventID,
			RecordDigest:     event.descriptor.RecordDigest,
			PublicationEpoch: smokeEpoch, FragmentCount: event.descriptor.FragmentCount,
		})
		if err != nil {
			return nil, err
		}
		for i, entry := range eventEntries {
			chainSequence, err := codec.ChainSequence(sequenceBase, event.descriptor.Ordinal)
			if err != nil {
				return nil, err
			}
			fragment := event.fragments[i]
			err = writer.Write(entry, chainSequence, fragment, func(ef *codec.EmittedFrame) error {
				key := delivery.FrameKey{Ordinal: entry.Ordinal, Fragment: entry.FragmentIndex,
					Part: ef.PartIndex, Complete: entry.Form == proto.FormComplete}
				planned.frames = append(planned.frames, publisher.Frame{
					Key: key, Subject: subject, MessageID: ef.MessageID,
					Header: ef.NATSHeader, Payload: ef.Encoded,
				})
				var part []byte
				if ef.Frame.Complete != nil {
					part = append([]byte(nil), ef.Frame.Complete.FragmentBytes...)
				} else {
					part = append([]byte(nil), ef.Frame.Part.Data...)
				}
				planned.reassembly[key] = part
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}
	return planned, nil
}

// entriesOfOrdinal filters the layout entries of one event ordinal.
func entriesOfOrdinal(entries []proto.LayoutManifestEntry, ordinal uint64) []proto.LayoutManifestEntry {
	out := make([]proto.LayoutManifestEntry, 0, len(entries))
	for _, e := range entries {
		if e.Ordinal == ordinal {
			out = append(out, e)
		}
	}
	return out
}

// smokePlanDigest stands in for the reservation plan digest the gateway
// seed fixes; the smoke corpus pins one stable value.
func smokePlanDigest() proto.Hash32 {
	seed := []byte("extractor.reservation.v2 smoke corpus seed")
	return proto.Hash32(sha256.Sum256(seed))
}
