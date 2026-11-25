package journal

// The drain and cutover control records of DELIVERY_STATE_MACHINE (D54,
// D57 and D59, the drain and cutover section): the active destination binding, the sealed
// generation of one producer/run, the destination switch and the resume that
// opens the next admission generation. Every mutating call is one atomic
// commit; every transition binds its whole intent, so a retry returns the
// durable receipt and one matching number alone is never enough.

import "github.com/sigaev-pro/bc-indexer/core/journal/storage"

// Binding is the content identity of one DestinationBinding the switch
// checks need: the destination line, the binding generation, the network and
// the descriptor digest. The gateway never admits a descriptor whose digest
// was not verified, so the journal receives it already parsed.
type Binding struct {
	DestinationID    [16]byte
	Generation       uint64
	ChainID          [32]byte
	GenesisHash      [32]byte
	DescriptorDigest [32]byte
}

// ActiveBinding returns the content identity of the active destination
// binding. known is false when the store holds only its digest (no switch
// committed and Open supplied none): a switch then cannot be checked for
// continuity and is refused.
func (j *Journal) ActiveBinding() (binding Binding, known bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.hasBinding {
		return Binding{}, false
	}
	return j.bindingLocked(), true
}

// bindingLocked is the active binding; the caller holds j.mu.
func (j *Journal) bindingLocked() Binding {
	return Binding{
		DestinationID:    j.dstID,
		Generation:       j.gen,
		ChainID:          j.chainID,
		GenesisHash:      j.genesis,
		DescriptorDigest: j.bnd,
	}
}

// applyBinding installs the active binding in memory.
func (j *Journal) applyBinding(b Binding) {
	j.bnd = b.DescriptorDigest
	j.gen = b.Generation
	j.dstID = b.DestinationID
	j.chainID = b.ChainID
	j.genesis = b.GenesisHash
	j.hasBinding = true
}

// Seal is the durable seal of one admission generation of a producer/run. On
// the wire it is the SealAck: the whole intent bound together.
type Seal struct {
	DrainID             [16]byte
	BindingDigest       [32]byte
	AdmissionGeneration uint64
	Highwater           uint64
	IssuerEpoch         [16]byte
	// BindingGeneration is the binding generation the seal was given for.
	// The wire SealAck does not carry it; a retry of the seal compares it.
	BindingGeneration uint64
}

// sameAck reports whether both seals bind the same whole intent on the wire.
func (s Seal) sameAck(o Seal) bool {
	return s.DrainID == o.DrainID && s.BindingDigest == o.BindingDigest &&
		s.AdmissionGeneration == o.AdmissionGeneration &&
		s.Highwater == o.Highwater && s.IssuerEpoch == o.IssuerEpoch
}

// SealIntent is the SealGeneration request: the binding and admission
// generations being sealed, the drain identity and the batch highwater every
// obligation below is finished for.
type SealIntent struct {
	BindingGeneration   uint64
	AdmissionGeneration uint64
	DrainID             [16]byte
	Highwater           uint64
}

