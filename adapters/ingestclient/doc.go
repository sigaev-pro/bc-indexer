// Package ingestclient is the producer half of the gateway ingest boundary
// (E03-02.rpc_streaming, client half): the gRPC client of the gateway ingest
// service, the session handshake and the driver that moves every READY batch
// of the producer journal to RETIRED.
//
// Rules this package holds (references are CONTROL_WIRE_CONTRACT and
// DELIVERY_STATE_MACHINE unless noted):
//
//   - The module never imports the gateway's ingest module. It carries
//     byte-identical copies of the two generated files in its nested module
//     ingest, and requires the third-party versions of the accepted BSC
//     graph, because it compiles into the node's dependency graph.
//   - Every mutating request carries the session header of the contract:
//     protocol version 2, a request id, the session token, producer and run
//     ids, connection and admission generations and the binding digest.
//   - The gRPC status is transport only. A typed error answer is acted on by
//     its retry action: RETRY_BACKOFF is a capped exponential backoff with
//     jitter and no attempt limit; RECONNECT opens a new session, after the
//     connection generation was incremented and saved; RESUME_AT_OFFSET
//     continues at the cursor the answer carries; FIX_CONFIG stops the driver
//     with CONFIG_BLOCKED; the stop actions stop it with BLOCKED. Nothing is
//     confirmed or lost by a retry.
//   - Each step is idempotent and a restart repeats the first step the
//     durable state allows. A reservation is never allocated twice, a batch
//     is never published twice under two plans, and a batch is released only
//     after the batch before it is RETIRED.
//   - An acknowledgement is recorded only after all six fields of the
//     IngestAck equal the producer's own records. CLOSED without an
//     acknowledgement is not a delivery, and ALREADY_RETIRED is never taken
//     as one.
//
// The driver keeps one key of its own, driver/meta/connection_generation, in
// the storage of the producer journal; the journal never writes that prefix.
package ingestclient
