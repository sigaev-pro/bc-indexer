package ingest

import (
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/gateway/journal"
	"github.com/sigaev-pro/bc-indexer/gateway/session"
)

// Bounds of one control/chunk RPC (CONTROL_WIRE_CONTRACT, "Размеры, headers,
// память"): the whole RPC including its wrapper is at most 256 KiB, the
// payload fragment of one chunk at most 64 KiB. The wrapper bound is the
// gRPC max receive message size; the chunk bound is a typed error.
const (
	MaxMessageBytes = 256 << 10
	MaxDataChunk    = journal.MaxDataChunkBytes
)

// wireError is the typed error answer of one RPC
// ({code, request_id, owner_state, field_path, retry_action, detail}).
type wireError struct {
	code   ErrorCode
	action RetryAction
	detail string
}

func (e *wireError) Error() string {
	return fmt.Sprintf("%s: %s", e.code.String(), e.detail)
}

// mapJournalError translates a journal or session error into the wire
// registry. Unknown errors become STORE_FATAL: never a silent success.
func mapJournalError(err error) *wireError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, journal.ErrAlreadyRetired):
		return &wireError{ErrorCode_ALREADY_RETIRED, RetryAction_RETRY_UNSPECIFIED, err.Error()}
	case errors.Is(err, journal.ErrOffsetMismatch):
		return &wireError{ErrorCode_OFFSET_MISMATCH, RetryAction_RESUME_AT_OFFSET, err.Error()}
	case errors.Is(err, journal.ErrImmutableConflict):
		return &wireError{ErrorCode_IMMUTABLE_CONFLICT, RetryAction_DIAGNOSE_IMMUTABLE, err.Error()}
	case errors.Is(err, journal.ErrModeConflict):
		return &wireError{ErrorCode_MODE_CONFLICT, RetryAction_RETRY_UNSPECIFIED, err.Error()}
	case errors.Is(err, journal.ErrStateConflict):
		return &wireError{ErrorCode_MODE_CONFLICT, RetryAction_RETRY_BACKOFF, err.Error()}
	case errors.Is(err, journal.ErrManifestMismatch), errors.Is(err, journal.ErrLayoutMismatch):
		return &wireError{ErrorCode_INVALID_DESCRIPTOR, RetryAction_DIAGNOSE_IMMUTABLE, err.Error()}
	case errors.Is(err, journal.ErrPayloadDigest):
		return &wireError{ErrorCode_INVALID_PAYLOAD, RetryAction_RETRY_BACKOFF, err.Error()}
	case errors.Is(err, journal.ErrIdentityMismatch), errors.Is(err, journal.ErrInvalidSeed),
		errors.Is(err, journal.ErrBatchNotFound):
		return &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, err.Error()}
	case errors.Is(err, journal.ErrChunkTooLarge):
		return &wireError{ErrorCode_LIMIT_INCOMPATIBLE, RetryAction_FIX_CONFIG, err.Error()}
	case errors.Is(err, journal.ErrIncomplete):
		return &wireError{ErrorCode_INVALID_DESCRIPTOR, RetryAction_RETRY_BACKOFF, err.Error()}
	case errors.Is(err, journal.ErrCounterOverflow):
		return &wireError{ErrorCode_STORE_FATAL, RetryAction_STOP_LOCAL_OWNER, err.Error()}
	case errors.Is(err, session.ErrSessionStale):
		return &wireError{ErrorCode_SESSION_STALE, RetryAction_RECONNECT, err.Error()}
	case errors.Is(err, session.ErrGenerationConflict):
		return &wireError{ErrorCode_GENERATION_CONFLICT, RetryAction_RECONNECT, err.Error()}
	case errors.Is(err, storage.ErrBatchTooLarge):
		return &wireError{ErrorCode_STORE_FATAL, RetryAction_STOP_LOCAL_OWNER, err.Error()}
	default:
		return &wireError{ErrorCode_STORE_FATAL, RetryAction_STOP_LOCAL_OWNER, err.Error()}
	}
}

// toWireError maps any error, preferring an already-typed wire error.
func toWireError(err error) *wireError {
	if err == nil {
		return nil
	}
	var we *wireError
	if errors.As(err, &we) {
		return we
	}
	return mapJournalError(err)
}

// toProto renders the typed error for one answer. detail stays redacted: no
// credentials, no payload bytes.
func (e *wireError) toProto(header *SessionHeader) *Error {
	if e == nil {
		return nil
	}
	p := &Error{Code: e.code, OwnerState: "gateway", RetryAction: e.action, Detail: e.detail}
	if header != nil {
		p.RequestId = header.GetRequestId()
	}
	return p
}
