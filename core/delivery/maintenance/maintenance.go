// Package maintenance is E03-03.maintenance: the Seal/Switch/Resume
// transition machine of the drain and cutover protocol, with the D77
// target metadata checks at the prescribed points, idempotent committed
// receipts on a NATS outage, and MAINTENANCE recovery from a quiesced
// producer (DELIVERY_STATE_MACHINE, "Drain и cutover D54/D57/D59";
// CONTROL_WIRE_CONTRACT, "Управляемый restart, drain, cutover" and the
// D77 phase table).
//
// The engine drives both stores through the maintenance seams of
// core/delivery. The online target verification is a seam too:
// TargetVerifier receives the desired binding view and performs the D77
// account/stream/route/metadata check; the gateway/destination package
// adapts to it at the wiring point, so this package stays free of the
// NATS client surface.
//
// The rules the engine owns: a seal needs an exhausted delivery frontier
// and binds the whole intent; a switch verifies the new target before
// SwitchPrepared and again before the first CommitSwitch; an already
// committed switch or resume returns its durable receipt regardless of
// the current NATS availability, while new publication still waits for a
// fresh target check; the producer's binding commit never rolls back
// automatically; a resume accepts exactly the next admission generation,
// and capture stays blocked until the producer's LIVE commit.
package maintenance

