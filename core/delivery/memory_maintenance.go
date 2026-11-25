// The maintenance extensions of the in-memory stores: drain points and
// seal acks, switch phases, resume generations and the admission seal that
// forbids new Prepare of a sealed generation. The same rules the RocksDB
// stores of E03-01 will carry; every mutating method is one atomic durable
// commit.
package delivery

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// ---- producer-side maintenance state -------------------------------------

// SetDrainPoint durably records the drain id and batch highwater of an
// exhausted native obligation set. It requires the DRAIN mode.
func (m *MemoryProducer) SetDrainPoint(drainID proto.ID16, highwater uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if drainID.IsZero() {
		return fmt.Errorf("%w: drain id is zero", ErrInvalidValue)
	}
	if m.mode != ModeDrain {
		return fmt.Errorf("%w: drain point needs DRAIN mode, store is %s", ErrState, m.mode)
	}
	if highwater != 0 && highwater != m.retired {
		return fmt.Errorf("%w: drain highwater %d differs from the retired prefix %d",
			ErrState, highwater, m.retired)
	}
	m.drainID = drainID
	m.drainHighwater = highwater
	return nil
}

// DrainPoint returns the durable drain point.
func (m *MemoryProducer) DrainPoint() (drainID proto.ID16, highwater uint64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.drainID, m.drainHighwater, !m.drainID.IsZero()
}

// SaveSealAck durably records DRAINED together with the seal ack. This is
// the producer's terminal drain commit; an ordinary restart without an
// explicit Resume never returns to LIVE.
func (m *MemoryProducer) SaveSealAck(ack SealAck) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ack.DrainID.IsZero() && ack.DrainID != m.drainID {
		return fmt.Errorf("%w: seal ack names drain %v, store holds %v",
			ErrImmutableConflict, ack.DrainID, m.drainID)
	}
	if ack.Highwater != m.drainHighwater {
		return fmt.Errorf("%w: seal ack highwater %d, store holds %d",
			ErrImmutableConflict, ack.Highwater, m.drainHighwater)
	}
	if m.sealAck != nil {
		if !m.sealAck.SameSeal(ack) {
			return fmt.Errorf("%w: store holds a different seal ack", ErrImmutableConflict)
		}
		return nil
	}
	m.sealAck = &ack
	m.mode = ModeDrained
	return nil
}

// SealAckSaved returns the durable seal ack.
func (m *MemoryProducer) SealAckSaved() (SealAck, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sealAck == nil {
		return SealAck{}, false
	}
	return *m.sealAck, true
}

// SaveSwitchIntent durably records the producer's switch intent.
func (m *MemoryProducer) SaveSwitchIntent(switchID proto.ID16, desiredDigest proto.Hash32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if switchID.IsZero() {
		return fmt.Errorf("%w: switch id is zero", ErrInvalidValue)
	}
	if m.switchIntentID.IsZero() {
		m.switchIntentID, m.switchIntentDigest = switchID, desiredDigest
		return nil
	}
	if m.switchIntentID != switchID || m.switchIntentDigest != desiredDigest {
		return fmt.Errorf("%w: a new switch cannot jump an unfinished one", ErrImmutableConflict)
	}
	return nil
}

// SwitchIntentSaved returns the durable switch intent.
func (m *MemoryProducer) SwitchIntentSaved() (proto.ID16, proto.Hash32, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.switchIntentID, m.switchIntentDigest, !m.switchIntentID.IsZero()
}

// CommitBinding durably commits the new binding of a switch on the
// producer. The commit is never rolled back automatically.
func (m *MemoryProducer) CommitBinding(digest proto.Hash32, generation uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mode != ModeDrained {
		return fmt.Errorf("%w: binding commit needs DRAINED, store is %s", ErrState, m.mode)
	}
	m.committedBindingDigest, m.committedBindingGeneration = digest, generation
	return nil
}

// CommittedBinding returns the durably committed binding of the producer.
func (m *MemoryProducer) CommittedBinding() (proto.Hash32, uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.committedBindingDigest, m.committedBindingGeneration,
		m.committedBindingGeneration != 0
}

