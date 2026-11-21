package compatibility

import (
	"errors"
	"testing"
)

// pinned mirrors protocol/proto/registry.go: one entry per pinned version
// constant, named after it, with the identifier this package registers.
var pinned = []struct {
	version EncodingVersion
	id      VersionID
}{
	{TransportV2, "transport/2"},
	{RecordFormatV1, "record_format/1"},
	{LayoutV2, "layout/2"},
	{HeaderV2, "header/2"},
	{ControlV2, "control/2"},
	{ManifestV2, "manifest/2"},
}

func TestPinnedVersionsMatchProtoRegistry(t *testing.T) {
	if len(pinned) != 6 {
		t.Fatalf("the proto registry pins six versions, got %d", len(pinned))
	}
	for _, p := range pinned {
		if p.version.ID() != p.id {
			t.Fatalf("%T must carry the registry identifier %q, got %q", p.version, p.id, p.version.ID())
		}
		if !p.version.WritesNew() {
			t.Fatalf("%s must be writable: it is the current version of its kind", p.id)
		}
		if !p.version.Reads(p.id) {
			t.Fatalf("%s must read its own records", p.id)
		}
	}
}

func TestPinnedVersionIDsAreDistinct(t *testing.T) {
	seen := make(map[VersionID]bool, len(pinned))
	for _, p := range pinned {
		if seen[p.id] {
			t.Fatalf("identifier %q is pinned more than once", p.id)
		}
		seen[p.id] = true
	}
}

func TestPinnedVersionsClaimNoEarlierVersions(t *testing.T) {
	// Every pinned version is the first version of its kind: no earlier
	// version exists to claim, so the read set is empty and identity is
	// the only readability. When a future codec version lands (for
	// example a transport v3), it registers with an explicit claim of the
	// versions it reads; nothing is assumed until then.
	for _, p := range pinned {
		for _, other := range pinned {
			if p.id == other.id {
				continue
			}
			if p.version.Reads(other.id) {
				t.Fatalf("%s must not claim to read the unrelated version %s", p.id, other.id)
			}
		}
		if p.version.Reads("record-v1") || p.version.Reads("") {
			t.Fatalf("%s must not claim to read unknown versions", p.id)
		}
	}
}

func TestPinnedPairsReadAndReplayClaims(t *testing.T) {
	// One matrix holding all six pinned versions answers every ordered
	// pair: identity reads and replays; any different pair is rejected
	// because no pinned version claims another. Replay always mirrors the
	// read claim of the same pair (read implies replay for these
	// immutable encodings).
	m, err := NewMatrix(TransportV2, RecordFormatV1, LayoutV2, HeaderV2, ControlV2, ManifestV2)
	if err != nil {
		t.Fatalf("NewMatrix: %v", err)
	}
	for _, from := range pinned {
		for _, to := range pinned {
			readErr := m.CanRead(from.id, to.id)
			replayErr := m.CanReplay(from.id, to.id)
			if from.id == to.id {
				if readErr != nil || replayErr != nil {
					t.Fatalf("(%s -> %s): identity must read and replay, got %v / %v", from.id, to.id, readErr, replayErr)
				}
				continue
			}
			var r *ErrReadUnsupported
			if !errors.As(readErr, &r) || r.From != from.id || r.To != to.id {
				t.Fatalf("(%s -> %s): must be ErrReadUnsupported naming both, got %v", from.id, to.id, readErr)
			}
			var p *ErrReplayUnsupported
			if !errors.As(replayErr, &p) || p.From != from.id || p.To != to.id {
				t.Fatalf("(%s -> %s): must be ErrReplayUnsupported naming both, got %v", from.id, to.id, replayErr)
			}
		}
	}
}