// SealGeneration seals under the journal's serialized ordering and returns
// the SealAck. Rules:
//
//   - The binding generation must be the active one; the seal binds the
//     active binding digest.
//   - The retired prefix of the producer/run must equal the highwater and no
//     batch at or below it may be in a non-retired state: the frontier is
//     exhausted. Zero is a valid highwater for a run that never created a
//     batch (the empty frontier).
//   - A retry of the same drain returns the same SealAck when the whole
//     intent matches; the same drain id with another intent is an immutable
//     conflict.
//   - A sealed generation is sealed for good: only the admission generation
//     a Resume opened can be sealed again by a later drain. That commit
//     retires the resume intent.
func (j *Journal) SealGeneration(id BatchID, intent SealIntent) (Seal, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if intent.AdmissionGeneration == 0 || intent.BindingGeneration == 0 || intent.DrainID == ([16]byte{}) {
		return Seal{}, ErrInvalidIntent
	}
	seal, sealed, err := j.readSeal(id)
	if err != nil {
		return Seal{}, err
	}
	if sealed && seal.DrainID == intent.DrainID {
		if seal.AdmissionGeneration == intent.AdmissionGeneration &&
			seal.Highwater == intent.Highwater && seal.BindingGeneration == intent.BindingGeneration {
			return seal, nil // the retry returns the same SealAck
		}
		return Seal{}, ErrImmutableConflict
	}
	resume, resumed, err := j.readResume(id)
	if err != nil {
		return Seal{}, err
	}
	if sealed && (!resumed || resume.NewAdmissionGeneration != intent.AdmissionGeneration) {
		return Seal{}, ErrImmutableConflict // the generation is already sealed
	}
	if intent.BindingGeneration != j.gen {
		return Seal{}, ErrBindingGeneration
	}
	if prefix := j.readRetired(id); prefix != intent.Highwater {
		return Seal{}, ErrStateConflict // unfinished operations at or below the highwater
	}
	if err := j.assertRetiredBelow(id, intent.Highwater); err != nil {
		return Seal{}, err
	}

	next := Seal{
		DrainID:             intent.DrainID,
		BindingDigest:       j.bnd,
		AdmissionGeneration: intent.AdmissionGeneration,
		Highwater:           intent.Highwater,
		IssuerEpoch:         j.epoch,
		BindingGeneration:   intent.BindingGeneration,
	}
	b := storage.NewBatch()
	b.Put(sealedKey(id), encodeSeal(next))
	if resumed {
		b.Delete(resumeKey(id)) // the resumed generation is sealed: its intent is retired
	}
	if err := j.st.Apply(b); err != nil {
		return Seal{}, err
	}
	return next, nil
}

// SealedGeneration returns the durable seal of one producer/run.
func (j *Journal) SealedGeneration(id BatchID) (seal Seal, ok bool, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.readSeal(id)
}

// assertRetiredBelow verifies that no batch of the producer/run at or below
// highwater is in a non-retired state.
func (j *Journal) assertRetiredBelow(id BatchID, highwater uint64) error {
	lo := append([]byte(keyBatch), id.ProducerID[:]...)
	lo = append(lo, id.RunID[:]...)
	_, hi := keyRange(lo)
	const suffix = "/state"
	return j.st.Iterate(lo, hi, func(k, v []byte) error {
		rest := k[len(lo):]
		if len(rest) != 8+len(suffix) || string(rest[8:]) != suffix {
			return nil
		}
		if beU64(rest) <= highwater && (len(v) != 1 || State(v[0]) != StateRetired) {
			return ErrStateConflict
		}
		return nil
	})
}

// SwitchPhase is the durable phase of one destination switch.
type SwitchPhase uint8

// The switch phases.
const (
	// SwitchPreparedPhase: the desired target was verified and the switch
	// intent is durable; the active binding is still the old one.
	SwitchPreparedPhase SwitchPhase = 1
	// SwitchCommittedPhase: the new binding is the active one.
	SwitchCommittedPhase SwitchPhase = 2
)

// SwitchPrepared is the receipt of a prepared switch: the switch identity
// with the old and new binding digests, the drain it continues, the sealed
// admission generation and the issuing store epoch. On the wire it is the
// SwitchPrepared of the contract, and SwitchCommitted repeats exactly this
// identity.
type SwitchPrepared struct {
	SwitchID            [16]byte
	OldDigest           [32]byte
	NewDigest           [32]byte
	DrainID             [16]byte
	AdmissionGeneration uint64
	IssuerEpoch         [16]byte
}

// Switch is the durable record of one destination switch.
type Switch struct {
	Phase SwitchPhase
	SwitchPrepared
	// OldGeneration is the binding generation the switch continues from.
	OldGeneration uint64
	// NewBinding is the content identity the switch activates at commit.
	NewBinding Binding
}

// CheckPrepareSwitch verifies a PrepareSwitch request against the durable
// state and changes nothing. It returns the stored receipt when a switch of
// the current seal is already open under the same intent (a retry), and an
// error for every refusal. The RPC boundary runs it before the online
// target check, so a durable refusal never depends on the target being
// available.
func (j *Journal) CheckPrepareSwitch(id BatchID, oldSeal Seal, desired Binding, switchID [16]byte) (*SwitchPrepared, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.checkPrepareLocked(id, oldSeal, desired, switchID)
}

