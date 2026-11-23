// E07-08.isolation (#75): the network isolation matrix. The three family
// slots share one NATS server — common infrastructure — and never a
// writable store: adding, stopping and restarting one network's adapter
// pair leaves the neighbors' writers, stores and stream contents
// untouched, a stopped network's stream stays frozen, and a restart
// recovers exactly its own obligations (TASK_DECOMPOSITION
// E07-08.isolation; TOPO, DELIVERY, EVID; REQ-OPS-02 single-writer/store
// isolation, REQ-CAP-10, TC08).
//
// The writer separation is proven, not assumed: a foreign manifest is
// rejected by the WAL's identity check, a foreign session fence by the
// gateway journal, and a foreign batch by the decision→batch allocator —
// the shared NATS never becomes a shared journal.
package acceptance

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// isolationServer builds the shared acceptance NATS: one stream per
// family, the common infrastructure of the topology.
func isolationServer(t *testing.T) *publisher.MemoryJetStream {
	t.Helper()
	configs := make([]publisher.StreamConfig, 0, len(networkSlots))
	for _, slot := range networkSlots {
		configs = append(configs, publisher.StreamConfig{
			Name: slot.stream, Subjects: []string{slot.prefix + ".>"},
			MaxMsgSize: acceptanceBudget,
		})
	}
	server, err := publisher.NewMemoryJetStream(configs...)
	if err != nil {
		t.Fatalf("shared server: %v", err)
	}
	return server
}

// stacksOf builds a stack per slot over the shared server with a common
// window.
func stacksOf(server *publisher.MemoryJetStream, windowStart, windowEnd uint64) []*acceptanceStack {
	out := make([]*acceptanceStack, 0, len(networkSlots))
	for _, slot := range networkSlots {
		out = append(out, newStack(slot, server, windowStart, windowEnd))
	}
	return out
}

// stackFor returns the stack of a slot name.
func stackFor(t *testing.T, stacks []*acceptanceStack, name string) *acceptanceStack {
	t.Helper()
	for _, h := range stacks {
		if h.slot.name == name {
			return h
		}
	}
	t.Fatalf("no stack of %q in the topology", name)
	return nil
}

// TestIsolationAddNetworkLeavesNeighborsUntouched verifies the add column
// of the matrix: ETH and BSC run, POLY is added to the shared topology —
// its first delivery writes only its own stream, and the neighbors' stream
// bytes stay identical to their snapshots (TC08; REQ-OPS-02).
func TestIsolationAddNetworkLeavesNeighborsUntouched(t *testing.T) {
	server := isolationServer(t)
	eth := newStack(slotByNameOrFatal(t, "ETH"), server, 400, 420)
	bsc := newStack(slotByNameOrFatal(t, "BSC"), server, 400, 420)
	for _, h := range []*acceptanceStack{eth, bsc} {
		d, err := h.source.admitLive(fixtureBlock(400, 2, 0))
		if err != nil {
			t.Fatalf("%s: admit: %v", h.slot.name, err)
		}
		h.deliverDecision(t, server, d)
	}
	ethSnapshot := snapshotMessages(server, eth.slot)
	bscSnapshot := snapshotMessages(server, bsc.slot)

	// POLY joins the running topology.
	poly := newStack(slotByNameOrFatal(t, "POLY"), server, 400, 420)
	d, err := poly.source.admitLive(fixtureBlock(400, 3, 1))
	if err != nil {
		t.Fatalf("POLY: admit: %v", err)
	}
	poly.deliverDecision(t, server, d)

	assertSnapshotUnchanged(t, eth.slot, server, ethSnapshot)
	assertSnapshotUnchanged(t, bsc.slot, server, bscSnapshot)
	verifyStream(t, server, poly.slot, poly.source).mustBeClean(t, poly.slot)
	verifyStream(t, server, eth.slot, eth.source).mustBeClean(t, eth.slot)
	verifyStream(t, server, bsc.slot, bsc.source).mustBeClean(t, bsc.slot)

	// The message identities never collide across networks: the network
	// digest is part of every Nats-Msg-Id, so the shared server's dedup
	// never merges two networks' frames.
	owners := make(map[string]string)
	for _, h := range []*acceptanceStack{eth, bsc, poly} {
		for _, msg := range messagesOf(server, h.slot) {
			if other, held := owners[msg.MsgID]; held && other != h.slot.name {
				t.Fatalf("message %s of %s is also claimed by %s", msg.MsgID, h.slot.name, other)
			}
			owners[msg.MsgID] = h.slot.name
		}
	}
}

