// Package publisher is E03-03.publisher: the NATS publishing half of the
// gateway. It drives one manifest/plan/destination from a CLOSED batch to
// a durable NATS_ACKED and then to the IngestAck the producer is answered
// (DELIVERY_STATE_MACHINE, "Таблица переходов"; CONTROL_WIRE_CONTRACT,
// "TransportFrame layout v2").
//
// NATS itself is a seam: Conn is the minimal publish surface of the pinned
// nats.go 1.54.0 JetStream client (COMPONENT_STACK: the NATS SDK lives
// only in the gateway process). MemoryJetStream is the in-memory adapter —
// subject routing, per-stream dedup by Nats-Msg-Id, the exact header
// layout and message-size limits — used by the tests and the smoke corpus.
// It is a transport double, not a production verifier.
//
// The rules the engine owns: a frame publishes only under its exact saved
// id, bytes and subject, so any retry republishes identical bytes and the
// stream dedups it; every PubAck is verified against the expected stream
// and durably accounted per frame key; NATS_ACKED is synced only when
// every transport part of every fragment of every event of the manifest
// carries a PubAck — a partial set never completes the batch; and the
// internal publisher generation fences late callbacks against the Release
// and SealGeneration barriers.
package publisher
