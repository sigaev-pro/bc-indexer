module github.com/sigaev-pro/bc-indexer/gateway/ingest

// The Gateway service baseline is Go 1.27.2 with gRPC-Go 1.84.0 and
// protobuf-go 1.36.12 (COMPONENT_STACK, "Gateway / publisher" and
// "Генерация Go"); this module never compiles into the native clients.
go 1.27.2

require (
	github.com/sigaev-pro/bc-indexer/core/journal/storage v0.0.0
	github.com/sigaev-pro/bc-indexer/gateway/journal v0.0.0
	github.com/sigaev-pro/bc-indexer/gateway/session v0.0.0
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.12
)

require (
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260706201446-f0a921348800 // indirect
)

// Repository-local modules without published versions.
replace github.com/sigaev-pro/bc-indexer/core/journal/storage => ../../core/journal/storage

replace github.com/sigaev-pro/bc-indexer/gateway/journal => ../journal

replace github.com/sigaev-pro/bc-indexer/gateway/session => ../session
