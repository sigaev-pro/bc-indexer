# Local native import slice

This is the first partial output of E01-01.harness. Its public seam is a CLI
request that admits immutable inputs, imports prepared blocks with the accepted
Geth binary, and compares offline native observations with a separately authored
oracle. It does not require an undocumented running node.

The slice covers a local value transfer followed by an empty block. It does not
complete the `quick` suite, a public network profile, capture, consensus, delivery,
or any lifecycle acceptance case. All seven public profile results remain
`NOT_RUN`. Individual local scenario results do not close AC-CAP-01-01, whose
owner is E01-02.lifecycle_seams.

## Public seam and trust boundary

`python3 -I test/harness/run.py check REQUEST.json` validates inputs without
creating state or invoking Docker. `run REQUEST.json --output NEW_DIRECTORY`
performs a measured run in a fresh owned directory. The caller supplies the
accepted BuildManifest path and exact SHA256; the harness checks its selected
Geth binary, source, runtime image, and profile association. Supplying a digest
does not independently establish that a build has been accepted.

The versioned local request and proof are separate from CaseResult and
ProfileManifest. Artifact references hash raw bytes, including binary and RLP
files. Public `ethereum-mainnet` build association is separate from private
fixture chain ID, genesis hash, and the digest of the complete local fork config.
No custom genesis claims the public chain identity or ENV_READY.

Preparation is separate from the measured run. Pinned Geth code generates input
blocks; those block hashes and state roots are input bindings, not an independent
semantic oracle. A human-readable, independently worked oracle fixes balances,
nonces, gas, transaction counts, and fees before native import. Both block states
and the complete exported chain must match, so a valid prefix cannot pass.

The measured deadline includes admission, initialization, import, observation,
comparison, and owned cleanup. It is finite and at most 1800 seconds. Native
commands have smaller finite caps. Containers have no network, bounded CPU,
memory and PIDs, a read-only root, read-only admitted inputs, and a unique owned
writable state directory. The importer never uses `--nocompaction`, which can
mask native import errors in this pinned client.

Each attempt has a new output identity. Failures and their logs are retained.
An admission-tamper rejection differs from a native-invalid-block test: the latter
must explicitly authorize the new corrupt input digest before dispatch. No
retry changes an earlier result or silently creates a successful suite result.

## Run it

Use Python 3.12 or newer on Linux or macOS, Docker with the accepted Linux/amd64
compiler and Debian runtime images already present, and an accepted actual-build
artifact directory. Preparation authenticates the selected source archive and
all retained Geth module downloads, extracts a new source tree, and runs native
module verification against the read-only cache. It uses separate writable Go
metadata and a fresh build cache. It performs no network requests or image pulls.

```sh
python3 -I test/harness/prepare.py \
  --build-manifest "$BUILD_ARTIFACTS/manifest.json" \
  --sha256 "$ACCEPTED_BUILD_MANIFEST_SHA256" \
  --output "$HARNESS_PREPARATION"
python3 -I test/harness/run.py check "$HARNESS_PREPARATION/request.json"
python3 -I test/harness/run.py run "$HARNESS_PREPARATION/request.json" \
  --output "$HARNESS_RUN"
```

All paths above must be absolute, with no symlink ancestors. Output directories
must be new and outside admitted build/fixture storage. Accepted inputs stay
unchanged. Preparation uses at most 900 seconds, 2 CPUs and 4 GiB. The default
measured run uses 300 seconds, 2 CPUs and 768 MiB per command. Each native command
has a 120-second cap clipped to the shared run deadline. A 35-second reserve
covers process/container cleanup. The request can lower or raise the total bound
up to 1800 seconds; insufficient remaining time rejects dispatch.

For explicit real controls, run:

```sh
python3 -I test/harness/exercise.py "$HARNESS_PREPARATION/request.json" \
  --output "$HARNESS_CONTROLS"
```

This separately records a positive import, a newly authorized truncated RLP input
that native import must reject, a wrong independent balance, and independently
reauthorized false genesis/block hashes that comparison must reject. Changed
binary/genesis/RLP bytes must reject before native state creation. Real container
timeout and parent SIGTERM/SIGKILL controls test both individual-parent and whole
parent-process-group termination with `/bin/sleep`; they are not Ethereum scenarios.
Expected negative native attempts retain `result: FAIL`; only the control's
expectation gets `control_result: PASS`. The control campaign has a separate
900-second bound and never becomes a quick-suite result.