// TestIsolationStopAndRestartOneNetwork verifies the stop and restart
// columns: stopping BSC's adapter pair freezes only its stream; ETH and
// POLY keep delivering through the shared server; restarting BSC recovers
// exactly its own obligations — its own messages, nothing duplicated with
// foreign bytes, nothing of the neighbors.
func TestIsolationStopAndRestartOneNetwork(t *testing.T) {
	server := isolationServer(t)
	stacks := stacksOf(server, 410, 430)
	for _, h := range stacks {
		d, err := h.source.admitLive(fixtureBlock(410, 2, 0))
		if err != nil {
			t.Fatalf("%s: admit: %v", h.slot.name, err)
		}
		h.deliverDecision(t, server, d)
	}
	bsc := stackFor(t, stacks, "BSC")
	frozen := snapshotMessages(server, bsc.slot)

	// The operator stops BSC; the neighbors keep running.
	bsc.stop()
	for _, name := range []string{"ETH", "POLY"} {
		h := stackFor(t, stacks, name)
		d, err := h.source.admitLive(fixtureBlock(411, 3, 0))
		if err != nil {
			t.Fatalf("%s: admit after the BSC stop: %v", name, err)
		}
		h.deliverDecision(t, server, d)
	}
	assertSnapshotUnchanged(t, bsc.slot, server, frozen)
	for _, name := range []string{"ETH", "POLY"} {
		h := stackFor(t, stacks, name)
		verifyStream(t, server, h.slot, h.source).mustBeClean(t, h.slot)
	}

	// A live decision admitted to the stopped network before its restart
	// is a saved obligation: the restart finishes it, in order.
	if _, err := bsc.source.admitLive(fixtureBlock(411, 2, 0)); err != nil {
		t.Fatalf("BSC: admit while stopped: %v", err)
	}
	if _, err := bsc.source.admitLive(fixtureBlock(412, 1, 0)); err != nil {
		t.Fatalf("BSC: admit 412: %v", err)
	}
	bsc.restart()
	bsc.deliverAll(t, server)

	verifyStream(t, server, bsc.slot, bsc.source).mustBeClean(t, bsc.slot)
	// The recovery republished the already-stored frames as exact bytes:
	// the stream holds one copy per identity and the frozen prefix is
	// untouched.
	current := snapshotMessages(server, bsc.slot)
	for id, payload := range frozen {
		got, ok := current[id]
		if !ok || string(got) != string(payload) {
			t.Fatalf("BSC: the restart rewrote the frozen message %s", id)
		}
	}
	if len(current) == len(frozen) {
		t.Fatalf("BSC: the restart delivered nothing of its saved obligations")
	}
}

