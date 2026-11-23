// E07-03.runbook (#74): the operating procedures drill. Every procedure
// documented in runbook.md — START (live), STOP (graceful drain to
// DRAINED), STOP (hard) with crash recovery, and RESUME — is executed
// against the real drain controller (adapters/maintenance) over the real
// delivery maintenance engine (core/delivery/maintenance), per
// applicability slot, and the normative startup decision table embedded in
// the document is verified row by row against the implementation
// (TASK_DECOMPOSITION E07-03.runbook; BOOT, CONFIG, DELIVERY; REQ-OPS-08,
// REQ-DLV-10, REQ-DLV-11).
//
// The drill keeps the document honest: a procedure that drifts from the
// implementation fails here, and an undocumented refusal the
// implementation answers fails the table coverage below.
package acceptance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/adapters/maintenance"
	"github.com/sigaev-pro/bc-indexer/core/delivery"
	dmaintenance "github.com/sigaev-pro/bc-indexer/core/delivery/maintenance"
	"github.com/sigaev-pro/bc-indexer/core/delivery/release"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// runbookHarness is one drain drill stack of an applicability slot: the
// producer store, the gateway journal, the delivery maintenance engine
// over an always-verifying target, the obligation frontier, the retained
// pin ledger and the drain controller.
type runbookHarness struct {
	slot     maintenance.ClientSlot
	producer proto.ID16
	run      proto.ID16
	epoch    proto.ID16
	binding  proto.Hash32
	stream   string
	prod     *delivery.MemoryProducer
	gw       *delivery.MemoryGateway
	engine   *dmaintenance.Engine
	frontier *maintenance.MemoryFrontier
	pins     *maintenance.PinLedger
	ctrl     *maintenance.Controller
}

// newRunbookHarness builds a fresh drill stack of a slot with one retired
// warm-up batch, so every procedure starts from a non-empty frontier.
func newRunbookHarness(t *testing.T, slot maintenance.ClientSlot) *runbookHarness {
	t.Helper()
	fam := byte(slot.Family)
	h := &runbookHarness{slot: slot,
		producer: proto.ID16{fam, 1}, run: proto.ID16{fam, 2}, epoch: proto.ID16{fam, 3},
		binding: proto.Hash32{fam, 0xaa}, stream: "EXTRACTOR_" + slot.Client,
		frontier: maintenance.NewMemoryFrontier(), pins: maintenance.NewPinLedger()}
	var err error
	h.prod, err = delivery.NewMemoryProducer(h.producer, h.run)
	if err != nil {
		t.Fatalf("NewMemoryProducer: %v", err)
	}
	h.gw, err = delivery.NewMemoryGateway(h.epoch)
	if err != nil {
		t.Fatalf("NewMemoryGateway: %v", err)
	}
	if err := h.gw.SetActiveBinding(1, h.binding); err != nil {
		t.Fatalf("SetActiveBinding: %v", err)
	}
	if err := h.gw.OpenSession(h.fence(delivery.SessionNormal, 1)); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	verifier := &alwaysVerifier{}
	h.engine, err = dmaintenance.New(h.prod, h.gw, verifier)
	if err != nil {
		t.Fatalf("maintenance engine: %v", err)
	}
	h.ctrl, err = maintenance.Open(slot, h.prod, h.frontier, h.pins)
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	h.runWarmupBatch(t)
	return h
}

