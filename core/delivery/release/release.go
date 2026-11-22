// Package release is E03-01.cleanup: the ordered release handshake and the
// bounded cleanup of both sides, the retired baseline of a recovered
// gateway store, the RecoveryRelease without payload, and the IMPORT_SEALED
// registration of a producer plan after a gateway store loss
// (DELIVERY_STATE_MACHINE, "Release и потеря Gateway store" and the
// transition table; CONTROL_WIRE_CONTRACT, "Служебные запросы и ответы").
//
// Release goes strictly by batch sequence with one in-flight release per
// run: a late ack never jumps an earlier unfinished batch. The producer
// keeps the full IngestAck and the release outbox until a verified
// ReleaseAck, and its payload cleanup never deletes them, so an
// acknowledged batch no longer needs its payload after a gateway loss.
// The gateway never rolls its retired prefix back: when it is one release
// ahead of the producer after a lost ReleaseAck, the producer finishes its
// outbox by the repeated ack.
package release

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
)

// OnIngestAck verifies one IngestAck against the producer's SEALED batch
// and atomically records ACKED_CLEANUP plus the release outbox. The whole
// IngestAck is durable from here on; payload cleanup may follow at any
// time and never removes it.
func OnIngestAck(prod delivery.ProducerStore, seq uint64, ack delivery.IngestAck) error {
	if err := ack.Validate(); err != nil {
		return err
	}
	if ack.Identity.Batch.Seq != seq {
		return fmt.Errorf("%w: ack names sequence %d, caller drove %d",
			delivery.ErrInvalidValue, ack.Identity.Batch.Seq, seq)
	}
	producer, run := prod.Identity()
	if ack.Identity.Batch.Producer != producer || ack.Identity.Batch.Run != run {
		return fmt.Errorf("%w: ack names another run", delivery.ErrInvalidValue)
	}
	rec, ok := prod.Batch(seq)
	if !ok {
		return fmt.Errorf("%w: sequence %d has no batch", delivery.ErrNotFound, seq)
	}
	if !rec.HasIdentity || rec.State != delivery.StateSealed {
		return fmt.Errorf("%w: sequence %d is %s, an IngestAck needs SEALED",
			delivery.ErrState, seq, rec.State)
	}
	if !rec.Identity.SameIdentity(ack.Identity) {
		return fmt.Errorf("%w: IngestAck identity differs from batch %d",
			delivery.ErrImmutableConflict, seq)
	}
	return prod.SaveAckOutbox(seq, ack)
}

// CleanupPayload runs bounded payload cleanup steps of one acknowledged
// batch. It stops after maxSteps steps and reports whether payload
// remains; a restart resumes from the durable progress. The outbox entry
// and the IngestAck are never touched.
func CleanupPayload(prod delivery.ProducerStore, seq uint64, maxSteps int) (remains bool, err error) {
	if maxSteps <= 0 {
		return false, fmt.Errorf("%w: maxSteps %d is not positive", delivery.ErrInvalidValue, maxSteps)
	}
	for step := 0; step < maxSteps; step++ {
		remains, err = prod.CleanupPayload(seq)
		if err != nil || !remains {
			return remains, err
		}
	}
	return remains, nil
}

// ReleaseReady reports whether the producer may issue the ordered Release
// of the sequence: the outbox entry exists and the payload cleanup is
// finished (release only after local cleanup).
func ReleaseReady(prod delivery.ProducerStore, seq uint64) bool {
	rec, ok := prod.Batch(seq)
	return ok && rec.Outbox && rec.State == delivery.StateAckedCleanup && !rec.HasPayload
}

// OnReleaseAck applies one ReleaseAck to the producer store. It accepts
// the idempotent cases — the batch already retired, or a gateway prefix
// at or above the sequence (the gateway is one release ahead after a lost
// ReleaseAck and never rolls back) — and otherwise atomically advances
// the retired prefix and deletes the outbox. An unacknowledged batch is
// never treated as delivered.
func OnReleaseAck(prod delivery.ProducerStore, ack delivery.ReleaseAck) error {
	if err := ack.Validate(); err != nil {
		return err
	}
	seq := ack.Batch.Seq
	prefix := prod.RetiredPrefix()
	if seq <= prefix {
		// Already retired by an earlier ack: the producer holds only the
		// compact prefix now.
		return nil
	}
	rec, ok := prod.Batch(seq)
	if !ok {
		return fmt.Errorf("%w: sequence %d has no outbox entry; an unacknowledged batch is not delivered by this ack",
			delivery.ErrState, seq)
	}
	if !rec.Outbox || rec.Ack.Identity.Batch.Seq == 0 {
		return fmt.Errorf("%w: sequence %d carries no durable IngestAck", delivery.ErrState, seq)
	}
	if ack.RetiredPrefix < seq {
		return fmt.Errorf("%w: ReleaseAck prefix %d does not cover batch %d",
			delivery.ErrState, ack.RetiredPrefix, seq)
	}
	return prod.Retire(seq, rec.Ack)
}

// HasAck reports whether the producer record carries the durable ack.
func recordHasAck(rec delivery.ProducerRecord) bool { return rec.Outbox }

