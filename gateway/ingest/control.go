package ingest

// The control RPCs of the contract's RPC table that complete the delivery
// handshake and the drain/cutover protocol: GetBatchState, Release,
// RecoveryRelease, SealGeneration, PrepareSwitch, CommitSwitch and Resume.
//
// Every RPC follows the rules of the mutating boundary (the control
// requests and protocol errors sections of CONTROL_WIRE_CONTRACT, and
// DELIVERY_STATE_MACHINE):
//
//   - The session header is validated and the journal step runs inside the
//     gate's serialized ordering, so the token check happens together with
//     the commit.
//   - Answers carry the registry's typed error; the gRPC status is only a
//     transport categorization.
//   - A repeated call with the same intent returns the durable receipt; a
//     retry never allocates, advances or commits anything a second time.
//   - SealGeneration and Resume raise the admission barrier in the same
//     serialized step as their commit, so a late RPC of the sealed
//     generation answers SESSION_STALE and creates no state.

import (
	"context"

	"github.com/sigaev-pro/bc-indexer/gateway/journal"
)

// Bounds of the control answers and steps.
const (
	// releaseStepKeys is the largest number of payload keys one bounded
	// release step deletes.
	releaseStepKeys = 4096
	// assertionSchemaVersion is the only schema version of the producer
	// assertions; control evidence is a fixed versioned schema.
	assertionSchemaVersion = 1
)

// stateCursorsPerPage bounds the frame cursors of one GetBatchState answer:
// the page stays far below the 256 KiB RPC bound whatever the batch size,
// and next_cursor continues the listing. It is a variable only so tests can
// exercise the continuation with a small batch.
var stateCursorsPerPage = 1024

// runOf reads the producer and run of a validated header.
func runOf(header *SessionHeader) journal.BatchID {
	var id journal.BatchID
	copy(id.ProducerID[:], header.GetProducerId())
	copy(id.RunID[:], header.GetRunId())
	return id
}

// id16 validates one Id16 field: exactly 16 bytes and not the zero identity.
func id16(b []byte, what string) ([16]byte, *wireError) {
	var v [16]byte
	if len(b) != 16 {
		return v, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, what + " must be 16 bytes"}
	}
	copy(v[:], b)
	if v == ([16]byte{}) {
		return v, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, what + " must not be zero"}
	}
	return v, nil
}

func cursorProto(c journal.FrameCursor) *FrameCursor {
	return &FrameCursor{Ordinal: c.Addr.Ordinal, FragmentIndex: c.Addr.FragmentIndex,
		FrameIndex: c.Addr.FrameIndex, NextOffset: c.NextBytes}
}

// batchStateProto maps a journal state onto the wire; 0 is NOT_FOUND.
func batchStateProto(state journal.State) BatchState {
	switch state {
	case journal.StatePreparing:
		return BatchState_PREPARING
	case journal.StatePrepared:
		return BatchState_PREPARED
	case journal.StateReceiving:
		return BatchState_RECEIVING
	case journal.StateClosed:
		return BatchState_CLOSED
	case journal.StateNatsAcked:
		return BatchState_NATS_ACKED
	case journal.StateReleasing:
		return BatchState_RELEASING
	case journal.StateRetired:
		return BatchState_RETIRED
	default:
		return BatchState_NOT_FOUND
	}
}

func ackToProto(a journal.IngestAck) *IngestAck {
	return &IngestAck{
		Batch:                &BatchId{ProducerId: a.Batch.ProducerID[:], RunId: a.Batch.RunID[:], BatchSeq: a.Batch.Seq},
		ManifestDigest:       a.ManifestDigest[:],
		PlanDigest:           a.PlanDigest[:],
		LayoutManifestDigest: a.LayoutDigest[:],
		BindingDigest:        a.BindingDigest[:],
		IssuerStoreEpoch:     a.IssuerEpoch[:],
	}
}