// PrepareSwitch durably accepts a verified switch intent and returns its
// SwitchPrepared receipt. The caller verifies the desired target online
// before calling: a failed check must never produce a SwitchPrepared.
// Rules:
//
//   - The producer/run must hold a durable seal and the presented old seal
//     must equal it entirely (ErrDrainRequired without a seal,
//     ErrIdentityMismatch on any differing field).
//   - The seal must not be resumed yet: a switch runs between the seal and
//     the Resume (ErrDrainRequired).
//   - A retry of the same switch id returns the same receipt when the intent
//     is the same; another switch id over an unfinished switch is an
//     immutable conflict: a new switch never jumps an unfinished one. A
//     committed switch of an earlier seal is history and blocks nothing, so
//     every later drain cycle can switch again.
//   - The desired descriptor must differ from the active binding, continue
//     the same destination line and the same network, and be exactly the
//     next binding generation. A descriptor is a legal successor only when
//     the active binding identity is known.
func (j *Journal) PrepareSwitch(id BatchID, oldSeal Seal, desired Binding, switchID [16]byte) (SwitchPrepared, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	receipt, err := j.checkPrepareLocked(id, oldSeal, desired, switchID)
	if err != nil {
		return SwitchPrepared{}, err
	}
	if receipt != nil {
		return *receipt, nil
	}
	seal, _, err := j.readSeal(id)
	if err != nil {
		return SwitchPrepared{}, err
	}
	rec := Switch{
		Phase: SwitchPreparedPhase,
		SwitchPrepared: SwitchPrepared{SwitchID: switchID, OldDigest: j.bnd, NewDigest: desired.DescriptorDigest,
			DrainID: seal.DrainID, AdmissionGeneration: seal.AdmissionGeneration, IssuerEpoch: j.epoch},
		OldGeneration: j.gen,
		NewBinding:    desired,
	}
	if err := j.st.Put(cutoverKey(desired.Generation), encodeSwitch(rec)); err != nil {
		return SwitchPrepared{}, err
	}
	return rec.SwitchPrepared, nil
}

// checkPrepareLocked holds every rule of PrepareSwitch; the caller holds
// j.mu. A nil receipt with a nil error means the switch may be committed.
func (j *Journal) checkPrepareLocked(id BatchID, oldSeal Seal, desired Binding, switchID [16]byte) (*SwitchPrepared, error) {
	if switchID == ([16]byte{}) || desired.DescriptorDigest == ([32]byte{}) {
		return nil, ErrInvalidIntent
	}
	seal, sealed, err := j.readSeal(id)
	if err != nil {
		return nil, err
	}
	if !sealed {
		return nil, ErrDrainRequired // a switch continues a sealed generation
	}
	if !seal.sameAck(oldSeal) {
		return nil, ErrIdentityMismatch // the whole old seal must match
	}
	// A switch belongs to the seal it was prepared under. It is an open
	// transition while it is only prepared, or while its seal is still the
	// current one; a committed switch of an earlier seal is history and
	// never blocks the switch of a later drain cycle.
	cur, open, err := j.lastSwitch()
	if err != nil {
		return nil, err
	}
	if open && (cur.Phase == SwitchPreparedPhase || cur.DrainID == seal.DrainID) {
		if cur.SwitchID != switchID {
			return nil, ErrImmutableConflict // another switch of this seal is unfinished
		}
		if cur.NewDigest != desired.DescriptorDigest || oldSeal.DrainID != cur.DrainID ||
			oldSeal.AdmissionGeneration != cur.AdmissionGeneration || oldSeal.BindingDigest != cur.OldDigest {
			return nil, ErrImmutableConflict // the same id must carry the same intent
		}
		return &cur.SwitchPrepared, nil // the retry returns the same receipt
	}
	if resume, resumed, err := j.readResume(id); err != nil {
		return nil, err
	} else if resumed && resume.OldAdmissionGeneration == seal.AdmissionGeneration {
		return nil, ErrDrainRequired // this seal was already resumed; a switch needs a new drain
	}
	if desired.DescriptorDigest == j.bnd {
		return nil, ErrStateConflict // no switch is pending
	}
	if !j.hasBinding || desired.DestinationID != j.dstID ||
		desired.ChainID != j.chainID || desired.GenesisHash != j.genesis {
		return nil, ErrBindingMismatch // another destination line, another network, or an unknown active binding
	}
	if desired.Generation != j.gen+1 {
		return nil, ErrBindingGeneration // the generation jump is forbidden
	}
	return nil, nil
}

