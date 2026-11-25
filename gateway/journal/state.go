package journal

import "github.com/sigaev-pro/bc-indexer/core/journal/storage"

// IngestAck is the full durable acknowledgement of one delivered batch
// (CONTROL_WIRE_CONTRACT: {batch_id, manifest_digest, plan_digest,
// layout_manifest_digest, binding_digest, issuer_store_epoch}). It exists
// only once the NATS_ACKED marker is durable; a partial PubAck set or a
// successful transport write never composes one.
type IngestAck struct {
	Batch          BatchID
	ManifestDigest [32]byte
	PlanDigest     [32]byte
	LayoutDigest   [32]byte
	BindingDigest  [32]byte
	IssuerEpoch    [16]byte
}

// recoveredSuffix is the key suffix, under the batch id, of the IngestAck a
// RecoveryRelease accepted.
const recoveredSuffix = "/recovered"

// OutboxAssertion is the producer's durable ACKED/outbox assertion of a
// RecoveryRelease: the batch it released locally, with the plan and binding
// identity of the IngestAck it received. The gateway checks that the
// assertion and the saved IngestAck name one identity; it cannot inspect the
// producer's disk, so the assertion is evidence the producer states, not a
// fact the gateway verifies.
type OutboxAssertion struct {
	Batch         BatchID
	PlanDigest    [32]byte
	BindingDigest [32]byte
}

// BatchStatePage is the GetBatchState answer: the durable state of one
// batch, its IngestAck when that is durable, and one bounded page of frame
// cursors.
type BatchStatePage struct {
	// State is the durable state. It is 0 when the store holds no record of
	// the batch (NOT_FOUND on the wire).
	State State
	// Ack is set only while the NATS_ACKED marker is durable.
	Ack *IngestAck
	// Cursors is one page of the declared frames in (ordinal, fragment,
	// frame) order, each with its next expected offset.
	Cursors []FrameCursor
	// More reports that cursors follow the last one of the page.
	More bool
}

// BatchState reports the durable state of one batch bound to planDigest.
// Rules:
//
//   - A batch at or below the retired prefix is RETIRED. Its evidence was
//     deleted with the retire commit, so no plan is compared and no ack is
//     returned: only the retired prefix remembers it.
//   - A batch the store never saw is State 0 (NOT_FOUND).
//   - A PREPARING batch has no reservation and no plan: its state is
//     reported as it is, without cursors or ack.
//   - From PREPARED on the plan digest must equal the stored plan identity;
//     any other digest is ErrIdentityMismatch and answers nothing.
//   - The IngestAck is returned only while the NATS_ACKED marker is durable
//     (NATS_ACKED and RELEASING).
//   - The cursors are one page of at most limit declared frames after the
//     position after (nil starts at the first frame); More reports a
//     following page. The answer of one call stays bounded whatever the
//     batch size.
func (j *Journal) BatchState(id BatchID, planDigest [32]byte, after *FrameAddr, limit int) (BatchStatePage, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if limit <= 0 {
		return BatchStatePage{}, ErrStateConflict
	}
	if id.Seq <= j.readRetired(id) {
		return BatchStatePage{State: StateRetired}, nil
	}
	state, err := j.readState(id)
	if err == ErrBatchNotFound {
		return BatchStatePage{}, nil
	}
	if err != nil {
		return BatchStatePage{}, err
	}
	page := BatchStatePage{State: state}
	if state == StatePreparing {
		return page, nil
	}
	res, err := j.readReservation(id)
	if err != nil {
		return BatchStatePage{}, err
	}
	if state == StatePrepared {
		if PlanDigest(res.Seed) != planDigest {
			return BatchStatePage{}, ErrIdentityMismatch
		}
		return page, nil
	}
	pub, err := j.readPublish(id)
	if err != nil {
		return BatchStatePage{}, err
	}
	if pub.PlanDigest != planDigest {
		return BatchStatePage{}, ErrIdentityMismatch
	}
	if state == StateNatsAcked || state == StateReleasing {
		marker, err := j.readNatsAcked(id)
		if err != nil {
			return BatchStatePage{}, err
		}
		closed, err := j.readClosed(id)
		if err != nil {
			return BatchStatePage{}, err
		}
		page.Ack = &IngestAck{
			Batch:          id,
			ManifestDigest: marker.ManifestDigest,
			PlanDigest:     marker.PlanDigest,
			LayoutDigest:   closed.LayoutDigest,
			BindingDigest:  closed.BindingDigest,
			IssuerEpoch:    res.IssuerEpoch,
		}
	}
	cursors, more, err := j.frameCursorsAfter(id, after, limit)
	if err != nil {
		return BatchStatePage{}, err
	}
	page.Cursors, page.More = cursors, more
	return page, nil
}