// runWarmupBatch retires one batch through the durable boundaries of the
// delivery state machine, so every procedure starts from a non-empty
// frontier (a seal over a store that never held a batch is invalid
// input, not the graceful stop path).
func (h *runbookHarness) runWarmupBatch(t *testing.T) {
	t.Helper()
	f := h.fence(delivery.SessionNormal, 1)
	seq, err := h.prod.AllocateIntent()
	if err != nil {
		t.Fatalf("AllocateIntent: %v", err)
	}
	if err := h.prod.StageChunk(seq, "runbook-warmup"); err != nil {
		t.Fatalf("StageChunk: %v", err)
	}
	manifest := delivery.BatchIdentity{
		Batch:          delivery.BatchID{Producer: h.producer, Run: h.run, Seq: seq},
		ManifestDigest: proto.Hash32{byte(seq), 1}, BindingDigest: h.binding,
	}
	if err := h.prod.CommitReady(seq, manifest); err != nil {
		t.Fatalf("CommitReady: %v", err)
	}
	keys := []delivery.FrameKey{
		{Ordinal: 0, Fragment: 0, Complete: true},
		{Ordinal: 1, Fragment: 0, Complete: true},
	}
	id := manifest
	id.PlanDigest = proto.Hash32{byte(seq), 2}
	id.LayoutManifestDigest = proto.Hash32{byte(seq), 3}
	id.IssuerStoreEpoch = h.epoch
	plan := release.NewSealedPlan(id, 1000+seq, h.stream, keys)
	if err := h.prod.CommitSealed(seq, plan); err != nil {
		t.Fatalf("CommitSealed: %v", err)
	}
	if err := h.gw.ImportClosed(f, plan, false); err != nil {
		t.Fatalf("ImportClosed: %v", err)
	}
	for _, key := range keys {
		if err := h.gw.RecordPubAck(f, id.Batch, id.PlanDigest, key); err != nil {
			t.Fatalf("RecordPubAck: %v", err)
		}
	}
	ack, err := h.gw.MarkNATSACKed(f, id)
	if err != nil {
		t.Fatalf("MarkNATSACKed: %v", err)
	}
	if err := release.OnIngestAck(h.prod, seq, ack); err != nil {
		t.Fatalf("OnIngestAck: %v", err)
	}
	if remains, err := release.CleanupPayload(h.prod, seq, 10); err != nil || remains {
		t.Fatalf("CleanupPayload: remains=%v err=%v", remains, err)
	}
	releaseAck, err := release.Release(h.gw, f, id.Batch, 10)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := release.OnReleaseAck(h.prod, releaseAck); err != nil {
		t.Fatalf("OnReleaseAck: %v", err)
	}
}

// alwaysVerifier verifies every target digest: the drill exercises the
// mode barrier, not the D77 online check.
type alwaysVerifier struct{}

// VerifyTarget accepts every digest.
func (v *alwaysVerifier) VerifyTarget(context.Context, proto.Hash32) error { return nil }

// fence builds a session fence of the drill.
func (h *runbookHarness) fence(kind delivery.SessionKind, admission uint64) delivery.Fence {
	return delivery.Fence{SessionToken: []byte("runbook-session"), Producer: h.producer, Run: h.run,
		ConnectionGeneration: 1, AdmissionGeneration: admission,
		BindingDigest: h.binding, Kind: kind}
}

// drainID fixes the drill drain identity.
func (h *runbookHarness) drainID() proto.ID16 { return proto.ID16{byte(h.slot.Family), 0x77} }

// resumeID fixes the drill resume identity.
func (h *runbookHarness) resumeID() proto.ID16 { return proto.ID16{byte(h.slot.Family), 0x88} }

// runbookSlots maps the product slots onto the drain controller's
// applicability slots.
func runbookSlots(t *testing.T) []maintenance.ClientSlot {
	t.Helper()
	slots := maintenance.Slots()
	if len(slots) != 3 {
		t.Fatalf("the drain mode owns three applicability slots, got %d", len(slots))
	}
	return slots
}