// SaveResumeIntent durably records the resume intent. A repeat of the same
// intent is idempotent; a different one is a conflict while unfinished.
func (m *MemoryProducer) SaveResumeIntent(intent ResumeIntent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := intent.Validate(); err != nil {
		return err
	}
	if m.resumeIntent == nil {
		m.resumeIntent = &intent
		return nil
	}
	if m.resumeIntent.ResumeID != intent.ResumeID {
		return fmt.Errorf("%w: a new resume cannot jump an unfinished one", ErrImmutableConflict)
	}
	return nil
}

// ResumeIntentSaved returns the durable resume intent.
func (m *MemoryProducer) ResumeIntentSaved() (ResumeIntent, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resumeIntent == nil {
		return ResumeIntent{}, false
	}
	return *m.resumeIntent, true
}

// CommitLive durably records LIVE after a verified ResumeAck. Capture was
// blocked until this commit.
func (m *MemoryProducer) CommitLive(ack ResumeAck) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resumeIntent == nil || m.resumeIntent.ResumeID != ack.ResumeID {
		return fmt.Errorf("%w: live commit without the matching resume intent", ErrState)
	}
	m.mode = ModeLive
	m.resumeAck = &ack
	return nil
}

// ResumeAckSaved returns the resume ack of a finished resume.
func (m *MemoryProducer) ResumeAckSaved() (ResumeAck, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resumeAck == nil {
		return ResumeAck{}, false
	}
	return *m.resumeAck, true
}

// ---- gateway-side maintenance state ---------------------------------------

// ActiveBinding returns the active durable binding generation and digest.
func (m *MemoryGateway) ActiveBinding() (uint64, proto.Hash32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeGeneration, m.activeDigest
}

// SetActiveBinding installs the active binding (the bootstrap state).
func (m *MemoryGateway) SetActiveBinding(generation uint64, digest proto.Hash32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation == 0 {
		return fmt.Errorf("%w: binding generation is zero", ErrInvalidValue)
	}
	m.activeGeneration, m.activeDigest = generation, digest
	return nil
}

// EnterAwaitSwitch opens AWAIT_SWITCH: the desired binding differs, the
// old binding stays active, and only recovery and maintenance handshakes
// run. The presence of a new descriptor file never activates it.
func (m *MemoryGateway) EnterAwaitSwitch(desiredDigest proto.Hash32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if desiredDigest == m.activeDigest {
		return fmt.Errorf("%w: desired binding equals the active one; no switch is pending", ErrState)
	}
	if m.switchPhase == SwitchNone {
		m.switchPhase = SwitchAwait
		m.desiredDigest = desiredDigest
		return nil
	}
	if m.desiredDigest != desiredDigest {
		return fmt.Errorf("%w: desired config drifted; finish the saved switch first", ErrImmutableConflict)
	}
	return nil
}

// SwitchState returns the switch phase with its durable records.
func (m *MemoryGateway) SwitchState() (SwitchPhase, SwitchPrepared) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.switchPhase, m.switchPrepared
}

