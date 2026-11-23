package finalityobserver

import (
	"bytes"
	"errors"
	"math"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

func TestHeaderBlockIDCheckedMultiply(t *testing.T) {
	id, err := HeaderBlockID(3, 10000)
	if err != nil {
		t.Fatal(err)
	}
	// 30000 as a 32-byte big-endian integer.
	if id[31] != 30000&0xff || id[30] != 30000>>8 || id[0] != 0 {
		t.Fatalf("header block id %s", id)
	}
	if _, err := HeaderBlockID(0, 10000); !errors.Is(err, ErrMalformed) {
		t.Fatal("a zero checkpoint id is malformed")
	}
	if _, err := HeaderBlockID(3, 0); !errors.Is(err, ErrMalformed) {
		t.Fatal("a zero interval is malformed")
	}
	max, err := HeaderBlockID(math.MaxUint64, math.MaxUint64)
	if err != nil {
		t.Fatal(err)
	}
	if max.IsZero() {
		t.Fatal("the maximum u64 product must not wrap to zero")
	}
}

func TestHeaderBlocksCalldataShape(t *testing.T) {
	id, err := HeaderBlockID(testCheckpointID, testInterval)
	if err != nil {
		t.Fatal(err)
	}
	calldata := HeaderBlocksCalldata(id)
	if len(calldata) != 4+32 {
		t.Fatalf("calldata length %d", len(calldata))
	}
	if !bytes.Equal(calldata[:4], HeaderBlocksSelector[:]) {
		t.Fatalf("selector %x, want 41539d4a", calldata[:4])
	}
	if !bytes.Equal(calldata[4:], id[:]) {
		t.Fatal("the argument must be the 32-byte header block id")
	}
}

func TestDecodeHeaderBlockResultStrict(t *testing.T) {
	raw := headerBlockResult(testBorStart, testBorEnd, 42, testCheckpointRt, testProposer)
	res, err := DecodeHeaderBlockResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.Start != testBorStart || res.End != testBorEnd {
		t.Fatalf("range %d..%d", res.Start, res.End)
	}
	if res.Root != testCheckpointRt || res.Proposer != testProposer {
		t.Fatal("root and proposer must decode exactly")
	}
	var createdAt block.UInt256
	createdAt[31] = 42
	if res.CreatedAt != createdAt {
		t.Fatalf("created at %s", res.CreatedAt)
	}

	for _, tc := range []struct {
		name string
		raw  func() []byte
	}{
		{"short", func() []byte { return raw[:100] }},
		{"long", func() []byte { return append(append([]byte(nil), raw...), make([]byte, 32)...) }},
		{"dirty u64 upper half", func() []byte {
			out := append([]byte(nil), raw...)
			out[0] = 7
			return out
		}},
		{"end below start", func() []byte {
			return headerBlockResult(30000, 20001, 42, testCheckpointRt, testProposer)
		}},
		{"zero root", func() []byte {
			return headerBlockResult(1, 2, 42, block.Hash32{}, testProposer)
		}},
		{"zero proposer", func() []byte {
			return headerBlockResult(1, 2, 42, testCheckpointRt, block.Address20{})
		}},
		{"dirty address upper half", func() []byte {
			out := headerBlockResult(1, 2, 42, testCheckpointRt, testProposer)
			out[4*32] = 9
			return out
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeHeaderBlockResult(tc.raw()); !errors.Is(err, ErrMalformed) {
				t.Fatalf("error %v, want ErrMalformed", err)
			}
		})
	}
}

func TestStoreAppendDedupeAndConflict(t *testing.T) {
	store := NewMemoryStore()
	obs := Observation{ID: "obs-1", Binding: ObservationBinding{SourcesBinding: mustHash("s1")}}
	obs.Evidence = emptyEvidence()
	if err := store.Append(obs); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(obs); err != nil {
		t.Fatalf("the same observation is idempotent: %v", err)
	}
	other := obs
	other.Evidence.Checkpoint = UnavailableOf[Checkpoint](block.UnavailableCheckpointPending)
	if err := store.Append(other); !errors.Is(err, ErrConflict) {
		t.Fatalf("error %v, want ErrConflict", err)
	}
	store.Sync()
	got, err := store.Observation("obs-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(obs) {
		t.Fatal("the durable observation must equal the appended one")
	}
	if _, err := store.Observation("missing"); !errors.Is(err, ErrUnknownObservation) {
		t.Fatalf("error %v, want ErrUnknownObservation", err)
	}
}

func TestStoreSyncCrashFrontier(t *testing.T) {
	store := NewMemoryStore()
	obs := Observation{ID: "obs-1", Binding: ObservationBinding{SourcesBinding: mustHash("s1")}}
	obs.Evidence = emptyEvidence()
	if err := store.Append(obs); err != nil {
		t.Fatal(err)
	}
	observations, _ := store.Observations()
	if len(observations) != 0 {
		t.Fatal("an unsynced write is not durable")
	}
	ev := L1Checkpoint{
		L1Network:          testL1Network,
		SupportingNodeID:   mustID("l1-node"),
		SourcesBinding:     mustHash("s1"),
		RootChainAddress:   testRootChain,
		CheckpointID:       1,
		HeaderBlockID:      block.UInt256(mustHash("header-block-1")),
		ChildBlockInterval: testInterval,
		BorStart:           1,
		BorEnd:             2,
		CheckpointRoot:     testCheckpointRt,
		BorEndRef:          block.AnchorRef{Height: 2, Hash: mustHash("bor-end-2")},
		FinalizedL1Anchor:  block.AnchorRef{Height: 9, Hash: mustHash("l1-finalized-9")},
	}
	ev.BorRangeDigest = BorRangeDigest(ev.BorStart, ev.BorEnd, ev.BorEndRef)
	ev.Verification = FinalizedContractState{
		CallBlockHash:         ev.FinalizedL1Anchor.Hash,
		RootChainParamsDigest: ParamsDigest(ev.RootChainAddress, ev.ChildBlockInterval),
		CalldataDigest:        mustHash("calldata"),
		ResultDigest:          mustHash("result"),
		ResultProposer:        testProposer,
	}
	if err := store.SaveFrontier(ev); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Frontier(); ok {
		t.Fatal("an unsynced frontier is not durable")
	}
	store.Sync()
	frontier, ok, _ := store.Frontier()
	if !ok || frontier.BorEnd != 2 {
		t.Fatal("the synced frontier is durable")
	}
	// Power loss after the sync keeps exactly the synced state.
	late := Observation{ID: "obs-2", Binding: ObservationBinding{SourcesBinding: mustHash("s1")}}
	late.Evidence = emptyEvidence()
	if err := store.Append(late); err != nil {
		t.Fatal(err)
	}
	store.Crash()
	observations, _ = store.Observations()
	if len(observations) != 1 || observations[0].ID != "obs-1" {
		t.Fatalf("the crash must discard exactly the unsynced writes, got %d", len(observations))
	}
	if frontier, ok, _ := store.Frontier(); !ok || frontier.BorEnd != 2 {
		t.Fatal("the synced frontier survives the crash")
	}
}
