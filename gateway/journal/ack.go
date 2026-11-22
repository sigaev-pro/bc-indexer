package journal

import (
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// RecordPubAck accounts one transport frame acknowledgement against a CLOSED
// batch. The callback is bound to the batch/plan identity: an unknown frame
// or a non-closed batch never creates state. A duplicate acknowledgement of
// an already recorded frame is allowed (an in-flight NATS request may
// deliver a legal duplicate) and changes nothing. allDone reports whether
// every frame of every entry is now acknowledged.
func (j *Journal) RecordPubAck(id BatchID, planDigest [32]byte, addr FrameAddr) (allDone bool, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	state, err := j.readState(id)
	if err != nil {
		return false, err
	}
	switch state {
	case StateClosed, StateNatsAcked, StateReleasing, StateRetired:
	default:
		return false, ErrStateConflict // PubAck only runs on CLOSED batches
	}
	pub, err := j.readPublish(id)
	if err != nil {
		return false, err
	}
	if pub.PlanDigest != planDigest {
		return false, ErrIdentityMismatch
	}
	// An undeclared frame cannot be acknowledged; late callbacks never
	// create missing records.
	if _, err := j.frameTotalOf(id, addr); err != nil {
		return false, err
	}
	key := ackKey(id, addr.Ordinal, addr.FragmentIndex, addr.FrameIndex)
	if _, err := j.st.Get(key); err == storage.ErrNotFound {
		if err := j.st.Put(key, []byte{1}); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	if state != StateClosed {
		return true, nil // NATS_ACKED already covers everything
	}
	return j.allFramesAcked(id)
}

func (j *Journal) allFramesAcked(id BatchID) (bool, error) {
	lo, hi := keyRange(batchKey(id, "layout/"))
	acked := true
	err := j.st.Iterate(lo, hi, func(k, v []byte) error {
		e, derr := decodeLayoutEntry(v)
		if derr != nil {
			return derr
		}
		for f := uint64(0); f < e.FrameCount(); f++ {
			if _, err := j.st.Get(ackKey(id, e.Ordinal, e.FragmentIndex, f)); err == storage.ErrNotFound {
				acked = false
				return storage.ErrStopIterate
			} else if err != nil {
				return err
			}
		}
		return nil
	})
	return acked, err
}

// MarkNATSACKed records the durable NATS_ACKED marker for one manifest and
// plan identity. It requires every transport frame of every fragment of
// every event to be acknowledged; a partial acknowledgement never completes
// a batch. One atomic commit binds the marker with the state switch.
func (j *Journal) MarkNATSACKed(id BatchID, planDigest, manifestDigest [32]byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	state, err := j.readState(id)
	if err != nil {
		return err
	}
	if state == StateNatsAcked || state == StateReleasing || state == StateRetired {
		marker, err := j.readNatsAcked(id)
		if err != nil {
			return err
		}
		if marker.PlanDigest != planDigest || marker.ManifestDigest != manifestDigest {
			return ErrImmutableConflict
		}
		return nil
	}
	if state != StateClosed {
		return ErrStateConflict
	}
	closed, err := j.readClosed(id)
	if err != nil {
		return err
	}
	if closed.PlanDigest != planDigest || closed.ManifestDigest != manifestDigest {
		return ErrIdentityMismatch
	}
	done, err := j.allFramesAcked(id)
	if err != nil {
		return err
	}
	if !done {
		return ErrStateConflict
	}

	b := storage.NewBatch()
	b.Put(batchKey(id, "/nats_acked"), encodeNatsAcked(NatsAcked{PlanDigest: planDigest, ManifestDigest: manifestDigest}))
	j.putState(b, id, StateNatsAcked)
	return j.st.Apply(b)
}

func (j *Journal) readNatsAcked(id BatchID) (NatsAcked, error) {
	raw, err := j.st.Get(batchKey(id, "/nats_acked"))
	if err == storage.ErrNotFound {
		return NatsAcked{}, ErrBatchNotFound
	}
	if err != nil {
		return NatsAcked{}, err
	}
	return decodeNatsAcked(raw)
}
