// The drain-mode tests of E05-03.drain: the startup barrier of
// --extractor-mode, the admission and poll refusals, the exhaustion rule
// that fixes the highwater only after the saved obligations, the retained
// future-GC pins that never block DRAINED, and the crash points around
// the seal (AC-DLV-10-01…AC-DLV-10-04). Every scenario runs once per
// applicability slot: a PASS of one family never closes the others.
package maintenance

import (
	"context"
	"errors"
	"testing"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// TestStartupDecisions checks the startup barrier table: an unknown flag
// value is invalid input, an explicit live over an unfinished drain is
// rejected, and DRAINED answers live with the Resume action, never an
// automatic return to capture.
func TestStartupDecisions(t *testing.T) {
	cases := []struct {
		flag    StartupMode
		durable delivery.ProducerMode
		want    Action
		err     bool
	}{
		{FlagLive, delivery.ModeLive, ActionLive, false},
		{FlagLive, delivery.ModeBlocked, ActionLive, false},
		{FlagLive, delivery.ModeDrain, 0, true},
		{FlagLive, delivery.ModeDrained, ActionResume, false},
		{FlagDrain, delivery.ModeLive, ActionDrain, false},
		{FlagDrain, delivery.ModeDrain, ActionDrain, false},
		{FlagDrain, delivery.ModeBlocked, ActionDrain, false},
		{FlagDrain, delivery.ModeDrained, ActionDrain, false},
		{StartupMode("sync"), delivery.ModeLive, 0, true},
		{StartupMode(""), delivery.ModeDrained, 0, true},
	}
	for _, tc := range cases {
		action, err := Startup(tc.flag, tc.durable)
		if tc.err {
			if err == nil {
				t.Fatalf("Startup(%s, %s) succeeded, want rejection", tc.flag, tc.durable)
			}
			if action != 0 {
				t.Fatalf("Startup(%s, %s) returned %s with an error", tc.flag, tc.durable, action)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Startup(%s, %s): %v", tc.flag, tc.durable, err)
		}
		if action != tc.want {
			t.Fatalf("Startup(%s, %s) returned %s, want %s", tc.flag, tc.durable, action, tc.want)
		}
	}
	if _, err := Startup(FlagLive, delivery.ModeDrain); !errors.Is(err, ErrTransition) {
		t.Fatalf("live over an unfinished drain: %v", err)
	}
}

// TestOpenRejectsBadInput checks the controller validation.
func TestOpenRejectsBadInput(t *testing.T) {
	slot := Slots()[0]
	prod, err := delivery.NewMemoryProducer(proto.ID16{1}, proto.ID16{2})
	if err != nil {
		t.Fatalf("NewMemoryProducer: %v", err)
	}
	frontier, pins := NewMemoryFrontier(), NewPinLedger()
	if _, err := Open(ClientSlot{Client: "noun", Family: slot.Family}, prod, frontier, pins); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown slot: %v", err)
	}
	if _, err := Open(slot, nil, frontier, pins); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil producer: %v", err)
	}
	if _, err := Open(slot, prod, nil, pins); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil frontier: %v", err)
	}
	if _, err := Open(slot, prod, frontier, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil pins: %v", err)
	}
}

// TestEnterDrainTransitions checks the durable DRAIN entry: LIVE and
// BLOCKED enter, an unfinished DRAIN stays, a DRAINED store refuses.
func TestEnterDrainTransitions(t *testing.T) {
	for _, slot := range Slots() {
		t.Run(slot.String(), func(t *testing.T) {
			prod, err := delivery.NewMemoryProducer(proto.ID16{1}, proto.ID16{2})
			if err != nil {
				t.Fatalf("NewMemoryProducer: %v", err)
			}
			ctrl, err := Open(slot, prod, NewMemoryFrontier(), NewPinLedger())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := ctrl.EnterDrain(); err != nil {
				t.Fatalf("EnterDrain from LIVE: %v", err)
			}
			if mode := prod.Mode(); mode != delivery.ModeDrain {
				t.Fatalf("mode is %s, want DRAIN", mode)
			}
			if err := ctrl.EnterDrain(); err != nil {
				t.Fatalf("EnterDrain over DRAIN: %v", err)
			}
			if err := prod.SetMode(delivery.ModeBlocked); err != nil {
				t.Fatalf("SetMode: %v", err)
			}
			if err := ctrl.EnterDrain(); err != nil {
				t.Fatalf("EnterDrain from BLOCKED: %v", err)
			}
			if err := prod.SetMode(delivery.ModeDrained); err != nil {
				t.Fatalf("SetMode: %v", err)
			}
			if err := ctrl.EnterDrain(); !errors.Is(err, ErrTransition) {
				t.Fatalf("EnterDrain over DRAINED: %v", err)
			}
		})
	}
}

