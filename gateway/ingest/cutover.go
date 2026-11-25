package ingest

// The drain and cutover RPCs: SealGeneration, PrepareSwitch, CommitSwitch and
// Resume (DELIVERY_STATE_MACHINE, the drain and cutover section).
//
//   - SealGeneration seals an exhausted admission generation. Its commit and
//     the admission barrier are one serialized step: every session of the
//     sealed generation is fenced and a retry returns the same SealAck.
//   - PrepareSwitch checks the desired destination online (account, stream,
//     route, limits and both stream metadata identity keys) before anything
//     durable. A failed or unreachable check never produces SwitchPrepared.
//   - CommitSwitch re-checks the target before the first durable activation.
//     An already committed switch answers its durable receipt without the
//     target: an outage never cancels a committed control receipt.
//   - Resume opens exactly the next admission generation; a repeated intent
//     returns the same ResumeAck.

import (
	"context"
	"errors"

	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/gateway/journal"
)

// ErrTargetUnavailable marks a target check that could not reach the
// destination. It is retryable and is never evidence that the target
// matches or mismatches.
var ErrTargetUnavailable = errors.New("ingest: destination target unavailable")

// TargetVerifier performs the online check of one desired destination
// binding: the connection account, the stream, the route, the message size
// limit and the two stream metadata identity keys. The verifier resolves
// the desired descriptor digest to the stored descriptor it stands for, so
// the RPC layer never hands over route content it did not verify itself.
// VerifyTarget returns nil when the target matches. An unreachable target
// returns an error wrapping ErrTargetUnavailable; any other error is a
// concrete mismatch that stops the switch until the configuration is fixed.
// The text of that error becomes the detail of the typed error the producer
// receives: it names the reason (the stream, the key and the kind of
// mismatch) and never carries credentials. The verifier never publishes and
// never mutates a binding.
type TargetVerifier interface {
	VerifyTarget(ctx context.Context, desiredDigest [32]byte) error
}

// SetTargetVerifier binds the target verifier of the switch RPCs. Without
// one, PrepareSwitch and the first CommitSwitch answer TRANSIENT_UNAVAILABLE:
// the check is mandatory and is never skipped. Call it before serving.
func (s *Server) SetTargetVerifier(v TargetVerifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verifier = v
}

// checkTarget runs the online check of one binding and classifies a failure:
// an unreachable target is transient, a concrete mismatch is a configuration
// problem. The detail carries the verifier's reason, which names no
// credentials.
func (s *Server) checkTarget(ctx context.Context, desiredDigest [32]byte) *wireError {
	s.mu.Lock()
	v := s.verifier
	s.mu.Unlock()
	if v == nil {
		return &wireError{ErrorCode_TRANSIENT_UNAVAILABLE, RetryAction_RETRY_BACKOFF, "no destination target verifier is configured"}
	}
	err := v.VerifyTarget(ctx, desiredDigest)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrTargetUnavailable) {
		return &wireError{ErrorCode_TRANSIENT_UNAVAILABLE, RetryAction_RETRY_BACKOFF, "destination target check is unavailable"}
	}
	return &wireError{ErrorCode_BINDING_MISMATCH, RetryAction_FIX_CONFIG, "destination target check failed: " + err.Error()}
}

func sealAckToProto(seal journal.Seal) *SealAck {
	return &SealAck{DrainId: seal.DrainID[:], BindingDigest: seal.BindingDigest[:],
		AdmissionGeneration: seal.AdmissionGeneration, Highwater: seal.Highwater, IssuerStoreEpoch: seal.IssuerEpoch[:]}
}

// sealAckOf parses and validates a presented SealAck.
func sealAckOf(m *SealAck) (journal.Seal, *wireError) {
	var seal journal.Seal
	if m == nil {
		return seal, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, "seal ack is required"}
	}
	var werr *wireError
	if seal.DrainID, werr = id16(m.GetDrainId(), "seal drain id"); werr != nil {
		return seal, werr
	}
	if seal.BindingDigest, werr = hash32(m.GetBindingDigest(), "seal binding digest"); werr != nil {
		return seal, werr
	}
	if seal.IssuerEpoch, werr = id16(m.GetIssuerStoreEpoch(), "seal issuer store epoch"); werr != nil {
		return seal, werr
	}
	if m.GetAdmissionGeneration() == 0 {
		return seal, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, "seal admission generation starts at 1"}
	}
	seal.AdmissionGeneration = m.GetAdmissionGeneration()
	seal.Highwater = m.GetHighwater()
	return seal, nil
}