Portable tests run with `python3 -B -m unittest discover -s test/harness` and in
bootstrap CI. They use explicit synthetic admission data and a fake Docker
executable at the process boundary. They do not claim native runtime evidence.

## Input and proof records

The closed request has `schema_version: 1`, `build_manifest`,
`profile_association`, `fixture`, and integer `deadline_seconds`. A raw artifact
reference has exactly `path`, lowercase hexadecimal `sha256`, and integer
`size_bytes`. Duplicate JSON keys, unknown request/fixture fields, symlinks, and
changed bytes fail admission. The fixture binds genesis JSON, import RLP, full
chain RLP, generator source, independent expected JSON, private identity, and two
prepared block identities. The config digest uses sorted compact JSON over the
complete native config. The fixed fixture uses chain ID 31337 and historical
London proof-of-work rules; it makes no claim about current public fork heads.

Admission checks the opened regular file's actual size before reading. Reads and
copies consume at most the admitted byte count, check for growth, and share the
run's original monotonic deadline. A later file or source snapshot cannot reset
that budget. Static oversize rejects before copying. Copied input uses an
exclusive destination; a failed bounded proof read also fences later dispatch.

`proof.json` records the local run identity, source snapshots, admitted and copied
input references, selected build/profile, actual fixture identity, both observed
states, native header observations, complete exported chain,
command/stdout/stderr/cleanup records, measured
duration, scenario results, and remaining coverage. `run_id` is an evidence
identity; `native_owner_nonce` identifies its owned Docker commands. These are
distinct fields, never interchangeable. The source snapshots keep executed Python
bytes available even after a later checkout changes. Native block hashes/roots
bind input; balances/nonces/gas/fee expectations come from
[the independent oracle](fixtures/ORACLE.md).

Six native commands initialize/import, dump heights 0/1/2, and export the complete
chain. Each dump uses pinned Geth JSON logging. Exactly one `State dump configured`
event must report the expected height and full `header.Hash().Hex()` value. The
pinned native JSON handler renders `header.Number` as a canonical decimal string,
so the accepted heights are exactly `"0"`, `"1"`, and `"2"`. Missing, ambiguous,
malformed, differently typed, or mismatched identity rejects PASS. The raw stderr
observations and parser source bytes are retained. This binds the reported genesis
and both block hashes to actual stored native headers without a custom hash
implementation. The genesis dump is an identity observation, not a third scenario.

The guardian runs in its own session so parent-group termination cannot kill the
cleanup owner. It keeps the native child group leader unreaped with `waitid(WNOWAIT)`
until termination. This covers a leader that exits before its descendants. It
then reaps the leader, proves process-group absence, and separately verifies that
its exact named container is absent. It never signals a reaped PID. A parent
pipe remains open through normal execution; parent loss causes cleanup. Failed
or uncertain cleanup fences all later commands. Process visibility is required;
an unavailable absence check cannot produce a cleanup PASS.

Preparation writes `preparation.json`, including generator binary/source, retained
orchestration bytes, oracle-before-generation, command ownership, and separate
preparation duration. A corrected preparation may use `--previous-attempt` and
`--correction`; the referenced earlier FAIL remains intact. Failed preparation is
not a native runtime CaseResult. There is no implicit retry in the measured runner.

The pure comparison boundary is `compare_state(observed, expected, state_root)`
and `compare(admitted, dumps, exported)` in `compare.py`. It validates literal
expectations, requires every expected address, compares the full raw chain, and
binds native header identity through the admitted record's `header_observations`.
Record validation and artifact hashes provide reproducibility and internal
consistency. Independent review and an observed real invocation establish the
execution claim; a caller-created JSON record alone does not.

## Private BSC Parlia adapter (in progress)

