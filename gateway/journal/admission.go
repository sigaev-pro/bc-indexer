package journal

// Data admission of a producer/run: which admission generation may run the
// Prepare and Publish RPCs.
//
// A run admits data in the generation it is live in. A seal
// (SealGeneration) closes admission for the whole run: the drained frontier
// is exhausted and no batch may be created until a Resume opens exactly the
// next generation. A destination switch that is prepared and not committed
// closes admission for every run of the store: the active binding is about
// to change, and a batch created meanwhile would be bound to a binding that
// is no longer active once the switch commits. The publisher refuses such a
// batch, so it could never be acknowledged or released, and it would hold
// the next seal.

// CheckDataAdmission reports whether a data RPC, a Prepare or Publish step
// of a session at one admission generation, may run for one producer/run.
// It reads the durable state and changes nothing. Rules:
//
//   - A run without a durable seal admits data in every generation.
//   - A sealed run admits data only in the generation its Resume opened.
//     Every other generation stays closed: the sealed one and any later one
//     that no Resume opened alike.
//   - While a destination switch is prepared and not committed, no run of
//     the store admits data.
func (j *Journal) CheckDataAdmission(id BatchID, admission uint64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.checkDataAdmissionLocked(id, admission)
}

// checkDataAdmissionLocked is CheckDataAdmission; the caller holds j.mu.
func (j *Journal) checkDataAdmissionLocked(id BatchID, admission uint64) error {
	if j.swOpen && j.sw.Phase == SwitchPreparedPhase {
		return ErrAdmissionClosed
	}
	_, sealed, err := j.readSeal(id)
	if err != nil {
		return err
	}
	if !sealed {
		return nil
	}
	resume, resumed, err := j.readResume(id)
	if err != nil {
		return err
	}
	if resumed && resume.Ack.NewAdmissionGeneration == admission {
		return nil
	}
	return ErrAdmissionClosed
}
