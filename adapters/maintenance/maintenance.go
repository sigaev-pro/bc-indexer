// Package maintenance is E05-03.drain: the native RECOVERY_ONLY DRAIN mode
// of the adapter process. Started with --extractor-mode=drain on the same
// compatible build and store, the mode durably enters DRAIN before the
// recovery sync, finishes every saved obligation — native capture,
// lifecycle and delivery — up to the seal and batch highwater, admits no
// new live callbacks and starts no new polls, keeps retained future-GC
// pins untouched, and saves DRAINED with the seal ack
// (DELIVERY_STATE_MACHINE, "Drain и cutover D54/D57/D59"; NATIVE_CAPTURE
// PROTOCOL, "Startup, known branch и lifecycle GC": DRAIN stays
// RECOVERY_ONLY; LIVE_CONFIG_SPEC: a saved unfinished DRAIN resumes in
// DRAIN, an explicit live before its completion is rejected, and DRAINED
// returns to LIVE only through an explicit Resume of the next admission
// generation).
//
// The controller composes three seams and owns none of their machinery:
// the producer store of core/delivery carries the durable mode, drain
// point and seal ack; the Frontier seam is the saved obligation surface
// E05-02.reorg replays (the durable registry of E05-01.registry backs it
// in the real adapter); the Sealer seam is Engine.SealToDrained of
// core/delivery/maintenance, which fixes the drain point, seals the
// generation and saves DRAINED. What the controller owns is the mode
// barrier: no new admissions and no new polls while the drain runs, the
// seal forbidden until no batch-capable obligation remains, and capture
// staying closed after DRAINED until the Resume commit opens LIVE.
package maintenance