// TestDrainFinishesSavedObligationsBeforeSeal is AC-DLV-10-01: a drain
// with a pending native obligation capable of creating a batch, while new
// live callbacks keep arriving, finishes the recovery first — the new
// admissions are never created, the early seal is refused, and the
// highwater is fixed only after the old obligations.
func TestDrainFinishesSavedObligationsBeforeSeal(t *testing.T) {
	for _, slot := range Slots() {
		t.Run(slot.String(), func(t *testing.T) {
			h := newHarness(t, slot)
			warm := h.prod.RetiredPrefix()
			if warm == 0 {
				t.Fatalf("the warm-up batch left an empty frontier")
			}
			// One saved obligation, admitted before the drain and still
			// capable of creating a batch.
			const obSeq = 7
			if err := h.frontier.Save(Obligation{Sequence: obSeq, Stage: ObligAdmitted}); err != nil {
				t.Fatalf("Save: %v", err)
			}
			if h.ctrl.Exhausted() {
				t.Fatalf("an ADMITTED obligation must keep the frontier open")
			}

			h.enterDrain(t)
			drainID := proto.ID16{byte(h.slot.Family), 0x44}

			// Live inputs keep arriving during the recovery; none is
			// admitted, and the seal waits through every stage.
			for i, stage := range []ObligationStage{ObligExecReady, ObligDecided} {
				h.refuseNewWork(t, warm+uint64(i+1))
				if _, err := h.ctrl.Seal(context.Background(), h.engine,
					h.fence(delivery.SessionNormal, 1), drainID); !errors.Is(err, ErrSealBlocked) {
					t.Fatalf("early seal at %s: %v", stage, err)
				}
				if err := h.frontier.Advance(obSeq, stage, 0); err != nil {
					t.Fatalf("Advance to %s: %v", stage, err)
				}
			}
			// The DECIDED obligation allocates its batch, the delivery
			// finishes it, and only then the obligation closes.
			seq := h.runBatch(t, h.fence(delivery.SessionNormal, 1))
			if seq != warm+1 {
				t.Fatalf("the obligation's batch is %d, want %d", seq, warm+1)
			}
			if err := h.frontier.Advance(obSeq, ObligFinished, seq); err != nil {
				t.Fatalf("Advance to FINISHED: %v", err)
			}
			if !h.ctrl.Exhausted() {
				t.Fatalf("the finished obligation still blocks the frontier")
			}

			ack, err := h.ctrl.Seal(context.Background(), h.engine, h.fence(delivery.SessionNormal, 1), drainID)
			if err != nil {
				t.Fatalf("Seal after exhaustion: %v", err)
			}
			if ack.Highwater != seq {
				t.Fatalf("highwater is %d, want the obligation's batch %d", ack.Highwater, seq)
			}
			if mode := h.prod.Mode(); mode != delivery.ModeDrained {
				t.Fatalf("mode is %s, want DRAINED", mode)
			}
			// After the completed drain the barrier stays closed.
			h.refuseNewWork(t, warm+10)
		})
	}
}