// frameCursorsAfter lists at most limit declared frames in (ordinal,
// fragment, frame) order after the position after, reporting whether more
// frames follow.
func (j *Journal) frameCursorsAfter(id BatchID, after *FrameAddr, limit int) (cursors []FrameCursor, more bool, err error) {
	lo, hi := keyRange(batchKey(id, "layout/"))
	if after != nil {
		// Seek to the entry that holds the continuation position, so a page
		// deep in a large batch costs the page and not the batch.
		lo = layoutKey(id, after.Ordinal, after.FragmentIndex)
	}
	err = j.st.Iterate(lo, hi, func(k, v []byte) error {
		e, derr := decodeLayoutEntry(v)
		if derr != nil {
			return derr
		}
		for f := uint64(0); f < e.FrameCount(); f++ {
			addr := FrameAddr{Ordinal: e.Ordinal, FragmentIndex: e.FragmentIndex, FrameIndex: f}
			if after != nil && !frameAddrAfter(addr, *after) {
				continue
			}
			if len(cursors) == limit {
				more = true
				return storage.ErrStopIterate
			}
			prefix := frameKeyPrefix(id, addr.Ordinal, addr.FragmentIndex, addr.FrameIndex)
			cover, _, cerr := chunkCover(j.st, prefix)
			if cerr != nil {
				return cerr
			}
			cursors = append(cursors, FrameCursor{Addr: addr, NextBytes: cover})
		}
		return nil
	})
	return cursors, more, err
}

// frameAddrAfter orders two frame positions by (ordinal, fragment, frame).
func frameAddrAfter(a, b FrameAddr) bool {
	if a.Ordinal != b.Ordinal {
		return a.Ordinal > b.Ordinal
	}
	if a.FragmentIndex != b.FragmentIndex {
		return a.FragmentIndex > b.FragmentIndex
	}
	return a.FrameIndex > b.FrameIndex
}

// Release drives the ordered release of one NATS_ACKED batch up to its final
// atomic commit and returns the retired prefix of the producer/run; the
// ReleaseAck may be answered only after this call succeeded. Rules:
//
//   - A batch at or below the retired prefix answers that prefix again (a
//     lost ReleaseAck replay: the gateway stays one release ahead and never
//     rolls its prefix back).
//   - Release goes strictly by batch sequence: a batch above retired_prefix+1
//     is ErrStateConflict.
//   - Only a NATS_ACKED batch releases; any other state is ErrStateConflict.
//   - The manifest and plan digests must both equal the durable NATS_ACKED
//     marker: the ack binds the whole identity, one matching digest is not
//     enough.
//   - The cleanup runs in bounded steps of at most maxKeys keys; a crash
//     resumes from the remaining keys because the RELEASING marker survives
//     until the final commit.
func (j *Journal) Release(id BatchID, manifestDigest, planDigest [32]byte, maxKeys int) (uint64, error) {
	j.mu.Lock()
	prefix, retired, err := j.checkReleaseLocked(id, manifestDigest, planDigest, maxKeys)
	j.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if retired {
		return prefix, nil
	}
	if err := j.BeginRelease(id, planDigest); err != nil {
		return 0, err
	}
	for {
		done, err := j.ReleaseCleanup(id, maxKeys)
		if err != nil {
			return 0, err
		}
		if done {
			break
		}
	}
	return j.CompleteRelease(id)
}

// CheckRelease verifies a Release request against the durable state and
// changes nothing. retired reports that the batch is already at or below the
// retired prefix, so the release answers that prefix without a single step.
// It holds every rule that decides whether a release may start, so the RPC
// boundary runs it before it raises the publisher barrier: a refused release
// then never fences a batch that still publishes.
func (j *Journal) CheckRelease(id BatchID, manifestDigest, planDigest [32]byte, maxKeys int) (retired bool, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	_, retired, err = j.checkReleaseLocked(id, manifestDigest, planDigest, maxKeys)
	return retired, err
}

