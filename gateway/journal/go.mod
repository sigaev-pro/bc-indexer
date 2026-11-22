module github.com/sigaev-pro/bc-indexer/gateway/journal

// The Gateway service baseline is Go 1.27.2 (COMPONENT_STACK, "Gateway /
// publisher"); this module never compiles into the native client graphs.
go 1.27.2

require github.com/sigaev-pro/bc-indexer/core/journal/storage v0.0.0

// The storage seam is a repository-local module without a published version.
replace github.com/sigaev-pro/bc-indexer/core/journal/storage => ../../core/journal/storage
