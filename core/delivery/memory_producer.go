// The in-memory producer store adapter. It implements ProducerStore with
// plain maps so the release, maintenance and fault-matrix tests run without
// RocksDB, and it models the durable/RAM boundary the crash oracle needs:
// staged chunks live in a RAM overlay that Reopen discards, while every
// interface mutation is one atomic durable commit. The adapter is the test
// double of E03-01.producer_store; the real store adapts to the same
// interface.
package delivery

import (
	"fmt"
	"sync"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// MemoryProducer is the in-memory ProducerStore adapter.
type MemoryProducer struct {
	mu      sync.Mutex
	idProd  proto.ID16
	idRun   proto.ID16
	mode    ProducerMode
	retired uint64
	nextSeq uint64
	batches map[uint64]*ProducerRecord
	plans   map[uint64]SealedPlan
	// payloadChunks models the bounded payload cleanup: the number of
	// chunks still held for a sequence.
	payloadChunks map[uint64]int
	// staged is the RAM overlay of STAGING chunks: visible to reads but
	// discarded by Reopen.
	staged map[uint64][]string

	// Maintenance state: drain point, seal ack, switch intent, committed
	// binding and resume intent of the cutover protocol.
	drainID                    proto.ID16
	drainHighwater             uint64
	sealAck                    *SealAck
	switchIntentID             proto.ID16
	switchIntentDigest         proto.Hash32
	committedBindingDigest     proto.Hash32
	committedBindingGeneration uint64
	resumeIntent               *ResumeIntent
	resumeAck                  *ResumeAck
}

// NewMemoryProducer returns an empty producer store with the given
// persistent identities and mode LIVE.
func NewMemoryProducer(producer, run proto.ID16) (*MemoryProducer, error) {
	if producer.IsZero() || run.IsZero() {
		return nil, fmt.Errorf("%w: producer or run identity is zero", ErrInvalidValue)
	}
	return &MemoryProducer{
		idProd: producer, idRun: run, mode: ModeLive, nextSeq: 1,
		batches: make(map[uint64]*ProducerRecord), plans: make(map[uint64]SealedPlan),
		payloadChunks: make(map[uint64]int), staged: make(map[uint64][]string),
	}, nil
}

// Identity returns the persistent ids.
func (m *MemoryProducer) Identity() (proto.ID16, proto.ID16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.idProd, m.idRun
}

// Mode returns the durable mode.
func (m *MemoryProducer) Mode() ProducerMode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode
}

// SetMode durably records the mode. DRAINED never silently returns to
// LIVE: the only accepted LIVE transition comes through the maintenance
// Resume commit, which calls SetMode directly under its own guards.
func (m *MemoryProducer) SetMode(mode ProducerMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mode != ModeLive && mode != ModeDrain && mode != ModeDrained && mode != ModeBlocked {
		return fmt.Errorf("%w: unknown mode %d", ErrInvalidValue, uint8(mode))
	}
	m.mode = mode
	return nil
}

// RetiredPrefix returns the retired prefix.
func (m *MemoryProducer) RetiredPrefix() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.retired
}

// Batch reads one record. A retired batch reads as gone: the producer
// keeps only the compact retired prefix.
func (m *MemoryProducer) Batch(seq uint64) (ProducerRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.batches[seq]
	if !ok {
		return ProducerRecord{}, false
	}
	return *rec, true
}

// SealedPlan reads the sealed plan of a SEALED (or later) batch.
func (m *MemoryProducer) SealedPlan(seq uint64) (SealedPlan, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	plan, ok := m.plans[seq]
	return plan, ok
}

// AllocateIntent allocates the next sequence with a durable INTENT.
func (m *MemoryProducer) AllocateIntent() (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nextSeq == 0 {
		return 0, fmt.Errorf("%w: batch sequence counter exhausted", ErrCounterOverflow)
	}
	seq := m.nextSeq
	if seq+1 < seq {
		m.nextSeq = 0
	} else {
		m.nextSeq = seq + 1
	}
	m.batches[seq] = &ProducerRecord{State: StateIntent}
	return seq, nil
}

