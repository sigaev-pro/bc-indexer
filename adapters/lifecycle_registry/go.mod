module github.com/sigaev-pro/bc-indexer/adapters/lifecycle_registry

// Go 1.25 is the oldest go directive among the accepted Geth, BSC and Bor
// modules, so every client build graph can compile this module unchanged.
go 1.25

require (
	github.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0
	github.com/sigaev-pro/bc-indexer/test/lifecycle_validator v0.0.0
)

require (
	github.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/proto v0.0.0 // indirect
)

// The test oracle of E02-03 and the semantic identity model are not
// published, so the local modules are the only source.
replace github.com/sigaev-pro/bc-indexer/protocol/semantic => ../../protocol/semantic

replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../../protocol/balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../../protocol/block

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../../protocol/profiles

replace github.com/sigaev-pro/bc-indexer/protocol/proto => ../../protocol/proto

replace github.com/sigaev-pro/bc-indexer/test/lifecycle_validator => ../../test/lifecycle_validator