// Release drives the gateway side of one ordered release under the fence:
// the release barrier has passed (the engine's caller owns the publisher
// barrier), RELEASING is synced with a cleanup cursor, the bounded cleanup
// runs at most maxSteps steps (a restart resumes from the durable cursor),
// and the final atomic commit advances the retired prefix and removes the
// RELEASING marker. The ReleaseAck is returned only after that commit.
// Releasing an already retired sequence repeats the stored ack.
func Release(gw delivery.GatewayStore, fence delivery.Fence, id delivery.BatchID, maxSteps int) (delivery.ReleaseAck, error) {
	if err := id.Validate(); err != nil {
		return delivery.ReleaseAck{}, err
	}
	if maxSteps <= 0 {
		return delivery.ReleaseAck{}, fmt.Errorf("%w: maxSteps %d is not positive",
			delivery.ErrInvalidValue, maxSteps)
	}
	prefix := gw.RetiredPrefix(id.Producer, id.Run)
	if id.Seq <= prefix {
		// Lost ReleaseAck replay: the gateway stays one release ahead and
		// answers with its durable prefix; already deleted batches are
		// not required again.
		return delivery.ReleaseAck{Batch: id, RetiredPrefix: prefix}, nil
	}
	if id.Seq != prefix+1 {
		return delivery.ReleaseAck{}, fmt.Errorf("%w: release of %d jumps the frontier %d (one in-flight release per run)",
			delivery.ErrState, id.Seq, prefix)
	}
	if err := gw.BeginRelease(fence, id); err != nil {
		return delivery.ReleaseAck{}, err
	}
	for step := 0; step < maxSteps; step++ {
		more, err := gw.CleanupStep(fence, id)
		if err != nil {
			return delivery.ReleaseAck{}, err
		}
		if !more {
			break
		}
	}
	return gw.FinishRelease(fence, id)
}

// RecoveryRelease completes the delivery of one batch after a gateway
// store loss, without the payload the producer may already have deleted.
// The producer's durable ACKED/outbox assertion must name exactly the
// sequence above the producer's retired prefix, and the gateway's adopted
// baseline must be exactly one below the sequence; the gateway syncs the
// assertion, advances the prefix and answers ReleaseAck. An arbitrary
// jump, an unacknowledged producer batch or a conflicting local record is
// an error that advances nothing.
func RecoveryRelease(prod delivery.ProducerStore, gw delivery.GatewayStore, fence delivery.Fence, seq uint64) (delivery.ReleaseAck, error) {
	producer, run := prod.Identity()
	if fence.Producer != producer || fence.Run != run {
		return delivery.ReleaseAck{}, fmt.Errorf("%w: fence names another run", delivery.ErrFenced)
	}
	prodPrefix := prod.RetiredPrefix()
	if seq != prodPrefix+1 {
		return delivery.ReleaseAck{}, fmt.Errorf("%w: recovery release of %d is not the producer's next batch after prefix %d",
			delivery.ErrState, seq, prodPrefix)
	}
	rec, ok := prod.Batch(seq)
	if !ok || !rec.Outbox || !recordHasAck(rec) {
		return delivery.ReleaseAck{}, fmt.Errorf("%w: sequence %d has no durable ACKED/outbox assertion",
			delivery.ErrState, seq)
	}
	// The payload is deliberately not consulted: an acknowledged batch
	// that already cleaned its payload recovers through this path.
	ack, err := gw.RecordRecoveryRelease(fence, rec.Ack)
	if err != nil {
		return delivery.ReleaseAck{}, err
	}
	return ack, nil
}

// AdoptBaseline installs the producer's durable retired prefix as the
// baseline of a recovered gateway store, after the old operations were
// fenced. Only the current fenced owner may adopt; a lower producer
// prefix never rolls the gateway back; an unconfirmed local batch below
// the claim is an error that deletes nothing.
func AdoptBaseline(gw delivery.GatewayStore, fence delivery.Fence, prefix uint64) error {
	return gw.AdoptBaseline(fence, fence.Producer, fence.Run, prefix)
}

// ImportSealed registers the exact issuer plan of a producer SEALED batch
// on a recovered gateway store: the original issuer store epoch, layout
// and frame identities stand, and no new sequence is allocated. A
// different manifest or seed under the same batch id is an immutable
// conflict. The chunked transport of this import belongs to
// E03-02.rpc_streaming; this function owns the durable state rule.
func ImportSealed(prod delivery.ProducerStore, gw delivery.GatewayStore, fence delivery.Fence, seq uint64) (delivery.BatchIdentity, error) {
	producer, run := prod.Identity()
	if fence.Producer != producer || fence.Run != run {
		return delivery.BatchIdentity{}, fmt.Errorf("%w: fence names another run", delivery.ErrFenced)
	}
	plan, ok := prod.SealedPlan(seq)
	if !ok {
		return delivery.BatchIdentity{}, fmt.Errorf("%w: sequence %d has no SEALED plan", delivery.ErrNotFound, seq)
	}
	if err := gw.ImportClosed(fence, plan, true); err != nil {
		return delivery.BatchIdentity{}, err
	}
	return plan.Identity, nil
}

// NewSealedPlan is a small constructor producers and tests use to pin the
// immutable plan fields before CommitSealed.
func NewSealedPlan(identity delivery.BatchIdentity, sequenceBase uint64, stream string, keys []delivery.FrameKey) delivery.SealedPlan {
	return delivery.SealedPlan{Identity: identity, SequenceBase: sequenceBase,
		Stream: stream, FrameKeys: append([]delivery.FrameKey(nil), keys...)}
}
