module github.com/sigaev-pro/bc-indexer/core/normalization/eth

// Go 1.25 is the oldest go directive among the accepted Geth, BSC and Bor
// modules, so every client build graph can compile this module unchanged.
go 1.25

require (
	github.com/sigaev-pro/bc-indexer/core/capture v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0
)

// The normalizer composes the repository-local protocol models and the
// capture seam; none of them is published, so the local modules are the
// only source.
replace github.com/sigaev-pro/bc-indexer/core/capture => ../../capture

replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../../../protocol/balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../../../protocol/block

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../../../protocol/profiles

replace github.com/sigaev-pro/bc-indexer/protocol/semantic => ../../../protocol/semantic
