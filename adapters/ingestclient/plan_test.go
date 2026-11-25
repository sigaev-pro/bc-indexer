package ingestclient

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/adapters/ingestclient/ingesttest"
	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// staged is one READY batch of one block event, with what the plan is built from.
type staged struct {
	store   *producer.Store
	seq     uint64
	ready   producer.Ready
	binding destination.Binding
	network proto.NetworkIdentity
	seed    Seed
}

// block is the block fact of the test records: small enough for a few fragments.
func block(extra []byte) ingesttest.Block {
	return ingesttest.Block{Number: 49123456, Hash: sha256.Sum256([]byte("block hash")),
		ParentHash: sha256.Sum256([]byte("parent")), Timestamp: 1732000000, TxCount: 12, Extra: extra}
}

// stage writes one READY batch over a fresh store and parses a seed of the
// reservation the driver checks.
func stage(t *testing.T, extra []byte) staged {
	t.Helper()
	binding := ingesttest.Binding(ingesttest.Network())
	store := ingesttest.OpenStore(t, storage.NewMemory(), binding)
	seq, ready := ingesttest.Stage(t, store, 1, binding.Network, block(extra))
	seedBytes := contractSeed{reservation: [16]byte{0xAA}, epoch: [16]byte{0xBB},
		producer: store.Identity().ProducerID, run: store.Identity().RunID, seq: seq,
		manifest: ready.ManifestDigest, events: 1, sequenceBase: 1, binding: binding.DescriptorDigest,
		frameLimit: 2097152, versions: pinned}.bytes()
	seed, err := ParseSeed(seedBytes)
	if err != nil {
		t.Fatal(err)
	}
	return staged{store: store, seq: seq, ready: ready, binding: binding, network: binding.Network, seed: seed}
}

