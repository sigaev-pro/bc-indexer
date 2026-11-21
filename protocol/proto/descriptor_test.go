package proto

import (
	"bytes"
	"errors"
	"math"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// Golden digests from the independent Python implementation.
const (
	goldenRecordDigestHex         = "8f8132b639a79da537d24f0573f47e6dc985845fc7820c3a7c6cd62c6267ce4d"
	goldenFragmentManifestDictHex = "3b2e50605ce395438059b686af277ff730b611f20d83a5a8b5b7e3f083e1c1ea"
)

func hexHash32(t *testing.T, s string) Hash32 {
	t.Helper()
	var out Hash32
	copy(out[:], mustHex(t, s))
	return out
}

// TestBuildEventDescriptorGolden checks the descriptor arithmetic against
// the Python vectors: record length over all fragments, record digest over
// the content fragments (seal excluded) and the fragment manifest digest.
func TestBuildEventDescriptorGolden(t *testing.T) {
	event, frags, err := BuildEventDescriptor(0, goldenEventID(t), FamilyBlock, KindApplied,
		goldenFragments(t))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if event.RecordLength != 15+200+12 {
		t.Fatalf("record length %d, want 227", event.RecordLength)
	}
	if event.RecordDigest != hexHash32(t, goldenRecordDigestHex) {
		t.Fatalf("record digest %s, want %s", event.RecordDigest, goldenRecordDigestHex)
	}
	if event.FragmentManifestDigest != hexHash32(t, goldenFragmentManifestDictHex) {
		t.Fatalf("fragment manifest digest %s, want %s",
			event.FragmentManifestDigest, goldenFragmentManifestDictHex)
	}
	if event.FragmentCount != 3 || len(frags) != 3 {
		t.Fatalf("fragment count %d with %d descriptors", event.FragmentCount, len(frags))
	}
	// The seal participates in the length but not in the record digest.
	sealOnly, _, err := BuildEventDescriptor(0, goldenEventID(t), FamilyBlock, KindApplied,
		[][]byte{goldenFragments(t)[0], goldenFragments(t)[1],
			bytes.Repeat([]byte{0xEE}, 12)})
	if err != nil {
		t.Fatalf("build with a different seal: %v", err)
	}
	if sealOnly.RecordDigest != event.RecordDigest {
		t.Fatalf("record digest changed with the seal fragment")
	}
	if sealOnly.FragmentManifestDigest == event.FragmentManifestDigest {
		t.Fatalf("manifest digest ignored the seal fragment")
	}
	if err := ValidateFragments(event, frags); err != nil {
		t.Fatalf("built descriptors do not validate: %v", err)
	}
}

func TestEventDescriptorRoundTrip(t *testing.T) {
	event := EventDescriptor{
		Ordinal: 41, EventID: goldenEventID(t), Family: FamilyTx, Kind: KindRemoved,
		RecordLength: 600, RecordDigest: digest32(t, "record"),
		FragmentCount: 3, FragmentManifestDigest: digest32(t, "manifest"),
	}
	encoded, err := event.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(encoded) != EventDescriptorSize || EventDescriptorSize != 132 {
		t.Fatalf("event descriptor size %d, want 132", len(encoded))
	}
	decoded, err := DecodeEventDescriptor(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded != event {
		t.Fatalf("round trip changed the descriptor: %+v vs %+v", decoded, event)
	}
	// Reproducibility: encoding the decoded value is byte-identical.
	again, err := decoded.Encode()
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if !bytes.Equal(encoded, again) {
		t.Fatalf("descriptor encoding is not reproducible")
	}
}

func TestFragmentDescriptorRoundTrip(t *testing.T) {
	d := FragmentDescriptor{FragmentIndex: 2, EncodedLength: 65536,
		FragmentSHA256: digest32(t, "fragment")}
	encoded := d.Encode()
	if len(encoded) != FragmentDescriptorSize || FragmentDescriptorSize != 48 {
		t.Fatalf("fragment descriptor size %d, want 48", len(encoded))
	}
	decoded, err := DecodeFragmentDescriptor(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded != d {
		t.Fatalf("round trip changed the descriptor")
	}
	if _, err := DecodeFragmentDescriptor(encoded[:47]); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("short descriptor error %v", err)
	}
}

// TestEventDescriptorValidateRejects: unknown registry values are errors
// and there is no OTHER; the record shape must be carriable.
func TestEventDescriptorValidateRejects(t *testing.T) {
	valid := EventDescriptor{Ordinal: 0, EventID: goldenEventID(t), Family: FamilyTx,
		Kind: KindApplied, RecordLength: 3, RecordDigest: Hash32{},
		FragmentCount: 3, FragmentManifestDigest: Hash32{}}
	cases := []struct {
		name string
		mut  func(*EventDescriptor)
		want error
	}{
		{"family 0", func(d *EventDescriptor) { d.Family = 0 }, ErrClosedRegistry},
		{"family 5", func(d *EventDescriptor) { d.Family = 5 }, ErrClosedRegistry},
		{"family 65535", func(d *EventDescriptor) { d.Family = 65535 }, ErrClosedRegistry},
		{"kind 0", func(d *EventDescriptor) { d.Kind = 0 }, ErrClosedRegistry},
		{"kind 4", func(d *EventDescriptor) { d.Kind = 4 }, ErrClosedRegistry},
		{"zero event id", func(d *EventDescriptor) { d.EventID = EventID{} }, ErrInvalidValue},
		{"fragment count 2", func(d *EventDescriptor) { d.FragmentCount = 2 }, ErrFragments},
		{"record length below fragment count", func(d *EventDescriptor) { d.RecordLength = 2 }, ErrFragments},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := valid
			tc.mut(&d)
			err := d.Validate()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Validate error %v does not carry %v", err, tc.want)
			}
		})
	}
}

