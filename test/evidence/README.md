# Version 3 test evidence

These artifacts implement the resolved evidence format for `E01-01.evidence`.
They are test infrastructure, not a product wire format or a runtime acceptance
claim. Native runtime remains NOT_RUN until an actual run produces valid evidence
and the responsible gate accepts it.

The normative inputs are [EVID](../../.project/docs/IMPLEMENTATION_GATES.md),
[D82 and D83](../../.project/docs/TESTING_CONTRACT.md),
[the D91 validation interface](../../.project/automation/evidence-task-contract.md),
and AC-DEV-01-01, AC-DEV-04-02, and AC-DEV-04-03 in
[ACCEPTANCE_CASES](../../.project/docs/ACCEPTANCE_CASES.md). The accepted
[corpus](../../.project/testing/corpus/README.md) remains the definition source.

## Files and validation

`case-registry.schema.json`, `case-result.schema.json`, and
`run-manifest.schema.json` are standard JSON Schema 2020-12 documents. Shared
definitions live in `evidence-definitions.schema.json`. The composite and stage
plan schemas cover the cross-document validation interface.
`reader-boundary-manifest.schema.json` defines the separate D87 version 1
observation artifact. Keep these local files together when using a separate JSON
Schema tool.

The CLI uses Python's standard library. It needs Python 3.10 or later and installs
no packages. Its small schema evaluator implements only the keywords used by
these files; it rejects unsupported keywords. It is not a general Draft 2020-12
implementation. External schema tools can check structure, but acceptance also
needs the semantic checks in `validate.py`.

Run from the repository root:

```sh
python3 -I test/evidence/validate.py < bundle.json
python3 -I -m unittest discover -s test/evidence -p 'test_*.py'
python3 .project/automation/evidence_task_verifier.py --candidate .
```

The first command reads exactly one JSON bundle. Ordinary validation produces no
stdout. Exit 0 means valid evidence; exit 2 means invalid input, a broken reference,
or an unreadable local schema. Diagnostics are bounded and omit artifact content.
Timeouts, output saturation, and missing files remain verifier failures. The CLI
does not execute the recorded command, access the network, or open any path from
the bundle. References resolve only through supplied context maps. JSON with
duplicate object keys, nonfinite numbers, trailing documents, or invalid UTF-8
is rejected.

```json
{
  "contract": "evidence-artifacts-v1",
  "kind": "CaseResult",
  "document": {},
  "context": {}
}
```

The empty objects above are placeholders for the document and its context, not a
valid result. `kind` accepts `CaseRegistry`, `CaseResult`, `RunManifest`,
`CompositeManifest`, `PrerequisitePlan`, or `ReaderBoundaryManifest`.
The optional `synthetic` label never
changes validity. Unit fixtures and the trusted pack are explicitly synthetic;
their PASS/FAIL strings test representation, not native behavior.

For expansion, send `contract`, `kind: "ExpandProfiles"`, and
`scope: "ALL"`, `"ETH"`, `"BSC"`, or `"POLY"`. The CLI returns a sorted JSON array
on stdout. `profile-registry.json` contains exactly seven concrete profiles. It
contains no invented chain IDs, genesis values, fork configs, or runtime hashes.

## Context and identity

The caller supplies reviewed context:

| Context field | Meaning |
|---|---|
| `registry` | The current resolved CaseRegistry for results and manifests |
| `task_ids` | Distinct allowed task IDs from the accepted task catalog |
| `case_owners` | AC ID to assigned task ID mapping |
| `profiles` | Optional copy of the exact accepted seven-profile map |
| `rules` | Accepted applicability rules, keyed by `rule_ref` |
| `manifests` | Closed RunManifest objects keyed by exact reference path |
| `child_manifests` | Closed child RunManifest objects for a composite |
| `artifacts` | Exact UTF-8 artifact text keyed by reference path |

Registry validation needs the task and owner maps. Excluded cases also need
rules. Run validation needs the registry and all input, expected, actual, and log
artifacts. Prerequisite validation can use an empty context. An absent map is
allowed only when that validation does not need it. Context is an authority input;
the CLI cannot prove that its author obtained a real approval. The coordinator
must bind it to accepted catalog/rule sources and retained artifact bytes.

