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

## Unchanged-image observation predicates

[nats_image_checks.py](nats_image_checks.py) is an early offline predicate over
supplied observations. It validates a closed plan, exact binary publication and
ACK bytes, one declared lost-ACK/retry pair, process generation/Wait ordering,
original store identities and fixed restart permissions. Its
[expectations](fixtures/nats-image/ORACLE.md) are fixed before native execution.

```sh
python3 -I test/harness/nats_image_checks.py /absolute/observations.json \
  --sha256 EXTERNALLY_PINNED_OBSERVATION_SHA256
```

The input document contains exactly `schema_version: 1`,
`kind: nats-image-supplied-observations`, `plan`, `topology`, `census`,
`boundaries` and `processes`; the portable tests provide complete synthetic
examples. The checker reads and hashes one bounded buffer and creates no files.
Its `observation_valid` result establishes internal consistency only and always
retains `runtime_admitted: false` with explicit missing evidence. It does not
start a native process or authenticate an executable/tool from supplied fields.

The later [nats_image.py](nats_image.py) source checkpoint adds preparation and
measurement around the unchanged accepted executable. Its static, standard
library Go coordinator has portable tests. The first three measured attempts
remain FAIL: the first stopped at the protocol 1 handshake;
the second captured all five publication ACKs, then rejected a publisher ACK
on the manager connection because both subscribed to the same wildcard inbox.
The third completed standalone native recovery/export, but independent comparison
rejected API-only metadata absent from JSz. Its overall proof remains FAIL;
R3 stayed NOT_RUN. Read-only replay after the scoped comparison repair is a
diagnostic, not a replacement result. Fresh preparation 04 of the repaired
coordinator passed, and measured attempt 04, linked to FAIL03, passed both
standalone and R3 with five stored and exported messages each. That result
covers only this private fence block, not runtime admission. The independent
[collection](nats_image_collection.py) and [raw-byte join](nats_image_proof.py)
checks extend the supplied predicate; they cannot establish native truth alone.
Issue #12 stays open and `runtime_admitted` remains false.

Subscriptions use `_INBOX.IMAGE.<role>.C<ordinal>.>` for each connection;
each role's ACL permits only its own inbox namespace. The census records the
publisher inbox explicitly. Management, publication, reader and denied-publish
replies retain exact correlation; an unexpected message fails instead of being
discarded. Every raw connection is joined to its own process/configuration,
including typed INFO server name, port, protocol and payload limit. Standalone
Raft absence accepts only the pinned native 404 body `No Raft nodes registered`;
generic errors and absent R3 groups reject.

Native API configurations require exactly `_nats.req.level: "0"`,
`_nats.level: "5"` and `_nats.ver: "2.15.2"`; JSz requires only the first field.
The checker validates these exact metadata shapes/types before comparing all
remaining fields without coercion. Raw API and monitoring responses stay intact.
Before publication, a 60-second sub-budget of the measured parent deadline
waits for the expected empty stream/configuration/group on every local voter.
Only a valid zero-stream APP inventory is pending; malformed, foreign or changed
evidence fails immediately. Every sample remains in the HTTP log, and only the
final complete collection becomes `initial_inventory`. This readiness control
does not weaken the later full fence or represent an observed R3 failure.

```sh
python3 -I test/harness/nats_image.py prepare /absolute/BuildManifest.json \
  --sha256 ACCEPTED_BUILD_SHA256 \
  --source-conformance /absolute/accepted-source-conformance.json \
  --source-sha256 ACCEPTED_SOURCE_PROOF_SHA256 --output /absolute/new-preparation
python3 -I test/harness/nats_image.py run /absolute/new-preparation/tool.json \
  --sha256 EXTERNALLY_ACCEPTED_TOOL_SHA256 --plan /absolute/image-plan.json \
  --plan-sha256 EXTERNALLY_PINNED_PLAN_SHA256 --output /absolute/new-run
```

The closed plan is `{"schema_version":1,"messages":3,"payload_bytes":32,
"measured_seconds":900,"topologies":["standalone","R3"]}`. Volumes are bounded
and configurable; both topologies are required. The externally pinned tool is
separate from scenario inputs. Preparation authenticates the accepted image,
native executable, source-native proof and its five cases. It compiles only our
coordinator with the accepted offline compiler; native NATS is not rebuilt.
The image run checks its actual executable bytes before starting native children.