The separate BSC entry point uses the accepted BSC binary and its own isolated
source/module graph. `prepare_bsc.py --build-manifest MANIFEST --sha256 SHA
--output NEW` has a finite 1800-second preparation budget. It copies the pinned
Truffle genesis, preserves all system code, adds the test sender, freezes the
independent [BSC oracle](fixtures/bsc/ORACLE.md), compiles the BSC-only helper,
verifies the public validator identity, and prepares two genuinely sealed Parlia
blocks. No expected result is generated by that helper. Preparation remains
separate from the measured import run.

Preparation emits `observer-tool.json` separately from `fixture.json` and
`request.json`. A trusted caller must review and pin the tool manifest SHA256,
just as it pins accepted build artifacts. The tool manifest binds the accepted
BuildManifest, native source archive/commit, complete BSC module closure digest,
compiler image, source, Go metadata, exact offline command/result/log references,
public validator and executable bytes. A self-authored manifest is not proof of
trusted compilation. Scenario authors cannot select a different executable by
rehashing their fixture. The runtime does not derive or accept the external tool
pin from scenario data.

Authenticated JSON is parsed from the exact bounded byte buffer whose size and
digest were verified. Replacing a file between checking and parsing cannot
change the admitted tool, build, fixture or oracle. This same primitive protects
the existing Ethereum JSON admission path without changing its public schema.
Before PASS, both runners revalidate the original request, build, fixture and
scenario references; BSC also revalidates its pinned tool and provenance refs.
Post-capture replacement therefore retains a failed run rather than a PASS whose
authority references no longer match their recorded bytes. Relative build
artifact roots remain attached to the original BuildManifest location.

Use `python3 -B test/harness/bsc_run.py check REQUEST --observer-tool TOOL
--observer-tool-sha256 TRUSTED_SHA` for read-only admission. Add `run` instead of
`check` and `--output NEW` for the bounded measured run. Admission, raw input
copies, native import, comparison and cleanup share the request deadline of at
most 1800 seconds. Missing tool authority or mismatched bytes reject before any
output state or native dispatch. Accepted Ethereum interfaces remain unchanged.

The BSC runtime uses the same owned command guardian and six native CLI commands
as the Ethereum slice, followed by the pinned helper opening the imported BSC
database read-only. This observer only supports the explicit fresh height-0/1/2
hot-store fixture. It uses native readonly KV and the native no-freezer wrapper;
missing, pruned or freezer-only records reject. The existing empty KV LOCK has
one writable bind exception for native coordination, with bytes verified before
and after observation. All DB data remains mounted read-only. It does not start
a node service. Native JSON dump headers at
heights 0, 1 and 2 bind the private genesis and both blocks; the complete native
export must match the admitted chain. The observer reads native canonical blocks
and derived receipts from that actual database, including native body and receipt
root checks. Its block/config/transaction/
receipt observations are joined to those verified identities before comparison.
The independent oracle checks user balances/nonces/gas, system-call order and
values, validator nonce, and the empty block's unchanged state. Contract-internal
storage/log semantics and exact system gas remain unchecked.

The first BSC preparation retained a FAIL when helper compilation filled the
container's small `/tmp`. The corrected BSC-only command uses an owned
`/work/build-temp` through `GOTMPDIR` and `TMPDIR`; global command limits are
unchanged. Preparation accepts `--previous-attempt PREVIOUS_PROOF --correction
DESCRIPTION` to retain that history under a new identity. A successful previous
preparation requires an explicitly changed helper source before a follow-up;
its PASS remains intact and the attempt counter increments. Authentic Parlia
preparation has passed, but measured runs retained observer-lock/freezer failures
after six successful native CLI commands. The hot-only observer now has a new
successful preparation/tool pin and measured positive run; the retained evidence
records their exact source and proof identities.
No complete quick-suite or public-profile result is claimed.

