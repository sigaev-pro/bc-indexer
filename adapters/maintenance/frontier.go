// The saved native obligation frontier of a drain: the ADMITTED,
// EXEC_READY and decision-marker obligations E05-02.reorg replays after a
// restart (NATIVE_CAPTURE_PROTOCOL, "Startup, known branch и lifecycle GC":
// local recovery runs ADMITTED/EXEC_READY/decision markers before any new
// admission). A drain may fix its drain point and seal only after no saved
// obligation can still create a delivery batch.
package maintenance

import (
	"fmt"
	"sort"
	"sync"
)

// ObligationStage is how far one saved native obligation has progressed
// through the capture pipeline. The stages follow the recovery order of the
// startup rules: ADMITTED is the durable capture obligation, EXEC_READY is
// the closed execution result, DECIDED is the recorded native decision, and
// FINISHED means the obligation has fixed its whole delivery outcome — a
// batch closed, or a no-events decision checkpoint saved — and can no
// longer create a batch.
type ObligationStage uint8

// The obligation stages.
const (
	// ObligAdmitted is the durable admission boundary: the observation is
	// saved, its execution is not closed yet.
	ObligAdmitted ObligationStage = iota + 1

	// ObligExecReady is the durable execution result of the observation.
	ObligExecReady

	// ObligDecided is the recorded native decision. Its public events may
	// still form a batch, so the obligation still holds the pipeline.
	ObligDecided

	// ObligFinished is the closed delivery outcome: the decision's batch
	// was allocated and closed, or the no-events checkpoint was saved. The
	// obligation can no longer create a batch.
	ObligFinished
)

// String returns the stage name.
func (s ObligationStage) String() string {
	switch s {
	case ObligAdmitted:
		return "ADMITTED"
	case ObligExecReady:
		return "EXEC_READY"
	case ObligDecided:
		return "DECIDED"
	case ObligFinished:
		return "FINISHED"
	}
	return fmt.Sprintf("ObligationStage(%d)", uint8(s))
}

// Obligation is one saved native obligation of a producer run. Sequence is
// the native observation sequence, never a delivery batch sequence; Batch
// is the delivery batch the obligation allocated, zero while it has none.
// One native decision with public events has exactly one batch, so an
// obligation with an allocated batch cannot create another.
type Obligation struct {
	Sequence uint64
	Stage    ObligationStage
	Batch    uint64
}

// BatchCapable reports whether the obligation can still create a delivery
// batch: it is unfinished and has allocated none yet. A finished
// obligation, and one whose batch is already allocated, cannot.
func (o Obligation) BatchCapable() bool { return o.Stage != ObligFinished && o.Batch == 0 }

// Frontier is the native obligation seam the drain controller runs against.
// The real adapter keeps these records with the lifecycle registry of
// E05-01.registry and the decision mirror of E05-02.reorg; this interface
// is the exact surface a drain needs, so the controller stays independent
// of the registry's own module.
//
// All methods are safe for concurrent use; every mutating method is one
// atomic durable commit in the real store.
type Frontier interface {
	// Obligations returns the saved obligations in sequence order.
	Obligations() []Obligation

	// Advance moves one obligation one recovery step forward: to the next
	// stage, and, when the step allocates the delivery outcome, to its
	// batch sequence. Going backwards, skipping a stage, advancing a
	// missing sequence or changing an allocated batch is rejected; the
	// frontier is unchanged.
	Advance(sequence uint64, to ObligationStage, batch uint64) error
}

// MemoryFrontier is the in-memory Frontier store: a map guarded by a
// mutex, modeling the durable obligation records with atomic step
// transitions. It is the test double of the registry-backed frontier.
type MemoryFrontier struct {
	mu          sync.Mutex
	obligations map[uint64]Obligation
}

// NewMemoryFrontier returns an empty frontier.
func NewMemoryFrontier() *MemoryFrontier {
	return &MemoryFrontier{obligations: make(map[uint64]Obligation)}
}

// Obligations returns the saved obligations in sequence order.
func (m *MemoryFrontier) Obligations() []Obligation {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Obligation, 0, len(m.obligations))
	for _, o := range m.obligations {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out
}

// Save adds one saved obligation at its stage. It models the durable
// recovery input a restart finds: the admission, execution and decision
// markers the previous process left behind.
func (m *MemoryFrontier) Save(o Obligation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if o.Sequence == 0 {
		return fmt.Errorf("%w: obligation sequence is zero", ErrInvalid)
	}
	if o.Stage == 0 {
		return fmt.Errorf("%w: obligation %d has no stage", ErrInvalid, o.Sequence)
	}
	if _, dup := m.obligations[o.Sequence]; dup {
		return fmt.Errorf("%w: obligation %d is already saved", ErrInvalid, o.Sequence)
	}
	if o.Batch != 0 && o.Stage != ObligFinished {
		return fmt.Errorf("%w: obligation %d names batch %d before its outcome is closed",
			ErrInvalid, o.Sequence, o.Batch)
	}
	m.obligations[o.Sequence] = o
	return nil
}

// Advance moves one obligation exactly one stage forward.
func (m *MemoryFrontier) Advance(sequence uint64, to ObligationStage, batch uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.obligations[sequence]
	if !ok {
		return fmt.Errorf("%w: obligation %d is not saved", ErrNotFound, sequence)
	}
	if to != cur.Stage+1 {
		return fmt.Errorf("%w: obligation %d is %s, one step goes to %s not %s",
			ErrTransition, sequence, cur.Stage, cur.Stage+1, to)
	}
	if cur.Batch != 0 && batch != 0 && cur.Batch != batch {
		return fmt.Errorf("%w: obligation %d cannot change its batch %d to %d",
			ErrImmutable, sequence, cur.Batch, batch)
	}
	if batch != 0 && to != ObligFinished {
		return fmt.Errorf("%w: obligation %d allocates batch %d outside its closing step",
			ErrTransition, sequence, batch)
	}
	next := Obligation{Sequence: sequence, Stage: to, Batch: cur.Batch}
	if batch != 0 {
		next.Batch = batch
	}
	m.obligations[sequence] = next
	return nil
}
