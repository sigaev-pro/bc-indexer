package journal

import (
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// BeginRelease starts the ordered release of one NATS_ACKED batch. The
// publisher barrier (E03-02.fencing) is raised by the caller before this
// call; the journal commits the RELEASING marker with its cleanup cursor in
// one atomic batch, after which no publication runs for the batch.
func (j *Journal) BeginRelease(id BatchID, planDigest [32]byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	state, err := j.readState(id)
	if err != nil {
		return err
	}
	switch state {
	case StateReleasing:
		// Idempotent, but the retried plan identity is still verified.
		marker, err := j.readNatsAcked(id)
		if err == nil && marker.PlanDigest != planDigest {
			return ErrIdentityMismatch
		}
		if err != nil && err != ErrBatchNotFound {
			return err
		}
		return nil
	case StateRetired:
		return ErrAlreadyRetired
	case StateNatsAcked:
	default:
		return ErrStateConflict
	}
	marker, err := j.readNatsAcked(id)
	if err != nil {
		return err
	}
	if marker.PlanDigest != planDigest {
		return ErrIdentityMismatch
	}

	b := storage.NewBatch()
	b.Put(batchKey(id, "/releasing"), encodeReleasing(Releasing{}))
	j.putState(b, id, StateReleasing)
	return j.st.Apply(b)
}

// ReleaseCleanup deletes the batch payload in bounded steps (prepare
// chunks, frames, frame heads, layout entries, ackset); at most maxKeys keys
// per atomic call, so a crash resumes from the remaining keys. The RELEASING
// marker itself survives the whole bounded cleanup; done reports that no
// payload key is left. Missing keys after a crash are not a reason to
// publish again.
func (j *Journal) ReleaseCleanup(id BatchID, maxKeys int) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if maxKeys <= 0 {
		return false, ErrStateConflict
	}
	state, err := j.readState(id)
	if err != nil {
		return false, err
	}
	switch state {
	case StateRetired:
		return true, nil
	case StateReleasing:
	default:
		return false, ErrStateConflict
	}
	rel, err := j.readReleasing(id)
	if err != nil {
		return false, err
	}
	if rel.Done {
		return true, nil
	}

	prefixes := [][]byte{
		batchKey(id, "prepare/"),
		batchKey(id, "frames/"),
		batchKey(id, "framehead/"),
		batchKey(id, "layout/"),
		batchKey(id, "ackset/"),
	}
	b := storage.NewBatch()
	remaining := 0
	for _, prefix := range prefixes {
		lo, hi := keyRange(prefix)
		err := j.st.Iterate(lo, hi, func(k, _ []byte) error {
			if b.Len() < maxKeys {
				b.Delete(append([]byte(nil), k...))
			} else {
				remaining++
			}
			return nil
		})
		if err != nil {
			return false, err
		}
	}
	if remaining == 0 {
		b.Put(batchKey(id, "/releasing"), encodeReleasing(Releasing{Done: true}))
	}
	if err := j.st.Apply(b); err != nil {
		return false, err
	}
	return remaining == 0, nil
}

// CompleteRelease performs the final atomic commit of the ordered release:
// it advances the retired prefix of the producer/run exactly one batch,
// deletes the RELEASING marker with the remaining evidence records and
// leaves the batch RETIRED. ReleaseAck may be answered only after this
// commit. A batch at or below the prefix is an idempotent success; a jump
// over an unfinished earlier batch is rejected.
func (j *Journal) CompleteRelease(id BatchID) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	state, err := j.readState(id)
	if err != nil {
		return 0, err
	}
	prefix := j.readRetired(id)
	if id.Seq <= prefix || state == StateRetired {
		return prefix, nil
	}
	if id.Seq != prefix+1 {
		return prefix, ErrStateConflict // release is strictly ordered
	}
	if state != StateReleasing {
		return prefix, ErrStateConflict
	}
	rel, err := j.readReleasing(id)
	if err != nil {
		return 0, err
	}
	if !rel.Done {
		return prefix, ErrStateConflict
	}

	b := storage.NewBatch()
	b.Put(retiredKey(id), u64be(id.Seq))
	b.Delete(batchKey(id, "/releasing"))
	b.Delete(batchKey(id, "/closed"))
	b.Delete(batchKey(id, "/nats_acked"))
	b.Delete(batchKey(id, "/publish"))
	b.Delete(batchKey(id, "/reservation"))
	b.Delete(batchKey(id, "/prepare_meta"))
	j.putState(b, id, StateRetired)
	if err := j.st.Apply(b); err != nil {
		return 0, err
	}
	j.forgetManifest(id)
	return id.Seq, nil
}

func (j *Journal) readReleasing(id BatchID) (Releasing, error) {
	raw, err := j.st.Get(batchKey(id, "/releasing"))
	if err == storage.ErrNotFound {
		return Releasing{}, ErrBatchNotFound
	}
	if err != nil {
		return Releasing{}, err
	}
	return decodeReleasing(raw)
}

// BatchInfo is the durable state of one batch, derived from its markers.
type BatchInfo struct {
	ID          BatchID
	State       State
	Reservation *Reservation
	Publish     *Publish
	Closed      *Closed
}

// Batch returns the durable state of one batch.
func (j *Journal) Batch(id BatchID) (BatchInfo, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	state, err := j.readState(id)
	if err != nil {
		return BatchInfo{}, err
	}
	info := BatchInfo{ID: id, State: state}
	if state != StatePreparing {
		if res, err := j.readReservation(id); err == nil {
			info.Reservation = &res
		} else if err != ErrBatchNotFound && state != StateRetired {
			return BatchInfo{}, err
		}
	}
	if state != StatePreparing && state != StatePrepared && state != StateRetired {
		if pub, err := j.readPublish(id); err == nil {
			info.Publish = &pub
		} else if err != ErrBatchNotFound {
			return BatchInfo{}, err
		}
	}
	if state == StateClosed || state == StateNatsAcked || state == StateReleasing {
		closed, err := j.readClosed(id)
		if err != nil {
			return BatchInfo{}, err
		}
		info.Closed = &closed
	}
	return info, nil
}