// GetBatchState reports the durable state of one batch: its IngestAck when
// that is durable, and one bounded page of frame cursors. A read is not a
// permission to delete producer payload; it runs under the same fence as
// the mutations so a batch is never reported across a barrier.
func (s *Server) GetBatchState(_ context.Context, req *GetBatchStateRequest) (*GetBatchStateResponse, error) {
	resp := &GetBatchStateResponse{}
	we := s.mutateControl(req.GetHeader(), func() error {
		id, werr := s.batchID(req.GetHeader(), req.GetBatch())
		if werr != nil {
			return werr
		}
		plan, werr := hash32(req.GetPlanDigest(), "plan digest")
		if werr != nil {
			return werr
		}
		var after *journal.FrameAddr
		if c := req.GetCursor(); c != nil {
			after = &journal.FrameAddr{Ordinal: c.GetOrdinal(), FragmentIndex: c.GetFragmentIndex(), FrameIndex: c.GetFrameIndex()}
		}
		page, err := s.journal.BatchState(id, plan, after, stateCursorsPerPage)
		if err != nil {
			return toWireError(err)
		}
		resp.State = batchStateProto(page.State)
		if page.Ack != nil {
			resp.Ack = ackToProto(*page.Ack)
		}
		for _, c := range page.Cursors {
			resp.Cursors = append(resp.Cursors, cursorProto(c))
		}
		if page.More && len(resp.Cursors) > 0 {
			resp.NextCursor = resp.Cursors[len(resp.Cursors)-1]
		}
		return nil
	})
	if we != nil {
		return &GetBatchStateResponse{Error: we.toProto(req.GetHeader())}, nil
	}
	return resp, nil
}

// Release drives the ordered release of one NATS_ACKED batch and answers
// ReleaseAck only after the final atomic commit. A repeated Release of an
// already retired sequence answers the durable prefix again.
//
// The order is the one of the contract's release row: the publisher barrier
// goes up first, then the RELEASING marker is committed, then the bounded
// cleanup runs and the final commit advances the retired prefix. The
// journal decides whether the release may start before the barrier is
// raised, so a refused release never fences a batch that still publishes.
func (s *Server) Release(_ context.Context, req *ReleaseRequest) (*ReleaseResponse, error) {
	resp := &ReleaseResponse{}
	we := s.mutateControl(req.GetHeader(), func() error {
		id, werr := s.batchID(req.GetHeader(), req.GetBatch())
		if werr != nil {
			return werr
		}
		manifest, werr := hash32(req.GetManifestDigest(), "manifest digest")
		if werr != nil {
			return werr
		}
		plan, werr := hash32(req.GetPlanDigest(), "plan digest")
		if werr != nil {
			return werr
		}
		retired, err := s.journal.CheckRelease(id, manifest, plan, releaseStepKeys)
		if err != nil {
			return toWireError(err)
		}
		if !retired {
			// The publisher barrier precedes the RELEASING commit: from here
			// on no publisher callback of this batch is applied.
			s.publisher.Release(id.Bytes())
		}
		prefix, err := s.journal.Release(id, manifest, plan, releaseStepKeys)
		if err != nil {
			return toWireError(err)
		}
		resp.Ack = &ReleaseAck{Batch: req.GetBatch(), RetiredPrefix: prefix}
		return nil
	})
	if we != nil {
		return &ReleaseResponse{Error: we.toProto(req.GetHeader())}, nil
	}
	return resp, nil
}

// ingestAckOf parses a presented IngestAck. The batch is validated by the
// caller against the request and the session identity. A missing ack needs
// no check of its own: the batch of a nil message is no valid batch id, so
// the first field already refuses it with IDENTITY_MISMATCH.
func ingestAckOf(m *IngestAck) (journal.IngestAck, *wireError) {
	var a journal.IngestAck
	var werr *wireError
	if a.Batch, werr = batchIDOf(m.GetBatch()); werr != nil {
		return a, werr
	}
	if a.ManifestDigest, werr = hash32(m.GetManifestDigest(), "ingest ack manifest digest"); werr != nil {
		return a, werr
	}
	if a.PlanDigest, werr = hash32(m.GetPlanDigest(), "ingest ack plan digest"); werr != nil {
		return a, werr
	}
	if a.LayoutDigest, werr = hash32(m.GetLayoutManifestDigest(), "ingest ack layout manifest digest"); werr != nil {
		return a, werr
	}
	if a.BindingDigest, werr = hash32(m.GetBindingDigest(), "ingest ack binding digest"); werr != nil {
		return a, werr
	}
	if a.IssuerEpoch, werr = id16(m.GetIssuerStoreEpoch(), "ingest ack issuer store epoch"); werr != nil {
		return a, werr
	}
	return a, nil
}

