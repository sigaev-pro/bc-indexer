module github.com/sigaev-pro/bc-indexer/test/lifecycle_validator

go 1.25

require github.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0

// The semantic package is not published; the local module is the only source.
replace github.com/sigaev-pro/bc-indexer/protocol/semantic => ../../protocol/semantic