func TestNewProtocolVersionValidatesItsClaims(t *testing.T) {
	cases := []struct {
		name  string
		id    VersionID
		reads []VersionID
	}{
		{"empty identifier", "", nil},
		{"empty claimed version", "transport/3", []VersionID{"transport/2", ""}},
		{"self claim", "transport/3", []VersionID{"transport/3"}},
		{"duplicate claim", "transport/3", []VersionID{"transport/2", "transport/2"}},
	}
	for _, tc := range cases {
		_, err := NewProtocolVersion(tc.id, tc.reads, true)
		if err == nil {
			t.Fatalf("%s: must be rejected", tc.name)
		}
		var inv *ErrInvalidVersion
		if !errors.As(err, &inv) {
			t.Fatalf("%s: must be ErrInvalidVersion, got %T", tc.name, err)
		}
	}
}

func TestProtocolVersionClaimsArePerPair(t *testing.T) {
	v1, err := NewProtocolVersion("transport/1", nil, true)
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	v2, err := NewProtocolVersion("transport/2", []VersionID{"transport/1"}, true)
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	v3, err := NewProtocolVersion("transport/3", []VersionID{"transport/2"}, true)
	if err != nil {
		t.Fatalf("v3: %v", err)
	}
	if !v2.Reads("transport/1") || !v3.Reads("transport/2") {
		t.Fatal("an explicitly claimed version must be readable")
	}
	// Claims are per pair and never transitive: v3 reads v2, v2 reads v1,
	// and v3 still does not read v1.
	if v3.Reads("transport/1") {
		t.Fatal("a claim chain must not make an unclaimed pair readable")
	}
	if v1.Reads("transport/2") {
		t.Fatal("an older version never reads a newer one by default")
	}
}

func TestProtocolVersionFlowsThroughPolicy(t *testing.T) {
	// The concrete type end-to-end: a hypothetical history of the
	// transport kind, decided by the matrix, the guard and the two claim
	// families.
	old, err := NewProtocolVersion("transport/1", nil, true)
	if err != nil {
		t.Fatalf("transport/1: %v", err)
	}
	cur, err := NewProtocolVersion("transport/2", []VersionID{"transport/1"}, true)
	if err != nil {
		t.Fatalf("transport/2: %v", err)
	}
	next, err := NewProtocolVersion("transport/3", nil, true)
	if err != nil {
		t.Fatalf("transport/3: %v", err)
	}
	m, err := NewMatrix(next, old, cur)
	if err != nil {
		t.Fatalf("NewMatrix: %v", err)
	}
	p := NewPolicy(m)

	// The current version transport/3 reads no earlier version, so old
	// obligations are not continuable: the pending bytes and their
	// version stay untouched and wait for a compatible build.
	d := DurableState{Version: "transport/2", Mode: ModeDrain, Pending: 4, MaxDelivery: StateSealed}
	err = p.ContinuePending(d)
	var rp *ErrReplayUnsupported
	if !errors.As(err, &rp) || rp.From != "transport/2" || rp.To != "transport/3" {
		t.Fatalf("continuing unreadable pending must be ErrReplayUnsupported naming both versions, got %v", err)
	}
	// The current version always continues its own obligations.
	if err := p.ContinuePending(DurableState{Version: "transport/3", Mode: ModeDrain, Pending: 1, MaxDelivery: StateReady}); err != nil {
		t.Fatalf("identity pending must be continuable: %v", err)
	}

	// transport/3 claims no earlier version, so the pair transport/2 ->
	// transport/3 is incompatible and exercises the two claim families on
	// the same quiet drained snapshot.
	quietDrained := DurableState{Version: "transport/2", Mode: ModeDrained, Pending: 0, MaxDelivery: StateNone}
	if err := p.PreflightTransition(quietDrained, "transport/3", ClaimWirePlan); err != nil {
		t.Fatalf("an incompatible wire/plan upgrade after DRAINED must be allowed: %v", err)
	}
	err = p.PreflightTransition(quietDrained, "transport/3", ClaimStoreFormat)
	var s *ErrStoreTransitionRejected
	if !errors.As(err, &s) || s.From != "transport/2" || s.To != "transport/3" {
		t.Fatalf("the same pair as a store format change must be rejected before any write, got %v", err)
	}
}