func switchPreparedToProto(p journal.SwitchPrepared) *SwitchPrepared {
	return &SwitchPrepared{SwitchId: p.SwitchID[:], OldBindingDigest: p.OldDigest[:], NewBindingDigest: p.NewDigest[:],
		DrainId: p.DrainID[:], AdmissionGeneration: p.AdmissionGeneration, IssuerStoreEpoch: p.IssuerEpoch[:]}
}

// switchPreparedOf parses and validates a presented SwitchPrepared receipt.
func switchPreparedOf(m *SwitchPrepared) (journal.SwitchPrepared, *wireError) {
	var p journal.SwitchPrepared
	if m == nil {
		return p, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, "switch prepared receipt is required"}
	}
	var werr *wireError
	if p.SwitchID, werr = id16(m.GetSwitchId(), "switch id"); werr != nil {
		return p, werr
	}
	if p.OldDigest, werr = hash32(m.GetOldBindingDigest(), "old binding digest"); werr != nil {
		return p, werr
	}
	if p.NewDigest, werr = hash32(m.GetNewBindingDigest(), "new binding digest"); werr != nil {
		return p, werr
	}
	if p.DrainID, werr = id16(m.GetDrainId(), "switch drain id"); werr != nil {
		return p, werr
	}
	if p.IssuerEpoch, werr = id16(m.GetIssuerStoreEpoch(), "switch issuer store epoch"); werr != nil {
		return p, werr
	}
	p.AdmissionGeneration = m.GetAdmissionGeneration()
	return p, nil
}

func resumeAckToProto(a journal.ResumeAck) *ResumeAck {
	return &ResumeAck{ResumeId: a.ResumeID[:], BindingDigest: a.BindingDigest[:],
		OldAdmissionGeneration: a.OldAdmissionGeneration, NewAdmissionGeneration: a.NewAdmissionGeneration,
		DrainId: a.DrainID[:], IssuerStoreEpoch: a.IssuerEpoch[:]}
}

// journalBinding keeps the identity of one parsed descriptor the journal
// checks: destination line, generation, network and descriptor digest.
func journalBinding(b destination.Binding) journal.Binding {
	return journal.Binding{
		DestinationID:    [16]byte(b.DestinationID),
		Generation:       b.Generation,
		ChainID:          [32]byte(b.Network.ChainID),
		GenesisHash:      [32]byte(b.Network.GenesisHash),
		DescriptorDigest: [32]byte(b.DescriptorDigest),
	}
}

// SealGeneration seals an exhausted admission generation. The request names
// the generation being sealed; it comes from the session of that generation,
// or on a retry from a session of the next one.
func (s *Server) SealGeneration(_ context.Context, req *SealGenerationRequest) (*SealGenerationResponse, error) {
	resp := &SealGenerationResponse{}
	header := req.GetHeader()
	sealed := req.GetAdmissionGeneration()
	we := s.mutateRaise(header, sealed+1, func() error {
		if sealed == 0 || sealed == ^uint64(0) || req.GetBindingGeneration() == 0 {
			return &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, "generations start at 1 and must leave room for the next one"}
		}
		if session := header.GetAdmissionGeneration(); session != sealed && session != sealed+1 {
			return &wireError{ErrorCode_GENERATION_CONFLICT, RetryAction_RECONNECT, "the sealed generation must be the session's or the one below it"}
		}
		drain, werr := id16(req.GetDrainId(), "drain id")
		if werr != nil {
			return werr
		}
		seal, err := s.journal.SealGeneration(runOf(header), journal.SealIntent{
			BindingGeneration:   req.GetBindingGeneration(),
			AdmissionGeneration: sealed,
			DrainID:             drain,
			Highwater:           req.GetBatchHighwater(),
		})
		if err != nil {
			return toWireError(err)
		}
		// The publisher barrier: a callback of the previous publisher
		// generation creates no state.
		s.publisher.NewGeneration()
		resp.Seal = sealAckToProto(seal)
		return nil
	})
	if we != nil {
		return &SealGenerationResponse{Error: we.toProto(header)}, nil
	}
	return resp, nil
}

// switchIntent is the parsed PrepareSwitch request.
type switchIntent struct {
	run      journal.BatchID
	oldSeal  journal.Seal
	desired  journal.Binding
	switchID [16]byte
}

