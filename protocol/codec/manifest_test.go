// Tests of the ManifestBuilder: byte format and digest of the
// "extractor.manifest.v2" stream, determinism across builders, ordinal and
// identity rules, the bounded chunker with boundaries inside descriptor
// encodings, and the batch layout composition through protocol/proto.
package codec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// batchEvents builds descriptors for two events of a closed batch.
func batchEvents(t *testing.T) ([]proto.EventDescriptor, [][]proto.FragmentDescriptor) {
	t.Helper()
	var events []proto.EventDescriptor
	var allFrags [][]proto.FragmentDescriptor
	for i, nodes := range [][]TreeNode{sampleTree(), bigBlobTree(40000)} {
		fragments := buildTreeFragments(t, nodes)
		event, frags, err := proto.BuildEventDescriptor(uint64(i), testEventID(uint64(20+i)),
			proto.FamilyTx, proto.KindApplied, fragments)
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		events = append(events, event)
		allFrags = append(allFrags, frags)
	}
	return events, allFrags
}

// filledBuilder returns a builder with both events added.
func filledBuilder(t *testing.T) *ManifestBuilder {
	t.Helper()
	events, allFrags := batchEvents(t)
	builder := NewManifestBuilder()
	for i := range events {
		if err := builder.AddEvent(events[i], allFrags[i]); err != nil {
			t.Fatalf("AddEvent %d: %v", i, err)
		}
	}
	return builder
}

func TestManifestBytesFormat(t *testing.T) {
	builder := filledBuilder(t)
	got, err := builder.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	events, allFrags := batchEvents(t)
	want := []byte(ManifestDomain)
	want = append(want, 0)
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(events)))
	want = append(want, count[:]...)
	for i := range events {
		encoded, err := events[i].Encode()
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, encoded...)
		for _, frag := range allFrags[i] {
			want = append(want, frag.Encode()...)
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("manifest bytes differ: got %d bytes, want %d", len(got), len(want))
	}
	if !bytes.HasPrefix(got, append([]byte(ManifestDomain), 0)) {
		t.Fatal("manifest does not open with the domain and zero byte")
	}
}

func TestManifestDigestMatchesBytes(t *testing.T) {
	builder := filledBuilder(t)
	digest, err := builder.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	body, err := builder.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if want := sha256Of(body); digest != want {
		t.Fatal("digest differs from SHA-256 of the manifest bytes")
	}
	again, err := builder.Digest()
	if err != nil || again != digest {
		t.Fatal("digest is not stable across calls")
	}
}

func TestManifestDeterministicAcrossBuilders(t *testing.T) {
	first, second := filledBuilder(t), filledBuilder(t)
	firstBytes, err := first.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := second.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("two builders produced different manifest bytes")
	}
	firstDigest, _ := first.Digest()
	secondDigest, _ := second.Digest()
	if firstDigest != secondDigest {
		t.Fatal("two builders produced different digests")
	}
}

func TestManifestAddEventRejections(t *testing.T) {
	events, allFrags := batchEvents(t)

	t.Run("ordinal gap", func(t *testing.T) {
		builder := NewManifestBuilder()
		if err := builder.AddEvent(events[0], allFrags[0]); err != nil {
			t.Fatal(err)
		}
		skipped := events[1]
		skipped.Ordinal = 5
		err := builder.AddEvent(skipped, allFrags[1])
		assertSentinel(t, ErrFragments, err)
	})

	t.Run("duplicate event id", func(t *testing.T) {
		builder := NewManifestBuilder()
		if err := builder.AddEvent(events[0], allFrags[0]); err != nil {
			t.Fatal(err)
		}
		repeat := events[1]
		repeat.Ordinal = 1
		repeat.EventID = events[0].EventID
		err := builder.AddEvent(repeat, allFrags[1])
		assertSentinel(t, ErrFragments, err)
	})

	t.Run("inconsistent fragments", func(t *testing.T) {
		builder := NewManifestBuilder()
		broken := events[0]
		broken.RecordLength++
		err := builder.AddEvent(broken, allFrags[0])
		if err == nil {
			t.Fatal("inconsistent descriptor accepted")
		}
	})
}

