package poly

import (
	"github.com/sigaev-pro/bc-indexer/adapters/bor/finality_observer"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// The supporting-stack fixtures of the finality observer: the pinned
// sources_file of this fixture and the two own-source seams. Only the
// native observation path runs here; the L1 GC read stays unused, and its
// seam answers with its identity only.

func fixtureSources() *finalityobserver.Sources {
	src, err := finalityobserver.ParseSources([]byte(`{
  "schema_version": 1,
  "l1": {
    "deployment_id": "` + idOf("l1-deployment").String() + `",
    "role": "POLYGON_L1_EXECUTION",
    "endpoint": "http://polygon-l1:8545",
    "network": {"family": "EVM", "chain_id": "` + block.UInt256(hashOf("poly-chain-id")).String() + `", "genesis_hash": "` + hashOf("poly-genesis").String() + `"}
  },
  "heimdall": {
    "deployment_id": "` + idOf("heimdall-deployment").String() + `",
    "role": "POLYGON_HEIMDALL",
    "endpoint": "http://heimdall:1317",
    "chain_id": "heimdall-fixture",
    "genesis_document_sha256": "` + hashOf("heimdall-genesis").String() + `"
  }
}`))
	if err != nil {
		panic("fixture sources: " + err.Error())
	}
	return src
}

// fixtureHeimdall answers the own-Heimdall identity.
type fixtureHeimdall struct{}

func (fixtureHeimdall) StoreIdentity() block.Id16 { return idOf("heimdall-deployment") }

func (fixtureHeimdall) Identity() finalityobserver.HeimdallIdentity {
	return finalityobserver.HeimdallIdentity{
		ChainID:               "heimdall-fixture",
		GenesisDocumentSHA256: hashOf("heimdall-genesis"),
	}
}

func (fixtureHeimdall) ChainParams() (finalityobserver.ChainParams, finalityobserver.Response, error) {
	return finalityobserver.ChainParams{}, finalityobserver.Response{}, errGCSourceUnused
}

func (fixtureHeimdall) CheckpointParams() (finalityobserver.CheckpointParams, finalityobserver.Response, error) {
	return finalityobserver.CheckpointParams{}, finalityobserver.Response{}, errGCSourceUnused
}

func (fixtureHeimdall) MilestoneLatest() (finalityobserver.SourceMilestone, finalityobserver.Response, error) {
	return finalityobserver.SourceMilestone{}, finalityobserver.Response{}, errGCSourceUnused
}

func (fixtureHeimdall) CheckpointLatest() (finalityobserver.SourceCheckpoint, finalityobserver.Response, error) {
	return finalityobserver.SourceCheckpoint{}, finalityobserver.Response{}, errGCSourceUnused
}

// fixtureL1 answers the own Polygon L1 identity.
type fixtureL1 struct{}

func (fixtureL1) NodeID() block.Id16 { return idOf("l1-deployment") }

func (fixtureL1) Network() block.NetworkIdentity { return polyNetwork }

func (fixtureL1) Finalized() (block.AnchorRef, error) {
	return block.AnchorRef{}, errGCSourceUnused
}

func (fixtureL1) IsCanonicalAncestor(ancestor, descendant block.AnchorRef) (bool, error) {
	return false, errGCSourceUnused
}

func (fixtureL1) HeaderBlocks(calldata []byte, at block.AnchorRef) ([]byte, error) {
	return nil, errGCSourceUnused
}

type gcSourceError struct{}

func (gcSourceError) Error() string {
	return "poly fixture: the L1 GC lookup is not part of this lifecycle fixture"
}

var errGCSourceUnused error = gcSourceError{}