The source and image configurations are explicitly different: embedded `NoSigs`
servers with `$G`/`$SYS` versus CLI processes with `APP`/`SYS`, exact client ACLs,
names, ports and original-store restart. Both cover ordinary PUB, FileStorage,
Limits retention and default persistence without batching, consumers, sources,
mirrors, schedules, transforms or direct reads. Unsupported settings reject.
Each topology uses one owned network-isolated container; native processes have
separate PID/start identities, INFO IDs, configurations, store identities and
Wait/exit records. Simultaneously live voters require distinct PIDs; reuse after
the old process exited is allowed only with a new start identity.

The socket audit retains actual ingress and replies before the driver suppresses
one ACK from the application's real timed wait. Its explicit retry is another
publication and append. There is no hidden reconnect or library retry. The
selected path rejects unexpected extra protocol frames rather than inventing
attempt correlation. It consumes the pinned protocol 1 handshake exactly:
initial INFO, PONG, then `connect_info: true` INFO bound to the same typed server
identity and the APP account. It does not discard arbitrary INFO frames while
waiting for a publication response. All original publisher/manager connections
close before old children are stopped. Every old Wait finishes before restart. Before-store
snapshots are immutable copies taken only after exit; restart opens the original
stores, and final store bytes are referenced only after new children exit.
The driver captures each snapshot's complete relative file inventory, exact
sizes and SHA256 values before restart. Final validation checks the same complete
path set and bytes; a directory's continued existence is insufficient.

Before the fenced API request, the driver waits until every voter reports the
same nonempty metadata and stream leader, exactly one voter is LEADER and the
others are FOLLOWER. Healthy restarted voters can briefly report no stream
leader, and a stream API request in that window has no responder.

Restart users can publish only exact stream INFO and sequence GET API subjects.
A real denied data publication checks that restriction. Full health, JSz and
explicit APP/SYS/$G Raft inventories enumerate metadata and stream groups, with
all three native member IDs and distinct logical/transport accounts. Inventory
rows and explicit samples must agree. Complete before/after collections bracket
the upper sequence selection and each direct sequence read. Raw native API
frames, stored binary bytes, timestamps and interval identities are joined to
the census. Every retained attempt request/response must match its census row;
all HTTP request orders are unique, including records outside the final selected
brackets. Initial and fenced configuration joins preserve JSON types. No consumer
is created to read the interval.

One monotonic parent budget covers admission, both topologies, faults, restart,
comparison, evidence hashing and cleanup; it never exceeds 1,800 seconds.
Preparation has its own 600-second limit. Native command caps are clipped to
the parent budget and retain the existing owned-command cleanup reserve. Failed
attempts remain failed, with commands, partial wire bytes and available native
artifacts retained. A corrected retry needs `--previous`, its explicit
`--previous-sha256`, and `--correction`; it uses a fresh output directory and
authenticates the old build/tool binding. The predecessor attempt index must be
an integer in `1 <= attempt_index < 2**63`; this is a representation bound, not
a repair-cycle ceiling. These are private harness artifacts,
not public profile or product delivery evidence.

## Source-native NATS conformance

[nats_conformance.py](nats_conformance.py) prepares an in-package test executable
from the accepted NATS source and its isolated offline module closure. Its five
fixed cases are described in the independently fixed
[oracle](fixtures/nats/ORACLE.md). Standalone coverage is the storage/seal barrier;
the pre-proposal, delayed-apply, lost-acknowledgment and leader-change cases use
three voters. This is source-native conformance, not unchanged-image recovery or
a complete terminal fence. Standalone acknowledgment loss remains a later
requirement. Issue #12 and all public profile/runtime admission gates stay open.

```sh
python3 -I test/harness/nats_conformance.py prepare /absolute/BuildManifest.json \
  --sha256 ACCEPTED_MANIFEST_SHA256 --output /absolute/new-preparation
python3 -I test/harness/nats_conformance.py run /absolute/new-preparation/tool.json \
  --sha256 EXTERNALLY_ACCEPTED_TOOL_SHA256 --output /absolute/new-run
```

