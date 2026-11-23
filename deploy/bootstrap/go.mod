module github.com/sigaev-pro/bc-indexer/deploy/bootstrap

// The host-side bootstrap of the deployment stack: binding generation,
// trust material and NATS accounts, stream creation with the D77
// identity metadata, and the runtime verification commands. This module
// runs only on the host, so it may compose the repository's authoritative
// gateway/destination library; the mock services never import it.
go 1.26.0

require (
	github.com/nats-io/nats.go v1.54.0
	github.com/sigaev-pro/bc-indexer/deploy v0.0.0
	github.com/sigaev-pro/bc-indexer/gateway/destination v0.0.0
)

require (
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/proto v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/sigaev-pro/bc-indexer/deploy => ../

replace github.com/sigaev-pro/bc-indexer/gateway/destination => ../../gateway/destination

replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../../protocol/balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../../protocol/block

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../../protocol/profiles

replace github.com/sigaev-pro/bc-indexer/protocol/proto => ../../protocol/proto

replace github.com/sigaev-pro/bc-indexer/protocol/semantic => ../../protocol/semantic