// TestRetainedFutureGCPinsDoNotBlockDrained is AC-DLV-10-02: with the
// delivery finished and retained GC pins still waiting for a future
// anchor, SealGeneration runs and the drain completes without waiting;
// the pins are retained and no new GC poll or capture starts.
func TestRetainedFutureGCPinsDoNotBlockDrained(t *testing.T) {
	for _, slot := range Slots() {
		t.Run(slot.String(), func(t *testing.T) {
			h := newHarness(t, slot)
			retained := []Pin{
				{ID: "pin-side-branch", Reason: "future finalized anchor"},
				{ID: "pin-replay-inputs", Reason: "future finalized anchor"},
			}
			for _, pin := range retained {
				if err := h.pins.Retain(pin); err != nil {
					t.Fatalf("Retain: %v", err)
				}
			}
			h.enterDrain(t)
			drainID := proto.ID16{byte(h.slot.Family), 0x55}

			h.refuseNewWork(t, h.prod.RetiredPrefix()+1)
			ack, err := h.ctrl.Seal(context.Background(), h.engine, h.fence(delivery.SessionNormal, 1), drainID)
			if err != nil {
				t.Fatalf("Seal with retained pins: %v", err)
			}
			if ack.Highwater != h.prod.RetiredPrefix() {
				t.Fatalf("highwater %d differs from the retired prefix %d", ack.Highwater, h.prod.RetiredPrefix())
			}
			if mode := h.prod.Mode(); mode != delivery.ModeDrained {
				t.Fatalf("mode is %s, want DRAINED", mode)
			}
			kept := h.pins.Pins()
			if len(kept) != len(retained) {
				t.Fatalf("the drain kept %d pins, want %d", len(kept), len(retained))
			}
			for i, pin := range retained {
				if kept[i] != pin {
					t.Fatalf("pin %d is %+v, want %+v", i, kept[i], pin)
				}
			}
			// DRAINED keeps refusing new polls: the pins wait for their
			// anchor without any polling by the drained process.
			h.refuseNewWork(t, h.prod.RetiredPrefix()+1)
		})
	}
}

// TestCrashAfterGatewaySealBeforeProducerCommit is AC-DLV-10-03: the
// gateway accepted the seal, the producer has not yet synced DRAINED, and
// the power fails. The restart stays in DRAIN and finishes the same
// seal/drain_id; LIVE is not opened.
func TestCrashAfterGatewaySealBeforeProducerCommit(t *testing.T) {
	for _, slot := range Slots() {
		t.Run(slot.String(), func(t *testing.T) {
			h := newHarness(t, slot)
			h.enterDrain(t)
			drainID := proto.ID16{byte(h.slot.Family), 0x66}
			highwater := h.prod.RetiredPrefix()

			// The crash window: the drain point is fixed, the gateway
			// sealed durably, the producer has not saved the ack.
			if err := h.prod.SetDrainPoint(drainID, highwater); err != nil {
				t.Fatalf("SetDrainPoint: %v", err)
			}
			gwAck, err := h.gw.SealGeneration(h.fence(delivery.SessionNormal, 1), delivery.SealIntent{
				BindingGeneration: 1, AdmissionGeneration: 1, DrainID: drainID,
				Highwater: highwater, BindingDigest: h.oldBind,
			})
			if err != nil {
				t.Fatalf("SealGeneration: %v", err)
			}
			h.prod.Reopen()
			h.gw.Reopen()
			if mode := h.prod.Mode(); mode != delivery.ModeDrain {
				t.Fatalf("mode after the crash is %s, want DRAIN", mode)
			}
			if _, sealed := h.prod.SealAckSaved(); sealed {
				t.Fatalf("the producer saved a seal ack the crash window excludes")
			}
			// The restarted process handshakes a fresh session before it
			// finishes the saved drain.
			if err := h.gw.OpenSession(h.fence(delivery.SessionNormal, 1)); err != nil {
				t.Fatalf("OpenSession after the restart: %v", err)
			}

			// The restart: an explicit live is rejected, the drain flag
			// resumes the same drain and finishes the same seal.
			if _, err := Startup(FlagLive, h.prod.Mode()); !errors.Is(err, ErrTransition) {
				t.Fatalf("live start over the crashed drain: %v", err)
			}
			action, err := Startup(FlagDrain, h.prod.Mode())
			if err != nil || action != ActionDrain {
				t.Fatalf("drain restart: %v %v", action, err)
			}
			ack, err := h.ctrl.Seal(context.Background(), h.engine, h.fence(delivery.SessionNormal, 1), drainID)
			if err != nil {
				t.Fatalf("Seal resumes the saved drain: %v", err)
			}
			if !ack.SameSeal(gwAck) {
				t.Fatalf("the resumed seal is %+v, want the saved %+v", ack, gwAck)
			}
			if ack.DrainID != drainID || ack.Highwater != highwater {
				t.Fatalf("the resumed seal changed the drain identity or highwater: %+v", ack)
			}
			if mode := h.prod.Mode(); mode != delivery.ModeDrained {
				t.Fatalf("mode is %s, want DRAINED", mode)
			}
			if err := h.ctrl.Admit(LiveCallback{Kind: CallbackNewBlock, Sequence: highwater + 1}); !isAdmissionClosed(err) {
				t.Fatalf("LIVE opened after the crash recovery: %v", err)
			}
		})
	}
}

