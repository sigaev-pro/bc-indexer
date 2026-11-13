// This file is Geth-only. The E01-02.lifecycle_seams Geth preparation copies
// it into the Geth tree as extractor/admission/native/lifecycle_test.go. The
// native callers themselves are exercised by the measured probe
// (test/harness/fixtures/lifecycle_geth); these tests pin the pure parts.

package native

import (
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
)

func TestCheckSyncMode(t *testing.T) {
	if err := CheckSyncMode("full"); err != nil {
		t.Fatalf("full sync refused: %v", err)
	}
	for _, mode := range []string{"snap", "", "light"} {
		if err := CheckSyncMode(mode); !errors.Is(err, ErrProfile) {
			t.Fatalf("sync mode %q: %v", mode, err)
		}
	}
}

func TestCheckFlags(t *testing.T) {
	t.Setenv(ConfigEnv, "")
	if err := CheckFlags(true, true, "snap"); err != nil {
		t.Fatalf("a node without the capture config is checked: %v", err)
	}
	t.Setenv(ConfigEnv, "/capture.json")
	for _, c := range []struct {
		dev, target bool
		mode, want  string
	}{{true, false, "full", "geth.dev-sealing"}, {false, true, "full", "geth.sync-target"},
		{false, false, "snap", "geth.snap-sync"}} {
		err := CheckFlags(c.dev, c.target, c.mode)
		if !errors.Is(err, ErrProfile) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
	if err := CheckFlags(false, false, "full"); err != nil {
		t.Fatalf("the capture profile refused full sync: %v", err)
	}
}

func TestNativeIdentity(t *testing.T) {
	saved := BinaryCommit
	defer func() { BinaryCommit = saved }()
	BinaryCommit = strings.Repeat("ab", 20)
	if got, err := NativeIdentity(); err != nil || got != BinaryCommit {
		t.Fatalf("stamp: %q %v", got, err)
	}
	BinaryCommit = "not-a-commit"
	if _, err := NativeIdentity(); !errors.Is(err, ErrIdentity) {
		t.Fatalf("a malformed stamp is accepted: %v", err)
	}
}

func head(n uint64, b byte) decision.Head {
	return decision.Head{Number: n, Hash: strings.Repeat(string("0123456789abcdef"[b&15]), 64)}
}

func TestEvidenceDigests(t *testing.T) {
	members := []decision.Member{{Observation: "p.r.0000000000000001"}}
	base := CanonicalEvidence("geth.set-canonical", head(3, 1), head(4, 2), members)
	if len(base) != 64 || base != CanonicalEvidence("geth.set-canonical", head(3, 1), head(4, 2), members) {
		t.Fatalf("canonical evidence is not a stable digest: %s", base)
	}
	for _, other := range []string{
		CanonicalEvidence("geth.known-block", head(3, 1), head(4, 2), members),
		CanonicalEvidence("geth.set-canonical", head(2, 1), head(4, 2), members),
		CanonicalEvidence("geth.set-canonical", head(3, 1), head(4, 3), members),
		CanonicalEvidence("geth.set-canonical", head(3, 1), head(4, 2), nil),
	} {
		if other == base {
			t.Fatal("a changed caller, head or member set keeps the canonical evidence")
		}
	}
	clear := ClearEvidence(lifecycle.Safe, "geth.set-head", head(5, 4))
	if clear == ClearEvidence(lifecycle.Finalized, "geth.set-head", head(5, 4)) ||
		clear == ClearEvidence(lifecycle.Safe, "geth.set-head", head(5, 5)) {
		t.Fatal("the clear evidence ignores the field or the old reference")
	}
	if NotAppliedEvidence("aa", [32]byte{1}) == NotAppliedEvidence("aa", [32]byte{2}) {
		t.Fatal("the not-applied evidence ignores the native head")
	}
}
