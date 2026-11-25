module github.com/sigaev-pro/bc-indexer/test/ingest_handshake

// The handshake tests run the real gateway ingest server, a Go 1.27.2
// module with gRPC v1.84.0 (COMPONENT_STACK, "Gateway / publisher"); they
// never compile into the native clients.
go 1.27.2

require (
	github.com/sigaev-pro/bc-indexer/adapters/ingestclient v0.0.0
	github.com/sigaev-pro/bc-indexer/config v0.0.0
	github.com/sigaev-pro/bc-indexer/core/journal/producer v0.0.0
	github.com/sigaev-pro/bc-indexer/core/journal/storage v0.0.0
	github.com/sigaev-pro/bc-indexer/gateway/destination v0.0.0
	github.com/sigaev-pro/bc-indexer/gateway/ingest v0.0.0
	github.com/sigaev-pro/bc-indexer/gateway/journal v0.0.0
	github.com/sigaev-pro/bc-indexer/gateway/session v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/codec v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/proto v0.0.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/compatibility v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0 // indirect
)

// DECISION (contract reference: CONTROL_WIRE_CONTRACT, the RPC table; one
// wire definition for both owners): the generated wire file is linked once.
// The client module carries byte-identical copies of the two generated
// files of gateway/ingest in its nested module ingest. Two copies of one
// proto file cannot be linked into one binary: each registers the file at
// package init and the second registration panics before any test runs.
// These tests therefore map the nested module onto gateway/ingest, and the
// driver here uses the gateway's own generated types.
//
// TEMPORARY until feature/s6b-gateway-frame-accounting (bfb1e27) merges:
// gateway/ingest, gateway/journal and the nested module point at a pinned
// export of that commit under .project/runtime (git-ignored), created with
//   git archive bfb1e27 gateway/ingest gateway/journal | tar -x -C .project/runtime/s6b-bfb1e27
// When the branch merges, both ingest lines become ../../gateway/ingest
// and the gateway/journal line becomes ../../gateway/journal.
replace github.com/sigaev-pro/bc-indexer/adapters/ingestclient/ingest => ../../.project/runtime/s6b-bfb1e27/gateway/ingest

replace github.com/sigaev-pro/bc-indexer/gateway/ingest => ../../.project/runtime/s6b-bfb1e27/gateway/ingest

replace github.com/sigaev-pro/bc-indexer/gateway/journal => ../../.project/runtime/s6b-bfb1e27/gateway/journal

// The repository-local modules are not published, so the local modules are
// the only source.
replace github.com/sigaev-pro/bc-indexer/adapters/ingestclient => ../../adapters/ingestclient

replace github.com/sigaev-pro/bc-indexer/config => ../../config

replace github.com/sigaev-pro/bc-indexer/core/journal/producer => ../../core/journal/producer

replace github.com/sigaev-pro/bc-indexer/core/journal/storage => ../../core/journal/storage

replace github.com/sigaev-pro/bc-indexer/gateway/destination => ../../gateway/destination

replace github.com/sigaev-pro/bc-indexer/gateway/session => ../../gateway/session

replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../../protocol/balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../../protocol/block

replace github.com/sigaev-pro/bc-indexer/protocol/codec => ../../protocol/codec

replace github.com/sigaev-pro/bc-indexer/protocol/compatibility => ../../protocol/compatibility

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../../protocol/profiles

replace github.com/sigaev-pro/bc-indexer/protocol/proto => ../../protocol/proto

replace github.com/sigaev-pro/bc-indexer/protocol/semantic => ../../protocol/semantic