// TestRunbookStartupTableMatchesImplementation parses the normative
// startup decision table out of runbook.md and verifies every documented
// (flag, durable mode) row against the real Startup decision — the
// document cannot drift from the implementation silently.
func TestRunbookStartupTableMatchesImplementation(t *testing.T) {
	rows := parseRunbookTable(t)
	if len(rows) == 0 {
		t.Fatalf("runbook.md carries no startup decision rows")
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.flag+"|"+row.mode] = true
		var flag maintenance.StartupMode
		switch row.flag {
		case "live":
			flag = maintenance.FlagLive
		case "drain":
			flag = maintenance.FlagDrain
		default:
			t.Fatalf("runbook.md names unknown flag %q", row.flag)
		}
		var durable delivery.ProducerMode
		switch row.mode {
		case "LIVE":
			durable = delivery.ModeLive
		case "BLOCKED":
			durable = delivery.ModeBlocked
		case "DRAIN":
			durable = delivery.ModeDrain
		case "DRAINED":
			durable = delivery.ModeDrained
		default:
			t.Fatalf("runbook.md names unknown durable mode %q", row.mode)
		}
		action, err := maintenance.Startup(flag, durable)
		switch row.result {
		case "LIVE":
			if err != nil || action != maintenance.ActionLive {
				t.Fatalf("documented live over %s: got action %v err %v", row.mode, action, err)
			}
		case "DRAIN":
			if err != nil || action != maintenance.ActionDrain {
				t.Fatalf("documented drain over %s: got action %v err %v", row.mode, action, err)
			}
		case "RESUME":
			if err != nil || action != maintenance.ActionResume {
				t.Fatalf("documented resume over %s: got action %v err %v", row.mode, action, err)
			}
		case "REJECT":
			if err == nil {
				t.Fatalf("documented rejection of %s over %s was accepted", row.flag, row.mode)
			}
		default:
			t.Fatalf("runbook.md names unknown outcome %q", row.result)
		}
	}
	// The table is complete: every (flag, mode) pair of the closed
	// registry is documented — an undocumented transition of the
	// implementation would otherwise pass unnoticed.
	for _, flag := range []string{"live", "drain"} {
		for _, mode := range []string{"LIVE", "BLOCKED", "DRAIN", "DRAINED"} {
			if !seen[flag+"|"+mode] {
				t.Fatalf("runbook.md does not document flag=%s mode=%s", flag, mode)
			}
		}
	}
	// Unknown flags never fall back silently.
	if _, err := maintenance.Startup(maintenance.StartupMode("vacation"), delivery.ModeLive); err == nil {
		t.Fatalf("an unknown --extractor-mode value was accepted")
	}
}

// runbookRow is one parsed row of the embedded startup table.
type runbookRow struct {
	flag, mode, result string
}

// parseRunbookTable extracts the ```table fenced startup decision rows.
func parseRunbookTable(t *testing.T) []runbookRow {
	t.Helper()
	doc := readRunbook(t)
	var rows []runbookRow
	inTable := false
	for _, line := range strings.Split(doc, "\n") {
		switch {
		case strings.TrimSpace(line) == "```table":
			inTable = true
		case inTable && strings.TrimSpace(line) == "```":
			inTable = false
		case inTable:
			parts := strings.Fields(line)
			if len(parts) != 4 || parts[2] != "->" ||
				!strings.HasPrefix(parts[0], "flag=") || !strings.HasPrefix(parts[1], "mode=") {
				t.Fatalf("runbook.md table row %q is malformed", line)
			}
			rows = append(rows, runbookRow{flag: strings.TrimPrefix(parts[0], "flag="),
				mode: strings.TrimPrefix(parts[1], "mode="), result: parts[3]})
		}
	}
	return rows
}

// readRunbook loads runbook.md from the module directory.
func readRunbook(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("cannot locate the module directory")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "runbook.md"))
	if err != nil {
		t.Fatalf("read runbook.md: %v", err)
	}
	return string(raw)
}

