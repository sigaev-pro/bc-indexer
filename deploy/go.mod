module github.com/sigaev-pro/bc-indexer/deploy

// The mock services of the deployment stack. This module is deliberately
// self-contained: it depends only on the pinned NATS client and a YAML
// decoder, never on repository-local modules, so its Docker build context
// stays inside deploy/.
go 1.26.0

require (
	github.com/nats-io/nats.go v1.54.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)