// StageChunk buffers one STAGING chunk in RAM. It is the fault oracle's
// "not yet durable" layer: nothing here survives Reopen, and no publish
// path may read it.
func (m *MemoryProducer) StageChunk(seq uint64, chunk string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.batches[seq]
	if !ok {
		return fmt.Errorf("%w: batch %d has no intent", ErrNotFound, seq)
	}
	if rec.State != StateIntent && rec.State != StateStaging {
		return fmt.Errorf("%w: batch %d is %s, staging is over", ErrState, seq, rec.State)
	}
	rec.State = StateStaging
	m.staged[seq] = append(m.staged[seq], chunk)
	return nil
}

// StagedChunks returns a copy of the RAM staging overlay of a batch.
func (m *MemoryProducer) StagedChunks(seq uint64) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.staged[seq]))
	copy(out, m.staged[seq])
	return out
}

// CommitReady closes STAGING into READY: the closing commit makes the
// staged chunks durable payload and fixes the manifest half of the
// identity (batch, manifest digest, binding digest). The plan half —
// plan digest, layout digest and issuer store epoch — is fixed by the
// reservation and CommitSealed: a READY batch replans freely after a
// gateway store loss as long as the manifest identity stands.
func (m *MemoryProducer) CommitReady(seq uint64, identity BatchIdentity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := identity.Batch.Validate(); err != nil {
		return err
	}
	if identity.Batch.Producer != m.idProd || identity.Batch.Run != m.idRun || identity.Batch.Seq != seq {
		return fmt.Errorf("%w: identity of sequence %d does not name this store and sequence",
			ErrInvalidValue, identity.Batch.Seq)
	}
	rec, ok := m.batches[seq]
	if !ok {
		return fmt.Errorf("%w: batch %d has no intent", ErrNotFound, seq)
	}
	switch rec.State {
	case StateIntent, StateStaging:
		rec.State = StateReady
		rec.Identity = manifestOf(identity)
		rec.HasIdentity = true
		chunks := len(m.staged[seq])
		if chunks == 0 {
			chunks = 1 // a closed batch always carries its payload
		}
		rec.HasPayload = true
		m.payloadChunks[seq] = chunks
		delete(m.staged, seq)
		return nil
	case StateReady:
		if rec.HasIdentity && sameManifest(rec.Identity, identity) {
			return nil // idempotent replay of the same closing commit
		}
		return fmt.Errorf("%w: batch %d is READY with a different manifest identity", ErrImmutableConflict, seq)
	default:
		return fmt.Errorf("%w: batch %d is %s, READY is closed", ErrState, seq, rec.State)
	}
}

// manifestOf zeroes the plan half of an identity.
func manifestOf(identity BatchIdentity) BatchIdentity {
	identity.PlanDigest = proto.Hash32{}
	identity.LayoutManifestDigest = proto.Hash32{}
	identity.IssuerStoreEpoch = proto.ID16{}
	return identity
}

// sameManifest compares the manifest half of two identities.
func sameManifest(a, b BatchIdentity) bool {
	return a.Batch == b.Batch && a.ManifestDigest == b.ManifestDigest && a.BindingDigest == b.BindingDigest
}

// CommitSealed closes READY into SEALED with the exact plan. The plan is
// immutable from here on: retries replay it and never re-plan.
func (m *MemoryProducer) CommitSealed(seq uint64, plan SealedPlan) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := plan.Identity.Validate(); err != nil {
		return err
	}
	if plan.Identity.Batch.Seq != seq {
		return fmt.Errorf("%w: plan sequence %d does not match %d",
			ErrInvalidValue, plan.Identity.Batch.Seq, seq)
	}
	rec, ok := m.batches[seq]
	if !ok {
		return fmt.Errorf("%w: batch %d has no record", ErrNotFound, seq)
	}
	if !rec.HasIdentity || !sameManifest(rec.Identity, plan.Identity) {
		return fmt.Errorf("%w: plan identity differs from the READY manifest identity of batch %d",
			ErrImmutableConflict, seq)
	}
	switch rec.State {
	case StateReady:
		// The seal fixes the plan half immutably: from here on every
		// replay carries the same plan digest, layout and issuer epoch.
		rec.State = StateSealed
		rec.Identity = plan.Identity
		m.plans[seq] = plan
		return nil
	case StateSealed:
		if m.plans[seq].Identity.SameIdentity(plan.Identity) {
			return nil
		}
		return fmt.Errorf("%w: batch %d is SEALED with a different plan", ErrImmutableConflict, seq)
	default:
		return fmt.Errorf("%w: batch %d is %s, SEALED needs READY", ErrState, seq, rec.State)
	}
}