Every resolved case has eight independent identity axes:
`(ac_id, network, profile, profile_manifest_digest, fork_variant, fixture_kind,
suite, variant_id)`. The task, fixture ID, and fixture version bind the record but
do not replace any identity axis. Duplicate identities are invalid even if they
have different fixture versions or task IDs. Named variants stay separate across
profiles, forks, fixture kinds, and suites.

Runtime families are ETH, BSC, and POLY. Mainnet and testnet are separate profiles.
Resolved document evidence uses `network=DOC`, `profile=DOC`,
`profile_manifest_digest=NOT_APPLICABLE`, `fork_variant=NOT_APPLICABLE`, and
`fixture_kind=document`. COMMON and ALL are definition scopes, never resolved
runtime identities. Fixture kinds are `document`, `native-seam`, `local-e2e`, and
`public`; `harness` and `synthetic` do not introduce fixture kinds. Suites are
`quick`, `functional`, `fault`, `soak`, and `public-smoke`.
Runtime `public-smoke` requires a `public` fixture; a local seam cannot replace it.

Definition cells from the accepted corpus are not runnable registry records.
Unresolved profile/fork/fixture fields remain unresolved inputs until their
owners supply real artifacts. Do not fill required fields with made-up hashes to
pass validation. A new registry variant or rule set requires a new version and
digest. Version 2 evidence remains historical and is rejected as current version
3 evidence.

## Applicability and verdicts

| Applicability | Verdict | Required evidence |
|---|---|---|
| APPLICABLE | NOT_RUN | No run reference, actual output, or execution timestamp |
| APPLICABLE | PASS or FAIL | Full referenced run with this exact individual outcome |
| NOT_APPLICABLE | Absent | Nonempty reason and accepted profile/fork rule |
| NOT_ENABLED | Absent | Nonempty reason and accepted disabled optional-extension rule |
| DEFERRED | Absent | Nonempty reason and accepted scope decision |

An accepted rule contains `accepted: true` and the matching `applicability`.
NOT_ENABLED also requires `optional_extension: true`. Optional identity, task,
and fixture selectors in a rule must match exactly. A `case_identities` selector
is an explicit allowlist. Broad accepted rules are permitted by the handoff
interface; independent review must confirm their real scope. A missing mandatory
native path cannot become optional through a worker-authored reason.

A valid FAIL is honest evidence, not a passing gate. A valid NOT_RUN records
missing execution. UNSTABLE and PARTIAL are attempt/campaign diagnostics, not
new CaseResult verdicts. The final E07 requirement-verdict task still owns complete
matrix reconciliation, retry history, and acceptance. This CLI does not turn
valid JSON into DONE or copy a parent PASS onto its children.

## Run passport and hashes

One RunManifest describes one executed fixture for one network/profile/fork,
fixture kind, and suite. It binds the exact registry version and digest, task and
fixture version, requirement/finding lists, source/patch commits, dependency lock,
compiler/generator versions, image digests, architecture, scenario and seed,
fault boundary, storage/cache model, command, environment versions, producer,
input/expected/actual/log references, comparator, result explanation, phase times,
resource/deadline digest, and optional parent run. Every declared case has exactly
one PASS/FAIL outcome and belongs to that run's registry slot. An empty finding
list is explicit and valid.

Native runs need pinned native commits and compiler versions. Empty image maps
need a nonempty `build_applicability.reason` and `rule_ref`, for example a reviewed
direct-binary fixture passport. DOC always requires this explanation. A source
review or DOC fixture cannot stand in for runtime evidence.

Manifest and registry digests are SHA-256 of sorted compact ASCII JSON followed
by one newline: `json.dumps(value, sort_keys=True, separators=(",", ":"),
ensure_ascii=True, allow_nan=False) + "\n"`. Artifact hashes cover exact UTF-8 bytes,
including line endings and non-ASCII text. References carry `path`, `sha256`, and,
for a run, `run_id`. No circular CaseResult hash goes into the run. Binary artifacts
must have a separate exact-byte transport before this text-bundle interface can
validate them; changing a binary digest to match decoded text is invalid.