// CheckCommitSwitch verifies a CommitSwitch request against the durable
// switch and changes nothing. done reports that this exact switch is already
// committed, so its receipt is durable whatever the target is now. The RPC
// boundary runs it before the online target check.
func (j *Journal) CheckCommitSwitch(switchID [16]byte, prepared SwitchPrepared, committedDigest [32]byte, committedGeneration uint64) (done bool, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.checkCommitLocked(switchID, prepared, committedDigest, committedGeneration)
}

// CommitSwitch durably activates a prepared switch and returns the same
// identity the SwitchPrepared carried. Rules:
//
//   - An already committed switch answers its durable receipt to the same
//     switch id and the same exact SwitchPrepared, whatever the target
//     availability is; it never contacts the target.
//   - A first commit needs the prepared receipt of this switch id; the
//     presented SwitchPrepared must equal it exactly and the producer's
//     binding commit assertion must name the new digest and generation.
//   - One atomic commit marks the switch committed and makes the new
//     binding the active one (meta/destination_binding); sessions and
//     batches of the old binding digest are no longer admitted.
func (j *Journal) CommitSwitch(switchID [16]byte, prepared SwitchPrepared, committedDigest [32]byte, committedGeneration uint64) (SwitchPrepared, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	done, err := j.checkCommitLocked(switchID, prepared, committedDigest, committedGeneration)
	if err != nil {
		return SwitchPrepared{}, err
	}
	cur, open, err := j.lastSwitch()
	if err != nil {
		return SwitchPrepared{}, err
	}
	if !open {
		return SwitchPrepared{}, ErrStateConflict // nothing changed since the check that admitted the commit
	}
	if done {
		return cur.SwitchPrepared, nil // the durable receipt stands, whatever the target is now
	}

	rec := cur
	rec.Phase = SwitchCommittedPhase
	b := storage.NewBatch()
	b.Put(cutoverKey(rec.NewBinding.Generation), encodeSwitch(rec))
	b.Put([]byte(keyBinding), encodeBinding(rec.NewBinding))
	if err := j.st.Apply(b); err != nil {
		return SwitchPrepared{}, err
	}
	j.applyBinding(rec.NewBinding)
	return rec.SwitchPrepared, nil
}

// checkCommitLocked holds every rule of CommitSwitch; the caller holds j.mu.
func (j *Journal) checkCommitLocked(switchID [16]byte, prepared SwitchPrepared, committedDigest [32]byte, committedGeneration uint64) (bool, error) {
	cur, open, err := j.lastSwitch()
	if err != nil {
		return false, err
	}
	if !open {
		return false, ErrStateConflict // commit needs its PREPARED receipt
	}
	if cur.SwitchID != switchID {
		return false, ErrImmutableConflict // another switch is prepared or committed
	}
	if cur.SwitchPrepared != prepared {
		return false, ErrImmutableConflict // the exact SwitchPrepared is required
	}
	if cur.Phase == SwitchCommittedPhase {
		return true, nil
	}
	if committedDigest != cur.NewDigest || committedGeneration != cur.NewBinding.Generation {
		return false, ErrIdentityMismatch // the producer must have committed exactly this binding
	}
	return false, nil
}

// CurrentSwitch returns the latest durable switch record of the store; the
// switch of the highest binding generation is the current one.
func (j *Journal) CurrentSwitch() (sw Switch, ok bool, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lastSwitch()
}

// lastSwitch is the current switch record; the caller holds j.mu.
func (j *Journal) lastSwitch() (sw Switch, ok bool, err error) {
	lo, hi := keyRange([]byte(keyCutover))
	err = j.st.Iterate(lo, hi, func(_, v []byte) error {
		rec, derr := decodeSwitch(v)
		if derr != nil {
			return derr
		}
		sw, ok = rec, true // ascending generation: the last record is the current one
		return nil
	})
	return sw, ok, err
}

// ResumeIntent is the producer's intent to open the next admission
// generation after a seal, under the active binding.
type ResumeIntent struct {
	ResumeID                [16]byte
	OldSeal                 Seal
	BindingDigest           [32]byte
	NextAdmissionGeneration uint64
}