// PrepareSwitch durably accepts a verified switch intent. A repeated
// PrepareSwitch of the same identity returns the same receipt.
func (m *MemoryGateway) PrepareSwitch(fence Fence, desired BindingView, switchID proto.ID16) (SwitchPrepared, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFenceLocked(fence, true); err != nil {
		return SwitchPrepared{}, err
	}
	if m.sealAck == nil {
		return SwitchPrepared{}, fmt.Errorf("%w: switch needs a sealed generation (DRAIN_REQUIRED)", ErrState)
	}
	if m.switchPhase == SwitchCommittedPhase || m.switchPhase == SwitchPreparedPhase {
		if m.switchPrepared.SwitchID == switchID {
			return m.switchPrepared, nil
		}
		return SwitchPrepared{}, fmt.Errorf("%w: another switch is in phase %s",
			ErrImmutableConflict, m.switchPhase)
	}
	if desired.DescriptorDigest == m.activeDigest {
		return SwitchPrepared{}, fmt.Errorf("%w: desired binding equals the active one", ErrState)
	}
	if m.switchPhase == SwitchAwait && m.desiredDigest != desired.DescriptorDigest {
		return SwitchPrepared{}, fmt.Errorf("%w: prepared binding differs from the awaited desired digest",
			ErrImmutableConflict)
	}
	if desired.Generation != m.activeGeneration+1 {
		return SwitchPrepared{}, fmt.Errorf("%w: desired generation %d is not the next after active %d",
			ErrInvalidValue, desired.Generation, m.activeGeneration)
	}
	m.switchPhase = SwitchPreparedPhase
	m.desiredDigest = desired.DescriptorDigest
	m.switchPrepared = SwitchPrepared{SwitchID: switchID, OldDigest: m.activeDigest,
		NewDigest: desired.DescriptorDigest, DrainID: m.sealAck.DrainID,
		AdmissionGeneration: m.sealAck.AdmissionGeneration, IssuerStoreEpoch: m.epoch}
	return m.switchPrepared, nil
}

// CommitSwitch durably activates the prepared binding. A retry of the
// committed switch returns the same receipt.
func (m *MemoryGateway) CommitSwitch(fence Fence, switchID proto.ID16) (SwitchCommitted, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFenceLocked(fence, true); err != nil {
		return SwitchCommitted{}, err
	}
	switch m.switchPhase {
	case SwitchCommittedPhase:
		if m.switchPrepared.SwitchID == switchID {
			return SwitchCommitted{Prepared: m.switchPrepared}, nil
		}
		return SwitchCommitted{}, fmt.Errorf("%w: committed switch %v differs", ErrImmutableConflict, switchID)
	case SwitchPreparedPhase:
		if m.switchPrepared.SwitchID != switchID {
			return SwitchCommitted{}, fmt.Errorf("%w: unknown switch %v", ErrNotFound, switchID)
		}
		m.switchPhase = SwitchCommittedPhase
		m.activeGeneration++
		m.activeDigest = m.switchPrepared.NewDigest
		return SwitchCommitted{Prepared: m.switchPrepared}, nil
	default:
		return SwitchCommitted{}, fmt.Errorf("%w: switch is %s, commit needs PREPARED", ErrState, m.switchPhase)
	}
}

// SealedGeneration returns the durable sealed generation.
func (m *MemoryGateway) SealedGeneration() (SealAck, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sealAck == nil {
		return SealAck{}, false
	}
	return *m.sealAck, true
}

// SealGeneration seals under the barrier: no unfinished operation at or
// below the highwater, the retired prefix equals the highwater, the
// binding digest matches the active binding. A repeat of the same intent
// returns the same ack. The seal forbids any later Prepare of the sealed
// admission generation.
func (m *MemoryGateway) SealGeneration(fence Fence, intent SealIntent) (SealAck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFenceLocked(fence, false); err != nil {
		return SealAck{}, err
	}
	if err := intent.Validate(); err != nil {
		return SealAck{}, err
	}
	if intent.BindingDigest != m.activeDigest || intent.BindingGeneration != m.activeGeneration {
		return SealAck{}, fmt.Errorf("%w: seal intent does not match the active binding", ErrState)
	}
	if m.sealAck != nil {
		if m.sealAck.DrainID == intent.DrainID {
			return *m.sealAck, nil
		}
		return SealAck{}, fmt.Errorf("%w: generation is already sealed", ErrImmutableConflict)
	}
	key := runOf(fence)
	if m.prefixes[key] != intent.Highwater {
		return SealAck{}, fmt.Errorf("%w: retired prefix %d differs from the highwater %d",
			ErrState, m.prefixes[key], intent.Highwater)
	}
	for seq, rec := range m.batches[key] {
		if seq <= intent.Highwater {
			return SealAck{}, fmt.Errorf("%w: batch %d is still %s at or below the highwater",
				ErrState, seq, rec.State)
		}
	}
	m.sealAck = &SealAck{DrainID: intent.DrainID, BindingDigest: intent.BindingDigest,
		AdmissionGeneration: intent.AdmissionGeneration, Highwater: intent.Highwater,
		IssuerStoreEpoch: m.epoch}
	return *m.sealAck, nil
}

