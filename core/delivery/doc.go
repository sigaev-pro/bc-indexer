// Package delivery is the shared seam layer of the E03 delivery engine:
// the durable identities both sides exchange (batch id, IngestAck,
// ReleaseAck), the gateway-side batch states, the session fence every
// mutating step carries, and the minimal journal interfaces the producer
// and gateway stores expose to the release, maintenance and error engines
// (DELIVERY_STATE_MACHINE, "Identity, modes and atomicity";
// CONTROL_WIRE_CONTRACT, "Служебные запросы и ответы").
//
// The store interfaces are deliberately narrow: the producer store exposes
// the durable state machine INTENT/STAGING/READY/SEALED/ACKED_CLEANUP plus
// the release outbox and retired prefix, and the gateway store exposes
// PREPARING/PREPARED/RECEIVING/CLOSED/NATS_ACKED/RELEASING plus the retired
// baseline. Every mutating method of both interfaces is one atomic sync
// commit — the closing-marker atomicity of the contract. Chunked payload
// transfer itself belongs to E03-02.rpc_streaming; this layer records only
// what survives it.
//
// The concrete RocksDB stores of E03-01 are built in a parallel block; the
// MemoryProducer and MemoryGateway adapters here implement the same
// interfaces with plain maps so the release, maintenance, publisher and
// fault-matrix tests run without RocksDB. The memory adapters model the
// durable/RAM boundary explicitly: staged chunk writes live in a RAM
// overlay that Reopen discards, while every interface mutation is durable
// by construction, so a test can prove which bytes a crash may lose.
//
// The package imports only the repository-local schema and version modules
// plus the Go standard library. NATS is never imported here: the publisher
// seam lives in gateway/publisher.
package delivery