// TestRunbookDocumentedProceduresRun drills every documented procedure of
// runbook.md per applicability slot: START live, STOP graceful (drain to
// DRAINED with its refusals and idempotent seal), STOP hard with crash
// recovery inside the drain, and RESUME back to an open admission gate.
func TestRunbookDocumentedProceduresRun(t *testing.T) {
	for _, slot := range runbookSlots(t) {
		t.Run(slot.String(), func(t *testing.T) {
			h := newRunbookHarness(t, slot)

			// --- START (live): the documented rows 1–4 of the procedure.
			action, err := maintenance.Startup(maintenance.FlagLive, h.prod.Mode())
			if err != nil || action != maintenance.ActionLive {
				t.Fatalf("START: startup decision %v err %v, want LIVE", action, err)
			}
			if err := h.ctrl.Admit(maintenance.LiveCallback{
				Kind: maintenance.CallbackNewBlock, Sequence: 1}); err != nil {
				t.Fatalf("START: a live new-block callback was refused: %v", err)
			}
			if err := h.ctrl.Poll(maintenance.PollTick{Kind: maintenance.PollGC}); err != nil {
				t.Fatalf("START: a live GC poll was refused: %v", err)
			}
			if mode := h.prod.Mode(); mode != delivery.ModeLive {
				t.Fatalf("START: producer mode %s, want LIVE (the documented evidence)", mode)
			}

			// --- STOP (graceful): drain to DRAINED.
			action, err = maintenance.Startup(maintenance.FlagDrain, h.prod.Mode())
			if err != nil || action != maintenance.ActionDrain {
				t.Fatalf("STOP: startup decision %v err %v, want DRAIN", action, err)
			}
			if err := h.ctrl.EnterDrain(); err != nil {
				t.Fatalf("STOP: EnterDrain: %v", err)
			}
			if mode := h.prod.Mode(); mode != delivery.ModeDrain {
				t.Fatalf("STOP: mode is %s, want DRAIN", mode)
			}
			// The mode barrier: no new work while the drain runs.
			if err := h.ctrl.Admit(maintenance.LiveCallback{
				Kind: maintenance.CallbackNewBlock, Sequence: 2}); !errors.Is(err, maintenance.ErrAdmissionClosed) {
				t.Fatalf("STOP: a new-block callback was admitted during the drain: %v", err)
			}
			for _, tick := range []maintenance.PollTick{{Kind: maintenance.PollGC},
				{Kind: maintenance.PollObserver}} {
				if err := h.ctrl.Poll(tick); !errors.Is(err, maintenance.ErrAdmissionClosed) {
					t.Fatalf("STOP: a %s poll started during the drain: %v", tick.Kind, err)
				}
			}

			// An early seal is refused: a batch-capable saved obligation
			// still holds the frontier (the documented refusal).
			if err := h.frontier.Save(maintenance.Obligation{Sequence: 7,
				Stage: maintenance.ObligDecided}); err != nil {
				t.Fatalf("frontier save: %v", err)
			}
			if _, err := h.ctrl.Seal(context.Background(), h.engine,
				h.fence(delivery.SessionNormal, 1), h.drainID()); !errors.Is(err, maintenance.ErrSealBlocked) {
				t.Fatalf("STOP: an early seal was allowed: %v", err)
			}
			// The recovery finishes the saved obligation to its closed
			// delivery outcome: the batch is allocated and the stage ends
			// FINISHED.
			if err := h.frontier.Advance(7, maintenance.ObligFinished, 3); err != nil {
				t.Fatalf("frontier advance: %v", err)
			}
			// A retained future-GC pin does not block the completed drain.
			if err := h.pins.Retain(maintenance.Pin{ID: "future-gc",
				Reason: "waiting for a finalized anchor"}); err != nil {
				t.Fatalf("pin retain: %v", err)
			}
			if !h.ctrl.Exhausted() {
				t.Fatalf("STOP: the frontier still reports batch-capable obligations")
			}
			ack, err := h.ctrl.Seal(context.Background(), h.engine,
				h.fence(delivery.SessionNormal, 1), h.drainID())
			if err != nil {
				t.Fatalf("STOP: seal: %v", err)
			}
			if mode := h.prod.Mode(); mode != delivery.ModeDrained {
				t.Fatalf("STOP: mode is %s after the seal, want DRAINED", mode)
			}
			// The documented evidence: the saved seal ack, the snapshot
			// with no remaining batch-capable obligations, the retained
			// pin still held.
			saved, ok := h.prod.SealAckSaved()
			if !ok || saved.DrainID != h.drainID() || saved != ack {
				t.Fatalf("STOP: the durable seal ack differs from the returned one")
			}
			snap := h.ctrl.Snapshot()
			if snap.Mode != delivery.ModeDrained || len(snap.Remaining) != 0 ||
				len(snap.RetainedPins) != 1 || !snap.Sealed {
				t.Fatalf("STOP: snapshot %+v contradicts the documented evidence", snap)
			}
			// The documented refusals of the sealed state.
			if err := h.ctrl.EnterDrain(); !errors.Is(err, maintenance.ErrTransition) {
				t.Fatalf("STOP: re-entering drain over DRAINED was allowed: %v", err)
			}
			if _, err := h.ctrl.Seal(context.Background(), h.engine,
				h.fence(delivery.SessionNormal, 1), proto.ID16{0x99}); !errors.Is(err, maintenance.ErrImmutable) {
				t.Fatalf("STOP: sealing under a different drain id was allowed: %v", err)
			}
			repeated, err := h.ctrl.Seal(context.Background(), h.engine,
				h.fence(delivery.SessionNormal, 1), h.drainID())
			if err != nil || repeated != ack {
				t.Fatalf("STOP: the repeated seal of the same drain id did not return the saved ack")
			}
			// The mode barrier holds after the drain: capture opens only
			// through the explicit Resume.
			if err := h.ctrl.Admit(maintenance.LiveCallback{
				Kind: maintenance.CallbackFinality, Sequence: 3}); !errors.Is(err, maintenance.ErrAdmissionClosed) {
				t.Fatalf("STOP: a callback was admitted over DRAINED: %v", err)
			}

			// --- STOP (hard) and crash recovery: an unfinished drain
			// resumes in DRAIN; an explicit live start over it is
			// rejected (the documented crash rows).
			crashDrill := newRunbookHarness(t, slot)
			if err := crashDrill.ctrl.EnterDrain(); err != nil {
				t.Fatalf("crash drill: EnterDrain: %v", err)
			}
			crashDrill.prod.Reopen() // the hard stop: RAM overlays lost, the mode survives
			action, err = maintenance.Startup(maintenance.FlagLive, crashDrill.prod.Mode())
			if !errors.Is(err, maintenance.ErrTransition) {
				t.Fatalf("crash drill: live over an unfinished drain was accepted: %v", err)
			}
			action, err = maintenance.Startup(maintenance.FlagDrain, crashDrill.prod.Mode())
			if err != nil || action != maintenance.ActionDrain {
				t.Fatalf("crash drill: drain restart over DRAIN: %v err %v", action, err)
			}
			if err := crashDrill.ctrl.EnterDrain(); err != nil {
				t.Fatalf("crash drill: the resumed drain entry failed: %v", err)
			}
			if _, err := crashDrill.ctrl.Seal(context.Background(), crashDrill.engine,
				crashDrill.fence(delivery.SessionNormal, 1), crashDrill.drainID()); err != nil {
				t.Fatalf("crash drill: seal: %v", err)
			}

			// --- RESUME: the explicit return to live.
			action, err = maintenance.Startup(maintenance.FlagLive, h.prod.Mode())
			if err != nil || action != maintenance.ActionResume {
				t.Fatalf("RESUME: startup decision %v err %v, want RESUME", action, err)
			}
			resumeAck, err := h.engine.Resume(h.fence(delivery.SessionNormal, 1), h.resumeID())
			if err != nil {
				t.Fatalf("RESUME: engine resume: %v", err)
			}
			if mode := h.prod.Mode(); mode != delivery.ModeLive {
				t.Fatalf("RESUME: producer mode %s after the resume commit, want LIVE", mode)
			}
			if _, ok := h.prod.ResumeIntentSaved(); !ok {
				t.Fatalf("RESUME: the resume intent was not saved")
			}
			if err := h.ctrl.Admit(maintenance.LiveCallback{
				Kind: maintenance.CallbackNewBlock, Sequence: 4}); err != nil {
				t.Fatalf("RESUME: capture did not open after the resume: %v", err)
			}
			// A lost ResumeAck recovers through the saved intent: the
			// same ack, not a new generation (the documented recovery).
			again, err := h.engine.Resume(h.fence(delivery.SessionNormal, 1), h.resumeID())
			if err != nil || again != resumeAck {
				t.Fatalf("RESUME: the repeated resume did not return the same ack: %v err %v", again, err)
			}
			if resumeAck.NewAdmissionGeneration <= ack.AdmissionGeneration {
				t.Fatalf("RESUME: the resume generation %d did not advance past the seal %d",
					resumeAck.NewAdmissionGeneration, ack.AdmissionGeneration)
			}
		})
	}
}