// batchIDOf validates a BatchId message on its own.
func batchIDOf(b *BatchId) (journal.BatchID, *wireError) {
	if b == nil || len(b.GetProducerId()) != 16 || len(b.GetRunId()) != 16 || b.GetBatchSeq() == 0 {
		return journal.BatchID{}, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, "batch id must carry 16-byte producer and run ids and a sequence from 1"}
	}
	var id journal.BatchID
	copy(id.ProducerID[:], b.GetProducerId())
	copy(id.RunID[:], b.GetRunId())
	id.Seq = b.GetBatchSeq()
	return id, nil
}

// outboxAssertionOf parses the producer's durable ACKED/outbox assertion.
func outboxAssertionOf(m *OutboxAssertion) (journal.OutboxAssertion, *wireError) {
	var a journal.OutboxAssertion
	if m == nil {
		return a, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, "outbox assertion is required"}
	}
	if m.GetSchemaVersion() != assertionSchemaVersion {
		return a, &wireError{ErrorCode_VERSION_UNSUPPORTED, RetryAction_DRAIN_OLD_BUILD, "outbox assertion schema version must be 1"}
	}
	var werr *wireError
	if a.Batch, werr = batchIDOf(m.GetBatch()); werr != nil {
		return a, werr
	}
	if a.PlanDigest, werr = hash32(m.GetPlanDigest(), "outbox assertion plan digest"); werr != nil {
		return a, werr
	}
	if a.BindingDigest, werr = hash32(m.GetBindingDigest(), "outbox assertion binding digest"); werr != nil {
		return a, werr
	}
	return a, nil
}

// RecoveryRelease completes the delivery of a batch the gateway store lost
// from the producer's saved IngestAck and durable ACKED/outbox assertion.
// The assertion names exactly the next sequence; an arbitrary jump, or a
// batch the store still holds, is refused and advances nothing.
func (s *Server) RecoveryRelease(_ context.Context, req *RecoveryReleaseRequest) (*ReleaseResponse, error) {
	resp := &ReleaseResponse{}
	we := s.mutateControl(req.GetHeader(), func() error {
		id, werr := s.batchID(req.GetHeader(), req.GetBatch())
		if werr != nil {
			return werr
		}
		ack, werr := ingestAckOf(req.GetIngestAck())
		if werr != nil {
			return werr
		}
		assertion, werr := outboxAssertionOf(req.GetAssertion())
		if werr != nil {
			return werr
		}
		// The journal refuses an ack or an assertion that does not name
		// the batch of the call, which already matches the session identity.
		prefix, err := s.journal.RecoveryRelease(id, ack, assertion)
		if err != nil {
			return toWireError(err)
		}
		resp.Ack = &ReleaseAck{Batch: req.GetBatch(), RetiredPrefix: prefix}
		return nil
	})
	if we != nil {
		return &ReleaseResponse{Error: we.toProto(req.GetHeader())}, nil
	}
	return resp, nil
}

// mutateFor is mutateControl with one more admitted binding digest. The
// session is validated before the binding, so a caller that must read the
// store to know the admitted digest does it after the session check.
func (s *Server) mutateFor(header *SessionHeader, admitBinding *[32]byte, fn func() error) *wireError {
	rec, we := s.checkSession(header)
	if we != nil {
		return we
	}
	if we := s.checkBinding(header, admitBinding); we != nil {
		return we
	}
	return toWireError(s.gate.Mutate(rec.token, fn))
}

// mutateRaise is mutate for the barrier RPCs (SealGeneration, Resume): the
// commit and the raise of the admission barrier to fenceTo are one
// serialized step. Sessions of older epochs are then forgotten: their tokens
// answer SESSION_STALE and create no state.
func (s *Server) mutateRaise(header *SessionHeader, fenceTo uint64, fn func() error) *wireError {
	rec, we := s.checkHeader(header)
	if we != nil {
		return we
	}
	if err := s.gate.MutateRaise(rec.token, fenceTo, fn); err != nil {
		return toWireError(err)
	}
	s.forgetSessionsBelow(fenceTo)
	return nil
}

// forgetSessionsBelow drops the sessions of epochs older than epoch.
func (s *Server) forgetSessionsBelow(epoch uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, rec := range s.sessions {
		if rec.token.Epoch < epoch {
			delete(s.sessions, id)
		}
	}
}