// SaveAckOutbox atomically records ACKED_CLEANUP plus the release outbox.
func (m *MemoryProducer) SaveAckOutbox(seq uint64, ack IngestAck) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ack.Validate(); err != nil {
		return err
	}
	if ack.Identity.Batch.Seq != seq {
		return fmt.Errorf("%w: ack sequence %d does not match %d",
			ErrInvalidValue, ack.Identity.Batch.Seq, seq)
	}
	rec, ok := m.batches[seq]
	if !ok {
		return fmt.Errorf("%w: batch %d has no record", ErrNotFound, seq)
	}
	if !rec.HasIdentity || !rec.Identity.SameIdentity(ack.Identity) {
		return fmt.Errorf("%w: ack identity differs from the sealed identity of batch %d",
			ErrImmutableConflict, seq)
	}
	switch rec.State {
	case StateSealed:
		rec.State = StateAckedCleanup
		rec.Outbox = true
		rec.Ack = ack
		return nil
	case StateAckedCleanup:
		if rec.Ack.Identity.SameIdentity(ack.Identity) {
			return nil
		}
		return fmt.Errorf("%w: batch %d carries a different saved ack", ErrImmutableConflict, seq)
	default:
		return fmt.Errorf("%w: batch %d is %s, an ack needs SEALED", ErrState, seq, rec.State)
	}
}

// CleanupPayload deletes one bounded payload chunk per call. The outbox
// entry and the IngestAck survive: RecoveryRelease needs them after a
// Gateway store loss even with the payload gone.
func (m *MemoryProducer) CleanupPayload(seq uint64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.batches[seq]
	if !ok {
		return false, fmt.Errorf("%w: batch %d has no record", ErrNotFound, seq)
	}
	if rec.State != StateAckedCleanup || !rec.Outbox {
		return false, fmt.Errorf("%w: batch %d is %s, cleanup needs the ACKED_CLEANUP outbox",
			ErrState, seq, rec.State)
	}
	if m.payloadChunks[seq] > 0 {
		m.payloadChunks[seq]--
	}
	if m.payloadChunks[seq] == 0 {
		rec.HasPayload = false
		delete(m.payloadChunks, seq)
		return false, nil
	}
	return true, nil
}

// PayloadChunkCount returns the number of payload chunks still held.
func (m *MemoryProducer) PayloadChunkCount(seq uint64) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.payloadChunks[seq]
}

// Retire atomically advances the retired prefix and deletes the outbox.
// It requires a finished payload cleanup, the outbox entry and the exact
// next sequence — the frontier never jumps a pending batch.
func (m *MemoryProducer) Retire(seq uint64, ack IngestAck) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.batches[seq]
	if !ok {
		// The batch may already be retired (lost ReleaseAck replay).
		if seq <= m.retired {
			return nil
		}
		return fmt.Errorf("%w: batch %d has no outbox entry", ErrNotFound, seq)
	}
	if seq != m.retired+1 {
		return fmt.Errorf("%w: retire %d jumps the frontier %d", ErrState, seq, m.retired)
	}
	if !rec.Outbox || rec.State != StateAckedCleanup {
		return fmt.Errorf("%w: batch %d has no finished outbox entry", ErrState, seq)
	}
	if !rec.HasPayload {
		// Payload cleanup finished: RETIRED, payload and plan absent.
	} else {
		return fmt.Errorf("%w: batch %d still has payload", ErrState, seq)
	}
	if !rec.Ack.Identity.SameIdentity(ack.Identity) {
		return fmt.Errorf("%w: ReleaseAck ack differs from the outbox entry", ErrImmutableConflict)
	}
	m.retired = seq
	delete(m.batches, seq)
	delete(m.plans, seq)
	return nil
}

// Reopen models a process restart: durable state survives, the RAM
// staging overlay is lost. It is the fault oracle's cut point.
func (m *MemoryProducer) Reopen() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.staged = make(map[uint64][]string)
}
