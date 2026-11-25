module github.com/sigaev-pro/bc-indexer/gateway/journal

// The Gateway service baseline is Go 1.27.2 (COMPONENT_STACK, "Gateway /
// publisher"); this module never compiles into the native client graphs.
go 1.27.2

require (
	github.com/sigaev-pro/bc-indexer/core/journal/storage v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/codec v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/proto v0.0.0
)

require (
	github.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0 // indirect
)

// Repository-local modules without published versions.
replace github.com/sigaev-pro/bc-indexer/core/journal/storage => ../../core/journal/storage

// The frame accounting asks the schema module for the exact encoded size of
// a TransportFrame, and the tests build their manifests with the producer's
// own builder in the codec module. Both compose the repository-local model
// modules, none of which is published.
replace github.com/sigaev-pro/bc-indexer/protocol/proto => ../../protocol/proto

replace github.com/sigaev-pro/bc-indexer/protocol/codec => ../../protocol/codec

replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../../protocol/balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../../protocol/block

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../../protocol/profiles

replace github.com/sigaev-pro/bc-indexer/protocol/semantic => ../../protocol/semantic