Independent expected provenance uses nonempty `expected_source` and
`expected_method` plus immutable expected artifacts. Structural checks bind these
fields and bytes. They cannot prove that a prose claim is true or that expected
semantics were independently authored. QA must inspect provenance and reject an
oracle generated by the normalizer under test.

Timestamps use RFC 3339 with an explicit offset, uppercase `T`/`Z`, valid calendar
dates, and no leap-second spelling. Preparation, execution, and teardown have
separate finite positive deadlines and ordered intervals within the run. Optional
`duration_seconds` must agree with timestamps. Deadline overruns are valid only
as recorded FAIL evidence. `run_mode` defaults to `standard`, whose execution
deadline cannot exceed 1800 seconds; `extended` is explicit. Preparation and
teardown do not spend the standard execution budget. Runtime harness checks must
also prove that scenario actions and final comparison occurred in execution.

Optional D83 metadata uses `attempt_index` 1 or 2. Attempt 2 requires a distinct
`first_run_id` and a `retry_reason`; a correction can carry `correction_ref`.
The compatibility interface does not require these fields in its initial
conformance examples. Runtime harness and final acceptance must retain the first
FAIL, allow only one diagnostic retry, and verify plan/version/seed equality.
A successful diagnostic attempt does not erase an earlier case acceptance FAIL.

## Composite runs and prerequisites

A CompositeManifest lists required concrete profiles, child references with
their exact aggregate outcomes, one correlation ID, and a separate TC08
integration outcome. Every child must name this parent, match its reference and
profile, and pass full RunManifest validation. Duplicate, missing, substituted,
or mismatched children fail validation. Aggregate FAIL is required when any child
or integration outcome fails; it is valid evidence. A parent PASS cannot hide it.
Child correlation IDs, when supplied, must match the parent.

Parent timing metadata is optional in the initial linkage interface. If any of
`started_at`, `finished_at`, `phases`, or `resource_deadline_manifest_digest` is
supplied, all four are required. Parent execution must contain all child execution
intervals. Standard parent PASS cannot span more than 1800 seconds of child
execution even when parent timing is absent. Full parent scheduling, integration
timing, and READY admission remain harness obligations.

PrerequisitePlan uses E01 through E07. It may contain a partial stage set for
constraint checks. Every named dependency must exist and precede its dependent
stage; this rejects both cycles and acyclic future dependencies. E01 requires
`native-seam`, E03 requires `delivery`, and E05 requires `composed`. These are
stage evidence labels, not fixture kinds. A partial constraint plan does not
replace the coordinator's complete accepted task and gate graph.

## Remaining runtime inputs

ProfileManifest contents, actual native/supporting builds, fixture resolution,
resource/RunPlan artifacts, and actual D87 reader boundary evidence still need
their assigned implementation and real runs. The D87 schema records those
observations but does not produce them. The harness must preserve immutable
source, delivery, stream, and terminal-fence evidence through its output hashes.
The accepted corpus, source locks, and product specification are unchanged.

## D87 reader boundary record

`ReaderBoundaryManifest` uses schema version 1 and the coordinator's AUT-02
`reader-evidence-contract.md` interface, derived from D87. It never adds new
CaseResult verdicts. The CLI checks this artifact's structure, references,
identity bindings, and internal consistency. It does not replay or certify
native decisions, NATS ingress, Raft frontiers, message reconstruction, or oracle
semantics.

Every field below is required; nullable fields represent unavailable evidence.
All nested objects reject unknown fields.

| Field group | Contents |
|---|---|
| Identity | `schema_version=1`, `boundary_id`, `artifact_path`, six run axes, and complete `case_identities` |
| Execution | `run_started`, nullable `run_id` and `run_manifest_path`, nullable `run_plan_ref` and `oracle_ref` |
| Expectation | `expectation.mode` is `positive_delivery` or `declared_negative`; `predicate_ref` is null for positive delivery and required for a declared negative |
| Sources | `source_boundaries` with distinct source IDs and baseline, admission-set, terminal-frontier, and episode/binding/generation transition references |
| Delivery | `delivery_boundaries` with distinct destination/binding/episode/generation tuples, manifest chunks, highwater, seal, producer PubAck, attempt census, and attempt classification references |
| Streams | `streams` with distinct stream/destination/generation tuples, config, sequence bounds, seal, raw coverage/chunks, and terminal-fence references |
| Comparison | `logical_comparison.expected_ref`, `actual_ref`, and `diff_ref` |
| Phases | Exactly `startup`, `observation`, `shutdown_drain`, `stream_closure`, `reconstruction`, and `comparison` in `phase_times` |
| Conditions | Exactly the six independent conditions listed below, plus `reader_complete` and nullable `comparison_passed` |

