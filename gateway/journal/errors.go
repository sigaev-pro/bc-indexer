package journal

import "errors"

// Errors of the gateway journal, named after the wire error registry of
// CONTROL_WIRE_CONTRACT ("Ошибки протокола"); the ingest RPC boundary maps
// them one-to-one onto the typed RPC errors.
var (
	// ErrAlreadyRetired answers Prepare/Publish for a batch at or below the
	// retired prefix without creating keys.
	ErrAlreadyRetired = errors.New("gateway journal: batch already retired")
	// ErrOffsetMismatch is a chunk gap; the expected cursor is the current
	// covered length returned alongside.
	ErrOffsetMismatch = errors.New("gateway journal: chunk offset mismatch")
	// ErrImmutableConflict is the same identity with different immutable
	// bytes (conflicting rewrite, second digest, changed declaration).
	ErrImmutableConflict = errors.New("gateway journal: immutable content conflict")
	// ErrStateConflict is a transition the durable state does not allow
	// (MODE_CONFLICT / DRAIN_REQUIRED class).
	ErrStateConflict = errors.New("gateway journal: state does not allow this transition")
	// ErrModeConflict is the wrong prepare/publish mode for the batch (for
	// example FinalizePublish on an IMPORT_SEALED batch).
	ErrModeConflict = errors.New("gateway journal: mode conflict")
	// ErrManifestMismatch is a manifest stream whose digest or completeness
	// check failed.
	ErrManifestMismatch = errors.New("gateway journal: manifest digest mismatch")
	// ErrLayoutMismatch is a layout manifest digest mismatch.
	ErrLayoutMismatch = errors.New("gateway journal: layout manifest digest mismatch")
	// ErrPayloadDigest is a chunk whose SHA-256 does not match the declared
	// digest.
	ErrPayloadDigest = errors.New("gateway journal: payload digest mismatch")
	// ErrIdentityMismatch is a plan or seed identity that does not match the
	// stored reservation.
	ErrIdentityMismatch = errors.New("gateway journal: identity mismatch")
	// ErrInvalidSeed is a malformed or non-validatable IMPORT_SEALED seed.
	ErrInvalidSeed = errors.New("gateway journal: invalid reservation seed")
	// ErrBatchNotFound is an unknown batch.
	ErrBatchNotFound = errors.New("gateway journal: batch not found")
	// ErrCounterOverflow is a checked counter overflow before commit.
	ErrCounterOverflow = errors.New("gateway journal: counter overflow")
	// ErrChunkTooLarge is a data chunk over MaxDataChunkBytes.
	ErrChunkTooLarge = errors.New("gateway journal: data chunk exceeds 64 KiB")
	// ErrIncomplete is a closing attempt with an incomplete stream.
	ErrIncomplete = errors.New("gateway journal: stream incomplete")
	// errCorrupt is an unreadable store record; fatal for the owner.
	errCorrupt = errors.New("gateway journal: corrupt record")
)
