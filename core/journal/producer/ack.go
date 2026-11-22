package producer

import (
	"sort"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// IngestAck is the full gateway acknowledgement of one manifest
// (CONTROL_WIRE_CONTRACT "Служебные запросы и ответы"). The producer keeps
// the whole record until a verified ReleaseAck; payload cleanup never
// deletes it.
type IngestAck struct {
	ManifestDigest [32]byte
	PlanDigest     [32]byte
	LayoutDigest   [32]byte
	BindingDigest  [32]byte
	IssuerEpoch    [16]byte
}

// OnIngestAck verifies the acknowledgement against the SEALED plan and, in
// one atomic commit, records the full ack plus the release_outbox entry and
// switches the batch to ACKED_CLEANUP. A partial acknowledgement or a bare
// successful transport write never reaches this method. Retrying the same
// ack is a no-op; a different ack for the same batch is a conflict.
func (s *Store) OnIngestAck(seq uint64, ack IngestAck) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.readState(seq)
	if err != nil {
		return err
	}
	if state == StateAckedCleanup || state == StateRetired {
		if state == StateRetired {
			return nil // already fully released; a replayed ack changes nothing
		}
		old, err := s.readOutbox(seq)
		if err != nil {
			return err
		}
		if old == ack {
			return nil
		}
		return ErrImmutableConflict
	}
	if state != StateSealed {
		return ErrStateConflict
	}
	sealed, err := s.readSealed(seq)
	if err != nil {
		return err
	}
	ready, err := s.readReady(seq)
	if err != nil {
		return err
	}
	if sealed.PlanDigest != ack.PlanDigest || sealed.LayoutDigest != ack.LayoutDigest ||
		ready.ManifestDigest != ack.ManifestDigest || ack.BindingDigest != s.bnd {
		return ErrIngestAckMismatch
	}

	b := storage.NewBatch()
	b.Put(outboxKey(seq), encodeAck(ack))
	b.Put(cleanupKey(seq), encodeCleanup(Cleanup{}))
	s.putState(b, seq, StateAckedCleanup)
	return s.st.Apply(b)
}

// Cleanup deletes the batch payload in bounded steps: attempt data and
// manifest chunks, and every plan generation's chunks. At most maxKeys keys
// are removed per call, each call being one atomic batch, so a crash resumes
// from the durable cursor (the remaining keys themselves). It returns done
// when no payload key is left. The outbox, ack, READY marker and checkpoint
// survive: they are the replay evidence for RecoveryRelease.
func (s *Store) Cleanup(seq uint64, maxKeys int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if maxKeys <= 0 {
		return false, ErrStateConflict
	}
	state, err := s.readState(seq)
	if err != nil {
		return false, err
	}
	switch state {
	case StateRetired:
		return true, nil
	case StateAckedCleanup:
		clean, err := s.readCleanup(seq)
		if err != nil {
			return false, err
		}
		if clean.Done {
			return true, nil
		}
	default:
		return false, ErrStateConflict
	}

	prefixes := [][]byte{batchKey(seq, "attempt/"), planBatchPrefix(seq)}
	doomed, remaining, err := collectDoomed(s.st, prefixes, keyPlanSealed(seq), maxKeys)
	if err != nil {
		return false, err
	}
	b := storage.NewBatch()
	for _, k := range doomed {
		b.Delete(k)
	}
	if remaining == 0 {
		b.Put(cleanupKey(seq), encodeCleanup(Cleanup{Done: true}))
	}
	if err := s.st.Apply(b); err != nil {
		return false, err
	}
	return remaining == 0, nil
}

// OnReleaseAck completes the ordered release of one batch after its local
// cleanup finished and the gateway acknowledged the Release. Exactly one
// atomic commit advances meta/retired_prefix, deletes the release_outbox
// entry and marks the batch RETIRED. The acknowledgement must be for the
// next batch in sequence: a late ack cannot jump over an earlier unfinished
// batch. A replayed ack for an already retired prefix is a success, and the
// gateway being one release ahead (ack for seq <= retired_prefix) is the
// normal lost-ReleaseAck case, not an error.
func (s *Store) OnReleaseAck(seq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.readState(seq)
	if err != nil {
		return err
	}
	if seq <= s.ret || state == StateRetired {
		return nil
	}
	if seq != s.ret+1 {
		return ErrOutOfOrderRelease
	}
	if state != StateAckedCleanup {
		return ErrStateConflict
	}
	clean, err := s.readCleanup(seq)
	if err != nil {
		return err
	}
	if !clean.Done {
		return ErrStateConflict // Release only after local cleanup
	}

	b := storage.NewBatch()
	b.Put([]byte(keyRetired), u64be(seq))
	b.Delete(outboxKey(seq))
	b.Delete(cleanupKey(seq))
	b.Delete(keyPlanSealed(seq)) // RETIRED end state: payload and plan absent
	s.putState(b, seq, StateRetired)
	if err := s.st.Apply(b); err != nil {
		return err
	}
	s.ret = seq
	return nil
}

// OutboxEntry is one pending ordered release.
type OutboxEntry struct {
	Seq uint64
	Ack IngestAck
}

