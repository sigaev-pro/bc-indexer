package borlifecycle

import (
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
)

var (
	zero = strings.Repeat("00", 32)
	ones = strings.Repeat("11", 32)
)

// The expected digests are computed independently from the README formula
// (lifecycle_bor.py vectors).
func TestEvidenceDigestVectors(t *testing.T) {
	canonical := Evidence{Caller: "bor.set-head", Kind: decision.Canonical, Operation: "set-head",
		OldHead: decision.Head{Number: 7, Hash: ones}, NewHead: decision.Head{Number: 6, Hash: zero}}
	if got := EvidenceDigest(canonical); got != "0ce2b5cfbc69c5b0690ce4771c9e3f3acc17272e2d7adc2dcb31d17875f9d132" {
		t.Fatalf("canonical evidence digest %s", got)
	}
	finality := Evidence{Caller: "bor.milestone", Kind: decision.Finality, Operation: "last-finality",
		OldHead: decision.Head{Number: 4, Hash: ones}, NewHead: decision.Head{Number: 4, Hash: ones}, Key: "6c617374",
		New: "ab"}
	if got := EvidenceDigest(finality); got != "a057dbf15e72046543e635f8b592f5e96bb720b4be0af2dbbc3d12e954ed8837" {
		t.Fatalf("finality evidence digest %s", got)
	}
	changed := finality
	changed.Old = "ab"
	if EvidenceDigest(changed) == EvidenceDigest(finality) {
		t.Fatal("the old value is not bound")
	}
}

func TestAddRefusalOnlyForRefuseCallers(t *testing.T) {
	m, err := lifecycle.Callers()
	if err != nil {
		t.Fatal(err)
	}
	n := &Node{m: m, outside: map[string]uint64{}}
	for _, id := range []string{"bor.import", "bor.milestone", "bor.eth-call", "geth.snap-sync", "bor.unknown"} {
		if err := n.AddRefusal(id); !errors.Is(err, lifecycle.ErrInvalid) {
			t.Fatalf("AddRefusal(%s) = %v", id, err)
		}
	}
	if err := n.AddRefusal("bor.seal-write"); err != nil {
		t.Fatal(err)
	}
	if err := n.RefuseOutside("bor.parallel"); err == nil {
		t.Fatal("RefuseOutside did not refuse")
	}
	if n.outside["bor.seal-write"] != 1 || n.outside["bor.parallel"] != 1 || len(n.outside) != 2 {
		t.Fatalf("outside refusals %v", n.outside)
	}
}

func TestDisabledAndNilNode(t *testing.T) {
	Disable()
	if n, err := Open(Inputs{}, nil); n != nil || err != nil {
		t.Fatalf("Open without Enable = %v, %v", n, err)
	}
	var n *Node
	if err := n.GoLive(nil); err != nil {
		t.Fatal(err)
	}
	if err := n.Abort(); err != nil {
		t.Fatal(err)
	}
	n.Close()
}
