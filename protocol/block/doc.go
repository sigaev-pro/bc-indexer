// Package block is the block metadata and lifecycle schema of
// E02-08.block_metadata (#24) for the ETH, BSC and Polygon (Bor) families.
//
// The model follows MESSAGE_CONTRACT (envelope identity, block events,
// lifecycle), CURRENT_SEMANTICS (BlockRecord, header fields, source
// timestamps, finality evidence references) and CONTROL_WIRE_CONTRACT
// (activation, batch and observation identities). Those documents fix the
// meaning; this package fixes the typed Go shape that E02-02.schemas and the
// later validators build on.
//
// Invariants held by this package:
//
//   - Counts are observations. SourceTransactionCount, the Parlia system
//     transaction count and the Bor StateSync count record what the chosen
//     client's native representation reported. No count ever asserts that a
//     block is complete (card invariant; D25 excludes BlockComplete and
//     CoverageUpdated). Zero is a valid observed value.
//   - Empty blocks are first-class records. Validation imposes no minimum
//     transaction count; an empty block is published with count 0.
//   - Absence is explicit. Every optional field is an Optional[T]; a present
//     zero value is never confused with an absent field, and every enum has
//     no valid zero value.
//   - A block record carries its activation state as references. Admission,
//     native decision, observed head and finality markers are typed
//     references to native observations, never derived truth. Status
//     derivation from ancestry belongs to E02-03.lifecycle_validator.
//   - A reorged-away activation is immutable. Returning of the same block
//     after REMOVED creates a new activation record with a new activation_id;
//     the old record and its lifecycle are never mutated or reactivated.
//   - DeliveryBatchRef links prepared messages to delivery progress only. It
//     references block activations and its own frontier (WAL_SAVED, then
//     NATS_ACKED) and carries no completeness or coverage statement.
//
// The package imports only the Go standard library. It performs no I/O.
//
// The JSON encoding in encode.go is the package's local, version-tagged
// record encoding used by the portable tests. It is not the D73 wire codec
// and assigns no wire field numbers; E02-02.schemas and E02-02.codec own the
// fragment registry.
package block
