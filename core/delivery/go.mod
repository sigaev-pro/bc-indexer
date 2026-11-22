module github.com/sigaev-pro/bc-indexer/core/delivery

// Go 1.25 is the oldest go directive among the accepted client modules, so
// every build graph can compile this module unchanged.
go 1.25

require (
	github.com/sigaev-pro/bc-indexer/protocol/compatibility v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/proto v0.0.0
)

require (
	github.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0 // indirect
)

// The delivery engine composes the repository-local schema and version
// modules; none of them is published, so the local modules are the only
// source.
replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../../protocol/balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../../protocol/block

replace github.com/sigaev-pro/bc-indexer/protocol/compatibility => ../../protocol/compatibility

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../../protocol/profiles

replace github.com/sigaev-pro/bc-indexer/protocol/proto => ../../protocol/proto

replace github.com/sigaev-pro/bc-indexer/protocol/semantic => ../../protocol/semantic