import (
	"context"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// TargetVerifier performs the online D77 target check of one desired
// binding: account, stream, route, limits and the two metadata identity
// keys. The verifier resolves the desired descriptor digest to its
// stored desired binding, so the engine never carries unverified route
// content. VerifyTarget returns nil when the target is verified. An
// unavailable target returns a non-nil error the caller classifies as
// transient; a concrete mismatch returns a non-nil error the caller
// classifies as configuration. The engine never publishes and never
// mutates a binding inside a check.
type TargetVerifier interface {
	VerifyTarget(ctx context.Context, desiredDigest proto.Hash32) error
}

// ProducerSeam is the producer-side maintenance surface.
type ProducerSeam interface {
	delivery.ProducerStore
	SetDrainPoint(drainID proto.ID16, highwater uint64) error
	DrainPoint() (proto.ID16, uint64, bool)
	SaveSealAck(ack delivery.SealAck) error
	SealAckSaved() (delivery.SealAck, bool)
	SaveSwitchIntent(switchID proto.ID16, desiredDigest proto.Hash32) error
	SwitchIntentSaved() (proto.ID16, proto.Hash32, bool)
	CommitBinding(digest proto.Hash32, generation uint64) error
	CommittedBinding() (proto.Hash32, uint64, bool)
	SaveResumeIntent(intent delivery.ResumeIntent) error
	ResumeIntentSaved() (delivery.ResumeIntent, bool)
	CommitLive(ack delivery.ResumeAck) error
	ResumeAckSaved() (delivery.ResumeAck, bool)
}

// GatewaySeam is the gateway-side maintenance surface.
type GatewaySeam interface {
	delivery.GatewayStore
	ActiveBinding() (uint64, proto.Hash32)
	SetActiveBinding(generation uint64, digest proto.Hash32) error
	EnterAwaitSwitch(desiredDigest proto.Hash32) error
	SwitchState() (delivery.SwitchPhase, delivery.SwitchPrepared)
	PrepareSwitch(fence delivery.Fence, desired delivery.BindingView, switchID proto.ID16) (delivery.SwitchPrepared, error)
	CommitSwitch(fence delivery.Fence, switchID proto.ID16) (delivery.SwitchCommitted, error)
	SealGeneration(fence delivery.Fence, intent delivery.SealIntent) (delivery.SealAck, error)
	SealedGeneration() (delivery.SealAck, bool)
	Resume(fence delivery.Fence, intent delivery.ResumeIntent) (delivery.ResumeAck, error)
	ResumeState() (delivery.ResumeAck, bool)
}

// Engine drives the maintenance transitions of one producer/gateway pair.
type Engine struct {
	prod   ProducerSeam
	gw     GatewaySeam
	verify TargetVerifier
}

// New returns an engine over the two stores and the target verifier. A nil
// verifier is rejected: every switch path needs the D77 check.
func New(prod ProducerSeam, gw GatewaySeam, verify TargetVerifier) (*Engine, error) {
	if prod == nil || gw == nil {
		return nil, fmt.Errorf("maintenance: store is nil")
	}
	if verify == nil {
		return nil, fmt.Errorf("maintenance: target verifier is nil")
	}
	return &Engine{prod: prod, gw: gw, verify: verify}, nil
}

// SealToDrained finishes a drain: with the producer in DRAIN and its
// delivery frontier exhausted (every batch released, the retired prefix
// equal to the highwater), the engine fixes the drain point, sends
// SealGeneration, and saves DRAINED with the seal ack. Retained pins
// waiting for a future finalized anchor do not block this. A repeated
// call with the same drain id is idempotent.
func (e *Engine) SealToDrained(ctx context.Context, fence delivery.Fence, drainID proto.ID16) (delivery.SealAck, error) {
	if fence.Kind != delivery.SessionNormal {
		return delivery.SealAck{}, fmt.Errorf("%w: seal runs in the ordinary session", delivery.ErrFenced)
	}
	if ack, ok := e.prod.SealAckSaved(); ok && ack.DrainID == drainID {
		return ack, nil // crash between ack and terminal commit replays
	}
	if e.prod.Mode() != delivery.ModeDrain {
		return delivery.SealAck{}, fmt.Errorf("%w: seal needs DRAIN mode, producer is %s",
			delivery.ErrState, e.prod.Mode())
	}
	highwater := e.prod.RetiredPrefix()
	// The frontier must be exhausted: batch allocation is sequential and
	// retired records are gone, so the first sequence above the prefix
	// settles whether anything is still open.
	if _, ok := e.prod.Batch(highwater + 1); ok {
		return delivery.SealAck{}, fmt.Errorf("%w: batch %d is still open; the frontier is not exhausted",
			delivery.ErrState, highwater+1)
	}
	if err := e.prod.SetDrainPoint(drainID, highwater); err != nil {
		return delivery.SealAck{}, err
	}
	generation, digest := e.gw.ActiveBinding()
	ack, err := e.gw.SealGeneration(fence, delivery.SealIntent{
		BindingGeneration: generation, AdmissionGeneration: fence.AdmissionGeneration,
		DrainID: drainID, Highwater: highwater, BindingDigest: digest,
	})
	if err != nil {
		return delivery.SealAck{}, err
	}
	if err := e.prod.SaveSealAck(ack); err != nil {
		return delivery.SealAck{}, err
	}
	return ack, nil
}

// EnterAwaitSwitch opens the gateway's AWAIT_SWITCH for a desired digest
// that differs from the active binding: the old binding stays active and
// publication to the desired route stays forbidden.
func (e *Engine) EnterAwaitSwitch(desiredDigest proto.Hash32) error {
	return e.gw.EnterAwaitSwitch(desiredDigest)
}

// PrepareSwitch verifies the desired target online and, on success,
// durably prepares the switch. The active binding stays old; a failed
// check leaves no SwitchPrepared. A repeated call with the same switch id
// returns the same receipt without a new check only after the durable
// preparation exists.
func (e *Engine) PrepareSwitch(ctx context.Context, fence delivery.Fence, desired delivery.BindingView, switchID proto.ID16) (delivery.SwitchPrepared, error) {
	if phase, prepared := e.gw.SwitchState(); phase == delivery.SwitchPreparedPhase || phase == delivery.SwitchCommittedPhase {
		if prepared.SwitchID == switchID {
			return prepared, nil
		}
		return delivery.SwitchPrepared{}, fmt.Errorf("%w: another switch is %s",
			delivery.ErrImmutableConflict, phase)
	}
	// The D77 check of the desired target runs before anything durable:
	// an unavailable or mismatched target never produces SwitchPrepared.
	if err := e.verify.VerifyTarget(ctx, desired.DescriptorDigest); err != nil {
		return delivery.SwitchPrepared{}, fmt.Errorf("maintenance: desired target check: %w", err)
	}
	if err := e.prod.SaveSwitchIntent(switchID, desired.DescriptorDigest); err != nil {
		return delivery.SwitchPrepared{}, err
	}
	return e.gw.PrepareSwitch(fence, desired, switchID)
}

// ProducerCommitBinding durably commits the desired binding on the
// producer after SwitchPrepared. The commit is never rolled back
// automatically: a later failed gateway step leaves the producer on the
// new binding with a saved intent.
func (e *Engine) ProducerCommitBinding(desired delivery.BindingView) error {
	return e.prod.CommitBinding(desired.DescriptorDigest, desired.Generation)
}

// CommitSwitch durably activates the prepared switch. An already
// committed switch returns its durable receipt without contacting the
// target — a NATS outage never cancels a committed control receipt. A
// fresh commit re-verifies the target first; when the check fails after
// the producer committed, the engine returns the error and changes
// nothing: the producer is not rolled back and nothing publishes to the
// old destination.
func (e *Engine) CommitSwitch(ctx context.Context, fence delivery.Fence, switchID proto.ID16) (delivery.SwitchCommitted, error) {
	// The durable committed receipt answers first: a NATS outage never
	// cancels an already committed control receipt.
	if committed, ok := committedReceipt(e.gw, switchID); ok {
		return committed, nil
	}
	phase, prepared := e.gw.SwitchState()
	if phase != delivery.SwitchPreparedPhase || prepared.SwitchID != switchID {
		return delivery.SwitchCommitted{}, fmt.Errorf("%w: switch %v is %s, commit needs its PREPARED receipt",
			delivery.ErrState, switchID, phase)
	}
	// Before the first durable activation the target is re-verified. When
	// this fails after the producer committed, nothing changes here: the
	// producer is not rolled back and nothing publishes to the old
	// destination.
	if err := e.verify.VerifyTarget(ctx, prepared.NewDigest); err != nil {
		return delivery.SwitchCommitted{}, fmt.Errorf("maintenance: target re-check before commit: %w", err)
	}
	return e.gw.CommitSwitch(fence, switchID)
}

// committedReceipt returns the durable committed receipt of a switch.
func committedReceipt(gw GatewaySeam, switchID proto.ID16) (delivery.SwitchCommitted, bool) {
	phase, prepared := gw.SwitchState()
	if phase == delivery.SwitchCommittedPhase && prepared.SwitchID == switchID {
		return delivery.SwitchCommitted{Prepared: prepared}, true
	}
	return delivery.SwitchCommitted{}, false
}

// Resume opens the next admission generation after a seal. The producer
// saves the resume intent first; the gateway accepts exactly the next
// generation under the barrier; the producer commits LIVE only after the
// answer. A repeated intent returns the same result; a crash between the
// gateway answer and the producer commit continues the same intent.
func (e *Engine) Resume(fence delivery.Fence, resumeID proto.ID16) (delivery.ResumeAck, error) {
	if fence.Kind != delivery.SessionNormal {
		return delivery.ResumeAck{}, fmt.Errorf("%w: resume runs in the ordinary session; recovery uses RecoverTransition",
			delivery.ErrFenced)
	}
	if ack, ok := e.gw.ResumeState(); ok && ack.ResumeID == resumeID {
		// The gateway answered before the producer committed; the same
		// intent continues, whatever the current NATS availability is.
		if err := e.prod.CommitLive(ack); err != nil {
			return delivery.ResumeAck{}, err
		}
		return ack, nil
	}
	seal, ok := e.prod.SealAckSaved()
	if !ok {
		return delivery.ResumeAck{}, fmt.Errorf("%w: resume needs a saved seal ack", delivery.ErrState)
	}

	intent := delivery.ResumeIntent{ResumeID: resumeID, OldSeal: seal,
		BindingDigest: seal.BindingDigest, NextAdmissionGeneration: seal.AdmissionGeneration + 1}
	if err := e.prod.SaveResumeIntent(intent); err != nil {
		return delivery.ResumeAck{}, err
	}
	ack, err := e.gw.Resume(fence, intent)
	if err != nil {
		return delivery.ResumeAck{}, err
	}
	if err := e.prod.CommitLive(ack); err != nil {
		return delivery.ResumeAck{}, err
	}
	return ack, nil
}

// RecoverTransition finishes a saved transition over a MAINTENANCE
// session from a quiesced producer: it matches the durable intent and
// receipt first and returns the saved result; a lost response after a
// commit is answered by the receipt, not by SESSION_STALE. It never
// accepts new Prepare/Publish/Seal work.
func (e *Engine) RecoverTransition(fence delivery.Fence, switchID proto.ID16, resumeID proto.ID16) (delivery.SwitchCommitted, delivery.ResumeAck, error) {
	if fence.Kind != delivery.SessionMaintenance {
		return delivery.SwitchCommitted{}, delivery.ResumeAck{},
			fmt.Errorf("%w: recovery runs in the MAINTENANCE session", delivery.ErrFenced)
	}
	var committed delivery.SwitchCommitted
	if switchID != (proto.ID16{}) {
		receipt, ok := committedReceipt(e.gw, switchID)
		if !ok {
			phase, prepared := e.gw.SwitchState()
			if phase == delivery.SwitchPreparedPhase && prepared.SwitchID == switchID {
				// The intent was accepted but not committed: finish it
				// only when the producer carries the matching committed
				// binding assertion.
				if digest, _, ok := e.prod.CommittedBinding(); ok && digest == prepared.NewDigest {
					receipt, err := e.gw.CommitSwitch(fence, switchID)
					if err != nil {
						return delivery.SwitchCommitted{}, delivery.ResumeAck{}, err
					}
					return receipt, delivery.ResumeAck{}, nil
				}
			}
			return delivery.SwitchCommitted{}, delivery.ResumeAck{},
				fmt.Errorf("%w: switch %v has no saved committed receipt and no producer assertion",
					delivery.ErrNotFound, switchID)
		}
		committed = receipt
	}
	var resumed delivery.ResumeAck
	if resumeID != (proto.ID16{}) {
		ack, ok := e.gw.ResumeState()
		if !ok || ack.ResumeID != resumeID {
			return committed, delivery.ResumeAck{},
				fmt.Errorf("%w: resume %v has no saved receipt", delivery.ErrNotFound, resumeID)
		}
		resumed = ack
	}
	return committed, resumed, nil
}
