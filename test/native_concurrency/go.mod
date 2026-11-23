module github.com/sigaev-pro/bc-indexer/test/native_concurrency

// The matrix composes gateway/session, whose Gateway service baseline is
// Go 1.27.2 (COMPONENT_STACK, "Gateway / publisher"); this module never
// compiles into the native clients.
go 1.27.2

require (
	github.com/sigaev-pro/bc-indexer/core/delivery v0.0.0
	github.com/sigaev-pro/bc-indexer/gateway/destination v0.0.0
	github.com/sigaev-pro/bc-indexer/gateway/publisher v0.0.0
	github.com/sigaev-pro/bc-indexer/gateway/session v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/proto v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0
	github.com/sigaev-pro/bc-indexer/test/lifecycle_validator v0.0.0
)

require (
	github.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/compatibility v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0 // indirect
)

// The race matrix composes the repository-local delivery, session,
// publisher and semantic modules; none of them is published, so the local
// modules are the only source.
replace github.com/sigaev-pro/bc-indexer/core/delivery => ../../core/delivery

replace github.com/sigaev-pro/bc-indexer/gateway/destination => ../../gateway/destination

replace github.com/sigaev-pro/bc-indexer/gateway/publisher => ../../gateway/publisher

replace github.com/sigaev-pro/bc-indexer/gateway/session => ../../gateway/session

replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../../protocol/balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../../protocol/block

replace github.com/sigaev-pro/bc-indexer/protocol/compatibility => ../../protocol/compatibility

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../../protocol/profiles

replace github.com/sigaev-pro/bc-indexer/protocol/proto => ../../protocol/proto

replace github.com/sigaev-pro/bc-indexer/protocol/semantic => ../../protocol/semantic

replace github.com/sigaev-pro/bc-indexer/test/lifecycle_validator => ../lifecycle_validator
