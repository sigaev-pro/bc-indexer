module github.com/sigaev-pro/bc-indexer/core/normalization/bsc

// Go 1.25 is the oldest go directive among the accepted Geth, BSC and Bor
// modules, so every client build graph can compile this module unchanged.
go 1.25

require (
	github.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0
)

// The BSC normalization module composes the repository-local phase-1
// models; none of them is published, so the local modules are the only
// source.
replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../../../protocol/balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../../../protocol/block

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../../../protocol/profiles