import (
	"context"
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// The named errors of the native drain mode. Every refusal carries one of
// these sentinels with a specific reason.
var (
	// ErrInvalid rejects a malformed argument.
	ErrInvalid = errors.New("maintenance: invalid argument")

	// ErrNotFound marks a read of a record the store does not hold.
	ErrNotFound = errors.New("maintenance: record not found")

	// ErrTransition rejects a step the durable mode does not allow.
	ErrTransition = errors.New("maintenance: mode does not allow the step")

	// ErrImmutable rejects different content under an id that already
	// carries durable content.
	ErrImmutable = errors.New("maintenance: immutable record conflict")

	// ErrFence rejects a step of the wrong session kind.
	ErrFence = errors.New("maintenance: session fence rejected the step")

	// ErrAdmissionClosed refuses a new live callback or a new poll while
	// the drain mode runs: a drain recovers saved work, it never admits
	// new work.
	ErrAdmissionClosed = errors.New("maintenance: drain admits no new work")

	// ErrSealBlocked refuses an early seal: saved obligations that can
	// still create a batch remain, so the drain point and highwater are
	// not yet fixed. An obligation whose native source is unavailable
	// stays batch-capable, which is why an unavailable source keeps the
	// drain in DRAIN and never yields DRAINED.
	ErrSealBlocked = errors.New("maintenance: seal waits for the saved obligations")
)

// ClientSlot names one adapter applicability slot of the drain mode: the
// native client and the network family whose mainnet and testnet profiles
// it covers. A PASS of one family never closes the others.
type ClientSlot struct {
	// Client is the native client name: geth, bsc or bor.
	Client string
	// Family is the closed profile family of the client.
	Family profiles.Family
}

// slots are the three applicability slots of E05-03.drain.
var slots = []ClientSlot{
	{Client: "geth", Family: profiles.FamilyEth},
	{Client: "bsc", Family: profiles.FamilyBsc},
	{Client: "bor", Family: profiles.FamilyBor},
}

// Slots returns the three applicability slots, in family order.
func Slots() []ClientSlot {
	out := make([]ClientSlot, len(slots))
	copy(out, slots)
	return out
}

// String returns "client/family" of the slot.
func (s ClientSlot) String() string {
	return fmt.Sprintf("%s/%s", s.Client, s.Family)
}

// validSlot reports whether the slot is one of the three known slots.
func validSlot(s ClientSlot) bool {
	for _, known := range slots {
		if known == s {
			return true
		}
	}
	return false
}

// StartupMode is the value of the named build parameter
// --extractor-mode=live|drain. It is a command-line project parameter,
// not a YAML key, and no other value exists (LIVE_CONFIG_SPEC).
type StartupMode string

// The startup mode values.
const (
	// FlagLive starts the process for live capture.
	FlagLive StartupMode = "live"

	// FlagDrain starts the process in RECOVERY_ONLY DRAIN.
	FlagDrain StartupMode = "drain"
)

// Action is what a process start does with the durable producer mode.
type Action uint8

// The startup actions.
const (
	// ActionLive runs live capture: a new store in LIVE, or a restart
	// that continues LIVE (or BLOCKED) work.
	ActionLive Action = iota + 1

	// ActionDrain runs or continues the RECOVERY_ONLY DRAIN: a fresh
	// drain start from LIVE, an unfinished drain resumed after a crash,
	// or a completed drain restarted with the drain flag, which finds
	// its saved seal and stays DRAINED.
	ActionDrain

	// ActionResume answers an explicit live start over a DRAINED store:
	// the Resume of the next admission generation must finish, with the
	// applicable switch, before any capture opens.
	ActionResume
)

// String returns the action name.
func (a Action) String() string {
	switch a {
	case ActionLive:
		return "LIVE"
	case ActionDrain:
		return "DRAIN"
	case ActionResume:
		return "RESUME"
	}
	return fmt.Sprintf("Action(%d)", uint8(a))
}

// Startup decides what a process start does, from the --extractor-mode
// flag and the durable producer mode. An unknown flag value is invalid
// input, never a silent default. An explicit live start over an
// unfinished DRAIN is rejected: the saved drain resumes in DRAIN first
// (LIVE_CONFIG_SPEC). An explicit live start over DRAINED is the Resume
// path, never an automatic return to capture.
func Startup(flag StartupMode, durable delivery.ProducerMode) (Action, error) {
	switch flag {
	case FlagLive:
		switch durable {
		case delivery.ModeLive, delivery.ModeBlocked:
			return ActionLive, nil
		case delivery.ModeDrain:
			return 0, fmt.Errorf("%w: explicit live while the saved drain is unfinished; restart with --extractor-mode=drain to finish it",
				ErrTransition)
		case delivery.ModeDrained:
			return ActionResume, nil
		}
		return 0, fmt.Errorf("%w: producer mode %s", ErrInvalid, durable)
	case FlagDrain:
		switch durable {
		case delivery.ModeLive, delivery.ModeDrain, delivery.ModeBlocked, delivery.ModeDrained:
			return ActionDrain, nil
		}
		return 0, fmt.Errorf("%w: producer mode %s", ErrInvalid, durable)
	}
	return 0, fmt.Errorf("%w: unknown --extractor-mode value %q", ErrInvalid, flag)
}

// CallbackKind names the native live input that reached the adapter.
type CallbackKind string

// The live callback kinds.
const (
	// CallbackNewBlock is a new canonical head candidate: the H+1 capture
	// input a drain never admits.
	CallbackNewBlock CallbackKind = "NEW_BLOCK"

	// CallbackFinality is a finality change without a new block.
	CallbackFinality CallbackKind = "FINALITY"
)

// LiveCallback is one native live input offered to the admission gate.
type LiveCallback struct {
	Kind     CallbackKind
	Sequence uint64
}

// PollKind names one optional poll a live adapter runs.
type PollKind string

// The poll kinds.
const (
	// PollGC is a GC anchor poll: looking for a new finalized anchor to
	// release retained pins. A drain never starts one; retained pins
	// simply wait.
	PollGC PollKind = "GC"

	// PollObserver is a supporting-source observer cycle, such as the
	// Polygon L1 checkpoint observer.
	PollObserver PollKind = "OBSERVER"
)

// PollTick is one optional poll offered to the gate.
type PollTick struct {
	Kind PollKind
}

// ProducerState is the producer-side surface the drain mode drives. The
// memory producer of core/delivery and the RocksDB producer of E03-01
// implement it through the ProducerSeam of core/delivery/maintenance.
type ProducerState interface {
	delivery.ProducerStore
	DrainPoint() (drainID proto.ID16, highwater uint64, ok bool)
	SealAckSaved() (delivery.SealAck, bool)
	ResumeIntentSaved() (delivery.ResumeIntent, bool)
	CommitLive(ack delivery.ResumeAck) error
}

// Sealer finishes the delivery half of a drain once the native
// obligations are exhausted: it fixes the drain point and batch
// highwater, seals the admission generation under the barrier, and saves
// DRAINED with the seal ack. Engine.SealToDrained of
// core/delivery/maintenance is the implementation.
type Sealer interface {
	SealToDrained(ctx context.Context, fence delivery.Fence, drainID proto.ID16) (delivery.SealAck, error)
}

// ResumeRecovery finishes a saved resume transition over a MAINTENANCE
// session: the exact saved intent returns its durable receipt.
// Engine.RecoverTransition of core/delivery/maintenance is the
// implementation.
type ResumeRecovery interface {
	RecoverTransition(fence delivery.Fence, switchID, resumeID proto.ID16) (delivery.SwitchCommitted, delivery.ResumeAck, error)
}

// Controller is the native drain mode of one adapter process: the mode
// barrier over one producer store, its saved obligation frontier and its
// retained pins. It holds no state of its own beyond the seams; every
// decision reads the durable mode, so a crash between steps recovers by
// running the same method again. A Controller is safe for concurrent use.
type Controller struct {
	slot     ClientSlot
	prod     ProducerState
	frontier Frontier
	pins     *PinLedger
}

// Open returns the controller of one applicability slot over the producer
// store, the saved obligation frontier and the retained pin ledger. The
// seams must be non-nil and the slot must be one of Slots().
func Open(slot ClientSlot, prod ProducerState, frontier Frontier, pins *PinLedger) (*Controller, error) {
	if !validSlot(slot) {
		return nil, fmt.Errorf("%w: unknown client slot %s", ErrInvalid, slot)
	}
	if prod == nil {
		return nil, fmt.Errorf("%w: producer store is nil", ErrInvalid)
	}
	if frontier == nil {
		return nil, fmt.Errorf("%w: obligation frontier is nil", ErrInvalid)
	}
	if pins == nil {
		return nil, fmt.Errorf("%w: pin ledger is nil", ErrInvalid)
	}
	return &Controller{slot: slot, prod: prod, frontier: frontier, pins: pins}, nil
}

// Slot returns the applicability slot of the controller.
func (c *Controller) Slot() ClientSlot { return c.slot }

// EnterDrain durably enters DRAIN before the recovery sync. A store
// already in DRAIN (a crash-resumed drain) is left as it is; a DRAINED
// store refuses: the drain is complete and the explicit Resume owns the
// way back. LIVE and BLOCKED stores enter DRAIN, which is the first
// durable step of the drain procedure.
func (c *Controller) EnterDrain() error {
	switch c.prod.Mode() {
	case delivery.ModeDrain:
		return nil
	case delivery.ModeDrained:
		return fmt.Errorf("%w: the drain is complete; an explicit Resume owns the return to live",
			ErrTransition)
	case delivery.ModeLive, delivery.ModeBlocked:
		return c.prod.SetMode(delivery.ModeDrain)
	}
	return fmt.Errorf("%w: producer mode %s", ErrInvalid, c.prod.Mode())
}

// Admit decides one native live callback against the durable mode. While
// the drain runs, and after it, new live callbacks create no obligations:
// the refusal is the DRAIN contract (no new H+1 capture, no new live
// admissions). Only LIVE admits, which is the ordinary capture path this
// same gate serves after a Resume.
func (c *Controller) Admit(cb LiveCallback) error {
	switch c.prod.Mode() {
	case delivery.ModeDrain:
		return fmt.Errorf("%w: %s callback %d while draining; only saved obligations run",
			ErrAdmissionClosed, cb.Kind, cb.Sequence)
	case delivery.ModeDrained:
		return fmt.Errorf("%w: %s callback %d after the drain; capture opens only through an explicit Resume",
			ErrAdmissionClosed, cb.Kind, cb.Sequence)
	case delivery.ModeLive:
		return nil
	}
	return fmt.Errorf("%w: producer mode %s", ErrTransition, c.prod.Mode())
}

// Poll decides one optional poll tick against the durable mode. A drain
// starts no new polls and never polls for a GC anchor: retained pins wait
// for their anchor without new capture, and only a saved GC decision
// recovered through the frontier runs. Only LIVE runs polls.
func (c *Controller) Poll(tick PollTick) error {
	switch c.prod.Mode() {
	case delivery.ModeDrain:
		return fmt.Errorf("%w: %s poll while draining; a drain starts no new polls",
			ErrAdmissionClosed, tick.Kind)
	case delivery.ModeDrained:
		return fmt.Errorf("%w: %s poll after the drain; retained pins wait without polling",
			ErrAdmissionClosed, tick.Kind)
	case delivery.ModeLive:
		return nil
	}
	return fmt.Errorf("%w: producer mode %s", ErrTransition, c.prod.Mode())
}

// Remaining returns the saved obligations that can still create a
// delivery batch, in sequence order.
func (c *Controller) Remaining() []Obligation {
	remaining := []Obligation{}
	for _, o := range c.frontier.Obligations() {
		if o.BatchCapable() {
			remaining = append(remaining, o)
		}
	}
	return remaining
}

// Exhausted reports whether no batch-capable obligation remains. Retained
// future-GC pins never count: they wait for a future finalized anchor
// without new capture and do not block the completed drain
// (AC-DLV-10-02).
func (c *Controller) Exhausted() bool { return len(c.Remaining()) == 0 }

// Seal finishes the drain: with the native obligations exhausted it
// delegates to the delivery sealer, which fixes the drain point and batch
// highwater, seals the admission generation and saves DRAINED with the
// seal ack. An early seal — any batch-capable obligation still saved — is
// refused with ErrSealBlocked, which is also how an obligation whose
// native source is unavailable keeps the drain in DRAIN: its delivery
// stays unfixed, so DRAINED is never reached over it. A repeated seal of
// the same drain id returns the saved ack, whichever crash point restarted
// the process; a different drain id over a saved seal is an immutable
// conflict.
func (c *Controller) Seal(ctx context.Context, sealer Sealer, fence delivery.Fence, drainID proto.ID16) (delivery.SealAck, error) {
	if sealer == nil {
		return delivery.SealAck{}, fmt.Errorf("%w: sealer is nil", ErrInvalid)
	}
	if drainID.IsZero() {
		return delivery.SealAck{}, fmt.Errorf("%w: drain id is zero", ErrInvalid)
	}
	if ack, ok := c.prod.SealAckSaved(); ok {
		if ack.DrainID == drainID {
			return ack, nil
		}
		return delivery.SealAck{}, fmt.Errorf("%w: the saved seal binds drain %v, not %v",
			ErrImmutable, ack.DrainID, drainID)
	}
	if remaining := c.Remaining(); len(remaining) > 0 {
		return delivery.SealAck{}, fmt.Errorf("%w: %d saved obligation(s) can still create a batch, first at sequence %d; the highwater is fixed only after them",
			ErrSealBlocked, len(remaining), remaining[0].Sequence)
	}
	return sealer.SealToDrained(ctx, fence, drainID)
}

// FinishResume completes a saved resume intent after a lost ResumeAck:
// over the exact MAINTENANCE intent the gateway's durable receipt comes
// back — the same ack, not a new generation — and the producer commits
// LIVE with it. Capture opens at that commit and not before: the admission
// gate stays closed while the store is DRAINED (AC-DLV-11-03).
func (c *Controller) FinishResume(rec ResumeRecovery, fence delivery.Fence, resumeID proto.ID16) (delivery.ResumeAck, error) {
	if rec == nil {
		return delivery.ResumeAck{}, fmt.Errorf("%w: resume recovery is nil", ErrInvalid)
	}
	if resumeID.IsZero() {
		return delivery.ResumeAck{}, fmt.Errorf("%w: resume id is zero", ErrInvalid)
	}
	if fence.Kind != delivery.SessionMaintenance {
		return delivery.ResumeAck{}, fmt.Errorf("%w: finishing a saved resume needs the MAINTENANCE session",
			ErrFence)
	}
	if c.prod.Mode() != delivery.ModeDrained {
		return delivery.ResumeAck{}, fmt.Errorf("%w: resume continues a sealed drain, store is %s",
			ErrTransition, c.prod.Mode())
	}
	intent, ok := c.prod.ResumeIntentSaved()
	if !ok || intent.ResumeID != resumeID {
		return delivery.ResumeAck{}, fmt.Errorf("%w: resume %v has no saved intent", ErrNotFound, resumeID)
	}
	_, ack, err := rec.RecoverTransition(fence, proto.ID16{}, resumeID)
	if err != nil {
		return delivery.ResumeAck{}, err
	}
	if err := c.prod.CommitLive(ack); err != nil {
		return delivery.ResumeAck{}, err
	}
	return ack, nil
}

// Report is the control snapshot of one controller: the durable mode, the
// batch-capable obligations, the retained pins and the drain point with
// its seal. It reads only committed state (E06-12 observable states).
type Report struct {
	Slot         ClientSlot
	Mode         delivery.ProducerMode
	Remaining    []Obligation
	RetainedPins []Pin
	DrainID      proto.ID16
	Highwater    uint64
	Sealed       bool
}

// Snapshot returns the control report.
func (c *Controller) Snapshot() Report {
	drainID, highwater, hasPoint := c.prod.DrainPoint()
	ack, sealed := c.prod.SealAckSaved()
	if sealed {
		drainID, highwater = ack.DrainID, ack.Highwater
	} else if !hasPoint {
		highwater = 0
	}
	return Report{Slot: c.slot, Mode: c.prod.Mode(), Remaining: c.Remaining(),
		RetainedPins: c.pins.Pins(), DrainID: drainID, Highwater: highwater, Sealed: sealed}
}