// TestDrainedSurvivesRestartWithoutResume is AC-DLV-10-04: DRAINED with
// the seal ack durable survives a power loss and an ordinary restart; a
// new LIVE and capture never start without the explicit Resume.
func TestDrainedSurvivesRestartWithoutResume(t *testing.T) {
	for _, slot := range Slots() {
		t.Run(slot.String(), func(t *testing.T) {
			h := newHarness(t, slot)
			h.enterDrain(t)
			drainID := proto.ID16{byte(h.slot.Family), 0x77}
			acked, err := h.ctrl.Seal(context.Background(), h.engine, h.fence(delivery.SessionNormal, 1), drainID)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}

			// Power loss and an ordinary restart of the same build.
			h.prod.Reopen()
			h.gw.Reopen()
			if err := h.gw.OpenSession(h.fence(delivery.SessionNormal, 1)); err != nil {
				t.Fatalf("OpenSession after the restart: %v", err)
			}
			action, err := Startup(FlagDrain, h.prod.Mode())
			if err != nil || action != ActionDrain {
				t.Fatalf("drain restart over DRAINED: %v %v", action, err)
			}
			report := h.ctrl.Snapshot()
			if !report.Sealed || report.Mode != delivery.ModeDrained {
				t.Fatalf("the restart lost DRAINED: %+v", report)
			}
			if report.DrainID != drainID || report.Highwater != acked.Highwater {
				t.Fatalf("the restart lost the drain point: %+v", report)
			}
			again, err := h.ctrl.Seal(context.Background(), h.engine, h.fence(delivery.SessionNormal, 1), drainID)
			if err != nil {
				t.Fatalf("seal replay over DRAINED: %v", err)
			}
			if !again.SameSeal(acked) {
				t.Fatalf("the replayed seal is %+v, want %+v", again, acked)
			}
			// A different drain id over the saved seal is a conflict.
			other := proto.ID16{byte(h.slot.Family), 0x78}
			if _, err := h.ctrl.Seal(context.Background(), h.engine, h.fence(delivery.SessionNormal, 1), other); !errors.Is(err, ErrImmutable) {
				t.Fatalf("a second drain id over the saved seal: %v", err)
			}
			// Live remains explicit: the live flag maps to the Resume
			// action, and the admission gate stays closed until it runs.
			action, err = Startup(FlagLive, h.prod.Mode())
			if err != nil || action != ActionResume {
				t.Fatalf("live restart over DRAINED: %v %v", action, err)
			}
			h.refuseNewWork(t, h.prod.RetiredPrefix()+1)
		})
	}
}

// TestSealNeedsDrainModeAndValidInput checks the seal guards outside the
// scenario flow: a zero drain id, a nil sealer and a LIVE store.
func TestSealNeedsDrainModeAndValidInput(t *testing.T) {
	for _, slot := range Slots() {
		t.Run(slot.String(), func(t *testing.T) {
			h := newHarness(t, slot)
			ctx := context.Background()
			fence := h.fence(delivery.SessionNormal, 1)
			drainID := proto.ID16{byte(h.slot.Family), 0x88}
			if _, err := h.ctrl.Seal(ctx, h.engine, fence, proto.ID16{}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("zero drain id: %v", err)
			}
			if _, err := h.ctrl.Seal(ctx, nil, fence, drainID); !errors.Is(err, ErrInvalid) {
				t.Fatalf("nil sealer: %v", err)
			}
			// A LIVE store never seals: the mode barrier rejects first.
			if _, err := h.ctrl.Seal(ctx, h.engine, fence, drainID); err == nil {
				t.Fatalf("a LIVE store sealed a drain")
			}
		})
	}
}