The caller pins the accepted BuildManifest, then separately accepts and pins the
prepared tool manifest. A matching self-declared compiler/source hash is not
tool approval. Preparation copies the authenticated source archive, records and
excludes only `server/*_test.go`, and adds this repository's overlay. Every other
production input, including non-Go assets, `go.mod` and `go.sum`, is hashed before
and after compilation. The pinned compiler, offline module cache, exact commands,
excluded inventory, oracle, helper bytes and resulting executable are bound in
the tool manifest. The original source and module cache remain read-only.
Removing upstream test files prevents their package initializers from changing
native election, reconnect and tracing defaults. Native constructors and natural
metadata election are used; the explicit harness option `NoSigs=true` leaves
signal ownership with the existing owned test process.

Preparation has a separate 1,800-second budget. The measured run has one shared
1,800-second budget including comparison and cleanup, with finite child limits.
Each case runs in an owned offline container, retains its configuration, native
identities, raw observations, stores, command output and cleanup evidence, and
must produce the exact selected Go test and checked case report. In-process
server shutdown is not proof of old native OS-process exit. Raftz records bind
the actual native group/account mapping; the stream traffic account is retained
separately. The supplied one-group predicate does not prove a full group census.

Output paths must be new. A corrected attempt uses `--previous` with the prior
failed preparation or run proof and a nonempty `--correction`; prior evidence is
preserved. Preparation retries keep the same accepted manifest binding. A changed
overlay requires fresh preparation and a new externally pinned tool. A corrected
measured run may link that new tool to a previous failed run only after both tool
artifacts authenticate the identical accepted build, source archive and module
closure. Its proof retains the old/new tool pins and increments the old attempt
ordinal. Missing or modified predecessor artifacts reject. Failures remain
failures. The source checkpoint's portable tests use
synthetic reports and temporary files only: native preparation, compilation and
execution require separate actual evidence. Corrected source `125439f` prepared
successfully in 56.275 seconds, then passed the five declared cases in 9.404
seconds. The first measured run remains FAIL because its delayed-apply collection
was selected too early; attempt 2 preserves that failure and both tool pins.
See the [retained harness evidence](../../.project/evidence/E01-01.harness.md) for
exact identities and cleanup. These results do not complete standalone
acknowledgment-loss coverage, unchanged-image recovery or runtime admission.

## NATS input inspection and frontier predicate

This first NATS checkpoint is read-only. It prepares no server, invokes no
container and grants no runtime admission:

```sh
python3 -I test/harness/nats_inputs.py /absolute/accepted/manifest.json \
  --manifest-sha256 CALLER_PINNED_ACCEPTED_BUILD_SHA256
```

The caller must supply an already accepted actual BuildManifest. A caller pin
authenticates bytes; it does not replace build acceptance. Inspection checks the
supported NATS source archive and native module closure, including its own
nats.go v1.53.1 test client, plus the exact Go compiler and NATS image artifacts.
It authenticates the selected linux/amd64 image index, manifest and config, every
ordered uncompressed archive layer, and the effective NATS executable. Later
replacement, whiteout or a linked/special executable ancestor rejects. This is
a limited image check, not a general image filesystem implementation.

The supported lock is NATS v2.15.2 at source
`5a815f11a911efa2c88fd1dbfb18ed89a449bfc1`, Go1.27.3 and the accepted exact
image/executable hashes in [nats_inputs.py](nats_inputs.py). Inline Go metadata
must bind that source revision, unmodified source, module, compiler and platform.
Changing the lock requires a reviewed update and new conformance evidence.
Original source trees are not used as writable preparation directories.
The inspector rechecks authenticated references before returning; later
preparation must revalidate them again. Reads have a 120-second aggregate limit,
128 MiB per in-memory archive and 1 GiB for streamed compiler-artifact hashing.
Exit 0 means these input checks succeeded; exit 2 means rejection. The report
always records `runtime_admitted: false`, `source_conformance: NOT_RUN` and
`process_fence: NOT_RUN`.

`nats_fence.validate_frontier(binding, before, after)` is only a predicate over
supplied raw filtered Raftz responses for one expected three-voter group. It
checks independently derived server-name/member-ID bindings, exact account/
group/voter coverage, peer membership, native roles, leader/term/log term,
`pindex == committed == applied`, health and empty queues in both brackets.
Legitimately omitted false health flags follow the pinned native source;
required progress/queue fields cannot be omitted or invented. Diagnostic
timestamps and WAL retention counts are not the Raft frontier.

