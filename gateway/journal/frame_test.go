package journal

import (
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// testManifest builds a real manifest with the producer's own builder: one
// event per slice of fragment lengths, in ordinal order. A record has at
// least three fragments (header, content, seal), so every event carries at
// least that many.
func testManifest(t *testing.T, events ...[]uint64) []byte {
	t.Helper()
	b := codec.NewManifestBuilder()
	for ordinal, lengths := range events {
		frags := make([]proto.FragmentDescriptor, len(lengths))
		var total uint64
		for i, n := range lengths {
			frags[i] = proto.FragmentDescriptor{FragmentIndex: uint64(i), EncodedLength: n,
				FragmentSHA256: proto.Hash32(sha256.Sum256([]byte("fragment")))}
			total += n
		}
		eventID := proto.NewEventID([16]byte{1}, [16]byte{2}, uint64(ordinal)+1)
		event := proto.EventDescriptor{Ordinal: uint64(ordinal), EventID: eventID,
			Family: proto.FamilyTx, Kind: proto.KindApplied, RecordLength: total,
			RecordDigest: proto.Hash32(sha256.Sum256([]byte("record"))), FragmentCount: uint64(len(frags))}
		var err error
		if event.FragmentManifestDigest, err = proto.FragmentManifestDigest(eventID, frags); err != nil {
			t.Fatal(err)
		}
		if err := b.AddEvent(event, frags); err != nil {
			t.Fatalf("manifest event %d: %v", ordinal, err)
		}
	}
	manifest, err := b.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

// testFrame encodes one real transport frame of a layout entry with the
// producer's wrapper: the canonical encoding of the frame that carries data.
func testFrame(sequenceBase uint64, entry LayoutEntry, fragmentCount, frame uint64, data []byte) []byte {
	f := proto.TransportFrame{
		TransportVersion:    proto.TransportVersion,
		Network:             proto.NetworkIdentity{Family: proto.NetworkFamilyEVM},
		EventID:             proto.NewEventID([16]byte{1}, [16]byte{2}, entry.Ordinal+1),
		RecordDigest:        proto.Hash32(sha256.Sum256([]byte("record"))),
		PublicationEpoch:    proto.ID16{1},
		ChainSequence:       sequenceBase + entry.Ordinal,
		FragmentIndex:       entry.FragmentIndex,
		FragmentCount:       fragmentCount,
		FragmentDigest:      proto.Hash32(sha256.Sum256(data)),
		RecordFormatVersion: proto.RecordFormatVersion,
	}
	if entry.Form == 0 {
		f.Complete = &proto.CompleteFragment{FragmentBytes: data}
	} else {
		f.Part = &proto.FragmentPart{PartIndex: frame, PartCount: entry.PartCount,
			FragmentTotalBytes: entry.FragmentTotal, Data: data}
	}
	encoded, err := f.Encode()
	if err != nil {
		panic("test frame: " + err.Error())
	}
	return encoded
}

// testPlanFrames encodes the three frames of the plan fixture: event 0
// fragment 0 as one COMPLETE frame, event 1 fragment 0 as two PART frames.
func testPlanFrames(sequenceBase uint64, entries []LayoutEntry) []frameFixture {
	return []frameFixture{
		{FrameAddr{0, 0, 0}, FrameMeta{Form: 0, Capacity: 6, PartCount: 1, FragmentTotal: 6},
			testFrame(sequenceBase, entries[0], 3, 0, []byte("abcdef"))},
		{FrameAddr{1, 0, 0}, FrameMeta{Form: 1, Capacity: 4, PartCount: 2, FragmentTotal: 6},
			testFrame(sequenceBase, entries[1], 3, 0, []byte("wxyz"))},
		{FrameAddr{1, 0, 1}, FrameMeta{Form: 1, Capacity: 4, PartCount: 2, FragmentTotal: 6},
			testFrame(sequenceBase, entries[1], 3, 1, []byte("uv"))},
	}
}

// The journal's size of a declared frame is the exact size of the canonical
// encoding the producer writes: checked against Encode of protocol/proto,
// the single owner of the formula, for both forms and for varint lengths
// that grow with the sequence, the fragment index and the fragment count.
func TestFrameSizeIsTheEncodedTransportFrame(t *testing.T) {
	var scratch []byte
	for _, fragmentCount := range []uint64{3, 127, 128, 16383, 16384} {
		for _, sequenceBase := range []uint64{1, 127, 128, 1 << 40} {
			entries := []LayoutEntry{
				{Ordinal: 0, FragmentIndex: 0, Form: 0, Capacity: 6, PartCount: 1, FragmentTotal: 6},
				{Ordinal: 1, FragmentIndex: 200, Form: 1, Capacity: 300, PartCount: 3, FragmentTotal: 650},
			}
			for i := range entries {
				for frame := uint64(0); frame < entries[i].FrameCount(); frame++ {
					data := make([]byte, entries[i].FrameData(frame))
					want := len(testFrame(sequenceBase, entries[i], fragmentCount, frame, data))
					got, err := frameEncodedSize(&scratch, sequenceBase, entries[i], fragmentCount, frame)
					if err != nil || got != uint64(want) {
						t.Fatalf("count %d base %d entry %d frame %d: journal size %d, encoded size %d (%v)",
							fragmentCount, sequenceBase, i, frame, got, want, err)
					}
				}
			}
		}
	}
	// The data of a frame is far smaller than its encoding: the S8 probe
	// measured a 137-byte fragment as a 353-byte frame.
	complete := LayoutEntry{Ordinal: 0, FragmentIndex: 0, Form: 0, Capacity: 137, PartCount: 1, FragmentTotal: 137}
	size, err := frameEncodedSize(&scratch, 1, complete, 4, 0)
	if err != nil || size <= 137 || size != uint64(len(testFrame(1, complete, 4, 0, make([]byte, 137)))) {
		t.Fatalf("complete frame of a 137-byte fragment: size %d err %v", size, err)
	}
}

// A frame larger than one 64 KiB data chunk still arrives: in several chunks
// whose offsets count bytes inside the frame. The chunk bound and the
// overrun rule are unchanged.
func TestFrameAboveChunkBoundArrivesInPieces(t *testing.T) {
	j, _ := newJournal(t)
	id := testBatch(1)
	manifest := testManifest(t, []uint64{proto.FragmentMaxBytes, 3, 3})
	digest := sha256.Sum256(manifest)
	res := prepareBatch(t, j, id, manifest, 1)
	entry := LayoutEntry{Ordinal: 0, FragmentIndex: 0, Form: 0,
		Capacity: proto.FragmentMaxBytes, PartCount: 1, FragmentTotal: proto.FragmentMaxBytes}
	meta := FrameMeta{Form: 0, Capacity: proto.FragmentMaxBytes, PartCount: 1, FragmentTotal: proto.FragmentMaxBytes}
	plan := PlanDigest(res.Seed)
	if _, err := j.BeginPublish(id, res.Seed, plan, computeLayoutDigest(plan, []LayoutEntry{entry}), 1); err != nil {
		t.Fatal(err)
	}
	frame := testFrame(res.SequenceBase, entry, 3, 0, make([]byte, proto.FragmentMaxBytes))
	if uint64(len(frame)) <= MaxDataChunkBytes {
		t.Fatalf("the frame of a %d-byte fragment encodes to %d bytes, want more than one %d-byte chunk",
			proto.FragmentMaxBytes, len(frame), MaxDataChunkBytes)
	}
	addr := FrameAddr{0, 0, 0}

	// One chunk cannot carry the whole frame.
	whole, err := j.PublishChunk(id, plan, addr, &meta, 0, sha256.Sum256(frame), frame)
	if !errors.Is(err, ErrChunkTooLarge) || whole != 0 {
		t.Fatalf("whole frame in one chunk: next=%d err=%v, want ErrChunkTooLarge", whole, err)
	}
	// It arrives in two pieces split at the chunk bound.
	first := frame[:MaxDataChunkBytes]
	next, err := j.PublishChunk(id, plan, addr, &meta, 0, sha256.Sum256(first), first)
	if err != nil || next != MaxDataChunkBytes {
		t.Fatalf("first piece: next=%d err=%v", next, err)
	}
	total := uint64(len(frame))
	if next, err = j.PublishChunk(id, plan, addr, nil, uint64(len(first)), sha256.Sum256(frame[len(first):]), frame[len(first):]); err != nil || next != total {
		t.Fatalf("second piece: next=%d err=%v, want %d", next, err, total)
	}
	// Nothing extends a complete frame.
	if _, err = j.PublishChunk(id, plan, addr, &meta, total, sha256.Sum256([]byte("x")), []byte("x")); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("byte beyond the encoded size: got %v, want ErrStateConflict", err)
	}
	if err := j.FinalizePublish(id, plan, digest, computeLayoutDigest(plan, []LayoutEntry{entry})); err != nil {
		t.Fatalf("close after a chunked frame: %v", err)
	}
	// A fragment of 64 KiB sent as raw data, the way the old accounting
	// counted it, is short of the frame and never closes a batch.
	short := newPlan(t, j, 2, false)
	short.beginPublish(t, j)
	for _, f := range short.frames {
		if _, err := j.PublishChunk(short.id, short.planDigest, f.addr, &f.meta, 0, sha256.Sum256([]byte("raw")), []byte("raw")); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.FinalizePublish(short.id, short.planDigest, short.res.ManifestDigest, short.layout); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("raw bytes in place of frames: got %v, want ErrIncomplete", err)
	}
}

// A manifest that is not a descriptor stream cannot support a publish. Its
// preparation and reservation still succeed: the digest and the length are
// the checks of the prepare, and the manifest is immutable from there. The
// first frame declaration is then refused by a named reason and writes
// nothing, because the fragment count of the event cannot be read.
func TestManifestWithoutFragmentCountsRefusesPublish(t *testing.T) {
	good := testManifest(t, []uint64{6, 3, 3})
	wrongDomain := append([]byte(nil), good...)
	wrongDomain[0] ^= 0xFF
	header := len(manifestDomain) + 1 + 8
	zeroEvents := append([]byte(nil), good[:header]...) // the header alone, event count set below
	put64be(zeroEvents[len(manifestDomain)+1:header], 0)
	ordinalBreak := append([]byte(nil), good...)
	put64be(ordinalBreak[header:header+8], 5) // the first event claims ordinal 5
	cases := map[string][]byte{
		"not a descriptor stream": []byte("extractor.manifest.v2\x00payload"),
		"another domain":          wrongDomain,
		"zero events":             zeroEvents,
		"ordinal break":           ordinalBreak,
		"truncated":               good[:len(good)-10],
		"trailing bytes":          append(append([]byte(nil), good...), 0),
	}
	entry := LayoutEntry{Ordinal: 0, FragmentIndex: 0, Form: 0, Capacity: 6, PartCount: 1, FragmentTotal: 6}
	meta := FrameMeta{Form: 0, Capacity: 6, PartCount: 1, FragmentTotal: 6}
	seq := uint64(1)
	for name, manifest := range cases {
		j, st := newJournal(t)
		id := testBatch(seq)
		seq++
		res := prepareBatch(t, j, id, manifest, 1)
		plan := PlanDigest(res.Seed)
		if _, err := j.BeginPublish(id, res.Seed, plan, computeLayoutDigest(plan, []LayoutEntry{entry}), 1); err != nil {
			t.Fatalf("%s: begin publish: %v", name, err)
		}
		if _, err := j.PublishChunk(id, plan, FrameAddr{0, 0, 0}, &meta, 0, sha256.Sum256([]byte("abc")), []byte("abc")); !errors.Is(err, ErrManifestMismatch) {
			t.Fatalf("%s: first frame declaration: got %v, want ErrManifestMismatch", name, err)
		}
		// Nothing was written for the frame.
		if _, err := st.Get(layoutKey(id, 0, 0)); err != storage.ErrNotFound {
			t.Fatalf("%s: the refused declaration left a layout entry (%v)", name, err)
		}
		if _, err := st.Get(frameHeadKey(id, 0, 0, 0)); err != storage.ErrNotFound {
			t.Fatalf("%s: the refused declaration left a frame head (%v)", name, err)
		}
	}

	// An event outside the manifest cannot be declared either, and a frame
	// that carries no data is no frame.
	j, _ := newJournal(t)
	pf := newPlan(t, j, 1, false)
	pf.beginPublish(t, j)
	empty := FrameMeta{Form: 0, Capacity: 0, PartCount: 1, FragmentTotal: 0}
	for _, c := range []struct {
		name string
		addr FrameAddr
		meta FrameMeta
	}{{"event outside the manifest", FrameAddr{2, 0, 0}, empty}, {"frame without data", FrameAddr{0, 2, 0}, empty}} {
		if _, err := j.PublishChunk(pf.id, pf.planDigest, c.addr, &c.meta, 0, sha256.Sum256(nil), nil); !errors.Is(err, ErrManifestMismatch) && !errors.Is(err, ErrStateConflict) {
			t.Fatalf("%s: got %v", c.name, err)
		}
	}
}

// The fragment counts are read once per batch, read again after a restart,
// and dropped when the batch is closed.
func TestFragmentCountsAreKeptOnceAndDropped(t *testing.T) {
	j, _ := newJournal(t)
	pf := newPlan(t, j, 1, false)
	if _, kept := j.manifests[pf.id]; kept {
		t.Fatal("a batch that declares no frame keeps no fragment counts")
	}
	pf.beginPublish(t, j)
	pf.publishFrames(t, j)
	if counts, kept := j.manifests[pf.id]; !kept || len(counts) != 2 || counts[0] != 3 || counts[1] != 3 {
		t.Fatalf("kept fragment counts: kept=%v %v", kept, counts)
	}
	if err := j.FinalizePublish(pf.id, pf.planDigest, pf.res.ManifestDigest, pf.layout); err != nil {
		t.Fatal(err)
	}
	if _, kept := j.manifests[pf.id]; kept {
		t.Fatal("a closed batch keeps its fragment counts")
	}
}

// put64be writes v big endian into b.
func put64be(b []byte, v uint64) {
	for i := 0; i < 8; i++ {
		b[7-i] = byte(v >> (8 * uint(i)))
	}
}