Run explicit controls with `python3 -B test/harness/bsc_exercise.py REQUEST
--observer-tool TOOL --observer-tool-sha256 TRUSTED_SHA --output NEW`. The driver
has a finite 900-second total budget and retains every measured negative as FAIL
while separately reporting whether its expected rejection occurred. It includes
authorized corrupt and prefix-only RLP, wrong expectations/header identities,
raw/config/binary/tool authority rejection, incomplete real hot DB, timeout and
parent/process-group loss. Missing-state/receipt controls mutate copies of actual
retained observations and are labeled artifact-comparison checks, not new native
execution. The deliberately invalid build negative receives its own explicit
invalid authority solely to reach binary-byte validation; it is never a positive
build or an override of the original observer pin. Portable CI does not execute
this driver or launch native resources.
Corrupt/prefix-chain controls require the exact native command stage, exit and
diagnostic, so an unrelated launch/init failure cannot pass them. Each resource
dispatch reserves startup/termination/cleanup time and passes the same earlier
campaign bound to its child; expiry stops new work while owned cleanup finishes.
The retained control proof includes shared and per-dispatch monotonic budgets.
Before reporting PASS, the campaign rechecks the original request, build,
fixture, observer authority and their required bytes under its remaining total
budget. Individual native runs retain their own shorter deadlines.

## Offline RunPlan resolution

The versioned [RunPlan schema](run-plan.schema.json) freezes declarations before
execution. It is a test-only input contract, not a production YAML extension or
a scheduler. The public commands are:

```sh
python3 -I test/harness/run_plan.py check /absolute/plan.json \
  --registry-bundle /absolute/registry.json --registry-sha256 TRUSTED_RAW_SHA256
python3 -I test/harness/run_plan.py freeze /absolute/plan.json \
  --registry-bundle /absolute/registry.json --registry-sha256 TRUSTED_RAW_SHA256 \
  --output /absolute/new-output-directory
```

The caller supplies the trusted raw-byte digest of an existing v3
`evidence-artifacts-v1` / `CaseRegistry` validation bundle. Its document and
context use the accepted [evidence interface](../evidence/README.md); this block
does not create a second case registry or trust a digest inside the plan itself.
Accepted applicability rules belong to that independently selected bundle.
Synthetic bundles remain suitable for schema tests, not runtime credit.

The closed plan selects profiles, exact case identities, suite, fixture kind,
standard/extended mode and seed. Fixture bindings fix profile/fork/version,
actual chain/genesis/config identity, prepared inputs and independent expected
artifact references. Private identity and public correspondence are separate.
Unknown or duplicate cases, unsupported fixture/fork bindings, missing quotas
and unapproved exclusions reject. `PARTIAL` preserves every omitted applicable
cell as NOT_RUN. `COMPLETE` means every applicable cell in the selected registry,
profiles, suite and fixture kind was selected; it does not mean execution passed.

Source start conditions, finite AND/OR stop triggers, action inputs/outcomes,
quotas, allowed faults, resources and volumes are fixed before execution.
Local stop requires both declared actions and native decisions. Public stop uses
duration and/or native decisions with its explicit operator; all controlled
transaction outcomes remain mandatory regardless of that operator. Public
mainnet allows observation only. Each selected public testnet needs controlled
transactions with declared inclusion/execution outcomes and a signer chain ID
matching the actual fixture identity. The validator does not acquire a signer.

One standard parent measured cap is at most 1,800 seconds. Event, comparison,
restart, fault, drain, fence and reader work all belong inside it; per-event and
comparison caps are ceilings inside that budget, not additional time afterward.
Preparation/startup happen before READY; teardown has a separate finite cap.
Extended duration requires an explicit soak suite and never becomes a standard
30-minute claim. Resources/volumes are declarations, not measured availability.

Local-e2e and public plans need an exact implementation/source/build/configuration/
topology fence declaration and prior conformance binding. Missing declarations
remain explicit blockers; inconsistent bindings reject. Even a matching supplied
artifact is opaque and unverified here. Native-seam diagnostics do not require
NATS. Full ProfileManifest verification, native/supporting readiness, isolated
runtime ownership, fixture/build equivalence, independent oracle review, actual
resource checks and terminal-fence conformance remain separate work.

Exit 0 means offline resolution succeeded. The report always has
`runtime_admitted: false`; every case remains NOT_RUN. It never emits a READY
state, CaseResult or RunManifest. Exit 2 means invalid declarations, changed
artifacts or another validation failure. `check` creates no output.