The JSON schema defines the exact field spellings inside each group. Every
artifact reference is `{path, sha256}` and resolves through `context.artifacts`.
Large evidence sets are hash-bound chunk artifacts. This text-bundle validator
checks their exact UTF-8 bytes, without pretending their presence proves the
underlying runtime facts. Generations and stream sequences are nonnegative
integers. A known upper sequence cannot precede the lower sequence.

Each condition has `status`, `evidence_refs`, `reason`, and `missing_obligations`.
Statuses are `NOT_RUN`, `SATISFIED`, `FAILED`, and `INCOMPLETE`. SATISFIED requires
evidence and no missing obligations. FAILED requires evidence. INCOMPLETE names
at least one missing obligation. NOT_RUN has no condition evidence. Every status
requires a nonempty explanation.

| Condition | Required structure for SATISFIED |
|---|---|
| `source_closure` | At least one source; every baseline, admission-set, terminal-frontier, and transition reference is present |
| `delivery_closure` | At least one boundary; manifest references and all five highwater/seal/PubAck/census/classification references are present |
| `terminal_fence` | At least one stream; implementation, conformance, process exit/restart, ingress restriction, and recovered-frontier references are present |
| `stream_coverage` | At least one stream; lower/upper bounds and configuration/seal/coverage references; nonempty intervals also have raw-message references |
| `reconstruction` | Condition evidence; actual part/record/batch correctness remains a reader obligation |
| `semantic_comparison` | Expected, actual, and diff references; an empty diff still has a hash |

`reader_complete` is true exactly when the first five conditions are SATISFIED.
`comparison_passed` is true for SATISFIED semantic comparison, false for FAILED,
and null for NOT_RUN or INCOMPLETE. A complete reader can therefore retain a
semantic mismatch and a FAIL result. An empty stream interval remains
structurally possible, while independent runtime evidence must prove the absence
of publication obligations.

When execution starts, `context.manifests[run_manifest_path]` must be a valid
RunManifest with the same run ID, six axes, and exact case identity set. Its
`actual_artifacts` must include `artifact_path` exactly once with the SHA-256 of
this boundary's canonical JSON bytes. The same bytes must exist in
`context.artifacts`. The boundary names the run path/ID without hashing the run;
the run hashes the boundary. This avoids a circular digest. Checking only a
RunManifest checks its output hashes; call the ReaderBoundaryManifest validator
as well to check this typed output's content.

When `run_started=false`, the profile and case identities must already be
resolved and applicable. All conditions are NOT_RUN, flags are false/null, run
ID/path and phase timestamps are null, source/delivery/stream arrays are empty,
and logical comparison references are null. Prepared plan/oracle references may
be present or null. No invented RunManifest or actual output is required. A
failure before identity resolution remains an ordinary CaseResult NOT_RUN
diagnostic, without a fabricated reader boundary record.

Each phase has nullable `started_at`/`finished_at` and a finite positive
`deadline_seconds`. A finish requires a start, cannot precede it, and all actual
times lie in the linked run's execution interval. Phases may overlap. Timestamp
comparison preserves fractional seconds exactly, including nanoseconds and UTC
offsets.

For a started positive-delivery run whose case outcomes all report PASS, all six
conditions must be SATISFIED and every phase must finish within its deadline.
Recorded FAIL can retain failed, incomplete, or unstarted checks. A declared
negative can represent PASS with unsatisfied positive closure conditions only
with its hash-bound predeclared predicate. Structural validity never proves that
predicate or its forbidden-output checks. The later runtime comparator must do
that work, including all D87 counterexamples.
