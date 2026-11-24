module github.com/sigaev-pro/bc-indexer/cmd/extractor-rpc

// The real-network adapter of the Sepolia demonstration: it composes the
// repository-local semantic, codec, destination and publisher modules and
// the pinned Geth and NATS clients. Go 1.25 is the oldest go directive
// among the accepted client modules, so every build graph can compile this
// module unchanged.
go 1.26.0

require (
	github.com/ethereum/go-ethereum v1.17.10
	github.com/nats-io/nats.go v1.54.0
	github.com/sigaev-pro/bc-indexer/gateway/destination v0.0.0
	github.com/sigaev-pro/bc-indexer/gateway/publisher v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/codec v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/proto v0.0.0
	github.com/sigaev-pro/bc-indexer/protocol/semantic v0.0.0
)

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/ProjectZKM/Ziren/crates/go-runtime/zkvm_runtime v0.0.0-20251001021608-1fe7b43fc4d6 // indirect
	github.com/StackExchange/wmi v1.2.1 // indirect
	github.com/bits-and-blooms/bitset v1.20.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/consensys/gnark-crypto v0.18.1 // indirect
	github.com/crate-crypto/go-eth-kzg v1.5.0 // indirect
	github.com/deckarep/golang-set/v2 v2.6.0 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.0.1 // indirect
	github.com/ethereum/c-kzg-4844/v2 v2.1.8 // indirect
	github.com/fjl/jsonw v0.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/gorilla/websocket v1.4.2 // indirect
	github.com/holiman/uint256 v1.3.2 // indirect
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/shirou/gopsutil v3.21.4-0.20210419000835-c7a38de76ee5+incompatible // indirect
	github.com/sigaev-pro/bc-indexer/core/delivery v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/balance v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/block v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/compatibility v0.0.0 // indirect
	github.com/sigaev-pro/bc-indexer/protocol/profiles v0.0.0 // indirect
	github.com/supranational/blst v0.3.16 // indirect
	github.com/tklauser/go-sysconf v0.3.12 // indirect
	github.com/tklauser/numcpus v0.6.1 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.46.0 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/exp v0.0.0-20231110203233-9a3e6036ecaa // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)

// The adapter composes the repository-local modules; none of them is
// published, so the local modules are the only source.
replace github.com/sigaev-pro/bc-indexer/core/delivery => ../../core/delivery

replace github.com/sigaev-pro/bc-indexer/gateway/destination => ../../gateway/destination

replace github.com/sigaev-pro/bc-indexer/gateway/publisher => ../../gateway/publisher

replace github.com/sigaev-pro/bc-indexer/protocol/balance => ../../protocol/balance

replace github.com/sigaev-pro/bc-indexer/protocol/block => ../../protocol/block

replace github.com/sigaev-pro/bc-indexer/protocol/codec => ../../protocol/codec

replace github.com/sigaev-pro/bc-indexer/protocol/compatibility => ../../protocol/compatibility

replace github.com/sigaev-pro/bc-indexer/protocol/profiles => ../../protocol/profiles

replace github.com/sigaev-pro/bc-indexer/protocol/proto => ../../protocol/proto

replace github.com/sigaev-pro/bc-indexer/protocol/semantic => ../../protocol/semantic