// ResumeAck is the durable answer of a Resume: the resume identity with the
// old and new admission generations and the drain it continues.
type ResumeAck struct {
	ResumeID               [16]byte
	BindingDigest          [32]byte
	OldAdmissionGeneration uint64
	NewAdmissionGeneration uint64
	DrainID                [16]byte
	IssuerEpoch            [16]byte
}

// Resume accepts exactly the next admission generation of a sealed
// producer/run and returns the ResumeAck. Rules:
//
//   - The run must hold a durable seal (ErrDrainRequired) and the presented
//     old seal must equal it entirely (ErrIdentityMismatch).
//   - A repeated resume id returns the saved ack when the intent is the
//     same; the same id with another intent or another resume id is an
//     immutable conflict: a retired or conflicting intent is rejected.
//   - The binding digest must be the active binding and the next admission
//     generation exactly one above the sealed one (ErrGenerationConflict).
//
// Capture stays blocked until the producer's own LIVE commit, which happens
// after this answer.
func (j *Journal) Resume(id BatchID, intent ResumeIntent) (ResumeAck, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if intent.ResumeID == ([16]byte{}) || intent.NextAdmissionGeneration == 0 {
		return ResumeAck{}, ErrInvalidIntent
	}
	if saved, ok, err := j.readResume(id); err != nil {
		return ResumeAck{}, err
	} else if ok {
		if saved.ResumeID == intent.ResumeID {
			if saved.BindingDigest == intent.BindingDigest &&
				saved.OldAdmissionGeneration == intent.OldSeal.AdmissionGeneration &&
				saved.NewAdmissionGeneration == intent.NextAdmissionGeneration &&
				saved.DrainID == intent.OldSeal.DrainID {
				return saved, nil // the repeated intent returns the same result
			}
			return ResumeAck{}, ErrImmutableConflict
		}
		return ResumeAck{}, ErrImmutableConflict // another resume is already accepted
	}
	seal, sealed, err := j.readSeal(id)
	if err != nil {
		return ResumeAck{}, err
	}
	if !sealed {
		return ResumeAck{}, ErrDrainRequired // resume needs a sealed generation
	}
	if !seal.sameAck(intent.OldSeal) {
		return ResumeAck{}, ErrIdentityMismatch
	}
	if intent.BindingDigest != j.bnd {
		return ResumeAck{}, ErrBindingMismatch
	}
	if intent.NextAdmissionGeneration != seal.AdmissionGeneration+1 {
		return ResumeAck{}, ErrGenerationConflict // exactly the next generation
	}

	ack := ResumeAck{
		ResumeID:               intent.ResumeID,
		BindingDigest:          intent.BindingDigest,
		OldAdmissionGeneration: seal.AdmissionGeneration,
		NewAdmissionGeneration: intent.NextAdmissionGeneration,
		DrainID:                seal.DrainID,
		IssuerEpoch:            j.epoch,
	}
	if err := j.st.Put(resumeKey(id), encodeResume(ack)); err != nil {
		return ResumeAck{}, err
	}
	return ack, nil
}

// ResumedGeneration returns the durable ResumeAck of one producer/run.
func (j *Journal) ResumedGeneration(id BatchID) (ack ResumeAck, ok bool, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.readResume(id)
}

func sealedKey(id BatchID) []byte {
	k := append([]byte(keySealed), id.ProducerID[:]...)
	return append(k, id.RunID[:]...)
}

func resumeKey(id BatchID) []byte {
	k := append([]byte(keyResumed), id.ProducerID[:]...)
	return append(k, id.RunID[:]...)
}

func cutoverKey(generation uint64) []byte {
	return append([]byte(keyCutover), u64be(generation)...)
}

func (j *Journal) readSeal(id BatchID) (Seal, bool, error) {
	raw, err := j.st.Get(sealedKey(id))
	if err == storage.ErrNotFound {
		return Seal{}, false, nil
	}
	if err != nil {
		return Seal{}, false, err
	}
	seal, err := decodeSeal(raw)
	return seal, err == nil, err
}

func (j *Journal) readResume(id BatchID) (ResumeAck, bool, error) {
	raw, err := j.st.Get(resumeKey(id))
	if err == storage.ErrNotFound {
		return ResumeAck{}, false, nil
	}
	if err != nil {
		return ResumeAck{}, false, err
	}
	ack, err := decodeResume(raw)
	return ack, err == nil, err
}