// checkReleaseLocked is the locked read that decides whether a release may
// start; the caller holds j.mu.
func (j *Journal) checkReleaseLocked(id BatchID, manifestDigest, planDigest [32]byte, maxKeys int) (prefix uint64, retired bool, err error) {
	if maxKeys <= 0 {
		return 0, false, ErrStateConflict
	}
	prefix = j.readRetired(id)
	if id.Seq <= prefix {
		return prefix, true, nil
	}
	if id.Seq != prefix+1 {
		return 0, false, ErrStateConflict // release is strictly ordered
	}
	state, err := j.readState(id)
	if err != nil {
		return 0, false, err
	}
	if state != StateNatsAcked && state != StateReleasing {
		return 0, false, ErrStateConflict
	}
	marker, err := j.readNatsAcked(id)
	if err != nil {
		return 0, false, err
	}
	if marker.PlanDigest != planDigest || marker.ManifestDigest != manifestDigest {
		return 0, false, ErrIdentityMismatch
	}
	return prefix, false, nil
}

// RecoveryRelease completes the delivery of one batch after a gateway store
// loss, without the payload the producer may already have deleted. The
// producer's durable ACKED/outbox assertion must name exactly the next
// sequence of the run and carry the IngestAck it received. One atomic commit
// records the assertion, marks the batch RETIRED and advances the retired
// prefix; the returned prefix may be acknowledged only after that commit.
// Rules:
//
//   - The assertion and the IngestAck must name the batch of the call and
//     one plan and binding identity; a zero issuer epoch is invalid.
//   - The IngestAck binding digest must be the active binding: an
//     acknowledgement of another destination is never released here.
//   - A batch at or below the retired prefix answers that prefix again. A
//     recovered batch answers only to the same assertion; a different one is
//     an immutable conflict.
//   - Only exactly the next sequence recovers: an arbitrary jump is
//     ErrStateConflict.
//   - Any existing local record of the batch conflicts with the assertion
//     (ErrImmutableConflict) and deletes nothing: RecoveryRelease is for a
//     batch the store lost, an existing one goes through Release.
func (j *Journal) RecoveryRelease(id BatchID, ack IngestAck, assertion OutboxAssertion) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if ack.Batch != id || assertion.Batch != id ||
		assertion.PlanDigest != ack.PlanDigest || assertion.BindingDigest != ack.BindingDigest ||
		ack.IssuerEpoch == ([16]byte{}) {
		return 0, ErrIdentityMismatch
	}
	if ack.BindingDigest != j.bnd {
		return 0, ErrBindingMismatch
	}
	prefix := j.readRetired(id)
	if id.Seq <= prefix {
		saved, err := j.readRecovered(id)
		if err == nil {
			if saved != ack {
				return 0, ErrImmutableConflict
			}
		} else if err != ErrBatchNotFound {
			return 0, err
		}
		return prefix, nil
	}
	if id.Seq != prefix+1 {
		return 0, ErrStateConflict // recovery release is exactly the next sequence
	}
	if _, err := j.readState(id); err == nil {
		return 0, ErrImmutableConflict // a local record conflicts with the assertion
	} else if err != ErrBatchNotFound {
		return 0, err
	}

	b := storage.NewBatch()
	b.Put(batchKey(id, recoveredSuffix), encodeIngestAck(ack))
	j.putState(b, id, StateRetired)
	b.Put(retiredKey(id), u64be(id.Seq))
	if err := j.st.Apply(b); err != nil {
		return 0, err
	}
	j.forgetManifest(id)
	return id.Seq, nil
}

func (j *Journal) readRecovered(id BatchID) (IngestAck, error) {
	raw, err := j.st.Get(batchKey(id, recoveredSuffix))
	if err == storage.ErrNotFound {
		return IngestAck{}, ErrBatchNotFound
	}
	if err != nil {
		return IngestAck{}, err
	}
	ack, err := decodeIngestAck(raw)
	if err != nil {
		return IngestAck{}, err
	}
	ack.Batch = id
	return ack, nil
}

// IngestAck record: the four digests and the issuer epoch (the batch is the key).
func encodeIngestAck(v IngestAck) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.raw(v.ManifestDigest[:])
	e.raw(v.PlanDigest[:])
	e.raw(v.LayoutDigest[:])
	e.raw(v.BindingDigest[:])
	e.raw(v.IssuerEpoch[:])
	return e.b
}

func decodeIngestAck(b []byte) (IngestAck, error) {
	d := newDecoder(b)
	v := IngestAck{}
	copy(v.ManifestDigest[:], d.raw(32))
	copy(v.PlanDigest[:], d.raw(32))
	copy(v.LayoutDigest[:], d.raw(32))
	copy(v.BindingDigest[:], d.raw(32))
	copy(v.IssuerEpoch[:], d.raw(16))
	if err := d.done(); err != nil {
		return IngestAck{}, err
	}
	return v, nil
}
