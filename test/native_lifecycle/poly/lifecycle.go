package poly

import (
	"encoding/json"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/adapters/bor/finality_observer"
	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// sharedState is the durable-side state shared across a crash: the
// lifecycle periods and records of every block hash, the native decision
// counter and the finality observation stream. A crashed journal reopens
// over the same shared state, which is what the repeat of a native marker
// replays against.
type sharedState struct {
	obs      *finalityobserver.Observer
	obsStore *finalityobserver.MemoryStore

	// periods keeps every canonical period of every block hash, in order;
	// the last period of a hash is its current lifecycle object.
	periods map[block.Hash32][]block.ActivationState
	records map[block.Hash32][]block.BlockRecord

	decisions    uint64
	observations uint64
}

// driver is one producer journal over the fixture chain: it turns native
// import and reorg decisions into lifecycle events, commits each decision
// as one producer batch through the real E03-01 journal, and folds the
// committed events into the protocol/block lifecycle automaton.
type driver struct {
	chain   *chain
	journal *producer.Store
	shared  *sharedState
}

// newFixture builds the chain and the shared state with its finality
// observer wired over the same chain, so the snapshots carry the real
// Polygon markers of the fixture's own milestone whitelists.
func newFixture(chain *chain) (*sharedState, error) {
	obsStore := finalityobserver.NewMemoryStore()
	observer, err := finalityobserver.NewObserver(obsStore, fixtureSources(), fixtureHeimdall{}, fixtureL1{}, chainAncestry{chain})
	if err != nil {
		return nil, err
	}
	return &sharedState{
		obs:      observer,
		obsStore: obsStore,
		periods:  map[block.Hash32][]block.ActivationState{},
		records:  map[block.Hash32][]block.BlockRecord{},
	}, nil
}

// attachDriver opens one journal session over the shared state.
func attachDriver(chain *chain, journal *producer.Store, shared *sharedState) *driver {
	return &driver{chain: chain, journal: journal, shared: shared}
}

// nextDecision allocates the next native decision sequence.
func (d *driver) nextDecision() block.NativeDecisionRef {
	d.shared.decisions++
	return block.NativeDecisionRef{
		NodeInstanceID:   idOf("bor-node"),
		ProducerID:       idOf("bor-producer"),
		RunID:            idOf("bor-run"),
		DecisionSequence: d.shared.decisions,
	}
}

// observeMilestone records one native milestone whitelist observation at
// the canonical anchor of the given height and returns the markers for the
// next snapshots. A whitelist off the canonical chain is refused by the
// observer.
func (d *driver) observeMilestone(height uint64) (block.PolygonMarkers, error) {
	anchor, ok := d.chain.canonicalAnchor(height)
	if !ok {
		return block.PolygonMarkers{}, fmt.Errorf("fixture: no canonical anchor at %d", height)
	}
	decision := d.nextDecision()
	_, err := d.shared.obs.ObserveNative(finalityobserver.NativeInput{
		Head:     d.chain.headAnchor(),
		Decision: decision,
		Origin:   block.OriginNativeLive,
		MilestoneWhitelist: block.Some(finalityobserver.RetainedWhitelist{
			Kind:     finalityobserver.WhitelistMilestone,
			Anchor:   anchor,
			Decision: decision,
			Request:  finalityobserver.Request{Operation: finalityobserver.OpMilestoneLatest},
			Response: block.Some(finalityobserver.Response{
				Bytes:           []byte(fmt.Sprintf(`{"milestone":{"end":%d}}`, height)),
				RequestSequence: d.shared.observations + 1,
			}),
		}),
	})
	if err != nil {
		return block.PolygonMarkers{}, err
	}
	d.shared.observations++
	d.shared.obsStore.Sync()
	return d.shared.obs.Markers()
}

// snapshot builds the lifecycle snapshot of one event under the current
// observed head and markers.
func (d *driver) snapshot(decision block.NativeDecisionRef, markers block.PolygonMarkers) block.LifecycleSnapshot {
	return block.LifecycleSnapshot{
		NativeDecision:        decision,
		ObservedExecutionHead: d.chain.headAnchor(),
		Markers:               block.FinalityMarkers{Polygon: &markers},
	}
}

// metadata builds the typed block metadata of one observed block: Bor
// details with the sprint-end StateSync observation.
func metadata(b *blockInfo) block.BlockMetadata {
	details := block.BorBlockDetails{
		HeimdallCheckpoints: []block.HeimdallCheckpointRef{},
	}
	if b.StateSync > 0 || b.Number%sprintLength == 0 {
		count := b.StateSync
		details.StateSyncCount = block.Some(count)
		details.SprintEnd = block.Some(b.Number%sprintLength == 0)
		if count > 0 {
			details.StateSyncMode = block.Some(block.StateSyncEndOfBlockCommit)
		}
	}
	return block.BlockMetadata{
		Number:     b.Number,
		Hash:       b.Hash,
		ParentHash: b.Parent,
		Timestamp:  block.TimestampFromSeconds(1600000000 + b.Number),
		Header: block.Header{
			ParentHash: b.Parent,
			Number:     b.Number,
			SourceRLP:  []byte(fmt.Sprintf("rlp-%s", b.Hash)),
		},
		Details: block.FamilyDetails{Bor: &details},
	}
}

// eventRecord pairs one lifecycle event with the record it publishes.
type eventRecord struct {
	event  block.LifecycleEvent
	record *block.BlockRecord
}

// appliedEvent builds the Applied event and record of one fresh block
// inclusion under one native decision.
func (d *driver) appliedEvent(b *blockInfo, decision block.NativeDecisionRef, markers block.PolygonMarkers) eventRecord {
	snapshot := d.snapshot(decision, markers)
	ref := block.BlockActivationRef{
		Network:      polyNetwork,
		BlockHash:    b.Hash,
		ProducerID:   decision.ProducerID,
		RunID:        decision.RunID,
		ActivationID: block.ActivationID{DecisionSequence: decision.DecisionSequence},
	}
	d.shared.observations++
	record := block.BlockRecord{
		Activation:      ref,
		Admission:       block.ObservationRef{ProducerID: decision.ProducerID, RunID: decision.RunID, ObservationSequence: d.shared.observations},
		Metadata:        metadata(b),
		InitialSnapshot: snapshot,
	}
	return eventRecord{
		event:  block.LifecycleEvent{Activation: ref, Kind: block.EventApplied, Snapshot: snapshot},
		record: &record,
	}
}

// manifestEvent is the kind-dependent manifest descriptor of one event:
// an Applied carries its full snapshot, a StatusChanged its revision and
// the replacement snapshot, a Removed its native decision and reason —
// never an invented snapshot of a removal.
type manifestEvent struct {
	Activation     block.BlockActivationRef `json:"activation"`
	Kind           string                   `json:"kind"`
	Revision       uint64                   `json:"revision"`
	Snapshot       *block.LifecycleSnapshot `json:"snapshot,omitempty"`
	NativeDecision *block.NativeDecisionRef `json:"native_decision,omitempty"`
	Reason         string                   `json:"reason,omitempty"`
}

func toManifestEvent(ev block.LifecycleEvent) (manifestEvent, error) {
	if err := ev.Validate(); err != nil {
		return manifestEvent{}, err
	}
	out := manifestEvent{Activation: ev.Activation, Kind: ev.Kind.String(), Revision: ev.Revision}
	switch ev.Kind {
	case block.EventApplied, block.EventStatusChanged:
		snapshot := ev.Snapshot
		out.Snapshot = &snapshot
	case block.EventRemoved:
		decision := ev.NativeDecision
		out.NativeDecision = &decision
		out.Reason = ev.Reason.String()
	}
	return out, nil
}

func fromManifestEvent(m manifestEvent) (block.LifecycleEvent, error) {
	out := block.LifecycleEvent{Activation: m.Activation, Revision: m.Revision}
	switch m.Kind {
	case "APPLIED":
		out.Kind = block.EventApplied
	case "STATUS_CHANGED":
		out.Kind = block.EventStatusChanged
	case "REMOVED":
		out.Kind = block.EventRemoved
	default:
		return block.LifecycleEvent{}, fmt.Errorf("manifest: unknown event kind %q", m.Kind)
	}
	if m.Snapshot != nil {
		out.Snapshot = *m.Snapshot
	}
	if m.NativeDecision != nil {
		out.NativeDecision = *m.NativeDecision
	}
	if m.Reason != "" {
		found := false
		for _, name := range []string{"NATIVE_CANONICAL_CHANGE"} {
			if m.Reason == name {
				found = true
			}
		}
		if !found {
			return block.LifecycleEvent{}, fmt.Errorf("manifest: unknown removal reason %q", m.Reason)
		}
		out.Reason = block.RemovalReasonNativeCanonicalChange
	}
	return out, out.Validate()
}

// encodeManifest encodes the ordered descriptor set of one decision.
func encodeManifest(events []block.LifecycleEvent) ([]byte, error) {
	out := make([]manifestEvent, len(events))
	for i, ev := range events {
		m, err := toManifestEvent(ev)
		if err != nil {
			return nil, err
		}
		out[i] = m
	}
	return json.Marshal(out)
}

// decodeManifest decodes the ordered descriptor set of one decision.
func decodeManifest(raw []byte) ([]block.LifecycleEvent, error) {
	var manifest []manifestEvent
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	events := make([]block.LifecycleEvent, len(manifest))
	for i, m := range manifest {
		ev, err := fromManifestEvent(m)
		if err != nil {
			return nil, err
		}
		events[i] = ev
	}
	return events, nil
}

// commitBatch commits one native decision as one producer batch: the
// attempt data and the manifest are written as bounded contiguous chunks
// and the closing READY commit verifies the complete manifest stream. The
// same decision replayed returns the same batch (one batch_id), and a
// partial manifest never reaches READY.
func (d *driver) commitBatch(decision uint64, events []block.LifecycleEvent) (producer.Attempt, producer.Ready, error) {
	att, _, err := d.journal.AllocateBatch(decision, uint64(len(events)))
	if err != nil {
		return producer.Attempt{}, producer.Ready{}, err
	}
	// A fresh decision starts attempt generation 1; a replay of a decision
	// whose earlier attempt crashed mid-build starts the next generation
	// of the same batch. Either way the batch identity never changes.
	att, err = d.journal.StartAttempt(att.BatchSeq, uint64(len(events)))
	if err != nil {
		return producer.Attempt{}, producer.Ready{}, err
	}
	manifest, err := encodeManifest(events)
	if err != nil {
		return producer.Attempt{}, producer.Ready{}, err
	}
	var offset uint64
	for _, ev := range events {
		descriptor, err := toManifestEvent(ev)
		if err != nil {
			return producer.Attempt{}, producer.Ready{}, err
		}
		data, err := json.Marshal(descriptor)
		if err != nil {
			return producer.Attempt{}, producer.Ready{}, err
		}
		if err := d.journal.WriteAttemptData(att.BatchSeq, att.Generation, offset, data); err != nil {
			return producer.Attempt{}, producer.Ready{}, err
		}
		offset += uint64(len(data))
	}
	for off := 0; off < len(manifest); off += chunkSize {
		end := off + chunkSize
		if end > len(manifest) {
			end = len(manifest)
		}
		if err := d.journal.WriteAttemptManifest(att.BatchSeq, att.Generation, uint64(off), manifest[off:end]); err != nil {
			return producer.Attempt{}, producer.Ready{}, err
		}
	}
	ready, err := d.journal.CommitReady(att.BatchSeq, att.Generation, uint64(len(manifest)), []byte("poly-lifecycle-checkpoint"))
	if err != nil {
		return producer.Attempt{}, producer.Ready{}, err
	}
	return att, ready, nil
}

// fold applies the committed events to the lifecycle automaton: Applied
// opens the canonical period, Removed is terminal, StatusChanged replaces
// the evidence. The fold runs only after the READY commit, so a crashed
// build changes no lifecycle state.
func (d *driver) fold(events []eventRecord) error {
	for _, er := range events {
		hash := er.event.Activation.BlockHash
		periods := d.shared.periods[hash]
		switch er.event.Kind {
		case block.EventApplied:
			state, err := block.StartApplied(er.event)
			if err != nil {
				return err
			}
			d.shared.periods[hash] = append(periods, state)
			d.shared.records[hash] = append(d.shared.records[hash], *er.record)
		case block.EventStatusChanged, block.EventRemoved:
			if len(periods) == 0 {
				return fmt.Errorf("fixture: %s event without an applied period", er.event.Kind)
			}
			last := len(periods) - 1
			state, err := periods[last].Apply(er.event)
			if err != nil {
				return err
			}
			periods[last] = state
			d.shared.periods[hash] = periods
		}
	}
	return nil
}

// importBlocks extends the canonical chain: one import decision per block,
// each committed as its own batch and folded.
func (d *driver) importBlocks(markers block.PolygonMarkers, count int) (block.NativeDecisionRef, error) {
	var last block.NativeDecisionRef
	for i := 0; i < count; i++ {
		b := d.chain.extend("canon", sprintStateSync(d.chain.nextHeight()))
		decision := d.nextDecision()
		er := d.appliedEvent(b, decision, markers)
		if _, _, err := d.commitBatch(decision.DecisionSequence, []block.LifecycleEvent{er.event}); err != nil {
			return decision, err
		}
		if err := d.fold([]eventRecord{er}); err != nil {
			return decision, err
		}
		last = decision
	}
	return last, nil
}

// sprintStateSync returns the observed StateSync count of one block: the
// sprint end commits the pending records of the sprint.
func sprintStateSync(number uint64) uint64 {
	if number%sprintLength != 0 {
		return 0
	}
	return 1 + number/sprintLength // a growing sprint batch keeps the fixture non-trivial
}

// reorgDecision is one native reorg decision: every live activation of the
// removed suffix gets its Removed event (regardless of the transport
// PubAck of its batch), every promoted block gets its Applied event — a
// fresh period for a returning hash, a first period for a new block — and
// the whole ordered set commits as one batch.
func (d *driver) reorgDecision(tip *blockInfo, markers block.PolygonMarkers) (uint64, []eventRecord, error) {
	if _, _, err := d.chain.viewSwitch(tip); err != nil {
		return 0, nil, err
	}
	// The chain switches first, so the snapshots of the promoted blocks
	// observe the new head.
	removed, promoted := d.chain.switchTo(tip)
	decision := d.nextDecision()
	var events []eventRecord

	// Removed first, ascending by number: the canonical change is one
	// native decision for the whole suffix.
	for _, b := range removed {
		periods := d.shared.periods[b.Hash]
		if len(periods) == 0 {
			continue // the block was never applied (side storage)
		}
		last := periods[len(periods)-1]
		if last.State != block.StateActive && last.State != block.StatePendingApplied {
			continue // already terminal
		}
		events = append(events, eventRecord{event: block.LifecycleEvent{
			Activation:     last.Activation,
			Kind:           block.EventRemoved,
			Revision:       last.Revision + 1,
			NativeDecision: decision,
			Reason:         block.RemovalReasonNativeCanonicalChange,
		}})
	}

	// Then the promoted path, ascending: reapply of a removed hash opens a
	// new canonical period under this decision.
	for _, b := range promoted {
		periods := d.shared.periods[b.Hash]
		if len(periods) > 0 {
			// A returning hash: the old period must be terminal.
			old := periods[len(periods)-1]
			snapshot := d.snapshot(decision, markers)
			d.shared.observations++
			record, err := block.NewCanonicalPeriod(old, metadata(b), block.ObservationRef{
				ProducerID: decision.ProducerID, RunID: decision.RunID, ObservationSequence: d.shared.observations,
			}, snapshot)
			if err != nil {
				return 0, nil, err
			}
			events = append(events, eventRecord{
				event: block.LifecycleEvent{
					Activation: record.Activation,
					Kind:       block.EventApplied,
					Snapshot:   snapshot,
				},
				record: &record,
			})
			continue
		}
		events = append(events, d.appliedEvent(b, decision, markers))
	}

	return decision.DecisionSequence, events, nil
}

// commitReorg commits one prepared reorg decision as one batch and folds
// it, returning the batch sequence. A crash inside the build returns the
// error with no fold: the lifecycle state changes only after the READY
// commit.
func (d *driver) commitReorg(decision uint64, events []eventRecord) (uint64, error) {
	att, _, err := d.commitBatch(decision, eventsOf(events))
	if err != nil {
		return 0, err
	}
	return att.BatchSeq, d.fold(events)
}

func eventsOf(ers []eventRecord) []block.LifecycleEvent {
	out := make([]block.LifecycleEvent, len(ers))
	for i, er := range ers {
		out[i] = er.event
	}
	return out
}

// viewSwitch previews the switch of a branch without mutating the chain.
func (c *chain) viewSwitch(tip *blockInfo) ([]*blockInfo, []*blockInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	path := []*blockInfo{}
	for b := tip; b != nil; b = c.blocks[b.Parent] {
		if cur, ok := c.canon[b.Number]; ok && cur == b.Hash {
			break
		}
		path = append(path, b)
	}
	if len(path) == 0 {
		return nil, nil, fmt.Errorf("fixture: the branch is already canonical")
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	divergence := path[0].Number
	if divergence > c.head.Height+1 {
		return nil, nil, fmt.Errorf("fixture: branch diverges at %d beyond the head %d", divergence, c.head.Height)
	}
	var removed []*blockInfo
	for n := divergence; n <= c.head.Height; n++ {
		if h, ok := c.canon[n]; ok {
			removed = append(removed, c.blocks[h])
		}
	}
	return removed, path, nil
}
