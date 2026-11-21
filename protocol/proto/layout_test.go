package proto

import (
	"crypto/sha256"
	"errors"
	"math"
	"testing"
)

// Golden digest from the independent Python implementation.
const (
	goldenPlanDigestHex        = "af3d3716f236a8fa6e8b29f3204562dbed807f101d936b0e73e09f3f59553d8f"
	goldenLayoutManifestDigest = "13699a843aa17bb9fa3af7c621b2e4e7a25eb87cab641380438683098d67e449"
)

func layoutRequestFixture(t *testing.T, budget uint64) LayoutRequest {
	t.Helper()
	return LayoutRequest{
		Budget:           budget,
		Stream:           "EXTRACTOR_ETH",
		Network:          goldenNetwork(t),
		PublicationEpoch: goldenEpoch(t),
		SequenceBase:     424242,
		Events: []EventFragments{
			{Ordinal: 0, FragmentLengths: []uint64{900, 4100, 30000}},
		},
	}
}

func TestLayoutCompleteAtExactBoundary(t *testing.T) {
	req := layoutRequestFixture(t, 0)
	header, err := NatsHeaderSize(req.Stream)
	if err != nil {
		t.Fatalf("header size: %v", err)
	}
	scalars := frameScalars{chainSequence: req.SequenceBase, fragmentIndex: 0, fragmentCount: 3}
	total := uint64(900)
	exact := completeEncodedSize(scalars, total) + header
	req.Budget = exact
	plan, err := ComputeLayout(req)
	if err != nil {
		t.Fatalf("layout at the exact boundary: %v", err)
	}
	if len(plan.Entries) != 3 {
		t.Fatalf("entries %d, want 3", len(plan.Entries))
	}
	first := plan.Entries[0]
	if first.Form != FormComplete || first.Capacity != total || first.PartCount != 1 {
		t.Fatalf("boundary fragment not COMPLETE: %+v", first)
	}
	start, end, err := first.PartBounds(0)
	if err != nil || start != 0 || end != total {
		t.Fatalf("COMPLETE bounds [%d,%d) err %v", start, end, err)
	}
	if plan.Budget != req.Budget || plan.StreamHeaderBytes != header {
		t.Fatalf("plan does not echo its inputs")
	}
}

// TestLayoutPartOneByteOverComplete: one byte below the COMPLETE boundary
// the plan must switch to PART with the largest fitting capacity and a
// continuous part cover (LAYOUT-01).
func TestLayoutPartOneByteOverComplete(t *testing.T) {
	req := layoutRequestFixture(t, 0)
	header, _ := NatsHeaderSize(req.Stream)
	scalars := frameScalars{chainSequence: req.SequenceBase, fragmentIndex: 1, fragmentCount: 3}
	total := uint64(4100)
	boundary := completeEncodedSize(scalars, total) + header
	req.Budget = boundary - 1
	plan, err := ComputeLayout(req)
	if err != nil {
		t.Fatalf("layout below the boundary: %v", err)
	}
	entry := plan.Entries[1]
	if entry.Form != FormPart {
		t.Fatalf("form %s, want PART", entry.Form)
	}
	// The capacity is maximal under the conservative probe: it fits, the
	// next byte does not.
	if partEncodedSize(scalars, math.MaxUint64, math.MaxUint64, total, entry.Capacity)+header > req.Budget {
		t.Fatalf("capacity %d does not fit the budget", entry.Capacity)
	}
	if entry.Capacity < total &&
		partEncodedSize(scalars, math.MaxUint64, math.MaxUint64, total, entry.Capacity+1)+header <= req.Budget {
		t.Fatalf("capacity %d is not maximal", entry.Capacity)
	}
	// The parts cover [0,total) exactly, in order, without gaps.
	var cursor uint64
	for i := uint64(0); i < entry.PartCount; i++ {
		start, end, err := entry.PartBounds(i)
		if err != nil {
			t.Fatalf("part %d bounds: %v", i, err)
		}
		if start != cursor || end <= start || end-start > entry.Capacity {
			t.Fatalf("part %d bounds [%d,%d) break the cover at %d", i, start, end, cursor)
		}
		cursor = end
	}
	if cursor != total {
		t.Fatalf("parts cover %d bytes, want %d", cursor, total)
	}
}