// ResumeState returns the durable resume record.
func (m *MemoryGateway) ResumeState() (ResumeAck, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resumeAck == nil {
		return ResumeAck{}, false
	}
	return *m.resumeAck, true
}

// Resume accepts exactly the next admission generation under the barrier.
// A repeat of the same resume id returns the same ack; an old generation
// or an unknown id is rejected. Capture stays blocked until the producer's
// LIVE commit, which happens after this answer.
func (m *MemoryGateway) Resume(fence Fence, intent ResumeIntent) (ResumeAck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkFenceLocked(fence, true); err != nil {
		return ResumeAck{}, err
	}
	if err := intent.Validate(); err != nil {
		return ResumeAck{}, err
	}
	if m.sealAck == nil {
		return ResumeAck{}, fmt.Errorf("%w: resume needs a sealed generation", ErrState)
	}
	if m.resumeAck != nil {
		if m.resumeAck.ResumeID == intent.ResumeID {
			return *m.resumeAck, nil
		}
		return ResumeAck{}, fmt.Errorf("%w: another resume is already accepted", ErrImmutableConflict)
	}
	if m.switchPhase == SwitchPreparedPhase {
		// The switch of a seal runs between the seal and the Resume: its
		// commit decides the active binding the resume continues.
		return ResumeAck{}, fmt.Errorf("%w: a destination switch is prepared, the Resume waits for its commit", ErrState)
	}
	if intent.BindingDigest != m.activeDigest {
		return ResumeAck{}, fmt.Errorf("%w: resume binding differs from the active one", ErrState)
	}
	if intent.NextAdmissionGeneration != m.sealAck.AdmissionGeneration+1 {
		return ResumeAck{}, fmt.Errorf("%w: next admission generation %d is not exactly %d+1",
			ErrInvalidValue, intent.NextAdmissionGeneration, m.sealAck.AdmissionGeneration)
	}
	m.resumeAck = &ResumeAck{ResumeID: intent.ResumeID, BindingDigest: intent.BindingDigest,
		OldAdmissionGeneration: m.sealAck.AdmissionGeneration,
		NewAdmissionGeneration: intent.NextAdmissionGeneration,
		DrainID:                m.sealAck.DrainID, IssuerStoreEpoch: m.epoch}
	return *m.resumeAck, nil
}

// checkFenceLocked verifies the session under an already-held lock. The
// wantMaintenance flag admits the MAINTENANCE kind for the switch and
// resume steps, which the contract allows exactly for a saved transition
// intent; the data steps never admit it.
func (m *MemoryGateway) checkFenceLocked(fence Fence, transitionStep bool) error {
	if m.session == nil {
		return fmt.Errorf("%w: no session is open", ErrFenced)
	}
	s := m.session
	if string(s.SessionToken) != string(fence.SessionToken) ||
		s.Producer != fence.Producer || s.Run != fence.Run ||
		s.BindingDigest != fence.BindingDigest {
		return fmt.Errorf("%w: session of another owner or token", ErrFenced)
	}
	if s.ConnectionGeneration != fence.ConnectionGeneration {
		return fmt.Errorf("%w: connection generation %d is not the session's %d",
			ErrFenced, fence.ConnectionGeneration, s.ConnectionGeneration)
	}
	if fence.Kind == SessionMaintenance {
		if !transitionStep {
			return fmt.Errorf("%w: a MAINTENANCE session never accepts data or seal steps", ErrFenced)
		}
		// The admission generation of a maintenance session may be the
		// old or the new one of the saved transition, exactly as the
		// contract's recovery rule prescribes.
		return nil
	}
	if s.AdmissionGeneration != fence.AdmissionGeneration {
		return fmt.Errorf("%w: admission generation %d is not the session's %d",
			ErrFenced, fence.AdmissionGeneration, s.AdmissionGeneration)
	}
	return nil
}
