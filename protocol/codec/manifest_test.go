// Tests of the ManifestBuilder: byte format and digest of the
// "extractor.manifest.v2" stream, determinism across builders, ordinal and
// identity rules, the bounded chunker with boundaries inside descriptor
// encodings, and the batch layout composition through protocol/proto.
package codec

import (
	"bytes"
	"encoding/binary"
	"errors"
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
