// Package destination is E03-03.destination: the destination validation of
// the publisher gateway. It owns the immutable DestinationBinding
// descriptor (parse, digest, route), the online account/stream/route check
// and the D77 stream metadata identity check
// (CONTROL_WIRE_CONTRACT, "DestinationBinding file" and "Stream metadata
// identity D77"; DELIVERY_STATE_MACHINE, "DestinationBinding до первого
// capture").
//
// The binding is fixed before the first capture: the environment writes the
// descriptor file, both owners store a reference to it, and the adapter
// needs no NATS credentials. The gateway re-checks the live target on every
// connection or reconnect and before admitting a CLOSED batch to
// publication. A missing or mismatched target is CONFIG_BLOCKED: pending
// batches and their bytes are kept and never redirected to another
// destination. An unreachable target is a retryable blocked state, never
// proof that the metadata is missing or matches.
//
// The online half is a seam: Remote is the minimal surface the pinned
// nats.go 1.54.0 client exposes for account and stream information
// (COMPONENT_STACK: the NATS SDK lives only in the gateway). MemoryRemote
// is the in-memory adapter for tests and for the smoke corpus; the real
// client is wired at the process boundary.
//
// The package imports only the repository-local schema module and the Go
// standard library.
package destination