// Binding record: destination id, generation, network and descriptor digest.
func encodeBinding(v Binding) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.raw(v.DestinationID[:])
	e.u64(v.Generation)
	e.raw(v.ChainID[:])
	e.raw(v.GenesisHash[:])
	e.raw(v.DescriptorDigest[:])
	return e.b
}

func decodeBinding(b []byte) (Binding, error) {
	d := newDecoder(b)
	v := Binding{}
	copy(v.DestinationID[:], d.raw(16))
	v.Generation = d.u64()
	copy(v.ChainID[:], d.raw(32))
	copy(v.GenesisHash[:], d.raw(32))
	copy(v.DescriptorDigest[:], d.raw(32))
	if err := d.done(); err != nil {
		return Binding{}, err
	}
	return v, nil
}

// Seal record: drain id, binding digest, admission generation, highwater,
// issuer epoch and the binding generation the seal was given for.
func encodeSeal(v Seal) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.raw(v.DrainID[:])
	e.raw(v.BindingDigest[:])
	e.u64(v.AdmissionGeneration)
	e.u64(v.Highwater)
	e.raw(v.IssuerEpoch[:])
	e.u64(v.BindingGeneration)
	return e.b
}

func decodeSeal(b []byte) (Seal, error) {
	d := newDecoder(b)
	v := Seal{}
	copy(v.DrainID[:], d.raw(16))
	copy(v.BindingDigest[:], d.raw(32))
	v.AdmissionGeneration = d.u64()
	v.Highwater = d.u64()
	copy(v.IssuerEpoch[:], d.raw(16))
	v.BindingGeneration = d.u64()
	if err := d.done(); err != nil {
		return Seal{}, err
	}
	return v, nil
}

// Switch record: phase byte, the SwitchPrepared receipt, the old binding
// generation and the new binding content.
func encodeSwitch(v Switch) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.u8(uint8(v.Phase))
	e.raw(v.SwitchID[:])
	e.raw(v.OldDigest[:])
	e.raw(v.NewDigest[:])
	e.raw(v.DrainID[:])
	e.u64(v.AdmissionGeneration)
	e.raw(v.IssuerEpoch[:])
	e.u64(v.OldGeneration)
	e.raw(encodeBinding(v.NewBinding)[1:]) // the version byte is the record's own
	return e.b
}

func decodeSwitch(b []byte) (Switch, error) {
	d := newDecoder(b)
	v := Switch{}
	v.Phase = SwitchPhase(d.u8())
	copy(v.SwitchID[:], d.raw(16))
	copy(v.OldDigest[:], d.raw(32))
	copy(v.NewDigest[:], d.raw(32))
	copy(v.DrainID[:], d.raw(16))
	v.AdmissionGeneration = d.u64()
	copy(v.IssuerEpoch[:], d.raw(16))
	v.OldGeneration = d.u64()
	if d.err != nil {
		return Switch{}, d.err
	}
	if v.Phase != SwitchPreparedPhase && v.Phase != SwitchCommittedPhase {
		return Switch{}, errCorruptRecord
	}
	nb, err := decodeBinding(append([]byte{recordVersion}, d.b...))
	if err != nil {
		return Switch{}, err
	}
	v.NewBinding = nb
	return v, nil
}

// Resume record: the ResumeAck.
func encodeResume(v ResumeAck) []byte {
	e := &encoder{}
	e.u8(recordVersion)
	e.raw(v.ResumeID[:])
	e.raw(v.BindingDigest[:])
	e.u64(v.OldAdmissionGeneration)
	e.u64(v.NewAdmissionGeneration)
	e.raw(v.DrainID[:])
	e.raw(v.IssuerEpoch[:])
	return e.b
}

func decodeResume(b []byte) (ResumeAck, error) {
	d := newDecoder(b)
	v := ResumeAck{}
	copy(v.ResumeID[:], d.raw(16))
	copy(v.BindingDigest[:], d.raw(32))
	v.OldAdmissionGeneration = d.u64()
	v.NewAdmissionGeneration = d.u64()
	copy(v.DrainID[:], d.raw(16))
	copy(v.IssuerEpoch[:], d.raw(16))
	if err := d.done(); err != nil {
		return ResumeAck{}, err
	}
	return v, nil
}
