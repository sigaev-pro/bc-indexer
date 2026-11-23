// The event path of the acceptance pipeline: the normalization seam (the
// semantic descriptors of one decision), the E02 codec stage (records,
// manifest, layout) and the delivery drive that takes one decision from
// its durable marker to a fully retired batch. Everything is derived
// deterministically from the decision spec, so a recovery pass after a
// crash rebuilds the identical identities and the identical transport
// bytes (REQ-CAP-07, REQ-CAP-08, REQ-DLV-02).
package acceptance

import (
	"crypto/sha256"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/core/delivery/release"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// descriptors derives the ordered semantic descriptor set of a decision
// the normalizer way: the block entity first, then the body transactions
// in native order, then the optional-family events of the slot. A Removed
// or StatusChanged decision derives the entities of its target application
// under the decision's kind and revision.
func (h *acceptanceStack) descriptors(d decisionSpec) ([]semantic.SemanticDescriptor, error) {
	target := d
	if d.kind != semantic.KindApplied {
		stored, ok := h.source.decision(d.target)
		if !ok {
			return nil, fmt.Errorf("decision %d targets unadmitted decision %d", d.sequence, d.target)
		}
		target = stored
		if target.block.hash != d.block.hash {
			return nil, fmt.Errorf("decision %d names block %v, target applied %v",
				d.sequence, d.block.hash, target.block.hash)
		}
	}
	net := semNet(h.slot)
	producer, run := semID(h.slot.producer), semID(h.slot.run)
	node, writer := semID(h.slot.node), semID(h.slot.writer)
	activation := d.sequence
	if d.kind != semantic.KindApplied {
		activation = d.target
	}
	source := func(ordinal uint64) semantic.SourceRef {
		return semantic.SourceRef{NodeInstanceID: node, WriterEpoch: writer,
			SourceSequence: d.sequence*1000 + ordinal + 1}
	}
	block, err := semantic.NewBlockDescriptor(d.kind, net, producer, run,
		activation, semHash(target.block.hash), d.revision, source(0))
	if err != nil {
		return nil, err
	}
	out := []semantic.SemanticDescriptor{block}
	for i, tx := range target.block.txs {
		td, err := semantic.NewTransactionDescriptor(d.kind, net, producer, run,
			activation, semHash(target.block.hash), semHash(tx.hash), uint64(i), d.revision,
			source(uint64(i)+1))
		if err != nil {
			return nil, err
		}
		out = append(out, td)
	}
	for j := 0; j < target.block.balances; j++ {
		od, err := semantic.NewOptionalDescriptor(h.slot.optionalFamily, d.kind, net,
			producer, run, activation, semHash(target.block.hash),
			uint64(len(target.block.txs)), uint64(j), d.revision,
			source(uint64(len(target.block.txs))+uint64(j)+1))
		if err != nil {
			return nil, err
		}
		out = append(out, od)
	}
	return out, nil
}

// blockNodes builds the record tree of a block event from the fixture.
// Field 4 (native header metadata) exists only when the source block
// carries it — an absent optional field is absent, never a synthetic zero.
func blockNodes(b blockFixture) []codec.TreeNode {
	childCount := uint64(3)
	if len(b.metadata) > 0 {
		childCount = 4
	}
	root := codec.TreeNode{Kind: codec.ValueMessage, TypeKey: typeKeyBlock, ChildCount: childCount}
	height := codec.TreeNode{Kind: codec.ValueUint64, HasFieldTag: true, FieldTag: 1, Uint64: b.height}
	txCount := codec.TreeNode{Kind: codec.ValueUint64, HasFieldTag: true, FieldTag: 2,
		Uint64: uint64(len(b.txs))}
	hash := codec.TreeNode{Kind: codec.ValueBytes, HasFieldTag: true, FieldTag: 3,
		Value: append([]byte(nil), b.hash[:]...)}
	nodes := []codec.TreeNode{root, height, txCount, hash}
	if len(b.metadata) > 0 {
		nodes = append(nodes, codec.TreeNode{Kind: codec.ValueBytes, HasFieldTag: true,
			FieldTag: 4, Value: append([]byte(nil), b.metadata...)})
	}
	return nodes
}

// txNodes builds the record tree of a transaction event.
func txNodes(tx fixtureTx) []codec.TreeNode {
	root := codec.TreeNode{Kind: codec.ValueMessage, TypeKey: typeKeyTx, ChildCount: 2}
	hash := codec.TreeNode{Kind: codec.ValueBytes, HasFieldTag: true, FieldTag: 1,
		Value: append([]byte(nil), tx.hash[:]...)}
	payload := codec.TreeNode{Kind: codec.ValueBytes, HasFieldTag: true, FieldTag: 2,
		Value: fixturePayload(tx.payload)}
	return []codec.TreeNode{root, hash, payload}
}

// optionalNodes builds the record tree of an optional-family event (the
// BSC system / Bor legacy scope of the slot).
func optionalNodes(nativeOrder uint64) []codec.TreeNode {
	root := codec.TreeNode{Kind: codec.ValueMessage, TypeKey: typeKeyOptional, ChildCount: 2}
	order := codec.TreeNode{Kind: codec.ValueUint64, HasFieldTag: true, FieldTag: 1,
		Uint64: nativeOrder}
	value := codec.TreeNode{Kind: codec.ValueBytes, HasFieldTag: true, FieldTag: 2,
		Value: fixturePayload(200)}
	return []codec.TreeNode{root, order, value}
}

// builtEvent is one encoded event of a decision: its wire family/kind, its
// descriptor, its fragment descriptors and the canonical fragment bytes.
type builtEvent struct {
	family     proto.Family
	kind       proto.Kind
	descriptor proto.EventDescriptor
	frags      []proto.FragmentDescriptor
	fragments  [][]byte
	nodes      []codec.TreeNode
}

// buildEvents encodes every descriptor of a decision into its record
// fragment stream with the E02 codec and fixes the READY descriptor of
// each event.
func (h *acceptanceStack) buildEvents(d decisionSpec) ([]builtEvent, error) {
	descs, err := h.descriptors(d)
	if err != nil {
		return nil, err
	}
	events := make([]builtEvent, 0, len(descs))
	for ordinal, desc := range descs {
		family, kind := protoFamilyOf(desc.Family), protoKindOf(desc.Kind)
		eventID := proto.NewEventID(h.slot.producer, h.slot.run, eventSequence(d, uint64(ordinal)))
		var nodes []codec.TreeNode
		switch desc.Family {
		case semantic.FamilyBlock:
			nodes = blockNodes(d.block)
		case semantic.FamilyBalanceSystem, semantic.FamilyBorLegacy:
			nodes = optionalNodes(desc.Activation.ActivationID.EntityOrdinal -
				uint64(len(d.block.txs)) - 1)
		default:
			if ordinal < 1 || ordinal > len(d.block.txs) {
				return nil, fmt.Errorf("event %d of decision %d is not a body transaction",
					ordinal, d.sequence)
			}
			nodes = txNodes(d.block.txs[ordinal-1])
		}
		event := builtEvent{family: family, kind: kind, nodes: nodes}
		result, err := codec.BuildRecord(eventID, protoNet(h.slot), family, kind,
			nodes[0].TypeKey, nodes, func(_ uint64, fragment []byte) error {
				event.fragments = append(event.fragments, append([]byte(nil), fragment...))
				return nil
			})
		if err != nil {
			return nil, fmt.Errorf("event %d of decision %d: %w", ordinal, d.sequence, err)
		}
		descriptor, frags, err := proto.BuildEventDescriptor(uint64(ordinal), eventID,
			family, kind, event.fragments)
		if err != nil {
			return nil, fmt.Errorf("descriptor of event %d of decision %d: %w", ordinal, d.sequence, err)
		}
		if descriptor.RecordDigest != result.RecordDigest {
			return nil, fmt.Errorf("event %d of decision %d: codec and schema digests disagree",
				ordinal, d.sequence)
		}
		event.descriptor, event.frags = descriptor, frags
		events = append(events, event)
	}
	return events, nil
}

// plannedBatch is the transport-planned batch of one decision: its sealed
// identity, its frames (publisher shape) and its layout entries.
type plannedBatch struct {
	identity delivery.BatchIdentity
	events   []builtEvent
	entries  []proto.LayoutManifestEntry
	frames   []publisher.Frame
}

// planDigestOf derives the deterministic reservation plan digest of a
// batch sequence: the gateway seed half the seal fixes.
func planDigestOf(seq uint64) proto.Hash32 {
	seed := []byte(fmt.Sprintf("acceptance.reservation.v2|%d", seq))
	return proto.Hash32(sha256.Sum256(seed))
}

// sealedIdentity completes the manifest identity with the plan half the
// gateway reservation fixes; the derivation is deterministic per batch, so
// a recovery pass rebuilds the identical identity.
func (h *acceptanceStack) sealedIdentity(manifest delivery.BatchIdentity) delivery.BatchIdentity {
	out := manifest
	out.PlanDigest = planDigestOf(manifest.Batch.Seq)
	out.IssuerStoreEpoch = h.gw.StoreEpoch()
	return out
}

// planDecision builds the manifest, the layout and the exact frames of a
// decision's batch, and returns the sealed identity with the manifest
// half bound to the stack's verified binding. The decision-to-batch
// allocation is read through the source allocator, never re-derived.
func (h *acceptanceStack) planDecision(d decisionSpec) (*plannedBatch, error) {
	events, err := h.buildEvents(d)
	if err != nil {
		return nil, err
	}
	batchID, _, err := h.source.allocator.EnsureBatch(d.sequence)
	if err != nil {
		return nil, err
	}
	_, _, seq := batchID.Decode()
	// The manifest half of the batch identity is the semantic manifest the
	// decision fixed before DELIVERY_STAGING (D79): one derivation drives
	// both the READY commit and the sealed plan.
	manifestDigest, err := h.manifestDigestOf(d)
	if err != nil {
		return nil, err
	}
	builder := codec.NewManifestBuilder()
	for _, event := range events {
		if err := builder.AddEvent(event.descriptor, event.frags); err != nil {
			return nil, err
		}
	}
	sequenceBase := d.sequence * 1000
	layoutDigestInput := proto.LayoutRequest{Budget: acceptanceBudget, Stream: h.slot.stream,
		Network: protoNet(h.slot), PublicationEpoch: h.slot.epoch, SequenceBase: sequenceBase}
	entries, layoutDigest, err := builder.BuildLayout(layoutDigestInput, planDigestOf(seq))
	if err != nil {
		return nil, err
	}
	manifest := delivery.BatchIdentity{
		Batch:          delivery.BatchID{Producer: h.slot.producer, Run: h.slot.run, Seq: seq},
		ManifestDigest: manifestDigest, BindingDigest: h.bindingDigest,
	}
	identity := h.sealedIdentity(manifest)
	identity.LayoutManifestDigest = layoutDigest
	planned := &plannedBatch{identity: identity, events: events, entries: entries}
	for _, event := range events {
		subject, err := destination.Subject(h.slot.prefix, event.family, event.kind)
		if err != nil {
			return nil, err
		}
		eventEntries := entriesOfOrdinal(entries, event.descriptor.Ordinal)
		subPlan := &proto.LayoutPlan{Budget: acceptanceBudget, Stream: h.slot.stream, Entries: eventEntries}
		writer, err := codec.NewFragmentWriter(subPlan, codec.WriterConfig{
			Network: protoNet(h.slot), EventID: event.descriptor.EventID,
			RecordDigest:     event.descriptor.RecordDigest,
			PublicationEpoch: h.slot.epoch, FragmentCount: event.descriptor.FragmentCount,
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

// stageChunk stages one RAM chunk through the producer seam.
func (h *acceptanceStack) stageChunk(seq uint64, chunk string) error {
	store, ok := h.prod.(interface {
		StageChunk(seq uint64, chunk string) error
	})
	if !ok {
		return fmt.Errorf("the producer store exposes no staging seam")
	}
	return store.StageChunk(seq, chunk)
}

// keysOf extracts the accounting keys of frames in order.
func keysOf(frames []publisher.Frame) []delivery.FrameKey {
	keys := make([]delivery.FrameKey, len(frames))
	for i := range frames {
		keys[i] = frames[i].Key
	}
	return keys
}

// deliverDecision drives one decision to a retired batch: the idempotent
// marker/intent/staging/READY replay, the seal, the CLOSED registration,
// the publish to NATS_ACKED, the payload cleanup and the ordered release
// of both sides. Every step is safe to repeat; the loop re-reads the
// durable record after each commit, exactly as a recovering process does.
func (h *acceptanceStack) deliverDecision(t testingT, conn publisher.Conn, d decisionSpec) delivery.BatchIdentity {
	t.Helper()
	f := h.fence(1)
	batchSeq, err := h.driveMarker(t, d)
	if err != nil {
		t.Fatalf("decision %d marker: %v", d.sequence, err)
	}
	planned, err := h.planDecision(d)
	if err != nil {
		t.Fatalf("plan decision %d: %v", d.sequence, err)
	}
	if planned.identity.Batch.Seq != batchSeq {
		t.Fatalf("decision %d planned batch %d, marker bound %d", d.sequence,
			planned.identity.Batch.Seq, batchSeq)
	}
	id := planned.identity
	plan := release.NewSealedPlan(id, d.sequence*1000, h.slot.stream, keysOf(planned.frames))
	for {
		rec, ok := h.prod.Batch(batchSeq)
		if !ok {
			return id // the batch completed its release
		}
		switch rec.State {
		case delivery.StateReady:
			if err := h.prod.CommitSealed(batchSeq, plan); err != nil {
				t.Fatalf("CommitSealed of %d: %v", batchSeq, err)
			}
		case delivery.StateSealed:
			if err := h.gw.ImportClosed(f, plan, false); err != nil {
				t.Fatalf("ImportClosed of %d: %v", batchSeq, err)
			}
			ack, err := h.engine(conn).Publish(f, h.target,
				publisher.Batch{Identity: id, Frames: planned.frames})
			if err != nil {
				t.Fatalf("Publish of %d: %v", batchSeq, err)
			}
			if err := release.OnIngestAck(h.prod, batchSeq, ack); err != nil {
				t.Fatalf("OnIngestAck of %d: %v", batchSeq, err)
			}
		case delivery.StateAckedCleanup:
			if rec.HasPayload {
				if remains, err := release.CleanupPayload(h.prod, batchSeq, 100); err != nil || remains {
					t.Fatalf("cleanup of %d: remains=%v err=%v", batchSeq, remains, err)
				}
			}
			releaseAck, err := release.Release(h.gw, f, id.Batch, 100)
			if err != nil {
				t.Fatalf("Release of %d: %v", batchSeq, err)
			}
			if err := release.OnReleaseAck(h.prod, releaseAck); err != nil {
				t.Fatalf("OnReleaseAck of %d: %v", batchSeq, err)
			}
		default:
			t.Fatalf("the replay left batch %d in %s", batchSeq, rec.State)
		}
	}
}

// driveMarker drives the durable native marker and the WAL half of one
// decision: the decision-to-batch allocation, the staged chunks and the
// closing READY commit. It is idempotent and returns the bound batch
// sequence.
func (h *acceptanceStack) driveMarker(t testingT, d decisionSpec) (uint64, error) {
	t.Helper()
	batchID, _, err := h.source.allocator.EnsureBatch(d.sequence)
	if err != nil {
		return 0, err
	}
	_, _, seq := batchID.Decode()
	manifestDigest, err := h.manifestDigestOf(d)
	if err != nil {
		return 0, err
	}
	manifest := delivery.BatchIdentity{
		Batch:          delivery.BatchID{Producer: h.slot.producer, Run: h.slot.run, Seq: seq},
		ManifestDigest: manifestDigest, BindingDigest: h.bindingDigest,
	}
	rec, ok := h.prod.Batch(seq)
	if !ok {
		if seq <= h.prod.RetiredPrefix() {
			return seq, nil // the batch is retired; the frontier answers
		}
		allocated, err := h.prod.AllocateIntent()
		if err != nil {
			return 0, err
		}
		if allocated != seq {
			return 0, fmt.Errorf("producer allocated sequence %d, the marker bound %d", allocated, seq)
		}
		rec, ok = h.prod.Batch(seq)
		if !ok {
			return 0, fmt.Errorf("intent of sequence %d vanished", seq)
		}
	}
	if rec.State == delivery.StateIntent || rec.State == delivery.StateStaging {
		if err := h.stageChunk(seq, fmt.Sprintf("decision-%d-a", d.sequence)); err != nil {
			return 0, err
		}
		if err := h.stageChunk(seq, fmt.Sprintf("decision-%d-b", d.sequence)); err != nil {
			return 0, err
		}
	}
	switch rec.State {
	case delivery.StateIntent, delivery.StateStaging, delivery.StateReady:
		if err := h.prod.CommitReady(seq, manifest); err != nil {
			return 0, err
		}
	}
	return seq, nil
}

// manifestDigestOf derives the manifest digest of a decision: SHA-256 over
// the domain tag, one zero byte and each event descriptor digest in
// ordinal order — the semantic manifest each decision fixes before
// DELIVERY_STAGING (D79).
func (h *acceptanceStack) manifestDigestOf(d decisionSpec) (proto.Hash32, error) {
	descs, err := h.descriptors(d)
	if err != nil {
		return proto.Hash32{}, err
	}
	digest := sha256.New()
	digest.Write([]byte("acceptance.manifest.v1"))
	digest.Write([]byte{0})
	for _, desc := range descs {
		dg, err := desc.Digest()
		if err != nil {
			return proto.Hash32{}, err
		}
		digest.Write(dg[:])
	}
	var out proto.Hash32
	digest.Sum(out[:0])
	return out, nil
}

// deliverAll replays every admitted decision of the source to a retired
// batch in admission order — the recovery drive a restarting process runs.
func (h *acceptanceStack) deliverAll(t testingT, conn publisher.Conn) {
	t.Helper()
	for _, d := range h.source.decided() {
		h.deliverDecision(t, conn, d)
	}
}
