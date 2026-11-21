package compatibility

import (
	"errors"
	"testing"
)

func TestDefaultRegistryRegistersEveryPinnedVersion(t *testing.T) {
	r := NewDefaultRegistry()
	kinds := []struct {
		name    string
		m       *Matrix
		current VersionID
	}{
		{"Transport", r.Transport, TransportV2.ID()},
		{"RecordFormat", r.RecordFormat, RecordFormatV1.ID()},
		{"Layout", r.Layout, LayoutV2.ID()},
		{"Header", r.Header, HeaderV2.ID()},
		{"Control", r.Control, ControlV2.ID()},
		{"Manifest", r.Manifest, ManifestV2.ID()},
	}
	for _, k := range kinds {
		if k.m == nil {
			t.Fatalf("%s: matrix must be built", k.name)
		}
		if k.m.Current() != k.current {
			t.Fatalf("%s: current must be %q, got %q", k.name, k.current, k.m.Current())
		}
		// Presence is proven by deciding over the version: the identity
		// pair reads, replays and accepts new writes in both families.
		if err := k.m.CanRead(k.current, k.current); err != nil {
			t.Fatalf("%s: %s must be registered, got %v", k.name, k.current, err)
		}
		if err := k.m.CanReplay(k.current, k.current); err != nil {
			t.Fatalf("%s: %s must be registered, got %v", k.name, k.current, err)
		}
		for _, claim := range []ClaimType{ClaimWirePlan, ClaimStoreFormat} {
			if err := k.m.CanWriteNew(k.current, k.current, claim); err != nil {
				t.Fatalf("%s: new writes in %s must be allowed (%s), got %v", k.name, k.current, claim, err)
			}
		}
	}
}

func TestDefaultRegistryVersionsList(t *testing.T) {
	r := NewDefaultRegistry()
	vs := r.Versions()
	if len(vs) != 6 {
		t.Fatalf("the registry must expose six pinned versions, got %d", len(vs))
	}
	seen := make(map[VersionID]bool)
	for _, v := range vs {
		id := v.ID()
		if id == "" || seen[id] {
			t.Fatalf("version list must be non-empty and duplicate-free, got %q twice", id)
		}
		seen[id] = true
	}
	for _, p := range pinned {
		if !seen[p.id] {
			t.Fatalf("the version list must contain %s", p.id)
		}
	}
}

func TestDefaultRegistryRejectsDuplicateRegistration(t *testing.T) {
	r := NewDefaultRegistry()
	// Re-registering a pinned version into its own kind's matrix is a
	// duplicate; the first registration stands.
	cases := []struct {
		name string
		m    *Matrix
		v    EncodingVersion
	}{
		{"Transport", r.Transport, TransportV2},
		{"RecordFormat", r.RecordFormat, RecordFormatV1},
		{"Layout", r.Layout, LayoutV2},
		{"Header", r.Header, HeaderV2},
		{"Control", r.Control, ControlV2},
		{"Manifest", r.Manifest, ManifestV2},
	}
	for _, tc := range cases {
		err := tc.m.Register(tc.v)
		var dup *ErrDuplicateVersion
		if !errors.As(err, &dup) || dup.ID != tc.v.ID() {
			t.Fatalf("%s: re-registering %s must be ErrDuplicateVersion naming it, got %v", tc.name, tc.v.ID(), err)
		}
		if tc.m.Current() != tc.v.ID() {
			t.Fatalf("%s: the first registration must stand", tc.name)
		}
	}
}

func TestDefaultRegistryCrossKindQuestionIsUnknown(t *testing.T) {
	r := NewDefaultRegistry()
	// A cross-kind question is a category error: on the transport matrix
	// the record format's identifier is unknown, and the decision is the
	// safe rejection, never a fallback.
	err := r.Transport.CanRead(RecordFormatV1.ID(), TransportV2.ID())
	var u *ErrUnknownVersion
	if !errors.As(err, &u) || u.ID != RecordFormatV1.ID() {
		t.Fatalf("must be ErrUnknownVersion naming the cross-kind version, got %v", err)
	}
	err = r.Layout.CanWriteNew(LayoutV2.ID(), ManifestV2.ID(), ClaimWirePlan)
	if !errors.As(err, &u) || u.ID != ManifestV2.ID() {
		t.Fatalf("must be ErrUnknownVersion naming the cross-kind target, got %v", err)
	}
}
