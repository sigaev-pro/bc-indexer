module github.com/sigaev-pro/bc-indexer/core/journal/producer

// Go 1.25 is the oldest go directive among the accepted Geth, BSC and Bor
// modules, so every client build graph can compile this module unchanged.
go 1.25

require github.com/sigaev-pro/bc-indexer/core/journal/storage v0.0.0

// The storage seam is a repository-local module without a published version.
replace github.com/sigaev-pro/bc-indexer/core/journal/storage => ../storage
