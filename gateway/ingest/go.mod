module github.com/sigaev-pro/bc-indexer/gateway/ingest

// The Gateway service baseline is Go 1.27.2 with gRPC-Go 1.84.0 and
// protobuf-go 1.36.12 (COMPONENT_STACK, "Gateway / publisher" and
// "Генерация Go"); this module never compiles into the native clients.
go 1.27.2

require (
	github.com/sigaev-pro/bc-indexer/core/journal/storage v0.0.0
	github.com/sigaev-pro/bc-indexer/gateway/destination v0.0.0
	github.com/sigaev-pro/bc-indexer/gateway/journal v0.0.0
	github.com/sigaev-pro/bc-indexer/gateway/session v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/codec v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/proto v0.0.0
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260706201446-f0a921348800 // indirect
)

// Repository-local modules without published versions.
replace github.com/sigaev-pro/bc-indexer/core/journal/storage => ../../core/journal/storage

// The switch RPCs validate the desired descriptor with the destination
// module, which composes the repository-local schema modules; none of them
// is published, so the local modules are the only source.
replace github.com/sigaev-pro/bc-indexer/gateway/destination => ../destination

replace github.com/sigaev-pro/bc-indexer/gateway/journal => ../journal

replace github.com/sigaev-pro/bc-indexer/gateway/session => ../session

replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../../protocol/balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../../protocol/block

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../../protocol/profiles

replace github.com/sigaev-pro/bc-indexer/protocol/proto => ../../protocol/proto

replace github.com/sigaev-pro/bc-indexer/protocol/semantic => ../../protocol/semantic

// The tests build records, manifests and frames with the real codec.
replace github.com/sigaev-pro/bc-indexer/protocol/codec => ../../protocol/codec