// parseSwitch validates every field of a PrepareSwitch request.
func (s *Server) parseSwitch(req *PrepareSwitchRequest) (switchIntent, *wireError) {
	var in switchIntent
	header := req.GetHeader()
	in.run = runOf(header)
	var werr *wireError
	if in.oldSeal, werr = sealAckOf(req.GetOldSeal()); werr != nil {
		return in, werr
	}
	if in.switchID, werr = id16(req.GetSwitchId(), "switch id"); werr != nil {
		return in, werr
	}
	old, werr := hash32(req.GetOldBindingDigest(), "old binding digest")
	if werr != nil {
		return in, werr
	}
	if string(old[:]) != string(header.GetBindingDigest()) {
		return in, &wireError{ErrorCode_BINDING_MISMATCH, RetryAction_FIX_CONFIG, "old binding digest does not match the session"}
	}
	in.desired, werr = parseDescriptor(req.GetDesiredDescriptor())
	return in, werr
}

// parseDescriptor validates the desired DestinationBinding descriptor: its
// size, its strict JSON form and the digest it states for itself.
func parseDescriptor(data []byte) (journal.Binding, *wireError) {
	if len(data) > destination.MaxDescriptorBytes {
		return journal.Binding{}, &wireError{ErrorCode_LIMIT_INCOMPATIBLE, RetryAction_FIX_CONFIG, "desired descriptor exceeds 16 KiB"}
	}
	b, err := destination.ParseBinding(data)
	if err != nil {
		return journal.Binding{}, &wireError{ErrorCode_INVALID_DESCRIPTOR, RetryAction_FIX_CONFIG, "desired descriptor is invalid"}
	}
	return journalBinding(b), nil
}

// PrepareSwitch checks the desired destination and durably prepares the
// switch; the active binding stays the old one. A retry of a durable switch
// returns its receipt without a new check.
func (s *Server) PrepareSwitch(ctx context.Context, req *PrepareSwitchRequest) (*PrepareSwitchResponse, error) {
	resp := &PrepareSwitchResponse{}
	header := req.GetHeader()
	var in switchIntent
	received := false
	// Every durable rule runs first and changes nothing: a retry of an
	// open switch answers its receipt, a refusal of the durable state
	// (no seal, another old seal, an illegal successor descriptor, another
	// switch over an unfinished or committed one) is answered as it is.
	// None of that depends on the target being available.
	we := s.mutateControl(header, func() error {
		var werr *wireError
		if in, werr = s.parseSwitch(req); werr != nil {
			return werr
		}
		receipt, err := s.journal.CheckPrepareSwitch(in.run, in.oldSeal, in.desired, in.switchID)
		if err != nil {
			return toWireError(err)
		}
		if receipt != nil {
			resp.Prepared = switchPreparedToProto(*receipt)
			received = true
		}
		return nil
	})
	if we != nil {
		return &PrepareSwitchResponse{Error: we.toProto(header)}, nil
	}
	if received {
		return resp, nil
	}
	// The online check of the desired target precedes anything durable: a
	// failed or unreachable check never produces SwitchPrepared.
	if we = s.checkTarget(ctx, in.desired.DescriptorDigest); we != nil {
		return &PrepareSwitchResponse{Error: we.toProto(header)}, nil
	}
	we = s.mutateControl(header, func() error {
		prepared, err := s.journal.PrepareSwitch(in.run, in.oldSeal, in.desired, in.switchID)
		if err != nil {
			return toWireError(err)
		}
		resp.Prepared = switchPreparedToProto(prepared)
		return nil
	})
	if we != nil {
		return &PrepareSwitchResponse{Error: we.toProto(header)}, nil
	}
	return resp, nil
}

// switchCommit is the parsed CommitSwitch request.
type switchCommit struct {
	switchID      [16]byte
	prepared      journal.SwitchPrepared
	bindingDigest [32]byte
	generation    uint64
}

func (s *Server) parseCommit(req *CommitSwitchRequest) (switchCommit, *wireError) {
	var c switchCommit
	var werr *wireError
	if c.switchID, werr = id16(req.GetSwitchId(), "switch id"); werr != nil {
		return c, werr
	}
	if c.prepared, werr = switchPreparedOf(req.GetPrepared()); werr != nil {
		return c, werr
	}
	a := req.GetAssertion()
	if a == nil {
		return c, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, "binding commit assertion is required"}
	}
	if a.GetSchemaVersion() != assertionSchemaVersion {
		return c, &wireError{ErrorCode_VERSION_UNSUPPORTED, RetryAction_DRAIN_OLD_BUILD, "binding commit assertion schema version must be 1"}
	}
	assertedID, werr := id16(a.GetSwitchId(), "assertion switch id")
	if werr != nil {
		return c, werr
	}
	if assertedID != c.switchID {
		return c, &wireError{ErrorCode_IDENTITY_MISMATCH, RetryAction_RETRY_BACKOFF, "assertion names another switch"}
	}
	if c.bindingDigest, werr = hash32(a.GetBindingDigest(), "assertion binding digest"); werr != nil {
		return c, werr
	}
	c.generation = a.GetBindingGeneration()
	return c, nil
}

