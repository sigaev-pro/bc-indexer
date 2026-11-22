// The in-memory gateway store adapter. It implements GatewayStore with
// plain maps so the publisher, release, maintenance and fault-matrix tests
// run without RocksDB, and it models the durable/RAM boundary of the crash
// oracle: frame chunks received but not yet closed live in a RAM overlay
// that Reopen discards, while every interface mutation is one atomic
// durable commit. The adapter is the test double of E03-01.gateway_store.
package delivery

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// runKey indexes the per-run retired prefixes and batches.
type runKey struct {
	producer proto.ID16
	run      proto.ID16
}

// MemoryGateway is the in-memory GatewayStore adapter.
type MemoryGateway struct {
	mu       sync.Mutex
	epoch    proto.ID16
	session  *Fence
	prefixes map[runKey]uint64
	batches  map[runKey]map[uint64]*GatewayRecord
	// frameChunks is the RAM overlay of RECEIVING frame chunks: visible
	// to reads but discarded by Reopen.
	frameChunks map[runKey]map[uint64][]string

	// Maintenance state: active binding, sealed generation, switch phase
	// and resume record of the cutover protocol.
	activeGeneration uint64
	activeDigest     proto.Hash32
	sealAck          *SealAck
	switchPhase      SwitchPhase
	desiredDigest    proto.Hash32
	switchPrepared   SwitchPrepared
	resumeAck        *ResumeAck
}

// NewMemoryGateway returns an empty gateway store with the given store
// epoch. A recovered store gets a fresh epoch here or in the caller.
func NewMemoryGateway(epoch proto.ID16) (*MemoryGateway, error) {
	if epoch.IsZero() {
		return nil, fmt.Errorf("%w: store epoch is zero", ErrInvalidValue)
	}
	return &MemoryGateway{
		epoch:       epoch,
		prefixes:    make(map[runKey]uint64),
		batches:     make(map[runKey]map[uint64]*GatewayRecord),
		frameChunks: make(map[runKey]map[uint64][]string),
	}, nil
}

// StoreEpoch returns the persistent store identity.
func (m *MemoryGateway) StoreEpoch() proto.ID16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epoch
}

// OpenSession installs a session fence, replacing any previous one. An
// old token never survives a new session.
func (m *MemoryGateway) OpenSession(fence Fence) error {
	if fence.Producer.IsZero() || fence.Run.IsZero() {
		return fmt.Errorf("%w: session without producer or run", ErrInvalidValue)
	}
	if len(fence.SessionToken) == 0 {
		return fmt.Errorf("%w: session without token", ErrInvalidValue)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.session = &fence
	return nil
}

// checkFence verifies the session under the store lock: same token, same
// owner run, same generations, same binding digest and the wanted kind.
func (m *MemoryGateway) checkFence(fence Fence, want SessionKind) error {
	if m.session == nil {
		return fmt.Errorf("%w: no session is open", ErrFenced)
	}
	s := m.session
	if !bytes.Equal(s.SessionToken, fence.SessionToken) {
		return fmt.Errorf("%w: stale session token", ErrFenced)
	}
	if s.Producer != fence.Producer || s.Run != fence.Run {
		return fmt.Errorf("%w: session of another run", ErrFenced)
	}
	if s.ConnectionGeneration != fence.ConnectionGeneration {
		return fmt.Errorf("%w: connection generation %d is not the session's %d",
			ErrFenced, fence.ConnectionGeneration, s.ConnectionGeneration)
	}
	if s.AdmissionGeneration != fence.AdmissionGeneration {
		return fmt.Errorf("%w: admission generation %d is not the session's %d (late RPC of an old seal)",
			ErrFenced, fence.AdmissionGeneration, s.AdmissionGeneration)
	}
	if s.BindingDigest != fence.BindingDigest {
		return fmt.Errorf("%w: binding digest does not match the session", ErrFenced)
	}
	if want == SessionNormal && fence.Kind != SessionNormal {
		return fmt.Errorf("%w: a MAINTENANCE session never accepts data steps", ErrFenced)
	}
	return nil
}

// runOf maps a fence to its run key.
func runOf(fence Fence) runKey { return runKey{producer: fence.Producer, run: fence.Run} }

// RetiredPrefix returns the retired prefix of the run.
func (m *MemoryGateway) RetiredPrefix(producer, run proto.ID16) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prefixes[runKey{producer: producer, run: run}]
}