// Outbox returns the pending release entries in batch order. Release runs
// strictly by batch_seq with one release in flight per run; the entries are
// kept on disk until their own turn.
func (s *Store) Outbox() ([]OutboxEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lo, hi := keyRange([]byte(keyOutbox))
	var entries []OutboxEntry
	err := s.st.Iterate(lo, hi, func(k, v []byte) error {
		seq := beU64(k[len(keyOutbox):])
		ack, err := decodeAck(v)
		if err != nil {
			return err
		}
		entries = append(entries, OutboxEntry{Seq: seq, Ack: ack})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Seq < entries[j].Seq })
	return entries, nil
}

// BatchInfo is the durable state of one batch, derived from its markers.
type BatchInfo struct {
	Seq            uint64
	State          State
	Intent         Intent
	Ready          *Ready
	PlanDigest     [32]byte
	LayoutDigest   [32]byte
	PlanGeneration uint64
	Ack            *IngestAck
	CleanupDone    bool
}

// Batch returns the durable state of one batch.
func (s *Store) Batch(seq uint64) (BatchInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.readState(seq)
	if err != nil {
		return BatchInfo{}, err
	}
	info := BatchInfo{Seq: seq, State: state}
	if info.Intent, err = s.readIntent(seq); err != nil {
		return BatchInfo{}, err
	}
	if state != StateIntent && state != StateStaging {
		if raw, err := s.st.Get(batchKey(seq, "/ready")); err == nil {
			ready, derr := decodeReady(raw)
			if derr != nil {
				return BatchInfo{}, derr
			}
			info.Ready = &ready
		} else if err != storage.ErrNotFound {
			return BatchInfo{}, err
		}
	}
	if state != StateIntent && state != StateStaging && state != StateReady {
		// The SEALED marker survives cleanup but is deleted with the retire
		// commit ("RETIRED: payload and plan absent"), so it is read when
		// present and reported empty afterwards.
		if sealed, err := s.readSealed(seq); err == nil {
			info.PlanDigest = sealed.PlanDigest
			info.LayoutDigest = sealed.LayoutDigest
			info.PlanGeneration = sealed.Generation
		} else if err != ErrNotFound {
			return BatchInfo{}, err
		}
	}
	if state == StateAckedCleanup || state == StateRetired {
		if raw, err := s.st.Get(outboxKey(seq)); err == nil {
			ack, derr := decodeAck(raw)
			if derr != nil {
				return BatchInfo{}, derr
			}
			if state == StateAckedCleanup {
				info.Ack = &ack
			}
		} else if err != storage.ErrNotFound && state == StateAckedCleanup {
			return BatchInfo{}, err
		}
		if raw, err := s.st.Get(cleanupKey(seq)); err == nil && state == StateAckedCleanup {
			clean, derr := decodeCleanup(raw)
			if derr != nil {
				return BatchInfo{}, derr
			}
			info.CleanupDone = clean.Done
		}
	}
	return info, nil
}

// Checkpoint returns the capture/lifecycle checkpoint saved with READY.
func (s *Store) Checkpoint(seq uint64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.st.Get(batchKey(seq, "/checkpoint"))
	if err == storage.ErrNotFound {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func collectDoomed(st storage.Storage, prefixes [][]byte, keep []byte, maxKeys int) (doomed [][]byte, remaining int, err error) {
	for _, prefix := range prefixes {
		lo, hi := keyRange(prefix)
		err = st.Iterate(lo, hi, func(k, _ []byte) error {
			if string(k) == string(keep) {
				return nil // the SEALED closing marker survives until retire
			}
			if len(doomed) < maxKeys {
				doomed = append(doomed, append([]byte(nil), k...))
			} else {
				remaining++
			}
			return nil
		})
		if err != nil {
			return nil, 0, err
		}
	}
	return doomed, remaining, nil
}

func outboxKey(seq uint64) []byte {
	return append([]byte(keyOutbox), u64be(seq)...)
}

func cleanupKey(seq uint64) []byte {
	return append([]byte(keyCleanup), u64be(seq)...)
}

// planBatchPrefix covers every plan key of one batch.
func planBatchPrefix(seq uint64) []byte {
	return append([]byte(keyPlanPrefix), u64be(seq)...)
}

func (s *Store) readOutbox(seq uint64) (IngestAck, error) {
	raw, err := s.st.Get(outboxKey(seq))
	if err == storage.ErrNotFound {
		return IngestAck{}, ErrNotFound
	}
	if err != nil {
		return IngestAck{}, err
	}
	return decodeAck(raw)
}

func (s *Store) readCleanup(seq uint64) (Cleanup, error) {
	raw, err := s.st.Get(cleanupKey(seq))
	if err == storage.ErrNotFound {
		return Cleanup{}, ErrNotFound
	}
	if err != nil {
		return Cleanup{}, err
	}
	return decodeCleanup(raw)
}

func encodeAck(v IngestAck) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.hash(v.ManifestDigest)
	e.hash(v.PlanDigest)
	e.hash(v.LayoutDigest)
	e.hash(v.BindingDigest)
	e.id16(v.IssuerEpoch)
	return e.b
}

func decodeAck(b []byte) (IngestAck, error) {
	d := newDecoder(b)
	v := IngestAck{
		ManifestDigest: d.hash(),
		PlanDigest:     d.hash(),
		LayoutDigest:   d.hash(),
		BindingDigest:  d.hash(),
		IssuerEpoch:    d.id16(),
	}
	return v, d.done()
}
