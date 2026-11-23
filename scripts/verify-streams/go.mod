module github.com/sigaev-pro/bc-indexer/scripts/verify-streams

// The read-back verifier of the multi-network verification: a tiny
// client-side tool that inspects the demo JetStream streams. It pins the
// same NATS client as the adapter; nothing else is required.
go 1.26.0

require github.com/nats-io/nats.go v1.54.0

require (
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)
