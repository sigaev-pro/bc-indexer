module github.com/sigaev-pro/bc-indexer/core/normalization/poly

// Go 1.25 is the oldest go directive among the accepted Geth, BSC and Bor
// modules, so every client build graph can compile this module unchanged.
go 1.25

require (
	github.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0
)

// The phase-1 protocol models are repository-local modules without published
// versions; this package composes them and shares no other dependency.
replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../../../protocol/balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../../../protocol/block

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../../../protocol/profiles