// TestLayoutLargeFragmentSplitsAcrossParts walks a 30000-byte fragment into
// parts and checks the entry arithmetic end to end.
func TestLayoutLargeFragmentSplitsAcrossParts(t *testing.T) {
	req := layoutRequestFixture(t, 2_000_000) // the D52 default is 2 MiB
	plan, err := ComputeLayout(req)
	if err != nil {
		t.Fatalf("layout: %v", err)
	}
	third := plan.Entries[2]
	total := uint64(30000)
	if third.FragmentTotalBytes != total {
		t.Fatalf("total %d, want %d", third.FragmentTotalBytes, total)
	}
	want, err := ceilDiv(total, third.Capacity)
	if err != nil {
		t.Fatalf("ceil: %v", err)
	}
	if third.PartCount != want {
		t.Fatalf("part count %d, want ceil(%d/%d)=%d", third.PartCount, total, third.Capacity, want)
	}
	if err := ValidateFragments(EventDescriptor{
		Ordinal: 0, EventID: goldenEventID(t), Family: FamilyBlock, Kind: KindApplied,
		RecordLength: 900 + 4100 + 30000, FragmentCount: 3,
	}, []FragmentDescriptor{
		{FragmentIndex: 0, EncodedLength: 900},
		{FragmentIndex: 1, EncodedLength: 4100},
		{FragmentIndex: 2, EncodedLength: 30000},
	}); err != nil {
		t.Fatalf("fragment lengths vs plan: %v", err)
	}
}

// TestLayoutLimitIncompatible: a budget that cannot carry even a one-byte
// part under the conservative probe keeps the batch READY with a named
// error, never a truncated plan.
func TestLayoutLimitIncompatible(t *testing.T) {
	req := layoutRequestFixture(t, 0)
	header, _ := NatsHeaderSize(req.Stream)
	scalars := frameScalars{chainSequence: req.SequenceBase, fragmentIndex: 0, fragmentCount: 3}
	smallestPart := partEncodedSize(scalars, math.MaxUint64, math.MaxUint64, 900, 1)
	req.Budget = header + smallestPart - 1
	_, err := ComputeLayout(req)
	if !errors.Is(err, ErrLimitIncompatible) {
		t.Fatalf("error %v does not carry ErrLimitIncompatible", err)
	}
	if req.Budget == 0 {
		t.Fatalf("fixture broken")
	}
	if _, err := ComputeLayout(LayoutRequest{Budget: 0, Stream: "S"}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("zero budget error %v", err)
	}
}

// TestLayoutDeterministic: the same request plans to the same entries and
// the same digest on every attempt.
func TestLayoutDeterministic(t *testing.T) {
	req := layoutRequestFixture(t, 600)
	first, err := ComputeLayout(req)
	if err != nil {
		t.Fatalf("first plan: %v", err)
	}
	var planDigest Hash32
	planSum := sha256.Sum256([]byte("determinism"))
	copy(planDigest[:], planSum[:])
	firstDigest, err := LayoutManifestDigest(planDigest, first.Entries)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := ComputeLayout(req)
		if err != nil {
			t.Fatalf("repeat %d: %v", i, err)
		}
		if len(again.Entries) != len(first.Entries) {
			t.Fatalf("repeat %d: entry count changed", i)
		}
		for j := range again.Entries {
			if again.Entries[j] != first.Entries[j] {
				t.Fatalf("repeat %d: entry %d changed: %+v vs %+v", i, j, again.Entries[j], first.Entries[j])
			}
		}
		digest, err := LayoutManifestDigest(planDigest, again.Entries)
		if err != nil {
			t.Fatalf("repeat %d digest: %v", i, err)
		}
		if digest != firstDigest {
			t.Fatalf("repeat %d: digest changed", i)
		}
	}
}

