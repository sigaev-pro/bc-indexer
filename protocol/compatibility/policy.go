package compatibility

// DurableState is the minimal durable input of a preflight decision: the
// encoding version the persisted records carry, plus the drain facts. It
// is a read-only snapshot; no decision in this package mutates it.
type DurableState struct {
	// Version is the encoding version carried by the durable records.
	Version VersionID
	// Mode is the producer mode at the moment of the decision.
	Mode Mode
	// Pending is the count of obligations that are not yet retired.
	Pending int
	// MaxDelivery is the highest delivery state any pending obligation
	// has reached; StateNone when Pending is zero.
	MaxDelivery DeliveryState
}

// snapshot projects the drain facts of the state.
func (d DurableState) snapshot() DrainSnapshot {
	return DrainSnapshot{Mode: d.Mode, Pending: d.Pending, MaxDelivery: d.MaxDelivery}
}

// Policy composes the capability matrix and the drain guard into the
// preflight performed before any writable mutation
// (DELIVERY_STATE_MACHINE: read/replay/new-write capabilities are checked
// before a writable mutation). It holds no state of its own.
type Policy struct {
	matrix *Matrix
	guard  DrainGuard
}

// NewPolicy builds a policy over m. The matrix must come from NewMatrix
// and must not be nil.
func NewPolicy(m *Matrix) *Policy {
	return &Policy{matrix: m, guard: DrainGuard{}}
}

// Matrix returns the capability matrix the policy consults.
func (p *Policy) Matrix() *Matrix { return p.matrix }

// ContinuePending decides whether the running build may continue the
// pending obligations carried by the durable state: the current version
// must be able to read and replay the durable version. A rejection means
// a hard stop that preserves the pending bytes and their version
// (CONTEXT "saved-state compatibility": a new version continues old
// obligations; it never rewrites them, and completing delivery elsewhere
// does not make an incompatible store compatible). Pending records keep
// their original version, enforced per record by RewritePending.
func (p *Policy) ContinuePending(d DurableState) error {
	return p.matrix.CanReplay(d.Version, p.matrix.Current())
}

// PreflightTransition decides whether new writes may target the target
// version while the durable store carries d.Version, under the transition
// policy of the claim family. Both rule families apply, in a fixed order:
//
//  1. The matrix decides capability: registration, the current-version
//     target rule, writability, and — for the store-format family
//     (ClaimStoreFormat) — the never-a-silent-migration rule. An
//     incompatible store transition is rejected here regardless of drain
//     state.
//  2. For a real transition (d.Version != target), the drain guard vetoes
//     the timing. A compatible pair follows the ordinary timing rules
//     (no pending obligations, no drain in progress). An incompatible
//     pair survives the matrix only in the wire/plan family
//     (ClaimWirePlan) and then requires mode DRAINED: the old build must
//     have finished and sealed every saved obligation before a build that
//     cannot read the old format starts writing.
//
// Writing new records in the store's own current version (d.Version ==
// target) is not a transition: the guard does not apply, so a live build
// keeps capturing while earlier batches are still pending.
func (p *Policy) PreflightTransition(d DurableState, target VersionID, claim ClaimType) error {
	if err := p.matrix.CanWriteNew(d.Version, target, claim); err != nil {
		return err
	}
	if d.Version == target {
		return nil
	}
	if p.matrix.CanRead(d.Version, target) == nil {
		return p.guard.AllowFormatTransition(d.snapshot())
	}
	// The pair is incompatible and the matrix did not reject it, so this
	// is a wire/plan upgrade: only a completed, sealed drain by the old
	// build opens it.
	return p.guard.AllowIncompatibleWireUpgrade(d.snapshot())
}
