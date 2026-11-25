// Package session is the E03-02.fencing output: session, admission and
// publisher fencing for the gateway (DELIVERY_STATE_MACHINE, "Fencing").
//
// A FencingToken binds one authenticated session to the epoch that admitted
// it: the admission generation for capture sessions, the connection
// generation for RPC sessions. A Barrier invalidates every token of an
// older epoch. Barriers rise on SealGeneration, Resume and Release; a stale
// RPC or callback then cannot create state after the barrier.
//
// The Gate is the single serialized ordering point: session switches
// (Raise, Revoke) and the mutations they guard (Mutate) share one mutex, so
// the token check happens together with the commit, not only when the RPC
// is read. A mutation that passed validation completes before any later
// barrier takes effect; after a barrier returns, every mutation holding an
// older token fails without running its effect.
//
// The PublisherGate fences the internal publisher worker by
// publisher_generation: PubAck callbacks are bound to batch_id, plan digest
// and generation. Raising the generation or releasing a batch puts a
// barrier: a late callback of an older generation, or any callback for a
// released batch, is rejected and cannot resurrect state. A duplicate
// delivery of a still-valid callback stays allowed: already-sent network
// bytes cannot be recalled, and the journal answers duplicates
// idempotently.
//
// The package performs no I/O and keeps no durable state: boot_nonce and
// session lifetimes are per process by contract, so a restart fences every
// old token implicitly.
package session

import (
	"crypto/rand"
	"errors"
	"sync"
)

// Errors of the fencing boundary; the ingest RPC layer maps them onto
// SESSION_STALE and GENERATION_CONFLICT of the wire error registry.
var (
	// ErrSessionStale is an unknown, revoked or barrier-invalidated token.
	ErrSessionStale = errors.New("session: stale session token")
	// ErrGenerationConflict is a session opened below the current barrier.
	ErrGenerationConflict = errors.New("session: generation conflict")
	// ErrEpochInvalid is a zero epoch; counters start at 1.
	ErrEpochInvalid = errors.New("session: epoch must be at least 1")
	// ErrCallbackStale is a publisher callback of an older generation.
	ErrCallbackStale = errors.New("session: stale publisher callback")
	// ErrBatchReleased is a callback for a batch whose release began.
	ErrBatchReleased = errors.New("session: batch already released")
)

// Token identifies one session: the epoch that admitted it (admission or
// connection generation) and an unpredictable per-session id
// (CONTROL_WIRE_CONTRACT: session identities are generated with crypto RNG
// and are not logged).
type Token struct {
	Epoch     uint64
	SessionID [16]byte
}

// Barrier invalidates every token of an older epoch.
type Barrier struct {
	epoch uint64
}

// Epoch returns the barrier's epoch.
func (b Barrier) Epoch() uint64 { return b.epoch }

// Invalidates reports whether the barrier fences the token: true for every
// token whose epoch is older than the barrier epoch.
func (b Barrier) Invalidates(t Token) bool { return t.Epoch < b.epoch }

// Gate is the serialized session registry and admission barrier of one
// gateway process.
type Gate struct {
	mu       sync.Mutex
	barrier  uint64
	sessions map[[16]byte]uint64 // session id -> epoch
}

// NewGate returns a gate with no sessions and an empty barrier.
func NewGate() *Gate {
	return &Gate{sessions: make(map[[16]byte]uint64)}
}

// Open admits one session at the given epoch and returns its token. An
// epoch below the current barrier is a generation conflict: an old seal
// cannot open new work. The session id is unpredictable.
func (g *Gate) Open(epoch uint64) (Token, error) {
	if epoch == 0 {
		return Token{}, ErrEpochInvalid
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if epoch < g.barrier {
		return Token{}, ErrGenerationConflict
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Token{}, err
	}
	g.sessions[id] = epoch
	return Token{Epoch: epoch, SessionID: id}, nil
}

// Raise puts a barrier at epoch and invalidates every token of an older
// epoch. Raising is monotonic: a lower epoch never lowers the barrier.
// SealGeneration, Resume and Release raise barriers through this call.
func (g *Gate) Raise(epoch uint64) Barrier {
	g.mu.Lock()
	defer g.mu.Unlock()
	if epoch > g.barrier {
		g.barrier = epoch
	}
	return Barrier{epoch: g.barrier}
}

// Barrier returns the current barrier.
func (g *Gate) Barrier() Barrier {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Barrier{epoch: g.barrier}
}

// Revoke closes one session (a session switch); its token becomes stale.
// Revoking an unknown session is a no-op.
func (g *Gate) Revoke(sessionID [16]byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.sessions, sessionID)
}