func TestLayoutManifestDigestGolden(t *testing.T) {
	entries := []LayoutManifestEntry{
		{Ordinal: 0, FragmentIndex: 0, Form: FormComplete, Capacity: 900, PartCount: 1, FragmentTotalBytes: 900},
		{Ordinal: 0, FragmentIndex: 1, Form: FormPart, Capacity: 1000, PartCount: 5, FragmentTotalBytes: 4100},
	}
	var planDigest Hash32
	copy(planDigest[:], mustHex(t, goldenPlanDigestHex))
	digest, err := LayoutManifestDigest(planDigest, entries)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	var want Hash32
	copy(want[:], mustHex(t, goldenLayoutManifestDigest))
	if digest != want {
		t.Fatalf("layout manifest digest %s, want %s", digest, want)
	}
	// The digest is a pure function of plan digest and entries.
	again, _ := LayoutManifestDigest(planDigest, entries)
	if again != digest {
		t.Fatalf("digest is not reproducible")
	}
}

func TestLayoutManifestEntryRoundTrip(t *testing.T) {
	entry := LayoutManifestEntry{Ordinal: 3, FragmentIndex: 2, Form: FormPart,
		Capacity: 512, PartCount: 8, FragmentTotalBytes: 4096} // ceil(4096/512)=8
	encoded, err := entry.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(encoded) != LayoutManifestEntrySize || LayoutManifestEntrySize != 41 {
		t.Fatalf("entry size %d, want 41", len(encoded))
	}
	decoded, err := DecodeLayoutManifestEntry(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded != entry {
		t.Fatalf("round trip changed the entry")
	}
	if _, err := DecodeLayoutManifestEntry(encoded[:40]); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("short entry error %v", err)
	}
}

// TestLayoutEntryValidateRejects: the COMPLETE/PART arithmetic inside one
// entry is validated, not trusted.
func TestLayoutEntryValidateRejects(t *testing.T) {
	cases := []struct {
		name  string
		entry LayoutManifestEntry
		want  error
	}{
		{"form 2", LayoutManifestEntry{Form: 2, Capacity: 10, PartCount: 1, FragmentTotalBytes: 10}, ErrClosedRegistry},
		{"zero total", LayoutManifestEntry{Form: FormComplete, Capacity: 0, PartCount: 1}, ErrInvalidValue},
		{"total over bound", LayoutManifestEntry{Form: FormComplete, Capacity: 65537, PartCount: 1, FragmentTotalBytes: 65537}, ErrInvalidValue},
		{"COMPLETE capacity mismatch", LayoutManifestEntry{Form: FormComplete, Capacity: 9, PartCount: 1, FragmentTotalBytes: 10}, ErrInvalidValue},
		{"COMPLETE part count", LayoutManifestEntry{Form: FormComplete, Capacity: 10, PartCount: 2, FragmentTotalBytes: 10}, ErrInvalidValue},
		{"PART zero capacity", LayoutManifestEntry{Form: FormPart, Capacity: 0, PartCount: 10, FragmentTotalBytes: 10}, ErrInvalidValue},
		{"PART capacity over total", LayoutManifestEntry{Form: FormPart, Capacity: 11, PartCount: 1, FragmentTotalBytes: 10}, ErrInvalidValue},
		{"PART wrong count", LayoutManifestEntry{Form: FormPart, Capacity: 4, PartCount: 2, FragmentTotalBytes: 10}, ErrInvalidValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.entry.Validate()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Validate error %v does not carry %v", err, tc.want)
			}
		})
	}
}