func TestManifestEmptyBatchRejected(t *testing.T) {
	builder := NewManifestBuilder()
	if _, err := builder.Digest(); !errors.Is(err, ErrFragments) {
		t.Fatalf("Digest of an empty batch: %v", err)
	}
	if _, err := builder.Bytes(); !errors.Is(err, ErrFragments) {
		t.Fatalf("Bytes of an empty batch: %v", err)
	}
	if _, err := builder.Chunks(1024); !errors.Is(err, ErrFragments) {
		t.Fatalf("Chunks of an empty batch: %v", err)
	}
}

func TestManifestDescriptorBytesTotal(t *testing.T) {
	builder := filledBuilder(t)
	events, allFrags := batchEvents(t)
	want := uint64(0)
	for i := range events {
		want += uint64(proto.EventDescriptorSize + len(allFrags[i])*proto.FragmentDescriptorSize)
	}
	if got := builder.DescriptorBytesTotal(); got != want {
		t.Fatalf("descriptor bytes %d, want %d", got, want)
	}
	if builder.EventCount() != uint64(len(events)) {
		t.Fatalf("event count %d", builder.EventCount())
	}
}

func TestManifestChunkerReassembles(t *testing.T) {
	builder := filledBuilder(t)
	body, err := builder.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, max := range []uint64{uint64(proto.EventDescriptorSize), 200, 1024, uint64(len(body))} {
		chunker, err := builder.Chunks(max)
		if err != nil {
			t.Fatalf("Chunks(%d): %v", max, err)
		}
		var reassembled []byte
		offset := uint64(0)
		for {
			chunk, ok, err := chunker.Next()
			if err != nil {
				t.Fatalf("max %d: %v", max, err)
			}
			if !ok {
				break
			}
			if chunk.Offset != offset {
				t.Fatalf("max %d: chunk offset %d, want %d", max, chunk.Offset, offset)
			}
			if uint64(len(chunk.Bytes)) > max {
				t.Fatalf("max %d: chunk of %d bytes", max, len(chunk.Bytes))
			}
			if chunk.SHA256 != sha256Of(chunk.Bytes) {
				t.Fatalf("max %d: chunk digest wrong", max)
			}
			reassembled = append(reassembled, chunk.Bytes...)
			offset += uint64(len(chunk.Bytes))
		}
		if !bytes.Equal(reassembled, body) {
			t.Fatalf("max %d: chunks reassemble to different bytes", max)
		}
	}
}

func TestManifestChunkerRejectsSmallMax(t *testing.T) {
	builder := filledBuilder(t)
	_, err := builder.Chunks(uint64(proto.EventDescriptorSize) - 1)
	assertSentinel(t, ErrInvalidValue, err)
}

