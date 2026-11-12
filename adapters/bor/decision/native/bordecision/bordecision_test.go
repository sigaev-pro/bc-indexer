package bordecision

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
)

func decisionObservation() admission.Observation { return admission.Observation{Key: "bor.run-1.0000000000000000"} }

func TestTransitionCarriesTheNativeFields(t *testing.T) {
	parent := &types.Header{Number: big.NewInt(4), Difficulty: big.NewInt(1)}
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(5), ParentHash: parent.Hash(),
		Root: common.HexToHash("0x1234"), Difficulty: big.NewInt(1)})
	got := transition(core.ExtractorTransition{Kind: core.ExtractorImport, Block: block, OldHead: parent,
		NewHead: block.Header(), LocalTD: big.NewInt(9), ExternTD: big.NewInt(10)})
	if got.Kind != decision.Import || got.Block != headOf(block.Header()) || got.OldHead.Number != 4 ||
		got.OldHead.Hash != hexOf(parent.Hash()) || got.NewHead != headOf(block.Header()) ||
		got.ChildRoot != hexOf(block.Root()) || got.LocalTD != "9" || got.ExternTD != "10" {
		t.Fatalf("transition %+v", got)
	}
	if len(got.Block.Hash) != 64 || strings.HasPrefix(got.Block.Hash, "0x") {
		t.Fatalf("block hash %q is not 64 lowercase hex digits", got.Block.Hash)
	}
}

func TestHashOfRefusesMalformedHashes(t *testing.T) {
	for _, value := range []string{"", "0x" + strings.Repeat("0", 64), strings.Repeat("0", 63), strings.Repeat("g", 64)} {
		if _, err := hashOf(value); !errors.Is(err, decision.ErrInvalid) {
			t.Fatalf("hashOf(%q): %v", value, err)
		}
	}
	hash := common.HexToHash("0xabcd")
	if got, err := hashOf(hexOf(hash)); err != nil || got != hash {
		t.Fatalf("hashOf round trip: %v %v", got, err)
	}
}

func TestNoMirrorAppliesNothing(t *testing.T) {
	if applied, err := (NoMirror{}).Applied(0, strings.Repeat("0", 64)); applied || err != nil {
		t.Fatalf("NoMirror applied: %v %v", applied, err)
	}
}

func TestLiveCompletesNothing(t *testing.T) {
	if err := (live{}).Complete(nil, decisionObservation()); !errors.Is(err, decision.ErrState) {
		t.Fatalf("live completion: %v", err)
	}
}