// Check validates a token against the registry and the current barrier.
func (g *Gate) Check(t Token) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.checkLocked(t)
}

func (g *Gate) checkLocked(t Token) error {
	epoch, ok := g.sessions[t.SessionID]
	if !ok || epoch != t.Epoch || t.Epoch < g.barrier {
		return ErrSessionStale
	}
	return nil
}

// Mutate validates the token and runs fn under the gate's serialized
// ordering, so the check happens together with the commit the mutation
// performs: a barrier raised after Mutate returned cannot undo fn, and a
// barrier raised before Mutate starts prevents fn from running at all.
// fn must not call back into the Gate (it would deadlock).
func (g *Gate) Mutate(t Token, fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.checkLocked(t); err != nil {
		return err
	}
	return fn()
}

// MutateRaise is Mutate for the barrier steps (SealGeneration, Resume): it
// validates the token, runs fn under the gate's serialized ordering and, only
// when fn succeeded, raises the barrier to epoch before the mutex is
// released. The commit of fn and the barrier are therefore one step: no RPC
// of an older epoch can commit between them, and a failed fn leaves the
// barrier where it was. The barrier never lowers. fn must not call back into
// the Gate (it would deadlock).
func (g *Gate) MutateRaise(t Token, epoch uint64, fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.checkLocked(t); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	if epoch > g.barrier {
		g.barrier = epoch
	}
	return nil
}

// maxReleasedBatches bounds the per-batch release barriers a PublisherGate
// remembers: the most recent releases. A gateway that runs for a long time
// releases one batch after another, so the set has to be bounded.
const maxReleasedBatches = 4096

// PublisherGate fences the internal publisher worker and its PubAck
// callbacks by publisher_generation and by released batch.
type PublisherGate struct {
	mu         sync.Mutex
	generation uint64
	released   map[[40]byte]struct{}
	// releaseOrder is the ring of the remembered releases, filled in order,
	// releaseNext the slot of the next one and releaseCount how many are
	// remembered. Once the ring is full, the slot of the next release holds
	// the oldest remembered one, which is forgotten when it is reused. The
	// ring is allocated with the first release.
	releaseOrder [][40]byte
	releaseNext  int
	releaseCount int
}

// NewPublisherGate returns a publisher gate at generation 1 with nothing
// released.
func NewPublisherGate() *PublisherGate {
	return &PublisherGate{generation: 1, released: make(map[[40]byte]struct{})}
}

// Callback identifies one PubAck callback: the batch_id bytes, the plan
// digest and the publisher generation that sent the request.
type Callback struct {
	Batch      [40]byte
	PlanDigest [32]byte
	Generation uint64
}

// Generation returns the current publisher generation.
func (p *PublisherGate) Generation() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.generation
}

// NewGeneration raises the publisher generation: every callback of an older
// generation becomes stale. SealGeneration and Release put this barrier.
func (p *PublisherGate) NewGeneration() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.generation++
	return p.generation
}

// Release puts a per-batch barrier: the ordered release of one batch began,
// so later callbacks for it cannot resurrect state.
//
// The gate remembers the maxReleasedBatches most recent releases and
// forgets the oldest one beyond that. Forgetting is safe: a batch that many
// releases behind is long retired, its records left with the retire commit,
// and the journal never creates a record for a callback of a batch it does
// not hold. A repeated Release of one batch puts the barrier once.
func (p *PublisherGate) Release(batch [40]byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, done := p.released[batch]; done {
		return
	}
	if p.releaseOrder == nil {
		p.releaseOrder = make([][40]byte, maxReleasedBatches)
	}
	if p.releaseCount == maxReleasedBatches {
		delete(p.released, p.releaseOrder[p.releaseNext]) // the oldest remembered release
	} else {
		p.releaseCount++
	}
	p.releaseOrder[p.releaseNext] = batch
	p.releaseNext = (p.releaseNext + 1) % maxReleasedBatches
	p.released[batch] = struct{}{}
}

// Apply validates a callback and runs fn under the gate's serialized
// ordering. Stale generations and released batches are rejected before fn
// runs; a duplicate of a still-valid callback runs fn and lets the journal
// answer it idempotently. fn must not call back into the gate.
func (p *PublisherGate) Apply(cb Callback, fn func() error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cb.Generation < p.generation {
		return ErrCallbackStale
	}
	if _, done := p.released[cb.Batch]; done {
		return ErrBatchReleased
	}
	return fn()
}
