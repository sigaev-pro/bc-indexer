// The retained state a completed drain keeps: source pins that wait for a
// future finalized GC anchor, and the saved lifecycle activations whose
// later events keep referencing the delivery that ACKed them (D59). A
// drain closes obligations; it never releases pins, never rewrites an
// ACKed reference, and never copies ACKed history to a new destination.
package maintenance

import (
	"fmt"
	"sort"
	"sync"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// Pin is one retained source pin of a producer run: the pinned native
// state and inputs a future finalized GC anchor may still need. Pins
// survive a drain; only the GC decision of the anchor owner releases
// them (E05-01.activation_gc), so this package offers no drain path that
// touches them.
type Pin struct {
	ID     string
	Reason string
}

// PinLedger is the in-memory pin store: the retained-pin half of the
// recovery state, guarded by a mutex.
type PinLedger struct {
	mu    sync.Mutex
	pins  map[string]Pin
	order []string
}

// NewPinLedger returns an empty ledger.
func NewPinLedger() *PinLedger { return &PinLedger{pins: make(map[string]Pin)} }

// Retain adds one retained pin. A repeated retain of the same id and
// content is idempotent; a different pin under the same id is refused.
func (l *PinLedger) Retain(p Pin) error {
	if p.ID == "" {
		return fmt.Errorf("%w: pin id is empty", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if old, ok := l.pins[p.ID]; ok {
		if old == p {
			return nil
		}
		return fmt.Errorf("%w: pin %q is retained for %q, not %q", ErrImmutable, p.ID, old.Reason, p.Reason)
	}
	l.pins[p.ID] = p
	l.order = append(l.order, p.ID)
	return nil
}

// Pins returns the retained pins in retention order.
func (l *PinLedger) Pins() []Pin {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Pin, 0, len(l.order))
	for _, id := range l.order {
		out = append(out, l.pins[id])
	}
	return out
}

// Release removes one pin. It is the GC owner's step after an accepted
// anchor; a drain never calls it, which is exactly why retained pins do
// not block a completed drain.
func (l *PinLedger) Release(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.pins[id]; !ok {
		return fmt.Errorf("%w: pin %q is not retained", ErrNotFound, id)
	}
	delete(l.pins, id)
	for i, held := range l.order {
		if held == id {
			l.order = append(l.order[:i], l.order[i+1:]...)
			break
		}
	}
	return nil
}

// EventKind is a later public lifecycle event of a saved activation.
type EventKind string

// The later event kinds of a saved activation.
const (
	// EventRemoved is the canonical removal of a previously applied
	// inclusion (a reorg).
	EventRemoved EventKind = "REMOVED"

	// EventStatus is a status change without a new block.
	EventStatus EventKind = "STATUS"
)

// Reference points at the delivery that ACKed one public lifecycle event:
// the binding digest of the destination that acknowledged it and the
// retired batch that carried it. D59: a later Removed or Status of the
// same saved activation keeps this reference even after a cutover; the
// ACKed history itself is never copied to the new destination.
type Reference struct {
	Binding proto.Hash32
	Batch   uint64
}

// Activation is one saved lifecycle activation with the reference of its
// ACKed Applied event. The durable registry of E05-01.registry owns the
// full activation records; this is the retained surface a cutover and a
// drain must preserve.
type Activation struct {
	ID      string
	Applied Reference
}

// PlannedEvent is where one new lifecycle event goes: the current binding
// of the producer, with the retained reference of the earlier ACKed
// Applied traveling unchanged.
type PlannedEvent struct {
	Activation string
	Kind       EventKind
	Sequence   uint64
	// Target is the binding digest the new event is delivered under: the
	// producer's committed binding after a cutover, which may be the old
	// one when the switch was skipped.
	Target proto.Hash32
	// Prior is the retained ACKed Applied reference of the activation.
	Prior Reference
}

// Lifecycles is the in-memory store of saved activations with their ACKed
// Applied references.
type Lifecycles struct {
	mu   sync.Mutex
	byID map[string]Activation
}

// NewLifecycles returns an empty store.
func NewLifecycles() *Lifecycles { return &Lifecycles{byID: make(map[string]Activation)} }

// Retain saves one activation. A repeated retain of the same id and
// reference is idempotent; a different reference under the same id is
// refused, because an ACKed delivery is never rewritten.
func (l *Lifecycles) Retain(a Activation) error {
	if a.ID == "" {
		return fmt.Errorf("%w: activation id is empty", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if old, ok := l.byID[a.ID]; ok {
		if old == a {
			return nil
		}
		return fmt.Errorf("%w: activation %q keeps its ACKed Applied reference", ErrImmutable, a.ID)
	}
	l.byID[a.ID] = a
	return nil
}

// Get returns one saved activation.
func (l *Lifecycles) Get(id string) (Activation, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.byID[id]
	return a, ok
}

// IDs returns the saved activation ids in sorted order.
func (l *Lifecycles) IDs() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.byID))
	for id := range l.byID {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// PlanEvent plans one later lifecycle event of a saved activation under
// the producer's current committed binding. The event goes only to
// target; the retained ACKed Applied reference travels with it unchanged,
// and no event is planned for the old binding — the ACKed history is not
// copied to the new destination (AC-DLV-12-01, D59).
func (l *Lifecycles) PlanEvent(target proto.Hash32, activation string, kind EventKind, sequence uint64) (PlannedEvent, error) {
	if target == (proto.Hash32{}) {
		return PlannedEvent{}, fmt.Errorf("%w: event target binding is zero", ErrInvalid)
	}
	if kind != EventRemoved && kind != EventStatus {
		return PlannedEvent{}, fmt.Errorf("%w: event kind %q", ErrInvalid, kind)
	}
	if sequence == 0 {
		return PlannedEvent{}, fmt.Errorf("%w: event sequence is zero", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.byID[activation]
	if !ok {
		return PlannedEvent{}, fmt.Errorf("%w: activation %q is not saved", ErrNotFound, activation)
	}
	return PlannedEvent{Activation: activation, Kind: kind, Sequence: sequence,
		Target: target, Prior: a.Applied}, nil
}