func TestManifestLayoutComposition(t *testing.T) {
	builder := filledBuilder(t)
	events, allFrags := batchEvents(t)
	base := proto.LayoutRequest{Budget: 4096, Stream: "EXT_TX", Network: testNetwork(),
		PublicationEpoch: testEpoch(), SequenceBase: 77}
	var planDigest proto.Hash32
	copy(planDigest[:], testPattern(5, 32))

	entries, layoutDigest, err := builder.BuildLayout(base, planDigest)
	if err != nil {
		t.Fatalf("BuildLayout: %v", err)
	}
	// The entries must equal a direct proto.ComputeLayout over the same
	// fragment lengths.
	direct, err := proto.ComputeLayout(proto.LayoutRequest{Budget: 4096, Stream: "EXT_TX",
		Network: testNetwork(), PublicationEpoch: testEpoch(), SequenceBase: 77,
		Events: []proto.EventFragments{
			{Ordinal: events[0].Ordinal, FragmentLengths: lengthsOf(allFrags[0])},
			{Ordinal: events[1].Ordinal, FragmentLengths: lengthsOf(allFrags[1])},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(direct.Entries) {
		t.Fatalf("%d entries, want %d", len(entries), len(direct.Entries))
	}
	for i := range entries {
		if entries[i] != direct.Entries[i] {
			t.Fatalf("entry %d differs from proto.ComputeLayout", i)
		}
	}
	wantDigest, err := proto.LayoutManifestDigest(planDigest, direct.Entries)
	if err != nil {
		t.Fatal(err)
	}
	if layoutDigest != wantDigest {
		t.Fatal("layout digest differs from proto.LayoutManifestDigest")
	}
	// Deterministic recomputation.
	entries2, digest2, err := builder.BuildLayout(base, planDigest)
	if err != nil || len(entries2) != len(entries) || digest2 != layoutDigest {
		t.Fatal("BuildLayout is not deterministic")
	}
}

// lengthsOf is a helper mirroring the fragment lengths of a descriptor list.
func lengthsOf(frags []proto.FragmentDescriptor) []uint64 {
	out := make([]uint64, len(frags))
	for i, frag := range frags {
		out[i] = frag.EncodedLength
	}
	return out
}

func TestManifestLayoutRequestRejections(t *testing.T) {
	builder := filledBuilder(t)
	base := proto.LayoutRequest{Budget: 4096, Stream: "EXT_TX", Network: testNetwork(),
		PublicationEpoch: testEpoch(), SequenceBase: 1}

	t.Run("base already carries events", func(t *testing.T) {
		filled := base
		filled.Events = []proto.EventFragments{{Ordinal: 0}}
		_, err := builder.LayoutRequest(filled)
		assertSentinel(t, ErrInvalidValue, err)
	})

	t.Run("empty builder", func(t *testing.T) {
		_, err := NewManifestBuilder().LayoutRequest(base)
		assertSentinel(t, ErrInvalidValue, err)
	})
}

func TestManifestLayoutRejectsBadBudget(t *testing.T) {
	builder := filledBuilder(t)
	base := proto.LayoutRequest{Budget: 0, Stream: "EXT_TX", Network: testNetwork(),
		PublicationEpoch: testEpoch(), SequenceBase: 1}
	var planDigest proto.Hash32
	_, _, err := builder.BuildLayout(base, planDigest)
	if err == nil {
		t.Fatal("zero budget accepted")
	}
}

// manifestFixture is one built manifest: its bytes and, per event, the
// descriptors the builder was given.
type manifestFixture struct {
	bytes  []byte
	events []proto.EventDescriptor
	frags  [][]proto.FragmentDescriptor
}

// buildManifestFixture builds a manifest of count events with varying
// trees, families, kinds and fragment counts.
func buildManifestFixture(t *testing.T, count int) manifestFixture {
	t.Helper()
	fixture := manifestFixture{bytes: nil, events: make([]proto.EventDescriptor, 0, count),
		frags: make([][]proto.FragmentDescriptor, 0, count)}
	builder := NewManifestBuilder()
	families := []proto.Family{proto.FamilyTx, proto.FamilyBlock, proto.FamilyBalanceSystem}
	kinds := []proto.Kind{proto.KindApplied, proto.KindStatus, proto.KindRemoved}
	for i := 0; i < count; i++ {
		var nodes []TreeNode
		switch i % 3 {
		case 0:
			nodes = sampleTree()
		case 1:
			nodes = bigBlobTree(40000 + 64*i)
		default:
			nodes = manyNodesTree(5 + i)
		}
		var fragments [][]byte
		_, err := BuildRecord(testEventID(uint64(100+i)), testNetwork(), families[i%3], kinds[i%3], 100, nodes,
			func(index uint64, fragment []byte) error {
				fragments = append(fragments, bytes.Clone(fragment))
				return nil
			})
		if err != nil {
			t.Fatalf("event %d: BuildRecord: %v", i, err)
		}
		event, frags, err := proto.BuildEventDescriptor(uint64(i), testEventID(uint64(100+i)),
			families[i%3], kinds[i%3], fragments)
		if err != nil {
			t.Fatalf("event %d: BuildEventDescriptor: %v", i, err)
		}
		if err := builder.AddEvent(event, frags); err != nil {
			t.Fatalf("event %d: AddEvent: %v", i, err)
		}
		fixture.events = append(fixture.events, event)
		fixture.frags = append(fixture.frags, frags)
	}
	body, err := builder.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	fixture.bytes = body
	return fixture
}

// decodedEvents is what DecodeManifest must return for the fixture.
func (f manifestFixture) decodedEvents() []ManifestEvent {
	out := make([]ManifestEvent, 0, len(f.events))
	for i := range f.events {
		out = append(out, ManifestEvent{Ordinal: f.events[i].Ordinal, EventID: f.events[i].EventID,
			Family: uint16(f.events[i].Family), Kind: uint16(f.events[i].Kind),
			RecordLength: f.events[i].RecordLength, RecordDigest: f.events[i].RecordDigest,
			Fragments: f.frags[i]})
	}
	return out
}

func manifestEventsEqual(a, b []ManifestEvent) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.Ordinal != y.Ordinal || x.EventID != y.EventID || x.Family != y.Family || x.Kind != y.Kind ||
			x.RecordLength != y.RecordLength || x.RecordDigest != y.RecordDigest ||
			!slices.Equal(x.Fragments, y.Fragments) {
			return false
		}
	}
	return true
}

// The fixed header is the domain, the zero byte and the event count.
func TestManifestHeaderBytes(t *testing.T) {
	// The domain "extractor.manifest.v2" holds 21 bytes; with the zero byte
	// and the 8-byte event count the header holds 30, not 32.
	if ManifestHeaderBytes != 30 {
		t.Fatalf("manifest header is %d bytes, want 30", ManifestHeaderBytes)
	}
	if ManifestHeaderBytes != uint64(len(ManifestDomain)+1+8) {
		t.Fatalf("manifest header is %d bytes, want len(domain) + 1 + 8", ManifestHeaderBytes)
	}
	fixture := buildManifestFixture(t, 1)
	if !bytes.HasPrefix(fixture.bytes, []byte(ManifestDomain)) || fixture.bytes[len(ManifestDomain)] != 0 ||
		binary.BigEndian.Uint64(fixture.bytes[ManifestHeaderBytes-8:ManifestHeaderBytes]) != 1 {
		t.Fatalf("manifest header %x", fixture.bytes[:ManifestHeaderBytes])
	}
}

// DecodeManifest parses the bytes of ManifestBuilder.Bytes() for 1, 2 and
// 50 events back into the same descriptors.
func TestDecodeManifestParsesBuilderBytes(t *testing.T) {
	for _, count := range []int{1, 2, 50} {
		fixture := buildManifestFixture(t, count)
		got, err := DecodeManifest(fixture.bytes)
		if err != nil {
			t.Fatalf("%d events: DecodeManifest: %v", count, err)
		}
		if want := fixture.decodedEvents(); !manifestEventsEqual(got, want) {
			t.Fatalf("%d events: decoded events differ from the added descriptors", count)
		}
		// The decoded events rebuild the same manifest bytes.
		builder := NewManifestBuilder()
		for i := range got {
			event := fixture.events[i]
			if err := builder.AddEvent(event, got[i].Fragments); err != nil {
				t.Fatalf("%d events: event %d: AddEvent: %v", count, i, err)
			}
		}
		again, err := builder.Bytes()
		if err != nil || !bytes.Equal(again, fixture.bytes) {
			t.Fatalf("%d events: rebuilt manifest differs: %v", count, err)
		}
	}
}

// A manifest reassembled from chunks of any size decodes to the same
// events, also when a chunk boundary falls inside a descriptor.
func TestDecodeManifestAcceptsReassembledChunks(t *testing.T) {
	fixture := buildManifestFixture(t, 2)
	want, err := DecodeManifest(fixture.bytes)
	if err != nil {
		t.Fatalf("DecodeManifest: %v", err)
	}
	builder := NewManifestBuilder()
	for i := range fixture.events {
		if err := builder.AddEvent(fixture.events[i], fixture.frags[i]); err != nil {
			t.Fatal(err)
		}
	}
	for _, max := range []uint64{uint64(proto.EventDescriptorSize), 150, 199, 1024, uint64(len(fixture.bytes))} {
		chunker, err := builder.Chunks(max)
		if err != nil {
			t.Fatalf("Chunks(%d): %v", max, err)
		}
		var reassembled []byte
		for {
			chunk, ok, err := chunker.Next()
			if err != nil {
				t.Fatalf("max %d: %v", max, err)
			}
			if !ok {
				break
			}
			reassembled = append(reassembled, chunk.Bytes...)
		}
		got, err := DecodeManifest(reassembled)
		if err != nil {
			t.Fatalf("max %d: DecodeManifest: %v", max, err)
		}
		if !manifestEventsEqual(got, want) {
			t.Fatalf("max %d: decoded events differ", max)
		}
	}
}

// patchUint64 returns a copy of b with the 8 bytes at offset replaced.
func patchUint64(b []byte, offset int, value uint64) []byte {
	out := bytes.Clone(b)
	binary.BigEndian.PutUint64(out[offset:offset+8], value)
	return out
}

// eventOffset is the offset of the event descriptor of ordinal i in the
// fixture.
func (f manifestFixture) eventOffset(i int) int {
	offset := int(ManifestHeaderBytes)
	for j := 0; j < i; j++ {
		offset += proto.EventDescriptorSize + len(f.frags[j])*proto.FragmentDescriptorSize
	}
	return offset
}

// The reader accepts only what the builder produces. Each rejection names
// its cause and wraps one sentinel.
func TestDecodeManifestRejects(t *testing.T) {
	two := buildManifestFixture(t, 2)
	one := buildManifestFixture(t, 1)
	if len(two.frags[0]) < 2 || two.events[1].FragmentCount < 2 {
		t.Fatalf("the fixture needs events of at least two fragments: %d", two.events[1].FragmentCount)
	}
	event0, event1 := two.eventOffset(0), two.eventOffset(1)
	// The event count ends the header; an event descriptor is ordinal (8),
	// event id (40), family (2), kind (2), record length (8), record digest
	// (32), fragment count (8) and fragment manifest digest (32); a
	// fragment descriptor is index (8), encoded length (8) and SHA-256 (32).
	countAt := int(ManifestHeaderBytes) - 8
	const idAt, familyAt, recordLengthAt, fragmentCountAt = 8, 48, 52, 92
	zeroCount := patchUint64(two.bytes, countAt, 0)
	for _, tc := range []struct {
		name   string
		data   []byte
		wraps  []error
		reason string
	}{
		{"empty input", nil, []error{ErrTruncated}, "shorter than the 21-byte domain"},
		{"shorter than the domain", two.bytes[:10], []error{ErrTruncated}, "shorter than the 21-byte domain"},
		{"ends inside the header", two.bytes[:29], []error{ErrTruncated}, "ends inside the 30-byte header"},
		{"wrong domain", append([]byte("extractor.manifest.v3"), two.bytes[23:]...),
			[]error{ErrInvalidValue}, `manifest domain "extractor.manifest.v3"`},
		{"missing zero byte", func() []byte {
			out := bytes.Clone(two.bytes)
			out[len(ManifestDomain)] = 1
			return out
		}(), []error{ErrMalformed}, "byte after the manifest domain is 1"},
		{"zero count with descriptors", zeroCount, []error{ErrFragments}, "declares zero events"},
		{"zero count alone", zeroCount[:ManifestHeaderBytes], []error{ErrFragments}, "declares zero events"},
		{"truncated event descriptor", two.bytes[:event0+100], []error{ErrTruncated},
			"event descriptor 0 holds 100 bytes"},
		{"truncated event descriptor of the second event", two.bytes[:event1+131], []error{ErrTruncated},
			"event descriptor 1 holds 131 bytes"},
		{"truncated fragment descriptors", two.bytes[:event0+proto.EventDescriptorSize+48+10],
			[]error{ErrTruncated}, fmt.Sprintf("event 0 declares %d fragment descriptors and the manifest holds bytes for 1",
				two.events[0].FragmentCount)},
		{"truncated at a fragment boundary", two.bytes[:event0+proto.EventDescriptorSize],
			[]error{ErrTruncated}, "holds bytes for 0"},
		{"count above the events", patchUint64(two.bytes, countAt, 3), []error{ErrFragments},
			"manifest ends after 2 events"},
		{"count of the whole range", patchUint64(one.bytes, countAt, ^uint64(0)), []error{ErrFragments},
			"manifest ends after 1 events"},
		{"count below the events", patchUint64(two.bytes, countAt, 1), []error{ErrFragments},
			"declares 1 events and holds more"},
		{"trailing bytes", append(bytes.Clone(one.bytes), 1, 2, 3, 4, 5, 6, 7), []error{ErrMalformed},
			"7 bytes after the last descriptor"},
		{"trailing bytes of a descriptor length without its fragments",
			append(bytes.Clone(one.bytes), one.bytes[event0:event0+proto.EventDescriptorSize]...),
			[]error{ErrMalformed}, "132 bytes after the last descriptor"},
		{"trailing bytes that form one more event", append(bytes.Clone(one.bytes), two.bytes[event1:]...),
			[]error{ErrFragments}, "declares 1 events and holds more"},
		{"fragment descriptor of zero length", patchUint64(one.bytes, event0+proto.EventDescriptorSize+8, 0),
			[]error{ErrInvalidValue, proto.ErrInvalidValue}, "fragment descriptor 0 of event 0"},
		{"fragment descriptor above the fragment bound",
			patchUint64(one.bytes, event0+proto.EventDescriptorSize+8, proto.FragmentMaxBytes+1),
			[]error{ErrInvalidValue, proto.ErrInvalidValue}, "encoded length 65537"},
		{"unknown event family", func() []byte {
			out := bytes.Clone(one.bytes)
			binary.BigEndian.PutUint16(out[event0+familyAt:event0+familyAt+2], 9)
			return out
		}(), []error{ErrInvalidValue, proto.ErrClosedRegistry}, "event descriptor 0"},
		{"unset event id", func() []byte {
			out := bytes.Clone(one.bytes)
			clear(out[event0+idAt : event0+idAt+16])
			return out
		}(), []error{ErrInvalidValue, proto.ErrInvalidValue}, "event descriptor 0"},
		{"record length differs from the fragment lengths",
			patchUint64(one.bytes, event0+recordLengthAt, one.events[0].RecordLength+1),
			[]error{ErrInvalidValue, proto.ErrFragments}, "fragments of event 0"},
		{"fragment index gap", patchUint64(one.bytes, event0+proto.EventDescriptorSize+48, 9),
			[]error{ErrInvalidValue, proto.ErrFragments}, "fragments of event 0"},
		{"fragment count above the bytes of the manifest",
			patchUint64(one.bytes, event0+fragmentCountAt, 20), []error{ErrTruncated},
			fmt.Sprintf("event 0 declares 20 fragment descriptors and the manifest holds bytes for %d",
				(len(one.bytes)-event0-proto.EventDescriptorSize)/proto.FragmentDescriptorSize)},
		{"fragment count above the record length",
			patchUint64(one.bytes, event0+fragmentCountAt, 1<<40),
			[]error{ErrInvalidValue, proto.ErrFragments}, "event descriptor 0"},
		{"fragment count below the record minimum", patchUint64(one.bytes, event0+fragmentCountAt, 1),
			[]error{ErrInvalidValue, proto.ErrFragments}, "event descriptor 0"},
		{"second event ordinal is not continuous", patchUint64(two.bytes, event1, 5),
			[]error{ErrFragments}, "event 1 carries ordinal 5"},
		{"first event ordinal is not zero", patchUint64(two.bytes, event0, 1),
			[]error{ErrFragments}, "event 0 carries ordinal 1"},
		{"event id repeats in the batch", func() []byte {
			out := bytes.Clone(two.bytes)
			copy(out[event1+idAt:event1+idAt+40], out[event0+idAt:event0+idAt+40])
			return out
		}(), []error{ErrFragments}, "event id repeats in the batch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events, err := DecodeManifest(tc.data)
			if err == nil {
				t.Fatalf("%d events were accepted", len(events))
			}
			if events != nil {
				t.Fatalf("a rejected manifest returned %d events", len(events))
			}
			for _, want := range tc.wraps {
				assertSentinel(t, want, err)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("error %q does not name %q", err, tc.reason)
			}
		})
	}
}

// A decoded manifest shares no memory with its input: the descriptors are
// values.
func TestDecodeManifestOwnsItsResult(t *testing.T) {
	fixture := buildManifestFixture(t, 2)
	got, err := DecodeManifest(fixture.bytes)
	if err != nil {
		t.Fatalf("DecodeManifest: %v", err)
	}
	for i := range fixture.bytes {
		fixture.bytes[i] = 0
	}
	if !manifestEventsEqual(got, buildManifestFixture(t, 2).decodedEvents()) {
		t.Fatal("the decoded events changed with their input")
	}
}
