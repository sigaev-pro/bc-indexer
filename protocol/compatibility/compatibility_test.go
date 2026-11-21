package compatibility

import (
	"errors"
	"strings"
	"testing"
)

// fakeVersion is the test double for a codec-owned encoding version: an
// opaque identifier, an explicit list of readability claims and a
// writability flag. It mirrors how E02-02.codec (#27) will register
// concrete versions once it lands.
type fakeVersion struct {
	id     VersionID
	reads  []VersionID
	writes bool
}

func (v fakeVersion) ID() VersionID { return v.id }

func (v fakeVersion) Reads(from VersionID) bool {
	for _, r := range v.reads {
		if r == from {
			return true
		}
	}
	return false
}

func (v fakeVersion) WritesNew() bool { return v.writes }

// Identifiers of the standing scenario: an older readable format, the
// current new-write format, a future candidate that reads the current one
// but not the older one, and an unrelated format nothing reads.
const (
	idOld    VersionID = "record-v1"
	idCur    VersionID = "record-v2"
	idNext   VersionID = "record-v3"
	idUnread VersionID = "record-unrelated"
)

// newTestMatrix builds the standing scenario with idCur current.
func newTestMatrix(t *testing.T) *Matrix {
	t.Helper()
	old := fakeVersion{id: idOld, writes: true}
	cur := fakeVersion{id: idCur, reads: []VersionID{idOld}, writes: true}
	next := fakeVersion{id: idNext, reads: []VersionID{idCur}, writes: true}
	unread := fakeVersion{id: idUnread, writes: true}
	m, err := NewMatrix(cur, old, next, unread)
	if err != nil {
		t.Fatalf("NewMatrix: %v", err)
	}
	return m
}

func TestRegisterRejectsNilAndEmptyVersions(t *testing.T) {
	m, err := NewMatrix(fakeVersion{id: idCur, writes: true})
	if err != nil {
		t.Fatalf("NewMatrix: %v", err)
	}
	err = m.Register(nil)
	if err == nil {
		t.Fatal("a nil implementation must be rejected at registration")
	}
	var invalid *ErrInvalidVersion
	if !errors.As(err, &invalid) {
		t.Fatalf("nil registration must be ErrInvalidVersion, got %T", err)
	}
	if err := m.Register(fakeVersion{id: "", writes: true}); err == nil {
		t.Fatal("an empty identifier must be rejected at registration")
	}
	if !errors.As(err, &invalid) {
		t.Fatalf("empty identifier must be ErrInvalidVersion, got %T", err)
	}
}

func TestRegisterRejectsDuplicateVersion(t *testing.T) {
	m := newTestMatrix(t)
	err := m.Register(fakeVersion{id: idOld, writes: true})
	if err == nil {
		t.Fatal("a duplicate identifier must be rejected")
	}
	var dup *ErrDuplicateVersion
	if !errors.As(err, &dup) || dup.ID != idOld {
		t.Fatalf("duplicate registration must be ErrDuplicateVersion naming %s, got %v", idOld, err)
	}
}

func TestNewMatrixRejectsBadCurrentVersion(t *testing.T) {
	if _, err := NewMatrix(nil); err == nil {
		t.Fatal("a nil current version must fail construction")
	}
	if _, err := NewMatrix(fakeVersion{id: "", writes: true}); err == nil {
		t.Fatal("an empty current identifier must fail construction")
	}
}

func TestUnknownVersionIsRejectedNeverDefaulted(t *testing.T) {
	m := newTestMatrix(t)
	const unknown VersionID = "record-unknown"
	cases := []struct {
		name string
		err  error
	}{
		{"CanRead from unknown", m.CanRead(unknown, idCur)},
		{"CanRead to unknown", m.CanRead(idOld, unknown)},
		{"CanReplay from unknown", m.CanReplay(unknown, idCur)},
		{"CanReplay to unknown", m.CanReplay(idOld, unknown)},
		{"CanWriteNew from unknown", m.CanWriteNew(unknown, idCur)},
		{"CanWriteNew to unknown", m.CanWriteNew(idOld, unknown)},
	}
	for _, tc := range cases {
		if tc.err == nil {
			t.Fatalf("%s: an unknown version must be rejected", tc.name)
		}
		var u *ErrUnknownVersion
		if !errors.As(tc.err, &u) {
			t.Fatalf("%s: must be ErrUnknownVersion, got %T", tc.name, tc.err)
		}
		if u.ID != unknown {
			t.Fatalf("%s: error must name the unknown version %q, got %q", tc.name, unknown, u.ID)
		}
		if !strings.Contains(tc.err.Error(), string(unknown)) {
			t.Fatalf("%s: message must name the unknown version: %v", tc.name, tc.err)
		}
	}
}
