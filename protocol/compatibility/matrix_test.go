package compatibility

import (
	"errors"
	"strings"
	"testing"
)

func TestCanReadIdentityAlwaysHolds(t *testing.T) {
	m := newTestMatrix(t)
	if err := m.CanRead(idCur, idCur); err != nil {
		t.Fatalf("a version must read its own records: %v", err)
	}
	if err := m.CanRead(idOld, idOld); err != nil {
		t.Fatalf("a version must read its own records: %v", err)
	}
}

func TestCanReadAllowsRegisteredClaimOnly(t *testing.T) {
	m := newTestMatrix(t)
	// The compatibility claim under test: the current version declares
	// that it reads the older one.
	if err := m.CanRead(idOld, idCur); err != nil {
		t.Fatalf("a registered read claim must hold: %v", err)
	}
	// The older build never reads the newer format, and no claim exists.
	err := m.CanRead(idCur, idOld)
	if err == nil {
		t.Fatal("reading a newer version without a claim must be rejected")
	}
	var r *ErrReadUnsupported
	if !errors.As(err, &r) || r.From != idCur || r.To != idOld {
		t.Fatalf("must be ErrReadUnsupported naming both versions, got %v", err)
	}
	if !strings.Contains(err.Error(), string(idCur)) || !strings.Contains(err.Error(), string(idOld)) {
		t.Fatalf("message must name both versions: %v", err)
	}
}

func TestReadClaimsAreNotTransitive(t *testing.T) {
	m := newTestMatrix(t)
	// idNext reads idCur and idCur reads idOld, but idNext makes no claim
	// about idOld: compatibility is per registered pair.
	if err := m.CanRead(idOld, idNext); err == nil {
		t.Fatal("an unclaimed pair must be rejected even when a chain exists")
	}
	if err := m.CanRead(idCur, idNext); err != nil {
		t.Fatalf("the direct claim must still hold: %v", err)
	}
}

func TestCanReplayMirrorsReadClaim(t *testing.T) {
	m := newTestMatrix(t)
	if err := m.CanReplay(idOld, idCur); err != nil {
		t.Fatalf("replay of a readable older version must be allowed: %v", err)
	}
	if err := m.CanReplay(idCur, idCur); err != nil {
		t.Fatalf("replay of a version's own records must be allowed: %v", err)
	}
	err := m.CanReplay(idUnread, idCur)
	if err == nil {
		t.Fatal("replay of an unreadable version must be rejected")
	}
	var r *ErrReplayUnsupported
	if !errors.As(err, &r) || r.From != idUnread || r.To != idCur {
		t.Fatalf("must be ErrReplayUnsupported naming both versions, got %v", err)
	}
	if !strings.Contains(err.Error(), string(idUnread)) || !strings.Contains(err.Error(), string(idCur)) {
		t.Fatalf("message must name both versions: %v", err)
	}
}

func TestCanWriteNewAlwaysTargetsCurrentVersion(t *testing.T) {
	m := newTestMatrix(t)
	if m.Current() != idCur {
		t.Fatalf("current must be %q, got %q", idCur, m.Current())
	}
	err := m.CanWriteNew(idCur, idNext)
	if err == nil {
		t.Fatal("new writes must not target a non-current version")
	}
	var w *ErrWriteTargetNotCurrent
	if !errors.As(err, &w) || w.Target != idNext || w.Current != idCur {
		t.Fatalf("must be ErrWriteTargetNotCurrent naming target and current, got %v", err)
	}
	// Even the store's own older format is not a legal new-write target.
	err = m.CanWriteNew(idOld, idOld)
	if err == nil {
		t.Fatal("new writes must not extend an older version")
	}
	if !errors.As(err, &w) || w.Target != idOld {
		t.Fatalf("targeting the store's older format must name it, got %v", err)
	}
}

func TestCanWriteNewSameVersionAllowed(t *testing.T) {
	m := newTestMatrix(t)
	if err := m.CanWriteNew(idCur, idCur); err != nil {
		t.Fatalf("new writes in the current version must be allowed: %v", err)
	}
}

func TestCanWriteNewCompatibleTransitionAllowed(t *testing.T) {
	m := newTestMatrix(t)
	// The current version reads the store's older format, so switching
	// new writes to the current version keeps the remaining metadata
	// readable.
	if err := m.CanWriteNew(idOld, idCur); err != nil {
		t.Fatalf("a readable transition must be allowed: %v", err)
	}
}

func TestCanWriteNewRejectsReadOnlyCurrentVersion(t *testing.T) {
	ro := fakeVersion{id: "record-readonly", reads: []VersionID{idOld}, writes: false}
	m, err := NewMatrix(ro)
	if err != nil {
		t.Fatalf("NewMatrix: %v", err)
	}
	err = m.CanWriteNew("record-readonly", "record-readonly")
	if err == nil {
		t.Fatal("a current version registered read-only must reject new writes")
	}
	var w *ErrVersionNotWritable
	if !errors.As(err, &w) || w.ID != "record-readonly" {
		t.Fatalf("must be ErrVersionNotWritable naming the version, got %v", err)
	}
}

func TestIncompatibleStoreTransitionRejected(t *testing.T) {
	m := newTestMatrix(t)
	err := m.CanWriteNew(idUnread, idCur)
	if err == nil {
		t.Fatal("a transition to a format that cannot read the store must be rejected")
	}
	var s *ErrStoreTransitionRejected
	if !errors.As(err, &s) || s.From != idUnread || s.To != idCur {
		t.Fatalf("must be ErrStoreTransitionRejected naming both versions, got %v", err)
	}
	if !strings.Contains(err.Error(), string(idUnread)) || !strings.Contains(err.Error(), string(idCur)) {
		t.Fatalf("message must name both versions: %v", err)
	}
}