// Batch reads one batch record of the fence's run.
func (m *MemoryGateway) Batch(fence Fence, id BatchID) (GatewayRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFence(fence, SessionNormal); err != nil {
		return GatewayRecord{}, false
	}
	rec, ok := m.batches[runOf(fence)][id.Seq]
	if !ok {
		return GatewayRecord{}, false
	}
	out := *rec
	out.Acked = make(map[FrameKey]struct{}, len(rec.Acked))
	for k := range rec.Acked {
		out.Acked[k] = struct{}{}
	}
	out.FrameKeys = append([]FrameKey(nil), rec.FrameKeys...)
	return out, true
}

// ImportClosed registers a CLOSED batch, the single exit of the ordinary
// publish stream and of IMPORT_SEALED. With preserveEpoch the issuer epoch
// of the imported plan stands (no new allocation); otherwise the store
// epoch of this gateway becomes the issuer.
func (m *MemoryGateway) ImportClosed(fence Fence, plan SealedPlan, preserveEpoch bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFence(fence, SessionNormal); err != nil {
		return err
	}
	key := runOf(fence)
	if err := m.guardRetiredLocked(key, plan.Identity.Batch.Seq); err != nil {
		return err
	}
	if m.sealAck != nil && fence.AdmissionGeneration <= m.sealAck.AdmissionGeneration {
		// SEALED_GENERATION forbids Prepare of the sealed admission
		// generation; new data waits for the Resume generation.
		return fmt.Errorf("%w: admission generation %d is sealed (DRAIN_REQUIRED)",
			ErrState, fence.AdmissionGeneration)
	}
	if !preserveEpoch {
		plan.Identity.IssuerStoreEpoch = m.epoch
	}
	if err := plan.Identity.Validate(); err != nil {
		return err
	}
	if plan.Identity.BindingDigest != fence.BindingDigest {
		return fmt.Errorf("%w: plan binding digest differs from the session binding", ErrFenced)
	}
	existing, ok := m.batches[key][plan.Identity.Batch.Seq]
	if ok {
		if existing.Identity.SameIdentity(plan.Identity) && existing.State != StateReleasing {
			return nil // idempotent replay of the same import
		}
		return fmt.Errorf("%w: batch %d already carries a different identity",
			ErrImmutableConflict, plan.Identity.Batch.Seq)
	}
	if m.batches[key] == nil {
		m.batches[key] = make(map[uint64]*GatewayRecord)
	}
	m.batches[key][plan.Identity.Batch.Seq] = &GatewayRecord{
		State:     StateClosed,
		Identity:  plan.Identity,
		Acked:     make(map[FrameKey]struct{}),
		FrameKeys: append([]FrameKey(nil), plan.FrameKeys...),
	}
	return nil
}

// guardRetiredLocked rejects sequences at or below the retired prefix.
func (m *MemoryGateway) guardRetiredLocked(key runKey, seq uint64) error {
	if seq <= m.prefixes[key] {
		return fmt.Errorf("%w: sequence %d is at or below the retired prefix %d",
			ErrAlreadyRetired, seq, m.prefixes[key])
	}
	return nil
}

// PrepareGuard answers the ALREADY_RETIRED rule without creating keys.
func (m *MemoryGateway) PrepareGuard(fence Fence, id BatchID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFence(fence, SessionNormal); err != nil {
		return err
	}
	if err := id.Validate(); err != nil {
		return err
	}
	return m.guardRetiredLocked(runOf(fence), id.Seq)
}

// StageFrameChunk buffers one RECEIVING frame chunk in RAM. It is the
// fault oracle's "not yet durable" layer; no publish path reads it.
func (m *MemoryGateway) StageFrameChunk(fence Fence, id BatchID, chunk string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFence(fence, SessionNormal); err != nil {
		return err
	}
	if err := m.guardRetiredLocked(runOf(fence), id.Seq); err != nil {
		return err
	}
	key := runOf(fence)
	if m.frameChunks[key] == nil {
		m.frameChunks[key] = make(map[uint64][]string)
	}
	m.frameChunks[key][id.Seq] = append(m.frameChunks[key][id.Seq], chunk)
	return nil
}

// StagedFrameChunks returns a copy of the RAM frame chunk overlay.
func (m *MemoryGateway) StagedFrameChunks(fence Fence, seq uint64) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]string(nil), m.frameChunks[runOf(fence)][seq]...)
	return out
}

