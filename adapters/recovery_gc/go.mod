module github.com/sigaev-pro/bc-indexer/adapters/recovery_gc

// Go 1.25 is the oldest go directive among the accepted Geth, BSC and Bor
// modules, so every client build graph can compile this module unchanged.
go 1.25

require (
	github.com/sigaev-pro/bc-indexer/adapters/lifecycle_registry v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0
)

// The repository-local modules are not published, so the local modules are
// the only source. The composition test uses adapters/lifecycle_registry,
// whose own tests use the E02-03 oracle through its module graph.
replace github.com/sigaev-pro/bc-indexer/adapters/lifecycle_registry => ../lifecycle_registry

replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../../protocol/balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../../protocol/block

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../../protocol/profiles

replace github.com/sigaev-pro/bc-indexer/protocol/proto => ../../protocol/proto

replace github.com/sigaev-pro/bc-indexer/protocol/semantic => ../../protocol/semantic

replace github.com/sigaev-pro/bc-indexer/test/lifecycle_validator => ../../test/lifecycle_validator
