package producer

import "errors"

// Errors of the producer store. They map one-to-one onto the wire error
// registry of CONTROL_WIRE_CONTRACT at the RPC boundary; the store itself
// never invents success answers.
var (
	// ErrStateConflict is a transition that the current durable state does
	// not allow (MODE_CONFLICT / DRAIN_REQUIRED class on the wire).
	ErrStateConflict = errors.New("producer journal: state does not allow this transition")
	// ErrImmutableConflict is the same identity with different immutable
	// content (IMMUTABLE_CONFLICT: bytes under an ID are never changed).
	ErrImmutableConflict = errors.New("producer journal: immutable content conflict")
	// ErrCounterOverflow is a checked counter overflow before any commit;
	// values are never wrapped or reused.
	ErrCounterOverflow = errors.New("producer journal: counter overflow")
	// ErrModeConflict is a capture attempt in a mode that forbids new
	// commitments (only LIVE admits new capture).
	ErrModeConflict = errors.New("producer journal: mode does not allow new capture")
	// ErrOutOfOrderRelease is a late release acknowledgement that would jump
	// over an unfinished earlier batch (Release is strictly by batch_seq).
	ErrOutOfOrderRelease = errors.New("producer journal: release out of batch order")
	// ErrIngestAckMismatch is an IngestAck that does not match the SEALED
	// plan, manifest or destination binding.
	ErrIngestAckMismatch = errors.New("producer journal: IngestAck does not match sealed batch")
	// ErrNotFound is a read of an unknown batch.
	ErrNotFound = errors.New("producer journal: batch not found")
	// ErrOffsetGap is a chunk write that would leave a hole in a stream.
	ErrOffsetGap = errors.New("producer journal: chunk offset beyond current end")
	// ErrBindingRequired is capture before the destination binding was
	// durably saved.
	ErrBindingRequired = errors.New("producer journal: destination binding not set")
	// ErrEmptyBatch is a delivery intent without public events; an empty
	// public batch is never created (INVALID_DESCRIPTOR class).
	ErrEmptyBatch = errors.New("producer journal: batch without public events")
	// ErrCorrupt is an unreadable store record; the owner stops on it
	// (STORE_FATAL class) instead of guessing.
	ErrCorrupt = errCorrupt
)