// RecordPubAck accounts one PubAck. Only a publishable batch (CLOSED or
// NATS_ACKED before the barrier) accepts accounting; a RELEASING or gone
// batch rejects it, so a late callback cannot resurrect state.
func (m *MemoryGateway) RecordPubAck(fence Fence, id BatchID, planDigest proto.Hash32, key FrameKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFence(fence, SessionNormal); err != nil {
		return err
	}
	rk := runOf(fence)
	if err := m.guardRetiredLocked(rk, id.Seq); err != nil {
		return err
	}
	rec, ok := m.batches[rk][id.Seq]
	if !ok {
		return fmt.Errorf("%w: batch %d is not registered", ErrNotFound, id.Seq)
	}
	if rec.Identity.PlanDigest != planDigest {
		return fmt.Errorf("%w: PubAck of plan %s under batch %d sealed for another plan",
			ErrImmutableConflict, planDigest, id.Seq)
	}
	switch rec.State {
	case StateClosed, StateNATSACKed:
		rec.Acked[key] = struct{}{} // duplicate accounting is safe
		return nil
	default:
		return fmt.Errorf("%w: batch %d is %s, a late PubAck cannot resurrect it",
			ErrFenced, id.Seq, rec.State)
	}
}

// MarkNATSACKed syncs NATS_ACKED once every expected frame key carries a
// PubAck and returns the durable IngestAck. A partial set never completes
// the batch.
func (m *MemoryGateway) MarkNATSACKed(fence Fence, identity BatchIdentity) (IngestAck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFence(fence, SessionNormal); err != nil {
		return IngestAck{}, err
	}
	if err := identity.Validate(); err != nil {
		return IngestAck{}, err
	}
	rk := runOf(fence)
	rec, ok := m.batches[rk][identity.Batch.Seq]
	if !ok {
		return IngestAck{}, fmt.Errorf("%w: batch %d is not registered",
			ErrNotFound, identity.Batch.Seq)
	}
	if !rec.Identity.SameIdentity(identity) {
		return IngestAck{}, fmt.Errorf("%w: NATS_ACKED identity differs from batch %d",
			ErrImmutableConflict, identity.Batch.Seq)
	}
	switch rec.State {
	case StateNATSACKed:
		return rec.Ack, nil // idempotent
	case StateClosed:
		for _, k := range rec.FrameKeys {
			if _, ok := rec.Acked[k]; !ok {
				return IngestAck{}, fmt.Errorf("%w: batch %d misses a PubAck of frame %s; a partial set does not complete the batch",
					ErrState, identity.Batch.Seq, k)
			}
		}
		rec.State = StateNATSACKed
		rec.Ack = IngestAck{Identity: rec.Identity}
		rec.HasAck = true
		return rec.Ack, nil
	default:
		return IngestAck{}, fmt.Errorf("%w: batch %d is %s, NATS_ACKED needs CLOSED",
			ErrState, identity.Batch.Seq, rec.State)
	}
}

// BeginRelease syncs RELEASING for exactly the next sequence of the run.
func (m *MemoryGateway) BeginRelease(fence Fence, id BatchID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFence(fence, SessionNormal); err != nil {
		return err
	}
	rk := runOf(fence)
	if err := m.guardRetiredLocked(rk, id.Seq); err != nil {
		return nil // already retired: an idempotent repeat is fine
	}
	if id.Seq != m.prefixes[rk]+1 {
		return fmt.Errorf("%w: release of %d is not the next batch after prefix %d (one in-flight release)",
			ErrState, id.Seq, m.prefixes[rk])
	}
	rec, ok := m.batches[rk][id.Seq]
	if !ok {
		return fmt.Errorf("%w: batch %d is not registered", ErrNotFound, id.Seq)
	}
	switch rec.State {
	case StateNATSACKed:
		rec.State = StateReleasing
		rec.ReleasingCursor = 0
		return nil
	case StateReleasing:
		return nil // idempotent restart of the bounded cleanup
	default:
		return fmt.Errorf("%w: batch %d is %s, release needs NATS_ACKED",
			ErrState, id.Seq, rec.State)
	}
}

// CleanupStep deletes the next bounded chunk and syncs the cursor.
func (m *MemoryGateway) CleanupStep(fence Fence, id BatchID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFence(fence, SessionNormal); err != nil {
		return false, err
	}
	rk := runOf(fence)
	rec, ok := m.batches[rk][id.Seq]
	if !ok {
		return false, fmt.Errorf("%w: batch %d is not registered", ErrNotFound, id.Seq)
	}
	if rec.State != StateReleasing {
		return false, fmt.Errorf("%w: batch %d is %s, cleanup needs RELEASING",
			ErrState, id.Seq, rec.State)
	}
	rec.ReleasingCursor++
	more := rec.ReleasingCursor < uint64(len(rec.FrameKeys))
	return more, nil
}