`freeze` copies the exact authenticated plan, registry and schema buffers and
writes `resolution.json` last. Original authority and referenced bytes are
rechecked before that final record. A failure during publication may retain
incomplete snapshots, without a resolution record; those are not a frozen plan.
Relative artifact paths resolve against the original plan directory recorded in
the report, not the snapshot directory. Absolute paths are allowed; traversal,
symlinks and nonregular files reject. External large payloads are not copied.
Later execution must revalidate every original reference and obtain the missing
readiness proofs; a frozen hash alone does not verify their semantics.

Resolution checks a 30-second validation budget, 16 MiB per JSON authority and
1 GiB per referenced artifact using bounded reads. These are initial offline
validator limits, not product payload or runtime throughput limits. All authored
numeric fields use the accepted exact JSON parsing boundary; fractional values
cannot silently round into integer seeds, counts or deadlines.

## Remaining work in issue 12

The first Bor checkpoint adds a [fixed private oracle](fixtures/bor/ORACLE.md)
and portable comparison seam in `bor.py`. It distinguishes header coinbase from
native author, base-fee routing from tip routing, and the two exact Bor logs.
The implementation now includes `prepare_bor.py --build-manifest PATH --sha256
SHA --output NEW` and `bor_run.py check|run REQUEST --observer-tool TOOL
--observer-tool-sha256 TRUSTED_SHA --output NEW`. Preparation has a separate
1,800-second bound and uses only the accepted isolated Bor graph. Its helper
verifies the tracked public signer and bounded native IPC shutdown before
genuine Prepare/Authorize/Seal/InsertChain generation. The external tool pin is
supplied by the trusted caller, outside mutable scenario input.

The measured path owns one container containing the accepted `bor server` and
its local IPC helper. It requires fresh genesis/head 0, exact node chain config
and contract validator identity before `admin_importChain`, then full raw chain,
native body receipts, authors and complete small-fixture state dumps. Raw IPC
bytes and server logs have fixed limits; the operation deadline also stops an
import that outlives its RPC call. Normal success requires an actual server exit
of zero without forced cleanup. Kill remains a bounded failure-cleanup fallback.
`dumpconfig` proves parsed execution flags using explicit `=false` values; its
intentional chain/datadir neutralization is covered by actual launch arguments,
genesis/head 0 and `admin_nodeInfo`, not treated as the launched path values.

The corrected native preparation and one measured server/IPC import passed on
source commit `6a5fc4996374bc3cc10597a0b5edd8c6741536eb`; earlier failed
preparations remain recorded in the task evidence. This is a private native seam,
not a complete quick suite or a public-profile result.

`bor_exercise.py REQUEST --observer-tool TOOL --observer-tool-sha256 TRUSTED_SHA
--output NEW` runs the explicit 19-control campaign within one 900-second bound.
It retains reauthorized corrupt/prefix inputs, altered oracle/identity failures,
admission tampering, and three clearly labeled retained-observation comparisons.
Corrupt/prefix success requires exact IPC request/response identity and the
intended native failure stage. Five resource controls start the real accepted
Bor server in a fresh private data directory, retain its exact owned IPC-ready
event from a bounded live log prefix, confirm it is still running, then test deadline or parent/process-group
termination. Each dispatch reserves execution and cleanup time under the same
campaign deadline. Final success revalidates the original authority artifacts.
The helper and its caller-pinned preparation can be reused when only this Python
campaign changes. Its first execution retained 14 successful controls before a
readiness-log buffering failure. The corrected owner flushes bounded writes;
the live poll permits appends while final artifact hashing remains strict.
The corrected campaign passed all 19 controls in 60.287 seconds on source
`b25215c7c0152a984ad7157af598a3e37c4cce3c`. Fresh Ethereum/BSC compatibility
positives also passed using accepted prepared inputs. Exact final-head review,
CI and protected acceptance remain separate from these measured results.

Complete BSC fixture/fork coverage, the Bor native adapter, all other required
fixture families, complete ProfileManifest verification and runtime RunPlan admission,
supporting services, public mainnet/testnet smoke, and integration with future
capture and Gateway components remain unimplemented here. The collector and NATS
are absent by design at this first native seam. E03 delivery and later composed
gates will add their assigned runtime requirements. The accepted corpus and
product contracts are unchanged.