// CommitSwitch durably activates a prepared switch and answers the same
// identity the SwitchPrepared carried. The session may be bound to the
// active binding or to the old binding of this exact switch, which is the
// session that prepared it.
//
// The session is validated before anything is read from the store. The one
// read, for the old binding that switch admits, cannot fail: the journal
// holds the switch record in memory since it opened the store.
func (s *Server) CommitSwitch(ctx context.Context, req *CommitSwitchRequest) (*CommitSwitchResponse, error) {
	resp := &CommitSwitchResponse{}
	header := req.GetHeader()
	rec, we := s.checkSession(header)
	if we != nil {
		return &CommitSwitchResponse{Error: we.toProto(header)}, nil
	}
	cur, open := s.journal.CurrentSwitch()
	var admit *[32]byte
	if open {
		old := cur.OldDigest
		admit = &old
	}
	if we := s.checkBinding(header, admit); we != nil {
		return &CommitSwitchResponse{Error: we.toProto(header)}, nil
	}
	committed := false
	// The exact receipt is verified against the durable switch first, so a
	// refusal never depends on the target. An already committed switch
	// answers its receipt here: a target outage never cancels it.
	err := s.gate.Mutate(rec.token, func() error {
		c, werr := s.parseCommit(req)
		if werr != nil {
			return werr
		}
		done, err := s.journal.CheckCommitSwitch(c.switchID, c.prepared, c.bindingDigest, c.generation)
		if err != nil {
			return toWireError(err)
		}
		if !done {
			return nil
		}
		prepared, err := s.journal.CommitSwitch(c.switchID, c.prepared, c.bindingDigest, c.generation)
		if err != nil {
			return toWireError(err)
		}
		resp.Committed = &SwitchCommitted{Prepared: switchPreparedToProto(prepared)}
		committed = true
		return nil
	})
	if we = toWireError(err); we != nil {
		return &CommitSwitchResponse{Error: we.toProto(header)}, nil
	}
	if committed {
		return resp, nil
	}
	// The gated check accepted a prepared switch of this id, so the store
	// holds one and cur is its record.
	// Before the first durable activation the target is re-checked. When
	// the check fails nothing changes: the producer is not rolled back and
	// nothing is published to the old destination.
	if we = s.checkTarget(ctx, cur.NewDigest); we != nil {
		return &CommitSwitchResponse{Error: we.toProto(header)}, nil
	}
	we = s.mutateFor(header, admit, func() error {
		c, werr := s.parseCommit(req)
		if werr != nil {
			return werr
		}
		prepared, err := s.journal.CommitSwitch(c.switchID, c.prepared, c.bindingDigest, c.generation)
		if err != nil {
			return toWireError(err)
		}
		resp.Committed = &SwitchCommitted{Prepared: switchPreparedToProto(prepared)}
		return nil
	})
	if we != nil {
		return &CommitSwitchResponse{Error: we.toProto(header)}, nil
	}
	return resp, nil
}

// Resume opens exactly the next admission generation of a sealed run. The
// commit and the admission barrier of the new generation are one serialized
// step; a repeated intent returns the same ResumeAck.
func (s *Server) Resume(_ context.Context, req *ResumeRequest) (*ResumeResponse, error) {
	resp := &ResumeResponse{}
	header := req.GetHeader()
	next := req.GetNextAdmissionGeneration()
	we := s.mutateRaise(header, next, func() error {
		oldSeal, werr := sealAckOf(req.GetOldSeal())
		if werr != nil {
			return werr
		}
		resumeID, werr := id16(req.GetResumeId(), "resume id")
		if werr != nil {
			return werr
		}
		binding, werr := hash32(req.GetActiveBindingDigest(), "active binding digest")
		if werr != nil {
			return werr
		}
		// The resume comes from the session of the sealed generation, or
		// from the one of the generation it opens.
		if session := header.GetAdmissionGeneration(); session != next && session != next-1 {
			return &wireError{ErrorCode_GENERATION_CONFLICT, RetryAction_RECONNECT, "the next generation must be the session's or the one above it"}
		}
		ack, err := s.journal.Resume(runOf(header), journal.ResumeIntent{
			ResumeID:                resumeID,
			OldSeal:                 oldSeal,
			BindingDigest:           binding,
			NextAdmissionGeneration: next,
		})
		if err != nil {
			return toWireError(err)
		}
		resp.Ack = resumeAckToProto(ack)
		return nil
	})
	if we != nil {
		return &ResumeResponse{Error: we.toProto(header)}, nil
	}
	return resp, nil
}