// readAll collects one stream of the batch.
func readAll(t *testing.T, s staged, read func(seq, generation uint64, visit producer.ChunkVisitor) error) []byte {
	t.Helper()
	var out []byte
	if err := read(s.seq, s.ready.Attempt, func(_ uint64, chunk []byte) error {
		out = append(out, chunk...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDecodeManifestReadsTheBuilderBytes(t *testing.T) {
	binding := ingesttest.Binding(ingesttest.Network())
	store := ingesttest.OpenStore(t, storage.NewMemory(), binding)
	// Two events in one batch: the manifest holds both in ordinal order.
	seq, ready := ingesttest.Stage(t, store, 4, binding.Network, block(nil), block([]byte("second")))
	var manifest []byte
	if err := store.AttemptManifest(seq, ready.Attempt, func(_ uint64, chunk []byte) error {
		manifest = append(manifest, chunk...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	events, err := decodeManifest(manifest)
	if err != nil || len(events) != 2 {
		t.Fatalf("decodeManifest: %d events, %v", len(events), err)
	}
	for ordinal := range events {
		want, err := ingesttest.BuildBlockEvent(uint64(ordinal), proto.EventID(store.EventID(1+uint64(ordinal))), binding.Network,
			block([][]byte{nil, []byte("second")}[ordinal]))
		if err != nil {
			t.Fatal(err)
		}
		got := events[ordinal]
		if got.Ordinal != uint64(ordinal) || got.EventID != want.Descriptor.EventID ||
			got.RecordDigest != want.Descriptor.RecordDigest || got.RecordLength != want.Descriptor.RecordLength ||
			got.Family != uint16(proto.FamilyBlock) || got.Kind != uint16(proto.KindApplied) ||
			len(got.Fragments) != len(want.FragmentDescriptors) {
			t.Fatalf("event %d:\n got %+v\nwant %+v", ordinal, got, want.Descriptor)
		}
		for j := range got.Fragments {
			if got.Fragments[j] != want.FragmentDescriptors[j] {
				t.Fatalf("event %d fragment %d differs from the READY descriptor", ordinal, j)
			}
		}
	}
	if manifestHeaderBytes != 30 {
		t.Fatalf("the manifest header is %d bytes, want 30: the domain, the zero byte and the count", manifestHeaderBytes)
	}
	if uint64(len(manifest)) != ready.ManifestLength {
		t.Fatalf("manifest of %d bytes, READY marker names %d", len(manifest), ready.ManifestLength)
	}
}

func TestDecodeManifestRejectsWhatIsNotTheStream(t *testing.T) {
	s := stage(t, nil)
	manifest := readAll(t, s, s.store.AttemptManifest)
	// The descriptor bytes of the event: everything after the header.
	descriptors := len(manifest) - int(manifestHeaderBytes)
	cases := []struct {
		name     string
		manifest []byte
	}{
		{"another domain", concatBytes([]byte("extractor.manifest.v3"), manifest[len(codec.ManifestDomain):])},
		{"no zero byte", concatBytes(manifest[:len(codec.ManifestDomain)], []byte{1}, manifest[len(codec.ManifestDomain)+1:])},
		{"empty", manifest[:int(manifestHeaderBytes)-1]},
		{"no events with descriptors", concatBytes(manifest[:int(manifestHeaderBytes)-8], make([]byte, 8), manifest[int(manifestHeaderBytes):])},
		{"count beyond the bytes", concatBytes(manifest[:int(manifestHeaderBytes)-8], binary.BigEndian.AppendUint64(nil, 1<<40), manifest[int(manifestHeaderBytes):])},
		{"count short of the events", concatBytes(manifest[:int(manifestHeaderBytes)-8], binary.BigEndian.AppendUint64(nil, 0), manifest[int(manifestHeaderBytes):])},
		{"truncated descriptor", manifest[:len(manifest)-1]},
		{"truncated to the header", manifest[:int(manifestHeaderBytes)]},
		{"trailing bytes", concatBytes(manifest, []byte{0})},
	}
	for _, tc := range cases {
		if _, err := decodeManifest(tc.manifest); !errors.Is(err, ErrJournalData) {
			t.Fatalf("%s: %v, want an error of ErrJournalData", tc.name, err)
		}
	}
	// An ordinal that breaks the continuity and an event id that repeats.
	shifted := append([]byte(nil), manifest...)
	shifted[int(manifestHeaderBytes)] = 1 // the ordinal of the first event
	if _, err := decodeManifest(shifted); !errors.Is(err, ErrJournalData) {
		t.Fatalf("ordinal 1 at position 0: %v", err)
	}
	if descriptors <= 0 {
		t.Fatal("the manifest holds no descriptor")
	}
	if err := verifyManifest(manifest, s.ready); err != nil {
		t.Fatalf("verifyManifest of the stored manifest: %v", err)
	}
	for name, ready := range map[string]producer.Ready{
		"another length": {ManifestLength: s.ready.ManifestLength + 1, ManifestDigest: s.ready.ManifestDigest, EventCount: 1},
		"another digest": {ManifestLength: s.ready.ManifestLength, ManifestDigest: [32]byte{7}, EventCount: 1},
		"another count":  {ManifestLength: s.ready.ManifestLength, ManifestDigest: s.ready.ManifestDigest, EventCount: 2},
	} {
		if err := verifyManifest(manifest, ready); !errors.Is(err, ErrJournalData) {
			t.Fatalf("%s: %v, want ErrJournalData", name, err)
		}
	}
	flipped := append([]byte(nil), manifest...)
	flipped[len(flipped)-1] ^= 1
	if err := verifyManifest(flipped, s.ready); !errors.Is(err, ErrJournalData) {
		t.Fatalf("a changed manifest byte: %v, want ErrJournalData", err)
	}
}

func TestPlanRoundTrip(t *testing.T) {
	s := stage(t, nil)
	manifest := readAll(t, s, s.store.AttemptManifest)
	data := readAll(t, s, s.store.AttemptData)
	events, err := decodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := layout(events, s.seed, s.binding)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := buildFrames(events, plan, data, s.seed, s.binding)
	if err != nil {
		t.Fatal(err)
	}
	// One COMPLETE frame per fragment, each the canonical wrapper of its
	// fragment with the exact NATS header.
	headerBytes, err := proto.NatsHeaderSize(s.binding.Stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != len(events[0].Fragments) || len(frames) != len(plan.Entries) {
		t.Fatalf("%d frames for %d fragments and %d entries", len(frames), len(events[0].Fragments), len(plan.Entries))
	}
	for i, frame := range frames {
		if frame.Entry.Form != proto.FormComplete || frame.Part != 0 || frame.Entry.FragmentIndex != uint64(i) ||
			frame.Entry.Ordinal != 0 || frame.Entry.Capacity != events[0].Fragments[i].EncodedLength {
			t.Fatalf("frame %d is not the COMPLETE entry of fragment %d: %+v", i, i, frame.Entry)
		}
		decoded, err := proto.DecodeTransportFrame(frame.Encoded)
		if err != nil || decoded.Complete == nil {
			t.Fatalf("frame %d does not decode: %v", i, err)
		}
		// The fragment bytes of the frame are the next bytes of the attempt.
		fragment := firstFragment(t, s, i)
		if !bytes.Equal(decoded.Complete.FragmentBytes, fragment) || decoded.FragmentDigest != events[0].Fragments[i].FragmentSHA256 ||
			decoded.EventID != events[0].EventID || decoded.ChainSequence != s.seed.SequenceBase ||
			decoded.PublicationEpoch != s.seed.IssuerEpoch || decoded.FragmentIndex != uint64(i) {
			t.Fatalf("frame %d does not carry its fragment under the reservation", i)
		}
		if uint64(len(frame.Header)) != headerBytes || !bytes.HasPrefix(frame.Header, []byte("NATS/1.0\r\nNats-Msg-Id: ex2.")) ||
			!bytes.HasSuffix(frame.Header, []byte("\r\nNats-Expected-Stream: "+s.binding.Stream+"\r\n\r\n")) {
			t.Fatalf("frame %d carries other NATS header bytes than the contract: %q", i, frame.Header)
		}
		if size, _ := decoded.EncodedFrameSize(); size != uint64(len(frame.Encoded)) {
			t.Fatalf("frame %d: encoded size %d, %d bytes", i, size, len(frame.Encoded))
		}
	}
	encoded := encodePlan(frames)
	stored, err := decodePlan(encoded)
	if err != nil || len(stored) != len(frames) {
		t.Fatalf("decodePlan: %d frames, %v", len(stored), err)
	}
	for i := range frames {
		if !bytes.Equal(stored[i].Encoded, frames[i].Encoded) || !bytes.Equal(stored[i].Header, frames[i].Header) {
			t.Fatalf("frame %d changed on the way through the plan payload", i)
		}
	}
	checked, err := checkPlan(stored, events, plan, s.seed, s.binding)
	if err != nil || len(checked) != len(frames) {
		t.Fatalf("checkPlan of the round trip: %d frames, %v", len(checked), err)
	}
	for i := range checked {
		if !bytes.Equal(checked[i].Encoded, frames[i].Encoded) || checked[i].Part != 0 {
			t.Fatalf("checked frame %d differs", i)
		}
	}

	// The spooled plan is refused when it is not what the manifest, the
	// reservation and the binding determine.
	damaged := func(change func([]storedFrame) []storedFrame) []storedFrame {
		copies := make([]storedFrame, len(stored))
		for i := range stored {
			copies[i] = storedFrame{Header: append([]byte(nil), stored[i].Header...), Encoded: append([]byte(nil), stored[i].Encoded...)}
		}
		return change(copies)
	}
	cases := map[string][]storedFrame{
		"a frame lost":   damaged(func(f []storedFrame) []storedFrame { return f[1:] }),
		"a frame extra":  damaged(func(f []storedFrame) []storedFrame { return append(f, f[0]) }),
		"a byte changed": damaged(func(f []storedFrame) []storedFrame { f[0].Encoded[len(f[0].Encoded)-1] ^= 1; return f }),
		"a header changed": damaged(func(f []storedFrame) []storedFrame {
			f[0].Header[13] ^= 1
			return f
		}),
		"a frame not canonical": damaged(func(f []storedFrame) []storedFrame { f[0].Encoded = []byte("not a frame"); return f }),
	}
	for name, frames := range cases {
		if _, err := checkPlan(frames, events, plan, s.seed, s.binding); !errors.Is(err, ErrJournalData) {
			t.Fatalf("%s: %v, want ErrJournalData", name, err)
		}
	}
	// The same frames under another reservation or binding are refused.
	other := s.seed
	other.SequenceBase = 99
	if _, err := checkPlan(stored, events, plan, other, s.binding); !errors.Is(err, ErrJournalData) {
		t.Fatalf("another sequence base: %v", err)
	}
	// The layout digest is the one the gateway verifies at CLOSED.
	digest, err := proto.LayoutManifestDigest(PlanDigest(s.seed.raw), plan.Entries)
	if err != nil || digest == ([32]byte{}) {
		t.Fatalf("layout manifest digest: %x %v", digest, err)
	}

	// decodePlan refuses a truncated record.
	for _, cut := range []int{1, 7, 8, len(encoded) - 1} {
		if _, err := decodePlan(encoded[:cut]); !errors.Is(err, ErrJournalData) {
			t.Fatalf("plan payload cut at %d: %v", cut, err)
		}
	}
	// An empty plan has no frames and is well formed.
	if empty, err := decodePlan(nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty plan: %v %d", err, len(empty))
	}
}

func TestPlanPayloadRecordShape(t *testing.T) {
	// One frame record: u32be(len(header)) || u32be(len(frame)) || header || frame.
	frames := []planFrame{{Encoded: []byte("FRAME"), Header: []byte("HEADER")}, {Encoded: []byte("F2"), Header: []byte("H2")}}
	payload := encodePlan(frames)
	want := concatBytes(
		binary.BigEndian.AppendUint32(nil, 6), binary.BigEndian.AppendUint32(nil, 5), []byte("HEADER"), []byte("FRAME"),
		binary.BigEndian.AppendUint32(nil, 2), binary.BigEndian.AppendUint32(nil, 2), []byte("H2"), []byte("F2"))
	if !bytes.Equal(payload, want) {
		t.Fatalf("plan payload %q, want %q", payload, want)
	}
}

func TestPlanRefusesAFragmentTheBudgetCannotCarry(t *testing.T) {
	s := stage(t, nil)
	events, err := decodeManifest(readAll(t, s, s.store.AttemptManifest))
	if err != nil {
		t.Fatal(err)
	}
	small := s.seed
	small.FrameLimit = 250 // below the NATS header and a one-byte part
	if _, err := layout(events, small, s.binding); !errors.Is(err, ErrLimitIncompatible) {
		t.Fatalf("layout under a budget of 250: %v, want ErrLimitIncompatible", err)
	}
	if info, _ := s.store.Batch(s.seq); info.State != producer.StateReady {
		t.Fatalf("the batch is %s, want READY: nothing was sealed", info.State)
	}
}

func TestBuildFramesChecksTheAttemptData(t *testing.T) {
	s := stage(t, nil)
	events, err := decodeManifest(readAll(t, s, s.store.AttemptManifest))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := layout(events, s.seed, s.binding)
	if err != nil {
		t.Fatal(err)
	}
	data := readAll(t, s, s.store.AttemptData)
	if _, err := buildFrames(events, plan, data, s.seed, s.binding); err != nil {
		t.Fatalf("the stored attempt data builds: %v", err)
	}
	short := data[:len(data)-1]
	if _, err := buildFrames(events, plan, short, s.seed, s.binding); !errors.Is(err, ErrJournalData) {
		t.Fatalf("attempt data one byte short: %v", err)
	}
	long := append(append([]byte(nil), data...), 0)
	if _, err := buildFrames(events, plan, long, s.seed, s.binding); !errors.Is(err, ErrJournalData) {
		t.Fatalf("attempt data one byte long: %v", err)
	}
	flipped := append([]byte(nil), data...)
	flipped[len(flipped)-1] ^= 1
	if _, err := buildFrames(events, plan, flipped, s.seed, s.binding); !errors.Is(err, ErrJournalData) {
		t.Fatalf("a fragment byte changed: %v", err)
	}
}

// firstFragment returns the i-th fragment of the staged record.
func firstFragment(t *testing.T, s staged, i int) []byte {
	t.Helper()
	event, err := ingesttest.BuildBlockEvent(0, proto.EventID(s.store.EventID(1)), s.network, block(nil))
	if err != nil || i >= len(event.Fragments) {
		t.Fatalf("fragment %d: %v", i, err)
	}
	return event.Fragments[i]
}