func TestFragmentDescriptorBounds(t *testing.T) {
	if err := (FragmentDescriptor{FragmentIndex: 0, EncodedLength: 0}.Validate()); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("zero length accepted")
	}
	if err := (FragmentDescriptor{EncodedLength: FragmentMaxBytes + 1}.Validate()); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("length over bound accepted")
	}
	if err := (FragmentDescriptor{EncodedLength: FragmentMaxBytes}.Validate()); err != nil {
		t.Fatalf("boundary length rejected: %v", err)
	}
}

// TestValidateFragmentsEnforcesManifestRules covers continuity, the count
// match, the exact length sum and the checked sum overflow.
func TestValidateFragmentsEnforcesManifestRules(t *testing.T) {
	event, frags, err := BuildEventDescriptor(0, goldenEventID(t), FamilyTx, KindApplied,
		goldenFragments(t))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := ValidateFragments(event, frags); err != nil {
		t.Fatalf("valid list rejected: %v", err)
	}
	gap := append([]FragmentDescriptor{}, frags...)
	gap[2].FragmentIndex = 4
	if err := ValidateFragments(event, gap); !errors.Is(err, ErrFragments) {
		t.Fatalf("index gap error %v", err)
	}
	dup := append([]FragmentDescriptor{}, frags...)
	dup[1].FragmentIndex = 0
	if err := ValidateFragments(event, dup); !errors.Is(err, ErrFragments) {
		t.Fatalf("duplicate index error %v", err)
	}
	short := frags[:2]
	if err := ValidateFragments(event, short); !errors.Is(err, ErrFragments) {
		t.Fatalf("count mismatch error %v", err)
	}
	wrongSum := event
	wrongSum.RecordLength++
	if err := ValidateFragments(wrongSum, frags); !errors.Is(err, ErrFragments) {
		t.Fatalf("sum mismatch error %v", err)
	}
	// A bounded descriptor list can never overflow the checked u64 sum
	// (65536 times any reachable fragment count stays far below 2^64), so
	// a huge record_length is a mismatch, never a wrapped equality.
	huge := event
	huge.RecordLength = math.MaxUint64
	if err := ValidateFragments(huge, frags); !errors.Is(err, ErrFragments) {
		t.Fatalf("huge record_length error %v", err)
	}
}

// TestRecordDigestRejectsShape: fewer than header/items/seal fragments and
// out-of-bound fragment lengths are named errors.
func TestRecordDigestRejectsShape(t *testing.T) {
	if _, err := RecordDigest(nil); !errors.Is(err, ErrFragments) {
		t.Fatalf("no fragments error %v", err)
	}
	two := goldenFragments(t)[:2]
	if _, err := RecordDigest(two); !errors.Is(err, ErrFragments) {
		t.Fatalf("two fragments error %v", err)
	}
	empty := goldenFragments(t)
	empty[1] = nil
	if _, err := RecordDigest(empty); !errors.Is(err, ErrFragments) {
		t.Fatalf("empty fragment error %v", err)
	}
	huge := goldenFragments(t)
	huge[1] = make([]byte, FragmentMaxBytes+1)
	if _, err := RecordDigest(huge); !errors.Is(err, ErrFragments) {
		t.Fatalf("oversize fragment error %v", err)
	}
}

// TestFamilyKindRegistryClosure pins registry v1 against the phase-1
// semantic registry: the same closed set, no OTHER, every unknown value an
// error.
func TestFamilyKindRegistryClosure(t *testing.T) {
	families := map[Family]semantic.Family{
		FamilyTx:            semantic.FamilyTx,
		FamilyBlock:         semantic.FamilyBlock,
		FamilyBalanceSystem: semantic.FamilyBalanceSystem,
		FamilyBorLegacy:     semantic.FamilyBorLegacy,
	}
	for wire, sem := range families {
		got, err := familyFromSemantic(sem)
		if err != nil || got != wire {
			t.Fatalf("semantic family %s mapped to %d (err %v)", sem, got, err)
		}
	}
	if _, err := familyFromSemantic(semantic.Family(99)); !errors.Is(err, ErrClosedRegistry) {
		t.Fatalf("unknown semantic family error %v", err)
	}
	kinds := map[Kind]semantic.Kind{
		KindApplied: semantic.KindApplied,
		KindRemoved: semantic.KindRemoved,
		KindStatus:  semantic.KindStatus,
	}
	for wire, sem := range kinds {
		got, err := kindFromSemantic(sem)
		if err != nil || got != wire {
			t.Fatalf("semantic kind %s mapped to %d (err %v)", sem, got, err)
		}
	}
	if _, err := kindFromSemantic(semantic.Kind(99)); !errors.Is(err, ErrClosedRegistry) {
		t.Fatalf("unknown semantic kind error %v", err)
	}
	for _, unknown := range []Family{0, 5, 65535} {
		if unknown.Valid() {
			t.Fatalf("family %d accepted", unknown)
		}
		if _, err := variantFor(unknown, KindApplied); !errors.Is(err, ErrClosedRegistry) {
			t.Fatalf("family %d variant error %v", unknown, err)
		}
	}
	for _, unknown := range []Kind{0, 4, 65535} {
		if unknown.Valid() {
			t.Fatalf("kind %d accepted", unknown)
		}
		if _, err := variantFor(FamilyTx, unknown); !errors.Is(err, ErrClosedRegistry) {
			t.Fatalf("kind %d variant error %v", unknown, err)
		}
	}
}
