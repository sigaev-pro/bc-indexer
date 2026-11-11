# Состояние проекта

## E01-02.admission — Guard, pin and ADMITTED seam (issue #16)

Status: IN_PROGRESS; block 0 admission-core ACCEPTED in PR #146 at merge `9046de9`. Dependencies E01-02.pins (#13),
E01-02.inputs (#14) and E01-02.owned_capture (#15) are accepted. Plan agreed
with the owner on 2026-11-11, the same split as #14 under D97 and D98: block
0 core (architect, then native-engineer), then up to three parallel
per-client blocks (native-engineer each) with qa for the AC matrix; reviewer
for the Spec and Standards reviews.

### Block 0: admission-core

Base `a73fcfc`. Branch/worktree: `claude/e01-02-admission-core-a0420b` /
`e01-02-admission-core-a0420b`. Allowed paths:
`adapters/{geth,bsc,bor}/admission/**`, `adapters/{geth,bsc,bor}/source_inputs/**`
(one accessor, `Log.Closed`, and the README line on the binding),
`test/harness/admission.py`, `test/harness/test_admission.py`,
`test/harness/admission.mutants.json`, `test/harness/fixtures/admission_core/**`,
`.project/evidence/E01-02.admission.md` and this section. Profiles: none;
client-independent unit tests on an in-memory power-loss store model, so
every AC-CAP-02 profile cell stays NOT_RUN and the block gives no AC verdict.

Design (architect), in full in the package README
[adapters/geth/admission/README.md](../adapters/geth/admission/README.md):

- Shared stdlib-only package `admission`, byte-identical in the three
  clients except a reserved `native/` directory for the client blocks. It
  reaches the pin ledger and the input log through two interfaces (`Pins`,
  `Inputs`), so it compiles in every native module graph.
- Records per observation under `extractor-admission-v1/<producer>.<run>/`:
  a durable counter (commit point of `Begin`, INPUT_PREPARING), then
  ADMITTED (commit point of the closing admission sync, after the pin and
  the sealed capsule verify), then EXEC_READY, and ABANDONED as the only
  cleanup of INPUT_PREPARING. The observation key is also the pin ID and the
  `source_inputs` ID.
- Real EXEC_READY binding (D98 item 7): `ExecReady` writes its record only
  after the durable closed consumed manifest of the same capsule is read and
  its digest recomputes; the EXEC_READY digest covers the admitted digest,
  the consumed digest and the record-corpus digest.
- Modes: `Open` starts in RECOVERY_ONLY; `GoLive` and `Begin` are refused
  while an observation is in INPUT_PREPARING or ADMITTED, so an ADMITTED
  obligation keeps its pin and cannot be passed over by a new H.
- Exits before EXEC_READY: `Abandon` for INPUT_PREPARING and `Reject`
  (NATIVE_REJECTED, with the digest of the native rejection evidence) for an
  ADMITTED observation that native validation refused. Both release the pin
  and advance the low-water mark; only this package writes its keys.
- One observation guard with tokens; nested native calls pass the token.
- Shared canonical JSON rule (D99 item 3): integers only, kept as written;
  sorted keys; minimal escaping; Go and Python agree byte for byte.

### Plan for the per-client blocks

The three blocks start after block 0 merges and are independent of each
other. Each installs `source_inputs`, `admission` and its wiring under
`extractor/` in the accepted native tree (module path
`github.com/ethereum/go-ethereum` in all three), adds its native patch set,
and records private local evidence; public cells stay NOT_RUN for E06 and
E07 (D96, D98, D99). Family-only files go under
`adapters/<client>/admission/native/` or `test/harness/fixtures/`.

| Item handed to admission | Source | Block |
|---|---|---|
| Guard/pin/ADMITTED contract, crash points of AC-CAP-02-01 and AC-CAP-02-02 (unit form) | TASK_DECOMPOSITION card | 0 |
| Real EXEC_READY binding of the consumed-manifest digest | D98 item 7 | 0 (core and binding test); every client block on its import path; Bor credits AC-CAP-04-03 with qa |
| Shared canonical JSON rule, with the BSC fork-id recompute | D99 item 3 | 0 defines it; each client block moves its genesis and config items to it; BSC drops the key-suffix recompute |
| Retention patch: `PruneState`, `RecoverPruning`, `RequireRewindAllowed`, `FreezeLimit` called from the shipped binaries | D98 item 2 | Geth, BSC, Bor |
| VM safe-point patch: the interpreter calls the `core/capture` safe point | D98 item 4 | Geth, BSC, Bor |
| Product import path and per-block observations (Begin, pin, seal, Admit, log, ExecReady at the native boundary) | D99 technical decisions | Geth (before `processor.Process`), BSC (import seam and `Parlia.applyTransaction`), Bor (`processBlockForImport` to `ProcessBlock`) |
| AC-CAP-02-01 and AC-CAP-02-02 per network, private local | TASK_DECOMPOSITION card | Geth (ETH), BSC (BSC), Bor (POLY, with consumed Heimdall inputs in the obligation); qa owns the matrix |
| Bor post-Rio ancestor rule (`getVeBlopSnapshot`); log or bound the latest-span and status calls | D99 item 1 | Bor |
| BSC reads below the window (`FindAncientHeader`, `GetFinalizedHeader`): extend the window or fail closed | D99 item 2 | BSC |
| BSC parent-state completeness: replace `CheckParentState` with a workable per-block check | D99 item 2 | BSC |
| Parlia system-contract calls (`ethAPI`) | D99 item 2 | BSC |
| Read order of concurrent Parlia reads of different keys; snapshot-key declaration without the fresh-engine assumption | D99 technical decisions | BSC |
| Window and checkpoint rule on the real import path | D99 item 5 | BSC defines and proves it (stored checkpoint snapshot above genesis, trusted-checkpoint path, window above a checkpoint); Geth proves the full 256-header BLOCKHASH window; Bor proves its checkpoint interval |
| Native validation failure after ADMITTED: call `Reject` with the rejection evidence, never `ExecReady` | Spec review 001 of block 0; NATIVE_CAPTURE_PROTOCOL NATIVE_REJECTED | 0 adds `Reject`; Geth, BSC and Bor call it on their import path |
| Closing EXEC_READY observations after the native decision, and the low-water mark past them, through a step added to this package (never by writing its keys) | TASK_DECOMPOSITION decision_bridge card | Not admission: E01-02.decision_bridge |
| Pin adapter: maps `state_provider` `ErrUnknownPin` to nil in `Release`, returns the ledger-record SHA-256 from `Verify`, tests an `Abandon` without a pin | Spec review 001 of block 0 | Geth, BSC, Bor |
| Recovery driver: at startup in RECOVERY_ONLY, re-execute each ADMITTED obligation from its pin and capsule (replaying the log), then `ExecReady` or `Reject`, then `GoLive` | AC-CAP-02-02 | Geth, BSC, Bor |
| Startup identity: refuse a native store of another identity and open the saved producer run, so `Open` sees its obligations (`Preflight` of E01-02.pins plus the run record) | NATIVE_CAPTURE_PROTOCOL startup | Geth, BSC, Bor wire it; the caller map and live boundary H stay with E01-02.lifecycle_seams |
| Normalization profile | D99 item 4 | Not admission: E04-01.block_record |
| Raw spool encoder | D98 item 4 | Not admission: E04-04.import_pressure |

#16 closes only when every client block is accepted and both AC-CAP-02
cases pass for every applicable private slot under the D96 boundary.

Implementation (native-engineer): the package above, `Log.Closed` in
`source_inputs`, the Python reference `test/harness/admission.py` with its
portable test and mutant table, and the binding test with the real
`source_inputs` log in `test/harness/fixtures/admission_core`. Evidence:
[E01-02.admission](evidence/E01-02.admission.md). Retained record 03 of
`9f6bbe4`: PASS; Go tests in all three copies and the assembled module, Go
mutants 115 killed and 2 labeled equivalent, `source_inputs` mutants 68
killed, Python mutants 35 and 11 killed. Records 01 (`648e8ed`) and 02
(`752b739`) passed and are superseded and kept.

Review 001 (head `648e8ed`): Spec REQUEST_CHANGES (P2: an ADMITTED
observation had no exit except EXEC_READY, so a native validation failure
would block admissions; P3: the pin adapter contract, an unchecked fetch
claim in the binding test, the recovery driver and startup identity owners)
and Standards PASS with P3 notes (dead test code, a placeholder value, `Mode`
without the guard, one Admission per producer). Commits `8a97f04` to
`752b739` fix all of them: `Reject` (NATIVE_REJECTED) with tests and
mutants, the counter re-read in `Begin`, `Mode` with the token, the fetch
assertion, and plan rows for the rest.

Review 002 (head `1ebedb3`): Spec REQUEST_CHANGES (P2: `Reject` released the
pin of an ADMITTED obligation before its record was durable) and Standards
PASS with two P3 wording notes. Commit `9f6bbe4` writes and syncs the
terminal record before the release, lets `Open` release the pins of closed
observations above the low-water mark again, and fixes the wording.

Next: none for block 0; it is merged (PR #146, merge `9046de9`). The Geth,
BSC and Bor blocks run in parallel sessions, each with its own section
below.

## E01-02.admission — Geth block

Status: IN_PROGRESS (part of #16). Base `9046de9`, after block 0 (PR #146)
merged. Branch/worktree: `claude/e01-02-admission-geth-95b3ee` /
`e01-02-admission-geth-95b3ee`. Role: native-engineer; reviewer for the Spec
and Standards reviews. Allowed paths: `adapters/geth/admission/native/**`,
`test/harness/fixtures/admission_geth/**`,
`.project/evidence/E01-02.admission.geth.md` and this section, plus the
block 0 status and "Next" lines above. The shared copies of `admission`
and `source_inputs` are unchanged. Profile: private local ETH fixture
(chain ID 31337) in the accepted Geth tree `1df30b18`; public cells stay
NOT_RUN (D96, D98, D99).

Scope: every Geth row of the plan table above. Design in
[adapters/geth/admission/native/README.md](../adapters/geth/admission/native/README.md):

- Product native patch set (`patches.json`, 20 anchored patches and two new
  files): `ProcessBlock` calls `Begin` (INPUT_PREPARING, pin, seal, closing
  ADMITTED sync) before `processor.Process`, `ExecReady` after validation
  and before the block is written, and `Reject` on any native validation
  failure; the import entry points take the observation guard before the
  chain mutex.
- Retention patch (D98 item 2): `geth snapshot prune-state` calls
  `PruneState`, startup calls `RecoverPruning` through the guard, requested
  rewinds call `RequireRewindAllowed` under the chain mutex, and the chain
  freezer uses `FreezeLimit`.
- VM safe-point patch (D98 item 4): the interpreter calls
  `vm.Config.ExtractorSafePoint` between VM steps; an execution with a safe
  point runs the table loop.
- Pin adapter, recovery driver (RECOVERY_ONLY, re-execute every ADMITTED
  obligation from its pin and capsule through the patched import, then
  GoLive), startup identity (Preflight before the store opens, Bind, the
  saved producer run record).
- Capsule items `chain-config` and `fork-id` in the shared canonical JSON
  form (D99 item 3); the capsule holds the full 256-header BLOCKHASH window,
  which native execution reads from the saved headers (D99 item 5).

Evidence: [E01-02.admission.geth](evidence/E01-02.admission.geth.md).

Draft PR #147. Review 001 (head `3304991`): Spec REQUEST_CHANGES (P2:
recovery moved the head, recovery did not check the static inputs, a bound
store started without a config; P3: Begin after the prefetcher, the
stateless self-check reached the safe point, an early evidence claim) and
Standards REQUEST_CHANGES (P1: `debug_executionWitness` reached the
admission without the guard; P2: recovery moved the head, the stateless
self-check, a 3600-second run deadline, no coverage of the engine import
path; P3 wording and docs). The next commit fixes all of them. Preparation
01 at `3304991` waited for the host lock and was stopped before it started,
because review 001 made it stale; its wait record is kept.

Review 002 (head `07fec51`): Spec PASS with one P3 (the preparation 01
wording) and Standards REQUEST_CHANGES (P2: `geth import` opened the chain
without `eth.New` and could import into a capture store; P3: the same
wording, a comment wrap). Commit `717cae6` refuses every state-writing
import into a capture store when no admission is installed, adds the
shipped `geth import` row and a mutant, and fixes the wording.

Evidence at `717cae6`: preparation 03 PASS (64 unit tests, 21 native mutant
builds), measured run 03 PASS (probe PASS, 21 of 21 native mutants killed,
432 seconds), Python mutants 64 of 64 killed. Records 02 at `d438376`
passed and are superseded and kept.

Resume point: review 003 of the head with records 03, hosted CI, then READY
for the coordinator.

## E01-02.admission — BSC block

Status: part A merged in PR #148 at `4bf65f3`, part B merged in PR #150 at
`10cd2c6`. The qa AC verdict for the private BSC slot is IN PROGRESS on
branch `claude/e01-02-admission-bsc-ac-verdict`, base main `10cd2c6`. Part
of #16.

Split: the coordinator approved on 2026-11-11 splitting the BSC block into
PR A "bsc-import" and PR B "bsc-parlia-inputs", both "Part of #16", with no
item deferred. PR B starts on main after the coordinator merges PR A.

Part A (PR #148) covers these plan rows for BSC: the product import path
and per-block observations (import seam, with the Parlia system
transactions of `Parlia.applyTransaction` inside the observation);
`Reject` on native validation failure; the retention patch (D98 item 2);
the VM safe-point patch (D98 item 4); the pin adapter; the recovery driver;
startup identity; the genesis and config items moved to the shared
canonical JSON rule, with the key-suffix fork-id recompute dropped; and
AC-CAP-02-01 and AC-CAP-02-02 private local for BSC (public cells NOT_RUN,
D96, D98, D99).

Part B covers the BSC Parlia input rows: reads
below the window (`FindAncientHeader`, `GetFinalizedHeader`), the
`CheckParentState` replacement, Parlia system-contract calls (`ethAPI`),
the concurrent read order and a snapshot-key declaration without the
fresh-engine assumption, and the window and checkpoint rule.

Design: [native README](../adapters/bsc/admission/native/README.md).
Evidence: [E01-02.admission.bsc](evidence/E01-02.admission.bsc.md).

Records at `e0b16c8` (2026-11-11): preparation 03, run 03, Go mutants 01
(60 killed, 2 labeled equivalent) and Python mutants 02 (70 killed) all
PASS. Earlier records are kept under D83: preparation 01 FAIL (harness
source changed during the run), the superseded `5f1aa7c` records, and run
02, cancelled before it started.

Part B design (2026-11-11, native-engineer, from a read-only map of every
input that a BSC block import reads):

- Header reads in an observation. Header verification, `Process`,
  `Finalize` and BLOCKHASH receive a scoped chain reader while an
  observation is attached. The reader serves only the capsule headers (the
  parent and the 256-header BLOCKHASH window). A read of any other header
  stops the observation, which stays ADMITTED (fail closed, never
  NATIVE_REJECTED). Readers on other goroutines (RPC, vote pool) keep the
  native chain and are never logged into the observation, except through
  the shared system-call backend (below).
- Logged Parlia snapshots. Through the scoped reader, Parlia gets each
  snapshot as a logged dependency response: the request is the block
  number and hash, the response is the snapshot's JSON. The first attempt
  computes it with the native engine (memory cache, stored checkpoint,
  header walk); later attempts get the saved bytes. The header walk from the
  stored checkpoint and `FindAncientHeader` at a validator-set switch run
  inside the native computation, so they are not observation reads. The
  attestation and finality-reward headers go through the reader, and their
  snapshots are logged responses. A snapshot is requested only for a capsule
  header. This replaces the logged `parlia-` key reads and the engine reset:
  retries get the saved bytes, whatever the engine holds in memory.
- Read order. One block per `insertChain`; header verification finishes
  before execution starts, and the logger serializes the requests, so the
  log order is the native order. Other goroutines do not use the scoped
  reader.
- `GetFinalizedHeader` and the fork choice run after EXEC_READY. They do not
  feed the execution result; the finality decision belongs to
  E01-02.decision_bridge.
- System-contract calls (`ethAPI`). They run in `Finalize` on epoch,
  turn-length and breathe blocks, and read the parent state by its hash.
  The probe wires a real `ethapi.BlockChainAPI`. Its backend refuses a
  state lookup other than the observed parent while an observation is
  attached.
- Parent-state completeness, replacing `CheckParentState`. `Begin`
  requires the parent state root, and the pin keeps the state. A state read
  error during execution (a missing trie node or code) is not
  authoritative: the observation stays ADMITTED, and is never recorded as
  NATIVE_REJECTED.
- Window and checkpoint rule. The capsule holds the parent and the 256
  BLOCKHASH ancestors, plus a declaration of the parent snapshot. A new
  private Parlia fixture of more than 1,024 blocks proves the rule on the
  real import path: epoch blocks with system-contract calls, a stored
  checkpoint snapshot at 1024, a window above that checkpoint, and the same
  logged snapshot bytes from a cold and a warm engine. The trusted-checkpoint
  path runs only after a walk of more than 600,000 headers without a stored
  checkpoint (`params.FullImmutabilityThreshold`). It runs inside the native
  snapshot computation, whose result is logged like any other snapshot. The
  coordinator chose a native proof (option A, 2026-11-11), with no scope
  moved to E06/E07. The `generate-trusted` preparation builds the
  601,251-block private chain, resumably, in a persistent directory outside
  the working tree, with a native home of blocks 1 to 601,250. The measured
  `trusted` run (1800 s deadline) copies that home, deletes the stored
  snapshots, and defeats the startup head verification. Its cold observed
  import of block 601,251 then takes the trusted-checkpoint path at block
  1200, inside the observation's logged lookup.
- Technical decision (native-engineer, not an owner answer): the finality
  result of `GetFinalizedHeader` and the fork choice is not an execution
  input. Both run after EXEC_READY, on the native home, where every header
  exists, so the silent finality 0 of D99 item 2 cannot occur there. The
  finality decision belongs to E01-02.decision_bridge.

Part B records (2026-11-11, at `7eb892c`): preparation 06, run 06, trusted
chain 03 and trusted 03 PASS (the trusted path was entered at block 1200
inside the observation), Go mutants 04 PASS (81 killed, 2 labeled
equivalents), Python mutants 06 PASS. Earlier attempts are kept under D83
and listed in
[E01-02.admission.bsc-parlia-inputs](evidence/E01-02.admission.bsc-parlia-inputs.md).

BSC AC verdict (qa, 2026-11-11, from the accepted records only: part A
run 03 at `e0b16c8` and part B run 06 at `7eb892c`). BSC private local:
AC-CAP-02-01 PASS and AC-CAP-02-02 PASS. All eight scenarios PASS in both
runs, and every public cell (`bsc-mainnet`, `bsc-testnet`) is NOT_RUN. The
matrix is in
[E01-02.admission.bsc-parlia-inputs](evidence/E01-02.admission.bsc-parlia-inputs.md#ac-matrix-bsc).

Resume point: none (not paused).

## E01-02.admission — Bor block

Status: IN_PROGRESS (Part of #16), draft PR #149. Base `9046de9` (block 0 merged in PR #146).
Branch/worktree: `claude/e01-02-admission-bor-b95e1c` /
`e01-02-admission-bor-b95e1c`. Roles: native-engineer (revision
`ed35bb98`) for the patches and the probe, qa (revision `4b479131`) for the
POLY AC matrix; two independent read-only reviewers. Allowed paths:
`adapters/bor/admission/native/**`, `test/harness/fixtures/admission_bor/**`,
`test/harness/bor_admission_probe.py`, `test/harness/bor_admission_tree.py`,
`test/harness/test_bor_admission_probe.py`, `test/harness/bor_admission_probe.mutants.json`,
`.project/evidence/E01-02.admission.bor.md` and this section. The shared copies
of `adapters/*/admission` and `adapters/*/source_inputs` stay unchanged.

Design (native-engineer), the Bor rows of the plan table:

- Install: the preparation copies `source_inputs`, `admission`,
  `state_provider` and `core/capture` under `extractor/` of the accepted Bor
  tree, and the Bor glue of `adapters/bor/admission/native/` to
  `extractor/borimport`. Native patches are anchored, exactly-once text edits
  and new files, listed in `native/patches.json` and recorded with the file
  digests before and after.
- Import path: one observation per block on `processBlockForImport` to
  `ProcessBlock`. Begin, pin of the parent root, sealed per-block capsule and
  the closing ADMITTED sync come first; then the Heimdall caches are purged so
  every Heimdall read of the block goes through the obligation's log; the
  header seal is verified again from the capsule ancestors and the logged
  span; then `ProcessBlock`. A native validation failure calls `Reject` with
  the digest of the rejection evidence; success closes the log and calls
  `ExecReady`. Preconfirmed results are not used while the observer runs, and
  a batch is imported one block at a time, so no header verification of a
  later block runs inside a window.
- Ancestor rule: before Rio, the parent and the headers back to the nearest
  1024-block checkpoint, with the stored checkpoint snapshot; from Rio on
  (`getVeBlopSnapshot`), only the parent header, and the span comes from the
  log. Latest-span and status calls inside a window are logged like spans and
  state sync pages.
- Retention patch (D98 item 2): the shipped `bor` binary routes `snapshot
  prune-state`, the startup `RecoverPruning`, `SetHead` and the chain freezer
  threshold through `PruneState`, `RecoverPruning`, `RequireRewindAllowed` and
  `FreezeLimit`.
- VM safe point (D98 item 4): the interpreter calls a tracer-scoped safe
  point between steps; the Bor glue maps it to `core/capture` `Reserve`.
- Pin adapter, recovery driver (RECOVERY_ONLY, re-execute each ADMITTED
  obligation from its pin and capsule, then `ExecReady` or `Reject`, then
  `GoLive`) and startup identity (`Preflight`, `Bind`, the saved producer run).

Review 001 (head `2b251ea`, before the measured evidence): Spec and
Standards REQUEST_CHANGES. P2 findings: recovery reported an input failure
or a failed Reject write as a rejection; capsule and store errors were
treated as native rejections; one Heimdall error stopped the window and the
node until a restart; the Heimdall client type was not checked; header
verification of other blocks (block fetcher, side-chain re-import) could
read Heimdall inside a window. The fixes: a rejection only when the
NATIVE_REJECTED record is written; native and other failures kept apart
(`Fail`, `Verify` returns both); source errors retryable through the log; the
next `Observe` resolves a saved obligation; `ExtractorCheckHeimdall`; a
window gate on `VerifyHeader`, `VerifyHeaders` and `VerifySeal`; the batch
split moved into `insertChainWithWitnesses`; and the P3 notes (README limits,
lock order, safe point owner, `go list -deps ./cmd/cli` check, batch imports
in the probe). Preparations 01 (failed at the probe link step) and 02 (stopped, superseded
by these fixes) on `2b251ea` are kept (D83).

Preparation 03 on `937d174` failed at the same probe link step: the
container's `/tmp` is a 256 MB tmpfs and the external linker writes its
temporary files there; the driver now sets `TMPDIR` to the build directory,
as the E01-02.pins harness does. Preparations 01 and 03 failed for this
reason, not for lack of host disk.

Review 002 (head `bcaa277`, evidence of preparation 04 and run 01 on
`cd9e2ef`): Spec PASS with P3 notes; Standards REQUEST_CHANGES (P2: recovery
re-execution rejected local-state errors that the live path keeps). Commit
`e103f18` fixes it (`ExtractorLocalStateUnavailable` in recovery), passes a
Heimdall error status to Bor as its native response (Spec P3), removes a dead
return value and corrects README and status wording.

Evidence ([E01-02.admission.bor](evidence/E01-02.admission.bor.md)):
preparation 06 and measured run 03 on clean `e103f18`, PASS (all nine
scenarios). POLY private local: AC-CAP-02-01 PASS, AC-CAP-02-02 PASS,
AC-CAP-04-03 real binding PASS; every public cell NOT_RUN. Mutants on
`e103f18`: Go 54 of 54, Python 86 of 86 killed.

Next: review 003 on the head with this evidence, then READY for the
coordinator.

## E01-02.inputs — Persist native inputs and consumed dependency responses (issue #14)

Status: ACCEPTED; #14 closes under D99 (2026-11-11) when this record merges.
Block 0 inputs-core merged in PR #137 at `a2fb163`; the Geth, BSC and Bor
blocks merged in PRs #138, #139 and #140. The coordinator closed #14 too
early on 2026-11-10 and reopened it the same day, because static manifest
items of NATIVE_CAPTURE_PROTOCOL were still open. The owner chose to finish
them inside #14 in three per-client blocks: Geth in PR #142 (merge
`4b1b3b0`), Bor in PR #143 (merge `40c1114`) and BSC in PR #144 (merge
`24e7a42`). Every static item now has a capsule item for each client. The
limits that the evidence leaves to later tasks (Bor post-Rio rule, BSC reads
outside the window and parent-state check, canonical JSON, normalization
profile, full windows and checkpoints) have owners under D99:
E01-02.admission, E04-01.block_record, E06 and E07. Every AC-CAP-04 public
cell stays NOT_RUN. Dependencies E01-02.pins and BOOT-09 are accepted (#13
closed under D98). Plan agreed with the owner under D97 and D98:

- Block 0, inputs-core: static input capsule and append-only dependency log;
  shared files are copied into each `adapters/<client>/source_inputs`.
  Roles: architect (design), native-engineer (implementation), reviewer.
- Then up to three parallel blocks: Geth, BSC and Bor (native-engineer each);
  qa owns the Bor matrix for AC-CAP-04-01 to AC-CAP-04-03 (POLY only).
- Bor evidence uses a deterministic in-process Heimdall stub; the counted
  calls are state sync events and spans; saved bytes are the raw response
  body at the transport boundary, before decoding.
- AC-CAP-04-03 binds the final consumed-manifest digest to a test-only
  stand-in marker; the real EXEC_READY binding is proven in E01-02.admission.

### Block 0: inputs-core

Base `bf17dbe`. Allowed paths: `adapters/{geth,bsc,bor}/source_inputs/**`,
`test/harness/source_inputs.py`, `test/harness/test_source_inputs.py`,
`test/harness/source_inputs.mutants.json`, `.project/evidence/E01-02.inputs.md`
and this section. Profiles: none; these are client-independent unit tests on
an in-memory power-loss store model, so every AC-CAP-04 profile cell stays
NOT_RUN. Evidence: [E01-02.inputs](evidence/E01-02.inputs.md).

Design (architect), in full in the package README
[adapters/geth/source_inputs/README.md](../adapters/geth/source_inputs/README.md):

- Store: the subset `Has`, `Get`, `Put`, `SyncKeyValue` of the native
  `ethdb.Database`; a write is durable only after a sync, and a power loss may
  keep any subset of unsynced writes. Keys are
  `extractor-source-inputs-v1/<observation>/...`; no key is 32 or 33 bytes, so
  the native offline pruner never deletes one.
- Records and digests: SHA-256 with domain tags. The capsule digest covers the
  ordered item names, lengths and data digests. Each log entry records the
  sequence, the request SHA-256 and length, and the response SHA-256 and
  length; a hash chain starting from the capsule digest binds the entries in
  order. The consumed-manifest digest covers the capsule digest, the entry
  count and the last chain value.
- Durability order: a response is written as chunks plus its entry, synced,
  then the advanced frontier is written and synced, and only then handed to
  native code. The synced frontier is the commit point; data beyond it was
  never handed out and is overwritten.
- Replay: `OpenLog` syncs first, so a frontier that is visible but unsynced
  after a process crash becomes durable before replay. Below the durable
  frontier, a matching request returns the saved bytes and never calls the
  fetch function.
- Prefix conflict: a different request at a durable offset is
  `ErrPrefixConflict`; nothing is written and the log stops.
- Tail append: at the frontier, the fetch function runs once and the new
  response is appended under the durability order above.
- Final consumed manifest: `Finish` requires that the attempt consumed every
  durable entry, writes and syncs the closed manifest, and afterwards refuses
  new responses while still replaying the prefix.

Review 001 (head `143518c`): Spec PASS and Standards PASS with P3 notes.
Commit `0fb3461` fixes them: the sync in `OpenLog` and on reseal, the
enforced chunk layout, store read errors passed through, and one more
mutant. Retained records and earlier attempts are in the evidence file.

Next: none for this block; it is merged. #14 closes under D99.

## E01-02.inputs — BSC block

Status: block ACCEPTED in PR #139 at merge `046d0a7` (part of #14). Base
`a2fb163`, after block 0 (PR #137) merged. Role: native-engineer (D98).
AC-CAP-04-01 to AC-CAP-04-03 apply to POLY only, so this block issues no AC
verdict. It is private local evidence under D96 and D98; public cells stay
NOT_RUN.

Allowed paths: `test/harness/bsc_source_inputs.py`,
`test/harness/fixtures/source_inputs_bsc/**`,
`.project/evidence/E01-02.inputs.bsc.md` and this section.
`adapters/bsc/source_inputs/**` was also allowed, but this block leaves it
unchanged.

Coordinator technical decision (2026-11-10), not an owner answer: BSC-only Go
sources live in `test/harness/fixtures/source_inputs_bsc/`, and the driver
copies them into the native tree. A BSC-only file in the shared package would
fail the shared-file check. The Geth and Bor blocks use their own
`source_inputs_<client>` directories.

Output: `ParliaDB` wraps the database handle that `parlia.New` receives, so
every Parlia read goes through the dependency log of the current observation.
A private native probe on the accepted BSC Parlia fixture shows:

- the capsule is synced before execution;
- each new response is durable before Parlia receives it (SIGKILL at the
  hand-off keeps it);
- resume and native-only replay hand Parlia the saved bytes. A tampered live
  snapshot breaks block 2 in the control but not in the replay.

Main `9d14e96` (the Bor block, PR #140) was merged into this branch at
`bd323fc`, as the coordinator asked. It adds only Bor files.

Review 001 (heads `3ae655e` and `bd323fc`): Spec and Standards both returned
REQUEST_CHANGES with no P1 finding. The findings were five P2 items (missing
controls, the record of an interrupted mutant, unstated boundary limits, the
replay wording) and P3 notes. Commit `0cce068` fixes the code and the
evidence file fixes the documents.

Evidence: [E01-02.inputs.bsc](evidence/E01-02.inputs.bsc.md). At `0cce068`,
preparation 02 and measured run 02 PASS. Go mutant record 03 killed 35 of 35,
and the Python driver mutants killed 98 of 98. The records at `943817f` and
`03e058b` are kept, including Go mutant record 01 (FAIL: 2 BUILD_FAILED
mutants from table bugs).

Review 002 (head `b577212`): Standards PASS with two P3 notes. Spec
REQUEST_CHANGES with one P2 on wording: the missing BSC capsule items (the
parent header, the parent state root and the ancestor headers) stay open work
of #14, not E01-02.admission. The evidence now says so.

Main `b7c2679` (the Geth block, PR #138) was then merged into this branch.
Remaining for #14 after this block: those BSC capsule items (done in
PR #144). The real EXEC_READY binding stays with E01-02.admission.

Next: none for this block; it is merged. #14 closes under D99.

The status line and "Next" above are for the merged block. A new block
follows.

Static manifest block (Part of #14): ACCEPTED in PR #144 at merge `24e7a42`.
Role: native-engineer. Allowed paths: the same BSC-only files as above. The
BSC capsule now holds every static item of NATIVE_CAPTURE_PROTOCOL: the block,
parent hash, parent header, parent root, the ancestor headers down to the
Parlia checkpoint and BLOCKHASH floor, the genesis, the EIP-2124 fork id, the
declared Parlia snapshot keys, native commit, normalization profile and state
profile. `ParliaDB` refuses a read of an undeclared key. Block 2 now replays
alone on a home that never imported block 1: its headers come from the
capsule, and its parent body and state come from the parent pin. A missing
parent state stops the replay before execution (`single-no-state`). Without
the capsule headers, the live import fails with `unknown ancestor`
(`single-no-headers`). The driver recomputes every item with its own
Keccak-256. On clean commit `186ae16`: preparation 07 PASS; run 07 PASS in
5.921 seconds with 36 Go tests; Go mutants 89 of 89 and Python 172 of 172
killed. Kept failed records: Go mutants 04 at `c4d2657` (3 build failures, 1
survivor; fixed in `646a918`), run 05 at `b8386c6` (a test sealed a duplicate
item, which the shared core refuses; fixed in `43b215e`) and Go mutants 07 at
`43b215e` (2 survivors on the parent header checks; fixed in `186ae16`).
Review 001 of PR #144 (head `e168017`): Spec and Standards REQUEST_CHANGES
with no P1; all P2 and P3 findings are fixed except two P3 notes kept in the
evidence. Review 002 (head `bb854f5`): Spec and Standards PASS with P3 notes;
hosted `bootstrap/ci` passed on that head. Next: none; the limits in "Still
missing" of the evidence have owners under D99.

## E01-02.inputs — Geth block

Status: block ACCEPTED in PR #138 at merge `b7c2679` (part of #14), after
block 0 (PR #137, merged at `a2fb163`). Role: native-engineer (D98 item 5).
Base `a2fb163`.

Static manifest follow-up (part of #14): ACCEPTED in PR #142 at merge
`4b1b3b0`. Base `1cebde2` (main with PRs #137 to #141). Same role and allowed
paths as below.

- Coordinator technical decision (2026-11-10), not an owner answer, following
  NATIVE_CAPTURE_PROTOCOL line 36: the BLOCKHASH window ancestors of a Geth
  block (blocks `max(0, N-256)` to `N-1`) are known before execution, so
  they are static capsule items (`parent-header`, `ancestor-header-<n>`).
  The dependency log keeps only responses unknown in advance; the Geth
  import of this profile has none, so its log stays empty and closes with
  count 0. This
  replaces the test-only BLOCKHASH routing through the log described below.
- Output: the Geth capsule holds every static item that the protocol lists:
  header and body RLP, an encodings descriptor, the parent header, hash and
  root, the window ancestor headers, the chain config and fork ID, the
  native commit and the normalization profile (a stand-in ID until the
  normalizer defines a product profile). The probe imports the block from
  the saved header and body bytes and answers every BLOCKHASH lookup with
  the Keccak-256 of the saved header. The oracle decodes and checks every
  item with its own Keccak-256 and RLP decoder.
- Review 001 (head `693994e`): Spec PASS; Standards REQUEST_CHANGES (one
  P2, an RLP rule without its own control). Commit `cdc81bf` fixes it and
  the P3 notes.
- Checks at commit `cdc81bf`: preparation 04 PASS (shared unit tests 20
  PASS inside the native module; eight native mutants built); measured run
  04 PASS (37.127 seconds of execution; the probe passes and all eight
  native mutants are killed); Python mutants 136 killed, 0 survivors; 48
  portable tests. Superseded run 03 is kept. Details, the decision and limits:
  [E01-02.inputs.geth](evidence/E01-02.inputs.geth.md).
- Next: none; it is merged. The limits in the evidence have owners under
  D98 and D99.

Allowed paths: `test/harness/source_inputs_geth.py`,
`test/harness/fixtures/source_inputs_geth/**` (coordinator technical decision:
Geth-only files cannot live in `adapters/geth/source_inputs/` because the
block 0 identity check compares every file of the three copies),
`.project/evidence/E01-02.inputs.geth.md` and this section. Shared core files
are unchanged. Profiles: none; private local Geth import with the ethash
faker, so no AC verdict (AC-CAP-04-0x are POLY only) and no public-profile
credit (D96, D98).

Output: a test-only probe built inside the accepted Geth tree runs the shared
core on the chain's own Pebble database. It routes the BLOCKHASH ancestor
lookup of the serial state processor through the log as a test-only
stand-in dependency; the product patch and the product classification of
Geth ancestor hashes belong to E01-02.admission (D98).

Review 001 (head `b8ece65`): Spec and Standards REQUEST_CHANGES. Commits
`5e7bda8` and `affd2f9` add a replay of a saved log that differs from native
truth, driver controls, two native mutants and the P3 fixes.

Checks at commit `affd2f9`: preparation 02 PASS (shared unit tests 20 PASS
inside the native module, five native mutants built); measured run 02 PASS
(15.170 seconds of execution; the probe passes and all five native mutants
are killed); Python mutants 95 killed, 0 survivors; 39 portable tests.
Superseded run 01 and the failed hosted CI run are kept. Details and limits:
[E01-02.inputs.geth](evidence/E01-02.inputs.geth.md).

Next: none for this block; it is merged. #14 closes under D99.

## E01-02.inputs — Bor block

Status: block ACCEPTED in PR #140 at merge `9d14e96` (part of #14). Base main
`a2fb163` (block 0 merged in PR #137). Roles: native-engineer (wiring), qa (AC
matrix and oracle), reviewer (Spec and Standards). Allowed paths: new
Bor-specific files `test/harness/bor_source_inputs_probe.py`,
`test/harness/test_bor_source_inputs_probe.py`,
`test/harness/bor_source_inputs_probe.mutants.json`,
`test/harness/fixtures/source_inputs_bor/**`,
`.project/evidence/E01-02.inputs.bor.md` and this section.
`adapters/bor/source_inputs` is unchanged, so the shared-file check still
passes. Coordinator technical decision: Bor-only probe sources live in
`test/harness/fixtures/source_inputs_bor/`, never under
`adapters/*/source_inputs/`; Geth and BSC use `source_inputs_geth` and
`source_inputs_bsc`.

Output: a private probe builds inside the accepted Bor tree and runs native
Bor consensus with the HTTP Heimdall client. A deterministic in-process
Heimdall stub answers through the process transport; state sync pages and
spans by ID pass through the shared dependency log, which saves the raw body
before Bor decodes it (D98). Evidence:
[E01-02.inputs.bor](evidence/E01-02.inputs.bor.md).

Review 001 (head `0cb6c70`): Spec PASS with P3 notes; Standards
REQUEST_CHANGES (P2: the driver's `prepare()` and `run()` had no tests or
mutants). `ab172ee` adds them, moves the fixture and fixes the P3 notes.

Result on clean commit `8203955`: preparation 02 PASS; measured run 02 PASS
in 5.473 seconds. Private local verdicts: AC-CAP-04-01 PASS, AC-CAP-04-02
PASS, AC-CAP-04-03 PASS with the test-only stand-in marker; every public cell
stays NOT_RUN (D96, D98). Mutants: Go 29 killed (`8203955`). Review 002
(head `eba6992`): Spec PASS with a P3 wording note; Standards REQUEST_CHANGES
(P2: four checks without a control). `170f85f` adds the controls and mutants
without changing the driver or probe code: Python 98 of 98 killed. The
earlier preparation 01 and run 01 on `a32bc67` also passed and are kept.

Next: none for this block; it is merged. #14 closes under D99.

Static manifest block (owner decision 2026-11-10, Part of #14): ACCEPTED in PR
#143 at merge `40c1114`. Roles: native-engineer (probe), qa (oracle), reviewer
(Spec and Standards). Allowed paths: the same Bor-only files as above. The Bor
capsule now holds every static item of NATIVE_CAPTURE_PROTOCOL: parent header,
parent hash and root, known ancestor headers, the effective chain config with
`stateSyncConfirmationDelay`, the EIP-2124 fork ID, native commit and
normalization profile, next to the genesis and the sealed blocks. The driver
computes every item independently with its own Keccak-256. The AC-CAP-04-02
fault now changes the delay after the seal; a new `control-capsule-items`
scenario refuses each tampered derived item and a changed chain config. On
clean `d128983`: preparation 03 PASS; run 03 PASS in 13.078 seconds
(AC-CAP-04-01 to AC-CAP-04-03 PASS on the private local profile, public cells
NOT_RUN); mutants Python 167 of 167 and Go 29 of 29 killed. Next: none; it is
merged. The limits in "Still missing" of the evidence have owners under D99.

## Review tooling that cuts review rounds (D97 process improvements 1-3)

Status: ACCEPTED in PR #132 at merge
`0de9dc129a2655fee8063c62ec1bea185027d2b0`. Owner-approved
process improvements 1-3, run in parallel with E01-02.pins and
E01-02.owned_capture under D97.
Most extra review rounds in PRs #128-#130 came from hand-copied evidence
numbers, hand-made mutation checks (one counted 10 compile failures as kills)
and a flaky controller test. Outputs:

- `test/harness/evidence_summary.py`: prints the figures an evidence section
  quotes from one retained run and fails closed on any reference mismatch.
- `test/mutate.py`: shared single-check mutation tool for Python and Go with
  tables next to the code (`*.mutants.json`). Mutants must compile first; a
  build failure is BUILD_FAILED and fails the tool.
- `.project/docs/REVIEW_CHECKLIST.md`, linked from DEVELOPMENT_WORKFLOW.
- Deterministic controller tests. The watchdog spawn test failed when the
  supervisor started after its own 0.3-second dispatch deadline (reproduced
  with a 0.5-second start delay: "supervisor never reached the spawn
  boundary", about 10 ms past the spin deadline). The signal test failed when
  Darwin returned a transient EPERM from `killpg` while the group leader was
  exiting, which `terminate_group` treated as a final denial. Retained probe
  `review-tooling-eperm-01-fc3a1ac` killed 300 sleeping children with each
  version: the `f8d3e67` code reported `cleanup-denied` once although the
  group exit was then confirmed; the fixed code reported `graceful` 300 times.
  `session_runner.py` now reports
  `cleanup-denied` only when EPERM persists to the grace deadline with live
  members.

Review 001 (head `2b76a0b`) returned REQUEST_CHANGES from Spec and Standards;
`076e74f` fixed the 0–5 sequence range (the lower bound is an exclusive
frontier), the Git provenance of reports from an extracted copy, timeout
cleanup of process trees (children in their own session), a false equivalence
label and the P3 items. Review 002 (head `fd5b64f`): Standards PASS with P3
notes; Spec REQUEST_CHANGES because a local runner D83 retry was printed as a
new lineage. The fixes report the retry's first run ID, SHA256 and reason,
verify the first run record when it is found among sibling runs, re-read the
process tree before SIGKILL, keep the Darwin CLD_STOPPED check with a control,
and fix the help text. After main gained the E01-02 blocks, the tool also reads
top-level `run-record.json` and `run.json` proofs and flat epoch phase marks.

The branch includes main at `667bf40` through merge commits: a rebase would
need a force push, which the permission check denied. Every PROJECT_STATUS and
harness README section from main is kept unchanged.

Retained record `.project/automation/runs/d95-20261107/review-tooling-05-55365b2`
(source `55365b2` extracted with `git archive`, its `record.sh`, Python 3.13.5,
go1.27.1 darwin/amd64; the tool, test and table files are byte-identical at the
merged head): `evidence_summary.py` summarized 40 retained runs with exit 0 (32
NATS image, NATS source, local runner and family preparation runs, owned-capture
runs 01-05 and the three pins runs). The eight owned-capture preparations fail
closed (exit 1) because their proofs cite files in a live worktree that later
changed; the seven mutation record directories have no run proof (exit 2). Its
30 tests passed with none skipped, including the four opt-in checks that
reproduce the figures of attempt 08, preparation 08, attempt 06 and runner run
04; `test_mutate` passed 27 tests. Mutation reports (killed, pre-labeled
equivalent): NATS driver 38 and 2 (matches record 09); `evidence_summary.py`
37 and 2; `mutate.py` 37 and 1; `session_runner.py` 6 and 0; no build failure,
unlabeled survivor or cleanup failure. The watchdog, signal and five EPERM tests
passed 25 of 25 verbose repeats beside 16 busy `yes` processes. Records 01-04
are retained and superseded. Local `.project/automation/ci.py` passed 685
tests in 54 files at the merged head.

Allowed paths: `test/mutate.py`, `test/test_mutate.py`, `test/mutate.mutants.json`,
`test/harness/evidence_summary*`, `test/harness/test_evidence_summary.py`,
`test/harness/fixtures/nats-image/mutants.json`, `test/harness/README.md` (its
own section), `.project/automation/ci.py`, `.project/automation/session_runner*`,
`.project/automation/test_controller_runtime.py`,
`.project/automation/test_ci_discovery.py`, `.project/docs/REVIEW_CHECKLIST.md`,
`.project/docs/DEVELOPMENT_WORKFLOW.md` and this status file. No adapter,
capture, NATS or runner production logic changes; no Docker or native run.

Base: `f8d3e67`, with main merged up to `667bf40`. Branch/worktree:
`feat/review-tooling` / `harness-review-tooling-288aea`. Next: Spec and
Standards review round 3 of the exact head, exact-head CI, a review summary
comment, then report READY to the coordinator, who merges.

## E01-02.pins — Native state retention and release (issue #13)

Status: ACCEPTED in PR #133 at merge
`667bf40e45327d01cc7a319f410c7808dfe2cb5e`. #13 will close after D98 merges
(D98 item 8), under the D98 boundary: private local native-seam evidence,
public cells NOT_RUN with E06 and E07. GitHub closed #13 early on the #133
merge; the coordinator reopened it. The block adds the hash-provider pin seam
`adapters/{geth,bsc,bor}/state_provider` (durable pin
ledger in the native key-value store; guards for the offline pruner, its
startup resume, head rewinds and the chain freezer's side-chain wipe; a
read-only store preflight; an effective-profile check) and the driver
`test/harness/pins.py`. Assigned ACs: AC-CAP-03-01, AC-CAP-03-02,
AC-OPS-01-02, AC-OPS-01-03, AC-OPS-03-02, covered on private local fixtures
only; every public-profile cell stays NOT_RUN.

Executed source `9a842ec` from a clean detached worktree: preparation 02
passed in 1,380.252 seconds, measured run 01 passed all three families in
63.515 seconds with complete cleanup, and Go mutation record 02 compiled all
67 mutants and killed 64 (three survivors labeled before the run). Details,
hashes and the retained failed or stopped records are in
[E01-02.pins evidence](evidence/E01-02.pins.md). Spec and Standards review of
`9a842ec` returned PASS in round 3 with P3 notes (a missing-profile-result
control, a `source_commit` value check in a run test, the `FreezeLimit`
comment for a zero limit, the preparation attempt-index meaning). The branch
then merged main up to `d40c6ea` (PRs #134, #131 and #135); the merges changed
none of the block's executed files and no harness file the driver imports. The
reviewed merged head passed exact-head CI and merged as PR #133. Next: close
#13 after D98 merges.

Historical base of the block: `d40c6ea` (main, merged into the branch).
Branch/worktree: `feat/e01-02-pins` / `e01-02-pins-a2f23f`.

RESOLVED by D98 (was BLOCKED_DECISION): the closure boundary for #13. Its
ACs list all seven public profiles in `local-e2e` and `harness` modes, while
D96 keeps public profile environments with E06. D98 item 1 lets #13 close on
private local native-seam evidence, as D96 did for #12. The block gives no
public-profile credit, and every public AC cell stays NOT_RUN until E06 and
E07.

RESOLVED by D98, which assigns the patch to E01-02.admission (was
BLOCKED_DECISION): who adds the native retention patch. The accepted
native sources delete committed state, code or pinned headers through the
offline prune command and its startup resume, head rewinds (`SetHead`, crash
repair) and the chain freezer's side-chain wipe. This block adds a ledger
guard for each path (`PruneState`, `RecoverPruning`, `RequireRewindAllowed`,
`FreezeLimit`) and proves them against the native code in-process (the
freezer guard by unit test). Calling them from the shipped binaries needs a
native patch. E01-01.builds defines a source/patch identity, but its accepted
builds carry no patch, and spec v3 says the hash scheme without retention
patches is insufficient. Either E01-01.builds reopens to build patched
clients, or E01-02.admission (which first wires the seam into the real import
path) adds the patch set; this changes accepted-task sequencing, so it is the
owner's call. Until then AC-CAP-03-01 `pinned_state_pruning_attempt` is proven
in-process only, and the unpatched native `snapshot prune-state`, a manual
`debug_setHead` and side-branch pins older than the freezer threshold remain
hazards outside this block's evidence.

## E01-02.owned_capture — Owned capture seam with bounded memory (issue #15)

Status: first block ACCEPTED in PR #134 at merge
`4c2abb20b3ef42f4388e93403b323cfd5277ff7f` from reviewed head `14c315a`;
follow-up block ACCEPTED in PR #135 at merge
`d40c6ea9b25894bf1b7e786d810eaa0bc511886a`. #15 will close after D98 merges
(D98 item 8), under the D98 boundary (ethash-faker BSC and Bor runs count
for the seam; native
consensus, public slots and RunManifests move to E06 and E07; the safe-point
patch goes to E01-02.admission and the raw spool encoder to
E04-04.import_pressure). The block delivers the
dependency-free Go module `core/capture` (immutable frames,
BEGIN_FIELD/CHUNK/END_FIELD with sequence, offset, length and SHA-256, one
fixed byte budget, safe-point waits that a callback can never enter, callback
notes paid from credit reserved at a safe point, fail-closed errors) and a
private native probe, `test/harness/capture_probe.py`, that runs the module
inside the accepted Geth, BSC and Bor block import. Executed source `cd60d53`:
preparation 08 passed in 467.473 seconds (unit tests with the race detector in
the pinned compiler, 100.0% coverage) and measured run 03 passed all three
families in 4.705 seconds of execution with complete cleanup. Five earlier
preparations, run 01 (a Bor prefetcher reached the probe safe point) and four
mutation records are retained as failures or superseded; see
[the evidence](evidence/E01-02.owned_capture.md). Review 001 of `d2c51cc`:
Spec PASS with notes, Standards REQUEST_CHANGES; the fixes are in `cd60d53`.
All seven public profile slots stay NOT_RUN and no AC verdict is issued.

RESOLVED by D98 (was BLOCKED_DECISION, needed before #15 could close):

1. Closure boundary for #15. The issue asks for every applicable mainnet and
   testnet profile, and the corpus lists AC-CAP-05-01 and AC-CAP-06-02 for all
   seven profiles in `local-e2e` mode. D96 set a private-fixture boundary only
   for #12. This block's probe uses one Ethereum-style genesis with the ethash
   faker for all three clients, so it runs each client's own interpreter and
   `InsertChain` but not Parlia, Bor consensus, system transactions or
   StateSync, and it writes no RunManifest. Options: a D96-style boundary for
   #15 (private local probe evidence accepted for the seam, public slots
   NOT_RUN with E06/E07, and whether the ethash-faker BSC and Bor runs count
   for those families), or #15 stays open until family-native fixtures and
   per-AC RunManifests exist.
2. Owner of the product VM safe-point patch. TASK_DECOMPOSITION gives the
   safe-point interface to E01-02.owned_capture and lists it as an input of
   E04-04.import_pressure (`test/native_import`), but no adapter task names the
   interpreter patch that calls it, or a wait inside the client's own block
   loop. The probe's interpreter line is test-only.
3. Owner of the raw spool writer. NATIVE_CAPTURE_PROTOCOL requires the
   BEGIN_FIELD/CHUNK/END_FIELD raw spool; this block provides the in-memory
   frames and leaves writing them to the worker. The module table assigns the
   spool to `core/capture` and `core/normalization`. Confirm that the spool
   encoder belongs to a later task (for example the AC-CAP-06-01 owner in E04)
   and name it.

Mutation record 05 of `4f1154a` compiles all 90 mutants and kills 89; the
survivor is equivalent.

Follow-up block (branch `feat/e01-02-owned-capture-followup` from
`4c2abb2`): corrects the shared-host window in the evidence (the pins
session's corrected window overlapped preparation 08 but not run 03), closes
the P3 notes of the final review (`capture_probe.py` blocks stop signals before
the output exists and reads the oracle once; accurate wording for `4f1154a`)
and marks the E01-01.harness section accepted. Because `capture_probe.py`
changed, measured run 04 ran preparation 08 with the tool at `93c7470` and
passed all three families in 6.147 seconds of execution. Review of `93c7470`
returned Spec and Standards PASS; Standards reproduced a remaining stop window
at the start of `finally`, now closed by retrying the record write. Measured
run 05 of that tool at `4da6fec` passed all three families in 5.918 seconds of
execution with complete cleanup. The re-reviewed head passed exact-head CI and
merged as PR #135. Next: close #15 after D98 merges.

Base: `f8d3e67d2aa936adaa69316ed268523a549c7e93` (first block),
`4c2abb20b3ef42f4388e93403b323cfd5277ff7f` (follow-up).
Branch/worktree: `feat/e01-02-owned-capture`, then
`feat/e01-02-owned-capture-followup` / `e01-02-owned-capture-157133`.

## E01-01.harness — NATS settle check uses pinned raftz names

Status: ACCEPTED in PR #130 at merge `f8d3e67d2aa936adaa69316ed268523a549c7e93`
from reviewed head `7edd9e0` (2026-11-09T22:15:35Z). This was the last open #12 item: the PR #128 follow-up
in which the Go `settled()` polling filter read raftz `catchup` and
`write_error`, while pinned NATS (`server/monitor.go` `RaftzGroup`) names them
`catching_up` and `wal_error`. Executed source `ed723a4`; retained mutation
record 09 compiles all 40 mutants and the tests kill 38, with two pre-labeled
equivalent survivors. Preparation 08 passed in 56.826 seconds and measured
attempt 08 passed both topologies in 9.064 seconds with complete cleanup.
Issue #12 closed under D96 on 2026-11-09T22:15:49Z after PRs #128, #129 and
#130; E01-02 has started.

Base: `c60591bba17fce65e02af4f577146e2374b938bf`.
Branch/worktree: `feat/e01-harness-raftz-fields` / `e01-harness-raftz-fields`.

Accepted prior block: PR #129 (local native-seam runner, D96) is accepted at
merge `c60591bba17fce65e02af4f577146e2374b938bf` from reviewed head `b07e424`,
after exact-head CI run 42688111514 and final Spec and Standards PASS. Executed
source `f2b351e`; measured run 04 record
`d60abc8f86dea93b675730d7c00808f2d51894de4fc51094a1170e65e6ddd919`.

## E01-01.harness — Unified local runner and runtime admission (D96)

Status: ACCEPTED in PR #129 (see the section above). Review 001 (head
`36b1550`) returned REQUEST_CHANGES from Spec and Standards. The owner confirmed
the D96 boundaries: local supporting services and Polygon own-L1 inputs move to
E05-01, the harness writes no RunManifest, and the first `run_started: true`
reader boundary belongs to E03-11. Executed source `b338c60` makes the runner
fail closed (lock, signals, always teardown and record), scopes cleanup to its
own containers, takes phase deadlines from the plan and adds negative controls.
Review 002 found a run record that could be skipped and untested repair paths;
executed source `b04ff76` always records the run, blocks signals while stopping a
child, fails cleanup on foreign harness containers and adds the missing controls.
Review 003 found a FAIL record written into a pre-existing output and a late
signal that could escape the final write; executed source `f2b351e` writes the
partial record only into an output this admission created, restores handlers
before unblocking signals, and adds the missing controls. Measured run 04 passed
all three families in 33.011 seconds of execution with clean teardown. Remaining #12 items after this block: the Go `settled()` raftz
field-name follow-up from PR #128. Next: exact-head CI, fresh Spec and Standards
reviews, protected acceptance.

D96 sets the #12 closure boundary on private local native-seam fixtures; public
profile environments stay with E06 and all seven public slots remain NOT_RUN.

Accepted prior block: PR #128 (unchanged-image NATS fence) is accepted at merge
`e2113d7356000b689240e8a0005b80357867ee36` from reviewed head `a00c7b5`, after
exact-head CI and final Spec and Standards PASS. Executed source `599521b`;
passing attempt 06 proof `e7442a9b42e6922e319c19d0e628d95cdf17f7829444e0ac63953c0fc216e301`.
The Go `settled()` raftz field-name follow-up remains open inside #12. Issue #12
remains open; no dependent task is unlocked.

Base: `e2113d7356000b689240e8a0005b80357867ee36`.
Branch/worktree: `feat/e01-harness-local-runner` / `e01-harness-local-runner`.

## E01-01.harness — Prove the unchanged-image NATS fence

Status: ACCEPTED in PR #128 (see the section above). Executed source
`599521b3de0ba8a4fe87644c2527bbe17d4b5892` prepared successfully in 48.913
seconds; driver tests run with the pinned Linux compiler passed (14 methods,
eight handshake subcases). Measured attempt 06, linked to FAIL05, passed both
declared topologies in 10.350 seconds: standalone and R3 each stored and
exported five messages (sequences 1–5), and R3 covered groups
`APP/S-R3F-c1Dvc0Km` and `SYS/_meta_`. Before the fenced request, all three
voters reported leader `image-S-2` for both groups at term 2. Both owned
commands exited zero with complete cleanup and absent process groups; no
exact-label container or native process remained. The author revalidated 545
unique absolute artifact references (14,558,731 bytes). Only documentation
and Python test changes follow the executed source; Python tests are not part of
the prepared tool. Review 006 returned Spec PASS and Standards REQUEST_CHANGES
for test coverage only. New Python controls kill 34 of 36 reviewer mutants of
the readiness, round and configuration checks; the two survivors are equivalent.
Follow-up outside this block: Go `settled()` reads raftz `catchup` and
`write_error`, which pinned NATS names `catching_up` and `wal_error`, and several
Go agreement/precheck lines lack direct controls. The independent Python
frontier check enforces those properties; a Go fix needs a new preparation and run. Runtime admission stays false; public
profiles and the remaining issue #12 criteria stay open. Next: exact-head CI,
fresh independent Spec and Standards reviews, and protected acceptance.

Historical leader-readiness checkpoint `599521b`:
Measured run 05 at `1d933c3d05e8ba683e2c5d2df3f228fb739eac08` FAILED. Preparation
05 passed in 53.885 seconds. Standalone passed; R3 restarted all three voters
and returned the exact publication denial, but the fenced STREAM.INFO request on
`image-S-1` received no reply and the driver stopped with a read timeout. The
retained "before" collection shows the restarted stream group with no leader on
all three voters (term 1), while the metadata group already had leader
`image-S-2`. The server logs show the stream leader elected at 14:40:56.244 UTC,
just after the request was sent. The driver's settled predicate compared the
leader field for equality only, so three empty leaders passed. Cleanup was
complete and no native process or owned container remained.

The repair requires a nonempty Raft leader that every voter reports, exactly one
voter in LEADER state with that identity, and FOLLOWER state on the others,
for both the metadata and stream groups, before the fenced API request. The
independent Python frontier predicate already required exactly one recovered
leader. A new Go control rejects the empty, unknown, disagreeing and wrong-role
leader cases; five of its six cases passed the old predicate.

Historical review-005 checkpoint `1d933c3`: the independent Python checker now
validates every retained readiness sample between CREATE and the first
publication, requires complete voter-ordered polling rounds, rejects an earlier
complete round and freezes only the final complete round. New Go and Python
negative controls kill every single-check mutation of the readiness and
recovered/sealed configuration checks except one redundant round-size check.
Read-only replay of the retained attempt-03 and attempt-04 native reports passes
the repaired checker without changing either proof. The runner links only
failed predecessors, so run 05 started a new lineage (`attempt_index` 1).

Historical attempt 04: executed source
`e2f92111262a59f6096f515b1a73785dab4af630` prepared successfully in 70.450
seconds; driver tests run with the pinned Linux compiler passed (12 methods,
eight handshake subcases). Measured attempt 04, linked to FAIL03, passed both
declared topologies in 10.562 seconds: standalone and R3 each stored and
exported five messages (sequences 1–5), and R3 covered groups
`APP/S-R3F-xXZsijp4` and `SYS/_meta_`. Both owned commands exited zero with
complete cleanup and absent process groups. The author revalidated 533 unique
absolute artifact references (14,444,100 bytes). Attempt 04 has no outer launch
record next to its run directory; its inner proof retains the command records.
That proof remains retained unchanged.

Historical attempt-03 repair checkpoint (superseded): the
source-confirmed API/JSz metadata correction after measured attempt 03.
Preparation 03 passed in 65.534 seconds. Standalone
native execution completed, but independent configuration comparison rejected
API-only version/level metadata absent from JSz. The full measured proof remains
FAIL; R3 was NOT_RUN. All resources were cleaned, and no new native work is active.
The correction validates exact metadata per surface and preserves every other
configuration field. It also bounds initial all-voter materialization polling
before publication; that startup race was found in source, not an actual R3 run.
Forty-one affected Python methods and 12 Go methods plus eight handshake subcases
pass. Read-only replay accepts the retained standalone bytes without changing
the failed proof. Next: publish, prepare a new tool, then run attempt 04 linked
to FAIL03. Driver/oracle changes require fresh preparation.

Historical attempt-02 repair checkpoint: forty affected
Python methods and 11 host Go methods, including eight handshake subcases,
pass. Every connection now has its own role-scoped inbox; retained wire bytes
bind all requests, replies and INFO fields to the selected process/config.
The exact pinned standalone no-Raft response is recognized without accepting
generic 404 responses or missing R3 groups. That checkpoint was published before
preparation 03 and measured attempt 03. No successful unchanged-image fence or
public-profile runtime admission is claimed.

Historical attempt 01: source `4645131abfd20a799ea74bb672cc0ded09f88a3f`
prepared successfully with the pinned compiler in 58.348 seconds. Measured
attempt 01 failed in 1.656 seconds before standalone publication because the
driver returned at PONG without consuming the native post-CONNECT INFO. R3
remained NOT_RUN. The original proof, tool and source are retained unchanged;
native processes, owned process group and exact-label containers are absent.

The first checked repair closed retained artifact joins, immutable snapshot byte
bindings, strict configuration types and the positive bounded retry counter.
That checkpoint passed 37 Python methods and eight host Go tests, including
eight handshake subcases. Those results remain historical evidence.

Historical attempt 02: published `181a91c73597596b9e924208ee9e5daced3547a2`
prepared successfully in 63.897 seconds. Measured attempt 02 passed the handshake
and received all five publication ACKs, then failed at seal because the manager
and publisher shared a wildcard inbox subscription. R3 remained NOT_RUN; cleanup
completed. Both preparations, tools, failures and the unchanged plan are retained.
Fresh driver preparation is required before another measured attempt.

The first published checkpoint `98120a100b66ca8bbd1f0249d2bc857e1afe94a9`
had one confirmed review defect: it allowed overlapping voters to share a PID
with different start ticks. Both generation controls reproduced that failure;
the new checkpoint requires distinct live PIDs while retaining valid later
reuse. Earlier checkpoint results remain historical, not final acceptance.

PR #127 is accepted at merge
`522eebc5155e655a7556bf1163376bedf88ddc42`, with both independent reviews and
exact-head CI passed. Its five source-native cases and retained failed attempt
are accepted partial evidence. Earlier readiness statements below are historical.
Issue #12 remains open; no product or public-profile gate is unlocked.

Scope retained from the original handoff: implement the bounded standalone/R3 slice using the unchanged pinned NATS
image and executable. Bind every framed publication/ACK, old child exit,
same-store restart with publishing disabled, complete native group collection
and exact interval export. Use a separately pinned driver and accepted offline
inputs. Commit the checked source checkpoint before native preparation or runs.
The repaired image-fence outcomes are NOT_RUN. Root owns publication and acceptance;
the assigned author owns this worktree. No generic controller or public work.

Base: `522eebc5155e655a7556bf1163376bedf88ddc42`.
Branch/worktree: `feat/e01-harness-nats-image-fence` /
`e01-harness-nats-image-fence`. Sole writer: `/root/boot04_preflight`.

## E01-01.harness — Execute native NATS fence conformance

Status: READY_FOR_REVIEW for actual source-native conformance. Executed source
`125439f7a040e491934fdd3960b8f87282624854` prepared successfully in 56.274827659
seconds. Measured attempt 2 passed all five declared native cases in 9.403554072
seconds. The author revalidated 292 unique artifact references, every case
report, the old/new tool pins and the retained first failure. All eight commands
in preparation 2 and run 2 report cleanup and process-group absence; parent PIDs
and exact `extractor.harness` container labels are absent. Only documentation
changes follow the executed source. Next: final exact-head CI, both independent
reviews and protected acceptance of this partial block.

Coverage is one standalone storage barrier and four R3 cases. Standalone
acknowledgment loss, unchanged-image native process restart, complete group and
ingress census, the full terminal fence and public-profile evidence remain
unfinished. Runtime admission stays false; issue #12 remains open.

Historical attempt 1 remains FAIL with its original hash. Its delayed-apply
collection was premature; the independent predicate correctly rejected it.
The corrected native wait preserves that strict predicate, and attempt 2 links
the failed proof and separately pinned old/new tools against the same accepted
build/source. No failed result was replaced or relabeled.

Historical source review correction: the 16 focused
tests pass in 0.120 seconds after reproducing 14 failing regression subcases.
Delayed-apply observations now require complete typed membership and health,
ACK JSON rejects duplicate keys, and native test listings use the authenticated
buffer. Normal frontier acceptance remains strict. Checkpoint
`32472e0a9442a01c2cca76a7a077fc96e5bb8372` passed hosted CI and source-only Spec
review, but Standards rejected delayed-apply validation, duplicate ACK keys and
test-list buffer binding. That rejected checkpoint and review remain in history.
No native preparation or execution has started. Next: publish the correction,
obtain independent readiness re-review and exact-head CI, then the separate
native preparation/execution handoff. Issue #12 remains open.

Historical source checkpoint: sixteen
focused NATS tests passed in 0.106 seconds. Preparation, native compilation and
all five native case outcomes are NOT_RUN. The source checkpoint must be
published and reviewed before its separately authorized execution handoff.

The NATS input-inspection and one-group observation
predicate are accepted in PR #126, merge
`d7657376f551f78acad8308b30e4cb0880b72a38`. Both independent reviews and CI
passed; both reviewers reproduced the read-only 84-reference inspection exactly.
Earlier READY_FOR_REVIEW entries are historical. No NATS runtime proof exists yet.

The checked source adds a bounded preparation/run CLI and an in-package overlay
against the accepted NATS commit and offline closure. It covers standalone
storage and four R3 cases: pre-proposal publication, committed-but-unapplied work,
lost acknowledgment and leader change. Preparation excludes only the copied
root server package's upstream test files, records their hashes, and verifies all
other production inputs remain unchanged. Original trees and production binaries
remain read-only. Next: independent source review, hosted CI, then actual
preparation and measured execution with retained failures and cleanup.

The later unchanged-image process restart, complete collection/census and final
runtime fence and standalone acknowledgment loss remain separate obligations.
In-process server shutdown is not proof
of an old native process exiting. Issue #12 remains OPEN; no dependency unlock.

Base: `d7657376f551f78acad8308b30e4cb0880b72a38`.
Branch/worktree: `feat/e01-harness-nats-conformance` / `e01-harness-nats-conformance`.
Sole writer: `/root/boot04_preflight`; root owns publication and acceptance.

## E01-01.harness — Prove the NATS terminal fence

Status: READY_FOR_REVIEW for the first NATS input/comparator checkpoint.
Eight focused methods passed in 0.076 seconds. The public read-only inspector
authenticated 84 retained references and the exact NATS image executable in
4.425 seconds, with its implementation hashes retained in the inspection proof.
The frontier predicate rejects incomplete identities, changing terms/leaders,
unapplied work and unhealthy groups. It does not establish a complete fence.
Native overlay preparation, source conformance, ingress census and unchanged-image
process restart remain NOT_RUN. No runtime admission changed; issue #12 remains
open. The next checkpoint adds the isolated native overlay after publication.

Accepted prior block: the offline RunPlan block is accepted in PR #125, merge
`08de707807444528e14b9607eea4e1860adfd5cd`, after 17 focused tests, exact-head CI
and both independent reviews passed. Earlier pending statements are historical.
Its output remains offline: runtime admission is false and cases stay NOT_RUN.
Exclusive output ownership and the demonstrated concurrent-writer limitations
are explicit. Issue #12 is OPEN. GitHub briefly auto-closed it from a negated
closing phrase; the coordinator immediately reopened it and corrected the PR.
No dependent work was started.

Remaining NATS work: implement
source-native conformance at the required fault boundaries, and separately prove
unchanged-image process exit/restart and recovered frontiers. Start with ordinary
FileStorage publishing in standalone and three-voter configurations. Read
TESTING_CONTRACT104–114 and retain instrumentation/runtime distinctions. No
production capture, standardized delivery or public-profile acceptance is claimed.

Base: `08de707807444528e14b9607eea4e1860adfd5cd`.
Branch/worktree: `feat/e01-harness-nats-fence` / `e01-harness-nats-fence`.
One writer: `/root/boot04_preflight`; root owns publication and acceptance.
First checkpoint: reviewed source, deterministic fixtures/oracle and focused
tests, committed and pushed before native preparation. Reuse accepted inputs;
do not rebuild the five clients or change active locks. Finite test/resource
deadlines remain required; no arbitrary campaign stopping window applies.

## E01-01.harness — Define and validate shared RunPlan admission

Status: READY_FOR_REVIEW. PR #125 re-review of
`c7b7c51b91f88370dda1b4f0fc61abbe5947b22c` passed Spec but found one
remaining Standards defect: a regular directory substituted between creation
and opening could receive snapshots alongside unrelated files. The repair
rejects detectable initial-directory conflicts before writing and makes
exclusive ownership of the output parent and directory an explicit prerequisite.
It does not claim protection from arbitrary concurrent same-user replacements.
The pending-file and moved-directory final-link probes remain documented limits;
no runtime evidence or product durability guarantee changes. Two new public-seam
tests reproduced the conflict and a later identity substitution before the fix;
all 17 focused methods now pass in 3.984 seconds. Exact-head hosted CI and both
independent review axes remain required. Issue #12 stays open and every plan
still has `runtime_admitted: false`.

Historical first repair: PR #125 candidate
`b85f89768c1e6d54d0ecc876ebb3624617953ade` passed the offline Spec review,
but Standards reproduced publication defects: a late fsync error left a success
record; an output-directory replacement redirected writes; a replaced snapshot
was not rechecked; late durability work could exceed the validation budget; and
some lexical patterns accepted a trailing newline. The original rejected
candidate remains in history. The focused repair now uses held no-follow
directory descriptors, exclusive fd-relative writes, snapshot identity/byte
checks and final authority/deadline checks before atomic publication. Schema
lexical endings reject trailing newlines. All five defect classes were red
before correction and now pass; unrelated replacements are preserved.
No runtime or broader scope was added. All 15 focused methods passed in
3.841 seconds, including final-marker collision preservation and both file and
directory fsync failures. Commit and independent re-review follow; the earlier
nine-test result below is historical.

The offline RunPlan block has a closed version-1
schema and `check`/`freeze` CLI. It authenticates plan/registry/input bytes,
resolves exact v3 case identities, preserves partial coverage as NOT_RUN, and
checks finite source stops, public-network action boundaries and one standard
parent measured cap of 1,800 seconds. Matching fence declarations remain
unverified; missing ProfileManifest/runtime proofs remain explicit blockers.
Every report has `runtime_admitted: false`; no native work ran in this block.

Nine focused test methods passed in 3.489 seconds, including real-file and
publication-time mutations, unsupported/duplicate/stale identities, unauthorized
exclusions, public testnet/mainnet restrictions, budget limits and fence-binding
mismatches and portable schema references. An external Draft 2020-12
meta-validator is not installed and was not run. Next: checked commit, hosted CI
and independent exact-head reviews.
Issue #12 remains open; no dependency or acceptance gate is unlocked.

The three private native slices are accepted: Ethereum
PR #122, BSC PR #123 and Bor PR #124. Bor merged as
`78bb82be366ce66c0e13917717a8887f770acd89` after both independent reviews and
exact-head CI passed. Its 19-control campaign and fresh Ethereum/BSC compatibility
runs passed; all 24 command records and 13 exact owner checks confirm cleanup.
The earlier READY_FOR_REVIEW statements below are historical.

Complete ProfileManifest verification, NATS fence conformance, READY checks,
runtime scheduling and production capture remain separate unfinished outputs.

Worktree: `e01-harness-run-plan`; branch: `feat/e01-harness-run-plan`.
Root owns publication and acceptance. Record implementation, focused tests and
the exact remaining outputs before the next commit and draft PR.

## E01-01.harness — Build the private Bor server and IPC import slice

Status: READY_FOR_REVIEW. Draft PR #124 now has a complete private Bor result
on executed source `b25215c7c0152a984ad7157af598a3e37c4cce3c`: all 19
controls passed in 60.287208683 seconds. The actual server used the accepted
Bor binary, fresh genesis, genuine validator contract and local IPC import.
Corruption/prefix failures, oracle/identity checks, bounded state/receipt/log
comparisons, deadline and parent/process-group interruption controls passed.
All 11 command groups and exact-owner container queries are clean. The final
source passed 61 portable harness tests; source/document/whitespace checks pass.

Because the shared owner now flushes its bounded live output, compatibility
positives reused accepted Ethereum and BSC preparation: PASS in 4.534311259 and
22.616163411 seconds. All 13 additional command groups and both exact owners
are clean. No helper or native-client rebuild was needed for this correction.
The result checkpoint changes documentation only; executed source bytes stay
unchanged. Exact final-head CI, independent reviews and protected acceptance
remain the coordinator's next steps. Issue #12 remains open.

Preparation 3 on `6a5fc4996374bc3cc10597a0b5edd8c6741536eb` passed in
235.444298286 seconds; its external tool pin remains unchanged. The first
measured import passed in 5.241974693 seconds. Two failed preparations and the
first campaign's 14-control partial FAIL remain immutable in
[task evidence](evidence/E01-01.harness.md), with their causes and cleanup.
No earlier failure is relabeled. The independent oracle was fixed before
generation and has not changed.

The preceding private BSC block remains accepted in PR #123, merge
`8ebbf9b02b3825428623c50afef5b98173f895d2`. Earlier BSC pending statements
below are historical. All public profiles, the complete quick suite, lifecycle,
capture, NATS and later gates remain incomplete or NOT_RUN. Root owns publication,
review and acceptance; this block does not close the parent task.

## E01-01.harness — Build the private BSC Parlia import slice

Current checkpoint: the partial BSC block is READY_FOR_REVIEW with final native
evidence. All 19 controls on source
`dcf1a8ca58f60d5a7abb61fe2a9a5ca086e232df` passed in 164.508446058 seconds,
within the shared 900-second budget. The proof SHA256 is
`914c619096e81b74a68673fc43659d10777a948a5ac4f1f806f1c2abebe5b0ae`.
All 42 command results prove cleanup/group absence, and all 12 exact owner-label
container checks are empty. The campaign rechecked its original authority
references before PASS. All 44 portable harness tests passed; both independent
source reviews and source CI run `42506634925` passed. This final checkpoint only
updates evidence/status; executed harness bytes are unchanged. Final metadata
review, exact-head CI and protected merge remain pending.

Preparation 4 and its external observer-tool pin are reused with their original
source identity. Expected negative executions remain FAIL in their own proofs;
the campaign PASS means their specified rejection was observed. Earlier failed
preparations, failed imports and rejected driver revisions remain intact. Issue
12 stays open; public profiles, the complete quick suite, native capture and NATS
remain NOT_RUN. See [the evidence record](evidence/E01-01.harness.md) for exact
artifact identities and remaining scope.

### Historical BSC checkpoints

The following entries preserve the status at each earlier source checkpoint;
their pending statements do not override the current checkpoint above.

The first real 19-control campaign on `3e1e4cb5c3dbf112ca23c2b8635e8c56a820df13`
completed PASS in 138.263239408 seconds. Its SHA256 is
`f48040cb71637053a839269c1759acde9b3b77e091f7722c3455a5ab548cdbdd`.
All 42 command results recorded cleanup/group absence; independent exact-label
checks found no containers for any of its 12 owners. This remains historical
evidence because review found two driver defects: generic native-exit matching
could accept the wrong stage, and resource subchecks did not inherit/check the
shared campaign deadline at every dispatch.

The corrected driver verifies exact native command stage, exit and diagnostic.
A bounded virtual-clock regression reproduces the old late dispatch; each new
resource now requires execution/cleanup reserve and inherits a deadline clipped
to the campaign. Cleanup still finishes after interruption, with no subsequent
dispatch. Budget metadata is retained. All 43 harness tests pass in 9.309 seconds;
the final five focused controls pass in 0.229 seconds. One fresh 19-control
campaign is required on the corrected source. Helper, preparation and native
runtime behavior are unchanged; all earlier results remain intact.

The hot-store correction now has actual positive evidence on
`b6da76388f1e9da481437de769a68061a8b79056`. Preparation 4 passed in
229.347142061 seconds, preserving preparation 3 and its earlier results.
The new external observer-tool pin is
`4b515f3709cbff42c29e458fcd9ebbd734dc8d8d212d8709f70c65d1d6e16c4a`.
The measured BSC positive passed all seven native commands and the independent
comparison in 33.054069097 seconds; proof SHA256
`2457a84ff77753456ab9e6a95d9ee6cc896018dfb2a20a70553e0f6e9d908fc5`.
All owned groups/containers were absent. The explicit 19-control driver is now
implemented; its real campaign is pending. All 40 portable tests pass in 9.477
seconds. Helper/preparation/runtime bytes are unchanged by this control-driver
checkpoint. Public profiles, full quick, capture/NATS and issue 12 stay open.

The second measured BSC run on `ce1f3b2fd004b339c3a3418da80ca752e1a32e2c`
retained FAIL after 21.783390208 seconds: the lock-file exceptions worked, but
native freezer opening still requested write access to `headers.meta` despite
ReadOnly. All seven command groups and exact-owner containers were cleaned.
Proof SHA256: `829b35f5699c1b147fd0aeed5701490db6cfbbf624f990768a37e84270d9b801`.

The observer is now explicitly limited to this fresh private two-block hot store.
It uses native readonly KV plus the native no-freezer wrapper, requires complete
canonical genesis/blocks/receipts, and validates native transaction/receipt roots.
Only the existing empty hot-store LOCK gets a writable handle; all database data
stays read-only. Missing or freezer-only records reject. The helper change needs
a new external tool pin and preparation 4, linked to successful preparation 3
without changing that result or resetting history. All 38 portable tests pass.

Authentic BSC preparation attempt 3 on `b706987bf80a952ca03f52d6bc37025ea1003d97`
passed in 242.454577276 seconds. Public-key verification and genuine Parlia
preparation/assembly/sealing/self-import succeeded. Tool manifest external pin:
`c9a2779abd2c6facf25ab12ffb1256f9dc358107d8fa2b018feb6ee9f0a180fe`.
All four command groups/containers were absent. A fresh real Ethereum positive
compatibility run passed in 9.823902191 seconds with six commands and clean
resources. The first measured BSC run then passed its six native CLI commands,
but its read-only observer could not open Pebble's LOCK handle. It retained FAIL
after 27.900825399 seconds, with all seven groups/containers cleaned.

The runtime-only correction keeps DB data and native DB options read-only while
allowing writable handles for exactly the two existing zero-byte LOCK/FLOCK
files. Their bytes are checked before and after observation. The helper,
preparation and external tool pin are unchanged. The new boundary test failed
before implementation; all 37 harness tests now pass in 9.645 seconds. A new
measured BSC run is required. No full quick/public-profile/NATS result is claimed.

Follow-up review confirmed the immutable-buffer fix, then found that a manifest
replaced after capture could still leave an invalid reference in a PASS proof.
Four complete seven-command simulations reproduced this for tool, build, fixture
and request JSON. Both native runners now retain the request's authenticated
buffer identity and revalidate all required original authority/scenario/tool
references before PASS. Replacements produce retained FAIL results; unchanged
inputs still pass. All 36 portable harness tests pass in 10.554 seconds. This
repair and the pinned BSC header-number API fix precede the third preparation.
The earlier source-review failures and both preparation failures remain retained.

The second linked preparation on `be248e8bd6cb1f6ef3c4744636c753f921aca9ef`
passed module verification and the corrected scratch boundary, then compilation
identified the pinned BSC API's `(uint64, bool)` return from `ReadHeaderNumber`.
The helper incorrectly expected a pointer. Preparation recorded FAIL after
197.309844342 seconds; cleanup was complete and the exact owner had no remaining
containers. Proof SHA256:
`8f44fe640675ecb1b7dc13a2dcf6cf82b17c1c8462311426e168b4da18d858bd`.
The source-confirmed three-line fix retains the explicit missing-header rejection.
A new linked preparation is required; no key/generation/import success is claimed.

Independent source review rejected the earlier observer authority boundary:
the tool manifest was hashed and reopened for JSON parsing, allowing a file
replacement between those steps. The exact replacement was reproduced before
the fix. Shared bounded JSON primitives now authenticate and parse the same
immutable buffer. BSC tool/build/fixture/oracle/command admission and the
corresponding Ethereum JSON inputs use that boundary. Deterministic replacement
and malformed-JSON controls pass; all 35 harness tests pass in 9.207 seconds.
The review failure is retained. No second preparation or BSC import has run;
the next preparation will include both the compiler scratch and JSON fixes.

The first offline BSC preparation on `212810725e69978fd160e65034a7b1843a70d45e`
failed after 138.149319096 seconds. Native module verification passed, but the
larger BSC/CGO helper compilation exhausted the unchanged 256 MiB container
`/tmp`. No key verification, generation or import ran. Proof SHA256:
`8cc1b14f7dab76dccedac44b32f44aeb846c70d0ca221a839df53aa59037316f`.
Both owned process groups and the exact-nonce containers were absent after
cleanup. The failure remains immutable. The scoped correction assigns compiler
temporary files to owned `/work/build-temp`, with explicit `GOTMPDIR` and
`TMPDIR`; global native limits remain unchanged. Native dump address casing is
also normalized with duplicate/incomplete-address rejection. Its two new test
methods first failed, then all 32 harness tests passed in 8.917 seconds. The
next action is a new linked preparation attempt on the corrected source.

The first checked BSC checkpoint fixes the independent monetary/system-call
oracle and the native observation comparison boundary. Four new portable tests
exercise the positive shape and independent negative controls; all 26 harness
tests pass. The initial stub failed all four test methods before implementation.
BSC key verification, fixture preparation, CLI import and observer execution are
still NOT_RUN. The observer will use a separate caller-pinned tool manifest;
scenario edits cannot nominate an executable. No BSC runtime claim is made by
this checkpoint.

The next source checkpoint adds the separate caller-pinned observer tool,
offline BSC preparation and bounded import entry points. Four additional CLI
tests cover executable substitution and raw-byte/provenance rejection. Two
command-provenance counterexamples failed red before the fix; all 30 portable
harness tests then passed in 9.547 seconds. Actual BSC key verification, helper
compilation, Parlia generation and CLI import are still NOT_RUN. The next action
is one owned, finite offline preparation with retained failure evidence.

2026-11-08: The first local Ethereum harness block is accepted in PR #122,
merge `0a43d21b0a3877ff7b0c1f06f43a952b3a9c9142`. Both independent final
reviews and exact-head CI passed. The repaired native run passed 13 controls;
its index digest is `c3ddb69138cd795af6e7800ff52298de87050bc6679bcf291b5cc0e5e9038d28`.
All three review findings were corrected. The acceptance comment is
https://github.com/sigaev-pro/bc-indexer/issues/12#issuecomment-6723822587.

The next separate block stays within issue #12: explicit private BSC genesis,
test-only validator provenance, authentic Parlia preparation/sealing/import,
independent expected results and bounded owned execution. Use accepted BSC
build/source artifacts and preserve the Ethereum runner's accepted behavior.
The sole writer is `/root/boot04_preflight`, in worktree
`.project/worktrees/e01-harness-bsc-parlia` on branch
`feat/e01-harness-bsc-parlia`, based on the accepted merge above. Allowed paths
are `test/harness/**`, this status file and the existing harness evidence record.
Root owns publication, independent review and protected acceptance.

Start with a narrow feasibility check of the pinned public test keystore,
genesis/system contracts and native Parlia generation seam. Record the oracle
before measuring native execution. Preparation and measured execution remain
separate; measured runs have one deadline of at most 1800 seconds. Preserve
failed attempts, actual resource ownership and exact source/input bindings.
Run focused checks locally, publish checked commits promptly, and use mandatory
exact-head hosted CI for the full suite. Do not unlock dependent tasks from a
partial block. Issue #12 remains open; all seven public profiles, the full quick
suite, lifecycle, capture, NATS and later gates remain incomplete or NOT_RUN.

## E01-01.harness — Repaired source verified; final acceptance pending

2026-11-08: Both independent review axes requested changes to PR #122 at
`b9f9328d7052b412f795df4f58ab155f69ea2c87`. The earlier checks and native
diagnostics remain accurate history, but this candidate is not accepted. The
confirmed defects are guardian survival when the parent process group is killed,
unbounded actual artifact reads/copies despite declared limits, and unverified
reported genesis/block hashes. The author owns one scoped repair block with
red-before-green controls, aggregate deadlines, fresh frozen-source verification,
and retained failed-candidate history. Root owns publication and acceptance.
Issue #12 remains open; all public profiles, full quick coverage, capture, NATS,
and later stage gates remain NOT_RUN.

Repaired implementation `13d1a37c648bca25b7b3b65a3fda36890e7cab11` passed 22
focused portable tests, source/document checks, and fresh actual verification on
a clean unchanged checkout. Preparation took 67.080 seconds; the measured positive
native import/state/header/full-chain comparison took 7.236 seconds. All 13
controls passed, including separately reauthorized false genesis/block hashes and
real parent-group SIGTERM/SIGKILL. All 34 command groups and containers were
confirmed absent. Native identity uses exact JSON header observations at 0/1/2
with canonical decimal-string heights. The result-record update is docs-only.

Root owns final publication, mandatory hosted CI, both independent review axes,
and protected acceptance. The full suite was not duplicated locally before
publication. [The evidence record](evidence/E01-01.harness.md) retains the rejected
candidate, repairs, exact executed source and fresh proof hashes. No new adapter
or acceptance gate has been opened.

## E01-01.harness — First partial runner verified; independent review pending

2026-11-08: Implementation commit `e1c7821251d0f347a4c62245e7fa591c2af633c7`
adds the local Ethereum import CLI, raw-byte admission, independent oracle,
offline preparation, owned Docker execution, and 12 portable tests. Full local
CI passed 414 tests (two existing opt-in skips), 54 component negatives, and all
document/mirror checks. Initial preparation and process-ownership failures remain
retained with their corrections; none was relabeled as success.

Fresh verification on that clean commit passed: preparation 78.417 seconds,
measured native import/state/full-chain comparison 9.152 seconds, and all nine
real/admission/resource controls. All 18 owned command groups and containers were
confirmed absent. Draft PR #122 is under independent review and hosted CI; root
owns publication and protected acceptance. This result-record change is docs-only
and preserves the executed source bytes. All seven public profiles and the full
quick suite remain NOT_RUN; issue #12 is open.
[Evidence and retained failures](evidence/E01-01.harness.md) bind the exact
implementation, proofs, durations and owner nonces. No other adapter, collector,
Gateway, public network or stage-gate work has started.

## E01-01.harness — Reproducible local Ethereum import block

2026-11-08: Issue #11 is closed after accepted build PR #107 (`4149913f686344919478ee000f76019aab58a62f`) and result checkpoint PR #121 (`0388a975e008a1f6d8335c29687c086d8c975020`). Dependencies #9 and #10 are also closed. Root opens issue #12 from exact base `0388a975e008a1f6d8335c29687c086d8c975020` in `.project/worktrees/e01-harness-ethereum-import`, branch `feat/e01-harness-ethereum-import`. No runtime operation is active. The accepted five-client artifact manifest is `cb42ed6d004f1fe71d3d40f95a6a47a11aeb35fcb0211b553f42a62f4e1d1c39` from operation `run_D95_awake_builds_1794132505 / 4809d9872661eab78f1266c076`.

The first block is a partial local Ethereum native-import runner under `test/harness`: explicit prepared inputs, local genesis/state and fixture identity, retained block bytes, finite owned execution, and independently specified expected balances/nonces/gas/block results. The approved test boundary is the harness's public invocation through the native import interface and observable native output. Use the TDD loop at that boundary; no hidden prepared node, internal-only mocks or expected values generated by the implementation under test. Preserve the distinction between the selected public profile and the actual local fixture identity. Preparation remains separate from execution; the standard measured run stays within 1,800 seconds.

This block must reject mismatched artifacts/inputs, detect corrupt blocks and wrong expected results, and prove timeout/interruption cleanup. Do not use Geth's `--nocompaction` shortcut or infer success from exit zero alone. Preserve binary hashes over raw bytes and fit the accepted evidence/RunPlan interfaces without inventing runtime results. Keep all seven profile slots explicit; this initial Ethereum slice cannot close issue #12 or establish E06 ENV_READY, capture equivalence, NATS delivery or a broader E01 gate. No public transactions, production deployment, purchases or active-lock promotion are authorized by this block.

Root assigns sole writer ownership to `/root/boot04_preflight` in the QA / Native Engineer role. Allowed changes are `test/harness/**`, focused harness tests/fixtures and documentation, `.project/PROJECT_STATUS.md`, and `.project/evidence/E01-01.harness.md`. Read accepted task/contracts and pinned native source before choosing implementation details. Preserve the accepted build artifacts and existing product code. The writer must deliver a meaningful checked commit, push a draft PR and return ownership for independent review before acceptance. Report an essential out-of-scope change with evidence; routine technical choices within D84/D95 do not require renewed owner approval.

## E01-01.builds — Accepted; record the result before starting the harness

2026-11-08: PR #107 is accepted and merged at `4149913f686344919478ee000f76019aab58a62f`. The exact executed candidate `ccc31749ebe9403aced2d2e55361d1cf69e7bd07` passed both independent final artifact reviews and CI `42450702489`; verifier source was `22a101a8b582b842b9e470caec01dd3b6bd718c8`. Full operation `run_D95_awake_builds_1794132505 / 4809d9872661eab78f1266c076` passed in 4,789.182 seconds: five actual Linux amd64 client builds, ten isolated module closures, four authentic source/updater passes, all seven actual profile preflights and final materialized revalidation. All 457 native commands and 60 expected control outcomes passed. Result SHA256: `5b64c5b2ff5429c84bb899a9c4aec45473c5ef7a98ed7966bdc3210aab7ff667`.

Both reviewers confirmed the assigned build criteria. Spec independently replayed complete materialized validation in 107.070 seconds; Standards verified 12,884 direct artifact references. All owned processes, containers and the temporary idle-sleep assertion are absent. Prior failed runs remain unchanged, including the documented host idle-sleep interruption. The successful run used a finite utility-lifetime idle-sleep assertion; no permanent power setting changed. The [accepted build evidence](evidence/E01-01.builds.md) records hashes, exact review/CI/merge bindings and scope limits.

Root owns this separate documentation-only checkpoint. It does not change or relabel the executed candidate. Publish and accept this result record, then close issue #11 with its evidence and start `E01-01.harness` / issue #12. Dependencies #10 (`E01-01.evidence`) and #9 (`BOOT-09`) are closed; recheck their accepted artifacts when opening the harness worktree. The first meaningful proposed harness block is a reproducible local Ethereum import with explicit prepared inputs and independent expected results. Preserve all seven profile slots and the remaining harness requirements; a partial Ethereum result cannot close #12. Preparation stays separate from measured execution, and the standard test-run limit remains 1,800 seconds. Arm64 builds, repeat_build, native chain execution, capture and NATS delivery remain NOT_RUN. No broader stage gate, active-lock promotion or production deployment is accepted here.

This result checkpoint passes documentation validation: 118 files, 1,125 local links, 84 catalog tasks and 16 normative mirrors; original v2 is unchanged. Whitespace passes. Only project status and build evidence change; executable code and the accepted run remain unchanged. Publish this block and require its exact-head documentation reviews and CI before advancing.

## E01-01.builds — Accepted materialized preflight budget synchronized

2026-11-08: Root synchronized accepted PR #120, merge `22a101a8b582b842b9e470caec01dd3b6bd718c8`, into the product candidate. Final source head `93cefad1d1796b6d27247877192b849300268b8c` passed both independent reviews and CI `42450445072`. Its actual derivative diagnostic passed all seven profile preflights in 36.268–38.399 seconds, rejected a changed binary hash, preserved the original files and completed cleanup. Both artifact reviews passed. Result SHA256: `d4a3898618b98fb6810c63a638cee143273c6032945dfec49284eb70039997a5`.

The previous complete operation compiled all five clients and verified ten module closures, but remains failed at the first ten-second actual preflight. The accepted repair lets trusted actual checks use a fixed 300-second cap while small controls retain ten seconds; original deadlines, cleanup and artifact checks remain intact. The status-only merge preserves both histories below. Product code remains unchanged; trusted automation must match accepted main. Next: validate this integration, commit/push, confirm exact-head reviews and CI, and run a fresh complete operation. Require all source/updater passes, five native artifacts, ten closures, seven profile preflights, final materialized revalidation and independent artifact review. Issue #11 remains open, PR #107 remains draft, and #12 remains gated. Native/NATS runtime remains NOT_RUN.

Synchronization checks passed: 118 documents, 1,124 local links, 84 catalog tasks, 16 normative mirrors and original v2 unchanged. Whitespace passed and no conflict remains. Product scripts, build code and build tests match the previously reviewed and executed `8c7f10ea` tree. Trusted automation matches accepted main exactly. Both histories below remain preserved.

## E01-01.builds — Accepted native provenance correction synchronized

2026-11-08: Root synchronized accepted PR #119, merge `5edbe3e5feb46fc381520b36d6c5abd97fdbaf9f`, into the product branch. Final source head `755124823f17aba8c0fdd1a428cb9bc69589a206` passed both independent reviews and CI after the actual Bor/Prysm/Heimdall diagnostic passed in 1,653.251 seconds. It compiled all three clients, ran version commands in the pinned runtime image and passed shared source/command/binary/version/ELF/OCI checks. Both artifact reviews and independent cleanup checks passed. Result SHA256: `76f5f0552fc5247c2b653ccf14b7b0859ebce2dcc4366489e42024aa9817d56d`.

The prior full operation remains failed at Bor import validation; its successful Geth/BSC builds and all historical evidence remain unchanged. The merge conflict affected only this status file; both histories are preserved below. Product code remains unchanged, and trusted automation must match accepted main. Next: validate, commit and push this synchronization, then run a fresh complete operation with both updater modes, both independent refreshes, five native artifacts, ten module closures, all preflight controls, final materialized validation and cleanup. Require final independent artifact reviews before closing issue #11. PR #107 remains draft, #12 stays gated and native/NATS runtime remains NOT_RUN.

Synchronization checks passed: 117 documents, 1,122 local links, 84 catalog tasks, 16 normative mirrors and original v2 unchanged. Whitespace passed and no conflicts remain. Trusted automation matches accepted main exactly; product scripts, build code and build tests match the previously reviewed `dc48176` tree. Both histories below remain preserved.

## E01-01.builds — Accepted source transport corrections synchronized

2026-11-08: Root synchronized the product with accepted PR #118, merge `0e64930f73336abdde7b547add6872714d52dd47`. Product code at `c4c078f4` passed both scoped independent reviews, all 79 product tests and exact-head CI. Trusted head `990d80a4` passed both final independent reviews and exact-head CI after all code findings and the contract-text inconsistency were resolved. The final documentation correction left code and tests unchanged from the reviewed code head.

A bounded real-source diagnostic passed in 4.381 seconds: both transports obtained the official page with 33 releases and both journals validated. Product attempt 1 used an explicitly injected local TLS EOF, followed by a real GET; trusted acquisition used one real GET. Result SHA256 `d8dbea4de1b93be50fbfd7b018c292d7a1a1971e3be578f04e0ed743c1d8e33d`; all owned processes and containers are absent. This proves the scoped transport, not a full updater sweep or build.

The previous full preparation and its one unchanged retry remain failed, with cleanup confirmed. Synchronization checks passed: 116 documents, 1,118 links, 84 catalog tasks, 16 mirrors, original v2 unchanged and whitespace. Product code matches reviewed `c4c078f4`; trusted automation matches accepted main exactly. Next: commit and push this synchronization, then run a fresh full operation linked to the accepted correction. Require both updater modes, both independent source reports, five native artifacts, ten isolated module closures, final artifact/control reviews and cleanup. Issue #11 remains open, PR #107 remains draft, and issue #12/native-NATS runtime remain gated. Both status histories below are preserved.

## E01-01.builds — Outer deadline transition repair ready for review

2026-11-08: Standards re-review requested changes on `4135ec2d5d153495a8a1feb3de5c40b4627f2bb9`. During-open cancellation and terminal-body persistence controls passed. One remaining counterexample expires the inherited alarm after the network guard exits, during HTTP status validation; a later URL could still dispatch. Preserve the inherited absolute deadline across the entire fetch transition and later requests, add that exact regression and relevant controls, then check and publish the product-only correction. Previous review results and failures remain preserved. Trusted work remains separately owned; no live or native operation is authorized here.

The transport now retains the earliest inherited monotonic deadline across setup, network reads, status validation, persistence and later requests. Consuming its signal or arming a later timer cannot extend it. The exact delayed-status and between-request controls failed before the repair; a single-request timeout control remained valid. Final checks pass: 23 focused tests, all 79 product tests, documentation validation and whitespace checks. Next: publish this clean correction to PR #107, return ownership and require independent review of the exact new head. No journal shape, retry policy, pin or native runtime claim changed.

## E01-01.builds — Source acquisition review repair ready for review

2026-11-08: Product writer ownership returned to `/root/boot03_controller` at `e3e6c555c6d3785b0a32cd1582389d7f4c50185f`. Standards review requested two corrections: an expired inherited one-shot alarm did not block a later URL, and failed terminal diagnostic-body persistence did not block a later URL. Spec review passed 73 tests and six negatives; CI `42430932686` passed, but those checks missed both cross-request defects and do not establish acceptance. Add direct red regressions, latch outer expiry and terminal evidence failure, then run affected checks and publish the correction to PR #107. Trusted implementation remains separately owned. No live request, native build or old-artifact change is part of this repair.

Both defects are corrected with unchanged journal fields and retry policy. The direct red controls produced three assertion failures before the fix. All 19 focused tests and 75 product tests now pass; docs validation passes 115 documents, 1,112 links, 84 catalog issues and 16 mirrors, with original v2 unchanged. Whitespace checks pass. Next: push the clean correction to PR #107, return writer ownership and require fresh independent exact-head review. The failed operations and first review remain preserved; no new runtime or live-source result is claimed.

## E01-01.builds — Bounded source acquisition repair ready for review

2026-11-08: Sole writer `/root/boot03_controller` owns the product correction from `3932094c248e97c557bde7378108ef061923d9ea` under D95. Preserve the two failed preparation operations after PR #117: trusted GitHub refresh EOF in `2b78ba25349204d63d61149c699c19`, then product Bor TimeoutError and nats-go URLError in `19e78cfb03ebc965a6a62af27f8040`. The latter wrapper's cause was not retained and must not be inferred. Cleanup was independently confirmed. Add at most two official GET attempts, a total deadline covering body reads, durable secret-safe attempt evidence, and a hash-bound acquisition reference on each observation. Preserve credentials, pagination, validation, active inputs, and terminal failure behavior. A separate trusted worktree will validate the evidence and preserve the native command success invariant. Next: focused fault controls, checked product commit/push to PR #107, then trusted integration. No runtime or full-run retry is authorized in this block.

Implementation now retains separate attempt bodies and a closed hash-bound journal, with intent synchronized before dispatch. Only exact typed network timeouts and the approved typed TLS EOF can retry once. One 60-second monotonic deadline covers both attempts and reads within the discovery limit. Persistence failure, cancellation, HTTP/certificate/parse/hash failures and exhausted deadlines never permit recovery. Existing signal handlers and outer timer deadlines are preserved. Synthetic replay remains explicitly synthetic. All 17 focused tests and 73 product tests passed; the old-head replay failed the targeted recovery and intent controls. The first red harness reached its 60-second timeout because its oversized-response fixture depended on the missing implementation limit; that fixture now terminates independently. See the retained author evidence for exact checks. Next: publish the clean product commit to PR #107, obtain independent exact-head review, and integrate the separately reviewed trusted journal validator before a new full operation. Issue #11 remains open; this block performs no native build or runtime work.

## E01-01.builds — Accepted module metadata repair synchronized

2026-11-08: Root synchronized this product candidate with accepted PR #117, merge `23eb96dffd3019bb4c6b1c345ab29707a5ec1751`. Both cumulative independent reviews and exact-head CI passed on `7be151ea694411a29f63e038374a55bdf15844db`. The initial review failures remain preserved. The actual Geth diagnostic built and ran Geth 1.17.10 with 348 verified modules and unchanged original sources; the corrected validator revalidated its unchanged history and rejected the three review mutations. Its cleanup is complete. The diagnostic does not include the full imported-package/build row.

Product files remain unchanged from the reviewed product scope. The previous full operation failed on read-only native checksum metadata before compilation; it remains failed. The next action is a fresh full five-client operation using the accepted repair and this synchronized candidate. Require ten isolated module closures, actual final build-row checks, both updater modes and independent checks, seven profile bindings, negative controls, final artifact reviews and complete cleanup. Issue #11 remains open, #12 remains gated, and native/NATS runtime remains NOT_RUN. Both status histories below are preserved.

## E01-01.builds — Full verification after accepted package transport recovery

2026-11-08: Root synchronized this candidate with accepted PR #116, merge `0299a653cae572e3f044f8746782d681b7778599`. Both cumulative independent reviews and exact-head CI `42419502230` passed after correcting the two initial review findings. A fresh real diagnostic passed exact Go version, three signatures, offline installation of 13 roots, all 225 installed package archives and sealed proof validation in 315.27 seconds. One explicitly injected typed TLS EOF recovered exactly once; cleanup completed. Result SHA-256: `cf63f3aa0fbb2447598ea4bfea5d0029d683d5b1e1b43aaba4216fa83d9ef7a6`. The previous full run's actual TLS failure, initial failed reviews and both diagnostics remain preserved. The status-only merge retains both histories below. Product files and active pins are unchanged. Next: check and publish this exact synchronized candidate, then run a fresh full operation with the accepted correction, unchanged retained report and new ownership. Parent issue #11 remains open. Native compilation, repeat builds, arm64 and native/NATS runtime remain NOT_RUN until actual proof exists.

Synchronization checks pass: 114 documents, 1,111 local links, 84 catalog entries, 16 normative mirrors, unchanged original v2 and clean whitespace. Product files under `scripts`, `build` and `test/builds` are byte-identical to `0a20f0e` and the previously reviewed product scope. The accepted transport repair completed 118 trusted build tests with two opt-in Docker skips; current-candidate CI and actual build proof remain required.

## E01-01.builds — Full verification after the accepted APT correction

2026-11-08: Root synchronized this candidate with accepted PR #115, merge `b0d592d7b77db85209fbd668ec31f1bbc2979a5c`. Both independent review axes and exact-head CI `42414185493` passed. The separate real diagnostic passed all three retained signatures, offline installation of 13 exact roots, and archive verification for all 225 installed compiler packages in 324.03 seconds; cleanup completed. Diagnostic result SHA-256: `8324e882cbb7f13c9539654252cd11e99147fecfa505e483d1ab7011b8f55b32`. This corrects the APT parser failure; prior failed operations remain preserved. The merge conflict was confined to this status log, and both histories are retained below. Product files and pins remain unchanged. Next: verify the merged documents and product identity, commit/push, then run a fresh full operation with accepted source, exact retained report, new ownership and the correction reference. Parent issue #11 remains open. Native compilation, repeat builds, arm64 and native/NATS runtime remain NOT_RUN until actual proof exists.

Synchronization checks pass: 113 documents, 1,109 local links, 84 catalog entries, 16 normative mirrors, and unchanged original v2; whitespace is clean. Product files under `scripts`, `build`, and `test/builds` are byte-identical to `e9f6122` and the previously reviewed product scope. The accepted parser's 34 focused tests and exact-head CI already pass; final current-candidate CI and actual build proof remain required.

## E01-01.builds — Verify with the exact retained package snapshot

2026-11-07: Root synchronized this candidate with accepted PR #114, merge `4a767f64d8742cba0d79c7fc6bcf7979ee19875c`, after both independent reviews and GitHub CI `42409404290` passed. The previous actual run passed both updater modes, both independent source checks, and all three image acquisitions, then failed before compilation because a mutable Debian Security index changed. Cleanup completed and the failure remains recorded. The next run supplies the exact retained raw source report with SHA-256 `c22f2c1ab87f5f4f7886ecef60426778f094647932db54dedb481bb60b759d2e`; the driver copies its approved index/InRelease pairs and performs new image-owned signature and validity checks. Product files and pins are unchanged. The only merge conflict was this status log; both histories are retained. Check, publish, and run the new exact candidate. Parent issue #11, actual builds, and native/NATS runtime remain incomplete or NOT_RUN until actual evidence passes.

Synchronization checks pass: 56 isolated product tests in 14.540 seconds; documentation with 112 documents, 1108 links, 84 catalog entries, and 16 mirrors; whitespace checks. Product files are byte-identical to the previously reviewed `952d210` candidate.

## E01-01.builds — Fresh verification after accepted source repairs

2026-11-07: Root owns this synchronized product candidate. Source-preparation PR #113 is accepted at `d8c901e205a0957e6bac79ef3f6b120827527116` with both independent reviews and exact-head GitHub CI `42405911216` passing. It includes trusted Docker Engine selection, refreshed Debian candidate metadata, and the reviewed corpus hash bindings. Product selector repair `e79d59e` is retained unchanged. The status-only merge conflict preserves both histories below. Next: check and publish this exact candidate, run a fresh owned real-source verification and native build, retain actual outputs/failures, and obtain final independent reviews. Issue #11 remains incomplete; no dependent task or profile is accepted. Native builds and NATS runtime remain NOT_RUN. Preserve source and candidate bytes while verification runs.

Synchronization checks pass: all 56 isolated product tests in 11.612 seconds, documentation with 111 documents and 1107 links, and whitespace checks. Product files under `scripts`, `build`, and `test/builds` are byte-identical to `e79d59e`. Actual source acquisition and native compilation are the next checks.

## E01-01.builds — Docker Engine release selection repair

2026-11-07: Direct author `docker_product_fix` repaired the product selector from base `8207904f9de1fcafbb584d20a0265b3df1847b1c` under D95 and issue #11. The previous verifier was stopped before this writer handoff. Official Moby metadata exposed a selector defect: `docker-v29.9.3` was ignored in favor of `v28.5.2`. Five new tests failed before the fix; all 56 product builds tests now pass. The retained two-row replay selects `docker-v29.9.3` twice. Selection now accepts complete current and legacy Engine tags only for the official stable Docker Engine source, preserving original tag bytes and excluding API/client namespaces and suffixes. Documentation and whitespace checks pass. [Repair evidence](evidence/E01-01.builds.md) records scope, failures, and checks. Trusted automation, active pins, registry, and catalog are unchanged. Native compilation and runtime remain NOT_RUN for all seven profiles. Next: publish this checked block and return ownership for fresh real-source verification and independent exact-head review; issue #11 remains open.

## E01-01.builds — Synchronize the published candidate for direct verification

D95 workflow PR #110 is accepted at `146746e`; the standalone GitHub pagination block PR #111 is accepted at `fe14767`, with both review axes and GitHub CI passing. Parent issue #11 remains open. The published product branch is being merged with accepted main using a normal merge; existing product implementation bytes are unchanged from reviewed `7022c0d`. Preserve the original failed `f17bb19` worktree and canonical run unchanged.

Next: check and publish this synchronized candidate, then run fresh real-source verification and native builds with the existing standalone verifier and bounded process supervisor in a new owned evidence directory. No controller launch, ledger reservation, or recovery transition is required. Native builds and native/NATS runtime remain NOT_RUN until actual results exist. Do not change this candidate while its verification is running.
## E01-01.builds — Actual derivative preflights passed

2026-11-08: Both independent code reviews and exact CI `42449578253` passed for PR #120 head `046d1cb0a45c3592c6cfc07742a6fc454b4381b0`. The separately reviewed diagnostic copied 12,870 referenced files, changed only the derivative cleanup reference, and passed all seven actual product preflights in 36.268–38.399 seconds each. A changed binary hash was rejected. Supervised operation `run_D95_materialized_preflight_1794114608 / 47d65615515dbff21712af3558` passed in 326.506 seconds; result SHA256 `d4a3898618b98fb6810c63a638cee143273c6032945dfec49284eb70039997a5`. Supervisor, worker, all eight candidate groups and exact-nonce containers are independently confirmed absent. Original manifest, command-history and cleanup-journal hashes remain unchanged.

Root owns this documentation-only result checkpoint after author ownership returned. The [preflight evidence](evidence/E01-preflight-budget.md) records the exact helper, request, proof, timings and diagnostic limits. Both independent diagnostic reviews passed, including all eight control records, 12,870 separate file inodes and unchanged original hashes. Documentation checks passed 115 files, 1,123 links, 84 tasks and 16 mirrors; original v2 and executable code remain unchanged. Final exact-head reviews and CI must pass before protected merge. Then synchronize the product branch and run a fresh complete operation. This diagnostic does not establish new native provenance or convert the earlier failed full run to success. Issue #11 stays open, PR #107 stays draft, and #12 remains gated; native/NATS runtime acceptance remains NOT_RUN.

## E01-01.builds — Bound actual materialized preflight time

2026-11-08: Complete operation `run_D95_builds_1794109506 / c2f5e8372cde4279d846d9f68488`, source `5edbe3e5` and product `8c7f10ea`, compiled all five native clients, verified all ten module closures and reached final product preflights after initial trusted materialized validation. The first real Ethereum preflight exceeded the unconditional 10-second control timeout. Result SHA256: `20fd70567be575280454b300e9720057325a2e958659018f2dac1549de73d5e3`; duration 3,854.763 seconds. Supervisor, worker, all 54 candidate groups and owned containers are absent. Original artifacts and the failed result remain immutable.

The retained manifest has 13,041 reference occurrences and about 8.526 GiB of declared referenced data, unlike small synthetic controls. Add a named fixed finite budget for actual materialized preflights while retaining the 10-second synthetic/plan budget, original outer deadline clipping, monotonic enforcement, process-group cleanup and all artifact checks. Do not derive authority from candidate size/time declarations or skip hashing. A separate read-only measurement ran 33.228 seconds and correctly rejected the old intermediate cleanup hash, because failure cleanup appended to that journal. That measurement is not a positive preflight or parent acceptance. Any derivative diagnostic must be explicit and leave all original evidence unchanged. Require focused controls, independent exact-head review and real preflight evidence before accepting this repair. Issue #11 and PR #107 remain open; #12 remains gated.

Root transferred sole writer ownership of `.project/worktrees/build-preflight-budget` to `/root/boot04_preflight`. The trusted final profile loop now explicitly selects a named 300-second materialized cap; plan/synthetic controls retain 10 seconds. No candidate declaration selects the larger budget. All six focused methods and 178 affected build tests pass, with two existing opt-in Docker skips and no ResourceWarning. Documentation and whitespace checks pass. Publication, exact-head CI, and independent review remain required. See [the scoped evidence](evidence/E01-preflight-budget.md). This author has not launched a full build, native process, container, or live-source operation.

## E01-01.builds — Actual remaining-client diagnostic passed

2026-11-08: Both independent code reviews and CI `42440734067` passed for PR #119 head `b382aae725c7cde4b9561cb2f149cbb0a63d6086`. The reviewed helper then compiled Bor, Prysm and Heimdall, ran their version commands in the pinned runtime image, created OCI outputs and passed the same `native_build` checks used by full materialized validation. Operation `run_D95_remaining_native_1794107042 / 34d6045183932e1cb8fcbf208aac` passed in 1,653.251 seconds; result SHA256 `76f5f0552fc5247c2b653ccf14b7b0859ebce2dcc4366489e42024aa9817d56d`. Both independent artifact reviews passed. All 208 recorded commands succeeded. Supervisor, worker and exact-nonce containers are independently confirmed absent.

The [native provenance evidence](evidence/E01-file-target.md) records exact request, helper, proof, history and binary hashes. Root owns this documentation-only result update after the author and reviewers returned ownership. Documentation validation passed: 114 documents, 1,121 links, 84 catalog tasks, 16 mirrors and original v2 unchanged. Whitespace passed; only this status and the evidence note changed, and code is identical to reviewed `b382aae`. Next: publish these records, confirm the exact final head with both reviewers and CI, then merge PR #119 through protected checks. Synchronize the product branch and perform a fresh full five-client, ten-closure operation. The previous full operation remains failed; issue #11 stays open, PR #107 stays draft and #12 remains gated. Native/NATS runtime acceptance remains NOT_RUN.

## E01-01.builds — Bind native entry points and source provenance

2026-11-08: Full operation `run_D95_builds_1794102326 / c2d81fad7336b030c629b0661f0f67`, source `0e64930` and product `dc48176`, failed at Bor import validation after actual Geth and BSC builds succeeded. Both updater modes and independent refreshes completed. Bor's exact native file target `./cmd/cli/main.go` yields a Go `command-line-arguments` main package without a Module field. The current verifier rejects that legitimate entry point before compilation. Retained import output has 1,921 rows and exactly one such moduleless nonstandard row. Result SHA256: `2ae14c3b2db98aa42eaa427c6322b4c3571c295adcc8c83fb64feead7256baa4`.

Supervisor, worker group, all 53 recorded groups and owned Docker containers are independently confirmed absent. Original operation artifacts remain immutable. This isolated trusted-verifier block must bind the exact native entry point to its selected source and recipe while rejecting other moduleless packages, altered paths or files, foreign roots and mismatched commands. Preserve all module, NATS and provenance checks. Add direct positive and negative controls, validate the retained actual Bor import output, and require independent exact-head review before merging. No public schema, client pin, recipe, retry policy or runtime guarantee changes are intended. Issue #11 remains open, PR #107 remains draft, and #12 remains gated.

Root transferred sole writer ownership of `.project/worktrees/build-file-target` to `/root/boot04_preflight`. The scoped correction binds the exact Bor file root, archive, selected commit and native import arguments while retaining the separate Main-module proof and existing dependency isolation. All 29 focused methods and 168 affected build tests pass, with two existing opt-in Docker skips. Read-only replay of the actual 1,921 packages passes, and missing-root/wrong-target controls reject; retained input hashes are unchanged. See [the repair evidence](evidence/E01-file-target.md). Publication, exact-head CI, and independent review remain required. No new live build was started by this author.

The coordinator then confirmed Bor's file-target metadata shape on the exact pinned compiler. Actual retained Geth/BSC build-info also lacks `-ldflags`; the old source guard rejects it. The shared `native_build` validator now requires authenticated selected source, exact compile/import/inspection commands, the compiled binary's runtime copy, and its native version output before accepting omitted flags. All 34 final focused methods and 173 affected build tests pass, with two existing opt-in Docker skips. Documentation and whitespace checks pass. The failed operation has no same-operation runtime version outputs; a separate successful Geth/BSC version diagnostic is format evidence only. Exact-head CI and independent review remain required. The native recipe, public fields, pins, and all original failed artifacts remain unchanged.

## E01-01.builds — PR #118 contract text aligned with the reviewed code

2026-11-08: Both independent reviewers found no remaining code blocker on `e5e34bb1fedbcb4cb483e5d982a5b85250c8ad5c`. Their only remaining finding was a stale four-field process-proof sentence that contradicted the new eight-field definition. Root owns this documentation-only correction after both reviewers returned clean ownership. The old sentence now references the single current definition; no code, tests, schema implementation or retry policy changes.

Documentation checks passed: 113 documents, 1,117 links, 84 catalog entries, 16 mirrors and unchanged original v2. Whitespace passed; the diff contains only this status, the contract and the evidence note, so code and tests are unchanged from the reviewed head. Next: commit and push, obtain exact-head confirmation from both reviewers, then run the bounded actual-source diagnostic. PR #118 and the parent build remain unaccepted until their respective remaining checks pass. Earlier failed reviews and preparations remain preserved.

## E01-01.builds — PR #118 raw-stream evidence repair

2026-11-08: Candidate `090fdc4a7c12d5c0db12a7f0afda8d5481134706` passed Spec review and fixed both original findings, but Standards found that raw stdout/stderr overflow omitted an available safe prefix without separate raw-byte accounting. The retained review history and reproduction are in [the source transport evidence](evidence/E01-source-transport.md).

The scoped repair retains the bounded safe prefix and adds closed internal stdout/stderr observed counts and truncation flags. Decoded-body fields stay separate; public BuildManifest fields and retry classes are unchanged. All 30 focused methods and 163 affected build tests pass, with two existing opt-in Docker skips and no ResourceWarning. Document and whitespace checks pass. Remote CI and independent reviews of the new commit remain required. Parent issue #11 stays open; this block claims no live-source or native execution result.

## E01-01.builds — PR #118 repair ready for fresh independent review

2026-11-08: Both independent axes requested changes on trusted source-recovery candidate `8560c00cc3d62d50b4a04bd4d0ab0089e2a0c7c3`, despite its exact-head CI passing 377 tests and 54 component negatives. The two findings are retained in [the evidence](evidence/E01-source-transport.md): effective GitHub credentials were not protected in raw outputs, and an incomplete HTTP read could leave a started attempt without its available partial bytes.

The scoped repair privately binds effective GitHub authentication, scans raw outputs before retention, and records incomplete HTTP reads as terminal nonretryable failures with bounded partial bytes. All 26 focused methods and 159 affected build tests pass, with two existing opt-in Docker skips and no ResourceWarning. Document and whitespace checks pass. The new commit requires remote CI and both independent reviews. Parent issue #11 remains open. Native compilation, actual-source diagnostics, and runtime acceptance are not claimed by this repair.

## E01-01.builds — Trusted source recovery ready for cumulative checks

2026-11-08: Root transferred sole writer ownership of `build/source-transport` to `/root/boot04_preflight` from accepted `23eb96dffd3019bb4c6b1c345ab29707a5ec1751`. The two failed preparation operations remain failed and unchanged. This scoped block adds bounded same-URL GET recovery, explicitly bound source-attempt evidence, an internal reference to the first independent refresh report, and final nested-evidence checks. Native command success, dependency pins, product BuildManifest fields, D83, and parent issue #11 gates remain intact.

The first full portable CI passed 375 tests and 54 component negatives, with two existing opt-in Docker skips. A later direct control reproduced loss of an inherited deadline between requests; the runner now retains that bound across future source/native admission. All 20 focused transport/evidence tests pass with tracemalloc and ResourceWarnings enabled after the correction. A supervised synthetic product-interface check passed for 148 observations with exit 0 and confirmed cleanup; it is not product or live-source acceptance. [Block evidence](evidence/E01-source-transport.md) retains both preparation failures, discovered implementation gaps and corrections, exact boundaries, checks, and the stable diagnostic API. No author live requests, Docker actions, native builds, full runs, or controller changes occurred. Next: commit/push a frozen draft, its cumulative CI, independent Spec/Standards review, and the coordinator's bounded actual-source diagnostic. Issue #11 remains incomplete and runtime remains NOT_RUN.

## E01-01.builds — Trusted source acquisition repair in progress

2026-11-08: Sole writer `/root/boot03_controller` owns `build/source-transport` from accepted `23eb96dffd3019bb4c6b1c345ab29707a5ec1751` under D95. Preserve the trusted GitHub EOF failure `2b78ba25349204d63d61149c699c19` and subsequent product source-discovery failure `19e78cfb03ebc965a6a62af27f8040`; both ended before compilation and cleanup was independently confirmed. The coordinated product correction runs first in its separate worktree. This trusted block will validate closed per-observation acquisition evidence, bound official GET recovery to two attempts and one request deadline, retain failed attempts outside the all-success native command list, and preserve legacy retained source inputs without inventing acquisition proof. Authentication, pagination, signatures, hashes, pins, and product build-manifest fields remain unchanged. Next: product publication, narrow trusted integration/tests, checked commit/push, and independent reviews before any new full operation.

## E01-01.builds — Module metadata review repair checked

2026-11-08: Both independent reviewers requested changes on PR #117 head `0fca5cc3f83c024d6ea9bd29d9c8f961e5670ac8`. Ownership returned to `/root/boot03_controller` for one focused repair: validate effective Go arguments, require offline build/import settings, and require captures before present build/import commands on the correct component/container. Preserve the pre-compilation API and bind existing final build rows without a schema change. The coordinator's actual Geth diagnostic passed in 183 seconds with 348 modules and a built Geth 1.17.10 binary; cleanup was independently confirmed. That diagnostic does not satisfy the failed review gate or prove the whole parent task. Next: direct red regressions, correction, affected checks, commit/push the frozen repair, and fresh independent reviews/CI. No author Docker or full run is authorized.

The old validator failed 19 direct regression cases. The corrected code passes all 30 focused and 133 trusted build tests; the broader run took 33.895 seconds with two existing Docker skips. All five fixed native command forms pass, including Bor's repaired intermediate parser mismatch. Read-only revalidation of the retained actual Geth history passes for its observed compilation/captures and rejects all three review mutations; no full build-row or new runtime claim is made from that diagnostic. Documentation checks pass for 112 documents, 1,111 links, 84 catalog entries, 16 mirrors, and unchanged original v2; whitespace checks pass. No author Docker, native compilation, full operation, or background process ran. Next: publish the frozen repair to PR #117 and obtain fresh exact-head reviews and CI.

## E01-01.builds — Isolated Go checksum metadata checked

2026-11-08: Direct author `/root/boot03_controller` owns `build/module-metadata` from accepted `0299a653cae572e3f044f8746782d681b7778599` under D95. Actual operation `a7f0906b899f026ac8556a8824cc240a` in `run_D95_builds_1794090095` passed source, image, signature, installation, and all 225 package-archive checks, then failed before compilation: Geth downloaded 348 module records but could not update read-only `/source/go.sum`. Preserve that failure and the coordinator's verified cleanup. This partial issue #11 block adds per-component alternate module metadata using `-modfile`, preserves the read-only source and exact `go.mod`, and binds generated checksum files through existing command evidence. Original checksum entries and authenticated selected-download hashes remain mandatory. Scope excludes product/schema changes, dependency pins, controller work, root or old-run mutation, and a full native run. Next: red controls, narrow implementation, focused checks, commit/push, and independent review of the frozen draft.

Implementation is complete. All 25 focused checks and 128 trusted build tests pass; the broader run took 35.147 seconds and retained two existing opt-in Docker skips. The ten new methods include an actual offline host-Go check of relative replacements using synthetic modules. Documentation and whitespace checks pass. [Block evidence](evidence/E01-module-metadata.md) preserves the red controls, original failure, and the coordinator's separate 50.37-second pinned-Go flag probe. That probe confirms native flag behavior, not this implementation or a full build. No author Docker, live module download, or full build ran. Next: commit/push the frozen draft, independent reviews and CI, then the coordinator's bounded real Geth diagnostic. Issue #11 remains incomplete.

## E01-01.builds — Package transport review repair checked

2026-11-08: Both independent reviews requested changes on `2926d6dd642532090309dae87aebfce2ecf4ff74` in draft PR #116. Writer ownership has returned to `/root/boot03_controller`. Repair only two findings: bind and validate preserved original signature logs after sealing, and retain bounded available bytes from oversized reads and wrapped exception partials without widening retry eligibility. The coordinator's real diagnostic passed in 315.84 seconds with three signatures, 13 installed roots, 225 inventoried packages, and one injected/recovered TLS EOF; result SHA-256 is `9571c28dd770a2f524023ec456118365ff0301eebbbea4f6d0251dfaa939142e`. Cleanup was independently verified. That diagnostic did not satisfy the failed review gate and does not validate this repair. Next: reproduce the findings, apply the focused corrections, run affected checks, commit/push PR #116, and return the frozen head for fresh review and diagnostic. Issue #11 remains incomplete; no full build or native runtime claim is added.

The direct regressions reproduced five failing subcases across three methods before the correction. All 18 transport tests now pass, including exact original/extended core-field matching, post-seal original mutation rejection, wrapped partial retention without duplication, and bounded oversized-read evidence without retry. All 118 trusted build tests pass in 33.073 seconds with two existing Docker skips. Documentation checks pass for 111 documents, 1,110 local links, 84 catalog entries, 16 mirrors, and unchanged original v2; whitespace checks pass. The seal API and product manifest shape are unchanged. [Repair evidence](evidence/E01-package-transport.md) retains both reviews and the previous diagnostic separately. No author live fetch, Docker, full build, or background process ran. Next: publish the frozen repair to PR #116 for fresh independent review, GitHub CI, and the coordinator's real diagnostic.

## E01-01.builds — Bounded package transport recovery checked

2026-11-08: Direct author `/root/boot03_controller` owns `build/package-transport-retry` from accepted `b0d592d7b77db85209fbd668ec31f1bbc2979a5c` under D95. Actual operation `b8a437fc11c030a96ef495b4b5534103` in `run_D95_builds_1794086057` failed before compilation with an SSL unexpected EOF while acquiring the installed package closure. The coordinator preserved the failed result, verified cleanup, and observed one later successful exact-hash fetch; that probe does not establish a permanent repair. The approved block adds explicit package-archive-only recovery for typed `ssl.SSLEOFError` with reason `UNEXPECTED_EOF_WHILE_READING`, at most two total attempts under the existing deadline/stop. Journal intent before dispatch, preserve failed-attempt metadata and available bytes, and bind recovery evidence into the final proof. Metadata/image/updater callers and authentication, size, hash, filesystem, and runtime failure behavior remain unchanged. No Docker, full build, product/pin/controller change, or root-run mutation is authorized here. Next: red-first deterministic controls, narrow implementation, focused checks, commit/push, draft PR, and independent review.

The package-only helper and immutable proof extension are complete. Thirteen new controls cover the exact retry type/bound, partial and exception-provided bytes, stop/deadline changes during journal persistence, nonretryable failures, and tampered log/journal/body rejection. All 113 trusted build tests pass in 32.548 seconds with two opt-in Docker skips; 45 focused tests also passed before final integration controls. Documentation checks pass for 111 documents, 1,109 local links, 84 catalog entries, 16 normative mirrors, and unchanged original v2; whitespace checks pass. [Block evidence](evidence/E01-package-transport.md) preserves the actual failure, scope, and seal API. No live fetch, Docker, or full build ran in this author block. Next: commit/push and the coordinator's bounded injected-EOF diagnostic on the frozen head before independent reviews and CI.

## E01-01.builds — APT archive-plan parsing checked

2026-11-08: Direct author `/root/boot03_controller` owns `build/apt-package-plan` from accepted `4a767f64d8742cba0d79c7fc6bcf7979ee19875c` under D95. Actual operation `0973d8349b6f10a74b7e6c5ebb2a8b4b` in `run_D95_builds_1794083568` failed before compilation because the trusted parser required four APT print-URI fields, but the successful APT command returned three for security packages without an optional digest. The adjacent stable-package row includes an MD5Sum fourth field. Preserve the failed operation and cleanup evidence. This partial [issue #11](https://github.com/sigaev-pro/bc-indexer/issues/11) block repairs only trusted archive-plan parsing, regression tests, and its evidence/contract. Exact authenticated index membership, size, and SHA-256 archive checks remain mandatory. No product, pin, corpus, controller, or root-run change is authorized. Next: reproduce with the saved rows, repair, run focused/document checks, commit/push, and open a draft PR for review. No full build or package installation runs in this author block.

The saved two-row regression reproduced the parser failure. The focused repair now accepts both APT shapes while retaining exact signed-index and archive checks and rejecting unsafe paths. All 34 focused tests pass, including six new parser methods. Documentation checks pass for 110 documents, 1,108 local links, 84 catalog entries, 16 normative mirrors, and unchanged original v2; whitespace checks pass. [Block evidence](evidence/E01-apt-package-plan.md) records the original output hash, diagnosis, red result, and limitations. Next: commit/push, draft PR, and coordinator diagnostic on the frozen head before independent reviews and CI. Issue #11 remains incomplete.

## E01-01.builds — Retained signed package inputs checked

2026-11-07: Direct author `/root/boot03_controller` owns `build/retained-package-snapshot` from accepted `d8c901e205a0957e6bac79ef3f6b120827527116` under D95. Actual operation `9676a622fe9d7f213db3d1a94773e819` failed before container creation or compilation because Debian Security republished its mutable index after the candidate was prepared. Preserve that failure and all original run bytes. This partial [issue #11](https://github.com/sigaev-pro/bc-indexer/issues/11) block adds explicit retained-report input to the trusted package acquirer: exact report digest, selected original observations, copied index/InRelease bytes, and new image-owned signature verification with current validity checks. The earlier report is source input, not task acceptance; unrelated release selections are not trusted. Scope is trusted acquisition, driver argument plumbing, proof validation, focused tests, public contract, evidence, and status. Active pins, corpus, product interfaces, controller state, and existing live updater checks remain unchanged. Next: red-first controls, implementation, focused checks, commit/push, and draft PR with independent review. Native builds and runtime remain NOT_RUN.

Implementation and 13 new portable regression methods are complete. The paired report arguments copy only exact pinned input pairs with bounded no-follow reads; hash-bound signature-log provenance makes the result self-contained. New cryptographic checks remain mandatory. The red baseline and earlier real failure are preserved in [block evidence](evidence/E01-package-snapshot.md). A disposable copy-only diagnostic read the actual retained report and matched all three pins; it did not authenticate signatures or perform a build. The final trusted build suite passes 94 methods in 36.399 seconds with two explicit Docker skips. Documentation checks pass for 109 documents, 1,107 local links, 84 catalog entries, 16 normative mirrors, and unchanged original v2; whitespace checks pass. Next: commit/push, GitHub CI, and independent exact-head reviews. Issue #11 remains incomplete.

## E01-01.builds — Combined real-source preparation review

CI `42405291880` and independent Standards review rejected combined head `702f1e1` because the corpus still referenced the previous candidate-lock hash. Root refreshed that derived binding and its canonical source digest only; all 33 source mappings and seven profiles remain unchanged. Required next checks are corpus validation, full CI, and fresh exact-head reviews. The prior failure and review remain evidence; no previous runtime result is carried forward.

Corrected corpus validation passes with 217 acceptance cases, 677 baseline variants, and 36083 expanded cells. Full local CI passes: 305 tests in 123.699 seconds, with two Docker integration skips. Publish the correction and obtain fresh independent reviews plus exact-head GitHub CI before merge.

Root combined the published trusted Engine-tag repair `688ce7f` and package-candidate refresh `480eb4c` for one exact-head review under D95. Both scoped histories and evidence are retained below. The only merge conflict was this status log; both entries are preserved. Product repair `e79d59e` remains in draft PR #107. Next: check and publish the combined preparation PR, obtain independent Spec/Standards reviews and required CI, merge through branch protection, synchronize the product candidate, and start a fresh real verification operation. Issue #11 remains incomplete; native builds and NATS runtime are NOT_RUN.

Combined checks pass: 81 trusted build tests with two Docker integration checks explicitly skipped, 54 rejected structural negatives, 108 documents, 1106 local links, 84 catalog entries, and 16 unchanged normative mirrors. Whitespace checks pass. Skipped Docker controls do not prove the actual build or native runtime.

## E01-01.builds — Refresh Debian build-candidate metadata

2026-11-07: Root owns `build/refresh-package-candidate` at base `fe14767132587d93bac502a7782606879c1a962a` under D95. Real source observations show that both Debian Security package indexes changed after BOOT-06; the exact old digest cannot authenticate the current bytes. Refresh only those candidate index hashes and the newer `zlib1g-dev` security package metadata, preserving the 41-component registry, native source pins, seven profiles, active-release state, compatibility constraints, and all NOT_RUN results. Check the official bytes independently, validate the candidate structure and build projection, then commit/push for independent Spec and Standards review. Signature verification and actual compilation remain required in the subsequent full build run. This is a partial issue #11 block, not task acceptance or release promotion.

Metadata refresh and direct official index checks are complete. Both build-platform plans and the structural validator pass; all 54 invalid controls are rejected. Documentation and whitespace checks pass. [Evidence](evidence/E01-package-refresh.md) records the exact hashes, initial validation failures and corrections, and remaining signature/build requirements. Next: publish and obtain independent review before using this candidate in the full build.

## E01-01.builds — Trusted Docker Engine release selection checked

2026-11-07: Accepted main `fe14767132587d93bac502a7782606879c1a962a` includes the standalone pagination repair in PR #111. Direct author `/root/boot03_controller` owns `build/engine-release-tags` for this partial [issue #11](https://github.com/sigaev-pro/bc-indexer/issues/11) block under D95. The coordinator's live product check selected legacy Docker Engine `v28.5.2` instead of the available `docker-v29.9.3`; the failed check remains historical evidence. This block checks and repairs the independent trusted selector for the same source/component distinction, with red-first synthetic tests. Allowed changes are the trusted updater and its tests, necessary contract prose, block evidence, and this status. Checks are focused release/updater/vector tests, documentation validation, and whitespace checks; GitHub runs full CI. Product scripts are owned separately. No active pin, catalog, controller, ledger, canonical run, or native runtime change is authorized here. Next: reproduce, repair, check, commit, push, and request independent review through a draft PR referencing #11. Native builds and runtime remain NOT_RUN.

The trusted defect is reproduced and repaired: Engine selection now accepts modern and legacy whole Engine tags, rejects unrelated module tags and unstable forms, compares numerically, and preserves exact tag/commit evidence. The initial three regression methods failed with eight assertions and six errors; the repaired focused suite passes all 30 methods, including the existing updater/vector and pagination controls. Documentation and whitespace checks pass: 107 documents, 1,104 local links, 84 catalog entries, 16 normative mirrors, and unchanged original v2. [Block evidence](evidence/E01-build-checks.md#docker-engine-release-selection-follow-up) preserves the failures and scope. Next: commit/push, GitHub CI, and independent exact-head reviews. This does not complete issue #11; native builds and runtime remain NOT_RUN.

## E01-01.builds — Standalone pagination repair checked under D95

2026-11-07: Workflow [PR #110](https://github.com/sigaev-pro/bc-indexer/pull/110) is accepted at `146746ee2a60d51450e7e3e4a96039c03c4ad689`. Direct author `d95-build-checks-author-001` owns the isolated `build/standalone-github-pagination` branch at that base. This [issue #11](https://github.com/sigaev-pro/bc-indexer/issues/11) block extracts the completed trusted GitHub pagination repair from retained `db07c163d3d5679308d04bb9b9c3b967973333c0`: exact official metadata, numeric aliases, retained source references, and logical loop rejection. Allowed paths are the updater, synthetic vectors, pagination tests, required verifier registration, pagination contract prose, this status, and the block evidence report. Required checks are focused updater/pagination tests, documentation validation, and whitespace checks; GitHub runs full CI. No profile or native runtime result is established by these synthetic controls. Next: implement the narrow extraction, check, commit, push, and open a draft PR referencing #11. The parent issue remains incomplete; controller state and the original failed candidate remain untouched.

The extraction is complete and byte-identical to the retained repaired files. All 24 focused tests pass; documentation and whitespace checks pass. Existing verifier registration already covers the updater modules and is unchanged. [Block evidence](evidence/E01-build-checks.md) records hashes, historical failures, checks, and remaining parent requirements. Next: commit and push this block, open a draft PR referencing #11, and obtain GitHub CI plus independent Spec and Standards review. No controller operation, native build, live acquisition, or canonical-state change occurred; runtime remains NOT_RUN.

## WORKFLOW-01 — Step 2 in progress under D95

The owner authorized the remaining agreed stages. Current block: [WORKFLOW-01, issue #109](https://github.com/sigaev-pro/bc-indexer/issues/109). The [direct workflow](docs/DEVELOPMENT_WORKFLOW.md) now defines prompt publication, exact-head review, protected merges, and the dependency-ordered queue. Controller launches and recovery are optional legacy operations. Product code and the accepted specification are unchanged. Documentation checks passed for 106 documents, 1,102 local links, 84 catalog entries, and 16 normative mirrors; all eight agent roles verified; whitespace checks passed. GitHub CI and independent exact-head review remain pending. The next block is actual client build verification under issue #11; the publication-only stop below is historical and superseded by D95.


## HANDOFF-PUBLISH-01 — Complete; all work published in draft PRs

The owner authorized step 1 only: inspect and stop owned background work, publish completed local commits in working branches and draft PRs, and report verified results and blockers. Do not start new implementation, review rounds, recovery, native builds, or merges in this block. See D94. This checkpoint concerns existing issues #11 and #105; it does not add a product task to the fixed catalog.

All worktrees were clean at intake. The interrupted AUT06 author session is recorded as cancelled with verified cleanup. Its retained supervisor diagnostic passed 323 tests with two opt-in Docker skips and all 54 negative controls on clean `db07c163d3d5679308d04bb9b9c3b967973333c0`; the earlier wrapper timeout remains retained. The supervisor, its process group, and all 28 recorded child groups are absent. No matching project process or running Docker container was found.

Published and remotely verified: draft [PR #106](https://github.com/sigaev-pro/bc-indexer/pull/106) at `db07c16`, draft [PR #107](https://github.com/sigaev-pro/bc-indexer/pull/107) at `7022c0d`, draft documentation [PR #108](https://github.com/sigaev-pro/bc-indexer/pull/108), and two archive branches retaining `f17bb19` and `7723a17`. GitHub CI passed for both code heads. Main remains `f917e73`; no merge or deployment occurred. See the [publication report](docs/PUBLICATION_HANDOFF_2026_11_07.md) for exact checks, retained failures, process cleanup, and blockers. The next product task remains issue #11, actual client build verification. This block ends with the handoff report; further implementation and workflow changes await agreement under D94.


## Точка продолжения

2026-11-07: **E01-01.builds PAGINATION_REPAIR_CHECKED / D93**. Invocation `d93-e01-builds-author-050` repaired numeric GitHub pagination only in `e01-native-builds-repair`. Numeric aliases now require retained metadata with the configured repository's exact full_name/canonical URL and positive integer ID; endpoint/origin, loop, ambiguity and page bounds remain enforced. All 11 focused and 51 isolated product tests passed. The original failed canonical operation and immutable worktree remain unchanged; maintenance #105 owns trusted validation/recovery. Next: commit this repair, run exact-head portable CI and return ownership. This repair's live check and all native compilation/runtime remain NOT_RUN; the previous canonical live failure remains retained.

2026-11-07: **E01-01.builds PAGINATION_REPAIR / D93**. Invocation `d93-e01-builds-author-050` owns only the new `e01-native-builds-repair` worktree on `feat/e01-native-builds-repair`, starting at `f17bb195d1baf62e93c596ed0fc067f2c6b60574` with accepted base `f917e73a7e5ec3de28110faddb6a924d2e04cf9e`. The original worktree and failed canonical verification are immutable evidence. The live source failure exposed named-to-numeric GitHub pagination. Next: bind numeric aliases to exact configured repository metadata, add independent positive/negative paging controls, run isolated tests and exact-head CI, then return clean ownership. No live request, credential lookup, trusted edit or canonical-state mutation is authorized here.

2026-11-07: **E01-01.builds REVIEW_FIX_02_CHECKED / D93**. Invocation `d93-e01-builds-author-047` reproduced and repaired the new P1 redirect disclosure and P2 pathname rollback race from both final reviews. The redirect guard rejects literal/encoded echoes before follow-up requests. Final output deletion is removed; report publication precedes the candidate's final exclusive commit, with unpromoted preparation blobs retained as diagnostic evidence. All 40 isolated product tests passed. Prior failures and both review rounds remain preserved. Next: commit, run exact-head portable CI, return ownership and obtain fresh reviews. Native/live/runtime acceptance remains NOT_RUN.

2026-11-07: **E01-01.builds REVIEW_FIX_02 / D93**. Invocation `d93-e01-builds-author-047` owns the two new findings from both REQUEST_CHANGES reviews on `2f601c4291081a5ab8861bd66b5921874a14261f`: prevent credential-bearing redirects before follow-up requests, and remove final-path rollback deletion based on stale ownership checks. Preserve the earlier fixes and failed review history. Next: reproduce both findings, implement guarded redirects and a candidate-last commit point, run isolated product tests, commit and run exact-head portable CI. No live credentials, acquisition or native execution is authorized here.

2026-11-07: **E01-01.builds AUT05_SYNC_READY_FOR_REVIEW / D93**. Invocation `d93-e01-builds-author-044` rebased the product onto accepted AUT05 PR #104 merge `f917e73a7e5ec3de28110faddb6a924d2e04cf9e` after the coordinator's epoch-5 source handoff. Both maintenance and product status histories, failed attempts and prior CI evidence are preserved. Product files are byte-for-byte unchanged from archived `7723a1704b7f0f43987611c6d218ecdd73eda256`. The configured isolated product suite passed all 36 tests in 13.328 seconds. The coordinator's separate 51-control diagnostic remains synthetic only. Next: commit this evidence update, run exact-head portable CI, return ownership and obtain both final reviews. Native/live/runtime acceptance remains NOT_RUN.

2026-11-07: **AUT-05 EXPANDED_REPAIR_CHECKED / D93**. All 17 focused updater tests pass after the independently reproduced Chart.yaml provenance-order repair. Full portable CI passed in 144.69 seconds: 292 tests with two opt-in Docker skips, component checks, 104 documents/1,086 links, all 84 catalog entries, 16 mirrors, and unchanged v2. [AUT-05 evidence](evidence/AUT-05.md) retains the failed diagnostics, the historical compression-only Spec PASS, the new regression failures, and checked module hashes. The compression fixture is unchanged from `dbd4c90`; all metadata/hash/version/server-compatibility and source-reference conditions remain required. Next: commit, return ownership for both fresh exact-head reviews, obtain protected acceptance, and safely hand off the accepted source. Product files, locks, canonical state, and ledger remain untouched; native builds and runtime remain NOT_RUN.

2026-11-07: **AUT-05 PROVENANCE_FOCUSED_CHECKS_PASS / D93**. The author independently confirmed that declaring the exact Chart.yaml URL caused rejection before that source was read. The unchanged source-reference gate now runs after required content authentication; all metadata/hash/version/server-compatibility checks remain intact. Five new methods first failed on the ordering defect, then all 17 focused tests passed, including rejection of recorded but unvalidated provenance. The compression-only Spec PASS and both diagnostic failures remain retained in [AUT-05 evidence](evidence/AUT-05.md). Next: full portable CI, commit, and fresh review on both axes. No product or canonical state changed; native builds and runtime remain NOT_RUN.

2026-11-07: **AUT-05 PROVENANCE_FIX_IN_PROGRESS / D93**. Reserved invocation `d93-aut05-author-041` owns the expanded issue #103 repair at `dbd4c90f32ab68430f5b9448ed47f24b9581deec`. The compression-only Spec PASS remains historical. A separate product diagnostic completed discovery but failed because the source-reference gate ran before Chart.yaml authentication. Next: independently reproduce the order defect, retain every metadata/hash/version/server-compatibility check, add positive and negative provenance tests, run focused and full portable CI, and commit for fresh review on both axes. Product files, active locks, canonical run, and ledger remain untouched; native builds and runtime remain NOT_RUN.

2026-11-07: **AUT-05 CHECKED / D93** ([evidence](evidence/AUT-05.md)). All 12 focused updater tests pass. Full portable CI passed on the repaired source in 124.59 seconds: 287 tests with two opt-in Docker skips, component checks, 104 documents/1,084 links, all 84 catalog entries, 16 mirrors, and unchanged v2. The report retains the initial missing-XZ failures and checked module hashes. Only this final evidence/status update follows that CI run; implementation bytes are unchanged. Next: commit the checked work, obtain both independent exact-head reviews and protected acceptance, then safely hand off the accepted verifier before retrying the product diagnostic. No product or active-lock changes, live acquisition, or native builds occurred; native/NATS runtime remains NOT_RUN.

2026-11-07: **AUT-05 FOCUSED_CHECKS_PASS / D93** ([evidence](evidence/AUT-05.md)). The synthetic transport now serves both gzip and XZ package indexes from identical records for all six channel/architecture pairs. New tests first reproduced missing XZ observations, then all 12 focused tests passed, including exact compressed hashes and complete check/update reports selecting either form or active-lock URLs. Existing negatives and source checks remain required. Next: full portable CI, checked commit, both independent review axes, and protected acceptance before a source handoff. No product files, locks, canonical run, or ledger changed; native builds and native/NATS runtime remain NOT_RUN.

2026-11-07: **AUT-05 IN_PROGRESS / D93** ([issue #103](https://github.com/sigaev-pro/bc-indexer/issues/103)). Reserved author invocation `d93-aut05-author-038` owns the isolated `aut/05-updater-fixtures` worktree at base `21eb4a7deab2808c008321136be33feaeb23d29d`. The synthetic updater transport currently omits supported XZ package indexes required by active-lock URLs. Next: provide gzip and XZ bodies from identical records for all six channel/architecture pairs, add independent coverage/hash/report checks, run portable CI, and commit for both independent review axes. Product files, active locks, acceptance rules, and canonical run state remain unchanged. The failed product diagnostic remains retained; native builds and native/NATS runtime remain NOT_RUN.

2026-11-07: **E01-01.builds AUT05_SYNC_IN_PROGRESS / D93**. Invocation `d93-e01-builds-author-044` owns synchronization onto accepted AUT05 merge `f917e73a7e5ec3de28110faddb6a924d2e04cf9e`. The coordinator completed the canonical epoch-5 source handoff and archived product head `7723a1704b7f0f43987611c6d218ecdd73eda256` at `archive/e01-builds-before-aut05-7723`. Preserve both maintenance and product history, all failed attempts and prior evidence. Next: rebase without product behavior changes, update evidence, run all 36 product tests and exact-head portable CI, then return ownership for both final reviews. No live acquisition, credential lookup or native execution is authorized in this block.

2026-11-07: **E01-01.builds AUTH_REPAIR_CHECKED / D93**. Invocation `d93-e01-builds-author-039` added bounded reuse of existing GitHub credentials with exact-origin authorization, cross-origin stripping and retained-evidence secrecy. Synthetic mode does not query credentials or invoke gh. All 36 independent product tests passed, including full mocked discovery and earlier publication/compression regressions. Next: commit, run exact-head portable CI, return ownership and obtain independent review. No real credentials were queried by the author; live acquisition, native builds and runtime acceptance remain NOT_RUN.

2026-11-07: **E01-01.builds AUTH_REPAIR / D93**. Invocation `d93-e01-builds-author-039` owns bounded reuse of existing GitHub credentials after the coordinator confirmed that unauthenticated discovery exceeds the public API allowance. Preserve the previous repairs and base. Next: implement credential precedence and exact-origin isolation, add independent failure/leakage tests, commit, run exact-head portable CI and return ownership. No login, live acquisition or native execution is authorized for this author block.

2026-11-07: **E01-01.builds REVIEW_FIX_01_CHECKED / D93**. Invocation `d93-e01-builds-author-037` reproduced and repaired both P2 findings while preserving the failed review and test history. Truncated gzip/XZ indexes now produce incomplete reports; output publication uses complete temporary files, exclusive hard links and retained inode ownership for rollback. Independent controls also cover concurrent replacements and both candidate/report I/O failures. All 26 product tests passed. Next: commit this checked repair, run exact-head portable CI, return ownership, then let the coordinator complete the separate trusted-source handoff and independent re-review. Native/live-source/runtime acceptance remains NOT_RUN.

2026-11-07: **E01-01.builds REVIEW_FIX_01 / D93**. Author invocation `d93-e01-builds-author-037` owns the repair of two independent P2 findings on `34332416c5ff02c3f859683fc2161959d6675ef5`: malformed compressed indexes must produce incomplete reports, and failed candidate publication must leave no owned candidate. Both REQUEST_CHANGES results remain preserved. Next: add focused failure tests, repair the product interfaces, run portable CI, commit and return ownership for independent review. Native builds and live-source/runtime acceptance remain NOT_RUN.

2026-11-07: **E01-01.builds READY_FOR_REVIEW / D93**. Author invocation `d93-e01-builds-author-034` implemented the closed plan, materialized artifact preflight, standalone 41-component updater, schema and documentation. Checkpoint `ec18504d7c509b3115194463ed7951c4796439ff` passed portable CI: 283 tests, two Docker skips, and 54 component negatives. The final product test set passes 18 independent synthetic cases. The evidence report preserves the intermediate decompression failure and repair. No trusted inputs, active locks, private oracle answers, or canonical run state were changed. Next: commit the checked final block, run exact-head portable CI, return author ownership, then obtain independent review and trusted actual execution. Native builds, live-source acceptance and all runtime results remain NOT_RUN.

2026-11-07: **E01-01.builds IN_PROGRESS / D93**. Author invocation `d93-e01-builds-author-034` owns implementation in `feat/e01-native-builds`. The author is reading the accepted field definitions and implementing the closed plan, materialized preflight, and complete inventory updater with independent synthetic tests. Native execution, live-source acceptance, independent review, and all runtime results remain NOT_RUN. Next: check and commit the product interfaces, then return ownership to the coordinator for exact-head review and trusted execution.

2026-11-07: **E01-01.builds AUTHOR_RESERVED / D93** ([issue #11](https://github.com/sigaev-pro/bc-indexer/issues/11)). AUT-04 is accepted through PR #102 at main merge `21eb4a7deab2808c008321136be33feaeb23d29d`; both independent reviews, local CI (283 tests, two Docker skips), and protected GitHub CI passed. D93 adoption completed on `run_D92_1794007047`, configuration epoch 4, with all earlier history retained and no campaign deadline. Old D92 ledger and accepted E01 evidence receipt bytes are unchanged. Author invocation `d93-e01-builds-author-034` is reserved before dispatch. This block implements the accepted build plan, materialized artifact preflight, standalone version updater, and independent product tests in the issue's allowed paths. Worktree: `.project/worktrees/e01-native-builds`, branch `feat/e01-native-builds`, accepted base `21eb4a7deab2808c008321136be33feaeb23d29d`. Next: dispatch the DevOps author, obtain exact-head independent review, then run actual trusted build verification and protected acceptance. Native builds and native/NATS runtime remain NOT_RUN.

2026-11-07: **AUT-04 REVIEW_FIX_01_CHECKED / D93** ([issue #101](https://github.com/sigaev-pro/bc-indexer/issues/101), [PR #102](https://github.com/sigaev-pro/bc-indexer/pull/102)). Initial candidate `ffda7fbc5561bafa45d3f7a47bd72ddb53d30989` received REQUEST_CHANGES on both independent axes despite passing CI. All five findings now have repairs: historical-author exclusion, operation-level frozen-source checks, inherited start-time completion, recorded policy in service templates, and consistent build-contract status. Full portable CI passed with 283 tests (two Docker skips), 54 component negatives, unchanged catalog/mirrors/v2, and focused regressions. Failed review reports and actual observations remain retained. Canonical adoption has not occurred. Next: commit this repair, obtain fresh exact-head reviews and protected acceptance, adopt D93, then continue E01-01.builds. Native builds and native/NATS runtime remain NOT_RUN.

2026-11-07: **AUT-03 OWNER_AUTHORIZED_REPAIR_03 / D92 addendum**. The owner authorized steps 1–3 and one additional bounded cycle: repair AUT03-R3-1, add the scheduling regression, run CI and both independent reviews, then merge PR #100 only if accepted. Root owns implementation attempt 4 / repair cycle 3; previous failures and counters remain retained. Issue #11 will not start. Keep the original D92 deadline and all acceptance requirements.

2026-11-07: **AUT-03 BLOCKED_REVIEW_LIMIT / D92**. Final review of `1c2c3d4fd2026a20565a00045472ecc2c5b79e18` returned Spec PASS and Standards REQUEST_CHANGES. One P2 remains: closing the candidate lifeline before explicit group cleanup races guardian self-termination and can reject a valid command. Both reviewers confirmed cleanup; all five earlier findings are resolved. Local CI passed 262 tests (two Docker skips) and 54 component negatives, and exact-head GitHub CI passed, but those checks did not catch this race. The limit of three implementations / two review-repair cycles is exhausted under DEVELOPMENT_AUTOPILOT:85 and D92. Draft PR #100 is not accepted or merged; accepted main remains `eda0a33fb6a8611ac661d5ae717709e1bab87030`, and E01-01.builds remains blocked. Next: obtain explicit authorization for one additional bounded repair cycle, keep the lifeline open through cleanup, add the scheduling regression, and repeat both exact-head reviews. Preserve the original deadline and all attempt history. No native builds or native/NATS runtime have run.

2026-11-07: **AUT-03 REVIEW_FIX_02 CHECKED / D92**. Re-review of `f624f56de88cc79bae6f95b9c1b8e99a3b56cbe4` confirmed all three original repairs, but rejected two new process-ownership defects: a nested candidate could survive outer driver termination, and normal cleanup signaled a group after reaping its leader. Both reviewers returned ownership after cleanup. Implementation attempt 3 / repair cycle 2 binds candidate groups to a parent lifeline, retains unreaped leaders, and includes nested-group absence in outer cleanup evidence. Targeted tests passed and full CI exited 0 in 114.36 seconds. Next: commit and obtain both final exact-head independent reviews. Preserve both failed reviews and the original deadline; no acceptance yet.

2026-11-07: **AUT-03 REVIEW_FIX_01 CHECKED / D92**. Both independent reviews of `453315a19dc2abc8c55dee20a5a18f3cb562d360` returned REQUEST_CHANGES and confirmed cleanup. Root repaired source-contained Prysm module replacements, immutable external-lock fingerprints, and final artifact validation after candidate commands and child-process cleanup. Targeted tests passed; repaired full CI exited 0 in 104.34 seconds. Next: commit this repair and obtain both independent exact-head re-reviews. Preserve the failed review and the original deadline; native builds and runtime remain NOT_RUN.

2026-11-07: **E01-01.evidence ACCEPTED; AUT-03 READY FOR REVIEW / D92**. Product PR #99 is accepted at main merge `eda0a33fb6a8611ac661d5ae717709e1bab87030`: both independent axes passed, supervised verification passed 207 bootstrap tests, 90 product tests, and 138 trusted outcomes, and the actual merge/receipt/issue closure were verified. AUT-03's checked substep is commit `e194d40`; integration preserves both status histories and the accepted product files. Integrated CI passed in 101.11 seconds: 254 tests (two opt-in Docker cases skipped), 54 component negatives, 101 documents, and 1,075 local links. Next: commit the checked integration and obtain independent exact-head review. Native builds and native/NATS runtime remain NOT_RUN.

2026-11-07: **AUT-03 candidate checks passed / D92**. The complete build/updater adapter and its frozen CLI/field contract pass full offline CI: 254 tests and all 54 component negatives in 101.00 seconds. Earlier document-tracking and whitespace failures are retained in the evidence report. Root now owns the candidate after the provider-limited author ended. Next: commit this checked substep, integrate accepted E01 evidence main `eda0a33fb6a8611ac661d5ae717709e1bab87030`, then complete independent exact-head review. Actual native builds and native/NATS runtime remain NOT_RUN.

2026-11-07: **AUT-03 IN_PROGRESS / coordinator takeover**. The delegated author stopped at the provider usage limit before completing the candidate. The owner requested continuation. Root preserved the committed resource guard and every uncommitted adapter change, confirmed no remaining author processes or running containers, and took sole write ownership. The next action is to finish the contract and fixed-command checks, run the full suite, integrate accepted E01 evidence main, and obtain independent exact-head review. No product build or native/NATS runtime is accepted. The original D92 deadline and shared counters remain unchanged.

2026-11-07: **AUT-03 owned-resource substep verified / D92**. Ten Docker ownership/creation-intent tests, five signal tests, and three backend tests pass. Authorized disposable fixtures proved live daemon cleanup after worker timeout and parent-watchdog termination; immutable IDs and limits are recorded in the AUT-03 evidence report. Full tracked-tree CI passed 219 tests and component checks in 106.11 seconds, with 99 documents, 1,067 links, 16 mirrors, 84 catalog entries, and unchanged v2. No native build ran and no build recipe is enabled. Next: commit this supervision substep, then implement the constrained task adapter and independent build/updater proof. The fixed launch deadline and prior task history remain unchanged.

2026-11-07: **AUT-03 IN_PROGRESS / D92, invocation 019**. The assigned maintenance implementer now owns this worktree after draft commit `5536fa2c448074b47a70b11a205afbed5bf3e1d3`. Work will add exact task adapter selection and trusted build execution/provenance, including independent updater checks and owned Docker cleanup. The existing evidence path and retained receipts must remain valid. A missing execution or cleanup proof will block the build recipe, not become a fixture-based PASS. No product code, native build, service, canonical run, or remote state is changed by this handoff. The original deadline `1794032247` remains fixed.

2026-11-07: **AUT-03 INTERFACE_DRAFT / D92**. Maintenance issue #98 owns the separate trusted acceptance adapter required for E01-01.builds. The coordinator prepared the draft build/updater interface in this isolated worktree based on accepted main `41b9cd9f14445f373d3ac7d282f78aa1e8dd6a34`. Document checks passed 98 documents, 1,067 links, 16 mirrors, 84 catalog entries, and unchanged v2. The draft preserves the existing evidence adapter, exact task/source bindings, shared deadline and attempts, independent reviews, and protected acceptance. Next: resolve the concrete CLI/manifest interface, implement the independent adapter and its failure checks, and obtain exact-head review before enabling any build recipe. E01-01.evidence is under independent review elsewhere; its branch remains read-only. No build task is accepted or enabled by this preparation commit. Deadline `1794032247` is unchanged.

2026-11-07: **E01-01.evidence REVIEW_FIX_01 READY_FOR_REVIEW / D92, implementation attempt 2**. Reserved invocation `d92-e01-evidence-repair-018` repaired both independently reproduced P2 findings without changing schemas or the trusted verifier. New raw CLI tests first failed on five lossy-numeric/invalid-offset counterexamples. Exact decimal validation now rejects unsupported precision before coercion while preserving supported D91 hash bytes, valid integral spellings, ordinary fractions, and exact fractional deadlines. Timestamp component checks reject normalization of invalid offsets. All 90 product tests and the unchanged accepted verifier's 138 artifact-conformance outcomes pass; runtime remains NOT_RUN. Exact-head bootstrap CI follows the repair commit, then ownership returns for independent re-review and supervised acceptance. Prior attempts/review results remain retained. No remote or canonical runtime mutation occurred; the fixed deadline remains `1794032247`.

2026-11-07: **E01-01.evidence REVIEW_FIX_01 / D92**. Both independent reviews of `19d455ba6ce846b28771faad86068e3c6b80c22c` requested two P2 fixes: lossy JSON float decoding admits fractional schema versions and integer seeds, and timestamp parsing normalizes invalid RFC3339 offset minutes. Both reviewers reproduced the real CLI counterexamples. The existing 79 product tests and 138 trusted outcomes passed; Standards also passed exact-head bootstrap CI with 207 tests and 54 component negatives. These suites missed the defects. Next: a separate implementer must add raw CLI regressions, preserve valid integral numbers and valid offsets, repair the boundaries, run checks, and commit for independent re-review. The original author attempt and both reports are retained; no acceptance or runtime PASS is claimed.

2026-11-07: **E01-01.evidence READY_FOR_REVIEW; D92 CONFIGURATION EPOCH 2**. The integrated product tree passed 207 bootstrap tests and component checks in 104.41 seconds, plus all 79 isolated product tests in 5.38 seconds. Documentation checks passed 99 documents, 1,071 links, 16 mirrors, 84 catalog entries, and unchanged v2. The accepted controller's real safe handoff completed in 82.04 seconds at revision 8, epoch 2, with all task records, authorization, deadline, limits, and 15 consumed invocations preserved. The run remains STOPPED; only the coordinator is active. Next: commit the integrated candidate, reserve and run two independent exact-head product reviews, adopt the actual completed author/review provenance, then explicitly resume trusted verification and protected publication. Native runtime remains NOT_RUN.

2026-11-07: **E01-01.evidence INTEGRATING; AUT-02 ACCEPTED / D92**. Protected PR #97 merged as `41b9cd9f14445f373d3ac7d282f78aa1e8dd6a34` after both independent review axes passed, exact-head CI passed 207 tests and 54 component negatives, and fresh acceptance plus actual merge parent/tree/main checks succeeded. The original BOOT-09 dependency receipt is unchanged. The coordinator now owns integration of completed product candidate `dc4b9cb832f8414500294c16021d8ec645f20d33`; only the status history conflicted, and both histories are retained. Next: verify the integrated product tree, commit, safely hand off the accepted source on the same D92 run, obtain exact-head product reviews, and run real supervised adoption/verification/publication. The launch has consumed 15 of 200 invocations; deadline `1794032247` remains fixed. AUT-03 issue #98 records the separate acceptance adapter required before E01-01.builds can run. Product and native runtime acceptance remain pending.

2026-11-07: **E01-01.evidence READY_FOR_REVIEW / D92**. All required evidence artifacts are implemented, including D87 ReaderBoundaryManifest schema version 1 and its validation kind. The full product suite passes 79 test methods. Final working-source CI passes 161 bootstrap tests, 54 component negatives, 95 documents/1062 links, 16 mirrors, all 84 issue mappings, and unchanged original v2. The coordinator's independent working-source diagnostic passes 138 conformance outcomes; final exact-head review and supervised acceptance remain pending. The five-kind substep is commit `9262ecc756c76ed97b645b82122c9146a02ce6e4`; the complete handoff commit follows this status update. All changes remain within assigned paths. No remote mutation or deployment occurred, and native runtime remains NOT_RUN. Next: coordinator integration only after accepted maintenance main, exact-head independent review, and the protected acceptance workflow.

2026-11-07: **E01-01.evidence IN_PROGRESS; D87 implementation verified / D92**. Substep commit `9262ecc756c76ed97b645b82122c9146a02ce6e4` supplies the original D91 interface. The additional version-1 ReaderBoundaryManifest schema and validation kind now follow the coordinator's exact AUT-02 contract. All 79 product tests pass. The coordinator's independent working-source diagnostic passes 138 outcomes using AUT-02 candidate `b79dba598fa4acb574b2db4e76c96323d59e9e3b`; it is not an acceptance proof. The implementation distinguishes six reader conditions, preparation NOT_RUN, truthful FAIL/declared-negative evidence, canonical run output binding, exact case/profile identity, and fractional phase timing. Native runtime remains NOT_RUN. Next: final source checks and commit, then wait for accepted maintenance main and exact-head independent review before integration.

2026-11-07: **E01-01.evidence IN_PROGRESS; D91 artifact substep verified / D92**. The schema-v3 registry/result/run artifacts, seven-profile registry, composite/prerequisite schemas, and stdlib validation CLI pass 47 independent tests and all 54 trusted vectors plus four profile expansions. Required working-source CI passes 161 bootstrap tests, 54 component negatives, 95 documents/1062 links, and unchanged source/spec checks. D87 also assigns the ReaderBoundaryManifest schema to this task. The coordinator is preparing its exact field contract and independent trusted adapter; that required schema remains pending, so this task is not READY_FOR_REVIEW or accepted. Native runtime remains NOT_RUN. Next: commit this verified substep, implement the D87 interface, then obtain independent exact-head review and acceptance through the coordinator.

2026-11-07: **E01-01.evidence IN_PROGRESS / D92**. The assigned worker owns `test/evidence/`, its evidence report, and this worktree status on `feat/e01-evidence`, starting from `a076de93d948b08892171fd84dd5545cc344eaf6`. Work covers schema-v3 CaseRegistry, CaseResult, RunManifest, the seven-profile registry, validation CLI, independent conformance tests, and documentation. The accepted contracts and corpus remain unchanged. Schema conformance checks do not establish native runtime results; runtime remains NOT_RUN. The fixed launch ends at 2026-11-07 06:17:27 UTC. Next: read the full D82/EVID obligations and implement the evidence artifacts for independent review.
2026-11-07: **AUT-02 READY_FOR_REVIEW / D92, implementation attempt 2**. Both independent axes requested the same P2 correction at `547c8fb70b5ff7e474fa9cc1b4ba1610717b57c9`: JSON `true` passes the required reader schema version check because Python equates it with `1`. Both initial exact-head CI runs passed 205 tests and 54 component negatives; those passes did not cover this defect. The new regression failed before the fix; an exact integer guard now passes all 12 targeted tests, including isolated full-driver rejection. Repaired-tree CI passed 207 tests and component checks in 104.12 seconds. Reservations 014/015 succeeded for new independent reviews, bringing the shared total to 15 of 200. Next: commit, exact-head re-review, protected acceptance, and safe handoff. Prior findings remain retained. No product/runtime acceptance occurred; deadline `1794032247` is unchanged.

2026-11-07: **AUT-02 READY_FOR_REVIEW; D92 STOPPED**. Accepted AUT-01 main is integrated. Full integrated CI passed 205 tests and the required component checks in 103.46 seconds; conflict resolution preserves both the reader schema requirement and isolated product tests. Two independent BOOT-09 maintenance review reservations for AUT-02 were created through the accepted controller API before dispatch. The canonical run is now revision 5, epoch 1, with 13 of 200 invocations consumed and deadline `1794032247` unchanged. Next: commit this integration, review the exact head, obtain protected acceptance, and safely hand off the new accepted source before product acceptance. No native runtime result is accepted.

2026-11-07: **AUT-01 ACCEPTED; AUT-02 INTEGRATING; D92 STOPPED**. [PR #96](https://github.com/sigaev-pro/bc-indexer/pull/96) merged as `c9ebf91044b72c0d554707ebc735a5b0ec77dff7` after both independent review axes and exact-head CI passed 195 tests and 54 component negatives. Fresh acceptance and merge parent/tree/main checks passed; the original BOOT-03 dependency receipt is unchanged. The canonical D92 run `run_D92_1794007047` is initialized and stopped at configuration epoch 1, revision 3: 9 DONE, 75 PLANNED, 11 of 200 invocations used, deadline `1794032247`. Preflight completed in 170.22 seconds without product dispatch; the completed D86 run remains byte-identical. Current work integrates the accepted source into AUT-02 while preserving its required reader schema and the explicit product unittest command. Next: local checks, merge commit, independent exact-head review, protected acceptance, and safe configuration handoff before E01 product acceptance.

2026-11-07: **AUT-02 READY_FOR_REVIEW / D92**. Maintenance issue #95 closes the existing D87 ReaderBoundaryManifest gap in the trusted E01-01.evidence handoff and verification pack. Added 80 independent reader artifact inputs while preserving the original 54 inputs and four profile expansions. Local CI passed 171 tests and 54 component negatives; details and remaining acceptance are in [AUT-02 evidence](evidence/AUT-02.md). Integrate accepted AUT-01, obtain independent exact-commit review and protected CI, then adopt the source through safe handoff. The schema remains an E01 artifact; runtime boundary and reader proofs remain NOT_RUN. Preserve the fixed D92 deadline and shared budget.

2026-11-07: **AUT-01 REVIEW_FIX_02 / D92, implementation attempt 3**. Both reviewers confirmed the descendant-cleanup and parallel-review fixes at `c4467bf0b4f427bb0fc773bb8b32c8e93f6046b7`, with independent 190-test/54-negative CI PASS, but reproduced a new signal-composition defect during resume preflight. Real SIGTERM/SIGINT regressions reproduced the missing durable stop on that head. The final normal repair forwards caller interruption through an explicit supervisor callback, scopes/restores signal handlers, acknowledges only the prior stop before setup, persists interrupted preflight status, and blocks automatic recovery before backend construction. All 62 targeted tests pass, including empty-group cleanup checks and repeated zero-effect automatic recovery. Invocation `d92-preinstall-aut01-repair-009` was reserved before dispatch. Next: clean repair commit, exact-head full CI with a 300-second bound, and independent re-review. No new product/model execution, remote mutation, service, or canonical runtime write occurred. Deadline remains `1794032247`.

2026-11-07: **D92 continuation handoff; acceptance still pending**. The coordinator reports clean E01-01.evidence candidate `dc4b9cb832f8414500294c16021d8ec645f20d33` with 79 product tests and clean AUT-02 trusted-reader candidate `b79dba598fa4acb574b2db4e76c96323d59e9e3b`. Its diagnostic against the product candidate passed 138 outcomes; neither candidate is accepted. After AUT-01 acceptance, integrate and independently review AUT-02, adopt the accepted trusted-source configuration through safe handoff, then obtain exact product review and supervised acceptance. Pinned native sources and three build/runtime images are cached as preparation only; no native execution occurred. The private preparation note is `.project/automation/runs/d92-20261107/e01-builds-preflight.md`. Every continuation retains deadline `1794032247` and the shared invocation/attempt history.

2026-11-07: **AUT-01 REVIEW_FIX_01 / D92, implementation attempt 2**. Independent Standards review passed at `bba8668756d4cc2f9752c0d439087d96ae38b5e9`; Spec review requested changes for an ordinary-command descendant surviving timeout and a reservation guard that prevented parallel Standards/Spec reviewers. Both defects were reproduced by failing isolated regressions. Ordinary commands now reuse the existing bounded process-group runner with private retained cleanup evidence and no serialized inherited environment; remote mutation outcomes remain uncertain after interruption. Parallel read-only reviewers are allowed within the same three-slot cap, while overlapping writers remain blocked. Twenty-nine focused launch tests pass, including resistant descendants and unconfirmed-cleanup blocking. Next: full and exact-head CI, clean repair commit, and independent re-review. Invocation `d92-preinstall-aut01-repair-006` is coordinator-reserved before dispatch and will be included honestly in the one-time pre-installation accounting import. Hard deadline remains `1794032247`.

2026-11-07: **AUT-01 implementation ready for final checks / D92**. The fixed launch ledger, deadline enforcement, explicit stopped initialization, coordinator-session accounting, exact-candidate adoption, and revision/epoch-checked configuration handoff are implemented. Twenty-one isolated launch regressions pass, including real Git provenance checks; the prior automation suite also passes. All future product adapters remain blocked until reviewed, and the existing trusted proof/publication path remains mandatory. [AUT-01 evidence](evidence/AUT-01.md) records scope and validation limits. Next: tracked-file CI, clean commit, exact-head checks, and independent review. No real product/model execution or remote mutation occurred here. The hard deadline remains `1794032247`, including all continuations.

2026-11-07: **AUT-01 implementation started / D92**. Maintenance issue #94 is read and scoped outside the fixed 84-task catalog. The controller worktree will add a canonical D92 launch ledger with the fixed Unix deadline `1794032247`, a shared 200-invocation limit, and an explicit safe configuration handoff that preserves the run's history and work. Existing D86 runs remain immutable. The current adapter has no supported coordinator-session accounting or external-candidate adoption API; the proposed narrow extension is reported to the coordinator before implementation. Only isolated tests and local commits are authorized in this worktree; no model session or remote mutation may run here. The seven-hour window includes setup and every continuation.

2026-11-07: **D92 ACTIVE; AUT-01 IN_PROGRESS; E01-01.evidence READY**. The owner authorized seven hours of autonomous work, including required automation changes, starting at 02:17:27 and ending at 09:17:27 Europe/Moscow. Accepted base is `6a486bf48a8182ca4e53d60a19f6d8708b0c283a`; BOOT remains complete. AUT-01 will enforce the fixed launch deadline and preserve budgets, attempts, receipts, and work across reviewed configuration updates. E01-01.evidence is the first product task; its dependencies are accepted and its trusted verifier is available. The coordinator will use isolated worktrees and independent review, then publish and merge through the existing protected workflow. Current runtime remains NOT_RUN. No production deployment or requirement change is authorized. Next: record the maintenance issue, complete the launch configuration, and implement the evidence schemas under the same seven-hour window.

2026-11-07: **BOOT COMPLETE / D86, D91**. All nine bootstrap issues are accepted. PR #92 merged the final BOOT-09 candidate as `17d421a0c6aec96c2af82dbc9080eda36849a692`; issue #9 acceptance comment is `6697157355`. The canonical controller run `run_466b9ee5481a45f99b682d94f00c0309` finished COMPLETE with nine DONE tasks, 75 PLANNED tasks, zero model invocations in this run, and no blocker. Its authorization remains D86/BOOT. Exact-head CI passed 161 tests and 54 component negative checks; both independent reviews passed. The final handoff changes only this status, the runbook's startup note, and [BOOT-09 evidence](evidence/BOOT-09.md), which records acceptance, the persisted run, its completion hash, and verification limits. GitHub access works through D88's existing SSH setup. **Next: wait for a new owner launch before E01-01.evidence.** Only that initial artifact task is configured; the later eleven E01 tasks and G.E01 require reviewed verification contracts and a budget-preserving handoff. Product schemas and native runtime remain NOT_RUN. No service was installed or production deployment performed. This launch has stopped before E01.

2026-11-07: **BOOT-09 REVIEW_FIX_01 READY_FOR_REVIEW / D86, D91**. Clean repair commit `912bae9cd040de32489a234e81931d479811b9cf` passes exact-head CI with 161 tests and 54 component negatives. Its single bounded live read-only bootstrap check returns PASS for all eight prerequisites and all 84 issues, while keeping E01 unauthorized and product runtime NOT_RUN. The report records the updated driver and handoff hashes; vector content, proof adapter, recipes, and D86 remain unchanged. This final documentation update changes no implementation or trusted-pack bytes. Next: independent exact-head re-review and coordinator acceptance; no product, model, service, or remote mutation ran.

2026-11-07: **BOOT-09 REVIEW_FIX_01 implementation verified / D86, D91**. All 161 local tests and 54 component negative cases pass. The handoff now permits empty stdout for ordinary validation while requiring checked ExpandProfiles JSON and all artifact files. Either stdout or stderr reaching 65,536 bytes fails regardless of an expected exit code. New regressions reproduced eight pre-fix failures, then passed for caught overflow and exact-cap writes on both streams with exit 0 and 2; empty ordinary output and missing expansion output are also covered. The 54-vector source body and four expansion calls are unchanged. Next: commit, exact-head CI, one bounded read-only live check, and independent re-review. No product or model execution, service, or remote mutation ran.

2026-11-07: **BOOT-09 REVIEW_FIX_01 IN_PROGRESS / D86, D91**. Independent review of `d6b11e0b6a12af6e3b13610a1973f9c6740f9231` requested two focused corrections: ordinary validation uses authoritative exit codes and permits empty stdout, and a candidate must fail when either output stream reaches 65,536 bytes even if it catches the write-limit error and exits with an expected code. This first repair preserves all 54 oracle vectors and the four profile-expansion calls. No product artifact, model session, native runtime, service, or remote mutation is authorized.

2026-11-07: **BOOT-09 READY_FOR_REVIEW / D86, D91**. Clean implementation `41015c8043431829f4d72fb08ef713f113b4fb0f` passes exact-head CI with 158 tests and 54 component negatives. Its bounded read-only live verification returns PASS for accepted BOOT-01 through BOOT-08 and reconciles all 84 issues. The report identifies the configured initial E01-01.evidence contract and exact trusted-pack hashes, lists the other twelve E01 nodes as unconfigured, and explicitly reports `e01_authorized: false`, `product_verification_configured: false`, and runtime NOT_RUN. This final status/evidence update changes no implementation or trusted-pack bytes. BOOT-09 still requires independent exact-head review and coordinator acceptance. No actual E01 artifact, native runtime, model session, service, or remote mutation ran; stop before E01.

2026-11-07: **BOOT-09 implementation verified / D86, D91**. Working-source CI passes 158 tests, 54 component negative cases, 16 mirrors, and 93 documents/1051 links. The initial E01-01.evidence path now has a frozen trusted verifier, exact output recipe, bounded validation driver, and public coordinator proof binding enforced before capture/merge and on receipt replay. Its 54 oracle bundles and four profile-expansion calls are synthetic bootstrap inputs; disposable protocol stubs are not product schema implementations. D86 remains the sole authorization, with eleven later E01 tasks and G.E01 explicitly unconfigured. No E01 artifact, native runtime, model session, service, or remote mutation ran. Next: commit, exact-head CI, one clean-source read-only bootstrap check, and independent review.

2026-11-07: **BOOT-03 DONE; BOOT-09 IN_PROGRESS / D86, D91**. BOOT-03 was accepted through PR #90 as main `24ae66d72512427e0e1fd7dc281640035c37db51`, with issue #5 acceptance comment `6695975227`, review `6695941742`, and CI run `42295448825`. The exclusive BOOT-09 worktree now adds a narrow trusted verifier and retained-proof adapter for the future E01-01.evidence artifact task. D86 remains the only launch authorization and permits BOOT only. No product schema, product runtime, model session, service, or remote mutation is authorized now. Remaining E01 tasks must stay blocked until independently reviewed verifiers exist. Next: implement and test the bootstrap extension, verify accepted dependencies read-only, and obtain independent exact-head review.

2026-11-07: **BOOT-03 REVIEW_FIX_02 READY_FOR_REVIEW / D86, D89**. Integrated clean code commit `9354bea60e2f253363fbb883133cdef691883f52` against accepted main `29a626d4cc3a7238521113f386a7273fb58c45d1` passes exact-head CI: 144 tests, 54 component negatives, 16 mirrors, and 91 documents/1044 links. A fresh read-only live bootstrap check on that exact clean commit reconciled all 84 issues and authenticated BOOT-01/02/04/05/06/07/08, correctly blocking only on unaccepted BOOT-03. It reports E01 unauthorized, product verification unconfigured, and runtime NOT_RUN. Durable receipt revalidations bind the clean commit and six executing module hashes. This final status/evidence update changes no implementation bytes. Next: independent re-review, coordinator publication and exact-head acceptance; this is the second and final authorized repair cycle. No real model session, installed service, or remote mutation ran.

2026-11-07: **BOOT-03 integrated with accepted BOOT-08 / D86, D89, D90**. Repair commit `565dbb5cb3dad243c6955a92741d7cb13ab95be1` is integrated with accepted main `29a626d4cc3a7238521113f386a7273fb58c45d1` through a normal merge. Both status and decision histories are preserved; accepted BOOT-08 corpus, validator, specification, mirrors, and evidence are unchanged. Integrated working-source CI passes 144 tests and 54 component negatives. Next: exact committed-source CI, read-only bootstrap verification, and independent re-review of the final candidate. BOOT-03 remains unaccepted; no E01 or product runtime execution is authorized.

2026-11-07: **BOOT-03 REVIEW_FIX_02 implementation verified / D86, D89**. All 95 local bootstrap tests, 54 component negative cases, and document checks pass. Service templates now use automatic recovery that preserves durable stops, budget stops, and blockers. Explicit resume can replace a stopped session only with intact retained evidence and confirmed process-group cleanup; linked history and charged attempts survive crashes. Failed model sessions require a separate bounded diagnostic retry; failed verification remains blocked under unchanged D83 semantics. Next: integrate accepted BOOT-08 main `29a626d4cc3a7238521113f386a7273fb58c45d1`, run exact committed-source CI and read-only bootstrap verification, then independent re-review. No model session, service, or remote mutation ran; product runtime remains NOT_RUN.

2026-11-07: **BOOT-03 REVIEW_FIX_02 IN_PROGRESS / D86, D89**. Independent re-review of `035c88f9c8fb787d04716add6a08aa36b05c2a62` found that service startup could clear a durable operator stop. This second and final authorized repair separates automatic recovery from explicit resume and investigates retained stopped or failed session replay. Recovery must retain prior evidence, charge new attempts, and block uncertain cleanup. The earlier watchdog and terminal lease fixes remain in place. No model session, service installation, or remote mutation is authorized; product runtime remains NOT_RUN and E01 is blocked.

2026-11-06: **BOOT-03 REVIEW_FIX_01 READY_FOR_REVIEW / D86, D89**. The two requested recovery corrections and their regressions pass all 84 local CI tests, component validation, and document checks. The real spawn regression synchronizes at the supervisor's pre-Popen boundary; the terminal-state regression checks crashes on both sides of save and release without duplicate closure. This is the first authorized repair cycle after review of `85918b5`. Exact repaired-head CI and independent re-review are required before coordinator acceptance. D86 remains BOOT only, product runtime remains NOT_RUN, and no remote mutation or model session ran.

2026-11-06: **BOOT-03 REVIEW_FIX_01 verification / D86, D89**. Both confirmed review defects were reproduced and corrected. The watchdog keeps the supervisor stopped from child enumeration through final termination, preventing a worker spawn after inspection. DONE is now saved before generation-checked lease release; reconciliation completes terminal cleanup without repeating issue closure. New regressions cover a real pre-Popen stop with a self-bounded worker and all four terminal-save/release crash boundaries. All 32 focused controller/process tests pass. Next: full exact-head CI, commit, and independent re-review. No new model session or remote mutation ran; stop before E01.

2026-11-06: **BOOT-03 REVIEW_FIX_01 IN_PROGRESS / D86, D89**. Independent review of `85918b5bfd9a4b955a415c3493af49cd062760a8` requested two corrections: the watchdog could resume a supervisor that had not yet spawned its worker, and task lease release preceded durable DONE state. This repair is limited to closing those recovery windows and adding regressions. Preserve BOOT-09 preflight, D86's BOOT-only limit, the four historical exceptions, and existing acceptance checks. No remote writes or real model sessions are authorized for this repair.

2026-11-06: **BOOT-03 READY_FOR_REVIEW / D86, D89**. Integrated implementation `6bb1f908e8ecc6cdc46adb74db7f2ea5d8331693` passes clean exact-head CI with 82 tests and component validation. Read-only live verification of that exact clean source reconciled all 84 issues, authenticated BOOT-01/02/04/05/06/07, and correctly returned BLOCKED with only BOOT-03/08 missing. It explicitly reports `e01_authorized: false`, unconfigured product verification, and product runtime NOT_RUN. Durable receipt revalidations record the clean executing commit and all six module hashes; earlier working-source provenance is distinguished. No real model session or GitHub mutation ran. Next: coordinator independent review, fresh remote exact-head CI/review evidence, durable capture, verified merge, and issue acceptance. BOOT-03 is not self-accepted; D86 still stops before E01.

2026-11-06: **BOOT-03 candidate ready for committed-source verification / D86, D89**. The final local correction records denied or uncertain process-group cleanup and rejects it even when the process exit code is zero. Its regression brings the controller/bootstrap suite to 82 tests. The integrated candidate preserves accepted BOOT-06/07 code and decisions. Next: exact-head CI, a read-only live receipt revalidation with clean source provenance, independent review, and coordinator publication. BOOT-03 remains unaccepted and product runtime NOT_RUN; stop before E01.

2026-11-06: **BOOT-03 integrated with accepted BOOT-06/07 main / D86, D89**. Controller implementation `af7c4b55edb922589f5bafb2665cecdd38c7867e` passes exact-head local CI with 81 tests. Accepted main `41d9ed0627fe4822bb3ffc2e1c043a84f18af406` is integrated through a normal merge; only status and decision histories conflicted, and both histories are preserved. D87, D88, and D89 remain distinct. Integrated CI and component validation pass. BOOT-07 is accepted through PR #89; BOOT-08 is independent ongoing work. Committed-source live revalidation and independent controller review remain next. No product runtime or E01 authorization is claimed.

2026-11-06: **BOOT-03 implementation validation in progress / D86, D89**. The controller, durable receipt importer, bounded session supervisor, task recipes, service templates, and isolated failure suites are implemented. The public run guard requires accepted BOOT-09 and preflight; the scheduler follows the reviewed authorization's ordered stage bound, with D86 fixed at BOOT. Provisional working-source live verification reconciled all 84 issues and authenticated BOOT-01/02/04/05/06/07, then correctly reported BOOT-03/08 missing. Source provenance distinguishes working files from checkout HEAD. No real model session or GitHub mutation ran. Next: finish exact committed-source checks, merge accepted main, obtain independent review and live acceptance, and stop before E01. Details and fault evidence: [BOOT-03](evidence/BOOT-03.md).

2026-11-06: **BOOT-03 capture durability correction ready for review / D86**. Independent review of substep `d138a76` found that newly created nested directories also need their parent entries synced. The shared atomic-write primitive now creates and syncs each directory level before descending and covers an interrupted attempt on retry. Two focused fault tests were added. Scheduling and the remaining controller implementation are still in progress; no BOOT-03 acceptance, remote mutation, or E01 launch is claimed.

2026-11-06: **BOOT-03 IN_PROGRESS: durable capture substep ready for review / D86**. `automation/evidence.py` now retains the complete passing BOOT-05 collector snapshot and verdict atomically before a coordinator merges. It requires exact requested head/base and task identities, syncs storage, preserves earlier captures, and writes only to an explicitly selected ignored directory. All 42 local bootstrap tests and document checks pass, including 11 new isolated capture tests. No real model session or GitHub mutation ran. The coordinator will independently review this substep before using it for pending bootstrap PRs. Scheduling, leases, historical imports, stop/recovery, and controller failure tests remain in progress; this is not BOOT-03 acceptance or E01 authorization.

2026-11-06: **BOOT-05 DONE; BOOT-03 IN_PROGRESS / D86, D88**. PR #87 merged independently reviewed candidate `c102162ead6ca4bce575831fe28eb6682e82956c` as accepted main `504b711dca4eb1c621f14ef54102102c3739248d`, with tree `911903efee26d3e070f89a6bfa183087a5d805ca`. Exact-head Actions run `42281765023`, review comment `6693795983`, status `58285478798`, and the live acceptance collector passed. Issue #4 is closed with acceptance comment `6693814923`. Live-evidence comment `6693807028` records collector snapshot SHA-256 `b74bb970a697bcf61e56c6c7eeb1fa0432e95f5e864b752108f5380f9763970c`; the original snapshot bytes were not retained. BOOT-03 will explicitly import the public post-merge proof without claiming to reconstruct those bytes. BOOT-03 now implements the persistent controller and isolated failure tests in its assigned worktree. Historical bootstrap acceptance requires explicit verified receipts; closed issues and worker output are not acceptance. This launch remains BOOT only. Product runtime is NOT_RUN, and E01 must not start.
2026-11-06: **BOOT-08 READY_FOR_REVIEW / REVIEW FIX 1 / D90**. Repaired the structural gaps reported against 452b40a408c0915f3952705357631c9b68e8a97b: typed oracle/provenance contracts, required resolution fields, explicit owners checked against all 68 real tasks and their stages, and direct campaign rejection despite a fresh digest. Corrected E07-02.e2e task-name parsing and added the existing schema-v3 DOC projection without fabricated runtime hashes or new fixture enums. All 80 portable tests (49 corpus tests), 54 component negatives, 16 mirrors, 90 documents/1040 links, stable IDs and unchanged v2 pass. Accepted base remains 41d9ed0627fe4822bb3ffc2e1c043a84f18af406. Next: commit, exact-head CI and independent review; wait for the coordinator's accepted main before integration. No remote mutation or E01 work.

2026-11-06: **BOOT-07 DONE; BOOT-08 READY_FOR_REVIEW / D86, D90**. Accepted base is 41d9ed0627fe4822bb3ffc2e1c043a84f18af406 after BOOT-07 PR #89 and issue #7 acceptance (comment 6694260541, CI 42284807172, review 6694219370/status 58287063782). BOOT-06 is also accepted. The owned boot08 worktree defines all 217 ACs/677 baseline variants, explicit source/fork/type/entrypoint and contract recipes, independent oracles, quota/deadline rules, and a static coverage validator. Its 36,083 expanded obligations remain NOT_RUN, with actual manifests/fixtures pending E01/E06. Validation covers 65 unit tests, 54 component negative cases, all 16 normative mirrors, stable ID mappings and unchanged v2. See [BOOT-08 evidence](evidence/BOOT-08.md). Next: exact-head CI and independent coordinator review/serialized integration; no self-acceptance, push, product runtime, public transactions or E01 work. Stop before E01.

2026-11-06: **BOOT-05 DONE; BOOT-06 DONE; BOOT-07 READY_FOR_REVIEW / INTEGRATION / D86–D88**. Accepted main is 7764b9b4df1e660ff5d6b5b652d93cd3445d1337. PR #87 closed BOOT-05 issue #4; PR #88 closed BOOT-06 issue #6 with acceptance evidence in comment 6694117880. The independently reviewed BOOT-07 head 730c71c3c79fc5364293907cf54b81895a63e52c now includes that accepted history through a normal merge. Only status and decision-history conflicts required reconciliation; D87 and D88 are preserved. The reviewed reader contract and accepted component files remain unchanged. Integrated CI passes 31 tests, 54 component negative cases, all 16 normative mirrors, 88 documents/1019 local links, 63 REQ/217 AC/68 product tasks/84 issues, and the original v2 hash check. Next: exact-head CI and independent review of the merge commit, then coordinator publication/acceptance. BOOT-08 has not started; product runtime remains NOT_RUN. Stop before E01.

2026-11-06: **BOOT-07 READY_FOR_REVIEW / REVIEW_FIX_01**. Independent review of 3c691d75517a291ed1ba2fc4d1e5a9cc42aa4f9f found that a JetStream seal acknowledgment alone does not prove that earlier clustered publish proposals have finished. D87 now requires an exhaustive attempt census and a concrete cold-restart fence on the same isolated NATS stores/topology with publishing disabled, followed by verified recovery/log frontiers before U. Exact already-durable retries rejected solely by that test fence are diagnostics; first/altered/extra parts do not gain that exception. Exact-lock/topology conformance remains required in E01. This is a source-supported proof gap, not a reproduced NATS defect. All affected mirrors and evidence are synchronized; runtime remains NOT_RUN. Independent re-review is pending.

2026-11-06: **BOOT-07 READY_FOR_REVIEW / D87**. The isolated boot07 worktree specifies finite source/delivery/stream boundaries, independent expected-set comparison, reorg/cutover handling, retention checks, negative cases, and deadlines. Existing product APIs and delivery guarantees are unchanged. The canonical/executor body and all five changed normative mirrors match; 63 REQ and 217 AC IDs/order are preserved; the unchanged issue catalog contains 68 product child tasks. The original v2 hash is unchanged. Checks across 84 Markdown files resolve 530 local paths and 491 anchors; English prose and diff checks pass. Evidence: [BOOT-07](evidence/BOOT-07.md). Independent acceptance is pending; TEST-OPEN-02 remains BOOT-08 work and product runtime remains NOT_RUN. Next: review the exact candidate and integrate through the coordinator. Stop before E01.

2026-11-06: **BOOT-05 DONE; BOOT-06 READY_FOR_REVIEW / D86**. PR #87 merged BOOT-05 as main 504b711dca4eb1c621f14ef54102102c3739248d; issue #4 is closed with acceptance evidence in comment 6693814923. The previously reviewed BOOT-06 commit b8e26ae6be1d050267ba87b172a0eb5ab40f50c7 now includes that accepted main history through a normal merge. The only conflict was this status history; both histories are preserved below. Integrated bootstrap CI passes 31 tests, all 16 mirrors, the complete REQ/AC/task graph, and all 54 component negative cases. Registry, candidate lock, validator, and guide bytes match the reviewed BOOT-06 revision. The diff against accepted main contains only the four component files, BOOT-06 evidence, and this status file. Next: independent review of the integration commit, remote CI, and exact-commit acceptance. Candidate versions remain unpromoted and runtime NOT_RUN. Stop before E01.

2026-11-06: **BOOT-06 READY_FOR_REVIEW / review revision / D86**. Fixed both findings from independent review of 38ab319: the offline validator now enforces accepted profile chain IDs, native/support/L1 clients, common-component completeness and applicability, and each native/support Go client's unique go.mod/go.sum pair. All 54 negative cases are rejected, including 37 new regressions. Candidate registry/lock bytes and active specification pins remain unchanged. Evidence: [BOOT-06](evidence/BOOT-06.md). Build/runtime compatibility remains NOT_RUN. Next: independent review of the revised commit before coordinator acceptance; stop before E01.

2026-11-06: **BOOT-06 READY_FOR_REVIEW / D86**. The isolated component inventory contains 41 components, seven profile entries, 11 open compatibility constraints, and eight explicitly pending generated outputs. Registry and candidate lock include exact source commits, dependency-input hashes, release checksums, OCI index/platform digests, and Debian stable/security package pins. The offline structural check passes and rejects all 17 unsafe mutations; JSON/Python parsing, local links, English text scan, and diff checks pass. Evidence: [BOOT-06](evidence/BOOT-06.md). Base commit: 6b8e1dd561d8be39f80b00ef5860d35b7db95d24. Active specification pins remain unchanged; all build/runtime compatibility is NOT_RUN. Next: independent review of this commit, then serialized PR acceptance by the coordinator. Stop before E01.

2026-11-06: **BOOT-05 FINAL_REPAIR_READY_FOR_REVIEW / D86, D88**. Candidate 799a25f444257f64268c469a63b9cc340653eee6 passed independent review and real Actions run 42280000422; review comment 6693562933 and status 58284457193 were published. Live collection then exposed a second API-shape mismatch: combined status omits creator, while the status list includes it. The final allowed correction preserves both responses, binds publisher identity to the same latest status/commit/repository, and rejects mismatches, newer statuses, races, and incomplete pagination. The captured-API regression reproduced the failure before the fix; all 31 local tests and document checks pass. The corrected collector completed the full read-only live smoke on remote799a25f with PASS and the IDs above; this validates the API path, not acceptance of the new code. Next: independently review this final candidate, obtain fresh exact-head CI/report/status, then run the live gate. No further uncontrolled repair cycle or E01 start is authorized.

2026-11-06: **BOOT-05 FIX_READY_FOR_REVIEW / D86, D88**. SSH publication succeeded and PR #87 exists. Actions run 42279051046 passed on head 9896982a7ef995bd76120cb0dc310c407d47719a. The coordinator applied and read back the required branch policy; the missing independent-review status blocked the PR as intended. Independent review passed, and comment 6693432825 plus its status were published. The live collector then rejected GitHub's valid /pull/87#issuecomment-6693432825 URL because its validator accepted only /issues/... URLs. The focused correction accepts both valid URL routes while binding GitHub's response and URL to the exact repository, intended PR, and requested comment ID. The regression reproduced the failure before the fix; all 25 local tests and document checks now pass. This new head requires fresh independent review, remote CI, a new review report/status, and live collection before acceptance. No merge or E01 start is authorized by the earlier results.

2026-11-06: **BOOT-05 READY_FOR_SSH_RECOVERY / D88**. The owner reported that the OAuth code did not arrive and offered SSH authentication. The coordinator verified the existing key as GitHub user sigaev-pro with noninteractive authentication and strict host-key checking, then changed only this repository's origin to git@github.com:sigaev-pro/bc-indexer.git. No key, global configuration, or gh authentication was changed. gh remains the interface for issue/PR/API operations. Independent review of implementation commit 3af36b6661cdb6855459950221ff85fd6985bd9c remains recorded; the status/recovery delta needs exact-head review before publication. No BOOT-05 push, PR, live CI, protection setup, or merge success is claimed. Next: review this documentation delta, push the reviewed branch normally over SSH, then complete the existing remote acceptance sequence. The earlier OAuth-refresh wait is superseded for Git publication. Preserve all CI controls and stop before E01.

2026-11-06: **BOOT-05 BLOCKED_ACCESS / D86**. Implementation commit 3af36b6661cdb6855459950221ff85fd6985bd9c passed independent review and clean exact-head local CI. GitHub rejected its push because the current gh OAuth token has `repo`, `read:org`, and `gist` scopes but lacks `workflow`, which is required to publish .github/workflows/bootstrap.yml. No BOOT-05 PR exists; remote main remains 1ab566cf402a84661729502685be284afb38ba73. Live Actions, branch-protection configuration, review-status publication, and merge verification have not run. The coordinator already asked the owner to run `gh auth refresh -h github.com -s workflow`; do not request the same action again while it is pending. Next: confirm the scope after the owner completes authorization, independently review this status delta and its new exact head, retry the normal branch push, create the PR, and complete the remote acceptance steps in evidence/BOOT-05.md. Preserve the CI controls and stop before E01.

2026-11-06: **BOOT-05 READY_FOR_REVIEW / D86**. The candidate adds pinned GitHub Actions, portable tracked-file checks, branch-policy configuration, and a read-only exact-commit acceptance collector. Local CI passes 23 unittest cases, all 16 normative mirrors, the complete REQ/AC/task mapping, Python/JSON checks, and unchanged v2. The gate rejects stale CI/review, self-review, changed review evidence, outdated bases, and merge trees different from the reviewed head. Evidence: [BOOT-05](evidence/BOOT-05.md). Independent review, real GitHub CI, applying/verifying protection, publishing the review status, and the PR merge are still pending. Bootstrap checks do not establish native product runtime acceptance. Stop before E01.

2026-11-06: **BOOT-04 DONE; BOOT-05 IN_PROGRESS / D86**. [PR #86](https://github.com/sigaev-pro/bc-indexer/pull/86) merged candidate 071df3ea5549364c594829e219426adecdf69131 as main 1ab566cf402a84661729502685be284afb38ba73 with an identical tree. Issue #3 is closed with independent review and real access/container evidence. Current task: GitHub Actions, exact-commit acceptance checks, and enforced branch policy. BOOT-06 and BOOT-07 use separate worktrees for candidate versions and reader-completion criteria. No product stage is authorized in this run.

2026-11-06: **BOOT-04 READY_FOR_REVIEW / D86**. Container, model, sandbox-write, Git commit, remote-read, and unattended branch-push probes passed. Access proof is remote commit 24268af0f6cc384cc759174177e43b8038dc075c on boot/04-access-probe. The macOS launcher now uses a process-scoped sleep guard. Evidence: [BOOT-04](evidence/BOOT-04.md). Resource capacity for seven profiles, signed public tests, and controller recovery remain unproven and are explicitly assigned to later tasks. Candidate review/PR are pending; BOOT-06 proceeds in its isolated worktree. Stop before E01.

2026-11-06: **BOOT-02 DONE; BOOT-04 IN_PROGRESS / D86**. [PR #85](https://github.com/sigaev-pro/bc-indexer/pull/85) merged reviewed candidate 4205b97647abdfd71dd8228d11b65c2a36922a45 as main 6b8e1dd561d8be39f80b00ef5860d35b7db95d24, with an identical tree. Issue #2 is closed; six boundary tests, fresh Reviewer/DevOps probes, and independent review passed. Current work: verify actual container execution and unattended sandbox/Git capabilities. BOOT-06 prepares the candidate component inventory in an isolated worktree with a separate writer. Product pins remain unchanged. Stop before E01.

2026-11-06: **BOOT-02 READY_FOR_REVIEW / D86**. Eight project-local roles, source hashes/license, a validated explicit-prompt loader, and real read-only probes are in .project/agency. Six boundary tests pass. Host Codex 0.145.0 failed model compatibility; exact project-local 0.167.0 passes Reviewer and DevOps probes with the owner's unchanged model. Evidence: [BOOT-02](evidence/BOOT-02.md). Independent candidate review and PR acceptance are pending. BOOT-04 preparation found OrbStack stopped; the owner approved its launch and daemon checks now pass. Container execution and unattended write/Git capabilities remain unverified. BOOT-06 source research is prepared but not yet accepted. Next: review and merge BOOT-02, then continue bootstrap dependencies; stop before E01.

2026-11-06: **BOOT-01 DONE; BOOT-02 IN_PROGRESS / D86**. Reviewed commit 145f91e5c8cb7b855fc70db59a9decb065b41842 is published on remote main and verified through the refs API. [Issue #1](https://github.com/sigaev-pro/bc-indexer/issues/1) is closed with [independent review and publication evidence](https://github.com/sigaev-pro/bc-indexer/issues/1#issuecomment-6691486322). HTTPS Git needed the existing gh credential helper and approved network access; no credential was copied into the repository. Current task: create and verify the pinned project-local agent roles. Independent read-only preparation for BOOT-04 and BOOT-06 may proceed; a single writer owns this worktree. Stop before E01.

2026-11-06: **BOOT-01 READY_FOR_REVIEW / D86**. The owner launched BOOT with an explicit stop before E01. Verified the empty remote, the complete 84-issue graph and matching dependencies, 619 historical blobs with no secret-pattern matches, the unchanged original v2 hash, ignore rules, runbook shell syntax, all 16 normative mirrors, and the 63 REQ / 217 AC / 68-task mapping. Local starting commit: 4554628. Evidence: [BOOT-01](evidence/BOOT-01.md). The launcher now stops after bootstrap. Publication and independent review are still pending; all nine BOOT issues remain open. Agent prompts, controller, and runtime readiness remain unverified. Next: review and publish the baseline, then take ready bootstrap tasks only.

2026-11-06: **PROJECT-ENGLISH-01 DONE**. D85 requires clear, simple American English for all project-authored text, including issues/comments, PRs/reviews, code comments/docstrings, documentation, commits, tests, CLI messages, logs, and reports. All 84 existing GitHub issues #1–#84 now have English titles and bodies. The local ISSUE_CATALOG, ISSUE_MAP, project rules, and bootstrap prompt are in English; the runbook points to the mandatory rule. Local translation commit: 17345e7. Publication used the already authorized gh connection; each update changed only title/body and checked that issue identity, state, labels, assignees, milestone, comment count, and creation time remained unchanged.

Verification: a fresh GitHub listing contains exactly 84 unique expected issues, all open, with zero comments. Remote titles, dependencies, REQ/AC references, and local catalog match; no missing IDs, Cyrillic text, or flagged British spelling was found. English wording was reviewed for technical meaning. The 84-node graph remains acyclic, all 16 normative mirrors match, local links/anchors and git diff checks pass, and the original v2 hash is unchanged. Historical source specifications were not bulk-translated. Product code, controller, and runtime tests were not started; git push was not performed. Next: follow the bootstrap command in DEVELOPMENT_AUTOPILOT when starting development. All new project artifacts must follow D85.

2026-11-06: **DEV-AUTOPILOT-00 DONE_RUNBOOK_AND_ISSUES**. D84 отменил docs-only D64 и разрешил разработку по issue-first gates. DEVELOPMENT_AUTOPILOT.md содержит роли, передачу задач, отдельные worktrees/review, durable controller contract, preflight/лимиты и команду запуска через codex exec. Bootstrap prompt и ISSUE_CATALOG сохранены. В sigaev-pro/bc-indexer созданы и повторно проверены 84 OPEN issues #1–#84: 9 BOOT + 68 child + 7 gates; ссылки — docs/ISSUE_MAP.md. Нет missing/duplicate IDs или несогласованных titles. GitHub connector POST дал integration 403; публикация выполнена разрешённым gh CLI, auth которого проверен. Origin настроен; git push не выполнялся, GitHub code repository пока пустой.

Первый локальный commit регламента/каталога: eaa257234297a2d62f308a145ab64244bd82df83. Проверки: 63 REQ/217 AC/68 child/7 product gates; 84-node issue DAG без циклов; 16 mirrors и full TZ_EXECUTOR согласованы; Markdown links/anchors/tables и shell bash -n пройдены; v2 SHA неизменён. Codex CLI 0.145.0 и gh доступны, Docker daemon/unattended permissions ещё не проверены. Controller и продукт не реализованы, runtime NOT_RUN. Следующее действие — запуск bootstrap из раздела 7 DEVELOPMENT_AUTOPILOT; BOOT-01 публикует базу, BOOT-03 создаёт controller, BOOT-07/08 закрывают открытые критерии, BOOT-09 открывает E01. Непройденный bootstrap не считается автономной готовностью. Текущий запрос на регламент и issues завершён; многодневный продуктовый цикл не запускался.

2026-11-06: **DOC-TEST-POLICY-02 DONE_DOCS_ONLY**. D83 закреплён: standard run ≤1800 секунд wall-clock без подготовки (parent run выбранных profiles), configurable topology/объёмы и отдельный extended режим; scripts/refresh-versions как самостоятельный проектируемый артефакт check/update всех stable pins без floating latest, candidate lock/provenance/full gate/atomic promotion без runtime restart; собственные testnet contracts и точные inputs/amounts; один clean retry с сохранением FAIL и UNSTABLE diagnostic; новый public smoke перед приёмкой candidate. Реализации updater/контрактов, обновления actual versions и запусков не было.

Проверки D83: 63 REQ / 217 AC / 68 child tasks / 7 gates, полный mapping без missing/unknown/cycles; 16/16 нормативных зеркал и full body TZ_EXECUTOR совпадают; Markdown links/anchors/таблицы и git diff --check пройдены; v2 SHA-256 сохранён. Runtime NOT_RUN. Открыты TEST-OPEN-01 (окончание NATS потока, пользователь отложил) и TEST-OPEN-02 (конкретный независимый corpus/quotas, допустима последующая доработка). Они блокируют full acceptance, не скрыты DEFERRED/N/A; завершена фиксация решений, а не все вопросы ТЗ. Следующее действие: вернуться к TEST-OPEN-01/02 по указанию пользователя. Только документация и локальный commit, без push.

2026-11-06: **DOC-TEST-MATRIX-01 DONE_DOCS_ONLY**. D82 принят поручением «реализуй в тз». Закреплены семь профилей ETH mainnet/Sepolia/Hoodi, BSC mainnet/testnet, Polygon mainnet/Amoy, отдельный L1 Sepolia Amoy; TESTING_CONTRACT содержит TC01–TC12, quick/functional/fault/soak/public-smoke, подготовленные native fixtures без public sync, независимый bounded-memory NATS reader, полную профильную матрицу и evidence schema v3. Обновлены оба ТЗ, строгий YAML enum, bootstrap/изоляция/порты, приёмка, задачи и словарь. Wire/schema продукта не менялись. Официальные источники подтверждают реестр сетей; testnet build/fork/bootstrap compatibility остаётся обязательным runtime proof, не объявлена проверенной. Численные deadlines/ресурсы закрепляются паспортом стенда до приёмки, 5/10 минут не выдаются за измеренный SLA.

Проверки D82: 63 REQ / 211 AC / 68 child tasks / 7 gates; полный mapping, без неизвестных dependencies/циклов и ошибок таблиц. 16 нормативных зеркал и полное тело TZ_EXECUTOR согласованы. Markdown links/anchors, неизменность original v2 и git diff --check проверены. Runtime NOT_RUN, кода/запусков/deployment/push нет. Следующее действие: рассмотрение пользователем редакции D82; новых несогласованных продуктовых решений в рамках этого задания не оставлено. Профильные manifests, fixture artifacts и измерения — описанные результаты приёмки проектируемой системы по D64, не задачи на реализацию в этой работе.

2026-11-05: **DOC-ALL-DOCS-VERIFY-01 DONE_DOCS_ONLY**. Перепроверены все 64 Markdown-файла базы 967906d: текущие контракты/два ТЗ, задачи/приёмка/окружение, словарь и исторические материалы. Отчёт — docs/ALL_DOCS_CONSISTENCY_2026_11_05.md. Четыре редакционные несостыковки исправлены: V01 ограничивает fatal-запрет событиями/GC конфликтующего native decision, сохраняя обычный protocol прежнего корректного CLOSED Gateway batch по D57; V02 исправляет текущий счётчик плана 195→199; V03 маркирует ADR-0003 superseded; V04 отмечает закрытие старых 41 находок и историческую карту состояния. Новых технических решений и изменений scope/версий схем нет. Нового подтверждённого дефекта доставки/потери данных не выявлено.

Проверки полной сверки: 63 REQ / 199 AC / 68 child tasks / 7 gates; полное однозначное mapping, согласованные stages, без дубликатов ID, неизвестных dependencies и циклов. Все Markdown-таблицы проверены. 15/15 нормативных зеркал и тело TZ_EXECUTOR согласованы с v3. После добавления отчёта: 65 Markdown-файлов / 945 local links / 473 anchors — ошибок нет; git diff --check пройден. Исходный v2 SHA-256 `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634` сохранён. В корне только два ТЗ; всё остальное в .project. Повторная внешняя переаттестация релизов/URL/build compatibility и runtime не выполнялась; результаты runtime остаются NOT_RUN.

**Точка завершения общей сверки:** F01–F14, IR-01–IR-03 и V01–V04 закрыты документально. Открытых замечаний этих реестров нет; обязательная следующая задача не назначена. Новые требования/контрпримеры рассматриваются отдельно. Локальный commit — в git log и ответе пользователю; без реализации и push, D64/D70 сохраняются. Ниже — исторические состояния.

2026-11-05: **DOC-IR01-03-CLOSE-01 DONE_DOCS_ONLY / D81**. По поручению пользователя исследованы pinned CREATE/CREATE2 paths Geth/BSC/Bor, сохранены ссылки/символы и SHA-256 шести source files в docs/research/IR01_CREATE_ADDRESS_REVIEW_2026_11_05.md. Вывод: принятую модель сохраняем; attempted address, локальный success и конечное existence — разные факты, returned address не заменяет native boundary/outcome. Ранний перечень CallFrame приведён к точным полям; добавлен AC-DAT-04-03 с ранними отказами/constructor revert/failure/ancestor rollback. IR-03 устранён в MESSAGE_CONTRACT, LIVE_CONFIG_SPEC и NODE_DEPLOYMENT_SPEC: модель/defaults определены, generated artifacts и runtime proof остаются результатами описанных задач. Оба адресных ТЗ, v3, REQ/AC/task mapping и реестры синхронизированы.

Проверки D81: 63 REQ / 199 AC / 68 child tasks / 7 gates; полное назначение сценариев, согласованные stages, нет неизвестных зависимостей и циклов. 15/15 нормативных зеркал совпадают; TZ_EXECUTOR совпадает с телом v3 после нормализации ссылок. 64 Markdown-файла / 935 local links / 472 anchors — ошибок нет; git diff --check пройден. Исходный v2 SHA-256 `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634` сохранён. В корне только два ТЗ. Это source/document review, не runtime-проверка; runtime NOT_RUN, без реализации и push.

**Точка завершения D81:** F01–F14 и IR-01–IR-03 имеют DOC_FIXED; открытых замечаний и согласований этих реестров нет. Обязательная следующая задача не назначена; новые требования или конкретный новый контрпример рассматриваются отдельно. Дальнейшие записи — исторические состояния, не список текущих открытых работ. Итоговый локальный commit — в git log и сообщении пользователю. D64/D70 неизменны.

2026-11-05: **DOC-IR02-FIX-01 DONE_DOCS_ONLY / D80**. Пользователь подтвердил sources_binding_digest:Hash32 в SourceObservationRef и L1Checkpoint с неизменностью при replay. Поле добавлено в оба typed evidence; закреплены SHA-256 проверенных bytes файла и durable observation provenance, новые observations используют новую конфигурацию, recovery/replay не переписывают прежние digest/READY/SEALED bytes. Narrower whitelist без source_evidence не получает выдуманного digest. Добавлены AC-LIF-07-07 (E02 schema), AC-LIF-07-08 (E05 config replacement), AC-LIF-07-09 (E05 replay); обновлены REQ-LIF-07, task mapping и полная карточка observer. Оба адресных ТЗ и нормативные копии синхронизированы. IR-02 — DOC_FIXED; IR-01/IR-03 остаются OPEN_PROPOSAL.

Проверки D80: 63 REQ / 198 AC / 68 child tasks / 7 gates; полное назначение сценариев, согласованные stages, без неизвестных зависимостей и циклов. 15/15 нормативных зеркал совпадают; тело TZ_EXECUTOR совпадает с v3 после нормализации ссылок. 63 Markdown-файла / 922 local links / 466 anchors — ошибок нет; git diff --check пройден. Исходный v2 SHA-256 `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634` сохранён. Runtime NOT_RUN; только документация, без push.

**Следующее действие D80:** рассмотреть оставшиеся IR-01/IR-03 независимой рецензии. Поле IR-02 повторного согласования не требует. Прежние записи ниже — история рецензии и закрытых этапов, не актуальный статус этого исправления.

2026-11-05: **DOC-INDEPENDENT-REVIEW-01 DONE_DOCS_ONLY / 3 OPEN_PROPOSAL**. По поручению пользователя выполнена независимая документальная рецензия базы 1e97c11. Отчёт — docs/INDEPENDENT_REVIEW_2026_11_05.md. Выявлены IR-01: старый перечень CallFrame против точной семантики attempted/created address; IR-02: sources_binding_digest требуется в Polygon evidence, но отсутствует в перечнях полей SourceObservationRef/L1Checkpoint; IR-03: остаточные заявления о безымянных открытых проектных вопросах. Для каждого приведены доказательство, контрпример, конкретная правка и проверки. Нового подтверждённого дефекта потери данных в автомате доставки этот проход не выявил; это не runtime PASS и не доказательство отсутствия иных дефектов.

Проверки рецензии: 63 REQ / 195 AC / 68 child tasks / 7 gates; mapping полный, неизвестных зависимостей и циклов нет; 15/15 нормативных зеркал совпадают. 63 Markdown-файла / 919 local links / 463 anchors — ошибок нет; git diff --check пройден. Два адресных ТЗ и канонический v3 побайтно совпадают с базой 1e97c11. Исходный v2 SHA-256 `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634` сохранён. В корне по-прежнему только два ТЗ; отчёт служебный. Повторная полная внешняя переаттестация pinned clients, сборки, runtime и push не выполнялись.

**Следующее действие:** рассмотреть конкретные предложения отчёта. IR-01 приводит пересказ к принятой точной модели без новой семантики; IR-02 рекомендует два явных Hash32 fields и проверку сохранения digest при замене sources_file; IR-03 устраняет редакционную неопределённость. Новые поля не внесены молча; предложения пока не приняты. После решения — синхронные правки контрактов/ТЗ/AC/tasks и документальная сверка. D64/D70 неизменны. Более ранние точки завершения ниже относятся к пакету F01–F14 и не закрывают IR-01–IR-03.

2026-11-05: **DOC-F02-CONFIRM-01 DONE_DOCS_ONLY / D79**. Получено отдельное подтверждение F02 после общего акцепта и финального commit 5267911. Ответ записан как дополнение D79 в DECISIONS; он полностью совпадает с действующим MESSAGE_CONTRACT. Оба адресных ТЗ и нормативные контракты не меняются. Следующих задач и открытых согласований нет; границы D64/D70 сохранены. Изменены только журнал решений и состояние; проверка — git diff --check и сверка состава diff.

2026-11-05: **DOC-AUDIT-CLOSE-01 DONE_DOCS_ONLY / D79**. Пользователь акцептовал все предложенные решения и поручил завершить работу. Оставшиеся F02/F04/F05/F06/F07/F08/F09/F10/F14 исправлены; все 14 находок F01–F14 закрыты в документации (DOC_FIXED). Согласованы decision-to-batch, retention ACK, READY/SEALED, CaseRegistry/CaseResult/RunManifest, task IDs и доступ Bor к выделенной L1-ноде; краткое ТЗ дополнено account state diff; устранены устаревшие требования и ссылка на старый Geth pin. Нормативные разделы, два адресных ТЗ, приёмка и задачи синхронизированы.

Проверки D79: 63 REQ / 195 AC / 68 child tasks / 7 gates; все AC назначены, stages согласованы, неизвестных parents/dependencies и циклов нет. 15/15 нормативных зеркал совпадают; TZ_EXECUTOR совпадает с телом v3 после нормализации ссылок. 62 Markdown-файла / 911 local links / 459 anchors — ошибок нет; git diff --check пройден. Исходный v2 SHA-256 `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634` не изменён. Source review Geth выполнен по принятому pin; runtime-проверки не запускались, применимые runtime cases остаются NOT_RUN. В корне видны только TZ_DECISION_MAKER.md и TZ_EXECUTOR.md.

**Точка завершения D79:** ТЗ подготовлено для передачи; открытых согласований по этому аудиту нет. После проверок — локальный commit документации; его хеш находится в git log и итоговом сообщении. Следующая задача не назначена: новые правки возможны по новым требованиям или выявленному контрпримеру. D64/D70 сохраняются: реализации не будет никогда в рамках этой работы; конечная граница проекта — стандартизированные данные, подтверждённо доставленные в NATS JetStream. Push не выполняется.

## Исторические точки состояния

Записи ниже отражают состояние до D79; их просьбы о согласовании и следующие действия более не действуют.

2026-11-05: **DOC-F01-FIX-01 DONE_DOCS_ONLY / D78**. F01 закрыт в документации: AC-OPS-01-01 больше не отвергает любой relative path; AC-OPS-01-04 проверяет storage.live_dir/storage.gateway_dir относительно каталога YAML независимо от cwd; AC-OPS-01-05 отдельно отклоняет relative finality.polygon.sources_file. LIVE_CONFIG_SPEC уточняет базу путей и field-specific абсолютный путь; config check не создаёт каталог/DB/stream/run. Синхронизированы оба ТЗ, нормативные источники и E06-07.yaml mapping.

Проверки D78: 63 REQ / 187 AC / 68 child tasks / 7 gates; все AC назначены, stages согласованы, неизвестных parents/dependencies и циклов нет. 15/15 нормативных зеркал совпадают; TZ_EXECUTOR совпадает с телом v3 после нормализации ссылок. 62 Markdown-файла / 902 local links / 445 anchors — ошибок нет; git diff --check пройден. Исходный v2 SHA-256 `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634` не изменён. Runtime NOT_RUN; только документация, без push.

**Следующее действие D78:** после локального коммита предъявить отдельно F02 — один native decision с публичными событиями соответствует одному delivery batch, включая multi-block reorg/finality-only; decision без событий не выделяет batch. Получить решение до исправления неточных block-batch формулировок и приёмки. Закрыты F11/F03/F12/F13/F01; остальные 9 находок не согласованы. D64/D70 сохраняются. Ниже — исторические точки состояния.

2026-11-05: **DOC-F13-FIX-01 DONE_DOCS_ONLY / D77**. F13 закрыт в документации: exact stream metadata keys extractor.network_digest и extractor.destination_id, lowercase hex formats, выбор active/desired binding по фазе, CONFIG_BLOCKED при missing/malformed/mismatch. Bootstrap CREATE/read-back и проверка existing stream не допускают auto-adoption/retag/reset/purge; compatible UPDATE сохраняет посторонние metadata. Cutover проверяет новый target до первых PrepareSwitch/CommitSwitch; durable control retry не зависит от доступности NATS, публикация проверяется отдельно. Account/stream/route/limits/generation checks сохранены; native offline capture не получил зависимости от NATS.

Проверки D77: подтверждён metadata map в pinned NATS StreamConfig (`b2fd3efc7456b11b06a54877ad6f6ccfb911edb4/server/stream.go`); это source review, не runtime. Добавлены 7 AC: всего 63 REQ / 185 AC / 68 child tasks / 7 gates. Все AC назначены, stages согласованы, неизвестных dependencies/parents и циклов нет. 15/15 нормативных зеркал совпадают; TZ_EXECUTOR совпадает с телом v3 после нормализации ссылок. 62 Markdown-файла / 902 local links / 445 anchors — ошибок нет; git diff --check пройден. Исходный v2 SHA-256 `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634` не изменён. Runtime NOT_RUN; только документация, без push.

**Следующее действие D77:** после локального коммита предъявить отдельно F01 — противоречие разрешённых относительных YAML paths и отрицательного AC; сохранить relative storage paths относительно YAML, отдельный absolute-only sources_file Polygon. Получить решение до исправления. Закрыты F11/F03/F12/F13; остальные 10 находок не согласованы. D64/D70 сохраняются. Ниже — исторические точки состояния.

2026-11-05: **DOC-F12-FIX-01 DONE_DOCS_ONLY / D75–D76**. F12 закрыт в документации: SourceReceiptEncoding registry 1/2/3, native_type/presence/origin, exact body consensus bytes, отдельный optional BOR_LEGACY_STORAGE_RLP и точная применимость network details. Уточнение D76 разрешает bounded byte-equivalent writer из native Receipt в spool; большой receipt проверяется потоково, без дополнительного полного буфера/decoded graph. Common fields сохраняются отдельно от consensus bytes. Синхронизированы оба ТЗ, v3, нормативные источники/словарь/приёмка; исторический alias Bor storage encoding явно отменён.

Проверки D75–D76: перечитаны pinned receipt.go Geth/BSC/Bor и Bor rawdb/bor_receipt.go; подтверждены consensus MarshalBinary contract, typed outer RLP wrapper в EncodeRLP, отсутствие Bloom в storage, Bor StateSync decoder и raw auxiliary reader. Это source review, не запуск кодека. Добавлены 6 AC: всего 63 REQ / 178 AC / 68 child tasks / 7 gates. Все AC назначены, stages согласованы, неизвестных dependencies/parents и циклов нет. 15/15 нормативных зеркал совпадают; TZ_EXECUTOR совпадает с телом v3 после нормализации ссылок. 62 Markdown-файла / 893 local links / 438 anchors — ошибок нет; git diff --check пройден. Исходный v2 SHA-256 `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634` не изменён. Runtime NOT_RUN; только документация, без push.

**Следующее действие D76:** после локального коммита предъявить отдельно F13 — точные project-owned stream metadata keys NATS и их проверка. Предложение есть в реестре полного аудита; получить решение до исправления. Закрыты F11/F03/F12; остальные 11 находок не согласованы. D64/D70 сохраняются. Ниже — исторические точки состояния.

2026-11-05: **DOC-F03-FIX-01 DONE_DOCS_ONLY / D74**. F03 закрыт в документации: общий tx-local execution_order, точное равенство opcode/projection, отдельные body/protocol/effect indices, RollbackScope tree/refs/presence и ancestor rollback. Scope 0 отделён от root execution; сохранённые native fees/nonce/authorization не приписываются отменённой области. Native capture фиксирует порядок/identity до worker; recovery не удваивает операции. Добавлены 8 AC, синхронизированы оба ТЗ, v3, нормативные источники, словарь и task mapping.

Проверки D74: 63 REQ / 172 AC / 68 child tasks / 7 gates; все AC назначены, stage совпадают, неизвестных dependencies/parents и циклов нет. 15/15 тематических зеркал совпадают; TZ_EXECUTOR совпадает с телом v3 после нормализации ссылок. 62 Markdown-файла / 883 local links / 430 anchors — ошибок нет. Проверены формулировки различий entry/operation, cause/equality, scope 0/root, native ID/public ID, rollback/reorg и recovery/retry; старые неопределённые rollback_scope и счётчики AC в активном контракте заменены. git diff --check пройден; исходный v2 SHA-256 `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634` не изменён. Runtime NOT_RUN; только документация, без push.

**Следующее действие D74:** после локального коммита предъявить отдельно F12 — точное native consensus receipt encoding и отдельный optional Bor legacy storage encoding. Предложение уже описано в реестре полного аудита; получить решение до исправления. Закрыты F11/F03, остальные 12 находок не согласованы. D64/D70 неизменны. Ниже — предыдущие точки состояния.

2026-11-05: **DOC-F11-FIX-01 DONE_DOCS_ONLY / D73**. F11 закрыт в документации: логическая Envelope кодируется bounded типизированными RecordFragment до сериализации; большие поля также дробятся. Закреплены header/tree/blob/seal, tags/presence/order/digests, READY manifest/reservation/transport/control v2, fragment-aware MsgID/RPC/cursors и all-fragments ACK. Предел RecordFragment 65536 bytes, BlobChunk.data 16384 bytes; общего прикладного лимита записи нет, checked u64/диск/fatal правила сохраняются. Синхронизированы v3, два адресных ТЗ, словарь, тематические источники и приёмка; монолитный Envelope wire удалён.

Проверки D73: 63 REQ / 164 AC / 68 child tasks / 7 gates; все AC назначены, неизвестных parents/dependencies и циклов нет. 15/15 нормативных зеркал совпадают; TZ_EXECUTOR совпадает с телом v3 после нормализации ссылок. Проверены 62 Markdown-файла, 877 local links и 424 anchors — ошибок нет. Документальная арифметика wire: максимальный TypedItem 91 bytes; fragment с ним 150; с полным BlobChunk 16473; header bound 194, seal 113; MsgID 123 ASCII bytes. Это расчёт контракта, не runtime-тест. git diff --check пройден; исходный v2 SHA-256 `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634` не изменён. Все runtime verdict NOT_RUN. Только документация, без push.

**Следующее действие:** после локального коммита этого этапа предъявить отдельно F03 — общий execution_order и публичные rollback scopes, по конкретному предложению [реестра аудита](docs/FULL_AUDIT_APPROVAL_2026_11_05.md). Получить решение до исправления F03. Остальные 13 находок остаются открытыми; весь пакет не согласован. D64/D70 сохраняются.

Историческая точка D72, закрыта ответом D73: **DOC-F11-DISCUSS-01**. Пользователь выбрал разбор замечаний по одному, начиная с больших записей. На этом шаге вариант ещё не был принят; затем D73 утвердил типизированные фрагменты. Это не согласие на весь пакет. Остальные 13 замечаний остаются открытыми; D64/D70 сохраняются.

2026-11-05: **DOC-FULL-AUDIT-01 DONE_DOCS_ONLY / D71; ИСПРАВЛЕНИЯ ОЖИДАЮТ СОГЛАСОВАНИЯ**. [Полный реестр и пакет согласования](docs/FULL_AUDIT_APPROVAL_2026_11_05.md): 14 групп F01–F14, из них 10 перепроверенных и 4 новых. Для каждой есть доказательство, контрпример, конкретное предложение и критерий проверки. Новые: F11 — монолитный Protobuf Envelope против необозначенной верхней границы logical record; F12 — отсутствующий receipt encoding registry; F13 — неопределённые stream metadata checks; F14 — источник Geth types от старого pin.

Десять групп F01/F02/F04–F10/F14 согласуют текст с принятым поведением. F03/F11/F12/F13 содержат предложенную конкретизацию контракта и требуют утверждения. Для F11 рекомендованы ограниченные типизированные protobuf-сообщения до сериализации вместо нарезки одного потенциально недопустимого Envelope; альтернатива с ограничением logical record явно меняет гарантию и не выбрана. Внешняя сверка ограничена официальным Protobuf и выбранными native receipt/type sources; runtime/полная переаттестация релизов не выполнялись.

Проверки: 63 REQ / 156 AC / 68 child tasks, полное однозначное табличное mapping, без неизвестных зависимостей и циклов; 15 нормативных зеркал совпадают; ссылки на task IDs в предложениях разрешаются. 62 Markdown-файла, 864 локальные ссылки / 417 anchors без ошибок; git diff --check. Оба видимых ТЗ, v3 и v2 побайтно сохранены относительно 61afb7a. В корне только два ТЗ. Контрольный commit DOC-FULL-AUDIT-01 — в git log; без push.

Следующее действие: получить согласование пакета либо выбранных пунктов, сразу записать ответ в DECISIONS, затем исправлять только утверждённые пункты с синхронной правкой нормативных копий/AC/tasks. Текущий запрос проверки не заменяет согласование ещё не принятых дополнений. D64/D70 сохраняются: только ТЗ, конечный результат — стандартизированные данные в NATS, без downstream-бизнес-кейса.

2026-11-05: **DOC-SCOPE-CLARIFY-01 DONE_DOCS_ONLY / D70**. В двух адресных ТЗ и v3 закреплён конечный результат: предусмотренные контрактом данные в единой версионированной схеме проекта, подтверждённо доставленные в NATS JetStream. Приёмка включает корректность формата/семантики, PubAck всех frames/parts закрытого batch в одном назначении и durable NATS_ACKED. Внутренние ACK/cleanup/recovery, lifecycle и D64 сохранены.

[CEO/CFO-заключение исправлено](docs/EXECUTIVE_REVIEW_2026_11_05.md): downstream-польза, consumer и его экономика не являются недостающими условиями завершения этого ТЗ; прежние блокирующие выводы отозваны. Старый текст сохранён как явно обозначенный архив оценки D69. Технические замечания F01–F10 остаются открытыми и этим уточнением не исправлялись.

Проверено: normative body подробного ТЗ совпадает с v3 после нормализации ссылок; приложения A–E не менялись, 63 REQ / 156 AC / 68 child tasks без дублей. 61 Markdown-файл, 861 локальная ссылка / 417 anchors без ошибок; git diff --check, исходный v2 неизменен. В корне видимы только два ТЗ. Контрольный commit DOC-SCOPE-CLARIFY-01 — в git log; без push. Следующее возможное уточнение касается качества самого контракта данных/доставки и открытых технических замечаний, без расширения задачи за NATS.

2026-11-05: **DOC-EXECUTIVE-REVIEW-01 DONE_DOCS_ONLY / D69 — историческая оценка, скорректирована D70**. [Заключение CEO/CFO](docs/EXECUTIVE_REVIEW_2026_11_05.md) первоначально требовало оснований экономической целесообразности, потребителя и владельцев обязательств за NATS. D70 отозвал эти требования как выход за границу задачи; они не являются открытыми блокерами ТЗ. Разбор внутренних рисков хранения и сопровождения сохраняется без введения нового обязательного бюджета или SLO.

Суммы, рынок, сроки и бюджеты не выдуманы; внешние цены/runtime не проверялись. Предложенный критерий завершения документации и недостающие управленческие вводные явно не являются новыми принятыми решениями. Оба адресных ТЗ, v3 и исходный v2 не изменены; проверены ссылки/anchors, git diff --check и неизменность двух видимых файлов. Контрольный commit DOC-EXECUTIVE-REVIEW-01 — в git log, без push. D64 сохраняется; дальнейшая работа только над документацией по следующему поручению пользователя.

2026-11-05: **DOC-AUDIENCE-AUDIT-01 DONE_DOCS_ONLY / D68**. Повторная проверка двух адресных ТЗ завершена; [отчёт с 10 группами замечаний F01–F10](docs/AUDIENCE_REVIEW_2026_11_05.md). Документы ещё не идеальны: противоречие relative paths/AC, неполная модель evidence, разные child IDs, неоднозначности batch/order/rollback scope, пропущенная связь Bor→L1, неполное краткое описание state diff и редакционные остатки. Список относится к внутренней проверке текста, не к доказанным runtime-сбоям. Принятые ограничения и явные NOT_RUN не записаны в дефекты.

Оба видимых ТЗ побайтно совпадают с проверенной редакцией a99227f; исправления в них не выполнялись. Подтверждены 63 REQ / 156 AC / 68 child tasks без дублей и совпадение нормативного тела с v3 после нормализации ссылок. Проверки: 60 Markdown-файлов, 855 локальных ссылок / 417 anchors без ошибок, git diff --check; v2 неизменен. В корне по-прежнему видимы только два ТЗ. Контрольный commit DOC-AUDIENCE-AUDIT-01 — в git log; remote не публиковался.

Следующий предмет работы при поручении исправить ТЗ — замечания отчёта: сначала F01/F04/F05, затем F02/F03, затем F06–F10. Не подменять проверку автоматическим изменением требований и не переоткрывать принятые решения без конкретной развилки. D64 сохраняется: только проектирование и документация.

2026-11-05: **DOC-VISIBILITY-01 DONE_DOCS_ONLY / D67**. В корне видимы только TZ_DECISION_MAKER.md и TZ_EXECUTOR.md. Остальные 57 документов, включая AGENTS, CONTEXT, этот статус, исходники ТЗ и docs, перенесены в скрытую папку .project с сохранением Git-истории. Относительные ссылки исправлены; служебные комментарии в двух видимых файлах указывают новые пути к правилам и статусу.

Проверено: все прежние 59 файлов сохранены; v2, v3 и служебные правила побайтно неизменны. В двух адресных ТЗ изменены только ссылки и служебный комментарий; нормативное тело совпадает с v3 после нормализации путей. 852 локальные ссылки / 417 anchors без ошибок; git diff --check пройден. Контрольный commit DOC-VISIBILITY-01 — в git log, без push. Запрос выполнен; продолжать только уточнение документации по D64. Пути в документах внутри .project разрешаются относительно их нового расположения.

2026-11-05: **DOC-AUDIENCE-01 DONE_DOCS_ONLY / D66**. Созданы два самостоятельных адресных документа: [для принимающего решения](../TZ_DECISION_MAKER.md) — результат, границы, решения, последствия и условия принятия; [для исполнителя](../TZ_EXECUTOR.md) — русскоязычные пояснения терминов и состояний, порядок чтения и полный технический контракт с задачами и приёмкой.

Подробный документ содержит согласованный нормативный текст v3 полностью и без изменений: 63 REQ / 156 AC / 68 task IDs, без дублей. SHA-256 включённого тела: `b7e9b1941f6d525d207b850a70bf932836d5746be132dfd0b31137342d2cc5c8`. Каноническая v3 и исходная v2 не менялись. Проверены таблицы, локальные ссылки/anchors, точное совпадение нормативного тела и git diff --check. Контрольный commit DOC-AUDIENCE-01 — в git log; remote не публиковался.

Запрос двух файлов выполнен; новых решений от пользователя не требуется. Дальнейшие изменения — только уточнение и проверка ТЗ по D64. Runtime verdict остаются NOT_RUN; задачи и испытания в документе исполнителя не являются разрешением на реализацию.

2026-11-05: **DOC-FINALIZE-03.1 / DOC-FINALIZE-03.2 DONE_DOCS_ONLY**, D65. Полное ТЗ подготовлено для полноценного grill. Подэтап атомарной приёмки/декомпозиции сохранён в `8f28b33`; финальная сборка — commit DOC-FINALIZE-03.2 в git log. Remote не публиковался.

[Единый root v3](evm-transaction-extractor-tz-v3-codex.md): краткое ядро 95 строк / 1 461 слово и полные нормативные приложения A–E в том же MD. 63 REQ раскрыты в [156 атомарных AC](docs/ACCEPTANCE_CASES.md), все AC привязаны к [68 child tasks / 7 gates](docs/TASK_DECOMPOSITION.md). Есть 3 полные карточки сложных задач; словарь содержит 51 термин. Новых продуктовых развилок текущая документальная проверка не выявила.

[Карта полного grill и отчёт](docs/FINAL_SPEC_REVIEW.md) покрывают G01–G08: scope, capture, данные, lifecycle/finality, delivery, эксплуатацию, проверяемость/задачи, фиксацию ТЗ. Это основа интервью, не отметка нового интервью как пройденного пользователем. Принятые D16–D65 не открывать повторно без конкретного противоречия; новый контрпример привязывать к ветке/REQ/AC и документировать решение до изменения гарантии.

Проверены ссылки/anchors, таблицы и 15 нормативных разделов root против тематических файлов; 63 REQ и 156 AC покрыты полностью, назначения AC однозначны и совпадают по stage. В графе 68 tasks / 7 gates нет неизвестных зависимостей и циклов. Краткое ядро независимо сверено; исправлены порядок установки RECOVERY_ONLY bridge до local recovery и момент сохранения динамических dependencies. Исходный v2 SHA-256 неизменен: `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634`.

Последний полный документальный прогон DOC-AUDIENCE-01: 59 Markdown-файлов, 852 локальные ссылки, включая 417 anchors, без ошибок; 63/156/68 определений REQ/AC/tasks в root и документе исполнителя без дублей; все проверенные таблицы корректны; git diff --check пройден. Изменения только Markdown.

**D64 — постоянная граница:** наша единственная цель — идеальное ТЗ; реализации не будет никогда. E01–E07, артефакты и runtime-проверки — содержание ТЗ, не исполняемый backlog агента. Все runtime verdict NOT_RUN; source inspection/документальная сверка не заменяют build/ABI/power-loss/genesis payload proof. Дальнейшая работа — только grill, уточнение и проверка ТЗ, с фиксацией завершённых документальных подэтапов в Git.

## История работы — не текущие задания

Прежние checkpoints ниже сохранены для происхождения решений. Их статусы и next steps относятся к указанному моменту; текущие разрешения и точка продолжения заданы выше.

2026-11-05: DOC-FINALIZE-03.2 IN_PROGRESS. Подэтап словаря, атомарной приёмки и точной декомпозиции сохранён в 8f28b33; git status чист. Сборка одного самодостаточного root MD: краткое ядро + полные нормативные приложения A–E, карта grill и итоговая документальная сверка. D64 сохраняется.

2026-11-05: DOC-FINALIZE-03.1 DONE_DOCS_ONLY / D65. Словарь обновлён (51 термин); 63 REQ раскрыты в 156 атомарных AC; TASK_DECOMPOSITION задаёт 68 child tasks / 7 gates / 3 полные карточки, точные owners/outputs/dependencies и AC mapping. Устранены прежнее повторное «утверждение» закрытой модели в E02 и неверная фраза binding «до сетевого обмена» (точно: local binding до obligations, cross-owner check при handshake до Prepare/Publish). Новых продуктовых развилок аудит не выявил. После commit — DOC-FINALIZE-03.2: краткое ядро + полные нормативные приложения в том же root, карта полного grill, итоговые проверки. D64: runtime NOT_RUN, только документы.

Проверки DOC-FINALIZE-03.1: 57 Markdown-файлов / 540 локальных ссылок / 112 anchors без ошибок; 13 зеркальных нормативных разделов совпадают; все 156 AC имеют ровно одну задачу нужного этапа, все 63 REQ покрыты; 68 tasks и межэтапные gates без неизвестных зависимостей/циклов. Таблицы и diff --check корректны, v2 SHA-256 неизменен. Контрольный commit подэтапа — в git log; remote не публиковался.

2026-11-05: DOC-FINALIZE-03 IN_PROGRESS / D65. Полная финализация ТЗ как основы полноценного grill: краткое представление, единые термины, требования/приёмка, границы компонентов, пригодность к декомпозиции и все сквозные сценарии. Проверка фактов делегирована по явно вызванному grilling; изменения только документации. Согласованную семантику сохранить, реальные поведенческие развилки не замалчивать. После завершённого подэтапа — проверки и локальный commit; никакой реализации.

2026-11-05: DOC-SCOPE-01 DONE_DOCS_ONLY / D64. Постоянная цель — только идеальное ТЗ; реализации не будет никогда. Обновлены AGENTS, текущая точка продолжения, ТЗ и связанные документы; прежний условный переход к E01 отменён. Технический план сохранён только как содержание ТЗ. Проверены 55 Markdown-файлов, 420 локальных ссылок / 24 anchors, совпадение 13 нормативных разделов, отсутствие текущих указаний перейти к реализации, git diff --check; v2 неизменен. Следующее направление — только проверка и уточнение ТЗ. Контрольная фиксация — commit DOC-SCOPE-01 в git log, без push.

2026-11-05: **DOC-FINAL-01 / DOC-FINAL-02 DONE_DOCS_ONLY**. Чистовая [единая редакция ТЗ v3](evm-transaction-extractor-tz-v3-codex.md) завершена по D61–D63. Подэтап точных контрактов сохранён в d1668f7; финальная редактура — commit DOC-FINAL-02 в git log. Remote не публиковался.

D64: постоянная единственная цель — идеальное ТЗ. Реализации не будет никогда; код продукта, сборки, runtime-тесты и deployment исключены из нашей работы. E01–E07 и runtime acceptance описываются только как содержание ТЗ, не как ожидающие разрешения задачи агента. Следующее направление — проверять и уточнять ТЗ, устранять противоречия, задавать вопросы по неясным требованиям. Build/runtime/crash/deployment verdict NOT_RUN.

Результат: 41/41 исходных audit findings SPECIFIED; [63 требования и критерии приёмки](docs/ACCEPTANCE_REQUIREMENTS.md). D62 фиксирует оба источника BSC finality и отдельные фактические комиссионные движения. D63: Polygon recovery-state GC после checkpoint в finalized-состоянии собственной supporting L1; tx/status не ждут GC. Точные IDs/control/wire, drain/cutover, supporting identities и конфигурация включены в root. Архивные проектные материалы явно отделены от текущего контракта.

[Финальная сверка](docs/FINAL_SPEC_REVIEW.md): 13 нормативных разделов root совпадают с тематическими файлами после преобразования heading/link paths; 63 REQ ID уникальны, все 41 audit ID покрыты. Проверены Markdown links/anchors и таблицы, git diff --check. Исходный v2 SHA-256: ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634.

Не выдавать source inspection за power-loss/runtime proof, проверенные image/ABI/toolchain locks либо полный bootstrap genesis payload. Новые противоречия и неопределённости прорабатывать в документации; каждый законченный документальный подэтап фиксировать отдельным commit.

Ниже сохранены прежние checkpoints. Их IN_PROGRESS, next steps и открытые вопросы относятся к указанному моменту; текущая задача и разрешения определены только в точке продолжения выше.

2026-11-05: DOC-FINAL-02 IN_PROGRESS. Контракты DOC-FINAL-01 сохранены в d1668f7; git status чист. Чистовая консолидация единого нормативного v3, current-only message/type/stack/plan views, traceability и финальная проверка. Только документация.

2026-11-05: DOC-FINAL-01 DONE_DOCS_ONLY / D61–D63. Добавлены CURRENT_SEMANTICS, CONTROL_WIRE_CONTRACT и ACCEPTANCE_REQUIREMENTS (63 уникальных требования с fixture/stage mapping); точная финальность ETH/BSC/Polygon, комиссии, supporting identity bootstrap, wire/ID/layout/RPC, maintenance/cutover и recovery SEALED согласованы между документами. Повторный source review уточнил BSC milliseconds, authoritative finality regression, ancestry direction и восстановление потерянных switch/resume replies. D62/D63 приняты; неотвеченных вопросов нет. Проверки: 54 MD / 407 local links / 43 anchors, 41 audit finding SPECIFIED, diff --check; v2 SHA-256 прежний. Runtime tests NOT_RUN. После commit — DOC-FINAL-02: чистовая консолидация root, удаление deferred требований из active contract, текущие state/index документы и финальная сверка. Реализация/deploy/push не разрешены.

2026-11-05: DOC-FINAL-01 IN_PROGRESS / D61 — пользователь поручил чистовую доводку всего ТЗ. Основа ca5aa6a; git status был чист. Проверяются точные current finality/pin-GC contracts, wire/control identities, YAML/CLI cutover, полная requirement acceptance matrix и единый нормативный root. Только документы/source research. Новые поведенческие развилки — вопрос до зависимой правки. После закрытия contracts и commit — DOC-FINAL-02: чистовая редактура, scope/traceability/ссылки и финальный commit.

2026-11-05: DOC-REPAIR-01…05, DEP-04, DOC-CONSOLIDATE и E00-11 DONE_DOCS_ONLY. Все 41/41 находки SPECIFIED; неотвеченных пользовательских развилок нет. Полное актуальное ТЗ — evm-transaction-extractor-tz-v3-codex.md (1409 строк): scope, typed messages, native capture, delivery/drain/cutover, YAML, bootstrap/ports/stack, REC и E01–E07 с evidence. Старые безусловные требования истории/sync-выгрузки убраны из root v3. Исходный v2 не изменён.

Итоговые проверки: 49 Markdown-файлов, 392 локальные ссылки (включая 43 anchors) без ошибок; все 41 ID покрыты traceability ровно один раз; пять полных нормативных разделов root побайтно совпадают с источниками после нормализации заголовков/локальных ссылок. git diff --check пройден; SHA-256 v2 `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634`. Только Markdown changes. Контрольные commits подэтапов: a6ff50c, c151551, 4fa0607, 9b1c652, 574bb99; commit консолидации — последний git log.

Точная следующая задача: оставаться в проектировании до отдельного указания пользователя. После разрешения реализации — E01-01 (.build/.supporting/.harness/.artifacts), затем E01-02 admission/ownership/pin/decision proofs ETH/BSC/POLY. Все E01–E07 TODO; сборок/runtime/deployment/push не было. Не выдавать source inspection за доказательство power-loss durability, pin/marker caller coverage, совместимости images/ABI или проверки полного Heimdall genesis payload. Новые поведенческие развилки выносить пользователю до зависимых изменений.

2026-11-05: DOC-CONSOLIDATE IN_PROGRESS — 574bb99 сохранил DOC-REPAIR-05, git status чист. Все 41/41 пункта SPECIFIED. Сборка полного актуального root v3 из проверенных normative contracts; устранение устаревших требований истории и прежних next steps. Последняя проверка подэтапа 05: 49 MD / 374 ссылки (уточнение предыдущей записи 375), 5 anchors, без ошибок. Код/runtime не выполнять.

2026-11-05: DOC-REPAIR-05 SPECIFIED — все 41/41 находки имеют нормативный контракт и fixture/stage mapping в IMPLEMENTATION_GATES. Исправлены active/deferred backlog, ранний native proof и harness, partial/full REC-T01, independent oracles/evidence, module graphs; DEP-04 документально завершён. Повторный review уточнил обязательный reorg BSC system tx/Bor StateSync и deferred Rust. Проверки: 49 MD / 375 локальных ссылок, все 41 ID покрыты ровно один раз, diff --check, v2 неизменен. Runtime E01–E07 TODO. После commit — DOC-CONSOLIDATE, собрать действующий полный root MD и убрать устаревшие next steps; код не начинать.

2026-11-05: DOC-REPAIR-05 IN_PROGRESS — bootstrap сохранён в 9b1c652, git status чист. Завершаются DEV-01…06, traceability fixtures/этапов, исправление действующих gates/backlog и DEP-04 сверка. Только документация.

2026-11-05: DOC-REPAIR-04 SPECIFIED — 35/41 пунктов, DEPLOYMENT_BOOTSTRAP_CONTRACT и актуальные D58/D60 profiles. Повторный source review исправил BSC init: hash scheme обязателен уже при создании genesis DB. 48 MD / 373 локальные ссылки, 5 anchors, diff --check пройдены; v2 неизменен. Native/deployment source review завершён без оставшихся выявленных блокеров этого контракта; сборки, genesis payload validation, runtime не выполнялись. После commit — DOC-REPAIR-05: активный backlog, ранние proofs/harness, evidence и итоговая сверка.

2026-11-05: DOC-REPAIR-04 IN_PROGRESS — native подэтап сохранён в 4fa0607, git status чист. Подготовка bootstrap/mount/secret/startup контракта и обновление текущего кандидата Heimdall по D58; исходные исторические source audit pins сохраняются.

2026-11-05: DOC-REPAIR-03 SPECIFIED — 24/41 пунктов получили проверяемые контракты. Добавлен NATIVE_CAPTURE_PROTOCOL, receipt provenance; повторная сверка доставки закрыла RecoveryRelease, crash-safe RELEASING и Resume admission generation. Native review уточнил native-only replay, child-root durability, static/dependency/final input manifests и RECOVERY_ONLY hooks до recovery. Проверки: 47 MD / 356 локальных ссылок, включая 5 anchors; diff --check; v2 SHA-256 неизменен. Runtime gates не выполнены. После commit — DOC-REPAIR-04 bootstrap/config/trust и Heimdall v0.13.2.

2026-11-05: DOC-REPAIR-03 IN_PROGRESS — доставка сохранена в c151551, git status чист после commit. Прорабатываются native admission, state pin, capture ownership и lifecycle recovery по D58; никаких runtime-результатов пока нет.

2026-11-05: DOC-REPAIR-02 SPECIFIED — нормативный [автомат доставки](docs/DELIVERY_STATE_MACHINE.md) закрывает DLV-02…13 и OPS-01; всего 17/41 пунктов специфицированы, runtime не проверен. Зафиксирован D59: подтверждённую историю при cutover не переносим. Проверки: 46 MD, 341 локальная ссылка без отсутствующих файлов, git diff --check, неизменный SHA-256 v2. Следующий подэтап после commit: DOC-REPAIR-03, native admission/owned snapshots/decision markers. Разрешены только документы; код, deployment и push не выполнялись.

2026-11-05: DOC-REPAIR-02 IN_PROGRESS / D57–D59 — DOC-REPAIR-01 завершён commit a6ff50c; проверки 45 MD/332 локальные ссылки, source/contract assertions и git diff --check пройдены, v2 неизменен. Теперь delivery state machine/D54/drain/versioning. Приняты удержание state/reject несовместимого backend, Heimdall v0.13.2, ordinary Bor import и VM-safe yield; D59 не переносит уже подтверждённую историю при cutover. Первый commit меняет только документацию; runtime/deployment/push отсутствуют. Следом native admission и bootstrap, затем план/итоговая сверка. Актуальные статусы — DESIGN_REPAIR_STATUS.

2026-11-05: DOC-REPAIR-01 SPECIFIED / D56–D57 — исправлены NAT-01/04/07/08 в реестре типов, MESSAGE_CONTRACT и v3; добавлены AUTH/SELF/LIFE fixtures в [реестр исправлений](docs/DESIGN_REPAIR_STATUS.md). Это 4 из 41 документальных пунктов, runtime не проверен. D57 принят: local fatal завершает native процесс, drain отдельным запуском без нового capture, несовместимое обновление после drain старой сборкой. Вопросы state retention/backend, Heimdall v0.13.2 и Bor import modes ещё открыты. После проверок — отдельный commit этого подэтапа, затем DOC-REPAIR-02. Код/runtime/deployment/push запрещены; исходный аудит 8549a2a сохраняется как история находок.

2026-11-05: DOC-AUDIT-02 DONE / D55 как аудит — [подробный реестр](docs/DEEP_DESIGN_AUDIT_2026_11_05.md): 41 находка, 29 P1 и 12 P2; native semantics, delivery/recovery, bootstrap/config/operations и порядок разработки. У каждого пункта source, сценарий, последствие, предложение и будущий fixture. Проверены exact upstream refs и конкретные Geth/Bor/EIP факты; runtime/сборок нет. Исправления НЕ завершены, предложения не стали принятыми решениями. Новых самостоятельных продуктовых вопросов нет. Следующий рекомендуемый документальный шаг: NAT-01/04/07/08, затем native ownership/intent, delivery state machine/D54/memory/versioning, bootstrap; после этого консолидация и DEP-04. Только проектирование/документация, без реализации/deployment/push. Контрольный коммит аудита — в git log; предыдущая фиксация 8a88e5f.

Проверки DOC-AUDIT-02: 44 Markdown-файла, 325 локальных ссылок/anchors без ошибок; все 41 ID уникальны, у каждого есть source/предложение/fixture; git diff --check пройден. SHA-256 v2 остался `ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634`. Изменены только шесть Markdown-файлов. Native/delivery выводы прошли повторную сверку; неподтверждённая гипотеза о receipt bloom исключена. Пост-коммитная точка: аудит закончен, исправление 41 находки не начато, публикация/deployment отсутствуют.

2026-11-05: финальная фиксация DOC-AUDIT-01 — устранены два оставшихся расхождения порядка Prepare/layout в v3 и DELIVERY_RECOVERY. Аудит и согласование D54 закончены, консолидация и технические T01–T05 ниже остаются. Проверены 43 Markdown-файла / 305 локальных ссылок и anchors без ошибок, git diff --check пройден; SHA-256 v2 неизменен, все 19 изменённых файлов — Markdown. Контрольный коммит этой фиксации — в git log; remote не публиковать. Точка продолжения — документальная детализация capture intent/destination checks и deployment, без реализации.

2026-11-04: DOC-AUDIT-01 DONE / D53–D54 — [аудит](docs/DESIGN_AUDIT_2026_11_04.md) и исправления действующих документов. Пользователь принял запрет смены logical destination NATS до завершения pending; URL того же назначения может меняться после проверки. Исправлены scope/gates/ADR, YAML stream/subjects, владельцы part layout и границы Gateway lifecycle validation. Единственный вопрос раунда закрыт; технические T01–T05 остаются открытыми, особенно durable intent до STAGING и точная проверка destination/drain. DOC-CONSOLIDATE и DEP-04 не завершены. Следующее действие: документально проработать T01/T02 и .3b/pins, затем собрать единое ТЗ и провести DEP-04. Код/runtime/deployment/push не разрешены; эта контрольная точка коммитится до следующего подэтапа.

2026-10-22: DOC-CONSOLIDATE IN_PROGRESS — по поручению пользователя собрать действующее ТЗ в один MD в корне без дополнительных вопросов. Сохранить источники и оригинал v2; совместить актуальные D16–D52, контракт сообщений, WAL/recovery, deployment, YAML и этапы, явно вынести оставшиеся технические проверки. После проверки создать коммит. Код/deployment не разрешены.

2026-10-22: фиксация D52 завершена. Операционная готовность по логам; управление Docker/Kubernetes без отдельной probe/shutdown спецификации; frame default 2 MiB, NATS лимиты согласованы, pending bytes неизменны; локального Git достаточно. DEP-03.3b и E00-05 DONE, E00-11 пункт 3 закрыт. Проверены локальные ссылки и git diff --check; SHA-256 v2 неизменен. Контрольный коммит D52 — в git log. Ожидать обещанного указания пользователя по оформлению результата, закрытые вопросы повторно не задавать. Техническая детализация .3b/pins и DEP-04 сверка ещё не выполнены; E00-11 не объявлен завершённым. Только документы, без реализации/runtime/deployment/push.

2026-10-21: D51 принят; DEP-03.2b.2b.3a DONE — штатные заменяемые configs/init-шаг окружения, сохранение state/ключей, read-only `/config` и writable native файлы `/data/node` зафиксированы в ТЗ и deployment/live документах. [REMAINING_AGREEMENTS](docs/REMAINING_AGREEMENTS.md) содержит актуальные согласования отдельно от технической детализации и отложенного scope. Проверены локальные ссылки и git diff --check; SHA-256 v2 неизменен. Контрольный коммит D51 — в git log. Далее .3b точные flags/config keys/init/mount mappings, затем DEP-03.3b готовность/остановка, E00-11 размер frame, E00-05 backup документов и разделение финализации ТЗ/разрешения на реализацию; DEP-04 итоговая сверка. E00-11 остаётся открыт. Только документация, без runtime/deployment/push.

2026-10-21: D50, DEP-03.2b.2b.2 DONE. По делегированию пользователя выбраны порты/экспонирование в [NETWORK_PORTS](docs/NETWORK_PORTS.md): уникальные P2P-порты с одинаковым отображением, Compose bridge/mappings, Kubernetes NodePort; LoadBalancer опционально. Внутренние endpoints перечислены. Проверены официальные источники, локальные ссылки и git diff --check; SHA-256 v2 неизменен. Контрольный коммит D50 — в git log. Далее DEP-03.2b.2b.3: точные native flags/config keys и mount mappings; следующий вопрос — как готовить штатные конфиги и сохранять изменяемые native файлы при read-only /config. Затем DEP-03.3b probes/shutdown и DEP-04. E00-11 пункт 2 открыт. Только ТЗ, без реализации/runtime/deployment/push.

2026-10-21: D49 принят; DEP-03.2b.2b.1 DONE — defaults внутренних портов, публикации P2P, каталогов и сохранения P2P-идентичности внесены в NODE_DEPLOYMENT_SPEC, ТЗ, LIVE_CONFIG_SPEC и план. Локальные ссылки и git diff --check пройдены; SHA-256 оригинала v2 неизменен. Контрольный коммит D49 — в git log. Далее DEP-03.2b.2b.2: предложить точную таблицу P2P-портов/протоколов и оставшихся native endpoints, экспонирование Compose/Kubernetes; новые решения сначала согласовать. Затем .3 flags/mount mappings, DEP-03.3b probes/shutdown и DEP-04. E00-11 пункт 2 остаётся IN_PROGRESS. Только документация, без реализации/runtime/deployment/push.

2026-10-21: D48 принят; DEP-03.2b.2a DONE — гибкая конфигурация зафиксирована в ТЗ, NODE_DEPLOYMENT_SPEC, LIVE_CONFIG_SPEC и плане: отдельные YAML сборщика/Gateway каждой сети, штатные native configs/args, Compose overrides/Kubernetes values, отдельные secrets, применение перезапуском компонента. Проверены локальные ссылки и git diff --check; SHA-256 оригинала v2 неизменен. Контрольный коммит D48 — в git log. Далее DEP-03.2b.2b: предложить конкретные defaults/порты/P2P/mounts и согласовать новые решения, затем DEP-03.3b probes/shutdown и DEP-04. E00-11 пункт 2 остаётся IN_PROGRESS. Только ТЗ, без реализации/runtime/deployment/push.

2026-10-21: D47 принят; DEP-03.2b.1 DONE — согласованные кандидаты версий зафиксированы в ТЗ, матрице и плане; источники и ограничения в [DEPLOYMENT_VERSION_CANDIDATES](docs/research/DEPLOYMENT_VERSION_CANDIDATES.md). Прежний source audit сохранён как исторический; новые Geth/Bor требуют проверки изменений native hooks. Локальные ссылки и git diff --check пройдены, SHA-256 оригинала v2 неизменен. Контрольный коммит D47 — в git log. Следующий шаг DEP-03.2b.2: предложить точные native configs/порты/P2P/mounts; новые решения сначала согласовать, затем DEP-03.3b probes/shutdown и DEP-04. E00-11 пункт 2 остаётся IN_PROGRESS. Совместная сборка/runtime и image digests не проверены; только документация, без реализации/deployment/push.

2026-10-21: D46 принят, DEP-03.2a/03.3a DONE: StatefulSet replicas=1/Service/PVC каждого компонента, Compose services/named volumes, service DNS, раздельные configs/secrets, независимая готовность. NODE_DEPLOYMENT_SPEC содержит карту связей и хранения. Следующий шаг DEP-03.2b — исследовать и предложить совместимые версии, затем точные конфиги/порты/P2P/mounts; DEP-03.3b probes и DEP-04 остаются. Версии этим ответом не утверждены. Только документы, без кода/deploy. Проверены ссылки и git diff --check; SHA-256 v2 неизменен. Контрольный коммит D46 — в git log. Новые решения сначала согласовывать.

2026-10-21: D45 принят, DEP-03.1 DONE: отдельные Compose services и Kubernetes workloads/Pods каждого самостоятельного компонента; карта 11 основных сервисов в NODE_DEPLOYMENT_SPEC. Адаптеры встроены, RocksDB embedded, у owner отдельное состояние; обязательного общего хоста нет. Далее DEP-03.2 — версии/конфиги/workload types/связи, затем DEP-03.3 readiness и DEP-04 сверка. Пункт 2 E00-11 не завершён. Только документы, без реализации/deploy. Проверки ссылок и git diff --check пройдены; SHA-256 v2 неизменен. Контрольный коммит D45 — в git log. Новые решения сначала согласовывать.

2026-10-21: D44 принят, DEP-02 DONE: Prysm для Ethereum, отдельный Geth/Prysm без Ethereum-адаптера для L1 нужд Polygon. Обновлены ТЗ, состав, матрица и план. Далее DEP-03: согласовать layout контейнеров/Pod и закрепить совместимые версии/конфигурации; выбор одного контейнера не принят. DEP-04 и E00-11 пункт 2 остаются открытыми. Только документация, без сборок/deploy. Локальные ссылки и git diff --check пройдены, SHA-256 v2 неизменен. Контрольный коммит D44 — в git log. Новые решения сначала согласовать.

2026-10-21: E00-11 пункт 2 IN_PROGRESS, D43 принят. Ноды со всеми компонентами, их компоновка/конфигурация для Compose и Kubernetes входят в задачу. [NODE_DEPLOYMENT_SPEC](docs/NODE_DEPLOYMENT_SPEC.md): DEP-01 DONE, DEP-02–04 открыты; E06-13 фиксирует будущую реализацию полного комплекта. Далее DEP-02: согласовать Ethereum consensus-клиент и Polygon L1 topology, без молчаливой внешней зависимости. Общий CLI не управляет оркестратором. Только документы, сборки/deploy не выполнялись. Проверены локальные ссылки и git diff --check, SHA-256 v2 неизменен. Контрольный коммит D43 — в git log. Дополнительные решения и правки сначала согласовывать.

2026-10-21: E00-11 пункт 1 DONE как правки документации, D42: текущие ETH/BSC/Polygon; Base/Arbitrum отложены. Обновлены ТЗ, активные YAML-профили, обязательные задачи и gates трёх сетей. Исследования пяти сетей сохранены; Classic/исторические сценарии отложены D38/D42, E07-04 release bundle — D41. У каждой сети собственные WAL/журнал Gateway. Локальные ссылки и git diff --check проверены, SHA-256 оригинала v2 неизменен. Только документы; контрольный коммит D42 — в git log. Далее обсуждать отдельно пункт 2 E00-11: внешние supporting-компоненты нод; затем пункт 3 о размере transport frame и пункт 4 о разделении завершения ТЗ/разрешения на реализацию. Эти предложения пока не приняты, без согласования не менять. E00-11 в целом открыт.

2026-10-21: D41 принят. Лицензия и open-source поставка отложены: E00-08/OSS-01/02 и E07-07 DEFERRED_BY_USER, не блокируют текущее ТЗ. Open-source намерение сохраняется; публикация не разрешена. Следующий блок — E00-11: read-only сверка финального архитектурного пакета и remaining supporting-client profiles, предложения изменений сначала согласовать. Только документация. Проверены локальные ссылки и git diff --check; SHA-256 оригинала v2 неизменен. Контрольный коммит D41 — в git log. Реализация и deployment не выполнялись.

2026-10-21: E00-04 DONE в согласованной границе D40. Последовательность live-only подтверждена; каждой сети принадлежат собственные WAL адаптера, журнал Gateway, процессы и прогресс. Общий multi-network WAL/writer запрещён; ранее принятый общий NATS сохраняется. Performance требования/benchmark исключены, production-методика отложена; предложения 24 часа/30 минут не приняты. Функциональные fixtures и корректность протокола остаются. Обновлены ТЗ, план, конфигурация, архитектура и владельцы состояния. Локальные ссылки и git diff --check пройдены; SHA-256 v2 неизменен. Только документация, без runtime/deploy. Контрольный коммит D40 — в git log. Далее E00-08 (лицензия/open-source поставка); E00-05/11 и не выбранные supporting-client profiles остаются открытыми. Новые изменения сначала согласовывать с пользователем.

2026-10-21: CFG-S05 и E00-07 DONE как документация по четырём согласованным правкам D39. Ноду запускает окружение; live начинается с H+1 после синхронизации; pending WAL передаётся независимо. Активный CONFIG_CONTRACT очищен; основное ТЗ, LIVE_CONFIG_SPEC, архитектура и критерии плана согласованы. Проверки локальных ссылок и git diff --check пройдены; SHA-256 оригинала v2 неизменен. Код, runtime-тесты и deployment не выполнялись. Контрольный коммит D39/CFG-S05 — в git log. Далее E00-04: численные параметры приёмки и оставшиеся ограничения; E00 в целом не завершён. Новые изменения сначала согласовывать с пользователем.

### Более ранние записи

2026-10-21: CFG-S04 DONE как технический проект: [LIVE_CONFIG_SPEC](docs/LIVE_CONFIG_SPEC.md). Описаны команды, поля YAML, пять native-профилей, запуск встроенных адаптеров, проверки путей/identity/TLS и restart pending WAL. CONFIG_CLI_DRAFT архивный. Предложения CLI/native launch и gate live требуют итоговой сверки CFG-S05; они не объявлены реализованными или проверенными runtime. Следующий шаг — CFG-S05, синхронизация ТЗ/плана/критериев в live-only scope. История и пропуски отложены. Локальные ссылки и git diff --check пройдены, SHA-256 v2 неизменен. Только документы; контрольный коммит CFG-S04 — в git log.

2026-10-21: D38 принят. История и загрузка пропусков DEFERRED_BY_USER. CFG-R4 снят, ответы не нужны. CFG-S04 продолжается только для live/Gateway: уточнить профильные native.source и завершить YAML/CLI. Pending WAL и live reorg/finality сохраняются. Предыдущие записи ниже — история работы, не актуальные ожидаемые вопросы. Только ТЗ. Локальные ссылки и git diff --check проверены; SHA-256 v2 неизменен. Контрольный коммит D38 — в git log. Реализация не выполнялась.

2026-10-21: CFG-S04 IN_PROGRESS. Проект полей YAML, CLI, валидации и exit codes сохранён в [CONFIG_CLI_DRAFT](docs/CONFIG_CLI_DRAFT.md), не утверждён. Ожидаются CFG-R4.1 (новый history run / явный resume) и CFG-R4.2 (M ещё не существует). Затем уточнить закрытые профильные схемы native.source; CFG-S04/S05 пока не завершены. Только ТЗ; D36/D37 сохраняются. Локальные ссылки и git diff --check проверены, SHA-256 v2 неизменен. Контрольный коммит проекта CFG-S04 — в git log. Код и runtime-проверки не выполнялись.

2026-10-21: D37 принят, CFG-R3/CFG-S03 закрыты в текущем scope. Учёт запусков, сообщение в отдельный NATS канал/таблица ClickHouse/скрипт сверки — контекст будущего внешнего механизма, не проектируется сейчас. Live не ищет/не догружает разрывы за время остановки; pending WAL повторно отправляется автоматически. Текущая задача: native сбор → единая типизированная нормализация → NATS, плюс явный history N–M по D36. Следующий шаг CFG-S04 — точный YAML/CLI без watcher/apply/default истории/контроля полноты. Только документы. Проверки: локальные ссылки корректны, git diff --check пройден, SHA-256 оригинала v2 неизменен. Контрольный коммит D37 — в git log; реализация и runtime-проверки не выполнялись.

2026-10-21: D36 принят — live-only основной загрузчик, история только отдельным сервисом на том же узле, что и нода, по отдельной команде N–M. Отменены default три часа/start_block/apply-triggered history. CFG-R2 снят; активные документы/план обновлены, E06-06 помечен REMOVED_BY_D36. E00-07/E00-09 IN_PROGRESS: ожидается CFG-R3 о новых блоках за время остановки live, отдельно от автоматической передачи pending WAL. Затем точная YAML/schema/CLI. Проверки ссылок и git diff --check пройдены, SHA-256 v2 неизменен; только ТЗ. Контрольный commit — в git log.

2026-10-21: E00-07/E00-09 IN_PROGRESS, CFG-S02 DONE. D34/D35: единый интерфейс всех сетей, изменение YAML + явный apply, native реализация внутри контура сети, никакой зависимости от Kubernetes и никаких управляющих изменений остальных компонентов. Без рестарта меняется только start_block. Предложение watcher/Kubernetes launcher отклонено. [CONFIG_CONTRACT](docs/CONFIG_CONTRACT.md) обновлён; следующий CFG-S03 — границы догрузки и повторные изменения. Действующие N–M, RocksDB/ACK и публикация неизменны. Только документы, после проверки commit; ответы следующего раунда не подставлять самостоятельно.

2026-10-20: E00-07/E00-09 IN_PROGRESS — начат CFG/JOB-01, только ТЗ. Принятые D06/D10/D12/D14/D23/D26–D33 не пересматриваются молча. [CONFIG_CONTRACT](docs/CONFIG_CONTRACT.md) сохраняет фиксированные требования, draft структуры и раунд CFG-R1: владелец автоматического запуска отдельного history pod, hot-reload поля, применение изменений к активной команде N–M. Ответы ожидаются; draft не утверждён. До ответов не фиксировать launcher/полномочия Kubernetes или поведение остановки задания как принятые.

2026-10-16: E00-03.07 DONE, родитель E00-03 DONE как архитектурный блок ТЗ. Результат: [E00_03_ARCHITECTURE_REVIEW](docs/E00_03_ARCHITECTURE_REVIEW.md). Проверены D16–D33, матрица компонент/стек/состояние/гарантии/fixtures; исправлены устаревшие REC/WAL/ARCH-03 статусы, block-batch ACK и потоковая передача до READY/CLOSED. Уточнено минимальное lifecycle_state для Removed/Status после ACK cleanup. Исторические исследования/ADR не заменяют последние решения.

**Только ТЗ.** Код, сборки, runtime/crash tests, POC и deployments не выполнялись. Их задачи остаются E01–E07 и не запускаются автоматически.

E00 в целом IN_PROGRESS. Остаются E00-04 (приёмка/численные параметры/supporting profiles), E00-05 (локальный Git принят, remote/backup и разрешение на реализацию нет), E00-07/E00-09 (YAML/reload/startup), E00-08 (лицензия/публикация), E00-11 (финальный пакет ТЗ). **Следующий шаг после commit — E00-07 + E00-09, раунд CFG/JOB-01.** Не повторять выбор сетей, RocksDB, N–M, отдельных pods, одного NATS и требований REC.

2026-10-20: финальная проверка .07 выполнена перед коммитом: 185 локальных ссылок/anchors без ошибок; 60 task IDs и 15 строк статусов E00 без дублей; git diff --check пройден. SHA-256 исходного v2 неизменен: ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634. Хеш контрольной фиксации — в git log. Никакие документы ниже не означают уже начатый код.

## История контрольных точек

Ниже сохранена хронология. Старые TODO/WAITING/«следующий шаг» относятся к состоянию на момент записи; текущий статус — только раздел выше.

2026-10-16: E00-03.07 IN_PROGRESS — по поручению пользователя итоговая сверка только ТЗ. Проверить scope D16–D33, согласованность компонентов/стека/контракта/recovery, состояние E00 и задачи будущей реализации. Исправить противоречия, сохранить матрицу результата и оставшиеся вопросы, проверить ссылки/ID/неизменность v2, commit. Код, сборки, runtime tests и deployment не выполнять.

2026-10-16: E00-03.06 DONE как проектный контракт; E00-06 REC также DONE в текущем scope. [Pins](docs/research/ROCKSDB_STACK.md): RocksDB 10.4.2, grocksdb v1.10.2, Rust rocksdb 0.24.0 / librocksdb-sys 0.17.3+10.4.2; source/API evidence проверены. [Протокол](docs/ROCKSDB_DELIVERY_PROTOCOL.md): sync READY, durable publication plan/frame bytes, publication_epoch, block IngestAck, ACKED cleanup/release, single writer, recovery и REC-T01–16. Обновлены ТЗ, контракт, стек, владельцы состояния и план. Сборки/ABI/benchmarks/crash tests не выполнялись; это E01/E03/E05/E06. Следующий шаг после проверок и commit — E00-03.07, итоговая сверка архитектуры; реализация не начата. Более ранние записи ниже описывают историю закрытых вопросов, а не текущие блокеры.

Проверка закрытия .06: 157 локальных ссылок/anchors без ошибок, 60 основных ID без дублей; git diff --check пройден, SHA-256 оригинального v2 совпал с исходным. Source/API pins проверены; runtime tests не запускались. Хеш контрольной фиксации смотреть в git log.

История выполнения: E00-03.06-B/C/D были начаты по поручению «выполняй»; теперь закрыты как документы, результат выше. .07 до успешного commit не начинать.

2026-10-16: D33 accepted — пользователь утвердил RocksDB. E00-03.06-B PARTIAL: движок выбран, версии engine/Go/Rust bindings и детали durable записи/очистки ещё открыты. Следующее действие внутри .06 — закрепить совместимые pins, затем завершить ACK/checkpoint/Gateway recovery и crash fixtures (.06-C/D). Обычный RocksDB — baseline, integrated BlobDB optional для будущих измерений. Реализация и benchmark не выполнялись; .06 целиком не закрыт. Результаты исследования ниже — история выбора, не повод повторно спрашивать о движке.

2026-10-16: E00-03.06-B IN_PROGRESS — исследование завершено, выбор ещё открыт. SQLite исключён D32. [Рекомендация RocksDB](docs/research/WAL_ENGINE_SELECTION.md), [сравнение альтернатив](docs/research/WAL_ALTERNATIVES.md): baseline RocksDB + sync WriteBatch, integrated BlobDB как отдельный POC-кандидат для больших payload. Pebble — резерв для Go; MDBX/Badger/сегментный log имеют описанные tradeoffs. POC и benchmark не запускались, bindings/version не выбраны. Следующее действие — согласовать engine baseline и завершить протокол checkpoint/cleanup/Gateway в .06-C; .07 не начат. Проверки локальных ссылок и git diff --check выполняются перед коммитом; оригинальный v2 не менять.

2026-10-16: E00-03.06 IN_PROGRESS. Ответы REC сохранены D26–D31: restart с диском автоматический; потеря диска — новый запуск и ручной N–M; накопление WAL без TTL/прикладного лимита; удаление после ACK всего delivery batch; один NATS, HA вне scope; архив доставленных статусов не нужен. [Контракт](docs/DELIVERY_RECOVERY.md). Следующее действие внутри .06: выбрать embedded backend для Go/Rust, определить atomic write/flush/checkpoint/cleanup и single-writer ownership, затем проверить согласованность и закрыть .06. Пользовательские ответы получены, дополнительных вопросов сейчас нет. Этот checkpoint коммитится до продолжения.

2026-10-16: E00-03.05 DONE как документация. D23–D25 приняты: разные live/history pods, простая команда N–M; overlap/dedup и независимая проверка полноты вне ТЗ. Результат: [LIVE_HISTORY_DELIVERY](docs/LIVE_HISTORY_DELIVERY.md). Учёт RECEIVED → WAL_SAVED → NATS_ACKED; хранилище прогресса — предложение, backend не выбран. Следующий подэтап .06: доставка, локальный WAL/журнал и REC (сохранность, restart, потеря диска, retention). Перед переходом обязателен коммит текущих изменений. Реализация не начата.

Проверка D23–D25: 128 локальных ссылок/anchors без ошибок; git diff --check пройден; SHA-256 исходного v2 неизменен. Статусы и план синхронизированы; снятая E06-04 сохранена с пометкой REMOVED_BY_D25. Runtime-тесты не запускались: изменения только в документации. Хеш этой фиксации — в git log.

### Предыдущие контрольные точки (история; актуальное состояние выше)

2026-10-15: пользователь инициализировал Git и поручил закоммитить текущее состояние. Проверено: ветка master, до этой фиксации коммитов нет; исходные документы и результаты E00 входят в начальный snapshot. В AGENTS.md добавлено обязательное правило коммита после изменений и проверок перед переходом к следующему этапу/подэтапу. Фактический хеш checkpoint смотреть в git log; remote-публикация не запрошена. Текущий архитектурный вопрос ARCH-03 остаётся без ответа.

2026-10-15: D21/D22 приняты: Compose + Kubernetes входят в поставку; NATS разворачивается внутри обеих сред. E00-03.02 DONE. .03 DONE как проектный baseline: [стек](docs/COMPONENT_STACK.md), [NATS packaging](docs/research/NATS_DEPLOYMENT_STACK.md). .04 DONE как логическая модель: [владельцы состояния](docs/STATE_OWNERSHIP.md). Совместные builds/runtime не проверены, manifests не создавались.

2026-10-15: E00-03 IN_PROGRESS; [подэтапы .01–.07](docs/E00_03_COMPONENT_PLAN.md), .01–04 завершены как документы. Текущий .05 WAITING_USER: ARCH-03 — допускается ли отдельный history-процесс по необходимости с дополнительными CPU/RAM и историческим состоянием на диске, либо сначала измерить затраты на POC? Рекомендация не является ответом. После ответа записать решение и завершить live/history-модель. REC и YAML/history control остаются отдельными пулами.

2026-10-15: E00-15 DONE как исследование и проект исторических профилей. Созданы [ARBITRUM_CLASSIC_CAPTURE.md](docs/research/ARBITRUM_CLASSIC_CAPTURE.md) и [POLYGON_LEGACY_CAPTURE.md](docs/research/POLYGON_LEGACY_CAPTURE.md). Для Classic определены native inventory, Go/C++ AVM observer candidate и проверка неизменности исторического исполнения; для Bor — optional auxiliary receipt отдельно от обязательного body StateSyncTx. Реестр, контракт, матрица и ТЗ согласованы. Архивы не скачивались, сборки/реализация/runtime-приёмка не выполнялись; CL-01…08/POLY-L01…07 остаются будущими fixtures.

2026-10-15: E00-14 DONE как проект логической модели. Созданы [TRANSACTION_TYPE_REGISTRY.md](docs/TRANSACTION_TYPE_REGISTRY.md) и [MESSAGE_CONTRACT.md](docs/MESSAGE_CONTRACT.md): native variants пяти сетей, применимость полей, raw encodings, transaction/block/coverage/lifecycle, CompleteRecord/RecordPart. Старая proto-заготовка сохранена в архиве и исключена из действующего ТЗ. Proto/validators/fixtures и точные network execution/finality extensions остаются E02; реализация не начата.

E00-13 DONE: первый пункт по прямому указанию пользователя выполнен как исследование. [CLIENT_MATRIX.md](docs/CLIENT_MATRIX.md) и три файла docs/research содержат рекомендуемые клиенты, pinned commits, toolchains, native capture paths, историю и ограничения всех пяти сетей. Сборки, ноды, POC и изменения кода не выполнялись.

E00-11 IN_PROGRESS: финализировать архитектуру через E00-03. ARCH-03 закрыт D23; следующим идёт .06, зависит от REC, .07 от полноты обязательных решений. D21/D22 не выбирают replicas/storage/retention. Classic ещё не прошёл POC; E00-15 не является обещанием готовой истории от genesis. Запуск реализации требует отдельного указания.

Рекомендуемый source-verified стек: Geth v1.17.8, BSC v1.7.8, base/base v1.5.0 + pinned base/reth, Nitro v3.12.1 + own Geth, Bor v2.10.2 + Heimdall-v2 v0.12.2. Go adapters ETH/BSC/ARB/POLY, Rust Base. Конкретные pins и toolchain расхождения в матрице; это не runtime-приёмка.

MSG-01/MSG-02/DEPLOY-02 приняты: строгие общие типы; крупные записи разбиваются; каждой сети свой node/adapter/WAL/Gateway, общий NATS с раздельными streams/subjects/лимитами. Главный критерий — все транзакции и детали; изменения вне транзакций optional, можно опустить при усложнении. Полная история всех балансов тогда не заявляется.

JOB-01/YAML отложен пользователем D20 для отдельного обсуждения простоты и эффективности; ответ не выбран. Не задавать его сейчас и не финализировать reload молча. REC также остаётся отдельным отложенным пулом. DB-01 снят; точные сети повторно не уточнять.

## Подтверждено

- Ethereum Mainnet, BSC Mainnet и Polygon PoS — текущий обязательный контур по D42; Base/Arbitrum отложены.
- Каждая сеть — независимый процесс/контейнер/pod с собственным конфигом; добавление/изменение одной не перезапускает остальные.
- Все включённые в блоки транзакции обязательны: успешные, failed и родные системные типы со всеми применимыми деталями/действиями. Изменения вне транзакций — необязательное расширение по D17.
- После синхронизации ноды фиксируется H; новые live-наблюдения начинаются с H+1. Pending WAL передаётся независимо, saved lifecycle/reorg/finality сохраняются; snapshot всех адресов не нужен.
- История N–M, startup history и поиск пропусков отложены D38. YAML применяется рестартом, без hot reload.
- Open-source намерение сохраняется; лицензия и публикация отложены D41. Отдельные YAML live/Gateway, native configs/args и Compose/Kubernetes overrides приняты D48/D51.
- Основная единица — одна транзакция; крупные записи разбиваются на части. Default полного frame с NATS headers — 2 MiB по D52; part layout определяет Gateway, producer сохраняет immutable plan/bytes. Точная wire schema ещё требует E02.
- Конечная граница — NATS JetStream PubAck. Один NATS instance/stream replica; отдельные RocksDB WAL и Gateway journals сетей. Pending без TTL/прикладной квоты до ACK всего delivery batch; post-ACK судьба данных вне гарантии сборщика.
- D54: logical destination NATS можно менять только после завершения pending адаптера и Gateway; ACK разных назначений не суммируются. Проверка same-destination при смене адреса обязательна.
- Оригинал v2 неизменяем. Работа ведётся в v3-codex. Все решения, задачи и текущая точка сохраняются в файлах.

## Историческая карта документов до D56

Описание ссылок ниже сохранено как снимок прежнего состояния: «пять сетей», «открытые 41 находка» и «точный protobuf ещё проект» не являются текущим статусом. Актуальные результаты и следующая задача указаны в начале этого файла; итоговая сверка — docs/FINAL_SPEC_REVIEW.md.

- [Стек компонентов](docs/COMPONENT_STACK.md) — версии-кандидаты, изоляция native toolchains и NATS packaging.
- [Владельцы состояния](docs/STATE_OWNERSHIP.md) — lifecycle/IDs/bytes/parts, publication mapping/destination и ACK; RocksDB принят D33.
- [E00-03 по шагам](docs/E00_03_COMPONENT_PLAN.md) — .01–.07, карта компонентов, текущие вопросы и критерии завершения.
- [Classic historical capture](docs/research/ARBITRUM_CLASSIC_CAPTURE.md) — source profiles, original inventory, observer candidate и fixtures CL-01…08.
- [Polygon legacy capture](docs/research/POLYGON_LEGACY_CAPTURE.md) — separate receipt, optional scope, native replay и fixtures POLY-L01…07.
- [Реестр native типов](docs/TRANSACTION_TYPE_REGISTRY.md) — поля, применимость, encoding/hash особенности и ограничения пяти сетей.
- [Контракт сообщений](docs/MESSAGE_CONTRACT.md) — логическая схема, block metadata, lifecycle, parts/destination и будущие fixtures; без production verifier полноты.
- [Матрица клиентов](docs/CLIENT_MATRIX.md) — проверенные исходники, рекомендации стека, capture/history ограничения и будущие POC.
- [Краткая архитектура](docs/COLLECTION_ARCHITECTURE.md) — компоненты, границы, стек-кандидат и порядок финализации.
- [Рабочее ТЗ v3](evm-transaction-extractor-tz-v3-codex.md) — детальные требования; строгие типы и разбиение приняты, точный protobuf ещё проект.
- [Решения и R07](docs/DECISIONS.md) — авторитетные решения, история отменённых требований и открытые вопросы.
- [План и задачи](docs/DEVELOPMENT_PLAN.md) — E00–E08; снятые ID не переиспользуются.
- [Балансовые данные](docs/BALANCE_MODEL.md) — действия, сложные случаи и контракт нормализации.
- [Словарь](CONTEXT.md) — термины.
- [Повторный аудит](docs/DESIGN_AUDIT_2026_11_04.md) — исправленные противоречия, D54 и технический остаток T01–T05.
- [Глубокий аудит flow и разработки](docs/DEEP_DESIGN_AUDIT_2026_11_05.md) — актуальные 41 открытая находка, приоритеты, counterexamples и предложенная детализация этапов.

## Исторические пробелы аудита D55

Актуальный детальный реестр: DOC-AUDIT-02, NAT-01–10 / DLV-01–13 / OPS-01–12 / DEV-01–06. T01–T05 предыдущего аудита сохранены как более крупные группы; их нельзя считать закрытыми наличием нового отчёта. Среди новых подтверждённых дефектов: source authorization signature, представление SELFDESTRUCT, отмена pending Applied и ранний StatusChanged; среди критичных механизмов — snapshot mutable hooks, capture/lifecycle intent, frontiers/fencing, drain и destination до Prepare, bounded serialization, init/cutover/upgrade.

Точный wire schema, network execution/finality variants, action enums и validators требуют E02; lifecycle reconciliation/GC — E05. До закрытия REC-T01 нужно устранить неописанное окно crash до durable capture descriptor. Prepare/plan после D52 и destination/ACK/drain checks D54 требуют технической детализации E03. Кандидаты клиентов/библиотек и RocksDB выбраны; остаются source/image pins, delta audit Geth/Bor, native config/init/mount mappings и DEP-04. Сборки/runtime не доказаны. Исторические Classic/Bor legacy и Base/Arbitrum не входят в текущий gate; performance требования сняты D40. Полный список — T01–T05 в аудите.

## Проверки

Перед начальным Git-коммитом проверены 28 Markdown-файлов, 127 локальных ссылок без отсутствующих целей; распространённые шаблоны credentials не обнаружены. SHA-256 v2 совпал с исходным. Удалены лишние завершающие пустые строки из архивной proto-заготовки. Единственное оставшееся предупреждение git diff --check — исходная пустая строка в конце неизменяемого v2; оригинал намеренно сохранён побайтно. Код/runtime не менялись.

Проверка после D21/D22 и E00-03.03–04: девять затронутых документов, 93 локальные ссылки/anchors без ошибок; 59 основных ID и семь подэтапов уникальны. SDK/toolchain manifests и NATS chart/server candidates проверены по первичным источникам. SHA-256 v2 совпал с исходным. Совместные builds, registry digests, manifests и runtime-тесты не выполнялись. ARCH-03 ещё ожидает ответа.

Проверка после декомпозиции E00-03: пять затронутых документов, 56 локальных ссылок/anchors без ошибок. 59 основных ID задач и семь уникальных подэтапов E00-03.01–07; дубликатов нет. SHA-256 v2 совпал с исходным. Новых runtime-проверок нет: выполнены сверка документов и планирование. Ответы ARCH-01/02 пока не получены.

Проверка после E00-15: 11 затронутых документов, 86 локальных ссылок/anchors без ошибок; 59 ID задач уникальны, в E00 закрыты 7 из 15 пунктов. Проверены 32 уникальных pinned source URL двух новых исследований — доступны; official Classic guide прочитан, архив не скачивался. SHA-256 оригинала v2 совпал с исходным. Runtime fixtures не выполнялись; source research не доказывает полноту истории.

Проверка после E00-14: восемь затронутых документов, 51 локальная ссылка/anchor без ошибок; 59 ID задач уникальны. Все 15 внешних ссылок из новых документов доступны; это проверка источников, не runtime-приёмка. SHA-256 оригинала v2 совпал с исходным. Source facts отделены от проектных правил и открытых E00-15/E02/REC. Сборки, генерация proto, ноды и тесты реализации не выполнялись.

Проверка после E00-13: девять документов проверены; локальные ссылки и anchors корректны, 59 ID задач уникальны. SHA-256 оригинала v2 совпал с исходным. Research-файлы содержат прямые ссылки на manifests/source commits и отдельно помеченные будущие POC. Сборки, ноды, runtime-тесты и benchmark не выполнялись; E00-13 закрыт только как source research.

Проверка после D17–D20: девять документов проверены; локальные ссылки корректны, 56 ID задач уникальны. Подтверждены строгий реестр типов, optional системные балансы, принятая topology и отложенный JOB-01. В ТЗ нет требований к обработке после NATS. SHA-256 v2 совпал с исходным. Код и runtime-тесты не выполнялись: этап документальный.

Ожидаемый SHA-256 исходного v2: ea825f0f0c3b747133de216c5bb1b15bc4f1d0601380ff1b6c3c1a4b620bc634.

Проверка после D16: проверены девять документов; локальные ссылки корректны, 53 ID действующих задач уникальны. В активном ТЗ, плане, модели балансов и карте архитектуры нет требований к последующему хранилищу/SQL/шардированию. Десять снятых ID сохранены отдельно. SHA-256 v2 совпал с исходным. Тестов реализации нет; проверка документации не доказывает полноту будущих интеграций.

## Журнал контрольных точек

- 2026-10-15 / D21–D22, E00-03.02–05: ответы о Compose/Kubernetes и NATS приняты; .02 закрыт. .03 source-checked stack и .04 logical ownership сохранены. .05 ждёт ARCH-03; runtime/code/deployments не выполнялись.

- 2026-10-15 / E00-03.01–02: сохранена декомпозиция на семь подэтапов; .01 DONE, .02 WAITING_USER. Исправлены устаревшие формулировки REC-01/05 по D16 без открытия REC. Следующее действие зависит от ARCH-01/02; реализация не начата.

- 2026-10-15 / E00-15: завершены два source research historical profiles. Уточнены native origins, inventory, optional Bor legacy scope и consensus-neutral Classic observer candidate. Все доказательства runtime оставлены будущим этапам; следующая задача E00-03.

- 2026-10-15 / E00-14: сохранены TRANSACTION_TYPE_REGISTRY и MESSAGE_CONTRACT; старый proto/Any sketch вынесен в docs/archive. ТЗ, архитектура, план и журнал согласованы. Следующий шаг E00-15; JOB-01/REC остаются отложенными.

- 2026-10-13: созданы отдельная v3, план, решения, словарь и restart-safe инструкции; применены grill-with-docs, grilling и domain-modeling.
- 2026-10-14: подтверждены пять точных сетей, независимые процессы, команда истории N–M.
- 2026-10-14 / D16: пользователь ограничил текущий этап публикацией в NATS. Прежние требования последующего хранения отменены; исторический ADR-0004 помечен superseded. В плане сняты десять задач без переиспользования ID; добавлены E00-10/11, E03-11 и E07-09. Новая точка — R07.

- 2026-10-15 / D17–D20: приняты строгие типы, части крупных транзакций и независимые контуры до общего NATS. Вне-транзакционные балансы optional; history/YAML отложены. Добавлены E00-12, E01-05, E02-10; E04-02 теперь OPTIONAL, E00-09 DEFERRED_BY_USER.

- 2026-10-15 / E00-13: исследованы пять сетей и Classic candidate; созданы CLIENT_MATRIX и три research-файла. Обновлены ТЗ (включая отдельный Polygon profile), архитектура, план и решения. Следующий шаг E00-14/15. Последующая обработка после NATS по-прежнему вне scope.

## Правила продолжения

Перед существенной задачей записать ID и статус. Ответ пользователя сначала фиксировать в DECISIONS, затем обновлять ТЗ/план. После результата сохранять артефакты, проверки и точное следующее действие. DONE требует результата и предусмотренной проверки.

После обрыва сначала прочитать этот файл, затем проверить фактические файлы. Исторические проверки внешних сервисов перепроверять. При первоначальном осмотре исходного кода и git-репозитория не было; это исторический факт, а не текущая проверка. Локальные документы защищают от потери контекста сессии, но не заменяют резервирование диска.