// FinishRelease is the final atomic commit: prefix advance, RELEASING
// marker and batch record removal, then the ReleaseAck.
func (m *MemoryGateway) FinishRelease(fence Fence, id BatchID) (ReleaseAck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFence(fence, SessionNormal); err != nil {
		return ReleaseAck{}, err
	}
	rk := runOf(fence)
	if id.Seq <= m.prefixes[rk] {
		// Lost ReleaseAck replay: the gateway is one release ahead and
		// never rolls its prefix back.
		return ReleaseAck{Batch: id, RetiredPrefix: m.prefixes[rk]}, nil
	}
	rec, ok := m.batches[rk][id.Seq]
	if !ok {
		return ReleaseAck{}, fmt.Errorf("%w: batch %d is not registered", ErrNotFound, id.Seq)
	}
	if rec.State != StateReleasing {
		return ReleaseAck{}, fmt.Errorf("%w: batch %d is %s, finish needs RELEASING",
			ErrState, id.Seq, rec.State)
	}
	if rec.ReleasingCursor < uint64(len(rec.FrameKeys)) {
		return ReleaseAck{}, fmt.Errorf("%w: bounded cleanup of batch %d is not finished (%d of %d)",
			ErrState, id.Seq, rec.ReleasingCursor, len(rec.FrameKeys))
	}
	m.prefixes[rk] = id.Seq
	delete(m.batches[rk], id.Seq)
	delete(m.frameChunks[rk], id.Seq)
	return ReleaseAck{Batch: id, RetiredPrefix: id.Seq}, nil
}

// AdoptBaseline installs the producer's durable retired prefix as the
// baseline of a recovered store: only forward, only from the current
// fenced owner, never over an unconfirmed local batch.
func (m *MemoryGateway) AdoptBaseline(fence Fence, producer, run proto.ID16, prefix uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFence(fence, SessionNormal); err != nil {
		return err
	}
	if fence.Producer != producer || fence.Run != run {
		return fmt.Errorf("%w: baseline of another run", ErrFenced)
	}
	key := runKey{producer: producer, run: run}
	local := m.prefixes[key]
	if prefix < local {
		// The gateway may be one release ahead after a lost ReleaseAck;
		// it keeps its prefix and the producer finishes its outbox.
		return nil
	}
	if prefix == local {
		return nil
	}
	// A local record at or below the claimed baseline is unconfirmed
	// owner state: error, never deletion of pending.
	for seq := range m.batches[key] {
		if seq <= prefix {
			return fmt.Errorf("%w: local batch %d is at or below the claimed baseline %d",
				ErrConflictPending, seq, prefix)
		}
	}
	m.prefixes[key] = prefix
	return nil
}

// RecordRecoveryRelease accepts the producer's durable assertion for
// exactly the next sequence, syncs it and advances the prefix — without
// the payload, which the producer may already have deleted.
func (m *MemoryGateway) RecordRecoveryRelease(fence Fence, ack IngestAck) (ReleaseAck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFence(fence, SessionNormal); err != nil {
		return ReleaseAck{}, err
	}
	if err := ack.Validate(); err != nil {
		return ReleaseAck{}, err
	}
	if ack.Identity.Batch.Producer != fence.Producer || ack.Identity.Batch.Run != fence.Run {
		return ReleaseAck{}, fmt.Errorf("%w: recovery assertion of another run", ErrFenced)
	}
	key := runOf(fence)
	seq := ack.Identity.Batch.Seq
	if seq != m.prefixes[key]+1 {
		return ReleaseAck{}, fmt.Errorf("%w: recovery release of %d is not exactly the next batch after prefix %d",
			ErrState, seq, m.prefixes[key])
	}
	if _, ok := m.batches[key][seq]; ok {
		return ReleaseAck{}, fmt.Errorf("%w: a local record of batch %d conflicts with the recovery assertion",
			ErrConflictPending, seq)
	}
	m.prefixes[key] = seq
	return ReleaseAck{Batch: ack.Identity.Batch, RetiredPrefix: seq}, nil
}

// Reopen models a gateway process restart on the same store: durable
// state survives, the RAM frame chunk overlay is lost.
func (m *MemoryGateway) Reopen() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.frameChunks = make(map[runKey]map[uint64][]string)
	m.session = nil
}
