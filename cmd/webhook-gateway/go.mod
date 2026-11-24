module github.com/sigaev-pro/bc-indexer/cmd/webhook-gateway

// The Bonpay webhook gateway: a self-contained module that depends only on
// the pinned NATS client and the pure-Go SQLite driver, never on
// repository-local modules, so its Docker build context stays inside
// cmd/webhook-gateway.
go 1.26.0

require (
	github.com/nats-io/nats.go v1.34.1
	modernc.org/sqlite v1.63.0
)

require (
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.17.2 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/nats-io/nkeys v0.4.7 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/crypto v0.18.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	modernc.org/libc v1.80.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
