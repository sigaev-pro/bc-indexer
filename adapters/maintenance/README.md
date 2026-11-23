# adapters/maintenance — E05-03.drain (#56)

Owner task: E05-03.drain (#56, stage E05): native RECOVERY_ONLY DRAIN.
Applicability: `adapters/<client>/maintenance` for ETH (geth), BSC (bsc)
and POLY (bor); the drain protocol itself is client-independent, so one
module carries it with one applicability slot per client
(`Slots()`), and a PASS of one family never closes the others. Normative
anchors: DELIVERY_STATE_MACHINE ("Drain и cutover D54/D57/D59",
"Errors, ENOSPC, совместимость"), NATIVE_CAPTURE_PROTOCOL ("Startup,
known branch и lifecycle GC": DRAIN stays RECOVERY_ONLY), LIVE_CONFIG_SPEC
(`--extractor-mode=live|drain`; a saved unfinished DRAIN resumes in DRAIN,
an explicit live before its completion is rejected, DRAINED returns to
LIVE only through an explicit Resume), CONTEXT ("Drain", "Resume",
"Закрепление исходного состояния (pin)").

The package uses the Go standard library plus the repository-local
delivery seam (`core/delivery`) and the closed profile family registry
(`protocol/profiles`). The tests compose the memory stores of
`core/delivery`, the maintenance engine of `core/delivery/maintenance`
and the release engine of `core/delivery/release`; there is no NATS, no
RocksDB and no native binary here. The per-client native integration —
the mode flag of the native process, the callback wiring, the poll loops
— plugs into the seams below inside each client graph, exactly as the
E01-02 adapter blocks do for their seams.

## What the module owns

The mode barrier of the drain, and only that:

- `Startup(flag, durable)` — the `--extractor-mode` startup decision: an
  unknown flag value is invalid input; an explicit live over an
  unfinished DRAIN is rejected; DRAINED answers live with the Resume
  action, never an automatic return to capture; the drain flag runs or
  resumes the drain from LIVE, DRAIN, BLOCKED and DRAINED.
- `Controller.EnterDrain` — the durable DRAIN entry before the recovery
  sync; idempotent for a crash-resumed drain; refused over DRAINED.
- `Controller.Admit` / `Controller.Poll` — the admission gate: in DRAIN
  and DRAINED every new live callback (new block, finality change) and
  every optional poll (GC anchor poll, observer cycle) is refused with
  `ErrAdmissionClosed`; only LIVE admits. The same gate serves the live
  path, so the asymmetry is one rule, not two.
- `Controller.Remaining` / `Controller.Exhausted` — the saved obligation
  frontier: a drain may seal only when no saved obligation can still
  create a batch. Retained future-GC pins never count (AC-DLV-10-02),
  and an obligation whose native source is unavailable stays
  batch-capable, which keeps the drain in DRAIN — the source is needed
  for an unclosed capture, and its unavailability never yields DRAINED.
- `Controller.Seal` — the seal composition: an early seal is refused with
  `ErrSealBlocked`; after exhaustion it delegates to the `Sealer` seam
  (`Engine.SealToDrained` of core/delivery/maintenance), which fixes the
  drain point and batch highwater, seals the admission generation under
  the barrier and saves DRAINED with the seal ack. A repeated seal of the
  same drain id returns the saved ack from whichever crash point the
  process restarted (AC-DLV-10-03); a different drain id over a saved
  seal is an immutable conflict.
- `Controller.FinishResume` — the lost-ResumeAck recovery (AC-DLV-11-03):
  over the exact MAINTENANCE intent the gateway's durable receipt comes
  back — the same ack, not a new generation — and the producer's LIVE
  commit opens capture exactly there.
- `Controller.Snapshot` — the control report over committed state only.

## Seams

| Seam | Real owner | Test double |
|---|---|---|
| `ProducerState` | the producer store of E03-01 through `ProducerSeam` of core/delivery/maintenance | `delivery.MemoryProducer` |
| `Frontier` | the saved obligation records E05-02.reorg replays; backed by the durable registry of E05-01.registry and the decision mirror | `MemoryFrontier` |
| `Sealer` | `Engine.SealToDrained` of core/delivery/maintenance | wired in the tests |
| `ResumeRecovery` | `Engine.RecoverTransition` of core/delivery/maintenance | wired in the tests |
| `PinLedger`, `Lifecycles` | the pin ledger of E01-02.pins and the activation registry of E05-01.registry | the in-memory stores of this package |

## Retained state (drain keeps it)

- `Pin` / `PinLedger` — retained future-GC pins. A drain never releases a
  pin and never polls for an anchor; `Release` exists for the GC owner
  (E05-01.activation_gc), which is exactly why pins do not block DRAINED.
- `Activation` / `Lifecycles` — saved lifecycle activations with the
  reference of their ACKed Applied event (binding digest, retired batch).
  `PlanEvent` plans a later Removed/Status under the producer's current
  committed binding with the old reference traveling unchanged (D59,
  AC-DLV-12-01); no path copies ACKed history to a new destination and no
  path rewrites a saved reference.

## Obligation stages

`ObligAdmitted → ObligExecReady → ObligDecided → ObligFinished`, the
recovery order of the startup rules (ADMITTED/EXEC_READY/decision
markers). `BatchCapable` is true while the obligation is unfinished and
has allocated no batch: one native decision with public events has
exactly one batch, so an obligation with an allocated batch cannot create
another. The closing step (`Advance(…, ObligFinished, batch)`) fixes the
batch; going backwards, skipping a stage or changing an allocated batch
is refused.

## Tests

Portable Go tests (`go test ./...`, also under `-race`), one run per
applicability slot:

| Test | Covers |
|---|---|
| `TestStartupDecisions` | the `--extractor-mode` barrier table |
| `TestEnterDrainTransitions` | the durable DRAIN entry from each mode |
| `TestDrainFinishesSavedObligationsBeforeSeal` | AC-DLV-10-01: recovery finishes while live callbacks arrive; no new admissions; early seal refused; the highwater is fixed after the old obligations |
| `TestRetainedFutureGCPinsDoNotBlockDrained` | AC-DLV-10-02: DRAINED reached without waiting for the anchor; pins retained; no new GC poll or capture |
| `TestCrashAfterGatewaySealBeforeProducerCommit` | AC-DLV-10-03: recovery stays DRAIN, finishes the same seal/drain_id, LIVE not opened |
| `TestDrainedSurvivesRestartWithoutResume` | AC-DLV-10-04: DRAINED survives restart; no new LIVE/capture without the explicit Resume |
| `TestLostResumeAckRecoversSameIntent` | AC-DLV-11-03 in the same-binding and switched variants: the exact MAINTENANCE intent returns the prior ack; the binding stays the variant's; the generation is not incremented again; capture closed until LIVE |
| `TestCutoverLifecycleEventKeepsOldReference` | AC-DLV-12-01/D59: the new Removed goes only to the new binding with the old reference; ACKed Applied history is not copied |
| `TestFrontierSteps`, `TestPinLedgerRules`, `TestLifecyclesRules` | the retained-state rules |
| `TestModeBarrierIsRaceFree` | concurrent callbacks, polls and control reads |

The runtime AC verdicts of the public profiles (mainnet and testnet of
each family) stay with the acceptance runs of E07; these tests are the
engine-level evidence of the mode barrier, not a public-profile PASS.

## What the module does not do

No native process runs here: the mode flag parsing of each client binary,
the native callback wiring and the poll loops are the per-client
integration blocks. The full combined crash matrix is E05-03.full_crash
_matrix; the user-level stop-confirm-start scenario is E06-08.maintenance;
the compatibility matrix over old builds is E05-03.upgrade
(`test/compatibility_recovery`), which consumes the completed drain this
module produces.