// TestRunbookDiagnosableStatesAreObservable verifies the diagnostics
// section: the controller snapshot reports only committed state — mode,
// obligations, pins, drain point and seal — readable with no debug
// machinery, and every refusal names its sentinel.
func TestRunbookDiagnosableStatesAreObservable(t *testing.T) {
	for _, slot := range runbookSlots(t) {
		t.Run(slot.String(), func(t *testing.T) {
			h := newRunbookHarness(t, slot)
			snap := h.ctrl.Snapshot()
			if snap.Slot != slot || snap.Mode != delivery.ModeLive || snap.Sealed {
				t.Fatalf("diagnostics: the live snapshot %+v is wrong", snap)
			}
			// Every documented refusal carries a named sentinel.
			if err := h.ctrl.EnterDrain(); err != nil {
				t.Fatalf("enter drain: %v", err)
			}
			// A saved batch-capable obligation holds the frontier, so the
			// seal refusal is the documented seal-blocked sentinel.
			if err := h.frontier.Save(maintenance.Obligation{Sequence: 5,
				Stage: maintenance.ObligDecided}); err != nil {
				t.Fatalf("frontier save: %v", err)
			}
			checks := []struct {
				name string
				err  error
				want error
			}{
				{"admission", h.ctrl.Admit(maintenance.LiveCallback{
					Kind: maintenance.CallbackNewBlock, Sequence: 1}), maintenance.ErrAdmissionClosed},
				{"poll", h.ctrl.Poll(maintenance.PollTick{Kind: maintenance.PollGC}),
					maintenance.ErrAdmissionClosed},
				{"seal", func() error {
					_, err := h.ctrl.Seal(context.Background(), h.engine,
						h.fence(delivery.SessionNormal, 1), h.drainID())
					return err
				}(), maintenance.ErrSealBlocked},
			}
			for _, check := range checks {
				if check.err == nil {
					t.Fatalf("diagnostics: the %s refusal is missing", check.name)
				}
				if !errors.Is(check.err, check.want) {
					t.Fatalf("diagnostics: the %s refusal carries %v, want the documented sentinel %v",
						check.name, check.err, check.want)
				}
				if strings.TrimSpace(check.err.Error()) == "" {
					t.Fatalf("diagnostics: the %s refusal is silent", check.name)
				}
			}
			// A zero drain id is invalid input, never a silent default.
			if _, err := h.ctrl.Seal(context.Background(), h.engine,
				h.fence(delivery.SessionNormal, 1), proto.ID16{}); !errors.Is(err, maintenance.ErrInvalid) {
				t.Fatalf("diagnostics: a zero drain id produced %v, want invalid input", err)
			}
		})
	}
}

// The runbook document names its procedures; the drill proves the
// document and the implementation agree (format check keeps the parser
// and the prose in sync).
func TestRunbookDocumentCoversTheDrilledProcedures(t *testing.T) {
	doc := readRunbook(t)
	for _, section := range []string{
		"## START (live)", "## STOP (graceful: drain to DRAINED)",
		"## STOP (hard) and crash recovery", "## RESUME", "## Diagnostics",
	} {
		if !strings.Contains(doc, section) {
			t.Fatalf("runbook.md lost its section %q", section)
		}
	}
	lower := strings.ToLower(doc)
	for _, forbidden := range []string{"password", "private key", "api_key", "token="} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("runbook.md embeds credential material %q; the acceptance procedures run without secrets",
				forbidden)
		}
	}
	// The documented evidence of the seal is observable through the
	// producer's durable answers, asserted by fmt-free sentinel checks in
	// the drill above; this guard keeps the doc naming them.
	for _, term := range []string{"SealAck", "ResumeAck", "DRAINED", "admission-closed"} {
		if !strings.Contains(doc, term) {
			t.Fatalf("runbook.md stopped naming the observable %q", term)
		}
	}
}
