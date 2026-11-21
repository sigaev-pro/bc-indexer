module github.com/sigaev-pro/bc-indexer/protocol/proto

// Go 1.25 is the oldest go directive among the accepted Geth, BSC and Bor
// modules, so every client build graph can compile this module unchanged.
go 1.25

require (
	github.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0
)

// The four phase-1 models are repository-local modules without published
// versions; the schema layer composes them but shares no other dependency.
replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../block

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../profiles

replace github.com/sigaev-pro/bc-indexer/protocol/semantic => ../semantic
