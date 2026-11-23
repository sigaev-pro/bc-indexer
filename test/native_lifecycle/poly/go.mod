module github.com/sigaev-pro/bc-indexer/test/native_lifecycle/poly

// Go 1.25 is the oldest go directive among the accepted Geth, BSC and Bor
// modules, so every client build graph can compile this module unchanged.
go 1.25

require (
	github.com/sigaev-pro/bc-indexer/adapters/bor/finality_observer v0.0.0
	github.com/sigaev-pro/bc-indexer/core/journal/producer v0.0.0
	github.com/sigaev-pro/bc-indexer/core/journal/storage v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0
)

require github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0 // indirect

// The composed modules are repository-local without published versions.
replace github.com/sigaev-pro/bc-indexer/adapters/bor/finality_observer => ../../../adapters/bor/finality_observer

replace github.com/sigaev-pro/bc-indexer/core/journal/producer => ../../../core/journal/producer

replace github.com/sigaev-pro/bc-indexer/core/journal/storage => ../../../core/journal/storage

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../../../protocol/block

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../../../protocol/profiles