// TestIsolationNoSharedWALWriters proves the writer separation: each
// network's WAL rejects a foreign manifest by identity, each gateway
// journal rejects a foreign session fence, and the decision→batch
// allocator rejects a foreign batch — the three networks can never write
// one another's stores even by direct misuse.
func TestIsolationNoSharedWALWriters(t *testing.T) {
	server := isolationServer(t)
	stacks := stacksOf(server, 440, 460)
	eth := stackFor(t, stacks, "ETH")
	bsc := stackFor(t, stacks, "BSC")

	// Every slot owns a distinct writer identity and distinct stores.
	identities := make(map[proto.ID16]string)
	stores := make(map[string]int)
	for _, h := range stacks {
		if other, held := identities[h.slot.producer]; held {
			t.Fatalf("slots %s and %s share the writer identity %x", other, h.slot.name, h.slot.producer)
		}
		identities[h.slot.producer] = h.slot.name
		stores[fmt.Sprintf("%p", h.mem)]++
		stores[fmt.Sprintf("%p", h.gw)]++
	}
	for key, count := range stores {
		if count > 1 {
			t.Fatalf("two networks share the store %s", key)
		}
	}

	// The WAL identity check: BSC's manifest can never commit on ETH's
	// producer store.
	if _, err := eth.prod.AllocateIntent(); err != nil {
		t.Fatalf("ETH intent: %v", err)
	}
	foreign := delivery.BatchIdentity{
		Batch:          delivery.BatchID{Producer: bsc.slot.producer, Run: bsc.slot.run, Seq: 1},
		ManifestDigest: proto.Hash32{0x1}, BindingDigest: eth.bindingDigest,
	}
	if err := eth.prod.CommitReady(1, foreign); err == nil {
		t.Fatalf("ETH's WAL accepted the BSC manifest")
	}

	// The gateway journal fence: BSC's session can never drive ETH's
	// journal (another producer/run is fenced off).
	bscFence := bsc.fence(1)
	if err := eth.gw.ImportClosed(bscFence, delivery.SealedPlan{Identity: foreign}, false); err == nil {
		t.Fatalf("ETH's gateway journal accepted the BSC session fence")
	} else if !errors.Is(err, delivery.ErrFenced) {
		t.Fatalf("the foreign-fence refusal is %v, want the fenced sentinel", err)
	}

	// The allocator identity: ETH's batch can never be adopted by BSC's
	// decision→batch allocation.
	ethBatch, _, err := eth.source.allocator.EnsureBatch(1)
	if err != nil {
		t.Fatalf("ETH allocation: %v", err)
	}
	if err := bsc.source.allocator.AdoptBatch(1, semantic.BatchID(ethBatch)); err == nil {
		t.Fatalf("BSC's allocator adopted an ETH batch")
	}

	// The journals never answer another network's batch: ETH's gateway
	// holds no record of the BSC run.
	if _, ok := eth.gw.Batch(eth.fence(1), delivery.BatchID{
		Producer: bsc.slot.producer, Run: bsc.slot.run, Seq: 1}); ok {
		t.Fatalf("ETH's gateway journal answers a BSC batch")
	}
}

// TestIsolationConcurrentNetworksDeliverIndependently runs the three
// networks' deliveries interleaved on the shared server and verifies each
// stream against its own oracle: concurrency on common infrastructure
// never mixes identities, routes or content (TC08; REQ-DEV-05 per-profile
// separation).
func TestIsolationConcurrentNetworksDeliverIndependently(t *testing.T) {
	server := isolationServer(t)
	stacks := stacksOf(server, 470, 490)
	// Round-robin interleaving of the three networks' decisions.
	pending := make([][]decisionSpec, len(stacks))
	for i, h := range stacks {
		first, err := h.source.admitLive(fixtureBlock(470, 2, 0))
		if err != nil {
			t.Fatalf("%s: admit: %v", h.slot.name, err)
		}
		second, err := h.source.admitLive(fixtureBlock(471, 3, 0))
		if err != nil {
			t.Fatalf("%s: admit: %v", h.slot.name, err)
		}
		pair, err := h.source.reorgAt(471, reorgFixtureBlock(471, 1, 0))
		if err != nil {
			t.Fatalf("%s: reorg: %v", h.slot.name, err)
		}
		pending[i] = append([]decisionSpec{first, second}, pair...)
	}
	for round := 0; round < 4; round++ {
		for i := range stacks {
			h := stacks[i]
			d := pending[i][round]
			h.deliverDecision(t, server, d)
		}
	}
	for _, h := range stacks {
		verifyStream(t, server, h.slot, h.source).mustBeClean(t, h.slot)
	}
}

// slotByNameOrFatal resolves a slot by family name.
func slotByNameOrFatal(t *testing.T, name string) networkSlot {
	t.Helper()
	slot, err := slotByName(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return slot
}
