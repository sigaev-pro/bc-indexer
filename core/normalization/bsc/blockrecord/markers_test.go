package blockrecord

import (
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// bscMarkersOf builds the event of in and returns its BSC finality markers.
func bscMarkersOf(t *testing.T, in Input) *block.BscMarkers {
	t.Helper()
	return mustBuild(t, in, 1).Record.InitialSnapshot.Markers.Bsc
}

// With an attestation the record carries the attested snapshot with the
// anchors of the attestation read, the effective finalized anchor of the
// decision evidence, and the basis HEADER_SNAPSHOT.
func TestMarkersAttestedSnapshot(t *testing.T) {
	in := validInput()
	markers := bscMarkersOf(t, in)
	want := block.BscMarkers{
		Attested: block.Some(block.AttestedSnapshot{SnapshotHead: anchor(in.NewHead),
			Justified: anchor(in.Attestation.Justified), Finalized: anchor(in.Attestation.Finalized)}),
		EffectiveFinalized: block.Known(anchor(in.Finalized), block.OriginNativeLive),
		EffectiveBasis:     block.BscBasisHeaderSnapshot,
	}
	if !markers.Attested.Equal(want.Attested) || markers.EffectiveFinalized != want.EffectiveFinalized ||
		markers.EffectiveBasis != want.EffectiveBasis {
		t.Fatalf("markers %+v, want %+v", markers, want)
	}

	// The attested anchors come from the attestation read; the effective
	// finalized anchor comes from the decision evidence.
	in.Attestation.Finalized = Head{Number: 400, Hash: hashOf(0x31)}
	markers = bscMarkersOf(t, in)
	snapshot, ok := markers.Attested.Get()
	if !ok || snapshot.Finalized != (block.AnchorRef{Height: 400, Hash: hashOf(0x31)}) ||
		snapshot.SnapshotHead != anchor(in.NewHead) || snapshot.Justified != anchor(in.Attestation.Justified) {
		t.Fatalf("attested snapshot %+v present %t", snapshot, ok)
	}
	if markers.EffectiveFinalized != block.Known(anchor(in.Finalized), block.OriginNativeLive) {
		t.Fatalf("effective finalized %+v", markers.EffectiveFinalized)
	}
	paths := readPaths(t, mustBuild(t, in, 1))
	base := blockPath + "initial_snapshot.markers.bsc."
	for path, expected := range map[string]string{
		base + "attested.snapshot_head.height": "uint64:420",
		base + "attested.justified.height":     "uint64:419",
		base + "attested.finalized.height":     "uint64:400",
		base + "effective_finalized.kind":      "enum:1",
		base + "effective_finalized.origin":    "enum:1",
		base + "effective_basis":               "enum:1",
	} {
		if got := render(paths[path]); got != expected {
			t.Fatalf("%s is %s, want %s", path, got, expected)
		}
	}
}

// Without an attestation no attested snapshot is invented: the basis is
// GENESIS_FALLBACK and the effective finalized anchor is the genesis block
// the decision evidence reported.
func TestMarkersGenesisFallback(t *testing.T) {
	in := validInput()
	genesis := Head{Number: 0, Hash: in.Network.GenesisHash}
	in.Attestation = Attestation{Status: StatusGenesisFallback, Justified: genesis, Finalized: genesis,
		Digest: hashOf(0x51).String()}
	in.Finalized = genesis
	markers := bscMarkersOf(t, in)
	if _, ok := markers.Attested.Get(); ok {
		t.Fatalf("the fallback invented an attested snapshot: %+v", markers.Attested)
	}
	if markers.EffectiveFinalized != block.Known(anchor(genesis), block.OriginNativeLive) ||
		markers.EffectiveBasis != block.BscBasisGenesisFallback {
		t.Fatalf("markers %+v", markers)
	}
	paths := readPaths(t, mustBuild(t, in, 1))
	for path := range paths {
		if strings.Contains(path, ".attested") {
			t.Fatalf("the fallback carries %s", path)
		}
	}
	base := blockPath + "initial_snapshot.markers.bsc."
	if got := render(paths[base+"effective_basis"]); got != "enum:3" {
		t.Fatalf("effective_basis is %s, want enum:3", got)
	}
	if got := render(paths[base+"effective_finalized.anchor.height"]); got != "uint64:0" {
		t.Fatalf("the effective finalized anchor is %s", got)
	}
}

// The VotePool may have advanced the effective finalized result of the
// decision evidence to the justified block while the attested source stays
// behind. The attested snapshot keeps the header-derived anchors of the
// attestation read, the effective reference carries the advanced head, and
// the basis is the one the plan fixes for an attested import.
func TestMarkersEffectiveFinalizedAheadOfTheAttestedSource(t *testing.T) {
	in := validInput()
	in.Finalized = in.Justified
	markers := bscMarkersOf(t, in)
	snapshot, ok := markers.Attested.Get()
	if !ok || snapshot.Finalized.Height != 418 || snapshot.Justified.Height != 419 {
		t.Fatalf("attested snapshot %+v present %t", snapshot, ok)
	}
	if markers.EffectiveFinalized.Anchor.Height != 419 || markers.EffectiveBasis != block.BscBasisHeaderSnapshot {
		t.Fatalf("effective finalized %+v with basis %s", markers.EffectiveFinalized, markers.EffectiveBasis)
	}
}

func TestMarkersRejectsAnUnknownStatus(t *testing.T) {
	for _, status := range []string{"attested", "ATTESTED ", "VOTE_POOL_QUORUM"} {
		in := validInput()
		in.Attestation.Status = status
		_, err := BuildBlockEvent(in, eventIDOf(in, 1))
		if err == nil || !strings.Contains(err.Error(), "attestation status") {
			t.Fatalf("status %q: %v", status, err)
		}
	}
}
