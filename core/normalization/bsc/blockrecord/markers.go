// The BSC finality markers of one block record: the decision evidence and
// the attestation read of one import projected onto the marker skeleton of
// protocol/block (CURRENT_SEMANTICS, D62).
//
// BSC has two native finality surfaces. The header-derived one is the
// snapshot vote attestation: the merged VoteData, whose target is the
// justified block and whose source is the finalized block. The other is
// the native effective finalized result of the decision evidence, which the
// VotePool may have advanced beyond the attested source. The projection
// keeps them apart:
//
//   - BscMarkers.Attested is present exactly when the attestation read
//     answered ATTESTED. It carries the decided block as the snapshot head
//     and the justified and finalized anchors of the attestation.
//   - BscMarkers.EffectiveFinalized is the finalized head of the decision
//     evidence, as a known anchor observed live.
//   - BscMarkers.EffectiveBasis is HEADER_SNAPSHOT with an attestation and
//     GENESIS_FALLBACK without one.
//
// The builder derives no finality of its own and invents no finalized
// anchor: every anchor is one the native evidence reported. The VotePool
// evidence (early votes, quorum counts) belongs to E05-01.bsc_finality and
// is not claimed here.
package blockrecord

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// markers projects the observed heads and the attestation read of one
// import onto the BSC finality markers. The heads were validated by
// validate; an unknown attestation status is rejected here.
func markers(in Input) (block.BscMarkers, error) {
	switch in.Attestation.Status {
	case StatusAttested:
		return block.BscMarkers{
			Attested: block.Some(block.AttestedSnapshot{
				SnapshotHead: anchor(in.NewHead),
				Justified:    anchor(in.Attestation.Justified),
				Finalized:    anchor(in.Attestation.Finalized),
			}),
			EffectiveFinalized: block.Known(anchor(in.Finalized), block.OriginNativeLive),
			EffectiveBasis:     block.BscBasisHeaderSnapshot,
		}, nil
	case StatusGenesisFallback:
		return block.BscMarkers{
			EffectiveFinalized: block.Known(anchor(in.Finalized), block.OriginNativeLive),
			EffectiveBasis:     block.BscBasisGenesisFallback,
		}, nil
	}
	return block.BscMarkers{}, fmt.Errorf("%w: attestation status %q", block.ErrInvalidValue, in.Attestation.Status)
}
