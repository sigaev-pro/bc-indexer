module github.com/sigaev-pro/bc-indexer/core/journal/storage/rocksdb

// This module needs CGO and a native librocksdb, so it is separate from
// core/journal/storage: the seam, the producer and the Gateway journals build
// without it. Go 1.25 is the oldest go directive among the accepted Geth, BSC
// and Bor modules, so the client build graphs compile this module unchanged.
go 1.25

require (
	github.com/linxGnu/grocksdb v1.10.2
	github.com/sigaev-pro/bc-indexer/core/journal/producer v0.0.0
	github.com/sigaev-pro/bc-indexer/core/journal/storage v0.0.0
)

// Both are repository-local modules without a published version. The
// producer journal is used by tests only, to restart the real journal on the
// durable store; nothing outside _test.go files imports it.
replace (
	github.com/sigaev-pro/bc-indexer/core/journal/producer => ../../producer
	github.com/sigaev-pro/bc-indexer/core/journal/storage => ../
)
