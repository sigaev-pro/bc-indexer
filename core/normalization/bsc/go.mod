module github.com/sigaev-pro/bc-indexer/core/normalization/bsc

// Go 1.25 is the oldest go directive among the accepted Geth, BSC and Bor
// modules, so every client build graph can compile this module unchanged.
go 1.25

require (
	github.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/codec v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/proto v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0
)

// The BSC normalization module composes the repository-local phase-1
// models; none of them is published, so the local modules are the only
// source. The blockrecord package adds the schema layer, the semantic
// descriptors and the fragment codec. Go applies replace directives only
// in the main module, so the replacements of the codec's own dependencies
// are repeated here.
replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../../../protocol/balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../../../protocol/block

replace github.com/sigaev-pro/bc-indexer/protocol/codec => ../../../protocol/codec

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../../../protocol/profiles

replace github.com/sigaev-pro/bc-indexer/protocol/proto => ../../../protocol/proto

replace github.com/sigaev-pro/bc-indexer/protocol/semantic => ../../../protocol/semantic
