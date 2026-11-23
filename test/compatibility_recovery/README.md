# test/compatibility_recovery — E05-03.upgrade (#57)

Owner task: E05-03.upgrade (#57, stage E05): old-build drain / compatible
replay / rollback matrix with retained lifecycle metadata; an
incompatible persistent format is always rejected prewrite. REQ-DLV-14,
AC-DLV-14-02. Applicability: ETH/BSC/POLY — every scenario runs once per
adapter slot of `adapters/maintenance.Slots()`, and a PASS of one family
never closes the others.

Normative anchors: DELIVERY_STATE_MACHINE ("Errors, ENOSPC,
совместимость": preflight versions are checked before any writable
mutation; an incompatible wire/plan upgrade requires DRAINED by the old
build; an incompatible persistent store format change is rejected before
a write even with empty payload, there are no automatic migrations;
UPGRADE-01), CONTROL_WIRE_CONTRACT (unknown version = protocol
incompatibility, no fallback success), CONTEXT ("Совместимость
сохранённого состояния": completing delivery does not make an
incompatible store compatible). Inputs: E02-04.compatibility
(`protocol/compatibility`, the policy) and E05-03.drain
(`adapters/maintenance`, the completed drain).

The module is a Go test module: it composes the version policy with a
real drain driven by the `adapters/maintenance` controller over the
memory stores of `core/delivery`, so every verdict lands on an actual
store image. There is no NATS, no RocksDB and no native binary; the
PubAck accounting of a finished batch is driven straight through the
store seams.

## Model

- `build` — one extractor build: its current wire/plan version with the
  earlier versions it claims to read, its persistent store format, and
  one `compatibility.Policy` per family. `buildsOf` wires the old build
  (pinned wire version, `extractor-store/1`) and the new build
  (successor wire version claiming to read the pinned one,
  `extractor-store/2`) for each of the six preflight kinds of the
  wire/plan family (`transport/2→3`, `record_format/1→2`, `layout/2→3`,
  `header/2→3`, `control/2→3`, `manifest/2→3`), so the matrix covers the
  whole family, not one kind.
- `scenario` — one durable store image: the memory producer/gateway
  stores with one open session, the maintenance engine, the drain
  controller, the retained lifecycle metadata (saved activation
  references, future-GC pins), the version facts of the pending records
  (`compatibility.PendingRecord`), and the drivers:
  `writePending` (INTENT/READY/SEALED), `finishPending` (drives one
  pending record to retired through IMPORT_SEALED of its exact sealed
  plan, full PubAck accounting, bounded cleanup, ordered release),
  `drain` (the old build's drain: startup decision, durable DRAIN entry,
  admission refusal, ordered finish, seal to DRAINED with the highwater),
  `durable` (the policy input), and `fingerprint` (a SHA-256 over the
  whole visible store image — both delivery stores with every maintenance
  record, the saved obligations, the retained pins and activation
  references, the pending version facts).

## Matrix

| Test | Column | Expected |
|---|---|---|
| `TestWireUpgradeRejectedBeforeDrain` | preflight before the drain | The incompatible wire/plan upgrade is rejected (`ErrTransitionBlocked`) in LIVE with pending, DRAIN with pending, DRAIN in progress and BLOCKED; the fingerprint is unchanged; reading/replaying the pending records stays allowed (continuing saved obligations is not an upgrade). First half of AC-DLV-14-02. |
| `TestOldBuildDrainsThenUpgradePasses` | old-build drain | After the pre-drain rejection the old build drains by the prescribed order; nothing migrated silently; with DRAINED and zero pending the new build's preflight passes for all six kinds; the store-format pair still rejects; the producer identity stands. Second half of AC-DLV-14-02. |
| `TestCompatibleReplayRepublishesExactBytes` | compatible replay | A build that reads the old version continues the pending obligations; IMPORT_SEALED replays the exact issuer identity and frame layout; the same-version rewrite passes and any other version is rejected `ErrPendingImmutable` naming the record and both versions; no compatibility decision rewrites the image. |
| `TestRollbackMatrix` | rollback | An old build that never knew the new version answers `ErrUnknownVersion` (no fallback); an old build that knows it read-only cannot replay it (`ErrReplayUnsupported`) and its write preflight is blocked until the build that understands the new version drains; after that drain the old version writes again; identity retained. |
| `TestStoreFormatRejectedPrewriteInEveryMode` | never-rule | The incompatible persistent store format is rejected prewrite (`ErrStoreTransitionRejected`) in both directions in LIVE with pending, DRAIN with pending, DRAIN empty, BLOCKED and DRAINED with empty payload (UPGRADE-01); the image never changes. |
| `TestRetainedLifecycleMetadataSurvivesDrainAndReplay` | retained metadata | The producer/run identity, the ACKed Applied reference of a saved activation (D59) and the retained future-GC pins survive compatible replay plus the old-build drain; a later event of the saved activation still plans against the retained reference; the store-format pair still never transitions. |

## Boundaries

The matrix starts from a given durable store image; native
admission/decision interleavings and producer total-store-loss belong to
E05-03.full_crash_matrix, and the executable user scenario
(stop-confirm-start/drain/switch/Resume/upgrade) to E06-08.maintenance.
The runtime AC verdicts of the public profiles stay with the acceptance
runs of E07; these tests are the engine-level evidence of the version
rules, not a public-profile PASS.

## Commands

```sh
cd test/compatibility_recovery
go build ./... && go vet ./...
go test ./...        # add -race for the race-detector run
gofmt -l .           # empty output means formatted
```