Neither synthetic rows nor this predicate establish recovery or a complete
fence. A later authenticated collector must freeze the complete relevant
metadata/stream-group set, retain all raw responses and bracket the full
collection, U and direct sequence export. It must bind actual process exits,
fresh process generations with stable names/stores/membership, exact operating
to sealed/restricted configurations, and an exhaustive framed ingress census
with responses observed before reply loss. This input predicate's own
source-conformance fields remain NOT_RUN; it does not execute those cases.
The separate five-case source-native proof was accepted in PR #127. Unchanged-image
standalone/R3 fence attempt 04 passed after three retained failed attempts.
Run 05 of the review repair failed because the driver did not wait for a
restarted stream leader. Attempt 06 with that repair passed both topologies;
its acceptance is pending review. The offline
RunPlan does not treat this inspection as
verified conformance. Issue #12 remains open.

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
NATS. Offline resolution itself checks none of the runtime facts. For local
native-seam plans the [local runner](#local-native-seam-runner-d96) adds test-only
ProfileManifest verification, resource checks and owned execution. Case-level
oracle acceptance by each AC owner, public ProfileManifests and terminal-fence
conformance for local-e2e plans remain separate work.

Exit 0 means offline resolution succeeded. The report always has
`runtime_admitted: false`; every case remains NOT_RUN. It never emits a READY
state, CaseResult or RunManifest. Exit 2 means invalid declarations, changed
artifacts or another validation failure. `check` creates no output.

`freeze` requires the caller to have exclusive writer ownership of the output
parent and directory through publication and later consumption/revalidation.
This is a precondition, not a lock that the tool acquires. Do not run another
writer or allow a same-user process to replace these paths while using the
result. The descriptor, inode and hash checks detect mutations under that
precondition; they do not isolate an adversarial concurrent writer.

The tool checks the newly created directory's captured identity and rejects
nonempty contents before writing. This preserves an unrelated regular directory
substituted with existing files, as well as detectable identity substitutions.
It copies the exact authenticated plan, registry and schema buffers through
exclusive writes relative to a held, no-follow directory descriptor. It checks
the directory identity, snapshot inodes and bytes, original authority references
and remaining validation time before publishing `resolution.json`. The resolution
is prepared in a bounded private file, closed and fsynced before the final
exclusive atomic link. An existing marker is never replaced. A rejected attempt
may retain snapshots and one private `.pending` file, but it does not publish a
new resolution marker. Unrelated replacement files/directories are preserved.
Only `resolution.json` is the publication marker; private prepared files are not
a completed freeze.

The ownership precondition matters even when every check passes. An empty
directory substituted before the initial identity capture is indistinguishable
from the created directory. Controlled review probes also replaced the pending
pathname immediately before the final link, publishing replacement bytes, or
moved the output at that boundary, publishing only into the held moved directory.
These are demonstrated limits when ownership is violated, not prevented attacks.
Later consumers must revalidate the marker and all referenced bytes at the
expected paths under the same ownership precondition; a prior success does not
establish their current integrity.

The final link is the publication boundary. No fallible cleanup afterward can
turn published success into a reported failure. This offline artifact is not a
power-loss durability protocol: directory publication across a host crash needs
inspection and later revalidation. Do not reuse this contract for WAL or delivery
evidence. The bounded prepared file may remain to avoid unsafe cleanup of a
possibly replaced path.

The frozen schema is a byte-bound snapshot of the reviewed repository contract,
not a standalone validation bundle. It depends on the repository's reviewed
`test/evidence/evidence-definitions.schema.json`, other evidence schemas and
`test/evidence/validate.py` evaluator. Use that reviewed repository version when
resolving the plan; copying the schema alone does not copy its dependencies.
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

## Local native-seam runner (D96)

D96 closes issue #12 on private local native-seam fixtures for the three
families. The test-only [runner](runner.py) is the single entry point for a
frozen local native-seam plan:

```sh
python3 -I test/harness/profile_manifest.py /abs/manifest.json --sha256 PIN
python3 -I test/harness/run_plan.py freeze /abs/plan.json --registry-bundle /abs/registry.json \
  --registry-sha256 PIN --output /abs/frozen
python3 -I test/harness/runner.py admit|run /abs/frozen --resolution-sha256 PIN --output /abs/new-run \
  [--previous-run /abs/first/run-record.json --previous-sha256 PIN --retry-reason TEXT]
```

A [test-only ProfileManifest](profile-manifest.schema.json) binds one private
fixture to its accepted family preparation: profile and family, fixture identity,
test-only chain ID, genesis hash and native config digest, build, preparation
proof, runner slice, request, observer tool, genesis and block inputs, and the
independent oracle. [profile_manifest.py](profile_manifest.py) re-hashes every
reference and checks each binding against the preparation, request and fixture
files. BSC and Bor preparations must record complete cleanup; the accepted Geth
preparation predates that field. The identity is always `test-only`, the manifest
names no supporting services, and it never gives public-profile credit. It is not
an E06 public ProfileManifest: fork ranges, native type sets, finality and GC
parameters, state backend and ENV_READY identity stay with the public manifests.
Under D96, local supporting services, their test credentials and the Polygon
own-L1 fixture inputs belong to E05-01.polygon_observer.

Admission re-authenticates the frozen plan, registry, schema, resolution and
every artifact within the plan's preparation deadline. It accepts only a standard
native-seam plan without a fence or faults. Every selected profile needs one
verified manifest, one matching test-only fixture and one native-import action
bound to its request and oracle; case identities must use the verified manifest
digest. Host and Docker architecture, CPUs, memory and free disk must meet the
plan, parallelism is 1, and no `extractor.harness` container may exist. Child
request deadlines plus a 30-second grace each must fit the parent measured
budget of at most 1,800 seconds. `admission.json` records `runtime_admitted: true`
for this private scope only. Native readiness is shown by each child PASS, not
by admission.

`run` holds a host-wide single-writer lock for the whole run. It dispatches the
existing family CLIs (`run.py`, `bsc_run.py`, `bor_run.py`) serially, each in its
own process group with stdin closed and stdout/stderr written to files. The
parent kills a child group at the parent deadline or when the runner receives
SIGTERM, SIGINT or SIGHUP. Dispatch stops after the first child FAIL. A child
passes only with a bounded, duplicate-key-free proof object of the matching
kind, PASS, complete cleanup, the admitted request, exit zero and an absent
process group. Teardown always runs, even after an error or signal. It removes
only `extractor.harness` containers whose mounts are inside this run's output or
that carry a recorded child nonce label. Any other harness container, or one
Docker cannot describe, is left in place and fails cleanup, so the run fails.
Once this run has created its output, `run-record.json` is always written; a
refusal before that, including an output that already exists, leaves no record.
The record keeps phase timestamps and plan deadlines for
`preparation` (admission), `execution` (all child imports and their comparisons)
and `teardown`, the child commands, proofs, output tails and results, any error,
and the D83 attempt. A diagnostic retry must link a pinned first FAIL of the
same frozen plan; the runner does not record whether that first FAIL was already
retried, so the operator keeps that single-use rule.

The harness owns no AC verdict and writes no RunManifest (D96). Each selected
case gets a CaseResult with `APPLICABLE` and `NOT_RUN`, validated against the
frozen registry and its owners. Each profile gets a D87 ReaderBoundaryManifest
with `run_started: false`, the pinned plan and oracle bytes, and all six
conditions `NOT_RUN`, because no capture, Gateway or NATS delivery exists at E01.
The first `run_started: true` boundary belongs to E03-11.block_lifecycle.
Private fixtures use the public profile name whose fork rules they follow, with
a test-only manifest digest; a later owning task must not turn such a cell into
public-profile credit. Remaining blockers always include
`private_identity_has_no_public_profile_credit`, `harness_owns_no_ac_verdict`
and `case_oracle_acceptance_belongs_to_ac_owner`. Oracle independence for the
three accepted fixtures was reviewed with their family slices (PRs #122–#124);
accepting an oracle for a case verdict stays with that AC's owner.

## Issue 12 boundary and earlier slice notes

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

Under D96 the remaining items have other owners and are not #12 work:
broader BSC/Bor/Ethereum fixture, fork and corpus coverage belongs to the D90
corpus owners and E01-03.smoke_corpus; Polygon local supporting services, their
test credentials and own-L1 inputs belong to E05-01.polygon_observer; public
ProfileManifests, environments and mainnet/testnet smoke belong to E06 and E07;
the first real-delivery reader boundary belongs to E03-11.block_lifecycle; capture
and Gateway integration belong to their E02–E05 tasks. Within #12 only the Go
`settled()` raftz field-name follow-up from PR #128 remains. The collector and NATS
are absent by design at this first native seam. E03 delivery and later composed
gates will add their assigned runtime requirements. The accepted corpus and
product contracts are unchanged.