// TestLayoutRejectsBadRequests: closed-batch shape errors, checked
// arithmetic and fragment bounds, each with a named error.
func TestLayoutRejectsBadRequests(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*LayoutRequest)
		want error
	}{
		{"no events", func(r *LayoutRequest) { r.Events = nil }, ErrInvalidValue},
		{"first ordinal not 0", func(r *LayoutRequest) { r.Events[0].Ordinal = 1 }, ErrInvalidValue},
		{"ordinal gap", func(r *LayoutRequest) {
			r.Events = append(r.Events, EventFragments{Ordinal: 2, FragmentLengths: []uint64{10, 10, 10}})
		}, ErrInvalidValue},
		{"duplicate ordinal", func(r *LayoutRequest) {
			r.Events = append(r.Events, EventFragments{Ordinal: 0, FragmentLengths: []uint64{10, 10, 10}})
		}, ErrInvalidValue},
		{"event without fragments", func(r *LayoutRequest) { r.Events[0].FragmentLengths = nil }, ErrInvalidValue},
		{"zero-length fragment", func(r *LayoutRequest) { r.Events[0].FragmentLengths[0] = 0 }, ErrInvalidValue},
		{"oversize fragment", func(r *LayoutRequest) { r.Events[0].FragmentLengths[0] = FragmentMaxBytes + 1 }, ErrInvalidValue},
		{"zero publication epoch", func(r *LayoutRequest) { r.PublicationEpoch = ID16{} }, ErrInvalidValue},
		{"unknown network family", func(r *LayoutRequest) { r.Network.Family = 9 }, ErrClosedRegistry},
		{"empty stream", func(r *LayoutRequest) { r.Stream = "" }, ErrInvalidValue},
		{"sequence overflow", func(r *LayoutRequest) {
			r.SequenceBase = math.MaxUint64
			r.Events = append(r.Events, EventFragments{Ordinal: 1, FragmentLengths: []uint64{10, 10, 10}})
		}, ErrOverflow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := layoutRequestFixture(t, 600) // a fresh request per case: no shared slices
			tc.mut(&req)
			_, err := ComputeLayout(req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ComputeLayout error %v does not carry %v", err, tc.want)
			}
		})
	}
}

// TestPartBoundsRejectsOutOfRange covers the checked offset arithmetic of
// the part cover.
func TestPartBoundsRejectsOutOfRange(t *testing.T) {
	entry := LayoutManifestEntry{Form: FormPart, Capacity: 1000, PartCount: 5, FragmentTotalBytes: 4100}
	if _, _, err := entry.PartBounds(5); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("index at count error %v", err)
	}
	start, end, err := entry.PartBounds(4)
	if err != nil {
		t.Fatalf("last part: %v", err)
	}
	if start != 4000 || end != 4100 {
		t.Fatalf("last part bounds [%d,%d), want [4000,4100)", start, end)
	}
	invalid := entry
	invalid.Capacity = 1025 // ceil(4100/1025)=4, no longer the stored count 5
	if _, _, err := invalid.PartBounds(0); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("inconsistent entry error %v", err)
	}
}

// TestLayoutPlanEchoesBudgetInHeaderSize keeps the budget honest: the plan
// sizes frames against the fixed header bytes of its own stream.
func TestLayoutPlanEchoesBudgetInHeaderSize(t *testing.T) {
	req := layoutRequestFixture(t, 600)
	plan, err := ComputeLayout(req)
	if err != nil {
		t.Fatalf("layout: %v", err)
	}
	header, _ := NatsHeaderSize(req.Stream)
	for _, entry := range plan.Entries {
		if entry.Form != FormPart {
			continue
		}
		scalars := frameScalars{chainSequence: req.SequenceBase + entry.Ordinal,
			fragmentIndex: entry.FragmentIndex, fragmentCount: 3}
		if partEncodedSize(scalars, math.MaxUint64, math.MaxUint64,
			entry.FragmentTotalBytes, entry.Capacity)+header > req.Budget {
			t.Fatalf("entry %+v does not fit the budget under the conservative probe", entry)
		}
	}
}

func TestLayoutBudgetBelowHeaderRejected(t *testing.T) {
	stream := "TEST"
	header, _ := NatsHeaderSize(stream)
	for _, budget := range []uint64{1, 100, header} {
		_, err := ComputeLayout(LayoutRequest{
			Budget: budget, Stream: stream, Network: goldenNetwork(t),
			Events: []EventFragments{
				{Ordinal: 1, FragmentLengths: []uint64{10}},
			},
		})
		if !errors.Is(err, ErrLimitIncompatible) {
			t.Fatalf("budget %d (header %d): expected ErrLimitIncompatible, got %v", budget, header, err)
		}
	}
}
