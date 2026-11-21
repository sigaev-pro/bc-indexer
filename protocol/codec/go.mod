module github.com/sigaev-pro/bc-indexer/protocol/codec

// Go 1.25 is the oldest go directive among the accepted Geth, BSC and Bor
// modules, so every client build graph can compile this module unchanged.
go 1.25

require github.com/sigaev-pro/bc-indexer/protocol/proto v0.0.0

require (
	github.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0 // indirect
)

// The codec composes the schema layer's pinned tables, digests and layout
// planner behind its seam. protocol/proto itself composes the four phase-1
// models; Go applies replace directives only in the main module, so the
// codec repeats the repository-local replacements for its transitive
// dependencies. None of them has a published version.
replace github.com/sigaev-pro/bc-indexer/protocol/proto => ../proto

replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../block

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../profiles

replace github.com/sigaev-pro/bc-indexer/protocol/semantic => ../semantic
