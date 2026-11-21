// Tests of the FragmentReader: reassembly of complete, part and mixed
// streams, identity and count consistency, idempotent identical
// redeliveries, and the named rejections — conflicting duplicates,
// reordering, truncation, digest mismatch and form conflicts.
package codec

import (
	"bytes"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// readerFixture assembles the encoded frames of one published record.
type readerFixture struct {
	frames    [][]byte
	fragments [][]byte
	event     proto.EventDescriptor
}

func newReaderFixture(t *testing.T, budget uint64, nodes []TreeNode) *readerFixture {
	t.Helper()
	fragments := buildTreeFragments(t, nodes)
	event, _, err := proto.BuildEventDescriptor(0, testEventID(7), proto.FamilyTx,
		proto.KindApplied, fragments)
	if err != nil {
		t.Fatalf("BuildEventDescriptor: %v", err)
	}
	lengths := make([]uint64, len(fragments))
	for i, frag := range fragments {
		lengths[i] = uint64(len(frag))
	}
	plan, err := proto.ComputeLayout(proto.LayoutRequest{Budget: budget, Stream: "EXT_TX",
		Network: testNetwork(), PublicationEpoch: testEpoch(), SequenceBase: 100,
		Events: []proto.EventFragments{{Ordinal: 0, FragmentLengths: lengths}}})
	if err != nil {
		t.Fatalf("ComputeLayout: %v", err)
	}
	writer, err := NewFragmentWriter(plan, WriterConfig{Network: testNetwork(),
		EventID: event.EventID, RecordDigest: event.RecordDigest,
		PublicationEpoch: testEpoch(), FragmentCount: event.FragmentCount})
	if err != nil {
		t.Fatalf("NewFragmentWriter: %v", err)
	}
	var frames [][]byte
	for i, entry := range plan.Entries {
		if err := writer.Write(entry, 100, fragments[i], func(ef *EmittedFrame) error {
			frames = append(frames, ef.Encoded)
			return nil
		}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	return &readerFixture{frames: frames, fragments: fragments, event: event}
}

// drain feeds all frames into a fresh reader and returns it.
func (f *readerFixture) drain(t *testing.T) *FragmentReader {
	t.Helper()
	reader := NewFragmentReader(ReaderOptions{})
	for _, frame := range f.frames {
		if err := reader.Add(frame); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	return reader
}

func TestReaderReassemblesCompleteStream(t *testing.T) {
	f := newReaderFixture(t, 2*1024*1024, sampleTree())
	reader := f.drain(t)
	reassembled, err := reader.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	for i := range f.fragments {
		if !bytes.Equal(reassembled[i], f.fragments[i]) {
			t.Fatalf("fragment %d differs", i)
		}
	}
}

func TestReaderReassemblesPartStream(t *testing.T) {
	f := newReaderFixture(t, 600, bigBlobTree(4000))
	parts := 0
	for _, frame := range f.frames {
		decoded, err := proto.DecodeTransportFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Part != nil {
			parts++
		}
	}
	if parts == 0 {
		t.Fatal("fixture carries no part frames")
	}
	reader := f.drain(t)
	reassembled, err := reader.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	for i := range f.fragments {
		if !bytes.Equal(reassembled[i], f.fragments[i]) {
			t.Fatalf("fragment %d differs", i)
		}
	}
}

func TestReaderIdenticalRedeliveryIsIdempotent(t *testing.T) {
	f := newReaderFixture(t, 600, bigBlobTree(4000))
	reader := NewFragmentReader(ReaderOptions{})
	// Feed every frame twice and a few of them a third time.
	for _, frame := range f.frames {
		if err := reader.Add(frame); err != nil {
			t.Fatalf("Add: %v", err)
		}
		if err := reader.Add(frame); err != nil {
			t.Fatalf("redelivery rejected: %v", err)
		}
	}
	if err := reader.Add(f.frames[len(f.frames)/2]); err != nil {
		t.Fatalf("late identical redelivery rejected: %v", err)
	}
	reassembled, err := reader.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if len(reassembled) != len(f.fragments) {
		t.Fatalf("reassembled %d fragments", len(reassembled))
	}
}

func TestReaderRejectsConflictingDuplicate(t *testing.T) {
	f := newReaderFixture(t, 600, bigBlobTree(4000))
	reader := NewFragmentReader(ReaderOptions{})
	for i, frame := range f.frames {
		if err := reader.Add(frame); err != nil {
			t.Fatalf("Add: %v", err)
		}
		if i == len(f.frames)/2 {
			// Mutate the payload bytes of one already-delivered part; the
			// frame stays canonical, only the data differs.
			decoded, err := proto.DecodeTransportFrame(frame)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Part != nil {
				decoded.Part.Data[0] ^= 0xFF
			} else {
				decoded.Complete.FragmentBytes[0] ^= 0xFF
			}
			encoded, err := decoded.Encode()
			if err != nil {
				t.Fatal(err)
			}
			err = reader.Add(encoded)
			assertSentinel(t, ErrDuplicate, err)
		}
	}
}

func TestReaderRejectsReordering(t *testing.T) {
	f := newReaderFixture(t, 600, bigBlobTree(4000))

	t.Run("fragment gap", func(t *testing.T) {
		reader := NewFragmentReader(ReaderOptions{})
		if err := reader.Add(f.frames[0]); err != nil {
			t.Fatal(err)
		}
		// Skip to a later fragment without closing the ones between.
		last := f.frames[len(f.frames)-1]
		err := reader.Add(last)
		assertSentinel(t, ErrReordered, err)
	})

	t.Run("part gap inside a fragment", func(t *testing.T) {
		// Find a fragment delivered in parts and skip its first part.
		var first, third []byte
		var fragmentIndex uint64
		byIndex := map[uint64][][]byte{}
		for _, frame := range f.frames {
			decoded, err := proto.DecodeTransportFrame(frame)
			if err != nil {
				t.Fatal(err)
			}
			byIndex[decoded.FragmentIndex] = append(byIndex[decoded.FragmentIndex], frame)
		}
		for index, frames := range byIndex {
			if len(frames) >= 3 {
				first, third = frames[0], frames[2]
				fragmentIndex = index
				break
			}
		}
		if first == nil {
			t.Skip("fixture has no three-part fragment")
		}
		reader := NewFragmentReader(ReaderOptions{})
		for index := uint64(0); index < fragmentIndex; index++ {
			if err := reader.Add(byIndex[index][0]); err != nil {
				t.Fatal(err)
			}
		}
		if err := reader.Add(first); err != nil {
			t.Fatal(err)
		}
		err := reader.Add(third)
		assertSentinel(t, ErrReordered, err)
	})

	t.Run("regression with an unreceived part index", func(t *testing.T) {
		reader := NewFragmentReader(ReaderOptions{})
		for _, frame := range f.frames[:2] {
			if err := reader.Add(frame); err != nil {
				t.Fatal(err)
			}
		}
		// A PART frame for the completed fragment 0 at a part index that
		// was never received: no known duplicate, pure order violation.
		decoded, err := proto.DecodeTransportFrame(f.frames[0])
		if err != nil {
			t.Fatal(err)
		}
		data := decoded.Complete.FragmentBytes
		digest := decoded.FragmentDigest
		forged := *decoded
		forged.Complete = nil
		forged.Part = &proto.FragmentPart{PartIndex: 1, PartCount: 2,
			FragmentTotalBytes: uint64(len(data)), Data: data[:10]}
		forged.FragmentDigest = digest
		encoded, err := forged.Encode()
		if err != nil {
			t.Fatal(err)
		}
		err = reader.Add(encoded)
		assertSentinel(t, ErrReordered, err)
	})
}

func TestReaderRejectsTruncation(t *testing.T) {
	f := newReaderFixture(t, 600, bigBlobTree(4000))

	t.Run("missing final frames", func(t *testing.T) {
		reader := NewFragmentReader(ReaderOptions{})
		for _, frame := range f.frames[:len(f.frames)-2] {
			if err := reader.Add(frame); err != nil {
				t.Fatal(err)
			}
		}
		_, err := reader.Finish()
		assertSentinel(t, ErrTruncated, err)
	})

	t.Run("part stream cut mid-fragment", func(t *testing.T) {
		reader := NewFragmentReader(ReaderOptions{})
		for _, frame := range f.frames[:len(f.frames)-1] {
			if err := reader.Add(frame); err != nil {
				t.Fatal(err)
			}
		}
		_, err := reader.Finish()
		assertSentinel(t, ErrTruncated, err)
	})
}

func TestReaderRejectsDigestMismatch(t *testing.T) {
	f := newReaderFixture(t, 600, bigBlobTree(4000))
	reader := NewFragmentReader(ReaderOptions{})
	for i, frame := range f.frames {
		if i == len(f.frames)-1 {
			// Corrupt the seal fragment's payload inside the frame while
			// keeping the canonical encoding intact.
			decoded, err := proto.DecodeTransportFrame(frame)
			if err != nil {
				t.Fatal(err)
			}
			data := decoded.Complete.FragmentBytes
			if decoded.Part != nil {
				data = decoded.Part.Data
			}
			data[len(data)-1] ^= 0xFF
			encoded, err := decoded.Encode()
			if err != nil {
				t.Fatal(err)
			}
			err = reader.Add(encoded)
			assertSentinel(t, ErrDigestMismatch, err)
			continue
		}
		if err := reader.Add(frame); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
}

func TestReaderRejectsIdentityChanges(t *testing.T) {
	f := newReaderFixture(t, 2*1024*1024, sampleTree())
	mutateFirst := func(change func(frame *proto.TransportFrame)) []byte {
		decoded, err := proto.DecodeTransportFrame(f.frames[0])
		if err != nil {
			t.Fatal(err)
		}
		change(decoded)
		encoded, err := decoded.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	cases := []struct {
		name string
		want error
		run  func(reader *FragmentReader) error
	}{
		{"event id change", ErrIdentityMismatch, func(reader *FragmentReader) error {
			if err := reader.Add(f.frames[0]); err != nil {
				t.Fatal(err)
			}
			return reader.Add(mutateFirst(func(frame *proto.TransportFrame) {
				frame.EventID = testEventID(88)
			}))
		}},
		{"record digest change", ErrIdentityMismatch, func(reader *FragmentReader) error {
			if err := reader.Add(f.frames[0]); err != nil {
				t.Fatal(err)
			}
			return reader.Add(mutateFirst(func(frame *proto.TransportFrame) {
				frame.RecordDigest[1] ^= 0xFF
			}))
		}},
		{"epoch change", ErrIdentityMismatch, func(reader *FragmentReader) error {
			if err := reader.Add(f.frames[0]); err != nil {
				t.Fatal(err)
			}
			return reader.Add(mutateFirst(func(frame *proto.TransportFrame) {
				var epoch proto.ID16
				epoch[0] = 9
				frame.PublicationEpoch = epoch
			}))
		}},
		{"fragment count change", ErrConflict, func(reader *FragmentReader) error {
			if err := reader.Add(f.frames[0]); err != nil {
				t.Fatal(err)
			}
			return reader.Add(mutateFirst(func(frame *proto.TransportFrame) {
				frame.FragmentCount++
			}))
		}},
		{"malformed frame bytes", ErrMalformed, func(reader *FragmentReader) error {
			return reader.Add([]byte{0x08, 0x02})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := NewFragmentReader(ReaderOptions{})
			assertSentinel(t, tc.want, tc.run(reader))
		})
	}
}

func TestReaderFormChangeConflict(t *testing.T) {
	// Send part 0 of a parted fragment, then a COMPLETE frame for the same
	// fragment with the correct digest: the form of a fragment cannot
	// change mid-stream.
	f := newReaderFixture(t, 600, bigBlobTree(4000))
	byIndex := map[uint64][][]byte{}
	for _, frame := range f.frames {
		decoded, err := proto.DecodeTransportFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		byIndex[decoded.FragmentIndex] = append(byIndex[decoded.FragmentIndex], frame)
	}
	var partedIndex uint64
	for index, frames := range byIndex {
		if len(frames) >= 2 {
			decoded, err := proto.DecodeTransportFrame(frames[0])
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Part != nil {
				partedIndex = index
				break
			}
		}
	}
	reader := NewFragmentReader(ReaderOptions{})
	for index := uint64(0); index < partedIndex; index++ {
		if err := reader.Add(byIndex[index][0]); err != nil {
			t.Fatal(err)
		}
	}
	if err := reader.Add(byIndex[partedIndex][0]); err != nil {
		t.Fatal(err)
	}
	// Build a COMPLETE frame carrying the whole fragment bytes with the
	// same digests as the parted form.
	parts := byIndex[partedIndex]
	var assembled []byte
	part0, err := proto.DecodeTransportFrame(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range parts {
		decoded, err := proto.DecodeTransportFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		assembled = append(assembled, decoded.Part.Data...)
	}
	complete := *part0
	complete.Complete = &proto.CompleteFragment{FragmentBytes: assembled}
	complete.Part = nil
	encoded, err := complete.Encode()
	if err != nil {
		t.Fatal(err)
	}
	err = reader.Add(encoded)
	assertSentinel(t, ErrConflict, err)
}

func TestReaderRejectsPartShapeViolations(t *testing.T) {
	f := newReaderFixture(t, 600, bigBlobTree(4000))
	byIndex := map[uint64][][]byte{}
	for _, frame := range f.frames {
		decoded, err := proto.DecodeTransportFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		byIndex[decoded.FragmentIndex] = append(byIndex[decoded.FragmentIndex], frame)
	}
	var partedIndex uint64
	for index, frames := range byIndex {
		if len(frames) >= 2 {
			decoded, _ := proto.DecodeTransportFrame(frames[0])
			if decoded != nil && decoded.Part != nil {
				partedIndex = index
				break
			}
		}
	}
	prefix := func(reader *FragmentReader) {
		for index := uint64(0); index < partedIndex; index++ {
			if err := reader.Add(byIndex[index][0]); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Run("fragment opens at nonzero part", func(t *testing.T) {
		reader := NewFragmentReader(ReaderOptions{})
		prefix(reader)
		decoded, err := proto.DecodeTransportFrame(byIndex[partedIndex][1])
		if err != nil {
			t.Fatal(err)
		}
		// Present part 1 of the parted fragment as its opening frame.
		err = reader.Add(byIndex[partedIndex][1])
		_ = decoded
		assertSentinel(t, ErrReordered, err)
	})
	t.Run("total length mismatch", func(t *testing.T) {
		reader := NewFragmentReader(ReaderOptions{})
		prefix(reader)
		decoded, err := proto.DecodeTransportFrame(byIndex[partedIndex][0])
		if err != nil {
			t.Fatal(err)
		}
		decoded.Part.FragmentTotalBytes++
		encoded, err := decoded.Encode()
		if err != nil {
			t.Fatal(err)
		}
		// The lying opening part starts an assembly; the truthful next
		// part then contradicts the total.
		if err := reader.Add(encoded); err != nil {
			t.Fatalf("lying opening part rejected early: %v", err)
		}
		err = reader.Add(byIndex[partedIndex][1])
		assertSentinel(t, ErrConflict, err)
	})
	t.Run("short middle part", func(t *testing.T) {
		reader := NewFragmentReader(ReaderOptions{})
		prefix(reader)
		if err := reader.Add(byIndex[partedIndex][0]); err != nil {
			t.Fatal(err)
		}
		decoded, err := proto.DecodeTransportFrame(byIndex[partedIndex][1])
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Part.PartIndex+1 == decoded.Part.PartCount {
			t.Skip("two-part fixture: the short part is final")
		}
		decoded.Part.Data = decoded.Part.Data[:len(decoded.Part.Data)-1]
		encoded, err := decoded.Encode()
		if err != nil {
			t.Fatal(err)
		}
		err = reader.Add(encoded)
		assertSentinel(t, ErrInvalidValue, err)
	})
}

func TestReaderStreamingConsumer(t *testing.T) {
	f := newReaderFixture(t, 600, bigBlobTree(4000))
	var streamed [][]byte
	reader := NewFragmentReader(ReaderOptions{OnFragment: func(index uint64, fragment []byte) error {
		if index != uint64(len(streamed)) {
			t.Fatalf("streamed index %d at position %d", index, len(streamed))
		}
		streamed = append(streamed, append([]byte(nil), fragment...))
		return nil
	}})
	for _, frame := range f.frames {
		if err := reader.Add(frame); err != nil {
			t.Fatal(err)
		}
	}
	finished, err := reader.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished != nil {
		t.Fatal("streaming reader retained fragment bytes")
	}
	for i := range f.fragments {
		if !bytes.Equal(streamed[i], f.fragments[i]) {
			t.Fatalf("streamed fragment %d differs", i)
		}
	}
}

func TestReaderRejectsRecordDigestMismatch(t *testing.T) {
	// Frames whose record digest field disagrees with the actual content
	// fragments: assembly succeeds fragment by fragment, Finish rejects.
	f := newReaderFixture(t, 600, bigBlobTree(4000))
	reader := NewFragmentReader(ReaderOptions{})
	for i, frame := range f.frames {
		decoded, err := proto.DecodeTransportFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		decoded.RecordDigest[7] ^= 0xFF
		// The fragment digests stay intact: they hash the single
		// fragments, not the record.
		encoded, err := decoded.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if err := reader.Add(encoded); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	_, err := reader.Finish()
	assertSentinel(t, ErrDigestMismatch, err)
}
